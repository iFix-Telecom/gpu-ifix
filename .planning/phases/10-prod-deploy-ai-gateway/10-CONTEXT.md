# Phase 10: prod-deploy-ai-gateway - Context

**Gathered:** 2026-05-25
**Status:** Ready for planning

<domain>
## Phase Boundary

First production deploy of the `ifix-ai-gateway` (gateway + dashboard) — moving from the operator-managed `/opt/ai-gateway-dev/` dev stack on `vps-ifix-vm` to a hardened production stack on `n8n-ia-vm` (VM 101, 10.10.10.20) colocated with the existing tier-0 embed pod (`ai-gateway-embed` Infinity 24/7). Cuts the first stable release (`main` branch + tag `v1.0.0`).

**In scope (Phase 10):**
- Provision `/opt/ai-gateway-prod/` on `n8n-ia-vm` (compose stack: gateway + dashboard + dedicated Redis).
- Create new Postgres schema `ai_gateway_prod` on the shared DO managed instance; run migrations.
- Public DNS + TLS via the existing edge Traefik on `ifix-prod-01` host: `ai-gateway.converse-ai.app` + `ai-dashboard.converse-ai.app`.
- Bring up new Sentry project `ifix-ai-gateway-prod`.
- Promote `develop` → `main`; tag `v1.0.0`; GHA builds `:main` / `:v1.0.0` images.
- Minimal `RUNBOOK-DEPLOY.md` (bring-up + rollback + cut-release).
- Golden-path smoke per tenant (re-use Phase 08/09 smoke contract) executed inside the operator HUMAN-UAT plan.
- Cascade-close Phase 02/03/04/05 live-UAT deferrals — the prod deploy is the live environment those VERIFICATION.md files were waiting on.

**Out of scope (split → Phase 11 hardening, separately scheduled):**
- PRD-01 production load test (3-tenant real-traffic profile baseline).
- PRD-02 chaos test — kill primary pod under load.
- PRD-03 chaos test — OpenRouter degraded / sensitive-tenant policy proof.
- PRD-05 LGPD legal sign-off (gpu-ifix already ships the sub-processor disclosure + checklist from Phase 09; sign-off is the external gate).
- PRD-06 dashboard SSO / Better Auth admin hardening.
- Full `RUNBOOK-INCIDENTS.md` postmortem template (Phase 10 ships the deploy + rollback runbook only; incident response gets full treatment in Phase 11).

**Repo boundary:** all artifacts land in `gpu-ifix`. Operator HUMAN-UAT performs the actual deploy actions + cut-release on the n8n-ia-vm + GitHub against this repo. No client app repos are edited here (Phase 08/09 already handled client integration smokes + sensitive-tenant docs).

</domain>

<decisions>
## Implementation Decisions

### Host + Network
- **D-01:** Production stack runs on `n8n-ia-vm` (VM 101, 10.10.10.20) inside the existing Hetzner dedicated `ifix-prod-01`. Colocates with `ai-gateway-embed` (Infinity 24/7 on :7997) — tier-0 embed and gateway share the same host (~0 ms hop) and the same Traefik internal overlay. No new VM provisioned; zero incremental infra cost.
- **D-02:** Public ingress flows: Cloudflare → edge Traefik on `ifix-prod-01` host (162.55.92.154:443) → DNAT/route to `n8n-ia-vm:443` → internal Traefik (`traefik-internal_traefik.1`) → `gateway` / `dashboard` containers. Research must confirm exact route — the existing pattern for `portainer3.ifixtelecom.com.br` is already in `/home/pedro/projetos/pedro/infra/traefik-dynamic/worker.yml`; mirror it for the new vhosts.
- **D-03:** Traefik container labels in `gateway/docker-compose.yml` already include `traefik.http.routers.ai_gateway.tls.certresolver=letsencryptresolver` — keep that wiring; cert issuance is via the edge Traefik on the host, not the internal one.

### DNS
- **D-04:** Production hostnames: `ai-gateway.converse-ai.app` (gateway) + `ai-dashboard.converse-ai.app` (dashboard). Zone `converse-ai.app` (CF zone id `0e779b74b86957bdb628d646dbf33978`). CF DNS API token already available in `~/.claude/CLAUDE.md`; plans create both records via `POST /zones/{zone_id}/dns_records` (A → 162.55.92.154, proxied=OFF for LE HTTP-01 challenge, TTL 300).

### Data Layer
- **D-05:** Postgres = same DO managed instance used by dev (`db-grupoifix-do-user-7520351-0.j.db.ondigitalocean.com:25060`), **new schema `ai_gateway_prod`**. Migration runs on boot via `AI_GATEWAY_MIGRATE_ON_BOOT=true` for the first deploy, then flipped to `false` after the first successful deploy (per dev `gateway/docker-compose.yml` comment). Trusted-source whitelisting already covers `162.55.92.154` (the dedicated public IP) — confirm during research whether traffic from VM 101 egresses NAT through that IP (it should — same Proxmox host).
- **D-06:** Dashboard owns its own database (separate from gateway's `ai_gateway_prod`) per existing pattern in `gateway/docker-compose.yml` (`DASHBOARD_DATABASE_URL` is `ai_dashboard_prod` schema, separate from `AI_GATEWAY_PG_DSN`). Research confirms exact dashboard schema name conventions vs the dev stack.
- **D-07:** Redis = new dedicated container `redis-gateway-prod` on `n8n-ia-vm`. Password set via Portainer secret / `.env` (not anonymous like dev `infra-redis-1`). Persistence optional — gateway state in Redis is ephemeral (rate-limit counters, breaker observations, force-override TTL keys, `gw:primary:state` mirror). No AOF/RDB required; restart loses ephemeral state and reconstructs within ~30 s of observation cycles. Reject reusing `n8n_redis` (keyspace collisions risk).

### Secrets + Upstream Keys
- **D-08:** Upstream provider keys (OpenRouter / OpenAI / Vast.ai) **reused from dev** — operator decision, accepted trade-off. Implication: spend tracking will mix dev + prod traffic; revoking a key affects both environments. Documented as known limitation to revisit when billing per-env separation becomes a hard requirement (Phase 11 likely).
- **D-09:** Per-tenant API keys are created fresh in prod via `gatewayctl key create` (separate keyspace because `tenants` + `api_keys` rows live in `ai_gateway_prod` schema, isolated from dev). Operator runs the provisioning seed script (extension of `scripts/integration-smoke/provision-tenants.sh` from Phase 08/09) targeting the prod gateway during HUMAN-UAT.
- **D-10:** Secrets storage method = `.env` file at `/opt/ai-gateway-prod/.env` (mode 600, owner pedro). Same pattern as dev stack. Not committed to git. Plans produce `.env.example` documenting required keys; operator copies + populates real values during HUMAN-UAT.

### Deploy Method + Branch
- **D-11:** Deploy method = **direct `docker compose` operator-managed** at `/opt/ai-gateway-prod/` on `n8n-ia-vm` — same operational pattern as dev (`/opt/ai-gateway-dev/`). NOT Portainer Repository / NOT ArgoCD. Rationale: dev stack already proves the pattern; team is already familiar; debugging path is shorter (no Portainer abstraction layer). Trade-off documented: no GitOps trail, no webhook auto-deploy — operator runs `docker compose pull && docker compose up -d` to roll forward; rollback uses pinned image tags.
- **D-12:** Branch strategy = promote `develop` → `main` as part of Phase 10. GHA workflow updated to build `:main` and `:latest` and `:v1.0.0` image tags from `main`; `:latest-dev` continues to build from `develop`. Prod stack pins `:v1.0.0` (not floating `:main`) so rollback is to a previous tag, not a "previous SHA from a moving branch".
- **D-13:** First cut release = `v1.0.0`. Bound to commit on `main` after develop→main fast-forward / merge. Tag is annotated, signed if GPG configured.

### Monitoring + Observability
- **D-14:** Sentry = **new project `ifix-ai-gateway-prod`** in the existing Ifix Sentry org. Separate DSN from dev. Release tag = git tag (`v1.0.0` …). Environment tag = `production`. Wired via `SENTRY_DSN` env var (already wired in dev — `gateway/docker-compose.yml` line 47).
- **D-15:** Prometheus `/metrics` endpoint already exposed by gateway (OBS-02, Phase 7). Production stack does not bring up its own Prometheus scraper in Phase 10 — that is Phase 11 hardening (PRD-04 full runbook). For Phase 10, the dashboard's own `/admin/metrics` JSON + Sentry are sufficient for golden-path validation.

### Phase Scope + UAT Pattern
- **D-16:** Phase 10 scope = **DEPLOY ONLY**. Production-hardening requirements (PRD-01 load, PRD-02/03 chaos, PRD-05 LGPD sign-off, PRD-06 dashboard SSO, PRD-04 full incident runbook) split into a follow-on **Phase 11: prod-hardening**, to be created when Phase 10 closes. REQUIREMENTS.md traceability table will need to be updated to remap those PRD-* rows from Phase 10 → Phase 11.
- **D-17:** PRD-07 (DNS + TLS) is in-scope here — first deploy must have public DNS + TLS to be reachable.
- **D-18:** PRD-04 partial here — Phase 10 ships `RUNBOOK-DEPLOY.md` (bring-up + rollback + cut-release procedure). Full incident-response runbook (detection → diagnosis → postmortem) ships in Phase 11.
- **D-19:** UAT pattern = autonomous plans handle setup/migration/deploy/smoke; final plan = **operator HUMAN-UAT.md** (`autonomous: false`) — operator walks the deploy checklist, executes the cut-release, runs per-tenant golden-path smokes against `ai-gateway.converse-ai.app`, and cascade-closes Phase 02/03/04/05 VERIFICATION.md live-UAT deferrals (gateway is now live → those `human_needed` / `passed_partial` rows can flip to `passed`).
- **D-20:** Cascade VERIFICATION close happens **inside the HUMAN-UAT plan**, one small commit per phase closed, with positive-assertion grep (same pattern Phase 06.9 used to close Phase 02/03/05 — see WARNING-5 in Phase 06.9 closeout).

### Claude's Discretion
- Exact compose stack file split (single `docker-compose.yml` with all services vs separate files for gateway/dashboard/redis) — research chooses based on Swarm vs plain compose decision (n8n-ia-vm runs Swarm but `/opt/ai-gateway-dev/` is plain compose — confirm prod target mode).
- Whether to use Traefik labels on the dashboard for a redirect from `dashboard.converse-ai.app` → `/admin` of the gateway, or keep them as independent vhosts.
- Specific Redis password generator + storage path (e.g., `openssl rand -hex 24` written to `/opt/ai-gateway-prod/.env`).
- Whether to enable Postgres `connection_pooler_mode=transaction` on the DO side for the prod DSN — Phase 7 / Phase 4 didn't decide; planner verifies based on `pg_stat_activity` from dev.
- Choice of cutover sequence (in-place rename `/opt/ai-gateway-dev/` → `/opt/ai-gateway-prod/` vs net-new install with dev still running) — recommendation: net-new install on `n8n-ia-vm` (different VM), leave `/opt/ai-gateway-dev/` on `vps-ifix-vm` untouched as fallback during stabilization.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Project + Roadmap
- `.planning/PROJECT.md` — tier-1 stack final, primary GPU shape, LGPD sub-processor list, key decisions.
- `.planning/REQUIREMENTS.md` — INT-06 + PRD-01..PRD-07 are the mapped requirements for Phase 10; the traceability table in §Traceability needs updating when D-16 splits PRD-* to Phase 11.
- `.planning/ROADMAP.md` — Phase 10 placeholder (Goal: TBD) must be populated by `gsd:plan-phase 10`.

### Prior Phase Artifacts Phase 10 Builds On
- `.planning/phases/02-gateway-core-multi-tenant-auth/02-VERIFICATION.md` — SC-5 live deploy deferral; closes via Phase 10 cascade.
- `.planning/phases/03-resilience-fallback-chain/03-VERIFICATION.md` — SC-1 live failover deferral; closes via Phase 10 cascade.
- `.planning/phases/04-multi-tenant-quotas-billing-schedule-routing/04-VERIFICATION.md` — SC-1/SC-2/SC-4 live UAT deferral; closes via Phase 10 cascade.
- `.planning/phases/05-load-shedding-saturation-aware-routing/05-VERIFICATION.md` — SC-4 + SC-5 passed_partial; cascade close.
- `.planning/phases/06.9-openrouter-model-rewrite-per-upstream/06.9-VERIFICATION.md` — confirms the WARNING-5 positive-assertion grep pattern for cascade close.
- `.planning/phases/08-client-integration-converseai-chat-ifix/08-CONTEXT.md` + Phase 08 plans — `scripts/integration-smoke/` + `provision-tenants.sh` foundation reused in Phase 10 HUMAN-UAT.
- `.planning/phases/09-client-integration-sensitive-tenants-telefonia-cobran-as-campanhas-voice-api/09-CONTEXT.md` — LGPD sub-processor disclosure (already shipped; Phase 10 references but does not reauthor).

### Code + Infra Anchors
- `gateway/docker-compose.yml` — Swarm-mode stack template with Traefik labels, gateway + dashboard service blocks, env var contract. Production stack starts here.
- `gateway/Dockerfile` — gateway image build.
- `dashboard/Dockerfile` — dashboard image build.
- `gateway/docs/RUNBOOK-FAILOVER.md`, `RUNBOOK-PRIMARY-POD.md`, `RUNBOOK-EMERGENCY-POD.md`, `RUNBOOK-OBSERVABILITY-ALERTING.md`, `RUNBOOK-QUOTAS-BILLING.md`, `RUNBOOK-CLIENT-INTEGRATION.md`, `RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md` — existing runbook set; `RUNBOOK-DEPLOY.md` from this phase joins them.
- `gateway/docs/LGPD-SUBPROCESSORS.md` + `LGPD-REVIEW-CHECKLIST.md` — Phase 09 artifacts referenced by Phase 11 LGPD gate.
- `/home/pedro/.claude/CLAUDE.md` — Hetzner topology, SSH aliases, CF DNS API token (zone IDs), MinIO + Sentry context.
- `/home/pedro/projetos/pedro/infra/traefik-dynamic/worker.yml` — pattern for routing external hostnames through edge Traefik → worker-vm; mirror for n8n-ia-vm ingress.

### External Configs
- DO Postgres console — Trusted Sources must include 162.55.92.154 (already does, per CLAUDE.md). Schema `ai_gateway_prod` must be created (manually or via plan) before `goose up` runs.
- Cloudflare DNS — zone `converse-ai.app` (id `0e779b74b86957bdb628d646dbf33978`); 2 new A records.
- Sentry org "Ifix" — new project `ifix-ai-gateway-prod` (operator creates; DSN goes into `.env`).
- GitHub `IfixTelecom/gpu-ifix` — develop→main merge + tag `v1.0.0` + GHA workflow updates for new tag pattern.

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- `gateway/docker-compose.yml` is Swarm-mode (uses `deploy:` block + `mode: replicated`). The dev stack runs plain compose. Research must reconcile: does prod adopt the Swarm-mode file as-is (n8n-ia-vm IS in Swarm — `traefik-internal_traefik.1` confirms), or fork a plain-compose variant matching dev's operational model? Per D-11, prod is operator-managed direct compose, so the Swarm `deploy:` block is informative but the prod compose file may need simplification (resource limits, restart policy → plain compose equivalents).
- `gateway/docs/RUNBOOK-*.md` family (7 runbooks already shipped Phase 02–09). `RUNBOOK-DEPLOY.md` joins the set; mirror their structure (Triggers → Preconditions → Steps → Verification → Rollback).
- `scripts/integration-smoke/provision-tenants.sh` (Phase 08/09) — already idempotent gatewayctl wrapper for tenant creation. HUMAN-UAT reuses it pointed at prod gateway DSN.
- `scripts/integration-smoke/smoke-converseai.py`, `smoke-chat-ifix.py` (Phase 08), the sensitive smoke (Phase 09) — re-runnable contract smokes; HUMAN-UAT executes them against prod once env-var-switched.
- `gatewayctl` CLI binary (built alongside gateway in `cmd/gatewayctl/`) is the operator surface for tenant + key + model-alias + breaker management in prod.

### Established Patterns
- HUMAN-UAT pattern (`autonomous: false`) for live-validation steps — used by Phase 03/04/06/06.6/06.7/06.8/06.9/08/09; Phase 10 follows it. Phase 06.9 added cascade-close (`-CASCADE-CLOSE-VERIFY.md` positive-assertion grep) — Phase 10 inherits.
- `AI_GATEWAY_MIGRATE_ON_BOOT=true` first-deploy flip-off pattern (commented in compose file).
- `.env` operator-managed per-instance secrets (D-06 from Phase 06.9 made env-override-wins permanent + coequal with schema rows).
- Direct `docker compose` operator-managed at `/opt/ai-gateway-*/` (existing dev pattern documented in CLAUDE.md "OpenRouter token + stack location" memory).
- Edge Traefik on `ifix-prod-01` host routes external hostnames to internal VM Traefiks via the `infra/traefik-dynamic/*.yml` files.

### Integration Points
- Gateway compose ↔ DO Postgres (TCP 25060) — egress NAT through 162.55.92.154 host IP. Trusted Sources already cover this IP.
- Gateway compose ↔ `ai-gateway-embed` Infinity (n8n-ia-vm:7997) — local TCP on the same VM (UPSTREAM_EMBED_URL=http://10.10.10.20:7997 or via Docker network alias). Tier-0 embed path.
- Gateway compose ↔ Vast.ai primary pod (when active) — egress through host IP; same key as dev.
- Gateway compose ↔ OpenRouter / OpenAI (tier-1 fallback) — egress through host IP.
- Edge Traefik (`/home/pedro/projetos/pedro/infra/traefik-dynamic/*.yml`) ↔ n8n-ia-vm:443 Traefik internal ↔ gateway/dashboard containers.
- Cloudflare DNS ↔ 162.55.92.154 ↔ edge Traefik ↔ ingress chain above.
- GHA workflows (`.github/workflows/`) ↔ ghcr.io image tags ↔ `docker compose pull` on n8n-ia-vm.
- New Sentry project ↔ gateway + dashboard containers via `SENTRY_DSN`.

### Capacity Sanity Check
- n8n-ia-vm = 6c / 16G / 40G. Current workload: n8n cluster (workers + webhooks + editor + redis + postgres pgvector) + rabbitmq + Infinity embed + Traefik internal + Portainer agent. Adding gateway (2 cpu / 1G limit per compose) + dashboard (1 cpu / 512M) + redis-gateway-prod (~200M) = ~3.7 cpu / 1.7G additional ceiling. Research confirms there is headroom; if not, scale VM (qm set 101 -cores 8 -memory 24576) before deploy.

</code_context>

<specifics>
## Specific Ideas

- Production hostnames are `ai-gateway.converse-ai.app` + `ai-dashboard.converse-ai.app` (zone `converse-ai.app`, controlled via the CF token in `~/.claude/CLAUDE.md`).
- Production tag = `v1.0.0` (first GA release; the deploy phase doubles as the release-cut phase).
- `.env` file path on the prod host = `/opt/ai-gateway-prod/.env` (mirror of dev `/opt/ai-gateway-dev/.env` operator-managed convention).
- Postgres prod schema name = `ai_gateway_prod` (paired with dashboard's `ai_dashboard_prod`, parallel to dev `ai_gateway` / `ai_dashboard`).
- Sentry prod project name = `ifix-ai-gateway-prod`.
- Cascade-close set when HUMAN-UAT passes: Phase 02 SC-5, Phase 03 SC-1, Phase 04 SC-1/SC-2/SC-4, Phase 05 SC-4 + SC-5. Each closed in its own small commit using the WARNING-5 positive-assertion grep pattern from Phase 06.9.

</specifics>

<deferred>
## Deferred Ideas

- **PRD-01 production load test** (3-tenant real-traffic baseline + P95 + capacity) → **Phase 11: prod-hardening**.
- **PRD-02 chaos test — kill primary pod under load** → Phase 11.
- **PRD-03 chaos test — OpenRouter degraded + sensitive-tenant policy proof** → Phase 11.
- **PRD-04 full incident-response runbook** (detection → diagnosis → postmortem template) → Phase 11. (Phase 10 ships a slim `RUNBOOK-DEPLOY.md` only.)
- **PRD-05 LGPD legal sign-off** → operator/external gate; gpu-ifix already ships disclosure + checklist in Phase 09. Tracked separately; not a code deliverable.
- **PRD-06 dashboard SSO / Better Auth admin hardening** → Phase 11.
- **Per-env upstream provider key separation** (new OpenRouter + OpenAI keys with `env=prod` project tag) — accepted trade-off D-08 for now; revisit when billing-per-env separation becomes required.
- **Portainer Repository + webhook auto-deploy migration** — explicitly chose direct compose D-11; can revisit if operator burden grows.
- **CI tag for `v1.0.x` patch releases** — branch + release strategy beyond `v1.0.0` is a Phase 11+ concern; this phase only cuts the first tag.

</deferred>

---

*Phase: 10-prod-deploy-ai-gateway*
*Context gathered: 2026-05-25*
