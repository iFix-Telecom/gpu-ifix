// Package proxy (dispatcher.go): role-based fallback chain dispatcher.
// One handler per role (llm / stt / embed) — at request time:
//
//  1. enforce token cap (chat=16k, embed=8k); 400 on over-cap
//
//  2. detect stream:true (chat-specific)
//
//  3. resolve tier-0 upstream via upstreams.Loader
//
//  4. consult breaker.Set state to choose dispatch path:
//
//     | tier-0 state | data_class | streaming | action                                         |
//     | -----------  | ---------- | --------- | ---------------------------------------------- |
//     | CLOSED       | any        | any       | dispatch tier-0                                |
//     | OPEN/H-OPEN  | normal     | any       | dispatch tier-1 (if CLOSED) else 503           |
//     | OPEN/H-OPEN  | sensitive  | streaming | 503 immediate (D-B4 fail-fast)                 |
//     | OPEN/H-OPEN  | sensitive  | non-stream| SensitiveRetry → tier-0 if CLOSED else 503     |
//
// Sensitive blocks write audit_log.upstream="blocked_sensitive" via
// audit.WithUpstreamOverride (D-B3) so dashboards can isolate them.
//
// RES-02 NOTE: this Phase-3 dispatcher relies on the breaker for
// resilience; backoff-retry-within-same-upstream (DoWithBackoff) is
// shipped in retry.go but not wired into ReverseProxy.ServeHTTP — see
// retry.go godoc for the Phase 5 deferral rationale.
package proxy

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/sony/gobreaker/v2"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// UpstreamBlockedSensitiveValue mirrors audit.UpstreamBlockedSensitive
// so dispatcher.go can stamp the audit override without importing the
// audit package (audit imports proxy via the SSE interceptor — see
// auditctx package godoc for the cycle-break rationale). The mirror is
// intentional: changing the wire value in audit/middleware.go MUST be
// done in lockstep here.
const UpstreamBlockedSensitiveValue = "blocked_sensitive"

// DispatcherConfig groups the collaborators required to route a single
// role's traffic. One DispatcherConfig per role (llm / stt / embed).
type DispatcherConfig struct {
	Role         string
	Loader       *upstreams.Loader
	Breaker      *breaker.Set
	TokenCounter *TokenCounter // nil to skip token-cap enforcement (STT)
	ContextCap   int           // ChatContextCap or EmbedContextCap; 0 to skip

	// Proxies maps upstream name → http.Handler (typically a fully
	// configured *httputil.ReverseProxy with the appropriate Director).
	// MUST contain entries for every upstream the loader can resolve to
	// for this role; missing keys yield 503.
	Proxies map[string]http.Handler

	Log *slog.Logger
}

// NewDispatcher builds the http.Handler that applies breaker + fallback
// rules for a single role. Mount at "POST /v1/chat/completions" etc.
func NewDispatcher(cfg DispatcherConfig) http.Handler {
	log := cfg.Log.With("module", "DISPATCHER", "role", cfg.Role)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ac, ok := auth.FromContext(r.Context())
		if !ok {
			httpx.WriteOpenAIError(w, http.StatusUnauthorized,
				"authentication_error", "no_api_key",
				"Authenticated tenant required.")
			return
		}

		// Phase 4 — schedule middleware override (D-C2). When a tenant is
		// in peak mode AND outside its business window, the schedule
		// middleware (gateway/internal/schedule/middleware.go) wrote an
		// upstream_override onto the request context. This block forces
		// the corresponding upstream, bypassing tier-0 entirely (the GPU
		// may be suspended off-hours — its breaker state is irrelevant).
		//
		// NOT a fallback chain: if the override target is OPEN we return
		// 503 off_hours_upstream_unavailable immediately. Per D-C2 the
		// off-hours block on external = fail-fast; no local retry.
		//
		// Phase 5 — three NEW reserved override values arrive here when
		// the shed middleware (gateway/internal/shed/middleware.go) has
		// already written its own 503 response and stamped the audit row:
		//   - UpstreamShedBlockedSensitiveValue (D-B3 sensitive 503)
		//   - UpstreamShedTier1UnavailableValue (D-D1 no-tier-1 503)
		// In both cases the wire body is already on its way to the
		// client and the dispatcher MUST short-circuit without writing
		// again. The remaining shed value (UpstreamShedSaturatedValue
		// stamped on the *shed decision*; the override carries the
		// tier-1 upstream NAME) flows through dispatchOverride with a
		// special-cased envelope when that tier-1 breaker is OPEN
		// (D-D1: emit all_chat_upstreams_saturated + Retry-After: 30
		// instead of off_hours_upstream_unavailable).
		if override := auditctx.UpstreamOverrideFromContext(r.Context()); override != "" &&
			override != UpstreamBlockedSensitiveValue {
			// Phase 5 short-circuit: shed middleware already responded.
			if override == auditctx.UpstreamShedBlockedSensitiveValue ||
				override == auditctx.UpstreamShedTier1UnavailableValue {
				return
			}
			// Phase 5 — shed_saturated path: override is the tier-1
			// upstream name. If its breaker is OPEN we emit the D-D1
			// envelope (all_chat_upstreams_saturated + Retry-After: 30)
			// instead of the schedule-derived off-hours envelope.
			if auditctx.ShedDecisionFromContext(r.Context()) == auditctx.UpstreamShedSaturatedValue {
				if cb, found := cfg.Breaker.Get(override); found && cb != nil && cb.State() == gobreaker.StateOpen {
					// Re-stamp ctx for audit (D-D4) so the audit row
					// reads upstream=shed_tier1_unavailable rather
					// than the openrouter-chat upstream name that
					// shed middleware wrote on its way down.
					*r = *r.WithContext(auditctx.WithUpstreamOverride(r.Context(),
						auditctx.UpstreamShedTier1UnavailableValue))
					obs.GatewayShedDecisions.WithLabelValues(override, "tier1_unavailable").Inc()
					w.Header().Set("Retry-After", "30")
					httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
						"service_unavailable", "all_chat_upstreams_saturated",
						"Primary saturated and secondary unavailable.")
					return
				}
			}
			cfg.dispatchOverride(w, r, override, log)
			return
		}

		// 1. Token-count enforcement (RES-07) — non-STT only.
		if cfg.TokenCounter != nil && cfg.ContextCap > 0 {
			body, err := readAndRestoreBody(r)
			if err == nil && len(body) > 0 {
				// Extract the model name from the body so the cache key is
				// specific to the requested model's tokenizer (Pitfall 6 /
				// HIGH-04: passing cfg.Role here caused cross-tokenizer cache
				// collisions — e.g. a "gpt-4o" request would reuse a cached
				// count produced by the Qwen tokenizer). Fall back to cfg.Role
				// only when the body carries no "model" field.
				modelName := extractModelName(body)
				if modelName == "" {
					modelName = cfg.Role
				}
				if _, terr := cfg.TokenCounter.Enforce(r.Context(), body, modelName, cfg.ContextCap); terr != nil {
					httpx.WriteOpenAIError(w, http.StatusBadRequest,
						"invalid_request_error", "context_length_exceeded",
						"Request exceeds context cap.")
					return
				}
			}
		}

		// 2. Detect streaming (chat-specific; embed/audio always non-stream).
		streaming := IsStreamingRequest(r)

		// 3. Resolve tier-0.
		t0, ok := cfg.Loader.Resolve(cfg.Role, 0)
		if !ok {
			httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
				"service_unavailable", "upstream_unavailable",
				"No primary upstream configured for role.")
			return
		}
		t0State := gobreaker.StateClosed
		if cb0, found := cfg.Breaker.Get(t0.Name); found && cb0 != nil {
			t0State = cb0.State()
		}

		sensitive := ac.DataClass == auth.DataClassSensitive

		// 4. Routing decision tree.
		if t0State == gobreaker.StateClosed {
			cfg.dispatchTo(w, r, t0.Name, streaming, log)
			return
		}

		// tier-0 OPEN or HALF_OPEN.
		if sensitive {
			if streaming {
				// D-B4 — fail-fast pre-flight, no retry loop.
				cfg.writeSensitiveBlock(w, r)
				return
			}
			// D-B1 — bounded retry loop (~4s); blocks if breaker stays OPEN.
			ok, retryErr := SensitiveRetry(r.Context(), cfg.Breaker, t0.Name)
			if !ok {
				if errors.Is(retryErr, context.Canceled) {
					// Client disconnected during the retry wait; nothing to
					// write — the ResponseWriter is already gone. Avoids
					// inflating the blocked_response metric for canceled
					// requests (MED-05 fix).
					return
				}
				cfg.writeSensitiveBlock(w, r)
				return
			}
			cfg.dispatchTo(w, r, t0.Name, streaming, log)
			return
		}

		// Normal tenant: try tier-1 (D-C4 — no fallback of fallback for chat;
		// stt/embed roles are independent).
		t1, ok := cfg.Loader.Resolve(cfg.Role, 1)
		if !ok {
			httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
				"service_unavailable", "upstream_unavailable",
				"Primary upstream unavailable and no fallback configured for role.")
			return
		}
		if cb1, found := cfg.Breaker.Get(t1.Name); found && cb1 != nil && cb1.State() != gobreaker.StateClosed {
			httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
				"service_unavailable", "upstream_unavailable",
				"All inference upstreams are unavailable.")
			return
		}
		cfg.dispatchTo(w, r, t1.Name, streaming, log)
	})
}

// dispatchOverride routes to a specific upstream when the schedule
// middleware has set ctx.upstream_override (Phase 4 D-C2). Unlike
// dispatchTo, this path:
//
//   - does NOT consult the tier-0 breaker (GPU may be suspended
//     off-hours; its state is irrelevant);
//   - DOES consult the override target's breaker — if OPEN, emits 503
//     off_hours_upstream_unavailable (no fallback of fallback per D-C2);
//   - skips token-count + streaming detection done by the normal path
//     — the override target (OpenRouter) enforces its own context cap
//     and the streaming flag is carried in the body regardless.
func (cfg DispatcherConfig) dispatchOverride(w http.ResponseWriter, r *http.Request, name string, log *slog.Logger) {
	// Check the override target's breaker. If CLOSED or HALF_OPEN we
	// dispatch; if OPEN the tenant is peak-mode off-hours and has no
	// viable upstream — fail fast with the D-C2 envelope.
	if cb, found := cfg.Breaker.Get(name); found && cb != nil && cb.State() == gobreaker.StateOpen {
		log.Warn("off-hours external unavailable; fail-fast",
			"upstream", name,
			"request_id", httpx.RequestIDFrom(r.Context()),
		)
		httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
			"service_unavailable", "off_hours_upstream_unavailable",
			"Tenant in peak mode and off-hours external upstream unavailable.")
		return
	}
	proxy, ok := cfg.Proxies[name]
	if !ok {
		log.Warn("override target has no registered proxy; fail-fast",
			"upstream", name,
			"request_id", httpx.RequestIDFrom(r.Context()),
		)
		httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
			"service_unavailable", "off_hours_upstream_unavailable",
			"Off-hours external upstream not configured.")
		return
	}
	log.Debug("dispatching via upstream_override",
		"upstream", name,
		"request_id", httpx.RequestIDFrom(r.Context()),
	)
	// Phase 4 (BL-01 fix): same billing ctx plumbing as dispatchTo so the
	// schedule-override path (peak off-hours) also attributes cost to the
	// resolved external upstream.
	r = r.WithContext(auditctx.WithBillingUpstream(r.Context(), name))
	proxy.ServeHTTP(w, r)
}

// dispatchTo invokes the named upstream's proxy handler. Logs the
// dispatch decision so operators can correlate request_id with the
// chosen upstream.
func (cfg DispatcherConfig) dispatchTo(w http.ResponseWriter, r *http.Request, name string, streaming bool, log *slog.Logger) {
	proxy, ok := cfg.Proxies[name]
	if !ok {
		httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
			"service_unavailable", "upstream_unavailable",
			"Upstream proxy not registered.")
		return
	}
	log.Debug("dispatching",
		"upstream", name,
		"streaming", streaming,
		"request_id", httpx.RequestIDFrom(r.Context()),
	)
	// Phase 4 (BL-01 fix): stash the resolved upstream name on the ctx so
	// the billing UsageInterceptor can attribute cost + external flag at
	// Flush time (the interceptor runs inside ModifyResponse where the
	// dispatch decision is otherwise opaque).
	r = r.WithContext(auditctx.WithBillingUpstream(r.Context(), name))
	// RES-02 deferral: ReverseProxy.ServeHTTP writes directly to the
	// ResponseWriter — backoff retry would require a buffering layer
	// (see retry.go godoc). Phase 3 relies on breaker fallback instead.
	proxy.ServeHTTP(w, r)
}

// writeSensitiveBlock writes the standardized 503 envelope for sensitive
// tenants whose primary breaker is OPEN AND retry budget is exhausted
// (or streaming pre-flight fail-fast). Stamps the audit context with
// upstream="blocked_sensitive" so the audit middleware records D-B3.
func (cfg DispatcherConfig) writeSensitiveBlock(w http.ResponseWriter, r *http.Request) {
	// Mutate the request reference in-place so the audit middleware sees the
	// override via r.Context() after ServeHTTP returns. This is safe ONLY
	// because audit.Middleware reads r.Context() sequentially AFTER
	// next.ServeHTTP(aw, r) returns — there is no concurrent goroutine
	// reading *r at this point. CONTRACT: if audit.Middleware ever moves
	// the r.Context() read into a separate goroutine, this in-place
	// mutation MUST be replaced with a sync.Map keyed by request_id or a
	// dedicated response header that the audit middleware reads instead.
	// See HIGH-02 in 03-REVIEW.md for the full fragility analysis.
	*r = *r.WithContext(auditctx.WithUpstreamOverride(r.Context(), UpstreamBlockedSensitiveValue))
	w.Header().Set("Retry-After", "30")
	httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
		"service_unavailable", "upstream_unavailable_for_sensitive_tenant",
		"Primary inference upstream is unavailable; sensitive-data tenants cannot be routed to external providers.")
	obs.SensitiveRetryTotal.WithLabelValues("blocked_response").Inc()
}
