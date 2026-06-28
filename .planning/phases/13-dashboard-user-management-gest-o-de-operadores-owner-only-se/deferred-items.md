# Deferred Items — Phase 13

Out-of-scope discoveries logged during execution (NOT fixed by the discovering plan).

## From plan 13-03 (Wave 3 — admin server actions)

These RED test files are owned by OTHER waves/plans and were already RED at
13-03's base commit (`0df4ea6`). 13-03 touches only `admin-actions.ts`,
`admin-actions.test.ts`, and `email.ts` — none of the files below — so they
are out of scope per the executor SCOPE BOUNDARY rule.

| Test file | Requirement | Owner | Status |
|-----------|-------------|-------|--------|
| `dashboard/src/lib/seed-owner.test.ts` | UM-03 (idempotent owner seed) | Wave 1 (`seed-owner.ts`) | RED — implementation pending |
| `dashboard/src/app/settings/operadores/page.test.tsx` | UM-10 (real-role badge + owner-gate UI) | Plan 05 (operadores UI wiring) | RED — UI wires `@/lib/admin-actions` (now available) |

Note: 13-03 made the 4 server actions + `writeAuditLog` available at
`@/lib/admin-actions`, which is the import Plan 05's `page.tsx` needs to turn
`page.test.tsx` green. No action required from 13-03.
