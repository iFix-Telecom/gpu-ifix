//go:build integration

// Phase 5 Plan 05-08 Task 8.2 — SC-3: hot-reload of shed thresholds via
// SQL UPDATE → NOTIFY upstreams_changed → loader refresh → FSM re-evaluates
// in <2s.
//
// SC-3 (CONTEXT.md §Success Criteria):
//
//	"Thresholds (inflight_max, P95_ms, VRAM_bytes, hysteresis_seconds)
//	 can be changed by updating rows in Postgres and take effect within
//	 2s without restarting the gateway."
//
// Scenario:
//  1. Drive the FSM into StateOn by hammering local-llm with a slow
//     mock (600ms latency × 10 RPS = ~6 inflight, above shed_inflight_max=4).
//  2. Confirm gw:shed:local-llm Hash shows state=on (FSM published).
//  3. UPDATE circuit_config.shed_inflight_max=1000 (effectively disable
//     shed). This fires the upstreams_changed NOTIFY trigger which the
//     gateway's pgxlisten consumer picks up, calls loader.Refresh + the
//     onReload callback rebuilds shed.Set thresholds via UpdateConfig.
//  4. Assert FSM transitions out of StateOn within 2s. With the load
//     still applied + threshold raised, the signal evaluates as "not
//     saturated" so FSM goes ON → RECOVERING (hysteresis preserved per
//     D-C5: a hot-reload does not skip the recover window — strict
//     stability).
//
// Failure modes this catches:
//   - NOTIFY trigger not firing (migration 0009 / 0016 trigger bug)
//   - Loader not subscribing to upstreams_changed (Phase 3 D-D4 regression)
//   - shed.Set not bridging the loader callback to UpdateConfig (Plan 06 wiring bug)
//   - FSM holds StateOn indefinitely after threshold raise (D-C5 violation)
package integration

import (
	"net/http"
	"testing"
	"time"

	vegeta "github.com/tsenart/vegeta/lib"
)

func TestSC3_HotReloadAppliesInUnder2Seconds(t *testing.T) {
	stack := newShedStack(t)
	gwURL := bootGateway(stack, nil)

	// Drive FSM to ON: slow tier-0 + sustained inflight burst.
	// 600ms × 10 RPS = ~6 inflight > shed_inflight_max=4 → InflightOverMax.
	// 600ms latency → p95 well over shed_p95_ms=500 → P95OverMax.
	// Two-of-three saturation → FSM goes Off → Armed → On (test-scaled arm=1s).
	stack.Tier0Mock.SetLatency(600 * time.Millisecond)

	target := vegeta.Target{
		Method: "POST",
		URL:    gwURL + "/v1/chat/completions",
		Header: http.Header{
			"Authorization": {"Bearer " + stack.ApiKey("converseai")},
			"Content-Type":  {"application/json"},
		},
		Body: chatBody(),
	}
	rate := vegeta.Rate{Freq: 10, Per: time.Second}
	attacker := vegeta.NewAttacker(vegeta.Timeout(10 * time.Second))

	// Hammer for 6s — enough to satisfy arm=1s + tick latency + some
	// margin. Run in a goroutine so we can keep firing while we issue
	// the SQL UPDATE; load continues during the assertion phase.
	loadCtx := make(chan struct{})
	go func() {
		defer close(loadCtx)
		for res := range attacker.Attack(vegeta.NewStaticTargeter(target), rate, 15*time.Second, "sc3-warmup") {
			_ = res
		}
	}()

	// Wait for FSM=on (with load running).
	if state := waitForState(t, stack, "local-llm", "on", 10*time.Second); state != "on" {
		attacker.Stop()
		<-loadCtx
		t.Fatalf("SC-3 precondition: FSM never reached on; got %q", state)
	}
	t.Log("SC-3: FSM=on confirmed; proceeding with hot-reload")

	// Hot-reload: raise shed_inflight_max to a value the inflight signal
	// cannot reach (1000) → InflightOverMax becomes false. P95OverMax may
	// still be true (latency=600ms > p95_ms=500), but with 2-of-3 needing
	// 2 signals, just P95 alone is insufficient → FSM should transition
	// out of On (Off → Recovering is the strict hysteresis-preserved path).
	updateStart := time.Now()
	sqlUpdate(t, stack, `
		UPDATE ai_gateway.upstreams
		SET circuit_config = circuit_config || jsonb_build_object(
			'shed_inflight_max', 1000,
			'shed_p95_ms', 60000
		)
		WHERE name = 'local-llm'
	`)

	// Assert FSM exits StateOn within 2s of UPDATE.
	// Accepted target states: "recovering" (hysteresis path) or "off"
	// (if the recover_seconds=2 elapsed before our poll fired).
	reachedExit := false
	var lastState string
	deadline := updateStart.Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m, _ := readShedState(stack, "local-llm")
		lastState = m["state"]
		if lastState == "recovering" || lastState == "off" {
			reachedExit = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	elapsed := time.Since(updateStart)

	attacker.Stop()
	<-loadCtx

	if !reachedExit {
		t.Errorf("SC-3 FAIL: FSM did not transition out of on within 2s of UPDATE; last state=%q elapsed=%s", lastState, elapsed)
	} else {
		t.Logf("SC-3 PASS: hot-reload propagated to FSM in %s (state=%s)", elapsed, lastState)
	}
}
