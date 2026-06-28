---
phase: 13-dashboard-user-management-gest-o-de-operadores-owner-only-se
plan: 01
subsystem: dashboard
status: complete
tags: [scaffolding, tdd-red, shadcn, user-management, owner-only]
requires:
  - dashboard/src/lib/auth.test.ts (memoryAdapter buildTestAuth harness)
  - dashboard/src/app/login/page.test.tsx (authClient component-mock pattern)
provides:
  - RED test surface for UM-01..UM-10 (4 files, 11 failing assertions)
  - shadcn dialog / dropdown-menu / alert-dialog primitives for UI waves
affects:
  - dashboard/package.json (nodemailer ^9.0.1 + @types/nodemailer ^8.0.1 added)
tech-stack:
  added:
    - nodemailer ^9.0.1 (Brevo SMTP transport, D-04 invite/reset)
    - "@types/nodemailer ^8.0.1 (devDep — nodemailer 9 ships no bundled types)"
    - shadcn dialog/dropdown-menu/alert-dialog (copied source, official registry)
  patterns:
    - "computed dynamic-import specifier to keep not-yet-built modules as RED assertions (not Vite collection errors)"
    - "vi.mock @/lib/email mailer transport — no real SMTP in tests"
key-files:
  modified_deps:
    - dashboard/package.json (nodemailer + @types/nodemailer)
    - dashboard/bun.lock
  created:
    - dashboard/src/components/ui/dialog.tsx
    - dashboard/src/components/ui/dropdown-menu.tsx
    - dashboard/src/components/ui/alert-dialog.tsx
    - dashboard/src/lib/admin-actions.test.ts
    - dashboard/src/lib/seed-owner.test.ts
    - dashboard/src/app/settings/page.test.tsx
    - dashboard/src/app/settings/operadores/page.test.tsx
  modified: []
decisions:
  - "Reverted shadcn's unrelated cosmetic edit to button.tsx (out of scope; could regress existing UI)"
  - "Defeated Vite static import-analysis with a computed import specifier so the 3 not-yet-built modules fail as RED assertions, not suite-collection errors (plan acceptance criteria)"
metrics:
  duration: ~35m
  completed: 2026-06-28
  tasks_completed: 3
  tasks_total: 3
---

# Phase 13 Plan 01: Wave 0 Scaffolding Summary

Established the failing-test surface (RED) for all ten UM-* requirements,
installed the three net-new shadcn primitives the UI waves consume, and added
the single net-new npm dependency (nodemailer) after its `[ASSUMED]` legitimacy
was cleared via a blocking-human checkpoint. Plan 13-01 is COMPLETE (3/3).

## Status: COMPLETE (3/3 tasks)

| Task | Name | Status | Commit |
|------|------|--------|--------|
| 1 | Gate + install nodemailer ([ASSUMED] → [OK]) | ✅ done | 61a378d |
| 2 | Install shadcn dialog + dropdown-menu + alert-dialog | ✅ done | 68ad65e |
| 3 | Write RED test stubs UM-01..UM-10 | ✅ done | 2398265 |

Tasks 2 and 3 were executed first because neither depends on nodemailer
(Task 3 mocks the `@/lib/email` mailer transport via `vi.mock`, so no SMTP /
nodemailer import is exercised). Task 1 — the `gate="blocking-human"` checkpoint
— was paused for human verification, then approved and completed.

## What Was Built

### Task 1 — nodemailer dependency (commit 61a378d)
- `nodemailer@9.0.1` added to `dashboard/package.json` dependencies (`^9.0.1`)
  — Brevo SMTP transport for D-04 invite/reset links (UM-09).
- `@types/nodemailer@8.0.1` added as devDependency: nodemailer 9 ships NO
  bundled types (no `types` field in its `package.json`, zero `.d.ts` under
  `node_modules/nodemailer`), so per the plan's conditional acceptance criterion
  the `@types` package WAS required.
- `[ASSUMED]` legitimacy CLEARED → `[OK]` (see Package Legitimacy Decision below).

### Task 2 — shadcn primitives (commit 68ad65e)
- `dialog.tsx` (UI-SPEC state 5: provision modal)
- `dropdown-menu.tsx` (UI-SPEC state 6: row `···` action menu)
- `alert-dialog.tsx` (UI-SPEC state 7: destructive confirm)
- Official shadcn registry only; `components.json` `registries: {}` unchanged.
- `form.tsx` / `label.tsx` deliberately NOT installed (UI-SPEC: plain
  `<form>`+`<label>` precedent from `2fa/enroll`).

### Task 3 — RED test stubs (commit 2398265)
Four files, collected by vitest, **11 assertions RED** (implementation absent),
NOT import/collection errors:
- `src/lib/admin-actions.test.ts` — UM-04 (invite owner-gate + non-@ifixtelecom
  reject + reset email on MOCKED mailer), UM-05 (remove + revoke all sessions),
  UM-06 (reset-pw + revoke), UM-07 (reset-2FA clears two_factor +
  twoFactorEnabled=false), UM-08/D-09 (one audit row per admin op AND ZERO rows
  for self-service change-password). Reuses the `auth.test.ts` memoryAdapter
  harness plus the `admin({ adminRoles:["owner"], defaultRole:"operator" })`
  plugin.
- `src/lib/seed-owner.test.ts` — UM-03 (earliest user → owner, rest → operator,
  idempotent re-run no-op).
- `src/app/settings/page.test.tsx` — UM-01 (self-service change-password;
  wrong current pw → inline error + no navigation; success clears fields).
  Mirrors `login/page.test.tsx` authClient mock.
- `src/app/settings/operadores/page.test.tsx` — UM-10 (role badge from real
  `role` column + owner-gate hides `+ Provisionar` / `···` for non-owners).
  Renders the awaited Server Component with a mocked `@/lib/db`.

## Verification

- `bun install --frozen-lockfile` (node_modules absent in fresh worktree —
  installed existing locked deps; NOT a package add; bun.lock + package.json
  unchanged).
- `cd dashboard && bun run test <4 files>` → **RED-CONFIRMED**, 11 failed, no
  `Failed Suites` / `no tests` / "Does the file exist" (genuine assertion RED).
- Full suite: `4 failed | 8 passed (12)` files, `11 failed | 38 passed (49)`
  tests — the 8 pre-existing files stay green (no regression).
- Three shadcn files exist; `form`/`label` absent; `registries: {}` intact.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] RED stubs failed at SUITE COLLECTION instead of as assertions**
- **Found during:** Task 3 first verify run.
- **Issue:** A string-literal `import("@/lib/seed-owner")` (etc.) of a
  not-yet-created module is resolved by Vite's static `import-analysis` plugin at
  transform time, failing the WHOLE suite with "Does the file exist?" → reported
  as `Failed Suites` / `Tests: no tests`. The plan's acceptance criteria require
  RED via failing assertions, NOT a collection/import error.
- **Fix:** Built the import specifier from a variable
  (`["@/lib","admin-actions"].join("/")` + `/* @vite-ignore */`) so Vite leaves
  resolution to runtime; the missing module then rejects, is caught, and yields
  a real `expect(...).not.toBeNull()` failure (RED).
- **Files modified:** admin-actions.test.ts, seed-owner.test.ts,
  settings/page.test.tsx (operadores/page.test.tsx unaffected — its target page
  already exists).
- **Commit:** 2398265

**2. [Rule 1 - Bug] shadcn rewrote an unrelated button.tsx variant**
- **Found during:** Task 2.
- **Issue:** `npx shadcn add` updated `src/components/ui/button.tsx` with the
  registry's current `default`/`secondary` variant styling — out of scope and a
  potential visual regression for existing UI.
- **Fix:** `git checkout -- src/components/ui/button.tsx` (reverted); committed
  only the three new files.
- **Commit:** 68ad65e

### Environment note (not a deviation)
- The fresh worktree had no `dashboard/node_modules`; `bunx tsc --noEmit`
  therefore reports "Cannot find module 'react'" for ALL files (incl.
  pre-existing `button.tsx`) until `bun install` runs. Resolved by the
  frozen-lockfile install. CI / merge will install normally.
- A local `.planning/clickup-active-task.json` (`{"skip": true}`, gitignored)
  was replicated from the main checkout so the company ClickUp-enforce PostToolUse
  hook (GSD-pure skip mode already chosen by the operator) passes inside the
  worktree. Not committed.

## Package Legitimacy Decision — nodemailer (T-13-SC, RESOLVED)

**Checkpoint:** Task 1 `checkpoint:human-verify` (`gate="blocking-human"`).
**Outcome:** APPROVED — `[ASSUMED]` → `[OK]` (not `[SLOP]`/`[SUS]`).

Threat T-13-SC required a blocking human verification of the `[ASSUMED]`
nodemailer package before `bun add` (RESEARCH could not run slopcheck at plan
time). The executor paused at this checkpoint and did NOT auto-approve; a human
verification was performed and approval relayed with the following registry
evidence (`registry.npmjs.org/nodemailer/latest`):

- **v9.0.1**, author **Andris Reinman** (canonical nodemailer author).
- Published via **GitHub Actions OIDC trusted publisher**.
- Repo **github.com/nodemailer/nodemailer**, license **MIT-0**.
- **No** `install` / `preinstall` / `postinstall` scripts (scripts = test /
  format / lint / update only).
- **Zero production dependencies.**

**Independent confirmation by the executor at install time:** installed
`nodemailer@9.0.1`; nodemailer 9 ships NO bundled types (no `types` field in its
`package.json`, zero `.d.ts` under `node_modules/nodemailer`) → `@types/nodemailer@8.0.1`
added as devDependency per the plan's conditional acceptance criterion.

### Acceptance (Task 1) — MET
- `dashboard/package.json` dependencies contains `"nodemailer": "^9.0.1"` ✓
- No `[SLOP]`/`[SUS]` verdict; legitimacy decision recorded above ✓
- `@types/nodemailer` present BECAUSE nodemailer 9 lacks bundled `.d.ts` ✓

## Self-Check: PASSED

All 7 created files verified present on disk; all 3 task commits
(61a378d, 68ad65e, 2398265) verified in `git log`. `dashboard/package.json`
contains `"nodemailer"` (`grep -q` passes). Full suite: 8 passed / 4 RED files
(38 passed, 11 RED) — no regression introduced by the nodemailer add.
