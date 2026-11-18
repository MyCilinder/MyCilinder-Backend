package cognito

import (
	"errors"
	"fmt"

	"github.com/aws/smithy-go"
)

// Domain errors. The auth service maps them to problem types; nothing outside this
// package inspects Cognito exception names.
var (
	ErrUserExists       = errors.New("cognito: user already exists")
	ErrInvalidPassword  = errors.New("cognito: password does not meet the policy")
	ErrInvalidCode      = errors.New("cognito: invalid verification code")
	ErrExpiredCode      = errors.New("cognito: expired verification code")
	ErrNotConfirmed     = errors.New("cognito: user not confirmed")
	ErrNotAuthorized    = errors.New("cognito: not authorized")
	ErrUserNotFound     = errors.New("cognito: user not found")
	ErrAlreadyConfirmed = errors.New("cognito: user already confirmed")
	ErrInvalidParameter = errors.New("cognito: invalid parameter")
	ErrTooManyRequests  = errors.New("cognito: too many requests")
	ErrUnavailable      = errors.New("cognito: unavailable")
)

// InvalidPasswordError carries Cognito's explanation of which policy rule failed, so the
// facade can return it in errors[].message (UC-01 3b).
type InvalidPasswordError struct{ Message string }

func (e *InvalidPasswordError) Error() string { return ErrInvalidPassword.Error() + ": " + e.Message }
func (e *InvalidPasswordError) Unwrap() error { return ErrInvalidPassword }

// mapError converts an SDK error to a domain error. The original error stays in the chain
// for logging. Unknown errors, timeouts, and network failures become ErrUnavailable.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	var target error
	switch apiErr.ErrorCode() {
	case "UsernameExistsException", "AliasExistsException":
		target = ErrUserExists
	case "InvalidPasswordException":
		return fmt.Errorf("%w", &InvalidPasswordError{Message: apiErr.ErrorMessage()})
	case "CodeMismatchException":
		target = ErrInvalidCode
	case "ExpiredCodeException":
		target = ErrExpiredCode
	case "UserNotConfirmedException":
		target = ErrNotConfirmed
	case "NotAuthorizedException", "RefreshTokenReuseException", "UnauthorizedException":
		// Also returned for confirm/resend on an already confirmed user; the auth service
		// decides how to present that per operation.
		target = ErrNotAuthorized
	case "UserNotFoundException":
		target = ErrUserNotFound
	case "InvalidParameterException", "UnsupportedTokenTypeException":
		target = ErrInvalidParameter
	case "TooManyRequestsException", "LimitExceededException", "TooManyFailedAttemptsException", "CodeDeliveryFailureException":
		target = ErrTooManyRequests
	default:
		target = ErrUnavailable
	}
	return fmt.Errorf("%w: %w", target, err)
}
