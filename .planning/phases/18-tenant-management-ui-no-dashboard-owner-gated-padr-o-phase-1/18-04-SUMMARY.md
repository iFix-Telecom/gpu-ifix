---
phase: 18-tenant-management-ui-no-dashboard-owner-gated-padr-o-phase-1
plan: 04
subsystem: web
tags: [rsc, next, owner-gate, tenant-management, ui, client-island, alert-dialog, gateway-admin, security]

requires:
  - phase: 18-03
    provides: createTenant/createTenantKey/revokeKey server actions (owner-gated, audited)
  - phase: 18-02
    provides: fetchTenants/fetchTenantKeys (proxy GET), TenantRow/TenantKeyRow types, fetchTenantsServer (RSC read)
  - phase: 17
    provides: getViewerRole (server-side role), operacao/config RSC owner-aware pattern, operator-controls island pattern
provides:
  - "/tenants/gerenciar RSC owner-aware page (getViewerRole + fetchTenantsServer → isOwner + tenants)"
  - "tenant-controls.tsx client island: list tenants (expand→keys), create tenant, generate key (raw 1×), revoke (impact confirm), cosmetic owner gate"
  - "sidebar NavItem Tenants (gestão) → /tenants/gerenciar (new route, /tenants metrics untouched)"
  - "gatewayAdminGet server-only reader (gateway-admin.ts) — RSC reads gateway directly, no self-fetch hairpin"
affects: []

tech-stack:
  added: []
  patterns:
    - "RSC owner-aware page: getViewerRole server-side + fetchTenantsServer, isOwner cosmetic gate; server actions re-check requireOwner"
    - "Raw API key shown ONCE from createTenantKey return in a copy block; never persisted/refetched (fetchTenantKeys never returns key)"
    - "Dangerous revoke = alert-dialog with explicit impact string, 1-click confirm, NO type-to-confirm, default-focus Cancel"
    - "Server-context gateway reads call the gateway directly via gatewayAdminGet (blessed server-only file), not the public /api/gateway proxy URL (which middleware 307s to /login)"

key-files:
  created:
    - dashboard/src/app/(dashboard)/tenants/gerenciar/page.tsx
    - dashboard/src/app/(dashboard)/tenants/gerenciar/tenant-controls.tsx
    - dashboard/src/lib/gateway-admin.test.ts
  modified:
    - dashboard/src/components/app-sidebar.tsx
    - dashboard/src/lib/gateway-admin.ts
    - dashboard/src/lib/gateway-server.ts
    - dashboard/src/lib/gateway-server.test.ts

key-decisions:
  - "RSC self-fetch through the public /api/gateway proxy URL was the source of the UAT failure ('Unexpected token <, <!DOCTYPE'): middleware gates /api/gateway/*, 307-redirects the self-call to /login, fetch follows it, res.json() parses the login HTML. Fixed by reading the gateway DIRECTLY via a new gatewayAdminGet in gateway-admin.ts (the blessed server-only file), removing the hairpin. Same latent bug affected fetchPodConfigServer (config page) since the dashboard moved to worker-vm."
  - "gatewayAdminGet lives in gateway-admin.ts so GATEWAY_ADMIN_KEY stays in exactly {route.ts, gateway-admin.ts} — leak-guard (T-07-24/T-18-03) unchanged; gateway-server.ts references no key (imports the function)."
  - "data-class selector lives ONLY in the generate-key dialog (data_class is per-key, RES-08), never in create-tenant."
  - "Owner gate is cosmetic (buttons hidden when !isOwner); the 18-03 server actions re-check requireOwner server-side — hidden-but-callable is still barred."

patterns-established:
  - "Server-context gateway reads → gatewayAdminGet (direct); browser reads → proxyGet (via /api/gateway). Never RSC→own-public-URL→middleware→proxy."

requirements-completed: [TEN-UI-10, TEN-UI-11]

duration: 90 min
completed: 2026-07-06
---

# Phase 18 Plan 04: Tenants management page Summary

**Owner-aware `/tenants/gerenciar` page — RSC (`getViewerRole` + `fetchTenantsServer`) feeding a client island that lists tenants, creates a tenant, generates an API key (raw shown once), and revokes keys behind an impact-confirm; data-class selector in the generate-key flow; sidebar nav added. Operator sees the same data read-only. E2E owner/operator flow human-verified in prod (approved 2026-07-06).**

## Accomplishments
- **Task 1 (`43417cd`)** — `tenants/gerenciar/page.tsx` RSC (`export const dynamic="force-dynamic"`, `getViewerRole` → `isOwner`, `fetchTenantsServer`, pt-BR error card) + sidebar NavItem "Tenants (gestão)" → `/tenants/gerenciar` (the metrics `/tenants` item untouched).
- **Task 2 (`bc4cd95`)** — `tenant-controls.tsx` `"use client"` island: expandable tenant table (fetches keys on demand), create-tenant dialog, generate-key dialog with data-class select (raw shown once in a copy block, not persisted), revoke via alert-dialog with impact string (1-click, no type-to-confirm), cosmetic `isOwner` gate.
- **Task 3 — human-verify checkpoint APPROVED** — owner create/generate-raw-once/revoke-impact/data-class + operator read-only, validated live on `ai-dashboard.converse-ai.app`.

## Deviations / gap closed during UAT
- **RSC self-fetch hairpin bug (`89a5972`)** — first UAT load failed with `Unexpected token '<', "<!DOCTYPE"`. Root cause: `fetchTenantsServer` rebuilt the public `/api/gateway/tenants` URL and self-fetched it; `middleware.ts` gates `/api/gateway/*`, 307-redirected the self-call to `/login`, `fetch` followed it, `res.json()` parsed the login HTML. Fixed by adding `gatewayAdminGet` in `gateway-admin.ts` and having the RSC readers (`fetchTenantsServer` + `fetchPodConfigServer`) call the gateway directly with `X-Admin-Key` — no hairpin, no middleware re-auth. Leak-guard held (key still only in `route.ts` + `gateway-admin.ts`). Added `gateway-admin.test.ts` (direct GET path/header/error) and rewrote `gateway-server.test.ts` to delegation. 78/78 dashboard suite green.
- Doc-comment reworded ("no type-to-confirm" → "no text-entry step") to satisfy the literal acceptance grep; keyed `<Fragment>` for the row+expanded-keys pair (React map-key). No behavior change.

## Verification
- `tsc --noEmit` clean; `vitest run` 78/78; leak-guard green; 5 admin routes live under X-Admin-Key (`/admin/tenants` verified 401 without key / 200 with).
- Deployed to prod (gateway `develop-1175ef2` stack 38, dashboard `develop-89a5972` stack 40, digest-repointed); E2E owner/operator approved.

## Self-Check: PASSED
