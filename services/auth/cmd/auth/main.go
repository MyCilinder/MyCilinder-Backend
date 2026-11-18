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
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware/jwtauth"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware/ratelimit"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/redisclient"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/auth"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/cognito"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/config"
	httpapi "github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/http"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/profile"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/store"
)

func main() {
	// `auth healthcheck` probes /healthz on the local port and exits 0 or 1. The runtime
	// image has no shell or curl, so Docker and ECS container health checks run this.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		slog.Error("auth facade stopped", "err", err)
		os.Exit(1)
	}
}

func healthcheck() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
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
	slog.SetDefault(logger.With("service", "auth"))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// PostgreSQL through RDS Proxy (or the Compose Postgres locally).
	pool, err := db.NewPool(ctx, db.Options{URL: cfg.DatabaseURL})
	if err != nil {
		return err
	}
	defer pool.Close()

	// Redis is optional (throttling fails open), so an outage is logged, not fatal, and
	// it is not part of /readyz.
	rdb := redisclient.New(redisclient.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword, TLS: cfg.RedisTLS})
	defer rdb.Close()
	pingCtx, cancelPing := context.WithTimeout(ctx, 2*time.Second)
	if err := redisclient.Ping(rdb)(pingCtx); err != nil {
		slog.Warn("redis unreachable at startup; throttling will fail open", "err", err)
	}
	cancelPing()

	// Cognito: SDK client for the user pool API, JWKS cache for token validation.
	sdk, err := cognito.NewSDKClient(ctx, cfg.CognitoRegion)
	if err != nil {
		return err
	}
	idp := cognito.New(sdk, cfg.CognitoClientID, cfg.CognitoClientSecret)

	verifier, err := jwtauth.NewCognitoVerifier(ctx, jwtauth.CognitoConfig{
		Region: cfg.CognitoRegion, UserPoolID: cfg.CognitoUserPoolID, ClientID: cfg.CognitoClientID,
	})
	if err != nil {
		return err
	}
	defer func() { _ = verifier.Close(context.Background()) }()

	users := store.NewUsers(pool)
	audit := store.NewAudit(pool, "auth")
	throttle := auth.NewRedisThrottle(rdb, auth.ThrottleLimits{
		MaxFailuresPerEmail: cfg.LoginMaxFailuresPerEmail,
		MaxFailuresPerIP:    cfg.LoginMaxFailuresPerIP,
		Window:              cfg.LoginFailureWindow,
	})
	clientIP := ratelimit.ClientIP(cfg.TrustedProxyHops)

	handler := httpapi.NewRouter(httpapi.Options{
		OriginVerifyEnabled: cfg.OriginVerifyEnabled,
		OriginVerifySecret:  cfg.OriginVerifySecret,
		ReadinessChecks: []health.Check{
			{Name: "database", Fn: db.Ping(pool)},
		},
		Auth:        auth.New(idp, users, audit, throttle),
		Profile:     profile.New(idp, users),
		Verifier:    verifier,
		PublicLimit: ratelimit.PerIP(ratelimit.New(rdb, "auth:public", cfg.PublicAuthMaxPerIP, cfg.PublicAuthWindow), clientIP),
		ClientIP:    clientIP,
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
