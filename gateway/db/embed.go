// Package gatewaydb holds the SQL assets (migrations, queries) for the
// gateway. The Go source here exists ONLY so `//go:embed migrations/*.sql`
// can reach the sibling `migrations/` directory (Go disallows go:embed
// across parent-directory boundaries — the directive must live in the
// same package as the embedded files or in a descendant of it).
//
// The actual migration runner lives at gateway/internal/db/migrate.go
// (package db) and imports MigrationsFS from here.
package gatewaydb

import "embed"

// MigrationsFS is the pressly/goose-compatible embedded filesystem
// containing all versioned SQL migrations for the `ai_gateway` schema.
// Consumed by gateway/internal/db.Up / Down / Status.
//
//go:embed migrations/*.sql
var MigrationsFS embed.FS
