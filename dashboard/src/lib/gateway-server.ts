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

import { GatewayError, type PodConfigResponse } from "@/lib/gateway";

/** Error-envelope shape the proxy/gateway emit (mirrors gateway.ts). */
interface ErrorEnvelope {
  error?: { message?: string; type?: string };
}

/**
 * GET /admin/primary/config from a SERVER context (RSC / server action). Routes
 * through the request-derived absolute `/api/gateway/primary/config` proxy URL
 * so the admin key stays server-only. Throws GatewayError on non-2xx, surfacing
 * the specific envelope message when present.
 */
export async function fetchPodConfigServer(): Promise<PodConfigResponse> {
  const h = await headers();
  const host = h.get("x-forwarded-host") ?? h.get("host");
  if (!host) {
    throw new GatewayError(
      500,
      "Não foi possível resolver o host da requisição para ler a configuração do pod.",
      "configuration_error",
    );
  }
  const proto = h.get("x-forwarded-proto") ?? "http";
  const url = `${proto}://${host}/api/gateway/primary/config`;

  // Forward the inbound session cookie. This request re-enters the app through
  // the public origin, so it passes back through `middleware.ts`, which gates
  // /api/gateway/* behind the auth + 2FA session. Without the cookie the
  // self-call is unauthenticated → middleware 307-redirects to /login and the
  // fetch follows it to an HTML page, making `res.json()` throw
  // "Unexpected token '<'". The page only renders after the caller's session is
  // fully verified, so forwarding that cookie is always valid here.
  const cookie = h.get("cookie");

  const res = await fetch(url, {
    method: "GET",
    headers: {
      Accept: "application/json",
      ...(cookie ? { Cookie: cookie } : {}),
    },
    cache: "no-store",
  });

  if (!res.ok) {
    let message = "Não foi possível carregar a configuração do pod.";
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

  return (await res.json()) as PodConfigResponse;
}
