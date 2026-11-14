// Package user holds the profile type and the display-name rule shared by the store,
// the services, and the HTTP handlers.
package user

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// User is the local profile row (auth.users). ID is the Cognito sub.
type User struct {
	ID          string
	DisplayName string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ErrNotFound is returned when no profile exists for an id.
var ErrNotFound = errors.New("user not found")

// Display name limits from openapi.yaml (components.schemas.DisplayName) and the
// CHECK constraint on auth.users.
const (
	DisplayNameMin = 1
	DisplayNameMax = 50
)

// Rule ids used in problem `errors[].rule` (openapi FieldError).
const (
	RuleRequired = "required"
	RuleLength   = "length"
	RuleFormat   = "format"
)

// ValidationError reports which rule a display name broke.
type ValidationError struct {
	Rule    string
	Message string
}

func (e *ValidationError) Error() string { return "displayName: " + e.Message }

// NormalizeDisplayName trims surrounding whitespace and checks the 1-50 character limit
// (counted in Unicode code points, like PostgreSQL char_length). Control characters are
// rejected so names cannot break logs or client layouts.
func NormalizeDisplayName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", &ValidationError{Rule: RuleRequired, Message: "display name is required"}
	}
	if !utf8.ValidString(s) {
		return "", &ValidationError{Rule: RuleFormat, Message: "display name must be valid UTF-8"}
	}
	if n := utf8.RuneCountInString(s); n < DisplayNameMin || n > DisplayNameMax {
		return "", &ValidationError{Rule: RuleLength, Message: "display name must be 1-50 characters"}
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", &ValidationError{Rule: RuleFormat, Message: "display name must not contain control characters"}
		}
	}
	return s, nil
}
