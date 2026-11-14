package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/problem"
)

func TestRouterStubsReturnProblem(t *testing.T) {
	h := NewRouter(Options{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	req.Header.Set(middleware.CorrelationHeader, "test-corr-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d", rec.Code)
	}
	var body problem.Details
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.CorrelationID != "test-corr-1" || rec.Header().Get(middleware.CorrelationHeader) != "test-corr-1" {
		t.Errorf("correlation id not propagated: header %q body %q",
			rec.Header().Get(middleware.CorrelationHeader), body.CorrelationID)
	}
}

func TestRouterOriginVerifySkipsHealth(t *testing.T) {
	h := NewRouter(Options{OriginVerifyEnabled: true, OriginVerifySecret: "s3cret"})

	for path, want := range map[string]int{
		"/healthz":           http.StatusOK,
		"/readyz":            http.StatusOK,
		"/api/v1/auth/login": http.StatusForbidden,
	} {
		method := http.MethodGet
		if path == "/api/v1/auth/login" {
			method = http.MethodPost
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		if rec.Code != want {
			t.Errorf("%s: status = %d, want %d", path, rec.Code, want)
		}
	}
}

func TestRouterUnknownPathIsProblem(t *testing.T) {
	rec := httptest.NewRecorder()
	NewRouter(Options{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/nope", nil))
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != problem.ContentType {
		t.Errorf("got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}
