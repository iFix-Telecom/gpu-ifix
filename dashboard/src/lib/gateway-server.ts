import "server-only";

/**
 * Server-side gateway read helpers (Plan 17-06, fixed 18-04-gap).
 *
 * A Server Component cannot fetch the RELATIVE `/api/gateway/*` proxy (no
 * origin). The original approach rebuilt the ABSOLUTE proxy URL from the
 * inbound headers and self-fetched it — but that round-trip re-enters
 * `middleware.ts` (which gates `/api/gateway/*`), and the self-call is
 * 307-redirected to /login; `fetch` follows it and returns the login HTML, so
 * `res.json()` throws "Unexpected token '<'". Instead these readers call the
 * gateway DIRECTLY via `gatewayAdminGet` (gateway-server never leaves the
 * server; the page that renders it is already auth-gated by middleware). The
 * admin key stays ONLY in the proxy (route.ts) + gateway-admin.ts — this module
 * reads NO key, so the leak-guard invariant holds (T-07-24 / T-18-03).
 *
 * `import "server-only"` makes any accidental client import a build error.
 */

import { gatewayAdminGet } from "@/lib/gateway-admin";
import type { PodConfigResponse, TenantRow } from "@/lib/gateway";

/**
 * GET /admin/primary/config from a SERVER context — the `/operacao/config` RSC
 * reads the current pod config server-side.
 */
export function fetchPodConfigServer(): Promise<PodConfigResponse> {
  return gatewayAdminGet<PodConfigResponse>("primary/config");
}

/**
 * GET /admin/tenants from a SERVER context — the tenant-management RSC (Plan
 * 18-04) reads the tenant list server-side after the owner check.
 */
export function fetchTenantsServer(): Promise<TenantRow[]> {
  return gatewayAdminGet<TenantRow[]>("tenants");
}
