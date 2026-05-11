// Package redisx (shed_test.go): miniredis-backed round-trip tests for
// the Phase 5 shedding mirror helpers. Pattern mirrors breaker_test.go
// (one miniredis per test, t.Cleanup-bound) so the suite stays fast and
// independent.
package redisx

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestClient(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	m := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: m.Addr()})
	t.Cleanup(func() { _ = c.Close() })
	return c, m
}

func TestWriteAndReadShedState(t *testing.T) {
	ctx := context.Background()
	c, _ := newTestClient(t)
	sig := &ShedEventSignals{Inflight: 8, P95Ms: 2500, VramMiB: 22000}
	if err := WriteShedState(ctx, c, "local-llm", "on", "arm_timeout_sustained", 1234567890, sig); err != nil {
		t.Fatalf("write: %v", err)
	}
	m, err := ReadShedState(ctx, c, "local-llm")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if m["state"] != "on" {
		t.Errorf("state = %q want on", m["state"])
	}
	if m["since_unix"] != "1234567890" {
		t.Errorf("since_unix = %q", m["since_unix"])
	}
	if m["p95_ms"] != "2500" {
		t.Errorf("p95_ms = %q", m["p95_ms"])
	}
	if m["reason"] != "arm_timeout_sustained" {
		t.Errorf("reason = %q", m["reason"])
	}
}

func TestReadShedState_NotFoundReturnsNilNil(t *testing.T) {
	ctx := context.Background()
	c, _ := newTestClient(t)
	m, err := ReadShedState(ctx, c, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if m != nil {
		t.Fatalf("expected nil map, got %v", m)
	}
}

func TestPublishSubscribeRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, _ := newTestClient(t)

	ps := SubscribeShedEvents(ctx, c)
	defer ps.Close()
	// Wait for subscription to be established
	time.Sleep(50 * time.Millisecond)

	ev := ShedEvent{Upstream: "local-llm", State: "on", SinceUnix: 1000, Reason: "test"}
	if err := PublishShedEvent(ctx, c, ev); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case msg := <-ps.Channel():
		var got ShedEvent
		if err := json.Unmarshal([]byte(msg.Payload), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.Upstream != "local-llm" || got.State != "on" {
			t.Fatalf("bad event: %+v", got)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("no message received within 1s")
	}
}

func TestWriteShedForce_Success(t *testing.T) {
	ctx := context.Background()
	c, _ := newTestClient(t)
	if err := WriteShedForce(ctx, c, "local-llm", "off", 5*time.Minute); err != nil {
		t.Fatalf("write force: %v", err)
	}
	state, ttl, ok := GetShedForce(ctx, c, "local-llm")
	if !ok || state != "off" {
		t.Fatalf("get force: state=%s ok=%v", state, ok)
	}
	if ttl <= 0 || ttl > 5*time.Minute {
		t.Fatalf("ttl out of expected range: %s", ttl)
	}
}

func TestWriteShedForce_InvalidState(t *testing.T) {
	ctx := context.Background()
	c, _ := newTestClient(t)
	if err := WriteShedForce(ctx, c, "local-llm", "bogus", 1*time.Minute); err == nil {
		t.Fatal("expected error for invalid state")
	}
}

func TestWriteShedForce_TTLCeiling(t *testing.T) {
	ctx := context.Background()
	c, _ := newTestClient(t)
	if err := WriteShedForce(ctx, c, "local-llm", "off", 2*time.Hour); err == nil {
		t.Fatal("expected error for TTL > 1h")
	}
}

func TestWriteShedForce_TTLZero(t *testing.T) {
	ctx := context.Background()
	c, _ := newTestClient(t)
	if err := WriteShedForce(ctx, c, "local-llm", "off", 0); err == nil {
		t.Fatal("expected error for TTL == 0")
	}
}

func TestGetShedForce_NoKey(t *testing.T) {
	ctx := context.Background()
	c, _ := newTestClient(t)
	_, _, ok := GetShedForce(ctx, c, "nobody")
	if ok {
		t.Fatal("expected not found")
	}
}

func TestDeleteShedForce(t *testing.T) {
	ctx := context.Background()
	c, _ := newTestClient(t)
	_ = WriteShedForce(ctx, c, "a", "on", 1*time.Minute)
	if err := DeleteShedForce(ctx, c, "a"); err != nil {
		t.Fatal(err)
	}
	_, _, ok := GetShedForce(ctx, c, "a")
	if ok {
		t.Fatal("expected key deleted")
	}
}

func TestAllShedStateKeys_ExcludesForceKeys(t *testing.T) {
	ctx := context.Background()
	c, _ := newTestClient(t)
	_ = WriteShedState(ctx, c, "a", "on", "x", 1, nil)
	_ = WriteShedState(ctx, c, "b", "off", "x", 1, nil)
	_ = WriteShedForce(ctx, c, "a", "on", 1*time.Minute)
	keys, err := AllShedStateKeys(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	// Expect 2 state keys, not 3 (force excluded)
	if len(keys) != 2 {
		t.Fatalf("expected 2 state keys, got %d: %v", len(keys), keys)
	}
}

func TestShedStateKey_Format(t *testing.T) {
	if got := ShedStateKey("local-llm"); got != "gw:shed:local-llm" {
		t.Fatalf("ShedStateKey = %q, want gw:shed:local-llm", got)
	}
}

func TestShedForceKey_Format(t *testing.T) {
	if got := ShedForceKey("local-llm"); got != "gw:shed:force:local-llm" {
		t.Fatalf("ShedForceKey = %q, want gw:shed:force:local-llm", got)
	}
}

func TestShedEventsChannel_Constant(t *testing.T) {
	if ShedEventsChannel != "gw:shed:events" {
		t.Fatalf("ShedEventsChannel = %q, want gw:shed:events", ShedEventsChannel)
	}
}

func TestWriteShedState_NilClient(t *testing.T) {
	if err := WriteShedState(context.Background(), nil, "x", "on", "r", 1, nil); err == nil {
		t.Fatal("expected error on nil client")
	}
}

func TestReadShedState_NilClient(t *testing.T) {
	if _, err := ReadShedState(context.Background(), nil, "x"); err == nil {
		t.Fatal("expected error on nil client")
	}
}
