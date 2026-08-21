---
phase: quick-20260821-dash-schedule-audit-fixes
plan: 01
status: complete
subsystem: gateway-admin-audit
tags: [dashboard, admin-operations, audit-log, fsm, incidents]
requirements: [DASH-AUDIT-20260821]
dependency-graph:
  requires: []
  provides:
    - "primary.Reconciler.LiveRule() exported read-only schedule observer"
    - "audit.PrimaryFSMAuditAdapter (primary_state_change rows)"
    - "emerg FSM emerg_state_change rows with data_class normal"
  affects: ["/admin/operations schedule section", "/admin/audit incidents feed"]
key-files:
  created:
    - gateway/internal/audit/fsm_adapter.go
    - gateway/internal/audit/fsm_adapter_test.go
  modified:
    - gateway/internal/primary/reconciler.go
    - gateway/internal/primary/reconciler_test.go
    - gateway/internal/admin/operations.go
    - gateway/internal/admin/operations_test.go
    - gateway/internal/audit/writer.go
    - gateway/internal/emerg/fsm.go
    - gateway/internal/emerg/fsm_test.go
    - gateway/cmd/gateway/main.go
key-decisions:
  - "scheduleSection prefers rec.LiveRule() (pod_config snapshot) when reconciler wired; env parse kept only as nil-rec fallback"
  - "Primary FSM kind fsm_transition remapped to primary_state_change in the adapter; emerg kind renamed to emerg_state_change (event_kind is opaque TEXT — no dashboard change needed)"
  - "Adapter is best-effort: always returns nil, WARN+drop on malformed payload, rides async non-blocking Enqueue"
metrics:
  duration: "~15min"
  completed: "2026-08-21T20:07Z"
  tasks: 3
  commits: 2
---

# Quick 20260821 Plan 01: Dashboard Schedule + Audit Fixes Summary

**One-liner:** /admin/operations schedule now reads the live pod_config rule via exported LiveRule(), and both primary (previously nil-writer) and emerg FSM transitions persist audit_log state-change rows with data_class=normal so /incidents fills.

## Commits

| Commit | Scope |
| --- | --- |
| 6f02236 | fix(admin): operations schedule section reads live reconciler rule, not static env (dashboard ops audit 2026-08-21) |
| c20e377 | fix(audit): record primary/emerg FSM transitions as audit_log state changes (dashboard ops audit 2026-08-21) |

NOT pushed (deploy is a separate orchestrator step) — `develop` is ahead of origin by 2.

## What Was Done

### Fix A — schedule section (commit 6f02236)
- `primary/reconciler.go`: exported `LiveRule()` delegating to `liveRule()` (snapshot re-parse → boot-rule fallback, never errors).
- `admin/operations.go` `scheduleSection()`: uses `h.rec.LiveRule()` when `rec != nil`; keeps `ParseScheduleEnv` + parse-error → minimal-disabled behavior for nil rec.
- Tests: `TestLiveRule_Exported` (boot-rule fallback + snapshot 8→19 wins over boot 9→17, structural tz preserved); `TestOperationsHandler_ScheduleFromLiveRule` (real `NewReconcilerFull` + `NewStaticLoaderForTest`, asserts up 8 / down 19 / disabled false / 5 days over env). Existing nil-rec tests untouched and green.

### Fix B — FSM audit rows (commit c20e377)
- New `audit/fsm_adapter.go`: `PrimaryFSMAuditAdapter{W, Log}` satisfies primary's untyped `stateChangeWriter`; extracts `from/to/reason/at` from the map payload, emits `Event{Route: primary_fsm_transition, Method: from->to, Upstream: to, DataClass: "normal", Reason: "from→to (reason)"}` under kind `primary_state_change`. Malformed payload → WARN + nil. Nil writer → no-op.
- `cmd/gateway/main.go` (~:989): `primary.NewFSM(&audit.PrimaryFSMAuditAdapter{W: auditWriter, Log: log}, onChange)` replaces the nil writer. onChange Redis mirror untouched.
- `emerg/fsm.go`: transition event now sets `DataClass: "normal"` (NOT NULL enum batch-poison fix), kind renamed `fsm_transition` → `emerg_state_change`, `Reason` formatted `from→to (reason)`; `SetAuditWriter` doc updated.
- `audit/writer.go`: `WriteStateChange` doc kinds list updated + DataClass hazard note.
- Tests: `fsm_adapter_test.go` (well-formed → 1 event with kind/DataClass/Route/Method/TS/minted RequestID; malformed → drop+nil; nil-writer no-op); emerg `TestFSMTransitionEmitsAuditRow` updated for new kind/Reason + DataClass assertion.

## Validation Output

- `cd gateway && gofmt -l .` → empty.
- `go build ./...` → clean.
- `go test ./internal/admin/... ./internal/primary/... ./internal/emerg/... ./internal/audit/...` → all `ok` (admin 0.43s, primary 22.6s, emerg 5.2s, audit 10.6s on first uncached run).
- Integration tests (`-tags integration`) NOT run locally — docker unavailable on this host; CI covers them (per plan constraint).

## Deviations from Plan

None functional — plan executed as written. No migrations, no dashboard/frontend changes, no pushes.

## Known Stubs

None.

## Threat Flags

None — no new network endpoints, auth paths, or schema changes; both mitigations from the plan's threat register applied (async best-effort audit path T-Q0821-01; explicit DataClass T-Q0821-02).

## Self-Check: PASSED

- gateway/internal/audit/fsm_adapter.go — FOUND
- gateway/internal/audit/fsm_adapter_test.go — FOUND
- Commit 6f02236 — FOUND in git log
- Commit c20e377 — FOUND in git log
