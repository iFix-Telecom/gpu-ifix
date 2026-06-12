---
phase: 12
slug: gateway-resilience-remediation
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-06-12
---

# Phase 12 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (go 1.24.9) |
| **Config file** | `gateway/go.mod` |
| **Quick run command** | `cd gateway && go test ./internal/primary/ ./internal/upstreams/ ./internal/proxy/ ./internal/breaker/` |
| **Full suite command** | `cd gateway && go build ./... && go test ./...` |
| **Estimated runtime** | ~60 seconds |

---

## Sampling Rate

- **After every task commit:** Run quick run command (packages touched by the task)
- **After every plan wave:** Run full suite command
- **Before `/gsd:verify-work`:** Full suite must be green
- **Max feedback latency:** 120 seconds

---

## Per-Task Verification Map

> To be filled by planner from RESEARCH.md `## Validation Architecture` section.

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| TBD | — | — | RES-11 | — | FSM Ready tick detects dead instance (404 3-strike + exited/stopped), transitions Ready→Draining→Asleep | unit | `cd gateway && go test ./internal/primary/ -run TestEvaluateReady` | ❌ W0 | ⬜ pending |
| TBD | — | — | RES-12 | — | Probe tick + health endpoint resolve tier-0 via Resolve(role,0), honoring tier0Override | unit | `cd gateway && go test ./internal/upstreams/` | ❌ W0 | ⬜ pending |
| TBD | — | — | RES-13 | — | Dial-failure (connection-class) falls through to tier-1 chain; sensitive tenants still 503 | unit | `cd gateway && go test ./internal/proxy/` | ❌ W0 | ⬜ pending |
| TBD | — | — | RES-13 / D-18 | — | Chaos re-run (dev then prod): zero connection-class 502 during kill window | manual (HUMAN-UAT) | — | — | ⬜ pending |
| TBD | — | — | CAP-01 | — | Saturation baseline decision doc exists | doc | — | — | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] Unit test stubs for Ready-tick death classification (`internal/primary/`)
- [ ] Unit test stubs for prober Resolve parity (`internal/upstreams/`)
- [ ] Unit test stubs for connection-class detection + fallthrough (`internal/proxy/`)

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Prod chaos re-run zero-502 gate | RES-13 (D-16/D-18) | Requires live Vast pod kill + real traffic; destructive + costs ~$0.80-1.50 | Re-run 11-07 chaos recipe: provision cheapest qualified pod (D-17), load via 11-07 recipe, Vast API DELETE, assert zero `upstream_unreachable` 502s T+0..end; 503 `sensitive_block` expected |
| Billing-stop critical alert fan-out | RES-11 (D-03) | Alert delivery (Chatwoot+ClickUp+Brevo) end-to-end needs live channels | Trigger detection in dev with mocked Vast status `exited`; confirm critical alert with "Vast account sem crédito" title |
| `gatewayctl primary state` coherence | RES-11 (D-05) | CLI output inspection against live routing table | After force-up + kill cycle, `pod_url`/`lifecycle_id` must match proxy routing table |
