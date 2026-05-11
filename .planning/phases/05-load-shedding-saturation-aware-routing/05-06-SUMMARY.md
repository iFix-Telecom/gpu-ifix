---
phase: 05-load-shedding-saturation-aware-routing
plan: 06
subsystem: gateway/load-shedding
tags: [go, middleware, dispatcher, wiring, chi-router, phase-5]
dependency_graph:
  requires:
    - "Plan 05-01 (config + auditctx scaffolding)"
    - "Plan 05-02 (DB migrations 0016/0017/0018 + tenant shed-limit columns)"
    - "Plan 05-03 (shed.FSM + shed.Set + LatencyRing + InflightRegistry)"
    - "Plan 05-04 (dcgm.Scraper + redisx ShedEvent helpers)"
    - "Plan 05-05 (shed.MakePublishTransition + Subscribe + ReconcileLoop + RunTicker)"
  provides:
    - "shed.Middleware (chi HTTP middleware mounted between schedule and dispatcher)"
    - "shed.MiddlewareDeps + UpstreamResolver + TenantLookup interfaces"
    - "proxy/dispatcher.go tier-1 unavailable branch (D-D1)"
    - "gateway main.go Phase 5 wiring (4 goroutines + middleware mount)"
  affects:
    - "Phase 5 Plan 07 (gatewayctl shed-state + shed-force consume same Redis keys)"
    - "Phase 5 Plan 08 (integration tests validate full chain end-to-end)"
tech_stack:
  added: []
  patterns:
    - "Small test-seam interfaces (UpstreamResolver, TenantLookup) so middleware tests don't need a live Postgres pool"
    - "Defer-based atomic Inc/Dec pairing with latency-ring write inside trackAndPass — same shape as Phase 4 rate-limit middleware"
    - "Pre-Subscribe HydrateFromRedis to close the lossy-Pub/Sub convergence gap on boot (RESEARCH Pitfall 3 mitigation)"
key_files:
  created:
    - gateway/internal/shed/middleware.go
    - gateway/internal/shed/middleware_test.go
  modified:
    - gateway/internal/proxy/dispatcher.go
    - gateway/cmd/gateway/main.go
decisions:
  - "Use small package-level interfaces (UpstreamResolver, TenantLookup) instead of concrete *upstreams.Loader/*tenants.Loader on MiddlewareDeps so unit tests can drive every branch with in-memory fakes."
  - "Defer Task 6.4 (Run/NewRouter/TestHooks extraction) to Plan 08 — outside the orchestrator envelope for this plan."
  - "Inline the TenantLabel closure in main.go RunTicker wiring (avoids adding SlugByID to tenants.Loader public API; on miss the closure emits the empty slug so Prometheus drops the high-cardinality sample)."
  - "tenantsLoader construction moved BEFORE the Phase 5 shed wiring block in main.go so the RunTicker TenantLabel closure can call tenantsLoader.Get without a forward reference."
  - "Dispatcher D-D1 envelope chosen over schedule's off_hours_upstream_unavailable when shed_decision=shed_saturated AND tier-1 breaker is OPEN — the audit row reads shed_tier1_unavailable rather than the openrouter-chat upstream name."
metrics:
  duration_minutes: 21
  completed: 2026-05-11T22:22:00Z
  tasks_completed: 3
  tasks_deferred: 1
  files_created: 2
  files_modified: 2
  commits: 4
---

# Phase 5 Plan 06: Shed Middleware + Dispatcher + Main Wiring Summary

Wires Plans 02-05 of Phase 5 into the gateway runtime: implements the chi
HTTP middleware that translates FSM state + per-tenant cap into a routing
decision (D-B4 decision tree), extends the dispatcher with the
all_chat_upstreams_saturated 503 branch (D-D1), and adds the Phase 5
wiring block to main.go (4 goroutines + chain mount between schedule and
proxy).

## What Was Built

### A) `gateway/internal/shed/middleware.go` (NEW, 296 lines)

Chi HTTP middleware implementing the D-B4 decision tree exactly. Every
decision emits one `GatewayShedDecisions{upstream, reason}` counter
sample.

Decision tree (10 branches):

| Branch | Condition | Outcome | Reason label |
| ------ | --------- | ------- | ------------ |
| 01 | auth missing | next (defensive fallthrough) | — |
| 02 | tenant_id malformed UUID | next (defensive fallthrough) | — |
| 03 | tenant snapshot missing | next (loader stale) | — |
| 04 | schedule already overrode (peak off-hours) | stamp shed_decision + next | `skipped_peak_offhours` |
| 05 | route not classified (/admin, /health) | next without stamp | — |
| 06 | tier-0 upstream missing | next (dispatcher handles 503) | — |
| 07 | FSM != ON (Off/Armed/Recovering) | trackAndPass (Inc + defer Dec + p95 record) | `passed` |
| 08 | FSM=ON + inflight < cap | trackAndPass | `passed` |
| 09 | FSM=ON + inflight ≥ cap + normal + tier-1 available | divert to tier-1 + stamp shed_saturated | `tenant_cap` |
| 10a | FSM=ON + inflight ≥ cap + sensitive | **503 upstream_saturated_for_sensitive_tenant + Retry-After: 5 (D-B3)** | `sensitive_capped` |
| 10b | FSM=ON + inflight ≥ cap + normal + no tier-1 | **503 all_chat_upstreams_saturated + Retry-After: 30 (D-D1)** | `tier1_unavailable` |

**Two small package-level interfaces** introduced for test-seam:
- `UpstreamResolver` — `Resolve(role, tier) (UpstreamConfig, bool)` (satisfied by *upstreams.Loader)
- `TenantLookup`    — `Get(uuid.UUID) (TenantConfig, error)` (satisfied by *tenants.Loader)

Both are verified at compile-time via `var _ TenantLookup = (*tenants.Loader)(nil)`
guards in the test file. No production code touches these interfaces;
they exist purely so unit tests can pass in-memory fakes.

**Helpers** (`defaultClassifyRoute`, `resolveCapForRole`, `defaultCapForRole`)
are exported lowercase package-internal so tests can drive them
directly without spinning up the middleware.

### B) `gateway/internal/shed/middleware_test.go` (NEW, 432 lines)

11 branch tests + 3 helper tests, all passing under `-race`. Uses
`fakeUpstreamLoader` and `fakeTenantLookup` for in-memory tenancy +
upstream resolution; the real Postgres/Redis path is exercised by
Plan 08 integration tests.

Test coverage per branch is documented in the source via the `Branch
01..10b` naming convention so future maintainers can trace any branch
back to its test.

### C) `gateway/internal/proxy/dispatcher.go` (MODIFY, +39 lines)

Adds 3 short-circuit paths in the existing override branch:
1. `UpstreamShedBlockedSensitiveValue` override → return (middleware
   already wrote 503; double-write would corrupt the wire frame).
2. `UpstreamShedTier1UnavailableValue` override → return (same).
3. `shed_decision=shed_saturated` + tier-1 breaker OPEN → emit
   `all_chat_upstreams_saturated` + `Retry-After: 30` instead of the
   schedule-derived `off_hours_upstream_unavailable`. Re-stamps the
   audit override to `shed_tier1_unavailable` so dashboards can
   distinguish breaker-driven vs shed-driven 503s.

Zero regression in existing proxy tests (10 s runtime, all green).

### D) `gateway/cmd/gateway/main.go` (MODIFY, +178/-10 lines)

Adds the Phase 5 wiring block (`====== Phase 5 — Load Shedding wiring ======`)
between the upstreams/breaker block and the prices/fx loaders:

1. `LatencyRing` per upstream + global `InflightRegistry`.
2. Optional `dcgm.Scraper` — only constructed when `DCGM_EXPORTER_URL`
   is non-empty; ReadMiB on a nil receiver returns `(0, true)` so the
   FSM treats VRAM as unknown and the 2-of-3 gate reduces to
   inflight + p95.
3. `shed.Set` with `OnChange` callback that captures the per-upstream
   signal snapshot and publishes via the `MakePublishTransition`
   helper on a fire-and-forget goroutine — never blocks the FSM tick.
4. `HydrateFromRedis` BEFORE Subscribe so a freshly booted replica
   observes the cluster-wide view before its first Pub/Sub event.
5. Three goroutines:
   - `shedSet.Subscribe(ctx, rdb)`  — cross-replica Pub/Sub consumer
   - `shedSet.ReconcileLoop(ctx, rdb, DefaultReconcileInterval, log)`
   - `shed.RunTicker(ctx, deps, log)` — 1Hz FSM ticker + per-tenant gauge fanout
6. `dcgmScraper.Run(ctx)` — optional, only when DCGM_EXPORTER_URL is set.
7. `shed.Middleware` mounted in `buildRouter` between `schedule.Middleware`
   and the per-role dispatchers.

**Chain order in `buildRouter` is now (D-D1 conformant):**
```
obs.Requests → auth → audit → rate-limit → quota → schedule → SHED → dispatcher
```

**Hot-reload pipeline** (NOTIFY upstreams_changed) extended to also
call `shedSet.Rebuild(loader.Names())` and top up `shedLatency` with
rings for newly-added upstreams — alongside the existing
`breakerSet.Rebuild` callback.

**Reordering:** `tenantsLoader` construction moved BEFORE the Phase 5
wiring block so the `RunTicker.TenantLabel` closure can call
`tenantsLoader.Get(id)` without a forward reference. Functional
behaviour unchanged — same Refresh + LISTEN/NOTIFY + invariant check.

## Verification

```bash
go build ./...                                                                # ok
go vet ./gateway/cmd/gateway/... ./gateway/internal/shed/...                  # ok
go test -race -count=1 -short -timeout=120s ./gateway/internal/shed/...       # ok 1.6s
go test -race -count=1 -short -timeout=120s ./gateway/internal/proxy/...      # ok 10s
go test -race -count=1 -short -timeout=120s ./gateway/cmd/gateway/...         # ok 1s
```

Grep verification per plan:
- `shed.NewSet`            in main.go: 2 ✓
- `shed.RunTicker`         in main.go: 2 ✓
- `dcgm.New`               in main.go: 1 ✓
- `shed.Middleware`        in main.go: 2 ✓ (import + mount)
- `shedSet.Rebuild`        in main.go: 2 ✓ (boot + hot-reload callback)
- `shedSet.Subscribe`      in main.go: 1 ✓
- `shedSet.ReconcileLoop`  in main.go: 1 ✓
- `HydrateFromRedis`       in main.go: 2 ✓ (comment + call)
- `ShedDecisionFromContext` in dispatcher.go: 1 ✓
- `all_chat_upstreams_saturated` in dispatcher.go: 3 ✓
- `Retry-After.*30`        in dispatcher.go: 4 ✓

## Commits

| Hash | Type | Description |
| ---- | ---- | ----------- |
| 49f8021 | test | RED — 10 branch tests + 3 helper tests for shed.Middleware decision tree |
| 18d54ec | feat | GREEN — implement shed.Middleware decision tree (296 lines) |
| 232827c | feat | dispatcher.go tier-1 unavailable branch + short-circuit for shed-written 503 (D-D1) |
| 7b79b1e | feat | main.go Phase 5 wiring — LatencyRing + InflightRegistry + DCGM scraper + shed.Set + 4 goroutines + middleware mount |

## Deviations from Plan

### Task 6.4 (Run / NewRouter / TestHooks extraction) — DEFERRED

**Reason:** Task 6.4 in the plan body asks to create a new file
`gateway/cmd/gateway/router.go` containing `RouterDeps + NewRouter` and
to refactor `func main()` into `func Run(ctx, cfg, hooks *TestHooks) error`.

The orchestrator's envelope for this plan explicitly limits scope to
four files (`shed/middleware.go`, `shed/middleware_test.go`,
`proxy/dispatcher.go`, `cmd/gateway/main.go`). Creating `router.go` is
outside that envelope.

The Plan PRD `files_modified` frontmatter also lists only `main.go` and
the three other files — internally consistent with the orchestrator's
envelope, but contradictory with the Task 6.4 body which describes the
new `router.go`. This contradiction was flagged in the plan as
"BLOCKER #4 — Plan 08 Task 8.1 placeholder" — but Plan 08 has not been
written yet, so resolving it here is premature.

**Forward path (for Plan 08 author):**
1. Either extract `Run` + `NewRouter` from `main.go` into `router.go`
   as part of Plan 08's setup work; OR
2. Use a subprocess-based gateway boot in `bootGateway` (less elegant
   but unblocks integration tests without restructuring `main.go`); OR
3. Accept a one-time scope expansion in Plan 08 to do the extraction
   alongside the integration test wiring.

No other deviations.

## Known Stubs

None. Every shed middleware branch and every wiring goroutine is fully
implemented with real Inc/Dec, real publish path, real FSM evaluation.

## Pending for Plan 07 + Plan 08

- **Plan 07 (gatewayctl extensions)** — consumes the Redis keys this
  plan writes (`gw:shed:{upstream}` Hash + `gw:shed:events` Pub/Sub +
  `gw:shed:force:{upstream}` operator override). The shed middleware
  also makes the per-tenant `local_inflight_max_*` columns observable
  via the `gateway_shed_decisions_total{reason=tenant_cap}` counter —
  `gatewayctl tenant set-shed-limits` will write back to those columns.
- **Plan 08 (integration tests + SC-1..SC-4)** — must validate the
  full chain end-to-end with testcontainers Postgres/Redis. SC-1
  exercises Branch 09 (burst with tenant cap → tier-1 divert), SC-2
  exercises the FSM hysteresis under sustained signal, SC-3 exercises
  the hot-reload pipeline (`circuit_config` UPDATE → FSM re-evaluates
  in <2s), SC-4 exercises Branch 09 across two tenants (one capped one
  not). Edge cases: D-B3 (Branch 10a) and D-D1 (Branch 10b + dispatcher
  shed_tier1_unavailable path).

## Self-Check: PASSED

- File `gateway/internal/shed/middleware.go` exists ✓
- File `gateway/internal/shed/middleware_test.go` exists ✓
- File `gateway/internal/proxy/dispatcher.go` modified ✓
- File `gateway/cmd/gateway/main.go` modified ✓
- Commit 49f8021 in log ✓
- Commit 18d54ec in log ✓
- Commit 232827c in log ✓
- Commit 7b79b1e in log ✓
- `go build ./...` clean ✓
- `go test -race ./gateway/internal/shed/... ./gateway/internal/proxy/... ./gateway/cmd/gateway/...` green ✓
- 11/11 shed.Middleware branch tests pass ✓
- Chain order verified: auth → audit → rate-limit → quota → schedule → SHED → dispatcher ✓
- Sensitive tenants under FSM=ON + cap get 503 not external routing ✓
- Tier-1 unavailable emits all_chat_upstreams_saturated + Retry-After:30 ✓
- 4 goroutines wired in main.go: dcgm.Scraper.Run, shed.Subscribe, shed.ReconcileLoop, shed.RunTicker ✓
- DCGM_EXPORTER_URL=="" fail-open path: scraper stays nil; ReadMiB returns (0, true) ✓
- auditctx.WithShedDecision stamped on every routing decision ✓
- Envelope respected — no files outside the 4 listed in the orchestrator prompt ✓
