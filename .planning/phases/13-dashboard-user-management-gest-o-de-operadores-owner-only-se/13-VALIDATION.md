---
phase: 13
slug: dashboard-user-management-gest-o-de-operadores-owner-only-se
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-06-15
---

# Phase 13 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | vitest (dashboard) — reuse `auth.test.ts` memoryAdapter harness per RESEARCH |
| **Config file** | `dashboard/vitest.config.ts` (confirm in Wave 0) |
| **Quick run command** | `cd dashboard && bun run test` (or `bunx vitest run <file>`) |
| **Full suite command** | `cd dashboard && bun run test` |
| **Estimated runtime** | ~30 seconds |

---

## Sampling Rate

- **After every task commit:** Run quick run command on touched test file
- **After every plan wave:** Run full suite command
- **Before `/gsd:verify-work`:** Full suite must be green
- **Max feedback latency:** 30 seconds

---

## Per-Task Verification Map

> Planner fills exact task IDs. Rows below are the validation skeleton derived from RESEARCH Validation Architecture (UM-01..UM-10).

| Area | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| Admin plugin wired (adminRoles=owner, defaultRole=operator) | UM-01 | T-13-authz | non-owner session rejected server-side | unit | `bunx vitest run admin` | ❌ W0 | ⬜ pending |
| Owner seed one-shot idempotent | UM-02 | — | exactly one role='owner' after run | integration | seed script + SQL assert | ❌ W0 | ⬜ pending |
| Server-action owner-gating (4 ops) | UM-03 | T-13-authz | operator role → action throws/forbidden | unit | `bunx vitest run actions` | ❌ W0 | ⬜ pending |
| Create/invite operator via createUser + sendResetPassword | UM-04 | T-13-invite | non-@ifixtelecom rejected; reset token emailed | integration | mock SMTP + memoryAdapter | ❌ W0 | ⬜ pending |
| Reset operator password (reuse invite path) + revoke sessions | UM-05 | T-13-session | target sessions revoked | integration | `bunx vitest run reset` | ❌ W0 | ⬜ pending |
| Reset-2FA (clear two_factor + flag false + revoke) CR-01-safe | UM-06 | T-13-2fa | enabled=false post-reset; CR-01 untouched | integration | `bunx vitest run twofactor-reset` | ❌ W0 | ⬜ pending |
| Remove operator + revoke all sessions | UM-07 | T-13-session | user removed, sessions gone | integration | `bunx vitest run remove` | ❌ W0 | ⬜ pending |
| Self-service change-password (current pw required) | UM-08 | T-13-selfpw | wrong current pw rejected | unit | `bunx vitest run changepw` | ❌ W0 | ⬜ pending |
| admin_audit_log writes on every admin action | UM-09 | T-13-audit | row per action; no self-pw logged | integration | `bunx vitest run audit` | ❌ W0 | ⬜ pending |
| operadores page reads real DB role (not i===0) | UM-10 | — | owner badge from role column | unit | `bunx vitest run operators` | ❌ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] Confirm/install `dashboard/vitest.config.ts` + vitest deps
- [ ] Reuse `auth.test.ts` memoryAdapter harness for server-action + admin-endpoint tests
- [ ] Mock SMTP transport (nodemailer) for invite/reset email assertions
- [ ] Test stubs for UM-01..UM-10

*Staging-first checks (RESEARCH Open Questions, NOT unit-testable — do before prod push):*
- [ ] A2: boot-test `admin({ adminRoles:["owner"] })` accepts custom string role on `bd_ai_dashboard_staging`
- [ ] A4: confirm `BETTER_AUTH_URL` in prod container = public origin (drives reset-link URLs)
- [ ] A5: rehearse `drizzle-kit push` on staging, review diff is additive-only

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Invite email actually delivered via Brevo SMTP | UM-04 | live SMTP relay, external delivery | Owner invites a real @ifixtelecom address; confirm email arrives with working set-password link |
| Operator re-enrolls 2FA at next login after reset | UM-06 | full browser auth flow incl. middleware routing | After reset-2FA, log in as target → middleware routes to /2fa/enroll |
| drizzle-kit push on prod is additive-only (no data loss) | UM-01 | irreversible prod migration | Review staging diff (A5) then apply to bd_ai_dashboard_prod |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 30s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
