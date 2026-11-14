package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/problem"
)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func ok(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }

// captureLogs swaps slog.Default for the test and returns the buffer.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func TestCorrelation(t *testing.T) {
	tests := []struct {
		name     string
		incoming string
		keep     bool
	}{
		{"generated when absent", "", false},
		{"kept when valid", "client-id_1.2", true},
		{"replaced when unsafe", "bad id\nINJECT", false},
		{"replaced when too long", strings.Repeat("a", 129), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen string
			h := Correlation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = problem.CorrelationID(r.Context())
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.incoming != "" {
				req.Header.Set(CorrelationHeader, tt.incoming)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			got := rec.Header().Get(CorrelationHeader)
			if got != seen {
				t.Errorf("header %q != context %q", got, seen)
			}
			if tt.keep && got != tt.incoming {
				t.Errorf("got %q, want incoming %q", got, tt.incoming)
			}
			if !tt.keep && !uuidRe.MatchString(got) {
				t.Errorf("expected generated UUID, got %q", got)
			}
		})
	}
}

func TestRequestLogOmitsSensitiveData(t *testing.T) {
	buf := captureLogs(t)
	h := Correlation(RequestLog("/healthz")(http.HandlerFunc(ok)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login?email=diego@example.com",
		strings.NewReader(`{"password":"Secret#123"}`))
	req.Header.Set("Authorization", "Bearer eyJsecret")
	h.ServeHTTP(httptest.NewRecorder(), req)

	line := buf.String()
	for _, secret := range []string{"Secret#123", "eyJsecret", "diego@example.com"} {
		if strings.Contains(line, secret) {
			t.Errorf("log contains %q: %s", secret, line)
		}
	}
	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("log is not JSON: %q", line)
	}
	if entry["status"] != float64(204) || entry["path"] != "/api/v1/auth/login" || entry["correlationId"] == nil {
		t.Errorf("unexpected log entry %v", entry)
	}

	buf.Reset()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if buf.Len() != 0 {
		t.Errorf("skipped path was logged: %s", buf.String())
	}
}

func TestRecover(t *testing.T) {
	buf := captureLogs(t)
	h := Correlation(Recover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	var body problem.Details
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Type != problem.Internal || body.CorrelationID != rec.Header().Get(CorrelationHeader) {
		t.Errorf("unexpected body %+v", body)
	}
	if !strings.Contains(buf.String(), "boom") {
		t.Error("panic was not logged")
	}
}

func TestOriginVerify(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		secret  string
		header  string
		want    int
	}{
		{"disabled passes", false, "s3cret", "", http.StatusNoContent},
		{"match passes", true, "s3cret", "s3cret", http.StatusNoContent},
		{"missing rejected", true, "s3cret", "", http.StatusForbidden},
		{"wrong rejected", true, "s3cret", "nope", http.StatusForbidden},
		{"empty secret rejects all", true, "", "", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := OriginVerify(tt.enabled, tt.secret)(http.HandlerFunc(ok))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.header != "" {
				req.Header.Set(OriginHeader, tt.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}
