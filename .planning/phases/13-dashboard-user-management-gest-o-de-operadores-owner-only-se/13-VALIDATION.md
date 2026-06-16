---
phase: 13
slug: dashboard-user-management-gest-o-de-operadores-owner-only-se
status: planned
nyquist_compliant: true
wave_0_complete: false
created: 2026-06-15
---

# Phase 13 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | vitest 3 (jsdom) + @testing-library/react — reuse `auth.test.ts` memoryAdapter harness per RESEARCH |
| **Config file** | `dashboard/vitest.config.ts` (verified present 2026-06-15; include `src/**/*.test.{ts,tsx}`, setup `src/test-setup.ts`) |
| **Quick run command** | `cd dashboard && bun run test <file>` (script `test`=`vitest run`) |
| **Full suite command** | `cd dashboard && bun run test` |
| **Estimated runtime** | ~30 seconds |

---

## Sampling Rate

- **After every task commit:** Run quick run command on touched test file + `bunx tsc --noEmit`
- **After every plan wave:** Run full suite command
- **Before `/gsd:verify-work`:** Full suite green + staging `drizzle-kit push` dry-run reviewed
- **Max feedback latency:** 30 seconds

---

## Per-Task Verification Map

> UM-IDs use the canonical RESEARCH §Phase Requirements definitions. Task IDs are `{plan}-T{n}`.

| Area | Requirement | Plan / Task | Threat Ref | Secure Behavior | Test Type | Automated Command | Status |
|------|-------------|-------------|------------|-----------------|-----------|-------------------|--------|
| Self-service change-password (`/settings`, current pw required) | UM-01 | 13-04-T1 | T-13-selfpw | wrong current pw → inline error; not audited | unit/component | `bun run test src/app/settings/page.test.tsx` | ⬜ pending |
| Admin plugin wired (`adminRoles:["owner"]`, `defaultRole:"operator"`) + role cols via CLI regen | UM-02 | 13-02-T1, 13-02-T2 | T-13-authz | admin gate keyed to role='owner'; boots w/o invalid-roles (A2) | integration | `bun run test src/lib/auth.test.ts` | ⬜ pending |
| One-shot idempotent owner seed (earliest user → owner) | UM-03 | 13-02-T2, 13-02-T3 | T-13-lockout | exactly one role='owner' post-run; re-run no-op | integration | `bun run test src/lib/seed-owner.test.ts` | ⬜ pending |
| Owner-gated Server Action: create/invite operator (createUser + requestPasswordReset) | UM-04 | 13-03-T2 | T-13-invite | non-owner FORBIDDEN; non-@ifixtelecom rejected; reset email (mock SMTP) | integration | `bun run test src/lib/admin-actions.test.ts` | ⬜ pending |
| Owner-gated Server Action: remove operator + revoke all sessions | UM-05 | 13-03-T2 | T-13-session | user removed; sessions revoked | integration | `bun run test src/lib/admin-actions.test.ts` | ⬜ pending |
| Owner-gated Server Action: reset operator password (email link + revoke) | UM-06 | 13-03-T2 | T-13-session | reset email dispatched; target sessions revoked | integration | `bun run test src/lib/admin-actions.test.ts` | ⬜ pending |
| Owner-gated Server Action: reset operator 2FA (clear two_factor + flag false + revoke), CR-01 intact | UM-07 | 13-02-T2, 13-03-T2 | T-13-2fa | enabled=false post-reset; CR-01 still permits enable after | integration | `bun run test src/lib/auth.test.ts src/lib/admin-actions.test.ts` | ⬜ pending |
| `admin_audit_log` table (D-08) + every admin op writes a row; self-pw NOT logged (D-09) | UM-08 | 13-02-T2, 13-03-T1, 13-03-T2 | T-13-audit | row per admin op; zero rows for self-service change-pw | integration | `bun run test src/lib/admin-actions.test.ts` | ⬜ pending |
| Brevo SMTP via nodemailer wired into sendResetPassword; container reachability confirmed | UM-09 | 13-01-T1, 13-02-T1, 13-02-T3 | T-13-smtp | SMTP transport (not API); creds in container env | integration + manual | `bun run test src/lib/admin-actions.test.ts` (mock) + live deliver | ⬜ pending |
| `operadores/page.tsx` reads real `role`; owner-gate hides controls for non-owners | UM-10 | 13-05-T1, 13-05-T2 | T-13-authz | role badge from real column; non-owner sees no controls | component | `bun run test src/app/settings/operadores/page.test.tsx` | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements (Plan 13-01)

- [x] Confirm `dashboard/vitest.config.ts` + vitest deps (verified present 2026-06-15)
- [ ] RED test stubs UM-01..UM-10 (13-01-T3) reusing `auth.test.ts` memoryAdapter harness
- [ ] Mock SMTP transport (nodemailer mailer) for invite/reset email assertions (13-01-T3)
- [ ] shadcn dialog/dropdown-menu/alert-dialog installed (13-01-T2)
- [ ] nodemailer install gated behind blocking-human checkpoint (13-01-T1)

*Staging-first checks (RESEARCH Open Questions, NOT unit-testable — done in 13-02-T3 before prod push):*
- [ ] A2: boot-test `admin({ adminRoles:["owner"] })` accepts custom string role (13-02-T2 auth.test.ts boot)
- [ ] A4: confirm `BETTER_AUTH_URL` in prod container = public origin (13-02-T3)
- [ ] A5: rehearse `drizzle-kit push` on `bd_ai_dashboard_staging`, review diff additive-only (13-02-T3)

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Invite email actually delivered via Brevo SMTP | UM-04/UM-09 | live SMTP relay, external delivery | Owner invites a real @ifixtelecom address; confirm email arrives with working set-password link |
| Operator re-enrolls 2FA at next login after reset | UM-07 | full browser auth flow incl. middleware routing | After reset-2FA, log in as target → middleware routes to /2fa/enroll |
| drizzle-kit push on prod is additive-only (no data loss) | UM-02/UM-08 | irreversible prod migration | Review staging diff (A5) then apply to bd_ai_dashboard_prod + immediate owner-seed (13-02-T3) |

---

## Validation Sign-Off

- [x] All tasks have `<automated>` verify or Wave 0 dependencies
- [x] Sampling continuity: no 3 consecutive tasks without automated verify
- [x] Wave 0 covers all MISSING references
- [x] No watch-mode flags
- [x] Feedback latency < 30s
- [x] `nyquist_compliant: true` set in frontmatter

**Approval:** approved (planner, 2026-06-15)
