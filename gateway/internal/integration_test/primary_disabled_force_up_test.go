//go:build integration

// Phase 6.6 Plan 06.6-10 Task 1 — reviews consensus action #2
// DISABLED + force_up_request integration coverage.
//
// Reviews #2 semantic resolution: PRIMARY_POD_SCHEDULE_DISABLED=true is
// a SCHEDULE-LOOP kill-switch (schedule ticks skip evaluateTick) but the
// event subscriber goroutine MUST still run so operators can publish
// force_up_request via gatewayctl during the soak-gate phase. Without
// this property the gatewayctl `primary force-up` command would publish
// to a channel with no live consumer.
//
// Two tests:
//
//   - TestPrimaryDisabled_ForceUpRequest_Provisions: DISABLED=true; start
//     reconciler; publish force_up_request via Pub/Sub; assert FSM
//     transitions Asleep → Provisioning within 5s. Proves event
//     subscriber is alive AND force-up bypasses the schedule gate.
//
//   - TestPrimaryDisabled_NoEvent_StaysAsleep: DISABLED=true; rule
//     would normally trigger ShouldBeProvisioned=true; start reconciler;
//     wait 10s; assert FSM stays Asleep. Proves the schedule tick gate
//     short-circuits at the per-tick decision point.
package integration

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/primary"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// TestPrimaryDisabled_ForceUpRequest_Provisions — reviews #2 part 1:
// DISABLED + operator force_up_request must still drive Asleep →
// Provisioning. Proves the event subscriber goroutine is spawned
// regardless of DISABLED and that handleForceUpRequest bypasses the
// schedule gate.
func TestPrimaryDisabled_ForceUpRequest_Provisions(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := primaryTestCfg(t)
	// Soak-gate posture: schedule loop is OFF but the event subscriber
	// MUST still run.
	cfg.PrimaryPodScheduleDisabled = true
	cfg.PrimaryProvisionFailureCooldownSeconds = 0

	// Fake Vast: SearchOffers returns 1 cheap offer; CreateInstance
	// succeeds; GetInstance returns running with all 4 ports populated
	// (so the spawn provisioning goroutine can advance past CreateInstance
	// without blocking).
	fakeV := &fakeVastPrimary{
		SearchOffersFn: func(_ context.Context, _ vast.SearchFilter) ([]vast.Offer, error) {
			return []vast.Offer{{ID: 1, DphTotal: 0.30, GpuName: "RTX 4090",
				Reliability: 0.99, NumGpus: 1, HostID: 100}}, nil
		},
		CreateInstanceFn: func(_ context.Context, _ int64, _ vast.CreateRequest) (vast.Instance, error) {
			return vast.Instance{ID: 999}, nil
		},
		GetInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return runningPrimaryInstance(999), nil
		},
	}

	fsm := primary.NewFSM(nil, nil)
	r := primary.NewReconciler(primary.Deps{
		Cfg:         cfg,
		Log:         slog.New(slog.DiscardHandler),
		Vast:        fakeV,
		Loader:      newFakePrimaryLoader(),
		DCGMScraper: &fakePrimaryDCGM{},
		FSM:         fsm,
		Rule:        alwaysInPeakRule(),
		DB:          pool,
		Redis:       rdb,
		ReplicaID:   "test-disabled-force-up",
		HealthCheck: alwaysHealthy,
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	r.Start(ctx)

	// Wait for leadership before publishing — the event handler is
	// leader-gated.
	require.Eventually(t, r.IsLeader, 5*time.Second, 100*time.Millisecond,
		"reconciler must acquire leadership before force-up bypass can fire")

	// Publish force_up_request via the canonical Pub/Sub channel.
	require.NoError(t, redisx.PublishPrimaryEvent(ctx, rdb, redisx.PrimaryEvent{
		Type:      "force_up_request",
		Reason:    "reviews_2_uat",
		SinceUnix: time.Now().Unix(),
		ReplicaID: "test-publisher",
	}))

	// FSM transitions Asleep → Provisioning within 5s. Proves event
	// subscriber + handleForceUpRequest fired regardless of DISABLED.
	require.Eventually(t, func() bool {
		return fsm.State() == primary.StateProvisioning || fsm.State() == primary.StateReady
	}, 6*time.Second, 100*time.Millisecond,
		"DISABLED + force_up_request must drive FSM Asleep→Provisioning; got %s", fsm.State())
}

// TestPrimaryDisabled_NoEvent_StaysAsleep — reviews #2 part 2: DISABLED
// + schedule rule that WOULD trigger ShouldBeProvisioned=true must keep
// the FSM in Asleep. Proves the schedule tick gate short-circuits at the
// per-tick decision point.
func TestPrimaryDisabled_NoEvent_StaysAsleep(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := primaryTestCfg(t)
	cfg.PrimaryPodScheduleDisabled = true

	fsm := primary.NewFSM(nil, nil)
	r := primary.NewReconciler(primary.Deps{
		Cfg:         cfg,
		Log:         slog.New(slog.DiscardHandler),
		Vast:        &fakeVastPrimary{},
		Loader:      newFakePrimaryLoader(),
		DCGMScraper: &fakePrimaryDCGM{},
		FSM:         fsm,
		Rule:        alwaysInPeakRule(),
		DB:          pool,
		Redis:       rdb,
		ReplicaID:   "test-disabled-stays-asleep",
		HealthCheck: alwaysHealthy,
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	r.Start(ctx)

	// Wait for leadership so we know the schedule loop is actively
	// ticking — and observe that it skips evaluateTick because DISABLED.
	require.Eventually(t, r.IsLeader, 5*time.Second, 100*time.Millisecond,
		"reconciler must acquire leadership before we can verify schedule-loop gate")

	// Sleep 8s: the schedule loop fires 8 ticks. Every tick MUST observe
	// PrimaryPodScheduleDisabled=true and short-circuit BEFORE
	// evaluateTick. FSM must NEVER leave Asleep.
	time.Sleep(8 * time.Second)
	require.Equal(t, primary.StateAsleep, fsm.State(),
		"DISABLED schedule loop must NOT advance FSM past Asleep")

	// And: zero rows in primary_lifecycles — spawnProvisioning never fired.
	var count int
	require.NoError(t, pool.QueryRow(rootCtx,
		`SELECT COUNT(*) FROM ai_gateway.primary_lifecycles`,
	).Scan(&count))
	require.Equal(t, 0, count,
		"DISABLED + no force-up event must NOT create any lifecycle rows")
}
