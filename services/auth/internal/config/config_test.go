package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	libconfig "github.com/MyCilinder/MyCilinder-Backend/libs/go/config"
)

func validEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":          "postgres://u:p@localhost:5432/db?sslmode=disable",
		"COGNITO_USER_POOL_ID":  "us-east-1_example",
		"COGNITO_CLIENT_ID":     "client",
		"COGNITO_CLIENT_SECRET": "secret",
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(libconfig.FromMap(validEnv()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != 8000 || cfg.LogLevel != slog.LevelInfo || cfg.RedisAddr != "127.0.0.1:6379" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.LoginMaxFailuresPerEmail != 5 || cfg.LoginFailureWindow != 15*time.Minute || cfg.TrustedProxyHops != 0 {
		t.Fatalf("unexpected throttle defaults: %+v", cfg)
	}
}

func TestLoadInvalidLimits(t *testing.T) {
	env := validEnv()
	env["LOGIN_MAX_FAILURES_PER_EMAIL"] = "0"
	env["PUBLIC_AUTH_WINDOW"] = "10ms"
	env["TRUSTED_PROXY_HOPS"] = "-1"
	_, err := Load(libconfig.FromMap(env))
	for _, key := range []string{"LOGIN_MAX_FAILURES_PER_EMAIL", "PUBLIC_AUTH_WINDOW", "TRUSTED_PROXY_HOPS"} {
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("error does not mention %s: %v", key, err)
		}
	}
}

func TestLoadInvalid(t *testing.T) {
	_, err := Load(libconfig.FromMap(map[string]string{
		"PORT":                  "70000",
		"LOG_LEVEL":             "loud",
		"ORIGIN_VERIFY_ENABLED": "true",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, key := range []string{"PORT", "LOG_LEVEL", "ORIGIN_VERIFY_SECRET", "DATABASE_URL", "COGNITO_USER_POOL_ID", "COGNITO_CLIENT_ID", "COGNITO_CLIENT_SECRET"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not mention %s: %v", key, err)
		}
	}
}
