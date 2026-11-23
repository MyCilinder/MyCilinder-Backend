//go:build integration

package cache

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/redisclient"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/browse"
)

// TEST_REDIS_ADDR=127.0.0.1:6379 go test -tags integration ./services/catalog/internal/cache/...
func TestRedisStore(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR not set")
	}
	rdb := redisclient.New(redisclient.Options{Addr: addr})
	t.Cleanup(func() { rdb.Close() })
	s := NewRedisStore(rdb)
	ctx := context.Background()
	key := "test:catalog:" + time.Now().Format(time.RFC3339Nano)
	t.Cleanup(func() { rdb.Del(ctx, key) })

	if _, err := s.Get(ctx, key); !errors.Is(err, ErrMiss) {
		t.Fatalf("missing key: %v, want ErrMiss", err)
	}
	pages := NewPages(New(s, quiet()))
	loads := 0
	load := func(context.Context) (browse.Page, error) {
		loads++
		return browse.Page{NextCursor: "c"}, nil
	}
	for i := 0; i < 2; i++ {
		if p, err := pages.Page(ctx, key, 30*time.Second, load); err != nil || p.NextCursor != "c" {
			t.Fatalf("%+v %v", p, err)
		}
	}
	if loads != 1 {
		t.Errorf("loads = %d, want 1", loads)
	}
	if ttl := rdb.TTL(ctx, key).Val(); ttl <= 0 || ttl > 30*time.Second {
		t.Errorf("ttl = %v", ttl)
	}
}

// An unreachable Redis returns errors (not ErrMiss) quickly, and reads still succeed.
func TestRedisUnreachable(t *testing.T) {
	rdb := redisclient.New(redisclient.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { rdb.Close() })
	pages := NewPages(New(NewRedisStore(rdb), quiet()))

	start := time.Now()
	p, err := pages.Page(context.Background(), "feed:recent:-:20", time.Minute, func(context.Context) (browse.Page, error) {
		return browse.Page{NextCursor: "db"}, nil
	})
	if err != nil || p.NextCursor != "db" {
		t.Fatalf("%+v %v", p, err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("fallback took %v", d)
	}
}
