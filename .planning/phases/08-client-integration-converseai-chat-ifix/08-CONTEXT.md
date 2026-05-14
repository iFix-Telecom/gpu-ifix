# Phase 8: Client Integration — ConverseAI + Chat Ifix - Context

**Gathered:** 2026-05-14
**Status:** Ready for planning

<domain>
## Phase Boundary

The first two production workloads run through the gateway: chat + agents on ConverseAI v4, and audio transcription on Chat Ifix. Delivers INT-01 and INT-02 — validating the OpenAI-compat contract with real traffic, with a tested rollback plan and per-tenant visibility on the dashboard.

This phase delivers the **gpu-ifix-side artifacts** (tenant provisioning, rollback runbook, smoke-test scripts, dashboard verification). The actual `base_url`/env-var switches inside the client app repos are operator HUMAN-UAT actions — and the live validation is gated on the gateway being deployed (currently blocked on Phase 6 emerg integration tests).

Out of scope: the LGPD-sensitive tenants (Telefonia, Cobranças, Campanhas, voice-api) — those are Phase 9. Production hardening / load + chaos testing — Phase 10.

</domain>

<decisions>
## Implementation Decisions

### Scope, Repo Boundary & Tenants
- The gpu-ifix repo delivers: an idempotent tenant-provisioning script, a rollback runbook, and smoke-test scripts. The client-app `base_url`/env-var switches happen **in the client repos** as operator actions (HUMAN-UAT) — not edited by this phase's plans. (The gateway is not deployed yet anyway — build-gateway is blocked on Phase 6 integration tests.)
- **Chat Ifix** = a Chatwoot + kanban fork, with custom apps/improvements; its **external backend lives in the `campanhas-chatifix` repo** (sibling at `/home/pedro/projetos/pedro/campanhas-chatifix`). That backend is the integration surface for the WhatsApp-audio Whisper transcription (INT-02).
- **ConverseAI v4** = `api` (Elysia, OpenAI SDK) + `agents` (Python, LangChain) — sibling repo at `/home/pedro/projetos/pedro/converseai-v4`. Both switch `base_url` via env var.
- Tenant model: **one tenant `converseai`** covering both the api and agents consumers (same org / shared cost attribution); **one tenant `chat-ifix`**. Two tenants total for this phase.
- `data_class` = **`normal`** for both tenants. The `sensitive`-class tenants are Phase 9.

### Tenant Provisioning & Rollback
- Tenant provisioning: an **idempotent seed script in gpu-ifix** using the existing `gatewayctl tenant` / `gatewayctl admin-key create` CLIs (from Phase 2 + Phase 4). Versioned in the repo, re-runnable.
- Rollback (SC3 — under 5 minutes): a **documented runbook** with the exact env-var diff per app (`base_url` reverts to direct OpenAI / Anthropic) plus the redeploy command per app (converseai-v4 via Portainer, campanhas-chatifix via its deploy flow).
- Smoke tests (SC1, SC2): **scripts in gpu-ifix `scripts/integration-smoke/`** that hit the gateway with each tenant's API key — chat / streaming / tool calls / embeddings for ConverseAI; WhatsApp-audio Whisper transcription for Chat Ifix.
- The Chat Ifix audio smoke test ships with a **WhatsApp audio fixture** in gpu-ifix and compares latency + transcription quality against a recorded baseline (SC2 — within ±10%).

### Dashboard & Validation Gating
- SC4 ("dashboard shows both apps as separate tenants") is a **verification item, not new code** — the Phase 7 dashboard already renders per-tenant latency/cost (tenant-table + tenant-name labels). Confirm the two new tenants surface correctly.
- A final **HUMAN-UAT plan (`autonomous: false`)** covers the live integration: the env-var switch in each client app, production smoke tests, the rollback drill, and the dashboard cross-check. Mirrors the 03-08 / 04-09 / 06-11 / 07-09 pattern.
- Everything that requires the gateway to be **live** is deferred to that HUMAN-UAT plan — the autonomous plans (08-01..) ship only the gpu-ifix-side artifacts.

</decisions>

<code_context>
## Existing Code Insights

### Reusable Assets
- `gateway/cmd/gatewayctl` — CLI with `tenant` subcommands (set-mode/set-quota, Phase 4) and `admin-key create/revoke/list` (Phase 4); tenant + API-key auth from Phase 2. The provisioning seed script wraps these.
- Gateway OpenAI-compat endpoints (Phase 2): `POST /v1/chat/completions` (SSE streaming), `POST /v1/embeddings`, `POST /v1/audio/transcriptions` (multipart). The smoke scripts target these.
- Phase 7 dashboard (`dashboard/`) — already renders per-tenant latency (P50/P95/P99), error rate, cost, and tenant-name labels (`tenant-table.tsx`, WR-10 name/slug join). SC4 verifies against this, no new dashboard code.
- Prior HUMAN-UAT plans (03-08, 04-09, 06-11, 07-09) — structural template for the Phase 8 HUMAN-UAT plan.

### Established Patterns
- Client apps are **separate sibling repos**, not part of gpu-ifix: `converseai-v4/` (Turborepo: apps/api Elysia + agents Python), `campanhas-chatifix/` (Bun monorepo backend — the Chat Ifix external backend).
- OpenAI-compat contract means client integration is an env-var change: `base_url` → `gateway.ifix.com.br/v1`, `api_key` → the tenant key. No client code rewrite.
- Deploy: converseai-v4 via Portainer + GitHub webhook; campanhas-chatifix via its own deploy flow. Both standard Ifix.
- HUMAN-UAT / live-validation deferral is the established pattern for every phase whose success criteria need a deployed environment.

### Integration Points
- gpu-ifix seed script ↔ `gatewayctl` ↔ gateway DB (`tenants`, `api_keys`, `admin_keys`).
- Smoke scripts ↔ gateway `/v1/*` endpoints (per-tenant API key).
- Client apps (converseai-v4 api + agents, campanhas-chatifix backend) ↔ gateway `base_url` — operator-configured env vars, validated in HUMAN-UAT.
- Dashboard ↔ gateway `/admin/metrics` — the 2 new tenants appear automatically once they have traffic.

</code_context>

<specifics>
## Specific Ideas

- ConverseAI v4 has two consumers — `apps/api` (Elysia, OpenAI SDK) and `agents/` (Python, LangChain). Both move to the gateway via env var; they share the single `converseai` tenant.
- Chat Ifix's WhatsApp-audio Whisper transcription is implemented in the `campanhas-chatifix` repo's backend — that is the INT-02 integration point.
- Rollback target is concrete: under 5 minutes from decision to fully rolled back (SC3) — the runbook must be drilled, not just written.
- Transcription equivalence bar: latency + quality within ±10% of the prior direct integration (SC2).

</specifics>

<deferred>
## Deferred Ideas

- **Live integration validation** — the env-var switch in each client app + validation against real dev/prod traffic. Deferred to the Phase 8 HUMAN-UAT plan; additionally gated on the gateway being deployed (build-gateway is currently blocked on Phase 6 emerg integration test failures — separate debug session).
- **Sensitive-tenant integration** (Telefonia/NextBilling, Cobranças, Campanhas, voice-api) — Phase 9, behind the data-class-aware failover policy + LGPD review.
- **Production load/chaos testing** of the integrated apps — Phase 10.

</deferred>
