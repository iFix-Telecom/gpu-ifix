package proxy_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/billing"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy"
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
func TestUsageExtractorOpenAIShape(t *testing.T) {
	acct := billing.NewAccountant()
	ix := proxy.NewUsageInterceptor(acct, discardLog())

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
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("Close: %v", err)
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
}

// TestUsageExtractorLlamaCppShape: llama.cpp-style "usage in same chunk as
// finish_reason=stop".
func TestUsageExtractorLlamaCppShape(t *testing.T) {
	acct := billing.NewAccountant()
	ix := proxy.NewUsageInterceptor(acct, discardLog())

	ctx := httpx.ContextWithRequestID(context.Background(), "req-llama")
	body := `data: {"choices":[{"finish_reason":"stop","delta":{},"index":0}],"usage":{"prompt_tokens":42,"completion_tokens":99}}` + "\n\n"
	resp := mkResp("text/event-stream", body, ctx)
	if err := ix.Intercept(resp); err != nil {
		t.Fatal(err)
	}
	var sink bytes.Buffer
	_, _ = io.Copy(&sink, resp.Body)
	_ = resp.Body.Close()

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
}

// TestUsageExtractorIgnoresChunksWithoutUsage: chunks without a top-level
// "usage" key do not change counters.
func TestUsageExtractorIgnoresChunksWithoutUsage(t *testing.T) {
	acct := billing.NewAccountant()
	ix := proxy.NewUsageInterceptor(acct, discardLog())
	ctx := httpx.ContextWithRequestID(context.Background(), "req-no-usage")
	body := `data: {"id":"x","choices":[{"delta":{"content":"hello"}}]}` + "\n\n"
	resp := mkResp("text/event-stream", body, ctx)
	_ = ix.Intercept(resp)
	var sink bytes.Buffer
	_, _ = io.Copy(&sink, resp.Body)
	_ = resp.Body.Close()
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
}

// TestUsageExtractorDoneChunkIgnored: Pitfall 5 guard — the SSE sentinel
// `data: [DONE]\n\n` must NOT panic and must NOT spuriously update counters.
func TestUsageExtractorDoneChunkIgnored(t *testing.T) {
	acct := billing.NewAccountant()
	ix := proxy.NewUsageInterceptor(acct, discardLog())
	ctx := httpx.ContextWithRequestID(context.Background(), "req-done-only")
	body := "data: [DONE]\n\n"
	resp := mkResp("text/event-stream", body, ctx)
	if err := ix.Intercept(resp); err != nil {
		t.Fatalf("Intercept: %v", err)
	}
	var sink bytes.Buffer
	_, _ = io.Copy(&sink, resp.Body)
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
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
}

// TestUsageExtractorNonStreamingPassthrough: Content-Type application/json
// is a no-op — body is unchanged and no slot is created.
func TestUsageExtractorNonStreamingPassthrough(t *testing.T) {
	acct := billing.NewAccountant()
	ix := proxy.NewUsageInterceptor(acct, discardLog())
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
	ix := proxy.NewUsageInterceptor(acct, discardLog())
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
	ix := proxy.NewUsageInterceptor(acct, discardLog())
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
