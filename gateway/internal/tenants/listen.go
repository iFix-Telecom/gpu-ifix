// Package tenants (listen.go): pgxlisten handler subscribing to the
// `tenants_changed` channel and triggering loader.Refresh on each
// notification. Mirrors gateway/internal/upstreams/listen.go.
package tenants

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgxlisten"
)

// ListenAndReload opens a dedicated pgx.Conn (NOT pgxpool — LISTEN state is
// connection-scoped), issues LISTEN tenants_changed, and calls
// loader.Refresh on each NOTIFY. Blocks until ctx is canceled. Reconnects
// with 5s backoff on transient failures (pgxlisten handles the loop).
//
// dsn is the libpq connection string (typically cfg.PGDSN). A fresh
// pgx.Conn is created here so LISTEN does not pin a pool slot.
//
// Returns ctx.Err() on graceful shutdown; returns the underlying error
// only if ctx is still alive.
func ListenAndReload(ctx context.Context, dsn string, loader *Loader, log *slog.Logger) error {
	log = log.With("module", "TENANTS_LISTEN")
	listener := &pgxlisten.Listener{
		Connect: func(ctx context.Context) (*pgx.Conn, error) {
			return pgx.Connect(ctx, dsn)
		},
		LogError: func(_ context.Context, err error) {
			log.Warn("pgxlisten error", "err", err)
		},
		ReconnectDelay: 5 * time.Second,
	}
	listener.Handle("tenants_changed", pgxlisten.HandlerFunc(
		func(ctx context.Context, n *pgconn.Notification, _ *pgx.Conn) error {
			log.Info("tenants_changed NOTIFY received", "payload", n.Payload)
			if err := loader.Refresh(ctx); err != nil {
				log.Error("tenants refresh after NOTIFY failed", "err", err)
				// Returning nil keeps the listener alive (same shape as
				// upstreams/listen.go: a transient refresh error must not
				// take the LISTEN loop down).
				return nil
			}
			return nil
		},
	))
	log.Info("starting LISTEN tenants_changed")
	err := listener.Listen(ctx)
	if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("LISTEN loop exiting", "ctx_err", ctx.Err())
	return ctx.Err()
}
