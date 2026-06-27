---
phase: 15-dashboard-economia-e-historico
verified: 2026-06-27T00:51:00Z
status: human_needed
score: 17/17 must-haves verified
overrides_applied: 0
human_verification:
  - test: "Open /economia in the browser; confirm the 5 KPIs render (Líquido R$, ROI multiplier, Custo OpenRouter, % servido local, Horas pod UP) and the daily phantom-vs-Vast trend chart appears"
    expected: "All 5 KpiCards show non-placeholder values (or '—' for null ROI/pct); LineChart renders 3 lines (phantom_brl, vast_brl, economia_brl) on a shared Y axis"
    why_human: "Component rendering, Recharts chart display, and real gateway data flow cannot be verified without a running browser + live gateway"
  - test: "On /incidents, apply a date-range via the calendar picker and type a search term; confirm the table filters and the pager shows 'X–Y de {total}' format"
    expected: "Date picker opens Popover+Calendar; search box filters table rows; pager next/prev buttons are disabled/enabled based on offset+limit vs total"
    why_human: "Interactive UI state transitions and accurate pager disabling require browser testing"
---

# Phase 15: Dashboard Economia & Histórico Verification Report

**Phase Goal:** Dar ao operador o número que mais importa — se a GPU própria economiza de verdade vs OpenRouter (OBS-09: painel economia phantom vs Vast + série temporal real, na nova rota /economia, com 5 métricas: líquido R$, ROI null-safe, custo OpenRouter fallback, % servido local, horas pod UP) — e tornar o histórico de incidentes navegável (OBS-10: filtro de data + busca + total count no /incidents).
**Verified:** 2026-06-27T00:51:00Z
**Status:** human_needed
**Re-verification:** No — initial verification

## Goal Achievement

### Observable Truths

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | GET /admin/economy?from=&to= returns 200 JSON with keys {range, summary, series} | ✓ VERIFIED | `economy.go:276-298` — EconomyResponse struct encodes all three fields; ServeHTTP writes 200 + JSON |
| 2 | summary carries all 5 locked metrics: economia_liquida_brl, roi_multiplier, custo_openrouter_brl, pct_servido_local, horas_pod_up | ✓ VERIFIED | `EconomySummary` struct at `economy.go:54-62`; all 5 fields present with correct json tags |
| 3 | summary.roi_multiplier is null (not Inf/NaN) when vast_brl == 0; pct_servido_local null when zero requests | ✓ VERIFIED | `economy.go:265-275` — both are `*float64`, nil only when denominator is zero; `TestEconomyHandler_ROINilWhenVastZero` and `TestEconomyHandler_PctServidoLocal` confirm nil at zero-denominator |
| 4 | phantom + external cost summed gateway-wide (all tenants) with NO tenant filter | ✓ VERIFIED | `billing.sql:95-107` — `SumBillingAllTenantsRange` has no `WHERE tenant_id` clause; gen file at `billing.sql.go:107-110` confirms params are only `Ts`/`Ts_2` |
| 5 | Vast cost + pod-up hours buckets by started_at BRT date reusing the operations.go accrual | ✓ VERIFIED | `economy.go:201-225` — closed row uses `numericPtr(row.TotalCostBrl)`, open row uses `*dph * hours * h.cfg.USDToBRLRate`; `TestEconomyHandler_OpenLifecycleAccrual` confirms 0.4 dph × 2h × 5.0 = ~4.0 BRL |
| 6 | GET /admin/audit accepts from/to/search params and returns total count | ✓ VERIFIED | `audit.go:114-248` — full tz/date parse block + `searchPattern` + `CountAuditStateChanges`; `AuditResponse.Total int64` at line 62 |
| 7 | audit read queries select ONLY audit_log metadata, never audit_log_content | ✓ VERIFIED | `audit.sql.go` gen file: the only `audit_log_content` reference is the pre-existing INSERT writer (line 42); `ListAuditStateChanges` and `CountAuditStateChanges` SELECT only metadata columns |
| 8 | search is a parameterized ILIKE arg, never string-concatenated | ✓ VERIFIED | `audit.go:189-192` — `searchPattern = "%" + q + "%"` is the value, bound as `Column3` param to sqlc; `audit.sql:31` — `($3 = '%' OR route ILIKE $3 OR ...)` |
| 9 | AuditResponse includes a total field for honest pagination | ✓ VERIFIED | `audit.go:62` — `Total int64 json:"total"` in AuditResponse; populated from `CountAuditStateChanges` at line 209-220 |
| 10 | /economia route renders 5 locked KPIs with "—" on null ROI and null % local | ✓ VERIFIED | `economy-panel.tsx:31-46` — `formatRoi` returns "—" when `roi === null`; `formatPctLocal` returns "—" when `pct === null`; all 5 KpiCards present at lines 64-89 |
| 11 | A real time-series chart (X = day) renders phantom, vast, and economia lines | ✓ VERIFIED | `economy-trend-chart.tsx:62-82` — exactly 3 `<Line>` elements (`phantom_brl`, `vast_brl`, `economia_brl`); single shared `<YAxis>` with no per-series `yAxisId` binding |
| 12 | /economia uses a single server-side fetchEconomy — NO per-tenant fan-out | ✓ VERIFIED | `economia/page.tsx:85-88` — single `useQuery` with `queryFn: () => fetchEconomy(applied.from, applied.to)`; no `Promise.allSettled`, no per-tenant loop |
| 13 | Economia appears in the sidebar nav | ✓ VERIFIED | `app-sidebar.tsx:40` — `{ href: "/economia", label: "Economia", icon: TrendingUp }` in NAV_ITEMS; `TrendingUp` imported from lucide-react at line 19 |
| 14 | /incidents has a date-range picker and a search box above the table | ✓ VERIFIED | `incidents/page.tsx:104-135` — Popover+Calendar block + Input with search state; `Calendar` import confirmed at line 28 |
| 15 | The pager uses a real total (offset+limit < total), not a row-count heuristic | ✓ VERIFIED | `audit-table.tsx:82` — `canNext = total !== undefined ? offset + limit < total : rows.length >= limit`; pager footer shows `de {total}` at line 138 |
| 16 | fetchAudit forwards from/to/search params to the endpoint | ✓ VERIFIED | `gateway.ts:391-405` — `fetchAudit(limit=50, offset=0, from?, to?, search?)` spreads optional params only when truthy into `proxyGet<AuditResponse>("audit", {...})` |
| 17 | sqlc gen layer in sync with SQL queries (CI gate) | ✓ VERIFIED | Gen files contain all three new billing functions (`SumPhantomAllTenantsByDate`, `SumBillingAllTenantsRange`, `ListPrimaryLifecyclesInRange`) and `CountAuditStateChanges`; `go build ./...` succeeds |

**Score:** 17/17 truths verified

### Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `gateway/db/queries/billing.sql` | SumPhantomAllTenantsByDate + SumBillingAllTenantsRange (no tenant filter) | ✓ VERIFIED | Both queries present; neither contains `tenant_id` filter; tz idiom preserved |
| `gateway/db/queries/primary_lifecycles.sql` | ListPrimaryLifecyclesInRange (range overlap) | ✓ VERIFIED | `started_at >= $1 AND started_at < $2`, no LIMIT, full compact column list |
| `gateway/internal/db/gen/billing.sql.go` | Regenerated with new query functions | ✓ VERIFIED | `SumPhantomAllTenantsByDate`, `SumBillingAllTenantsRange` functions at lines 254+, 96+ |
| `gateway/internal/db/gen/primary_lifecycles.sql.go` | Regenerated with ListPrimaryLifecyclesInRange | ✓ VERIFIED | Function at line 206; `ListPrimaryLifecyclesInRangeParams` struct at line 182 |
| `gateway/internal/db/gen/audit.sql.go` | CountAuditStateChanges + renumbered ILIKE params | ✓ VERIFIED | `CountAuditStateChanges` at line 34; `Column3 interface{}` search param in both List and Count structs |
| `gateway/internal/admin/economy.go` | EconomyHandler GET /admin/economy (5-metric summary + series); min 140 lines | ✓ VERIFIED | 298 lines; dual constructor; all 5 metrics; Vast accrual reuses numericPtr + USDToBRLRate |
| `gateway/internal/admin/economy_test.go` | 8 tests: gateway-wide sum, economia/ROI, ROI nil, custo OpenRouter, pct local, horas pod up, open accrual, bad date | ✓ VERIFIED | 8 tests, all PASS — confirmed by `go test ./internal/admin/ -run Economy` |
| `gateway/internal/admin/audit.go` | from/to/search + Total in AuditResponse | ✓ VERIFIED | Full tz block; `searchPattern` bound as Column3; `Total int64` in AuditResponse |
| `gateway/internal/admin/audit_test.go` | 7 tests (3 existing + 4 new for range/search/total) | ✓ VERIFIED | 7 tests, all PASS — confirmed by `go test ./internal/admin/ -run Audit` |
| `gateway/cmd/gateway/main.go` | adminEconomyHandler wired (field, construct, assign, mount) | ✓ VERIFIED | 5 occurrences of `adminEconomyHandler`; mount at line 1499 as GET /economy |
| `dashboard/src/lib/gateway.ts` | EconomyResponse (7 summary fields) + fetchEconomy + AuditResponse.total + fetchAudit(from/to/search) | ✓ VERIFIED | All interfaces present; `fetchEconomy` at line 426; `AuditResponse.total: number` at line 150; `fetchAudit` with optional params at line 391 |
| `dashboard/src/lib/gateway.test.ts` | Tests for fetchEconomy (null fixture) and fetchAudit (from/to/search forwarding) | ✓ VERIFIED | 9 tests pass; `describe("fetchEconomy")` at line 252; `describe("fetchAudit")` at line 107 |
| `dashboard/src/components/economy-panel.tsx` | 5 KPIs, null-safe ROI + % local | ✓ VERIFIED | 5 KpiCards; `formatRoi` returns "—" on null; `formatPctLocal` returns "—" on null |
| `dashboard/src/components/economy-trend-chart.tsx` | Single-axis LineChart with 3 lines | ✓ VERIFIED | Starts with `"use client"`; 3 `<Line>` elements; single `<YAxis>` (no dual axis) |
| `dashboard/src/app/(dashboard)/economia/page.tsx` | /economia route with fetchEconomy, date-range picker, EconomyPanel + EconomyTrendChart | ✓ VERIFIED | Single useQuery; Popover+Calendar; isoDate/currentMonthRange (no toISOString); renders EconomyPanel + EconomyTrendChart |
| `dashboard/src/components/app-sidebar.tsx` | Sidebar nav entry /economia | ✓ VERIFIED | NAV_ITEMS entry at line 40; TrendingUp icon imported |
| `dashboard/src/app/(dashboard)/incidents/page.tsx` | Date-range + search controls; fetchAudit with from/to/search | ✓ VERIFIED | Popover+Calendar + Input search box; fetchAudit(PAGE_SIZE, offset, applied.from, applied.to, search); offset resets on filter change |
| `dashboard/src/components/audit-table.tsx` | total-driven pager (canNext = offset + limit < total) | ✓ VERIFIED | Line 82: `canNext = total !== undefined ? offset + limit < total : rows.length >= limit` |

### Key Link Verification

| From | To | Via | Status | Details |
|------|----|-----|--------|---------|
| `gateway/cmd/gateway/main.go` | `admin.NewEconomyHandler` | `adminEconomyHandler` field + mount GET /economy | ✓ WIRED | Lines 115, 1253, 1273, 1498-1499 — all 4 wiring points confirmed |
| `gateway/internal/admin/economy.go` | `gen.SumBillingAllTenantsRange` | `economyQueries` interface | ✓ WIRED | Interface at line 77; called at line 157 |
| `gateway/internal/admin/audit.go` | `gen.CountAuditStateChanges` | `auditQueries` interface | ✓ WIRED | Interface at line 88; called at line 209 |
| `gateway/internal/admin/audit.go` | `ListAuditStateChangesParams` with from/to/search | Column3 + Ts/Ts_2 params | ✓ WIRED | Lines 194-200; params include Ts, Ts_2, Column3 (ILIKE), Limit, Offset |
| `dashboard/src/app/(dashboard)/economia/page.tsx` | `fetchEconomy` | `useQuery` queryFn | ✓ WIRED | Line 87: `queryFn: () => fetchEconomy(applied.from, applied.to)` |
| `dashboard/src/lib/gateway.ts` | `/admin/economy` | `proxyGet<EconomyResponse>("economy", ...)` | ✓ WIRED | Line 427: `return proxyGet<EconomyResponse>("economy", { from, to })` |
| `dashboard/src/components/app-sidebar.tsx` | `/economia` | NAV_ITEMS entry | ✓ WIRED | Line 40: `{ href: "/economia", label: "Economia", icon: TrendingUp }` |
| `dashboard/src/app/(dashboard)/incidents/page.tsx` | `fetchAudit` | `useQuery` queryFn with from/to/search | ✓ WIRED | Line 82: `fetchAudit(PAGE_SIZE, offset, applied.from, applied.to, search)` |
| `dashboard/src/components/audit-table.tsx` | `total` | `canNext = offset + limit < total` | ✓ WIRED | Line 82: canNext logic; line 138: pager footer shows `de {total}` |

### Data-Flow Trace (Level 4)

| Artifact | Data Variable | Source | Produces Real Data | Status |
|----------|---------------|--------|-------------------|--------|
| `economy.go` | `sumRow` (phantom, external, local/total requests) | `gen.SumBillingAllTenantsRange` — DB query with no tenant filter | DB query confirmed in gen SQL: `FROM ai_gateway.billing_events WHERE ts >= $1 AND ts < $2` | ✓ FLOWING |
| `economy.go` | `lcRows` (vast accrual + pod hours) | `gen.ListPrimaryLifecyclesInRange` — DB query on primary_lifecycles | DB query confirmed: `FROM ai_gateway.primary_lifecycles WHERE started_at >= $1 AND started_at < $2` | ✓ FLOWING |
| `audit.go` | `rows` + `total` | `gen.ListAuditStateChanges` + `gen.CountAuditStateChanges` | DB queries confirmed in gen SQL; `total` from `COUNT(*)::bigint` | ✓ FLOWING |
| `economia/page.tsx` | `query.data` | `fetchEconomy` → `proxyGet` → `/api/gateway/economy` → gateway `/admin/economy` | Full chain: page → proxyGet → server route.ts → gateway DB query | ✓ FLOWING |
| `incidents/page.tsx` | `data` | `fetchAudit` → `proxyGet` → `/api/gateway/audit` → gateway `/admin/audit` | Full chain: page → proxyGet → server route.ts → gateway DB queries (list + count) | ✓ FLOWING |

### Behavioral Spot-Checks

| Behavior | Command | Result | Status |
|----------|---------|--------|--------|
| Go Economy tests (8) | `go test ./internal/admin/ -run Economy` | 8/8 PASS — all assertions green | ✓ PASS |
| Go Audit tests (7) | `go test ./internal/admin/ -run Audit` | 7/7 PASS — all assertions green | ✓ PASS |
| Gateway build | `go build ./...` | exit 0 — no compilation errors | ✓ PASS |
| Go vet | `go vet ./internal/admin/ ./cmd/gateway/` | exit 0 — no issues | ✓ PASS |
| Dashboard vitest | `bun run test` | 38/38 PASS (8 files) | ✓ PASS |
| Dashboard gateway wrapper tests | `bun run test src/lib/gateway.test.ts` | 9/9 PASS (fetchEconomy + fetchAudit cases) | ✓ PASS |
| TypeScript typecheck | `bunx tsc --noEmit` | exit 0 — no type errors | ✓ PASS |
| adminEconomyHandler count | `grep -c adminEconomyHandler cmd/gateway/main.go` | 5 (>= 3 required) | ✓ PASS |

### Probe Execution

Step 7c: SKIPPED — no `scripts/*/tests/probe-*.sh` files found; phase is a feature development phase (gateway handler + dashboard UI), not a migration/tooling phase.

### Requirements Coverage

| Requirement | Source Plan | Description | Status | Evidence |
|-------------|------------|-------------|--------|----------|
| OBS-09 | 15-01, 15-03 | Dashboard painel de Economia (phantom vs Vast) — 5 metrics, series temporal | ✓ SATISFIED | economy.go + economy-panel.tsx + economy-trend-chart.tsx + /economia route; all 5 metrics computed + rendered; tests green |
| OBS-10 | 15-02, 15-04 | /incidents filtro de data + busca + total count | ✓ SATISFIED | audit.go from/to/search + CountAuditStateChanges; incidents/page.tsx + audit-table.tsx; pager uses real total |

**Note:** REQUIREMENTS.md traceability table shows OBS-09 still as "Pending" while OBS-10 is "Complete". This is a documentation inconsistency only — the OBS-09 implementation is fully verified in the codebase. Not a code gap; a tracking update is needed in REQUIREMENTS.md.

### Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
|------|------|---------|----------|--------|
| None found | — | No TBD/FIXME/XXX markers in any phase-modified file | — | — |
| No stubs | — | No placeholder returns, no hardcoded empty responses, no `return null` in component renders | — | — |

Scanned: `economy.go`, `audit.go`, `economy-panel.tsx`, `economy-trend-chart.tsx`, `economia/page.tsx`, `incidents/page.tsx`, `audit-table.tsx`. All clean.

### Human Verification Required

### 1. /economia page renders KPIs + trend chart with real data

**Test:** Open `/economia` in the browser (current-month default). Observe the 5 KPI cards and the trend chart below.
**Expected:** All 5 KpiCards show non-placeholder values from the gateway. ROI card shows "—" if vast_brl is 0 for the period. Chart renders 3 lines (phantom R$/dia, Vast R$/dia, Economia R$/dia) on a single shared Y axis with dates on X axis. No Inf/NaN anywhere.
**Why human:** Recharts chart rendering, actual numeric values from the live gateway DB, and the visual null-safe "—" display require a running browser + live gateway connection.

### 2. /incidents filter controls work correctly

**Test:** Open `/incidents`. Apply a date-range via the calendar picker (e.g. past 7 days). Type a search term (e.g. "failover"). Observe the table and pager.
**Expected:** Popover opens with a two-month Calendar; selected range appears in the trigger button. Typing in the search box filters rows in real time. Pager shows `{from}–{to} de {total}` format; Next button disables when `offset + limit >= total`. Offset resets to page 1 on filter change.
**Why human:** Interactive React state transitions (Popover open/close, Calendar selection, Input onChange → setOffset reset) require browser testing; real total from the gateway must be observed.

### Gaps Summary

No automated gaps found. All 17 truths are verified against the actual codebase. Two items need human browser verification (visual rendering + interactive filter behavior) — these are expected for a UI-heavy phase and do not indicate implementation incompleteness.

The only non-blocking note is a REQUIREMENTS.md documentation inconsistency: OBS-09 remains marked "Pending" in the traceability table while the implementation is complete. This should be updated to "Complete" separately.

---

_Verified: 2026-06-27T00:51:00Z_
_Verifier: Claude (gsd-verifier)_
