# Phase 17: Dashboard pod-config control — Pattern Map

**Mapped:** 2026-06-30
**Files analyzed:** 16 (8 new, 8 modified)
**Analogs found:** 16 / 16 (every file has a strong in-tree analog — this phase is a mirror-and-extend, no greenfield surface)

> Scope guardrails honored (CONTEXT D-01/D-02): NO `restart.go` / `POST /admin/gateway/restart`, NO structural-edit form. `pod_config` holds ONLY the 16 hot fields, seeded from env. Bounds (min/max) are themselves owner-editable (D-03) — mapped as a second storage + editor surface.

---

## File Classification

| New/Modified File | Role | Data Flow | Closest Analog | Match Quality |
|-------------------|------|-----------|----------------|---------------|
| **NEW** `gateway/internal/podconfig/loader.go` | service (in-mem snapshot) | event-driven (LISTEN/NOTIFY) | `gateway/internal/upstreams/loader.go` | exact |
| **NEW** `gateway/internal/podconfig/listen.go` | service (DB listener) | event-driven | `gateway/internal/upstreams/listen.go` | exact |
| **NEW** `gateway/internal/podconfig/loader_test.go` | test | event-driven | `gateway/internal/upstreams/loader_test.go` | exact |
| **NEW** `gateway/db/migrations/0031_create_pod_config.sql` | migration | CRUD + NOTIFY trigger | `gateway/db/migrations/0009_upstreams_notify_trigger.sql` + `0023_primary_lifecycles.sql` | role-match |
| **NEW** `gateway/db/queries/pod_config.sql` | model (sqlc source) | CRUD | `gateway/db/queries/upstreams.sql` | exact |
| **NEW** `gateway/internal/admin/lifecycle.go` (`GET /admin/primary/lifecycle`) | controller (admin handler) | request-response | `gateway/internal/admin/operations.go` | exact |
| **NEW** `gateway/internal/admin/lifecycle_test.go` | test | request-response | `gateway/internal/admin/operations_test.go` | role-match |
| **MODIFY** `gateway/internal/primary/lifecycle.go` (Reconciler struct) | service | event-driven | self (add `podCfg atomic.Pointer`, mirror `activePodURLs`) | self |
| **MODIFY** `gateway/internal/primary/reconciler.go` (offer-selection + budgets/timeouts reads) | service | event-driven | self (swap ~13 `r.cfg.*` → `r.podCfg.Load().*`) | self |
| **MODIFY** `gateway/internal/primary/schedule.go` (`ParseScheduleEnv` from snapshot) | service | transform | self | self |
| **MODIFY** `gateway/internal/primary/budget.go` (`CheckBudget` read) | service | request-response | self (`b.cfg.MonthlyPrimaryBudgetBRL` → snapshot) | self |
| **MODIFY** `gateway/cmd/gateway/main.go` (wire loader + `ListenAndReload` goroutine + mount handler) | config/wiring | event-driven | self (mirror `upstreams.ListenAndReload` block ~main.go:495) | self |
| **NEW** dashboard config page `dashboard/src/app/(dashboard)/operacao/config/page.tsx` (RSC) | component (page) | request-response | `dashboard/src/app/(dashboard)/operacao/page.tsx` + `settings/operadores/page.tsx` | exact |
| **NEW** `dashboard/src/components/pod-config-controls.tsx` (client island, edit affordances) | component | request-response | `dashboard/src/app/settings/operadores/operator-controls.tsx` + `components/operacao-fsm-panel.tsx` | exact |
| **MODIFY** `dashboard/src/lib/admin-actions.ts` (`updatePodConfig` / `updatePodConfigBound`) | service (server action) | request-response | self (`inviteOperator` shape: `requireOwner` + `writeAuditLog`) | self |
| **MODIFY** `dashboard/src/lib/gateway.ts` (`fetchPrimaryLifecycle` + types) | utility (fetch wrapper) | request-response | self (`fetchOperations` + `OperationsResponse`) | self |
| **MODIFY** `dashboard/src/components/app-sidebar.tsx` (nav entry) | component | — | self (nav array, `app-sidebar.tsx:36-41`) | self |
| **NEW** `dashboard/src/components/ui/switch.tsx` | component (primitive) | — | `npx shadcn add switch` (official registry) | n/a |
| **MODIFY** `dashboard/src/lib/schema-custom.ts` | model | — | self (no change to table; only `action` string values used) | self |

---

## Pattern Assignments

### `gateway/internal/podconfig/loader.go` (service, event-driven) — MIRROR VERBATIM

**Analog:** `gateway/internal/upstreams/loader.go` (read in full). Copy the structure exactly; only the row type and field set change (16 hot fields + bounds instead of upstream rows). Drop the `tier0Override` machinery — pod_config has no override layer.

**Imports + snapshot + struct** (loader.go:1-25, 55-77):
```go
package podconfig

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"
	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// snapshot is the immutable view of pod_config served from the hot path.
// Built fresh on every Refresh, atomically swapped via atomic.Pointer so
// reads are lock-free. Mirror upstreams.snapshot.
type snapshot struct {
	cfg   PodConfig      // the 16 hot fields, typed
	rule  ScheduleRule   // pre-parsed from the snapshot (see schedule note)
	bounds PodConfigBounds // min/max per numeric hot field (D-03)
}

type loaderQueries interface {
	GetPodConfig(ctx context.Context) (gen.PodConfig, error) // single-row
}

type Loader struct {
	pool *pgxpool.Pool
	q    loaderQueries
	snap atomic.Pointer[snapshot]
	log  *slog.Logger
}
```

**Constructor + initial Refresh fail-fast** (loader.go:66-77) — copy verbatim, renaming. Initial `Refresh` MUST succeed or boot fails (the seed guarantees a row exists).

**Refresh — last-good-on-error is THE critical invariant** (loader.go:108-199). Copy the error path exactly: on query error, increment a metric and **return without swapping** the snapshot (keep last-good). Pitfall 1 in RESEARCH depends on this:
```go
func (l *Loader) Refresh(ctx context.Context) error {
	row, err := l.q.GetPodConfig(ctx)
	if err != nil {
		obs.PodConfigReloadTotal.WithLabelValues("error").Inc() // add metric (mirror UpstreamsReloadTotal, loader.go:111)
		return fmt.Errorf("get pod_config: %w", err)
		// NOTE: caller (listen handler) logs + returns nil → snapshot UNCHANGED.
	}
	s := &snapshot{ cfg: rowToPodConfig(row), bounds: rowToBounds(row) }
	rule, perr := ParseScheduleFromSnapshot(s.cfg) // see schedule.go note
	if perr != nil {
		obs.PodConfigReloadTotal.WithLabelValues("error").Inc()
		return fmt.Errorf("reparse schedule: %w", perr) // bad tz → keep last-good
	}
	s.rule = rule
	l.snap.Store(s)
	obs.PodConfigReloadTotal.WithLabelValues("ok").Inc()
	l.log.Info("pod_config refreshed")
	return nil
}
```

**Lock-free getter** (loader.go:201-209, 417-425): provide `Load() *snapshot` (or `Cfg()`/`Rule()`/`Bounds()` accessors) doing a single `l.snap.Load()` — same as `Get`/`All`.

---

### `gateway/internal/podconfig/listen.go` (service, event-driven) — MIRROR VERBATIM

**Analog:** `gateway/internal/upstreams/listen.go` (read in full, 69 lines). Copy entire file; change channel name `upstreams_changed` → `pod_config_changed` and the doc comments. The reconnect/backoff, the "return nil on handler error to keep listener alive", and the dedicated `pgx.Conn` (not pool) are all load-bearing — keep them.

**Core listener block** (listen.go:34-69):
```go
func ListenAndReload(ctx context.Context, dsn string, loader *Loader, onReload func(), log *slog.Logger) error {
	log = log.With("module", "LISTEN")
	listener := &pgxlisten.Listener{
		Connect:        func(ctx context.Context) (*pgx.Conn, error) { return pgx.Connect(ctx, dsn) },
		LogError:       func(_ context.Context, err error) { log.Warn("pgxlisten error", "err", err) },
		ReconnectDelay: 5 * time.Second,
	}
	listener.Handle("pod_config_changed", pgxlisten.HandlerFunc(
		func(ctx context.Context, n *pgconn.Notification, _ *pgx.Conn) error {
			if err := loader.Refresh(ctx); err != nil {
				log.Error("loader refresh after NOTIFY failed", "err", err)
				return nil // keep listener alive — transient DB hiccup must not stop hot-reload
			}
			if onReload != nil { onReload() }
			return nil
		}))
	err := listener.Listen(ctx)
	if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) { return err }
	return ctx.Err()
}
```
The reconciler does NOT need an `onReload` callback (it reads `loader.Load()` live every tick) — pass `nil`, OR use `onReload` to atomically swap `r.rule` if the schedule rule is cached on the Reconciler instead of in the snapshot. Planner picks; mapping recommends storing `rule` inside the snapshot (shown above) so `onReload` stays nil.

---

### `gateway/db/migrations/0031_create_pod_config.sql` (migration) — NOTIFY trigger template

**Analogs:** `0009_upstreams_notify_trigger.sql` (NOTIFY trigger, read in full) + table-create idioms from `0023_primary_lifecycles.sql`.

**Goose envelope + search_path** (0009:1-3, 51-57): keep the `-- +goose Up/Down` + `-- +goose StatementBegin/End` framing and `SET search_path = ai_gateway, public;`.

**NOTIFY function + split INSERT/DELETE vs UPDATE triggers** (0009:5-48) — copy this pattern verbatim, renaming to `notify_pod_config_changed` and target `ai_gateway.pod_config`. The split (Postgres forbids OLD in an INSERT WHEN clause and NEW in a DELETE WHEN clause) is mandatory:
```sql
CREATE OR REPLACE FUNCTION ai_gateway.notify_pod_config_changed() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('pod_config_changed', COALESCE(NEW.id::text, OLD.id::text));
    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER pod_config_insert_delete_notify
AFTER INSERT OR DELETE ON ai_gateway.pod_config
FOR EACH ROW WHEN (pg_trigger_depth() = 0)
EXECUTE FUNCTION ai_gateway.notify_pod_config_changed();

CREATE TRIGGER pod_config_update_notify
AFTER UPDATE ON ai_gateway.pod_config
FOR EACH ROW
WHEN ( pg_trigger_depth() = 0 AND (
    NEW.<col> IS DISTINCT FROM OLD.<col> OR ... -- enumerate every config + bound column
))
EXECUTE FUNCTION ai_gateway.notify_pod_config_changed();
```
Unlike `upstreams` (which filters out probe writebacks at 0009:28-48), `pod_config` has no high-frequency writeback column — but keep the `IS DISTINCT FROM` WHEN clause so an idempotent re-save of identical values doesn't spuriously fire a reload. Single-row table: a `CHECK (id = TRUE)` boolean-PK or `singleton` sentinel column is the idiom for "exactly one row". Per D-03 the bounds are columns (`*_min`/`*_max`) OR a sibling `pod_config_bounds` table — planner's call; if a sibling table, add a second NOTIFY trigger on it (or reuse the same channel).

**Seed-from-env caveat (RESEARCH Migration Concern):** the migration creates the table EMPTY; the seed (INSERT from current `cfg.Primary*`) happens in Go at boot (in `Loader.Refresh` first-run, or a one-shot in `main.go` between pool-connect and reconciler-start). The seed must copy the 3 fail-fast SHAs even though those stay structural — but per D-02 structural fields do NOT enter `pod_config`, so the seed is ONLY the 16 hot fields. Bounds seeded from RESEARCH defaults (cap 0.10–1.50, coldstart 300–5400s, etc., CONTEXT D-03).

---

### `gateway/db/queries/pod_config.sql` (model, CRUD) 

**Analog:** `gateway/db/queries/upstreams.sql:1-31` (read). Same `-- name: X :one/:many/:exec` sqlc annotation style. Add the file to `sqlc.yaml` `queries:` list (after `primary_lifecycles.sql`), then regen (`sqlc generate`). Needed queries:
```sql
-- name: GetPodConfig :one
SELECT * FROM ai_gateway.pod_config WHERE id = TRUE;

-- name: SeedPodConfig :exec   -- idempotent first-boot seed (INSERT ... ON CONFLICT DO NOTHING)
INSERT INTO ai_gateway.pod_config (...) VALUES (...) ON CONFLICT (id) DO NOTHING;

-- name: UpdatePodConfigField :exec  -- one column per owner edit (clean audit diff)
UPDATE ai_gateway.pod_config SET <col> = $1, updated_at = now() WHERE id = TRUE;
```
The dashboard server action writes through the gateway admin API (D-07), NOT directly to this table — but the gateway needs a write path. **Decision for planner:** the dashboard owner-action calls a NEW admin write endpoint (e.g. `PATCH /admin/primary/config`) on the gateway, which runs the `UpdatePodConfigField` query → trigger fires → loader reloads. (The RESEARCH Open-Q5 / A4 says writes go via server action that calls the gateway admin API directly; that admin endpoint is the gateway-side write seam and is in scope even though the self-RESTART endpoint is not.)

---

### `gateway/internal/admin/lifecycle.go` — `GET /admin/primary/lifecycle` (controller, request-response)

**Analog:** `gateway/internal/admin/operations.go` (read in full, the canonical template). It ALREADY reads `rec` (FSM) + `cfg` + `ListPrimaryLifecycles`. Build a focused subset: current FSM state + the OPEN lifecycle's event trail (D-05).

**Handler struct + dual constructor + query isolation** (operations.go:111-160) — copy the shape exactly:
```go
type lifecycleQueries interface {
	GetOpenPrimaryLifecycle(ctx context.Context) (gen.PrimaryLifecycle, error) // already generated (db/gen/primary_lifecycles.sql.go)
	ListPrimaryLifecycles(ctx context.Context, arg gen.ListPrimaryLifecyclesParams) ([]gen.ListPrimaryLifecyclesRow, error)
}

type PrimaryLifecycleHandler struct {
	q   lifecycleQueries
	rec *primary.Reconciler // nil-safe: Vast off → "unknown"
	log *slog.Logger
}
func NewPrimaryLifecycleHandler(q *gen.Queries, rec *primary.Reconciler, log *slog.Logger) *PrimaryLifecycleHandler { ... }
```

**FSM section** (operations.go:58-65 + ServeHTTP fsmSection helper): reuse `rec.FSM.State()` / `rec.ActivePodURLs()` (lifecycle.go:344) the way `OperationsHandler.fsmSection` does. The OPEN lifecycle's `events` jsonb + `shutdown_reason` is the event trail (RESEARCH: primary_lifecycles cols started_at, first_health_pass_at, drain_started_at, ended_at, trigger_reason, vast_offer_id, accepted_dph, total_cost_brl, shutdown_reason, events jsonb).

**Error envelope + admin metric per branch** (operations.go:179-212): copy verbatim — `httpx.WriteOpenAIError(w, 500, "api_error", "lifecycles_query_failed", "")` + `obs.GatewayAdminRequests.WithLabelValues("/admin/primary/lifecycle", "5xx").Inc()`.

**Router mount** (main.go:1505-1511 pattern): add inside the existing `adminRouter` block, same X-Admin-Key gate:
```go
if px.adminPrimaryLifecycleHandler != nil {
	adminRouter.Method(http.MethodGet, "/primary/lifecycle", px.adminPrimaryLifecycleHandler)
}
```

---

### `gateway/internal/primary/{lifecycle,reconciler,schedule,budget}.go` (MODIFY — service, event-driven)

**Inject the loader into the Reconciler struct** (lifecycle.go:239-242 + the `activePodURLs atomic.Pointer` idiom at lifecycle.go:247): add one field mirroring the existing atomic pointer pattern. Prefer holding the `*podconfig.Loader` (so reads call `Load()`), not a bare pointer:
```go
type Reconciler struct {
	deps   Deps
	cfg    config.Config // STAYS — structural reads + fallback (D-02)
	rule   ScheduleRule  // becomes a fallback; live rule read from podCfg snapshot
	podCfg *podconfig.Loader // NEW — hot-field source of truth
	...
}
```
Wire it in `NewReconciler`/`NewReconcilerFull` (lifecycle.go:327-337) from `deps`.

**Swap the ~13 hot reads** (reconciler.go:1194-1230 read — the offer-selection block):
```go
// BEFORE (reconciler.go:1193-1201):
filters := vast.DefaultSearchFilters(
	r.cfg.PrimaryVastPriceCapPrimary, r.cfg.PrimaryVastPriceCapFallback,
	r.cfg.PrimaryHostID,
	r.cfg.PrimaryVastGPUNamePrimary, r.cfg.PrimaryVastGPUNameFallback,   // STRUCTURAL — stays r.cfg
	r.cfg.PrimaryVastNumGPUsPrimary, r.cfg.PrimaryVastNumGPUsFallback,   // STRUCTURAL — stays r.cfg
	r.cfg.PrimaryVastMachineBlocklist...,
)
// AFTER — read the snapshot ONCE at the top of provisionLifecycle:
snap := r.podCfg.Load()
filters := vast.DefaultSearchFilters(
	snap.cfg.PrimaryVastPriceCapPrimary, snap.cfg.PrimaryVastPriceCapFallback, // HOT
	snap.cfg.PrimaryHostID,                                                    // HOT
	r.cfg.PrimaryVastGPUNamePrimary, r.cfg.PrimaryVastGPUNameFallback,         // STRUCTURAL stays r.cfg
	r.cfg.PrimaryVastNumGPUsPrimary, r.cfg.PrimaryVastNumGPUsFallback,         // STRUCTURAL stays r.cfg
	snap.cfg.PrimaryVastMachineBlocklist...,                                    // HOT
)
```
Hot reads to swap (from RESEARCH Config Surface Table — read the snapshot, NOT `r.cfg`): blocklist/allowlist (#1,#2 reconciler.go:1198,1219), price caps (#3,#4 :1194,1203), host_id (#5 :1195), reject-private-ip (#6 :1579), coldstart budget (#7 :1333), port-bind budget (#8 :1389), failure cooldown (#9 :416), monthly budget (#10 budget.go), schedule fields (#11-16 via `r.rule`). **Keep STRUCTURAL reads on `r.cfg`** (GPU name/num #18-21, images, weights). **Critical (Pitfall 1):** read `snap := r.podCfg.Load()` ONCE at provision-start; an in-flight goroutine keeps its captured values (this is the desired blocklist-append semantics, RESEARCH §central-refactor-seam).

**Schedule re-parse** (schedule.go:103-137 `ParseScheduleEnv`): add a sibling `ParseScheduleFromSnapshot(cfg PodConfig)` (or generalize `ParseScheduleEnv` to take the 7 schedule fields). Keep the fail-fast `time.LoadLocation` (schedule.go:104) — but per D-03a timezone stays STRUCTURAL (read from `r.cfg.PrimaryPodScheduleTimezone`), so the loc never changes via hot-reload and the fail-fast never fires at runtime. The snapshot's rule re-parse uses the structural tz + the hot UpHour/DownHour/Days/Lead/Grace/Disabled.

**Budget** (budget.go:112-129): `b.cfg.MonthlyPrimaryBudgetBRL` → `b.podCfg.Load().cfg.MonthlyPrimaryBudgetBRL`. Inject the loader into `BudgetChecker` the same way as the Reconciler.

---

### `gateway/cmd/gateway/main.go` (MODIFY — wiring)

**Analog:** the `upstreams.ListenAndReload` goroutine block (main.go:495-516, read). Mirror it:
```go
podCfgLoader, err := podconfig.NewLoader(ctx, pool, log) // initial Refresh fail-fast (mirror loader.go:73)
if err != nil { log.Error("pod_config loader init failed", "err", err); os.Exit(2) }
go func() {
	if err := podconfig.ListenAndReload(ctx, cfg.PGDSN, podCfgLoader, nil, log); err != nil {
		log.Warn("pod_config listener exited", "err", err)
	}
}()
```
Pass `podCfgLoader` into `primary.NewReconcilerFull` deps + into the new `admin.NewPrimaryLifecycleHandler`. Add the handler to the proxy struct + mount (main.go:1505 pattern). The seed (if not done inside `Loader.Refresh` first-run) goes between `pool` connect and `NewLoader`.

---

### `dashboard/src/app/(dashboard)/operacao/config/page.tsx` (NEW — component, RSC)

**Analogs:** `operacao/page.tsx` (live-panel React Query + skeleton/error states, read in full) + `settings/operadores/page.tsx` (owner-gate via `getViewerRole`).

**Owner-gate pattern** (viewer.ts `getViewerRole`, read): the page is an RSC that reads the viewer role and renders the client edit island ONLY when `role === "owner"`; operators see read-only values. The cosmetic gate mirrors operadores; the authoritative gate is `requireOwner` in the server action.

**Live-panel composition** (operacao/page.tsx:34-77): copy the React Query + `refetchInterval: 10000` + `StaleIndicator` + skeleton/error idiom for Surface C. Layout shell `<div className="flex flex-col gap-8">` + `h1 text-[28px] font-semibold` (operacao/page.tsx:43-45).

---

### `dashboard/src/components/pod-config-controls.tsx` (NEW — client island)

**Analogs:** `settings/operadores/operator-controls.tsx` (read — owner edit affordances, `alert-dialog`, sonner, Spinner) + `components/operacao-fsm-panel.tsx` (read — `Field` idiom + FSM badge classes for the live panel).

**`Field` read-only idiom** (operacao-fsm-panel.tsx:78-87) — copy verbatim for all read-only value rendering (config view, structural display, bounds cells):
```tsx
function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-[12px] font-semibold text-muted-foreground">{label}</span>
      <span className="text-[14px] tabular-nums">{children}</span>
    </div>
  );
}
```

**Inline 14×14 Spinner** (operator-controls.tsx:63-71) — copy verbatim for pending state.

**`alert-dialog` dangerous-confirm** (operator-controls.tsx:28-37 imports + the AlertDialog usage): default-focus Cancelar, `--destructive` confirm button, confirm pending = disabled + Spinner (D-04). Reuse the exact import block + structure; the specific impact strings come from UI-SPEC §Dangerous confirmations.

**FSM badge classes** (operacao-fsm-panel.tsx:26-56 `primaryStateClass`/`primaryStateLabel`) — reuse verbatim for the live panel (Surface C); DO NOT redefine. UI-SPEC says reuse `fsm.ts` tiers.

**Edit submit pattern** (operator-controls.tsx:105-119 `handleSubmit`): `setPending(true)` → `await updatePodConfig({field, value})` (actor omitted → server reads session + re-checks owner) → `toast.success(...)` → close edit → `catch` sets inline error + `setPending(false)`.

**`switch` primitive:** `npx shadcn add switch` (UI-SPEC: the ONE net-new install, official registry) for `reject_private_ip` + `Disabled`. The `Disabled` switch's `onCheckedChange` opens the dangerous `alert-dialog` BEFORE committing.

---

### `dashboard/src/lib/admin-actions.ts` (MODIFY — server action)

**Analog:** `inviteOperator` (admin-actions.ts:195-233, read in full) — the canonical owner-gated audited action. New actions `updatePodConfig` / `updatePodConfigBound` follow it EXACTLY:
```ts
export async function updatePodConfig(args: {
  actor?: Actor; auth?: AuthLike; field: string; value: unknown; oldValue?: unknown;
}): Promise<void> {
  const authInstance = args.auth ?? (realAuth as unknown as AuthLike);
  const { actor } = await requireOwner(args.actor, authInstance);  // D-07 server-side gate (admin-actions.ts:117)
  // ... server-side bounds validation against CURRENT bound BEFORE the gateway write (D-03a) ...
  // ... call the gateway admin write endpoint (PATCH /admin/primary/config) via fetch w/ X-Admin-Key —
  //     server-side only, NOT the read-only proxy (D-07) ...
  await writeAuditLog({                                            // D-06 (admin-actions.ts:156)
    actor: { id: actor.id, email: actor.email },
    action: "pod_config.update",
    metadata: { field: args.field, old: args.oldValue, new: args.value },
  });
  safeRevalidate(); // adapt path to /operacao/config
}
```
`updatePodConfigBound` is identical with `action: "pod_config_bounds.update"`. Both re-check `requireOwner` (operator → throws localized error, admin-actions.ts:136-138). Audit metadata is `{field, old, new}` per D-06 — NEVER secrets.

**GOTCHA — the gateway admin key in the server action:** the read-only proxy (`route.ts`, read) is the ONLY existing place `GATEWAY_ADMIN_KEY` is read, and it stays GET-only (D-07). The write server action must read `GATEWAY_ADMIN_KEY` + `GATEWAY_BASE_URL` from `process.env` server-side and `fetch` the gateway write endpoint directly with the `X-Admin-Key` header — same env vars, different (server-action) call site. The T-07-24 grep acceptance criterion (proxy route.ts:10-13) must be updated to allow this second server-only reader, OR the action delegates the key-read to a shared server-only helper. Flag for planner.

---

### `dashboard/src/lib/gateway.ts` (MODIFY — fetch wrapper) + `app-sidebar.tsx`

**Analog:** `fetchOperations` + `OperationsResponse` (gateway.ts:416-419, 258-264, read). Add `fetchPrimaryLifecycle()` calling `proxyGet<PrimaryLifecycleResponse>("primary/lifecycle")` + a TS interface mirroring the new Go handler's JSON FIELD-FOR-FIELD (gateway.ts convention: "the Go handler is the source of truth"). The poll path stays read-only through the proxy (D-07).

**Sidebar nav** (app-sidebar.tsx:36-41, read): add one entry after Operação:
```ts
{ href: "/operacao/config", label: "Config do pod", icon: SlidersHorizontal },
```
Visible to both roles (UI-SPEC); edit affordances owner-gated inside the page.

---

## Shared Patterns

### Hot-reload (LISTEN/NOTIFY + atomic snapshot + last-good)
**Source:** `gateway/internal/upstreams/loader.go` + `listen.go` + migration `0009`.
**Apply to:** `gateway/internal/podconfig/*` (loader, listen), migration `0031`, main.go wiring.
**Load-bearing invariants:** (1) initial `Refresh` fail-fast at boot; (2) NEVER swap on a failed read (keep last-good, increment error metric — loader.go:111); (3) dedicated `pgx.Conn` for LISTEN, not the pool (listen.go:14-19); (4) handler returns nil on Refresh error so the listener survives a transient DB hiccup (listen.go:48-55); (5) hot path reads `loader.Load()` in-memory, ZERO synchronous DB call per tick.

### Admin handler (X-Admin-Key gated, OpenAI error envelope)
**Source:** `gateway/internal/admin/operations.go` + router mount `main.go:1488-1524`.
**Apply to:** new `GET /admin/primary/lifecycle` (+ the gateway-side `PATCH /admin/primary/config` write endpoint the dashboard server action calls).
**Pattern:** query-interface isolation (`operationsQueries`) for test injection; dual constructor (prod `*gen.Queries` + test fake); `httpx.WriteOpenAIError` + `obs.GatewayAdminRequests.WithLabelValues(route, class).Inc()` per branch; mount inside `adminRouter` so `admin.Middleware` (X-Admin-Key) gates it.

### Owner-gated server action + dashboard-side audit
**Source:** `dashboard/src/lib/admin-actions.ts` (`requireOwner` :117, `writeAuditLog` :156, `inviteOperator` :195).
**Apply to:** `updatePodConfig`, `updatePodConfigBound`.
**Pattern:** `requireOwner(args.actor, auth)` FIRST (server-side `role==="owner"`, throws localized error for operators — NOT UI-hiding); then the gateway write; then exactly one `writeAuditLog` row (`action`, `metadata={field,old,new}`, NEVER secrets); then `safeRevalidate`. Self-service flows are NOT audited but config edits ARE (D-06).

### Owner-cosmetic-gate + live-panel React Query (dashboard page)
**Source:** `dashboard/src/lib/viewer.ts` (`getViewerRole`) + `operacao/page.tsx` (React Query `refetchInterval: 10000` + `StaleIndicator` + skeleton/error) + `operacao-fsm-panel.tsx` (`Field` + FSM badge classes).
**Apply to:** the new config page + `pod-config-controls.tsx`.
**Pattern:** RSC reads `getViewerRole`, renders edit island only when owner (fail-closed on null role); live panel reuses the operacao poll idiom + `fsm.ts` tiers verbatim.

---

## No Analog Found

None. Every file maps to a strong in-tree analog. The closest thing to "no analog" is the gateway-side **config WRITE endpoint** (`PATCH /admin/primary/config`) — there is no existing admin *write* handler (all current `/admin/*` are GET). But its skeleton (struct + dual constructor + X-Admin-Key mount + error envelope) is fully covered by `operations.go`; only the HTTP method (POST/PATCH, mirror the `/debug/panic` POST mount at main.go:1521-1522) and the `UpdatePodConfigField` sqlc call are new. Classify it as **role-match** (admin controller) with method/verb being the only delta.

---

## Metadata

**Analog search scope:** `gateway/internal/{upstreams,admin,primary,db}`, `gateway/db/{migrations,queries}`, `gateway/cmd/gateway`, `dashboard/src/{lib,components,app}`.
**Files scanned (read in full or targeted):** upstreams/loader.go, upstreams/listen.go, db/migrations/0009, admin/operations.go, primary/lifecycle.go, primary/reconciler.go (offer block), primary/schedule.go, primary/budget.go (grep), cmd/gateway/main.go (LISTEN + admin mount), db/queries/upstreams.sql, sqlc.yaml, dashboard admin-actions.ts, route.ts, gateway.ts, schema-custom.ts, viewer.ts, operacao/page.tsx, operacao-fsm-panel.tsx, operator-controls.tsx, app-sidebar.tsx.
**Pattern extraction date:** 2026-06-30
