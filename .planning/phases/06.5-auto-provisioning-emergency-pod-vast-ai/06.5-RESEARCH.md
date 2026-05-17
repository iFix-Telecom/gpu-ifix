# Phase 6: Auto-provisioning Emergency Pod (Vast.ai) - Research

**Researched:** 2026-05-13
**Domain:** Vast.ai REST orchestration + Go reconciler FSM + Redis distributed leader election
**Confidence:** HIGH (in-house patterns); MEDIUM (Vast.ai API edge cases — race semantics not officially documented, only observable empirically)

## Summary

A Phase 6 emergency-pod reconciler em Go composto por: (1) cliente REST puro para a Vast.ai (`https://vast.ai/api/v0`); (2) FSM de 9 estados in-process autoritativo + mirror Redis Hash + Pub/Sub events (mesmo template Phase 3 D-D1 + Phase 5 D-C3); (3) leader-election via `go-redsync/redsync v4` (TTL 30s, renew a cada 10s); (4) tabela durável `ai_gateway.emergency_lifecycles` com partial unique index para garantir 1 live lifecycle; (5) integração com dispatcher via `upstreams.OverrideTier0`/`RestoreTier0` chamados de uma `atomic.Pointer[string]` em `internal/emerg`.

A maior parte do trabalho de Phase 6 é **replicação direta de patterns existentes**: o breaker subscriber + shed FSM cobrem 100% da arquitetura Pub/Sub-mirror, o testcontainers harness Phase 4 cobre o test infra, e o `pod/scripts/vast-ai.sh` Bash CLI Phase 1 documenta o shape exato de cada endpoint Vast (corpo de request, JSON de resposta, edge cases já observados no smoke.yml). A novidade real é: leader-election (não existia antes), FSM 9-estado (vs 4 do shed), 3-camadas de cancel-in-flight, e leader-recovery on crash.

**Primary recommendation:** Estruturar `gateway/internal/emerg/` em 6 arquivos paralelos a `internal/shed/` (`fsm.go`, `set.go` ou `reconciler.go`, `subscribe.go`, `mirror.go` namespace, `lifecycle.go`, `recovery.go`) + `gateway/internal/emerg/vast/` sub-package para o cliente REST (`client.go` + `types.go` + `errors.go`). Vast.ai API é menos documentada que assumido — investir 20% do tempo de research em **respostas reais** capturadas durante o spike (ver `## Open Questions for Planner`). Para dúvida real de race semantics: usar `httptest.Server` com fixtures Recordadas + 1 burst test E2E com pod real $0.10 (cap em CI) durante UAT.

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| FSM 9-state machine | Gateway / API | — | Core orchestrator lives in-process per replica; in-process autoritativo + Redis mirror (Phase 3+5 invariant) |
| Vast.ai REST client | Gateway / API | — | Não existe SDK Go oficial (PRV-01 explicita); chamadas HTTP diretas desde o gateway |
| Leader election | Redis | DB partial unique index (defense-in-depth) | Redis = primary lock authority (1ms ops); DB index = belt-and-suspenders contra split-brain |
| Lifecycle audit log | Database (Postgres) | — | Durável > 30d para Phase 7 dashboard; JSONB events column captura timeline |
| Live FSM state mirror | Redis | — | Cross-replica visibility (non-leader observa via Pub/Sub); ephemeral, NOT durable |
| Pod runtime | Vast.ai (external) | — | Imagem `ghcr.io/ifixtelecom/ifix-ai-pod:v1.0` Phase 1; gateway só invoca lifecycle |
| Health verification | Pod `/health` (port 9100) | Gateway probe goroutine | Gateway poll diretamente o `/health` do pod recém-criado; dispatcher só roteia após PASS |
| Routing override | Dispatcher (in-process, atomic.Pointer) | upstreams.Loader (snapshot) | `OverrideTier0("local-llm", podURL)` sobrepõe snapshot sem re-load DB |
| Cost calculation | Gateway (in-process, lifecycle close) | Postgres (`emergency_lifecycles.total_cost_brl`) | DPH × hours_active × USD_TO_BRL_RATE; Phase 4 analog em `billing/cost.go` |
| Operator interface | gatewayctl (Go binary, mesma image) | — | `docker exec ifix-ai-gateway /gatewayctl emerg ...`; mesmo padrão Phase 4/5 |
| Sentry forensics | Sentry (`sentry-go v0.29.1` no go.mod) | slog | Breadcrumbs por transição FSM + CaptureMessage para terminal errors (offer_race_lost, health_timeout, leader_recovery_zombie, budget_exceeded) |

## User Constraints (from CONTEXT.md)

### Locked Decisions

**Section A — Vast.ai REST client + offer selection:**
- D-A1: HTTP timeout fixo 30s para todas as ops (search/create/destroy/get); package-level constant, NOT env-tunable
- D-A2: Offer filter strict — `gpu_name="RTX 4090"`, `reliability ≥ 0.99`, `dph_total ≤ ${VAST_PRICE_CAP_DPH:-0.40}`, `inet_down ≥ 500 Mbps`, `cuda_max_good ≥ 12.4`, `host_id != ${PRIMARY_HOST_ID}` quando conhecido. Sort `dph_total ASC limit 20`. **NÃO** filtra geo (4090 inventory escasso).
- D-A3: Bid race retry — 3x exp backoff 2s/4s/8s, com nova `search_offers` entre tentativas; após 3 falhas: lifecycle abort + `shutdown_reason='offer_race_lost'` + Sentry warning
- D-A4: Cold-start budget `PROVISION_COLDSTART_BUDGET_SECONDS=600` env (default 600 = 10min); polling /health a cada 5s
- D-A5: API key via env `VAST_AI_API_KEY` (já existe em GitHub Secrets); validação via `vast.Ping()` chama `/users/current` no boot

**Section B — FSM persistence + leader election:**
- D-B1: Redis namespace `gw:emerg:*` (state Hash + lock + events Pub/Sub); reusa `internal/redisx` client
- D-B2: redsync TTL 30s, Extend renew a cada 10s (1/3 TTL); leader cede em Extend fail; non-leader retry Lock no próximo tick
- D-B3: FSM tick 1s (mesma cadência Phase 5 D-C1)
- D-B4: Schema novo `emergency_lifecycles` em migration `0019_emergency_lifecycles.sql` — corrigindo o número (CONTEXT diz 0017 mas Phase 5 já consumiu 0017+0018; próxima livre = 0019)
- D-B5: Partial unique index `emergency_live_singleton ON ((TRUE)) WHERE ended_at IS NULL` — defesa em camadas

**Section C — Trigger + cancel-in-flight + concurrency:**
- D-C1: Trigger threshold `PROVISION_TRIGGER_FAILED_OVER_SECONDS` env, default 120
- D-C2: Trigger signal = `local-llm` breaker.OPEN sustained (NÃO STT/embed, NÃO sheddingFSM=ON)
- D-C3: Cancel-in-flight triple layer (context cancel + Pub/Sub broadcast + post-create destroy)
- D-C4: Multi-failover ride-out (uma vez ACTIVE, novos breaker.OPEN não criam novas lifecycles)
- D-C5: Concurrency guards triple (leader lock + DB unique index + reconciler check)

**Section D — Cutback + cost guardrails + audit:**
- D-D1: `PROVISION_HEALTHY_DURATION_SECONDS=300` + `PROVISION_IDLE_GRACE_SECONDS=300` (env-tunable)
- D-D2: Monthly budget alert via Sentry warning; check a cada 60s; in-memory dedupe 1 alerta/dia; **NÃO** bloqueia provisioning automaticamente
- D-D3: Lifecycle event log JSONB append-only `events` column
- D-D4: Total cost = `(DPH × hours_active) × USD_TO_BRL_RATE`; `hours_active = (ended_at - first_health_pass_at)`; `USD_TO_BRL_RATE` env (default 5.0)
- D-D5: Leader recovery 3 cenários (instance ALIVE+healthy → resume; ALIVE+zombie → destroy; NOT_FOUND → close)

**Section E — gatewayctl + observability + integration:**
- D-E1: 4 subcomandos (`state`, `force-provision`, `force-destroy`, `lifecycles`)
- D-E2: 7 métricas Prometheus novas em `obs/metrics.go`
- D-E3: Dispatcher integration via `upstreams.OverrideTier0("local-llm", podURL)` race-free atomic.Pointer; só LLM (NÃO STT/embed em v1)
- D-E4: Sentry breadcrumbs por transição + CaptureMessage em estados de erro

### Claude's Discretion

- Goroutine layout, channel design, package boundaries dentro de `internal/emerg/` — Claude estrutura no plan-phase
- Specific HTTP retry policies dentro de `vast.Client` — pode usar stdlib `net/http` + custom RoundTripper OR `cenkalti/backoff/v5` (já em go.mod via Phase 3)
- DB query implementation (sqlc per-query files vs raw pgx) — segue convenção Phase 4
- Test layout (sandbox Vast.ai vs httptest.Server) — Claude avalia; mínimo 5 cenários (provision happy path, cancel-in-flight pre-create, cancel-in-flight post-create, leader recovery zombie, budget alert)
- Rate limiting do Vast.ai client — não documentado explicitamente; Claude implementa token bucket conservador (1 req/s default) com observação 429

### Deferred Ideas (OUT OF SCOPE)

- Multi-region failover (BR fail → US pod) — v2 ou Phase 10
- Pre-warmed pod stand-by — custo proibitivo (R$60/uso)
- Per-tenant emergency pod (premium dedicado) — v2; v1 = 1 pod compartilhado
- Spot pricing dynamic auctioning beyond price cap — v1 simples cap+retry
- Emergency pod hosting STT/embed também — v2; v1 = só LLM (chat = SLA crítico)
- Auto-rollback dispatcher override on N consecutive failures — v1 humano via `gatewayctl emerg force-destroy`
- Phase 7 dashboard timeline render
- WhatsApp/email alerting — Phase 7
- Vast.ai client rate limit observation/auto-throttle baseado em headers `X-RateLimit-*` (Vast NÃO retorna esses headers — confirmado WebFetch — então não é viável adaptar)

## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| PRV-01 | Vast.ai REST client em Go puro (search/create/destroy/get) — sem SDK | Section "Vast.ai REST API Reference" + bash CLI `pod/scripts/vast-ai.sh` (Phase 1) document endpoint shape |
| PRV-02 | FSM 9-estado persistida no Redis | Section "Internal patterns to reuse" — replica Phase 5 `shed/fsm.go` atomic.Int32 + atomic.Int64 enteredAt; mirror Hash via `redisx` per Phase 3 |
| PRV-03 | Leader-election via `go-redsync/redsync` | Section "go-redsync v4 patterns" + Vast.ai key already in env |
| PRV-04 | Trigger sustentado por X segundos | Section "Internal patterns to reuse" / breaker subscriber — `gw:upstreams:events` (NOTE: `gw:breaker:events` per real code) Pub/Sub channel já existe |
| PRV-05 | Guardrails (price cap $0.40/h, 1-pod-singleton, monthly budget alert) | DB partial unique index pattern + Phase 4 cost calc analog (`billing/cost.go`) |
| PRV-06 | Reliability ≥99% + image pré-construída | Phase 1 onstart.sh + image `ghcr.io/ifixtelecom/ifix-ai-pod:v1.0` |
| PRV-07 | Readiness `/health` por modelo | Phase 1 health-bridge port 9100 — endpoint shape `{status, services{llm,stt,embed}, uptime_s, timestamp}` |
| PRV-08 | Cutback automático 5min healthy + 5min grace | env-tunable durations + reconciler tick |
| PRV-09 | Cancel-in-flight triple layer | Section "Internal patterns to reuse" + context.WithCancel pattern |
| PRV-10 | Audit log completo por lifecycle | Schema `emergency_lifecycles` + JSONB events column |

## Standard Stack

### Core (already in go.mod)

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `github.com/sony/gobreaker/v2` | v2.4.0 | Circuit breaker em `internal/breaker` (já consumido em Phase 3) — Phase 6 NÃO adiciona breakers, só **subscreve** ao Pub/Sub `gw:breaker:events` para detectar `local-llm` OPEN sustained | [VERIFIED: gateway/go.mod] |
| `github.com/redis/go-redis/v9` | v9.18.0 | Redis client; Phase 6 consome via `internal/redisx.NewClient` | [VERIFIED: gateway/go.mod] |
| `github.com/jackc/pgx/v5` | v5.7.1 | Postgres driver para emergency_lifecycles INSERT/UPDATE/SELECT | [VERIFIED: gateway/go.mod] |
| `github.com/jackc/pgxlisten` | v0.0.0-20250802... | Não usado em Phase 6 (config emergency vem de env, não de tabela hot-reloadable) | [VERIFIED: gateway/go.mod] |
| `github.com/getsentry/sentry-go` | v0.29.1 | Breadcrumbs + CaptureMessage para forensics | [VERIFIED: gateway/go.mod + internal/obs/sentry.go] |
| `github.com/prometheus/client_golang` | v1.20.5 | 7 novas métricas em `obs/metrics.go` | [VERIFIED: gateway/go.mod] |
| `github.com/google/uuid` | v1.6.0 | Não estritamente necessário para emergency_lifecycles (BIGSERIAL PK), mas consistente com pattern existente | [VERIFIED: gateway/go.mod] |
| `github.com/pressly/goose/v3` | v3.23.0 | Migration runner (já roda no boot via `AI_GATEWAY_MIGRATE_ON_BOOT`) | [VERIFIED: gateway/go.mod] |
| `github.com/cenkalti/backoff/v5` | v5.0.3 | Phase 6 pode usar para HTTP retry exponencial dentro do `vast.Client` (Claude's discretion) | [VERIFIED: gateway/go.mod] |
| `log/slog` | stdlib | Logging estruturado, módulo `EMERG` / `EMERG_FSM` / `EMERG_VAST` / `EMERG_LIFECYCLE` | [VERIFIED: in-house pattern] |

### New deps required

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `github.com/go-redsync/redsync/v4` | v4.16.0 (latest, published 2026-02-25) | Distributed lock para leader-election; PRV-03 explicita | [VERIFIED: proxy.golang.org/github.com/go-redsync/redsync/v4/@latest] |

**Installation step:**
```bash
cd gateway && go get github.com/go-redsync/redsync/v4@v4.16.0
```

**Version verification:**
- v4.16.0 = current stable, published 2026-02-25 [VERIFIED: `curl -sS https://proxy.golang.org/github.com/go-redsync/redsync/v4/@latest`]
- v4 line is what `pkg.go.dev` documents (anteriores são v0); usar v4 sempre [CITED: pkg.go.dev/github.com/go-redsync/redsync/v4]

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| `go-redsync/redsync v4` | `bsm/redislock` ou hand-rolled SETNX+EXPIRE | redsync é o que CONTEXT D-B2 explicita; `bsm/redislock` mais simples mas perde o algoritmo Redlock formal; hand-rolled = NIH |
| Vast.ai REST direto | Wrapping em `vast-ai-cli` (Python, via subprocess) | Subprocess = pesado, slow, dependência runtime extra; PRV-01 explicita Go puro |
| `cenkalti/backoff/v5` para HTTP retry | Hand-rolled exp backoff | Backoff já em go.mod (Phase 3); usar é mais legível e testável; mas hand-rolled aceitável (2s/4s/8s = 3 valores fixos) |
| BIGSERIAL PK em emergency_lifecycles | UUID PK | BIGSERIAL é JSON-natural para gatewayctl output + Phase 7 dashboard; UUID overkill (não há merge cross-region) |
| Per-tick HGETALL reconcile (Phase 5 padrão) | Boot-time HGETALL only | Phase 6 leader recovery é via DB query (`WHERE ended_at IS NULL`), não Redis; reconcile NÃO necessário |

## Architecture Patterns

### System Architecture Diagram

```
                ┌──────────────────────────────────────────────────────────────┐
                │ Gateway Replica (1..N — orquestração distribuída)           │
                │                                                              │
   ┌────────┐   │  ┌──────────────────┐   ┌──────────────────────────────┐    │
   │ Client │──▶│  │ Dispatcher       │──▶│ tier-0: local-llm (primário) │    │
   │ App    │   │  │ (existing,       │   │  OR emergency pod (override) │    │
   └────────┘   │  │  reads atomic.   │   │ tier-1: openrouter-chat      │    │
                │  │  Pointer[string] │   └──────────────────────────────┘    │
                │  │  via             │                                       │
                │  │  upstreams.OverrideTier0)                                │
                │  └──────────────────┘                                       │
                │           ▲                                                  │
                │           │ OverrideTier0("local-llm", podURL)              │
                │           │                                                  │
                │  ┌────────────────────────────────────────────┐              │
                │  │ internal/emerg (NEW)                       │              │
                │  │                                            │              │
                │  │ ┌────────────────┐  ┌──────────────────┐   │              │
                │  │ │ FSM (9 states) │  │ reconciler tick  │   │              │
                │  │ │ atomic.Int32 + │  │ 1Hz goroutine    │◀──┼──┐           │
                │  │ │ atomic.Int64   │  │ + leader CAS     │   │  │           │
                │  │ └────────────────┘  └──────────────────┘   │  │           │
                │  │         │                    │             │  │           │
                │  │         ▼                    ▼             │  │           │
                │  │ ┌────────────────┐  ┌──────────────────┐   │  │           │
                │  │ │ Pub/Sub        │  │ leader lock      │   │  │           │
                │  │ │ subscriber:    │  │ (redsync v4):    │   │  │           │
                │  │ │ gw:breaker:    │  │ gw:emerg:lock    │   │  │           │
                │  │ │ events         │  │ TTL=30s renew=10s│   │  │           │
                │  │ │ (Phase 3 ch)   │  │                  │   │  │           │
                │  │ └────────────────┘  └──────────────────┘   │  │           │
                │  │                              │             │  │           │
                │  │  ┌─────────────┐             ▼             │  │           │
                │  │  │ vast.Client │  ┌──────────────────┐    │  │           │
                │  │  │ (REST)      │◀─│ provisionLifecyc │    │  │           │
                │  │  │ - search    │  │ (goroutine, ctx- │    │  │           │
                │  │  │ - create    │  │  cancellable)    │    │  │           │
                │  │  │ - get       │  └──────────────────┘    │  │           │
                │  │  │ - destroy   │           │              │  │           │
                │  │  │ - ping      │           ▼              │  │           │
                │  │  └─────────────┘  ┌──────────────────┐    │  │           │
                │  │         │         │ readiness poll   │    │  │           │
                │  │         │         │ pod /health 9100 │    │  │           │
                │  │         │         │ 5s interval      │    │  │           │
                │  │         │         └──────────────────┘    │  │           │
                │  │         │                  │              │  │           │
                │  │         │                  ▼              │  │           │
                │  │  ┌──────────────────────────────────┐    │  │           │
                │  │  │ lifecycle audit (DB + Redis Hash)│    │  │           │
                │  │  │ - INSERT emergency_lifecycles    │    │  │           │
                │  │  │ - HSET gw:emerg:state            │    │  │           │
                │  │  │ - PUBLISH gw:emerg:events        │    │  │           │
                │  │  └──────────────────────────────────┘    │  │           │
                │  └──────────────────────────────────────────┘  │           │
                │                                                 │           │
                └─────────────────────────────────────────────────┼───────────┘
                                                                  │
                                                                  │
                ┌───────────────────────────────────────┐         │
                │ Postgres (DO managed, ai_gateway sch) │         │
                │  - emergency_lifecycles (NEW table)   │         │
                │  - upstreams (existing)               │         │
                │  - audit_log (existing)               │         │
                └───────────────────────────────────────┘         │
                                                                  │
                ┌───────────────────────────────────────┐         │
                │ Redis (infra-redis-1, traefik-public) │         │
                │  - gw:emerg:state (Hash)              │◀────────┘
                │  - gw:emerg:lock (redsync, TTL=30s)   │
                │  - gw:emerg:events (Pub/Sub)          │
                │  - gw:breaker:events (existing, sub)  │
                └───────────────────────────────────────┘

                ┌───────────────────────────────────────┐
                │ Vast.ai (https://vast.ai/api/v0)      │
                │  - GET  /bundles/?q=<json filter>     │
                │  - PUT  /asks/{offer_id}/             │
                │  - GET  /instances/{id}/              │
                │  - DELETE /instances/{id}/            │
                │  - GET  /users/current                │
                └───────────────────────────────────────┘

                ┌───────────────────────────────────────┐
                │ Emergency Pod (created on Vast.ai)    │
                │  Image: ghcr.io/ifixtelecom/           │
                │         ifix-ai-pod:v1.0               │
                │  Ports:                               │
                │   8000 LLM (OpenAI-compat)            │
                │   8001 STT                            │
                │   8002 embed                          │
                │   9100 health-bridge ◀────────────────┐│
                │   9400 dcgm-exporter                  ││
                │  onstart.sh: MinIO weight pull (~3min)│
                │  cold-start envelope: 3-5min validated││
                └───────────────────────────────────────┘
                                                         │
                              gateway readiness poll ────┘
                              5s interval, 600s budget
```

### Recommended Project Structure

```
gateway/
├── internal/
│   ├── emerg/                       # NEW Phase 6
│   │   ├── fsm.go                   # 9-state FSM (atomic.Int32 + onChange)
│   │   ├── fsm_test.go
│   │   ├── reconciler.go            # tick goroutine + leader CAS
│   │   ├── reconciler_test.go
│   │   ├── lifecycle.go             # provisionLifecycle, cancelLifecycle, destroyLifecycle
│   │   ├── lifecycle_test.go
│   │   ├── recovery.go              # recoverOrphanLifecycles (D-D5)
│   │   ├── recovery_test.go
│   │   ├── subscribe.go             # Pub/Sub gw:breaker:events consumer
│   │   ├── subscribe_test.go
│   │   ├── mirror.go                # namespace const + helpers (delegates to redisx)
│   │   ├── errors.go                # ErrOfferRaceLost, ErrHealthTimeout, ErrBudgetExceeded, ErrLeaderLost...
│   │   ├── budget.go                # monthly budget check + Sentry alert
│   │   └── vast/
│   │       ├── client.go            # HTTP client wrapping search/create/get/destroy/ping
│   │       ├── client_test.go       # httptest.Server fixtures
│   │       ├── types.go             # Offer, Instance, CreateRequest, ErrorResponse structs
│   │       └── errors.go            # Vast-specific sentinels (ErrOfferGone, ErrRateLimited, ErrUnauthorized)
│   ├── redisx/
│   │   └── emerg.go                 # NEW: gw:emerg:* helpers (mirror Hash + Pub/Sub + redsync wrapper)
│   ├── upstreams/
│   │   └── loader.go                # MODIFIED: add OverrideTier0 + RestoreTier0 (atomic.Pointer[string])
│   ├── obs/
│   │   └── metrics.go               # MODIFIED: + 7 emergency_* collectors
│   └── config/
│       └── config.go                # MODIFIED: + 7 env vars (VAST_AI_API_KEY, VAST_PRICE_CAP_DPH, MONTHLY_EMERGENCY_BUDGET_BRL, PROVISION_*_SECONDS, USD_TO_BRL_RATE, EMERGENCY_POD_IMAGE_TAG, PRIMARY_HOST_ID)
├── cmd/
│   ├── gateway/
│   │   └── main.go                  # MODIFIED: + emerg.NewReconciler + go reconciler.Run + dispatcher hookup
│   └── gatewayctl/
│       ├── emerg.go                 # NEW: dispatcher for emerg state|force-provision|force-destroy|lifecycles
│       ├── emerg_test.go
│       └── main.go                  # MODIFIED: + "emerg" case
├── db/
│   ├── migrations/
│   │   └── 0019_emergency_lifecycles.sql   # NEW (corrigindo CONTEXT D-B4 que diz 0017)
│   └── queries/
│       └── emergency_lifecycles.sql        # NEW sqlc queries
├── sqlc.yaml                        # MODIFIED: add emergency_lifecycles.sql to queries list
└── go.mod                           # MODIFIED: + go-redsync/redsync/v4 v4.16.0
```

### Pattern 1: 9-state FSM (atomic + onChange callback)

**What:** Atomic-based state machine with lockless hot-path read; transitions via CAS; OnStateChange callback fires AFTER successful CAS for log + metric + Sentry breadcrumb + Pub/Sub publish.

**When to use:** All FSM mutations within `internal/emerg`.

**Source:** `gateway/internal/shed/fsm.go` lines 110-258 (replicate exactly, expand to 9 states):

```go
// Source: gateway/internal/shed/fsm.go (Phase 5 D-C1 pattern)
type State int32

const (
    StateHealthy                 State = iota
    StateDegraded
    StateFailedOver
    StateEmergencyProvisioning
    StateEmergencyActive
    StateRecovering
    StateCooldown
    StateOffHours
    StateMaintenance
)

type FSM struct {
    state     atomic.Int32
    enteredAt atomic.Int64
    cfg       atomic.Pointer[Config]
    onChange  func(from, to State, reason string)
    log       *slog.Logger
}

func (f *FSM) State() State { return State(f.state.Load()) }

func (f *FSM) transition(from, to State, now time.Time, reason string) {
    if from == to { return }
    if !f.state.CompareAndSwap(int32(from), int32(to)) { return }
    f.enteredAt.Store(now.Unix())
    obs.GatewayEmergencyState.WithLabelValues(stateString(to)).Set(1)
    // ... reset other state gauges to 0 (only one active state)
    f.log.Info("emerg FSM transition", "from", from, "to", to, "reason", reason)
    sentry.AddBreadcrumb(&sentry.Breadcrumb{
        Category: "emerg",
        Message:  fmt.Sprintf("state %s→%s", from, to),
        Level:    sentry.LevelInfo,
        Data:     map[string]interface{}{"reason": reason},
    })
    if f.onChange != nil { f.onChange(from, to, reason) }
}
```

### Pattern 2: Leader-elected reconciler tick (1Hz)

**What:** Single goroutine ticker at 1Hz. Each tick: (a) attempt Lock if not leader; (b) if leader, Extend; (c) if leader, evaluate FSM transitions; (d) if not leader, no-op (just observe via Pub/Sub).

**When to use:** Once at gateway boot in `main.go`.

**Source:** Composição de `gateway/internal/shed/tick.go` (1Hz ticker pattern) + `go-redsync/redsync v4` lock pattern [VERIFIED: pkg.go.dev/github.com/go-redsync/redsync/v4]:

```go
// Source: pkg.go.dev/github.com/go-redsync/redsync/v4 (Extend ticker pattern)
//      + gateway/internal/shed/tick.go (1Hz ForEach + Evaluate)
func (r *Reconciler) Run(ctx context.Context) {
    t := time.NewTicker(1 * time.Second)
    defer t.Stop()

    mutex := r.redsync.NewMutex("gw:emerg:lock",
        redsync.WithExpiry(30*time.Second),
        redsync.WithTries(1),
        redsync.WithRetryDelay(0),
    )

    for {
        select {
        case <-ctx.Done():
            if r.isLeader.Load() {
                _, _ = mutex.UnlockContext(context.Background()) // graceful release
            }
            return
        case now := <-t.C:
            if !r.isLeader.Load() {
                ok, err := mutex.LockContext(ctx)
                if err != nil || !ok {
                    continue // someone else holds it
                }
                r.isLeader.Store(true)
                r.log.Info("acquired leadership")
                if err := r.recoverOrphanLifecycles(ctx); err != nil {
                    r.log.Error("orphan recovery failed", "err", err)
                }
            } else {
                ok, err := mutex.ExtendContext(ctx)
                if err != nil || !ok {
                    r.log.Warn("lost leadership", "err", err, "ok", ok)
                    r.isLeader.Store(false)
                    continue
                }
            }
            // Leader: evaluate FSM
            r.evaluateTick(ctx, now)
        }
    }
}
```

### Pattern 3: Cross-replica mirror (Pub/Sub + Hash + reconnect-with-backoff)

**What:** Authoritative state em-process; Redis Hash + Pub/Sub channel para visibility cross-replica.

**When to use:** Every FSM transition publishes; subscriber goroutine consume to update non-leader local view.

**Source:** `gateway/internal/breaker/subscribe.go` integral (replicate exact):

```go
// Source: gateway/internal/breaker/subscribe.go (Phase 3 D-D1)
func (r *Reconciler) Subscribe(ctx context.Context) {
    log := r.log.With("subsystem", "subscribe")
    for {
        if err := ctx.Err(); err != nil { return }
        ps := redisx.SubscribeEmergEvents(ctx, r.rdb)
        ch := ps.Channel()
        drained := false
        for !drained {
            select {
            case <-ctx.Done():
                _ = ps.Close()
                return
            case msg, ok := <-ch:
                if !ok { drained = true; break }
                var ev redisx.EmergEvent
                if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
                    log.Warn("malformed emerg event", "err", err)
                    continue
                }
                r.applyRemoteEvent(ev)
            }
        }
        _ = ps.Close()
        log.Warn("pubsub closed; reconnecting")
        select {
        case <-ctx.Done(): return
        case <-time.After(1 * time.Second):
        }
    }
}
```

### Pattern 4: Cancel-in-flight via context.WithCancel + post-create destroy

**What:** Provisioning roda em goroutine separada com `ctx, cancel := context.WithCancel(reconcilerCtx)`. Reconciler armazena `cancel` em `lifecycle.Cancel`. Quando `local-llm` recupera (via Pub/Sub event), reconciler chama `lifecycle.Cancel()`. `provisionLifecycle` checa `ctx.Err()` em cada checkpoint; se cancelled APÓS create, executa destroy antes de retornar.

**When to use:** Triple-layer cancel guarantee per D-C3.

**Source:** Standard Go pattern (no in-house analog):

```go
// CONTEXT D-C3 — triple layer: context cancel + Pub/Sub broadcast + post-create destroy
func (r *Reconciler) startProvisioning(parentCtx context.Context, lifecycleID int64) {
    ctx, cancel := context.WithCancel(parentCtx)
    r.activeLifecycle.Store(&Lifecycle{ID: lifecycleID, Cancel: cancel})

    go func() {
        defer cancel()
        if err := r.provisionLifecycle(ctx, lifecycleID); err != nil {
            r.log.Error("lifecycle failed", "id", lifecycleID, "err", err)
        }
    }()
}

func (r *Reconciler) provisionLifecycle(ctx context.Context, id int64) error {
    // Checkpoint 1: pre-search
    if ctx.Err() != nil { return ctx.Err() }
    offers, err := r.vast.SearchOffers(ctx, r.searchFilter())
    if err != nil { return err }

    // Checkpoint 2: pre-create
    for attempt := 0; attempt < 3; attempt++ {
        if ctx.Err() != nil { return ctx.Err() }
        offer := offers[0] // already sorted dph_total ASC
        instance, err := r.vast.CreateInstance(ctx, offer.ID, r.createRequest(offer))
        if err == nil {
            // Checkpoint 3: post-create — must destroy on cancel
            return r.waitForReadyOrDestroy(ctx, id, instance.ID)
        }
        if !errors.Is(err, vast.ErrOfferGone) { return err }
        // Bid race: re-search and retry with exp backoff
        select {
        case <-ctx.Done(): return ctx.Err()
        case <-time.After(time.Duration(1<<attempt) * 2 * time.Second): // 2s/4s/8s
        }
        offers, err = r.vast.SearchOffers(ctx, r.searchFilter())
        if err != nil { return err }
    }
    return ErrOfferRaceLost
}

func (r *Reconciler) waitForReadyOrDestroy(ctx context.Context, lifecycleID, instanceID int64) error {
    poll := time.NewTicker(5 * time.Second)
    defer poll.Stop()
    deadline := time.NewTimer(time.Duration(r.cfg.ColdStartBudgetSeconds) * time.Second)
    defer deadline.Stop()

    for {
        select {
        case <-ctx.Done():
            // Cancel post-create: MUST destroy
            destroyCtx, destroyCancel := context.WithTimeout(context.Background(), 30*time.Second)
            defer destroyCancel()
            _ = r.vast.DestroyInstance(destroyCtx, instanceID)
            r.closeLifecycle(lifecycleID, "cancelled_in_flight")
            return ctx.Err()
        case <-deadline.C:
            destroyCtx, destroyCancel := context.WithTimeout(context.Background(), 30*time.Second)
            defer destroyCancel()
            _ = r.vast.DestroyInstance(destroyCtx, instanceID)
            r.closeLifecycle(lifecycleID, "health_timeout")
            return ErrHealthTimeout
        case <-poll.C:
            inst, err := r.vast.GetInstance(ctx, instanceID)
            if err != nil { continue } // retry next tick
            if inst.ActualStatus != "running" { continue }
            // Got SSH host:port — try /health
            healthURL := fmt.Sprintf("http://%s:%d/health", inst.PublicIPAddr, /* mapped 9100 port */)
            if r.checkHealth(ctx, healthURL) {
                r.markHealthy(lifecycleID, healthURL, inst)
                return nil
            }
        }
    }
}
```

### Anti-Patterns to Avoid

- **Polling Vast.ai > 1 req/s:** Vast rate-limit não documenta header `Retry-After`; conservador 1 req/s. Burst raras (search→create) ok, mas sustained polling deve respeitar 5s no waitForReady.
- **HSET emergency state em hot path com timeout > 2s:** Mesma regra de breaker/shed — Redis NEVER blocks state machine. 2s timeout, fire-and-log on failure.
- **Lock-renewal goroutine separada do reconciler tick:** CONTEXT D-B2 diz "renew a cada 10s". O cleanest é fazer Extend dentro do tick 1s (skip a cada 10 ticks ou check enteredAt). Goroutine separada introduz race "tick está em transition mas Extend acabou de falhar — quem vê primeiro?".
- **Permitir lifecycle reset por novos breaker.OPEN events em estado ACTIVE:** D-C4 explícito. Subscriber só atualiza in-memory timer; reconciler tick é quem decide se age.
- **Storing the redsync mutex pointer in shared state:** Cada loop iteration deve usar o mesmo `*Mutex` instance — não recriar a cada tick (reset de internal state).
- **Sentry CaptureException para transições normais:** AddBreadcrumb sim; Capture* APENAS para terminal errors (ofer_race_lost, health_timeout, leader_recovery_zombie, budget_exceeded).
- **Trust de `actual_status` field como sinal único de readiness:** Vast doc diz `actual_status` pode ser running mas SSH não estar disponível ainda (já tratado em vast-ai.sh `wait-running` checking ssh_host + ssh_port presence). Phase 6 deve checar (a) `actual_status == "running"`, (b) `public_ipaddr != ""`, (c) `/health` HTTP 200 — todos os 3.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Distributed mutex with TTL renewal | Hand-rolled SETNX + EXPIRE + Lua refresh | `go-redsync/redsync v4` (CONTEXT D-B2) | Redlock algorithm formal; correta handling de clock skew, partial failure; já prod-tested; PRV-03 mandata |
| HTTP retry com exp backoff | Hand-rolled `for attempt < 3 { ... time.Sleep(1<<attempt) }` | `cenkalti/backoff/v5` (já em go.mod) — OR hand-rolled aceitável aqui (3 attempts fixed) | backoff library tem jitter, max-elapsed, reset semantics; mas para 3 attempts fixos, hand-rolled é OK |
| Vast.ai REST client | Wrap em CLI subprocess `vast-ai.sh` | Pure Go HTTP + `encoding/json` | PRV-01 explícito (Go puro); subprocess = slow + dependência |
| FSM atomic state | `sync.Mutex` em hot path | `atomic.Int32` + CAS (Phase 5 D-C1 pattern) | Lockless hot-path; State() chamado por dispatcher per request |
| Test fixtures Vast.ai | Live API calls em CI | `httptest.Server` com response fixtures recordadas | Determinism + cost ($0/test); 1 burst real test em UAT |
| Cross-replica state sync | Polling DB | Pub/Sub `gw:emerg:events` (Phase 3 pattern) | <1s convergence vs poll lag; same pattern as breaker/shed |
| Lifecycle JSON event log | Separate audit_log inserts per transition | JSONB `events` column append-only com `jsonb_insert` | CONTEXT D-D3 explícito; uma row por lifecycle, timeline natural; Phase 7 dashboard render trivial |

**Key insight:** Phase 6 reusa 90% dos patterns Phase 3+5. A novidade real é (a) leader-election (use redsync), (b) FSM 9-estado com timer-based transitions (atomic + ticker), (c) external API integration (Vast.ai REST com 4 endpoints + cuidado com race semantics empíricas), (d) cancel-in-flight triple guarantee (context + Pub/Sub + post-create destroy). Cada uma tem analog ou library — não hand-roll nada.

## Vast.ai REST API Reference

**Base URL:** `https://vast.ai/api/v0` (CONTEXT D-A1 + verified in `pod/scripts/vast-ai.sh`)
**Authentication:** `Authorization: Bearer ${VAST_AI_API_KEY}` (verified)
**Documentation status:** Officially documented surface is **incomplete** for our use cases. Significant areas (bid race response shape, port reachability timing, status transitions) have to be inferred from (a) `pod/scripts/vast-ai.sh` Phase 1 already-tested behavior, (b) WebFetch of docs.vast.ai — see citations below, (c) recommended 3h spike per STATE.md TODO.

### Endpoint: Search Offers

**Method:** `GET /bundles/?q=<json-url-encoded>` [VERIFIED: pod/scripts/vast-ai.sh line 60]

**Request filter shape** (validated in Phase 1 smoke):
```json
{
  "gpu_name": "RTX 4090",
  "num_gpus": 1,
  "disk_space": {"gte": 50},
  "reliability": {"gte": 0.99},
  "inet_down": {"gte": 500},
  "rentable": true,
  "verified": {"eq": true},
  "dph_total": {"lte": 0.40},
  "cuda_max_good": {"gte": 12.4},
  "order": [["dph_total", "asc"]],
  "limit": 20
}
```

**Response shape** [CITED: docs.vast.ai/api-reference/instances + observed in Phase 1]:
```json
{
  "offers": [
    {
      "id": <int64>,                  // ask_id used in PUT /asks/{id}/
      "gpu_name": "RTX 4090",
      "num_gpus": 1,
      "dph_total": 0.350,             // dollars per hour
      "reliability": 0.992,
      "inet_down": 850,               // Mbps
      "cuda_max_good": 12.6,
      "machine_id": <int64>,
      "host_id": <int64>,             // PRIMARY_HOST_ID exclusion key
      "geolocation": "US",
      "rentable": true,
      "verified": true,
      ...
    }
  ]
}
```

**Note on offer churn:** Offers are **highly ephemeral** (seconds to minutes). A successful search returning offer X does NOT guarantee X is still available 30s later. The 3-attempt retry with re-search between attempts (CONTEXT D-A3) is the correct pattern.

### Endpoint: Create Instance (Accept Offer)

**Method:** `PUT /asks/{offer_id}/` [VERIFIED: pod/scripts/vast-ai.sh line 89]

**Request body shape** [CITED: docs.vast.ai/api-reference/instances/create-instance]:
```json
{
  "client_id": "me",
  "image": "ghcr.io/ifixtelecom/ifix-ai-pod:v1.0",
  "env": {
    "MINIO_ENDPOINT": "https://s3.ifixtelecom.com.br",
    "MINIO_ACCESS_KEY": "...",
    "MINIO_SECRET_KEY": "...",
    "MINIO_BUCKET": "ai-gateway",
    "WEIGHTS_QWEN_KEY": "...",
    "WEIGHTS_QWEN_SHA256": "...",
    "...": "..."
  },
  "onstart": "echo <base64-encoded-onstart.sh> | base64 -d > /root/onstart.sh && chmod +x /root/onstart.sh && /root/onstart.sh",
  "runtype": "ssh",
  "disk": 60,
  "label": "ifix-emerg-{lifecycle_id}",
  "target_state": "running"
}
```

**Response shape** (HTTP 200 success) [CITED: docs.vast.ai/api-reference/instances/create-instance]:
```json
{
  "success": true,
  "new_contract": <int64>     // <-- this is the instance_id used by GET/DELETE /instances/{id}/
}
```

**HTTP status codes** [CITED: docs.vast.ai/api-reference/instances/create-instance]:

| Status | Scenario | Action |
|--------|----------|--------|
| 200 | Created | Store `new_contract` as `vast_instance_id`; transition FSM to EMERGENCY_PROVISIONING |
| 400 | Invalid args / price / SSH key | Sentry CaptureMessage + abort lifecycle (config bug, NOT retryable) |
| 401 | Unauthorized | Sentry CaptureException CRITICAL — `VAST_AI_API_KEY` is invalid or revoked. Fail hard. |
| 403 | Forbidden | Same as 401 (audit operator action) |
| 404 | Offer not found / unavailable | **Bid race lost** — re-search + retry (D-A3) |
| 410 | Offer no longer available (when `cancel_unavail=true` on Vast side) | **Bid race lost** — same as 404 |
| 429 | Rate limited | **Backoff** then retry; bump `gateway_vast_api_requests_total{status="429"}`; consider longer poll interval |
| 5xx | Vast server error | Backoff + retry up to 3x then abort with `shutdown_reason='vast_5xx'` |

**Bid-race response (HTTP 404 OR 410):**
```json
{
  "success": false,
  "error": "no_such_ask",
  "msg": "Offer is no longer available",
  "ask_id": <int64>
}
```

**Critical:** Vast does NOT use a single canonical error envelope. Some endpoints return only `msg`; others return `error + msg`; some return `success: false + error`. **Always defensively parse both `error` and `msg` fields**. [CITED: docs.vast.ai/api-reference/rate-limits-and-errors] [ASSUMED: exact 404 vs 410 split for "lost race" — both possible; treat both as ErrOfferGone]

### Endpoint: Get Instance Status

**Method:** `GET /instances/{instance_id}/` [VERIFIED: pod/scripts/vast-ai.sh line 102]

**Response shape** [VERIFIED: vast-ai.sh wait-running parses the same fields]:
```json
{
  "instances": {              // CAREFUL: top-level may be object OR array — vast-ai.sh handles both
    "id": <int64>,
    "actual_status": "running",      // <-- key signal
    "intended_status": "running",
    "cur_state": "running",
    "next_state": null,
    "ssh_host": "ssh1.vast.ai",
    "ssh_port": 12345,
    "public_ipaddr": "1.2.3.4",
    "machine_id": <int64>,
    "host_id": <int64>,
    "dph_total": 0.350,
    "image_uuid": "...",
    "label": "ifix-emerg-42",
    ...
  }
}
```

**`actual_status` documented values** [CITED: docs.vast.ai + Phase 1 vast-ai.sh observation]:

| Value | Meaning | Phase 6 action |
|-------|---------|----------------|
| `loading` | Image pull / container startup | Wait — not yet ready |
| `running` | Container running per docker | **Necessary but not sufficient** for /health (onstart.sh still pulling weights) |
| `exited` | Container exited (rare during onstart) | Destroy + lifecycle close `shutdown_reason='instance_exited'` |
| `unknown` | Vast cannot reach machine | Treated as "destroy and retry" per [CITED: docs.vast.ai] |
| `offline` | Host offline | Same as unknown — destroy + close |
| `scheduling` | Vast assigning to host | Wait |

**Critical note:** `actual_status: running` ≠ pod ready. The onstart.sh script pulls weights from MinIO (~3min for Qwen GGUF ~16GB) AFTER container starts. Phase 6 readiness requires:
1. `actual_status == "running"` (container live)
2. `public_ipaddr != ""` (network ready)
3. `GET http://{public_ipaddr}:{mapped_9100_port}/health` returns HTTP 200 with `status != "unknown"` (per Phase 1 health-bridge contract)

**Port reachability timing** [ASSUMED: not officially documented; inferred from Phase 1 onstart.sh behavior]:
- Vast.ai port mappings (e.g., 9100 → public_port) are assigned at create time but reachable only AFTER container starts (`actual_status: running`)
- Total time-to-`/health/ready` from Vast `create` accept: ~3-5 minutes (Phase 1 baseline — see "Cold-start budget evidence" section)
- Mapped port discovery: typically retrieved from `instances.ports` or `instances.machine.public_ipaddr_port_map` field — **EXACT field name needs spike confirmation**; fallback: use `--cmd "docker port"` SSH into instance

### Endpoint: Destroy Instance

**Method:** `DELETE /instances/{instance_id}/` [VERIFIED: pod/scripts/vast-ai.sh line 144]

**Response:** HTTP 200 with `{"success": true}` [CITED: docs.vast.ai] OR error envelope.

**Critical:** Destroy is **idempotent** (deleting a missing instance returns 404 with `no_such_instance`, NOT a destructive error). Phase 6 should treat 404 from DELETE as success (instance already gone — leader recovery scenario).

### Endpoint: Auth Ping (Boot validation)

**Method:** `GET /users/current` [CITED: docs.vast.ai/api/overview-and-quickstart]

**Use:** `vast.Client.Ping()` chamado at gateway boot. If returns 401 → fail-loud (env var misconfigured); if returns 200 with user payload → API key valid.

### Rate Limits

[CITED: docs.vast.ai/api-reference/rate-limits-and-errors]:

- **Scope:** per-endpoint AND per-identity (identity = bearer token + session user + api_key query param + client IP)
- **Signal:** HTTP 429 with body `"API requests too frequent"` OR `"API requests too frequent: endpoint threshold=..."`
- **Headers:** **Vast does NOT set standard rate-limit headers** (`X-RateLimit-*`, `Retry-After`) — clients must implement own backoff
- **Specific quotas:** **NOT publicly documented** — Vast advises "contact support for higher limits with endpoint details and usage projections"
- **Mitigation:** batch requests, reduce polling, distribute over time

**Phase 6 default policy:** Token bucket conservador 1 req/s globally para `vast.Client`; on 429, exp backoff 5s/15s/45s before next attempt; bump `gateway_vast_api_requests_total{status="429"}` counter for observability. Configurável via env (Claude's discretion: `VAST_API_QPS_LIMIT=1` default).

### Error Envelope Defensive Parsing

[CITED: docs.vast.ai/api-reference/rate-limits-and-errors]:

```go
// Source: derived from CITED Vast docs — multiple shapes coexist
type vastErrorEnvelope struct {
    Success bool   `json:"success,omitempty"`  // sometimes omitted
    Error   string `json:"error,omitempty"`    // sometimes omitted
    Msg     string `json:"msg,omitempty"`      // sometimes "message"
    Message string `json:"message,omitempty"`  // alternate spelling
    AskID   int64  `json:"ask_id,omitempty"`   // present on 404/410 ask errors
}

func (c *Client) parseErrorBody(resp *http.Response) error {
    body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024)) // 16KB cap
    var env vastErrorEnvelope
    _ = json.Unmarshal(body, &env)

    msg := env.Msg
    if msg == "" { msg = env.Message }
    if msg == "" { msg = string(body) } // fallback raw text

    switch resp.StatusCode {
    case 401, 403:
        return &VastError{Status: resp.StatusCode, Code: "unauthorized", Msg: msg}
    case 404, 410:
        if env.Error == "no_such_ask" || strings.Contains(msg, "no longer available") {
            return ErrOfferGone
        }
        if env.Error == "no_such_instance" {
            return ErrInstanceNotFound
        }
        return &VastError{Status: resp.StatusCode, Code: env.Error, Msg: msg}
    case 429:
        return ErrRateLimited
    case 500, 502, 503, 504:
        return &VastError{Status: resp.StatusCode, Code: "server_error", Msg: msg}
    }
    return &VastError{Status: resp.StatusCode, Code: env.Error, Msg: msg}
}
```

## go-redsync/redsync v4 Patterns

[VERIFIED: pkg.go.dev/github.com/go-redsync/redsync/v4]

### Construction

```go
import (
    "github.com/go-redsync/redsync/v4"
    redsyncredis "github.com/go-redsync/redsync/v4/redis/goredis/v9"
    "github.com/redis/go-redis/v9"
)

pool := redsyncredis.NewPool(rdb)  // rdb = existing *redis.Client from internal/redisx
rs := redsync.New(pool)

mutex := rs.NewMutex("gw:emerg:lock",
    redsync.WithExpiry(30*time.Second),    // CONTEXT D-B2
    redsync.WithTries(1),                   // single attempt per tick — reconciler retries 1Hz
    redsync.WithRetryDelay(0),              // no internal backoff (tick already rate-limits)
)
```

### Lock + Extend signature (verified)

```go
// VERIFIED: pkg.go.dev/github.com/go-redsync/redsync/v4#Mutex.Extend
func (m *Mutex) Lock() error
func (m *Mutex) LockContext(ctx context.Context) error
func (m *Mutex) Extend() (bool, error)
func (m *Mutex) ExtendContext(ctx context.Context) (bool, error)
func (m *Mutex) Unlock() (bool, error)
func (m *Mutex) UnlockContext(ctx context.Context) (bool, error)
```

**Return semantics:**
- `Extend()`: `(true, nil)` = success; `(false, ErrLockAlreadyExpired)` = TTL passed before extend (lost lock); `(false, ErrExtendFailed)` = quorum not met but no expiry; `(_, RedisError)` = transport error
- `Lock()`: `nil` = acquired; `ErrFailed` = exhausted retries (other holder); `ErrTaken` / `ErrNodeTaken` = quorum nuance (single-Redis = N/A but check anyway)

### Recommended Pattern (production)

[VERIFIED: pkg.go.dev/github.com/go-redsync/redsync/v4 + WebSearch citations]:

```go
// Source: pkg.go.dev/github.com/go-redsync/redsync/v4 + verified WebSearch
// TTL/renew ratio: 1/2 to 1/3 of expiry. CONTEXT D-B2 picks 1/3 (10s renew on 30s expiry).

ticker := time.NewTicker(10 * time.Second) // 1/3 of 30s expiry
defer ticker.Stop()

for {
    select {
    case <-ticker.C:
        ok, err := mutex.ExtendContext(ctx)
        if err != nil {
            if errors.Is(err, redsync.ErrLockAlreadyExpired) {
                // Lock TTL expired before extend; we already lost leadership
                r.log.Warn("lock expired before extend; another replica may have acquired")
                r.isLeader.Store(false)
                return // exit goroutine; reconciler tick will re-attempt Lock
            }
            // Transport error (Redis blip) — log + return, treat as lost
            r.log.Warn("Extend failed; ceding leadership", "err", err)
            r.isLeader.Store(false)
            return
        }
        if !ok {
            // No error but quorum not met (single-Redis = unusual; cluster = quorum issue)
            r.log.Warn("Extend returned false (quorum)")
            r.isLeader.Store(false)
            return
        }
    case <-ctx.Done():
        // Graceful shutdown: best-effort release
        releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 2*time.Second)
        defer releaseCancel()
        _, _ = mutex.UnlockContext(releaseCtx) // ignore error — TTL expiry catches it
        return
    }
}
```

### Quorum requirements

[CITED: pkg.go.dev/github.com/go-redsync/redsync/v4]:

- Single-Redis OK for development AND production with single Redis instance (Phase 6 gateway uses `infra-redis-1` single-instance)
- For HA, use multiple **independent** Redis nodes (NOT cluster replicas) and pass multiple pools to `New`
- Phase 6 uses **single Redis** (existing `infra-redis-1` container) — same Redis as breaker mirror, shed mirror, rate-limit. **Acceptable trade-off:** if Redis dies, gateway has bigger problems (rate-limit, breaker mirror, billing flush all fail) — emergency provisioning being unavailable is consistent with the rest of the system being degraded.

### Graceful handoff on shutdown

CONTEXT D-B2 says "Se Extend falha → leader cede". The cleanest pattern:
1. On context cancel, call `mutex.UnlockContext` with a short timeout (2s)
2. Ignore Unlock error (TTL will expire naturally if Unlock fails)
3. Set `isLeader = false` so subscriber/handler don't act on stale leader role

**Don't:** force-release on every signal — Vast lifecycle in-flight should NOT be cancelled by a SIGTERM mid-deploy. Use the normal ctx.Done() fan-out: in-flight `provisionLifecycle` already handles ctx cancel via the triple-layer pattern (Pattern 4 above).

## Internal Patterns to Reuse

### Pattern Map

| New Phase 6 file | Closest analog | Match quality |
|-----------------|-----------------|---------------|
| `internal/emerg/fsm.go` | `internal/shed/fsm.go` | exact (atomic.Int32 + onChange + transition CAS); expand 4→9 states |
| `internal/emerg/reconciler.go` | `internal/shed/tick.go` (1Hz ForEach) | role-match (1Hz tick is the same; FSM evaluation logic is novel — 9 states with timer-based transitions and external API trigger) |
| `internal/emerg/subscribe.go` | `internal/breaker/subscribe.go` | exact (replicate 56-line reconnect-with-backoff loop); subscribe to `gw:breaker:events` to read `local-llm` state changes |
| `internal/emerg/mirror.go` | `internal/breaker/mirror.go` | exact (namespace const only — `Namespace = "gw:emerg:"`) |
| `internal/redisx/emerg.go` | `internal/redisx/breaker.go` (71 lines integral) | exact (replicate WriteEmergState / PublishEmergEvent / SubscribeEmergEvents); add `WriteEmergLifecycleHash` for FSM mirror with all 5 fields per CONTEXT D-B1 (state, lifecycle_id, pod_url, pod_instance_id, entered_at) |
| `internal/emerg/lifecycle.go` | (no direct analog — novel) | new — provisionLifecycle goroutine + waitForReady polling + closeLifecycle DB writes |
| `internal/emerg/recovery.go` | (no direct analog — novel) | new — recoverOrphanLifecycles (D-D5 3 cenários) |
| `internal/emerg/budget.go` | `internal/billing/cost.go` (cost calc helper) | partial — same pattern (pure helper that computes value, called from reconciler tick); novel: 60s rate-limited Sentry alert |
| `internal/emerg/vast/client.go` | (no direct analog — novel) | new — `pod/scripts/vast-ai.sh` is the **best reference** for response shape but Bash is not Go |
| `internal/emerg/errors.go` | `internal/breaker/errors.go` | exact (sentinel error pattern) |
| `db/migrations/0019_emergency_lifecycles.sql` | `db/migrations/0010_create_billing_events.sql` (DDL with CHECK + indexes) + `db/migrations/0016_evolve_tenants_shedding_limits.sql` (NOTIFY trigger pattern not needed here — config is env-only) | role-match (DDL only, no NOTIFY trigger) |
| `db/queries/emergency_lifecycles.sql` | `db/queries/billing.sql` (sqlc patterns: `:exec`, `:one`, `:many`, partial UPDATE with `sqlc.narg`) | exact |
| `cmd/gatewayctl/emerg.go` | `cmd/gatewayctl/upstreams.go` (subcommand dispatcher + tabwriter output + JSON flag) | exact |

### Breaker Subscriber (consume `gw:breaker:events`)

**Source:** `gateway/internal/breaker/subscribe.go` (Phase 3 D-D1) — replicate 1:1, but consume to maintain in-memory `local-llm` state with sustained-duration timer:

```go
// Phase 6 reconciler subscribes to gw:breaker:events to detect local-llm OPEN sustained
type localLlmTracker struct {
    state        atomic.Value // string: "closed" | "half-open" | "open"
    openSince    atomic.Int64 // unix when transitioned to OPEN; 0 if not OPEN
}

func (t *localLlmTracker) ApplyEvent(ev redisx.BreakerEvent) {
    if ev.Upstream != "local-llm" { return }
    t.state.Store(ev.State)
    if ev.State == "open" {
        if t.openSince.Load() == 0 {
            t.openSince.Store(time.Now().Unix())
        }
    } else {
        t.openSince.Store(0)
    }
}

func (t *localLlmTracker) SustainedFailedOverSeconds() int64 {
    if t.state.Load() != "open" { return 0 }
    since := t.openSince.Load()
    if since == 0 { return 0 }
    return time.Now().Unix() - since
}
```

### sqlc Query Organization

[VERIFIED: gateway/sqlc.yaml + db/queries/*.sql layout]

**Pattern:** One `.sql` file per logical group (often per table OR per workflow). Phase 6 adds `db/queries/emergency_lifecycles.sql` containing:

```sql
-- name: InsertEmergencyLifecycle :one
INSERT INTO ai_gateway.emergency_lifecycles (
    started_at, trigger_reason, leader_replica
) VALUES (
    NOW(), $1, $2
) RETURNING id;

-- name: UpdateEmergencyLifecycleVastIDs :exec
UPDATE ai_gateway.emergency_lifecycles
SET vast_offer_id = $2, vast_instance_id = $3, accepted_dph = $4,
    events = events || sqlc.arg('event_json')::jsonb
WHERE id = $1;

-- name: MarkEmergencyLifecycleHealthy :exec
UPDATE ai_gateway.emergency_lifecycles
SET first_health_pass_at = NOW(),
    events = events || sqlc.arg('event_json')::jsonb
WHERE id = $1;

-- name: CloseEmergencyLifecycle :exec
UPDATE ai_gateway.emergency_lifecycles
SET ended_at = NOW(),
    shutdown_reason = $2,
    total_cost_brl = $3,
    events = events || sqlc.arg('event_json')::jsonb
WHERE id = $1;

-- name: ListLiveEmergencyLifecycles :many
-- Used by leader recovery (D-D5): query rows with ended_at IS NULL.
-- Partial unique index guarantees ≤1 row.
SELECT id, vast_offer_id, vast_instance_id, started_at, events
FROM ai_gateway.emergency_lifecycles
WHERE ended_at IS NULL;

-- name: GetMonthlyCostBRL :one
-- Used by budget alert (D-D2). Sums closed lifecycles for current month.
SELECT COALESCE(SUM(total_cost_brl), 0)::numeric AS month_cost
FROM ai_gateway.emergency_lifecycles
WHERE started_at >= date_trunc('month', NOW())
  AND ended_at IS NOT NULL;

-- name: ListEmergencyLifecycles :many
-- gatewayctl emerg lifecycles --since N --limit M.
SELECT id, started_at, ended_at, trigger_reason, vast_offer_id, vast_instance_id,
       accepted_dph, total_cost_brl, shutdown_reason, leader_replica
FROM ai_gateway.emergency_lifecycles
WHERE started_at >= $1
ORDER BY started_at DESC
LIMIT $2;
```

Then add to `gateway/sqlc.yaml`:
```yaml
queries:
  - db/queries/upstreams.sql
  - db/queries/audit.sql
  # ...
  - db/queries/emergency_lifecycles.sql  # NEW
```

### Sentry Breadcrumb + CaptureMessage Pattern

[VERIFIED: gateway/internal/obs/sentry.go + sentry.AddBreadcrumb usage pattern]:

```go
// Each FSM transition — breadcrumb (not capture)
sentry.AddBreadcrumb(&sentry.Breadcrumb{
    Category:  "emerg",
    Message:   fmt.Sprintf("state %s→%s", from.String(), to.String()),
    Level:     sentry.LevelInfo,
    Timestamp: time.Now(),
    Data: map[string]interface{}{
        "lifecycle_id": lifecycleID,
        "reason":       reason,
    },
})

// Terminal error states — CaptureMessage with tags for forensics
hub := sentry.CurrentHub().Clone()
hub.Scope().SetTag("subsystem", "emerg")
hub.Scope().SetTag("lifecycle_id", strconv.FormatInt(lifecycleID, 10))
hub.Scope().SetTag("shutdown_reason", "offer_race_lost")
hub.Scope().SetExtra("attempts", 3)
hub.Scope().SetExtra("last_offer_id", offerID)
hub.CaptureMessage("emergency lifecycle aborted: offer_race_lost")
```

Sentry init is already done in `gateway/internal/obs/sentry.go` — Phase 6 just emits.

### Atomic.Pointer for Dispatcher Override

**Pattern:** In-process `atomic.Pointer[string]` para race-free read by dispatcher:

```go
// internal/upstreams/loader.go MODIFIED
type Loader struct {
    // ... existing fields ...
    tier0Override map[string]*atomic.Pointer[string] // role → URL override (LLM only in v1)
}

func (l *Loader) OverrideTier0(role, url string) {
    if p, ok := l.tier0Override[role]; ok {
        p.Store(&url)
    }
}

func (l *Loader) RestoreTier0(role string) {
    if p, ok := l.tier0Override[role]; ok {
        p.Store(nil)
    }
}

func (l *Loader) Resolve(role string, tier int) (UpstreamConfig, bool) {
    s := l.snap.Load()
    if s == nil { return UpstreamConfig{}, false }

    // Phase 6 — check override first for tier-0
    if tier == 0 {
        if p, ok := l.tier0Override[role]; ok {
            if overrideURL := p.Load(); overrideURL != nil && *overrideURL != "" {
                u, ok := s.byRoleTier[RoleTier{Role: role, Tier: 0}]
                if !ok { return UpstreamConfig{}, false }
                u.URL = *overrideURL // ephemeral override
                u.Name = "emergency_pod_" + role
                return u, true
            }
        }
    }

    u, ok := s.byRoleTier[RoleTier{Role: role, Tier: tier}]
    return u, ok
}
```

### Leader-Replica Identification

For `emergency_lifecycles.leader_replica` audit field — use `os.Hostname()` at boot:

```go
hostname, _ := os.Hostname() // e.g., "ai-gateway-dev-1.abc123" in Portainer stack
```

Phase 7 dashboard can attribute lifecycle to physical replica.

## Cold-Start Budget Evidence (Phase 1)

**Phase 1 baseline data** [VERIFIED: pod/onstart.sh lines 134-137 + 01-05-SUMMARY.md]:

- **Cold-start target:** 3-5 min ([CITED: 01-CONTEXT.md D-04])
- **Composition:**
  - Vast.ai container pull (~1-2 min for `nvidia/cuda` base layers; `ifix-ai-pod:v1.0` slim ~2GB)
  - MinIO weight pull (~2-3 min in parallel for Qwen GGUF + Whisper + BGE-M3, totalizing ~6GB; `inet_down ≥ 500 Mbps` filter ensures throughput)
  - llama-server / Speaches / Infinity warmup (mmap GGUF, CUDA kernel JIT) ~30-90s
- **onstart.sh hard timeout:** 600s (10min) — `READINESS_TIMEOUT_SECONDS=600`
- **WARN threshold:** > 300s in onstart.sh log indicates slow cold-start (operator triage)
- **Phase 6 budget:** `PROVISION_COLDSTART_BUDGET_SECONDS=600` (CONTEXT D-A4) = same 10min ceiling = bate SC-1 "≤10min once /health passes" literal

**Note:** Phase 1 HUMAN-UAT is still PENDING (smoke.yml not yet executed against real Vast.ai pod per STATE.md TODO). The 5min target is from D-04 design; **empirical confirmation comes from first smoke.yml run**. Phase 6 plan should NOT block on this — 10min budget already accommodates 2x slippage.

**`/health` endpoint shape** [VERIFIED: pod/health-bridge/handlers.go lines 67-79]:

```json
GET http://{pod_ip}:9100/health  →  HTTP 200 OR 503
{
  "status": "healthy" | "degraded" | "failed" | "unknown",
  "services": {
    "llm":   {"status": "healthy", "latency_ms": 234, "last_probe": "2026-05-13T...", "error": ""},
    "stt":   {"status": "healthy", ...},
    "embed": {"status": "healthy", ...}
  },
  "uptime_s": 412,
  "timestamp": "2026-05-13T14:32:18-03:00"
}
```

**Per-model endpoints:**
- `GET /health/llm`   → `{"status": "healthy", "latency_ms": ..., ...}` HTTP 200/503
- `GET /health/stt`   → same shape
- `GET /health/embed` → same shape
- `GET /health/live`  → `{"status": "ok"}` always 200 (process up)
- `GET /health/ready` → aggregate (same as `/health`)

**Phase 6 readiness check (PRV-07):**
- Poll `GET /health` (aggregate) every 5s
- Pass condition: HTTP 200 AND `body.status == "healthy"` AND `body.services.llm.status == "healthy"` (for LLM-only override; STT/embed nice-to-have but not blocker per CONTEXT D-E3 "só LLM em v1")
- Fail conditions: timeout (10min ceiling) OR HTTP 5xx for 6 consecutive probes (60s) OR `services.llm.status == "failed"`

**MinIO unreachable scenario (defensive note):** Phase 1 onstart.sh aborts with exit code 2 (`download failed`) if `mc cp` fails after internal retries. Pod never reaches `actual_status: running` for /health. Phase 6 detects via:
- `actual_status: exited` → close lifecycle `shutdown_reason='instance_exited'`
- 600s budget exhausted while still `loading` → close `shutdown_reason='health_timeout'`

## Test Infrastructure

### Testcontainers Pattern (Phase 4 reused integral)

**Source:** `gateway/internal/integration_test/setup_test.go` lines 38-100:

- Postgres 16-alpine container (shared across all integration tests via `sync.Once`)
- Redis 7-alpine container (shared)
- `freshSchema(t, ctx)` helper resets DB between tests via `db.Down + db.Up`
- Phase 6 reuses verbatim — no new container needed

```go
//go:build integration

func TestIntegration_EmergProvisionHappyPath(t *testing.T) {
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    pool, rdb := freshSchema(t, ctx)

    // Mock Vast.ai
    mockVast := newMockVastServer(t, mockVastResponses{
        searchOffers: []vast.Offer{{ID: 999, DphTotal: 0.35, ...}},
        createInstance: vast.CreateResponse{Success: true, NewContract: 12345},
        getInstance: vast.GetResponse{ActualStatus: "running", PublicIPAddr: "127.0.0.1", ...},
    })
    defer mockVast.Close()

    // Mock pod /health (via httptest.Server on a random port)
    mockPodHealth := newMockPodHealthServer(t, podHealthHealthy)
    defer mockPodHealth.Close()

    // Build reconciler with mocks injected
    reconciler := emerg.NewReconciler(emerg.Deps{
        DB:            pool,
        Redis:         rdb,
        VastClient:    vast.NewClientWithBaseURL(mockVast.URL),
        TickInterval:  100 * time.Millisecond, // accelerate for tests
        ColdBudget:    5 * time.Second,
        // ...
    })

    go reconciler.Run(ctx)

    // Simulate breaker.OPEN sustained
    publishBreakerEvent(t, rdb, "local-llm", "open")
    time.Sleep(200 * time.Millisecond) // tracker absorbs

    // Force trigger threshold to 100ms for test (env override)
    triggerEmergencyProvisioning(t, reconciler)

    // Wait for FSM to reach EMERGENCY_ACTIVE
    require.Eventually(t, func() bool {
        return reconciler.State() == emerg.StateEmergencyActive
    }, 5*time.Second, 100*time.Millisecond)

    // Assert lifecycle row created with vast_instance_id=12345
    var instID int64
    err := pool.QueryRow(ctx, "SELECT vast_instance_id FROM ai_gateway.emergency_lifecycles WHERE ended_at IS NULL").Scan(&instID)
    require.NoError(t, err)
    require.Equal(t, int64(12345), instID)
}
```

### httptest.Server Vast.ai Mocking

**Pattern:** Per-test mock with controllable responses + atomic counters for assertion:

```go
type mockVastServer struct {
    *httptest.Server
    searchHits  atomic.Int64
    createHits  atomic.Int64
    destroyHits atomic.Int64
    statusHits  atomic.Int64

    searchResponse atomic.Pointer[[]vast.Offer]
    createResponse atomic.Pointer[vast.CreateResponse]
    createStatus   atomic.Int32  // can override to 404 for bid race tests
}

func newMockVastServer(t *testing.T) *mockVastServer {
    m := &mockVastServer{}
    m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch {
        case strings.HasPrefix(r.URL.Path, "/bundles/"):
            m.searchHits.Add(1)
            offers := m.searchResponse.Load()
            if offers != nil {
                json.NewEncoder(w).Encode(map[string]any{"offers": *offers})
            }
        case strings.HasPrefix(r.URL.Path, "/asks/") && r.Method == "PUT":
            m.createHits.Add(1)
            status := int(m.createStatus.Load())
            if status == 0 { status = 200 }
            w.WriteHeader(status)
            if status == 200 {
                json.NewEncoder(w).Encode(*m.createResponse.Load())
            } else if status == 404 {
                json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "no_such_ask", "msg": "not available"})
            }
        case strings.HasPrefix(r.URL.Path, "/instances/") && r.Method == "DELETE":
            m.destroyHits.Add(1)
            json.NewEncoder(w).Encode(map[string]any{"success": true})
        case strings.HasPrefix(r.URL.Path, "/instances/") && r.Method == "GET":
            m.statusHits.Add(1)
            // ... return current state
        case r.URL.Path == "/users/current":
            json.NewEncoder(w).Encode(map[string]any{"id": 1, "email": "test@ifix"})
        }
    }))
    return m
}
```

### Race Detector

**Mandatory:** All tests run under `-race` flag (Phase 5 D-C5 pattern). Phase 6 has high concurrency (reconciler goroutine + Pub/Sub subscriber + provisionLifecycle goroutine + atomic.Pointer dispatcher reads). Race detector is non-negotiable.

## Validation Architecture

### Test Framework

| Property | Value |
|----------|-------|
| Framework | Go stdlib `testing` + `testcontainers-go v0.34.0` (Postgres 16 + Redis 7) |
| Config file | `gateway/internal/integration_test/setup_test.go` (build tag `integration`) |
| Quick run command | `cd gateway && go test -race ./internal/emerg/... ./internal/redisx/...` |
| Full suite command | `cd gateway && go test -race -tags=integration ./...` |
| Slow suite (live UAT scenarios) | `cd gateway && go test -race -tags="integration integration_slow" ./internal/integration_test/emerg_*_test.go` |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| PRV-01 | Vast REST client returns parsed Offer/Instance/error | unit | `go test -race ./internal/emerg/vast/...` | ❌ Wave 0 |
| PRV-02 | FSM 9-state transitions correctly under signals | unit | `go test -race ./internal/emerg/...` (TestFSMTransitions) | ❌ Wave 0 |
| PRV-03 | Leader lock blocks 2nd replica from advancing | integration | `go test -race -tags=integration ./internal/integration_test/emerg_leader_test.go` | ❌ Wave 0 |
| PRV-04 | Trigger fires after sustained `local-llm` OPEN ≥ N seconds | integration | `go test -race -tags=integration ./internal/integration_test/emerg_trigger_test.go` | ❌ Wave 0 |
| PRV-05 | Price cap rejects offers > $0.40/h; budget alert emits Sentry; partial unique index blocks 2nd live row | integration + unit | `go test -race ./internal/emerg/...` + integration TestEmergPriceCap | ❌ Wave 0 |
| PRV-06 | Mock Vast returns offer + create returns success → reconciler creates instance | integration | TestEmergProvisionHappyPath | ❌ Wave 0 |
| PRV-07 | /health poll passes only when `services.llm.status == "healthy"` | unit + integration | TestHealthCheck + TestEmergProvisionHappyPath | ❌ Wave 0 |
| PRV-08 | Cutback after 5min healthy + destroy after 5min idle | integration (with accelerated timers) | TestEmergCutback + TestEmergIdleDestroy | ❌ Wave 0 |
| PRV-09 | Cancel-in-flight pre-create + post-create both work + zero leak | integration | TestEmergCancelPreCreate + TestEmergCancelPostCreate | ❌ Wave 0 |
| PRV-10 | Each lifecycle leaves audit row with all fields | integration | TestEmergAuditCompleteness | ❌ Wave 0 |

### Sampling Rate

- **Per task commit:** `cd gateway && go test -race ./internal/emerg/...` (unit only, ~5s)
- **Per wave merge:** `cd gateway && go test -race -tags=integration ./internal/integration_test/emerg_*_test.go` (~30-60s)
- **Phase gate:** `cd gateway && go test -race -tags=integration ./...` (full suite, ~3min)
- **LIVE UAT:** post-deploy, manual `gatewayctl emerg force-provision --reason "phase6_uat"` against real Vast.ai (~5-15min real cold-start)

### Wave 0 Gaps

- [ ] `gateway/internal/emerg/fsm.go` + `fsm_test.go` — FSM 9-state transitions (PRV-02)
- [ ] `gateway/internal/emerg/vast/client.go` + `client_test.go` — Vast REST client w/ httptest.Server fixtures (PRV-01)
- [ ] `gateway/internal/emerg/lifecycle.go` + `lifecycle_test.go` — provision + cancel + destroy paths (PRV-06, PRV-09, PRV-10)
- [ ] `gateway/internal/emerg/recovery.go` + `recovery_test.go` — leader recovery 3 cenários (D-D5)
- [ ] `gateway/internal/emerg/budget.go` + `budget_test.go` — monthly budget alert (D-D2 / PRV-05)
- [ ] `gateway/internal/emerg/reconciler.go` + `reconciler_test.go` — 1Hz tick + leader CAS (PRV-03, PRV-04)
- [ ] `gateway/internal/emerg/subscribe.go` + `subscribe_test.go` — Pub/Sub gw:breaker:events consumer
- [ ] `gateway/internal/redisx/emerg.go` + `emerg_test.go` — Redis Hash + Pub/Sub helpers + redsync wrapper
- [ ] `gateway/internal/integration_test/emerg_provision_happy_test.go` — full provision happy path (testcontainers + mock Vast + mock pod /health)
- [ ] `gateway/internal/integration_test/emerg_cancel_pre_create_test.go` — cancel before create_instance (PRV-09)
- [ ] `gateway/internal/integration_test/emerg_cancel_post_create_test.go` — cancel after create (PRV-09); assert vast.destroy is called
- [ ] `gateway/internal/integration_test/emerg_leader_recovery_zombie_test.go` — leader crash + new leader finds zombie instance + destroys (D-D5)
- [ ] `gateway/internal/integration_test/emerg_budget_alert_test.go` — Sentry warning when month_cost > MONTHLY_EMERGENCY_BUDGET_BRL (D-D2 / PRV-05)
- [ ] `gateway/internal/integration_test/emerg_singleton_test.go` — DB partial unique index blocks 2nd live row (D-B5 / PRV-05)
- [ ] `gateway/db/migrations/0019_emergency_lifecycles.sql` — schema + indexes
- [ ] `gateway/db/queries/emergency_lifecycles.sql` — sqlc queries
- [ ] `gateway/sqlc.yaml` — append `db/queries/emergency_lifecycles.sql` to queries list
- [ ] `gateway/cmd/gatewayctl/emerg.go` — 4 subcommands (state, force-provision, force-destroy, lifecycles)
- [ ] `gateway/cmd/gateway/main.go` — wire NewReconciler + go reconciler.Run + dispatcher OverrideTier0 hook

**Test infrastructure: NONE missing — testcontainers + Postgres 16 + Redis 7 already in setup_test.go.**

### 5+ Minimum Integration Test Scenarios

| Scenario | What it validates | Mock setup | SC ref |
|----------|-------------------|------------|--------|
| **TestEmergProvisionHappyPath** | Full lifecycle: trigger → search → create → poll /health → register → cutback → destroy | Mock Vast (offers + 200 create + running status); mock pod /health (healthy after 1s) | SC-1, PRV-04..08, PRV-10 |
| **TestEmergCancelPreCreate** | local-llm recovers BEFORE create_instance — context cancelled, no instance ever created | Mock Vast search returns offers; before create called, recovery event fires | SC-3 |
| **TestEmergCancelPostCreate** | local-llm recovers DURING /health polling — destroy called before pod registered | Mock Vast (200 create + running status); cancel event fires during 5s poll loop; assert mockVast.destroyHits == 1 | SC-3, PRV-09 |
| **TestEmergLeaderRecoveryZombie** | Leader crashes mid-EMERGENCY_ACTIVE; new leader finds row + GetInstance returns zombie + destroys | Pre-seed `emergency_lifecycles WHERE ended_at IS NULL`; mock Vast GetInstance returns `actual_status: exited` | D-D5 |
| **TestEmergBudgetAlert** | After lifecycle close, month_cost crosses MONTHLY_EMERGENCY_BUDGET_BRL → Sentry warning emitted (1 per day dedupe) | Pre-seed lifecycles totaling R$199; trigger 1 more lifecycle costing R$5 → close → assert Sentry hub received message | PRV-05, D-D2 |
| **TestEmergSingletonDBIndex** | After lifecycle in EMERGENCY_ACTIVE, attempt to INSERT 2nd row with `ended_at IS NULL` returns unique violation | Direct SQL INSERT bypassing reconciler | PRV-05, D-B5 |
| **TestEmergPriceCap** | Mock Vast returns offer with `dph_total: 0.45` → reconciler skips (filter rejects); falls through to next offer | Mock Vast search returns 2 offers, first over cap | PRV-05, D-A2 |
| **TestEmergBidRaceLost** | Mock Vast create returns 404 `no_such_ask` for 3 consecutive attempts → lifecycle aborts with `shutdown_reason='offer_race_lost'` + Sentry warning | Mock Vast createStatus = 404 | D-A3 |
| **TestEmergMultiFailoverRideOut** | While in EMERGENCY_ACTIVE, new local-llm OPEN events do NOT create new lifecycles | After ACTIVE, publish new breaker.OPEN; assert lifecycle count unchanged | D-C4 |
| **TestEmergLeaderLockBlocks2ndReplica** | Spawn 2 reconciler goroutines sharing same Redis; only 1 acquires lock | 2x reconciler.Run sharing same rdb, force trigger; assert only 1 advances FSM | PRV-03, SC-2 |

### E2E Live UAT Scenarios

Post-deploy in `ai-gateway-dev` Portainer stack, operator runs:

| UAT scenario | Manual command | Validates |
|--------------|----------------|-----------|
| **UAT-1 force-provision** | `docker exec ifix-ai-gateway /gatewayctl emerg force-provision --reason "phase6_uat"` then poll `gatewayctl emerg state` | Full real Vast.ai integration (search → create → cold-start ~5min → /health pass → register → audit) |
| **UAT-2 budget tally** | Run UAT-1 several times, then `gatewayctl emerg lifecycles --since 24h` | Cost calc accuracy (BRL conversion, hours_active correct) |
| **UAT-3 force-destroy** | After UAT-1 in ACTIVE state, `docker exec ifix-ai-gateway /gatewayctl emerg force-destroy` | Manual destroy + audit row close + dispatcher restore |
| **UAT-4 Sentry breadcrumbs** | After UAT-1, check Sentry events tagged `subsystem=emerg` | Forensic visibility |
| **UAT-5 budget exceeded** | Pre-seed budget at R$199, run UAT-1 (~R$5 spent) → check Sentry | D-D2 alert fires |

## Pitfalls / Landmines

### Pitfall 1: Vast.ai actual_status `running` ≠ pod ready
**What goes wrong:** Reconciler sees `actual_status: running` and registers pod, but onstart.sh is still pulling weights from MinIO. First requests routed to emergency pod return 503 (llama-server not yet bound to port 8000).
**Why it happens:** Vast `actual_status` reflects **container** state, not application state. Phase 1 onstart.sh runs `docker compose up -d` then polls /health for 10min — which is exactly what Phase 6 must replicate.
**How to avoid:** Triple condition for readiness — `actual_status == "running"` AND `public_ipaddr != ""` AND HTTP 200 from `/health` with `services.llm.status == "healthy"`.
**Warning signs:** First-burst 5xx rate after `EMERGENCY_ACTIVE` transition.

### Pitfall 2: Vast.ai port mapping discovery
**What goes wrong:** Pod creates with port 9100 → Vast maps to dynamic public port. Phase 6 needs to know **which** public port to query for `/health`.
**Why it happens:** Vast assigns ephemeral ports per instance — exact field name in GET response is **not in current docs reading**. `pod/scripts/vast-ai.sh` doesn't use this (smoke.yml uses SSH instead, which Vast docs more clearly).
**How to avoid:** Spike during Phase 6 plan-phase: create 1 instance, GET response, document exact field path (`instances.machine.public_ipaddr_port_map["9100/tcp"]` is the typical Docker-style format). Alternative: pod's onstart.sh can register its own URL by writing to a Vast tag/label OR via SSH from the gateway (vast-ai.sh has `ssh-exec`).
**Recommended:** Use the same SSH primitive Phase 1 already uses — `vast.SSHExec(host, port, "curl -s localhost:9100/health")` proxied through SSH. Slower but eliminates port-mapping fragility. Or: Image v1.1 publishes `/etc/pod-url.txt` with its public ingress.
**Warning signs:** "connection refused" against the IP-port combo derived from `instances.public_ipaddr` + raw 9100.

**OPEN QUESTION FOR PLANNER:** Resolve port-discovery strategy in plan phase. Options: (a) GET response field parsing, (b) SSH proxy, (c) image-side URL self-registration. Recommend (a) with (b) fallback.

### Pitfall 3: Redis Pub/Sub is at-most-once / lossy
**What goes wrong:** Subscriber connection drops mid-incident, misses transition events. Replica state diverges silently.
**Why it happens:** Redis Pub/Sub doesn't persist messages; reconnect = miss messages between disconnect and resubscribe.
**How to avoid:** Phase 6 doesn't strictly need cross-replica state convergence (only leader acts; non-leader observes for visibility only). But `applyRemoteEvent` should not panic on missed messages. **Optionally** add a periodic HGETALL reconcile loop (Phase 5 pattern) — but Phase 6 leader recovery via DB query already covers the critical path. Recommend: **skip Phase 5-style reconcile**; rely on (a) DB query as source of truth for live lifecycle, (b) reconciler tick re-publishes current state every 60s as heartbeat.
**Warning signs:** Non-leader replica reports stale `gatewayctl emerg state`.

### Pitfall 4: redsync `mutex.Extend()` returns `(false, nil)` quietly
**What goes wrong:** Production code checks `err != nil` only; misses `ok==false` case (quorum not met without transport error). Extends silently fail; both replicas think they hold the lock.
**Why it happens:** Single-Redis deployment usually returns `(true, nil)` or `(false, ErrLockAlreadyExpired)`; the `(false, nil)` quorum case is rare but possible.
**How to avoid:** Always check **both** `ok` and `err`. Treat any combination other than `(true, nil)` as lost leadership: log + cede + let next tick re-attempt Lock.
**Warning signs:** 2 replicas both write to `emergency_lifecycles` → unique constraint violation surfaces.

### Pitfall 5: Vast.ai offer `dph_total` decimal precision
**What goes wrong:** Reading `dph_total` as `float64` and comparing to cap `0.40` — IEEE 754 arithmetic can give `0.4000000000001 > 0.40`. Marginally-priced offers rejected.
**Why it happens:** JSON parses `0.4` as `float64`, comparison can drift.
**How to avoid:** Compare with explicit epsilon: `if offer.DphTotal > cap+0.0001 { skip }`. OR use `*big.Float` (overkill). Stored cap in Postgres `NUMERIC(6,4)` (D-B4 schema) is fine for storage.
**Warning signs:** Search returns 0 offers when cap is at edge of inventory pricing.

### Pitfall 6: Vast.ai bid race window is **seconds**
**What goes wrong:** 2s/4s/8s exp backoff between retries — by attempt 3 (~14s elapsed), all top offers churned. 4090 inventory might have only 5-10 offers globally that match filter at any moment.
**Why it happens:** Spot-market for 4090 is highly contested.
**How to avoid:** Re-search with each retry attempt (CONTEXT D-A3 already specifies). Consider widening filter on retry 3 (e.g., relax `reliability` from 0.99 → 0.98). **Don't** widen `dph_total` cap — price cap is the hard guardrail.
**Warning signs:** Sentry `offer_race_lost` count spikes; investigate by bumping `gateway_vast_api_requests_total{op="search"}` baseline.

### Pitfall 7: Lifecycle DB INSERT before vast_instance_id known
**What goes wrong:** Schema D-B4 has BIGSERIAL `id` PK but `vast_instance_id` is nullable BIGINT. If we INSERT row at FSM transition to EMERGENCY_PROVISIONING (before create_instance succeeds), we have an "orphan" row. Leader crash here = recovery sees `vast_instance_id IS NULL` row.
**Why it happens:** We need a `lifecycle_id` to log events JSONB even before instance exists.
**How to avoid:** Recovery handles via `WHERE ended_at IS NULL AND vast_instance_id IS NULL → close as 'leader_recovery_pre_create'`. CONTEXT recovery code already covers this in D-D5.
**Warning signs:** Leader recovery logs frequent `pre_create` orphans = leader churn during provisioning.

### Pitfall 8: `mutex.UnlockContext` in defer of `Run()` swallows ctx
**What goes wrong:** `defer mutex.UnlockContext(ctx)` runs after ctx already cancelled → UnlockContext fails immediately because of cancelled ctx.
**Why it happens:** Standard Go gotcha.
**How to avoid:** Use a separate ctx for unlock: `releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 2*time.Second); defer releaseCancel(); _, _ = mutex.UnlockContext(releaseCtx)`.
**Warning signs:** Log shows unlock errors at every shutdown.

### Pitfall 9: `actual_status: unknown` from Vast == treat as destroy
**What goes wrong:** Treating unknown as transient retry → instance never recovers → wasted time + budget.
**Why it happens:** Vast docs say: "if `actual_status` becomes `exited`, `unknown`, or `offline` it will never reach `running` — destroy and retry".
**How to avoid:** In `waitForReadyOrDestroy`, treat `actual_status ∈ {exited, unknown, offline}` as terminal failure → destroy + close lifecycle `shutdown_reason='instance_terminal_state'`.
**Warning signs:** Lifecycle hangs in PROVISIONING for full 600s budget.

### Pitfall 10: Goose migration number conflict
**What goes wrong:** CONTEXT.md D-B4 says migration `0017_emergency_lifecycles.sql`, but Phase 5 already shipped `0017_evolve_upstreams_shed_thresholds.sql` and `0018_audit_log_shed_values.sql`. Phase 6 must use `0019`.
**Why it happens:** CONTEXT was written assuming Phase 5 used 0016 only.
**How to avoid:** Verify next free number via `ls gateway/db/migrations/ | sort -V | tail -1` → next number = current+1. Phase 6 = `0019_emergency_lifecycles.sql`.
**Warning signs:** Goose at boot reports "duplicate migration version".

### Pitfall 11: Sentry CaptureMessage rate-limit dedupe
**What goes wrong:** Reconciler tick = 1s. If month_cost stays > budget for hours, every tick emits CaptureMessage → Sentry inbox flooded.
**Why it happens:** No built-in dedupe in `sentry.CaptureMessage`.
**How to avoid:** In-memory dedupe state (CONTEXT D-D2 says "1 alerta por dia"):
```go
type budgetAlertDedupe struct {
    lastEmittedDay atomic.Int64 // unix day = epoch / 86400
}
func (b *budgetAlertDedupe) shouldEmit() bool {
    today := time.Now().Unix() / 86400
    return b.lastEmittedDay.CompareAndSwap(b.lastEmittedDay.Load(), today) // ← buggy; correct version below
}
// Correct:
func (b *budgetAlertDedupe) shouldEmit() bool {
    today := time.Now().Unix() / 86400
    last := b.lastEmittedDay.Load()
    if last == today { return false }
    return b.lastEmittedDay.CompareAndSwap(last, today)
}
```
**Warning signs:** Sentry quota burn alert.

### Pitfall 12: Vast SSH host-key acceptance
**What goes wrong:** If Phase 6 uses SSH-based `/health` proxy (Pitfall 2 fallback), each new pod has a fresh SSH host. Strict host-key checking = can't connect.
**Why it happens:** Vast assigns fresh ephemeral SSH hosts per pod.
**How to avoid:** Use `accept-new` policy with per-instance known_hosts (Phase 1 vast-ai.sh already does this). For Go: `ssh.InsecureIgnoreHostKey()` is acceptable for ephemeral pods (T-01-08-01 threat: short-lived session, no long-lived secrets — confirmed by Phase 1).
**Warning signs:** SSH connection failures in vast.SSHExec.

### Pitfall 13: Test acceleration of timer-based transitions
**What goes wrong:** Production cutback timing is 5min/5min. Integration test asserting cutback would take 10min wall clock per test — totally infeasible.
**Why it happens:** Timer logic uses `time.Now()` directly.
**How to avoid:** Make `PROVISION_HEALTHY_DURATION_SECONDS` and `PROVISION_IDLE_GRACE_SECONDS` env-tunable (CONTEXT D-D1 already says "env-tunable") — set to 1s in tests via env override. Same for `PROVISION_TRIGGER_FAILED_OVER_SECONDS` (D-C1) and `PROVISION_COLDSTART_BUDGET_SECONDS` (D-A4).
**Warning signs:** Phase 6 test suite runtime > 5min.

### Pitfall 14: `created_at` on emergency_lifecycles
**What goes wrong:** Schema D-B4 has `started_at` but no separate `created_at`. If row created at PROVISIONING but completed instance only at ACTIVE, "duration in PROVISIONING" not directly observable from base columns.
**Why it happens:** Single timestamp design optimizes for "duration of service" (SC-1, SC-4 ≤10min, ≤5min cutback) — not "time spent provisioning" (operational debug).
**How to avoid:** Use JSONB `events` column to store per-state-entry timestamps. Phase 7 dashboard can compute durations from event log.
**Warning signs:** Operator asks "how long did it take to provision?" — answer requires JSONB query.

### Pitfall 15: Cost calc using ended_at not first_health_pass_at
**What goes wrong:** CONTEXT D-D4 says `hours_active = (ended_at - first_health_pass_at)`, but schema D-B4 doesn't have `first_health_pass_at` column.
**Why it happens:** Schema design omitted intermediate timestamp.
**How to avoid:** **Add `first_health_pass_at TIMESTAMPTZ` column** to schema OR store in JSONB events. Recommend: explicit column for query simplicity (Phase 7 dashboard).
**Warning signs:** total_cost_brl computation has nothing to subtract from.

**OPEN QUESTION FOR PLANNER:** Add `first_health_pass_at TIMESTAMPTZ` column to migration 0019 OR derive from JSONB events.

## Open Questions for Planner (RESOLVED 2026-05-13)

All questions below were answered during planning. Inline `**RESOLVED:**` markers cite the plan/decision/spike that closed each item. Items still requiring runtime/spike-time judgement are marked `**RESOLVED:** Deferred to execution phase` with rationale.

1. **Vast port mapping discovery (Pitfall 2):** What's the exact JSON path in `GET /instances/{id}/` response for the public port mapped to container 9100? Spike to confirm. Fallback: SSH proxy.
   **RESOLVED:** Plan 06-06 Task 1 SPIKE (gate task, blocking) executes a $0.10–0.30 spike against Vast.ai LIVE; outcome documented in `06-SPIKE-vast-port-mapping.md` (artifact required by Plan 06-06). Implementation in `vast/types.go` + `lifecycle.go podHealthURL()` follows spike decision (a/b/c).
2. **HTTP retry policy values for vast.Client:** Beyond 3-attempt bid race, should transient 5xx errors retry? Recommend: 2 retries with 1s/3s backoff for 5xx, single attempt for 4xx (except 429 which gets 5s backoff).
   **RESOLVED:** Deferred to execution phase — `vast.Client` author (Plan 06-06 Task 2) implements the recommended policy via `cenkalti/backoff/v5` (already in go.mod). Rationale: policy is implementation detail; CONTEXT D-A1 only mandates 30s HTTP timeout; tunables intentionally left to code-time judgement.
3. **Migration number:** Confirm `0019_emergency_lifecycles.sql` (NOT 0017 as CONTEXT D-B4 says — Phase 5 already used 0017+0018).
   **RESOLVED:** Plan 06-02 migration is `0019_emergency_lifecycles.sql`. CONTEXT D-B4 mention of "0017" is superseded by RESEARCH evidence (Phase 5 consumed 0017+0018). All plans (02, 03, 06, 07, 08, 11) reference migration 0019.
4. **first_health_pass_at column (Pitfall 15):** Add explicit column to schema OR derive from JSONB? Recommend: explicit column.
   **RESOLVED:** Explicit `first_health_pass_at TIMESTAMPTZ` column added to migration 0019 (see DDL block at RESEARCH lines 1572-1592 + COMMENT line 1611-1612). D-D4 cost calc uses this column directly. Plan 06-02 generates `MarkEmergencyLifecycleHealthy` sqlc query that updates this column.
5. **Port discovery strategy:** Spike outcome — option (a) parse Vast response field, (b) SSH proxy via vast.SSHExec, (c) image self-registration. Recommend (a) with (b) fallback.
   **RESOLVED:** Plan 06-06 Task 1 SPIKE selects strategy at execution time (operator + Claude jointly). Default recommendation = (a) with (b) fallback per RESEARCH; final decision recorded in `06-SPIKE-vast-port-mapping.md` artifact.
6. **SSH client library:** If using SSH proxy fallback — `golang.org/x/crypto/ssh` (stdlib-adjacent) is the standard. Will need to add as direct dep.
   **RESOLVED:** Deferred to execution phase — only added if Plan 06-06 Task 1 spike selects strategy (b). If selected, executor adds `golang.org/x/crypto/ssh` to go.mod via `go get` during Plan 06-06 Task 2 implementation.
7. **Reconciler shutdown ordering:** Order matters for `main.go` graceful shutdown — (1) stop accepting new HTTP requests, (2) cancel reconciler ctx, (3) wait for active lifecycle goroutine to honor ctx (or 60s timeout), (4) release redsync lock, (5) close DB pool. Document in plan.
   **RESOLVED:** Plan 06-04 Run() loop already implements step (4) (Pitfall 8 separate releaseCtx for UnlockContext). Plan 06-10 Task 2 main.go wiring inherits the existing gateway shutdown order from Phase 2 (HTTP server first, then ctx cancel). Active lifecycle wait is delegated to ctx propagation (Plans 06+07 ensure ctx checks at every checkpoint).
8. **gatewayctl `emerg lifecycles` output format:** JSON-by-default vs tabwriter table? Recommend: tabwriter default with `--json` flag (matches Phase 4 `gatewayctl usage report`).
   **RESOLVED:** Plan 06-10 Task 1 implements `--format=table|json` flag, default `table` (tabwriter), per recommendation. Matches Phase 4 `gatewayctl usage report` ergonomic.
9. **Sentry sampling rate:** Phase 6 emits ~10 breadcrumbs per lifecycle. Sentry cost-per-event matters at scale. Recommend: breadcrumbs only (zero cost — attached to next CaptureMessage); CaptureMessage only on terminal errors (4-6 per failed lifecycle).
   **RESOLVED:** Pattern adopted across Plans 06-03 (FSM transition breadcrumbs only), 06-06/07/09 (CaptureMessage only on terminal errors via `captureTerminalSentry` helper per 06-PATTERNS.md "Padrão B"). Per D-E4.
10. **Reconciler `evaluateTick` mega-function:** 9-state FSM with timer-based + signal-based transitions has risk of becoming a 200-line switch. Recommend: 1 method per state — `evaluateHealthy(now)`, `evaluateDegraded(now)`, etc. Keeps each method short + testable.
    **RESOLVED:** Adopted per recommendation. Plans 06-05 (`evaluateHealthy`), 06-06 (case `StateEmergencyProvisioning` calls `startProvisioning`), 06-07 (cancel paths in respective evaluators), 06-08 (`evaluateEmergencyActive`, `evaluateRecovering`, `evaluateCooldown`) each define one method per state. After BLOCKER 3 resolution (drop OFF_HOURS + MAINTENANCE), reconciler covers 7 states with one method each.
11. **`onstart.sh` modification needed?** Phase 1 onstart pulls weights and starts services. Phase 6 emergency pod uses **same image + same onstart**. No modification needed — confirmed by reading pod/onstart.sh.
    **RESOLVED:** No modification. Phase 6 reuses `ghcr.io/ifixtelecom/ifix-ai-pod:v1.0` image + onstart untouched per CONTEXT specifics + D-domain item 7.
12. **VAST_API_QPS_LIMIT default:** No documented Vast rate limit. Recommend: 1 req/s sustained, 10 req burst. Configurable via env.
    **RESOLVED:** Static 1 req/s default in Plan 06-01 Task 1 env (`VAST_API_QPS_LIMIT=1`, operator-tunable via Portainer). Token bucket implementation in `vast.Client` is Claude's discretion per CONTEXT (last bullet of `### Claude's Discretion`).
13. **Image tag pinning:** CONTEXT specifics says `EMERGENCY_POD_IMAGE_TAG=v1.0` env override. Confirm: `:v1.0` exists in `ghcr.io/ifixtelecom/ifix-ai-pod` (Phase 1 publishes `:v1.0` AND `:latest`).
    **RESOLVED:** Confirmed in CONTEXT specifics ("Phase 1 publica `:v1.0` AND `:latest`"). Plan 06-01 Task 3 (operator gate) re-confirms via 06-WAVE0-GATES.md item 4 (operator can override tag if Phase 1 publishes newer pinned tag).

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go 1.23+ | All gateway code | (not on this control plane; on GHA runners only) | — | Build via GHA self-hosted runner `converseai-dev-vps-*` (Phase 2 D-08 pattern) |
| Postgres 16 with pgvector | DB tests + production | ✓ (DO managed) | 16 | testcontainers Postgres 16-alpine for tests |
| Redis 7 | Pub/Sub + redsync + breaker mirror + shed mirror | ✓ (`infra-redis-1` container) | 7 | testcontainers Redis 7-alpine for tests |
| Vast.ai API access | Production runtime | ✓ (key in env `VAST_AI_API_KEY` already provisioned per CLAUDE.md token store + GitHub Secret) | — | None — required for Phase 6 production; tests mock via httptest.Server |
| MinIO `s3.ifixtelecom.com.br` | Pod onstart weight pull | ✓ (Phase 1 verified) | — | None |
| `ghcr.io/ifixtelecom/ifix-ai-pod:v1.0` | Pod image | ✓ (Phase 1 plan-07 publishes) | v1.0 | `:latest` tag also published |
| Sentry DSN | Forensic alerts | ✓ (env `SENTRY_DSN` already configured per Phase 2 D-A4) | — | If empty, Sentry init is no-op (graceful skip) |
| Docker | Production deploy + LIVE UAT | ✓ (Portainer stack `ai-gateway-dev`) | — | None |

**Missing dependencies with no fallback:** None — all deps available.
**Missing dependencies with fallback:** None — Vast.ai is the only external dep and is mockable for tests.

## Project Constraints (from CLAUDE.md)

CLAUDE.md content discovered:

- **Comm rule:** "NEVER use speculative language: 'provavelmente', 'geralmente', 'possivelmente', 'talvez', 'pode ser que', 'likely', 'probably', 'maybe'. Always validate claims with evidence." — Phase 6 plan must avoid these in PLAN.md / SUMMARY.md / RUNBOOK.
- **GSD enforcement:** Use GSD commands; do not direct-edit outside workflow.
- **Dev env:** Gateway runs in Portainer stack `ai-gateway-dev` on `vps-ifix-vm` (10.10.10.30, Tailscale-reachable via subnet route from ops-claude). Logs accessed via `ssh vps-ifix-vm 'docker logs ai-gateway-dev-1'`.
- **GitHub PAT in CLAUDE.md:** `***REMOVED-GITHUB-PAT-see-CLAUDE.md***` — for git push from ops-claude.
- **Vast.ai API key in CLAUDE.md:** `***REMOVED-VAST-API-KEY-see-CLAUDE.md***` — already in GitHub Secret `VAST_AI_API_KEY` for repo `IfixTelecom/gpu-ifix`.
- **MinIO creds in CLAUDE.md:** Already configured for ai-gateway bucket; weights URL = `https://s3.ifixtelecom.com.br/ai-gateway`.
- **Output language:** pt-BR for prose; English for code identifiers, framework names, file paths, and technical terms (matches CONTEXT.md style).

## Code Examples

### Vast.ai Search Offers (Go translation of vast-ai.sh)

```go
// Source: gateway/internal/emerg/vast/client.go (planned)
// References: pod/scripts/vast-ai.sh `search` subcommand (Phase 1)

func (c *Client) SearchOffers(ctx context.Context, filter SearchFilter) ([]Offer, error) {
    // Build q JSON exactly like vast-ai.sh
    q, err := json.Marshal(filter)
    if err != nil { return nil, err }

    u := fmt.Sprintf("%s/bundles/?q=%s", c.baseURL, url.QueryEscape(string(q)))
    req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
    if err != nil { return nil, err }
    req.Header.Set("Authorization", "Bearer "+c.apiKey)

    obs.GatewayVastAPIRequestsTotal.WithLabelValues("search", "started").Inc()
    resp, err := c.httpClient.Do(req)  // HTTP timeout 30s (D-A1)
    if err != nil {
        obs.GatewayVastAPIRequestsTotal.WithLabelValues("search", "transport_error").Inc()
        return nil, err
    }
    defer resp.Body.Close()

    obs.GatewayVastAPIRequestsTotal.WithLabelValues("search", strconv.Itoa(resp.StatusCode)).Inc()
    if resp.StatusCode != 200 {
        return nil, c.parseErrorBody(resp)
    }

    var body struct {
        Offers []Offer `json:"offers"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
        return nil, err
    }
    return body.Offers, nil
}

// SearchFilter matches Vast.ai q JSON shape from pod/scripts/vast-ai.sh
type SearchFilter struct {
    GpuName       string                 `json:"gpu_name"`
    NumGpus       int                    `json:"num_gpus"`
    DiskSpace     map[string]float64     `json:"disk_space"`
    Reliability   map[string]float64     `json:"reliability"`
    InetDown      map[string]int         `json:"inet_down"`
    Rentable      bool                   `json:"rentable"`
    Verified      map[string]bool        `json:"verified"`
    DphTotal      map[string]float64     `json:"dph_total"`
    CudaMaxGood   map[string]float64     `json:"cuda_max_good"`
    Order         [][]string             `json:"order"`
    Limit         int                    `json:"limit"`
}

func DefaultSearchFilter(maxDPH float64) SearchFilter {
    return SearchFilter{
        GpuName:     "RTX 4090",
        NumGpus:     1,
        DiskSpace:   map[string]float64{"gte": 50},
        Reliability: map[string]float64{"gte": 0.99},
        InetDown:    map[string]int{"gte": 500},
        Rentable:    true,
        Verified:    map[string]bool{"eq": true},
        DphTotal:    map[string]float64{"lte": maxDPH},
        CudaMaxGood: map[string]float64{"gte": 12.4},
        Order:       [][]string{{"dph_total", "asc"}},
        Limit:       20,
    }
}
```

### Migration 0019 (DDL — addressing Pitfall 10 + 15)

```sql
-- gateway/db/migrations/0019_emergency_lifecycles.sql
-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway, public;

-- Phase 6 — emergency pod lifecycle audit table.
-- Source: CONTEXT.md D-B4 + Pitfall 15 fix (added first_health_pass_at).
CREATE TABLE IF NOT EXISTS ai_gateway.emergency_lifecycles (
    id                      BIGSERIAL PRIMARY KEY,
    started_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    first_health_pass_at    TIMESTAMPTZ,
    ended_at                TIMESTAMPTZ,
    trigger_reason          TEXT NOT NULL,
        -- 'failed_over_sustained' | 'manual_force'
    vast_offer_id           BIGINT,
    vast_instance_id        BIGINT,
    accepted_dph            NUMERIC(6,4),
    total_cost_brl          NUMERIC(10,4),
    shutdown_reason         TEXT,
        -- 'cutback_idle' | 'cancelled_in_flight' | 'health_timeout'
        -- | 'offer_race_lost' | 'manual' | 'budget_exceeded'
        -- | 'leader_recovery_zombie' | 'leader_recovery_lost' | 'leader_recovery_pre_create'
        -- | 'instance_terminal_state' | 'vast_5xx'
    events                  JSONB NOT NULL DEFAULT '[]'::JSONB,
        -- [{ts, from_state, to_state, reason, payload}]
    leader_replica          TEXT
        -- os.Hostname() of the gateway replica that was leader at lifecycle start
);

-- D-B5 — partial unique index: at most 1 row with ended_at IS NULL.
-- Defense-in-depth alongside leader-election + reconciler check (D-C5).
CREATE UNIQUE INDEX IF NOT EXISTS emergency_live_singleton
    ON ai_gateway.emergency_lifecycles ((TRUE)) WHERE ended_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_emergency_lifecycles_started_at
    ON ai_gateway.emergency_lifecycles (started_at DESC);

CREATE INDEX IF NOT EXISTS idx_emergency_lifecycles_live
    ON ai_gateway.emergency_lifecycles (ended_at) WHERE ended_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_emergency_lifecycles_month_cost
    ON ai_gateway.emergency_lifecycles (started_at)
    WHERE ended_at IS NOT NULL;

COMMENT ON TABLE ai_gateway.emergency_lifecycles IS
    'Audit trail for Vast.ai emergency pod lifecycles (Phase 6 PRV-10). One row per provision attempt; events JSONB captures full timeline.';
COMMENT ON COLUMN ai_gateway.emergency_lifecycles.first_health_pass_at IS
    'Timestamp when pod /health first returned healthy. Used as start of cost calc (D-D4): hours_active = ended_at - first_health_pass_at.';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_gateway.emergency_lifecycles CASCADE;
-- +goose StatementEnd
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Vast.ai CLI (Bash subprocess) | Pure Go REST client | Phase 6 PRV-01 | Eliminates subprocess overhead; testable with httptest.Server |
| Manual operator pod provision | FSM-driven autonomous | This phase | Removes human-in-loop for emergency response (≤10min vs hours of operator wake-up + ssh + vast-ai.sh) |
| Single replica leader | redsync-leader-elected multi-replica | This phase | Phase 7 deploys 2+ replicas; Phase 6 establishes the lock pattern |
| Custom mutex via SETNX+EXPIRE+Lua | `go-redsync/redsync v4` | This phase | Industry-standard Redlock; published 2026-02-25 stable |

**Deprecated/outdated:** None — Phase 6 is greenfield within an established codebase.

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | Vast 404 == bid race lost (vs offer truly missing) | Vast.ai REST API Reference / Bid race | Wrong handling: false positive on `no_such_ask` from a real bug → infinite re-search loop. Mitigation: D-A3 caps at 3 attempts. |
| A2 | Vast 410 = same as 404 for bid race purposes | Vast.ai REST API Reference / HTTP status codes | Same risk as A1; same 3-attempt cap mitigates. |
| A3 | Port 9100 mapping discoverable from GET /instances/{id}/ response | Pitfall 2 | If wrong: spike must implement SSH-proxy fallback before plan-phase ends. |
| A4 | `actual_status` value `unknown` → terminal (per docs) | Pitfall 9 | Treating unknown as transient = wasted budget on dead instance. Mitigation: docs CITED quote is explicit. |
| A5 | Phase 1 cold-start ~5min holds in production (smoke.yml has not yet executed) | Cold-start Budget Evidence | If real cold-start > 10min, SC-1 fails. Phase 6 budget 600s = 2x design target — accommodates 100% drift. |
| A6 | Phase 1 `ghcr.io/ifixtelecom/ifix-ai-pod:v1.0` is published and pullable | Architecture Diagram | Phase 1 plan-07 ships build-pod.yml. Operator must verify tag exists before Phase 6 LIVE UAT. |
| A7 | Vast doesn't return X-RateLimit headers (only message body) | Vast.ai REST API Reference / Rate Limits | If wrong: client could implement adaptive throttling. Currently uses static 1 req/s. CITED but verify in spike. |
| A8 | redsync `Extend()` `(false, nil)` quorum case is handleable identically to `ErrLockAlreadyExpired` (cede leadership) | Pitfall 4 | Both require ceding; only difference is log severity. Aligned handling is safe. |
| A9 | Vast SSH host-key acceptance via `accept-new` is acceptable (Phase 1 already does it for smoke.yml) | Pitfall 12 | T-01-08-01 threat already accepted in Phase 1; Phase 6 inherits the policy. |
| A10 | `os.Hostname()` returns deterministic per-replica identifier in Portainer | Leader-Replica Identification | If returns generic `localhost`, all replicas indistinguishable in audit. Verify via `docker exec ai-gateway-dev-1 hostname`. |

## Sources

### Primary (HIGH confidence)

- **In-house code (verified by direct file read):**
  - `gateway/internal/breaker/{breaker,subscribe,mirror,errors}.go` — Phase 3 D-D1 pattern
  - `gateway/internal/shed/{fsm,tick,subscribe}.go` — Phase 5 D-C1 + D-C3 pattern
  - `gateway/internal/upstreams/{loader,types,listen}.go` — Phase 3 D-D2 + Phase 5 hot-reload
  - `gateway/internal/redisx/{breaker,shed,client}.go` — Redis helper pattern
  - `gateway/internal/auditctx/override.go` — Context key pattern for audit overrides
  - `gateway/internal/obs/{metrics,sentry}.go` — Prometheus + Sentry init
  - `gateway/internal/integration_test/setup_test.go` — testcontainers Postgres 16 + Redis 7
  - `gateway/cmd/gatewayctl/{main,upstreams,shed,thresholds}.go` — CLI subcommand pattern
  - `gateway/db/migrations/0010_create_billing_events.sql` — DDL pattern with indexes
  - `gateway/db/queries/upstreams.sql` — sqlc query pattern
  - `gateway/sqlc.yaml` — sqlc config
  - `gateway/go.mod` — current dep versions
  - `pod/scripts/vast-ai.sh` — Phase 1 verified Vast endpoint behavior (search/create/status/wait-running/destroy)
  - `pod/onstart.sh` + `pod/health-bridge/handlers.go` — Phase 1 cold-start + /health shape
  - `.planning/phases/01-gpu-pod-image-smoke-test/{01-04,01-05,01-06,01-08,01-09}-SUMMARY.md` — Phase 1 outcomes
  - `.planning/phases/04-multi-tenant-quotas-billing-schedule-routing/04-08-SUMMARY.md` — testcontainers integration test pattern
  - `.planning/phases/05-load-shedding-saturation-aware-routing/05-PATTERNS.md` — Phase 5 pattern map (Phase 6 follows analog template)

### Secondary (MEDIUM confidence — official docs verified by WebFetch)

- [Vast.ai PUT /asks/{id}/ create-instance docs](https://docs.vast.ai/api-reference/instances/create-instance) — request/response shape, HTTP status codes (200/400/401/403/404/410/429), bid-race semantics
- [Vast.ai instances show/list docs](https://docs.vast.ai/api-reference/instances/show-instances) — `actual_status` values (running/exited/unknown/offline), instance object fields
- [Vast.ai rate-limits-and-errors docs](https://docs.vast.ai/api-reference/rate-limits-and-errors) — per-endpoint per-identity rate limit; HTTP 429 signal; no Retry-After header; error envelope variations
- [Vast.ai overview & quickstart](https://docs.vast.ai/api/overview-and-quickstart) — base URL, Bearer auth
- [pkg.go.dev/github.com/go-redsync/redsync/v4](https://pkg.go.dev/github.com/go-redsync/redsync/v4) — Mutex.Extend signature `(bool, error)`; `ErrLockAlreadyExpired`, `ErrFailed`, `ErrTaken`, `ErrNodeTaken`
- `proxy.golang.org/github.com/go-redsync/redsync/v4/@latest` returned `v4.16.0` published 2026-02-25 — VERIFIED current

### Tertiary (LOW confidence — flagged for spike validation)

- Vast.ai port mapping field name in GET response (Pitfall 2 / OPEN QUESTION 1) — must spike during plan-phase
- Vast.ai exact bid-race response code (404 vs 410) — both possible per docs; treat both as ErrOfferGone
- `os.Hostname()` value inside Portainer container (Assumption A10) — verify post-deploy

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — all libraries verified in go.mod or proxy.golang.org; redsync v4.16.0 confirmed current
- Architecture (in-house patterns): HIGH — Phase 3 + 5 patterns are extensively documented and Phase 6 directly replicates
- Vast.ai REST API: MEDIUM — official docs read but bid-race semantics + port discovery require empirical spike
- Pitfalls (in-house): HIGH — derived from Phase 3+5 hard-won lessons (Pub/Sub lossiness, MiB vs bytes, hot-reload race, etc.)
- Pitfalls (Vast-specific): MEDIUM — derived from docs + Phase 1 SSH/REST behavior; Pitfall 2 (port discovery) explicitly flagged

**Research date:** 2026-05-13
**Valid until:** 2026-06-13 (30 days for Vast.ai docs may shift; in-house patterns are stable)

## RESEARCH COMPLETE

Phase 6 reusa o template autoritativo Phase 3 D-D1 (breaker mirror + Pub/Sub) e Phase 5 D-C1 (atomic FSM + 1Hz tick), expandindo para 9 estados FSM com leader-election via `go-redsync/redsync v4.16.0` (já verificado current via proxy.golang.org). O cliente Vast.ai REST em Go é a única peça **realmente nova** — `pod/scripts/vast-ai.sh` Phase 1 já documenta endpoint shapes e exit codes (search/create/status/wait-running/destroy/ssh-exec/scp-upload), reutilizável como ground truth de "como Vast responde". Top 5 things para o planner internalizar: (1) **migration number é 0019**, não 0017 como CONTEXT diz — Phase 5 já consumiu 0017+0018; (2) adicionar coluna `first_health_pass_at TIMESTAMPTZ` para suportar D-D4 cost calc (`hours_active = ended_at - first_health_pass_at`); (3) **Vast port mapping discovery** (Pitfall 2) precisa de spike — recomendado parsing de `GET /instances/{id}/` field path com fallback SSH-proxy via vast-ai.sh `ssh-exec`; (4) `redsync.Extend()` retorna `(bool, error)` — sempre cheque **ambos**, treat any non-`(true, nil)` as lost leadership; (5) testcontainers Phase 4 harness reusa integral, mock Vast via httptest.Server com atomic counters (5+ scenarios mínimos: provision happy + cancel-pre-create + cancel-post-create + leader recovery zombie + budget alert + price cap + bid race + multi-failover ride-out + leader lock + DB singleton).
