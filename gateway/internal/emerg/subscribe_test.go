// Package emerg (subscribe_test.go): Plan 06-05 Task 1 unit tests for
// the gw:breaker:events subscribe loop. Drives miniredis +
// redisx.PublishBreakerEvent to verify the tracker converges.
package emerg

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// newSubscribeTestReconciler constructs a Reconciler wired against a
// fresh miniredis. The DB pool is left nil — Plan 06-05 Task 1 only
// exercises the subscribe → tracker path. t.Cleanup tears down the
// miniredis + redis client.
func newSubscribeTestReconciler(t *testing.T) (*Reconciler, *redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = rdb.Close()
		mr.Close()
	})
	fsm := NewFSM(slog.New(slog.DiscardHandler), nil)
	r := NewReconciler(Deps{
		DB:           nil,
		Redis:        rdb,
		Redsync:      redisx.NewEmergRedsync(rdb),
		FSM:          fsm,
		Cfg:          config.Config{},
		TickInterval: 50 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})
	return r, rdb, mr
}

// TestSubscribe_AppliesLocalLlmEvent — publish an OPEN event for
// upstream=local-llm and verify the tracker converges to state=open
// with openSince > 0 within 1 second.
func TestSubscribe_AppliesLocalLlmEvent(t *testing.T) {
	r, rdb, _ := newSubscribeTestReconciler(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go r.Subscribe(ctx)
	// Allow the subscriber loop to register the SUBSCRIBE before publishing.
	time.Sleep(100 * time.Millisecond)

	if err := redisx.PublishBreakerEvent(ctx, rdb, redisx.BreakerEvent{
		Upstream: "local-llm", State: "open", SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("PublishBreakerEvent: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.tracker.State() == "open" && r.tracker.openSince.Load() > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("tracker did not converge: state=%q openSince=%d",
		r.tracker.State(), r.tracker.openSince.Load())
}

// TestSubscribe_IgnoresNonLocalLlm — publish an OPEN for local-stt;
// tracker MUST remain in the closed initial state. Phase 6 D-C2: only
// local-llm chat is the trigger signal.
func TestSubscribe_IgnoresNonLocalLlm(t *testing.T) {
	r, rdb, _ := newSubscribeTestReconciler(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go r.Subscribe(ctx)
	time.Sleep(100 * time.Millisecond)

	if err := redisx.PublishBreakerEvent(ctx, rdb, redisx.BreakerEvent{
		Upstream: "local-stt", State: "open", SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("PublishBreakerEvent: %v", err)
	}

	// Wait long enough that a buggy implementation would have written.
	time.Sleep(300 * time.Millisecond)

	if got := r.tracker.State(); got != "closed" {
		t.Fatalf("tracker state mutated by non-local-llm event: got %q, want closed", got)
	}
	if got := r.tracker.openSince.Load(); got != 0 {
		t.Fatalf("tracker openSince mutated by non-local-llm event: got %d, want 0", got)
	}
}

// TestSubscribe_MalformedPayloadDoesNotCrash — publish a non-JSON
// payload; the subscriber must drop it (log Warn) and continue
// processing subsequent valid events. Threat T-6-W5-02.
func TestSubscribe_MalformedPayloadDoesNotCrash(t *testing.T) {
	r, rdb, _ := newSubscribeTestReconciler(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go r.Subscribe(ctx)
	time.Sleep(100 * time.Millisecond)

	// Publish raw garbage on the channel — bypasses the Marshal helper
	// so the subscriber sees an unparseable payload.
	if err := rdb.Publish(ctx, redisx.BreakerEventsChannel(), []byte("{not-json")).Err(); err != nil {
		t.Fatalf("raw publish: %v", err)
	}

	// Then publish a VALID local-llm OPEN event — subscriber must still
	// be alive to consume it.
	if err := redisx.PublishBreakerEvent(ctx, rdb, redisx.BreakerEvent{
		Upstream: "local-llm", State: "open", SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("PublishBreakerEvent: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.tracker.State() == "open" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("subscriber did not survive malformed payload (tracker state = %q)",
		r.tracker.State())
}

// TestSubscribeEmergCommands_NonLeaderIgnores — even when a
// force_provision_request arrives, a non-leader reconciler must NOT
// mutate FSM state or write to the DB. Leader-only filter check.
func TestSubscribeEmergCommands_NonLeaderIgnores(t *testing.T) {
	r, rdb, _ := newSubscribeTestReconciler(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Confirm baseline — fresh reconciler is NOT leader.
	if r.IsLeader() {
		t.Fatalf("freshly constructed reconciler must not be leader")
	}

	go r.SubscribeEmergCommands(ctx)
	time.Sleep(100 * time.Millisecond)

	if err := redisx.PublishEmergEvent(ctx, rdb, redisx.EmergEvent{
		Type:      "force_provision_request",
		Reason:    "smoke",
		ReplicaID: "gatewayctl",
		SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("PublishEmergEvent: %v", err)
	}

	// Allow handler dispatch + drop on non-leader path.
	time.Sleep(200 * time.Millisecond)

	if got := r.deps.FSM.State(); got != StateHealthy {
		t.Fatalf("non-leader FSM mutated: got %s, want healthy", got)
	}
	if got := r.activeLifecycle.Load(); got != nil {
		t.Fatalf("non-leader activeLifecycle set: %+v", got)
	}
}

// =============================================================================
// Plan 06.6-08 Task 1 — SubscribePrimaryEvents (Pitfall #11) tests.
// =============================================================================
//
// All three tests share the same SubscribePrimaryEvents subscriber goroutine
// pattern: spawn → wait 100ms for SUBSCRIBE to register → publish via
// redisx.PublishPrimaryEvent → wait 200-300ms for handler dispatch → assert.
// The assertions hinge on whether r.cancelActiveLifecycle fires, observable
// via the activeLifecycle pointer being cleared by the layer-1 cancel branch
// (or remaining set when the leader/FSM-state gate refuses the cancel).

// newPrimarySubscribeTestReconciler constructs a Reconciler wired with an
// active lifecycle pre-seeded so cancelActiveLifecycle's "lc == nil" guard
// does not short-circuit before the test can verify the cancel path. The
// FSM is left at StateHealthy by default; callers explicitly transition via
// FSM.SetState when they want StateEmergencyActive.
func newPrimarySubscribeTestReconciler(t *testing.T) (*Reconciler, *redis.Client, *miniredis.Miniredis) {
	t.Helper()
	r, rdb, mr := newSubscribeTestReconciler(t)
	// Pre-seed activeLifecycle so the Layer-1 cancelActiveLifecycle branch
	// has something observable to act on. The lifecycleCancel pointer is
	// left nil — Layer 1 is idempotent against a nil swap. Layer 2 (Pub/Sub
	// broadcast on gw:emerg:events) still fires; we do not assert against
	// it here because the test scope is the Pitfall #11 dispatch decision.
	r.activeLifecycle.Store(&ActiveLifecycle{
		ID:             7777,
		VastInstanceID: 1234,
		StartedUnix:    time.Now().Unix(),
	})
	return r, rdb, mr
}

// TestSubscribePrimaryEvents_ForceDestroyEmergOnPrimaryReady — leader emerg
// replica AND FSM=EmergencyActive: a primary_ready event MUST trigger
// cancelActiveLifecycle (Pitfall #11 Option B primary precedence).
//
// Observable signal: the Layer-2 Pub/Sub broadcast on gw:emerg:events
// `cancel_in_flight` event proves cancelActiveLifecycle ran end-to-end.
// We subscribe to gw:emerg:events and assert receipt within 2s.
func TestSubscribePrimaryEvents_ForceDestroyEmergOnPrimaryReady(t *testing.T) {
	r, rdb, _ := newPrimarySubscribeTestReconciler(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up the prerequisites: leader=true AND FSM=EmergencyActive.
	r.isLeader.Store(true)
	r.deps.FSM.SetState(StateEmergencyActive, time.Now(), "test_force_emerg_active")

	// Tap gw:emerg:events to observe the Layer-2 cancel_in_flight publish.
	emergSub := rdb.Subscribe(ctx, redisx.EmergEventsChannel)
	t.Cleanup(func() { _ = emergSub.Close() })
	emergCh := emergSub.Channel()

	go r.SubscribePrimaryEvents(ctx)
	// Allow SUBSCRIBE on BOTH channels to register before publishing.
	time.Sleep(150 * time.Millisecond)

	if err := redisx.PublishPrimaryEvent(ctx, rdb, redisx.PrimaryEvent{
		Type:        "primary_ready",
		State:       "Ready",
		LifecycleID: 99,
		Reason:      "first_health_pass",
		SinceUnix:   time.Now().Unix(),
		ReplicaID:   "primary-leader",
	}); err != nil {
		t.Fatalf("PublishPrimaryEvent: %v", err)
	}

	// Wait for the layer-2 cancel_in_flight broadcast that proves
	// cancelActiveLifecycle ran. Deadline 2s is generous; the actual
	// dispatch typically lands within ~50ms.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case msg, ok := <-emergCh:
			if !ok {
				t.Fatalf("gw:emerg:events channel closed before cancel_in_flight observed")
			}
			var ev redisx.EmergEvent
			if err := jsonUnmarshalForTest(msg.Payload, &ev); err != nil {
				continue
			}
			if ev.Type == "cancel_in_flight" && ev.Reason == "primary_took_over" {
				// Success — cancelActiveLifecycle ran with the expected
				// reason string ("primary_took_over").
				return
			}
		case <-deadline:
			t.Fatalf("cancelActiveLifecycle did not fire: no cancel_in_flight event with reason=primary_took_over observed within 2s")
		}
	}
}

// TestSubscribePrimaryEvents_NoOpWhenNotLeader — non-leader emerg replica
// MUST drop primary_ready events even when FSM=EmergencyActive. The
// single-leader invariant (PRV-03) requires only the leader to mutate FSM
// state; non-leaders observe but never act.
func TestSubscribePrimaryEvents_NoOpWhenNotLeader(t *testing.T) {
	r, rdb, _ := newPrimarySubscribeTestReconciler(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Pre-conditions: NOT leader, FSM=EmergencyActive (would normally
	// trigger the cancel if leadership were present).
	if r.IsLeader() {
		t.Fatalf("freshly constructed reconciler must not be leader")
	}
	r.deps.FSM.SetState(StateEmergencyActive, time.Now(), "test_force_emerg_active")

	go r.SubscribePrimaryEvents(ctx)
	time.Sleep(150 * time.Millisecond)

	if err := redisx.PublishPrimaryEvent(ctx, rdb, redisx.PrimaryEvent{
		Type:        "primary_ready",
		State:       "Ready",
		LifecycleID: 99,
		Reason:      "first_health_pass",
		SinceUnix:   time.Now().Unix(),
		ReplicaID:   "primary-leader",
	}); err != nil {
		t.Fatalf("PublishPrimaryEvent: %v", err)
	}

	// Allow handler dispatch + drop on non-leader path. 250ms is generous —
	// a buggy implementation would have cleared activeLifecycle by now.
	time.Sleep(250 * time.Millisecond)

	if got := r.activeLifecycle.Load(); got == nil {
		t.Fatalf("non-leader replica acted on primary_ready: activeLifecycle was cleared")
	}
	if got := r.deps.FSM.State(); got != StateEmergencyActive {
		t.Fatalf("non-leader replica mutated FSM state: got %s, want %s",
			got, StateEmergencyActive)
	}
}

// TestSubscribePrimaryEvents_NoOpWhenNotEmergActive — leader emerg replica
// BUT FSM != EmergencyActive: cancelActiveLifecycle MUST NOT fire. The
// Pitfall #11 handoff only applies when an emerg pod is actively serving;
// other emerg states (Healthy / Cooldown / Recovering / Provisioning) have
// no active lifecycle to cancel OR are already mid-cutback.
func TestSubscribePrimaryEvents_NoOpWhenNotEmergActive(t *testing.T) {
	r, rdb, _ := newPrimarySubscribeTestReconciler(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Pre-conditions: leader=true, FSM=StateHealthy (no emerg pod live).
	r.isLeader.Store(true)
	// FSM stays at default StateHealthy (initial state from NewFSM).
	if got := r.deps.FSM.State(); got != StateHealthy {
		t.Fatalf("baseline FSM state mismatch: got %s, want %s", got, StateHealthy)
	}

	go r.SubscribePrimaryEvents(ctx)
	time.Sleep(150 * time.Millisecond)

	if err := redisx.PublishPrimaryEvent(ctx, rdb, redisx.PrimaryEvent{
		Type:        "primary_ready",
		State:       "Ready",
		LifecycleID: 99,
		Reason:      "first_health_pass",
		SinceUnix:   time.Now().Unix(),
		ReplicaID:   "primary-leader",
	}); err != nil {
		t.Fatalf("PublishPrimaryEvent: %v", err)
	}

	// Allow handler dispatch + drop on non-active path. A buggy
	// implementation would have cleared activeLifecycle by now.
	time.Sleep(250 * time.Millisecond)

	if got := r.activeLifecycle.Load(); got == nil {
		t.Fatalf("leader replica wrongly cleared activeLifecycle from non-Active FSM state")
	}
	if got := r.deps.FSM.State(); got != StateHealthy {
		t.Fatalf("leader replica mutated FSM state from non-Active baseline: got %s, want %s",
			got, StateHealthy)
	}
}

// jsonUnmarshalForTest is a thin wrapper that callers `continue` past on
// parse failure (e.g. concurrent publishes from other tests racing the
// shared miniredis). Keeps the assertion loop terse.
func jsonUnmarshalForTest(payload string, v any) error {
	return json.Unmarshal([]byte(payload), v)
}
