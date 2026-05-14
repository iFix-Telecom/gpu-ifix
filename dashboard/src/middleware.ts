/**
 * Auth boundary — every dashboard route except `/login` and `/api/auth/*` is
 * gated. An unauthenticated request (no Better Auth session cookie) is
 * redirected to `/login`.
 *
 * Source: 07-RESEARCH.md verbatim example (lines 494-504). This is the
 * mitigation for threat T-07-25 (unauthenticated dashboard access).
 *
 * `getSessionCookie` only checks for the cookie's presence — it does NOT
 * validate the session against the DB (that would need a Node runtime). Full
 * validation happens in route handlers / server components; the middleware is
 * the cheap first gate that keeps anonymous traffic off every page.
 */
import { getSessionCookie } from "better-auth/cookies";
import { type NextRequest, NextResponse } from "next/server";

export function middleware(req: NextRequest) {
  const session = getSessionCookie(req);

  if (!session && !req.nextUrl.pathname.startsWith("/login")) {
    return NextResponse.redirect(new URL("/login", req.url));
  }

  return NextResponse.next();
}

export const config = {
  matcher: ["/((?!login|api/auth|_next|favicon).*)"],
};
