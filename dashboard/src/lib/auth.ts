/**
 * Standalone Better Auth instance for the ifix-ai-gateway dashboard.
 *
 * Source: 07-RESEARCH.md verbatim example (lines 471-505) + the converseai-v4
 * pattern, stripped to a single capability. CONTEXT.md locks this as a
 * STANDALONE instance — NOT a shared session with converseai-v4 — for ~4 Ifix
 * admins.
 *
 * ONLY `emailAndPassword` is enabled. Every other converseai-v4 plugin
 * (organization, twoFactor, admin, apiKey, phoneNumber, …) is intentionally
 * stripped: a 4-admin internal monitoring tool has no org/role-escalation
 * surface to manage (RESEARCH.md threat T-07-27).
 */
import { betterAuth } from "better-auth";
import { drizzleAdapter } from "better-auth/adapters/drizzle";
import { db, schema } from "./db"; // dashboard's OWN db, NOT ai_gateway

export const auth = betterAuth({
  baseURL: process.env.BETTER_AUTH_URL,
  secret: process.env.BETTER_AUTH_SECRET,
  database: drizzleAdapter(db, { provider: "pg", schema }),
  // 4 admins, internal tool — no email verification flow needed.
  emailAndPassword: { enabled: true },
  session: { expiresIn: 60 * 60 * 24 * 7 }, // 7 days
  advanced: { database: { generateId: () => crypto.randomUUID() } },
});

export type Auth = typeof auth;
