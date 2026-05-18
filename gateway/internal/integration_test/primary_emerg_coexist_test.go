//go:build integration

// Phase 6.6 Plan 06.6-10 Task 2 — Pitfall #11 (emerg + primary coexistence)
// mechanical proof.
//
// 06.6-RESEARCH.md §Pitfall #11 — Option B (primary precedence handoff):
// when the primary pod transitions to StateReady and publishes a
// `primary_ready` event on gw:primary:events, any emergency pod that is
// currently serving (FSM=StateEmergencyActive) is REDUNDANT — a single
// tier-0 LLM pod should serve the peak window. The emerg leader replica
// observes the Pub/Sub event via emerg.SubscribePrimaryEvents and calls
// cancelActiveLifecycle('primary_took_over').
//
// This test drives the full cross-package handoff:
//
//   1. Drive emerg from Healthy → EmergencyActive via the proven
//      happy-path pattern (mock Vast + breaker open).
//   2. Spawn emerg.SubscribePrimaryEvents in its own goroutine.
//   3. Publish PrimaryEvent{Type:"primary_ready", LifecycleID: N} on
//      gw:primary:events.
//   4. Assert emerg cancels its active lifecycle: FSM transitions OUT of
//      EmergencyActive AND emerg's fake Vast records DestroyInstance was
//      called with the emerg instance ID.
package integration

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// TestEmergCoexist_PrimaryReadyForceDestroysEmerg — Pitfall #11 Option B
// proof. primary_ready while emerg=EmergencyActive triggers
// emerg.cancelActiveLifecycle('primary_took_over').
func TestEmergCoexist_PrimaryReadyForceDestroysEmerg(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

	// Mock Vast server reports a running emerg instance with ports
	// populated and /health returning 200 — drives emerg to
	// EmergencyActive.
	mock := newMockVastServer(t)
	offers := []vast.Offer{{
		ID: 9001, DphTotal: 0.35, GpuName: "RTX 4090", Reliability: 0.99,
		HostID: 100, MachineID: 50, NumGpus: 1,
	}}
	mock.searchResponse.Store(&offers)
	inst := vast.Instance{
		ID: 888, ActualStatus: "running", IntendedStatus: "running",
		PublicIPAddr: "127.0.0.1",
		Ports: map[string][]vast.PortBinding{
			"8000/tcp": {{HostIP: "0.0.0.0", HostPort: "40888"}},
		},
		HostID: 100, DphTotal: 0.35, MachineID: 50,
	}
	mock.getResponse.Store(&inst)

	vastClient := vast.NewClientWithBaseURL("test-key", mock.Server.URL)
	fsm := emerg.NewFSM(slog.New(slog.DiscardHandler), nil)
	r := emerg.NewReconciler(emerg.Deps{
		DB:           pool,
		Redis:        rdb,
		Redsync:      redisx.NewEmergRedsync(rdb),
		FSM:          fsm,
		Cfg:          cfg,
		Vast:         vastClient,
		TickInterval: 100 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})
	r.SetHealthCheck(func(_ context.Context, _ string) bool { return true })

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	// Wait for leadership before we drive the trigger.
	require.Eventually(t, r.IsLeader, 5*time.Second, 50*time.Millisecond,
		"emerg reconciler must acquire leadership")

	// Spawn the primary-events subscriber in lock-step with Run (Plan
	// 06.6-08 main.go wiring pattern).
	subDone := make(chan struct{})
	go func() { r.SubscribePrimaryEvents(ctx); close(subDone) }()

	// Drive emerg to EmergencyActive via breaker-open trigger.
	publishBreakerEvent(t, rdb, "local-llm", "open")
	require.Eventually(t, func() bool {
		return fsm.State() == emerg.StateEmergencyActive
	}, 20*time.Second, 100*time.Millisecond,
		"emerg FSM must reach EmergencyActive before primary_ready can pre-empt it")
	require.True(t, r.IsActive(), "emerg.IsActive() must be true after EmergencyActive")

	// Now the Pitfall #11 pre-emption: publish primary_ready.
	require.NoError(t, redisx.PublishPrimaryEvent(ctx, rdb, redisx.PrimaryEvent{
		Type:        "primary_ready",
		State:       "ready",
		LifecycleID: 42,
		Reason:      "all_probes_passed",
		SinceUnix:   time.Now().Unix(),
		ReplicaID:   "test-primary-replica",
	}))

	// Within ~10s emerg should observe the primary_ready event, call
	// cancelActiveLifecycle('primary_took_over') and DestroyInstance(888).
	require.Eventually(t, func() bool {
		return fsm.State() != emerg.StateEmergencyActive &&
			mock.destroyHits.Load() >= 1
	}, 25*time.Second, 100*time.Millisecond,
		"primary_ready must drive emerg out of EmergencyActive + trigger DestroyInstance; "+
			"got emerg.FSM=%s destroyHits=%d", fsm.State(), mock.destroyHits.Load())

	// Cleanup: cancel reconciler + subscribe loops.
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("emerg.Run did not return after ctx cancel")
	}
	select {
	case <-subDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("emerg.SubscribePrimaryEvents did not return after ctx cancel")
	}
}
