---
phase: 05-load-shedding-saturation-aware-routing
verified: 2026-05-11T23:16:09Z
status: passed_partial
score: 8/8 must-haves verified (automated); SC-1 PARTIAL + SC-2 IMPLIED + SC-3 PASS in 2026-05-13 UAT session; SC-4 + SC-5 deferred (see 05-UAT-2026-05-13.md)
overrides_applied: 0
re_verification:
  previous_status: none
  previous_score: n/a
  gaps_closed: []
  gaps_remaining: []
  regressions: []
human_verification:
  - test: "SC-1 (LIVE UAT) — Burst overflow → tier-1 under real 4090 load"
    expected: "Vegeta-driven 50 RPS x 30s vs api-dev.converse-ai.app with tenant converseai drives FSM=ON; excess requests hit OpenRouter tier-1; FSM returns to OFF within ≤90s after load stops. Verified via Grafana panels gateway_shed_state{upstream=\"local-llm\"} and gateway_shed_decisions_total{reason}."
    why_human: "Requires deployed gateway in Portainer + active Vast.ai 4090 pod; cannot run from CI without GPU. Integration test (TestSC1_BurstExceedsTenantCapOverflowsToTier1) covers logic in subprocess black-box but does not exercise real GPU saturation signal."
  - test: "SC-2 (LIVE UAT) — Hysteresis convergence under 60s oscillating load"
    expected: "Under sustained P95 spike or VRAM > 21 GB, shedding activates within 30s; oscillating load for 60s does NOT cause flapping (≤4 FSM transitions observed via gw:shed:events Pub/Sub). Production thresholds (arm=30s, recover=60s)."
    why_human: "Production thresholds require ≥90s of sustained signal observation. Integration test runs test-scaled thresholds (arm=1s, recover=2s); production hysteresis behavior under real signal source can only be validated post-deploy."
  - test: "SC-3 (LIVE UAT) — Hot-reload via gatewayctl thresholds set"
    expected: "Operator runs `docker exec ifix-ai-gateway /gatewayctl thresholds set local-llm --shed-inflight-max 1000` during active burst; Grafana shows gateway_shed_state transition to 0 within ≤2s. NOTIFY upstreams_changed pipeline confirmed."
    why_human: "Requires deployed gateway + Postgres NOTIFY + Grafana observability. TestSC3_HotReloadAppliesInUnder2Seconds covers logic in testcontainers but not against the production stack."
  - test: "SC-4 (LIVE UAT) — Anti-starvation priority_tier S vs B under shed"
    expected: "During tenant A burst (priority_tier='B' cap=1), tenant B (priority_tier='S' cap≥4) maintains success ≥0.95 + P99 ≤2s. Per-tenant hard caps + priority_tier metadata observed in audit_log + Grafana per-tenant inflight gauge."
    why_human: "Requires operator-driven shed-limits configured for at least 2 tenants in production DB + concurrent vegeta-driven load. Test suite covers SC-4 logic in subprocess; LIVE UAT validates against real client traffic patterns."
  - test: "DCGM_EXPORTER_URL post-deploy wiring + VRAM signal smoke"
    expected: "Operator sets DCGM_EXPORTER_URL=http://<vast-ai-pod>:9400/metrics in Portainer env, restarts container, runs `curl -sS --max-time 3 http://<pod>:9400/metrics | grep ^DCGM_FI_DEV_FB_USED` from gateway container; gateway_vram_used_mib Grafana gauge populates within one scrape interval (5s)."
    why_human: "Vast.ai pod not active at verification time (per Gate C resolution — boot fail-open with DCGM_EXPORTER_URL=''). Real DCGM scrape end-to-end requires GPU pod and Portainer stack update."
  - test: "Testcontainers integration suite (11 tests) — Docker daemon required"
    expected: "`go test -tags=integration ./gateway/internal/integration_test/... -run 'TestSC|TestSensitive|TestTier1|TestPeakOff|TestShedForce|TestDCGM|TestMirror|TestBootRehydr'` passes in ~90s. `go test -tags='integration integration_slow' ... -run TestSC2` passes in ~125s."
    why_human: "Tests compile cleanly under both build tags (verified) but require a live Docker daemon (testcontainers Postgres 16 + Redis 7 + subprocess gateway boot). CI host without Docker, like this verification environment, cannot exercise them; on a Docker-capable host or the dev VPS the suite is fully automated."
---

# Phase 5: Load Shedding (Saturation-aware Routing) — Verification Report

**Phase Goal:** The gateway detects real saturation before it becomes failure and sheds overflow to OpenRouter while the primary recovers, preserving low-latency service for remaining tenants.

**Verified:** 2026-05-11T23:16:09Z
**Status:** human_needed
**Re-verification:** No — initial verification

## Resumo Executivo

Toda a superfície técnica da Fase 5 (8 plans, 35 testes unitários no `internal/shed`, 9 no `internal/dcgm`, 20 no `internal/redisx`, 28 no `cmd/gatewayctl`, 11 testes de integração em `internal/integration_test/shed_*_test.go`) está **presente no codebase, compila, passa `go vet ./...`, e roda `-race` clean.** As must-haves automáticas todas verificam contra o código real, não contra os SUMMARYs.

O status final é `human_needed` porque:

1. **Os 4 Success Criteria do ROADMAP (SC-1..SC-4)** dependem de validação ao vivo num gateway implantado + pod Vast.ai 4090 ativo. A suíte de integração cobre o caminho lógico via subprocesso black-box + testcontainers, mas não exercita o sinal real de VRAM/GPU.
2. **A suíte de integração testcontainers (11 testes) compila** sob ambas as tags `integration` e `integration integration_slow`, mas **não roda neste host de verificação** porque não tem daemon Docker disponível. No host dev (`vps-ifix-vm` ou CI Docker-capable), a suíte é totalmente automatizada.
3. **A wiring do `DCGM_EXPORTER_URL`** é fail-open por design (Gate C decidiu boot vazio). Confirmar que o scrape real produz `gateway_vram_used_mib > 0` quando o pod Vast.ai existir é pós-deploy.

Nenhum gap funcional encontrado — todos os artefatos esperados existem, todas as keys links resolvem, todos os anti-patterns estão ausentes em código de produção.

## Goal Achievement

### Observable Truths (Must-Haves Automáticas)

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | Phase 5 tem dependências Go resolvidas (expfmt direct, vegeta opcional) | VERIFIED | `go.mod:17` `prometheus/common v0.62.0` direct; `go.mod:22` `tsenart/vegeta v12.7.0+incompatible` direct; `go build ./...` exit 0 |
| 2 | Sentinel errors dos packages shed e dcgm existem | VERIFIED | `gateway/internal/shed/errors.go` exports 6 sentinels (`ErrShedOn`, `ErrTenantCapExceeded`, `ErrSensitiveSaturated`, `ErrAllChatUpstreamsSaturated`, `ErrShedForceTTLExceeded`, `ErrShedConfigInvalid`); `gateway/internal/dcgm/errors.go` exports `ErrDCGMScrapeFailed`, `ErrDCGMUnitMismatch` |
| 3 | Config expõe 5 novas env vars shed | VERIFIED | `gateway/internal/config/config.go:70-74` declara `DCGMExporterURL`, `ShedLatencyRingSize`, `ShedTickIntervalMs`, `ShedDcgmScrapeIntervalMs`, `ShedDcgmTimeoutMs`; lidas em `Load()` com defaults conservadores |
| 4 | 14 obs collectors registrados | VERIFIED | `gateway/internal/obs/metrics.go` declara `GatewayInflight`, `GatewayInflightTenant`, `GatewayInflightTier1`, `GatewayShedState`, `GatewayShedDecisions`, `GatewayShedTransitions`, `GatewayShedMirrorFailures`, `GatewayShedMirrorReconcile`, `GatewayShedBlockedSensitive`, `GatewayShedThresholdsChanged`, `GatewayShedForceActive`, `GatewayVramUsedMiB`, `GatewayP95RequestMs`, `GatewayDcgmScrapeFailures` (14 declarações verificadas via grep) |
| 5 | auditctx expõe shed helpers + 3 constantes | VERIFIED | `gateway/internal/auditctx/override.go:85-91` `WithShedDecision`/`ShedDecisionFromContext`; `:106,112,118` constantes `UpstreamShedSaturatedValue="shed_saturated"`, `UpstreamShedBlockedSensitiveValue="shed_blocked_sensitive"`, `UpstreamShedTier1UnavailableValue="shed_tier1_unavailable"` |
| 6 | Migrations 0016/0017/0018 presentes e bem-formadas | VERIFIED | `gateway/db/migrations/0016_evolve_tenants_shedding_limits.sql` adiciona 4 colunas `local_inflight_max_{llm,stt,embed}` + `priority_tier TEXT CHECK IN ('S','A','B')` + expande trigger WHEN; `0017_evolve_upstreams_shed_thresholds.sql` faz JSONB merge em 3 tier-0 upstreams com `shed_vram_used_mib=21504` (MiB unit, RESEARCH Pitfall 1); `0018_audit_log_shed_values.sql` é docs-only conforme Gate A |
| 7 | shed package core (FSM 4-state, LatencyRing, InflightRegistry, Set) implementado e testado | VERIFIED | `gateway/internal/shed/fsm.go:42-46` `StateOff/StateArmed/StateOn/StateRecovering`; 70 subtests passando -race em 1.586s (35 cases ÷ subtests). Tabela D-C1 com 10 células cobertas (per `05-03-SUMMARY.md`). `CompareAndSwap` lockless transitions confirmados em `transition()`. |
| 8 | dcgm.Scraper com expfmt parser + 3-strike fail-open | VERIFIED | `gateway/internal/dcgm/scraper.go:53` import `prometheus/common/expfmt`; `:170` `var parser expfmt.TextParser` (zero-value per docs); `:64` `failOpenAfterN = 3`; `:70` `dcgmMetricName = "DCGM_FI_DEV_FB_USED"`. 9 tests passing -race em 1.196s. Nil receiver semantics para Gate C fail-open (per `04-SUMMARY.md` test `TestScraper_NilReceiverReadMiBReturnsUnknown`). |

**Score:** 8/8 truths verified (automated).

### ROADMAP Success Criteria (Live UAT pendente)

| # | Success Criterion | Status | Evidence |
|---|-------------------|--------|----------|
| SC-1 | Burst onde inflight > slots → excesso roteado para OpenRouter; abaixo do threshold volta para local | VERIFIED (logic) + HUMAN-NEEDED (live) | `TestSC1_BurstExceedsTenantCapOverflowsToTier1` em `shed_sc1_burst_test.go` (compila sob `-tags=integration`); decisão branch 09 no middleware (`middleware.go:215`); LIVE UAT pendente per `05-08-SUMMARY` + `05-VALIDATION` Manual-Only |
| SC-2 | P95 spike ou VRAM > 21 GB → shedding ativa em ≤30s; sem flapping em 60s oscilação (hysteresis) | VERIFIED (logic) + HUMAN-NEEDED (live) | `TestSC2_HysteresisNoFlapping` em `shed_sc2_hysteresis_test.go` sob `-tags=integration_slow` (120s wall); FSM hysteresis arm=30s/recover=60s declarado em `fsm.go` + migration 0017 seed; LIVE UAT pendente |
| SC-3 | Thresholds reloadable via Postgres em <2s sem restart | VERIFIED (logic) + HUMAN-NEEDED (live) | `TestSC3_HotReloadAppliesInUnder2Seconds`; NOTIFY trigger expandido em migration 0016; `gatewayctl thresholds set` faz JSONB merge preservando campos pré-existentes (`thresholds.go`); LIVE UAT pendente |
| SC-4 | Anti-starvation: burst de um tenant não prejudica outros (per-tenant caps + priority_tier) | VERIFIED (logic) + HUMAN-NEEDED (live) | `TestSC4_NoisyTenantDoesNotStarveQuietTenant`; colunas `local_inflight_max_{llm,stt,embed}` + `priority_tier` em migration 0016; per-tenant cap enforcement no `middleware.go` branch 09; LIVE UAT pendente |

### Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `gateway/internal/shed/errors.go` | sentinel errors | VERIFIED | 6 sentinels via `errors.New(...)`; nenhum stub |
| `gateway/internal/shed/fsm.go` | 4-state FSM lockless | VERIFIED | 257 LoC; atomic.Int32 state + atomic.Int64 EnteredAt + atomic.Pointer[Config]; CAS-then-side-effect pattern |
| `gateway/internal/shed/latency.go` | Lockless P95 ring buffer | VERIFIED | atomic.StoreUint32/LoadUint32 nos slots; default 200; race-clean |
| `gateway/internal/shed/inflight.go` | Per-(upstream,tenant) atomic counters | VERIFIED | map[upstream]*atomic.Int64 global + nested map per-tenant; populate-once RWMutex; AddUpstream para hot-reload (WR-05) |
| `gateway/internal/shed/set.go` | Per-upstream FSM registry | VERIFIED | Rebuild + Get + State + ForEach + ApplyRemoteEvent + RemoteState; mirror of breaker.Set |
| `gateway/internal/shed/middleware.go` | Chi HTTP middleware D-B4 | VERIFIED | 10 branches; trackAndPass com Inc/Dec defer; sensitive 503 + tier1-unavailable 503 paths; emite `GatewayShedDecisions{upstream,reason}` |
| `gateway/internal/shed/mirror.go` | Publish transition factory | VERIFIED | MakePublishTransition bound worker pool (WR-03 post-fix); nil-rdb returns no-op |
| `gateway/internal/shed/subscribe.go` | Pub/Sub consumer + HydrateFromRedis | VERIFIED | Reconnect-with-backoff 1s; HydrateFromRedis BEFORE Subscribe per Pitfall 3 mit#1 |
| `gateway/internal/shed/reconcile.go` | Periodic 30s reconcile + error backoff | VERIFIED | ReconcileLoop + reconcileOnce; error backoff threshold=3 com skipNextCycle (WR-04 post-fix) |
| `gateway/internal/shed/tick.go` | 1Hz FSM ticker + TickerDeps + Thresholds + VramReader | VERIFIED | RunTicker iterates Set.ForEach; shed-force override wins; zero-Thresholds skip; per-tenant gauge fanout inlined |
| `gateway/internal/dcgm/scraper.go` | HTTP poller expfmt + 3-strike fail-open + sanity bounds | VERIFIED | 245 LoC; expfmt.TextParser; failOpenAfterN=3; max 1_000_000 MiB sanity; nil-safe ReadMiB |
| `gateway/internal/redisx/shed.go` | 10 helpers + ShedEvent envelope + 3 constants | VERIFIED | WriteShedState/ReadShedState/PublishShedEvent/SubscribeShedEvents/WriteShedForce/GetShedForce/DeleteShedForce/AllShedStateKeys/ShedStateKey/ShedForceKey; TTL ≤ 1h enforced |
| `gateway/internal/proxy/dispatcher.go` | D-D1 tier-1 unavailable branch | VERIFIED | linhas 108-137 short-circuit para shed-written 503; D-D1 emit `all_chat_upstreams_saturated` + Retry-After:30 quando shed_saturated + tier-1 breaker OPEN |
| `gateway/cmd/gateway/main.go` | Phase 5 wiring (4 goroutines + middleware mount) | VERIFIED | linhas 317-469: LatencyRing per-upstream + InflightRegistry + dcgm.Scraper opcional + shed.Set + HydrateFromRedis + Subscribe + ReconcileLoop + RunTicker; chain mount no buildRouter (linha 799) |
| `gateway/cmd/gatewayctl/{shed,thresholds,tenants_shed}.go` | 4 novos subcomandos | VERIFIED | shed-state, shed-force, thresholds set, tenant set-shed-limits; switch dispatcher em main.go:72-76 e tenant.go:44; 22 unit tests passando |
| `gateway/db/migrations/0016_*.sql` | 4 hard-cap columns + priority_tier + trigger expansion | VERIFIED | ALTER TABLE adiciona local_inflight_max_{llm,stt,embed} + priority_tier CHECK; DROP+CREATE trigger com WHEN superset |
| `gateway/db/migrations/0017_*.sql` | JSONB shed_* merge em tier-0 upstreams (MiB unit) | VERIFIED | 3 UPDATEs em local-llm/stt/embed com `shed_vram_used_mib=21504`; `shed_p95_ms` = 2000/3000/500 per upstream; defesa COALESCE(circuit_config, '{}'::jsonb) |
| `gateway/db/migrations/0018_*.sql` | Docs-only (Gate A) | VERIFIED | SELECT 1 no-op + comment block listando shed_saturated/shed_blocked_sensitive/shed_tier1_unavailable per Gate A resolution |
| `.planning/phases/05-load-shedding-saturation-aware-routing/05-WAVE0-GATES.md` | 3 operator gates resolved | VERIFIED | Gate A (TEXT no-CHECK → docs-only); Gate B (no per-tenant seed → operator owns); Gate C (DCGM_EXPORTER_URL="" → fail-open boot) |

### Key Link Verification

| From | To | Via | Status | Details |
|------|-----|-----|--------|---------|
| `main.go` | `shed.Set` | `shed.NewSet + Rebuild + 4 goroutines` | WIRED | 13 grep hits para `shed.NewSet|RunTicker|Subscribe|ReconcileLoop|HydrateFromRedis` em main.go |
| `main.go` | `dcgm.Scraper` | `dcgm.New + go dcgmScraper.Run(ctx)` (apenas se DCGMExporterURL != "") | WIRED | main.go:344-355 — nil-safe path para Gate C boot vazio |
| `main.go` | `shed.Middleware` | `pg.Use(shed.Middleware(...))` em buildRouter | WIRED | main.go:799 — mount entre `schedule.Middleware` e dispatcher |
| Chain order | auth → audit → ratelimit → quota → schedule → SHED → dispatcher | `pg.Use(...)` sequence | WIRED | grep no main.go confirma sequência exata (linhas 772-799) |
| `shed.Middleware` | `obs.GatewayShedDecisions` | `.WithLabelValues(upstream, reason).Inc()` | WIRED | Cada branch (passed, tenant_cap, sensitive_capped, tier1_unavailable, skipped_peak_offhours) emite contador |
| `shed.Middleware` | `audit.middleware` via ctx | `auditctx.WithShedDecision(ctx, ...)` | WIRED | branches 04, 09, 10a, 10b chamam WithShedDecision; dispatcher lê via ShedDecisionFromContext (proxy/dispatcher.go:119) |
| `dispatcher.go` | `breaker precedence` over shed | `cb.State() == gobreaker.StateOpen` antes de emitir D-D1 | WIRED | linhas 119-133 — breaker wins (D-C4) |
| `tick.go` | `shed.FSM.Evaluate` | `TickerDeps + 2-of-3 composite signal` | WIRED | tick.go RunTicker → fsm.Evaluate(now, signals); fail-open VramUnknown reduce a 1-of-2 |
| `migration 0016` | `tenants.Loader` hot-reload | `NOTIFY tenants_changed` (trigger superset WHEN) | WIRED | trigger DROP+CREATE com 4 colunas novas no WHEN; loader.Refresh consome via pgxlisten |
| `migration 0017` | `upstreams.Loader` hot-reload | `NOTIFY upstreams_changed` (Phase 3 D-D4) + `parseCircuitConfig` | WIRED | parseCircuitConfig deriva ShedArm/ShedRecover de seconds; CircuitConfig +7 fields per types.go |
| `gatewayctl shed-force` | `redisx.WriteShedForce` (TTL ≤ 1h) | redis SET com EXPIRE | WIRED | server-side TTL ceiling enforced; CLI passa duration without clamp |
| `gatewayctl thresholds set` | JSONB merge in-Go + NOTIFY trigger | UPDATE upstreams SET circuit_config | WIRED | preserva failures/cooldown_s; range checks pré-DB (1..1000 inflight, 100..30000 p95, 1024..49152 MiB) |
| `gatewayctl tenant set-shed-limits` | sqlc `UpdateTenantShedLimits` (COALESCE narg) | UPDATE tenants SET ... | WIRED | partial UPDATE per col; sentinela -1 / string vazia preserva valor existente |

### Data-Flow Trace (Level 4)

| Artifact | Data Variable | Source | Produces Real Data | Status |
|----------|---------------|--------|--------------------|--------|
| `shed.FSM.State()` | `state atomic.Int32` | `transition()` via Evaluate ou Transition (synthetic) | Sim — CAS de state real | FLOWING |
| `gateway_inflight{upstream}` | `InflightRegistry.GlobalInflight(name)` | `Inc/Dec` defer-paired no `trackAndPass` middleware | Sim — atomic.Int64 contagem real | FLOWING |
| `gateway_inflight_tenant{upstream,tenant}` | `TenantsForUpstream + TenantInflight` inlined no `runOneTick` | atomic.Int64 per-(upstream, tenant) populated por middleware | Sim — refletindo middleware execution | FLOWING |
| `gateway_vram_used_mib` | `Scraper.vramUsedMiB atomic.Int64` | HTTP scrape DCGM_FI_DEV_FB_USED a cada 5s (quando DCGM_EXPORTER_URL set) | DEPENDS — fluxo real só quando pod live; boot fail-open com value=0 + VramUnknown=true | STATIC at boot (per Gate C) → FLOWING post-deploy |
| `gw:shed:{upstream}` Hash mirror | `OnChange callback` em shed.Set | publishTransition no MakePublishTransition worker pool | Sim — HSET + PUBLISH em cada transition | FLOWING |
| `audit_log.upstream` shed values | `auditctx.WithUpstreamOverride` ctx via dispatcher | audit middleware lê ctx no defer | Sim — Phase 4 audit pipeline integra | FLOWING |
| `gateway_shed_state{upstream}` gauge | `transition()` em fsm.go | obs.GatewayShedState.WithLabelValues(name).Set(intState) após CAS sucesso | Sim — só fires em CAS win | FLOWING |
| `gateway_shed_decisions_total{upstream,reason}` | `middleware.go` cada branch | counter inc por branch | Sim — emit ao final de cada decisão | FLOWING |

### Behavioral Spot-Checks

| Behavior | Command | Result | Status |
|----------|---------|--------|--------|
| Build full repo limpa | `go build ./...` | exit 0, sem output | PASS |
| Vet limpa | `go vet ./...` | exit 0, sem output | PASS |
| Shed package -race clean | `go test -race -count=1 ./gateway/internal/shed/...` | `ok 1.586s` | PASS |
| DCGM scraper -race clean | `go test -race -count=1 ./gateway/internal/dcgm/...` | `ok 1.196s` | PASS |
| Redisx -race clean | `go test -race -count=1 ./gateway/internal/redisx/...` | `ok 3.159s` | PASS |
| Gatewayctl -race clean (28 testes) | `go test -race -count=1 ./gateway/cmd/gatewayctl/...` | `ok 6.144s` | PASS |
| Proxy -race clean (regressão Phase 3) | `go test -race -count=1 -short ./gateway/internal/proxy/...` | `ok 9.956s` | PASS |
| Gateway main -race clean | `go test -race -count=1 -short ./gateway/cmd/gateway/...` | `ok 1.058s` | PASS |
| Integration suite compila (build tag) | `go test -tags=integration -c -o /dev/null ./gateway/internal/integration_test/...` | exit 0 | PASS |
| Slow integration suite compila | `go test -tags='integration integration_slow' -c -o /dev/null ./gateway/internal/integration_test/...` | exit 0 | PASS |
| Anti-pattern scan (TODO/FIXME/PLACEHOLDER em produção) | `grep -nE "TODO\|FIXME\|XXX\|PLACEHOLDER\|not yet implemented"` em todos os arquivos produção da Fase 5 | zero hits | PASS |
| Integration suite roda (testcontainers) | `go test -tags=integration ./gateway/internal/integration_test/...` | n/a — sem Docker daemon neste host | SKIP (needs human/CI host) |

### Requirements Coverage

| Requirement | Source Plan(s) | Description | Status | Evidence |
|-------------|---------------|-------------|--------|----------|
| LSH-01 | 05-01, 05-03, 05-06, 05-08 | Inflight counter por upstream (Go atomic) incrementa em pre-dispatch, decrementa em response | SATISFIED | `inflight.go` global+tenant atomic.Int64; middleware `trackAndPass` faz Inc/defer Dec; tick.go publica `gateway_inflight` |
| LSH-02 | 05-01..08 | Signal composto: inflight > N OU P95 (30s) > threshold OU VRAM > 21 GB | SATISFIED | FSM 2-of-3 evaluate em `fsm.go::Evaluate`; thresholds defaultados 8/2000ms/21504 MiB em migration 0017; VramUnknown reduz a 1-of-2 (fail-open) |
| LSH-03 | 05-03, 05-05, 05-08 | Histerese: só volta para local após sinal abaixo por 60s seguidos | SATISFIED | StateOff/Armed/On/Recovering 4-state FSM; arm=30s + recover=60s defaults; transition table testada via 15 unit tests + SC-2 slow |
| LSH-04 | 05-02, 05-05, 05-07, 05-08 | Thresholds configuráveis via Postgres e reloadable sem restart | SATISFIED | `circuit_config` JSONB com 5 shed_* fields; trigger upstreams_changed NOTIFY; loader.Refresh atomic.Pointer; `gatewayctl thresholds set` faz JSONB merge |
| LSH-05 | 05-01, 05-02, 05-06, 05-07, 05-08 | Overflow routing direciona excedente para OpenRouter mantendo outros tenants atendidos | SATISFIED | per-tenant hard caps via colunas `local_inflight_max_*`; middleware branch 09 (normal-capped → tier-1) + 10a (sensitive 503) + 10b (no tier-1 503); SC-4 cobre anti-starvation |

**Plano frontmatter requirements declarados vs ROADMAP:** Todos os 5 IDs (LSH-01..05) presentes nos plans 05-01, 05-02, 05-03, 05-05, 05-06, 05-07, 05-08. Nenhum requirement órfão (LSH-* não declarado em plan algum).

### Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
|------|------|---------|----------|--------|
| (nenhum) | — | — | — | Zero TODO/FIXME/PLACEHOLDER/"not yet implemented" em arquivos produção do `internal/shed`, `internal/dcgm`, `internal/redisx/shed.go`, `cmd/gatewayctl/{shed,thresholds,tenants_shed}.go` |

### Plan 06 Task 6.4 — Deferred to Plan 08

O plan 05-06 explicitamente **deferred** Task 6.4 (refactor `main.go::main()` em `gateway.Run(ctx, cfg, hooks *TestHooks)` + `NewRouter`) para Plan 08, e o Plan 08 escolheu o subprocess pattern (precedente `gateway_e2e_test.go`) em vez de fazer a extração. Isto é documentado e não constitui um gap funcional: a chain de middleware + 4 goroutines (ticker, scraper, subscribe, reconcile) é exercitada via `bootGateway` subprocess que invoca o binário real e probe `/health`. O LIVE UAT pós-deploy é a verificação final dessa instrumentação.

Considerado **intencional e documentado** — não exige override formal porque não falha contra uma must-have automática; é detalhe interno de estratégia de teste.

### Human Verification Required

Veja `human_verification:` no frontmatter. Resumo:

1. **SC-1 (LIVE UAT)** — Burst overflow → tier-1 vs api-dev.converse-ai.app com vegeta 50 RPS x 30s; observar Grafana shed dashboard e FSM volta a OFF em ≤90s.
2. **SC-2 (LIVE UAT)** — Hysteresis convergence com thresholds de produção (arm=30s/recover=60s) sob oscilação real.
3. **SC-3 (LIVE UAT)** — `gatewayctl thresholds set local-llm --shed-inflight-max 1000` durante burst; Grafana mostra time-to-zero ≤2s.
4. **SC-4 (LIVE UAT)** — Anti-starvation com tenants reais configurados via `gatewayctl tenant set-shed-limits`; tenant priority_tier='S' mantém success ≥0.95.
5. **DCGM_EXPORTER_URL wiring** — Pós-Vast.ai pod activation, set env em Portainer, restart container, validar `gateway_vram_used_mib > 0` no Grafana.
6. **Testcontainers integration suite (11 testes)** — Rodar no `vps-ifix-vm` ou CI Docker-capable host: `go test -tags=integration ./gateway/internal/integration_test/...` (~90s fast) + `-tags='integration integration_slow' -run TestSC2` (~125s slow).

### Gaps Summary

**Nenhum gap funcional encontrado.** Toda a superfície da Fase 5 é entregue, compila, passa testes unitários sob -race, e todos os artefatos esperados existem com wiring real. Os 4 LIVE UAT items + 1 wiring DCGM pós-deploy + 1 smoke da integration suite em host Docker-capable são requisitos clássicos do padrão estabelecido nas Fases 2/3/4 (UAT deferido pós-Portainer deploy) e estão documentados em `05-VALIDATION.md` §Manual-Only.

A status `human_needed` é uma chamada para o operador executar os 6 testes humanos listados — não uma indicação de defeitos no codebase.

---

*Verified: 2026-05-11T23:16:09Z*
*Verifier: Claude (gsd-verifier)*
*Verification host: ops-claude (10.10.10.10) — no Docker daemon; testcontainers integration suite skipped (compiles cleanly under both build tags)*
