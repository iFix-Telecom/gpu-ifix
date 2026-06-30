import "server-only";

/**
 * Server-only gateway admin WRITE helper (Plan 17-05).
 *
 * This is the SECOND (and last) place in the dashboard that reads
 * GATEWAY_ADMIN_KEY — the GET-only proxy (app/api/gateway/[...path]/route.ts)
 * is the first. The `import "server-only"` marker makes any accidental import
 * into a client bundle a BUILD error, so the admin key can never cross the
 * browser boundary (threat T-07-24). The owner write server actions
 * (admin-actions.ts updatePodConfig / updatePodConfigBound) call
 * `gatewayAdminPatch` AFTER `requireOwner` + server-side bounds validation.
 *
 * Unlike the read proxy (GET, D-07), this helper issues the ONE mutation verb
 * the gateway exposes: PATCH /admin/primary/config. It does NOT go through the
 * read-only `/api/gateway/*` proxy — it talks to the gateway admin API directly
 * with the `X-Admin-Key` header, server-side only.
 */

/**
 * PATCH the gateway admin API at `${GATEWAY_BASE_URL}/admin/<path>` with the
 * `X-Admin-Key` header and a JSON body. Throws on a non-2xx response, surfacing
 * the gateway's `{error:{message}}` envelope when present so the calling action
 * can report the specific validation failure. The admin key is read from
 * `process.env` here and NEVER returned to the caller.
 */
export async function gatewayAdminPatch(
  path: string,
  body: unknown,
): Promise<void> {
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
      method: "PATCH",
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
    let message = "Falha ao gravar a configuração no gateway.";
    try {
      const env = (await res.json()) as { error?: { message?: string } };
      if (env.error?.message) message = env.error.message;
    } catch {
      // Non-JSON / empty body — keep the generic fallback message.
    }
    throw new Error(message);
  }
}
