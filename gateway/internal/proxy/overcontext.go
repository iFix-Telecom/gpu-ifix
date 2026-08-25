// Package proxy (overcontext.go) — quick 260824-ucv Fix A.
//
// A tier-0 upstream answering 400 `exceed_context_size_error` is NOT telling us
// the client sent a bad request: the very same body succeeds on a tier-1 with a
// larger window (measured — HANDOFF-tokenizer-fail-open-pod-ctx-16k.md: the
// 18k-token Maestro turns were served by openrouter-chat until the pod came up
// at 07:43 and took over tier-0). It is a CAPACITY signal about the upstream we
// picked, therefore a ROUTING signal — so we suppress the response pre-byte and
// let the dispatcher cascade to tier-1.
//
// Wiring is deliberately restricted to the TIER-0 chat proxies (NewChatProxy +
// the dynamic override "emergency_pod_llm"). openrouter-chat (tier-1) does NOT
// get this interceptor: there is nowhere left to cascade, and a 400 from the
// last hop is legitimate information the client must see.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// errOverContextFallthrough is the over-context routing sentinel. It WRAPS the
// pre-existing errUpstreamRetryable on purpose: the sentinel-aware ErrorHandler
// (errors.go) already matches errUpstreamRetryable via errors.Is, so write
// suppression + the fallthrough_ signal come for free and no condition there
// needs to change — while the dispatcher can still distinguish the CAUSE
// (errors.Is(res.err, errOverContextFallthrough)) to decide NOT to record a
// breaker failure (T-ucv-05).
var errOverContextFallthrough = fmt.Errorf("proxy: tier-0 over-context, route to tier-1: %w", errUpstreamRetryable)

// overContextStreamingKey carries the dispatch-time streaming decision into the
// interceptor. ModifyResponse cannot re-read the (already consumed) request
// body, and D-07 forbids failover once bytes may have been flushed — so the
// flag is stamped by dispatchTo, which already knows.
type overContextStreamingKey struct{}

// withStreamingFlag stamps the dispatch-time streaming decision onto ctx.
func withStreamingFlag(ctx context.Context, streaming bool) context.Context {
	return context.WithValue(ctx, overContextStreamingKey{}, streaming)
}

// streamingFlagFrom reads the flag stamped by dispatchTo. stamped=false means
// the request did NOT come through dispatchTo (dispatchOverride / standalone
// proxy) — the interceptor treats that as ineligible, a second lock alongside
// the dispatchResult gate.
func streamingFlagFrom(ctx context.Context) (streaming bool, stamped bool) {
	v, ok := ctx.Value(overContextStreamingKey{}).(bool)
	return v, ok
}

// overContextPeekBytes bounds how much of the upstream error body we read to
// classify it (T-ucv-03). Error envelopes are a few hundred bytes; anything
// larger is not one, and a truncated peek simply fails to parse → passthrough.
const overContextPeekBytes = 8 << 10 // 8 KiB

// restoredBody re-joins the peeked prefix with the untouched remainder so the
// response stays byte-identical for every non-cascading path. Close delegates
// to the original body per the ProxyResponseInterceptor contract (interceptor.go:
// interceptors MUST NOT close the body themselves, and a wrapper's Close MUST
// delegate).
type restoredBody struct {
	io.Reader
	orig io.ReadCloser
}

func (b restoredBody) Close() error { return b.orig.Close() }

// chatOverContextInterceptor turns a tier-0 over-context 400/413 into the
// errOverContextFallthrough routing sentinel. It runs inside ModifyResponse —
// after the upstream response is available but BEFORE any byte reaches the
// client — so emitting an error here is pre-byte-safe for non-streaming chat.
type chatOverContextInterceptor struct{ log *slog.Logger }

// Intercept applies the eligibility gates cheapest-first, only touching the
// body once everything else has already qualified.
func (ic chatOverContextInterceptor) Intercept(resp *http.Response) error {
	// (a) Defensive: no response, or no request → no ctx to reason about.
	if resp == nil || resp.Request == nil {
		return nil
	}
	// (b) Only the over-context status class. Everything else is untouched.
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusRequestEntityTooLarge {
		return nil
	}
	ctx := resp.Request.Context()
	// (c) Nobody would re-route: the dispatchOverride path and standalone
	// proxies install no dispatchResult, and for them the ErrorHandler would
	// turn our sentinel into a 502 — strictly worse than the honest 400.
	if dispatchResultFrom(ctx) == nil {
		return nil
	}
	// (d) D-07: an SSE response may already have flushed bytes; never failover
	// after the fact. An unstamped flag means the request did not come through
	// dispatchTo at all → also ineligible.
	streaming, stamped := streamingFlagFrom(ctx)
	if !stamped || streaming {
		return nil
	}
	// (e) An AuthContext is required — it carries the tenant label that makes
	// the cascade auditable. Without it we cannot attribute the external spend
	// (nor the payload), so we decline: fail-safe passthrough.
	//
	// POLICY (decisão Pedro, 2026-08-24): data_class is deliberately NOT a gate
	// on the over-context class. Sensitive tenants cascade too. Rationale: the
	// clients (n8n) already fall back straight to OpenRouter when the gateway
	// errors, so blocking here did not keep the payload inside the perimeter —
	// it only removed billing, audit and metrics from the path. Cascading
	// THROUGH the gateway is strictly better observability for the same data
	// exposure. RES-08 remains fully in force for every OTHER cause of
	// fallthrough (breaker OPEN, dial failure, shed) — see the dispatcher's
	// writeSensitiveBlock hard gate, which is bypassed for over-context ONLY.
	ac, ok := auth.FromContext(ctx)
	if !ok {
		return nil
	}
	if resp.Body == nil {
		return nil
	}
	// (f) Bounded peek + unconditional restore (T-ucv-03). The restore happens
	// on EVERY subsequent path, including the non-matching ones, so a response
	// we decline to re-route still reaches the client byte-identical.
	peek, readErr := io.ReadAll(io.LimitReader(resp.Body, overContextPeekBytes))
	orig := resp.Body
	resp.Body = restoredBody{Reader: io.MultiReader(bytes.NewReader(peek), orig), orig: orig}
	if readErr != nil {
		return nil
	}
	// (g) Classify. Parse failure → false → passthrough (fail-safe).
	if !isOverContextBody(peek) {
		return nil
	}
	// (h) Match: this request is going to cost money at the external tier-1,
	// so it is counted and logged before the sentinel leaves.
	upstream := auditctx.BillingUpstreamFrom(ctx)
	obs.OverContextCascadedTotal.WithLabelValues(ac.TenantID, upstream).Inc()
	if ic.log != nil {
		// data_class is logged because for a sensitive tenant this line is the
		// audit trail of a payload leaving the perimeter (policy above).
		ic.log.WarnContext(ctx, "tier-0 over-context; routing to tier-1",
			"module", "OVERCONTEXT",
			"upstream", upstream,
			"status", resp.StatusCode,
			"tenant", ac.TenantID,
			"data_class", string(ac.DataClass),
			"request_id", httpx.RequestIDFrom(ctx),
		)
	}
	return errOverContextFallthrough
}

// overContextEnvelope is a TOLERANT decoder for the OpenAI-shaped error
// envelope. `code` is json.RawMessage because llama.cpp emits it as a NUMBER
// (400) while OpenAI emits a STRING ("context_length_exceeded"); typing it as
// string would fail the whole Unmarshal and silently turn this classifier into
// a no-op — exactly the failure mode this fix exists to remove.
type overContextEnvelope struct {
	Error struct {
		Type    string          `json:"type"`
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
	} `json:"error"`
}

// isOverContextBody reports whether an upstream error body describes a context
// window overflow. Recognizes the llama.cpp type, the OpenAI code, and the two
// message shapes. Any parse failure returns false — passthrough is the
// fail-safe answer (it preserves today's behaviour).
func isOverContextBody(b []byte) bool {
	var env overContextEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return false
	}
	if env.Error.Type == "exceed_context_size_error" {
		return true
	}
	if len(env.Error.Code) > 0 {
		// Only string codes are meaningful here; a numeric code (llama.cpp's
		// 400) unquotes with an error and is ignored.
		var code string
		if json.Unmarshal(env.Error.Code, &code) == nil && code == "context_length_exceeded" {
			return true
		}
	}
	msg := strings.ToLower(env.Error.Message)
	return strings.Contains(msg, "exceeds the available context size") ||
		strings.Contains(msg, "maximum context length")
}
