/**
 * Auth boundary — every dashboard route except /login, /signup, /2fa/*,
 * /first-login, /signed-out, /api/auth/* is gated. Phase 11 (PRD-06)
 * extends the original session-presence check with a two-stage TOTP gate
 * (D-12 + D-15) per 11-RESEARCH §Pitfall 4.
 *
 * Decision tree (reviews HIGH #2 — cookie-claim contract):
 *   1. No session cookie → redirect to /login
 *   2. Session present but `user.twoFactorEnabled !== true` → /2fa/enroll
 *   3. Session present, twoFactorEnabled=true, `session.twoFactorVerified !== true` → /2fa/challenge
 *   4. Both claims present → next()
 *
 * Claims are read from the session COOKIE CACHE (Better Auth's
 * `getCookieCache`, configured in `lib/auth.ts` with `cookieCache.enabled`
 * + `session.additionalFields` exposing `twoFactorVerified` AND the
 * twoFactor plugin contributing `user.twoFactorEnabled`). The Edge
 * runtime MUST NOT make a DB call — this is the contract Task 11-02-02
 * wires.
 *
 * If `getCookieCache` returns null (cache stale or absent — e.g. just
 * after sign-in before the first cookieCache write), we PESSIMISTICALLY
 * treat as session-present, 2FA-enrolled, but NOT verified — routing to
 * /2fa/challenge (NOT /2fa/enroll). See CR-01: routing to /2fa/enroll
 * exposes a credential-rotation primitive because
 * `authClient.twoFactor.enable` will overwrite a real TOTP secret +
 * backup codes for an already-enrolled user. A user who is genuinely
 * unenrolled who lands on /2fa/challenge will see a clear "two-factor
 * not enabled" error from the verify endpoint — a safe failure mode.
 * This is loop-safe: the challenge page itself is excluded by
 * `config.matcher` below.
 *
 * Matcher exclusions (UI-SPEC v2 §Anchors): login, signup, 2fa, first-login,
 * signed-out, api/auth, _next, favicon.
 */
import { getCookieCache, getSessionCookie } from "better-auth/cookies";
import { type NextRequest, NextResponse } from "next/server";

/**
 * Read twoFactor claims from the Better Auth session cookie cache.
 * Returns:
 *   - { hasSession: false } when no session cookie present.
 *   - { hasSession: true, twoFactorEnabled, twoFactorVerified } when the
 *     cookieCache payload was successfully decoded.
 *   - { hasSession: true, twoFactorEnabled: true, twoFactorVerified: false }
 *     when the session cookie exists but cookieCache is missing/stale —
 *     PESSIMISTIC fallback per CR-01: pretend enrolled to force /2fa/challenge
 *     rather than /2fa/enroll (which would overwrite TOTP on the real user).
 */
async function readTwoFactorClaims(req: NextRequest): Promise<{
  hasSession: boolean;
  twoFactorEnabled: boolean;
  twoFactorVerified: boolean;
}> {
  const sessionCookie = getSessionCookie(req);
  if (!sessionCookie) {
    return {
      hasSession: false,
      twoFactorEnabled: false,
      twoFactorVerified: false,
    };
  }

  const cache = await getCookieCache(req);
  if (!cache || !cache.session || !cache.user) {
    // Cookie cache stale/absent (post-redeploy, post-secret-rotation, or the
    // 60s cookieCache miss window) — we cannot consult the DB from Edge, so
    // we PESSIMISTICALLY treat the user as enrolled-but-unverified. This
    // routes to /2fa/challenge (NOT /2fa/enroll). See CR-01: routing to
    // /2fa/enroll would let an attacker with a stolen session cookie reset
    // the legitimate user's TOTP secret + backup codes via
    // authClient.twoFactor.enable. A user who is genuinely not enrolled and
    // hits /2fa/challenge will receive a clear "two-factor not enabled"
    // error from the verify endpoint — safe failure mode.
    return {
      hasSession: true,
      twoFactorEnabled: true,
      twoFactorVerified: false,
    };
  }

  const user = cache.user as { twoFactorEnabled?: boolean };
  const session = (cache.session as { twoFactorVerified?: boolean }) ?? {};
  return {
    hasSession: true,
    twoFactorEnabled: user.twoFactorEnabled === true,
    twoFactorVerified: session.twoFactorVerified === true,
  };
}

export async function middleware(req: NextRequest) {
  const claims = await readTwoFactorClaims(req);

  // Stage 1: no session → clean /login. The Edge runtime cannot tell
  // "never logged in" apart from "session genuinely expired" (both are
  // simply the absence of a session cookie), so we MUST NOT append
  // ?session_expired=1 here — doing so falsely shows the "Sessão encerrada
  // por inatividade" banner on every first visit.
  if (!claims.hasSession) {
    return NextResponse.redirect(new URL("/login", req.url));
  }

  // Stage 2a: session present, 2FA not enrolled → /2fa/enroll.
  if (!claims.twoFactorEnabled) {
    return NextResponse.redirect(new URL("/2fa/enroll", req.url));
  }

  // Stage 2b: session present, enrolled but this session hasn't verified
  // TOTP yet → /2fa/challenge.
  if (!claims.twoFactorVerified) {
    return NextResponse.redirect(new URL("/2fa/challenge", req.url));
  }

  return NextResponse.next();
}

// Matcher excludes the auth-flow routes so the middleware does NOT
// redirect-loop on /2fa, /first-login, /signed-out, /login, /signup, or
// the Better Auth API itself.
export const config = {
  matcher: [
    "/((?!login|signup|2fa|first-login|signed-out|api/auth|_next|favicon).*)",
  ],
};
