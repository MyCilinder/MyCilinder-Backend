package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware/jwtauth"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware/ratelimit"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/problem"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/auth"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/profile"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/user"
)

// AuthService is implemented by *auth.Service.
type AuthService interface {
	Register(ctx context.Context, in auth.RegisterInput) error
	Confirm(ctx context.Context, email, code string) error
	ResendCode(ctx context.Context, email string) error
	Login(ctx context.Context, in auth.LoginInput) (auth.Tokens, error)
	Refresh(ctx context.Context, refreshToken string) (auth.Tokens, error)
	Logout(ctx context.Context, sub, refreshToken string) error
}

// ProfileService is implemented by *profile.Service.
type ProfileService interface {
	Get(ctx context.Context, sub, accessToken string) (profile.Profile, error)
	UpdateDisplayName(ctx context.Context, sub, accessToken, displayName string) (profile.Profile, error)
}

type handlers struct {
	auth     AuthService
	profile  ProfileService
	clientIP func(*http.Request) string
}

// --- request and response bodies (openapi.yaml components.schemas) ---

type registerRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
}

type confirmRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

type resendRequest struct {
	Email string `json:"email"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type refreshTokenRequest struct {
	RefreshToken string `json:"refreshToken"`
}

type updateProfileRequest struct {
	DisplayName string `json:"displayName"`
}

type tokenSet struct {
	AccessToken  string `json:"accessToken"`
	IDToken      string `json:"idToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int    `json:"expiresIn"`
	TokenType    string `json:"tokenType"`
}

type refreshedTokenSet struct {
	AccessToken string `json:"accessToken"`
	IDToken     string `json:"idToken"`
	ExpiresIn   int    `json:"expiresIn"`
	TokenType   string `json:"tokenType"`
}

type myProfile struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	CreatedAt   string `json:"createdAt"`
}

// --- auth handlers ---

func (h *handlers) register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !decode(w, r, &req) {
		return
	}
	var v validator
	in := auth.RegisterInput{Email: v.email(req.Email), DisplayName: v.displayName(req.DisplayName), Password: req.Password}
	v.password(req.Password, 8)
	if !v.ok() {
		v.write(w, r)
		return
	}
	if err := h.auth.Register(r.Context(), in); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *handlers) confirm(w http.ResponseWriter, r *http.Request) {
	var req confirmRequest
	if !decode(w, r, &req) {
		return
	}
	var v validator
	email, code := v.email(req.Email), v.code(req.Code)
	if !v.ok() {
		v.write(w, r)
		return
	}
	if err := h.auth.Confirm(r.Context(), email, code); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) resendCode(w http.ResponseWriter, r *http.Request) {
	var req resendRequest
	if !decode(w, r, &req) {
		return
	}
	var v validator
	email := v.email(req.Email)
	if !v.ok() {
		v.write(w, r)
		return
	}
	if err := h.auth.ResendCode(r.Context(), email); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decode(w, r, &req) {
		return
	}
	var v validator
	email := v.email(req.Email)
	v.password(req.Password, 1)
	if !v.ok() {
		v.write(w, r)
		return
	}
	tok, err := h.auth.Login(r.Context(), auth.LoginInput{Email: email, Password: req.Password, ClientIP: h.clientIP(r)})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeTokens(w, http.StatusOK, tokenSet{
		AccessToken: tok.AccessToken, IDToken: tok.IDToken, RefreshToken: tok.RefreshToken,
		ExpiresIn: tok.ExpiresIn, TokenType: "Bearer",
	})
}

func (h *handlers) refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshTokenRequest
	if !decode(w, r, &req) {
		return
	}
	var v validator
	rt := v.refreshToken(req.RefreshToken)
	if !v.ok() {
		v.write(w, r)
		return
	}
	tok, err := h.auth.Refresh(r.Context(), rt)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeTokens(w, http.StatusOK, refreshedTokenSet{
		AccessToken: tok.AccessToken, IDToken: tok.IDToken, ExpiresIn: tok.ExpiresIn, TokenType: "Bearer",
	})
}

func (h *handlers) logout(w http.ResponseWriter, r *http.Request) {
	claims, _ := jwtauth.FromContext(r.Context())
	var req refreshTokenRequest
	if !decode(w, r, &req) {
		return
	}
	var v validator
	rt := v.refreshToken(req.RefreshToken)
	if !v.ok() {
		v.write(w, r)
		return
	}
	err := h.auth.Logout(r.Context(), claims.Subject, rt)
	if errors.Is(err, auth.ErrInvalidRefreshToken) {
		// The caller is authenticated; the body is what is wrong.
		writeValidation(w, r, "refreshToken is not a valid refresh token",
			problem.FieldError{Field: "refreshToken", Rule: "format"})
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- profile handlers ---

func (h *handlers) getMe(w http.ResponseWriter, r *http.Request) {
	claims, _ := jwtauth.FromContext(r.Context())
	p, err := h.profile.Get(r.Context(), claims.Subject, claims.Token)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toMyProfile(p))
}

func (h *handlers) patchMe(w http.ResponseWriter, r *http.Request) {
	claims, _ := jwtauth.FromContext(r.Context())
	var req updateProfileRequest
	if !decode(w, r, &req) {
		return
	}
	p, err := h.profile.UpdateDisplayName(r.Context(), claims.Subject, claims.Token, req.DisplayName)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toMyProfile(p))
}

func toMyProfile(p profile.Profile) myProfile {
	return myProfile{ID: p.ID, Email: p.Email, DisplayName: p.DisplayName, CreatedAt: p.CreatedAt.UTC().Format(time.RFC3339)}
}

// --- responses ---

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeTokens sends a token response that must not be cached anywhere.
func writeTokens(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, status, body)
}

// writeError maps service errors to problem responses (Interface Design §1.1).
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		policy  *auth.PasswordPolicyError
		limited *auth.RateLimitedError
		input   *auth.InvalidInputError
		name    *user.ValidationError
	)
	switch {
	case errors.As(err, &limited):
		ratelimit.WriteRateLimited(w, r, limited.RetryAfter)
	case errors.As(err, &policy):
		writeValidation(w, r, policy.Message,
			problem.FieldError{Field: "password", Rule: "password-policy", Message: policy.Message})
	case errors.As(err, &name):
		writeValidation(w, r, name.Message, problem.FieldError{Field: "displayName", Rule: name.Rule, Message: name.Message})
	case errors.As(err, &input):
		writeValidation(w, r, input.Message)
	case errors.Is(err, auth.ErrInvalidCode):
		writeValidation(w, r, "the verification code is invalid",
			problem.FieldError{Field: "code", Rule: "invalid-code"})
	case errors.Is(err, auth.ErrExpiredCode):
		writeValidation(w, r, "the verification code has expired; request a new one",
			problem.FieldError{Field: "code", Rule: "expired-code"})
	case errors.Is(err, auth.ErrInvalidCredentials):
		problem.Write(w, r, http.StatusUnauthorized, problem.Unauthenticated, "invalid email or password")
	case errors.Is(err, auth.ErrInvalidRefreshToken):
		problem.Write(w, r, http.StatusUnauthorized, problem.Unauthenticated, "the refresh token is invalid, expired, or revoked")
	case errors.Is(err, profile.ErrUnauthenticated):
		problem.Write(w, r, http.StatusUnauthorized, problem.Unauthenticated, "the access token is no longer valid")
	case errors.Is(err, auth.ErrNotConfirmed):
		problem.Write(w, r, http.StatusForbidden, problem.Forbidden, "the account is not confirmed",
			problem.FieldError{Rule: "account-not-confirmed"})
	case errors.Is(err, auth.ErrUnavailable), errors.Is(err, profile.ErrUnavailable):
		middleware.Logger(r.Context()).Error("dependency unavailable", "err", err)
		problem.Write(w, r, http.StatusServiceUnavailable, problem.Unavailable, "")
	default:
		middleware.Logger(r.Context()).Error("unexpected error", "err", err)
		problem.Write(w, r, http.StatusInternalServerError, problem.Internal, "")
	}
}
