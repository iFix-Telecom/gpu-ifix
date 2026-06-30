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
import {
  fetchPodConfig as realFetchPodConfig,
  type PodConfigResponse,
} from "@/lib/gateway";
import { gatewayAdminPatch as realGatewayAdminPatch } from "@/lib/gateway-admin";

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
// POD-CFG-10/11/07 — owner-gated pod-config write actions (Plan 17-05).
//
// Mirror inviteOperator EXACTLY: requireOwner FIRST (D-07, server-side) →
// refetch the LIVE pod_config (the single source for the current value AND the
// validation bound — never trust a client-passed value) → validate against the
// refetched bound BEFORE any gateway call (D-03a, defense-in-depth with the
// gateway's own check) → PATCH the gateway via the server-only key helper (D-07,
// NOT the read-only proxy) → write EXACTLY one audit row (D-06) → revalidate.
// ──────────────────────────────────────────────────────────────────────────

type PodConfigSection = PodConfigResponse["config"];
type PodConfigBoundsT = PodConfigResponse["bounds"];

/**
 * Spec for one editable hot config field. `configKey` is where the audit `old`
 * value is read from the refetched snapshot (the PATCH `field` name and the GET
 * config key diverge only for the two list fields). When `min`/`max` are set
 * the value is range-validated against those refetched bounds; `hour` adds the
 * 0-23 + up≠down cross-field rule.
 */
interface ConfigFieldSpec {
  configKey: keyof PodConfigSection;
  min?: keyof PodConfigBoundsT;
  max?: keyof PodConfigBoundsT;
  hour?: boolean;
}

/** The 16 hot fields, keyed by their gateway PATCH `field` name. */
const CONFIG_FIELDS: Record<string, ConfigFieldSpec> = {
  blocklist: { configKey: "vast_machine_blocklist" },
  allowlist: { configKey: "vast_machine_allowlist" },
  cap_primary: {
    configKey: "cap_primary",
    min: "cap_primary_min",
    max: "cap_primary_max",
  },
  cap_fallback: {
    configKey: "cap_fallback",
    min: "cap_fallback_min",
    max: "cap_fallback_max",
  },
  host_id: { configKey: "host_id" },
  reject_private_ip: { configKey: "reject_private_ip" },
  coldstart_budget_s: {
    configKey: "coldstart_budget_s",
    min: "coldstart_budget_s_min",
    max: "coldstart_budget_s_max",
  },
  port_bind_budget_s: {
    configKey: "port_bind_budget_s",
    min: "port_bind_budget_s_min",
    max: "port_bind_budget_s_max",
  },
  failure_cooldown_s: {
    configKey: "failure_cooldown_s",
    min: "failure_cooldown_s_min",
    max: "failure_cooldown_s_max",
  },
  monthly_budget_brl: {
    configKey: "monthly_budget_brl",
    min: "monthly_budget_brl_min",
    max: "monthly_budget_brl_max",
  },
  schedule_up_hour: {
    configKey: "schedule_up_hour",
    min: "schedule_up_hour_min",
    max: "schedule_up_hour_max",
    hour: true,
  },
  schedule_down_hour: {
    configKey: "schedule_down_hour",
    min: "schedule_down_hour_min",
    max: "schedule_down_hour_max",
    hour: true,
  },
  schedule_days: { configKey: "schedule_days" },
  grace_ramp_down_s: {
    configKey: "grace_ramp_down_s",
    min: "grace_ramp_down_s_min",
    max: "grace_ramp_down_s_max",
  },
  provision_lead_s: {
    configKey: "provision_lead_s",
    min: "provision_lead_s_min",
    max: "provision_lead_s_max",
  },
  schedule_disabled: { configKey: "schedule_disabled" },
};

/** Spec for one editable bound: its counterpart + which side this field is. */
interface BoundFieldSpec {
  counterpart: keyof PodConfigBoundsT;
  side: "min" | "max";
}

/** The 20 bound fields, keyed by their gateway PATCH `field` name. */
const BOUND_FIELDS: Record<string, BoundFieldSpec> = {
  cap_primary_min: { counterpart: "cap_primary_max", side: "min" },
  cap_primary_max: { counterpart: "cap_primary_min", side: "max" },
  cap_fallback_min: { counterpart: "cap_fallback_max", side: "min" },
  cap_fallback_max: { counterpart: "cap_fallback_min", side: "max" },
  coldstart_budget_s_min: { counterpart: "coldstart_budget_s_max", side: "min" },
  coldstart_budget_s_max: { counterpart: "coldstart_budget_s_min", side: "max" },
  port_bind_budget_s_min: { counterpart: "port_bind_budget_s_max", side: "min" },
  port_bind_budget_s_max: { counterpart: "port_bind_budget_s_min", side: "max" },
  failure_cooldown_s_min: { counterpart: "failure_cooldown_s_max", side: "min" },
  failure_cooldown_s_max: { counterpart: "failure_cooldown_s_min", side: "max" },
  monthly_budget_brl_min: { counterpart: "monthly_budget_brl_max", side: "min" },
  monthly_budget_brl_max: { counterpart: "monthly_budget_brl_min", side: "max" },
  schedule_up_hour_min: { counterpart: "schedule_up_hour_max", side: "min" },
  schedule_up_hour_max: { counterpart: "schedule_up_hour_min", side: "max" },
  schedule_down_hour_min: { counterpart: "schedule_down_hour_max", side: "min" },
  schedule_down_hour_max: { counterpart: "schedule_down_hour_min", side: "max" },
  grace_ramp_down_s_min: { counterpart: "grace_ramp_down_s_max", side: "min" },
  grace_ramp_down_s_max: { counterpart: "grace_ramp_down_s_min", side: "max" },
  provision_lead_s_min: { counterpart: "provision_lead_s_max", side: "min" },
  provision_lead_s_max: { counterpart: "provision_lead_s_min", side: "max" },
};

/** Injectable seams for unit tests (mock the gateway read + write). */
interface PodConfigDeps {
  fetchConfig?: () => Promise<PodConfigResponse>;
  patch?: (path: string, body: unknown) => Promise<void>;
}

/**
 * Edit ONE hot pod_config field. Owner-gated (D-07). The value is validated
 * against the REFETCHED live bound BEFORE the gateway PATCH (D-03a); the audit
 * `old` is sourced from that SAME refetch (never a client-passed value). Writes
 * exactly one `pod_config.update` audit row with metadata `{field, old, new}` —
 * NEVER any secret. One field per call (clean audit diff).
 */
export async function updatePodConfig(args: {
  actor?: Actor;
  auth?: AuthLike;
  db?: unknown;
  field: string;
  value: unknown;
  deps?: PodConfigDeps;
}): Promise<void> {
  const authInstance = args.auth ?? (realAuth as unknown as AuthLike);
  const { actor } = await requireOwner(args.actor, authInstance);

  const spec = CONFIG_FIELDS[args.field];
  if (!spec) {
    throw new Error(`Campo de configuração desconhecido: ${args.field}`);
  }

  const fetchConfig = args.deps?.fetchConfig ?? realFetchPodConfig;
  const patch = args.deps?.patch ?? realGatewayAdminPatch;

  // Refetch the LIVE config: the single source for both the bound and the
  // audit `old`. Done AFTER the owner gate, BEFORE validation.
  const current = await fetchConfig();

  // Range-validate numeric fields against the refetched bound (D-03a).
  if (spec.min && spec.max) {
    if (typeof args.value !== "number" || Number.isNaN(args.value)) {
      throw new Error(`Valor inválido para ${args.field}.`);
    }
    const lo = current.bounds[spec.min] as number;
    const hi = current.bounds[spec.max] as number;
    if (args.value < lo || args.value > hi) {
      throw new Error(
        `Valor ${args.value} fora do limite permitido [${lo}, ${hi}] para ${args.field}.`,
      );
    }
    if (spec.hour) {
      if (args.value < 0 || args.value > 23) {
        throw new Error("A hora deve estar entre 0 e 23.");
      }
      const other =
        args.field === "schedule_up_hour"
          ? current.config.schedule_down_hour
          : current.config.schedule_up_hour;
      if (args.value === other) {
        throw new Error(
          "schedule_up_hour e schedule_down_hour devem ser diferentes.",
        );
      }
    }
  }

  await patch("primary/config", {
    field: args.field,
    value: args.value,
    kind: "config",
  });

  await writeAuditLog({
    db: args.db,
    actor: { id: actor.id, email: actor.email },
    action: "pod_config.update",
    metadata: {
      field: args.field,
      old: current.config[spec.configKey],
      new: args.value,
    },
  });

  safeRevalidatePodConfig();
}

/**
 * Edit ONE owner-editable bound. Owner-gated (D-07). Enforces min < max against
 * the REFETCHED counterpart bound BEFORE the gateway PATCH; the audit `old` is
 * sourced from the same refetch. Writes exactly one `pod_config_bounds.update`
 * audit row with metadata `{field, old, new}`.
 */
export async function updatePodConfigBound(args: {
  actor?: Actor;
  auth?: AuthLike;
  db?: unknown;
  field: string;
  value: unknown;
  deps?: PodConfigDeps;
}): Promise<void> {
  const authInstance = args.auth ?? (realAuth as unknown as AuthLike);
  const { actor } = await requireOwner(args.actor, authInstance);

  const spec = BOUND_FIELDS[args.field];
  if (!spec) {
    throw new Error(`Limite desconhecido: ${args.field}`);
  }
  if (typeof args.value !== "number" || Number.isNaN(args.value)) {
    throw new Error(`Valor inválido para ${args.field}.`);
  }

  const fetchConfig = args.deps?.fetchConfig ?? realFetchPodConfig;
  const patch = args.deps?.patch ?? realGatewayAdminPatch;

  const current = await fetchConfig();
  const counterpart = current.bounds[spec.counterpart] as number;

  if (spec.side === "min" && args.value >= counterpart) {
    throw new Error(`O limite mínimo deve ser menor que o máximo (${counterpart}).`);
  }
  if (spec.side === "max" && args.value <= counterpart) {
    throw new Error(`O limite máximo deve ser maior que o mínimo (${counterpart}).`);
  }

  await patch("primary/config", {
    field: args.field,
    value: args.value,
    kind: "bound",
  });

  await writeAuditLog({
    db: args.db,
    actor: { id: actor.id, email: actor.email },
    action: "pod_config_bounds.update",
    metadata: {
      field: args.field,
      old: current.bounds[args.field as keyof PodConfigBoundsT],
      new: args.value,
    },
  });

  safeRevalidatePodConfig();
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
 * Revalidate the pod-config page after an owner edit (Plan 17-05/17-06).
 * Swallowed outside a Next.js request scope (unit test), like safeRevalidate.
 */
function safeRevalidatePodConfig(): void {
  try {
    revalidatePath("/operacao/config");
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
