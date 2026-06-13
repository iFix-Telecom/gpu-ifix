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
	"bytes"
	"context"
	"errors"
	"io"
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

// maxSTTBodyBuffer caps how many bytes of a request body the dispatcher will
// buffer in memory to make it replayable across the tier-1 cascade (RES-13 /
// Plan 12-03). A body larger than this is EXEMPT from buffering and from
// fallthrough — on a tier-0 dial failure it takes the normal 502/503 path,
// so an over-cap multipart STT upload can never exhaust memory (T-12-10).
//
// maxSTTBodyBuffer: matches the global request body ceiling enforced at
// cmd/gateway/main.go:1189 via http.MaxBytesHandler(r, cfg.MaxBodyBytes),
// where cfg.MaxBodyBytes is the fixed 25 MiB STT/audio limit set at
// internal/config/config.go:350 (`MaxBodyBytes: 25 * (1 << 20)`). Chat/embed
// bodies are additionally token-capped (16k/8k) so they always sit far below
// this; the cap is effectively the STT upload ceiling.
const maxSTTBodyBuffer = 25 * (1 << 20) // 25 MiB — mirrors config.MaxBodyBytes

// dispatchResult is the request-scoped control-flow channel between
// dispatchTo / the sentinel-aware ErrorHandler and the dispatch decision
// (RES-13 / Plan 12-03 — Pitfall 2). It is installed into the request context
// by dispatchTo BEFORE proxy.ServeHTTP and read AFTER:
//
//   - fallthrough_ == true && wrote == false  → a pre-byte connection-class
//     dial failure occurred and NOTHING was written; the caller may
//     re-dispatch into the tier-1 cascade.
//   - wrote == true → a response was committed (a normal 502 for a
//     non-sentinel error, or a successful upstream response); the dispatch is
//     terminal and the caller MUST NOT re-dispatch (D-07: never re-dispatch
//     after any byte was written).
//
// The struct is carried via context.WithValue (Codex suggestion) rather than
// an implicit side effect so the control flow is explicit and testable.
type dispatchResult struct {
	fallthrough_ bool  // pre-byte dial failure observed (sentinel suppressed)
	wrote        bool  // a response was committed to the ResponseWriter
	err          error // the underlying error (sentinel or upstream)
}

// dispatchResultCtxKey is the unexported context key for *dispatchResult.
type dispatchResultCtxKey struct{}

// withDispatchResult installs a *dispatchResult into ctx so the ErrorHandler
// running inside ReverseProxy.ServeHTTP can record the fallthrough signal.
func withDispatchResult(ctx context.Context, res *dispatchResult) context.Context {
	return context.WithValue(ctx, dispatchResultCtxKey{}, res)
}

// dispatchResultFrom reads the *dispatchResult installed by dispatchTo. Returns
// nil when none is present (e.g. dispatchOverride path, which does not
// participate in dial fallthrough).
func dispatchResultFrom(ctx context.Context) *dispatchResult {
	res, _ := ctx.Value(dispatchResultCtxKey{}).(*dispatchResult)
	return res
}

// EmergTrafficRegistrar abstracts the emerg.Reconciler for the dispatcher.
// Plan 06-08 (D-E3) integration: when Resolve returns an UpstreamConfig
// with IsEmergency=true (set by Loader.Resolve when a tier-0 emergency
// override is active), dispatcher calls RegisterTraffic so the reconciler
// can track lastEmergencyTrafficAt for the idle-grace destroy timer
// (PROVISION_IDLE_GRACE_SECONDS, default 300s).
//
// Implemented by *emerg.Reconciler — but kept as an interface here to
// (a) avoid an import cycle (proxy → emerg → ... → proxy via ws
//
//	signaling), and
//
// (b) make dispatcher tests trivially mockable without importing the
//
//	full reconciler stack.
//
// W9 revision (2026-05-13) — interface injection chosen over runtime
// `strings.HasPrefix` sniffing on Name. The fragile string match would
// silently break if a future plan renamed "emergency_pod_*" to anything
// else; the IsEmergency flag is set deliberately by Loader.Resolve at
// the same atomic instant the override URL is read, so dispatcher and
// loader can never disagree about whether traffic is emergency-bound.
type EmergTrafficRegistrar interface {
	RegisterTraffic()
}

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

	// EmergTraffic is the Plan 06-08 (D-E3) emergency traffic registrar.
	// nil → no-op (Phase 6 disabled OR role != "llm"). When non-nil AND
	// the resolved upstream carries IsEmergency=true, the dispatcher
	// calls EmergTraffic.RegisterTraffic() before forwarding so the
	// reconciler's idle-grace timer (D-D1, 5min default) sees recent
	// activity and does NOT destroy the pod prematurely.
	EmergTraffic EmergTrafficRegistrar

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
				if cfg.Breaker.EffectiveState(override) == gobreaker.StateOpen {
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
		// Phase 06.9 Plan 04: EffectiveState combines operator force-override
		// (via gatewayctl breaker force-open) with the observation-driven
		// gobreaker state. Force-open SHORT-CIRCUITS routing to tier-1 without
		// requiring the probe loop to first accumulate ConsecutiveFailures.
		t0State := cfg.Breaker.EffectiveState(t0.Name)

		sensitive := ac.DataClass == auth.DataClassSensitive

		// Plan 06-08 D-E3: when the resolved tier-0 upstream is the
		// emergency pod (Loader.Resolve set IsEmergency=true because
		// OverrideTier0 is active), register traffic so the reconciler's
		// idle-grace timer sees recent activity. We register BEFORE the
		// breaker-state check so a HALF-OPEN probe to the emergency pod
		// also counts as "user-facing traffic flowed in this window".
		if t0.IsEmergency && cfg.EmergTraffic != nil {
			cfg.EmergTraffic.RegisterTraffic()
		}

		// 4. Routing decision tree.
		if t0State == gobreaker.StateClosed {
			// RES-13 / Plan 12-03: prepare the body for replay across the
			// cascade BEFORE the first dispatch. If the body is over-cap or
			// non-replayable, replayable=false → we still dispatch tier-0 but
			// a dial failure takes the normal 502 path (no fallthrough).
			restoreBody, replayable := prepareReplayBody(r)
			res := cfg.dispatchTo(w, r, t0.Name, streaming, log)
			if !res.fallthrough_ || res.wrote {
				// Terminal: successful response OR a committed 502/non-dial
				// error (D-07 — never re-dispatch after a byte was written).
				return
			}
			// Pre-byte connection-class dial failure on tier-0.
			// D-09: record a failure on the tier-0 breaker so it opens
			// naturally after N dials.
			cfg.recordDialFailure(t0.Name)

			// D-10 / RES-08 (HARD GATE): sensitive tenants NEVER fall through
			// to an external tier-1 — emit the sensitive 503 block.
			if sensitive {
				obs.DialFallthroughTotal.WithLabelValues(cfg.Role, "sensitive_blocked").Inc()
				cfg.writeSensitiveBlock(w, r)
				return
			}

			// Over-cap / non-replayable body: cannot safely resend → normal
			// 502 path (the ErrorHandler suppressed the write, so emit it).
			if !replayable {
				httpx.WriteOpenAIError(w, http.StatusBadGateway,
					"api_error", "upstream_unreachable",
					"The upstream inference service is temporarily unreachable.")
				return
			}

			// Normal tenant: re-dispatch into the tier-1 cascade, replaying
			// the body before each attempt (D-08). The cascade outcome drives
			// the DialFallthroughTotal label: tier1_served when a candidate
			// served, chain_exhausted when every candidate failed.
			served := cfg.cascadeTier1(w, r, streaming, restoreBody, log)
			if served {
				obs.DialFallthroughTotal.WithLabelValues(cfg.Role, "tier1_served").Inc()
			} else {
				obs.DialFallthroughTotal.WithLabelValues(cfg.Role, "chain_exhausted").Inc()
			}
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
			// Sensitive non-stream retry succeeded → tier-0 CLOSED again.
			// If the post-retry dispatch ALSO hits a pre-byte dial failure,
			// the ErrorHandler suppressed the write; a sensitive tenant MUST
			// still 503-block, never fall through (D-10 HARD GATE).
			res := cfg.dispatchTo(w, r, t0.Name, streaming, log)
			if res.fallthrough_ && !res.wrote {
				cfg.recordDialFailure(t0.Name)
				obs.DialFallthroughTotal.WithLabelValues(cfg.Role, "sensitive_blocked").Inc()
				cfg.writeSensitiveBlock(w, r)
			}
			return
		}

		// Normal tenant, tier-0 OPEN/HALF: iterate tier-1 candidates in
		// tier_priority ASC order (Phase 11.2 D-B5′ + Plan 12-03 dial-aware).
		// Prepare the body for replay so a tier-1 dial failure can advance to
		// the next candidate with identical bytes.
		restoreBody, _ := prepareReplayBody(r)
		cfg.cascadeTier1(w, r, streaming, restoreBody, log)
	})
}

// cascadeTier1 dispatches to the first CLOSED tier-1 candidate (tier_priority
// ASC). On a pre-byte connection-class dial failure (D-08) it records the
// candidate's breaker failure and advances to the next CLOSED candidate,
// replaying the body identically each hop (restoreBody). When the chain is
// exhausted it writes the existing 503 exhaustion envelope exactly once.
//
// Returns true when a candidate committed a response without dial-failing
// (served), false on exhaustion. The caller owns DialFallthroughTotal
// accounting so the metric is only emitted on the tier-0 dial-fallthrough
// path (NOT the plain tier-0-OPEN cascade, which is not a dial fallthrough).
//
// EffectiveState honors operator force-override (gw:breaker:force:{name}) —
// force-opening a candidate makes the loop skip it (Phase 06.9 Plan 04).
func (cfg DispatcherConfig) cascadeTier1(w http.ResponseWriter, r *http.Request, streaming bool, restoreBody func(), log *slog.Logger) bool {
	candidates := cfg.Loader.ResolveAllTier1(cfg.Role)
	if len(candidates) == 0 {
		httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
			"service_unavailable", "upstream_unavailable",
			"Primary upstream unavailable and no fallback configured for role.")
		return false
	}
	for _, t1 := range candidates {
		if cfg.Breaker.EffectiveState(t1.Name) != gobreaker.StateClosed {
			continue
		}
		if restoreBody != nil {
			restoreBody() // resend identical bytes on every hop (D-08)
		}
		res := cfg.dispatchTo(w, r, t1.Name, streaming, log)
		if res.fallthrough_ && !res.wrote {
			// This tier-1 candidate dial-failed pre-byte: record its breaker
			// failure and try the next CLOSED candidate.
			cfg.recordDialFailure(t1.Name)
			continue
		}
		// Terminal: a response was committed (success or a non-dial 502).
		return true
	}
	// Chain exhausted: every CLOSED candidate dial-failed (or none CLOSED).
	httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
		"service_unavailable", "upstream_unavailable",
		"All inference upstreams are unavailable.")
	return false
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
	// viable upstream — fail fast with the D-C2 envelope. Phase 06.9 Plan 04:
	// EffectiveState honors operator force-open as well as observed state.
	if cfg.Breaker.EffectiveState(name) == gobreaker.StateOpen {
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
	// Phase 11.2 Plan 08 (D-B13 audit-distinguish fix): mirror the
	// dispatchTo registry stamp so peak-off-hours dispatch also records
	// the factual upstream into audit_log.upstream.
	auditctx.SetDispatchedUpstream(httpx.RequestIDFrom(r.Context()), name)
	proxy.ServeHTTP(w, r)
}

// dispatchTo invokes the named upstream's proxy handler. Logs the
// dispatch decision so operators can correlate request_id with the
// chosen upstream.
//
// RES-13 / Plan 12-03: dispatchTo installs a fresh *dispatchResult into the
// request context BEFORE proxy.ServeHTTP. The sentinel-aware ErrorHandler
// (errors.go) records fallthrough_/wrote on it. dispatchTo returns the result
// so the dispatch decision can re-route into the tier-1 cascade on a pre-byte
// dial failure (fallthrough_ && !wrote). A "proxy not registered" miss returns
// wrote=true (terminal) — that 503 envelope is final.
//
// replayBody, when non-nil, is restored as a fresh r.Body before ServeHTTP so
// the same bytes can be resent on the next cascade hop (the caller restores it
// again before each subsequent attempt).
func (cfg DispatcherConfig) dispatchTo(w http.ResponseWriter, r *http.Request, name string, streaming bool, log *slog.Logger) dispatchResult {
	res := &dispatchResult{}
	proxy, ok := cfg.Proxies[name]
	if !ok {
		httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
			"service_unavailable", "upstream_unavailable",
			"Upstream proxy not registered.")
		res.wrote = true
		return *res
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
	// RES-13 / Plan 12-03: install the request-scoped dispatchResult so the
	// sentinel-aware ErrorHandler can carry the fallthrough signal back.
	r = r.WithContext(withDispatchResult(r.Context(), res))
	// Phase 11.2 Plan 08 (D-B13 audit-distinguish fix): also stamp the
	// FACTUAL upstream into the request-id-keyed registry so the audit
	// middleware (which sits OUTSIDE http.TimeoutHandler and therefore
	// captured the pre-clone *Request) can record the cascade
	// fall-through target name into audit_log.upstream. The registry
	// pattern bypasses ctx propagation entirely — TimeoutHandler clones
	// the request before passing to the inner handler, breaking any
	// `*r = *r.WithContext(...)` mutation done here. See
	// auditctx.SetDispatchedUpstream godoc for full rationale.
	auditctx.SetDispatchedUpstream(httpx.RequestIDFrom(r.Context()), name)
	// RES-02 deferral: ReverseProxy.ServeHTTP writes directly to the
	// ResponseWriter — backoff retry would require a buffering layer
	// (see retry.go godoc). Phase 3 relies on breaker fallback instead.
	proxy.ServeHTTP(w, r)
	// If the ErrorHandler did not run (successful proxy) the response was
	// committed by ReverseProxy — record wrote=true so callers treat it as
	// terminal.
	if !res.fallthrough_ {
		res.wrote = true
	}
	return *res
}

// recordDialFailure records a single failure on the named tier-0 breaker so
// it opens naturally after N dials (D-09). breaker.Set exposes no direct
// "record failure" API; we drive one failure through Execute with a non-4xx
// error that IsSuccessful classifies as a failure. The fn never makes a real
// network call — it returns the synthetic error immediately.
func (cfg DispatcherConfig) recordDialFailure(name string) {
	_, _ = cfg.Breaker.Execute(name, func() (*http.Response, error) {
		return nil, &breaker.HTTPError{Status: http.StatusBadGateway, Msg: "tier-0 dial failed (fallthrough)"}
	})
}

// prepareReplayBody makes r's body replayable across cascade hops. Returns a
// restore closure that resets r.Body to a fresh reader, and replayable=true
// when the body can be safely resent. When the body is over the cap, missing,
// or non-replayable, replayable=false and the caller MUST skip fallthrough
// (normal 502/503 path) — guaranteeing no unbounded buffering (T-12-10).
func prepareReplayBody(r *http.Request) (func(), bool) {
	// No body → nothing to replay; a bodyless request is trivially replayable.
	if r.Body == nil || r.Body == http.NoBody {
		return func() {}, true
	}
	// Over-cap by declared Content-Length → exempt from buffering + fallthrough.
	if r.ContentLength > maxSTTBodyBuffer {
		return func() {}, false
	}
	// Common case: the HTTP server / middleware already set GetBody. Use it.
	if r.GetBody != nil {
		restore := func() {
			if b, err := r.GetBody(); err == nil {
				r.Body = b
			}
		}
		restore() // prime before the first dispatch
		return restore, true
	}
	// GetBody == nil but Body != nil: buffer ONCE (bounded) and set GetBody.
	buf, err := io.ReadAll(io.LimitReader(r.Body, maxSTTBodyBuffer+1))
	_ = r.Body.Close()
	if err != nil {
		return func() {}, false
	}
	if int64(len(buf)) > maxSTTBodyBuffer {
		// Streamed body exceeded the cap mid-read → non-replayable.
		r.Body = io.NopCloser(bytes.NewReader(buf))
		return func() {}, false
	}
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(buf)), nil
	}
	restore := func() {
		b, _ := r.GetBody()
		r.Body = b
	}
	restore() // prime before the first dispatch
	return restore, true
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
