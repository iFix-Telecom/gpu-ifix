# Phase 10: prod-deploy-ai-gateway - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-05-25
**Phase:** 10-prod-deploy-ai-gateway
**Areas discussed:** Host + DNS subdomain, Data layer prod (Postgres + Redis), Secrets + deploy method + branch, Validation + monitoring (PRD-01..07)

---

## Area Selection

| Option | Description | Selected |
|--------|-------------|----------|
| Host + DNS subdomain | VM dedicada vs DO vs Hetzner Cloud + subdomain final | ✓ |
| Data layer prod (Postgres + Redis) | DO managed shared vs dedicado + Redis container vs reuse vs cloud | ✓ |
| Secrets + deploy method + branch | Keys separadas vs reuse, Portainer vs ArgoCD, develop→main agora | ✓ |
| Validation + monitoring (PRD-01..07) | Sentry, load, chaos, runbook, LGPD, dashboard SSO, DNS | ✓ |

**User's choice:** All 4 areas.

---

## Host

| Option | Description | Selected |
|--------|-------------|----------|
| Nova VM no dedicado ifix-prod-01 (Recommended) | Clone template 9000 → prod-gateway-vm, ~4c/8G/40G, ops-claude pattern | |
| Reuse VPS prod 5.161.207.105 (Hetzner Cloud) | Colocate com converseai-v4 prod stack; blast-radius concern | |
| DigitalOcean droplet novo | ~$24/m additional, different provider | |

**User's choice:** Free-text — "vm 101, aonde já tem um container ai-gateway-embe..."
**Notes:** User chose VM 101 = `n8n-ia-vm` (10.10.10.20), where `ai-gateway-embed` Infinity already runs 24/7. Colocates tier-0 embed + gateway. Zero new VM; reuse existing Traefik internal + Portainer agent + Swarm setup. Verified `ssh n8n-ia-vm 'docker ps'` — `ai-gateway-embed michaelf34/infinity:0.0.77` healthy on :7997, plus `traefik-internal_traefik.1`, `portainer_agent`, n8n + rabbitmq + postgres-pgvector + redis containers.

---

## DNS

| Option | Description | Selected |
|--------|-------------|----------|
| `ai-gateway.ifixtelecom.com.br` + `ai-dashboard.ifixtelecom.com.br` (Recommended) | Symmetric to dev (only swap prefix) | |
| `gateway.ifixtelecom.com.br` + `dashboard.ifixtelecom.com.br` | Shorter; naming conflict risk for future generic gateways | |
| `api.converseai.app.br` + `dashboard.converseai.app.br` | Couples to ConverseAI product (wrong: gateway serves 6 apps) | |

**User's choice:** Free-text — "opção 1 com dominio converseai.app", clarified plain-text follow-up = `converse-ai.app` (hyphenated).
**Notes:** Final hostnames: `ai-gateway.converse-ai.app` + `ai-dashboard.converse-ai.app`. Zone `converse-ai.app` controlled via CF token in `~/.claude/CLAUDE.md` (zone_id `0e779b74b86957bdb628d646dbf33978`).

---

## Postgres

| Option | Description | Selected |
|--------|-------------|----------|
| Reuse DO managed shared, novo schema `ai_gateway_prod` (Recommended) | Same DO instance; schema isolation dev/prod; zero added cost | ✓ |
| Reuse DO managed shared, mesmo schema `ai_gateway` | Dangerous: migrations + tenant keys + audit_log mix dev+prod | |
| Cluster Postgres dedicado novo | Max isolation; $15-50/m DO or self-host on VM | |

**User's choice:** Reuse DO managed shared, novo schema `ai_gateway_prod`.

---

## Redis

| Option | Description | Selected |
|--------|-------------|----------|
| Container Redis dedicado no n8n-ia-vm (Recommended) | New `redis-gateway-prod`; locality, latency ~0ms, isolation | ✓ |
| Reuse `n8n_redis` da VM 101 | Zero overhead; risk of TTL collisions / accidental flush | |
| Redis Cloud managed externo | Adds 10-30ms latency Hetzner DE ↔ cloud; harms breaker observation cycle | |

**User's choice:** Container Redis dedicado no n8n-ia-vm.

---

## Keys

| Option | Description | Selected |
|--------|-------------|----------|
| Keys NOVAS dedicadas prod (Recommended) | OpenRouter project tag prod + OpenAI new key + Vast pod labels; clean LGPD audit + independent revoke | |
| Reuse keys dev em prod | Immediate; spend mix; no per-env billing audit; coupled revoke | ✓ |
| Decidir caso-a-caso (Vast reuse, OpenRouter/OpenAI separar) | Vast = one account, separate via labels; OR + OAI new keys; pragmatic | |

**User's choice:** Reuse keys dev em prod.
**Notes:** Operator-accepted trade-off; spend mixing + coupled revoke documented as known limitation. Revisit when per-env billing separation becomes a hard requirement.

---

## Deploy

| Option | Description | Selected |
|--------|-------------|----------|
| Portainer stack `ai-gateway-prod` + webhook GitHub (Recommended) | Repository mode, webhook auto-pull, converseai-v4 prod pattern | |
| Direct docker compose operator-managed (mesmo padrão dev) | rsync + `docker compose up -d`; no GitOps trail | ✓ |
| ArgoCD | Requires K8s; gpu-ifix is Docker Compose/Swarm; migration not worth it | |

**User's choice:** Direct docker compose operator-managed.
**Notes:** Mirrors `/opt/ai-gateway-dev/` operational pattern. Operator runs `docker compose pull && docker compose up -d` to roll forward. Pin image tags (`:v1.0.0`) so rollback is deterministic.

---

## Branch

| Option | Description | Selected |
|--------|-------------|----------|
| Promover develop→main agora; prod consome `main` tag (Recommended) | Cut release main + v1.0.0; GHA builds `:main`/`:latest`/`:v1.0.0`; dev keeps `:latest-dev` | ✓ |
| Prod consome `develop` mesmo (no main) | Dev = prod tip; no gate; rejected | |
| Manter develop→dev forever, main release manual | Diferred main cut; dual-deploy + risk of forgetting | |

**User's choice:** Promover develop→main agora; prod consome `main` tag.
**Notes:** Cut `v1.0.0` as part of Phase 10. Prod pins `:v1.0.0` not floating `:main`.

---

## Sentry

| Option | Description | Selected |
|--------|-------------|----------|
| Novo projeto `ifix-ai-gateway-prod` na org Ifix Sentry (Recommended) | Separate DSN; alerts isolation; release tags + retention separate per env | ✓ |
| Reuse projeto dev `ifix-ai-gateway` com env tag | Single project + environment tag; alert fatigue risk | |
| Sem Sentry em prod (só logs Loki) | Loses traceback + release tracking + alert critical; rejected | |

**User's choice:** Novo projeto `ifix-ai-gateway-prod`.

---

## Scope

| Option | Description | Selected |
|--------|-------------|----------|
| Phase 10 = DEPLOY only; split PRD-01..06 → Phase 11 (Recommended) | Deploy + DNS + TLS + smoke + RUNBOOK-DEPLOY.md + cut release; hardening in Phase 11 | ✓ |
| Phase 10 = full PRD-01..07 (deploy + hardening end-to-end) | Single big phase; 4-6 weeks; drift risk | |
| Phase 10 = deploy + PRD-01 + PRD-04 medium; PRD-02/03/05/06 split | Middle ground | |

**User's choice:** Phase 10 = DEPLOY only.
**Notes:** REQUIREMENTS.md traceability table will need to be updated to remap PRD-01/02/03/04(full)/05/06 from Phase 10 → new Phase 11: prod-hardening (created post-Phase-10 close).

---

## UAT

| Option | Description | Selected |
|--------|-------------|----------|
| HUMAN-UAT.md autonomous=false no final (Recommended) | Autonomous plans setup/migrate/deploy/smoke; operator HUMAN-UAT cut-release + cascade VERIFICATION close | ✓ |
| Tudo autonomous (sem HUMAN-UAT) | Auto-cut release sans human gate; risky | |
| Tudo HUMAN (só doc + checklist, sem autonomous deploy) | Slowest; not reproducible | |

**User's choice:** HUMAN-UAT.md autonomous=false no final.
**Notes:** Cascade close Phase 02/03/04/05 VERIFICATION.md live deferrals inside the HUMAN-UAT plan (mirror Phase 06.9 WARNING-5 positive-assertion grep pattern).

---

## Done Check

| Option | Description | Selected |
|--------|-------------|----------|
| Pronto p/ CONTEXT.md | Write 10-CONTEXT.md + DISCUSSION-LOG.md + commit; next `/gsd:plan-phase 10` | ✓ |
| Explorar mais gray areas | 2-4 lacunas adicionais (secrets storage, observability stack, capacity, ingress topology) | |

**User's choice:** Pronto p/ CONTEXT.md.

---

## Claude's Discretion

- Exact compose file split (single multi-service file vs split per-service).
- Swarm-mode (`deploy:` block) vs plain compose for the prod stack file.
- Whether the dashboard publishes a separate vhost or redirects from gateway.
- Redis password generator + storage method (likely `openssl rand -hex 24` + `.env` mode 600).
- Postgres `connection_pooler_mode` setting on the DO side.
- Cutover sequence (recommendation: net-new install on n8n-ia-vm; leave dev stack on vps-ifix-vm as fallback).

## Deferred Ideas

- PRD-01 production load test → Phase 11.
- PRD-02 chaos primary kill → Phase 11.
- PRD-03 chaos OpenRouter degraded → Phase 11.
- PRD-04 full incident-response runbook → Phase 11 (Phase 10 ships slim RUNBOOK-DEPLOY only).
- PRD-05 LGPD legal sign-off → external gate; tracked outside code.
- PRD-06 dashboard SSO / Better Auth hardening → Phase 11.
- Per-env upstream provider key separation → revisit when billing-per-env mandated.
- Portainer Repository / webhook auto-deploy migration → reconsider if operator burden grows.
- CI tag for `v1.0.x` patch releases → Phase 11+.
