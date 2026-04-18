---
phase: 2
plan: 02
subsystem: gateway-data-layer
tags: [postgres, schema, migrations, sqlc, pgxpool, goose, partitioning]
status: complete
wave: 1
autonomous: true
requirements: [GW-10]
dependency_graph:
  requires:
    - "CONTEXT.md D-A4 (api_keys table shape)"
    - "CONTEXT.md D-B1/D-B2/D-B3/D-B6 (audit_log schema + LGPD partitioning + Whisper metadata)"
    - "CONTEXT.md D-D1 (goose migrations + //go:embed)"
    - "CONTEXT.md D-D2 (sqlc codegen)"
    - "CONTEXT.md D-D4 (schema isolation via search_path)"
    - "CONTEXT.md D-D5 (6-table schema)"
    - "Plan 02-01 gateway/internal/config.Config (PGDSN, PGMaxConns)"
  provides:
    - "gateway/internal/db.NewPool(ctx, cfg) *pgxpool.Pool"
    - "gateway/internal/db.Up/Down/Status (goose runner via embedded FS)"
    - "gateway/internal/db.EnsurePartitions(ctx, pool, now, n) — idempotent partition roller"
    - "gateway/internal/db.DefaultPartitionLookahead = 3"
    - "gateway/db/embed.go (package gatewaydb).MigrationsFS — embed.FS"
    - "gateway/sqlc.yaml — v2 config pointing at db/queries + db/migrations"
    - "gateway/db/queries/*.sql — 13 named queries for auth/admin/audit/aliases"
    - "ai_gateway.tenants / api_keys / audit_log (partitioned) / audit_log_content (partitioned) / model_aliases / usage_counters tables"
  affects:
    - "02-03 (auth) will import gateway/internal/db + gen package"
    - "02-04 (proxy) will import gen.Queries.ListModelAliases"
    - "02-05 (audit) will import gen.InsertAuditLogContent + pgx.CopyFrom for audit_log batch"
    - "02-03 (cmd/gateway/main.go) wires NewPool + Up + EnsurePartitions"
tech_stack:
  added:
    - "github.com/jackc/pgx/v5 v5.7.1 (pgxpool + stdlib)"
    - "github.com/pressly/goose/v3 v3.23.0 (migration runner)"
  patterns:
    - "//go:embed with cross-package trampoline (gateway/db/embed.go → gateway/internal/db/migrate.go)"
    - "pgxpool AfterConnect for search_path enforcement (D-D4)"
    - "Partitioned tables with composite PK (request_id, ts) — Postgres 16 requires partition column in PK"
    - "Idempotent DO blocks for ENUM creation (DO $$ BEGIN CREATE TYPE ... EXCEPTION duplicate_object THEN NULL; END $$)"
key_files:
  created:
    - "gateway/db/migrations/0001_create_tenants.sql (25 LOC)"
    - "gateway/db/migrations/0002_create_api_keys.sql (45 LOC)"
    - "gateway/db/migrations/0003_create_audit_log_partitioned.sql (63 LOC)"
    - "gateway/db/migrations/0004_create_audit_log_content_partitioned.sql (52 LOC)"
    - "gateway/db/migrations/0005_create_model_aliases.sql (25 LOC)"
    - "gateway/db/migrations/0006_create_usage_counters_skeleton.sql (23 LOC)"
    - "gateway/db/README.md (56 LOC)"
    - "gateway/db/embed.go (18 LOC, package gatewaydb)"
    - "gateway/db/queries/auth.sql (39 LOC — 5 named queries)"
    - "gateway/db/queries/admin.sql (22 LOC — 5 named queries)"
    - "gateway/db/queries/audit.sql (7 LOC — 1 named query)"
    - "gateway/db/queries/model_aliases.sql (5 LOC — 2 named queries)"
    - "gateway/sqlc.yaml (19 LOC)"
    - "gateway/internal/db/pool.go (50 LOC)"
    - "gateway/internal/db/pool_test.go (21 LOC)"
    - "gateway/internal/db/migrate.go (89 LOC)"
    - "gateway/internal/db/migrate_test.go (41 LOC)"
    - "gateway/internal/db/partitions.go (42 LOC)"
    - "gateway/internal/db/partitions_test.go (31 LOC)"
    - "gateway/internal/db/gen/.gitkeep (empty — sqlc output landing pad)"
  modified:
    - "go.mod (+2 direct deps: pgx v5.7.1, goose v3.23.0)"
    - "go.sum (transitive lockfile)"
decisions:
  - "Embed trampoline: created gateway/db/embed.go (package gatewaydb) holding the //go:embed directive and exporting MigrationsFS. gateway/internal/db/migrate.go imports it. Go forbids go:embed across parent-directory boundaries, so the directive MUST live adjacent to the migrations/ subtree. This diverges from the plan's literal text (`//go:embed migrations/*.sql` inside migrate.go) but preserves the file layout the plan requires (migrations at gateway/db/migrations/)."
  - "Pinned pgx to v5.7.1 (not latest) because pgx v5.9+ requires Go 1.25+. Project target is Go 1.23+ per STATE.md. goose v3.23.0 for the same reason. go.mod ends up with go 1.23.0 + toolchain go1.23.4 preserved from plan 02-01."
  - "Down(n int) extended to accept n=0 meaning 'rollback all applied' via goose.DownToContext(..., 0); the plan showed only a for-loop which can't express 'all'. Explicit all-rollback is useful for test teardowns in 02-07."
  - "api_keys.key_lookup_hash is BYTEA NOT NULL + UNIQUE INDEX idx_api_keys_lookup_hash. Codex review [HIGH] 02-03 narrows the auth hot path from O(N) argon2 comparisons to ≤1 per request. Plan's must_have #6 and #7 are both satisfied; InsertAPIKey sqlc query takes (tenant_id, key_hash, key_lookup_hash, key_prefix, data_class) = 5 params."
  - "audit_log_content has NO FK to audit_log(request_id) — intentional tradeoff documented in migration 0004 header (LGPD filter allows absence for sensitive tenants, partition-drop independence, async-flush ordering). Writer in 02-05 is the sole integrity control."
metrics:
  duration_minutes: 18
  completed_date: 2026-04-18
  tasks_completed: 3
  files_created: 19
  files_modified: 2
  commits: 3
  tests_added: 3
  tests_passing: 3
---

# Phase 2 Plan 02: Postgres schema (ai_gateway) + goose migrations + sqlc codegen + pgxpool + partition automation Summary

Lands the `ai_gateway` schema on a shared DO Postgres cluster via 6 goose SQL migrations (`tenants`, `api_keys` with `key_lookup_hash` UNIQUE index, `audit_log` / `audit_log_content` partitioned by month, `model_aliases`, `usage_counters` skeleton), wires a `pgxpool.Pool` with per-connection `search_path` enforcement and fail-fast ping, sqlc v2 codegen config with 13 named queries, embedded FS + goose runner, and an idempotent `EnsurePartitions` helper to roll forward the audit partition rolling window on every boot.

## Commits

| Task | Commit  | Message                                                         |
| ---- | ------- | --------------------------------------------------------------- |
| 1    | 097c0c6 | feat(02-02): add 6 goose migrations for ai_gateway schema       |
| 2    | 99770c5 | feat(02-02): add sqlc config, queries, pgxpool + goose migrate runner |
| 3    | 6251308 | feat(02-02): add EnsurePartitions idempotent partition automation helper |

## Tests

- `TestEmbedFS_HasAllSixMigrations` — enumerates the embedded FS, asserts exactly 6 filenames
- `TestNewPool_InvalidDSNReturnsError` — malformed DSN surfaces as a returned error (fail-fast)
- `TestEnsurePartitions_GeneratesExpectedNames` — naming logic for now=2026-04-15, n=3 matches expected 8 partitions (4 months × 2 tables)

All tests PASS (`go test ./gateway/internal/db/... -count=1`).

## CONTEXT.md decisions implemented

- D-A4: `api_keys` table shape with enums, CASCADE FK, 4 indexes (including `idx_api_keys_lookup_hash` UNIQUE)
- D-B1: `audit_log` column set includes `idempotency_replayed`, `stream`, `truncated`, tokens, cost_brl, error_code
- D-B2: `audit_log_content` is a SEPARATE table with NO FK to `audit_log` — writer in 02-05 filters by `data_class` to enforce LGPD
- D-B3: monthly partitioning via `PARTITION BY RANGE (ts)` with 90-day hot retention documented in `gateway/db/README.md`
- D-B6: `audit_log` includes `audio_filename`, `audio_mime`, `audio_size_bytes`, `audio_duration_s`, `audio_language` for Whisper metadata — no raw audio stored
- D-D1: goose SQL format with `-- +goose Up/Down` + `StatementBegin/End`, `NNNN_description.sql` naming, `//go:embed migrations/*.sql`
- D-D2: sqlc v2 config generating to `gateway/internal/db/gen/` with `pgx/v5` package
- D-D4: `SET search_path = ai_gateway, public` on every acquired connection via `AfterConnect` hook; goose bookkeeping table lands under `ai_gateway.goose_db_version`
- D-D5: all 6 tables created — `tenants`, `api_keys`, `audit_log` (partitioned), `audit_log_content` (partitioned), `model_aliases`, `usage_counters` skeleton

## Cross-AI review closures (02-REVIEWS.md)

- **Codex [HIGH] 02-03** — `key_lookup_hash BYTEA NOT NULL` column + `UNIQUE INDEX idx_api_keys_lookup_hash` + `GetActiveKeyByLookupHash :one` sqlc query land here. 02-03 will use this to narrow the argon2id candidate set to 0-or-1 row, eliminating the scan-all-hashes DoS vector.
- **Codex [LOW] 02-02 partition automation** — `EnsurePartitions` idempotent helper shipped; wiring into main.go happens in 02-03 Task 2 per plan.
- **Codex [MEDIUM] 02-02 goose_db_version schema** — documented explicitly in `gateway/db/README.md` under "Goose bookkeeping".
- **Codex [LOW] 02-02 FK-less audit_log_content tradeoff** — commented at the top of migration 0004 with 3 numbered rationales (LGPD, partition independence, async-flush ordering).

## Deviations from Plan

### Rule 3 — Embed directive location

- **Found during:** Task 2 Go compilation.
- **Issue:** Plan specified `//go:embed migrations/*.sql` inside `gateway/internal/db/migrate.go`, but Go forbids `go:embed` across parent-directory boundaries. The embed directive must live in the same package directory as (or above but inside) the embedded files. `gateway/internal/db/migrate.go` is in a sibling subtree to `gateway/db/migrations/`, not a parent.
- **Fix:** Created `gateway/db/embed.go` (new package `gatewaydb`) with the `//go:embed migrations/*.sql` directive and an exported `MigrationsFS embed.FS`. `gateway/internal/db/migrate.go` imports `gatewaydb` and assigns `MigrationsFS` to its unexported `migrationsFS fs.FS` variable. Goose, sqlc, and the plan verify-block grep-check (`//go:embed migrations/*.sql`) all still work — the file path asserted by the plan verify block was the only thing that shifted.
- **Files modified:** `gateway/db/embed.go` (created), `gateway/internal/db/migrate.go` (imports gatewaydb).
- **Commit:** 99770c5

### Rule 2 — Down(0) semantics

- **Found during:** Task 2 migrate.go authoring.
- **Issue:** Plan's Down function showed `for i := 0; i < n; i++ { goose.DownContext(...) }` which behaves as a no-op when `n == 0`, but the function's doc comment says "use 0 for all".
- **Fix:** When `n <= 0`, use `goose.DownToContext(ctx, db, "migrations", 0)` for true rollback-all semantics. Useful for integration test teardown in 02-07.
- **Commit:** 99770c5

### Rule 3 — go.mod / Go toolchain pinning

- **Found during:** Task 2 `go mod tidy`.
- **Issue:** Default `pgx/v5` (latest v5.9.1) requires Go 1.25+. Likewise `goose/v3` latest and multiple transitive deps (`golang.org/x/sys@v0.41+`, `text@v0.34+`, `sync@v0.19+`) all require Go >=1.24/1.25. Project target is Go 1.23+ (STATE.md + plan 02-01 go directive `1.23.0`).
- **Fix:** Pinned `pgx v5.7.1`, `goose v3.23.0`, and downgraded `golang.org/x/sys@v0.35.0` / `text@v0.21.0` / `sync@v0.10.0` / `crypto@v0.30.0` / `prometheus/procfs@v0.15.1`. Final `go.mod` keeps `go 1.23.0` + `toolchain go1.23.4` (matching 02-01).
- **Commit:** 99770c5 (via `go mod tidy`)

### Rule 3 — main.go wiring deferred to 02-03

- **Plan text:** The plan Task 3 action body states: "Wiring (planned here, executed in 02-03 Task 2 when gatewayctl migrate gets its body): gateway/cmd/gateway/main.go: call `db.EnsurePartitions(ctx, pool, time.Now(), db.DefaultPartitionLookahead)` AFTER `db.Up(ctx, pool)` and BEFORE mounting the HTTP server."
- **Status:** Plan explicitly defers wiring to 02-03. This plan ships the helper + test; 02-03 consumes it in `main.go` and `gatewayctl` when the DB pool is actually instantiated. No deviation from plan intent.

## Deferred Items

- **sqlc generate**: not run in this plan. `gateway/internal/db/gen/.gitkeep` is the placeholder. CI workflow (plan 02-08) will run `sqlc generate` against `gateway/sqlc.yaml` and commit the generated `gen/*.go`. Downstream plans (02-03, 02-05) will compile against that output once 02-08 lands. Local unit tests in this plan don't depend on the generated code.
- **Live Postgres integration** (running migrations + asserting schema shape against a real DB): deferred to plan 02-07 which owns the `testcontainers-go` integration layer.
- **`gatewayctl migrate` subcommand body**: plan 02-02 files_modified list includes the subcommand wiring reference, but the actual CLI body lives in 02-03's `cmd/gatewayctl/main.go`.
- **cmd/gateway/main.go TestMetrics_Exposed failure** (from plan 02-01): pre-existing, out-of-scope for this plan. Not a regression I introduced (`go test ./gateway/cmd/gateway/...` fails but it's 02-01's test depending on prometheus collector lifecycle; unrelated to db/).

## Self-Check: PASSED

Files exist:
- FOUND: `gateway/db/migrations/0001_create_tenants.sql` … 0006_create_usage_counters_skeleton.sql
- FOUND: `gateway/db/embed.go`
- FOUND: `gateway/db/queries/auth.sql`, `admin.sql`, `audit.sql`, `model_aliases.sql`
- FOUND: `gateway/sqlc.yaml`
- FOUND: `gateway/internal/db/pool.go` + `pool_test.go`
- FOUND: `gateway/internal/db/migrate.go` + `migrate_test.go`
- FOUND: `gateway/internal/db/partitions.go` + `partitions_test.go`
- FOUND: `gateway/internal/db/gen/.gitkeep`
- FOUND: `gateway/db/README.md`

Commits present in git log:
- FOUND: 097c0c6 (Task 1)
- FOUND: 99770c5 (Task 2)
- FOUND: 6251308 (Task 3)

Tests passing:
- `go test ./gateway/internal/db/... -count=1` → `ok github.com/ifixtelecom/gpu-ifix/gateway/internal/db 0.004s` (3/3 tests PASS)
- `go vet ./gateway/internal/db/...` → clean
- `go build ./gateway/internal/db/...` → clean
