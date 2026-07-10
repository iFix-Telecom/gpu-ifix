---
phase: 20-primary-pod-provisioning-resilience
plan: 05
subsystem: ui
tags: [nextjs, react, typescript, dashboard, pod-config]

# Dependency graph
requires:
  - phase: 20-01
    provides: GET /admin/primary/config returns created_budget_s + progress_stall_budget_s and their _min/_max bounds (config_read.go)
provides:
  - PodConfigSection + PodConfigBounds TS types for the two new budgets
  - two owner-editable sliders (created_budget_s, progress_stall_budget_s) in the pod-config editor
affects: [dashboard pod-config editor, phase 20 CFG-01/config_write]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Reuse the Phase 17 int-field descriptor molde: a new pod_config field becomes a slider by adding a { field, kind:'int', configKey, minKey, maxKey } entry to FIELD_GROUPS — no new component or gating code."

key-files:
  created: []
  modified:
    - dashboard/src/lib/gateway.ts
    - dashboard/src/lib/gateway.test.ts
    - dashboard/src/components/pod-config-controls.tsx

key-decisions:
  - "No lint gate: dashboard has no eslint/biome config (`next lint` is unconfigured, prompts interactively). Canonical checks are tsc + next build + vitest, all green."
  - "TS key ordering + bound values mirror config_read.go (created after port_bind_budget_s) and the 20-01 seeded bounds (min 30 / max 600)."

patterns-established:
  - "Adding a dashboard-editable pod_config budget = TS keys on PodConfigSection/PodConfigBounds + one FIELD_GROUPS int descriptor. The generic control renders + owner-gates it."

requirements-completed: [UI-01]

# Metrics
duration: 8min
completed: 2026-07-10
---

# Phase 20 Plan 05: two dashboard sliders for the new budgets Summary

**Owner-editable `created_budget_s` + `progress_stall_budget_s` sliders added to the pod-config editor by reusing the Phase 17 int-field molde — no new component or gating code.**

## Performance

- **Duration:** ~8 min
- **Tasks:** 2
- **Files modified:** 3

## Accomplishments
- `PodConfigSection` gains `created_budget_s` + `progress_stall_budget_s`; `PodConfigBounds` gains the 4 matching `_min`/`_max` bound keys — all `number`, snake_case, byte-for-byte matching the gateway config_read.go JSON.
- Two `kind:"int"` descriptors in the "Orçamentos e timeouts" group render as owner-editable sliders (operator sees read-only via the existing gate), `nextProvision:true` like coldstart/port-bind.
- tsc clean, `next build` succeeds, gateway vitest suite (16 tests) passes.

## Task Commits

1. **Task 1: TS types (6 new keys on PodConfig + PodConfigBounds)** - `73c05b0` (feat)
2. **Task 2: two slider descriptors in "Orçamentos e timeouts"** - `f8449a7` (feat)

## Files Created/Modified
- `dashboard/src/lib/gateway.ts` - 2 new keys on PodConfigSection, 4 new bound keys on PodConfigBounds
- `dashboard/src/lib/gateway.test.ts` - fetchPodConfig fixture updated with the new required keys
- `dashboard/src/components/pod-config-controls.tsx` - 2 int-field descriptors for the new budgets

## Decisions Made
- Placed the new keys after `port_bind_budget_s` to mirror config_read.go field order; bound fixture values (30/600) match the 20-01 seeded min/max.
- Lint: no eslint/biome config exists in the dashboard or repo root; `next lint` is unconfigured and prompts interactively. Verified via tsc + next build + vitest instead (all green). Setting up ESLint would be out-of-scope net-new config.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Updated fetchPodConfig test fixture with the new required keys**
- **Found during:** Task 1 (TS types)
- **Issue:** `gateway.test.ts` builds a typed `PodConfigResponse` literal; adding required keys to the interfaces made the fixture fail tsc (TS2739 missing properties).
- **Fix:** Added `created_budget_s`/`progress_stall_budget_s` (120/120) to the config fixture and the 4 bound keys (30/600) to the bounds fixture.
- **Files modified:** dashboard/src/lib/gateway.test.ts
- **Verification:** `npx tsc --noEmit` clean; `npx vitest run src/lib/gateway.test.ts` 16/16 pass.
- **Committed in:** `73c05b0` (Task 1 commit)

**2. [Rule 3 - Blocking] Installed dashboard's own lockfile deps to run verification**
- **Found during:** Task 1 verification
- **Issue:** `dashboard/node_modules` was absent, so tsc/build/test could not run.
- **Fix:** `bun install` (project's existing bun.lock — no new package added).
- **Files modified:** none tracked (node_modules gitignored)
- **Verification:** tsc + build + vitest all run green afterward.
- **Committed in:** n/a (no tracked files)

---

**Total deviations:** 2 auto-fixed (both Rule 3 - blocking).
**Impact on plan:** Both necessary to complete + verify the typed change. No scope creep.

## Issues Encountered
- None beyond the deviations above.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- Sliders bind to the 20-01 config_read fields and persist via the existing PATCH proxy — no new route. Full read/write path is live once the 20-01/config_write PATCH cases (CFG-01) are in place.

## Self-Check: PASSED

- FOUND: 20-05-SUMMARY.md
- FOUND: 73c05b0 (Task 1)
- FOUND: f8449a7 (Task 2)

---
*Phase: 20-primary-pod-provisioning-resilience*
*Completed: 2026-07-10*
