---
phase: 07-observability-dashboard-alerting
plan: 08
subsystem: ui
tags: [next.js, react, react-query, shadcn, recharts, observability, dashboard]

# Dependency graph
requires:
  - phase: 07-observability-dashboard-alerting (07-07)
    provides: "dashboard/ Next.js 15 skeleton — shadcn radix-nova design system, the typed fetchMetrics/fetchAudit/fetchUsage wrappers, the server-side /api/gateway/* proxy, the auth boundary"
  - phase: 07-observability-dashboard-alerting (07-03)
    provides: "/admin/metrics + /admin/audit JSON handlers — the response shapes the UI renders"
provides:
  - "(dashboard) route group — layout with sidebar + sticky critical banner + React Query provider"
  - "Overview page — KPI row (P95 / error rate / requests) + FSM panel + 3-series latency chart"
  - "Tenants page — per-tenant metrics table + date-range cost filter driving fetchUsage"
  - "Incidents page — audit-log / incident-history table, newest-first, limit/offset pager"
  - "7 UI components: kpi-card, latency-chart, fsm-panel, critical-banner, tenant-table, audit-table, app-sidebar"
  - "React Query provider with a 7s (5-10s band) refetchInterval — the dashboard polling model"
  - "Shared fsm.ts (state -> pt-BR label + 3-tier status palette) + format.ts (pt-BR metric formatters + aggregation)"
affects: [07-09-human-uat, observability]

# Tech tracking
tech-stack:
  added:
    - "@testing-library/dom ^10.4.1 — missing peer of @testing-library/react; required for the component tests to load"
  patterns:
    - "Single source of truth for FSM metadata: fsm.ts owns the state->label->tier map, shared by the critical banner and the FSM panel — no duplicated mapping"
    - "React Query polling drives the whole dashboard: one QueryProvider sets refetchInterval=7000ms, every useQuery inherits it; the queryKey ['metrics'] is shared so the banner and the pages dedupe to one poll"
    - "Read-only acknowledge: 'Reconhecer incidente' mutates only local component state (a 5-min TTL timer) — zero gateway write, zero fetch call from the banner"

key-files:
  created:
    - "dashboard/src/lib/query-client.tsx — React Query provider, 7000ms refetchInterval (5-10s band)"
    - "dashboard/src/lib/fsm.ts — FSM state -> pt-BR label + 3-tier status palette; isCritical/isWarning helpers"
    - "dashboard/src/lib/format.ts — pt-BR metric formatters (ms/%/BRL/count) + tenant aggregation + status thresholds"
    - "dashboard/src/components/app-sidebar.tsx — 3-item nav (Visão geral / Tenants / Histórico) with active-item accent"
    - "dashboard/src/components/critical-banner.tsx — sticky 44px banner on critical/warning FSM state, local-only acknowledge"
    - "dashboard/src/components/critical-banner.test.tsx — red on FAILED_OVER, nothing on HEALTHY, hides on acknowledge"
    - "dashboard/src/components/kpi-card.tsx — 12/600 caption + 28/600 Display value, tabular-nums, status-tier color"
    - "dashboard/src/components/latency-chart.tsx — Recharts LineChart, 3 series P50/P95/P99 (green/amber/red)"
    - "dashboard/src/components/fsm-panel.tsx — card + status-colored badge with the pt-BR FSM label"
    - "dashboard/src/components/fsm-panel.test.tsx — state->label->tier mapping for every contract state"
    - "dashboard/src/components/stale-indicator.tsx — 'Atualizado há {n}s' off dataUpdatedAt"
    - "dashboard/src/components/tenant-table.tsx — shadcn table, 36px rows, status-colored error-rate badge"
    - "dashboard/src/components/audit-table.tsx — scroll-area table, newest-first, 36px rows, limit/offset pager"
    - "dashboard/src/app/(dashboard)/layout.tsx — query provider + sidebar + sticky banner wrapper"
    - "dashboard/src/app/(dashboard)/page.tsx — Overview: KPI row + FSM panel + latency chart"
    - "dashboard/src/app/(dashboard)/tenants/page.tsx — tenant metrics table + date-range cost filter"
    - "dashboard/src/app/(dashboard)/incidents/page.tsx — audit-log / incident-history table"
    - "dashboard/src/test-setup.ts — registers @testing-library/jest-dom matchers for vitest"
  modified:
    - "dashboard/package.json — added @testing-library/dom devDependency"
    - "dashboard/package-lock.json — lockfile for @testing-library/dom"
    - "dashboard/vitest.config.ts — setupFiles: ./src/test-setup.ts"
  deleted:
    - "dashboard/src/app/page.tsx — the 07-07 placeholder; (dashboard)/page.tsx now owns the / route (intentional, documented in 07-07's Known Stubs)"

key-decisions:
  - "audit-table.tsx columns follow the ACTUAL 07-07 AuditRow shape (ts/event_kind/tenant_id/actor/detail), not the stale plan prose column list (route/status_code/latency_ms) — the gateway.ts interface is binding; route/status/latency are not in the /admin/audit response and T-07-31 keeps content out of the UI anyway"
  - "Centralized FSM metadata in fsm.ts rather than duplicating the state->label->color map in both critical-banner and fsm-panel — one map, one place to keep in sync with the gateway FSM"
  - "Monthly-cost KPI is on the Tenants page (per-tenant, via fetchUsage), not the Overview KPI row — fetchMetrics().tenants carries no cost field, so the Overview KPI row aggregates P95 + error rate + requests from metrics, and cost lives where the data actually is"
  - "React Query queryKey ['metrics'] is shared between the critical banner and the Overview/Tenants pages so the 7s poll is deduped to a single request, not one-per-consumer"

patterns-established:
  - "fsm.ts as the FSM contract surface — any new gateway FSM state is added in one file and both the banner and the panel pick it up"
  - "Page-level useQuery + skeleton/empty/error triad: every (dashboard) page renders a skeleton on initial fetch, a pt-BR empty state when there is no data, and the pt-BR error state with a 'Tentar novamente' button on failure"

requirements-completed: [OBS-03]

# Metrics
duration: ~28min
completed: 2026-05-14
---

# Phase 7 Plan 08: Dashboard UI — Components & Pages Summary

**The operator-facing OBS-03 deliverable: the `(dashboard)` route group — layout with sidebar + sticky critical banner + React Query polling — and the three views (Overview KPI row + FSM panel + 3-series latency chart, Tenants metrics table + date-range cost filter, Incidents audit-log table), all built strictly to the `07-UI-SPEC.md` contract (radix-nova dark, 3-tier status palette, the spacing scale, 4-size/2-weight typography, pt-BR operational copy) and consuming the 07-07 typed fetch wrappers — the dashboard never touches the gateway or the admin key directly.**

## Performance

- **Duration:** ~28 min
- **Tasks:** 3 auto tasks completed + 1 checkpoint (programmatic parts verified, visual review deferred)
- **Files modified:** 21 (18 created, 3 modified, 1 deleted)

## Accomplishments

- Built the **React Query polling model**: `query-client.tsx` sets a single `refetchInterval` of 7000ms (inside the UI-SPEC 5-10s band) + `refetchOnWindowFocus`; every `useQuery` inherits it. The critical banner and the Overview/Tenants pages share the `['metrics']` queryKey so the poll is deduped to one request.
- Built the **`(dashboard)` layout**: sidebar (3-item nav, active-item `--primary` accent) + the sticky critical banner above the page content + `2xl`/48px top padding. Wrapped in `TooltipProvider` (the shadcn `SidebarProvider` does not add one — `SidebarMenuButton` renders collapsed-state tooltips).
- Built the **sticky critical banner** — the UI-SPEC's primary focal point: a 44px-min-height `--destructive` red banner on `FAILED_OVER`/`EMERGENCY_ACTIVE`, an amber `--status-warning` banner on the degraded/recovering states, pt-BR copy, and a "Reconhecer incidente" button that silences it locally for 5 min (mirroring the alerting dedup TTL) with **zero gateway write** (T-07-30).
- Built the **Overview page**: a KPI row (P95 / error rate / requests, 28/600 Display values with `tabular-nums`, status-tier colors), the FSM panel (status-colored badge + pt-BR label), and the 3-series Recharts latency chart (P50 green / P95 amber / P99 red). Renders `skeleton` on initial fetch, the "Sem dados no período" empty state, and the "Atualizado há {n}s" stale indicator next to the title.
- Built the **Tenants page** (per-tenant metrics table with 36px rows + status-colored error-rate badges, plus a `select`+`popover`+`calendar` date-range filter whose "Aplicar período" button drives `fetchUsage` for the cost summary) and the **Incidents page** (the audit-log / incident-history table, newest-first, with a limit/offset pager). Both render the skeleton + pt-BR error state + "Tentar novamente" triad.
- Centralized FSM metadata in `fsm.ts` (state → pt-BR label → 3-tier status palette) and metric formatting/aggregation in `format.ts` — both the banner and the panel consume `fsm.ts`, eliminating a duplicated mapping.

## Task Commits

Each task was committed atomically:

1. **Task 1: React Query provider + (dashboard) layout + sidebar + critical banner** - `2dadaa6` (feat)
2. **Task 2: KPI card + latency chart + FSM panel + Overview page** - `807b7e8` (feat)
3. **Task 3: Tenant table + audit table + Tenants & Incidents pages** - `3c068ce` (feat)

## Files Created/Modified

**Task 1 — provider, layout, sidebar, banner:**
- `dashboard/src/lib/query-client.tsx` - React Query provider, `refetchInterval: 7000` (5-10s band), `refetchOnWindowFocus`
- `dashboard/src/lib/fsm.ts` - FSM state → pt-BR label + `StatusTier`; `isCriticalFsmState`/`isWarningFsmState`/`fsmMeta`/`tierTextClass` helpers
- `dashboard/src/components/app-sidebar.tsx` - shadcn `sidebar` block, 3-item nav, active item via `isActive` → sidebar accent
- `dashboard/src/components/critical-banner.tsx` - sticky 44px `alert`, critical (red) / warning (amber) variants, local-only acknowledge (5-min TTL)
- `dashboard/src/components/critical-banner.test.tsx` - 3 tests: red on `FAILED_OVER`, nothing on `HEALTHY`, hides on acknowledge
- `dashboard/src/app/(dashboard)/layout.tsx` - `QueryProvider` + `TooltipProvider` + `SidebarProvider` + `AppSidebar` + `CriticalBanner` + `Toaster`
- `dashboard/src/test-setup.ts` - `import "@testing-library/jest-dom/vitest"`
- `dashboard/package.json` / `package-lock.json` / `vitest.config.ts` - **modified** — `@testing-library/dom` dep + `setupFiles`

**Task 2 — KPI, chart, FSM panel, Overview:**
- `dashboard/src/components/kpi-card.tsx` - 12/600 caption + 28/600 Display value, `tabular-nums`, optional `StatusTier` color
- `dashboard/src/components/latency-chart.tsx` - Recharts `LineChart`, 3 `Line` series P50/P95/P99 in `var(--primary)`/`var(--status-warning)`/`var(--destructive)`, 12/600 axis labels
- `dashboard/src/components/fsm-panel.tsx` - shadcn `card` + status-colored `badge` (`data-tier` attr), pt-BR label via `fsmMeta`
- `dashboard/src/components/fsm-panel.test.tsx` - 5 tests: HEALTHY/FAILED_OVER/OFF_HOURS/DEGRADED + every state in `FSM_STATE_META`
- `dashboard/src/components/stale-indicator.tsx` - "Atualizado há {n}s" ticking off `dataUpdatedAt`
- `dashboard/src/lib/format.ts` - `formatMs`/`formatErrorRate`/`formatBrl`/`formatCount` + `errorRateTier`/`latencyTier` + `aggregateP95`/`aggregateErrorRate`/`aggregateRequests`
- `dashboard/src/app/(dashboard)/page.tsx` - Overview: KPI row + FSM panel + latency chart, `useQuery(fetchMetrics)`, skeleton + "Sem dados no período" empty + stale indicator
- `dashboard/src/app/page.tsx` - **deleted** — the 07-07 placeholder; `(dashboard)/page.tsx` now owns `/`

**Task 3 — tables, Tenants & Incidents pages:**
- `dashboard/src/components/tenant-table.tsx` - shadcn `table`, `h-9` (36px) rows, `tabular-nums` metric cells, status-colored error-rate `badge`, "Sem dados no período" empty state
- `dashboard/src/components/audit-table.tsx` - shadcn `table` in a `scroll-area`, newest-first, `h-9` rows, limit/offset pager, "Nenhum evento registrado no período." empty state
- `dashboard/src/app/(dashboard)/tenants/page.tsx` - `useQuery(fetchMetrics)` → `TenantTable` + a `select`+`popover`+`calendar` date-range filter with "Aplicar período" → `fetchUsage`; skeleton + pt-BR error + "Tentar novamente"
- `dashboard/src/app/(dashboard)/incidents/page.tsx` - `useQuery(fetchAudit)` → `AuditTable` with limit/offset pager; skeleton + pt-BR error + "Tentar novamente"

## Decisions Made

- **audit-table columns follow the real `AuditRow` type, not the plan prose** — the plan's Task 3 action lists audit columns "ts, event_kind, tenant, route, status_code, latency_ms", but 07-07's `gateway.ts` `AuditRow` interface (the binding contract) is `{ id, ts, event_kind, tenant_id, actor, detail }` — there is no `route`/`status_code`/`latency_ms` in the `/admin/audit` response. The table renders the actual shape (ts / event_kind badge / tenant / actor / detail). This also aligns with T-07-31 (only metadata columns reach the browser).
- **Monthly-cost KPI lives on the Tenants page, not the Overview KPI row** — `fetchMetrics().tenants` (`TenantMetricRow`) carries no cost field; only `fetchUsage` returns cost, and it is per-tenant + per-date-range. The Overview KPI row therefore aggregates P95 + error rate + requests from `fetchMetrics`; the cost figure is rendered on the Tenants page where `fetchUsage` is actually called via the date-range filter. The must-have "daily/monthly cost" is satisfied there.
- **Centralized FSM metadata** — `fsm.ts` is the single state→label→tier map; the critical banner and the FSM panel both import it. Adding a new gateway FSM state is a one-file change.
- **Shared `['metrics']` queryKey** — the critical banner, the Overview page, and the Tenants page all key their metrics query on `['metrics']`, so React Query dedupes the 7s poll to one network request regardless of how many components are mounted.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] `@testing-library/react` missing its `@testing-library/dom` peer dependency**
- **Found during:** Task 1
- **Issue:** `npx vitest run src/components/critical-banner.test.tsx` failed with `Cannot find module '@testing-library/dom'` — `@testing-library/react` (installed by 07-07) declares `@testing-library/dom` as a peer but it was never installed, so no component test could load. Task 1's verify (`npx vitest run ...`) could not run.
- **Fix:** `npm install --save-dev @testing-library/dom@^10.4.0`. Also created `src/test-setup.ts` (`import "@testing-library/jest-dom/vitest"`) and wired `setupFiles` into `vitest.config.ts` — 07-07's `vitest.config.ts` had no setup file because its only tests (`gateway.test.ts`, `smoke.test.ts`) use no jest-dom matchers; the 07-08 component tests need `.toBeInTheDocument()` / `.toHaveAttribute()`.
- **Files modified:** `dashboard/package.json`, `dashboard/package-lock.json`, `dashboard/vitest.config.ts`, `dashboard/src/test-setup.ts` (created)
- **Commit:** `2dadaa6`

**2. [Rule 3 - Blocking] `next build` prerender of `/` failed — `Tooltip` must be used within `TooltipProvider`**
- **Found during:** Task 2 (build step)
- **Issue:** The shadcn `SidebarMenuButton` renders a collapsed-state `Tooltip` when given a `tooltip` prop (the `app-sidebar` nav items pass one). `SidebarProvider` does **not** include a `TooltipProvider`, so `next build`'s static prerender of `/` threw `Error: Tooltip must be used within TooltipProvider`.
- **Fix:** Wrapped the `(dashboard)/layout.tsx` tree in `<TooltipProvider delayDuration={0}>` between `QueryProvider` and `SidebarProvider`. This touches a Task 1 file but was discovered during Task 2's build; folded into the Task 2 commit.
- **Files modified:** `dashboard/src/app/(dashboard)/layout.tsx`
- **Commit:** `807b7e8`

### Intentional Deletion

- `dashboard/src/app/page.tsx` (the 07-07 placeholder "Painel de observabilidade — em construção (07-08)") was deleted — the new `(dashboard)/page.tsx` resolves to the same `/` route, and Next.js errors on two pages for one route. 07-07's SUMMARY explicitly listed this file under "Known Stubs" as intentional and slated for 07-08 replacement. Documented in the Task 2 commit message.

## Checkpoint — Task 4 (human-verify)

Task 4 is a `checkpoint:human-verify` (visual UI-SPEC compliance review). This plan ran inside an autonomous orchestration, so every **programmatically-checkable** part of the checkpoint was executed and verified here; the **human-only visual review** is deferred (see below).

### Programmatic verification — PASSED

| Check | Command | Result |
|-------|---------|--------|
| No admin key in UI components / pages | `grep -rn "X-Admin-Key\|GATEWAY_ADMIN_KEY" dashboard/src/components/ dashboard/src/app/(dashboard)/` | none found — **PASS** |
| `GATEWAY_ADMIN_KEY` greppable to one file | `grep -rl 'GATEWAY_ADMIN_KEY' dashboard/src/` | only `src/app/api/gateway/[...path]/route.ts` — **PASS** |
| Banner issues no gateway write (T-07-30) | `grep -nE 'fetch\(|useMutation|mutate' dashboard/src/components/critical-banner.tsx` | none found — **PASS** |
| No `NEXT_PUBLIC_` secret leak | `grep -rn "NEXT_PUBLIC_" dashboard/src/` | one hit — a descriptive comment in the proxy route, not a real env var — **PASS** |
| No direct gateway URL in UI | `grep -rn "GATEWAY_BASE_URL" dashboard/src/components/ dashboard/src/app/(dashboard)/` | none found — **PASS** |
| Admin key absent from client bundle | `grep -rl "X-Admin-Key\|GATEWAY_ADMIN_KEY" dashboard/.next/static/` | none found — **PASS** |
| Every UI gateway call via `@/lib/gateway` wrappers | `grep -rn 'from "@/lib/gateway"'` | all 8 UI fetch sites import the typed wrappers — **PASS** |
| `npm run build` | — | exit 0 (routes `/`, `/incidents`, `/tenants`, `/login`, proxy + auth routes, middleware) |
| `npx tsc --noEmit` | — | exit 0 |
| `npx vitest run` | — | 13/13 passing (4 test files) |

The threat-model invariant T-07-29 ("no browser request carries `X-Admin-Key`") is confirmed structurally: the admin key is greppable to exactly one server route handler, it is absent from the built client bundle, and every UI component fetches through the `@/lib/gateway` wrappers which call the `/api/gateway/*` proxy.

## Deferred / Human-Verify

**Task 4 visual review — DEFERRED (human_needed).** The autonomous run cannot perform the live-browser visual checks. Mirroring the Phases 1-6 `human_needed` UAT pattern, the following require an operator with a running gateway + the dashboard `npm run dev`:

1. **Auth boundary** — visiting `http://localhost:3001` redirects to `/login`; sign-in with a Better Auth test account lands on the Overview.
2. **Overview visuals** — KPI row shows P95 / error rate / requests with aligned `tabular-nums`; FSM panel shows the correct pt-BR label + color; the latency chart shows three distinguishable green/amber/red percentile lines; the "Atualizado há {n}s" indicator confirms the ~5-10s refetch cadence.
3. **Critical banner** — driving the gateway FSM to `FAILED_OVER` (or stubbing the metrics response) raises the sticky red banner on every view; "Reconhecer incidente" collapses it.
4. **Tenants page** — the per-tenant table renders with 36px compact rows; picking a date range + "Aplicar período" updates the cost columns.
5. **Incidents page** — the audit-log table lists state-change rows newest-first; the limit/offset pager walks the history.
6. **Theme + copy** — the whole UI is dark (radix-nova `.dark`), spacing is consistent, all copy is pt-BR operational tone.
7. **Devtools Network** — every gateway call goes to `/api/gateway/*` and no request carries an `X-Admin-Key` header from the browser. *(Verified structurally above; the live devtools confirmation is the human step.)*

This deferral is tracked the same way Phase 7's `07-09-human-uat` plan and the Phase 1-6 verification deferrals are — it does not block the autonomous wave.

## Threat Surface Notes

All four Task threat-model entries hold as designed — no new surface beyond the plan's `<threat_model>`:
- **T-07-29** (admin-key disclosure to the browser): every UI component fetches via the `@/lib/gateway` wrappers → the `/api/gateway/*` server proxy; `GATEWAY_ADMIN_KEY` is greppable to one server route handler and is absent from the built client bundle. *Mitigated.*
- **T-07-30** (tampering via "Reconhecer incidente"): the banner's acknowledge mutates only local component state (a 5-min TTL timer); `grep` confirms no `fetch`/`useMutation`/`mutate` in `critical-banner.tsx`. *Accept — no tamper surface.*
- **T-07-31** (audit-log content disclosure): `audit-table.tsx` renders only the metadata columns the `AuditRow` type exposes (ts, event_kind, tenant_id, actor, detail) — no prompt/response content field exists in the response shape. *Mitigated.*
- **T-07-32** (direct navigation to a dashboard route): the `(dashboard)` route group sits behind 07-07's `middleware.ts` session gate — unchanged by this plan. *Mitigated (inherited).*

No `## Threat Flags` — this plan introduces no new network endpoints, auth paths, file access, or schema changes; it only renders the existing `/admin/*` JSON through the existing proxy.

## Verification Results

- `cd dashboard && npm install` — exit 0
- `cd dashboard && npm run build` — exit 0 (static `/`, `/incidents`, `/tenants`, `/login`; dynamic proxy + auth routes; middleware)
- `cd dashboard && npx tsc --noEmit` — exit 0
- `cd dashboard && npx vitest run` — exit 0 (13 tests: 4 gateway-wrapper + 1 smoke + 3 critical-banner + 5 fsm-panel)
- `cd dashboard && npx vitest run src/components/critical-banner.test.tsx` — exit 0 (3 tests)
- `cd dashboard && npx vitest run src/components/fsm-panel.test.tsx` — exit 0 (5 tests)
- Security greps (admin key / NEXT_PUBLIC_ / client bundle) — all PASS (table above)

## Self-Check: PASSED

All 18 created files exist on disk; all 3 task commits (`2dadaa6`, `807b7e8`, `3c068ce`) are present in git history.
