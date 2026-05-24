//go:build integration

// Package main — integration tests for `gatewayctl breaker` audit-log
// writes (Phase 06.9 Plan 04 Task 3 tests 5 & 6). These tests require a
// real Postgres testcontainer (provided by the package-shared TestMain in
// upstreams_test.go) AND a miniredis instance — both audit_log writes and
// Redis force-key writes are exercised.
//
// Naming convention: integration tests in the gatewayctl package use the
// `_integration_test.go` suffix and the `integration` build tag. CI runs
// them via `go test -tags=integration ./cmd/gatewayctl/...`.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
)

// integrationBreakerSetup applies migrations + sets env vars for the
// `gatewayctl breaker` audit-log integration tests. Reuses the
// freshSchema helper from upstreams_test.go (also //go:build integration).
//
// Returns the miniredis address so tests can read Redis assertions
// directly. Postgres is the shared testcontainer.
func integrationBreakerSetup(t *testing.T, ctx context.Context) string {
	t.Helper()
	// freshSchema sets PG_DSN / REDIS_ADDR / UPSTREAM_*_URL env vars and
	// applies migrations. Its REDIS_ADDR points at the shared Redis
	// testcontainer; we override it below with a miniredis we control.
	pool, _ := freshSchema(t, ctx)
	_ = pool

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	t.Setenv("AI_GATEWAY_REDIS_ADDR", mr.Addr())
	return mr.Addr()
}

// Test 5 — TestBreakerForceOpen_WritesAuditLogRow: force-open writes an
// audit_log row with event_kind="breaker_force_open" + upstream + reason.
func TestBreakerForceOpen_WritesAuditLogRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	mrAddr := integrationBreakerSetup(t, ctx)

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if code := runBreaker(ctx, []string{"force-open", "--upstream", "openrouter-chat", "--ttl", "60s"}, log); code != 0 {
		t.Fatalf("force-open exit=%d", code)
	}

	// Wait briefly so the audit writer's batch flush ticker fires
	// (Run runs in a goroutine inside writeBreakerAudit; writeBreakerAudit
	// already sleeps 1.5s after WriteStateChange so we add a small margin
	// here for testcontainer scheduling jitter).
	time.Sleep(500 * time.Millisecond)

	// Verify the audit_log row landed.
	rdb := redis.NewClient(&redis.Options{Addr: mrAddr})
	defer func() { _ = rdb.Close() }()
	key := breaker.ForceOverrideKey("openrouter-chat")
	if _, err := rdb.Get(ctx, key).Result(); err != nil {
		t.Errorf("Redis force key missing: %v", err)
	}

	// Now check Postgres audit_log via the shared pool (freshSchema
	// already applied migrations).
	dsn := os.Getenv("AI_GATEWAY_PG_DSN")
	if dsn == "" {
		t.Fatal("AI_GATEWAY_PG_DSN unset; freshSchema did not run")
	}
	verifyAuditLogRowExists(t, ctx, dsn, "breaker_force_open", "openrouter-chat")
}

// Test 6 — TestBreakerForceClose_WritesAuditLogRow.
func TestBreakerForceClose_WritesAuditLogRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = integrationBreakerSetup(t, ctx)

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if code := runBreaker(ctx, []string{"force-open", "--upstream", "openrouter-chat", "--ttl", "60s"}, log); code != 0 {
		t.Fatalf("force-open exit=%d", code)
	}
	if code := runBreaker(ctx, []string{"force-close", "--upstream", "openrouter-chat"}, log); code != 0 {
		t.Fatalf("force-close exit=%d", code)
	}
	time.Sleep(500 * time.Millisecond)

	dsn := os.Getenv("AI_GATEWAY_PG_DSN")
	verifyAuditLogRowExists(t, ctx, dsn, "breaker_force_close", "openrouter-chat")
}

// verifyAuditLogRowExists scans ai_gateway.audit_log for at least one row
// matching (event_kind, upstream). Asserts via t.Errorf if no row found.
func verifyAuditLogRowExists(t *testing.T, ctx context.Context, dsn, eventKind, upstream string) {
	t.Helper()
	// Use pgxpool via the existing db helper to honor the shared
	// connection limits in tests.
	t.Setenv("AI_GATEWAY_PG_DSN", dsn)
	_, pool, err := loadAndPool(ctx, slog.Default())
	if err != nil {
		t.Fatalf("loadAndPool for audit check: %v", err)
	}
	defer pool.Close()

	var count int
	err = pool.QueryRow(ctx, `
        SELECT count(*) FROM ai_gateway.audit_log
        WHERE event_kind = $1 AND upstream = $2
    `, eventKind, upstream).Scan(&count)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if count < 1 {
		t.Errorf("expected ≥1 audit_log row with event_kind=%s upstream=%s; got %d", eventKind, upstream, count)
		// Dump nearby rows to help debug.
		rows, _ := pool.Query(ctx, `SELECT event_kind, upstream, route, method, reason FROM ai_gateway.audit_log WHERE event_kind IS NOT NULL ORDER BY ts DESC LIMIT 5`)
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var ek, up, route, meth, reason string
				_ = rows.Scan(&ek, &up, &route, &meth, &reason)
				t.Logf("audit row: event_kind=%s upstream=%s route=%s method=%s reason=%s", ek, up, route, meth, reason)
			}
		}
	}
}

// Sanity ping to keep the json import live in case the verifier helpers
// are pruned in a refactor.
var _ = json.Marshal

// Compile-time guard that the env helper resolves.
var _ = fmt.Sprint
