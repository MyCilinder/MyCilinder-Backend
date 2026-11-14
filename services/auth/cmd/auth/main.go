// Command auth runs the MyCilinder auth facade (Architecture §4.2).
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
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/redisclient"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/config"
	httpapi "github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/http"
)

func main() {
	if err := run(); err != nil {
		slog.Error("auth facade stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(libconfig.FromEnv())
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger.With("service", "auth"))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx, db.Options{URL: cfg.DatabaseURL})
	if err != nil {
		return err
	}
	defer pool.Close()

	rdb := redisclient.New(redisclient.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword, TLS: cfg.RedisTLS})
	defer rdb.Close()
	// Redis is optional (login throttling fails open), so an outage is logged, not fatal,
	// and it is not part of /readyz.
	pingCtx, cancelPing := context.WithTimeout(ctx, 2*time.Second)
	if err := redisclient.Ping(rdb)(pingCtx); err != nil {
		slog.Warn("redis unreachable at startup; login throttling will fail open", "err", err)
	}
	cancelPing()

	r := httpapi.NewRouter(httpapi.Options{
		OriginVerifyEnabled: cfg.OriginVerifyEnabled,
		OriginVerifySecret:  cfg.OriginVerifySecret,
		ReadinessChecks: []health.Check{
			{Name: "database", Fn: db.Ping(pool)},
		},
	})

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           r,
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
	return srv.Shutdown(shutdownCtx)
}
