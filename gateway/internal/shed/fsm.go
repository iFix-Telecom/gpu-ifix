// Package shed (fsm.go): per-upstream 4-state shedding FSM driven by
// the 2-of-3 saturation signal (CONTEXT.md D-A1 + D-C1). One FSM per
// tier-0 upstream (local-llm, local-stt, local-embed) — see D-C2.
//
// State semantics:
//   - StateOff:        signals below threshold; requests pass to tier-0 normally
//   - StateArmed:      signal saturated; waiting ArmSeconds of sustained
//                      saturation before committing to ON. Drops back to OFF
//                      immediately if signal clears (D-C1 hysteresis-in).
//   - StateOn:         signal sustained ArmSeconds; shed middleware overrides
//                      tier-0 → tier-1 for tenants whose inflight ≥ cap
//                      (D-B1, D-B4).
//   - StateRecovering: signal cleared; waiting RecoverSeconds clean before
//                      committing to OFF. Returns to ON immediately if signal
//                      re-saturates (D-C1 hysteresis-out: skip Armed).
//
// All hot-path reads (State, EnteredAt) are lockless atomic.Load. The
// transition CAS guards against the rare case of two ticks racing —
// only one wins, the loser re-evaluates on the next tick. onChange is
// fired AFTER the CAS succeeds, off the request path.
//
// Test note (Threat T-05-05): same-state transitions are filtered at
// the top of transition() so signal floods do not produce duplicate
// callbacks. The tick goroutine (Plan 05-05) runs at 1Hz per FSM, so
// physically only one transition can fire per second per upstream.
package shed

import (
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// State is the FSM state. Stored as int32 so it can ride atomic.Int32
// for the lockless hot-path read in dispatcher/middleware.
type State int32

const (
	// StateOff: shedding disabled; requests dispatch tier-0 normally.
	StateOff State = iota
	// StateArmed: saturation detected; waiting ArmSeconds sustained.
	StateArmed
	// StateOn: saturation sustained; shed tier-0 → tier-1 for capped tenants.
	StateOn
	// StateRecovering: saturation cleared; waiting RecoverSeconds clean.
	StateRecovering
)

// String returns the canonical lowercase name used in metrics labels,
// log fields, the Redis mirror payload, and the wire ShedEvent. Adding
// a new state is a wire-format change — keep this map in sync with the
// gatewayctl shed-state output (Plan 05-07).
func (s State) String() string {
	switch s {
	case StateOff:
		return "off"
	case StateArmed:
		return "armed"
	case StateOn:
		return "on"
	case StateRecovering:
		return "recovering"
	}
	return "unknown"
}

// Config holds per-upstream FSM tunables derived from the
// upstreams.circuit_config JSONB row (D-A4). ArmSeconds and
// RecoverSeconds are read once per Evaluate via atomic.Pointer[Config]
// so a hot-reload (D-C5) takes effect on the next tick without
// disturbing in-flight transitions.
type Config struct {
	// Upstream is the upstream name used in log fields, Prometheus
	// labels, and the Redis mirror key. Set by NewFSM from its first
	// argument; the field exists so UpdateConfig can keep it stable
	// across hot-reloads (D-C5 invariant: name never changes).
	Upstream string

	// ArmSeconds is the saturation-sustained window before OFF→ARMED→ON
	// commits. CONTEXT D-C1 default is 30s; minimum enforced by config
	// validation at the JSONB parser layer (D-A4 ShedConfigInvalid).
	ArmSeconds int64

	// RecoverSeconds is the clean-signal window before ON→RECOVERING→OFF
	// commits. CONTEXT D-C1 default is 60s.
	RecoverSeconds int64
}

// Signals carries one tick's observed values for the FSM 2-of-3 gate
// (D-A1). All booleans MUST be pre-computed by the tick goroutine
// (Plan 05-05) — Evaluate does NOT read thresholds itself, keeping the
// state machine free of policy.
//
// VramUnknown reduces the gate to 1-of-2 over (Inflight, P95) — when
// DCGM scraper is in fail-open mode (D-A3) we cannot trust the VRAM
// signal even if VramOverMax happens to be true.
type Signals struct {
	InflightOverMax bool
	P95OverMax      bool
	VramOverMax     bool
	VramUnknown     bool
}

// FSM is the per-upstream shed state machine. Construct via NewFSM,
// mutate by feeding ticks to Evaluate (1Hz from Plan 05-05). State()
// is the lockless hot-path read consumed by the dispatcher and
// middleware.
type FSM struct {
	upstream  string
	state     atomic.Int32
	enteredAt atomic.Int64 // unix-seconds when current state was entered
	cfg       atomic.Pointer[Config]
	onChange  func(from, to State, reason string)
	log       *slog.Logger
}

// NewFSM constructs an FSM initialised at StateOff with EnteredAt=now.
// The upstream name is folded into both the slog logger and the
// Config.Upstream field so UpdateConfig cannot accidentally rebind it.
// The Prometheus gauge is initialised to 0 (StateOff) so dashboards
// have a value to render before the first transition.
func NewFSM(upstream string, cfg Config, onChange func(from, to State, reason string), log *slog.Logger) *FSM {
	if log == nil {
		log = slog.Default()
	}
	cfg.Upstream = upstream
	f := &FSM{
		upstream: upstream,
		onChange: onChange,
		log:      log.With("module", "SHED_FSM", "upstream", upstream),
	}
	f.cfg.Store(&cfg)
	f.enteredAt.Store(time.Now().Unix())
	obs.GatewayShedState.WithLabelValues(upstream).Set(float64(StateOff))
	return f
}

// UpdateConfig atomically swaps the active tunables. The upstream name
// is preserved (D-C5 invariant) — a misconfigured Config with a
// different Upstream string is ignored at the name level.
func (f *FSM) UpdateConfig(cfg Config) {
	cfg.Upstream = f.upstream
	f.cfg.Store(&cfg)
}

// State returns the current FSM state. Lockless atomic.Load — safe to
// call from the request hot path.
func (f *FSM) State() State {
	return State(f.state.Load())
}

// EnteredAt returns the wall-clock time at which the FSM entered its
// current state. Used by gatewayctl shed-state for the dashboard
// "armed since…" / "recovering since…" lines (Plan 05-07).
func (f *FSM) EnteredAt() time.Time {
	return time.Unix(f.enteredAt.Load(), 0)
}

// Evaluate applies the D-C1 transition rules given one tick's signals.
// Called once per second by the tick goroutine (Plan 05-05). Reads cfg
// via atomic.Pointer so a concurrent UpdateConfig (D-C5 hot-reload)
// takes effect on the next tick.
//
// The 2-of-3 saturation score:
//   - InflightOverMax counts unconditionally
//   - P95OverMax counts unconditionally
//   - VramOverMax counts only when VramUnknown is false (D-A1 fail-open)
//
// saturated = score >= 2. When VramUnknown is true, the rule reduces
// to "InflightOverMax AND P95OverMax must both be true" — i.e., 2-of-2.
func (f *FSM) Evaluate(now time.Time, sig Signals) {
	score := 0
	if sig.InflightOverMax {
		score++
	}
	if sig.P95OverMax {
		score++
	}
	if sig.VramOverMax && !sig.VramUnknown {
		score++
	}
	saturated := score >= 2

	cfg := f.cfg.Load()
	if cfg == nil {
		return
	}
	current := State(f.state.Load())
	entered := f.enteredAt.Load()
	elapsed := now.Unix() - entered

	switch current {
	case StateOff:
		if saturated {
			f.transition(StateOff, StateArmed, now, "signal_rose")
		}
	case StateArmed:
		if !saturated {
			f.transition(StateArmed, StateOff, now, "signal_dropped_during_arm")
		} else if elapsed >= cfg.ArmSeconds {
			f.transition(StateArmed, StateOn, now, "arm_timeout_sustained")
		}
	case StateOn:
		if !saturated {
			f.transition(StateOn, StateRecovering, now, "signal_dropped")
		}
	case StateRecovering:
		if saturated {
			// Skip ARMED — already proved saturated, no hysteresis-in needed
			// the second time within the same incident (CONTEXT D-C1).
			f.transition(StateRecovering, StateOn, now, "signal_returned_during_recover")
		} else if elapsed >= cfg.RecoverSeconds {
			f.transition(StateRecovering, StateOff, now, "recover_timeout_clean")
		}
	}
}

// Transition exposes synthetic state changes for two callers:
//
//   - gatewayctl shed-force (operator override): bypasses the saturation
//     gate to drive the FSM to a specific state for a TTL window (D-C5).
//   - subscribe.go remote-event consumer (Plan 05-04): converges to the
//     state reported by another replica via Pub/Sub (D-C3).
//
// transition() filters same-state calls, so Transition(currentState, …)
// is a safe no-op.
func (f *FSM) Transition(newState State, reason string) {
	from := State(f.state.Load())
	f.transition(from, newState, time.Now(), reason)
}

// transition performs the lockless CAS commit. If the state changed
// under us (another tick raced and won), skip — the next tick will
// re-evaluate. obs + onChange are fired AFTER the CAS succeeds so
// failed transitions do not leak metrics or callbacks (T-05-05 mitigation).
func (f *FSM) transition(from, to State, now time.Time, reason string) {
	if from == to {
		return
	}
	if !f.state.CompareAndSwap(int32(from), int32(to)) {
		return
	}
	f.enteredAt.Store(now.Unix())
	obs.GatewayShedState.WithLabelValues(f.upstream).Set(float64(to))
	obs.GatewayShedTransitions.WithLabelValues(f.upstream, from.String(), to.String()).Inc()
	f.log.Info("shed FSM transition",
		"from", from.String(),
		"to", to.String(),
		"reason", reason,
		"at", now.Format(time.RFC3339),
	)
	if f.onChange != nil {
		f.onChange(from, to, reason)
	}
}
