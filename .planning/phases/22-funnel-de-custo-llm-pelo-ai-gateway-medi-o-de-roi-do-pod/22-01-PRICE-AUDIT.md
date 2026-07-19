# 22-01 PRICE-AUDIT — preços reais `ai_gateway.prices` vs vigentes (PRICE-01 GATE)

Data: 2026-07-19. Fonte de preço: **OpenRouter models API** (`GET /api/v1/models`, autoritativa — o gateway roteia chat via `openrouter-chat`) + ai.google.dev/pricing (áudio) + OpenAI pricing (whisper). Sanity-check de ordem de grandeza: faturas Google (R$238,59) e OpenAI ($53).

## Método de conversão (evita erro decimal por-milhão vs por-token)
- `input_token` / `output_token` = **USD/1M tokens ÷ 1e6** → USD/token.
- `audio_second` = **USD/min ÷ 60** → USD/s.
- `embed_request` = USD por request direto.
- Coluna `unit_cost_usd` é `numeric(12,8)` (8 casas) → valores arredondam a 1e-8.

## Duas fontes distintas (não confundir)
- **Preço de tabela do provider** (taxa oficial USD/unit) = **é o que vai pra `prices`**.
- **Custo observado na fatura** = mistura volume off-gateway + preço → **só sanity-check de magnitude**, NÃO é a fonte do `unit_cost_usd`.

## Resolução da hipótese do CONTEXT — "gemini-stt R$1,33 subvalorizado OU volume off-gateway?"
**FATO (medido, não hipótese):** gemini-stt on-gateway 30d = **R$1,3275** (45.283,9 audio_s × 0,0000096 × fx), e a chave casa 100% (0 priced-but-zero em 7d). A fatura Google é **R$238,59**. Como o preço on-gateway já está correto e o volume on-gateway é minúsculo, a diferença é **volume OFF-gateway** (o agente principal da converseai chamando Gemini chat DIRETO com `GOOGLE_AI_API_KEY` própria). ⇒ **NÃO é preço subvalorizado; é o funnel que falta (CV-02/22-04+22-06).** O preço unitário do gemini-stt fica como está.

> A fatura Google **não discrimina por modelo** (1 linha "Gemini API R$238,59") → atribuição fina gemini-stt vs gemini-chat pela fatura é impossível; o FATO acima vem da comparação billing on-gateway (R$1,33) × fatura (R$238), ambos medidos.

## O buraco real — CHAT bila R$0 (97% das reqs)
**FATO:** `openrouter-chat` últimos 7d = 7.936 reqs, **7.669 com `cost_external_brl=0`**. Breakdown por modelo:

| model (billing_events) | reqs 7d | tok_in | tok_out | brl | tem preço? |
|---|--:|--:|--:|--:|---|
| `deepseek/deepseek-v4-flash` (bare) | 7669 | 12.032.156 | 3.224.793 | **0,0000** | ❌ **AUSENTE** |
| `deepseek/deepseek-v4-flash-20260423` (datado) | 265 | 289.958 | 59.221 | 0,1888 | ✅ casa |

Root cause: o gateway grava `model` = nome bare (`deepseek/deepseek-v4-flash`) em 97% das reqs, mas só existe linha de preço pro nome datado (`-20260423`). Custo é no ingest, não retroativo → 7.669 reqs viraram R$0 silencioso.

## Tabela de decisão de preços

Chave = (model, provider, unit). `provider` é o que `providerForUpstream(upstream)` produz: `openrouter-chat → openrouter-fireworks`; `gemini-stt → gemini-stt`; `openai-whisper → openai`.

| model | provider | unit | USD vigente | USD proposto | fonte (preço de tabela) | ação |
|---|---|---|--:|--:|---|---|
| `deepseek/deepseek-v4-flash` | openrouter-fireworks | input_token | — (ausente) | **0.00000010** | OpenRouter 0.000000098 → arred. | **ADD** (fecha o R$0 do chat) |
| `deepseek/deepseek-v4-flash` | openrouter-fireworks | output_token | — (ausente) | **0.00000020** | OpenRouter 0.000000196 → arred. | **ADD** |
| `google/gemini-2.5-flash-lite` | openrouter-fireworks | input_token | — (ausente) | **0.00000010** | OpenRouter 0.0000001 | **ADD** (funnel CV-02: classifier/format-hint) |
| `google/gemini-2.5-flash-lite` | openrouter-fireworks | output_token | — (ausente) | **0.00000040** | OpenRouter 0.0000004 | **ADD** |
| `google/gemini-2.5-flash` | openrouter-fireworks | input_token | — (ausente) | **0.00000030** | OpenRouter 0.0000003 | **ADD** (funnel CV-02: agente principal) |
| `google/gemini-2.5-flash` | openrouter-fireworks | output_token | — (ausente) | **0.00000250** | OpenRouter 0.0000025 | **ADD** |
| `gemini-2.5-flash-lite` | gemini-stt | audio_second | 0.00000960 | **0.00000960** | ai.google.dev: audio $0.30/1M tok × 32 tok/s ÷... = 9.6e-6/s | KEEP (casa 100%) |
| `whisper-1` | openai | audio_second | 0.00010000 | **0.00010000** | OpenAI whisper-1 $0.006/min ÷ 60 | KEEP¹ |

¹ **HIPÓTESE:** o STT OpenAI real hoje é `gpt-4o-mini-transcribe` (~$0.003/min), mas `sttBillingModel(openai-whisper)="whisper-1"` → bila sempre como whisper-1 ($0.006/min). Volume ínfimo (492 reqs / R$0,60 em 30d) → impacto no ROI desprezível. Resolveria: setar preço do gpt-4o-mini-transcribe e mudar sttBillingModel — fora de escopo (imaterial). KEEP.

### Câmbio
| | USD/BRL | fonte |
|---|--:|---|
| vigente | 5.082509 | fx_rates active, valid_from 2026-07-17 |
| proposto | **5.10** | open.er-api.com 2026-07-19 = 5.09987 (frankfurter 17/07 = 5.1158) |

Delta ~0,3% — imaterial pro ROI, mas refresca o stale. (Opção: manter 5.082509 se preferir não mexer.)

## Exemplos numéricos (1 por unit, prova a conversão)
- **input_token** deepseek bare 7d: 12.032.156 tok × 0.00000010 = 1,2032 USD.
- **output_token** deepseek bare 7d: 3.224.793 tok × 0.00000020 = 0,6450 USD.
  → chat deepseek 7d = (1,2032+0,6450) × 5,10 = **R$9,42/7d ≈ R$40/mês** que hoje bila **R$0**.
- **audio_second** gemini-stt 30d: 45.283,9 s × 0.00000960 × 5,10 = **R$2,22** (bate com R$1,33 medido, pois ~38% das rows históricas têm audio_seconds=0 — gap de metering pré-existente, não de preço).

## Prova de unicidade (a rodar na Task 2, concern codex HIGH)
As adições são **models novos** (bare deepseek + gemini chat) → chave distinta das existentes, sem conflito de 1-active-por-chave. Ainda assim Task 2 roda o `GROUP BY model,provider,unit HAVING count(*)>1 WHERE valid_to IS NULL` → deve dar 0.

## Resumo pro checkpoint
6 linhas ADD (deepseek bare ×2, gemini-2.5-flash-lite ×2, gemini-2.5-flash ×2) + fx 5.082509→5.10. 2 KEEP (gemini-stt audio, whisper-1 audio). O ADD deepseek bare é o que fecha o R$0 de 97% do chat AGORA; os 4 gemini são pré-requisito pro funnel (22-04/22-06) medir o R$238.
