/**
 * RED test stub for the role-aware operadores roster (UM-10, Phase 13).
 *
 * Contract (UM-10 / T-13-authz / D-03):
 *   - The roster renders a ROLE BADGE sourced from the real `role` column
 *     (owner → warning/destaque, operator → neutral) — NOT the legacy
 *     `i===0` positional heuristic.
 *   - A non-owner viewer sees NO owner-only controls: no "+ Provisionar"
 *     button and no per-row "···" action menu (owner-gate; UI hiding is
 *     cosmetic but required — server enforcement lives in admin-actions).
 *
 * EXPECTED TO FAIL (RED) until Wave 3 wires the role column + owner-gate
 * into `operadores/page.tsx`. The page is an async Server Component; the
 * test awaits its element output and renders it. Today's implementation
 * neither reads `role` nor gates controls, so these assertions FAIL (RED)
 * — they are real assertion failures, not collection/import errors.
 *
 * `@/lib/db` is mocked so no live Postgres is touched. A viewer-role
 * source (`@/lib/viewer`) is mocked to flip the owner-gate; the impl may
 * read the viewer role differently — the assertions key off RENDERED
 * OUTPUT (badge text / control presence), which is implementation-shape
 * agnostic.
 */
import { render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const { viewerMock } = vi.hoisted(() => ({ viewerMock: vi.fn() }));

// Roster with REAL role values (owner + operator) returned by the mocked
// drizzle client, regardless of which query shape the page uses.
// NOTE (13-05): the names/emails below are deliberately chosen so they do
// NOT themselves contain the role words ("owner"/"operator"/"operador"). The
// badge assertions use a singular `getByText(/\bowner\b.../)`, which would
// error on multiple matches if a roster name/email ALSO contained the role
// word. The `role` field (the actual UM-10 contract — the badge must derive
// from the real `role` column, not `i===0`) is unchanged.
const ROSTER = [
  {
    id: "u-1",
    name: "Ana Diretora",
    email: "ana@ifixtelecom.com.br",
    role: "owner",
    two_factor_enabled: true,
    twoFactorEnabled: true,
  },
  {
    id: "u-2",
    name: "Bruno Suporte",
    email: "bruno@ifixtelecom.com.br",
    role: "operator",
    two_factor_enabled: false,
    twoFactorEnabled: false,
  },
];

vi.mock("@/lib/db", () => {
  const chain = {
    select: () => chain,
    from: () => chain,
    where: () => chain,
    groupBy: () => Promise.resolve([]),
    orderBy: () => Promise.resolve(ROSTER),
    then: (r: (v: unknown) => unknown) => Promise.resolve(ROSTER).then(r),
  };
  const db = {
    execute: async () => ({ rows: ROSTER }),
    select: () => chain,
  };
  return { getDb: () => db, db, schema: {} };
});

// Likely Wave-3 viewer-role source. If the impl reads the role elsewhere,
// the rendered-output assertions below still hold.
vi.mock("@/lib/viewer", () => ({
  getViewerRole: viewerMock,
}));

/** Render the awaited async Server Component output. */
async function renderOperadores(): Promise<boolean> {
  try {
    const mod = (await import("@/app/(dashboard)/settings/operadores/page")) as {
      default: (props?: unknown) => Promise<React.ReactElement> | React.ReactElement;
    };
    const el = await mod.default({});
    render(el);
    return true;
  } catch {
    return false;
  }
}

describe("operadores roster — UM-10 real-role badge + owner-gate (RED until Wave 3)", () => {
  it("renders a role badge from the real role column (owner + operator)", async () => {
    viewerMock.mockResolvedValue("owner");
    const ok = await renderOperadores();
    expect(ok, "operadores/page must render with mocked db").toBe(true);

    // Scope the badge queries to the roster's table body so the assertions
    // target the per-row role BADGE, not the page chrome (the "Operador"
    // column header in <thead> and the "+ Provisionar operador" button both
    // legitimately contain the role word but are not the badge). The badge
    // contract — derived from the real `role` column, not `i===0` — is what
    // UM-10 verifies; rowgroup scoping keeps the regex + intent intact.
    const body = screen.getAllByRole("rowgroup").at(-1) as HTMLElement;
    const rows = within(body);

    // Owner role badge present (text 'owner' / 'Owner' / 'Dono').
    expect(
      rows.getByText(/\bowner\b|\bdono\b/i),
      "owner role badge must render from real role column",
    ).toBeInTheDocument();
    // Operator role badge present.
    expect(
      rows.getByText(/\boperator\b|\boperador\b/i),
      "operator role badge must render from real role column",
    ).toBeInTheDocument();
  });

  it("hides + Provisionar and ··· row menu for a non-owner viewer (owner-gate)", async () => {
    viewerMock.mockResolvedValue("operator");
    const ok = await renderOperadores();
    expect(ok).toBe(true);

    // Owner-only controls MUST be absent for a non-owner viewer.
    expect(
      screen.queryByRole("button", { name: /provisionar/i }),
      "+ Provisionar must be hidden for non-owner",
    ).toBeNull();
    expect(
      screen.queryByRole("button", { name: /ações|açao|menu|···|\.\.\./i }),
      "··· row action menu must be hidden for non-owner",
    ).toBeNull();
  });
});
