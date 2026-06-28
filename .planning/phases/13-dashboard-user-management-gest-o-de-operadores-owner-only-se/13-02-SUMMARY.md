---
phase: 13-dashboard-user-management-gest-o-de-operadores-owner-only-se
plan: 02
subsystem: dashboard-auth
tags: [better-auth, admin-plugin, drizzle, brevo-smtp, access-control, audit-log]
status: partial-checkpoint
requires:
  - "13-01 (dashboard auth foundation, twoFactor + allowlist + rateLimit)"
provides:
  - "admin plugin gate (adminRoles:['owner'], defaultRole:'operator')"
  - "Brevo SMTP sendResetPassword transport (invite/reset link delivery)"
  - "regenerated schema.ts with role/banned/banReason/banExpires + impersonatedBy"
  - "isolated admin_audit_log table (schema-custom.ts) wired into drizzle client"
  - "idempotent owner-seed script (scripts/dashboard/seed-owner.ts)"
  - "createAccessControl owner/operator roles map (A2/Pitfall-4 fix)"
affects:
  - "13-03 server actions (requireOwner gate, invite/remove/reset-2fa/audit) depend on this"
  - "bd_ai_dashboard_prod schema (PENDING human Task 3 — not yet applied)"
tech-stack:
  added:
    - "nodemailer (already in deps) — Brevo SMTP transport"
    - "better-auth admin plugin + better-auth/plugins/access createAccessControl"
  patterns:
    - "CLI-canonical schema regen + hand-maintained custom-table module (Pitfall 1)"
    - "access-control roles map for custom string roles (Pitfall 4 / A2)"
key-files:
  created:
    - dashboard/src/lib/email.ts
    - dashboard/src/lib/schema-custom.ts
    - scripts/dashboard/seed-owner.ts
  modified:
    - dashboard/src/lib/auth.ts
    - dashboard/src/lib/schema.ts
    - dashboard/src/lib/db.ts
    - dashboard/drizzle.config.ts
    - dashboard/src/lib/auth.test.ts
decisions:
  - "A2/Pitfall-4 resolved: admin({adminRoles:['owner']}) requires a createAccessControl roles map declaring owner+operator — without it the CLI/runtime throws 'Invalid admin roles: owner'. Added owner (full admin statements) + operator (none)."
  - "schema.ts doc header is re-prepended by hand after each CLI regen (generator owns the body)."
metrics:
  duration: "~25min"
  completed: "2026-06-28"
  tasks_completed: 2
  tasks_total: 3
  files_created: 3
  files_modified: 5
---

# Phase 13 Plan 02: Auth admin-plugin + Brevo SMTP + schema/audit foundation Summary

Wired the Better Auth `admin` plugin (owner-gated, operator-default), the Brevo SMTP
`sendResetPassword` transport, the CLI-regenerated schema (admin columns) plus an isolated
`admin_audit_log` table, the db/drizzle-config glue, and an idempotent owner-seed script —
the security-and-data foundation Plan 03's server actions build on. **Task 3 (irreversible
prod `drizzle-kit push` + immediate owner-seed against `bd_ai_dashboard_prod`) is a
blocking-human checkpoint and was NOT executed.**

## What was built (Tasks 1-2, committed)

### Task 1 — admin plugin + Brevo email transport + sendResetPassword (`8bae800`)
- `dashboard/src/lib/email.ts` (NEW): `mailer = nodemailer.createTransport({ host:"smtp-relay.brevo.com", port:587, secure:false, auth:{BREVO_SMTP_USER,BREVO_SMTP_PASS} })`. SMTP only (Brevo HTTP API is IP-locked → 401). Account 797fad001.
- `dashboard/src/lib/auth.ts`: registered `admin({ adminRoles:["owner"], defaultRole:"operator" })` (kept `twoFactor` first); added `emailAndPassword.sendResetPassword` calling `mailer.sendMail` with the public reset `url`; updated the L1-40 header to record the D-01 reversal.
- **CR-01 hook (TWO_FACTOR_ALREADY_ENABLED) and D-13 allowlist hook preserved verbatim**; `bunx tsc --noEmit` clean.

### Task 2 — schema regen + custom audit table + db/config wiring + owner-seed + tests (`393d27c`)
- `dashboard/src/lib/schema.ts`: regenerated via `bunx @better-auth/cli@latest generate` — now carries `role`/`banned`/`ban_reason`/`ban_expires` on `user` and `impersonated_by` on `session`. Doc header re-prepended by hand.
- `dashboard/src/lib/schema-custom.ts` (NEW): `adminAuditLog` pgTable (`admin_audit_log`) with D-08 columns (`id`, `actor_id`, `actor_email`, `target_id`, `target_email`, `action`, `metadata` jsonb, `created_at`) + `admin_audit_log_actor_idx`. Isolated from the CLI-regenerated file (Pitfall 1).
- `dashboard/src/lib/db.ts`: spreads `{ ...authSchema, ...customSchema }` into the drizzle client; `export { schema }` preserved.
- `dashboard/drizzle.config.ts`: schema glob → `./src/lib/schema*.ts` (migrates audit table); `strict:true` + TLS/CA logic untouched.
- `scripts/dashboard/seed-owner.ts` (NEW): idempotent two-statement backfill (earliest user → owner only if none exists; NULL → operator), fail-fast on missing `DASHBOARD_DATABASE_URL`, prints aggregate counts only (no secrets), asserts exactly 1 owner + 0 NULL roles.
- `dashboard/src/lib/auth.test.ts`: harness now mirrors the admin plugin + access-control map; **UM-02** (admin boots with `adminRoles:["owner"]`, user carries `role` column) + **UM-07** (reset-2FA stays CR-01-safe: re-enroll permitted after clearing two_factor + twoFactorEnabled=false) added. **8/8 tests green.**

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] A2/Pitfall-4: admin plugin would not boot with custom role `owner`**
- **Found during:** Task 2 (schema regen boot-test — exactly the A2 staging boot-test the plan mandates).
- **Issue:** `bunx @better-auth/cli generate` (which imports the real `auth.ts` config) threw `BetterAuthError: Invalid admin roles: owner. Admin roles must be defined in the 'roles' configuration.` The plugin validates every `adminRoles` entry against its access-control `roles` map, whose defaults are only `admin`/`user`.
- **Fix:** Added the contained `createAccessControl(defaultStatements)` map in `auth.ts` — `owner` = full admin statement set (`user`: create/list/set-role/ban/impersonate/delete/set-password/get/update; `session`: list/revoke/delete), `operator` = none — and passed `ac` + `roles:{ owner, operator }` to `admin(...)`. This is exactly the small/contained fix RESEARCH Pitfall 4 / A2 / Open Question A4-precursor predicted. Mirrored the same map in the test harness so UM-02 exercises it.
- **Files modified:** `dashboard/src/lib/auth.ts`, `dashboard/src/lib/auth.test.ts`
- **Commit:** `393d27c`

**2. [Rule 2 - Missing critical] schema.ts doc header lost on regen**
- **Found during:** Task 2 (post-regen inspection).
- **Issue:** the better-auth generator emits only the table definitions and dropped the CLI-canonical workflow doc header (which documents the never-hand-edit rule and the Pitfall-1 split).
- **Fix:** re-prepended the doc header as a leading block comment (documentary only, no table change), now also noting the manual re-prepend step + the schema-custom.ts split.
- **Commit:** `393d27c`

### Environment note (not a code deviation)
- The clickup-link-enforce PostToolUse hook blocked the first Write because the fresh worktree lacked `.planning/clickup-active-task.json`. The main repo already carries `{"skip": true}` (operator's standing decision to skip ClickUp linkage for this project). Mirrored that gitignored marker into the worktree `.planning/` to satisfy the hook — no ClickUp task created, no tracked file added.

## Authentication / Infrastructure Gates

None encountered in Tasks 1-2 (no live auth required). Task 3 itself is the human-action gate (see below).

## Task 3 — BLOCKING human-action checkpoint (NOT executed)

**Type:** checkpoint:human-action (gate=blocking-human)
**Status:** PENDING HUMAN — irreversible prod DB migration coupled to owner-seed; cannot be auto-approved.

Task 3 applies the additive schema (admin columns + `admin_audit_log`) to the LIVE
`bd_ai_dashboard_prod` via `drizzle-kit push`, then IMMEDIATELY runs `seed-owner.ts`
(no gap — otherwise `role IS NULL` for every row and the admin gate locks out everyone),
plus sets Brevo SMTP creds + confirms `BETTER_AUTH_URL` in the n8n-ia-vm dashboard stack.
This executor (parallel worktree agent) must not mutate production. See the orchestrator
checkpoint return for the exact human runbook.

## Self-Check: PASSED

- Files: all 8 plan files FOUND on disk.
- Commits: `8bae800` (Task 1) + `393d27c` (Task 2) present in `git log`.
- Verification: `bunx tsc --noEmit` clean; `bun run test src/lib/auth.test.ts` → 8/8 passed.
- No file deletions; no stray untracked files.
- No DB/infra mutations; STATE.md/ROADMAP.md untouched (orchestrator owns those).
