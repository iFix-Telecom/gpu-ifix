# Phase 22 — RESEARCH (funnel de custo pelo gateway + ROI do pod)

> Pesquisa para planejamento. Regra de Ouro do projeto respeitada: cada claim tem tag
> `[VERIFIED: cmd/arquivo:linha]`, `[CITED: doc]` ou `[HIPÓTESE]`. Bloco de FATOS separado
> de HIPÓTESES. Onde faltou dado: "NÃO SEI / precisa verificar" + comando que resolve.
> Acesso usado: SÓ LEITURA (ssh worker-vm, psql SELECT via container, gatewayctl list/report, git log/grep).

---

## <user_constraints>  (verbatim de 22-CONTEXT.md — LOCKED)

**Objetivo do Pedro (LOCKED):** Funnelar TODO o gasto pelo gateway — mesmo em fallback
google/openai direto — para `billing_events` capturar 100% e permitir decidir se **alugar
pod compensa** (custo pod vs external). Sem isso, impossível medir.

**Decisões / sequência (LOCKED):**
1. **PRICE-01 é GATE.** `cost_external_brl` subvalorizado (gemini-stt 754min→R$1,33 irreal).
   Corrigir `ai_gateway.prices` com preços reais BRL por provider/modelo/unit (áudio $/min +
   token in/out) ANTES de qualquer conclusão de ROI. `prices` tem NOTIFY hot-reload
   (UPDATE/INSERT no DB, sem deploy). 3 colunas de custo: cost_external_brl=real externo,
   cost_local_brl=0 sempre, cost_local_phantom_brl=referência openrouter.
2. **CV-01 (STT primeiro).** Ativar Phase 113: setar `STT_GATEWAY_KEY`=ifix_sk_jj7h… no stack 15
   → UAT → depois classifier/format-hint/ai-match, uma por vez. Código já mergeado+inerte.
   STT ataca os $38 + valida o funnel end-to-end com baixo risco.
3. **CV-02 (maior corte R$).** Agente principal + `GOOGLE_AI_API_KEY` → gateway. PRÉ: criar
   upstream de **fallback gemini de CHAT** no gateway (hoje só gemini-stt). Ataca o R$238.
4. **CV-03.** Followup worker TS (spec `docs/route-secondary-llms-through-gateway.md`, não Phase 113).
5. **MEASURE-01.** Query/painel ROI mensal: `sum(cost_external_brl)` por upstream/tenant vs
   custo do pod (dph × horas up). Opcional expor no dashboard economia (Phase 15).

**Gotchas (LOCKED):**
- Rollout Phase 113 é opt-in por env, gradual, com rollback (esvaziar key + redeploy). NÃO ligar tudo de uma vez.
- CV-02 exige o upstream gemini-chat existir no gateway antes (senão quebra comportamento).
- Não regredir Phase 21 (STT local) nem Phase 20 (provisioning) ao mexer no gateway.

</user_constraints>

---

## <phase_requirements>

| ID | Descrição | Suporte da pesquisa (como implementar) |
|----|-----------|-----------------------------------------|
| **PRICE-01** | Corrigir preços reais em `ai_gateway.prices` (áudio $/min + token in/out) por provider/modelo/unit; hot-reload NOTIFY | `gatewayctl prices set -model -provider -unit -usd [-notes]` + `set-fx -usd-brl`. NÃO precisa deploy (trigger `prices_changed` → loader LISTEN). Chaves de lookup ditadas por `providerForUpstream`+`sttBillingModel`. Ver §PRICE-01. |
| **CV-01** | Ativar Phase 113 rollout gradual (STT→classifier→format-hint→ai-match), 1 key por vez + UAT + rollback | **BLOQUEADO:** código Phase 113 NÃO está em `origin/develop` (dangling). Setar a env sozinho é no-op. Re-landar o código 113 antes. Validação: `gatewayctl usage report --tenant <slug>` passa de 0 rows → N rows. Ver §CV-01. |
| **CV-02** | Agente + Gemini pelo gateway; PRÉ criar upstream gemini-CHAT | Upstream = row DB (`upstreams`) + env `url_env` (loader lê `os.Getenv` dinâmico). SEM director gemini-chat (só STT). Alternativa proven: alias `gemini-flash-lite`→`openrouter-chat`→`google/gemini-2.5-flash-lite`. Reconciliar: R$238 Gemini = classifier/format-hint (=CV-01) + media pipeline nativo (não-OpenAI-compat, gateway não proxya). Ver §CV-02. |
| **CV-03** | Followup worker TS → gateway | Spec existe: `converseai-v4/docs/route-secondary-llms-through-gateway.md`. Alvo `apps/worker/src/shared/followup-llm.ts::getFollowupClient`. Branch gateway quando `FOLLOWUP_GATEWAY_KEY` set, `model=qwen` (alias já serve deepseek). Ver §CV-03. |
| **MEASURE-01** | Query/painel ROI: `sum(cost_external_brl)` por upstream/tenant vs custo pod | `GET /admin/economy` (economy.go, Phase 15/OBS-09) JÁ computa phantom_brl, vast_brl, roi_multiplier, custo_openrouter_brl + série diária. Pod $ = `primary_lifecycles.total_cost_brl`. Reuso, não construir do zero. Ver §MEASURE-01. |

</phase_requirements>

---

## Summary

**Recomendação primária:** PRICE-01 e MEASURE-01 são baratos e de baixo risco (CLI hot-reload +
endpoint `/admin/economy` já existente) — fazer primeiro para destravar a medição. **CV-01 tem
um bloqueio crítico não previsto no CONTEXT: o código da Phase 113 NÃO está em `origin/develop`
(commits dangling, `git branch --contains`=vazio) — setar `STT_GATEWAY_KEY` no stack 15 é no-op
até o código ser re-landado.** CV-02 como redação literal ("criar upstream gemini-CHAT") esbarra
em falta de director gemini-chat no gateway; o caminho proven (spec 113) é alias sobre
`openrouter-chat`. A maior parte do R$238 Gemini é classifier/format-hint (= escopo CV-01), não
"agente principal".

---

## PRICE-01 — tabela `ai_gateway.prices` e cálculo de `cost_external_brl`

### FATOS

**DDL da tabela** `[VERIFIED: psql \d ai_gateway.prices + gateway/db/migrations/0012_create_prices_and_fx.sql:6-20]`:
```
prices(id uuid, model text, provider text, unit text, unit_cost_usd numeric(12,8),
       valid_from timestamptz DEFAULT now(), valid_to timestamptz, notes text, created_at)
UNIQUE (model, provider, unit, valid_from)
CHECK unit IN ('input_token','output_token','audio_second','embed_request')
idx_prices_active btree(model,provider,unit) WHERE valid_to IS NULL
```
Não existe `cost_external_brl` NEM `unit` de "$/min" na tabela `prices`. Áudio é modelado em
`audio_second` (USD/segundo); token in/out em `input_token`/`output_token` (USD/token); embed em
`embed_request` ou `input_token`. `[VERIFIED: 0012:10]`

**`cost_external_brl` é COLUNA de `billing_events` e `usage_counters`, calculada por request** — NÃO
fica em `prices`. `[VERIFIED: gateway/db/migrations/0010_create_billing_events.sql:17-19; 0011_evolve_usage_counters.sql:8-9]`
As 3 colunas: `cost_local_brl` (=0 sempre, GPU custo fixo), `cost_local_phantom_brl` (referência
openrouter), `cost_external_brl` (custo real externo).

**Onde o gateway LÊ prices e CALCULA custo por request:**
- `gateway/internal/proxy/interceptor_usage.go:258-264` — no `FinalizeRequest`:
  `isLocal := strings.HasPrefix(upstream,"local-")`; se não-local calcula `costExternal` via
  `priceTokens(...)`; `costPhantom` sempre com provider `openrouter-fireworks`.
- `priceTokens` (`interceptor_usage.go:298-317`) soma `ComputeCostBRL` para cada dimensão
  (input_token/output_token/audio_second/embed_request).
- `ComputeCostBRL` (`gateway/internal/billing/cost.go:20-56`): `units × unit_cost_usd × fx(USD/BRL)`.
  Se preço faltar → retorna 0 + WARN + incrementa Prometheus `gateway_prices_missing_total`. `[VERIFIED: cost.go:34-45]`

**Chave de lookup (model, provider, unit) — como é derivada:**
- `provider` vem de `providerForUpstream(upstream)` `[VERIFIED: interceptor_usage.go:323-334]`:
  `openrouter-chat→openrouter-fireworks`, `openai-embed/openai-whisper→openai`, **default → o próprio
  nome do upstream** (logo `gemini-stt→provider "gemini-stt"`, `groq-whisper→"groq-whisper"`).
- Para STT sem campo `model` na resposta ({"text":...}), `model` vem de `sttBillingModel(upstream)`
  `[VERIFIED: interceptor_usage.go:340-351]`: `gemini-stt→"gemini-2.5-flash-lite"`,
  `openai-whisper→"whisper-1"`, `groq-whisper→"whisper-large-v3"`.
- Áudio ($/min) é convertido: seconds via response `duration` OU fallback derivado do request
  (`RequestAudioSecondsMiddleware`), depois `× unit_cost_usd(audio_second)`. `[VERIFIED: interceptor_usage.go:378-399]`

**Hot-reload NOTIFY** `[VERIFIED: 0012:37-57]`: trigger `prices_insert_delete_notify` (AFTER INSERT/DELETE)
e `prices_update_notify` (AFTER UPDATE quando `unit_cost_usd` ou `valid_to` muda) → `pg_notify('prices_changed', id)`.
Lado código: `gateway/internal/billing/prices_loader.go` + `gateway/internal/billing/listen.go` (LISTEN
`prices_changed`, snapshot atômico, last-good-on-error). FX análogo (`fx_changed`).

**Preços vigentes hoje** `[VERIFIED: gatewayctl prices list, 2026-07-19]` (linhas active relevantes):
| model | provider | unit | USD | nota |
|-------|----------|------|-----|------|
| gemini-2.5-flash-lite | gemini-stt | audio_second | 0.00000960 | "0.30usd/1M tok × 32 tok/s" (aproximação) |
| whisper-1 | openai | audio_second | 0.00010000 | Phase 4 seed ($0.006/min) |
| qwen3.5-27b | openrouter-fireworks | input_token | 0.00000020 | Phase 4 seed |
| qwen3.5-27b | openrouter-fireworks | output_token | 0.00000156 | Phase 4 seed |
| text-embedding-3-small | openai | input_token | 0.00000002 | Phase 4 seed |
| deepseek/deepseek-v4-flash-20260423 | openrouter-fireworks | input/output_token | 0.00000010 / 0.00000020 | phantom |

FX ativo: **USD/BRL = 5.082509** `[VERIFIED: psql SELECT fx_rates WHERE valid_to IS NULL]`.

**Gasto real do gateway em julho (por upstream)** `[VERIFIED: psql billing_events WHERE ts>=2026-07-01]`:
| upstream | route | reqs | cost_external_brl | phantom_brl | audio_s | tokens_in | tokens_out |
|----------|-------|------|-------------------|-------------|---------|-----------|-----------|
| openrouter-chat | chat | 20632 | 11.20 | 11.20 | 0 | 25.2M | 8.7M |
| gemini-stt | stt | 20881 | 1.33 | 1.33 | 45258 (754min) | 0 | 0 |
| openai-whisper | stt | 492 | 0.60 | 0.011 | 1938 | 0 | 0 |
| local-embed | embed | 200 | 0.00 | 0.00 | 0 | 421k | 0 |
| emergency_pod_llm | chat | 6401 | 0.00 | 3.59 | 0 | 5.8M | 998k | (pod local — external 0, phantom conta a economia) |
| emergency_pod_stt | stt | 27 | 0.00 | 0.00 | 22 | 0 | 0 | (Phase 21 STT in-house — cost_external=0) |

→ confirma o CONTEXT: total externo pelo gateway ~R$13/mês. O grosso ($38 OpenAI + R$238 Gemini) passa **FORA**.

### HIPÓTESES

- `[HIPÓTESE]` O preço gemini-stt (0.0000096/s) está subvalorizado *relativo ao custo real que a
  converseai paga direto*. Resolveria: comparar a fatura Google (`/sync/Minha conta de
  faturamento_…csv`) contra `audio_seconds × 0.0000096 × 5.08`. Gemini 2.5 Flash-Lite audio-input É
  genuinamente barato — o "irreal" pode ser o *volume* que não passa pelo gateway, não a taxa. **NÃO
  SEI qual dos dois** sem cruzar a fatura Google discriminada por serviço.
- `[HIPÓTESE]` OpenAI $38 = STT (whisper) direto da converseai. CONTEXT diz "provável STT áudio" e
  "falta export de áudio OpenAI p/ cravar". Resolveria: export de uso de áudio do dashboard OpenAI.

### Passo-a-passo de execução (PRICE-01)

Rodar no container do gateway (worker-vm), **hot-reload, sem deploy**:
```bash
GW=$(docker ps -q -f name=ai-gateway-prod_gateway|head -1)
# Ex.: corrigir taxa de câmbio
docker exec $GW /gatewayctl prices set-fx -usd-brl 5.45
# Ex.: setar/atualizar preço (cria nova linha active; NÃO há -update, é INSERT)
docker exec $GW /gatewayctl prices set -model gemini-2.5-flash-lite -provider gemini-stt \
  -unit audio_second -usd 0.00000960 -notes "revalidado jul/2026 vs fatura Google"
```
`prices set` faz INSERT (nova `valid_from`); o loader ativa a linha com `valid_to IS NULL` mais recente.
`[VERIFIED: gatewayctl prices --help → set/list/set-fx; cmd/gatewayctl/prices.go]`
**Gotcha de chave:** a linha só é usada se `(model, provider, unit)` casar com o que o interceptor
monta em runtime (ver `providerForUpstream`/`sttBillingModel` acima). Preço para o provider errado
= silenciosamente 0 + `gateway_prices_missing_total`++.

---

## CV-01 — Phase 113 wiring (converseai-v4)

### FATOS

**Localização:** converseai-v4 = `/home/pedro/projetos/pedro/converseai-v4` (repo separado deste). `[VERIFIED: ls -d]`
Deploy = stack Portainer **15** `converseai-v4-dev`, endpoint **3** (socket local vps-ifix-vm .30). `[CITED: CLAUDE.md Portainer + ROADMAP Phase 22 root-cause]`

**Env keys da Phase 113** (aliases pydantic) `[VERIFIED: git show 386494ba (converseai-v4)]`:
- `STT_GATEWAY_KEY`, `AGENT_CLASSIFIER_GATEWAY_KEY`, `AGENT_FORMAT_HINT_GATEWAY_KEY`,
  `AGENT_AI_MATCH_GATEWAY_KEY` (todas default `""` = comportamento direto atual),
  `AGENT_GATEWAY_BASE_URL` (default `https://ai-gateway.converse-ai.app/v1`),
  `AGENT_GATEWAY_TIMEOUT_SECONDS` (default 10.0).
- Mapeamento key→tenant `[CITED: CLAUDE.md + spec doc]`:
  `STT_GATEWAY_KEY=ifix_sk_jj7h…`(converseai-stt), `AGENT_CLASSIFIER_GATEWAY_KEY=ifix_sk_hu3x…`(converseai-classifier),
  `AGENT_FORMAT_HINT_GATEWAY_KEY=ifix_sk_2h7i…`(converseai-format-hint), `AGENT_AI_MATCH_GATEWAY_KEY=ifix_sk_4u3d…`(converseai-ai-match).

**Validação de ativação:** `gatewayctl usage report --tenant <slug> --from YYYY-MM-DD --to YYYY-MM-DD
--format table`. `[VERIFIED: gatewayctl usage --help]`
- Hoje `converseai-stt` retorna **0 rows** (não roteado ainda). `[VERIFIED: usage report --tenant converseai-stt = vazio]`
- `converseai` (RAG HyDE, já ativo) retorna rows reais. `[VERIFIED: usage report --tenant converseai = 9+ dias com reqs]`
  → prova de ativação = tenant sai de 0 → N rows + billing_events com `upstream != unknown`.

**Log `gateway_enabled=True`:** a redação exata do CONTEXT/CLAUDE.md. O código 113-01 loga
`logger.info("llm.create", provider="ifix-gateway", model=...)` no branch gateway. `[CITED: docs/route-secondary-llms-through-gateway.md §Implementação]`
`[HIPÓTESE]` O literal `gateway_enabled=True` pode ser de outro ponto (structured.py/transcription).
Resolveria: `git show 680accfa` (feat 113-03 STT) e `b205bd6d` (feat 113-02 LLM) — mas ver bloqueio abaixo.

### 🔴 BLOQUEIO CRÍTICO (não previsto no CONTEXT)

**O código da Phase 113 NÃO está em `origin/develop` (= HEAD atual do checkout).** `[VERIFIED:]`
```
git rev-parse HEAD origin/develop  → ambos c6fd7b6c  (local em sync com origin)
git merge-base --is-ancestor 386494ba HEAD  → NO-not-in-HEAD
git branch -a --contains 386494ba           → (vazio — nenhum branch contém)
grep agent_classifier_gateway_key agents/src/config.py  → 0 ocorrências
grep gateway_key agents/src/llm/provider.py             → 0 ocorrências
```
Os commits 113 (`386494ba`, `6a00de74`, `b205bd6d`, `680accfa`, `5ce38f57`…) existem como objetos
**dangling** (só via `git log --all`/reflog), mas foram deixados fora de `develop` (rebase/force-update).
→ **Setar `STT_GATEWAY_KEY` no stack 15 NÃO faz nada**: a env é lida mas nenhum código a consome, e
a imagem `:develop` (buildada de develop pelo GHA) não tem o branch gateway.

**Implicação para o plano:** CV-01 NÃO é "só setar 1 env". Precisa, ANTES:
1. Confirmar de qual commit a imagem rodando no stack 15 foi buildada (pode ser anterior ao drop).
   Resolve: `docker inspect` da imagem converseai no vps-ifix-vm + label de git-rev, OU
   `ssh vps-ifix-vm 'docker exec <api-container> python -c "..."'` para checar se `config.Settings`
   tem `stt_gateway_key`.
2. Se ausente: re-landar o código 113 em develop (cherry-pick dos dangling `6a00de74 b205bd6d
   0d038b3b 680accfa` + config) → CI builda → redeploy stack 15 → SÓ ENTÃO setar as keys.

### Passo-a-passo (CV-01, assumindo código re-landado)

Rollout 1 key por vez no stack 15 (Portainer UI env → redeploy), começando por STT:
```
STT_GATEWAY_KEY=ifix_sk_jj7huyqn7ilqatkstxgikt6i2ujohqac   # 1º (ataca STT/$38)
```
UAT por etapa: `gatewayctl usage report --tenant converseai-stt --from <hoje> --to <hoje>` > 0 rows
+ log do agente `provider="ifix-gateway"`. Rollback = esvaziar a key + redeploy. Depois classifier →
format-hint → ai-match, um de cada vez.

---

## CV-02 — upstream gemini-CHAT no gateway

### FATOS

**Como upstreams são definidos:** tabela `ai_gateway.upstreams` (linhas seedadas por migration; NÃO
há `gatewayctl upstreams create` — só `list|update|enable|disable`). `[VERIFIED: gatewayctl upstreams → "list|update|enable|disable"]`
Colunas relevantes: `name, role, tier, tier_priority, url_env, auth_bearer_env, enabled, circuit_config`.
`[VERIFIED: 0029_readd_whisper_add_gemini_groq.sql:50-64]`

**Upstreams atuais** `[VERIFIED: gatewayctl upstreams list, 2026-07-19]`:
| name | role | tier | enabled | url_env | probe |
|------|------|------|---------|---------|-------|
| local-llm | llm | 0 | true | UPSTREAM_LLM_URL | failed (pod down agora) |
| openrouter-chat | llm | 1 | true | UPSTREAM_LLM_OPENROUTER_URL | config |
| local-stt | stt | 0 | true | UPSTREAM_STT_URL | failed |
| gemini-stt | stt | 1 | true | UPSTREAM_STT_FALLBACK_1_URL | config |
| groq-whisper | stt | 1 | false | UPSTREAM_STT_FALLBACK_2_URL | failed |
| openai-whisper | stt | 1 | true | UPSTREAM_STT_OPENAI_URL | ok |
| local-embed / openai-embed / local-tts / kokoro-tts | … | | | | |

→ **Não existe upstream role=llm apontando p/ Gemini.** Confirmado o CONTEXT.

**Resolução de URL do upstream é dinâmica:** `url := os.Getenv(r.UrlEnv)` `[VERIFIED: gateway/internal/upstreams/loader.go:132]`;
se a env não estiver setada a **row é SKIPPED** (graceful, não fail-fast) `[VERIFIED: loader.go:104]`.
→ um novo upstream = **row DB (migration) + nova env var no stack**. Não precisa alterar o struct
`Config` para a URL (o loader lê qualquer `url_env`).

**Director de chat é genérico OpenAI-compat:** `BuildDirector(upstream *url.URL)` reescreve host+path
com `path.Join(upstream.Path, inboundPath)`. `[VERIFIED: gateway/internal/proxy/director.go:39-60]`
**NÃO existe director gemini-CHAT.** O único código Gemini-específico é
`gateway/internal/proxy/gemini_stt_director.go` (STT: reescreve multipart→JSON Gemini, ModifyResponse).
`[VERIFIED: grep gemini em proxy/ → só gemini_stt_director.go]`

**Como o alias mapeia p/ upstream:** `gatewayctl model-alias set -alias <a> -upstream <u> -target <t>`.
`[VERIFIED: model-alias list mostra alias→upstream_name→target]`. Ex atual: `qwen`→`local-llm`(tier0)+`openrouter-chat`(tier1).

**Comportamento a preservar (agente converseai):** `[VERIFIED: converseai-v4/agents/src/config.py + spec doc]`
- `AGENT_PRIMARY_LLM` (agente principal) = deepseek (OpenRouter) — o spec diz "fora do escopo, continua".
- classifier = `AGENT_CLASSIFIER_LLM_MODEL` default `google/gemini-2.5-flash-lite`.
- format-hint = `AGENT_FORMAT_HINT_LLM` default `google/gemini-2.5-flash-lite`.
- media pipeline = `media_llm_primary` default `gemini-2.5-flash` via `client.aio.models.generate_content`
  (SDK google-genai **nativo, multimodal, NÃO OpenAI-compat**). `[VERIFIED: agents/src/media/gemini_analyzer.py:159-204]`

### Reconciliação importante (o CONTEXT conflaciona)

**O R$238 Gemini NÃO é o "agente principal".** Fontes reais de gasto Gemini na converseai:
1. **classifier + format-hint** (`google/gemini-2.5-flash-lite`) → **isto é escopo CV-01** (keys
   `AGENT_CLASSIFIER_GATEWAY_KEY`/`AGENT_FORMAT_HINT_GATEWAY_KEY`, Phase 113). Roteável como CHAT.
2. **media pipeline** (`gemini_analyzer.py`, gemini-2.5-flash, generate_content nativo) → **o gateway
   NÃO consegue proxyar** (só OpenAI-compat; sem upstream de visão — CLAUDE.md: "vision = gateway sem
   upstream de visão"). Funnel deste = fora de alcance sem novo director multimodal.

→ **CV-02 "criar upstream gemini-CHAT" atende (1) se seguir o caminho do spec**, não um upstream nativo.

### Dois caminhos para CV-02 (o planner escolhe; Pedro travou "criar upstream gemini")

**Caminho A — upstream gemini-chat NATIVO (redação literal do CONTEXT):**
- migration nova: INSERT row `upstreams(name='gemini-chat', role='llm', tier=1, tier_priority=?,
  url_env='UPSTREAM_LLM_FALLBACK_1_URL', auth_bearer_env='UPSTREAM_LLM_FALLBACK_1_AUTH_BEARER')` +
  swap constraint se colidir `(role,tier,tier_priority)` (padrão de 0029).
- env no stack: `UPSTREAM_LLM_FALLBACK_1_URL` + `_AUTH_BEARER` (a GOOGLE_AI key).
- `[HIPÓTESE — ALTO RISCO]` O endpoint OpenAI-compat do Gemini é `/v1beta/openai/chat/completions`;
  o director genérico faz `path.Join("/v1beta/openai", "/v1/chat/completions")` =
  `/v1beta/openai/v1/chat/completions` → **path errado, quebra**. Precisaria de um director/rewrite
  específico (como o gemini-stt tem). Resolve: testar `curl` direto no endpoint OpenAI-compat do
  Gemini com o path que o director produz ANTES de assumir que row+env basta.

**Caminho B — alias sobre openrouter-chat (proven, o que o spec 113 fez p/ os secundários):** `[CITED: docs/route-secondary-llms-through-gateway.md §Pré-requisito]`
```bash
docker exec $GW /gatewayctl model-alias set \
  -alias gemini-flash-lite -upstream openrouter-chat -target google/gemini-2.5-flash-lite
```
Sem novo upstream, sem director. Custo: OpenRouter serve o Gemini (markup) e o provider muda de
Google-direto p/ OpenRouter. `⚠ [CITED: spec]` confirmar que o gateway repassa `tool_choice`/
structured-output pro OpenRouter (classifier/format-hint usam `with_structured_output` — bug Phase 70.2)
ANTES de ligar em prod: testar `/v1/chat/completions` com `tools`+`tool_choice` via a tenant.

---

## CV-03 — followup worker TS

### FATOS

**Spec existe:** `/home/pedro/projetos/pedro/converseai-v4/docs/route-secondary-llms-through-gateway.md`.
`[VERIFIED: sed do arquivo]`. Cobre classifier/format-hint (Python) **e** followup (TS).

**O que especifica p/ followup** `[CITED: spec §Worker TS + §Implementação item 3]`:
- Alvo: `apps/worker/src/shared/followup-llm.ts::getFollowupClient` (~linha 85), que monta
  `new OpenAI({apiKey, baseURL})` escolhendo provider por prefixo (openrouter/gemini/openai).
- Mudança: adicionar branch `gateway` — quando `process.env.FOLLOWUP_GATEWAY_KEY` setado, apontar
  `baseURL` pro gateway + `model=qwen` (alias `qwen` já resolve deepseek-v4-flash; sem alias novo p/ followup).
- tenant: `converseai-followup` (`ifix_sk_gfx6op…`).
- É worker TS (`apps/worker`), codebase separado dos agents Python — **não é Phase 113** (aquela é só Python).

`[HIPÓTESE]` A imagem worker roda do mesmo `origin/develop`; como o followup NÃO depende dos commits
113 dangling (é mudança nova neste arquivo TS), CV-03 é implementável do zero seguindo o spec. Resolve:
`grep -n "FOLLOWUP_GATEWAY_KEY\|getFollowupClient" apps/worker/src/shared/followup-llm.ts` no HEAD.

---

## MEASURE-01 — dados de ROI

### FATOS

**Schema `billing_events`** `[VERIFIED: 0010_create_billing_events.sql:5-24]` (particionada por RANGE(ts)):
`request_id, ts, tenant_id, api_key_id, route, upstream, model, tokens_in, tokens_out, audio_seconds,
embeds_count, cost_local_brl, cost_local_phantom_brl, cost_external_brl, currency, source`.
Índice `idx_billing_events_tenant_ts (tenant_id, ts DESC)`.

**Query ROI por upstream/tenant/mês** (validada ao vivo):
```sql
SELECT b.upstream, t.slug,
       count(*)                          AS reqs,
       round(sum(b.cost_external_brl),4) AS externo_brl,
       round(sum(b.cost_local_phantom_brl),4) AS phantom_brl
FROM ai_gateway.billing_events b
JOIN ai_gateway.tenants t ON t.id = b.tenant_id
WHERE b.ts >= date_trunc('month', now())
GROUP BY b.upstream, t.slug
ORDER BY externo_brl DESC NULLS LAST;
```
`[VERIFIED: variação sem JOIN rodada — retornou breakdown de julho por upstream]`.
Mapa slug↔id: 18 tenants `[VERIFIED: psql tenants JOIN api_keys]` (converseai, converseai-stt,
converseai-classifier, converseai-format-hint, converseai-ai-match, converseai-followup, chat-ifix,
telefonia, cobrancas, campanhas, voice-api, ia-kanban, transcricao-voip, analise-transcr-voip,
converseai-finan-app, uat10-test, hermes, claude-wpp).

**Custo do pod (Vast):** `ai_gateway.primary_lifecycles.total_cost_brl` (accrual `accepted_dph ×
horas up`), colunas `accepted_dph numeric(6,4)`, `total_cost_brl numeric(10,4)`, timestamps
`started_at`/`ended_at` (NÃO `created_at`). `[VERIFIED: 0023_primary_lifecycles.sql:27-28 + psql information_schema colunas]`
→ NÃO precisa chamar a Vast API para custo agregado (já persistido pelo reconciler). A Vast API
(dph por oferta) só entra se quiser custo *projetado* de always-on.

**Painel Phase 15 já existe — REUSO direto** `[VERIFIED: gateway/internal/admin/economy.go:1-106]`:
`GET /admin/economy` (X-Admin-Key) computa server-side:
- `phantom_brl` (SUM cost_local_phantom_brl gateway-wide),
- `vast_brl` (de primary_lifecycles),
- `economia_liquida_brl = phantom − vast`,
- `roi_multiplier = phantom / vast` (null se vast=0),
- `custo_openrouter_brl = SUM(cost_external_brl)` (gasto externo real),
- + série diária (`Series []EconomyDayRow`).
Dashboard Phase 15 (OBS-09) consome isso no painel "Economia". `[CITED: REQUIREMENTS OBS-09]`

### Recomendação MEASURE-01 (ponytail)

MEASURE-01 ≈ **já implementado** pelo `/admin/economy` + painel Economia. O gap real é que hoje o
número está **errado** porque (a) preços subvalorizados (PRICE-01) e (b) 100% do gasto não passa pelo
gateway (CV-01/02/03). Ou seja: MEASURE-01 destrava sozinho quando PRICE-01 + o funnel estiverem
prontos. Entrega mínima = uma query SQL mensal (acima) OU um recorte adicional no endpoint por
upstream/tenant se o painel atual só mostrar o agregado.

---

## Environment Availability

| Recurso | Disponível? | Como |
|---------|-------------|------|
| `ssh worker-vm` | ✅ | key authorized; containers via `docker ps` |
| gateway container | ✅ | `docker ps -q -f name=ai-gateway-prod_gateway` (imagem scratch — SEM shell; só `/gatewayctl`) |
| `gatewayctl` (read) | ✅ | `docker exec $GW /gatewayctl {prices list, upstreams list, model-alias list, usage report, --help}` |
| psql no `bd_ai_gateway` | ✅ (indireto) | DB é DigitalOcean managed (`db-grupoifix-do-user-7520351-0…:25060/bd_ai_gateway?sslmode=require`); host worker-vm/gateway SEM psql; usar `docker exec <postgres_postgres container> psql "$DSN" -c '…'` (esse container tem cliente psql e egressa pelo IP whitelisted) |
| DSN | ✅ | `docker inspect $GW` env `AI_GATEWAY_PG_DSN` (root/doadmin — SÓ SELECT nesta pesquisa) |
| converseai-v4 repo | ✅ | `/home/pedro/projetos/pedro/converseai-v4` (develop = origin/develop c6fd7b6c) |
| Portainer stack 15 env | ⚠️ NÃO checado | precisa Portainer API/UI (token no CLAUDE.md) p/ ver env atual do converseai-v4-dev + imagem digest |
| imagem stack 15 (tem código 113?) | ⚠️ NÃO checado | `ssh vps-ifix-vm 'docker inspect <converseai-api>'` + checar `config.Settings` — **GATE do CV-01** |
| Vast API | ⚠️ não necessário | custo pod já em `primary_lifecycles.total_cost_brl` |

---

## Common Pitfalls (rollout)

1. **CV-01 antes de confirmar o código 113 na imagem = no-op silencioso.** Env lida, nenhum consumidor.
   Confirmar `stt_gateway_key` presente em `config.Settings` da imagem rodando ANTES de tocar env.
2. **Não ligar tudo de uma vez.** Uma `*_GATEWAY_KEY` por vez + UAT + rollback (esvaziar key + redeploy). LOCKED.
3. **CV-02 caminho A (upstream nativo) pode quebrar por path** (`/v1beta/openai/v1/…`). Testar o path
   real que o director genérico produz contra o endpoint Gemini ANTES; senão precisa director dedicado.
4. **tool_choice/structured-output pelo gateway** (classifier/format-hint) — testar `tools`+`tool_choice`
   na tenant antes de prod (bug histórico Phase 70.2). Se quebrar, mapear pra modelo com tool-calling robusto.
5. **Preço p/ provider errado = R$0 + drift silencioso.** Conferir `providerForUpstream`/`sttBillingModel`
   e casar a chave `(model,provider,unit)` exata; monitorar `gateway_prices_missing_total`.
6. **Não regredir Phase 21 (STT in-house):** `emergency_pod_stt` já grava `cost_external=0` — ao mexer
   em aliases/upstreams STT, manter `local-stt` tier0 primário e a cascade tier1 intacta.
7. **Não regredir Phase 20 (provisioning):** mexer em `upstreams`/`model-alias` NÃO deve tocar
   `pod_config`/reconciler/`primary_lifecycles`. Migrations de upstream são aditivas (padrão 0029, ON CONFLICT DO NOTHING).
8. **Media pipeline Gemini (visão) não é funnelável** pelo gateway atual — não prometer no escopo.

---

## Validation Architecture (Nyquist enabled — 1 prova empírica por requirement)

| Requirement | Comando que PROVA |
|-------------|-------------------|
| **PRICE-01** | `gatewayctl prices list` mostra a linha com USD realista + `psql SELECT ... FROM prices WHERE valid_to IS NULL` = 1 linha active por (model,provider,unit); depois de 1 request real, `psql SELECT cost_external_brl FROM billing_events WHERE upstream='<u>' ORDER BY ts DESC LIMIT 1` > 0 e coerente com `audio_s × usd × fx`. `gateway_prices_missing_total` NÃO incrementa. |
| **CV-01** | Antes: `gatewayctl usage report --tenant converseai-stt` = 0 rows. Depois de setar a key + UAT: mesma query > 0 rows E `psql SELECT DISTINCT upstream FROM billing_events WHERE tenant_id='11bfafdf-…'` mostra `gemini-stt`/`openai-whisper` (não `unknown`). Log do agente: `provider="ifix-gateway"`. |
| **CV-02** | `gatewayctl model-alias list` mostra o alias novo; `curl -H "Authorization: Bearer <converseai key>" .../v1/chat/completions -d '{"model":"gemini-flash-lite","tools":[…],"tool_choice":…}'` → 200 com `tool_calls`; `psql` billing_events registra o request com upstream esperado. |
| **CV-03** | `grep FOLLOWUP_GATEWAY_KEY apps/worker/src/shared/followup-llm.ts` (branch existe); após deploy, `gatewayctl usage report --tenant converseai-followup` > 0 rows. |
| **MEASURE-01** | `curl -H "X-Admin-Key: …" .../admin/economy?from=…&to=…` → JSON com `phantom_brl`, `vast_brl`, `roi_multiplier`, `custo_openrouter_brl` não-nulos; cruzar com a query SQL manual (§MEASURE-01). |

---

## Open Questions

1. **[GATE CV-01] O código Phase 113 está na imagem rodando do stack 15?** `origin/develop` NÃO tem
   (dangling). Resolve: `ssh vps-ifix-vm 'docker inspect <converseai-api-container> --format "{{.Config.Image}}"'`
   + `ssh vps-ifix-vm 'docker exec <container> python -c "from src.config import get_settings; s=get_settings(); print(hasattr(s,\"stt_gateway_key\"))"'`. Se False → re-landar 113 (cherry-pick `6a00de74 b205bd6d 0d038b3b 680accfa` + config) antes de qualquer env.
2. **[PRICE-01] gemini-stt 0.0000096/s é realmente subvalorizado, ou o problema é só volume off-gateway?**
   Resolve: cruzar `/sync/Minha conta de faturamento_…csv` (Google, discriminado) contra
   `SUM(audio_seconds)×0.0000096×fx` do gateway. Idem OpenAI $38: export de uso de áudio do dashboard OpenAI.
3. **[CV-02] O director genérico funciona contra o endpoint OpenAI-compat do Gemini?**
   Resolve: `curl https://generativelanguage.googleapis.com/v1beta/openai/v1/chat/completions`
   (o path que `path.Join` produz) vs `/v1beta/openai/chat/completions` — se o primeiro der 404,
   caminho A exige director dedicado; usar caminho B (alias openrouter-chat).
4. **[CV-02/escopo] O R$238 Gemini se decompõe quanto em classifier+format-hint (=CV-01) vs media
   pipeline (não-funnelável)?** Resolve: fatura Google por serviço + logs `media_model`/metrics
   (`agents/src/observability/metrics.py`). Define se CV-02 tem corte marginal além do que CV-01 já pega.
5. **[CV-03] getFollowupClient no HEAD atual** — confirmar assinatura/linha antes de planejar o edit.
   Resolve: `grep -n getFollowupClient apps/worker/src/shared/followup-llm.ts`.
6. **[MEASURE-01] O painel Economia atual mostra por-upstream/tenant ou só agregado?** Se só agregado,
   MEASURE-01 pede um recorte extra. Resolve: ler o consumo do `/admin/economy` no dashboard (Phase 15 code).
