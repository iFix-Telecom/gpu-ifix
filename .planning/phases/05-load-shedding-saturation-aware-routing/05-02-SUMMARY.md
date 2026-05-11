---
phase: 05-load-shedding-saturation-aware-routing
plan: 02
subsystem: database
tags: [go, postgres, migration, sqlc, shed-config, jsonb]

requires:
  - phase: 05-load-shedding-saturation-aware-routing
    provides: "Wave 0 gate decisions (A=docs-only / B=no-seed / C=DCGM-disabled-at-boot), shed package scaffold (errors.go, tools_phase5.go), 14 obs collectors, auditctx shed helpers"
  - phase: 04-multitenancy-rate-limiting-schedule
    provides: "tenants table with mode/peak/quota/data_class columns + tenants_update_notify trigger family; tenants_admin.sql query pattern (UpdateTenantQuota partial-UPDATE with sqlc.narg COALESCE); Loader.Refresh atomic.Pointer snapshot"
  - phase: 03-failover-circuit-breaker
    provides: "upstreams table with circuit_config JSONB + upstreams_changed NOTIFY trigger (0009); parseCircuitConfig pattern (json.Unmarshal + derived time.Duration); CircuitConfig struct to extend"

provides:
  - "Migration 0016 — 4 hard-cap columns on tenants (local_inflight_max_llm/stt/embed) + priority_tier CHECK IN ('S','A','B') + expanded tenants_update_notify WHEN clause"
  - "Migration 0017 — 5 shed_* fields merged into circuit_config JSONB of the 3 tier-0 upstreams (local-llm/stt/embed) with shed_vram_used_mib=21504 (MiB, not bytes)"
  - "Migration 0018 — docs-only no-op (Gate A confirmed audit_log.upstream is unconstrained TEXT); reserved values shed_saturated / shed_blocked_sensitive / shed_tier1_unavailable documented inline"
  - "sqlc UpdateTenantShedLimits :exec + extended ListTenantsForLoaderRow/GetTenantConfigRow with int32 hard caps + string priority_tier"
  - "CircuitConfig struct +7 shed fields (5 wire + 2 derived time.Duration); parseCircuitConfig now derives ShedArm/ShedRecover from ShedArmSeconds/ShedRecoverSeconds"
  - "TenantConfig struct +4 fields (LocalInflightMaxLLM/STT/Embed + PriorityTier); Loader.Refresh populates from sqlc row"

affects: [05-03-shed-package-core, 05-04-dcgm-scraper, 05-05-shed-middleware, 05-06-dispatcher-precedence, 05-07-gatewayctl-shed-cli, 05-08-integration-tests]

tech-stack:
  added: []
  patterns:
    - "Migration evolves trigger WHEN clause via DROP + CREATE (WHEN is not alterable in Postgres) — list MUST stay synced with prior migrations (0013); Down recreates the original WHEN to leave Phase 4 operation coherent."
    - "JSONB merge with COALESCE(col, '{}'::jsonb) || jsonb_build_object — preserves Phase 3 fields (failures, cooldown_s) untouched. Down uses `-` operator to strip new keys cleanly."
    - "DCGM unit consistency: shed_vram_used_mib JSON tag + ShedVramUsedMiB int64 Go field; never bytes (RESEARCH Pitfall 1)."
    - "Derived time.Duration pattern in parseCircuitConfig (mirrors Cooldown ← CooldownS); JSON tag json:\"-\" excludes derived fields from wire."

key-files:
  created:
    - "gateway/db/migrations/0016_evolve_tenants_shedding_limits.sql — 4 hard-cap cols + priority_tier + trigger WHEN expansion (~91 LoC)"
    - "gateway/db/migrations/0017_evolve_upstreams_shed_thresholds.sql — 3 UPDATE JSONB merges on tier-0 upstreams with MiB-unit thresholds (~68 LoC)"
    - "gateway/db/migrations/0018_audit_log_shed_values.sql — docs-only goose stub (Gate A) (~38 LoC)"
    - ".planning/phases/05-load-shedding-saturation-aware-routing/05-02-SUMMARY.md — this file"
  modified:
    - "gateway/db/queries/tenants_admin.sql — extended SELECT lists + new UpdateTenantShedLimits :exec"
    - "gateway/internal/db/gen/tenants_admin.sql.go — regenerated (sqlc v1.30.0)"
    - "gateway/internal/db/gen/models.go — regenerated (new Tenant fields)"
    - "gateway/internal/db/gen/querier.go — regenerated (UpdateTenantShedLimits method)"
    - "gateway/internal/upstreams/types.go — CircuitConfig +7 shed fields; parseCircuitConfig derives ShedArm/ShedRecover"
    - "gateway/internal/tenants/config.go — TenantConfig +4 phase-5 fields"
    - "gateway/internal/tenants/loader.go — Refresh populates the 4 new fields"

key-decisions:
  - "Per Gate B (resolved 05-01): migration 0016 does NOT seed per-tenant caps — operator is the source of truth via gatewayctl tenant set-shed-limits (Plan 05-07). Conservative column defaults (4/2/8/'A') keep the system safe until then."
  - "Per Gate A (resolved 05-01): migration 0018 is docs-only (audit_log.upstream is TEXT without CHECK; no DDL required). Inline comment block documents the 3 reserved shed_* values for downstream callers."
  - "JSON wire field shed_vram_used_mib (NOT shed_vram_used_bytes) — DCGM_FI_DEV_FB_USED is reported in MiB natively (RESEARCH Pitfall 1). Migration 0017 writes 21504 (=21 GB)."
  - "JSONB merge uses COALESCE(circuit_config, '{}'::jsonb) || jsonb_build_object so rows with NULL circuit_config (defensive against operator inserts) don't fail with `NULL || X = NULL` semantics."

patterns-established:
  - "Pattern: Trigger WHEN expansion — DROP TRIGGER → CREATE TRIGGER with full superset WHEN clause; Down recreates the prior WHEN (without phase-5 cols) to keep downgrade coherent."
  - "Pattern: Conditional migration via Wave 0 gate resolution — Gate A determined 0018 shape was docs-only at planning time, eliminating a possible DDL branch at execute time."
  - "Pattern: sqlc partial UPDATE — `SET col = COALESCE(sqlc.narg('col')::type, col)` per column, idiom inherited from UpdateTenantQuota."

requirements-completed: [LSH-02, LSH-04, LSH-05]

duration: 8min
completed: 2026-05-11
---

# Phase 5 Plan 02: DB Foundation + Types Extension Summary

**Migrations 0016/0017/0018 add 4 hard-cap columns + priority_tier to tenants and merge 5 shed_* thresholds (MiB unit) into tier-0 upstreams' JSONB; CircuitConfig/TenantConfig structs and Loader.Refresh extended; sqlc regenerated with UpdateTenantShedLimits exec.**

## Performance

- **Duration:** 8 min
- **Started:** 2026-05-11T21:27:00Z
- **Completed:** 2026-05-11T21:34:55Z
- **Tasks:** 3 (all atomic-committed)
- **Files created:** 4 (3 migrations + this summary)
- **Files modified:** 7 (1 query SQL + 3 sqlc-gen + types.go + config.go + loader.go)

## Accomplishments

- **Migration 0016**: ALTER ai_gateway.tenants ADD 4 cols (`local_inflight_max_llm`/`stt`/`embed` INT DEFAULT 4/2/8 + `priority_tier` TEXT DEFAULT 'A' CHECK IN ('S','A','B')); DROP+CREATE `tenants_update_notify` trigger so the WHEN clause superset includes the 4 new cols → NOTIFY tenants_changed fires when operator runs `gatewayctl tenant set-shed-limits`.
- **Migration 0017**: jsonb_build_object merge of `{shed_inflight_max, shed_p95_ms, shed_vram_used_mib, shed_arm_seconds, shed_recover_seconds}` into `circuit_config` for `local-llm` (8/2000/21504/30/60), `local-stt` (4/3000/21504/30/60), `local-embed` (16/500/21504/30/60). Tier-1 upstreams left intact (shed never runs against tier-1, D-C4).
- **Migration 0018**: docs-only — Goose stub with `SELECT 1` no-op + a comment block listing `shed_saturated` / `shed_blocked_sensitive` / `shed_tier1_unavailable` reserved values. Gate A confirmed `audit_log.upstream` is unconstrained TEXT (no DDL required).
- **sqlc evolution**: `UpdateTenantShedLimits :exec` (partial UPDATE with `sqlc.narg` COALESCE per col) + 4 new columns added to `ListTenantsForLoader`/`GetTenantConfig` SELECT lists. Regen produced `UpdateTenantShedLimitsParams{Slug, LocalInflightMaxLlm/Stt/Embed pgtype.Int4, PriorityTier pgtype.Text}` and added 4 fields to existing row structs.
- **Go struct extensions**: `CircuitConfig` +7 fields (5 wire — `ShedInflightMax`, `ShedP95Ms`, `ShedVramUsedMiB int64`, `ShedArmSeconds`, `ShedRecoverSeconds` — and 2 derived `time.Duration` — `ShedArm`, `ShedRecover`). `parseCircuitConfig` derives the durations from `*Seconds`. `TenantConfig` +4 fields (`LocalInflightMaxLLM`/`STT`/`Embed` int + `PriorityTier` string). `Loader.Refresh` populates from sqlc row.

## Task Commits

Each task was committed atomically:

1. **Task 2.1: Migrations 0016/0017/0018** — `f2abbfb` (feat)
2. **Task 2.2: Evolve sqlc queries + regenerate Go types** — `e5ad140` (feat)
3. **Task 2.3: Extend CircuitConfig + TenantConfig structs + tenants.Loader.Refresh** — carried by `09d5954` (see Deviations §1)

## Files Created/Modified

### Created
- `gateway/db/migrations/0016_evolve_tenants_shedding_limits.sql` — 4 hard-cap cols + priority_tier CHECK + trigger WHEN expansion. Up adds and seeds nothing per Gate B; Down drops and recreates the Phase 4 trigger.
- `gateway/db/migrations/0017_evolve_upstreams_shed_thresholds.sql` — 3 UPDATE JSONB merges on tier-0 upstreams; uses COALESCE(circuit_config, '{}'::jsonb) to defend against NULL JSONB.
- `gateway/db/migrations/0018_audit_log_shed_values.sql` — docs-only (`SELECT 1` no-op) + comment block per Gate A.

### Modified
- `gateway/db/queries/tenants_admin.sql` — extended SELECT lists; new `UpdateTenantShedLimits :exec`.
- `gateway/internal/db/gen/tenants_admin.sql.go` — regenerated (sqlc v1.30.0): new `UpdateTenantShedLimitsParams`, `UpdateTenantShedLimits` func, +4 fields per Row.
- `gateway/internal/db/gen/models.go` — regenerated (Tenant struct +4 fields).
- `gateway/internal/db/gen/querier.go` — regenerated (Querier interface +UpdateTenantShedLimits method).
- `gateway/internal/upstreams/types.go` — `CircuitConfig` extended; `parseCircuitConfig` derives `ShedArm`/`ShedRecover`.
- `gateway/internal/tenants/config.go` — `TenantConfig` +4 fields.
- `gateway/internal/tenants/loader.go` — `Refresh` populates new fields from `ListTenantsForLoaderRow.LocalInflightMax{Llm,Stt,Embed}` + `.PriorityTier`.

## Decisions Made

- **No live `goose up/down/up` round-trip executed.** `GATEWAY_DB_URL` was unset and `goose` CLI not on PATH on this host (CI pipeline applies migrations in deploy per Phase 2-08 boot-time flag `AI_GATEWAY_MIGRATE_ON_BOOT`). SQL was validated by inspection + sqlc statically parsing the schema during `sqlc generate` (sqlc reads `db/migrations/` to build its type model; a parse error there would have failed step 2.2 — it didn't, exit 0).
- **Conservative defaults at column level** (4/2/8/'A') even though Gate B says operator owns production caps. Default keeps the column NOT NULL contract cheap and makes the row valid without an UPDATE.
- **Migration 0017 wraps JSONB with `COALESCE(circuit_config, '{}'::jsonb)`** (defensive against NULL JSONB even though the schema seeds `'{}'::jsonb` for fresh rows — protects against operator-inserted rows without circuit_config).

## Deviations from Plan

### Parallel-Execution Race (Documented, Not Fixable)

**1. [Plan 05-03 commit absorbed Task 2.3 edits] — Wrong commit attribution**

- **Found during:** Task 2.3 staging (`git status` after Edit) and again confirmed via `git log --oneline`.
- **What happened:** Plan 05-03 was supposedly running in parallel per the dispatch prompt, but the executor is also running on the main working tree (not a worktree). The shared git index meant the parallel agent staged its own test files (`gateway/internal/shed/fsm_test.go`, `set_test.go`) along with my Plan 05-02 struct-extension edits (`types.go`, `config.go`, `loader.go`). When 05-03's commit `09d5954` ("test(05-03): failing tests for FSM 4-state + Set registry (RED)") landed, it carried my Task 2.3 edits inside it. By the time I returned, working tree was clean — leaving Task 2.3 nothing to commit on its own.
- **Functional impact:** None. The edits live in HEAD; `go build ./gateway/internal/upstreams/... ./gateway/internal/tenants/...` and `go vet` on those packages exit 0. The `key-files.modified` paths in this summary's frontmatter accurately reflect what's in HEAD even though the commit hash belongs to a sibling plan.
- **Commit-hash discrepancy:** Task 2.3 row in the Task Commits table points to `09d5954` with a footnote rather than a dedicated Plan 05-02 commit. If clean attribution matters, the SUMMARY ground-truth is the file diff between Phase 5 Plan 02 entry and exit, not the commit hashes per-task.
- **Why not retroactively fix:** Reverting `09d5954` would destroy 05-03's RED-phase test work. Per `destructive_git_prohibition`, `git reset --hard` / `git rebase -i` are forbidden in this context. The pragmatic resolution is documentation, not history rewriting.

**2. [Process — Task 2.1 commit also picked up parallel files]**

- **Found during:** First `git commit` for Task 2.1 (migrations) — output reported 5 files instead of 3 staged.
- **What happened:** Same root cause as deviation §1. `git add gateway/db/migrations/0016_*.sql gateway/db/migrations/0017_*.sql gateway/db/migrations/0018_*.sql` only staged 3 files (`git status` confirmed it), but between that command and `git commit`, the parallel agent staged `inflight_test.go` + `latency_test.go`. The commit captured all 5.
- **Functional impact:** None on Plan 05-02 deliverables. The parallel files belong to Plan 05-03 and would have been committed there anyway. Commit `f2abbfb` (Task 2.1) carries 2 out-of-scope `_test.go` files — flagged here for audit clarity.
- **Lesson learned for future parallel waves:** Sequential executor on the main working tree (per the dispatch flag `--no-transition` + `sequential_execution` block in the prompt) is NOT a safe boundary when the orchestrator simultaneously runs another plan's executor on the same checkout. The supposed parallelism is unsound without worktrees. This is a process-level finding, not a code defect.

---

**Total deviations:** 2 documented (both clerical / parallel-race; zero code defects)
**Impact on plan:** No functional impact. All 3 tasks completed; success criteria all met by inspection. Commit attribution is the only artifact affected.

## Issues Encountered

- **`goose` CLI not on PATH on this host** — live migration round-trip skipped per plan ("If `GATEWAY_DB_URL` not set, skip this step and validate via compile only"). `sqlc generate` parsing `db/migrations/` to build its type model serves as a syntactic sanity check; semantic correctness will be validated on first CI deploy when `AI_GATEWAY_MIGRATE_ON_BOOT=true` runs goose up.
- **`go vet ./...` reports `undefined: FSM` in `gateway/internal/shed/fsm_test.go`** — this is leftover Plan 05-03 in-flight work (RED-phase test file pending its GREEN implementation in `fsm.go`). `go vet` on my owned scope (`upstreams/...`, `tenants/...`, `db/...`) is clean. This will green once 05-03 lands GREEN.

## Known Stubs

None. Migration 0018 is intentionally docs-only per Gate A (not a stub — operator gate already decided the column needs no DDL). Default tenant cap values (4/2/8) are deliberate per Gate B (operator overrides them).

## Threat Flags

None. The threat register in 05-02-PLAN.md mapped T-05-03 (`parseCircuitConfig` returns zero struct on `json.Unmarshal` error — preserved unchanged) and T-05-04 (operator-supplied invalid `shed_vram_used_mib` — mitigated in Plan 05-07 CLI per the plan's threat model). No new trust boundaries introduced.

## TDD Gate Compliance

N/A — this plan is `type: execute`, not `type: tdd`. No RED/GREEN gate sequence required.

## Self-Check

- [x] Migration files exist (`ls gateway/db/migrations/0016_*.sql 0017_*.sql 0018_*.sql` → 3 files).
- [x] `21504` (MiB) present in 0017 with 3 occurrences (one per tier-0 upstream); no bytes value (`22548578304` only appears in a warning comment).
- [x] `priority_tier ... CHECK ... IN ('S','A','B')` present in 0016.
- [x] WHEN clause of expanded trigger includes `local_inflight_max_{llm,stt,embed}` and `priority_tier`.
- [x] `gateway/internal/db/gen/tenants_admin.sql.go` contains `UpdateTenantShedLimits` (`grep -l` matches).
- [x] `gateway/internal/db/gen/querier.go` contains `UpdateTenantShedLimits` (`grep -l` matches).
- [x] `LocalInflightMaxLlm/Stt/Embed` fields present in `ListTenantsForLoaderRow` and `GetTenantConfigRow`.
- [x] `CircuitConfig` has `ShedInflightMax`, `ShedP95Ms`, `ShedVramUsedMiB`, `ShedArmSeconds`, `ShedRecoverSeconds`, `ShedArm`, `ShedRecover` (7 fields, grep returned 9 lines including doc references).
- [x] `parseCircuitConfig` derives `ShedArm`/`ShedRecover` from seconds.
- [x] `TenantConfig` has `LocalInflightMaxLLM`/`STT`/`Embed` + `PriorityTier`.
- [x] `Loader.Refresh` populates the 4 new fields from sqlc row.
- [x] Commits `f2abbfb` and `e5ad140` exist in `git log`.
- [x] `go build ./gateway/internal/upstreams/... ./gateway/internal/tenants/... ./gateway/internal/db/...` exit 0.
- [x] `go vet` on owned scope exit 0.

**Self-Check: PASSED**

## Next Phase Readiness

Plan 05-02 unblocks the rest of Wave 1+ for Phase 5:

- **Plan 05-03** (shed package core: FSM/LatencyRing/InflightRegistry/Set) can now consume `upstreams.CircuitConfig.ShedInflightMax`/`ShedP95Ms`/`ShedVramUsedMiB`/`ShedArm`/`ShedRecover` from the runtime snapshot and per-tenant caps from `tenants.TenantConfig.LocalInflightMax{LLM,STT,Embed}`.
- **Plan 05-04** (dcgm.Scraper) is unblocked because the MiB unit is now canonical in the schema and parsed correctly in `parseCircuitConfig` — the scraper just exposes `gateway_vram_used_mib` and the FSM consumes both via composite 2-of-3.
- **Plan 05-07** (gatewayctl shed CLI) can wire `q.UpdateTenantShedLimits` (sqlc method now exists) for `tenant set-shed-limits` and use `circuit_config` JSONB merge for `thresholds set`.
- **Plan 05-08** (integration tests) can round-trip the audit `upstream` strings `shed_saturated`/`shed_blocked_sensitive`/`shed_tier1_unavailable` (column is unconstrained TEXT per Gate A).

No blockers.

---
*Phase: 05-load-shedding-saturation-aware-routing*
*Completed: 2026-05-11*
