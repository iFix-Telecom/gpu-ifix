//go:build integration && integration_slow

// Phase 5 Plan 05-08 Task 8.3 — SC-2: hysteresis prevents flapping under
// oscillating load.
//
// SC-2 (CONTEXT.md §Success Criteria):
//
//	"Under sustained P95 latency spike or VRAM > 21 GB, shedding activates
//	 within 30s; no flapping occurs during 60s of oscillating load
//	 (hysteresis verified)."
//
// Opt-in slow test: ~125s runtime. Built only with the `integration_slow`
// build tag so the default `go test -tags=integration` suite stays under
// 5 min (threat T-05-15: CI timeout DoS).
//
// Scenario:
//   - Oscillate tier-0 mock latency between HIGH (600ms — drives P95 over
//     threshold) and LOW (10ms — drops signal) in 10s cycles for 120s.
//   - Drive 20 RPS sustained load so the P95 ring buffer always has
//     fresh samples (otherwise the FSM evaluator sees stale signal).
//   - Subscribe to gw:shed:events BEFORE oscillation begins and count
//     every message — each FSM transition publishes exactly one event,
//     so message count == transition count.
//
// Assertion:
//   - Total transitions ≤ 4 over the 120s window.
//     A full cycle is: OFF → ARMED → ON → RECOVERING → OFF = 4 transitions.
//     If the FSM flapped (oscillation between ON and RECOVERING repeatedly),
//     we'd see 6+ transitions. Test-scaled timings (arm=1s, recover=2s)
//     in helpers_shed_test.go mean even one full cycle takes ~3s on the
//     state-machine side, so 12 oscillation cycles of 10s each cannot
//     produce more than 4 transitions if hysteresis is honored.
package integration

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	vegeta "github.com/tsenart/vegeta/lib"
)

func TestSC2_HysteresisNoFlapping(t *testing.T) {
	stack := newShedStack(t)
	gwURL := bootGateway(stack, nil)

	// Subscribe BEFORE driving load — events published before subscribe
	// arrives are lost (at-most-once semantics).
	getTransitions := startShedTransitionCounter(t, stack)

	// Oscillator goroutine: flip latency every 10s for 120s.
	stopOscillate := make(chan struct{})
	var oscillateWG sync.WaitGroup
	oscillateWG.Add(1)
	go func() {
		defer oscillateWG.Done()
		highLatency := true
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		// Set initial state to HIGH so the first FSM transition happens early.
		stack.Tier0Mock.SetLatency(600 * time.Millisecond)
		for {
			select {
			case <-stopOscillate:
				return
			case <-ticker.C:
				highLatency = !highLatency
				if highLatency {
					stack.Tier0Mock.SetLatency(600 * time.Millisecond)
				} else {
					stack.Tier0Mock.SetLatency(10 * time.Millisecond)
				}
			}
		}
	}()

	// Load driver goroutine: sustained 20 RPS for 120s. This populates
	// the latency ring buffer + inflight counter; without traffic the
	// FSM sees stale signals and never transitions.
	target := vegeta.Target{
		Method: "POST",
		URL:    gwURL + "/v1/chat/completions",
		Header: http.Header{
			"Authorization": {"Bearer " + stack.ApiKey("converseai")},
			"Content-Type":  {"application/json"},
		},
		Body: chatBody(),
	}
	var loadWG sync.WaitGroup
	loadWG.Add(1)
	go func() {
		defer loadWG.Done()
		attacker := vegeta.NewAttacker(vegeta.Timeout(10 * time.Second))
		for res := range attacker.Attack(vegeta.NewStaticTargeter(target),
			vegeta.Rate{Freq: 20, Per: time.Second}, 120*time.Second, "sc2-oscillation") {
			_ = res
		}
	}()

	// Wait for the full oscillation window + a small drain margin.
	time.Sleep(125 * time.Second)
	close(stopOscillate)
	oscillateWG.Wait()
	loadWG.Wait()

	transitions := getTransitions()
	t.Logf("SC-2 transitions observed over 120s oscillation: %d", transitions)

	// Hysteresis upper bound: a clean cycle is OFF→ARMED→ON→RECOVERING→OFF
	// = 4 transitions. Anything > 4 means the FSM flapped — either ARMED
	// returned to OFF early then re-armed (signal dropped during arm), or
	// RECOVERING returned to ON before recover_seconds elapsed.
	// Both are legal in CONTEXT.md D-C1 but indicate the test signal is
	// not stable enough; we expect at most one clean cycle in 120s if the
	// 10s flip cadence is below the arm/recover thresholds (it isn't — 10s
	// > arm=1s and recover=2s — so 0..4 transitions is the realistic range).
	if transitions > 4 {
		t.Errorf("SC-2 FAIL: %d transitions over 120s (want ≤4) — FSM flapped under hysteresis", transitions)
	} else {
		t.Logf("SC-2 PASS: hysteresis held — %d transitions ≤ 4 over oscillating load", transitions)
	}
}

// startShedTransitionCounter subscribes to gw:shed:events on stack.Rdb
// and counts every published message. Each shed.FSM.transition fires
// MakePublishTransition exactly once, so message count == transition count.
//
// Returns a func that closes the subscription, drains pending messages,
// and returns the final count. Caller MUST invoke this BEFORE driving
// load — Redis Pub/Sub is at-most-once and events published before
// SUBSCRIBE attaches land in the void.
//
// The 50ms sleep after Subscribe gives the Redis client time to
// establish the subscription before any test load fires.
func startShedTransitionCounter(t *testing.T, stack *ShedStack) func() int {
	t.Helper()
	ps := stack.Rdb.Subscribe(stack.Ctx, "gw:shed:events")
	var counter atomic.Int64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range ps.Channel() {
			counter.Add(1)
		}
	}()
	time.Sleep(50 * time.Millisecond)
	return func() int {
		_ = ps.Close()
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
		}
		return int(counter.Load())
	}
}
