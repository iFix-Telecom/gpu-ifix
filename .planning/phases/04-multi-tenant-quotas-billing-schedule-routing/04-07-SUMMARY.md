---
phase: 04-multi-tenant-quotas-billing-schedule-routing
plan: 07
subsystem: admin-cli
tags: [phase-04, gatewayctl, cli, admin, reconcile, bcrypt, hot-reload, sqlc]

# Dependency graph
requires:
  - phase: 04-02
    provides: sqlc queries (UpdateTenantMode, UpdateTenantQuota, ExpireActivePrice, InsertPrice, ExpireActiveFX, InsertFX, ListActivePrices, ListAllPrices, InsertAdminKey, RevokeAdminKey, ListAdminKeys, ResetUsageCountersForReconcile, SumBillingEventsByDate, SumBillingEventsRange, ListTenantsForLoader, GetTenantConfig, GetUsageCountersToday)
  - phase: 04-05
    provides: admin bcrypt pattern (gateway/internal/admin/middleware.go) — CLI admin-key create mirrors cost 10 + SHA-256 lookup hash shape
  - phase: 02-03
    provides: gateway/cmd/gatewayctl/key.go runKeyCreate print-once pattern — admin_key.go mirrors exactly but with bcrypt instead of argon2id
provides:
  - gatewayctl tenant set-mode (D-C1 path 1: rejects sensitive+peak pre-DB with LGPD-specific error)
  - gatewayctl tenant set-quota (partial UPDATE via pgtype.Int4/Int8 wrappers on -1 sentinel)
  - gatewayctl prices set | list | set-fx (atomic swap in single pgx txn → NOTIFY prices_changed fires once)
  - gatewayctl admin-key create | revoke | list (bcrypt cost 10 hash + print-once raw key)
  - gatewayctl billing reconcile [--apply] (drift > 0.1% alarm; optional authoritative rewrite)
  - gatewayctl usage report --format table|json (per-tenant breakdown; SC-3 shape in json mode)
  - parseWindowHours helper for HH-HH window flag validation
affects: [04-08, 04-09]

# Tech tracking
tech-stack:
  added: []  # All deps already in go.mod (bcrypt, uuid, pgx/v5, pgtype)
  patterns:
    - "Atomic price swap in single pgx txn: ExpireActive* + Insert* inside BeginTx → Commit ensures NOTIFY trigger fires exactly once"
    - "Partial UPDATE via -1 flag sentinel → pgtype.Int4/Int8{Valid: true} wrapper → sqlc.narg COALESCE"
    - "bcrypt cost 10 + SHA-256 lookup hash mirrors gateway/internal/admin/middleware.go so CLI-created keys verify identically"
    - "Print-once raw key to stdout + log only id/prefix/label (extends Phase 2 D-A3 key.go pattern)"
    - "Defense in depth for sensitive+peak: CLI pre-DB rejection (path 1) → CHECK constraint (path 2) → boot-time CountSensitivePeakInvariant (path 3)"

key-files:
  created:
    - gateway/cmd/gatewayctl/prices.go (252 lines; 3 subcommands + dispatcher)
    - gateway/cmd/gatewayctl/admin_key.go (280 lines; 3 subcommands + dispatcher + bcryptCost constant)
    - gateway/cmd/gatewayctl/billing.go (456 lines; reconcile + usage report + scheduleTZ constant)
    - gateway/cmd/gatewayctl/tenant_test.go (43 lines; TestParseWindowHours 10 cases)
    - gateway/cmd/gatewayctl/prices_test.go (placeholder; integration in 04-08)
    - gateway/cmd/gatewayctl/admin_key_test.go (placeholder; integration in 04-08)
    - gateway/cmd/gatewayctl/billing_test.go (placeholder; integration in 04-08)
  modified:
    - gateway/cmd/gatewayctl/main.go (dispatcher + usage text: 4 new cases — prices, billing, usage, admin-key)
    - gateway/cmd/gatewayctl/tenant.go (extended with runTenantSetMode, runTenantSetQuota, parseWindowHours, dataClassString; preserved runTenantCreate)

key-decisions:
  - "Reconcile cache comparison limited to TODAY (usage_counters sqlc only exposes GetUsageCountersToday); historical days are reported as 'cache unavailable' rather than silently skipped — 04-08 may add GetUsageCountersForDay if integration tests need historical reconcile"
  - "CLI rejects sensitive+peak BEFORE issuing any UPDATE (D-C1 path 1) — message explicitly names LGPD policy so operator understands the block is intentional, not a transient failure"
  - "prices set + set-fx use single pgx txn (BeginTx/Commit) so NOTIFY prices_changed fires exactly once on commit; Rollback on InsertPrice failure preserves the prior active row"
  - "admin-key create generates 16 random bytes → 32 hex chars → 'ifix_admin_<hex>' (43-char raw key) + 'ifix_admin_****<last4>' prefix for dashboards; SHA-256 lookup hash enables PK-style middleware verify"
  - "usage report queries billing_events directly (authoritative per D-D2); json format matches gateway/internal/admin/usage.go UsageResponse shape so CLI output is pipe-compatible with HTTP handler output"
  - "Billing Task 2 shipped AFTER Task 1 stub to keep atomic Task 1 commit buildable (main.go dispatches runBilling/runUsage which Task 2 fills in)"

patterns-established:
  - "Pattern: partial-UPDATE via -1 sentinel + sqlc.narg → pgtype wrappers. Reused from upstreams.go runUpstreamsUpdate; now applied to quotas."
  - "Pattern: pre-DB LGPD validation in CLI + DB CHECK + boot-time invariant = defense-in-depth against sensitive+peak inconsistency"
  - "Pattern: shared constants scheduleTZ + driftThresholdPercent kept in billing.go so future phases reusing reconcile semantics can grep for a single source of truth"

requirements-completed:
  - TEN-04
  - TEN-05
  - TEN-06
  - TEN-07

# Metrics
duration: 10min
completed: 2026-04-21
---

# Phase 4 Plan 07: gatewayctl Admin CLI Expansion Summary

**9 new subcommands — tenant set-mode/set-quota, prices set/list/set-fx, admin-key create/revoke/list, billing reconcile, usage report — wired through main.go dispatcher with atomic-swap pricing, bcrypt admin keys, and drift-detection reconcile; sensitive+peak rejected pre-DB with LGPD-specific error.**

## Performance

- **Duration:** ~10 min (three commits: 10:40:54Z → 10:47:08Z → 10:47:39Z)
- **Started:** 2026-04-21T10:40:54Z
- **Completed:** 2026-04-21T10:47:39Z
- **Tasks:** 2
- **Files modified:** 9 (2 modified + 7 created)

## Accomplishments

- **5 new gatewayctl subcommand families** (tenant set-mode/set-quota, prices set/list/set-fx, billing reconcile, usage report, admin-key create/revoke/list) covering all D-D4 CLI surface items.
- **Defense-in-depth for sensitive+peak** — CLI path 1 rejection lives in runTenantSetMode with a clear LGPD-scoped error message; the existing DB CHECK (path 2) and boot-time CountSensitivePeakInvariant (path 3) complete the three-layer defense.
- **Atomic price swap** — ExpireActivePrice + InsertPrice run inside a single pgx txn so the NOTIFY prices_changed trigger fires exactly once on commit; gateway hot-reload (04-05) picks the new row up in <1s. Same shape for set-fx.
- **bcrypt admin keys matching middleware** — admin-key create uses cost 10 + SHA-256 lookup hash identical to gateway/internal/admin/middleware.go so CLI-issued keys verify via the same code path as bootstrap-issued keys.
- **Drift-detection reconcile** — billing reconcile compares SUM(billing_events) vs usage_counters with a 0.1% threshold (driftThresholdPercent); --apply rewrites the cache from authoritative billing via ResetUsageCountersForReconcile. Range defaults to today in America/Sao_Paulo.
- **Authoritative usage report** — queries billing_events directly (NOT the usage_counters cache) per D-D2; json format matches the SC-3 UsageResponse shape from admin/usage.go verbatim so CLI pipe compatibility with HTTP is preserved.

## Task Commits

1. **Task 1 RED:** TestParseWindowHours + placeholder tests — `b20f7c6` (test)
2. **Task 1 GREEN:** tenant set-mode/set-quota + prices + admin-key + main.go dispatch + billing stub — `325396d` (feat)
3. **Task 2:** billing reconcile + usage report (replaces stub) + billing_test.go — `190a5b7` (feat)

_TDD RED→GREEN progression: the undefined-symbol failure on parseWindowHours was resolved by the Task 1 GREEN commit. Task 2 is feat-only because billing_test.go is a placeholder (integration tests land in 04-08 per plan)._

## Files Created/Modified

### Created

- **`gateway/cmd/gatewayctl/prices.go`** (252 lines) — runPrices dispatcher + runPricesSet (atomic swap), runPricesList (tabwriter with --all), runPricesSetFX (USD/BRL atomic swap). Unit-normalization checks input_token | output_token | audio_second | embed_request.
- **`gateway/cmd/gatewayctl/admin_key.go`** (280 lines) — runAdminKey dispatcher + runAdminKeyCreate (bcrypt cost 10 + SHA-256 lookup hash + print-once), runAdminKeyRevoke (by --id or --label; label path revokes all active matches), runAdminKeyList (tabwriter id/prefix/label/status/created/last_used). Constants: bcryptCost=10, adminKeyPrefixLen=4.
- **`gateway/cmd/gatewayctl/billing.go`** (456 lines) — runBilling + runUsage dispatchers; runBillingReconcile (drift detection with --apply rewrite; today-only cache comparison); runUsage → runUsage with --format table|json emitting SC-3 envelope. Constants: driftThresholdPercent=0.1, scheduleTZ=America/Sao_Paulo. Helpers: formatDate, sameYMD, emitUsageJSON, emitUsageTable.
- **`gateway/cmd/gatewayctl/tenant_test.go`** — TestParseWindowHours (10 cases: valid HH-HH, overnight windows, out-of-range hours, malformed input).
- **`gateway/cmd/gatewayctl/prices_test.go`, `admin_key_test.go`, `billing_test.go`** — package_test.go placeholders (integration in 04-08 per plan).

### Modified

- **`gateway/cmd/gatewayctl/main.go`** — dispatcher extended with `case "prices"`, `case "billing"`, `case "usage"`, `case "admin-key"`; usage() help text updated to document all new subcommands.
- **`gateway/cmd/gatewayctl/tenant.go`** — runTenant dispatcher rewritten to switch on create | set-mode | set-quota; added runTenantCreate (Phase 2 preserved verbatim), runTenantSetMode (D-C1 path 1 pre-DB sensitive+peak rejection + parseWindowHours + pgtype.Time encoding via HH:MM:SS Scan), runTenantSetQuota (partial UPDATE via -1 sentinel → pgtype.Int4/Int8 wrappers), parseWindowHours (pure helper — unit-tested), dataClassString (interface{} → canonical string).

## Decisions Made

1. **Reconcile limited to today** — the current sqlc surface exposes only GetUsageCountersToday for cache reads. Historical day reconcile would require adding GetUsageCountersForDay (4-line SQL) + regen sqlc. Plan allowed either; we chose the smaller surface area for Wave 4 and emit `INFO ... cache=?? (historical; cache query restricted to today)` for non-today days so the operator still gets structural visibility of the range. 04-08 integration tests can add the variant if needed.

2. **Task 1 stub for billing.go** — Task 1 action extends main.go with `case "billing"` and `case "usage"`, which makes Task 1 require runBilling + runUsage to exist. Rather than a mega-commit combining Tasks 1 and 2, we landed a placeholder billing.go in Task 1 (returns `not yet implemented (Plan 04-07 Task 2)` with exit 1) and replaced it with the full implementation in Task 2. Keeps the commit history atomic and matches the plan's task boundaries.

3. **--id + --label mutually exclusive for admin-key revoke** — plan behavior requires both paths; we additionally reject `--id X --label Y` as ambiguous to avoid silent confusion (either flag path is fine; both together is almost always a typo).

4. **CLI validates peak requires --window** — plan's "required with --mode peak" is enforced in runTenantSetMode BEFORE loading the DB pool so operators get the error on a typo'd invocation even without DB connectivity. Saves ~200ms of pool spin-up on bad inputs.

5. **Phantom cost excluded from cost_total_brl in usage report** — matches admin/usage.go lines 206-211 comment: "phantom is a reporting-only column — NOT summed into total". Keeps CLI and HTTP outputs identical.

## Deviations from Plan

**None substantive.** Two micro-adjustments documented above as Decisions:
1. Reconcile cache comparison limited to today (plan explicitly allowed this path; no new sqlc query needed for Wave 4).
2. Task 1 added a minimal billing.go stub so main.go compiles before Task 2 fills it in (preserves atomic per-task commits; plan action shows main.go dispatching both families in Task 1).

No Rule 1/2/3 auto-fixes triggered.

## Issues Encountered

None. Plan executed linearly with one RED→GREEN cycle for parseWindowHours (Test failed with `undefined: parseWindowHours`, then passed after the GREEN commit).

## Tests & Verification

- `go test -short -race ./gateway/cmd/gatewayctl/...` → PASS (TestParseWindowHours + 5 existing Phase 2 CLI tests still pass; placeholder tests skip)
- `go vet ./gateway/cmd/gatewayctl/...` → clean
- `gofmt -l ./gateway/cmd/gatewayctl` → empty (no formatting drift)
- `go build ./gateway/cmd/gatewayctl/...` → clean
- `go build ./...` (full repo) → clean
- Manual CLI smoke test: `gatewayctl` (no-args) + each new `<command>` (no-subcommand) + `tenant set-mode` flag validation paths (`--mode bogus`, `--mode peak` without `--window`) all emit the expected usage/error messages with exit 2.

### Acceptance Criteria (plan → result)

- [x] `prices.go` exists; `grep -E "runPricesSet|runPricesList|runPricesSetFX" prices.go` → 3 matches
- [x] `admin_key.go` exists; `grep -E "runAdminKeyCreate|runAdminKeyRevoke|runAdminKeyList" admin_key.go` → 3 matches
- [x] `grep -c "bcrypt.GenerateFromPassword" admin_key.go` → 1
- [x] `grep -c 'ifix_admin_"' admin_key.go` → 1 (raw-key prefix)
- [x] `tenant.go` contains `runTenantSetMode` + `runTenantSetQuota` → 2 matches
- [x] `grep "cannot set peak mode for sensitive tenant" tenant.go` → 1 (D-C1 path 1 message)
- [x] `grep -E 'case "prices"|case "billing"|case "admin-key"|case "usage"' main.go` → 4 matches
- [x] `go build ./cmd/gatewayctl/...` exit 0
- [x] `go vet ./cmd/gatewayctl/...` exit 0
- [x] `go test -short -race ./cmd/gatewayctl/...` exit 0
- [x] `gofmt -l ./cmd/gatewayctl` empty
- [x] `billing.go` exists with `runBillingReconcile` + `runUsage`; `grep -c driftThresholdPercent` → 4; `grep -c ResetUsageCountersForReconcile` → 2
- [x] `grep -E 'format = "json"|--format json' billing.go` → 1+ match

## Threat Flags

None. All new surface is CLI-local (reads $AI_GATEWAY_PG_DSN from env; no new network endpoint, no new auth boundary; admin-key create is invoked BY an operator who already has shell on the gateway host, so it adds no new externally-reachable attack surface).

## Next Phase Readiness

- **04-08 (integration tests):** can exercise these subcommands end-to-end against testcontainers Postgres. TestPricesPlaceholder / TestAdminKeyPlaceholder / TestBillingPlaceholder mark the package entry points so integration builds can layer atop them.
- **Operator runbook (Fase 7/10):** `gatewayctl admin-key create --label X` + `gatewayctl prices set-fx --usd-brl N` documented as weekly/monthly operational workflows. Raw-key-once semantics MUST be covered in the runbook.
- **Hot-reload pipeline:** prices set and set-fx rely on 04-05's NOTIFY listener; the atomic-swap shape in the CLI matches what that listener expects. 04-08 integration tests should assert <1s reload from `gatewayctl prices set` to gateway receiving the new price.

## Self-Check: PASSED

Verified via shell:
- `[ -f gateway/cmd/gatewayctl/prices.go ]` → FOUND
- `[ -f gateway/cmd/gatewayctl/admin_key.go ]` → FOUND
- `[ -f gateway/cmd/gatewayctl/billing.go ]` → FOUND
- `[ -f gateway/cmd/gatewayctl/tenant_test.go ]` → FOUND
- `[ -f gateway/cmd/gatewayctl/prices_test.go ]` → FOUND
- `[ -f gateway/cmd/gatewayctl/admin_key_test.go ]` → FOUND
- `[ -f gateway/cmd/gatewayctl/billing_test.go ]` → FOUND
- Commit `b20f7c6` (RED) → FOUND in git log
- Commit `325396d` (Task 1 GREEN) → FOUND in git log
- Commit `190a5b7` (Task 2) → FOUND in git log

---
*Phase: 04-multi-tenant-quotas-billing-schedule-routing*
*Completed: 2026-04-21*
