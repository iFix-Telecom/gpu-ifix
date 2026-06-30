/**
 * Server-side gateway proxy — /api/gateway/<anything> → ${GATEWAY_BASE_URL}/admin/<anything>.
 *
 * This is ONE of exactly TWO server-only files in the dashboard that read
 * GATEWAY_ADMIN_KEY — this GET-only proxy, and `src/lib/gateway-admin.ts` (the
 * server-only write helper the owner write actions use, Plan 17-05). Both run
 * exclusively on the Next.js server (this is a route handler — never bundled
 * into client code), inject the `X-Admin-Key` header, forward to the gateway's
 * admin API, and stream the JSON response back to the browser.
 *
 * Threat T-07-24 (admin-key disclosure): the browser only ever sees relative
 * `/api/gateway/*` paths. The admin key never crosses the browser boundary —
 * no NEXT_PUBLIC_ exposure, no client-component reference. An acceptance test
 * (gateway.test.ts "admin-key leak guard") greps the whole src/ tree and
 * asserts GATEWAY_ADMIN_KEY appears in EXACTLY these two server-only files.
 *
 * This proxy stays GET-only (D-07) — the write path goes through the dedicated
 * server-only gateway-admin.ts helper, NOT this proxy.
 */
import { type NextRequest, NextResponse } from "next/server";

// Always run on the server at request time — never statically optimized,
// never edge (we need process.env + Node fetch streaming).
export const runtime = "nodejs";
export const dynamic = "force-dynamic";

async function proxy(req: NextRequest, segments: string[]) {
  const base = process.env.GATEWAY_BASE_URL;
  const adminKey = process.env.GATEWAY_ADMIN_KEY;

  if (!base || !adminKey) {
    return NextResponse.json(
      {
        error: {
          message:
            "Gateway proxy não configurado — defina GATEWAY_BASE_URL e GATEWAY_ADMIN_KEY.",
          type: "configuration_error",
        },
      },
      { status: 500 },
    );
  }

  // Reconstruct the downstream path under /admin/*, preserving query string.
  const path = segments.map(encodeURIComponent).join("/");
  const search = req.nextUrl.search;
  const target = `${base.replace(/\/$/, "")}/admin/${path}${search}`;

  let upstream: Response;
  try {
    upstream = await fetch(target, {
      method: "GET",
      headers: { "X-Admin-Key": adminKey, Accept: "application/json" },
      // Operator dashboard polls every 5–10s — never serve a cached body.
      cache: "no-store",
    });
  } catch {
    return NextResponse.json(
      {
        error: {
          message:
            "Não foi possível alcançar o gateway. Verifique se o gateway está no ar.",
          type: "upstream_unreachable",
        },
      },
      { status: 502 },
    );
  }

  const body = await upstream.text();
  return new NextResponse(body, {
    status: upstream.status,
    headers: {
      "Content-Type":
        upstream.headers.get("Content-Type") ?? "application/json",
    },
  });
}

export async function GET(
  req: NextRequest,
  ctx: { params: Promise<{ path: string[] }> },
) {
  const { path } = await ctx.params;
  return proxy(req, path ?? []);
}
