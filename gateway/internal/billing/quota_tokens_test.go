package billing

import "testing"

// Preços reais do passo ① (claude-haiku-5-5 / anthropic), US$ por token:
// input 0,10/M, cache_read 0,01/M, cache_write 0,125/M, cache_write_1h 0,20/M.
func anthropicHaikuLoader() *PricesLoader {
	return NewStaticPricesLoader(map[PriceKey]Price{
		{Model: "claude-haiku-5-5", Provider: "anthropic", Unit: "input_token"}:           {UnitCostUSD: 0.10e-6},
		{Model: "claude-haiku-5-5", Provider: "anthropic", Unit: "output_token"}:          {UnitCostUSD: 0.50e-6},
		{Model: "claude-haiku-5-5", Provider: "anthropic", Unit: "cache_read_token"}:      {UnitCostUSD: 0.01e-6},
		{Model: "claude-haiku-5-5", Provider: "anthropic", Unit: "cache_write_token"}:     {UnitCostUSD: 0.125e-6},
		{Model: "claude-haiku-5-5", Provider: "anthropic", Unit: "cache_write_1h_token"}:  {UnitCostUSD: 0.20e-6},
		{Model: "claude-haiku-5-5:long", Provider: "anthropic", Unit: "input_token"}:      {UnitCostUSD: 0.50e-6},
		{Model: "claude-haiku-5-5:long", Provider: "anthropic", Unit: "cache_read_token"}: {UnitCostUSD: 0.05e-6},
	})
}

func TestCacheWeightsAnthropic(t *testing.T) {
	wr, ww := CacheWeights(anthropicHaikuLoader(), "claude-haiku-5-5", "anthropic")
	if wr != 0.1 || ww != 1.25 {
		t.Fatalf("pesos: want 0.1/1.25, got %v/%v", wr, ww)
	}
}

func TestCacheWeightsMissingPricesFallBackToOne(t *testing.T) {
	l := anthropicHaikuLoader()
	cases := []struct {
		name            string
		loader          *PricesLoader
		model, provider string
	}{
		{"loader nil", nil, "claude-haiku-5-5", "anthropic"},
		{"par sem preço nenhum", l, "deepseek/deepseek-v4-flash", "openrouter-fireworks"},
		{"input zero", NewStaticPricesLoader(map[PriceKey]Price{
			{Model: "m", Provider: "p", Unit: "input_token"}:      {UnitCostUSD: 0},
			{Model: "m", Provider: "p", Unit: "cache_read_token"}: {UnitCostUSD: 1e-8},
		}), "m", "p"},
	}
	for _, c := range cases {
		wr, ww := CacheWeights(c.loader, c.model, c.provider)
		if wr != 1 || ww != 1 {
			t.Errorf("%s: want 1/1, got %v/%v", c.name, wr, ww)
		}
	}
	// :long tem cache_read mas não cache_write → só a escrita cai em 1.
	wr, ww := CacheWeights(l, "claude-haiku-5-5:long", "anthropic")
	if wr != 0.1 || ww != 1 {
		t.Errorf(":long: want 0.1/1, got %v/%v", wr, ww)
	}
}

func TestQuotaTokensInFormula(t *testing.T) {
	cases := []struct {
		name            string
		in, read, write int64
		wr, ww          float64
		want            int64
	}{
		// Caso real do card: tokens_in 54.899 com 54.895 lidos do cache.
		// 4 + round(5489,5) = 4 + 5490 = 5494.
		{"real anthropic leitura", 54899, 54895, 0, 0.1, 1.25, 5494},
		{"leitura + escrita", 10000, 8000, 1000, 0.1, 1.25, 1000 + 800 + 1250},
		{"sem cache igual a hoje", 12345, 0, 0, 0.1, 1.25, 12345},
		{"peso 1 igual a hoje", 10000, 8000, 1000, 1, 1, 10000},
		{"cache maior que tokens_in (dado ruim) clampa uncached", 100, 200, 0, 0.1, 1, 20},
	}
	for _, c := range cases {
		if got := QuotaTokensIn(c.in, c.read, c.write, c.wr, c.ww); got != c.want {
			t.Errorf("%s: want %d, got %d", c.name, c.want, got)
		}
	}
}

func TestQuotaTokensInForUsesBillingPriceKey(t *testing.T) {
	l := anthropicHaikuLoader()
	// anthropic-haiku: o model do frame é ignorado; vale claude-haiku-5-5.
	if got := QuotaTokensInFor(l, "anthropic-haiku", "claude-haiku-5-5-20261001", 54899, 54895, 0); got != 5494 {
		t.Errorf("anthropic-haiku: want 5494, got %d", got)
	}
	// >100k → linha :long (cache_read 0,05/0,50 = 0,1; sem cache_write → 1).
	if got := QuotaTokensInFor(l, "anthropic-haiku", "x", 120000, 100000, 10000); got != 10000+10000+10000 {
		t.Errorf(":long: want 30000, got %d", got)
	}
	// Upstream sem preço → peso 1 (comportamento de hoje).
	if got := QuotaTokensInFor(l, "openrouter-chat", "deepseek/deepseek-v4-flash", 12050, 11800, 0); got != 12050 {
		t.Errorf("sem preço: want 12050, got %d", got)
	}
	// Sem cache → inalterado mesmo com preço.
	if got := QuotaTokensInFor(l, "anthropic-haiku", "x", 777, 0, 0); got != 777 {
		t.Errorf("sem cache: want 777, got %d", got)
	}
}

func TestInsertParamsQuotaTokensIn(t *testing.T) {
	// Preenchido → vai para usage_counters; billing_events segue cru.
	p := insertParams(Event{TokensIn: 54899, TokensCacheRead: 54895, QuotaTokensIn: 5494})
	if p.TokensIn != 54899 || p.QuotaTokensIn != 5494 || p.TokensCacheRead != 54895 {
		t.Errorf("preenchido: in=%d quota=%d cr=%d", p.TokensIn, p.QuotaTokensIn, p.TokensCacheRead)
	}
	// Não preenchido (caller antigo) → cai no tokens_in cru.
	if p := insertParams(Event{TokensIn: 300}); p.QuotaTokensIn != 300 {
		t.Errorf("fallback: want 300, got %d", p.QuotaTokensIn)
	}
	if p := insertParams(Event{}); p.QuotaTokensIn != 0 {
		t.Errorf("vazio: want 0, got %d", p.QuotaTokensIn)
	}
}

func TestNewPricesLoaderFromRows(t *testing.T) {
	l := NewPricesLoaderFromRows(nil, nil)
	if _, ok := l.Get("a", "b", "input_token"); ok {
		t.Fatal("loader vazio devolveu preço")
	}
	if wr, ww := CacheWeights(l, "a", "b"); wr != 1 || ww != 1 {
		t.Fatalf("loader vazio: want 1/1, got %v/%v", wr, ww)
	}
}
