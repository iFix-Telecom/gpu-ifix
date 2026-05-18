// Package primary (fsm_test.go): unit tests for the 5-state primary-pod
// FSM (Plan 06.6-05). Mirrors gateway/internal/emerg/fsm_test.go patterns —
// CAS race coverage + onChange capture + lockless State() read.
package primary

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeStateChangeWriter is a recording stub for the FSM's audit
// dependency. Mutex-guarded because the FSM is exercised concurrently
// in TestFSM_ConcurrentTransitions_OnlyOneWins.
type fakeStateChangeWriter struct {
	mu    sync.Mutex
	calls []fakeStateChangeCall
}

type fakeStateChangeCall struct {
	kind  string
	event any
}

func (f *fakeStateChangeWriter) WriteStateChange(kind string, ev any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeStateChangeCall{kind: kind, event: ev})
	return nil
}

func (f *fakeStateChangeWriter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func TestFSM_StateString(t *testing.T) {
	cases := map[State]string{
		StateAsleep:       "asleep",
		StateProvisioning: "provisioning",
		StateReady:        "ready",
		StateDraining:     "draining",
		StateDestroying:   "destroying",
		State(99):         "unknown",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Fatalf("State(%d).String() = %q, want %q", s, got, want)
		}
	}
}

func TestFSM_InitialStateIsAsleep(t *testing.T) {
	f := NewFSM(nil, nil)
	if f.State() != StateAsleep {
		t.Fatalf("initial state = %s, want asleep", f.State())
	}
}

func TestFSM_Transition_HappyPath(t *testing.T) {
	f := NewFSM(nil, nil)
	now := time.Unix(1000, 0)
	if err := f.Transition(StateAsleep, StateProvisioning, now, "schedule_up"); err != nil {
		t.Fatalf("Transition err=%v, want nil", err)
	}
	if f.State() != StateProvisioning {
		t.Fatalf("after happy-path: state = %s, want provisioning", f.State())
	}
	if f.EnteredAt().Unix() != now.Unix() {
		t.Fatalf("after happy-path: EnteredAt=%d, want %d", f.EnteredAt().Unix(), now.Unix())
	}
}

func TestFSM_Transition_FailsOnStaleFrom(t *testing.T) {
	f := NewFSM(nil, nil)
	// Current state is StateAsleep. Caller passes StateProvisioning as
	// from → CAS fails → ErrInvalidTransition.
	err := f.Transition(StateProvisioning, StateReady, time.Unix(1000, 0), "stale")
	if err == nil {
		t.Fatalf("Transition with stale from returned nil err, want ErrInvalidTransition")
	}
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Transition err = %v, want ErrInvalidTransition", err)
	}
	if f.State() != StateAsleep {
		t.Fatalf("after stale Transition: state = %s, want asleep (unchanged)", f.State())
	}
}

func TestFSM_Transition_SameStateIsNoop(t *testing.T) {
	var callbackCount atomic.Int32
	onChange := func(from, to State, at time.Time, reason string) {
		callbackCount.Add(1)
	}
	f := NewFSM(nil, onChange)
	// Asleep → Asleep is a noop; no error, no callback fire.
	if err := f.Transition(StateAsleep, StateAsleep, time.Unix(1000, 0), "noop"); err != nil {
		t.Fatalf("same-state Transition err=%v, want nil", err)
	}
	if callbackCount.Load() != 0 {
		t.Fatalf("onChange fired on same-state transition; want 0 calls, got %d", callbackCount.Load())
	}
}

func TestFSM_Transition_OnChangeFires(t *testing.T) {
	type capture struct {
		from, to State
		at       time.Time
		reason   string
	}
	var (
		mu       sync.Mutex
		captured []capture
	)
	onChange := func(from, to State, at time.Time, reason string) {
		mu.Lock()
		defer mu.Unlock()
		captured = append(captured, capture{from, to, at, reason})
	}
	f := NewFSM(nil, onChange)

	now := time.Unix(1000, 0)
	if err := f.Transition(StateAsleep, StateProvisioning, now, "schedule_up"); err != nil {
		t.Fatalf("Transition err=%v", err)
	}
	if err := f.Transition(StateProvisioning, StateReady, now.Add(time.Second), "health_pass"); err != nil {
		t.Fatalf("Transition err=%v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 2 {
		t.Fatalf("captured %d transitions, want 2: %+v", len(captured), captured)
	}
	if captured[0].from != StateAsleep || captured[0].to != StateProvisioning || captured[0].reason != "schedule_up" {
		t.Fatalf("captured[0] = %+v", captured[0])
	}
	if !captured[0].at.Equal(now) {
		t.Fatalf("captured[0].at = %v, want %v", captured[0].at, now)
	}
	if captured[1].from != StateProvisioning || captured[1].to != StateReady || captured[1].reason != "health_pass" {
		t.Fatalf("captured[1] = %+v", captured[1])
	}
}

func TestFSM_SetState_Unconditional(t *testing.T) {
	var captured []State
	var mu sync.Mutex
	onChange := func(from, to State, at time.Time, reason string) {
		mu.Lock()
		defer mu.Unlock()
		captured = append(captured, to)
	}
	f := NewFSM(nil, onChange)

	// Force Asleep → Destroying without going through the canonical
	// happy-path sequence (recovery scenario).
	now := time.Unix(2000, 0)
	f.SetState(StateDestroying, now, "recovery")
	if f.State() != StateDestroying {
		t.Fatalf("after SetState: state = %s, want destroying", f.State())
	}
	if f.EnteredAt().Unix() != now.Unix() {
		t.Fatalf("after SetState: EnteredAt=%d, want %d", f.EnteredAt().Unix(), now.Unix())
	}

	// SetState with same state is a noop (no callback fire).
	f.SetState(StateDestroying, now.Add(time.Second), "same")

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 1 {
		t.Fatalf("captured %d transitions, want 1: %v", len(captured), captured)
	}
	if captured[0] != StateDestroying {
		t.Fatalf("captured[0] = %s, want destroying", captured[0])
	}
}

func TestFSM_ConcurrentTransitions_OnlyOneWins(t *testing.T) {
	// 100 goroutines race Asleep→Provisioning. CAS guarantees exactly
	// one onChange fire and exactly one nil-error return; the other 99
	// callers MUST receive ErrInvalidTransition (CAS failure).
	var callbackCount atomic.Int32
	onChange := func(from, to State, at time.Time, reason string) {
		callbackCount.Add(1)
	}
	f := NewFSM(nil, onChange)

	const N = 100
	var (
		start    sync.WaitGroup
		done     sync.WaitGroup
		okCount  atomic.Int32
		errCount atomic.Int32
	)
	start.Add(1)
	done.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer done.Done()
			start.Wait()
			err := f.Transition(StateAsleep, StateProvisioning, time.Unix(3000, 0), "race")
			if err == nil {
				okCount.Add(1)
				return
			}
			if errors.Is(err, ErrInvalidTransition) {
				errCount.Add(1)
			}
		}()
	}
	start.Done()
	done.Wait()

	if f.State() != StateProvisioning {
		t.Fatalf("after race: state = %s, want provisioning", f.State())
	}
	if got := callbackCount.Load(); got != 1 {
		t.Fatalf("onChange fired %d times, want exactly 1 (CAS guarantees idempotency)", got)
	}
	if got := okCount.Load(); got != 1 {
		t.Fatalf("ok returns = %d, want exactly 1", got)
	}
	if got := errCount.Load(); got != N-1 {
		t.Fatalf("ErrInvalidTransition returns = %d, want %d", got, N-1)
	}
}

func TestFSM_WriterReceivesStateChange(t *testing.T) {
	// The optional stateChangeWriter (mirrors emerg pattern; non-nil
	// causes a WriteStateChange call after each successful transition).
	writer := &fakeStateChangeWriter{}
	f := NewFSM(writer, nil)
	if err := f.Transition(StateAsleep, StateProvisioning, time.Unix(4000, 0), "schedule_up"); err != nil {
		t.Fatalf("Transition err=%v", err)
	}
	if got := writer.count(); got != 1 {
		t.Fatalf("WriteStateChange calls = %d, want 1", got)
	}
	// Same-state noop must NOT emit a write.
	_ = f.Transition(StateProvisioning, StateProvisioning, time.Unix(4001, 0), "noop")
	if got := writer.count(); got != 1 {
		t.Fatalf("After noop: WriteStateChange calls = %d, want 1", got)
	}
	// Invalid (CAS-failing) transition must NOT emit a write.
	_ = f.Transition(StateAsleep, StateReady, time.Unix(4002, 0), "stale")
	if got := writer.count(); got != 1 {
		t.Fatalf("After stale: WriteStateChange calls = %d, want 1", got)
	}
}

func TestFSM_ConcurrentReadDuringTransition(t *testing.T) {
	// Race-detector smoke: hammer State() in one goroutine while
	// another runs Transition / SetState. Lockless atomic.Load must
	// not race with CompareAndSwap.
	f := NewFSM(nil, nil)

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

	now := time.Unix(5000, 0)
	for i := 0; i < 100; i++ {
		_ = f.Transition(StateAsleep, StateProvisioning, now.Add(time.Duration(i)*time.Second), "race")
		f.SetState(StateAsleep, now.Add(time.Duration(i)*time.Second), "reset")
	}
	close(stop)
	done.Wait()
}
