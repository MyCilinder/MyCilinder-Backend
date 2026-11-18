package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware/ratelimit"
)

// RedisThrottle counts failed logins per email and per client IP in fixed windows
// (SEC-09). A successful login clears the email counter; the IP counter keeps running so
// one address cannot cycle through many accounts.
//
// Emails are stored as a SHA-256 hash, so Redis holds no addresses (SEC-12).
// It fails open: Redis errors are logged and the attempt is allowed.
type RedisThrottle struct {
	email *ratelimit.Limiter
	ip    *ratelimit.Limiter
}

// ThrottleLimits configures RedisThrottle.
type ThrottleLimits struct {
	MaxFailuresPerEmail int
	MaxFailuresPerIP    int
	Window              time.Duration
}

// NewRedisThrottle returns a LoginThrottle backed by Redis.
func NewRedisThrottle(rdb redis.Cmdable, l ThrottleLimits) *RedisThrottle {
	return &RedisThrottle{
		email: ratelimit.New(rdb, "login:email", l.MaxFailuresPerEmail, l.Window),
		ip:    ratelimit.New(rdb, "login:ip", l.MaxFailuresPerIP, l.Window),
	}
}

// Check returns a *RateLimitedError when either counter is at its limit.
func (t *RedisThrottle) Check(ctx context.Context, email, clientIP string) error {
	var wait time.Duration
	for _, c := range t.counters(email, clientIP) {
		d, err := c.limiter.Peek(ctx, c.key)
		if err != nil {
			middleware.Logger(ctx).Warn("login throttle unavailable, allowing attempt", "err", err)
			return nil
		}
		if !d.Allowed && d.RetryAfter > wait {
			wait = d.RetryAfter
		}
	}
	if wait > 0 {
		return NewRateLimitedError(wait)
	}
	return nil
}

// RecordFailure counts one failed attempt.
func (t *RedisThrottle) RecordFailure(ctx context.Context, email, clientIP string) {
	for _, c := range t.counters(email, clientIP) {
		if _, err := c.limiter.Hit(ctx, c.key); err != nil {
			middleware.Logger(ctx).Warn("login throttle unavailable, failure not counted", "err", err)
			return
		}
	}
}

// Reset clears the email counter after a successful login.
func (t *RedisThrottle) Reset(ctx context.Context, email string) {
	if err := t.email.Reset(ctx, emailKey(email)); err != nil {
		middleware.Logger(ctx).Warn("login throttle reset failed", "err", err)
	}
}

type counter struct {
	limiter *ratelimit.Limiter
	key     string
}

func (t *RedisThrottle) counters(email, clientIP string) []counter {
	out := []counter{{t.email, emailKey(email)}}
	if clientIP != "" {
		out = append(out, counter{t.ip, clientIP})
	}
	return out
}

func emailKey(email string) string {
	sum := sha256.Sum256([]byte(email))
	return hex.EncodeToString(sum[:16])
}
