---
phase: 07-observability-dashboard-alerting
fixed_at: 2026-05-14T09:10:00Z
review_path: .planning/phases/07-observability-dashboard-alerting/07-REVIEW.md
iteration: 2
findings_in_scope: 2
fixed: 2
skipped: 0
status: all_fixed
---

# Phase 7: Code Review Fix Report

**Fixed at:** 2026-05-14T09:10:00Z
**Source review:** .planning/phases/07-observability-dashboard-alerting/07-REVIEW.md
**Iteration:** 2

**Summary:**
- Findings in scope: 2 (WR-10, WR-11 — the 2 residual WARNINGs; INFO findings IN-07/IN-08 out of scope)
- Fixed: 2
- Skipped: 0

## Fixed Issues

### WR-10: Per-tenant UI renders raw tenant UUIDs as human labels

**Files modified:** `gateway/db/queries/audit.sql`, `gateway/internal/db/gen/audit.sql.go`, `gateway/internal/admin/metrics.go`, `gateway/internal/admin/metrics_test.go`, `dashboard/src/lib/gateway.ts`, `dashboard/src/lib/gateway.test.ts`, `dashboard/src/components/tenant-table.tsx`, `dashboard/src/app/(dashboard)/tenants/page.tsx`
**Commit:** 4d702f6
**Applied fix:** Took option (a) from the review — kept the dashboard a pure consumer by extending the gateway contract.

- `TenantLatencyPercentiles` query now `LEFT JOIN ai_gateway.tenants` (the `tenants` table has `slug` + `name` columns, confirmed against migration `0001_create_tenants.sql`) and emits `tenant_slug` + `tenant_name`. LEFT JOIN — not INNER — so an audit row whose tenant was since deleted still surfaces with NULL slug/name. The `GROUP BY` was extended to `al.tenant_id, t.slug, t.name, al.route` and all column refs were table-qualified (`al.`/`t.`).
- Regenerated sqlc output by hand (sqlc binary not installed in the env): `TenantLatencyPercentilesRow` gains `TenantSlug pgtype.Text` + `TenantName pgtype.Text`, and the `Scan` call was updated to the new column order.
- `admin.TenantLatencyRow` gains `TenantSlug *string` + `TenantName *string` (JSON `null` for a deleted tenant); the handler maps them via the existing `pgTextPtr` helper already present in `audit.go` (no duplicate added).
- Dashboard `TenantMetricRow` gains `tenant_slug: string | null` + `tenant_name: string | null`, plus a new `tenantLabel()` helper (name → slug → raw UUID fallback). `tenant-table.tsx` renders `tenantLabel(row)` instead of the raw `tenant_id`; the tenants-page `Select` shows `tenantLabel` while keeping the UUID as the option value (`/admin/usage` accepts UUID or slug — verified in `usage.go` `resolveTenant`).
- Tests updated to the new shape: `metrics_test.go` seeds + asserts the slug/name round-trip; `gateway.test.ts` payload carries the new fields and a new `tenantLabel` describe block asserts the 3-tier fallback.

### WR-11: `dbFlusher.Flush` CopyFrom path for state-change rows is still untested

**Files modified:** `gateway/internal/audit/writer.go`, `gateway/internal/audit/writer_test.go`
**Commit:** cd7c39a
**Applied fix:** Took the review's second option ("a focused unit test asserting the CopyFrom column list and row tuple are positionally consistent"). The embedded-Postgres harness that exists in the gateway (`internal/integration_test/setup_test.go`, testcontainers) is gated behind the `//go:build integration` tag and is NOT part of the phase verification command (`go test ./internal/audit/... ./internal/admin/... ./internal/db/...` with no `-tags integration`) — so a containerized test there would not run in the loop. The positional-consistency unit test runs under the default `go test`.

- Refactored `dbFlusher.Flush`: the 24-element CopyFrom column list is now the package-level `auditLogCopyColumns`, and the per-event row tuple is built by the new `auditLogCopyRow(Event) []any`. `Flush` calls both. Behavior is byte-identical — pure extraction.
- Added 3 tests in `writer_test.go`: `TestAuditLogCopy_ColumnRowLengthsMatch` (column count == value count — catches a column added to only one slice), `TestAuditLogCopy_StateChangeRowPositions` (builds a real state-change Event tuple, maps it by column name, asserts `event_kind`/`reason`/`request_id` plus mid-tuple anchors land in the right slots), and `TestAuditLogCopy_PerRequestRowLeavesStateChangeColsNull` (per-request row leaves `event_kind`/`reason` NULL — the additive contract).

## Verification

All commands from the task ran clean against the worktree:

- `cd gateway && go build ./...` — OK
- `cd gateway && go vet ./...` — OK
- `cd gateway && go test ./internal/audit/... ./internal/admin/... ./internal/db/... -count=1` — all `ok`
- `cd dashboard && npx tsc --noEmit` — exit 0
- `cd dashboard && npx vitest run` — 14 tests passed (4 files)
- `cd dashboard && npm run build` — build succeeded, all 7 routes generated

(`dashboard/node_modules` was absent in the fresh worktree checkout; `npm install` was run once before the dashboard checks — not committed.)

---

_Fixed: 2026-05-14T09:10:00Z_
_Fixer: Claude (gsd-code-fixer)_
_Iteration: 2_
