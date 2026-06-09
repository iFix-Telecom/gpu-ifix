package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/sony/gobreaker/v2"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
)

// newSensitiveTestSet builds a *breaker.Set wired to miniredis with
// short cooldown so tests can OPEN/CLOSE quickly. Returns the set + a
// helper to trip a named breaker into OPEN, plus a cleanup.
func newSensitiveTestSet(t *testing.T, names []string, cooldown time.Duration) (*breaker.Set, func(string), func()) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	bs := breaker.NewSet(rdb, slog.New(slog.NewTextHandler(io.Discard, nil)),
		breaker.Options{ConsecutiveFailures: 1, Cooldown: cooldown}, names)
	trip := func(name string) {
		t.Helper()
		// Force a failure through Execute — gobreaker counts via IsSuccessful.
		_, _ = bs.Execute(name, func() (*http.Response, error) {
			return nil, &breaker.HTTPError{Status: 500, Msg: "boom"}
		})
	}
	cleanup := func() {
		_ = rdb.Close()
		mr.Close()
	}
	return bs, trip, cleanup
}

// TestSensitiveRetry_BreakerStaysOpenExhausts verifies the 3-attempt
// loop returns ErrSensitiveRetryExhausted when the breaker stays OPEN
// throughout the budget. Wall time is bounded to ~4s + slack.
func TestSensitiveRetry_BreakerStaysOpenExhausts(t *testing.T) {
	bs, trip, cleanup := newSensitiveTestSet(t, []string{"local-llm"}, 30*time.Second)
	defer cleanup()
	trip("local-llm")
	// Confirm pre-conditions: breaker is OPEN.
	cb, _ := bs.Get("local-llm")
	if cb.State() != gobreaker.StateOpen {
		t.Fatalf("setup: expected OPEN, got %v", cb.State())
	}

	start := time.Now()
	ok, err := SensitiveRetry(context.Background(), bs, "local-llm")
	elapsed := time.Since(start)

	if ok {
		t.Errorf("ok = true, want false")
	}
	if !errors.Is(err, ErrSensitiveRetryExhausted) {
		t.Errorf("err = %v, want ErrSensitiveRetryExhausted", err)
	}
	// Total budget: 200 + 800 + 3000 = 4000ms; allow generous slack.
	if elapsed < 3900*time.Millisecond || elapsed > 5000*time.Millisecond {
		t.Errorf("elapsed = %v, want ~4s (3.9-5.0s window)", elapsed)
	}
}

// TestSensitiveRetry_ClientDisconnectExits is the Pitfall 5 regression
// guard: a client cancel during the loop returns ctx.Err() promptly
// without leaking the goroutine.
func TestSensitiveRetry_ClientDisconnectExits(t *testing.T) {
	bs, trip, cleanup := newSensitiveTestSet(t, []string{"local-llm"}, 30*time.Second)
	defer cleanup()
	trip("local-llm")

	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	var ok bool
	var err error
	go func() {
		ok, err = SensitiveRetry(ctx, bs, "local-llm")
		close(doneCh)
	}()

	// Let the first sleep start, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-doneCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("SensitiveRetry did not exit within 500ms after ctx cancel")
	}
	if ok {
		t.Errorf("ok = true, want false")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// TestSensitiveRetry_BreakerClosedReturnsTrue proves that if the breaker
// is already CLOSED on the first attempt, the loop returns true after
// the first sleep (no spinning).
func TestSensitiveRetry_BreakerClosedReturnsTrue(t *testing.T) {
	bs, _, cleanup := newSensitiveTestSet(t, []string{"local-llm"}, 30*time.Second)
	defer cleanup()
	// Don't trip; breaker stays CLOSED.

	start := time.Now()
	ok, err := SensitiveRetry(context.Background(), bs, "local-llm")
	elapsed := time.Since(start)

	if !ok {
		t.Errorf("ok = false, want true")
	}
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	// Should return after the FIRST sleep (~200ms) — not waiting full budget.
	if elapsed > 500*time.Millisecond {
		t.Errorf("elapsed = %v, want < 500ms (first attempt should succeed)", elapsed)
	}
}

// TestSensitiveRetry_UnknownUpstreamExhausts: if Get returns false the
// loop continues silently (since the upstream may have been removed by
// hot-reload mid-flight). The test asserts we eventually exhaust
// cleanly without panic.
func TestSensitiveRetry_UnknownUpstreamExhausts(t *testing.T) {
	bs, _, cleanup := newSensitiveTestSet(t, []string{"local-llm"}, 30*time.Second)
	defer cleanup()

	// Use a name not registered in bs; Get returns nil/false.
	// We use a short-cooldown ctx to keep the test wall-time low.
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	ok, err := SensitiveRetry(ctx, bs, "ghost-upstream")
	if ok {
		t.Errorf("ok = true, want false (ghost upstream cannot succeed)")
	}
	// Either ctx.Err (canceled by timeout) OR ErrSensitiveRetryExhausted is acceptable.
	if err == nil {
		t.Errorf("err = nil, want non-nil")
	}
}

// TestSensitiveRetry_ForceOverrideKeepsLoopOpen is the SEED-005 sanity
// regression: with a Redis-backed operator force-open in place and the
// natural gobreaker FSM in StateClosed, SensitiveRetry MUST honor the
// override (returning ok=false, ErrSensitiveRetryExhausted) so the
// dispatcher's sensitive-tenant path stays at writeSensitiveBlock instead
// of escaping to dispatchTo. Pre-fix the loop polled `cb.State()` only
// and returned (true, nil) immediately — sensitive requests hit the
// real upstream and emitted 502 upstream_unreachable instead of the
// contracted 503 upstream_unavailable_for_sensitive_tenant
// (live request_id 019e7008-3cc0 on 2026-05-28T19:20:48 UTC).
func TestSensitiveRetry_ForceOverrideKeepsLoopOpen(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	bs := breaker.NewSet(rdb, slog.New(slog.NewTextHandler(io.Discard, nil)),
		breaker.Options{ConsecutiveFailures: 1, Cooldown: 30 * time.Second}, []string{"local-llm"})

	// Natural FSM stays CLOSED (no trips). Plant a force-open in Redis.
	val := breaker.ForceOverrideValue{
		State:  "open",
		TTLSec: 60,
		SetBy:  "test-seed-005-regression",
		SetAt:  time.Now().UTC(),
	}
	buf, err := json.Marshal(val)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := rdb.Set(context.Background(), breaker.ForceOverrideKey("local-llm"), string(buf), 60*time.Second).Err(); err != nil {
		t.Fatalf("redis SET: %v", err)
	}

	// With force-override active, SensitiveRetry must NOT return (true, nil)
	// just because the natural FSM is StateClosed. EffectiveState must win.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ok, retryErr := SensitiveRetry(ctx, bs, "local-llm")
	if ok {
		t.Fatalf("ok = true with force-override active; want false (must exhaust). natural state was %v", bs.Snapshot()["local-llm"])
	}
	if !errors.Is(retryErr, ErrSensitiveRetryExhausted) {
		t.Errorf("err = %v, want ErrSensitiveRetryExhausted", retryErr)
	}
}
