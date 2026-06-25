---
phase: 260625-sna
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - dashboard/src/lib/consumo.ts
  - dashboard/src/components/app-sidebar.tsx
  - dashboard/src/components/consumo-trend-chart.tsx
  - dashboard/src/components/consumo-table.tsx
  - dashboard/src/app/(dashboard)/consumo/page.tsx
autonomous: true
requirements: []
must_haves:
  truths:
    - "Operator sees a 'Consumo' item in the sidebar (Observabilidade group, between Tenants e Histórico) and clicking it lands on /consumo"
    - "Page aggregates real /admin/usage data across ALL tenants client-side (fetchMetrics → dedup tenant ids → Promise.allSettled(fetchUsage))"
    - "KPI row shows aggregated Custo total R$ (cost_local_phantom_brl), Tokens entrada, Tokens saída, Áudio (s), Embeds"
    - "Trend chart plots tokens/dia and custo R$/dia merged by date across all tenants"
    - "Per-tenant table lists Tenant | Custo R$ | Tokens entrada | Tokens saída | Áudio (s) | Embeds, sorted by custo desc by default"
    - "Period filter (popover+calendar) defaults to current month (day 1 → today); from/to formatted YYYY-MM-DD in local components (NOT toISOString)"
    - "Loading shows skeletons; error shows pt-BR message with 'Tentar novamente'; partial tenant failures do not crash the page"
    - "0 values render as 0 (audio/embeds/sparse cost) — no fake placeholders"
    - "cd dashboard && npm run build passes with clean typecheck"
  artifacts:
    - path: "dashboard/src/lib/consumo.ts"
      provides: "Client-side aggregation helpers over UsageResponse[]"
      exports: ["aggregateSummary", "aggregateDaily", "perTenantRows"]
    - path: "dashboard/src/components/consumo-trend-chart.tsx"
      provides: "Dual-axis recharts LineChart (tokens/dia + custo R$/dia)"
    - path: "dashboard/src/components/consumo-table.tsx"
      provides: "Per-tenant usage table sorted by cost desc"
    - path: "dashboard/src/app/(dashboard)/consumo/page.tsx"
      provides: "Aggregated consumption page wiring filter + KPIs + chart + table"
  key_links:
    - from: "dashboard/src/app/(dashboard)/consumo/page.tsx"
      to: "fetchMetrics + fetchUsage"
      via: "useQuery queryFn: fetchMetrics then Promise.allSettled(fetchUsage per tenant)"
      pattern: "Promise.allSettled"
    - from: "dashboard/src/components/app-sidebar.tsx"
      to: "/consumo"
      via: "NAV_ITEMS array entry"
      pattern: "/consumo"
---

<objective>
Add a new "Consumo" page (`/consumo`) to the ifix-ai-dashboard that shows AGGREGATED cost/usage across ALL tenants, sourced from real `/admin/usage` data. The gateway's `/admin/usage` requires a single `tenant` and has no "all" mode, so the page aggregates client-side: list tenants via `fetchMetrics()`, fan out `fetchUsage` per tenant with `Promise.allSettled`, then sum/merge results.

Purpose: Give operators a single at-a-glance view of total spend and token/audio/embed usage instead of inspecting tenants one by one on the Tenants page.

Output: New aggregation helper, sidebar nav entry, trend chart + per-tenant table components, and the `/consumo` route page. Frontend ONLY — no backend/gateway changes.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md

# Patterns to copy (already read during planning — extract, do not re-explore):
@dashboard/src/app/(dashboard)/tenants/page.tsx
@dashboard/src/components/latency-chart.tsx
@dashboard/src/components/kpi-card.tsx
@dashboard/src/components/app-sidebar.tsx
@dashboard/src/lib/gateway.ts
@dashboard/src/lib/format.ts

<interfaces>
<!-- Contracts the executor builds against. Extracted from dashboard/src/lib/gateway.ts. -->

From dashboard/src/lib/gateway.ts:
```typescript
export interface UsageResponse {
  tenant: { id: string; slug: string; name: string; data_class: string; mode: string };
  range: { from: string; to: string; granularity: string; timezone: string };
  summary: {
    tokens_in: number; tokens_out: number; audio_seconds: number; embeds_count: number;
    cost_local_brl: number; cost_local_phantom_brl: number; cost_external_brl: number;
    cost_total_brl: number; requests_count: number;
  };
  rows: Array<{
    date: string; tokens_in: number; tokens_out: number; audio_seconds: number;
    embeds_count: number; cost_local_brl: number; cost_local_phantom_brl: number;
    cost_external_brl: number; cost_total_brl: number; requests_count: number;
  }>;
}
export interface TenantMetricRow { tenant_id: string; tenant_slug: string | null; tenant_name: string | null; /* ... */ }
export function tenantLabel(row: Pick<TenantMetricRow,"tenant_id"|"tenant_slug"|"tenant_name">): string;
export function fetchMetrics(window?: string): Promise<MetricsResponse>;   // .tenants: TenantMetricRow[]
export function fetchUsage(tenant: string, from: string, to: string): Promise<UsageResponse>;
export class GatewayError extends Error { status: number; type: string | null; }
```

From dashboard/src/lib/format.ts:
```typescript
export function formatBrl(value: number): string;   // "R$ 84,20"
export function formatCount(value: number): string;  // pt-BR integer "1.234"
```

From dashboard/src/components/kpi-card.tsx:
```typescript
export function KpiCard(props: { caption: string; value: string; status?; hint?: string }): JSX.Element;
```

shadcn primitives available under @/components/ui: card (Card/CardHeader/CardTitle/CardContent),
table (Table/TableHeader/TableBody/TableRow/TableHead/TableCell), button, popover, calendar,
skeleton, chart (ChartContainer/ChartTooltip/ChartTooltipContent/ChartLegend/ChartLegendContent, type ChartConfig).
</interfaces>
</context>

<tasks>

<task type="auto">
  <name>Task 1: Aggregation helper + sidebar nav entry</name>
  <files>dashboard/src/lib/consumo.ts, dashboard/src/components/app-sidebar.tsx</files>
  <action>
Create `dashboard/src/lib/consumo.ts` exporting three pure functions over an array of `UsageResponse` (import the type from `@/lib/gateway`). These are the contracts the page consumes — define them first.

1. `aggregateSummary(responses: UsageResponse[])` returns an object summing each numeric field of `summary` across all responses: at minimum `cost_local_phantom_brl`, `cost_total_brl`, `tokens_in`, `tokens_out`, `audio_seconds`, `embeds_count`, `requests_count`. Empty array → all zeros.

2. `aggregateDaily(responses: UsageResponse[])` merges every response's `rows` by `date`: for each date sum `tokens_in + tokens_out` into a `tokens` field and sum `cost_local_phantom_brl` into a `cost_brl` field. Return `Array<{ date: string; tokens: number; cost_brl: number }>` sorted by `date` ascending (string compare on YYYY-MM-DD is correct). Use a `Map<string, ...>` keyed by date.

3. `perTenantRows(responses: UsageResponse[])` maps each response to one row `{ tenant_id, label, cost_local_phantom_brl, tokens_in, tokens_out, audio_seconds, embeds_count }` taken from `response.summary`, with `label = response.tenant.name || response.tenant.slug || response.tenant.id`. Return sorted by `cost_local_phantom_brl` descending. Export an interface `TenantUsageRow` for the row shape and a `DailyAggRow` interface for the daily shape so the chart/table components import them.

Add JSDoc per the codebase convention. Do NOT invent fields the UsageResponse handler does not emit.

In `dashboard/src/components/app-sidebar.tsx`: import a lucide icon (`Receipt`) and add an entry `{ href: "/consumo", label: "Consumo", icon: Receipt }` to `NAV_ITEMS`, positioned between the Tenants entry and the Histórico de incidentes entry. Keep the `as const` and existing active-route logic untouched (it already uses `pathname.startsWith(item.href)`).
  </action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix/dashboard && npx tsc --noEmit 2>&1 | tail -5; grep -q '/consumo' src/components/app-sidebar.tsx && grep -q 'export function aggregateDaily' src/lib/consumo.ts && echo OK</automated>
  </verify>
  <done>consumo.ts exports aggregateSummary/aggregateDaily/perTenantRows + TenantUsageRow/DailyAggRow types; sidebar NAV_ITEMS contains the /consumo entry between Tenants and Histórico; typecheck clean.</done>
</task>

<task type="auto">
  <name>Task 2: Trend chart + per-tenant table components</name>
  <files>dashboard/src/components/consumo-trend-chart.tsx, dashboard/src/components/consumo-table.tsx</files>
  <action>
Create `dashboard/src/components/consumo-trend-chart.tsx` — a `"use client"` recharts `LineChart` modeled on `latency-chart.tsx`. Props: `{ rows: DailyAggRow[] }` (import `DailyAggRow` from `@/lib/consumo`). Use `ChartContainer` + `ChartConfig` with two series: `tokens` (label "Tokens/dia", color `var(--primary)`) and `cost_brl` (label "Custo R$/dia", color `var(--status-warning)`). XAxis `dataKey="date"`. Use TWO Y axes via `yAxisId`: left axis (`yAxisId="tokens"`) for the tokens line, right axis (`yAxisId="cost"`, `orientation="right"`) for the cost line — tokens are large and cost is sparse/small, so a shared axis would flatten cost to zero. Each `<Line>` sets its matching `yAxisId`, `type="monotone"`, `strokeWidth={2}`, `dot={false}`, stroke `var(--color-tokens)` / `var(--color-cost)`. Include `ChartTooltip`/`ChartTooltipContent` and `ChartLegend`/`ChartLegendContent`. Axis labels use `className="text-[12px] font-semibold"` per UI-SPEC. Do NOT inline a currency formatter into the axis; a plain numeric tick is fine.

Create `dashboard/src/components/consumo-table.tsx` — a `"use client"` table built on shadcn `@/components/ui/table`. Props: `{ rows: TenantUsageRow[] }` (import from `@/lib/consumo`). Columns in order: Tenant (`row.label`), Custo R$ (`formatBrl(row.cost_local_phantom_brl)` from `@/lib/format`), Tokens entrada (`formatCount(row.tokens_in)`), Tokens saída (`formatCount(row.tokens_out)`), Áudio (s) (`formatCount(row.audio_seconds)`), Embeds (`formatCount(row.embeds_count)`). Render rows in the order received (page passes them already sorted by cost desc). Numeric cells use `className="tabular-nums text-right"` and matching `TableHead` right-aligned. Render 0 as 0 — no placeholder substitution. Use `row.tenant_id` as the React key.
  </action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix/dashboard && npx tsc --noEmit 2>&1 | tail -5 && test -f src/components/consumo-trend-chart.tsx && test -f src/components/consumo-table.tsx && echo OK</automated>
  </verify>
  <done>Both components compile, import their prop types from @/lib/consumo, use the shadcn chart/table primitives, and render the specified columns/series. Dual Y axes present in the chart.</done>
</task>

<task type="auto">
  <name>Task 3: /consumo page wiring filter + KPIs + chart + table</name>
  <files>dashboard/src/app/(dashboard)/consumo/page.tsx</files>
  <action>
Create `dashboard/src/app/(dashboard)/consumo/page.tsx` as a `"use client"` default-export page, mirroring `tenants/page.tsx` structure and pt-BR copy.

Copy the `isoDate(d: Date)` helper verbatim from tenants/page.tsx (local Y-M-D components, NOT `toISOString` — see the WR-08 comment; preserve that rationale in a short JSDoc) and an `errorMessage(error)` helper identical to tenants/page.tsx.

State: a `range: DateRange | undefined` for the popover+calendar, initialized to the current month — `from = new Date(year, month, 1)`, `to = new Date()` (today). Keep an `applied` range string pair `{ from, to }` initialized from that default so data loads on first render without requiring "Aplicar período". Provide an "Aplicar período" button that sets `applied` from the picked range (guard both ends defined).

Data: a single `useQuery` keyed `["consumo", applied]` whose `queryFn` is async:
  1. `const metrics = await fetchMetrics();`
  2. dedup tenant ids: `Array.from(new Map((metrics.tenants ?? []).map(t => [t.tenant_id, t])).values())` → list of unique tenant_ids (copy the dedup pattern from tenants/page.tsx L99-106).
  3. `const settled = await Promise.allSettled(ids.map(id => fetchUsage(id, applied.from, applied.to)));`
  4. collect `responses = settled.filter(r => r.status === "fulfilled").map(r => r.value)`.
  5. count `failures = settled.length - responses.length`.
  6. return `{ summary: aggregateSummary(responses), daily: aggregateDaily(responses), tenants: perTenantRows(responses), failures, total: settled.length }`.
Partial failures must NOT throw — only surface the error state if `fetchMetrics` itself throws (queryFn rejects) or zero responses succeeded. Import `aggregateSummary`, `aggregateDaily`, `perTenantRows` from `@/lib/consumo`.

Layout (top to bottom, `flex flex-col gap-8`):
  - Header `<h1>` "Consumo" matching the tenants page heading classes, plus a `StaleIndicator updatedAt={query.dataUpdatedAt}` (import from `@/components/stale-indicator`, same as tenants page).
  - Period filter row: the popover+calendar+"Aplicar período" button copied from tenants/page.tsx (no tenant `Select` — this page is all-tenants). Show the selected range label via `isoDate`.
  - If `query.data.failures > 0`, render a subtle `text-[12px] text-muted-foreground` note like `{failures} de {total} tenants não retornaram dados no período.` (honest partial-failure hint, no fake numbers).
  - KPI row: a `grid grid-cols-2 gap-4 sm:grid-cols-5` of `KpiCard` (import from `@/components/kpi-card`): Custo total → `formatBrl(summary.cost_local_phantom_brl)`; Tokens entrada → `formatCount(summary.tokens_in)`; Tokens saída → `formatCount(summary.tokens_out)`; Áudio → `formatCount(summary.audio_seconds)` with caption "Áudio (s)"; Embeds → `formatCount(summary.embeds_count)`.
  - A `Card` titled "Tendência" wrapping `<ConsumoTrendChart rows={query.data.daily} />`.
  - A `Card` titled "Consumo por tenant" wrapping `<ConsumoTable rows={query.data.tenants} />`.

States: while `query.isLoading` render `Skeleton` blocks (KPI strip + chart + table heights, like tenants page uses `<Skeleton className="h-48 w-full" />`). On `query.isError` render the centered pt-BR error block with a "Tentar novamente" `Button variant="outline"` calling `query.refetch()` — copy the markup from tenants/page.tsx. Honesty: render 0 values as 0; optionally show "sem dados no período" muted text when `daily` is empty, but never substitute fake numbers.
  </action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix/dashboard && npm run build 2>&1 | tail -15 && grep -q 'Promise.allSettled' src/app/\(dashboard\)/consumo/page.tsx && echo BUILD_OK</automated>
  </verify>
  <done>`/consumo` route builds clean (Next standalone), aggregates all tenants via fetchMetrics + Promise.allSettled(fetchUsage), renders KPI row + trend chart + per-tenant table + period filter defaulting to current month, and handles loading/error/partial-failure states in pt-BR.</done>
</task>

</tasks>

<verification>
- `cd dashboard && npm run build` passes (Next.js standalone, includes typecheck).
- `cd dashboard && npm run lint` reports no new errors for the added files.
- Sidebar shows Consumo between Tenants and Histórico; navigating to `/consumo` renders the page.
- Aggregation is client-side over all tenants; a single failing tenant does not blank the page.
</verification>

<success_criteria>
- New `/consumo` page exists and builds.
- KPI row, trend chart (tokens/dia + custo R$/dia), and per-tenant table all driven by aggregated real `/admin/usage` data.
- Period filter defaults to current month, formats dates as local YYYY-MM-DD (no UTC shift).
- 0 values render honestly; partial tenant failures tolerated.
- No backend/gateway files touched.
</success_criteria>

<output>
Create `.planning/quick/260625-sna-dashboard-nova-pagina-consumo-tier-1-cus/260625-sna-SUMMARY.md` when done.
</output>
