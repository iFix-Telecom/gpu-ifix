// Package schedule (middleware.go): pre-dispatch routing decision. Writes
// an upstream_override onto the request context when the tenant is in
// peak mode AND the clock is outside the business window (D-C2), so the
// dispatcher can skip tier-0 entirely (GPU may be suspended off-hours)
// and dispatch straight to the tier-1 external upstream.
//
// 24/7 tenants never get an override — their tier-0 breaker state drives
// the normal fallback chain (Phase 3 dispatcher).
//
// Metric: every decision increments GatewayScheduleRouting{tenant,decision}
// with decision ∈ {"local", "off_hours_external"} so dashboards can show
// which tenants are actively using the external path.
package schedule

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/tenants"
)

// upstreamForTier maps a tier int (from DecideUpstreamTier) to the
// upstream name the dispatcher expects. Today only Tier1 emits an override
// (openrouter-chat is the sole peak-off-hours destination); Tier0 means
// "no override; follow the normal breaker chain".
//
// If Phase 5 adds STT/embed off-hours routing, extend here.
func upstreamForTier(tier int) string {
	if tier == Tier1 {
		return "openrouter-chat"
	}
	return ""
}

// Middleware returns a chi-compatible middleware that decides the upstream
// override based on the tenant's schedule policy. Consumed by the
// dispatcher via auditctx.UpstreamOverrideFromContext.
//
// Fallthroughs (all pass-through, no override):
//   - no auth context (handled by auth/rate-limit earlier in the chain)
//   - malformed tenant_id (should never happen; auth guarantees UUID shape)
//   - tenant snapshot missing (freshly added, pending refresh)
//
// The decision is resolved via schedule.DecideUpstreamTier which already
// handles nil Location + wrap-around peak windows.
func Middleware(loader *tenants.Loader, log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	log = log.With("module", "SCHEDULE")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ac, ok := auth.FromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			tenantID, perr := uuid.Parse(ac.TenantID)
			if perr != nil {
				next.ServeHTTP(w, r)
				return
			}
			cfg, err := loader.Get(tenantID)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			ctx := r.Context()
			tier := DecideUpstreamTier(cfg, time.Now())
			if name := upstreamForTier(tier); name != "" {
				// HI-03 fix (Phase 4 review): defence-in-depth against a
				// sensitive+peak tenant sneaking past the DB CHECK
				// constraint (e.g. `SET session_replication_role=replica`
				// during a migration, or an operator ALTER TABLE that drops
				// chk_sensitive_no_peak). Boot-time CheckSensitivePeakInvariant
				// is ONE shot; subsequent refreshes only WARN. If the
				// tenant's data_class == sensitive at this point, the DB
				// state is invalid — fail-fast on the request with the
				// Phase 3 sensitive envelope rather than routing it to
				// OpenRouter and violating LGPD.
				if cfg.DataClass == "sensitive" {
					log.Error("sensitive tenant in peak mode at request time; CHECK constraint bypassed",
						"tenant", cfg.Slug)
					obs.GatewayScheduleRouting.WithLabelValues(cfg.Slug, "blocked_sensitive_peak").Inc()
					// D-B3 contract (Phase 9 SC1 smoke): audit row MUST record
					// upstream="blocked_sensitive" on every RES-08 503 path so
					// the audit-decision gate + dashboards can isolate
					// sensitive-block rows from routine tier-0 503s. Schedule
					// short-circuits BEFORE the Phase 3 dispatcher (whose
					// writeSensitiveBlock sets the same override at
					// dispatcher.go:365), so we must set it here too. See
					// 11-08-EVIDENCE.md Segment B re-run for the gap report.
					*r = *r.WithContext(auditctx.WithUpstreamOverride(r.Context(),
						"blocked_sensitive"))
					w.Header().Set("Retry-After", "30")
					httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
						"service_unavailable", "upstream_unavailable_for_sensitive_tenant",
						"Sensitive tenant cannot be routed to external providers.")
					return
				}
				ctx = auditctx.WithUpstreamOverride(ctx, name)
				obs.GatewayScheduleRouting.WithLabelValues(cfg.Slug, "off_hours_external").Inc()
				log.Debug("schedule override",
					"tenant", cfg.Slug, "upstream", name)
			} else {
				obs.GatewayScheduleRouting.WithLabelValues(cfg.Slug, "local").Inc()
			}
			// Mutate the request in-place so downstream context stamps
			// (proxy.dispatcher.writeSensitiveBlock's upstream_override,
			// shed.trackAndPass's shed_decision) propagate back to the
			// audit.Middleware r pointer captured upstream. Using
			// r.WithContext(ctx) here would create a new *http.Request
			// and silently swallow the downstream mutations — see
			// .planning/debug/audit-blocked-sensitive-override-not-propagated.md.
			*r = *r.WithContext(ctx)
			next.ServeHTTP(w, r)
		})
	}
}
