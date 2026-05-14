/**
 * Server-side gateway proxy — /api/gateway/<anything> → ${GATEWAY_BASE_URL}/admin/<anything>.
 *
 * This is the ONLY file in the dashboard that reads GATEWAY_ADMIN_KEY. It runs
 * exclusively on the Next.js server (a route handler — never bundled into
 * client code), injects the `X-Admin-Key` header, forwards to the gateway's
 * admin API, and streams the JSON response back to the browser.
 *
 * Threat T-07-24 (admin-key disclosure): the browser only ever sees relative
 * `/api/gateway/*` paths. The admin key never crosses the browser boundary —
 * no NEXT_PUBLIC_ exposure, no client-component reference. An acceptance
 * criterion greps the whole src/ tree and asserts GATEWAY_ADMIN_KEY appears
 * in exactly this file.
 *
 * The dashboard is read-only (UI-SPEC) — only GET is proxied.
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
