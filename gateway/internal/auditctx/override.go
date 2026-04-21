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

import "context"

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
