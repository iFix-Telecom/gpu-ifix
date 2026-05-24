// Phase 06.9 Plan 04 Task 2 — breaker force-override read-path tests.
// Covers WARNING-4 entry-gate guarantees: read overhead ≤ 1ms per
// eval-tick (here = per cached evaluation; debounced to 1s freshness so
// Redis cost is amortized away from the request hot path).
package breaker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/sony/gobreaker/v2"
)

// newMiniRedis spins a miniredis server + redis.Client wired to it.
// Cleanup closes both.
func newMiniRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb, mr
}

// TestReadForceOverride_KeyAbsentReturnsSetFalse — no Redis key → caller
// treats as "no override" and falls through to observation-driven state.
func TestReadForceOverride_KeyAbsentReturnsSetFalse(t *testing.T) {
	rdb, _ := newMiniRedis(t)
	ctx := context.Background()

	state, ttl, set, err := ReadForceOverride(ctx, rdb, "local-llm")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if set {
		t.Errorf("set=true with no Redis key; want false")
	}
	if state != "" {
		t.Errorf("state=%q with no key; want empty", state)
	}
	if ttl != 0 {
		t.Errorf("ttl=%v with no key; want 0", ttl)
	}
}

// TestReadForceOverride_KeyPresentReturnsSetTrue — key set with state=open
// and TTL=60s returns set=true + state="open" + remaining TTL approximately
// 60s.
func TestReadForceOverride_KeyPresentReturnsSetTrue(t *testing.T) {
	rdb, _ := newMiniRedis(t)
	ctx := context.Background()

	val := ForceOverrideValue{
		State:  "open",
		TTLSec: 60,
		SetBy:  "operator-pedro",
		SetAt:  time.Now().UTC(),
	}
	buf, err := json.Marshal(val)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := rdb.Set(ctx, ForceOverrideKey("local-llm"), string(buf), 60*time.Second).Err(); err != nil {
		t.Fatalf("redis SET: %v", err)
	}

	state, ttl, set, err := ReadForceOverride(ctx, rdb, "local-llm")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !set {
		t.Errorf("set=false with key present; want true")
	}
	if state != "open" {
		t.Errorf("state=%q; want open", state)
	}
	if ttl <= 0 || ttl > 60*time.Second {
		t.Errorf("ttl=%v; want (0, 60s]", ttl)
	}
}

// TestReadForceOverride_KeyExpiredAfterTTL — Redis expires the key
// naturally; ReadForceOverride returns set=false after expiry.
func TestReadForceOverride_KeyExpiredAfterTTL(t *testing.T) {
	rdb, mr := newMiniRedis(t)
	ctx := context.Background()

	val := ForceOverrideValue{State: "open", TTLSec: 1, SetBy: "op", SetAt: time.Now().UTC()}
	buf, _ := json.Marshal(val)
	if err := rdb.Set(ctx, ForceOverrideKey("local-llm"), string(buf), 1*time.Second).Err(); err != nil {
		t.Fatalf("redis SET: %v", err)
	}

	// Fast-forward miniredis's clock by 2s — avoids a real sleep in tests.
	mr.FastForward(2 * time.Second)

	_, _, set, err := ReadForceOverride(ctx, rdb, "local-llm")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if set {
		t.Errorf("set=true after key expiry; want false")
	}
}

// TestReadForceOverride_MalformedValueReturnsErr — non-JSON value returns
// (set=false, err != nil). Breaker FSM uses err presence to fall back to
// observation-driven state rather than ignore the malformed override
// silently.
func TestReadForceOverride_MalformedValueReturnsErr(t *testing.T) {
	rdb, _ := newMiniRedis(t)
	ctx := context.Background()

	if err := rdb.Set(ctx, ForceOverrideKey("local-llm"), "not-json{{", 60*time.Second).Err(); err != nil {
		t.Fatalf("redis SET: %v", err)
	}

	state, _, set, err := ReadForceOverride(ctx, rdb, "local-llm")
	if err == nil {
		t.Fatal("expected err on malformed JSON; got nil")
	}
	if set {
		t.Errorf("set=true on malformed JSON; want false")
	}
	if state != "" {
		t.Errorf("state=%q on malformed JSON; want empty", state)
	}
}

// TestBreakerFSM_ForceOpenHonored — integration with Set.Execute. With a
// healthy upstream + CLOSED breaker, setting the force key causes the
// next Execute (after the 1s freshness window) to short-circuit with
// ErrBreakerOpen WITHOUT invoking fn. Deleting the key + waiting one
// freshness window lets Execute through again.
func TestBreakerFSM_ForceOpenHonored(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	s := NewSet(rdb, discardLogger(), Options{ConsecutiveFailures: 3, Cooldown: 100 * time.Millisecond}, []string{"openrouter-chat"})

	ctx := context.Background()
	val := ForceOverrideValue{State: "open", TTLSec: 300, SetBy: "operator", SetAt: time.Now().UTC()}
	buf, _ := json.Marshal(val)
	if err := rdb.Set(ctx, ForceOverrideKey("openrouter-chat"), string(buf), 300*time.Second).Err(); err != nil {
		t.Fatalf("SET: %v", err)
	}

	// Sanity: pre-force, breaker is CLOSED.
	if cb, _ := s.Get("openrouter-chat"); cb.State() != gobreaker.StateClosed {
		t.Fatalf("pre-force state = %v; want closed", cb.State())
	}

	// Force-refresh the cache so the test does not race the 1s debounce.
	s.RefreshForceOverride(ctx, "openrouter-chat")

	called := false
	_, err = s.Execute("openrouter-chat", func() (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: 200}, nil
	})
	// We expect a forced-open short-circuit (the breaker is otherwise CLOSED).
	if called {
		t.Fatal("fn invoked despite forced-open; expected short-circuit")
	}
	if !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("expected ErrBreakerOpen, got %v", err)
	}

	// Clear the key + invalidate cache → Execute fires fn again.
	if err := rdb.Del(ctx, ForceOverrideKey("openrouter-chat")).Err(); err != nil {
		t.Fatalf("DEL: %v", err)
	}
	s.RefreshForceOverride(ctx, "openrouter-chat")

	called = false
	_, err = s.Execute("openrouter-chat", func() (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: 200}, nil
	})
	if err != nil {
		t.Fatalf("post-clear Execute err = %v; want nil", err)
	}
	if !called {
		t.Fatal("fn not invoked after force-key cleared; expected normal dispatch")
	}
}

// TestBreakerFSM_ForceOverrideReadOverheadUnderMs — WARNING-4 measurement.
// Per the plan's eval-tick budget: 1000 evaluations with force key ABSENT
// must complete in <10ms total (≤10µs/eval — Redis GET is debounced to a
// cache hit). Force key PRESENT case must complete in <50ms total
// (≤50µs/eval — Redis GET amortized). Asserts that ReadForceOverride does
// NOT add per-request overhead.
func TestBreakerFSM_ForceOverrideReadOverheadUnderMs(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	s := NewSet(rdb, discardLogger(), Options{ConsecutiveFailures: 3, Cooldown: 100 * time.Millisecond}, []string{"local-llm"})
	ctx := context.Background()

	// Warm-up: prime the cache (one Redis GET) so subsequent reads hit the
	// in-memory cache. This is the "evaluation tick" cadence the plan
	// references — the first call within a freshness window costs one
	// Redis GET; subsequent calls within the window cost only a map read.
	s.RefreshForceOverride(ctx, "local-llm")

	// (a) Force key ABSENT — 1000 reads through CheckForceOverride must
	// complete in <10ms.
	start := time.Now()
	for i := 0; i < 1000; i++ {
		_ = s.CheckForceOverride("local-llm")
	}
	elapsed := time.Since(start)
	if elapsed > 10*time.Millisecond {
		t.Errorf("1000 CheckForceOverride calls (absent) took %v; want <10ms (≤10µs/eval)", elapsed)
	}

	// (b) Force key PRESENT — write the key, refresh once, then 1000
	// cached reads must complete in <50ms.
	val := ForceOverrideValue{State: "open", TTLSec: 300, SetBy: "op", SetAt: time.Now().UTC()}
	buf, _ := json.Marshal(val)
	if err := rdb.Set(ctx, ForceOverrideKey("local-llm"), string(buf), 300*time.Second).Err(); err != nil {
		t.Fatalf("SET: %v", err)
	}
	s.RefreshForceOverride(ctx, "local-llm")

	start = time.Now()
	for i := 0; i < 1000; i++ {
		if !s.CheckForceOverride("local-llm") {
			t.Fatalf("CheckForceOverride returned false with key present (iteration %d)", i)
		}
	}
	elapsed = time.Since(start)
	if elapsed > 50*time.Millisecond {
		t.Errorf("1000 CheckForceOverride calls (present) took %v; want <50ms (≤50µs/eval)", elapsed)
	}
}
