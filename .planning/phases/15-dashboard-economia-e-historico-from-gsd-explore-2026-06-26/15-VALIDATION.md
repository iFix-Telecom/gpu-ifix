---
phase: 15
slug: dashboard-economia-e-historico
status: draft
nyquist_compliant: false
wave_0_complete: false
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
| TBD | — | — | OBS-09 | — | N/A | unit | TBD | ❌ W0 | ⬜ pending |
| TBD | — | — | OBS-10 | — | N/A | unit | TBD | ❌ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] Confirm dashboard test runner + add component test fixture for economy panel (if none)
- [ ] Gateway: sqlc codegen gate (CI runs `sqlc generate` diff check) covers new queries

*Planner to finalize.*

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

**Approval:** pending
