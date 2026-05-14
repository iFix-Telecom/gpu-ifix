# Client Integration (Sensitive Tenants) — Telefonia + Cobranças + Campanhas + voice-api

**Phase 9 (`ifix-ai-gateway`) sensitive-tenant client-integration layer.**
**Last updated: 2026-05-14.** Read this when:

- One of the four Phase-9 apps — **Telefonia/NextBilling**, **Cobranças**,
  **Campanhas**, or **voice-api** — is being pointed at the gateway for the
  first time.
- A **sensitive** tenant (Telefonia or Cobranças) is getting `503`s during an
  upstream outage — see Symptom 3, this is **expected** RES-08 behavior, not
  a bug.
- One of the four integrations is misbehaving (`401`s, `429`s, regressed
  transcription/LLM latency or quality).
- An integration **needs to be rolled back** — reverting a client app to call
  the LLM/STT provider directly, bypassing the gateway.
- Post-incident review of a sensitive-tenant client-integration event.

This document is the operator's first-line reference for the gpu-ifix-side of
the four Phase-9 integrations. The actual `base_url`/`api_key` env-var switches
happen **in the client repos** (`fallback-register-ramais-nextbilling`,
`cobrancas-api`, `campanhas-chatifix`, `voice-api` — sibling repos, **not** part
of gpu-ifix) and are operator actions, validated by `09-HUMAN-UAT.md`.

> **Deployment gate:** as of Phase 9 the gateway is not yet deployed to the
> `ai-gateway-dev` Portainer stack (build-gateway is blocked on Phase 6
> emergency-pod integration tests — a separate debug session). The live
> integration + this runbook's procedures are exercised only once the gateway
> is deployed.

---

## Mental Model (30 seconds)

OpenAI-compatibility means client integration is an **env-var change, not a
code rewrite**. Each client app moves from calling the provider directly to
calling the gateway by flipping two values:

| Knob       | Direct (today)                   | Gateway (after switch)                          |
|------------|----------------------------------|-------------------------------------------------|
| `base_url` | `https://api.openai.com/v1` etc. | `https://ai-gateway-dev.converse-ai.app/v1`     |
| `api_key`  | the direct provider key          | the per-**tenant** key minted by `gatewayctl`   |

**The data-class split is the load-bearing concept of this runbook.** The four
Phase-9 tenants are NOT all the same — two are `sensitive` and two are `normal`,
and that controls what happens during an upstream outage:

| Tenant      | App repo                              | `data_class` | On upstream outage                          |
|-------------|---------------------------------------|--------------|---------------------------------------------|
| `telefonia` | `fallback-register-ramais-nextbilling`| **sensitive**| queue (~4s bounded retry) or `503` — NEVER external |
| `cobrancas` | `cobrancas-api`                       | **sensitive**| queue (~4s bounded retry) or `503` — NEVER external |
| `campanhas` | `campanhas-chatifix`                  | `normal`     | fails over to OpenRouter / OpenAI           |
| `voice-api` | `voice-api`                           | `normal`     | fails over to OpenRouter / OpenAI           |

```
                              ┌────────────────────────────────────────┐
 SENSITIVE tenants            │              ifix-ai-gateway            │
 ┌──────────────────┐  key:   │   /v1/chat/completions     (LLM)        │   ┌──────────────┐
 │ telefonia        │─telefonia│   /v1/embeddings           (EMBED)     │──▶│ local GPU    │
 │ (call-audio STT) │  ──┐    │   /v1/audio/transcriptions (STT)        │   │ (Qwen/Whisper│
 ├──────────────────┤    ├───▶│                                         │   │  /BGE-M3)    │
 │ cobrancas        │─cobrancas│  data_class=sensitive → on outage:      │   └──────┬───────┘
 │ (LLM + embed)    │  ──┘    │  bounded ~4s retry, then 503             │          │ outage
 └──────────────────┘         │  (upstream='blocked_sensitive')         │          │
                              │  ╳ NEVER proxied to OpenAI/OpenRouter    │   ┌──────▼───────┐
 NORMAL tenants               │                                         │   │ OpenRouter / │
 ┌──────────────────┐  key:   │  data_class=normal → on outage:          │──▶│ OpenAI       │
 │ campanhas        │─campanhas│  fail over to external provider          │   │ (failover)   │
 │ (LLM + embed)    │  ──┐    │                                         │   └──────────────┘
 ├──────────────────┤    ├───▶│                                         │
 │ voice-api        │─voice-api│  voice-api: ONLY LLM script-generation  │
 │ (LLM scripts)    │  ──┘    │  routes here — TTS stays on local CPU    │
 └──────────────────┘         └────────────────────────────────────────┘
```

**Sensitive = NEVER external (RES-08).** `telefonia` carries call audio (PII)
and `cobrancas` carries financial/collections data. During a local-upstream
outage their requests **queue** (a bounded ~4s, 3-attempt retry: 200ms / 800ms
/ 3s) waiting for a CLOSED breaker, and if that exhausts they **fail closed**
with a `503` — they are **never** proxied to OpenAI or OpenRouter. This is the
Phase-3 RES-08 contract; see `RUNBOOK-FAILOVER.md` Symptom 3.

**Normal = can fail over.** `campanhas` (marketing personalization) and
`voice-api` (LLM script generation) are `normal` — during a local outage they
fail over to the external providers like the Phase-8 tenants do.

**voice-api scope note.** Only voice-api's **LLM script-generation** calls route
through the gateway. Its **TTS stays on the local CPU** and is out of scope for
this integration — do not point voice-api's TTS path at the gateway.

The four tenants + their API keys are seeded idempotently by
`scripts/integration-smoke/provision-tenants.sh` (wraps `gatewayctl tenant
create` / `key create` / `tenant set-quota` / `admin-key create`). `cobrancas`
and `campanhas` additionally get per-tenant quotas from that script. The raw
keys are surfaced to stdout exactly once and pasted by the operator into each
client app's deploy env vars — they are never committed.

---

## Quick Diagnosis (~2 minutes)

Run these in order; stop at the first one that points to a clear cause.
Substitute the real gateway host and the real per-tenant keys.

```bash
# 1. Gateway reachable with each tenant key? Curl one endpoint per tenant.
#    telefonia — transcription (call audio):
curl -sS -X POST https://ai-gateway-dev.converse-ai.app/v1/audio/transcriptions \
  -H "Authorization: Bearer $TELEFONIA_TENANT_KEY" \
  -F model=whisper \
  -F file=@scripts/integration-smoke/fixtures/whatsapp-sample.ogg
#    cobrancas / campanhas / voice-api — chat completions:
for KEY in "$COBRANCAS_TENANT_KEY" "$CAMPANHAS_TENANT_KEY" "$VOICE_API_TENANT_KEY"; do
  curl -sS -X POST https://ai-gateway-dev.converse-ai.app/v1/chat/completions \
    -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
    -d '{"model":"qwen","messages":[{"role":"user","content":"PING"}],"max_tokens":5}'
done
# 200 + a completion  → auth + path OK
# 401                 → wrong/unset api key (Symptom 1)
# 429                 → tenant quota / rate-limit (Symptom 2)
# 503                 → if a sensitive tenant during an outage, see Symptom 3 (EXPECTED)

# 2. Audit log — are requests landing under the right tenant slug?
psql "$AI_GATEWAY_PG_DSN" -c "
  SELECT t.slug, count(*) AS reqs_10min
    FROM ai_gateway.audit_log a
    JOIN ai_gateway.tenants t ON t.id = a.tenant_id
   WHERE a.created_at > now() - interval '10 minutes'
   GROUP BY t.slug;"
# Expect rows for 'telefonia' / 'cobrancas' / 'campanhas' / 'voice-api' once
# their apps have traffic.

# 3. Sensitive-block check — during a local-upstream outage, confirm the
#    RES-08 fail-closed path is firing for the sensitive tenants:
psql "$AI_GATEWAY_PG_DSN" -c "
  SELECT t.slug, a.upstream, a.status_code, count(*)
    FROM ai_gateway.audit_log a
    JOIN ai_gateway.tenants t ON t.id = a.tenant_id
   WHERE a.created_at > now() - interval '10 minutes'
     AND a.upstream = 'blocked_sensitive'
   GROUP BY t.slug, a.upstream, a.status_code;"
# Rows here for 'telefonia' / 'cobrancas' with upstream='blocked_sensitive'
# and status_code=503 = RES-08 working AS DESIGNED (see Symptom 3), NOT a bug.

# 4. Fuller contract check — run the committed sensitive-failover smoke
#    against a sensitive tenant key (it induces an upstream failure and
#    asserts the queue/fail-closed + never-external + audit gates):
python scripts/integration-smoke/smoke-sensitive-failover.py \
  --gateway-url https://ai-gateway-dev.converse-ai.app \
  --api-key "$COBRANCAS_TENANT_KEY" --out /tmp/sensitive-failover-report.json
# exit 0 + gates.all_passed == true → the RES-08 contract holds

# 5. Phase 7 dashboard — confirm all four tenants render as separate rows:
#    open the dashboard, check the tenant-table shows 'telefonia',
#    'cobrancas', 'campanhas', and 'voice-api'.
```

---

## Incident Response by Symptom

### Symptom 1 — Client app gets `401` from the gateway

**Likely cause:** the client app's `api_key` env var is unset, is still the
direct-provider key, or is a tenant key that was revoked.

**Diagnose:**

1. Confirm the env var is set in the client app's deploy config (Telefonia →
   `fallback-register-ramais-nextbilling` deploy; Cobranças → `cobrancas-api`
   Portainer stack; Campanhas → `campanhas-chatifix` deploy; voice-api →
   `voice-api` deploy).
2. Curl the gateway with the same key (Quick Diagnosis step 1). A `401` here
   confirms it is a key problem, not a client-code problem.
3. Check the key exists and is active:
   ```bash
   docker exec ifix-ai-gateway /gatewayctl key list --tenant telefonia
   docker exec ifix-ai-gateway /gatewayctl key list --tenant cobrancas
   docker exec ifix-ai-gateway /gatewayctl key list --tenant campanhas
   docker exec ifix-ai-gateway /gatewayctl key list --tenant voice-api
   ```

**Mitigate:**

- If the key is wrong/unset: set the correct per-tenant key in the client app's
  deploy env var and redeploy (see ROLLBACK below for the per-app redeploy
  command — the same redeploy mechanism applies).
- If the key was revoked or lost: re-mint it with
  `scripts/integration-smoke/provision-tenants.sh --mint-keys` (it is idempotent
  for `tenant create`; `--mint-keys` mints a fresh key row), then paste the new
  raw key into the client app env var.

**Recovery:** re-run Quick Diagnosis step 1 — a `200` confirms auth is restored.
Confirm an `audit_log` row appears under the expected tenant slug (step 2).

### Symptom 2 — Client app gets `429` from the gateway

**Likely cause:** the tenant hit its per-tenant quota or rate-limit. **Cobranças
and Campanhas** are provisioned with per-tenant quotas by the seed script
(`gatewayctl tenant set-quota`, Phase-4 quota machinery); Telefonia and voice-api
are not quota-capped by default.

**Diagnose:**

1. Check the tenant's current mode + quota:
   ```bash
   docker exec ifix-ai-gateway /gatewayctl tenant set-quota --slug cobrancas   # (no value = show)
   ```
   (See `gateway/docs/RUNBOOK-QUOTAS-BILLING.md` for the full quota-inspection
   procedure.)
2. Check `audit_log` for `status_code = 429` rows and their `error_code`.

**Mitigate:** this is a capacity signal, not a health signal — the client app
should back off per its own retry policy. If the quota is genuinely too low for
the workload, raise it via `gatewayctl tenant set-quota` (see
`RUNBOOK-QUOTAS-BILLING.md`). A `429` does **not** mean the integration is
broken — do not roll back for a `429`.

### Symptom 3 — Sensitive tenant (Telefonia / Cobranças) getting `503`s during an upstream outage

> **This is EXPECTED RES-08 behavior, not a bug.** Do not "fix" the 503. Do not
> roll back the sensitive tenant to make the 503 go away.

**Likely cause:** the local GPU upstream is down or saturated, and a `sensitive`
tenant's request could not be served locally. Per the Phase-3 RES-08 policy, a
`sensitive` tenant is **never** proxied to OpenAI/OpenRouter — leaking call audio
(Telefonia) or financial data (Cobranças) to an external provider is not
permitted. So the request **fails closed**: a bounded ~4s, 3-attempt retry
(200ms / 800ms / 3s) waits for a CLOSED breaker; if that exhausts, the gateway
returns `503` with envelope code `upstream_unavailable_for_sensitive_tenant` and
`Retry-After: 30`, and writes an `audit_log` row with `upstream='blocked_sensitive'`.
A streaming sensitive request `503`s immediately (D-B4 fail-fast, no 4s retry).

**Diagnose:**

1. Confirm it is the RES-08 path and not an auth/quota problem — run Quick
   Diagnosis step 3. Rows with `upstream='blocked_sensitive'` + `status_code=503`
   for `telefonia` / `cobrancas` confirm RES-08 fired as designed.
2. Confirm the root cause is a local-upstream outage — check the upstream
   health/breaker state per `RUNBOOK-FAILOVER.md` Symptom 1. An `open` breaker
   on the local tier-0 upstream is the root cause.
3. Cross-reference `RUNBOOK-FAILOVER.md` **Symptom 3 — Sensitive tenant reports
   503s** for the upstream-side view of this same behavior.

**Mitigate:** the mitigation is to **restore the local upstream**, not to touch
the sensitive tenant. Follow `RUNBOOK-FAILOVER.md` / `RUNBOOK-EMERGENCY-POD.md`
to recover or replace the local GPU pod. While the upstream is down the
sensitive tenants will continue to receive `503`s — their client apps should
back off per `Retry-After: 30`. There is **no override path** to send sensitive
traffic external (LGPD policy).

**Recovery:** once the local upstream's breaker returns to CLOSED, the bounded
retry succeeds and sensitive requests are served again. Re-run Quick Diagnosis
step 3 — no new `blocked_sensitive` rows means recovery is complete.

### Symptom 4 — Transcription / LLM latency or quality regressed

**Likely cause:** the gateway's STT path (Telefonia call-audio Whisper) or LLM
path (Cobranças / Campanhas / voice-api) is slower or lower-quality than the
prior direct integration, or — for the `normal` tenants — the gateway is in
failover to an external provider.

**Diagnose:**

1. Run the relevant smoke. For the sensitive RES-08 path, run
   `smoke-sensitive-failover.py` (Quick Diagnosis step 4). For a steady-state
   contract check, the Phase-8 `smoke-converseai.py` / `smoke-chat-ifix.py`
   contract shapes apply to the chat / transcription endpoints.
2. For a `normal` tenant: check whether the upstream is in failover
   (`curl .../v1/health/upstreams` — an `open` tier-0 state means traffic is on
   the slower external fallback; see `RUNBOOK-FAILOVER.md` Symptom 1).
3. For a `sensitive` tenant: it is never on an external fallback — a latency
   bump is the bounded ~4s RES-08 retry (it is waiting for the local breaker),
   or a genuine local-path regression.

**Mitigate:** if the regression is a failover state on a `normal` tenant,
recover the local tier-0 per `RUNBOOK-FAILOVER.md`. If it is a genuine
gateway-path regression that cannot be quickly fixed, **roll back the affected
app** (see ROLLBACK below) and file the regression for a follow-up.

---

## ROLLBACK procedure

> **This is the load-bearing section of this runbook (SC4).** Target: under
> **5 minutes per app** from the decision to roll back to a fully-rolled-back,
> verified state. Each of the four procedures below MUST be **drilled** in
> `09-HUMAN-UAT.md` (timed) — a written-but-never-run rollback does not satisfy
> SC4.

Roll back when a client app's gateway integration is causing a
production-affecting regression that cannot be fixed in-place faster than the
rollback itself. Do **not** roll back for a `429` (capacity), a transient `401`
(fix the key instead), or a sensitive-tenant `503` during an outage (Symptom 3
— that is expected; restore the upstream instead).

Each app has its own numbered procedure: env-var revert → redeploy → verify.
The verify step is a `psql` `audit_log` row-count for the tenant slug that MUST
reach `0` after the redeploy — a non-zero count means the app is half-switched
(still sending some traffic through the gateway) and the procedure is **not**
done.

### To roll back Telefonia / NextBilling

1. **Revert the env vars** in the `fallback-register-ramais-nextbilling` deploy
   config (its own deploy flow — see `CLAUDE.md` `## Dev Environment`). Flip the
   call-audio transcription backend's STT config back to the direct provider:

   | Env var           | App                                    | Gateway value (current)                     | Direct value (rollback target) |
   |-------------------|----------------------------------------|---------------------------------------------|--------------------------------|
   | `OPENAI_BASE_URL` | `fallback-register-ramais-nextbilling` | `https://ai-gateway-dev.converse-ai.app/v1` | `https://api.openai.com/v1`    |
   | `OPENAI_API_KEY`  | `fallback-register-ramais-nextbilling` | the `telefonia` tenant key                  | the direct OpenAI key          |

   > **`> CONFIRM:`** the env-var **names** above are the standard OpenAI-SDK
   > names. The operator MUST confirm the actual names the
   > `fallback-register-ramais-nextbilling` repo reads for its Whisper/STT
   > `base_url` + `api_key` and substitute the real names here before drilling
   > this procedure.

2. **Redeploy** the `fallback-register-ramais-nextbilling` app via its deploy
   flow (webhook / Portainer stack as configured for that repo). The app
   restarts and reads the reverted env vars at boot.

3. **Verify** Telefonia no longer routes through the gateway:
   ```bash
   psql "$AI_GATEWAY_PG_DSN" -c "
     SELECT count(*) AS telefonia_reqs_since_rollback
       FROM ai_gateway.audit_log a
       JOIN ai_gateway.tenants t ON t.id = a.tenant_id
      WHERE t.slug = 'telefonia'
        AND a.created_at > now() - interval '2 minutes';"
   # Expect 0 once the app has restarted on the direct provider.
   ```
   Also send a real call audio through Telefonia and confirm it transcribes
   (now via the direct provider). **<5-min budget. Must be drilled in 09-HUMAN-UAT.**

### To roll back Cobranças

1. **Revert the env vars** in the `cobrancas-api` Portainer stack (Portainer UI
   → stack → Editor → Environment variables). Flip each from the gateway value
   back to the direct-provider value:

   | Env var           | App           | Gateway value (current)                     | Direct value (rollback target) |
   |-------------------|---------------|---------------------------------------------|--------------------------------|
   | `OPENAI_BASE_URL` | `cobrancas-api` | `https://ai-gateway-dev.converse-ai.app/v1` | `https://api.openai.com/v1`    |
   | `OPENAI_API_KEY`  | `cobrancas-api` | the `cobrancas` tenant key                  | the direct OpenAI key          |

   > **`> CONFIRM:`** confirm the exact env-var names the `cobrancas-api` repo
   > reads for its LLM/embedding `base_url` + `api_key` against that repo, and
   > substitute the real names here before drilling.

2. **Redeploy.** In Portainer, the `cobrancas-api` stack: Portainer UI → stack
   `cobrancas-api` → **Update the stack** (re-create + pull image). Containers
   restart and read the reverted env vars at boot.

3. **Verify** Cobranças no longer routes through the gateway:
   ```bash
   psql "$AI_GATEWAY_PG_DSN" -c "
     SELECT count(*) AS cobrancas_reqs_since_rollback
       FROM ai_gateway.audit_log a
       JOIN ai_gateway.tenants t ON t.id = a.tenant_id
      WHERE t.slug = 'cobrancas'
        AND a.created_at > now() - interval '2 minutes';"
   # Expect 0 once the app has restarted on the direct provider.
   ```
   Also exercise a Cobranças LLM-personalization request and confirm it still
   works (now via the direct provider). **<5-min budget. Must be drilled in
   09-HUMAN-UAT.**

### To roll back Campanhas

1. **Revert the env vars** in the `campanhas-chatifix` deploy config (its own
   deploy flow — see `CLAUDE.md` `## Dev Environment`). Flip each from the
   gateway value back to the direct-provider value:

   | Env var           | App                          | Gateway value (current)                     | Direct value (rollback target) |
   |-------------------|------------------------------|---------------------------------------------|--------------------------------|
   | `OPENAI_BASE_URL` | `campanhas-chatifix` backend | `https://ai-gateway-dev.converse-ai.app/v1` | `https://api.openai.com/v1`    |
   | `OPENAI_API_KEY`  | `campanhas-chatifix` backend | the `campanhas` tenant key                  | the direct OpenAI key          |

   > **`> CONFIRM:`** verify the exact env-var names the `campanhas-chatifix`
   > backend uses for its LLM/embedding `base_url` + `api_key` against that
   > repo, and substitute the real names here once confirmed. Note `campanhas`
   > shares the `campanhas-chatifix` repo with the Phase-8 `chat-ifix` tenant —
   > confirm you are reverting the Campanhas LLM-personalization config, not the
   > Chat-Ifix transcription config.

2. **Redeploy** the `campanhas-chatifix` backend via its deploy flow (webhook /
   Portainer stack as configured for that repo). The backend restarts and reads
   the reverted env vars.

3. **Verify** Campanhas no longer routes through the gateway:
   ```bash
   psql "$AI_GATEWAY_PG_DSN" -c "
     SELECT count(*) AS campanhas_reqs_since_rollback
       FROM ai_gateway.audit_log a
       JOIN ai_gateway.tenants t ON t.id = a.tenant_id
      WHERE t.slug = 'campanhas'
        AND a.created_at > now() - interval '2 minutes';"
   # Expect 0 once the backend has restarted on the direct provider.
   ```
   Also exercise a Campanhas personalization request and confirm it still works
   (now via the direct provider). **<5-min budget. Must be drilled in
   09-HUMAN-UAT.**

### To roll back voice-api

1. **Revert the env vars** in the `voice-api` deploy config (its own deploy flow
   — see `CLAUDE.md` `## Dev Environment`). Flip the **LLM script-generation**
   config back to the direct provider (the TTS path is unaffected — it never ran
   through the gateway):

   | Env var           | App         | Gateway value (current)                     | Direct value (rollback target) |
   |-------------------|-------------|---------------------------------------------|--------------------------------|
   | `OPENAI_BASE_URL` | `voice-api` | `https://ai-gateway-dev.converse-ai.app/v1` | `https://api.openai.com/v1`    |
   | `OPENAI_API_KEY`  | `voice-api` | the `voice-api` tenant key                  | the direct OpenAI key          |

   > **`> CONFIRM:`** confirm the exact env-var names the `voice-api` repo reads
   > for its LLM script-generation `base_url` + `api_key` against that repo, and
   > substitute the real names here before drilling. Verify you are reverting
   > only the LLM script-generation config — the local-CPU TTS path has no
   > gateway env vars to revert.

2. **Redeploy** the `voice-api` app via its deploy flow (webhook / Portainer
   stack as configured for that repo). The app restarts and reads the reverted
   env vars.

3. **Verify** voice-api LLM script-generation no longer routes through the
   gateway:
   ```bash
   psql "$AI_GATEWAY_PG_DSN" -c "
     SELECT count(*) AS voice_api_reqs_since_rollback
       FROM ai_gateway.audit_log a
       JOIN ai_gateway.tenants t ON t.id = a.tenant_id
      WHERE t.slug = 'voice-api'
        AND a.created_at > now() - interval '2 minutes';"
   # Expect 0 once the app has restarted on the direct provider.
   ```
   Also exercise a voice-api script-generation request and confirm it still
   works (now via the direct provider). **<5-min budget. Must be drilled in
   09-HUMAN-UAT.**

---

## Required Env Vars

The per-app env vars the integration depends on, with their gateway-vs-direct
values. These are set in each client app's **deploy config** — **never
committed to git** (per `CLAUDE.md` deploy pattern).

| Var               | App                                    | Required | Gateway value                               | Direct value                | Purpose                                                  |
|-------------------|----------------------------------------|----------|---------------------------------------------|------------------------------|----------------------------------------------------------|
| `OPENAI_BASE_URL` | `fallback-register-ramais-nextbilling` | yes      | `https://ai-gateway-dev.converse-ai.app/v1` | `https://api.openai.com/v1`  | Whisper/STT base URL for Telefonia call-audio transcription |
| `OPENAI_API_KEY`  | `fallback-register-ramais-nextbilling` | yes      | the `telefonia` tenant key                  | the direct OpenAI key        | auth — sent as `Authorization: Bearer`                   |
| `OPENAI_BASE_URL` | `cobrancas-api`                        | yes      | `https://ai-gateway-dev.converse-ai.app/v1` | `https://api.openai.com/v1`  | OpenAI-SDK base URL for Cobranças LLM personalization + embeddings |
| `OPENAI_API_KEY`  | `cobrancas-api`                        | yes      | the `cobrancas` tenant key                  | the direct OpenAI key        | auth for the `cobrancas` tenant                          |
| `OPENAI_BASE_URL` | `campanhas-chatifix` backend           | yes      | `https://ai-gateway-dev.converse-ai.app/v1` | `https://api.openai.com/v1`  | base URL for Campanhas LLM personalization + embeddings  |
| `OPENAI_API_KEY`  | `campanhas-chatifix` backend           | yes      | the `campanhas` tenant key                  | the direct OpenAI key        | auth for the `campanhas` tenant                          |
| `OPENAI_BASE_URL` | `voice-api`                            | yes      | `https://ai-gateway-dev.converse-ai.app/v1` | `https://api.openai.com/v1`  | base URL for voice-api LLM script generation (TTS stays local CPU) |
| `OPENAI_API_KEY`  | `voice-api`                            | yes      | the `voice-api` tenant key                  | the direct OpenAI key        | auth for the `voice-api` tenant                          |

> **`> CONFIRM:`** the exact env-var **names** above are the standard OpenAI-SDK
> names. Confirm against the `fallback-register-ramais-nextbilling`,
> `cobrancas-api`, `campanhas-chatifix`, and `voice-api` repos and substitute
> the real names before drilling the ROLLBACK procedure.

The gateway tenant keys are minted by
`scripts/integration-smoke/provision-tenants.sh --mint-keys` and surfaced to
stdout exactly once — paste them straight into the client app deploy env vars,
never into git or a log file.

---

## Sensitive-tenant notes

Two facts about the `sensitive` tenants (Telefonia, Cobranças) that do **not**
apply to the Phase-8 `normal` tenants:

- **Sensitive tenants cannot be set to `peak` mode.** Attempting
  `gatewayctl tenant set-mode --slug telefonia --mode peak` (or `cobrancas`) is
  **rejected**. `peak` mode routes off-hours traffic to OpenRouter — which would
  send sensitive data external, violating RES-08. This is enforced by a
  triple-defense (Phase 4): the `chk_sensitive_no_peak` DB CHECK constraint, a
  service-layer guard, and a CLI-layer guard. If you need a sensitive tenant on
  a cost-saving schedule, that is not available — sensitive tenants run `24/7`
  on the local upstream or they `503`.
- **Sensitive content is never persisted.** Per D-B2, the gateway writes the
  decision row to `audit_log` (route, tenant, `upstream`, `status_code`,
  latency) for a sensitive request but writes **no** `audit_log_content` row —
  the request/response bodies of sensitive tenants are never stored. The
  `audit_log` row with `upstream='blocked_sensitive'` is the attribution record;
  there is no body capture to leak.

---

## Escalation

- **1st responder:** on-call Ifix engineer (whoever notices the client-app
  regression or is paged).
- **Escalation:** Pedro <pedro.araujo@ifixtelecom.com.br>.
- **Sustained client-app regression (>15 min) that a rollback would fix:**
  execute the ROLLBACK procedure for the affected app immediately — a drilled
  <5-min rollback is always preferable to a prolonged regression. Communicate
  the rollback to the affected app's owners via WhatsApp.
- **Sustained sensitive-tenant `503`s (>15 min):** this is a local-upstream
  outage, not an integration bug — do **not** roll back the sensitive tenant.
  Communicate to the affected tenants (Telefonia, Cobranças) via WhatsApp that
  the gateway will continue to block their requests until the local primary
  recovers (LGPD policy — no override path), then debug the upstream per
  `RUNBOOK-FAILOVER.md` / `RUNBOOK-EMERGENCY-POD.md`.
- **Gateway-wide outage affecting all integrated apps:** roll back the `normal`
  tenants (`campanhas`, `voice-api`) to their direct providers; the `sensitive`
  tenants (`telefonia`, `cobrancas`) cannot be sent external — restore the local
  upstream for them. Notify all gateway-consuming app owners, then debug the
  gateway per `RUNBOOK-FAILOVER.md` / `RUNBOOK-EMERGENCY-POD.md`.

---

## Related Docs

- `.planning/phases/09-client-integration-sensitive-tenants-telefonia-cobran-as-campanhas-voice-api/09-CONTEXT.md`
  — Phase 9 decisions: the four-tenant mixed-data-class model, the
  sensitive-vs-normal split, the gpu-ifix-side-vs-operator-action repo boundary,
  the <5-min rollback bar.
- `.planning/phases/09-client-integration-sensitive-tenants-telefonia-cobran-as-campanhas-voice-api/09-HUMAN-UAT.md`
  — the live-verification scenario sheet; it drills this runbook's four ROLLBACK
  procedures end-to-end and times them, runs the sensitive-failover smoke, and
  carries the blocking LGPD legal sign-off checkpoint.
- `scripts/integration-smoke/provision-tenants.sh` — idempotent seed script that
  creates the `telefonia` / `cobrancas` / `campanhas` / `voice-api` tenants,
  mints their per-data-class API keys, and sets the Cobranças + Campanhas
  quotas.
- `scripts/integration-smoke/smoke-sensitive-failover.py` — the sensitive-class
  failover smoke: induces an upstream failure and asserts the RES-08
  queue/fail-closed + never-external + audit gates against a sensitive tenant
  key.
- `gateway/docs/RUNBOOK-FAILOVER.md` — upstream failover + circuit breakers;
  **Symptom 3 — Sensitive tenant reports 503s** is the upstream-side view of
  this runbook's Symptom 3.
- `gateway/docs/RUNBOOK-EMERGENCY-POD.md` — emergency-pod spin-up, the recovery
  path when a sensitive tenant's local upstream is down.
- `gateway/docs/RUNBOOK-QUOTAS-BILLING.md` — per-tenant quota / rate-limit
  inspection (Symptom 2 `429`s; Cobranças + Campanhas are quota-capped).
- `gateway/docs/RUNBOOK-CLIENT-INTEGRATION.md` — the Phase-8 client-integration
  runbook (ConverseAI v4 + Chat Ifix); this doc mirrors its skeleton.
- `gateway/docs/LGPD-SUBPROCESSORS.md` — the LGPD sub-processor disclosure
  (OpenAI, OpenRouter, Vast.ai) and the sensitive-never-external guarantee.
- `gateway/docs/LGPD-REVIEW-CHECKLIST.md` — the LGPD review checklist gating
  sensitive-tenant production activation.
- `CLAUDE.md` `## Dev Environment` — the Portainer + webhook deploy flows for
  the four client app repos.
