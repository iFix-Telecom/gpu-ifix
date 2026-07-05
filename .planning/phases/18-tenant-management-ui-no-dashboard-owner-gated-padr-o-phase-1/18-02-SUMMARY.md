---
phase: 18-tenant-management-ui-no-dashboard-owner-gated-padr-o-phase-1
plan: 02
subsystem: ui
tags: [nextjs, dashboard, server-only, fetch, gateway-admin, rsc, typescript]

requires:
  - phase: 18-01
    provides: "gateway admin handlers (GET/POST tenants + keys, POST keys/{id}/revoke) that these wrappers/helper consume"
  - phase: 17-05
    provides: "gateway-admin.ts PATCH write helper + T-07-24 leak-guard invariant"
provides:
  - "gatewayAdminPost<T>/gatewayAdminPatch — common server-only X-Admin-Key mutation path; POST returns gateway JSON verbatim"
  - "fetchTenants/fetchTenantKeys — GET-only proxy read wrappers (no admin key in browser)"
  - "fetchTenantsServer() — RSC server-side tenant read (absolute URL from headers, cookie forwarded)"
  - "TenantRow/TenantKeyRow types mirroring the 18-01 Go handlers field-for-field"
affects: [18-03, 18-04]

tech-stack:
  added: []
  patterns:
    - "gatewayAdminMutate<T>(method,path,body) common path — method-parameterized, parses JSON body when present else undefined"
    - "proxyGetServer<T>(path,fallbackMsg) shared RSC read helper (dedup of fetchPodConfigServer)"

key-files:
  created: []
  modified:
    - dashboard/src/lib/gateway-admin.ts
    - dashboard/src/lib/gateway.ts
    - dashboard/src/lib/gateway-server.ts
    - dashboard/src/lib/gateway.test.ts

key-decisions:
  - "Stack 40 (dashboard prod, Portainer endpoint 6/worker-vm) GATEWAY_BASE_URL=http://gateway:8080 → swarm alias 'gateway' of service ai-gateway-prod_gateway (consolidated worker-vm gateway, image ifix-ai-gateway@sha256:2b3e561c). Confirmed NOT n8n-ia-vm/vps-ifix-vm. Gate PASSED."
  - "Refactored fetchPodConfigServer into a shared proxyGetServer<T> helper instead of duplicating ~45 lines for fetchTenantsServer (DRY; leak-guard still holds — no key)."
  - "gatewayAdminMutate returns parsed JSON only when Content-Type is JSON and status != 204; PATCH pod-config (204) stays Promise<void>."

patterns-established:
  - "Write helper method-parameterized: one X-Admin-Key path serves both POST (returns body) and PATCH (void)"
  - "RSC reads route through the same GET-only /api/gateway/* proxy via an absolute request-derived URL — admin key never leaves route.ts + gateway-admin.ts"

requirements-completed: [TEN-UI-06, TEN-UI-07]

duration: 12min
completed: 2026-07-05
---

# Phase 18 Plan 02: Dashboard server-side tenant layer Summary

**Generalized the server-only gateway write helper to POST (returning the gateway JSON verbatim) and added GET-only tenant read wrappers + an RSC server read, with the admin key confined to the two server-only files (leak-guard green) and dashboard prod confirmed pointing at the consolidated worker-vm gateway.**

## Performance

- **Duration:** 12 min
- **Started:** 2026-07-05T19:34:00Z
- **Completed:** 2026-07-05T19:40:00Z
- **Tasks:** 2
- **Files modified:** 4

## Accomplishments
- **Operational gate PASSED:** stack 40 `GATEWAY_BASE_URL=http://gateway:8080`, where `gateway` is the swarm network alias of `ai-gateway-prod_gateway` on worker-vm (endpoint 6, image `ghcr.io/ifixtelecom/ifix-ai-gateway@sha256:2b3e561c`). Dashboard and gateway services share both overlay networks. Confirmed the consolidated worker-vm gateway — not a decommissioned n8n-ia-vm/vps-ifix-vm gateway.
- `gateway-admin.ts` generalized: extracted `gatewayAdminMutate<T>(method, path, body)` common path; `gatewayAdminPost<T>` returns the gateway JSON body verbatim (create-key `{id,key_prefix,data_class,key}`, never key_hash/key_lookup_hash); `gatewayAdminPatch` preserved as `Promise<void>`. `import "server-only"` intact.
- `gateway.ts`: `TenantRow`/`TenantKeyRow` types (field-for-field with the 18-01 Go handlers, `last_used_at` nullable) + `fetchTenants`/`fetchTenantKeys` via the GET-only proxy; `fetchTenantKeys` encodeURIComponent-encodes the slug.
- `gateway-server.ts`: `fetchTenantsServer()` RSC read (absolute URL from headers, cookie forwarded).
- Leak-guard green: `GATEWAY_ADMIN_KEY` appears in exactly `{route.ts, gateway-admin.ts}`. Full suite 16/16 green.

## Task Commits

1. **Task 1: gate + generalize gateway-admin.ts for POST** - `618016a` (feat)
2. **Task 2: tenant read wrappers + RSC server read + types + tests** - `363b913` (feat)

## Files Created/Modified
- `dashboard/src/lib/gateway-admin.ts` - method-parameterized mutation path; gatewayAdminPost<T> + gatewayAdminPatch
- `dashboard/src/lib/gateway.ts` - TenantRow/TenantKeyRow + fetchTenants/fetchTenantKeys (proxyGet)
- `dashboard/src/lib/gateway-server.ts` - shared proxyGetServer<T> + fetchTenantsServer()
- `dashboard/src/lib/gateway.test.ts` - fetchTenants/fetchTenantKeys wrapper tests (paths, nullable, slug encoding)

## Decisions Made
- Stack 40 gate: `http://gateway:8080` resolves via swarm alias to the consolidated worker-vm prod gateway (verified via Portainer API + `docker service inspect` on worker-vm). Gate PASSED, coding proceeded.
- Types verified against the real 18-01 handlers (`tenants_admin_http.go` / `keys_admin_http.go` JSON tags), not just the plan's `<interfaces>` — they matched.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Simplification] Extracted shared `proxyGetServer<T>` helper in gateway-server.ts**
- **Found during:** Task 2 (fetchTenantsServer)
- **Issue:** The plan said to add `fetchTenantsServer()` "mirroring fetchPodConfigServer" — a literal mirror duplicates ~45 lines of header/cookie/error plumbing.
- **Fix:** Factored the common logic into `proxyGetServer<T>(path, fallbackMsg)`; both `fetchPodConfigServer` and `fetchTenantsServer` delegate to it. Only behavioral change: the host-missing error message is now generic ("Não foi possível resolver o host da requisição.") instead of pod-specific — no test asserts that string, and the happy path + non-2xx envelope handling are unchanged.
- **Files modified:** dashboard/src/lib/gateway-server.ts
- **Verification:** `bunx tsc --noEmit` clean; leak-guard + 16/16 tests green; no test imports gateway-server.ts so no assertion broke.
- **Committed in:** 363b913 (Task 2 commit)

---

**Total deviations:** 1 auto-fixed (1 simplification)
**Impact on plan:** DRY refactor within the same file the plan already modifies; no scope creep, no new files, no behavior change on any tested path. package.json unchanged (zero new deps).

## Issues Encountered
None.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Server-side layer ready for Plan 18-03 server actions: `gatewayAdminPost<T>` (create tenant/key + revoke) + `gatewayAdminPatch`, read wrappers `fetchTenants`/`fetchTenantKeys`, and RSC `fetchTenantsServer()`. Plan 18-03 only CONSUMES gateway-admin.ts (never edits it), so the leak-guard invariant is locked.
- No blockers.

## Self-Check: PASSED
- gateway-admin.ts line 1 `import "server-only";` — intact.
- `grep -rln GATEWAY_ADMIN_KEY src --include='*.ts' | grep -v .test.` → exactly `gateway-admin.ts` + `route.ts`.
- `bunx vitest run src/lib/gateway.test.ts` → 16/16 passed (leak-guard + wrappers).
- `bunx tsc --noEmit` → clean.
- Stack 40 GATEWAY_BASE_URL recorded + confirmed = consolidated worker-vm gateway.

---
*Phase: 18-tenant-management-ui-no-dashboard-owner-gated-padr-o-phase-1*
*Completed: 2026-07-05*
