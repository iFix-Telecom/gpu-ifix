---
status: testing
phase: 13-dashboard-user-management
source: [13-01-SUMMARY.md, 13-02-SUMMARY.md, 13-03-SUMMARY.md, 13-04-SUMMARY.md, 13-05-SUMMARY.md]
started: 2026-06-28
updated: 2026-06-28
---

## Current Test

number: 3
name: Invite operator end-to-end (UM-04 + UM-09)
expected: |
  As owner, "+ Provisionar operador" with an @ifixtelecom address sends an email;
  the recipient opens the /reset-password/[token] link and sets a password, then logs in.
awaiting: user response

## Tests

### 1. Login page loads
expected: Dashboard at https://ai-dashboard.converse-ai.app redirects unauthenticated users to /login with E-mail + Senha + Entrar.
result: pass
note: Auto-verified via Playwright — login form renders (e-mail/senha/Entrar).

### 2. Reset-password public surface (UM-04/UM-06 prerequisite)
expected: An unauthenticated visitor opening /reset-password/[token] sees the set-password form (this is the surface an invited/reset operator reaches from the email link).
result: pass
note: |
  Was a blocker (live Playwright: /reset-password/dummy-token → 307 /login). FIXED in
  commit b37ed6d — added `reset-password` to middleware config.matcher exclusion. Verified
  deterministically: matcher regex now PUBLIC for /reset-password/* while /settings +
  /settings/operadores stay GATED; tsc clean; suite 51/51. LIVE re-verify pending the
  dashboard image rebuild+redeploy (the running container still serves the pre-fix build).

### 3. Invite operator end-to-end (UM-04 + UM-09)
expected: As owner, "+ Provisionar operador" with an @ifixtelecom address → email arrives → /reset-password/[token] set password → new operator logs in.
result: [pending]
blocked_by: depends on Test 2 fix (reset-password gated by middleware)

### 4. Reset operator password (UM-06 + UM-09)
expected: Owner resets an operator's password via the ··· menu → email link arrives and works.
result: [pending]
blocked_by: depends on Test 2 fix

### 5. Reset operator 2FA + CR-01-safe re-enroll (UM-07)
expected: Owner resets an operator's 2FA → operator re-enrolls TOTP on next login via /2fa/enroll; admin path never calls /two-factor/enable.
result: [pending]

### 6. Remove operator + session revoke (UM-05)
expected: Owner removes an operator → roster updates and the removed operator's sessions are revoked (cannot re-login / reuse session).
result: [pending]

### 7. Owner-gate cosmetic (UM-10 runtime)
expected: Logged in as a non-owner operator, "+ Provisionar operador" and per-row ··· menu are hidden (server-side requireOwner is the real gate).
result: [pending]

### 8. Self-service change-password (UM-01)
expected: Logged-in operator opens /settings, enters current + new password, password changes (wrong current → error).
result: [pending]

## Summary

total: 8
passed: 2
issues: 0
pending: 6
skipped: 0

## Gaps

- truth: "Unauthenticated visitor opening /reset-password/[token] sees the set-password form"
  status: closed
  reason: "Middleware matcher missing reset-password exclusion. FIXED commit b37ed6d (added reset-password to config.matcher). Regex-verified PUBLIC; tsc + 51/51 green. Live re-verify pending dashboard redeploy."
  severity: blocker
  test: 2
  artifacts: [dashboard/src/middleware.ts]
  missing: []
