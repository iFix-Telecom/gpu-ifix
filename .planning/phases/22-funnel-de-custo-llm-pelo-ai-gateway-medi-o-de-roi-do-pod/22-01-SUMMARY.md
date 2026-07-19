# 22-01 SUMMARY — PRICE-01 (GATE) corrigir `ai_gateway.prices`

**Status:** ✅ complete · **Data:** 2026-07-19 · **Requirement:** PRICE-01

## O que foi feito
Corrigido o pré-requisito de medição da fase: `ai_gateway.prices` agora tem preço real por (model,provider,unit) pros upstreams que a converseai vai funnelar, e fx atualizado. Tudo hot-reload (NOTIFY), zero deploy.

## Root cause encontrado (Task 1 audit)
**FATO:** `openrouter-chat` bilava **R$0 em 97% das reqs** (7.669/7.936 em 7d) — o modelo gravado é `deepseek/deepseek-v4-flash` (bare), mas só existia linha de preço pro nome datado `-20260423`. Custo é no ingest (não-retroativo) → R$0 silencioso em ~R$40/mês de chat.

**FATO (resolve hipótese do CONTEXT):** gemini-stt on-gateway 30d = R$1,33 (preço 0.0000096 casa 100%) vs fatura Google R$238,59 → o R$238 é **volume OFF-gateway** (agente principal Gemini chat direto), **não** preço subvalorizado. Unit price do gemini-stt mantido.

## Mutações em prod (Task 2, checkpoint aprovado "aprovar tudo + fx 5.10")
Via `gatewayctl prices set` no container `ai-gateway-prod_gateway` (worker-vm):

| model | provider | unit | USD | ação |
|---|---|---|--:|---|
| deepseek/deepseek-v4-flash | openrouter-fireworks | input_token | 0.00000010 | ADD |
| deepseek/deepseek-v4-flash | openrouter-fireworks | output_token | 0.00000020 | ADD |
| google/gemini-2.5-flash-lite | openrouter-fireworks | input_token | 0.00000010 | ADD |
| google/gemini-2.5-flash-lite | openrouter-fireworks | output_token | 0.00000040 | ADD |
| google/gemini-2.5-flash | openrouter-fireworks | input_token | 0.00000030 | ADD |
| google/gemini-2.5-flash | openrouter-fireworks | output_token | 0.00000250 | ADD |

FX USD/BRL 5.082509 → **5.10** (fx_id ee5c75ac). Notes="revalidado jul/2026 phase 22 PRICE-01". Fonte de preço: OpenRouter models API (autoritativa).

## Prova (must_haves)
- **1 active por chave:** `GROUP BY model,provider,unit HAVING count(*)>1 WHERE valid_to IS NULL` → **0 rows** ✓
- **billing coerente:** request real chat (chat-ifix, alias qwen→deepseek/deepseek-v4-flash) 9+10 tok → cost_external_brl=**0,000014** (= 9×1e-7 + 10×2e-7 ×5,10) ✓; outra req 454/232 tok → 0,000468 ✓
- **sem zeros pós-insert:** janela `ts > 15:04:25` external upstreams → **zero_com_unidade=0** (chat+whisper) ✓
- **fx atual:** fx_rates active = 5.10 ✓
- **não-retroativo confirmado:** única row zero recente (15:03:53) é pré-insert — esperado.

## Artefatos
- `22-01-PRICE-AUDIT.md` — tabela de decisão de preços, fontes, exemplos numéricos, resolução da hipótese.

## GATE liberado
PRICE-01 fechado → MEASURE-01 (22-07) pode concluir ROI sobre preços reais. Os 4 preços gemini são pré-requisito do funnel CV-02 (22-04/22-06) — já plantados.

## Self-Check: PASSED
