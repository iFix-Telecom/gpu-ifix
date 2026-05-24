// Package breaker — Phase 06.9 Plan 04 Task 2: force-override read-path.
//
// Operators may force a breaker to OPEN out-of-band (via
// `gatewayctl breaker force-open --upstream <name> --ttl <duration>`),
// independently of the observation-driven gobreaker state. This is
// required by Phase 06.9 Plan 06 HUMAN-UAT, which needs to drive
// failover scenarios (S1-S3) deterministically without breaking real
// upstream credentials.
//
// Read path (this file):
//
//   1. ReadForceOverride(ctx, rdb, name) consults the Redis key
//      `gw:breaker:force:{name}`. Value is JSON-encoded ForceOverrideValue.
//      Returns (state, ttl, set, err) — set=true tells the breaker FSM to
//      honor `state` over observation. Missing key returns set=false (no
//      error); malformed value returns err != nil (caller falls back to
//      observation-driven state, NOT silently ignored — operator wants to
//      know if their override was corrupted).
//
//   2. Set has an in-memory force-override cache (Set.forceCache) keyed
//      by upstream name. CheckForceOverride(name) is a pure map read.
//      RefreshForceOverride(ctx, name) updates the cache from Redis
//      (single GET); Set.Execute calls it lazily with a 1s freshness
//      debounce so the Redis GET is amortized away from the request hot
//      path (≤10µs/request cache hit, ≤50µs/request cache miss).
//
//   3. Set.Execute checks force-override FIRST (before remoteOpen / local
//      gobreaker state). Force takes PRECEDENCE over observation per
//      WARNING-4 acceptance: a forced-open breaker short-circuits with
//      ErrBreakerOpen even when the observed state is CLOSED.
//
// Write path: Operator-tooling (gateway/cmd/gatewayctl/breaker.go) writes
// the Redis key with Redis EX so a forgotten override expires naturally —
// max TTL = 300s enforced at the CLI layer.
//
// WARNING-4 entry-gate findings (documented in 06.9-PATTERNS.md
// "Breaker force-override seam"):
//   - Canonical FSM read site: gateway/internal/breaker/breaker.go:103
//     Set.Execute(name, fn). Pre-existing remoteOpen overlay (lines
//     111-113) is the analog we mirror.
//   - Breaker struct already holds rdb *redis.Client (breaker.go:42); NO
//     constructor refactor needed.
//   - Eval cadence: per-request through Execute. NO ticker. We add an
//     in-memory cache with 1s freshness debounce — first read per window
//     pays one Redis GET (~30-100µs against miniredis, ~1-3ms against
//     real Redis), subsequent reads within the window pay a map read
//     (~50ns).
//
// Tick latency window: force-override takes effect on the next
// per-upstream eval-tick (typically within ~1s — the freshness debounce
// horizon). The Plan 06 UAT scenarios tolerate ~1s arming latency
// against a 30-300s force-open TTL.
package breaker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ForceOverrideKeyPrefix is the Redis key namespace for breaker force-
// overrides. Layout: gw:breaker:force:{upstream-name}. The plain breaker
// state mirror uses gw:breaker:{upstream-name} (see breaker/mirror.go:13);
// force keys deliberately occupy a SUB-namespace to keep the two stores
// independent — a SCAN on `gw:breaker:force:*` enumerates only force keys.
const ForceOverrideKeyPrefix = "gw:breaker:force:"

// ForceOverrideKey returns the Redis key for an upstream's force-override
// entry. Exported so the gatewayctl CLI (Plan 04 Task 3) writes the same
// key the breaker FSM reads.
func ForceOverrideKey(upstreamName string) string {
	return ForceOverrideKeyPrefix + upstreamName
}

// ForceOverrideValue is the JSON shape stored in the Redis force key.
// Per WARNING-4 / R1 mandate:
//
//	State   — currently always "open" (Plan 04 ships open-only; the field is
//	          string-typed for forward-compat with future force-close/half-open
//	          semantics).
//	TTLSec  — the requested TTL the operator chose (also expressed as Redis
//	          EX on the SET — the field is informational, not authoritative).
//	          Max 300s enforced at the CLI layer.
//	SetBy   — the operator identity (e.g. shell user when invoked via SSH).
//	          For audit_log cross-reference.
//	SetAt   — UTC timestamp of the force-open write. Used by `gatewayctl
//	          breaker list` to display "forced since" duration.
type ForceOverrideValue struct {
	State  string    `json:"state"`
	TTLSec int       `json:"ttl_sec"`
	SetBy  string    `json:"set_by"`
	SetAt  time.Time `json:"set_at"`
}

// ReadForceOverride fetches and decodes the force-override entry for an
// upstream. Returns (state, remainingTTL, set, err):
//
//   - set=false, err=nil: no key in Redis. Caller MUST treat as "no
//     override" and consult observation-driven state.
//   - set=true,  err=nil: key present and well-formed. Caller MUST honor
//     state over observation.
//   - set=false, err!=nil: key present but malformed. Caller MUST log the
//     err and fall back to observation-driven state (do NOT silently
//     ignore — operator likely has a typo in the override script).
//
// Remaining TTL is read via PTTL so callers can display "expires in N"
// in operator output. Zero remaining TTL indicates the key has no TTL
// or already expired between GET and PTTL (rare race; treated as set=true
// because Redis returned a value).
func ReadForceOverride(ctx context.Context, rdb redis.Cmdable, upstreamName string) (state string, ttl time.Duration, set bool, err error) {
	key := ForceOverrideKey(upstreamName)
	raw, gerr := rdb.Get(ctx, key).Result()
	if gerr != nil {
		if errors.Is(gerr, redis.Nil) {
			return "", 0, false, nil
		}
		return "", 0, false, fmt.Errorf("breaker force-override GET: %w", gerr)
	}
	var v ForceOverrideValue
	if uerr := json.Unmarshal([]byte(raw), &v); uerr != nil {
		return "", 0, false, fmt.Errorf("breaker force-override JSON decode: %w", uerr)
	}
	// PTTL returns the remaining TTL in milliseconds; -2 means key absent
	// (race with expiry between GET and PTTL — treat as no override),
	// -1 means key has no expiry (operator wrote a permanent key, e.g.
	// for soak testing — return ttl=0 + set=true so the override still
	// takes effect).
	pttl, perr := rdb.PTTL(ctx, key).Result()
	if perr != nil {
		// PTTL error is non-fatal — we still have the value; just
		// surface ttl=0.
		return v.State, 0, true, nil
	}
	switch {
	case pttl < 0:
		// -2 (key absent) → race; treat as not-set.
		// -1 (no expiry) → permanent override; ttl=0, set=true.
		if pttl == -2 {
			return "", 0, false, nil
		}
		return v.State, 0, true, nil
	default:
		return v.State, pttl, true, nil
	}
}

// forceCacheEntry is the in-memory cache row for a single upstream.
// lastRefresh is the timestamp of the last Redis GET; CheckForceOverride
// returns `set` directly without re-touching Redis as long as
// time.Since(lastRefresh) < forceCacheFreshness.
type forceCacheEntry struct {
	set         bool
	state       string
	lastRefresh time.Time
}

// forceCacheFreshness is how long a cached force-override read is trusted
// before the next per-upstream Execute call re-reads from Redis. Tradeoff:
// shorter = faster reaction to operator force-open/close (good for Plan 06
// UAT scenarios); longer = lower Redis QPS in steady state. 1s matches the
// plan's "≤1ms per eval-tick" budget for the typical (cache-hit) case while
// keeping operator arming latency comfortably under the 5s polling cadence
// most operators expect from an admin CLI.
const forceCacheFreshness = 1 * time.Second

// forceCache is the in-memory cache attached to a *Set. Kept under its own
// mutex (separate from Set.mu) so cache lookups never contend with the
// breaker rebuild path or Snapshot iteration.
type forceCache struct {
	mu      sync.RWMutex
	entries map[string]forceCacheEntry
}

func newForceCache() *forceCache {
	return &forceCache{entries: map[string]forceCacheEntry{}}
}

// get returns the cached entry under RLock. Caller decides if it is fresh
// enough.
func (c *forceCache) get(name string) (forceCacheEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[name]
	return e, ok
}

// set writes the cache entry under Lock.
func (c *forceCache) set(name string, e forceCacheEntry) {
	c.mu.Lock()
	c.entries[name] = e
	c.mu.Unlock()
}
