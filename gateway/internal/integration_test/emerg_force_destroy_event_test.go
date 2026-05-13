//go:build integration

// Phase 6 Plan 06-08 — BLOCKER 2 cross-plan deferred test from Plan 06-05.
//
// TestEmergReconcilerHandlesForceDestroyEvent proves the end-to-end path
// from gatewayctl `emerg force-destroy` (Plan 06-10) → Pub/Sub
// gw:emerg:events → Reconciler.applyEmergCommand → handleForceDestroy →
// destroyAndCloseLifecycle (the Plan 06-08 helper introduced in this
// plan).
//
// Originally deferred from Plan 06-05 because destroyAndCloseLifecycle
// was a logging-only stub at that time. This test is the integration
// evidence that the FUNCTIONAL helper actually destroys + closes the
// lifecycle row + transitions FSM to Cooldown.
//
// Setup: drive the reconciler from HEALTHY → EmergencyActive (real
// provision happy path). Publish force_destroy_request via
// redisx.PublishEmergEvent. Assert:
//
//   - mockVast.destroyHits >= 1 (DestroyInstance called)
//   - DB row closed with shutdown_reason='manual'
//   - FSM == StateCooldown (handleForceDestroy transitions there)
//   - Loader override cleared (RestoreTier0)
//
// All within ~10s budget.
package integration

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// TestEmergReconcilerHandlesForceDestroyEvent — BLOCKER 2 cross-plan
// integration: Plan 06-05 publishes the event, Plan 06-08 ships the
// destroyAndCloseLifecycle helper. End-to-end evidence the two plans
// integrate cleanly.
func TestEmergReconcilerHandlesForceDestroyEvent(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)
	// Bump cutback timing so the test does NOT race the natural cutback
	// path (we want force-destroy to be the one driving the close).
	cfg.ProvisionHealthyDurationSeconds = 60
	cfg.ProvisionIdleGraceSeconds = 60

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

	// Capture lifecycle ID + sanity-check Loader override is active.
	var lifecycleID int64
	require.NoError(t, pool.QueryRow(rootCtx,
		`SELECT id FROM ai_gateway.emergency_lifecycles WHERE ended_at IS NULL`,
	).Scan(&lifecycleID))

	t0Active, _ := loader.Resolve("llm", 0)
	require.True(t, t0Active.IsEmergency,
		"sanity: override must be active before force-destroy")

	// Publish force_destroy_request — gatewayctl-style.
	require.NoError(t, redisx.PublishEmergEvent(ctx, rdb, redisx.EmergEvent{
		Type:      "force_destroy_request",
		Reason:    "operator-initiated",
		ReplicaID: "gatewayctl",
		SinceUnix: time.Now().Unix(),
	}))

	// Wait for the destroy + close + FSM transition. Budget 5s.
	if !waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		return fsm.State() == emerg.StateCooldown && mock.destroyHits.Load() >= 1
	}) {
		t.Fatalf("force-destroy did not complete within 5s; "+
			"got fsm=%s destroyHits=%d", fsm.State(), mock.destroyHits.Load())
	}

	// Assert: DestroyInstance was called.
	require.GreaterOrEqual(t, mock.destroyHits.Load(), int64(1),
		"force-destroy must call DestroyInstance")

	// Assert: lifecycle row closed with shutdown_reason='manual'.
	var reason pgtype.Text
	var endedAt pgtype.Timestamptz
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT shutdown_reason, ended_at FROM ai_gateway.emergency_lifecycles WHERE id = $1`,
		lifecycleID).Scan(&reason, &endedAt))
	require.True(t, endedAt.Valid, "ended_at must be set after force-destroy")
	require.True(t, reason.Valid)
	require.Equal(t, "manual", reason.String,
		"force-destroy shutdown_reason must be 'manual'")

	// Assert: activeLifecycle pointer cleared.
	require.False(t, r.IsActive(),
		"reconciler.IsActive() must be false after force-destroy")

	// Assert: Loader override cleared (defensive RestoreTier0 in
	// closeLifecycle + the explicit RestoreTier0 in destroyAndCloseLifecycle).
	t0Restored, _ := loader.Resolve("llm", 0)
	require.False(t, t0Restored.IsEmergency,
		"after force-destroy, Loader override must be cleared")
	require.Equal(t, "local-llm", t0Restored.Name)

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}
