// Package db builds the PostgreSQL connection pool used by the Go services.
//
// The settings follow the RDS Proxy rules in Architecture §3:
//   - QueryExecModeExec: queries stay parameterized but pgx never creates named prepared
//     statements, which would pin the client to one Aurora connection.
//   - No session SET and no reset query; per-role settings (search_path,
//     statement_timeout, TimeZone) are applied with ALTER ROLE.
//   - A small pool per task (at most 10 connections, Architecture §4.3).
//
// TLS is controlled by sslmode in the URL: sslmode=disable locally, sslmode=require (or
// verify-full) against RDS Proxy.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Options configures the pool.
type Options struct {
	URL      string
	MaxConns int32 // 0 means the default of 10
}

// NewPool parses the URL, applies the RDS Proxy-compatible settings, and returns a pool.
// It does not connect; call Ping to check reachability.
func NewPool(ctx context.Context, opts Options) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(opts.URL)
	if err != nil {
		// The error may include the URL; return a message without it so the password is
		// not logged.
		return nil, fmt.Errorf("db: invalid DATABASE_URL")
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	cfg.MaxConns = 10
	if opts.MaxConns > 0 {
		cfg.MaxConns = opts.MaxConns
	}
	cfg.MinConns = 0
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: create pool: %w", err)
	}
	return pool, nil
}

// Ping returns a readiness check function for the pool.
func Ping(pool *pgxpool.Pool) func(context.Context) error {
	return func(ctx context.Context) error { return pool.Ping(ctx) }
}
