//go:build integration

// Phase 5 Plan 05-08 Task 8.2 — SC-4: anti-starvation — noisy tenant
// bursting does not degrade a quiet tenant's quality of service.
//
// SC-4 (CONTEXT.md §Success Criteria):
//
//	"During shedding, one tenant's burst does not starve other tenants —
//	 per-tenant inflight quotas keep smaller apps responsive while
//	 overflow from the noisy tenant hits OpenRouter."
//
// Scenario:
//   - Tenant A ("converseai", local_inflight_max_llm=4) bursts at 100 RPS
//     for 10s → ~1000 requests, far exceeding the per-tenant cap.
//   - Tenant B ("campanhas", local_inflight_max_llm=4) runs concurrently
//     at 5 RPS for 10s → ~50 requests, well within cap.
//   - Tier-0 mock is slow (200ms) → inflight on local-llm builds rapidly,
//     FSM goes ON. Tenant A's excess requests overflow to tier-1.
//   - Tenant B's requests stay on tier-0 (or fall back gracefully) and
//     should NOT show degraded latency or success rate.
//
// Assertions:
//   - Tenant B success rate ≥ 0.95 (no starvation).
//   - Tenant B P99 latency ≤ 2s (no catastrophic queuing).
//
// What this validates:
//   - shed.Middleware reads per-tenant inflight from InflightRegistry
//     (not global inflight) when deciding to override → tier-1.
//   - LocalInflightMaxLLM cap is enforced PER TENANT, not globally.
//   - One tenant's noisy burst does not consume the global shed_inflight_max
//     slot space and lock out other tenants.
package integration

import (
	"net/http"
	"sync"
	"testing"
	"time"

	vegeta "github.com/tsenart/vegeta/lib"
)

func TestSC4_NoisyTenantDoesNotStarveQuietTenant(t *testing.T) {
	stack := newShedStack(t)
	gwURL := bootGateway(stack, nil)
	stack.Tier0Mock.SetLatency(200 * time.Millisecond)

	targetA := vegeta.Target{
		Method: "POST",
		URL:    gwURL + "/v1/chat/completions",
		Header: http.Header{
			"Authorization": {"Bearer " + stack.ApiKey("converseai")},
			"Content-Type":  {"application/json"},
		},
		Body: chatBody(),
	}
	targetB := vegeta.Target{
		Method: "POST",
		URL:    gwURL + "/v1/chat/completions",
		Header: http.Header{
			"Authorization": {"Bearer " + stack.ApiKey("campanhas")},
			"Content-Type":  {"application/json"},
		},
		Body: chatBody(),
	}

	var wg sync.WaitGroup
	var metricsA, metricsB vegeta.Metrics
	wg.Add(2)

	// Tenant A — noisy: 100 RPS × 10s. Drives FSM to ON quickly and
	// produces excess inflight that the middleware must redirect to
	// tier-1 (not block tenant B's slots).
	go func() {
		defer wg.Done()
		attacker := vegeta.NewAttacker(vegeta.Timeout(10 * time.Second))
		for res := range attacker.Attack(vegeta.NewStaticTargeter(targetA),
			vegeta.Rate{Freq: 100, Per: time.Second}, 10*time.Second, "A-noisy") {
			metricsA.Add(res)
		}
		metricsA.Close()
	}()

	// Tenant B — quiet: 5 RPS × 10s. Should not be affected by A's
	// burst. Each request must either stay on tier-0 (B inflight < cap=4)
	// or transparently fall back to tier-1 with similar latency.
	go func() {
		defer wg.Done()
		attacker := vegeta.NewAttacker(vegeta.Timeout(10 * time.Second))
		for res := range attacker.Attack(vegeta.NewStaticTargeter(targetB),
			vegeta.Rate{Freq: 5, Per: time.Second}, 10*time.Second, "B-quiet") {
			metricsB.Add(res)
		}
		metricsB.Close()
	}()
	wg.Wait()

	t.Logf("SC-4 Tenant A (noisy 100 RPS): success=%.3f p50=%s p99=%s",
		metricsA.Success, metricsA.Latencies.P50, metricsA.Latencies.P99)
	t.Logf("SC-4 Tenant B (quiet 5 RPS): success=%.3f p50=%s p99=%s",
		metricsB.Success, metricsB.Latencies.P50, metricsB.Latencies.P99)

	// Primary assertion: tenant B must remain responsive.
	if metricsB.Success < 0.95 {
		t.Errorf("SC-4 FAIL: tenant B success=%.3f, want ≥0.95 (starved by tenant A)", metricsB.Success)
	}

	// Secondary assertion: tenant B P99 must not blow up. With tier-0
	// latency 200ms and tier-1 latency ~0ms, the natural P99 is ≤500ms.
	// 2s gives 10x slack for outliers + scheduler jitter under a 105
	// RPS combined load on a CI runner.
	if metricsB.Latencies.P99 > 2*time.Second {
		t.Errorf("SC-4 FAIL: tenant B P99=%s, want ≤2s (degraded latency under tenant A burst)",
			metricsB.Latencies.P99)
	}
}
