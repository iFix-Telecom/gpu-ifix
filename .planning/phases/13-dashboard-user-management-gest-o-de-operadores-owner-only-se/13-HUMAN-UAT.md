---
phase: 13
status: awaiting_human_testing
created: 2026-06-28
source: 13-VERIFICATION.md (status=human_needed, 9/10 automated verified)
---

# Phase 13 — Human UAT

Automated verification passed 9/10 (tsc clean, vitest 51/51, prod migration applied +
owner seeded). The items below need live human testing before the phase is marked passed.
Re-run `/gsd:verify-work 13` (or re-verify) after completing them.

## BLOCKING operational prerequisite

- [x] **BREVO SMTP creds — DONE (2026-06-28).** `BREVO_SMTP_USER=797fad001@smtp-brevo.com`
  + `BREVO_SMTP_PASS=xsmtpsib-…` added to `/opt/ai-gateway-prod/.env` on n8n-ia-vm (backup
  `.env.bak-pre-brevo-*`) and container recreated (`docker compose up -d ifix-ai-dashboard`).
  Creds sourced from `converseai-dev-worker-email` (same Brevo account `797fad001`). Live
  SMTP AUTH against `smtp-relay.brevo.com:587` verified OK. Container healthy (Next.js Ready).
  - `BETTER_AUTH_URL=https://ai-dashboard.converse-ai.app` already confirmed correct.

## Human verification items

- [ ] **UM-04 + UM-09 — invite operator end-to-end:** as owner, `+ Provisionar operador`
  with an `@ifixtelecom.com.br` address → confirm the reset/set-password email arrives →
  open `/reset-password/[token]` → set password → new operator can log in.
- [ ] **UM-06 + UM-09 — reset operator password:** owner resets an operator's password via
  the `···` menu → confirm the email arrives and the link works.
- [ ] **UM-10 runtime — owner-gate (cosmetic):** log in as a non-owner operator → confirm
  `+ Provisionar operador` and the per-row `···` menu are hidden (server-side `requireOwner`
  is the real gate; this checks the UI layer).
- [ ] **UM-07 runtime — reset 2FA + CR-01-safe re-enroll:** owner resets an operator's 2FA →
  operator re-enrolls TOTP on next login via `/2fa/enroll` (CR-01: `/two-factor/enable` is
  never called by the admin path — confirmed in code).
- [ ] **UM-05 runtime — remove operator:** owner removes an operator → roster updates and the
  removed operator's sessions are revoked (cannot re-use an existing session, cannot re-login).

## Notes

- Prod DB (`bd_ai_dashboard_prod`) already migrated additively + owner elected
  (owners=1 = pedro.araujo@ifixtelecom.com.br, null_roles=0). No further DB action needed.
- Security: no `*-SECURITY.md` yet — run `/gsd:secure-phase 13` to verify the threat model
  mitigations (owner-gating, audit log, session revocation, 2FA reset vector) against code.
