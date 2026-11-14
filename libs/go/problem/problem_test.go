package problem

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWrite(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", nil)
	req = req.WithContext(WithCorrelationID(req.Context(), "abc-123"))
	rec := httptest.NewRecorder()

	Write(rec, req, http.StatusBadRequest, ValidationError, "email is required",
		FieldError{Field: "email", Rule: "required"})

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != ContentType {
		t.Errorf("content type = %q", ct)
	}

	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"type":          "/problems/validation-error",
		"title":         "Request validation failed",
		"status":        float64(400),
		"detail":        "email is required",
		"instance":      "/api/v1/auth/register",
		"correlationId": "abc-123",
	}
	for k, v := range want {
		if raw[k] != v {
			t.Errorf("%s = %v, want %v", k, raw[k], v)
		}
	}
	errs, ok := raw["errors"].([]any)
	if !ok || len(errs) != 1 {
		t.Fatalf("errors = %v", raw["errors"])
	}
	if e := errs[0].(map[string]any); e["field"] != "email" || e["rule"] != "required" {
		t.Errorf("errors[0] = %v", e)
	}
}

func TestWriteOmitsEmptyOptionalFields(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(rec, httptest.NewRequest(http.MethodGet, "/me", nil), http.StatusUnauthorized, Unauthenticated, "")

	var raw map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	for _, k := range []string{"detail", "errors"} {
		if _, present := raw[k]; present {
			t.Errorf("%s should be omitted, body %s", k, rec.Body.String())
		}
	}
	// correlationId is required by the contract, so it is always present.
	if _, present := raw["correlationId"]; !present {
		t.Error("correlationId must always be present")
	}
}
