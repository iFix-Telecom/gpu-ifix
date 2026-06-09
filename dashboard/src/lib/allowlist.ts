/**
 * D-13 domain allowlist for dashboard signUp.
 *
 * Env-driven via `DASHBOARD_ALLOWED_EMAIL_DOMAINS` (comma-separated,
 * lowercase). Default scoped to the single Ifix domain
 * (`ifixtelecom.com.br`) — enough for the ~4 internal operators that
 * Phase 11 ships against. SREs can extend at runtime without a code
 * change (e.g. add a partner audit domain).
 *
 * Implementation note (11-RESEARCH §Don't Hand-Roll): uses
 * `lastIndexOf("@")` rather than a regex. Handles edge cases like
 * `quoted@local@domain.com` safely enough for a 4-admin use case
 * without introducing a regex audit surface.
 */
const ALLOWED = (
  process.env.DASHBOARD_ALLOWED_EMAIL_DOMAINS ?? "ifixtelecom.com.br"
)
  .split(",")
  .map((s) => s.trim().toLowerCase())
  .filter((s) => s.length > 0);

/**
 * Returns `true` when `email` belongs to one of the configured
 * allowlisted domains. Case-insensitive. Returns `false` for
 * malformed input (no `@`, empty string).
 */
export function isAllowedEmail(email: string): boolean {
  if (typeof email !== "string" || email.length === 0) return false;
  const at = email.lastIndexOf("@");
  if (at < 0) return false;
  const domain = email.slice(at + 1).toLowerCase();
  if (domain.length === 0) return false;
  return ALLOWED.includes(domain);
}
