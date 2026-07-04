---
phase: 19-gateway-consolidation-worker-vm
verified: 2026-07-04T20:35:00Z
status: passed
score: 9/9 requirements verified
overrides_applied: 0
---

# Phase 19: Gateway Consolidation → worker-vm — Verification Report

**Phase Goal:** worker-vm (10.10.10.50) becomes the single, Portainer-managed, consolidated ai-gateway (gateway + embed + rerank + dashboard + infra-redis-1 on Docker Swarm), serving all ~18 production tenants over the unchanged public hostname ai-gateway.converse-ai.app — with prod tenant/key hashes migrated verbatim into bd_ai_gateway, a reversible edge-Traefik-line cutover, and the old n8n-ia-vm (prod) + vps-ifix-vm (dev) gateways decommissioned.

**Verified:** 2026-07-04T20:35:00Z
**Status:** PASSED
**Re-verification:** No — initial verification

---

## Verification Methodology

This is a live-infra phase with no repo source code. All verifications were performed against the running system via SSH, curl, Docker inspect, and log analysis. No code was mutated.

---

## Requirements Coverage

| Req | Description | Status | Live Evidence |
|-----|-------------|--------|---------------|
| CONS-01 | All gateway stacks unified on worker-vm | VERIFIED | 4 Portainer stacks active on endpoint 6; n8n-ia-vm has 0 gateway containers; stack 34 (vps-ifix-vm) returns HTTP 404 |
| CONS-02 | Dev-style env: bd_ai_gateway + R2 + infra-redis-1 | VERIFIED | `AI_GATEWAY_PG_DSN=…/bd_ai_gateway?sslmode=require` confirmed via `docker service inspect`; `AI_GATEWAY_REDIS_ADDR=infra-redis-1:6379` confirmed |
| NET-01 | infra-redis-1 alias created | VERIFIED | `docker service inspect ai-gateway-prod_redis` shows alias `[infra-redis-1 redis]` on overlay `sv5po43sbxeq3u4szzqkmu98j`; gateway uses `infra-redis-1:6379` |
| EMB-01 | Embed moved to worker-vm pre-decommission | VERIFIED | `ai-gateway-embed_embed` 1/1 on worker-vm; `UPSTREAM_EMBED_URL=http://embed:7997` (overlay DNS); gateway logs show `upstream_embed=http://embed:7997`; n8n-ia-vm embed containers gone |
| UI-01 | All stacks Portainer endpoint 6, UI-editable | VERIFIED | Stacks 38 (gateway+redis), 39 (embed), 40 (dashboard), 41 (rerank) — all `ep=6 status=1 type=1` (swarm compose-string) |
| DB-01 | 18 tenants + 19 api_keys hash-verbatim | VERIFIED | Gateway logs: `tenants refreshed rows=18`; migrate.sql COMMIT log: `tenants=18, api_keys=19, api_hash_match=19`; prod tenant slugs (transcricao-voip, analise-transcr-voip, ia-kanban) active in admin metrics |
| DB-02 | admin_keys + usage_counters + model_aliases reconciled; billing archived | VERIFIED | Prod admin key `****613f` returns 200 on `/admin/metrics`; model_aliases: `count=19` from gateway refresh logs; billing landing in bd_ai_gateway (30 events in last 30min from worker-vm logs); bd_ai_gateway_prod retained (97MB source dump on ops-claude) |
| CUT-01 | Reversible edge-Traefik cutover, DNS-stable | VERIFIED | Edge file `ai-gateway-prod.yml` has 2 service blocks both pointing to `http://10.10.10.50:80`; DNS unchanged (162.55.92.154); backup `.bak-pre19-05` retained on vps-ifix-vm; cutover_ts `2026-07-04T19:22:52Z` recorded |
| DEC-01 | Decommission n8n-ia-vm + vps-ifix-vm gateways | VERIFIED | n8n-ia-vm: 0 gateway/embed/rerank/redis-gateway-prod containers (`docker ps` grep count=0); Portainer stack 34 HTTP 404; dirs + volumes retained (no `-v`); bd_ai_gateway_prod kept |

**Score: 9/9**

---

## Observable Truths

| # | Truth | Status | Live Evidence |
|---|-------|--------|---------------|
| 1 | `https://ai-gateway.converse-ai.app` serves 200 from worker-vm | VERIFIED | `curl https://ai-gateway.converse-ai.app/health` → 200 `{"status":"ok","uptime_s":68596,"version":"main-5553bd4"}` |
| 2 | All 5 gateway services running 1/1 on worker-vm swarm | VERIFIED | `docker service ls` confirms: gateway 1/1, redis 1/1, embed 1/1, rerank 1/1, dashboard 1/1 |
| 3 | gateway reads bd_ai_gateway (not bd_ai_gateway_prod) | VERIFIED | `AI_GATEWAY_PG_DSN` env points to `…/bd_ai_gateway?sslmode=require` in swarm service spec |
| 4 | infra-redis-1 alias resolves on the ai-gateway overlay | VERIFIED | Redis service alias `[infra-redis-1 redis]` on overlay `sv5po43sbxeq3u4szzqkmu98j`; gateway uses `infra-redis-1:6379` |
| 5 | Embed upstream is on worker-vm (not n8n-ia-vm 10.10.10.20) | VERIFIED | `UPSTREAM_EMBED_URL=http://embed:7997`; embed service on ai-gateway overlay only; n8n-ia-vm embed containers gone |
| 6 | 18 prod tenants + 19 api_keys present and serving | VERIFIED | Gateway refresh log: `tenants refreshed rows=18`; model aliases: `count=19`; billing events flowing from prod tenant_ids (622ebdc6, 82f0052b, dc813ef0, etc.) |
| 7 | Edge Traefik routes to worker-vm (DNS-stable, reversible) | VERIFIED | Both service blocks in `ai-gateway-prod.yml` point to `http://10.10.10.50:80`; 10.10.10.20 appears only in file comments; backup file retained |
| 8 | Old gateways decommissioned | VERIFIED | n8n-ia-vm: 0 gateway containers; stack 34: 404; dirs/volumes retained on n8n-ia-vm |
| 9 | Ops timers unmasked and repointed to worker-vm | VERIFIED | `gateway-price-sync.timer` + `prod-primary-report.timer` both `enabled` (not masked); scripts SSH to `worker-vm docker exec $(docker ps -q -f name=ai-gateway-prod_gateway...)` |

---

## Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| Portainer stack 38 `ai-gateway-prod` (endpoint 6) | gateway + redis, UI-editable | VERIFIED | Status=1, type=1 (swarm); image digest-pinned `@sha256:382a0fc8...` |
| Portainer stack 39 `ai-gateway-embed` (endpoint 6) | bge-m3 CPU, UI-editable | VERIFIED | Status=1, type=1; `infinity:0.0.77@sha256:11e8b39...` |
| Portainer stack 40 `ai-gateway-dashboard` (endpoint 6) | dashboard, login-proven | VERIFIED | Status=1, type=1; `ifix-ai-dashboard@sha256:07b7d18...` |
| Portainer stack 41 `ai-gateway-rerank` (endpoint 6) | rerank, direct /v1/rerank | VERIFIED | Status=1, type=1; port 7998 published; infinity:0.0.77 |
| `ai-gateway` overlay network on worker-vm | driver=overlay, attachable | VERIFIED | `docker network ls`: driver=overlay, scope=swarm |
| `ops-claude:~/gw-decomm-archive-19/` | root-600 rebuild archive | VERIFIED | Dir 700 root:root; n8n-ia-vm/ + vps-ifix-vm-stack34/ subdirs with compose/env-names/image-digests/inspect/logs |
| `ops-claude:~/gw-migration-19/` | pre-migration dumps root-600 | VERIFIED | `bd_ai_gateway.pre.20260704-1426.dump` (root:root 600), `bd_ai_gateway_prod.source.20260704-1426.dump` (97MB), migrate.sql, .backup_main_path |
| `vps-ifix-vm:ai-gateway-prod.yml` | service URLs → 10.10.10.50:80 | VERIFIED | `grep -c "10.10.10.50:80"` = 2; `10.10.10.20` only in comments |
| `vps-ifix-vm:ai-gateway-prod.yml.bak-pre19-05` | rollback backup retained | VERIFIED | File exists; dated May 26 (pre-Phase 19); verified identical to pre-flip state |

---

## Key Link Verification

| From | To | Via | Status | Details |
|------|----|-----|--------|---------|
| edge Traefik (vps-ifix-vm) | worker-vm internal Traefik :80 | file-provider server URL `http://10.10.10.50:80` | VERIFIED | 2 service blocks, hot-reload confirmed |
| gateway service | bd_ai_gateway | `AI_GATEWAY_PG_DSN` in swarm env | VERIFIED | DSN confirmed in service spec; `tenants refreshed rows=18` in logs |
| gateway service | infra-redis-1:6379 | swarm overlay DNS alias on `ai-gateway` network | VERIFIED | Redis service alias `[infra-redis-1 redis]`; gateway env `AI_GATEWAY_REDIS_ADDR=infra-redis-1:6379` |
| gateway service | embed:7997 | `UPSTREAM_EMBED_URL=http://embed:7997` on ai-gateway overlay | VERIFIED | Embed on ai-gateway overlay only; billing logs show `upstream=local-embed` for embed route |
| ops-claude timers | worker-vm gateway | scripts SSH `worker-vm 'docker exec $(docker ps -q -f name=ai-gateway-prod_gateway...) /gatewayctl...'` | VERIFIED | Both scripts read from worker-vm; live run of price-sync: `fx_updated=1 models_updated=4`; report: `FSM=asleep chat=200` from worker-vm |

---

## Data-Flow Trace (Level 4)

| Check | Data | Source | Produces Real Data | Status |
|-------|------|--------|-------------------|--------|
| Billing events | Live prod traffic billing rows | tenant requests → billing_events in bd_ai_gateway | 30 events in last 30min from gateway debug logs; tenant_ids match prod slugs | FLOWING |
| Tenant auth | api_key hash lookup | api_keys table in bd_ai_gateway (hash-verbatim migrated) | Gateway refreshes 18 tenants; prod tenant requests authenticated and billed | FLOWING |
| Model alias resolution | alias → upstream routing | model_aliases in bd_ai_gateway (10→19 additive) | Gateway logs `model aliases refreshed count=19` every 60s | FLOWING |

---

## Behavioral Spot-Checks

| Behavior | Command | Result | Status |
|----------|---------|--------|--------|
| Public gateway health | `curl https://ai-gateway.converse-ai.app/health` | 200 `{"status":"ok","uptime_s":68596,"version":"main-5553bd4"}` | PASS |
| Public dashboard reachable | `curl -o /dev/null -w "%{http_code}" https://ai-dashboard.converse-ai.app/` | 307 (redirect to /login) | PASS |
| Prod admin key authenticates | `GET /admin/metrics` with `X-Admin-Key: ifix_admin_****613f` | 200, active tenant metrics (transcricao-voip, analise-transcr-voip, ia-kanban) | PASS |
| Live billing traffic | `docker service logs ai-gateway-prod_gateway --since 30m \| grep "billing event" \| wc -l` | 30 events (chat + stt routes; multiple prod tenant_ids) | PASS |
| Old n8n-ia-vm gateway gone | `docker ps --format "{{.Names}}" \| grep -c -E "ifix-ai-gateway\|ai-gateway-embed..."` | 0 | PASS |
| Dev stack 34 gone | `GET /api/stacks/34?endpointId=3` (Portainer API) | HTTP 404 | PASS |
| Ops timers enabled | `systemctl --user list-unit-files \| grep -E "report\|primary"` | `gateway-price-sync.timer: enabled`, `prod-primary-report.timer: enabled` | PASS |
| Edge config points to worker-vm | `grep -c "10.10.10.50:80" ai-gateway-prod.yml` | 2 (both service blocks) | PASS |

---

## Anti-Patterns Found

| Finding | Severity | Details |
|---------|----------|---------|
| `ai-gateway-dev_gateway` service at 0/0 replicas on worker-vm | Info | Pre-existing orphan from an earlier phase; explicitly documented and deferred in `deferred-items.md`; does not serve traffic |
| `ifix-ai-dashboard-dev` container running on vps-ifix-vm | Info | Separate dev dashboard serving `ai-dashboard-dev.ifixtelecom.com.br` (different hostname from prod `ai-dashboard.converse-ai.app`); not part of gateway consolidation scope; image `ifix-ai-dashboard:dev-p17` with `restart=unless-stopped` |
| Soak window was ~54 min (plan required 24h/business cycle) | Warning | User explicitly pre-approved decommission at 54 min with clean soak metrics (0 edge 5xx, 0 auth errors, 90 billing rows landing from real prod traffic). Not a gap — operator decision. |

No TBD/FIXME/XXX markers (infra-only phase, no source code modified in repo).

---

## Reversibility Posture

Rollback path after decommission is **rebuild**, not one-line revert (n8n-ia-vm containers are down):

1. **Rebuild fastest path:** retained `/opt/ai-gateway-{prod,embed,rerank}` dirs + `.env` + volumes on n8n-ia-vm → `docker compose up -d` for each
2. **Edge revert:** `cp ai-gateway-prod.yml.bak-pre19-05 ai-gateway-prod.yml` on vps-ifix-vm → Traefik hot-reloads → traffic returns to n8n-ia-vm
3. **Archive fallback:** `~/gw-decomm-archive-19/` on ops-claude (compose files, env VAR NAMES, image digests `@sha256:382a0fc8...`)
4. **DB:** bd_ai_gateway_prod retained on DO cluster with 34,130 billing rows

Retention window: 14 days from July 4 (until ~July 18) for dumps + dirs + volumes.

---

## Deferred Items

| Item | Details | Tracked |
|------|---------|---------|
| `ai-gateway-dev_gateway` 0/0 orphan on worker-vm | Cleanup: `ssh worker-vm docker service rm ai-gateway-dev_gateway` | `deferred-items.md` |
| `ifix-ai-dashboard-dev` on vps-ifix-vm | Separate dev dashboard; out of Phase 19 scope | Out-of-scope |
| Primary GPU pod geo filter (Americas) | Relevant if primary pod is reactivated on worker-vm | `19-CONTEXT.md` deferred section |
| `captain-embed` alias routing to local bge-m3 (1024-dim) not OpenAI (1536-dim) | May need revisiting if captain-embed consumers need OpenAI vector dimensions | `19-03-SUMMARY.md` notes |

---

## Human Verification Required

None. This is an infrastructure-only phase verified entirely through observable system state (service replicas, network config, env vars, logs, DB row counts from gateway telemetry). No visual UI behavior or external service integrations require human eyeballing beyond what was captured.

---

## Gaps Summary

No gaps. All 9 requirements are verified against live evidence. The three informational findings (0/0 orphan service, separate dev dashboard, thin soak) are all either explicitly deferred or operator-approved and do not constitute implementation gaps.

---

## Final Verdict

**PASSED — 9/9 requirements verified against live system.**

worker-vm is the single, Portainer-managed, consolidated ai-gateway. All 5 services (gateway + redis + embed + rerank + dashboard) run at 1/1 replicas on endpoint 6. Public traffic (`ai-gateway.converse-ai.app`, `ai-dashboard.converse-ai.app`) is served by worker-vm with live billing landing in `bd_ai_gateway`. The 18 prod tenants + 19 api_keys are present and authenticating. Old gateways are decommissioned. Ops timers are unmasked and repointed. A rebuild archive + retained dirs/volumes provide the rollback path.

---

_Verified: 2026-07-04T20:35:00Z_
_Verifier: Claude (gsd-verifier)_
