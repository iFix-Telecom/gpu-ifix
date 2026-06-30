---
phase: 17
slug: dashboard-pod-config-control-owner-controla-todas-as-configs
status: draft
nyquist_compliant: true
wave_0_complete: false
created: 2026-06-30
---

# Phase 17 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.
> Source: 17-RESEARCH.md §Validation Architecture.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework (gateway)** | Go `testing` + testify-style; integration via `internal/integration_test` (testcontainers PG/Redis) |
| **Framework (dashboard)** | Bun test (`*.test.ts`) |
| **Config file** | none — frameworks already in tree |
| **Quick run command** | `cd gateway && go test ./internal/{primary,config,admin,podconfig}/...` + `cd dashboard && bun test <touched>.test.ts` |
| **Full suite command** | `cd gateway && go test ./... -tags integration && gofmt -l .` + `cd dashboard && bun test` |
| **Estimated runtime** | ~60–180s (integration testcontainers dominate) |

---

## Sampling Rate

- **After every task commit:** package-scoped `go test ./internal/{primary,config,admin,podconfig}/...` + `bun test <touched>.test.ts`
- **After every plan wave:** `go test ./... -tags integration` + `gofmt -l .`
- **Before `/gsd:verify-work`:** Full suite green + live UAT (owner edita blocklist no dashboard → reconciler prod evita host na próxima provision)
- **Max feedback latency:** ~180 seconds

> GOTCHA (MEMORY gateway-integration-tests-not-in-executor-check): rodar `-tags integration` + `gofmt -l` ANTES de push — executor's local go-test passa mas CI fica vermelho sem isso.

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| T1 | 17-01 | 1 | POD-CFG-01,02,05 | T-17-01/02/03 | seed idempotent (no overwrite); NOTIFY gated IS DISTINCT FROM; no secret columns | build+grep | `cd gateway && sqlc generate && go build ./internal/db/... && grep -c "CREATE TRIGGER pod_config_" db/migrations/0031_create_pod_config.sql` | gen by task | ⬜ pending |
| T2 | 17-01 | 1 | POD-CFG-02 | T-17-01 | empty-create + idempotent seed + update mutates | integration | `cd gateway && gofmt -l internal/integration_test/pod_config_test.go && go test -tags integration ./internal/integration_test/ -run PodConfig` | created by task | ⬜ pending |
| T1 | 17-02 | 2 | POD-CFG-03 | — | last-good-on-error snapshot (no crash on bad reload) | unit | `cd gateway && gofmt -l internal/podconfig/ && go test ./internal/podconfig/ -run Loader` | created by task | ⬜ pending |
| T2 | 17-02 | 2 | POD-CFG-03 | — | listens on pod_config_changed (NOT upstreams_changed) | build+grep | `cd gateway && go build ./internal/podconfig/ && grep -c "pod_config_changed" internal/podconfig/listen.go && ! grep -q "upstreams_changed" internal/podconfig/listen.go && gofmt -l internal/podconfig/listen.go` | created by task | ⬜ pending |
| T1 | 17-03 | 3 | POD-CFG-04 | — | reconciler reads HOT from snapshot not boot cfg | unit | `cd gateway && gofmt -l internal/primary/ && go test ./internal/primary/ -run Reconcile` | created by task | ⬜ pending |
| T2 | 17-03 | 3 | POD-CFG-04 | — | schedule re-parse + budget from snapshot | unit | `cd gateway && go build ./internal/primary/ ./internal/podconfig/ && go test ./internal/primary/ -run 'Schedule|Budget' && gofmt -l internal/primary/` | created by task | ⬜ pending |
| T3 | 17-03 | 3 | POD-CFG-02,03 | — | first-boot env→DB seed wired; ListenAndReload goroutine | build+grep | `cd gateway && go build ./... && grep -q "podconfig.ListenAndReload" cmd/gateway/main.go && grep -q "SeedPodConfig" cmd/gateway/main.go && gofmt -l cmd/gateway/main.go` | created by task | ⬜ pending |
| T1 | 17-04 | 4 | POD-CFG-07,15 | T-17-11/23 | lifecycle+config read X-Admin-Key gated; config_read reads pod_config NOT boot env; ErrNoRows→envelope no panic | tdd/unit | `cd gateway && gofmt -l internal/admin/lifecycle.go internal/admin/lifecycle_test.go internal/admin/config_read.go internal/admin/config_read_test.go && go test ./internal/admin/ -run 'Lifecycle|ConfigRead'` | created by task | ⬜ pending |
| T2 | 17-04 | 4 | POD-CFG-06 | T-17-12/13/14 | bound validation server-side; static field allowlist (no dyn SQL); no restart/structural | tdd/unit | `cd gateway && gofmt -l internal/admin/config_write.go internal/admin/config_write_test.go && go test ./internal/admin/ -run ConfigWrite && ! grep -qi "os.Exit\|WeightsKey" internal/admin/config_write.go` | created by task | ⬜ pending |
| T3 | 17-04 | 4 | POD-CFG-06,07,15 | T-17-11 | all 3 endpoints mounted inside X-Admin-Key adminRouter; no restart route | build+grep | `cd gateway && go build ./... && grep -q 'MethodGet, "/primary/config"' cmd/gateway/main.go && grep -q 'MethodPatch, "/primary/config"' cmd/gateway/main.go && ! grep -q "gateway/restart" cmd/gateway/main.go && gofmt -l cmd/gateway/main.go` | created by task | ⬜ pending |
| T1 | 17-05 | 5 | POD-CFG-10,11 | T-17-15/17/18 | requireOwner FIRST; refetch live bound via fetchPodConfig; one audit row {field,old,new} | tdd/unit | `cd dashboard && bun test src/lib/admin-actions.test.ts` | created by task | ⬜ pending |
| T2 | 17-05 | 5 | POD-CFG-07,15 | T-17-16 | fetchPrimaryLifecycle + fetchPodConfig GET-only; key never in client wrappers | unit+grep | `cd dashboard && bun test src/lib/gateway.test.ts && grep -q "fetchPodConfig" src/lib/gateway.ts && ! grep -q "GATEWAY_ADMIN_KEY" src/lib/gateway.ts` | created by task | ⬜ pending |
| T1 | 17-06 | 6 | POD-CFG-13,14,15 | T-17-19/20 | page fetches current config; live panel poll; structural read-only; no restart UI | build+grep | `cd dashboard && bun run build && grep -q "/operacao/config" src/components/app-sidebar.tsx && grep -q "fetchPodConfig" "src/app/(dashboard)/operacao/config/page.tsx" && ! grep -qi "reiniciar\|restart" src/components/pod-config-live-panel.tsx` | created by task | ⬜ pending |
| T2 | 17-06 | 6 | POD-CFG-08,12 | T-17-21 | one-click confirm w/ specific impact string; no type-to-confirm; operator read-only | build+grep | `cd dashboard && bun run build && grep -q "updatePodConfig" src/components/pod-config-controls.tsx && grep -q "drenar o pod" src/components/pod-config-controls.tsx && ! grep -qi "digite" src/components/pod-config-controls.tsx` | created by task | ⬜ pending |
| T3 | 17-06 | 6 | POD-CFG-09 | T-17-19 | owner-editable bounds table; min<max copy; operator read-only | build+grep | `cd dashboard && bun run build && grep -q "updatePodConfigBound" src/components/pod-config-controls.tsx && grep -q "menor que o máximo" src/components/pod-config-controls.tsx` | created by task | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

> All 15 tasks carry a real `<automated>` verify command (build/grep gate, unit test, integration test, or TDD test created in-task). No task is MISSING an automated check → `nyquist_compliant: true`. The TDD/test files are authored inside their own task; the Wave 0 list below tracks the scaffolds the executor must stand up first.

---

## Wave 0 Requirements

(from RESEARCH §Wave 0 Gaps — planner converts to Wave 0 tasks)

- [ ] `gateway/internal/podconfig/loader_test.go` — snapshot refresh + last-good-on-error (17-02 T1)
- [ ] `gateway/internal/primary/reconciler_test.go` — offer-selection reads snapshot not cfg (extend) (17-03 T1)
- [ ] `gateway/internal/admin/lifecycle_test.go` — `/admin/primary/lifecycle` admin-key gate + payload (17-04 T1)
- [ ] `gateway/internal/admin/config_read_test.go` — `/admin/primary/config` (GET) reads pod_config row + bounds, ErrNoRows→pod_config_unseeded, admin-key gate (17-04 T1)
- [ ] `gateway/internal/admin/config_write_test.go` — `/admin/primary/config` (PATCH) bound validation + static allowlist + no-restart (17-04 T2)
- [ ] `gateway/db/migrations/0031_create_pod_config.sql` + NOTIFY trigger + sqlc regen + seed-from-env idempotent (integration up/down) (17-01 T1/T2)
- [ ] dashboard: extend `admin-actions.test.ts` for `updatePodConfig` + `updatePodConfigBound` owner-gate + live-bound-refetch (fetchPodConfig) + audit row (17-05 T1)
- [ ] dashboard: extend `gateway.test.ts` for `fetchPrimaryLifecycle` + `fetchPodConfig` wrappers + key-leak grep (17-05 T2)

> NOTE: scope narrowed in 17-CONTEXT.md — NO `/admin/gateway/restart` endpoint this phase (D-02), so RESEARCH's `restart_test.go` Wave 0 gap is DROPPED. Bounds are owner-editable (D-03) so the bounds-editor test path is added. The GET `/admin/primary/config` read endpoint (POD-CFG-15, added in revision) backs the dashboard editor/validation/audit — its test is folded into 17-04 T1.

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Owner edita blocklist no dashboard → reconciler prod evita host na próxima provision | POD-CFG live | Requires live prod gateway + Vast provisioning cycle | Append known-bad host id no blocklist via UI → observe próximo provision skip (painel ao vivo shutdown_reason / lifecycle trail) |
| Hot-reload latency (edit vale em segundos sem restart) | POD-CFG hot | Requires live NOTIFY round-trip | Edit cap no dashboard → confirmar snapshot swap no log do gateway `<1s` |

---

## Validation Sign-Off

- [x] All tasks have `<automated>` verify or Wave 0 dependencies
- [x] Sampling continuity: no 3 consecutive tasks without automated verify
- [x] Wave 0 covers all MISSING references
- [x] No watch-mode flags
- [x] Feedback latency < 180s
- [x] `nyquist_compliant: true` set in frontmatter

**Approval:** ready (pre-execution; `wave_0_complete` flips at execution time)
