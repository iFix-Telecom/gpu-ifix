# Phase 22 — CONTEXT (funnel de custo pelo gateway + ROI do pod)

Criado 2026-07-19. Origem: análise de custo LLM jul/2026 (pedido do Pedro — garantir execução via phase).

## Problema (FATO, medido 2026-07-19)
- OpenAI jul = **$53,15**: projeto `ai-gateway` **$38,28** + `Vps-Claude-Pedro` **$14,61** + `N8N` **$0,27**.
  - Vps-Claude ($14,6) = `proj_Jpvi` / key_GD4Nw = dev/agente do Pedro (gpt-5.5 + gpt-5-nano + o3-mini + Codex). Discricionário.
  - N8N ($0,27) = `proj_1nM0` / key_7PvTNf = gpt-4o-mini. Ninharia.
  - ai-gateway **$38 tem ZERO chat-completions** no export → é ÁUDIO (whisper/STT) ou embeddings. **Falta export de áudio OpenAI p/ cravar.**
- Google **Gemini API R$238,59** (CSV faturamento, −18% vs jun).
- Gateway `billing_events` jul = só ~**R$13** externo (openrouter R$11,2 + gemini-stt R$1,33 + openai-whisper R$0,60) → confirma que o grosso passa FORA do gateway.

## Root cause (FATO — env das stacks Portainer)
`converseai-v4-dev` (stack **15**, endpoint 3) bypassa o gateway com 3 keys DIRETAS:
- `GOOGLE_AI_API_KEY=AIzaSyCSvh…` (Gemini direto = os R$238)
- `OPENAI_API_KEY=sk-proj-dLD68…` (OpenAI direto = os $38, provável STT áudio)
- `OPENROUTER_API_KEY=sk-or-v1-…` (OpenRouter direto)

`backend-chat-ifix` (stack 9) JÁ usa o gateway (`OPENROUTER_BASE_URL=…converse-ai.app`) — modelo certo a replicar.

## Objetivo do Pedro (LOCKED)
Funnelar TODO o gasto pelo gateway — **mesmo em fallback google/openai direto** — para `billing_events` capturar 100% e permitir decidir se **alugar pod compensa** (custo pod vs external). Sem isso, impossível medir.

## Decisões / sequência (LOCKED)
1. **PRICE-01 é GATE.** `cost_external_brl` subvalorizado (gemini-stt 754min→R$1,33 irreal). Corrigir `ai_gateway.prices` com preços reais BRL por provider/modelo/unit (áudio $/min + token in/out) ANTES de qualquer conclusão de ROI. `prices` tem NOTIFY hot-reload (UPDATE/INSERT no DB, sem deploy). Ver [[stt-cpu-and-billing-columns]] (3 colunas de custo: cost_external_brl=real externo, cost_local_brl=0 sempre, cost_local_phantom_brl=referência openrouter).
2. **CV-01 (STT primeiro).** Ativar Phase 113: setar `STT_GATEWAY_KEY`=ifix_sk_jj7h… no stack 15 → UAT → depois classifier/format-hint/ai-match, uma por vez. Código já mergeado+inerte. STT ataca os $38 + valida o funnel end-to-end com baixo risco.
3. **CV-02 (maior corte R$).** Agente principal + `GOOGLE_AI_API_KEY` → gateway. PRÉ: criar upstream de **fallback gemini de CHAT** no gateway (hoje só gemini-stt). Ataca o R$238.
4. **CV-03.** Followup worker TS (spec `docs/route-secondary-llms-through-gateway.md`, não Phase 113).
5. **MEASURE-01.** Query/painel ROI mensal: `sum(cost_external_brl)` por upstream/tenant vs custo do pod (dph × horas up). Opcional expor no dashboard economia (Phase 15).

## Gotchas
- Rollout Phase 113 é opt-in por env, gradual, com rollback (esvaziar key + redeploy). NÃO ligar tudo de uma vez.
- CV-02 exige o upstream gemini-chat existir no gateway antes (senão quebra comportamento).
- Não regredir Phase 21 (STT local) nem Phase 20 (provisioning) ao mexer no gateway.

## Refs
- Memory: [[gateway-cost-funnel-and-pod-roi]], [[stt-cpu-and-billing-columns]], [[coldstart-pod-economics]], [[gateway-prod-deploy-mechanism]].
- CLAUDE.md (raiz `/home/pedro/.claude/CLAUDE.md`): seção API Keys — bloco Phase 113 com as 4 `*_GATEWAY_KEY` + tenants.
- Dados brutos: `/sync/completions_usage_2026-*.{json,csv}` (OpenAI usage) + `/sync/Minha conta de faturamento_Relatórios, 2026-07-01 — 2026-07-31.csv` (Google).
