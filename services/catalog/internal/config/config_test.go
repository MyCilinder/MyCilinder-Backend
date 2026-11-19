package config

import (
	"strings"
	"testing"
	"time"

	libconfig "github.com/MyCilinder/MyCilinder-Backend/libs/go/config"
)

func base() map[string]string {
	return map[string]string{
		"DATABASE_URL":         "postgres://catalog_svc@localhost/mycilinder",
		"COGNITO_USER_POOL_ID": "us-east-1_test",
		"COGNITO_CLIENT_ID":    "client",
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(libconfig.FromMap(base()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 8001 {
		t.Errorf("Port = %d, want 8001", cfg.Port)
	}
	if cfg.DatabaseReaderURL != cfg.DatabaseURL {
		t.Errorf("DatabaseReaderURL = %q, want the writer URL", cfg.DatabaseReaderURL)
	}
	if cfg.OperatorsGroup != "operators" {
		t.Errorf("OperatorsGroup = %q", cfg.OperatorsGroup)
	}
	if cfg.JobInterval != 15*time.Minute || cfg.PendingUploadExpiry != 24*time.Hour ||
		cfg.ProcessingTimeout != 2*time.Hour || cfg.CleanupInterval != 24*time.Hour ||
		cfg.OutputRetention != 7*24*time.Hour {
		t.Errorf("job defaults = %s %s %s %s %s", cfg.JobInterval, cfg.PendingUploadExpiry,
			cfg.ProcessingTimeout, cfg.CleanupInterval, cfg.OutputRetention)
	}
	if cfg.ConsumerEnabled || cfg.JobsEnabled || cfg.PlaybackConfigured() {
		t.Errorf("background workers and playback should be off by default: %+v", cfg)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"missing required", map[string]string{}, []string{"DATABASE_URL", "COGNITO_USER_POOL_ID", "COGNITO_CLIENT_ID"}},
		{"bad port", with(base(), "PORT", "70000"), []string{"PORT"}},
		{"bad log level", with(base(), "LOG_LEVEL", "loud"), []string{"LOG_LEVEL"}},
		{"origin verify needs secret", with(base(), "ORIGIN_VERIFY_ENABLED", "true"), []string{"ORIGIN_VERIFY_SECRET"}},
		{"consumer needs queue", with(base(), "CONSUMER_ENABLED", "true"), []string{"VIDEO_STATUS_QUEUE_URL"}},
		{"jobs need bucket", with(base(), "JOBS_ENABLED", "true"), []string{"HLS_OUTPUT_BUCKET"}},
		{"bad duration", with(base(), "JOB_INTERVAL", "often"), []string{"JOB_INTERVAL"}},
		{"duration too short", with(base(), "OUTPUT_RETENTION", "0s"), []string{"OUTPUT_RETENTION"}},
		{"partial cloudfront", with(base(), "CLOUDFRONT_DOMAIN", "d123.cloudfront.net"), []string{"set all three or none"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(libconfig.FromMap(tt.env))
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not mention %q", err, w)
				}
			}
		})
	}
}

func TestLoadReaderURL(t *testing.T) {
	cfg, err := Load(libconfig.FromMap(with(base(), "DATABASE_READER_URL", "postgres://reader")))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseReaderURL != "postgres://reader" {
		t.Errorf("DatabaseReaderURL = %q", cfg.DatabaseReaderURL)
	}
}

func TestLoadDemoJobSettings(t *testing.T) {
	env := base()
	env["JOB_INTERVAL"] = "10s"
	env["OUTPUT_RETENTION"] = "1m"
	cfg, err := Load(libconfig.FromMap(env))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JobInterval != 10*time.Second || cfg.OutputRetention != time.Minute {
		t.Errorf("JobInterval = %s, OutputRetention = %s", cfg.JobInterval, cfg.OutputRetention)
	}
}

func with(m map[string]string, k, v string) map[string]string {
	m[k] = v
	return m
}
