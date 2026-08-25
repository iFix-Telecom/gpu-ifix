package proxy

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
)

// llamaOverContextBody is the VERBATIM envelope measured in production
// (HANDOFF-tokenizer-fail-open-pod-ctx-16k.md L10): note `code` is a JSON
// NUMBER, not a string — a decoder that types it as string fails to parse the
// whole envelope and silently turns the classifier into a no-op.
const llamaOverContextBody = `{"error":{"code":400,"message":"request (18068 tokens) exceeds the available context size (16384 tokens), try increasing it","type":"exceed_context_size_error"}}`

// overContextRespOpts parameterizes the synthetic upstream response + request
// context the interceptor inspects.
type overContextRespOpts struct {
	status          int
	body            string
	dataClass       auth.DataClass
	streaming       bool
	stampStreaming  bool
	withDispatchRes bool
	noAuth          bool
}

func newOverContextResp(o overContextRespOpts) *http.Response {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	ctx := r.Context()
	if !o.noAuth {
		ctx = auth.WithContext(ctx, auth.AuthContext{
			TenantID:  "tenant-oc",
			APIKeyID:  "key-oc",
			DataClass: o.dataClass,
		})
	}
	if o.withDispatchRes {
		ctx = withDispatchResult(ctx, &dispatchResult{})
	}
	if o.stampStreaming {
		ctx = withStreamingFlag(ctx, o.streaming)
	}
	r = r.WithContext(ctx)
	return &http.Response{
		StatusCode: o.status,
		Body:       io.NopCloser(strings.NewReader(o.body)),
		Request:    r,
		Header:     make(http.Header),
	}
}

func newOverContextInterceptor() chatOverContextInterceptor {
	return chatOverContextInterceptor{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// eligibleOpts is the fully-eligible baseline: 400 + llama.cpp over-context
// envelope + non-streaming + normal tenant + dispatcher-driven.
func eligibleOpts() overContextRespOpts {
	return overContextRespOpts{
		status:          http.StatusBadRequest,
		body:            llamaOverContextBody,
		dataClass:       auth.DataClassNormal,
		streaming:       false,
		stampStreaming:  true,
		withDispatchRes: true,
	}
}

// TestOverContext_EligibleEmitsSentinel is the core contract: the sentinel
// wraps errUpstreamRetryable so the EXISTING sentinel-aware ErrorHandler
// suppresses the write + records fallthrough_, while the dispatcher can still
// distinguish the CAUSE (over-context vs dial failure) via errOverContextFallthrough.
func TestOverContext_EligibleEmitsSentinel(t *testing.T) {
	err := newOverContextInterceptor().Intercept(newOverContextResp(eligibleOpts()))
	if !errors.Is(err, errOverContextFallthrough) {
		t.Fatalf("err = %v, want errors.Is(err, errOverContextFallthrough)", err)
	}
	if !errors.Is(err, errUpstreamRetryable) {
		t.Fatalf("err = %v, want errors.Is(err, errUpstreamRetryable) (ErrorHandler suppression)", err)
	}
}

// TestOverContext_StreamingNeverCascades locks D-07: an SSE request may already
// have flushed bytes; post-response failover is forbidden.
func TestOverContext_StreamingNeverCascades(t *testing.T) {
	o := eligibleOpts()
	o.streaming = true
	if err := newOverContextInterceptor().Intercept(newOverContextResp(o)); err != nil {
		t.Fatalf("err = %v, want nil (D-07: streaming never failovers)", err)
	}
}

// TestOverContext_SensitiveAlsoCascades locks the POLICY decided by Pedro on
// 2026-08-24: data_class is NOT a gate on the over-context class. The clients
// (n8n) already fall back straight to OpenRouter when the gateway errors, so
// blocking here removed billing/audit/metrics without keeping the payload
// inside the perimeter. RES-08 still governs every OTHER fallthrough cause.
func TestOverContext_SensitiveAlsoCascades(t *testing.T) {
	o := eligibleOpts()
	o.dataClass = auth.DataClassSensitive
	err := newOverContextInterceptor().Intercept(newOverContextResp(o))
	if !errors.Is(err, errOverContextFallthrough) {
		t.Fatalf("err = %v, want errOverContextFallthrough (sensitive cascades too)", err)
	}
}

// TestOverContext_NoAuthContextIsNoop: without an AuthContext we cannot prove
// the tenant is non-sensitive → fail-safe passthrough.
func TestOverContext_NoAuthContextIsNoop(t *testing.T) {
	o := eligibleOpts()
	o.noAuth = true
	if err := newOverContextInterceptor().Intercept(newOverContextResp(o)); err != nil {
		t.Fatalf("err = %v, want nil (no auth ctx → fail-safe)", err)
	}
}

// TestOverContext_NoDispatchResultIsNoop: the dispatchOverride / standalone
// path has nobody to re-route the request; emitting the sentinel there would
// make the ErrorHandler write a 502 — strictly worse than the honest 400.
func TestOverContext_NoDispatchResultIsNoop(t *testing.T) {
	o := eligibleOpts()
	o.withDispatchRes = false
	if err := newOverContextInterceptor().Intercept(newOverContextResp(o)); err != nil {
		t.Fatalf("err = %v, want nil (no dispatchResult → nobody re-routes)", err)
	}
}

// TestOverContext_UnstampedStreamingFlagIsNoop: the streaming flag is only
// stamped by dispatchTo. Absence means the request did not come through the
// dispatcher → inelegible (second lock alongside the dispatchResult gate).
func TestOverContext_UnstampedStreamingFlagIsNoop(t *testing.T) {
	o := eligibleOpts()
	o.stampStreaming = false
	if err := newOverContextInterceptor().Intercept(newOverContextResp(o)); err != nil {
		t.Fatalf("err = %v, want nil (unstamped streaming flag → inelegible)", err)
	}
}

// TestOverContext_NonOverContextEnvelopesAreNoop: only the over-context class
// re-routes. A generic 400 is a real client error and a 200 is a success.
func TestOverContext_NonOverContextEnvelopesAreNoop(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"generic 400", http.StatusBadRequest, `{"error":{"code":"invalid_api_param","message":"bad tools schema","type":"invalid_request_error"}}`},
		{"200 ok", http.StatusOK, `{"choices":[{"message":{"content":"hi"}}]}`},
		{"unparseable body", http.StatusBadRequest, `not json at all`},
		{"500", http.StatusInternalServerError, llamaOverContextBody},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := eligibleOpts()
			o.status = tc.status
			o.body = tc.body
			if err := newOverContextInterceptor().Intercept(newOverContextResp(o)); err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
		})
	}
}

// TestOverContext_OpenAIStyleEnvelopeMatches: the OpenAI-shaped envelope uses a
// STRING code and a different message; both must classify as over-context so a
// tier-0 that is not llama.cpp still cascades.
func TestOverContext_OpenAIStyleEnvelopeMatches(t *testing.T) {
	o := eligibleOpts()
	o.body = `{"error":{"code":"context_length_exceeded","message":"This model's maximum context length is 8192 tokens","type":"invalid_request_error"}}`
	if err := newOverContextInterceptor().Intercept(newOverContextResp(o)); !errors.Is(err, errOverContextFallthrough) {
		t.Fatalf("err = %v, want errOverContextFallthrough", err)
	}
}

// TestOverContext_413AlsoClassifies: some upstreams answer 413 for the same
// condition.
func TestOverContext_413AlsoClassifies(t *testing.T) {
	o := eligibleOpts()
	o.status = http.StatusRequestEntityTooLarge
	if err := newOverContextInterceptor().Intercept(newOverContextResp(o)); !errors.Is(err, errOverContextFallthrough) {
		t.Fatalf("err = %v, want errOverContextFallthrough on 413", err)
	}
}

// TestOverContext_BodyRestoredByteIdentical: the interceptor peeks the body to
// classify it. On EVERY path (match or not) the body must remain fully readable
// so a non-cascading response still reaches the client verbatim.
func TestOverContext_BodyRestoredByteIdentical(t *testing.T) {
	cases := []struct {
		name string
		opts overContextRespOpts
	}{
		{"match", eligibleOpts()},
		{"no match", func() overContextRespOpts {
			o := eligibleOpts()
			o.body = `{"error":{"code":"invalid_api_param","message":"nope","type":"invalid_request_error"}}`
			return o
		}()},
		{"no-auth short-circuit", func() overContextRespOpts {
			o := eligibleOpts()
			o.noAuth = true
			return o
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := newOverContextResp(tc.opts)
			_ = newOverContextInterceptor().Intercept(resp)
			got, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read restored body: %v", err)
			}
			if string(got) != tc.opts.body {
				t.Fatalf("restored body = %q, want %q", string(got), tc.opts.body)
			}
		})
	}
}

// TestOverContext_LargeBodyRestoredByteIdentical proves the io.MultiReader
// restore also covers bodies LARGER than the 8 KiB peek window.
func TestOverContext_LargeBodyRestoredByteIdentical(t *testing.T) {
	big := `{"error":{"type":"exceed_context_size_error","code":400,"message":"` +
		strings.Repeat("x", 16<<10) + `"}}`
	o := eligibleOpts()
	o.body = big
	resp := newOverContextResp(o)
	// Truncated peek → JSON parse fails → fail-safe passthrough (no sentinel).
	if err := newOverContextInterceptor().Intercept(resp); err != nil {
		t.Fatalf("err = %v, want nil (truncated peek → fail-safe passthrough)", err)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read restored body: %v", err)
	}
	if string(got) != big {
		t.Fatalf("restored body length = %d, want %d", len(got), len(big))
	}
}

// TestOverContext_NilSafe: defensive — a nil response or a response without a
// Request (no ctx to read) must never panic.
func TestOverContext_NilSafe(t *testing.T) {
	ic := newOverContextInterceptor()
	if err := ic.Intercept(nil); err != nil {
		t.Fatalf("nil resp: err = %v, want nil", err)
	}
	if err := ic.Intercept(&http.Response{StatusCode: 400}); err != nil {
		t.Fatalf("no request: err = %v, want nil", err)
	}
}
