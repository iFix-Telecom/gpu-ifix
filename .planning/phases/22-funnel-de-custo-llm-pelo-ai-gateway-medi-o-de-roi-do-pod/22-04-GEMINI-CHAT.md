# 22-04 GEMINI-CHAT — prova do path Gemini + decisão A/B

Data: 2026-07-19 · Requirement: CV-02

## Baseline (rollback anchor)
`gatewayctl model-alias list` antes: NÃO havia alias de CHAT para gemini (só `whisper`/`whisper-large-v3` → `gemini-stt` STT, pré-existentes). Alias temporário criado e **removido** ao fim (baseline restaurado).

## Caminho A (nativo) — prova de path
Endpoint OpenAI-compat do Gemini = `/v1beta/openai/chat/completions`. O director genérico faz `path.Join(upstream.Path, inboundPath)` → `/v1beta/openai` + `/v1/chat/completions` = **`/v1beta/openai/v1/chat/completions`** (`/v1` extra).
- Curl com auth FAKE: ambos os paths → HTTP 400 (auth-gated, não distingue 404 de path — inconclusivo por curl).
- **Defeito estrutural confirmado por análise** (pesquisa §CV-02): o path.Join injeta `/v1` extra → precisa **director dedicado** de reescrita de path (código Go novo).
- **PORÉM Google É alcançável pelo gateway:** o upstream `gemini-stt` (STT) aponta pro `generativelanguage.googleapis.com` e **funciona em prod** (20.916 reqs billing OK). Conectividade + auth Google já provadas — o único bloqueio de A é o path do director de chat.

## Caminho B (alias → openrouter-chat) — PROVA: NÃO funciona pro Gemini
Criei alias `google/gemini-2.5-flash-lite` (nome=literal do app) → openrouter-chat → target `google/gemini-2.5-flash-lite`. Request via gateway (tenant converseai):
```
{"message":"No endpoints found for google/gemini-2.5-flash-lite.","code":404}
```
Dispatcher roteou correto (`upstream=openrouter-chat`), mas OpenRouter recusou.

### Root cause (FATO)
- gemini-2.5-flash-lite na OpenRouter só tem provider **Google / Google AI Studio** (`/models/.../endpoints`).
- Direto na OpenRouter (key iFix, SEM provider) = **OK 3/3** (não flaky).
- Forçar `provider:{order:["novita"],allow_fallbacks:false}` direto = **reproduz o 404 EXATO**.
- ⇒ o request do openrouter-chat do gateway carrega um constraint de provider (Novita/Fireworks) que **exclui Google** → gemini sem endpoint.
- As secundárias do spec 113 (deepseek/llama) funcionam por B porque **estão na Novita**; **gemini é a exceção** (Google-only).

**HIPÓTESE:** o constraint vem de `UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER` (vazio no env, mas possivelmente serializado como filtro que casa nada) OU default hardcoded no código do director. Resolveria: capturar o body outbound literal que o gateway envia à OpenRouter (não trivial sem instrumentar).

## Conclusão para o checkpoint
Nenhum dos dois caminhos é "1 comando proven" para o Gemini:
- **A** = director dedicado (Go) — Google alcançável (STT prova), mas exige código+build+redeploy do gateway prod.
- **B** = fixar o provider-constraint do openrouter-chat p/ permitir Google no gemini — mecanismo incerto (env/config vazios → provável code default), também exige mudança no gateway.
- **Defer** = deixar o R$238 gemini OFF-gateway, reportado como gasto conhecido off-gateway no 22-07 (escopo honesto, Pitfall 8). O funnel ainda captura STT + deepseek secundárias.

ESCOPO (locked): CV-02 cobre só Gemini chat OpenAI-compat; media/visão nativo (`generate_content`) fica FORA de qualquer caminho.

---

## DECISÃO: Caminho B-fixed (checkpoint aprovado)

Root cause definitivo (código, FATO): `gateway/internal/config/config.go:390`
```go
UpstreamOpenRouterProviderOrder:  csvOr(os.Getenv("UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER"), []string{"novita"}),
UpstreamOpenRouterAllowFallbacks: boolOr(os.Getenv("UPSTREAM_LLM_OPENROUTER_ALLOW_FALLBACKS"), false),
```
Env `PROVIDER_ORDER` vazio → `csvOr` cai no default `["novita"]`; `allow_fallbacks` default `false` → o director injeta `provider:{order:["novita"],allow_fallbacks:false}` em todo request. Gemini (Google-only) → 404.

Prova do fix (direto OpenRouter, key iFix):
- gemini + `order:["novita"],allow_fallbacks:true` → **OK, provider=Google** (fallback).
- deepseek + mesmo → **OK, provider=Novita** (inalterado).

### Aplicado
1. Stack 38 (ai-gateway-prod, Portainer ep6): add env `UPSTREAM_LLM_OPENROUTER_ALLOW_FALLBACKS=true` + linha no compose `- UPSTREAM_LLM_OPENROUTER_ALLOW_FALLBACKS=${...}` (PUT 200, durável). Gateway recriou (blip ~seg, health 200 develop-1f6e6a5).
2. Alias `google/gemini-2.5-flash-lite` → openrouter-chat → target `google/gemini-2.5-flash-lite` (nome=literal do app).

### Prova final (via gateway prod)
- `/v1/chat/completions model=google/gemini-2.5-flash-lite` + tools → **200, finish_reason=tool_calls, get_weather({"city":"Sao Paulo"})** (tool-calling OK, Pitfall 4).
- billing_events: upstream=openrouter-chat, cost_external_brl=**0.000020** (17in/6out × preço gemini 22-01 × fx 5.10 ✓).
- qwen intacto (local-llm + openrouter-chat→deepseek:nitro); Phase 20 não regrediu (health ok).

### Efeito colateral (aceito)
`allow_fallbacks=true` agora vale p/ TODO openrouter-chat: se Novita cair, deepseek/llama caem pra outro provider (antes falhava). = mais resiliência, risco de custo marginal.

### Rollback
Remover alias (`model-alias delete -alias google/gemini-2.5-flash-lite -upstream openrouter-chat`) + reverter env no stack 38 (backup em scratchpad/stack38_backup.json → `allow_fallbacks` volta false por default do código).
