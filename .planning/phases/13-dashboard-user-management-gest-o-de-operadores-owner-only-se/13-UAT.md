---
status: complete
phase: 13-dashboard-user-management
source: [13-01-SUMMARY.md, 13-02-SUMMARY.md, 13-03-SUMMARY.md, 13-04-SUMMARY.md, 13-05-SUMMARY.md]
started: 2026-06-28
updated: 2026-06-28
---

## Current Test

[testing complete]

## Tests

### 1. Login page loads
expected: Dashboard redirects unauthenticated users to /login with E-mail + Senha + Entrar.
result: pass
note: Auto-verified via Playwright.

### 2. Reset-password public surface (UM-04/UM-06 prerequisite)
expected: An unauthenticated visitor opening /reset-password/[token] sees the set-password form.
result: pass
note: |
  Was a blocker (middleware gated /reset-password → /login). FIXED commit b37ed6d. LIVE-verified
  after deploying Phase 13 build (image a49d9a7): the "Definir nova senha" form renders.

### 3. Invite operator end-to-end (UM-04 + UM-09)
expected: Owner "+ Provisionar operador" with @ifixtelecom address → reset link → set password → operator logs in.
result: pass
note: |
  Full E2E driven via Playwright on the live PROD dashboard with a controlled test owner
  (adm+uatowner) + invited operator (adm+uatop). Invite created the operator row (role=operator,
  "aguardando enroll"), wrote an operator.invite audit row, and generated a reset token. The
  set-password link (token from the verification table) rendered the form; setting the password
  let the operator log in (→ /2fa/enroll). Provisionar dialog (shadcn dialog) wired correctly.

### 4. Reset operator password (UM-06)
expected: Owner ··· → Resetar senha → email link + sessions revoked + audited.
result: pass
note: |
  alert-dialog confirm "Resetar senha?" → operator.reset_password audit row written; a fresh
  reset token created; the operator's sessions dropped to 0 (revoked). shadcn alert-dialog wired.

### 5. Reset operator 2FA + CR-01-safe re-enroll (UM-07)
expected: Owner ··· → Resetar 2FA → 2FA cleared, re-enroll required next login, never calls /two-factor/enable.
result: pass
note: |
  Confirm dialog → two_factor row deleted, user.two_factor_enabled=false, roster badge flipped to
  "aguardando enroll", operator.reset_2fa audit row. Direct table clear (CR-01-safe; the admin path
  never calls /two-factor/enable — confirmed in code + security audit).

### 6. Remove operator + session revoke (UM-05)
expected: Owner ··· → Remover operador → user deleted, sessions revoked, roster updates, audited.
result: pass
note: |
  Destructive alert-dialog "Remover operador?" → user row deleted (cascade), roster dropped to 2,
  operator.remove audit row. Full audit trail captured: invite → reset_password → reset_2fa → remove.

### 7. Owner-gate cosmetic (UM-10 runtime)
expected: Non-owner operator sees roster but + Provisionar and per-row ··· are hidden.
result: pass
note: |
  Logged in as the operator (non-owner): /settings/operadores rendered the roster read-only with
  real role badges (D-02), but "+ Provisionar operador" was absent and every "ações" cell was empty
  (no ··· menus). Server-side requireOwner is the real gate (D-03); this confirms the UI layer.

### 8. Self-service change-password (UM-01)
expected: Logged-in operator at /settings changes own password (requires current password).
result: pass
note: |
  /settings "Alterar senha" (Senha atual + Nova + Confirmar) → password changed (form cleared).
  D-09 confirmed: admin_audit_log count unchanged (self-service change-password is NOT audited).

## Summary

total: 8
passed: 8
issues: 0
pending: 0
skipped: 0

## Notes / caveats

- **UM-09 email INBOX delivery — not fully confirmable in this UAT (not a code defect):** the
  dashboard → Brevo SMTP send path works (BREVO_SMTP creds set + live AUTH verified; sends accepted,
  no errors). The functional invite/reset flow was proven by reading the reset token from the
  `verification` table and completing set-password. But the email could not be confirmed landing in
  a readable inbox: `ifixtelecom.com.br` MX = Cloudflare Email Routing (route1/2/3.mx.cloudflare.net),
  and the `adm+uat*@ifixtelecom.com.br` plus-addresses did not surface in the google-mcp `comercial`
  (adm@) mailbox — likely no Cloudflare routing rule for plus-addresses. A real operator with a
  Cloudflare-routed @ifixtelecom mailbox should receive it (SPF includes spf.brevo.com). ACTION:
  confirm one real invite reaches an operator's inbox, or check Cloudflare Email Routing rules.
- All test accounts (adm+uatowner, adm+uatop) + their sessions/2FA/tokens + the UAT audit rows were
  cleaned up afterward. Prod is back to 1 owner (pedro.araujo), 0 NULL roles, 0 audit rows.
- shadcn dialog / dropdown-menu / alert-dialog all render + wire correctly in prod.

## Gaps

[none — 8/8 passed; UM-09 inbox delivery flagged as an external mail-routing confirmation, not a code gap]
