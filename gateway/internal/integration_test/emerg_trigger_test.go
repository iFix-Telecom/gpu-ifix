//go:build integration

// Phase 6 Plan 06-05 Task 2 — emergency trigger gate (PRV-04 / SC-1).
//
// Drives the local-llm breaker Pub/Sub channel against a leader-elected
// reconciler and asserts:
//
//   - Sustained OPEN (≥ ProvisionTriggerFailedOverSeconds=1 in test cfg)
//     advances FSM HEALTHY → EMERGENCY_PROVISIONING. (TestEmergTriggerSustained)
//   - Transient OPEN→CLOSED (< 1s) does NOT trigger; FSM stays HEALTHY.
//     (TestEmergTriggerTransient)
//   - When a live lifecycle row already exists in the DB, sustained OPEN
//     does NOT trigger again (D-C5 reconciler check). (TestEmergTriggerNoSpawnIfLiveLifecycle)
//
// All tests reuse the defaultTestCfg helper from emerg_leader_test.go
// which sets ProvisionTriggerFailedOverSeconds=1 (RESEARCH Pitfall 13:
// accelerated timings keep integration runtime sub-30s).
package integration

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// publishBreakerEvent is the single-call helper used by Plan 06-05 +
// downstream plans to drive the local-llm trigger. Wraps the canonical
// redisx.PublishBreakerEvent so test code stays compact.
func publishBreakerEvent(t *testing.T, rdb *redis.Client, upstream, state string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := redisx.PublishBreakerEvent(ctx, rdb, redisx.BreakerEvent{
		Upstream: upstream, State: state, SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("publishBreakerEvent(%s,%s): %v", upstream, state, err)
	}
}

// TestEmergTriggerSustained — single reconciler, miniredis-shared
// channel, OPEN published once. With ProvisionTriggerFailedOverSeconds=1,
// the FSM must reach EMERGENCY_PROVISIONING within ~3s (1s sustained +
// reconciler tick latency).
func TestEmergTriggerSustained(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

	fsm := emerg.NewFSM(slog.New(slog.DiscardHandler), nil)
	r := emerg.NewReconciler(emerg.Deps{
		DB:           pool,
		Redis:        rdb,
		Redsync:      redisx.NewEmergRedsync(rdb),
		FSM:          fsm,
		Cfg:          cfg,
		TickInterval: 100 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	// Wait for leadership so the trigger gate is allowed to fire.
	if !waitFor(t, 3*time.Second, 50*time.Millisecond, r.IsLeader) {
		t.Fatalf("reconciler did not acquire leadership within 3s")
	}

	// Publish OPEN — sustained timer begins.
	publishBreakerEvent(t, rdb, "local-llm", "open")

	// Eventually FSM reaches EMERGENCY_PROVISIONING. Budget = 5s.
	if !waitFor(t, 5*time.Second, 100*time.Millisecond, func() bool {
		return fsm.State() == emerg.StateEmergencyProvisioning
	}) {
		t.Fatalf("FSM did not reach EMERGENCY_PROVISIONING after sustained OPEN; got %s",
			fsm.State())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}

// TestEmergTriggerTransient — OPEN immediately followed by CLOSED at
// 200ms. With threshold=1s, the openSince must be reset BEFORE the
// reconciler observes it. FSM must remain HEALTHY.
func TestEmergTriggerTransient(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

	fsm := emerg.NewFSM(slog.New(slog.DiscardHandler), nil)
	r := emerg.NewReconciler(emerg.Deps{
		DB:           pool,
		Redis:        rdb,
		Redsync:      redisx.NewEmergRedsync(rdb),
		FSM:          fsm,
		Cfg:          cfg,
		TickInterval: 100 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	if !waitFor(t, 3*time.Second, 50*time.Millisecond, r.IsLeader) {
		t.Fatalf("reconciler did not acquire leadership within 3s")
	}

	publishBreakerEvent(t, rdb, "local-llm", "open")
	time.Sleep(200 * time.Millisecond)
	publishBreakerEvent(t, rdb, "local-llm", "closed")

	// Wait long enough that a buggy implementation (one that armed the
	// trigger on the first OPEN without re-reading state on the tick)
	// would have fired. With threshold=1s, the gap between OPEN at t=0
	// and CLOSED at t=0.2s is shorter than the threshold, so the timer
	// resets to 0 BEFORE the gate would have fired.
	time.Sleep(2 * time.Second)

	if got := fsm.State(); got != emerg.StateHealthy {
		t.Fatalf("FSM transitioned on transient OPEN→CLOSED: got %s, want healthy", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}

// TestEmergTriggerNoSpawnIfLiveLifecycle — pre-seed an unclosed
// emergency_lifecycles row so D-C5 reconciler check fires. Sustained
// OPEN must NOT cause a transition; FSM stays HEALTHY and the reconciler
// logs an error (not asserted directly — visible in -v output).
//
// 2026-05-14 — stale-test fix (debug session emerg-integration-tests-ci,
// cause 2). The original pre-seed INSERTed a row with NO vast_instance_id.
// This test was authored against the Plan 06-05 reconciler; Plan 06-07
// then added recoverOrphanLifecycles, which runs on the FIRST tick after
// a fresh leader acquires the lock — BEFORE evaluateTick / the D-C5 check.
// recoverOneLifecycle's branch (a) treats a live row with
// vast_instance_id IS NULL as a "pre-create orphan" and immediately
// closes it (shutdown_reason='leader_recovery_pre_create'). The pre-seeded
// row the test relied on therefore stopped being "live" before the D-C5
// check ever ran, so the sustained-OPEN trigger fired and the FSM
// advanced to emergency_provisioning ("FSM transitioned despite live
// lifecycle").
//
// Fix: pre-seed the row WITH a vast_instance_id. recoverOneLifecycle then
// skips branch (a) and falls through to the Vast.GetInstance branches —
// but this test wires no Vast client, so recoverOneLifecycle hits its
// `vastClient == nil` guard and SKIPS the row entirely (logged Warn,
// "next leader acquisition will retry"), leaving it live. The D-C5
// reconciler check in evaluateHealthy then correctly observes the live
// lifecycle and blocks the trigger — exactly the behaviour this test
// exists to prove. The fake instance ID is never dialled because no Vast
// client exists.
func TestEmergTriggerNoSpawnIfLiveLifecycle(t *testing.T) {
	rootCtx, rootCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer rootCancel()

	pool, rdb := freshSchema(t, rootCtx)
	cfg := defaultTestCfg(t)

	// Pre-seed: insert an unclosed lifecycle row (ended_at IS NULL) WITH a
	// vast_instance_id set. The partial unique index `emergency_live_singleton`
	// (Plan 06-02) guarantees ≤1 such row at a time. The vast_instance_id is
	// what makes leader recovery (Plan 06-07 recoverOrphanLifecycles) treat
	// the row as a real in-flight lifecycle rather than a pre-create orphan
	// to garbage-collect — and because this test wires no Vast client,
	// recovery skips the row and leaves it live, so it trips the D-C5
	// reconciler check on every subsequent trigger evaluation.
	if _, err := pool.Exec(rootCtx,
		`INSERT INTO ai_gateway.emergency_lifecycles (trigger_reason, vast_instance_id)
		 VALUES ('manual_force', 999999)`); err != nil {
		t.Fatalf("pre-seed lifecycle: %v", err)
	}

	fsm := emerg.NewFSM(slog.New(slog.DiscardHandler), nil)
	r := emerg.NewReconciler(emerg.Deps{
		DB:           pool,
		Redis:        rdb,
		Redsync:      redisx.NewEmergRedsync(rdb),
		FSM:          fsm,
		Cfg:          cfg,
		TickInterval: 100 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
	})

	ctx, cancel := context.WithCancel(rootCtx)
	defer cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	if !waitFor(t, 3*time.Second, 50*time.Millisecond, r.IsLeader) {
		t.Fatalf("reconciler did not acquire leadership within 3s")
	}

	publishBreakerEvent(t, rdb, "local-llm", "open")
	// Allow >> 1s sustained — D-C5 must block the trigger every tick.
	time.Sleep(2500 * time.Millisecond)

	if got := fsm.State(); got != emerg.StateHealthy {
		t.Fatalf("FSM transitioned despite live lifecycle (D-C5 check failed): got %s, want healthy", got)
	}

	// The pre-seeded live lifecycle must still be live — leader recovery
	// skipped it (no Vast client) rather than closing it.
	var liveCount int
	if err := pool.QueryRow(rootCtx,
		`SELECT COUNT(*) FROM ai_gateway.emergency_lifecycles WHERE ended_at IS NULL`,
	).Scan(&liveCount); err != nil {
		t.Fatalf("count live lifecycles: %v", err)
	}
	if liveCount != 1 {
		t.Fatalf("pre-seeded live lifecycle count = %d, want 1 (leader recovery must not close it)", liveCount)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}
