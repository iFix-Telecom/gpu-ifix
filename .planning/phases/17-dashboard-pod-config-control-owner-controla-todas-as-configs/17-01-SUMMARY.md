---
phase: 17-dashboard-pod-config-control-owner-controla-todas-as-configs
plan: 01
subsystem: database
tags: [postgres, sqlc, goose, listen-notify, pgx, migration, pod-config]

# Dependency graph
requires:
  - phase: 06.6-primary-pod-refactor
    provides: "ai_gateway.primary_lifecycles + NOTIFY trigger idiom (0009/0023) reused as the migration template"
provides:
  - "ai_gateway.pod_config single-row table (16 hot fields + 10 numeric min/max bound pairs)"
  - "pod_config_changed NOTIFY trigger (split insert/delete + IS-DISTINCT-FROM-gated update)"
  - "gen.AiGatewayPodConfig model + GetPodConfig/SeedPodConfig/UpdatePodConfigField*(16)/UpdatePodConfigBound*(20) typed queries"
  - "Idempotent env→DB seed contract (ON CONFLICT DO NOTHING) proven by integration test"
affects: [17-02-podconfig-listen, 17-03-loader-seed, 17-04-admin-write-endpoint, 17-05-reconciler-refactor, 17-06-dashboard-config-page]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Single-row config table via boolean PK DEFAULT TRUE CHECK (id = TRUE)"
    - "Per-column UpdatePodConfigField/Bound queries (one column per query → clean dashboard audit diff)"
    - "Bound columns (*_min/*_max) co-located in the same row as the values they gate (D-03)"

key-files:
  created:
    - gateway/db/migrations/0031_create_pod_config.sql
    - gateway/db/queries/pod_config.sql
    - gateway/internal/db/gen/pod_config.sql.go
    - gateway/internal/integration_test/pod_config_test.go
  modified:
    - gateway/sqlc.yaml
    - gateway/internal/db/gen/models.go
    - gateway/internal/db/gen/querier.go
    - gateway/internal/db/migrate_test.go
    - gateway/internal/integration_test/setup_test.go

key-decisions:
  - "All hot + bound columns NOT NULL (the Go-side seed always provides every value; no nullable config knobs)"
  - "10 numeric bound pairs (the plan's interfaces list) — host_id gets no bound columns (min 0 is a validation constant, not stored)"
  - "Generated model is gen.AiGatewayPodConfig (schema-qualified, matching the existing AiGateway* convention) — NOT gen.PodConfig as PATTERNS loosely wrote"

patterns-established:
  - "Single-row singleton table guarded by boolean-PK CHECK (id = TRUE)"
  - "NOTIFY-on-real-change: UPDATE trigger WHEN enumerates every config+bound column IS DISTINCT FROM OLD so idempotent re-saves fire no reload"

requirements-completed: [POD-CFG-01, POD-CFG-02, POD-CFG-05]

# Metrics
duration: 20min
completed: 2026-06-30
---

# Phase 17 Plan 01: pod_config DB Foundation Summary

**Single-row `ai_gateway.pod_config` table (16 hot fields + 10 numeric min/max bound pairs) with a `pod_config_changed` NOTIFY trigger, four sqlc query families, and an integration test proving empty-create + idempotent seed + update.**

## Performance

- **Duration:** ~20 min
- **Started:** 2026-06-30T17:35:00Z
- **Completed:** 2026-06-30T17:54:00Z
- **Tasks:** 2
- **Files modified:** 9 (4 created, 5 modified)

## Accomplishments
- Migration 0031 creates the EMPTY single-row `pod_config` table: boolean-PK `CHECK (id = TRUE)` singleton guard, 16 hot config columns (native types: `bigint[]`/`text[]`/`numeric`/`bigint`/`integer`/`boolean`), and 20 owner-editable bound columns for the 10 numeric fields.
- `pod_config_changed` NOTIFY function + split insert/delete vs update triggers, mirroring `0009_upstreams_notify_trigger.sql`. The UPDATE trigger's WHEN clause enumerates all 36 config+bound columns with `IS DISTINCT FROM OLD` so an idempotent re-save fires no spurious reload (T-17-02).
- sqlc-generated `gen.AiGatewayPodConfig` model + `GetPodConfig` (`:one`), idempotent `SeedPodConfig` (`:exec`, `ON CONFLICT (id) DO NOTHING`), 16 `UpdatePodConfigField*`, and 20 `UpdatePodConfigBound*` typed queries.
- Integration test (testcontainers PG) proves: table created EMPTY → seed populates → second seed is a no-op (idempotent, T-17-01) → `UpdatePodConfigField`/`Bound` mutate and advance `updated_at`. **PASS** under `go test -tags integration`.

## Task Commits

1. **Task 1: Migration 0031 + pod_config.sql queries + sqlc regen** - `354c42c` (feat)
2. **Task 2: Migration 0031 integration test (up/down + idempotent seed)** - `48fbe23` (test)

## Files Created/Modified
- `gateway/db/migrations/0031_create_pod_config.sql` - pod_config table + 20 bound columns + notify_pod_config_changed() + 2 split triggers
- `gateway/db/queries/pod_config.sql` - GetPodConfig, SeedPodConfig (idempotent), 16 UpdatePodConfigField*, 20 UpdatePodConfigBound*
- `gateway/internal/db/gen/pod_config.sql.go` - generated typed queries (sqlc)
- `gateway/internal/db/gen/models.go` - generated AiGatewayPodConfig model (sqlc)
- `gateway/internal/db/gen/querier.go` - generated Querier interface additions (sqlc)
- `gateway/sqlc.yaml` - registered db/queries/pod_config.sql after primary_lifecycles.sql
- `gateway/internal/db/migrate_test.go` - added 0031 to embed-FS want-list (Rule 3)
- `gateway/internal/integration_test/pod_config_test.go` - TestPodConfig_SeedIdempotentAndUpdate
- `gateway/internal/integration_test/setup_test.go` - truncate pod_config in freshSchema (Rule 3)

## Decisions Made
- **Columns NOT NULL:** every hot + bound column is `NOT NULL` because the Go-side seed (Plan 17-03) always supplies all 36 values; pod_config has no optional knobs.
- **10 bound pairs, not 9:** the plan prose says "9 numeric fields" but its `<interfaces>` block explicitly lists 10 (cap_primary, cap_fallback, coldstart_budget_s, port_bind_budget_s, failure_cooldown_s, monthly_budget_brl, schedule_up_hour, schedule_down_hour, grace_ramp_down_s, provision_lead_s). Implemented all 10 (superset; matches the enumerated RESEARCH defaults). `host_id` gets no bound columns per the interfaces parenthetical ("min 0, no max").
- **Model name:** sqlc generated `gen.AiGatewayPodConfig` (schema-qualified, consistent with `AiGatewayPrimaryLifecycle` etc.). PATTERNS/plan referenced `gen.PodConfig` loosely; downstream plans must use `gen.AiGatewayPodConfig`.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Added 0031 to the embedded-migrations want-list**
- **Found during:** Task 1 (sqlc regen + build)
- **Issue:** `gateway/internal/db/migrate_test.go` `TestEmbedFS_HasAllMigrations` asserts an exact hardcoded list + count of embedded migrations. Adding `0031_create_pod_config.sql` would fail that test (count + index mismatch).
- **Fix:** Appended `"0031_create_pod_config.sql"` to the `want` slice.
- **Files modified:** gateway/internal/db/migrate_test.go
- **Verification:** `go build ./...` exits 0; list stays sorted/contiguous.
- **Committed in:** `354c42c` (Task 1 commit)

**2. [Rule 3 - Blocking] Truncate pod_config in the shared integration harness**
- **Found during:** Task 2 (integration test)
- **Issue:** `freshSchema` does not truncate `pod_config`; the testcontainers PG is shared package-wide, so the "table is EMPTY before seed" assertion would be non-deterministic on re-runs or if a future test seeds the row.
- **Fix:** Added `pod_config` to the `freshSchema` truncate loop (parity with the primary/emergency lifecycle tables).
- **Files modified:** gateway/internal/integration_test/setup_test.go
- **Verification:** `go test -tags integration -run TestPodConfig` PASS (empty-before-seed assertion green).
- **Committed in:** `48fbe23` (Task 2 commit)

---

**Total deviations:** 2 auto-fixed (both Rule 3 - blocking)
**Impact on plan:** Both were mechanical test-harness fixes required to keep the suite green after adding a migration. No scope creep; no functional surface beyond the plan.

## Issues Encountered
- Docker socket is not accessible to user `pedro` on this host; the integration test was run via `sudo env PATH=... GOPATH=... go test -tags integration -run TestPodConfig` (testcontainers PG came up, migrated through 0031, test PASSed in 6.5s). No root-owned artifacts left in the worktree.

## User Setup Required
None - no external service configuration required. The migration runs via `db.Up` at gateway boot; the env→DB seed lands in Plan 17-03.

## Next Phase Readiness
- `gen.AiGatewayPodConfig` + the four query families are ready for import by Plan 17-02 (LISTEN/loader), 17-03 (boot seed), 17-04 (admin PATCH write endpoint), and 17-05 (reconciler snapshot reads).
- Downstream note: the model is `gen.AiGatewayPodConfig`, not `gen.PodConfig`.
- NOTIFY delivery itself is asserted in Plan 17-02's listen test (intentionally out of scope here).

---
*Phase: 17-dashboard-pod-config-control-owner-controla-todas-as-configs*
*Completed: 2026-06-30*
