//go:build integration

// Phase 6 Plan 06-08 — D-D1 cutback integration test (PRV-08 / SC-4).
//
// Drives the reconciler from HEALTHY → EmergencyProvisioning →
// EmergencyActive (using the same mock vast pattern as Plan 06-06's
// happy-path test) THEN publishes a sustained local-llm CLOSED event.
// With ProvisionHealthyDurationSeconds=1 (defaultTestCfg accelerated per
// RESEARCH Pitfall 13), the FSM should transition Active → Recovering
// within ~2s AND the upstreams.Loader tier-0 override must be cleared
// (Resolve(llm,0).URL == primary, IsEmergency == false).
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

// TestEmergCutback — D-D1: sustained local-llm CLOSED for
// ProvisionHealthyDurationSeconds (= 1s in test cfg) MUST trigger:
//
//   - dispatcher.Loader.RestoreTier0("llm") (override cleared)
//   - FSM EmergencyActive → Recovering
//   - r.lastEmergencyTrafficAt updated to NOW (idle-grace baseline)
//
// The pod is NOT destroyed yet — that happens in TestEmergIdleDestroy
// after the additional ProvisionIdleGraceSeconds elapse.
func TestEmergCutback(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

	// Build a real Loader with local-llm + openrouter-chat upstreams so
	// the cutback can verify Resolve returns the primary post-restore.
	loader := upstreams.NewLoaderInMemory(
		upstreams.UpstreamConfig{
			Name: "local-llm", Role: "llm", Tier: 0, URL: "http://primary:8000",
			Enabled: true,
		},
		upstreams.UpstreamConfig{
			Name: "openrouter-chat", Role: "llm", Tier: 1,
			URL: "https://openrouter.example/v1", Enabled: true,
		},
	)

	// Set up the standard mock vast happy path — SearchOffers + CreateInstance
	// + GetInstance running with populated ports.
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

	// 1. Drive HEALTHY → EmergencyProvisioning → EmergencyActive.
	publishBreakerEvent(t, rdb, "local-llm", "open")
	if !waitFor(t, 15*time.Second, 100*time.Millisecond, func() bool {
		return fsm.State() == emerg.StateEmergencyActive
	}) {
		t.Fatalf("FSM did not reach EmergencyActive within 15s; got %s", fsm.State())
	}

	// 2. Verify Loader.OverrideTier0 was activated by markHealthy.
	t0Active, _ := loader.Resolve("llm", 0)
	require.True(t, t0Active.IsEmergency,
		"after markHealthy, Resolve(llm,0).IsEmergency must be true")
	require.Equal(t, "emergency_pod_llm", t0Active.Name)
	require.Equal(t, "http://127.0.0.1:40713", t0Active.URL,
		"override URL must be the pod base URL (no /health suffix)")

	// 3. Publish sustained CLOSED — local-llm has recovered.
	publishBreakerEvent(t, rdb, "local-llm", "closed")

	// 4. Wait for cutback: HealthyDurationSeconds=1 + propagation.
	//    Budget 5s (plenty of headroom; tracker observes within tick cadence).
	if !waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		return fsm.State() == emerg.StateRecovering
	}) {
		t.Fatalf("FSM did not transition to Recovering within 5s; got %s", fsm.State())
	}

	// 5. Verify Loader.RestoreTier0 cleared the override.
	t0Restored, _ := loader.Resolve("llm", 0)
	require.False(t, t0Restored.IsEmergency,
		"after cutback, Resolve(llm,0).IsEmergency must be false (RestoreTier0 fired)")
	require.Equal(t, "local-llm", t0Restored.Name)
	require.Equal(t, "http://primary:8000", t0Restored.URL,
		"after cutback, primary URL must be restored")

	// 6. Pod is NOT yet destroyed — that's evaluateRecovering's job.
	require.Equal(t, int64(0), mock.destroyHits.Load(),
		"cutback alone must NOT destroy the pod (evaluateRecovering owns destroy)")

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}
