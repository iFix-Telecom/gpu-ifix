package main

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/billing"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// quick 261020: o reconcile recalcula usage_counters.tokens_in com a mesma
// ponderação de cache do hot path — senão o --apply desfaria a mudança.
func TestQuotaTokensInByDate(t *testing.T) {
	prices := billing.NewStaticPricesLoader(map[billing.PriceKey]billing.Price{
		{Model: "claude-haiku-5-5", Provider: "anthropic", Unit: "input_token"}:       {UnitCostUSD: 0.10e-6},
		{Model: "claude-haiku-5-5", Provider: "anthropic", Unit: "cache_read_token"}:  {UnitCostUSD: 0.01e-6},
		{Model: "claude-haiku-5-5", Provider: "anthropic", Unit: "cache_write_token"}: {UnitCostUSD: 0.125e-6},
	})
	d1 := pgtype.Date{Time: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), Valid: true}
	d2 := pgtype.Date{Time: time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC), Valid: true}
	rows := []gen.SumBillingQuotaTokensByDateRow{
		{Date: d1, Upstream: "anthropic-haiku", Model: "claude-haiku-5-5", TokensIn: 10000, TokensCacheRead: 8000, TokensCacheWrite: 1000},
		{Date: d1, Upstream: "openrouter-chat", Model: "deepseek/deepseek-v4-flash", TokensIn: 5000, TokensCacheRead: 4000},
		{Date: d2, Upstream: "local-llm", Model: "qwen", TokensIn: 700},
	}
	got := quotaTokensInByDate(prices, rows)
	if got["2026-10-20"] != 3050+5000 {
		t.Errorf("dia 1: want 8050, got %d", got["2026-10-20"])
	}
	if got["2026-10-21"] != 700 {
		t.Errorf("dia 2: want 700, got %d", got["2026-10-21"])
	}
}
