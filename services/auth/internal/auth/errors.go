package auth

import (
	"errors"
	"time"
)

// Errors returned by Service. The HTTP layer maps each to a problem type.
var (
	// ErrInvalidCredentials: wrong email or password, or unknown email (401).
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	// ErrNotConfirmed: the account exists but the email is not verified (403,
	// rule account-not-confirmed).
	ErrNotConfirmed = errors.New("auth: account not confirmed")
	// ErrInvalidCode and ErrExpiredCode: confirmation failures (400).
	ErrInvalidCode = errors.New("auth: invalid verification code")
	ErrExpiredCode = errors.New("auth: expired verification code")
	// ErrInvalidRefreshToken: invalid, expired, or revoked refresh token (401).
	ErrInvalidRefreshToken = errors.New("auth: invalid refresh token")
	// ErrRateLimited is matched by *RateLimitedError (429).
	ErrRateLimited = errors.New("auth: rate limited")
	// ErrUnavailable: Cognito or the database failed (503).
	ErrUnavailable = errors.New("auth: dependency unavailable")
)

// PasswordPolicyError reports which password rule failed (400, rule password-policy).
type PasswordPolicyError struct{ Message string }

func (e *PasswordPolicyError) Error() string { return "auth: password policy: " + e.Message }

// RateLimitedError tells the client how long to wait (429 with Retry-After).
type RateLimitedError struct {
	RetryAfter time.Duration
	cause      error
}

func (e *RateLimitedError) Error() string        { return ErrRateLimited.Error() }
func (e *RateLimitedError) Is(target error) bool { return target == ErrRateLimited }
func (e *RateLimitedError) Unwrap() error        { return e.cause }

// NewRateLimitedError is used by LoginThrottle implementations.
func NewRateLimitedError(retryAfter time.Duration) *RateLimitedError {
	return &RateLimitedError{RetryAfter: retryAfter}
}

// InvalidInputError: Cognito rejected a parameter our validation accepted (400).
type InvalidInputError struct {
	Message string
	cause   error
}

func (e *InvalidInputError) Error() string { return "auth: invalid input: " + e.Message }
func (e *InvalidInputError) Unwrap() error { return e.cause }
