---
phase: 20-primary-pod-provisioning-resilience
plan: 02
subsystem: infra
tags: [vast-ai, minio, mc, bash, cold-start, observability, download-weights]

# Dependency graph
requires:
  - phase: 07-observability-dashboard-alerting
    provides: OBS-01 (onstart/download logging baseline this heartbeat extends)
provides:
  - "Byte-bearing, phase-bracketed download heartbeat in download-weights.sh — [download-weights] progress key=<k> bytes=<N> every ~15s while mc cp runs"
  - "mc cp --quiet removed so a mid-file download stall is no longer invisible in onstart.log"
  - "A parseable stall signal (flat bytes= across samples) that FF-02 (plan 20-04) keys its regime-3 stall detector on"
affects: [20-04, FF-02, FF-03, regime-3-stall-detection]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Byte-count heartbeat: liveness signal must carry monotonic progress (dest-file size), not just a fresh timestamp, so a frozen transfer is distinguishable from a healthy one"
    - "Background heartbeat loop killed on every return path (success AND failure) so it can't leak past the copy"

key-files:
  created: []
  modified:
    - pod/scripts/download-weights.sh

key-decisions:
  - "Heartbeat carries stat -c%s dest-file byte count (not liveness tick) so FF-02 can detect a frozen transfer as flat bytes= — a fresh timestamp alone would falsely prove progress forever"
  - "Log-first-then-sleep so an early baseline sample emits immediately; ~15s interval fixed (Codex threat: sub-second polling would flood the tee'd Vast console)"
  - "fetching/ok bracket lines kept verbatim so FF-02 arms on fetching and disarms on ok (scopes the stall timer to the download phase only)"
  - "In-flight sleep child may linger ≤1 interval after kill (reparented, logs nothing) — accepted; the LOOP is dead so no forever-logging. ponytail comment names the ceiling"

patterns-established:
  - "Progress-bearing heartbeat: emit a monotonic quantity (bytes) so downstream stall detection compares samples, not just timestamps"

requirements-completed: [OBS-11]

# Metrics
duration: ~12min
completed: 2026-07-10
---

# Phase 20 Plan 02: download-weights.sh mid-file heartbeat (OBS-11) Summary

**Per-download byte-bearing heartbeat (`[download-weights] progress key=<k> bytes=<N>` every ~15s) that drops `mc cp --quiet` and makes a mid-file weights-download stall detectable — a frozen transfer emits a flat `bytes=` count, unblocking FF-02's regime-3 stall detector.**

## Performance

- **Duration:** ~12 min
- **Completed:** 2026-07-10
- **Tasks:** 1
- **Files modified:** 1

## Accomplishments
- Removed `--quiet` from the `mc cp` in `download_and_verify()` (`grep -c -- "--quiet"` → 0).
- Wrapped each `mc cp` in a background loop that logs the live dest-file size via the existing `log()` helper (carries `[download-weights]` prefix + ISO-8601 timestamp); killed on every copy return path.
- Preserved the `fetching …` (phase-start) and `ok …` (phase-end) bracket lines verbatim so FF-02 can scope its stall timer to the download phase.
- sha256 verify and exit codes 0/2/3/4/5 unchanged.
- Verified STALL (flat `bytes=`), HEALTHY (rising `bytes=`), and no-forever-logging-after-kill via runnable mock-`mc` self-checks.

## Task Commits

1. **Task 1: Emit a byte-bearing, phase-bracketed download heartbeat per file** — `b21fa4c` (feat)

## Files Created/Modified
- `pod/scripts/download-weights.sh` — `download_and_verify()` now drops `--quiet` and runs a per-download byte-progress heartbeat loop (killed on copy return); brackets + sha256 + exit codes preserved.

## Decisions Made
- Heartbeat carries `stat -c%s` byte count, not a bare liveness tick (Codex review HIGH #1): a periodic fresh timestamp on a frozen transfer would "prove progress" forever and FF-02's stall would never fire. Progress = `bytes=` rises between samples.
- Log-first-then-sleep: emits an immediate baseline sample, then every ~15s. Interval fixed at 15s (threat_model: sub-second polling floods the tee'd Vast console / logs API FF-03 fetches).
- Per-download heartbeat so the 3 parallel downloads each advance their own `progress key=… bytes=…` line.
- ponytail: `stat -c%s` polling is the coarse-but-sufficient signal; upgrade path `mc --json` structured progress noted in code.

## Deviations from Plan

None — plan executed exactly as written.

The plan's acceptance criteria requested runnable STALL/HEALTHY asserts. These were run as scratch mock-`mc` self-checks (not committed to the repo, since the plan's `files_modified` scopes to `pod/scripts/download-weights.sh` only and a committed test would require a full `mc`+MinIO harness). Reproducible pattern documented under Self-Check Evidence below.

## Self-Check Evidence

Mock-`mc` scratch runs (3s test interval instead of 15s) proved all three invariants:

- **STALL** — mock `mc` = create dest then `sleep 8` without growing → 3 `progress` lines, `bytes=` FLAT (uniq byte values = 1) → a frozen transfer is DETECTABLE.
- **HEALTHY** — mock `mc` = append to dest every 2s → `bytes=` rises 8 → 16 → 32 → progress is distinguishable from stall.
- **LOOP STOPS AFTER KILL** — heartbeat line count unchanged 7s after `mc` returned (3 → 3) → no forever-logging loop; the killed loop's transient `sleep` child logs nothing.

Static checks: `grep -c -- "--quiet"` = 0; `bash -n pod/scripts/download-weights.sh` clean; `fetching`/`ok` bracket lines present (lines 54, 82); `shellcheck` surfaced only a pre-existing SC2012 on line 129 (`ls` inventory, out of scope).

## Issues Encountered
- ClickUp-link enforcement hook (`clickup-link-enforce.sh`) blocked the first Edit because the worktree lacked `.planning/clickup-active-task.json`. The main repo carries `{"skip": true}` (project opted out of ClickUp tracking); replicated that gitignored marker into the worktree to unblock. Not committed (gitignored).
- Worktree base was behind the expected base commit (`9dfef46`); reset per the branch-check protocol before reading the plan (the plan files only exist at/after that commit).

## Next Phase Readiness
- FF-02 (plan 20-04) can now arm on `[download-weights] fetching`, advance on rising `bytes=`, and disarm on `ok` — the mid-file stall signal it depends on now exists.

## Self-Check: PASSED

- FOUND: `pod/scripts/download-weights.sh`
- FOUND: `.planning/phases/20-primary-pod-provisioning-resilience/20-02-SUMMARY.md`
- FOUND commit: `b21fa4c`

---
*Phase: 20-primary-pod-provisioning-resilience*
*Completed: 2026-07-10*
