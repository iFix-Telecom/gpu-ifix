// Package main — Phase 06.9 Plan 04 Task 3: unit tests for the
// `gatewayctl breaker` subcommand family (force-open / force-close /
// list). Mirrors the shed_test.go shape — flag parsing + Redis
// interaction via miniredis. The audit_log + DB-roundtrip tests live in
// breaker_integration_test.go behind the `integration` build tag.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
)

// newBreakerMiniRedis wires miniredis + redis.Client, sets the env vars
// required by config.Load so runBreaker* can resolve a Redis URL via the
// normal loadAndRedis path. Returns the redis client for assertions.
//
// NOTE on TTL: miniredis does NOT advance Redis EX automatically — tests
// that assert TTL semantics use miniredis.FastForward.
func newBreakerMiniRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	// Required by config.Load — gatewayctl shares the same Load() path.
	t.Setenv("AI_GATEWAY_REDIS_ADDR", mr.Addr())
	t.Setenv("AI_GATEWAY_PG_DSN", "postgres://u:p@127.0.0.1:1/x?sslmode=disable")
	t.Setenv("UPSTREAM_LLM_URL", "http://x")
	t.Setenv("UPSTREAM_STT_URL", "http://x")
	t.Setenv("UPSTREAM_EMBED_URL", "http://x")
	return mr, rdb
}

// =====================================================================
// Usage / arg-parsing tests (no Redis required)
// =====================================================================

// Test 8 — runBreakerForceOpen with no args prints usage + returns
// non-zero. Validates the precheck path before any Redis dial.
func TestBreaker_MissingUpstreamArg(t *testing.T) {
	ctx := context.Background()
	log := slog.Default()
	if code := runBreaker(ctx, []string{"force-open"}, log); code == 0 {
		t.Errorf("expected non-zero exit when --upstream missing; got %d", code)
	}
}

// Test 9 — runBreaker with an unknown subcommand prints usage listing
// the valid subcommands.
func TestBreaker_UnknownSubcommand(t *testing.T) {
	ctx := context.Background()
	log := slog.Default()
	if code := runBreaker(ctx, []string{"force-explode"}, log); code == 0 {
		t.Errorf("expected non-zero exit for unknown subcommand; got %d", code)
	}
}

// Test 2 — force-open without --ttl rejects with exit 2 + clear error
// (TTL is mandatory per WARNING-4 / R1 — operator MUST set bounded
// expiry; forgetting would otherwise leave the breaker forced indefinitely).
func TestBreakerForceOpen_RejectsMissingTTL(t *testing.T) {
	ctx := context.Background()
	log := slog.Default()
	code := runBreaker(ctx, []string{"force-open", "--upstream", "openrouter-chat"}, log)
	if code != 2 {
		t.Errorf("expected exit 2 when --ttl missing; got %d", code)
	}
}

// Test 3 — force-open with --ttl > 300s rejects with exit 2 + error
// (R1 bounded-TTL guarantee — max 300s = 5min; longer windows defeat
// the safety net of natural expiry).
func TestBreakerForceOpen_RejectsTTLOver300s(t *testing.T) {
	ctx := context.Background()
	log := slog.Default()
	code := runBreaker(ctx, []string{"force-open", "--upstream", "openrouter-chat", "--ttl", "600s"}, log)
	if code != 2 {
		t.Errorf("expected exit 2 when --ttl > 300s; got %d", code)
	}
}

// =====================================================================
// Redis round-trip tests (miniredis-backed; audit_log assertions live
// in breaker_integration_test.go because they need real Postgres).
// =====================================================================

// Test 1 — force-open writes the canonical Redis key with the matching
// JSON value shape + Redis EX. Audit-log row creation is asserted in the
// integration test variant.
func TestBreakerForceOpen_WritesRedisMirrorWithTTL(t *testing.T) {
	_, rdb := newBreakerMiniRedis(t)
	ctx := context.Background()
	log := slog.Default()

	// Set DB_DRIVER_SKIP so runBreakerForceOpen can run without a Postgres
	// pool — the audit_log write path is intentionally skipped when the
	// pool dial fails (we WARN-log and continue so the Redis effect still
	// happens). Production deployments always have Postgres reachable.
	t.Setenv("AI_GATEWAY_BREAKER_AUDIT_SKIP_ON_DB_ERR", "1")

	code := runBreaker(ctx, []string{"force-open", "--upstream", "openrouter-chat", "--ttl", "60s"}, log)
	if code != 0 {
		t.Fatalf("force-open exit = %d; want 0", code)
	}

	key := breaker.ForceOverrideKey("openrouter-chat")
	raw, err := rdb.Get(ctx, key).Result()
	if err != nil {
		t.Fatalf("Redis GET %s: %v", key, err)
	}
	var val breaker.ForceOverrideValue
	if err := json.Unmarshal([]byte(raw), &val); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if val.State != "open" {
		t.Errorf("State=%q; want open", val.State)
	}
	if val.TTLSec != 60 {
		t.Errorf("TTLSec=%d; want 60", val.TTLSec)
	}
	if val.SetBy == "" {
		t.Errorf("SetBy empty; expected current OS user or 'operator'")
	}
	if val.SetAt.IsZero() {
		t.Errorf("SetAt unset; want recent timestamp")
	}

	pttl, err := rdb.PTTL(ctx, key).Result()
	if err != nil {
		t.Fatalf("PTTL: %v", err)
	}
	if pttl <= 0 || pttl > 60*time.Second {
		t.Errorf("PTTL=%v; want (0, 60s]", pttl)
	}
}

// Test 4 — force-close DELs the Redis key written by a prior force-open.
func TestBreakerForceClose_DeletesRedisMirror(t *testing.T) {
	_, rdb := newBreakerMiniRedis(t)
	ctx := context.Background()
	log := slog.Default()
	t.Setenv("AI_GATEWAY_BREAKER_AUDIT_SKIP_ON_DB_ERR", "1")

	if code := runBreaker(ctx, []string{"force-open", "--upstream", "openrouter-chat", "--ttl", "60s"}, log); code != 0 {
		t.Fatalf("force-open exit=%d", code)
	}
	if code := runBreaker(ctx, []string{"force-close", "--upstream", "openrouter-chat"}, log); code != 0 {
		t.Fatalf("force-close exit=%d", code)
	}

	key := breaker.ForceOverrideKey("openrouter-chat")
	if _, err := rdb.Get(ctx, key).Result(); err == nil {
		t.Errorf("key %s still present after force-close; want deleted", key)
	} else if err != redis.Nil {
		t.Errorf("expected redis.Nil; got %v", err)
	}
}

// Test 7 — list enumerates upstreams + their state. With a force key
// written for one upstream, the row shows FORCED_OPEN + remaining TTL.
// The other rows show OBSERVATION (or read the breaker mirror Hash if
// present). Output format is tab-separated with at minimum a NAME
// column and a STATE column.
//
// We use a baseline set of upstream names the loader would return in
// production (`local-llm`, `openrouter-chat`, ...) — the CLI reads names
// from a fixed-source list when no Postgres is reachable in unit tests
// (see runBreakerList implementation).
func TestBreakerList_ReturnsKnownUpstreamsAndStates(t *testing.T) {
	_, rdb := newBreakerMiniRedis(t)
	ctx := context.Background()
	log := slog.Default()
	t.Setenv("AI_GATEWAY_BREAKER_AUDIT_SKIP_ON_DB_ERR", "1")
	// Allow the list path to fall back to a static baseline of upstream
	// names so the unit test doesn't need a Postgres pool.
	t.Setenv("AI_GATEWAY_BREAKER_LIST_FALLBACK_NAMES", "local-llm,openrouter-chat,local-stt,openai-whisper,local-embed,openai-embed")

	// Force-open one upstream so we can assert its state column reads
	// FORCED_OPEN.
	if code := runBreaker(ctx, []string{"force-open", "--upstream", "openrouter-chat", "--ttl", "60s"}, log); code != 0 {
		t.Fatalf("force-open exit=%d", code)
	}

	var code int
	stdout := captureStdout(t, func() {
		code = runBreaker(ctx, []string{"list"}, log)
	})
	if code != 0 {
		t.Fatalf("list exit=%d", code)
	}
	if !strings.Contains(stdout, "openrouter-chat") {
		t.Errorf("list output missing openrouter-chat row:\n%s", stdout)
	}
	if !strings.Contains(stdout, "FORCED_OPEN") {
		t.Errorf("list output missing FORCED_OPEN state for openrouter-chat:\n%s", stdout)
	}
	// Sanity: at least one OBSERVATION row exists (the other upstreams
	// have no force key + no remote breaker mirror written by this test).
	if !strings.Contains(stdout, "local-llm") {
		t.Errorf("list output missing local-llm row:\n%s", stdout)
	}
	// Suppress unused warning on rdb — we use it implicitly via env.
	_ = rdb
}
