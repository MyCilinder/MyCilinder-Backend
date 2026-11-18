package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/problem"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/user"
)

// maxBodyBytes bounds request bodies. The largest auth body is a refresh token (a few KB).
const maxBodyBytes = 16 << 10

// decode reads a single JSON object into dst. Unknown fields are rejected because every
// request schema has additionalProperties: false. On failure it writes a 400 problem and
// returns false.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()

	err := dec.Decode(dst)
	if err == nil && dec.More() {
		err = errors.New("body must contain a single JSON object")
	}
	if err == nil {
		return true
	}

	var (
		syntaxErr *json.SyntaxError
		typeErr   *json.UnmarshalTypeError
		maxErr    *http.MaxBytesError
	)
	switch {
	case errors.Is(err, io.EOF):
		writeValidation(w, r, "request body is required")
	case errors.As(err, &maxErr):
		writeValidation(w, r, "request body is too large")
	case errors.As(err, &typeErr):
		writeValidation(w, r, fmt.Sprintf("%s has the wrong type", typeErr.Field),
			problem.FieldError{Field: typeErr.Field, Rule: "type"})
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		field := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
		writeValidation(w, r, "unknown field "+field, problem.FieldError{Field: field, Rule: "unknown-field"})
	case errors.As(err, &syntaxErr), errors.Is(err, io.ErrUnexpectedEOF):
		writeValidation(w, r, "request body must be valid JSON")
	default:
		writeValidation(w, r, "request body must be a JSON object")
	}
	return false
}

func writeValidation(w http.ResponseWriter, r *http.Request, detail string, errs ...problem.FieldError) {
	problem.Write(w, r, http.StatusBadRequest, problem.ValidationError, detail, errs...)
}

// validator collects field errors.
type validator struct{ errs []problem.FieldError }

func (v *validator) add(field, rule, msg string) {
	v.errs = append(v.errs, problem.FieldError{Field: field, Rule: rule, Message: msg})
}

func (v *validator) ok() bool { return len(v.errs) == 0 }

// write sends the collected errors; detail is the first message.
func (v *validator) write(w http.ResponseWriter, r *http.Request) {
	writeValidation(w, r, v.errs[0].Message, v.errs...)
}

// email validates and normalizes an address: trimmed and lower-cased, so the same
// mailbox always maps to the same Cognito username and throttle key.
func (v *validator) email(raw string) string {
	e := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case e == "":
		v.add("email", "required", "email is required")
	case len(e) > 254:
		v.add("email", "length", "email must be at most 254 characters")
	default:
		addr, err := mail.ParseAddress(e)
		if err != nil || addr.Address != e || addr.Name != "" {
			v.add("email", "format", "email is not a valid address")
		}
	}
	return e
}

// password checks the schema bounds only; the policy itself is enforced by Cognito and
// reported as rule password-policy (SEC-11).
func (v *validator) password(raw string, min int) {
	n := utf8.RuneCountInString(raw)
	switch {
	case raw == "":
		v.add("password", "required", "password is required")
	case n < min || len(raw) > 256:
		v.add("password", "length", fmt.Sprintf("password must be %d-256 characters", min))
	}
}

func (v *validator) displayName(raw string) string {
	name, err := user.NormalizeDisplayName(raw)
	var ve *user.ValidationError
	if errors.As(err, &ve) {
		v.add("displayName", ve.Rule, ve.Message)
	}
	return name
}

var codeRe = regexp.MustCompile(`^[0-9]{6}$`)

func (v *validator) code(raw string) string {
	c := strings.TrimSpace(raw)
	switch {
	case c == "":
		v.add("code", "required", "code is required")
	case !codeRe.MatchString(c):
		v.add("code", "format", "code must be 6 digits")
	}
	return c
}

func (v *validator) refreshToken(raw string) string {
	t := strings.TrimSpace(raw)
	if t == "" {
		v.add("refreshToken", "required", "refreshToken is required")
	}
	return t
}
