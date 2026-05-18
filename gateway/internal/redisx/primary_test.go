// Package redisx (primary_test.go): miniredis-backed unit tests for the
// Phase 6.6 primary-pod Hash + Pub/Sub helpers + redsync wrapper
// (06.6-CONTEXT.md D-08 + 06.6-PATTERNS.md §redisx/primary.go). Mirrors
// emerg_test.go layout so the test patterns stay consistent.
//
// Coverage:
//   - Key getters (PrimaryStateKey, PrimaryLockKey) return canonical strings
//   - Namespace gw:primary:* is operationally separate from gw:emerg:*
//   - WritePrimaryState HSET round-trip
//   - WritePrimaryState nil-client guard
//   - Publish/Subscribe round-trip for "primary_ready" event
//   - Publish/Subscribe round-trip for "force_up_request" event (reviews #3)
//   - Publish/Subscribe round-trip for "force_down_request" event (reviews #3)
//   - SubscribePrimaryEvents returns non-nil *redis.PubSub
//   - NewPrimaryRedsync acquires Lock successfully on fresh redis
//   - redisOpTimeout is honoured (pre-cancelled ctx errors)
package redisx

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/go-redsync/redsync/v4"
	"github.com/redis/go-redis/v9"
)

// reusedNewMiniRedis: the helper newMiniRedis is defined in emerg_test.go
// (same package). Tests here reuse it directly — no redeclaration.

func TestPrimaryStateKey_ReturnsExpectedPrefix(t *testing.T) {
	if got := PrimaryStateKey(); got != "gw:primary:state" {
		t.Fatalf("PrimaryStateKey() = %q, want gw:primary:state", got)
	}
}

func TestPrimaryLockKey_ReturnsExpectedPrefix(t *testing.T) {
	if got := PrimaryLockKey(); got != "gw:primary:lock" {
		t.Fatalf("PrimaryLockKey() = %q, want gw:primary:lock", got)
	}
}

func TestPrimaryEventsChannel_Constant(t *testing.T) {
	if PrimaryEventsChannel != "gw:primary:events" {
		t.Fatalf("PrimaryEventsChannel = %q, want gw:primary:events", PrimaryEventsChannel)
	}
}

// TestPrimaryLockKey_SeparateFromEmergLockKey enforces the namespace
// isolation invariant — primary and emerg MUST NOT share Redis keys.
// If this test fails, leader election will cross-clobber between the two
// reconcilers.
func TestPrimaryLockKey_SeparateFromEmergLockKey(t *testing.T) {
	if PrimaryLockKey() == EmergLockKey() {
		t.Fatalf("PrimaryLockKey() == EmergLockKey() (%q) — namespaces collide", PrimaryLockKey())
	}
	if PrimaryStateKey() == EmergStateKey() {
		t.Fatalf("PrimaryStateKey() == EmergStateKey() (%q) — namespaces collide", PrimaryStateKey())
	}
	if PrimaryEventsChannel == EmergEventsChannel {
		t.Fatalf("PrimaryEventsChannel == EmergEventsChannel (%q) — namespaces collide", PrimaryEventsChannel)
	}
}

func TestWritePrimaryState_RoundTrip(t *testing.T) {
	rdb := newMiniRedis(t)
	ctx := context.Background()

	enteredUnix := int64(1700000000)
	if err := WritePrimaryState(ctx, rdb, "primary_ready", "7", "http://10.10.10.50:8000", "98765", enteredUnix); err != nil {
		t.Fatalf("WritePrimaryState: %v", err)
	}

	got := rdb.HGetAll(ctx, PrimaryStateKey()).Val()
	want := map[string]string{
		"state":           "primary_ready",
		"lifecycle_id":    "7",
		"pod_url":         "http://10.10.10.50:8000",
		"pod_instance_id": "98765",
		"entered_at":      "1700000000",
	}
	if len(got) != len(want) {
		t.Fatalf("HGetAll: got %d fields, want %d. got=%+v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("HGetAll[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestWritePrimaryState_NilClient(t *testing.T) {
	// Wiring-bug guard: nil client must error fast (not panic).
	err := WritePrimaryState(context.Background(), nil, "primary_ready", "", "", "", 0)
	if err == nil {
		t.Fatalf("WritePrimaryState(nil rdb) returned nil err, want non-nil")
	}
}

// TestPublishPrimaryEvent_RoundTrip — baseline event round-trip via
// "primary_ready" event type. Proves Pub/Sub channel wiring works.
func TestPublishPrimaryEvent_RoundTrip(t *testing.T) {
	rdb := newMiniRedis(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ps := SubscribePrimaryEvents(ctx, rdb)
	t.Cleanup(func() { _ = ps.Close() })
	// Wait for the subscription to register against miniredis Pub/Sub.
	time.Sleep(50 * time.Millisecond)

	ev := PrimaryEvent{
		Type:        "primary_ready",
		State:       "primary_ready",
		LifecycleID: 7,
		Reason:      "first_health_pass",
		SinceUnix:   1700000000,
		ReplicaID:   "test-host",
	}
	if err := PublishPrimaryEvent(ctx, rdb, ev); err != nil {
		t.Fatalf("PublishPrimaryEvent: %v", err)
	}

	msg, err := ps.ReceiveMessage(ctx)
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}
	if msg.Channel != PrimaryEventsChannel {
		t.Fatalf("msg.Channel = %q, want %q", msg.Channel, PrimaryEventsChannel)
	}
	var got PrimaryEvent
	if err := json.Unmarshal([]byte(msg.Payload), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Type != ev.Type || got.State != ev.State || got.LifecycleID != ev.LifecycleID {
		t.Fatalf("event mismatch: got %+v, want %+v", got, ev)
	}
	if got.Reason != ev.Reason || got.SinceUnix != ev.SinceUnix || got.ReplicaID != ev.ReplicaID {
		t.Fatalf("event fields mismatch: got %+v, want %+v", got, ev)
	}
}

// TestPublishPrimaryEvent_ForceUpRequestRoundTrip — reviews #3 contract:
// proves "force_up_request" event type marshals + delivers correctly.
// Consumer is primary.Reconciler.handleForceUpRequest in Plan 06.6-06a.
func TestPublishPrimaryEvent_ForceUpRequestRoundTrip(t *testing.T) {
	rdb := newMiniRedis(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ps := SubscribePrimaryEvents(ctx, rdb)
	t.Cleanup(func() { _ = ps.Close() })
	time.Sleep(50 * time.Millisecond)

	ev := PrimaryEvent{
		Type:        "force_up_request",
		State:       "idle",
		LifecycleID: 0,
		Reason:      "manual_gatewayctl",
		SinceUnix:   1700000100,
		ReplicaID:   "ops-claude",
	}
	if err := PublishPrimaryEvent(ctx, rdb, ev); err != nil {
		t.Fatalf("PublishPrimaryEvent(force_up_request): %v", err)
	}

	msg, err := ps.ReceiveMessage(ctx)
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}
	var got PrimaryEvent
	if err := json.Unmarshal([]byte(msg.Payload), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Type != "force_up_request" {
		t.Fatalf("got.Type = %q, want force_up_request — reviews #3 contract broken", got.Type)
	}
	if got.Reason != "manual_gatewayctl" {
		t.Fatalf("got.Reason = %q, want manual_gatewayctl", got.Reason)
	}
}

// TestPublishPrimaryEvent_ForceDownRequestRoundTrip — reviews #3 contract:
// proves "force_down_request" event type marshals + delivers correctly.
// Consumer is primary.Reconciler.handleForceDownRequest in Plan 06.6-06a.
func TestPublishPrimaryEvent_ForceDownRequestRoundTrip(t *testing.T) {
	rdb := newMiniRedis(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ps := SubscribePrimaryEvents(ctx, rdb)
	t.Cleanup(func() { _ = ps.Close() })
	time.Sleep(50 * time.Millisecond)

	ev := PrimaryEvent{
		Type:        "force_down_request",
		State:       "primary_ready",
		LifecycleID: 42,
		Reason:      "manual_gatewayctl",
		SinceUnix:   1700000200,
		ReplicaID:   "ops-claude",
	}
	if err := PublishPrimaryEvent(ctx, rdb, ev); err != nil {
		t.Fatalf("PublishPrimaryEvent(force_down_request): %v", err)
	}

	msg, err := ps.ReceiveMessage(ctx)
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}
	var got PrimaryEvent
	if err := json.Unmarshal([]byte(msg.Payload), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Type != "force_down_request" {
		t.Fatalf("got.Type = %q, want force_down_request — reviews #3 contract broken", got.Type)
	}
	if got.LifecycleID != 42 {
		t.Fatalf("got.LifecycleID = %d, want 42", got.LifecycleID)
	}
}

func TestPublishPrimaryEvent_NilClient(t *testing.T) {
	err := PublishPrimaryEvent(context.Background(), nil, PrimaryEvent{})
	if err == nil {
		t.Fatalf("PublishPrimaryEvent(nil rdb) returned nil err, want non-nil")
	}
}

func TestSubscribePrimaryEvents_ReturnsRedisPubSub(t *testing.T) {
	rdb := newMiniRedis(t)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	ps := SubscribePrimaryEvents(ctx, rdb)
	if ps == nil {
		t.Fatal("SubscribePrimaryEvents returned nil")
	}
	t.Cleanup(func() { _ = ps.Close() })

	// Type assertion (compile-time check via blank ID).
	var _ *redis.PubSub = ps
}

func TestNewPrimaryRedsync_AcquiresLockSuccessfully(t *testing.T) {
	rdb := newMiniRedis(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rs := NewPrimaryRedsync(rdb)
	if rs == nil {
		t.Fatal("NewPrimaryRedsync returned nil")
	}

	mtx := rs.NewMutex(PrimaryLockKey(),
		redsync.WithExpiry(5*time.Second),
		redsync.WithTries(1),
	)
	if err := mtx.LockContext(ctx); err != nil {
		t.Fatalf("LockContext: %v", err)
	}

	// A second mutex on the same key should fail to lock (Tries=1).
	mtx2 := rs.NewMutex(PrimaryLockKey(),
		redsync.WithExpiry(5*time.Second),
		redsync.WithTries(1),
	)
	if err := mtx2.LockContext(ctx); err == nil {
		t.Fatal("second LockContext succeeded; want failure (lock held)")
	}

	ok, err := mtx.UnlockContext(ctx)
	if err != nil {
		t.Fatalf("UnlockContext: %v", err)
	}
	if !ok {
		t.Fatal("UnlockContext returned ok=false")
	}
}

func TestPrimaryRedisOpTimeout_Enforced(t *testing.T) {
	// Pre-cancelled context yields context.Canceled or DeadlineExceeded on
	// the first Redis op, never blocks. Protects the reconciler hot path
	// from a wedged Redis connection (same invariant as emerg).
	rdb := newMiniRedis(t)
	parent, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := WritePrimaryState(parent, rdb, "primary_ready", "0", "", "", 0)
	if err == nil {
		t.Fatal("WritePrimaryState with cancelled ctx returned nil err, want non-nil")
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got err=%v, want context.Canceled or DeadlineExceeded", err)
	}
}
