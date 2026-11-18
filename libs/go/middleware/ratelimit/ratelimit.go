// Package ratelimit implements fixed-window counters in Redis and a per-client-IP HTTP
// middleware (SEC-09, Architecture §5 F3).
//
// Every caller fails open: if Redis is unreachable, requests are allowed and a warning is
// logged. Redis is an optimization for abuse resistance, not a dependency the request
// path may block on (Architecture §3.3, "Redis unavailable").
//
// Counters use INCR plus EXPIRE ... NX, which needs Redis 7 or Valkey (ElastiCache's
// current engines).
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/problem"
)

// Decision is the outcome of a check.
type Decision struct {
	Allowed    bool
	Count      int64
	RetryAfter time.Duration // set when Allowed is false
}

// Limiter allows at most limit events per key in each fixed window.
type Limiter struct {
	rdb    redis.Cmdable
	prefix string
	limit  int64
	window time.Duration
}

// New returns a Limiter. Keys are stored as "rl:<prefix>:<key>".
func New(rdb redis.Cmdable, prefix string, limit int, window time.Duration) *Limiter {
	return &Limiter{rdb: rdb, prefix: "rl:" + prefix + ":", limit: int64(limit), window: window}
}

// Hit records one event and reports whether it is within the limit. The first event of a
// window starts the window's expiry.
func (l *Limiter) Hit(ctx context.Context, key string) (Decision, error) {
	k := l.prefix + key
	pipe := l.rdb.TxPipeline()
	incr := pipe.Incr(ctx, k)
	pipe.ExpireNX(ctx, k, l.window)
	ttl := pipe.PTTL(ctx, k)
	if _, err := pipe.Exec(ctx); err != nil {
		return Decision{Allowed: true}, fmt.Errorf("ratelimit hit: %w", err)
	}
	d := Decision{Count: incr.Val(), Allowed: incr.Val() <= l.limit}
	if !d.Allowed {
		d.RetryAfter = l.retryAfter(ttl.Val())
	}
	return d, nil
}

// Peek reports whether another event would be allowed, without recording one. Used for
// failure counters: check before the attempt, Hit only when the attempt fails.
func (l *Limiter) Peek(ctx context.Context, key string) (Decision, error) {
	k := l.prefix + key
	pipe := l.rdb.Pipeline()
	get := pipe.Get(ctx, k)
	ttl := pipe.PTTL(ctx, k)
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return Decision{Allowed: true}, fmt.Errorf("ratelimit peek: %w", err)
	}
	n, _ := get.Int64() // redis.Nil (no failures yet) reads as 0
	d := Decision{Count: n, Allowed: n < l.limit}
	if !d.Allowed {
		d.RetryAfter = l.retryAfter(ttl.Val())
	}
	return d, nil
}

// Reset clears the counter for key.
func (l *Limiter) Reset(ctx context.Context, key string) error {
	if err := l.rdb.Del(ctx, l.prefix+key).Err(); err != nil {
		return fmt.Errorf("ratelimit reset: %w", err)
	}
	return nil
}

// retryAfter turns a PTTL result into a wait time. PTTL is -1 (no expiry) or -2 (gone)
// in edge cases; the full window is a safe answer then.
func (l *Limiter) retryAfter(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return l.window
	}
	return ttl
}

// WriteRateLimited sends 429 with Retry-After in whole seconds (at least 1).
func WriteRateLimited(w http.ResponseWriter, r *http.Request, retryAfter time.Duration) {
	secs := int(math.Ceil(retryAfter.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	problem.Write(w, r, http.StatusTooManyRequests, problem.RateLimited, "too many requests, retry later")
}

// PerIP counts every request per client IP and rejects requests over the limit.
func PerIP(l *Limiter, clientIP func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r)
			if ip == "" {
				next.ServeHTTP(w, r)
				return
			}
			d, err := l.Hit(r.Context(), ip)
			if err != nil {
				middleware.Logger(r.Context()).Warn("rate limiter unavailable, allowing request", "err", err)
			} else if !d.Allowed {
				WriteRateLimited(w, r, d.RetryAfter)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP returns a function that finds the client address.
//
// trustedHops is the number of proxies in front of the service that append to
// X-Forwarded-For. The client address is the entry trustedHops positions from the right;
// entries further left were supplied by the client and cannot be trusted. With 0 (local
// development) the TCP peer address is used. The right value for the AWS chain
// (CloudFront, API Gateway, ALB) must be confirmed against real traffic.
func ClientIP(trustedHops int) func(*http.Request) string {
	return func(r *http.Request) string {
		if trustedHops > 0 {
			var parts []string
			for _, h := range r.Header.Values("X-Forwarded-For") {
				for _, p := range strings.Split(h, ",") {
					parts = append(parts, strings.TrimSpace(p))
				}
			}
			if len(parts) >= trustedHops {
				if ip := parts[len(parts)-trustedHops]; net.ParseIP(ip) != nil {
					return ip
				}
			}
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			return r.RemoteAddr
		}
		return host
	}
}
