/**
 * RED test stub for the one-shot idempotent owner seed (UM-03, Phase 13).
 *
 * Contract (D-03 / RESEARCH §Architecture):
 *   - The EARLIEST-created user (min created_at) → role='owner'.
 *   - Every other existing user → role='operator'.
 *   - Re-running the seed is a NO-OP when an owner already exists
 *     (idempotent — exactly one role='owner' after any number of runs).
 *
 * EXPECTED TO FAIL (RED) until Wave 1 implements `src/lib/seed-owner.ts`.
 * The module does not exist yet, so the test does a guarded dynamic
 * import and asserts on the resolved module — a missing module fails as
 * an ASSERTION (RED), not a collection/import error.
 *
 * The user roster is modeled as a plain in-memory array mirroring the
 * `user` table shape (id, email, createdAt, role) so the seed's ordering
 * + idempotency logic is exercised without a live Postgres.
 */
import { describe, expect, it } from "vitest";

type SeedUser = {
  id: string;
  email: string;
  createdAt: Date;
  role: string | null;
};

/**
 * Guarded dynamic import — returns the module or null (RED-friendly).
 *
 * The specifier is built from a variable so Vite's static `import-analysis`
 * plugin does NOT try to resolve it at transform time (a string-literal
 * `import("@/lib/seed-owner")` of a not-yet-created module fails the whole
 * SUITE at collection, not as a RED assertion — see 13-01 plan acceptance
 * criteria "RED, not import error"). At runtime the missing module rejects
 * and is caught → the test reports a FAILING ASSERTION (RED).
 */
async function importSeedOwner(): Promise<Record<string, unknown> | null> {
  const specifier = ["@/lib", "seed-owner"].join("/");
  try {
    return (await import(/* @vite-ignore */ specifier)) as Record<
      string,
      unknown
    >;
  } catch {
    return null;
  }
}

function roster(): SeedUser[] {
  return [
    {
      id: "u-2",
      email: "second@ifixtelecom.com.br",
      createdAt: new Date("2026-02-01T00:00:00Z"),
      role: null,
    },
    {
      id: "u-1",
      email: "first@ifixtelecom.com.br",
      createdAt: new Date("2026-01-01T00:00:00Z"), // earliest
      role: null,
    },
    {
      id: "u-3",
      email: "third@ifixtelecom.com.br",
      createdAt: new Date("2026-03-01T00:00:00Z"),
      role: null,
    },
  ];
}

describe("seed-owner — UM-03 idempotent owner seed (RED until Wave 1)", () => {
  it("assigns role='owner' to the earliest user and 'operator' to the rest", async () => {
    const mod = await importSeedOwner();
    // RED: module absent today → fails here as an assertion, not import error.
    expect(mod, "@/lib/seed-owner must export seedOwner").not.toBeNull();
    const seedOwner = mod?.seedOwner as
      | ((users: SeedUser[]) => Promise<SeedUser[]> | SeedUser[])
      | undefined;
    expect(typeof seedOwner).toBe("function");

    const users = roster();
    const result = await seedOwner?.(users);
    const out = (result ?? users) as SeedUser[];

    const owners = out.filter((u) => u.role === "owner");
    expect(owners.length).toBe(1);
    expect(owners[0].id).toBe("u-1"); // earliest createdAt
    const operators = out.filter((u) => u.role === "operator");
    expect(operators.length).toBe(2);
  });

  it("is idempotent: re-running when an owner already exists is a no-op", async () => {
    const mod = await importSeedOwner();
    expect(mod, "@/lib/seed-owner must export seedOwner").not.toBeNull();
    const seedOwner = mod?.seedOwner as
      | ((users: SeedUser[]) => Promise<SeedUser[]> | SeedUser[])
      | undefined;
    expect(typeof seedOwner).toBe("function");

    const users = roster();
    users[1].role = "owner"; // u-1 already owner
    users[0].role = "operator";
    users[2].role = "operator";

    const result = await seedOwner?.(users);
    const out = (result ?? users) as SeedUser[];

    const owners = out.filter((u) => u.role === "owner");
    expect(owners.length).toBe(1);
    expect(owners[0].id).toBe("u-1"); // unchanged — no second owner minted
  });
});
