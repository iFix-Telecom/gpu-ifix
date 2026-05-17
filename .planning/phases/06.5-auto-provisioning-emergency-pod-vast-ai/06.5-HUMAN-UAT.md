---
status: partial
phase: 06-auto-provisioning-emergency-pod-vast-ai
source: [06-VALIDATION.md, 06-11-PLAN.md Task 1, 06-CONTEXT.md, 06-WAVE0-GATES.md]
started: 2026-05-13
updated: 2026-05-13
estimated_total_cost_brl: "10-15"
operator: ___________
date_executed: ___________
final_status: pending  # pass | partial | fail
---

# Phase 6 — Human UAT (LIVE Vast.ai)

This document drives the operator-only UAT for Phase 6 (Auto-provisioning
Emergency Pod). All scenarios consume real Vast.ai credit and exercise the
end-to-end stack (gateway server + reconciler + leader election + Vast.ai
REST API + Postgres audit + Sentry breadcrumbs).

The companion runbook is `gateway/docs/RUNBOOK-EMERGENCY-POD.md` — read
the **Deploy** section there before running any UAT below.

---

## Prerequisites

Verify all rows are satisfied **before** starting UAT-1.

- [ ] **Stack deployed:** `ai-gateway-dev` Portainer stack updated to the
      Phase 6 image (verify via Portainer UI → stack → "Updated" timestamp
      after the latest webhook trigger).
- [ ] **Env vars present in Portainer stack** (per RUNBOOK Deploy section):
      `VAST_AI_API_KEY`, `VAST_PRICE_CAP_DPH=0.40`,
      `MONTHLY_EMERGENCY_BUDGET_BRL=200`, `USD_TO_BRL_RATE=5.0`,
      `EMERGENCY_POD_IMAGE_TAG=v1.0`,
      `PROVISION_TRIGGER_FAILED_OVER_SECONDS=120`,
      `PROVISION_HEALTHY_DURATION_SECONDS=300`,
      `PROVISION_IDLE_GRACE_SECONDS=300`,
      `PROVISION_COLDSTART_BUDGET_SECONDS=600`,
      `PRIMARY_HOST_ID=0`, `VAST_API_QPS_LIMIT=1`.
- [ ] **Boot logs confirm Phase 6 is enabled:**
      `ssh vps-ifix-vm 'docker logs ai-gateway-dev_gateway --since 5m 2>&1 | grep -E "vast.Ping|emergency reconciler started"'`
      should show both lines (`vast.Ping ok` + `Phase 6 emergency reconciler started`).
      If `vast.Ping failed` is logged, the API key is wrong/expired — fix BEFORE proceeding.
- [ ] **Migration 0019 applied:**
      `ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -c "\d ai_gateway.emergency_lifecycles"'`
      shows the table with 11 columns and 5 indexes.
- [ ] **FSM at boot is healthy:**
      `ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway /gatewayctl emerg state --format=json'`
      returns `{}` (empty mirror = HEALTHY initial state — reconciler only mirrors on first transition) OR `{"state":"healthy",...}`.
- [ ] **Sentry project ready:** project `ifix-ai-gateway` (or `ifix-ai-gateway-dev`)
      DSN configured in `SENTRY_DSN` env, dashboard accessible.
- [ ] **Vast.ai account funded:** ≥ R$30 (≈ $6) free balance at
      <https://cloud.vast.ai/account/>.
- [ ] **No prior live lifecycle leak:**
      `ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway /gatewayctl emerg lifecycles --since 24h --format=json | jq "[.[] | select(.EndedAt.Valid == false)] | length"'`
      returns `0`. If it returns `1+`, run `force-destroy` first OR investigate via RUNBOOK Incident Playbook.

> **Container name note:** the compose file does not set `container_name`,
> so the Portainer stack-prefixed name is `ai-gateway-dev_gateway` (or
> `ai-gateway-dev-gateway-1` depending on Compose version). Substitute the
> actual name from `ssh vps-ifix-vm 'docker ps | grep gateway'` if the
> commands below 404.

---

## Current Test

[awaiting human testing — operator runs all 6 UATs sequentially below]

## Tests

### 1. UAT-1 — Force-provision happy path against real Vast.ai (SC-1, PRV-01..08)

**Pre-conditions:** FSM at HEALTHY, no live lifecycle.

**Steps:**

```bash
ssh vps-ifix-vm

# Capture timestamp
T0=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
echo "T0=$T0"

# Trigger force-provision
docker exec ai-gateway-dev_gateway /gatewayctl emerg force-provision --reason "phase6_uat_1"

# Poll FSM state every 30s until EMERGENCY_ACTIVE (or timeout at 10min per SC-1)
for i in {1..20}; do
  echo "=== iteration $i / $(date) ==="
  docker exec ai-gateway-dev_gateway /gatewayctl emerg state --format=json
  sleep 30
done

# Once state shows {"state":"emergency_active",...} with a pod_url, capture lifecycle row:
docker exec ai-gateway-dev_gateway /gatewayctl emerg lifecycles --since 30m --format=json | jq '.[0]'
```

**Expected:**

- FSM transitions `healthy → failed_over → emergency_provisioning → emergency_active`
  in ≤10min after `force-provision` (SC-1 ceiling).
- `emerg state` shows `state=emergency_active` with non-empty `pod_url`,
  `pod_instance_id`, and `lifecycle_id`.
- DB lifecycle row has `vast_instance_id IS NOT NULL`,
  `accepted_dph ≤ 0.4001`, `trigger_reason='manual_force'`, and the
  `events` JSONB contains at least an `offer_accepted` and `healthy` entry.
- Gateway logs show
  `lifecycle X marked healthy` (lifecycle.go markHealthy) and
  `dispatcher OverrideTier0("llm", <pod-url>)`.

**Pass/Fail criteria:**

- PASS: all expected criteria met within 10 minutes of `force-provision`.
- FAIL: timeout, `state=cooldown` (silent abort), or no `vast_instance_id` in DB after 10min.

**Cleanup:** UAT-3 will tear down — leave the pod live for now.

**Cost estimate:** R$1–2 (≤ 10min @ ≤ R$2/h cap).

expected: `force-provision` triggers reconciler subscriber → reconciler bids on Vast.ai 4090 ≤ R$2/h → instance reaches `actual_status=running` → `/health` returns `services.llm=healthy` → FSM transitions to `emergency_active` ≤10min after trigger; lifecycle row populated with vast_instance_id + accepted_dph + offer_accepted event in JSONB; dispatcher OverrideTier0 called.
result: [pending]

---

### 2. UAT-2 — Cost calc accuracy (PRV-10, D-D4)

**Pre-conditions:** UAT-1 PASS — at least 1 lifecycle row exists (live or closed).

**Steps:**

```bash
ssh vps-ifix-vm

# Snapshot lifecycle as JSON
docker exec ai-gateway-dev_gateway /gatewayctl emerg lifecycles --since 24h --format=json \
  | jq '.[0] | {id, started: .StartedAt, first_health_pass: (.Events[] | select(.type=="healthy") | .ts), ended: .EndedAt.Time, dph: .AcceptedDph, total_brl: .TotalCostBrl}'

# Cross-reference Vast.ai bill (browser):
#   https://cloud.vast.ai/billing/
# For the matching instance_id, capture (USD billed, hours run on Vast meter).

# Manual sanity formula (D-D4):
#   total_cost_brl ≈ accepted_dph × hours_active × USD_TO_BRL_RATE
# where hours_active = (ended_at - first_health_pass_at) / 3600
```

**Expected:**

- `hours_active` is computed from `first_health_pass_at` (NOT `started_at`).
  This is the D-D4 invariant — cold-start time is excluded from the audit cost.
- `total_cost_brl` matches the manual formula within ±5% (BRL conversion drift).
- Vast.ai bill shows ≤ accepted_dph × hours_running (Vast bills cold-start;
  audit does not — the gap = cold-start cost, expected and acceptable).

**Pass/Fail criteria:**

- PASS: `total_cost_brl` matches formula AND Vast bill within ±5% of formula+cold-start estimate.
- FAIL: `total_cost_brl` is 0 (would indicate `first_health_pass_at` was NULL during close), OR off by >5% from formula.

**Cleanup:** none (read-only).

**Cost estimate:** R$0.

expected: cost calculation reads first_health_pass_at (not started_at), multiplies dph × hours × USD_TO_BRL_RATE, total_cost_brl matches formula within ±5%; Vast.ai bill ≤ formula+cold-start within tolerance.
result: [pending]

---

### 3. UAT-3 — Force-destroy + dispatcher restore (PRV-08, D-E1)

**Pre-conditions:** UAT-1 left FSM in `emergency_active` with a live pod.
If not, repeat UAT-1 steps 1-3 first.

**Steps:**

```bash
ssh vps-ifix-vm

# Confirm live state + pod_url is being routed by the dispatcher.
docker exec ai-gateway-dev_gateway /gatewayctl emerg state --format=json
# Note the pod_url — that is the override target for tier-0 LLM.

# (Optional smoke) — send a chat completion through the gateway and verify
# audit_log.upstream is the emergency pod url. Skip if no test API key set.
# curl https://ai-gateway-dev.converse-ai.app/v1/chat/completions \
#   -H "Authorization: Bearer ${TEST_API_KEY}" \
#   -H "Content-Type: application/json" \
#   -d '{"model":"qwen","messages":[{"role":"user","content":"hi"}],"max_tokens":4}'

# Tear down
docker exec ai-gateway-dev_gateway /gatewayctl emerg force-destroy

# Wait one reconciler tick (~5s) then re-check state
sleep 10
docker exec ai-gateway-dev_gateway /gatewayctl emerg state --format=json

# Confirm lifecycle row closed with shutdown_reason='manual'
docker exec ai-gateway-dev_gateway /gatewayctl emerg lifecycles --since 30m --format=json \
  | jq '.[0] | {id, ended: .EndedAt, shutdown: .ShutdownReason}'

# Vast.ai dashboard manual check: confirm instance gone
# https://cloud.vast.ai/instances/
```

**Expected:**

- Pod destroyed in Vast.ai (instance disappears from dashboard within ~30s).
- FSM transitions `emergency_active → recovering → cooldown` (or directly to `cooldown`).
- Lifecycle row has `EndedAt.Valid=true`, `shutdown_reason='manual'`.
- Dispatcher restored to primary routing — gateway logs show `RestoreTier0("llm")` (lifecycle.go cutback path).

**Pass/Fail criteria:**

- PASS: pod gone in Vast UI + lifecycle closed with `manual` + `RestoreTier0` log line within 30s of `force-destroy`.
- FAIL: pod still running after 60s OR lifecycle NOT closed.

**Cleanup:** if Vast dashboard still shows the instance after 60s, manually destroy via CLI: `VAST_AI_API_KEY=... ./pod/scripts/vast-ai.sh destroy <instance_id>`.

**Cost estimate:** R$0 (cleanup of UAT-1).

expected: force-destroy publishes typed EmergEvent → leader subscriber consumes → destroyAndCloseLifecycle invoked → vast.DestroyInstance returns success → lifecycle close with shutdown_reason='manual' → dispatcher RestoreTier0("llm") called within ≤30s.
result: [pending]

---

### 4. UAT-4 — Sentry breadcrumbs forensic visibility (D-E4)

**Pre-conditions:** UAT-1 OR UAT-3 executed — Sentry events should already exist.

**Steps:**

1. Open <https://sentry.io> → project `ifix-ai-gateway` (or `-dev`).
2. Search filter: `subsystem:emerg`.
3. Verify ≥1 event tagged `subsystem=emerg` exists from the UAT window.
4. Click into the most recent event. Inspect the **Breadcrumbs** panel.
5. Confirm the breadcrumb chain shows the FSM transitions for the lifecycle:
   - `state healthy→failed_over` (or `→emergency_provisioning` if forced)
   - `state emergency_provisioning→emergency_active`
   - `state emergency_active→recovering`
   - `state recovering→cooldown`
   - + lifecycle metadata: `lifecycle_id`, `pod_instance_id`, `shutdown_reason`.

**Expected:**

- Sentry shows ≥1 event with tag `subsystem=emerg`.
- Breadcrumb chain is complete (all 4 transition entries visible).
- No PII in breadcrumb data — only IDs, state names, and reasons.

**Pass/Fail criteria:**

- PASS: ≥4 breadcrumb entries per lifecycle, no PII.
- FAIL: missing breadcrumbs OR PII in any breadcrumb (api keys, request bodies, tenant secrets).

**Cleanup:** none.

**Cost estimate:** R$0.

expected: Sentry events tagged subsystem=emerg show ≥4 breadcrumbs per lifecycle (one per FSM transition) with lifecycle_id + pod_instance_id + reason; no PII in payload.
result: [pending]

---

### 5. UAT-5 — Budget alert threshold (PRV-05, D-D2)

**Operator may skip this UAT** if the previous month's emergency spend is already ≥ R$199 — the alert will fire on its own as soon as the next lifecycle closes. The pre-seed below is for testing the alert path in isolation.

**Pre-conditions:** FSM at HEALTHY, no live lifecycle.

**Steps:**

```bash
ssh vps-ifix-vm

# Pre-seed budget at R$199 (1 BRL below the 200 threshold)
docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -c "
INSERT INTO ai_gateway.emergency_lifecycles
  (started_at, ended_at, total_cost_brl, trigger_reason, shutdown_reason)
VALUES
  (date_trunc('month', NOW()), NOW(), 199.0, 'manual_force', 'cutback_idle');
"

# Verify pre-seed visible
docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -c "
SELECT COALESCE(SUM(total_cost_brl), 0) AS month_cost
  FROM ai_gateway.emergency_lifecycles
 WHERE started_at >= date_trunc('month', NOW())
   AND ended_at IS NOT NULL;
"
# Expect month_cost ≈ 199.0

# Run UAT-1 steps 1-3 (force-provision + cleanup), spend ~R$2-3
# ... (see UAT-1 Steps block above) ...

# After UAT-1 cleanup (force-destroy + lifecycle closed), wait for next budget
# tick (≤60s — the budget check is rate-limited via lastBudgetCheckUnix atomic).
sleep 90

# Open Sentry → filter `alert:budget_exceeded` — expect 1 fresh event
# https://sentry.io/.../events/?query=subsystem%3Aemerg+alert%3Abudget_exceeded
```

**Expected:**

- Sentry receives 1 event with tags `subsystem=emerg`, `alert=budget_exceeded`.
- Event level is `Warning` (not Error — alerts are non-blocking per D-D2).
- Event extras include `month_cost_brl` (~ R$201-202) and `budget_brl` (200.0).
- Reconciler does NOT block subsequent provisioning attempts (D-D2 — alert only).
- Operator receives Sentry email/Slack notification (if configured).

**Pass/Fail criteria:**

- PASS: 1 Sentry warning event AND reconciler still functional (next force-provision works).
- FAIL: no Sentry event OR reconciler refuses next provision (would indicate an auto-block bug).

**Cleanup:**

```bash
# Remove the pre-seed row so subsequent month-cost queries are accurate
docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -c "
DELETE FROM ai_gateway.emergency_lifecycles
 WHERE total_cost_brl = 199.0
   AND trigger_reason = 'manual_force'
   AND shutdown_reason = 'cutback_idle';
"
```

**Cost estimate:** R$2–3 (the embedded UAT-1 cycle).

expected: pre-seed R$199 + run a small lifecycle (~R$2-3) → checkBudget tick observes month_cost > MonthlyEmergencyBudgetBRL → CaptureMessage with tags subsystem=emerg + alert=budget_exceeded + level=Warning; dedupe gate ensures exactly 1 emit per UTC day even with concurrent ticks.
result: [pending]

---

### 6. UAT-6 — Cancel-in-flight against real Vast.ai (PRV-09, SC-3)

**Pre-conditions:** FSM at HEALTHY, no live lifecycle.

**Steps:**

```bash
ssh vps-ifix-vm

# Trigger force-provision
docker exec ai-gateway-dev_gateway /gatewayctl emerg force-provision --reason "phase6_uat_6_cancel"

# IMMEDIATELY (within < 30s, BEFORE pod becomes healthy) — publish a
# fake `local-llm` recovery event on the breaker channel. The reconciler's
# subscriber treats this as the trigger to cancel-in-flight (Phase 3 D-D1
# Pub/Sub channel name).
docker exec infra-redis-1 redis-cli -n 5 PUBLISH gw:upstreams:events \
  '{"upstream":"local-llm","state":"closed","timestamp":'"$(date +%s)"'}'

# Wait for cancel propagation
sleep 30

# Confirm FSM back to healthy
docker exec ai-gateway-dev_gateway /gatewayctl emerg state --format=json

# Confirm lifecycle row closed with shutdown_reason='cancelled_in_flight'
docker exec ai-gateway-dev_gateway /gatewayctl emerg lifecycles --since 5m --format=json \
  | jq '.[0] | {id, started: .StartedAt, ended: .EndedAt, shutdown: .ShutdownReason, instance_id: .VastInstanceID}'

# Manual Vast dashboard check — no instance leak
# https://cloud.vast.ai/instances/
```

**Expected:**

- FSM returns to `healthy` (or `cooldown` briefly first) within ≤30s of the cancel publish.
- Lifecycle row closed with `shutdown_reason='cancelled_in_flight'`.
- ZERO leaked instance in Vast.ai (the triple-layer cancel — context + pubsub + post-create destroy — guarantees no leak per D-C3):
  - If cancelled BEFORE `create_instance`: `vast_instance_id` is NULL on the row.
  - If cancelled AFTER `create_instance`: `vast_instance_id` is populated AND the dashboard shows the instance was destroyed (history, not active).

**Pass/Fail criteria:**

- PASS: FSM healthy + lifecycle closed `cancelled_in_flight` + zero active instance in Vast UI.
- FAIL: any active instance still alive in Vast UI after 60s, OR lifecycle did NOT close.

**Cleanup:**

- Vast.ai dashboard manual check; if any leak detected, destroy manually:
  `VAST_AI_API_KEY=... ./pod/scripts/vast-ai.sh destroy <instance_id>`.

**Cost estimate:** R$0–1 (cancel before /health pass typically incurs no Vast bill, but a post-create cancel may bill ~30s).

expected: force-provision starts → recovery Pub/Sub event arrives mid-flight → reconciler context.WithCancel triggers → lifecycle closes with shutdown_reason='cancelled_in_flight'; if instance was created, vast.DestroyInstance is called before close; ZERO leaked instance in Vast UI; FSM back to healthy within 30s.
result: [pending]

---

## Summary

total: 6
passed: 0
issues: 0
pending: 6
skipped: 0
blocked: 0

Total cost so far: R$ ____

## Final Sign-off

- [ ] All 6 UATs PASS — Phase 6 ready for production rollout
- [ ] PARTIAL: ___ of 6 PASS; deferred items: ___
- [ ] FAIL: blocking issue(s) found, see Gaps below

- **Operator:** ___________
- **Date:** ___________
- **Total Vast.ai cost:** R$ ___________
- **Sentry events linked (UAT-4, UAT-5):** ___________

## Gaps

(populate during/after execution — list any UAT that did not pass, why,
and what follow-up issue/plan tracks the remediation)
