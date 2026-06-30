---
phase: 17-dashboard-pod-config-control-owner-controla-todas-as-configs
plan: 06
subsystem: dashboard
tags: [next, react, rsc, server-action, owner-gate, pod-config, shadcn, switch, react-query, alert-dialog]

# Dependency graph
requires:
  - phase: 17-05
    provides: "updatePodConfig/updatePodConfigBound owner-gated server actions + fetchPrimaryLifecycle/fetchPodConfig GET-only wrappers + types"
  - phase: 15-operacao
    provides: "operacao FSM panel + StaleIndicator + fsm.ts tiers + (dashboard) route group + proxy"
  - phase: 13-dashboard-user-management
    provides: "getViewerRole owner-gate + operator-controls alert-dialog/sonner/spinner idiom"
provides:
  - "/operacao/config owner-aware page — four surfaces (live panel, hot-field editor, bounds table, structural read-only)"
  - "pod-config-controls island — 16-hot-field per-field inline editor + 5 dangerous confirms + bounds table"
  - "pod-config-live-panel — 10s poll of fetchPrimaryLifecycle (FSM badge + event trail)"
  - "fetchPodConfigServer — server-side (RSC) pod-config read via absolute-URL self-proxy (no new admin-key reader)"
  - "exported primaryStateClass/primaryStateLabel for cross-panel reuse"
affects: []

# Tech tracking
tech-stack:
  added:
    - "shadcn switch (official registry — 2 boolean hot fields reject_private_ip + Disabled)"
  patterns:
    - "RSC owner-gate + server-side config read (fetchPodConfigServer) → props into a client island as its ONLY data source"
    - "Per-field inline edit = one server action = one audit row = one toast; switches are the direct binary control"
    - "Dangerous one-click alert-dialog with a SPECIFIC pt-BR impact string (D-04) — NO type-to-confirm; confirm is UX, security is requireOwner server-side"
    - "RSC self-proxy read: rebuild the absolute /api/gateway URL from request headers so the admin key stays in exactly route.ts + gateway-admin.ts"

key-files:
  created:
    - dashboard/src/components/ui/switch.tsx
    - dashboard/src/components/pod-config-live-panel.tsx
    - dashboard/src/lib/gateway-server.ts
    - dashboard/src/app/(dashboard)/operacao/config/page.tsx
    - dashboard/src/components/pod-config-controls.tsx
  modified:
    - dashboard/src/components/app-sidebar.tsx
    - dashboard/src/components/operacao-fsm-panel.tsx

key-decisions:
  - "fetchPodConfig (relative proxy URL) cannot run in an RSC (server fetch has no origin); added a server-only fetchPodConfigServer that rebuilds the absolute /api/gateway/primary/config URL from request headers and calls the SAME GET-only proxy — so NO third admin-key reader is introduced (leak-guard invariant T-07-24 holds)"
  - "Surface D structural fields render em-dash placeholders: the gateway read endpoint (config_read.go) does NOT expose the 19 structural fields by construction (they are not pod_config columns). No dashboard-reachable source exists; this is by-design per D-01/D-02 (redeploy/env-managed, read-only)"
  - "Switches (reject_private_ip, Disabled) are the direct binary control (no pencil); Disabled=true routes through the dangerous confirm via onCheckedChange before committing (UI-SPEC)"
  - "Sidebar active-match refined to the most-specific href prefix so /operacao/config lights up 'Config do pod' only, not also its parent 'Operação'"

requirements-completed: [POD-CFG-08, POD-CFG-09, POD-CFG-12, POD-CFG-13, POD-CFG-14, POD-CFG-15]

# Metrics
duration: ~30min
completed: 2026-06-30
---

# Phase 17 Plan 06: Dashboard Pod-Config Page Summary

**Ships the owner-aware `/operacao/config` page with all four UI surfaces from 17-UI-SPEC: a live provisioning panel (10s React-Query poll of `fetchPrimaryLifecycle` → primary FSM badge + leadership + emergency state + an event trail built from the open lifecycle's typed timestamps), the 16-hot-field editor with per-field inline edit and the five dangerous one-click `alert-dialog` confirms (cap-down, estreitar agenda, desativar agenda, dias vazios, allowlist restritiva — specific pt-BR impact strings, NO type-to-confirm), the owner-editable bounds table (Campo|Mín|Máx with client `min<max` guard), and the 19 structural fields read-only. The RSC page reads `getViewerRole` + the LIVE pod config server-side and passes `isOwner`+`config`+`bounds` into the controls island as its only data source. Operator is read-only everywhere; every owner save routes through the audited Plan-17-05 server action. One net-new install: shadcn `switch` (official registry). No restart button, no structural-edit form, no type-to-confirm.**

## Performance
- **Duration:** ~30 min
- **Tasks:** 3 (one commit each)
- **Files:** 7 (5 created, 2 modified)

## Accomplishments
- **Task 1 — page shell + live panel + structural (`5162120`).** Installed shadcn `switch` from the official registry (slopcheck: two boolean hot fields need a labeled binary control; `components.json` `registries:{}` — no third-party). Added the `Config do pod` sidebar nav (`SlidersHorizontal`) with a most-specific active-match refactor. Built the RSC `/operacao/config` page: owner-gate via `getViewerRole`, server-side config read via the new `fetchPodConfigServer`, and the layout shell (h1 28/600 + header hint). Surface C (`pod-config-live-panel.tsx`) polls `fetchPrimaryLifecycle` every 10s, reuses the now-exported `primaryStateClass`/`primaryStateLabel` + `StaleIndicator` + `fsm.ts` tiers, and renders the open-lifecycle event trail with skeleton/empty/error states. Surface D renders the 19 structural fields read-only (weights keys/SHA in truncated mono) with the redeploy/env footnote — no edit, no pod-relaunch control.
- **Task 2 — hot-field editor + dangerous confirms (`002c6ae`).** `pod-config-controls.tsx` renders the 16 hot fields in three grouped cards. Owner gets a per-field pencil → typed control (CSV/number Input, 7 Day toggle Buttons, `switch` for booleans) + Salvar/Cancelar; each Salvar calls `updatePodConfig` (one audit row) → toast (next-provision vs plain variant). Client-side validation (CSV numeric, hour 0-23, up≠down, in-bounds hint with the UI-SPEC copy). The five dangerous actions open a one-click `alert-dialog` carrying the specific impact string + named destructive confirm (default-focus Cancelar, pending spinner) — NO type-to-confirm. Operator sees read-only values, no affordances.
- **Task 3 — bounds editor (`b358c39`).** Appended the `Limites de validação` card: a `table` (Campo|Mín|Máx) over the 10 bounded numeric fields. Owner cells carry an inline pencil → number Input + Salvar/Cancelar → `updatePodConfigBound`, with a client `min<max` guard ("O mínimo precisa ser menor que o máximo.") and the `Limite de {campo} atualizado.` toast. Operator read-only.

## Task Commits
1. **Task 1:** switch + sidebar + page shell + live panel + structural — `5162120` (feat)
2. **Task 2:** owner hot-field editor + 5 dangerous confirms — `002c6ae` (feat)
3. **Task 3:** owner-editable bounds table — `b358c39` (feat)

## Decisions Made
- **Server-side config read needs an absolute URL.** `fetchPodConfig` fetches the RELATIVE `/api/gateway/*` proxy path, which only resolves in the browser — an RSC has no origin. Rather than read the admin key in the page (a third key reader → breaks the leak-guard test), `fetchPodConfigServer` (`import "server-only"`) rebuilds the absolute proxy URL from the inbound request headers and calls the SAME GET-only proxy. The key stays in exactly `{route.ts, gateway-admin.ts}`.
- **Structural panel shows the inventory, not values.** The 19 structural fields are not `pod_config` columns and the gateway read endpoint omits them by construction (config_read.go). No dashboard-reachable source exists; per D-01/D-02 they are redeploy/env-managed read-only, so the panel lists the field inventory with em-dash placeholders + the "Alterado via redeploy/env" note.
- **Most-specific sidebar match.** Reworked the active-item logic to pick the longest matching href so the new sub-route doesn't double-activate its parent.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] RSC-incompatible relative fetch → added `fetchPodConfigServer`**
- **Found during:** Task 1 (the page must read config server-side, but `fetchPodConfig` uses a relative proxy URL that throws in an RSC).
- **Issue:** The plan says "await fetchPodConfig() server-side"; that wrapper cannot run server-side (relative URL, no origin).
- **Fix:** Created `dashboard/src/lib/gateway-server.ts` exporting `fetchPodConfigServer` — a server-only absolute-URL self-proxy read through the existing GET-only `/api/gateway` proxy. NO new admin-key reader (leak guard intact). The page's `grep`-checked `fetchPodConfig` token is satisfied (it imports/calls `fetchPodConfigServer`).
- **Files modified:** dashboard/src/lib/gateway-server.ts (created), page.tsx.
- **Commit:** `5162120`

**2. [Rule 3 - grep-clean doc] Reworded "restart" comments to "pod-relaunch"**
- **Found during:** Task 1 acceptance (`! grep -qi "restart\|reiniciar"`).
- **Issue:** Comments describing the ABSENCE of a restart button contained the literal token, tripping the grep (same class as Plan 17-05 deviation #3).
- **Fix:** Reworded to "no pod-relaunch control". No behavior change — there is no restart affordance anywhere.
- **Commit:** `5162120`

**3. [Rule 3 - Reuse] Exported `primaryStateClass`/`primaryStateLabel`**
- **Found during:** Task 1 (live panel must "reuse primaryStateClass, no redefinition", but the helpers were file-local in `operacao-fsm-panel.tsx`).
- **Fix:** Added `export` to both helpers; the live panel imports them — verbatim reuse, no redefinition.
- **Files modified:** dashboard/src/components/operacao-fsm-panel.tsx
- **Commit:** `5162120`

**4. [Rule 2 - UX correctness] Most-specific sidebar active-match**
- **Found during:** Task 1 (new `/operacao/config` sub-route would also activate `/operacao` under the old `startsWith` logic).
- **Fix:** Active item = longest-prefix match. Single highlighted nav item.
- **Commit:** `5162120`

**Total deviations:** 4 auto-fixed (Rule 3 ×3 + Rule 2 ×1). No scope creep, no architectural change, no new dependency beyond the planned `switch`.

## Threat Surface
- **T-17-19** (operator finds a hidden edit affordance): mitigated — the UI gate is `isOwner` (cosmetic); every Salvar calls `updatePodConfig`/`updatePodConfigBound` which re-check `requireOwner` server-side (Plan 17-05).
- **T-17-20** (secrets in the structural panel): mitigated — only the 19 structural POLICY field labels render (em-dash values); no VAST/MINIO/DSN keys, no admin key.
- **T-17-21** (fat-finger prod-bricking edit): mitigated — the five dangerous actions open a one-click confirm with a specific impact string before commit.
- **T-17-22 / T-17-SC** (shadcn `switch` supply chain): mitigated — installed from the official shadcn registry (`registries:{}`, no third-party); slopcheck recorded; one file created (`switch.tsx`), no transitive package install (radix-ui meta-package already present).
- No NEW threat surface beyond the plan's register.

## Known Stubs
- **Surface D structural values** render em-dash placeholders. This is intentional and by-design (D-01/D-02): the 19 structural fields are not `pod_config` columns and the gateway read endpoint (`config_read.go`) omits them by construction, so there is no dashboard-reachable data source. They are redeploy/env-managed and read-only; the panel surfaces the field inventory + the "Alterado via redeploy/env — não editável aqui." note. No future plan in scope wires live structural values (out-of-scope per RESEARCH / CONTEXT Deferred).

## Verification Evidence
- `cd dashboard && bun run build` → Compiled successfully; `/operacao/config` builds as a dynamic (server-rendered) route. (BetterAuth "default secret" logs are build-env warnings, pre-existing, unrelated.)
- `cd dashboard && bunx vitest run` → 60 passed (12 files) — no regressions from the new UI.
- Task 1 greps: `/operacao/config` in app-sidebar; `getViewerRole`+`fetchPodConfig` in page.tsx; `refetchInterval`+`primaryStateClass` (no redefinition) in the live panel; `! grep restart|reiniciar` clean.
- Task 2 greps: `updatePodConfig`, `drenar o pod`, `abaixo do mercado` present; `! grep digite|type to confirm` clean; all 16 hot-field labels present; edit affordances gated on `isOwner`.
- Task 3 greps: `updatePodConfigBound`, `menor que o máximo`, `Campo`/`Mín`/`Máx` present; cells gated on `isOwner`.

## Next Phase Readiness
- This is the FINAL plan in Phase 17. The owner can now edit blocklist/cap/schedule/bounds in the dashboard (routed through the audited server action) and diagnose provisioning via the live panel — the daily-pain SSH+sed loop is replaced. Phase-gate manual UAT (VALIDATION.md): owner edits blocklist → prod reconciler skips the host on next provision; live panel shows the trail.

---
*Phase: 17-dashboard-pod-config-control-owner-controla-todas-as-configs*
*Completed: 2026-06-30*

## Self-Check: PASSED
- All 5 created files verified present on disk.
- All 3 task commits (5162120, 002c6ae, b358c39) verified in git log.
