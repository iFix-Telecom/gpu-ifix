#!/usr/bin/env bun
/**
 * scripts/dashboard/seed-owner.ts — Phase 13 (D-02 / UM-03) one-shot,
 * idempotent owner backfill for the standalone Better Auth dashboard.
 *
 * CONTEXT (13-RESEARCH §Runtime State Inventory + Pitfall 4): the `admin`
 * plugin's `role` column is nullable with NO default, so after the
 * schema push EVERY existing `user` row has `role IS NULL`. With no owner,
 * the admin permission gate (`adminRoles:["owner"]`) denies EVERYONE and
 * the dashboard becomes unmanageable. This script MUST run IMMEDIATELY
 * after `bunx drizzle-kit push` (coupled, same session, no gap) to elect
 * the earliest user as `owner` and normalize the rest to `operator`.
 *
 * IDEMPOTENT (re-run = no-op once an owner exists), per 13-RESEARCH
 * Pattern 7:
 *   1. Promote the single earliest user (created_at ASC) to 'owner' —
 *      but ONLY if no owner already exists.
 *   2. Normalize every remaining NULL role to 'operator'.
 *
 * SAFETY (mirrors scripts/dashboard/seed-admins.sh principles):
 *   - Fail-fast if DASHBOARD_DATABASE_URL is unset (the dashboard's OWN
 *     isolated DB, never ai_gateway — 07-RESEARCH Pitfall 7).
 *   - Prints NO secrets: no passwords, no DSN, no row contents — only
 *     aggregate counts (owners, operators) so the operator can confirm
 *     "exactly 1 owner, 0 NULL roles" without leaking identity.
 *
 * Run (from repo root, after the prod schema push):
 *   DASHBOARD_DATABASE_URL=... bun run scripts/dashboard/seed-owner.ts
 */
import { sql } from "drizzle-orm";
import { getDb } from "../../dashboard/src/lib/db";

async function main(): Promise<void> {
  if (!process.env.DASHBOARD_DATABASE_URL) {
    // Fail-fast — the dashboard's isolated auth DB is required. Do NOT
    // print the value (it carries credentials).
    console.error(
      "DASHBOARD_DATABASE_URL is not set. seed-owner needs the dashboard's " +
        "OWN Postgres DSN (bd_ai_dashboard_prod / public), isolated from " +
        "the gateway's ai_gateway schema.",
    );
    process.exit(1);
  }

  const db = getDb();

  // 1. Elect the earliest user as owner, but only if no owner exists yet.
  //    Idempotent: re-run when an owner already exists → the NOT EXISTS
  //    guard makes this a no-op.
  await db.execute(sql`
    UPDATE "user" SET role = 'owner'
    WHERE id = (SELECT id FROM "user" ORDER BY created_at ASC LIMIT 1)
      AND NOT EXISTS (SELECT 1 FROM "user" WHERE role = 'owner')
  `);

  // 2. Normalize every still-NULL role to 'operator'.
  await db.execute(sql`
    UPDATE "user" SET role = 'operator' WHERE role IS NULL
  `);

  // Aggregate-only assertions (no row identity printed).
  const owners = await db.execute(
    sql`SELECT count(*)::int AS n FROM "user" WHERE role = 'owner'`,
  );
  const nullRoles = await db.execute(
    sql`SELECT count(*)::int AS n FROM "user" WHERE role IS NULL`,
  );
  const operators = await db.execute(
    sql`SELECT count(*)::int AS n FROM "user" WHERE role = 'operator'`,
  );

  // node-postgres returns { rows }; drizzle's execute surfaces that shape.
  const rowN = (r: unknown): number => {
    const rows = (r as { rows?: Array<{ n?: number }> }).rows ?? [];
    return rows[0]?.n ?? 0;
  };
  const ownerCount = rowN(owners);
  const nullCount = rowN(nullRoles);
  const operatorCount = rowN(operators);

  console.log(
    `seed-owner: owners=${ownerCount} operators=${operatorCount} null_roles=${nullCount}`,
  );

  if (ownerCount !== 1 || nullCount !== 0) {
    console.error(
      `seed-owner FAILED invariant: expected exactly 1 owner and 0 NULL ` +
        `roles, got owners=${ownerCount} null_roles=${nullCount}.`,
    );
    process.exit(1);
  }

  console.log("seed-owner OK: exactly 1 owner, 0 NULL roles.");
  process.exit(0);
}

main().catch((err) => {
  // Never print the DSN or secrets; surface the error message only.
  console.error(`seed-owner error: ${(err as Error)?.message ?? String(err)}`);
  process.exit(1);
});
