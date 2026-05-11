---
phase: 05-load-shedding-saturation-aware-routing
plan: 05
subsystem: shed
tags: [go, redis, pubsub, fsm-ticker, mirror, reconcile, hydrate, vram, dcgm, prometheus]

# Dependency graph
requires:
  - phase: 05-load-shedding-saturation-aware-routing
    provides: "Plan 05-01 obs metrics (GatewayShedMirrorFailures, GatewayShedMirrorReconcile, GatewayShedForceActive, GatewayP95RequestMs, GatewayInflight, GatewayInflightTenant)"
  - phase: 05-load-shedding-saturation-aware-routing
    provides: "Plan 05-03 in-process FSM + Set + LatencyRing + InflightRegistry"
  - phase: 03 (breaker mirror pattern reference)
    provides: "breaker/subscribe.go (reconnect-with-backoff template) + redisx/breaker.go (HSet+Publish pattern)"
provides:
  - "redisx/shed.go (10 funcs + ShedEvent/ShedEventSignals + 3 const)"
  - "shed/mirror.go (MakePublishTransition factory + PublishTransitionFunc typedef)"
  - "shed/subscribe.go (Set.Subscribe goroutine + Set.HydrateFromRedis boot helper + parseState)"
  - "shed/reconcile.go (Set.ReconcileLoop + reconcileOnce; RESEARCH Pitfall 3 mitigation #2)"
  - "shed/tick.go (RunTicker goroutine + TickerDeps + Thresholds + VramReader interface)"
affects:
  - "05-06 (middleware + main.go wiring consumes RunTicker, Set.Subscribe, ReconcileLoop, HydrateFromRedis, MakePublishTransition)"
  - "05-07 (gatewayctl shed-force CLI consumes WriteShedForce/GetShedForce/DeleteShedForce)"
  - "Phase 6 (multi-replica leader-elected reconciler will consume gw:shed:events Pub/Sub contract)"
  - "Phase 7 (dashboard reads gw:shed:{name} Hash via AllShedStateKeys + ReadShedState)"

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Redis mirror pattern (in-process authoritative + HSET state Hash + PUBLISH events channel + periodic HGETALL reconcile)"
    - "Boot rehydration before subscribe (HGETALL gw:shed:{*} once, then start consumer)"
    - "Fail-open Redis: nil rdb returns no-op publisher; in-process FSM keeps operating; failures bump counter"
    - "Per-tenant Prometheus gauge fanout inlined in ticker (TenantsForUpstream + TenantInflight loop)"
    - "ctx.Done() shutdown contract: every goroutine (Subscribe, ReconcileLoop, RunTicker) exits within ~500ms"
    - "Reconnect-with-backoff Pub/Sub consumer (1:1 copy of breaker/subscribe.go shape)"

key-files:
  created:
    - "gateway/internal/redisx/shed.go (244 lines, 10 funcs + 3 const + 2 structs)"
    - "gateway/internal/redisx/shed_test.go (196 lines, 14 miniredis tests)"
    - "gateway/internal/shed/mirror.go (63 lines, MakePublishTransition factory)"
    - "gateway/internal/shed/subscribe.go (133 lines, Set.Subscribe + Set.HydrateFromRedis + parseState)"
    - "gateway/internal/shed/subscribe_test.go (181 lines, 10 tests)"
    - "gateway/internal/shed/reconcile.go (106 lines, Set.ReconcileLoop + reconcileOnce)"
    - "gateway/internal/shed/reconcile_test.go (84 lines, 5 tests)"
    - "gateway/internal/shed/tick.go (210 lines, RunTicker + TickerDeps + Thresholds + VramReader)"
    - "gateway/internal/shed/tick_test.go (170 lines, 6 tests)"
  modified: []

key-decisions:
  - "Inlined per-tenant gauge fanout in tick.go instead of calling InflightRegistry.ObserveMetrics (the planned API does not exist in Plan 05-03 inflight.go; this plan can only import from it, not modify). Used the public TenantsForUpstream + TenantInflight + TenantLabel functional triad — same semantic, no API surface added."
  - "AllShedStateKeys filters gw:shed:force:* via strings.HasPrefix instead of length+slice comparison (more idiomatic + safer against off-by-one)."
  - "parseState maps unknown/empty wire strings to StateOff (safe default — next valid event corrects remoteState; a publisher bug cannot poison the dashboard)."
  - "Operator-override 'malformed value' path: tick.go logs WARN + returns early for that upstream (avoids double-evaluating with the non-override signal path while the override key is corrupted)."

patterns-established:
  - "Pattern A — Mirror Goroutines: Subscribe (consumer) + ReconcileLoop (periodic HGETALL) + RunTicker (FSM evaluator) form a 3-goroutine trio coordinated only via Set (lockless atomic state). Each goroutine is independent and tolerates the others being down."
  - "Pattern B — Hydrate-then-Subscribe boot order: HydrateFromRedis seeds remoteState from the Hash mirror BEFORE Subscribe registers with Pub/Sub. Closes the 'new replica sees no remote state until next event' gap (RESEARCH Pitfall 3 mitigation #1)."
  - "Pattern C — Threshold zero-skip: when CircuitConfig returns Thresholds{0,0,0} for an upstream, the ticker silently skips evaluation. This makes tier-1 fallback upstreams and healthchecks inert without special-casing in the loop body."
  - "Pattern D — VramReader interface for DCGM isolation: tick.go does not import dcgm package; it consumes a 1-method interface (ReadMiB() (int64, bool)). Plan 05-04 dcgm.Scraper satisfies the interface naturally. Tests use fakeVram with no dcgm dep."

requirements-completed: [LSH-03, LSH-04]

# Metrics
duration: 8m30s
completed: 2026-05-11
---

# Phase 5 Plan 05: Mirror, Subscribe, Reconcile, Tick Summary

**Redis state mirror (HSET gw:shed:{name} + PUBLISH gw:shed:events) + boot HGETALL rehydration + periodic 30s reconcile loop + 1Hz FSM ticker coordinating LatencyRing/InflightRegistry/DCGM via 2-of-3 saturation gate.**

## Performance

- **Duration:** 8m30s
- **Started:** 2026-05-11T21:46:43Z
- **Completed:** 2026-05-11T21:55:13Z
- **Tasks:** 3 (each TDD: RED commit + GREEN commit)
- **Files created:** 9 (5 implementation + 4 test files)
- **Lines added:** 1387 (production + test)
- **Test count:** 14 (redisx) + 21 (shed) = 35 new tests, all passing under -race

## Accomplishments

1. **Redis mirror plumbing complete** — `redisx/shed.go` exports the full Phase 5 surface: 10 helpers + 3 constants + `ShedEvent`/`ShedEventSignals` JSON envelope. Wire format is decided and documented.
2. **Pub/Sub + boot rehydration** — `shed/Subscribe` consumes `gw:shed:events` with 1s reconnect backoff (1:1 with breaker pattern). `shed/HydrateFromRedis` seeds `remoteState` from Hash mirror before Subscribe registers, closing RESEARCH Pitfall 3 mitigation #1.
3. **Periodic reconcile loop** — `shed/ReconcileLoop` runs every 30s, HGETALLs every managed upstream, corrects `remoteState` on divergence, emits `gateway_shed_mirror_reconcile_total{result=ok|diverged|error}`. Forward-compat for Phase 6 multi-replica.
4. **FSM ticker** — `shed/RunTicker` at 1Hz (configurable via `SHED_TICK_INTERVAL_MS`) iterates `Set.ForEach`, derives `Signals{InflightOverMax, P95OverMax, VramOverMax, VramUnknown}` from `InflightRegistry`/`LatencyRing`/`VramReader`, calls `fsm.Evaluate`. Shed-force override (D-C5) wins ahead of signal evaluation. Per-tenant Prometheus gauge fanout inlined.
5. **All 35 tests pass under `-race`** in 1.6s (shed) + 3.2s (redisx). `go build ./...` and `go vet ./...` clean for the full repo.

## Task Commits

Each task was committed atomically as a TDD pair:

1. **Task 5.1 — redisx/shed.go + tests** — `25c5d7a` (test RED) → `e9e46ea` (feat GREEN)
2. **Task 5.2 — shed/mirror.go + shed/subscribe.go** — `e057924` (test RED) → `3c6b26a` (feat GREEN)
3. **Task 5.3 — shed/reconcile.go + shed/tick.go** — `acab741` (test RED) → `5ad85b9` (feat GREEN)

All commits made with `--no-verify` (parallel-executor protocol to avoid pre-commit hook contention).

## Files Created

- `gateway/internal/redisx/shed.go` (244 lines) — Mirror helpers: `WriteShedState`, `ReadShedState`, `PublishShedEvent`, `SubscribeShedEvents`, `WriteShedForce` (TTL≤1h enforced), `GetShedForce`, `DeleteShedForce`, `AllShedStateKeys` (SCAN-based), `ShedStateKey`, `ShedForceKey`. Constants `ShedEventsChannel` ("gw:shed:events"), prefix strings, 2s op timeout. Structs `ShedEvent` + `ShedEventSignals` for the wire envelope.
- `gateway/internal/redisx/shed_test.go` (196 lines) — 14 miniredis-backed round-trip tests covering write/read, publish/subscribe, force TTL ceiling + invalid state, force expiry, AllShedStateKeys force-prefix exclusion, key/const format guards, nil-client errors.
- `gateway/internal/shed/mirror.go` (63 lines) — `MakePublishTransition(rdb)` factory returns a `PublishTransitionFunc` closure that HSETs + PUBLISHes back-to-back. nil-rdb returns a no-op (D-C3 fail-open). Failures bump `GatewayShedMirrorFailures`.
- `gateway/internal/shed/subscribe.go` (133 lines) — `Set.Subscribe(ctx, rdb)` consumer goroutine with 1s reconnect backoff; ctx.Done() exits cleanly; malformed payloads logged + skipped. `Set.HydrateFromRedis(ctx, rdb, log)` boot-time helper. `parseState` wire-string mapper (unknown → StateOff).
- `gateway/internal/shed/subscribe_test.go` (181 lines) — 10 tests covering Subscribe round-trip, Hydrate seeding remoteState, FSM-not-forced invariant, malformed-payload resilience, nil-client guards, MakePublishTransition write+publish, parseState exhaustive mapping.
- `gateway/internal/shed/reconcile.go` (106 lines) — `Set.ReconcileLoop(ctx, rdb, interval, log)` + `reconcileOnce`. Reads `gw:shed:{upstream}` Hash for each managed upstream; counter results `ok|diverged|error`; corrects `remoteState` on divergence; nil-rdb early-return.
- `gateway/internal/shed/reconcile_test.go` (84 lines) — 5 tests: divergence detection, agreed-state stability, no-redis-state ok path, ctx.Done() shutdown, nil-rdb immediate return.
- `gateway/internal/shed/tick.go` (210 lines) — `RunTicker(ctx, deps, log)` + `runOneTick` method; `TickerDeps` struct (Set, Inflight, Latency, VramReader, ThresholdSrc, Rdb, Interval, TenantLabel); `Thresholds` struct; `VramReader` interface. Shed-force override drives FSM ahead of signal evaluation; zero-Thresholds skip; nil VramReader → VramUnknown=true (1-of-2 reduction).
- `gateway/internal/shed/tick_test.go` (170 lines) — 6 tests: composite signal Off→Armed→On, VramUnknown 1-of-2, zero-threshold skip, nil VramReader safety, RunTicker ctx cancel <500ms, nil-Set early-return.

## Decisions Made

1. **InflightRegistry.ObserveMetrics replaced by inlined fanout.** The plan referenced `d.Inflight.ObserveMetrics(upstream, d.TenantLabel)` but `inflight.go` (owned by Plan 05-03) does not expose that method. Parallel-execution rules forbid modifying it. The inline implementation in `tick.go` uses the existing public API (`TenantsForUpstream` + `TenantInflight` + the supplied `TenantLabel` func) with identical semantics — no new public surface added.
2. **`AllShedStateKeys` uses `strings.HasPrefix` for force-key filtering.** Plan listing used a length-and-slice comparison; `strings.HasPrefix` is idiomatic Go and immune to off-by-one.
3. **Operator-override malformed value handling.** When `gw:shed:force:{upstream}` holds a string outside `{off, on}`, the ticker logs WARN and `return`s (skipping that upstream for this tick) rather than falling through to signal evaluation. Falling through could double-evaluate against a stale override the operator is about to delete.
4. **Reconcile counter increments on `ok` even when Redis has no record.** A boot-time upstream with no Hash yet is the most-common case and indistinguishable from "agreement at zero values" semantically — both paths increment `{result="ok"}`.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Inlined per-tenant gauge fanout instead of calling InflightRegistry.ObserveMetrics**

- **Found during:** Task 5.3 (tick.go implementation)
- **Issue:** Plan listed `d.Inflight.ObserveMetrics(upstream, d.TenantLabel)` as the per-tenant Prometheus fanout call. The method does not exist in `gateway/internal/shed/inflight.go` (which is owned by Plan 05-03, merged in the base wave). Parallel-execution constraints state: "Already-created files in gateway/internal/shed/ … ONLY import from them, do NOT modify."
- **Fix:** Inlined the fanout in `tick.go::runOneTick` using `d.Inflight.TenantsForUpstream(upstream)` + `d.Inflight.TenantInflight(upstream, tid)` + `d.TenantLabel(tid)` — the public API exposed by Plan 05-03. Identical semantics: emits `gateway_inflight_tenant{upstream, tenant}` per tenant-upstream pair, cardinality budget unchanged (3 × 6 = 18 series, D-D4).
- **Files modified:** `gateway/internal/shed/tick.go` (only).
- **Verification:** `TestRunOneTick_*` suite passes; manual inspection confirms the gauge surface is updated once per tick per (upstream, tenant) pair via `WithLabelValues(upstream, label).Set(...)`.
- **Committed in:** `5ad85b9` (Task 5.3 GREEN).

**2. [Rule 1 - Bug] Plan code listing used unsafe-pattern length-and-slice for force-key filtering**

- **Found during:** Task 5.1 (redisx/shed.go implementation)
- **Issue:** Plan code listing for `AllShedStateKeys` used `if len(k) > len(shedForceKeyPrefix) && k[:len(shedForceKeyPrefix)] == shedForceKeyPrefix`. This is off-by-one-prone (the `>` should be `>=`) and unnecessarily clever.
- **Fix:** Replaced with `if strings.HasPrefix(k, shedForceKeyPrefix)`. Identical semantics, idiomatic, off-by-one-immune.
- **Files modified:** `gateway/internal/redisx/shed.go` (only).
- **Verification:** `TestAllShedStateKeys_ExcludesForceKeys` passes; 2 state keys returned out of (2 state + 1 force) written.
- **Committed in:** `e9e46ea` (Task 5.1 GREEN).

---

**Total deviations:** 2 auto-fixed (1 blocking, 1 bug)
**Impact on plan:** Both deviations preserve the planned contract (counter labels, gauge labels, AllShedStateKeys filter behavior). No scope creep, no public-API additions. Plan 05-06 wiring will see exactly the surface the plan promised.

## Issues Encountered

- **Go binary not on default PATH** in the worktree shell — `go: comando não encontrado` until `/home/pedro/.local/go/bin` was exported. Resolved by prefixing each verification command with `export PATH=…`. Not a code issue.

## Coordination Diagram (3 Goroutines + Set)

```
                   ┌──────────────────────────────────────────────┐
                   │                  Set (Plan 05-03)            │
                   │  fsms: map[upstream]*FSM (authoritative)     │
                   │  remoteState: map[upstream]State (advisory)  │
                   └──────────────────────────────────────────────┘
                            ▲           ▲              ▲
                            │           │              │
                  (1) HydrateFromRedis  │              │  (4) ApplyRemoteEvent
                  (boot, HGETALL)       │              │  (corrects remoteState)
                            │           │              │
                            │  (2) onChange → publishTransition (goroutine)
                            │      HSET gw:shed:{name} + PUBLISH gw:shed:events
                            │           │              │
   ┌────────────────────────┴───┐  ┌───┴────────────┐ │
   │  RunTicker (1Hz)           │  │  Subscribe     │ │
   │  TickerDeps.runOneTick:    │  │  consumer loop │ │
   │  - shed-force override?    │  │  reconnect 1s  │ │
   │  - 2-of-3 Signals derive   │  │  gw:shed:      │ │
   │  - fsm.Evaluate            │  │   events       │ │
   │  - GaugeFanout (inflight,  │  └────────────────┘ │
   │    p95, force_active)      │                     │
   └────────────────────────────┘                     │
                            │                         │
                            │  (3) ReconcileLoop (30s)│
                            │  HGETALL gw:shed:{name} │
                            │  compare vs remoteState │
                            │  diverged → ApplyRemoteEvent + counter
                            └──────────────────────────────────────┘
```

**Boot order (Plan 05-06 main.go wires):**

1. `Set.Rebuild(loader.Names())` — populate FSMs.
2. `Set.HydrateFromRedis(ctx, rdb, log)` — seed remoteState from mirror.
3. `go Set.Subscribe(rootCtx, rdb)` — start Pub/Sub consumer.
4. `go Set.ReconcileLoop(rootCtx, rdb, 30s, log)` — start periodic sweep.
5. `go shed.RunTicker(rootCtx, deps, log)` — start 1Hz FSM evaluator.
6. FSM.onChange = `MakePublishTransition(rdb)` (wired at NewSet time).

## Redis Keys Layout (final)

| Key                          | Type   | Purpose                                              | TTL          |
|------------------------------|--------|------------------------------------------------------|--------------|
| `gw:shed:{upstream}`         | Hash   | Mirror of current FSM state + last-known signals     | none (persistent) |
| `gw:shed:events`             | Pub/Sub channel | Cross-replica state transition broadcast    | n/a          |
| `gw:shed:force:{upstream}`   | String | Operator override ("off"\|"on")                      | 0 < ttl ≤ 1h |

**Hash fields** (`gw:shed:{upstream}`):
- `state`: `"off"` | `"armed"` | `"on"` | `"recovering"`
- `since_unix`: int64 (Unix seconds at transition)
- `reason`: string (e.g. `"arm_timeout_sustained"`, `"operator_override"`)
- `inflight`: int64 (optional, present when signals captured at transition)
- `p95_ms`: uint32 (optional)
- `vram_mib`: int64 (optional)

## Metric Interactions (when each is updated)

| Metric                                              | Updated by                          | Trigger                                                |
|-----------------------------------------------------|-------------------------------------|--------------------------------------------------------|
| `gateway_shed_mirror_failures_total`                | `mirror.go::publishTransition`      | HSET or PUBLISH error (each error increments once)     |
| `gateway_shed_mirror_reconcile_total{result}`       | `reconcile.go::reconcileOnce`       | Every upstream every 30s (`ok`/`diverged`/`error`)     |
| `gateway_shed_force_active{upstream}`               | `tick.go::runOneTick`               | 1 when force key present at tick, 0 otherwise (per-upstream per-tick) |
| `gateway_p95_request_ms{upstream}`                  | `tick.go::runOneTick`               | Once per upstream per tick (ring.P95())                |
| `gateway_inflight{upstream}`                        | `tick.go::runOneTick`               | Once per upstream per tick (GlobalInflight)            |
| `gateway_inflight_tenant{upstream,tenant}`          | `tick.go::runOneTick`               | Per (upstream, tenant) per tick — only when `TenantLabel != nil` |
| `gateway_shed_state{upstream}`                      | `fsm.go::transition` (Plan 05-03)   | On FSM transition (already wired by Plan 05-03)        |
| `gateway_shed_transitions_total{upstream,from,to}`  | `fsm.go::transition` (Plan 05-03)   | On FSM transition (already wired by Plan 05-03)        |

## Threat Coverage

- **T-05-08** (Tampering: forged Pub/Sub events) — **accept** disposition honored. `Subscribe` consumes any payload that JSON-parses; `parseState` defaults unknowns to StateOff. `ApplyRemoteEvent` updates remoteState (advisory only) — does NOT force the in-process FSM (D-C3 invariant). Redis network is trusted infra per CLAUDE.md.
- **T-05-09** (DoS: stuck shed-force) — **mitigated**. `WriteShedForce` enforces `0 < ttl ≤ 1h` ceiling; `GatewayShedForceActive{upstream}` gauge surfaces active overrides to dashboards; `DeleteShedForce` (Plan 05-07 CLI) clears overrides explicitly.

## Pending for Plan 05-06

- Wire `MakePublishTransition(rdb)` into `NewSet`'s `Options.OnChange`.
- Wire `HydrateFromRedis` → `Subscribe` → `ReconcileLoop` → `RunTicker` boot sequence in `gateway/cmd/gateway/main.go`.
- Construct `TickerDeps` with real `Inflight`, `Latency`, `VramReader` (from Plan 05-04 dcgm.Scraper), `ThresholdSrc` (from `upstreams.Loader.CircuitConfig`), `TenantLabel` (from `tenants.Loader` slug resolver).
- Implement shed middleware (the request-level decision: tier-0 vs tier-1 vs 503) that reads `Set.Get(name).State()` and `InflightRegistry.TenantInflight(...)`.

## Self-Check: PASSED

Verified at completion:
- **Files exist:** `gateway/internal/redisx/shed.go`, `gateway/internal/redisx/shed_test.go`, `gateway/internal/shed/mirror.go`, `gateway/internal/shed/subscribe.go`, `gateway/internal/shed/subscribe_test.go`, `gateway/internal/shed/reconcile.go`, `gateway/internal/shed/reconcile_test.go`, `gateway/internal/shed/tick.go`, `gateway/internal/shed/tick_test.go` — all 9 FOUND.
- **Commits exist:** `25c5d7a`, `e9e46ea`, `e057924`, `3c6b26a`, `acab741`, `5ad85b9` — all 6 FOUND in `git log`.
- **`go test -race -count=1 ./gateway/internal/shed/... ./gateway/internal/redisx/...`** — PASS.
- **`go build ./...`** — clean (no output).
- **`go vet ./...`** — clean (no output).
- **No files outside envelope modified** — `git status --short` clean post-commit on each task.

---
*Phase: 05-load-shedding-saturation-aware-routing*
*Plan: 05*
*Completed: 2026-05-11*
