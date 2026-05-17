# Phase 6: Auto-provisioning Emergency Pod (Vast.ai) - Context

**Gathered:** 2026-05-13
**Status:** Ready for planning

<domain>
## Phase Boundary

Estende o gateway com um **reconciler de pod emergencial** que detecta quando o primário Vast.ai (`local-llm`) está fora por minutos e automaticamente provisiona, gerencia, e tear-down de um pod Vast.ai substituto — preservando o contrato OpenAI-compat das apps cliente sem ação humana e sem runaway de custo:

1. **State machine 7-estado** (PRV-02) — `HEALTHY → DEGRADED → FAILED_OVER → EMERGENCY_PROVISIONING → EMERGENCY_ACTIVE → RECOVERING → COOLDOWN`. *(Originalmente especificado como 9-estado incluindo OFF_HOURS + MAINTENANCE; reduzido para 7-estado em revisão BLOCKER 3 — ver `<deferred>`. Os 2 estados removidos não tinham triggers/transitions definidos em D-XX e exigiriam dependência nova do schedule middleware Phase 4.)*. In-process autoritativo + mirror Redis (`gw:emerg:state` Hash) + Pub/Sub (`gw:emerg:events`) — mesmo pattern do breaker Fase 3 D-D1 e do shedding Fase 5 D-C3. Tick FSM 1s.
2. **Leader-election via `go-redsync/redsync` distributed lock** (PRV-03) — `gw:emerg:lock`, TTL 30s, renew 10s. Apenas o leader avança o FSM, dispara provisioning ou destroy. Réplica não-leader observa estado via Pub/Sub e responde HTTP normalmente (proxying para upstreams seleccionados pelo dispatcher).
3. **Trigger por sustentação de FAILED_OVER** (PRV-04, SC-1) — quando `local-llm` breaker.OPEN persistir por `PROVISION_TRIGGER_FAILED_OVER_SECONDS` (default 120s, env-tunable), leader transita FSM para `EMERGENCY_PROVISIONING` e dispara fluxo Vast.ai. Sinal: breaker do `local-llm` (LLM é o anchor SLA — chat = workload crítico), não composição com STT/embed (over-trigger) nem com sheddingFSM=ON (saturação ≠ falha por boundary Fase 5).
4. **Vast.ai REST client em Go puro** (PRV-01) — sem Go SDK; chamadas diretas a `https://vast.ai/api/v0` com `Authorization: Bearer ${VAST_AI_API_KEY}`. Operações: `search_offers`, `create_instance` (`PUT /asks/{id}/`), `get_instance` (`GET /instances/{id}`), `destroy_instance` (`DELETE /instances/{id}`). Timeout HTTP **30s** (Vast lento sob load; 10s flaps, 60s desperdiça budget em chamadas mortas).
5. **Offer filter** (PRV-05, PRV-06, SC-5) — search filter: `gpu_name == "RTX 4090"` strict, `reliability ≥ 0.99`, `dph_total ≤ ${VAST_PRICE_CAP_DPH:-0.40}`, `inet_down ≥ 500 Mbps` (MinIO weight pull bottleneck), `cuda_max_good ≥ 12.4` (image dep Phase 1 D-04). Sort por `dph_total ASC`. Hosts diferentes do primário (`host_id != ${PRIMARY_HOST_ID}` quando conhecido).
6. **Bid race retry** — se `create_instance` retornar conflito (offer já aceita por outro buyer), retry **3 vezes** com nova `search_offers` entre tentativas; backoff exponencial 2s/4s/8s. Após 3 falhas: lifecycle abort + audit `shutdown_reason='offer_race_lost'` + Sentry alert.
7. **Image + onstart** (PRV-06) — imagem `ghcr.io/ifixtelecom/ifix-ai-pod:v1.0` (mesma do primário per Phase 1 D-04); `onstart` script já versionado faz `mc cp` weights de `s3.ifixtelecom.com.br/ai-gateway`. Cold-start budget: **10min** (≥ Phase 1 SC ceiling 5min × 2 grace; bate SC-1 ≤10min literal).
8. **Readiness check `/health` por modelo** (PRV-07) — antes de adicionar pod ao routing pool, verifica `GET ${pod_url}/health` retornando `{llm:true, stt:true, embed:true}`. Polling 5s interval, timeout total = `PROVISION_COLDSTART_BUDGET_SECONDS=600`. Falha → destroy + audit `shutdown_reason='health_timeout'`.
9. **Cancel-in-flight** (PRV-09, SC-3) — provisioning rodando dentro de `context.WithCancel`. Se `local-llm` recuperar (breaker.CLOSED via `gw:upstreams:events`) durante `EMERGENCY_PROVISIONING`: cancela context (interrompe polls e bid), e SE `vast_instance_id` já criado, chama `vast.destroy()` antes de retornar para HEALTHY. Pub/Sub `gw:emerg:events` broadcast cancel para non-leader. Triple-layer (context + pubsub + post-create destroy) garante zero leak.
10. **Cutback automático** (PRV-08, SC-4) — primário healthy por `PROVISION_HEALTHY_DURATION_SECONDS=300` (5min, env-tunable) → routing volta para `local-llm` tier-0; FSM = `RECOVERING`. Após mais `PROVISION_IDLE_GRACE_SECONDS=300` (5min idle, env-tunable) com pod emergencial sem traffic: `vast.destroy()` + FSM = `COOLDOWN` por mesma duração para evitar oscilação.
11. **Multi-failover sem oscilação** — uma vez em `EMERGENCY_PROVISIONING/ACTIVE`, novos breaker.OPEN events não resetam trigger nem criam pods adicionais. Lifecycle ride-out até cancel ou natural cutback. Apenas após `COOLDOWN` (5min) é que novo trigger pode disparar nova lifecycle. Previne "chatty failover → runaway provisions".
12. **Cost guardrails** (PRV-05) — preço cap hard-enforced: `if offer.dph_total > VAST_PRICE_CAP_DPH: skip`. Budget mensal: query agregada `SUM(total_cost_brl) FROM emergency_lifecycles WHERE started_at >= date_trunc('month', NOW())`; quando ultrapassa `MONTHLY_EMERGENCY_BUDGET_BRL` (env, default R$200) emite Sentry warning. Alerta WhatsApp/email fica para Fase 7 (dashboard consume table direto).
13. **Schema novo `ai_gateway.emergency_lifecycles`** (PRV-10) — tabela durável de auditoria:
    ```sql
    CREATE TABLE ai_gateway.emergency_lifecycles (
      id              BIGSERIAL PRIMARY KEY,
      started_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
      ended_at        TIMESTAMPTZ,
      trigger_reason  TEXT NOT NULL,           -- 'failed_over_sustained', 'manual_force'
      vast_offer_id   BIGINT,
      vast_instance_id BIGINT,
      accepted_dph    NUMERIC(6,4),
      total_cost_brl  NUMERIC(10,4),
      shutdown_reason TEXT,                     -- 'cutback_idle', 'cancelled_in_flight', 'health_timeout', 'offer_race_lost', 'manual', 'budget_exceeded'
      events          JSONB NOT NULL DEFAULT '[]'::JSONB,  -- [{ts, state, reason, ...}]
      leader_replica  TEXT
    );
    CREATE INDEX ON ai_gateway.emergency_lifecycles (started_at DESC);
    CREATE INDEX ON ai_gateway.emergency_lifecycles (ended_at) WHERE ended_at IS NULL;  -- live lifecycles
    ```
    Live lifecycles (ended_at IS NULL) ≤1 invariante (PRV-05). State row é Redis (live FSM); table é eventlog durável.
14. **Leader recovery on crash** — novo leader em próximo tick lê `emergency_lifecycles WHERE ended_at IS NULL`, valida instance via `vast.get(instance_id)`. Cenários: (a) instance ALIVE + healthy → resume FSM no estado correto baseado em events JSONB; (b) instance ALIVE mas zombie → destroy + close lifecycle `shutdown_reason='leader_recovery_zombie'`; (c) instance NOT FOUND → close lifecycle `shutdown_reason='leader_recovery_lost'`. Garante PRV-05 "no duplicated pods" mesmo com leader churn.
15. **gatewayctl extensions** — `gatewayctl emerg state` (FSM live + signals + active lifecycle), `gatewayctl emerg force-provision` (manual trigger override, bypass `failed_over` requirement), `gatewayctl emerg force-destroy` (manual destroy live pod, audit `shutdown_reason='manual'`), `gatewayctl emerg lifecycles --since 7d` (query table direto via leader Redis ou DB).
16. **Routing integration** — quando `EMERGENCY_ACTIVE`, dispatcher lê `emerg.ActivePodURL()` (in-process, mirror Redis) e injeta pod URL como tier-0 substituto para roles disponíveis. Pod novo + primário recovered = ambos tier-0 brevemente (durante `RECOVERING` 5min cutback grace) — primário ganha precedência. Após cutback completo: pod = removido do pool dispatcher.

**Fora de escopo desta phase:**
- Dashboard Next.js + WhatsApp/email alerts → Fase 7 (esta phase só expõe métrica + tabela; Fase 7 consome)
- Multi-region failover (BR fail → US pod) → v2 ou Fase 10 se operacional exigir
- Spot pricing dynamic auctioning beyond price cap → v1 simples cap + retry race
- Per-tenant emergency pod (pods dedicados a tenants premium) → v2; v1 = 1 pod compartilhado
- TLS termination no pod emergencial → image Phase 1 já handle (HTTP/HTTPS reverse proxy interno)
- Pre-warmed pod stand-by (idle pod consumindo budget para colar provisioning) → custo proibitivo (24/7 = ~R$300/mes ÷ ~5 emergências = R$60 por uso vs ~R$5-10 cold-start, anti-econômico)

</domain>

<decisions>
## Implementation Decisions

### A. Vast.ai REST client + offer selection

- **D-A1 (HTTP timeout 30s):** `vast.Client` HTTP timeout fixed em 30s para search/create/destroy/get. Vast REST API é lento sob load (search retorna 100+ offers JSON; create envolve queue). 10s gera flap quando provider está saturado; 60s desperdiça budget em retry de chamadas mortas. Configurável só via constante package-level (não env) — ajuste é raro.
- **D-A2 (Offer filter strict):** Search filter:
  ```
  gpu_name == "RTX 4090"
  reliability >= 0.99
  dph_total <= ${VAST_PRICE_CAP_DPH:-0.40}
  inet_down >= 500          # Mbps — MinIO weight pull (~6 GB) é gargalo se <500
  cuda_max_good >= 12.4     # image Phase 1 D-04 dep
  host_id != ${PRIMARY_HOST_ID}  # quando primário conhecido (não rejeita se primário desconhecido)
  ```
  Ordenação `sort=dph_total:asc&limit=20`. Geo BR/EU não é filter — Vast 4090 inventory escasso, geo strict rejeita demais.
  - **Por que não geo strict:** Vast inventory de 4090 com reliability ≥99% é escasso (~5-15 ofertas globais qualquer momento); strict BR/EU drop-rate alto demais → pode resultar em "no offers" → trigger fallback OpenRouter (que já é Fase 3 fallback) — defeats purpose do pod emergencial.
- **D-A3 (Bid race retry 3x exp backoff):** Se `create_instance` retorna 409/422 (offer já aceita), reexecuta `search_offers` (offer churn é segundos), pega top novo, retry. 3 attempts com backoff 2s/4s/8s. Após 3 falhas: lifecycle abort. Sentry warning + audit `shutdown_reason='offer_race_lost'`. Loop infinito proibido (custo + risk de aceitar overpriced offer em panic).
- **D-A4 (Cold-start budget 10min):** `PROVISION_COLDSTART_BUDGET_SECONDS=600` env (default 600). Polling `/health` a cada 5s. Bate SC-1 literal "≤10min once `/health` passes". Phase 1 baseline ~5min em cold pod novo + grace 5min para slow MinIO/inet variation. Falha = destroy + close lifecycle.
- **D-A5 (API key env):** `VAST_AI_API_KEY` env (já existe em CLAUDE.md token store + GitHub Secret repo `IfixTelecom/gpu-ifix`). Validação at boot (`vast.Ping()` chama `/users/current`); fail-loud no startup se inválido.

### B. FSM persistence + leader election

- **D-B1 (Redis namespace):** Mesmo pattern Phase 3 D-D1:
  - `gw:emerg:state` Hash — keys `state`, `lifecycle_id`, `pod_url`, `pod_instance_id`, `entered_at`
  - `gw:emerg:lock` — redsync key
  - `gw:emerg:events` Pub/Sub — JSON `{type, state, lifecycle_id, payload, ts, replica_id}`
  - Reusa cliente Redis existente (Fase 2 `gateway/internal/redis/`)
- **D-B2 (Leader lock TTL 30s + renew 10s):** `redsync.New(pool).NewMutex("gw:emerg:lock", redsync.WithExpiry(30*time.Second), redsync.WithTries(1), redsync.WithRetryDelay(0))`. Renew goroutine `mutex.Extend()` a cada 10s (1/3 TTL). Drift-tolerant; sobrevive Pub/Sub blip + Redis pause sem perder leadership. Se Extend falha → leader cede (set local flag `is_leader=false`); próximo tick alguma réplica volta a tentar `Lock`.
- **D-B3 (FSM tick 1s):** Goroutine `reconcilerTick` global ao gateway, ticker 1s. Mesmo cadência Phase 5 D-C1 `fsmTick`. Necessário para cancel-in-flight reactivity SC-3 (sub-2s). 5s/10s loses cancel reactivity.
- **D-B4 (Schema novo `emergency_lifecycles`):** Migration `0017_emergency_lifecycles.sql` (próxima após Phase 5 `0016_evolve_tenants_shedding_limits.sql`). Tabela definida em `<domain>` item 13. **NÃO reusa `audit_log`** — eventos múltiplos por lifecycle complica query "show me last 30 days of provisions"; lifecycle column-per-row é natural shape para Phase 7 dashboard timeline. **NÃO usa JSONB em `upstreams`** — runtime state em config table é anti-pattern (mutável vs imutável).
- **D-B5 (Live lifecycle invariant):** `CHECK` constraint OU partial unique index assegura no máximo 1 row com `ended_at IS NULL`:
  ```sql
  CREATE UNIQUE INDEX emergency_live_singleton ON ai_gateway.emergency_lifecycles ((TRUE)) WHERE ended_at IS NULL;
  ```
  Defense-in-depth contra race conditions. Leader-election + lock são primary; index é safety net.

### C. Trigger + cancel-in-flight + concurrency

- **D-C1 (Trigger threshold 120s default, env-tunable):** `PROVISION_TRIGGER_FAILED_OVER_SECONDS` env, default 120. Operador pode ajustar via Portainer env update sob outage sustentado (ex: 60s se SLA crítico naquele dia). Bate SC-1 example "e.g., 2 min".
- **D-C2 (Trigger signal = `local-llm` breaker.OPEN sustained):** Reconciler observa `gw:upstreams:events` (Phase 3 D-D1 Pub/Sub) e mantém in-memory timer per-upstream. Quando `local-llm` transita CLOSED→OPEN, inicia timer. Quando OPEN→HALF_OPEN ou OPEN→CLOSED, cancela timer. Quando timer expira (FAILED_OVER ≥ trigger_seconds), leader pode disparar EMERGENCY_PROVISIONING. **Não usa STT/embed breakers** — chat é SLA anchor; STT/embed down é menos crítico (apps degradam graceful via Phase 3 fallback). **Não usa sheddingFSM=ON** — saturação ≠ falha (Phase 5 boundary explícito); shedding já tem destino tier-1 (OpenRouter), não precisa pod novo.
- **D-C3 (Cancel-in-flight: triple layer):** Implementação:
  1. **Context cancel** — provisioning rodando em goroutine `go provisionLifecycle(ctx, ...)` com `ctx, cancel := context.WithCancel(reconcilerCtx)`. Quando `local-llm` recovers, leader chama `cancel()`.
  2. **Pub/Sub broadcast** — leader publica `{type: 'cancel_in_flight', lifecycle_id}` em `gw:emerg:events`. Réplicas non-leader recebem e atualizam in-memory state (visibility — não toma ação).
  3. **Post-create destroy** — `provisionLifecycle` checa `ctx.Err()` em cada checkpoint (após search, após create, durante poll /health). Se cancelled APÓS `create_instance` succeeded, executa `vast.destroy(instance_id)` antes de retornar; lifecycle row close `shutdown_reason='cancelled_in_flight'`.
  - Garante zero leak: pre-create cancel = nada criado; post-create cancel = destroy guaranteed.
- **D-C4 (Multi-failover ride-out):** Uma vez `EMERGENCY_PROVISIONING/ACTIVE`, novos breaker.OPEN events de `local-llm` NÃO criam novas lifecycles, NÃO resetam trigger. Razão: provisioning leva minutos; chatty oscilação resetando produziria múltiplas instâncias = budget runaway. Cancel só por `local-llm` recovery sustentado (D-C3). Após cutback + COOLDOWN (5min), trigger volta a estar armed.
- **D-C5 (Concurrency guards):** Defense-in-depth:
  1. Leader lock (D-B2) — só leader avança FSM
  2. DB partial unique index (D-B5) — no máximo 1 live lifecycle
  3. Reconciler check — antes de `EMERGENCY_PROVISIONING` transition, leader query `SELECT COUNT(*) FROM emergency_lifecycles WHERE ended_at IS NULL` — se ≥1, abort transition (log error, continue HEALTHY/DEGRADED state — humano investiga via gatewayctl)

### D. Cutback + cost guardrails + audit

- **D-D1 (Cutback timings env-tunable, defaults 5min/5min):** `PROVISION_HEALTHY_DURATION_SECONDS=300` + `PROVISION_IDLE_GRACE_SECONDS=300`. Bate SC-4 literal. Env-tunable porque operador sob cost pressure pode shorten (ex: 60s/60s) para tear-down agressivo. Defaults conservadores garantem SC-4.
- **D-D2 (Monthly budget alert via Sentry):** Reconciler tick (1s já caro; faz check cada 60s) executa:
  ```sql
  SELECT COALESCE(SUM(total_cost_brl), 0) AS month_cost
  FROM ai_gateway.emergency_lifecycles
  WHERE started_at >= date_trunc('month', NOW())
    AND ended_at IS NOT NULL;
  ```
  Quando `month_cost > MONTHLY_EMERGENCY_BUDGET_BRL` (env, default 200): emite Sentry `WARNING` com tag `month_cost`, `budget`. Apenas 1 alerta por dia (in-memory dedupe). NÃO bloqueia provisioning futuro automaticamente — operador decide.
  - **NÃO bloqueia automaticamente:** trade-off — se SLA crítico durante outage atravessa budget, app cliente quebrar é pior que estourar R$200. Operador faz manual `gatewayctl emerg force-stop` se quiser hard-stop.
  - WhatsApp alert = Fase 7 (consume table).
- **D-D3 (Lifecycle event log JSONB):** Cada transição append `{ts, from_state, to_state, reason, payload}` em `events JSONB`. Exemplos payload: `{offer_id, dph}` em accept, `{instance_id}` em create, `{health_check_count, last_response}` em ready, `{idle_seconds}` em destroy. Render Phase 7 timeline trivial. Query "give me the offer that was accepted in lifecycle X" = `SELECT events FROM ... WHERE id=X` + JS filter (table só guarda summary `accepted_dph` para fast monthly aggregate).
- **D-D4 (Total cost calc):** Quando lifecycle close, `total_cost_brl = (DPH × hours_active) × USD_TO_BRL_RATE`. `USD_TO_BRL_RATE` env (default 5.0; operador atualiza trimestralmente; Phase 4 já tem analog para token cost). `hours_active = (ended_at - first_health_pass_at).TotalHours()` — só conta horas que pod serviu, não o cold-start (Vast cobra mas conta é de "uso útil" para audit, não para Vast bill — Vast bill é informativo separado).
- **D-D5 (Leader recovery 3 cenários):** Spec em `<domain>` item 14. Implementação `reconcilerCtx.OnLeaderAcquired()` callback chama `recoverOrphanLifecycles()`:
  ```go
  func recoverOrphanLifecycles(ctx context.Context) error {
    rows, _ := db.Query(`SELECT id, vast_instance_id FROM ai_gateway.emergency_lifecycles WHERE ended_at IS NULL`)
    for each row {
      if row.vast_instance_id IS NULL { closeOrphan(row.id, 'leader_recovery_pre_create'); continue }
      inst, err := vast.GetInstance(row.vast_instance_id)
      if err == ErrNotFound { closeOrphan(row.id, 'leader_recovery_lost'); continue }
      if !inst.IsActive() { vast.Destroy(row.vast_instance_id); closeOrphan(row.id, 'leader_recovery_zombie'); continue }
      // active + healthy: resume FSM
      resumeFSMFromEvents(row)
    }
  }
  ```
  Garante PRV-05 "no duplicated pods" mesmo com leader churn.

### E. gatewayctl + observability + integration

- **D-E1 (gatewayctl emerg subcommands):** Quatro novos subcomandos:
  - `gatewayctl emerg state` → JSON com `state`, `lifecycle_id`, `pod_url`, `entered_at`, `is_leader_replica`, `last_signals` (LLM breaker state, sustained_failed_over_seconds)
  - `gatewayctl emerg force-provision [--reason "manual smoke"]` → bypass trigger, transita `HEALTHY → EMERGENCY_PROVISIONING` direto. Audit `trigger_reason='manual_force'`. Apenas leader-replica accept; non-leader exit error.
  - `gatewayctl emerg force-destroy` → leader destroy live pod imediato; audit `shutdown_reason='manual'`.
  - `gatewayctl emerg lifecycles [--since 7d] [--limit 50]` → query DB `SELECT id, started_at, ended_at, accepted_dph, total_cost_brl, shutdown_reason FROM emergency_lifecycles ORDER BY started_at DESC LIMIT N`. Pretty-print table.
- **D-E2 (Métricas Prometheus):** Novas em `gateway/internal/obs/metrics.go`:
  - `gateway_emergency_state` (gauge, label `state`) — 1 para current state, 0 outros
  - `gateway_emergency_lifecycles_total` (counter, labels `trigger_reason`, `shutdown_reason`) — count rollup
  - `gateway_emergency_active_pod` (gauge, label `pod_url`) — 1 quando pod live, 0 caso contrário
  - `gateway_emergency_provision_duration_seconds` (histogram) — tempo de search→ready
  - `gateway_emergency_cost_dph` (gauge, label `lifecycle_id`) — current pod cost por hora
  - `gateway_emergency_month_cost_brl` (gauge) — running monthly aggregate
  - `gateway_vast_api_requests_total` (counter, labels `op`, `status`) — operacional
- **D-E3 (Dispatcher integration):** `emerg.ActivePodURL()` retorna `(string, bool)`. Quando bool=true, dispatcher:
  ```go
  if emerg.IsActive() {
    activePod := emerg.ActivePodURL()
    upstreams.OverrideTier0("local-llm", activePod)  // chat only - emergency pod só serve LLM por enquanto
    // STT/embed continuam apontando para primário ou fallback existente
  }
  ```
  Após cutback completo: `upstreams.RestoreTier0("local-llm")` reverte. Race-free via in-process atomic.Pointer[string]. Apenas `local-llm` é overridden por enquanto (STT/embed: primário 4090 hospeda os 3, mas emergency pod faz a mesma imagem — operador pode habilitar STT/embed override em v2; chat é o caminho crítico).
- **D-E4 (Sentry breadcrumbs):** Cada transição FSM emite `sentry.AddBreadcrumb({category: 'emergency', level: ..., message: 'state X→Y'})`. Estados de erro (`offer_race_lost`, `health_timeout`, `leader_recovery_zombie`, `budget_exceeded`) também emitem `sentry.CaptureException` ou `CaptureMessage` com tags `lifecycle_id`, `pod_instance_id` para forensic.

### Claude's Discretion

- **Goroutine layout, channel design, package boundaries** dentro de `gateway/internal/emerg/` (`fsm.go`, `vast/client.go`, `reconciler.go`, `lifecycle.go`, `recovery.go`) — Claude estrutura no plan-phase. Pattern Phase 5 `internal/shed/` é referência.
- **Specific HTTP retry policies** dentro de `vast.Client` (read errors, timeouts, transient 5xx) — Claude usa stdlib `net/http` + custom `RoundTripper` ou `cenkalti/backoff/v4` (deja em go.mod via Phase 3 D-A1).
- **DB query implementation** (`sqlc` para queries novas vs raw `pgx` direto) — Claude segue convenção Phase 4 sqlc per-query files.
- **Test layout**: integration tests com Vast.ai sandbox (se existe) ou httptest.Server mocking. Claude avalia em plan-phase. Mínimo: 5 cenários (provision happy path, cancel-in-flight pre-create, cancel-in-flight post-create, leader recovery zombie, budget alert).
- **Rate limiting do Vast.ai client** — não documentado público; Claude implementa token bucket conservador (1 req/s default) com observação de 429 responses. Configurável via env.

</decisions>

<code_context>
## Existing Code Insights

### Reusable Assets

- **`gateway/internal/redis/`** (Phase 2) — Redis client wrapper, conexão pool já configurada
- **`gateway/internal/breaker/`** (Phase 3) — gobreaker v2 wrapper + Redis mirror + Pub/Sub subscriber. Pattern de "in-process autoritativo + Redis mirror + Pub/Sub events" é o template direto para `internal/emerg/`
- **`gateway/internal/upstreams/`** (Phase 3 D-D1, Phase 5 hot-reload) — loader pgxlisten + tipos para upstreams; Phase 6 estende com `OverrideTier0(name, url)` + `RestoreTier0(name)` métodos
- **`gateway/internal/shed/`** (Phase 5) — FSM 4-estado + tick 1s goroutine + atomic state ops + RWMutex pattern. Pattern direto para `emerg/fsm.go` (FSM 7-estado — reduzido de 9 em revisão BLOCKER 3)
- **`gateway/internal/obs/metrics.go`** — Prometheus registry + counter/gauge helpers já registrados (Phase 2 D-A4 baseline + Phase 3-5 extensions)
- **`gateway/internal/db/`** + **`gateway/internal/db/queries/`** — sqlc generated queries pattern; Phase 6 adiciona queries novas para emergency_lifecycles
- **`gateway/cmd/gatewayctl/`** — CLI binary, mesmo image que gateway server (D-08 Phase 2 distroless 27.7 MB compartilhado). Pattern de subcomandos extendido por Phase 3 D-G1 (`upstreams`), Phase 4 (`tenants`, `quotas`), Phase 5 (`shed-state`, `shed-force`, `thresholds`, `tenant set-shed-limits`)

### Established Patterns

- **In-process autoritativo + Redis mirror + Pub/Sub** — pattern instituído Phase 3 D-D1 (breakers) e replicado Phase 5 D-C3 (shed). Phase 6 segue identical.
- **Hot-reload via NOTIFY/Pub/Sub** — Phase 3 D-D4 `upstreams_changed` + Phase 4 D-C4 `tenants_changed`. Phase 6 não adiciona novo channel (config emergency vem só de env vars).
- **Migration via `goose` + sqlc generation** — Phase 2 D-08 `AI_GATEWAY_MIGRATE_ON_BOOT` flag aplica no boot. Phase 6 `0017_emergency_lifecycles.sql` segue.
- **gatewayctl subcomandos exec via docker** — `docker exec ifix-ai-gateway /gatewayctl emerg state` é o ops model (Phase 2 D-08).
- **Env vars validados via Zod-equivalente Go** — Phase 2 cria `internal/config/config.go` parsing env at boot. Phase 6 estende.
- **Sentry breadcrumbs + CaptureException** — Phase 3 D-G2 instituiu pattern para breakers. Phase 6 segue identical.

### Integration Points

- **Dispatcher** (`gateway/internal/proxy/dispatcher.go` Phase 3 D-A1) — chama `emerg.IsActive()` antes de selecionar tier-0; quando true, override URL para `local-llm`
- **Breaker subscriber** (`gateway/internal/breaker/subscriber.go` Phase 3 D-D1) — Phase 6 reconciler subscribe ao mesmo `gw:upstreams:events` channel para detectar `local-llm` state changes
- **Audit middleware** (Phase 4) — log de routing decisions; Phase 6 adiciona `audit_log.upstream='emergency_pod'` quando dispatcher routa via emergency pod URL
- **Sentry init** (Phase 2 D-A4) — `internal/obs/sentry.go` já tem hub global; Phase 6 só usa
- **gatewayctl** — adiciona `emerg.go` subcommand handler em `cmd/gatewayctl/`
- **CI build** (`.github/workflows/build-gateway.yml` Phase 2 D-08) — sem mudança; build estende imagem distroless atual
- **Portainer stack `ai-gateway-dev`** — adiciona env vars novas (`VAST_AI_API_KEY`, `VAST_PRICE_CAP_DPH`, `MONTHLY_EMERGENCY_BUDGET_BRL`, `PROVISION_*_SECONDS`, `USD_TO_BRL_RATE`); operador atualiza via Portainer UI

</code_context>

<specifics>
## Specific Ideas

- **Vast.ai API key já provisionada** — `***REMOVED-VAST-API-KEY-see-CLAUDE.md***` em CLAUDE.md token store + GitHub Secret `VAST_AI_API_KEY` (já usado por `pod/scripts/vast-ai.sh` Phase 1 e `.github/workflows/smoke.yml`). Reusa direto.
- **MinIO weights endpoint** — `https://s3.ifixtelecom.com.br/ai-gateway` (CLAUDE.md token store). `onstart` script já implementado Phase 1.
- **Image label** — `ghcr.io/ifixtelecom/ifix-ai-pod:v1.0` ou `:latest` (Phase 1 publica ambas). Use `:v1.0` para reproducibility. Operador atualiza tag via env `EMERGENCY_POD_IMAGE_TAG` (default `v1.0`).
- **Reference research timeboxed (3h spike)** — STATE.md TODO: "Phase 6: Timeboxed (3h) Vast.ai REST API spike before committing the phase scope". gsd-phase-researcher executa antes do plan, com foco: bid acceptance race semantics, instance lifecycle states, port exposure timing (`/health` reachable when), price cap enforcement edge cases.
- **vast-ai.sh script existe** (`pod/scripts/vast-ai.sh` Phase 1) — bash CLI para search/create/destroy. Não reusado em runtime (Go client é PRV-01); mas é referência de "como Vast API responde" durante research.

</specifics>

<deferred>
## Deferred Ideas

- **Multi-region failover** (BR fail → US pod) — v2 ou Fase 10 se operacional exigir
- **Pre-warmed pod stand-by** — custo proibitivo, anti-econômico (R$60/uso vs R$5-10 cold-start)
- **Per-tenant emergency pod** (premium tenants ganham pod dedicado) — v2; v1 = 1 pod compartilhado
- **Spot pricing dynamic auctioning** — v1 simples cap + retry race
- **Emergency pod hosting STT/embed também** (override completo dos 3 roles) — v2 se metric mostra LLM-only override deixa gaps; v1 = só LLM (chat = SLA crítico)
- **Auto-rollback dispatcher override on N consecutive failures** — v1 humano via `gatewayctl emerg force-destroy`; v2 se métrica mostra padrão recorrente
- **Phase 7 dashboard timeline render** — esta phase só expõe `events JSONB`; consume em Phase 7
- **WhatsApp/email alerting** — Phase 7
- **Vast.ai client rate limit observation/auto-throttle** baseado em headers `X-RateLimit-*` (se Vast retornar) — v1 conservador 1 req/s; v2 adaptive
- **FSM state OFF_HOURS** — originalmente listado em domain item 1 (9-estado spec); reduzido para 7-estado em revisão BLOCKER 3 (2026-05-13). Nenhum D-XX definia trigger/transitions; implementação requer dependência nova do `tenants_changed` channel + scheduleService da Fase 4. Reativar quando operacional pedir suspensão automática de provisioning fora de horários de pico.
- **FSM state MAINTENANCE** — originalmente listado em domain item 1 (9-estado spec); reduzido para 7-estado em revisão BLOCKER 3 (2026-05-13). Não tinha gatewayctl subcomando definido (apenas 4 subcomandos D-E1: state/force-provision/force-destroy/lifecycles). Reativar quando operacional pedir window de manutenção que suprima provisioning sem desabilitar VAST_AI_API_KEY.

</deferred>
