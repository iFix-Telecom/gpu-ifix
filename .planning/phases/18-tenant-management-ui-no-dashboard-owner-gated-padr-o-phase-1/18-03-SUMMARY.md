---
phase: 18-tenant-management-ui-no-dashboard-owner-gated-padr-o-phase-1
plan: 03
subsystem: api
tags: [server-actions, better-auth, owner-gate, audit, next, gateway-admin, security]

requires:
  - phase: 18-02
    provides: gatewayAdminPost (server-only POST, returns gateway JSON body verbatim), gateway-admin.ts leak-guard
  - phase: 17-05
    provides: updatePodConfigCore molde (requireOwner→validar→gateway→audit→revalidate), admin-actions-core.ts / admin-actions.ts split
  - phase: 13
    provides: requireOwner (session-only identity, CR-01), writeAuditLog (admin_audit_log), inviteOperatorCore molde
provides:
  - createTenantCore/createTenantKeyCore/revokeKeyCore (server-only, owner-gated, audited) em admin-actions-core.ts
  - 3 wrappers "use server" thin (createTenant/createTenantKey/revokeKey) em admin-actions.ts
  - 6 testes TEN-UI-08/09 (owner-gate + audit + no-secret + validação server-side)
affects: [18-04]

tech-stack:
  added: []
  patterns:
    - "Tenant/key mutations espelham updatePodConfigCore 1:1: requireOwner FIRST → validação server-side → gatewayAdminPost → 1 audit → safeRevalidate"
    - "Raw secret (create-key `key`) retornado ao caller p/ exibição efêmera mas NUNCA em audit metadata nem log"

key-files:
  created: []
  modified:
    - dashboard/src/lib/admin-actions-core.ts
    - dashboard/src/lib/admin-actions.ts
    - dashboard/src/lib/admin-actions.test.ts

key-decisions:
  - "safeRevalidateTenants revalida /tenants/gerenciar (rota da UI do Plan 18-04); swallow fora de request scope como safeRevalidatePodConfig"
  - "post seam injetável (deps.post: typeof gatewayAdminPost) só p/ testes; default = gatewayAdminPost real. gateway-admin.ts NÃO tocado (leak-guard, dono 18-02)"
  - "key.create metadata = {tenant, data_class, key_prefix} — o raw `key` sai só no retorno da função, nunca auditado (T-18-04)"

patterns-established:
  - "TenantMutationDeps/CreatedKey exportados do core; wrappers importam os *Core + CreatedKey type, sem expor requireOwner/writeAuditLog (CR-02)"

requirements-completed: [TEN-UI-08, TEN-UI-09]

duration: 15 min
completed: 2026-07-05
---

# Phase 18 Plan 03: Dashboard tenant/key server actions Summary

**Three owner-gated Server Actions (createTenant / createTenantKey / revokeKey) mirroring updatePodConfigCore 1:1 — requireOwner first, server-side slug/name/data_class validation before any gateway call, exactly one admin_audit_log row per action, raw create-key returned to caller but never audited.**

## Performance

- **Duration:** 15 min
- **Started:** 2026-07-05T19:33:00Z
- **Completed:** 2026-07-05T19:48:00Z
- **Tasks:** 2
- **Files modified:** 3

## Accomplishments
- `createTenantCore` / `createTenantKeyCore` / `revokeKeyCore` in `admin-actions-core.ts` (server-only, no `"use server"`): `requireOwner` is the FIRST await in each, server-side validation (`slug` `/^[a-z0-9][a-z0-9-]*$/`, `name` non-empty, `data_class ∈ {normal,sensitive}`) runs BEFORE `gatewayAdminPost`, then EXACTLY one `writeAuditLog` row + `safeRevalidateTenants()`.
- `createTenantKeyCore` returns the raw `{id,key_prefix,data_class,key}` to the caller for one-time display; audit metadata carries only `{tenant,data_class,key_prefix}` — the raw `key` never lands in an audit row or log (T-18-04).
- 3 thin `"use server"` wrappers in `admin-actions.ts` calling `requireOwner()` with no args (session-only identity, CR-01); `requireOwner`/`writeAuditLog` remain unexported (CR-02).
- 6 TEN-UI tests: operator → 0 gateway + 0 audit; owner → 1 POST + 1 audit row; invalid slug and invalid data_class → 0 gateway; raw key returned to caller but absent from audit metadata.
- `gateway-admin.ts` NOT edited — `gatewayAdminPost` (Plan 18-02) already returns the parsed JSON body, so this plan only consumes it (leak-guard invariant intact).

## Task Commits

1. **Task 1: createTenantCore + createTenantKeyCore + revokeKeyCore (server-only)** - `5f01fa5` (feat)
2. **Task 2: Wrappers use-server + owner-gate/audit/no-secret tests** - `d45c9ba` (feat)

## Files Created/Modified
- `dashboard/src/lib/admin-actions-core.ts` - Added `createTenantCore`/`createTenantKeyCore`/`revokeKeyCore`, `TenantMutationDeps`/`CreatedKey` types, `safeRevalidateTenants`, and the `gatewayAdminPost` import.
- `dashboard/src/lib/admin-actions.ts` - Added 3 `"use server"` wrappers + imports; `createTenantKey` returns `CreatedKey`.
- `dashboard/src/lib/admin-actions.test.ts` - Added `gatewayAdminPostMock` (hoisted + gateway-admin mock) and a `TEN-UI-08/09` describe block (6 tests).

## Decisions Made
- `safeRevalidateTenants` targets `/tenants/gerenciar` (the Plan 18-04 UI route), swallowed outside a request scope like `safeRevalidatePodConfig`.
- `deps.post` seam typed as `typeof gatewayAdminPost` for zero-friction default; tests rely on the module-level `@/lib/gateway-admin` mock (same pattern as the POD-CFG tests) rather than injecting `deps.post`.

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
None.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- The 3 server actions are ready for the Plan 18-04 UI (`/tenants/gerenciar`) to consume: `createTenant`, `createTenantKey` (raw shown once), `revokeKey`.
- Owner-gate is server-side and non-bypassable; the Plan 18-04 UI-hide is cosmetic.

## Self-Check: PASSED
- `bunx vitest run src/lib/admin-actions.test.ts` → 19/19 passed (13 pre-existing + 6 new TEN-UI).
- `bunx tsc --noEmit` → clean (exit 0).
- 3 core fns exported; `admin-actions-core.ts` line 1 = `import "server-only";`, no `"use server"` directive.
- `createTenant`/`createTenantKey`/`revokeKey` exported from `admin-actions.ts`; `requireOwner` NOT exported.
- `gateway-admin.ts` unmodified by this plan (leak-guard preserved).

---
*Phase: 18-tenant-management-ui-no-dashboard-owner-gated-padr-o-phase-1*
*Completed: 2026-07-05*
