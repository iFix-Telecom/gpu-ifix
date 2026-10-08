package proxy

// quick-261007-t9f — cost attribution tests for FinalizeRequest.
//
// Decision (Pedro, 2026-10-07): "os valores locais do pod devem ser
// considerados como economia". cost_local_phantom_brl = savings = what the
// self-hosted infra would have cost externally. Traffic served by a paid
// external upstream is NOT savings. The two cost columns are therefore
// mutually exclusive per upstream.

import (
	"context"
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/billing"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// captureEnqueuer records every billing.Event the interceptor enqueues.
type captureEnqueuer struct{ events []billing.Event }

func (c *captureEnqueuer) Enqueue(e billing.Event) { c.events = append(c.events, e) }

const testFX = 5.0

func newCostInterceptor(rows map[billing.PriceKey]billing.Price) (*UsageInterceptor, *captureEnqueuer) {
	acct := billing.NewAccountant()
	ix := NewUsageInterceptor(acct, nil, billing.NewStaticPricesLoader(rows), nil, nil, testFX, discardLog())
	capt := &captureEnqueuer{}
	ix.flusher = capt
	return ix, capt
}

func price(usd float64) billing.Price { return billing.Price{UnitCostUSD: usd} }

func key(model, provider, unit string) billing.PriceKey {
	return billing.PriceKey{Model: model, Provider: provider, Unit: unit}
}

// finalizeOne seeds an accountant slot, finalizes it and returns the single
// enqueued event (fails the test if not exactly one).
func finalizeOne(t *testing.T, ix *UsageInterceptor, capt *captureEnqueuer, upstream, route, model string, in, out, embeds int64) billing.Event {
	t.Helper()
	reqID := uuid.NewString()
	u := &billing.RequestUsage{}
	u.TokensIn.Store(in)
	u.TokensOut.Store(out)
	u.EmbedsCount.Store(embeds)
	u.SetModel(model)
	ix.accountant.Set(reqID, u)
	ctx := auditctx.WithBillingUpstream(context.Background(), upstream)
	ctx = auditctx.WithBillingRoute(ctx, route)
	ix.FinalizeRequest(ctx, reqID, "/v1/x", "final")
	if len(capt.events) != 1 {
		t.Fatalf("want 1 event, got %d", len(capt.events))
	}
	return capt.events[0]
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

// External paid upstream → only cost_external; phantom (savings) is 0 even
// though an openrouter-fireworks reference price exists for the model.
func TestFinalizeExternalUpstreamHasNoPhantom(t *testing.T) {
	ix, capt := newCostInterceptor(map[billing.PriceKey]billing.Price{
		key("deepseek/x", "openrouter-fireworks", "input_token"):  price(1e-6),
		key("deepseek/x", "openrouter-fireworks", "output_token"): price(2e-6),
	})
	ev := finalizeOne(t, ix, capt, "openrouter-chat", "chat", "deepseek/x", 100, 50, 0)
	if ev.CostLocalPhantomBRL != 0 {
		t.Errorf("phantom: want 0 for external upstream, got %v", ev.CostLocalPhantomBRL)
	}
	want := (100*1e-6 + 50*2e-6) * testFX
	if !approx(ev.CostExternalBRL, want) {
		t.Errorf("external: want %v, got %v", want, ev.CostExternalBRL)
	}
}

// Our own pod (override name emergency_pod_llm) → only phantom; external 0.
func TestFinalizeEmergencyPodIsSavings(t *testing.T) {
	ix, capt := newCostInterceptor(map[billing.PriceKey]billing.Price{
		key("qwen", "openrouter-fireworks", "input_token"):  price(1e-6),
		key("qwen", "openrouter-fireworks", "output_token"): price(2e-6),
		key("qwen", "emergency_pod_llm", "input_token"):     price(9e-6), // must be ignored
	})
	ev := finalizeOne(t, ix, capt, "emergency_pod_llm", "chat", "qwen", 100, 50, 0)
	if ev.CostExternalBRL != 0 {
		t.Errorf("external: want 0 for self-hosted, got %v", ev.CostExternalBRL)
	}
	want := (100*1e-6 + 50*2e-6) * testFX
	if !approx(ev.CostLocalPhantomBRL, want) {
		t.Errorf("phantom: want %v, got %v", want, ev.CostLocalPhantomBRL)
	}
}

// Local embed priced per token: embed_request row is optional when tokens are
// present — no price-missing metric, phantom = tokens * input price * fx.
func TestFinalizeLocalEmbedEmbedRequestOptional(t *testing.T) {
	ix, capt := newCostInterceptor(map[billing.PriceKey]billing.Price{
		key("bge-m3", "openrouter-fireworks", "input_token"): price(1e-8),
	})
	missing := obs.GatewayPricesMissing.WithLabelValues("bge-m3", "openrouter-fireworks", "embed_request")
	before := testutil.ToFloat64(missing)
	ev := finalizeOne(t, ix, capt, "local-embed", "embed", "bge-m3", 1000, 0, 3)
	if after := testutil.ToFloat64(missing); after != before {
		t.Errorf("gateway_prices_missing{embed_request} incremented: %v -> %v", before, after)
	}
	want := 1000 * 1e-8 * testFX
	if !approx(ev.CostLocalPhantomBRL, want) {
		t.Errorf("phantom: want %v, got %v", want, ev.CostLocalPhantomBRL)
	}
	if ev.CostExternalBRL != 0 {
		t.Errorf("external: want 0, got %v", ev.CostExternalBRL)
	}
}

// When embed_request IS the only dimension (tokens_in == 0), a missing row is
// still surfaced (WARN + metric) — the pre-existing behaviour is preserved.
func TestFinalizeEmbedOnlyCountStillWarnsWhenMissing(t *testing.T) {
	ix, capt := newCostInterceptor(map[billing.PriceKey]billing.Price{})
	missing := obs.GatewayPricesMissing.WithLabelValues("embed-only-t9f", "openrouter-fireworks", "embed_request")
	before := testutil.ToFloat64(missing)
	_ = finalizeOne(t, ix, capt, "local-embed", "embed", "embed-only-t9f", 0, 0, 2)
	if after := testutil.ToFloat64(missing); after != before+1 {
		t.Errorf("gateway_prices_missing{embed_request}: want +1, got %v -> %v", before, after)
	}
}

// When an embed_request row DOES exist alongside tokens, it is still priced.
func TestFinalizeEmbedRequestPricedWhenPresent(t *testing.T) {
	ix, capt := newCostInterceptor(map[billing.PriceKey]billing.Price{
		key("e5", "openrouter-fireworks", "input_token"):   price(1e-8),
		key("e5", "openrouter-fireworks", "embed_request"): price(1e-4),
	})
	ev := finalizeOne(t, ix, capt, "embed-gpu", "embed", "e5", 1000, 0, 2)
	want := (1000*1e-8 + 2*1e-4) * testFX
	if !approx(ev.CostLocalPhantomBRL, want) {
		t.Errorf("phantom: want %v, got %v", want, ev.CostLocalPhantomBRL)
	}
}
