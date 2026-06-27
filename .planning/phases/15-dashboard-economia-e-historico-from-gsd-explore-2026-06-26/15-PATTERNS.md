# Phase 15: dashboard-economia-e-historico - Pattern Map

**Mapped:** 2026-06-26
**Files analyzed:** 17 (8 gateway/Go, 8 dashboard/TS, 1 codegen output)
**Analogs found:** 17 / 17 (every file clones an in-repo analog — zero greenfield shapes)

> This phase is **pure reuse**. There is no novel logic. The failure mode is divergence
> from the established `usage.go` / `consumo` shapes. Every excerpt below is the exact
> shape to replicate. All line numbers verified this session.

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `gateway/db/queries/billing.sql` (MODIFY: +2 queries) | query/sql | CRUD (aggregate read) | same file `SumBillingEventsByDate`/`Range` (l.43-77) | exact |
| `gateway/db/queries/primary_lifecycles.sql` (MODIFY: +1 query) | query/sql | CRUD (range read) | same file `ListPrimaryLifecycles` (l.65-77) | exact |
| `gateway/db/queries/audit.sql` (MODIFY ListAudit + ADD CountAudit) | query/sql | CRUD (paged read + count) | same file `ListAuditStateChanges` (l.9-23) | exact |
| `gateway/internal/db/gen/*.sql.go` (regen) | codegen output | — | `sqlc generate` (committed; CI gate) | exact |
| `gateway/internal/admin/economy.go` (NEW) | controller/handler | request-response (read aggregate) | `gateway/internal/admin/usage.go` | exact |
| `gateway/internal/admin/economy_test.go` (NEW) | test | — | `operations_test.go` + `audit_test.go` | exact |
| `gateway/internal/admin/audit.go` (MODIFY) | controller/handler | request-response | itself + `usage.go` tz block (l.132-150) | exact |
| `gateway/internal/admin/audit_test.go` (MODIFY) | test | — | itself (l.17-48) | exact |
| `gateway/cmd/gateway/main.go` (MODIFY: 3 edits) | config/wiring | — | existing `adminOperationsHandler` wiring | exact |
| `dashboard/src/lib/gateway.ts` (MODIFY: +EconomyResponse/fetchEconomy, +AuditResponse.total) | service/client | request-response | itself (`fetchUsage`/`fetchOperations`/`AuditResponse`) | exact |
| `dashboard/src/components/economy-panel.tsx` (NEW) | component | presentation | `operacao-cost-panel.tsx` | exact |
| `dashboard/src/components/economy-trend-chart.tsx` (NEW) | component | presentation (time-series) | `consumo-trend-chart.tsx` | exact |
| `dashboard/src/app/(dashboard)/economia/page.tsx` (NEW) | page/route | request-response | `consumo/page.tsx` | exact (drop fan-out) |
| `dashboard/src/app/(dashboard)/incidents/page.tsx` (MODIFY) | page/route | request-response | `consumo/page.tsx` (date-range) + itself | exact |
| `dashboard/src/components/audit-table.tsx` (MODIFY pager) | component | presentation | itself (l.69-73, 125-148) | exact |
| `dashboard/src/components/app-sidebar.tsx` (MODIFY: +1 nav) | component | presentation | itself (l.28-34) | exact |
| `dashboard/src/lib/gateway.test.ts` (MODIFY) | test | — | itself (existing fetch wrapper tests) | exact |

**No proxy change:** `dashboard/src/app/api/gateway/[...path]/route.ts` forwards ANY `/admin/*`
GET generically (l.42-44) — economy + audit-with-params ride it unchanged. Verified.

---

## Pattern Assignments

### `gateway/db/queries/billing.sql` (query/sql, aggregate read) — MODIFY

**Analog:** `SumBillingEventsByDate` / `SumBillingEventsRange` in the same file (l.43-77).

The new queries are byte-for-byte the same shape MINUS the `WHERE tenant_id = $1` predicate —
that omission IS the OBS-09 blocker fix (`operations.go:18-21` left phantom nil because no
no-tenant SUM existed). Note params shift: `$1/$2` (ts range) instead of `$2/$3`.

**Existing analog excerpt** (`billing.sql:43-61` — copy the tz idiom + COALESCE casts verbatim):
```sql
-- name: SumBillingEventsByDate :many
SELECT
    (ts AT TIME ZONE 'America/Sao_Paulo')::date AS date,
    ...
    COALESCE(SUM(cost_local_phantom_brl), 0)::numeric(20,6) AS cost_local_phantom_brl,
    COUNT(*)::bigint                                    AS requests_count
FROM ai_gateway.billing_events
WHERE tenant_id = $1
  AND ts >= $2
  AND ts <  $3
GROUP BY (ts AT TIME ZONE 'America/Sao_Paulo')::date
ORDER BY date;
```

**New queries to add** (drop `tenant_id`, renumber params, keep the BRT tz idiom):
```sql
-- name: SumPhantomAllTenantsByDate :many
-- Gateway-WIDE phantom sum (NO tenant filter) bucketed by BRT day — OBS-09 blocker.
SELECT
    (ts AT TIME ZONE 'America/Sao_Paulo')::date AS date,
    COALESCE(SUM(cost_local_phantom_brl), 0)::numeric(20,6) AS phantom_brl
FROM ai_gateway.billing_events
WHERE ts >= $1 AND ts < $2
GROUP BY (ts AT TIME ZONE 'America/Sao_Paulo')::date
ORDER BY date;

-- name: SumPhantomAllTenantsRange :one
SELECT COALESCE(SUM(cost_local_phantom_brl), 0)::numeric(20,6) AS phantom_brl
FROM ai_gateway.billing_events
WHERE ts >= $1 AND ts < $2;
```

**Critical idiom (do NOT deviate):** tz is `(ts AT TIME ZONE 'America/Sao_Paulo')::date` — the
`CURRENT_DATE`+tz form is invalid (flagged at `billing.sql:7-10`). Index watch-item: no
`(ts)`-leading index serves a no-tenant scan → seq-scan over the month partition; volume is
low, acceptable (RESEARCH Pitfall 7). Do NOT pre-add an index.

---

### `gateway/db/queries/primary_lifecycles.sql` (query/sql, range read) — MODIFY

**Analog:** `ListPrimaryLifecycles` (l.65-77). Same SELECT column list; replace the
single `started_at >= $1` lower bound with a `[from,to)` overlap so a lifecycle is
captured for the economy window.

**Existing analog (`primary_lifecycles.sql:70-76`):**
```sql
SELECT id, started_at, drain_started_at, ended_at, trigger_reason,
       vast_offer_id, vast_instance_id, accepted_dph, total_cost_brl,
       shutdown_reason, leader_replica
FROM ai_gateway.primary_lifecycles
WHERE started_at >= $1
ORDER BY started_at DESC
LIMIT $2;
```

**New `ListPrimaryLifecyclesInRange :many`** — same columns, add upper bound (`started_at < $2`).
Bucketing happens Go-side by `started_at.In(loc)` (one lifecycle = one BRT day, RESEARCH A1).
The `accepted_dph` + `total_cost_brl` columns feed the exact accrual at `operations.go:214-245`.

---

### `gateway/db/queries/audit.sql` (query/sql, paged read + count) — MODIFY + ADD

**Analog:** `ListAuditStateChanges` (l.9-23). Add `from`/`to`/`search` params (single reused
`$3` ILIKE) and a parallel `CountAuditStateChanges`.

**Existing analog (`audit.sql:18-23`):**
```sql
SELECT ts, request_id, tenant_id, route, method, upstream, status_code,
       latency_ms, error_code, event_kind, reason
FROM ai_gateway.audit_log
WHERE event_kind IS NOT NULL
ORDER BY ts DESC
LIMIT $1 OFFSET $2;
```

**Modified + new (note param renumber — limit/offset become `$4/$5`; RESEARCH Pitfall 8):**
```sql
-- name: ListAuditStateChanges :many
SELECT ts, request_id, tenant_id, route, method, upstream, status_code,
       latency_ms, error_code, event_kind, reason
FROM ai_gateway.audit_log
WHERE event_kind IS NOT NULL
  AND ts >= $1 AND ts < $2
  AND ($3 = '%' OR route ILIKE $3 OR reason ILIKE $3
       OR error_code ILIKE $3 OR event_kind ILIKE $3)
ORDER BY ts DESC
LIMIT $4 OFFSET $5;

-- name: CountAuditStateChanges :one
SELECT COUNT(*)::bigint AS total
FROM ai_gateway.audit_log
WHERE event_kind IS NOT NULL
  AND ts >= $1 AND ts < $2
  AND ($3 = '%' OR route ILIKE $3 OR reason ILIKE $3
       OR error_code ILIKE $3 OR event_kind ILIKE $3);
```

**Security invariant (preserve):** select ONLY `audit_log` metadata — NEVER
`audit_log_content` prompts/responses (T-07-09). `search` is a parameterized ILIKE arg,
never string-concatenated. After editing SQL, run `sqlc generate` and **read the new
`ListAuditStateChangesParams` struct in `internal/db/gen/audit.sql.go`** before touching
the handler (sqlc auto-names range params `Ts`/`Ts_2`).

---

### `gateway/internal/admin/economy.go` (controller/handler, request-response) — NEW

**Analog:** `gateway/internal/admin/usage.go` (clone the 5-part contract EXACTLY).

**Imports pattern** (`usage.go:13-27`):
```go
import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)
// + "github.com/ifixtelecom/gpu-ifix/gateway/internal/config"  (for cfg.USDToBRLRate)
// + "github.com/jackc/pgx/v5/pgtype"                            (numericPtr already in operations.go)
```

**Query-interface isolation** (mirror `usage.go:85-90`) — list ONLY the sqlc methods used:
```go
type economyQueries interface {
	SumPhantomAllTenantsByDate(ctx context.Context, arg gen.SumPhantomAllTenantsByDateParams) ([]gen.SumPhantomAllTenantsByDateRow, error)
	SumPhantomAllTenantsRange(ctx context.Context, arg gen.SumPhantomAllTenantsRangeParams) (gen.SumPhantomAllTenantsRangeRow, error)
	ListPrimaryLifecyclesInRange(ctx context.Context, arg gen.ListPrimaryLifecyclesInRangeParams) ([]gen.ListPrimaryLifecyclesInRangeRow, error)
}
```

**Dual constructor** (mirror `usage.go:99-113`) — `NewEconomyHandler(q *gen.Queries, cfg config.Config, log)`
for prod + `newEconomyHandlerWithQueries(q economyQueries, cfg config.Config, log)` for tests.
Economy needs `cfg` (unlike usage) for `cfg.USDToBRLRate` in the open-lifecycle accrual.

**tz + param parse block (copy VERBATIM from `usage.go:132-150`):**
```go
loc, err := time.LoadLocation("America/Sao_Paulo")
// ... 500 on err ...
fromT, ferr := time.ParseInLocation("2006-01-02", from, loc)
toT, terr := time.ParseInLocation("2006-01-02", to, loc)
// ... 400 invalid_date on err ...
toT = toT.Add(24 * time.Hour) // exclusive end
```

**Error envelope on EVERY failure** (`usage.go:125-130`, `:162-168`):
```go
httpx.WriteOpenAIError(w, http.StatusInternalServerError, "api_error", "economy_query_failed", "")
obs.GatewayAdminRequests.WithLabelValues("/admin/economy", "5xx").Inc()
return
```
Increment the admin metric on the 2xx path too (`usage.go:239`), label `"/admin/economy"`.

**Vast-cost accrual (copy `operations.go:214-245` EXACTLY — closed→total_cost_brl, open→dph×hours×rate):**
```go
var cost float64
if row.EndedAt.Valid {
	if f := numericPtr(row.TotalCostBrl); f != nil { cost = *f }
} else if dph := numericPtr(row.AcceptedDph); dph != nil {
	hours := now.Sub(row.StartedAt).Hours()
	if hours < 0 { hours = 0 }
	cost = *dph * hours * h.cfg.USDToBRLRate
}
// bucket by row.StartedAt.In(loc) date (operations.go:242)
```
`numericPtr` already exists at `operations.go:363-373` (same package — reuse directly).
`pgtype.Numeric.Float64Value()` for the SUM rows (mirror `usage.go:183-185`).

**Response shape (mirror `UsageResponse` {range, summary, rows} — usage.go:29-81). ROI nullable-ptr (Pitfall 3):**
```go
type EconomyResponse struct {
	Range   RangeSection     `json:"range"`
	Summary EconomySummary   `json:"summary"`
	Series  []EconomyDayRow  `json:"series"`
}
type EconomySummary struct {
	PhantomBRL         float64  `json:"phantom_brl"`
	VastBRL            float64  `json:"vast_brl"`
	EconomiaLiquidaBRL float64  `json:"economia_liquida_brl"`
	ROIMultiplier      *float64 `json:"roi_multiplier"` // nil (JSON null) when vast_brl == 0
}
```
ROI as `*float64` → `nil` when `vast_brl == 0` (NEVER emit Inf/NaN). Mirrors the nullable-ptr
idiom already used for `CostBRL` (`operations.go:92-93, 363-373`).

**Phantom is NOT summed into a "total"** — keep it separate; economy = phantom − vast
(`usage.go:206-209` explicitly excludes phantom from cost_total).

---

### `gateway/internal/admin/economy_test.go` (test) — NEW

**Analog:** `operations_test.go:21-120` (fake-queries + helpers) and `audit_test.go:17-48`.

Clone the fake-double pattern (`operations_test.go:24-38`), the `opNumeric`/`opTimestamptz`/`opInt8`
builders (`operations_test.go:43-57`), and `opTestCfg()` (`:81-90`, supplies `USDToBRLRate:5.0`).
Required cases: gateway-wide sum across rows, ROI nil when vast=0 (no Inf/NaN), series bucketing,
400 on bad date. Decode into a pointer-ROI struct (mirror `opResponse` at `:94-120`).

---

### `gateway/internal/admin/audit.go` (controller/handler) — MODIFY

**Analog:** itself + `usage.go:132-150` tz block.

Add to the existing handler (`audit.go:98-169`):
1. Parse `from`/`to` via the `usage.go:132-150` tz block (copy verbatim); default to current-month
   when absent (Pitfall 6 — partitions only cover recent months).
2. Build `searchPattern := "%"; if q := r.URL.Query().Get("search"); q != "" { searchPattern = "%"+q+"%" }`.
3. Pass the new params into `ListAuditStateChangesParams` alongside existing `Limit`/`Offset`
   (`audit.go:132-135`) — **re-read the regenerated param struct first** (param renumber, Pitfall 8).
4. Call the new `CountAuditStateChanges`, add `Total int64 \`json:"total"\`` to `AuditResponse`
   (`audit.go:44-48`).

Keep the existing `pgTextPtr` nullable rendering (`audit.go:171-179`), the limit cap
`maxAuditLimit=200` (`:40`), and per-branch `obs.GatewayAdminRequests...WithLabelValues("/admin/audit", ...)`.

---

### `gateway/cmd/gateway/main.go` (config/wiring) — MODIFY (3 edits, mirror adminOperationsHandler)

**Analog:** the `adminOperationsHandler` wiring already present.

1. **struct field** next to `adminOperationsHandler http.Handler` (l.110):
   `adminEconomyHandler http.Handler`
2. **construct** next to l.1244:
   `adminEconomyHandler := admin.NewEconomyHandler(gen.New(pool), cfg, log)`
3. **assign to px** next to l.1263 (`adminEconomyHandler: adminEconomyHandler,`) then **mount**
   next to l.1485:
```go
if px.adminEconomyHandler != nil {
	adminRouter.Method(http.MethodGet, "/economy", px.adminEconomyHandler)
}
```
The `X-Admin-Key` middleware already gates the whole sub-router (l.1470 region) — no auth code.

---

### `dashboard/src/lib/gateway.ts` (service/client) — MODIFY

**Analog:** itself — `fetchUsage`/`fetchOperations` (l.340-352), `UsageResponse` (l.153-190),
`AuditResponse` (l.144-148), `fetchAudit` (l.333-338).

**Add `EconomyResponse` + `fetchEconomy`** (mirror `fetchUsage` shape, field-for-field with the Go struct):
```ts
export interface EconomyResponse {
  range: { from: string; to: string; timezone: string };
  summary: {
    phantom_brl: number;
    vast_brl: number;
    economia_liquida_brl: number;
    roi_multiplier: number | null; // null when vast_brl == 0
  };
  series: Array<{ date: string; phantom_brl: number; vast_brl: number; economia_brl: number }>;
}
export function fetchEconomy(from: string, to: string): Promise<EconomyResponse> {
  return proxyGet<EconomyResponse>("economy", { from, to });
}
```
All wrappers go through the existing `proxyGet<T>` helper (l.298-322) — never fetch the gateway
directly (T-07-24).

**Modify `AuditResponse` + `fetchAudit`** — add `total: number` to the interface (l.144-148; the
comment at l.141-143 saying "no total" must be updated), and add `from`/`to`/`search` args:
```ts
export function fetchAudit(limit = 50, offset = 0, from?: string, to?: string, search?: string): Promise<AuditResponse> {
  return proxyGet<AuditResponse>("audit", {
    limit: String(limit), offset: String(offset),
    ...(from ? { from } : {}), ...(to ? { to } : {}), ...(search ? { search } : {}),
  });
}
```

---

### `dashboard/src/components/economy-panel.tsx` (component) — NEW

**Analog:** `operacao-cost-panel.tsx` (KPI-card grid in a `Card`).

Mirror the structure: `Card`>`CardHeader`/`CardTitle` + `CardContent` with a
`grid grid-cols-1 ... sm:grid-cols-3` of `KpiCard`s (`operacao-cost-panel.tsx:36-48`).
Use `KpiCard` (`@/components/kpi-card`, props `caption`/`value`/`hint`) and `formatBrl`
(`@/lib/format:25-30`). Render the 5 CONTEXT metrics: Líquido R$, ROI multiplier
(render `"—"` when `roi_multiplier === null` — Pitfall 3), Custo OpenRouter fallback,
% servido local, Horas pod UP. Import the type from `@/lib/gateway` (mirror
`operacao-cost-panel.tsx:18` importing `OperationsVastCost`).

---

### `dashboard/src/components/economy-trend-chart.tsx` (component, time-series) — NEW

**Analog:** `consumo-trend-chart.tsx` (Recharts `LineChart` via shadcn `chart` block).

Copy the imports (`consumo-trend-chart.tsx:16-31`), `chartConfig` satisfies-`ChartConfig`
pattern (`:35-38`), and the `ChartContainer`>`LineChart` body (`:45-94`). **Key difference:**
all three values are BRL → ONE shared Y axis (drop the dual `yAxisId` the consumo chart needs
for tokens-vs-cost). Three `<Line>`s: `phantom_brl`, `vast_brl`, `economia_brl`, each
`stroke="var(--color-<key>)"`, `strokeWidth={2}`, `dot={false}`, `type="monotone"`. Must start
with `"use client";`.

---

### `dashboard/src/app/(dashboard)/economia/page.tsx` (page/route) — NEW

**Analog:** `consumo/page.tsx` — BUT **drop the per-tenant fan-out** (the whole point of OBS-09
is a single server-side sum; RESEARCH anti-pattern + Pitfall 5).

Copy verbatim: `isoDate()` (`:65-67`, NOT `toISOString` — WR-08), `currentMonthRange()` (`:69-76`),
the `range`/`applied` state + `applyPeriod()` (`:78-130`), the Popover+Calendar date-range block
(`:140-163`), and the loading/error/data branches (`:165-244`). **Replace** the `useQuery` body —
instead of `fetchMetrics` + `Promise.allSettled(fetchUsage...)`, call a single
`fetchEconomy(applied.from, applied.to)`:
```ts
const query = useQuery({
  queryKey: ["economia", applied],
  queryFn: () => fetchEconomy(applied.from, applied.to),
});
```
Render `<EconomyPanel summary={query.data.summary} />` + `<EconomyTrendChart rows={query.data.series} />`.
No partial-failure note (no fan-out). Reuse `StaleIndicator`, `Skeleton`, the pt-BR error state
with "Tentar novamente" (`:171-180`).

---

### `dashboard/src/app/(dashboard)/incidents/page.tsx` (page/route) — MODIFY

**Analog:** `consumo/page.tsx` date-range block + its own existing structure.

Add the Popover+Calendar date-range (copy `consumo/page.tsx:140-163`) + a search `Input`
(`@/components/ui/input`; verify it exists, else `npx shadcn add input` — RESEARCH A4) above the
table. Add `range`/`applied`/`search` state + `isoDate`/`currentMonthRange` helpers (copy from
consumo). Thread the new args into the existing `useQuery` (`incidents/page.tsx:34-37`):
```ts
queryKey: ["audit", offset, applied, search],
queryFn: () => fetchAudit(PAGE_SIZE, offset, applied.from, applied.to, search),
```
Pass `total={data?.total}` to `AuditTable`. Keep the existing loading/error branches (`:55-70`).

---

### `dashboard/src/components/audit-table.tsx` (component) — MODIFY pager

**Analog:** itself (l.69-73 pager math, l.125-148 pager UI).

Add `total?: number` to `AuditTableProps` (l.35-43). Replace the inferred
`canNext = rows.length >= limit` (l.73, plus update the comment at l.71-72 and the docstring
l.14-17) with a real `canNext = total !== undefined ? offset + limit < total : rows.length >= limit`.
Optionally show `from–to de {total}` in the pager footer (l.127-129). Keep `key={row.request_id}`
(l.100) and the existing column set.

---

### `dashboard/src/components/app-sidebar.tsx` (component) — MODIFY (+1 nav)

**Analog:** itself — the `NAV_ITEMS` array (l.28-34).

Add one entry, choosing a `lucide-react` icon (import alongside l.14; e.g. `PiggyBank` or
`TrendingUp`):
```ts
{ href: "/economia", label: "Economia", icon: TrendingUp },
```
The active-route + render logic (l.55-74) handles it automatically.

---

### `dashboard/src/lib/gateway.test.ts` (test) — MODIFY

**Analog:** itself (existing fetch-wrapper tests). Add `fetchEconomy` (URL/params + typed shape)
and the new `AuditResponse.total` + `fetchAudit(from,to,search)` params.

---

## Shared Patterns

### Admin handler 5-part contract (Go)
**Source:** `gateway/internal/admin/usage.go` (canonical), also `audit.go`, `operations.go`, `metrics.go`.
**Apply to:** `economy.go` (new), `audit.go` (extend).
1. private `xQueries` interface listing only the sqlc methods used (test injection without pgxpool)
2. dual constructor `NewXHandler(*gen.Queries, ...)` + `newXHandlerWithQueries(xQueries, ...)`
3. `httpx.WriteOpenAIError(w, status, type, code, msg)` on every failure branch
4. `obs.GatewayAdminRequests.WithLabelValues("/admin/<route>", "2xx"|"4xx"|"5xx").Inc()` per branch
5. `America/Sao_Paulo` tz: `time.LoadLocation` + `time.ParseInLocation("2006-01-02", ...)` + `toT.Add(24h)`

### sqlc codegen contract
**Source:** `gateway/sqlc.yaml` + `.github/workflows/build-gateway.yml:61-67`.
**Apply to:** all 3 query-file edits.
Edit `db/queries/*.sql` → `cd gateway && sqlc generate` → `git add internal/db/gen/` → commit.
CI runs `git diff --exit-code internal/db/gen/` — uncommitted drift fails the build.

### Nullable pgtype → JSON null (Go)
**Source:** `operations.go:363-373` (`numericPtr`), `audit.go:171-179` (`pgTextPtr`), `usage.go:183-185` (`.Float64Value()`).
**Apply to:** `economy.go` (reuse `numericPtr` directly — same package), ROI `*float64`.
Distinguish "not computed" (null) from "zero" — never emit Inf/NaN/0-as-null.

### Typed client wrapper through the proxy (TS)
**Source:** `dashboard/src/lib/gateway.ts` — `proxyGet<T>` (l.298-322) + `GatewayError` (l.274-283).
**Apply to:** `fetchEconomy`, modified `fetchAudit`. Interface mirrors the Go struct field-for-field;
browser never reads `GATEWAY_ADMIN_KEY` (T-07-24 — only `route.ts` does).

### Date-range picker (TS)
**Source:** `consumo/page.tsx:65-76` (`isoDate`/`currentMonthRange`), `:140-163` (Popover+Calendar).
**Apply to:** `economia/page.tsx`, `incidents/page.tsx`.
Use LOCAL date components, NOT `toISOString()` (WR-08 — shifts the calendar day by UTC offset).
Default to current month (Pitfall 6 — partitions only cover recent months).

### pt-BR loading/error UX (TS)
**Source:** `consumo/page.tsx:165-180`, `incidents/page.tsx:55-70`.
**Apply to:** `economia/page.tsx`. `Skeleton` while loading; `GatewayError.message` + "Tentar
novamente" on error; `StaleIndicator updatedAt={query.dataUpdatedAt}` in the header.

### Recharts time-series via shadcn chart block (TS)
**Source:** `consumo-trend-chart.tsx` (full file).
**Apply to:** `economy-trend-chart.tsx`. `ChartContainer config={...satisfies ChartConfig}` >
`LineChart` > `CartesianGrid`/`XAxis`/`YAxis`/`ChartTooltip`/`ChartLegend`/`Line`s.
Economy uses ONE Y axis (all BRL) — drop the dual `yAxisId`.

---

## No Analog Found

| File | Role | Data Flow | Reason |
|------|------|-----------|--------|
| `dashboard/src/lib/economia.ts` (NEW, OPTIONAL) | utility | transform | Server computes everything (`/admin/economy`); a client helper module is likely unnecessary. If any pure derivation is needed, mirror `dashboard/src/lib/consumo.ts` aggregation shape — but do NOT reuse its per-tenant fan-out. |

Every other file has an exact in-repo analog.

## Metadata

**Analog search scope:** `gateway/internal/admin/`, `gateway/db/queries/`, `gateway/cmd/gateway/`,
`dashboard/src/lib/`, `dashboard/src/components/`, `dashboard/src/app/(dashboard)/`,
`dashboard/src/app/api/gateway/`.
**Files scanned (read in full or targeted):** 16.
**Pattern extraction date:** 2026-06-26
</content>
</invoke>
