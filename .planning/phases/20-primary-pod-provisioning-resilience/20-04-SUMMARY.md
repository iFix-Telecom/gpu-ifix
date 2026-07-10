---
phase: 20-primary-pod-provisioning-resilience
plan: 04
subsystem: primary-pod-reconciler
tags: [go, vast-ai, coldstart, fast-fail, blocklist, allowlist, reconciler, tdd]

# Dependency graph
requires:
  - phase: 20-01
    provides: pod_config.CreatedBudgetS + ProgressStallBudgetS (podconfig.PodConfig/PodConfigBounds) HOT budget fields
  - phase: 20-02
    provides: download-weights.sh byte-heartbeat log lines (fetching/progress bytes=/ok) that FF-02 parses
  - phase: 20-03
    provides: vast.OnstartLog(ctx,id) -> OnstartLogResult{Status,Text} + primary.VastAPI.OnstartLog + fakeVast.onstartLogFn
provides:
  - "FF-01 created-state budget: a pod stuck in actual_status=created/scheduling is destroyed at ~created_budget_s (created_state_timeout); created->loading (regime 2) rides to the coldstart ceiling"
  - "FF-02 byte-progress download-stall detector: fires progress_stall_timeout only when dest-file bytes are provably frozen while OnstartLog telemetry is Available, scoped to the fetching->all-ok download phase; telemetry-unavailable rides"
  - "BL-01 classified auto-blocklist + AL-01 auto-allowlist at the provisionLifecycle return; machineAttributableReason gate; recordProvisionOutcome hook"
  - "waitForReadyOrDestroy now returns (reason, err) so the outcome hook can classify the close reason"
affects: [20-05, 20-06]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Two-observation budget anchor (anchor on first created poll, fire on a subsequent one) so a created->loading transition resets and wins — distinct from the single-poll port-bind anchor"
    - "Telemetry-status branching: fire only on OnstartLogAvailable; NotReady/FetchError/Empty = UNKNOWN => ride to ceiling, never false-kill (Codex #3)"
    - "Reason-classifier allowlist for auto-blocklist: only machine-attributable close reasons park a host; global/ambiguous reasons (incl. progress_stall_timeout) never do (Codex #9)"
    - "Sub-cadence throttle for an async external fetch inside a tighter poll loop (primaryOnstartLogIntervalForTest gate)"

key-files:
  created: []
  modified:
    - gateway/internal/primary/reconciler.go
    - gateway/internal/primary/reconciler_test.go
    - gateway/internal/integration_test/primary_helpers_test.go

key-decisions:
  - "created-budget arms ONLY on created/scheduling and RESETS on the loading transition (regime 1 kill vs regime 2 ride, 20-CONTEXT.md:15-16)"
  - "FF-02 stall is BYTES-frozen + download-phase-scoped + Available-only; a completed download disarms; telemetry-unavailable rides (safe default; SSH-tail follow-up deferred)"
  - "progress_stall_timeout DELIBERATELY excluded from machineAttributableReason — a global R2/network stall hits every host, so blocklisting on it would cascade-poison the market (Codex #9). FF-02 still kills the pod, just does not park the host"
  - "BL-01 off-by-one: gate blocklist on failStreak>=1 (failStreak is read pre-attempt, the open row excluded) so the ban lands on the 2nd consecutive failure"
  - "AL-01 is UNGATED by reason — success always allowlists (dedup, FIFO cap 20) and un-blocklists; a machine is never in both lists; list-write errors are swallowed"

requirements-completed: [FF-01, FF-02, BL-01, AL-01]

# Metrics
duration: ~55min
completed: 2026-07-10
---

# Phase 20 Plan 04: Reconciler coldstart fast-fail (FF-01/FF-02) + outcome-driven blocklist/allowlist (BL-01/AL-01) Summary

**Adds two coldstart fast-fail exits inside `waitForReadyOrDestroy` (regime-1 created-state budget, regime-3 byte-frozen download-stall detector) plus a classified outcome hook at the provision return that auto-blocklists machine-attributable failures on the 2nd consecutive failure and auto-allowlists on success — all in one plan because all four requirements edit `primary/reconciler.go`.**

## Tasks (5/5)

1. **RED — FF/BL/AL tests + compile stubs** — `9637060` (test)
2. **GREEN FF-01 — created-state budget anchor** — `1d9d6b7` (feat)
3. **GREEN FF-02 — byte-progress download-stall detector + parseDownloadProgress** — `af0c314` (feat)
4. **GREEN BL-01+AL-01 — classified outcome hook (waitForReadyOrDestroy -> (reason,err))** — `e12f42b` (feat)
5. **Integration + fmt gate** — `116e48f` (test): OnstartLog on the integration `fakeVastPrimary` (Rule 3 blocker — interface grew in 20-03) + gofmt

## What shipped

- **FF-01 (regime 1 — host morto):** `firstCreatedAt` anchors on the first `created`/`scheduling` poll and fires `created_state_timeout` + `BestEffortDestroy` on a subsequent poll once `elapsed >= created_budget_s` (`>=` for budget=0 determinism). A `created->loading` transition RESETS the anchor so a healthy 15-min image pull (regime 2) rides to `coldstart_budget_s` — never killed. The anchor also clears on reaching `running`.
- **FF-02 (regime 3 — download stall):** a sub-cadence (`primaryOnstartLogIntervalForTest`, default 30s) `OnstartLog` fetch. `parseDownloadProgress` extracts `fetching` (arms), `ok` count (disarms at `expectedWeightFiles`=3), and the SUM of the newest `bytes=` per `key=`. Fires `progress_stall_timeout` + destroy ONLY while armed-and-not-done, on `Available` telemetry, with bytes frozen past `progress_stall_budget_s`. Rising bytes reset the anchor; a completed download disarms; `NotReady`/`FetchError`/`Empty` are UNKNOWN and ride (never false-kill).
- **BL-01/AL-01:** `waitForReadyOrDestroy` now returns `(reason, err)`; the sole caller runs `recordProvisionOutcome(ctx, offer.MachineID, failStreak, reason, err, log)`. `machineAttributableReason` allowlists `created_state_timeout` / `instance_terminal_state(_confirmed)` / `public_port_bind_timeout`; everything else (progress_stall_timeout, cancelled_in_flight, health_timeout, create/config/status_msg) is excluded. Success -> allowlist (dedup, FIFO cap 20) + un-blocklist; machine-attributable failure at `failStreak>=1` -> blocklist + un-allowlist; DB read/write errors logged and swallowed. Helpers `appendDedupCap`/`removeFromList`/`int64SliceEqual`.

## Verification evidence

- `cd gateway && go build ./...` — clean.
- `cd gateway && go test ./...` (whole module, non-integration) — **exit 0**, no FAIL lines.
- `cd gateway && go test ./internal/primary/` — **ok** (all new FF-01/FF-02/BL-01/AL-01 + classifier/parser/list-helper self-checks pass; no regression in existing port-bind/terminal/status_msg tests).
- `cd gateway && sudo env PATH=$PATH go test -tags integration -run Primary ./internal/integration_test/` — **exit 0** (32.8s), i.e. 20-04's own integration surface is green.
- `gofmt -l gateway/internal/primary` — empty. `go vet ./internal/primary/` — clean.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] `fakeVastPrimary` missing `OnstartLog`**
- **Found during:** Task 5 (integration gate).
- **Issue:** the `primary.VastAPI` interface grew `OnstartLog` in 20-03, but the integration double `fakeVastPrimary` was not updated there, so `go test -tags integration ./internal/integration_test/` failed to COMPILE.
- **Fix:** added `fakeVastPrimary.OnstartLog` returning `{Status: OnstartLogNotReady}` (UNKNOWN, non-fatal) so existing primary integration tests are unaffected.
- **Files:** gateway/internal/integration_test/primary_helpers_test.go — **Commit:** 116e48f

**Design resolution (not a plan deviation, but worth recording):** the plan's FF-01 text described a same-poll anchor+fire, but the plan's own regime-2 negative test (`created` once then `loading`, budget=0) REQUIRES the anchor to reset before firing. Implemented as anchor-on-first-created / fire-on-subsequent-created so all four FF-01 cases (stuck, ->running, ->loading, budget>0) hold.

## Deferred Issues (out of scope — logged in deferred-items.md)

- **Full `-tags integration` suite is RED, but NOT from 20-04.** Root cause is migration **0033**'s Down (owner: **20-01**, commit `4f6e958`, ancestor of this base): it `DROP COLUMN created_budget_s` BEFORE `DROP TRIGGER pod_config_update_notify`, whose WHEN-clause references that column -> `SQLSTATE 2BP01`. 20-01's SUMMARY notes the migration was never run against a real Postgres, so it surfaced only now. That failed Down cascades into the 0029 migration tests and leaves the throughput/FSM load tests flaky under concurrent load. 20-04 touched **zero** migration/SQL files (`git diff --name-only <base> HEAD` = 3 Go files). Fix is a one-line reorder in the 0033 Down — see `deferred-items.md`. Surfaced to the orchestrator; belongs to a 20-01 follow-up.

## Threat Flags

None new. This plan's decisions map to the plan `<threat_model>` (billing-burn ceiling untouched; regime-2 + telemetry false-kill guards in place; blocklist poisoning bounded by the reason classifier + failStreak>=1 grace + success-heals + cap-20; leader-gated list writes).

## Self-Check: PASSED

- Files present: gateway/internal/primary/reconciler.go, gateway/internal/primary/reconciler_test.go, gateway/internal/integration_test/primary_helpers_test.go — all FOUND.
- Commits in history: 9637060, 1d9d6b7, af0c314, e12f42b, 116e48f — all FOUND.

---
*Phase: 20-primary-pod-provisioning-resilience*
*Completed: 2026-07-10*
