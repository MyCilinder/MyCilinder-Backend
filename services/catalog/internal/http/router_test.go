package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/health"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware/jwtauth"
)

// fakeVerifier accepts "user" (no groups) and "operator" (operators group).
type fakeVerifier struct{}

func (fakeVerifier) Verify(_ context.Context, raw string) (jwtauth.Claims, error) {
	switch raw {
	case "user":
		return jwtauth.Claims{Subject: "11111111-1111-1111-1111-111111111111"}, nil
	case "operator":
		return jwtauth.Claims{Subject: "22222222-2222-2222-2222-222222222222", Groups: []string{"operators"}}, nil
	}
	return jwtauth.Claims{}, jwtauth.ErrInvalidToken
}

func do(t *testing.T, h http.Handler, method, path, token string) int {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(""))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestRouteAuth(t *testing.T) {
	h := NewRouter(Options{Verifier: fakeVerifier{}})
	const id = "33333333-3333-3333-3333-333333333333"
	tests := []struct {
		method, path, token string
		want                int
	}{
		// Public.
		{"GET", "/api/v1/feed", "", 501},
		{"GET", "/api/v1/search?q=campus", "", 501},
		{"GET", "/api/v1/creators/" + id + "/videos", "", 501},
		// Optional: guest and valid token pass, an invalid token is rejected.
		{"GET", "/api/v1/videos/" + id, "", 501},
		{"GET", "/api/v1/videos/" + id, "user", 501},
		{"GET", "/api/v1/videos/" + id, "bad", 401},
		{"GET", "/api/v1/videos/" + id + "/playback", "", 501},
		{"GET", "/api/v1/videos/" + id + "/playback", "bad", 401},
		// JWT.
		{"GET", "/api/v1/me/videos", "", 401},
		{"GET", "/api/v1/me/videos", "user", 501},
		{"PATCH", "/api/v1/videos/" + id, "", 401},
		{"PATCH", "/api/v1/videos/" + id, "user", 501},
		{"DELETE", "/api/v1/videos/" + id, "", 401},
		{"DELETE", "/api/v1/videos/" + id, "user", 501},
		// Operator.
		{"POST", "/api/v1/ops/videos/" + id + "/hide", "", 401},
		{"POST", "/api/v1/ops/videos/" + id + "/hide", "user", 403},
		{"POST", "/api/v1/ops/videos/" + id + "/hide", "operator", 501},
		{"POST", "/api/v1/ops/videos/" + id + "/restore", "user", 403},
		{"POST", "/api/v1/ops/videos/" + id + "/restore", "operator", 501},
		// Unknown route.
		{"GET", "/api/v1/nope", "", 404},
	}
	for _, tt := range tests {
		name := tt.method + " " + tt.path + " token=" + tt.token
		t.Run(name, func(t *testing.T) {
			if got := do(t, h, tt.method, tt.path, tt.token); got != tt.want {
				t.Errorf("status = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestHealthAndOrigin(t *testing.T) {
	failing := health.Check{Name: "database", Fn: func(context.Context) error { return errors.New("down") }}
	h := NewRouter(Options{
		Verifier:            fakeVerifier{},
		OriginVerifyEnabled: true,
		OriginVerifySecret:  "s3cret",
		ReadinessChecks:     []health.Check{failing},
	})
	if got := do(t, h, "GET", "/healthz", ""); got != 200 {
		t.Errorf("/healthz = %d, want 200", got)
	}
	if got := do(t, h, "GET", "/readyz", ""); got != 503 {
		t.Errorf("/readyz = %d, want 503", got)
	}
	// Without X-Origin-Verify the API rejects the request before routing.
	if got := do(t, h, "GET", "/api/v1/feed", ""); got != 403 {
		t.Errorf("/api/v1/feed without origin header = %d, want 403", got)
	}
}
