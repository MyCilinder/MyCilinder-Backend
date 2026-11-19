// Command catalog runs the MyCilinder Catalog service (Architecture §4.2): feeds, search,
// video detail, the creator studio, playback access, and operator moderation of videos.
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

	libconfig "github.com/MyCilinder/MyCilinder-Backend/libs/go/config"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/db"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/health"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware/jwtauth"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/redisclient"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/config"
	httpapi "github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/http"
)

func main() {
	// `catalog healthcheck` probes /healthz on the local port and exits 0 or 1. The
	// runtime image has no shell or curl, so Docker and ECS container health checks run
	// this.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		slog.Error("catalog stopped", "err", err)
		os.Exit(1)
	}
}

func healthcheck() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8001"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func run() error {
	cfg, err := config.Load(libconfig.FromEnv())
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger.With("service", "catalog"))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// PostgreSQL through RDS Proxy (or the Compose Postgres locally). Writes and
	// read-after-write checks use the writer; feeds, search, and detail use the reader.
	writer, err := db.NewPool(ctx, db.Options{URL: cfg.DatabaseURL})
	if err != nil {
		return err
	}
	defer writer.Close()

	checks := []health.Check{{Name: "database", Fn: db.Ping(writer)}}
	reader := writer
	if cfg.DatabaseReaderURL != cfg.DatabaseURL {
		reader, err = db.NewPool(ctx, db.Options{URL: cfg.DatabaseReaderURL})
		if err != nil {
			return err
		}
		defer reader.Close()
		checks = append(checks, health.Check{Name: "database_reader", Fn: db.Ping(reader)})
	}

	// Redis holds the feed and detail cache only; reads fall back to Postgres, so an
	// outage is logged, not fatal, and it is not part of /readyz.
	rdb := redisclient.New(redisclient.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword, TLS: cfg.RedisTLS})
	defer rdb.Close()
	pingCtx, cancelPing := context.WithTimeout(ctx, 2*time.Second)
	if err := redisclient.Ping(rdb)(pingCtx); err != nil {
		slog.Warn("redis unreachable at startup; reads will go to Postgres", "err", err)
	}
	cancelPing()

	// Cognito JWKS cache for token validation (SEC-01).
	verifier, err := jwtauth.NewCognitoVerifier(ctx, jwtauth.CognitoConfig{
		Region: cfg.CognitoRegion, UserPoolID: cfg.CognitoUserPoolID, ClientID: cfg.CognitoClientID,
	})
	if err != nil {
		return err
	}
	defer func() { _ = verifier.Close(context.Background()) }()

	// TODO: build the repositories on writer and reader, the cache on rdb, and the
	// services, then pass them to the router. Start the status consumer and the jobs
	// here when cfg.ConsumerEnabled and cfg.JobsEnabled are set.
	_ = reader
	slog.Info("starting",
		"consumerEnabled", cfg.ConsumerEnabled,
		"jobsEnabled", cfg.JobsEnabled,
		"playbackConfigured", cfg.PlaybackConfigured(),
		"separateReader", cfg.DatabaseReaderURL != cfg.DatabaseURL,
	)

	handler := httpapi.NewRouter(httpapi.Options{
		OriginVerifyEnabled: cfg.OriginVerifyEnabled,
		OriginVerifySecret:  cfg.OriginVerifySecret,
		ReadinessChecks:     checks,
		Verifier:            verifier,
		OperatorsGroup:      cfg.OperatorsGroup,
	})

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	slog.Info("stopped")
	return nil
}
