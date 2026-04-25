---
phase: 05
slug: load-shedding-saturation-aware-routing
status: approved
nyquist_compliant: true
wave_0_complete: false
created: 2026-04-23
revised: 2026-04-23
---

# Phase 05 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | `go test` (Go 1.24) com `-race` para unit tests, build tags `integration` (testcontainers Postgres 16 + Redis 7 + miniredis) e `integration_slow` para SC-2 |
| **Config file** | `gateway/go.mod` (módulo raiz `github.com/ifixtelecom/gpu-ifix`); testcontainers config inline em `gateway/internal/integration_test/setup_test.go` (reusado das Phases 2-4) |
| **Quick run command** | `cd /home/pedro/projetos/pedro/gpu-ifix && go test -race -short ./gateway/internal/shed/... ./gateway/internal/dcgm/... ./gateway/internal/redisx/... -count=1 -timeout=60s` |
| **Full suite command** | `cd /home/pedro/projetos/pedro/gpu-ifix && go test -race -count=1 -timeout=120s ./gateway/... && go test -tags=integration -count=1 -timeout=180s ./gateway/internal/integration_test/...` |
| **Slow suite (SC-2)** | `cd /home/pedro/projetos/pedro/gpu-ifix && go test -tags='integration integration_slow' -count=1 -timeout=300s ./gateway/internal/integration_test/...` |
| **Estimated runtime** | quick ~30s · full ~3-4 min · slow +2 min |

---

## Sampling Rate

- **After every task commit:** Run `go test -race ./gateway/internal/{shed,dcgm,redisx}/... -count=1 -timeout=60s` (quick suite)
- **After every plan wave:** Run full suite (`go test -race -count=1 ./gateway/...` + integration build check `go test -tags=integration -c -o /dev/null ./gateway/internal/integration_test/...`)
- **Before `/gsd-verify-work`:** Full suite verde + integration suite verde + `go vet ./...` + `go build ./...`
- **Max feedback latency:** 60 seconds (quick suite); 4 min (full)

---

## Per-Task Verification Map

> Threat IDs em formato `T-05-NN` referenciam o STRIDE register de cada plan. SC refs (SC-1..SC-4) são success criteria do CONTEXT.md mapeados em RESEARCH.md §Validation Architecture.

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 05-01-01 | 01 | 0 | LSH-01..05 | T-05-01 / T-05-02 | go.mod com expfmt direct + vegeta dep; sentinel errors `shed.ErrShedOn|ErrTenantCapExceeded|ErrSensitiveSaturated|ErrAllChatUpstreamsSaturated|ErrShedForceTTLExceeded|ErrShedConfigInvalid` + `dcgm.ErrDCGMScrapeFailed|ErrDCGMUnitMismatch` definidos | unit | `cd /home/pedro/projetos/pedro/gpu-ifix && go build ./... && go vet ./... && grep -E "^\s+github.com/prometheus/common" go.mod \| grep -v "// indirect" \| head -1 && grep -E "^\s+github.com/tsenart/vegeta" go.mod` | ❌ W0 | ⬜ pending |
| 05-01-02 | 01 | 0 | LSH-01..05 | T-05-01 | 5 env vars (`DCGMExporterURL|ShedLatencyRingSize|ShedTickIntervalMs|ShedDcgmScrapeIntervalMs|ShedDcgmTimeoutMs`); `auditctx.WithShedDecision|ShedDecisionFromContext` + 3 const; 14 collectors `Gateway{Inflight|Shed|Vram|P95|Dcgm}` registrados sem colisão | unit | `cd /home/pedro/projetos/pedro/gpu-ifix && go build ./... && go vet ./... && grep -c "^var Gateway\(Inflight\|Shed\|Vram\|P95\|Dcgm\)" gateway/internal/obs/metrics.go && grep -E "^const (UpstreamShed|UpstreamShedBlocked|UpstreamShedTier1)" gateway/internal/auditctx/override.go` | ❌ W0 | ⬜ pending |
| 05-01-03 | 01 | 0 | N/A (operator gates) | — | 05-WAVE0-GATES.md gravado com 3 respostas (audit_log.upstream type, slugs, DCGM URL) | manual gate | (checkpoint:human-action) — ver Manual-Only Verifications | ❌ W0 | ⬜ pending |
| 05-02-01 | 02 | 1 | LSH-02, LSH-04, LSH-05 | T-05-03 / T-05-04 | migrations 0016/0017/0018 aplicáveis em ordem; valor MiB (não bytes) em 0017; trigger NOTIFY expandido com 4 colunas novas; CHECK priority_tier ∈ {S,A,B}; 0018 conditional per Gate A; `goose up && goose down 1 && goose up` ciclo limpo | integration (DB) | `cd /home/pedro/projetos/pedro/gpu-ifix && ls gateway/db/migrations/0016_*.sql gateway/db/migrations/0017_*.sql gateway/db/migrations/0018_*.sql && grep -c "shed_vram_used_mib.*21504" gateway/db/migrations/0017_evolve_upstreams_shed_thresholds.sql && grep -c "priority_tier.*CHECK.*IN.*'S','A','B'" gateway/db/migrations/0016_evolve_tenants_shedding_limits.sql && grep -c "local_inflight_max_llm IS DISTINCT FROM" gateway/db/migrations/0016_evolve_tenants_shedding_limits.sql && cd gateway && goose -dir db/migrations postgres "$GATEWAY_DB_URL" up && goose -dir db/migrations postgres "$GATEWAY_DB_URL" down 1 && goose -dir db/migrations postgres "$GATEWAY_DB_URL" up` | ❌ W0 | ⬜ pending |
| 05-02-02 | 02 | 1 | LSH-02, LSH-04 | T-05-03 | sqlc gen produz `UpdateTenantShedLimitsParams` + `LocalInflightMaxLlm/Stt/Embed` + `PriorityTier` em rows; `ListTenantsForLoader` SELECT estendido; build OK | unit | `cd /home/pedro/projetos/pedro/gpu-ifix && grep -l "UpdateTenantShedLimits" gateway/internal/db/gen/*.go && grep -l "LocalInflightMaxLlm" gateway/internal/db/gen/*.go && go build ./gateway/internal/db/gen/...` | ❌ W0 | ⬜ pending |
| 05-02-03 | 02 | 1 | LSH-02, LSH-04 | T-05-04 | `CircuitConfig` com 7 campos shed_* (3 int + 1 int64 + 2 int seconds + 2 derivados time.Duration); `parseCircuitConfig` deriva ShedArm/ShedRecover; `TenantConfig` com 4 campos novos; loader.Refresh popula | unit | `cd /home/pedro/projetos/pedro/gpu-ifix && go build ./... && go vet ./... && grep -E "ShedInflightMax\|ShedVramUsedMiB\|ShedArm\b\|ShedRecover\b" gateway/internal/upstreams/types.go \| wc -l && grep -E "LocalInflightMaxLLM\|PriorityTier" gateway/internal/tenants/config.go \| wc -l` | ❌ W0 | ⬜ pending |
| 05-03-01 | 03 | 1 | LSH-02, LSH-03 | T-05-05 | LatencyRing lockless (atomic.Int64 cursor + slice), P95 com sort O(n log n); InflightRegistry com per-tenant atomic.Int64; race-clean | unit (TDD) | `cd /home/pedro/projetos/pedro/gpu-ifix && go test -race ./gateway/internal/shed/... -run 'TestLatency\|TestInflight' -count=1 -timeout=30s` | ❌ W0 | ⬜ pending |
| 05-03-02 | 03 | 1 | LSH-02, LSH-03 | T-05-05 | FSM 4 estados (Off/Armed/On/Recovering) com transition table determinística; hysteresis arm=30s/recover=60s default; Set agregador thread-safe; tests cobrem todas transições | unit (TDD) | `cd /home/pedro/projetos/pedro/gpu-ifix && go test -race ./gateway/internal/shed/... -run 'TestFSM\|TestSet' -count=1 -timeout=30s -v 2>&1 \| tail -40` | ❌ W0 | ⬜ pending |
| 05-04-01 | 04 | 2 | LSH-02 | T-05-06 / T-05-07 | dcgm.Scraper usa expfmt (não regex); sanity check `0..1_000_000 MiB`; fail-open após 3 falhas; `ReadMiB() (int64, bool)` lockless; nil-safe receiver; ctx.Done() <1s; valida SC-2 (sinal VRAM) | unit (TDD) | `cd /home/pedro/projetos/pedro/gpu-ifix && go test -race ./gateway/internal/dcgm/... -count=1 -timeout=30s -v 2>&1 \| tail -30` | ❌ W0 | ⬜ pending |
| 05-05-01 | 05 | 2 | LSH-03, LSH-04 | T-05-08 | redisx/shed.go: 10 helpers (`WriteShedState|ReadShedState|PublishShedEvent|SubscribeShedEvents|WriteShedForce|GetShedForce|DeleteShedForce|AllShedStateKeys` + `ShedEvent`/`ShedEventSignals`); WriteShedForce valida state ∈ {off,on} e TTL ≤ 1h; round-trip miniredis verde | unit (miniredis) | `cd /home/pedro/projetos/pedro/gpu-ifix && go test -race ./gateway/internal/redisx/... -run 'TestWriteAndReadShed\|TestReadShedState_NotFound\|TestPublishSubscribeRoundTrip\|TestWriteShedForce\|TestGetShedForce\|TestDeleteShedForce\|TestAllShedStateKeys' -count=1 -timeout=30s` | ❌ W0 | ⬜ pending |
| 05-05-02 | 05 | 2 | LSH-03, LSH-04 | T-05-08 / T-05-09 | mirror.go publishTransition fail-open (counter GatewayShedMirrorFailures); subscribe.go reconnect-with-backoff; HydrateFromRedis no boot antes de Subscribe (Pitfall 3); malformed payload tolerado | unit (miniredis) | `cd /home/pedro/projetos/pedro/gpu-ifix && go test -race ./gateway/internal/shed/... -run 'TestSubscribe\|TestHydrate\|TestPublishTransition' -count=1 -timeout=30s` | ❌ W0 | ⬜ pending |
| 05-05-03 | 05 | 2 | LSH-03, LSH-04 | T-05-08 / T-05-09 | reconcile loop 30s default detecta divergência via HGETALL; tick.go RunTicker honra shed-force override antes de Evaluate; VramReader interface (nil-safe); GatewayP95RequestMs e GatewayInflight emitidos por tick (valida SC-2 hysteresis) | unit (miniredis) | `cd /home/pedro/projetos/pedro/gpu-ifix && go test -race ./gateway/internal/shed/... -run 'TestReconcile\|TestRunOneTick\|TestRunTicker' -count=1 -timeout=30s -v 2>&1 \| tail -40` | ❌ W0 | ⬜ pending |
| 05-06-01 | 06 | 3 | LSH-01, LSH-02, LSH-03, LSH-04, LSH-05 | T-05-10 / T-05-11 / T-05-12 | shed.Middleware com decision tree de 10 branches (auth missing → tenant parse fail → tenant loader err → schedule override → role unmapped → tier-0 absent → FSM=off passes → FSM=on under cap passes → sensitive 503 → normal cap routes to tier-1 / tier-1 unavailable 503); cobertura unit por branch + smoke; valida SC-1 (overflow) e SC-4 (anti-starvation tenant cap) | unit (TDD) | `cd /home/pedro/projetos/pedro/gpu-ifix && go test -race ./gateway/internal/shed/... -count=1 -timeout=30s 2>&1 \| tail -20 && go vet ./gateway/internal/shed/...` | ❌ W0 | ⬜ pending |
| 05-06-02 | 06 | 3 | LSH-01, LSH-05 | T-05-10 | dispatcher.go acrescenta branch tier-1 unavailable quando shed_decision=`shed_saturated` + breaker tier-1 OPEN → 503 `all_chat_upstreams_saturated` + Retry-After:30 (Pitfall 7 hardcoded) | unit | `cd /home/pedro/projetos/pedro/gpu-ifix && go build ./gateway/... && grep -c "all_chat_upstreams_saturated" gateway/internal/proxy/dispatcher.go && grep -c "ShedDecisionFromContext" gateway/internal/proxy/dispatcher.go && grep -c "Retry-After.*30" gateway/internal/proxy/dispatcher.go` | ❌ W0 | ⬜ pending |
| 05-06-03 | 06 | 3 | LSH-01..05 | T-05-10 / T-05-11 | main.go wiring: 14 LatencyRings, InflightRegistry, dcgm.Scraper opcional, shed.Set com publishTransition callback, 4 goroutines (scraper.Run + Subscribe + ReconcileLoop + RunTicker); HydrateFromRedis antes de Subscribe; shedSet.Rebuild no onReload; chain order auth→audit→idempotency→ratelimit→quota→schedule→shed→proxy; zero regressão Phases 2/3/4 | unit + integration | `cd /home/pedro/projetos/pedro/gpu-ifix && go build ./... && go test -race -count=1 -short ./gateway/internal/shed/... ./gateway/internal/dcgm/... ./gateway/internal/redisx/... ./gateway/internal/upstreams/... ./gateway/internal/proxy/... -timeout=60s 2>&1 \| tail -30` | ❌ W0 | ⬜ pending |
| 05-06-04 | 06 | 3 | LSH-01..05 | — | gateway expõe `func Run(ctx, cfg, hooks *TestHooks) error` + `NewRouter(...)` separados de main; TestHooks expõe `FSMSet/BreakerSet/DCGMScraperOverride` para integration_test; Option A confirmada para Plan 08 bootGateway | refactor + unit | `cd /home/pedro/projetos/pedro/gpu-ifix && go build ./... && grep -c "func Run(" gateway/cmd/gateway/main.go && grep -c "type TestHooks" gateway/cmd/gateway/main.go && grep -c "func NewRouter" gateway/cmd/gateway/main.go` | ❌ W0 | ⬜ pending |
| 05-07-01 | 07 | 3 | LSH-04, LSH-05 | T-05-13 | gatewayctl `shed-state` lê HGETALL gw:shed:{upstream}; `shed-force` SET com TTL ≤ 1h e state ∈ {off,on,clear} | unit (TDD) | `cd /home/pedro/projetos/pedro/gpu-ifix && go build ./gateway/cmd/gatewayctl/... && go test -race ./gateway/cmd/gatewayctl/... -run 'TestRunShed' -count=1 -timeout=30s` | ❌ W0 | ⬜ pending |
| 05-07-02 | 07 | 3 | LSH-02, LSH-04 | T-05-04 / T-05-13 | gatewayctl `thresholds set` valida ranges (vram 1024..49152 MiB, p95 100..30000 ms, inflight 1..1000); JSONB merge preserva campos existentes; UPDATE dispara NOTIFY upstreams_changed | unit + integration (DB) | `cd /home/pedro/projetos/pedro/gpu-ifix && go build ./gateway/cmd/gatewayctl/... && go test -race ./gateway/cmd/gatewayctl/... -run 'TestRunThresholds' -count=1 -timeout=60s` | ❌ W0 | ⬜ pending |
| 05-07-03 | 07 | 3 | LSH-02, LSH-04 | T-05-13 | gatewayctl `tenant set-shed-limits` chama `UpdateTenantShedLimits` sqlc com COALESCE; valida priority_tier ∈ {S,A,B} antes do UPDATE; CLI helpers integrados no main.go | unit + integration (DB) | `cd /home/pedro/projetos/pedro/gpu-ifix && go build ./gateway/cmd/gatewayctl/... && go test -race ./gateway/cmd/gatewayctl/... -run 'TestRunTenantSetShedLimits\|TestRunShedForce\|TestRunShedState\|TestRunThresholds' -count=1 -timeout=30s 2>&1 \| tail -20 && grep -E "shed-state\|shed-force\|thresholds\|set-shed-limits" gateway/cmd/gatewayctl/main.go gateway/cmd/gatewayctl/tenant.go \| wc -l` | ❌ W0 | ⬜ pending |
| 05-08-01 | 08 | 4 | LSH-01..05 | T-05-15 | helpers_shed_test.go: `newShedStack` (Pool+Rdb+2 mocks), `ControlledMockServer` (atomic SetLatency/SetStatus/Hits), `bootGateway` Option A (calls `gateway.Run(ctx, cfg, hooks)` + retorna URL listener) — sem placeholders | integration (compile only) | `cd /home/pedro/projetos/pedro/gpu-ifix && go test -tags=integration -c -o /dev/null ./gateway/internal/integration_test/... 2>&1 \| tail -10` | ❌ W0 | ⬜ pending |
| 05-08-02 | 08 | 4 | LSH-01, LSH-02, LSH-04 | T-05-15 | SC-1 burst (50 RPS x 20s, tier-1 hits ≥ 50, success ≥ 0.95, FSM volta a Off em ≤ 15s test-scaled); SC-3 hot-reload UPDATE→ON-OFF transition em ≤ 2s; SC-4 anti-starvation tenant B success ≥ 0.95 + p99 ≤ 2s sob burst de A | integration (vegeta + testcontainers) | `cd /home/pedro/projetos/pedro/gpu-ifix && go test -tags=integration -count=1 -timeout=180s ./gateway/internal/integration_test/ -run 'TestSC1\|TestSC3\|TestSC4' 2>&1 \| tail -20` | ❌ W0 | ⬜ pending |
| 05-08-03 | 08 | 4 | LSH-01..05 | T-05-15 | SC-2 hysteresis sob oscilação 120s — total transitions ≤ 4 (no flapping) usando contagem real via Redis Subscribe `gw:shed:events`; edge cases: D-B3 sensitive 503 com warmup concreto que dirige FSM→ON antes de assertar; D-D1 tier-1 OPEN durante shed; D-D3 peak-off-hours noop; D-C5 shed-force; DCGM fail-open; mirror_convergence (Pub/Sub + HydrateFromRedis) | integration (build) + integration_slow (run) | `cd /home/pedro/projetos/pedro/gpu-ifix && go test -tags=integration -c -o /dev/null ./gateway/internal/integration_test/... 2>&1 \| tail -10 && go test -tags='integration integration_slow' -c -o /dev/null ./gateway/internal/integration_test/... 2>&1 \| tail -10` | ❌ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

**SC coverage map:**
- **SC-1** (burst overflow → tier-1): 05-06-01 (decision tree branch normal-capped) + 05-08-02 (SC-1 vegeta burst)
- **SC-2** (no flapping under oscillation): 05-04-01 (DCGM signal) + 05-05-03 (FSM ticker hysteresis) + 05-08-03 (SC-2 oscillation slow)
- **SC-3** (hot-reload <2s): 05-02-01 (NOTIFY trigger) + 05-08-02 (TestSC3_HotReload)
- **SC-4** (anti-starvation per-tenant cap): 05-02-03 (LocalInflightMax* fields) + 05-06-01 (cap enforcement) + 05-08-02 (TestSC4)

---

## Wave 0 Requirements

- [ ] `gateway/internal/integration_test/helpers_shed_test.go` — harness compartilhado (Plan 08 Task 8.1) — depende do refactor `gateway.Run/NewRouter/TestHooks` em Plan 06 Task 6.4
- [ ] `gateway/internal/shed/{latency,inflight,fsm,set,middleware,mirror,subscribe,reconcile,tick}_test.go` — TDD scaffolds criados em Plans 03/05/06 antes da implementação
- [ ] `gateway/internal/dcgm/scraper_test.go` — TDD scaffold criado em Plan 04 antes da implementação
- [ ] `gateway/internal/redisx/shed_test.go` — TDD scaffold criado em Plan 05 antes da implementação
- [ ] vegeta dependency adicionada em `go.mod` (Plan 01 Task 1.1) — bloqueador para Plan 08
- [ ] `05-WAVE0-GATES.md` confirmado por operador (Plan 01 Task 1.3) com 3 respostas (audit_log type, slugs, DCGM URL)

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Operator confirma DCGM_EXPORTER_URL acessível do gateway na VPS dev (curl pod:9400/metrics) | LSH-02 | Requer SSH/Portainer + IP privado do pod ativo no Vast.ai (não disponível em CI) | Em 05-WAVE0-GATES.md responder Gate C (Plan 01 checkpoint) |
| Operator confirma `audit_log.upstream` column type (ENUM vs TEXT vs TEXT+CHECK) na DO Postgres dev | LSH-04 (audit) | Requer credenciais DB de prod + introspection schema-level | Em 05-WAVE0-GATES.md responder Gate A (Plan 01 checkpoint) |
| Operator confirma slugs reais dos 6 tenants ativos | LSH-04 (seed migrations) | Requer credenciais DB | Em 05-WAVE0-GATES.md responder Gate B (Plan 01 checkpoint) |
| LIVE UAT em ambiente Vast.ai pod (após deploy `dev` Portainer): dirigir burst real, observar Grafana shed dashboard, confirmar que FSM transita ON sob carga e volta a OFF após calmar — pattern Phase 2/3/4 deferred | SC-1, SC-2, SC-4 | Requer pod GPU ativo (Vast.ai) + observabilidade Grafana operacional + janela de manutenção. Padrão estabelecido em Phases 2/3/4 onde UAT LIVE foi diferida pós-deploy. | Após deploy via Portainer dev stack: (1) curl pod-direct para baseline; (2) vegeta 50 RPS x 30s contra `https://api-dev.converse-ai.app/v1/chat/completions` com tenant `converseai`; (3) `kubectl exec` ou `docker exec` no gateway container para `gatewayctl shed-state local-llm` e confirmar `state=on`; (4) observar Grafana panel `gateway_shed_decisions_total` por reason; (5) cessar carga e aguardar volta a `off` em ≤ 90s. Resultado registrado em `05-VERIFICATION.md` campo "human_needed". |
| LIVE UAT SC-3: UPDATE production-like circuit_config via `gatewayctl thresholds set` durante carga e observar mudança em ≤ 2s no dashboard | SC-3 | Requer Postgres dev + janela de operação | Após deploy: aplicar burst (item anterior); rodar `gatewayctl thresholds set local-llm --shed-inflight-max 1000` (efetivamente desliga); medir tempo até `gateway_shed_state` cair para 0 no Grafana. |
| LIVE UAT shed-force override (D-C5): aplicar `gatewayctl shed-force local-llm on --ttl 60s` e validar que FSM honra mesmo com sinais zerados | LSH-03, LSH-04 | Requer ambiente real | Comando direto no container; observar `gateway_shed_force_active{upstream="local-llm"}=1` no Grafana e tráfego desviado para tier-1. |

---

## Validation Sign-Off

- [x] All tasks have `<automated>` verify or Wave 0 dependencies (3 LIVE UAT tasks documented as Manual-Only following Phase 2/3/4 deferred pattern)
- [x] Sampling continuity: nenhuma sequência de 3 tasks sem automated verify
- [x] Wave 0 covers all MISSING references (TDD scaffolds nas Plans 03/04/05/06 antes da implementação; helpers_shed_test.go scaffold em Plan 08)
- [x] No watch-mode flags
- [x] Feedback latency < 60s (quick suite)
- [x] `nyquist_compliant: true` set in frontmatter

**Approval:** approved 2026-04-23 (post-revision iteration 1/3 — VALIDATION fully populated, all 21 task rows mapped, SC coverage explicit, LIVE UAT deferred per Phase 2/3/4 pattern)
