---
phase: 17-dashboard-pod-config-control-owner-controla-todas-as-configs
plan: 03
subsystem: gateway
tags: [go, podconfig, reconciler, hot-reload, snapshot, schedule, budget, main-wiring, seed]

# Dependency graph
requires:
  - phase: 17-01
    provides: "gen.AiGatewayPodConfig + SeedPodConfig + pod_config_changed trigger"
  - phase: 17-02
    provides: "podconfig.Loader (atomic snapshot, last-good-on-error) + ListenAndReload + ParseScheduleFromSnapshot (data-only mirror)"
  - phase: 06.6-primary-pod-refactor
    provides: "primary.Reconciler 5-state FSM + BudgetChecker + ScheduleRule + main.go wiring"
provides:
  - "Reconciler reads the 16 HOT pod_config fields from the live snapshot (caps/blocklist/allowlist/host/reject-private-ip/budgets/schedule); STRUCTURAL fields stay on r.cfg (D-02)"
  - "provision-captured snapshot (read ONCE at provisionLifecycle top) — in-flight attempt is stable, edits land on the NEXT provision (T-17-09)"
  - "primary.ParseScheduleFromSnapshot — cycle-free bridge podconfig.PodConfig -> evaluable primary.ScheduleRule"
  - "BudgetChecker reads MonthlyBudgetBRL from the snapshot (cfg fallback)"
  - "main.go: first-boot env->DB seed (idempotent) + podconfig.NewLoader + ListenAndReload goroutine + loader injected into the reconciler"
  - "podconfig.NewStaticLoaderForTest — DB-less snapshot injection seam for downstream unit tests"
affects: [17-04-admin-write-endpoint, 17-06-dashboard-config-page]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Read-once-at-provision-start snapshot capture (atomic.Pointer[PodConfig]) for in-flight stability; per-tick gates read the live snapshot"
    - "liveCfg()/liveRule()/currentProvisionCfg() with boot-cfg fallback so the reconciler NEVER reads zero-config (podCfg nil OR pre-first-refresh)"
    - "DISABLED soak-gate reads liveCfg().ScheduleDisabled (snapshot, cfg-fallback); provision/drain decision reads liveRule() — both from the same snapshot in prod"

key-files:
  created: []
  modified:
    - gateway/internal/primary/lifecycle.go
    - gateway/internal/primary/reconciler.go
    - gateway/internal/primary/reconciler_test.go
    - gateway/internal/primary/schedule.go
    - gateway/internal/primary/schedule_test.go
    - gateway/internal/primary/budget.go
    - gateway/internal/primary/budget_test.go
    - gateway/internal/podconfig/loader.go
    - gateway/cmd/gateway/main.go

key-decisions:
  - "DISABLED soak-gate reads liveCfg().ScheduleDisabled, NOT liveRule().Disabled — preserves the UAT-14 force-up semantics (a disabled rule's ShouldStayUp=false must not auto-drain an operator force-up pod) AND keeps the existing test fixtures (cfg-disabled + alwaysInPeakRule) consistent. In production both derive from the same snapshot field, so they never disagree"
  - "Snapshot captured ONCE at provisionLifecycle top into Reconciler.provisionCfg (atomic.Pointer); budget/reject-private-ip helpers read currentProvisionCfg() so an in-flight attempt keeps captured values WITHOUT threading params through waitForReadyOrDestroy (avoids churning its 4 direct-call tests + 9 ForTest callers)"
  - "ParseScheduleFromSnapshot lives in package primary (returns the evaluable primary.ScheduleRule); takes podconfig.PodConfig as input. podconfig stays import-cycle-free (data-only mirror, never imports primary)"
  - "podconfig.NewStaticLoaderForTest added to loader.go (production file, *ForTest convention matching budget.go's SetCostOverrideForTest) so primary unit tests inject a deterministic snapshot without Postgres"

requirements-completed: [POD-CFG-04]

# Metrics
duration: 45min
completed: 2026-06-30
---

# Phase 17 Plan 03: Reconciler Hot-Reload Seam + main.go Wiring Summary

**The central refactor: the primary reconciler now reads the 16 HOT pod_config fields (caps/blocklist/allowlist/host/reject-private-ip/budgets/schedule) from the live `podCfg` snapshot instead of the immutable boot `r.cfg`, with STRUCTURAL fields (GPU name/num) staying on `r.cfg` (D-02); main.go seeds `pod_config` from env on first boot (idempotent) and runs the NOTIFY-driven reload goroutine — so a blocklist/cap/schedule edit reaches the next provision/tick in seconds with NO gateway restart (the 2026-06-29 blocklist-append daily-pain fix).**

## Performance
- **Duration:** ~45 min
- **Tasks:** 3 (Tasks 1+2 split RED/GREEN per tdd="true")
- **Files modified:** 9

## Accomplishments
- **Task 1 (TDD) — offer selection + budgets/timeouts from the snapshot.** Added `Deps.PodCfg` + `Reconciler.podCfg` + `Reconciler.provisionCfg atomic.Pointer[podconfig.PodConfig]`, wired through both constructors. `provisionLifecycle` reads `hot := r.liveCfg()` ONCE at the top and captures it; offer selection (caps/host/blocklist/allowlist) reads `hot.*`, structural GPU name/num STILL read `r.cfg`. `waitForReadyOrDestroy` (cold-start + port-bind budgets) and `rejectPrivateIPOffers` read the provision-captured snapshot via `currentProvisionCfg()`; `cooldownElapsed` reads the live snapshot. `liveCfg()`/`bootHotCfg()` guarantee a never-zero-config fallback. Two unit tests prove: (a) the snapshot blocklist reaches the Vast SearchFilter while the boot blocklist does not + structural GPU name still from r.cfg, (b) podCfg nil falls back to boot cfg.
- **Task 2 (TDD) — schedule re-parse + budget snapshot read.** `primary.ParseScheduleFromSnapshot(podconfig.PodConfig, *time.Location)` bridges the data-only mirror into an evaluable `primary.ScheduleRule` (cycle-free). `liveRule()` re-parses from the snapshot each tick (last-good fallback to the boot rule). `evaluateAsleep`/`evaluateReady` DISABLED gate reads `liveCfg().ScheduleDisabled`; provision/drain reads `liveRule()`; `evaluateDraining` grace reads the live snapshot. `BudgetChecker` gained a `*podconfig.Loader`; `CheckBudget` reads `MonthlyBudgetBRL` from the snapshot (cfg fallback). 4 schedule + 1 budget unit tests added.
- **Task 3 — main.go wiring.** First-boot `SeedPodConfig` from env (idempotent ON CONFLICT DO NOTHING) → `podconfig.NewLoader` (fail-fast `os.Exit(2)`) → `go podconfig.ListenAndReload(...)` (nil onReload, mirrors the upstreams LISTEN block) → `podCfgLoader` injected into `NewReconcilerFull` Deps. `podConfigSeedParams` maps the 16 hot env fields + the 10 RESEARCH bound defaults; env stays the disaster-recovery fallback (never removed).

## Task Commits
1. **Task 1 RED:** failing snapshot offer-selection test — `afcdcb3` (test)
2. **Task 1 GREEN:** reconciler reads offer-selection + budgets from snapshot — `392d8d9` (feat)
3. **Task 2 RED:** failing schedule-from-snapshot + budget-snapshot tests — `47a1e71` (test)
4. **Task 2 GREEN:** ParseScheduleFromSnapshot + liveRule + budget snapshot — `2b613a3` (feat)
5. **Task 3:** main.go loader + ListenAndReload + env seed — `3991b03` (feat)

## Decisions Made
- **DISABLED soak-gate reads `liveCfg().ScheduleDisabled`, not `liveRule().Disabled`.** The initial GREEN unified the gate onto `liveRule().Disabled`, which broke `TestEvaluateReady_TransitionsToDrainingOutOfPeak` + `TestStartDrain_RestoreTier0_CalledFor_STT`: those fixtures express "out of peak" via `neverInPeakRule()` (Disabled=true) while leaving the cfg soak-gate false. The UAT-14 contract is that the SOAK-GATE (operator force-up under DISABLED) is a distinct concern from the window decision. Reading the gate from `liveCfg().ScheduleDisabled` (which falls back to `r.cfg` when podCfg is nil) reproduces the exact pre-existing two-source behavior in tests while making it snapshot-live in production. In prod both derive from the same `schedule_disabled` field, so they never disagree.
- **Read-once capture via `provisionCfg atomic.Pointer`.** Rather than thread cold-start/port-bind budget params through `waitForReadyOrDestroy` (which has 4 direct-call tests + a 9-caller `ForTest` seam), the snapshot is captured once at provision-start and the downstream helpers read `currentProvisionCfg()` (falls back to `liveCfg()` for direct test calls). This honors T-17-09 (in-flight attempt keeps captured values; edits land on the NEXT provision) with zero signature churn.
- **`ParseScheduleFromSnapshot` placed in package primary.** It returns the evaluable `primary.ScheduleRule` and takes `podconfig.PodConfig` as input — `primary` imports `podconfig` (one direction), keeping the import graph cycle-free (17-02's data-only mirror decision).

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Added `podconfig.NewStaticLoaderForTest` to podconfig/loader.go**
- **Found during:** Task 1 (RED test authoring)
- **Issue:** `podconfig.Loader` is a concrete struct with unexported `snapshot`/`snap` fields; `NewLoader` requires a live `*pgxpool.Pool` + a DB query. The primary unit tests need to inject a deterministic snapshot (with a blocklist differing from r.cfg) WITHOUT Postgres, and cannot construct the loader from outside the package.
- **Fix:** Added an exported `NewStaticLoaderForTest(cfg, rule, bounds, log)` that fixes the snapshot with no DB/LISTEN wiring (matches the codebase `*ForTest` convention, e.g. `budget.go`'s `SetCostOverrideForTest`).
- **Files modified:** gateway/internal/podconfig/loader.go
- **Commit:** `afcdcb3` (Task 1 RED)

**2. [Plan-files note] Schedule-read swaps in reconciler.go committed under Task 2**
- The reconciler's schedule reads (DISABLED gate, ShouldBeProvisioned/ShouldStayUp, grace) were swapped in the Task 2 commit (`2b613a3`) because they depend on Task 2's `ParseScheduleFromSnapshot`. `reconciler.go` is listed in the plan's top-level `files_modified`, so this is within scope; only the per-task `<files>` grouping shifted.

**Total deviations:** 1 auto-fixed (Rule 3 - blocking) + 1 file-grouping note. No scope creep.

## Threat Surface
- **T-17-07** (seed overwrites operator edits): mitigated — `SeedPodConfig` is `ON CONFLICT DO NOTHING`; boot seed is idempotent, an existing operator-edited row wins.
- **T-17-08** (structural read moved to snapshot → mis-provision): mitigated — GPU name/num stay on r.cfg; `TestProvisionLifecycle_OfferSelectionReadsSnapshotNotCfg` asserts the snapshot carries NO GPU name and the structural read still comes from r.cfg.
- **T-17-09** (mid-lifecycle edit corrupts in-flight provision): mitigated — snapshot read ONCE at provision-start into `provisionCfg`; downstream helpers read the captured copy.
- **T-17-10** (DB-less boot breaks the gateway / zero-config): mitigated — `liveCfg`/`liveRule`/`currentProvisionCfg` fall back to the boot cfg; env never removed; `TestProvisionLifecycle_NilPodCfgFallsBackToBootCfg` proves it.
- No NEW threat surface introduced (no network endpoints, no auth paths — internal read-path swap + a boot seed). The dashboard PATCH write surface lands in Plan 17-04.

## Verification Evidence
- `cd gateway && go build ./...` exits 0; repo-wide `gofmt -l .` clean.
- `go test ./internal/primary/... ./internal/podconfig/...` PASS (full primary suite green, incl. the 5 new tests).
- Per-wave integration gate (MEMORY gateway-integration-tests-not-in-executor-check): `sudo ... go test -tags integration ./internal/integration_test/ -run 'PodConfig|Primary'` PASS (33.7s) — Docker via sudo (pedro lacks socket access, the known 17-01 condition). No import cycle: `go build ./internal/primary/ ./internal/podconfig/` exits 0.

## Next Phase Readiness
- Plan 17-04 (admin PATCH write endpoint) mutates the `pod_config` row → the trigger fires `pod_config_changed` → the loader wired here refreshes live → the reconciler picks up the edit on the next provision/tick.
- Plan 17-06 (dashboard config page) reads/writes through 17-04; the bounds seeded here gate operator-supplied values.

---
*Phase: 17-dashboard-pod-config-control-owner-controla-todas-as-configs*
*Completed: 2026-06-30*

## Self-Check: PASSED
- All 6 key source files verified present on disk + SUMMARY present.
- All 5 commits (afcdcb3, 392d8d9, 47a1e71, 2b613a3, 3991b03) verified in git log.
