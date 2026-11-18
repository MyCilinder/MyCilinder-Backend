// Package redisclient builds the go-redis client used by the Go services.
package redisclient

import (
	"context"
	"crypto/tls"
	"time"

	"github.com/redis/go-redis/v9"
)

// Options configures the client.
type Options struct {
	Addr     string // host:port
	Password string
	TLS      bool // ElastiCache in-transit encryption; off locally
}

// New returns a client. It does not connect; call Ping to check reachability.
//
// Timeouts are short because every Redis use in the services has a fallback (feeds fall
// back to Aurora, login throttling fails open), so a slow Redis must not stall requests.
func New(opts Options) *redis.Client {
	o := &redis.Options{
		Addr:         opts.Addr,
		Password:     opts.Password,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  500 * time.Millisecond,
		WriteTimeout: 500 * time.Millisecond,
		PoolSize:     10,
		MaxRetries:   1,
	}
	if opts.TLS {
		o.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return redis.NewClient(o)
}

// Ping returns a check function for the client.
func Ping(c *redis.Client) func(context.Context) error {
	return func(ctx context.Context) error { return c.Ping(ctx).Err() }
}
