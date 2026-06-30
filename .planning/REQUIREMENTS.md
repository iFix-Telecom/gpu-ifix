# Requirements: ifix-ai-gateway

**Defined:** 2026-04-17
**Core Value:** Nenhuma aplicação da Ifix sente quando a GPU cai. Failover invisível; chamadas continuam respondendo dentro do SLO mesmo durante incidentes ou picos de demanda.

## v1 Requirements

Requirements para o release inicial. Cada item mapeia para um phase do roadmap.

### Infra — Inference Pod (GPU)

- [x] **POD-01**: Imagem Docker `ghcr.io/ifixtelecom/ifix-ai-pod` construída e publicada com llama.cpp (llama-server CUDA), Speaches (Whisper large-v3), Infinity (BGE-M3) e dcgm-exporter (Phase 1)
- [x] **POD-02**: Imagem magra (~2 GB) publicada em `ghcr.io/ifixtelecom/ifix-ai-pod`; weights (Qwen 3.5 27B Q4_K_M GGUF, Whisper large-v3, BGE-M3) baixados do MinIO Ifix via `onstart.sh` no boot do pod — cold-start ≤5 min (decisões D-01, D-02, D-04; integridade validada via SHA-256 por D-05) (Phase 1 — runtime validation pending HUMAN-UAT)
- [x] **POD-03**: Health-bridge no pod expõe `/health` por modelo (LLM, STT, embed) com verificação real (latency test), não só container-running (Phase 1)
- [x] **POD-04**: Pod mede e expõe VRAM total, livre, por processo via dcgm-exporter na porta 9400 (Phase 1)
- [x] **POD-05**: Template Jinja patched para Qwen 3.5 27B (tool-calling funcional, sem bug de role "developer") validado em smoke-test (Phase 1 — runtime validation pending HUMAN-UAT)
- [x] **POD-06**: `max_model_len=16384` enforçado no llama.cpp para conter crescimento de KV cache (Phase 1)
- [x] **POD-07**: Smoke-test sob carga (2 chats concorrentes 8k tokens + 1 Whisper longa) confirma margem ≥3 GB VRAM sob pico (Phase 1 — runtime validation pending HUMAN-UAT)

### Gateway — Core HTTP (Go)

- [x] **GW-01**: Gateway Go roda como binário único com `chi v5` + `httputil.ReverseProxy` (streaming compatible) + `slog`
- [x] **GW-02**: Expõe `POST /v1/chat/completions` OpenAI-compatible (incluindo streaming SSE com `FlushInterval: -1`)
- [x] **GW-03**: Expõe `POST /v1/embeddings` OpenAI-compatible
- [x] **GW-04**: Expõe `POST /v1/audio/transcriptions` OpenAI-compatible (multipart upload)
- [x] **GW-05**: Expõe `GET /health` (gateway saudável) e `GET /v1/health/upstreams` (status por upstream: LLM local, STT local, embed local, OpenRouter, OpenAI-Whisper, OpenAI-embed)
- [x] **GW-06**: Pass-through de tool/function calling no formato OpenAI
- [x] **GW-07**: Model alias mapping (cliente pede `model: "qwen"`, gateway resolve para versão atual)
- [x] **GW-08**: Request ID único (UUID) emitido em todo request e echoed em `X-Request-ID` header + logs estruturados
- [x] **GW-09**: Deploy Docker Compose + Portainer + webhook GitHub no padrão Ifix (VPS dedicada 4 vCPU)
- [x] **GW-10**: Schema Postgres inicial (dedicated schema no DO compartilhado) com tabelas `api_keys`, `tenants`, `audit_log`, `billing_events`, `usage_counters`; migrations versionadas

### Multi-tenant — Auth, Quotas, Cost Attribution

- [x] **TEN-01**: API key auth em `Authorization: Bearer <key>` ou `X-API-Key`; lookup em Postgres com cache Redis
- [x] **TEN-02**: Cada API key pertence a um tenant (ConverseAI, Chat Ifix, Telefonia, Cobranças, Campanhas, voice-api) com campo `data_class` (`normal` | `sensitive`)
- [x] **TEN-03**: Rate limiting por API key (RPS e requests/min) usando Redis Lua atomic
- [x] **TEN-04**: Quota diária e mensal por tenant (tokens de LLM, minutos de áudio, requests de embed); bloqueio ao atingir limite — audio/embed metering wired Phase 16 (`applyAudioEmbedUsage` producer, 7 proxies); quota-trip tests green; live-UAT DB-populate deferred (human_needed)
- [x] **TEN-05**: Modo de operação configurável por tenant: `24/7` (sempre local primário) OU `peak` (08-22h local, fora de horário OpenRouter)
- [x] **TEN-06**: Token counting + custo calculado por request e gravado em `billing_events` (append-only)
- [x] **TEN-07**: Report de custo e uso por tenant acessível via endpoint admin
- [x] **TEN-08**: Error format consistente com OpenAI (`{error: {message, type, code}}`) para 401, 403, 429, 5xx
- [x] **TEN-09**: Idempotency key support em endpoints de chat (cliente envia `Idempotency-Key` header)

### Resiliência — Circuit Breakers, Retries, Fallback

- [x] **RES-01**: Circuit breaker (`sony/gobreaker v2`) por upstream: local-LLM, local-STT, local-embed, OpenRouter, OpenAI-Whisper, OpenAI-embed
- [x] **RES-02**: Retry com exponential backoff (`cenkalti/backoff v5`) para requests não-streaming; NÃO retry após primeiros bytes enviados ao cliente em streaming
- [x] **RES-03**: Fallback chain ativa automaticamente quando circuit abre: local-LLM → OpenRouter (Qwen 3.5 27B); local-STT → OpenAI Whisper; local-embed → OpenAI text-embedding-3-small
- [x] **RES-04**: Health-check proativo a cada 10s em todos os upstreams; resultado atualiza estado no Redis
- [x] **RES-05**: Política de streaming em failover documentada: fail-fast com 503 + cliente espera retry end-to-end (não re-inject chunks)
- [x] **RES-06**: Tool calls marcados com idempotency key separado; gateway NUNCA retry de tool call; agent layer trata
- [x] **RES-07**: Context window normalization entre local (16k) e OpenRouter (32k) para evitar truncation surpresa; política: usar menor dos dois
- [x] **RES-08**: Apps com `data_class: sensitive` (telefonia, cobranças) usam política alternativa em failover: enfileirar (com retry curto) ao invés de enviar a provider externo (mitigação LGPD)

### Load shedding — Saturation-aware routing

- [x] **LSH-01**: Inflight counter por upstream no gateway (Go atomic) incrementa em pré-dispatch, decrementa em response
- [x] **LSH-02**: Signal composto para saturação local: inflight_count > N OU P95 latência (janela 30s) > threshold OU VRAM (via dcgm-exporter) > 21 GB
- [x] **LSH-03**: Histerese configurada: só volta para local após sinal ficar abaixo do threshold por 60s seguidos (previne flapping)
- [x] **LSH-04**: Thresholds (inflight, P95, VRAM) configuráveis via Postgres e reloadable sem restart
- [x] **LSH-05**: Overflow routing direciona excedente para OpenRouter enquanto local se recupera, mantendo outros tenants atendidos

### Auto-provisioning — Vast.ai Emergency Pod

- [x] **PRV-01**: Cliente REST Vast.ai em Go (search offers, create instance, destroy instance, get status) — sem Go SDK, implementação direta
- [x] **PRV-02**: State machine de pod emergencial (HEALTHY → DEGRADED → FAILED_OVER → EMERGENCY_PROVISIONING → EMERGENCY_ACTIVE → RECOVERING → COOLDOWN → OFF_HOURS → MAINTENANCE) persistida no Redis
- [x] **PRV-03**: Leader-election via Redis distributed lock (`go-redsync/redsync`) garante single-reconciler — só o leader avança o FSM
- [x] **PRV-04**: Trigger de provisioning quando primário está em FAILED_OVER por X segundos (configurável)
- [x] **PRV-05**: Guardrails enforçados no state machine: preço máx $0,40/h (rejeita ofertas acima), máximo 1 pod emergencial ativo simultâneo, orçamento mensal com alerta
- [x] **PRV-06**: Provisioning filtra hosts Vast.ai por reliability ≥99% e network capability adequada; usa imagem `ghcr.io/ifixtelecom/ifix-ai-pod:vX.Y` com onstart script
- [x] **PRV-07**: Readiness check do pod emergencial usa `/health` por modelo (não só container running) antes de adicionar ao pool ativo
- [x] **PRV-08**: Cutback automático: primário saudável por 5 min → gateway roteia tráfego de volta para primário; +5 min grace period com pod emergencial idle → destroi pod emergencial
- [x] **PRV-09**: Cancelamento in-flight: se primário recupera durante EMERGENCY_PROVISIONING, cancela criação do pod (previne spin desperdiçado)
- [x] **PRV-10**: Audit log completo de cada ciclo de provisioning (trigger, oferta aceita, preço, duração, custo total, motivo shutdown)

### Observabilidade — Dashboard + Alerts

- [x] **OBS-01**: Gateway expõe `/admin/metrics` JSON com latência (P50/P95/P99) por rota e upstream, error rate, inflight, saturação, FSM state
- [x] **OBS-02**: Gateway expõe `/metrics` Prometheus format (prometheus/client_golang) com label budget controlado (cardinality ≤ 10k series)
- [x] **OBS-03**: Dashboard Next.js 15 (shadcn + Recharts, padrão converseai-v4) exibe: latência por tenant, error rate, custo diário/mensal por tenant, status FSM, histórico de incidentes
- [x] **OBS-04**: Alertas com severity tiers: `critical` (GPU primary down > 30s, pod emergencial failed, quota tenant 90%) → WhatsApp + email; `warning` (saturação > 10min, erro rate > 5%) → email; `info` → dashboard só
- [x] **OBS-05**: WhatsApp via provider Ifix (Evolution API ou equivalente a confirmar) e email via Brevo SMTP (padrão Ifix)
- [x] **OBS-06**: Rate-limit de alertas (deduplicação em janela de 5 min) para prevenir alert fatigue
- [x] **OBS-07**: Audit log append-only em `audit_log` Postgres para: mudança de FSM, ativação/desativação de tenant, spin-up/shutdown emergencial, ajuste de threshold
- [x] **OBS-08**: Sentry integration (padrão Ifix) com redaction de API keys e payloads sensíveis
- [x] **OBS-09**: Dashboard exibe painel de **Economia** (phantom vs Vast) — destrava o painel hoje deferred. Gateway soma `cost_local_phantom_brl` gateway-wide (todos tenants) por período e cruza com custo real Vast (`primary_lifecycles.total_cost_brl` + accrual `accepted_dph × horas`). UI mostra 3 números lado a lado: **líquido R$** (phantom − Vast), **recorte janela pod-up** (só horas com pod UP), **multiplicador ROI** (phantom evitado por R$1 de GPU), + gráfico de economia como **série temporal real** (eixo X = tempo). Assume preço phantom confiável (daily timer OpenRouter+forex)
- [x] **OBS-10**: Página `/incidents` (audit log) ganha **filtro de data + busca + total count** — hoje só tem pager limit/offset, sem range nem COUNT

### Integrations — Client Apps

- [ ] **INT-01**: ConverseAI v4 (agents Python + api Elysia) apontando para gateway via `base_url` + API key; rollback documentado
- [ ] **INT-02**: Chat Ifix migrado para usar gateway em transcrição Whisper de áudios
- [ ] **INT-03**: Telefonia/NextBilling migrado para gateway em transcrição de ligações (com `data_class: sensitive`)
- [ ] **INT-04**: Cobranças + Campanhas migrados para gateway em LLM (personalização) e embedding (regras inteligentes)
- [ ] **INT-05**: voice-api mantém TTS em CPU local mas usa gateway quando precisar de LLM (ex: geração de roteiros)
- [ ] **INT-06**: Cada integração tem smoke-test de produção + rollback plan (reversão para config antiga em <5min)

### Produção — Hardening Final

- [x] **PRD-01**: Load test com 3 tenants simultâneos usando perfil real de produção (não sintético); baseline de P95 e capacidade
- [x] **PRD-02**: Chaos test: matar pod primário durante carga, medir tempo até recovery completo (meta: invisível para cliente com streaming fail-fast + retry automático)
- [x] **PRD-03**: Chaos test: simular OpenRouter indisponível durante failover, validar comportamento (enfileirar, retry OpenAI tier 3, ou falha controlada)
- [x] **PRD-04**: Runbook de incidentes documentado (detecção → diagnóstico → rollback → postmortem)
- [x] **PRD-05**: Revisão LGPD concluída antes de ativar tenant `sensitive` (Telefonia, Cobranças) em produção — evidência de base legal, disclosure de sub-processadores (OpenAI, OpenRouter, Vast.ai)
- [x] **PRD-06**: Dashboard acessível por admin Ifix com autenticação SSO ou Better Auth (a confirmar)
- [x] **PRD-07**: DNS `gateway.ifix.com.br` configurado via Cloudflare; TLS/HTTPS end-to-end

### Dashboard User Management (Phase 13 — derived in plan-phase 2026-06-15)

- [x] **UM-01**: Self-service change-password (`/settings`, `authClient.changePassword`, exige senha atual; NÃO admin, NÃO auditado)
- [x] **UM-02**: Instalar admin plugin (`adminRoles:["owner"]`, `defaultRole:"operator"`) + colunas role/banned/banReason/banExpires via regen CLI-canônico
- [x] **UM-03**: Seed one-shot idempotente — 1º operador → `role='owner'`, demais → `operator`
- [x] **UM-04**: Server Action owner-gated: criar/convidar operador (`admin.createUser` random pwd + `requestPasswordReset` via Brevo SMTP)
- [x] **UM-05**: Server Action owner-gated: remover operador + revogar todas as sessões
- [x] **UM-06**: Server Action owner-gated: resetar senha de operador (link e-mail + revogar sessões)
- [x] **UM-07**: Server Action owner-gated: resetar 2FA (clear `two_factor` + `two_factor_enabled=false` + revogar) — CR-01 intacto
- [x] **UM-08**: Tabela `admin_audit_log` (D-08); toda ação admin grava 1 row; self-service change-password NÃO logado (D-09)
- [x] **UM-09**: Brevo SMTP via nodemailer wired em `sendResetPassword`; reachability do container confirmada
- [x] **UM-10**: `operadores/page.tsx` lê role real; owner-gate esconde controles para não-owners

### Dashboard Pod-Config Control (Phase 17 — derived in plan-phase 2026-06-30)

- [ ] **POD-CFG-01**: Migration 0031 `ai_gateway.pod_config` single-row table (16 hot fields) + `pod_config_changed` NOTIFY trigger (espelha 0009)
- [ ] **POD-CFG-02**: Seed env→DB idempotente no 1º boot (ON CONFLICT DO NOTHING); env permanece fallback (NÃO removido) (D-02)
- [ ] **POD-CFG-03**: `podconfig.Loader` hot-reload (LISTEN/NOTIFY + atomic snapshot + last-good-on-error) espelhando `upstreams.Loader`
- [ ] **POD-CFG-04**: Reconciler/budget/schedule leem os 16 hot fields do snapshot (próxima provision/tick); estruturais continuam em `r.cfg` (D-01/D-02)
- [ ] **POD-CFG-05**: Storage de bounds (min/max) owner-editável, seedado dos defaults da RESEARCH (D-03)
- [ ] **POD-CFG-06**: Endpoint gateway `PATCH /admin/primary/config` (write, X-Admin-Key) → UpdatePodConfigField/Bound; SEM restart/estrutural (D-02)
- [ ] **POD-CFG-07**: Endpoint gateway `GET /admin/primary/lifecycle` (FSM state + event trail da lifecycle ABERTA) (D-05)
- [ ] **POD-CFG-08**: Dashboard editor dos 16 hot fields — owner-edit / operator read-only (D-01/D-07)
- [ ] **POD-CFG-09**: Dashboard editor de bounds (Campo|Mín|Máx) — owner-edit / operator read-only (D-03)
- [ ] **POD-CFG-10**: Server action owner-gated (`requireOwner` server-side) + validação do valor vs bound corrente antes de salvar (D-03a/D-07)
- [ ] **POD-CFG-11**: Audit dashboard-side (`admin_audit_log`, action=`pod_config.update`/`pod_config_bounds.update`, metadata={field,old,new}); SEM dual-write no gateway (D-06)
- [ ] **POD-CFG-12**: Confirm simples de um clique com string de impacto específica nas ações perigosas (cap-down, estreitar agenda, Disabled, dias vazios, allowlist restritiva); SEM type-to-confirm (D-04)
- [ ] **POD-CFG-13**: Display read-only dos 19 campos estruturais (shape/imagens/pesos/llama args/timezone) (D-01)
- [ ] **POD-CFG-14**: Painel ao vivo no dashboard (poll 10s de `/admin/primary/lifecycle`, FSM + event trail, reusa `fsm.ts` + `StaleIndicator`) (D-05)

## v2 Requirements

Deferred to future release. Tracked but not in current roadmap.

### Semantic Caching

- **CACHE-01**: Cache semântico com threshold calibrado após coleta de dados de produção
- **CACHE-02**: Invalidação temporal configurável por tenant

### Request Shadowing & Canary

- **CANR-01**: Shadow request para novo modelo (compara respostas sem servir ao cliente)
- **CANR-02**: Canary deployment de versão nova do gateway (5% → 25% → 100%)

### Advanced Multi-region

- **REG-01**: Multi-region deploy (Brasil primário, US secundário) se demanda global emergir
- **REG-02**: GeoDNS para routing regional

### TTS na GPU

- **TTS-01**: Migrar voice-api para GPU (Piper ou Coqui XTTS) quando houver headroom VRAM (eventualmente GPU upgrade para L40S 48GB)

## Out of Scope

Explicitly excluded. Documented to prevent scope creep.

| Feature | Reason |
|---------|--------|
| PII redaction centralizada | Regras são domain-specific de cada app; centralização leva a regras genéricas erradas |
| SSO / RBAC granular | 4 admins Ifix total; API key + role simples é suficiente para v1 |
| Prompt engineering helpers (reformular, traduzir, resumir embedded) | Escopo das apps cliente, não do gateway |
| Guardrails / safety filters built-in | Apps têm guardrails específicos ao domínio; gateway não opina em conteúdo |
| ElevenLabs ou TTS premium | Não está no escopo desta milestone |
| Coqui XTTS-v2 / voice cloning | Consome VRAM que compromete Qwen na 4090 |
| Kubernetes / Docker Swarm | Docker Compose atende a complexidade atual; overhead não justificado |
| vLLM / TGI como servidor LLM | vLLM não cabe na 4090 single-GPU para Qwen 27B (bug #37080 + TP=2); TGI descontinuado |
| `llama-cpp-python` Python server | Substituído por `llama.cpp` nativo (mais simples, tool-calling limpo) |
| `faster-whisper` + FastAPI custom | Substituído por Speaches (ativamente mantido, OpenAI-compat nativo) |
| `sentence-transformers` server | Substituído por Infinity (2-3× throughput mesma VRAM) |
| Fiber Go framework | Incompatível com `http.Flusher` para SSE streaming |
| Aprovação manual de spin-up emergencial | Failover invisível exige autonomia; guardrails compensam |
| Grafana + Prometheus stack completo | Dashboard próprio Next.js é suficiente para v1; Prometheus metrics exposto mas consumido pelo gateway |
| GPU primária RunPod Secure | Custo ~2× vs Vast.ai; spin-up emergencial Vast.ai cobre instabilidade |
| Modelos diferentes de Qwen 3.5 27B (Llama 3.3, Mixtral) | Fixar modelo minimiza drift entre local e OpenRouter |

## Traceability

Which phases cover which requirements. Updated during roadmap creation.

| Requirement | Phase | Status |
|-------------|-------|--------|
| POD-01 | Phase 1: GPU Pod Image & Smoke-Test | Pending |
| POD-02 | Phase 1: GPU Pod Image & Smoke-Test | Pending |
| POD-03 | Phase 1: GPU Pod Image & Smoke-Test | Pending |
| POD-04 | Phase 1: GPU Pod Image & Smoke-Test | Pending |
| POD-05 | Phase 1: GPU Pod Image & Smoke-Test | Pending |
| POD-06 | Phase 1: GPU Pod Image & Smoke-Test | Pending |
| POD-07 | Phase 1: GPU Pod Image & Smoke-Test | Pending |
| GW-01 | Phase 2: Gateway Core + Multi-tenant Auth | Complete |
| GW-02 | Phase 2: Gateway Core + Multi-tenant Auth | Complete |
| GW-03 | Phase 2: Gateway Core + Multi-tenant Auth | Complete |
| GW-04 | Phase 2: Gateway Core + Multi-tenant Auth | Complete |
| GW-05 | Phase 2: Gateway Core + Multi-tenant Auth | Complete |
| GW-06 | Phase 2: Gateway Core + Multi-tenant Auth | Complete |
| GW-07 | Phase 2: Gateway Core + Multi-tenant Auth | Complete |
| GW-08 | Phase 2: Gateway Core + Multi-tenant Auth | Complete |
| GW-09 | Phase 2: Gateway Core + Multi-tenant Auth | Complete |
| GW-10 | Phase 2: Gateway Core + Multi-tenant Auth | Complete |
| TEN-01 | Phase 2: Gateway Core + Multi-tenant Auth | Complete |
| TEN-02 | Phase 2: Gateway Core + Multi-tenant Auth | Complete |
| TEN-03 | Phase 4: Multi-tenant Quotas, Billing & Schedule Routing | Complete |
| TEN-04 | Phase 4: Multi-tenant Quotas, Billing & Schedule Routing → Phase 16: STT/embed metering | Complete (code; audio/embed metering wired Phase 16, quota-trip tests green; live-UAT DB-populate deferred) |
| TEN-05 | Phase 4: Multi-tenant Quotas, Billing & Schedule Routing | Complete |
| TEN-06 | Phase 4: Multi-tenant Quotas, Billing & Schedule Routing | Complete |
| TEN-07 | Phase 4: Multi-tenant Quotas, Billing & Schedule Routing | Complete |
| TEN-08 | Phase 2: Gateway Core + Multi-tenant Auth | Complete |
| TEN-09 | Phase 2: Gateway Core + Multi-tenant Auth | Complete |
| RES-01 | Phase 3: Resilience & Fallback Chain | Complete |
| RES-02 | Phase 3: Resilience & Fallback Chain | Complete |
| RES-03 | Phase 3: Resilience & Fallback Chain | Complete |
| RES-04 | Phase 3: Resilience & Fallback Chain | Complete |
| RES-05 | Phase 3: Resilience & Fallback Chain | Complete |
| RES-06 | Phase 3: Resilience & Fallback Chain | Complete |
| RES-07 | Phase 3: Resilience & Fallback Chain | Complete |
| RES-08 | Phase 3: Resilience & Fallback Chain | Complete |
| LSH-01 | Phase 5: Load Shedding | Complete |
| LSH-02 | Phase 5: Load Shedding | Complete |
| LSH-03 | Phase 5: Load Shedding | Complete |
| LSH-04 | Phase 5: Load Shedding | Complete |
| LSH-05 | Phase 5: Load Shedding | Complete |
| PRV-01 | Phase 6: Auto-provisioning Emergency Pod | Complete |
| PRV-02 | Phase 6: Auto-provisioning Emergency Pod | Complete |
| PRV-03 | Phase 6: Auto-provisioning Emergency Pod | Complete |
| PRV-04 | Phase 6: Auto-provisioning Emergency Pod | Complete |
| PRV-05 | Phase 6: Auto-provisioning Emergency Pod | Complete |
| PRV-06 | Phase 6: Auto-provisioning Emergency Pod | Complete |
| PRV-07 | Phase 6: Auto-provisioning Emergency Pod | Complete |
| PRV-08 | Phase 6: Auto-provisioning Emergency Pod | Complete |
| PRV-09 | Phase 6: Auto-provisioning Emergency Pod | Complete |
| PRV-10 | Phase 6: Auto-provisioning Emergency Pod | Complete |
| OBS-01 | Phase 7: Observability — Dashboard & Alerting | Complete |
| OBS-02 | Phase 7: Observability — Dashboard & Alerting | Complete |
| OBS-03 | Phase 7: Observability — Dashboard & Alerting | Complete |
| OBS-04 | Phase 7: Observability — Dashboard & Alerting | Complete |
| OBS-05 | Phase 7: Observability — Dashboard & Alerting | Complete |
| OBS-06 | Phase 7: Observability — Dashboard & Alerting | Complete |
| OBS-07 | Phase 7: Observability — Dashboard & Alerting | Complete |
| OBS-08 | Phase 7: Observability — Dashboard & Alerting | Complete |
| OBS-09 | Phase 15: Dashboard Economia & Histórico | Complete |
| OBS-10 | Phase 15: Dashboard Economia & Histórico | Complete |
| INT-01 | Phase 8: Client Integration — ConverseAI + Chat Ifix | Code-complete; live UAT pending |
| INT-02 | Phase 8: Client Integration — ConverseAI + Chat Ifix | Code-complete; live UAT pending |
| INT-03 | Phase 9: Client Integration — Sensitive Tenants | Code-complete; live UAT pending |
| INT-04 | Phase 9: Client Integration — Sensitive Tenants | Code-complete; live UAT pending |
| INT-05 | Phase 9: Client Integration — Sensitive Tenants | Code-complete; live UAT pending |
| INT-06 | Phase 10: prod-deploy-ai-gateway | Code-complete; live UAT pending |
| PRD-01 | Phase 11: prod-hardening | Complete |
| PRD-02 | Phase 11: prod-hardening | Complete |
| PRD-03 | Phase 11: prod-hardening | Complete |
| PRD-04 (partial) | Phase 10: prod-deploy-ai-gateway | Pending — RUNBOOK-DEPLOY.md only |
| PRD-04 (full) | Phase 11: prod-hardening | Complete — RUNBOOK-INCIDENTS.md (4 D-11 classes) + POSTMORTEM-TEMPLATE.md (Google SRE blameless 9-section) + RUNBOOK-2FA-RECOVERY.md shipped via 11-09 |
| PRD-05 | Phase 11: prod-hardening | Complete |
| PRD-06 | Phase 11: prod-hardening | Complete |
| PRD-07 | Phase 10: prod-deploy-ai-gateway | Complete |
| UM-01 | Phase 13: dashboard-user-management | Complete |
| UM-02 | Phase 13: dashboard-user-management | Complete |
| UM-03 | Phase 13: dashboard-user-management | Complete |
| UM-04 | Phase 13: dashboard-user-management | Complete |
| UM-05 | Phase 13: dashboard-user-management | Complete |
| UM-06 | Phase 13: dashboard-user-management | Complete |
| UM-07 | Phase 13: dashboard-user-management | Complete |
| UM-08 | Phase 13: dashboard-user-management | Complete |
| UM-09 | Phase 13: dashboard-user-management | Complete |
| UM-10 | Phase 13: dashboard-user-management | Complete |

<!-- 2026-05-26: Phase 10 plan-phase per D-16 split PRD-01/02/03/05/06 from Phase 10 → Phase 11; PRD-04 split into partial (Phase 10 RUNBOOK-DEPLOY.md) + full (Phase 11 incident runbook). -->

**Coverage:**

- v1 requirements: 70 total
- Mapped to phases: 70 (100% — Phase 10 + Phase 11 split per D-16; PRD-04 counted once as the requirement, mapped to two phases as partial + full)
- Unmapped: 0

---
*Requirements defined: 2026-04-17*
*Last updated: 2026-05-26 — D-16 split: 5 PRDs moved to new Phase 11; PRD-04 split partial (Phase 10) + full (Phase 11)*
