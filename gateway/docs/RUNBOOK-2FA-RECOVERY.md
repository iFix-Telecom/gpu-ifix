# 2FA Admin Recovery Runbook

**Phase 11 (`ai-dashboard` SSO).** Read this when an Ifix admin has
lost their TOTP authenticator device (phone wipe, authenticator-app
deletion, …) AND lost their 10 backup codes (no password-manager copy)
— they cannot pass the `/2fa/challenge` page and are locked out of
`https://ai-dashboard.converse-ai.app`.

**Last updated: 2026-05-27.**

Related runbooks:

- [`RUNBOOK-INCIDENTS.md`](./RUNBOOK-INCIDENTS.md) — general incident
  response. Class 4 (Rate-limit / quota lockout tenant) has an admin
  2FA lockout sub-detection; Operator Recovery Procedures cross-refs
  this runbook.
- [`RUNBOOK-DEPLOY.md`](./RUNBOOK-DEPLOY.md) — per-env keys (D-19)
  context and `scripts/dashboard/seed-admins.sh` (Plan 11-05) reset
  paired flow.

---

## Mental Model (30 seconds)

2FA is enforced by the Better Auth `twoFactor` plugin (D-12), wired in
`dashboard/src/lib/auth.ts`. A locked-out admin has:

- lost their authenticator app (phone wipe, app deletion, lost device,
  factory reset, …) AND
- lost their 10 backup codes (no password-manager copy, no printed
  paper backup).

The recovery procedure resets `two_factor_enabled` to `false` on the
`dashboard_auth.user` row AND deletes the matching `two_factor` row
for that user. On the next login attempt, the middleware redirects the
admin to `/2fa/enroll` (Plan 11-02 enroll flow), allowing fresh
TOTP enrolment via the dashboard UI.

**Why we don't just disable 2FA permanently:** the 4-admin dashboard
controls production breaker overrides, quota changes, and Vast primary
lifecycle. Single-factor login on those controls is a credible
elevation-of-privilege risk (threat T-11-DOC-03 in 11-09-PLAN). The
device-loss path is *bounded* — it temporarily steps the admin down
to first-factor + audit-trail, then immediately re-enrolls them.

---

## Authorization rules (separation of duty)

Treat 2FA recovery the same as any other authentication-control
mutation in production:

1. **The locked-out admin requests recovery via a SECONDARY channel.**
   Acceptable secondary channels:
   - **Voice WhatsApp call** to a second admin — confirm voice
     identity. Voice is the canonical secondary channel (cheap,
     deniable-resistant, no text trail to spoof).
   - **In-person request** with photo-ID verification.
   - Password-manager-shared secure note (1Password vault item)
     where the locked-out admin proves they hold the password-manager
     master vault — meaning credential compromise is still bounded.

   Text-only requests (SMS, plain WhatsApp text, email) are NOT
   acceptable. Treat any text-only request as a potential takeover
   attempt and escalate to the on-call platform owner before acting.

2. **The recovery is EXECUTED by a DIFFERENT admin.** Self-service
   recovery is forbidden — it would defeat the 2FA control entirely.
   The executing admin is the "secondary admin" in the request flow.

3. **Every recovery writes an audit row BEFORE the SQL UPDATE.**
   This is the repudiation defense: if the recovery is later
   questioned (compliance audit, security incident review), the audit
   row is the only proof that the procedure was followed. Writing
   the audit row first means a failed UPDATE still leaves a
   breadcrumb.

---

## Procedure

### Option A — `gatewayctl admin reset-2fa` (preferred when available)

If a future phase ships `gatewayctl admin-key reset-2fa --email <addr>`
(a subcommand that wraps the audit log row + SQL update atomically),
prefer that path. Cross-ref Plan 11-04 for the current gatewayctl
surface. Until the subcommand exists, use Option B.

### Option B — direct Postgres update (current default)

The executing admin (NOT the locked-out one) runs the following
sequence inside the dashboard prod database
(`bd_ai_dashboard_prod`, schema `dashboard_auth`).

**Connection:** the executing admin obtains the `DASHBOARD_DATABASE_URL`
DSN from the operator-only secret store (see [`RUNBOOK-DEPLOY.md`](./RUNBOOK-DEPLOY.md)
§"Per-env DSN access"). Never copy the DSN into chat — paste at
shell only.

```bash
# Set variables (NEVER echo to stdout / logs)
export DASHBOARD_DATABASE_URL='postgres://doadmin:<PASS>@…/bd_ai_dashboard_prod?sslmode=require&options=-c%20search_path%3Ddashboard_auth'
export LOCKED_OUT_EMAIL='{locked-out admin email — @ifixtelecom.com.br}'
export EXECUTING_ADMIN_EMAIL='{your admin email — must be different}'
```

**Step 1 — Confirm the user row exists and that 2FA is currently
enabled.** Refuse to proceed if `two_factor_enabled` is already
`false` (means the admin is locked out for a different reason — wrong
password, account disabled, … — see Pitfalls below).

```sql
-- psql "$DASHBOARD_DATABASE_URL" -c "..."
SELECT id, email, two_factor_enabled, "emailVerified"
FROM dashboard_auth."user"
WHERE email = current_setting('myvars.locked_out_email');
```

(Substitute `current_setting(...)` for the shell variable interpolation
via `psql -v locked_out_email="$LOCKED_OUT_EMAIL"` if your client
supports parameter substitution. Otherwise inline the literal — do NOT
build the query string in shell with unquoted variables.)

Expect: exactly one row, `two_factor_enabled = true`.

**Step 2 — Write the audit row FIRST (before the reset).**

```sql
INSERT INTO dashboard_auth.audit_log
  (actor_email, action, target_email, reason, ts)
VALUES
  ('{EXECUTING_ADMIN_EMAIL}',
   '2fa-reset',
   '{LOCKED_OUT_EMAIL}',
   'device-loss-and-backup-codes-lost (RUNBOOK-2FA-RECOVERY)',
   NOW());
```

If `dashboard_auth.audit_log` does not exist on the currently-installed
schema (Better Auth schema evolution may rename or postpone this
table), fall back to:

1. A comment-tagged INSERT against any audit-capable table the
   dashboard does ship (check `\dt dashboard_auth.*` first).
2. If no audit table exists at all, document the manual audit entry
   in the postmortem ticket
   (`.planning/postmortems/postmortem-{YYYY-MM-DD}-2fa-reset-{slug}.md`)
   created per the [`RUNBOOK-INCIDENTS.md`](./RUNBOOK-INCIDENTS.md)
   Class 4 sub-class flow. **Never skip the audit step entirely.**

**Step 3 — Run the reset in a single transaction.**

```sql
BEGIN;
UPDATE dashboard_auth."user"
   SET two_factor_enabled = false
 WHERE email = '{LOCKED_OUT_EMAIL}';
DELETE FROM dashboard_auth.two_factor
 WHERE "userId" = (SELECT id FROM dashboard_auth."user"
                    WHERE email = '{LOCKED_OUT_EMAIL}');
COMMIT;
```

> **Schema note.** The Drizzle schema in
> [`dashboard/src/lib/schema.ts`](../../dashboard/src/lib/schema.ts:98)
> declares the table as `two_factor` (snake_case) with column
> `two_factor_enabled` on the `user` table (snake_case in DB; camelCase
> `twoFactorEnabled` only in TypeScript). Use the snake_case forms
> shown above for SQL.

**Step 4 — Verify.**

```sql
-- two_factor_enabled is now false
SELECT email, two_factor_enabled
FROM dashboard_auth."user"
WHERE email = '{LOCKED_OUT_EMAIL}';

-- two_factor row for this user is gone
SELECT count(*) FROM dashboard_auth.two_factor
WHERE "userId" = (SELECT id FROM dashboard_auth."user"
                   WHERE email = '{LOCKED_OUT_EMAIL}');
```

Expect: `two_factor_enabled = false`; `two_factor` row count = 0.

**Step 5 — Notify the locked-out admin via the same secondary
channel.**

> "2FA reset committed. Log in via `https://ai-dashboard.converse-ai.app/login`;
> the middleware will redirect you to `/2fa/enroll` to set up a fresh
> authenticator. Save the new 10 backup codes in your password
> manager this time."

**Step 6 — Locked-out admin completes fresh enrolment.** They walk
through the `/2fa/enroll` dashboard UI flow (Plan 11-02), scan the
QR code with a fresh authenticator app, verify with one TOTP code,
and copy the displayed 10 backup codes into their password manager
(1Password vault).

After enrolment, the executing admin reconfirms via:

```sql
SELECT email, two_factor_enabled
FROM dashboard_auth."user"
WHERE email = '{LOCKED_OUT_EMAIL}';
```

Expect: `two_factor_enabled = true` again. Recovery cycle complete.

---

## Related: password reset (paired flow)

If the locked-out admin also forgot their password OR the executing
admin suspects credential compromise alongside device loss, pair the
2FA reset with a password reset via
[`scripts/dashboard/seed-admins.sh`](../../scripts/dashboard/seed-admins.sh)
(Plan 11-05 — per-env admin provisioning script). `seed-admins.sh`:

- Uses HTTP-only single-path provisioning (no direct SQL probe; see
  the script header for the rationale).
- Generates a fresh password and writes it to a `chmod 600` file —
  the file path goes to stdout, the password itself never does.
- The path is delivered to the locked-out admin via the same
  secondary channel used for the 2FA reset request.

After the locked-out admin picks up the password file, the executing
admin shreds it from disk (`shred -u <file>`).

---

## Audit trail

The audit row written in Step 2 is the canonical proof of authorised
recovery. Operator search query:

```sql
SELECT actor_email, action, target_email, reason, ts
FROM dashboard_auth.audit_log
WHERE action = '2fa-reset'
ORDER BY ts DESC
LIMIT 20;
```

Compliance / annual review query:

```sql
SELECT date_trunc('month', ts) AS m, count(*)
FROM dashboard_auth.audit_log
WHERE action = '2fa-reset'
GROUP BY 1 ORDER BY 1 DESC;
```

A 2FA-reset cadence higher than ~1 per quarter across the 4-admin
team is a signal of weak password-manager backup hygiene — surface
it at the next ops review.

---

## Pitfalls

- **DO NOT skip the audit row** (Step 2). It is the only repudiation
  defense if the recovery is later questioned. If the audit table
  is unavailable, document the audit entry in the postmortem ticket
  before running the SQL UPDATE.
- **DO NOT execute against your own user.** Separation of duty — the
  executing admin and the locked-out admin must be different
  individuals.
- **DO NOT communicate the reset over an unauthenticated text
  channel.** Voice call OR password-manager-shared secure note only.
- **DO NOT reset multiple users in bulk.** Each reset is a
  single-user event with its own audit row. Bulk resets defeat the
  audit-trail invariant and are also a red flag of compromise.
- **DO NOT proceed if `two_factor_enabled` is already `false`** at
  Step 1. That means the admin is locked out for a different reason
  (forgotten password, account disabled, email not verified). Route
  to the appropriate runbook section (password reset via
  `seed-admins.sh`, or
  [`RUNBOOK-INCIDENTS.md`](./RUNBOOK-INCIDENTS.md) Class 4 for an
  account-disabled situation).
- **DO NOT export the DSN to chat or stdout.** Set it only at the
  executing admin's shell and `unset DASHBOARD_DATABASE_URL` after
  the procedure completes.
- **DO wait 60 seconds after the SQL UPDATE (Step 3) before assuming
  the locked-out admin is actually denied.** The dashboard middleware
  uses Better Auth `cookieCache` with `maxAge=60` (see
  `dashboard/src/lib/auth.ts` session block, WR-01) — for up to 60s
  after the DB update, an existing session cookie can still pass the
  middleware gate with stale `twoFactorVerified=true` claims. If the
  admin had an active browser tab open at the moment of recovery, ask
  them to either close the tab or wait 60s before testing the
  re-enroll flow. This is acceptable for the 4-admin internal panel
  per the Class 4 incident-recovery trade-off documented in
  [`RUNBOOK-INCIDENTS.md`](./RUNBOOK-INCIDENTS.md).

---

## Cross-refs

- [`RUNBOOK-INCIDENTS.md`](./RUNBOOK-INCIDENTS.md) — Class 4 sub-detection
  for admin 2FA lockout + Operator Recovery Procedures pointer.
- [`RUNBOOK-DEPLOY.md`](./RUNBOOK-DEPLOY.md) — per-env DSN access + the
  D-19 per-env key separation context.
- [`scripts/dashboard/seed-admins.sh`](../../scripts/dashboard/seed-admins.sh)
  — Plan 11-05 admin password reset paired flow (single source of
  truth for password generation; `chmod 600` file output).
- [`dashboard/src/lib/auth.ts`](../../dashboard/src/lib/auth.ts) —
  Better Auth `twoFactor` plugin source + issuer string `Ifix AI
  Gateway` (D-12).
- [`dashboard/src/lib/schema.ts`](../../dashboard/src/lib/schema.ts) —
  `user.two_factor_enabled` column + `two_factor` table definitions.
- Dashboard 2FA enroll flow at `https://ai-dashboard.converse-ai.app/2fa/enroll`
  (Plan 11-02 enroll page — post-reset re-enrolment target).
- 11-CONTEXT.md D-12 (TOTP 2FA mandatory) + D-13 (manual admin
  provisioning by operator).

---

*Phase 11 (`ai-dashboard`) admin 2FA recovery runbook. Authoritative
copy at `gateway/docs/RUNBOOK-2FA-RECOVERY.md`. Reset events are
audited inline (Step 2 above) — no separate evidence file convention.*
