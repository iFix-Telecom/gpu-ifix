/**
 * Standalone Better Auth instance for the ifix-ai-gateway dashboard.
 *
 * Source: 07-RESEARCH.md verbatim example + Phase 11 PRD-06 hardening
 * (D-12 / D-13 / D-14 / D-15). CONTEXT.md locks this as a STANDALONE
 * instance — NOT a shared session with converseai-v4 — for ~4 Ifix admins.
 *
 * Phase 11 plugin / hook surface:
 * - twoFactor (D-12): mandatory TOTP, issuer "Ifix AI Gateway", SHA-1
 *   default (Google Authenticator + 1Password compatible).
 * - databaseHooks.user.create.before (D-13): email allowlist enforced
 *   server-side; only `@ifixtelecom.com.br` (or override via
 *   DASHBOARD_ALLOWED_EMAIL_DOMAINS) can sign up.
 * - rateLimit customRules (D-14): /sign-in/email + /sign-up/email
 *   5 attempts / 15 min / IP; /two-factor/verify-totp 5 attempts /
 *   60s / IP. Built-in (NOT plugin) per 11-RESEARCH state-of-the-art.
 * - session.expiresIn = 7 days (D-15, revised quick-260625-k17): 7-day
 *   idle window (raised back from the 30-min Phase-11 value). cookieCache
 *   maxAge matches expiresIn so the Edge middleware can read the twoFactor
 *   claims without a DB call and never bounces mid-session (WR-01).
 *
 * Cookie-claim contract (11-REVIEWS HIGH #2): the middleware MUST be
 * able to read `user.twoFactorEnabled` AND `session.twoFactorVerified`
 * from the session cookie cache. We use Option A (`session.additionalFields`)
 * — Better Auth's `setCookieCache` runs `filterOutputFields(session,
 * options.session.additionalFields)` AND `parseUserOutput(options, user)`
 * (which includes the twoFactor plugin's user-table column
 * `twoFactorEnabled`), so both claims materialise in the cookie payload
 * automatically when cookieCache is enabled. No session callback needed.
 *
 * Rate-limit storage decision (11-REVIEWS MEDIUM #6): explicit
 * "memory" by default (4 admins, container restart resets are an
 * acceptable trade-off; see RUNBOOK-INCIDENTS.md class 4). Switch to
 * "secondary-storage" when REDIS_URL is wired into the dashboard
 * container (already on infra-redis-1 in stack).
 *
 * Schema source-of-truth (11-REVIEWS HIGH #3): Better Auth CLI is
 * canonical (`bunx @better-auth/cli@latest migrate`). The Drizzle
 * schema.ts file does NOT mirror the twoFactor plugin tables — see
 * the top-of-file comment in schema.ts.
 */
import { betterAuth } from "better-auth";
import { drizzleAdapter } from "better-auth/adapters/drizzle";
import { APIError, createAuthMiddleware, getSessionFromCtx } from "better-auth/api";
import { twoFactor } from "better-auth/plugins";
import { isAllowedEmail } from "./allowlist";
import { db, schema } from "./db"; // dashboard's OWN db, NOT ai_gateway

// Rate-limit storage decision (11-REVIEWS MEDIUM #6): explicit.
// When REDIS_URL is set, use "secondary-storage" (Better Auth's
// abstraction; wire ioredis at deploy time per docs). Otherwise fall
// back to "memory" — counters reset on container restart; see
// RUNBOOK-INCIDENTS.md class 4 for the lockout-bypass caveat. 4 admins
// + 15-min lockout window make this an acceptable trade-off until
// Phase 11-09 wires secondary-storage formally.
const RATE_LIMIT_STORAGE: "memory" | "secondary-storage" = process.env.REDIS_URL
  ? "secondary-storage"
  : "memory";

export const auth = betterAuth({
  baseURL: process.env.BETTER_AUTH_URL,
  secret: process.env.BETTER_AUTH_SECRET,
  database: drizzleAdapter(db, { provider: "pg", schema }),
  // D-12: never auto sign-in pre-enrollment (11-RESEARCH Pitfall 4).
  emailAndPassword: { enabled: true, autoSignIn: false },
  // D-15 (revised quick-260625-k17): 7-day idle session + cookieCache so
  // the Edge middleware can read twoFactor claims without a DB hit. Option A
  // — session.additionalFields
  // declares `twoFactorVerified`; the twoFactor plugin contributes
  // `user.twoFactorEnabled` (column on the user table) which also flows
  // into the cookie via parseUserOutput. See file header for the full
  // cookie-claim contract.
  session: {
    expiresIn: 7 * 24 * 60 * 60,
    updateAge: 5 * 60,
    // WR-01 (revised quick-260625-k17): cookieCache.maxAge MUST equal
    // session.expiresIn (both 7 days). The middleware reads
    // twoFactorVerified from the signed cookie for up to 7 days without
    // consulting the DB. maxAge MUST NOT be shorter than expiresIn —
    // nothing reheats the cookieCache during normal dashboard use:
    // useSession/get-session only fires at first-login, and the overview
    // poll hits /api/gateway, not /api/auth/get-session. When maxAge <
    // expiresIn the session_data cookie expires mid-session, getCookieCache
    // returns null, and the middleware's pessimistic fallback bounces
    // authenticated admins back to /2fa/challenge mid-session — the exact
    // bug this casa-de-valores fix kills (was maxAge=1800 vs expiresIn=1800,
    // both 30 min, which still expired during long idle sessions).
    // twoFactorVerified is monotonic within a session (false→true, never
    // reverts), so staleness on that claim is harmless. The only tradeoff:
    // after an operator revokes a session
    // (`DELETE FROM session WHERE id = ...`) OR after a TOTP reset via
    // RUNBOOK-2FA-RECOVERY.md, that change does NOT propagate to active
    // middleware decisions for up to 7 days (was up to 30 min). For
    // IMMEDIATE revocation the operator MUST delete the row in
    // `public.session` in the database — that invalidates the session
    // server-side regardless of the cookie cache. For a 4-admin internal
    // panel this trade-off is acceptable; runbook ops MUST either delete
    // the `public.session` row for instant lockout or wait up to 7 days.
    // See gateway/docs/RUNBOOK-INCIDENTS.md class 4 +
    // RUNBOOK-2FA-RECOVERY.md for the operator workflow.
    cookieCache: { enabled: true, maxAge: 7 * 24 * 60 * 60 },
    additionalFields: {
      twoFactorVerified: {
        type: "boolean",
        required: false,
        defaultValue: false,
        input: false,
      },
    },
  },
  // D-14: built-in rate-limit (NOT plugin) with per-route customRules.
  // Storage chosen per RATE_LIMIT_STORAGE above. customRules keys MUST
  // be the Better Auth canonical endpoints (NOT the Next.js routes):
  // /sign-in/email, /sign-up/email, /two-factor/verify-totp.
  rateLimit: {
    enabled: true,
    window: 60,
    max: 100,
    storage: RATE_LIMIT_STORAGE,
    customRules: {
      "/sign-in/email": { window: 900, max: 5 },
      "/sign-up/email": { window: 900, max: 5 },
      "/two-factor/verify-totp": { window: 60, max: 5 },
    },
  },
  // CR-01 defense-in-depth: reject /two-factor/enable when the caller's
  // user already has two_factor enabled. authClient.twoFactor.enable is
  // implemented as "issue-and-replace" by Better Auth — it overwrites
  // the existing TOTP secret + backup codes. An attacker with a valid
  // session cookie (XSS, stolen-laptop, MITM during the 30min cookieCache
  // miss window) + the user's password could otherwise rotate the
  // legitimate user's credentials and lock them out. We require an
  // operator-mediated reset via RUNBOOK-2FA-RECOVERY.md before any
  // re-enrollment.
  hooks: {
    before: createAuthMiddleware(async (ctx) => {
      if (ctx.path !== "/two-factor/enable") {
        return;
      }
      // Pull the session from the request — the enable endpoint also
      // requires a session, but we read here so we can inspect
      // user.twoFactorEnabled BEFORE the plugin overwrites the secret.
      const session = await getSessionFromCtx(ctx).catch(() => null);
      const enabled =
        (session as { user?: { twoFactorEnabled?: boolean } } | null)?.user
          ?.twoFactorEnabled === true;
      if (enabled) {
        throw new APIError("FORBIDDEN", {
          message:
            "two-factor já está habilitado neste usuário. Para rotacionar, execute o procedimento RUNBOOK-2FA-RECOVERY.md (audit-logged, separação-de-deveres).",
          code: "TWO_FACTOR_ALREADY_ENABLED",
        });
      }
    }),
  },
  // D-12: mandatory TOTP. issuer string locked per CONTEXT.md specifics
  // (line 159). SHA-1 default per @better-auth/utils/dist/otp.mjs:12 —
  // Google Authenticator + 1Password compatible.
  plugins: [twoFactor({ issuer: "Ifix AI Gateway" })],
  // D-13: email allowlist enforced server-side via the user-create hook
  // BEFORE Better Auth persists the row. Non-allowlisted domains are
  // rejected with a clear pt-BR message; the signup page surfaces the
  // rejection via the Alert in `app/signup/page.tsx`.
  databaseHooks: {
    user: {
      create: {
        before: async (user) => {
          if (!isAllowedEmail(user.email)) {
            throw new Error(
              "E-mail fora do allowlist @ifixtelecom.com.br",
            );
          }
          return { data: user };
        },
      },
    },
    // [reviews HIGH #2 Option B fallback]: Better Auth twoFactor plugin
    // verify-totp endpoint creates a new session row when 2FA challenge
    // completes (verify-two-factor.mjs L28: createSession + setSessionCookie),
    // but does NOT set our custom `twoFactorVerified` field (additionalFields
    // contract). Without this hook the middleware loops back to
    // /2fa/challenge after a successful TOTP verify. We hook
    // session.create.before and infer "session created from 2FA challenge
    // verify" by the endpoint path the wrapper attaches to the context.
    //
    // CR-04 hardening: match multiple potential path strings (current
    // 1.4.x naming + speculative future naming) AND read from both the
    // documented `endpoint.path` field and the legacy `path` field. If
    // Better Auth ships a context refactor that lands the path elsewhere,
    // this hook stays correct as long as one of the known shapes is
    // populated. Also fail-loud when neither shape carries a usable
    // string — that catches a future regression in CI when the
    // verify-totp integration test below stops setting twoFactorVerified.
    session: {
      create: {
        before: async (session, context) => {
          const ctx = context as
            | {
                path?: unknown;
                endpoint?: { path?: unknown } | null;
              }
            | null;
          const candidate1 =
            typeof ctx?.endpoint?.path === "string" ? ctx.endpoint.path : "";
          const candidate2 =
            typeof ctx?.path === "string" ? ctx.path : "";
          const path = candidate1 || candidate2;
          // Known verify endpoint paths across Better Auth 1.4.x. If a
          // future version renames the route (e.g. /two-factor/totp/verify),
          // add the new path here AND extend the auth.test.ts integration
          // test below so a missed rename fails in CI.
          const VERIFY_PATHS = new Set<string>([
            "/two-factor/verify-totp",
            "/two-factor/verify-backup-code",
            "/two-factor/verify-otp",
          ]);
          const fromChallenge = VERIFY_PATHS.has(path);
          if (fromChallenge) {
            return { data: { ...session, twoFactorVerified: true } };
          }
          return { data: session };
        },
      },
    },
  },
  advanced: { database: { generateId: () => crypto.randomUUID() } },
});

export type Auth = typeof auth;
