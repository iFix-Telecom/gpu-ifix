---
phase: 15-dashboard-economia-e-historico
status: secured
asvs_level: 1
block_on: high
register_authored_at_plan_time: true
threats_total: 16
threats_closed: 16
threats_open: 0
verified: 2026-06-27
---

# SECURITY.md — Phase 15 (dashboard-economia-e-historico)

**ASVS Level:** 1
**Block on:** high
**Register authored at plan time:** yes (State B — mitigations VERIFIED against implementation)
**Verified:** 2026-06-27
**Result:** SECURED — 16/16 threats CLOSED

The 16 STRIDE threats come from the four 15-0x-PLAN `<threat_model>` blocks
(OBS-09 backend, OBS-10 backend, OBS-09 frontend, OBS-10 frontend). Each
`mitigate` threat was verified by locating the actual mitigation in code; each
`accept` threat is logged below.

---

## Threat Verification (mitigate)

| Threat ID | Category | Evidence (file:line) |
|-----------|----------|----------------------|
| T-15-01 | Tampering | `gateway/internal/admin/economy.go:130,144` — `time.ParseInLocation("2006-01-02", …, loc)`; bad date → 400 `invalid_date` at `:132-137` / `:146-151`. Values bound as sqlc params `SumBillingAllTenantsRangeParams{Ts,Ts_2}` (`:157-160`), `SumPhantomAllTenantsByDateParams` (`:170-173`), `ListPrimaryLifecyclesInRangeParams` (`:183-186`) — never string-concatenated. Gen layer binds via `q.db.Query(ctx, sql, arg.Ts, arg.Ts_2)` (`internal/db/gen/billing.sql.go:282`). |
| T-15-02 | Info Disclosure | `gateway/cmd/gateway/main.go:1498-1500` — `/economy` mounted on `adminRouter` which applies `admin.Middleware(px.adminVerifier, log)` (`:1479`, X-Admin-Key bcrypt). No new key handling in `economy.go` (no env/key reads). |
| T-15-05 | Tampering | `gateway/internal/admin/audit.go:189-192` — `searchPattern` built as `"%"+q+"%"` and passed as single `Column3` arg (`:197`,`:212`). SQL `db/queries/audit.sql:31,44` binds it once as `$3` across route/reason/error_code/event_kind ILIKE; gen layer `internal/db/gen/audit.sql.go:35,120` passes `arg.Column3` to `QueryRow`/`Query` — no `fmt.Sprintf`/concat in generated code. |
| T-15-06 | Tampering | `gateway/internal/admin/audit.go:163,174` — `time.ParseInLocation`; bad `from`/`to` → 400 `invalid_date` (`:164-170` / `:175-181`) BEFORE either query runs. Bound as `Ts`/`Ts_2` sqlc params (`:195-196`,`:210-211`). |
| T-15-07 | Info Disclosure | `gateway/db/queries/audit.sql:26-27` (ListAuditStateChanges) and `:40` (CountAuditStateChanges) select ONLY metadata columns; `audit_log_content` is referenced in the file solely by the pre-existing `InsertAuditLogContent` writer (`:1-7`). Read queries select 0 content columns. Verified gen `internal/db/gen/audit.sql.go` matches. |
| T-15-08 | DoS | `gateway/internal/admin/audit.go:52` `maxAuditLimit=200`; clamp at `:129-131`. `CountAuditStateChanges` bounded by date range (`db/queries/audit.sql:42-44`, same `$1/$2` window). |
| T-15-09 | Info Disclosure | `gateway/cmd/gateway/main.go:1490-1491` — `/audit` mounted on the same X-Admin-Key `adminRouter` (`:1479`). No new key handling in `audit.go`. |
| T-15-10 | Info Disclosure | `dashboard/src/lib/gateway.ts:347-371` `proxyGet` hits only `/api/gateway/*`; `fetchEconomy` (`:426-428`) routes through it. `GATEWAY_ADMIN_KEY` read ONLY in `dashboard/src/app/api/gateway/[...path]/route.ts:26` (grep of `src/` finds it nowhere else except that file's doc comments). |
| T-15-11 | Tampering | `dashboard/src/app/api/gateway/[...path]/route.ts` exports `GET` only (`:77`); fetch uses `method:"GET"` (`:49`). No POST/PUT/DELETE/PATCH exported (verified). `/economia` uses `fetchEconomy`→`proxyGet` (GET). |
| T-15-12 | Info Disclosure | `dashboard/src/lib/gateway.ts:426-428` — single `proxyGet<EconomyResponse>("economy", {from,to})`; backed by gateway-wide `SumBillingAllTenantsRange` / `SumPhantomAllTenantsByDate` (`db/queries/billing.sql:95-107,79-93`, no `WHERE tenant_id`). No `Promise.allSettled`/client fan-out (SUMMARY 15-03 guard `! grep allSettled economia/page.tsx`). |
| T-15-13 | Tampering | `dashboard/src/lib/gateway.ts:391-405` — `search` forwarded as query param only when truthy; lands on the parameterized ILIKE (T-15-05). `dashboard/src/components/audit-table.tsx:108-128` renders all values via JSX `{…}` text nodes (React auto-escapes); no `dangerouslySetInnerHTML` in audit/incidents path. |
| T-15-14 | Info Disclosure | `dashboard/src/lib/gateway.ts:398` `fetchAudit`→`proxyGet`; admin key stays in `route.ts` (T-15-10 evidence). |
| T-15-15 | Info Disclosure | `dashboard/src/components/audit-table.tsx:90-127` — columns are Horário/Evento/Tenant/Rota/Motivo only (ts, event_kind, tenant_id, route, reason/error_code). No prompt/response/content fields exist on `AuditRow` (`gateway.ts:124-137`) because the backend never selects `audit_log_content` (T-15-07). |
| T-15-16 | Tampering | `dashboard/src/app/api/gateway/[...path]/route.ts` GET-only (T-15-11 evidence); `/incidents` reads via `fetchAudit`→`proxyGet` (GET). |

---

## Accepted Risks Log (accept)

| Threat ID | Category | Component | Rationale (from PLAN 15-01) | Verified |
|-----------|----------|-----------|------------------------------|----------|
| T-15-03 | Info Disclosure | phantom/Vast aggregate read on `/admin/economy` | Aggregate cost numbers only; no per-tenant PII, no prompt/response content selected. Confirmed: `EconomyResponse` (`economy.go:37-71`) carries only BRL sums + day series; `SumBillingAllTenantsRange`/`SumPhantomAllTenantsByDate` select only `cost_*`/`COUNT` (`db/queries/billing.sql:86-107`). No content columns, no tenant identifiers in the response. | ACCEPTED — risk owner: gateway/observability; endpoint gated by X-Admin-Key (T-15-02). |
| T-15-04 | DoS | unbounded series scan on `/admin/economy` | Low volume; default range = current month; seq-scan over one month partition acceptable (Pitfall 7). Confirmed: handler defaults to current-month bounds when from/to absent (`economy.go:124-154`). No row cap on the economy series, accepted given low admin-only traffic. | ACCEPTED — risk owner: gateway/observability; endpoint gated by X-Admin-Key. Re-evaluate if range becomes operator-tunable beyond a single month. |

---

## Unregistered Flags

None. No SUMMARY (`15-01..15-04-SUMMARY.md`) contains a `## Threat Flags`
section. All deviations recorded in the Deviations sections are environment-only
(ClickUp skip marker propagation, `bun install`/node_modules) or non-security
(chart color token `--status-healthy`→valid tokens; `setOffset(0)` on filter
change) and introduce no new attack surface.

**Note (informational, not a gap):** `dashboard/src/components/ui/chart.tsx:77`
uses `dangerouslySetInnerHTML` — this is stock shadcn chart library code that
injects only developer-defined CSS color tokens from `chartConfig`. It never
receives audit search input or audit content, and predates Phase 15. Not a
phase-15 attack surface.

---

## Summary

- **mitigate:** 14/14 CLOSED — each mitigation located at a specific file:line.
- **accept:** 2/2 logged with rationale and risk owner.
- **threats_open:** 0
- **Implementation files modified:** none (read-only audit).
