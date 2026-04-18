package db

import (
	"context"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
)

// TestNewPool_InvalidDSNReturnsError documents that a malformed DSN
// fails fast (parse or ping error) rather than being deferred to first
// query. Integration tests (Plan 02-07) exercise the happy path against
// a real Postgres via testcontainers-go.
func TestNewPool_InvalidDSNReturnsError(t *testing.T) {
	cfg := config.Config{PGDSN: "not-a-valid-dsn", PGMaxConns: 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := NewPool(ctx, cfg); err == nil {
		t.Fatal("expected error for bad DSN")
	}
}
