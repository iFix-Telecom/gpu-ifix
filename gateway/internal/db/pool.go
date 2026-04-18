// Package db owns the gateway's Postgres connection pool and migration
// runner. Every package that talks to Postgres goes through the pgxpool
// created here. Schema isolation (CONTEXT.md D-D4) is enforced via an
// AfterConnect hook that sets search_path on every acquired connection.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
)

// NewPool opens a pgxpool.Pool against cfg.PGDSN with search_path hooked
// to 'ai_gateway, public' on every connection acquired. Fail-fast: a bad
// DSN or unreachable Postgres surfaces at startup (Ping) rather than on
// the first request.
func NewPool(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.PGDSN)
	if err != nil {
		return nil, fmt.Errorf("db: parse DSN: %w", err)
	}
	if cfg.PGMaxConns > 0 {
		pcfg.MaxConns = cfg.PGMaxConns
	} else {
		pcfg.MaxConns = 10
	}
	pcfg.MaxConnIdleTime = 5 * time.Minute
	pcfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path = ai_gateway, public")
		if err != nil {
			return fmt.Errorf("db: set search_path: %w", err)
		}
		return nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("db: new pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}
