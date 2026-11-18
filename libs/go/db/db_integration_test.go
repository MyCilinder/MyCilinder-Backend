//go:build integration

package db

import (
	"context"
	"os"
	"testing"
	"time"
)

// Run with the local stack up:
//
//	TEST_DATABASE_URL=postgres://auth_svc:auth_svc_local@127.0.0.1:5432/mycilinder?sslmode=disable \
//	  go test -tags integration ./libs/go/db/...
func TestPoolConnects(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := NewPool(ctx, Options{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if err := Ping(pool)(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if got := pool.Config().MaxConns; got != 10 {
		t.Errorf("MaxConns = %d, want 10", got)
	}

	// Parameterized query through QueryExecModeExec (no named prepared statement).
	var n int
	if err := pool.QueryRow(ctx, "SELECT $1::int + 1", 41).Scan(&n); err != nil || n != 42 {
		t.Fatalf("query: n=%d err=%v", n, err)
	}
}
