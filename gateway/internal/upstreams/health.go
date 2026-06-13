// Package upstreams (health.go): Phase 3 multi-upstream health aggregator.
//
// Replaces the Phase 2 implementation that proxied to the pod's
// :9100 health-bridge. Phase 3 derives state in-process from
// breaker.Set.EffectiveStateSnapshot() (live circuit-breaker state
// honoring operator force-overrides installed via
// `gatewayctl breaker force-open`, per SEED-005) + Loader.All()
// (the 6-row config) + the upstreams.last_probe_* columns the probe
// loop populates (Task 1).
//
// Cache TTL is 2s (CONTEXT.md "Claude's Discretion / GET
// /v1/health/upstreams endpoint"). Phase 7 dashboard polls this
// endpoint at most every refresh-interval-seconds; the 2s cache
// absorbs concurrent operator clicks without re-snapshotting the
// breaker map on every request.
package upstreams

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

// healthCacheTTL is the in-memory cache TTL for /v1/health/upstreams.
// 2s is short enough that the dashboard sees near-real-time state but
// long enough to absorb operator click bursts.
const healthCacheTTL = 2 * time.Second

// healthResponse is the JSON shape returned to clients.
type healthResponse struct {
	Status    string                    `json:"status"`
	Upstreams map[string]upstreamStatus `json:"upstreams"`
}

// upstreamStatus is one entry under .upstreams. NOTE: AuthBearer is
// NEVER included (T-03-04-03) — only public config + live state.
//
// Phase 12 (RES-12/D-14) ADDITIVE fields — all `omitempty` so a no-override
// response is byte-for-byte identical to the pre-12-01 payload and existing
// consumers (gatewayctl, dashboard, monitors) see no schema change:
//
//   - OverrideActive / OverrideSource: set on the EFFECTIVE tier-0 entry
//     (the emergency pod) when a tier-0 override is active for its role.
//     OverrideSource is a role/source LABEL only (e.g. "primary pod") — it
//     NEVER carries the raw pod URL or any secret (T-12-02).
//   - Overridden: set on the static tier-0 row that an active override
//     replaced. The row is still listed (additive) but is excluded from the
//     aggregate-status computation so a healthy pod does not yield "failed"
//     (D-14).
type upstreamStatus struct {
	State           string `json:"state"` // "closed" | "half-open" | "open" | "forced-open" | "unknown"
	Role            string `json:"role"`
	Tier            int    `json:"tier"`
	LastProbeMs     *int32 `json:"last_probe_ms,omitempty"`
	LastProbeAt     string `json:"last_probe_at,omitempty"`
	LastProbeStatus string `json:"last_probe_status,omitempty"`

	// Phase 12 RES-12/D-14 — additive override surface (omitempty).
	OverrideActive bool   `json:"override_active,omitempty"`
	OverrideSource string `json:"override_source,omitempty"`
	Overridden     bool   `json:"overridden,omitempty"`
}

// cachedHealthResponse is the per-handler cache slot.
type cachedHealthResponse struct {
	storedAt time.Time
	body     []byte
	status   int
}

// NewHealthHandler returns the GET /v1/health/upstreams handler. Phase 3
// signature: derives every value in-process from loader + bs (no HTTP
// proxy to the pod's :9100). The handler is safe for concurrent use;
// state lives behind a mutex-protected cache slot with 2s TTL.
func NewHealthHandler(loader *Loader, bs *breaker.Set, log *slog.Logger) http.HandlerFunc {
	log = log.With("module", "UPSTREAMS_HEALTH")
	var (
		mu    sync.Mutex
		cache cachedHealthResponse
	)
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if time.Since(cache.storedAt) < healthCacheTTL && cache.body != nil {
			b, s := cache.body, cache.status
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(s)
			_, _ = w.Write(b)
			return
		}
		mu.Unlock()

		body, status, err := buildHealthResponse(loader, bs)
		if err != nil {
			log.Error("marshal health response", "err", err,
				"request_id", httpx.RequestIDFrom(r.Context()))
			httpx.WriteOpenAIError(w, http.StatusInternalServerError,
				"api_error", "health_encoding_failed",
				"Failed to encode health response.")
			return
		}

		mu.Lock()
		cache = cachedHealthResponse{
			storedAt: time.Now(),
			body:     body,
			status:   status,
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
}

// buildHealthResponse renders the JSON body + HTTP status from a fresh
// snapshot. Pulled out of the handler so tests can drive it without
// going through HTTP.
//
// Status derivation (CONTEXT.md "Claude's Discretion"):
//
//	ok       — every role's tier-0 upstream breaker is CLOSED
//	degraded — at least one role has tier-0 OPEN/HALF_OPEN but its tier-1 is CLOSED
//	failed   — at least one role has 0 CLOSED upstreams across all tiers
//
// HTTP status: 200 for ok|degraded; 503 for failed (so monitors that
// only inspect HTTP code can alert without parsing JSON).
//
// If loader is nil OR returns 0 upstreams (boot misconfig / Phase 3
// wiring incomplete), status is "failed" with HTTP 503 and an empty
// upstreams map. This keeps the handler self-defensive against
// uninitialized callers.
func buildHealthResponse(loader *Loader, bs *breaker.Set) ([]byte, int, error) {
	resp := healthResponse{Upstreams: map[string]upstreamStatus{}}
	if loader == nil {
		body, err := json.Marshal(healthResponse{Status: "failed", Upstreams: map[string]upstreamStatus{}})
		return body, http.StatusServiceUnavailable, err
	}

	// RES-12: resolve tier-0 per role through the SAME override-honoring path
	// the dispatcher + prober use (Resolve(role,0) via ResolveTier0Roles)
	// instead of enumerating loader.All(). Pitfall 3: this is the SECOND
	// All() call site (the prober is the first) — both MUST switch or the
	// health endpoint keeps returning 503 with a live pod (SEED-012).
	tier0 := loader.ResolveTier0Roles()
	all := loader.All()
	if len(tier0) == 0 && len(all) == 0 {
		body, err := json.Marshal(healthResponse{Status: "failed", Upstreams: map[string]upstreamStatus{}})
		return body, http.StatusServiceUnavailable, err
	}

	var snap map[string]string
	if bs != nil {
		// SEED-005: honor operator force-override (gw:breaker:force:{name}).
		// Snapshot() reads raw FSM only; EffectiveStateSnapshot() emits
		// "forced-open" when a force-override is active so dashboards +
		// smoke pre-condition gates see routing-layer reality.
		snap = bs.EffectiveStateSnapshot()
	}

	stateFor := func(name string) string {
		if snap != nil {
			if s, ok := snap[name]; ok {
				return s
			}
		}
		return "unknown"
	}

	// tier0Closed tracks per-role: is the EFFECTIVE tier-0 breaker CLOSED?
	// roleHasClosed tracks per-role: is ANY tier CLOSED?
	// overriddenStatic: static tier-0 rows replaced by an active override —
	// listed additively but excluded from the aggregate (D-14).
	tier0Closed := map[string]bool{}
	roleHasClosed := map[string]bool{}
	rolesPresent := map[string]bool{}
	tier0RolesPresent := map[string]bool{}
	overriddenStatic := map[string]bool{}

	// 1. Effective tier-0 entries (emergency pod under override; static row
	//    otherwise). These drive the tier-0 aggregate.
	for _, res := range tier0 {
		eff := res.Effective
		st := stateFor(eff.Name)
		us := upstreamStatus{State: st, Role: eff.Role, Tier: 0}
		if res.Overridden {
			us.OverrideActive = true
			// Source LABEL only — never the raw pod URL (T-12-02).
			us.OverrideSource = "primary pod"
			if res.ReplacedStaticName != "" {
				overriddenStatic[res.ReplacedStaticName] = true
			}
		}
		resp.Upstreams[eff.Name] = us
		rolesPresent[eff.Role] = true
		tier0RolesPresent[eff.Role] = true
		if st == "closed" {
			roleHasClosed[eff.Role] = true
			tier0Closed[eff.Role] = true
		}
	}

	// 2. Remaining rows from the snapshot: tier-1 entries, plus any static
	//    tier-0 row that an override replaced (listed additively as standby,
	//    excluded from the aggregate).
	for _, u := range all {
		if u.Tier == 0 {
			if overriddenStatic[u.Name] {
				// Additive standby marker. NOT folded into aggregate maps.
				us := upstreamStatus{
					State:      stateFor(u.Name),
					Role:       u.Role,
					Tier:       0,
					Overridden: true,
				}
				resp.Upstreams[u.Name] = us
			}
			// Non-overridden tier-0 rows are already represented by the
			// effective resolution above (same name) — skip to avoid a
			// double-write that would just re-set the same entry.
			continue
		}
		st := stateFor(u.Name)
		resp.Upstreams[u.Name] = upstreamStatus{State: st, Role: u.Role, Tier: u.Tier}
		rolesPresent[u.Role] = true
		if st == "closed" {
			roleHasClosed[u.Role] = true
		}
	}

	httpStatus := http.StatusOK
	switch {
	case allTier0Closed(tier0RolesPresent, tier0Closed):
		resp.Status = "ok"
	case allRolesHaveAnyClosed(rolesPresent, roleHasClosed):
		resp.Status = "degraded"
	default:
		resp.Status = "failed"
		httpStatus = http.StatusServiceUnavailable
	}

	body, err := json.Marshal(resp)
	return body, httpStatus, err
}

// allTier0Closed returns true iff every role with a tier-0 upstream
// has that tier-0 in CLOSED state. Empty input (no tier-0 rows at all)
// returns false to err on the side of "failed" — operators want a
// loud signal when configuration is missing.
func allTier0Closed(tier0Roles, tier0Closed map[string]bool) bool {
	if len(tier0Roles) == 0 {
		return false
	}
	for role := range tier0Roles {
		if !tier0Closed[role] {
			return false
		}
	}
	return true
}

// allRolesHaveAnyClosed returns true iff every role has at least one
// CLOSED upstream across all tiers. Empty input returns false (same
// reason as above).
func allRolesHaveAnyClosed(rolesPresent, roleHasClosed map[string]bool) bool {
	if len(rolesPresent) == 0 {
		return false
	}
	for role := range rolesPresent {
		if !roleHasClosed[role] {
			return false
		}
	}
	return true
}
