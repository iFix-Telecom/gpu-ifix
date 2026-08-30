import "server-only";

import { GatewayError } from "@/lib/gateway";

/**
 * Server-only gateway admin WRITE helper (Plan 17-05, generalized 18-02).
 *
 * This is the SECOND (and last) place in the dashboard that reads
 * GATEWAY_ADMIN_KEY — the GET-only proxy (app/api/gateway/[...path]/route.ts)
 * is the first. The `import "server-only"` marker makes any accidental import
 * into a client bundle a BUILD error, so the admin key can never cross the
 * browser boundary (threat T-07-24 / T-18-03). The owner write server actions
 * (admin-actions.ts updatePodConfig, and the Plan 18-03 tenant/key actions)
 * call these helpers AFTER `requireOwner` + server-side validation.
 *
 * Unlike the read proxy (GET, D-07), these helpers issue the gateway's mutation
 * verbs directly against the admin API with the `X-Admin-Key` header,
 * server-side only. `gatewayAdminPatch` drives PATCH /admin/primary/config;
 * `gatewayAdminPost` drives POST /admin/tenants, /admin/tenants/{slug}/keys,
 * /admin/keys/{id}/revoke and returns the gateway's JSON body verbatim (e.g.
 * create-key `{id, key_prefix, data_class, key}`). The gateway response NEVER
 * includes key_hash/key_lookup_hash (Plan 18-01 omits them) and nothing here
 * reintroduces them — the body is passed through as-is.
 */

/**
 * Mutate the gateway admin API at `${GATEWAY_BASE_URL}/admin/<path>` with the
 * given HTTP method, the `X-Admin-Key` header, and a JSON body. Throws on a
 * non-2xx response, surfacing the gateway's `{error:{message}}` envelope when
 * present so the calling action can report the specific validation failure. On
 * success it returns the parsed JSON body when the response carries one (JSON
 * Content-Type and not 204), otherwise `undefined` (PATCH pod-config is 204 /
 * empty). The admin key is read from `process.env` here and NEVER returned.
 */
async function gatewayAdminMutate<T = void>(
  method: "POST" | "PATCH" | "PUT" | "DELETE",
  path: string,
  body: unknown,
): Promise<T> {
  const base = process.env.GATEWAY_BASE_URL;
  const adminKey = process.env.GATEWAY_ADMIN_KEY;

  if (!base || !adminKey) {
    throw new Error(
      "Gateway admin não configurado — defina GATEWAY_BASE_URL e GATEWAY_ADMIN_KEY.",
    );
  }

  const target = `${base.replace(/\/$/, "")}/admin/${path}`;

  let res: Response;
  try {
    res = await fetch(target, {
      method,
      headers: {
        "X-Admin-Key": adminKey,
        "Content-Type": "application/json",
        Accept: "application/json",
      },
      cache: "no-store",
      body: JSON.stringify(body),
    });
  } catch {
    throw new Error(
      "Não foi possível alcançar o gateway. Verifique se o gateway está no ar.",
    );
  }

  if (!res.ok) {
    let message = "Falha ao gravar no gateway.";
    try {
      const env = (await res.json()) as { error?: { message?: string } };
      if (env.error?.message) message = env.error.message;
    } catch {
      // Non-JSON / empty body — keep the generic fallback message.
    }
    throw new Error(message);
  }

  // 204 No Content (PATCH pod-config) or a non-JSON body → nothing to parse.
  if (res.status === 204) return undefined as T;
  const contentType = res.headers.get("content-type") ?? "";
  if (!contentType.includes("application/json")) return undefined as T;
  return (await res.json()) as T;
}

/**
 * GET the gateway admin API at `${GATEWAY_BASE_URL}/admin/<path>` with the
 * `X-Admin-Key` header, server-side only. The RSC server readers
 * (gateway-server.ts) call this so a Server Component reads the gateway
 * DIRECTLY instead of hair-pinning through its own PUBLIC `/api/gateway/*`
 * proxy URL — that round-trip re-enters `middleware.ts`, which 307-redirects
 * the self-call to /login, and `res.json()` then throws on the login HTML
 * ("Unexpected token '<'"). Reading here keeps the admin key inside this
 * blessed server-only file (leak-guard invariant T-07-24 / T-18-03). Throws
 * GatewayError on a non-2xx response, surfacing the gateway `{error:{message}}`
 * envelope so the page can show the specific failure.
 */
export async function gatewayAdminGet<T>(path: string): Promise<T> {
  const base = process.env.GATEWAY_BASE_URL;
  const adminKey = process.env.GATEWAY_ADMIN_KEY;

  if (!base || !adminKey) {
    throw new GatewayError(
      500,
      "Gateway admin não configurado — defina GATEWAY_BASE_URL e GATEWAY_ADMIN_KEY.",
      "configuration_error",
    );
  }

  const target = `${base.replace(/\/$/, "")}/admin/${path}`;

  let res: Response;
  try {
    res = await fetch(target, {
      method: "GET",
      headers: { "X-Admin-Key": adminKey, Accept: "application/json" },
      cache: "no-store",
    });
  } catch {
    throw new GatewayError(
      502,
      "Não foi possível alcançar o gateway. Verifique se o gateway está no ar.",
      "upstream_unreachable",
    );
  }

  if (!res.ok) {
    let message = "Falha ao ler do gateway.";
    let type: string | null = null;
    try {
      const env = (await res.json()) as {
        error?: { message?: string; type?: string };
      };
      if (env.error?.message) message = env.error.message;
      if (env.error?.type) type = env.error.type;
    } catch {
      // Non-JSON / empty body — keep the generic fallback message.
    }
    throw new GatewayError(res.status, message, type);
  }

  return (await res.json()) as T;
}

/**
 * PATCH the gateway admin API (e.g. /admin/primary/config). Returns void — the
 * config PATCH responds 204. Preserves the Plan 17-05 contract.
 */
export async function gatewayAdminPatch(
  path: string,
  body: unknown,
): Promise<void> {
  await gatewayAdminMutate<void>("PATCH", path, body);
}

/**
 * POST the gateway admin API (e.g. /admin/tenants, /admin/tenants/{slug}/keys,
 * /admin/keys/{id}/revoke). Returns the parsed JSON body the gateway sends
 * verbatim (create-key `{id, key_prefix, data_class, key}`) — never
 * key_hash/key_lookup_hash, which Plan 18-01 omits.
 */
export async function gatewayAdminPost<T = void>(
  path: string,
  body: unknown,
): Promise<T> {
  return gatewayAdminMutate<T>("POST", path, body);
}

/**
 * PUT the gateway admin API (quick 260830-o2j: /admin/model-aliases,
 * /admin/tenants/{slug}/provider-prefs). Returns the gateway JSON verbatim.
 */
export async function gatewayAdminPut<T = void>(
  path: string,
  body: unknown,
): Promise<T> {
  return gatewayAdminMutate<T>("PUT", path, body);
}

/**
 * DELETE on the gateway admin API (quick 260830-o2j:
 * /admin/model-aliases/{alias}/{upstream}). 204 → resolves void.
 */
export async function gatewayAdminDelete(path: string): Promise<void> {
  await gatewayAdminMutate<void>("DELETE", path, undefined);
}
