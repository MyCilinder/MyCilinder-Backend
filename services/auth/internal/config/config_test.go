package config

import (
	"log/slog"
	"strings"
	"testing"

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
