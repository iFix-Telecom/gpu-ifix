package billing

import "math"

// Cota de tokens ponderada pelo preço do cache (quick 261020, card
// OPERACOES-28152).
//
// billing_events.tokens_in guarda o prompt_tokens cru, que INCLUI os tokens
// lidos do cache (tokens_cache_read) e os gravados no cache
// (tokens_cache_write) — tanto no passo ① traduzido
// (anthropic_translate_resp.go: prompt = input + cache_read + cache_creation)
// quanto no OpenRouter (prompt_tokens_details são subconjuntos de
// prompt_tokens). A cobrança já pesa cada parcela pelo seu preço; a cota
// (usage_counters.tokens_in, lida por quota.CheckQuotaToday/Month) passa a
// pesar igual:
//
//	uncached = max(0, tokens_in − cache_read − cache_write)
//	quota_in = uncached + round(cache_read × w_read) + round(cache_write × w_write)
//	w_x      = preço(cache_x_token) / preço(input_token)   do mesmo (model, provider)
//
// Sem preço de input_token (ou input ≤ 0) ou sem a linha de cache, o peso é 1
// — exatamente o comportamento anterior (conservador). O peso de escrita pode
// passar de 1: é o preço real (Anthropic: 1,25×). billing_events continua com
// os números crus; só o contador de cota muda.

// Unidades de preço usadas na ponderação.
const (
	unitInputToken      = "input_token"
	unitCacheReadToken  = "cache_read_token"
	unitCacheWriteToken = "cache_write_token"
)

// Chaves de preço do passo ① (provider anthropic). A linha :long vale quando
// a entrada total (prompt_tokens) passa de AnthropicLongContextThreshold.
// Fonte única para a cobrança (proxy.priceAnthropicDirect) e para a cota.
const (
	AnthropicHaikuUpstream        = "anthropic-haiku"
	AnthropicHaikuProvider        = "anthropic"
	AnthropicHaikuPriceModel      = "claude-haiku-5-5"
	AnthropicHaikuLongPriceModel  = "claude-haiku-5-5:long"
	AnthropicLongContextThreshold = 100000
)

// ProviderForUpstream mapeia o nome do upstream ("openrouter-chat",
// "openai-embed", ...) para a coluna provider da tabela de preços. O nome do
// upstream é o identificador de fio; provider agrupa o preço por fornecedor.
func ProviderForUpstream(upstream string) string {
	switch upstream {
	case "openrouter-chat", "openrouter-deepseek":
		return "openrouter-fireworks"
	case AnthropicHaikuUpstream:
		return AnthropicHaikuProvider
	case "openrouter-tts":
		// Chave (tts-1, openrouter, input_token) — não colide com o phantom
		// de TTS self-hosted (tts-1, openrouter-fireworks).
		return "openrouter"
	case "openai-embed":
		return "openai"
	case "openai-whisper":
		return "openai"
	default:
		return upstream
	}
}

// QuotaPriceKey devolve o (model, provider) cujo preço pondera o cache na
// cota — o mesmo par que a cobrança usa no ramo de tabela de preços:
// anthropic-haiku → claude-haiku-5-5 (ou :long quando longContext, isto é,
// tokens_in > AnthropicLongContextThreshold) / anthropic; demais → (model,
// ProviderForUpstream(upstream)).
func QuotaPriceKey(upstream, model string, longContext bool) (string, string) {
	if upstream == AnthropicHaikuUpstream {
		if longContext {
			return AnthropicHaikuLongPriceModel, AnthropicHaikuProvider
		}
		return AnthropicHaikuPriceModel, AnthropicHaikuProvider
	}
	return model, ProviderForUpstream(upstream)
}

// CacheWeights devolve (w_read, w_write) para (model, provider). Cada peso é
// preço(cache_x)/preço(input_token); 1 quando o loader é nil, falta o preço
// de input (ou ele é ≤ 0) ou falta a linha de cache correspondente.
func CacheWeights(prices *PricesLoader, model, provider string) (float64, float64) {
	if prices == nil {
		return 1, 1
	}
	in, ok := prices.Get(model, provider, unitInputToken)
	if !ok || in.UnitCostUSD <= 0 {
		return 1, 1
	}
	w := func(unit string) float64 {
		p, ok := prices.Get(model, provider, unit)
		if !ok || p.UnitCostUSD < 0 {
			return 1
		}
		// Arredonda a razão em 1e-9 para que 0,01/0,10 dê 0,1 exato e o
		// round() do contador não oscile por ruído de ponto flutuante.
		return math.Round(p.UnitCostUSD/in.UnitCostUSD*1e9) / 1e9
	}
	return w(unitCacheReadToken), w(unitCacheWriteToken)
}

// QuotaTokensIn aplica a fórmula da cota sobre os números crus do billing.
// Sem cache (cacheRead = cacheWrite = 0) devolve tokensIn inalterado.
func QuotaTokensIn(tokensIn, cacheRead, cacheWrite int64, wRead, wWrite float64) int64 {
	if cacheRead < 0 {
		cacheRead = 0
	}
	if cacheWrite < 0 {
		cacheWrite = 0
	}
	if cacheRead == 0 && cacheWrite == 0 {
		return tokensIn
	}
	uncached := tokensIn - cacheRead - cacheWrite
	if uncached < 0 {
		uncached = 0
	}
	return uncached +
		int64(math.Round(float64(cacheRead)*wRead)) +
		int64(math.Round(float64(cacheWrite)*wWrite))
}

// QuotaTokensInFor é o atalho usado no hot path e no reconcile: resolve o par
// de preço, os pesos e aplica a fórmula.
func QuotaTokensInFor(prices *PricesLoader, upstream, model string, tokensIn, cacheRead, cacheWrite int64) int64 {
	if cacheRead <= 0 && cacheWrite <= 0 {
		return tokensIn
	}
	m, p := QuotaPriceKey(upstream, model, tokensIn > AnthropicLongContextThreshold)
	wr, ww := CacheWeights(prices, m, p)
	return QuotaTokensIn(tokensIn, cacheRead, cacheWrite, wr, ww)
}
