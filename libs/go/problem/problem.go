// Package problem writes RFC 9457 Problem Details responses (Interface Design §1.1).
package problem

import (
	"context"
	"encoding/json"
	"net/http"
)

// Type is a problem type URI reference from the API contract.
type Type string

// Problem types from openapi.yaml (components.schemas.Problem.type).
const (
	ValidationError Type = "/problems/validation-error"
	Unauthenticated Type = "/problems/unauthenticated"
	Forbidden       Type = "/problems/forbidden"
	NotFound        Type = "/problems/not-found"
	Conflict        Type = "/problems/conflict"
	LimitExceeded   Type = "/problems/limit-exceeded"
	RateLimited     Type = "/problems/rate-limited"
	QuotaExceeded   Type = "/problems/quota-exceeded"
	Unavailable     Type = "/problems/unavailable"

	// Internal and NotImplemented are not in the public contract. Internal covers
	// unexpected failures (500); NotImplemented is used by stub handlers while the
	// service is being built.
	Internal       Type = "/problems/internal"
	NotImplemented Type = "/problems/not-implemented"
)

var titles = map[Type]string{
	ValidationError: "Request validation failed",
	Unauthenticated: "Authentication required",
	Forbidden:       "Forbidden",
	NotFound:        "Resource not found",
	Conflict:        "Conflict",
	LimitExceeded:   "Limit exceeded",
	RateLimited:     "Too many requests",
	QuotaExceeded:   "Quota exceeded",
	Unavailable:     "Service unavailable",
	Internal:        "Internal error",
	NotImplemented:  "Not implemented",
}

// FieldError is one entry of the errors array (openapi FieldError).
type FieldError struct {
	Field   string `json:"field,omitempty"`
	Rule    string `json:"rule"`
	Message string `json:"message,omitempty"`
}

// Details is the response body.
type Details struct {
	Type          Type         `json:"type"`
	Title         string       `json:"title"`
	Status        int          `json:"status"`
	Detail        string       `json:"detail,omitempty"`
	Instance      string       `json:"instance,omitempty"`
	CorrelationID string       `json:"correlationId"`
	Errors        []FieldError `json:"errors,omitempty"`
}

// ContentType is the media type of problem responses.
const ContentType = "application/problem+json"

// correlationKey is set by the correlation middleware through WithCorrelationID, so this
// package does not import the middleware package.
type correlationKey struct{}

// WithCorrelationID stores the correlation id in ctx.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationKey{}, id)
}

// CorrelationID returns the id stored by WithCorrelationID, or "".
func CorrelationID(ctx context.Context) string {
	id, _ := ctx.Value(correlationKey{}).(string)
	return id
}

// Write sends a problem response. detail may be empty.
func Write(w http.ResponseWriter, r *http.Request, status int, t Type, detail string, errs ...FieldError) {
	title, ok := titles[t]
	if !ok {
		title = http.StatusText(status)
	}
	body := Details{
		Type:          t,
		Title:         title,
		Status:        status,
		Detail:        detail,
		Instance:      r.URL.Path,
		CorrelationID: CorrelationID(r.Context()),
		Errors:        errs,
	}
	w.Header().Set("Content-Type", ContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
