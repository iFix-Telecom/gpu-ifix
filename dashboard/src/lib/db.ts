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
import * as schema from "./schema";

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
 * Drizzle client over the dashboard's OWN auth DB. Proxied so the underlying
 * Pool + client are constructed on first property access rather than at
 * import — `next build` evaluates this module with no DSN in env.
 */
export const db = new Proxy({} as DrizzleClient, {
  get(_target, prop, receiver) {
    return Reflect.get(getClient(), prop, receiver);
  },
});

export { schema };
