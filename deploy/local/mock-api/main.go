// Command mock-api is an in-memory stand-in for the Catalog, Interactions and Upload
// services, for frontend demos before those services exist. Development only; never
// deployed. Tokens are verified against the real Cognito user pool, so sign-in goes
// through the real auth facade. See README.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware/jwtauth"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		slog.Error("mock-api stopped", "err", err)
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadConfig() (config, error) {
	delay, err := time.ParseDuration(env("PROCESSING_DELAY", "15s"))
	if err != nil {
		return config{}, fmt.Errorf("PROCESSING_DELAY: %w", err)
	}
	cfg := config{
		Port:            env("PORT", "8000"),
		CognitoRegion:   env("COGNITO_REGION", "us-east-1"),
		CognitoPoolID:   os.Getenv("COGNITO_USER_POOL_ID"),
		CognitoClientID: os.Getenv("COGNITO_CLIENT_ID"),
		OperatorsGroup:  env("OPERATORS_GROUP", "operators"),
		MediaDir:        env("MEDIA_DIR", "/media"),
		MediaBaseURL:    os.Getenv("MEDIA_BASE_URL"),
		AuthURL:         env("AUTH_URL", "http://auth:8000"),
		ProcessingDelay: delay,
		SeedPerUser:     env("SEED_PER_USER", "true") == "true",
	}
	if cfg.CognitoPoolID == "" || cfg.CognitoClientID == "" {
		return config{}, errors.New("COGNITO_USER_POOL_ID and COGNITO_CLIENT_ID are required (same values as services/auth/.env)")
	}
	return cfg, nil
}

func run() error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "mock-api"))
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	verifier, err := jwtauth.NewCognitoVerifier(ctx, jwtauth.CognitoConfig{
		Region: cfg.CognitoRegion, UserPoolID: cfg.CognitoPoolID, ClientID: cfg.CognitoClientID,
	})
	if err != nil {
		return err
	}
	defer func() { _ = verifier.Close(context.Background()) }()

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           newServer(cfg, NewStore(), LoadMedia(cfg.MediaDir), verifier).routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", srv.Addr, "processingDelay", cfg.ProcessingDelay.String(), "seedPerUser", cfg.SeedPerUser)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func healthcheck() int {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + env("PORT", "8000") + "/healthz")
	if err != nil {
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
