package primary

// quick-261001-qdd — bid preemption detection: destroy + close "preempted" +
// no billing suppression + no blocklist + re-provision + metric.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/podconfig"
)

func preemptCount(t *testing.T, phase string) float64 {
	t.Helper()
	return testutil.ToFloat64(obs.PrimaryPreemptionsTotal.WithLabelValues(phase))
}

// bidReadyHarness — Ready-state reconciler, in-peak rule (so the schedule
// would re-provision), tracking a BID instance whose GetInstance is scripted.
func bidReadyHarness(t *testing.T, isBid bool, inst func(id int64) vast.Instance) (*Reconciler, *fakeVast, *reasonRecorder) {
	t.Helper()
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "x")
	_ = fsm.Transition(StateProvisioning, StateReady, time.Now(), "x")
	stopBlock := make(chan struct{})
	t.Cleanup(func() { close(stopBlock) })
	fv := &fakeVast{
		getInstanceFn: func(_ context.Context, id int64) (vast.Instance, error) { return inst(id), nil },
		// Re-provision goroutine blocks here so the test can observe
		// StateProvisioning deterministically.
		searchOffersFn: func(_ context.Context, _ vast.SearchFilter) ([]vast.Offer, error) {
			<-stopBlock
			return nil, errors.New("test teardown")
		},
	}
	rr := &reasonRecorder{}
	dbtx := newCloseReasonDBTX(rr)
	dbtx.queryRowFn = func(_ context.Context, sql string, _ ...interface{}) pgx.Row {
		if contains(sql, "InsertPrimaryLifecycle") {
			return insertReturningRow{id: 8, startedAt: time.Now()}
		}
		return errRow{err: errors.New("unscripted")}
	}
	r := buildReconciler(t, Deps{
		Cfg:  cfg,
		FSM:  fsm,
		Vast: fv,
		Rule: alwaysInPeakRule(),
	})
	r.SetQueriesForTest(gen.New(dbtx))
	r.activeLifecycleID.Store(7)
	r.activeInstanceID.Store(42)
	r.activeIsBid.Store(isBid)
	urls := primaryPodURLs{
		LLM:  "http://203.0.113.7:33000/v1/models",
		STT:  "http://203.0.113.7:33001/health",
		DCGM: "http://203.0.113.7:33400/metrics",
	}
	r.activePodURLs.Store(&urls)
	return r, fv, rr
}

// TestBidPreemption_ReadyPath_DestroysClosesPreemptedAndReprovisions — the
// full Ready → Draining → Destroying → Asleep → Provisioning path.
func TestBidPreemption_ReadyPath_DestroysClosesPreemptedAndReprovisions(t *testing.T) {
	r, fv, rr := bidReadyHarness(t, true, func(id int64) vast.Instance {
		// Outbid: Vast flips intended_status=stopped while actual is still
		// "running" (not IsTerminal) — only the bid path catches it.
		return vast.Instance{ID: id, ActualStatus: "running", IntendedStatus: "stopped"}
	})
	ctx := context.Background()
	startDeath := deathCount(t, "preempted")
	startPre := preemptCount(t, "ready")

	r.evaluateReady(ctx, time.Now(), testLogger())
	r.evaluateReady(ctx, time.Now(), testLogger())
	require.Equal(t, StateReady, r.deps.FSM.State(), "2 strikes must not confirm")
	r.evaluateReady(ctx, time.Now(), testLogger())
	require.Equal(t, StateDraining, r.deps.FSM.State(), "3rd strike confirms preemption → drain")

	r.evaluateDraining(ctx, time.Now(), testLogger()) // inflight 0 → Destroying
	require.Equal(t, StateDestroying, r.deps.FSM.State())
	r.evaluateDestroying(ctx, time.Now(), testLogger())
	require.Equal(t, StateAsleep, r.deps.FSM.State())

	require.Equal(t, []int64{42}, fv.destroyed(), "preempted instance must be destroyed")
	require.True(t, rr.has("preempted"), "lifecycle must close with shutdown_reason=preempted")
	require.False(t, rr.has("destroyed"), "must NOT close as plain destroyed")
	require.Nil(t, r.billingSuppressionActiveForTest(), "preemption must NOT arm billing suppression")
	require.Nil(t, r.pendingCloseReason.Load(), "close reason consumed exactly once")
	require.False(t, r.activeIsBid.Load())
	require.InDelta(t, startDeath+1, deathCount(t, "preempted"), 0.001)
	require.InDelta(t, startPre+1, preemptCount(t, "ready"), 0.001)

	// Schedule loop re-provisions on the next in-window tick (no cooldown,
	// no suppression).
	r.evaluateAsleep(ctx, time.Now(), testLogger())
	require.Equal(t, StateProvisioning, r.deps.FSM.State(), "preempted lifecycle must re-provision")
}

// TestBidPreemption_ActualStoppedAlsoDetected — actual_status=stopped (not in
// IsTerminal's set) is also a preemption signal for bid.
func TestBidPreemption_ActualStoppedAlsoDetected(t *testing.T) {
	r, _, _ := bidReadyHarness(t, true, func(id int64) vast.Instance {
		return vast.Instance{ID: id, ActualStatus: "stopped"}
	})
	ctx := context.Background()
	require.Nil(t, r.classifyDeathOnReadyTickForTest(ctx, testLogger()))
	require.Nil(t, r.classifyDeathOnReadyTickForTest(ctx, testLogger()))
	got := r.classifyDeathOnReadyTickForTest(ctx, testLogger())
	require.NotNil(t, got)
	require.Equal(t, "preempted", got.cause)
}

// TestBidPreemption_CreditMarkerStillBillingStop — zero-credit stop of a bid
// pod must still arm the billing suppression (T-qdd-05).
func TestBidPreemption_CreditMarkerStillBillingStop(t *testing.T) {
	r, _, _ := bidReadyHarness(t, true, func(id int64) vast.Instance {
		return vast.Instance{ID: id, ActualStatus: "exited", IntendedStatus: "stopped",
			StatusMsg: "instance stopped: account out of credit"}
	})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		r.evaluateReady(ctx, time.Now(), testLogger())
	}
	require.Equal(t, StateDraining, r.deps.FSM.State())
	require.NotNil(t, r.billingSuppressionActiveForTest(), "credit marker → billing_stopped suppression even for bid")
	require.Nil(t, r.pendingCloseReason.Load(), "billing stop is not a preemption")
}

// TestOnDemand_IntendedStopped_Unchanged — on-demand keeps classifyDeath:
// intended=stopped with a non-terminal actual is NOT a strike (unchanged), and
// a terminal intended=stopped is billing_stopped.
func TestOnDemand_IntendedStopped_Unchanged(t *testing.T) {
	r, _, _ := bidReadyHarness(t, false, func(id int64) vast.Instance {
		return vast.Instance{ID: id, ActualStatus: "running", IntendedStatus: "stopped"}
	})
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		require.Nil(t, r.classifyDeathOnReadyTickForTest(ctx, testLogger()),
			"on-demand Ready poll keys only on IsTerminal (unchanged)")
	}
	require.Equal(t, "billing_stopped",
		classifyDeathForMode(vast.Instance{ActualStatus: "exited", IntendedStatus: "stopped"}, false))
	require.Equal(t, "host_death",
		classifyDeathForMode(vast.Instance{ActualStatus: "exited"}, false))
	require.Equal(t, "preempted",
		classifyDeathForMode(vast.Instance{ActualStatus: "exited", IntendedStatus: "stopped"}, true))
}

// TestBidPreemption_ProvisioningPath — a bid instance stopped during cold start
// closes "preempted" (not instance_terminal_state), destroys once, files no
// machine report, and is not machine-attributable (no auto-blocklist).
func TestBidPreemption_ProvisioningPath(t *testing.T) {
	withTestPollInterval(t, 2*time.Millisecond)
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return vast.Instance{ID: 42, ActualStatus: "loading", IntendedStatus: "stopped"}, nil
		},
	}
	rr := &reasonRecorder{}
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Rule: alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(newCloseReasonDBTX(rr)))
	r.activeLifecycleID.Store(99)
	r.activeIsBid.Store(true)
	setProvisionCfgForTest(r, podconfig.PodConfig{CreatedBudgetS: 60, ColdStartBudgetS: 30, PortBindBudgetS: 600})
	startPre := preemptCount(t, "provisioning")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reason, err := r.waitForReadyOrDestroy(ctx, 99, 42, 0.30, testLogger())

	require.Error(t, err)
	require.Equal(t, "preempted", reason)
	require.Equal(t, "preempted", errReason(err))
	require.True(t, rr.has("preempted"))
	require.False(t, rr.has("instance_terminal_state"))
	require.Equal(t, int32(1), fakeV.destroyCalls.Load())
	require.Empty(t, fakeV.reportedProblems(), "a lost bid is not a host defect")
	require.False(t, machineAttributableReason("preempted"), "preempted must never auto-blocklist the machine")
	require.InDelta(t, startPre+1, preemptCount(t, "provisioning"), 0.001)
}

// TestBidPreemption_ProvisioningCreditMarker_StaysTerminal — a credit-marker
// stop during a bid cold start keeps the legacy instance_terminal_state close.
func TestBidPreemption_ProvisioningCreditMarker_StaysTerminal(t *testing.T) {
	withTestPollInterval(t, 2*time.Millisecond)
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return vast.Instance{ID: 42, ActualStatus: "exited", IntendedStatus: "stopped", StatusMsg: "out of credit"}, nil
		},
	}
	rr := &reasonRecorder{}
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Rule: alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(newCloseReasonDBTX(rr)))
	r.activeLifecycleID.Store(99)
	r.activeIsBid.Store(true)
	setProvisionCfgForTest(r, podconfig.PodConfig{CreatedBudgetS: 60, ColdStartBudgetS: 30, PortBindBudgetS: 600})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reason, err := r.waitForReadyOrDestroy(ctx, 99, 42, 0.30, testLogger())
	require.Error(t, err)
	require.Equal(t, "instance_terminal_state", reason)
}

// bidOpenLifecycleRow extends openLifecycleRow with is_bid (column 14).
type bidOpenLifecycleRow struct {
	openLifecycleRow
	isBid pgtype.Bool
}

func (r bidOpenLifecycleRow) Scan(dest ...interface{}) error {
	if err := r.openLifecycleRow.Scan(dest...); err != nil {
		return err
	}
	if len(dest) < 15 {
		return errors.New("bidOpenLifecycleRow: expected 15 dest pointers")
	}
	if p, ok := dest[13].(*pgtype.Bool); ok {
		*p = r.isBid
	}
	return nil
}

// TestRecoverOpenLifecycle_RestoresIsBid — a gateway restart mid bid
// lifecycle restores activeIsBid so a later preemption is still classified.
func TestRecoverOpenLifecycle_RestoresIsBid(t *testing.T) {
	for _, tc := range []struct {
		name string
		col  pgtype.Bool
		want bool
	}{
		{"bid", pgtype.Bool{Bool: true, Valid: true}, true},
		{"ondemand", pgtype.Bool{Bool: false, Valid: true}, false},
		{"legacy_null", pgtype.Bool{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbtx := &fakeDBTX{}
			dbtx.queryRowFn = func(_ context.Context, sql string, _ ...interface{}) pgx.Row {
				if contains(sql, "ended_at IS NULL") {
					return bidOpenLifecycleRow{
						openLifecycleRow: openLifecycleRow{id: 100, vastInstanceID: pgtype.Int8{Int64: 42, Valid: true}},
						isBid:            tc.col,
					}
				}
				return errRow{err: errors.New("unexpected query")}
			}
			r := buildReconciler(t, Deps{
				Cfg:         testCfg(t),
				FSM:         NewFSM(nil, nil),
				Loader:      newFakeLoader(),
				DCGMScraper: &fakeDCGMScraper{},
				HealthCheck: func(_ context.Context, _ string) bool { return true },
				Vast: &fakeVast{getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
					return runningInstanceWithAllPorts(42), nil
				}},
				Rule: alwaysInPeakRule(),
			})
			r.SetQueriesForTest(gen.New(dbtx))
			if !tc.want {
				r.activeIsBid.Store(true) // must be overwritten to false
			}
			require.NoError(t, r.recoverOpenLifecycle(context.Background()))
			require.Equal(t, tc.want, r.activeIsBid.Load())
		})
	}
}
