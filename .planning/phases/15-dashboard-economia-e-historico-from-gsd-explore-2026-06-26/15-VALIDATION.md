---
phase: 15
slug: dashboard-economia-e-historico
status: planned
nyquist_compliant: true
wave_0_complete: false  # economy_test.go created in 15-01.T2 (post-sqlc-gen)
created: 2026-06-26
---

# Phase 15 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | Go: `go test` (gateway); Dashboard: per `dashboard/package.json` (planner to confirm vitest/jest) |
| **Config file** | gateway: `go.mod`; dashboard: `dashboard/package.json` |
| **Quick run command** | `cd gateway && go test ./internal/admin/... ./internal/db/...` |
| **Full suite command** | `cd gateway && go test ./... && cd ../dashboard && <pkg test>` |
| **Estimated runtime** | ~TBD (planner to fill) |

---

## Sampling Rate

- **After every task commit:** Run quick run command for the touched module
- **After every plan wave:** Run full suite command
- **Before `/gsd:verify-work`:** Full suite must be green
- **Max feedback latency:** TBD

---

## Per-Task Verification Map

*Filled by planner / gsd-nyquist-auditor during planning. Each OBS-09/OBS-10 task maps to an automated assertion (sqlc query compiles + gen check, handler returns expected JSON shape, dashboard component renders given fixture data).*

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 15-01.T1 | 15-01 | 1 | OBS-09 | T-15-01 | parameterized date range; no tenant filter | codegen | `cd gateway && sqlc generate && git diff --exit-code internal/db/gen/` | ❌ new queries | ⬜ pending |
| 15-01.T2 | 15-01 | 1 | OBS-09 | T-15-01..04 | ROI/pct null-safe; reused accrual; metadata-only | unit (Go) | `cd gateway && go test ./internal/admin/ -run Economy` | ❌ W0 economy_test.go | ⬜ pending |
| 15-02.T1 | 15-02 | 2 | OBS-10 | T-15-07 | metadata-only select; ILIKE parameterized | codegen | `cd gateway && sqlc generate && git diff --exit-code internal/db/gen/` | ❌ new query | ⬜ pending |
| 15-02.T2 | 15-02 | 2 | OBS-10 | T-15-05..09 | parameterized search; limit cap; total | unit (Go) | `cd gateway && go test ./internal/admin/ -run Audit` | ⚠️ extend audit_test.go | ⬜ pending |
| 15-03.T1 | 15-03 | 2 | OBS-09 | T-15-10 | key stays server-side; no fan-out | unit (vitest) | `cd dashboard && bun run test src/lib/gateway.test.ts` | ⚠️ extend gateway.test.ts | ⬜ pending |
| 15-03.T2 | 15-03 | 2 | OBS-09 | T-15-10 | null-safe render; single-axis chart | type/lint | `cd dashboard && bunx tsc --noEmit && bun run lint` | ❌ new components | ⬜ pending |
| 15-03.T3 | 15-03 | 2 | OBS-09 | T-15-11,12 | GET-only; single server-side fetch | type/lint | `cd dashboard && bunx tsc --noEmit && bun run lint` | ❌ new page | ⬜ pending |
| 15-04.T1 | 15-04 | 3 | OBS-10 | T-15-14 | key server-side; total typed | unit (vitest) | `cd dashboard && bun run test src/lib/gateway.test.ts` | ⚠️ extend gateway.test.ts | ⬜ pending |
| 15-04.T2 | 15-04 | 3 | OBS-10 | T-15-13,15,16 | GET-only; metadata-only render; honest pager | type/lint | `cd dashboard && bunx tsc --noEmit && bun run lint` | ⚠️ modify page+table | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [x] Dashboard test runner confirmed: **vitest** (`dashboard/package.json` "test": "vitest run").
- [x] Gateway test runner: Go stdlib `go test`; sqlc codegen gate enforced by CI (`build-gateway.yml:61-67` `git diff --exit-code internal/db/gen/`).
- [ ] 15-01.T2 creates `gateway/internal/admin/economy_test.go` (clones `operations_test.go` fakes/helpers) — the only fully-new Go test file. It compiles only AFTER 15-01.T1 sqlc regen (references generated param/row types), so RED-first is gated on the gen output.
- [ ] 15-02.T2 + 15-03.T1 + 15-04.T1 extend existing test files (`audit_test.go`, `gateway.test.ts`).
- [ ] No framework install needed — both suites already exist.

**Per-task `<automated>` continuity:** every task in all 4 plans carries an automated verify command (codegen diff, `go test -run`, or `vitest`/`tsc`/`lint`). No 3 consecutive tasks lack an automated check.

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Economy numbers match reality | OBS-09 | Cross-check phantom vs Vast against real billing_events/primary_lifecycles rows | Query DB for a known period, compare to `/economia` panel output |

*Automated coverage applies to query/handler/render; the end-to-end "number is correct" check is manual against live data.*

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < TBD
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** planner — 2026-06-26
