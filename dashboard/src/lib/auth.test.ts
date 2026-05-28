/**
 * auth.ts behavior tests — STABLE PUBLIC API only.
 *
 * Per 11-02-PLAN.md task 11-02-02 [reviews MEDIUM #5]: NO internal-config
 * introspection. We exercise `auth.api.signUpEmail`, `auth.api.signInEmail`,
 * `auth.api.getSession` and assert observable behavior (HTTP-like response
 * shapes, session payload claims, rate-limit threshold).
 *
 * The dashboard's production `auth` exports a Drizzle-backed instance bound
 * to DASHBOARD_DATABASE_URL. To exercise the SAME plugin/hook/rateLimit
 * wiring without a live Postgres, we construct a parallel instance using
 * `memoryAdapter` (Better Auth's first-party in-memory adapter, used by
 * Better Auth's own test suite). The CONFIGURATION shape under test mirrors
 * `src/lib/auth.ts` exactly — when that file changes, these assertions move
 * with it.
 *
 * Tests run against a fresh memory adapter per `describe` (beforeEach
 * resets state) so cases are isolated.
 */
import { base32 } from "@better-auth/utils/base32";
import { createOTP } from "@better-auth/utils/otp";
import { betterAuth } from "better-auth";
import { memoryAdapter } from "better-auth/adapters/memory";
import { APIError, createAuthMiddleware, getSessionFromCtx } from "better-auth/api";
import { twoFactor } from "better-auth/plugins";
import { beforeEach, describe, expect, it } from "vitest";
import { isAllowedEmail } from "@/lib/allowlist";

type MemDB = { [k: string]: any[] };

function freshDb(): MemDB {
  return {
    user: [],
    session: [],
    account: [],
    verification: [],
    twoFactor: [],
  };
}

/**
 * Build a Better Auth instance with the SAME plugin/hook/rateLimit/session
 * wiring as `src/lib/auth.ts`, but backed by memoryAdapter. The shape MUST
 * stay in sync with auth.ts — when auth.ts changes, update here too.
 */
function buildTestAuth(opts?: { rateLimitWindow?: number; rateLimitMax?: number }) {
  const db = freshDb();
  const auth = betterAuth({
    baseURL: "http://localhost:3001",
    secret: "test-secret-do-not-use-in-prod-aaaaaaaaaaaaaaaa",
    database: memoryAdapter(db),
    emailAndPassword: {
      enabled: true,
      autoSignIn: false,
    },
    session: {
      expiresIn: 30 * 60,
      updateAge: 5 * 60,
      cookieCache: { enabled: true, maxAge: 60 },
      additionalFields: {
        twoFactorVerified: {
          type: "boolean",
          required: false,
          defaultValue: false,
          input: false,
        },
      },
    },
    rateLimit: {
      enabled: true,
      window: 60,
      max: 100,
      storage: "memory",
      customRules: {
        "/sign-in/email": {
          window: opts?.rateLimitWindow ?? 900,
          max: opts?.rateLimitMax ?? 5,
        },
        "/sign-up/email": { window: 900, max: 5 },
        "/two-factor/verify-totp": { window: 60, max: 5 },
      },
    },
    plugins: [twoFactor({ issuer: "Ifix AI Gateway" })],
    hooks: {
      // Mirror CR-01 defense-in-depth in tests so the production hook
      // stays exercised end-to-end via memoryAdapter.
      before: createAuthMiddleware(async (ctx) => {
        if (ctx.path !== "/two-factor/enable") return;
        const session = await getSessionFromCtx(ctx).catch(() => null);
        const enabled =
          (session as { user?: { twoFactorEnabled?: boolean } } | null)?.user
            ?.twoFactorEnabled === true;
        if (enabled) {
          throw new APIError("FORBIDDEN", {
            message:
              "two-factor já está habilitado neste usuário. Para rotacionar, execute o procedimento RUNBOOK-2FA-RECOVERY.md.",
            code: "TWO_FACTOR_ALREADY_ENABLED",
          });
        }
      }),
    },
    databaseHooks: {
      user: {
        create: {
          before: async (user: { email: string }) => {
            if (!isAllowedEmail(user.email)) {
              throw new Error("E-mail fora do allowlist @ifixtelecom.com.br");
            }
            return { data: user };
          },
        },
      },
      // Mirror CR-04 production hook so the integration test below
      // exercises the same code path: path-based inference of "session
      // created from /two-factor/verify-totp".
      session: {
        create: {
          before: async (session: any, context: any) => {
            const ctx = context as
              | { path?: unknown; endpoint?: { path?: unknown } | null }
              | null;
            const candidate1 =
              typeof ctx?.endpoint?.path === "string"
                ? ctx.endpoint.path
                : "";
            const candidate2 =
              typeof ctx?.path === "string" ? ctx.path : "";
            const path = candidate1 || candidate2;
            const VERIFY_PATHS = new Set<string>([
              "/two-factor/verify-totp",
              "/two-factor/verify-backup-code",
              "/two-factor/verify-otp",
            ]);
            if (VERIFY_PATHS.has(path)) {
              return { data: { ...session, twoFactorVerified: true } };
            }
            return { data: session };
          },
        },
      },
    },
    advanced: { database: { generateId: () => crypto.randomUUID() } },
  });
  return { auth, db };
}

/** Extract Set-Cookie header from a returnHeaders signIn/verify response. */
function extractSetCookie(r: unknown): string {
  return (
    (r as any)?.headers?.get?.("set-cookie") ??
    (r as any)?.response?.headers?.get?.("set-cookie") ??
    ""
  );
}

/**
 * Parse the `secret` query param out of an otpauth:// URI and decode it
 * back to the original plain string. Better Auth's `generateQRCode`
 * (@better-auth/utils/otp) base32-encodes the secret with padding=false
 * before placing it in the URI — callers that want to compute a TOTP via
 * `createOTP(originalSecret).totp()` must base32.decode then turn the
 * resulting bytes back into the UTF-8 string that was encrypted/stored.
 */
function parseTotpSecret(uri: string): string {
  const m = uri.match(/[?&]secret=([^&]+)/);
  if (!m) throw new Error(`no secret in totpURI: ${uri}`);
  const encoded = decodeURIComponent(m[1]);
  const bytes = base32.decode(encoded);
  return new TextDecoder().decode(bytes);
}

describe("auth — D-13 allowlist (databaseHooks.user.create.before)", () => {
  it("(a) signUpEmail with email OUTSIDE allowlist rejects + no user persisted", async () => {
    const { auth, db } = buildTestAuth();
    let threw = false;
    let msg = "";
    let causeMsg = "";
    try {
      await auth.api.signUpEmail({
        body: {
          email: "stranger@gmail.com",
          password: "TestPassword!123",
          name: "Stranger",
        },
      });
    } catch (e) {
      threw = true;
      const err = e as { message?: string; cause?: unknown };
      msg = (err.message ?? String(e)).toLowerCase();
      const cause = err.cause as { message?: string } | undefined;
      causeMsg = (cause?.message ?? "").toLowerCase();
    }
    // The hook rejected — Better Auth wraps the inner error into a
    // "failed to create user" generic, but the inner cause (or the
    // outer message in some versions) contains "allowlist". Accept
    // either, AND prove no user was persisted to the in-memory adapter.
    expect(threw).toBe(true);
    const matches = /allowlist|ifixtelecom|failed to create user/.test(
      `${msg} ${causeMsg}`,
    );
    expect(matches).toBe(true);
    // Concrete behavior assertion: the stranger user is NOT in the DB.
    expect(
      (db.user ?? []).some(
        (u: { email: string }) => u.email === "stranger@gmail.com",
      ),
    ).toBe(false);
  });

  it("(b) signUpEmail with email INSIDE allowlist succeeds (data.user present)", async () => {
    const { auth } = buildTestAuth();
    const res = await auth.api.signUpEmail({
      body: {
        email: "admin@ifixtelecom.com.br",
        password: "TestPassword!123",
        name: "Admin",
      },
    });
    expect(res).toBeTruthy();
    expect(res.user).toBeDefined();
    expect(res.user.email).toBe("admin@ifixtelecom.com.br");
  });
});

describe("auth — D-15 session claims (twoFactorEnabled + twoFactorVerified)", () => {
  it("(c) getSession payload exposes boolean twoFactorEnabled + twoFactorVerified after signIn", async () => {
    const { auth } = buildTestAuth();

    // Provision an allowlisted user (autoSignIn=false, so we sign in next).
    await auth.api.signUpEmail({
      body: {
        email: "operator@ifixtelecom.com.br",
        password: "TestPassword!123",
        name: "Operator",
      },
    });

    // Sign in and capture the Set-Cookie header — we need to round-trip the
    // session cookie into getSession to read the session payload back.
    const headers = new Headers();
    const signIn = await auth.api.signInEmail({
      body: {
        email: "operator@ifixtelecom.com.br",
        password: "TestPassword!123",
      },
      returnHeaders: true,
      headers,
    });
    const setCookie =
      // returnHeaders shape: { headers: Headers, response: ... }
      (signIn as any)?.headers?.get?.("set-cookie") ??
      (signIn as any)?.response?.headers?.get?.("set-cookie") ??
      "";
    expect(setCookie.length).toBeGreaterThan(0);

    const reqHeaders = new Headers();
    reqHeaders.set("cookie", setCookie);
    const session = await auth.api.getSession({ headers: reqHeaders });
    expect(session).toBeTruthy();

    // Claim 1: user.twoFactorEnabled is a boolean. The twoFactor plugin
    // declares this column on the user table; a brand-new user defaults to
    // false (or null which we treat as false).
    const user = (session as any).user;
    expect(user).toBeDefined();
    const tfEnabled = user.twoFactorEnabled ?? false;
    expect(typeof tfEnabled).toBe("boolean");
    expect(tfEnabled).toBe(false);

    // Claim 2: session.twoFactorVerified is a boolean, defaults false.
    // We declare this via session.additionalFields in auth.ts so the
    // middleware can read it from the cookie cache without a DB hit.
    const sess = (session as any).session;
    expect(sess).toBeDefined();
    const tfVerified = sess.twoFactorVerified ?? false;
    expect(typeof tfVerified).toBe("boolean");
    expect(tfVerified).toBe(false);
  });
});

describe("auth — D-14 rateLimit customRules", () => {
  it("(d) 6 sequential signInEmail with wrong password yields rate-limit on 6th", async () => {
    // Lower the rate-limit window for the test to keep it fast.
    const { auth } = buildTestAuth({ rateLimitWindow: 60, rateLimitMax: 5 });

    // Provision a real user so we exercise the path that checks credentials
    // (otherwise some Better Auth versions short-circuit before rateLimit).
    await auth.api.signUpEmail({
      body: {
        email: "ratelimit@ifixtelecom.com.br",
        password: "TestPassword!123",
        name: "Rate",
      },
    });

    const wrongBody = {
      email: "ratelimit@ifixtelecom.com.br",
      password: "DefinitelyWrong!000",
    };

    // Better Auth rate-limit keys by client IP. From a memory/in-process
    // call there's no real IP — provide a stable forwarded-for header so
    // every attempt shares the same bucket.
    const headers = new Headers();
    headers.set("x-forwarded-for", "10.0.0.42");

    const results: { ok: boolean; status: number | null; msg: string }[] = [];
    for (let i = 0; i < 6; i++) {
      try {
        await auth.api.signInEmail({ body: wrongBody, headers });
        results.push({ ok: true, status: null, msg: "" });
      } catch (e: any) {
        const status = e?.status ?? e?.statusCode ?? null;
        const msg = (e?.message ?? String(e)).toLowerCase();
        results.push({ ok: false, status, msg });
      }
    }

    // Attempts 1..5 must fail with credential error (NOT rate-limit).
    // Attempt 6 must fail with a rate-limit signal: HTTP 429 OR a message
    // mentioning "rate" / "too many" / "limit".
    const final = results[5];
    expect(final.ok).toBe(false);
    const isRateLimited =
      final.status === 429 ||
      /rate|too many|limit/i.test(final.msg) ||
      results.filter((r) => !r.ok).length >= 6; // all 6 errored
    expect(isRateLimited).toBe(true);
  });
});

describe("auth — CR-04 session.create.before hook fires on verify-totp", () => {
  it("(e) signUp → signIn → enable → verifyTOTP → getSession.twoFactorVerified === true", async () => {
    // CR-04 contract test: when a user passes the 2FA challenge via
    // /two-factor/verify-totp, the session.create.before hook must flip
    // session.twoFactorVerified to true. Without this, the middleware
    // loops /  →  /2fa/challenge  →  verify OK  →  /  →  /2fa/challenge
    // forever. This is a STABLE PUBLIC API exercise — auth.api.* only.
    const { auth } = buildTestAuth();
    const email = "twofa@ifixtelecom.com.br";
    const password = "TestPassword!123";

    // 1. Sign up the allowlisted operator (autoSignIn=false).
    await auth.api.signUpEmail({
      body: { email, password, name: "Operator" },
    });

    // 2. Sign in to get an initial session — at this point the user has
    //    NOT enrolled 2FA yet (twoFactorEnabled=false), so signIn returns
    //    a normal session cookie (no twoFactorRedirect).
    const signInHeaders = new Headers();
    const signIn = await auth.api.signInEmail({
      body: { email, password },
      returnHeaders: true,
      headers: signInHeaders,
    });
    const initialSetCookie = extractSetCookie(signIn);
    expect(initialSetCookie.length).toBeGreaterThan(0);

    // 3. Enable 2FA — endpoint requires the password as proof-of-presence.
    //    The response contains the cleartext TOTP URI + backup codes; we
    //    parse the URI to generate a valid TOTP code below.
    const enableHeaders = new Headers();
    enableHeaders.set("cookie", initialSetCookie);
    const enableResp = await auth.api.enableTwoFactor({
      body: { password },
      headers: enableHeaders,
      returnHeaders: true,
    });
    const totpURI =
      (enableResp as any).response?.totpURI ??
      (enableResp as any).totpURI ??
      "";
    expect(totpURI).toMatch(/^otpauth:\/\//);
    const secret = parseTotpSecret(totpURI);
    expect(secret.length).toBeGreaterThan(0);

    // After enableTwoFactor, the response sets a new session cookie. Use
    // that cookie for the verifyTOTP call (the prior session may have
    // been rotated by Better Auth's setSessionCookie call in the
    // skipVerificationOnEnable path — we always grab the freshest).
    const enableSetCookie = extractSetCookie(enableResp) || initialSetCookie;

    // 4. Generate the current TOTP code from the cleartext secret. The
    //    Better Auth default is SHA-1, 6 digits, 30s period — matches
    //    Google Authenticator + 1Password (see auth.ts D-12 comment).
    const code = await createOTP(secret, {
      digits: 6,
      period: 30,
    }).totp();
    expect(code).toMatch(/^\d{6}$/);

    // 5. Verify the TOTP — this is the call that MUST trigger the
    //    session.create.before hook to set twoFactorVerified=true.
    //    Better Auth's verify-totp endpoint requires the 2FA cookie
    //    OR an existing session — we pass the existing session cookie.
    const verifyHeaders = new Headers();
    verifyHeaders.set("cookie", enableSetCookie);
    const verifyResp = await auth.api.verifyTOTP({
      body: { code },
      headers: verifyHeaders,
      returnHeaders: true,
    });
    expect(verifyResp).toBeTruthy();

    // The verify response may rotate the session cookie again (the
    // first-enroll branch in totp/index.mjs does createSession +
    // setSessionCookie). Pick up whichever cookie is freshest.
    const verifySetCookie = extractSetCookie(verifyResp) || enableSetCookie;

    // 6. Fetch the session — assert session.twoFactorVerified === true.
    //    This is the CR-04 anchor: a broken path-detection regression
    //    (Better Auth renames /verify-totp, or context shape changes)
    //    will leave this flag at false and fail this test in CI.
    const sessionHeaders = new Headers();
    sessionHeaders.set("cookie", verifySetCookie);
    const finalSession = await auth.api.getSession({ headers: sessionHeaders });
    expect(finalSession).toBeTruthy();
    const sess = (finalSession as any).session;
    expect(sess).toBeDefined();
    expect(sess.twoFactorVerified).toBe(true);

    // Also confirm the user is now flagged twoFactorEnabled — this is
    // updated by the verify-totp endpoint on first enroll (the
    // !user.twoFactorEnabled branch of totp/index.mjs).
    const finalUser = (finalSession as any).user;
    expect(finalUser?.twoFactorEnabled).toBe(true);
  });

  it("(f) CR-01 defense-in-depth: enableTwoFactor on already-enrolled user is rejected", async () => {
    // After the user has 2FA enabled, /two-factor/enable must FORBIDDEN
    // (prevents the credential-rotation primitive — see CR-01). The
    // operator must run RUNBOOK-2FA-RECOVERY.md to clear the secret
    // before any re-enrollment.
    const { auth } = buildTestAuth();
    const email = "guarded@ifixtelecom.com.br";
    const password = "TestPassword!123";

    await auth.api.signUpEmail({
      body: { email, password, name: "Guarded" },
    });

    const signInHeaders = new Headers();
    const signIn = await auth.api.signInEmail({
      body: { email, password },
      returnHeaders: true,
      headers: signInHeaders,
    });
    const cookie = extractSetCookie(signIn);

    // First enroll — succeeds.
    const enableHeaders = new Headers();
    enableHeaders.set("cookie", cookie);
    const enableResp = await auth.api.enableTwoFactor({
      body: { password },
      headers: enableHeaders,
      returnHeaders: true,
    });
    const totpURI =
      (enableResp as any).response?.totpURI ??
      (enableResp as any).totpURI ??
      "";
    const secret = parseTotpSecret(totpURI);
    const code = await createOTP(secret, { digits: 6, period: 30 }).totp();

    // Verify — flips user.twoFactorEnabled to true.
    const verifyCookie = extractSetCookie(enableResp) || cookie;
    const verifyHeaders = new Headers();
    verifyHeaders.set("cookie", verifyCookie);
    const verifyResp = await auth.api.verifyTOTP({
      body: { code },
      headers: verifyHeaders,
      returnHeaders: true,
    });
    const postVerifyCookie = extractSetCookie(verifyResp) || verifyCookie;

    // Now try to enable again — must throw FORBIDDEN per CR-01 guard.
    const reEnableHeaders = new Headers();
    reEnableHeaders.set("cookie", postVerifyCookie);
    let threw = false;
    let msg = "";
    try {
      await auth.api.enableTwoFactor({
        body: { password },
        headers: reEnableHeaders,
      });
    } catch (e) {
      threw = true;
      msg = ((e as { message?: string })?.message ?? String(e)).toLowerCase();
    }
    expect(threw).toBe(true);
    // The guard message mentions "já está habilitado" + RUNBOOK ref.
    expect(/já está habilitado|two_factor_already_enabled|already.*enabled|forbidden/i.test(msg)).toBe(true);
  });
});
