---
phase: 17
slug: dashboard-pod-config-control-owner-controla-todas-as-configs
status: draft
nyquist_compliant: false
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
| (planner fills from RESEARCH Phase Requirements → Test Map) | | | POD-CFG-* | | | | | ❌ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

(from RESEARCH §Wave 0 Gaps — planner converts to Wave 0 tasks)

- [ ] `gateway/internal/podconfig/loader_test.go` — snapshot refresh + last-good-on-error
- [ ] `gateway/internal/primary/reconciler_test.go` — offer-selection reads snapshot not cfg (extend)
- [ ] `gateway/internal/admin/lifecycle_test.go` — `/admin/primary/lifecycle` admin-key gate + payload
- [ ] `gateway/db/migrations/0031_create_pod_config.sql` + NOTIFY trigger + sqlc regen + seed-from-env idempotent (integration up/down)
- [ ] dashboard: extend `admin-actions.test.ts` for `updatePodConfig` + `updatePodConfigBounds` owner-gate + bounds-validation + audit row

> NOTE: scope narrowed in 17-CONTEXT.md — NO `/admin/gateway/restart` endpoint this phase (D-02), so RESEARCH's `restart_test.go` Wave 0 gap is DROPPED. Bounds are owner-editable (D-03) so add a bounds-editor test path.

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Owner edita blocklist no dashboard → reconciler prod evita host na próxima provision | POD-CFG live | Requires live prod gateway + Vast provisioning cycle | Append known-bad host id no blocklist via UI → observe próximo provision skip (painel ao vivo shutdown_reason / lifecycle trail) |
| Hot-reload latency (edit vale em segundos sem restart) | POD-CFG hot | Requires live NOTIFY round-trip | Edit cap no dashboard → confirmar snapshot swap no log do gateway `<1s` |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 180s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
