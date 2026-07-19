# 22-04 SUMMARY — CV-02: gateway serve chat Gemini

**Status:** ✅ complete · **Data:** 2026-07-19 · **Requirement:** CV-02

## Descoberta (Task 1)
Ambos os caminhos do plano falhavam pro Gemini:
- Caminho A (nativo): defeito path.Join (`/v1beta/openai/v1/...`) → precisa director Go.
- Caminho B (alias): gemini via openrouter-chat → **404 "No endpoints found"**. Root cause achado no **código** (`config.go:390`): default `provider.order=["novita"]` + `allow_fallbacks=false` injetado em todo request → gemini (Google-only na OpenRouter) sem endpoint. FATO, não hipótese.

## Fix (Task 2, checkpoint "Caminho B-fixed")
**1 env var, zero código:** `UPSTREAM_LLM_OPENROUTER_ALLOW_FALLBACKS=true` no stack 38 (Portainer ep6, compose+env via PUT durável) → novita primeiro, cai pra Google quando novita não serve. deepseek/llama ficam novita (inalterado).
+ alias `google/gemini-2.5-flash-lite` → openrouter-chat.

## Prova (via gateway prod)
- Direto OpenRouter: gemini+`allow_fallbacks:true`→provider=Google; deepseek→provider=Novita.
- Via gateway: `model=google/gemini-2.5-flash-lite` + tools → **200, finish_reason=tool_calls, get_weather({"city":"Sao Paulo"})** (Pitfall 4 OK).
- billing_events: cost_external_brl=**0.000020** (17in/6out × preço gemini 22-01 × fx 5.10).
- qwen intacto; Phase 20 não regrediu (health develop-1f6e6a5).

## Efeito colateral (aceito)
`allow_fallbacks=true` vale p/ todo openrouter-chat → se Novita cair, secundárias caem pra outro provider (antes falhavam). Mais resiliência, risco de custo marginal.

## Escopo honesto (Pitfall 8)
CV-02 cobre só Gemini chat OpenAI-compat. Media/visão Gemini (`generate_content` nativo) fica FORA — reportado como off-gateway no 22-07.

## Pré-requisito de 22-06
Gateway serve gemini-2.5-flash-lite (+ preços gemini-2.5-flash prontos no 22-01) → classifier/format-hint podem funnelar sem quebrar tool-calling.

## Rollback
Remover alias + reverter env no stack 38 (backup scratchpad/stack38_backup.json → default false).

## Desvio do plano
Plano assumia B = "1 comando proven". Realidade: B exigia o fix do provider-default (1 env var). A não usado. Documentado em 22-04-GEMINI-CHAT.md.

## Self-Check: PASSED
