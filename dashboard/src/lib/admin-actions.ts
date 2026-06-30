"use server";

/**
 * Owner-gated admin Server Actions — the network-reachable RPC entry points for
 * the Phase-13 operator panel (UM-04..UM-09) and the Phase-17 pod-config editor
 * (POD-CFG-07/10/11).
 *
 * SECURITY (CR-01 / CR-02 — Phase-17 review). This file carries `"use server"`,
 * so EVERY export below is a network-reachable Server Action whose arguments are
 * fully client-controlled. Two invariants are therefore enforced HERE:
 *
 *   1. Identity comes from the SESSION ONLY. Each wrapper calls `requireOwner()`
 *      with NO `actor` argument, forcing the cookie/session path. The caller can
 *      never assert their own `role` — closing the owner-gate bypass where an
 *      operator passed `{ actor: { role: "owner" } }` (CR-01).
 *   2. NO injectable `actor` / `auth` / `db` / `deps` in any public signature.
 *      Wrappers accept ONLY business arguments and delegate to the matching
 *      `*Core` impl in `@/lib/admin-actions-core` with the server-derived actor
 *      and the REAL deps. This removes the `db`-suppression and gateway-redirect
 *      seams from the network surface (CR-02 part 1).
 *
 * `requireOwner` and `writeAuditLog` are NOT exported here (they live in the
 * non-`"use server"` core), so they can never be RPC-invoked to forge or
 * suppress audit rows (CR-02 part 2). The `*Core` impls keep their injection
 * seams for in-process unit tests, which import them from the core module
 * directly — those seams are never reachable over the network.
 *
 * The UI islands (`operator-controls.tsx`, `pod-config-controls.tsx`) import the
 * public names below and already pass only business args.
 */

import {
  changePasswordCore,
  inviteOperatorCore,
  removeOperatorCore,
  requireOwner,
  resetOperator2FACore,
  resetOperatorPasswordCore,
  updatePodConfigBoundCore,
  updatePodConfigCore,
} from "@/lib/admin-actions-core";

// ──────────────────────────────────────────────────────────────────────────
// UM-04 — inviteOperator. Identity from session; @ifixtelecom enforced in core.
// ──────────────────────────────────────────────────────────────────────────

export async function inviteOperator(args: {
  name?: string;
  email: string;
}): Promise<void> {
  const { actor } = await requireOwner();
  await inviteOperatorCore({ actor, name: args.name, email: args.email });
}

// ──────────────────────────────────────────────────────────────────────────
// UM-05 — removeOperator. Identity from session.
// ──────────────────────────────────────────────────────────────────────────

export async function removeOperator(args: {
  targetId: string;
  targetEmail?: string;
}): Promise<void> {
  const { actor } = await requireOwner();
  await removeOperatorCore({
    actor,
    targetId: args.targetId,
    targetEmail: args.targetEmail,
  });
}

// ──────────────────────────────────────────────────────────────────────────
// UM-06 — resetOperatorPassword. Identity from session.
// ──────────────────────────────────────────────────────────────────────────

export async function resetOperatorPassword(args: {
  email: string;
  targetId?: string;
}): Promise<void> {
  const { actor } = await requireOwner();
  await resetOperatorPasswordCore({
    actor,
    email: args.email,
    targetId: args.targetId,
  });
}

// ──────────────────────────────────────────────────────────────────────────
// UM-07 — resetOperator2FA. Identity from session.
// ──────────────────────────────────────────────────────────────────────────

export async function resetOperator2FA(args: {
  targetId: string;
  targetEmail?: string;
}): Promise<void> {
  const { actor } = await requireOwner();
  await resetOperator2FACore({
    actor,
    targetId: args.targetId,
    targetEmail: args.targetEmail,
  });
}

// ──────────────────────────────────────────────────────────────────────────
// Self-service change-password (D-09) — NOT owner-gated, NOT audited.
// ──────────────────────────────────────────────────────────────────────────

export async function changePassword(args: {
  currentPassword: string;
  newPassword: string;
}): Promise<void> {
  await changePasswordCore({
    currentPassword: args.currentPassword,
    newPassword: args.newPassword,
  });
}

// ──────────────────────────────────────────────────────────────────────────
// POD-CFG-10/11 — owner-gated pod-config hot-field write. Identity from session.
// ──────────────────────────────────────────────────────────────────────────

export async function updatePodConfig(args: {
  field: string;
  value: unknown;
}): Promise<void> {
  const { actor } = await requireOwner();
  await updatePodConfigCore({ actor, field: args.field, value: args.value });
}

// ──────────────────────────────────────────────────────────────────────────
// POD-CFG-07/11 — owner-gated bound write. Identity from session.
// ──────────────────────────────────────────────────────────────────────────

export async function updatePodConfigBound(args: {
  field: string;
  value: unknown;
}): Promise<void> {
  const { actor } = await requireOwner();
  await updatePodConfigBoundCore({
    actor,
    field: args.field,
    value: args.value,
  });
}
