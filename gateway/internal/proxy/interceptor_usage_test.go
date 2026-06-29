package proxy

// NOTE (Phase 16-01): this file is package `proxy` (internal), not
// `proxy_test`. Task 3's unit tests call the UNEXPORTED applyAudioEmbedUsage
// helper directly — the Postgres-free seam — which requires same-package
// access. The pre-existing SSE/JSON interceptor tests were migrated here too
// (proxy. qualifiers dropped) so all interceptor_usage tests live in one file
// per the plan's acceptance grep.

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/billing"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

// mkResp builds a response fixture with the supplied Content-Type, body,
// and ctx-bound request. Passing a ctx with httpx.ContextWithRequestID so
// the interceptor can correlate the RequestUsage slot.
func mkResp(ct, body string, ctx context.Context) *http.Response {
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://x/", nil)
	return &http.Response{
		Header:  http.Header{"Content-Type": []string{ct}},
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: req,
	}
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// TestUsageExtractorOpenAIShape: OpenAI-style "separate final chunk, empty
// choices[], usage populated".
//
// BL-02 semantics: Close() deletes the accountant slot to prevent the
// copy-on-write map leak. The test therefore inspects the usage slot
// AFTER draining the body but BEFORE Close.
func TestUsageExtractorOpenAIShape(t *testing.T) {
	acct := billing.NewAccountant()
	ix := NewUsageInterceptor(acct, nil, nil, nil, nil, 0, discardLog())

	ctx := httpx.ContextWithRequestID(context.Background(), "req-openai")
	body := `data: {"id":"a","choices":[],"usage":{"prompt_tokens":50,"completion_tokens":100,"total_tokens":150}}` + "\n\ndata: [DONE]\n\n"
	resp := mkResp("text/event-stream", body, ctx)
	if err := ix.Intercept(resp); err != nil {
		t.Fatalf("Intercept: %v", err)
	}
	var sink bytes.Buffer
	if _, err := io.Copy(&sink, resp.Body); err != nil {
		t.Fatalf("Copy: %v", err)
	}

	u := acct.Get("req-openai")
	if u == nil {
		t.Fatal("accountant returned nil RequestUsage")
	}
	if got := u.TokensIn.Load(); got != 50 {
		t.Errorf("TokensIn: want 50, got %d", got)
	}
	if got := u.TokensOut.Load(); got != 100 {
		t.Errorf("TokensOut: want 100, got %d", got)
	}

	// Close triggers FinalizeRequest + slot Delete (BL-02).
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if acct.Get("req-openai") != nil {
		t.Fatal("slot should be Deleted after Close (BL-02)")
	}
}

// TestUsageExtractorLlamaCppShape: llama.cpp-style "usage in same chunk as
// finish_reason=stop".
func TestUsageExtractorLlamaCppShape(t *testing.T) {
	acct := billing.NewAccountant()
	ix := NewUsageInterceptor(acct, nil, nil, nil, nil, 0, discardLog())

	ctx := httpx.ContextWithRequestID(context.Background(), "req-llama")
	body := `data: {"choices":[{"finish_reason":"stop","delta":{},"index":0}],"usage":{"prompt_tokens":42,"completion_tokens":99}}` + "\n\n"
	resp := mkResp("text/event-stream", body, ctx)
	if err := ix.Intercept(resp); err != nil {
		t.Fatal(err)
	}
	var sink bytes.Buffer
	_, _ = io.Copy(&sink, resp.Body)

	u := acct.Get("req-llama")
	if u == nil {
		t.Fatal("nil usage")
	}
	if got := u.TokensIn.Load(); got != 42 {
		t.Errorf("TokensIn: %d", got)
	}
	if got := u.TokensOut.Load(); got != 99 {
		t.Errorf("TokensOut: %d", got)
	}
	_ = resp.Body.Close()
}

// TestUsageExtractorIgnoresChunksWithoutUsage: chunks without a top-level
// "usage" key do not change counters.
func TestUsageExtractorIgnoresChunksWithoutUsage(t *testing.T) {
	acct := billing.NewAccountant()
	ix := NewUsageInterceptor(acct, nil, nil, nil, nil, 0, discardLog())
	ctx := httpx.ContextWithRequestID(context.Background(), "req-no-usage")
	body := `data: {"id":"x","choices":[{"delta":{"content":"hello"}}]}` + "\n\n"
	resp := mkResp("text/event-stream", body, ctx)
	_ = ix.Intercept(resp)
	var sink bytes.Buffer
	_, _ = io.Copy(&sink, resp.Body)
	u := acct.Get("req-no-usage")
	if u == nil {
		t.Fatal("nil usage (Set should still have created slot)")
	}
	if got := u.TokensIn.Load(); got != 0 {
		t.Errorf("TokensIn: want 0, got %d", got)
	}
	if got := u.TokensOut.Load(); got != 0 {
		t.Errorf("TokensOut: want 0, got %d", got)
	}
	_ = resp.Body.Close()
}

// TestUsageExtractorDoneChunkIgnored: Pitfall 5 guard — the SSE sentinel
// `data: [DONE]\n\n` must NOT panic and must NOT spuriously update counters.
func TestUsageExtractorDoneChunkIgnored(t *testing.T) {
	acct := billing.NewAccountant()
	ix := NewUsageInterceptor(acct, nil, nil, nil, nil, 0, discardLog())
	ctx := httpx.ContextWithRequestID(context.Background(), "req-done-only")
	body := "data: [DONE]\n\n"
	resp := mkResp("text/event-stream", body, ctx)
	if err := ix.Intercept(resp); err != nil {
		t.Fatalf("Intercept: %v", err)
	}
	var sink bytes.Buffer
	_, _ = io.Copy(&sink, resp.Body)
	u := acct.Get("req-done-only")
	if u == nil {
		t.Fatal("nil usage (Set should have created slot)")
	}
	if got := u.TokensIn.Load(); got != 0 {
		t.Errorf("TokensIn: want 0 (sentinel ignored), got %d", got)
	}
	if got := u.TokensOut.Load(); got != 0 {
		t.Errorf("TokensOut: want 0, got %d", got)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestUsageExtractorNonStreamingPassthrough: Content-Type application/json
// is a no-op — body is unchanged and no slot is created.
func TestUsageExtractorNonStreamingPassthrough(t *testing.T) {
	acct := billing.NewAccountant()
	ix := NewUsageInterceptor(acct, nil, nil, nil, nil, 0, discardLog())
	resp := mkResp("application/json", `{"choices":[]}`, context.Background())
	if err := ix.Intercept(resp); err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != `{"choices":[]}` {
		t.Errorf("body mutated: %q", b)
	}
}

// TestUsageExtractorExtractFromBody: non-streaming JSON response —
// caller invokes ExtractFromBody post-buffer.
func TestUsageExtractorExtractFromBody(t *testing.T) {
	acct := billing.NewAccountant()
	ix := NewUsageInterceptor(acct, nil, nil, nil, nil, 0, discardLog())
	body := []byte(`{"id":"x","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":13}}`)
	ix.ExtractFromBody("req-nonstream", body)
	u := acct.Get("req-nonstream")
	if u == nil {
		t.Fatal("ExtractFromBody should have created a slot")
	}
	if got := u.TokensIn.Load(); got != 7 {
		t.Errorf("TokensIn: want 7, got %d", got)
	}
	if got := u.TokensOut.Load(); got != 13 {
		t.Errorf("TokensOut: want 13, got %d", got)
	}
}

// TestUsageExtractorNoRequestContext: a response without request_id in
// context must not panic and must not register a slot.
func TestUsageExtractorNoRequestContext(t *testing.T) {
	acct := billing.NewAccountant()
	ix := NewUsageInterceptor(acct, nil, nil, nil, nil, 0, discardLog())
	resp := mkResp("text/event-stream", "data: [DONE]\n\n", context.Background())
	if err := ix.Intercept(resp); err != nil {
		t.Fatal(err)
	}
	// Body should remain the original io.NopCloser — Intercept bailed early.
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "data: [DONE]\n\n" {
		t.Errorf("body mutated unexpectedly: %q", b)
	}
}

// ── Phase 16-01 Task 3: applyAudioEmbedUsage direct-call unit tests ───────────
// These call the UNEXPORTED helper directly with a freshly constructed
// &billing.RequestUsage{} + body + ctx, then assert the Stored atomics. NO
// flusher, NO accountant slot, NO Postgres — the Postgres-free seam (the
// JSON-buffer Close path deletes the slot via FinalizeRequest, and the
// production flusher needs a *pgxpool.Pool, so the atomic is otherwise
// unobservable DB-free).

// TestApplyAudioEmbedUsageDurationPresent: an STT response carrying a
// duration field meters from that duration. 90s → AudioSecondsMs10 900, and
// the documented unit chain (×10 store → /10 → /60) yields DailyAudioMinutes 1
// for a 60s-limit tenant.
func TestApplyAudioEmbedUsageDurationPresent(t *testing.T) {
	usage := &billing.RequestUsage{}
	applyAudioEmbedUsage(usage, "stt", []byte(`{"text":"oi","duration":90.0}`), context.Background())
	if got := usage.AudioSecondsMs10.Load(); got != 900 {
		t.Fatalf("AudioSecondsMs10: want 900 (90s ×10), got %d", got)
	}
	// Unit chain trap-guard: ×10 store → /10.0 flush → /60 minutes.
	audioSeconds := float64(usage.AudioSecondsMs10.Load()) / 10.0
	if audioSeconds != 90.0 {
		t.Fatalf("flush audioSeconds: want 90.0, got %.2f", audioSeconds)
	}
	if mins := int(audioSeconds / 60); mins != 1 {
		t.Fatalf("DailyAudioMinutes (90s ÷60): want 1, got %d", mins)
	}
}

// TestApplyAudioEmbedUsageNoDurationDerivesFromRequest: a DEFAULT-FORMAT STT
// response ({"text":"..."} with NO duration) STILL meters non-zero, derived
// from the request audio seconds stamped on the ctx (REVISION 1 / success
// criterion #1 — the common-case client must not meter 0).
func TestApplyAudioEmbedUsageNoDurationDerivesFromRequest(t *testing.T) {
	ctx := auditctx.WithRequestAudioSeconds(context.Background(), 30.0)
	usage := &billing.RequestUsage{}
	applyAudioEmbedUsage(usage, "stt", []byte(`{"text":"oi"}`), ctx)
	if got := usage.AudioSecondsMs10.Load(); got != 300 {
		t.Fatalf("AudioSecondsMs10 (request-derived 30s ×10): want 300, got %d", got)
	}
}

// TestBillingRouteResolutionPrefersCtxOverRewrittenPath is the regression for
// the live-UAT bug (2026-06-29): gemini-stt rewrites r.URL.Path to the Gemini
// API path, so resp.Request.URL.Path is no longer /v1/audio/* at flush time.
// Resolving the billing route from that rewritten path misclassifies STT as
// "chat" → applyAudioEmbedUsage no-ops → audio_seconds never written → the
// all-zero enqueue guard drops the billing_events row entirely. The dispatcher
// now stamps the route from the ORIGINAL path into ctx; usageJSONBuffer.Close
// + FinalizeRequest prefer it. This test proves: (a) the rewritten path alone
// misclassifies (the bug), and (b) the ctx-stamped route + ELSE-derive meters
// correctly.
func TestBillingRouteResolutionPrefersCtxOverRewrittenPath(t *testing.T) {
	rewritten := "/v1beta/models/gemini-2.5-flash-lite:generateContent"

	// (a) Prove the bug: the rewritten outbound path classifies as "chat".
	if got := routeToBillingRoute(rewritten); got != "chat" {
		t.Fatalf("precondition: rewritten gemini path should misclassify as chat, got %q", got)
	}

	// (b) The dispatcher stamps the route from the inbound path pre-rewrite.
	ctx := auditctx.WithBillingRoute(context.Background(), "stt")
	ctx = auditctx.WithRequestAudioSeconds(ctx, 3.0)

	// Resolution mirrors usageJSONBuffer.Close: ctx route wins over reqPath.
	route := auditctx.BillingRouteFrom(ctx)
	if route == "" {
		route = routeToBillingRoute(rewritten)
	}
	if route != "stt" {
		t.Fatalf("ctx-stamped route must win over rewritten path: want stt, got %q", route)
	}

	// With the correct route, the ELSE-derive branch meters the request audio.
	usage := &billing.RequestUsage{}
	applyAudioEmbedUsage(usage, route, []byte(`{"text":"a a a a"}`), ctx)
	if got := usage.AudioSecondsMs10.Load(); got != 30 {
		t.Fatalf("AudioSecondsMs10 (3s ×10 via ctx-stamped stt route): want 30, got %d", got)
	}
}

// TestApplyAudioEmbedUsageNoDurationNoCtxZero: no response duration AND no
// ctx-stamped request seconds → 0, no panic (graceful degrade).
func TestApplyAudioEmbedUsageNoDurationNoCtxZero(t *testing.T) {
	usage := &billing.RequestUsage{}
	applyAudioEmbedUsage(usage, "stt", []byte(`{"text":"oi"}`), context.Background())
	if got := usage.AudioSecondsMs10.Load(); got != 0 {
		t.Fatalf("AudioSecondsMs10: want 0 (no duration, no ctx), got %d", got)
	}
}

// TestApplyAudioEmbedUsageEmbedBatch: an embed response meters EmbedsCount =
// len(data[]). 3 elements → 3 (DailyEmbeds trap-guard).
func TestApplyAudioEmbedUsageEmbedBatch(t *testing.T) {
	usage := &billing.RequestUsage{}
	applyAudioEmbedUsage(usage, "embed", []byte(`{"data":[{},{},{}],"usage":{}}`), context.Background())
	if got := usage.EmbedsCount.Load(); got != 3 {
		t.Fatalf("EmbedsCount (len data[]=3): want 3, got %d", got)
	}
	// No audio dimension written for embed.
	if got := usage.AudioSecondsMs10.Load(); got != 0 {
		t.Fatalf("embed route must not write AudioSecondsMs10, got %d", got)
	}
}

// TestApplyAudioEmbedUsageChatNoOp: the chat route is a no-op for the new
// helper — neither audio nor embed atomics are touched (the chat token path
// is produced elsewhere).
func TestApplyAudioEmbedUsageChatNoOp(t *testing.T) {
	usage := &billing.RequestUsage{}
	tokenBody := []byte(`{"usage":{"prompt_tokens":7,"completion_tokens":13}}`)
	applyAudioEmbedUsage(usage, "chat", tokenBody, context.Background())
	if got := usage.AudioSecondsMs10.Load(); got != 0 {
		t.Fatalf("chat route must not write AudioSecondsMs10, got %d", got)
	}
	if got := usage.EmbedsCount.Load(); got != 0 {
		t.Fatalf("chat route must not write EmbedsCount, got %d", got)
	}
}
