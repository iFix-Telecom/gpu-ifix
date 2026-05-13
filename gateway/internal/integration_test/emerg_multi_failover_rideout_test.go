//go:build integration

// Phase 6 Plan 06-08 — D-C4 multi-failover ride-out integration test.
//
// Once the FSM is in EmergencyActive, additional local-llm OPEN events
// MUST NOT spawn new lifecycles. The partial unique index
// `emergency_live_singleton` is the DB-layer safety net; this test
// asserts the reconciler's leader-side noise gate prevents the lifecycle
// row count from growing AND prevents the FSM from regressing during
// chatty failover scenarios.
//
// Scenario: drive HEALTHY → EmergencyActive (1 lifecycle row created).
// Publish a SECOND OPEN event. Wait 3s (well beyond ProvisionTriggerFailedOverSeconds=1).
// Assert the lifecycle row count is UNCHANGED at 1. FSM remains
// EmergencyActive throughout.
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
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// TestEmergMultiFailoverRideOut — D-C4. The cutback path should NOT
// fire either (we never publish CLOSED), so the FSM stays in
// EmergencyActive for the duration of the test.
func TestEmergMultiFailoverRideOut(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)
	// Bump idle grace for this test so evaluateRecovering does NOT fire
	// during the wait window. The test asserts ride-out, not cutback.
	cfg.ProvisionIdleGraceSeconds = 60
	cfg.ProvisionHealthyDurationSeconds = 60

	loader := upstreams.NewLoaderInMemory(
		upstreams.UpstreamConfig{
			Name: "local-llm", Role: "llm", Tier: 0, URL: "http://primary:8000",
			Enabled: true,
		},
	)

	mock := newMockVastServer(t)
	offers := []vast.Offer{{
		ID: 9001, DphTotal: 0.35, GpuName: "RTX 4090", Reliability: 0.99,
		HostID: 100, MachineID: 50, NumGpus: 1,
	}}
	mock.searchResponse.Store(&offers)
	inst := vast.Instance{
		ID: 12345, ActualStatus: "running", PublicIPAddr: "127.0.0.1",
		Ports: map[string][]vast.PortBinding{
			"9100/tcp": {{HostIP: "0.0.0.0", HostPort: "40713"}},
		},
		HostID: 100, DphTotal: 0.35,
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
		Loader:       loader,
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

	// Drive to EmergencyActive.
	publishBreakerEvent(t, rdb, "local-llm", "open")
	if !waitFor(t, 15*time.Second, 100*time.Millisecond, func() bool {
		return fsm.State() == emerg.StateEmergencyActive
	}) {
		t.Fatalf("FSM did not reach EmergencyActive within 15s; got %s", fsm.State())
	}

	// Capture the baseline lifecycle row count (should be 1).
	var beforeCount int
	require.NoError(t, pool.QueryRow(rootCtx,
		`SELECT COUNT(*) FROM ai_gateway.emergency_lifecycles`).Scan(&beforeCount))
	require.Equal(t, 1, beforeCount,
		"baseline: exactly 1 lifecycle row should exist after first failover")

	// Publish a SECOND OPEN event (multi-failover scenario). This MUST
	// NOT spawn a new lifecycle.
	publishBreakerEvent(t, rdb, "local-llm", "open")

	// Wait 3s — well beyond the trigger threshold (1s in test cfg).
	// During this window, evaluateEmergencyActive should observe the
	// non-CLOSED tracker state and ride out (Debug log, no action).
	time.Sleep(3 * time.Second)

	var afterCount int
	require.NoError(t, pool.QueryRow(rootCtx,
		`SELECT COUNT(*) FROM ai_gateway.emergency_lifecycles`).Scan(&afterCount))
	require.Equal(t, beforeCount, afterCount,
		"D-C4 ride-out violated: lifecycle row count grew from %d to %d during multi-failover",
		beforeCount, afterCount)

	// FSM must still be in EmergencyActive — ride-out does NOT regress.
	require.Equal(t, emerg.StateEmergencyActive, fsm.State(),
		"FSM regressed from EmergencyActive during multi-failover ride-out; got %s",
		fsm.State())

	// CreateInstance was called exactly once (no duplicate provision).
	require.Equal(t, int64(1), mock.createHits.Load(),
		"D-C4: CreateInstance must be called exactly once even with sustained multi-failover")

	// DestroyInstance was NEVER called (cutback didn't fire).
	require.Equal(t, int64(0), mock.destroyHits.Load(),
		"DestroyInstance must NOT be called during ride-out")

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}
