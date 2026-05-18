//go:build integration

// Phase 6.6 Plan 06.6-10 Task 2 — D-C3 triple-layer cancel-in-flight
// coverage for the primary reconciler.
//
// cancelActiveLifecycle has three layers (parity emerg.cancelActiveLifecycle):
//
//   - Layer 1: ctx.Done() — the provisioning goroutine's lifecycleCancel
//     is invoked, so waitForReadyOrDestroy exits via its ctx.Done branch
//     and the in-flight lifecycle row is closed with shutdown_reason=
//     'cancelled_in_flight'.
//
//   - Layer 2: Pub/Sub broadcast — a PrimaryEvent{Type:"cancel_in_flight"}
//     is published on gw:primary:events so any cross-replica subscriber
//     observes the cancellation.
//
//   - Layer 3: Vast destroy — vastutil.BestEffortDestroy fires with a
//     FRESH context.Background (Pitfall 8 — the parent ctx may already
//     be cancelled).
//
// The test drives the cancel via `force_down_request` Pub/Sub event
// landed while FSM is in StateProvisioning. handleForceDownRequest
// dispatches to cancelActiveLifecycle in the Provisioning branch.
//
// Per WARNING 4 closure: ScheduleRule mutation is performed via the
// test-only export_test.go SetScheduleRuleForTest helper IF the test
// needs to drive Asleep→Provisioning under a "should provision" rule
// and then close the window. This test uses the simpler force-down
// path so SetScheduleRuleForTest is not strictly required, but the
// import is exercised by primary_overnight_schedule_test.go.
package integration

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/primary"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// TestPrimaryCancelInflight_TripleLayer — D-C3 cancel-in-flight triple-
// layer coverage. Drives the reconciler from Asleep → Provisioning via
// the schedule loop, then publishes a force_down_request and asserts all
// 3 layers fire.
func TestPrimaryCancelInflight_TripleLayer(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := primaryTestCfg(t)
	cfg.PrimaryPodScheduleDisabled = false
	// Generous budget so the cancel beats the deadline path.
	cfg.PrimaryProvisionColdStartBudgetSeconds = 60

	// Subscribe BEFORE Start so the Layer-2 Pub/Sub event is not lost.
	ps := redisx.SubscribePrimaryEvents(rootCtx, rdb)
	t.Cleanup(func() { _ = ps.Close() })
	pubsubCh := ps.Channel()

	fakeV := &fakeVastPrimary{
		SearchOffersFn: func(_ context.Context, _ vast.SearchFilter) ([]vast.Offer, error) {
			return []vast.Offer{{ID: 1, DphTotal: 0.30, GpuName: "RTX 4090",
				Reliability: 0.99, NumGpus: 1, HostID: 100}}, nil
		},
		CreateInstanceFn: func(_ context.Context, _ int64, _ vast.CreateRequest) (vast.Instance, error) {
			return vast.Instance{ID: 999}, nil
		},
		// GetInstance returns running with all 4 ports populated, but
		// HealthCheck always false keeps waitForReadyOrDestroy in its
		// poll loop — perfect for catching the cancel mid-provision.
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
		ReplicaID:   "test-cancel-inflight",
		HealthCheck: func(_ context.Context, _ string) bool { return false },
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	r.Start(ctx)

	// Wait for the FSM to reach Provisioning via the schedule loop.
	require.Eventually(t, func() bool {
		return fsm.State() == primary.StateProvisioning
	}, 6*time.Second, 100*time.Millisecond,
		"FSM must reach Provisioning via the schedule loop before force-down can cancel it")

	// Wait until the spawnProvisioning goroutine has run far enough for
	// CreateInstance to be called (instance 999 alive on Vast — Layer 3
	// has something to destroy).
	require.Eventually(t, func() bool {
		var n int
		err := pool.QueryRow(rootCtx,
			`SELECT COUNT(*) FROM ai_gateway.primary_lifecycles
			 WHERE vast_instance_id = 999`,
		).Scan(&n)
		return err == nil && n == 1
	}, 10*time.Second, 100*time.Millisecond,
		"primary_lifecycles row must record vast_instance_id=999 before force-down")

	// Publish force_down_request via gw:primary:events Pub/Sub. The
	// reconciler's event subscriber leader-gates and dispatches to
	// handleForceDownRequest → cancelActiveLifecycle.
	require.NoError(t, redisx.PublishPrimaryEvent(ctx, rdb, redisx.PrimaryEvent{
		Type:      "force_down_request",
		Reason:    "test_triple_layer",
		SinceUnix: time.Now().Unix(),
		ReplicaID: "test-publisher",
	}))

	// Drain Pub/Sub looking for a cancel_in_flight event (Layer 2).
	// Allow up to 5s for the leader to consume the force_down + publish.
	cancelDeadline := time.Now().Add(8 * time.Second)
	var sawCancelInflight bool
loop:
	for time.Now().Before(cancelDeadline) {
		select {
		case msg, ok := <-pubsubCh:
			if !ok {
				break loop
			}
			var ev redisx.PrimaryEvent
			if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
				continue
			}
			if ev.Type == "cancel_in_flight" {
				sawCancelInflight = true
				// Layer 2 payload sanity: cancel_in_flight events MUST
				// reference the active lifecycle and the publishing
				// replica.
				require.NotZero(t, ev.LifecycleID,
					"cancel_in_flight event must carry a non-zero lifecycle_id")
				require.NotEmpty(t, ev.ReplicaID,
					"cancel_in_flight event must carry the publishing replica ID")
				break loop
			}
		case <-time.After(8 * time.Second):
			break loop
		}
	}

	require.True(t, sawCancelInflight,
		"Layer 2: must observe PrimaryEvent{Type:'cancel_in_flight'} on gw:primary:events after force_down_request")

	// Layer 3: Vast.DestroyInstance(999) fired.
	require.Eventually(t, func() bool {
		return fakeV.HasDestroyed(999)
	}, 5*time.Second, 50*time.Millisecond,
		"Layer 3: fakeVast must record DestroyInstance(999) after cancelActiveLifecycle")

	// Layer 1: the in-flight provisioning goroutine observed ctx.Done()
	// and closed the lifecycle row with shutdown_reason='cancelled_in_flight'.
	require.Eventually(t, func() bool {
		var reason pgtype.Text
		err := pool.QueryRow(rootCtx,
			`SELECT shutdown_reason FROM ai_gateway.primary_lifecycles
			 WHERE vast_instance_id = 999 AND ended_at IS NOT NULL`,
		).Scan(&reason)
		return err == nil && reason.Valid && reason.String == "cancelled_in_flight"
	}, 10*time.Second, 100*time.Millisecond,
		"Layer 1: primary_lifecycles row must close with shutdown_reason='cancelled_in_flight' after ctx.Done() fires")
}
