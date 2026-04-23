# Phase 5: Load Shedding (Saturation-aware Routing) - Research

**Researched:** 2026-04-23
**Domain:** Gateway saturation detection, 4-state hysteresis FSM, per-tenant fairness, Redis-mirrored state convergence
**Confidence:** HIGH

## Summary

Fase 5 estende o gateway Go existente (Fases 2-4) com um detector de saturação composto (2-of-3: inflight + P95 + VRAM), um FSM de 4 estados com hysteresis (OFF → ARMED → ON → RECOVERING), e um middleware de overflow per-tenant que desvia tráfego para `openrouter-chat` durante pressão do tier-0. O CONTEXT.md já fechou toda a arquitetura macro (decisões D-A1..D-D4 + Claude's Discretion); esta pesquisa responde "como implementar bem e quais são os riscos concretos" — versões de libs, idiomas Go, armadilhas de Redis Pub/Sub, unidade do DCGM, e abordagem de load testing.

A boa notícia: **~90% da infraestrutura Fase 5 já existe como pattern em `gateway/internal/breaker/`**. O pacote `shed/` replica quase 1:1 a arquitetura (in-process autoritativo + Redis Hash mirror + Pub/Sub subscriber + OnStateChange callback). As únicas novidades conceituais são (1) o FSM de 4 estados hand-rolled (gobreaker não cobre este shape de máquina), (2) ring buffer lockless de latência, (3) dcgmScraper HTTP pull, e (4) middleware entre schedule e tokencount.

**Descoberta crítica:** `DCGM_FI_DEV_FB_USED` é reportado em **MiB**, não bytes. O default D-A4 (`shed_vram_used_bytes=22548578304`) assume bytes e precisa ser convertido (21 GB ≈ 21504 MiB) OU o campo renomeado para `shed_vram_used_mib`. Recomendação: **renomear campo para deixar a unidade explícita no schema**.

**Primary recommendation:**
1. Copiar estrutura `gateway/internal/breaker/` → `gateway/internal/shed/` (4 arquivos: fsm.go, set.go, subscribe.go, errors.go).
2. Hand-roll FSM com `atomic.Int32` (gobreaker v2 não suporta 4 estados com evaluator externo). Tick global 1s.
3. P95: `sort.Float64s` + index — `<50µs` para 200 samples, zero deps, idiomático.
4. DCGM parser: `prometheus/common/expfmt` (já é dep indireta no go.mod) — não hand-roll.
5. Mirror: **Redis Hash + Pub/Sub como Fase 3** — lossy Pub/Sub é aceitável porque Hash carrega estado autoritativo por-réplica (self-heal na próxima tick).
6. **Renomear `shed_vram_used_bytes` → `shed_vram_used_mib`** para casar com unidade do DCGM.

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Inflight counting (LSH-01) | Gateway middleware | — | Ground-truth per request; hot path `atomic.Int64`; nenhum outro tier tem essa visão per-tenant |
| Latency ring buffer (P95 de LSH-02) | Gateway (in-process) | — | Deve ser per-upstream e lockless; Prometheus seria um round-trip a cada 1s tick |
| DCGM VRAM scrape (LSH-02) | Gateway goroutine | Pod `:9400` | HTTP pull do gateway; pod exportando é passivo; fail-open se pod indisponível |
| FSM state machine (LSH-03) | Gateway in-process (autoritativo) | Redis Hash (mirror) + Pub/Sub (convergência) | Decisões lockless no hot path; Redis só para cross-replica (Fase 6) e dashboard (Fase 7) |
| Threshold config (LSH-04) | Postgres `upstreams.circuit_config` JSONB | — | Canal NOTIFY `upstreams_changed` já existe (Fase 3 D-D4); hot-reload <1s |
| Per-tenant cap (LSH-05) | Postgres `tenants` colunas novas | — | Canal `tenants_changed` já existe (Fase 4 D-C4); struct `TenantConfig` estendido |
| Overflow dispatch (LSH-05) | Gateway middleware + dispatcher | OpenRouter/Fireworks tier-1 | Decisão stampada no ctx, dispatcher lê e escolhe tier |
| Sensitive saturado 503 | Gateway middleware | — | Fail-fast pre-dispatch; nunca shed sensitive tenant para external (LGPD) |
| Tier-1 indisponível 503 | Gateway dispatcher | — | Sem fallback de fallback para chat (Fase 3 D-C4 reafirmado) |
| Ops overrides (shed-force) | Redis shadow keys com TTL | gatewayctl CLI | Operator desbloqueio durante incidente |

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions

**A. Sinal de saturação & thresholds**
- **D-A1:** Shed ativa com composite 2-of-3 de {inflight>max, P95>ms, VRAM_used>bytes} — NÃO usar OR-any, NÃO usar weighted score, NÃO usar inflight-primary isolado.
- **D-A2:** P95 calculado sobre ring buffer das últimas ~200 responses por upstream. Lockless (slice circular + atomic index). Sem deps externas. Size configurável via `SHED_LATENCY_RING_SIZE=200`. NÃO usar Prometheus histogram_quantile, HDR/tdigest, ou EWMA.
- **D-A3:** DCGM scrape via HTTP pull periódico de 5s ao `:9400/metrics` do pod. Timeout HTTP 2s. Fail-open: 3 scrapes consecutivos falhando → sinal VRAM marcado como "unknown", 2-of-3 efetivamente reduz a inflight+P95. Endpoint configurável via env `DCGM_EXPORTER_URL`. NÃO publicar do pod para Redis. NÃO fazer pull on-demand.
- **D-A4:** Thresholds em `upstreams.circuit_config` JSONB (estender, não criar tabela nova). Campos: `shed_inflight_max`, `shed_p95_ms`, `shed_vram_used_bytes`, `shed_arm_seconds` (30s default), `shed_recover_seconds` (60s default). Reusar trigger NOTIFY `upstreams_changed` existente. Migration `0017_evolve_upstreams_shed_thresholds.sql` faz UPDATE seed.

**B. Fairness per-tenant**
- **D-B1:** Hard cap per-tenant `local_inflight_max_{llm,stt,embed}` + overflow tier-1 quando atingido + FSM=ON. Outras tenants ainda veem slots livres locais. NÃO usar weighted fair-share (DRR/WRR). NÃO implementar pre-emption (deferred).
- **D-B2:** Colunas novas em `tenants`: `local_inflight_max_llm`, `local_inflight_max_stt`, `local_inflight_max_embed`, `priority_tier TEXT CHECK IN ('S','A','B')`. Migration `0016_evolve_tenants_shedding_limits.sql` com seed UPDATE dos 6 tenants conhecidos. Reusar canal NOTIFY `tenants_changed`. `priority_tier` em v1 é metadata only (sem preemption).
- **D-B3:** Sensitive tenant + FSM=ON + cap atingido → 503 imediato com `{type:'service_unavailable', code:'upstream_saturated_for_sensitive_tenant'}` + `Retry-After: 5`. SEM retry 4s (diferente de Fase 3 sensitive-breaker — saturação não é blip). Audit `upstream='shed_blocked_sensitive'`, `error_code='upstream_saturated_for_sensitive_tenant'`.
- **D-B4:** Middleware chain order (inalterada exceto pelo insert): `auth → idempotency → rate-limit → quota → schedule → SHEDDING → tokencount → dispatcher → billing-flush`. Shedding consulta tier do schedule via ctx; se já tier-1 → noop com métrica `skipped_peak_offhours`.

**C. Hysteresis FSM & breaker interaction**
- **D-C1:** FSM 4 estados: OFF → ARMED (30s) → ON → RECOVERING (60s) → OFF. Sinal caindo durante ARMED volta OFF imediato. Sinal subindo durante RECOVERING vai direto para ON (sem passar por ARMED). Implementação: `atomic.Int32` + `atomic.Int64` timestamps, tick global 1s. NÃO usar timer.AfterFunc per-FSM. NÃO usar 3-estados sem ARMED. NÃO usar leaky-bucket counter.
- **D-C2:** Escopo per-upstream tier-0 global — 3 FSMs (local-llm, local-stt, local-embed). NÃO per-tenant (18 FSMs). NÃO per-role (perde future-proofing para Fase 9/10 quando pods divergirem).
- **D-C3:** Persistência: in-process autoritativo + Redis mirror Hash `gw:shed:{upstream}` + Pub/Sub `gw:shed:events`. Mesmo pattern Fase 3 D-D1. Fallback silencioso com métrica `gateway_shed_mirror_failures_total` se Redis down. Novo package `gateway/internal/shed/` paralelo a `breaker/`: fsm.go, set.go, mirror.go, subscribe.go, errors.go.
- **D-C4:** Precedência: breaker wins. Ordem de verificação no dispatcher: `if breaker.open → tier-1; elif shed.on && tenantInflight>=cap → tier-1 ou 503-sensitive; else tier-0`.
- **D-C5:** Hot-reload via NOTIFY: thresholds swap atomic <1s, FSM re-avalia na próxima tick 1s (total <2s, bate SC-3). Se novos thresholds desaturariam o sinal: FSM vai ON → RECOVERING (preserva hysteresis). Operator override via Redis shadow key `gw:shed:force:{upstream}` com TTL (`gatewayctl shed-force`).

**D. Edge cases**
- **D-D1:** Tier-1 também indisponível (breaker open OR 429 Fireworks) durante shed → 503 `{code:'all_chat_upstreams_saturated'}` + `Retry-After: 30`. Audit `upstream='shed_tier1_unavailable'`. Aplica SÓ a chat (STT/embed mantêm fallback Fase 3 — openai-whisper e openai-text-embedding-3-small).
- **D-D2:** Streaming é pre-dispatch only. Requests em-flight seguem no upstream original; requests novas vão tier-1 durante FSM=ON. NUNCA abortar stream mid-flight.
- **D-D3:** Peak-off-hours é noop explícito. Schedule middleware já selecionou tier-1 antes; shed vê tier-1 e grava métrica `decision='skipped_peak_offhours'`.
- **D-D4:** Métricas Prometheus novas (~11 series + labels, total ~60 series — dentro do budget 10k Pitfall 13). Audit valores reservados: `shed_saturated`, `shed_blocked_sensitive`, `shed_tier1_unavailable`. Sentry breadcrumbs em transições FSM + 503s (não eventos — não-actionable).

### Claude's Discretion

- **Inflight counter implementação:** struct `shed.InflightRegistry` com `map[upstream]*atomic.Int64` (global) + `map[upstream]map[tenant]*atomic.Int64` (per-tenant). RWMutex apenas para populate do map de maps (não para inc/dec atomic). Increment em `shedMiddleware` (source of truth), decrement via defer. Tier-1 métrica separada `gateway_inflight_tier1{upstream}` (dashboard-only).
- **Ring buffer:** `[]uint32` (ms unsigned 32 bits, 4.3s max). Write index atomic; read copia snapshot + ordena. Env `SHED_LATENCY_RING_SIZE=200`.
- **dcgmScraper package:** `gateway/internal/dcgm/scraper.go`. HTTP client com transport custom (short keep-alive), parser mínimo (procurar SÓ `DCGM_FI_DEV_FB_USED`, `DCGM_FI_DEV_FB_TOTAL`). Goroutine `Run(ctx)` tick 5s.
- **FSM tick goroutine global:** `time.NewTicker(1*time.Second)` itera sobre todos FSMs. Evaluate computa `satScore` 2-of-3 e aplica transições.
- **shedMiddleware:** `gateway/internal/shed/middleware.go`. Lê schedule tier via ctx, consulta FSM + tenantCap, stampa `shed_decision` e upstream override via `auditctx.WithUpstreamOverride`.
- **gatewayctl subcommands:** `shed-state`, `shed-force {on|off}`, `thresholds set`, `tenant set-shed-limits`. Novos arquivos `shed.go`, `thresholds.go`, `tenants_shed.go` em `cmd/gatewayctl/`.
- **Migrations:** `0016_evolve_tenants_shedding_limits.sql`, `0017_evolve_upstreams_shed_thresholds.sql`, `0018_audit_log_shed_values.sql` (conditional — só se audit_log.upstream for ENUM).
- **Integration tests:** testcontainers-go + mock upstream HTTP server. SC-1 (burst+cap), SC-2 (hysteresis 120s — opt-in por duração), SC-3 (hot-reload <2s), SC-4 (anti-starvation), edge cases D-B3/D-D1/D-D3.

### Deferred Ideas (OUT OF SCOPE)

- Priority tier pre-emption real — deferred para Fase 10 ou v2
- Per-tenant FSM (18 FSMs) — rejeitado D-C2
- Autoscaling de caps per-tenant — v1 manual
- Abortar streams mid-flight quando FSM→ON — rejeitado Pitfall 3
- Fallback-de-fallback chat (tier-2 OpenAI direct) — rejeitado D-D1 (constraint PROJECT.md "Qwen fixo")
- DCGM via Redis key publish — rejeitado D-A3
- Pull on-demand DCGM — rejeitado D-A3
- HDR/tdigest para P95 — rejeitado D-A2
- EWMA em vez de P95 — rejeitado D-A2
- Prometheus histogram_quantile — rejeitado D-A2
- Tabela `shedding_policies` dedicada — rejeitado D-A4
- Weighted fair-share — rejeitado D-B1
- JSONB `shedding_config` em tenants — rejeitado D-B2
- Retry 4s sensitive saturated — rejeitado D-B3
- Shed dentro do dispatcher (em vez de middleware) — rejeitado D-B4
- 3-estados FSM — rejeitado D-C1
- Leaky-bucket counter — rejeitado D-C1
- FSM per-role — rejeitado D-C2
- In-process-only FSM state — rejeitado D-C3
- DB-persisted FSM state — rejeitado D-C3
- Shed wins durante HALF_OPEN — rejeitado D-C4
- Hot-reload com reset FSM — rejeitado D-C5
- Coluna `shed_reason` em audit_log — rejeitado D-D4
</user_constraints>

<phase_requirements>
## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| LSH-01 | Inflight counter por upstream no gateway (Go atomic) incrementa em pré-dispatch, decrementa em response | `sync/atomic.Int64` hot path [VERIFIED: Go stdlib, já em uso em `gateway/internal/breaker/`]; padrão `map[upstream]*atomic.Int64` + RWMutex só para populate (§Code Examples); benchmark sharded-maps não justifica esforço extra para 18 counters (§Don't Hand-Roll) |
| LSH-02 | Signal composto: inflight>N OR P95 janela 30s > threshold OR VRAM via dcgm-exporter > 21 GB | CONTEXT.md D-A1 trocou "OR" por "2-of-3" (mais robusto — §Common Pitfalls §Pitfall 2). DCGM em **MiB** [VERIFIED: dcgm-exporter CSV], threshold 21GB = 21504 MiB. Ring buffer lockless [VERIFIED: Go atomic ops] + `sort.Float64s` `<50µs` para 200 samples [CITED: Go 1.21 slices benchmarks]. Parser DCGM: `prometheus/common/expfmt.TextParser` [VERIFIED: pkg.go.dev, já dep indireta no go.mod] |
| LSH-03 | Histerese: só volta para local após sinal abaixo do threshold por 60s (previne flapping) | CONTEXT.md D-C1 estende com ARMED 30s (entrada) — hysteresis bidirecional. FSM hand-rolled 4 estados, tick global 1s (`atomic.Int32` state + `atomic.Int64` timestamps). Pitfall 2 (research) justifica time-sustained gate 30s/60s |
| LSH-04 | Thresholds configuráveis via Postgres reloadable sem restart | Canal NOTIFY `upstreams_changed` + loader atomic-swap já existem (Fase 3 D-D4 — `gateway/internal/upstreams/listen.go`, `loader.go`). Estender `CircuitConfig` struct com 5 campos novos + evoluir `parseCircuitConfig`. FSM consome via `fsm.UpdateConfig(cfg)` na próxima tick (<2s total) |
| LSH-05 | Overflow routing direciona excedente para OpenRouter enquanto local se recupera, mantendo outros tenants atendidos | Per-tenant hard caps D-B1 + middleware em `gateway/internal/shed/middleware.go` que override para tier-1 via `auditctx.WithUpstreamOverride` (padrão Fase 3). Dispatcher precedência breaker→shed→tier-0 (D-C4). Audit `upstream='shed_saturated'` |

</phase_requirements>

## Project Constraints (from CLAUDE.md)

CLAUDE.md é multi-projeto e aplica-se a toda pasta `/home/pedro/projetos/pedro/`; as restrições relevantes a este projeto Go:

- **GSD Workflow Enforcement:** Tasks só via `/gsd:*` — já em processo via este RESEARCH.
- **Dev Environment = VPS:** Máquina atual É a VPS dev (178.156.150.21). Deploy via Portainer webhook + GitHub Actions self-hosted runners (stack `converseai-v4-dev` — Phase 5 precisa confirmar qual stack/runner para `ifix-ai-gateway`).
- **Communication rules:** NEVER use speculative language ("provavelmente", "talvez", "likely"). Research claims must be evidence-backed (verified tool or cited doc).
- **Language:** Portuguese pt-BR com termos técnicos em inglês.

Do CONVENTIONS.md interno do repo `gpu-ifix`:
- `gofmt`/`go vet`/`golangci-lint` obrigatórios
- `slog` NDJSON com `module=UPPER_SNAKE_CASE`
- RFC3339 timestamps
- Sentinel errors pacote-level
- Conventional commits com scope `feat(05):`, `chore(05):`, `test(05):`

## Standard Stack

### Core

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `sync/atomic` | Go 1.23 stdlib | `atomic.Int32` FSM state, `atomic.Int64` counters + timestamps, `atomic.Pointer[T]` snapshot | [VERIFIED: Go stdlib] Zero-allocation, lockless on hot path; padrão já usado em `gateway/internal/upstreams/loader.go`, `breaker/breaker.go`, `tenants/loader.go` |
| `github.com/sony/gobreaker/v2` | v2.4.0 | Consultar `.State()` do breaker (D-C4 precedência); **não** usar como FSM de shedding | [VERIFIED: go.mod linha 18] Fase 3 já usa; Phase 5 só lê estado, não adiciona configuração |
| `github.com/prometheus/client_golang` | v1.20.5 | `promauto.NewGaugeVec`, `NewCounterVec` para ~11 métricas novas | [VERIFIED: go.mod linha 15] Padrão estabelecido em `gateway/internal/obs/metrics.go` |
| `github.com/prometheus/common/expfmt` | v0.62.0 | Parser do Prometheus text format ao scrape do pod `:9400/metrics` | [VERIFIED: go.mod linha 68, já indirect dep] `expfmt.NewTextParser(model.ValueValidationScheme).TextToMetricFamilies(io.Reader)` retorna `map[string]*dto.MetricFamily` — pega direto `DCGM_FI_DEV_FB_USED` |
| `github.com/redis/go-redis/v9` | v9.18.0 | Hash mirror `gw:shed:{upstream}`, Pub/Sub `gw:shed:events`, shadow keys `gw:shed:force:{upstream}` | [VERIFIED: go.mod linha 17] Padrão Fase 3 `gateway/internal/redisx/breaker.go` |
| `github.com/jackc/pgxlisten` | v0.0.0-20250802141604 | NOTIFY handler para `tenants_changed` e `upstreams_changed` | [VERIFIED: go.mod linha 13] Já em uso Fase 3/4; confirmed: single `*pgx.Conn`, `HandleNotification` é synchronously called, múltiplos canais safe no mesmo Listener [CITED: pkg.go.dev/github.com/jackc/pgxlisten] |
| `github.com/jackc/pgx/v5` | v5.7.1 | sqlc queries + pgxpool + pgtype | [VERIFIED: go.mod linha 12] Padrão estabelecido |
| `github.com/google/uuid` | v1.6.0 | Tenant/upstream IDs | [VERIFIED: go.mod linha 11] Padrão estabelecido |
| `github.com/getsentry/sentry-go` | v0.29.1 | Breadcrumbs em transições FSM + 503s | [VERIFIED: go.mod linha 9] Padrão Fase 3 |
| `github.com/pressly/goose/v3` | v3.23.0 | Migrations `0016`, `0017`, (`0018` conditional) | [VERIFIED: go.mod linha 14] Padrão estabelecido |
| `log/slog` | Go 1.23 stdlib | Structured NDJSON logging com `module=SHED`, `SHED_FSM`, `SHED_MIRROR`, `DCGM` | [VERIFIED: CONVENTIONS.md] |
| `github.com/go-chi/chi/v5` | v5.1.0 | Router + middleware chain `r.Group(pg.Use(...))` | [VERIFIED: go.mod linha 10] |

### Supporting

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `github.com/tsenart/vegeta/lib` | v12.12.0 (latest) | Load gen programático em integration tests (SC-1 burst, SC-2 oscillating) | [VERIFIED: pkg.go.dev/github.com/tsenart/vegeta/lib] API Go nativa: `vegeta.Rate{Freq:N, Per:time.Second}` + `vegeta.NewStaticTargeter` + `attacker.Attack(targeter, rate, duration, name)`. Embeddable no test, sem processo externo. Adicionar quando SC-2 test (oscillação 120s) for escrito — considerar opt-in tag como demais integration suite |
| `github.com/alicebob/miniredis/v2` | v2.37.0 | Unit tests do `shed.Set` + mirror sem testcontainers | [VERIFIED: go.mod linha 7] Padrão Fase 2/3 |
| `github.com/testcontainers/testcontainers-go` | v0.34.0 | Integration tests SC-1..SC-4 (Postgres 16 + Redis 7 + mock upstream HTTP server) | [VERIFIED: go.mod linha 19] Padrão Fase 3/4 |
| `net/http/httptest` | Go 1.23 stdlib | Mock upstream server (latency controlável, 500/429 controláveis) para SC-1..SC-4 | [VERIFIED: Go stdlib] |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| `sort.Float64s` + index para P95 | `github.com/influxdata/tdigest` (t-digest) | t-digest tem acurácia "few ppm" em p99.9 mas só ganha sobre stream massivo. Para 200 samples × 6 upstreams × 1 tick/s, `sort` é exato, <50µs, e sem nova dep. CONTEXT.md D-A2 já rejeitou tdigest como overkill. [CITED: github.com/influxdata/tdigest README] |
| `gobreaker` para FSM shed | Hand-rolled `atomic.Int32` FSM | gobreaker é binário (CLOSED ↔ OPEN ↔ HALF_OPEN via consecutive-failures counter). Shed FSM é 4 estados com evaluator externo (2-of-3 signals). API de gobreaker não expõe "transition externally-driven"; `OnStateChange` dispara só em resposta a `Execute` call. Hand-roll é idiomático aqui. [VERIFIED: github.com/sony/gobreaker/wiki draft-v3, API ReadyToTrip só lê `Counts`] |
| `prometheus/common/expfmt` full parser | Regex `^DCGM_FI_DEV_FB_USED\s+(\d+\.?\d*)` com `bufio.Scanner` | Regex é ~30 linhas + frágil (esquece comments, histograms quebram, multi-GPU hosts têm labels). `expfmt.TextParser.TextToMetricFamilies` é 3 linhas de código, já dep indireta, testado em milhões de prod deploys. Ambos precisam handle MiB unit. **Recomendação: expfmt** — o custo marginal é zero (já no vendored tree) |
| `redis.Streams` para cross-replica | `redis.Pub/Sub` (atual plano) | Streams é at-least-once + durable (XADD/XREAD), mas shed FSM **tolera lossy Pub/Sub** porque: (1) Hash mirror carrega estado autoritativo — réplica nova lê Hash no boot; (2) perda de 1 transição == divergência de <1s até próximo state change republicar; (3) Fase 3 breaker já tomou esta decisão e opera stable. Streams adiciona consumer-group complexity sem ganho proporcional. **Manter Pub/Sub, documentar a property.** [CITED: redis.io/docs/latest/develop/pubsub — at-most-once] |
| `k6` / `wrk2` externo | `vegeta/lib` embedded | k6 é process externo + JS scripts → não roda dentro de `go test -tags=integration`. wrk2 mesmo problema (C binary). Vegeta tem API Go nativa e roda in-process [VERIFIED: pkg.go.dev/github.com/tsenart/vegeta/lib] — ideal para testcontainers integration suite |
| Prometheus `histogram_quantile` | Ring buffer P95 in-process | Histogram_quantile depende de scrape cycle; sliding bucket boundaries enviesam p95 em janelas curtas; cardinality explode. Ring buffer é exato + lockless. CONTEXT.md D-A2 já rejeitou. |

**Installation:**

Nenhuma dep nova obrigatória — todas as libs já estão no `go.mod`. Para opcional vegeta load test:

```bash
go get github.com/tsenart/vegeta/lib@latest
```

**Version verification:**

Já verificadas contra `go.mod` no commit atual:
- `prometheus/common v0.62.0` [VERIFIED: go.mod linha 68, indirect] — promover para direct require ao adicionar import de `expfmt`
- `prometheus/client_golang v1.20.5` [VERIFIED: go.mod linha 15]
- `sony/gobreaker/v2 v2.4.0` [VERIFIED: go.mod linha 18]
- `redis/go-redis/v9 v9.18.0` [VERIFIED: go.mod linha 17]
- `jackc/pgxlisten v0.0.0-20250802141604-12b92425684c` [VERIFIED: go.mod linha 13]
- `getsentry/sentry-go v0.29.1` [VERIFIED: go.mod linha 9]

## Architecture Patterns

### System Architecture Diagram

```
[Request POST /v1/chat/completions]
            │
            ▼
    chi middleware chain:
    ┌─────────────────────────────────────────────────────────┐
    │ obs.RequestsMiddleware  (already mounted — Fase 4 HI-04)│
    │          │                                               │
    │          ▼                                               │
    │ auth.Middleware  (Fase 2)                                │
    │          │                                               │
    │          ▼                                               │
    │ audit.Middleware  (Fase 2)                               │
    │          │                                               │
    │          ▼                                               │
    │ idempotency (chat only, Fase 2/3)                        │
    │          │                                               │
    │          ▼                                               │
    │ quota.RateLimitMiddleware  (Fase 4)                      │
    │          │                                               │
    │          ▼                                               │
    │ quota.QuotaMiddleware  (Fase 4)                          │
    │          │                                               │
    │          ▼                                               │
    │ schedule.Middleware  (Fase 4 — writes tier-1 override    │
    │  │    via auditctx.WithUpstreamOverride when off-hours)  │
    │  │                                                       │
    │  ▼                                                       │
    │ ╔══ shed.Middleware  (Phase 5 — NEW) ══╗                 │
    │ ║  • read scheduled tier from ctx      ║                 │
    │ ║  • tier==1 → skipped_peak_offhours   ║                 │
    │ ║  • tier==0 → consult FSM + cap:      ║                 │
    │ ║    - OFF → pass                      ║                 │
    │ ║    - ON + inflight<cap → pass        ║                 │
    │ ║    - ON + inflight>=cap + normal →   ║                 │
    │ ║      override tier=1 (shed_saturated)║                 │
    │ ║    - ON + inflight>=cap + sensitive →║                 │
    │ ║      write 503 + audit + return      ║                 │
    │ ║  • increment inflight (atomic)       ║                 │
    │ ║  • defer decrement + record latency  ║                 │
    │ ╚══════════════════════════════════════╝                 │
    │          │                                               │
    │          ▼                                               │
    │ proxy.TokenCounter (Fase 3 — still inside Dispatcher)    │
    │          │                                               │
    │          ▼                                               │
    │ proxy.Dispatcher  (Fase 3, extended Fase 5):             │
    │   precedence: breaker → shed → tier-0                    │
    └─────────────────────────────────────────────────────────┘
                        │
          ┌─────────────┼─────────────┐
          ▼             ▼             ▼
      local-llm   openrouter-chat   503 envelope
      (tier-0)    (Fireworks pin    (sensitive or
                    Fase 3 D-C1)     tier1_unavail)

=== Parallel goroutines (started at boot via main.go) ===

[shed.FSMTicker — 1s tick] ──► For each upstream (3):
                                signals := {
                                  inflight: atomic.Int64 registry,
                                  p95:      ring.P95() via sort,
                                  vram:     atomic.Int64 from dcgmScraper,
                                }
                                satScore := count(signal >= threshold)
                                fsm.Evaluate(satScore >= 2)
                                │
                                ├─ on transition → publishShedEvent
                                │                  (Redis HSET + PUBLISH)
                                └─ update Prometheus gauge

[dcgm.Scraper — 5s tick] ──► GET http://pod-host:9400/metrics
                              parse via expfmt → DCGM_FI_DEV_FB_USED
                              atomic.Store(vramUsedMiB)
                              fail-open on 3 consecutive errors

[pgxlisten — existing Fase 3/4]
    LISTEN upstreams_changed ──► loader.Refresh ──► fsm.UpdateConfig
    LISTEN tenants_changed   ──► tenants.Loader.Refresh (extended)

[shed.Subscribe — cross-replica, Fase 6 ready] ──► consume gw:shed:events
                                                    ──► set.ApplyRemoteEvent
```

### Recommended Project Structure

```
gateway/internal/
├── shed/                    # NEW — pattern from breaker/
│   ├── fsm.go              # FSM 4 estados + Evaluate + UpdateConfig
│   ├── fsm_test.go         # unit tests: transition table, time hysteresis
│   ├── set.go              # map[upstream]*FSM + InflightRegistry + LatencyRing
│   ├── set_test.go         # unit tests: concurrent atomic ops, populate
│   ├── middleware.go       # chi middleware (decision + inflight tracking)
│   ├── middleware_test.go  # unit tests: sensitive path, normal path, skipped
│   ├── mirror.go           # publishTransition (Namespace = "gw:shed:")
│   ├── subscribe.go        # Pub/Sub loop consuming gw:shed:events
│   ├── subscribe_test.go   # miniredis round-trip
│   ├── errors.go           # sentinel errors (see §Established Patterns)
│   └── tick.go             # global FSMTicker goroutine
├── dcgm/                    # NEW
│   ├── scraper.go          # Run(ctx) + parse via expfmt
│   ├── scraper_test.go     # httptest server + fixture text/plain
│   └── errors.go           # ErrDCGMScrapeFailed
├── redisx/
│   ├── shed.go             # NEW — PublishShedEvent, WriteShedState,
│   │                       #       SubscribeShedEvents, WriteShedForce
│   └── shed_test.go
├── upstreams/types.go       # EXTEND CircuitConfig struct (5 new fields)
├── tenants/types.go         # EXTEND TenantConfig struct (4 new fields)
├── tenants/loader.go        # EXTEND Refresh to populate new fields
├── proxy/dispatcher.go      # EXTEND precedence (~10 lines)
├── auditctx/override.go     # EXTEND with WithShedDecision/FromContext
├── obs/metrics.go           # EXTEND with ~11 new collectors
├── config/                  # EXTEND env (DCGM_EXPORTER_URL etc)
└── integration_test/
    └── shed_*_test.go      # SC-1..SC-4 + edge cases

gateway/cmd/gatewayctl/
├── shed.go                 # NEW — shed-state, shed-force subcommands
├── thresholds.go           # NEW — thresholds set subcommand
└── tenants_shed.go         # NEW — tenant set-shed-limits subcommand

gateway/db/migrations/
├── 0016_evolve_tenants_shedding_limits.sql    # ALTER TABLE + seed UPDATE
├── 0017_evolve_upstreams_shed_thresholds.sql  # UPDATE circuit_config JSONB
└── 0018_audit_log_shed_values.sql             # conditional (ENUM add values)
```

### Pattern 1: Hand-rolled FSM com atomic.Int32

**What:** FSM 4 estados onde state é `atomic.Int32` e timestamps de entrada em cada estado são `atomic.Int64` (Unix seconds). Tick goroutine global avalia e transita.

**When to use:** FSM com evaluator externo (signals computed by different subsystem), múltiplos estados (>3), e necessidade de observar transições via callback — i.e., quando `gobreaker.Settings.ReadyToTrip` não se aplica.

**Example:**

```go
// Source: padrão derivado de gateway/internal/breaker/breaker.go + atomic ops idiomáticos
package shed

import (
    "sync/atomic"
    "time"
)

type State int32

const (
    StateOff State = iota
    StateArmed
    StateOn
    StateRecovering
)

func (s State) String() string {
    switch s {
    case StateOff:
        return "off"
    case StateArmed:
        return "armed"
    case StateOn:
        return "on"
    case StateRecovering:
        return "recovering"
    }
    return "unknown"
}

type FSM struct {
    upstream   string
    state      atomic.Int32 // actual State
    enteredAt  atomic.Int64 // Unix seconds when current state entered
    cfg        atomic.Pointer[Config]
    onChange   func(from, to State, reason string)
}

type Config struct {
    ArmSeconds     int64
    RecoverSeconds int64
}

type Signals struct {
    InflightOverMax bool
    P95OverMax      bool
    VramOverMax     bool
    VramUnknown     bool
}

// Evaluate is called once per tick (1s) by the global FSMTicker.
// It applies D-C1 transitions based on the 2-of-3 saturation gate.
// Fully lockless on the happy path (state didn't change).
func (f *FSM) Evaluate(now time.Time, sig Signals) {
    score := 0
    if sig.InflightOverMax {
        score++
    }
    if sig.P95OverMax {
        score++
    }
    if sig.VramOverMax && !sig.VramUnknown {
        score++
    }
    saturated := score >= 2

    cfg := f.cfg.Load()
    current := State(f.state.Load())
    entered := f.enteredAt.Load()
    elapsed := now.Unix() - entered

    switch current {
    case StateOff:
        if saturated {
            f.transition(StateOff, StateArmed, now, "signal_rose")
        }
    case StateArmed:
        if !saturated {
            // Signal dropped while arming — revert immediately (no hysteresis on false alarm)
            f.transition(StateArmed, StateOff, now, "signal_dropped_during_arm")
        } else if elapsed >= cfg.ArmSeconds {
            f.transition(StateArmed, StateOn, now, "arm_timeout_sustained")
        }
    case StateOn:
        if !saturated {
            f.transition(StateOn, StateRecovering, now, "signal_dropped")
        }
    case StateRecovering:
        if saturated {
            // Signal came back during recovery — jump straight to ON (no ARMED, we already know)
            f.transition(StateRecovering, StateOn, now, "signal_returned_during_recover")
        } else if elapsed >= cfg.RecoverSeconds {
            f.transition(StateRecovering, StateOff, now, "recover_timeout_clean")
        }
    }
}

// transition does the compare-and-swap. If state changed under us (another
// tick fired), we skip — next tick will re-evaluate fresh.
func (f *FSM) transition(from, to State, now time.Time, reason string) {
    if !f.state.CompareAndSwap(int32(from), int32(to)) {
        return
    }
    f.enteredAt.Store(now.Unix())
    if f.onChange != nil {
        f.onChange(from, to, reason)
    }
}

// State returns the current state for hot-path reads (dispatcher).
func (f *FSM) State() State {
    return State(f.state.Load())
}
```

**Critical property:** `Evaluate` é chamado por um tick goroutine ÚNICO (serializado), então o CAS raramente falha na prática — ele existe como safety net caso alguém adicione paralelização no futuro ou chame `transition` de um caminho não-tick (e.g., shed-force override).

### Pattern 2: Ring Buffer Lockless para P95

**What:** Slice circular pré-alocado com write index `atomic.Int64`. Writes são `atomic.LoadAdd` do índice + assignment direto (races benignos: racing writers podem sobrescrever um slot, impacto = <0.5% amostras perdidas). Reads copiam snapshot + `sort.Float64s` + index.

**When to use:** Percentile em janela curta (≤1000 samples) com hot path sensível a latência e tick de leitura de 1s.

**Example:**

```go
// Source: padrão derivado; validado em benchmarks Go slices.Sort (6% faster em 100 elem, 14% em 1K)
// [CITED: https://groups.google.com/g/golang-codereviews/c/rrg2l52NIxU]
package shed

import (
    "sort"
    "sync/atomic"
)

type LatencyRing struct {
    buf  []uint32 // ms; uint32 = 4.3s max (chat cap)
    size uint64
    idx  atomic.Uint64
}

func NewLatencyRing(size int) *LatencyRing {
    return &LatencyRing{buf: make([]uint32, size), size: uint64(size)}
}

// Record adds a sample. Benign race: two concurrent writers may both
// write to the same slot — we lose one sample out of thousands, no panic.
// Amortized cost: 1 atomic add + 1 slice write.
func (r *LatencyRing) Record(ms uint32) {
    i := r.idx.Add(1) - 1
    r.buf[i%r.size] = ms
}

// P95 copies the ring, sorts, and returns the 95th-percentile sample.
// Called once per tick (1s) per upstream — 3 ticks/s × sort of 200 floats
// ≈ 30µs total overhead. [CITED: Go 1.21 slices.Sort benchmarks]
func (r *LatencyRing) P95() uint32 {
    snap := make([]uint32, r.size)
    copy(snap, r.buf)
    // Drop zero samples (ring not full yet)
    out := snap[:0]
    for _, v := range snap {
        if v > 0 {
            out = append(out, v)
        }
    }
    if len(out) == 0 {
        return 0
    }
    sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
    idx := int(float64(len(out))*0.95 + 0.5)
    if idx >= len(out) {
        idx = len(out) - 1
    }
    return out[idx]
}
```

### Pattern 3: DCGM Scraper com expfmt

**What:** Goroutine com ticker 5s + HTTP client 2s-timeout + `prometheus/common/expfmt` parser → escreve `atomic.Int64` VRAM used em MiB + contador de falhas consecutivas.

**When to use:** Qualquer scraping de Prometheus text format em Go — é a lib upstream canônica.

**Example:**

```go
// Source: pkg.go.dev/github.com/prometheus/common/expfmt — NewTextParser signature verified 2026-04-23
package dcgm

import (
    "context"
    "fmt"
    "log/slog"
    "net/http"
    "sync/atomic"
    "time"

    "github.com/prometheus/common/expfmt"
    "github.com/prometheus/common/model"
    dto "github.com/prometheus/client_model/go"

    "github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

type Scraper struct {
    url           string
    client        *http.Client
    log           *slog.Logger
    interval      time.Duration
    vramUsedMiB   atomic.Int64 // gauge value
    vramUnknown   atomic.Bool
    consecutiveFail atomic.Int32
}

func New(url string, interval time.Duration, log *slog.Logger) *Scraper {
    return &Scraper{
        url: url,
        client: &http.Client{
            Timeout: 2 * time.Second,
            Transport: &http.Transport{
                DisableKeepAlives: false,
                MaxIdleConns:      1,
                IdleConnTimeout:   60 * time.Second,
            },
        },
        log:      log.With("module", "DCGM"),
        interval: interval,
    }
}

func (s *Scraper) Run(ctx context.Context) {
    t := time.NewTicker(s.interval)
    defer t.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-t.C:
            s.scrape(ctx)
        }
    }
}

func (s *Scraper) scrape(ctx context.Context) {
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
    resp, err := s.client.Do(req)
    if err != nil {
        s.fail("http_error", err)
        return
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        s.fail(fmt.Sprintf("status_%d", resp.StatusCode), nil)
        return
    }

    parser := expfmt.NewTextParser(model.UTF8Validation)
    families, err := parser.TextToMetricFamilies(resp.Body)
    if err != nil {
        s.fail("parse_error", err)
        return
    }

    fam, ok := families["DCGM_FI_DEV_FB_USED"]
    if !ok || len(fam.Metric) == 0 {
        s.fail("metric_missing", nil)
        return
    }
    // Single-GPU pod — take first sample (matches pod/smoke/smoke.py convention)
    var val float64
    if m := fam.Metric[0]; m.Gauge != nil {
        val = m.Gauge.GetValue()
    } else {
        s.fail("metric_not_gauge", nil)
        return
    }

    // DCGM_FI_DEV_FB_USED is in MiB — store as-is (MiB). Threshold in DB is also MiB.
    s.vramUsedMiB.Store(int64(val))
    s.vramUnknown.Store(false)
    s.consecutiveFail.Store(0)
    obs.GatewayVramUsedMiB.Set(val)
}

func (s *Scraper) fail(reason string, err error) {
    n := s.consecutiveFail.Add(1)
    obs.GatewayDcgmScrapeFailures.WithLabelValues(reason).Inc()
    if err != nil {
        s.log.Warn("dcgm scrape failed", "reason", reason, "err", err, "consecutive", n)
    } else {
        s.log.Warn("dcgm scrape failed", "reason", reason, "consecutive", n)
    }
    // CONTEXT.md D-A3 — fail-open after 3 consecutive failures
    if n >= 3 {
        s.vramUnknown.Store(true)
    }
}

// ReadMiB returns (vramUsedMiB, unknown). If unknown==true, FSM must
// treat the VRAM signal as not-contributing to the 2-of-3 gate.
func (s *Scraper) ReadMiB() (int64, bool) {
    return s.vramUsedMiB.Load(), s.vramUnknown.Load()
}
```

### Pattern 4: Per-tenant Inflight Registry

**What:** Nested map com `atomic.Int64` counters. Populate path usa RWMutex (rare — só no primeiro request de um tenant×upstream pair); hot path inc/dec é lockless.

**When to use:** Per-tenant counters de alta cardinalidade em hot path (até ~18k counters para 6 tenants × 3 upstreams × v2 expansion, mas realisticamente 18 counters em v1).

**Example:**

```go
// Source: padrão derivado — popula-once pattern comum em Go (ex: cache com sync.Map)
// Alternativa rejeitada: sync.Map — adds generic indirection overhead on hot path
// Alternativa rejeitada: sharded map — 18 counters não justifica; contention irrelevante em 200 RPS
package shed

import (
    "sync"
    "sync/atomic"

    "github.com/google/uuid"
)

type InflightRegistry struct {
    mu     sync.RWMutex
    global map[string]*atomic.Int64                 // upstream -> counter
    tenant map[string]map[uuid.UUID]*atomic.Int64   // upstream -> tenant -> counter
}

func NewInflightRegistry(upstreams []string) *InflightRegistry {
    r := &InflightRegistry{
        global: make(map[string]*atomic.Int64, len(upstreams)),
        tenant: make(map[string]map[uuid.UUID]*atomic.Int64, len(upstreams)),
    }
    for _, u := range upstreams {
        r.global[u] = &atomic.Int64{}
        r.tenant[u] = make(map[uuid.UUID]*atomic.Int64)
    }
    return r
}

// Inc increments both global and per-tenant inflight counters. Populate
// path (first time a tenant touches this upstream) takes the RWMutex
// briefly; steady state is lock-free.
func (r *InflightRegistry) Inc(upstream string, tenant uuid.UUID) {
    r.mu.RLock()
    g := r.global[upstream]
    tm := r.tenant[upstream]
    c, ok := tm[tenant]
    r.mu.RUnlock()

    if !ok {
        r.mu.Lock()
        if c, ok = r.tenant[upstream][tenant]; !ok {
            c = &atomic.Int64{}
            r.tenant[upstream][tenant] = c
        }
        r.mu.Unlock()
    }
    if g != nil {
        g.Add(1)
    }
    c.Add(1)
}

func (r *InflightRegistry) Dec(upstream string, tenant uuid.UUID) {
    r.mu.RLock()
    g := r.global[upstream]
    c := r.tenant[upstream][tenant]
    r.mu.RUnlock()
    if g != nil {
        g.Add(-1)
    }
    if c != nil {
        c.Add(-1)
    }
}

// TenantInflight returns the current count for (upstream, tenant).
// Hot path — lock-free atomic read.
func (r *InflightRegistry) TenantInflight(upstream string, tenant uuid.UUID) int64 {
    r.mu.RLock()
    c, ok := r.tenant[upstream][tenant]
    r.mu.RUnlock()
    if !ok {
        return 0
    }
    return c.Load()
}

// GlobalInflight returns the current count across all tenants for upstream.
func (r *InflightRegistry) GlobalInflight(upstream string) int64 {
    r.mu.RLock()
    c, ok := r.global[upstream]
    r.mu.RUnlock()
    if !ok {
        return 0
    }
    return c.Load()
}
```

### Pattern 5: FSM Ticker Global (single goroutine)

**What:** Uma goroutine com `time.NewTicker(1*time.Second)` itera sobre todos os FSMs (3 upstreams) e chama `Evaluate`. Mais simples que `time.AfterFunc` per-FSM — serialização é a feature, não o bug.

**Example:**

```go
// Source: padrão derivado; mesma filosofia que breaker.Set.Subscribe loop único
package shed

import (
    "context"
    "log/slog"
    "time"
)

type TickerDeps struct {
    Set          *Set
    Inflight     *InflightRegistry
    Latency      map[string]*LatencyRing  // per-upstream
    DCGM         *dcgm.Scraper
    ThresholdSrc func(upstream string) Thresholds
}

func RunTicker(ctx context.Context, d TickerDeps, log *slog.Logger) {
    log = log.With("module", "SHED_FSM")
    t := time.NewTicker(1 * time.Second)
    defer t.Stop()
    for {
        select {
        case <-ctx.Done():
            log.Info("FSM ticker stopping")
            return
        case now := <-t.C:
            d.Set.ForEach(func(upstream string, fsm *FSM) {
                th := d.ThresholdSrc(upstream)
                vram, unknown := int64(0), true
                if d.DCGM != nil {
                    vram, unknown = d.DCGM.ReadMiB()
                }
                sig := Signals{
                    InflightOverMax: d.Inflight.GlobalInflight(upstream) >= th.InflightMax,
                    P95OverMax:      d.Latency[upstream].P95() >= th.P95Ms,
                    VramOverMax:     vram >= th.VramMiB,
                    VramUnknown:     unknown,
                }
                fsm.Evaluate(now, sig)
            })
        }
    }
}
```

### Anti-Patterns to Avoid

- **`time.AfterFunc` per-FSM:** explode em edge cases quando signal oscila — fire+cancel race. Tick global é determinístico.
- **gobreaker para o shedding FSM:** gobreaker trip é baseado em consecutive-failure count; shedding é baseado em external signal evaluation. Abusar `ReadyToTrip` + fake Counts é frágil e confuso para leitor.
- **Separate mutex para cada atomic counter:** já é `atomic` — adicionar mutex ao redor defeats the purpose. Use RWMutex SÓ para populate do outer map.
- **Parser DCGM hand-rolled com regex:** `expfmt` é dep indireta existente (v0.62.0), 3 linhas, correto. Regex quebra em multi-GPU host com labels.
- **Publicar FSM transições em Pub/Sub SEM Hash mirror:** Pub/Sub é lossy — nova réplica boota sem estado. Hash é source of truth para boot/rehydration. Publicar nos DOIS é barato e obrigatório.
- **`sync.Map` para inflight counters:** força type assertion em hot path; slower than typed `map[string]*atomic.Int64` quando keys são conhecidos at boot.
- **Deferir decrement até dispatcher return:** dispatcher pode panicar ou escrever 503 sem retornar ao middleware — o `defer` deve estar no `shedMiddleware.ServeHTTP`, não no dispatcher. Garante decrement em todos os caminhos.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Prometheus text parser | Regex + bufio | `prometheus/common/expfmt.TextParser` | [VERIFIED: pkg.go.dev] já dep indireta, testado em milhões de deploys, handles comments/multi-GPU labels/histograms |
| Streaming percentile over 200 samples | t-digest inline | `sort.Float64s` + index | Exato, <50µs, idiomático Go. t-digest é para streams de milhões. CONTEXT.md D-A2 já rejeitou. |
| Circuit breaker binary trip | Novo wrapper | `sony/gobreaker/v2` (existing, Fase 3) | Já consumido via `.State()`. NÃO reusar para shed FSM (shape errado). |
| Redis Pub/Sub durable queue | Custom WAL em Postgres | `redis.Pub/Sub` + Hash mirror rehydrate | Pattern Fase 3 já opera estable. At-most-once é aceitável porque Hash carrega autoritativo. |
| Atomic counter with mutex | `sync.Mutex` + int | `sync/atomic.Int64` | Zero alloc, lockless, Go stdlib canonical. Pattern já usado em `gateway/internal/breaker/`. |
| Postgres LISTEN/NOTIFY loop | `pgx.Conn.WaitForNotification` manual | `github.com/jackc/pgxlisten` (existing) | Handles reconnect, multi-channel dispatch, serialização de handlers. Já dep. |
| Load generation em test | goroutine loops crafted manually | `github.com/tsenart/vegeta/lib` | API Go nativa, métricas built-in (p95/p99/error rate), controlled rate. |
| sqlc evolution para new columns | Raw SQL query | `sqlc generate` com evolved query file | Type-safe, consistency com Fase 2/3/4 pattern. |

**Key insight:** Fase 5 é quase inteiramente uma replicação mecânica do pattern `breaker/` + extensions idiomáticas ao shape 4-state FSM. O único hand-roll verdadeiro é a FSM — `gobreaker` não cobre esse shape, e toda alternativa de biblioteca externa adiciona deps sem ganho (o hand-roll são ~200 linhas testáveis).

## Runtime State Inventory

Fase 5 é uma fase **nova** (adiciona capability sem rename/refactor). Porém contém migrations + mudanças de schema em tabelas live (`tenants`, `upstreams`, possivelmente `audit_log`). Inventário de estado:

| Category | Items Found | Action Required |
|----------|-------------|-----------------|
| Stored data | (1) `ai_gateway.tenants`: novas colunas `local_inflight_max_{llm,stt,embed}`, `priority_tier` com defaults + seed UPDATE dos 6 tenants conhecidos. (2) `ai_gateway.upstreams.circuit_config` JSONB: UPDATE adiciona campos `shed_*` aos 3 tier-0 upstreams (local-llm, local-stt, local-embed). Tier-1 upstreams recebem campos NULL (noop). (3) `ai_gateway.audit_log.upstream`: 3 novos valores reservados (shed_saturated, shed_blocked_sensitive, shed_tier1_unavailable) — se coluna for ENUM, migration ALTER TYPE ADD VALUE obrigatório; se TEXT, só docs. | Migrations `0016`, `0017`, conditional `0018`. Data migration = seed UPDATE em `0016` para os 6 tenants; `0017` UPDATE JSONB dos 3 upstreams tier-0. Code edit = `TenantConfig` + `CircuitConfig` structs + sqlc queries. |
| Live service config | (1) Redis Ifix (`infra-redis-1`): novas chaves ao namespace `gw:` — `gw:shed:{upstream}` (Hash), `gw:shed:events` (Pub/Sub channel, cria on first publish), `gw:shed:force:{upstream}` (shadow key com TTL). Nenhuma chave existente é renomeada. (2) Portainer stack `ai-gateway-dev` no Portainer3: env vars novas a configurar — `DCGM_EXPORTER_URL`, `SHED_LATENCY_RING_SIZE`, `SHED_TICK_INTERVAL_MS`, `SHED_DCGM_SCRAPE_INTERVAL_MS`, `SHED_DCGM_TIMEOUT_MS`. | Nenhuma data migration em Redis (chaves novas são criadas automaticamente on write). Portainer stack env UI update manual após deploy Fase 5 — adicionar ao deferred-items de Phase 5. |
| OS-registered state | Nenhum. Gateway é processo único no container distroless (Plan 02-08). Não há Windows Task Scheduler, systemd unit renaming, cron etc. A goroutine do dcgmScraper + FSM ticker são internas ao processo. | Nenhuma. |
| Secrets/env vars | Nenhum secret novo. Fase 5 não adiciona keys, só env vars de configuração (todas não-secretas). `DCGM_EXPORTER_URL` aponta para IP privado da VPS pod — se mesma rede, nenhum auth header. | Nenhuma ação de secret. Adicionar 5 env vars ao Portainer stack UI (ver Live service config). |
| Build artifacts | Nenhum artefato stale. `gateway/docker-compose.yml` + `Dockerfile` já empacotam `gatewayctl` + `gateway` no mesmo image distroless (Plan 02-08). Novos subcommands `gatewayctl shed-*` e `thresholds set` e `tenant set-shed-limits` são compiladas no mesmo binário. | Rebuild image após merge. CI Actions (`build-gateway.yml`) publica `ghcr.io/ifixtelecom/converseai-gateway:develop` ao push — Portainer webhook puxa. |

**Canonical question:** *Após todos os arquivos do repo serem atualizados, quais sistemas runtime ainda têm o estado antigo cacheado/armazenado/registrado?*

- Redis: não carrega estado antigo (chaves `gw:shed:*` são novas — nenhuma preexiste).
- Postgres: colunas novas em `tenants` têm DEFAULT; linhas antigas ganham o default sem intervenção. Seed UPDATE dos 6 tenants conhecidos ajusta para valores calibrados (ConverseAI LLM=4, Telefonia STT=2, etc — conforme D-B2).
- FSM state: in-process reconstrói em ≤30s após boot (ARMED timeout). Primeiro boot inicia OFF — aceito.
- Métrica Prometheus: Counters começam zerados, gauges preenchem no primeiro tick — aceito.

**Nothing found in category:** Secrets/env vars (none new), OS-registered state (none — single process in container), Build artifacts (standard rebuild cycle).

## Common Pitfalls

### Pitfall 1: Unit Mismatch DCGM_FI_DEV_FB_USED (MiB vs bytes)

**What goes wrong:** Threshold configurado como bytes (`22548578304`) mas métrica chega em MiB (`21504`). Comparação falha silenciosa — sinal VRAM nunca dispara OU dispara imediatamente em idle (dependendo da direção). Shed nunca ativa ou está sempre ativo.

**Why it happens:** CONTEXT.md D-A4 escreve `shed_vram_used_bytes=22548578304` assumindo bytes. Mas [VERIFIED: github.com/NVIDIA/dcgm-exporter/etc/dcp-metrics-included.csv] `DCGM_FI_DEV_FB_USED` help text diz "Framebuffer memory used (in MiB)". 21 GB ≠ 22 bilhões MiB.

**How to avoid:**
1. **Renomear campo JSONB** para `shed_vram_used_mib` — deixa unidade explícita no schema.
2. Converter threshold: 21 GB → 21504 MiB (21 × 1024).
3. Nome da métrica Prometheus: `gateway_vram_used_mib` (não `_bytes`). Help text explícito "VRAM framebuffer used in MiB (from DCGM_FI_DEV_FB_USED)".
4. Sentinel error `ErrDCGMUnitMismatch` se parser detecta metric sem unidade MiB no help text (defensive).

**Warning signs:**
- FSM nunca sai de OFF em carga real (bytes threshold × MiB signal → signal sempre << threshold).
- OU FSM sempre em ON (se campos foram invertidos na comparação).

### Pitfall 2: Race benigna no LatencyRing buffer

**What goes wrong:** Dois writers concorrentes acertam o mesmo `idx%size` slot após `atomic.Add`. Um sample é perdido. P95 calculado é 0.5% impreciso.

**Why it happens:** Ring buffer sem lock deixa o write be `atomic.Add(idx)` + `buf[i%size] = value`. O add é atomic, mas o write não é. Writes consecutivos ao mesmo slot podem race.

**How to avoid:** **Aceitar a race** — CONTEXT.md D-A2 especifica ring "lockless". Para 200 samples a perda de até 2 samples em carga alta é irrelevante para p95. Documentar com comentário no código. Alternativas (`sync.Mutex` wrapping every write) custam latência em hot path e ganho nulo em precisão estatística.

**Warning signs:**
- Test race detector flagging buf[i] writes → espera-se e é OK (comment-out com justificativa ou usar `-race=false` para este test específico).

### Pitfall 3: Pub/Sub message lost → divergence across replicas

**What goes wrong:** Redis Pub/Sub é at-most-once [CITED: redis.io/docs/latest/develop/pubsub]. Se mensagem `gw:shed:events` é perdida (Redis replication failover, network blip, subscriber reconnect gap), réplica B não sabe que FSM da réplica A mudou. Divergência de decisão por ≤30s até próxima transição republicar.

**Why it happens:** Fire-and-forget semântica do Pub/Sub. Hash mirror é escrito mas réplica B não sabe que precisa re-ler.

**How to avoid:**
1. **Hash é autoritativo no boot:** Nova réplica faz `HGETALL gw:shed:{upstream}` para cada upstream ao subir, reconstrói FSM local.
2. **Periodic reconcile:** A cada 30s, subscriber thread faz HGETALL de todos upstreams e compara com estado local. Divergência → transição sintética. Isto é **paralelo a Pub/Sub**, não substituto. Fase 3 não tem mas Phase 5 deve adicionar — baixo custo (3 HGETALL/30s) e resolve o gap.
3. **Accept imperfection:** v1 Fase 5 é single-replica (Phase 6 introduz multi-replica). Fase 5 publica o contrato, Fase 6 valida convergência. OK para lossy em v1.
4. Métrica `gateway_shed_mirror_reconcile_total{result=ok|diverged}` para observabilidade em v2.

**Warning signs:**
- Fase 6 multi-replica: dashboards mostram réplicas com FSM=OFF enquanto outras estão FSM=ON na mesma upstream.
- Audit logs mostram `upstream='shed_saturated'` em uma réplica e `upstream='local-llm'` em outra ao mesmo tempo.

### Pitfall 4: pgxlisten "conn busy" ao adicionar 2º canal no mesmo Listener

**What goes wrong:** Adicionar `LISTEN tenants_changed` (Fase 4 já tem) + `LISTEN upstreams_changed` (Fase 3 já tem) no MESMO `pgxlisten.Listener` é suposto funcionar. Mas se dois handlers rodam em paralelo E chamam pgxlisten com nil `Conn`, pode dar "conn busy".

**Why it happens:** [CITED: pkg.go.dev/github.com/jackc/pgxlisten] `HandleNotification` é **synchronously called** por um único Listener. Handlers que fazem trabalho pesado (e.g., query para refresh) bloqueiam o dispatcher loop. Fase 4 fez `loader.Refresh(ctx)` no handler — isso ativa a conn. Se handler tenta usar o **mesmo** `*pgx.Conn` do listener, "conn busy".

**How to avoid:**
1. Handlers NUNCA usam o `*pgx.Conn` passado como 3º arg. Usam o `pgxpool` do gateway para queries (que é uma pool separada).
2. Verificar Fase 3/4 code: `upstreams/listen.go` já segue esse pattern (`loader.Refresh` usa `l.pool`, não o listen conn). Padrão correto.
3. Fase 5 apenas estende o loader existente — nenhum novo `pgxlisten.Listener` é criado. Reuse o mesmo.

**Warning signs:**
- Log NDJSON mostra `pgxlisten error: conn busy` repeatedly.
- NOTIFY received mas refresh não executa.

### Pitfall 5: Sentry breadcrumb buffer overflow durante oscillation

**What goes wrong:** FSM oscila OFF→ARMED→OFF→ARMED (signal flutuando no limite) dezenas de vezes por minuto. Cada transição adiciona breadcrumb. Sentry breadcrumbs são ring buffer de tamanho default 100 [CITED: develop.sentry.dev]. O breadcrumb real do erro é pushed out antes do incident.

**Why it happens:** Breadcrumbs são úteis para "what led to this error" — se 80 deles são "FSM oscillated", eles não ajudam diagnóstico.

**How to avoid:**
1. Breadcrumb **apenas em transições que mudam behavior observável** — OFF→ARMED é preview-only (requests ainda tier-0), OK para breadcrumb. ARMED→ON é real change (requests começam a shed), breadcrumb obrigatório. ON→RECOVERING é aviso de recovery, breadcrumb OK. RECOVERING→OFF é confirmation, breadcrumb OK.
2. **Rate-limit breadcrumb adicional** — se 2 transições para o mesmo (upstream, to-state) em <5s, suprimir a segunda. Log still NDJSON.
3. Em vez de breadcrumb por transição, considere breadcrumb sumarizado "FSM flapped 12 times in last 60s" ao fim de um período flapping — só se necessário. V1 começa simples (breadcrumb por transição limpa).
4. Sentry **event** (não breadcrumb) reservado para: tier1_unavailable 503, sensitive_capped 503, e 3+ consecutive DCGM scrape failures. CONTEXT.md D-D4 alinha.

**Warning signs:**
- Sentry dashboard cheio de eventos spurious de FSM transition.
- Breadcrumbs em eventos reais não mostram contexto (buffer cheio).

### Pitfall 6: Schedule middleware não stampa tier em ctx antes do shedding

**What goes wrong:** Shedding middleware assume que schedule já escolheu tier e stampou no ctx. Se schedule é OPCIONAL na chain (pode não rodar para routes sem schedule config), shedding lê ctx vazio e assume tier=0 → mas na verdade não havia decisão.

**Why it happens:** Fase 4 D-C2 implementou schedule via `auditctx.WithUpstreamOverride("off_hours_external")` — só stampa quando override é necessário. Se tenant está em peak window, NENHUMA chave é stampada; shedding precisa decidir com esse ctx vazio.

**How to avoid:**
1. Shedding middleware: se `auditctx.UpstreamOverrideFromContext(ctx) == ""` → tratar como "tier-0 selected by default" (schedule não impôs override). Proceder com FSM check.
2. Se `auditctx.UpstreamOverrideFromContext(ctx) != ""` → schedule já decidiu tier-1 (peak_offhours). Shed é noop, métrica `skipped_peak_offhours`.
3. Nova chave ctx `shed_decision` via novo helper em `auditctx` — clean separation:
   ```go
   auditctx.WithShedDecision(ctx, "shed_saturated" | "shed_blocked_sensitive" | "skipped_peak_offhours" | "passed")
   ```
4. Dispatcher lê AMBAS: se `UpstreamOverride` presente (schedule) → route lá; senão, consulta `ShedDecision` (shedding) → route tier-0 ou tier-1.

**Warning signs:**
- Audit logs mostram `upstream='openrouter-chat'` mas `shed_decision=NULL` ou `skipped_peak_offhours` — unclear qual middleware routed.
- Métrica `gateway_shed_decisions_total{reason='skipped_peak_offhours'}` = 0 mesmo em peak off-hours carga.

### Pitfall 7: Fireworks 429 sem Retry-After header

**What goes wrong:** CONTEXT.md D-D1 assume que tier-1 (openrouter-chat via Fireworks) retorna 429 com `Retry-After` clean. Na verdade Fireworks não documenta `Retry-After` explicitamente [CITED: docs.fireworks.ai/guides/quotas_usage/rate-limits] — retorna `x-ratelimit-limit-requests`, `x-ratelimit-remaining-requests`, `x-ratelimit-over-limit` mas não `Retry-After`.

**Why it happens:** Fireworks usa "dynamic per-minute" rate limits com spike arrest até 6000 RPM. 429 é retornado mas sem standard `Retry-After`.

**How to avoid:**
1. Gateway SC-2 caminho "tier-1 unavailable" trata ambos: breaker OPEN (consistent) E 429 do próprio tier-1 (erro transitório). Ambos → 503 `all_chat_upstreams_saturated` + `Retry-After: 30` (hardcoded pelo gateway, NÃO passa o Fireworks response through).
2. Se Fireworks responder com `Retry-After`, honrar; senão, default 30s. Código:
   ```go
   if ra := resp.Header.Get("Retry-After"); ra != "" {
       w.Header().Set("Retry-After", ra)
   } else {
       w.Header().Set("Retry-After", "30")
   }
   ```
3. **Não contar 429 de Fireworks como breaker failure** (Fase 3 D-A4 `IsSuccessful` já implementa — 4xx não é failure). Apenas 5xx e timeouts trippam breaker. 429 durante shed simplesmente promove a 503 sem alterar breaker state.

**Warning signs:**
- Apps cliente veem 503 `all_chat_upstreams_saturated` sem `Retry-After` — backoff client pode ser infinito ou agressivo demais.

### Pitfall 8: audit_log.upstream column type check

**What goes wrong:** Assumir que `audit_log.upstream` é ENUM e preparar `ALTER TYPE ADD VALUE`. Mas Fase 2 pode ter modelado como TEXT com CHECK constraint — em que caso `ADD VALUE` falha.

**Why it happens:** CONTEXT.md D-D4 diz "migration `0018_audit_log_shed_values.sql` se `audit_log.upstream` for ENUM". Conditional mas não verificado.

**How to avoid:**
1. Antes de escrever `0018`, rodar: `SELECT udt_name, data_type FROM information_schema.columns WHERE table_schema='ai_gateway' AND table_name='audit_log' AND column_name='upstream';`
2. Se `data_type='USER-DEFINED'` → ENUM; escrever `ALTER TYPE ai_gateway.upstream_kind ADD VALUE 'shed_saturated';` × 3.
3. Se `data_type='text'` → TEXT com CHECK; escrever `ALTER TABLE ai_gateway.audit_log DROP CONSTRAINT audit_log_upstream_check; ALTER TABLE ADD CONSTRAINT audit_log_upstream_check CHECK (upstream IN (...))`. Mas pode nem haver CHECK — em que caso migration `0018` é skipped (doc-only).
4. Plan phase 5 deve ter Wave 0 task: "inspect audit_log.upstream schema, decide if 0018 needed".

### Pitfall 9: FSM tick clock skew sob alta carga

**What goes wrong:** Ticker 1s pode driftar sob CPU saturation (4 vCPU no gateway + shedding load = CPU press). Tick real vira 1.2s, 1.5s, 2s. ARMED timeout "30s" vira efetivamente 35-40s. SC-2 fica no limite do aceito.

**Why it happens:** Go `time.Ticker` é monotonic mas preempted sob CPU pressure. Não é real-time.

**How to avoid:**
1. ARMED default 30s tem margem natural para SC-2 "≤30s" — se real vira 32s pós-drift, ainda dentro de spec razoável.
2. Medir drift via métrica `gateway_shed_tick_drift_seconds{drift_bucket}` — alert warning se p95 drift > 2s.
3. Se drift virar problema sistemático, reduzir TICK_INTERVAL para 500ms (double work, metade do drift).
4. **Não** usar wall clock (`time.Now()`) para timestamps in FSM — USE monotonic `time.Now().Unix()` consistent. CONTEXT.md D-C1 aceita unix seconds (1s resolution).

**Warning signs:**
- SC-2 test fail intermitente em CI (CPU constrained).
- Métrica drift consistently > 1s em prod.

### Pitfall 10: gatewayctl shed-force TTL expires, operator surprise

**What goes wrong:** Operator roda `gatewayctl shed-force off --upstream local-llm --ttl 300s` durante incidente. 5 min depois TTL expira, FSM re-evalua sinais → imediatamente FSM=ON (incidente não resolveu). Tráfego volta a shed, operator é surpreso.

**Why it happens:** TTL é silent expiry. Sem notificação.

**How to avoid:**
1. Métrica `gateway_shed_force_active{upstream}` gauge 0/1 — alert crítico "operator override ativo há mais de TTL" (Fase 7 OBS-04).
2. Sentry breadcrumb ao shed-force set + ao expire (subscribe handler detecta key expire via Redis Keyspace Notifications OU polling).
3. `gatewayctl shed-state` mostra TTL restante no override (read remainging TTL via `PTTL`).
4. Docs runbook: "override é emergency, não config — monitor até FSM recuperar naturalmente".

## Code Examples

### Example 1: shedMiddleware — a inserção na chain

```go
// Source: pattern Fase 3 dispatcher + Fase 4 schedule middleware, este é novo em gateway/internal/shed/middleware.go
package shed

import (
    "net/http"
    "time"

    "github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
    "github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
    "github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
    "github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
    "github.com/ifixtelecom/gpu-ifix/gateway/internal/tenants"
    "github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

type MiddlewareDeps struct {
    Loader    *upstreams.Loader
    Tenants   *tenants.Loader
    Set       *Set
    Inflight  *InflightRegistry
    Latency   map[string]*LatencyRing // per-upstream
    RoleFor   func(path string) string // "llm" | "stt" | "embed"
}

func Middleware(d MiddlewareDeps) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            ac, ok := auth.FromContext(r.Context())
            if !ok {
                next.ServeHTTP(w, r) // let downstream auth middleware handle
                return
            }

            // Peak-off-hours short-circuit (D-D3)
            if override := auditctx.UpstreamOverrideFromContext(r.Context()); override != "" {
                ctx := auditctx.WithShedDecision(r.Context(), "skipped_peak_offhours")
                obs.GatewayShedDecisions.WithLabelValues("", "skipped_peak_offhours").Inc()
                next.ServeHTTP(w, r.WithContext(ctx))
                return
            }

            role := d.RoleFor(r.URL.Path)
            t0, ok := d.Loader.Resolve(role, 0)
            if !ok {
                next.ServeHTTP(w, r) // dispatcher handles no-primary case
                return
            }

            fsm, _ := d.Set.Get(t0.Name)
            if fsm == nil || fsm.State() != StateOn {
                // FSM off/armed/recovering → tier-0 allowed
                d.trackAndPass(w, r, next, t0.Name, ac.TenantID)
                return
            }

            // FSM=ON — check per-tenant cap
            tcfg, err := d.Tenants.Get(ac.TenantID)
            cap := resolveCapForRole(tcfg, role) // from D-B2 new fields
            if err != nil || int64(cap) == 0 {
                cap = defaultCapForRole(role)
            }

            inflight := d.Inflight.TenantInflight(t0.Name, ac.TenantID)
            if inflight < int64(cap) {
                // Still under cap — allow tier-0
                d.trackAndPass(w, r, next, t0.Name, ac.TenantID)
                return
            }

            // Cap exceeded — shed or block
            if ac.DataClass == auth.DataClassSensitive {
                // D-B3 — 503 immediate
                ctx := auditctx.WithShedDecision(r.Context(), "shed_blocked_sensitive")
                ctx = auditctx.WithUpstreamOverride(ctx, "shed_blocked_sensitive")
                obs.GatewayShedDecisions.WithLabelValues(t0.Name, "sensitive_capped").Inc()
                obs.GatewayShedBlockedSensitive.WithLabelValues(tcfg.Slug).Inc()
                w.Header().Set("Retry-After", "5")
                httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
                    "service_unavailable",
                    "upstream_saturated_for_sensitive_tenant",
                    "Primary upstream is saturated; sensitive-data tenants cannot be routed to external providers.")
                // request ends here — set ctx for audit middleware to read
                *r = *r.WithContext(ctx)
                return
            }

            // D-B4 normal tenant — override to tier-1
            t1, ok := d.Loader.Resolve(role, 1)
            if !ok {
                // Highly unusual — no tier-1 exists for this role. Treat as unavailable.
                obs.GatewayShedDecisions.WithLabelValues(t0.Name, "tier1_unavailable").Inc()
                httpx.WriteOpenAIError(w, http.StatusServiceUnavailable,
                    "service_unavailable", "all_chat_upstreams_saturated",
                    "Primary saturated and no secondary configured.")
                return
            }
            ctx := auditctx.WithShedDecision(r.Context(), "shed_saturated")
            ctx = auditctx.WithUpstreamOverride(ctx, t1.Name)
            obs.GatewayShedDecisions.WithLabelValues(t0.Name, "tenant_cap").Inc()
            // NOTE: we do NOT increment inflight for tier-1 in Inflight registry;
            // separate gauge gateway_inflight_tier1 tracks it for dashboard only (§Claude's Discretion)
            obs.GatewayInflightTier1.WithLabelValues(t1.Name).Inc()
            defer obs.GatewayInflightTier1.WithLabelValues(t1.Name).Dec()
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}

func (d MiddlewareDeps) trackAndPass(w http.ResponseWriter, r *http.Request, next http.Handler, upstream string, tenantID uuid.UUID) {
    d.Inflight.Inc(upstream, tenantID)
    start := time.Now()
    defer func() {
        d.Inflight.Dec(upstream, tenantID)
        elapsed := uint32(time.Since(start).Milliseconds())
        if ring := d.Latency[upstream]; ring != nil {
            ring.Record(elapsed)
        }
    }()
    ctx := auditctx.WithShedDecision(r.Context(), "passed")
    next.ServeHTTP(w, r.WithContext(ctx))
}
```

### Example 2: Extending CircuitConfig struct + JSONB

```go
// Source: extend gateway/internal/upstreams/types.go
type CircuitConfig struct {
    Failures  uint32        `json:"failures,omitempty"`
    Cooldown  time.Duration `json:"-"`
    CooldownS int           `json:"cooldown_s,omitempty"`

    // Phase 5 extensions
    ShedInflightMax     int   `json:"shed_inflight_max,omitempty"`
    ShedP95Ms           int   `json:"shed_p95_ms,omitempty"`
    ShedVramUsedMiB     int64 `json:"shed_vram_used_mib,omitempty"` // unit: MiB (matches DCGM_FI_DEV_FB_USED)
    ShedArmSeconds      int   `json:"shed_arm_seconds,omitempty"`
    ShedRecoverSeconds  int   `json:"shed_recover_seconds,omitempty"`

    ShedArm     time.Duration `json:"-"` // computed from ShedArmSeconds
    ShedRecover time.Duration `json:"-"` // computed from ShedRecoverSeconds
}

func parseCircuitConfig(raw []byte) CircuitConfig {
    var cc CircuitConfig
    if len(raw) == 0 {
        return cc
    }
    if err := json.Unmarshal(raw, &cc); err != nil {
        return CircuitConfig{}
    }
    cc.Cooldown = time.Duration(cc.CooldownS) * time.Second
    cc.ShedArm = time.Duration(cc.ShedArmSeconds) * time.Second
    cc.ShedRecover = time.Duration(cc.ShedRecoverSeconds) * time.Second
    return cc
}
```

### Example 3: Migration 0016 — tenants shed limits

```sql
-- Source: pattern from gateway/db/migrations/0013_evolve_tenants_schedule_quota.sql
-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway;

ALTER TABLE tenants
  ADD COLUMN local_inflight_max_llm   INT NOT NULL DEFAULT 4,
  ADD COLUMN local_inflight_max_stt   INT NOT NULL DEFAULT 2,
  ADD COLUMN local_inflight_max_embed INT NOT NULL DEFAULT 8,
  ADD COLUMN priority_tier            TEXT NOT NULL DEFAULT 'A'
    CHECK (priority_tier IN ('S','A','B'));

-- Seed known-6 tenants per CONTEXT.md D-B2
UPDATE tenants SET priority_tier='S', local_inflight_max_stt=2
  WHERE slug='telefonia';
UPDATE tenants SET priority_tier='A', local_inflight_max_llm=4
  WHERE slug='converseai';
UPDATE tenants SET priority_tier='B', local_inflight_max_llm=1
  WHERE slug='campanhas';
UPDATE tenants SET priority_tier='A', local_inflight_max_llm=1
  WHERE slug='voice-api';
UPDATE tenants SET priority_tier='A', local_inflight_max_llm=2
  WHERE slug='chat-ifix';
UPDATE tenants SET priority_tier='A', local_inflight_max_llm=1
  WHERE slug='cobrancas';

-- Trigger NOTIFY tenants_changed so live loader refreshes (Fase 4 D-C4 pattern)
NOTIFY tenants_changed;

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway;
ALTER TABLE tenants
  DROP COLUMN local_inflight_max_llm,
  DROP COLUMN local_inflight_max_stt,
  DROP COLUMN local_inflight_max_embed,
  DROP COLUMN priority_tier;
-- +goose StatementEnd
```

### Example 4: Migration 0017 — upstreams shed thresholds seed

```sql
-- Source: pattern from gateway/db/migrations/0008_seed_upstreams.sql
-- +goose Up
-- +goose StatementBegin
SET search_path = ai_gateway;

-- Extend circuit_config JSONB for tier-0 upstreams only
-- Tier-1 upstreams (openrouter-chat, openai-whisper, openai-text-embedding-3-small) get no shed_* fields (noop)
UPDATE upstreams SET circuit_config = circuit_config || jsonb_build_object(
    'shed_inflight_max', 8,
    'shed_p95_ms', 2000,
    'shed_vram_used_mib', 21504,  -- 21 GB × 1024 (MiB unit per DCGM exporter)
    'shed_arm_seconds', 30,
    'shed_recover_seconds', 60
  )
  WHERE name = 'local-llm';

UPDATE upstreams SET circuit_config = circuit_config || jsonb_build_object(
    'shed_inflight_max', 4,
    'shed_p95_ms', 3000,        -- Whisper is slower by design
    'shed_vram_used_mib', 21504,
    'shed_arm_seconds', 30,
    'shed_recover_seconds', 60
  )
  WHERE name = 'local-stt';

UPDATE upstreams SET circuit_config = circuit_config || jsonb_build_object(
    'shed_inflight_max', 16,
    'shed_p95_ms', 500,
    'shed_vram_used_mib', 21504,
    'shed_arm_seconds', 30,
    'shed_recover_seconds', 60
  )
  WHERE name = 'local-embed';

-- Trigger NOTIFY upstreams_changed so live loader refreshes
NOTIFY upstreams_changed;

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
SET search_path = ai_gateway;
UPDATE upstreams SET circuit_config = circuit_config - 'shed_inflight_max' - 'shed_p95_ms' - 'shed_vram_used_mib' - 'shed_arm_seconds' - 'shed_recover_seconds'
  WHERE name IN ('local-llm', 'local-stt', 'local-embed');
-- +goose StatementEnd
```

### Example 5: New Prometheus collectors (naming convention)

```go
// Source: gateway/internal/obs/metrics.go — extend existing file with Phase 5 collectors
// Naming conventions [VERIFIED: prometheus.io/docs/practices/naming]:
//   - snake_case, lowercase
//   - single-word prefix "gateway_" (application namespace)
//   - base units in name (_mib, _ms, _seconds, _bytes)
//   - _total suffix for Counters, no _total for Gauges
//   - no collision with existing gateway_* prefix (checked against obs/metrics.go lines 14-241)

// Phase 5 — load shedding
var GatewayInflight = promauto.NewGaugeVec(prometheus.GaugeOpts{
    Name: "gateway_inflight",
    Help: "Current in-flight requests per upstream (global, sum across tenants).",
}, []string{"upstream"})

var GatewayInflightTenant = promauto.NewGaugeVec(prometheus.GaugeOpts{
    Name: "gateway_inflight_tenant",
    Help: "Current in-flight requests per (upstream, tenant). Cardinality: 3 upstreams × 6 tenants = 18 series.",
}, []string{"upstream", "tenant"})

var GatewayInflightTier1 = promauto.NewGaugeVec(prometheus.GaugeOpts{
    Name: "gateway_inflight_tier1",
    Help: "Current in-flight requests routed to tier-1 during shedding. Dashboard-only; not used in decisions.",
}, []string{"upstream"})

var GatewayShedState = promauto.NewGaugeVec(prometheus.GaugeOpts{
    Name: "gateway_shed_state",
    Help: "Current shedding FSM state per upstream. 0=off, 1=armed, 2=on, 3=recovering. Cardinality: 3 upstreams × 4 states = 12 series.",
}, []string{"upstream", "state"})

var GatewayShedDecisions = promauto.NewCounterVec(prometheus.CounterOpts{
    Name: "gateway_shed_decisions_total",
    Help: "Count of routing decisions made by the shedding middleware. reason=inflight|p95|vram|tenant_cap|sensitive_capped|skipped_peak_offhours|tier1_unavailable|passed.",
}, []string{"upstream", "reason"})

var GatewayShedTransitions = promauto.NewCounterVec(prometheus.CounterOpts{
    Name: "gateway_shed_transitions_total",
    Help: "Count of FSM transitions per upstream, labeled by from→to states.",
}, []string{"upstream", "from", "to"})

var GatewayShedMirrorFailures = promauto.NewCounter(prometheus.CounterOpts{
    Name: "gateway_shed_mirror_failures_total",
    Help: "Count of Redis HSET/PUBLISH failures mirroring shed state. FSMs keep operating in-process (D-C3).",
})

var GatewayShedBlockedSensitive = promauto.NewCounterVec(prometheus.CounterOpts{
    Name: "gateway_shed_blocked_sensitive_total",
    Help: "Count of sensitive tenants blocked with 503 due to saturation + LGPD policy.",
}, []string{"tenant"})

var GatewayShedThresholdsChanged = promauto.NewCounterVec(prometheus.CounterOpts{
    Name: "gateway_shed_thresholds_changed_total",
    Help: "Count of circuit_config JSONB hot-reloads that changed shed_* fields.",
}, []string{"upstream"})

var GatewayShedForceActive = promauto.NewGaugeVec(prometheus.GaugeOpts{
    Name: "gateway_shed_force_active",
    Help: "Operator override active via gw:shed:force:{upstream} Redis shadow key. 0=off, 1=active.",
}, []string{"upstream"})

var GatewayVramUsedMiB = promauto.NewGauge(prometheus.GaugeOpts{
    Name: "gateway_vram_used_mib",
    Help: "GPU framebuffer memory used (MiB), scraped from DCGM_FI_DEV_FB_USED on the pod's :9400/metrics endpoint.",
})

var GatewayP95RequestMs = promauto.NewGaugeVec(prometheus.GaugeOpts{
    Name: "gateway_p95_request_ms",
    Help: "Rolling p95 request duration per upstream, derived from the shed ring buffer (last ~200 samples).",
}, []string{"upstream"})

var GatewayDcgmScrapeFailures = promauto.NewCounterVec(prometheus.CounterOpts{
    Name: "gateway_dcgm_scrape_failures_total",
    Help: "Count of DCGM scrape failures. reason=http_error|status_<n>|parse_error|metric_missing|metric_not_gauge.",
}, []string{"reason"})
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| `sort.Slice` | `slices.Sort` (generics) | Go 1.21 (Aug 2023) | [CITED: github.com/golang/go/issues/47619] 2.5-4x faster. Fase 5 pode usar `slices.Sort` diretamente; módulo já está em Go 1.23. |
| `sync.Map` para counters | `map[K]*atomic.Int64` populate-once | Go 1.19 typed atomics | Typed atomic is faster + safer than sync.Map for known-at-boot key set. |
| `time.AfterFunc` per-FSM | Single ticker goroutine | — | Determinismo + menos timers registrados + serialização natural da avaliação. |
| Postgres LISTEN/NOTIFY manual | `pgxlisten` lib (Fase 3 adoption) | 2024 | Fase 3 já migrated; Fase 5 reuses. |
| Redis Pub/Sub only | Redis Pub/Sub + Hash mirror | Fase 3 pattern | Fase 5 replicates. Streams NÃO é upgrade para este caso — lossy é aceitável. |

**Deprecated/outdated:**
- GPU utilization % como único sinal de saturação — deprecated (Pitfall 2 research). Fase 5 usa 2-of-3 com `DCGM_FI_DEV_FB_USED` (VRAM), inflight, P95.
- `sort.Slice` preferido sobre `slices.Sort` — deprecated. Use `slices.Sort` quando tipado; `sort.Float64s` ainda válido e idiomático.
- Histogram-based percentiles de Prometheus para latency SLO — OK para dashboards, inadequado para real-time FSM input.

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | 6 tenants conhecidos são: telefonia, converseai, campanhas, voice-api, chat-ifix, cobrancas | Migration 0016 seed | Se slugs reais diferem, seed UPDATE não afeta as linhas — defaults ficam. Plan deve confirmar slugs lendo tenants table no ambiente (data probe). `[ASSUMED]` derivado de STATE.md + CONTEXT.md context — não verificado contra live DB. |
| A2 | VPS GPU pod está na mesma rede privada que VPS gateway, permitindo egress HTTP 9400 sem autenticação | dcgmScraper config | Se firewall bloqueia, `DCGM_EXPORTER_URL` precisa ser over public IP + potencialmente auth bearer. CONTEXT.md assume same-private-network. `[ASSUMED]` |
| A3 | `audit_log.upstream` é TEXT (não ENUM) — Fase 2 migration check pendente | Migration 0018 conditional | Se ENUM, migration diferente. Plan Wave 0 task deve verificar. `[ASSUMED]` |
| A4 | Vegeta v12 lib pública em `github.com/tsenart/vegeta/lib` é a API canônica para load gen programático em Go tests | Integration tests SC-1/SC-2 | Lib aceita; se outra lib tiver melhor ergonomics (nenhuma mais popular detectada), trocar. `[VERIFIED: pkg.go.dev/github.com/tsenart/vegeta/lib]` não assumption. |
| A5 | `shed_vram_used_bytes` no CONTEXT.md D-A4 deve ser renomeado para `shed_vram_used_mib` — unit mismatch é real risk | Migration 0017 + parser | Se deixar "bytes" no schema mas interpretar como MiB no código, misleading para ops. Recomendação: renomear. Esta é recomendação, não assumption — fato (DCGM é MiB) é [VERIFIED]. |
| A6 | 200 samples com `sort.Float64s` no p95 completa em <50µs em hardware 4 vCPU VPS | Pattern 2 | Não medido neste hw. Benchmark Go 1.21 slices.Sort ~6% faster em 100 elem; extrapolação para 200 é fair. Se slow, `tdigest` reconsiderável. `[CITED: golang-codereviews message]` mas extrapolated. `[ASSUMED]` para 50µs specifically. |
| A7 | Fireworks 429 durante shed é raro o suficiente para não disparar breaker (é transient) e ainda assim CONTEXT.md D-D1 envelope é apropriado | Pitfall 7 | Se Fireworks 429 vira contínuo durante shed (e.g., spike arrest ceiling hit), breaker eventualmente trip por 5xx adjacentes. `[ASSUMED]` baseado em [VERIFIED: docs.fireworks.ai rate-limits] 6000 RPM ceiling — Ifix unlikely saturate. |
| A8 | Redis Ifix é Redis 7+ — Hash + Pub/Sub APIs consistentes | Mirror implementation | Fase 3 já usa; não é assumption nova, mas vale flag: se Redis < 5, Pub/Sub e some Hash ops differ. `[VERIFIED: CLAUDE.md "infra-redis-1"]` |

**If this table is empty:** N/A — há 5 assumptions + 3 verified-as-fact claims. Plan ou discuss-phase deve validar A1, A2, A3 antes de execute.

## Open Questions

1. **Exato slugs dos 6 tenants conhecidos no ambiente live**
   - What we know: STATE.md lista "ConverseAI, Telefonia, Campanhas, voice-api, Chat Ifix, Cobranças" como nomes human-readable.
   - What's unclear: se slugs de DB são exatamente `telefonia`, `converseai`, etc. ou variantes (`voice-api` vs `voice_api` vs `voiceapi`).
   - Recommendation: Plan Wave 0 task: `psql -c "SELECT slug FROM ai_gateway.tenants WHERE status='active';"` e ajustar migration 0016 seed UPDATE conforme. Alternativa: migration ignora seed e deixar operator rodar `gatewayctl tenant set-shed-limits` pós-deploy.

2. **Tipo da coluna `audit_log.upstream` (ENUM vs TEXT)**
   - What we know: Fase 2 D-B3 criou audit_log schema; CONTEXT.md D-D4 diz "migration 0018 se for ENUM".
   - What's unclear: qual dos dois.
   - Recommendation: Wave 0 task verificar: `psql -c "\d ai_gateway.audit_log"`. Se ENUM → migration `0018`; se TEXT sem CHECK → skip migration (doc-only). Se TEXT com CHECK → migration 0018 ajusta CHECK.

3. **DCGM_EXPORTER_URL endpoint configurado no ambiente**
   - What we know: Pod expõe :9400 (Phase 1). Gateway está na VPS dev (178.156.150.21). Pod é Vast.ai (IP privado resolvido pelo operator).
   - What's unclear: o IP/hostname exato a colocar em `DCGM_EXPORTER_URL` no Portainer stack.
   - Recommendation: Plan inclui Wave 0 env check + operator gate "confirmar DCGM_EXPORTER_URL no Portainer stack antes de deploy Fase 5".

4. **Cobertura do integration test SC-2 oscillation (120s duration)**
   - What we know: CONTEXT.md Claude's Discretion aceita 120s como exception to "tests <30s".
   - What's unclear: se CI aceita ou precisa ser opt-in tag (`go test -tags=integration_long`).
   - Recommendation: Plan decide tag strategy. Provável: `-tags=integration,slow` com CI rodando full só em nightly + pre-release.

5. **Threshold de `priority_tier` em Phase 5 (metadata only) vs v2 preemption**
   - What we know: CONTEXT.md D-B2 diz "metadata only em Phase 5", preemption deferred.
   - What's unclear: se `gatewayctl tenant set-shed-limits --tier X` deve permitir alterar tier mesmo que v1 não use — sim; metadata é lido por Fase 7 dashboard.
   - Recommendation: Plan explicita que CLI aceita --tier mesmo sendo metadata, para Fase 7 consumir.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Go toolchain | Build, tests | ✓ | 1.23.0 | — |
| Postgres 16 + `ai_gateway` schema | Migrations + sqlc queries | ✓ | 16 (shared DO cluster per STATE.md) | — |
| pgvector extension | (not needed for Phase 5) | ✓ | — | — |
| Redis 7 (`infra-redis-1`) | Mirror Hash + Pub/Sub + shadow keys | ✓ | 7 (CLAUDE.md confirma `infra-redis-1` container na rede traefik-public) | — |
| DCGM exporter no pod :9400 | dcgmScraper | ✓ | Phase 1 D-04 confirmed, pod/docker-compose.yml expõe | Fail-open se unreachable (CONTEXT.md D-A3) |
| `github.com/prometheus/common/expfmt` v0.62.0 | DCGM parser | ✓ | v0.62.0 indirect — promover para direct | — |
| `github.com/tsenart/vegeta/lib` | Integration load tests | ✗ | — | Pode usar goroutine loops manuais (menos ergonômico); adicionar no Wave 0 |
| Portainer stack `ai-gateway-dev` | Deploy | ✗ (pending Phase 2 SC-5) | — | Bloqueia LIVE UAT mas não execute Fase 5 (código + tests integration green) |
| GitHub Actions self-hosted runner para ifix-ai-gateway repo | CI | ✗ (only converseai-v4 + cobrancas-api + campanhas-chatifix) | — | Manual build local + push image; add runner quando Fase 5 promovido a main |
| DCGM_EXPORTER_URL configurado no Portainer stack | Runtime scraper | ✗ (Phase 5 adiciona env var nova) | — | Tests funcionam com httptest mock; prod precisa operator configurar |
| Vast.ai pod active para live UAT | SC-1/SC-2 empirical | ✗ (Phase 1 HUMAN-UAT pending) | — | Plan pode shippar sem; UAT postpones to "when pod live" — mesma pattern de Fases 2/3/4 LIVE UAT deferred |

**Missing dependencies with no fallback:**
- Nenhuma bloqueante para execute Fase 5. Todas as "✗" acima têm fallback (`httptest` para DCGM em test, manual vegeta install no Wave 0, LIVE UAT deferred pattern já estabelecido).

**Missing dependencies with fallback:**
- Vegeta lib: `go get github.com/tsenart/vegeta/lib` no Wave 0.
- Self-hosted runner ifix-ai-gateway: se needed para CI, adiciona com adaptado setup-runner-4.sh (CLAUDE.md template).
- Pod live: LIVE UAT deferred (Phase 5 VERIFICATION.md pode retornar `human_needed` para SC-1/SC-2 empirical — pattern Fases 2/3/4).

## Validation Architecture

### Test Framework

| Property | Value |
|----------|-------|
| Framework | Go stdlib `testing` + `testcontainers-go v0.34.0` + `miniredis/v2 v2.37.0` + `github.com/tsenart/vegeta/lib` (v12 — adicionar Wave 0) |
| Config file | `gateway/go.mod`, `gateway/internal/integration_test/*_test.go` (build tag `-tags=integration`) |
| Quick run command | `cd gateway && go test -race ./internal/shed/... ./internal/dcgm/... ./internal/redisx/... -count=1` (unit tests, ~5s) |
| Full suite command | `cd gateway && go test -race -tags=integration ./... -count=1 -timeout=300s` (Phase 5 adds ~120s for SC-2 oscillation; total ~4min) |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| LSH-01 | Inflight atomic counter per upstream + per (upstream, tenant) incrementa pre-dispatch, decrementa post-response | unit | `cd gateway && go test -race ./internal/shed/ -run TestInflightRegistry` | ❌ Wave 0 (new `gateway/internal/shed/set_test.go`) |
| LSH-01 | Prometheus gauge `gateway_inflight{upstream}` e `gateway_inflight_tenant{upstream,tenant}` expostos | unit | `cd gateway && go test ./internal/shed/ -run TestInflightMetrics` | ❌ Wave 0 |
| LSH-02 | Composite 2-of-3 signal — shed ativa quando ≥2 passam threshold | unit | `cd gateway && go test ./internal/shed/ -run TestFSMCompositeSignal` | ❌ Wave 0 (new `gateway/internal/shed/fsm_test.go`) |
| LSH-02 | Ring buffer P95 sobre ~200 samples | unit | `cd gateway && go test ./internal/shed/ -run TestLatencyRingP95` | ❌ Wave 0 |
| LSH-02 | DCGM scrape HTTP 5s + fail-open após 3 falhas | unit | `cd gateway && go test ./internal/dcgm/ -run TestScraperFailOpen` | ❌ Wave 0 (new `gateway/internal/dcgm/scraper_test.go`) |
| LSH-03 | FSM 4 estados com hysteresis 30s/60s — shed ativa em ≤30s, no flapping em 120s | integration | `cd gateway && go test -race -tags=integration ./internal/integration_test/ -run TestSC2_Hysteresis -timeout=180s` | ❌ Wave 3 (new `shed_hysteresis_integration_test.go`) |
| LSH-04 | Thresholds em `upstreams.circuit_config` JSONB, hot-reload via NOTIFY em <2s | integration | `cd gateway && go test -race -tags=integration ./internal/integration_test/ -run TestSC3_HotReload` | ❌ Wave 3 |
| LSH-05 | Overflow routing — tenant cap atingido + FSM=ON → tier-1; outras tenants permanecem tier-0 | integration | `cd gateway && go test -race -tags=integration ./internal/integration_test/ -run TestSC4_AntiStarvation` | ❌ Wave 3 |
| LSH-05 | SC-1: burst exceeds slot count → excesso para OpenRouter; abaixo volta local | integration | `cd gateway && go test -race -tags=integration ./internal/integration_test/ -run TestSC1_Burst` | ❌ Wave 3 |
| D-B3 | Sensitive tenant + saturação → 503 `upstream_saturated_for_sensitive_tenant` + Retry-After:5 | integration | `cd gateway && go test -race -tags=integration ./internal/integration_test/ -run TestSensitiveSaturated503` | ❌ Wave 3 |
| D-D1 | Tier-1 também indisponível → 503 `all_chat_upstreams_saturated` + Retry-After:30 | integration | `cd gateway && go test -race -tags=integration ./internal/integration_test/ -run TestTier1UnavailableDuringShed` | ❌ Wave 3 |
| D-D3 | Peak off-hours é noop explícito com métrica `skipped_peak_offhours` | integration | `cd gateway && go test -race -tags=integration ./internal/integration_test/ -run TestShedNoopOnPeakOffHours` | ❌ Wave 3 |
| D-C3 | Redis mirror publish/subscribe convergence cross-replica <1s | integration | `cd gateway && go test -race -tags=integration ./internal/integration_test/ -run TestShedMirrorConvergence` | ❌ Wave 3 |
| D-C5 | Operator override `gatewayctl shed-force off --ttl 300s` força FSM=OFF independente de sinais | integration | `cd gateway && go test -race -tags=integration ./internal/integration_test/ -run TestShedForceOverride` | ❌ Wave 3 |

### Sampling Rate

- **Per task commit:** `go test -race ./internal/shed/... ./internal/dcgm/... ./internal/redisx/... -count=1` (~5s unit only)
- **Per wave merge:** `cd gateway && go test -race -tags=integration ./... -count=1 -timeout=300s` (full suite, ~4min)
- **Phase gate:** Full suite green + SC-1..SC-4 integration + LIVE UAT deferred (same pattern Phases 2/3/4)

### Wave 0 Gaps

- [ ] `gateway/internal/shed/fsm_test.go` — cobre LSH-02 (composite signal) + LSH-03 (hysteresis)
- [ ] `gateway/internal/shed/set_test.go` — cobre LSH-01 (inflight registry) + set rebuild
- [ ] `gateway/internal/shed/middleware_test.go` — cobre LSH-05 decision paths
- [ ] `gateway/internal/dcgm/scraper_test.go` — cobre LSH-02 DCGM fail-open
- [ ] `gateway/internal/redisx/shed_test.go` — cobre D-C3 mirror
- [ ] `gateway/internal/integration_test/shed_sc1_burst_integration_test.go` — SC-1
- [ ] `gateway/internal/integration_test/shed_sc2_hysteresis_integration_test.go` — SC-2 (opt-in tag, ~120s)
- [ ] `gateway/internal/integration_test/shed_sc3_hotreload_integration_test.go` — SC-3
- [ ] `gateway/internal/integration_test/shed_sc4_antistarvation_integration_test.go` — SC-4
- [ ] `gateway/internal/integration_test/shed_edge_cases_integration_test.go` — D-B3, D-D1, D-D3, D-C3, D-C5
- [ ] Add `github.com/tsenart/vegeta/lib` to go.mod (Wave 0 deps step)
- [ ] Promote `github.com/prometheus/common` from indirect to direct in go.mod when first `expfmt` import added

**SC → Validation mapping:**

| SC | Validates | How |
|----|-----------|-----|
| SC-1 | Burst overflow → tier-1, below threshold → return local | TestSC1_Burst: vegeta 200 RPS por 30s com tenant_cap=4; assert ~4 concurrent em tier-0 + resto em tier-1 mock; assert que após carga cessar, tier-0 retorna em ≤60s (RECOVERING) |
| SC-2 | Hysteresis — shed ativa em ≤30s, no flapping em 60s oscilation | TestSC2_Hysteresis: simula signal oscilando ON/OFF em ciclos de 10s por 120s; assert transitions_total ≤ 4 (OFF→ARMED, ARMED→OFF, ARMED→ON after sustain, ON→RECOVERING→OFF after clean); zero intermediate flapping |
| SC-3 | Thresholds via Postgres UPDATE aplicam em <2s sem restart | TestSC3_HotReload: setup FSM em ON; UPDATE `circuit_config.shed_inflight_max` para valor alto via SQL direto; assert FSM re-avalia dentro de 2s e transita ON→RECOVERING (hysteresis preserved) |
| SC-4 | Anti-starvation — tenant A burst não starva tenant B | TestSC4_AntiStarvation: tenant A (cap=4) dispara 100 reqs, tenant B (cap=2) 10 reqs concurrent; assert todas de B são atendidas em tier-0; assert excess de A vai tier-1; assert médiana latency de B igual (±10%) à latência em carga leve |

### Chaos-drill scenarios

Plan Fase 5 deve incluir (mínimo 3):

1. **DCGM pod scrape falha durante carga** — mata processo dcgm-exporter no pod; gateway vê 3+ scrape failures; FSM 2-of-3 vira 1-of-2 (inflight + P95); shed ainda funciona por inflight/P95 isolado.
2. **Redis unavailable durante transição** — SHUTDOWN Redis mid-test; publishTransition falha silencioso; métrica `gateway_shed_mirror_failures_total` incrementa; FSM continua operar in-process.
3. **Cross-replica convergence gap** — 2 réplicas, mata subscribe goroutine em réplica B; publica evento; assert réplica B pega via reconcile periódico no boot ou polling (se adicionado Pitfall 3).
4. **Operator shed-force durante ON** — force OFF com TTL 10s durante FSM=ON; assert requests voltam tier-0 imediato; assert após TTL FSM re-avalia e volta ON (sinais ainda saturados).

### Database state validation

| Migration | Query validating state |
|-----------|-----------------------|
| 0016 | `SELECT slug, local_inflight_max_llm, priority_tier FROM ai_gateway.tenants WHERE slug IN ('telefonia','converseai','campanhas');` expects priority_tier IN ('S','A','B') |
| 0017 | `SELECT name, circuit_config->>'shed_inflight_max' FROM ai_gateway.upstreams WHERE name LIKE 'local-%';` expects values '8', '4', '16' |
| 0018 (conditional) | `SELECT pg_typeof(upstream) FROM ai_gateway.audit_log LIMIT 1;` or `\d ai_gateway.audit_log` |

## Security Domain

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | yes | Reuse Fase 2 `auth.Middleware` (API key + bcrypt). Shed middleware roda AFTER auth — tenant_id sempre resolvido antes de shed decision |
| V3 Session Management | no | Gateway é stateless; sem sessions. Idempotency key é separate concern (Fase 2/3) |
| V4 Access Control | yes | Per-tenant cap (LSH-05) é admin-config via `gatewayctl tenant set-shed-limits`, que deve ser authed via admin key (padrão Fase 4 D-D3). `gatewayctl shed-force` também admin-authed |
| V5 Input Validation | yes | Threshold JSONB via `gatewayctl thresholds set`: validate int ranges (inflight_max > 0, p95_ms > 0, vram_mib > 0, arm_seconds ≥ 5, recover_seconds ≥ 5). Migration 0016 CHECK constraint no priority_tier. Zod-equivalent em Go: sentinel errors + explicit range checks before UPDATE |
| V6 Cryptography | no | Nenhuma crypto nova em Fase 5. Reuse Fase 2 bcrypt para admin key, Fase 3 TLS para OpenRouter egress |
| V7 Error Handling | yes | 503 envelopes padronizados (OpenAI format). `Retry-After` header sempre presente em 503 shed. Nunca leak internal FSM state em error messages para cliente |
| V8 Data Protection | yes | `priority_tier` em tenants é metadata — NÃO leak tier de outros tenants em qualquer response ao cliente. `local_inflight_max_*` só visível via `gatewayctl tenant set-shed-limits` (admin) |
| V9 Communication | yes | Gateway → pod `:9400` é HTTP plain em private network (assumption A2). Se private network fails, precisa TLS bearer auth no DCGM_EXPORTER_URL |
| V13 API Security | yes | Rate limits, quotas (Fase 4) já enforced ANTES de shed middleware — shed é routing decision, não access control. Audit log append-only para todas decisões (Fase 2 D-B3) |

### Known Threat Patterns for Go Gateway + Redis + Postgres stack

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Tenant X sees tenant Y's inflight via metric endpoint `/metrics` | Information Disclosure | Métrica `gateway_inflight_tenant{upstream,tenant=UUID}` expõe tenant UUID. `/metrics` endpoint já é admin-authed (Fase 4 D-D3) — não exposed publicamente. Verificar que `/metrics` NÃO está mountado no `/v1/*` group (atual main.go:586 mostra mounted `r.Handle("/metrics", obs.Handler())` SEM auth). Fase 5 deve adicionar admin middleware OU mover para `/admin/metrics`. |
| Operator typo em `shed-force --ttl 300000s` (3.5 dias) bloqueia shedding prod | Denial of Service (self) | `gatewayctl shed-force` max TTL validation (e.g., 3600s). Métrica `gateway_shed_force_active` + alert if active > 900s (Fase 7 OBS-04). |
| DCGM endpoint retorna valor impossível (e.g., negativo, enorme) | Tampering/Information Disclosure | Parser valida: `if val < 0 || val > 1_000_000 { return fail("sanity_check") }`. Prevent memory overflow + prevent signal poisoning |
| Redis Pub/Sub message forjada por attacker com access ao Redis → fake FSM state | Tampering | Redis Ifix auth via password (verificar CLAUDE.md: "sem senha no host" — risk). Considerar adicionar Redis AUTH em Fase 5 ou accept que internal network is trusted boundary |
| Postgres NOTIFY flooding — attacker dispara 10k NOTIFY/s | Denial of Service | pgxlisten serializa handlers; Refresh loader rate-limited implicitly pelo DB query latency. Worst case: loader reload storm → `gateway_upstreams_reload_total{result=ok}` counter spike, alert Fase 7. |
| sqlc codegen missing field em new columns → runtime null panic | Info Disclosure (stack trace) | Plan Wave 0: rodar `sqlc generate` APÓS migration 0016/0017; verificar `TenantConfig` + `CircuitConfig` structs compilam com new fields. Recoverer middleware (httpx.Recoverer) já captura panic e serve 500 genérico. |
| `gw:shed:force:{upstream}` key set by non-admin | Spoofing | Redis in trusted network (CLAUDE.md). Se Redis ACL disponível no Ifix setup, considerar ACL user para gateway com CONFIG+SET+HSET+PUB permissions only (hardening tracked como deferred). |

## Sources

### Primary (HIGH confidence)

- **go.mod** (verified against repo at 2026-04-23) — confirms versions: gobreaker v2.4.0, prometheus/client_golang v1.20.5, prometheus/common v0.62.0 (indirect), redis/go-redis v9.18.0, jackc/pgxlisten v0.0.0-20250802141604, pgx v5.7.1, testcontainers v0.34.0, miniredis v2.37.0, sentry-go v0.29.1, go 1.23.0
- **gateway/internal/breaker/breaker.go** + **mirror.go** + **subscribe.go** — canonical pattern for Phase 5's shed package replication
- **gateway/internal/upstreams/types.go**, **loader.go**, **listen.go** — `CircuitConfig` struct + atomic.Pointer snapshot + pgxlisten wiring (all extendable)
- **gateway/internal/tenants/loader.go** — `TenantConfig` struct (to extend with 4 new fields)
- **gateway/internal/proxy/dispatcher.go** — precedence pattern (breaker check first) to extend
- **gateway/internal/auditctx/override.go** — `WithUpstreamOverride` + `WithBillingUpstream` pattern; Phase 5 adds `WithShedDecision`
- **gateway/internal/obs/metrics.go** — promauto pattern; lists existing 20+ gateway_* names to avoid collision
- **gateway/cmd/gateway/main.go:571-690** — chi middleware chain order (Phase 5 inserts shed between schedule and proxy)
- **pkg.go.dev/github.com/prometheus/common/expfmt** — `NewTextParser(model.UTF8Validation)` signature + `TextToMetricFamilies(io.Reader)` API [VERIFIED 2026-04-23]
- **pkg.go.dev/github.com/jackc/pgxlisten** — Listener.Handle multi-channel safe, HandleNotification synchronously called [VERIFIED 2026-04-23]
- **pkg.go.dev/github.com/sony/gobreaker** — Settings struct + ReadyToTrip Counts-based API (confirmed not suitable for 4-state FSM)
- **github.com/NVIDIA/dcgm-exporter/etc/dcp-metrics-included.csv** — `DCGM_FI_DEV_FB_USED: "Framebuffer memory used (in MiB)"` [VERIFIED 2026-04-23]
- **prometheus.io/docs/practices/naming** — metric naming conventions (snake_case, _total for counters, base units) [VERIFIED]
- **.planning/research/FEATURES.md §Load-Shedding Deep Dive** — Pattern 1-3 + Recommended Algorithm
- **.planning/research/PITFALLS.md §Pitfall 2, 3, 7, 9, 13** — justifications for D-A1 2-of-3, D-D2 streaming, D-D1 no-fallback-of-fallback, D-B1 per-tenant caps, ~60 series cardinality budget
- **.planning/phases/03-resilience-fallback-chain/03-CONTEXT.md** (cross-referenced) — Fase 3 patterns inherited

### Secondary (MEDIUM confidence)

- **docs.fireworks.ai/guides/quotas_usage/rate-limits** — Fireworks rate limits 6000 RPM spike ceiling; 429 headers (x-ratelimit-*) não Retry-After
- **redis.io/docs/latest/develop/pubsub** — Pub/Sub at-most-once semantics
- **github.com/tsenart/vegeta** (GitHub README + pkg.go.dev) — Rate{Freq, Per} + NewStaticTargeter + Attacker.Attack API
- **groups.google.com/g/golang-codereviews "[go] sort: improve average quicksort performance"** — Go 1.21 slices.Sort benchmarks 6-14% faster
- **github.com/golang/go/issues/47619** — slices package generic Sort introduction
- **develop.sentry.dev/sdk/data-model/event-payloads/breadcrumbs** — breadcrumb ring buffer semantics

### Tertiary (LOW confidence)

- **50µs estimate for sort 200 float64 on 4 vCPU VPS** — extrapolation from 100-element benchmarks; not measured on target hardware (A6 flagged)
- **"infra-redis-1 sem senha no host" security assumption** — CLAUDE.md states but ACL audit not verified for Phase 5

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — all versions verified against `go.mod`; APIs verified via pkg.go.dev in session
- Architecture: HIGH — pattern mirrors Fase 3 breaker/ (production-proven); only net-new concept is hand-rolled 4-state FSM (straightforward atomic ops)
- Pitfalls: HIGH — MiB unit mismatch is real and verifiable; pgxlisten + Redis Pub/Sub behaviors are documented; other pitfalls derive from research bundle cross-refs
- Code examples: MEDIUM — adapted from existing Fase 3 code and idiomatic Go; not compile-tested in this session (plan phase should compile-check Wave 0)
- Validation arch: HIGH — commands + structure pattern Fase 2/3/4 established

**Research date:** 2026-04-23
**Valid until:** 2026-05-23 (30 days for stable Go ecosystem; dcgm-exporter + Fireworks rate limits may evolve faster — re-check if any Wave 0 issue surfaces)
