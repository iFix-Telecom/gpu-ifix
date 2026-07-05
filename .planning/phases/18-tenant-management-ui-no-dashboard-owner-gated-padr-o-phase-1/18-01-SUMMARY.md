---
phase: 18-tenant-management-ui-no-dashboard-owner-gated-padr-o-phase-1
plan: 01
subsystem: api
tags: [go, chi, sqlc, admin-api, tenant, api-key, argon2]

requires:
  - phase: 17-primary-pod-config-ui
    provides: "config_read/config_write admin-handler template (isolated query interface, dual constructor, OpenAI envelope, obs.GatewayAdminRequests metric)"
  - phase: 02-gateway-http-auth
    provides: "auth.GenerateAPIKey + InsertAPIKey/RevokeAPIKey/ListActiveKeys*WithMeta sqlc queries"
provides:
  - "GET/POST /admin/tenants — tenant list + create (slug dup → 409)"
  - "GET/POST /admin/tenants/{slug}/keys — key list (operator-safe) + create (raw once)"
  - "POST /admin/keys/{id}/revoke — idempotent revoke"
  - "TenantAdminHandler + KeysAdminHandler mounted under X-Admin-Key adminRouter"
affects: [18-02-dashboard-proxy, tenant-management-ui]

tech-stack:
  added: []
  patterns:
    - "Multi-method admin handler: one struct exposes List/Create[/Revoke] methods mounted as separate chi routes (concrete pointer in proxies struct, not http.Handler)"
    - "Secret-once serialization: create-key response struct has no hash field; raw key in field 'key' only, never logged"

key-files:
  created:
    - gateway/internal/admin/tenants_admin_http.go
    - gateway/internal/admin/tenants_admin_http_test.go
    - gateway/internal/admin/keys_admin_http.go
    - gateway/internal/admin/keys_admin_http_test.go
  modified:
    - gateway/cmd/gateway/main.go

key-decisions:
  - "Tenant/key handlers stored as concrete *admin.TenantAdminHandler / *admin.KeysAdminHandler pointers in proxies (not http.Handler) because each serves multiple method routes from one struct — one nil-guard per handler field"
  - "Reused existing admin.dataClassString for status+data_class ENUM (interface{}) coercion instead of adding a new helper"

patterns-established:
  - "Multi-route admin handler mounted with http.HandlerFunc(h.Method) per chi route under a single nil-guard"

requirements-completed: [TEN-UI-01, TEN-UI-02, TEN-UI-03, TEN-UI-04, TEN-UI-05]

duration: 5min
completed: 2026-07-05
---

# Phase 18 Plan 01: Tenant/Key Admin HTTP Handlers Summary

**5 owner-gated admin endpoints (tenant list/create + key list/create/revoke) as thin handlers over existing sqlc queries + auth.GenerateAPIKey — create-key serializes the raw key exactly once and never a hash.**

## Performance

- **Duration:** 5 min
- **Started:** 2026-07-05T22:26:04Z
- **Completed:** 2026-07-05T22:31:43Z
- **Tasks:** 3
- **Files modified:** 5 (4 created, 1 modified)

## Accomplishments
- `tenants_admin_http.go` — `TenantAdminHandler.List`/`.Create` over ListTenants/CreateTenant; empty slug/name → 400 before any query; slug unique_violation (pg 23505) → 409 `tenant_exists`.
- `keys_admin_http.go` — `KeysAdminHandler.List`/`.Create`/`.Revoke`; create mints via auth.GenerateAPIKey + InsertAPIKey, returns raw in `key` field once (never key_hash/key_lookup_hash, never logged); data_class whitelisted ∈ {normal,sensitive} before query; revoke idempotent (status='active'-scoped UPDATE).
- 5 routes mounted inside the `if px.adminVerifier != nil` adminRouter (X-Admin-Key middleware), one nil-guard per handler field.
- 9 new fake-queries tests (no DB), all green alongside the pre-existing admin suite.

## Task Commits

1. **Task 1: Tenant handlers (GET+POST /admin/tenants)** - `43b51f6` (feat)
2. **Task 2: Key handlers (list/create raw-once/revoke)** - `505453d` (feat)
3. **Task 3: Mount 5 routes in main.go + build/test** - `4d4ee09` (feat)

## Files Created/Modified
- `gateway/internal/admin/tenants_admin_http.go` - tenant list/create handlers over isolated `tenantAdminQueries`
- `gateway/internal/admin/tenants_admin_http_test.go` - list, create-OK, empty-slug-400-no-query, dup-409 tests
- `gateway/internal/admin/keys_admin_http.go` - key list/create/revoke handlers over isolated `keysAdminQueries`
- `gateway/internal/admin/keys_admin_http_test.go` - raw-once/no-hash, invalid-data_class-400, default-normal, invalid-id-400, idempotent-revoke, list-no-hash tests
- `gateway/cmd/gateway/main.go` - 2 proxy struct fields + handler construction + 5 route mounts under adminRouter

## Decisions Made
- **Concrete-pointer handler fields (not http.Handler):** the plan's `must_haves` suggested `http.Handler` fields, but each handler serves multiple method routes from one struct (List/Create, List/Create/Revoke), which no single `ServeHTTP` can express. Stored `*admin.TenantAdminHandler` / `*admin.KeysAdminHandler` and mounted `http.HandlerFunc(h.Method)` per route — keeps one nil-guard per handler field exactly as the acceptance criteria require.
- **Reused `admin.dataClassString`** (already in usage.go) for status + data_class ENUM (interface{}) coercion — no new helper.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Interface signature drift] Plan `<interfaces>` typed InsertAPIKey/GenerateAPIKey differently than the generated code**
- **Found during:** Task 2 (key handlers)
- **Issue:** The plan's `<interfaces>` block declared `auth.GenerateAPIKey() (... lookupHash ... string ...)` and `InsertAPIKeyParams{... KeyLookupHash, ... DataClass string}`. The real generated/source code uses `lookupHash []byte` (auth/argon2.go:46), `InsertAPIKeyParams.KeyLookupHash []byte`, and `DataClass interface{}` (admin.sql.go). Per the key_rules, I read the real gen file and used the real signatures.
- **Fix:** Passed `lookupHash []byte` straight through to `KeyLookupHash []byte`, and the validated data_class string into `DataClass interface{}` (pgx encodes string → ENUM, exactly as the gatewayctl create path does).
- **Files modified:** gateway/internal/admin/keys_admin_http.go
- **Verification:** `go build ./...` + `go test ./internal/admin/...` green.
- **Committed in:** 505453d (Task 2 commit)

**2. [Rule 1 - Field type] Handler struct fields stored as concrete pointers, not http.Handler**
- **Found during:** Task 3 (route mount)
- **Issue:** `must_haves` guidance said "2 campos `http.Handler`", but a multi-method handler cannot be a single http.Handler.
- **Fix:** Concrete `*admin.TenantAdminHandler` / `*admin.KeysAdminHandler` fields; routes mounted via `http.HandlerFunc(h.Method)`. One nil-guard per field, all inside the adminVerifier block — satisfies every acceptance criterion.
- **Files modified:** gateway/cmd/gateway/main.go
- **Verification:** `grep -c 'adminRouter.Method.*"/tenants"'` = 2; all 5 routes present under their per-handler guards; build+tests green.
- **Committed in:** 4d4ee09 (Task 3 commit)

---

**Total deviations:** 2 auto-fixed (both Rule 1 — used real generated signatures / correct field type per key_rules).
**Impact on plan:** No scope change. Both are the plan's own instruction ("if a query signature doesn't match, use the real one") applied. No new migration, no new go.mod deps — `go.mod` unchanged.

## Issues Encountered
None.

## Security (T-18-04 / T-18-02)
- create-key response struct (`createKeyResponse`) has no hash field; test `TestKeyCreate_ReturnsRawOnceNoHash` asserts body contains `"key"` and `!Contains("key_hash") && !Contains("key_lookup_hash")`. Raw is never logged (log.Info carries only id/prefix/tenant/data_class).
- All 5 routes mounted inside `if px.adminVerifier != nil` → under `admin.Middleware` (X-Admin-Key). No key → 401 by the existing middleware.
- `key_hash`/`key_lookup_hash` appear in keys_admin_http.go only inside a doc comment; grep for `PATCH`/`restart` in the two new source files is empty (grep-clean per RESEARCH Pitfall 7).

## Next Phase Readiness
- Admin HTTP surface ready for the dashboard to consume via server-only proxy (Plan 18-02). Ingress unchanged.

## Self-Check: PASSED
- `[ -f ]` on all 4 created files: present.
- `git log --grep="18-01"`: 3 task commits (43b51f6, 505453d, 4d4ee09).
- `cd gateway && go build ./... && go test ./internal/admin/... -count=1`: green.
- `gofmt -l` on the 3 Go files: empty.
- 5 routes present under per-handler guards inside adminRouter.

---
*Phase: 18-tenant-management-ui-no-dashboard-owner-gated-padr-o-phase-1*
*Completed: 2026-07-05*
