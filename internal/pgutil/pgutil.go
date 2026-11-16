// Package pgutil has small helpers shared by services talking to Postgres or PGAdapter.
package pgutil

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Env returns $name or def.
func Env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// DefaultPG is the local source DSN (overridden by $PG_DSN).
func DefaultPG() string {
	return Env("PG_DSN", fmt.Sprintf("postgres://%s:%s@localhost:15432/%s?sslmode=disable",
		Env("POSTGRES_USER", "shadowcut"), Env("POSTGRES_PASSWORD", ""), Env("POSTGRES_DB", "wiki")))
}

// DefaultSpanner is the local PGAdapter DSN (overridden by $SPANNER_DSN).
func DefaultSpanner() string {
	return Env("SPANNER_DSN", "postgres://localhost:15433/"+Env("SPANNER_DATABASE", "wiki")+"?sslmode=disable")
}

// IsSpannerDSN is a heuristic used to pick PGAdapter-friendly settings.
func IsSpannerDSN(dsn string) bool {
	return dsn == DefaultSpanner() || os.Getenv("SPANNER_DSN") == dsn
}

// Connect opens a pool, retrying until ctx expires or the database answers.
// PGAdapter connections use the extended protocol with statement description
// (pgx default) which PGAdapter supports.
func Connect(ctx context.Context, dsn string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	var pool *pgxpool.Pool
	delay := 250 * time.Millisecond
	for attempt := 1; ; attempt++ {
		pool, err = pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err = pool.Ping(pctx)
			cancel()
			if err == nil {
				return pool, nil
			}
			pool.Close()
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("connect %s: %w (last error: %v)", redact(dsn), ctx.Err(), err)
		}
		if attempt%10 == 1 {
			slog.Info("waiting for database", "dsn", redact(dsn), "err", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(delay):
		}
		if delay < 5*time.Second {
			delay *= 2
		}
	}
}

// ConnectOne opens a single connection.
func ConnectOne(ctx context.Context, dsn string) (*pgx.Conn, error) {
	return pgx.Connect(ctx, dsn)
}

func redact(dsn string) string {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return "<dsn>"
	}
	return fmt.Sprintf("%s:%d/%s", cfg.Host, cfg.Port, cfg.Database)
}

// Redact returns host:port/db for logs (never the password).
func Redact(dsn string) string { return redact(dsn) }
