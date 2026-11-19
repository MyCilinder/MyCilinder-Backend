// Package config holds the Catalog service settings, read from environment variables.
//
// The same image runs locally and on ECS; only the environment differs:
//   - Locally, DATABASE_URL points at the Compose Postgres, Redis has no TLS, and AWS
//     credentials come from a mounted ~/.aws profile (AWS_PROFILE).
//   - On ECS, the database URLs point at the RDS Proxy read/write and read-only
//     endpoints (sslmode=require), Redis uses TLS, and AWS credentials come from the task
//     role through the default credential chain (no AWS_PROFILE).
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

	// DatabaseURL is the writer (RDS Proxy read/write endpoint in AWS). DatabaseReaderURL
	// is the RDS Proxy read-only endpoint; it defaults to DatabaseURL, which is also the
	// fallback when the reader is unavailable (Data Design §2.2).
	DatabaseURL       string
	DatabaseReaderURL string

	RedisAddr     string
	RedisPassword string
	RedisTLS      bool

	// Cognito user pool and app client whose access tokens the service accepts (SEC-01).
	// Catalog only verifies tokens, so it needs no client secret.
	CognitoRegion     string
	CognitoUserPoolID string
	CognitoClientID   string
	// OperatorsGroup is the cognito:groups value required on /ops routes.
	OperatorsGroup string

	// AWSRegion is used for SQS and S3. Credentials come from the default chain.
	AWSRegion string

	// ConsumerEnabled starts the video-status-queue consumer.
	ConsumerEnabled     bool
	VideoStatusQueueURL string

	// JobsEnabled starts the background jobs (expiry, stuck-processing sweep, output
	// cleanup). Every task may run them; an advisory lock keeps one run at a time.
	JobsEnabled     bool
	HLSOutputBucket string

	// Signed playback cookies (Interface Design §3.3). Locally the private key is a PEM
	// file mounted into the container; on ECS it comes from Secrets Manager.
	CloudFrontDomain         string
	CloudFrontKeyPairID      string
	CloudFrontPrivateKeyFile string
}

// Load reads the configuration and returns every problem at once.
func Load(env *libconfig.Loader) (Config, error) {
	cfg := Config{
		Port:                env.Int("PORT", 8001),
		ShutdownTimeout:     env.Duration("SHUTDOWN_TIMEOUT", 20*time.Second),
		OriginVerifyEnabled: env.Bool("ORIGIN_VERIFY_ENABLED", false),
		DatabaseURL:         env.Required("DATABASE_URL"),
		RedisAddr:           env.String("REDIS_ADDR", "127.0.0.1:6379"),
		RedisPassword:       env.String("REDIS_PASSWORD", ""),
		RedisTLS:            env.Bool("REDIS_TLS", false),
		CognitoRegion:       env.String("COGNITO_REGION", "us-east-1"),
		CognitoUserPoolID:   env.Required("COGNITO_USER_POOL_ID"),
		CognitoClientID:     env.Required("COGNITO_CLIENT_ID"),
		OperatorsGroup:      env.String("OPERATORS_GROUP", "operators"),
		AWSRegion:           env.String("AWS_REGION", "us-east-1"),
		ConsumerEnabled:     env.Bool("CONSUMER_ENABLED", false),
		JobsEnabled:         env.Bool("JOBS_ENABLED", false),

		CloudFrontDomain:         env.String("CLOUDFRONT_DOMAIN", ""),
		CloudFrontKeyPairID:      env.String("CLOUDFRONT_KEY_PAIR_ID", ""),
		CloudFrontPrivateKeyFile: env.String("CLOUDFRONT_PRIVATE_KEY_FILE", ""),
	}
	cfg.DatabaseReaderURL = env.String("DATABASE_READER_URL", cfg.DatabaseURL)
	if cfg.OriginVerifyEnabled {
		cfg.OriginVerifySecret = env.Required("ORIGIN_VERIFY_SECRET")
	}
	if cfg.ConsumerEnabled {
		cfg.VideoStatusQueueURL = env.Required("VIDEO_STATUS_QUEUE_URL")
	}
	if cfg.JobsEnabled {
		cfg.HLSOutputBucket = env.Required("HLS_OUTPUT_BUCKET")
	}

	var errs []error
	level := env.String("LOG_LEVEL", "info")
	if err := cfg.LogLevel.UnmarshalText([]byte(level)); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: must be debug, info, warn, or error, got %q", level))
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		errs = append(errs, fmt.Errorf("PORT: must be between 1 and 65535, got %d", cfg.Port))
	}
	// CloudFront signing is all or nothing: a partial setup would fail on the first
	// playback request instead of at startup.
	cf := []string{cfg.CloudFrontDomain, cfg.CloudFrontKeyPairID, cfg.CloudFrontPrivateKeyFile}
	if set := countSet(cf); set != 0 && set != len(cf) {
		errs = append(errs, errors.New("CLOUDFRONT_DOMAIN, CLOUDFRONT_KEY_PAIR_ID and CLOUDFRONT_PRIVATE_KEY_FILE: set all three or none"))
	}
	return cfg, errors.Join(append([]error{env.Err()}, errs...)...)
}

// PlaybackConfigured reports whether CloudFront signing settings are present.
func (c Config) PlaybackConfigured() bool { return c.CloudFrontDomain != "" }

func countSet(values []string) int {
	n := 0
	for _, v := range values {
		if v != "" {
			n++
		}
	}
	return n
}
