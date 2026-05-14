---
status: pending
phase: 09-client-integration-sensitive-tenants-telefonia-cobran-as-campanhas-voice-api
source: [09-CONTEXT.md, 09-04-PLAN.md Task 1, ROADMAP Phase 9 SC1-SC4]
started: ___________
updated: ___________
operator: ___________
date_executed: ___________
final_status: pending  # pass | partial | fail
---

# Phase 9 — Human UAT (Live Sensitive-Tenant Client Integration)

This document drives the operator-only UAT for Phase 9 (Client Integration —
Sensitive Tenants: Telefonia/NextBilling, Cobranças, Campanhas, voice-api). The
scenarios exercise the **live**, real-traffic, real-credential integration:
switching each of the four client apps' `base_url`/`api_key` env vars to the
gateway, running the committed sensitive-failover smoke against a
`data_class: sensitive` tenant key, drilling the per-app rollback procedure
(timed <5 min each), and obtaining the **external LGPD legal sign-off** from
Ifix legal before sensitive tenants are declared production-ready.

**The autonomous Phase 9 build is NOT blocked by this UAT.** Plans 09-01..09-03
ship only the gpu-ifix-side artifacts — the extended `provision-tenants.sh` seed
script (the 4 mixed-data-class tenants + the Cobranças/Campanhas quotas),
`smoke-sensitive-failover.py` + its `sensitive-failover-report-schema.json`, the
`RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md` runbook, and the two LGPD docs
(`LGPD-SUBPROCESSORS.md` + `LGPD-REVIEW-CHECKLIST.md`) — and are already green.
This UAT is the live-credential, live-traffic, external-sign-off verification
that cannot run autonomously, mirroring the 03-08 / 04-09 / 06-11 / 07-09 /
08-04 deferred-UAT pattern.

**Double gate — the live UAT is gated twice.** (1) The gateway itself is **not
deployed yet** — build-gateway is currently blocked on Phase 6 emergency-pod
integration tests (a separate debug session). (2) The **LGPD legal sign-off is
an external gate** — the operator obtains it from Ifix legal before activating
the sensitive tenants (Telefonia, Cobranças) in production. So the live UAT is
gated on the gateway being deployed AND the operator-run env-var switch in each
of the 4 client repos AND the external legal sign-off. If any gate is unmet,
the affected scenarios are `passed_partial` — see the fallback note at the
bottom.

The companion runbook is `gateway/docs/RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md`
— read its **Mental Model** + **ROLLBACK procedure** sections before running any
UAT below. UAT-4 drills that runbook's four ROLLBACK procedures directly. The
LGPD checklist is `gateway/docs/LGPD-REVIEW-CHECKLIST.md` — its signed `## Sign-off`
table is the artifact the `## Final Sign-off` section below attaches.

---

## Prerequisites

Verify all rows are satisfied **before** starting UAT-1. If a row cannot be
satisfied (most likely the gateway-not-deployed gate or the pending LGPD
signature), the affected UAT scenarios are `passed_partial` — see the fallback
note at the bottom.

- [ ] **Gateway deployed:** the gateway is deployed to the `ai-gateway-dev`
      Portainer stack. **This is currently blocked** on Phase 6 emergency-pod
      integration tests (a separate debug session) — per 09-CONTEXT.md
      `## Deferred Ideas`. If still blocked, every UAT below is `passed_partial`.
- [ ] **Tenants provisioned + keys captured:** run
      `scripts/integration-smoke/provision-tenants.sh --mint-keys` **once**
      against the gateway DB; capture the five raw keys it prints to stdout —
      the `telefonia` tenant key, the `cobrancas` tenant key, the `campanhas`
      tenant key, the `voice-api` tenant key, and the `phase-9-sensitive` admin
      key. The script is idempotent for `tenant create` + `tenant set-quota`;
      `--mint-keys` mints fresh key rows, so run it once. `telefonia` +
      `cobrancas` keys are minted `data_class: sensitive`; `campanhas` +
      `voice-api` are `normal`.
- [ ] **Telefonia env vars switched:** the `telefonia` tenant key + the gateway
      `base_url` set in the `fallback-register-ramais-nextbilling` deploy config
      for its call-audio Whisper/STT path. See
      `RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md` Required Env Vars table for the
      var names (operator must confirm them against the
      `fallback-register-ramais-nextbilling` repo).
- [ ] **Cobranças env vars switched:** the `cobrancas` tenant key + the gateway
      `base_url` set in the `cobrancas-api` Portainer stack for its LLM
      personalization + embedding path.
- [ ] **Campanhas env vars switched:** the `campanhas` tenant key + the gateway
      `base_url` set in the `campanhas-chatifix` backend deploy config for its
      LLM personalization + embedding path (NOT the Phase-8 `chat-ifix`
      transcription config — same repo, different tenant).
- [ ] **voice-api env vars switched:** the `voice-api` tenant key + the gateway
      `base_url` set in the `voice-api` deploy config for its **LLM
      script-generation** path only — the local-CPU TTS path is NOT pointed at
      the gateway.
- [ ] **LGPD checklist worked through + submitted:** `gateway/docs/LGPD-REVIEW-CHECKLIST.md`
      worked through item by item and submitted to Ifix legal for the `## Sign-off`
      table signature. The signature is captured in the `## Final Sign-off`
      section of this sheet — it is a BLOCKING external gate for sensitive-tenant
      production activation.
- [ ] **Python deps installed:** `pip install -r scripts/integration-smoke/requirements.txt`
      (`httpx`, `numpy`, `structlog`, `jsonschema`, **`psycopg[binary]`**) on the
      box that runs the smoke scripts.
- [ ] **`AI_GATEWAY_PG_DSN` available:** the audit-DB read DSN exported as
      `AI_GATEWAY_PG_DSN` (or passed via `--pg-dsn`) — `smoke-sensitive-failover.py`
      reads `ai_gateway.audit_log` + `audit_log_content` directly for its audit
      gates.

> **Container name note:** the gateway compose file does not set
> `container_name`, so the Portainer stack-prefixed name is
> `ai-gateway-dev_gateway` (or `ai-gateway-dev-gateway-1` depending on Compose
> version). Substitute the actual name from
> `ssh vps-ifix-vm 'docker ps | grep gateway'` if the commands below 404.

---

## Current Test

[awaiting human testing — operator runs UAT-1..UAT-4 sequentially below]

## Tests

### 1. UAT-1 — Telefonia sensitive-failover smoke (SC1)

**Pre-conditions:** gateway deployed; `telefonia` tenant provisioned + key
minted `data_class: sensitive`; `fallback-register-ramais-nextbilling` deploy
config env vars switched to the gateway for the call-audio transcription path;
Python deps installed (incl. `psycopg`); `AI_GATEWAY_PG_DSN` available.

**Steps:**

```bash
# 1. Confirm the fallback-register-ramais-nextbilling app redeployed after the
#    env-var switch (its deploy flow — webhook / Portainer per CLAUDE.md).

# 2. Trip the tier-0 local LLM breaker per the smoke's operator pre-step. The
#    smoke (--induce-failure-via operator-prestep, default) prints the exact
#    pre-step; in short: kill the local llama-server or point the local-llm
#    upstream at a dead host so the breaker opens. The smoke then polls
#    GET /v1/health/upstreams until local-llm shows state=open before
#    evaluating any gate (a HARD pre-condition — no false GREEN).

# 3. Run the RES-08 sensitive-failover smoke against the dev gateway with the
#    telefonia SENSITIVE tenant key:
python scripts/integration-smoke/smoke-sensitive-failover.py \
  --gateway-url https://ai-gateway-dev.converse-ai.app \
  --api-key "$TELEFONIA_TENANT_KEY" \
  --pg-dsn "$AI_GATEWAY_PG_DSN" \
  --out /tmp/sensitive-failover-report.json
echo "exit=$?"

# 4. Inspect the report — the three RES-08 gates:
jq '{all_passed: .gates.all_passed, gates, checks}' /tmp/sensitive-failover-report.json

# 5. Confirm the sensitive-block rows landed under the telefonia tenant:
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -c "
  SELECT t.slug, a.upstream, a.status_code, count(*) FROM ai_gateway.audit_log a
    JOIN ai_gateway.tenants t ON t.id = a.tenant_id
   WHERE a.created_at > now() - interval \"10 minutes\"
     AND t.slug = '"'"'telefonia'"'"' AND a.upstream = '"'"'blocked_sensitive'"'"'
   GROUP BY t.slug, a.upstream, a.status_code;"'

# 6. Restore the local upstream (un-trip the breaker) so the dev gateway is
#    healthy again before UAT-2.
```

**Expected:**

- `smoke-sensitive-failover.py` exits `0`.
- `/tmp/sensitive-failover-report.json` `gates.all_passed` is `true` — all
  three RES-08 gates pass:
  - **fail_closed:** sensitive `POST /v1/chat/completions` returns `503` +
    body `upstream_unavailable_for_sensitive_tenant` + `Retry-After: 30`.
  - **never_external:** the request's `audit_log` row has
    `upstream = 'blocked_sensitive'` — never proxied to OpenAI/OpenRouter.
  - **audit_decision:** an `audit_log` row exists for the request_id AND
    `audit_log_content` has **zero** rows for it (D-B2 — sensitive content
    never persisted).
- The `audit_log` query in step 5 shows `telefonia` rows with
  `upstream='blocked_sensitive'` + `status_code=503`.

**Pass/Fail criteria:**

- PASS: exit 0 + `gates.all_passed == true` + `blocked_sensitive` rows under
  `telefonia`. This is the load-bearing RES-08 proof — a sensitive tenant's
  request **never** reaches an external provider.
- FAIL: non-zero exit, any gate `false`, or the request was served / failed
  over with a non-`blocked_sensitive` upstream. Note: if the FAIL is because
  the breaker never opened (the pre-step pre-condition), that is a prerequisite
  miss, not a defect — re-do step 2 and re-run.

expected: smoke-sensitive-failover.py against the dev gateway with the telefonia sensitive tenant key returns exit 0 and a report whose gates.all_passed is true — fail_closed (503 + upstream_unavailable_for_sensitive_tenant + Retry-After: 30), never_external (audit_log upstream='blocked_sensitive'), and audit_decision (row found + zero audit_log_content rows) all pass; the request never reaches OpenAI/OpenRouter.
result: [pending]

---

### 2. UAT-2 — Cobranças + Campanhas quotas + cost-per-request (SC2)

**Pre-conditions:** gateway deployed; `cobrancas` (sensitive) + `campanhas`
(normal) tenants provisioned + keys minted + per-tenant quotas applied by the
seed script; `cobrancas-api` Portainer stack + `campanhas-chatifix` backend env
vars switched to the gateway; Phase 7 observability dashboard reachable.

**Steps:**

```bash
# 1. Confirm both cobrancas-api and campanhas-chatifix redeployed after the
#    env-var switch.

# 2. Send LLM-personalization + embedding requests through the gateway for
#    BOTH tenants — exercise each app's real personalization/embedding path
#    (or curl /v1/chat/completions + /v1/embeddings with each tenant key).
for KEY in "$COBRANCAS_TENANT_KEY" "$CAMPANHAS_TENANT_KEY"; do
  curl -sS -X POST https://ai-gateway-dev.converse-ai.app/v1/chat/completions \
    -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
    -d '{"model":"qwen","messages":[{"role":"user","content":"PING"}],"max_tokens":5}'
  curl -sS -X POST https://ai-gateway-dev.converse-ai.app/v1/embeddings \
    -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
    -d '{"model":"bge-m3","input":"quota + cost smoke"}'
done

# 3. Confirm the per-tenant quotas (set by provision-tenants.sh — cobrancas
#    2M daily-tokens / 120 rpm, campanhas 5M daily-tokens / 300 rpm) are
#    enforced. Drive a tenant past its rpm ceiling and confirm it 429s:
docker exec ai-gateway-dev_gateway /gatewayctl tenant set-quota --slug cobrancas   # (no value = show)
docker exec ai-gateway-dev_gateway /gatewayctl tenant set-quota --slug campanhas   # (no value = show)
# (then a short burst loop per tenant — expect 429 once the per-tenant rpm
#  ceiling is crossed; see RUNBOOK-QUOTAS-BILLING.md for the inspection detail.)

# 4. Confirm cost-per-request is reported per tenant — open the Phase 7
#    observability dashboard (or hit /admin/usage) and confirm cobrancas and
#    campanhas each show an independent cost panel populated from the step-2
#    traffic.

# 5. Cobranças is data_class=sensitive — exercise its never-external guarantee
#    too: re-run UAT-1's smoke with the cobrancas key:
python scripts/integration-smoke/smoke-sensitive-failover.py \
  --gateway-url https://ai-gateway-dev.converse-ai.app \
  --api-key "$COBRANCAS_TENANT_KEY" \
  --pg-dsn "$AI_GATEWAY_PG_DSN" \
  --out /tmp/sensitive-failover-cobrancas.json
echo "exit=$?"   # expect 0 + gates.all_passed == true
```

**Expected:**

- Both `cobrancas` and `campanhas` send LLM-personalization + embedding
  requests successfully through the gateway.
- The per-tenant quotas set by `provision-tenants.sh` are enforced — a tenant
  driven past its rpm ceiling gets `429` (capacity signal, not a defect).
- The Phase 7 dashboard / `/admin/usage` reports cost-per-request for
  `cobrancas` and `campanhas` as **independent**, populated panels.
- The cobrancas sensitive re-run of `smoke-sensitive-failover.py` exits `0`
  with `gates.all_passed == true` — cobrancas' never-external guarantee holds.

**Pass/Fail criteria:**

- PASS: per-tenant quotas enforced for both, cost reported per tenant, AND the
  cobrancas sensitive smoke passes.
- FAIL: a quota is not enforced, cost is not reported per tenant (or the two
  tenants' cost bleeds into one panel), or the cobrancas sensitive smoke fails
  any gate.

expected: cobrancas + campanhas send LLM-personalization + embedding requests through the gateway with their per-tenant quotas (set by provision-tenants.sh) enforced; the Phase 7 dashboard / /admin/usage reports cost-per-request per tenant as independent panels; the cobrancas sensitive key re-run of smoke-sensitive-failover.py exits 0 with gates.all_passed true.
result: [pending]

---

### 3. UAT-3 — voice-api LLM-via-gateway, TTS stays local (SC3)

**Pre-conditions:** gateway deployed; `voice-api` (normal) tenant provisioned +
key minted; `voice-api` deploy config env vars switched to the gateway for the
**LLM script-generation path only** — the local-CPU TTS path is NOT pointed at
the gateway.

**Steps:**

```bash
# 1. Confirm the voice-api app redeployed after the env-var switch.

# 2. Trigger an LLM script-generation call through voice-api (its real
#    script-generation path, or curl /v1/chat/completions with the voice-api
#    tenant key):
curl -sS -X POST https://ai-gateway-dev.converse-ai.app/v1/chat/completions \
  -H "Authorization: Bearer $VOICE_API_TENANT_KEY" -H "Content-Type: application/json" \
  -d '{"model":"qwen","messages":[{"role":"user","content":"Generate a short call script."}],"max_tokens":64}'

# 3. Confirm the LLM script-generation request landed under the voice-api
#    tenant in audit_log:
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -c "
  SELECT t.slug, a.route, count(*) FROM ai_gateway.audit_log a
    JOIN ai_gateway.tenants t ON t.id = a.tenant_id
   WHERE a.created_at > now() - interval \"10 minutes\" AND t.slug = '"'"'voice-api'"'"'
   GROUP BY t.slug, a.route;"'

# 4. Trigger a voice-api TTS call and confirm it runs on the local CPU and does
#    NOT route through the gateway — no /v1/audio/speech (or TTS) rows for the
#    voice-api tenant should appear in audit_log from the TTS path.
```

**Expected:**

- The LLM script-generation call returns a completion **through the gateway** —
  an `audit_log` row appears under the `voice-api` tenant for the chat route.
- voice-api TTS still runs on the **local CPU** — it does NOT route through the
  gateway; no TTS-path rows under `voice-api` in `audit_log`.

**Pass/Fail criteria:**

- PASS: LLM script generation works via the gateway (audit_log row under
  `voice-api`) AND TTS is unaffected (still local CPU, no gateway rows).
- FAIL: the LLM script-generation call does not route through the gateway, OR
  the TTS path was pointed at the gateway (TTS rows appear under `voice-api`).

expected: voice-api's LLM script-generation call returns through the gateway with an audit_log row under the voice-api tenant; voice-api TTS continues to run on the local CPU and is NOT routed through the gateway.
result: [pending]

---

### 4. UAT-4 — Per-app rollback drill, timed <5 min each (SC4)

**Pre-conditions:** UAT-1..UAT-3 executed — all four client apps are currently
routing through the gateway (for their in-scope paths).

**Steps:**

```bash
# For EACH of the 4 apps, drill the corresponding "### To roll back" procedure
# in gateway/docs/RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md, timing each.

# --- App 1: Telefonia / NextBilling ---
T0=$(date -u +"%Y-%m-%dT%H:%M:%SZ"); echo "telefonia T0=$T0"
# Execute "### To roll back Telefonia / NextBilling": revert the env vars in
# the fallback-register-ramais-nextbilling deploy config, redeploy, then verify:
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -c "
  SELECT count(*) AS telefonia_reqs_since_rollback FROM ai_gateway.audit_log a
    JOIN ai_gateway.tenants t ON t.id = a.tenant_id
   WHERE t.slug = '"'"'telefonia'"'"' AND a.created_at > now() - interval \"2 minutes\";"'
T1=$(date -u +"%Y-%m-%dT%H:%M:%SZ"); echo "telefonia T1=$T1"   # must be <5 min from T0, count must be 0

# --- App 2: Cobranças ---
# Execute "### To roll back Cobranças": revert the cobrancas-api Portainer stack
# env vars, Update the stack, then verify cobrancas count -> 0. Time it.

# --- App 3: Campanhas ---
# Execute "### To roll back Campanhas": revert the campanhas-chatifix backend
# env vars (the Campanhas LLM config, NOT the Phase-8 chat-ifix config),
# redeploy, then verify campanhas count -> 0. Time it.

# --- App 4: voice-api ---
# Execute "### To roll back voice-api": revert the voice-api LLM
# script-generation env vars (the TTS path has no gateway vars), redeploy,
# then verify voice-api count -> 0. Time it.
```

**Expected:**

- Each of the 4 apps is fully rolled back to its direct provider — the runbook
  `psql` `audit_log` row-count verify step reaches `0` for each tenant slug
  (a non-zero count = a half-switched app, the procedure is not done).
- The measured wall time per app, from the decision to roll back to the
  fully-verified rolled-back state, is **under 5 minutes** — record the
  measured time for each of the 4 apps.

**Pass/Fail criteria:**

- PASS: all 4 apps verified rolled back (each verify count `0`) AND each app's
  measured time < 5:00.
- FAIL: any app's elapsed time ≥ 5:00, OR any verify step still shows
  `audit_log` rows (a half-switched state — re-check the env-var revert per the
  runbook's per-app procedure).

expected: drilling the four "### To roll back" procedures in RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md reverts each app's env vars + redeploys + verifies the audit_log row-count reaches 0 for telefonia / cobrancas / campanhas / voice-api; each app's measured wall time is under 5 minutes.
result: [pending]
measured_rollback_time_telefonia: ___________
measured_rollback_time_cobrancas: ___________
measured_rollback_time_campanhas: ___________
measured_rollback_time_voice_api: ___________

---

## Summary

total: 4
passed: 0
issues: 0
pending: 4
skipped: 0
blocked: 0

## Sign-off

| UAT   | Scenario                                        | SC  | Result  | Date        | Operator    | Notes |
|-------|-------------------------------------------------|-----|---------|-------------|-------------|-------|
| UAT-1 | Telefonia sensitive-failover smoke              | SC1 | pending | ___________ | ___________ |       |
| UAT-2 | Cobranças + Campanhas quotas + cost-per-request | SC2 | pending | ___________ | ___________ |       |
| UAT-3 | voice-api LLM-via-gateway, TTS stays local      | SC3 | pending | ___________ | ___________ |       |
| UAT-4 | Per-app rollback drill, timed <5 min each       | SC4 | pending | ___________ | ___________ |       |

**Overall phase status:** `pending`
<!-- one of:
     passed         — all 4 live scenarios PASS AND the LGPD legal sign-off is attached
     passed_partial — some scenarios deploy/credential/signature-blocked; autonomous build (09-01..09-03) still green
     human_needed   — a real defect was found; see Gaps below
-->

## Final Sign-off — LGPD legal review (BLOCKING, external gate)

This is a **BLOCKING external gate**. The operator works through
`gateway/docs/LGPD-REVIEW-CHECKLIST.md` item by item, submits it to Ifix legal,
and Ifix legal signs that doc's `## Sign-off` table. The signed
`LGPD-REVIEW-CHECKLIST.md` is then **attached as evidence** to this section.

**Sensitive tenants (`telefonia`, `cobrancas`) MUST NOT be activated in
production until this signature exists.** This is the ROADMAP Phase 9 SC4 /
PRD-05 "LGPD review documented before sensitive tenants go live" gate — a
written-but-unsigned checklist does not satisfy it.

| Reviewer | Role | Date | Signature / approval reference | Notes |
|----------|------|------|--------------------------------|-------|
|          |      |      |                                |       |

- [ ] `gateway/docs/LGPD-REVIEW-CHECKLIST.md` worked through, all checklist
      items satisfied, and its `## Sign-off` table signed by Ifix legal.
- [ ] The signed `LGPD-REVIEW-CHECKLIST.md` is attached/linked here as the
      attributable evidence.
- [ ] Sensitive-tenant production activation is authorized **only** after the
      row above is signed.

- [ ] All 4 UATs PASS AND the LGPD legal sign-off is attached — Phase 9
      sensitive-tenant client integration ready
- [ ] PARTIAL: ___ of 4 PASS; deferred items: ___ (gateway not deployed /
      credentials unavailable / LGPD signature pending)
- [ ] FAIL: blocking defect(s) found, see Gaps below

- **Operator:** ___________
- **Date:** ___________

## passed_partial fallback

If the gateway is **not deployed** (the Phase-6-emerg-blocked gate in
Prerequisites), a credential is genuinely unavailable, **or the LGPD legal
sign-off is not yet obtained**, mark the affected scenarios `passed_partial`
and note the blocker in the Sign-off table Notes column. **The autonomous
Phase 9 build (09-01..09-03) is already green and is NOT blocked by this** — the
extended `provision-tenants.sh`, `smoke-sensitive-failover.py` + its schema,
`RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md`, `LGPD-SUBPROCESSORS.md`, and
`LGPD-REVIEW-CHECKLIST.md` all ship and are verified; the only deferred pieces
are the live deployed-gateway run + the external legal signature. This is the
same deferred-UAT pattern every prior phase used (03-08, 04-09, 06-11, 07-09,
08-04).

If a **real defect** is found (not a missing credential, an undeployed gateway,
or a pending signature — an actual contract break: the sensitive-failover smoke
fails a gate against a deployed gateway, a quota is not enforced, voice-api TTS
routes through the gateway, a rollback exceeds 5 minutes), set the overall phase
status to `human_needed` and describe the defect precisely in the Gaps section
below so a `/gsd-plan-phase --gaps` pass can close it.

## Gaps

(populate during/after execution — list any UAT that did not pass, whether it
was a deploy/credential/signature block or a real defect, and what follow-up
issue/plan tracks the remediation)
