// Package breaker (breaker.go) wraps sony/gobreaker/v2 circuit breakers
// per upstream and exposes a cross-replica overlay (remoteOpen) so a
// peer's OPEN transition propagated via Pub/Sub causes the local
// dispatcher to short-circuit without first having to fail itself.
//
// Authoritative state per process is the in-process *gobreaker.CircuitBreaker;
// Redis is a mirror, never the source of truth. Redis-down does NOT
// stop breakers from operating (CONTEXT.md D-D1).
package breaker

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sony/gobreaker/v2"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// Options controls breaker creation. Defaults come from config.Config
// (BREAKER_CONSECUTIVE_FAILURES=3, BREAKER_COOLDOWN_SECONDS=30).
type Options struct {
	ConsecutiveFailures uint32        // trip threshold (D-A3)
	Cooldown            time.Duration // OPEN → HALF_OPEN timeout (D-A3)
}

// DefaultOptions returns CONTEXT.md D-A3 strict defaults.
func DefaultOptions() Options {
	return Options{ConsecutiveFailures: 3, Cooldown: 30 * time.Second}
}

// Set manages a fixed pool of circuit breakers keyed by upstream name.
// Construct once at boot via NewSet; call Rebuild on hot-reload only
// when an upstream is added/removed.
type Set struct {
	rdb *redis.Client
	log *slog.Logger
	opt Options

	mu         sync.RWMutex
	cbs        map[string]*gobreaker.CircuitBreaker[*http.Response]
	remoteOpen map[string]time.Time // state reported by other replicas via Pub/Sub

	// Phase 06.9 Plan 04 Task 2 — operator force-override cache.
	// In-memory cache of `gw:breaker:force:{name}` Redis state; populated
	// lazily by Execute via the 1-second freshness debounce. See
	// breaker/force_override.go for the read-path contract + Plan 06.9-04
	// SUMMARY for the entry-gate findings (WARNING-4).
	forceCache *forceCache
}

// NewSet constructs the initial set of breakers, one per upstream name.
func NewSet(rdb *redis.Client, log *slog.Logger, opt Options, names []string) *Set {
	s := &Set{
		rdb:        rdb,
		log:        log.With("module", "BREAKER"),
		opt:        opt,
		cbs:        make(map[string]*gobreaker.CircuitBreaker[*http.Response], len(names)),
		remoteOpen: make(map[string]time.Time),
		// Phase 06.9 Plan 04 Task 2 — operator force-override cache. Empty
		// at construction; CheckForceOverride treats missing entries as
		// "no override" (the natural cold-start state). The first Execute
		// per upstream populates the cache via the lazy debounce path.
		forceCache: newForceCache(),
	}
	for _, n := range names {
		s.cbs[n] = s.newBreaker(n)
	}
	return s
}

// Rebuild atomically swaps the breaker map to match a new set of names.
// Breakers for unchanged names are preserved so their state survives
// hot-reloads; new names get fresh CLOSED breakers; removed names are
// dropped along with any remoteOpen overlay entry for them.
func (s *Set) Rebuild(names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	for n := range s.cbs {
		if !want[n] {
			delete(s.cbs, n)
			delete(s.remoteOpen, n)
		}
	}
	for _, n := range names {
		if _, ok := s.cbs[n]; !ok {
			s.cbs[n] = s.newBreaker(n)
		}
	}
}

// Get returns the breaker for name + found flag. Caller uses it for
// State() introspection OR calls Set.Execute for gated dispatch.
func (s *Set) Get(name string) (*gobreaker.CircuitBreaker[*http.Response], bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cb, ok := s.cbs[name]
	return cb, ok
}

// Execute wraps gobreaker.Execute with cross-replica + operator-override
// awareness. Short-circuits with ErrBreakerOpen WITHOUT firing fn when
// any of the following is TRUE:
//
//  1. The upstream name is unknown (defensive — treats like OPEN).
//  2. Phase 06.9 Plan 04: a force-override has been written to Redis
//     `gw:breaker:force:{name}` (operator drove Plan 06 UAT scenario).
//  3. A remote replica reported OPEN within the last Cooldown window
//     (prevents thundering herd on a known-dead upstream).
//
// Force-override (2) is checked BEFORE remote-open (3) because operator
// intent must take precedence over peer observation. The Redis GET that
// backs the override read is debounced via the in-memory forceCache with
// a 1-second freshness window — see breaker/force_override.go — so the
// per-request hot path stays at ~50ns map-read in the steady state.
func (s *Set) Execute(name string, fn func() (*http.Response, error)) (*http.Response, error) {
	// Lazy refresh of the force-override cache: cheap when within the
	// freshness window (returns early without touching Redis). Done
	// outside the s.mu RLock so contention on the breaker map is
	// unaffected — forceCache has its own RWMutex.
	s.maybeRefreshForceOverride(name)

	s.mu.RLock()
	cb, ok := s.cbs[name]
	remoteAt, isRemoteOpen := s.remoteOpen[name]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrBreakerOpen // unknown upstream behaves like OPEN
	}
	// Phase 06.9 Plan 04 — operator force-override takes PRECEDENCE over
	// observation. Operator wrote `gw:breaker:force:{name}` via
	// `gatewayctl breaker force-open`; honor it without driving the local
	// gobreaker counters (the override is orthogonal to the FSM).
	if s.CheckForceOverride(name) {
		return nil, ErrBreakerOpen
	}
	if isRemoteOpen && time.Since(remoteAt) < s.opt.Cooldown {
		return nil, ErrBreakerOpen
	}
	return cb.Execute(fn)
}

// CheckForceOverride returns TRUE when an operator-installed force-override
// is currently in effect for the named upstream. Pure cache read; safe to
// call on the hot path. Callers that need the latest Redis state (e.g.
// `gatewayctl breaker list`) should call RefreshForceOverride first.
//
// Phase 06.9 Plan 04 Task 2: the cache is populated lazily by Execute via
// maybeRefreshForceOverride. CheckForceOverride is also called from tests
// that have pre-warmed the cache via RefreshForceOverride.
func (s *Set) CheckForceOverride(name string) bool {
	e, ok := s.forceCache.get(name)
	if !ok {
		return false
	}
	// Phase 06.9 Plan 04 ships open-only force semantics. State "" (zero
	// value when set=false) also returns false; the explicit check on
	// `e.set` is the authoritative gate.
	return e.set && e.state == "open"
}

// RefreshForceOverride forces a Redis read for the named upstream and
// updates the cache. Used by:
//   - maybeRefreshForceOverride during normal Execute (debounced).
//   - Tests that need deterministic ordering (no 1s wait).
//   - `gatewayctl breaker list` (Plan 04 Task 3) when an operator wants
//     a non-cached view.
//
// On Redis error or malformed JSON, the cache is INVALIDATED (set=false)
// and a WARN log surfaces — the breaker FSM falls back to observation-
// driven state, matching the existing remote-open fallback policy.
func (s *Set) RefreshForceOverride(ctx context.Context, name string) {
	state, _, set, err := ReadForceOverride(ctx, s.rdb, name)
	if err != nil {
		s.log.Warn("breaker force-override read failed; falling back to observation",
			"upstream", name, "err", err)
		// Invalidate cache so we re-attempt the Redis GET on the next
		// freshness window expiry, rather than carry a stale "open" view.
		s.forceCache.set(name, forceCacheEntry{set: false, state: "", lastRefresh: time.Now()})
		return
	}
	s.forceCache.set(name, forceCacheEntry{
		set:         set,
		state:       state,
		lastRefresh: time.Now(),
	})
}

// maybeRefreshForceOverride is the lazy-refresh helper called by Execute.
// Returns immediately when the cached entry is still within
// forceCacheFreshness; otherwise fires a single Redis GET via
// RefreshForceOverride. Bounded latency: the Redis GET is best-case
// ~30-100µs against the colocated Redis we deploy with the gateway —
// well under the plan's ≤1ms-per-tick budget — and amortized away from
// the request hot path by the freshness window.
//
// Background goroutine NOT used here: forcing a synchronous GET on the
// first request per freshness window keeps the operator semantic clean
// (no separate "is the cache primed yet?" tracker) and the latency cost
// is bounded by the freshness window. Tests use a context.Background()
// because the Redis call is non-cancellable from the request side and
// short enough to outlast a typical client timeout.
func (s *Set) maybeRefreshForceOverride(name string) {
	e, ok := s.forceCache.get(name)
	if ok && time.Since(e.lastRefresh) < forceCacheFreshness {
		return
	}
	s.RefreshForceOverride(context.Background(), name)
}

// EffectiveState returns the routing-relevant breaker state for the named
// upstream, combining operator force-override (Phase 06.9 Plan 04) with the
// observation-driven gobreaker state. When a force-override is in effect,
// EffectiveState returns gobreaker.StateOpen regardless of the observed
// state — this is the read-path counterpart to Set.Execute's force-override
// check, intended for callers (e.g. proxy/dispatcher.go) that decide which
// tier to dispatch to BEFORE invoking Execute. The freshness of the override
// matches CheckForceOverride (cached map read; refresh via Execute or a
// caller-driven RefreshForceOverride). Unknown upstream → StateClosed to
// match `cb, ok := Get(name)` callers that treat "ok=false" as no-breaker.
func (s *Set) EffectiveState(name string) gobreaker.State {
	// Refresh the force-override cache once per freshness window so callers
	// that exclusively use EffectiveState (and never call Execute) still see
	// recent Redis state. Cheap when within the freshness window.
	s.maybeRefreshForceOverride(name)
	if s.CheckForceOverride(name) {
		return gobreaker.StateOpen
	}
	cb, ok := s.Get(name)
	if !ok || cb == nil {
		return gobreaker.StateClosed
	}
	return cb.State()
}

// Snapshot returns a name→state-string map suitable for /v1/health/upstreams.
// Values are one of "closed", "half-open", "open".
func (s *Set) Snapshot() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.cbs))
	for n, cb := range s.cbs {
		out[n] = cb.State().String()
	}
	return out
}

// newBreaker constructs a *gobreaker.CircuitBreaker[*http.Response] with
// CONTEXT.md D-A3 thresholds and D-A4 IsSuccessful filter.
func (s *Set) newBreaker(name string) *gobreaker.CircuitBreaker[*http.Response] {
	log := s.log.With("upstream", name)
	return gobreaker.NewCircuitBreaker[*http.Response](gobreaker.Settings{
		Name:        name,
		MaxRequests: 1,
		Interval:    0,
		Timeout:     s.opt.Cooldown,
		ReadyToTrip: func(c gobreaker.Counts) bool {
			return c.ConsecutiveFailures >= s.opt.ConsecutiveFailures
		},
		OnStateChange: func(n string, from, to gobreaker.State) {
			log.Info("breaker state change",
				"from", from.String(),
				"to", to.String(),
				"at", time.Now().Format(time.RFC3339),
			)
			obs.BreakerState.WithLabelValues(n).Set(stateFloat(to))
			if from == gobreaker.StateClosed && to == gobreaker.StateOpen {
				obs.BreakerTripsTotal.WithLabelValues(n).Inc()
			}
			// Mirror to Redis (best-effort; DO NOT block the state machine).
			go s.publishTransition(n, to)
		},
		IsSuccessful: IsSuccessful,
	})
}

// stateFloat maps gobreaker.State to the float64 expected by the
// gateway_breaker_state Prometheus gauge.
func stateFloat(st gobreaker.State) float64 {
	switch st {
	case gobreaker.StateClosed:
		return 0
	case gobreaker.StateHalfOpen:
		return 1
	case gobreaker.StateOpen:
		return 2
	}
	return -1
}

// IsSuccessful implements the D-A4 failure definition. Returns TRUE for
// conditions that MUST NOT trip the breaker: err==nil, context.Canceled
// (client gave up, not an upstream fault), and HTTP 4xx including 429
// (client mis-use or throttle, not health). Returns FALSE (counted as
// failure) for: context.DeadlineExceeded, any net.Error with Timeout(),
// HTTP 500-504 wrapped in *HTTPError, and any other error.
func IsSuccessful(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	var he *HTTPError
	if errors.As(err, &he) {
		if he.Status >= 400 && he.Status < 500 {
			return true
		}
		// 5xx always counts as failure
		return false
	}
	// Timeouts, connection-reset-before-first-byte, DNS errors → failure
	return false
}

// HTTPError is the typed error that the dispatcher emits when an
// upstream returned a non-2xx status. Using a typed error (rather than
// a sentinel or string match) lets IsSuccessful classify 4xx vs 5xx
// cleanly.
type HTTPError struct {
	Status int
	Msg    string
}

// Error returns the human-readable message. Status is exposed as a
// struct field rather than via Error() so IsSuccessful avoids string
// parsing.
func (e *HTTPError) Error() string { return e.Msg }

// publishTransition is the goroutine body fired from OnStateChange.
// Separate function so tests can stub it. Failures bump the
// gateway_breaker_mirror_failures_total counter and log at WARN; the
// in-process state machine is unaffected (CONTEXT.md D-D1).
func (s *Set) publishTransition(name string, to gobreaker.State) {
	ctx := context.Background()
	if err := redisx.WriteBreakerState(ctx, s.rdb, name, to.String(), time.Now().Unix()); err != nil {
		obs.BreakerMirrorFailuresTotal.Inc()
		s.log.Warn("breaker mirror HSET failed", "upstream", name, "err", err)
		return
	}
	if err := redisx.PublishBreakerEvent(ctx, s.rdb, redisx.BreakerEvent{
		Upstream:  name,
		State:     to.String(),
		SinceUnix: time.Now().Unix(),
	}); err != nil {
		obs.BreakerMirrorFailuresTotal.Inc()
		s.log.Warn("breaker mirror PUBLISH failed", "upstream", name, "err", err)
	}
}

// applyRemoteEvent is called by the subscribe goroutine when another
// replica's breaker transitions. We maintain a per-name overlay so
// Execute can short-circuit without driving the local gobreaker's
// counters to an inconsistent state.
//
// Note on timestamp: we record time.Now() (local-clock arrival) rather
// than time.Unix(ev.SinceUnix, 0). The wire format SinceUnix has
// 1-second resolution, which is too coarse for sub-second Cooldown
// windows used in tests, and clock drift between replicas would only
// add noise to a "did the peer trip recently?" check. Local arrival
// time is the right semantics anyway — we trust the message but not
// the peer's clock.
func (s *Set) applyRemoteEvent(ev redisx.BreakerEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch ev.State {
	case "open":
		s.remoteOpen[ev.Upstream] = time.Now()
	case "closed", "half-open":
		delete(s.remoteOpen, ev.Upstream)
	}
}
