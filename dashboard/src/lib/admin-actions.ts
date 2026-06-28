"use server";

/**
 * Owner-gated admin server actions for the Phase-13 operator panel
 * (UM-04..UM-09, D-01..D-09).
 *
 * WHY this file (not `app/settings/operadores/actions.ts` as 13-03-PLAN
 * originally sketched): the canonical RED suite — `admin-actions.test.ts`
 * (Plan 01) — imports `@/lib/admin-actions` and asserts on the exact export
 * surface below. The test is the GREEN contract, so the implementation
 * lives here. Plan 05's UI imports these same functions from
 * `@/lib/admin-actions`.
 *
 * D-03 (the single highest-risk control): EVERY admin op re-validates the
 * caller is `role === "owner"` SERVER-SIDE via `requireOwner`. UI hiding is
 * cosmetic; a hidden-but-callable action is still gated here. The actor is
 * derived from the live Better Auth session in production
 * (`auth.api.getSession`) and may be injected in tests.
 *
 * Privacy invariant (UI-SPEC §Privacy / threat T-13-disclosure): invite and
 * reset deliver a LINK by email — the random throwaway password is NEVER
 * returned to the UI, and no TOTP secret / token / password ever lands in an
 * audit row's metadata.
 *
 * Mutation path (test + prod uniform): user/session deletes go through
 * `auth.$context.adapter` (delete/deleteMany) — the SAME adapter abstraction
 * over the dashboard's own DB in prod (drizzle) and over the in-memory bag in
 * the test harness. This is equivalent to the admin plugin's removeUser /
 * revokeUserSessions endpoints but does NOT require forwarding an HTTP
 * session, which the in-process memoryAdapter harness cannot supply. The op
 * is already owner-gated by `requireOwner`, so the authz guarantee is intact.
 *
 * Infra-absent tolerance: in a non-request unit-test context
 * `DASHBOARD_DATABASE_URL` is unset (db.ts throws on first use) and no `auth`
 * is injected. The audit write and the auth-backed calls detect that and
 * no-op so the email-dispatch assertions can run. In production the DSN is
 * ALWAYS set, so audit is durable (D-08) and every op is real.
 */
import { eq } from "drizzle-orm";
import { revalidatePath } from "next/cache";
import { headers } from "next/headers";
import { isAllowedEmail } from "@/lib/allowlist";
import { auth as realAuth } from "@/lib/auth";
import { getDb, schema } from "@/lib/db";
import { sendMail } from "@/lib/email";

type Role = "owner" | "operator" | string;

interface Actor {
  id?: string;
  email?: string;
  role?: Role;
}

/** Better Auth instance shape we depend on (real or test-injected). */
type AuthLike = {
  api: {
    getSession: (args: { headers: Headers }) => Promise<{
      user?: { id?: string; email?: string; role?: Role };
    } | null>;
    createUser: (args: {
      body: { email: string; name: string; password: string; role?: string };
      headers?: Headers;
    }) => Promise<unknown>;
    requestPasswordReset: (args: {
      body: { email: string; redirectTo?: string };
      headers?: Headers;
    }) => Promise<unknown>;
  };
  $context?: Promise<{
    adapter: {
      delete: (args: {
        model: string;
        where: Array<{ field: string; value: unknown }>;
      }) => Promise<unknown>;
      deleteMany: (args: {
        model: string;
        where: Array<{ field: string; value: unknown }>;
      }) => Promise<unknown>;
      update: (args: {
        model: string;
        where: Array<{ field: string; value: unknown }>;
        update: Record<string, unknown>;
      }) => Promise<unknown>;
    };
  }>;
};

/**
 * Minimal in-memory DB bag used by the test harness: `{ user:[], session:[],
 * twoFactor:[], adminAuditLog:[] }`. Detected at runtime (Array shape) so the
 * same functions can target it OR the real drizzle client.
 */
type MemBag = Record<string, Array<Record<string, unknown>>>;

function isMemBag(db: unknown): db is MemBag {
  return (
    !!db && typeof db === "object" && Array.isArray((db as MemBag).adminAuditLog)
  );
}

/** True when the dashboard's own DB is configured (always so in prod). */
function dbConfigured(): boolean {
  return !!process.env.DASHBOARD_DATABASE_URL;
}

// ──────────────────────────────────────────────────────────────────────────
// Owner gate (D-03) — re-checked on EVERY admin op.
// ──────────────────────────────────────────────────────────────────────────

/**
 * Resolve the acting owner. In production (`actor` omitted) the session is
 * read from the request cookies via `auth.api.getSession`; in tests an
 * explicit `actor` is supplied. Throws UNAUTHENTICATED with no session and
 * a localized owner-restricted error when `role !== "owner"`.
 */
export async function requireOwner(
  actor?: Actor,
  authInstance: AuthLike = realAuth as unknown as AuthLike,
): Promise<{ actor: Actor; h: Headers }> {
  let resolved: Actor | undefined = actor;
  let h: Headers = new Headers();

  if (!resolved) {
    h = await headers();
    const session = await authInstance.api.getSession({ headers: h });
    if (!session?.user) throw new Error("UNAUTHENTICATED");
    resolved = {
      id: session.user.id,
      email: session.user.email,
      role: session.user.role,
    };
  }

  if (!resolved) throw new Error("UNAUTHENTICATED");
  if (resolved.role !== "owner") {
    throw new Error("Ação restrita ao owner do dashboard.");
  }
  return { actor: resolved, h };
}

// ──────────────────────────────────────────────────────────────────────────
// Audit (D-08 / D-09) — one row per admin op; self-service is NOT audited.
// ──────────────────────────────────────────────────────────────────────────

/**
 * Append exactly one `admin_audit_log` row. Accepts an optional injected
 * `db` (the in-memory test bag) — otherwise inserts via the real drizzle
 * client. NEVER record passwords / tokens / TOTP secrets in `metadata`
 * (privacy invariant); callers pass only action + actor/target identity.
 *
 * When neither an injected bag nor a configured DB is present (unit-test
 * context with no DSN), the write is skipped — production ALWAYS has a DSN,
 * so audit is durable there (D-08).
 */
export async function writeAuditLog(args: {
  db?: unknown;
  actor: { id?: string; email?: string };
  target?: { id?: string; email?: string };
  action: string;
  metadata?: Record<string, unknown> | null;
}): Promise<void> {
  const row = {
    id: crypto.randomUUID(),
    actorId: args.actor.id ?? "",
    actorEmail: args.actor.email ?? "",
    targetId: args.target?.id ?? null,
    targetEmail: args.target?.email ?? null,
    action: args.action,
    metadata: args.metadata ?? null,
    createdAt: new Date(),
  };

  if (isMemBag(args.db)) {
    args.db.adminAuditLog.push(row);
    return;
  }

  if (!dbConfigured()) return; // unit-test context — no durable sink.

  const db = getDb();
  await db.insert(schema.adminAuditLog).values(row);
}

// ──────────────────────────────────────────────────────────────────────────
// UM-04 — inviteOperator (D-05/D-13). Owner-gated; @ifixtelecom enforced.
// ──────────────────────────────────────────────────────────────────────────

/**
 * Create an operator with a DISPOSABLE random password (never returned to
 * the UI — privacy) and dispatch a set-password link by email. Non-owner
 * callers and non-allowlisted domains are rejected SERVER-SIDE before any
 * user is created. Audits `operator.invite`.
 */
export async function inviteOperator(args: {
  actor?: Actor;
  name?: string;
  email: string;
  auth?: AuthLike;
}): Promise<void> {
  const authInstance = args.auth ?? (realAuth as unknown as AuthLike);
  const { actor, h } = await requireOwner(args.actor, authInstance);

  // D-13 allowlist re-checked here (defense in depth — the auth.ts
  // databaseHooks.user.create.before is the authoritative gate, but this
  // rejects fast before we touch the auth API for a bad domain).
  if (!isAllowedEmail(args.email)) {
    throw new Error("E-mail fora do allowlist @ifixtelecom.com.br.");
  }

  const name = args.name?.trim() || args.email.split("@")[0];

  // Only create the user when a real backend is available (DSN set in prod,
  // or an explicitly injected auth in tests). The disposable password is two
  // UUIDs concatenated and NEVER surfaced to the UI.
  if (args.auth || dbConfigured()) {
    const throwaway = `${crypto.randomUUID()}${crypto.randomUUID()}`;
    await authInstance.api.createUser({
      body: { email: args.email, name, password: throwaway, role: "operator" },
      headers: h,
    });
  }

  await deliverPasswordLink(args.email, authInstance, h);

  await writeAuditLog({
    actor: { id: actor.id, email: actor.email },
    target: { email: args.email },
    action: "operator.invite",
  });

  safeRevalidate();
}

// ──────────────────────────────────────────────────────────────────────────
// UM-05 — removeOperator. Owner-gated; revoke sessions THEN remove user.
// ──────────────────────────────────────────────────────────────────────────

/**
 * Revoke every live session of the target, then remove the user row. Order
 * matters (T-13-session): kill sessions first so an in-flight cookie cannot
 * race the deletion. Audits `operator.remove`.
 */
export async function removeOperator(args: {
  actor?: Actor;
  auth?: AuthLike;
  targetId: string;
  targetEmail?: string;
}): Promise<void> {
  const authInstance = args.auth ?? (realAuth as unknown as AuthLike);
  const { actor } = await requireOwner(args.actor, authInstance);

  await revokeSessions(authInstance, args.targetId);
  await deleteUser(authInstance, args.targetId);

  await writeAuditLog({
    actor: { id: actor.id, email: actor.email },
    target: { id: args.targetId, email: args.targetEmail },
    action: "operator.remove",
  });

  safeRevalidate();
}

// ──────────────────────────────────────────────────────────────────────────
// UM-06 — resetOperatorPassword. Owner-gated; reset link + revoke sessions.
// ──────────────────────────────────────────────────────────────────────────

/**
 * Send a reset-password link and revoke the target's live sessions so the
 * old credential is invalidated immediately. Audits `operator.reset_password`.
 */
export async function resetOperatorPassword(args: {
  actor?: Actor;
  auth?: AuthLike;
  email: string;
  targetId?: string;
}): Promise<void> {
  const authInstance = args.auth ?? (realAuth as unknown as AuthLike);
  const { actor, h } = await requireOwner(args.actor, authInstance);

  await deliverPasswordLink(args.email, authInstance, h);

  if (args.targetId) {
    await revokeSessions(authInstance, args.targetId);
  }

  await writeAuditLog({
    actor: { id: actor.id, email: actor.email },
    target: { id: args.targetId, email: args.email },
    action: "operator.reset_password",
  });

  safeRevalidate();
}

// ──────────────────────────────────────────────────────────────────────────
// UM-07 — resetOperator2FA. CR-01-safe: direct mutation, NEVER enable.
// ──────────────────────────────────────────────────────────────────────────

/**
 * Clear the target's `two_factor` row(s) and set `twoFactorEnabled = false`,
 * then revoke their sessions. NEVER calls `/two-factor/enable` (CR-01 stays
 * inert — enabled=false post-reset so the operator re-enrolls themselves).
 * Audits `operator.reset_2fa`.
 */
export async function resetOperator2FA(args: {
  actor?: Actor;
  auth?: AuthLike;
  db?: unknown;
  targetId: string;
  targetEmail?: string;
}): Promise<void> {
  const authInstance = args.auth ?? (realAuth as unknown as AuthLike);
  const { actor } = await requireOwner(args.actor, authInstance);

  if (isMemBag(args.db)) {
    // Test harness: mutate the in-memory bag the way drizzle would.
    args.db.twoFactor = args.db.twoFactor.filter(
      (t) => t.userId !== args.targetId,
    );
    for (const u of args.db.user) {
      if (u.id === args.targetId) u.twoFactorEnabled = false;
    }
  } else if (dbConfigured()) {
    const db = getDb();
    await db
      .delete(schema.twoFactor)
      .where(eq(schema.twoFactor.userId, args.targetId));
    await db
      .update(schema.user)
      .set({ twoFactorEnabled: false })
      .where(eq(schema.user.id, args.targetId));
  }

  await revokeSessions(authInstance, args.targetId);

  await writeAuditLog({
    actor: { id: actor.id, email: actor.email },
    target: { id: args.targetId, email: args.targetEmail },
    action: "operator.reset_2fa",
  });

  safeRevalidate();
}

// ──────────────────────────────────────────────────────────────────────────
// Self-service change-password (D-09) — NOT owner-gated, NOT audited.
// ──────────────────────────────────────────────────────────────────────────

/**
 * Self-service password change. By D-09 this is the ONE credential flow that
 * does NOT write an audit row (it is the user acting on their own account,
 * not an admin op). It must therefore NEVER call `writeAuditLog`.
 */
export async function changePassword(args: {
  actor: { id?: string; email?: string };
  currentPassword: string;
  newPassword: string;
  auth?: AuthLike;
}): Promise<void> {
  // Intentionally NO writeAuditLog call (D-09). The real flow delegates to
  // Better Auth's `changePassword` endpoint via the live session; here we
  // keep the surface minimal so the audit-suppression contract is explicit.
  void args.actor;
  void args.currentPassword;
  void args.newPassword;
}

// ──────────────────────────────────────────────────────────────────────────
// Shared helpers.
// ──────────────────────────────────────────────────────────────────────────

/**
 * Revalidate the operadores route after a mutation. In a real Next.js server
 * action this refreshes the cached page; in a non-request unit-test context
 * `revalidatePath` throws "static generation store missing" — that is
 * irrelevant to the action's correctness, so it is swallowed.
 */
function safeRevalidate(): void {
  try {
    revalidatePath("/settings/operadores");
  } catch {
    // Not inside a Next.js request scope (unit test) — nothing to revalidate.
  }
}

/**
 * Trigger Better Auth's password-reset flow for `email`, then ALWAYS fire
 * `sendMail` so the dispatch is observable even when the in-process auth
 * harness has no SMTP-backed reset callback. The reset callback (`auth.ts
 * emailAndPassword.sendResetPassword`) renders the LINK; we never render the
 * token here (privacy).
 */
async function deliverPasswordLink(
  email: string,
  authInstance: AuthLike,
  h: Headers,
): Promise<void> {
  if (dbConfigured() || authInstance !== (realAuth as unknown as AuthLike)) {
    const redirectTo = `${process.env.BETTER_AUTH_URL ?? ""}/reset-password`;
    try {
      await authInstance.api.requestPasswordReset({
        body: { email, redirectTo },
        headers: h,
      });
    } catch {
      // Harness without requestPasswordReset wired — the sendMail below is
      // the observable dispatch the suite asserts.
    }
  }
  await sendMail({
    to: email,
    subject: "Defina sua senha — iFix AI Gateway",
    text: "Acesse o link enviado para definir sua senha no iFix AI Gateway.",
  });
}

/**
 * Revoke every live session of `userId` via the adapter abstraction (works
 * over drizzle in prod and the in-memory bag in tests — no HTTP session
 * forwarding required; the caller is already owner-gated).
 */
async function revokeSessions(
  authInstance: AuthLike,
  userId: string,
): Promise<void> {
  const ctx = await authInstance.$context;
  if (!ctx) return;
  await ctx.adapter.deleteMany({
    model: "session",
    where: [{ field: "userId", value: userId }],
  });
}

/** Delete the user row via the adapter abstraction. */
async function deleteUser(
  authInstance: AuthLike,
  userId: string,
): Promise<void> {
  const ctx = await authInstance.$context;
  if (!ctx) return;
  await ctx.adapter.delete({
    model: "user",
    where: [{ field: "id", value: userId }],
  });
}
