package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	gatewaydb "github.com/ifixtelecom/gpu-ifix/gateway/db"
)

// migrationsFS is the embedded filesystem of goose SQL migrations. The
// `//go:embed migrations/*.sql` directive itself lives in
// gateway/db/embed.go (package gatewaydb) because Go forbids go:embed
// across parent-directory boundaries; see the doc comment there.
//
// Goose expects the subdirectory name ("migrations") as its second arg,
// so we expose the outer FS here directly.
var migrationsFS fs.FS = gatewaydb.MigrationsFS

func newGooseDB(pool *pgxpool.Pool) (*sql.DB, error) {
	// goose requires a *sql.DB; pgx provides an adapter via stdlib.
	return stdlib.OpenDBFromPool(pool), nil
}

// Up applies all pending migrations in order. Idempotent — goose records
// the applied version in ai_gateway.goose_db_version. search_path is
// forced to ai_gateway,public by the pool's AfterConnect hook, so goose
// creates its bookkeeping table under the same schema (see gateway/db/README.md).
func Up(ctx context.Context, pool *pgxpool.Pool) error {
	db, err := newGooseDB(pool)
	if err != nil {
		return err
	}
	defer db.Close()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("db.Up: set dialect: %w", err)
	}
	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("db.Up: %w", err)
	}
	return nil
}

// Down rolls back n migrations (use 0 to rollback all applied migrations).
func Down(ctx context.Context, pool *pgxpool.Pool, n int) error {
	db, err := newGooseDB(pool)
	if err != nil {
		return err
	}
	defer db.Close()
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if n <= 0 {
		// Rollback everything — goose DownTo version 0.
		if err := goose.DownToContext(ctx, db, "migrations", 0); err != nil {
			return fmt.Errorf("db.Down(all): %w", err)
		}
		return nil
	}
	for i := 0; i < n; i++ {
		if err := goose.DownContext(ctx, db, "migrations"); err != nil {
			return fmt.Errorf("db.Down[%d]: %w", i, err)
		}
	}
	return nil
}

// Status prints the version table to stdout (used by `gatewayctl migrate status`).
func Status(ctx context.Context, pool *pgxpool.Pool) error {
	db, err := newGooseDB(pool)
	if err != nil {
		return err
	}
	defer db.Close()
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.StatusContext(ctx, db, "migrations")
}
