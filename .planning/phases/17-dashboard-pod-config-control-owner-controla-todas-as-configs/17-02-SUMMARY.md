---
phase: 17-dashboard-pod-config-control-owner-controla-todas-as-configs
plan: 02
subsystem: gateway
tags: [go, podconfig, listen-notify, atomic-snapshot, hot-reload, pgx, last-good]

# Dependency graph
requires:
  - phase: 17-01
    provides: "gen.AiGatewayPodConfig model + GetPodConfig query + pod_config_changed NOTIFY trigger"
  - phase: 06.6-primary-pod-refactor
    provides: "upstreams.Loader + upstreams.listen.go mirrored verbatim; primary.ScheduleRule shape"
provides:
  - "podconfig.Loader — atomic.Pointer snapshot of 16 hot fields + bounds + pre-parsed ScheduleRule"
  - "last-good-on-error Refresh invariant (query error OR bad schedule keeps prior snapshot)"
  - "podconfig.ListenAndReload — LISTEN pod_config_changed reload goroutine on a dedicated pgx.Conn"
  - "obs.PodConfigReloadTotal counter (result=ok|error)"
  - "podconfig.ParseScheduleFromSnapshot + ScheduleRule local mirror (cycle-free with package primary)"
affects: [17-03-loader-seed-wire, 17-04-admin-write-endpoint, 17-05-reconciler-refactor]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Mirror upstreams.Loader for a second NOTIFY-driven config table (snapshot + atomic.Pointer + last-good-on-error)"
    - "LOCAL ScheduleRule data-mirror (no methods) to avoid import cycle: package primary imports podconfig in 17-03"
    - "Structural timezone resolved once at NewLoader (fail-fast LoadLocation), never re-resolved at runtime (D-03a)"

key-files:
  created:
    - gateway/internal/podconfig/types.go
    - gateway/internal/podconfig/loader.go
    - gateway/internal/podconfig/listen.go
    - gateway/internal/podconfig/loader_test.go
  modified:
    - gateway/internal/obs/metrics.go

key-decisions:
  - "ScheduleRule is a LOCAL mirror (data-only, no IsInPeak/Should* methods) — package primary will import podconfig in 17-03, so importing primary back here would cycle"
  - "Testable error path for the schedule re-parse comes from out-of-range up/down hour validation [0,23] (NOT timezone — tz is fixed at boot, D-03a, so LoadLocation can never fail post-boot)"
  - "Loader.Cfg()/Rule()/Bounds() each do a single atomic.Pointer load; 17-03 reconciler reads them live every tick so ListenAndReload passes onReload=nil"
  - "loaderQueries interface returns gen.AiGatewayPodConfig (NOT gen.PodConfig) per 17-01 generated model name"

requirements-completed: [POD-CFG-03]

# Metrics
duration: 25min
completed: 2026-06-30
---

# Phase 17 Plan 02: podconfig Hot-Reload Engine Summary

**`podconfig.Loader` serves an atomic snapshot of the 16 hot pod_config fields + owner bounds + a pre-parsed schedule rule, refreshed on `pod_config_changed` NOTIFY via a dedicated pgx.Conn, with the load-bearing last-good-on-error invariant (a DB hiccup or broken schedule never swaps zero/garbage config into the provisioning hot path).**

## Performance

- **Duration:** ~25 min
- **Tasks:** 2 (Task 1 split RED/GREEN per tdd="true")
- **Files modified:** 5 (4 created, 1 modified)

## Accomplishments
- **Task 1 — Loader + types + reload metric (TDD).** `podconfig.Loader` mirrors `upstreams.Loader`: a `snapshot{cfg, rule, bounds}` swapped via `atomic.Pointer[snapshot]`; `loaderQueries` interface isolates `gen.AiGatewayPodConfig` for fakeable tests; `NewLoader` resolves the structural timezone once (fail-fast `time.LoadLocation`) then does an initial fail-fast `Refresh`. `Refresh` keeps last-good on BOTH a query error and a bad schedule re-parse (increments `obs.PodConfigReloadTotal{result="error"}`, returns without swapping). `Cfg()/Rule()/Bounds()/Load()` are lock-free single atomic loads. Five behaviors proven by unit tests (healthy swap, query-error last-good, bad-schedule last-good, zero-snapshot-before-first-refresh, concurrent no-tear under `-race`).
- **Task 2 — ListenAndReload.** Mirrors `upstreams.ListenAndReload` verbatim: dedicated `pgx.Conn` via `Connect` (not the pool), `ReconnectDelay: 5s`, `Handle("pod_config_changed", …)` whose handler calls `loader.Refresh` and **returns nil on Refresh error** so a transient DB hiccup never takes the listen loop down (T-17-05). Returns `ctx.Err()` on clean cancel; surfaces the listener error only on unexpected exit.

## Task Commits

1. **Task 1 (RED):** failing Loader snapshot + last-good tests — `6f43339` (test)
2. **Task 1 (GREEN):** implement Loader atomic snapshot + last-good-on-error — `5df473d` (feat)
3. **Task 2:** ListenAndReload on pod_config_changed — `ff1165d` (feat)

## Files Created/Modified
- `gateway/internal/podconfig/types.go` — `PodConfig` (16 hot fields), `PodConfigBounds` (20 min/max), `ScheduleRule` local mirror, `weekdayFromCSV`, `ParseScheduleFromSnapshot`, `rowToPodConfig`/`rowToBounds`/`numericToFloat`
- `gateway/internal/podconfig/loader.go` — `Loader`, `snapshot`, `loaderQueries`, `NewLoader`, `Refresh` (last-good-on-error), `Load`/`Cfg`/`Rule`/`Bounds`
- `gateway/internal/podconfig/listen.go` — `ListenAndReload` on `pod_config_changed`
- `gateway/internal/podconfig/loader_test.go` — fake `loaderQueries` + 5 behavior tests
- `gateway/internal/obs/metrics.go` — `PodConfigReloadTotal` counter (`result=ok|error`)

## Decisions Made
- **Local `ScheduleRule` mirror (data-only).** The plan offered "reuse `primary.ScheduleRule` shape OR a local mirror". Chose the local mirror because Plan 17-03's reconciler (package `primary`) imports `podconfig` on the hot path — importing `primary` back into `podconfig` would form an import cycle. The mirror carries data only; 17-03 maps it into `primary.ScheduleRule` for evaluation.
- **Schedule error path = hour-range validation, not timezone.** `ParseScheduleFromSnapshot` validates `up_hour`/`down_hour ∈ [0,23]`. The timezone is structural (D-02), resolved once at boot (D-03a), so a post-boot `LoadLocation` failure is impossible — the testable bad-rule path is an out-of-range hour, keeping last-good (T-17-06).
- **`gen.AiGatewayPodConfig`** used throughout (per 17-01 prior-wave note; the PATTERNS pseudo-code's `gen.PodConfig` does not exist).

## Deviations from Plan

None - plan executed exactly as written. (PATTERNS pseudo-code referenced `gen.PodConfig`; the real generated name `gen.AiGatewayPodConfig` was already flagged by the 17-01 summary and prior-wave note, so using it is the planned contract, not a deviation.)

## Threat Surface
- T-17-04 (config-read failure starves provisioning) mitigated: `Refresh` never swaps on error; reconciler reads in-memory `Cfg()/Rule()/Bounds()`, never a synchronous DB call — proven by `TestLoaderRefresh_QueryErrorKeepsLastGood`.
- T-17-05 (LISTEN drop stops hot-reload) mitigated: dedicated pgx.Conn + 5s reconnect + handler returns nil on transient error.
- T-17-06 (broken schedule swapped live) mitigated: bad-rule re-parse keeps last-good — proven by `TestLoaderRefresh_BadScheduleKeepsLastGood`.
- No new threat surface introduced (no network endpoints, no auth paths — this is an internal read-path loader).

## Next Phase Readiness
- Plan 17-03 wires `NewLoader` into `main.go` (boot seed + initial Refresh) and starts `ListenAndReload` as a goroutine, then refactors the reconciler to read `loader.Cfg()/Rule()/Bounds()` instead of `r.cfg`.
- Plan 17-04's admin PATCH endpoint mutates the row → the trigger fires `pod_config_changed` → this loader refreshes live.

---
*Phase: 17-dashboard-pod-config-control-owner-controla-todas-as-configs*
*Completed: 2026-06-30*

## Self-Check: PASSED
- All 5 key files verified present on disk.
- All 3 commits (6f43339, 5df473d, ff1165d) verified in git log.
