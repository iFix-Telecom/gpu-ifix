---
phase: 6
slug: auto-provisioning-emergency-pod-vast-ai
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-05-13
---

# Phase 6 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution. Source: 06-RESEARCH.md §"Validation Architecture".

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | Go stdlib `testing` + `testcontainers-go v0.34.0` (Postgres 16 + Redis 7) |
| **Config file** | `gateway/internal/integration_test/setup_test.go` (build tag `integration`) |
| **Quick run command** | `cd gateway && go test -race ./internal/emerg/... ./internal/redisx/...` |
| **Full suite command** | `cd gateway && go test -race -tags=integration ./...` |
| **Slow suite (live UAT scenarios)** | `cd gateway && go test -race -tags="integration integration_slow" ./internal/integration_test/emerg_*_test.go` |
| **Estimated runtime** | ~5s (unit only) / ~30-60s (integration per-wave) / ~3min (full suite) / ~5-15min (live UAT against real Vast.ai) |

---

## Sampling Rate

- **After every task commit:** Run `cd gateway && go test -race ./internal/emerg/... ./internal/redisx/...` (~5s, unit only)
- **After every plan wave:** Run `cd gateway && go test -race -tags=integration ./internal/integration_test/emerg_*_test.go` (~30-60s)
- **Before `/gsd-verify-work`:** Full suite must be green — `cd gateway && go test -race -tags=integration ./...` (~3min)
- **Max feedback latency:** 60 seconds (per-wave gate)

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 6-01-01 | 01 | 0 | PRV-01 | T-6-01 | Vast REST client uses Bearer auth, never logs API key | unit | `cd gateway && go test -race ./internal/emerg/vast/... -run TestClientAuth` | ❌ W0 | ⬜ pending |
| 6-01-02 | 01 | 0 | PRV-01 | — | Vast REST client returns parsed Offer/Instance/error | unit | `cd gateway && go test -race ./internal/emerg/vast/...` | ❌ W0 | ⬜ pending |
| 6-02-01 | 02 | 1 | PRV-02 | — | FSM 9-state transitions correctly under signals | unit | `cd gateway && go test -race ./internal/emerg/... -run TestFSMTransitions` | ❌ W0 | ⬜ pending |
| 6-02-02 | 02 | 1 | PRV-02 | — | FSM is in-process autoritativo + Redis Hash mirror | unit | `cd gateway && go test -race ./internal/redisx/... -run TestEmergMirror` | ❌ W0 | ⬜ pending |
| 6-03-01 | 03 | 1 | PRV-05 | T-6-02 | DB partial unique index blocks 2nd live row (singleton) | integration | `cd gateway && go test -race -tags=integration ./internal/integration_test/emerg_singleton_test.go` | ❌ W0 | ⬜ pending |
| 6-03-02 | 03 | 1 | PRV-10 | — | Migration 0019 creates emergency_lifecycles + indexes | integration | `cd gateway && go test -race -tags=integration ./internal/db/... -run TestMigration0019` | ❌ W0 | ⬜ pending |
| 6-04-01 | 04 | 2 | PRV-03 | T-6-03 | Leader lock blocks 2nd replica from advancing FSM | integration | `cd gateway && go test -race -tags=integration ./internal/integration_test/emerg_leader_test.go` | ❌ W0 | ⬜ pending |
| 6-04-02 | 04 | 2 | PRV-03 | — | redsync `(false, nil)` quorum case treated as lost lock (Pitfall 4) | unit | `cd gateway && go test -race ./internal/emerg/... -run TestExtendQuorumLoss` | ❌ W0 | ⬜ pending |
| 6-05-01 | 05 | 2 | PRV-04 | — | Trigger fires after sustained `local-llm` OPEN ≥ 120s | integration | `cd gateway && go test -race -tags=integration ./internal/integration_test/emerg_trigger_test.go` | ❌ W0 | ⬜ pending |
| 6-05-02 | 05 | 2 | PRV-04 | — | Trigger does not fire on transient OPEN→HALF_OPEN→CLOSED | unit | `cd gateway && go test -race ./internal/emerg/... -run TestTriggerTransient` | ❌ W0 | ⬜ pending |
| 6-06-01 | 06 | 3 | PRV-06 | — | Mock Vast: search → create → instance ready → registered | integration | `cd gateway && go test -race -tags=integration -run TestEmergProvisionHappyPath` | ❌ W0 | ⬜ pending |
| 6-06-02 | 06 | 3 | PRV-05 | T-6-04 | Price cap rejects offers > $0.40/h with epsilon (Pitfall 5) | unit | `cd gateway && go test -race ./internal/emerg/... -run TestPriceCap` | ❌ W0 | ⬜ pending |
| 6-06-03 | 06 | 3 | D-A3 | — | Bid race retry 3x; aborts with `offer_race_lost` after 3 fails | integration | `cd gateway && go test -race -tags=integration -run TestEmergBidRaceLost` | ❌ W0 | ⬜ pending |
| 6-06-04 | 06 | 3 | PRV-07 | — | /health poll passes only when `services.llm.status == "healthy"` | unit + integration | `cd gateway && go test -race ./internal/emerg/... -run TestHealthCheck` + integration TestEmergProvisionHappyPath | ❌ W0 | ⬜ pending |
| 6-06-05 | 06 | 3 | Pitfall 9 | — | `actual_status ∈ {exited, unknown, offline}` triggers destroy + close | unit | `cd gateway && go test -race ./internal/emerg/... -run TestActualStatusTerminal` | ❌ W0 | ⬜ pending |
| 6-07-01 | 07 | 4 | PRV-09 | — | Cancel-in-flight pre-create: no instance ever created | integration | `cd gateway && go test -race -tags=integration -run TestEmergCancelPreCreate` | ❌ W0 | ⬜ pending |
| 6-07-02 | 07 | 4 | PRV-09 | — | Cancel-in-flight post-create: vast.destroy called before register | integration | `cd gateway && go test -race -tags=integration -run TestEmergCancelPostCreate` | ❌ W0 | ⬜ pending |
| 6-07-03 | 07 | 4 | D-D5 | — | Leader recovery zombie: GetInstance returns exited → destroy + close | integration | `cd gateway && go test -race -tags=integration -run TestEmergLeaderRecoveryZombie` | ❌ W0 | ⬜ pending |
| 6-08-01 | 08 | 5 | PRV-08 | — | Cutback after 5min healthy (accelerated) | integration | `cd gateway && go test -race -tags=integration -run TestEmergCutback` | ❌ W0 | ⬜ pending |
| 6-08-02 | 08 | 5 | PRV-08 | — | Destroy after 5min idle grace (accelerated) | integration | `cd gateway && go test -race -tags=integration -run TestEmergIdleDestroy` | ❌ W0 | ⬜ pending |
| 6-08-03 | 08 | 5 | D-C4 | — | Multi-failover ride-out: new OPEN events do NOT spawn new lifecycles | integration | `cd gateway && go test -race -tags=integration -run TestEmergMultiFailoverRideOut` | ❌ W0 | ⬜ pending |
| 6-09-01 | 09 | 6 | PRV-05 | T-6-05 | Sentry warning when month_cost > MONTHLY_EMERGENCY_BUDGET_BRL | integration | `cd gateway && go test -race -tags=integration -run TestEmergBudgetAlert` | ❌ W0 | ⬜ pending |
| 6-09-02 | 09 | 6 | D-D2 | — | Budget alert dedupe: 1 emit per day max (Pitfall 11) | unit | `cd gateway && go test -race ./internal/emerg/... -run TestBudgetDedupe` | ❌ W0 | ⬜ pending |
| 6-10-01 | 10 | 6 | PRV-10 | — | Each lifecycle has all audit fields populated | integration | `cd gateway && go test -race -tags=integration -run TestEmergAuditCompleteness` | ❌ W0 | ⬜ pending |
| 6-11-01 | 11 | 7 | — | — | gatewayctl `emerg state` JSON contract | unit | `cd gateway && go test -race ./cmd/gatewayctl/... -run TestEmergStateCmd` | ❌ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `gateway/internal/emerg/fsm.go` + `fsm_test.go` — FSM 9-state transitions (PRV-02)
- [ ] `gateway/internal/emerg/vast/client.go` + `client_test.go` — Vast REST client w/ httptest.Server fixtures (PRV-01)
- [ ] `gateway/internal/emerg/vast/types.go` — Offer, Instance, ErrOfferRaceLost sentinel
- [ ] `gateway/internal/emerg/lifecycle.go` + `lifecycle_test.go` — provision + cancel + destroy paths (PRV-06, PRV-09, PRV-10)
- [ ] `gateway/internal/emerg/recovery.go` + `recovery_test.go` — leader recovery 3 cenários (D-D5)
- [ ] `gateway/internal/emerg/budget.go` + `budget_test.go` — monthly budget alert + dedupe (D-D2 / PRV-05)
- [ ] `gateway/internal/emerg/reconciler.go` + `reconciler_test.go` — 1Hz tick + leader CAS (PRV-03, PRV-04)
- [ ] `gateway/internal/emerg/subscribe.go` + `subscribe_test.go` — Pub/Sub gw:upstreams:events consumer
- [ ] `gateway/internal/redisx/emerg.go` + `emerg_test.go` — Redis Hash + Pub/Sub helpers + redsync wrapper
- [ ] `gateway/internal/integration_test/emerg_provision_happy_test.go` — full provision happy path
- [ ] `gateway/internal/integration_test/emerg_cancel_pre_create_test.go` — cancel before create_instance (PRV-09)
- [ ] `gateway/internal/integration_test/emerg_cancel_post_create_test.go` — cancel after create + assert destroy (PRV-09)
- [ ] `gateway/internal/integration_test/emerg_leader_test.go` — leader lock blocks 2nd replica (PRV-03, SC-2)
- [ ] `gateway/internal/integration_test/emerg_leader_recovery_zombie_test.go` — recovery (D-D5)
- [ ] `gateway/internal/integration_test/emerg_trigger_test.go` — sustained FAILED_OVER trigger (PRV-04)
- [ ] `gateway/internal/integration_test/emerg_singleton_test.go` — DB unique index (D-B5 / PRV-05)
- [ ] `gateway/internal/integration_test/emerg_budget_alert_test.go` — Sentry alert (D-D2 / PRV-05)
- [ ] `gateway/db/migrations/0019_emergency_lifecycles.sql` — schema + indexes (use 0019, NOT 0017 per RESEARCH Pitfall 10)
- [ ] `gateway/db/queries/emergency_lifecycles.sql` — sqlc queries
- [ ] `gateway/sqlc.yaml` — append `db/queries/emergency_lifecycles.sql` to queries list
- [ ] `gateway/cmd/gatewayctl/emerg.go` — 4 subcommands (state, force-provision, force-destroy, lifecycles)
- [ ] `gateway/cmd/gateway/main.go` — wire NewReconciler + go reconciler.Run + dispatcher OverrideTier0 hook
- [ ] `gateway/internal/proxy/dispatcher.go` — extend with `emerg.IsActive()` check + OverrideTier0/RestoreTier0
- [ ] `gateway/internal/obs/metrics.go` — add 7 emergency_* metrics

**Test infrastructure: NONE missing — testcontainers + Postgres 16 + Redis 7 already in setup_test.go (Phase 4).**

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Real Vast.ai API integration (search → create → cold-start ~5min → /health pass → register → audit) | PRV-01..08, SC-1 | Live cost (~$0.10–$0.50 per UAT run); requires real Vast inventory; cold-start is real-time 3-15min | UAT-1: post-deploy in `ai-gateway-dev` Portainer stack, `docker exec ifix-ai-gateway /gatewayctl emerg force-provision --reason "phase6_uat"` then poll `gatewayctl emerg state` until `EMERGENCY_ACTIVE` |
| Cost calc accuracy (BRL conversion, hours_active correct) | PRV-10, D-D4 | Cumulative across multiple real lifecycles; manual verification of BRL math | UAT-2: Run UAT-1 N times, then `gatewayctl emerg lifecycles --since 24h`; cross-check `total_cost_brl` against Vast.ai bill |
| Manual force-destroy + audit row close + dispatcher restore | PRV-08, D-E1 | Operator-triggered scenario | UAT-3: After UAT-1 in ACTIVE state, `docker exec ifix-ai-gateway /gatewayctl emerg force-destroy`; check audit row + dispatcher metric |
| Sentry breadcrumbs forensic visibility | D-E4 | Sentry dashboard inspection | UAT-4: After UAT-1, check Sentry events tagged `subsystem=emerg`; verify breadcrumb chain (state X→Y for each FSM transition) |
| Budget alert fires + WhatsApp/email path validated (Phase 7 hand-off) | PRV-05, D-D2 | Monthly aggregate query against real DB; Phase 7 alerting | UAT-5: Pre-seed budget at R$199, run UAT-1 (~R$5 spent) → check Sentry warning emitted; Phase 7 alerting follow-up |
| Cancel-in-flight against real Vast.ai (real bid + real cancel) | PRV-09, SC-3 | Race timing only reproducible against real API | UAT-6: Trigger force-provision then immediately publish `local-llm` recovery event via Redis CLI; verify no leaked instance via `vast list` |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references (24 files listed above)
- [ ] No watch-mode flags
- [ ] Feedback latency < 60s (per-wave gate)
- [ ] `nyquist_compliant: true` set in frontmatter (after planner verifies all tasks reference automated commands above)

**Approval:** pending
