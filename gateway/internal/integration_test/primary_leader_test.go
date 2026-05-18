//go:build integration

// Phase 6.6 Plan 06.6-10 Task 1 — leader-election PRV-03 invariant
// (parity emerg_leader_test).
//
// Proves the redsync v4 distributed mutex gw:primary:lock (TTL 30s,
// renew 10s = 1/3 TTL) enforces single-leader semantics across two
// reconciler goroutines sharing the same Redis. Two replicas may run
// side-by-side but ONLY ONE may be in the "leader" state at any given
// moment — the others observe events via Pub/Sub but do NOT mutate FSM.
//
// Setup: two primary.Reconciler instances + two FSMs + ONE shared Redis
// (miniredis via freshSchema) + ONE shared Postgres pool (testcontainers).
// Each replica gets its own ReplicaID. Start both. Within 5s assert
// exactly 1 IsLeader is true.
//
// Failover sub-scenario: cancel the leader's context. The lock is
// released via redsync.Unlock with a SEPARATE context.Background (Pitfall
// 8). The surviving replica's next mutex.LockContext call (within ~1s)
// succeeds and IsLeader flips.
package integration

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/primary"
)

// TestPrimaryLeader_OnlyOneActiveLeader proves the gw:primary:lock
// distributed mutex enforces exactly-one-leader semantics across 2
// replicas sharing the same Redis.
func TestPrimaryLeader_OnlyOneActiveLeader(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := primaryTestCfg(t)
	// Soak-gate ON — schedule loop is leader-gated regardless of DISABLED.
	cfg.PrimaryPodScheduleDisabled = true

	// Two FSMs, two reconcilers, ONE shared Redis. Different ReplicaIDs
	// so logs / Pub/Sub events can be attributed.
	fsm1 := primary.NewFSM(nil, nil)
	fsm2 := primary.NewFSM(nil, nil)

	ctx1, cancel1 := context.WithCancel(rootCtx)
	ctx2, cancel2 := context.WithCancel(rootCtx)
	defer cancel1()
	defer cancel2()

	r1 := primary.NewReconciler(primary.Deps{
		Cfg:         cfg,
		Log:         slog.New(slog.DiscardHandler),
		FSM:         fsm1,
		Rule:        alwaysInPeakRule(),
		DB:          pool,
		Redis:       rdb,
		ReplicaID:   "replica-1",
		Vast:        &fakeVastPrimary{},
		HealthCheck: alwaysHealthy,
	})
	r2 := primary.NewReconciler(primary.Deps{
		Cfg:         cfg,
		Log:         slog.New(slog.DiscardHandler),
		FSM:         fsm2,
		Rule:        alwaysInPeakRule(),
		DB:          pool,
		Redis:       rdb,
		ReplicaID:   "replica-2",
		Vast:        &fakeVastPrimary{},
		HealthCheck: alwaysHealthy,
	})

	r1.Start(ctx1)
	r2.Start(ctx2)

	// Exactly 1 leader within 5s — primary loop has a 1Hz ticker so the
	// first tick fires at ~1s; allow generous slack.
	if !waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		return r1.IsLeader() != r2.IsLeader()
	}) {
		t.Fatalf("expected exactly 1 leader within 5s; r1.IsLeader=%v r2.IsLeader=%v",
			r1.IsLeader(), r2.IsLeader())
	}

	// Identify leader/follower for the failover phase.
	var leader, follower *primary.Reconciler
	var leaderCancel context.CancelFunc
	if r1.IsLeader() {
		leader, follower = r1, r2
		leaderCancel = cancel1
	} else {
		leader, follower = r2, r1
		leaderCancel = cancel2
	}

	if !leader.IsLeader() {
		t.Fatalf("leader.IsLeader() should be true (PRV-03)")
	}
	if follower.IsLeader() {
		t.Fatalf("follower.IsLeader() should be false (PRV-03 violated — two leaders)")
	}

	// Failover: cancel the leader. Pitfall 8 separate-ctx Unlock should
	// release the lock cleanly. Survivor MUST acquire within a few ticks
	// (~2s budget — the loop ticker is 1Hz).
	leaderCancel()
	if !waitFor(t, 4*time.Second, 100*time.Millisecond, follower.IsLeader) {
		t.Fatalf("follower did not acquire leadership after primary leader was cancelled within 4s")
	}

	// Sanity: replica IDs are populated.
	if leader.ReplicaID() == "" {
		t.Errorf("leader.ReplicaID() empty — should be 'replica-1' or 'replica-2'")
	}
	if follower.ReplicaID() == "" {
		t.Errorf("follower.ReplicaID() empty")
	}
}
