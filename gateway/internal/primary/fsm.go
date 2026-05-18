// Package primary (fsm.go): Phase 6.6 primary-pod 5-state FSM, the
// in-process authoritative state machine consumed by the reconciler
// (Plan 06.6-06a) and mirrored to Redis/audit-log by the onChange hook.
//
// State semantics (5-state per RESEARCH.md §Schedule Reconciler Design
// lines 922–952):
//
//   - StateAsleep:       no pod is provisioned; reconciler waits for
//     schedule.ShouldBeProvisioned to flip true.
//   - StateProvisioning: leader is bidding+creating a Vast.ai pod for
//     the upcoming peak window. Cancellable mid-flight
//     if the schedule window closes (Draining).
//   - StateReady:        pod is healthy and serving as tier-0 LLM+STT+
//     embed substitute. Routing override is live via
//     loader.OverrideTier0.
//   - StateDraining:     schedule turned off OR pod unhealthy — drain
//     in-flight requests up to GraceRampDownS.
//   - StateDestroying:   drain complete; vast.DestroyInstance in flight.
//
// All hot-path reads (State, EnteredAt) are lockless atomic.Load. The
// Transition CAS guards against races; on CAS failure the caller
// receives ErrInvalidTransition and the next reconciler tick re-evaluates.
//
// onChange is fired AFTER the CAS succeeds, off the request path. The
// reconciler hooks onChange to (a) PUBLISH the state change to Redis
// for cross-replica visibility; (b) append to the lifecycle.events
// JSONB audit log; (c) toggle Prometheus gauges.
//
// stateChangeWriter is the optional audit-decoupling hook. When non-nil,
// the FSM emits a WriteStateChange call on every successful transition.
// The interface is intentionally untyped (event any) to keep the
// primary package free of dependencies on audit.Event — Plan 06.6-06+
// will wrap it.
//
// Wave 0 orthogonality: fsm.go is pure atomic.Int32 + CAS + time —
// independent of orchestration mechanism (supervisord vs DinD vs
// single-process). Decisions in Plan 04 (custom multi-stage image,
// b9191, supervisord) never touch this file.
package primary

import (
	"errors"
	"sync/atomic"
	"time"
)

// stateChangeWriter is the audit-decoupling hook the FSM uses to emit
// an append-only state-change row on every successful transition. nil
// is a valid value — the FSM simply skips the write. The reconciler
// (Plan 06.6-06+) supplies a real writer that fan-outs to Redis and the
// audit_log table.
//
// The event parameter is intentionally typed `any` so the primary
// package does not transitively depend on internal/audit — the
// reconciler is free to wrap a typed audit.Event of its choosing.
type stateChangeWriter interface {
	WriteStateChange(kind string, ev any) error
}

// State is the FSM state. Stored as int32 so it can ride atomic.Int32
// for the lockless hot-path read consumed by the reconciler dispatcher
// and gatewayctl.
type State int32

const (
	// StateAsleep: no pod provisioned. Initial state at boot.
	StateAsleep State = iota
	// StateProvisioning: leader bidding+creating Vast.ai pod;
	// cancellable mid-flight if schedule window closes.
	StateProvisioning
	// StateReady: pod healthy + serving tier-0 routing override live.
	StateReady
	// StateDraining: schedule off or pod unhealthy; drain inflight up
	// to GraceRampDownS.
	StateDraining
	// StateDestroying: drain complete; vast.DestroyInstance in flight.
	StateDestroying
)

// allStates is the canonical 5-state ordering. Adding a state here
// MUST be paired with a String() case AND with the test helper. Kept
// exported-private (lowercase) because Plan 06.6-06+ may need to
// enumerate states for gauge initialisation.
var allStates = []State{ //nolint:unused // referenced by Plan 06.6-06+ gauge init
	StateAsleep,
	StateProvisioning,
	StateReady,
	StateDraining,
	StateDestroying,
}

// String returns the canonical lowercase name used in metric labels,
// log fields, and audit rows. Adding a new state is a wire-format
// change — keep this map, allStates, and any future ParseState in sync.
func (s State) String() string {
	switch s {
	case StateAsleep:
		return "asleep"
	case StateProvisioning:
		return "provisioning"
	case StateReady:
		return "ready"
	case StateDraining:
		return "draining"
	case StateDestroying:
		return "destroying"
	}
	return "unknown"
}

// ErrInvalidTransition is returned by Transition when the caller's
// expected `from` state does NOT match the current FSM state (CAS
// failure). The caller is expected to either:
//
//   - re-read the state and retry (rare leader-handoff race), or
//   - treat the call as a silent noop and let the next reconciler tick
//     re-evaluate.
var ErrInvalidTransition = errors.New("primary fsm: invalid transition")

// FSM is the primary-pod state machine. Construct via NewFSM and drive
// transitions from the reconciler (Plan 06.6-06a). State() is the
// lockless hot-path read.
//
// The struct deliberately carries NO Config pointer: schedule tunables
// flow through ScheduleRule (passed to the reconciler), and the FSM
// itself owns no env-var surface.
type FSM struct {
	state     atomic.Int32
	enteredAt atomic.Int64 // unix-nanoseconds at most-recent transition
	onChange  func(from, to State, at time.Time, reason string)
	writer    stateChangeWriter
}

// NewFSM constructs an FSM initialised at StateAsleep with EnteredAt =
// now.
//
// Both writer and onChange may be nil during early-boot wiring (the
// reconciler injects the real callbacks after constructor returns).
// When nil, transitions still update the atomic state — the callbacks
// are purely the publish/audit hooks for the reconciler.
func NewFSM(writer stateChangeWriter, onChange func(from, to State, at time.Time, reason string)) *FSM {
	f := &FSM{
		onChange: onChange,
		writer:   writer,
	}
	f.state.Store(int32(StateAsleep))
	f.enteredAt.Store(time.Now().UnixNano())
	return f
}

// State returns the current FSM state. Lockless atomic.Load — safe to
// call from any goroutine.
func (f *FSM) State() State {
	return State(f.state.Load())
}

// EnteredAt returns the wall-clock time at which the FSM entered its
// current state. Used by the reconciler to compute "elapsed since
// Draining" for the drain-grace timer.
func (f *FSM) EnteredAt() time.Time {
	return time.Unix(0, f.enteredAt.Load())
}

// Transition is the public CAS entry point. The caller MUST pass the
// expected current state as `from`. If the state has changed under the
// caller, the CAS fails and ErrInvalidTransition is returned — the
// next reconciler tick will re-evaluate from the new state.
//
// Same-state calls (from == to) are filtered at the top as a noop:
// no error, no callback fire, no writer call. This matches the
// emerg/fsm.go contract so callers can call Transition unconditionally
// during recovery without spurious events.
func (f *FSM) Transition(from, to State, now time.Time, reason string) error {
	if from == to {
		return nil
	}
	if !f.state.CompareAndSwap(int32(from), int32(to)) {
		return ErrInvalidTransition
	}
	f.commitTransitionSideEffects(from, to, now, reason)
	return nil
}

// SetState forces the FSM to a target state regardless of the current
// state. Used by leader-recovery resumption flows that rebuild the FSM
// from external state (the Redis mirror or the lifecycle.events JSONB
// log).
//
// Same-state calls are filtered (no callback fire) so SetState can be
// called unconditionally during resume.
func (f *FSM) SetState(to State, now time.Time, reason string) {
	from := State(f.state.Load())
	if from == to {
		return
	}
	// Loop the CAS until it commits — SetState must converge regardless
	// of in-flight tick-driven transitions, so re-read on CAS miss.
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

// commitTransitionSideEffects runs the post-CAS side effects shared by
// Transition (caller-driven) and SetState (forced). Split out so the
// CAS-loop in SetState does not duplicate the audit/onChange code.
func (f *FSM) commitTransitionSideEffects(from, to State, now time.Time, reason string) {
	f.enteredAt.Store(now.UnixNano())
	if f.writer != nil {
		// Best-effort audit: any writer error is swallowed here so the
		// FSM transition path never stalls. The reconciler is expected
		// to wrap a writer that does its own retry / fan-out.
		_ = f.writer.WriteStateChange("fsm_transition", map[string]any{
			"from":   from.String(),
			"to":     to.String(),
			"at":     now,
			"reason": reason,
		})
	}
	if f.onChange != nil {
		f.onChange(from, to, now, reason)
	}
}
