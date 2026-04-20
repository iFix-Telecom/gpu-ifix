// Package proxy (dispatcher.go): role-based fallback chain dispatcher.
// One handler per role (llm / stt / embed) — at request time:
//
//  1. enforce token cap (chat=16k, embed=8k); 400 on over-cap
//  2. detect stream:true (chat-specific)
//  3. resolve tier-0 upstream via upstreams.Loader
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

		// 1. Token-count enforcement (RES-07) — non-STT only.
		if cfg.TokenCounter != nil && cfg.ContextCap > 0 {
			body, err := readAndRestoreBody(r)
			if err == nil && len(body) > 0 {
				if _, terr := cfg.TokenCounter.Enforce(r.Context(), body, cfg.Role, cfg.ContextCap); terr != nil {
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
			ok, _ := SensitiveRetry(r.Context(), cfg.Breaker, t0.Name)
			if !ok {
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
	// Mutate the request reference so the audit middleware (which reads
	// r.Context after our handler returns) sees the override. We can't
	// pass a derived ctx upstream after WriteHeader, so we update the
	// request struct in place.
	*r = *r.WithContext(auditctx.WithUpstreamOverride(r.Context(), UpstreamBlockedSensitiveValue))
	w.Header().Set("Retry-After", "30")
	httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
		"service_unavailable", "upstream_unavailable_for_sensitive_tenant",
		"Primary inference upstream is unavailable; sensitive-data tenants cannot be routed to external providers.")
	obs.SensitiveRetryTotal.WithLabelValues("blocked_response").Inc()
}
