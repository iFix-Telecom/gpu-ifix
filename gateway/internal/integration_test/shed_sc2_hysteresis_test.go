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
// Assertion (quick 260930-vkt):
//   - Total transitions do local-llm ≤ 4×6 ciclos + 2 = 26 em 120s.
//     Fases de 10s > arm=1s/recover=2s → a máquina correta faz 1 ciclo
//     completo por oscilação: OFF → ARMED → ON (fase alta) e
//     ON → RECOVERING → OFF (fase baixa) = 4 transições por ciclo de 20s.
//     A asserção anterior (≤4 no total) estava com a aritmética errada.
//   - Acima do limite = flapping real (On↔Recovering dentro da fase alta);
//     o teste falha com o histograma por reason — NÃO afrouxar.
package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	vegeta "github.com/tsenart/vegeta/lib"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

func TestSC2_HysteresisNoFlapping(t *testing.T) {
	if os.Getenv("CI") == "true" && os.Getenv("CI_ALLOW_TIGHT_SHED_TIMING") != "1" {
		t.Skip("skipping in CI — testcontainers + tight timing flaky on free-tier runners. Run locally or set CI_ALLOW_TIGHT_SHED_TIMING=1.")
	}
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

	stats := getTransitions()
	transitions := stats.total
	t.Logf("SC-2 transitions (local-llm) over 120s oscillation: %d; by reason: %v; by edge: %v",
		transitions, stats.byReason, stats.byEdge)

	// Quick 260930-vkt — limite corrigido. A asserção antiga (≤4 no total)
	// partia de uma aritmética errada: fases de 10s são MAIORES que
	// arm=1s/recover=2s (agora efetivamente aplicados via ApplyConfigs), então
	// a máquina correta faz 1 ciclo completo por oscilação:
	//   HIGH: Off→Armed→On (2)   LOW: On→Recovering→Off (2)  = 4 por ciclo.
	// 120s / 20s = 6 ciclos → ≤ 24, +2 de folga para as bordas da janela.
	//
	// Acima disso é FLAPPING REAL da FSM (On↔Recovering dentro da mesma fase
	// alta — "signal_dropped" seguido de "signal_returned_during_recover"),
	// e o teste deve FALHAR explicitamente: não afrouxar para esconder. A
	// correção, se necessária, é no fsm.go/tick.go (decisão do dono), não
	// aqui. Ver SUMMARY 260930-vkt §Achados.
	const cycles = 6
	maxTransitions := 4*cycles + 2
	bounces := stats.byReason["signal_returned_during_recover"]
	if transitions > maxTransitions {
		t.Errorf("SC-2 FAIL: %d transitions over 120s (want ≤%d = 4×%d ciclos + 2) — "+
			"FSM flapped; signal_returned_during_recover=%d signal_dropped=%d; by edge=%v "+
			"(achado 260930-vkt: On→Recovering sem espera + Recovering não shedda)",
			transitions, maxTransitions, cycles, bounces, stats.byReason["signal_dropped"], stats.byEdge)
	} else {
		t.Logf("SC-2 PASS: hysteresis held — %d transitions ≤ %d over oscillating load", transitions, maxTransitions)
	}
}

// shedTransitionStats agrega os eventos gw:shed:events do upstream
// local-llm: total, por reason e por aresta (from→to não vem no evento;
// usamos "→state").
type shedTransitionStats struct {
	total    int
	byReason map[string]int
	byEdge   map[string]int
}

// startShedTransitionCounter subscribes to gw:shed:events on stack.Rdb
// and counts every published message for local-llm. Each
// shed.FSM.transition fires MakePublishTransition exactly once, so
// message count == transition count.
//
// Quick 260930-vkt: decodifica o redisx.ShedEvent para classificar por
// reason (diagnóstico de flapping vs artefato) e filtra upstream=local-llm
// (o tier-0 mock é compartilhado por stt/embed; eventos de outras FSMs não
// devem contar para a histerese do chat).
//
// Returns a func that closes the subscription, drains pending messages,
// and returns the final stats. Caller MUST invoke this BEFORE driving
// load — Redis Pub/Sub is at-most-once and events published before
// SUBSCRIBE attaches land in the void.
func startShedTransitionCounter(t *testing.T, stack *ShedStack) func() shedTransitionStats {
	t.Helper()
	ps := stack.Rdb.Subscribe(stack.Ctx, "gw:shed:events")
	var mu sync.Mutex
	stats := shedTransitionStats{byReason: map[string]int{}, byEdge: map[string]int{}}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for msg := range ps.Channel() {
			var ev redisx.ShedEvent
			if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
				mu.Lock()
				stats.total++
				stats.byReason["undecodable"]++
				mu.Unlock()
				continue
			}
			if ev.Upstream != "local-llm" {
				continue
			}
			mu.Lock()
			stats.total++
			stats.byReason[ev.Reason]++
			stats.byEdge["→"+ev.State]++
			mu.Unlock()
		}
	}()
	time.Sleep(50 * time.Millisecond)
	return func() shedTransitionStats {
		_ = ps.Close()
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
		}
		mu.Lock()
		defer mu.Unlock()
		out := shedTransitionStats{total: stats.total, byReason: map[string]int{}, byEdge: map[string]int{}}
		for k, v := range stats.byReason {
			out.byReason[k] = v
		}
		for k, v := range stats.byEdge {
			out.byEdge[k] = v
		}
		return out
	}
}
