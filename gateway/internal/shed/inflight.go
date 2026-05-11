// Package shed (inflight.go): per-(upstream, tenant) inflight counter
// registry consumed by the FSM 2-of-3 saturation gate (CONTEXT.md D-A1
// inflight signal) and the per-tenant fairness hard cap (CONTEXT.md
// D-B1).
//
// Design (RESEARCH Pattern 4 + PATTERNS §inflight.go):
//   - global[upstream]      → atomic.Int64 (decision hot path)
//   - tenant[upstream][tid] → atomic.Int64 (per-tenant cap enforcement)
//   - RWMutex only on populate-once for the inner tenant map; Inc/Dec
//     and reads are lockless atomic ops on the *atomic.Int64.
//
// Increment is paired with a defer'd Dec in the shed middleware
// (Plan 05-06) — middleware is the single source of truth; dispatcher
// does NOT call Inc/Dec.
package shed

import (
	"sync"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// InflightRegistry tracks in-flight requests per (upstream, tenant).
// Construct via NewInflightRegistry; mutate via Inc/Dec; read via
// GlobalInflight / TenantInflight.
//
// Both `global` and `tenant` are protected by the same RWMutex so
// AddUpstream (called during upstream hot-reload) can grow the map
// safely. Inc/Dec/Read paths take the read lock briefly to look up
// the *atomic.Int64 pointer, then operate on it lockless via
// atomic.AddInt64 / Load.
type InflightRegistry struct {
	mu     sync.RWMutex
	global map[string]*atomic.Int64
	tenant map[string]map[uuid.UUID]*atomic.Int64
}

// NewInflightRegistry pre-allocates the global counter per upstream
// and the empty per-upstream tenant map. Tenant counters are created
// lazily on the first Inc for an unseen (upstream, tenantID) pair.
func NewInflightRegistry(upstreams []string) *InflightRegistry {
	r := &InflightRegistry{
		global: make(map[string]*atomic.Int64, len(upstreams)),
		tenant: make(map[string]map[uuid.UUID]*atomic.Int64, len(upstreams)),
	}
	for _, name := range upstreams {
		r.global[name] = &atomic.Int64{}
		r.tenant[name] = make(map[uuid.UUID]*atomic.Int64)
	}
	return r
}

// Inc bumps both the global and per-tenant counters for (upstream,
// tenantID). Inc on an unknown upstream is a no-op for the counters
// but bumps gateway_shed_inflight_unknown_upstream_total so dashboards
// surface the wiring bug (WR-05). Typical cause: a new upstream row was
// inserted via `gatewayctl upstreams create` but the inflight registry
// has not been rebuilt for it yet. Call AddUpstream / NewInflightRegistry
// in the hot-reload listener to track newly-added upstreams.
//
// The first Inc for a new tenantID on a known upstream takes the write
// lock to insert the counter; subsequent Inc/Dec for the same tenantID
// are lockless atomic.AddInt64.
func (r *InflightRegistry) Inc(upstream string, tenantID uuid.UUID) {
	if r == nil {
		return
	}
	// Fast path: read-lock + look up the upstream's global counter
	// and tenant counter together. If both exist, atomically Add.
	r.mu.RLock()
	g, gok := r.global[upstream]
	tmap := r.tenant[upstream]
	var c *atomic.Int64
	var cok bool
	if tmap != nil {
		c, cok = tmap[tenantID]
	}
	r.mu.RUnlock()

	if !gok {
		// WR-05: surface the wiring bug instead of silently dropping.
		// The in-process FSM will never see inflight signal for this
		// upstream until the registry is rebuilt — shedding is broken
		// for it. Operators need visibility on this state.
		obs.GatewayShedInflightUnknownUpstream.WithLabelValues(upstream, "inc").Inc()
		return
	}
	g.Add(1)

	if cok {
		c.Add(1)
		return
	}

	// Slow path: write-lock + map insert. Re-check after taking the
	// write lock in case another goroutine inserted the counter while
	// we were waiting.
	r.mu.Lock()
	tmap = r.tenant[upstream]
	if tmap == nil {
		// AddUpstream guarantees tenant[upstream] exists for any
		// upstream in r.global; this branch covers an edge race
		// where AddUpstream ran between the read-lock and write-lock.
		tmap = make(map[uuid.UUID]*atomic.Int64)
		r.tenant[upstream] = tmap
	}
	if c, cok = tmap[tenantID]; !cok {
		c = &atomic.Int64{}
		tmap[tenantID] = c
	}
	r.mu.Unlock()
	c.Add(1)
}

// Dec decrements both counters for (upstream, tenantID). A Dec without
// a matching Inc may temporarily push the counter negative; the registry
// stays arithmetically sound (the next Inc restores the balance) but
// dashboards may flicker — middleware MUST pair Inc with a defer'd Dec.
//
// Dec on an unknown upstream bumps
// gateway_shed_inflight_unknown_upstream_total (WR-05) so the wiring
// bug is visible. Dec on an unknown tenant for a KNOWN upstream stays
// a silent no-op (it is the symptom of an Inc/Dec mismatch on a
// rebuilt tenant map, not a wiring bug).
func (r *InflightRegistry) Dec(upstream string, tenantID uuid.UUID) {
	if r == nil {
		return
	}
	r.mu.RLock()
	g, gok := r.global[upstream]
	tmap := r.tenant[upstream]
	var c *atomic.Int64
	var cok bool
	if tmap != nil {
		c, cok = tmap[tenantID]
	}
	r.mu.RUnlock()

	if !gok {
		obs.GatewayShedInflightUnknownUpstream.WithLabelValues(upstream, "dec").Inc()
		return
	}
	g.Add(-1)
	if cok {
		c.Add(-1)
	}
}

// GlobalInflight returns the current in-flight count summed across all
// tenants for the upstream. Returns 0 for an unknown upstream (defensive
// — hot-reload may rebuild the registry mid-request).
func (r *InflightRegistry) GlobalInflight(upstream string) int64 {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	g, ok := r.global[upstream]
	r.mu.RUnlock()
	if !ok {
		return 0
	}
	return g.Load()
}

// TenantInflight returns the current in-flight count for one
// (upstream, tenantID) pair. Returns 0 if either is unknown.
func (r *InflightRegistry) TenantInflight(upstream string, tenantID uuid.UUID) int64 {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	tmap, ok := r.tenant[upstream]
	if !ok {
		r.mu.RUnlock()
		return 0
	}
	c, exists := tmap[tenantID]
	r.mu.RUnlock()
	if !exists {
		return 0
	}
	return c.Load()
}

// Upstreams returns a snapshot of the registered upstream names. Useful
// for the tick goroutine (Plan 05-05) to iterate the global gauge.
func (r *InflightRegistry) Upstreams() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.global))
	for n := range r.global {
		out = append(out, n)
	}
	return out
}

// AddUpstream registers a new upstream in the registry if not already
// present. Used by the upstreams hot-reload listener (WR-05) so that
// rows inserted via `gatewayctl upstreams create` start being tracked
// without a gateway restart. No-op for upstreams that are already
// registered — preserves any existing counters in flight.
func (r *InflightRegistry) AddUpstream(upstream string) {
	if r == nil || upstream == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.global[upstream]; exists {
		return
	}
	r.global[upstream] = &atomic.Int64{}
	if _, exists := r.tenant[upstream]; !exists {
		r.tenant[upstream] = make(map[uuid.UUID]*atomic.Int64)
	}
}

// TenantsForUpstream returns a snapshot of tenant UUIDs that currently
// have a counter for the given upstream. Used by the tick goroutine to
// publish gateway_inflight_tenant gauges (Plan 05-05). Cardinality
// bounded by D-D4 budget (~18 series).
func (r *InflightRegistry) TenantsForUpstream(upstream string) []uuid.UUID {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	tmap, ok := r.tenant[upstream]
	if !ok {
		return nil
	}
	out := make([]uuid.UUID, 0, len(tmap))
	for tid := range tmap {
		out = append(out, tid)
	}
	return out
}
