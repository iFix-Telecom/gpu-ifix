# Integration Smoke — Phase 8 + Phase 9 Client Integration

**Status:** Holds the **gpu-ifix-side** Phase 8 **and Phase 9** integration
artifacts — the shared tenant provisioning seed script, the per-app smoke-test
scripts, the smoke report schemas, and the WhatsApp audio fixtures. Phase 9
extends this directory with the sensitive-tenant integration artifacts (the
4-tenant mixed-data-class seed model + the sensitive-failover smoke). The
client-app `base_url`/env-var switches live in the sibling repos
(`converseai-v4`, `campanhas-chatifix`, `fallback-register-ramais-nextbilling`,
`cobrancas-api`, `voice-api`) and are operator HUMAN-UAT actions — they are
**not** part of this directory.

## Files

### Phase 8

| File | Purpose | Added by |
|---|---|---|
| `provision-tenants.sh` | Shared idempotent seed script — wraps `gatewayctl tenant/key/admin-key create` + `tenant set-quota`. Now seeds the Phase-8 **and** Phase-9 tenants (see below). | 08-01, extended 09-01 |
| `README.md` | This file | 08-01, updated 09-01 |
| `smoke-converseai.py` | INT-01 smoke — chat / streaming / tool-calls / embeddings against the gateway `/v1/*` with the `converseai` tenant key | 08-02 |
| `smoke-chat-ifix.py` | INT-02 smoke — WhatsApp-audio Whisper transcription against `/v1/audio/transcriptions`, latency + quality within ±10% of a recorded baseline | 08-03 |
| `report-schema.json` | JSON Schema the Phase-8 smoke scripts validate their report output against | 08-02 / 08-03 |
| `fixtures/` | Real WhatsApp audio sample + baseline transcript/latency for the chat-ifix smoke | 08-03 |

### Phase 9

| File | Purpose | Added by |
|---|---|---|
| `smoke-sensitive-failover.py` | INT-03/04/05 smoke — exercises the RES-08 sensitive-class failover path: induces an upstream failure with a `data_class: sensitive` tenant key, asserts the request fails closed / queues, an `audit_log` row records the decision, and it is **never** proxied to OpenAI/OpenRouter | Phase 9 plan 09-02 |
| `sensitive-failover-report-schema.json` | JSON Schema the sensitive-failover smoke validates its report output against | Phase 9 plan 09-02 |

## Provisioning the tenants

`provision-tenants.sh` wraps the compiled `gatewayctl` CLI to seed the client
tenants in the gateway DB. As of Phase 9 (plan 09-01) it seeds the **four
Phase-9 tenants** with a **per-tenant `data_class`**:

| Tenant | `data_class` | Client repo | Quota? |
|---|---|---|---|
| `telefonia` | `sensitive` | `fallback-register-ramais-nextbilling` | no |
| `cobrancas` | `sensitive` | `cobrancas-api` | yes |
| `campanhas` | `normal` | `campanhas-chatifix` | yes |
| `voice-api` | `normal` | `voice-api` | no |

The `sensitive` tenants (`telefonia`, `cobrancas`) are **never** proxied to
OpenAI/OpenRouter during failover — they queue or fail closed (RES-08). The
`normal` tenants (`campanhas`, `voice-api`) are allowed to fail over externally.

The script is **idempotent** for the tenant rows — re-running it on
already-provisioned tenants is a safe no-op (a "slug already exists" from
`gatewayctl tenant create` is treated as success).

Step 1 — create the tenants + apply the quotas (re-runnable any time):

```bash
AI_GATEWAY_PG_DSN=postgres://USER:PASS@HOST:PORT/DB \
  ./scripts/integration-smoke/provision-tenants.sh --gatewayctl /path/to/gatewayctl
```

This creates the 4 tenants **and** applies per-tenant quotas to `cobrancas` +
`campanhas` via `gatewayctl tenant set-quota`. The `set-quota` step is an
**idempotent UPDATE** — it is **not** gated behind `--mint-keys` and runs on
**every** invocation, re-applying the same quota each time. A non-zero
`set-quota` exit is fatal (it can only mean the tenant does not exist).

Step 2 — mint the API keys (run **exactly once**):

```bash
AI_GATEWAY_PG_DSN=postgres://USER:PASS@HOST:PORT/DB \
  ./scripts/integration-smoke/provision-tenants.sh --gatewayctl /path/to/gatewayctl --mint-keys
```

`gatewayctl key create` / `admin-key create` are **not** idempotent — each call
mints a brand-new row. The `--mint-keys` flag is the explicit opt-in that gates
those steps, so a routine re-run of the script does not spray duplicate keys.
Pass `--mint-keys` once, the first time, then copy the five raw keys it prints
(4 tenant keys, each with its correct per-tenant `data_class` + 1 dashboard
admin key) into the respective client repo's Portainer stack env var. The keys
are shown **once** and are never re-derivable.

Flags:

- `--gatewayctl PATH` — path to the compiled `gatewayctl` binary, or a wrapper
  such as a `docker exec ifix-ai-gateway /gatewayctl` shim. Default: `gatewayctl`
  on `PATH`.
- `--mint-keys` — opt-in: also mint the 4 tenant keys + the dashboard admin key.
- `--dry-run` — print the `gatewayctl` commands that would run (including the
  `set-quota` calls), execute nothing, touch no DB.

Env:

- `AI_GATEWAY_PG_DSN` (required) — Postgres DSN the wrapped `gatewayctl` reads to
  reach the gateway DB.

## Scope

This directory delivers **gpu-ifix-side artifacts only** (09-CONTEXT.md
`## Decisions`). It provisions the gateway-side identity (tenants + keys +
quotas) and ships the smoke scripts that exercise the gateway `/v1/*`
endpoints — including the Phase-9 sensitive-failover smoke.

It does **not** edit the client apps. The actual `base_url` / `api_key` env-var
switches inside the four client sibling repos —
`fallback-register-ramais-nextbilling` (Telefonia/NextBilling), `cobrancas-api`
(Cobranças), `campanhas-chatifix` (Campanhas), and `voice-api` — happen in those
repos as operator HUMAN-UAT actions, and the live validation is additionally
gated on the gateway being deployed.

## See also

- `docs/RUNBOOK-CLIENT-INTEGRATION.md` — Phase 8 rollback runbook (per-app
  env-var diff + redeploy command), produced by plan 08-04.
- `gateway/docs/RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md` — Phase 9
  sensitive-tenant rollback runbook (per-app rollback for telefonia, cobrancas,
  campanhas, voice-api; expected-503 RES-08 behavior), produced by plan 09-03.
- `gateway/docs/LGPD-SUBPROCESSORS.md` — sub-processor disclosure document
  (lists OpenAI, OpenRouter, Vast.ai), produced by plan 09-04.
- `gateway/docs/LGPD-REVIEW-CHECKLIST.md` — operator checklist gating
  sensitive-tenant production activation, produced by plan 09-04.
- The Phase 8 HUMAN-UAT plan (`08-HUMAN-UAT.md`) — live env-var switch,
  production smoke runs, the timed rollback drill, and the dashboard
  cross-check; produced by plan 08-04.
- The Phase 9 HUMAN-UAT plan (`09-HUMAN-UAT.md`) — live sensitive-tenant
  env-var switch per client repo, the sensitive-failover smoke run, the timed
  rollback drill, and the external LGPD legal sign-off checkpoint.
