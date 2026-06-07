package upstreams

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// snapshot is the immutable view of the upstreams table the Loader serves
// from its hot path. Built fresh on every Refresh and atomically swapped
// in via atomic.Pointer[snapshot] so reads are lock-free.
type snapshot struct {
	byName     map[string]UpstreamConfig
	byRoleTier map[RoleTier]UpstreamConfig
	ordered    []UpstreamConfig
}

// loaderQueries isolates the sqlc surface so tests can stub it without
// standing up a real Postgres pool. Mirrors the resolverQueries pattern
// in gateway/internal/models/resolver.go.
//
// Phase 11.2 (D-B5′/D-B6′) — ListEnabledUpstreamsRow returns the new
// per-query Row struct that includes tier_priority (sqlc autogen).
type loaderQueries interface {
	ListEnabledUpstreams(ctx context.Context) ([]gen.ListEnabledUpstreamsRow, error)
}

// Loader holds the in-memory authoritative snapshot of ai_gateway.upstreams.
// Readers call Resolve/Get/All on the hot path (atomic.Pointer — lock-free).
// Refresh is called at boot + on each LISTEN/NOTIFY from upstreams_changed.
//
// tier0Override is the Plan 06-08 emergency-pod dispatcher integration
// point (D-E3). When the emerg reconciler reaches StateEmergencyActive it
// calls OverrideTier0("llm", podURL); Resolve consults this map BEFORE
// the snapshot for tier=0 reads and returns an UpstreamConfig with
// URL=podURL + Name="emergency_pod_llm" + IsEmergency=true. RestoreTier0
// clears the override on cutback (StateEmergencyActive → StateRecovering).
//
// LLM-only in v1 per CONTEXT.md D-E3. The map is initialised in
// NewLoader / NewLoaderInMemory with a single "llm" key — STT/embed
// continue tier-0 primary even during emergency (per CONTEXT D-E3).
// Adding another role requires extending the map at construction time
// (no runtime mutation of the map keys — only the atomic.Pointer values).
//
// All reads on Resolve's hot path are lockless atomic.Pointer.Load.
type Loader struct {
	pool          *pgxpool.Pool
	q             loaderQueries
	snap          atomic.Pointer[snapshot]
	log           *slog.Logger
	tier0Override map[string]*atomic.Pointer[string]
}

// NewLoader constructs the Loader and performs the initial Refresh.
// Returns an error if the initial SELECT fails (boot MUST fail-fast if
// the upstreams table is unreadable).
func NewLoader(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) (*Loader, error) {
	l := &Loader{
		pool:          pool,
		q:             gen.New(pool),
		log:           log.With("module", "UPSTREAMS"),
		tier0Override: newTier0OverrideMap(),
	}
	if err := l.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("initial upstreams refresh: %w", err)
	}
	return l, nil
}

// newTier0OverrideMap returns the canonical Plan 06-08 emergency override
// map. LLM-only in v1 per CONTEXT.md D-E3 — adding STT/embed roles to
// this map enables runtime override for those roles too. The map keys are
// fixed at construction time (no runtime ADD/DEL); only the atomic.Pointer
// values are mutated.
//
// Phase 6.6 — primary pod overrides 3 roles (was LLM-only in Phase 6).
// OverrideTier0/RestoreTier0 são role-agnostic; só o map precisa crescer.
// See gateway/internal/primary/reconciler.go markReady (Plan 06.6-06a).
//
// Phase 06.7 (D-03/D-11) — the TTS engine moved onto the primary pod and
// embed moved OFF it (relocated to n8n-ia-vm CPU as a STATIC tier-0 upstream
// via UPSTREAM_EMBED_URL). So the dynamic-override roster is now
// {llm, stt, tts}: "tts" is a first-class dynamic-override role and "embed"
// is removed (Resolve("embed",0) still serves the static row unchanged
// because there is no override slot to consult).
func newTier0OverrideMap() map[string]*atomic.Pointer[string] {
	return map[string]*atomic.Pointer[string]{
		"llm": new(atomic.Pointer[string]),
		"stt": new(atomic.Pointer[string]),
		"tts": new(atomic.Pointer[string]),
	}
}

// Refresh loads all enabled rows and atomically swaps in a new snapshot.
// Missing env-var values (url_env not set) cause the row to be SKIPPED
// with a warn log — the dispatcher returns 503 if requested. This keeps
// the gateway bootable even when a fallback provider's bearer is not yet
// configured (CONTEXT.md "Plumbing" / 03-04-PLAN must_haves.truths).
func (l *Loader) Refresh(ctx context.Context) error {
	rows, err := l.q.ListEnabledUpstreams(ctx)
	if err != nil {
		obs.UpstreamsReloadTotal.WithLabelValues("error").Inc()
		return fmt.Errorf("list enabled upstreams: %w", err)
	}
	s := &snapshot{
		byName:     make(map[string]UpstreamConfig, len(rows)),
		byRoleTier: make(map[RoleTier]UpstreamConfig, len(rows)),
		ordered:    make([]UpstreamConfig, 0, len(rows)),
	}
	for _, r := range rows {
		// Phase 5 / WR-02: reject upstream names starting with "force:"
		// because they collide with the gw:shed:force:{upstream} Redis
		// namespace used by the shed-force operator override. A row named
		// "force:something" would write its state mirror to
		// gw:shed:force:something — indistinguishable from a real
		// override key and silently filtered out by AllShedStateKeys.
		if strings.HasPrefix(r.Name, "force:") {
			l.log.Warn("upstream name reserved (collides with gw:shed:force:* namespace); row skipped",
				"upstream", r.Name,
				"status", "reserved_name")
			continue
		}
		url := os.Getenv(r.UrlEnv)
		if url == "" {
			l.log.Warn("upstream url env var missing; row skipped",
				"upstream", r.Name,
				"url_env", r.UrlEnv,
				"status", "missing_url_env")
			continue
		}
		authBearerEnv := ""
		authBearer := ""
		if r.AuthBearerEnv.Valid {
			authBearerEnv = r.AuthBearerEnv.String
			authBearer = os.Getenv(authBearerEnv)
			if authBearer == "" {
				l.log.Warn("upstream auth bearer env missing; row kept but auth will be empty",
					"upstream", r.Name,
					"auth_bearer_env", authBearerEnv,
					"status", "missing_auth_bearer_env")
			}
		}
		var weight *int32
		if r.Weight.Valid {
			w := r.Weight.Int32
			weight = &w
		}
		u := UpstreamConfig{
			ID:            r.ID,
			Name:          r.Name,
			Role:          r.Role,
			Tier:          int(r.Tier),
			TierPriority:  int(r.TierPriority),
			URL:           url,
			AuthBearer:    authBearer,
			AuthBearerEnv: authBearerEnv,
			Enabled:       r.Enabled,
			Weight:        weight,
			CircuitConfig: parseCircuitConfig(r.CircuitConfig),
		}
		s.byName[u.Name] = u
		// Phase 11.2 (D-B5′): when multiple rows share (role, tier), the
		// lowest tier_priority wins in byRoleTier (rows are loaded in
		// ASC tier_priority order; first writer wins). This preserves
		// single-tier-1 backward-compat for llm/embed/tts (only one row
		// per (role, tier)) while STT's multi-tier-1 cascade is exposed
		// via ResolveAllTier1.
		if _, exists := s.byRoleTier[RoleTier{Role: u.Role, Tier: u.Tier}]; !exists {
			s.byRoleTier[RoleTier{Role: u.Role, Tier: u.Tier}] = u
		}
		s.ordered = append(s.ordered, u)
	}
	// Stable order for All() — by (role, tier, tier_priority) so callers
	// see a deterministic listing in /v1/health/upstreams + gatewayctl
	// output. Phase 11.2 (D-B5′): tier_priority breaks ties when multiple
	// rows share (role, tier) — STT cascade gemini(10)→groq(15)→openai(20).
	sort.SliceStable(s.ordered, func(i, j int) bool {
		if s.ordered[i].Role != s.ordered[j].Role {
			return s.ordered[i].Role < s.ordered[j].Role
		}
		if s.ordered[i].Tier != s.ordered[j].Tier {
			return s.ordered[i].Tier < s.ordered[j].Tier
		}
		return s.ordered[i].TierPriority < s.ordered[j].TierPriority
	})
	l.snap.Store(s)
	obs.UpstreamsReloadTotal.WithLabelValues("ok").Inc()
	l.log.Info("upstreams refreshed", "rows", len(s.byName))
	return nil
}

// Get returns the upstream by name + found flag. Lock-free (atomic.Pointer read).
func (l *Loader) Get(name string) (UpstreamConfig, bool) {
	s := l.snap.Load()
	if s == nil {
		return UpstreamConfig{}, false
	}
	u, ok := s.byName[name]
	return u, ok
}

// Resolve returns the upstream for (role, tier). Hot path used by the
// dispatcher: tier-0 CLOSED → primary; tier-0 OPEN → Resolve(role, 1) for fallback.
//
// Plan 06-08 (D-E3) — when an emergency-pod override is active for this
// role AND tier==0, Resolve returns an ephemeral UpstreamConfig with
// URL=overrideURL, Name="emergency_pod_<role>", IsEmergency=true. The
// override is NEVER applied to tier=1 (fallback chain stays untouched).
// All other fields (CircuitConfig, AuthBearer, etc.) are inherited from
// the underlying tier-0 row so the dispatcher's breaker + auth path
// remains intact (the emergency pod is a Vast.ai 4090 with the same
// llama.cpp stack — it accepts the same auth as local-llm if any).
func (l *Loader) Resolve(role string, tier int) (UpstreamConfig, bool) {
	s := l.snap.Load()
	if s == nil {
		return UpstreamConfig{}, false
	}
	if tier == 0 {
		if p, ok := l.tier0Override[role]; ok {
			if overridePtr := p.Load(); overridePtr != nil && *overridePtr != "" {
				u, found := s.byRoleTier[RoleTier{Role: role, Tier: 0}]
				if !found {
					// No tier-0 row to base the override on — return the
					// override URL with a synthesised name. Without a base
					// row we have no CircuitConfig, no auth — accept the
					// degradation rather than fail-close.
					return UpstreamConfig{
						Name:        "emergency_pod_" + role,
						Role:        role,
						Tier:        0,
						URL:         *overridePtr,
						Enabled:     true,
						IsEmergency: true,
					}, true
				}
				u.URL = *overridePtr
				u.Name = "emergency_pod_" + role
				u.IsEmergency = true
				return u, true
			}
		}
	}
	u, ok := s.byRoleTier[RoleTier{Role: role, Tier: tier}]
	return u, ok
}

// ResolveAllTier1 returns every enabled tier-1 upstream for the given
// role, ordered by tier_priority ASC (lower wins). Phase 11.2 (D-B5′)
// — STT cascade dispatcher iterates this slice on pod-OFF, dispatching
// to the first CLOSED-breaker upstream:
//
//	ResolveAllTier1("stt") → [gemini-stt(10), groq-whisper(15), openai-whisper(20)]
//
// Backward-compat: roles with a single tier-1 row (llm/embed/tts) return
// a slice of length 1, so existing single-tier-1 callers can be rewritten
// to ResolveAllTier1 without behavior change.
//
// Lock-free (atomic.Pointer read on the snapshot). Returns nil when the
// snapshot is uninitialised; returns an empty slice (not nil) when the
// role has no tier-1 rows.
func (l *Loader) ResolveAllTier1(role string) []UpstreamConfig {
	s := l.snap.Load()
	if s == nil {
		return nil
	}
	var out []UpstreamConfig
	for _, u := range s.ordered {
		if u.Role == role && u.Tier == 1 && u.Enabled {
			out = append(out, u)
		}
	}
	// s.ordered is already sorted by (role, tier, tier_priority) ASC in
	// Refresh, so the filtered slice is in the desired order. SliceStable
	// is a defensive belt-and-suspenders guard against a future ordering
	// regression in Refresh.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].TierPriority < out[j].TierPriority
	})
	return out
}

// OverrideTier0 sets a runtime tier-0 override URL for the given role.
// Plan 06-08 (D-E3) — called by emerg.Reconciler.markHealthy when the
// emergency pod becomes healthy, so the dispatcher routes tier-0 LLM
// traffic to the Vast.ai pod instead of the (failed) local-llm primary.
//
// No-op when role is not in the override map (only "llm" in v1) — non-LLM
// roles continue tier-0 primary even during emergency. Race-free via
// atomic.Pointer[string].Store; reads on Resolve hot path are lockless.
//
// The override persists until RestoreTier0(role) clears it. A second
// OverrideTier0 call replaces the prior URL atomically.
func (l *Loader) OverrideTier0(role, url string) {
	p, ok := l.tier0Override[role]
	if !ok {
		return
	}
	if l.log != nil {
		l.log.Info("tier-0 override activated (emerg)",
			"role", role, "override_url", url)
	}
	urlCopy := url
	p.Store(&urlCopy)
}

// RestoreTier0 clears the tier-0 override for the given role. Plan 06-08
// (D-E3) — called by emerg.Reconciler.evaluateEmergencyActive when the
// primary recovers (cutback: StateEmergencyActive → StateRecovering), so
// dispatcher routes tier-0 LLM back to local-llm.
//
// No-op when role is not in the override map. Idempotent — calling
// RestoreTier0 when no override is active is a Store(nil) of an already-
// nil pointer (cheap atomic).
func (l *Loader) RestoreTier0(role string) {
	p, ok := l.tier0Override[role]
	if !ok {
		return
	}
	if l.log != nil {
		l.log.Info("tier-0 override cleared (cutback)", "role", role)
	}
	p.Store(nil)
}

// Tier0OverrideURL reports the active tier-0 override URL for a role.
// Returns (url, true) when an override is currently set for the role, or
// ("", false) when the role has no override slot (e.g. "embed", which is a
// static tier-0 upstream post-Phase-06.7) or the slot is present but unset.
//
// Phase 06.7 (D-13) — the primary reconciler's Pitfall #11 re-assert path
// (Plan 06.7-08) reads this to decide whether the dynamic tier-0 slot for a
// role was cleared (e.g. by a pod replacement) and must be re-overridden.
// Mirrors the lockless atomic.Pointer read used by Resolve's hot path.
func (l *Loader) Tier0OverrideURL(role string) (string, bool) {
	p, ok := l.tier0Override[role]
	if !ok {
		return "", false
	}
	overridePtr := p.Load()
	if overridePtr == nil || *overridePtr == "" {
		return "", false
	}
	return *overridePtr, true
}

// All returns all upstreams ordered by (role, tier). Used by /v1/health/upstreams
// and gatewayctl upstreams list. Returns a defensive copy so callers cannot
// mutate the snapshot's internal slice.
func (l *Loader) All() []UpstreamConfig {
	s := l.snap.Load()
	if s == nil {
		return nil
	}
	out := make([]UpstreamConfig, len(s.ordered))
	copy(out, s.ordered)
	return out
}

// Names returns the list of all upstream names in the current snapshot.
// Used by breaker.Set.Rebuild on hot-reload (Wave 2 — 03-05).
func (l *Loader) Names() []string {
	s := l.snap.Load()
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.byName))
	for n := range s.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
