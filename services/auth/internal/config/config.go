// Package config holds the auth facade settings, read from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	libconfig "github.com/MyCilinder/MyCilinder-Backend/libs/go/config"
)

// Config is the full service configuration.
type Config struct {
	Port            int
	LogLevel        slog.Level
	ShutdownTimeout time.Duration

	// OriginVerifyEnabled turns on the X-Origin-Verify check. Off for local development.
	OriginVerifyEnabled bool
	OriginVerifySecret  string

	// DatabaseURL points at RDS Proxy in AWS and at the Compose Postgres locally.
	DatabaseURL string

	RedisAddr     string
	RedisPassword string
	RedisTLS      bool

	// Cognito user pool and confidential app client (Interface Design §3.1). The client
	// secret comes from Secrets Manager in AWS and from .env locally.
	CognitoRegion       string
	CognitoUserPoolID   string
	CognitoClientID     string
	CognitoClientSecret string

	// TrustedProxyHops is the number of proxies in front of the service that append to
	// X-Forwarded-For (0 locally). See ratelimit.ClientIP.
	TrustedProxyHops int

	// Failed-login throttling (SEC-09).
	LoginMaxFailuresPerEmail int
	LoginMaxFailuresPerIP    int
	LoginFailureWindow       time.Duration

	// Request limit per client IP on the public /auth/* routes.
	PublicAuthMaxPerIP int
	PublicAuthWindow   time.Duration
}

// Load reads the configuration and returns every problem at once.
func Load(env *libconfig.Loader) (Config, error) {
	cfg := Config{
		Port:                env.Int("PORT", 8000),
		ShutdownTimeout:     env.Duration("SHUTDOWN_TIMEOUT", 20*time.Second),
		OriginVerifyEnabled: env.Bool("ORIGIN_VERIFY_ENABLED", false),
		DatabaseURL:         env.Required("DATABASE_URL"),
		RedisAddr:           env.String("REDIS_ADDR", "127.0.0.1:6379"),
		RedisPassword:       env.String("REDIS_PASSWORD", ""),
		RedisTLS:            env.Bool("REDIS_TLS", false),
		CognitoRegion:       env.String("COGNITO_REGION", "us-east-1"),
		CognitoUserPoolID:   env.Required("COGNITO_USER_POOL_ID"),
		CognitoClientID:     env.Required("COGNITO_CLIENT_ID"),
		CognitoClientSecret: env.Required("COGNITO_CLIENT_SECRET"),

		TrustedProxyHops:         env.Int("TRUSTED_PROXY_HOPS", 0),
		LoginMaxFailuresPerEmail: env.Int("LOGIN_MAX_FAILURES_PER_EMAIL", 5),
		LoginMaxFailuresPerIP:    env.Int("LOGIN_MAX_FAILURES_PER_IP", 20),
		LoginFailureWindow:       env.Duration("LOGIN_FAILURE_WINDOW", 15*time.Minute),
		PublicAuthMaxPerIP:       env.Int("PUBLIC_AUTH_MAX_REQUESTS_PER_IP", 30),
		PublicAuthWindow:         env.Duration("PUBLIC_AUTH_WINDOW", time.Minute),
	}
	if cfg.OriginVerifyEnabled {
		cfg.OriginVerifySecret = env.Required("ORIGIN_VERIFY_SECRET")
	}

	var errs []error
	level := env.String("LOG_LEVEL", "info")
	if err := cfg.LogLevel.UnmarshalText([]byte(level)); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: must be debug, info, warn, or error, got %q", level))
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		errs = append(errs, fmt.Errorf("PORT: must be between 1 and 65535, got %d", cfg.Port))
	}
	if cfg.TrustedProxyHops < 0 {
		errs = append(errs, fmt.Errorf("TRUSTED_PROXY_HOPS: must be 0 or more, got %d", cfg.TrustedProxyHops))
	}
	for name, n := range map[string]int{
		"LOGIN_MAX_FAILURES_PER_EMAIL":    cfg.LoginMaxFailuresPerEmail,
		"LOGIN_MAX_FAILURES_PER_IP":       cfg.LoginMaxFailuresPerIP,
		"PUBLIC_AUTH_MAX_REQUESTS_PER_IP": cfg.PublicAuthMaxPerIP,
	} {
		if n < 1 {
			errs = append(errs, fmt.Errorf("%s: must be at least 1, got %d", name, n))
		}
	}
	for name, d := range map[string]time.Duration{
		"LOGIN_FAILURE_WINDOW": cfg.LoginFailureWindow,
		"PUBLIC_AUTH_WINDOW":   cfg.PublicAuthWindow,
	} {
		if d < time.Second {
			errs = append(errs, fmt.Errorf("%s: must be at least 1s, got %s", name, d))
		}
	}
	return cfg, errors.Join(append([]error{env.Err()}, errs...)...)
}
