import "server-only";

/**
 * Server-side gateway read helper (Plan 17-06).
 *
 * The browser-facing `fetchPodConfig` (gateway.ts) fetches the RELATIVE proxy
 * path `/api/gateway/*`, which only resolves in the browser — a Server
 * Component cannot fetch a relative URL (no origin). The `/operacao/config`
 * page is an RSC (it reads the owner role server-side), so it needs to read the
 * current pod config server-side. This helper rebuilds the ABSOLUTE proxy URL
 * from the inbound request headers and calls the SAME GET-only
 * `/api/gateway/primary/config` proxy — so the admin key still lives ONLY in
 * the proxy + gateway-admin.ts (this module reads NO key; the leak-guard
 * invariant holds, T-07-24).
 *
 * `import "server-only"` makes any accidental client import a build error.
 */

import { headers } from "next/headers";

import {
  GatewayError,
  type PodConfigResponse,
  type TenantRow,
} from "@/lib/gateway";

/** Error-envelope shape the proxy/gateway emit (mirrors gateway.ts). */
interface ErrorEnvelope {
  error?: { message?: string; type?: string };
}

/**
 * GET a `/api/gateway/*` proxy path from a SERVER context (RSC / server action).
 * Rebuilds the ABSOLUTE proxy URL from the inbound request headers (a Server
 * Component cannot fetch a relative URL) and forwards the session cookie so the
 * self-call passes back through `middleware.ts` auth (without it the middleware
 * 307-redirects to /login and `res.json()` throws on the HTML). The admin key
 * still lives ONLY in the proxy + gateway-admin.ts (this module reads NO key —
 * leak-guard invariant holds, T-07-24). `fallbackMsg` labels the generic error.
 */
async function proxyGetServer<T>(path: string, fallbackMsg: string): Promise<T> {
  const h = await headers();
  const host = h.get("x-forwarded-host") ?? h.get("host");
  if (!host) {
    throw new GatewayError(
      500,
      "Não foi possível resolver o host da requisição.",
      "configuration_error",
    );
  }
  const proto = h.get("x-forwarded-proto") ?? "http";
  const cookie = h.get("cookie");

  const res = await fetch(`${proto}://${host}/api/gateway/${path}`, {
    method: "GET",
    headers: {
      Accept: "application/json",
      ...(cookie ? { Cookie: cookie } : {}),
    },
    cache: "no-store",
  });

  if (!res.ok) {
    let message = fallbackMsg;
    let type: string | null = null;
    try {
      const body = (await res.json()) as ErrorEnvelope;
      if (body.error?.message) message = body.error.message;
      if (body.error?.type) type = body.error.type;
    } catch {
      // Non-JSON / empty body — keep the generic fallback.
    }
    throw new GatewayError(res.status, message, type);
  }

  return (await res.json()) as T;
}

/**
 * GET /admin/primary/config from a SERVER context — the `/operacao/config` RSC
 * reads the current pod config server-side. Routes through the same GET-only
 * proxy so the admin key stays server-only.
 */
export function fetchPodConfigServer(): Promise<PodConfigResponse> {
  return proxyGetServer<PodConfigResponse>(
    "primary/config",
    "Não foi possível carregar a configuração do pod.",
  );
}

/**
 * GET /admin/tenants from a SERVER context — the tenant-management RSC (Plan
 * 18-03) reads the tenant list server-side after the owner check.
 */
export function fetchTenantsServer(): Promise<TenantRow[]> {
  return proxyGetServer<TenantRow[]>(
    "tenants",
    "Não foi possível carregar a lista de tenants.",
  );
}
