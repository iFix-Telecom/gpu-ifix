// Package emerg (fsm.go): Phase 6 emergency-pod 7-state FSM, the
// in-process authoritative state machine consumed by the reconciler
// (Plan 04+) and mirrored to Redis via redisx/emerg.go for cross-replica
// visibility (CONTEXT.md D-B1).
//
// State semantics (7-state per BLOCKER 3 revision; OFF_HOURS +
// MAINTENANCE deferred — see CONTEXT.md `<deferred>`):
//
//   - StateHealthy:               primary `local-llm` upstream serving normally;
//     no breaker degradation, no shedding tier-1 routing.
//   - StateDegraded:              primary breaker has flapped or shedding FSM
//     entered ARMED — degradation observed but tier-0 still serves.
//   - StateFailedOver:            primary breaker.OPEN; dispatcher routing to
//     tier-1 (OpenRouter) per Phase 3 fallback. Trigger
//     timer (D-C1) is armed; if sustained for
//     PROVISION_TRIGGER_FAILED_OVER_SECONDS, the FSM
//     advances to EMERGENCY_PROVISIONING.
//   - StateEmergencyProvisioning: leader is bidding+creating a Vast.ai pod.
//     Cancellable via context.WithCancel (D-C3) if
//     primary recovers mid-flight.
//   - StateEmergencyActive:       Vast.ai pod is healthy + serving as tier-0
//     LLM substitute. Routing override is live.
//   - StateRecovering:            primary recovered, cutback in progress —
//     PROVISION_HEALTHY_DURATION_SECONDS grace then
//     destroy emergency pod.
//   - StateCooldown:              emergency pod destroyed; FSM holds for
//     PROVISION_IDLE_GRACE_SECONDS to suppress
//     re-trigger oscillation. Auto-returns to HEALTHY.
//
// All hot-path reads (State, EnteredAt) are lockless atomic.Load. The
// Transition CAS guards against the rare case of two goroutines (or
// reconciler tick + leader-recovery resume) racing — only one wins,
// the loser silently skips and the next tick re-evaluates.
//
// onChange is fired AFTER the CAS succeeds, off the request path. The
// reconciler hooks onChange to (a) PUBLISH gw:emerg:events; (b) HSET
// gw:emerg:state mirror; (c) append to lifecycle.events JSONB; (d)
// emit Sentry breadcrumbs already emitted in transition().
package emerg

import (
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// State is the FSM state. Stored as int32 so it can ride atomic.Int32
// for the lockless hot-path read consumed by the dispatcher and
// gatewayctl.
type State int32

const (
	// StateHealthy: primary upstream serving normally. Initial state at
	// boot.
	StateHealthy State = iota
	// StateDegraded: primary breaker flap or shedding ARMED — observed
	// degradation but tier-0 still serves.
	StateDegraded
	// StateFailedOver: primary breaker.OPEN; tier-1 fallback active;
	// trigger timer armed.
	StateFailedOver
	// StateEmergencyProvisioning: leader bidding+creating Vast.ai pod;
	// cancellable mid-flight if primary recovers (D-C3).
	StateEmergencyProvisioning
	// StateEmergencyActive: emergency pod healthy and serving as tier-0
	// LLM substitute.
	StateEmergencyActive
	// StateRecovering: primary recovered; cutback grace window before
	// destroy.
	StateRecovering
	// StateCooldown: emergency pod destroyed; oscillation-suppression
	// hold; auto-returns to HEALTHY.
	StateCooldown
)

// allStates is the canonical 7-state ordering used by transition() to
// reset every gauge label except the new state to 0. Adding a state
// here MUST be paired with a String() case AND a ParseState case AND
// the test helper allStatesForTest in fsm_test.go.
var allStates = []State{
	StateHealthy,
	StateDegraded,
	StateFailedOver,
	StateEmergencyProvisioning,
	StateEmergencyActive,
	StateRecovering,
	StateCooldown,
}

// String returns the canonical lowercase name used in metric labels,
// log fields, the Redis mirror payload, the Pub/Sub EmergEvent, and
// gatewayctl emerg-state output. Adding a new state is a wire-format
// change — keep this map, allStates, and ParseState in sync.
func (s State) String() string {
	switch s {
	case StateHealthy:
		return "healthy"
	case StateDegraded:
		return "degraded"
	case StateFailedOver:
		return "failed_over"
	case StateEmergencyProvisioning:
		return "emergency_provisioning"
	case StateEmergencyActive:
		return "emergency_active"
	case StateRecovering:
		return "recovering"
	case StateCooldown:
		return "cooldown"
	}
	return "unknown"
}

// ParseState converts the canonical lowercase state name back to its
// State value. Used by Plan 07 leader-recovery resumeFSMFromEvents to
// rebuild the in-process FSM from the lifecycle.events JSONB log.
//
// Returns an error on unknown strings so a corrupted JSONB payload
// (or wire-format drift between leaders running different binaries)
// surfaces at recovery time rather than silently snapping to Healthy.
func ParseState(s string) (State, error) {
	switch s {
	case "healthy":
		return StateHealthy, nil
	case "degraded":
		return StateDegraded, nil
	case "failed_over":
		return StateFailedOver, nil
	case "emergency_provisioning":
		return StateEmergencyProvisioning, nil
	case "emergency_active":
		return StateEmergencyActive, nil
	case "recovering":
		return StateRecovering, nil
	case "cooldown":
		return StateCooldown, nil
	}
	return State(-1), fmt.Errorf("emerg: unknown state %q", s)
}

// FSM is the emergency-pod state machine. Construct via NewFSM and
// drive transitions from the reconciler (Plan 04+). State() is the
// lockless hot-path read consumed by the dispatcher (`emerg.IsActive()`
// in Plan 06) and gatewayctl.
//
// Phase 6 does NOT carry a Config struct (cfg atomic.Pointer[Config]
// like the shed FSM) because emergency tunables come exclusively from
// env vars at boot — no hot-reload surface. If a future plan adds
// per-tenant emergency config, introduce cfg as the shed FSM does.
type FSM struct {
	state     atomic.Int32
	enteredAt atomic.Int64 // unix-seconds at most-recent transition
	onChange  func(from, to State, reason string)
	log       *slog.Logger
}

// NewFSM constructs an FSM initialised at StateHealthy with EnteredAt
// = now. The Prometheus gauge is initialised to 1 on "healthy" and 0
// on every other state label so dashboards have a value to render
// before the first transition.
//
// onChange may be nil during early-boot wiring (the reconciler injects
// the real callback after constructor returns). When nil, transitions
// still log + bump gauges + emit Sentry breadcrumbs — the callback is
// purely the publish/HSET/lifecycle-audit hook for the reconciler.
func NewFSM(log *slog.Logger, onChange func(from, to State, reason string)) *FSM {
	if log == nil {
		log = slog.Default()
	}
	f := &FSM{
		onChange: onChange,
		log:      log.With("module", "EMERG_FSM"),
	}
	f.state.Store(int32(StateHealthy))
	f.enteredAt.Store(time.Now().Unix())
	// Initialise gauge: 1 on healthy, 0 on the rest.
	for _, s := range allStates {
		v := 0.0
		if s == StateHealthy {
			v = 1.0
		}
		obs.GatewayEmergencyState.WithLabelValues(s.String()).Set(v)
	}
	return f
}

// State returns the current FSM state. Lockless atomic.Load — safe to
// call from the request hot path (dispatcher reads on every request).
func (f *FSM) State() State {
	return State(f.state.Load())
}

// EnteredAt returns the wall-clock time at which the FSM entered its
// current state. Used by gatewayctl emerg-state and by the reconciler
// to compute "elapsed since EMERGENCY_ACTIVE" for cutback timing.
func (f *FSM) EnteredAt() time.Time {
	return time.Unix(f.enteredAt.Load(), 0)
}

// Transition is the public CAS entry point. The caller MUST pass the
// expected current state as `from` — if the state has changed under the
// caller (rare leader-recovery race or reconciler-tick race), the CAS
// fails and the call is a silent noop. The next tick re-evaluates.
//
// Transition is idempotent under same-state calls (from==to is filtered
// at the top) so passing Transition(currentState, currentState, …) is
// safe.
func (f *FSM) Transition(from, to State, now time.Time, reason string) {
	f.transition(from, to, now, reason)
}

// SetState forces the FSM to a target state regardless of the current
// state. Used by Plan 07 leader-recovery resumeFSMFromEvents to rebuild
// the FSM after a leader-handoff with a known target state extracted
// from the lifecycle.events JSONB log.
//
// Same-state calls are filtered (no callback fire, no log emission) —
// matches the Transition() contract so callers can call SetState
// unconditionally during resume without spurious events.
func (f *FSM) SetState(to State, now time.Time, reason string) {
	from := State(f.state.Load())
	if from == to {
		return
	}
	// Loop the CAS until it commits — SetState semantically must
	// converge regardless of in-flight tick-driven transitions, so
	// re-read from on CAS miss.
	for {
		if f.state.CompareAndSwap(int32(from), int32(to)) {
			break
		}
		from = State(f.state.Load())
		if from == to {
			return
		}
	}
	f.commitTransitionSideEffects(from, to, now, reason)
}

// transition performs the lockless CAS commit. If the state changed
// under us (another tick raced and won, or caller passed wrong from),
// skip — the next tick will re-evaluate. obs + onChange are fired
// AFTER the CAS succeeds so failed transitions do not leak metrics or
// callbacks.
func (f *FSM) transition(from, to State, now time.Time, reason string) {
	if from == to {
		return
	}
	if !f.state.CompareAndSwap(int32(from), int32(to)) {
		return
	}
	f.commitTransitionSideEffects(from, to, now, reason)
}

// commitTransitionSideEffects runs the post-CAS side effects shared by
// Transition (caller-driven) and SetState (forced). Split out so the
// CAS-loop in SetState does not duplicate the gauge/log/breadcrumb/
// onChange code.
func (f *FSM) commitTransitionSideEffects(from, to State, now time.Time, reason string) {
	f.enteredAt.Store(now.Unix())
	// Set gauge to 1 on the new state, 0 on every other state. Cheap —
	// 7 Set() calls per transition, and transitions are rare (sub-Hz).
	for _, s := range allStates {
		v := 0.0
		if s == to {
			v = 1.0
		}
		obs.GatewayEmergencyState.WithLabelValues(s.String()).Set(v)
	}
	f.log.Info("emerg FSM transition",
		"from", from.String(),
		"to", to.String(),
		"reason", reason,
		"at", now.Format(time.RFC3339),
	)
	sentry.AddBreadcrumb(&sentry.Breadcrumb{
		Category:  "emerg",
		Message:   fmt.Sprintf("state %s→%s", from.String(), to.String()),
		Level:     sentry.LevelInfo,
		Timestamp: now,
		Data:      map[string]interface{}{"reason": reason},
	})
	if f.onChange != nil {
		f.onChange(from, to, reason)
	}
}
