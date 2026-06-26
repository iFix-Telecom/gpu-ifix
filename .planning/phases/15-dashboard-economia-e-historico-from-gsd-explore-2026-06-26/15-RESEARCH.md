# Phase 15: dashboard-economia-e-historico - Research

**Researched:** 2026-06-26
**Domain:** Go gateway aggregate SQL (sqlc/pgx) + Next.js 15 / Recharts dashboard panel + audit-log filtering
**Confidence:** HIGH (all patterns verified in-repo at file:line; no new external libraries)

## Summary

This phase is **pure reuse of two established in-repo patterns** — there is no new technology to learn and no external package to install. Everything needed already exists and was read during research.

OBS-09 (Economia panel) breaks down into three backend additions and two frontend additions. Backend: (1) a gateway-wide phantom SUM sqlc query with **no tenant filter** — this is the single documented blocker (`operations.go:18-21,257` left `phantom_month_brl` nil precisely because this query did not exist); (2) a per-day phantom bucket query for the time series; (3) Go-side Vast-cost bucketing reusing the exact accrual logic already in `operations.go:214-245`. Frontend: a new economy panel (mirror `operacao-cost-panel.tsx`) and a real time-series chart (mirror `consumo-trend-chart.tsx`, a dual-axis Recharts `LineChart` via the shadcn `chart` block).

OBS-10 (incident history filtering) is a straight extension of `audit.go` + `ListAuditStateChanges`: add `from`/`to`/`search` params (copy the `America/Sao_Paulo` date handling verbatim from `usage.go:132-150`) plus a `COUNT(*)` query, then extend `AuditResponse` with `total` and add a date-range picker + search box to `/incidents` (copy the `Popover`+`Calendar` block from `consumo/page.tsx:139-163`).

**Primary recommendation:** Add a NEW `GET /admin/economy?from=&to=` endpoint that mirrors `usage.go`'s `{range, summary, rows}` shape — do NOT bloat the 5-10s `/admin/operations` poll with a time-series array. Compute the three numbers + the daily series **server-side in Go** so the dashboard stays presentation-only. Bucket Vast cost by each lifecycle's `started_at` BRT date (the pod runs same-day 9-17h BRT windows that never cross midnight, so one lifecycle = one BRT day — this matches the existing `today` bucketing at `operations.go:242`).

## User Constraints (from /gsd:explore note + ROADMAP)

> No CONTEXT.md exists yet (this is the research step before discuss/plan). Constraints below are the LOCKED exploration decisions from `.planning/notes/dashboard-economia-definicao-e-gaps.md` and ROADMAP Phase 15. Treat these with the same authority as locked decisions.

### Locked Decisions
- **Economy formula:** `economia = SUM(cost_local_phantom_brl) − custo_real_Vast` per period.
- **Three numbers side-by-side:** (1) líquido R$ = phantom − Vast; (2) recorte janela pod-up = only hours pod was UP (phantom rows naturally only exist when served local, so this aligns automatically — no extra filtering needed); (3) ROI multiplier = phantom avoided per R$1 of GPU.
- **Plus a real time-series chart** (X axis = time/day buckets, not category).
- **Phantom price is TRUSTED — do NOT plan a price-validation task.** The daily ops-claude timer populates OpenRouter + forex prices; assume correct and build.
- **Vast cost source:** `primary_lifecycles.total_cost_brl` (closed lifecycles) + live accrual `accepted_dph × hours-since-started × USDToBRLRate` (open lifecycle). Logic already at `operations.go:214-245`.
- **Phantom source:** `ai_gateway.billing_events.cost_local_phantom_brl` — written only when served LOCAL/GPU.

### Claude's Discretion
- Endpoint design (new `/admin/economy` vs extending `/admin/operations`) — researched below, recommendation made.
- Day-bucketing strategy for Vast cost — researched below.
- Whether the economy panel lives on `/operacao`, `/consumo`, or a new `/economia` route — UI placement is open.

### Deferred Ideas (OUT OF SCOPE — do NOT plan)
- `audio_seconds` / `embeds_count` metering = 0 (metering never writes them). KPIs show zero; not this phase. (`HANDOFF-tier3-gpu-metrics.md:27`)
- Latency chart → time-series conversion (separate seed, SEED-020).
- Tier 3 GPU/RAM/CPU hardware panel (`HANDOFF-tier3-gpu-metrics.md`).
- Changing `/consumo` "custo total" from phantom to real external cost.

## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| OBS-09 | Painel Economia: gateway-wide phantom SUM por período cruzado com custo real Vast; 3 números (líquido R$, recorte pod-up, ROI) + série temporal real | §Standard Stack (new sqlc queries), §Architecture Patterns (new `/admin/economy` endpoint mirroring `usage.go`), §Code Examples (dual-axis Recharts mirror of `consumo-trend-chart.tsx`), §Common Pitfalls (tz, div-by-zero, day-bucketing) |
| OBS-10 | `/incidents` audit log: filtro de data + busca + total COUNT (hoje só pager limit/offset) | §Architecture Patterns (extend `ListAuditStateChanges` + new COUNT query, copy `usage.go` date handling), §Code Examples (Popover+Calendar from `consumo/page.tsx`) |

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Gateway-wide phantom SUM (no tenant filter) | API/Backend (Go + new sqlc query) | DB | The blocker query; must run server-side, no client fan-out (unlike `/consumo`) |
| Vast cost per-day bucketing | API/Backend (Go over `primary_lifecycles`) | — | Reuses accrual logic at `operations.go:214-245`; cost math stays in Go |
| Economy calc (phantom−Vast, ROI, pod-up recorte) | API/Backend (`/admin/economy`) | — | Compute server-side so dashboard stays presentation-only, mirroring `usage.go` |
| Economy time-series chart render | Frontend (Recharts via shadcn chart) | — | Mirror `consumo-trend-chart.tsx` |
| Date-range picker + search controls | Frontend (React state) | — | Mirror `consumo/page.tsx` Popover+Calendar |
| Audit date-range + search + COUNT | API/Backend (sqlc + handler) | Frontend (controls) | Extend `audit.go`; copy `usage.go` tz handling |
| Admin-key injection | Frontend Server (proxy route) | — | `api/gateway/[...path]/route.ts` already proxies any `/admin/*` — **NO CHANGE NEEDED** |

## Standard Stack

No new packages. Everything is already a direct dependency.

### Core (Gateway — Go)
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| sqlc | v1.30.0 | Generate type-safe Go from `db/queries/*.sql` | Already the codegen for all 14 query files [VERIFIED: `gateway/sqlc.yaml`, generated headers say `sqlc v1.30.0`] |
| pgx/v5 | (in go.mod) | Postgres driver; `pgtype.Numeric`/`pgtype.Timestamptz` | `sql_package: pgx/v5` in sqlc.yaml [VERIFIED: `sqlc.yaml:24`] |
| chi v5 | (in go.mod) | Admin sub-router mount | `adminRouter.Method(http.MethodGet, ...)` [VERIFIED: `cmd/gateway/main.go:1469-1487`] |
| google/uuid | (in go.mod) | tenant_id type override | [VERIFIED: `sqlc.yaml:30-31`] |

### Core (Dashboard — TypeScript)
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| next | ^15.2.0 | App Router; server proxy route | [VERIFIED: `dashboard/package.json:22`] |
| react | ^19.0.0 | UI | [VERIFIED: `package.json:27`] |
| recharts | 2.15.4 (pinned) | `LineChart` dual-axis time series | [VERIFIED: `package.json:30`; existing chart `consumo-trend-chart.tsx`] |
| @tanstack/react-query | ^5.65.0 | `useQuery` data fetching | [VERIFIED: `package.json:15`; existing `consumo/page.tsx`] |
| react-day-picker | ^9.13.2 | Date-range `Calendar` (shadcn) | [VERIFIED: `package.json:28`; used in `consumo/page.tsx`] |
| date-fns | ^4.1.0 | Date helpers (available; not strictly required — `isoDate` is hand-rolled in `consumo/page.tsx:65`) | [VERIFIED: `package.json:19`] |

### Supporting (shadcn/ui blocks already vendored)
| Component | Path | Use |
|-----------|------|-----|
| chart (ChartContainer/Tooltip/Legend) | `@/components/ui/chart` | Recharts wrapper [VERIFIED: imported in `consumo-trend-chart.tsx:24-31`] |
| Calendar / Popover | `@/components/ui/calendar`, `.../popover` | Date-range filter [VERIFIED: `consumo/page.tsx:27,35-38`] |
| Card / KpiCard | `@/components/ui/card`, `@/components/kpi-card` | 3-number layout [VERIFIED: `operacao-cost-panel.tsx`] |
| Table / ScrollArea / Badge | `@/components/ui/*` | Audit table [VERIFIED: `audit-table.tsx`] |
| Input | `@/components/ui/input` | Search box for OBS-10 (verify it exists; shadcn standard) |

**Installation:** None. `cd gateway && sqlc generate` is the only codegen step (CI enforces the tree is in sync — `build-gateway.yml:61-67`).

## Package Legitimacy Audit

> Not applicable — this phase installs **zero** new packages. All libraries are existing direct dependencies verified in `gateway/go.mod` and `dashboard/package.json`. No registry lookups or slopcheck required.

## Architecture Patterns

### System Architecture Diagram

```
                          Browser (read-only dashboard)
   /economia page                         /incidents page
   ┌──────────────────────┐              ┌─────────────────────────┐
   │ useQuery(fetchEconomy)│              │ useQuery(fetchAudit      │
   │   ↓                   │              │   limit,offset,from,to,q)│
   │ EconomyPanel (3 KPIs) │              │ date-range + search box  │
   │ EconomyTrendChart     │              │ AuditTable + total pager │
   │  (Recharts dual line) │              └───────────┬─────────────┘
   └──────────┬───────────┘                          │
              │  GET /api/gateway/economy?from=&to=   │ GET /api/gateway/audit?...
              ▼                                       ▼
   ┌─────────────────────────────────────────────────────────────┐
   │  Next.js server route  api/gateway/[...path]/route.ts        │
   │  injects X-Admin-Key  →  ${GATEWAY_BASE_URL}/admin/*         │  (NO CHANGE)
   └──────────┬──────────────────────────────────────┬───────────┘
              ▼                                        ▼
   ┌─────────────────────────┐         ┌──────────────────────────────┐
   │ NEW admin.EconomyHandler│         │ EXTENDED admin.AuditHandler  │
   │ GET /admin/economy      │         │ GET /admin/audit (+from/to/q)│
   │  ├ SumPhantomAllTenants  │         │  ├ ListAuditStateChanges(+rng)│
   │  │   Range/ByDate (NEW)  │         │  └ CountAuditStateChanges(NEW)│
   │  └ ListPrimaryLifecycles │         └──────────────┬───────────────┘
   │      InRange → Go bucket │                        │
   └──────────┬──────────────┘                        │
              ▼                                        ▼
   ┌─────────────────────────────────────────────────────────────┐
   │  Postgres ai_gateway schema                                  │
   │  billing_events (cost_local_phantom_brl)   ← phantom         │
   │  primary_lifecycles (total_cost_brl, accepted_dph,started_at)│
   │  audit_log (event_kind, ts, route, reason, ...)             │
   └─────────────────────────────────────────────────────────────┘
```

### Recommended file changes

```
gateway/
├── db/queries/billing.sql        # ADD SumPhantomAllTenantsByDate :many, SumPhantomAllTenantsRange :one
├── db/queries/primary_lifecycles.sql  # ADD ListPrimaryLifecyclesInRange :many (overlap [from,to))
├── db/queries/audit.sql          # MODIFY ListAuditStateChanges (+from/to/search); ADD CountAuditStateChanges :one
├── internal/db/gen/*.sql.go      # sqlc generate output (commit — CI enforces)
├── internal/admin/economy.go     # NEW EconomyHandler (clone usage.go shape exactly)
├── internal/admin/economy_test.go # NEW (clone operations_test.go fake-queries pattern)
├── internal/admin/audit.go       # MODIFY: parse from/to/search, add total to AuditResponse
├── internal/admin/audit_test.go  # MODIFY
└── cmd/gateway/main.go           # WIRE EconomyHandler: field on px struct (~1228-1263) + route (~1484)
dashboard/src/
├── lib/gateway.ts                # ADD EconomyResponse interface + fetchEconomy(); MODIFY AuditResponse (+total) + fetchAudit(from,to,q)
├── lib/economia.ts               # NEW pure helpers if any client derivation needed (likely minimal — server computes)
├── components/economy-panel.tsx  # NEW (mirror operacao-cost-panel.tsx: 3 KpiCards)
├── components/economy-trend-chart.tsx # NEW (mirror consumo-trend-chart.tsx)
├── components/audit-table.tsx    # MODIFY pager to use total
├── app/(dashboard)/economia/page.tsx  # NEW (or add panel to operacao/consumo)
└── app/(dashboard)/incidents/page.tsx # MODIFY: date-range + search controls
```

### Pattern 1: Adding a new sqlc query (the canonical gateway flow)
**What:** Write annotated SQL → regenerate → commit.
**When to use:** Any new DB read. This IS the codegen contract (CI fails if `internal/db/gen/` drifts).
**Example:**
```sql
-- Source: gateway/db/queries/billing.sql (existing pattern at SumBillingEventsRange)
-- name: SumPhantomAllTenantsByDate :many
-- Gateway-WIDE phantom sum (NO tenant filter) bucketed by BRT day — the
-- OBS-09 blocker query. cost_local_phantom_brl is only written when served
-- local/GPU, so this is naturally the "value delivered by the GPU" series.
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
Then: `cd gateway && sqlc generate && git add internal/db/gen/`. CI verifies via `git diff --exit-code internal/db/gen/` [VERIFIED: `.github/workflows/build-gateway.yml:61-67`]. The `idx_billing_events_tenant_ts (tenant_id, ts DESC)` index does NOT serve a no-tenant range scan efficiently — consider whether a `(ts)` index or accepting a seq-scan over the monthly partition is fine (volume is low; see Pitfall 6).

### Pattern 2: Admin handler shape (clone usage.go / operations.go EXACTLY)
**What:** Every admin handler follows the identical 5-part contract.
**When to use:** The new `EconomyHandler`.
**The contract** (verified across `usage.go`, `audit.go`, `operations.go`, `metrics.go`):
1. **Query-interface isolation** — a private interface (e.g. `economyQueries`) listing only the sqlc methods used, so tests inject a fake without a real `pgxpool` [VERIFIED: `usage.go:85-90`, `operations.go:113-115`].
2. **Dual constructor** — `NewEconomyHandler(q *gen.Queries, ...)` for prod + `newEconomyHandlerWithQueries(q economyQueries, ...)` for tests [VERIFIED: `usage.go:99-113`].
3. **OpenAI error envelope** on every failure via `httpx.WriteOpenAIError(w, status, type, code, msg)` [VERIFIED: `usage.go:125-130`].
4. **Admin-metric increment per branch** — `obs.GatewayAdminRequests.WithLabelValues("/admin/economy", "2xx"|"4xx"|"5xx").Inc()` [VERIFIED: every handler].
5. **`America/Sao_Paulo` tz** — `time.LoadLocation`, `time.ParseInLocation("2006-01-02", from, loc)`, `to = to.Add(24h)` for exclusive end [VERIFIED: `usage.go:132-150`].

### Pattern 3: Wiring a handler into the admin router
**What:** Three edits in `cmd/gateway/main.go`.
**Example:**
```go
// 1. struct field (~line 95-110)
adminEconomyHandler http.Handler
// 2. construct (~line 1228-1244)
adminEconomyHandler := admin.NewEconomyHandler(gen.New(pool), cfg, log)
// 3. assign to px (~line 1260-1263) then mount (~line 1484)
if px.adminEconomyHandler != nil {
    adminRouter.Method(http.MethodGet, "/economy", px.adminEconomyHandler)
}
```
[VERIFIED: `cmd/gateway/main.go:95-110, 1228-1263, 1469-1487`]. The `X-Admin-Key` middleware already gates the whole sub-router — no auth code needed.

### Pattern 4: Typed dashboard client wrapper (lib/gateway.ts)
**What:** Add an interface mirroring the Go response field-for-field + a `fetchX` that calls `proxyGet`.
**Example:**
```ts
// Source: dashboard/src/lib/gateway.ts (existing fetchUsage / fetchOperations)
export interface EconomyResponse {
  range: { from: string; to: string; timezone: string };
  summary: {
    phantom_brl: number;
    vast_brl: number;
    economia_liquida_brl: number;   // phantom − vast
    roi_multiplier: number | null;  // null when vast_brl == 0 (Pitfall 3)
  };
  series: Array<{ date: string; phantom_brl: number; vast_brl: number; economia_brl: number }>;
}
export function fetchEconomy(from: string, to: string): Promise<EconomyResponse> {
  return proxyGet<EconomyResponse>("economy", { from, to });
}
```
The proxy route `api/gateway/[...path]/route.ts` forwards ANY `/admin/*` path generically — **no proxy change needed** [VERIFIED: `route.ts:42-44`].

### Anti-Patterns to Avoid
- **Do NOT fan out per-tenant on the client for economy** (the `/consumo` `Promise.allSettled` pattern). The whole point of OBS-09 is a server-side gateway-wide sum. Client fan-out reintroduces partial-failure gaps and is the thing the blocker note calls out (`note:39`).
- **Do NOT add the time-series array to `/admin/operations`.** That endpoint is polled every 5-10s for a live snapshot; a date-ranged series belongs on its own endpoint (mirror `/admin/usage`).
- **Do NOT sum phantom into a "total real cost".** Phantom is reporting-only; `usage.go:206-209` explicitly excludes it from `cost_total`. Economy is phantom (counterfactual) MINUS real Vast — keep them separate.
- **Do NOT use `CURRENT_DATE` + tz.** The repo idiom is `(now() AT TIME ZONE 'America/Sao_Paulo')::date`; the alternative is flagged invalid in `billing.sql:7-9`.
- **Do NOT round-trip dates through `toISOString()`** in the frontend — it shifts the calendar day by the UTC offset. Use the local-component `isoDate()` helper [VERIFIED: `consumo/page.tsx:56-67`, WR-08].

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Date-range picker | Custom calendar | `Popover` + `Calendar mode="range"` | Already vendored + used in `consumo/page.tsx:140-158` |
| Dual-axis chart | Manual SVG/scale | Recharts `LineChart` + `yAxisId` via shadcn `ChartContainer` | Exact mirror exists: `consumo-trend-chart.tsx` |
| Vast cost accrual | New cost math | Copy `operations.go:214-245` (closed→total_cost_brl, open→accepted_dph×hours×USDToBRLRate) | Already correct + tested; reuse to stay consistent |
| pgtype.Numeric→float | Manual decode | `numericPtr()` / `.Float64Value()` | Helpers exist in `operations.go:363-373` and `usage.go:183-185` |
| BRL formatting | Custom | `formatBrl()` / `formatCount()` | `dashboard/src/lib/format.ts:24-34` |
| Tenant fan-out for "all tenants" | Loop in Go over tenants | A single no-`WHERE tenant_id` SUM | One query, server-side, complete — the blocker fix |
| Admin auth | New middleware | Existing `admin.Middleware` (X-Admin-Key bcrypt) gates the whole sub-router | `main.go:1469` |

**Key insight:** Phase 15 has ~zero novel logic. The risk is not "can we build it" but "do we clone the established shapes exactly" — divergence from the `usage.go`/`consumo` patterns is the failure mode.

## Runtime State Inventory

> This is a code/config + DB-read phase. No renames, no stored-key migration. Categories below answered explicitly.

| Category | Items Found | Action Required |
|----------|-------------|------------------|
| Stored data | None — reads existing `billing_events`, `primary_lifecycles`, `audit_log`. No schema migration to columns (only new read queries). | None |
| Live service config | None — no n8n/Datadog/external config touched. Dashboard env (`GATEWAY_BASE_URL`, `GATEWAY_ADMIN_KEY`) already set; new endpoint rides the existing proxy. | None |
| OS-registered state | None — no Task Scheduler / systemd units changed. | None |
| Secrets/env vars | `GATEWAY_ADMIN_KEY` already wired in dashboard proxy + Portainer stack; `USD_TO_BRL_RATE` (default 5.0) + `MONTHLY_PRIMARY_BUDGET_BRL` (default 800.0) already in gateway config. No new secrets. | None |
| Build artifacts | sqlc regenerates `internal/db/gen/*.sql.go` — **must be committed** (CI `git diff --exit-code` gate). | Run `cd gateway && sqlc generate` + commit |

**Migrations:** OBS-09 needs **no new migration** (columns already exist). OBS-10 needs **no new column** (filters existing `ts`/`route`/`reason`). Only new *queries* (no DDL) — but verify audit_log/billing_events monthly partitions cover the queried date range (Pitfall 6).

## Common Pitfalls

### Pitfall 1: Timezone mismatch between phantom buckets and Vast buckets
**What goes wrong:** `billing_events.ts` is UTC `timestamptz`; if you bucket phantom by UTC day but Vast by BRT day (or vice-versa), the daily economy numbers misalign at the day boundary and líquido R$ looks wrong on edge days.
**Why:** Phantom SQL must use `(ts AT TIME ZONE 'America/Sao_Paulo')::date`; Vast Go-side must use `row.StartedAt.In(loc)` where `loc = America/Sao_Paulo`.
**How to avoid:** Use the SAME BRT boundary on both sides. The SQL idiom is already established (`billing.sql:98,111`); the Go side already does `.In(loc)` (`operations.go:242`). Reuse both verbatim.
**Warning signs:** Economy spikes/dips that flip sign exactly at midnight; a day with phantom but zero Vast (or reverse) when the pod was clearly up.

### Pitfall 2: Vast cost day-bucketing for lifecycles
**What goes wrong:** A lifecycle has `started_at`/`ended_at`; naively attributing its whole `total_cost_brl` to multiple days double-counts, or spreading it wrong distorts the series.
**Why:** The schedule (`PRIMARY_POD_SCHEDULE`) runs the pod 9-17h BRT same-day (`MEMORY: prod-primary-schedule-and-email` — seg-sex 9-17h BRT), so a lifecycle does NOT cross midnight in normal operation → **one lifecycle = one BRT day**. Attribute the full cost to `started_at.In(loc)` date (matches the existing `today` bucket at `operations.go:242`).
**How to avoid:** Bucket each lifecycle's cost into `started_at.In(loc)` date. Document the assumption. If a future lifecycle ever spans midnight (manual force-up overnight), the day attribution is slightly off but the *period total* stays exact — acceptable for a savings panel.
**Warning signs:** A multi-day lifecycle (rare) showing all cost on one day — acceptable per above, but note it.

### Pitfall 3: Division by zero on ROI when Vast cost = 0
**What goes wrong:** `roi = phantom / vast` → `Infinity`/`NaN` when no pod ran in the period (vast_brl = 0). Renders as `R$ ∞` or `NaN`.
**Why:** Period with zero pod-up hours has phantom = 0 too (phantom only written when served local), but a partial period could have residual phantom with vast still rounding to 0.
**How to avoid:** Compute ROI in Go as `*float64` (pointer) → `nil` when `vast_brl == 0`, serialized as JSON `null`; frontend renders `—`. Mirror the nullable-pointer idiom already used for `cost_brl` (`operations.go:92-93, 363-373`, Pitfall D "distinguish not-computed from zero").
**Warning signs:** `Infinity`/`NaN` in the JSON; ROI card showing garbage.

### Pitfall 4: Open-lifecycle accrual over-counts the cold-start window
**What goes wrong:** Live accrual uses `now − started_at`, but billing-of-record uses `first_health_pass_at`; `started_at` is earlier, so the open lifecycle's accrued cost slightly over-states until it closes.
**Why:** Documented at `operations.go:222-224` (Pitfall B). The closed `total_cost_brl` is authoritative; only the currently-open lifecycle is approximate.
**How to avoid:** Reuse the exact existing accrual formula (don't invent a new one). The over-count is bounded to the cold-start window of one open pod and self-corrects at close. Acceptable; note it in the panel ("custo do pod atual é estimativa ao vivo").
**Warning signs:** Today's Vast cost ticking slightly high vs the eventual closed total.

### Pitfall 5: Partial-tenant data is NOT a concern for economy (but IS the reason to avoid client fan-out)
**What goes wrong:** `/consumo` tolerates per-tenant fetch failures and shows "N de M tenants não retornaram" — if you copy that pattern for economy, the savings number silently undercounts when a tenant call fails.
**Why:** Economy is a single gateway-wide sum; there are no per-tenant calls to partially fail.
**How to avoid:** Do the sum server-side with no tenant filter (the blocker fix). The result is always complete. Do NOT replicate the fan-out.
**Warning signs:** An economy page importing `Promise.allSettled` — wrong by construction.

### Pitfall 6: Partition coverage for date-range queries
**What goes wrong:** `billing_events` and `audit_log` are RANGE-partitioned by `ts`; querying a date outside seeded partitions returns empty (rows route only to existing partitions). A user picking an old date range sees zero, not an error.
**Why:** Migrations seeded only current month + next 2 (`0010`, `0003`); the partition-roll automation is a tracked open todo (`0020` comment, STATE.md).
**How to avoid:** Default the economy date range to current month (like `consumo/page.tsx:69-76`). For OBS-10 audit search, the same applies. Verify on the live DB which partitions exist before assuming deep history is queryable. This is a data-availability caveat, not a code bug.
**Warning signs:** Empty series for past months even though the pod ran then.

### Pitfall 7: No-tenant-filter phantom query and the index
**What goes wrong:** `SumPhantomAllTenantsByDate` has no `WHERE tenant_id` → the `idx_billing_events_tenant_ts (tenant_id, ts DESC)` index can't serve it; Postgres seq-scans the partition.
**Why:** That index is tenant-leading.
**How to avoid:** Volume is low (a handful of tenants, monthly partition) so a seq-scan over one month's partition is fine. If `EXPLAIN` shows a problem later, add a `(ts)` index. Do NOT prematurely add an index — note it as a watch-item. (Mirrors the `idx_audit_log_ts` rationale at `audit.sql` comments — a ts-leading index was added there precisely because a tenant-leading index couldn't serve a no-tenant-equality predicate.)

### Pitfall 8: sqlc param ordering for the audit search
**What goes wrong:** Adding `from`/`to`/`search` to `ListAuditStateChanges` shifts positional params; `$1/$2` (limit/offset) become `$4/$5`. The handler and struct field names (`Limit`, `Offset`, sqlc auto-names like `Ts`, `Ts_2`) change — easy to mis-map.
**How to avoid:** Regenerate and read the new `ListAuditStateChangesParams` struct from `internal/db/gen/audit.sql.go` before editing the handler. For optional search, pass `'%' || term || '%'` (or `'%'` when empty) and use `(route ILIKE $3 OR reason ILIKE $3 OR error_code ILIKE $3 OR event_kind ILIKE $3)` — a single `$3` reused.

## Code Examples

### Economy time-series chart (mirror consumo-trend-chart.tsx)
All three values are BRL → they can share ONE Y axis (unlike consumo's tokens-vs-cost which needed dual axes). Use two lines (phantom, vast) + optionally a reference/area for economia, OR three lines.
```tsx
// Source: derived from dashboard/src/components/consumo-trend-chart.tsx (verified pattern)
"use client";
import { CartesianGrid, Line, LineChart, XAxis, YAxis } from "recharts";
import { ChartContainer, ChartLegend, ChartLegendContent,
  ChartTooltip, ChartTooltipContent, type ChartConfig } from "@/components/ui/chart";

const chartConfig = {
  phantom_brl:   { label: "Economia bruta (OpenRouter)", color: "var(--primary)" },
  vast_brl:      { label: "Custo Vast (GPU)",            color: "var(--status-warning)" },
  economia_brl:  { label: "Líquido R$",                  color: "var(--status-success)" },
} satisfies ChartConfig;

export function EconomyTrendChart({ rows }: { rows: Array<{ date:string; phantom_brl:number; vast_brl:number; economia_brl:number }> }) {
  return (
    <ChartContainer config={chartConfig} className="aspect-auto h-[260px] w-full">
      <LineChart data={rows} margin={{ top: 8, right: 16, bottom: 8, left: 8 }}>
        <CartesianGrid vertical={false} />
        <XAxis dataKey="date" tickLine={false} axisLine={false} tickMargin={8}
               className="text-[12px] font-semibold" />
        <YAxis tickLine={false} axisLine={false} tickMargin={8} width={56}
               className="text-[12px] font-semibold tabular-nums" />
        <ChartTooltip content={<ChartTooltipContent />} />
        <ChartLegend content={<ChartLegendContent />} />
        <Line dataKey="phantom_brl"  type="monotone" stroke="var(--color-phantom_brl)"  strokeWidth={2} dot={false} />
        <Line dataKey="vast_brl"     type="monotone" stroke="var(--color-vast_brl)"     strokeWidth={2} dot={false} />
        <Line dataKey="economia_brl" type="monotone" stroke="var(--color-economia_brl)" strokeWidth={2} dot={false} />
      </LineChart>
    </ChartContainer>
  );
}
```

### Audit date-range + search params (extend ListAuditStateChanges)
```sql
-- Source: extend gateway/db/queries/audit.sql (existing ListAuditStateChanges)
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
Handler builds `searchPattern := "%"; if q != "" { searchPattern = "%"+q+"%" }`. Add `Total int64 \`json:"total"\`` to `AuditResponse`; frontend uses `total` for a real pager (`canNext = offset+limit < total`) replacing the inferred `canNext` in `audit-table.tsx`.

### Date-range picker block (copy from consumo/page.tsx)
```tsx
// Source: dashboard/src/app/(dashboard)/consumo/page.tsx:140-163 (verbatim pattern)
<Popover>
  <PopoverTrigger asChild>
    <Button size="sm" variant="outline">
      <CalendarIcon />
      {range?.from && range?.to ? `${isoDate(range.from)} → ${isoDate(range.to)}` : "Selecione o período"}
    </Button>
  </PopoverTrigger>
  <PopoverContent className="w-auto p-0" align="start">
    <Calendar mode="range" selected={range} onSelect={setRange} numberOfMonths={2} />
  </PopoverContent>
</Popover>
<Button size="sm" onClick={applyPeriod} disabled={!canApply}>Aplicar período</Button>
```
`isoDate()` (local components, NOT toISOString) and `currentMonthRange()` are at `consumo/page.tsx:65-76` — copy them.

## State of the Art

| Old Approach | Current Approach | Why |
|--------------|------------------|-----|
| `/admin/operations` `phantom_month_brl` left nil | New `/admin/economy` endpoint with gateway-wide phantom SUM | The blocker query (no-tenant-filter SUM) now gets built; operations stays a lightweight snapshot |
| `/consumo` fans out per-tenant + `Promise.allSettled` for "all" | Server-side single SUM (no tenant filter) | Complete numbers, no partial-failure gaps |
| `/incidents` pager infers `canNext` from full-page heuristic | Real `total` COUNT + `offset+limit<total` | Honest pagination; date-range + search |
| Latency chart X axis = route (categorical) | (Out of scope here — SEED-020) economy chart X axis = time | OBS-09 delivers the first true time-series chart; latency conversion is deferred |

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | Pod runs same-day 9-17h BRT windows → one lifecycle = one BRT day; bucket Vast cost by `started_at` BRT date | Pitfall 2 | If a lifecycle spans midnight, daily series is slightly off on that boundary (period total stays exact). Sourced from MEMORY `prod-primary-schedule-and-email` + `operations.go:242` existing behavior. |
| A2 | `USD_TO_BRL_RATE` config default 5.0 is the rate to use for live accrual (same as operations.go today) | §User Constraints, Code Examples | If economy must use the daily-timer forex rate from `fx_rates` table instead of the config constant, the open-lifecycle accrual differs slightly. operations.go uses `cfg.USDToBRLRate` [VERIFIED], but the exploration mentions a "daily timer OpenRouter+forex" — confirm whether economy should read `GetCurrentFX('USD/BRL')` instead. **Surface in discuss-phase.** |
| A3 | No new migration needed; monthly partitions cover the default (current-month) range | §Runtime State Inventory, Pitfall 6 | If deep historical ranges are required, partition-roll automation (open todo) must run first. |
| A4 | `Input` shadcn component exists for the search box | §Standard Stack | Trivial — `npx shadcn add input` if missing; verify before planning. |
| A5 | Phantom price is correct (per exploration decision) — no validation task | §User Constraints | Locked by exploration; explicitly out of scope. |

## Open Questions

1. **Forex rate source for live accrual: config constant vs `fx_rates` table?**
   - What we know: `operations.go` uses `cfg.USDToBRLRate` (default 5.0). A daily timer populates `fx_rates` (`GetCurrentFX('USD/BRL')` query exists).
   - What's unclear: Should the economy endpoint use the live `fx_rates` rate for accuracy, or the config constant for consistency with the existing operations panel?
   - Recommendation: Use `cfg.USDToBRLRate` to stay byte-identical with the existing Vast-cost computation (closed `total_cost_brl` already baked in BRL at close anyway; only the open-lifecycle accrual is affected). Flag for discuss-phase.

2. **UI placement: new `/economia` route, or panel on `/operacao` / `/consumo`?**
   - What we know: economy is conceptually close to both Vast cost (`/operacao`) and phantom cost (`/consumo`).
   - Recommendation: New `/economia` route — it has its own date-range + time-series (different cadence than the 5-10s operations poll). Cleaner. Decide in discuss/plan.

3. **"Recorte janela pod-up" number — derived or separate?**
   - What we know: phantom is only written when served local (pod up), so the gateway-wide phantom SUM IS already the pod-up recorte. The "líquido R$" and "recorte pod-up" may be the same number unless a separate hours-based filter is wanted.
   - Recommendation: Treat líquido R$ (full period) and recorte pod-up as the same when no external routing happened; if they must differ, the distinction is phantom-period vs hours-pod-was-up — clarify the exact intent with the user in discuss-phase.

## Environment Availability

> Code/DB-read phase. The only "tools" are build/codegen, all already in CI.

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| sqlc | regen gen layer | ✓ (CI installs) | v1.30.0 | `go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0` |
| Go toolchain | gateway build/test | ✓ | 1.24 | — |
| Bun/Node | dashboard build/test (vitest) | ✓ | Node 20+ / Next 15 | — |
| Postgres `ai_gateway` (DO managed) | live data (not for unit tests) | ✓ (prod/dev) | 16 | Unit tests use fake queries (no DB) |

**Missing dependencies:** None blocking. Unit tests run with in-memory fakes (`fakeOperationsQueries` pattern) — no DB needed for the test suite.

## Validation Architecture

> nyquist_validation = true in config.json → section included.

### Test Framework
| Property | Value |
|----------|-------|
| Framework (gateway) | Go `testing` stdlib + miniredis for breaker fakes |
| Framework (dashboard) | Vitest ^3.0.0 + @testing-library/jest-dom |
| Config file | `gateway/` (go test, no config); `dashboard/` vitest (package.json `"test": "vitest run"`) |
| Quick run command | `cd gateway && go test ./internal/admin/...` ; `cd dashboard && bun run test src/lib/gateway.test.ts` |
| Full suite command | `cd gateway && go test ./...` ; `cd dashboard && bun run test` |

### Phase Requirements → Test Map
| Req | Behavior | Test Type | Automated Command | File Exists? |
|-----|----------|-----------|-------------------|--------------|
| OBS-09 | EconomyHandler sums phantom gateway-wide, computes líquido/ROI, buckets series | unit (Go, fake queries) | `cd gateway && go test ./internal/admin/ -run Economy` | ❌ Wave 0 (`economy_test.go`) |
| OBS-09 | ROI nil when vast=0 (no Inf/NaN) | unit (Go) | same | ❌ Wave 0 |
| OBS-09 | sqlc query compiles + regen in sync | codegen | `cd gateway && sqlc generate && git diff --exit-code internal/db/gen/` | ✓ (CI gate) |
| OBS-09 | fetchEconomy typed wrapper + chart renders | unit (vitest) | `cd dashboard && bun run test src/lib/gateway.test.ts` | ⚠️ extend existing `gateway.test.ts` |
| OBS-10 | Audit handler parses from/to/search, returns total | unit (Go, fake queries) | `cd gateway && go test ./internal/admin/ -run Audit` | ✓ `audit_test.go` (extend) |
| OBS-10 | COUNT query + pager total | unit | same | ⚠️ extend |
| OBS-10 | Date-range picker + search controls render/apply | unit (vitest) | `cd dashboard && bun run test` | ⚠️ new component test |

### Sampling Rate
- **Per task commit:** the package-scoped command above (`go test ./internal/admin/...` or the vitest file).
- **Per wave merge:** `cd gateway && go test ./... && sqlc generate && git diff --exit-code internal/db/gen/` + `cd dashboard && bun run test`.
- **Phase gate:** full suite green + `go vet ./gateway/...` before `/gsd:verify-work`.

### Wave 0 Gaps
- [ ] `gateway/internal/admin/economy_test.go` — covers OBS-09 (clone `operations_test.go` `fakeOperationsQueries` + `opNumeric`/`opTimestamptz` helpers).
- [ ] Extend `gateway/internal/admin/audit_test.go` — from/to/search/total (OBS-10).
- [ ] Extend `dashboard/src/lib/gateway.test.ts` — `fetchEconomy` + new `AuditResponse.total`.
- [ ] New `dashboard/src/components/economy-trend-chart` + `economy-panel` tests (optional; mirror `fsm-panel.test.tsx`).
- [ ] No framework install needed — both suites already exist.

## Security Domain

> security_enforcement absent in config → treated as enabled. Scope is narrow: a read-only admin-gated dashboard endpoint + an audit search. Most ASVS categories are already satisfied by existing infra.

### Applicable ASVS Categories
| ASVS Category | Applies | Standard Control |
|---------------|---------|------------------|
| V2 Authentication | yes (inherited) | `X-Admin-Key` bcrypt middleware gates the whole `/admin` sub-router [`main.go:1469`]; admin key read ONLY in the Next proxy route (T-07-24) — no client exposure |
| V4 Access Control | yes | All new reads ride the existing admin-gated router; dashboard is GET-only (proxy rejects non-GET) |
| V5 Input Validation | yes | from/to via `time.ParseInLocation` (reject bad dates → 400); limit capped (`maxAuditLimit=200`); search passed as a **parameterized** ILIKE arg (never string-concatenated into SQL) |
| V6 Cryptography | no (none introduced) | — |
| V7 Error Handling/Logging | yes | OpenAI error envelope on all failures; audit content table (prompts/responses) NEVER selected by these queries (T-07-09) — only metadata columns |

### Known Threat Patterns
| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| SQL injection via audit search box | Tampering | sqlc parameterized query ($3 ILIKE), never string interpolation — the whole point of sqlc |
| Admin-key disclosure to browser | Info Disclosure | Key stays in `route.ts` server handler only (T-07-24); new endpoint adds no key handling |
| Unbounded result set (search DoS) | DoS | `maxAuditLimit=200` cap retained; COUNT query bounded by date range |
| Sensitive prompt/response leak via audit | Info Disclosure | Queries select only audit_log metadata, never `audit_log_content` (T-07-09 invariant — preserve it) |

## Sources

### Primary (HIGH confidence — read in this session)
- `gateway/internal/admin/operations.go` (Vast accrual `:214-245`, nullable-ptr helpers, deferred-phantom note `:18-21,257`)
- `gateway/internal/admin/usage.go` (handler contract, tz handling `:132-150`, phantom-not-in-total `:206-209`)
- `gateway/internal/admin/audit.go` + `db/queries/audit.sql` (pager, no-COUNT, event_kind filter)
- `gateway/db/queries/billing.sql` + `internal/db/gen/billing.sql.go` (SUM query shape, tz idiom)
- `gateway/db/queries/primary_lifecycles.sql` + migration `0023` (lifecycle schema, cost columns)
- `gateway/sqlc.yaml`, `.github/workflows/build-gateway.yml:36-67` (sqlc v1.30.0, codegen gate)
- `gateway/cmd/gateway/main.go:95-110,1228-1263,1469-1487` (handler wiring + router mount)
- `gateway/internal/config/config.go:162,169,244` (USDToBRLRate, MonthlyPrimaryBudgetBRL defaults)
- `gateway/internal/admin/operations_test.go` (fake-queries test pattern)
- `dashboard/src/lib/gateway.ts` (typed wrappers, proxyGet, GatewayError)
- `dashboard/src/components/consumo-trend-chart.tsx` (dual-axis Recharts mirror)
- `dashboard/src/lib/consumo.ts` + `app/(dashboard)/consumo/page.tsx` (fan-out, date-range, isoDate WR-08)
- `dashboard/src/app/(dashboard)/incidents/page.tsx` + `components/audit-table.tsx` (pager to extend)
- `dashboard/src/app/api/gateway/[...path]/route.ts` (generic admin proxy — no change needed)
- `dashboard/src/lib/format.ts` (formatBrl/formatCount)
- `.planning/notes/dashboard-economia-definicao-e-gaps.md`, ROADMAP Phase 15, REQUIREMENTS OBS-09/10
- `.planning/quick/260625-v04-.../260625-v04-RESEARCH.md:178,206-207` (original blocker analysis + option (a) recommendation)

### Secondary
- MEMORY: `prod-primary-schedule-and-email` (pod 9-17h BRT schedule — basis for A1 day-bucketing)

### Tertiary (LOW — flag for validation)
- None. All claims sourced to in-repo files.

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — zero new packages; all verified in go.mod/package.json + used in existing code.
- Architecture (endpoint design, sqlc flow, handler contract): HIGH — clones 4 existing handlers verified at file:line; CI codegen gate confirmed.
- Day-bucketing for Vast cost: MEDIUM — relies on assumption A1 (same-day pod windows); period total is exact regardless, only intra-period daily attribution depends on it.
- Forex rate source: MEDIUM — A2 open question (config constant vs fx_rates table) for discuss-phase.
- Pitfalls: HIGH — each traced to existing code comments (Pitfall B/D, tz idiom, index rationale).

**Research date:** 2026-06-26
**Valid until:** 2026-07-26 (stable — internal codebase, pinned tool versions)
