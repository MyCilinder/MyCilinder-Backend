// Package auth implements the sign-up, login, refresh, and logout use cases of the auth
// facade (Architecture §4.2, Interface Design §3.1).
//
// It translates Cognito outcomes into the errors in errors.go, which the HTTP layer maps
// to problem types. The rules that matter for security live here:
//   - register and resend-code never reveal whether an email is registered (UC-01 3a);
//   - login failures are generic and throttled (UC-02 2a, SEC-09);
//   - the local profile is created on first login (FR-AUTH-05);
//   - audit rows never contain emails, passwords, or tokens (SEC-12).
package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/cognito"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/store"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/user"
)

// Cognito is the part of the Cognito adapter the service uses.
type Cognito interface {
	SignUp(ctx context.Context, email, password, displayName string) (sub string, err error)
	ConfirmSignUp(ctx context.Context, email, code string) error
	ResendCode(ctx context.Context, email string) error
	Login(ctx context.Context, email, password string) (cognito.Tokens, error)
	Refresh(ctx context.Context, refreshToken string) (cognito.Tokens, error)
	Revoke(ctx context.Context, refreshToken string) error
}

// Users creates the local profile.
type Users interface {
	EnsureUser(ctx context.Context, id, displayName string) (created bool, err error)
}

// Audit appends audit rows.
type Audit interface {
	Write(ctx context.Context, e store.AuditEntry) error
}

// LoginThrottle limits failed logins per email and per client IP (SEC-09). Check returns
// ErrRateLimited (as a *RateLimitedError) when the caller must wait. Implementations fail
// open: when their backing store is down they allow the attempt.
type LoginThrottle interface {
	Check(ctx context.Context, email, clientIP string) error
	RecordFailure(ctx context.Context, email, clientIP string)
	Reset(ctx context.Context, email string)
}

// Tokens is the token set returned to the client.
type Tokens struct {
	AccessToken  string
	IDToken      string
	RefreshToken string // empty after a refresh
	ExpiresIn    int
}

// Service holds the dependencies of the use cases.
type Service struct {
	cognito  Cognito
	users    Users
	audit    Audit
	throttle LoginThrottle
}

// New returns a Service. throttle may be nil, which disables login throttling.
func New(c Cognito, u Users, a Audit, throttle LoginThrottle) *Service {
	if throttle == nil {
		throttle = noThrottle{}
	}
	return &Service{cognito: c, users: u, audit: a, throttle: throttle}
}

// RegisterInput is a validated sign-up request.
type RegisterInput struct {
	Email, Password, DisplayName string
}

// Register starts Cognito sign-up (FR-AUTH-01). An already registered email returns nil,
// exactly like a new one, so the response does not reveal which emails exist.
func (s *Service) Register(ctx context.Context, in RegisterInput) error {
	sub, err := s.cognito.SignUp(ctx, in.Email, in.Password, in.DisplayName)
	switch {
	case err == nil:
		s.writeAudit(ctx, store.AuditEntry{Action: store.ActionRegister, ActorID: sub, TargetType: "user", TargetID: sub})
		return nil
	case errors.Is(err, cognito.ErrUserExists):
		return nil
	}
	var pe *cognito.InvalidPasswordError
	if errors.As(err, &pe) {
		return &PasswordPolicyError{Message: cognitoPolicyMessage(pe.Message)}
	}
	return mapCommon(err)
}

// Confirm confirms the account with the emailed code (FR-AUTH-02). An unknown email and
// an already confirmed account both report ErrInvalidCode, so the endpoint cannot be used
// to probe which emails exist.
func (s *Service) Confirm(ctx context.Context, email, code string) error {
	err := s.cognito.ConfirmSignUp(ctx, email, code)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, cognito.ErrExpiredCode):
		return ErrExpiredCode
	case errors.Is(err, cognito.ErrInvalidCode),
		errors.Is(err, cognito.ErrUserNotFound),
		errors.Is(err, cognito.ErrNotAuthorized):
		return ErrInvalidCode
	}
	return mapCommon(err)
}

// ResendCode sends a new verification code (UC-01 5a). It succeeds for unknown and
// already confirmed emails too; only rate limits and outages are reported.
func (s *Service) ResendCode(ctx context.Context, email string) error {
	err := s.cognito.ResendCode(ctx, email)
	switch {
	case err == nil,
		errors.Is(err, cognito.ErrUserNotFound),
		errors.Is(err, cognito.ErrInvalidParameter), // "user is already confirmed"
		errors.Is(err, cognito.ErrNotAuthorized):
		return nil
	}
	return mapCommon(err)
}

// LoginInput is a validated login request. ClientIP feeds the throttle.
type LoginInput struct {
	Email, Password, ClientIP string
}

// Login authenticates with Cognito (FR-AUTH-03) and creates the profile on first login
// (FR-AUTH-05).
func (s *Service) Login(ctx context.Context, in LoginInput) (Tokens, error) {
	if err := s.throttle.Check(ctx, in.Email, in.ClientIP); err != nil {
		return Tokens{}, err
	}

	tok, err := s.cognito.Login(ctx, in.Email, in.Password)
	if err != nil {
		switch {
		case errors.Is(err, cognito.ErrNotAuthorized), errors.Is(err, cognito.ErrUserNotFound):
			s.throttle.RecordFailure(ctx, in.Email, in.ClientIP)
			s.writeAudit(ctx, store.AuditEntry{Action: store.ActionLoginFailed, Details: map[string]any{"reason": "invalid_credentials"}})
			return Tokens{}, ErrInvalidCredentials
		case errors.Is(err, cognito.ErrNotConfirmed):
			s.writeAudit(ctx, store.AuditEntry{Action: store.ActionLoginFailed, Details: map[string]any{"reason": "not_confirmed"}})
			return Tokens{}, ErrNotConfirmed
		}
		return Tokens{}, mapCommon(err)
	}

	claims, err := idTokenClaims(tok.IDToken)
	if err != nil {
		return Tokens{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	created, err := s.users.EnsureUser(ctx, claims.Sub, profileName(claims))
	if err != nil {
		// Without a profile the user would be invisible to Catalog and Interactions, so
		// the login fails. The issued tokens are not returned and expire on their own.
		return Tokens{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	s.throttle.Reset(ctx, in.Email)
	s.writeAudit(ctx, store.AuditEntry{
		Action: store.ActionLoginSucceeded, ActorID: claims.Sub, TargetType: "user", TargetID: claims.Sub,
		Details: map[string]any{"firstLogin": created},
	})
	return Tokens{
		AccessToken: tok.AccessToken, IDToken: tok.IDToken, RefreshToken: tok.RefreshToken,
		ExpiresIn: int(tok.ExpiresIn),
	}, nil
}

// Refresh exchanges a refresh token for new access and ID tokens (FR-AUTH-04).
func (s *Service) Refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	tok, err := s.cognito.Refresh(ctx, refreshToken)
	if err != nil {
		if errors.Is(err, cognito.ErrNotAuthorized) || errors.Is(err, cognito.ErrInvalidParameter) {
			return Tokens{}, ErrInvalidRefreshToken
		}
		return Tokens{}, mapCommon(err)
	}
	return Tokens{AccessToken: tok.AccessToken, IDToken: tok.IDToken, ExpiresIn: int(tok.ExpiresIn)}, nil
}

// Logout revokes the refresh token (FR-AUTH-06). Revoking an already revoked token
// succeeds, so the call is idempotent.
func (s *Service) Logout(ctx context.Context, sub, refreshToken string) error {
	err := s.cognito.Revoke(ctx, refreshToken)
	switch {
	case err == nil, errors.Is(err, cognito.ErrNotAuthorized):
	case errors.Is(err, cognito.ErrInvalidParameter):
		return ErrInvalidRefreshToken
	default:
		return mapCommon(err)
	}
	s.writeAudit(ctx, store.AuditEntry{Action: store.ActionLogout, ActorID: sub, TargetType: "user", TargetID: sub})
	return nil
}

// writeAudit records an event. A failed audit write is logged but does not fail the
// request: the event is also in the structured logs (Data Design §1.3).
func (s *Service) writeAudit(ctx context.Context, e store.AuditEntry) {
	if err := s.audit.Write(ctx, e); err != nil {
		middleware.Logger(ctx).Error("audit write failed", "action", e.Action, "err", err)
		return
	}
	middleware.Logger(ctx).Info("audit", "action", e.Action, "actorId", e.ActorID)
}

// mapCommon maps the errors every operation can return.
func mapCommon(err error) error {
	switch {
	case errors.Is(err, cognito.ErrTooManyRequests):
		return &RateLimitedError{RetryAfter: time.Minute, cause: err}
	case errors.Is(err, cognito.ErrInvalidParameter):
		return &InvalidInputError{Message: "the request was rejected by the identity provider", cause: err}
	}
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}

// cognitoPolicyMessage strips Cognito's error prefix ("Password did not conform with
// policy: ...") to the rule that failed.
func cognitoPolicyMessage(msg string) string {
	if i := strings.Index(msg, ": "); i >= 0 && i+2 < len(msg) {
		return msg[i+2:]
	}
	if msg == "" {
		return "password does not meet the policy"
	}
	return msg
}

type idClaims struct {
	Sub  string `json:"sub"`
	Name string `json:"name"`
}

// idTokenClaims reads sub and name from the ID token without verifying its signature.
// That is safe here only because the token was just received from Cognito over TLS in
// response to our own call; it is never used to authorize a client request.
func idTokenClaims(idToken string) (idClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return idClaims{}, errors.New("auth: malformed ID token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return idClaims{}, fmt.Errorf("auth: decode ID token: %w", err)
	}
	var c idClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return idClaims{}, fmt.Errorf("auth: parse ID token: %w", err)
	}
	if c.Sub == "" {
		return idClaims{}, errors.New("auth: ID token has no sub")
	}
	return c, nil
}

// profileName is the display name for a new profile: the Cognito `name` attribute, or a
// neutral fallback derived from the sub when it is missing or invalid (for example a user
// created in the console). The email is never used, because display names are public.
func profileName(c idClaims) string {
	if n, err := user.NormalizeDisplayName(c.Name); err == nil {
		return n
	}
	id := strings.ReplaceAll(c.Sub, "-", "")
	if len(id) > 8 {
		id = id[:8]
	}
	return "user-" + id
}

type noThrottle struct{}

func (noThrottle) Check(context.Context, string, string) error   { return nil }
func (noThrottle) RecordFailure(context.Context, string, string) {}
func (noThrottle) Reset(context.Context, string)                 {}
