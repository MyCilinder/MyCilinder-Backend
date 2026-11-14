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
	return cfg, errors.Join(append([]error{env.Err()}, errs...)...)
}
