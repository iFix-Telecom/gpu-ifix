//go:build integration

// Phase 5 Plan 05-08 Task 8.2 — SC-1: burst exceeds tenant cap, overflow
// goes to tier-1, FSM recovers when load drops.
//
// SC-1 (CONTEXT.md §Success Criteria):
//
//	"Under a burst where inflight on local LLM exceeds the configured
//	 slot count, excess requests are routed to OpenRouter automatically;
//	 below threshold, traffic returns to local."
//
// Scenario (test-scaled timings from helpers_shed_test.go seedShedThresholds):
//   - tenant cap_llm=4, shed_inflight_max=4, shed_arm_seconds=1, shed_recover_seconds=2
//   - tier-0 mock has 300ms latency → 50 RPS for 20s holds ~15 inflight
//     simultaneously, comfortably > shed_inflight_max=4
//   - vegeta attacks at 50 RPS for 20s = ~1000 requests
//   - expect tier-1 hits ≥ 50 (overflow happened — many requests for the
//     same tenant beyond cap=4 were re-routed)
//   - success rate ≥ 0.95 (tier-1 always 2xx in baseline mode)
//   - after attack stops, FSM transitions back to "off" within 15s
//     (test-scaled arm+recover totals 3s; 15s = 5x scheduler slack for
//     subprocess + Redis + tick latency)
package integration

import (
	"net/http"
	"testing"
	"time"

	vegeta "github.com/tsenart/vegeta/lib"
)

func TestSC1_BurstExceedsTenantCapOverflowsToTier1(t *testing.T) {
	stack := newShedStack(t)
	gwURL := bootGateway(stack, nil)

	// Slow upstream → inflight builds rapidly. 300ms × 50 RPS holds
	// ~15 inflight simultaneously, well above shed_inflight_max=4.
	stack.Tier0Mock.SetLatency(300 * time.Millisecond)
	stack.Tier0Mock.ResetHits()
	stack.Tier1Mock.ResetHits()

	target := vegeta.Target{
		Method: "POST",
		URL:    gwURL + "/v1/chat/completions",
		Header: http.Header{
			"Authorization": {"Bearer " + stack.ApiKey("converseai")},
			"Content-Type":  {"application/json"},
		},
		Body: chatBody(),
	}
	targeter := vegeta.NewStaticTargeter(target)
	rate := vegeta.Rate{Freq: 50, Per: time.Second}
	attacker := vegeta.NewAttacker(vegeta.Timeout(10 * time.Second))

	var metrics vegeta.Metrics
	for res := range attacker.Attack(targeter, rate, 20*time.Second, "SC-1 burst") {
		metrics.Add(res)
	}
	metrics.Close()

	tier0Hits := stack.Tier0Mock.Hits()
	tier1Hits := stack.Tier1Mock.Hits()
	t.Logf("SC-1 burst result: tier-0 hits=%d, tier-1 hits=%d, success=%.3f, p99=%s",
		tier0Hits, tier1Hits, metrics.Success, metrics.Latencies.P99)

	// Primary assertion: tier-1 absorbed overflow. Threshold 50 is
	// conservative — under perfect shedding behavior we expect ~900
	// (1000 reqs, cap=4 means ~95% overflow). 50 catches the case
	// where shed activates but FSM oscillates; below 50 means shed
	// never engaged or middleware is broken.
	if tier1Hits < 50 {
		t.Errorf("SC-1 FAIL: tier-1 hits=%d, want ≥50 (no overflow detected — shed not activating)", tier1Hits)
	}

	// Success rate must remain high — tier-1 mock returns 200 by
	// default, so even when tier-0 is fully saturated, end-user
	// requests should succeed. Below 0.95 means the dispatcher's
	// fallback path is broken.
	if metrics.Success < 0.95 {
		t.Errorf("SC-1 FAIL: success rate=%.3f, want ≥0.95 (overflow path failing)", metrics.Success)
	}

	// Recovery phase: load drops, FSM should transition Off via
	// ON → RECOVERING → OFF within test-scaled timings.
	// With arm=1s + recover=2s, the theoretical minimum is ~3s.
	// 15s gives 5x scheduler slack for subprocess + Redis + tick latency.
	stack.Tier0Mock.SetLatency(0)
	deadline := time.Now().Add(15 * time.Second)
	var lastState string
	for time.Now().Before(deadline) {
		m, _ := readShedState(stack, "local-llm")
		lastState = m["state"]
		if lastState == "off" {
			t.Logf("SC-1 PASS: FSM returned to off after load drop")
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Errorf("SC-1 FAIL: FSM did not return to off within 15s after burst ended; last state=%q", lastState)
}
