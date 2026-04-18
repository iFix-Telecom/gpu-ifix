package idempotency

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestStore(t *testing.T) (*Store, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewStore(rdb), mr
}

func TestStore_Get_EmptyKindIsEmpty(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	slot, err := s.Get(ctx, "tA", "k")
	if err != nil {
		t.Fatal(err)
	}
	if slot.Kind != SlotEmpty {
		t.Fatalf("expected SlotEmpty, got %v", slot.Kind)
	}
}

func TestStore_AcquireInFlight_FirstWinsSecondLoses(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()

	won, err := s.AcquireInFlight(ctx, "tA", "k", "req1", "hash1")
	if err != nil {
		t.Fatal(err)
	}
	if !won {
		t.Fatalf("expected first acquire to win")
	}
	won2, err := s.AcquireInFlight(ctx, "tA", "k", "req2", "hash1")
	if err != nil {
		t.Fatal(err)
	}
	if won2 {
		t.Fatalf("expected second acquire to lose while sentinel is held")
	}
	// Direct miniredis inspection: key must be `gw:idem:tA:k` with sentinel value.
	val, err := mr.Get("gw:idem:tA:k")
	if err != nil {
		t.Fatal(err)
	}
	if val == "" || val[:len(inFlightPrefix)] != inFlightPrefix {
		t.Fatalf("expected sentinel prefix, got %q", val)
	}
	// Get should decode back into SlotInFlight with WinnerReqID=req1, SentinelHash=hash1.
	slot, err := s.Get(ctx, "tA", "k")
	if err != nil {
		t.Fatal(err)
	}
	if slot.Kind != SlotInFlight {
		t.Fatalf("expected SlotInFlight, got %v", slot.Kind)
	}
	if slot.WinnerReqID != "req1" {
		t.Fatalf("WinnerReqID = %q, want req1", slot.WinnerReqID)
	}
	if slot.SentinelHash != "hash1" {
		t.Fatalf("SentinelHash = %q, want hash1", slot.SentinelHash)
	}
}

func TestStore_CompleteOverwritesSentinel(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, err := s.AcquireInFlight(ctx, "tA", "k", "req1", "hash1")
	if err != nil {
		t.Fatal(err)
	}
	entry := Entry{
		Status:      200,
		Headers:     map[string]string{"Content-Type": "application/json"},
		Body:        []byte(`{"ok":true}`),
		RequestHash: "hash1",
		StoredAt:    time.Now(),
	}
	if err := s.Complete(ctx, "tA", "k", entry); err != nil {
		t.Fatal(err)
	}
	slot, err := s.Get(ctx, "tA", "k")
	if err != nil {
		t.Fatal(err)
	}
	if slot.Kind != SlotCompleted {
		t.Fatalf("expected SlotCompleted after Complete, got %v", slot.Kind)
	}
	if slot.Entry.Status != 200 {
		t.Fatalf("Status = %d, want 200", slot.Entry.Status)
	}
	if string(slot.Entry.Body) != `{"ok":true}` {
		t.Fatalf("Body = %q", string(slot.Entry.Body))
	}
	if slot.Entry.RequestHash != "hash1" {
		t.Fatalf("RequestHash = %q, want hash1", slot.Entry.RequestHash)
	}
}

func TestStore_AbortClearsSentinel(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, _ = s.AcquireInFlight(ctx, "tA", "k", "req1", "hash1")
	if err := s.Abort(ctx, "tA", "k"); err != nil {
		t.Fatal(err)
	}
	slot, err := s.Get(ctx, "tA", "k")
	if err != nil {
		t.Fatal(err)
	}
	if slot.Kind != SlotEmpty {
		t.Fatalf("expected SlotEmpty after Abort, got %v", slot.Kind)
	}
	// Subsequent acquire must succeed.
	won, _ := s.AcquireInFlight(ctx, "tA", "k", "req2", "hash2")
	if !won {
		t.Fatalf("expected fresh acquire after abort to succeed")
	}
}

func TestStore_CompleteTTL24h(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()

	_, _ = s.AcquireInFlight(ctx, "tA", "k", "req1", "hash1")
	_ = s.Complete(ctx, "tA", "k", Entry{Status: 200, RequestHash: "hash1"})
	mr.FastForward(24*time.Hour + time.Second)
	slot, err := s.Get(ctx, "tA", "k")
	if err != nil {
		t.Fatal(err)
	}
	if slot.Kind != SlotEmpty {
		t.Fatalf("expected SlotEmpty after 24h FastForward, got %v", slot.Kind)
	}
}

func TestStore_InFlightTTL30s(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()

	_, _ = s.AcquireInFlight(ctx, "tA", "k", "req1", "hash1")
	mr.FastForward(31 * time.Second)
	slot, err := s.Get(ctx, "tA", "k")
	if err != nil {
		t.Fatal(err)
	}
	if slot.Kind != SlotEmpty {
		t.Fatalf("expected SlotEmpty after 31s FastForward, got %v", slot.Kind)
	}
}

func TestStore_KeyScopedByTenant(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, _ = s.AcquireInFlight(ctx, "tA", "x", "reqA", "hashA")
	_ = s.Complete(ctx, "tA", "x", Entry{Status: 200, RequestHash: "hashA"})
	slot, err := s.Get(ctx, "tB", "x")
	if err != nil {
		t.Fatal(err)
	}
	if slot.Kind != SlotEmpty {
		t.Fatalf("cross-tenant isolation broken: tB sees %v", slot.Kind)
	}
}

func TestStore_WaitForComplete_ReturnsWinnerOnSameHash(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, _ = s.AcquireInFlight(ctx, "tA", "k", "req1", "hash1")
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = s.Complete(ctx, "tA", "k", Entry{Status: 200, Body: []byte("ok"), RequestHash: "hash1"})
	}()
	start := time.Now()
	entry, err := s.WaitForComplete(ctx, "tA", "k", "hash1")
	if err != nil {
		t.Fatalf("WaitForComplete err: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("took too long: %v", elapsed)
	}
	if entry.Status != 200 {
		t.Fatalf("Status = %d", entry.Status)
	}
	if string(entry.Body) != "ok" {
		t.Fatalf("Body = %q", string(entry.Body))
	}
}

func TestStore_WaitForComplete_ConflictOnHashMismatch(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, _ = s.AcquireInFlight(ctx, "tA", "k", "req1", "hashA")
	start := time.Now()
	_, err := s.WaitForComplete(ctx, "tA", "k", "hashB")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	elapsed := time.Since(start)
	// Should NOT wait 30s — immediate conflict.
	if elapsed > 200*time.Millisecond {
		t.Fatalf("hash mismatch should be immediate, took %v", elapsed)
	}
}

func TestStore_WaitForComplete_TimeoutReturns409(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	// Shorten the budget for the test.
	oldBudget := waitPollBudget
	oldInterval := waitPollInterval
	waitPollBudget = 300 * time.Millisecond
	waitPollInterval = 50 * time.Millisecond
	t.Cleanup(func() {
		waitPollBudget = oldBudget
		waitPollInterval = oldInterval
	})

	_, _ = s.AcquireInFlight(ctx, "tA", "k", "req1", "hashA")
	// Never call Complete.
	start := time.Now()
	_, err := s.WaitForComplete(ctx, "tA", "k", "hashA")
	if !errors.Is(err, ErrInFlightTimeout) {
		t.Fatalf("expected ErrInFlightTimeout, got %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 250*time.Millisecond {
		t.Fatalf("returned too early (%v) — loop probably didn't poll", elapsed)
	}
}

func TestStore_WaitForComplete_AbortedReturnsEmpty(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	_, _ = s.AcquireInFlight(ctx, "tA", "k", "req1", "hashA")
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = s.Abort(ctx, "tA", "k")
	}()
	entry, err := s.WaitForComplete(ctx, "tA", "k", "hashA")
	if err != nil {
		t.Fatalf("WaitForComplete err: %v", err)
	}
	if entry.Status != 0 {
		t.Fatalf("expected empty Entry, got status=%d", entry.Status)
	}
}
