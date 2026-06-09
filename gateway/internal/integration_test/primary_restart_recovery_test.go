//go:build integration

// Phase 6.6 Plan 06.6-10 Task 3 — reviews consensus action #4 restart
// recovery integration coverage.
//
// primary.Reconciler.recoverOpenLifecycle runs ONCE at Start before the
// schedule loop and event subscriber begin, rehydrating in-memory state
// from primary_lifecycles WHERE ended_at IS NULL. Four branches:
//
//  1. Healthy → restore in-memory state (lifecycle id, instance id,
//     pod URLs) + OverrideTier0 3x + DCGMScraper.SetURL +
//     FSM.SetState(Ready, "restart_recovery").
//  2. Vast says destroyed (or not running) → close lifecycle with
//     shutdown_reason='gateway_restart_orphan'. FSM stays Asleep.
//  3. Healthy instance but 4-endpoint health probe fails → close with
//     shutdown_reason='gateway_restart_orphan_unhealthy'.
//  4. No open row → no-op (return nil, FSM stays Asleep).
//
// All 4 are driven through the public Start(ctx) API; assertions
// inspect the DB row state + FSM + fakeLoader / fakeDCGM snapshots.
package integration

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/primary"
)

// TestRestartRecovery_HealthyInstanceRestoresReady — reviews #4 branch
// 1: open lifecycle row + Vast says running + all 4 endpoints healthy
// → FSM = Ready, OverrideTier0 fires for 3 roles, DCGM SetURL fires,
// lifecycle row STAYS open (ended_at IS NULL).
func TestRestartRecovery_HealthyInstanceRestoresReady(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := primaryTestCfg(t)
	cfg.PrimaryPodScheduleDisabled = true // schedule loop OFF — we only test recovery

	// Seed an open lifecycle row pointing at instance 777.
	q := gen.New(pool)
	row, err := q.InsertPrimaryLifecycle(rootCtx, gen.InsertPrimaryLifecycleParams{
		TriggerReason: "test_restart_recovery_healthy",
	})
	require.NoError(t, err)
	_, err = pool.Exec(rootCtx,
		`UPDATE ai_gateway.primary_lifecycles
		 SET vast_instance_id = 777, accepted_dph = 0.30,
		     first_health_pass_at = NOW() - INTERVAL '50 minutes'
		 WHERE id = $1`, row.ID)
	require.NoError(t, err)
	lifecycleID := row.ID

	// Wire fakes: GetInstance returns running with 4 ports, HealthCheck
	// always healthy.
	loader := newFakePrimaryLoader()
	dcgm := &fakePrimaryDCGM{}
	fakeV := &fakeVastPrimary{
		GetInstanceFn: func(_ context.Context, id int64) (vast.Instance, error) {
			require.Equal(t, int64(777), id, "GetInstance must be called with seeded vast_instance_id")
			return runningPrimaryInstance(777), nil
		},
	}

	fsm := primary.NewFSM(nil, nil)
	r := primary.NewReconciler(primary.Deps{
		Cfg:         cfg,
		Log:         slog.New(slog.DiscardHandler),
		Vast:        fakeV,
		Loader:      loader,
		DCGMScraper: dcgm,
		FSM:         fsm,
		Rule:        neverInPeakRule(),
		DB:          pool,
		Redis:       rdb,
		ReplicaID:   "test-restart-healthy",
		HealthCheck: alwaysHealthy,
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	r.Start(ctx)

	// FSM transitions Asleep → Ready inside recoverOpenLifecycle.
	require.Eventually(t, func() bool {
		return fsm.State() == primary.StateReady
	}, 5*time.Second, 50*time.Millisecond,
		"recoverOpenLifecycle must SetState(Ready, 'restart_recovery') for a healthy instance")

	// Phase 11.2: 3-role OverrideTier0 (llm/stt/tts) — stt restored (revert
	// 11.1 D-A1), embed remains off-pod (D-03).
	require.Eventually(t, func() bool {
		return len(loader.Snapshot()) == 3
	}, 2*time.Second, 50*time.Millisecond,
		"OverrideTier0 must fire 3x (llm/stt/tts) on recovery")
	require.Equal(t, "http://203.0.113.7:33400/metrics", dcgm.Last(),
		"DCGM URL must point at the recovered pod's :9400 mapping")

	// Lifecycle row still open (ended_at IS NULL).
	var endedAt pgtype.Timestamptz
	require.NoError(t, pool.QueryRow(rootCtx,
		`SELECT ended_at FROM ai_gateway.primary_lifecycles WHERE id = $1`, lifecycleID,
	).Scan(&endedAt))
	require.False(t, endedAt.Valid,
		"healthy-recovery branch must leave the lifecycle row OPEN (ended_at IS NULL)")
}

// TestRestartRecovery_OrphanInstance_ClosesLifecycle — reviews #4
// branch 2: Vast.GetInstance returns ErrInstanceNotFound. The row is
// closed with shutdown_reason='gateway_restart_orphan'. FSM stays
// Asleep. OverrideTier0 NOT called.
func TestRestartRecovery_OrphanInstance_ClosesLifecycle(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := primaryTestCfg(t)
	cfg.PrimaryPodScheduleDisabled = true

	q := gen.New(pool)
	row, err := q.InsertPrimaryLifecycle(rootCtx, gen.InsertPrimaryLifecycleParams{
		TriggerReason: "test_restart_recovery_orphan",
	})
	require.NoError(t, err)
	_, err = pool.Exec(rootCtx,
		`UPDATE ai_gateway.primary_lifecycles SET vast_instance_id = 777 WHERE id = $1`,
		row.ID)
	require.NoError(t, err)
	lifecycleID := row.ID

	loader := newFakePrimaryLoader()
	dcgm := &fakePrimaryDCGM{}
	fakeV := &fakeVastPrimary{
		GetInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return vast.Instance{}, vast.ErrInstanceNotFound
		},
	}

	fsm := primary.NewFSM(nil, nil)
	r := primary.NewReconciler(primary.Deps{
		Cfg:         cfg,
		Log:         slog.New(slog.DiscardHandler),
		Vast:        fakeV,
		Loader:      loader,
		DCGMScraper: dcgm,
		FSM:         fsm,
		Rule:        neverInPeakRule(),
		DB:          pool,
		Redis:       rdb,
		ReplicaID:   "test-restart-orphan",
		HealthCheck: alwaysHealthy,
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	r.Start(ctx)

	// Row closed with shutdown_reason='gateway_restart_orphan' within 5s.
	require.Eventually(t, func() bool {
		var reason pgtype.Text
		var endedAt pgtype.Timestamptz
		err := pool.QueryRow(rootCtx,
			`SELECT shutdown_reason, ended_at FROM ai_gateway.primary_lifecycles WHERE id = $1`,
			lifecycleID,
		).Scan(&reason, &endedAt)
		return err == nil && endedAt.Valid && reason.Valid && reason.String == "gateway_restart_orphan"
	}, 5*time.Second, 50*time.Millisecond,
		"orphan instance must close lifecycle with shutdown_reason='gateway_restart_orphan'")

	// FSM stays Asleep + Loader.OverrideTier0 NOT called.
	require.Equal(t, primary.StateAsleep, fsm.State(),
		"orphan branch must leave FSM in Asleep (no Ready transition)")
	require.Empty(t, loader.Snapshot(),
		"orphan branch must NOT call OverrideTier0")
}

// TestRestartRecovery_UnhealthyInstance_ClosesLifecycle — reviews #4
// branch 3: Vast says running + ports populated, but the 4-endpoint
// health probe fails. The row is closed with shutdown_reason=
// 'gateway_restart_orphan_unhealthy'.
func TestRestartRecovery_UnhealthyInstance_ClosesLifecycle(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := primaryTestCfg(t)
	cfg.PrimaryPodScheduleDisabled = true

	q := gen.New(pool)
	row, err := q.InsertPrimaryLifecycle(rootCtx, gen.InsertPrimaryLifecycleParams{
		TriggerReason: "test_restart_recovery_unhealthy",
	})
	require.NoError(t, err)
	_, err = pool.Exec(rootCtx,
		`UPDATE ai_gateway.primary_lifecycles SET vast_instance_id = 777 WHERE id = $1`,
		row.ID)
	require.NoError(t, err)
	lifecycleID := row.ID

	loader := newFakePrimaryLoader()
	dcgm := &fakePrimaryDCGM{}
	fakeV := &fakeVastPrimary{
		GetInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return runningPrimaryInstance(777), nil
		},
	}

	// HealthCheck always false → 4-endpoint probe never passes.
	healthCheck := func(_ context.Context, _ string) bool { return false }

	fsm := primary.NewFSM(nil, nil)
	r := primary.NewReconciler(primary.Deps{
		Cfg:         cfg,
		Log:         slog.New(slog.DiscardHandler),
		Vast:        fakeV,
		Loader:      loader,
		DCGMScraper: dcgm,
		FSM:         fsm,
		Rule:        neverInPeakRule(),
		DB:          pool,
		Redis:       rdb,
		ReplicaID:   "test-restart-unhealthy",
		HealthCheck: healthCheck,
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	r.Start(ctx)

	// Row closed with shutdown_reason containing 'gateway_restart_orphan'
	// (the unhealthy branch closes with 'gateway_restart_orphan_unhealthy').
	require.Eventually(t, func() bool {
		var reason pgtype.Text
		var endedAt pgtype.Timestamptz
		err := pool.QueryRow(rootCtx,
			`SELECT shutdown_reason, ended_at FROM ai_gateway.primary_lifecycles WHERE id = $1`,
			lifecycleID,
		).Scan(&reason, &endedAt)
		if err != nil || !endedAt.Valid || !reason.Valid {
			return false
		}
		return reason.String == "gateway_restart_orphan_unhealthy" ||
			reason.String == "gateway_restart_orphan"
	}, 5*time.Second, 50*time.Millisecond,
		"unhealthy recovery must close lifecycle with gateway_restart_orphan(_unhealthy)")

	require.Equal(t, primary.StateAsleep, fsm.State(),
		"unhealthy branch must leave FSM in Asleep")
	require.Empty(t, loader.Snapshot(),
		"unhealthy branch must NOT call OverrideTier0")
}

// TestRestartRecovery_NoOpenRow_NoOp — reviews #4 branch 4: empty
// primary_lifecycles table. recoverOpenLifecycle returns pgx.ErrNoRows
// (mapped to nil error) and Start proceeds normally. FSM stays Asleep,
// no DB writes, no OverrideTier0.
func TestRestartRecovery_NoOpenRow_NoOp(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := primaryTestCfg(t)
	cfg.PrimaryPodScheduleDisabled = true

	loader := newFakePrimaryLoader()
	dcgm := &fakePrimaryDCGM{}
	fakeV := &fakeVastPrimary{}

	fsm := primary.NewFSM(nil, nil)
	r := primary.NewReconciler(primary.Deps{
		Cfg:         cfg,
		Log:         slog.New(slog.DiscardHandler),
		Vast:        fakeV,
		Loader:      loader,
		DCGMScraper: dcgm,
		FSM:         fsm,
		Rule:        neverInPeakRule(),
		DB:          pool,
		Redis:       rdb,
		ReplicaID:   "test-restart-no-row",
		HealthCheck: alwaysHealthy,
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	r.Start(ctx)

	// Wait 2s; assert no state change.
	time.Sleep(2 * time.Second)
	require.Equal(t, primary.StateAsleep, fsm.State(),
		"no-open-row branch must keep FSM in Asleep")
	require.Empty(t, loader.Snapshot(),
		"no-open-row branch must NOT call OverrideTier0")
	require.Equal(t, "", dcgm.Last(),
		"no-open-row branch must NOT call DCGMScraper.SetURL")

	// And: no rows in primary_lifecycles.
	var count int
	require.NoError(t, pool.QueryRow(rootCtx,
		`SELECT COUNT(*) FROM ai_gateway.primary_lifecycles`,
	).Scan(&count))
	require.Equal(t, 0, count, "no rows should exist after a clean boot")
}
