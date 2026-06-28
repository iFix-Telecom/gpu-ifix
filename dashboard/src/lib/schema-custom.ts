/**
 * Custom (non-Better-Auth) Drizzle tables — Phase 13 D-08 audit log.
 *
 * WHY a SEPARATE file (13-RESEARCH Pitfall 1): `schema.ts` is REGENERATED
 * by the Better Auth CLI, which emits ONLY the auth tables and DROPS any
 * hand-added custom table. Keeping `admin_audit_log` here — in a file the
 * generator never touches — means it survives every `generate` run. It is
 * merged into the drizzle client via the `{ ...authSchema, ...customSchema }`
 * spread in `db.ts`, and `drizzle.config.ts` globs `./src/lib/schema*.ts`
 * so `drizzle-kit push` migrates this table alongside the auth tables.
 *
 * Isolation invariant (07-RESEARCH Pitfall 7): this table lives in the
 * dashboard's OWN database (`bd_ai_dashboard_prod`, `public` schema),
 * NEVER in the gateway's `ai_gateway`.
 *
 * D-09: only owner-gated admin ops (invite/remove/reset-password/reset-2FA)
 * write rows here; the self-service change-password flow does NOT.
 *
 * Privacy (UI-SPEC §Privacy): NEVER store TOTP secrets, backup codes,
 * password hashes, the random invite password, IPs, or cookies in
 * `metadata` — store only the action + the actor/target identity.
 */
import { index, jsonb, pgTable, text, timestamp } from "drizzle-orm/pg-core";

export const adminAuditLog = pgTable(
  "admin_audit_log",
  {
    // crypto.randomUUID() — matches advanced.database.generateId in auth.ts
    // and the user.id idiom in schema.ts.
    id: text("id").primaryKey(),
    actorId: text("actor_id").notNull(),
    actorEmail: text("actor_email").notNull(),
    targetId: text("target_id"),
    targetEmail: text("target_email"),
    action: text("action").notNull(),
    metadata: jsonb("metadata"),
    createdAt: timestamp("created_at").defaultNow().notNull(),
  },
  (table) => [index("admin_audit_log_actor_idx").on(table.actorId)],
);
