// Package auditctx is a tiny zero-dependency helper that lets handlers
// stash overrides on the request context that the audit middleware later
// reads when building the per-request audit_log row.
//
// It exists as a separate package (not in audit) to break the import
// cycle that arose in Phase 3: gateway/internal/audit imports
// gateway/internal/proxy (for the SSE interceptor's IsSSEResponse
// helper); meanwhile gateway/internal/proxy.dispatcher needs to set
// audit overrides. A separate auditctx package is consumed by both
// without circular import.
package auditctx

import (
	"context"
	"sync"
)

type upstreamOverrideKey struct{}

// WithUpstreamOverride returns a derived context that, when handed to
// the audit middleware via the request, makes the audit row record the
// given upstream value instead of the route-derived default. Used by
// the Phase 3 dispatcher to write upstream="blocked_sensitive" for
// CONTEXT.md D-B3 (sensitive-blocked audit row).
//
// The handler MUST update the request via r = r.WithContext(ctx) — or
// mutate the request struct in place when a derived context can't
// propagate up — so the audit middleware (which reads from the same
// r reference post-next.ServeHTTP) sees the value.
func WithUpstreamOverride(parent context.Context, upstream string) context.Context {
	return context.WithValue(parent, upstreamOverrideKey{}, upstream)
}

// UpstreamOverrideFrom returns the override or empty string when none
// is set. Safe on any context (returns empty by default).
func UpstreamOverrideFrom(ctx context.Context) string {
	if v, ok := ctx.Value(upstreamOverrideKey{}).(string); ok {
		return v
	}
	return ""
}

// UpstreamOverrideFromContext is a long-name alias of UpstreamOverrideFrom
// kept to match the Phase 4 plan's API contract (Plan 04-06 dispatcher +
// enforcer reference this spelling). Both forms read the same context key.
func UpstreamOverrideFromContext(ctx context.Context) string {
	return UpstreamOverrideFrom(ctx)
}

type billingUpstreamKey struct{}

// WithBillingUpstream stashes the resolved upstream name (e.g. "local-llm",
// "openrouter-chat", "local-embed") on the request context. Consumed by
// the Phase 4 billing UsageInterceptor when building billing.Event records
// at flush time — the interceptor runs inside ModifyResponse where the
// dispatcher's dispatch decision is already opaque.
//
// Separate from WithUpstreamOverride (which carries the schedule routing
// signal "off_hours" / "blocked_sensitive"). This key carries the FACTUAL
// upstream chosen by the dispatcher; the override signals INTENT.
func WithBillingUpstream(parent context.Context, upstream string) context.Context {
	return context.WithValue(parent, billingUpstreamKey{}, upstream)
}

// BillingUpstreamFrom returns the upstream name or empty string when none
// was set.
func BillingUpstreamFrom(ctx context.Context) string {
	if v, ok := ctx.Value(billingUpstreamKey{}).(string); ok {
		return v
	}
	return ""
}

// shedDecisionKey is a dedicated context key for the routing decision taken
// by shed middleware (CONTEXT D-B4). Distinct from upstreamOverrideKey used
// by schedule/dispatcher because audit needs BOTH signals — schedule routes
// "off-hours" to tier-1 AND shed routes "saturated" to tier-1, but the
// dashboard must distinguish them.
type shedDecisionKey struct{}

// WithShedDecision stamps the shed middleware's decision on the request
// context. Used by audit middleware to differentiate
// "upstream=openrouter-chat because schedule off-hours" vs
// "upstream=openrouter-chat because shed_saturated" in audit_log.
//
// Valid values: "passed" | "skipped_peak_offhours" | "shed_saturated" |
// "shed_blocked_sensitive" | "shed_tier1_unavailable".
func WithShedDecision(parent context.Context, decision string) context.Context {
	return context.WithValue(parent, shedDecisionKey{}, decision)
}

// ShedDecisionFromContext returns the shed decision stamped by the
// middleware. Returns "" if no middleware decision was recorded.
func ShedDecisionFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(shedDecisionKey{}).(string); ok {
		return v
	}
	return ""
}

// Phase 5 — audit_log.upstream reserved values (D-D4). Used by shed
// middleware + dispatcher to stamp distinct causes for identical-wire
// 503 responses OR tier-1 shed routing, letting audit_log disambiguate
// "openrouter-chat because breaker" vs "openrouter-chat because shed".
const (
	// UpstreamShedSaturatedValue is written when tier-0 FSM=ON and a
	// normal tenant exceeds its local_inflight_max_<role>; request is
	// routed to tier-1 (openrouter-chat) with this label in audit.
	UpstreamShedSaturatedValue = "shed_saturated"

	// UpstreamShedBlockedSensitiveValue is written when a sensitive-data
	// tenant hits the same FSM=ON + cap path; request is NOT routed to
	// tier-1 (LGPD); 503 with code "upstream_saturated_for_sensitive_tenant"
	// is returned and this label stamps the audit row (D-B3).
	UpstreamShedBlockedSensitiveValue = "shed_blocked_sensitive"

	// UpstreamShedTier1UnavailableValue is written when tier-0 sheds to
	// tier-1 but tier-1 itself is breaker-OPEN or 429; 503 with code
	// "all_chat_upstreams_saturated" is returned and this label stamps
	// the audit row (D-D1).
	UpstreamShedTier1UnavailableValue = "shed_tier1_unavailable"
)

// Phase 11.2 Plan 08 (D-B13 audit-distinguish fix): a request-id-keyed
// registry of the factual dispatched-to upstream. The audit middleware
// reads from this registry AFTER next.ServeHTTP returns so the
// audit_log.upstream column carries the cascade fall-through target
// name (local-stt, gemini-stt, groq-whisper, openai-whisper) instead of
// the route-derived "stt"/"llm"/"embed".
//
// Why a registry instead of ctx propagation: http.TimeoutHandler clones
// the request before passing it to the inner handler, breaking the
// `*r = *r.WithContext(...)` mutation pattern used by shed/schedule
// middlewares. The audit middleware sits OUTSIDE the TimeoutHandler
// wrap, so it captured the pre-clone *http.Request — any in-handler
// mutation is invisible. Registry is keyed by request_id (UUIDv7,
// emitted by httpx.RequestID before any wrap), so producer (dispatcher)
// and consumer (audit middleware) share the same logical key without
// needing the same *Request pointer.
//
// Lifetime: producer (dispatcher.dispatchTo) sets the value before
// invoking the proxy; consumer (audit.Middleware) reads + deletes
// AFTER next.ServeHTTP returns. The DELETE on read prevents unbounded
// growth even if a request panics — combined with the request_id-keyed
// design (one entry per in-flight request), worst case is the current
// request count. No timer-based GC needed.
var dispatchedUpstreamRegistry sync.Map // map[string]string — request_id → upstream

// SetDispatchedUpstream stamps the factual upstream name into the
// registry. Called by proxy.dispatcher.dispatchTo just before invoking
// the upstream's proxy handler. No-op if request_id is empty (no
// request_id middleware ran, e.g. unit tests with bare http.NewRequest).
func SetDispatchedUpstream(requestID, upstream string) {
	if requestID == "" || upstream == "" {
		return
	}
	dispatchedUpstreamRegistry.Store(requestID, upstream)
}

// TakeDispatchedUpstream returns the stamped upstream name (or empty
// string when none) AND removes the entry. The take-semantic ensures
// the registry never grows unboundedly. Called by audit.Middleware once
// per request after next.ServeHTTP returns.
func TakeDispatchedUpstream(requestID string) string {
	if requestID == "" {
		return ""
	}
	v, ok := dispatchedUpstreamRegistry.LoadAndDelete(requestID)
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
