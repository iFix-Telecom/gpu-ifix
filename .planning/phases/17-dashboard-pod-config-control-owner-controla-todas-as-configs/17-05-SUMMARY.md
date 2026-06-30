---
phase: 17-dashboard-pod-config-control-owner-controla-todas-as-configs
plan: 05
subsystem: dashboard
tags: [next, react, server-action, owner-gate, pod-config, x-admin-key, audit, bounds, vitest]

# Dependency graph
requires:
  - phase: 17-04
    provides: "GET /admin/primary/lifecycle + GET/PATCH /admin/primary/config gateway endpoints (X-Admin-Key)"
  - phase: 13-dashboard-user-management
    provides: "requireOwner + writeAuditLog + admin_audit_log schema + inviteOperator owner-gated audited shape"
provides:
  - "updatePodConfig + updatePodConfigBound — owner-gated, bounds-validated, audited server actions writing through the gateway PATCH endpoint"
  - "gateway-admin.ts — server-only (import \"server-only\") X-Admin-Key PATCH helper; the 2nd legitimate admin-key reader"
  - "fetchPrimaryLifecycle + fetchPodConfig GET-only proxy wrappers + PrimaryLifecycleResponse/PodConfigResponse types"
  - "T-07-24 admin-key leak guard test — GATEWAY_ADMIN_KEY restricted to route.ts + gateway-admin.ts"
affects: [17-06-dashboard-config-page]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Owner-gated write server action: requireOwner FIRST → refetch live config → validate vs refetched bound → gateway PATCH via server-only helper → exactly one audit row → revalidate (mirrors inviteOperator)"
    - "Static field→spec allowlist maps (CONFIG_FIELDS / BOUND_FIELDS) so the action validates + sources audit-old without trusting client values"
    - "Server-only module isolation (import \"server-only\") to keep the admin key off the client bundle; filesystem-scan leak guard test"
    - "vitest alias for a non-hoisted Next marker package (server-only → next compiled empty.js) — resolve a side-effect import in tests with no new dependency"

key-files:
  created:
    - dashboard/src/lib/gateway-admin.ts
  modified:
    - dashboard/src/lib/admin-actions.ts
    - dashboard/src/lib/admin-actions.test.ts
    - dashboard/src/lib/gateway.ts
    - dashboard/src/lib/gateway.test.ts
    - dashboard/src/app/api/gateway/[...path]/route.ts
    - dashboard/vitest.config.ts

key-decisions:
  - "The admin key gets a SECOND server-only reader (gateway-admin.ts) rather than routing writes through the read-only GET proxy — the proxy stays GET-only (D-07); the key-leak guard is a real filesystem-scan test (not just a comment) allowing exactly {route.ts, gateway-admin.ts}"
  - "The audit `old` value is sourced from the SAME server-side fetchPodConfig refetch used for the bound — never a client-passed oldValue — so {field,old,new} is accurate even if the client UI was stale"
  - "Validation maps (CONFIG_FIELDS/BOUND_FIELDS) encode field→bound and field→configKey; the two list fields (blocklist/allowlist) map PATCH name → divergent GET config key (vast_machine_*)"
  - "vitest alias server-only → next's compiled empty.js resolves the marker import in tests with NO package install (avoids the package-legitimacy gate; server-only is already a transitive dep of next)"

requirements-completed: [POD-CFG-10, POD-CFG-11, POD-CFG-07, POD-CFG-15]

# Metrics
duration: 12min
completed: 2026-06-30
---

# Phase 17 Plan 05: Dashboard Pod-Config Write Path Summary

**Owner-gated dashboard write path (non-visual) for pod-config: `updatePodConfig` / `updatePodConfigBound` server actions mirror the Phase 13 `inviteOperator` shape (requireOwner FIRST → refetch the LIVE pod_config → validate the value against the refetched bound BEFORE any gateway call → PATCH `/admin/primary/config` via a server-only X-Admin-Key helper → write exactly one `admin_audit_log` row with `{field, old(from the same refetch), new}`). A new `gateway-admin.ts` (`import "server-only"`) is the second and last legitimate admin-key reader; `fetchPrimaryLifecycle` + `fetchPodConfig` are key-free GET-only proxy wrappers for the live panel + editor. A filesystem-scan leak guard test enforces that GATEWAY_ADMIN_KEY appears in exactly the two server-only files.**

## Performance
- **Duration:** ~12 min
- **Tasks:** 2 (Task 1 split RED/GREEN per tdd="true"); 3 commits
- **Files:** 7 (1 created, 6 modified)

## Accomplishments
- **Task 2 — read wrappers + leak guard (`gateway.ts`/`gateway.test.ts`).** Added `PrimaryLifecycleResponse` (+ `PrimaryLifecycleOpen`) and `PodConfigResponse` (+ `PodConfigSection`/`PodConfigBounds`) types mirroring the Plan 17-04 Go handlers (`lifecycle.go` / `config_read.go`) field-for-field, and `fetchPrimaryLifecycle()` / `fetchPodConfig()` GET-only proxy wrappers (route through `/api/gateway/*`, carry NO admin key). Added a real T-07-24 **admin-key leak guard test**: a recursive `src/` scan asserting `GATEWAY_ADMIN_KEY` appears in exactly `{route.ts, gateway-admin.ts}` (test files excluded — they don't ship to the browser; the needle is assembled at runtime so the test file isn't itself a match). 4 new gateway tests.
- **Task 1 (TDD) — owner-gated write actions.** `gateway-admin.ts`: a server-only (`import "server-only"`) `gatewayAdminPatch(path, body)` reading `GATEWAY_BASE_URL` + `GATEWAY_ADMIN_KEY` from `process.env` and PATCHing the gateway admin API with the `X-Admin-Key` header (throws the gateway's `{error:{message}}` on non-2xx). `admin-actions.ts`: `updatePodConfig` + `updatePodConfigBound` mirror `inviteOperator` — `requireOwner` FIRST (server-side gate, operator → localized throw), then `fetchPodConfig()` server-side (the single source for the LIVE bound AND the audit `old`), range/cross-field validation against the refetched bound BEFORE the PATCH, the gateway write via the server-only helper, exactly one audit row (`pod_config.update` / `pod_config_bounds.update`, metadata `{field, old, new}`), and `safeRevalidatePodConfig` for `/operacao/config`. Static `CONFIG_FIELDS` / `BOUND_FIELDS` allowlist maps encode each field's bound keys, config key, hour rule, and bound counterpart. 5 new admin-actions tests (operator-rejected/no-gateway/no-audit, owner-success with refetch-before-patch ordering + audit-old-from-fetch, out-of-bound throws before gateway call, bound min<max success + reject).

## Task Commits
1. **Task 2:** read wrappers + types + leak guard + vitest alias — `f9db633` (feat)
2. **Task 1 RED:** failing updatePodConfig/Bound tests — `83ff56d` (test)
3. **Task 1 GREEN:** gateway-admin.ts + the two write actions — `5559c1b` (feat)

## Decisions Made
- **Second server-only key reader, not a write-proxy.** The GET proxy (`route.ts`) stays GET-only (D-07); writes go through the dedicated `gateway-admin.ts` (`import "server-only"`). The T-07-24 acceptance "grep" is upgraded to a real committed test that scans `src/` and allows exactly the two server-only files — durable enforcement, not a comment.
- **Audit `old` from the server-side refetch.** Both actions read the current value from the same `fetchPodConfig` call that supplies the validation bound, so `{field, old, new}` is accurate regardless of client UI staleness; no client `oldValue` is trusted.
- **No new dependency for `server-only`.** It's a non-hoisted transitive dep of `next`; a vitest alias maps `server-only` → Next's compiled `empty.js` so the marker import resolves in tests. No package install → the package-legitimacy gate does not apply.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Reordered Task 2 before Task 1's GREEN (build dependency)**
- **Found during:** planning the commit sequence
- **Issue:** Task 1's actions `import { fetchPodConfig } from "@/lib/gateway"` — that wrapper is Task 2's deliverable. The plan's `read_first` for Task 1 anticipates this ("If implementing Task 1 first, add that wrapper in Task 2 before wiring here.").
- **Fix:** Committed Task 2 (gateway.ts wrappers + types) first (`f9db633`), then Task 1 RED (`83ff56d`) and GREEN (`5559c1b`). No scope change.
- **Files modified:** none beyond the planned set.

**2. [Rule 3 - Blocking] vitest alias for the non-hoisted `server-only` marker package**
- **Found during:** Task 1 GREEN (`import "server-only"` in gateway-admin.ts)
- **Issue:** `server-only` is declared by `next` (v0.0.1) but not hoisted to the root `node_modules`, so `require.resolve('server-only')` fails — a bare `import "server-only"` could fail to resolve in a non-mocked test path.
- **Fix:** Added a vitest `resolve.alias` mapping `server-only` → `node_modules/next/dist/compiled/server-only/empty.js` (Next's own no-op). Config change, NOT a package install (the package-legitimacy gate is for installs).
- **Files modified:** dashboard/vitest.config.ts

**3. [Rule 3 - Blocking] Reworded route.ts comment to keep the GET-only acceptance grep literal-clean**
- **Found during:** Task 1 acceptance greps (`! grep -q "PATCH" route.ts`)
- **Issue:** My updated route.ts header comment described the new helper as the "server-only PATCH helper" — the literal token `PATCH` in prose would trip the "proxy stays GET-only" grep even though no PATCH method handler exists.
- **Fix:** Reworded to "server-only write helper". No behavior change — route.ts still exports only `GET`. (Same class as Plan 17-04 deviation #1.)
- **Files modified:** dashboard/src/app/api/gateway/[...path]/route.ts
- **Commit:** `5559c1b`

**Total deviations:** 3 auto-fixed (all Rule 3 — ordering + non-install resolution + grep-clean doc). No scope creep, no architectural change.

## Threat Surface
- **T-17-15** (operator edits config): mitigated — `requireOwner` runs FIRST server-side in both actions; the operator test asserts a throw with ZERO gateway calls and ZERO audit rows.
- **T-17-16** (admin key leaks to browser): mitigated — the key is read only in the two server-only files; `gateway-admin.ts` carries `import "server-only"` (client-bundle import → build error); the committed leak-guard test enforces the allowlist.
- **T-17-17** (malicious value bricks prod): mitigated — server-side bounds validation (range + hour 0-23 + up≠down + bound min<max) runs BEFORE the gateway PATCH, defense-in-depth with the gateway's own check; out-of-bound tests assert no gateway call.
- **T-17-18** (config change with no trail): mitigated — exactly one `admin_audit_log` row per edit, action `pod_config.update` / `pod_config_bounds.update`, metadata `{field, old, new}` with `old` from the server-side refetch; no secret ever in metadata (test asserts).
- No NEW threat surface beyond the plan's register.

## Known Stubs
- None. The actions are fully wired to the live gateway endpoints (Plan 17-04) and the real `fetchPodConfig` / `gatewayAdminPatch`; the page (Plan 17-06) consumes them.

## Verification Evidence
- `cd dashboard && bunx vitest run src/lib/admin-actions.test.ts src/lib/gateway.test.ts` → 23 passed; full suite `bunx vitest run` → 60 passed (12 files).
- Acceptance greps: `gateway-admin.ts` line 1 is `import "server-only"`; reads `GATEWAY_ADMIN_KEY`; `updatePodConfig`/`updatePodConfigBound` present and call `requireOwner` before any gateway/audit work; `grep -rln GATEWAY_ADMIN_KEY src --include=*.ts | grep -v .test.` → exactly `{gateway-admin.ts, route.ts}`; `! grep -q "fetchPodConfig"`/`fetchPrimaryLifecycle` present in gateway.ts; `! grep -q GATEWAY_ADMIN_KEY` in gateway.ts; route.ts exports only `GET`, no `PATCH` token.
- Lint: the dashboard has no local linter binary installed (no biome/eslint in node_modules/.bin; `lint` script is `next lint` which requires interactive eslint setup) — skipped; the touched files follow the existing 2-space / double-quote / trailing-comma conventions and pass tsc-via-vitest transform.

## Next Phase Readiness
- Plan 17-06 (the config page UI) can now `import { fetchPrimaryLifecycle, fetchPodConfig } from "@/lib/gateway"` for current values + the live panel, and `import { updatePodConfig, updatePodConfigBound } from "@/lib/admin-actions"` for owner-gated edits (call with `actor` omitted → the action reads the session + re-checks owner). The field names accepted by the actions are the gateway PATCH names (config: `cap_primary`, `blocklist`, …; bound: `cap_primary_min`, …).

---
*Phase: 17-dashboard-pod-config-control-owner-controla-todas-as-configs*
*Completed: 2026-06-30*

## Self-Check: PASSED
- All 6 source files + the new gateway-admin.ts verified present on disk.
- All 3 task commits (f9db633, 83ff56d, 5559c1b) verified in git log.
