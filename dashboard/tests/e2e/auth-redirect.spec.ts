/**
 * Playwright route-test gate (Task 11-02-05A) — asserts middleware
 * redirect behavior end-to-end against a running dashboard.
 *
 * Reviews HIGH #2 / Truth: cookie-claim contract. The middleware MUST
 * read twoFactorEnabled + twoFactorVerified from the session cookie
 * cache (NOT from a DB call in Edge runtime). These 4 HTTP-level
 * assertions PROVE the cookie wiring landed — if any case fails the
 * cookie-claim configuration in lib/auth.ts is wrong AND prod migration
 * MUST NOT proceed.
 *
 * Prerequisites (operator):
 *   - DASHBOARD_DATABASE_URL points at a STAGING schema (Task 11-02-06)
 *   - BETTER_AUTH_URL + BETTER_AUTH_SECRET set
 *   - `bun run dev` (or `bun run start`) is up on http://localhost:3001
 *   - The schema has been migrated via `bunx @better-auth/cli@latest migrate`
 *     against the STAGING DSN (NOT prod) first
 *
 * Run:
 *   cd dashboard && bunx playwright test tests/e2e/auth-redirect.spec.ts
 *
 * The 4 cases (per 11-02-PLAN.md acceptance):
 *   1. unauthenticated GET / → 307 /login?session_expired=1
 *   2. session WITHOUT twoFactorEnabled claim → 307 /2fa/enroll
 *   3. session WITH enabled but WITHOUT verified claim → 307 /2fa/challenge
 *   4. session WITH both claims → 200 (dashboard renders)
 *
 * Tests assert HTTP response codes + Location headers ONLY. No
 * `auth.options` introspection (reviews MEDIUM #5).
 *
 * Implementation notes:
 *   - Cases 2-4 use Better Auth's in-process API via a tiny server-side
 *     helper endpoint (the operator may need to mount a /api/test-helper
 *     route for the staging window; or skip cases 2-4 and assert only
 *     case 1 in a no-DB CI run — case 1 alone proves the
 *     session-expired path works without sign-up). The 4-case full run
 *     is required for the BLOCKING staging smoke (Task 11-02-06).
 */
import { expect, test } from "@playwright/test";

const BASE = process.env.DASHBOARD_BASE_URL ?? "http://localhost:3001";

test.describe("middleware redirect behavior (HIGH #2 contract)", () => {
  test("1. unauthenticated GET / → 307 /login?session_expired=1", async ({
    request,
  }) => {
    const res = await request.get(`${BASE}/`, { maxRedirects: 0 });
    expect(res.status()).toBe(307);
    const loc = res.headers()["location"] ?? "";
    expect(loc).toContain("/login");
    expect(loc).toContain("session_expired=1");
  });

  test.skip(
    !process.env.PLAYWRIGHT_RUN_AUTHENTICATED_CASES,
    "set PLAYWRIGHT_RUN_AUTHENTICATED_CASES=1 to enable cases 2-4 (requires live DB + test helper)",
  );

  test("2. session WITHOUT twoFactorEnabled → 307 /2fa/enroll", async ({
    request,
  }) => {
    // Operator runbook (Task 11-02-06 staging smoke step):
    //   a. Sign up + sign in a test admin via the dashboard UI (or via
    //      a temporary /api/test-helper endpoint that returns a session
    //      cookie). The fresh session has user.twoFactorEnabled = false.
    //   b. Capture the Set-Cookie response (header `better-auth.session_token`
    //      and `better-auth.session_data`).
    //   c. Set the cookie on `request` below, then GET /.
    const cookie = process.env.PLAYWRIGHT_COOKIE_NO_2FA;
    test.skip(!cookie, "PLAYWRIGHT_COOKIE_NO_2FA not set");
    const res = await request.get(`${BASE}/`, {
      maxRedirects: 0,
      headers: { cookie: cookie! },
    });
    expect(res.status()).toBe(307);
    expect(res.headers()["location"] ?? "").toContain("/2fa/enroll");
  });

  test("3. session with enabled=true, verified=false → 307 /2fa/challenge", async ({
    request,
  }) => {
    // Operator runbook:
    //   a. Use the test admin from case 2, complete /2fa/enroll, log out,
    //      log in again. The new session has user.twoFactorEnabled=true
    //      AND session.twoFactorVerified=false.
    //   b. Capture cookies + set PLAYWRIGHT_COOKIE_ENROLLED.
    const cookie = process.env.PLAYWRIGHT_COOKIE_ENROLLED;
    test.skip(!cookie, "PLAYWRIGHT_COOKIE_ENROLLED not set");
    const res = await request.get(`${BASE}/`, {
      maxRedirects: 0,
      headers: { cookie: cookie! },
    });
    expect(res.status()).toBe(307);
    expect(res.headers()["location"] ?? "").toContain("/2fa/challenge");
  });

  test("4. session fully verified → 200 (no redirect)", async ({ request }) => {
    // Operator runbook:
    //   a. From case 3, complete /2fa/challenge with the test admin's
    //      TOTP code (or backup code). The post-verify session has
    //      session.twoFactorVerified=true.
    //   b. Capture cookies + set PLAYWRIGHT_COOKIE_VERIFIED.
    const cookie = process.env.PLAYWRIGHT_COOKIE_VERIFIED;
    test.skip(!cookie, "PLAYWRIGHT_COOKIE_VERIFIED not set");
    const res = await request.get(`${BASE}/`, {
      maxRedirects: 0,
      headers: { cookie: cookie! },
    });
    expect(res.status()).toBe(200);
    expect(res.headers()["location"]).toBeUndefined();
  });
});
