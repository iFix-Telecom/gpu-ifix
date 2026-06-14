/**
 * Edge middleware tests — proves the two-stage 2FA gate (D-12 + D-15)
 * routes correctly based on session-cookie claims AND that no
 * redirect-loop is possible on /2fa/challenge.
 *
 * Better Auth's `getSessionCookie` + `getCookieCache` are mocked here
 * — the unit under test is the middleware DECISION TREE, not Better
 * Auth's cookie decode. The cookie wiring itself is exercised
 * end-to-end by the Playwright route test (Task 11-02-05A).
 *
 * 6 cases per 11-02-PLAN.md Task 11-02-03 acceptance + CR-01:
 *   a. cookie absent → /login (clean, no param)
 *   b. session present, twoFactorEnabled=false → /2fa/enroll
 *   c. session present, twoFactorEnabled=true, twoFactorVerified=false → /2fa/challenge
 *   d. session present, both true → next()
 *   e. loop-guard: /2fa/challenge with unverified state → handled by
 *      matcher exclusion (this case proves the matcher config is right)
 *   f. CR-01: session cookie present but cookieCache stale/null → /2fa/challenge
 *      (NOT /2fa/enroll — that would let an attacker overwrite the user's
 *      real TOTP secret + backup codes via authClient.twoFactor.enable).
 */
import { NextRequest } from "next/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("better-auth/cookies", () => ({
  getSessionCookie: vi.fn(),
  getCookieCache: vi.fn(),
}));

// Import AFTER mock declaration so the middleware picks up the mock.
import { getCookieCache, getSessionCookie } from "better-auth/cookies";
import { config, middleware } from "@/middleware";

const mockGetSessionCookie = vi.mocked(getSessionCookie);
const mockGetCookieCache = vi.mocked(getCookieCache);

function makeReq(pathname: string = "/"): NextRequest {
  return new NextRequest(new URL(pathname, "http://localhost:3001"));
}

beforeEach(() => {
  mockGetSessionCookie.mockReset();
  mockGetCookieCache.mockReset();
});

describe("middleware — two-stage 2FA gate (D-12 + D-15)", () => {
  it("(a) cookie absent → /login (no session_expired param)", async () => {
    mockGetSessionCookie.mockReturnValueOnce(null);
    const res = await middleware(makeReq("/"));
    expect(res.status).toBe(307);
    const loc = res.headers.get("location") ?? "";
    expect(loc).toContain("/login");
    expect(loc).not.toContain("session_expired");
  });

  it("(b) session present, twoFactorEnabled=false → /2fa/enroll", async () => {
    mockGetSessionCookie.mockReturnValueOnce("session-token-value");
    mockGetCookieCache.mockResolvedValueOnce({
      session: { twoFactorVerified: false },
      user: { twoFactorEnabled: false },
    } as any);
    const res = await middleware(makeReq("/"));
    expect(res.status).toBe(307);
    expect(res.headers.get("location") ?? "").toContain("/2fa/enroll");
  });

  it("(c) session, twoFactorEnabled=true, twoFactorVerified=false → /2fa/challenge", async () => {
    mockGetSessionCookie.mockReturnValueOnce("session-token-value");
    mockGetCookieCache.mockResolvedValueOnce({
      session: { twoFactorVerified: false },
      user: { twoFactorEnabled: true },
    } as any);
    const res = await middleware(makeReq("/"));
    expect(res.status).toBe(307);
    expect(res.headers.get("location") ?? "").toContain("/2fa/challenge");
  });

  it("(d) session with both claims true → next() (no redirect)", async () => {
    mockGetSessionCookie.mockReturnValueOnce("session-token-value");
    mockGetCookieCache.mockResolvedValueOnce({
      session: { twoFactorVerified: true },
      user: { twoFactorEnabled: true },
    } as any);
    const res = await middleware(makeReq("/"));
    // NextResponse.next() returns 200 with no location header.
    expect(res.status).toBe(200);
    expect(res.headers.get("location")).toBeNull();
  });

  it("(f) CR-01: cookie present but cookieCache stale/null → /2fa/challenge (NOT /2fa/enroll)", async () => {
    // Cache miss is not theoretical — it happens on every Better Auth secret
    // rotation, every Redis flush, every cookieCache.maxAge boundary, and
    // the very first request of a fresh sign-in. CR-01: routing to
    // /2fa/enroll in this branch exposes a credential-rotation primitive
    // because authClient.twoFactor.enable issues-and-replaces the TOTP
    // secret. Stale cache MUST route to /2fa/challenge.
    mockGetSessionCookie.mockReturnValueOnce("session-token-value");
    mockGetCookieCache.mockResolvedValueOnce(null as any);
    const res = await middleware(makeReq("/"));
    expect(res.status).toBe(307);
    const loc = res.headers.get("location") ?? "";
    expect(loc).toContain("/2fa/challenge");
    expect(loc).not.toContain("/2fa/enroll");
  });

  it("(f.2) CR-01: cookie present, cookieCache missing user → /2fa/challenge", async () => {
    mockGetSessionCookie.mockReturnValueOnce("session-token-value");
    mockGetCookieCache.mockResolvedValueOnce({
      session: { twoFactorVerified: false },
      // user absent → treat as stale per CR-01
    } as any);
    const res = await middleware(makeReq("/"));
    expect(res.status).toBe(307);
    const loc = res.headers.get("location") ?? "";
    expect(loc).toContain("/2fa/challenge");
    expect(loc).not.toContain("/2fa/enroll");
  });

  it("(e) loop-guard: matcher excludes /2fa/challenge so middleware never runs against it", () => {
    // The matcher MUST exclude `2fa`, `first-login`, `signed-out`,
    // `login`, `signup`, `api/auth`, `_next`, `favicon`. If any of those
    // were inside the matcher, an unverified session on /2fa/challenge
    // would redirect back to /2fa/challenge → infinite loop.
    expect(config.matcher).toHaveLength(1);
    const pattern = config.matcher[0];
    for (const excluded of [
      "login",
      "signup",
      "2fa",
      "first-login",
      "signed-out",
      "api/auth",
      "_next",
      "favicon",
    ]) {
      expect(pattern).toContain(excluded);
    }
    // Sanity: the pattern uses a negative lookahead so all excluded
    // segments live INSIDE the `(?!...)` group, NOT after it.
    expect(pattern).toMatch(/\(\?!/);
  });
});
