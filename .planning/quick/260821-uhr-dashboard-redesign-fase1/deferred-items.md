# Deferred items — quick 260821-uhr

Out-of-scope discoveries found while executing this plan. NOT fixed here (they
are not caused by any file this plan touches).

## 1. Vitest default 5s timeout flakes in the auth/db suites

**FACT (measured, 2026-08-21):** with the default `testTimeout` (5000ms), a
varying subset of these files fails with `Test timed out in 5000ms`:

- `src/app/(dashboard)/settings/operadores/page.test.tsx`
- `src/lib/admin-actions.test.ts`
- `src/lib/auth.test.ts`

The set changes run to run (1 failure in one run, 3 in the next). With
`npx vitest run --testTimeout=30000 --pool=forks --poolOptions.forks.maxForks=2`
the FULL suite is green: **16 files / 83 tests passed**.

**FACT:** none of these files import any module changed by this plan. The
operadores page imports only `drizzle-orm`, `lucide-react`, `@/lib/db`,
`@/lib/viewer` and `./operator-controls`. The failing assertion is never
reached — the test dies on the timer, and the same test passes in ~8s when
given room.

**FACT:** one run also aborted with a native node assertion,
`Assertion failed: (0) == (uv_thread_create(...))` at `node_platform.cc:109` —
the box ran out of thread headroom.

**HIPÓTESE:** the root cause is cold module-transform plus better-auth's
password hashing (CPU-bound) exceeding 5s when the machine is loaded. The
evidence that would settle it: run the suite on an idle box and time the first
test of `operadores/page.test.tsx` with `--testTimeout=30000`; if it lands
under 5s idle and over 5s under load, it is purely a headroom problem.

**Suggested fix (separate task):** raise `test.testTimeout` in
`dashboard/vitest.config.ts` for the auth/server-component suites, or split
them into a slower project. Not done here — `vitest.config.ts` is shared
config, outside this plan's file list.
