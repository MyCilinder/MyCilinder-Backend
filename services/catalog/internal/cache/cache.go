// Package cache implements Catalog's cache-aside layer on Redis (Data Design §3.2).
//
//	feed:{sort}:{cursor}:{limit}        page of the feed       30 s, expiry only
//	search:{hash(q)}:{cursor}:{limit}   page of search results 30 s, expiry only
//	video:{id}                          READY video detail     60 s, deleted on every write
//
// Concurrent misses for the same key are collapsed into one load per task
// (singleflight). Redis is never required: any Redis error is logged at warn level and the
// value is loaded from Postgres.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

// ErrMiss is returned by Store.Get when the key does not exist.
var ErrMiss = errors.New("cache: miss")

// Store is the subset of Redis the cache uses. RedisStore implements it.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Del(ctx context.Context, keys ...string) error
}

// RedisStore adapts a go-redis client.
type RedisStore struct{ c redis.Cmdable }

// NewRedisStore returns a Store on c.
func NewRedisStore(c redis.Cmdable) RedisStore { return RedisStore{c: c} }

func (s RedisStore) Get(ctx context.Context, key string) ([]byte, error) {
	b, err := s.c.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrMiss
	}
	return b, err
}

func (s RedisStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return s.c.Set(ctx, key, value, ttl).Err()
}

func (s RedisStore) Del(ctx context.Context, keys ...string) error {
	return s.c.Del(ctx, keys...).Err()
}

// loadTimeout bounds a shared load. The load runs detached from the first caller's
// context, so one caller giving up does not fail everyone waiting on the same key.
const loadTimeout = 5 * time.Second

// Cache is the cache-aside helper.
type Cache struct {
	store Store
	group singleflight.Group
	log   *slog.Logger
}

// New returns a Cache. A nil logger uses slog.Default().
func New(store Store, log *slog.Logger) *Cache {
	if log == nil {
		log = slog.Default()
	}
	return &Cache{store: store, log: log}
}

// Load returns the value under key, or calls load on a miss and stores the result for
// ttl. Only one load per key runs at a time in this process; concurrent callers share its
// result. When store is false for a loaded value it is returned but not cached.
func Load[T any](ctx context.Context, c *Cache, key string, ttl time.Duration,
	load func(context.Context) (value T, store bool, err error)) (T, error) {

	var zero T
	b, err := c.store.Get(ctx, key)
	switch {
	case err == nil:
		var v T
		if jerr := json.Unmarshal(b, &v); jerr == nil {
			return v, nil
		}
		c.log.Warn("cache entry unreadable; reloading", "key", key)
	case !errors.Is(err, ErrMiss):
		c.log.Warn("cache get failed; reading from Postgres", "key", key, "err", err)
	}

	res, err, _ := c.group.Do(key, func() (any, error) {
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loadTimeout)
		defer cancel()
		v, keep, err := load(lctx)
		if err != nil {
			return nil, err
		}
		if keep {
			if b, err := json.Marshal(v); err == nil {
				if err := c.store.Set(lctx, key, b, ttl); err != nil {
					c.log.Warn("cache set failed", "key", key, "err", err)
				}
			}
		}
		return v, nil
	})
	if err != nil {
		return zero, err
	}
	return res.(T), nil
}

// Delete removes keys. Errors are logged, not returned: a stale entry expires with its
// TTL anyway.
func (c *Cache) Delete(ctx context.Context, keys ...string) {
	if err := c.store.Del(ctx, keys...); err != nil {
		c.log.Warn("cache delete failed; entry expires with its TTL", "keys", keys, "err", err)
	}
}
