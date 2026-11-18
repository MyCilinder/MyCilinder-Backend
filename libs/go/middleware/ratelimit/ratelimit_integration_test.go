//go:build integration

package ratelimit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// Run with the local stack up:
//
//	TEST_REDIS_ADDR=127.0.0.1:6379 go test -tags integration ./libs/go/middleware/ratelimit/...
func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR not set")
	}
	c := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func uniquePrefix() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "test-" + hex.EncodeToString(b)
}

func TestHitWindow(t *testing.T) {
	ctx := context.Background()
	l := New(testRedis(t), uniquePrefix(), 3, 2*time.Second)

	for i := 1; i <= 3; i++ {
		d, err := l.Hit(ctx, "k")
		if err != nil || !d.Allowed || d.Count != int64(i) {
			t.Fatalf("hit %d: %+v %v", i, d, err)
		}
	}
	d, err := l.Hit(ctx, "k")
	if err != nil || d.Allowed {
		t.Fatalf("4th hit should be blocked: %+v %v", d, err)
	}
	if d.RetryAfter <= 0 || d.RetryAfter > 2*time.Second {
		t.Errorf("RetryAfter = %v", d.RetryAfter)
	}

	// A different key has its own window.
	if d, _ := l.Hit(ctx, "other"); !d.Allowed {
		t.Error("other key should be allowed")
	}

	// The window expires.
	time.Sleep(2100 * time.Millisecond)
	if d, _ := l.Hit(ctx, "k"); !d.Allowed || d.Count != 1 {
		t.Errorf("after window: %+v", d)
	}
}

func TestPeekAndReset(t *testing.T) {
	ctx := context.Background()
	l := New(testRedis(t), uniquePrefix(), 2, time.Minute)

	if d, err := l.Peek(ctx, "k"); err != nil || !d.Allowed || d.Count != 0 {
		t.Fatalf("empty peek: %+v %v", d, err)
	}
	l.Hit(ctx, "k")
	l.Hit(ctx, "k")
	d, err := l.Peek(ctx, "k")
	if err != nil || d.Allowed || d.RetryAfter <= 0 {
		t.Fatalf("peek at limit should block: %+v %v", d, err)
	}
	if err := l.Reset(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if d, _ := l.Peek(ctx, "k"); !d.Allowed {
		t.Error("reset did not clear the counter")
	}
}

func TestPerIPMiddleware(t *testing.T) {
	l := New(testRedis(t), uniquePrefix(), 2, time.Minute)
	h := PerIP(l, ClientIP(0))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	codes := []int{}
	for range 3 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = "198.51.100.7:1234"
		h.ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
		if rec.Code == http.StatusTooManyRequests && rec.Header().Get("Retry-After") == "" {
			t.Error("429 without Retry-After")
		}
	}
	if codes[0] != 204 || codes[1] != 204 || codes[2] != 429 {
		t.Errorf("codes = %v", codes)
	}
}
