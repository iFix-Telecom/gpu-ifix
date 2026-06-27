---
phase: 15-dashboard-economia-e-historico
plan: 04
subsystem: dashboard-incidents
tags: [obs-10, audit, incidents, dashboard, nextjs, react-query, pagination]
requires:
  - GET /admin/audit?from=&to=&search= + AuditResponse.Total (15-02 — real COUNT)
  - proxyGet<T> server-side proxy (gateway.ts) — admin key stays server-side
  - Popover+Calendar + Input dashboard primitives (consumo/page.tsx analog)
provides:
  - AuditResponse.total TS field + fetchAudit(limit, offset, from?, to?, search?) wrapper
  - /incidents date-range picker + free-text search box
  - audit-table.tsx total-driven pager (canNext = offset + limit < total)
affects:
  - /incidents history panel (now filterable + honestly paginated)
tech-stack:
  added: []
  patterns:
    - optional query params spread into proxyGet only when truthy (preserves handler month default)
    - copied consumo isoDate/currentMonthRange local-date helpers (WR-08, no toISOString)
    - server-total-driven pager with full-page heuristic fallback
key-files:
  created: []
  modified:
    - dashboard/src/lib/gateway.ts
    - dashboard/src/lib/gateway.test.ts
    - dashboard/src/app/(dashboard)/incidents/page.tsx
    - dashboard/src/components/audit-table.tsx
decisions:
  - "fetchAudit forwards from/to/search only when truthy — an empty value would override the handler's current-month default (Pitfall 6)"
  - "AuditResponse.total typed number (not optional) — 15-02 always emits the COUNT; AuditTable prop total is optional for fallback safety"
  - "offset resets to 0 on any filter change (applyPeriod / search keystroke) so the pager never lands on an out-of-range page (Rule 2 correctness)"
  - "metadata-only columns unchanged — no audit_log_content column added (T-15-15)"
metrics:
  duration: ~12m
  completed: 2026-06-26
  tasks: 2
  files_created: 0
  files_modified: 4
---

# Phase 15 Plan 04: Incident history filter frontend (OBS-10) Summary

`/incidents` now has a date-range picker and a free-text search box above the
table, and its pager is driven by the real `total` COUNT (15-02) instead of a
row-count heuristic — `fetchAudit` forwards `from`/`to`/`search` to the gateway
through the existing server-side proxy.

## What Was Built

**Task 1 — AuditResponse.total + fetchAudit(from/to/search) (TDD: RED 71f08e0 → GREEN 9f3d258)**
- `gateway.test.ts`: existing fetchAudit fixture typed as `AuditResponse` and
  given a real `total: 137` (asserted); two new cases — `fetchAudit(50, 0,
  "2026-06-01", "2026-06-30", "503")` puts from/to/search in the proxy URL, and
  `fetchAudit(50, 0)` omits the optional keys (`?limit=50&offset=0` exactly).
  RED failed on the forwarding case (`expected '…?limit=50&offset=0' to contain
  'from=2026-06-01'`) before implementation — the omit + total cases passed at
  RED (the cast accepts the fixture; current impl already omits), so no
  unexpected behavior.
- `gateway.ts`: added `total: number` to `AuditResponse` (and rewrote the
  l.141-143 "no total" comment). Extended `fetchAudit` to
  `(limit = 50, offset = 0, from?, to?, search?)`, spreading `from`/`to`/`search`
  into `proxyGet<AuditResponse>("audit", {...})` only when truthy. Browser never
  reads GATEWAY_ADMIN_KEY — all calls go through the server proxy (T-15-14);
  search travels as a query param to the gateway's parameterized ILIKE, no
  client-side SQL (T-15-13).

**Task 2 — /incidents controls + total-driven pager (commit 5a6fadd)**
- `incidents/page.tsx`: copied `isoDate`/`currentMonthRange` (LOCAL date
  components, WR-08), added `range`/`applied`/`search` state defaulting to the
  current month (Pitfall 6). Rendered the consumo Popover+Calendar date-range
  block and an `Input` search box above the table. `useQuery` now keyed
  `["audit", offset, applied, search]` with
  `queryFn: () => fetchAudit(PAGE_SIZE, offset, applied.from, applied.to, search)`
  and passes `total={data?.total}` to `AuditTable`. Offset resets to 0 on
  `applyPeriod()` and on each search keystroke so the pager never lands on an
  out-of-range page. Loading/error branches preserved.
- `audit-table.tsx`: added `total?: number` to `AuditTableProps`; replaced the
  inferred `canNext = rows.length >= limit` with
  `canNext = total !== undefined ? offset + limit < total : rows.length >= limit`
  (docstring + inline comment updated); pager footer now shows `{from}–{to} de
  {total}` when total is known. Metadata-only columns and `key={row.request_id}`
  unchanged (T-15-15).

## Verification

- `cd dashboard && bun run test src/lib/gateway.test.ts` — 9 pass (incl. 3 audit)
- `cd dashboard && bun run test` — full suite 38 pass (8 files)
- `cd dashboard && bunx tsc --noEmit` — clean (no new type errors)
- guards: `grep -q 'offset + limit < total' audit-table.tsx` OK; `grep -q
  'Calendar' incidents/page.tsx` OK; the only `toISOString` in incidents/page.tsx
  is the WR-08 docstring comment (verbatim from consumo, which has the same
  single occurrence) — no actual call.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Installed dashboard deps + propagated ClickUp marker**
- **Found during:** Task 1 setup.
- **Issue:** The worktree had no `dashboard/node_modules` (vitest/tsc missing)
  and no `.planning/clickup-active-task.json` (PostToolUse hook would block edits
  — the worktree has a COPY of `.planning`, not a symlink).
- **Fix:** `bun install` in dashboard/ (resolved against the committed bun.lock —
  lock unchanged); copied the main-repo ClickUp skip marker into the worktree
  `.planning/`. Both environment-only — node_modules is gitignored; the marker is
  not part of any code commit.
- **Files modified:** none committed (environment only).

**2. [Rule 2 - Correctness] offset reset on filter change**
- **Found during:** Task 2.
- **Issue:** The plan's contract threads `applied`/`search` into the queryKey but
  did not specify resetting `offset`. Changing the period or search while on a
  high offset could land the user on an empty/out-of-range page.
- **Fix:** `setOffset(0)` in `applyPeriod()` and in the search `onChange`.
- **Files modified:** dashboard/src/app/(dashboard)/incidents/page.tsx
- **Commit:** 5a6fadd

## Deferred Issues

**lint gate non-functional in this repo (not introduced by this plan).**
The plan verify includes `bun run lint`, but the dashboard package has no
eslint/biome config tracked in git and `next lint` is deprecated/interactive
(documented in 15-03). `bunx tsc --noEmit` (clean) is the working typecheck gate
and was used in its place. Configuring eslint is out of scope (tooling task).

## Self-Check: PASSED
- dashboard/src/lib/gateway.ts — FOUND
- dashboard/src/lib/gateway.test.ts — FOUND
- dashboard/src/app/(dashboard)/incidents/page.tsx — FOUND
- dashboard/src/components/audit-table.tsx — FOUND
- commit 71f08e0 (RED) — FOUND
- commit 9f3d258 (GREEN) — FOUND
- commit 5a6fadd (Task 2) — FOUND

## TDD Gate Compliance
- Task 1: RED commit (test, 71f08e0) precedes GREEN commit (feat, 9f3d258). RED
  failed on the from/to/search forwarding assertion before implementation — no
  unexpected pass on the behavior under test. REFACTOR not needed.
