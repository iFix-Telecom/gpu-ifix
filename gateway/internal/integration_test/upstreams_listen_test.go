//go:build integration

package integration

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// TestIntegration_UpstreamsListener_NotifyTriggersRefresh validates the
// end-to-end LISTEN/NOTIFY → loader.Refresh roundtrip through the trigger
// installed by migration 0009. Mutating a config column on the seed table
// must fire the trigger, propagate the NOTIFY to the dedicated pgx.Conn,
// and cause the loader's snapshot to drop the disabled row within the
// 5s budget (CONTEXT.md D-D4 — latency <1s end-to-end).
func TestIntegration_UpstreamsListener_NotifyTriggersRefresh(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	resetUpstreamsTable(t, ctx, pool)

	clearUpstreamEnvs(t)
	t.Setenv("UPSTREAM_LLM_URL", "http://local-llm:8000")
	// Phase 11.1: UPSTREAM_STT_URL setenv removed (local-stt row deleted by 0028).
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

	// Allow LISTEN to register on the dedicated conn before the UPDATE.
	// Without this, a NOTIFY emitted before LISTEN starts is lost.
	time.Sleep(500 * time.Millisecond)

	if _, err := pool.Exec(ctx,
		"UPDATE ai_gateway.upstreams SET enabled = FALSE WHERE name = 'openrouter-chat'"); err != nil {
		t.Fatalf("UPDATE: %v", err)
	}

	// Poll for reload (target <1s; allow 5s headroom for slow CI).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if reloadCount.Load() > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if reloadCount.Load() == 0 {
		t.Fatal("NOTIFY did not trigger reload within 5s")
	}
	if _, ok := loader.Resolve("llm", 1); ok {
		t.Fatal("openrouter-chat still present after UPDATE enabled=FALSE + reload")
	}

	// Clean shutdown — listener goroutine must exit when ctx is canceled
	// (T-03-04-01 mitigation: no LISTEN connection leak).
	listenCancel()
	select {
	case <-listenDone:
	case <-time.After(10 * time.Second):
		t.Fatal("listener goroutine did not exit within 10s of ctx cancel — connection leak")
	}
}

// TestIntegration_UpstreamsListener_ProbeWritebackDoesNotTrigger validates
// that probe writebacks (UpdateUpstreamProbe — last_probe_*) do NOT fire
// the upstreams_change_notify trigger (Pitfall 7 / 03-02 trigger WHEN
// clause). If the WHEN clause is broken, every 10s probe cycle would
// trigger a Refresh — a 6×/10s reload-storm against the gateway's
// in-memory snapshot.
func TestIntegration_UpstreamsListener_ProbeWritebackDoesNotTrigger(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)
	resetUpstreamsTable(t, ctx, pool)

	clearUpstreamEnvs(t)
	t.Setenv("UPSTREAM_LLM_URL", "http://local-llm:8000")
	// Phase 11.1: UPSTREAM_STT_URL setenv removed (local-stt row deleted by 0028).
	t.Setenv("UPSTREAM_EMBED_URL", "http://local-embed:8002")

	loader, err := upstreams.NewLoader(ctx, pool, discardLogger())
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}

	if sharedPGDSN == "" {
		t.Skip("sharedPGDSN not set")
	}

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
	time.Sleep(500 * time.Millisecond)

	// Probe-style writeback — touches only last_probe_* + updated_at.
	// MUST NOT trigger the WHEN-filtered upstreams_update_notify trigger.
	if _, err := pool.Exec(ctx, `UPDATE ai_gateway.upstreams
        SET last_probe_at = NOW(),
            last_probe_ms = 120,
            last_probe_status = 'ok',
            last_probe_error = NULL,
            updated_at = NOW()
        WHERE name = 'local-llm'`); err != nil {
		t.Fatalf("probe writeback UPDATE: %v", err)
	}

	// Wait long enough that any spurious NOTIFY would have been delivered.
	time.Sleep(2 * time.Second)
	if got := reloadCount.Load(); got != 0 {
		t.Fatalf("probe writeback fired trigger %d times; WHEN clause broken (Pitfall 7)", got)
	}

	// Sanity: a real config change still fires the trigger after the
	// no-op probe writeback above. Ensures the listener is actually
	// alive (not silently dead, which would also yield reloadCount==0).
	if _, err := pool.Exec(ctx,
		"UPDATE ai_gateway.upstreams SET enabled = FALSE WHERE name = 'local-llm'"); err != nil {
		t.Fatalf("config UPDATE: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if reloadCount.Load() > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if reloadCount.Load() == 0 {
		t.Fatal("post-probe config UPDATE failed to fire trigger — listener may be dead")
	}

	listenCancel()
	select {
	case <-listenDone:
	case <-time.After(10 * time.Second):
		t.Fatal("listener goroutine did not exit on ctx cancel")
	}
}
