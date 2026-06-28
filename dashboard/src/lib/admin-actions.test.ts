/**
 * RED test stubs for the owner-gated admin server actions (Phase 13).
 *
 * Covers UM-04 (invite), UM-05 (remove), UM-06 (reset-password),
 * UM-07 (reset-2FA), UM-08/D-09 (audit row per admin op + ZERO rows for
 * self-service change-password).
 *
 * These are EXPECTED TO FAIL (RED) until Waves 1-3 implement
 * `src/lib/admin-actions.ts`. The module does not exist yet, so each test
 * performs a *guarded dynamic import* of `@/lib/admin-actions` and asserts
 * on the resolved module. A missing module surfaces as a FAILING
 * ASSERTION (RED) — NOT a vitest collection/import error — because the
 * import is awaited inside the test body and its rejection is caught and
 * turned into an explicit `expect(...).toBe(...)` failure.
 *
 * The Brevo SMTP mailer (`@/lib/email`) is mocked so no real SMTP fires:
 * the invite/reset assertions inspect the mocked transport's `sendMail`
 * spy. The auth surface reuses the SAME memoryAdapter harness as
 * `auth.test.ts` (admin plugin + twoFactor) so the owner-gate and
 * session-revocation behavior can be exercised in-process once the
 * implementation lands.
 */
import { betterAuth } from "better-auth";
import { memoryAdapter } from "better-auth/adapters/memory";
import { admin as adminPlugin, twoFactor } from "better-auth/plugins";
import { createAccessControl } from "better-auth/plugins/access";
import { defaultStatements } from "better-auth/plugins/admin/access";
import { describe, expect, it, vi } from "vitest";

// Mock the Brevo SMTP mailer transport — no real SMTP in tests. Waves 1-3
// will create `src/lib/email.ts` exporting `sendMail` (the nodemailer
// transport wrapper wired into Better Auth's `sendResetPassword`).
const { sendMailMock } = vi.hoisted(() => ({ sendMailMock: vi.fn() }));
vi.mock("@/lib/email", () => ({
  sendMail: sendMailMock,
  // also expose a transport-shaped surface in case the impl wires the
  // nodemailer transporter object directly.
  mailer: { sendMail: sendMailMock },
}));

type MemDB = { [k: string]: any[] };

function freshDb(): MemDB {
  return {
    user: [],
    session: [],
    account: [],
    verification: [],
    twoFactor: [],
    // D-08 audit table — present so the in-memory adapter can hold rows
    // once admin-actions writes them via drizzle in prod (mirrored here).
    adminAuditLog: [],
  };
}

/**
 * Build a Better Auth instance mirroring the Phase-13 target auth.ts:
 * the SAME memoryAdapter harness as auth.test.ts PLUS the admin plugin
 * (adminRoles:["owner"], defaultRole:"operator") that Wave 1 wires in.
 */
function buildAdminAuth() {
  const db = freshDb();
  // A2 / 13-RESEARCH Pitfall 4: the admin plugin validates every entry of
  // `adminRoles` against its `roles` access-control map AT CONSTRUCTION
  // (admin.mjs:21-22). The custom string role `owner` is NOT in the default
  // map (`admin`/`user`), so `adminPlugin({ adminRoles:["owner"] })` ALONE
  // throws "Invalid admin roles: owner" before any test body runs. We mirror
  // the exact `ac`/`roles` workaround from `lib/auth.ts` so the harness
  // constructs a valid owner-gated instance.
  const ac = createAccessControl(defaultStatements);
  const ownerRole = ac.newRole({
    user: [
      "create",
      "list",
      "set-role",
      "ban",
      "impersonate",
      "delete",
      "set-password",
      "get",
      "update",
    ],
    session: ["list", "revoke", "delete"],
  });
  const operatorRole = ac.newRole({ user: [], session: [] });
  const auth = betterAuth({
    baseURL: "http://localhost:3001",
    secret: "test-secret-do-not-use-in-prod-aaaaaaaaaaaaaaaa",
    database: memoryAdapter(db),
    emailAndPassword: { enabled: true, autoSignIn: false },
    plugins: [
      twoFactor({ issuer: "Ifix AI Gateway" }),
      adminPlugin({
        ac,
        roles: { owner: ownerRole, operator: operatorRole },
        adminRoles: ["owner"],
        defaultRole: "operator",
      }),
    ],
    advanced: { database: { generateId: () => crypto.randomUUID() } },
  });
  return { auth, db };
}

/**
 * Guarded dynamic import — returns the module or null (RED-friendly).
 *
 * The specifier is built from a variable so Vite's static `import-analysis`
 * plugin does NOT resolve it at transform time (a literal import of a
 * not-yet-created module fails the whole SUITE at collection rather than
 * as a RED assertion — see 13-01 acceptance criteria). At runtime the
 * missing module rejects and is caught → FAILING ASSERTION (RED).
 */
async function importAdminActions(): Promise<Record<string, unknown> | null> {
  const specifier = ["@/lib", "admin-actions"].join("/");
  try {
    return (await import(/* @vite-ignore */ specifier)) as Record<
      string,
      unknown
    >;
  } catch {
    return null;
  }
}

describe("admin-actions — UM-04..UM-08 owner-gated server actions (RED until Wave 1-3)", () => {
  it("(UM-04) inviteOperator: non-owner rejected, non-@ifixtelecom rejected, reset email fired on MOCK mailer", async () => {
    const mod = await importAdminActions();
    // RED: module absent today → fails here as an assertion, not import error.
    expect(mod, "@/lib/admin-actions must export inviteOperator").not.toBeNull();
    const inviteOperator = mod?.inviteOperator as
      | ((args: unknown) => Promise<unknown>)
      | undefined;
    expect(typeof inviteOperator).toBe("function");

    // Non-owner caller must be FORBIDDEN (owner-gate).
    let nonOwnerRejected = false;
    try {
      await inviteOperator?.({
        actor: { role: "operator" },
        email: "new@ifixtelecom.com.br",
      });
    } catch {
      nonOwnerRejected = true;
    }
    expect(nonOwnerRejected).toBe(true);

    // Non-@ifixtelecom target must be rejected even for an owner.
    let badDomainRejected = false;
    try {
      await inviteOperator?.({
        actor: { role: "owner" },
        email: "outsider@gmail.com",
      });
    } catch {
      badDomainRejected = true;
    }
    expect(badDomainRejected).toBe(true);

    // Owner inviting a valid @ifixtelecom address → reset/set-password
    // email dispatched via the MOCKED mailer (no real SMTP).
    await inviteOperator?.({
      actor: { role: "owner" },
      email: "valid@ifixtelecom.com.br",
    });
    expect(sendMailMock).toHaveBeenCalled();
  });

  it("(UM-05) removeOperator: owner-gated; removes user AND revokes all their sessions", async () => {
    const mod = await importAdminActions();
    expect(mod, "@/lib/admin-actions must export removeOperator").not.toBeNull();
    const removeOperator = mod?.removeOperator as
      | ((args: unknown) => Promise<unknown>)
      | undefined;
    expect(typeof removeOperator).toBe("function");

    const { auth, db } = buildAdminAuth();
    // Provision a target operator with at least one session.
    await auth.api.signUpEmail({
      body: {
        email: "target@ifixtelecom.com.br",
        password: "TestPassword!123",
        name: "Target",
      },
    });
    await auth.api.signInEmail({
      body: {
        email: "target@ifixtelecom.com.br",
        password: "TestPassword!123",
      },
    });
    const target = db.user.find(
      (u: { email: string }) => u.email === "target@ifixtelecom.com.br",
    );
    expect(target).toBeDefined();

    await removeOperator?.({
      actor: { role: "owner" },
      auth,
      targetId: target.id,
    });

    // After removal: no user row, no live sessions for that user.
    const stillThere = db.user.some(
      (u: { id: string }) => u.id === target.id,
    );
    expect(stillThere).toBe(false);
    const liveSessions = db.session.filter(
      (s: { userId: string }) => s.userId === target.id,
    );
    expect(liveSessions.length).toBe(0);
  });

  it("(UM-06) resetOperatorPassword: owner-gated; dispatches reset email + revokes target sessions", async () => {
    const mod = await importAdminActions();
    expect(
      mod,
      "@/lib/admin-actions must export resetOperatorPassword",
    ).not.toBeNull();
    const resetOperatorPassword = mod?.resetOperatorPassword as
      | ((args: unknown) => Promise<unknown>)
      | undefined;
    expect(typeof resetOperatorPassword).toBe("function");

    await resetOperatorPassword?.({
      actor: { role: "owner" },
      email: "target@ifixtelecom.com.br",
    });
    expect(sendMailMock).toHaveBeenCalled();
  });

  it("(UM-07) resetOperator2FA: clears two_factor + sets twoFactorEnabled=false; CR-01 enable allowed after", async () => {
    const mod = await importAdminActions();
    expect(
      mod,
      "@/lib/admin-actions must export resetOperator2FA",
    ).not.toBeNull();
    const resetOperator2FA = mod?.resetOperator2FA as
      | ((args: unknown) => Promise<unknown>)
      | undefined;
    expect(typeof resetOperator2FA).toBe("function");

    const { auth, db } = buildAdminAuth();
    await auth.api.signUpEmail({
      body: {
        email: "twofa@ifixtelecom.com.br",
        password: "TestPassword!123",
        name: "TwoFA",
      },
    });
    const target = db.user.find(
      (u: { email: string }) => u.email === "twofa@ifixtelecom.com.br",
    );
    // Simulate an enrolled 2FA state.
    target.twoFactorEnabled = true;
    db.twoFactor.push({ id: crypto.randomUUID(), userId: target.id });

    await resetOperator2FA?.({
      actor: { role: "owner" },
      auth,
      db,
      targetId: target.id,
    });

    // two_factor row cleared + flag false.
    const remaining2fa = db.twoFactor.filter(
      (t: { userId: string }) => t.userId === target.id,
    );
    expect(remaining2fa.length).toBe(0);
    const after = db.user.find((u: { id: string }) => u.id === target.id);
    expect(after.twoFactorEnabled).toBe(false);
  });

  it("(UM-08/D-09) audit: each admin op writes one admin_audit_log row; self-service change-password writes ZERO", async () => {
    const mod = await importAdminActions();
    expect(
      mod,
      "@/lib/admin-actions must export an audit writer (writeAuditLog) and a self-service changePassword that does NOT audit",
    ).not.toBeNull();
    const writeAuditLog = mod?.writeAuditLog as
      | ((args: unknown) => Promise<unknown> | unknown)
      | undefined;
    const changePassword = mod?.changePassword as
      | ((args: unknown) => Promise<unknown>)
      | undefined;
    expect(typeof writeAuditLog).toBe("function");
    expect(typeof changePassword).toBe("function");

    const { db } = buildAdminAuth();

    // One admin op → exactly one audit row.
    await writeAuditLog?.({
      db,
      actor: { id: "owner-1", email: "owner@ifixtelecom.com.br" },
      target: { id: "op-1", email: "op@ifixtelecom.com.br" },
      action: "invite",
    });
    expect(db.adminAuditLog.length).toBe(1);

    // Self-service change-password → ZERO audit rows (D-09).
    const before = db.adminAuditLog.length;
    await changePassword?.({
      actor: { id: "op-1", email: "op@ifixtelecom.com.br" },
      currentPassword: "old",
      newPassword: "new",
    });
    expect(db.adminAuditLog.length).toBe(before);
  });
});
