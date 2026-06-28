/**
 * Brevo SMTP transport for the dashboard (D-04, Phase 13 — UM-09).
 *
 * Source: 13-RESEARCH.md Pattern 4 verbatim + CLAUDE.md global §Brevo
 * (account 797fad001, smtp-relay.brevo.com:587, FROM
 * noreply@ifixtelecom.com.br). This module is the ONLY mail transport in
 * the dashboard — `auth.ts` `emailAndPassword.sendResetPassword` imports
 * `mailer` to deliver invite/reset links.
 *
 * Why SMTP and NOT the Brevo HTTP API: the Brevo account has the
 * Authorised-IPs lock enabled, which 401s the HTTP API from any IP not on
 * the allowlist. SMTP on port 587 is NOT affected by that lock and is
 * verified reachable from the dashboard container on n8n-ia-vm
 * (RESEARCH Pattern 4 — `nc -zw5` OPEN).
 *
 * Credentials come from the container env (`BREVO_SMTP_USER`,
 * `BREVO_SMTP_PASS`), wired into the dashboard stack on n8n-ia-vm. They
 * are read here at module load — acceptable because this module is not
 * evaluated during `next build` (only invoked when a reset email is sent
 * at request time), mirroring the deferred-env spirit of `db.ts`.
 */
import nodemailer from "nodemailer";

export const mailer = nodemailer.createTransport({
  host: "smtp-relay.brevo.com",
  port: 587,
  secure: false, // STARTTLS on 587
  auth: {
    user: process.env.BREVO_SMTP_USER,
    pass: process.env.BREVO_SMTP_PASS,
  },
});
