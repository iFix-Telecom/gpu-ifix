package proxy

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

// ErrUpstreamUnreachable is the sentinel reported in the OpenAI envelope
// when httputil.ReverseProxy's internal dial/roundtrip fails.
var ErrUpstreamUnreachable = errors.New("proxy: upstream unreachable")

// ErrSensitiveRetryExhausted is returned after 3× exp-backoff attempts
// (CONTEXT.md D-B1) when the primary breaker never returned to CLOSED.
// Maps to HTTP 503 with envelope code "upstream_unavailable_for_sensitive_tenant"
// + Retry-After: 30 header per D-B2.
var ErrSensitiveRetryExhausted = errors.New("proxy: sensitive retry exhausted")

// ErrToolCallPartialStream is raised when the SSE ModifyResponse tee
// detects "tool_calls" in the first 8KB then the upstream disconnects
// mid-stream (CONTEXT.md RES-06, SC-4). Gateway MUST NOT failover;
// emits a terminal SSE error event with code "tool_call_partial_stream"
// (HTTP 502 envelope for non-streaming tool calls).
var ErrToolCallPartialStream = errors.New("proxy: tool call partial stream")

// ErrContextLengthExceeded is raised by tokencount.go pre-dispatch when
// input_tokens > 16384 (chat) or > 8192 (embed BGE-M3 native cap).
// Maps to HTTP 400 with envelope code "context_length_exceeded" per RES-07.
var ErrContextLengthExceeded = errors.New("proxy: context length exceeded")

// errDialFailedFallthrough is the typed sentinel that fallthroughRoundTripper
// (transport.go) substitutes for a pre-byte connection-class dial error
// (connection-refused, no route, DNS, dial-phase timeout) so the dispatcher
// can re-route into the tier-1 cascade instead of writing a 502 (RES-13).
//
// It is NEVER written to the client. The sentinel-aware ErrorHandler below
// detects it, suppresses ALL writes, and records the fallthrough signal in a
// request-scoped dispatchResult so dispatchTo can re-dispatch after
// ReverseProxy.ServeHTTP returns. Every OTHER error keeps the normal 502
// write path. The sentinel is lowercase/unexported because no caller outside
// this package needs it.
var errDialFailedFallthrough = errors.New("proxy: dial failed, fall through to tier-1")

// ErrorHandler returns a ReverseProxy ErrorHandler that emits a 502
// with the OpenAI error envelope and logs the cause + request id.
func ErrorHandler(upstreamName string, log *slog.Logger) func(http.ResponseWriter, *http.Request, error) {
	log = log.With("module", "PROXY", "upstream", upstreamName)
	return func(w http.ResponseWriter, r *http.Request, err error) {
		log.ErrorContext(r.Context(), "upstream error",
			"err", err,
			"request_id", httpx.RequestIDFrom(r.Context()),
			"path", r.URL.Path,
			"sentinel", ErrUpstreamUnreachable.Error(),
		)
		httpx.WriteOpenAIError(w, http.StatusBadGateway,
			"api_error", "upstream_unreachable",
			"The upstream inference service is temporarily unreachable.")
	}
}
