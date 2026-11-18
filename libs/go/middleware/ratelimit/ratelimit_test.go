package ratelimit

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestClientIP(t *testing.T) {
	tests := []struct {
		name string
		hops int
		xff  []string
		want string
	}{
		{"no proxies uses peer", 0, []string{"1.1.1.1"}, "192.0.2.10"},
		{"one hop takes rightmost", 1, []string{"6.6.6.6, 1.1.1.1"}, "1.1.1.1"},
		{"two hops", 2, []string{"6.6.6.6, 1.1.1.1, 10.0.0.5"}, "1.1.1.1"},
		{"multiple headers are joined", 2, []string{"1.1.1.1", "10.0.0.5"}, "1.1.1.1"},
		{"too few entries falls back to peer", 3, []string{"1.1.1.1"}, "192.0.2.10"},
		{"garbage falls back to peer", 1, []string{"not-an-ip"}, "192.0.2.10"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = "192.0.2.10:5555"
			for _, v := range tt.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := ClientIP(tt.hops)(r); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// unreachable returns a client pointed at a closed port, so every command fails fast.
func unreachable(t *testing.T) *redis.Client {
	t.Helper()
	c := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond, MaxRetries: -1})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestFailOpen(t *testing.T) {
	l := New(unreachable(t), "test", 1, time.Minute)

	if d, err := l.Hit(t.Context(), "k"); err == nil || !d.Allowed {
		t.Errorf("Hit: d=%+v err=%v", d, err)
	}
	if d, err := l.Peek(t.Context(), "k"); err == nil || !d.Allowed {
		t.Errorf("Peek: d=%+v err=%v", d, err)
	}

	logs := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h := PerIP(l, ClientIP(0))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for range 3 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want request allowed", rec.Code)
		}
	}
	if !strings.Contains(logs.String(), "rate limiter unavailable") {
		t.Error("expected a warning log")
	}
}

func TestWriteRateLimitedRoundsUp(t *testing.T) {
	for in, want := range map[time.Duration]string{
		0:                       "1",
		300 * time.Millisecond:  "1",
		1500 * time.Millisecond: "2",
		15 * time.Minute:        "900",
	} {
		rec := httptest.NewRecorder()
		WriteRateLimited(rec, httptest.NewRequest(http.MethodGet, "/", nil), in)
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != want {
			t.Errorf("%v: code %d Retry-After %q, want %q", in, rec.Code, rec.Header().Get("Retry-After"), want)
		}
	}
}
