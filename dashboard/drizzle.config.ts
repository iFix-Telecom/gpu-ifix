/**
 * drizzle-kit migration runner config.
 *
 * CR-02: TLS verification must be ENABLED for any non-localhost DSN.
 * Previously this file shipped `rejectUnauthorized: false`, which trusts
 * any certificate on the wire — a MITM downgrade hole for migrations
 * pushed over the public network to managed Postgres (DO managed cluster
 * reachable over public IP; operators run `bunx drizzle-kit push` from
 * ops-claude through Tailscale → DO).
 *
 * Operator workflow:
 *   1. Download the DO managed Postgres CA bundle from the cluster
 *      "Overview → Connection details → Download CA certificate" panel
 *      (see https://docs.digitalocean.com/products/databases/postgresql/how-to/connect/#configure-an-ssl-certificate).
 *   2. Save it to a secure path on the operator host, e.g.
 *      `/etc/ssl/ifix/do-managed-pg-ca.crt` (mode 644).
 *   3. Export `DASHBOARD_DB_CA_CERT=/etc/ssl/ifix/do-managed-pg-ca.crt`
 *      alongside `DASHBOARD_DATABASE_URL` before running drizzle-kit.
 *   4. When `DASHBOARD_DB_CA_CERT` is NOT set, the config falls back to
 *      `rejectUnauthorized: true` with system root CAs only. If the DO
 *      managed Postgres presents a cert chained to a CA outside the
 *      system trust store, the connection will fail loudly — that is
 *      the intended fail-safe (better than silently trusting MITM).
 *
 * Localhost DSNs (`localhost` or `@127.`) opt out of TLS entirely, since
 * dev `docker compose` Postgres has no TLS termination.
 */
import { readFileSync } from "node:fs";
import { defineConfig } from "drizzle-kit";

if (!process.env.DASHBOARD_DATABASE_URL) {
  throw new Error("DASHBOARD_DATABASE_URL must be set for drizzle-kit");
}

const url = process.env.DASHBOARD_DATABASE_URL;
const isLocalhost = /\/\/localhost|@127\./.test(url);

const caCertPath = process.env.DASHBOARD_DB_CA_CERT;
const sslConfig = isLocalhost
  ? (false as const)
  : {
      rejectUnauthorized: true as const,
      // Pin the DO managed Postgres CA when DASHBOARD_DB_CA_CERT is set.
      // If unset, Node falls back to the system root CA bundle and will
      // fail loudly on managed-DB cert chains that are not in that
      // bundle — by design, do NOT downgrade.
      ...(caCertPath ? { ca: readFileSync(caCertPath, "utf8") } : {}),
    };

export default defineConfig({
  schema: "./src/lib/schema.ts",
  dialect: "postgresql",
  dbCredentials: {
    url,
    ssl: sslConfig,
  },
  // WR-08: verbose drizzle-kit output may include DDL with embedded
  // values during backfill UPDATEs (e.g. on a schema rename). Gate
  // verbosity off in production runs; operators who need verbose logs
  // for diagnostics can run with NODE_ENV=development locally.
  verbose: process.env.NODE_ENV !== "production",
  strict: true,
});
