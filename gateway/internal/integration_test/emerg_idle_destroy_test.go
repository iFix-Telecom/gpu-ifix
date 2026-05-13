//go:build integration

// Phase 6 Plan 06-08 — D-D1 idle-grace destroy integration test
// (PRV-08 / SC-4). Continues the cutback test scenario: after the FSM
// transitions Active → Recovering, no traffic flows to the emergency
// pod for at least ProvisionIdleGraceSeconds (= 1s in test cfg). The
// reconciler MUST:
//
//   - call vast.DestroyInstance (mockVast.destroyHits.Add(1))
//   - close the lifecycle row with shutdown_reason='cutback_idle'
//   - transition FSM Recovering → Cooldown
//   - eventually transition Cooldown → Healthy (after another
//     ProvisionHealthyDurationSeconds, so the trigger can re-arm)
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

// TestEmergIdleDestroy — drives the full cutback → idle destroy →
// cooldown → healthy transition chain. Total budget ~10s wall time.
func TestEmergIdleDestroy(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

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

	// Drive HEALTHY → EmergencyActive.
	publishBreakerEvent(t, rdb, "local-llm", "open")
	if !waitFor(t, 15*time.Second, 100*time.Millisecond, func() bool {
		return fsm.State() == emerg.StateEmergencyActive
	}) {
		t.Fatalf("FSM did not reach EmergencyActive within 15s; got %s", fsm.State())
	}

	// Capture the lifecycle ID for later assertion.
	var lifecycleID int64
	require.NoError(t, pool.QueryRow(rootCtx,
		`SELECT id FROM ai_gateway.emergency_lifecycles WHERE ended_at IS NULL`,
	).Scan(&lifecycleID))

	// Trigger cutback via sustained CLOSED.
	publishBreakerEvent(t, rdb, "local-llm", "closed")

	// Cutback should land in Recovering within ~2s.
	if !waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		return fsm.State() == emerg.StateRecovering
	}) {
		t.Fatalf("FSM did not transition to Recovering within 5s; got %s", fsm.State())
	}

	// Idle grace is 1s in test cfg. Do NOT register any traffic. The
	// reconciler should observe IsIdle=true and call destroyAndCloseLifecycle.
	// Budget 5s for the destroy + close + FSM transition to Cooldown.
	if !waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		return fsm.State() == emerg.StateCooldown && mock.destroyHits.Load() >= 1
	}) {
		t.Fatalf("FSM did not transition to Cooldown after idle grace within 5s; "+
			"got fsm=%s destroyHits=%d", fsm.State(), mock.destroyHits.Load())
	}

	// Verify destroyAndCloseLifecycle ran:
	//   1. mockVast.destroyHits == 1 (at least)
	//   2. lifecycle row closed with shutdown_reason='cutback_idle'
	require.GreaterOrEqual(t, mock.destroyHits.Load(), int64(1),
		"DestroyInstance must be called during cutback idle-grace destroy")

	var reason pgtype.Text
	var endedAt pgtype.Timestamptz
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT shutdown_reason, ended_at FROM ai_gateway.emergency_lifecycles WHERE id = $1`,
		lifecycleID).Scan(&reason, &endedAt))
	require.True(t, endedAt.Valid, "ended_at must be set after destroyAndCloseLifecycle")
	require.True(t, reason.Valid)
	require.Equal(t, "cutback_idle", reason.String,
		"shutdown_reason must be cutback_idle (D-D1 idle-grace destroy)")

	// activeLifecycle pointer must be cleared.
	require.False(t, r.IsActive(),
		"reconciler.IsActive() must be false after destroyAndCloseLifecycle")

	// Cooldown → Healthy after another HealthyDurationSeconds (= 1s).
	// Budget 5s.
	if !waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		return fsm.State() == emerg.StateHealthy
	}) {
		t.Fatalf("FSM did not return to Healthy after cooldown within 5s; got %s", fsm.State())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}
