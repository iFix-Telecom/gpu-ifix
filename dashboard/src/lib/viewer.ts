/**
 * Viewer-role resolver for owner-gated UI (Phase 13, D-02/D-03).
 *
 * The operadores RSC reads the *acting* viewer's role from the live Better
 * Auth session to decide which owner-only controls render. This is a thin
 * wrapper over `auth.api.getSession({ headers })` so that:
 *
 *   1. The page never imports `next/headers` / `auth` directly at the call
 *      site — the role-read is one named seam (`getViewerRole`), which the
 *      UM-10 RED suite (`operadores/page.test.tsx`) mocks to flip the gate
 *      without standing up a request scope or a real session.
 *   2. The owner-gate is COSMETIC. The authoritative re-check lives in the
 *      Plan-03 server actions (`requireOwner` in `@/lib/admin-actions`),
 *      which re-validate `role === "owner"` server-side on EVERY admin op
 *      (D-03 / T-13-authz). A hidden-but-callable control is still gated.
 *
 * Returns the role string (`"owner"` / `"operator"` / any custom role) or
 * `null` when there is no session / the session read fails. A `null` role is
 * treated as a non-owner by callers (fail-closed for the gate).
 */
import { headers } from "next/headers";
import { auth } from "@/lib/auth";

export async function getViewerRole(): Promise<string | null> {
  try {
    const session = await auth.api.getSession({ headers: await headers() });
    const role = (session?.user as { role?: string } | undefined)?.role;
    return role ?? null;
  } catch {
    // No request scope / session backend unavailable → fail-closed (non-owner).
    return null;
  }
}
