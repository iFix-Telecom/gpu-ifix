---
phase: 15-dashboard-economia-e-historico
plan: 03
subsystem: dashboard-economia
tags: [obs-09, economy, dashboard, nextjs, recharts, react-query]
requires:
  - GET /admin/economy (15-01 EconomyResponse: 5-metric summary + daily series)
  - proxyGet<T> server-side proxy (gateway.ts:298) — admin key stays server-side
  - KpiCard, ChartContainer, Popover+Calendar, StaleIndicator dashboard primitives
provides:
  - EconomyResponse + EconomyDayRow TS types + fetchEconomy(from,to) wrapper
  - EconomyPanel (5 null-safe KPIs) + EconomyTrendChart (single-axis 3-line series)
  - /economia route (single server-side fetch, date-range picker)
  - sidebar nav entry /economia
affects:
  - dashboard nav (new route between Operação and Histórico)
tech-stack:
  added: []
  patterns:
    - single-fetch page (one useQuery over a server-computed aggregate) — NOT the /consumo per-tenant fan-out
    - null-safe KPI formatters (roi/pct render "—" on JSON null, never Inf/NaN)
    - single-shared-Y-axis Recharts variant of consumo-trend-chart (all-BRL series)
key-files:
  created:
    - dashboard/src/components/economy-panel.tsx
    - dashboard/src/components/economy-trend-chart.tsx
    - dashboard/src/app/(dashboard)/economia/page.tsx
  modified:
    - dashboard/src/lib/gateway.ts
    - dashboard/src/lib/gateway.test.ts
    - dashboard/src/components/app-sidebar.tsx
decisions:
  - "fetchEconomy is a SINGLE gateway-wide proxyGet — the /consumo Promise.allSettled per-tenant fan-out is deliberately dropped (server returns one authoritative answer)"
  - "EconomyResponse mirrors the 15-01 Go struct field-for-field (json tags): roi_multiplier + pct_servido_local typed number|null"
  - "economy-trend-chart uses ONE shared Y axis (all three series are BRL) — drops consumo's dual yAxisId"
  - "economia line colored --primary; vast --status-warning; phantom --chart-2 (--status-healthy does not exist in the theme)"
metrics:
  duration: ~20m
  completed: 2026-06-26
  tasks: 3
  files_created: 3
  files_modified: 3
---

# Phase 15 Plan 03: Economia frontend (OBS-09) Summary

The /economia route now renders the five locked Economia KPIs (líquido R$, ROI
multiplier, custo OpenRouter fallback, % servido local, horas pod UP) plus the
dashboard's first true time-axis chart (daily phantom/vast/economia lines), all
from a single server-side `fetchEconomy` over the 15-01 `/admin/economy`
endpoint — no per-tenant fan-out.

## What Was Built

**Task 1 — EconomyResponse + fetchEconomy (TDD: RED 6f5a0df → GREEN 2e61c9c)**
- `gateway.test.ts`: two new cases — fetchEconomy hits `/api/gateway/economy`
  with from+to params; an EconomyResponse fixture round-trips with
  roi_multiplier AND pct_servido_local both null. RED failed with
  `fetchEconomy is not a function` before implementation.
- `gateway.ts`: `EconomyDayRow` + `EconomyResponse` interfaces mirroring the
  15-01 Go `admin.EconomyResponse` field-for-field (all 7 summary fields;
  roi_multiplier + pct_servido_local typed `number | null`). `fetchEconomy(from,
  to)` returns `proxyGet<EconomyResponse>("economy", { from, to })` — single
  gateway-wide fetch, no Promise.allSettled added. Browser never reads the admin
  key (T-15-10 — all calls go through the existing server proxy).

**Task 2 — EconomyPanel + EconomyTrendChart (commit 6bc4e97)**
- `economy-panel.tsx`: a Card with a responsive 5-KpiCard grid (mirrors
  operacao-cost-panel.tsx). Líquido R$ / Custo OpenRouter via `formatBrl`; ROI
  via `formatRoi` ("4,02×" or "—" when `roi === null`); % local via
  `formatPctLocal` ("87,0 %" or "—" when `pct === null`); horas pod UP via
  `formatHoras`. No metric downgraded to a placeholder — all five come from the
  endpoint summary.
- `economy-trend-chart.tsx`: `"use client"` Recharts LineChart with exactly
  three `<Line>`s (phantom_brl, vast_brl, economia_brl), each
  `stroke="var(--color-<key>)"` strokeWidth=2 dot=false, X axis `dataKey="date"`,
  and a SINGLE shared Y axis (consumo's dual per-series axis binding dropped —
  every series is BRL on one scale). chartConfig keys carry pt-BR labels +
  theme colors.

**Task 3 — /economia route + sidebar (commit 87f411f)**
- `economia/page.tsx`: copies consumo's `isoDate`/`currentMonthRange` (LOCAL
  date components, WR-08), range/applied state + applyPeriod(), Popover+Calendar
  block, and the pt-BR Skeleton/error("Tentar novamente")/StaleIndicator
  branches. Data layer REPLACED with one
  `useQuery({ queryKey: ["economia", applied], queryFn: () =>
  fetchEconomy(applied.from, applied.to) })` — no fetchMetrics, no
  per-tenant fan-out, no "N de M tenants" note. Renders `<EconomyPanel
  summary={...}>` + `<EconomyTrendChart rows={...series}>`; default range =
  current month.
- `app-sidebar.tsx`: one NAV_ITEMS entry `{ href: "/economia", label:
  "Economia", icon: TrendingUp }` + TrendingUp import.

## Verification

- `cd dashboard && bun run test src/lib/gateway.test.ts` — 7 pass (incl. 2 new)
- `cd dashboard && bun run test` — full suite 36 pass (8 files)
- `cd dashboard && bunx tsc --noEmit` — clean (no new type errors)
- guards: `grep -q '/economia' app-sidebar.tsx` OK; `! grep allSettled economia/page.tsx`
  OK; `! grep toISOString economia/page.tsx` OK; `proxyGet<EconomyResponse>("economy"` OK;
  3 `<Line>` elements, no `yAxisId` in chart code.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Installed dashboard deps + propagated ClickUp marker**
- **Found during:** Task 1 setup.
- **Issue:** The worktree had no `dashboard/node_modules` (so `vitest`/`tsc`
  were missing) and no `.planning/clickup-active-task.json` (PostToolUse hook
  would block edits — the worktree has a copy of `.planning`, not a symlink).
- **Fix:** `bun install` in dashboard/ (resolved against the existing committed
  bun.lock — lock unchanged); copied the main-repo ClickUp skip marker
  (`{"skip": true}`) into the worktree `.planning/`. Both environment-only —
  node_modules is gitignored; the marker is not part of any code commit.
- **Files modified:** none committed (environment only).

**2. [Rule 1 - Bug] economia chart color token `--status-healthy` does not exist**
- **Found during:** Task 2.
- **Issue:** The plan's analog implied a healthy/warning/neutral palette, but
  the theme (globals.css) only defines `--status-warning` beyond the radix-nova
  preset — `--status-healthy` is undefined and would render no stroke.
- **Fix:** economia line → `--primary` (the headline win), vast → `--status-warning`
  (cost), phantom → `--chart-2` — all valid theme tokens.
- **Files modified:** dashboard/src/components/economy-trend-chart.tsx
- **Commit:** 6bc4e97

## Deferred Issues

**lint gate non-functional in this repo (not introduced by this plan).**
The plan's verify includes `bun run lint`, but the dashboard package has NO
eslint/biome config tracked in git (`git ls-files dashboard | grep eslint`
empty) and no eslint dependency. `next lint` (deprecated) drops into an
interactive "configure ESLint" prompt and cannot run non-interactively. tsc
(`bunx tsc --noEmit`, clean) is the working typecheck gate and was used in its
place. Configuring eslint is out of scope (architectural + package install) —
flagged for a future tooling task.

## Self-Check: PASSED
- dashboard/src/lib/gateway.ts — FOUND
- dashboard/src/lib/gateway.test.ts — FOUND
- dashboard/src/components/economy-panel.tsx — FOUND
- dashboard/src/components/economy-trend-chart.tsx — FOUND
- dashboard/src/app/(dashboard)/economia/page.tsx — FOUND
- dashboard/src/components/app-sidebar.tsx — FOUND
- commit 6f5a0df (RED) — FOUND
- commit 2e61c9c (GREEN) — FOUND
- commit 6bc4e97 (Task 2) — FOUND
- commit 87f411f (Task 3) — FOUND

## TDD Gate Compliance
- Task 1: RED commit (test, 6f5a0df) precedes GREEN commit (feat, 2e61c9c). RED
  failed (`fetchEconomy is not a function`) before implementation; no unexpected
  pass during RED. REFACTOR not needed.
