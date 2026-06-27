---
phase: 260625-sna
plan: 01
subsystem: dashboard
tags: [frontend, nextjs, recharts, consumo, observability]
requires: [fetchMetrics, fetchUsage, UsageResponse, KpiCard, shadcn-chart, shadcn-table]
provides: [/consumo route, aggregateSummary, aggregateDaily, perTenantRows, ConsumoTrendChart, ConsumoTable]
affects: [dashboard/src/components/app-sidebar.tsx]
tech-stack:
  added: []
  patterns: [client-side cross-tenant aggregation via Promise.allSettled, dual-axis recharts LineChart, WR-08 local-date formatting]
key-files:
  created:
    - dashboard/src/lib/consumo.ts
    - dashboard/src/components/consumo-trend-chart.tsx
    - dashboard/src/components/consumo-table.tsx
    - dashboard/src/app/(dashboard)/consumo/page.tsx
  modified:
    - dashboard/src/components/app-sidebar.tsx
decisions:
  - "Trend chart uses two Y axes (yAxisId tokens left / cost right) so sparse BRL cost is not flattened by large token counts"
  - "chart config key for cost series is cost_brl → CSS var --color-cost_brl (shadcn derives --color-<key>)"
  - "Partial tenant failures via Promise.allSettled never blank the page; only a rejected fetchMetrics drives the error state"
metrics:
  duration: ~12m
  completed: 2026-06-25
---

# Phase 260625-sna Plan 01: Consumo (aggregated cost/usage) page Summary

Added a `/consumo` dashboard page that aggregates real `/admin/usage` cost/usage across ALL tenants client-side (fetchMetrics → dedup tenant ids → Promise.allSettled(fetchUsage)), rendering a KPI row, a dual-axis tokens/custo trend chart, and a per-tenant table behind a current-month-default period filter.

## What was built

- **Task 1 (`0aeefab`)** — `dashboard/src/lib/consumo.ts` with three pure helpers over `UsageResponse[]`: `aggregateSummary` (sums every summary numeric field, empty → zeros), `aggregateDaily` (merges rows by date into `{date, tokens, cost_brl}`, sorted ascending), `perTenantRows` (one row per tenant from summary, sorted by `cost_local_phantom_brl` desc, label name→slug→id). Exports `ConsumoSummary`, `DailyAggRow`, `TenantUsageRow` types. Sidebar gained a `/consumo` "Consumo" entry (Receipt icon) between Tenants and Histórico.
- **Task 2 (`78e7858`)** — `consumo-trend-chart.tsx` (`"use client"` recharts `LineChart`, dual Y axis: tokens left / custo R$ right, ChartTooltip + ChartLegend) and `consumo-table.tsx` (shadcn table: Tenant | Custo R$ | Tokens entrada | Tokens saída | Áudio (s) | Embeds, right-aligned tabular-nums numeric cells, 0 rendered honestly, `tenant_id` key).
- **Task 3 (`277aa23`)** — `(dashboard)/consumo/page.tsx`: single `useQuery(["consumo", applied])` whose queryFn lists tenants, dedups ids, fans out `Promise.allSettled(fetchUsage)`, collects fulfilled responses + failure count, and returns `{summary, daily, tenants, failures, total}`. Period filter (popover+calendar+"Aplicar período") defaults to current month (day 1 → today) using local `isoDate` (WR-08, no `toISOString`). KPI row + trend Card + per-tenant Card; loading skeletons; pt-BR error state with "Tentar novamente"; honest partial-failure note.

## Verification

- `cd dashboard && npm run build` → exit 0; `/consumo` route compiled (4.74 kB, 275 kB first load).
- Per-task `tsc --noEmit` → exit 0 for Tasks 1 and 2.
- `npm run lint` → no errors for the added files.
- `Promise.allSettled` present in the page (BUILD_OK grep).

Note: the worktree had no `node_modules`; symlinked the main checkout's `dashboard/node_modules` (identical lockfile, read-only during build) to run build/tsc/lint. The symlink is gitignored and not part of any commit.

## Deviations from Plan

None — plan executed as written. The chart cost series CSS var is `--color-cost_brl` (matching the `cost_brl` ChartConfig key) rather than the plan's loosely-worded `--color-cost`; the shadcn chart block derives `--color-<key>`, so this is the internally-consistent correct token.

## Known Stubs

None. All values flow from real aggregated `/admin/usage` data; 0 values render as 0 with no placeholder substitution.

## Self-Check: PASSED

- dashboard/src/lib/consumo.ts — FOUND
- dashboard/src/components/consumo-trend-chart.tsx — FOUND
- dashboard/src/components/consumo-table.tsx — FOUND
- dashboard/src/app/(dashboard)/consumo/page.tsx — FOUND
- dashboard/src/components/app-sidebar.tsx — MODIFIED (/consumo entry present)
- Commits 0aeefab, 78e7858, 277aa23 — FOUND in git log
