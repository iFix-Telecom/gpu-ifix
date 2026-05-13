//go:build integration

// Phase 5 Plan 05-08 Task 8.3 — cross-replica mirror convergence:
// Pub/Sub → ApplyRemoteEvent + boot rehydration via HydrateFromRedis.
//
// CONTEXT.md D-C3: in-process FSM is authoritative; Redis is a mirror.
// Cross-replica convergence happens via two paths:
//  1. Live: replica A publishes a transition; replica B's Subscribe
//     loop reads gw:shed:events and calls Set.ApplyRemoteEvent →
//     updates remoteState overlay.
//  2. Boot: a new replica starts late and misses the live event. It
//     runs Set.HydrateFromRedis at startup which HGETALLs every
//     gw:shed:{upstream} Hash and seeds remoteState from the mirror.
//
// These tests use TWO in-process shed.Set instances against the same
// testcontainer Redis — no gateway subprocess needed. The tests
// validate the convergence pattern at the package boundary; the full
// gateway smoke that exercises this end-to-end is part of LIVE UAT
// per VALIDATION.md.
package integration

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/shed"
)

// TestMirrorConvergence simulates two replicas sharing one Redis. Replica
// A transitions its FSM to ON and publishes via MakePublishTransition;
// replica B's Subscribe goroutine receives the event and applies it to
// the remoteState overlay. Convergence target: ≤500ms.
func TestMirrorConvergence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, rdb := freshSchema(t, ctx)

	// Replica A — owns the local FSM that will transition.
	setA := shed.NewSet(rdb, slog.Default(), shed.Options{
		DefaultArmSeconds:     1,
		DefaultRecoverSeconds: 1,
	})
	setA.Rebuild([]string{"local-llm"})

	// Replica B — the observer. Subscribe runs in a goroutine and
	// updates remoteState on every received event.
	setB := shed.NewSet(rdb, slog.Default(), shed.Options{
		DefaultArmSeconds:     1,
		DefaultRecoverSeconds: 1,
	})
	setB.Rebuild([]string{"local-llm"})

	go setB.Subscribe(ctx, rdb)
	// Allow subscribe to attach before publishing — Redis Pub/Sub is
	// at-most-once and SUBSCRIBE before the first PUBLISH is mandatory.
	time.Sleep(100 * time.Millisecond)

	// Replica A: drive FSM to ON synthetically + publish the event.
	fsmA, ok := setA.Get("local-llm")
	if !ok {
		t.Fatal("replica A: FSM for local-llm not present after Rebuild")
	}
	fsmA.Transition(shed.StateOn, "test-synthetic")

	pub := shed.MakePublishTransition(rdb)
	pub("local-llm", shed.StateOn, "test-synthetic", &redisx.ShedEventSignals{Inflight: 4})

	// Wait for replica B to converge.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if st, found := setB.RemoteState("local-llm"); found && st == shed.StateOn {
			t.Logf("mirror convergence PASS: replica B remoteState=on after %s",
				time.Since(deadline.Add(-500*time.Millisecond)))
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("mirror convergence FAIL: replica B did not receive remote state within 500ms")
}

// TestBootRehydration validates RESEARCH Pitfall 3 mitigation — a
// replica that starts late (after a live event was already published
// and lost) MUST be able to recover the cluster-wide state on boot by
// HGETALL-ing gw:shed:{upstream} via HydrateFromRedis.
func TestBootRehydration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, rdb := freshSchema(t, ctx)

	// Seed the mirror Hash directly via redisx.WriteShedState — as if
	// another replica had already persisted its FSM state to Redis at
	// some earlier point.
	if err := redisx.WriteShedState(ctx, rdb, "local-llm", "on", "seeded", time.Now().Unix(), nil); err != nil {
		t.Fatalf("WriteShedState: %v", err)
	}

	// Build a fresh Set that has NEVER seen a Pub/Sub event for this
	// upstream. Without HydrateFromRedis, remoteState would be empty
	// (false, StateOff) — the new replica would silently report "no
	// peer reports anything" until the next transition fires.
	set := shed.NewSet(rdb, slog.Default(), shed.Options{
		DefaultArmSeconds:     1,
		DefaultRecoverSeconds: 1,
	})
	set.Rebuild([]string{"local-llm"})
	set.HydrateFromRedis(ctx, rdb, slog.Default())

	st, ok := set.RemoteState("local-llm")
	if !ok {
		t.Fatalf("HydrateFromRedis: remoteState for local-llm not populated")
	}
	if st != shed.StateOn {
		t.Errorf("HydrateFromRedis: state=%s, want on", st.String())
	}
}
