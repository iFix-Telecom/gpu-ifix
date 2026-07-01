/**
 * /settings/operadores — Operadores tab inside the Phase 07 Settings panel.
 *
 * UI-SPEC v2 §Settings → Operadores Tab: 4-column stat strip + operator
 * table. Provides operator-visible evidence that D-12/D-13/D-14/D-15 are
 * live in prod.
 *
 * Data source decision (Task 11-02-05 action note): Better Auth 1.4.22
 * does NOT ship `auth.api.listUsers` in the default plugin set (that
 * lives on the admin plugin which we deliberately do NOT install — 4
 * operators, no role escalation surface needed). We use a direct
 * Drizzle query against the `user` table (existing schema.ts) for the
 * roster, plus the twoFactor-plugin-managed `twoFactorEnabled` column
 * surfaced via raw SQL (CLI-canonical schema, not declared in
 * schema.ts per Task 11-02-03 rule). Session count uses a COUNT(*)
 * over `session.expires_at > NOW()` per user.
 *
 * Privacy (UI-SPEC §Privacy / redaction rules):
 *   - E-mails OK (already-authenticated operator viewing peers)
 *   - Last-login uses relative copy (agora / há 3h / há 2d / nunca)
 *   - NEVER displayed: TOTP secrets, backup codes, password hashes,
 *     IP addresses, session cookie values, raw user UUIDs.
 *
 * This is a server component (default Next.js 15 App Router).
 */
import { count, sql } from "drizzle-orm";
import { Bell, RefreshCw } from "lucide-react";
import { getDb, schema } from "@/lib/db";
import { getViewerRole } from "@/lib/viewer";
import { OperatorRowActions, ProvisionOperatorButton } from "./operator-controls";

type Operator = {
  id: string;
  name: string;
  email: string;
  role: string;
  twoFactorEnabled: boolean;
  lastSignIn: Date | null;
  openSessions: number;
};

async function loadOperators(): Promise<Operator[]> {
  const db = getDb();

  // Raw SQL for `twoFactorEnabled` (the column lives on the Better Auth
  // user table but is added by the twoFactor plugin via the CLI migrate —
  // not declared in schema.ts per the Task 11-02-03 canonical-CLI rule).
  // Fall back to `false` when the column doesn't exist yet (pre-migrate).
  let users: {
    id: string;
    name: string;
    email: string;
    role: string;
    twoFactorEnabled: boolean;
  }[] = [];
  try {
    type Row = {
      id: string;
      name: string;
      email: string;
      role: string | null;
      two_factor_enabled: boolean | null;
    };
    // Phase 13 (D-02): read the REAL `role` column (added by the Better Auth
    // admin plugin via the CLI-canonical migrate). NULL until the owner seed
    // runs → COALESCE to "operator" so the badge derivation is data-driven,
    // never the legacy `i===0` positional heuristic.
    const result = await db.execute<Row>(
      sql`SELECT id, name, email, COALESCE(role, 'operator') AS role, COALESCE(two_factor_enabled, false) AS two_factor_enabled FROM "user" ORDER BY created_at ASC`,
    );
    // drizzle-orm/node-postgres returns the underlying pg.QueryResult
    // ({ rows: Row[], ... }) — pluck `rows`. Some adapters return the
    // array directly, so prefer `rows` when present.
    const raw = result as unknown as { rows?: Row[] } | Row[];
    const list: Row[] = Array.isArray(raw) ? raw : (raw.rows ?? []);
    users = list.map((r) => ({
      id: r.id,
      name: r.name,
      email: r.email,
      role: r.role ?? "operator",
      twoFactorEnabled: r.two_factor_enabled === true,
    }));
  } catch (_e) {
    // Column missing (pre-migrate); query the schema.ts user table.
    const list = await db
      .select({
        id: schema.user.id,
        name: schema.user.name,
        email: schema.user.email,
      })
      .from(schema.user);
    users = list.map((r) => ({ ...r, role: "operator", twoFactorEnabled: false }));
  }

  // WR-02 fix: single grouped query for session stats — open-sessions
  // count + latest sign-in (max updated_at) per user. Previously this
  // loop issued 2 sequential SELECTs per user (classic N+1). The new
  // shape is O(1) DB roundtrips regardless of operator count.
  //
  // Session stats are SUPPLEMENTARY (last-login + open-session count). A
  // failure here (e.g. the session schema not materialised yet) must NOT
  // blank the whole roster — the role/2FA columns are the primary signal.
  // Degrade gracefully to empty stats instead of aborting loadOperators().
  const statsByUser = new Map<
    string,
    { openSessions: number; lastSignIn: Date | null }
  >();
  try {
    const sessionStats = await db
      .select({
        userId: schema.session.userId,
        openSessions: count(
          sql`CASE WHEN ${schema.session.expiresAt} > NOW() THEN 1 END`,
        ),
        lastSignIn: sql<Date | null>`MAX(${schema.session.updatedAt})`,
      })
      .from(schema.session)
      .groupBy(schema.session.userId);

    for (const s of sessionStats) {
      statsByUser.set(s.userId, {
        openSessions: Number(s.openSessions ?? 0),
        lastSignIn: s.lastSignIn
          ? new Date(s.lastSignIn as unknown as string)
          : null,
      });
    }
  } catch (_e) {
    // Session schema unavailable — render the roster without session stats.
  }

  const operators: Operator[] = users.map((u) => {
    const stats = statsByUser.get(u.id);
    return {
      id: u.id,
      name: u.name,
      email: u.email,
      role: u.role,
      twoFactorEnabled: u.twoFactorEnabled,
      lastSignIn: stats?.lastSignIn ?? null,
      openSessions: stats?.openSessions ?? 0,
    };
  });
  return operators;
}

function relativeTime(d: Date | null): string {
  if (!d) return "nunca";
  const diffMs = Date.now() - d.getTime();
  const min = Math.floor(diffMs / 60_000);
  if (min < 1) return "agora";
  if (min < 60) return `há ${min}min`;
  const h = Math.floor(min / 60);
  if (h < 24) return `há ${h}h`;
  const days = Math.floor(h / 24);
  return `há ${days}d`;
}

function initials(name: string): string {
  const parts = name.trim().split(/\s+/);
  if (parts.length === 0) return "??";
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase();
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
}

export default async function OperadoresPage() {
  let operators: Operator[] = [];
  let loadError: string | null = null;
  try {
    operators = await loadOperators();
  } catch (e) {
    loadError = (e as Error).message ?? "erro ao carregar operadores";
  }

  // Owner-gate (D-03): read the acting viewer's role. UI hiding is COSMETIC —
  // the Plan-03 server actions re-check `role === "owner"` on every admin op.
  // A null/non-owner role hides every admin control (fail-closed).
  const viewerRole = await getViewerRole();
  const isOwner = viewerRole === "owner";

  const totalOperators = operators.length;
  const twoFAActive = operators.filter((o) => o.twoFactorEnabled).length;
  const twoFAPending = totalOperators - twoFAActive;
  const openSessionsAll = operators.reduce((s, o) => s + o.openSessions, 0);

  return (
    <main className="flex flex-col gap-6">
      {/* Page header */}
      <header className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">
            Configurações
          </h1>
          <p className="text-xs text-muted-foreground mt-1">
            ai-dashboard.converse-ai.app · {totalOperators} operadores · TOTP
            obrigatório
          </p>
        </div>
        <div className="flex items-center gap-2">
          <button
            type="button"
            className="rounded-md p-2 text-muted-foreground hover:text-foreground"
            aria-label="Atualizar"
          >
            <RefreshCw className="size-4" />
          </button>
          <button
            type="button"
            className="rounded-md p-2 text-muted-foreground hover:text-foreground"
            aria-label="Avisos"
          >
            <Bell className="size-4" />
          </button>
        </div>
      </header>

      {/* Tab bar — Geral / Integrações / Chaves admin / Operadores (active) */}
      <nav
        className="flex gap-6 border-b border-border"
        aria-label="Configurações"
      >
        {[
          { id: "geral", label: "Geral", active: false },
          { id: "integracoes", label: "Integrações", active: false },
          { id: "chaves", label: "Chaves admin", active: false },
          { id: "operadores", label: "Operadores", active: true },
        ].map((t) => (
          <span
            key={t.id}
            className={`py-2 text-sm ${
              t.active
                ? "border-b-2 font-semibold text-foreground"
                : "text-muted-foreground"
            }`}
            style={
              t.active
                ? { borderBottomColor: "var(--primary)" }
                : undefined
            }
            aria-current={t.active ? "page" : undefined}
          >
            {t.label}
          </span>
        ))}
      </nav>

      {/* 4-column stat strip */}
      <section
        className="grid"
        style={{ gridTemplateColumns: "repeat(4, 1fr)", gap: 16 }}
      >
        <StatCard
          label="Operadores"
          value={String(totalOperators)}
          sub="allowlist @ifixtelecom.com.br"
        />
        <StatCard
          label="2FA ativos"
          value={String(twoFAActive)}
          sub={
            twoFAPending > 0
              ? `${twoFAPending} pendente enroll`
              : "todos enrolados"
          }
          tone={twoFAPending > 0 ? "warning" : "default"}
        />
        <StatCard
          label="Sessões abertas"
          value={String(openSessionsAll)}
          sub="idle timeout 30 min"
        />
        <StatCard
          label="Rate-limit /login"
          value="5/15min"
          sub="por IP · D-14"
        />
      </section>

      {/* Operadores table */}
      <section className="rounded-md border border-border">
        <div className="flex items-center justify-between px-4 py-3 border-b border-border">
          <h2 className="text-sm font-semibold">Operadores</h2>
          {/* Owner-gate (D-03): provision control renders ONLY for an owner. */}
          {isOwner && <ProvisionOperatorButton />}
        </div>
        {loadError ? (
          <p className="p-4 text-xs text-destructive" role="alert">
            Erro ao carregar operadores: {loadError}
          </p>
        ) : totalOperators === 0 ? (
          <p className="p-4 text-xs text-muted-foreground">
            Nenhum operador cadastrado.
          </p>
        ) : (
          <table
            className="w-full text-xs"
            style={{ fontVariantNumeric: "tabular-nums" }}
          >
            <thead>
              <tr className="text-muted-foreground uppercase tracking-wider">
                <th className="text-left font-semibold" style={{ padding: "8px 12px" }}>
                  Operador
                </th>
                <th className="text-left font-semibold" style={{ padding: "8px 12px" }}>
                  Função
                </th>
                <th className="text-left font-semibold" style={{ padding: "8px 12px" }}>
                  Último login
                </th>
                <th className="text-left font-semibold" style={{ padding: "8px 12px" }}>
                  2FA
                </th>
                <th className="text-right font-semibold" style={{ padding: "8px 12px" }}>
                  Sessões
                </th>
                <th aria-label="ações" style={{ padding: "8px 12px" }} />
              </tr>
            </thead>
            <tbody>
              {operators.map((o) => (
                <tr
                  key={o.id}
                  className="border-t border-border hover:bg-[color:var(--row-hover,transparent)]"
                  style={{ height: 36 }}
                >
                  <td style={{ padding: "8px 12px" }}>
                    <div className="flex items-center gap-3">
                      <span
                        aria-hidden
                        className="flex h-7 w-7 items-center justify-center rounded-full text-[11px] font-semibold"
                        style={{
                          background:
                            "color-mix(in oklch, var(--primary) 18%, var(--card))",
                          color: "var(--primary)",
                        }}
                      >
                        {initials(o.name)}
                      </span>
                      <div>
                        <div className="text-foreground">{o.name}</div>
                        <div className="text-muted-foreground text-[11px]">
                          {o.email}
                        </div>
                      </div>
                    </div>
                  </td>
                  <td style={{ padding: "8px 12px" }}>
                    {/* D-02: badge derives from the REAL `role` column — owner
                        → warning tone, operator → neutral. Preserve the
                        color-mix styling + 2px 8px padding (no grid shift). */}
                    <span
                      className="rounded-md text-[11px] font-semibold"
                      style={{
                        padding: "2px 8px",
                        background:
                          o.role === "owner"
                            ? "color-mix(in oklch, var(--status-warning, oklch(0.769 0.188 70.08)) 16%, var(--card))"
                            : "var(--surface-tint-strong, var(--card))",
                        color:
                          o.role === "owner"
                            ? "var(--status-warning, oklch(0.769 0.188 70.08))"
                            : "var(--muted-foreground)",
                      }}
                    >
                      {o.role === "owner" ? "owner" : "operator"}
                    </span>
                  </td>
                  <td style={{ padding: "8px 12px" }} className="text-muted-foreground">
                    {relativeTime(o.lastSignIn)}
                  </td>
                  <td style={{ padding: "8px 12px" }}>
                    <span
                      className="rounded-md text-[11px] font-semibold"
                      style={{
                        padding: "2px 8px",
                        background: o.twoFactorEnabled
                          ? "color-mix(in oklch, var(--primary) 18%, var(--card))"
                          : "color-mix(in oklch, var(--status-warning, oklch(0.769 0.188 70.08)) 16%, var(--card))",
                        color: o.twoFactorEnabled
                          ? "var(--primary)"
                          : "var(--status-warning, oklch(0.769 0.188 70.08))",
                      }}
                    >
                      {o.twoFactorEnabled ? "ativo" : "aguardando enroll"}
                    </span>
                  </td>
                  <td
                    style={{ padding: "8px 12px" }}
                    className="text-right tabular-nums"
                  >
                    {o.openSessions}
                  </td>
                  <td style={{ padding: "8px 12px" }} className="text-right">
                    {/* Owner-gate (D-03): the per-row ··· menu renders ONLY for
                        an owner. OperatorRowActions replaces the literal "···"
                        with a <MoreHorizontal/> trigger (aria-label preserved)
                        and wires the dropdown-menu + confirms to the Plan-03
                        server actions. Server actions re-check regardless. */}
                    {isOwner && (
                      <OperatorRowActions
                        name={o.name}
                        email={o.email}
                        userId={o.id}
                      />
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <p
          className="text-[11px] text-muted-foreground px-4 py-2 border-t border-border"
          style={{ letterSpacing: "0.01em" }}
        >
          operadores gerenciados pelo painel
        </p>
      </section>
    </main>
  );
}

function StatCard({
  label,
  value,
  sub,
  tone = "default",
}: {
  label: string;
  value: string;
  sub: string;
  tone?: "default" | "warning";
}) {
  const toneClass =
    tone === "warning"
      ? "border-[color:var(--status-warning,oklch(0.769_0.188_70.08))]"
      : "border-border";
  return (
    <div
      className={`rounded-md border bg-card p-4 ${toneClass}`}
      style={{ borderColor: tone === "warning" ? undefined : undefined }}
    >
      <p className="text-[11px] uppercase tracking-wider text-muted-foreground">
        {label}
      </p>
      <p className="text-2xl font-semibold mt-1 tabular-nums">{value}</p>
      <p className="text-[11px] text-muted-foreground mt-1">{sub}</p>
    </div>
  );
}
