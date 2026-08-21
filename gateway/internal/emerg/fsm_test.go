// Package emerg (fsm_test.go): deterministic unit tests for the
// Phase 6 7-state emergency FSM (CONTEXT.md domain item 1, BLOCKER 3
// revision: OFF_HOURS + MAINTENANCE deferred).
//
// All tests drive Transition with an explicit time.Time so transitions
// are independent of wall-clock skew (mirrors Phase 5 shed/fsm_test.go
// approach). Race-detector friendly: every concurrent test launches its
// goroutines before mutating the FSM.
package emerg

import (
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/audit"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// allStatesForTest is the canonical 7-state ordering for assertions.
// Kept in a helper so tests do not duplicate the state list (and so
// adding a state in production must touch the helper, surfacing test
// breakage immediately).
var allStatesForTest = []State{
	StateHealthy,
	StateDegraded,
	StateFailedOver,
	StateEmergencyProvisioning,
	StateEmergencyActive,
	StateRecovering,
	StateCooldown,
}

// resetEmergGauge zeroes every label of GatewayEmergencyState so a
// later assertion of "1 on the new state, 0 on others" is deterministic
// even if a previous test left labels set. Cheap — 7 Set(0) calls.
func resetEmergGauge(t *testing.T) {
	t.Helper()
	for _, s := range allStatesForTest {
		obs.GatewayEmergencyState.WithLabelValues(s.String()).Set(0)
	}
}

func TestFSMStateString(t *testing.T) {
	cases := map[State]string{
		StateHealthy:               "healthy",
		StateDegraded:              "degraded",
		StateFailedOver:            "failed_over",
		StateEmergencyProvisioning: "emergency_provisioning",
		StateEmergencyActive:       "emergency_active",
		StateRecovering:            "recovering",
		StateCooldown:              "cooldown",
		State(99):                  "unknown",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Fatalf("State(%d).String() = %q, want %q", s, got, want)
		}
	}
}

func TestFSMParseState(t *testing.T) {
	// Round-trip: every valid state string must parse back to the same
	// State value. Used by leader-recovery resumeFSMFromEvents in Plan 07.
	for _, s := range allStatesForTest {
		got, err := ParseState(s.String())
		if err != nil {
			t.Fatalf("ParseState(%q) returned err=%v, want nil", s.String(), err)
		}
		if got != s {
			t.Fatalf("ParseState(%q) = %d, want %d", s.String(), got, s)
		}
	}
	// Invalid strings must error (so resumeFSMFromEvents can detect a
	// corrupted JSONB events payload and abort the resume).
	if _, err := ParseState("nonexistent_state"); err == nil {
		t.Fatalf("ParseState(\"nonexistent_state\") returned nil err, want non-nil")
	}
	if _, err := ParseState(""); err == nil {
		t.Fatalf("ParseState(\"\") returned nil err, want non-nil")
	}
}

func TestFSMInitialStateIsHealthy(t *testing.T) {
	resetEmergGauge(t)
	f := NewFSM(slog.Default(), nil)
	if f.State() != StateHealthy {
		t.Fatalf("initial state = %s, want healthy", f.State())
	}
	if f.EnteredAt().IsZero() {
		t.Fatalf("EnteredAt() should be non-zero at construction")
	}
}

func TestFSMTransitions(t *testing.T) {
	// Walk the canonical happy-path sequence:
	// Healthy → Degraded → FailedOver → EmergencyProvisioning →
	// EmergencyActive → Recovering → Cooldown.
	resetEmergGauge(t)
	f := NewFSM(slog.Default(), nil)

	steps := []struct {
		from, to State
		reason   string
	}{
		{StateHealthy, StateDegraded, "primary_breaker_open"},
		{StateDegraded, StateFailedOver, "tier1_serving"},
		{StateFailedOver, StateEmergencyProvisioning, "trigger_sustained"},
		{StateEmergencyProvisioning, StateEmergencyActive, "pod_health_passed"},
		{StateEmergencyActive, StateRecovering, "primary_recovered"},
		{StateRecovering, StateCooldown, "idle_grace_elapsed"},
	}

	now := time.Unix(1000, 0)
	for _, step := range steps {
		now = now.Add(1 * time.Second)
		f.Transition(step.from, step.to, now, step.reason)
		if f.State() != step.to {
			t.Fatalf("Transition(%s→%s): got %s", step.from, step.to, f.State())
		}
		if f.EnteredAt().Unix() != now.Unix() {
			t.Fatalf("Transition(%s→%s): EnteredAt=%d, want %d",
				step.from, step.to, f.EnteredAt().Unix(), now.Unix())
		}
	}
}

func TestFSMObsGaugeReset(t *testing.T) {
	// After a transition into StateDegraded, the gauge MUST be 1 on
	// "degraded" and 0 on every other state label.
	resetEmergGauge(t)
	f := NewFSM(slog.Default(), nil)
	f.Transition(StateHealthy, StateDegraded, time.Unix(1000, 0), "test")

	for _, s := range allStatesForTest {
		got := testutil.ToFloat64(obs.GatewayEmergencyState.WithLabelValues(s.String()))
		want := 0.0
		if s == StateDegraded {
			want = 1.0
		}
		if got != want {
			t.Fatalf("gauge[state=%s] = %v, want %v", s.String(), got, want)
		}
	}
}

func TestFSMOnChangeCallback(t *testing.T) {
	resetEmergGauge(t)

	type capture struct {
		from, to State
		reason   string
	}
	var (
		mu       sync.Mutex
		captured []capture
	)

	onChange := func(from, to State, reason string) {
		mu.Lock()
		defer mu.Unlock()
		captured = append(captured, capture{from, to, reason})
	}

	f := NewFSM(slog.Default(), onChange)
	f.Transition(StateHealthy, StateDegraded, time.Unix(1000, 0), "test_reason")
	f.Transition(StateDegraded, StateFailedOver, time.Unix(1001, 0), "another_reason")

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 2 {
		t.Fatalf("captured %d transitions, want 2: %+v", len(captured), captured)
	}
	if captured[0].from != StateHealthy || captured[0].to != StateDegraded || captured[0].reason != "test_reason" {
		t.Fatalf("captured[0] = %+v", captured[0])
	}
	if captured[1].from != StateDegraded || captured[1].to != StateFailedOver || captured[1].reason != "another_reason" {
		t.Fatalf("captured[1] = %+v", captured[1])
	}
}

func TestFSMInvalidTransition(t *testing.T) {
	// CAS guard: if the caller passes a from-state that does NOT match
	// the current state, transition is silently ignored (D-C1 semantics).
	resetEmergGauge(t)
	var callbackCount atomic.Int32
	onChange := func(from, to State, reason string) {
		callbackCount.Add(1)
	}
	f := NewFSM(slog.Default(), onChange)

	// Current state is StateHealthy (initial). Try to transition from
	// EmergencyActive — the CAS will fail.
	f.Transition(StateEmergencyActive, StateDegraded, time.Unix(1000, 0), "wrong_from")
	if f.State() != StateHealthy {
		t.Fatalf("state should remain Healthy after invalid transition; got %s", f.State())
	}
	if callbackCount.Load() != 0 {
		t.Fatalf("onChange should not fire on invalid transition; got %d calls", callbackCount.Load())
	}

	// Same-state transition (Healthy→Healthy) is also a noop.
	f.Transition(StateHealthy, StateHealthy, time.Unix(1001, 0), "noop")
	if callbackCount.Load() != 0 {
		t.Fatalf("onChange should not fire on same-state transition; got %d calls", callbackCount.Load())
	}
}

func TestFSMTransitionCAS(t *testing.T) {
	// Two goroutines race to do Healthy→Degraded. Exactly one CAS wins
	// → onChange fires exactly once. Final state = Degraded.
	resetEmergGauge(t)
	var callbackCount atomic.Int32
	onChange := func(from, to State, reason string) {
		callbackCount.Add(1)
	}
	f := NewFSM(slog.Default(), onChange)

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	done.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer done.Done()
			start.Wait()
			f.Transition(StateHealthy, StateDegraded, time.Unix(1000, 0), "race")
		}()
	}
	start.Done()
	done.Wait()

	if f.State() != StateDegraded {
		t.Fatalf("after race: got %s, want degraded", f.State())
	}
	if got := callbackCount.Load(); got != 1 {
		t.Fatalf("onChange called %d times, want exactly 1 (CAS guarantees idempotency)", got)
	}
}

func TestFSMSetState(t *testing.T) {
	// SetState forces the FSM to a target state regardless of current
	// state. Used by Plan 07 leader-recovery resumeFSMFromEvents.
	resetEmergGauge(t)
	var captured []State
	var mu sync.Mutex
	onChange := func(from, to State, reason string) {
		mu.Lock()
		defer mu.Unlock()
		captured = append(captured, to)
	}
	f := NewFSM(slog.Default(), onChange)

	now := time.Unix(2000, 0)
	f.SetState(StateEmergencyActive, now, "leader_recovery_resume")
	if f.State() != StateEmergencyActive {
		t.Fatalf("after SetState: got %s, want emergency_active", f.State())
	}
	if f.EnteredAt().Unix() != now.Unix() {
		t.Fatalf("after SetState: EnteredAt=%d, want %d", f.EnteredAt().Unix(), now.Unix())
	}

	// Calling SetState with same state is a noop (no callback).
	f.SetState(StateEmergencyActive, time.Unix(2010, 0), "no_change")

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 1 {
		t.Fatalf("captured %d transitions, want 1: %v", len(captured), captured)
	}
}

func TestFSMConcurrentReadDuringTransition(t *testing.T) {
	// Race-detector smoke: hammer State() in one goroutine while
	// another runs Transition. Lockless atomic.Load must not race with
	// CompareAndSwap.
	resetEmergGauge(t)
	f := NewFSM(slog.Default(), nil)

	var done sync.WaitGroup
	done.Add(1)

	stop := make(chan struct{})
	go func() {
		defer done.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = f.State()
				_ = f.EnteredAt()
			}
		}
	}()

	now := time.Unix(1000, 0)
	for i := 0; i < 100; i++ {
		f.Transition(StateHealthy, StateDegraded, now.Add(time.Duration(i)*time.Second), "race")
		f.SetState(StateHealthy, now.Add(time.Duration(i)*time.Second), "reset")
	}

	close(stop)
	done.Wait()
}

// fakeStateChangeWriter is a recording stub for the FSM's audit
// dependency (OBS-07). Mutex-guarded because the FSM is exercised
// concurrently in other tests and the -race build runs all of them.
type fakeStateChangeWriter struct {
	mu    sync.Mutex
	calls []fakeAuditCall
}

type fakeAuditCall struct {
	kind  string
	event audit.Event
}

func (f *fakeStateChangeWriter) WriteStateChange(kind string, ev audit.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeAuditCall{kind: kind, event: ev})
}

func (f *fakeStateChangeWriter) snapshot() []fakeAuditCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeAuditCall(nil), f.calls...)
}

func TestFSMTransitionEmitsAuditRow(t *testing.T) {
	// OBS-07: every FSM transition MUST write exactly one
	// WriteStateChange("fsm_transition", ...) audit row carrying the
	// from-state, to-state, and the transition reason.
	resetEmergGauge(t)
	fake := &fakeStateChangeWriter{}
	f := NewFSM(slog.Default(), nil)
	f.SetAuditWriter(fake)

	now := time.Unix(3000, 0)
	f.Transition(StateHealthy, StateDegraded, now, "breaker_flap")

	calls := fake.snapshot()
	if len(calls) != 1 {
		t.Fatalf("want exactly 1 audit call, got %d: %+v", len(calls), calls)
	}
	c := calls[0]
	if c.kind != "emerg_state_change" {
		t.Fatalf("audit kind = %q, want emerg_state_change", c.kind)
	}
	if c.event.Method != "healthy->degraded" {
		t.Fatalf("audit event Method = %q, want healthy->degraded", c.event.Method)
	}
	if c.event.Upstream != "degraded" {
		t.Fatalf("audit event Upstream = %q, want degraded", c.event.Upstream)
	}
	// CR-03: the transition reason rides the dedicated Reason field
	// (audit_log.reason column), NOT ErrorCode — ErrorCode is reserved
	// for genuine request error codes. Formatted "from→to (reason)" for
	// parity with primary_state_change rows (dashboard ops audit
	// 2026-08-21).
	if c.event.Reason != "healthy→degraded (breaker_flap)" {
		t.Fatalf("audit event Reason = %q, want healthy→degraded (breaker_flap)", c.event.Reason)
	}
	if !strings.Contains(c.event.Reason, "→") {
		t.Fatalf("audit event Reason = %q, want from→to arrow format", c.event.Reason)
	}
	// data_class is a NOT NULL enum (migration 0003); an empty value
	// poisons the whole CopyFrom batch. Must be "normal".
	if c.event.DataClass != "normal" {
		t.Fatalf("audit event DataClass = %q, want normal", c.event.DataClass)
	}
	if c.event.ErrorCode != "" {
		t.Fatalf("audit event ErrorCode = %q, want empty (reason must not overload ErrorCode)", c.event.ErrorCode)
	}
	if !c.event.TS.Equal(now) {
		t.Fatalf("audit event TS = %v, want %v", c.event.TS, now)
	}

	// A second transition writes a second row — one per transition.
	f.Transition(StateDegraded, StateFailedOver, now.Add(time.Second), "breaker_open")
	if got := len(fake.snapshot()); got != 2 {
		t.Fatalf("after second transition: want 2 audit calls, got %d", got)
	}

	// An invalid (CAS-failing) transition writes NO row — the audit
	// happens only after the CAS commits.
	f.Transition(StateHealthy, StateCooldown, now.Add(2*time.Second), "wrong_from")
	if got := len(fake.snapshot()); got != 2 {
		t.Fatalf("invalid transition must not audit: want 2 calls, got %d", got)
	}
}

func TestFSMNilAuditWriterDoesNotPanic(t *testing.T) {
	// A nil audit writer (the default — tests + early-boot wiring leave
	// it unset) must NOT panic the FSM transition path.
	resetEmergGauge(t)
	f := NewFSM(slog.Default(), nil)
	// auditWriter is nil; never call SetAuditWriter.
	f.Transition(StateHealthy, StateDegraded, time.Unix(4000, 0), "nil_writer_smoke")
	if f.State() != StateDegraded {
		t.Fatalf("transition with nil audit writer: state = %s, want degraded", f.State())
	}

	// SetAuditWriter(nil) is also a no-op and leaves the FSM nil-safe.
	f.SetAuditWriter(nil)
	f.Transition(StateDegraded, StateFailedOver, time.Unix(4001, 0), "explicit_nil")
	if f.State() != StateFailedOver {
		t.Fatalf("transition after SetAuditWriter(nil): state = %s, want failed_over", f.State())
	}
}
