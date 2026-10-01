// Package shed (fsm_test.go): deterministic unit tests for the
// 4-state shed FSM (CONTEXT.md D-C1 transition table). All tests
// drive Evaluate with an explicit time.Time so transitions are
// independent of wall-clock skew and the test suite is reproducible
// (RESEARCH §Pitfall 9 tick clock skew).
//
// Transition table coverage (16 cells of 4×4 state×signal):
//   - Off+!sat        → Off (stay)
//   - Off+sat         → Armed (signal_rose)
//   - Armed+!sat      → Off (signal_dropped_during_arm; no wait)
//   - Armed+sat <Arm  → Armed (still arming)
//   - Armed+sat ≥Arm  → On (arm_timeout_sustained)
//   - On+!sat         → Recovering (signal_dropped)
//   - On+sat          → On (sustained)
//   - Recovering+sat  → On (signal_returned_during_recover; skip Armed)
//   - Recov+!sat <Rec → Recovering (still recovering)
//   - Recov+!sat ≥Rec → Off (recover_timeout_clean)
//
// Plus VramUnknown reduces 2-of-3 to 1-of-2 (D-A1) and the OnChange
// callback fires after CAS success.
package shed

import (
	"log/slog"
	"sync"
	"testing"
	"time"
)

// newTestFSM constructs an FSM at StateOff with the given hysteresis
// windows and no onChange callback. Test helper centralised so the
// per-test boilerplate stays one line.
func newTestFSM(t *testing.T, arm, recover int64) *FSM {
	t.Helper()
	cfg := Config{ArmSeconds: arm, RecoverSeconds: recover}
	return NewFSM("local-llm", cfg, nil, slog.Default())
}

func TestFSM_InitialStateIsOff(t *testing.T) {
	f := newTestFSM(t, 30, 60)
	if f.State() != StateOff {
		t.Fatalf("initial = %s, want off", f.State())
	}
}

func TestFSM_StateString(t *testing.T) {
	cases := map[State]string{
		StateOff:        "off",
		StateArmed:      "armed",
		StateOn:         "on",
		StateRecovering: "recovering",
		State(99):       "unknown",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Fatalf("State(%d).String() = %q, want %q", s, got, want)
		}
	}
}

func TestFSM_Off_SaturatedSignal_GoesArmed(t *testing.T) {
	f := newTestFSM(t, 30, 60)
	now := time.Unix(1000, 0)
	f.Evaluate(now, Signals{InflightOverMax: true, P95OverMax: true})
	if f.State() != StateArmed {
		t.Fatalf("after 2-of-3 sat: got %s, want armed", f.State())
	}
}

func TestFSM_Off_OnlyOneSignal_StaysOff(t *testing.T) {
	f := newTestFSM(t, 30, 60)
	f.Evaluate(time.Unix(1000, 0), Signals{InflightOverMax: true}) // only 1/3
	if f.State() != StateOff {
		t.Fatalf("after 1-of-3: got %s, want off", f.State())
	}
}

func TestFSM_VramUnknown_ReducesGateToOneOfTwo(t *testing.T) {
	f := newTestFSM(t, 30, 60)
	// Vram over max but unknown should be ignored — inflight alone is
	// 1 effective signal out of 2 → stays off.
	f.Evaluate(time.Unix(1000, 0), Signals{InflightOverMax: true, VramOverMax: true, VramUnknown: true})
	if f.State() != StateOff {
		t.Fatalf("vram unknown should disable vram signal; got %s, want off", f.State())
	}
	// Inflight + P95 with vram unknown still triggers (2 real signals).
	f.Evaluate(time.Unix(1001, 0), Signals{InflightOverMax: true, P95OverMax: true, VramOverMax: true, VramUnknown: true})
	if f.State() != StateArmed {
		t.Fatalf("inflight+p95 should trigger even with vram unknown; got %s", f.State())
	}
}

func TestFSM_Armed_SignalDropped_GoesOffImmediate(t *testing.T) {
	f := newTestFSM(t, 30, 60)
	f.Evaluate(time.Unix(1000, 0), Signals{InflightOverMax: true, P95OverMax: true})
	if f.State() != StateArmed {
		t.Fatalf("precondition armed failed: %s", f.State())
	}
	f.Evaluate(time.Unix(1005, 0), Signals{}) // dropped
	if f.State() != StateOff {
		t.Fatalf("armed+dropped should go off immediately; got %s", f.State())
	}
}

func TestFSM_Armed_SustainedArm_GoesOnAfterArmSeconds(t *testing.T) {
	f := newTestFSM(t, 30, 60)
	f.Evaluate(time.Unix(1000, 0), Signals{InflightOverMax: true, P95OverMax: true})
	// elapsed 29s < 30s arm → still armed
	f.Evaluate(time.Unix(1029, 0), Signals{InflightOverMax: true, P95OverMax: true})
	if f.State() != StateArmed {
		t.Fatalf("elapsed 29s should still be armed; got %s", f.State())
	}
	// elapsed 30s == arm → transitions to On
	f.Evaluate(time.Unix(1030, 0), Signals{InflightOverMax: true, P95OverMax: true})
	if f.State() != StateOn {
		t.Fatalf("elapsed 30s should go on; got %s", f.State())
	}
}

func TestFSM_On_SignalDropped_GoesRecovering(t *testing.T) {
	f := newTestFSM(t, 1, 60) // short arm to reach On fast
	f.Evaluate(time.Unix(1000, 0), Signals{InflightOverMax: true, P95OverMax: true})
	f.Evaluate(time.Unix(1002, 0), Signals{InflightOverMax: true, P95OverMax: true}) // On
	if f.State() != StateOn {
		t.Fatalf("precondition on failed: %s", f.State())
	}
	f.Evaluate(time.Unix(1003, 0), Signals{}) // clean streak starts
	if f.State() != StateOn {
		t.Fatalf("on + 1 clean tick must stay on (dwell-out); got %s", f.State())
	}
	f.Evaluate(time.Unix(1006, 0), Signals{}) // clean for 3s = dwell-out
	if f.State() != StateRecovering {
		t.Fatalf("on + clean ≥ dwell-out should go recovering; got %s", f.State())
	}
}

func TestFSM_On_StaysOn_WhenStillSaturated(t *testing.T) {
	f := newTestFSM(t, 1, 60)
	f.Evaluate(time.Unix(1000, 0), Signals{InflightOverMax: true, P95OverMax: true})
	f.Evaluate(time.Unix(1002, 0), Signals{InflightOverMax: true, P95OverMax: true})
	if f.State() != StateOn {
		t.Fatalf("precondition on failed: %s", f.State())
	}
	f.Evaluate(time.Unix(1005, 0), Signals{InflightOverMax: true, P95OverMax: true, VramOverMax: true})
	if f.State() != StateOn {
		t.Fatalf("on+sat should stay on; got %s", f.State())
	}
}

func TestFSM_Recovering_SaturatedAgain_GoesOnNotArmed(t *testing.T) {
	f := newTestFSM(t, 1, 60)
	f.Evaluate(time.Unix(1000, 0), Signals{InflightOverMax: true, P95OverMax: true})
	f.Evaluate(time.Unix(1002, 0), Signals{InflightOverMax: true, P95OverMax: true})
	f.Evaluate(time.Unix(1003, 0), Signals{})
	f.Evaluate(time.Unix(1006, 0), Signals{}) // recovering (dwell-out 3s)
	if f.State() != StateRecovering {
		t.Fatalf("precondition recovering failed: %s", f.State())
	}
	f.Evaluate(time.Unix(1007, 0), Signals{InflightOverMax: true, P95OverMax: true}) // sat streak starts
	if f.State() != StateRecovering {
		t.Fatalf("recovering + 1 sat tick must stay recovering (dwell-in); got %s", f.State())
	}
	f.Evaluate(time.Unix(1008, 0), Signals{InflightOverMax: true, P95OverMax: true}) // sustained ≥ dwell-in (arm=1s)
	if f.State() != StateOn {
		t.Fatalf("recovering + sustained sat should go ON (not armed); got %s", f.State())
	}
}

func TestFSM_Recovering_CleanForRecoverSeconds_GoesOff(t *testing.T) {
	f := newTestFSM(t, 1, 10)
	f.Evaluate(time.Unix(1000, 0), Signals{InflightOverMax: true, P95OverMax: true})
	f.Evaluate(time.Unix(1002, 0), Signals{InflightOverMax: true, P95OverMax: true}) // On
	f.Evaluate(time.Unix(1003, 0), Signals{})                                        // clean streak starts
	f.Evaluate(time.Unix(1006, 0), Signals{})                                        // Recovering (dwell-out)
	if f.State() != StateRecovering {
		t.Fatalf("precondition recovering failed: %s", f.State())
	}
	// clean for 9s (since 1003) < 10
	f.Evaluate(time.Unix(1012, 0), Signals{})
	if f.State() != StateRecovering {
		t.Fatalf("clean 9s should still recover; got %s", f.State())
	}
	// clean for 10s ≥ 10
	f.Evaluate(time.Unix(1013, 0), Signals{})
	if f.State() != StateOff {
		t.Fatalf("clean 10s should go off; got %s", f.State())
	}
}

// Quick 261001-9fh: signal toggling every tick while On (shed holding
// inflight at the threshold) must NOT flap On↔Recovering.
func TestFSM_On_SignalTogglingEveryTick_DoesNotFlap(t *testing.T) {
	var mu sync.Mutex
	transitions := 0
	f := NewFSM("test", Config{ArmSeconds: 1, RecoverSeconds: 2}, func(_, _ State, _ string) {
		mu.Lock()
		transitions++
		mu.Unlock()
	}, slog.Default())
	sat := Signals{InflightOverMax: true, P95OverMax: true}
	f.Evaluate(time.Unix(1000, 0), sat)
	f.Evaluate(time.Unix(1001, 0), sat) // On
	if f.State() != StateOn {
		t.Fatalf("precondition on failed: %s", f.State())
	}
	mu.Lock()
	base := transitions
	mu.Unlock()
	for i := int64(2); i < 30; i++ {
		sig := sat
		if i%2 == 0 {
			sig = Signals{P95OverMax: true} // inflight dipped below max for this tick
		}
		f.Evaluate(time.Unix(1000+i, 0), sig)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := transitions - base; got != 0 {
		t.Fatalf("toggling signal while On caused %d transitions (flap); want 0, state=%s", got, f.State())
	}
}

// Quick 261001-9fh: a single saturated blip inside Recovering neither
// bounces to On nor lets the recover clock run through it.
func TestFSM_Recovering_BlipRestartsRecoverClock(t *testing.T) {
	f := newTestFSM(t, 1, 10)
	sat := Signals{InflightOverMax: true, P95OverMax: true}
	f.Evaluate(time.Unix(1000, 0), sat)
	f.Evaluate(time.Unix(1001, 0), sat)       // On
	f.Evaluate(time.Unix(1002, 0), Signals{}) // clean starts
	f.Evaluate(time.Unix(1005, 0), Signals{}) // Recovering
	f.Evaluate(time.Unix(1008, 0), sat)       // blip (1 tick)
	if f.State() != StateRecovering {
		t.Fatalf("single blip must not bounce to on; got %s", f.State())
	}
	f.Evaluate(time.Unix(1009, 0), Signals{}) // clean restarts at 1009
	f.Evaluate(time.Unix(1013, 0), Signals{}) // only 4s clean since blip
	if f.State() != StateRecovering {
		t.Fatalf("recover clock must restart after blip; got %s", f.State())
	}
	f.Evaluate(time.Unix(1019, 0), Signals{}) // 10s clean since 1009
	if f.State() != StateOff {
		t.Fatalf("10s clean after blip should go off; got %s", f.State())
	}
}

func TestFSM_OnChangeCallbackFires(t *testing.T) {
	var mu sync.Mutex
	var captured []string
	onChange := func(from, to State, reason string) {
		mu.Lock()
		defer mu.Unlock()
		captured = append(captured, from.String()+"->"+to.String()+":"+reason)
	}
	cfg := Config{ArmSeconds: 1, RecoverSeconds: 1}
	f := NewFSM("test", cfg, onChange, slog.Default())
	f.Evaluate(time.Unix(1000, 0), Signals{InflightOverMax: true, P95OverMax: true})
	f.Evaluate(time.Unix(1002, 0), Signals{InflightOverMax: true, P95OverMax: true})

	mu.Lock()
	defer mu.Unlock()
	if len(captured) < 2 {
		t.Fatalf("expected ≥2 transitions captured, got %d: %v", len(captured), captured)
	}
}

func TestFSM_Transition_SyntheticState(t *testing.T) {
	// Synthetic transitions (used by gatewayctl shed-force overrides
	// and the subscribe loop for remote events) must succeed even when
	// no signal change drove them.
	f := newTestFSM(t, 30, 60)
	f.Transition(StateOn, "manual_force_on")
	if f.State() != StateOn {
		t.Fatalf("synthetic transition: got %s, want on", f.State())
	}
	f.Transition(StateOff, "manual_force_off")
	if f.State() != StateOff {
		t.Fatalf("synthetic transition: got %s, want off", f.State())
	}
}

func TestFSM_UpdateConfig_AppliedAtomically(t *testing.T) {
	f := newTestFSM(t, 30, 60)
	f.UpdateConfig(Config{ArmSeconds: 1, RecoverSeconds: 1})
	// With short arm window, the FSM should reach On after just 1 second.
	f.Evaluate(time.Unix(1000, 0), Signals{InflightOverMax: true, P95OverMax: true})
	f.Evaluate(time.Unix(1002, 0), Signals{InflightOverMax: true, P95OverMax: true})
	if f.State() != StateOn {
		t.Fatalf("after UpdateConfig(arm=1), expected On; got %s", f.State())
	}
}

func TestFSM_EnteredAt_TracksTransition(t *testing.T) {
	f := newTestFSM(t, 30, 60)
	t0 := f.EnteredAt()
	if t0.IsZero() {
		t.Fatalf("EnteredAt should be initialised at construction, got zero")
	}
	// Force a transition. EnteredAt should move forward (or at least
	// be re-stamped to a time >= t0).
	f.Transition(StateOn, "test")
	t1 := f.EnteredAt()
	if t1.Before(t0) {
		t.Fatalf("EnteredAt(%v) regressed before initial(%v)", t1, t0)
	}
}
