# Phase 13: dashboard-user-management - Pattern Map

**Mapped:** 2026-06-15
**Files analyzed:** 13 (8 new, 5 modified)
**Analogs found:** 13 / 13 (all in-repo; reset-2FA op has no first-party endpoint but a direct-drizzle analog exists)

All work lives in `dashboard/` (Next.js 15 App Router, standalone Better Auth 1.4.22 instance, Drizzle/Postgres). Every analog below was read at the line ranges cited; the dashboard already ships every primitive a Phase 13 file needs to copy.

---

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| `dashboard/src/lib/auth.ts` (EDIT) | config | request-response | itself (extend existing `betterAuth({...})`) | exact (self) |
| `dashboard/src/lib/auth-client.ts` (EDIT, optional) | config | request-response | itself (extend existing `createAuthClient`) | exact (self) |
| `dashboard/src/lib/schema.ts` (REGEN) | model | CRUD | itself (CLI-canonical regen) | exact (self) |
| `dashboard/src/lib/schema-custom.ts` (NEW) | model | CRUD | `dashboard/src/lib/schema.ts` (pgTable blocks) | role-match |
| `dashboard/src/lib/email.ts` (NEW) | utility | request-response | none in dashboard (RESEARCH Pattern 4) | no analog (config-only) |
| `dashboard/src/lib/audit.ts` (NEW) | service | CRUD (insert) | `dashboard/src/lib/db.ts` (`getDb`) + operadores query | role-match |
| `dashboard/src/app/settings/operadores/actions.ts` (NEW) | controller | request-response | `dashboard/src/lib/auth.test.ts` (`auth.api.*` w/ headers) | role-match |
| `dashboard/src/app/settings/operadores/page.tsx` (EDIT) | component (RSC) | CRUD (read) | itself (real role refactor) | exact (self) |
| `dashboard/src/app/settings/page.tsx` (NEW) | component (client) | request-response | `dashboard/src/app/login/page.tsx` | role-match (exact form pattern) |
| `dashboard/src/app/reset-password/[token]/page.tsx` (NEW) | component (client) | request-response | `dashboard/src/app/login/page.tsx` + `2fa/enroll/page.tsx` | role-match |
| `dashboard/src/components/ui/{dialog,dropdown-menu,alert-dialog}.tsx` (NEW) | component | n/a | `npx shadcn add` (official registry) | generated |
| `scripts/dashboard/seed-owner.ts` (NEW) | script | batch | `scripts/dashboard/seed-admins.sh` (header/threat-model) + `db.ts` | role-match (TS not bash) |
| `dashboard/src/lib/admin-actions.test.ts` + others (NEW) | test | n/a | `dashboard/src/lib/auth.test.ts` | exact |

---

## Pattern Assignments

### `dashboard/src/lib/auth.ts` (config, request-response) — EDIT

**Analog:** itself. The `betterAuth({...})` call already wires `twoFactor`, `databaseHooks` allowlist (D-13), `hooks.before` CR-01, `rateLimit`, `session.cookieCache`. Phase 13 ADDS the `admin` plugin and `emailAndPassword.sendResetPassword`. **Do NOT remove or reorder the CR-01 hook (L127-147) or the D-13 allowlist hook (L156-168).**

**Plugin array** (`auth.ts:151` — extend, keep `twoFactor` first):
```typescript
plugins: [twoFactor({ issuer: "Ifix AI Gateway" })],
```
Becomes (per RESEARCH Pattern 1 + Pitfall 4):
```typescript
import { admin } from "better-auth/plugins"; // add alongside existing twoFactor import (L44)
// ...
plugins: [
  twoFactor({ issuer: "Ifix AI Gateway" }),
  admin({ adminRoles: ["owner"], defaultRole: "operator" }),
],
```

**`emailAndPassword` block** (currently `auth.ts:64`):
```typescript
emailAndPassword: { enabled: true, autoSignIn: false },
```
Becomes (add `sendResetPassword`; keep `enabled`/`autoSignIn`; import `mailer` from new `lib/email.ts` — RESEARCH Pattern 4):
```typescript
emailAndPassword: {
  enabled: true,
  autoSignIn: false,
  sendResetPassword: async ({ user, url }) => {
    await mailer.sendMail({
      from: "'iFix AI Gateway' <noreply@ifixtelecom.com.br>",
      to: user.email,
      subject: "Defina sua senha — iFix AI Gateway",
      text: `Acesse o link para definir sua senha: ${url}`,
    });
  },
},
```

**Existing CR-01 hook to PRESERVE verbatim** (`auth.ts:127-147`) — the `before` middleware that throws `FORBIDDEN`/`TWO_FACTOR_ALREADY_ENABLED` on `/two-factor/enable` when `user.twoFactorEnabled === true`. Reset-2FA (D-06) must NOT call enable; it clears the row so `enabled=false` and CR-01 stays inert. **Header comment block (L1-40) documents why the admin plugin was previously omitted — update that comment to reflect the Phase 13 reversal (D-01).**

**Caveat to plan around (RESEARCH Pitfall 4 / A2):** `admin({ adminRoles:["owner"] })` may warn on a custom string role not in the default `admin`/`user` access-control map. Boot-test on staging FIRST; if it errors, add a `createAccessControl` `roles` map including `owner`.

---

### `dashboard/src/lib/auth-client.ts` (config, request-response) — EDIT (optional)

**Analog:** itself. D-03 prefers server actions, so `adminClient()` is OPTIONAL — only add if a browser admin call is kept. The self-service change-password (UM-01) uses `authClient.changePassword` which is built-in (no plugin needed).

**Existing pattern** (`auth-client.ts:10-17`):
```typescript
import { twoFactorClient } from "better-auth/client/plugins";
import { createAuthClient } from "better-auth/react";

export const authClient = createAuthClient({
  plugins: [twoFactorClient()],
});
export const { signIn, signOut, signUp, useSession } = authClient;
```
If browser admin is kept, mirror the plugin-array extension:
```typescript
import { adminClient } from "better-auth/client/plugins";
// plugins: [twoFactorClient(), adminClient()]
```
`authClient.changePassword` is available WITHOUT adminClient (it is a built-in method) — the `/settings` page can import `authClient` as-is.

---

### `dashboard/src/lib/schema.ts` (model, CRUD) — REGEN ONLY

**Analog:** itself. **NEVER hand-edit** (header L1-20 + drizzle.config `schema: "./src/lib/schema.ts"`). Admin-plugin columns (`role`, `banned`, `banReason`, `banExpires` on `user`; `impersonatedBy` on `session`) arrive via the CLI-canonical workflow documented in the header:
```
1. bunx @better-auth/cli@latest generate --output src/lib/schema.ts --yes
2. bunx drizzle-kit push
```
The existing `pgTable("user", {...})` (L24-36) gains the admin columns automatically on regen. The `twoFactor` table (L98-112) + `twoFactorEnabled` column (L35) are the targets the reset-2FA op (D-06) deletes/updates.

---

### `dashboard/src/lib/schema-custom.ts` (model, CRUD) — NEW

**Analog:** `dashboard/src/lib/schema.ts` `pgTable` blocks (L24-96). The `admin_audit_log` table MUST live in a SEPARATE file (RESEARCH Pitfall 1 — the CLI regen would erase a custom table from `schema.ts`). Copy the `pgTable` + `index` idiom verbatim.

**Imports + table idiom to copy** (`schema.ts:21-36, 82-96`):
```typescript
import { pgTable, text, timestamp, jsonb, index } from "drizzle-orm/pg-core";

export const adminAuditLog = pgTable(
  "admin_audit_log",
  {
    id: text("id").primaryKey(),            // crypto.randomUUID() — matches user.id idiom
    actorId: text("actor_id").notNull(),
    actorEmail: text("actor_email").notNull(),
    targetId: text("target_id"),
    targetEmail: text("target_email"),
    action: text("action").notNull(),
    metadata: jsonb("metadata"),            // D-08 metadata column
    createdAt: timestamp("created_at").defaultNow().notNull(),
  },
  (table) => [index("admin_audit_log_actor_idx").on(table.actorId)],
);
```

**REQUIRED wiring change in `dashboard/src/lib/db.ts`** (currently `import * as schema from "./schema"`, L22 + `drizzle(pool, { schema })`, L43): spread the custom module so drizzle sees both:
```typescript
import * as authSchema from "./schema";
import * as customSchema from "./schema-custom";
const schema = { ...authSchema, ...customSchema };
```
**REQUIRED `drizzle.config.ts` change:** `schema: "./src/lib/schema.ts"` → `schema: "./src/lib/schema*.ts"` (glob) so `drizzle-kit push` migrates `admin_audit_log` too. Keep `strict: true` + the TLS/CA logic untouched.

---

### `dashboard/src/lib/email.ts` (utility, request-response) — NEW

**Analog:** none in `dashboard/` (no email module exists). Build from RESEARCH Pattern 4 verbatim. Lazy/safe-at-build idiom matches the spirit of `db.ts` (env read deferred — `createTransport` reads `process.env` at module load, acceptable since it is not invoked during `next build`). Reuse the existing global Brevo account `797fad001` (CLAUDE.md §Brevo). New env vars: `BREVO_SMTP_USER`, `BREVO_SMTP_PASS`.
```typescript
import nodemailer from "nodemailer";
export const mailer = nodemailer.createTransport({
  host: "smtp-relay.brevo.com",
  port: 587,
  secure: false, // STARTTLS on 587
  auth: { user: process.env.BREVO_SMTP_USER, pass: process.env.BREVO_SMTP_PASS },
});
```
**Install gate:** `bun add nodemailer` (9.0.0, no postinstall). RESEARCH tags nodemailer `[ASSUMED]` (slopcheck unavailable) — planner SHOULD gate behind a `checkpoint:human-verify` before install. **Use SMTP, NOT the Brevo HTTP API** (Authorised-IPs lock → 401).

---

### `dashboard/src/lib/audit.ts` (service, CRUD insert) — NEW

**Analog:** `dashboard/src/lib/db.ts` (`getDb()`, L63-65) + the drizzle insert idiom. The operadores page already demonstrates `getDb()` + `db.select(...)`; audit is the write counterpart. D-09: only admin ops write here; self-service change-password does NOT.

**`getDb()` access pattern to copy** (`db.ts:63-65`, `operadores/page.tsx:40`):
```typescript
import { getDb, schema } from "@/lib/db";
import { adminAuditLog } from "@/lib/schema-custom"; // or via spread schema

export async function writeAuditLog(args: {
  actor: { id: string; email: string };
  targetId?: string;
  targetEmail?: string;
  action: string;
  metadata?: Record<string, unknown>;
}) {
  const db = getDb();
  await db.insert(adminAuditLog).values({
    id: crypto.randomUUID(),               // matches advanced.database.generateId in auth.ts:218
    actorId: args.actor.id,
    actorEmail: args.actor.email,
    targetId: args.targetId ?? null,
    targetEmail: args.targetEmail ?? null,
    action: args.action,
    metadata: args.metadata ?? null,
  });
}
```

---

### `dashboard/src/app/settings/operadores/actions.ts` (controller, request-response) — NEW

**Analog:** `dashboard/src/lib/auth.test.ts` — the canonical `auth.api.*({ body, headers })` calling convention (L240-258, L352-356 sign-in with explicit `headers`). RESEARCH Patterns 2/3/5 build on this. **Every admin endpoint MUST forward `await headers()`** or the admin middleware rejects with FORBIDDEN (Pitfall 3).

**Owner-gate + header-forwarding idiom** (RESEARCH Pattern 2, modeled on auth.test.ts `auth.api.getSession({ headers })`):
```typescript
"use server";
import { headers } from "next/headers";
import { eq } from "drizzle-orm";
import { auth } from "@/lib/auth";
import { getDb, schema } from "@/lib/db";
import { writeAuditLog } from "@/lib/audit";

async function requireOwner() {
  const h = await headers();
  const session = await auth.api.getSession({ headers: h });
  if (!session) throw new Error("UNAUTHENTICATED");
  if ((session.user as { role?: string }).role !== "owner") {
    throw new Error("Ação restrita ao owner do dashboard."); // UI-SPEC copy
  }
  return { session, h };
}
```

**Invite (UM-04, RESEARCH Pattern 3)** — `createUser` (random pwd, server-side, no session) + `requestPasswordReset`; D-13 allowlist hook validates domain on persist:
```typescript
export async function inviteOperator(name: string, email: string) {
  const { session, h } = await requireOwner();
  const random = crypto.randomUUID() + crypto.randomUUID(); // never shown (UI-SPEC §Privacy)
  const created = await auth.api.createUser({
    body: { email, name, password: random, role: "operator" }, headers: h,
  });
  await auth.api.requestPasswordReset({
    body: { email, redirectTo: `${process.env.BETTER_AUTH_URL}/reset-password` }, headers: h,
  });
  await writeAuditLog({ actor: session.user, targetEmail: email,
    targetId: (created as { user?: { id?: string } }).user?.id, action: "operator.invite" });
}
```

**Remove (UM-05)** — revoke then remove, both with headers:
```typescript
export async function removeOperator(targetUserId: string) {
  const { session, h } = await requireOwner();
  await auth.api.revokeUserSessions({ body: { userId: targetUserId }, headers: h });
  await auth.api.removeUser({ body: { userId: targetUserId }, headers: h });
  await writeAuditLog({ actor: session.user, targetId: targetUserId, action: "operator.remove" });
}
```

**Reset password (UM-06)** — `requestPasswordReset` + `revokeUserSessions` (same email route as invite, D-07).

**Reset-2FA (UM-07, RESEARCH Pattern 5)** — NO first-party endpoint; direct drizzle on the dashboard's OWN db. Uses `schema.twoFactor` (schema.ts:98-112) + `schema.user.twoFactorEnabled` (schema.ts:35):
```typescript
export async function resetOperator2FA(targetUserId: string) {
  const { session, h } = await requireOwner();
  const db = getDb();
  await db.delete(schema.twoFactor).where(eq(schema.twoFactor.userId, targetUserId));
  await db.update(schema.user).set({ twoFactorEnabled: false })
    .where(eq(schema.user.id, targetUserId));
  await auth.api.revokeUserSessions({ body: { userId: targetUserId }, headers: h });
  await writeAuditLog({ actor: session.user, targetId: targetUserId, action: "operator.reset_2fa" });
}
```
Each action ends with `revalidatePath("/settings/operadores")`. **CR-01-safe by construction** — never calls `/two-factor/enable`.

---

### `dashboard/src/app/settings/operadores/page.tsx` (component RSC, CRUD read) — EDIT

**Analog:** itself. Server component; existing `loadOperators()` (L39-123) already runs the roster + session-stats query. Phase 13: (1) read REAL `role` column (D-02) instead of `i===0`; (2) owner-gate controls; (3) wire `+ Provisionar` → dialog, `···` → dropdown-menu; (4) update footer note.

**Existing role derivation to REPLACE** (`page.tsx:324-340` — the `i === 0 ? "owner" : "operator"` badge). The roster query (L59-61) selects `id, name, email, COALESCE(two_factor_enabled,...)`; ADD `role` to that SELECT (and to the `Operator` type L30-37):
```typescript
// Current (L60):
sql`SELECT id, name, email, COALESCE(two_factor_enabled, false) AS two_factor_enabled FROM "user" ORDER BY created_at ASC`
// Phase 13: add `role` (nullable until seed runs → treat NULL as "operator"):
sql`SELECT id, name, email, COALESCE(role, 'operator') AS role, COALESCE(two_factor_enabled, false) AS two_factor_enabled FROM "user" ORDER BY created_at ASC`
```
Badge becomes `role === 'owner'` (warning tone, L328-336) vs `operator` (neutral). **Preserve the `color-mix` badge styling + `padding: 2px 8px`** (Implementation Notes — do NOT shift to grid).

**Owner-gate** — read the viewer's role via `auth.api.getSession({ headers: await headers() })` in the RSC; if `role !== 'owner'`, hide `+ Provisionar operador` (L255-261) and every `···` trigger (L368-374). Server actions re-check (D-03 — UI hiding is cosmetic).

**`···` trigger to REPLACE** (`page.tsx:367-374`) — literal `···` text → `<MoreHorizontal />` lucide icon wrapping a `dropdown-menu`. Keep `aria-label={`Ações para ${o.name}`}`.

**Footer note to REPLACE** (`page.tsx:381-388`) — `provisionados via scripts/dashboard/seed-admins.sh (11-05)` → "operadores gerenciados pelo painel" (UI-SPEC §Visual Hierarchy; seed-admins.sh superseded).

---

### `dashboard/src/app/settings/page.tsx` (component client, request-response) — NEW

**Analog:** `dashboard/src/app/login/page.tsx` — the EXACT form pattern this page copies: `"use client"` + `useState` per field + plain `<form onSubmit>` + inline `<p className="text-xs text-destructive" role="alert">` errors + disabled-button-with-spinner pending state. Self-service change-password (UM-01) calls `authClient.changePassword` (RESEARCH Pattern 6); NOT an admin action, NOT audited (D-09), no `requireOwner`.

**Form + pending-spinner idiom to copy** (`login/page.tsx:158-205`):
```typescript
"use client";
import { useState } from "react";
import { toast } from "sonner";
import { authClient } from "@/lib/auth-client";
// ...plain <form onSubmit={handleSubmit}>, three <Input type="password"> with
//    <label className="text-xs font-semibold">, inline error <p role="alert">,
//    <Button type="submit" disabled={loading}> with the spinner span.
```
**Pending-spinner span to copy verbatim** (`login/page.tsx:193-200`):
```tsx
<span aria-hidden className="inline-block size-3.5 animate-spin rounded-full border-2 border-current border-t-transparent" />
```
**Call** (RESEARCH Pattern 6):
```typescript
const res = await authClient.changePassword({ currentPassword, newPassword, revokeOtherSessions: false });
// res.error?.code → inline "Senha atual incorreta." (UI-SPEC copy); success → toast + clear fields.
```
**Label idiom:** prefer plain `<label className="text-xs font-semibold">` (matches `2fa/enroll/page.tsx:198` + `login/page.tsx:160`) — UI-SPEC says skip the shadcn `label` block. Lives in the Settings shell as a tab/section (UI-SPEC §Inheritance — keep the 2px `--primary` active-tab indicator from `operadores/page.tsx:190-217`).

---

### `dashboard/src/app/reset-password/[token]/page.tsx` (component client, request-response) — NEW

**Analog:** `dashboard/src/app/login/page.tsx` (client form + `useSearchParams`/route-param + `authClient` call + `router.push`) and `2fa/enroll/page.tsx` (`AuthShell` + `Card` wrapper for unauthenticated surfaces). Consumes the reset token (RESEARCH Pitfall 6 — none exists today; `requestPasswordReset` builds `${BETTER_AUTH_URL}/reset-password/:token`).
```typescript
"use client";
import { authClient } from "@/lib/auth-client";
// read token from params; <form> with new-password + confirm; call:
const res = await authClient.resetPassword({ newPassword, token });
// on success → router.push("/login"); first login → middleware routes to /2fa/enroll.
```
Wrap in `<AuthShell>` + `<Card className="w-full max-w-sm">` like `2fa/enroll/page.tsx:174-175`. **Confirm `BETTER_AUTH_URL=https://ai-dashboard.converse-ai.app`** in the prod container env (Pitfall 6 / A4) before shipping invites.

---

### `dashboard/src/components/ui/{dialog,dropdown-menu,alert-dialog}.tsx` (component) — NEW (shadcn add)

**Analog:** the official shadcn registry (`components.json` → `"registries": {}`, `"style": "radix-nova"`). Generated source, not hand-written:
```bash
cd dashboard && npx shadcn add dialog dropdown-menu alert-dialog
```
Existing `dashboard/src/components/ui/` already holds `card, input, button, table, badge, sonner, separator, tooltip` — the new blocks slot in alongside. `dialog` = provision modal; `dropdown-menu` = `···` row menu; `alert-dialog` = destructive confirms (UI-SPEC §Component Inventory). Do NOT install `form` or `label` (UI-SPEC). Record the slopcheck/install decision in plan evidence (UI-SPEC §Registry Safety).

---

### `scripts/dashboard/seed-owner.ts` (script, batch) — NEW

**Analog:** `scripts/dashboard/seed-admins.sh` (header/threat-model/idempotency philosophy, L1-78) for the operational contract + `dashboard/src/lib/db.ts` (`getDb`) for the drizzle access. RESEARCH Pattern 7: one-shot idempotent backfill — earliest user → `owner`, rest → `operator`. Run AFTER `drizzle-kit push` (coupled ordering — without it no owner exists and the admin gate locks everyone out, RESEARCH Runtime State).

**Idempotent SQL to embed** (RESEARCH Pattern 7):
```sql
UPDATE "user" SET role = 'owner'
WHERE id = (SELECT id FROM "user" ORDER BY created_at ASC LIMIT 1)
  AND NOT EXISTS (SELECT 1 FROM "user" WHERE role = 'owner');
UPDATE "user" SET role = 'operator' WHERE role IS NULL;
```
Run via `bun run scripts/dashboard/seed-owner.ts` using `getDb()` + `db.execute(sql`...`)` (mirror `operadores/page.tsx:59` raw-SQL execute idiom). Reuse the seed-admins.sh principles: idempotent, no password/secret to stdout, fail-fast on missing `DASHBOARD_DATABASE_URL`.

---

### Test files (test) — NEW

**Analog:** `dashboard/src/lib/auth.test.ts` — the `buildTestAuth()` + `memoryAdapter` integration harness (L46-145) that boots a real `betterAuth({...})` mirroring `auth.ts` and exercises `auth.api.*`. RESEARCH §Validation: EXTEND this harness for admin/reset flows.
- `src/lib/admin-actions.test.ts` — owner-gate FORBIDDEN for non-owner + audit-write assertion (UM-04/05/06, D-09); mock the `mailer` transport.
- `src/lib/seed-owner.test.ts` — idempotency (UM-03).
- `src/app/settings/page.test.tsx` — mirror `src/app/login/page.test.tsx` mocking of `authClient` (UM-01).
- `src/app/settings/operadores/page.test.tsx` — real role badge + owner-gate (UM-10).
- Extend `src/lib/auth.test.ts` — admin-plugin boot (UM-02) + reset-2FA CR-01 invariant (UM-07), reusing the `parseTotpSecret`/`createOTP` helpers (L164-169) and the CR-01 test (L430-494).

---

## Shared Patterns

### Owner authorization gate (D-03)
**Source:** RESEARCH Pattern 2, modeled on `dashboard/src/lib/auth.test.ts` `auth.api.getSession({ headers })` (L256-258).
**Apply to:** every export in `settings/operadores/actions.ts` (NOT the self-service `/settings` page).
```typescript
const h = await headers();
const session = await auth.api.getSession({ headers: h });
if ((session?.user as { role?: string })?.role !== "owner")
  throw new Error("Ação restrita ao owner do dashboard.");
```

### Header forwarding to `auth.api` (Pitfall 3)
**Source:** `auth.test.ts:241-248, 352-356` — `signInEmail({ body, headers })`.
**Apply to:** every `auth.api.createUser/removeUser/revokeUserSessions/requestPasswordReset` call. Omitting `headers` → silent FORBIDDEN.

### Audit write (D-08/D-09)
**Source:** new `lib/audit.ts` over `getDb()` (`db.ts:63-65`).
**Apply to:** every admin action in `actions.ts` AFTER the op succeeds. NOT applied to `/settings` change-password.

### Client form + inline error + pending spinner
**Source:** `dashboard/src/app/login/page.tsx:158-205`.
**Apply to:** `settings/page.tsx`, `reset-password/[token]/page.tsx`, the provision-dialog form, all change-password/confirm forms.
```tsx
{error && <p className="text-xs text-destructive" role="alert">{error}</p>}
<Button type="submit" disabled={loading}>{loading ? "…" : "Confirmar"}</Button>
```

### Plain `<label>` (no shadcn label block)
**Source:** `login/page.tsx:160`, `2fa/enroll/page.tsx:198` — `<label htmlFor="x" className="text-xs font-semibold">`.
**Apply to:** all Phase 13 form fields (UI-SPEC prefers this over the `label` block).

### Drizzle access via `getDb()` (NEVER touch ai_gateway)
**Source:** `db.ts:63-65` + `operadores/page.tsx:40`.
**Apply to:** `audit.ts`, reset-2FA direct ops in `actions.ts`, `seed-owner.ts`. Dashboard DB (`bd_ai_dashboard_prod`/`public`) is strictly isolated from `ai_gateway` (07-RESEARCH Pitfall 7).

### CLI-canonical schema (never hand-edit `schema.ts`)
**Source:** `schema.ts:1-20` header + `drizzle.config.ts` `schema` field.
**Apply to:** admin-plugin columns (regen `schema.ts`); `admin_audit_log` goes in `schema-custom.ts` (Pitfall 1) with the drizzle.config `schema` glob + `db.ts` spread.

### Privacy / redaction (MANDATORY, inherited)
**Source:** `operadores/page.tsx:18-23` header + UI-SPEC §Privacy.
**Apply to:** every Phase 13 surface. NEVER render TOTP secrets, backup codes, password hashes, the random invite password, IPs, cookies, raw UUIDs. Invite/reset deliver a LINK only.

---

## No Analog Found

| File | Role | Data Flow | Reason |
|------|------|-----------|--------|
| `dashboard/src/lib/email.ts` | utility | request-response | No email module exists in `dashboard/`. Build from RESEARCH Pattern 4 (nodemailer Brevo SMTP). Config-only file; closest spiritual analog is `db.ts` lazy-env idiom. |

Reset-2FA has no first-party Better Auth endpoint (RESEARCH §Don't Hand-Roll "Key insight") — but its IMPLEMENTATION analog (direct `db.delete`/`db.update` via `getDb()` + `schema.twoFactor`/`schema.user`) exists in the repo, so it is classified as role-match above, not "no analog".

---

## Metadata

**Analog search scope:** `dashboard/src/lib/`, `dashboard/src/app/` (settings, login, 2fa, api/auth), `dashboard/src/components/`, `scripts/dashboard/`, `dashboard/drizzle.config.ts`, `dashboard/package.json`.
**Files scanned:** auth.ts, auth-client.ts, db.ts, allowlist.ts, schema.ts, operadores/page.tsx, 2fa/enroll/page.tsx, login/page.tsx, auth.test.ts, api/auth/[...all]/route.ts, audit-table.tsx, drizzle.config.ts, seed-admins.sh, package.json (14 files).
**Pattern extraction date:** 2026-06-15
