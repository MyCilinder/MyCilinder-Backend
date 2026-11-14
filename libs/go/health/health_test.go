package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func serve(t *testing.T, h http.HandlerFunc) (int, response) {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	var body response
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON %q: %v", rec.Body.String(), err)
	}
	return rec.Code, body
}

func TestLiveness(t *testing.T) {
	code, body := serve(t, Liveness())
	if code != http.StatusOK || body.Status != "ok" {
		t.Fatalf("got %d %+v", code, body)
	}
}

func TestReadiness(t *testing.T) {
	ok := Check{Name: "db", Fn: func(context.Context) error { return nil }}
	bad := Check{Name: "redis", Fn: func(context.Context) error { return errors.New("down") }}
	slow := Check{Name: "slow", Fn: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}

	tests := []struct {
		name   string
		checks []Check
		code   int
		want   map[string]string
	}{
		{"no checks", nil, http.StatusOK, map[string]string{}},
		{"all pass", []Check{ok}, http.StatusOK, map[string]string{"db": "ok"}},
		{"one fails", []Check{ok, bad}, http.StatusServiceUnavailable, map[string]string{"db": "ok", "redis": "fail"}},
		{"timeout", []Check{slow}, http.StatusServiceUnavailable, map[string]string{"slow": "fail"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body := serve(t, Readiness(50*time.Millisecond, tt.checks...))
			if code != tt.code {
				t.Errorf("code = %d, want %d", code, tt.code)
			}
			for k, v := range tt.want {
				if body.Checks[k] != v {
					t.Errorf("check %s = %q, want %q", k, body.Checks[k], v)
				}
			}
		})
	}
}
