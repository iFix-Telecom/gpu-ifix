// chat_retryable_status_test.go — Phase 23-04 Task 1: unit tests for
// ChatRetryableStatusInterceptor (cascata por status HTTP em chat tier-1).
package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// newChatStatusResp builds an upstream response whose Request context carries
// (or not) a dispatchResult — the "someone will re-route" gate.
func newChatStatusResp(t *testing.T, status int, body string, withResult bool) *http.Response {
	t.Helper()
	ctx := context.Background()
	if withResult {
		ctx = withDispatchResult(ctx, &dispatchResult{})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://upstream/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestChatRetryableStatus_429Cascades(t *testing.T) {
	body := `{"error":{"message":"rate limited"}}`
	resp := newChatStatusResp(t, 429, body, true)
	err := ChatRetryableStatusInterceptor().Intercept(resp)
	if !errors.Is(err, errUpstreamRetryable) {
		t.Fatalf("429: want errUpstreamRetryable, got %v", err)
	}
	var se *chatUpstreamStatusError
	if !errors.As(err, &se) {
		t.Fatalf("429: want *chatUpstreamStatusError, got %T", err)
	}
	if se.Status() != 429 {
		t.Fatalf("Status() = %d, want 429", se.Status())
	}
	if string(se.Body()) != body {
		t.Fatalf("Body() = %q, want %q", se.Body(), body)
	}
	if se.ContentType() != "application/json" {
		t.Fatalf("ContentType() = %q", se.ContentType())
	}
	// Body restored byte-identical for any downstream consumer.
	got, _ := io.ReadAll(resp.Body)
	if string(got) != body {
		t.Fatalf("restored body = %q, want %q", got, body)
	}
}

func TestChatRetryableStatus_AllRetryableStatuses(t *testing.T) {
	for _, s := range []int{402, 429, 500, 502, 503, 504, 524, 529} {
		if err := ChatRetryableStatusInterceptor().Intercept(newChatStatusResp(t, s, "{}", true)); !errors.Is(err, errUpstreamRetryable) {
			t.Errorf("status %d: want retryable, got %v", s, err)
		}
	}
}

func TestChatRetryableStatus_NonRetryablePassThrough(t *testing.T) {
	for _, s := range []int{200, 201, 400, 401, 403, 404, 413, 422} {
		if err := ChatRetryableStatusInterceptor().Intercept(newChatStatusResp(t, s, "{}", true)); err != nil {
			t.Errorf("status %d: want nil (no cascade), got %v", s, err)
		}
	}
}

func TestChatRetryableStatus_NoDispatchResultNoCascade(t *testing.T) {
	if err := ChatRetryableStatusInterceptor().Intercept(newChatStatusResp(t, 503, "{}", false)); err != nil {
		t.Fatalf("no dispatchResult: want nil, got %v", err)
	}
	if err := ChatRetryableStatusInterceptor().Intercept(nil); err != nil {
		t.Fatalf("nil resp: want nil, got %v", err)
	}
	if err := ChatRetryableStatusInterceptor().Intercept(&http.Response{StatusCode: 503}); err != nil {
		t.Fatalf("nil Request: want nil, got %v", err)
	}
}

// stream:true does NOT disable the cascade: a non-2xx status arrives before
// any SSE byte (Padrão 3 do research).
func TestChatRetryableStatus_StreamingStillCascades(t *testing.T) {
	resp := newChatStatusResp(t, 503, `{"error":{"message":"x"}}`, true)
	resp.Request = resp.Request.WithContext(withStreamingFlag(resp.Request.Context(), true))
	if err := ChatRetryableStatusInterceptor().Intercept(resp); !errors.Is(err, errUpstreamRetryable) {
		t.Fatalf("stream 503: want errUpstreamRetryable, got %v", err)
	}
}

// The peek is bounded (8 KiB) and the remainder is still delivered.
func TestChatRetryableStatus_BoundedPeek(t *testing.T) {
	big := bytes.Repeat([]byte("a"), chatErrorPeekMax+100)
	resp := newChatStatusResp(t, 502, string(big), true)
	err := ChatRetryableStatusInterceptor().Intercept(resp)
	var se *chatUpstreamStatusError
	if !errors.As(err, &se) {
		t.Fatalf("want *chatUpstreamStatusError, got %v", err)
	}
	if len(se.Body()) != chatErrorPeekMax {
		t.Fatalf("Body len = %d, want %d", len(se.Body()), chatErrorPeekMax)
	}
	got, _ := io.ReadAll(resp.Body)
	if len(got) != len(big) {
		t.Fatalf("restored len = %d, want %d", len(got), len(big))
	}
}

// countingInterceptor registra quantas vezes rodou (prova de ordem/curto-circuito).
type countingInterceptor struct{ n int }

func (c *countingInterceptor) Intercept(*http.Response) error { c.n++; return nil }

// Phase 23-14 — ExternalChatInterceptors é o helper ÚNICO de composição usado
// pelo main (②, ③, openrouter-tts) e pelo teste de integração.
func TestExternalChatInterceptors_NonFinal429Cascades(t *testing.T) {
	rest := &countingInterceptor{}
	err := ExternalChatInterceptors(false, rest).Intercept(newChatStatusResp(t, 429, `{"error":{}}`, true))
	if !errors.Is(err, errUpstreamRetryable) {
		t.Fatalf("não-final 429: want errUpstreamRetryable, got %v", err)
	}
	if rest.n != 0 {
		t.Fatalf("interceptors seguintes rodaram %d vez(es) após o erro retryable (deviam ser pulados)", rest.n)
	}
}

func TestExternalChatInterceptors_Final429PassesThrough(t *testing.T) {
	rest := &countingInterceptor{}
	resp := newChatStatusResp(t, 429, `{"error":{"message":"x"}}`, true)
	if err := ExternalChatInterceptors(true, rest).Intercept(resp); err != nil {
		t.Fatalf("final 429: want nil (status segue ao cliente), got %v", err)
	}
	if resp.StatusCode != 429 {
		t.Fatalf("status alterado: %d", resp.StatusCode)
	}
	if rest.n != 1 {
		t.Fatalf("interceptors do final rodaram %d vez(es), want 1", rest.n)
	}
}

func TestExternalChatInterceptors_Non200OKRunsRest(t *testing.T) {
	rest := &countingInterceptor{}
	if err := ExternalChatInterceptors(false, rest, nil).Intercept(newChatStatusResp(t, 200, `{}`, true)); err != nil {
		t.Fatalf("200: want nil, got %v", err)
	}
	if rest.n != 1 {
		t.Fatalf("rest rodou %d vez(es), want 1", rest.n)
	}
}
