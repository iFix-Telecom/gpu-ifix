---
status: pending
phase: 08-client-integration-converseai-chat-ifix
source: [08-CONTEXT.md, 08-04-PLAN.md Task 2, ROADMAP Phase 8 SC1-SC4]
started: ___________
updated: ___________
operator: ___________
date_executed: ___________
final_status: pending  # pass | partial | fail
---

# Phase 8 — Human UAT (Live Client Integration)

This document drives the operator-only UAT for Phase 8 (Client Integration —
ConverseAI v4 + Chat Ifix). The scenarios exercise the **live**, real-traffic
integration: switching each client app's `base_url`/`api_key` env vars to the
gateway, running the committed smoke scripts against real dev traffic,
drilling the rollback procedure, and cross-checking per-tenant visibility on
the Phase 7 dashboard.

**The autonomous Phase 8 build is NOT blocked by this UAT.** Plans 08-01..08-03
ship only the gpu-ifix-side artifacts — the idempotent `provision-tenants.sh`
seed script, both smoke scripts, the WhatsApp audio fixture + baseline — and
are already green. This UAT is the live-credential, live-traffic verification
that cannot run autonomously, mirroring the 03-08 / 04-09 / 06-11 / 07-09
deferred-UAT pattern.

The companion runbook is `gateway/docs/RUNBOOK-CLIENT-INTEGRATION.md` — read
its **Mental Model** + **ROLLBACK procedure** sections before running any UAT
below. UAT-3 drills that runbook's ROLLBACK procedure directly.

---

## Prerequisites

Verify all rows are satisfied **before** starting UAT-1. If a row cannot be
satisfied (most likely the gateway-not-deployed gate), the affected UAT
scenarios are `passed_partial` — see the fallback note at the bottom.

- [ ] **Gateway deployed:** the gateway is deployed to the `ai-gateway-dev`
      Portainer stack. **This is currently blocked** on Phase 6 emergency-pod
      integration tests (a separate debug session) — per 08-CONTEXT.md
      `## Deferred Ideas`. If still blocked, every UAT below is `passed_partial`.
- [ ] **Tenants provisioned + keys captured:** run
      `scripts/integration-smoke/provision-tenants.sh --mint-keys` **once**
      against the gateway DB; capture the three raw keys it prints to stdout —
      the `converseai` tenant key, the `chat-ifix` tenant key, and the
      `phase-8-dashboard` admin key. The script is idempotent for
      `tenant create`; `--mint-keys` mints fresh key rows, so run it once.
- [ ] **ConverseAI v4 env vars switched:** the `converseai` tenant key + the
      gateway `base_url` set as env vars in the `converseai-v4-dev` Portainer
      stack — for **both** consumers (`apps/api` Elysia/OpenAI-SDK **and**
      `agents/` Python/LangChain). See `RUNBOOK-CLIENT-INTEGRATION.md`
      Required Env Vars table for the exact var names (operator must confirm
      them against the `converseai-v4` repo).
- [ ] **Chat Ifix env vars switched:** the `chat-ifix` tenant key + the
      gateway `base_url` set in the `campanhas-chatifix` backend deploy config.
- [ ] **Python deps installed:** `pip install -r scripts/integration-smoke/requirements.txt`
      (`httpx`, `numpy`, `structlog`, `jsonschema`) on the box that runs the
      smoke scripts.
- [ ] **Baseline re-measured (UAT-2 prerequisite):** `whatsapp-sample.baseline.json`'s
      `baseline_latency_s` ships as a conservative placeholder (4.0s), not a
      measured number. Before UAT-2 is meaningful, measure the prior direct
      integration's transcription latency for the same fixture and update the
      baseline.

> **Container name note:** the gateway compose file does not set
> `container_name`, so the Portainer stack-prefixed name is
> `ai-gateway-dev_gateway` (or `ai-gateway-dev-gateway-1` depending on
> Compose version). Substitute the actual name from
> `ssh vps-ifix-vm 'docker ps | grep gateway'` if the commands below 404.

---

## Current Test

[awaiting human testing — operator runs UAT-1..UAT-4 sequentially below]

## Tests

### 1. UAT-1 — ConverseAI env-var switch + smoke (SC1)

**Pre-conditions:** gateway deployed; `converseai` tenant provisioned + key
minted; `converseai-v4-dev` Portainer stack env vars switched to the gateway
for **both** `apps/api` and `agents/`; Python deps installed.

**Steps:**

```bash
# 1. In Portainer, confirm the converseai-v4-dev stack redeployed after the
#    env-var switch (Portainer UI -> stack -> "Updated" timestamp).

# 2. Run the INT-01 contract smoke against the dev gateway with the converseai
#    tenant key (chat / streaming / tool-calls / embeddings):
python scripts/integration-smoke/smoke-converseai.py \
  --gateway-url https://ai-gateway-dev.converse-ai.app \
  --api-key "$CONVERSEAI_TENANT_KEY" \
  --out /tmp/converseai-report.json
echo "exit=$?"

# 3. Inspect the report:
jq '{exit_implied: .gates.all_passed, gates}' /tmp/converseai-report.json

# 4. Confirm audit_log rows landed under the converseai tenant:
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -c "
  SELECT t.slug, count(*) FROM ai_gateway.audit_log a
    JOIN ai_gateway.tenants t ON t.id = a.tenant_id
   WHERE a.created_at > now() - interval \"10 minutes\" AND t.slug = '"'"'converseai'"'"'
   GROUP BY t.slug;"'
```

**Expected:**

- `smoke-converseai.py` exits `0`.
- `/tmp/converseai-report.json` `gates.all_passed` is `true` — all of
  `chat_ok`, `streaming_flushes`, `tool_call_valid`, `embeddings_ok` pass.
- `audit_log` shows rows under the `converseai` tenant slug from the smoke
  run.
- A chat completion exercised in the ConverseAI v4 UI still works (now via
  the gateway).

**Pass/Fail criteria:**

- PASS: exit 0 + `gates.all_passed == true` + `audit_log` rows under
  `converseai`.
- FAIL: non-zero exit, any gate `false`, or no `audit_log` attribution.

expected: smoke-converseai.py against the dev gateway with the converseai tenant key returns exit 0 and a report whose gates.all_passed is true; chat / SSE streaming / tool-calls / embeddings all pass; audit_log attributes the requests to the converseai tenant.
result: [pending]

---

### 2. UAT-2 — Chat Ifix transcription smoke ±10% (SC2)

**Pre-conditions:** gateway deployed; `chat-ifix` tenant provisioned + key
minted; `campanhas-chatifix` backend env vars switched to the gateway;
`whatsapp-sample.baseline.json` `baseline_latency_s` re-measured against the
real direct integration (see Prerequisites).

**Steps:**

```bash
# 1. (Prerequisite re-measure) If baseline_latency_s is still the 4.0s
#    placeholder, measure the prior direct integration's transcription
#    latency for fixtures/whatsapp-sample.ogg and update
#    fixtures/whatsapp-sample.baseline.json before continuing.

# 2. Run the INT-02 transcription smoke against the dev gateway with the
#    chat-ifix tenant key:
python scripts/integration-smoke/smoke-chat-ifix.py \
  --gateway-url https://ai-gateway-dev.converse-ai.app \
  --api-key "$CHAT_IFIX_TENANT_KEY" \
  --out /tmp/chat-ifix-report.json
echo "exit=$?"

# 3. Inspect the report — both quality AND latency gates:
jq '{gates, comparison}' /tmp/chat-ifix-report.json

# 4. Confirm audit_log rows landed under the chat-ifix tenant:
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -c "
  SELECT t.slug, count(*) FROM ai_gateway.audit_log a
    JOIN ai_gateway.tenants t ON t.id = a.tenant_id
   WHERE a.created_at > now() - interval \"10 minutes\" AND t.slug = '"'"'chat-ifix'"'"'
   GROUP BY t.slug;"'
```

**Expected:**

- `smoke-chat-ifix.py` exits `0`.
- `/tmp/chat-ifix-report.json` `gates.all_passed` is `true` — both
  `quality_within_10pct` (word error rate ≤ 0.10) **and**
  `latency_within_10pct` (latency ratio ≤ 1.10 vs the committed baseline)
  pass.
- `audit_log` shows rows under the `chat-ifix` tenant slug.

**Pass/Fail criteria:**

- PASS: exit 0 + `gates.all_passed == true` (quality AND latency within
  ±10% of the baseline) + `audit_log` rows under `chat-ifix`.
- FAIL: non-zero exit, either the quality or the latency gate `false`, or no
  `audit_log` attribution. Note: if the FAIL is because the baseline was
  still the placeholder, that is a prerequisite miss, not a defect — fix the
  baseline and re-run.

expected: smoke-chat-ifix.py against the dev gateway with the chat-ifix tenant key returns exit 0 and a report whose gates.all_passed is true — transcription quality (WER <= 0.10) AND latency (ratio <= 1.10) both within +/-10% of the re-measured direct-integration baseline; audit_log attributes the request to the chat-ifix tenant.
result: [pending]

---

### 3. UAT-3 — Rollback drill, timed <5 min (SC3)

**Pre-conditions:** UAT-1 + UAT-2 executed — both client apps are currently
routing through the gateway.

**Steps:**

```bash
# 1. Start a stopwatch. Record T0.
T0=$(date -u +"%Y-%m-%dT%H:%M:%SZ"); echo "T0=$T0"

# 2. Execute the gateway/docs/RUNBOOK-CLIENT-INTEGRATION.md ROLLBACK
#    procedure for BOTH apps:
#    a) "To roll back ConverseAI v4" — revert the apps/api + agents/ env vars
#       in the converseai-v4-dev Portainer stack, redeploy, verify.
#    b) "To roll back Chat Ifix" — revert the campanhas-chatifix backend env
#       vars, redeploy, verify.

# 3. Run BOTH verify steps from the runbook — audit_log counts must be 0:
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -c "
  SELECT t.slug, count(*) AS reqs_since_rollback FROM ai_gateway.audit_log a
    JOIN ai_gateway.tenants t ON t.id = a.tenant_id
   WHERE a.created_at > now() - interval \"2 minutes\"
     AND t.slug IN ('"'"'converseai'"'"','"'"'chat-ifix'"'"')
   GROUP BY t.slug;"'
# Expect: no rows (or 0) for BOTH slugs once both apps restarted on direct providers.

# 4. Stop the stopwatch. Record T1 and the elapsed time.
T1=$(date -u +"%Y-%m-%dT%H:%M:%SZ"); echo "T1=$T1"
```

**Expected:**

- Both ConverseAI v4 (both consumers) and Chat Ifix are fully rolled back to
  their direct providers, with both runbook verify steps returning `0`
  `audit_log` rows.
- Elapsed time from T0 (decision) to T1 (both verified) is **under 5
  minutes** — record the measured time.

**Pass/Fail criteria:**

- PASS: both apps verified rolled back AND elapsed time < 5:00.
- FAIL: elapsed time ≥ 5:00, OR either verify step still shows `audit_log`
  rows (a half-switched state — re-check the `agents/` consumer per Symptom
  5 in the runbook).

expected: executing the runbook ROLLBACK procedure for both apps reverts the env vars + redeploys + verifies both ConverseAI v4 consumers and the Chat Ifix backend to their direct providers; both audit_log verify counts are 0; measured wall time from decision to fully-rolled-back is under 5 minutes.
result: [pending]
measured_rollback_time: ___________

---

### 4. UAT-4 — Dashboard per-tenant cross-check (SC4)

**Pre-conditions:** UAT-1 + UAT-2 generated traffic under the `converseai`
and `chat-ifix` tenants (run this **before** UAT-3's rollback, or re-run
UAT-1/UAT-2 traffic first — the dashboard needs recent per-tenant traffic to
populate). This is verification of existing Phase 7 dashboard code, **not**
new code.

**Steps:**

1. Open the Phase 7 dashboard (the Next.js observability dashboard from
   `dashboard/`).
2. Locate the tenant table.
3. Confirm **both** `converseai` and `chat-ifix` appear as **separate**
   tenant rows (by name/slug — the WR-10 name/slug join).
4. Confirm each tenant row has **independent** latency panels (P50 / P95 /
   P99) populated from the UAT-1 / UAT-2 traffic.
5. Confirm each tenant row has an **independent** cost panel populated from
   the same traffic.

**Expected:**

- The dashboard tenant table shows `converseai` and `chat-ifix` as two
  distinct rows.
- Each has its own latency (P50/P95/P99) panel with non-empty values from
  the UAT traffic.
- Each has its own cost panel with non-empty values.
- The two tenants' panels are independent — `converseai`'s LLM/embeddings
  traffic and `chat-ifix`'s transcription traffic do not bleed into each
  other's panels.

**Pass/Fail criteria:**

- PASS: both tenants render as separate rows with independent, populated
  latency + cost panels.
- FAIL: a tenant is missing, the two are merged into one row, or a panel is
  empty despite UAT-1/UAT-2 traffic having run.

expected: the Phase 7 dashboard tenant table shows converseai and chat-ifix as separate rows, each with independent latency (P50/P95/P99) and cost panels populated from the UAT-1/UAT-2 traffic — confirming per-tenant attribution + visibility.
result: [pending]

---

## Summary

total: 4
passed: 0
issues: 0
pending: 4
skipped: 0
blocked: 0

## Sign-off

| UAT   | Scenario                                  | SC  | Result  | Date        | Operator    | Notes |
|-------|-------------------------------------------|-----|---------|-------------|-------------|-------|
| UAT-1 | ConverseAI env-var switch + smoke         | SC1 | pending | ___________ | ___________ |       |
| UAT-2 | Chat Ifix transcription smoke ±10%        | SC2 | pending | ___________ | ___________ |       |
| UAT-3 | Rollback drill, timed <5 min              | SC3 | pending | ___________ | ___________ |       |
| UAT-4 | Dashboard per-tenant cross-check          | SC4 | pending | ___________ | ___________ |       |

**Overall phase status:** `pending`
<!-- one of:
     passed         — all 4 live scenarios PASS
     passed_partial — some scenarios deploy/credential-blocked; autonomous build (08-01..08-03) still green
     human_needed   — a real defect was found; see Gaps below
-->

## Final Sign-off

- [ ] All 4 UATs PASS — Phase 8 client integration ready
- [ ] PARTIAL: ___ of 4 PASS; deferred items: ___ (gateway not deployed / credentials unavailable)
- [ ] FAIL: blocking defect(s) found, see Gaps below

- **Operator:** ___________
- **Date:** ___________

## passed_partial fallback

If the gateway is **not deployed** (the Phase-6-emerg-blocked gate in
Prerequisites) or a credential is genuinely unavailable, mark the affected
scenarios `passed_partial` and note the blocker in the Sign-off table Notes
column. **The autonomous Phase 8 build (08-01..08-03) is already green and is
NOT blocked by this** — `provision-tenants.sh`, `smoke-converseai.py`,
`smoke-chat-ifix.py`, the audio fixture + baseline, and
`RUNBOOK-CLIENT-INTEGRATION.md` all ship and are verified; the only deferred
piece is the live, deployed-gateway run. This is the same deferred-UAT
pattern every prior phase used (03-08, 04-09, 06-11, 07-09).

If a **real defect** is found (not a missing credential or an undeployed
gateway — an actual contract break: a smoke gate fails against a deployed
gateway, a rollback exceeds 5 minutes, a dashboard panel is wrong), set the
overall phase status to `human_needed` and describe the defect precisely in
the Gaps section below so a `/gsd-plan-phase --gaps` pass can close it.

## Gaps

(populate during/after execution — list any UAT that did not pass, whether
it was a deploy/credential block or a real defect, and what follow-up
issue/plan tracks the remediation)
