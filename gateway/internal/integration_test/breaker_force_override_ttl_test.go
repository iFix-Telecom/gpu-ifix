//go:build integration

// Phase 06.9 Plan 05b Task 2 — R1 breaker force-override TTL restoration.
//
// Operators install a force-override by writing
//
//	SET gw:breaker:force:{upstream} '{"state":"open","ttl_sec":N,...}' EX N
//
// (the gatewayctl CLI from Plan 04 does this). Redis EX TTL ensures a
// forgotten override expires naturally — max TTL=300s enforced at the CLI
// layer per Plan 04 acceptance. This integration test locks the TTL-
// expiry contract end-to-end:
//
//  1. Write the force-override key with TTL=2s (the wall-clock is bounded
//     so CI stays fast; the plan's example used 5s but the SHAPE of the
//     test is what matters — Redis EX semantics are deterministic).
//  2. ReadForceOverride immediately → set=true, ttl > 0.
//  3. Wait until past the TTL boundary.
//  4. ReadForceOverride again → set=false (key expired, no override).
//  5. Wire a real breaker.Set + observe IsOpen() flips back to the
//     observation-driven state (CLOSED on a fresh breaker with no failures).
//
// What this proves: an operator's force-open lever does NOT leave the
// breaker stuck in OPEN forever — the TTL contract releases the override
// and the breaker resumes observation-driven behavior. This guard exists
// because the Plan 06 UAT will install short-lived (≤300s) force-opens to
// drive failover scenarios, and the integration suite must lock the
// expiry-restores-observation contract in CI.
package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sony/gobreaker/v2"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
)

// TestIntegration_BreakerForceOverrideTTLRestores verifies that after a
// force-override TTL elapses, the breaker returns to observation-driven
// state (CLOSED on a healthy upstream).
func TestIntegration_BreakerForceOverrideTTLRestores(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, rdb := freshSchema(t, ctx)

	const upstream = "test-upstream"
	const ttl = 2 * time.Second // small, deterministic; CI-friendly

	// Step 1 — write the force-override key with Redis EX TTL. The JSON
	// shape MUST match ForceOverrideValue so ReadForceOverride decodes it.
	val := breaker.ForceOverrideValue{
		State:  "open",
		TTLSec: int(ttl / time.Second),
		SetBy:  "integration-test",
		SetAt:  time.Now().UTC(),
	}
	raw, err := json.Marshal(val)
	if err != nil {
		t.Fatalf("marshal force-override value: %v", err)
	}
	key := breaker.ForceOverrideKey(upstream)
	if err := rdb.Set(ctx, key, raw, ttl).Err(); err != nil {
		t.Fatalf("redis SET force-override: %v", err)
	}

	// Step 2 — immediately read; set=true with ttl > 0.
	state, remaining, set, rerr := breaker.ReadForceOverride(ctx, rdb, upstream)
	if rerr != nil {
		t.Fatalf("ReadForceOverride before expiry: %v", rerr)
	}
	if !set {
		t.Fatalf("ReadForceOverride before expiry: set=false; want set=true")
	}
	if state != "open" {
		t.Errorf("state = %q, want \"open\"", state)
	}
	if remaining <= 0 {
		t.Errorf("remaining TTL = %v, want > 0", remaining)
	}

	// Step 3 — wire a real breaker.Set with the upstream. The breaker
	// starts CLOSED (no failures observed). With the force-override
	// installed, IsForceOpen path must report TRUE.
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 3, Cooldown: 30 * time.Second},
		[]string{upstream},
	)
	// Force a refresh so the in-memory cache reflects the just-written key
	// without waiting for the 1s freshness debounce.
	bs.RefreshForceOverride(ctx, upstream)
	if !bs.CheckForceOverride(upstream) {
		t.Fatalf("CheckForceOverride pre-expiry: false; want true (force key just written)")
	}

	// Step 4 — wait until past the TTL boundary. Add a small safety margin
	// because Redis EX granularity is per-second on the wire (the GET that
	// finds the key absent must arrive after Redis processed the expiry).
	waitBudget := ttl + 1*time.Second
	time.Sleep(waitBudget)

	// Step 5 — ReadForceOverride returns set=false (key expired).
	_, _, postSet, postErr := breaker.ReadForceOverride(ctx, rdb, upstream)
	if postErr != nil {
		t.Fatalf("ReadForceOverride after expiry: %v", postErr)
	}
	if postSet {
		t.Errorf("ReadForceOverride after expiry: set=true; want false (TTL %v elapsed)", ttl)
	}

	// Step 6 — refresh breaker cache + verify CheckForceOverride is FALSE
	// AND the underlying gobreaker state has stayed CLOSED (no failures
	// drove it open during this test). This is the "observation-driven
	// state restored" assertion.
	bs.RefreshForceOverride(ctx, upstream)
	if bs.CheckForceOverride(upstream) {
		t.Errorf("CheckForceOverride post-expiry: true; want false (TTL elapsed → cache should refresh to set=false)")
	}
	cb, ok := bs.Get(upstream)
	if !ok {
		t.Fatalf("breaker.Get(%s): not found", upstream)
	}
	if cb.State() != gobreaker.StateClosed {
		t.Errorf("breaker state after force-override expiry = %v, want %v",
			cb.State(), gobreaker.StateClosed)
	}

	t.Logf("R1 TTL RESTORE VERIFIED: force-override (TTL=%v) expired; cache + breaker FSM both returned to observation-driven CLOSED",
		ttl)
}
