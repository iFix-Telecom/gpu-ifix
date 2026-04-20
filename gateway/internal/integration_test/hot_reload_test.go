//go:build integration

// Phase 3 Plan 03-07 Task 2 — hot-reload latency from operator-driven
// admin UPDATE through to in-memory loader snapshot. The CONTEXT.md
// D-D4 budget is <1s end-to-end; this test polls with 50ms granularity
// up to a 2s headroom and asserts the reload was observed within 1s.
package integration

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// TestIntegration_LoaderReloadWithin1sOfAdminUpdate proves the LISTEN/
// NOTIFY → loader.Refresh pipeline lands a config change in the
// in-memory snapshot within 1s of the admin UPDATE landing in Postgres.
//
// This is the production hot-reload guarantee: when an operator runs
// `gatewayctl upstreams update --enabled=false …`, the running gateway
// must reflect that change without a restart.
func TestIntegration_LoaderReloadWithin1sOfAdminUpdate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	resetUpstreamsTable(t, ctx, pool)

	clearUpstreamEnvs(t)
	t.Setenv("UPSTREAM_LLM_URL", "http://local-llm:8000")
	t.Setenv("UPSTREAM_STT_URL", "http://local-stt:8001")
	t.Setenv("UPSTREAM_EMBED_URL", "http://local-embed:8002")
	t.Setenv("UPSTREAM_LLM_OPENROUTER_URL", "https://openrouter.ai/api/v1")
	t.Setenv("UPSTREAM_LLM_OPENROUTER_AUTH_BEARER", "or-test")
	t.Setenv("UPSTREAM_STT_OPENAI_URL", "https://api.openai.com/v1")
	t.Setenv("UPSTREAM_STT_OPENAI_AUTH_BEARER", "oa-test")
	t.Setenv("UPSTREAM_EMBED_OPENAI_URL", "https://api.openai.com/v1")
	t.Setenv("UPSTREAM_EMBED_OPENAI_AUTH_BEARER", "oa-embed")

	loader, err := upstreams.NewLoader(ctx, pool, discardLogger())
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	if _, ok := loader.Resolve("llm", 1); !ok {
		t.Fatal("openrouter-chat must be present before NOTIFY")
	}

	if sharedPGDSN == "" {
		t.Skip("sharedPGDSN not set (TestMain wiring missing)")
	}

	// Run the listener with a reload counter that mirrors the production
	// onReload callback (which would call breakerSet.Rebuild).
	var reloadCount atomic.Int32
	listenCtx, listenCancel := context.WithCancel(ctx)
	defer listenCancel()
	listenDone := make(chan struct{})
	go func() {
		defer close(listenDone)
		_ = upstreams.ListenAndReload(listenCtx, sharedPGDSN, loader, func() {
			reloadCount.Add(1)
		}, discardLogger())
	}()

	// Allow the dedicated LISTEN connection to register before any
	// UPDATE lands (NOTIFY emitted before LISTEN starts is silently lost).
	time.Sleep(500 * time.Millisecond)

	// Phase 1 — operator disables openrouter-chat. Measure wall time
	// from UPDATE return to loader snapshot reflecting the change.
	updateStart := time.Now()
	if _, err := pool.Exec(ctx,
		"UPDATE ai_gateway.upstreams SET enabled = FALSE WHERE name = 'openrouter-chat'"); err != nil {
		t.Fatalf("UPDATE openrouter-chat: %v", err)
	}

	// Poll loader.Resolve until openrouter-chat disappears (or timeout).
	deadline := time.Now().Add(2 * time.Second)
	var observedAt time.Time
	for time.Now().Before(deadline) {
		if _, ok := loader.Resolve("llm", 1); !ok {
			observedAt = time.Now()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if observedAt.IsZero() {
		t.Fatalf("loader did not reflect UPDATE within 2s; reload count = %d",
			reloadCount.Load())
	}
	reloadLatency := observedAt.Sub(updateStart)
	if reloadLatency > 1*time.Second {
		t.Errorf("hot-reload latency = %v, want <= 1s (D-D4)", reloadLatency)
	}
	t.Logf("hot-reload disable latency = %v (target <1s)", reloadLatency)

	// Phase 2 — re-enable; loader must drop the row back IN within 1s.
	updateStart = time.Now()
	if _, err := pool.Exec(ctx,
		"UPDATE ai_gateway.upstreams SET enabled = TRUE WHERE name = 'openrouter-chat'"); err != nil {
		t.Fatalf("UPDATE re-enable: %v", err)
	}

	deadline = time.Now().Add(2 * time.Second)
	observedAt = time.Time{}
	for time.Now().Before(deadline) {
		if _, ok := loader.Resolve("llm", 1); ok {
			observedAt = time.Now()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if observedAt.IsZero() {
		t.Fatalf("loader did not reflect re-enable within 2s; reload count = %d",
			reloadCount.Load())
	}
	reloadLatency = observedAt.Sub(updateStart)
	if reloadLatency > 1*time.Second {
		t.Errorf("hot-reload re-enable latency = %v, want <= 1s (D-D4)", reloadLatency)
	}
	t.Logf("hot-reload enable latency = %v (target <1s)", reloadLatency)

	// onReload callback must have fired at least 2x (one per UPDATE).
	if got := reloadCount.Load(); got < 2 {
		t.Errorf("onReload fired %d times; expected >= 2", got)
	}

	// Clean shutdown — listener goroutine must exit on ctx cancel.
	listenCancel()
	select {
	case <-listenDone:
	case <-time.After(10 * time.Second):
		t.Fatal("listener goroutine did not exit within 10s of ctx cancel")
	}
}
