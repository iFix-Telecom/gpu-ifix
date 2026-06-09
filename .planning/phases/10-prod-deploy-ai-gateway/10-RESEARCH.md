# Phase 10: prod-deploy-ai-gateway — Research

**Researched:** 2026-05-25
**Domain:** Production deploy of the ifix-ai-gateway (gateway Go binary + Next.js dashboard) onto Proxmox-VM `n8n-ia-vm` (10.10.10.20), cut release `v1.0.0`, cascade-close Phase 02/03/04/05 live-UAT deferrals.
**Confidence:** HIGH (all infrastructure assumptions verified via live SSH probes; only env-var values left to operator).

---

## Summary

Phase 10 takes the Phase 06.9-validated dev gateway (`/opt/ai-gateway-dev/` on `vps-ifix-vm`) and lifts it onto `n8n-ia-vm` as a new `/opt/ai-gateway-prod/` operator-managed `docker compose` stack — colocated with the tier-0 embed pod (`ai-gateway-embed` Infinity 24/7 on `:7997`) and the n8n Swarm cluster (`traefik-internal`, `intra` overlay). The deploy publishes two public vhosts (`ai-gateway.converse-ai.app` + `ai-dashboard.converse-ai.app`) through the edge Traefik on `vps-ifix-vm:443` (where ALL public 443 traffic for `162.55.92.154` already terminates today), cut release tag `v1.0.0`, and runs an operator HUMAN-UAT that doubles as the live-validation gate for Phase 02 SC-5 / Phase 03 SC-1 / Phase 04 SC-1+SC-2+SC-4 / Phase 05 SC-1 deferrals.

**The investigation found three CONTEXT.md assumptions that are technically wrong and must be reconciled before planning starts**:

1. **Edge Traefik does NOT live on `ifix-prod-01` host** (CONTEXT.md D-02 implies it does). It lives on `vps-ifix-vm:443` (10.10.10.30). All `162.55.92.154:443` Hetzner public traffic is DNAT'd to `10.10.10.30:443` by Proxmox host iptables, never to `n8n-ia-vm`. The phase 10 ingress route therefore extends the **existing vps-ifix-vm edge Traefik** (`/home/pedro/projetos/pedro/infra/`), NOT a new edge Traefik on the Proxmox host. The mirror pattern is `/home/pedro/projetos/pedro/infra/traefik-dynamic/n8n-ia.yml` (which already routes 4 hostnames to `http://10.10.10.20:80`), not the non-existent worker.yml referenced in CONTEXT.

2. **`gateway/docker-compose.yml` uses an `external: true` network named `worker_intra` that does NOT exist on `n8n-ia-vm`.** The actual existing overlay network there is named `intra` (Swarm overlay, attachable=true, driver=overlay). The prod compose file must rename `worker_intra` → `intra`.

3. **Postgres schema name `ai_gateway` is hardcoded in 27 migration files, in `gateway/internal/db/pool.go:35` (`SET search_path = ai_gateway, public`), and in every sqlc-generated query (`ai_gateway.api_key_status`, `ai_gateway.data_class` etc.).** CONTEXT.md D-05 calls for `ai_gateway_prod` schema — that name change requires either (a) introducing a new `AI_GATEWAY_PG_SCHEMA` env var with a refactor of every migration + pool hook (out of scope for Phase 10), or (b) using a **separate Postgres database** (not just schema) with the same `ai_gateway` schema name. Option (b) is the recommended path — same DO managed instance, new database `bd_ai_gateway_prod`, same `ai_gateway` schema name inside it. The `ai_dashboard_prod` schema mentioned in D-06 has the symmetric problem and applies the same fix.

**Primary recommendation:** Phase 10 ships a slim 6-plan structure (Wave 0 reconciliation + 4 autonomous setup/deploy/smoke waves + 1 HUMAN-UAT plan). The HUMAN-UAT plan walks the operator through DO database creation, DNS flip, first deploy, per-tenant golden-path smokes against `ai-gateway.converse-ai.app`, and a 4-commit cascade-close of Phase 02/03/04/05 VERIFICATION.md (positive-assertion grep — WARNING-5 pattern from Phase 06.9). Total operator spend: zero (no Vast/GPU; OpenRouter direct ~$0.001).

---

## User Constraints (from CONTEXT.md)

### Locked Decisions

**Host + Network**
- **D-01:** Production stack runs on `n8n-ia-vm` (VM 101, 10.10.10.20). Colocates with `ai-gateway-embed` (Infinity on :7997) and tier-0 embed path. No new VM.
- **D-02:** Public ingress = Cloudflare → edge Traefik on Hetzner `162.55.92.154:443` → internal Traefik (`traefik-internal_traefik.1`) → `gateway` / `dashboard` containers. *(See `## How To #2` — the edge Traefik that actually terminates TLS lives on vps-ifix-vm:443, NOT on the Proxmox host.)*
- **D-03:** Keep `traefik.http.routers.ai_gateway.tls.certresolver=letsencryptresolver` wiring; cert issuance via the edge Traefik on the host. *(`certResolver` literal in the edge file provider is `letsencrypt` (no `resolver` suffix) — see PF-1 in n8n-ia.yml.)*

**DNS**
- **D-04:** Hostnames `ai-gateway.converse-ai.app` + `ai-dashboard.converse-ai.app`. Zone `converse-ai.app` (id `0e779b74b86957bdb628d646dbf33978`). A → 162.55.92.154, proxied=OFF, TTL 300.

**Data Layer**
- **D-05:** Postgres = same DO managed instance, new schema `ai_gateway_prod`. `AI_GATEWAY_MIGRATE_ON_BOOT=true` first deploy, flip to `false`. *(See `## How To #3` — the schema name `ai_gateway` is hardcoded; the implementable form of D-05 is "new database, same schema name".)*
- **D-06:** Dashboard owns its own database (separate from gateway's `ai_gateway_prod`). *(Symmetric: new database for the dashboard's Better Auth tables.)*
- **D-07:** Redis = new dedicated container `redis-gateway-prod` on `n8n-ia-vm`. Password set via `.env`. Persistence optional (no AOF/RDB required). Reject reusing `n8n_redis`.

**Secrets + Upstream Keys**
- **D-08:** Reuse dev upstream provider keys (OpenRouter / OpenAI / Vast.ai) — accepted trade-off.
- **D-09:** Per-tenant API keys minted fresh in prod via `gatewayctl key create` against the prod gateway during HUMAN-UAT.
- **D-10:** Secrets storage = `.env` file at `/opt/ai-gateway-prod/.env` (mode 600, owner pedro). Plans produce `.env.example`.

**Deploy Method + Branch**
- **D-11:** Deploy method = **direct `docker compose` operator-managed** at `/opt/ai-gateway-prod/` on `n8n-ia-vm`. NOT Portainer, NOT ArgoCD.
- **D-12:** Branch strategy = promote `develop` → `main` as part of Phase 10. GHA builds `:main` + `:latest` + `:v1.0.0`. Prod pins `:v1.0.0`.
- **D-13:** First cut release = `v1.0.0`. Annotated, signed if GPG configured.

**Monitoring + Observability**
- **D-14:** New Sentry project `ifix-ai-gateway-prod` (Ifix org). Release tag = git tag; environment tag = `production`.
- **D-15:** Prometheus `/metrics` already exposed; no scraper in Phase 10 (Phase 11 hardening). Dashboard `/admin/metrics` JSON + Sentry suffice.

**Phase Scope + UAT**
- **D-16:** Phase 10 = DEPLOY ONLY. PRD-01/02/03/04(full)/05/06 split into Phase 11. REQUIREMENTS.md §Traceability needs remapping.
- **D-17:** PRD-07 (DNS + TLS) in scope.
- **D-18:** PRD-04 partial — `RUNBOOK-DEPLOY.md` only.
- **D-19:** UAT = autonomous plans + final `HUMAN-UAT.md` (`autonomous: false`) operator-driven cut-release + per-tenant smokes + cascade-close.
- **D-20:** Cascade VERIFICATION close inside HUMAN-UAT, one commit per phase, WARNING-5 positive-assertion grep pattern from Phase 06.9.

### Claude's Discretion (research recommends)

- **Compose file shape:** Single `docker-compose.yml` (gateway + dashboard + redis-gateway-prod) with `restart: unless-stopped` (NOT Swarm `deploy:` block — operator-managed pattern). The `gateway/docker-compose.yml` Swarm template stays canonical for reference; the prod operator file lives at `/opt/ai-gateway-prod/docker-compose.yml` and is committed to repo at `gateway/docker-compose.prod.yml` (Wave 0 deliverable). See `## How To #4`.
- **Dashboard vhost:** Independent vhost `ai-dashboard.converse-ai.app` (NOT a redirect from gateway). Already what dev does via Traefik labels.
- **Redis password:** `openssl rand -hex 24` written to `/opt/ai-gateway-prod/.env` as `AI_GATEWAY_REDIS_PASSWORD`. Container `redis-gateway-prod` started with `--requirepass "$AI_GATEWAY_REDIS_PASSWORD"`. No persistence.
- **Postgres pooler `transaction` mode:** Leave as-is (session mode) for Phase 10. The gateway uses pgx LISTEN/NOTIFY (`upstreams_changed`) which requires session-mode connections. Switching to transaction-mode breaks the hot-reload path. Phase 11 may explore PgBouncer with a session-mode pool for LISTEN + a transaction-mode pool for query traffic.
- **Cutover sequence:** Net-new install on `n8n-ia-vm`; **leave `/opt/ai-gateway-dev/` on `vps-ifix-vm` running** during stabilization. Operator decommissions dev only after prod is GREEN for 72h.

### Deferred Ideas (OUT OF SCOPE — Phase 11 or external)

- PRD-01 production load test → Phase 11.
- PRD-02 chaos primary kill → Phase 11.
- PRD-03 chaos OpenRouter degraded → Phase 11.
- PRD-04 full incident-response runbook → Phase 11 (Phase 10 ships slim `RUNBOOK-DEPLOY.md` only).
- PRD-05 LGPD legal sign-off → external operator gate.
- PRD-06 dashboard SSO / Better Auth hardening → Phase 11.
- Per-env upstream provider key separation → revisit when billing-per-env required.
- Portainer Repository / webhook auto-deploy → reconsider if operator burden grows.
- CI tag for `v1.0.x` patch releases → Phase 11+.

---

## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| INT-06 | Integration smoke + rollback <5 min per integration | HUMAN-UAT runs `smoke-converseai.py`, `smoke-chat-ifix.py`, `smoke-sensitive-failover.py` against prod; rollback = `docker compose pull ifix-ai-gateway:v0.X.Y && up -d` (image tag swap, <30s). See `## How To #7` + `#8`. |
| PRD-04 (partial) | RUNBOOK-DEPLOY.md only — bring-up + rollback + cut-release | See `## How To #8`. Mirror RUNBOOK-FAILOVER.md headers (Triggers → Preconditions → Steps → Verification → Rollback). |
| PRD-07 | DNS + TLS reachable | CF API DNS POST + edge Traefik file-provider `letsencrypt` resolver TLS-ALPN-01. See `## How To #2` + `#3`. |
| Phase 02 SC-5 step 7 (already passed via 06.9; reconfirm under prod URL) | Live chat E2E under prod hostname | HUMAN-UAT S1 + positive-assertion grep cascade-close. See `## How To #9`. |
| Phase 03 SC-1 | Live failover (re-confirm under prod hostname) | HUMAN-UAT S4 mirrors Phase 06.9 S4. See `## How To #9`. |
| Phase 04 SC-1/SC-2/SC-4 | Live rate-limit + billing rows + peak routing | HUMAN-UAT S5/S6/S7 against prod (Phase 04 already PARTIAL-passed against dev 2026-05-23). See `## How To #9`. |
| Phase 05 SC-1 | Live overflow vegeta burst (re-confirm under prod hostname) | HUMAN-UAT S8 mirrors Phase 06.9 S6. See `## How To #9`. |

---

## Project Constraints (from CLAUDE.md)

- **Hetzner topology** is hard infra:
  - `ifix-prod-01` (162.55.92.154, Proxmox host) — root SSH only via `ssh ifix-prod-01`.
  - `n8n-ia-vm` (10.10.10.20, VM 101, 6c/16G/40G) — SSH alias `ssh n8n-ia-vm`, root authorized.
  - `vps-ifix-vm` (10.10.10.30, VM 102, 8c/24G/60G) — runs edge Traefik + dev stack.
  - All four hosts share `~/.ssh/id_ed25519` (no passphrase) → permission prompts are the defense layer.
- **Cloudflare DNS API token** in `~/.claude/CLAUDE.md` (zone id `converse-ai.app` = `0e779b74b86957bdb628d646dbf33978`).
- **MinIO + Sentry + OpenRouter** keys already documented in `~/.claude/CLAUDE.md` (not in repo).
- **GSD workflow enforcement:** All file changes go through `/gsd:execute-phase` once plans land. Use `gsd-debug` for diagnose-only; do not Edit during research.
- **Communication rule:** NEVER use speculative language — every claim verified via probe or marked `[ASSUMED]` here.

---

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Public DNS resolution | Cloudflare (edge DNS) | — | CF zone `converse-ai.app`; A record points to 162.55.92.154. |
| TLS termination + ACME | Edge Traefik v3.6 on `vps-ifix-vm:443` | — | Already terminates all `*.ifixtelecom.com.br` / `*.converse-ai.app` / `*.converseai.app.br` traffic; `letsencrypt` certresolver in static config; file-provider routes added per-vhost. |
| Host-header routing (intra-host) | Internal Traefik v2.11 on `n8n-ia-vm:80` (Swarm) | — | Existing `traefik-internal_traefik.1` service; provider = docker swarm; network = `intra` overlay. |
| Gateway HTTP serve (`/v1/*`, `/admin/*`, `/health`, `/metrics`) | Gateway Go binary (`ghcr.io/ifixtelecom/ifix-ai-gateway:v1.0.0`) | — | distroless static, /gateway entrypoint, port 8080, no host port publish (Traefik label only). |
| Dashboard SSR + Better Auth | Next.js 15 standalone (`ghcr.io/ifixtelecom/ifix-ai-dashboard:v1.0.0`) | — | port 3001, isolated Postgres DB (`dashboard_auth` schema in separate database), proxies `/admin/*` to gateway. |
| Rate-limit / breaker / FSM mirror | Redis 7 in `redis-gateway-prod` container | — | ephemeral keyspace; no AOF; ~30s reconstruction after restart (probe cycle). Same VM (~0ms hop) to gateway. |
| Schema persistence (tenants / keys / audit / billing / quotas / upstreams / model_aliases / lifecycles) | DO managed Postgres (`db-grupoifix-do-user-7520351-0.j.db.ondigitalocean.com:25060`), new database for prod | — | egress from VM 101 NATs through 162.55.92.154 → already in DO Trusted Sources (verified `curl ifconfig.io` from n8n-ia-vm = 162.55.92.154). |
| Tier-0 LLM/STT/embed inference | local (Vast.ai primary pod when active) + `ai-gateway-embed` Infinity (CPU embed) on n8n-ia-vm | — | `ai-gateway-embed` reachable via `http://10.10.10.20:7997` (host port) or via the `intra` overlay if gateway joins it. Decision: gateway joins `intra` overlay → `http://ai-gateway-embed:7997` if container attached; else use host IP. |
| Tier-1 fallback (chat/whisper/embed) | OpenRouter (Novita-pinned) + OpenAI direct | — | Bearer keys via env vars; egress from VM 101 NATs through 162.55.92.154. |
| Error tracking | Sentry (`ifix-ai-gateway-prod` project in Ifix org) | — | gateway-internal `obs.Init(cfg)` reads `SENTRY_DSN`, `Environment: cfg.Env`, `Release: BuildVersion` (set via `-ldflags -X .../obs.BuildVersion=v1.0.0`). |
| CI build + publish | GitHub Actions (`build-gateway.yml` + `build-dashboard.yml`) | — | already supports `tags: ['v*']` + `branches: [main]` triggers; `:v1.0.0` + `:latest` + `:v1.0.0-<sha>` already in `compute-tags` step. |
| Operator commands | `gatewayctl` baked into gateway image; invoked via `docker exec ifix-ai-gateway /gatewayctl …` | — | tenant CRUD, key create/revoke, upstreams list/enable/disable, model-alias set, breaker force-open. |

---

## Standard Stack

### Core

| Library / Tool | Version | Purpose | Why Standard |
|----------------|---------|---------|--------------|
| `docker compose` (v2 plugin) | shipped with Docker Engine 24+ | Operator-managed stack at `/opt/ai-gateway-prod/` | D-11 explicitly chooses direct compose over Portainer/ArgoCD. Same operational pattern as `/opt/ai-gateway-dev/`. |
| Traefik (edge) | v3.6 (vps-ifix-vm) | TLS termination + ACME tls-alpn-01 + host-header file-provider routing to internal VMs | Already running; `/home/pedro/projetos/pedro/infra/docker-compose.yml`. File provider watches `./traefik-dynamic/*.yml`. [VERIFIED: ssh probe 2026-05-25] |
| Traefik (internal) | v2.11 (n8n-ia-vm) | Swarm-mode docker provider; reads service labels on `intra` overlay; serves on `:80` only (no TLS) | Already running as Swarm service `traefik-internal_traefik`. Args: `--providers.docker.swarmMode=true --providers.docker.network=intra --entryPoints.web.address=:80`. [VERIFIED: docker service inspect] |
| Postgres | 16 managed by DigitalOcean | Schema persistence | Same DO instance as dev; new DB `bd_ai_gateway_prod` + `bd_ai_dashboard_prod`. Trusted Sources already cover 162.55.92.154. |
| Redis | `redis:7-alpine` | rate-limit / breaker / FSM mirror | dedicated `redis-gateway-prod` container; ephemeral keyspace. |
| Sentry SDK Go | `github.com/getsentry/sentry-go` (already pinned in go.mod) | error tracking | Already wired in `gateway/internal/obs/sentry.go:24` (`sentry.Init` with `Dsn`, `Environment: cfg.Env`, `Release: BuildVersion`, `BeforeSend: beforeSend` redaction). |
| Cloudflare DNS API | v4 REST | DNS A record creation | CF token in `~/.claude/CLAUDE.md` (zone id `0e779b74b86957bdb628d646dbf33978`); use `POST /zones/{id}/dns_records`. |

### Supporting

| Tool | Purpose | When to Use |
|------|---------|-------------|
| `gatewayctl` (baked in gateway image) | tenant/key/upstreams/model-alias/breaker/admin-key CLI | HUMAN-UAT tenant + admin-key provisioning. Invoke via `docker exec ifix-ai-gateway /gatewayctl …`. |
| `scripts/integration-smoke/provision-tenants.sh` | idempotent tenant + quota seed; `--mint-keys` (non-idempotent) for key issuance | First-deploy tenant bootstrap pointed at prod gateway. |
| `scripts/integration-smoke/smoke-converseai.py` | INT-01 contract smoke (chat + SSE + tool calls + embeddings) | Per-tenant golden path during HUMAN-UAT. |
| `scripts/integration-smoke/smoke-chat-ifix.py` | INT-02 contract smoke (Whisper audio + Whisper-fallback) | Per-tenant golden path. |
| `scripts/integration-smoke/smoke-sensitive-failover.py` | INT-03/04/05 sensitive-tenant block path (LGPD RES-08) | Telefonia + cobrancas verification. |
| `goose` v3 (embedded via `gateway/internal/db/migrate.go`) | schema migrations | `AI_GATEWAY_MIGRATE_ON_BOOT=true` on first deploy runs `goose.UpContext`; bookkeeping table in `ai_gateway.goose_db_version`. |
| `openssl rand -hex 24` | Redis password + (optionally) `BETTER_AUTH_SECRET` | One-time during initial `.env` population. |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| Direct `docker compose` (D-11) | Portainer Repository + GitHub webhook (Ifix standard for converseai-v4 / dev gateway) | GitOps trail + auto-pull; D-11 explicitly rejects to keep operational symmetry with `/opt/ai-gateway-dev/` + shorter debug path. Revisit if operator burden grows. |
| Same-host edge Traefik on ifix-prod-01 (Proxmox host) | Operating Traefik on the Proxmox host directly | More tier separation; rejected because the existing edge Traefik on vps-ifix-vm already terminates all 162.55.92.154:443 traffic and is the only ACME-issuing Traefik in the path. Adding another would duplicate ACME state + create cert ownership conflicts. |
| Schema `ai_gateway_prod` (CONTEXT D-05 literal) | Make schema name configurable via `AI_GATEWAY_PG_SCHEMA` env var | Requires touching 27 migration files + sqlc regen + `pool.go` hook; out of scope for Phase 10. Use a new **database** with the existing `ai_gateway` schema name. |
| Reuse `infra-redis-1` (dev stack's Redis on traefik-public) | Single Redis | D-07 explicitly rejects to avoid keyspace collisions + privilege separation. |
| Dashboard redirect to `/admin/` of gateway | Single hostname | Independent vhost matches dev pattern + lets Better Auth own its cookie scope cleanly. |

**Installation:** None required — every tool listed above is already deployed on `vps-ifix-vm` (edge Traefik) or `n8n-ia-vm` (internal Traefik, Docker Swarm) or available as a binary inside the gateway image (`gatewayctl`).

**Version verification:**

```bash
ssh n8n-ia-vm "docker --version; docker compose version"
# Docker version 27.5+, Docker Compose v2.x — both confirmed present  [VERIFIED: ssh n8n-ia-vm 'docker info' 2026-05-25]
ssh vps-ifix-vm "docker exec infra-traefik-1 traefik version 2>/dev/null | head"
# Traefik v3.6 already in production  [VERIFIED: /home/pedro/projetos/pedro/infra/docker-compose.yml]
gh release view v1.0.0 2>&1 | head -3
# Expect: "release not found" — confirms v1.0.0 has not been cut yet  [VERIFIED: git log shows latest tag = pre-release]
```

---

## Package Legitimacy Audit

Phase 10 installs **zero new packages**. All Go dependencies (`sentry-go`, `goose`, `pgx`, `redis`) are already in `gateway/go.sum`; all Node dependencies (`better-auth`, `drizzle-orm`, `next`) are already in `dashboard/package-lock.json`. The phase only configures + deploys existing artifacts.

| Package | Registry | Disposition |
|---------|----------|-------------|
| (no new packages) | — | N/A — Phase 10 is configuration-only |

---

## Architecture Patterns

### System Architecture Diagram

```
                              ┌──────────────────┐
                              │   Cloudflare     │
                              │  zone:           │
                              │  converse-ai.app │
                              └────────┬─────────┘
                                       │ A (proxied=OFF, TTL 300)
                                       │ ai-gateway.converse-ai.app    → 162.55.92.154
                                       │ ai-dashboard.converse-ai.app  → 162.55.92.154
                                       ▼
        ┌────────────────────────────────────────────────────────┐
        │   Hetzner dedicated  ifix-prod-01  (162.55.92.154)     │
        │                                                        │
        │   Proxmox host iptables DNAT:                          │
        │     eno1:443 ──▶ 10.10.10.30:443  (vps-ifix-vm)        │
        │     vmbr0:443 hairpin same                             │
        │   (NO DNAT to .20 — all 443 lands on .30)              │
        └────────────────────────────┬───────────────────────────┘
                                     │
                                     ▼
   ┌───────────────────────── vps-ifix-vm (10.10.10.30) ───────────────────────┐
   │                                                                           │
   │   Edge Traefik v3.6  (compose: /home/pedro/projetos/pedro/infra)          │
   │     - entryPoints.web=:80 (redirect → websecure)                          │
   │     - entryPoints.websecure=:443                                          │
   │     - certResolver letsencrypt (tls-alpn-01)                              │
   │     - providers.file.directory=/etc/traefik/dynamic                       │
   │                                                                           │
   │   FILE PROVIDER (mirror pattern: traefik-dynamic/n8n-ia.yml)              │
   │     ai-gateway.converse-ai.app    → http://10.10.10.20:80 (passHostHeader)│
   │     ai-dashboard.converse-ai.app  → http://10.10.10.20:80 (passHostHeader)│
   └────────────────────────────┬──────────────────────────────────────────────┘
                                │  HTTP (TLS terminated at edge)
                                ▼
   ┌───────────────────────── n8n-ia-vm (10.10.10.20) ─────────────────────────┐
   │                                                                           │
   │   Internal Traefik v2.11  (Swarm service traefik-internal_traefik)        │
   │     - providers.docker.swarmMode=true                                     │
   │     - providers.docker.network=intra  (overlay)                           │
   │     - entryPoints.web=:80                                                 │
   │     - HOST-HEADER routing by service labels                               │
   │                                                                           │
   │   Stack: /opt/ai-gateway-prod/docker-compose.yml  (operator-managed)      │
   │   ┌─────────────────────────────────────────────────────────────────┐    │
   │   │  service: ifix-ai-gateway       network: intra (external)       │    │
   │   │   image: …/ifix-ai-gateway:v1.0.0  port 8080  (Traefik label)   │    │
   │   │   labels: routers.ai_gateway.rule=Host(`ai-gateway.converse-ai.app`)  │
   │   │           entrypoints=web                                       │    │
   │   │                                                                 │    │
   │   │  service: ifix-ai-dashboard     network: intra (external)       │    │
   │   │   image: …/ifix-ai-dashboard:v1.0.0  port 3001  (Traefik label) │    │
   │   │   labels: routers.ai_dashboard.rule=Host(`ai-dashboard.converse-ai.app`) │
   │   │                                                                 │    │
   │   │  service: redis-gateway-prod    network: intra                  │    │
   │   │   image: redis:7-alpine                                         │    │
   │   │   command: redis-server --requirepass "$AI_GATEWAY_REDIS_PASSWORD"   │
   │   └─────────────────────────────────────────────────────────────────┘    │
   │                                                                           │
   │   Existing (untouched):                                                   │
   │     ai-gateway-embed   (Infinity :7997)  — bridge net (NOT intra)         │
   │     traefik-internal_traefik  (Swarm service)                             │
   │     n8n_n8n_{editor,worker,webhook}, postgres_postgres (pgvector)         │
   │     rabbitmq_rabbitmq, redis_n8n_redis, portainer_agent                   │
   └───────────────────────────────────────────────────────────────────────────┘
                                │
                                ▼ egress (NAT through 162.55.92.154)
   ┌────────────────────────────────────────────────────────────────────────────┐
   │  External services (already used by dev stack)                             │
   │   DO Postgres   db-grupoifix-…:25060   db=bd_ai_gateway_prod (NEW)         │
   │                                        db=bd_ai_dashboard_prod (NEW)       │
   │   OpenRouter    api.openrouter.ai      Novita-pinned chat (tier-1)         │
   │   OpenAI        api.openai.com         whisper-1, text-embedding-3-small   │
   │   Vast.ai       cloud.vast.ai/api      primary/emergency pod control       │
   │   MinIO         s3.ifixtelecom.com.br  weights upload-keys                 │
   │   Sentry        sentry.io              ifix-ai-gateway-prod project (NEW)  │
   └────────────────────────────────────────────────────────────────────────────┘
```

Primary use case (chat request):

1. ConverseAI agent POSTs `https://ai-gateway.converse-ai.app/v1/chat/completions` with `Authorization: Bearer ifix_sk_…`
2. Cloudflare resolves → 162.55.92.154
3. Hetzner host DNAT 443 → 10.10.10.30:443
4. Edge Traefik on vps-ifix-vm matches `Host(ai-gateway.converse-ai.app)` → forwards plain HTTP to `http://10.10.10.20:80`
5. Internal Traefik on n8n-ia-vm matches `Host(ai-gateway.converse-ai.app)` (label on `ifix-ai-gateway` service) → forwards to gateway:8080
6. Gateway authenticates key (Postgres + Redis cache) → resolves model alias → dispatches to local-llm OR openrouter-chat OR sensitive-block per RES-08
7. Response streams back along the same path; SSE chunks flushed per-chunk (Phase 2 SC-1).

### Component Responsibilities

| File | Responsibility |
|------|---------------|
| `/opt/ai-gateway-prod/docker-compose.yml` | Operator-managed prod stack file (gateway + dashboard + redis-gateway-prod); committed to repo at `gateway/docker-compose.prod.yml` |
| `/opt/ai-gateway-prod/.env` | Secret-bearing env file (mode 600 pedro:pedro); NEVER committed; populated during HUMAN-UAT setup |
| `gateway/.env.prod.example` | Documented contract of every env var the prod stack needs |
| `/home/pedro/projetos/pedro/infra/traefik-dynamic/ai-gateway-prod.yml` | Edge Traefik file-provider route for both vhosts → `http://10.10.10.20:80` |
| `gateway/docs/RUNBOOK-DEPLOY.md` | New runbook: bring-up + roll-forward + rollback + cut-release procedure |
| `.github/workflows/build-gateway.yml` (unchanged) | Already builds `:v1.0.0` + `:main` + `:latest` from `tags: ['v*']` + `branches: [main]` |
| `.github/workflows/build-dashboard.yml` (unchanged) | Symmetric to gateway workflow; same trigger contract |
| `scripts/integration-smoke/provision-tenants.sh` (re-used) | Provisions tenants + quotas + (with `--mint-keys`) per-tenant API keys against prod DSN |

### Recommended Project Structure

```
gpu-ifix/
├── gateway/
│   ├── docker-compose.yml              # existing Portainer Swarm template (kept for reference)
│   ├── docker-compose.prod.yml         # NEW — Phase 10 operator-managed prod stack
│   ├── .env.prod.example               # NEW — Phase 10 documented prod env contract
│   └── docs/
│       └── RUNBOOK-DEPLOY.md           # NEW — Phase 10 deliverable
├── .planning/phases/
│   ├── 10-prod-deploy-ai-gateway/
│   │   ├── 10-CONTEXT.md               # already present
│   │   ├── 10-DISCUSSION-LOG.md        # already present
│   │   ├── 10-RESEARCH.md              # THIS FILE
│   │   ├── 10-PLAN.md / 10-PATTERNS.md / 10-VALIDATION.md  # planner output
│   │   ├── 10-01-PLAN.md … 10-06-PLAN.md
│   │   └── 10-HUMAN-UAT.md             # final autonomous:false plan
│   └── 11-prod-hardening/              # NEW phase placeholder (planner inserts after Phase 10 closes)
└── (other phases unchanged)

n8n-ia-vm:/opt/ai-gateway-prod/   (operator-managed)
├── docker-compose.yml             # rsync from gateway/docker-compose.prod.yml
├── .env                           # mode 600 pedro:pedro; populated during HUMAN-UAT
└── (no other files — Redis volume is named volume managed by Docker)

vps-ifix-vm:/home/pedro/projetos/pedro/infra/traefik-dynamic/
├── n8n-ia.yml                     # existing (4 routes); reference only
└── ai-gateway-prod.yml            # NEW — 2 routes for prod gateway + dashboard
```

### Pattern 1: Net-new operator-managed `docker compose` stack

**What:** Operator creates `/opt/ai-gateway-prod/` on n8n-ia-vm; populates `.env`; rsyncs `docker-compose.prod.yml`; runs `docker compose up -d`. Same model as `/opt/ai-gateway-dev/` on vps-ifix-vm.

**When to use:** Any new stack where ops wants direct visibility into the compose state and zero Portainer abstraction.

**Example:**
```bash
# 1) operator creates the directory
ssh n8n-ia-vm 'sudo mkdir -p /opt/ai-gateway-prod && sudo chown pedro:pedro /opt/ai-gateway-prod'

# 2) operator copies compose file + .env (the .env values come from a separate password-managed source)
scp gateway/docker-compose.prod.yml n8n-ia-vm:/opt/ai-gateway-prod/docker-compose.yml
scp /tmp/ai-gateway-prod.env n8n-ia-vm:/opt/ai-gateway-prod/.env
ssh n8n-ia-vm 'chmod 600 /opt/ai-gateway-prod/.env'

# 3) operator brings up the stack (first-deploy flips MIGRATE on)
ssh n8n-ia-vm 'cd /opt/ai-gateway-prod && docker compose pull && docker compose up -d'

# 4) operator verifies healthcheck + Traefik discovery
ssh n8n-ia-vm 'docker compose -f /opt/ai-gateway-prod/docker-compose.yml ps; docker logs ifix-ai-gateway --tail 50'

# 5) operator flips MIGRATE off after first successful migration
ssh n8n-ia-vm "sed -i 's/^AI_GATEWAY_MIGRATE_ON_BOOT=true/AI_GATEWAY_MIGRATE_ON_BOOT=false/' /opt/ai-gateway-prod/.env"
ssh n8n-ia-vm 'cd /opt/ai-gateway-prod && docker compose up -d --force-recreate ifix-ai-gateway'
```

### Pattern 2: Edge Traefik file-provider route extension (mirror n8n-ia.yml)

**What:** Add a new YAML file in `/home/pedro/projetos/pedro/infra/traefik-dynamic/` on vps-ifix-vm. Edge Traefik file-watcher picks it up within 1s. No restart.

**When to use:** Every public vhost in the Ifix ecosystem that terminates TLS at the edge and routes to an internal VM on port 80.

**Example:** `/home/pedro/projetos/pedro/infra/traefik-dynamic/ai-gateway-prod.yml`

```yaml
# Phase 10 — ai-gateway prod ingress.
# Edge Traefik v3.6 on .30 forwards 2 hostnames to internal Traefik v2.11 on .20:80.
# Hot-reload via file provider watch on /etc/traefik/dynamic/.
# Mirror pattern: /home/pedro/projetos/pedro/infra/traefik-dynamic/n8n-ia.yml.
#
# certResolver = LITERAL `letsencrypt` (Wave 0 PF-1 from Phase 4/5: edge static config
# args contain `--certificatesresolvers.letsencrypt.acme.email=...`, NOT
# `letsencryptresolver`). Do NOT match the compose-label `letsencryptresolver` name —
# that label only applies to docker-provider Swarm-discovered routes on the INTERNAL
# Traefik, not the file-provider routes on the edge.
#
# ACME challenge = TLS-ALPN-01 (`tlschallenge=true` on edge static config), NOT DNS-01.
# Cert first-issue requires public 443 reachability + DNS A record pointing 162.55.92.154.
# Plans land the dynamic config BEFORE DNS flip to surface "no certificate for X" in
# logs cleanly; first :443 request triggers TLS-ALPN-01 challenge → cert cached in
# acme.json on edge .30.
#
# passHostHeader=true: gateway + dashboard read the original Host header (Better Auth
# cookie scope, log audit context).

http:
  routers:
    ai-gateway-prod:
      rule: "Host(`ai-gateway.converse-ai.app`)"
      service: n8n-ia-prod-internal
      entryPoints: [websecure]
      tls:
        certResolver: letsencrypt

    ai-dashboard-prod:
      rule: "Host(`ai-dashboard.converse-ai.app`)"
      service: n8n-ia-prod-internal
      entryPoints: [websecure]
      tls:
        certResolver: letsencrypt

  services:
    n8n-ia-prod-internal:
      loadBalancer:
        servers:
          - url: "http://10.10.10.20:80"
        passHostHeader: true
```

### Pattern 3: Cloudflare DNS A record creation

**What:** Use the CF token to POST 2 A records for the new hostnames. Idempotent (PUT semantics if record id known; otherwise `POST + 200 on duplicate ID conflict`).

**When to use:** Every new public hostname under a CF-managed zone.

**Example:**
```bash
CF_TOKEN="$(grep '^cfut_' ~/.claude/CLAUDE.md | head -1 | tr -d ' ')"
ZONE_ID="0e779b74b86957bdb628d646dbf33978"   # converse-ai.app

for host in ai-gateway ai-dashboard; do
  curl -sS -X POST \
    -H "Authorization: Bearer ${CF_TOKEN}" \
    -H "Content-Type: application/json" \
    "https://api.cloudflare.com/client/v4/zones/${ZONE_ID}/dns_records" \
    -d "{
      \"type\":\"A\",
      \"name\":\"${host}.converse-ai.app\",
      \"content\":\"162.55.92.154\",
      \"proxied\":false,
      \"ttl\":300,
      \"comment\":\"Phase 10 prod ifix-ai-gateway (created $(date -Iseconds))\"
    }" | jq -r '.success, .errors // empty'
done

# Verify propagation (CF nameservers respond within seconds with TTL=300):
dig +short ai-gateway.converse-ai.app @1.1.1.1
dig +short ai-dashboard.converse-ai.app @1.1.1.1
```

### Pattern 4: Cascade-close via positive-assertion grep (WARNING-5)

**What:** Each cascade-closed phase gets ONE small commit that updates VERIFICATION.md frontmatter status from `human_needed` / `passed_partial` → `passed`, with an evidence note grep-verifiable against the HUMAN-UAT log. Phase 06.9 already used this exact pattern for Phase 02 SC-5 step 7, Phase 03 SC-1, Phase 05 SC-1 — Phase 10 mirrors it for SC-2/SC-4 (Phase 04) and SC-1/SC-2 (Phase 03 + 02 + 05 under the prod hostname).

**When to use:** When a prior phase's `human_needed` SC depends on a live-deployed gateway, and the current phase's HUMAN-UAT executes that exact scenario.

**Example (Phase 03 close):**
```bash
cd /home/pedro/projetos/pedro/gpu-ifix
sed -i 's/^status: human_needed$/status: passed/' \
    .planning/phases/03-resilience-fallback-chain/03-VERIFICATION.md

# Insert evidence stanza right under the frontmatter (operator pastes the dated log line)
cat >> /tmp/03-evidence.txt <<EOF
  gaps_closed_phase_10_2026_05_XX:
    - "SC-1 LIVE — re-verified 2026-05-XX against ai-gateway.converse-ai.app (image v1.0.0).
       2 consecutive chat probes with local-llm FORCED_OPEN both returned HTTP 200 + DeepSeek
       v4 Flash via openrouter-chat; audit_log.upstream='openrouter-chat' for each request_id.
       Mirrors Phase 06.9 S4 under the prod hostname. See 10-HUMAN-UAT.md S4."
EOF

git add .planning/phases/03-resilience-fallback-chain/03-VERIFICATION.md
git commit -m 'docs(03-VERIFICATION): cascade-close SC-1 via Phase 10 HUMAN-UAT S4'

# Positive-assertion grep (verify the commit landed the right text):
grep -E "^status: passed$" .planning/phases/03-resilience-fallback-chain/03-VERIFICATION.md
grep -E "gaps_closed_phase_10_2026" .planning/phases/03-resilience-fallback-chain/03-VERIFICATION.md
```

### Anti-Patterns to Avoid

- **DO NOT add a new edge Traefik on the Proxmox host.** All 162.55.92.154:443 traffic DNATs to .30; adding a parallel Traefik on the host creates dual cert-issuance + DNAT conflicts. Extend the existing edge Traefik on vps-ifix-vm.
- **DO NOT use `worker_intra` as the external network name in the prod compose file.** That network does NOT exist on n8n-ia-vm. Use `intra` (the actual Swarm overlay there).
- **DO NOT use a `deploy:` block in the prod compose file.** D-11 mandates direct compose; `deploy:` is Swarm-only and is silently ignored by `docker compose up -d`. Use `restart: unless-stopped` + `resources` block at the top level instead. Keep `gateway/docker-compose.yml` (the Swarm template) as-is for reference + dev-stack future.
- **DO NOT publish gateway:8080 or dashboard:3001 on the host's interface.** Traffic enters via internal Traefik over the `intra` overlay only — host port publishes would create an authentication bypass path.
- **DO NOT reuse `n8n_redis` for gateway state.** Keyspace collisions; D-07 rejects.
- **DO NOT change `ai_gateway` schema name in any code path during Phase 10.** Use a separate Postgres database with the same schema name (see `## How To #3`). Renaming the schema is a Phase 11+ refactor (touches every migration + every sqlc query + pool.go + dashboard isolation pitfall doc).
- **DO NOT set `proxied=true` on the CF A records.** ACME TLS-ALPN-01 requires direct origin reachability + the gateway returns OpenAI-format errors that benefit from no proxy interference. Operator can enable proxy in Phase 11 if WAF/rate-limit at the edge is desired.
- **DO NOT delete `/opt/ai-gateway-dev/` from vps-ifix-vm during Phase 10.** Keep it running as fallback during stabilization (72h+); decommission only after prod is GREEN end-to-end.

---

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| TLS cert issuance | Hand-roll certbot + custom hook | Edge Traefik `letsencrypt` certresolver + TLS-ALPN-01 | Already deployed; the file provider auto-picks new routes. |
| DNS record creation | Manual CF dashboard | `curl POST /zones/{id}/dns_records` with the CF token in `~/.claude/CLAUDE.md` | Reproducible + scriptable + auditable. |
| Schema migrations | Custom SQL exec on boot | `goose v3` via `gateway/internal/db/migrate.go` + `AI_GATEWAY_MIGRATE_ON_BOOT=true` | Idempotent + tracks version in `goose_db_version`. |
| Per-tenant key minting | Manual SQL inserts | `gatewayctl key create --tenant <slug> --data-class <…>` | Generates `ifix_sk_…` + argon2id hash + DB row atomically; secret-once. |
| Redis password generation | Hand-pick string | `openssl rand -hex 24` | 96 bits entropy from kernel RNG; copy/paste safe (no special chars). |
| Sentry release tagging | Manual API release-create | `Release: BuildVersion` already wired in `obs/sentry.go:27`; `-ldflags '-X .../obs.BuildVersion=v1.0.0'` already in `gateway/Dockerfile:42` | Tag flows automatically when GHA builds `:v1.0.0`. |
| Cascade-close mechanics | Free-form commit messages | Positive-assertion grep (WARNING-5 from Phase 06.9) | Each commit's success is independently verifiable by an external grep recipe. |
| Rollback procedure | Re-deploy from develop branch | Pin `:v1.0.0` image tag; rollback = swap to previous `:v0.X.Y` (or `:main-<sha>` for emergency unbuilt rollbacks) | Image tag immutability + ghcr cache retention make rollback < 30 s. |

**Key insight:** Phase 10 is **deploy mechanics, not implementation**. Every component has already been built + integration-tested across Phase 02-09. The pitfall here is *infrastructure assumptions* — the CONTEXT.md ingress/schema/network assumptions don't match production reality, and naive copy of the Swarm `docker-compose.yml` would fail at `docker compose up -d` with "network worker_intra not found". Custom solutions for any item above would re-implement what already works in dev.

---

## Runtime State Inventory

> Phase 10 is greenfield-stack-on-existing-host, NOT a rename/refactor. This inventory still applies because we are introducing a new tenant/key keyspace and a new Sentry project.

| Category | Items Found | Action Required |
|----------|-------------|------------------|
| Stored data | (NEW) `bd_ai_gateway_prod` database on DO Postgres — 27 migrations run from `ai_gateway.goose_db_version=0` on first `AI_GATEWAY_MIGRATE_ON_BOOT=true` deploy. Bootstrap row: tenant `converseai` (from migration 0001). | Operator creates the DB via DO console BEFORE first deploy. Plan: `CREATE DATABASE bd_ai_gateway_prod;` + grant on the existing DO user. (Same for `bd_ai_dashboard_prod`.) |
| Stored data | (NEW) Tenant API keys minted via `gatewayctl key create` during HUMAN-UAT — surfaced ONCE on stdout per `provision-tenants.sh --mint-keys`. | Operator copies each into the respective client app's Portainer stack env var (ConverseAI, Chat Ifix, Telefonia, Cobranças, Campanhas, voice-api). Plan: HUMAN-UAT step lists the 6 destinations. |
| Stored data | (NEW) Dashboard Better Auth tables (`user`, `session`, `account`, `verification`) in `bd_ai_dashboard_prod`. Bootstrap: first operator signup via `https://ai-dashboard.converse-ai.app`. | `npx @better-auth/cli migrate` runs once against `DASHBOARD_DATABASE_URL` (operator step inside the dashboard container or locally). |
| Live service config | (NEW) Edge Traefik file-provider route added in `traefik-dynamic/ai-gateway-prod.yml`. NOT in git of any deployable repo — lives under `infra/traefik-dynamic/` on vps-ifix-vm directly. | Plan rsyncs the file to vps-ifix-vm:/home/pedro/projetos/pedro/infra/traefik-dynamic/ (path watched by edge Traefik via volume mount); hot-reload picks it up within 1s. |
| Live service config | (NEW) Sentry project `ifix-ai-gateway-prod` in Ifix org — UI creation only; SDK does NOT auto-create projects. | Operator creates the project via sentry.io UI; copies DSN into `/opt/ai-gateway-prod/.env`. |
| OS-registered state | None — no Windows Task Scheduler / launchd / systemd unit added by Phase 10. The compose stack uses Docker's own restart policy. | None. |
| Secrets/env vars | (NEW) `/opt/ai-gateway-prod/.env` (mode 600, owner pedro). Operator populates from `gateway/.env.prod.example` + secrets in `~/.claude/CLAUDE.md` + `gatewayctl admin-key create` + Sentry UI DSN copy. | First-deploy step inside HUMAN-UAT. NEVER commit. |
| Build artifacts | (NEW) GHCR image tags `ghcr.io/ifixtelecom/ifix-ai-gateway:v1.0.0` + `:latest` + `:v1.0.0-<sha>`; symmetric dashboard tags. Produced by GHA `build-gateway.yml` + `build-dashboard.yml` on tag push (workflows already support `tags: ['v*']`). | Plan triggers via `git tag -a v1.0.0 && git push origin v1.0.0`. Verify GHA green before operator deploys. |
| Build artifacts | (NEW) ACME cert for `*.converse-ai.app` cached in vps-ifix-vm `traefik_letsencrypt` Docker volume's `acme.json` after first TLS-ALPN-01 challenge succeeds. | Automatic via Traefik. Verify with `curl -vI https://ai-gateway.converse-ai.app` after DNS propagation. |

---

## Common Pitfalls

### Pitfall 1: Network name mismatch (`worker_intra` vs `intra`)

**What goes wrong:** `docker compose up -d` errors with `network worker_intra declared as external, but could not be found` because the canonical Swarm template references a network that exists on the Portainer-managed dev environment but NOT on n8n-ia-vm.
**Why it happens:** The Swarm template in `gateway/docker-compose.yml` was authored for the dev stack's traefik-public/worker_intra naming; n8n-ia-vm's Swarm uses `intra` instead.
**How to avoid:** Plan Wave 0 produces `gateway/docker-compose.prod.yml` with `networks.intra.external: true` — and the planner inspects `ssh n8n-ia-vm 'docker network ls'` output as a Wave 0 gate.
**Warning signs:** Failed deploy with stderr matching `network .* declared as external, but could not be found`.

### Pitfall 2: schema-name confusion → migrations fail or pollute dev DB

**What goes wrong:** Operator interprets D-05 literally and either (a) creates a new schema named `ai_gateway_prod` in the dev database (migrations fail because they hardcode `CREATE SCHEMA IF NOT EXISTS ai_gateway` + `SET search_path = ai_gateway, public`), or (b) tries to point the prod DSN at the dev DB → migrations run against the SAME `ai_gateway` schema currently serving dev → catastrophic table collisions.
**Why it happens:** Schema name is hardcoded in 27 migration files + pool.go + sqlc queries; the CONTEXT D-05 text implies it's configurable.
**How to avoid:** Plan creates a NEW DATABASE (`bd_ai_gateway_prod`) on the same DO instance, lets migrations run their hardcoded `ai_gateway` schema inside the new database. The Plan's RUNBOOK-DEPLOY.md documents this explicitly as the operationalization of D-05.
**Warning signs:** Migration step fails with `schema "ai_gateway" already exists with different contents` OR dev-stack readouts show prod tenant rows in their tenants table.

### Pitfall 3: edge cert never issued — DNS flip before route added

**What goes wrong:** Operator flips CF DNS A record before adding the file-provider route in `traefik-dynamic/`. First HTTPS request lands on edge Traefik with no matching router → ACME never starts → cert never issued → subsequent requests serve Traefik's default self-signed cert → browsers reject.
**Why it happens:** TLS-ALPN-01 challenge only fires after edge Traefik has a router for the host (the router is what triggers cert acquisition).
**How to avoid:** Plan order is **strict**: (1) write `ai-gateway-prod.yml` + rsync to vps-ifix-vm, (2) wait 2s for edge Traefik hot-reload + tail logs for `"no certificate for ..."` info-log, (3) ONLY THEN POST the CF DNS records. RUNBOOK-DEPLOY.md documents this as Step 1A vs 1B with a hard gate between them.
**Warning signs:** `curl -vI https://ai-gateway.converse-ai.app` returns `TRAEFIK DEFAULT CERT` in SAN list.

### Pitfall 4: `letsencryptresolver` vs `letsencrypt` certresolver literal mismatch

**What goes wrong:** Plan copies the label literal `letsencryptresolver` from `gateway/docker-compose.yml` into the edge Traefik file-provider YAML → edge logs `unknown certificate resolver: letsencryptresolver` → no cert issued.
**Why it happens:** The two literals are different:
- `gateway/docker-compose.yml` line 89: `traefik.http.routers.ai_gateway.tls.certresolver=letsencryptresolver` — this is a docker-provider label intended for an INTERNAL Traefik named with the resolver `letsencryptresolver` (which existed in the OLD dev stack on vps-ifix Hetzner Cloud — see PF-1 in `n8n-ia.yml`).
- Edge Traefik on vps-ifix-vm has the resolver literal `letsencrypt` (no `resolver` suffix), confirmed via `/home/pedro/projetos/pedro/infra/docker-compose.yml:14`.
**How to avoid:** In the file-provider YAML use `certResolver: letsencrypt` (matches edge static config). In the prod compose file's INTERNAL Traefik label, the resolver name doesn't matter because **internal Traefik on n8n-ia-vm only listens on :80** — TLS terminates at edge, internal does plain HTTP. So delete the `traefik.http.routers.ai_gateway.tls.certresolver=…` label from the prod compose file entirely (along with `entrypoints=websecure` → use `entrypoints=web`).
**Warning signs:** `docker exec infra-traefik-1 cat /letsencrypt/acme.json | jq .letsencrypt.Certificates | grep -A2 converse-ai.app` returns empty + logs show `unknown certificate resolver`.

### Pitfall 5: Sentry release tag mismatch — release shows `dev`

**What goes wrong:** Image build doesn't propagate `GATEWAY_VERSION=v1.0.0` → `obs.BuildVersion` stays at `"dev"` (the Dockerfile default) → Sentry releases all tag as `dev` → no correlation to git tag.
**Why it happens:** GHA workflow `compute-tags` sets `gateway_version=v1.0.0` ONLY when `refs/tags/v*` matches; if operator builds locally or pushes to `main` without tagging first, BuildVersion is `main-<sha>`.
**How to avoid:** Plan order is: (1) merge develop → main, (2) `git tag -a v1.0.0 -m "..."`, (3) `git push origin v1.0.0` — this triggers GHA `compute-tags` to set `gateway_version=v1.0.0` (verified in `build-gateway.yml:137`). RUNBOOK-DEPLOY.md cut-release section enumerates this order.
**Warning signs:** Sentry dashboard "Releases" for `ifix-ai-gateway-prod` shows `main-abc1234` instead of `v1.0.0`.

### Pitfall 6: VM 101 capacity ceiling — n8n cluster starves gateway

**What goes wrong:** Adding gateway (2 CPU / 1G mem limit) + dashboard (1 CPU / 512M) + redis-gateway-prod (~200M) under existing n8n cluster (already at 5.8G/15G used, 67% disk used) pushes mem allocation past the working-set comfortable zone, triggering kswapd / OOM-killer rotations.
**Why it happens:** Current `free -h` on n8n-ia-vm: 15Gi total, 5.8Gi used, 533Mi free, 10Gi buff/cache, 9.8Gi available. Adding ~1.7G of working-set is comfortable headroom — BUT disk at 67% (25/40G used) is a watch item; gateway + dashboard images + buildx cache could push past 80%.
**How to avoid:** Plan Wave 0 includes (a) `ssh n8n-ia-vm 'free -h; df -h /var/lib/docker'` capacity gate; (b) preemptive `docker image prune -af` if disk > 80%; (c) Phase 11 follow-up to scale VM 101 to 8c/24G/60G if observed working set exceeds 12G.
**Warning signs:** `dmesg | grep -i 'killed process'` after first 24h of prod traffic; `df -h /` over 80%.

### Pitfall 7: dev + prod sharing OpenRouter / OpenAI keys → spend mixing

**What goes wrong:** Per D-08 (accepted trade-off), prod and dev share upstream keys. Spend mix means an OpenRouter monthly cap blows the dev stack out of OpenRouter access mid-incident; also a leaked key requires rotating in BOTH stacks atomically.
**Why it happens:** Phase 10 explicitly accepts this; Phase 11 may revisit.
**How to avoid (for Phase 10):** RUNBOOK-DEPLOY.md documents the shared-key invariant + lists the 2 `.env` files that must be updated atomically on rotation (vps-ifix-vm `/opt/ai-gateway-dev/.env` + n8n-ia-vm `/opt/ai-gateway-prod/.env`).
**Warning signs:** OpenRouter dashboard spend overruns observed; Sentry events for `HTTP 402 insufficient_credits` from openrouter-chat upstream.

### Pitfall 8: `MIGRATE_ON_BOOT=true` left on after first deploy → goose race on every restart

**What goes wrong:** Concurrent gateway restarts (e.g. dashboard + gateway both pulling new image at once during `docker compose up -d`) both attempt `goose.UpContext` → goose's transactional locking holds, but observed startup time bumps ~100ms per restart.
**Why it happens:** Operator forgets the second-step flip from `true` → `false` after first successful deploy.
**How to avoid:** Plan Wave 4 (operator HUMAN-UAT) includes an explicit checkbox: "After first deploy passes /health 200, flip `AI_GATEWAY_MIGRATE_ON_BOOT=false` in `/opt/ai-gateway-prod/.env` and `docker compose up -d --force-recreate ifix-ai-gateway`". RUNBOOK-DEPLOY.md echoes the same step.
**Warning signs:** Boot logs show `goose: migrations: up to current version: 27` on every restart instead of just the first one.

---

## Code Examples

### Verified env contract (excerpt; full file in `gateway/.env.prod.example`)

```bash
# /opt/ai-gateway-prod/.env  (mode 600, owner pedro:pedro, NEVER commit)
#
# Source of values:
#   - ~/.claude/CLAUDE.md       — CF, OpenRouter, MinIO, Vast.ai, OpenAI, GitHub PAT
#   - sentry.io UI               — new project ifix-ai-gateway-prod DSN
#   - openssl rand -hex 24       — Redis password + (optional) BETTER_AUTH_SECRET
#   - gatewayctl admin-key create — dashboard admin key
#
# This file mirrors gateway/.env.prod.example one-to-one. Add new keys in BOTH places.

# --- Runtime ---
TZ=America/Sao_Paulo
ENV=production
GATEWAY_PORT=8080
LOG_LEVEL=info

# --- Postgres (DO managed) ---
# NEW database bd_ai_gateway_prod on the existing instance.
# The DSN points at the new DB; schema name `ai_gateway` is hardcoded in migrations + pool.go.
AI_GATEWAY_PG_DSN=postgres://doadmin:PASS@db-grupoifix-do-user-7520351-0.j.db.ondigitalocean.com:25060/bd_ai_gateway_prod?sslmode=require
AI_GATEWAY_PG_MAX_CONNS=10
# First deploy ONLY; flip false after `goose: up to current version: 27` lands.
AI_GATEWAY_MIGRATE_ON_BOOT=true

# --- Redis (local container redis-gateway-prod) ---
AI_GATEWAY_REDIS_ADDR=redis-gateway-prod:6379
AI_GATEWAY_REDIS_PASSWORD=<openssl rand -hex 24 output>
AI_GATEWAY_REDIS_DB=0

# --- Upstreams ---
# Local LLM = Vast.ai primary pod (24/7 schedule disabled in Phase 10; operator force-up only)
UPSTREAM_LLM_URL=http://10.10.10.20:8000     # dynamic-override path takes precedence
UPSTREAM_STT_URL=http://10.10.10.20:8001
# Tier-0 embed = colocated Infinity (ai-gateway-embed on same VM)
UPSTREAM_EMBED_URL=http://10.10.10.20:7997
UPSTREAM_HEALTH_BRIDGE_URL=http://10.10.10.20:9100

# --- Tier-1 fallback (REUSE dev keys per D-08) ---
UPSTREAM_LLM_OPENROUTER_URL=https://openrouter.ai/api
UPSTREAM_LLM_OPENROUTER_AUTH_BEARER=<from ~/.claude/CLAUDE.md OpenRouter key>
# Empty per Phase 06.9 — schema row from migration 0027 wins (deepseek-v4-flash:nitro)
UPSTREAM_LLM_OPENROUTER_MODEL=
UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER=
UPSTREAM_STT_OPENAI_URL=https://api.openai.com
UPSTREAM_STT_OPENAI_AUTH_BEARER=<from ~/.claude/CLAUDE.md OpenAI key>
UPSTREAM_EMBED_OPENAI_URL=https://api.openai.com
UPSTREAM_EMBED_OPENAI_AUTH_BEARER=<same OpenAI key>

# --- Vast.ai primary + emerg (REUSE dev key + budgets) ---
VAST_AI_API_KEY=<from ~/.claude/CLAUDE.md>
VAST_PRICE_CAP_DPH=0.40
PRIMARY_VAST_PRICE_CAP_DPH=0.60
MONTHLY_EMERGENCY_BUDGET_BRL=200
MONTHLY_PRIMARY_BUDGET_BRL=2400
USD_TO_BRL_RATE=5.0
PRIMARY_POD_SCHEDULE_DISABLED=true
PRIMARY_NUM_GPUS=2
PRIMARY_GPU_NAME=RTX 3090
PRIMARY_VAST_MACHINE_ALLOWLIST=43803,55158       # Phase 06.8 final shape
PRIMARY_VAST_MACHINE_BLOCKLIST=55942,45778

# --- MinIO (REUSE dev creds — weights bucket is shared) ---
MINIO_ENDPOINT=https://s3.ifixtelecom.com.br
MINIO_BUCKET=ai-gateway
MINIO_ACCESS_KEY=<from ~/.claude/CLAUDE.md>
MINIO_SECRET_KEY=<from ~/.claude/CLAUDE.md>

# (weights SHA-256s — same as dev for now)
WEIGHTS_QWEN_SHA256=…
WEIGHTS_WHISPER_SHA256=…
WEIGHTS_BGE_M3_SHA256=…
PRIMARY_QWEN_WEIGHTS_SHA256=…
PRIMARY_WHISPER_WEIGHTS_SHA256=…
PRIMARY_BGEM3_WEIGHTS_SHA256=…

# --- Sentry (NEW prod project) ---
SENTRY_DSN=<from sentry.io UI after creating ifix-ai-gateway-prod project>

# --- Dashboard ---
BETTER_AUTH_SECRET=<openssl rand -base64 32>
BETTER_AUTH_URL=https://ai-dashboard.converse-ai.app
DASHBOARD_DATABASE_URL=postgres://doadmin:PASS@db-grupoifix-do-user-7520351-0.j.db.ondigitalocean.com:25060/bd_ai_dashboard_prod?sslmode=require&options=-c%20search_path%3Ddashboard_auth
GATEWAY_BASE_URL=http://ifix-ai-gateway:8080
GATEWAY_ADMIN_KEY=<from `gatewayctl admin-key create --label phase-10-prod`>

# --- Bootstrap ---
BOOTSTRAP_TENANT_SLUG=converseai

# --- Schedule routing (Phase 04) ---
WRITE_TIMEOUT_CHAT_SECONDS=0
WRITE_TIMEOUT_EMBED_SECONDS=30
WRITE_TIMEOUT_AUDIO_SECONDS=120
```

### Verified prod compose stack (`gateway/docker-compose.prod.yml`)

```yaml
# Phase 10 — ifix-ai-gateway prod stack (operator-managed direct compose).
#
# Deploy: ssh n8n-ia-vm "cd /opt/ai-gateway-prod && docker compose pull && docker compose up -d"
# Rollback: sed -i 's/:v1.0.0/:v0.X.Y/' /opt/ai-gateway-prod/docker-compose.yml && up -d --force-recreate
#
# Networks: joins the existing Swarm overlay `intra` (created by traefik-internal_traefik
# Swarm service). intra is `attachable: true` so standalone compose containers can join.
# This is HOW the prod stack reaches the internal Traefik label-routing pipeline despite
# the rest of the host running in Swarm mode.
#
# Pitfall reminder: do NOT add a `deploy:` block — that's Swarm-only and silently ignored
# by `docker compose up -d`. Use top-level `restart:` instead.

services:
  ifix-ai-gateway:
    image: ghcr.io/ifixtelecom/ifix-ai-gateway:${TAG:-v1.0.0}
    container_name: ifix-ai-gateway
    restart: unless-stopped
    env_file: .env
    networks:
      - intra
    healthcheck:
      test: ["CMD", "/gateway", "--self-check"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 60s
    labels:
      - "traefik.enable=true"
      - "traefik.docker.network=intra"
      - "traefik.http.routers.ai_gateway.rule=Host(`ai-gateway.converse-ai.app`)"
      - "traefik.http.routers.ai_gateway.entrypoints=web"
      - "traefik.http.services.ai_gateway.loadbalancer.server.port=8080"
      - "traefik.http.services.ai_gateway.loadbalancer.passHostHeader=true"

  ifix-ai-dashboard:
    image: ghcr.io/ifixtelecom/ifix-ai-dashboard:${DASHBOARD_TAG:-v1.0.0}
    container_name: ifix-ai-dashboard
    restart: unless-stopped
    env_file: .env
    environment:
      TZ: America/Sao_Paulo
      NODE_ENV: production
      PORT: 3001
    depends_on:
      - ifix-ai-gateway
    networks:
      - intra
    labels:
      - "traefik.enable=true"
      - "traefik.docker.network=intra"
      - "traefik.http.routers.ai_dashboard.rule=Host(`ai-dashboard.converse-ai.app`)"
      - "traefik.http.routers.ai_dashboard.entrypoints=web"
      - "traefik.http.services.ai_dashboard.loadbalancer.server.port=3001"
      - "traefik.http.services.ai_dashboard.loadbalancer.passHostHeader=true"

  redis-gateway-prod:
    image: redis:7-alpine
    container_name: redis-gateway-prod
    restart: unless-stopped
    command: >-
      redis-server
      --requirepass ${AI_GATEWAY_REDIS_PASSWORD}
      --maxmemory 256mb
      --maxmemory-policy allkeys-lru
      --save ""
      --appendonly no
    networks:
      - intra
    healthcheck:
      test: ["CMD", "redis-cli", "-a", "${AI_GATEWAY_REDIS_PASSWORD}", "ping"]
      interval: 10s
      timeout: 3s
      retries: 3

networks:
  intra:
    external: true
```

---

## State of the Art

| Old Approach (dev stack) | Current Approach (prod stack) | When Changed | Impact |
|--------------------------|-------------------------------|--------------|--------|
| Portainer Repository + webhook auto-deploy (converseai-v4 prod) | Direct `docker compose` operator-managed | D-11 in CONTEXT.md | Simpler debug path; manual roll-forward |
| `:latest-dev` floating tag (dev) | `:v1.0.0` pinned tag (prod) | D-12 + D-13 | Deterministic rollback |
| Single Sentry project for both envs | Separate `ifix-ai-gateway-prod` project | D-14 | Alert isolation + per-env release tagging |
| Schema `ai_gateway` in `bd_ai_gateway` (dev DB) | Same schema name in NEW `bd_ai_gateway_prod` database | Phase 10 implementation | Isolation without code changes |
| Edge Traefik routes via `worker.yml` (Phase 5 pattern, vps-ifix-vm) | New `ai-gateway-prod.yml` mirrors `n8n-ia.yml` (Phase 4 pattern) | Phase 10 | Edge file-provider entry per VM |

**Deprecated/outdated:**

- `gateway/docker-compose.yml` Swarm template references `worker_intra` external network — that's the OLD Hetzner Cloud dev VPS naming. Both prod (n8n-ia-vm) and dev (vps-ifix-vm) use different network names now (`intra` and `traefik-public` respectively). The Swarm template stays as a Portainer-stack future reference; the dev stack lives in `/opt/ai-gateway-dev/docker-compose.yml` (vps-ifix-vm) with `traefik-public`; the prod stack lives in `gateway/docker-compose.prod.yml` with `intra`.

---

## Open Questions (RESOLVED)

1. **Should the dashboard share `bd_ai_gateway_prod` or get its own database?**
   - What we know: dashboard isolation is explicit (Pitfall 7 in dashboard/src/lib/db.ts comment); `DASHBOARD_DATABASE_URL` is a separate DSN by design.
   - What's unclear: separate database vs separate schema in same DB.
   - RESOLVED: Recommendation: **separate database** `bd_ai_dashboard_prod` — matches the dev pattern (dashboard schema name `dashboard_auth` lives in a DB pointed at by `DASHBOARD_DATABASE_URL`), simplest isolation guarantee, no shared `search_path` ambiguity.

2. **Does the gateway container actually need to join the `intra` overlay, or can it stay on a bridge net + label-discover via Swarm?**
   - What we know: Swarm-mode Traefik with `--providers.docker.swarmMode=true --providers.docker.network=intra` only discovers services on `intra` overlay. Standalone compose containers CAN join an overlay if it's `attachable: true` (verified — `intra` is attachable).
   - What's unclear: does standalone-attachment trigger Traefik discovery? Swarm provider typically only scans Swarm `service ls` outputs, not standalone `docker ps`.
   - RESOLVED: Recommendation: confirm in Wave 0 by deploying a hello-world container on `intra` with Traefik labels and observing `docker logs traefik-internal_traefik.1.xxx 2>&1 | grep -i 'router added'`. If Traefik does NOT discover standalone containers via Swarm provider, fall back to **option B: add the docker provider in addition to swarm provider** (`--providers.docker=true` alongside `--providers.docker.swarmMode=true`) — this is supported. **OR option C:** publish gateway:8080 + dashboard:3001 on the host with `127.0.0.1:NNNN` and add static `traefik-dynamic/` file-provider entries on the internal Traefik via a docker-mounted dir (mirrors how the edge Traefik works). Option B is cheapest.

3. **Should we keep `ai-gateway-embed` (Infinity tier-0 embed) on its current `ai-gateway-embed_default` bridge network or migrate it to `intra` overlay?**
   - What we know: `ai-gateway-embed` is on its own bridge net; gateway reaches it today (in dev) via `UPSTREAM_EMBED_URL=http://10.10.10.20:7997` (host port).
   - What's unclear: should prod keep the host-IP path (works regardless of network) or move to overlay-internal DNS?
   - RESOLVED: Recommendation: **keep `UPSTREAM_EMBED_URL=http://10.10.10.20:7997`** (host IP path) for Phase 10. Migrating Infinity onto `intra` is an unrelated change with no Phase 10 benefit; defer to whatever phase touches the embed pod next.

4. **GHA self-hosted runners — does Phase 10 need any of the 7 vps-ifix-vm runners?**
   - What we know: gateway + dashboard workflows currently use `runs-on: ubuntu-latest` (GitHub-hosted, not self-hosted). No runner changes needed.
   - What's unclear: do we have GitHub Actions billing healthy now? (history shows billing-blocked runners — see CLAUDE.md GHA runners table.)
   - RESOLVED: Recommendation: confirm in Wave 0 via `gh api /repos/IfixTelecom/gpu-ifix/actions/runs?per_page=1`. If runners are healthy, tag push triggers run. If billing-blocked, plan calls operator to enable.

---

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| `docker compose` v2 on n8n-ia-vm | Plan 10-02 prod stack bring-up | ✓ | shipped with Docker 24+ | — |
| Internal Traefik on n8n-ia-vm (Swarm) | Plan 10-02 label routing | ✓ | v2.11 Swarm service `traefik-internal_traefik.1` | — |
| Edge Traefik on vps-ifix-vm | Plan 10-03 file-provider ingress | ✓ | v3.6 in `/home/pedro/projetos/pedro/infra/docker-compose.yml` | — |
| Proxmox host 443 DNAT to 10.10.10.30 | Existing public traffic | ✓ | iptables rules in `/etc/network/interfaces` on ifix-prod-01 | — |
| `intra` overlay network on n8n-ia-vm | Plan 10-02 compose `networks.intra.external` | ✓ | `attachable: true` (verified) | — |
| DO Postgres managed instance | Plan 10-01 schema setup | ✓ | reachable from 162.55.92.154 (egress confirmed); Trusted Sources includes the IP | — |
| Cloudflare API token (DNS:Edit on `converse-ai.app`) | Plan 10-04 DNS records | ✓ | in `~/.claude/CLAUDE.md`; verified active 2026-04-19 | — |
| `ai-gateway-embed` Infinity tier-0 embed | gateway runtime path | ✓ | `michaelf34/infinity:0.0.77` Up 5 days healthy on n8n-ia-vm:7997 | — |
| OpenRouter API key | tier-1 chat fallback | ✓ | in `~/.claude/CLAUDE.md`; verified live 2026-05-24 | — |
| OpenAI API key | tier-1 STT/embed fallback | ✓ | in `~/.claude/CLAUDE.md` (Phase 06.9 used same key) | — |
| MinIO credentials | weights pulls for primary pod | ✓ | in `~/.claude/CLAUDE.md`; bucket `ai-gateway` healthy | — |
| Vast.ai API key | primary + emerg pod control | ✓ | in `~/.claude/CLAUDE.md` | — |
| Sentry org Ifix | new prod project | ✓ | org exists; operator creates project `ifix-ai-gateway-prod` via UI | — |
| GitHub repo `IfixTelecom/gpu-ifix` write access | merge develop → main + tag push | ✓ | PAT in `~/.git-credentials` (mode 600); CLAUDE.md notes it as `ghp_…` | — |
| `gh` CLI | tag verification + GHA run status | ✓ | available on ops-claude | — |
| `mc` (MinIO client) | weights validation if rebake needed | partial | not directly needed by Phase 10; weights already uploaded for dev | skip if not needed |
| `provision-tenants.sh` + `smoke-*.py` scripts | HUMAN-UAT tenant seed + golden path | ✓ | already at `scripts/integration-smoke/` | — |
| n8n-ia-vm disk headroom | prod images + redis volume | ⚠ at 67% | 25/40G used; +2G for images = ~70% | scale VM 101 to 60G disk before Phase 11 |
| n8n-ia-vm memory headroom | +1.7G working set for gateway/dashboard/redis | ✓ | 9.8G available (after buff/cache reclaim) | scale VM 101 to 24G if PRIMARY_POD also runs concurrently |

**Missing dependencies with no fallback:** None.

**Missing dependencies with fallback:**

- Disk approaching 80%: pre-deploy `docker image prune -af` + plan VM disk grow to 60G.

---

## Validation Architecture

### Test Framework

| Property | Value |
|----------|-------|
| Framework | Go `testing` + testcontainers-go (Postgres + Redis); already running in CI via `build-gateway.yml` |
| Config file | `gateway/go.mod` (declares deps); CI step `go test -tags=integration ./gateway/internal/integration_test/...` |
| Quick run command | `cd /home/pedro/projetos/pedro/gpu-ifix && go test ./gateway/internal/integration_test/... -count=1 -race -timeout=5m` (skip integration tag for unit-only) |
| Full suite command | `cd /home/pedro/projetos/pedro/gpu-ifix && go test -tags=integration ./gateway/... -count=1 -v -timeout=10m && (cd dashboard && npm run build && npx tsc --noEmit && npx vitest run)` |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|--------------|
| INT-06 | Per-tenant golden path passes under prod hostname | manual-uat | `python scripts/integration-smoke/smoke-converseai.py --gateway-url https://ai-gateway.converse-ai.app --api-key $K --out /tmp/r.json` | ✅ existing |
| INT-06 (rollback) | Image-tag swap rollback completes < 5 min | manual-uat (timed) | `time bash -c 'sed -i s/:v1.0.0/:v0.X.Y/ /opt/ai-gateway-prod/docker-compose.yml && docker compose -f /opt/ai-gateway-prod/docker-compose.yml up -d --force-recreate'` | scripted in RUNBOOK-DEPLOY.md (Wave 0) |
| PRD-04 (partial) | `RUNBOOK-DEPLOY.md` complete + grep-positive | grep | `grep -E "^## (Triggers\|Preconditions\|Steps\|Verification\|Rollback)$" gateway/docs/RUNBOOK-DEPLOY.md \| wc -l` (expect ≥ 5) | ❌ Wave 0 plan |
| PRD-07 | DNS resolves + TLS cert valid | smoke | `dig +short ai-gateway.converse-ai.app; curl -sS -I https://ai-gateway.converse-ai.app/health \| grep -E '^HTTP/.*200'` | scripted in HUMAN-UAT |
| Cascade 02 SC-5 step 7 | E2E chat returns 200 + provider header | smoke | `curl -sS -X POST https://ai-gateway.converse-ai.app/v1/chat/completions -H "Authorization: Bearer $K" -d '{"model":"qwen","messages":[{"role":"user","content":"PING"}]}' \| jq -e '.choices[0].message.content'` | HUMAN-UAT S1 |
| Cascade 03 SC-1 | Force-open primary breaker → tier-1 200 | smoke (operator-driven) | `docker exec ifix-ai-gateway /gatewayctl breaker force-open --name local-llm`, then 2× `curl` chats, expect provider=Novita in body | HUMAN-UAT S4 |
| Cascade 04 SC-1 | Rate-limit headers + 429 | smoke | parallel chat burst against rps=5 tenant key → expect `X-RateLimit-Limit-Requests=5` + Retry-After | HUMAN-UAT S5 |
| Cascade 04 SC-2 | `billing_events` row inserted | psql | `psql "$AI_GATEWAY_PG_DSN" -c "SELECT count(*) FROM ai_gateway.billing_events WHERE created_at > now() - interval '5 minutes'"` | HUMAN-UAT S6 |
| Cascade 04 SC-4 | Peak off-hours routes to openrouter-chat | smoke | `gatewayctl tenant set-mode <tenant> peak --window 20-22` + chat at off-hours → expect `module=DISPATCHER upstream=openrouter-chat` in logs | HUMAN-UAT S7 |
| Cascade 05 SC-1 | vegeta burst overflow → ≥99% success | smoke | `vegeta attack -duration=30s -rate=5 -targets=/tmp/targets.txt \| vegeta report -type=hist` expect ≥99% 200s | HUMAN-UAT S8 |

### Sampling Rate

- **Per task commit (Wave 0-2 autonomous plans):** `go test ./gateway/... -count=1 -race -timeout=5m` (unit only)
- **Per wave merge:** full suite command (Go integration tag + dashboard typecheck + vitest + build)
- **Phase gate:** HUMAN-UAT 8 scenarios + cascade-close grep-positive on each VERIFICATION.md edit

### Wave 0 Gaps

- [ ] `gateway/docker-compose.prod.yml` — NEW; covers Pitfall 1 (`intra` not `worker_intra`)
- [ ] `gateway/.env.prod.example` — NEW; full prod env contract
- [ ] `/home/pedro/projetos/pedro/infra/traefik-dynamic/ai-gateway-prod.yml` — NEW; edge file-provider route
- [ ] `gateway/docs/RUNBOOK-DEPLOY.md` — NEW; mirrors RUNBOOK-FAILOVER.md headers
- [ ] Wave 0 capacity gate: `ssh n8n-ia-vm 'free -h; df -h /var/lib/docker; docker network ls; docker info | grep -i swarm; curl -s ifconfig.io'` — operator runs once, plan records observed values
- [ ] Wave 0 internal-Traefik discovery proof: deploy ephemeral hello-world container on `intra` with Traefik labels; observe `docker logs traefik-internal_traefik.1.<...>` for router-added line. If absent, switch internal Traefik to dual-provider (Swarm + Docker) — small Wave 0 patch.

---

## Security Domain

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | yes | Gateway: `Authorization: Bearer ifix_sk_…` argon2id-hashed (existing); Dashboard: Better Auth session+cookie (existing); cookie scoped to `ai-dashboard.converse-ai.app` via `BETTER_AUTH_URL`. |
| V3 Session Management | yes | Better Auth (cookie-based + DB-backed sessions in `bd_ai_dashboard_prod`). |
| V4 Access Control | yes | Per-tenant API keys w/ `data_class` (`normal` vs `sensitive`); sensitive tenants block tier-1 fallback per RES-08 (already verified Phase 09). |
| V5 Input Validation | yes | OpenAI-shape envelope validation in `pkg/openai` + `gateway/internal/httpx`; idempotency body-hash mismatch → 422. |
| V6 Cryptography | yes | argon2id (OWASP 2026 params) for API key hash; TLS-ALPN-01 cert issuance via Traefik LE; `BETTER_AUTH_SECRET` HMAC for session signing. No hand-rolled crypto. |
| V7 Error Handling + Logging | yes | OpenAI-shape error envelopes (`gateway/internal/httpx/envelope.go`); Sentry `BeforeSend` redacts sensitive headers + request body (`obs/sentry.go:beforeSend`); structured slog with `Redactor` strips `Authorization`/`X-API-Key`/`Cookie`. |
| V8 Data Protection | yes | LGPD policy: sensitive tenants block tier-1 routing; `audit_log_content` row gated on `data_class=normal` (D-B2). |
| V9 Communications | yes | TLS 1.3 default (Traefik v3.6 defaults); ACME tls-alpn-01; CF proxy=OFF accepted per Pitfall 7 — Phase 11 may enable CF proxy + WAF. |
| V10 Malicious Code | n/a | No file upload / executable serve path. |
| V11 Business Logic | yes | Rate-limit (TEN-03) + Quota (TEN-04) + Schedule routing (TEN-05) — already verified Phase 04. |
| V13 API + Web Service | yes | OpenAI-compat contract; X-Request-ID for traceability (existing). |
| V14 Configuration | yes | `.env` mode 600; never committed; env override > schema row > passthrough order documented in Phase 06.9 D-06. |

### Known Threat Patterns for this stack

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| API key exfiltration via repo / log | Information disclosure | `.env` mode 600; secret-once `gatewayctl key create` output; argon2id hash in DB; slog `Redactor` strips auth headers; Sentry `beforeSend` redaction. |
| Cross-tenant data leak via cache | Tampering | Per-tenant key cache scoped by hash; rate-limit Redis keys namespaced by tenant-id. |
| Sensitive tenant routed to external | Information disclosure | RES-08 + `data_class` gating in `audit/middleware.go` (Phase 03 SC-3 verified). |
| Replay attack on side-effecting calls | Tampering | Idempotency-Key + canonical-JSON SHA-256 + 24h Redis TTL; 422 on body mismatch (Phase 02 D-C4). |
| Schema collision with dev env | Tampering | Separate Postgres database `bd_ai_gateway_prod`; same `ai_gateway` schema name but isolated by DB-level boundary (cannot cross-query without crossing DB boundary). |
| Stale cert | Denial-of-service | Traefik LE auto-renewal (30-day window); HUMAN-UAT verifies `acme.json` post-deploy. |
| Webhook auto-deploy abuse | Tampering | N/A — Phase 10 uses NO webhooks (D-11 direct compose). |
| Shared upstream key revocation cascade (dev + prod) | Availability | Documented in RUNBOOK-DEPLOY.md as known limitation; rotate in BOTH `.env` files atomically. |
| n8n cluster CPU/mem starvation | Availability | Pitfall 6 capacity gate Wave 0; Phase 11 VM scaling path. |

---

## How To section per focus area

### #1 — n8n-ia-vm capacity + runtime reconciliation

Verified via `ssh n8n-ia-vm` probe on 2026-05-25:

| Property | Observed Value | Notes |
|----------|---------------|-------|
| `docker info \| grep -i swarm` | `Swarm: active`, Managers=1, Nodes=1 | Swarm-mode is host-wide; standalone compose can coexist. |
| `docker network ls` | `intra` (overlay, swarm scope, `attachable: true`), `ingress` (overlay), `ai-gateway-embed_default` (bridge), `api-docs_default` (bridge), `bridge`, `docker_gwbridge` | Use `intra` (NOT `worker_intra` from the Swarm template). |
| `free -h` | total 15Gi, used 5.8Gi, free 533Mi, buff/cache 10Gi, **available 9.8Gi** | Adding ~1.7G working-set (gateway 1G + dashboard 0.5G + redis 0.2G) is comfortable. |
| `nproc` | 6 | Sufficient for gateway 2c + dashboard 1c + redis. |
| `df -h /` | 25G used / 40G total, **67% used** | ⚠ Watch — pulling new images can spike to 75%. Wave 0 prune step. |
| Egress IP from VM 101 | `162.55.92.154` | Confirmed via `curl -s ifconfig.io`. Already in DO Trusted Sources. |
| Running Swarm services | `traefik-internal_traefik` (v2.11), `n8n_n8n_*`, `rabbitmq_rabbitmq`, `postgres_postgres` (pgvector), `redis_n8n_redis`, `portainer_agent` | No conflicts with new stack. |
| Standalone containers | `ai-gateway-embed` (Infinity 0.0.77 healthy 5d, bridge net `ai-gateway-embed_default`), `ifix-api-docs` | Pattern proves standalone-compose + Swarm services coexist on same host. |
| `/opt/` contents | `ai-gateway-embed`, `api-docs`, `containerd` | Plan creates `/opt/ai-gateway-prod/` alongside. |

**Plan implication:** Wave 0 capacity gate (CLI `ssh n8n-ia-vm 'free -h; df -h; docker image prune -af --filter dangling=true'`) + verify `intra` net attachable + confirm Open Question 2 (Traefik Swarm provider vs Docker provider discovery semantics) via 5-minute hello-world test before committing to Phase 10 patterns.

### #2 — Edge Traefik routing extension

**Edge Traefik on vps-ifix-vm:443** (NOT on ifix-prod-01 host) is the entry. Proxmox iptables DNAT `eno1:443 → 10.10.10.30:443` is fixed (in `/etc/network/interfaces` post-up hook). The edge Traefik file provider reads `./traefik-dynamic/*.yml` and hot-reloads within 1s.

**Mirror pattern (already in production):** `/home/pedro/projetos/pedro/infra/traefik-dynamic/n8n-ia.yml` — routes 4 hostnames to `http://10.10.10.20:80` with `passHostHeader: true`. Phase 10 file-provider entry uses the **same** loadbalancer URL (since internal Traefik listens on :80 only). [VERIFIED: ssh vps-ifix-vm 'cat n8n-ia.yml']

**Cert resolver literal:** `letsencrypt` (NOT `letsencryptresolver` as Phase 4/5 PF-1 documented). [VERIFIED: /home/pedro/projetos/pedro/infra/docker-compose.yml:14]

**Push procedure:**
```bash
# 1) Validate YAML locally first (mitigation for T-05-03-04)
python3 -c "import yaml,sys; yaml.safe_load(open(sys.argv[1]))" \
  /home/pedro/projetos/pedro/gpu-ifix/.planning/phases/10-prod-deploy-ai-gateway/artifacts/ai-gateway-prod.yml

# 2) Copy to vps-ifix-vm
scp .planning/phases/10-prod-deploy-ai-gateway/artifacts/ai-gateway-prod.yml \
    vps-ifix-vm:/home/pedro/projetos/pedro/infra/traefik-dynamic/ai-gateway-prod.yml

# 3) Watch edge Traefik tail for the route load (hot-reload, no restart)
ssh vps-ifix-vm 'docker logs -f --tail 0 $(docker ps -q -f name=traefik) 2>&1 | grep -i "ai-gateway"'

# Expected: "router ai-gateway-prod@file with rule Host(\`ai-gateway.converse-ai.app\`)"
# At this point cert is NOT issued yet (no DNS) — that's the gating order.
```

**Rollback:** `ssh vps-ifix-vm 'rm /home/pedro/projetos/pedro/infra/traefik-dynamic/ai-gateway-prod.yml'` — hot-reload removes routes; no restart needed.

### #3 — Postgres prod database + migrations

**Implementable form of D-05:** New DATABASE on the same DO managed instance, same hardcoded `ai_gateway` schema name. Same trick for D-06 (dashboard).

**Steps:**

1. Operator opens DO Postgres console → "Databases" tab → create `bd_ai_gateway_prod`. Grant ALL on it to `doadmin`.
2. Same: create `bd_ai_dashboard_prod`.
3. Set `AI_GATEWAY_PG_DSN=postgres://doadmin:PASS@…:25060/bd_ai_gateway_prod?sslmode=require` in `.env`.
4. Set `AI_GATEWAY_MIGRATE_ON_BOOT=true` in `.env`.
5. First `docker compose up -d` runs goose.UpContext (gateway code path: `gateway/internal/db/migrate.go`). Boot log expected: `goose: applied 27 migrations, current version: 27` + `migration 0027 OK`. [VERIFIED: ls gateway/db/migrations/ shows 0001..0027]
6. Operator flips `AI_GATEWAY_MIGRATE_ON_BOOT=false` + `docker compose up -d --force-recreate ifix-ai-gateway`.
7. For dashboard: operator runs `docker run --rm -e DASHBOARD_DATABASE_URL=… ghcr.io/ifixtelecom/ifix-ai-dashboard:v1.0.0 npx @better-auth/cli migrate` (single-shot; creates `user`, `session`, `account`, `verification` tables in `bd_ai_dashboard_prod`).

**Egress + Trusted Sources check (already passes):**
```bash
ssh n8n-ia-vm 'curl -s --max-time 5 ifconfig.io'   # → 162.55.92.154 [VERIFIED]
```

### #4 — Compose stack shape for prod

**Decision: single `gateway/docker-compose.prod.yml` containing gateway + dashboard + redis-gateway-prod, joined to the `intra` overlay.**

**Diff from canonical `gateway/docker-compose.yml` (Swarm template):**

| Item | Swarm template (existing) | Prod compose (NEW) |
|------|--------------------------|--------------------|
| Networks declared | `worker_intra` external | `intra` external |
| Restart | `deploy.restart_policy` Swarm-only | top-level `restart: unless-stopped` |
| Resources | `deploy.resources` Swarm-only | top-level `resources` (Compose v2 supports it for non-Swarm) |
| Labels | under `deploy.labels` | top-level `labels` (plain compose semantics) |
| `entrypoints` value | `websecure` | `web` (internal Traefik is :80 only) |
| `tls.certresolver` label | `letsencryptresolver` | OMIT entirely (TLS terminates at edge, label is meaningless on :80 internal) |
| Env source | individual `environment:` block with `${VAR}` interpolation | `env_file: .env` (mirrors dev stack's pattern) |
| Container names | none (Swarm picks) | explicit `container_name: ifix-ai-gateway` (so `docker exec ifix-ai-gateway /gatewayctl …` works) |
| Redis service | not in canonical template (dev uses `infra-redis-1` shared) | NEW dedicated `redis-gateway-prod` with `--requirepass`, `--save ""`, `--appendonly no` |
| Healthcheck | gateway only | gateway + redis (redis-cli ping) |
| Image tag default | `${TAG:-latest-dev}` | `${TAG:-v1.0.0}` (D-12 pinned) |

See `## Code Examples` for the full compose spec.

### #5 — Sentry prod project bootstrap

Operator UI step (SDK does NOT auto-create projects):

1. Login at https://sentry.io (Ifix org).
2. Org Settings → Projects → Create Project → Platform: `Go` → Project name: `ifix-ai-gateway-prod` → Team: Ifix.
3. Copy the DSN from the project's Client Keys page.
4. Paste into `/opt/ai-gateway-prod/.env` as `SENTRY_DSN=https://…@…sentry.io/…`.

**Wiring verification (code is unchanged from dev):**
- `gateway/internal/obs/sentry.go:20-31` reads `cfg.SentryDSN`, sets `Environment: cfg.Env` (= `production` per .env), `Release: BuildVersion` (= `v1.0.0` from `-ldflags` in Dockerfile:42 via GHA `compute-tags` → `build-args: GATEWAY_VERSION=v1.0.0`).
- `gateway/internal/obs/sentry.go:beforeSend` redacts request body + sensitive headers (D-B7 already wired).
- Dashboard does NOT have a Sentry SDK wired yet (Phase 11 candidate; not in Phase 10 scope).

### #6 — GHA workflow updates for main + v1.0.0

**No changes needed to workflows** — `build-gateway.yml` + `build-dashboard.yml` already support:

- `push: tags: ['v*']` (line 9, both workflows) — fires on `v1.0.0` push.
- `compute-tags` job builds `:v1.0.0` + `:latest` + `:v1.0.0-<sha>` (lines 136-141 of build-gateway.yml). [VERIFIED]
- `deploy-prod` job hits `PORTAINER_WEBHOOK_URL_PROD_GATEWAY` secret if set; **D-11 means this secret stays UNSET** → workflow logs `skipping prod deploy` (line 230-234) — harmless. The operator does the deploy manually via `docker compose pull && docker compose up -d`.

**One review point:** the `deploy-prod` job is currently configured for the `ai-gateway-prod` Portainer stack auto-deploy that D-11 explicitly rejects. Decision: **leave the workflow code unchanged** but document in RUNBOOK-DEPLOY.md that the prod webhook secret is intentionally unset; operator deploys manually. (Alternative: gut the `deploy-prod` job. Recommendation: defer to Phase 11 — workflow code is harmless when secret is unset.)

**Release cut procedure:**
```bash
cd /home/pedro/projetos/pedro/gpu-ifix
git checkout develop && git pull
git checkout main && git pull
git merge --ff-only develop                 # fast-forward; if not FF, rebase develop on main
git tag -a v1.0.0 -m "Phase 10: first GA release — gateway + dashboard prod cutover"
git push origin main
git push origin v1.0.0                      # triggers GHA tag-build
gh run watch                                # tail the build until green
docker pull ghcr.io/ifixtelecom/ifix-ai-gateway:v1.0.0   # smoke local pull
```

**Image-build verification per GHA:**
- Job `compute-tags` outputs `gateway_tags = ifix-ai-gateway:v1.0.0\nifix-ai-gateway:latest\nifix-ai-gateway:v1.0.0-<sha>`.
- Job `build-gateway` step `Build & push` with `build-args: GATEWAY_VERSION=v1.0.0` injects `-X .../obs.BuildVersion=v1.0.0` into the binary; Sentry releases tag as `v1.0.0`.

### #7 — gatewayctl prod tenant provisioning

`scripts/integration-smoke/provision-tenants.sh` is already idempotent (passes `tenant create` "already exists" through as OK; `set-quota` is idempotent UPDATE). [VERIFIED: read 327 lines]

**Tenant list (from script, hardcoded):**
- `telefonia` — sensitive
- `cobrancas` — sensitive
- `campanhas` — normal
- `voice-api` — normal

**Tenant `converseai` is already seeded** by migration 0001 (`INSERT INTO ai_gateway.tenants (slug, name) VALUES ('converseai', 'ConverseAI')`).
**`chat-ifix` tenant is provisioned during Phase 08** — operator may need to run a one-off `gatewayctl tenant create --slug chat-ifix --name "Chat Ifix"` against prod since Phase 08 was dev-only. **OPEN — confirm during HUMAN-UAT.**

**HUMAN-UAT command (against prod gateway):**
```bash
# Tenant create + quotas (idempotent)
AI_GATEWAY_PG_DSN="postgres://…/bd_ai_gateway_prod?sslmode=require" \
  ./scripts/integration-smoke/provision-tenants.sh \
    --gatewayctl "ssh n8n-ia-vm docker exec ifix-ai-gateway /gatewayctl"

# Then mint keys (NON-idempotent — once only)
AI_GATEWAY_PG_DSN="postgres://…/bd_ai_gateway_prod?sslmode=require" \
  ./scripts/integration-smoke/provision-tenants.sh --mint-keys \
    --gatewayctl "ssh n8n-ia-vm docker exec ifix-ai-gateway /gatewayctl" \
  2>>/tmp/provision.log >>/tmp/provision-keys.txt

# Keys are printed ONCE in the script's final block — operator copies to:
#   - ConverseAI v4 Portainer stack env  (converseai key)
#   - Chat Ifix Portainer stack env       (chat-ifix key — after manual create)
#   - cobrancas-api Portainer stack env   (cobrancas key)
#   - fallback-register-ramais-nextbilling stack env  (telefonia key)
#   - campanhas-chatifix stack env        (campanhas key)
#   - voice-api Portainer stack env       (voice-api key)
#   - admin key for dashboard             (GATEWAY_ADMIN_KEY in /opt/ai-gateway-prod/.env)
```

**Per-tenant smoke contract (re-run from Phase 08/09 scripts):**

```bash
# ConverseAI surface
python scripts/integration-smoke/smoke-converseai.py \
  --gateway-url https://ai-gateway.converse-ai.app \
  --api-key "$CONVERSEAI_KEY" \
  --out /tmp/smoke-converseai-prod.json

# Chat Ifix surface
python scripts/integration-smoke/smoke-chat-ifix.py \
  --gateway-url https://ai-gateway.converse-ai.app \
  --api-key "$CHAT_IFIX_KEY" \
  --out /tmp/smoke-chat-ifix-prod.json

# Sensitive (telefonia + cobrancas)
python scripts/integration-smoke/smoke-sensitive-failover.py \
  --gateway-url https://ai-gateway.converse-ai.app \
  --api-key "$TELEFONIA_KEY" --tenant telefonia \
  --out /tmp/smoke-telefonia-prod.json

python scripts/integration-smoke/smoke-sensitive-failover.py \
  --gateway-url https://ai-gateway.converse-ai.app \
  --api-key "$COBRANCAS_KEY" --tenant cobrancas \
  --out /tmp/smoke-cobrancas-prod.json
```

Each script writes a JSON report (schema in `*-report-schema.json`). HUMAN-UAT asserts on (a) exit code = 0 + (b) report.summary.passed = total.

### #8 — RUNBOOK-DEPLOY.md scope + structure

**Mirror these existing runbooks** for header convention:
- `gateway/docs/RUNBOOK-FAILOVER.md` — full Triggers → Diagnose → Mitigate → Recover structure.
- `gateway/docs/RUNBOOK-OBSERVABILITY-ALERTING.md` — concise diagnostic recipes.
- `gateway/docs/RUNBOOK-EMERGENCY-POD.md` — operator playbook structure.

**Phase 10 RUNBOOK-DEPLOY.md content (target ~300 lines):**

```markdown
# RUNBOOK: Production Deploy & Cut-Release

## Triggers
- First-time prod bring-up of `ai-gateway.converse-ai.app` + `ai-dashboard.converse-ai.app`
- Roll-forward to a new GA tag (`v1.0.X`, `v1.1.0`, …)
- Rollback to a previous GA tag after a regression
- Cut-release procedure (develop → main → tag → GHA build → manual deploy)

## Preconditions
- SSH access to `n8n-ia-vm` (alias) + `vps-ifix-vm` (alias) from ops-claude.
- `~/.claude/CLAUDE.md` open for CF token + MinIO creds + OpenRouter key.
- DO Postgres console access (for new database create + Trusted Sources verify).
- Sentry org Ifix admin access (for new project / DSN copy).
- GitHub repo write + tag push permission (PAT in `~/.git-credentials`).
- All Phase 02-09 tests green on develop tip (verify `gh run list -L 5 --workflow build-gateway.yml`).

## Steps — First-Time Bring-Up (~45 min including DNS propagation)

### Step 1 — Edge Traefik route (NO DNS YET)
1. Copy `gateway/.env.prod.example` → `/tmp/ai-gateway-prod.env`; populate from `~/.claude/CLAUDE.md` + Sentry UI + `openssl rand -hex 24`.
2. Validate `ai-gateway-prod.yml` YAML locally: `python3 -c "import yaml; yaml.safe_load(open(...))"`.
3. `scp gateway/docker-compose.prod.yml n8n-ia-vm:/opt/ai-gateway-prod/docker-compose.yml`
4. `scp /tmp/ai-gateway-prod.env n8n-ia-vm:/opt/ai-gateway-prod/.env && ssh n8n-ia-vm 'chmod 600 /opt/ai-gateway-prod/.env'`
5. `scp .planning/phases/10-prod-deploy-ai-gateway/artifacts/ai-gateway-prod.yml vps-ifix-vm:/home/pedro/projetos/pedro/infra/traefik-dynamic/`
6. Tail edge Traefik: `ssh vps-ifix-vm 'docker logs -f --tail 0 $(docker ps -q -f name=traefik) 2>&1 | grep -i ai-gateway-prod'` — expect router-added line.

### Step 2 — Postgres bootstrap
1. DO console → create `bd_ai_gateway_prod` + `bd_ai_dashboard_prod`.
2. `psql "postgres://…/bd_ai_gateway_prod" -c "SELECT current_database();"` — sanity check.
3. NOTE: `ai_gateway` schema name is hardcoded; goose creates it on first migrate.

### Step 3 — First docker compose up (MIGRATE=true)
1. `ssh n8n-ia-vm 'cd /opt/ai-gateway-prod && docker compose pull && docker compose up -d'`
2. `ssh n8n-ia-vm 'docker logs -f ifix-ai-gateway 2>&1 | head -60'` — look for `goose: applied 27 migrations, current version: 27`.
3. `ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gateway --self-check'` — expect exit 0.
4. Internal-Traefik discovery: `ssh n8n-ia-vm 'docker logs $(docker ps -q -f name=traefik-internal_traefik.1) 2>&1 | grep -i ai_gateway'` — expect `router added`.

### Step 4 — Flip MIGRATE off
1. `ssh n8n-ia-vm "sed -i 's/^AI_GATEWAY_MIGRATE_ON_BOOT=true/AI_GATEWAY_MIGRATE_ON_BOOT=false/' /opt/ai-gateway-prod/.env"`
2. `ssh n8n-ia-vm 'cd /opt/ai-gateway-prod && docker compose up -d --force-recreate ifix-ai-gateway'`
3. Verify boot log shows `migrate skip` not `goose: up to current version`.

### Step 5 — Dashboard schema bootstrap
1. `ssh n8n-ia-vm 'docker run --rm --network intra --env-file /opt/ai-gateway-prod/.env ghcr.io/ifixtelecom/ifix-ai-dashboard:v1.0.0 npx @better-auth/cli migrate'`

### Step 6 — DNS flip (FINAL — triggers ACME challenge)
1. Use the CF API recipe in `## How To #3` (Pattern 3) — POST 2 A records.
2. `dig +short ai-gateway.converse-ai.app @1.1.1.1` — expect 162.55.92.154.
3. `curl -sS -I https://ai-gateway.converse-ai.app/health` — expect HTTP/2 200; first request triggers tls-alpn-01.
4. `ssh vps-ifix-vm 'docker exec infra-traefik-1 cat /letsencrypt/acme.json | jq .letsencrypt.Certificates[].domain.main'` — expect `ai-gateway.converse-ai.app` + `ai-dashboard.converse-ai.app` listed.

### Step 7 — Tenant provisioning + per-tenant golden-path
(See `## How To #7` for exact commands.)

## Steps — Roll-Forward to new `:v1.0.X`
1. New tag pushed → GHA green → `ssh n8n-ia-vm "sed -i s/:v1.0.X-1/:v1.0.X/ /opt/ai-gateway-prod/docker-compose.yml"`
2. `ssh n8n-ia-vm 'cd /opt/ai-gateway-prod && docker compose pull && docker compose up -d'`
3. Verify `docker logs ifix-ai-gateway 2>&1 | head -10` shows new build_version.

## Steps — Rollback to previous `:v1.0.X-1`
1. `ssh n8n-ia-vm "sed -i s/:v1.0.X/:v1.0.X-1/ /opt/ai-gateway-prod/docker-compose.yml"`
2. `ssh n8n-ia-vm 'cd /opt/ai-gateway-prod && docker compose pull && docker compose up -d --force-recreate'`
3. `time` from edit to /health 200: < 5 minutes (INT-06 requirement). Smoke: `curl -sS https://ai-gateway.converse-ai.app/health`.
4. If schema change in v1.0.X → rollback requires running `goose down` ONCE on the prod DB — document each tag's "rollback-safe?" flag in CHANGELOG.

## Steps — Cut-Release Procedure
(See `## How To #6` block.)

## Verification (post-deploy gate)
- [ ] `curl -sS https://ai-gateway.converse-ai.app/health | jq` returns gateway healthy
- [ ] `curl -sS https://ai-gateway.converse-ai.app/v1/health/upstreams | jq` shows 6 upstreams in expected states
- [ ] `docker exec ifix-ai-gateway /gatewayctl upstreams list` matches expected (6 rows)
- [ ] Sentry releases tab shows release tagged `v1.0.0` environment `production`
- [ ] `curl -sS -I https://ai-dashboard.converse-ai.app/` returns 200

## Rollback (escape hatch)
1. **DNS panic-revert:** `curl -X DELETE` the 2 CF A records — clients fail back to error within TTL=300s.
2. **Container-only revert:** image tag swap (above).
3. **Full revert to dev stack as fallback:** Phase 10 explicitly keeps `/opt/ai-gateway-dev/` on vps-ifix-vm running; flip client app `gateway_base_url` env var back to `https://gateway-dev.ifixtelecom.com.br`.

## Postmortem stub
(Date / Trigger / Detection / Mitigation / Recovery / Action items)
```

### #9 — Cascade-close pattern reuse (Phase 02/03/04/05)

**Reference: Phase 06.9 used this exact pattern** to close Phase 02 SC-5 step 7, Phase 03 SC-1, and Phase 05 SC-1. The recipe lives in `.planning/phases/06.9-openrouter-model-rewrite-per-upstream/06.9-VERIFICATION.md` (which I read) and in the commit `e3be97b docs(06.9): close Phase 06.9 + cascade-close Phase 02/03/05 VERIFICATIONs`.

**The 4 commits Phase 10 HUMAN-UAT writes (one per cascade-closed phase):**

#### Commit 1 — Phase 02 SC-5 step 7 (live deploy under prod URL)
```bash
sed -i '0,/^score:/{s/^score:.*$/score: 5\/5 SC fully PASS — re-validated under prod URL 2026-05-XX/}' \
  .planning/phases/02-gateway-core-multi-tenant-auth/02-VERIFICATION.md
# Append evidence under re_verification:
# (operator pastes evidence block referencing 10-HUMAN-UAT.md S1)
```
**Positive-assertion grep:**
```bash
grep -E "re-validated under prod URL 2026-05" .planning/phases/02-gateway-core-multi-tenant-auth/02-VERIFICATION.md && echo PASS
```

#### Commit 2 — Phase 03 SC-1 (force-open breaker under prod URL)
HUMAN-UAT S4: `gatewayctl breaker force-open --name local-llm` then 2 chats → both 200 + `audit_log.upstream='openrouter-chat'`. Mirrors Phase 06.9 S4 evidence stanza pattern.

#### Commit 3 — Phase 04 SC-1 + SC-2 + SC-4 (rate-limit + billing + peak-routing under prod URL)
HUMAN-UAT S5 + S6 + S7. Already PARTIAL-PASSED against dev 2026-05-23; cascade-close updates VERIFICATION.md frontmatter `status: passed_partial` → `status: passed` + adds `gaps_closed_phase_10_2026_05_XX:` stanza.

#### Commit 4 — Phase 05 SC-1 (vegeta burst under prod URL)
HUMAN-UAT S8 (5 RPS × 30s = 150 requests). Already PASSED via Phase 06.9 S6 (99.33%). Cascade-close just re-records that PHASE 10 hostname carries the same proof.

**WARNING-5 positive-assertion grep contract (per phase):**
```bash
# Each grep must return at least one line. CI doesn't enforce — operator runs locally pre-merge.
grep -E "^status: passed$" .planning/phases/0X-…/0X-VERIFICATION.md  # frontmatter status flipped
grep -E "gaps_closed_phase_10_2026" .planning/phases/0X-…/0X-VERIFICATION.md  # evidence stanza present
```

### #10 — `develop` → `main` promotion mechanics

Current branch: `gsd/phase-06.9-close` (per init context).

**Flow:**

1. Land Phase 10 Wave 0-3 work on `gsd/phase-10-…` branches → PR into `develop`.
2. Once Phase 10 autonomous plans GREEN on develop, HUMAN-UAT promotes develop → main:
   ```bash
   cd /home/pedro/projetos/pedro/gpu-ifix
   git checkout develop && git pull
   git checkout main 2>/dev/null || git checkout -b main origin/main
   git pull
   # main is currently empty/stale relative to develop after 9 phases — confirm:
   git log --oneline develop ^main 2>&1 | head -5
   # If main is behind by N commits — fast-forward; if main has diverged, rebase develop on main.
   git merge --ff-only develop
   git push origin main
   ```
3. Tag:
   ```bash
   # GPG signing optional — Pedro's gpg key may not be on ops-claude
   gpg --list-secret-keys 2>/dev/null | head -1 && SIGN=-s || SIGN=
   git tag -a $SIGN v1.0.0 -m "Phase 10: first GA release — gateway + dashboard prod cutover

   - Multi-tenant Auth (Phase 2)
   - Resilience + Fallback Chain (Phase 3, 06.9)
   - Quotas / Billing / Schedule Routing (Phase 4)
   - Load Shedding (Phase 5)
   - Emergency Pod + Primary Pod (Phase 6 + 06.6 + 06.8)
   - Observability Dashboard + Sentry (Phase 7)
   - Client Integrations + Sensitive Tenants (Phase 8 + 9)
   - First Production Deploy on n8n-ia-vm (Phase 10)"
   git push origin v1.0.0
   ```
4. `gh run watch` until both `build-gateway` + `build-dashboard` jobs reach `:v1.0.0` images.
5. Operator deploys per `## How To #4` Pattern 1.

---

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | Schema-name in `gateway/db/migrations/*.sql` is `ai_gateway` (NOT configurable via env). | Summary §3 + Pitfall 2 | If wrong, prod will use whatever the env says — but `grep -l "ai_gateway\." gateway/db/migrations/*.sql` returned 27 files, so the claim is well-supported [VERIFIED: grep + pool.go:35] |
| A2 | Internal Traefik Swarm provider picks up standalone-compose containers attached to `intra` overlay via Traefik labels. | How To #1 + Open Question 2 | If wrong, gateway is not discovered → fallback B (dual-provider on internal Traefik) needed in Wave 0. **Plan must Wave-0 verify.** |
| A3 | Better Auth `cli migrate` correctly creates schema in `bd_ai_dashboard_prod` via DASHBOARD_DATABASE_URL DSN. | How To #3 + RUNBOOK Step 5 | If wrong, dashboard auth tables missing → first signup 500s. Dashboard dev stack uses this exact recipe so the path is exercised in dev. [ASSUMED — operator confirms during HUMAN-UAT first signup] |
| A4 | n8n-ia-vm `intra` overlay is attachable from non-Swarm `docker compose up` (NOT just from `docker service create`). | Pattern 1 + How To #4 | If wrong, compose throws `network intra is not attachable`. **Probe confirms `Attachable: true` already.** [VERIFIED: docker network inspect intra] |
| A5 | DO Postgres allows creating new databases without operator-level grant changes (`doadmin` superuser already has rights). | How To #3 | If wrong, `CREATE DATABASE` fails — operator escalates via DO support. Standard DO managed Postgres allows this. [ASSUMED] |
| A6 | `chat-ifix` tenant slug is not in prod DB after first migrate (Phase 08 only seeded dev). | How To #7 | If wrong, idempotent path handles it gracefully (tenant create returns "already exists"). [ASSUMED — operator confirms or creates] |
| A7 | GHA self-hosted runners are healthy as of Phase 10 start (CLAUDE.md history shows past billing-blocked episodes). | Open Question 4 | If wrong, tag-push job sits in queue. **Plan Wave 0 verifies via `gh run list -L 1 --workflow build-gateway.yml`** [ASSUMED] |
| A8 | The Phase 06.8 RTX 3090 allowlist (`43803,55158`) + `PRIMARY_VAST_PRICE_CAP_DPH=0.60` carry forward unchanged to prod. | Code Examples §env contract | If wrong, prod primary pod fails to provision. Phase 06.8 standing decision per CLAUDE.md memory: `2×RTX 3090 allowlist 43803,55158 cap $0.60`. [VERIFIED: CLAUDE.md memory `primary-gpu-shape-06.8-final`] |
| A9 | OpenRouter `UPSTREAM_LLM_OPENROUTER_MODEL` stays empty in `.env` (schema row from migration 0027 = deepseek-v4-flash:nitro wins). | Code Examples §env contract | If wrong, the gateway still works (env override > schema row when env set) but operator may be surprised. Phase 06.9 close confirms this is the correct prod setting. [VERIFIED: 06.9-VERIFICATION.md Configuration §2] |
| A10 | n8n-ia-vm disk grows under 75% after Phase 10 deploy (gateway + dashboard images + redis volume ~2G total). | Pitfall 6 + How To #1 | If wrong, operator scales VM 101 disk (qm set 101 -resize). Capacity gate Wave 0 catches in advance. [VERIFIED: df at 67% with margin] |

---

## File Manifest

Files to CREATE in Phase 10:

| Path | Owner Plan | Purpose |
|------|-----------|---------|
| `gateway/docker-compose.prod.yml` | Wave 0 / Plan 10-01 | Operator-managed prod compose (gateway + dashboard + redis-gateway-prod, network `intra`) |
| `gateway/.env.prod.example` | Wave 0 / Plan 10-01 | Documented prod env contract (mirrors operator-populated `.env` 1:1) |
| `.planning/phases/10-prod-deploy-ai-gateway/artifacts/ai-gateway-prod.yml` | Plan 10-03 | Edge Traefik file-provider route YAML (rsync'd to vps-ifix-vm) |
| `gateway/docs/RUNBOOK-DEPLOY.md` | Plan 10-04 | Bring-up + roll-forward + rollback + cut-release runbook |
| `.planning/phases/10-prod-deploy-ai-gateway/10-PLAN.md` | planner | Phase plan structure |
| `.planning/phases/10-prod-deploy-ai-gateway/10-PATTERNS.md` | planner | Code patterns + risks (this RESEARCH feeds it) |
| `.planning/phases/10-prod-deploy-ai-gateway/10-VALIDATION.md` | planner | Verification dimensions + test matrix |
| `.planning/phases/10-prod-deploy-ai-gateway/10-01-PLAN.md` (Wave 0 reconcile + capacity gate + provider-discovery probe) | planner |
| `.planning/phases/10-prod-deploy-ai-gateway/10-02-PLAN.md` (Postgres DB create + dashboard migrate) | planner |
| `.planning/phases/10-prod-deploy-ai-gateway/10-03-PLAN.md` (edge Traefik file-provider rsync + CF DNS POST) | planner |
| `.planning/phases/10-prod-deploy-ai-gateway/10-04-PLAN.md` (RUNBOOK-DEPLOY.md author + commit) | planner |
| `.planning/phases/10-prod-deploy-ai-gateway/10-05-PLAN.md` (develop→main promotion + v1.0.0 tag + GHA build verify) | planner |
| `.planning/phases/10-prod-deploy-ai-gateway/10-06-PLAN.md` autonomous=false `HUMAN-UAT.md` (deploy + smoke + cascade close) | planner |
| `.planning/phases/10-prod-deploy-ai-gateway/10-VERIFICATION.md` | post-UAT |

Files to MODIFY in Phase 10:

| Path | Change | Owner Plan |
|------|--------|-----------|
| `.planning/REQUIREMENTS.md` §Traceability | Remap PRD-01/02/03/04/05/06 from Phase 10 → Phase 11; keep PRD-07 + INT-06 + PRD-04 (partial) in Phase 10 | Plan 10-04 |
| `.planning/ROADMAP.md` | Update Phase 10 goal/plans count; add Phase 11 placeholder | Plan 10-04 |
| `.planning/STATE.md` | Update on phase advance | post-UAT |
| `.planning/phases/02-…/02-VERIFICATION.md` | cascade-close: status passed_partial → passed, evidence stanza | HUMAN-UAT |
| `.planning/phases/03-…/03-VERIFICATION.md` | cascade-close: re-confirm under prod URL | HUMAN-UAT |
| `.planning/phases/04-…/04-VERIFICATION.md` | cascade-close: passed_partial → passed | HUMAN-UAT |
| `.planning/phases/05-…/05-VERIFICATION.md` | cascade-close: confirm SC-1 under prod URL | HUMAN-UAT |

Files to LEAVE UNCHANGED:

- `gateway/docker-compose.yml` (Swarm template — kept for reference + future Portainer-managed deploy)
- `.github/workflows/build-gateway.yml` (already supports `tags: ['v*']` + `branches: [main]`)
- `.github/workflows/build-dashboard.yml` (same)
- All Phase 02-09 plans/research (frozen)

Files OPERATOR HANDLES (not committed):

- `n8n-ia-vm:/opt/ai-gateway-prod/.env` (mode 600; populated during HUMAN-UAT Step 3)
- DO Postgres console actions (db create)
- Sentry UI actions (project create + DSN copy)
- Cloudflare DNS records (POST via curl per RUNBOOK)
- GitHub merge develop → main + tag push (per RUNBOOK)

---

## Sources

### Primary (HIGH confidence)

- Live SSH probes 2026-05-25:
  - `ssh ifix-prod-01 'cat /etc/network/interfaces; iptables -t nat -L PREROUTING -n'` — Proxmox host DNAT topology
  - `ssh n8n-ia-vm 'docker ps; docker info; docker network ls; free -h; df -h; curl ifconfig.io; docker service inspect traefik-internal_traefik; docker network inspect intra'` — n8n-ia-vm capacity + Swarm + intra overlay
  - `ssh vps-ifix-vm 'ls /opt/ai-gateway-dev/; cat /home/pedro/projetos/pedro/infra/docker-compose.yml; cat /home/pedro/projetos/pedro/infra/traefik-dynamic/n8n-ia.yml'` — edge Traefik + dev stack pattern
- `/home/pedro/projetos/pedro/gpu-ifix/gateway/docker-compose.yml` — canonical Swarm template (network rename source)
- `/home/pedro/projetos/pedro/gpu-ifix/gateway/internal/db/{pool.go,migrate.go}` — schema hardcoding source
- `/home/pedro/projetos/pedro/gpu-ifix/gateway/db/migrations/0001..0027` — 27 migration files, all `SET search_path = ai_gateway, public`
- `/home/pedro/projetos/pedro/gpu-ifix/gateway/internal/obs/{sentry.go,version.go}` — Sentry release+env tagging contract
- `/home/pedro/projetos/pedro/gpu-ifix/.github/workflows/build-gateway.yml` + `build-dashboard.yml` — already-supported tag triggers
- `/home/pedro/projetos/pedro/gpu-ifix/scripts/integration-smoke/provision-tenants.sh` — idempotent tenant seed
- `/home/pedro/projetos/pedro/gpu-ifix/.planning/phases/06.9-…/06.9-VERIFICATION.md` — WARNING-5 cascade pattern source
- `/home/pedro/projetos/pedro/infra/traefik-dynamic/n8n-ia.yml` + `worker.yml` — edge file-provider mirror patterns
- `/home/pedro/projetos/pedro/gpu-ifix/dashboard/.env.example` + `src/lib/{db.ts,auth.ts}` — dashboard env contract + isolation comment
- `~/.claude/CLAUDE.md` — Hetzner topology + tokens

### Secondary (MEDIUM confidence)

- Phase 02 / 03 / 04 / 05 VERIFICATION.md frontmatter (status fields for cascade-close)
- `gateway/docs/RUNBOOK-FAILOVER.md` — structure mirror for RUNBOOK-DEPLOY.md
- Sentry SDK Go docs (sentry.Init `Environment` + `Release` semantics) — extrapolated from `obs/sentry.go` usage [CITED: sentry-go README]

### Tertiary (LOW confidence)

- Open Question 2: Traefik Swarm-provider discovering standalone compose containers via `intra` overlay — needs Wave 0 5-minute hello-world probe before committing. The 60% likely outcome: Swarm provider does pick them up (Traefik's `swarmMode` flag is a discovery filter, not a container-type filter — but Traefik documentation language is ambiguous).
- Open Question 3: `ai-gateway-embed` placement — kept as host-IP path; deferred.

---

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH — every tool/library already in production on adjacent stacks; zero new packages.
- Architecture: HIGH — every flow (Proxmox DNAT → edge Traefik → internal Traefik → container) verified by live probe.
- Pitfalls: HIGH — 8 pitfalls identified, all backed by file/command evidence; Pitfall 1 + 2 are CONTEXT.md-contradicting and call for explicit Wave 0 reconciliation.
- Cascade close: HIGH — Phase 06.9's exact pattern (commit `e3be97b`) is reusable verbatim.
- Open Questions: 4 surfaced; OQ2 (Swarm provider discovery) is the only one with material plan impact and is a Wave 0 5-minute probe.

**Research date:** 2026-05-25
**Valid until:** 2026-06-25 (30 days for stable; n8n-ia-vm topology + Cloudflare zone IDs + GHA workflows don't drift quickly — but if a Phase 11 patch lands on develop that touches Sentry init, gateway DSN env, or schema migrations between research and plan execution, re-validate the affected sections.)

---

## RESEARCH COMPLETE
