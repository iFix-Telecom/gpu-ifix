# Phase 23: Haiku 5.5 como LLM externo principal do gateway (créditos Max) — Research

**Pesquisado em:** 2026-10-14
**Domínio:** ai-gateway (Go, httputil.ReverseProxy + dispatcher por papel) · Anthropic Messages API · OpenRouter · billing/dashboard · TTS externo
**Confiança geral:** MÉDIA-ALTA (fatos de API verificados na doc oficial + dados de produção medidos hoje; design de adaptador é recomendação, não medição)

<user_constraints>
## User Constraints (sem CONTEXT.md — decisões travadas copiadas do 23-HANDOFF.md / ROADMAP)

### Decisões travadas (não rediscutir)
- Tudo que não roda no pod local segue a ordem: **① Claude Haiku 5.5 na API direta da Anthropic** usando os créditos mensais das 2–3 contas Claude Max (rodízio entre as chaves; chave que responde "Your credit balance is too low to access the Anthropic API" é marcada esgotada até virar o mês e o gateway passa para a próxima) → **② Haiku 5.5 no OpenRouter** (`anthropic/claude-haiku-5.5`, provedor de maior vazão) → **③ DeepSeek v4.1 Flash no OpenRouter** (padrão atual).
- Effort **high**.
- Depois de validado em produção: **desligar a 3090** (já desligada em 2026-10-14; a fase formaliza).
- RES-08: tenant sensível nunca vai para externo.
- `maestro-summary` intocado sem perguntar.
- Pendente do Pedro para o passo ①: vincular organização do Console em cada conta Max e gerar API keys. Sem as chaves, desenvolver/testar com o passo ②.

### Discrição do Claude
- Adaptador (SDK Go oficial vs endpoint OpenAI-compatível) — decidir no research.
- Onde guardar as chaves, como detectar/marcar esgotamento.
- Representação da cadeia em aliases × upstreams × tiers.
- Modelo de preço (degrau 100k + cache) em `prices`/billing/dashboard.
- Mecanismo de override de effort por tenant.
- Escolha do provedor de TTS externo (HAIKU-08).
- Fonte do custo real (HAIKU-09).

### Fora de escopo (pendências do handoff, não desta fase)
- `PROSE_CHAR_CAP = 600` do claude-wpp (dividir áudio).
- Validação de ouvido do XTTS (commit e7f7471) e protocolo picotado.
- llama.cpp b10772 / grammar do format-hint.
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Descrição (ROADMAP Phase 23) | Suporte deste research |
|----|------------------------------|------------------------|
| HAIKU-01 | Upstream Anthropic direto: adaptador OpenAI chat/completions → Messages; remove sampling/penalties que dão 400; preserva tools + `tool_choice` forçado | §Padrão 1 (tradutor no Director + ModifyResponse), §Tabela de mapeamento, Pitfalls 1–6 |
| HAIKU-02 | Pool de 2–3 chaves, rodízio, detecção/marcação de chave esgotada, armazenamento seguro | §Padrão 2 (RoundTripper de pool + estado no Redis), tabela de classificação de erros |
| HAIKU-03 | Cadeia de 3 passos no modelo aliases × upstreams × tiers; aliases DeepSeek apontam para ela; `maestro-summary` intocado | §Padrão 3 (2 upstreams novos + tier_priority + cascata por status), Pitfall 7–9 |
| HAIKU-04 | Preço Haiku em `prices` com degrau 5× >100k + cache read/write; billing e dashboard | §Padrão 4 (custo reportado pelo provedor + colunas de cache), tabela de preços verificada |
| HAIKU-05 | Effort high por padrão, override por tenant; prompt caching p/ chat-ifix | §Padrão 5, §Cache |
| HAIKU-06 | RES-08 preservado | §Padrão 6 (onde o RES-08 é aplicado; achado: sensibilidade vem de `api_keys.data_class`) |
| HAIKU-07 | Rollout em prod + validação + desligar 3090 | §Rollout, §Validation Architecture |
| HAIKU-08 | TTS PT-BR sem pod, upstream do gateway com billing por caractere | §Padrão 7 (OpenRouter `/api/v1/audio/speech`) |
| HAIKU-09 | Painel Economia fidedigno (custo real OpenRouter, 3060, economia a preço Haiku, % local por tokens) | §Padrão 8 |
</phase_requirements>

## Project Constraints (do CLAUDE.md)

- **REGRA DE OURO:** toda afirmação sem evidência direta vem marcada `HIPÓTESE:`; separar fatos (com fonte) de hipóteses; "NÃO SEI / dado insuficiente" quando faltar dado. (Global `~/.claude/CLAUDE.md`.)
- **Linguagem:** proibido "provavelmente/geralmente/possivelmente/talvez/pode ser que". (Projeto.)
- **Nomes, não ids:** tratar tenants/aliases/upstreams por nome.
- **GSD:** edições só dentro de fluxo GSD; debugger com `--diagnose` antes de editar; não alterar payloads de APIs externas sem confirmar schema.
- **Prod:** antes de mexer em prod, listar o que será criado, avaliar API oficial vs banco, snapshot antes (memória `feedback-prod-changes-api-vs-db`).
- **Segredos:** nunca commitar tokens/keys; usar placeholder (memória `github-push-protection-cf-token-incident`).
- **ClickUp:** todo trabalho tem card (card desta fase: 86akuaa73).
- **Gateway CI:** antes de push rodar `gofmt -l`, `go build ./...`, testes `-tags integration` (memória `gateway-integration-tests-not-in-executor-check`); migration nova exige bump de `db.Down(n)` em `migration_0026_test.go`/`migration_0029_test.go` (memória `openrouter-provider-prefs-and-dashboard-control`).
- **Deploy gateway prod:** worker-vm, Portainer stack 38 (endpoint 6); `migrate up` com a imagem nova ANTES do PUT; pré-puxar a imagem antes do PUT (outage 2026-08-29); Env completo no PUT (memória `gateway-prod-build-deploy`).

## Summary

A recomendação central é implementar o passo ① como **um upstream novo `anthropic-haiku` construído sobre o mesmo `httputil.ReverseProxy` dos outros upstreams**, com um **tradutor OpenAI→Messages no Director** e **Messages→OpenAI no ModifyResponse** (JSON e SSE), falando a **API nativa `POST /v1/messages`**. O endpoint OpenAI-compatível da Anthropic está descartado por evidência: a própria doc diz que "não é considerado production-ready", **não suporta prompt caching**, devolve `prompt_tokens_details` sempre vazio e **ignora `reasoning_effort`** [CITED: platform.claude.com/docs/en/api/openai-sdk]. Sem cache, o chat-ifix (688 M tokens de entrada em 30 dias) paga ~10× mais na entrada. O SDK Go oficial (`anthropic-sdk-go` v1.80.0) também não é recomendado como transporte: ele faz suas próprias chamadas HTTP com retries, o que contorna a cadeia já existente de breaker, fallthrough pré-byte, audit, tool-call guard e billing — todos acoplados a `ReverseProxy.ModifyResponse`.

A cadeia ①→②→③ cabe no modelo atual **sem upstream "kind" novo no banco**: dois upstreams novos (`anthropic-haiku` e `openrouter-deepseek`) com `tier=1` e `tier_priority` ordenando a cadeia, e linhas de `model_aliases` por (alias, upstream). O `dispatchPinned` já itera os candidatos na ordem (tier, tier_priority) e pula externos para tenant sensível. O que **falta** no código é a cascata por **status HTTP** (429/5xx/529/esgotamento de crédito): hoje só falha de conexão pré-byte cascateia em chat. Como o status chega antes de qualquer byte ir ao cliente, cascatear por status é seguro mesmo em streaming. O rodízio de chaves fica num `RoundTripper` próprio do upstream ① (tenta a próxima chave antes de desistir do upstream), com estado de "esgotada" no Redis.

Dados de produção medidos hoje mudam o plano em três pontos: (1) **há 400 em produção agora** no chat-ifix — "This model does not support assistant message prefill" (≈19 em 20 h): o Maestro manda conversas terminando em mensagem `assistant`, e o Haiku 5.5 rejeita isso; o gateway precisa normalizar antes de ① e ②; (2) **nenhuma requisição de chat passou de 100k tokens nos últimos 30 dias** (máx. 65.734), então o degrau 5× existe mas é raro; (3) TTS teve **80 requisições / 21.013 caracteres em 30 dias** — custo de qualquer provedor externo é irrisório, a escolha é por qualidade de voz. O OpenRouter já serve TTS em `POST /api/v1/audio/speech` no formato OpenAI (ElevenLabs, Gemini TTS, OpenAI, Fish, MAI etc.), reaproveitando a chave existente.

**Recomendação principal:** upstream `anthropic-haiku` = ReverseProxy + tradutor próprio (sem SDK no caminho de transporte) + RoundTripper de pool de chaves; cascata por status para todos os upstreams LLM tier-1 não-finais; custo real gravado a partir do `usage.cost` do OpenRouter e de `usage.cache_*` da Anthropic.

## Architectural Responsibility Map

| Capacidade | Tier principal | Tier secundário | Racional |
|------------|---------------|-----------------|----------|
| Tradução OpenAI↔Messages | API/Backend (gateway, proxy) | — | Clientes (n8n, Maestro, converseai) falam OpenAI; só o gateway sabe que o alvo é Anthropic |
| Pool de chaves + estado esgotado | API/Backend (gateway) | Redis (estado) | Estado compartilhado entre réplicas/restarts; perder o estado custa 1 tentativa por chave |
| Cadeia ①→②→③ | API/Backend (dispatcher) | Database (`upstreams`, `model_aliases`) | Ordem e pinagem são config hot-reload; lógica de cascata é código |
| RES-08 | API/Backend (dispatcher) | Database (`api_keys.data_class`) | Gate já centralizado em `dispatchPinned`/`writeSensitiveBlock` |
| Effort por tenant | Database (`tenants`) | API/Backend (Director) | Mesmo padrão de `tenants.provider_prefs` |
| Preço/custo | Database (`prices`, `billing_events`) | API/Backend (interceptor de usage) | Custo por request no flush; preços versionados |
| Painel Economia | Frontend Server (Next dashboard) | API/Backend (`/admin/economy`) | Cálculo server-side no gateway, dashboard só exibe |
| Custo da 3060 | ops-claude (systemd `vast-unified-*`) | Database (tabela nova de lifecycles auxiliares) | Pod 3060 vive fora do gateway hoje |
| TTS externo | API/Backend (gateway, upstream tts) | OpenRouter | Mesmo contrato `/v1/audio/speech` |

## Fatos medidos em produção (2026-10-14, consultas read-only em `bd_ai_gateway`)

| # | Fato | Fonte |
|---|------|-------|
| F1 | Chat 30 d: `openrouter-chat` 122.740 req, 699,8 M tokens in / 105,3 M out, R$528,38; `emergency_pod_llm` 41.200 req, 29,0 M in / 7,1 M out | `billing_events` agrupado por route/upstream |
| F2 | Por tenant 30 d (chat): chat-ifix 116.951 req / 688,0 M in / 103,7 M out; analise-transcr-voip 41.979 / 22,4 M / 8,0 M; demais < 12 M in | idem por tenant |
| F3 | **Máx. de tokens de entrada por request em 30 d = 65.734 (chat-ifix); requests > 100k = 0** | idem `max(tokens_in)`, `count FILTER (>100000)` |
| F4 | TTS 30 d: kokoro-tts 51 req / 11.810 chars; local-tts 29 req / 9.203 chars | `billing_events` route tts |
| F5 | **400 em produção:** chat-ifix, alias `deepseek-flash`, última mensagem `assistant` → "This model does not support assistant message prefill. The conversation must end with a user message." (≈19 nas últimas 20 h, desde a ponte Haiku de 2026-10-13). Origem: prompts do Maestro "analista silencioso" (system "Você analisa a conversa e decide QUAIS missões disparar…") | `audit_log` + `audit_log_content` |
| F6 | 504 do OpenRouter em 2026-10-13 17:48–18:02: "anthropic/claude-haiku-5.5 is temporarily rate-limited upstream" (provider Amazon Bedrock) — afetou chat-ifix e analise-transcr-voip | `audit_log_content.response` |
| F7 | 502 `upstream_unreachable` em `openrouter-chat` (11 em 20 h). Causa NÃO confirmada — ver HIPÓTESE H3 | `audit_log` |
| F8 | Parâmetros enviados (2 d): alias `deepseek-flash` manda `reasoning_effort:"high"` em 6.669 req, `temperature` em 3.227, `top_p`/penalties em 77, `tool_choice` forçado/auto/none; alias `qwen` manda `reasoning:{"enabled":false}` + `chat_template_kwargs` (2.158), `response_format: json_schema` (413), `tool_choice` forçado por nome (≈43); última mensagem `system` em 96 req; `assistant` em 28 | `audit_log_content.prompt` (chaves JSON) |
| F9 | `converseai-contacts-eval` manda `model:"anthropic/claude-haiku-5.5"` **sem alias**; com o pod ligado esse modelo era servido pelo `emergency_pod_llm` (Qwen) — 390 req em 2 d | join audit × content |
| F10 | Sensibilidade (RES-08) vem de **`api_keys.data_class`**: `cobrancas`, `telefonia`, `converseai-contacts-embed` = sensitive. `tenants.data_class` está `normal` para todos (coluna enganosa) | `api_keys` × `tenants`; STATE.md 19-04 ("sensitivity keys off api_keys.data_class NOT tenants.data_class") |
| F11 | Upstreams LLM hoje: `local-llm` (tier 0, **disabled**), `openrouter-chat` (tier 1, prio 0). Constraint `UNIQUE (role, tier, tier_priority)`; role CHECK inclui llm/stt/embed/tts/rerank | `pg_constraint`, `upstreams` |
| F12 | Preço ativo `anthropic/claude-haiku-5.5`/`openrouter-fireworks`: in 1e-7, out 5e-7 (gravado 2026-10-14 11:30Z pelo timer `gateway-price-sync`) | `prices` |
| F13 | Migration aplicada em prod = 40 (goose) | `goose_db_version` |
| F14 | Proxy `openrouter-chat` tem `ResponseHeaderTimeout: 30s` | `cmd/gateway/main.go` `buildOpenRouterChatProxy` |

## Standard Stack

### Core
| Componente | Versão | Uso | Por quê |
|-----------|--------|-----|---------|
| Go stdlib `net/http/httputil.ReverseProxy` | Go 1.24.9 (go.mod) | Transporte do upstream `anthropic-haiku` | Toda a resiliência (breaker, fallthrough pré-byte, audit, tool-call guard, billing) já está acoplada a ReverseProxy+ModifyResponse [VERIFIED: codebase] |
| Anthropic Messages API | `anthropic-version: 2023-06-01`, modelo `claude-haiku-5-5` | Passo ① | Único caminho com prompt caching + effort nativo + usage de cache [CITED: platform.claude.com/docs/en/models/haiku-5-5/overview] |
| OpenRouter chat/completions | atual | Passos ② e ③ | Já em produção; devolve `usage.cost` real sempre [CITED: openrouter.ai/docs usage-accounting] |
| OpenRouter `/api/v1/audio/speech` | atual | TTS externo | Formato OpenAI, mesma chave [CITED: openrouter.ai/docs/guides/overview/multimodal/tts] |
| Redis (`infra-redis-1`, go-redis v9.18.0) | já no go.mod | Estado de chave esgotada / cooldown | Já usado pelo gateway (breaker force flags) [VERIFIED: go.mod] |

### Supporting
| Item | Uso | Quando |
|------|-----|--------|
| `pkg/openai/types.go` (repo) | Tipos de resposta OpenAI para montar a saída traduzida | Só para a SAÍDA; a ENTRADA deve ser lida como `map[string]json.RawMessage` (padrão dos directors atuais) para não perder campos desconhecidos |
| `anthropic-sdk-go` v1.80.0 (2026-10-09) | NÃO adicionar | Considerar só se o planner quiser os tipos `MessageStreamEventUnion` para parse — custo: puxa aws-sdk-go-v2, mcp go-sdk etc. no go.sum |

### Alternativas consideradas
| Em vez de | Alternativa | Trade-off |
|-----------|-------------|-----------|
| Tradutor próprio sobre ReverseProxy | Endpoint OpenAI-compat da Anthropic (`https://api.anthropic.com/v1/chat/completions`) | Zero tradução, mas **sem prompt caching**, `prompt_tokens_details` vazio, `reasoning_effort` ignorado, doc diz "not production-ready" → **descartado** |
| Tradutor próprio | SDK Go oficial como cliente | Tipos prontos e retries, mas transporte paralelo fora da cadeia de interceptors; precisaria reimplementar breaker/fallthrough/audit/billing → **descartado como transporte** |
| 2º upstream `openrouter-deepseek` | Fallback nativo do OpenRouter `models:[haiku, deepseek]` num único request | Menos código, mas `provider` prefs viram únicas para os dois modelos (`order:["anthropic"]`, `max_price` do Haiku), sem breaker separado, e o gateway não vê qual passo serviu → **descartado** |
| Estado de chave no Redis | Tabela no Postgres | DB dá histórico; Redis já é o padrão para estado efêmero de roteamento. Perder estado custa 1 request de ~centenas de ms por chave → Redis basta |

**Instalação:** nenhuma dependência nova recomendada.

## Package Legitimacy Audit

Nenhum pacote externo novo é recomendado (tradutor em stdlib). Se o planner optar por `github.com/anthropics/anthropic-sdk-go`:

| Pacote | Registro | Idade/versão | Repo | slopcheck | Disposição |
|--------|----------|--------------|------|-----------|------------|
| github.com/anthropics/anthropic-sdk-go | proxy.golang.org | v1.80.0 publicado 2026-10-09 | github.com/anthropics/anthropic-sdk-go (citado na doc oficial) | não executado (slopcheck cobre npm/PyPI; este é módulo Go) | Não recomendado nesta fase; se usado, `checkpoint:human-verify` |

**Pacotes removidos por [SLOP]:** nenhum. **[SUS]:** nenhum.

## Architecture Patterns

### Diagrama do fluxo

```
cliente (n8n / Maestro / converseai)  POST /v1/chat/completions  model=<alias>
        │
        ▼
 auth (api_keys.data_class) → audit → idempotency → DISPATCHER(llm)
        │
        ├─ alias tem linhas em model_aliases? ── não ──► Resolve(llm,0) → local-llm DISABLED → 503
        │                                                (F9: por isso aliases novos p/ "anthropic/claude-haiku-5.5")
        ▼ sim → dispatchPinned: candidatos = [tier0 se pinado] + tier≥1 pinados em (tier, tier_priority)
        │
        │   tenant sensível? ── sim ──► pula todo externo → writeSensitiveBlock 503 (RES-08)
        ▼
 ① anthropic-haiku (tier1, prio 0)
     Director: alias→claude-haiku-5-5 · OpenAI→Messages · normaliza prefill/system · effort · cache_control
     KeyPoolTransport: chave A ─(crédito/402/spend-cap/401/429)─► marca no Redis ─► chave B ─► chave C
        │ todas esgotadas/sem chave ─► errUpstreamRetryable (nenhum byte escrito)
        │ 429/5xx/529 da Anthropic ─► interceptor de status ─► errUpstreamRetryable
        │ 200 ─► ModifyResponse: Messages(JSON|SSE) → OpenAI → audit → toolcall → usage(custo cache-aware)
        ▼ (cascata)
 ② openrouter-chat (tier1, prio 10)   target anthropic/claude-haiku-5.5, prefs order:["anthropic"]
     Director atual + PASS novo: reasoning.effort, normalização de prefill; cache_control (PASS 4 já existe)
        │ 429/5xx/402/524/529 ─► errUpstreamRetryable
        ▼ (cascata)
 ③ openrouter-deepseek (tier1, prio 20) target deepseek/deepseek-v4.1-flash:exacto, prefs padrão DeepSeek
        │ erro aqui = resposta final ao cliente (último salto)
        ▼
 billing_events (tokens in/out + cache read/write + custo real) → /admin/economy → painel
```

### Estrutura de arquivos recomendada (gateway)
```
gateway/internal/proxy/
├── anthropic_translate_req.go     # OpenAI chat/completions JSON → Messages JSON (puro, testável)
├── anthropic_translate_resp.go    # Messages JSON → OpenAI JSON (não-streaming)
├── anthropic_translate_sse.go     # io.ReadCloser que converte SSE Messages → chunks OpenAI
├── anthropic_director.go          # BuildAnthropicDirector (resolver, effort, cache, headers)
├── anthropic_keypool.go           # KeyPool + KeyPoolTransport (RoundTripper) + classificador de erro
├── chat_retryable_status.go       # interceptor: status retryable → errUpstreamRetryable (tier-1 LLM)
├── anthropic_compat.go            # normalizações compartilhadas ① e ② (prefill, system tardio)
└── testdata/anthropic/            # fixtures golden: req/resp/SSE reais capturados
gateway/db/migrations/0041_*.sql   # upstreams novos, tier_priority, tenants.llm_effort, colunas de cache
```

### Padrão 1 — Tradutor OpenAI→Messages no Director (HAIKU-01)

**O quê:** o Director lê o body como `map[string]json.RawMessage`, reescreve para o formato Messages e troca o path para `/v1/messages`. Headers: `x-api-key` (vem do pool), `anthropic-version: 2023-06-01`, `content-type`. Remove `Authorization`.

**Tabela de mapeamento (fatos de API com fonte; decisões marcadas):**

| OpenAI (entrada) | Messages (saída) | Fonte / nota |
|---|---|---|
| `model` (alias) | `"claude-haiku-5-5"` via `resolver.Resolve(alias,"anthropic-haiku")` | ID oficial sem data [CITED: migration-guide Haiku 5.5] |
| mensagens `system`/`developer` no início | `system` top-level (array de blocos text) | |
| `system` no meio/fim | mensagem `role:"system"` nativa (mid-conversation, sem beta header no Haiku 5.5) **se** vier logo após um turno `user` e for a última ou seguida de `assistant`; senão, dobrar como bloco de texto no turno `user` adjacente | [CITED: build-with-claude/mid-conversation-system-messages]. Hoistar para o `system` top-level quebra o cache de tudo (hierarquia tools→system→messages) |
| `assistant` com `tool_calls` | `assistant` com blocos `tool_use{id,name,input}` (`arguments` string → JSON objeto) | |
| mensagens `tool` consecutivas | **um** turno `user` com N blocos `tool_result{tool_use_id,content}` | |
| `user` `image_url` (data URL ou http) | bloco `image` (`source.type` base64 ou url) | |
| `tools[].function` | `tools[]{name,description,input_schema}`; `strict:true` → `strict:true` | |
| `tool_choice:"auto"` / `"none"` | `{type:"auto"}` / `{type:"none"}` | |
| `tool_choice:"required"` | `{type:"any"}` | Haiku 5.5 **aceita** forçado, mas sem thinking [CITED: migration-guide "accepts a forced tool_choice … has no thinking block"] |
| `tool_choice:{type:function,function:{name}}` | `{type:"tool",name}` | idem |
| `parallel_tool_calls:false` | `tool_choice.disable_parallel_tool_use:true` | [ASSUMED] nome do campo — confirmar na doc de tool use |
| `response_format:{type:json_schema,json_schema:{schema}}` | `output_config.format:{type:"json_schema",schema}` | Structured outputs só indisponível no Bedrock para Haiku 5.5 [CITED: migration-guide item 11]; formato exato [ASSUMED] — confirmar |
| `response_format:{type:json_object}` | instrução no system + validação | não há equivalente direto [ASSUMED] |
| `max_tokens` / `max_completion_tokens` | `max_tokens` (obrigatório na Anthropic). Default quando ausente: decisão do planner (sugestão 16000, teto 128000). Thinking conta dentro de `max_tokens` [CITED: migration-guide] | |
| `temperature`, `top_p`, `top_k` | **remover sempre** | qualquer valor ≠ default → 400; `temperature`+`top_p` juntos → 400; `top_p:1` → 400 [CITED: migration-guide "Remove sampling parameters"] |
| `frequency_penalty`, `presence_penalty`, `seed`, `logit_bias`, `logprobs`, `n`, `user`, `metadata`, `store`, `reasoning`, `chat_template_kwargs`, `stream_options`, `provider` | remover | não existem na Messages API |
| `reasoning_effort` (cliente) | ignorado ou usado — decisão HAIKU-05 | ver Padrão 5 |
| (gateway) effort | `output_config.effort:"high"` | [CITED: build-with-claude/effort] |
| (gateway) cache | `cache_control:{type:"ephemeral"}` **top-level** (cache automático, breakpoint avança sozinho) + breakpoint explícito no fim do `system` | [CITED: build-with-claude/prompt-caching] máx. 4 breakpoints; mínimo 512 tokens no Haiku 5.5 |
| `stream:true` | `stream:true` | |
| última mensagem `assistant` sem `tool_calls` (prefill) | **anexar turno `user`** sintético (texto a definir com Pedro) | 400 em produção hoje (F5) [CITED: api/errors "Prefill not supported"] |

**Resposta não-streaming (ModifyResponse):** `content[]` → `choices[0].message.content` (concatena blocos `text`; ignora `thinking`) + `tool_calls` (de `tool_use`: `id`, `function.name`, `function.arguments = json(input)`). `stop_reason` → `finish_reason`: `end_turn`/`stop_sequence`→`stop`, `max_tokens`/`model_context_window_exceeded`→`length`, `tool_use`→`tool_calls`, `refusal`→`content_filter` [ASSUMED mapeamento; `refusal` e `model_context_window_exceeded` existem — CITED migration-guide]. `usage` → `prompt_tokens = input_tokens + cache_read_input_tokens + cache_creation_input_tokens`, `completion_tokens = output_tokens`, `prompt_tokens_details.cached_tokens = cache_read_input_tokens`, `prompt_tokens_details.cache_write_tokens = cache_creation_input_tokens` (mesma forma que o OpenRouter usa — um parser só no interceptor). Fórmula de total de entrada [CITED: api/rate-limits "total_input_tokens = cache_read + cache_creation + input_tokens"]. Atualizar `Content-Length`.

**Streaming (SSE):** eventos `message_start`, `content_block_start`, `content_block_delta` (`text_delta`, `input_json_delta`, `thinking_delta`, `signature_delta`), `content_block_stop`, `message_delta` (stop_reason + usage **cumulativo**), `message_stop`, `ping`, `error` [CITED: build-with-claude/streaming]. Conversão:
- `message_start` → 1º chunk `{"choices":[{"index":0,"delta":{"role":"assistant"}}]}` (guardar `id`, `model`, usage de entrada).
- `text_delta` → `delta.content`.
- `content_block_start` `tool_use` → `delta.tool_calls[{index:k,id,type:"function",function:{name,arguments:""}}]`; `input_json_delta` → `delta.tool_calls[{index:k,function:{arguments:partial_json}}]` (k = contador próprio de tool_use, não o índice do bloco).
- `thinking`/`signature` → descartar.
- `message_delta` → chunk com `finish_reason`; depois chunk de usage com `choices:[]` (o usage interceptor já procura "usage" top-level).
- `message_stop` → `data: [DONE]`. `ping` → descartar.
- `error` depois do 200 → chunk de erro + `[DONE]`; **sem cascata** (bytes já saíram — D-07).

### Padrão 2 — Pool de chaves (HAIKU-02)

**Armazenamento:** env no stack 38, coerente com todos os segredos de upstream hoje (`auth_bearer_env` aponta para env) [VERIFIED: tabela `upstreams`, env do container]. Formato sugerido: `UPSTREAM_LLM_ANTHROPIC_API_KEYS="max-pedro=<chave>,max-b=<chave>,max-c=<chave>"` (rótulo legível + chave; ordem = ordem de uso). A linha `upstreams.auth_bearer_env` do `anthropic-haiku` aponta para esse env; o proxy faz o parse. Nunca logar a chave; métricas/admin usam o rótulo e um fingerprint (sha256[:8]).

**Seleção:** **pegajosa em ordem** (primeira chave não esgotada), **não** round-robin. Motivo: cache de prompt é isolado por organização e por workspace — "Different organizations never share caches" [CITED: prompt-caching]. Round-robin entre 3 orgs triplicaria escritas de cache e cortaria o hit-rate.

**RoundTripper `KeyPoolTransport`:** bufferiza o body (já feito por `prepareReplayBody` no dispatcher; chat é limitado), tenta a chave da vez; se a resposta for de "chave inutilizável", drena, marca e tenta a próxima — tudo antes de qualquer byte ir ao cliente. Se nenhuma chave serve, devolve um erro sentinela que embrulha `errUpstreamRetryable` → `ErrorHandler` suprime a escrita → `dispatchPinned` passa ao ②.

**Classificação de erro (fatos + lacunas):**

| Resposta | Significado | Ação | Fonte |
|---|---|---|---|
| corpo contém "credit balance is too low" (400 `invalid_request_error`, ou 402) | créditos da org acabaram | marcar esgotada até renovação; próxima chave | Texto exato [CITED: about-claude/api-credits-for-subscribers]. **Status HTTP NÃO documentado** — HIPÓTESE H1: 400. Casar por substring do `error.message`, qualquer status 4xx |
| 402 `billing_error` | problema de billing | igual à linha acima | [CITED: api/errors] |
| 429 com `error.details.error_code = "enforced_spend_limit_reached"` (sem `retry-after`) | teto mensal do tier (Start = US$500) | esgotada até 00:00 UTC do dia 1º | [CITED: api/rate-limits "Reaching your spend cap"] |
| 400 "You have reached your specified API usage limits" | limite de gasto definido pelo usuário | esgotada até a data citada na mensagem | [CITED: api/rate-limits] |
| 401 `authentication_error` | chave revogada/expirada | desabilitar chave (TTL longo) + alerta | [CITED: api/errors] |
| 429 `rate_limit_error` com `retry-after` | rate limit | cooldown = retry-after; próxima chave | [CITED: api/rate-limits] |
| 500 / 529 / 504 | Anthropic instável | NÃO trocar chave (é do serviço); cascatear para ② | [CITED: api/errors] |
| 400 de validação (prefill, thinking, schema) | erro do request | NÃO trocar chave, NÃO cascatear (devolver ao cliente) — o tradutor deve evitar | |

**"Até virar o mês":** os créditos renovam **a cada ciclo de cobrança de cada plano Max** (data da assinatura), não no dia 1º — "Credits arrive each billing cycle, shortly after your plan payment is processed" e "Unused credits expire at the end of each billing cycle" [CITED: about-claude/api-credits-for-subscribers]. Marcar até o dia 1º deixaria uma chave recarregada parada até ~30 dias. Recomendação (precisa de confirmação do Pedro, ver Open Question 2): `exhausted_until` = próximo dia de renovação configurado por chave (`max-pedro=<chave>@17` → dia 17), e sem dia configurado, re-teste a cada N horas (default 6 h) usando a própria requisição real (custo: uma ida-e-volta rejeitada, sem cobrança).

**Estado:** Redis `gw:anthropic:key:<fingerprint>` = `{status, until, reason}` com TTL até `until`. Métricas: `gateway_anthropic_key_state{key_label,state}`, `gateway_anthropic_key_switch_total{from,reason}`. Admin read-only `GET /admin/anthropic/keys` (rótulo, fingerprint, estado, até quando). Alerta (ntfy/e-mail já existentes) na 1ª marcação de esgotamento do mês.

**Nota sobre rate limit:** org vinculada a Max sobe para "ao menos o tier Start" (Haiku 5.5 Start: 1.000 RPM, 2 M ITPM, 400 k OTPM; cache read não conta no ITPM) [CITED: api/rate-limits; api-credits-for-subscribers]. Orgs novas podem começar no tier "Evaluation", abaixo do padrão [CITED: api/rate-limits]. HIPÓTESE H2: o volume do chat-ifix (~23 M tokens/dia de entrada, pico desconhecido) cabe no Start com cache; medir com os headers `anthropic-ratelimit-*` no primeiro dia.

### Padrão 3 — Cadeia nos modelos aliases × upstreams × tiers (HAIKU-03)

**Fato:** PK de `model_aliases` = `(alias, upstream_name)` [VERIFIED: migration 0026] → um alias não pode ter dois alvos no mesmo upstream. Por isso ② e ③ precisam de **nomes de upstream distintos** apontando para o mesmo OpenRouter.

**Migration 0041 (sugestão):**
1. `UPDATE upstreams SET tier_priority=10 WHERE name='openrouter-chat'` (antes dos INSERTs, por causa de `UNIQUE(role,tier,tier_priority)`).
2. `INSERT anthropic-haiku (role llm, tier 1, tier_priority 0, url_env UPSTREAM_LLM_ANTHROPIC_URL, auth_bearer_env UPSTREAM_LLM_ANTHROPIC_API_KEYS)`.
3. `INSERT openrouter-deepseek (role llm, tier 1, tier_priority 20, url_env UPSTREAM_LLM_OPENROUTER_URL, auth_bearer_env UPSTREAM_LLM_OPENROUTER_AUTH_BEARER)` — mesmo env do ②.
4. Linhas de alias (via `gatewayctl model-alias set`, não migration, para manter o padrão operacional): para os 7 aliases do padrão (`qwen`, `deepseek-flash`, `deepseek-v4-flash`, `gpt-4o`, `gpt-5.4-mini`, `gpt-5.4-mini-2026-03-17`, `ia-kanban`): `anthropic-haiku → claude-haiku-5-5`; `openrouter-chat → anthropic/claude-haiku-5.5` (já está); `openrouter-deepseek → deepseek/deepseek-v4.1-flash:exacto` com as prefs DeepSeek de 2026-10-12 (snapshot `23-model-aliases-pre-haiku-20261013.json`). Linhas `local-llm` intocadas.
5. Aliases novos para quem manda o nome do modelo direto (F9): `anthropic/claude-haiku-5.5` e `claude-haiku-5-5` com as mesmas 3 linhas.
6. `maestro-summary`: nenhuma linha nova (fica só em `openrouter-chat`, alvo DeepSeek BaseTen).

**Código (main.go):** registrar `llmRoleProxies["anthropic-haiku"]` e `llmRoleProxies["openrouter-deepseek"]`; `buildOpenRouterChatProxy` hoje fixa o nome `"openrouter-chat"` no Director e no ErrorHandler → parametrizar o nome. `providerForUpstream` ganha `openrouter-deepseek → openrouter-fireworks` e `anthropic-haiku → anthropic`. Proxy só é registrado se `loader.Get(name)` tiver URL na partida — sem chaves, `anthropic-haiku` fica sem proxy e `dispatchPinned` o pula com WARN ("pinned candidate has no registered proxy") [VERIFIED: dispatcher.go] → o passo ① fica dormente e ②/③ funcionam.

**Cascata por status (código novo, essencial):** hoje só falha de conexão pré-byte cascateia em chat (`transport.go`; doc diz "timeouts/5xx do not fall through" para chat) [VERIFIED: transport.go, errors.go]. Criar `chatRetryableStatusInterceptor` (primeiro na `ComposeInterceptors` dos três upstreams tier-1 LLM): status ∈ {429, 500, 502, 503, 504, 524, 529} ou 402 → `errUpstreamRetryable`. É pré-byte seguro **mesmo com `stream:true`**: `ModifyResponse` roda depois dos headers e antes de copiar o body; com status ≠ 2xx nada foi escrito. Exigências:
- Não aplicar a erros 400 de validação (iriam falhar igual nos outros saltos e esconder o erro real).
- No último salto (③) o erro precisa chegar ao cliente: guardar o último corpo de erro no `dispatchResult` e escrevê-lo na exaustão, em vez do 503 genérico `pinned_upstreams_unavailable`. Decisão de implementação do planner.
- Breaker: decidir se 429/402 contam como falha (o 429 de esgotamento de chave NÃO deve abrir o breaker do `anthropic-haiku` se houver outras chaves — tratado dentro do KeyPoolTransport).

### Padrão 4 — Preço e custo com cache e degrau 100k (HAIKU-04)

**Preços verificados (US$/M tokens)** [CITED: platform.claude.com/docs/en/about-claude/pricing; models/haiku-5-5/overview]:

| Haiku 5.5 | Entrada | Escrita cache 5 min | Escrita cache 1 h | Leitura cache | Saída |
|---|---|---|---|---|---|
| prompt ≤ 100.000 tokens | 0,10 | 0,125 | 0,20 | 0,01 | 0,50 |
| prompt > 100.000 tokens | 0,50 | 0,625 | 1,00 | 0,05 | 2,50 |

"A request's prompt length counts all of its input tokens, including cache reads and cache writes. Each request is priced on its own" [CITED: pricing "Long context pricing"]. `inference_geo:"us"` multiplica 1,1× (não usar). Batch 50% (fora de escopo, tráfego é síncrono).

**Recomendação:**
- **OpenRouter (② e ③):** usar `usage.cost` (US$) que o OpenRouter devolve **sempre**, em todo response e no último chunk SSE ("usage accounting is now automatic") [CITED: openrouter.ai/docs usage-accounting]. Converter com `fx_rates` e gravar em `cost_external_brl`. Isso já inclui desconto de cache, degrau e provedor escolhido — elimina a superestimativa (~2×) medida em 2026-10-13. Tabela `prices` vira só fallback quando `cost` faltar.
- **Anthropic direta (①):** a API não devolve custo → calcular a partir de `usage`: `input_tokens`, `cache_creation_input_tokens` (com quebra `cache_creation.ephemeral_5m_input_tokens`/`ephemeral_1h_input_tokens`), `cache_read_input_tokens`, `output_tokens` [CITED: prompt-caching]. Degrau: se `input+cache_read+cache_creation > 100000`, usar a linha "long".
- **Esquema:** `prices.unit` CHECK ganha `cache_read_token`, `cache_write_token` (5 min) e `cache_write_1h_token` [VERIFIED: CHECK atual só tem input/output/audio/embed]. Degrau como modelo separado (`claude-haiku-5-5:long`) para não mudar a chave `(model,provider,unit)` do `PricesLoader`. `billing_events` ganha `tokens_cache_read INT DEFAULT 0`, `tokens_cache_write INT DEFAULT 0`, `cost_source TEXT` (`provider_reported`|`price_table`). ALTER no pai particionado propaga às partições; lembrar automação de partições (memória `billing-events-partition-gap-aug2026`).
- `sseUsageFrame` hoje só lê `prompt_tokens/completion_tokens/total_tokens` [VERIFIED: interceptor_usage.go] → estender com `cost`, `prompt_tokens_details.cached_tokens`, `prompt_tokens_details.cache_write_tokens`.
- **Créditos Max = custo afundado:** decisão do Pedro se o custo do ① entra em `cost_external_brl` a preço de lista (recomendado, com `upstream=anthropic-haiku` para o painel separar "coberto por créditos" de "pago") — Open Question 4.
- Timer `gateway-price-sync` (ops-claude, 08:30 BRT) já grava preços do Haiku via OpenRouter [VERIFIED: F12 + systemctl --user list-timers] → incluir as unidades de cache e a linha `anthropic` direta.

**Estimativa (HIPÓTESE H4, não medição):** 30 d de chat ≈ 729 M in / 119 M out. Sem cache: ≈ US$73 entrada + US$60 saída. Com 57% de leitura de cache (medido numa janela curta em 2026-10-13): entrada ≈ US$35. Effort high aumenta a saída (thinking conta como output) em fator não medido. Créditos de 2 contas Max 20x = US$400/mês.

### Padrão 5 — Effort e cache (HAIKU-05)

- Valores: `low|medium|high|xhigh|max`; default do Haiku 5.5 = `medium`; Anthropic recomenda `high` "para trabalho de conhecimento, tarefas de agente longas e seguir instruções à risca" [CITED: build-with-claude/effort].
- **Precedência recomendada:** `tenants.llm_effort` (coluna nova, CHECK nos 5 valores, NULL = herda) > env `ANTHROPIC_DEFAULT_EFFORT=high`. O `reasoning_effort` do cliente é **ignorado e logado** — mesmo princípio do `provider` ("gateway policy always wins") [VERIFIED: openrouter_director.go]. Alternativa: cliente vence se válido (Open Question 3).
- No passo ② (OpenRouter), injetar `reasoning:{"effort":"<nível>"}` para alvos `anthropic/*` e remover `reasoning:{enabled:false}` do cliente. HIPÓTESE H5: o OpenRouter repassa `reasoning.effort` como `output_config.effort` ao Haiku 5.5 — evidência indireta: o agente converseai manda `reasoning.effort` via OpenRouter e o smoke de 2026-10-13 mostrou 257 tokens de raciocínio (memória `converseai-haiku55-agent-drift-portainer`). Validar com 2 chamadas (low vs high) comparando `completion_tokens_details.reasoning_tokens`.
- **Impacto de mudança:** o alias `qwen` (analise-transcr-voip, campanhas, ia-kanban, converseai-*) manda `reasoning:{enabled:false}` (F8) → hoje roda o Haiku **sem thinking** via OpenRouter (HIPÓTESE H6, depende do mapeamento do OpenRouter). Ligar effort high para eles aumenta latência e custo de saída. Confirmar com o Pedro se o "high" vale para todos ou só para chat-ifix (Open Question 3).
- Mudar o effort top-level entre requests de uma mesma conversa invalida o cache [CITED: effort "Hold top-level effort constant"] → effort fixo por tenant, nunca por request.
- `tool_choice` forçado no Haiku 5.5: resposta começa pelo tool call, **sem thinking** [CITED: migration-guide] → effort não encarece as chamadas forçadas do converseai.
- **Cache:** Anthropic direta — `cache_control` top-level (automático) + breakpoint no fim do system; mínimo 512 tokens. OpenRouter — PASS 4 já injeta dois breakpoints (commit f06681f) e `order:["anthropic"]` porque o Vertex só respeita o último breakpoint (memória). Cache 1 h custa 2× a escrita; usar 5 min (padrão) salvo medição contrária.

### Padrão 6 — RES-08 (HAIKU-06)

- Gate em `dispatchPinned`: candidato com `tier ≥ 1` é `external=true`; tenant sensível pula externo; se só sobrou externo → `writeSensitiveBlock` (503 `upstream_unavailable_for_sensitive_tenant`) [VERIFIED: dispatcher.go]. Os dois upstreams novos são tier 1 → protegidos sem código novo.
- Sensibilidade vem de `api_keys.data_class` (F10). Testes de integração devem usar chave de `telefonia`/`cobrancas`.
- Exceção conhecida: over-context cascateia para externo até para sensível (decisão Pedro 2026-08-24, memória `res08-overcontext-carveout`). Com o Haiku (1 M de contexto) e pod desligado, o carve-out não muda nada nesta fase.
- Caminhos que **não** passam pelo `dispatchPinned`: overrides do schedule/shed (`dispatchOverride`) roteiam para `Loader.Resolve(role,1)`, e o snapshot guarda em `byRoleTier` o **último** tier-1 escrito (maior `tier_priority`) [VERIFIED: loader.go `newSnapshot`] → com 3 tier-1, o override iria para `openrouter-deepseek`. Hoje todos os tenants estão em modo `24/7` e o pod está off (shed não dispara) → risco latente, não ativo. Registrar e testar.

### Padrão 7 — TTS externo PT-BR (HAIKU-08)

- **Fato:** OpenRouter expõe `POST /api/v1/audio/speech` no formato OpenAI (`model`, `input`, `voice`, `response_format: mp3|pcm` **default pcm**, `speed`, `provider.options`) [CITED: openrouter.ai/docs/guides/overview/multimodal/tts]. 35 modelos de fala listados hoje via `GET /api/v1/models?output_modalities=speech` [VERIFIED: chamada à API pública], entre eles (preço `prompt` por caractere, US$/M chars): `elevenlabs/eleven-flash-v2.5` 20; `elevenlabs/eleven-multilingual-v2` 40; `elevenlabs/eleven-v3` 40; `elevenlabs/eleven-v4` 40; `elevenlabs/eleven-v4-turbo` 20; `microsoft/mai-voice-2.1` 22; `fish-audio/s2.1-pro` 15; `google/gemini-3.8-flash-tts` (0,5/M in + 9/M out tokens de áudio); `qwen/qwen-audio-3.0-tts-flash` 15; `hexgrad/kokoro-82m` 0,62. O exemplo da doc cita `openai/gpt-4o-mini-tts-2025-12-15`.
- **Custo:** 21.013 chars/30 d (F4) × US$40/M = **US$0,84/mês** no modelo mais caro da lista acima. Decisão por qualidade, não preço.
- **Recomendação:** upstream `openrouter-tts` (role tts, tier 1, prio 0; `local-tts` Kokoro CPU desce para prio 10 como último recurso) com Director que: (a) reescreve `model` (alias `tts-1` → modelo escolhido), (b) mapeia `voice` (`pm_alex` e vozes atuais → voz do provedor), (c) **força `response_format:"mp3"` quando o cliente não manda** (OpenRouter default é pcm; consumidores atuais esperam mp3 — `claude-tts` manda `mp3` explícito), (d) injeta Bearer do OpenRouter. Billing: route `tts`, `tokens_in` = caracteres (já existe), preço por provedor (`model` fixo por upstream, provider `openrouter`).
- **Escolha da voz:** teste cego de ouvido com o Pedro (mesmo método do A/B do LLM) com 3–4 candidatos em PT-BR, frases reais do claude-wpp/claude-tts (números, R$, protocolos). HIPÓTESE H7: ElevenLabs multilingual/v3 e Gemini TTS soam nativos em PT-BR; sem medição.
- Latência: HIPÓTESE H8 — provedores externos respondem em 1–3 s por frase vs ~10 s do Kokoro CPU (memória `pods-desligados-20261014`); medir.
- `GET` de vozes do gateway (`voices.go`) precisa listar as vozes novas.

### Padrão 8 — Painel Economia fidedigno (HAIKU-09)

Estado atual [VERIFIED: gateway/internal/admin/economy.go, db/queries/billing.sql]:
- `phantom_brl` = soma de `cost_local_phantom_brl` (preço de referência `openrouter-fireworks` + model; hoje modelos DeepSeek).
- `vast_brl` = só `primary_lifecycles` (3090). **A 3060 não entra** (controlada por `ops/vast-3060/unified3060.py` + timers em ops-claude; estado só em `/var/lib/vast-3060/state.json`, sem histórico) [VERIFIED: repo].
- `pct_servido_local` = `local_requests/total_requests` (contagem de requests).
- `custo_openrouter_brl` = soma de `cost_external_brl` (estimativa da tabela `prices`).

Mudanças:
1. **Custo real OpenRouter:** com o Padrão 4, `cost_external_brl` passa a vir de `usage.cost` por request. Reconciliação diária opcional com `GET /api/v1/activity` (últimos 30 dias UTC completos, por modelo/endpoint, campo `usage` = custo; **exige management key**) [CITED: openrouter.ai/docs/api GET /api/v1/activity]. Memória `openrouter-dashboard-access` registra management key nos secrets.
2. **Custo da 3060:** criar tabela `aux_pod_lifecycles` (pod, instance_id, started_at, ended_at, dph_real, total_cost_usd) e fazer `unified3060.py` gravar start/stop (ele já calcula custo real `hourly + storage_h` em `real_cost()`) [VERIFIED: unified3060.py]. Histórico anterior: HIPÓTESE H9 — a API do Vast expõe faturas/cobranças por instância; não verificado. Sem isso, o histórico da 3060 é "NÃO SEI".
3. **Economia a preço Haiku:** o phantom de chat passa a usar a linha de preço do Haiku (`anthropic/claude-haiku-5.5`) como referência em vez de DeepSeek; definir se re-calcula histórico (query) ou só tráfego novo. Mudança de semântica: decisão do Pedro 2026-10-07 diz "economia = só self-hosted" (memória `billing-phantom-economia-semantics`) — mantém.
4. **% local por tokens:** nova query `SUM(tokens_in+tokens_out) FILTER (WHERE cost_local_phantom_brl>0) / SUM(tokens_in+tokens_out)`; manter a contagem por requests como campo secundário.
5. Dashboard: `dashboard/src/components/economy-panel.tsx`, `economy-trend-chart.tsx`, `app/(dashboard)/economia/page.tsx`, tipo espelho em `dashboard/src/lib/gateway.ts` (+ `gateway.test.ts`). Lembrar gotcha RSC/proxy (memória `dashboard-rsc-proxy-fetch-gotcha`).

### Anti-padrões a evitar
- **Hoistar todo `system` tardio para o `system` top-level** (como o compat layer): muda o início do prompt a cada turno e zera o cache.
- **Round-robin de chaves:** fragmenta cache entre orgs.
- **Cascatear em 400 genérico:** esconde o erro real e gasta 3 saltos num request inválido.
- **Typed struct na entrada** (`pkg/openai.ChatCompletionRequest`): perde campos desconhecidos; usar `map[string]json.RawMessage`.
- **Cascatear depois do primeiro byte de SSE** (viola D-07).

## Don't Hand-Roll

| Problema | Não construir | Usar | Por quê |
|---|---|---|---|
| Retry/breaker do passo ① | loop de retry próprio ou SDK com retries | dispatcher + `breaker.Set` + `errUpstreamRetryable` existentes | toda a cadeia já trata pré-byte, métricas e audit |
| Custo do OpenRouter | recalcular com tabela de preços | `usage.cost` do response | inclui cache, degrau, provedor real; sempre presente |
| Cache do lado Anthropic | heurística de breakpoints por turno | `cache_control` top-level (automático) + 1 breakpoint no system | a API move o breakpoint sozinha |
| TTS | adaptador por provedor (ElevenLabs/Gemini nativos) | OpenRouter `/api/v1/audio/speech` | mesmo contrato OpenAI, mesma chave, troca de modelo por config |
| Contagem de tokens > 100k | tokenizer local | `usage` devolvido pela Anthropic (soma input+cache) | o degrau é decidido pelo que a API cobra |

**Insight:** o único código realmente novo e inevitável é a tradução de formato (request, resposta JSON, SSE) e o pool de chaves; o resto já existe no gateway e deve ser reaproveitado.

## Common Pitfalls

### Pitfall 1: Prefill (última mensagem `assistant`) → 400
**O quê:** Haiku 5.5 rejeita conversa terminando em `assistant` [CITED: api/errors]. **Já acontece em produção** via OpenRouter (F5). **Evitar:** normalização compartilhada ① e ② — anexar turno `user` sintético (texto acordado com o Pedro) quando a última mensagem é `assistant` sem `tool_calls`. **Sinal:** 400 "does not support assistant message prefill" no `audit_log_content`.

### Pitfall 2: Sampling params → 400
`temperature` ≠ 1, `top_p` ≠ 0,99, qualquer `top_k`, ou `temperature`+`top_p` juntos → 400 [CITED: migration-guide]. F8: milhares de requests mandam `temperature`. **Evitar:** remover sempre no tradutor. No ②, o OpenRouter descarta hoje — desde que sem `require_parameters` (memória).

### Pitfall 3: `max_tokens` pequeno + thinking
Thinking conta em `max_tokens`; request pode parar em `max_tokens` depois do bloco thinking, sem texto [CITED: migration-guide]. Clientes mandam `max_tokens` baixos (alias `qwen`: 2.577 req com `max_tokens`). **Evitar:** piso mínimo quando effort ≥ medium (valor a definir), ou effort menor para esses tenants.

### Pitfall 4: Timeout de header em não-streaming
Proxy do OpenRouter usa `ResponseHeaderTimeout: 30s` (F14). Em não-streaming o header só chega no fim da geração; com effort high/xhigh a geração passa de 30 s (memória: effort `max` = 15–30 s até em pergunta fácil). HIPÓTESE H3: parte dos 502 `upstream_unreachable` (F7) é esse timeout — verificar no log do gateway por "timeout awaiting response headers". **Evitar:** timeout de header ≥ 120 s nos upstreams Haiku; o `TimeoutHandler` externo do gateway também precisa comportar.

### Pitfall 5: Thinking blocks em conversas OpenAI
Clientes OpenAI não devolvem blocos `thinking`. A doc manda "pass thinking blocks back unmodified with tool results" e rejeita bloco **modificado** [CITED: api/errors "Thinking blocks cannot be modified"]. HIPÓTESE H10: omitir os blocos por completo é aceito (perde-se o raciocínio anterior, sem 400). Evidência indireta: 438 requests Haiku via OpenRouter terminando em `tool` sem erro (F8 + F7). **Validar no Wave 0** com chamada real de tool loop direto na Anthropic. Também: blocos de thinking só valem na conta que os produziu [CITED: haiku-5-5 overview] — irrelevante se não guardamos blocos.

### Pitfall 6: `system` no meio/fim da conversa
96 requests terminam em `system` (F8). Hoist quebra cache; mid-conversation system tem regra de posição (após turno `user`, última ou seguida de `assistant`) [CITED: mid-conversation-system-messages]. **Evitar:** aplicar a regra de posição e, fora dela, dobrar o texto no turno `user`.

### Pitfall 7: Cascata só em falha de conexão
Hoje 429/5xx/504 do OpenRouter vão direto ao cliente (F6: 504 "rate-limited upstream" chegou ao chat-ifix) [VERIFIED: transport.go]. Sem o interceptor de status, a cadeia ①→②→③ não existe na prática.

### Pitfall 8: `UNIQUE(role, tier, tier_priority)`
Inserir `anthropic-haiku` com prio 0 antes de mover `openrouter-chat` para 10 viola a constraint. Ordem na migration: UPDATE primeiro.

### Pitfall 9: Modelo sem alias → 503
Com `local-llm` desabilitado, modelo sem linha em `model_aliases` cai em `Resolve(llm,0)` → 503 "No primary upstream configured" [VERIFIED: dispatcher.go]. `converseai-contacts-eval` usa `anthropic/claude-haiku-5.5` sem alias (F9). **Evitar:** aliases para os nomes de modelo usados diretamente.

### Pitfall 10: Probe sintético do upstream ①
`probe.go` faz POST `<url>/v1/chat/completions` com `Bearer <auth>` para todo upstream llm [VERIFIED: probe.go]. Para `anthropic-haiku` isso iria com a lista de chaves inteira num header inválido para `/v1/chat/completions`. **Evitar:** caso especial (ex.: `GET /v1/models` com `x-api-key` da 1ª chave válida, sem custo) ou pular o probe desse upstream.

### Pitfall 11: Migration + deploy
Migration nova exige bump `db.Down(n)` nos testes 0026/0029; `MIGRATE_ON_BOOT=false` em prod → `migrate up` com a imagem nova antes do PUT; pré-pull da imagem; Env completo (77+ vars) no PUT do stack 38 (memórias `gateway-prod-build-deploy`, `openrouter-provider-prefs-and-dashboard-control`).

### Pitfall 12: Rotação perde cache
Trocar de chave = trocar de org = cache frio (primeira chamada de cada conversa paga escrita). Aceitável no esgotamento mensal; evitar troca por 429 transitório se o `retry-after` for curto (decisão do planner: limiar).

### Pitfall 13: OpenRouter TTS default `pcm`
Sem `response_format`, o OpenRouter devolve PCM [CITED: OpenRouter TTS docs]. Forçar `mp3` no Director quando o cliente não mandar.

## Code Examples

### Classificador de erro de chave (esboço)
```go
// Fonte das regras: platform.claude.com/docs/en/api/errors, /api/rate-limits,
// /about-claude/api-credits-for-subscribers (texto exato da mensagem de crédito).
type keyVerdict int

const (
	keyOK keyVerdict = iota
	keyExhausted   // créditos/spend cap: até a renovação
	keyRevoked     // 401
	keyCooldown    // 429 rate limit com retry-after
	notKeyProblem  // 5xx/529/400 de validação: não troca chave
)

func classifyAnthropicKeyError(status int, body []byte, h http.Header) (keyVerdict, time.Duration) {
	var env struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Details struct {
				ErrorCode string `json:"error_code"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &env)
	msg := strings.ToLower(env.Error.Message)
	switch {
	case strings.Contains(msg, "credit balance is too low"), status == http.StatusPaymentRequired:
		return keyExhausted, 0
	case status == 429 && env.Error.Details.ErrorCode == "enforced_spend_limit_reached":
		return keyExhausted, 0
	case status == 400 && strings.Contains(msg, "reached your specified"):
		return keyExhausted, 0
	case status == 401:
		return keyRevoked, 0
	case status == 429:
		if s, err := strconv.Atoi(h.Get("retry-after")); err == nil {
			return keyCooldown, time.Duration(s) * time.Second
		}
		return keyCooldown, 30 * time.Second
	}
	return notKeyProblem, 0
}
```

### Request Messages com effort, cache automático e system cacheado
```json
// Fonte: platform.claude.com/docs/en/build-with-claude/effort + prompt-caching
{
  "model": "claude-haiku-5-5",
  "max_tokens": 16000,
  "cache_control": {"type": "ephemeral"},
  "output_config": {"effort": "high"},
  "system": [{"type": "text", "text": "<system do cliente>", "cache_control": {"type": "ephemeral"}}],
  "tools": [{"name": "get_messages", "description": "...", "input_schema": {"type": "object", "properties": {}}}],
  "tool_choice": {"type": "auto"},
  "messages": [{"role": "user", "content": "..."}],
  "stream": true
}
```

### Usage traduzido para o formato que o interceptor já lê (estendido)
```json
{"id":"msg_...","object":"chat.completion.chunk","model":"claude-haiku-5-5","choices":[],
 "usage":{"prompt_tokens":12050,"completion_tokens":431,"total_tokens":12481,
          "prompt_tokens_details":{"cached_tokens":11800,"cache_write_tokens":0}}}
```

## State of the Art

| Antes | Agora | Quando | Impacto |
|---|---|---|---|
| `thinking:{type:"enabled",budget_tokens}` | `thinking` adaptativo + `output_config.effort` | Claude 4.7+; Haiku 5.5 rejeita `enabled` com 400 | Nunca mandar `budget_tokens` |
| Sampling params livres | Proibidos (salvo defaults) | Claude 4.7+ | Tradutor remove |
| Prefill de assistant | 400 | Claude 4.6+ | Normalizar conversa |
| Cache só com breakpoints manuais | `cache_control` top-level automático | doc atual | 1 campo resolve multi-turno |
| OpenRouter `usage:{include:true}` | usage+cost sempre presentes (parâmetro deprecado) | doc atual | Custo real sem flag |
| Haiku 4.5 (200k ctx, US$1/5) | Haiku 5.5 (1 M ctx, US$0,10/0,50 ≤100k), lançado 2026-10-07 | 2026-10-07 | — |

**Deprecado/desatualizado:** endpoint OpenAI-compat da Anthropic para produção; memória/handoff que dizia "tool_choice forçado funciona" está **correta para Haiku 5.5**, mas Opus 5.5/Sonnet 5.5 rejeitam — não trocar de modelo sem rever.

## Assumptions Log

| # | Afirmação | Seção | Risco se errado |
|---|-----------|-------|-----------------|
| H1 | Status HTTP de "credit balance is too low" é 400 (doc não diz) | Padrão 2 | Baixo: classificador casa por texto em qualquer 4xx |
| H2 | Volume do chat-ifix cabe no tier Start com cache | Padrão 2 | 429 frequentes no ① → cascata para ② (degrada custo, não disponibilidade) |
| H3 | Parte dos 502 `upstream_unreachable` são `ResponseHeaderTimeout` 30 s | Pitfall 4 | Causa diferente fica sem correção |
| H4 | Estimativa de custo mensal Haiku (~US$95 + thinking) | Padrão 4 | Créditos insuficientes → mais tráfego no ② pago |
| H5 | OpenRouter mapeia `reasoning.effort` → effort do Haiku 5.5 | Padrão 5 | Effort do ② fica no default `medium` |
| H6 | `reasoning:{enabled:false}` hoje desliga o thinking do Haiku via OpenRouter | Padrão 5 | Mudança de comportamento ao forçar high é menor/maior que o previsto |
| H7 | ElevenLabs/Gemini TTS soam nativos em PT-BR | Padrão 7 | Escolha errada de voz; mitigado pelo teste cego |
| H8 | TTS externo 1–3 s/frase | Padrão 7 | — |
| H9 | API do Vast permite reconstruir custo histórico da 3060 | Padrão 8 | Histórico da 3060 fica "NÃO SEI" |
| H10 | Omitir blocos thinking em tool loops não dá 400 | Pitfall 5 | Tool loops do chat-ifix quebram no ① → precisa guardar blocos |
| A1 | Campo `disable_parallel_tool_use` e formato `output_config.format` exato | Padrão 1 | 400 em requests com `parallel_tool_calls:false` / `json_schema` |
| A2 | Mapeamento `refusal`→`content_filter` | Padrão 1 | Cliente interpreta finish_reason diferente |

## Open Questions (RESOLVED)

> Resolução registrada em 2026-10-14 (revisão do planner, iteração 1):
> 1. RESOLVED — 23-CONTEXT.md (Prefill): o gateway anexa turno user neutro ("Prossiga.") só para alvos Anthropic, ① e ②; planos 23-01 e 23-08.
> 2. RESOLVED — 23-CONTEXT.md (Chave esgotada): dia de renovação por chave + re-teste a cada 6 h, ordem fixa; plano 23-05.
> 3. RESOLVED — 23-CONTEXT.md (Effort): a recomendação "cliente ignorado" deste research foi SUBSTITUÍDA — pedido explícito do cliente vence, depois o tenant, depois high; planos 23-09 e 23-12.
> 4. RESOLVED — 23-CONTEXT.md (Painel): custo do ① a preço de lista marcado "coberto por créditos", caixa real = 0; planos 23-10, 23-11 e 23-13.
> 5. RESOLVED — teste cego de voz com o Pedro no plano 23-17 (checkpoint:decision).
> 6. RESOLVED — só daqui para frente, via POST /admin/aux-pod-lifecycles gravado pelo unified3060.py; planos 23-11 e 23-13.
> 7. RESOLVED — gate humano no 23-18 task 1 (Pedro confirma os termos suplementares antes de ligar o ①).
> 8. RESOLVED — gate humano no 23-18 task 1 (Pedro informa o tier de cada org no Console).

1. **Texto do turno `user` sintético para o prefill (F5).**
   - Sabemos: Maestro manda transcrição terminando na fala do bot; Haiku rejeita.
   - Falta: o que o Maestro espera (análise, não continuação). Corrigir no Maestro (conversalabs, terceiro) ou no gateway?
   - Recomendação: gateway anexa `{"role":"user","content":"(analise a conversa acima)"}` ou equivalente acordado; abrir pendência para o Maestro.
2. **"Até virar o mês" vs ciclo de cobrança de cada Max.**
   - Sabemos: créditos renovam por ciclo do plano, não no dia 1º [CITED].
   - Recomendação: dia de renovação por chave no env + re-teste a cada 6 h sem dia configurado. Pedro confirma.
3. **Effort high para todos os tenants ou só chat-ifix? Cliente pode sobrescrever?**
   - Sabemos: alias `qwen` desliga reasoning hoje; chat-ifix já pede `high`.
   - Recomendação: default high global + `tenants.llm_effort` para exceções; cliente ignorado.
4. **Custo do ① (créditos) no painel:** lista (cost_external) com rótulo "coberto por créditos" ou R$0?
5. **Escolha do modelo/voz de TTS:** teste cego com o Pedro.
6. **Histórico de custo da 3060:** reconstruir (API Vast — H9) ou só daqui para frente?
7. **Termos dos créditos:** "use them to build and run your own applications and agents" [CITED]; os termos suplementares (`anthropic.com/legal/credit-terms`) não foram lidos neste research — Pedro valida que uso num gateway que atende clientes da empresa é permitido.
8. **Tier inicial das orgs novas** ("Evaluation" pode ter limites abaixo do Start) — verificar no Console após vincular.

## Environment Availability

| Dependência | Requerida por | Disponível | Versão | Fallback |
|---|---|---|---|---|
| Go | build/testes gateway | ✓ (ops-claude) | 1.24.9 | — |
| Docker (testcontainers/integration) | `-tags integration` | ✓ via sudo (memória) | — | CI |
| psql | consultas/validação | ✓ | — | — |
| SSH worker-vm / Portainer API | deploy stack 38 | ✓ | — | — |
| Redis `infra-redis-1` | estado do pool | ✓ (já usado pelo gateway) | — | — |
| **Chaves Anthropic das contas Max** | passo ① ao vivo | ✗ | — | desenvolver com servidor fake (httptest) + passo ②; ① dormente |
| OpenRouter key (prod) | ②, ③, TTS | ✓ (env `UPSTREAM_LLM_OPENROUTER_AUTH_BEARER`) | — | — |
| OpenRouter management key | reconciliação `/api/v1/activity` | ✓ segundo memória `openrouter-dashboard-access` (não verificado nesta sessão) | — | só `usage.cost` por request |
| Vast API | custo 3060 | ✓ (memória `vast-ai-api-key`) | — | — |

**Bloqueante sem fallback:** nenhum para desenvolver; o rollout do ① depende das chaves.

## Validation Architecture

### Test Framework
| Propriedade | Valor |
|---|---|
| Framework | Go `testing` + testify; testcontainers-go (integration); vitest (dashboard) |
| Config | `.github/workflows/build-gateway.yml`, `build-dashboard.yml` |
| Quick run | `cd gateway && go test ./internal/proxy/ -run 'Anthropic|KeyPool|RetryableStatus|TTSOpenRouter' -count=1` |
| Suite completa | `gofmt -l . && go test ./gateway/... ./pkg/openai/... -count=1 -race && sudo env PATH=/usr/local/go/bin:$PATH HOME=$HOME GOCACHE=$HOME/.cache/go-build GOPATH=$HOME/go go test -tags=integration ./gateway/internal/integration_test/... -count=1` ; dashboard: `cd dashboard && npx vitest run` |

### Requirements → Test Map
| Req | Comportamento | Tipo | Comando | Existe? |
|---|---|---|---|---|
| HAIKU-01 | request OpenAI → Messages (golden: system, tool msgs, tool_choice forçado/required/none, image, json_schema, sampling removido, prefill normalizado, system tardio) | unit | `go test ./internal/proxy/ -run TestAnthropicTranslateRequest` | ❌ Wave 0 |
| HAIKU-01 | resposta JSON e SSE → OpenAI (texto, tool_calls streaming com `input_json_delta`, thinking descartado, usage com cache, error mid-stream) | unit | `-run 'TestAnthropicTranslateResponse|TestAnthropicSSE'` | ❌ Wave 0 |
| HAIKU-01 | smoke real (quando houver chave): tool loop + forced tool + stream | manual/live | `scripts/` smoke com chave de teste | ❌ |
| HAIKU-02 | classificador (crédito, 402, spend cap, 401, 429 retry-after, 529) e troca de chave sem byte escrito | unit | `-run TestKeyPool` | ❌ Wave 0 |
| HAIKU-02 | estado no Redis com TTL (miniredis) | unit | `-run TestKeyPoolRedisState` | ❌ |
| HAIKU-03 | cascata ①→②→③ por status (429/5xx/529) com fakes httptest, streaming e não-streaming; último salto devolve o erro real | integration | `-tags integration -run TestHaikuChain` | ❌ |
| HAIKU-03 | migration 0041 up/down + bump `db.Down` | integration | `-run 'Migration0041|Migration0026|Migration0029'` | ❌ / ✅ (bump) |
| HAIKU-04 | custo: `usage.cost` OpenRouter; cálculo Anthropic com cache e degrau >100k | unit | `-run 'TestUsageCost|TestHaikuLongContextPrice'` | ❌ |
| HAIKU-05 | effort: tenant > env; cliente ignorado; `reasoning` injetado no ② | unit | `-run TestEffortPrecedence` | ❌ |
| HAIKU-06 | chave sensível (`telefonia`) com pod off → 503 sem tocar ①/②/③ | integration | `-run TestSensitiveNeverExternalHaiku` | ❌ (há `fallback_routing_test.go` como molde) |
| HAIKU-07 | prod: 7 aliases 200 com `X-` upstream esperado; billing com cache; 0 prefill 400 | manual/live | queries de `audit_log`/`billing_events` | — |
| HAIKU-08 | Director TTS: mp3 forçado, voz mapeada, model reescrito; billing chars | unit | `-run TestOpenRouterTTSDirector` | ❌ |
| HAIKU-09 | `/admin/economy`: % local por tokens, custo 3060, economia a preço Haiku | unit + vitest | `go test ./internal/admin/ -run TestEconomy` ; `npx vitest run` | ✅ parcial (economy tests existem) |

### Sampling Rate
- **Por commit:** quick run + `gofmt -l .`
- **Por wave:** suite completa incluindo `-tags integration`
- **Gate da fase:** suite verde + smoke live dos 7 aliases + 24 h de `audit_log` sem 400 de prefill/sampling antes de `/gsd:verify-work`.

### Wave 0 Gaps
- [ ] `gateway/internal/proxy/testdata/anthropic/` — fixtures reais (capturar com chave de teste ou da doc de streaming)
- [ ] fake Anthropic server (httptest) reutilizável em unit e integration (status/erros configuráveis)
- [ ] `anthropic_translate_*_test.go`, `anthropic_keypool_test.go`, `chat_retryable_status_test.go`
- [ ] integration `haiku_chain_test.go` (molde: `fallback_routing_test.go`, `model_alias_test.go`)
- [ ] bump `db.Down(n)` em `migration_0026_test.go` e `migration_0029_test.go`

## Security Domain

### ASVS aplicável
| Categoria | Aplica | Controle |
|---|---|---|
| V2 Autenticação | sim (upstream) | chaves só em env do stack 38; `x-api-key` injetada pelo transporte; nunca logada (fingerprint sha256[:8]) |
| V3 Sessão | não | — |
| V4 Controle de acesso | sim | RES-08 via `api_keys.data_class`; admin `/admin/anthropic/keys` só leitura e owner/admin-key |
| V5 Validação de entrada | sim | tradutor tolerante (`json.RawMessage`), limites de body existentes (25 MiB), rejeição limpa de JSON inválido |
| V6 Criptografia | não (TLS do cliente HTTP padrão) | — |
| V7 Logs | sim | não logar body (padrão R12 dos directors); não logar chaves |

### Ameaças
| Padrão | STRIDE | Mitigação |
|---|---|---|
| Vazamento de chave Anthropic em log/audit/erro | Information disclosure | remover `x-api-key` de qualquer dump; erros do upstream não contêm a chave; testes que verificam ausência |
| Payload sensível sai para externo | Information disclosure | gate RES-08 existente + teste de integração com chave sensível |
| Override de schedule/shed contornando pinagem | Elevation/bypass | teste cobrindo `byRoleTier` com 3 tier-1; corrigir para escolher o 1º por prioridade se necessário |
| Consumo indevido dos créditos (chave usada fora do gateway) | Repudiation | workspace dedicado por org no Console + spend limit de workspace [CITED: api-credits "give it its own workspace and set spend limit"] |
| Segredos no git (commit de docs/scripts) | Information disclosure | placeholders; Push Protection já ativa (memória) |

## Sources

### Primárias (HIGH)
- platform.claude.com/docs/en/models/overview — IDs, effort default, contexto (fetch 2026-10-14)
- platform.claude.com/docs/en/models/haiku-5-5/overview — preço por faixa, 512 tokens mín. de cache, thinking ligado à conta
- platform.claude.com/docs/en/models/haiku-5-5/migration-guide — sampling 400, prefill 400, tool_choice forçado aceito sem thinking, `output_config.effort`
- platform.claude.com/docs/en/about-claude/pricing — tabela completa e "Long context pricing" (degrau >100k inclui cache)
- platform.claude.com/docs/en/about-claude/api-credits-for-subscribers — créditos por ciclo, mensagem de saldo, orgs, rate tier
- platform.claude.com/docs/en/api/rate-limits — tiers, spend cap 429 `enforced_spend_limit_reached`, cache-aware ITPM, headers
- platform.claude.com/docs/en/api/errors — códigos, prefill, thinking, forced tool
- platform.claude.com/docs/en/build-with-claude/effort — níveis e recomendação Haiku 5.5
- platform.claude.com/docs/en/build-with-claude/prompt-caching — cache automático top-level, isolamento por org/workspace, campos de usage
- platform.claude.com/docs/en/build-with-claude/streaming — eventos SSE, usage cumulativo, erro pós-200
- platform.claude.com/docs/en/build-with-claude/mid-conversation-system-messages — suporte Haiku 5.5, regras de posição
- platform.claude.com/docs/en/api/openai-sdk — limitações do compat layer
- Context7 `/websites/openrouter_ai` — `GET /api/v1/activity` (management key), `GET /api/v1/generation`, usage com `cost`/`cached_tokens`/`cache_write_tokens`, TTS `/api/v1/audio/speech`
- Código: `gateway/internal/proxy/{dispatcher,openrouter_director,errors,transport,overcontext,interceptor_usage,tts}.go`, `internal/upstreams/{loader,probe}.go`, `internal/admin/economy.go`, `cmd/gateway/main.go`, `db/migrations/0007,0010,0012,0026,0029,0038`, `db/queries/billing.sql`, `ops/vast-3060/unified3060.py`
- Banco prod `bd_ai_gateway` (read-only): `billing_events`, `audit_log(_content)`, `api_keys`, `tenants`, `upstreams`, `prices`, `goose_db_version`
- `GET https://openrouter.ai/api/v1/models?output_modalities=speech` (lista e preços TTS)
- `https://proxy.golang.org/github.com/anthropics/anthropic-sdk-go/@latest` (v1.80.0)

### Secundárias (MEDIUM)
- openrouter.ai/docs/use-cases/usage-accounting (WebFetch) — usage sempre presente
- Memórias do projeto citadas no texto (fatos medidos em sessões anteriores)

### Terciárias (LOW)
- Buscas web sobre preços de TTS (gpt-4o-mini-tts ~US$0,015/min; Gemini 2.5 Flash TTS US$0,50/US$10 por M; ElevenLabs Flash v2.5 US$0,04/1k chars direto) — não usadas como base de decisão

## Metadata

**Confiança:**
- Stack/adaptador: MÉDIA-ALTA — limitações do compat layer e regras do Haiku verificadas na doc; design do tradutor é recomendação.
- Arquitetura da cadeia: ALTA — baseada em leitura do dispatcher/loader e constraints do banco.
- Pool de chaves: MÉDIA — mensagem e regras verificadas; status HTTP do saldo zerado não documentado (H1).
- Preços: ALTA — tabela oficial.
- TTS: MÉDIA — endpoint e preços verificados; qualidade PT-BR não medida.
- Pitfalls: ALTA para 1, 2, 7, 8, 9 (evidência em produção/código); MÉDIA para 4, 5.

**Data:** 2026-10-14 · **Válido até:** 2026-10-28 (Haiku 5.5 lançado há 7 dias; OpenRouter e créditos Max são recentes — revalidar preços e comportamento antes do rollout)
