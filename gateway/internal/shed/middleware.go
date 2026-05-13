// Package shed (middleware.go): chi HTTP middleware that turns FSM state
// + per-tenant cap into a routing decision. Must be mounted AFTER schedule
// (Phase 4) and BEFORE the proxy dispatcher. Implements CONTEXT D-B4
// decision tree exactly. Writes the decision into the request context so
// audit + dispatcher can differentiate "routed off-hours" vs
// "shed_saturated".
//
// Decision tree (D-B4):
//
//  01. auth missing                    → next (defensive fallthrough)
//  02. tenant_id malformed UUID        → next (defensive fallthrough)
//  03. tenant snapshot missing         → next (tenant snapshot stale)
//  04. schedule already overrode       → stamp shed_decision=skipped_peak_offhours + next
//  05. route does not classify         → next (admin / health / etc.)
//  06. tier-0 upstream not configured  → next (dispatcher handles 503)
//  07. FSM != ON (Off/Armed/Recovering)→ trackAndPass (Inc/Dec + p95 record)
//  08. FSM=ON + inflight < cap         → trackAndPass (slot livre)
//  09. FSM=ON + inflight ≥ cap + normal+ tier-1 available → divert to tier-1
//     10a. FSM=ON + inflight ≥ cap + sensitive → 503 upstream_saturated_for_sensitive_tenant + Retry-After: 5 (D-B3)
//     10b. FSM=ON + inflight ≥ cap + normal + no tier-1 → 503 all_chat_upstreams_saturated + Retry-After: 30 (D-D1)
//
// Every decision emits exactly one GatewayShedDecisions{upstream,reason}
// counter sample so dashboards can break down routing volume by cause.
package shed

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/tenants"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// UpstreamResolver is the subset of *upstreams.Loader the shed middleware
// consumes. Declared as an interface so tests can pass an in-memory fake
// without standing up a real Postgres pool. *upstreams.Loader satisfies
// this surface by signature compatibility — see middleware_test.go
// compile-time guard.
type UpstreamResolver interface {
	Resolve(role string, tier int) (upstreams.UpstreamConfig, bool)
}

// TenantLookup is the subset of *tenants.Loader the shed middleware
// consumes. Same purpose as UpstreamResolver — kept on the package
// surface so unit tests do not need to construct a real snapshot.
// *tenants.Loader satisfies this surface; the compile-time guard lives
// in middleware_test.go.
type TenantLookup interface {
	Get(tenantID uuid.UUID) (tenants.TenantConfig, error)
}

// MiddlewareDeps carries wiring for the shed chi middleware. All
// pointer fields are required; nil values short-circuit the middleware
// to fallthrough in main.go (see the wiring guard there).
//
// RoleFor maps URL paths to internal role names ("llm" | "stt" |
// "embed" | ""). When nil the package default defaultClassifyRoute is
// used; tests inject custom mappings only when a new route is added.
type MiddlewareDeps struct {
	Loader   UpstreamResolver
	Tenants  TenantLookup
	Set      *Set
	Inflight *InflightRegistry
	Latency  map[string]*LatencyRing
	RoleFor  func(path string) string
}

// defaultClassifyRoute maps OpenAI-compatible paths to internal role
// names. Matches the conventions in gateway/internal/quota/enforcer.go
// classifyRoute (chat/audio/embed exact-match; everything else returns
// the empty string so the shed middleware skips evaluation entirely).
func defaultClassifyRoute(path string) string {
	switch {
	case strings.HasPrefix(path, "/v1/chat/completions"):
		return "llm"
	case strings.HasPrefix(path, "/v1/completions"):
		return "llm"
	case strings.HasPrefix(path, "/v1/audio/transcriptions"):
		return "stt"
	case strings.HasPrefix(path, "/v1/embeddings"):
		return "embed"
	}
	return ""
}

// Middleware returns a chi middleware applying the Phase 5 shedding
// decision tree. Noop (next) on any wiring/tenant error — this
// middleware is "soft" and never fails the request on its own (except
// for the documented 503 sensitive/tier1-unavailable branches).
//
// The middleware is mounted in main.go between schedule and the
// per-role dispatcher (D-B4 chain order). When deps fields are nil the
// caller is expected to guard the mount; this function assumes Loader,
// Tenants, Set, and Inflight are non-nil for the decision path.
func Middleware(d MiddlewareDeps, log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	log = log.With("module", "SHED")
	roleFor := d.RoleFor
	if roleFor == nil {
		roleFor = defaultClassifyRoute
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Branch 01 — auth missing. The chain mounts shed AFTER
			// auth.Middleware so this only fires when a route is
			// reached without auth (e.g. an internal /admin path
			// mis-mounted under /v1; defensive guard).
			ac, ok := auth.FromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			// Branch 02 — malformed tenant_id. auth.Middleware
			// already guarantees this is a UUID string but parse
			// defensively because a future refactor could relax that.
			tenantID, perr := uuid.Parse(ac.TenantID)
			if perr != nil {
				next.ServeHTTP(w, r)
				return
			}

			// Branch 03 — tenant snapshot missing. The tenants
			// loader is eventually consistent; a freshly inserted
			// tenant may not yet appear. Pass through and rely on
			// the dispatcher's own checks.
			tcfg, terr := d.Tenants.Get(tenantID)
			if terr != nil {
				next.ServeHTTP(w, r)
				return
			}

			// Branch 04 — schedule already overrode (D-D3 noop). The
			// shed middleware does not override on top of a peak
			// off-hours decision; just stamp the decision for the
			// dashboard fanout and forward.
			if auditctx.UpstreamOverrideFromContext(r.Context()) != "" {
				ctx := auditctx.WithShedDecision(r.Context(), "skipped_peak_offhours")
				obs.GatewayShedDecisions.WithLabelValues("", "skipped_peak_offhours").Inc()
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Branch 05 — unmapped route. /admin/*, /health, etc.
			// fall through without any shed stamping.
			role := roleFor(r.URL.Path)
			if role == "" {
				next.ServeHTTP(w, r)
				return
			}

			// Branch 06 — tier-0 upstream missing (mis-configured
			// or in the middle of a hot-reload). Pass through; the
			// dispatcher will emit 503 upstream_unavailable.
			t0, ok := d.Loader.Resolve(role, 0)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			// Branch 07 — FSM not StateOn. Off / Armed / Recovering
			// all keep tier-0; trackAndPass records the latency
			// sample so the FSM ticker can compute p95.
			fsm, _ := d.Set.Get(t0.Name)
			if fsm == nil || fsm.State() != StateOn {
				d.trackAndPass(w, r, next, t0.Name, tenantID)
				return
			}

			// Branch 08 — FSM=ON but tenant still under its cap.
			// Slot livre — tier-0 stays.
			capRole := resolveCapForRole(tcfg, role)
			if capRole == 0 {
				capRole = defaultCapForRole(role)
			}
			inflight := d.Inflight.TenantInflight(t0.Name, tenantID)
			if inflight < int64(capRole) {
				d.trackAndPass(w, r, next, t0.Name, tenantID)
				return
			}

			// Branch 10a — sensitive tenant over cap. LGPD: never
			// shed to external. 503 + Retry-After: 5 (D-B3).
			if ac.DataClass == auth.DataClassSensitive {
				ctx := auditctx.WithShedDecision(r.Context(), auditctx.UpstreamShedBlockedSensitiveValue)
				ctx = auditctx.WithUpstreamOverride(ctx, auditctx.UpstreamShedBlockedSensitiveValue)
				obs.GatewayShedDecisions.WithLabelValues(t0.Name, "sensitive_capped").Inc()
				obs.GatewayShedBlockedSensitive.WithLabelValues(tcfg.Slug).Inc()
				// Mutate the request in place so the audit middleware
				// (which reads r.Context() AFTER next returns) sees
				// the override stamp. Same pattern as
				// proxy.dispatcher.writeSensitiveBlock.
				*r = *r.WithContext(ctx)
				w.Header().Set("Retry-After", "5")
				log.Warn("shed blocked sensitive tenant",
					"tenant", tcfg.Slug, "upstream", t0.Name,
					"inflight", inflight, "cap", capRole)
				httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
					"service_unavailable",
					"upstream_saturated_for_sensitive_tenant",
					"Primary upstream is saturated; sensitive-data tenants cannot be routed to external providers.")
				return
			}

			// Branch 09 — normal tenant over cap. Divert to tier-1.
			t1, ok := d.Loader.Resolve(role, 1)
			if !ok {
				// Branch 10b — no tier-1 configured (D-D1). 503 with
				// Retry-After: 30. Hardcoded per RESEARCH Pitfall 7
				// (Fireworks does not emit Retry-After standardized).
				ctx := auditctx.WithShedDecision(r.Context(), auditctx.UpstreamShedTier1UnavailableValue)
				ctx = auditctx.WithUpstreamOverride(ctx, auditctx.UpstreamShedTier1UnavailableValue)
				*r = *r.WithContext(ctx)
				obs.GatewayShedDecisions.WithLabelValues(t0.Name, "tier1_unavailable").Inc()
				w.Header().Set("Retry-After", "30")
				log.Warn("shed tier-1 unavailable",
					"tenant", tcfg.Slug, "upstream", t0.Name)
				httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
					"service_unavailable",
					"all_chat_upstreams_saturated",
					"Primary saturated and secondary unavailable.")
				return
			}
			ctx := auditctx.WithShedDecision(r.Context(), auditctx.UpstreamShedSaturatedValue)
			ctx = auditctx.WithUpstreamOverride(ctx, t1.Name)
			obs.GatewayShedDecisions.WithLabelValues(t0.Name, "tenant_cap").Inc()
			obs.GatewayInflightTier1.WithLabelValues(t1.Name).Inc()
			defer obs.GatewayInflightTier1.WithLabelValues(t1.Name).Dec()
			log.Debug("shed routed to tier-1",
				"tenant", tcfg.Slug, "from", t0.Name, "to", t1.Name,
				"inflight", inflight, "cap", capRole)
			// Mutate the request in place so the audit middleware
			// (which reads r.Context() AFTER next returns, using the
			// same *http.Request pointer it captured upstream) sees
			// the shed_decision + upstream_override stamps. Same
			// pattern as Branches 10a/10b above and
			// proxy.dispatcher.writeSensitiveBlock.
			*r = *r.WithContext(ctx)
			next.ServeHTTP(w, r)
		})
	}
}

// trackAndPass increments the (upstream, tenant) inflight counter,
// schedules the matching decrement + latency-ring write via defer, and
// stamps shed_decision=passed on the request context before invoking
// next. This is the hot path for every request that is NOT shed away
// from tier-0 (Branches 07 and 08).
func (d MiddlewareDeps) trackAndPass(w http.ResponseWriter, r *http.Request, next http.Handler, upstream string, tenantID uuid.UUID) {
	d.Inflight.Inc(upstream, tenantID)
	start := time.Now()
	defer func() {
		d.Inflight.Dec(upstream, tenantID)
		elapsed := uint32(time.Since(start).Milliseconds())
		if ring, ok := d.Latency[upstream]; ok && ring != nil {
			ring.Record(elapsed)
		}
	}()
	ctx := auditctx.WithShedDecision(r.Context(), "passed")
	obs.GatewayShedDecisions.WithLabelValues(upstream, "passed").Inc()
	next.ServeHTTP(w, r.WithContext(ctx))
}

// resolveCapForRole reads the role-specific cap from TenantConfig.
// Returns 0 when the tenant has no explicit per-role cap configured —
// callers should fall back to defaultCapForRole in that case.
func resolveCapForRole(t tenants.TenantConfig, role string) int {
	switch role {
	case "llm":
		return t.LocalInflightMaxLLM
	case "stt":
		return t.LocalInflightMaxSTT
	case "embed":
		return t.LocalInflightMaxEmbed
	}
	return 0
}

// defaultCapForRole returns conservative fallback caps when TenantConfig
// values are zero (no per-tenant config yet). Matches the migration
// 0016 defaults so a freshly inserted tenant without explicit caps
// behaves identically to one with the defaults written explicitly.
func defaultCapForRole(role string) int {
	switch role {
	case "llm":
		return 4
	case "stt":
		return 2
	case "embed":
		return 8
	}
	return 1
}
