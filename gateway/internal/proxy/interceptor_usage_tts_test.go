package proxy

// quick-261007-t9f — TTS metering through the usage interceptor. The speech
// response is binary (audio/*), so the JSON/SSE paths never saw it and TTS had
// zero billing_events. tokens_in on route tts = input characters (D-P1),
// priced under model tts-1 (D-P2).

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/billing"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

func ttsResp(status int, ct string, chars int64, upstream string) (*http.Response, string) {
	reqID := uuid.NewString()
	ctx := httpx.ContextWithRequestID(context.Background(), reqID)
	ctx = auditctx.WithBillingRoute(ctx, "tts")
	ctx = auditctx.WithBillingUpstream(ctx, upstream)
	if chars > 0 {
		ctx = auditctx.WithRequestTTSChars(ctx, chars)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://x/v1/audio/speech", nil)
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{ct}},
		Body:       io.NopCloser(strings.NewReader(strings.Repeat("\x00", 4096))),
		Request:    req,
	}, reqID
}

func ttsInterceptor() (*UsageInterceptor, *captureEnqueuer) {
	return newCostInterceptor(map[billing.PriceKey]billing.Price{
		key(ttsBillingModel, "openrouter-fireworks", "input_token"): price(1.5e-5),
	})
}

func TestTTSInterceptFinalEvent(t *testing.T) {
	ix, capt := ttsInterceptor()
	resp, _ := ttsResp(200, "audio/mpeg", 9, "kokoro-tts")
	if err := ix.Intercept(resp); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if len(capt.events) != 1 {
		t.Fatalf("want 1 event, got %d", len(capt.events))
	}
	ev := capt.events[0]
	if ev.Route != "tts" || ev.Model != "tts-1" || ev.TokensIn != 9 || ev.TokensOut != 0 || ev.Source != "final" {
		t.Errorf("event mismatch: route=%s model=%s in=%d out=%d source=%s",
			ev.Route, ev.Model, ev.TokensIn, ev.TokensOut, ev.Source)
	}
	if ev.CostExternalBRL != 0 {
		t.Errorf("external: want 0 (kokoro-tts self-hosted), got %v", ev.CostExternalBRL)
	}
	if want := 9 * 1.5e-5 * testFX; !approx(ev.CostLocalPhantomBRL, want) {
		t.Errorf("phantom: want %v, got %v", want, ev.CostLocalPhantomBRL)
	}

	// Close is idempotent: no second event.
	_ = resp.Body.Close()
	if len(capt.events) != 1 {
		t.Fatalf("second Close produced another event: %d", len(capt.events))
	}
}

func TestTTSInterceptPartialOnEarlyClose(t *testing.T) {
	ix, capt := ttsInterceptor()
	resp, _ := ttsResp(200, "audio/wav", 9, "emergency_pod_tts")
	if err := ix.Intercept(resp); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if len(capt.events) != 1 {
		t.Fatalf("want 1 event, got %d", len(capt.events))
	}
	if ev := capt.events[0]; ev.Source != "partial" || ev.TokensIn != 9 {
		t.Errorf("want partial with tokens_in 9, got source=%s in=%d", ev.Source, ev.TokensIn)
	}
}

func TestTTSInterceptNoEventOnErrorOrZeroChars(t *testing.T) {
	cases := []struct {
		name   string
		status int
		ct     string
		chars  int64
	}{
		{"503 json error", 503, "application/json", 9},
		{"400 json error", 400, "application/json", 9},
		{"200 zero chars", 200, "audio/mpeg", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ix, capt := ttsInterceptor()
			resp, reqID := ttsResp(tc.status, tc.ct, tc.chars, "kokoro-tts")
			if err := ix.Intercept(resp); err != nil {
				t.Fatal(err)
			}
			if ix.accountant.Get(reqID) != nil {
				t.Fatal("no accountant slot expected")
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if len(capt.events) != 0 {
				t.Fatalf("want 0 events, got %d", len(capt.events))
			}
		})
	}
}
