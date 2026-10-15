package proxy

// Phase 23 plano 10 — billing fiel: custo reportado pelo OpenRouter
// (usage.cost), custo de lista cache-aware com degrau >100k para o passo ①
// (anthropic-haiku), colunas de cache, phantom de chat a preço Haiku e
// providers dos upstreams novos.

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/billing"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

// usageResp monta uma resposta com body e content-type dados, com request_id,
// upstream e route estampados no ctx — o mesmo caminho que o dispatcher usa.
func usageResp(ct, body, upstream, route string) *http.Response {
	reqID := uuid.NewString()
	ctx := httpx.ContextWithRequestID(context.Background(), reqID)
	ctx = auditctx.WithBillingRoute(ctx, route)
	ctx = auditctx.WithBillingUpstream(ctx, upstream)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://x/v1/chat/completions", nil)
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{ct}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

// drive executa Intercept + leitura total + Close e devolve o único evento.
func drive(t *testing.T, ix *UsageInterceptor, capt *captureEnqueuer, resp *http.Response) billing.Event {
	t.Helper()
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
	return capt.events[0]
}

const orChatJSON = `{"model":"deepseek/deepseek-v4.1-flash","choices":[],"usage":{"prompt_tokens":12050,"completion_tokens":431,"total_tokens":12481,"prompt_tokens_details":{"cached_tokens":11800,"cache_write_tokens":0},"cost":0.00123}}`

func TestUsageReportedCostJSON(t *testing.T) {
	ix, capt := newCostInterceptor(map[billing.PriceKey]billing.Price{
		key("deepseek/deepseek-v4.1-flash", "openrouter-fireworks", "input_token"): price(1),
	})
	ev := drive(t, ix, capt, usageResp("application/json", orChatJSON, "openrouter-chat", "chat"))
	if !approx(ev.CostExternalBRL, 0.00615) {
		t.Errorf("external: want 0.00615, got %v", ev.CostExternalBRL)
	}
	if ev.CostSource != "provider_reported" {
		t.Errorf("cost_source: want provider_reported, got %q", ev.CostSource)
	}
	if ev.TokensCacheRead != 11800 || ev.TokensCacheWrite != 0 || ev.TokensIn != 12050 || ev.TokensOut != 431 {
		t.Errorf("tokens: in=%d out=%d cr=%d cw=%d", ev.TokensIn, ev.TokensOut, ev.TokensCacheRead, ev.TokensCacheWrite)
	}
	if ev.CostLocalPhantomBRL != 0 {
		t.Errorf("phantom: want 0, got %v", ev.CostLocalPhantomBRL)
	}
}

func TestUsageReportedCostSSE(t *testing.T) {
	ix, capt := newCostInterceptor(nil)
	sse := `data: {"model":"deepseek/deepseek-v4.1-flash","choices":[{"delta":{"content":"oi"}}]}` + "\n\n" +
		`data: ` + orChatJSON + "\n\n" +
		"data: [DONE]\n\n"
	ev := drive(t, ix, capt, usageResp("text/event-stream", sse, "openrouter-chat", "chat"))
	if !approx(ev.CostExternalBRL, 0.00615) || ev.CostSource != "provider_reported" {
		t.Errorf("want 0.00615/provider_reported, got %v/%q", ev.CostExternalBRL, ev.CostSource)
	}
	if ev.TokensCacheRead != 11800 || ev.TokensIn != 12050 || ev.Source != "final" {
		t.Errorf("tokens/source: in=%d cr=%d source=%s", ev.TokensIn, ev.TokensCacheRead, ev.Source)
	}
}

func TestUsageSSEWithoutCostFallsBackToPriceTable(t *testing.T) {
	ix, capt := newCostInterceptor(map[billing.PriceKey]billing.Price{
		key("ds", "openrouter-fireworks", "input_token"):  price(1e-6),
		key("ds", "openrouter-fireworks", "output_token"): price(2e-6),
	})
	sse := `data: {"model":"ds","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}` + "\n\n"
	ev := drive(t, ix, capt, usageResp("text/event-stream", sse, "openrouter-chat", "chat"))
	if ev.CostSource != "price_table" {
		t.Errorf("cost_source: want price_table, got %q", ev.CostSource)
	}
	if want := (100*1e-6 + 50*2e-6) * testFX; !approx(ev.CostExternalBRL, want) {
		t.Errorf("external: want %v, got %v", want, ev.CostExternalBRL)
	}
}

func TestUsageOpenRouterDeepseekNoCostUsesFireworksProvider(t *testing.T) {
	ix, capt := newCostInterceptor(map[billing.PriceKey]billing.Price{
		key("ds", "openrouter-fireworks", "input_token"):  price(1e-6),
		key("ds", "openrouter-fireworks", "output_token"): price(2e-6),
	})
	body := `{"model":"ds","usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}`
	ev := drive(t, ix, capt, usageResp("application/json", body, "openrouter-deepseek", "chat"))
	if ev.CostSource != "price_table" {
		t.Errorf("cost_source: want price_table, got %q", ev.CostSource)
	}
	if want := (100*1e-6 + 50*2e-6) * testFX; !approx(ev.CostExternalBRL, want) {
		t.Errorf("external: want %v, got %v", want, ev.CostExternalBRL)
	}
}

// Linhas de preço do passo ① (research Padrão 4).
func haikuPrices(withCache bool) map[billing.PriceKey]billing.Price {
	rows := map[billing.PriceKey]billing.Price{
		key("claude-haiku-5-5", "anthropic", "input_token"):       price(1e-7),
		key("claude-haiku-5-5", "anthropic", "output_token"):      price(5e-7),
		key("claude-haiku-5-5:long", "anthropic", "input_token"):  price(5e-7),
		key("claude-haiku-5-5:long", "anthropic", "output_token"): price(2.5e-6),
	}
	if withCache {
		rows[key("claude-haiku-5-5", "anthropic", "cache_read_token")] = price(1e-8)
		rows[key("claude-haiku-5-5", "anthropic", "cache_write_token")] = price(1.25e-7)
		rows[key("claude-haiku-5-5:long", "anthropic", "cache_read_token")] = price(5e-8)
		rows[key("claude-haiku-5-5:long", "anthropic", "cache_write_token")] = price(6.25e-7)
	}
	return rows
}

func haikuBody(prompt, cached, write, completion int) string {
	return `{"model":"claude-haiku-5-5","usage":{"prompt_tokens":` + itoa(prompt) +
		`,"completion_tokens":` + itoa(completion) +
		`,"total_tokens":` + itoa(prompt+completion) +
		`,"prompt_tokens_details":{"cached_tokens":` + itoa(cached) +
		`,"cache_write_tokens":` + itoa(write) + `}}}`
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestUsageAnthropicHaikuCacheAware(t *testing.T) {
	ix, capt := newCostInterceptor(haikuPrices(true))
	ev := drive(t, ix, capt, usageResp("application/json", haikuBody(10000, 8000, 1000, 500), "anthropic-haiku", "chat"))
	wantUSD := 1000*1e-7 + 8000*1e-8 + 1000*1.25e-7 + 500*5e-7
	if !approx(wantUSD, 0.000555) {
		t.Fatalf("aritmética do teste: %v", wantUSD)
	}
	if !approx(ev.CostExternalBRL, wantUSD*testFX) {
		t.Errorf("external: want %v, got %v", wantUSD*testFX, ev.CostExternalBRL)
	}
	if ev.CostSource != "price_table" {
		t.Errorf("cost_source: want price_table, got %q", ev.CostSource)
	}
	if ev.TokensCacheRead != 8000 || ev.TokensCacheWrite != 1000 {
		t.Errorf("cache: cr=%d cw=%d", ev.TokensCacheRead, ev.TokensCacheWrite)
	}
}

func TestUsageAnthropicHaikuLongTier(t *testing.T) {
	ix, capt := newCostInterceptor(haikuPrices(true))
	ev := drive(t, ix, capt, usageResp("application/json", haikuBody(120000, 100000, 0, 1000), "anthropic-haiku", "chat"))
	wantUSD := 20000*5e-7 + 100000*5e-8 + 1000*2.5e-6
	if !approx(ev.CostExternalBRL, wantUSD*testFX) {
		t.Errorf("external :long: want %v, got %v", wantUSD*testFX, ev.CostExternalBRL)
	}
}

func TestUsageAnthropicHaikuMissingCacheRowChargesInput(t *testing.T) {
	ix, capt := newCostInterceptor(haikuPrices(false))
	ev := drive(t, ix, capt, usageResp("application/json", haikuBody(10000, 8000, 1000, 500), "anthropic-haiku", "chat"))
	// cached e write cobrados como input (fallback conservador).
	wantUSD := 10000*1e-7 + 500*5e-7
	if !approx(ev.CostExternalBRL, wantUSD*testFX) {
		t.Errorf("external fallback: want %v, got %v", wantUSD*testFX, ev.CostExternalBRL)
	}
}

func TestUsageSelfHostedChatPhantomAtHaikuPrice(t *testing.T) {
	ix, capt := newCostInterceptor(map[billing.PriceKey]billing.Price{
		key("anthropic/claude-haiku-5.5", "openrouter-fireworks", "input_token"):  price(1e-7),
		key("anthropic/claude-haiku-5.5", "openrouter-fireworks", "output_token"): price(5e-7),
		key("qwen", "openrouter-fireworks", "input_token"):                        price(9),
	})
	ev := finalizeOne(t, ix, capt, "local-llm", "chat", "qwen", 1000, 100, 0)
	if want := (1000*1e-7 + 100*5e-7) * testFX; !approx(ev.CostLocalPhantomBRL, want) {
		t.Errorf("phantom: want %v, got %v", want, ev.CostLocalPhantomBRL)
	}
	if ev.CostExternalBRL != 0 {
		t.Errorf("external: want 0, got %v", ev.CostExternalBRL)
	}
	if ev.Model != "qwen" {
		t.Errorf("model gravado deve seguir o real, got %q", ev.Model)
	}
}

// warnCapture é um slog.Handler que guarda as mensagens WARN.
type warnCapture struct{ msgs []string }

func (w *warnCapture) Enabled(context.Context, slog.Level) bool { return true }
func (w *warnCapture) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn {
		w.msgs = append(w.msgs, r.Message)
	}
	return nil
}
func (w *warnCapture) WithAttrs([]slog.Attr) slog.Handler { return w }
func (w *warnCapture) WithGroup(string) slog.Handler      { return w }

func TestUsageReportedCostOutOfRangeRejected(t *testing.T) {
	for _, c := range []string{"-0.5", "150"} {
		ix, capt := newCostInterceptor(map[billing.PriceKey]billing.Price{
			key("ds", "openrouter-fireworks", "input_token"):  price(1e-6),
			key("ds", "openrouter-fireworks", "output_token"): price(2e-6),
		})
		wc := &warnCapture{}
		ix.log = slog.New(wc)
		body := `{"model":"ds","usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"cost":` + c + `}}`
		ev := drive(t, ix, capt, usageResp("application/json", body, "openrouter-chat", "chat"))
		if ev.CostSource != "price_table" {
			t.Errorf("cost %s: want price_table, got %q", c, ev.CostSource)
		}
		if want := (100*1e-6 + 50*2e-6) * testFX; !approx(ev.CostExternalBRL, want) {
			t.Errorf("cost %s: external want %v, got %v", c, want, ev.CostExternalBRL)
		}
		found := false
		for _, m := range wc.msgs {
			if m == "usage_cost_out_of_range" {
				found = true
			}
		}
		if !found {
			t.Errorf("cost %s: WARN usage_cost_out_of_range ausente (%v)", c, wc.msgs)
		}
	}
}

func TestUsageReportedCostZeroAccepted(t *testing.T) {
	ix, capt := newCostInterceptor(map[billing.PriceKey]billing.Price{
		key("ds", "openrouter-fireworks", "input_token"): price(1e-6),
	})
	body := `{"model":"ds","usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"cost":0}}`
	ev := drive(t, ix, capt, usageResp("application/json", body, "openrouter-chat", "chat"))
	if ev.CostSource != "provider_reported" || ev.CostExternalBRL != 0 {
		t.Errorf("want provider_reported/0, got %q/%v", ev.CostSource, ev.CostExternalBRL)
	}
}

func TestUsageOpenRouterTTSPricedExternally(t *testing.T) {
	rows := map[billing.PriceKey]billing.Price{
		key(ttsBillingModel, "openrouter", "input_token"):           price(1.5e-5),
		key(ttsBillingModel, "openrouter-fireworks", "input_token"): price(1.5e-5),
	}
	ix, capt := newCostInterceptor(rows)
	resp, _ := ttsResp(200, "audio/mpeg", 100, "openrouter-tts")
	ev := drive(t, ix, capt, resp)
	if want := 100 * 1.5e-5 * testFX; !approx(ev.CostExternalBRL, want) {
		t.Errorf("openrouter-tts external: want %v, got %v", want, ev.CostExternalBRL)
	}
	if ev.CostLocalPhantomBRL != 0 {
		t.Errorf("openrouter-tts phantom: want 0, got %v", ev.CostLocalPhantomBRL)
	}

	ix2, capt2 := newCostInterceptor(map[billing.PriceKey]billing.Price{
		key(ttsBillingModel, "openrouter-fireworks", "input_token"): price(2e-5),
	})
	resp2, _ := ttsResp(200, "audio/mpeg", 100, "kokoro-tts")
	ev2 := drive(t, ix2, capt2, resp2)
	if ev2.CostExternalBRL != 0 {
		t.Errorf("kokoro external: want 0, got %v", ev2.CostExternalBRL)
	}
	if want := 100 * 2e-5 * testFX; !approx(ev2.CostLocalPhantomBRL, want) {
		t.Errorf("kokoro phantom: want %v, got %v", want, ev2.CostLocalPhantomBRL)
	}
}

func TestProviderForUpstreamNewUpstreams(t *testing.T) {
	cases := map[string]string{
		"anthropic-haiku":     "anthropic",
		"openrouter-deepseek": "openrouter-fireworks",
		"openrouter-tts":      "openrouter",
		"openrouter-chat":     "openrouter-fireworks",
		"openai-embed":        "openai",
	}
	for up, want := range cases {
		if got := providerForUpstream(up); got != want {
			t.Errorf("providerForUpstream(%q) = %q, want %q", up, got, want)
		}
	}
}
