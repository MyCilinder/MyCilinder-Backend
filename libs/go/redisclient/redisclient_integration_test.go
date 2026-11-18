//go:build integration

package redisclient

import (
	"context"
	"os"
	"testing"
	"time"
)

// Run with the local stack up:
//
//	TEST_REDIS_ADDR=127.0.0.1:6379 go test -tags integration ./libs/go/redisclient/...
func TestClientPings(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c := New(Options{Addr: addr})
	defer c.Close()
	if err := Ping(c)(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
}
