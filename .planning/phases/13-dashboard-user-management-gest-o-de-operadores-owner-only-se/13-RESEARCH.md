# Phase 13: dashboard-user-management — Research

**Researched:** 2026-06-15
**Domain:** Better Auth `admin` plugin + native password-reset + TOTP reset, on a standalone Better Auth 1.4.22 instance (Next.js 15 App Router, Drizzle/Postgres), Brevo SMTP via nodemailer, owner-gated Server Actions.
**Confidence:** HIGH (every API claim verified against the installed `node_modules/better-auth@1.4.22` typings/runtime + official docs via Context7; infra reachability verified live over SSH).

<user_constraints>
## User Constraints (from CONTEXT.md)

### Locked Decisions
- **D-01:** Install the Better Auth `admin` plugin. It provides the `role` column + ready endpoints `createUser` / `removeUser` / `setUserPassword` / `revokeUserSessions` / `setRole`. **Do NOT expose** `banUser` / `impersonateUser` in the UI. Codebase deliberately omitted this plugin before (`auth.ts` header + `seed-admins.sh`); Phase 13 reverses that.
- **D-02:** Owner = explicit `role='owner'` persisted in DB (NOT `created_at` ASC / `i===0`). 1st existing operator gets `owner` via one-shot seed/migration; rest = `operator`. `operadores/page.tsx` must read the REAL `role` column.
- **D-03:** Owner-gating of the 4 ops via Next.js Server Actions — each op re-validates session + `role==='owner'` server-side before calling `auth.api`. Server-side enforcement is mandatory (NOT just UI). Middleware stays as an additional layer. No `/api/admin/*` route handlers (Phase 13 is browser-only).
- **D-04:** Invite by email with link, delivered via Brevo **SMTP** (nodemailer — NOT the Brevo API, which has Authorised IPs on; SMTP is unaffected). Owner enters name + email; operator receives link, sets own password via token, then enrolls 2FA on first login. Allowlist `@ifixtelecom.com.br` (D-13 `auth.ts` databaseHooks) keeps validating server-side.
- **D-05:** Reuse native Better Auth `requestPasswordReset` / `resetPassword` with `sendResetPassword` hook: admin `createUser` with a random disposable password + dispatch a set-password email via token. Researcher validates viability (VALIDATED — see §Architecture Patterns).
- **D-06:** Reset-2FA = clear + re-enroll on login. Owner-only server action: delete the target's `two_factor` row + set `two_factor_enabled=false` + revoke their sessions. Next login routes them to `/2fa/enroll` (legitimate un-enrolled state). CR-01 (`auth.ts:127-147`) stays intact — `/two-factor/enable` is only allowed when `enabled=false`, so reset does NOT reopen the credential-rotation vector. Audit-logged.
- **D-07:** Reset password = same route as invite — owner triggers reset → operator gets Brevo email with set-password link (reuses D-04/D-05) + revoke target's sessions. Operator sets own new password.
- **D-08:** New table `admin_audit_log` in `bd_ai_dashboard_prod` (schema `public`) — columns: `actor_id`, `actor_email`, `target_id`, `target_email`, `action`, `created_at`, `metadata`. Same drizzle connection as the dashboard; isolated from `ai_gateway` (07-RESEARCH Pitfall 7). CLI-canonical-aware (see §Pitfall — schema.ts regen).
- **D-09:** Audited scope = every admin action: create/invite, remove, reset-password, reset-2FA + associated session revocations. Do NOT log the operator's own self-service change-password.

### Claude's Discretion
- Exact layout/UX of the self-service `/settings` page and the operators "···" modal/menu — follow UI-SPEC v2 §Settings (already referenced in `operadores/page.tsx`) and the existing visual pattern.
- Rate-limit on admin server actions (if needed) — planner decides, aligned with the `rateLimit` already configured in `auth.ts`.
- Detail of the admin-plugin migration scheme over the existing base (generate + drizzle-kit push) — follow the CLI-canonical workflow documented in `schema.ts`.

### Deferred Ideas (OUT OF SCOPE)
- Audit-log VIEWER UI ("Auditoria" tab) — table is created now; the read tab is a future phase.
- Granular RBAC beyond owner/operator — out of scope.
- Backup-code rotation/regeneration via UI — related to 2FA but not requested this phase.
</user_constraints>

<phase_requirements>
## Phase Requirements

REQUIREMENTS.md does not yet carry Phase-13 IDs (ROADMAP line 101: "Requirements: TBD — derivar no plan-phase"). Suggested IDs derived from scope (planner may adopt verbatim):

| ID | Description | Research Support |
|----|-------------|------------------|
| UM-01 | Self-service change-password (`authClient.changePassword`, requires current password) on `/settings` | Built-in `/change-password` endpoint exists in 1.4.22 (`api/routes/update-user.mjs`); body `currentPassword`, `newPassword`, optional `revokeOtherSessions`. Client method `authClient.changePassword`. |
| UM-02 | Install `admin` plugin with `adminRoles:["owner"]`; add `role`/`banned`/`banReason`/`banExpires` columns via CLI-canonical regen | Plugin present in installed pkg; schema columns confirmed (§Standard Stack). |
| UM-03 | One-shot idempotent seed: 1st existing operator → `role='owner'`, rest → `operator` | `setRole` endpoint OR direct drizzle UPDATE; idempotency pattern §Architecture. |
| UM-04 | Owner-gated Server Action: create/invite operator (`admin.createUser` random pwd + `requestPasswordReset` email via Brevo) | `admin.createUser` runs server-side without session; `sendResetPassword` callback signature verified. |
| UM-05 | Owner-gated Server Action: remove operator + revoke all their sessions | `admin.removeUser({userId})` cascades; `revokeUserSessions({userId})`. |
| UM-06 | Owner-gated Server Action: reset operator password (email link + revoke sessions) | `requestPasswordReset` + `revokeUserSessions`. |
| UM-07 | Owner-gated Server Action: reset operator 2FA (clear `two_factor` row + `two_factor_enabled=false` + revoke sessions), CR-01 intact | direct drizzle delete + update + `revokeUserSessions` (§Architecture Pattern 5). |
| UM-08 | `admin_audit_log` table; every admin op writes a row | custom (non-better-auth) table addition to schema.ts (§Pitfall 1). |
| UM-09 | Brevo SMTP via nodemailer wired into `sendResetPassword`; container reachability confirmed | nodemailer 9.0.0; container→`smtp-relay.brevo.com:587` OPEN (verified live). |
| UM-10 | `operadores/page.tsx` reads real `role`; owner-gate hides controls for non-owners | replace `i===0` with `role` from user table. |
</phase_requirements>

## Summary

The installed package is **Better Auth 1.4.22** (verified: `bun.lock` + `node_modules/better-auth/package.json`). The `admin` plugin ships inside this exact version (`node_modules/better-auth/dist/plugins/admin/`). It exposes every endpoint D-01 requires — `createUser`, `removeUser`, `setUserPassword`, `setRole`, `revokeUserSessions` (plus `revokeUserSession`, `listUsers`, `updateUser`, `getUser`) — and adds exactly `role`, `banned`, `banReason`, `banExpires` to the user table and `impersonatedBy` to the session table. The 4 required ops are fully covered; `banUser` / `impersonateUser` are simply never wired into the UI/server-actions (no UI suppression flag needed — if you don't call them and don't expose a UI affordance, they remain inert API routes gated by `adminRoles`).

The native password-reset flow (`requestPasswordReset` → `sendResetPassword({user,url,token})` → `resetPassword({newPassword,token})`) is present and is the correct primitive for both invite (D-04/D-05) and reset (D-07). It requires wiring `emailAndPassword.sendResetPassword` in `auth.ts` (NOT currently configured — confirmed absent). Invite = `admin.createUser` (runs server-side **without a session**) with a random disposable password, immediately followed by `requestPasswordReset` to dispatch the set-password link. nodemailer is NOT yet a dashboard dependency; the dashboard container (`ifix-ai-dashboard` on n8n-ia-vm) **does** reach `smtp-relay.brevo.com:587` (verified live: `CONTAINER-NC-OPEN`).

Reset-2FA (D-06) has no first-party admin endpoint — Better Auth's `/two-factor/disable` requires the **target's own password** (unavailable to an owner). So reset-2FA is a direct drizzle operation against the dashboard's own DB: delete the `two_factor` row, set `user.two_factor_enabled=false`, and `revokeUserSessions`. This is CR-01-safe by construction — CR-01 blocks `/two-factor/enable` only when `enabled=true`; after the reset `enabled=false`, so the operator's next login legitimately routes to `/2fa/enroll`.

**Primary recommendation:** Add `admin({ adminRoles: ["owner"], defaultRole: "operator" })` to `auth.ts`, wire `emailAndPassword.sendResetPassword` to a nodemailer Brevo transport, regenerate `schema.ts` via `bunx @better-auth/cli generate` + `bunx drizzle-kit push`, seed `role='owner'` on the 1st operator, and implement all 4 ops as owner-gated Server Actions that call `auth.api.*` with the request headers (the auth.ts/auth.test.ts established pattern). Reset-2FA uses direct drizzle. Every admin op writes `admin_audit_log`.

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Owner authorization gate (role==='owner') | API/Backend (Server Action calling `auth.api`) | Frontend Server (middleware) | D-03: server-side enforcement mandatory; UI hiding cosmetic only |
| Create/invite operator | API/Backend (`admin.createUser` + `requestPasswordReset`) | — | `createUser` runs server-side without session; email dispatch is server work |
| Remove operator + revoke sessions | API/Backend (`admin.removeUser`/`revokeUserSessions`) | Database (cascade delete) | session/user rows; FK cascade in schema |
| Reset operator password | API/Backend (`requestPasswordReset` + `revokeUserSessions`) | Email (Brevo SMTP) | token issued server-side; link delivered via SMTP |
| Reset operator 2FA | API/Backend (direct drizzle delete/update + `revokeUserSessions`) | Database | no first-party admin endpoint; DB-level op on dashboard's own DB |
| Self-service change-password | API/Backend (`auth.api.changePassword` / client) | — | requires current password; operator-scoped, not admin |
| Audit logging | API/Backend (drizzle insert) | Database (`admin_audit_log`) | written inside each server action, same connection |
| Role badge / roster display | Frontend Server (server component query) | Database | `operadores/page.tsx` is a server component reading the user table |
| Invite/reset email delivery | API/Backend (nodemailer) | CDN/External (Brevo SMTP relay) | SMTP relay is an external boundary |

## Standard Stack

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `better-auth` | 1.4.22 (installed, pinned `~1.4.18`) | auth + `admin` plugin + native reset/change-password + twoFactor | already the dashboard's auth instance; admin plugin ships in this exact version `[VERIFIED: node_modules/better-auth@1.4.22]` |
| `better-auth/plugins` → `admin` | (bundled in 1.4.22) | `role` column + createUser/removeUser/setUserPassword/setRole/revokeUserSessions | D-01 `[VERIFIED: dist/plugins/admin/{schema,admin}.mjs]` |
| `better-auth/client/plugins` → `adminClient` | (bundled) | browser-side admin calls (optional — server actions preferred per D-03) | `[VERIFIED: dist/plugins/admin/client.mjs]` |
| `nodemailer` | 9.0.0 (latest) | Brevo SMTP transport for invite/reset links | de-facto Node SMTP library; CONTEXT.md D-04 names it `[CITED: npmjs.com/package/nodemailer]` `[ASSUMED — slopcheck unavailable; see Package Legitimacy Audit]` |
| `drizzle-orm` | 0.45.0 (installed) | reset-2FA direct ops + `admin_audit_log` insert + roster query | already in use `[VERIFIED: package.json]` |
| `drizzle-kit` | 0.30.0 (installed) | `push` migration after CLI generate | already in use `[VERIFIED: package.json]` |

### Supporting
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `@types/nodemailer` | 8.0.1 | TS types for nodemailer | devDependency (nodemailer 9 may ship its own types — check `nodemailer/lib/...d.ts` before adding; prefer NOT adding if bundled) `[CITED: npmjs.com/package/@types/nodemailer]` |
| `@better-auth/cli` | latest (via `bunx`) | regenerate `schema.ts` from plugin config | run once per schema change (CLI-canonical workflow) `[VERIFIED: schema.ts header]` |

### Net-new shadcn UI blocks (from official registry — UI-SPEC §Component Inventory)
| Block | Decision | Source |
|-------|----------|--------|
| `dialog` | INSTALL — provision modal | shadcn official registry (UI-SPEC) |
| `dropdown-menu` | INSTALL — row `···` menu | shadcn official registry (UI-SPEC) |
| `alert-dialog` | INSTALL — destructive confirms | shadcn official registry (UI-SPEC) |
| `label` | prefer plain `<label>` (matches `2fa/enroll`) — no new dep | UI-SPEC |
| `form` | DO NOT INSTALL — plain `<form>`+`useState`+server actions | UI-SPEC |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| `admin` plugin | Plain `role` text column + hand-rolled authz | D-01 explicitly chose the plugin; hand-rolling re-implements 5 endpoints + permission gate that the plugin already ships and tests. Plugin is the locked decision. |
| native `requestPasswordReset` for invite | `organization` plugin invite / custom invite table | D-05 explicitly rejects inventing an invite table; native reset token reuses existing `verification` table. |
| `admin.setUserPassword` for reset | email-link reset (`requestPasswordReset`) | D-07 chose the email-link path (operator sets own password); `setUserPassword` would require the owner to choose + transmit a password (privacy rule forbids showing it in UI). `setUserPassword` is NOT used for reset. |
| nodemailer SMTP | Brevo HTTP API (`api.brevo.com/v3`) | Brevo API has Authorised-IPs ON → `401 unrecognised IP` for non-allowlisted IPs (CLAUDE.md global §Brevo); SMTP is unaffected. D-04 locks SMTP. |
| `adminClient()` browser calls | Server Actions calling `auth.api` | D-03 mandates server-side enforcement; server actions are the chosen path. `adminClient` is optional/unused. |

**Installation:**
```bash
cd dashboard
bun add nodemailer
# @types/nodemailer ONLY if nodemailer 9 does not ship bundled types (verify first)
bun add -d @types/nodemailer
npx shadcn add dialog dropdown-menu alert-dialog
```

**Version verification (performed 2026-06-15):**
- `better-auth`: `node_modules/better-auth/package.json` → `"version": "1.4.22"`; `bun.lock` → `better-auth@1.4.22`. `[VERIFIED: lockfile + node_modules]`
- `nodemailer`: `npm view nodemailer version` → `9.0.0`; `dist-tags.latest` → `9.0.0`; `scripts.postinstall` → empty (none). `[VERIFIED: npm registry]`
- `@types/nodemailer`: `npm view @types/nodemailer version` → `8.0.1`. `[VERIFIED: npm registry]`

## Package Legitimacy Audit

> slopcheck was NOT available at research time (`pip install slopcheck` not run; `command -v slopcheck` → not found). Per protocol, the one net-new external package is tagged `[ASSUMED]` and the planner SHOULD gate its install behind a `checkpoint:human-verify` task — though nodemailer is the de-facto standard Node SMTP library named explicitly in the project's own CONTEXT.md/CLAUDE.md, which materially lowers risk.

| Package | Registry | Age | Downloads | Source Repo | slopcheck | Disposition |
|---------|----------|-----|-----------|-------------|-----------|-------------|
| `nodemailer` | npm | ~15 yrs (v9 current) | very high (top-tier Node email lib) | github.com/nodemailer/nodemailer | unavailable | Approved — `[ASSUMED]`; no postinstall script (verified); named in CONTEXT.md D-04 |
| `@types/nodemailer` | npm | DefinitelyTyped | high | github.com/DefinitelyTyped/DefinitelyTyped | unavailable | Conditional — only if nodemailer 9 lacks bundled types |

**Packages removed due to slopcheck [SLOP] verdict:** none
**Packages flagged as suspicious [SUS]:** none

shadcn blocks (`dialog`, `dropdown-menu`, `alert-dialog`) are copied source from the official shadcn registry (`components.json` → `"registries": {}`, no third-party) — not npm installs; registry vetting gate N/A per UI-SPEC §Registry Safety.

## Architecture Patterns

### System Architecture Diagram

```
                          ┌─────────────────── Browser (operator) ───────────────────┐
                          │  /settings  (self-service change-password)                │
                          │  /settings/operadores  (owner admin surface)             │
                          └───────────────┬───────────────────────────┬──────────────┘
                                          │ <form action={serverAction}>
                                          ▼                            ▼
          ┌─────────── Next.js 15 Server (App Router, RSC + Server Actions) ──────────┐
          │                                                                            │
          │  middleware.ts ── session+2FA gate (unchanged; additional layer, D-03)    │
          │                                                                            │
          │  Server Action (owner op)                Server Action (self-service)      │
          │   1. getSession via auth.api              auth.api.changePassword(         │
          │      .getSession({headers})                 {currentPassword,newPassword}, │
          │   2. assert session.user.role==='owner'     {headers})  ── UM-01           │
          │   3. call auth.api.<op>({body},{headers})                                  │
          │   4. INSERT admin_audit_log (drizzle)                                      │
          │   5. revalidatePath('/settings/operadores')                               │
          └───────┬───────────────────────────────┬───────────────────┬──────────────┘
                  │ auth.api.createUser /           │ direct drizzle    │ nodemailer
                  │ removeUser / revokeUserSessions  │ (reset-2FA D-06)  │ (sendResetPassword)
                  ▼                                  ▼                   ▼
          ┌──────────────────────────────┐  ┌──────────────────┐  ┌──────────────────┐
          │ Better Auth internalAdapter  │  │ DELETE two_factor│  │ smtp-relay.brevo │
          │ → Drizzle → Postgres         │  │ UPDATE user      │  │ .com:587  (SMTP) │
          │ user/session/account/        │  │ (two_factor_     │  │ FROM noreply@    │
          │ verification/two_factor      │  │  enabled=false)  │  │ ifixtelecom...   │
          │ + admin cols (role,banned…)  │  └──────────────────┘  └──────────────────┘
          │ + admin_audit_log (custom)   │
          │  bd_ai_dashboard_prod/public │  (ISOLATED from ai_gateway — 07-RESEARCH P7)
          └──────────────────────────────┘
```

Primary use case (invite an operator) traced: owner submits name+email → server action asserts owner → `auth.api.createUser({email,name,password:<random>,role:"operator"})` (D-13 hook validates domain) → `auth.api.requestPasswordReset({email,redirectTo})` → `sendResetPassword` fires nodemailer → operator clicks link → `/reset-password/:token` page → `authClient.resetPassword({newPassword,token})` → first login → middleware routes to `/2fa/enroll`.

### Recommended Project Structure (net-new + edits)
```
dashboard/src/
├── lib/
│   ├── auth.ts                 # EDIT: + admin({adminRoles:["owner"],defaultRole:"operator"}); + emailAndPassword.sendResetPassword
│   ├── auth-client.ts          # EDIT (optional): + adminClient() — only if any browser admin call is kept; D-03 prefers server actions
│   ├── schema.ts               # REGEN: + admin cols on user; + admin_audit_log (custom block, re-add after generate — Pitfall 1)
│   ├── email.ts                # NEW: nodemailer Brevo transport (createTransport smtp-relay.brevo.com:587)
│   └── audit.ts                # NEW: writeAuditLog(actor,target,action,metadata) drizzle insert
├── app/
│   ├── settings/
│   │   ├── page.tsx            # NEW: self-service change-password (Surface A)
│   │   └── operadores/
│   │       ├── page.tsx        # EDIT: real role (D-02); owner-gate; activate buttons
│   │       └── actions.ts      # NEW: "use server" — 4 owner-gated admin ops
│   └── reset-password/
│       └── [token]/page.tsx    # NEW: set-password page (authClient.resetPassword)  OR  /reset-password?token=
└── components/ui/
    ├── dialog.tsx              # NEW (shadcn add)
    ├── dropdown-menu.tsx       # NEW (shadcn add)
    └── alert-dialog.tsx        # NEW (shadcn add)
```

### Pattern 1: `admin` plugin server config (D-01, D-02 role naming)
**What:** Register the admin plugin with `adminRoles:["owner"]` so the owner role passes the admin permission gate, and `defaultRole:"operator"` so created users default to operator.
**When to use:** `auth.ts` plugins array (alongside existing `twoFactor`).
**Why critical:** `adminRoles` defaults to `["admin"]` (verified `admin.mjs:17`). With D-02's `role='owner'`, the owner would FAIL the admin gate unless `adminRoles` includes `"owner"`. The admin gate uses `hasPermission({ role: session.user.role, ... })`.
```typescript
// Source: Context7 /better-auth/better-auth admin.ts + installed admin.mjs:17,
//         "adminRoles: options?.adminRoles ?? ['admin']", "role: options?.defaultRole ?? 'user'"
import { admin } from "better-auth/plugins";
// ...
plugins: [
  twoFactor({ issuer: "Ifix AI Gateway" }),
  admin({
    adminRoles: ["owner"],     // owner passes the admin permission gate
    defaultRole: "operator",   // createUser without explicit role → "operator"
  }),
],
```
**Schema columns added by the plugin (VERIFIED `dist/plugins/admin/schema.mjs`):**
- `user`: `role` (string, optional, `input:false`), `banned` (boolean, default false), `banReason` (string), `banExpires` (date)
- `session`: `impersonatedBy` (string, optional)

### Pattern 2: Owner-gated Server Action calling `auth.api` (D-03)
**What:** Every admin op re-reads the session server-side, asserts `role==='owner'`, then calls `auth.api.*` forwarding the incoming request headers.
**When to use:** `settings/operadores/actions.ts` (all 4 ops).
```typescript
// Source: dashboard/src/lib/auth.test.ts (auth.api.* with explicit headers),
//         Context7 server-side admin API usage
"use server";
import { headers } from "next/headers";
import { auth } from "@/lib/auth";
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

export async function removeOperator(targetUserId: string) {
  const { session, h } = await requireOwner();
  // revoke first so audit reflects the full effect, then remove
  await auth.api.revokeUserSessions({ body: { userId: targetUserId }, headers: h });
  await auth.api.removeUser({ body: { userId: targetUserId }, headers: h });
  await writeAuditLog({ actor: session.user, targetId: targetUserId, action: "operator.remove" });
}
```
**Note on header forwarding:** the admin endpoints run `adminMiddleware`, which reads the caller's session from the forwarded headers to authorize. Without `headers: h` the call has no session and is rejected. This matches the `auth.api.getSession({ headers })` / `signInEmail({ body, headers })` pattern already used in `auth.test.ts`.

### Pattern 3: Invite operator (D-04/D-05) — createUser + requestPasswordReset
**What:** Create the user server-side with a random throwaway password, then dispatch a set-password email.
```typescript
// Source: Context7 admin createUser ("creating users server-side without a session";
//         body {email, password?, name, role?, data?}) + requestPasswordReset endpoint
export async function inviteOperator(name: string, email: string) {
  const { session, h } = await requireOwner();
  // D-13 allowlist hook in auth.ts validates the domain server-side before persist.
  const random = crypto.randomUUID() + crypto.randomUUID(); // disposable, never shown
  const created = await auth.api.createUser({
    body: { email, name, password: random, role: "operator" },
    headers: h,
  });
  await auth.api.requestPasswordReset({
    body: { email, redirectTo: `${process.env.BETTER_AUTH_URL}/reset-password` },
    headers: h,
  });
  await writeAuditLog({ actor: session.user, targetEmail: email,
    targetId: (created as { user?: { id?: string } }).user?.id, action: "operator.invite" });
}
```
**Privacy:** the random password is never returned to the UI; the operator sets their own via the reset link (UI-SPEC §Privacy).

### Pattern 4: sendResetPassword via nodemailer Brevo SMTP (D-04, UM-09)
**What:** Wire the `emailAndPassword.sendResetPassword` callback (currently ABSENT in `auth.ts`) to a nodemailer Brevo transport. `requestPasswordReset` throws `RESET_PASSWORD_DISABLED` if this is not set.
```typescript
// Source: Context7 email-password.mdx sendResetPassword({user,url,token}, request)
//         + CLAUDE.md global §Brevo (smtp-relay.brevo.com:587, FROM noreply@ifixtelecom.com.br)
// lib/email.ts
import nodemailer from "nodemailer";
export const mailer = nodemailer.createTransport({
  host: "smtp-relay.brevo.com",
  port: 587,
  secure: false, // STARTTLS on 587
  auth: { user: process.env.BREVO_SMTP_USER, pass: process.env.BREVO_SMTP_PASS },
});
// auth.ts emailAndPassword:
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
**Reachability:** `ifix-ai-dashboard` container → `smtp-relay.brevo.com:587` = OPEN (verified live `nc -zw5`). The dashboard runs on n8n-ia-vm; the Brevo SMTP credentials are the existing account `797fad001` (CLAUDE.md). Brevo SMTP is NOT affected by the Authorised-IPs lock (that lock only blocks the HTTP API). New env vars needed: `BREVO_SMTP_USER`, `BREVO_SMTP_PASS` (wire in the dashboard stack on n8n-ia-vm).

### Pattern 5: Reset-2FA (D-06) — direct drizzle, CR-01-safe
**What:** No first-party admin endpoint disables another user's 2FA (`/two-factor/disable` requires the TARGET's own password — unusable by an owner). Do it directly on the dashboard's own DB.
```typescript
// Source: dist/.../two-factor disable requires target password (Context7);
//         schema.ts two_factor table + user.two_factor_enabled column
import { eq } from "drizzle-orm";
import { getDb, schema } from "@/lib/db";
export async function resetOperator2FA(targetUserId: string) {
  const { session, h } = await requireOwner();
  const db = getDb();
  await db.delete(schema.twoFactor).where(eq(schema.twoFactor.userId, targetUserId));
  // two_factor_enabled lives on the user table (column added by twoFactor plugin).
  // schema.ts DOES declare it (twoFactorEnabled boolean) → use drizzle update.
  await db.update(schema.user)
    .set({ twoFactorEnabled: false })
    .where(eq(schema.user.id, targetUserId));
  await auth.api.revokeUserSessions({ body: { userId: targetUserId }, headers: h });
  await writeAuditLog({ actor: session.user, targetId: targetUserId, action: "operator.reset_2fa" });
}
```
**Why CR-01 stays intact:** CR-01 (`auth.ts:127-147`) rejects `/two-factor/enable` only when `user.twoFactorEnabled === true`. After this reset `twoFactorEnabled=false`, so the operator's next login passes the middleware enrollment gate to `/2fa/enroll` legitimately, and `/two-factor/enable` is permitted because `enabled=false`. The reset does NOT call `/two-factor/enable` and does NOT touch the CR-01 hook. Cross-check `gateway/docs/RUNBOOK-2FA-RECOVERY.md` for consistency — this UI op is the audited, separation-of-duty replacement for the manual SQL described there.
**Session-revocation propagation caveat:** `cookieCache.maxAge=1800` means a revoked session can still satisfy the Edge middleware for up to 30 min (auth.ts WR-01 comment / RUNBOOK-INCIDENTS class 4). The audit + revoke are immediate at the DB level, but the locked-out operator's *current* tab may keep working up to 30 min. Surface this in the confirm-dialog copy if precision matters; otherwise document in the runbook.

### Pattern 6: Self-service change-password (UM-01)
```typescript
// Source: Context7 + installed api/routes/update-user.mjs "/change-password"
//         body: currentPassword, newPassword, revokeOtherSessions?
// Client (in a "use client" form on /settings):
import { authClient } from "@/lib/auth-client";
const res = await authClient.changePassword({
  currentPassword, newPassword, revokeOtherSessions: false,
});
// res.error?.code distinguishes wrong-current-password for inline error (UI-SPEC copy).
```
This is NOT an admin action — not audited (D-09). No `requireOwner`.

### Pattern 7: Owner seed / one-shot migration (D-02, UM-03)
**What:** Set `role='owner'` on the 1st existing operator (by `created_at ASC`), `operator` on the rest. Idempotent.
**Recommended:** a small TS script (or drizzle migration step) run once post-`drizzle-kit push`, NOT an `i===0` UI inference.
```sql
-- Idempotent: only promotes the single earliest user if NO owner exists yet.
UPDATE "user" SET role = 'owner'
WHERE id = (SELECT id FROM "user" ORDER BY created_at ASC LIMIT 1)
  AND NOT EXISTS (SELECT 1 FROM "user" WHERE role = 'owner');
UPDATE "user" SET role = 'operator' WHERE role IS NULL;
```
Run via `bunx drizzle-kit` data step or a one-shot `bun run scripts/dashboard/seed-owner.ts`. The admin plugin's `role` column is nullable with no default, so existing rows are `NULL` until backfilled — the second UPDATE normalizes them to `operator`. `operadores/page.tsx` then reads `role` directly.

### Anti-Patterns to Avoid
- **Calling `auth.api.*` admin endpoints without `headers`** — the admin middleware can't read the caller session → FORBIDDEN. Always forward `await headers()`.
- **Using `setUserPassword` for the reset flow** — would force the owner to pick + transmit a password (privacy violation). Use the email-link `requestPasswordReset` path (D-07).
- **Editing `schema.ts` by hand and expecting it to survive** — the next `bunx @better-auth/cli generate` overwrites it. The `admin_audit_log` custom block MUST be re-added after each regen OR kept in a separate non-regenerated schema file imported into the drizzle client (see Pitfall 1).
- **Trusting UI hiding for authz** — D-03: the `+ Provisionar` / `···` hidden-for-non-owner UI is cosmetic; the server action MUST re-check.
- **Wiring `adminRoles` to the default `["admin"]`** — D-02 uses `owner`; the owner would be denied. Set `adminRoles:["owner"]`.
- **Using the Brevo HTTP API** — blocked by Authorised-IPs (401). SMTP only.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Role column + admin authz | custom `role` text column + bespoke permission checks on each action | `admin` plugin (`adminRoles`, `hasPermission`) | D-01; plugin ships + tests 5 endpoints and the gate; hand-rolling re-creates a tested security surface |
| Invite token table | custom invite table + token generation/expiry | native `requestPasswordReset` (reuses `verification` table, `resetPasswordTokenExpiresIn`) | D-05; token lifecycle already handled |
| Password hashing for reset | manual scrypt | Better Auth `ctx.context.password.hash` (inside `resetPassword`/`setUserPassword`) | matches existing scrypt params (N=16384 r=16 p=1) per recovery memory |
| SMTP wire protocol | raw socket / custom MIME | `nodemailer` | STARTTLS, MIME, auth handled |
| Session revocation | `DELETE FROM session` raw SQL | `auth.api.revokeUserSessions({userId})` | plugin handles token/cookie semantics; raw SQL risks schema drift (seed-admins.sh §Why-HTTP-only) |
| Email enumeration protection on reset | custom "user not found" handling | native `requestPasswordReset` (returns 200 + generic message regardless of existence) | verified in endpoint source (`if (!user) { ... return generic }`) |

**Key insight:** The only operation with NO first-party endpoint is **reset-2FA of another user** (disable needs the target's password). Everything else is a wired plugin/native endpoint — direct DB manipulation is justified ONLY for reset-2FA, on the dashboard's own isolated DB.

## Runtime State Inventory

> Phase 13 is primarily additive (new plugin, new columns, new tables, new UI) but touches existing prod auth state. Categories assessed against `bd_ai_dashboard_prod`.

| Category | Items Found | Action Required |
|----------|-------------|------------------|
| Stored data | Existing operator rows in `user` (prod): `role` column is NEW and will be `NULL` for all existing rows after the admin-plugin migration. The earliest user must become `owner`; rest `operator`. | **Data migration** (one-shot seed, Pattern 7) IN ADDITION to the schema change. Without it, NO user is owner → admin gate denies everyone, locking out management. |
| Live service config | Dashboard env on n8n-ia-vm stack needs NEW vars: `BREVO_SMTP_USER`, `BREVO_SMTP_PASS` (and confirm `BETTER_AUTH_URL` is the public origin for reset links). These live in the stack UI/env, not git. | Config edit on the dashboard container (n8n-ia-vm). Verify reset-link `url` uses the public `https://ai-dashboard.converse-ai.app` origin (driven by `ctx.context.baseURL` ← `BETTER_AUTH_URL`). |
| OS-registered state | None — no OS-level registration involves dashboard auth. | None — verified (dashboard runs as a container; no Task Scheduler/systemd unit embeds operator identities). |
| Secrets/env vars | `BETTER_AUTH_SECRET` unchanged. New Brevo SMTP creds = the EXISTING Brevo account `797fad001` (CLAUDE.md global). No secret rotation. `DASHBOARD_DATABASE_URL` unchanged. | Add SMTP creds to the dashboard stack env only (no new secret system). |
| Build artifacts | `schema.ts` is regenerated (build/source artifact) — the `admin_audit_log` custom block is NOT emitted by the generator and will be dropped on regen if placed in the same file. | Keep `admin_audit_log` in a SEPARATE schema module (e.g. `schema-custom.ts`) merged into the drizzle client, OR re-append after every `generate` with a guarded script (Pitfall 1). |

**The canonical question — after every file is updated, what runtime state still holds the old shape?** The prod `user` table: existing rows have `role IS NULL` until the seed runs. This is the single most important migration-ordering risk: **schema push must be followed immediately by the owner seed**, or the dashboard becomes unmanageable (no owner). Plan these as two coupled, ordered tasks.

## Common Pitfalls

### Pitfall 1: CLI-canonical regen drops the custom `admin_audit_log` table
**What goes wrong:** `bunx @better-auth/cli generate --output src/lib/schema.ts` regenerates `schema.ts` from the Better Auth plugin config ONLY. A hand-added `admin_audit_log` table (not a Better Auth table) is erased on the next regen.
**Why it happens:** the generator emits exactly the plugin-declared tables (user/session/account/verification/two_factor + admin cols); it has no knowledge of custom tables.
**How to avoid:** Put `admin_audit_log` in a SEPARATE file (`src/lib/schema-custom.ts`) and merge both into the drizzle client: `drizzle(pool, { schema: { ...authSchema, ...customSchema } })`. Point `drizzle.config.ts` `schema` at a glob (`./src/lib/schema*.ts`) or an index barrel so `drizzle-kit push` sees both. The generator only ever rewrites `schema.ts`; `schema-custom.ts` is untouched. (The current `db.ts` imports `* as schema from "./schema"` — extend to spread the custom module.)
**Warning signs:** `admin_audit_log` disappears from `schema.ts` after a regen; `drizzle-kit push` proposes dropping the table.

### Pitfall 2: `drizzle-kit push` data-loss risk on prod when adding admin columns
**What goes wrong:** Adding `role`/`banned`/`banReason`/`banExpires` is additive (nullable columns) — low risk. BUT `drizzle-kit push` can propose destructive diffs if `schema.ts` drifts from prod (e.g. an index rename, or `strict:true` flagging the existing `two_factor` CLI-managed table).
**Why it happens:** `push` reconciles declared schema vs live DB; any unexpected delta becomes a migration the operator must approve.
**How to avoid:** Run `bunx drizzle-kit push` against `bd_ai_dashboard_staging` FIRST (the staging rehearsal DB named in `schema.ts` header), review the proposed SQL, confirm it is purely `ADD COLUMN` + new-table `CREATE`, THEN run against prod. `drizzle.config.ts` has `strict:true` + `verbose` gated off in production — keep NODE_ENV=development for the staging dry run to see full DDL. NEVER auto-approve a `push` against prod.
**Warning signs:** push diff contains `DROP`, `ALTER COLUMN ... TYPE`, or a `two_factor` table recreation.

### Pitfall 3: admin endpoint called without forwarded headers → silent FORBIDDEN
**What goes wrong:** `auth.api.createUser({ body })` without `headers` runs with no caller session; the admin middleware returns FORBIDDEN, surfacing as a generic "could not complete" error.
**How to avoid:** Always `const h = await headers(); auth.api.<op>({ body, headers: h })`. Mirror `auth.test.ts`.
**Warning signs:** every admin op fails with 403/FORBIDDEN even for a confirmed owner.

### Pitfall 4: `adminRoles` mismatch locks out the owner
**What goes wrong:** plugin defaults `adminRoles:["admin"]`; D-02 uses `"owner"`. The owner's `role='owner'` does NOT satisfy the default gate → all admin ops denied.
**How to avoid:** `admin({ adminRoles: ["owner"], defaultRole: "operator" })`. Also note the plugin validates that every `adminRoles` entry exists in the roles set (`admin.mjs:20-21`); since `owner` is a custom role string and the default `roles` map keys are `admin`/`user`, you may need to declare a `roles` access-control set OR rely on string roles. **Verify in staging** that `admin({ adminRoles:["owner"] })` boots without the invalid-roles warning; if it errors, pass an access-control `roles` config that includes `owner` (Better Auth `createAccessControl`). This is the single highest-risk config detail — test it first.
**Warning signs:** auth init throws/warns about invalid roles; owner gets FORBIDDEN.

### Pitfall 5: reset-2FA reopens CR-01 if implemented via `/two-factor/enable`
**What goes wrong:** trying to "reset" 2FA by calling enable would hit the CR-01 block (or, if bypassed, would reopen the credential-rotation vector CR-01 closes).
**How to avoid:** reset-2FA is a DB clear (Pattern 5), never an enable call. The operator re-enrolls on next login via the normal `/2fa/enroll` flow, where `enabled=false` legitimately permits enable.
**Warning signs:** reset-2FA action calls `authClient.twoFactor.enable` or `/two-factor/enable`.

### Pitfall 6: reset link points at a localhost / wrong origin
**What goes wrong:** `requestPasswordReset` builds `url = ${ctx.context.baseURL}/reset-password/${token}`. If `BETTER_AUTH_URL` is unset/localhost in the container, operators receive an unusable link.
**How to avoid:** confirm `BETTER_AUTH_URL=https://ai-dashboard.converse-ai.app` in the prod container env before shipping invites. Also ensure a `/reset-password` route exists to consume the token (NEW page — none today).
**Warning signs:** reset emails contain `http://localhost:3001/...`.

## Code Examples

### List endpoints the admin plugin exposes (1.4.22)
```
# Source: VERIFIED node_modules/better-auth/dist/plugins/admin/admin.d.mts (StrictEndpoint paths)
/admin/create-user          body: {email, password?, name, role?, data?}      ← invite (D-04/D-05)
/admin/set-user-password    body: {newPassword, userId}                        ← NOT used for reset (D-07)
/admin/set-role             body: {userId?, role: string|string[]}             ← owner seed alt
/admin/remove-user          body: {userId}                                     ← remove (D-05)
/admin/revoke-user-sessions body: {userId}                                     ← revoke on remove/reset
/admin/revoke-user-session  body: {sessionToken}
/admin/list-users           query: {searchValue?, limit?, offset?, ...}
/admin/update-user          body: {userId, data}
/admin/get-user, /admin/ban-user, /admin/unban-user, /admin/impersonate-user,
/admin/stop-impersonating, /admin/has-permission, /admin/list-user-sessions
# banUser/impersonateUser exist as routes but are NEVER wired into UI/server-actions (D-01).
```

### Native reset/change-password endpoints (built-in, no plugin)
```
# Source: VERIFIED dist/api/routes/{password,update-user}.mjs + Context7
/request-password-reset   body: {email, redirectTo?}   → fires sendResetPassword({user,url,token})
/reset-password           body: {newPassword, token}   (client: authClient.resetPassword)
/reset-password/:token    (GET — token landing)
/change-password          body: {currentPassword, newPassword, revokeOtherSessions?}  (self-service UM-01)
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| `seed-admins.sh` HTTP provisioning (no admin plugin) | `admin` plugin + browser Server Actions | Phase 13 | seed-admins.sh superseded; footer note in operadores page updated (UI-SPEC) |
| `i===0` visual role derivation | real `role` column (`adminRoles:["owner"]`) | Phase 13 | deterministic, auditable owner (D-02) |
| Manual SQL 2FA recovery (RUNBOOK-2FA-RECOVERY.md) | UI reset-2FA (clear + re-enroll), audited | Phase 13 | UI op replaces manual SQL; runbook remains the break-glass fallback |

**Deprecated/outdated:**
- `scripts/dashboard/seed-admins.sh`: superseded by the UI for provisioning. Keep as break-glass until the UI is verified in prod; the footer copy in `operadores/page.tsx` should stop citing it (UI-SPEC §Visual Hierarchy).

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | `nodemailer` 9.0.0 is the legitimate package (slopcheck unavailable) | Standard Stack / Package Audit | LOW — de-facto standard lib, no postinstall, named in CONTEXT.md; planner may add a `checkpoint:human-verify` before `bun add` |
| A2 | `admin({ adminRoles:["owner"] })` boots without an invalid-roles error using string roles (no custom access-control set) | Pitfall 4 | MEDIUM — if Better Auth requires `owner` to exist in a `roles` access-control map, an extra `createAccessControl` config is needed. MUST verify in staging boot. |
| A3 | Brevo SMTP creds for the dashboard = existing account `797fad001` (CLAUDE.md), usable from n8n-ia-vm | Pattern 4 / Runtime State | LOW — port reachability verified live; credential validity not test-sent in this session |
| A4 | `BETTER_AUTH_URL` in the prod container = public dashboard origin (drives reset-link URL) | Pitfall 6 | MEDIUM — if unset/localhost, invite/reset links are unusable. Confirm container env before shipping. |
| A5 | Adding admin columns + `admin_audit_log` via `drizzle-kit push` is purely additive on prod | Pitfall 2 | MEDIUM — must rehearse on `bd_ai_dashboard_staging` and review the diff before prod. |

**This table is NOT empty — items A2/A4/A5 need confirmation (staging rehearsal + container env check) before execution; A1/A3 are low-risk.**

## Open Questions

1. **Does `admin({ adminRoles:["owner"] })` require a matching access-control `roles` map?**
   - What we know: the plugin validates `adminRoles` entries against the roles set (`admin.mjs:20-21`); default roles are `admin`/`user`.
   - What's unclear: whether a custom string role `"owner"` is accepted as-is or must be declared via `createAccessControl`.
   - Recommendation: boot-test on staging FIRST (Pattern 1 + Pitfall 4). If it warns/errors, add `roles: { owner: ac.newRole(...), operator: ... }`. Small, contained.

2. **Does Better Auth's `requestPasswordReset` work when the freshly-created user has only a disposable password (no verified email)?**
   - What we know: `requestPasswordReset` looks up the user by email with `includeAccounts:true` and issues a token regardless of email-verification. `autoSignIn:false` is set.
   - What's unclear: whether any `requireEmailVerification` gate (not currently set in `auth.ts`) would block it. None is configured today.
   - Recommendation: verify on staging by inviting a test operator end-to-end; the existing `auth.test.ts` memoryAdapter harness can cover createUser→requestPasswordReset→resetPassword as an integration test.

3. **Self-service `/settings` vs existing `/settings/operadores` route grouping.**
   - What we know: `/settings/operadores/page.tsx` exists; `/settings/page.tsx` does not.
   - Recommendation: executor discretion (UI-SPEC) — add `/settings/page.tsx` as the change-password surface + a tab. No research blocker.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Better Auth `admin` plugin | D-01 all 4 ops | ✓ | 1.4.22 (bundled) | none needed |
| nodemailer (npm) | D-04 invite/reset email | ✗ (not yet a dep) | 9.0.0 available on registry | `bun add nodemailer` |
| Brevo SMTP relay (smtp-relay.brevo.com:587) | D-04 email delivery | ✓ from container | — | none (verified `CONTAINER-NC-OPEN`) |
| `@better-auth/cli` | schema regen | ✓ via `bunx` | latest | — |
| `drizzle-kit` | push migration | ✓ | 0.30.0 | — |
| `bd_ai_dashboard_staging` | migration rehearsal | assumed ✓ (named in schema.ts) | — | rehearse before prod push |

**Missing dependencies with no fallback:** none
**Missing dependencies with fallback:** `nodemailer` → `bun add nodemailer` (single install, no postinstall).

## Validation Architecture

### Test Framework
| Property | Value |
|----------|-------|
| Framework | Vitest 3 (jsdom) + @testing-library/react |
| Config file | `dashboard/vitest.config.ts` (include `src/**/*.test.{ts,tsx}`, setup `src/test-setup.ts`) |
| Quick run command | `cd dashboard && bun run test` (`vitest run`) |
| Full suite command | `cd dashboard && bun run test` (34 tests baseline) |
| Integration harness | `src/lib/auth.test.ts` boots a real `betterAuth({...})` over `memoryAdapter` and exercises `auth.api.*` — REUSE this for admin/reset flows. |

### Phase Requirements → Test Map
| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| UM-02 | admin plugin boots with `adminRoles:["owner"]`; user table gains `role` | integration | `vitest run src/lib/auth.test.ts` (extend) | ❌ Wave 0 (extend auth.test.ts) |
| UM-04 | owner `createUser` + `requestPasswordReset` succeeds; non-owner FORBIDDEN | integration | `vitest run src/lib/admin-actions.test.ts` | ❌ Wave 0 |
| UM-05 | `removeUser` deletes user + cascades sessions | integration | same file | ❌ Wave 0 |
| UM-06 | reset triggers `sendResetPassword` (mock transport) + revokes sessions | integration | same file (mock `mailer`) | ❌ Wave 0 |
| UM-07 | reset-2FA clears `two_factor` row + `two_factor_enabled=false`; CR-01 unaffected (enable allowed after) | integration | extend `auth.test.ts` CR-01 block | ❌ Wave 0 |
| UM-01 | change-password requires correct current password; wrong → error code | integration + component | `vitest run src/app/settings/page.test.tsx` | ❌ Wave 0 |
| UM-03 | owner seed promotes earliest user only; idempotent re-run | integration | `vitest run src/lib/seed-owner.test.ts` | ❌ Wave 0 |
| UM-10 | operadores page renders real role badge; non-owner sees no controls | component | `vitest run src/app/settings/operadores/page.test.tsx` | ❌ Wave 0 |
| D-09 | self-service change-password writes NO audit row; admin ops DO | integration | admin-actions.test.ts assertion | ❌ Wave 0 |

### Sampling Rate
- **Per task commit:** `cd dashboard && bun run test <touched-file>` + `bunx tsc --noEmit`
- **Per wave merge:** `cd dashboard && bun run test` (full suite green)
- **Phase gate:** full suite green + staging `drizzle-kit push` dry-run reviewed before `/gsd:verify-work`

### Wave 0 Gaps
- [ ] `src/lib/admin-actions.test.ts` — covers UM-04/05/06 + D-09 (owner gate + audit write), mock nodemailer transport
- [ ] `src/lib/seed-owner.test.ts` — covers UM-03 idempotency
- [ ] `src/app/settings/page.test.tsx` — covers UM-01 (mirror login/page.test.tsx mocking of `authClient`)
- [ ] `src/app/settings/operadores/page.test.tsx` — covers UM-10 role badge + owner-gate
- [ ] Extend `src/lib/auth.test.ts` — admin plugin boot (UM-02) + reset-2FA CR-01 invariant (UM-07)
- [ ] No framework install needed (Vitest present)

## Security Domain

> `security_enforcement` not found disabled in config — treat as enabled. ROADMAP §Phase 13 explicitly flags `/gsd:secure-phase` as required after planning.

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | yes | Better Auth credential accounts; reset tokens via `verification` table; 2FA TOTP mandatory |
| V3 Session Management | yes | `revokeUserSessions` on remove/reset/2FA-reset; cookieCache 30-min staleness caveat (RUNBOOK class 4) |
| V4 Access Control | yes | `admin` plugin `adminRoles:["owner"]` gate + server-action `requireOwner()` re-check (D-03); middleware as defense-in-depth |
| V5 Input Validation | yes | Zod body schemas on every admin endpoint (built into the plugin); D-13 allowlist hook on user.create; validate name/email in server action |
| V6 Cryptography | yes | reset/change use Better Auth `ctx.context.password.hash` (scrypt, never hand-rolled); random invite password via `crypto.randomUUID()` |
| V7 Error Handling/Logging | yes | `admin_audit_log` for every admin op (D-08/D-09); NEVER log passwords/tokens/secrets |

### Known Threat Patterns for Better Auth admin + Next Server Actions

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| Privilege escalation (operator invokes admin op via hidden-but-callable server action) | Elevation of Privilege | server-side `requireOwner()` re-check on EVERY action (D-03); UI hiding is cosmetic |
| Credential-rotation lockout via 2FA re-enable | Tampering / DoS | CR-01 hook intact; reset-2FA never calls enable (Pattern 5) |
| Email enumeration on reset endpoint | Information Disclosure | native `requestPasswordReset` returns generic 200 regardless of user existence (verified in source) |
| Showing temp password / TOTP secret in UI | Information Disclosure | privacy rule (UI-SPEC §Privacy); invite/reset deliver a LINK, never a credential |
| Stale session keeps revoked operator active ≤30 min | Spoofing | cookieCache maxAge tradeoff (auth.ts WR-01); document in runbook; optionally lower for high-assurance |
| Reset link points to wrong origin | Tampering | confirm `BETTER_AUTH_URL` = public origin (Pitfall 6) |
| SMTP creds / Brevo API exposure | Information Disclosure | SMTP creds in container env only; NOT the HTTP API (IP-locked); not committed |
| Cross-DB pollution (writing to ai_gateway) | Tampering | dashboard DB strictly isolated (07-RESEARCH Pitfall 7); `admin_audit_log` in `bd_ai_dashboard_prod` only |

## Sources

### Primary (HIGH confidence)
- `node_modules/better-auth@1.4.22` — `dist/plugins/admin/{schema,admin,client}.{mjs,d.mts}` (endpoint paths, body schemas, role columns, `adminRoles`/`defaultRole` defaults); `dist/api/routes/{password,update-user}.mjs` (reset/change-password); `bun.lock` + `node_modules/better-auth/package.json` (exact version 1.4.22)
- Context7 `/better-auth/better-auth` — admin plugin endpoints registration, createUser server-side-without-session, set-user-password/set-role docs, `sendResetPassword`/`requestPasswordReset`/`resetPassword`, two-factor disable requires-password
- Dashboard codebase (read 2026-06-15): `auth.ts`, `auth-client.ts`, `schema.ts`, `db.ts`, `drizzle.config.ts`, `middleware.ts`, `allowlist.ts`, `api/auth/[...all]/route.ts`, `app/2fa/enroll/page.tsx`, `app/settings/operadores/page.tsx`, `app/login/page.test.tsx`, `lib/auth.test.ts`, `vitest.config.ts`, `package.json`, `components.json`, `scripts/dashboard/seed-admins.sh`
- Live infra (SSH 2026-06-15): `ifix-ai-dashboard` container on n8n-ia-vm reaches `smtp-relay.brevo.com:587` (`CONTAINER-NC-OPEN`); host also OPEN
- npm registry: `nodemailer` 9.0.0 (no postinstall), `@types/nodemailer` 8.0.1

### Secondary (MEDIUM confidence)
- CLAUDE.md global §Brevo (SMTP relay host/port/FROM, Authorised-IPs lock applies to API not SMTP)
- CONTEXT.md D-01..D-09, UI-SPEC v2, ROADMAP §Phase 13, MEMORY `ai-dashboard-access-and-2fa-recovery.md`

### Tertiary (LOW confidence)
- none — all material claims verified against installed package or live infra.

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — every API verified against installed `better-auth@1.4.22` + Context7
- Architecture: HIGH — patterns derived from installed endpoint signatures + existing `auth.test.ts` conventions; reset-2FA DB approach forced by absence of a first-party endpoint (verified)
- Pitfalls: HIGH (Pitfall 1/2/3/5/6), MEDIUM (Pitfall 4 — `adminRoles` custom-role acceptance needs a staging boot-test, flagged A2)

**Research date:** 2026-06-15
**Valid until:** 2026-07-15 (Better Auth is fast-moving; the version is PINNED at `~1.4.18`/installed 1.4.22 — re-verify only if the lockfile bumps minor)
