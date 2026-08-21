---
phase: quick-20260822-dashboard-secondary-pods
plan: 01
status: complete
completed: 2026-08-21
tasks_completed: 3
tasks_total: 3
commits:
  - d831179  # test(RED) ListInstances tests
  - 9fbdf3e  # feat(GREEN) Vast ListInstances + Instance fields
  - b2f7b91  # feat /admin/operations secondary_pods section
  - bc90cb7  # feat dashboard "Outros pods" panel
requirements: [DASH-SECPODS-01]
pushed: false
---

# Quick 20260822 Plan 01: Dashboard Secondary Pods Summary

Adds an account-wide, read-only "Outros pods" view: a new Vast `ListInstances`
client method feeds a primary-filtered, 60s-cached, nil/failure-safe
`secondary_pods` section on `/admin/operations`, rendered as an "Outros pods"
card on the Operação page. The externally-managed 3060 STT/TTS pod shows today;
any future Vast instance appears automatically with no code change.

## What was built

### Task 1 — Vast client `ListInstances` + Instance fields (TDD)
- `gateway/internal/emerg/vast/types.go`: added `GpuName`, `NumGpus`,
  `StartDate` to the `Instance` struct (read-only projection for the panel).
- `gateway/internal/emerg/vast/client.go`: `ListInstances(ctx) ([]Instance, error)`
  — `GET /instances/`, decodes the array under the `instances` key (mirrors
  `SearchOffers`, NOT `GetInstance`'s single-object peek), nil→empty non-nil
  slice, non-200 → `parseErrorBody`, `list` metric verb, no lifecycle side
  effects.
- `gateway/internal/emerg/vast/client_test.go`: happy-path, empty-array, non-200.
- RED (d831179) then GREEN (9fbdf3e).

### Task 2 — `/admin/operations` secondary_pods section (TDD)
- `gateway/internal/admin/operations.go`: `SecondaryPodRow` + `secondary_pods`
  on `OperationsResponse`; unexported `vastLister` interface; nil-safe `vast`
  field + 60s `sync.Mutex` cache; `secondaryPods(ctx, activeInstanceID)` filters
  out the active primary (FSM `ActiveInstanceID`), computes `dph_brl = DphTotal ×
  cfg.USDToBRLRate` and `uptime_seconds = max(0, now - start_date)`; degrades to
  last-good/empty on Vast error (WARN), never 5xxs; wired into `ServeHTTP`. New
  `vast` param added to BOTH constructors.
- `gateway/cmd/gateway/main.go`: dedicated read-only `vast.NewClient` at the
  handler site (~1340), untyped nil when the key is unset.
- `gateway/internal/admin/operations_test.go`: `fakeVastLister`; filter/dph_brl/
  uptime, nil-vast (empty + 200), error-never-5xx, 60s cache; all existing call
  sites updated for the new arg.
- b2f7b91.

### Task 3 — Dashboard "Outros pods" panel
- `dashboard/src/lib/gateway.ts`: `OperationsSecondaryPod` + `secondary_pods`.
- `dashboard/src/lib/format.ts`: `formatUptime(seconds)` pt-BR compact.
- `dashboard/src/components/operacao-secondary-pods-panel.tsx`: read-only card,
  status badge, GPU `×N`, label, `formatBrl(dph_brl)/h`, `formatUptime`, subtle
  `#id`, empty state "Nenhum outro pod ativo."
- `dashboard/src/app/(dashboard)/operacao/page.tsx`: rendered after the cost panel.
- bc90cb7.

## Deviations from Plan

1. [Rule 1 — Bug] main.go passes untyped `nil` (not a nil `*vast.Client`) when
   VAST_AI_API_KEY is unset. The plan's `var opsVast *vast.Client; pass opsVast`
   would wrap a typed nil pointer in the `vastLister` interface → `h.vast == nil`
   false → nil-pointer panic inside `secondaryPods`. Fixed by branching in main.go
   and passing untyped `nil` in the disabled branch. File: `cmd/gateway/main.go`.

2. [Rule 3 — Blocking] The filtering test calls `secondaryPods(...)` directly
   instead of driving `Snapshot().ActiveInstanceID` via `NewReconcilerFull`. The
   reconciler's `activeInstanceID` is an unexported `atomic.Int64` seeded only by
   helpers in `primary/export_test.go` (compiled only in the primary package's own
   test build), unreachable from the admin test package. The test calls the
   same-package method with a chosen active id, precisely verifying filtering,
   dph_brl, uptime. nil-vast/error tests still exercise full ServeHTTP for the 200
   guarantee. File: `internal/admin/operations_test.go`.

## Validation

- `cd gateway && gofmt -l .` → empty (clean).
- `cd gateway && go build ./...` → OK.
- `cd gateway && go test ./internal/emerg/vast/... ./internal/admin/...` → both ok.
- `cd dashboard && npx tsc --noEmit` → clean (exit 0).
- `cd dashboard && npx vitest run src/lib/gateway.test.ts` → 16/16 passed.
- Dashboard lint: `next lint` unconfigured (interactive ESLint setup prompt, no
  eslint config present) — no working lint to run; skipped.
- Integration tests (`-tags integration`) NOT run here — require docker,
  unavailable in this environment. CI covers them.

## Threat surface

Only non-secret fields projected (T-secpods-01); 60s cache bounds Vast API
pressure (T-secpods-02); Vast error/nil never 5xxs (T-secpods-02). No threat flags.

## Known Stubs

None. `secondary_pods` is wired end-to-end.

## Not pushed

Committed to `develop`, NOT pushed (per instructions).

## Self-Check: PASSED
- gateway/internal/emerg/vast/client.go (ListInstances) — present.
- gateway/internal/admin/operations.go (SecondaryPods) — present.
- dashboard/src/components/operacao-secondary-pods-panel.tsx — present.
- dashboard/src/lib/gateway.ts (secondary_pods) — present.
- Commits d831179, 9fbdf3e, b2f7b91, bc90cb7 — all present in git log.
