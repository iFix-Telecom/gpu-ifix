# Client Integration Runbook — ConverseAI v4 + Chat Ifix

**Phase 8 (`ifix-ai-gateway`) client-integration layer.** Read this when:

- A client app (ConverseAI v4 `apps/api` or `agents/`, or the Chat Ifix
  backend in `campanhas-chatifix`) is being pointed at the gateway for the
  first time.
- A client app that already runs through the gateway is misbehaving (401s,
  429s, broken streaming, regressed transcription latency/quality).
- The integration needs to be **rolled back** — reverting a client app to
  call the LLM/STT provider directly, bypassing the gateway.
- Post-incident review of a client-integration event.

This document is the operator's first-line reference for the gpu-ifix-side
of the integration. The actual `base_url`/`api_key` env-var switches happen
**in the client repos** (`converseai-v4`, `campanhas-chatifix` — sibling
repos, not part of gpu-ifix) and are operator actions, validated by
`08-HUMAN-UAT.md`.

> **Deployment gate:** as of Phase 8 the gateway is not yet deployed to the
> `ai-gateway-dev` Portainer stack (build-gateway is blocked on Phase 6
> emergency-pod integration tests — a separate debug session). The live
> integration + this runbook's procedures are exercised only once the
> gateway is deployed.

---

## Mental Model (30 seconds)

OpenAI-compatibility means client integration is an **env-var change, not a
code rewrite**. Each client app moves from calling the provider directly to
calling the gateway by flipping two values:

| Knob       | Direct (today)                   | Gateway (after switch)                          |
|------------|----------------------------------|-------------------------------------------------|
| `base_url` | `https://api.openai.com/v1` etc. | `https://ai-gateway-dev.converse-ai.app/v1`     |
| `api_key`  | the direct provider key          | the per-**tenant** key minted by `gatewayctl`   |

```
                          ┌──────────────────────────────────────┐
ConverseAI v4             │              ifix-ai-gateway          │
┌───────────────┐         │   /v1/chat/completions  (LLM)         │   ┌──────────────┐
│ apps/api      │──┐      │   /v1/embeddings        (EMBED)       │──▶│ local GPU    │
│ (Elysia,      │  │ tenant│   /v1/audio/transcriptions (STT)     │   │ (Qwen/Whisper│
│  OpenAI SDK)  │  │ key:  │                                       │   │  /BGE-M3)    │
├───────────────┤  ├──────▶│   auth: Authorization: Bearer <key>   │   └──────┬───────┘
│ agents/       │  │converseai  per-tenant attribution + quotas    │          │ failover
│ (Python,      │──┘      │   audit_log row per request           │   ┌──────▼───────┐
│  LangChain)   │         │                                       │──▶│ OpenRouter / │
└───────────────┘         │                                       │   │ OpenAI       │
                          │                                       │   └──────────────┘
campanhas-chatifix        │                                       │
┌───────────────┐  tenant │                                       │
│ Chat Ifix     │  key:   │                                       │
│ backend       │──chat-ifix▶                                     │
│ (Whisper STT) │         └──────────────────────────────────────┘
└───────────────┘
```

**The two-consumers-one-tenant model.** ConverseAI v4 has **two** consumers
of the gateway:

- `apps/api` — the Elysia REST API, using the OpenAI SDK (chat / streaming /
  tool-calls / embeddings).
- `agents/` — the Python LangChain agent service.

Both share the **single `converseai` tenant** (same org, shared cost
attribution). When you switch ConverseAI v4 you must switch **both**
consumers — leaving one on the direct provider is a half-switched state
(see ROLLBACK + Symptom 5).

Chat Ifix's WhatsApp-audio Whisper transcription lives in the
**`campanhas-chatifix`** sibling repo's backend, on its own **`chat-ifix`
tenant**.

Two tenants total for Phase 8, both `data_class = normal`. Sensitive-class
tenants (Telefonia, Cobranças, Campanhas, voice-api) are Phase 9.

The two tenants + their API keys are seeded idempotently by
`scripts/integration-smoke/provision-tenants.sh` (wraps `gatewayctl tenant
create` / `key create` / `admin-key create`). The raw keys are surfaced to
stdout exactly once and pasted by the operator into each client app's
Portainer/deploy env vars — they are never committed.

---

## Quick Diagnosis (~2 minutes)

Run these in order; stop at the first one that points to a clear cause.
Substitute the real gateway host and the real per-tenant keys.

```bash
# 1. Gateway chat endpoint reachable with the converseai tenant key?
curl -sS -X POST https://ai-gateway-dev.converse-ai.app/v1/chat/completions \
  -H "Authorization: Bearer $CONVERSEAI_TENANT_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen","messages":[{"role":"user","content":"PING"}],"max_tokens":5}'
# 200 + a completion  → auth + LLM path OK
# 401                 → wrong/unset api key (Symptom 1)
# 429                 → tenant quota / rate-limit (Symptom 2)

# 2. Gateway transcription endpoint reachable with the chat-ifix tenant key?
curl -sS -X POST https://ai-gateway-dev.converse-ai.app/v1/audio/transcriptions \
  -H "Authorization: Bearer $CHAT_IFIX_TENANT_KEY" \
  -F model=whisper \
  -F file=@scripts/integration-smoke/fixtures/whatsapp-sample.ogg
# 200 + {"text": "..."} → auth + STT path OK

# 3. Fuller contract check — run the committed smoke scripts (they assert on
#    chat / streaming / tool-calls / embeddings and on transcription ±10%):
python scripts/integration-smoke/smoke-converseai.py \
  --gateway-url https://ai-gateway-dev.converse-ai.app \
  --api-key "$CONVERSEAI_TENANT_KEY" --out /tmp/converseai-report.json
python scripts/integration-smoke/smoke-chat-ifix.py \
  --gateway-url https://ai-gateway-dev.converse-ai.app \
  --api-key "$CHAT_IFIX_TENANT_KEY" --out /tmp/chat-ifix-report.json
# exit 0 + gates.all_passed == true  → the integration contract holds

# 4. Audit log — are requests landing under the right tenant slug?
psql "$AI_GATEWAY_PG_DSN" -c "
  SELECT created_at, tenant_id, route, upstream, status_code, error_code, latency_ms
    FROM ai_gateway.audit_log
   WHERE created_at > now() - interval '10 minutes'
   ORDER BY created_at DESC
   LIMIT 30;"

# 5. Per-tenant row counts in the same window — confirms attribution works:
psql "$AI_GATEWAY_PG_DSN" -c "
  SELECT t.slug, count(*) AS reqs_10min
    FROM ai_gateway.audit_log a
    JOIN ai_gateway.tenants t ON t.id = a.tenant_id
   WHERE a.created_at > now() - interval '10 minutes'
   GROUP BY t.slug;"
# Expect rows for 'converseai' and/or 'chat-ifix' once their apps have traffic.

# 6. Phase 7 dashboard — confirm both tenants render as separate rows with
#    independent latency (P50/P95/P99) + cost panels:
#    open the dashboard, check the tenant-table shows 'converseai' + 'chat-ifix'.
```

---

## Incident Response by Symptom

### Symptom 1 — Client app gets `401` from the gateway

**Likely cause:** the client app's `api_key` env var is unset, is still the
direct-provider key, or is a tenant key that was revoked.

**Diagnose:**

1. Confirm the env var is set in the client app's deploy config — for
   ConverseAI v4, the Portainer stack `converseai-v4-dev` env vars; for Chat
   Ifix, the `campanhas-chatifix` deploy config.
2. Curl the gateway with the same key (Quick Diagnosis step 1). A 401 here
   confirms it is a key problem, not a client-code problem.
3. Check the key exists and is active:
   ```bash
   docker exec ifix-ai-gateway /gatewayctl key list --tenant converseai
   docker exec ifix-ai-gateway /gatewayctl key list --tenant chat-ifix
   ```
4. Check the gateway redacts the header in logs (`Authorization` is on the
   redaction list per `gateway/README.md`) — never paste a raw key into a
   ticket.

**Mitigate:**

- If the key is wrong/unset: set the correct per-tenant key in the client
  app's deploy env var and redeploy (see ROLLBACK below for the per-app
  redeploy command — the same redeploy mechanism applies).
- If the key was revoked or lost: re-mint it with
  `scripts/integration-smoke/provision-tenants.sh --mint-keys` (it is
  idempotent for `tenant create`; `--mint-keys` mints a fresh key row), then
  paste the new raw key into the client app env var.

**Recovery:** re-run Quick Diagnosis step 1 — a `200` confirms auth is
restored. Confirm an `audit_log` row appears under the expected tenant slug
(step 5).

### Symptom 2 — Client app gets `429` from the gateway

**Likely cause:** the tenant hit its per-tenant quota or rate-limit (Phase 4
quota/rate-limit layer), or the gateway is shedding load.

**Diagnose:**

1. Check the tenant's current mode + quota:
   ```bash
   docker exec ifix-ai-gateway /gatewayctl tenant set-quota --slug converseai   # (no value = show)
   ```
   (See `gateway/docs/RUNBOOK-QUOTAS-BILLING.md` for the full quota-inspection
   procedure.)
2. Check `audit_log` for `status_code = 429` rows and their `error_code`.

**Mitigate:** this is a capacity signal, not a health signal — the client
app should back off per its own retry policy. If the quota is genuinely too
low for the workload, raise it via `gatewayctl tenant set-quota` (see
`RUNBOOK-QUOTAS-BILLING.md`). A `429` does **not** mean the integration is
broken — do not roll back for a `429`.

### Symptom 3 — Streaming responses arrive non-incrementally

**Likely cause:** an intermediary (the gateway's reverse proxy, or a proxy
in front of the client app) is buffering the SSE stream instead of flushing
each chunk.

**Diagnose:**

1. Run `smoke-converseai.py` (Quick Diagnosis step 3) — its `chat_stream`
   gate (`streaming_flushes`) is `true` only when ≥2 SSE chunks arrived
   incrementally. A `false` there is direct evidence of buffering.
2. Confirm the gateway's reverse proxy `FlushInterval` is `-1` (flush
   immediately) for the chat route — this is the Phase 2 contract.

**Mitigate:** if the gateway is buffering, the `FlushInterval` config for
the chat route is wrong — fix it in the gateway config and redeploy the
gateway. If a proxy in front of the client app is buffering, disable
response buffering for the streaming route on that proxy.

### Symptom 4 — Transcription latency or quality regressed (Chat Ifix)

**Likely cause:** the gateway's STT path (local Speaches/Whisper, or the
OpenAI Whisper fallback) is slower or lower-quality than the prior direct
integration, or the gateway is in failover for the STT role.

**Diagnose:**

1. Run `smoke-chat-ifix.py` (Quick Diagnosis step 3). It posts the committed
   `whatsapp-sample.ogg` fixture and gates **both** quality
   (`quality_within_10pct` — word error rate ≤ 0.10) **and** latency
   (`latency_within_10pct` — latency ratio ≤ 1.10) against the committed
   baseline in `fixtures/whatsapp-sample.baseline.json`.
2. If `latency_within_10pct` fails: check whether the STT upstream is in
   failover (`curl .../v1/health/upstreams | jq '.upstreams["local-stt"]'` —
   an `open` state means traffic is on the slower OpenAI Whisper fallback;
   see `RUNBOOK-FAILOVER.md` Symptom 1).
3. If `quality_within_10pct` fails: confirm the baseline transcript is still
   valid for the fixture, and that the STT model alias still resolves to
   `Systran/faster-whisper-large-v3`.

> **Baseline caveat:** `whatsapp-sample.baseline.json`'s `baseline_latency_s`
> ships as a conservative placeholder, **not** a measured direct-integration
> number. Before the `latency_within_10pct` gate is meaningful, the operator
> must re-measure it against the real direct integration and update the
> baseline — this is an `08-HUMAN-UAT.md` prerequisite (UAT-2).

**Mitigate:** if the regression is a failover state, recover the STT tier-0
per `RUNBOOK-FAILOVER.md`. If it is a genuine gateway-path regression that
cannot be quickly fixed, **roll back Chat Ifix** (see ROLLBACK below) and
file the regression for a follow-up.

### Symptom 5 — A client app is in a half-switched state

**Likely cause:** ConverseAI v4 has two consumers (`apps/api` + `agents/`);
only one was switched to the gateway. Traffic is split — part through the
gateway, part direct — which breaks cost attribution and makes incidents
hard to diagnose.

**Diagnose:** check `audit_log` per-tenant counts (Quick Diagnosis step 5).
If the `converseai` tenant shows chat traffic but no embeddings (or vice
versa), one consumer is still on the direct provider.

**Mitigate:** decide one direction and make it consistent — either finish
the switch for **both** consumers, or roll back **both** (the ROLLBACK
procedure below is explicitly per-app **and** per-consumer for exactly this
reason). Never leave ConverseAI v4 half-switched.

---

## ROLLBACK procedure

> **This is the load-bearing section of this runbook (SC3).** Target: under
> **5 minutes** from the decision to roll back to a fully-rolled-back,
> verified state. This procedure MUST be **drilled** in `08-HUMAN-UAT.md`
> (UAT-3, timed) — a written-but-never-run rollback does not satisfy SC3.

Roll back when a client app's gateway integration is causing a
production-affecting regression that cannot be fixed in-place faster than
the rollback itself (broken streaming, sustained transcription regression, a
gateway outage with no fast recovery). Do **not** roll back for a `429`
(capacity) or a transient `401` (fix the key instead).

Each app has its own numbered procedure: env-var diff → redeploy → verify.

### To roll back ConverseAI v4

ConverseAI v4 has **two** consumers — you must revert **both**.

1. **Revert the env vars** in the Portainer stack `converseai-v4-dev`
   (Portainer UI → stack → Editor → Environment variables). Flip each from
   the gateway value back to the direct-provider value:

   | Env var                  | Consumer    | Gateway value (current)                       | Direct value (rollback target)      |
   |--------------------------|-------------|-----------------------------------------------|-------------------------------------|
   | `OPENAI_BASE_URL`        | `apps/api`  | `https://ai-gateway-dev.converse-ai.app/v1`   | `https://api.openai.com/v1`         |
   | `OPENAI_API_KEY`         | `apps/api`  | the `converseai` tenant key                   | the direct OpenAI key               |
   | `ANTHROPIC_BASE_URL`     | `apps/api`  | `https://ai-gateway-dev.converse-ai.app/v1`   | `https://api.anthropic.com`         |
   | `ANTHROPIC_API_KEY`      | `apps/api`  | the `converseai` tenant key                   | the direct Anthropic key            |
   | `OPENAI_BASE_URL` (agents)| `agents/`  | `https://ai-gateway-dev.converse-ai.app/v1`   | `https://api.openai.com/v1`         |
   | `OPENAI_API_KEY` (agents)| `agents/`   | the `converseai` tenant key                   | the direct OpenAI key               |

   > **`> CONFIRM:`** the exact env-var **names** ConverseAI v4 reads are
   > the OpenAI-SDK / LangChain standard names (`OPENAI_BASE_URL`,
   > `OPENAI_API_KEY`, `ANTHROPIC_BASE_URL`, `ANTHROPIC_API_KEY`). The
   > operator MUST confirm them against the `converseai-v4` repo
   > (`apps/api` Elysia + OpenAI SDK config, and `agents/` LangChain
   > `base_url` config) before drilling this procedure — substitute the
   > real names here once confirmed. The `agents/` LangChain client may
   > use a distinct env var name for its base URL; verify both consumers.

2. **Redeploy.** In Portainer, the `converseai-v4-dev` stack redeploys via
   its GitHub webhook on `develop`, but an env-var-only change is applied by
   re-pulling/recreating the stack: Portainer UI → stack `converseai-v4-dev`
   → **Update the stack** (re-create + pull image). Containers restart and
   read the reverted env vars at boot.

3. **Verify** ConverseAI v4 no longer routes through the gateway:
   ```bash
   # No new audit_log rows under the converseai tenant after the redeploy:
   psql "$AI_GATEWAY_PG_DSN" -c "
     SELECT count(*) AS converseai_reqs_since_rollback
       FROM ai_gateway.audit_log a
       JOIN ai_gateway.tenants t ON t.id = a.tenant_id
      WHERE t.slug = 'converseai'
        AND a.created_at > now() - interval '2 minutes';"
   # Expect 0 once both consumers have restarted on the direct provider.
   ```
   Also exercise a chat completion in the ConverseAI v4 UI and confirm it
   still works (now via the direct provider).

### To roll back Chat Ifix

1. **Revert the env vars** in the `campanhas-chatifix` deploy config (its
   own deploy flow — see `CLAUDE.md` `## Dev Environment` for the
   `campanhas-chatifix` deploy mechanism). Flip the transcription backend's
   STT config back to the direct provider:

   | Env var               | App                       | Gateway value (current)                     | Direct value (rollback target) |
   |-----------------------|---------------------------|---------------------------------------------|--------------------------------|
   | `OPENAI_BASE_URL`     | `campanhas-chatifix` backend | `https://ai-gateway-dev.converse-ai.app/v1` | `https://api.openai.com/v1`    |
   | `OPENAI_API_KEY`      | `campanhas-chatifix` backend | the `chat-ifix` tenant key                  | the direct OpenAI key          |

   > **`> CONFIRM:`** verify the exact env-var name the `campanhas-chatifix`
   > backend uses for its Whisper/STT `base_url` + `api_key` against that
   > repo, and substitute here once confirmed.

2. **Redeploy** the `campanhas-chatifix` backend via its deploy flow
   (webhook / Portainer stack as configured for that repo). The backend
   restarts and reads the reverted env vars.

3. **Verify** Chat Ifix transcription no longer routes through the gateway:
   ```bash
   psql "$AI_GATEWAY_PG_DSN" -c "
     SELECT count(*) AS chat_ifix_reqs_since_rollback
       FROM ai_gateway.audit_log a
       JOIN ai_gateway.tenants t ON t.id = a.tenant_id
      WHERE t.slug = 'chat-ifix'
        AND a.created_at > now() - interval '2 minutes';"
   # Expect 0 once the backend has restarted on the direct provider."
   ```
   Also send a real WhatsApp audio through Chat Ifix and confirm it
   transcribes (now via the direct provider).

**Both apps, under 5 minutes, verified.** If either app is left
half-switched (e.g. ConverseAI `apps/api` reverted but `agents/` not), the
verify step's `audit_log` count will be non-zero — that is the catch. Do
not stop the stopwatch until both verify steps return `0`.

---

## Required Env Vars

The per-app env vars the integration depends on, with their gateway-vs-direct
values. These are set in each client app's **deploy config** (ConverseAI v4
→ Portainer stack `converseai-v4-dev`; Chat Ifix → the `campanhas-chatifix`
deploy flow) — **never committed to git** (per `CLAUDE.md` deploy pattern).

| Var                  | App / consumer                  | Required | Gateway value                                 | Direct value                   | Purpose                                              |
|----------------------|---------------------------------|----------|-----------------------------------------------|--------------------------------|------------------------------------------------------|
| `OPENAI_BASE_URL`    | ConverseAI v4 `apps/api`        | yes      | `https://ai-gateway-dev.converse-ai.app/v1`   | `https://api.openai.com/v1`    | OpenAI-SDK base URL for chat / embeddings / tool-calls |
| `OPENAI_API_KEY`     | ConverseAI v4 `apps/api`        | yes      | the `converseai` tenant key                    | the direct OpenAI key          | auth — sent as `Authorization: Bearer`               |
| `ANTHROPIC_BASE_URL` | ConverseAI v4 `apps/api`        | if used  | `https://ai-gateway-dev.converse-ai.app/v1`   | `https://api.anthropic.com`    | Anthropic base URL (if `apps/api` uses Anthropic)    |
| `ANTHROPIC_API_KEY`  | ConverseAI v4 `apps/api`        | if used  | the `converseai` tenant key                    | the direct Anthropic key       | auth for the Anthropic path                          |
| `OPENAI_BASE_URL`    | ConverseAI v4 `agents/`         | yes      | `https://ai-gateway-dev.converse-ai.app/v1`   | `https://api.openai.com/v1`    | LangChain base URL for the Python agent service      |
| `OPENAI_API_KEY`     | ConverseAI v4 `agents/`         | yes      | the `converseai` tenant key                    | the direct OpenAI key          | auth for the `agents/` consumer                      |
| `OPENAI_BASE_URL`    | `campanhas-chatifix` backend    | yes      | `https://ai-gateway-dev.converse-ai.app/v1`   | `https://api.openai.com/v1`    | Whisper/STT base URL for Chat Ifix transcription     |
| `OPENAI_API_KEY`     | `campanhas-chatifix` backend    | yes      | the `chat-ifix` tenant key                     | the direct OpenAI key          | auth for the `chat-ifix` tenant                      |

> **`> CONFIRM:`** the exact env-var **names** above are the standard
> OpenAI-SDK / LangChain names. Confirm against the `converseai-v4` and
> `campanhas-chatifix` repos and substitute the real names before drilling
> the ROLLBACK procedure. The two ConverseAI v4 consumers may read distinct
> names; verify both.

The gateway tenant keys are minted by
`scripts/integration-smoke/provision-tenants.sh --mint-keys` and surfaced to
stdout exactly once — paste them straight into the client app deploy env
vars, never into git or a log file.

---

## Escalation

- **1st responder:** on-call Ifix engineer (whoever notices the client-app
  regression or is paged).
- **Escalation:** Pedro <pedro.araujo@ifixtelecom.com.br>.
- **Sustained client-app regression (>15 min) that a rollback would fix:**
  execute the ROLLBACK procedure for the affected app immediately — a
  drilled <5-min rollback is always preferable to a prolonged regression.
  Communicate the rollback to the affected app's owners via WhatsApp.
- **Gateway-wide outage affecting both integrated apps:** roll back **both**
  ConverseAI v4 and Chat Ifix to their direct providers, notify all
  gateway-consuming app owners, then debug the gateway per
  `RUNBOOK-FAILOVER.md` / `RUNBOOK-EMERGENCY-POD.md`.

---

## Related Docs

- `.planning/phases/08-client-integration-converseai-chat-ifix/08-CONTEXT.md`
  — Phase 8 decisions: the env-var contract, the two-consumers-one-`converseai`-tenant
  model, the `chat-ifix` tenant, the rollback-must-be-drilled requirement,
  the per-app deploy flow.
- `.planning/phases/08-client-integration-converseai-chat-ifix/08-HUMAN-UAT.md`
  — the live-verification scenario sheet; UAT-3 drills this runbook's
  ROLLBACK procedure end-to-end and times it.
- `scripts/integration-smoke/provision-tenants.sh` — idempotent seed script
  that creates the `converseai` + `chat-ifix` tenants and (under
  `--mint-keys`) mints their API keys + the dashboard admin key.
- `scripts/integration-smoke/smoke-converseai.py` — INT-01 gateway contract
  smoke (chat / streaming / tool-calls / embeddings) against the `converseai`
  tenant key.
- `scripts/integration-smoke/smoke-chat-ifix.py` — INT-02 transcription smoke
  with ±10% latency + quality gates against the `chat-ifix` tenant key.
- `gateway/docs/RUNBOOK-FAILOVER.md` — upstream failover + circuit breakers
  (Symptom 4 latency regressions trace into this).
- `gateway/docs/RUNBOOK-QUOTAS-BILLING.md` — per-tenant quota / rate-limit
  inspection (Symptom 2 `429`s).
- `gateway/docs/RUNBOOK-OBSERVABILITY-ALERTING.md` — the Phase 7 dashboard
  the integration's per-tenant traffic surfaces in.
- `CLAUDE.md` `## Dev Environment` — the Portainer + webhook deploy flow for
  `converseai-v4-dev` and the `campanhas-chatifix` deploy flow.
