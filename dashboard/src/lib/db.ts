/**
 * Dashboard auth database — ISOLATED from the gateway's `ai_gateway` schema.
 *
 * RESEARCH.md Pitfall 7: pointing Better Auth's drizzleAdapter at the
 * gateway's Postgres + `ai_gateway` schema would pollute it with
 * user/session/account tables (or worse, collide). This client connects via
 * `DASHBOARD_DATABASE_URL` — a DB/schema dedicated to the dashboard's auth
 * (`dashboard_auth`), with NO `ai_gateway` connection string anywhere in the
 * dashboard codebase.
 *
 * The Better Auth tables (user/session/account/verification) are created by
 * `npx @better-auth/cli migrate` run against DASHBOARD_DATABASE_URL — a
 * one-time deploy step documented in the plan's user_setup block.
 *
 * The connection is LAZY: the Pool + drizzle client are constructed on first
 * property access, not at module load. This keeps `next build` (which
 * evaluates route modules with no runtime env) from failing — the DSN is a
 * deploy-time requirement, not a build-time one.
 */
import { drizzle } from "drizzle-orm/node-postgres";
import { Pool } from "pg";
import * as authSchema from "./schema";
import * as customSchema from "./schema-custom";

// Merge the CLI-canonical auth schema with the hand-maintained custom
// tables (admin_audit_log) so the drizzle client knows BOTH (13-RESEARCH
// Pitfall 1). `schema` is still re-exported below so existing callers
// (`import { schema } from "@/lib/db"`) keep working and gain
// `schema.adminAuditLog`.
const schema = { ...authSchema, ...customSchema };

type DrizzleClient = ReturnType<typeof drizzle<typeof schema>>;

let client: DrizzleClient | undefined;

function getClient(): DrizzleClient {
  if (client) return client;

  const connectionString = process.env.DASHBOARD_DATABASE_URL;
  if (!connectionString) {
    // Fail fast at first use — Better Auth cannot operate without its own
    // isolated DB. (Thrown at request time, not build time.)
    throw new Error(
      "DASHBOARD_DATABASE_URL is not set. The dashboard needs its OWN Postgres " +
        "schema/db for Better Auth, isolated from the gateway's ai_gateway schema " +
        "(see dashboard/.env.example).",
    );
  }

  const pool = new Pool({ connectionString });
  client = drizzle(pool, { schema });
  return client;
}

/**
 * Drizzle client over the dashboard's OWN auth DB.
 *
 * WR-09: the lazy-init goal (defer Pool construction past `next build`,
 * which evaluates this module with no DSN in env) is valid, but a Proxy
 * is the wrong tool — `Reflect.get(target, prop, receiver)` with the
 * proxy as `receiver` runs any getter on the real client with `this` =
 * the proxy, and hands back unbound methods, so `instanceof` and
 * `const { select } = db` break opaquely on a drizzle minor bump.
 *
 * Instead, `db` is a Proxy ONLY as a thin lazy-access shim, but every
 * access is forwarded to the REAL client as the `Reflect.get` receiver —
 * so getters run with the correct `this` and methods stay bound to the
 * real drizzle client, never the proxy. Prefer `getDb()` in new code:
 * it returns the real client object directly with no shim at all.
 */
export function getDb(): DrizzleClient {
  return getClient();
}

export const db = new Proxy({} as DrizzleClient, {
  get(_target, prop) {
    const real = getClient();
    // Forward to the REAL client as the receiver — never the proxy — so
    // getters see the right `this` and methods come back bound correctly.
    return Reflect.get(real, prop, real);
  },
});

export { schema };
