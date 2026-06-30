---
phase: 17-dashboard-pod-config-control-owner-controla-todas-as-configs
reviewed: 2026-06-30T22:31:23Z
depth: standard
files_reviewed: 24
files_reviewed_list:
  - gateway/db/migrations/0031_create_pod_config.sql
  - gateway/db/queries/pod_config.sql
  - gateway/internal/admin/config_read.go
  - gateway/internal/admin/config_write.go
  - gateway/internal/admin/lifecycle.go
  - gateway/internal/obs/metrics.go
  - gateway/internal/podconfig/listen.go
  - gateway/internal/podconfig/loader.go
  - gateway/internal/podconfig/types.go
  - gateway/internal/primary/budget.go
  - gateway/internal/primary/lifecycle.go
  - gateway/internal/primary/reconciler.go
  - gateway/internal/primary/schedule.go
  - gateway/cmd/gateway/main.go
  - dashboard/src/app/(dashboard)/operacao/config/page.tsx
  - dashboard/src/app/api/gateway/[...path]/route.ts
  - dashboard/src/components/app-sidebar.tsx
  - dashboard/src/components/operacao-fsm-panel.tsx
  - dashboard/src/components/pod-config-controls.tsx
  - dashboard/src/components/pod-config-live-panel.tsx
  - dashboard/src/components/ui/switch.tsx
  - dashboard/src/lib/admin-actions.ts
  - dashboard/src/lib/gateway-admin.ts
  - dashboard/src/lib/gateway-server.ts
  - dashboard/src/lib/gateway.ts
findings:
  critical: 2
  warning: 2
  info: 2
  total: 6
status: issues_found
---

# Phase 17: Code Review Report

**Reviewed:** 2026-06-30T22:31:23Z
**Depth:** standard
**Files Reviewed:** 24 (1 file — `gateway.ts` — counted once; lock/gen/test files excluded)
**Status:** issues_found

## Summary

Phase 17 ships a DB-backed pod-config hot-reload path (migration `0031` + `podconfig.Loader` + reconciler snapshot reads), three X-Admin-Key gateway endpoints (`GET/PATCH /admin/primary/config`, `GET /admin/primary/lifecycle`), and an owner-gated dashboard editor.

The **gateway side is solid**: the PATCH handler uses a static field→typed-query allowlist with zero dynamic column SQL (SQL-injection vector closed), bounds + cross-field validation run before any UPDATE, the loader honors last-good-on-error (never serves zero-config), and migration `0031`'s single-row CHECK + IS-DISTINCT-FROM NOTIFY trigger are correct. The admin key is correctly confined to exactly `route.ts` + `gateway-admin.ts` (both server-only).

The **dashboard write path has a critical authorization defect.** The phase's #1 stated invariant — "every pod-config write must call requireOwner FIRST; operator must be read-only" — is technically satisfied in source order but **defeated at runtime**: `admin-actions.ts` is a `"use server"` module whose functions are network-reachable server actions, and `requireOwner` trusts a client-supplied `actor` argument. Any authenticated operator can call `updatePodConfig({ actor: { role: "owner" }, ... })` and bypass the gate entirely. The same client-controllable-argument flaw lets the `db` parameter suppress (and `writeAuditLog`, also exported, forge) the audit trail. These two findings invalidate the owner-only and audit-trail guarantees the phase exists to provide.

## Critical Issues

### CR-01: Owner-gate bypass — pod-config write actions trust a client-supplied `actor`

**File:** `dashboard/src/lib/admin-actions.ts:122-145, 508-517, 585-594`
**Issue:**
`admin-actions.ts` declares `"use server"` at line 1, so every exported async function becomes a network-reachable Server Action (confirmed: `dashboard/src/components/pod-config-controls.tsx` — a `"use client"` component — imports and calls `updatePodConfig`/`updatePodConfigBound`). Server-action arguments are fully controlled by the caller.

`updatePodConfig`/`updatePodConfigBound` resolve the owner via `requireOwner(args.actor, ...)`. In `requireOwner`, when `actor` is supplied it is **trusted verbatim** — the session is only consulted when `actor` is `undefined`:

```ts
let resolved: Actor | undefined = actor;           // line 126 — client-supplied
if (!resolved) { /* read session */ }              // skipped when actor passed
if (resolved.role !== "owner") throw ...;           // checks the client's own claim
```

`Actor` (`{ id?, email?, role? }`) is a plain serializable object, so a logged-in **operator** (read-only role, but holding a valid 2FA session that satisfies `middleware.ts`) can invoke the action directly:

```
updatePodConfig({ actor: { role: "owner" }, field: "cap_primary", value: 0.5 })
```

`requireOwner` returns success without ever reading the session. The operator now writes any in-bounds pod-config field — caps, blocklist, schedule, kill-switch — exactly the privilege escalation the phase forbids (T-17-15). The gateway PATCH only checks the X-Admin-Key (which the server proxy always attaches), so there is no second line of defense for the human role.

**Fix:** Server Actions must never accept the caller identity from the client. Derive role exclusively from the session inside the action; drop the `actor` injection seam from the production path. Inject the actor only through a non-exported, test-only seam:

```ts
// Production action: NO client-facing actor/auth/db params.
export async function updatePodConfig(args: { field: string; value: unknown }) {
  const { actor } = await requireOwnerFromSession(); // session-only, no arg
  ...
}

// requireOwnerFromSession ignores any client input and always reads auth.api.getSession.
```
Tests should call an internal helper (not the exported server action) or use a server-side dependency-injection mechanism that is not part of the action's serialized argument surface. The same fix is required for the Phase-13 actions (`inviteOperator`, `removeOperator`, `resetOperator2FA`, …) which share the identical flaw.

---

### CR-02: Audit-trail suppression and forgery via client-controllable `db` + exported `writeAuditLog`

**File:** `dashboard/src/lib/admin-actions.ts:161-188, 565-574, 623-632`
**Issue:**
Two distinct audit-integrity holes, both stemming from the `"use server"` exposure:

1. **Suppression.** `updatePodConfig`/`updatePodConfigBound` forward `db: args.db` into `writeAuditLog`. `writeAuditLog` routes the row to an in-memory bag when `isMemBag(args.db)` is true (`Array.isArray(db.adminAuditLog)`). `db` is a plain serializable object, so a client passing `db: { adminAuditLog: [] }` makes the audit row land in a throwaway array and never reach Postgres — a config change with **no durable trail** (violates D-08 / T-17-18), even when the owner gate is satisfied.
2. **Forgery.** `writeAuditLog` is itself `export`ed from the `"use server"` module, so it is a network-reachable action. Any authenticated user can call it directly with an arbitrary `{ actor, target, action, metadata }`; when `dbConfigured()` is true (always in prod) it `INSERT`s straight into `schema.adminAuditLog`. An attacker can fabricate audit rows attributing actions to other actors, polluting the forensic record.

**Fix:**
- Remove `db` from the public action signatures (CR-01 fix covers this); `writeAuditLog` should always target the real DB in production and obtain the test bag through a non-serialized seam.
- Do not `export` `writeAuditLog` (and `requireOwner`) from a `"use server"` module — move shared helpers into a non-`"use server"` module imported by the actions, so they are not independently invocable over the network. Keep only the genuine entry-point actions exported.

## Warnings

### WR-01: Gateway proxy forwards arbitrary `/admin/*` GET for any authenticated user, with the admin key attached

**File:** `dashboard/src/app/api/gateway/[...path]/route.ts:27-86`
**Issue:** The catch-all proxy maps `GET /api/gateway/<anything>` → `${GATEWAY_BASE_URL}/admin/<anything>` and injects `X-Admin-Key`, with no path allowlist and no role check. `middleware.ts` only guarantees the caller is an authenticated, 2FA-verified dashboard user — it does not distinguish operator from owner. Any operator can therefore read every admin observability endpoint (`/admin/audit`, `/admin/economy`, `/admin/usage`, `/admin/operations`, `/admin/primary/config`, …) by hitting the proxy, since the key is supplied server-side. The blast radius is read-only (PATCH is not exported here, so writes are not reachable through the proxy), and the surface predates this phase, but `route.ts` is in scope and the unrestricted passthrough is a standing broken-access-control concern.
**Fix:** Constrain the proxy to an explicit allowlist of the GET paths the dashboard actually needs, and/or gate sensitive endpoints (`audit`, `economy`) behind a server-side owner check before forwarding. At minimum, document that every `/admin/*` GET is intentionally readable by operators.

### WR-02: `schedule_days` empty-array "dangerous confirm" is dead — the gateway always rejects it

**File:** `dashboard/src/components/pod-config-controls.tsx:690-700` and `gateway/internal/admin/config_write.go:212-224`
**Issue:** `dangerFor` offers a one-click confirm ("Salvar sem nenhum dia?") when the owner saves `schedule_days` with zero days, implying the empty save is permitted-but-dangerous. The gateway unconditionally rejects an empty `schedule_days` (`if err != nil || len(v) == 0 { validationErr(...) }`). So confirming the dialog always produces a gateway 400, which the UI surfaces as the generic `GENERIC_ERROR` ("Não foi possível salvar agora"). The "dias vazios" dangerous action is non-functional and misleads the operator about what the system allows.
**Fix:** Decide the intended contract and align both sides: either accept an empty `schedule_days` on the gateway (treating "no days" as a documented schedule-off state and keeping the confirm), or remove the empty-days branch from `dangerFor` and add a client-side validation error mirroring the gateway ("selecione ao menos um dia").

## Info

### IN-01: Redundant double snapshot-load in budget threshold read

**File:** `gateway/internal/primary/budget.go:96-103`
**Issue:** `monthlyBudgetBRL()` loads the snapshot twice: `if s := b.podCfg.Load(); s != nil { return b.podCfg.Cfg().MonthlyBudgetBRL }`. `s` is discarded and `Cfg()` performs a second atomic load. Harmless (both return a valid last-good snapshot), but a concurrent Refresh between the two loads means the nil-check and the value can come from different snapshots.
**Fix:** Read once: `if s := b.podCfg.Load(); s != nil { return s.cfg.MonthlyBudgetBRL }` (or expose the value off `s`).

### IN-02: Live-panel trail renders "Pod pronto" with the same timestamp as "Primeiro health check OK"

**File:** `dashboard/src/components/pod-config-live-panel.tsx:85-99`
**Issue:** When `fsm_state === "ready"`, `buildTrail` pushes a "Pod pronto" step using `open.first_health_pass_at` — identical to the timestamp already shown for "Primeiro health check OK". The two consecutive rows display the same time, which reads as a duplicate to operators.
**Fix:** Either drop the "Pod pronto" row (the ready badge already conveys it) or source its timestamp from a distinct ready transition; if no separate timestamp exists, render it without a time.

---

_Reviewed: 2026-06-30T22:31:23Z_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
