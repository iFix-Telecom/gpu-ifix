# ifix-ai-gateway

## What This Is

Plataforma central de IA da Ifix Telecom: um gateway HTTP que serve LLM, transcrição (STT), TTS e embeddings para todas as aplicações da empresa (ConverseAI v4, Chat Ifix, Telefonia/NextBilling, Cobranças, Campanhas, voice-api). Roda em GPU própria (Vast.ai — primary shape LOCKED Phase 06.8: 2×RTX 3090 single-pod, allowlist hosts 43803/55158, cap $0.60/h; 5090 32 GB validado como alternate UAT 06.6 #18/06.7) com failover automático para OpenRouter/OpenAI e spin-up emergencial paralelo quando a primária cai ou satura.

## Core Value

**Nenhuma aplicação da Ifix sente quando a GPU cai.** Failover deve ser invisível para o cliente final — chamadas continuam respondendo dentro do SLO, mesmo durante incidentes ou picos de demanda.

## Requirements

### Validated

- [x] **Pod image (Phase 1):** llama-server CUDA 27B + speaches Whisper + BGE-M3 + onstart.sh com bridge de healthcheck — validado em 01-VERIFICATION.md
- [x] **Gateway HTTP em Go (Phase 2):** Elysia-style routes `/v1/chat/completions`, `/v1/embeddings`, `/v1/audio/transcriptions` com proxy + auth multi-tenant (API key por aplicação) — validado em 02-VERIFICATION.md (4/5 SC PASS, SC-5 live deploy aguarda)
- [x] **Resilience & failover chain (Phase 3):** Circuit breakers per-upstream, fallback chain local→OpenRouter (Novita)→OpenAI, hot-reload <1s, sensitive-tenant block, tool-call no-retry — validado em 03-VERIFICATION.md (4/5 SC PASS, SC-1 live pod-kill em 03-HUMAN-UAT.md)

### Active

- [ ] Health-check periódico nos três serviços + circuit breaker (abre quando N falhas seguidas ou latência acima de threshold) — _coberto pelos breakers Phase 3, mas health probe ainda precisa de cleanup_
- [ ] Failover automático para OpenRouter (Qwen 3.5 27B via Novita/Fireworks — versão primary upgraded to Qwen 3.6 27B em Phase 06.6 mas OpenRouter tier-1 ainda em 3.5; minor drift aceito até OpenRouter publish Qwen 3.6), OpenAI Whisper API e OpenAI text-embedding-3-small quando GPU primária cai _(Phase 3 entregou; live UAT pendente)_
- [ ] Load shedding: detectar saturação por utilização de GPU/VRAM e desviar overflow para OpenRouter sem esperar falha real
- [ ] Spin-up emergencial paralelo de pod Vast.ai quando primária cai (auto, com guardrails: limite de preço/h e máximo 1 pod emergencial ativo)
- [ ] Cutback automático para primária quando ela voltar saudável por 5 min, com grace period de 5 min antes de desligar pod emergencial
- [ ] Modos de operação configuráveis por app: 24/7 (GPU sempre ligada) OU pico/vale (08–22h local, fora do horário roteia para OpenRouter)
- [ ] Dashboard próprio com métricas: latência, error rate, custo, requests por app, status do failover
- [ ] Alertas críticos via WhatsApp/email (queda da GPU, ativação do failover, spin-up emergencial, ultrapassagem de quota)
- [ ] Integração nas apps clientes v1: ConverseAI v4 (chat + agents), Chat Ifix (transcrição), Telefonia/NextBilling (transcrição), Cobranças, Campanhas, voice-api
- [ ] Persistência em Postgres compartilhado (Digital Ocean) para config, API keys, quotas, auditoria, billing
- [ ] Redis para estado quente: rate-limit, circuit breaker state, métricas curtas
- [ ] Deploy via Docker Compose em VPS dedicada (4 vCPU)

### Out of Scope

- ~~TTS rodando em GPU — voice-api continua em CPU por ora~~ — **DONE Phase 06.7**: Chatterbox Multilingual TTS no pod GPU (~4 GB VRAM); Piper CPU permanece como tier-1 fallback degradado
- Modelos diferentes do Qwen 27B family (Llama 3.3, Mixtral, etc.) — fixar Qwen para minimizar drift. **Primary upgraded Qwen 3.5 → Qwen 3.6 27B em Phase 06.6** (Wave 0 SPIKE 06.6-SPIKE-qwen3.6-jinja.md Round 3 PASS via GGUF-embedded chat_template peg-native parser); OpenRouter tier-1 fallback ainda em Qwen 3.5 27B (não publicado 3.6 em OpenRouter ainda — drift aceito como minor 3.5↔3.6 same family)
- ElevenLabs ou TTS premium — não está no escopo desta milestone
- Coqui XTTS-v2 ou voice cloning — descartado por consumir VRAM que comprometeria Qwen
- Kubernetes / Docker Swarm — Docker Compose simples atende a complexidade desta etapa
- Aprovação manual para spin-up emergencial — automatizado para garantir failover invisível
- Dashboards Grafana/Prometheus — dashboard próprio (Next.js) é suficiente para v1

## Context

**Ecossistema atual:**
- Empresa opera VPS dev (esta máquina, 178.156.150.21) e VPS prod (5.161.207.105), ambas via Portainer com deploy automático via webhook GitHub
- Várias aplicações já em produção consomem IA externa hoje (Anthropic, OpenAI direto): ConverseAI v4 (chat + agents Python), Chat Ifix (transcrição de áudios em conversas), Telefonia/NextBilling (transcrição de ligações), Cobranças/Campanhas (LLM para personalização), voice-api (TTS rodando em CPU hoje)
- Padrão da empresa: TypeScript + Bun para apps, Python para agents AI, Postgres + Redis para infra
- Postgres compartilhado em Digital Ocean já em uso pelas apps existentes

**Documento de partida (histórico — pré-Phase 06.8):**
- `ConverseAI_GPU_Stack_Guide.docx` (raiz do projeto) detalha o setup base original (single-pod RTX 4090 com Qwen + Whisper + BGE-M3, llama.cpp + FastAPI). Documento permanece como referência histórica; arquitetura final divergiu significativamente — vide Phase 06.6 (custom primary-pod image supervisord 4 children) + Phase 06.7 (TTS Chatterbox na GPU + embed 24/7 CPU off-pod) + Phase 06.8 (2×RTX 3090 single-pod final shape).

**Estimativas de VRAM (Phase 06.8 final, stack llm + stt + tts + dcgm):**
- Qwen 3.6 27B Q4_K_M: ~16 GB (MinIO key `qwen3.6-27b-Q4_K_M/v1.0.0/model.gguf` 17.106 GB raw, sha256 `a7cbd3ec…`)
- Whisper large-v3 (GPU offload): ~3 GB
- Chatterbox Multilingual TTS: ~4 GB
- BGE-M3 NÃO está mais no pod (Phase 06.7 D-03: moved off-pod para CPU Infinity 24/7 em n8n-ia-vm:7997)
- KV cache + overhead: ~2-3 GB
- Total: ~25 GB. NÃO cabe em 4090 24 GB (UAT 06.6 #16 CUDA OOM confirmado). Cabe em 2×RTX 3090 48 GB (Phase 06.8 split com auto-layer balancing) ou 5090 32 GB single-card.

**Custos de referência (Maio 2026 — atualizado pós-discovery EU 5090 inventory):**
- Vast.ai 2×RTX 3090 (primary final shape Phase 06.8): cap $0.60/h
- Vast.ai 5090 (alternate, validado UAT 06.6 #18): ~$0.33-0.77/h observado EU
- OpenRouter Qwen 3.5 27B via Fireworks: pay-per-token (tier-1 fallback; minor drift vs primary Qwen 3.6 — see Out of Scope)
- OpenAI Whisper API: ~$0,006/min de áudio (tier-1 fallback)
- Infinity multilingual-e5-large CPU n8n-ia-vm: $0 (host compartilhado)

**Justificativa Vast.ai como primária (não RunPod Secure):**
- Custo significativamente menor que RunPod Secure
- Aceito porque: failover robusto + spin-up emergencial paralelo cobrem instabilidade do host privado típica da Vast.ai
- Phase 06.6 catalogou broken-CDI hosts via PRIMARY_VAST_MACHINE_BLOCKLIST (operator-curated env)

## Constraints

- **Tech stack — Gateway**: Go — escolhido para performance de proxy alta e binário estático leve no deploy de 4 vCPU; difere do padrão TS da empresa, mas natural para gateway
- **Tech stack — IA (Phase 06.8 final)**: Qwen 3.6 27B Q4_K_M via llama.cpp `b9191` server-cuda (NÃO llama-cpp-python — Phase 06.6 D-07/D-24), faster-whisper 1.1.1 (silero_vad_v5 vendored) via Speaches 0.9.0rc3, Chatterbox Multilingual TTS (MIT, zero-shot voice clone via S3 WAV refetch — NO `.pt`), Infinity multilingual-e5-large CPU off-pod 24/7. Original BGE-M3 + Qwen 3.5 + llama-cpp-python design substituído.
- **Tech stack — Persistência**: Postgres compartilhado Digital Ocean (schema dedicado) + Redis — reuso de infra
- **Tech stack — Deploy**: Docker Compose + Portainer com webhook GitHub — segue padrão converseai-v4
- **Hardware (Phase 06.8 final)**: 2×RTX 3090 single-pod (allowlist 43803/55158, cap $0.60/h) — ~60% mais barato que 5090 single + maior depth de inventory Vast. 5090 32 GB validado como alternate. **NÃO usar 4090 24 GB** — stack full (llm+stt+tts+kv) ~25 GB excede budget (UAT 06.6 #16 CUDA OOM confirmado)
- **Compatibilidade**: APIs do gateway devem ser OpenAI-compatible (`/v1/chat/completions`, `/v1/embeddings`, `/v1/audio/transcriptions`) para que apps clientes só troquem `base_url` + `api_key`
- **Failover invisível**: requests não devem falhar para o cliente final; degradar latência é aceitável, perder request não
- **Multi-tenant**: cada app autentica com API key própria; quotas e contabilização de custo separadas por app
- **Guardrails operacionais**: cap preço/hora Vast.ai = $0.60/h (Phase 06.8 final, 2×3090 EU inventory), máximo 1 pod emergencial ativo simultâneo, alerta diário de uso acumulado, host blocklist via PRIMARY_VAST_MACHINE_BLOCKLIST
- **Auto-shutdown**: pod emergencial desliga sozinho após primário ficar saudável 5 min + 5 min de grace period

## Phase 06.9: Tier-1 Stack Final

Per-upstream model targets (post-Phase-06.9, schema-driven via `ai_gateway.model_aliases` composite PK `(alias, upstream_name)`):

| Upstream (name) | Role | Tier | Provider | Model slug | Notes |
|-----------------|------|------|----------|------------|-------|
| `local-llm` | llm | 0 | own pod (llama.cpp) | `qwen` (passthrough alias) | tier-0 proxies pass body byte-identical; resolver not consulted |
| `openrouter-chat` | llm | 1 | OpenRouter | `deepseek/deepseek-v4-flash:nitro` (canonical `deepseek/deepseek-v4-flash-20260423`; updated by migration 0027 — was `qwen/qwen3.5-27b`) | provider chosen by OpenRouter (no pin; was Novita-pinned for Qwen); SiliconFlow observed in initial probe; `:nitro` = OpenRouter high-perf routing variant |
| `local-stt` | stt | 0 | own pod (Speaches) | `whisper` (passthrough alias) | tier-0 pass-through |
| `openai-whisper` | stt | 1 | OpenAI | `whisper-1` | multipart/form-data; director rewrites `model` part value byte-identical to audio file part |
| `local-embed` | embed | 0 | Infinity (n8n-ia-vm CPU 24/7 off-pod) | `bge-m3` (passthrough alias) | tier-0 pass-through |
| `openai-embed` | embed | 1 | OpenAI | `text-embedding-3-small` + `dimensions=1024` (BGE-M3 parity invariant) | director injects `dimensions` |

**URL convention** (D-07): `UPSTREAM_LLM_OPENROUTER_URL` base ends at `/api` (NOT `/api/v1`). The gateway's `BuildDirector` preserves `r.URL.Path=/v1/chat/completions` from the inbound request, so the env var MUST NOT include `/v1` — concat would otherwise produce `/v1/v1`. Phase 06.9 added boot-time fail-fast validation: an `UPSTREAM_*_URL` value ending in `/v1` rejects config load with an explicit error message.

**Resolver lookup**: schema-driven via `ai_gateway.model_aliases` composite PK `(alias, upstream_name)`. Schema is the default for all instances (DB row → resolver in-memory map → director consults at every request).

**Operator override paths — BOTH supported coequally per D-06** (env-override-wins is NOT deprecated):

1. **Schema row (multi-instance):** `gatewayctl model-alias set --alias=<X> --upstream=<Y> --target=<Z>` (added by Plan 04). Persists to Postgres; all gateway instances pick it up on next resolver Refresh (≤1s via NOTIFY).
2. **Env var (per-instance):** `export UPSTREAM_<UPPER_UPSTREAM>_MODEL=<slug>` + restart container. Single-instance escape hatch — `UPSTREAM_LLM_OPENROUTER_MODEL`, `UPSTREAM_STT_OPENAI_MODEL`, `UPSTREAM_EMBED_OPENAI_MODEL` are the curated env-var keys per `upstreamEnvVarMap`.

**BLOCKER-1 / D-06 env-override-wins precedence**: per D-06, `UPSTREAM_<UPPER_UPSTREAM>_MODEL` env vars take PRECEDENCE over the schema row at resolver-lookup time. The env var is the per-instance operator escape hatch; the schema row is the multi-instance-consistent default. Both are supported permanently — env is NOT deprecated. Empty-string env values are treated as unset (schema wins). The precedence chain is:

1. Env override via `upstreamEnvVarMap` — env wins when non-empty.
2. Schema row in `model_aliases` keyed on `(alias, upstream_name)`.
3. Passthrough — alias returned unchanged (safety net for new upstreams not yet seeded).

**Operator breaker control**: `gatewayctl breaker force-open --upstream=<X> --ttl=<Yms>` writes Redis key `gw:breaker:force:{name}` with mandatory TTL (≤300s enforced at CLI). Write is audit-logged. Breaker FSM honors the force-override over observation-driven state; expiry restores observation. Plan 06 HUMAN-UAT uses this lever to drive failover scenarios deterministically.

**Phase 06.9 closure (2026-05-24)**: Phase 06.9 closed the per-upstream model rewrite gap end-to-end; SC-1 cascade closes Phase 02/03/05 deferrals. Integration test suite hardened with body-capturing mocks + selective-reject mocks (R13) + R6 Whisper edge-case coverage + R4 local-tier byte-identical assertions + D-06 env-override-wins end-to-end coverage.

**Phase 06.9 follow-up (2026-05-24)** — migration 0027 swaps OpenRouter chat tier-1 target from `qwen/qwen3.5-27b` (Novita-pinned) to `deepseek/deepseek-v4-flash:nitro` (OpenRouter chooses provider — SiliconFlow observed in probe). Decision rationale (operator authorized): alias `qwen` stays (transparent to clients — fallback swap is a deployment concern, not an API contract change); provider pin lifted (Q3 — OpenRouter routes); pricing not migrated (0015 row for `qwen/qwen3.5-27b` historically locked; tier-1 billing not yet wired — separate concern). RUNBOOK-FAILOVER.md retains historical Qwen + Novita references; treat them as historical until a dedicated doc-refresh PR lands.

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Go como linguagem do gateway | Performance, binário estático, baixo overhead em VPS 4 vCPU | — Pending |
| Vast.ai como GPU primária (não RunPod Secure) | Custo ~50% menor; failover paralelo cobre instabilidade | — Pending |
| ~~Qwen 3.5 27B fixo (LLM primário e fallback OpenRouter)~~ Primary upgraded Qwen 3.5 → **Qwen 3.6 27B Q4_K_M** em Phase 06.6 (Wave 0 SPIKE PASS; chat_format=peg-native via GGUF embedded chat_template); OpenRouter tier-1 ainda em Qwen 3.5 27B (Fireworks via OpenRouter) — minor drift aceito | DONE Phase 06.6 (primary); tier-1 lag pending OpenRouter Qwen 3.6 publication |
| TTS via Chatterbox Multilingual na GPU do pod (~4 GB VRAM) com Piper CPU como tier-1 fallback | Decisão original (TTS CPU only) revisada na Phase 06.7: GPU shape Phase 06.8 (2×3090 48 GB ou 5090 32 GB) tem headroom; Chatterbox MIT + zero-shot voice cloning durável via S3 WAV refetch. voice-api Piper continua como fallback degradado | DONE (Phase 06.7, UAT 06.7 5/6 PASS) |
| Embed (BGE-M3 → multilingual-e5-large) movido pra CPU 24/7 em n8n-ia-vm (off-pod) | Phase 06.7 D-03: pod só roda peak; embed precisa servir RAG 24/7. multilingual-e5-large CPU Infinity. Reverte parcialmente em SEED-002 (embed volta pro pod GPU como tier-0, CPU vira tier-1) | DONE Phase 06.7; reverso planejado SEED-002 |
| Detecção de saturação por GPU util/VRAM (não queue depth) | Mais simples, sinal direto, não exige instrumentação fina dos servidores de modelo | — Pending |
| Postgres compartilhado Digital Ocean | Reuso de infra existente; schema dedicado para isolar | — Pending |
| Spin-up emergencial automático com guardrails (não aprovação manual) | Failover invisível exige autonomia; cap de preço/h e 1-pod-ativo previnem desperdício | — Pending |
| Dashboard próprio Next.js + WhatsApp/email (não Grafana) | Menos infra para manter; alertas críticos chegam onde a equipe está | — Pending |

## Evolution

This document evolves at phase transitions and milestone boundaries.

**After each phase transition** (via `/gsd-transition`):
1. Requirements invalidated? → Move to Out of Scope with reason
2. Requirements validated? → Move to Validated with phase reference
3. New requirements emerged? → Add to Active
4. Decisions to log? → Add to Key Decisions
5. "What This Is" still accurate? → Update if drifted

**After each milestone** (via `/gsd-complete-milestone`):
1. Full review of all sections
2. Core Value check — still the right priority?
3. Audit Out of Scope — reasons still valid?
4. Update Context with current state

---
*Last updated: 2026-04-20 after Phase 3 (resilience + fallback chain) complete*
