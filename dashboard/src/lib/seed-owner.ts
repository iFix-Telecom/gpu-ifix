/**
 * src/lib/seed-owner.ts — Phase 13 (D-02 / UM-03) pure owner-election logic.
 *
 * This is the single source of truth for the role-assignment DECISION used by
 * the one-shot owner backfill. The runtime script `scripts/dashboard/seed-owner.ts`
 * applies the SAME semantics directly in SQL against the live dashboard DB
 * (set-based, idempotent UPDATEs); this module expresses the identical rule as a
 * pure, side-effect-free function so it can be unit-tested in-memory without a
 * live Postgres (13-RESEARCH §Runtime State Inventory + Pattern 7).
 *
 * Rule (mirrors the script's two SQL statements):
 *   1. If NO user currently has role 'owner', promote the single earliest user
 *      (min createdAt) to 'owner'. Idempotent: when an owner already exists this
 *      is a no-op — never mint a second owner.
 *   2. Normalize every remaining null/undefined role to 'operator'.
 *
 * Mutates and returns the same array (callers may rely on identity or the return
 * value interchangeably).
 */

export type SeedUserRole = string | null;

export interface SeedUserLike {
  id: string;
  createdAt: Date;
  role: SeedUserRole;
}

export function seedOwner<T extends SeedUserLike>(users: T[]): T[] {
  // 1. Elect the earliest user as owner, but ONLY if no owner exists yet.
  const hasOwner = users.some((u) => u.role === "owner");
  if (!hasOwner && users.length > 0) {
    const earliest = users.reduce((min, u) =>
      u.createdAt.getTime() < min.createdAt.getTime() ? u : min,
    );
    earliest.role = "owner";
  }

  // 2. Normalize every still-null/undefined role to 'operator'.
  for (const u of users) {
    if (u.role == null) {
      u.role = "operator";
    }
  }

  return users;
}
