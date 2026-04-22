# Phase 5: Load Shedding (Saturation-aware Routing) - Context

**Gathered:** 2026-04-22
**Status:** Ready for planning

<domain>
## Phase Boundary

Transforma o gateway com failover reativo (breaker-only, Fase 3) em um gateway **saturation-aware** que detecta degradação do backend local ANTES de virar falha e desvia overflow para `openrouter-chat` (Fireworks pin da Fase 3 D-C1) enquanto primário recupera, preservando baixa latência para as demais tenants via fairness per-tenant:

1. **Inflight counter atomic por upstream + por (upstream, tenant)** (LSH-01) — incrementa pre-dispatch, decrementa no return do handler (defer); expõe gauges Prometheus `gateway_inflight{upstream}` e `gateway_inflight_tenant{upstream,tenant}`.
2. **Sinal composto 2-of-3** (LSH-02) — shed ativa quando ≥2 de {inflight>shed_inflight_max, P95(ring buffer últimas ~200 req)>shed_p95_ms, VRAM_used>shed_vram_bytes} passam threshold simultaneamente. Ring buffer lockless por upstream; DCGM via HTTP pull a cada 5s do pod `:9400` com fail-open (2s timeout; se falha, ignora sinal VRAM e usa inflight+P95).
3. **Hysteresis FSM 4-estados** (LSH-03, SC-2) — OFF → ARMED (espera 30s sustentado) → ON → RECOVERING (espera 60s limpo) → OFF, per-upstream tier-0 (3 FSMs: local-llm, local-stt, local-embed). Estado in-process autoritativo + mirror Redis `gw:shed:{upstream}` Hash + Pub/Sub `gw:shed:events` (paralelo ao breaker Fase 3 D-D1).
4. **Thresholds em `upstreams.circuit_config` JSONB** (LSH-04, SC-3) — JSONB estendido com `shed_inflight_max`, `shed_p95_ms`, `shed_vram_bytes`, `shed_arm_seconds` (default 30), `shed_recover_seconds` (default 60). Hot-reload via trigger NOTIFY existente da Fase 3 D-D4 — mudança aplica em <1s, FSM re-avalia imediatamente mas respeita hysteresis do ciclo atual.
5. **Fairness per-tenant hard cap** (LSH-05, SC-4) — novas colunas em `tenants`: `local_inflight_max_llm`, `local_inflight_max_stt`, `local_inflight_max_embed`, `priority_tier TEXT CHECK IN ('S','A','B')`. Request que atinge cap do tenant durante FSM=ON vai para tier-1. FSM=ON + tenant dentro do cap → ainda local (slot livre). Hot-reload via canal `tenants_changed` existente (Fase 4 D-C4).
6. **Shedding middleware entre schedule e tokencount** — chain: `auth → idempotency → rate-limit → quota → schedule → SHEDDING → tokencount → dispatcher`. Se FSM=ON + tenant excedeu cap: middleware sobrescreve tier selecionado pelo schedule (tier-0 → tier-1) e stampa decision no request ctx para audit/dispatcher.
7. **Dispatcher precedência: breaker wins** — lógica consolidada: `if breaker.open → tier-1; elif shed.on_tenant_capped → tier-1; else tier-0`. Shedding é "degradation pré-via ao failover" — breaker (upstream morto) sempre vence shedding (upstream estressado).
8. **Sensitive saturado = 503 imediato** — se tenant `data_class='sensitive'` atinge cap local durante FSM=ON: 503 `{error:{type:'service_unavailable', code:'upstream_saturated_for_sensitive_tenant'}}` + `Retry-After: 5`. LGPD: não pode shed para external. Consistente com Fase 3 D-B4 (sensitive streaming pre-dispatch fail-fast).
9. **Tier-1 também indisponível = 503 `all_chat_upstreams_saturated`** — se openrouter-chat OPEN ou 429 durante shed, 503 com `Retry-After: 30`. Sem fallback-de-fallback para chat (Fase 3 D-C4 reafirmado: Qwen fixo).
10. **Streaming é pre-dispatch only** — decisão de shed acontece uma vez antes do dispatcher; requests em-flight continuam no upstream original. Se FSM transita OFF→ARMED→ON mid-stream, requests novas vão tier-1 mas existentes seguem. Alinhado RES-05 e Fase 3 D-B4.
11. **Peak-off-hours é noop para shedding** — schedule middleware já selecionou tier-1 antes do shedding rodar; shedding vê tier-1 e passa. Métrica registra `gateway_shed_decisions_total{decision='skipped_peak_offhours'}` para diferenciar em dashboards Fase 7.
12. **Observability + audit** — `audit_log.upstream='shed_saturated'` (valor reservado novo) + `shed_reason` implícito em `error_code=NULL` (não é erro, é routing decision). Métrica `gateway_shed_decisions_total{upstream, reason}` com reason ∈ {inflight, p95, vram, tenant_cap, skipped_peak_offhours, tier1_unavailable}.
13. **gatewayctl extensions** — subcomandos `gatewayctl shed-state [--upstream]` (exibe FSM live + signals), `gatewayctl shed-force {on|off} --upstream X [--ttl 300s]` (override operacional), `gatewayctl thresholds set --upstream X --inflight N --p95-ms N --vram-gb N` (atalho para editar `circuit_config` JSONB), `gatewayctl tenant set-shed-limits --tenant X --llm N --stt N --embed N --tier {S|A|B}`.

**Fora de escopo desta phase:**
- Auto-provisioning emergency pod via Vast.ai → Fase 6
- Dashboard Next.js + WhatsApp/email alerts → Fase 7 (shedding expõe métrica + audit; Fase 7 consome)
- Cost attribution separando custo phantom-local vs real-external durante shedding → Fase 4 já provê `cost_local_phantom_brl` + `cost_external_brl`; nenhum trabalho adicional em Fase 5
- Integração das apps cliente → Fases 8-9
- HTTPS/TLS + DNS público → Fase 10
- Per-tenant priority pre-emption (Pitfall 9 Tier-S preempt Tier-B) — v1 usa só hard caps per-tenant + priority_tier como metadata para dashboard/runbook; preemption real fica deferida para v2 ou Fase 10 se operacional exigir
- Autoscaling do cap per-tenant baseado em padrão histórico — v1 caps são config manual via gatewayctl

</domain>

<decisions>
## Implementation Decisions

### A. Sinal de saturação & thresholds

- **D-A1 (Composição 2-of-3):** Shed ativa quando ≥2 de {inflight>max, P95>ms, VRAM_used>bytes} passam threshold simultaneamente. Reduz falsos positivos de spike único (P95 outlier sozinho não dispara shed). Captura saturação real (fila formando + latency degradando, ou fila + VRAM próximo do teto). FEATURES.md "Recommended Load-Shedding Algorithm" explicita. Justificativa: com 6 tenants e 4 vCPU no gateway, OR-any seria ruidoso demais; weighted score é over-engineering; inflight-primary + confirmadores perde o caso "VRAM+P95 degradados sem fila visível" (ex: GPU thrashing por modelo de outro cliente da Vast.ai no mesmo host).
- **D-A2 (P95 via ring buffer das últimas ~200 requests por upstream):** Ring buffer in-memory lockless (slice circular + atomic index) por upstream guardando `request_duration_ms` das últimas ~200 responses. P95 calculado sob demanda (quicksort ou t-digest inline — Claude decide em execute; ~200 elements → ordenação é <100µs). Sem deps externas, exato para o que esta réplica dispatchou. Tamanho configurável via env `SHED_LATENCY_RING_SIZE=200` (default). Reset ao trocar tier-0 URL.
  - **Por que não Prometheus `histogram_quantile`:** exigiria self-scrape ou dependência no timing do próximo scrape; cardinality dos buckets pressiona obs/metrics.go.
  - **Por que não HDR/tdigest:** overkill para p95 (p99+ é que exige precisão de sliding window); 6 upstreams × 200 samples é trivial.
  - **Por que não EWMA:** FEATURES.md recomenda p95 real; EWMA é média, não percentil — discrepância com SC-2 ("P95 latency spike").
- **D-A3 (DCGM scrape HTTP pull periodic 5s + fail-open 2s):** Goroutine `dcgmScraper` no gateway faz `GET http://<pod-host>:9400/metrics` a cada 5s; parsa Prometheus text format para extrair `DCGM_FI_DEV_FB_USED` (MiB) + `DCGM_FI_DEV_FB_TOTAL`. Atualiza gauge `gateway_vram_used_bytes` + variável in-memory consumida pelo sinal. Timeout HTTP 2s — se falha, sinal VRAM é ignorado no 2-of-3 (effectively reverte a regra para inflight+P95 majority-OR enquanto pod:9400 não responde). Endpoint `:9400` configurável via env `DCGM_EXPORTER_URL` (default `http://local-llm-host:9400/metrics`). Métrica `gateway_dcgm_scrape_failures_total{reason}`.
  - Pod já expõe `DCGM_FI_DEV_FB_USED`/`DCGM_FI_DEV_FB_FREE`/`DCGM_FI_DEV_FB_TOTAL` (Phase 1 `pod/docker-compose.yml` dcgm-exporter + `pod/smoke/smoke.py` usa).
  - Host do pod é resolvido via env `DCGM_EXPORTER_URL` — operador configura no Portainer stack apontando para o IP privado da VPS GPU.
- **D-A4 (Thresholds em `upstreams.circuit_config` JSONB):** Estender JSONB existente da Fase 3 com novos campos:
  ```json
  {
    "failures": 3,
    "cooldown_s": 30,
    "shed_inflight_max": 8,
    "shed_p95_ms": 2000,
    "shed_vram_used_bytes": 22548578304,
    "shed_arm_seconds": 30,
    "shed_recover_seconds": 60
  }
  ```
  Reusa trigger NOTIFY `upstreams_changed` (Fase 3 D-D4) → hot-reload via loader existente. Nenhum migration de schema novo para thresholds; só evolução de `parseCircuitConfig` em `gateway/internal/upstreams/types.go`. Bate SC-3 (<2s sem restart) trivialmente — já provado em Fase 3 para breaker config.
  - Defaults conservadores (seed migration atualizada):
    - `shed_inflight_max=8` (chat), `16` (embed) — per FEATURES.md "Recommended"
    - `shed_p95_ms=2000` (chat), `500` (embed), `3000` (stt) — reconhecendo que Whisper é mais lento por design
    - `shed_vram_used_bytes=22548578304` (21 GB, ~87.5% da 4090 — bate LSH-02 "VRAM > 21 GB")
    - `shed_arm_seconds=30`, `shed_recover_seconds=60` — bate LSH-03 + SC-2
  - Operador refina via `gatewayctl thresholds set` após Phase 1 baseline empirical data (folded TODO de STATE.md).

### B. Fairness per-tenant (SC-4)

- **D-B1 (Hard cap per-tenant + overflow tier-1):** Cada tenant tem cap `local_inflight_max_{role}` (ex: ConverseAI=4, Telefonia=2, Campanhas=1, voice-api=1, Chat Ifix=2, Cobranças=1). Ao atingir cap durante FSM=ON do upstream tier-0, requests daquela tenant vão direto para openrouter-chat (shed). Outras tenants ainda veem slots livres localmente — previne noisy-neighbor (Pitfall 9). Simples, determinístico, configurável via CLI. 6 tenants totais: config manual é tratável.
  - **Quando FSM=OFF (local saudável):** cap per-tenant é enforced somente se atingido — tenant pode usar mais que seu cap se houver slots globais sobrando (fairness suave). Rejeitado em versões mais estritas.
  - **Decisão alternativa rejeitada:** priority tiers com pre-emption (Telefonia preempt Campanhas slot em ON) — fica como deferred (Fase 10 se operacional exigir). V1 usa só hard caps.
  - **Decisão alternativa rejeitada:** weighted fair-share (DRR/WRR) — "justiça matemática" pode conflitar com SLA real (Telefonia SLA > ConverseAI SLA independente do weight). Hard caps são mais predictable.
- **D-B2 (Colunas novas em `tenants`):** Migration `0016_evolve_tenants_shedding_limits.sql`:
  ```sql
  ALTER TABLE ai_gateway.tenants
    ADD COLUMN local_inflight_max_llm   INT NOT NULL DEFAULT 4,
    ADD COLUMN local_inflight_max_stt   INT NOT NULL DEFAULT 2,
    ADD COLUMN local_inflight_max_embed INT NOT NULL DEFAULT 8,
    ADD COLUMN priority_tier TEXT NOT NULL DEFAULT 'A' CHECK (priority_tier IN ('S','A','B'));
  ```
  - Seed sensato per tenant conhecido via UPDATE no mesmo migration (Telefonia `priority_tier='S'` + STT=2; ConverseAI `tier='A'` + LLM=4; Campanhas `tier='B'` + LLM=1).
  - Reusa canal `tenants_changed` NOTIFY + loader existente (Fase 4 D-C4) — hot-reload <1s. `gateway/internal/tenants/types.go` expande `TenantConfig` struct.
  - `priority_tier` em Phase 5 é **metadata only** — dashboard Fase 7 e runbook usam para decisões operacionais. Preemption real é deferida.
- **D-B3 (Sensitive saturado = 503 imediato):** Quando tenant `data_class='sensitive'` atinge `local_inflight_max_{role}` durante FSM=ON (ou request chega durante FSM=ON + cap atingido simultaneamente): retorna 503 com envelope OpenAI:
  ```json
  {"error":{"type":"service_unavailable","code":"upstream_saturated_for_sensitive_tenant","message":"Primary upstream is saturated; sensitive-data tenants cannot be routed to external providers."}}
  ```
  Status 503, header `Retry-After: 5`. Consistente com Fase 3 D-B4 (sensitive streaming pre-dispatch fail-fast). Sem retry in-memory 4s — justificativa: saturação é condição sustentada (ARMED 30s + ON), diferente de breaker-open que pode ser blip transitório; aguardar 4s adicionais raramente resolve e segura handler desnecessariamente.
  - **Audit log:** `upstream='shed_blocked_sensitive'` (valor reservado novo, paralelo ao Fase 3 `blocked_sensitive`), `error_code='upstream_saturated_for_sensitive_tenant'`, `status_code=503`. Sem `audit_log_content` (sensitive nunca persiste content — Fase 2 D-B2).
  - **Métrica:** `gateway_shed_decisions_total{upstream, reason='sensitive_capped'}` + `gateway_shed_blocked_sensitive_total{tenant}`.
- **D-B4 (Shedding middleware entre schedule e tokencount):** Chain ordenada:
  ```
  auth → idempotency → rate-limit → quota → schedule → SHEDDING → tokencount → dispatcher → billing-flush
  ```
  Shedding decide com base em:
  - Tier inicial escolhido pelo schedule (tier-0 para 24/7 tenants, tier-1 se peak-off-hours)
  - Se já é tier-1 (peak-off-hours ou outro override) → shedding é noop, grava `shed_decision=skipped_peak_offhours` no ctx
  - Se é tier-0 → consulta FSM do upstream tier-0 (via `shed.State(upstream)`) + inflight do tenant
    - FSM=OFF → permite tier-0
    - FSM=ON + tenant dentro do cap → permite tier-0
    - FSM=ON + tenant excedeu cap + normal → override para tier-1 + `shed_decision=shed_to_tier1`
    - FSM=ON + tenant excedeu cap + sensitive → 503 D-B3
  - Middleware stampa decisão no request ctx; dispatcher lê override; audit-middleware usa para `audit_log.upstream`.
  - Tokencount roda DEPOIS para contar tokens do upstream final (chat: 16k cap é igual em tier-0 e tier-1 per Fase 3 D-A4 Claude's Discretion; nenhum recálculo necessário).

### C. Hysteresis FSM & interação com breaker

- **D-C1 (FSM 4 estados: OFF → ARMED → ON → RECOVERING → OFF):**
  - **OFF:** sinais abaixo threshold; requests tier-0 normalmente.
  - **ARMED:** sinal 2-of-3 passou threshold agora; aguarda sustentação por `shed_arm_seconds` (default 30s). Se sinal cair durante ARMED, volta OFF imediato (não espera). Requests ainda tier-0 durante ARMED.
  - **ON:** sinal sustentou 30s; requests que excedem tenant cap vão tier-1. Em `shed_arm_seconds` aceita re-arm se sinal oscilar abaixo e voltar (evita flapping de curto prazo).
  - **RECOVERING:** sinal caiu abaixo threshold agora (ou 2-of-3 agora é só 1 sustentado); aguarda limpeza por `shed_recover_seconds` (default 60s). Durante RECOVERING requests continuam tier-1 (não reverte precipitadamente). Se sinal volta a ON durante RECOVERING, FSM transita para ON imediato (sem passar por ARMED — já está provado que está saturado).
  - Hysteresis nos dois caminhos: ARMED (30s) entrando e RECOVERING (60s) saindo. Bate SC-2 literalmente ("shedding ativa em ≤30s" + "no flapping em 60s oscilação").
  - Implementação: `gateway/internal/shed/fsm.go` com state field `atomic.Int32` + timestamps `atomic.Int64`; transições disparadas por tick de 1s (goroutine `fsmTick` global, per-upstream). Mais simples que timer.AfterFunc por FSM.
- **D-C2 (Escopo: per-upstream tier-0 global — 3 FSMs):** Um FSM por upstream tier-0 (`local-llm`, `local-stt`, `local-embed`) = 3 FSMs totais. FSM é propriedade do UPSTREAM, não da combinação tenant×upstream. Quando FSM=ON, aplicação ao tenant depende do cap per-tenant (D-B1). Sinais VRAM/P95 são shared entre tenants (todos os tenants usam o mesmo pod) — só inflight é per-tenant. Granularidade maior (per-tenant FSM) é over-engineering: 6 tenants × 3 roles = 18 FSMs sem benefício adicional.
  - **Decisão alternativa rejeitada:** per-role global (1 FSM per-role em vez de per-upstream) — menos estados mas perde distinção quando tier-0 muda (hoje tier-0 llm/stt/embed são todos do mesmo pod; amanhã podem divergir — per-upstream é mais future-proof).
- **D-C3 (Persistência: in-process autoritativo + Redis mirror + Pub/Sub):** Mesmo pattern do breaker Fase 3 D-D1:
  - In-process `shed.Set` struct com `map[upstream_name]*FSM` + RWMutex. Decisões no hot path são lockless via atomic ops na struct FSM.
  - Callback `OnStateChange` publica em Redis:
    - Chave: `gw:shed:{upstream_name}` — Hash com `{state, since_unix, signals:{inflight,p95_ms,vram_bytes}, reason}`
    - Pub/Sub: `PUBLISH gw:shed:events {upstream, state, since, signals, reason}`
  - Outras réplicas subscribem `gw:shed:events` e atualizam FSM local via `Transition(newState)` sintético. Convergência cross-replica <1s. Fase 6 multi-réplica funciona sem refactor.
  - Dashboard Fase 7 lê `gw:shed:*` keys.
  - **Fallback se Redis down:** FSMs continuam in-process (publish falha silencioso com métrica `gateway_shed_mirror_failures_total`). Mesma filosofia do breaker mirror.
  - Novo package `gateway/internal/shed/` paralelo a `breaker/`: `fsm.go`, `set.go`, `mirror.go`, `subscribe.go`, `errors.go`.
- **D-C4 (Precedência: breaker wins):** Dispatcher consulta ambos em ordem:
  ```go
  if breaker.State(upstream) != gobreaker.StateClosed {
      routeToTier1(reason: "breaker_open")
  } else if shed.State(upstream) == shed.StateOn && tenantInflight >= tenantCap {
      if tenant.DataClass == "sensitive" {
          write503(code: "upstream_saturated_for_sensitive_tenant")
      } else {
          routeToTier1(reason: "shed_saturated")
      }
  } else {
      dispatchTier0()
  }
  ```
  Breaker (upstream morto) sempre vence shedding (upstream estressado) — se breaker aberto, upstream não responde de qualquer jeito, shedding é supérfluo. Quando breaker HALF_OPEN e FSM=ON: request vai tier-1 (breaker wins); probe breaker continua sintético via Fase 3 D-A2 e pode fechar breaker independentemente; FSM só sai de ON via recovery normal (sinais caem).
  - **Decisão alternativa rejeitada:** shed wins durante HALF_OPEN (continuar shedando mesmo se breaker probe passou) — introduz complexidade no gobreaker callback sem ganho claro. Probe é raro.
  - **Audit distingue causa:** `upstream='shed_saturated'` vs `upstream='openrouter-chat'` (breaker-driven fallback) vs `upstream='openrouter-chat'` (normal dispatch quando FSM=OFF + tier-0 open). Audit middleware recebe `shed_decision` do ctx e decide qual valor gravar.
- **D-C5 (Hot-reload: aplicar imediato + re-avaliar FSM):** Quando `upstreams.circuit_config` muda via `gatewayctl thresholds set`:
  - Loader atomic-swap dos thresholds (<1s); FSM re-avalia na próxima tick de 1s (total <2s — bate SC-3).
  - Se novos thresholds tornam sinal "não-saturado" enquanto FSM=ON:
    - FSM transita ON → RECOVERING (espera `shed_recover_seconds` de limpeza antes de OFF). Preserva hysteresis do ciclo atual — mudar threshold não pula guarantee de estabilidade.
  - Se novos thresholds mantêm sinal "saturado": FSM permanece ON, reloga métrica `gateway_shed_thresholds_changed_total{upstream}` para audit.
  - **Override operacional via `gatewayctl shed-force off --upstream X --ttl 300s`:** força FSM=OFF por 5 min (TTL), independente de sinais — para operador desbloquear durante incidente conhecido. Implementado como "shadow state" no Redis key `gw:shed:force:{upstream}` com TTL — loader respeita se presente.

### D. Edge cases

- **D-D1 (Tier-1 também saturado → 503 `all_chat_upstreams_saturated`):** Se durante shed o openrouter-chat está OPEN (breaker) ou retorna 429 do próprio provider, retorna 503 com envelope OpenAI `{error:{type:'service_unavailable', code:'all_chat_upstreams_saturated', message:'Primary saturated and secondary unavailable.'}}` + `Retry-After: 30`. Consistente com Fase 3 D-C4 ("sem fallback de fallback para chat, Qwen fixo"). Apps cliente tratam via backoff próprio (429 do cliente cliente tier).
  - Audit: `upstream='shed_tier1_unavailable'` com `error_code='all_chat_upstreams_saturated'`, `status_code=503`.
  - STT e embed mantêm fallback Fase 3 independente (local-stt → openai-whisper, local-embed → openai-text-embedding-3-small) — esse erro é chat-specific.
- **D-D2 (Streaming é pre-dispatch only):** Decisão de shed acontece UMA vez antes do dispatcher. Request em-flight continua no upstream original até terminar (seja `[DONE]` ou abnormal close). Se FSM transita OFF→ARMED→ON mid-stream:
  - Requests novas vão tier-1 (via normal middleware evaluation).
  - Requests existentes seguem no upstream original; billing flush da Fase 4 funciona normalmente.
  - Alinhado com RES-05 (streaming fail-fast) e Fase 3 D-B4 (sensitive stream fail-fast pre-dispatch). Nunca retry/replay mid-stream.
  - **Decisão alternativa rejeitada:** abortar streams em-flight quando FSM transita para ON — duplica output risk (Pitfall 3) e agrava UX; não vale o ganho marginal de liberar inflight local mais rápido.
- **D-D3 (Peak-off-hours é noop explícito):** Schedule middleware já selecionou tier-1 antes do shedding rodar (ordem D-B4). Shedding vê tier-1 e passa sem alterar decisão.
  - Métrica `gateway_shed_decisions_total{decision='skipped_peak_offhours'}` incrementa para observabilidade. Dashboard Fase 7 pode diferenciar "shed by saturation" vs "routed by schedule" vs "overflowed during schedule decide".
  - Audit mantém `upstream='openrouter-chat'` + contexto schedule-derivado (Fase 4 D-C2 já grava).
- **D-D4 (Audit + métricas — convenção):**
  - `audit_log.upstream` ganha 3 novos valores reservados:
    - `shed_saturated` — tenant normal foi shedded para tier-1 por saturação
    - `shed_blocked_sensitive` — tenant sensitive foi bloqueado em 503 por saturação+LGPD
    - `shed_tier1_unavailable` — tanto tier-0 shedded quanto tier-1 indisponível, 503
  - `audit_log.error_code` em casos 503: `upstream_saturated_for_sensitive_tenant`, `all_chat_upstreams_saturated`
  - **Métricas Prometheus novas:**
    - `gateway_inflight{upstream}` — gauge (atomic counter)
    - `gateway_inflight_tenant{upstream,tenant}` — gauge (label budget: 3 upstreams × 6 tenants = 18 series, seguro)
    - `gateway_shed_state{upstream,state}` — gauge 0/1 (4 states × 3 upstreams = 12 series)
    - `gateway_shed_decisions_total{upstream,reason}` — counter (reason ∈ {inflight, p95, vram, tenant_cap, sensitive_capped, skipped_peak_offhours, tier1_unavailable})
    - `gateway_shed_transitions_total{upstream,from,to}` — counter (observa oscilações da FSM)
    - `gateway_shed_mirror_failures_total` — counter (Redis publish falhou)
    - `gateway_vram_used_bytes` — gauge (exposto pelo dcgmScraper)
    - `gateway_p95_request_ms{upstream}` — gauge atualizado por tick (derivado do ring buffer)
    - `gateway_dcgm_scrape_failures_total{reason}` — counter
    - `gateway_shed_thresholds_changed_total{upstream}` — counter (hot-reload observability)
    - `gateway_shed_force_active{upstream}` — gauge 0/1 (operator override ativo)
  - Cardinality total acrescentada: ~60 series — dentro do budget 10k da Pitfall 13 / Fase 7 OBS-02.
  - **Sentry breadcrumbs:** em transições de FSM (OFF→ARMED, ARMED→ON, ON→RECOVERING, RECOVERING→OFF) + em tier1_unavailable 503 + em sensitive 503.

### Claude's Discretion

- **Inflight counter implementação (LSH-01):**
  - Struct `shed.InflightRegistry` com:
    - `globalCounter map[upstreamName]*atomic.Int64` (decision hot path)
    - `tenantCounter map[upstreamName]map[tenantID]*atomic.Int64` (per-tenant enforcement)
    - RWMutex apenas para populate do map de maps (não para inc/dec atomic).
  - Increment em `shedMiddleware` após decisão de routing (tier-0 path); decrement via `defer` no próprio middleware. Dispatcher NÃO incrementa — middleware é source-of-truth.
  - Para tier-1 path (shed): apenas conta em métrica separada `gateway_inflight_tier1{upstream}` (dashboard-only), não usa em decisão.
- **Ring buffer de latência:**
  - `shed.LatencyRing` por upstream: `[]uint32` (ms unsigned 32 bits — 4.3s max, suficiente para chat; STT pode ter que ser int64 para áudio longo, verificar em execute).
  - Write index atomic; read copia snapshot e ordena.
  - Size configurável `SHED_LATENCY_RING_SIZE=200` env (default 200).
  - Atualizado via `defer latency.Record(upstream, elapsed)` no shedMiddleware.
- **dcgmScraper package:**
  - `gateway/internal/dcgm/scraper.go`: HTTP client com transport custom (short keep-alive), parser simples de Prometheus text format (procurando só `DCGM_FI_DEV_FB_USED`, `DCGM_FI_DEV_FB_TOTAL` — não precisa parser completo).
  - Goroutine `Run(ctx)` tick 5s. Atualiza `atomic.Int64` `vramUsedBytes` consumido pelo sinal.
  - Fail-open: se 3 scrapes consecutivos falharem, sinal VRAM fica marcado como "unknown" — 2-of-3 ignora esse componente (effectively vira "1-of-2 inflight+P95 ativado").
- **FSM tick goroutine:**
  - Um `fsmTick` global com `time.NewTicker(1 * time.Second)`; itera sobre todos FSMs e chama `fsm.Evaluate(signals)`.
  - Evaluate computa:
    ```go
    satScore := 0
    if signals.Inflight >= cfg.ShedInflightMax { satScore++ }
    if signals.P95Ms >= cfg.ShedP95Ms { satScore++ }
    if signals.VramUsedBytes >= cfg.ShedVramBytes && !signals.VramUnknown { satScore++ }
    saturated := satScore >= 2
    ```
  - Aplica transições de estado conforme FSM. Publica em Redis on-change.
  - Hot reload: `fsm.UpdateConfig(cfg)` atomic-swap da struct cfg.
- **shedMiddleware em `gateway/internal/shed/middleware.go`:**
  - Lê tier escolhido pelo schedule via ctx (schedule já grava `scheduleTierKey`).
  - Se tier==1: grava `shed_decision=skipped_peak_offhours` e passa.
  - Se tier==0: consulta FSM + tenantCap:
    - FSM.State != ON → passa (tier=0).
    - FSM.State == ON:
      - tenantInflight < tenantCap → passa (tier=0).
      - tenantInflight >= tenantCap + data_class=normal → override tier=1 + grava `shed_decision=shed_saturated` + increment counter tier-1.
      - tenantInflight >= tenantCap + data_class=sensitive → escreve 503 D-B3 + grava `shed_decision=shed_blocked_sensitive` + audit + return (não chama next).
  - Increment tenantCounter atomic ANTES do `next.ServeHTTP`; decrement via defer.
- **gatewayctl subcommands plumbing:**
  - `gatewayctl shed-state [--upstream X] [--format json|table]` — consulta Redis `gw:shed:*` + `gw:breaker:*` + gateway `/admin/shed-debug` (novo endpoint JSON dumping in-memory state).
  - `gatewayctl shed-force {on|off} --upstream X --ttl <duration>` — SET `gw:shed:force:{upstream}=<state>` com EXPIRE. Loader respeita se presente.
  - `gatewayctl thresholds set --upstream X --inflight N --p95-ms N --vram-gb N --arm-s N --recover-s N` — UPDATE na coluna JSONB `circuit_config` preservando campos existentes; NOTIFY automático via trigger existente.
  - `gatewayctl tenant set-shed-limits --tenant X --llm N --stt N --embed N [--tier {S|A|B}]` — UPDATE nas novas colunas de tenants.
- **Testes integration (testcontainers-go + mock pod):**
  - **SC-1:** sob burst onde inflight > cap, excesso vai OpenRouter; abaixo volta local. Cenário sintético: 1000 goroutines batendo chat com tenant_cap=4; verify 4 concurrent em tier-0 + resto em tier-1 mock.
  - **SC-2:** FSM hysteresis — simula sinal oscilando ON/OFF em ciclos de 10s por 120s; verify zero transições ON/RECOVERING além das reais (sinal sustentado). Duração real: 120s (exceção à regra "tests <30s" — integration suite opt-in).
  - **SC-3:** hot-reload — UPDATE `circuit_config` via SQL direto; verify FSM re-avalia em <2s. Mock clock?
  - **SC-4:** anti-starvation — tenant A (cap=4) burst 100 req; tenant B (cap=2) 10 req concurrent. Verify todas de B são atendidas em tier-0; excesso de A vai tier-1.
  - **Edge case D-B3:** sensitive tenant satura → 503 + audit `shed_blocked_sensitive`.
  - **Edge case D-D1:** tier-1 também indisponível → 503 `all_chat_upstreams_saturated`.
  - **Edge case D-D3:** peak-off-hours tenant → shedding noop; audit mantém `upstream='openrouter-chat'` (schedule-derived).
- **Per-route WriteTimeout (não aplicável Phase 5):** Fase 4 já restaurou. Sem trabalho adicional.
- **Migrations previstas:**
  - `0016_evolve_tenants_shedding_limits.sql` — colunas D-B2 + seed UPDATE 6 tenants.
  - `0017_evolve_upstreams_shed_thresholds.sql` — seed UPDATE `circuit_config` JSONB com campos `shed_*` default para 3 upstreams tier-0 (local-llm, local-stt, local-embed). Tier-1 upstreams ganham `shed_*=null` (noop — shedding nunca aciona contra tier-1).
  - `0018_audit_log_shed_values.sql` — se `audit_log.upstream` for ENUM (verificar Fase 2/3 schema), ADD VALUE; se TEXT, só docs no CHECK constraint.
- **Obs.go evolution (Fase 4 middleware.go):** wire novo counter `gateway_inflight_tenant{upstream,tenant}` via shedMiddleware. RequestsMiddleware existente (Fase 4) mantém — shedMiddleware roda DEPOIS dele na chain.

### Folded Todos

- **[STATE.md] Phase 5: Tune saturation thresholds (inflight N, P95 ms, VRAM GB) from Phase 1 baseline** → Defaults iniciais D-A4 (inflight=8/chat, P95=2000ms, VRAM=21GB=22548578304 bytes); operador refina pós-deploy via `gatewayctl thresholds set`. Plan deve incluir task "review thresholds vs Phase 1 smoke.yml output quando HUMAN-UAT destravar" (bloqueio atual: Phase 1 HUMAN-UAT pending).
- **[04-CONTEXT.md deferred] Per-tenant inflight fairness queue** → Resolvido em D-B1/D-B2 (hard cap per-tenant via colunas em tenants).
- **[04-CONTEXT.md deferred] Gauge `gateway_inflight{tenant}`** → Phase 4 deferiu para Phase 5; resolvido em D-A1/D-D4 (métrica `gateway_inflight_tenant{upstream,tenant}`).
- **[04-CONTEXT.md deferred] Per-app circuit breaker (trip por tenant noisy)** → Phase 5 não inclui. Hard caps + shed-to-tier-1 cobrem o caso noisy-neighbor (Pitfall 9). Per-tenant circuit fica deferred para v2 se operacional exigir (já está em 04 deferred).

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Project docs (internal)

- `.planning/PROJECT.md` — Vision, Core Value ("failover invisível"), Key Decisions (Detecção por inflight+P95+VRAM, não GPU util), Constraints (4090 24GB, VRAM apertado)
- `.planning/REQUIREMENTS.md` §Load shedding — LSH-01..LSH-05 (fonte de escopo desta phase)
- `.planning/REQUIREMENTS.md` §Out of Scope — Grafana/Prometheus full stack (dashboard Fase 7 via Next.js); Kubernetes
- `.planning/ROADMAP.md` §Phase 5 — Goal, Depends-on (Fase 3 fallback chain + Fase 4 inflight counters/tenants schema), Success Criteria SC-1..SC-4 (atenção: ROADMAP lista "Plans" que são copy-paste da Fase 3 — ignorar, Fase 5 é 5-7 plans novos)
- `.planning/STATE.md` — Todo "Phase 5: Tune saturation thresholds (inflight N, P95 ms, VRAM GB) from Phase 1 baseline" foldado aqui
- `.planning/phases/01-gpu-pod-image-smoke-test/01-CONTEXT.md` — Decisões D-04 (dcgm-exporter :9400 no pod) + D-27 (VRAM budget); pod já expõe `DCGM_FI_DEV_FB_USED`
- `.planning/phases/03-resilience-fallback-chain/03-CONTEXT.md` — Decisões Fase 3 que Fase 5 herda e estende: D-A1 (breaker-open puro pre-dispatch — Fase 5 adiciona shed pre-dispatch), D-B1..B4 (sensitive policy — Fase 5 aplica ao contexto de saturação), D-C1..C2 (Fireworks pin openrouter-chat — shed overflow herda), D-C4 (sem fallback-de-fallback chat — Fase 5 D-D1 reafirma), D-D1 (breaker in-process+Redis mirror — shed FSM replica mesmo pattern), D-D2..D4 (upstreams table + LISTEN/NOTIFY + circuit_config JSONB estendido em Fase 5), D-D4 (pgxlisten loader — Fase 5 reusa)
- `.planning/phases/04-multi-tenant-quotas-billing-schedule-routing/04-CONTEXT.md` — Decisões Fase 4 que Fase 5 herda: D-A4 (audit envelope com error codes discriminados), D-C1..C4 (schedule routing peak-off-hours — Fase 5 shed é noop em off-hours D-D3), D-D1 (middleware chain order — Fase 5 insere entre schedule e tokencount), D-D4 (gatewayctl suite), tenants loader + hot-reload canal `tenants_changed` (Fase 5 estende com colunas `local_inflight_max_*` + `priority_tier`); deferred "Per-tenant inflight fairness queue" + gauge `gateway_inflight{tenant}` — resolvido em Fase 5
- `.planning/phases/02-gateway-core-multi-tenant-auth/02-CONTEXT.md` — D-B1..B3 (audit_log schema com `upstream`, `error_code`, `status_code` columns — Fase 5 adiciona valores reservados `shed_saturated`, `shed_blocked_sensitive`, `shed_tier1_unavailable`), D-A3 (gatewayctl CLI pattern)

### Repo conventions (internal)

- `docs/CONVENTIONS.md` — gofmt/go vet/golangci-lint obrigatórios, slog `module=UPPER_SNAKE_CASE`, RFC3339, sentinel errors, conventional commits com scope
- `pkg/openai/types.go` — tipos OpenAI-compat; Fase 5 adiciona error code constants: `UpstreamSaturatedForSensitiveTenantCode`, `AllChatUpstreamsSaturatedCode`
- `/home/pedro/projetos/pedro/CLAUDE.md` — convenções Ifix-wide (GSD workflow enforcement, Sentry pattern, deploy via Portainer + webhook)

### Research bundle (internal)

- `.planning/research/SUMMARY.md` — resumo executivo da stack
- `.planning/research/STACK.md` §Resilience — sony/gobreaker v2 (reusa para shed FSM publisher pattern), x/sync/errgroup (probe paralelo reusa)
- `.planning/research/STACK.md` §Telemetry — prometheus/client_golang (novas métricas cardinality budget)
- `.planning/research/FEATURES.md` §Load-Shedding Deep Dive (linhas 80-146) — Pattern 1 (queue-depth vLLM), Pattern 2 (TTFT p95), Pattern 3 (DCGM), Recommended Algorithm com pseudocode, Key design decisions (gateway-side inflight é ground-truth)
- `.planning/research/FEATURES.md` §Load shedding by GPU saturation (tabela HIGH confidence) — "Multi-signal: n_busy_slots_per_decode, queue depth, kv_cache_usage_perc equivalent, GPU util from DCGM"
- `.planning/research/PITFALLS.md` §Pitfall 2 — Saturation detection via GPU util % triggers false failovers (justifica D-A1 composite 2-of-3 NÃO usar util alone + D-C1 hysteresis 30s/60s)
- `.planning/research/PITFALLS.md` §Pitfall 9 — Noisy-neighbor (justifica D-B1 per-tenant hard caps + priority_tier metadata)
- `.planning/research/PITFALLS.md` §Pitfall 3 — Streaming replay (justifica D-D2 streaming pre-dispatch only, nunca abort mid-stream)
- `.planning/research/PITFALLS.md` §Pitfall 7 — OpenRouter rate-limited (justifica D-D1 503 `all_chat_upstreams_saturated`, sem fallback de fallback)
- `.planning/research/PITFALLS.md` §Pitfall 13 — Cardinality explosion (informa label budget ~60 series novas ≤ 10k total)
- `.planning/research/ARCHITECTURE.md` §Gateway components — subsistemas dispatcher/breaker + placement de load-shedder entre schedule e dispatch

### Existing code (internal)

- `gateway/internal/upstreams/types.go` — `CircuitConfig` struct existente com `Failures`, `Cooldown`, `CooldownS` (Fase 3 D-D2); Fase 5 estende com `ShedInflightMax`, `ShedP95Ms`, `ShedVramBytes`, `ShedArmSeconds`, `ShedRecoverSeconds`
- `gateway/internal/upstreams/loader.go` + `listen.go` — loader com pgxlisten hot-reload (Fase 3 D-D4); Fase 5 consume sem modificar (circuit_config JSONB evolve é automaticamente refletido via `parseCircuitConfig`)
- `gateway/internal/breaker/` — package inteiro (breaker.go, mirror.go, subscribe.go, errors.go); Fase 5 replica pattern em novo package `gateway/internal/shed/`
- `gateway/internal/proxy/dispatcher.go` — role-based dispatcher com breaker check; Fase 5 adiciona shed.State() check entre breaker e tier-0 dispatch (D-C4 precedência)
- `gateway/internal/proxy/tokencount.go` — TokenCounter (Fase 3); Fase 5 não modifica — tokencount roda DEPOIS do shedding middleware (D-B4 chain order)
- `gateway/internal/proxy/interceptor_usage.go` — SSE usage extractor (Fase 4); Fase 5 reusa sem modificar
- `gateway/internal/schedule/policy.go` — tier decision middleware (Fase 4); Fase 5 consome tier selecionado via request ctx
- `gateway/internal/tenants/` — loader + pgxlisten canal `tenants_changed` (Fase 4 D-C4); Fase 5 estende `TenantConfig` struct com `LocalInflightMaxLLM`, `LocalInflightMaxSTT`, `LocalInflightMaxEmbed`, `PriorityTier` — loader existente absorve via evolução do sqlc query
- `gateway/internal/redisx/` — go-redis client; Fase 5 adiciona helpers `PublishShedEvent`, `SubscribeShedEvents`, `WriteShedState`, `GetShedForce` (shadow state para operator override)
- `gateway/internal/obs/metrics.go` — prom collectors; Fase 5 adiciona ~11 métricas novas (D-D4 lista)
- `gateway/internal/obs/middleware.go` — RequestsMiddleware existente (Fase 4); Fase 5 não modifica — shedMiddleware é middleware separado
- `gateway/internal/audit/` — audit pipeline (Fase 2 D-B3); Fase 5 só adiciona valores reservados em `upstream` column (sem schema change se TEXT; ENUM value add se enum)
- `gateway/internal/auditctx/` — context-based override pattern (Fase 3 D-B3); Fase 5 reusa para shedMiddleware gravar decision
- `gateway/internal/auth/` — API key middleware + tenant resolution (Fase 2); Fase 5 consome tenant_id + data_class via ctx (sem modificação)
- `gateway/internal/config/` — env loader; Fase 5 adiciona `DCGM_EXPORTER_URL`, `SHED_LATENCY_RING_SIZE`, `SHED_TICK_INTERVAL_MS`, `SHED_DCGM_SCRAPE_INTERVAL_MS`, `SHED_DCGM_TIMEOUT_MS`
- `gateway/cmd/gatewayctl/upstreams.go` — subcomando pattern (Fase 3 D-D3); Fase 5 adiciona arquivos novos `shed.go` (shed-state, shed-force), `thresholds.go`, `tenants_shed.go`
- `gateway/db/migrations/0007_create_upstreams.sql` + `0008_seed_upstreams.sql` + `0009_upstreams_notify_trigger.sql` — base Fase 3; Fase 5 adiciona `0017_evolve_upstreams_shed_thresholds.sql` (seed UPDATE `circuit_config`)
- `gateway/db/migrations/0013_evolve_tenants_schedule_quota.sql` — Fase 4 tenants schedule; Fase 5 adiciona `0016_evolve_tenants_shedding_limits.sql` (ALTER ADD COLUMN + seed UPDATE)
- `gateway/db/queries/upstreams.sql` + `gateway/db/queries/tenants_admin.sql` — sqlc queries existentes; Fase 5 evolui para incluir novos campos
- `gateway/internal/integration_test/` — testcontainers harness (Fase 2/3/4); Fase 5 adiciona 5-6 cenários listados em Claude's Discretion
- `pod/docker-compose.yml` — dcgm-exporter :9400 configurado (Fase 1); Fase 5 consome via HTTP pull
- `pod/smoke/smoke.py` §DCGM scrape (linhas 141+) — referência de como parsear `DCGM_FI_DEV_FB_USED` no Prometheus text format

### Upstream components (HIGH confidence)

- https://pkg.go.dev/sync/atomic — atomic.Int64 para inflight counters (lockless hot path)
- https://github.com/sony/gobreaker — gobreaker v2 (já em uso; Fase 5 só consulta `.State()`, não estende)
- https://github.com/prometheus/client_golang — promauto collectors (CounterVec, GaugeVec)
- https://github.com/redis/go-redis — go-redis v9 (Pub/Sub + Hash para shed mirror)
- https://github.com/jackc/pgx — pgx v5 (sqlc queries; hot-reload via LISTEN é no upstreams loader existente)
- https://pkg.go.dev/log/slog — slog module=SHED

### External reference (ecosystem context)

- https://docs.nvidia.com/datacenter/dcgm/latest/dcgm-api/dcgm-field-ids.html — DCGM field IDs (FB_USED = VRAM bytes used; FB_FREE = free; FB_TOTAL = total). Pod expõe via dcgm-exporter.
- https://docs.vllm.ai/en/latest/serving/metrics.html — Pattern 1 reference (vllm:num_requests_waiting); llama.cpp não expõe equivalente direto (confirmado FEATURES.md)
- https://www.systemoverflow.com/learn/resilience-patterns/circuit-breaker/circuit-breaker-failure-modes-flapping-stampedes-and-retry-amplification — hysteresis rationale (linked Pitfall 2)
- https://medium.com/@khalilsayed/system-design-multi-tenant-rate-limiting-service-32c63ade5ec7 — noisy-neighbor multi-tenant reference (Pitfall 9)
- https://llmgateway.io/blog/how-we-handle-llm-provider-failover — streaming failover tradeoffs (contexto Pitfall 3 → reafirma Fase 5 D-D2)

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets

- **`gateway/internal/breaker/`** — template completo (package de 4 arquivos) para replicar em `gateway/internal/shed/`: in-process autoritativo + Redis mirror Hash + Pub/Sub subscriber + OnStateChange callback. Fase 5 copia a arquitetura e adapta para FSM de 4 estados (vs 3 estados do breaker gobreaker).
- **`gateway/internal/upstreams/loader.go`** — hot-reload via `circuit_config` JSONB já funciona; adicionar campos ao JSONB via `parseCircuitConfig` + consumer em `gateway/internal/shed/fsm.go` é transparente. Nenhuma mudança em loader.go.
- **`gateway/internal/upstreams/types.go` `CircuitConfig`** — struct existente ganha 5 campos novos (sheets_*). `CooldownS` → `time.Duration` pattern já estabelecido; replicar para `ShedArmSeconds`/`ShedRecoverSeconds` → `time.Duration`.
- **`gateway/internal/tenants/`** — loader + canal `tenants_changed` pronto; adicionar 4 colunas novas em `TenantConfig` struct + sqlc query é trivial.
- **`gateway/internal/proxy/dispatcher.go`** — pattern de `if breaker.State != Closed → tier-1 else tier-0`; Fase 5 adiciona segunda condição `else if shed.State == On && tenantInflight >= cap → tier-1`. Refactor mínimo (~10 linhas).
- **`gateway/internal/auditctx/`** — context override pattern (Fase 3 D-B3 `WithUpstreamOverride`); Fase 5 adiciona `WithShedDecision(ctx, reason)` na mesma filosofia; audit middleware lê via `ShedDecisionFromContext`.
- **`gateway/internal/obs/metrics.go`** — promauto pattern; adicionar ~11 collectors novos é mecânico.
- **`pod/smoke/smoke.py`** linha 141 — referência de parsing DCGM: `DCGM_FI_DEV_FB_USED`, `DCGM_FI_DEV_FB_FREE`, `DCGM_FI_DEV_FB_TOTAL` como Prometheus text format. Replicar parser mínimo em Go sem dep externa.
- **`gateway/internal/redisx/`** — client existente; adicionar funções `PublishShedEvent`, `SubscribeShedEvents`, `WriteShedState`, `GetShedForce` seguindo pattern breaker/mirror.go.
- **`gateway/internal/integration_test/`** — testcontainers harness Fase 2/3/4 com Postgres 16 + Redis 7; Fase 5 adiciona mock upstream HTTP server configurável (latency controlável, 500s controláveis) para SC-1/SC-2/SC-3/SC-4.

### Established Patterns

- **slog NDJSON** com `module=UPPER_SNAKE_CASE` — novos módulos: `module=SHED`, `module=SHED_FSM`, `module=SHED_MIRROR`, `module=DCGM`.
- **Sentinel errors pacote-level** — Fase 5 em `gateway/internal/shed/errors.go`: `ErrShedForceTTLExceeded`, `ErrShedSubscriberDisconnected`, `ErrDCGMScrapeFailed`, `ErrTenantCapExceeded`, `ErrSensitiveSaturated`, `ErrAllChatUpstreamsSaturated`, `ErrShedConfigInvalid`.
- **atomic ops no hot path** — inflight counters, ring buffer write index, FSM state field — zero RTT por request.
- **goose migrations sequenciais** — 0016, 0017, (0018 conditional) — com `SET search_path = ai_gateway` header.
- **sqlc type-safe codegen** — evoluir queries existentes (upstreams.sql, tenants_admin.sql) vs novo arquivo (unlikely).
- **testcontainers-go** com Postgres 16 + Redis 7 — Fase 5 adiciona mock HTTP server (net/http/httptest) configurável para simular saturação progressiva.
- **Sentry breadcrumbs** em transições de estado — pattern Fase 3 breaker; Fase 5 replica em FSM transições.
- **Conventional commits** `feat(05):`, `chore(05):`, `test(05):`.
- **Per-route middleware ordering** — chain declarativa em main.go (Fase 2 D-A3 + Fase 3 + Fase 4); Fase 5 insere `shedMiddleware` entre `scheduleMiddleware` e `tokencountMiddleware`.

### Integration Points

- **Pod `:9400` DCGM exporter** — novo consumer: goroutine dcgmScraper. Egress HTTP do gateway para pod-host:9400 precisa estar permitido no firewall da VPS gateway (já está na mesma rede privada que o pod; sem mudança de infra — confirmar em execute). Pod host é resolvido via env `DCGM_EXPORTER_URL` (ex: `http://178.156.150.XX:9400/metrics` ou hostname da rede interna).
- **DO Postgres (`ai_gateway` schema):**
  - ALTER `tenants` (D-B2 colunas novas + seed UPDATE)
  - UPDATE `upstreams.circuit_config` JSONB (D-A4 seed)
  - Conditional: ALTER `audit_log` enum/constraint se necessário (D-D4) — maioria schemas TEXT não precisa migration, só docs
- **Redis Ifix (namespace `gw:`):** novas chaves:
  - `gw:shed:{upstream}` — Hash com state
  - `gw:shed:events` — Pub/Sub channel
  - `gw:shed:force:{upstream}` — shadow state com TTL (operator override)
  - (Existentes mantidos: `gw:apikey:*`, `gw:idem:*`, `gw:breaker:*`, `gw:rate:*`, `gw:quota:*`, `gw:admin:*`, `gw:tokenize:*`)
- **Fase 6 consumes:** `gw:shed:events` Pub/Sub para leader-elected reconciler decidir spin-up emergency pod quando shed sustentado (ex: FSM=ON por >N minutos triggera `EMERGENCY_PROVISIONING`). Fase 5 publica contrato, Fase 6 consome.
- **Fase 7 consumes:**
  - `gateway_shed_state{upstream,state}`, `gateway_inflight_tenant{upstream,tenant}`, `gateway_shed_decisions_total`, `gateway_vram_used_bytes` — dashboard Next.js panels (OBS-03)
  - `audit_log.upstream IN ('shed_saturated','shed_blocked_sensitive','shed_tier1_unavailable')` — alerta WhatsApp/email (OBS-04)
  - `gw:shed:{upstream}` Redis Hash — live view no dashboard (OBS-01 JSON /admin/metrics)
  - `gateway_shed_force_active` gauge — alerta crítico "operator override ativo há mais de TTL"
- **Fase 8/9 consume:** apps cliente devem honrar `Retry-After` header em 503 shed — mesmo contrato Fase 4 rate-limit Retry-After.
- **gatewayctl ops surface:**
  - `shed-state` diagnostic (read-only, operator-frequent)
  - `shed-force` emergency override (rare use, TTL-bounded por segurança)
  - `thresholds set` tuning (expected moderate use pós-Phase 1 HUMAN-UAT baseline)
  - `tenant set-shed-limits` one-time configuration + occasional tuning

</code_context>

<specifics>
## Specific Ideas

- **Composite 2-of-3 é sweet spot para nossa escala (D-A1):** 6 tenants, 3 upstreams tier-0, 4 vCPU. OR-any com time-sustained ainda ruidoso se um sinal flutuar (P95 tem natural spikes por request cara); weighted score é over-engineering. Inflight-primary + confirmadores perde caso "GPU thrashing sem fila visível" (thermal throttling, noisy neighbor Vast.ai host). 2-of-3 força concordância de ≥2 sinais ortogonais antes de acionar — robusto sem ser conservador demais.

- **Ring buffer das últimas ~200 em vez de sliding window por tempo (D-A2):** Count-based é mais estável que time-based sob baixa carga — com 2 req/min, time-based 30s teria 1 amostra só. Count-based espera encher. Trade-off: sob alta carga (200 req/s), a janela é 1s — muito curta, sensível a ruído. Em execute, considerar dual-mode: count-based com `MAX(30s, 200 amostras)` time floor.

- **dcgmScraper fail-open é crítico para não acoplar fate-share (D-A3):** Se pod:9400 fica down (por exemplo, dcgm-exporter crash no container mas llama-server continua saudável), gateway não pode entrar em estado crítico inadimplente. Fail-open = sinal VRAM vira "unknown" e 2-of-3 se reduz a 1-of-2 (inflight + P95) — continua detectando saturação pelos sinais próprios, sem falso positivo por ausência de dado. Métrica `gateway_dcgm_scrape_failures_total` alerta a operador.

- **Estender `circuit_config` JSONB vs nova tabela (D-A4):** Tentação de normalizar com tabela `shedding_policies` dedicada. Mas: upstreams table JÁ existe com LISTEN/NOTIFY, gatewayctl JÁ edita, loader JÁ consome. Adicionar 5 campos num JSONB existente custa zero linhas de plumbing; tabela nova custa loader+listen+gatewayctl novos. Trade-off de "clean separation" vs "minimize novo código" pende forte para JSONB dada a maturidade do loader existente.

- **Hard cap per-tenant supera weighted fair-share para 6 tenants (D-B1):** Weighted fair-share é melhor em deployments com 50+ tenants e variação de demanda imprevisível. Com 6 tenants conhecidos e SLA explícito por tenant (Telefonia real-time > Campanhas batch), hard caps manuais + priority_tier metadata + runbook são suficientes. Pre-emption real fica deferida para v2 se operacional exigir — hoje v1 foca em "não-starvation", não "priorização estrita".

- **Sensitive+saturado = 503 imediato ≠ Fase 3 sensitive-retry (D-B3):** Fase 3 D-B1 fazia retry in-memory 4s para sensitive + breaker OPEN porque blips de breaker podem resolver em <4s (pod restart graceful). Saturação é diferente: ARMED exige 30s sustentado + ON continua, então por definição NÃO é blip — aguardar 4s não resolve. 503 direto é honest e libera handler.

- **Breaker wins shedding (D-C4) é coerente com "failover invisível" core value:** Se breaker OPEN, upstream está morto — shedding é irrelevante (requests iriam falhar no tier-0 de qualquer jeito). Precedência breaker → shed mantém lógica simples no dispatcher e previne duplicação de decisão. Single source of truth: breaker diz "upstream vivo ou morto?"; shedding diz "upstream saudável ou estressado?". Morto trumps estressado.

- **Streaming pre-dispatch only (D-D2) é resiliência-por-design:** Tentação de abortar streams em-flight quando FSM transita para ON (libera inflight local mais rápido). Mas Pitfall 3 proíbe: token já renderizado no cliente + replay = resposta duplicada. Pre-dispatch-only significa shedding é "diretivo para novas requests, não para em-flight" — alinha com breaker-open puro Fase 3 D-A1.

- **Hot-reload com re-avaliação preserva hysteresis (D-C5):** Tentação de "operator edita threshold e FSM reseta" — permite operador desligar shed rapidamente durante incidente. Mas reset sem RECOVERING perde garantia de estabilidade — se reload foi acidental, FSM oscila. Re-avaliação passa por RECOVERING (60s), que é exatamente a garantia que hysteresis estabelece. Operator que PRECISA forçar shed off usa `gatewayctl shed-force off --ttl 300s` — override explícito com TTL-bounded.

- **Audit valores reservados são discrimináveis para dashboard Fase 7:** `shed_saturated` vs `shed_blocked_sensitive` vs `shed_tier1_unavailable` permitem queries separadas em Fase 7:
  - "Quantos requests foram shedded por tenant esta semana?" → `upstream='shed_saturated' GROUP BY tenant`
  - "Quantos sensitive tenants bateram em saturação crítica?" → `upstream='shed_blocked_sensitive' GROUP BY tenant` (alerta WhatsApp por ser LGPD-relevant)
  - "Incidentes de chat totalmente indisponível?" → `upstream='shed_tier1_unavailable' GROUP BY date` (alerta crítico)

- **Per-upstream FSM (D-C2) é future-proof para Fase 9/10 quando tier-0 divergir:** Hoje local-llm/stt/embed são todos do mesmo pod — parece que poderia ser 1 FSM por role. Mas milestone v2+ pode separar pods (ex: STT em pod dedicado por volume diferente). Per-upstream FSM não muda; per-role FSM precisaria refactor. Custo marginal de 3 FSMs vs 1: nenhum.

- **Tuning de thresholds bloqueado por Phase 1 HUMAN-UAT:** Defaults D-A4 são chute educado; operador refinará via `gatewayctl thresholds set` depois que smoke.yml rodar em pod real e dados de baseline Phase 1 estiverem disponíveis. Plan da Fase 5 deve incluir task explícita "document threshold tuning runbook" mas implementação não bloqueada (defaults seguros).

</specifics>

<deferred>
## Deferred Ideas

- **Priority tier pre-emption real** (Tier-S preempt Tier-B slot durante FSM=ON) — Phase 5 só usa `priority_tier` como metadata para dashboard/runbook. Pre-emption real deferida para Fase 10 ou v2 se operacional mostrar starvation entre tiers.
- **Per-tenant FSM escopo** (18 FSMs = 6 tenants × 3 roles) — rejeitado em D-C2. Reconsiderar em v2 se sinais per-tenant (além do inflight) se tornarem relevantes.
- **Autoscaling de cap per-tenant baseado em histórico** (ex: ConverseAI aumenta cap durante horário de campanha) — v1 caps manuais. Fase 10+ pode adicionar "smart caps" derivados de padrão histórico de `billing_events`.
- **Abortar streams em-flight quando FSM transita ON** — rejeitado em D-D2 (Pitfall 3). Não reconsiderar — é decisão de design sólida.
- **Fallback-de-fallback para chat (tier-2 OpenAI direct)** — reafirmado em D-D1, consistente com Fase 3 D-C4. "Qwen fixo" é constraint PROJECT.md.
- **DCGM scrape via Redis key (pod publica)** — rejeitado em D-A3. Pod hoje não tem network para Redis Ifix; HTTP pull é mais simples e evita mudança no pod image.
- **Pull on-demand DCGM (em vez de periodic scraper)** — rejeitado em D-A3. Periodic 5s + cache é mais previsível.
- **Adiar VRAM signal para Phase 6+** — rejeitado em D-A3. LSH-02 explicita VRAM como componente; defaults seguros + fail-open já mitigam risco.
- **HDR/tdigest para P95** — rejeitado em D-A2. Overkill para 200 samples × 6 upstreams.
- **EWMA em vez de P95 real** — rejeitado em D-A2. SC-2 exige "P95 spike" literal.
- **Prometheus histogram_quantile para P95** — rejeitado em D-A2. Introduz self-scrape lag; ring buffer é exato e sem dep.
- **Tabela `shedding_policies` dedicada** — rejeitado em D-A4. JSONB `circuit_config` existente é mais direto.
- **Weighted fair-share (DRR/WRR)** — rejeitado em D-B1. Hard caps são mais predictable para 6 tenants conhecidos.
- **JSONB `shedding_config` em tenants em vez de colunas** — rejeitado em D-B2. Colunas permitem CHECK constraints + sqlc type-safety.
- **Nova tabela `tenant_inflight_limits`** — rejeitado em D-B2. Coerência com padrão `rps_limit`/`rpm_limit` já em tenants.
- **Retry in-memory 4s para sensitive saturado** — rejeitado em D-B3 (justificativa em Specifics — saturação não é blip).
- **Shed dentro do dispatcher** — rejeitado em D-B4. Middleware dedicado é mais testável e mantém chain declarativo.
- **Shed após tokencount** — rejeitado em D-B4. Tokencount deve rodar sobre upstream FINAL decidido, não intermediário.
- **3-estados FSM (sem ARMED)** — rejeitado em D-C1. Hysteresis só no caminho de volta é insuficiente (spike <30s causaria shed imediato).
- **Leaky-bucket counter em vez de FSM discreto** — rejeitado em D-C1. FSM é mais observável e debugável.
- **FSM escopo per-role** — rejeitado em D-C2.
- **In-process-only FSM state** — rejeitado em D-C3. Redis mirror habilita Fase 6 + Fase 7 sem refactor.
- **DB-persisted FSM state** — rejeitado em D-C3. Overkill; estado reconstrói em 30s.
- **Shed wins durante breaker HALF_OPEN** — rejeitado em D-C4. Complexidade sem ganho.
- **Independent check de breaker + shed** — rejeitado em D-C4. Precedência explícita é mais clara em audit.
- **Hot-reload com reset FSM** — rejeitado em D-C5. Perde garantia de hysteresis.
- **Hot-reload diferido até FSM OFF** — rejeitado em D-C5. Viola SC-3 literal.
- **Forcar dispatch local mesmo saturado** (D-D1 alternativa) — rejeitado. Viola core value.
- **Retry exp-backoff durante tier-1 indisponível** (D-D1 alternativa) — rejeitado. Apps cliente têm retry próprio.
- **Não aplicar shedding a streams** (D-D2 alternativa) — rejeitado. Streams são a carga principal de Qwen — shedding viraria inócuo.
- **Coluna nova `shed_reason` em audit_log** (D-D4 alternativa) — rejeitado. Valores reservados em `upstream` column + métrica são suficientes; migration pesada por low gain.
- **Shedding para STT/embed quando local saturado, apesar do tier-1 também estar fora** — STT tem fallback `openai-whisper` (Fase 3 D-C4 permite); embed tem fallback `openai-text-embedding-3-small`. D-D1 só fala de chat. STT/embed herdam fallback chain normal — shed vira "breaker-sim" antes da falha real.

### Reviewed Todos (not folded)

- **[STATE.md] Phase 1 HUMAN-UAT** (smoke.yml, Jinja tool-call, VRAM ceiling, cold-start) — bloqueia tuning empírico dos defaults de threshold mas não bloqueia execute da Fase 5. Threshold refinement pós-UAT é task isolada.
- **[STATE.md] Phase 6: Vast.ai REST API spike** — Fase 6; Fase 5 publica contrato Pub/Sub `gw:shed:events` que Fase 6 consumirá.
- **[STATE.md] Phase 7: Confirm Ifix WhatsApp provider** — Fase 7.
- **[STATE.md] Phase 7: Choose dashboard auth** — Fase 7.
- **[STATE.md] Phase 9: Obtain LGPD review sign-off** — Fase 9.
- **[STATE.md] Phase 2 SC-5 PARTIAL (post-push checklist + Portainer stack deploy)** — pré-requisito operacional; 03 e 04 HUMAN-UAT deferido mesmo.
- **[STATE.md] Phase 4 3 SC LIVE UAT deferred** — mesma dependência de Portainer stack deploy.

</deferred>

---

*Phase: 05-load-shedding-saturation-aware-routing*
*Context gathered: 2026-04-22*
