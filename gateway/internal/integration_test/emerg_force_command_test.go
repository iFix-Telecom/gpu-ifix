//go:build integration

// Phase 6 Plan 06-05 Task 3 — gw:emerg:events command consumption
// (BLOCKER 2 fix 2026-05-13). Proves that EmergEvents published by
// gatewayctl (Plan 06-10) are consumed end-to-end by the
// leader-elected reconciler:
//
//   - force_provision_request: leader INSERTs lifecycle row with
//     trigger_reason='manual_force' AND advances FSM HEALTHY →
//     EMERGENCY_PROVISIONING. (TestEmergReconcilerHandlesForceProvisionEvent)
//   - When 2 reconcilers race, the lifecycle is INSERTed exactly once
//     (single-leader invariant — non-leader observes the event but
//     does NOT mutate state). (TestEmergReconcilerForceProvisionRejectedNonLeader)
//   - When no active lifecycle exists, force-destroy is a no-op
//     (logged Warn, no FSM mutation, no destroy call). (TestEmergReconcilerForceDestroyNoOpWhenIdle)
//
// The active-lifecycle force-destroy path (TestEmergReconcilerHandlesForceDestroyEvent)
// is DEFERRED to Plan 06-08 alongside the destroyAndCloseLifecycle helper
// it depends on.
//
// 2026-05-14 — stale-test fix (debug session emerg-integration-tests-ci,
// cause 2). These tests were authored against the Plan 06-05 reconciler,
// whose StateEmergencyProvisioning branch was a no-op stub ("Plan 06-05
// stops at the FSM transition"). Plans 06-06/06-07 added the real
// evaluateEmergencyProvisioning, which on the very next tick after the
// force-provision INSERT:
//
//   - sees activeLifecycle != nil → takes the D-C3 cancel-detection branch;
//   - reads r.tracker.State(), which for a fresh tracker (no breaker event
//     ever published) is the zero-value "closed";
//   - "closed" matches the cancel condition → it transitions the FSM back
//     EmergencyProvisioning → Healthy with reason
//     'cancelled_local_llm_recovered'.
//
// The FSM therefore bounced Healthy → EmergencyProvisioning → Healthy
// within ~one tick, and the assertion polling caught "healthy"
// ("FSM did not advance" / "NEITHER FSM advanced"). The reconciler
// behaviour is CORRECT — the tests simply never published a local-llm
// OPEN event, so the reconciler legitimately read "primary recovered,
// cancel the provisioning". The fix mirrors the established idiom from
// every other passing emerg integration test (emerg_provision_happy_test.go,
// emerg_force_destroy_event_test.go): publish a sustained local-llm OPEN
// so the tracker stays "open" (no spurious cancel) AND wire a mock Vast.ai
// server + stub /health so provisionLifecycle has a working client instead
// of self-aborting with shutdown_reason='no_vast_client'. The intent of
// the tests — leader INSERTs exactly one manual_force lifecycle row and
// advances the FSM out of HEALTHY; the non-leader does neither — is
// preserved.
package integration

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// forceProvisionVastMock builds a mock Vast.ai server pre-loaded with a
// single below-cap offer + a running instance, matching the happy-path
// fixture used by emerg_provision_happy_test.go. Shared by the two
// force_provision_request tests so the reconciler's provisioning goroutine
// has a working client (otherwise provisionLifecycle aborts immediately
// with shutdown_reason='no_vast_client' and bounces the FSM to HEALTHY).
func forceProvisionVastMock(t *testing.T) *vast.Client {
	t.Helper()
	mock := newMockVastServer(t)
	offers := []vast.Offer{{
		ID: 9001, DphTotal: 0.35, GpuName: "RTX 4090", Reliability: 0.99,
		HostID: 100, MachineID: 50, NumGpus: 1,
	}}
	mock.searchResponse.Store(&offers)
	inst := vast.Instance{
		ID: 12345, ActualStatus: "running", IntendedStatus: "running",
		PublicIPAddr: "127.0.0.1",
		Ports: map[string][]vast.PortBinding{
			"9100/tcp": {{HostIP: "0.0.0.0", HostPort: "40713"}},
		},
		HostID: 100, DphTotal: 0.35, MachineID: 50,
	}
	mock.getResponse.Store(&inst)
	return vast.NewClientWithBaseURL("test-key", mock.Server.URL)
}

// stateAtLeastProvisioning is true once the FSM has advanced out of
// HEALTHY into the emergency path. With the real (post-06-06) reconciler
// the FSM does not park in EMERGENCY_PROVISIONING — once provisioning
// succeeds it continues to EMERGENCY_ACTIVE. The force-provision tests
// only care that the leader DID advance the FSM (and the follower did
// NOT), so we accept any of the emergency-path states.
func stateAtLeastProvisioning(s emerg.State) bool {
	switch s {
	case emerg.StateEmergencyProvisioning,
		emerg.StateEmergencyActive,
		emerg.StateRecovering,
		emerg.StateCooldown:
		return true
	default:
		return false
	}
}

// TestEmergReconcilerHandlesForceProvisionEvent — single reconciler
// acquires leadership, gatewayctl-style force_provision_request is
// published, reconciler consumes and INSERTs lifecycle row + advances
// FSM. Within 5s budget.
func TestEmergReconcilerHandlesForceProvisionEvent(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

	fsm := emerg.NewFSM(slog.New(slog.DiscardHandler), nil)
	r := emerg.NewReconciler(emerg.Deps{
		DB:           pool,
		Redis:        rdb,
		Redsync:      redisx.NewEmergRedsync(rdb),
		FSM:          fsm,
		Cfg:          cfg,
		Vast:         forceProvisionVastMock(t),
		TickInterval: 100 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})
	r.SetHealthCheck(func(_ context.Context, _ string) bool { return true })

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	if !waitFor(t, 3*time.Second, 50*time.Millisecond, r.IsLeader) {
		t.Fatalf("reconciler did not acquire leadership within 3s")
	}

	// Sustained local-llm OPEN keeps the per-replica tracker in "open" so
	// evaluateEmergencyProvisioning's D-C3 cancel-detection branch does NOT
	// fire (a fresh tracker defaults to "closed", which the reconciler
	// correctly reads as "primary recovered → cancel provisioning").
	publishBreakerEvent(t, rdb, "local-llm", "open")

	if err := redisx.PublishEmergEvent(ctx, rdb, redisx.EmergEvent{
		Type:      "force_provision_request",
		Reason:    "smoke",
		ReplicaID: "gatewayctl",
		SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("PublishEmergEvent: %v", err)
	}

	// Eventually the FSM advances out of HEALTHY into the emergency path
	// (the leader consumed the command, INSERTed the lifecycle row, and
	// drove the FSM transition).
	if !waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		return stateAtLeastProvisioning(fsm.State())
	}) {
		t.Fatalf("FSM did not advance after force-provision; got %s", fsm.State())
	}

	// Exactly one lifecycle row with trigger_reason='manual_force' was
	// INSERTed by the leader's handleForceProvision.
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM ai_gateway.emergency_lifecycles WHERE trigger_reason = 'manual_force'`,
	).Scan(&count); err != nil {
		t.Fatalf("count manual_force lifecycles: %v", err)
	}
	if count != 1 {
		t.Fatalf("manual_force lifecycle count = %d, want 1", count)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}

// TestEmergReconcilerForceProvisionRejectedNonLeader — 2 reconcilers
// share 1 Redis. ONE will be leader. force-provision published once →
// leader INSERTs once + transitions; non-leader observes the event but
// does NOT INSERT (PRV-03 single-leader invariant carried into the
// command path).
func TestEmergReconcilerForceProvisionRejectedNonLeader(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

	fsm1 := emerg.NewFSM(slog.New(slog.DiscardHandler), nil)
	fsm2 := emerg.NewFSM(slog.New(slog.DiscardHandler), nil)

	ctx1, cancel1 := context.WithCancel(rootCtx)
	ctx2, cancel2 := context.WithCancel(rootCtx)
	defer cancel1()
	defer cancel2()

	r1 := emerg.NewReconciler(emerg.Deps{
		DB:           pool,
		Redis:        rdb,
		Redsync:      redisx.NewEmergRedsync(rdb),
		FSM:          fsm1,
		Cfg:          cfg,
		Vast:         forceProvisionVastMock(t),
		TickInterval: 100 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})
	r2 := emerg.NewReconciler(emerg.Deps{
		DB:           pool,
		Redis:        rdb,
		Redsync:      redisx.NewEmergRedsync(rdb),
		FSM:          fsm2,
		Cfg:          cfg,
		Vast:         forceProvisionVastMock(t),
		TickInterval: 100 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})
	r1.SetHealthCheck(func(_ context.Context, _ string) bool { return true })
	r2.SetHealthCheck(func(_ context.Context, _ string) bool { return true })

	done1 := make(chan struct{})
	done2 := make(chan struct{})
	go func() { r1.Run(ctx1); close(done1) }()
	go func() { r2.Run(ctx2); close(done2) }()

	// Wait for exactly one leader.
	if !waitFor(t, 3*time.Second, 50*time.Millisecond, func() bool {
		return r1.IsLeader() != r2.IsLeader()
	}) {
		t.Fatalf("expected exactly 1 leader; r1.IsLeader=%v r2.IsLeader=%v",
			r1.IsLeader(), r2.IsLeader())
	}

	// Sustained local-llm OPEN — both replicas' trackers go "open" so the
	// leader's evaluateEmergencyProvisioning does not spuriously cancel the
	// provisioning it is about to start (see file-level comment).
	publishBreakerEvent(t, rdb, "local-llm", "open")

	// Publish force-provision command.
	if err := redisx.PublishEmergEvent(rootCtx, rdb, redisx.EmergEvent{
		Type:      "force_provision_request",
		Reason:    "smoke-2-replicas",
		ReplicaID: "gatewayctl",
		SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("PublishEmergEvent: %v", err)
	}

	// Eventually exactly 1 row is INSERTed (PRV-03: single-leader
	// invariant prevents duplicate INSERT). The partial unique index
	// `emergency_live_singleton` is the safety net at the DB layer; the
	// reconciler's leader-only filter is the primary defense.
	if !waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		var count int
		if err := pool.QueryRow(rootCtx,
			`SELECT COUNT(*) FROM ai_gateway.emergency_lifecycles WHERE trigger_reason = 'manual_force'`,
		).Scan(&count); err != nil {
			return false
		}
		return count == 1
	}) {
		var count int
		_ = pool.QueryRow(rootCtx,
			`SELECT COUNT(*) FROM ai_gateway.emergency_lifecycles WHERE trigger_reason = 'manual_force'`,
		).Scan(&count)
		t.Fatalf("expected exactly 1 manual_force lifecycle; got %d", count)
	}

	// Verify it stays at 1 (no late duplicate INSERT after the leader
	// processes the event).
	time.Sleep(500 * time.Millisecond)
	var count int
	if err := pool.QueryRow(rootCtx,
		`SELECT COUNT(*) FROM ai_gateway.emergency_lifecycles WHERE trigger_reason = 'manual_force'`,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("late duplicate INSERT detected: count = %d, want 1", count)
	}

	// Verify exactly one FSM advanced (the leader's). The follower's FSM
	// must remain in HEALTHY because applyEmergCommand short-circuits on
	// !isLeader BEFORE the type switch.
	leaderAdvanced := stateAtLeastProvisioning(fsm1.State())
	followerAdvanced := stateAtLeastProvisioning(fsm2.State())
	if leaderAdvanced && followerAdvanced {
		t.Fatalf("BOTH FSMs advanced (PRV-03 violated)")
	}
	if !leaderAdvanced && !followerAdvanced {
		t.Fatalf("NEITHER FSM advanced; force-provision was dropped silently")
	}

	cancel1()
	cancel2()
	select {
	case <-done1:
	case <-time.After(3 * time.Second):
		t.Fatalf("r1 Run did not return after cancel")
	}
	select {
	case <-done2:
	case <-time.After(3 * time.Second):
		t.Fatalf("r2 Run did not return after cancel")
	}
}

// TestEmergReconcilerForceDestroyNoOpWhenIdle — FSM = HEALTHY (no
// active lifecycle); force_destroy_request must be a no-op (Warn log,
// no destroy call, no FSM mutation). The active-lifecycle destroy path
// is exercised in Plan 06-08 (TestEmergReconcilerHandlesForceDestroyEvent
// lands there alongside the destroyAndCloseLifecycle helper).
func TestEmergReconcilerForceDestroyNoOpWhenIdle(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

	fsm := emerg.NewFSM(slog.New(slog.DiscardHandler), nil)
	r := emerg.NewReconciler(emerg.Deps{
		DB:           pool,
		Redis:        rdb,
		Redsync:      redisx.NewEmergRedsync(rdb),
		FSM:          fsm,
		Cfg:          cfg,
		TickInterval: 100 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	if !waitFor(t, 3*time.Second, 50*time.Millisecond, r.IsLeader) {
		t.Fatalf("reconciler did not acquire leadership within 3s")
	}

	// Publish force-destroy with no active lifecycle.
	if err := redisx.PublishEmergEvent(ctx, rdb, redisx.EmergEvent{
		Type:      "force_destroy_request",
		Reason:    "smoke-idle",
		ReplicaID: "gatewayctl",
		SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("PublishEmergEvent: %v", err)
	}

	// Wait long enough for the subscriber to dispatch.
	time.Sleep(500 * time.Millisecond)

	if got := fsm.State(); got != emerg.StateHealthy {
		t.Fatalf("FSM mutated by no-op force-destroy: got %s, want healthy", got)
	}
	// Verify no lifecycle rows were touched.
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM ai_gateway.emergency_lifecycles`,
	).Scan(&count); err != nil {
		t.Fatalf("count lifecycles: %v", err)
	}
	if count != 0 {
		t.Fatalf("lifecycle rows created by no-op force-destroy: count = %d, want 0", count)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}
