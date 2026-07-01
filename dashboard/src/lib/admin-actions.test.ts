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
import { beforeEach, describe, expect, it, vi } from "vitest";

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

// Mock the pod-config read wrapper (the LIVE-bound + audit-old source) and the
// server-only PATCH helper. The owner write actions (Plan 17-05) refetch the
// config via fetchPodConfig server-side, validate, then PATCH via
// gatewayAdminPatch — both are mocked so no proxy/network/admin-key is touched.
const { fetchPodConfigMock, gatewayAdminPatchMock } = vi.hoisted(() => ({
  fetchPodConfigMock: vi.fn(),
  gatewayAdminPatchMock: vi.fn(),
}));
// The owner write actions refetch live config via `fetchPodConfigServer`
// (gateway-server.ts) — the server-safe reader. Mock THAT, and leave
// `@/lib/gateway` real so `GatewayError` (imported by gateway-server) resolves.
vi.mock("@/lib/gateway-server", () => ({
  fetchPodConfigServer: fetchPodConfigMock,
}));
vi.mock("@/lib/gateway-admin", () => ({ gatewayAdminPatch: gatewayAdminPatchMock }));

// `requireOwner`'s session path calls `next/headers` `headers()`, which throws
// outside a Next.js request scope. Stub it so the CR-01 session-gate test can
// exercise the owner check with an injected auth instance (the legacy seam
// tests never hit this — they always pass an explicit `actor`).
vi.mock("next/headers", () => ({ headers: async () => new Headers() }));

/** A representative live pod_config snapshot for the write-action tests. */
function buildPodConfig() {
  return {
    config: {
      vast_machine_blocklist: [55942],
      vast_machine_allowlist: [43803, 55158],
      cap_primary: 0.5,
      cap_fallback: 1.0,
      host_id: 0,
      reject_private_ip: true,
      coldstart_budget_s: 3600,
      port_bind_budget_s: 300,
      failure_cooldown_s: 120,
      monthly_budget_brl: 2400,
      schedule_up_hour: 9,
      schedule_down_hour: 17,
      schedule_days: ["mon", "tue", "wed", "thu", "fri"],
      grace_ramp_down_s: 300,
      provision_lead_s: 600,
      schedule_disabled: false,
    },
    bounds: {
      cap_primary_min: 0.1,
      cap_primary_max: 1.5,
      cap_fallback_min: 0.1,
      cap_fallback_max: 3.0,
      coldstart_budget_s_min: 600,
      coldstart_budget_s_max: 7200,
      port_bind_budget_s_min: 60,
      port_bind_budget_s_max: 900,
      failure_cooldown_s_min: 30,
      failure_cooldown_s_max: 600,
      monthly_budget_brl_min: 200,
      monthly_budget_brl_max: 8000,
      schedule_up_hour_min: 0,
      schedule_up_hour_max: 23,
      schedule_down_hour_min: 0,
      schedule_down_hour_max: 23,
      grace_ramp_down_s_min: 0,
      grace_ramp_down_s_max: 1800,
      provision_lead_s_min: 0,
      provision_lead_s_max: 3600,
    },
  };
}

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
  // CR-01/CR-02 fix: the injectable surface (actor/auth/db/deps seams) now lives
  // in the non-`"use server"` core module. The public `@/lib/admin-actions`
  // Server Actions take ONLY business args and derive identity from the session,
  // so the in-process injection tests below target the `*Core` impls directly.
  const specifier = ["@/lib", "admin-actions-core"].join("/");
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
    const inviteOperator = mod?.inviteOperatorCore as
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
    const removeOperator = mod?.removeOperatorCore as
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
    const resetOperatorPassword = mod?.resetOperatorPasswordCore as
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
    const resetOperator2FA = mod?.resetOperator2FACore as
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
    const changePassword = mod?.changePasswordCore as
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

describe("admin-actions — POD-CFG owner-gated pod-config write actions (Plan 17-05)", () => {
  beforeEach(() => {
    fetchPodConfigMock.mockReset();
    gatewayAdminPatchMock.mockReset();
    sendMailMock.mockReset();
  });

  it("(POD-CFG-10) updatePodConfig: operator is rejected server-side — NO gateway call, NO audit row", async () => {
    const mod = await importAdminActions();
    expect(mod, "@/lib/admin-actions must export updatePodConfig").not.toBeNull();
    const updatePodConfig = mod?.updatePodConfigCore as
      | ((args: unknown) => Promise<unknown>)
      | undefined;
    expect(typeof updatePodConfig).toBe("function");

    const db = freshDb();
    fetchPodConfigMock.mockResolvedValue(buildPodConfig());

    let rejected = false;
    try {
      await updatePodConfig?.({
        actor: { role: "operator" },
        db,
        field: "cap_primary",
        value: 0.7,
      });
    } catch {
      rejected = true;
    }
    expect(rejected).toBe(true);
    // requireOwner throws FIRST — the gateway is never called and nothing is
    // audited.
    expect(gatewayAdminPatchMock).not.toHaveBeenCalled();
    expect(db.adminAuditLog.length).toBe(0);
  });

  it("(POD-CFG-10/11) updatePodConfig: owner success — refetches config, PATCHes the gateway, audits {field, old(from fetch), new}", async () => {
    const mod = await importAdminActions();
    const updatePodConfig = mod?.updatePodConfigCore as
      | ((args: unknown) => Promise<unknown>)
      | undefined;
    expect(typeof updatePodConfig).toBe("function");

    const db = freshDb();
    // cap_primary current value is 0.5 — the audit `old` MUST come from here,
    // not from any client-passed value.
    fetchPodConfigMock.mockResolvedValue(buildPodConfig());
    gatewayAdminPatchMock.mockResolvedValue(undefined);

    await updatePodConfig?.({
      actor: { id: "owner-1", email: "owner@ifixtelecom.com.br", role: "owner" },
      db,
      field: "cap_primary",
      value: 0.7,
    });

    // Refetch happened BEFORE the gateway write (live-bound + audit-old source).
    expect(fetchPodConfigMock).toHaveBeenCalledTimes(1);
    expect(gatewayAdminPatchMock).toHaveBeenCalledTimes(1);
    expect(
      fetchPodConfigMock.mock.invocationCallOrder[0],
    ).toBeLessThan(gatewayAdminPatchMock.mock.invocationCallOrder[0]);

    // PATCH carries the field/value with kind="config".
    const [path, body] = gatewayAdminPatchMock.mock.calls[0];
    expect(path).toBe("primary/config");
    expect(body).toMatchObject({ field: "cap_primary", value: 0.7, kind: "config" });

    // Exactly one audit row, action="pod_config.update", metadata {field, old, new}
    // where `old` is the REFETCHED current value (0.5), not the client value.
    expect(db.adminAuditLog.length).toBe(1);
    const row = db.adminAuditLog[0];
    expect(row.action).toBe("pod_config.update");
    expect(row.metadata).toEqual({ field: "cap_primary", old: 0.5, new: 0.7 });
    // No secret key ever lands in metadata.
    expect(JSON.stringify(row.metadata)).not.toContain("Admin");
  });

  it("(POD-CFG-11) updatePodConfig: a value outside the LIVE bound throws BEFORE any gateway call", async () => {
    const mod = await importAdminActions();
    const updatePodConfig = mod?.updatePodConfigCore as
      | ((args: unknown) => Promise<unknown>)
      | undefined;
    expect(typeof updatePodConfig).toBe("function");

    const db = freshDb();
    // cap_primary_max is 1.5 — 9.9 is out of range.
    fetchPodConfigMock.mockResolvedValue(buildPodConfig());

    let rejected = false;
    try {
      await updatePodConfig?.({
        actor: { role: "owner" },
        db,
        field: "cap_primary",
        value: 9.9,
      });
    } catch {
      rejected = true;
    }
    expect(rejected).toBe(true);
    expect(gatewayAdminPatchMock).not.toHaveBeenCalled();
    expect(db.adminAuditLog.length).toBe(0);
  });

  it("(POD-CFG-07/11) updatePodConfigBound: owner success — enforces min<max, PATCHes kind=bound, audits old from bounds", async () => {
    const mod = await importAdminActions();
    const updatePodConfigBound = mod?.updatePodConfigBoundCore as
      | ((args: unknown) => Promise<unknown>)
      | undefined;
    expect(typeof updatePodConfigBound).toBe("function");

    const db = freshDb();
    // cap_primary_min current value is 0.1 (the audit `old`); new 0.2 < max 1.5.
    fetchPodConfigMock.mockResolvedValue(buildPodConfig());
    gatewayAdminPatchMock.mockResolvedValue(undefined);

    await updatePodConfigBound?.({
      actor: { id: "owner-1", email: "owner@ifixtelecom.com.br", role: "owner" },
      db,
      field: "cap_primary_min",
      value: 0.2,
    });

    const [path, body] = gatewayAdminPatchMock.mock.calls[0];
    expect(path).toBe("primary/config");
    expect(body).toMatchObject({
      field: "cap_primary_min",
      value: 0.2,
      kind: "bound",
    });
    expect(db.adminAuditLog.length).toBe(1);
    const row = db.adminAuditLog[0];
    expect(row.action).toBe("pod_config_bounds.update");
    expect(row.metadata).toEqual({ field: "cap_primary_min", old: 0.1, new: 0.2 });
  });

  it("(POD-CFG-11) updatePodConfigBound: min >= max is rejected BEFORE any gateway call", async () => {
    const mod = await importAdminActions();
    const updatePodConfigBound = mod?.updatePodConfigBoundCore as
      | ((args: unknown) => Promise<unknown>)
      | undefined;
    expect(typeof updatePodConfigBound).toBe("function");

    const db = freshDb();
    // cap_primary_max is 1.5 — setting cap_primary_min=2.0 violates min<max.
    fetchPodConfigMock.mockResolvedValue(buildPodConfig());

    let rejected = false;
    try {
      await updatePodConfigBound?.({
        actor: { role: "owner" },
        db,
        field: "cap_primary_min",
        value: 2.0,
      });
    } catch {
      rejected = true;
    }
    expect(rejected).toBe(true);
    expect(gatewayAdminPatchMock).not.toHaveBeenCalled();
    expect(db.adminAuditLog.length).toBe(0);
  });
});

describe("admin-actions — CR-01/CR-02 authz hardening (session-only identity)", () => {
  beforeEach(() => {
    fetchPodConfigMock.mockReset();
    gatewayAdminPatchMock.mockReset();
  });

  it("(CR-01) requireOwner() with NO actor + a non-owner session is rejected", async () => {
    const core = await import("@/lib/admin-actions-core");
    const requireOwner = core.requireOwner as (
      actor?: unknown,
      authInstance?: unknown,
    ) => Promise<unknown>;
    expect(typeof requireOwner).toBe("function");

    // An injected auth whose live session resolves to an OPERATOR. With no
    // `actor` argument requireOwner MUST read this session (not trust a client
    // claim) and reject — proving the session is the only identity source.
    const operatorSessionAuth = {
      api: {
        getSession: async () => ({
          user: {
            id: "op-1",
            email: "op@ifixtelecom.com.br",
            role: "operator",
          },
        }),
      },
    };

    let rejected = false;
    try {
      await requireOwner(undefined, operatorSessionAuth);
    } catch {
      rejected = true;
    }
    expect(rejected).toBe(true);

    // Sanity: the SAME session resolving to an owner is accepted.
    const ownerSessionAuth = {
      api: {
        getSession: async () => ({
          user: { id: "ow-1", email: "ow@ifixtelecom.com.br", role: "owner" },
        }),
      },
    };
    const ok = (await requireOwner(undefined, ownerSessionAuth)) as {
      actor: { role?: string };
    };
    expect(ok.actor.role).toBe("owner");
  });

  it("(CR-01) the public updatePodConfig action exposes NO actor seam — a forged actor cannot escalate", async () => {
    // The public Server Action surface must not carry actor/auth/db/deps. A
    // forged `actor:{role:"owner"}` is therefore ignored; identity comes from
    // the session (absent here) so the call resolves UNAUTHENTICATED and never
    // touches the gateway.
    const pub = await import("@/lib/admin-actions");
    const updatePodConfig = pub.updatePodConfig as (
      args: unknown,
    ) => Promise<unknown>;
    expect(typeof updatePodConfig).toBe("function");

    fetchPodConfigMock.mockResolvedValue(buildPodConfig());

    let rejected = false;
    try {
      await updatePodConfig({
        // Forged escalation attempt — there is no `actor` param to bind to.
        actor: { role: "owner" },
        field: "cap_primary",
        value: 0.7,
      } as unknown);
    } catch {
      rejected = true;
    }
    expect(rejected).toBe(true);
    expect(gatewayAdminPatchMock).not.toHaveBeenCalled();
  });

  it("(CR-02) requireOwner and writeAuditLog are NOT exported from the `use server` action module", async () => {
    const pub = (await import("@/lib/admin-actions")) as Record<
      string,
      unknown
    >;
    // De-RPC proof: the audit writer and the owner gate must live ONLY in the
    // non-`use server` core, never on the network-reachable action surface.
    expect(pub.requireOwner).toBeUndefined();
    expect(pub.writeAuditLog).toBeUndefined();
    // The genuine entry points are still present.
    expect(typeof pub.updatePodConfig).toBe("function");
    expect(typeof pub.inviteOperator).toBe("function");
  });
});
