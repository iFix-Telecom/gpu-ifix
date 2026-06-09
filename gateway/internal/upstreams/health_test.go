package upstreams_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/sony/gobreaker/v2"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// sixUpstreams returns a 6-row fixture matching the migration 0008
// seed: one tier-0 + one tier-1 per role (llm/stt/embed).
func sixUpstreams() []upstreams.UpstreamConfig {
	return []upstreams.UpstreamConfig{
		{Name: "local-llm", Role: "llm", Tier: 0, URL: "http://llm", Enabled: true},
		{Name: "openrouter-chat", Role: "llm", Tier: 1, URL: "http://or", Enabled: true},
		{Name: "local-stt", Role: "stt", Tier: 0, URL: "http://stt", Enabled: true},
		{Name: "openai-whisper", Role: "stt", Tier: 1, URL: "http://oa", Enabled: true},
		{Name: "local-embed", Role: "embed", Tier: 0, URL: "http://em", Enabled: true},
		{Name: "openai-embed", Role: "embed", Tier: 1, URL: "http://oa", Enabled: true},
	}
}

// newMinRedis spins up an in-process miniredis + go-redis client. Test
// cleanup closes both. Tests that drive the breaker need this because
// gobreaker's OnStateChange calls the Redis publish path even though
// failures are best-effort.
func newMinRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// tripBreaker drives `name` to OPEN by issuing 3 5xx errors through
// breaker.Set.Execute. After the third failure the gobreaker
// transitions; we sleep briefly so the OnStateChange goroutine commits
// + Redis publish completes before the assertion.
func tripBreaker(t *testing.T, bs *breaker.Set, name string) {
	t.Helper()
	for i := 0; i < 3; i++ {
		_, _ = bs.Execute(name, func() (*http.Response, error) {
			return nil, &breaker.HTTPError{Status: 503, Msg: "trip"}
		})
	}
	time.Sleep(20 * time.Millisecond)
	cb, ok := bs.Get(name)
	if !ok {
		t.Fatalf("breaker for %q not found", name)
	}
	if got := cb.State(); got != gobreaker.StateOpen {
		t.Fatalf("breaker %q: want StateOpen, got %v", name, got)
	}
}

// loaderNames is a tiny helper so tests can construct a breaker.Set
// from the same name list the loader exposes.
func loaderNames(l *upstreams.Loader) []string { return l.Names() }

// TestHealthHandler_AllClosed_OK — every breaker CLOSED → status=ok,
// HTTP 200.
func TestHealthHandler_AllClosed_OK(t *testing.T) {
	loader := upstreams.NewLoaderForTest(sixUpstreams()...)
	rdb := newMinRedis(t)
	bs := breaker.NewSet(rdb, discardLogger(), breaker.DefaultOptions(), loaderNames(loader))
	h := upstreams.NewHealthHandler(loader, bs, discardLogger())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/health/upstreams", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("status field = %v, want ok", body["status"])
	}
	ups, ok := body["upstreams"].(map[string]any)
	if !ok {
		t.Fatalf("upstreams field missing or wrong type: %v", body["upstreams"])
	}
	if len(ups) != 6 {
		t.Errorf("upstreams count = %d, want 6", len(ups))
	}
	llm, ok := ups["local-llm"].(map[string]any)
	if !ok {
		t.Fatalf("local-llm entry missing")
	}
	if llm["state"] != "closed" {
		t.Errorf("local-llm.state = %v, want closed", llm["state"])
	}
	if llm["role"] != "llm" {
		t.Errorf("local-llm.role = %v, want llm", llm["role"])
	}
}

// TestHealthHandler_Tier0OpenButTier1Closed_Degraded — one tier-0 OPEN
// while the same role's tier-1 is CLOSED → status=degraded, HTTP 200.
// Use a long Cooldown so the OPEN doesn't auto-transition during the
// sub-millisecond test window.
func TestHealthHandler_Tier0OpenButTier1Closed_Degraded(t *testing.T) {
	loader := upstreams.NewLoaderForTest(sixUpstreams()...)
	rdb := newMinRedis(t)
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 3, Cooldown: 30 * time.Second},
		loaderNames(loader))
	tripBreaker(t, bs, "local-llm")

	h := upstreams.NewHealthHandler(loader, bs, discardLogger())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/health/upstreams", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["status"] != "degraded" {
		t.Fatalf("status = %v, want degraded", body["status"])
	}
}

// TestHealthHandler_NoClosedForRole_Failed — both stt upstreams OPEN →
// status=failed, HTTP 503.
func TestHealthHandler_NoClosedForRole_Failed(t *testing.T) {
	loader := upstreams.NewLoaderForTest(sixUpstreams()...)
	rdb := newMinRedis(t)
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 3, Cooldown: 30 * time.Second},
		loaderNames(loader))
	tripBreaker(t, bs, "local-stt")
	tripBreaker(t, bs, "openai-whisper")

	h := upstreams.NewHealthHandler(loader, bs, discardLogger())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/health/upstreams", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["status"] != "failed" {
		t.Fatalf("status = %v, want failed", body["status"])
	}
}

// TestHealthHandler_ForceOverrideEmitsForcedOpen — Phase 06.9 (SEED-005):
// when an operator installs a force-override at gw:breaker:force:{name},
// /v1/health/upstreams MUST report state="forced-open" for that upstream
// and overall status="degraded" (because the same role's tier-1 is still
// closed, so allTier0Closed=false but allRolesHaveAnyClosed=true).
func TestHealthHandler_ForceOverrideEmitsForcedOpen(t *testing.T) {
	loader := upstreams.NewLoaderForTest(sixUpstreams()...)
	rdb := newMinRedis(t)
	bs := breaker.NewSet(rdb, discardLogger(), breaker.DefaultOptions(), loaderNames(loader))

	ctx := context.Background()
	val := breaker.ForceOverrideValue{State: "open", TTLSec: 300, SetBy: "test", SetAt: time.Now().UTC()}
	buf, _ := json.Marshal(val)
	if err := rdb.Set(ctx, breaker.ForceOverrideKey("local-llm"), string(buf), 300*time.Second).Err(); err != nil {
		t.Fatalf("SET: %v", err)
	}
	bs.RefreshForceOverride(ctx, "local-llm")

	h := upstreams.NewHealthHandler(loader, bs, discardLogger())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/health/upstreams", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (degraded keeps 200)", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["status"] != "degraded" {
		t.Fatalf("top-level status = %v, want degraded", body["status"])
	}
	ups, ok := body["upstreams"].(map[string]any)
	if !ok {
		t.Fatalf("upstreams field missing or wrong type: %v", body["upstreams"])
	}
	llm, ok := ups["local-llm"].(map[string]any)
	if !ok {
		t.Fatalf("local-llm entry missing")
	}
	if llm["state"] != "forced-open" {
		t.Fatalf("local-llm.state = %v, want forced-open", llm["state"])
	}
	// Sibling tier-1 must still report closed — force-override is per-name.
	or, ok := ups["openrouter-chat"].(map[string]any)
	if !ok {
		t.Fatalf("openrouter-chat entry missing")
	}
	if or["state"] != "closed" {
		t.Fatalf("openrouter-chat.state = %v, want closed (force-override is per-name)", or["state"])
	}
}

// TestHealthHandler_Cache2s — 3 rapid GETs share a cached body even
// when the breaker state changes underneath. After the cache TTL
// elapses (2s), the next GET MUST observe the new state.
func TestHealthHandler_Cache2s(t *testing.T) {
	if testing.Short() {
		t.Skip("cache TTL test requires real-time wait; skip in -short mode")
	}
	loader := upstreams.NewLoaderForTest(sixUpstreams()...)
	rdb := newMinRedis(t)
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 3, Cooldown: 30 * time.Second},
		loaderNames(loader))
	h := upstreams.NewHealthHandler(loader, bs, discardLogger())

	req := httptest.NewRequest(http.MethodGet, "/v1/health/upstreams", nil)

	// Prime the cache with all-CLOSED.
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("priming req %d: code = %d", i, rec.Code)
		}
	}

	// Trip a breaker — should NOT be visible while cache is warm.
	tripBreaker(t, bs, "local-llm")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	ups := body["upstreams"].(map[string]any)
	llm := ups["local-llm"].(map[string]any)
	if llm["state"] != "closed" {
		t.Fatalf("cache TTL not honored: local-llm.state = %v immediately after trip", llm["state"])
	}

	// Wait past the cache TTL — the next GET MUST re-snapshot and see
	// the OPEN state.
	time.Sleep(2100 * time.Millisecond)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	var body2 map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &body2)
	ups2 := body2["upstreams"].(map[string]any)
	llm2 := ups2["local-llm"].(map[string]any)
	if llm2["state"] != "open" {
		t.Fatalf("cache did not expire after 2s: local-llm.state = %v, want open", llm2["state"])
	}
}
