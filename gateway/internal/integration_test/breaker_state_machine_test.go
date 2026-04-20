//go:build integration

// Phase 3 Plan 03-07 Task 2 — breaker state machine end-to-end.
//
// Drives one mock httptest server through the full gobreaker lifecycle
// (CLOSED → OPEN → HALF_OPEN → CLOSED) using the production breaker.Set
// (Plan 03-03). All transitions are observed via Snapshot() reads — no
// internal gobreaker fields are touched. Cooldown is set to 300ms so the
// test runs sub-second wall time.
package integration

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sony/gobreaker/v2"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
)

// TestIntegration_BreakerFullLifecycle proves CLOSED → OPEN → HALF_OPEN →
// CLOSED transitions are visible via breaker.Set.Snapshot(). Mock returns
// 500 for the first 3 calls (trip threshold) then 200 (recovery).
func TestIntegration_BreakerFullLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, rdb := freshSchema(t, ctx)

	// Mock that returns 500 for the first 3 calls, then 200.
	var hits atomic.Int64
	mock := newCountingMockWithHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		n := hits.Add(1)
		if n <= 3 {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mock.hits = &hits // keep counter accessible to assertions

	// Build breaker.Set against testcontainer Redis with a fast cooldown.
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 3, Cooldown: 300 * time.Millisecond},
		[]string{"local-llm"},
	)

	// Phase 1: CLOSED. Snapshot must show "closed" before any traffic.
	if got := bs.Snapshot()["local-llm"]; got != "closed" {
		t.Fatalf("baseline state = %q, want closed", got)
	}

	// Phase 2: drive 3 failed requests through the breaker. The breaker
	// trips on the 3rd consecutive failure (ConsecutiveFailures=3).
	client := mock.server.Client()
	for i := 0; i < 3; i++ {
		_, err := bs.Execute("local-llm", func() (*http.Response, error) {
			resp, herr := client.Get(mock.server.URL)
			if herr != nil {
				return nil, herr
			}
			if resp.StatusCode >= 500 {
				resp.Body.Close()
				return nil, &breaker.HTTPError{Status: resp.StatusCode, Msg: "upstream 5xx"}
			}
			return resp, nil
		})
		_ = err
	}

	// 3 hits to the upstream (no extra retries — Phase 3 hot path is
	// 1-shot through the breaker).
	if got := mock.hits.Load(); got != 3 {
		t.Fatalf("upstream hits after trip phase = %d, want 3", got)
	}
	// State must be OPEN now (or we would have tripped on the 3rd
	// success-counter-reset; gobreaker semantics are deterministic).
	if got := bs.Snapshot()["local-llm"]; got != "open" {
		t.Fatalf("state after 3 failures = %q, want open", got)
	}

	// Phase 3: while OPEN, calls are short-circuited (no upstream hits).
	hitsBeforeShortCircuit := mock.hits.Load()
	_, err := bs.Execute("local-llm", func() (*http.Response, error) {
		t.Fatal("breaker fn must not run while OPEN")
		return nil, nil
	})
	if err == nil {
		t.Error("expected non-nil error when calling Execute on OPEN breaker")
	}
	if mock.hits.Load() != hitsBeforeShortCircuit {
		t.Errorf("upstream was hit while breaker OPEN; before=%d after=%d",
			hitsBeforeShortCircuit, mock.hits.Load())
	}

	// Phase 4: wait past cooldown so the next call probes HALF_OPEN.
	time.Sleep(350 * time.Millisecond)

	// In HALF_OPEN, the gobreaker allows MaxRequests=1 probe. The mock now
	// returns 200 for the 4th overall hit → CLOSED.
	_, err = bs.Execute("local-llm", func() (*http.Response, error) {
		resp, herr := client.Get(mock.server.URL)
		if herr != nil {
			return nil, herr
		}
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			return nil, &breaker.HTTPError{Status: resp.StatusCode, Msg: "upstream 5xx"}
		}
		return resp, nil
	})
	if err != nil {
		t.Fatalf("HALF_OPEN probe should succeed: %v", err)
	}

	// Phase 5: state must converge back to CLOSED after the success probe.
	if !waitForBreakerState(bs, "local-llm", gobreaker.StateClosed, 500*time.Millisecond) {
		t.Fatalf("state after HALF_OPEN success = %q, want closed",
			bs.Snapshot()["local-llm"])
	}
	if got := mock.hits.Load(); got != 4 {
		t.Errorf("upstream hits after recovery = %d, want 4 (3 fails + 1 probe)", got)
	}
}
