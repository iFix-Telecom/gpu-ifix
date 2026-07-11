---
phase: 20
plan: "07"
subsystem: primary-provisioning
tags: [coldstart, fast-fail, port-bind, ff-02]
requires: [20-04, 20-06]
provides: [port-bind-download-anchor, expected-weight-files-4]
affects: [gateway/internal/primary/reconciler.go]
tech-stack:
  added: []
  patterns: [three-way-anchor, ff-02-disarm-gate]
key-files:
  created: []
  modified:
    - gateway/internal/primary/reconciler.go
    - gateway/internal/primary/reconciler_test.go
key-decisions:
  - "Port-bind fast-fail uses a three-way anchor: skip while download armed-but-not-done (FF-02 + coldstart_budget_s own that window), measure from downloadDoneAt once done, fall back to firstRunningAt when telemetry never arms."
  - "expectedWeightFiles 3→4 to match the 4 mandatory onstart download_with_verify calls (qwen+whisper+bge-m3+chatterbox); the 3 non-qwen files can no longer prematurely disarm FF-02 while qwen downloads."
requirements-completed: [FF-01, FF-02]
duration: "~35 min"
completed: 2026-07-11
---

# Phase 20 Plan 07: Port-bind gate on downloadDone + FF-02 expectedWeightFiles 3→4 Summary

Gap-closure for two coldstart-poll defects surfaced by the 20-06 live UAT on real Vast (lifecycle 137). Both in `gateway/internal/primary/reconciler.go`; neither changes the FSM shape.

- **Tasks:** 3 · **Files:** 2 modified · **Commits:** 2 code/test + 1 docs

## What was built

**Task 1 — port-bind anchored on download completion.** A fresh primary downloads ~20GB of weights inside the onstart script (after `actual_status=running`, before llama-server binds its public port). The tight `port_bind_budget_s` (seed 120s) previously anchored on `firstRunningAt` = download start, so a healthy download false-killed with `public_port_bind_timeout`. Added `downloadDoneAt time.Time` (set when FF-02 disarms) and a `portBindElapsed()` closure with a three-way anchor applied to both port-bind blocks (unbound-ports at ~:1733 and TCP-unreachable at ~:1762):
- `downloadArmed && !downloadDone` → `enforce=false`: skip the timeout (Warn still logs); FF-02 byte-stall + `coldstart_budget_s` backstop own this window.
- `downloadDone` → measure `time.Since(downloadDoneAt)` (post-download service-bind window).
- never armed (telemetry unavailable) → legacy `firstRunningAt` anchor (no regression).

The `>=` (not `>`) comparison is preserved so a `budget=0` unit test still fires deterministically.

**Task 2 — expectedWeightFiles 3→4.** The PRIMARY onstart (`onstart.go`) runs 4 mandatory `download_with_verify` calls. At 3, FF-02's disarm case could fire on the 3 non-qwen files while qwen (the ~18GB one) still downloaded. Set to 4 with an enumerating comment; the optional jinja 5th (okCount 5 ≥ 4) still disarms.

**Task 3 — tests.** Four new tests + one stale-fixture fix (see Deviations).

## Verification

- `cd gateway && go build ./...` → OK
- `go test ./internal/primary/` → ok (19.5s)
- `go test ./...` (full gateway) → all green (exit 0)
- `gofmt -l internal/primary/` → empty

New tests: `TestPortBind_DownloadInFlight_RidesThenFiresAfterDone`, `TestPortBind_TelemetryUnavailable_FiresFromFirstRunning`, `TestFF02_ThreeOfFourOk_StaysArmed`, `TestFF02_FourthOk_Disarms`.

## Deviations from Plan

**[Rule 1 — Bug] Stale fixture in `TestProgressStall_PostDownloadStartup_Rides`** — Found during: Task 3 build. The existing test modeled a "complete download" as only 3 `ok` lines; with `expectedWeightFiles=4` (Task 2) that fixture no longer disarms, so the frozen-bytes ride would false-trip `progress_stall_timeout`. Fix: added the 4th mandatory weight (`chatterbox` fetching/progress/ok) to the fixture so it represents a genuinely complete download. Files: `reconciler_test.go`. Verification: test passes; full suite green. Commit: b40d861.

**Total deviations:** 1 auto-fixed (stale test fixture). **Impact:** none on production behavior — fixture now matches the 4-download reality.

## Issues Encountered

None.

## Post-deploy ops (human, not code)

After this ships to prod, revert the live 20-06 workaround: PATCH `port_bind_budget_s` back to the seed 120 (or a chosen tight value) via `/admin/primary/config` — the code fix makes the tight budget safe again.

## Next Phase Readiness

Phase 20 complete (all 7 plans have summaries once 20-06 closes; 20-07 was the last gap-closure). Ready for phase verification.
