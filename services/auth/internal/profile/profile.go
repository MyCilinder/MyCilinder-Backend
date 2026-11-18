// Package profile implements GET /me and PATCH /me (FR-AUTH-08).
//
// The local profile stores only the display name; the email is read from Cognito with the
// caller's access token on each request (SEC-12). That GetUser call also rejects access
// tokens revoked by logout, which local JWT validation cannot detect.
package profile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/cognito"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/user"
)

// Cognito reads the caller's attributes.
type Cognito interface {
	GetUser(ctx context.Context, accessToken string) (cognito.UserInfo, error)
}

// Users is the profile repository.
type Users interface {
	EnsureUser(ctx context.Context, id, displayName string) (bool, error)
	Get(ctx context.Context, id string) (user.User, error)
	UpdateDisplayName(ctx context.Context, id, displayName string) (user.User, error)
}

var (
	// ErrUnauthenticated: Cognito rejected the access token (revoked or user disabled).
	ErrUnauthenticated = errors.New("profile: token rejected by identity provider")
	// ErrUnavailable: Cognito or the database failed.
	ErrUnavailable = errors.New("profile: dependency unavailable")
)

// Profile is the MyProfile response.
type Profile struct {
	ID          string
	Email       string
	DisplayName string
	CreatedAt   time.Time
}

// Service implements the profile use cases.
type Service struct {
	cognito Cognito
	users   Users
}

// New returns a Service.
func New(c Cognito, u Users) *Service { return &Service{cognito: c, users: u} }

// Get returns the caller's profile. If no local row exists (for example, the database was
// down during the first login) it is created here from the Cognito name.
func (s *Service) Get(ctx context.Context, sub, accessToken string) (Profile, error) {
	info, err := s.userInfo(ctx, sub, accessToken)
	if err != nil {
		return Profile{}, err
	}
	u, err := s.users.Get(ctx, sub)
	if errors.Is(err, user.ErrNotFound) {
		u, err = s.create(ctx, sub, info.DisplayName)
	}
	if err != nil {
		return Profile{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return toProfile(u, info.Email), nil
}

// UpdateDisplayName validates and stores a new display name. Invalid names return a
// *user.ValidationError.
func (s *Service) UpdateDisplayName(ctx context.Context, sub, accessToken, displayName string) (Profile, error) {
	name, err := user.NormalizeDisplayName(displayName)
	if err != nil {
		return Profile{}, err
	}
	info, err := s.userInfo(ctx, sub, accessToken)
	if err != nil {
		return Profile{}, err
	}
	u, err := s.users.UpdateDisplayName(ctx, sub, name)
	if errors.Is(err, user.ErrNotFound) {
		u, err = s.create(ctx, sub, name)
	}
	if err != nil {
		return Profile{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return toProfile(u, info.Email), nil
}

func (s *Service) userInfo(ctx context.Context, sub, accessToken string) (cognito.UserInfo, error) {
	info, err := s.cognito.GetUser(ctx, accessToken)
	switch {
	case err == nil:
	case errors.Is(err, cognito.ErrNotAuthorized), errors.Is(err, cognito.ErrUserNotFound):
		return cognito.UserInfo{}, ErrUnauthenticated
	default:
		return cognito.UserInfo{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if info.Sub != "" && info.Sub != sub {
		// The JWT and Cognito disagree about who this is; refuse rather than guess.
		return cognito.UserInfo{}, ErrUnauthenticated
	}
	return info, nil
}

func (s *Service) create(ctx context.Context, sub, name string) (user.User, error) {
	n, err := user.NormalizeDisplayName(name)
	if err != nil {
		n = "user"
	}
	if _, err := s.users.EnsureUser(ctx, sub, n); err != nil {
		return user.User{}, err
	}
	return s.users.Get(ctx, sub)
}

func toProfile(u user.User, email string) Profile {
	return Profile{ID: u.ID, Email: email, DisplayName: u.DisplayName, CreatedAt: u.CreatedAt}
}
