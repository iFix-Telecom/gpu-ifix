---
phase: 04-multi-tenant-quotas-billing-schedule-routing
plan: 02
subsystem: database
tags: [phase-04, schema, migrations, sqlc, billing, quota, tenants, prices, fx, admin-keys, postgres, goose]

# Dependency graph
requires:
  - phase: 04-multi-tenant-quotas-billing-schedule-routing/04-01
    provides: "Package sentinels (quota/billing/schedule/admin/tenants errors.go) + operator gate A1/A2/A5 closed"
provides:
  - "5 goose migrations 0010..0014 establishing Phase 4 schema foundation"
  - "6 sqlc query files + regenerated internal/db/gen/*.go Go bindings"
  - "partitioned billing_events table with PK (request_id, ts), idx_billing_events_tenant_ts, 3 monthly partitions seeded"
  - "usage_counters evolved with 4 new columns (audio_seconds, embeds_count, cost_local_phantom_brl, cost_external_brl)"
  - "prices + fx_rates with notify_prices_changed + notify_fx_changed triggers (split INSERT/DELETE vs UPDATE WHEN-clause)"
  - "tenants ALTER adding 14 columns (4 schedule + 8 quota + 2 limits) + chk_sensitive_no_peak CHECK + data_class/status backfill + notify_tenants_changed"
  - "admin_keys with bcrypt key_hash + SHA-256 key_lookup_hash UNIQUE INDEX"
  - "sqlc.yaml explicit queries list including 6 new Phase 4 files"
affects:
  - "04-03 (seed prices + quotas via migration 0015 depends on this schema)"
  - "04-04 (quota enforcer reads GetTenantConfig + GetUsageCountersToday)"
  - "04-05 (billing flusher uses InsertBillingEvent CTE)"
  - "04-06 (schedule router reads tenants.mode + peak_window_*)"
  - "04-07 (admin CLI uses admin_keys + UpdateTenantMode/UpdateTenantQuota)"
  - "04-08 (integration tests validate goose up on PG16 + live CTE flush semantics)"
  - "04-09 (/admin/usage aggregates SumBillingEventsByDate)"

# Tech tracking
tech-stack:
  added:
    - "sqlc v1.30.0 codegen for Phase 4 queries (builds on existing Phase 2 setup)"
    - "partitioned Postgres table pattern (billing_events PARTITION BY RANGE (ts)) mirroring 0003 audit_log_partitioned"
    - "NOTIFY/LISTEN hot-reload trigger pattern for 3 config tables (prices, fx_rates, tenants) mirroring 0009 upstreams"
    - "CTE-based atomic insert+upsert pattern (InsertBillingEvent) solving Pitfall 7 replay double-count"
  patterns:
    - "Migration ordering (billing_events -> usage_counters evolve -> prices+fx -> tenants ALTER -> admin_keys): each step self-contained; later migrations depend on earlier schema"
    - "WHEN (pg_trigger_depth() = 0 AND <col IS DISTINCT FROM>) clause to block reload-storms from probe-style writebacks"
    - "sqlc.narg partial-update pattern (UpdateTenantMode/UpdateTenantQuota) for operator-driven partial PATCH semantics"
    - "SHA-256 lookup hash + bcrypt verify (admin_keys) mirrors Phase 2 api_keys for constant-time DB lookup + secure password compare"
    - "Tenant-level data_class column (distinct from Phase 2 api_keys.data_class) drives LGPD invariant chk_sensitive_no_peak"

key-files:
  created:
    - "gateway/db/migrations/0010_create_billing_events.sql - partitioned append-only billing events"
    - "gateway/db/migrations/0011_evolve_usage_counters.sql - 4 new aggregation columns"
    - "gateway/db/migrations/0012_create_prices_and_fx.sql - price + fx history tables with NOTIFY triggers"
    - "gateway/db/migrations/0013_evolve_tenants_schedule_quota.sql - 14 tenant columns + LGPD CHECK + NOTIFY trigger"
    - "gateway/db/migrations/0014_create_admin_keys.sql - bcrypt admin auth table"
    - "gateway/db/queries/billing.sql - InsertBillingEvent CTE + SumBillingEventsBy{Date,Range}"
    - "gateway/db/queries/usage_counters.sql - GetUsageCountersToday/Month + ResetUsageCountersForReconcile"
    - "gateway/db/queries/prices.sql - ListActivePrices/ListAllPrices/ExpireActivePrice/InsertPrice"
    - "gateway/db/queries/fx_rates.sql - GetCurrentFX/ListAllFX/ExpireActiveFX/InsertFX"
    - "gateway/db/queries/tenants_admin.sql - GetTenantConfig/ListTenantsForLoader/UpdateTenantMode/UpdateTenantQuota/CountSensitivePeakInvariant"
    - "gateway/db/queries/admin_keys.sql - GetAdminKeyByLookupHash + 5 admin key ops"
    - "gateway/internal/db/gen/billing.sql.go, usage_counters.sql.go, prices.sql.go, fx_rates.sql.go, tenants_admin.sql.go, admin_keys.sql.go - sqlc-generated Go bindings"
  modified:
    - "gateway/sqlc.yaml - explicit queries list with 11 files (5 existing + 6 new)"
    - "gateway/internal/db/gen/admin.sql.go, models.go, querier.go - regenerated with new types + interface additions"

key-decisions:
  - "DO-block DDL replaced by plain ALTER TABLE ADD COLUMN IF NOT EXISTS (migration 0013) because sqlc's static analyzer cannot see columns added inside DO-blocks; PG runtime behavior is identical since IF NOT EXISTS is idempotent since PG 9.6"
  - "tenants.data_class (tenant-level) added defensively in 0013 distinct from api_keys.data_class (per-key) from Phase 2 migration 0002 -- Phase 4 LGPD model is tenant-scoped per D-C1"
  - "tenants.status column added defensively in 0013 to support ListTenantsForLoader WHERE status = 'active' filter; kept in Down migration to avoid cascading data loss on re-ups"
  - "sqlc.yaml moved from 'queries: db/queries' (directory mode) to explicit list so plan-driven additions are visible in version control and new files must be consciously registered"
  - "InsertBillingEvent written as CTE (WITH inserted AS (INSERT ... ON CONFLICT DO NOTHING RETURNING) INSERT ... SELECT FROM inserted) so ON CONFLICT returning 0 rows auto-skips the usage_counters UPSERT -- solves Pitfall 7 replay double-count without explicit application txn"

patterns-established:
  - "Phase 4 partition seeding: DO-block loop i IN 0..2 creates current + 2 future monthly partitions; later plan (scheduled partition rollover) extends this"
  - "Hot-reload trigger triad for config tables: notify_<table>_changed function + <table>_insert_delete_notify trigger (always fire) + <table>_update_notify trigger (WHEN pg_trigger_depth()=0 AND <config IS DISTINCT FROM>) -- applied to prices, fx_rates, tenants"
  - "Timezone idiom for per-day counters: (now() AT TIME ZONE 'America/Sao_Paulo')::date -- the alternative CURRENT_DATE + tz form is invalid SQL (documented in RESEARCH Anti-Patterns); every Phase 4 query uses the correct form"

requirements-completed:
  - TEN-03
  - TEN-04
  - TEN-05
  - TEN-06
  - TEN-07

# Metrics
duration: 24min
completed: 2026-04-21
---

# Phase 04 Plan 02: Phase 4 Schema + sqlc Codegen Foundation Summary

**5 goose migrations (partitioned billing_events, usage_counters columns, prices+fx NOTIFY triggers, tenants 14-column ALTER with LGPD CHECK, bcrypt admin_keys) + 6 sqlc query files producing InsertBillingEvent CTE, GetUsageCountersToday PK read, ListActivePrices/GetCurrentFX hot-reload sources, GetAdminKeyByLookupHash, and UpdateTenantMode/UpdateTenantQuota partial-update queries -- all compiling via `sqlc generate` and applying cleanly on PG16 testcontainer in 4.7s.**

## Performance

- **Duration:** ~24 min
- **Started:** 2026-04-21T09:12:00Z
- **Completed:** 2026-04-21T09:36:00Z
- **Tasks:** 3 (all `type=auto`, no checkpoints)
- **Files created:** 12 (5 migrations + 6 query files + 1 defensive column ADD in 0013; 6 new gen/*.go auto-generated)
- **Files modified:** 4 (sqlc.yaml + 3 regenerated existing gen/*.go)

## Accomplishments

- All 5 migrations (0010, 0011, 0012, 0013, 0014) apply cleanly via goose on a fresh PG16 testcontainer and pass the full down(1)/up idempotency cycle tested by TestIntegration_01_Migrate (4.7s wall time).
- sqlc v1.30.0 generates 6 new `.go` files in `gateway/internal/db/gen/` with 8 key hot-path bindings (InsertBillingEvent, GetUsageCountersToday, ListActivePrices, GetCurrentFX, GetTenantConfig, UpdateTenantQuota, CountSensitivePeakInvariant, GetAdminKeyByLookupHash) plus ~20 supporting queries.
- `go build ./...` and `go vet ./...` on the entire gateway module pass cleanly -- no downstream breakage from schema additions or new gen types.
- Pitfall 7 (replay double-count) solved via CTE form of InsertBillingEvent: when `ON CONFLICT (request_id, ts) DO NOTHING` fires, the CTE returns 0 rows and the downstream `INSERT INTO usage_counters` no-ops automatically without needing an application transaction.
- LGPD invariant chk_sensitive_no_peak installed at DB layer as path 2 of the D-C1 triple-defense (CLI pre-check + DB CHECK + boot-time CountSensitivePeakInvariant).
- NOTIFY trigger reload-storm defense: all 3 new config tables (prices, fx_rates, tenants) use the Pitfall 7 pattern of splitting INSERT/DELETE triggers (always fire) from UPDATE triggers (WHEN `pg_trigger_depth()=0` AND column-specific DISTINCT-FROM clause).

## Task Commits

Each task was committed atomically (all `--no-verify` per parallel-executor protocol):

1. **Task 1: Migrations 0010, 0011, 0012, 0014** - `0ba23aa` (feat) - billing_events partitioned table + usage_counters 4-column evolve + prices/fx tables with NOTIFY triggers + admin_keys table
2. **Task 2: Migration 0013 + sqlc.yaml** - `8a8983c` (feat) - tenants ALTER (14 columns including defensive data_class/status) + chk_sensitive_no_peak CHECK + notify_tenants_changed trigger + sqlc.yaml explicit queries list
3. **Task 3: 6 sqlc query files + codegen + 0013 Rule 1 fix** - `90db2b2` (feat) - all query files + DO-block→plain ALTER fix in 0013 to let sqlc see the data_class/status columns + sqlc generate regenerated all gen/*.go

## Files Created/Modified

### Migrations (5 new files)
- `gateway/db/migrations/0010_create_billing_events.sql` - partitioned append-only billing events, 16 columns, PK (request_id, ts), idx_billing_events_tenant_ts, 3 monthly partitions seeded via DO-block
- `gateway/db/migrations/0011_evolve_usage_counters.sql` - ADD COLUMN IF NOT EXISTS for audio_seconds, embeds_count, cost_local_phantom_brl, cost_external_brl with column comments
- `gateway/db/migrations/0012_create_prices_and_fx.sql` - prices + fx_rates history tables (UNIQUE constraints on (model, provider, unit, valid_from) and (currency_pair, valid_from)), partial indexes ON (...) WHERE valid_to IS NULL, 2 notify functions + 4 triggers (split INSERT/DELETE vs UPDATE WHEN-clause)
- `gateway/db/migrations/0013_evolve_tenants_schedule_quota.sql` - 14 ADD COLUMN IF NOT EXISTS on ai_gateway.tenants (4 schedule + 8 quota + 2 limits per plan + 2 defensive data_class/status per Rule 2), chk_sensitive_no_peak CHECK constraint, notify_tenants_changed function + 2 triggers
- `gateway/db/migrations/0014_create_admin_keys.sql` - admin_keys table with bcrypt key_hash TEXT, SHA-256 key_lookup_hash BYTEA UNIQUE INDEX, status='active'|'revoked' CHECK, label, created_at/revoked_at/last_used_at timestamps

### Query files (6 new files)
- `gateway/db/queries/billing.sql` - InsertBillingEvent (CTE Pitfall 7 pattern), SumBillingEventsByDate, SumBillingEventsRange
- `gateway/db/queries/usage_counters.sql` - GetUsageCountersToday (PK read, correct timezone idiom), GetUsageCountersMonth (date_trunc aggregation), ResetUsageCountersForReconcile (idempotent UPSERT)
- `gateway/db/queries/prices.sql` - ListActivePrices, ListAllPrices, ExpireActivePrice, InsertPrice (sqlc.narg for notes)
- `gateway/db/queries/fx_rates.sql` - GetCurrentFX, ListAllFX, ExpireActiveFX, InsertFX
- `gateway/db/queries/tenants_admin.sql` - GetTenantConfig (hot-path PK), ListTenantsForLoader (boot + NOTIFY), UpdateTenantMode + UpdateTenantQuota (sqlc.narg partial-update), CountSensitivePeakInvariant (D-C1 path 3)
- `gateway/db/queries/admin_keys.sql` - GetAdminKeyByLookupHash (hot-path verify), InsertAdminKey, RevokeAdminKey, ListAdminKeys, TouchAdminKeyLastUsed, CountActiveAdminKeys (boot-time bootstrap)

### sqlc codegen (6 new + 3 regenerated gen/*.go)
- `gateway/internal/db/gen/billing.sql.go` - NEW: InsertBillingEvent, SumBillingEventsByDate, SumBillingEventsRange Go bindings with typed params
- `gateway/internal/db/gen/usage_counters.sql.go` - NEW: 3 Go bindings
- `gateway/internal/db/gen/prices.sql.go` - NEW: 4 Go bindings + AiGatewayPrice model
- `gateway/internal/db/gen/fx_rates.sql.go` - NEW: 4 Go bindings + AiGatewayFxRate model
- `gateway/internal/db/gen/tenants_admin.sql.go` - NEW: 5 Go bindings with custom GetTenantConfigRow/ListTenantsForLoaderRow structs for the full Phase 4 column set
- `gateway/internal/db/gen/admin_keys.sql.go` - NEW: 6 Go bindings + AiGatewayAdminKey model
- `gateway/internal/db/gen/models.go` - MODIFIED: 3 new struct types (AiGatewayPrice, AiGatewayFxRate, AiGatewayAdminKey) + AiGatewayUsageCounter and AiGatewayTenant field expansions
- `gateway/internal/db/gen/querier.go` - MODIFIED: Querier interface gains ~20 new method signatures

### Config
- `gateway/sqlc.yaml` - MODIFIED: replaced `queries: db/queries` directory mode with explicit list of 11 files (5 existing + 6 new Phase 4)

## Decisions Made

- **DO-block -> ALTER TABLE ADD COLUMN IF NOT EXISTS for data_class/status (migration 0013):** The initial plan used DO-blocks to defensively add `data_class` and `status` columns conditional on their absence. sqlc's static analyzer does not parse DO-block bodies, so it did not see those columns and rejected every SELECT that referenced them with `column "data_class" does not exist`. PG has supported `ADD COLUMN IF NOT EXISTS` since 9.6 (it is idempotent at runtime), so switching to the plain form is behaviorally identical and gives sqlc the schema visibility it needs.
- **tenants.data_class distinct from api_keys.data_class:** Phase 2 migration 0002 already defines `ai_gateway.data_class` ENUM and puts it on `api_keys`. Phase 4's D-C1 LGPD model treats data classification as *tenant-scoped* (all keys of a sensitive tenant inherit sensitive); the `tenants.data_class` column is the one referenced by `chk_sensitive_no_peak`. The two columns coexist -- api_keys.data_class (if ever reused) now carries Phase-2 legacy semantics.
- **sqlc.yaml explicit queries list:** The previous config used `queries: db/queries` as a directory, so sqlc auto-picked up every `.sql` file. Switching to an explicit list forces every new query file to be consciously registered (which is what the plan documents). This also surfaces accidental file deletions in review diffs. 5 prior files + 6 new = 11 total; all compile.
- **Down migrations for 0013 leave data_class + status in place:** To avoid cascading data loss if an operator later re-ups, the defensively-added columns stay even on a full down. The documented plan columns (mode, peak_window_*, schedule_timezone, 6 quota dims, rps/rpm_limit) are dropped. This is consistent with the 0013 Up IF NOT EXISTS idempotency.
- **billing_events PK = (request_id, ts) not just request_id:** PARTITION BY RANGE (ts) requires ts in the primary key because every unique index on a partitioned table must include the partition key (PG enforces this). Idempotent inserts use `ON CONFLICT (request_id, ts) DO NOTHING`; since ts is always computed at the call site, uniqueness semantics remain per-request_id in practice.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 2 - Missing Critical] Added defensive `data_class` and `status` columns to tenants in migration 0013**

- **Found during:** Task 3 (sqlc generate)
- **Issue:** Plan's Task 3 query file `tenants_admin.sql` selects `data_class` and has `WHERE status = 'active'` on ai_gateway.tenants, and migration 0013's `chk_sensitive_no_peak` references `data_class`. But ai_gateway.tenants (created in 0001) only has id/slug/name/timestamps -- neither column exists. Phase 2's `data_class` lives on api_keys, not tenants. Without these columns: (a) CHECK constraint fails with "column data_class does not exist", (b) ListTenantsForLoader WHERE status='active' fails, (c) sqlc generate rejects every SELECT referencing these columns.
- **Fix:** Added `ALTER TABLE ai_gateway.tenants ADD COLUMN IF NOT EXISTS data_class ai_gateway.data_class NOT NULL DEFAULT 'normal'` and `ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled'))` in 0013 Up. Left them in Down (they are defensive-added; dropping cascades data loss).
- **Files modified:** gateway/db/migrations/0013_evolve_tenants_schedule_quota.sql
- **Verification:** sqlc generate exits 0; TestIntegration_01_Migrate PASS on PG16 testcontainer; chk_sensitive_no_peak CHECK now has both columns to reference.
- **Committed in:** 8a8983c (Task 2 commit, with follow-up fix to DO-block form in Task 3 commit 90db2b2)

**2. [Rule 1 - Bug] Replaced DO-block conditional ADD COLUMN with plain ADD COLUMN IF NOT EXISTS**

- **Found during:** Task 3 (first `sqlc generate` attempt)
- **Issue:** My initial implementation of the data_class/status defensive ADDs used DO-blocks (`DO $$ BEGIN IF NOT EXISTS ... THEN ALTER TABLE ... END $$;`). sqlc's static SQL analyzer does not enter DO-block bodies, so it did not register data_class/status as columns of ai_gateway.tenants. Result: sqlc generate failed with `db/queries/tenants_admin.sql:3:24: column "data_class" does not exist` on every SELECT referencing them.
- **Fix:** Converted both DO-blocks to plain `ALTER TABLE ai_gateway.tenants ADD COLUMN IF NOT EXISTS <col>`. PG 9.6+ supports IF NOT EXISTS natively and is idempotent, so runtime behavior is identical (both forms no-op if column exists). sqlc now sees the columns.
- **Files modified:** gateway/db/migrations/0013_evolve_tenants_schedule_quota.sql
- **Verification:** sqlc generate exits 0; go build ./internal/db/gen/... exits 0; all 8 key Go bindings generate; TestIntegration_01_Migrate PASS.
- **Committed in:** 90db2b2 (Task 3 commit; folded in with the query files since the DO-block issue surfaced only when sqlc first ran against the queries)

**3. [Rule 1 - Bug] Removed "CURRENT_DATE AT TIME ZONE" reference from billing.sql comment**

- **Found during:** Task 3 acceptance-criteria grep check
- **Issue:** The plan's verbatim InsertBillingEvent template included an explanatory comment noting that the invalid form `CURRENT_DATE AT TIME ZONE ...` should NOT be used. The plan's acceptance grep (`grep -c "CURRENT_DATE AT TIME ZONE" gateway/db/queries/ returns 0`) matches inside SQL comments too, so the comment self-triggered the grep. The actual SQL (runtime path) was correct.
- **Fix:** Reworded the comment to reference the anti-pattern without repeating the literal token sequence. Zero behavioral change; purely a documentation tweak to satisfy the acceptance grep.
- **Files modified:** gateway/db/queries/billing.sql
- **Verification:** `grep -rc "CURRENT_DATE AT TIME ZONE" gateway/db/queries/` returns 0 matches across all files.
- **Committed in:** 90db2b2 (Task 3 commit)

---

**Total deviations:** 3 auto-fixed (1 Rule 2 missing critical, 2 Rule 1 bug/blocking)
**Impact on plan:** All three auto-fixes are essential: without (1) the migration and queries cannot coexist; without (2) sqlc codegen fails entirely; (3) is a cosmetic acceptance-grep fix. Zero scope creep — no new queries, migrations, or runtime code outside the documented plan.

## Issues Encountered

- goose/sqlc binaries not on default PATH: both live at /home/pedro/go/bin/sqlc and need `export PATH=...:/home/pedro/go/bin:$PATH` in every Bash invocation. Documented in parallel_execution notes; addressed by exporting PATH in each relevant command. Migration application was verified via `go test -tags=integration -run TestIntegration_01_Migrate` which uses goose-as-library (no binary needed).

## User Setup Required

None - schema additions are applied automatically by boot-time migrations (`AI_GATEWAY_MIGRATE_ON_BOOT=true` per Phase 2 decision). No environment variables or external service configuration needed at this plan boundary.

## Next Phase Readiness

- **Plan 04-03 (seed migration 0015):** Ready -- depends only on prices/fx_rates/tenants schema which is now in place. Operator gate A1/A2 values from Plan 04-01 feed directly into the seed INSERTs.
- **Wave 2 plans (04-04 quota enforcer, 04-05 billing flusher, 04-06 schedule router):** All three can now `import "gateway/internal/db/gen"` and call the generated `q.GetTenantConfig(...)`, `q.GetUsageCountersToday(...)`, `q.InsertBillingEvent(...)`, `q.ListActivePrices(...)`, etc. No further schema blockers.
- **Plan 04-08 integration tests:** TestIntegration_01_Migrate already validates goose-up/down/up on 14 migrations including the 5 new ones; more targeted tests for InsertBillingEvent CTE semantics, chk_sensitive_no_peak rejection, and NOTIFY delivery will live in 04-08.

## Self-Check: PASSED

Verified by Read/Bash at end of execution:

- 5 migration files exist under gateway/db/migrations/:
  - 0010_create_billing_events.sql
  - 0011_evolve_usage_counters.sql
  - 0012_create_prices_and_fx.sql
  - 0013_evolve_tenants_schedule_quota.sql
  - 0014_create_admin_keys.sql
- 6 query files exist under gateway/db/queries/: billing.sql, usage_counters.sql, prices.sql, fx_rates.sql, tenants_admin.sql, admin_keys.sql
- 6 new gen/*.go files exist under gateway/internal/db/gen/: billing.sql.go, usage_counters.sql.go, prices.sql.go, fx_rates.sql.go, tenants_admin.sql.go, admin_keys.sql.go
- Commits 0ba23aa, 8a8983c, 90db2b2 present in `git log` between base bfde4b3 and HEAD
- `sqlc generate` exits 0; `go build ./...` exits 0; `go vet ./...` exits 0
- TestIntegration_01_Migrate PASS (4.751s) on PG16 testcontainer

---
*Phase: 04-multi-tenant-quotas-billing-schedule-routing*
*Plan: 02*
*Completed: 2026-04-21*
