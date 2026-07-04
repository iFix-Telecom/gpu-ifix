---
phase: 19-gateway-consolidation-worker-vm
plan: 02
subsystem: infra/consolidated-gateway
tags: [portainer, docker-swarm, ai-gateway, embed, bge-m3, redis, overlay, digest-pin, bd_ai_gateway]
requires:
  - "19-01: ai-gateway overlay + swarm-string create route + swarmID + internal-Traefik Host routing"
provides:
  - "worker-vm consolidated ai-gateway swarm stack (Portainer stack 38) reading bd_ai_gateway + infra-redis-1 + R2 — UI-editable, digest-pinned"
  - "worker-vm bge-m3 embed swarm stack (Portainer stack 39) reachable at overlay DNS embed:7997 — off n8n-ia-vm"
  - "pre-cutover green baseline on bd_ai_gateway (chat/embed/admin 200 + billing writes) with a DEV tenant"
affects:
  - "19-03 (dashboard + rerank + model_alias reconcile — same overlay/route/stacks pattern)"
  - "19-04 (DB migration into bd_ai_gateway — the stack this plan stood up receives the migrated prod tenants)"
  - "19-05 (edge-Traefik cutover flips public traffic onto this stack)"
tech-stack:
  added: []
  patterns:
    - "Portainer swarm compose-string stack with env supplied via env array (${VAR} interpolation) — UI-editable, secrets never baked into stackFileContent"
    - "Fresh Redis under hardcoded DNS via swarm network alias infra-redis-1 (zero env change)"
    - "Distroless-container connectivity proof via --network container:<gw> shared-netns curl (exec has no shell)"
    - "Image digest-pin (@sha256) for migration stability instead of :main/:latest-dev drift"
key-files:
  created:
    - ".planning/phases/19-gateway-consolidation-worker-vm/19-02-SUMMARY.md"
  modified: []
  infra:
    - "portainer: stack 38 ai-gateway-prod (endpoint 6, swarm) — gateway + fresh redis(alias infra-redis-1) — CREATED"
    - "portainer: stack 39 ai-gateway-embed (endpoint 6, swarm) — Infinity bge-m3 CPU — CREATED"
    - "worker-vm: /opt/ai-gateway-prod/.env root-600 (dev-style secrets, NOT committed) — CREATED"
decisions:
  - "172.18.0.1:18000 (LLM/STT/HEALTH) = intentionally tier-1-only/dormant (GPU-less VM, PRIMARY_POD_SCHEDULE_DISABLED=true); probe-unhealthy is EXPECTED, gateway serves on tier-1 (OpenRouter/Gemini) — proven by chat 200 via openrouter-chat"
  - "Fresh Redis (redis:7.4.2, no auth, alias infra-redis-1) dedicated to the consolidated gateway — treated as ephemeral cache; durable state rebuilt from bd_ai_gateway. Cold Redis at cutover is safe (AI_GATEWAY_REDIS_PASSWORD emptied for dev-style no-auth)"
  - "Routing SCHEME A adopted (gateway on worker_intra + ai-gateway, label traefik.docker.network=worker_intra) — zero mutation of the shared traefik-internal service; SCHEME B (--network-add ai-gateway) NOT executed (unnecessary)"
  - "Gateway image pinned to the CURRENTLY-RUNNING prod digest ghcr.io/ifixtelecom/ifix-ai-gateway@sha256:382a0fc… (plan text's 'converseai-gateway:main' was a naming error — real prod image is ifix-ai-gateway)"
  - "MINIO_* pointed at Cloudflare R2 (bucket ai-gateway-weights) per env-alvo; not load-exercised at boot (weights dormant — no pod)"
metrics:
  duration: "~24m"
  completed: "2026-07-04"
  tasks: 3
  files_created: 1
---

# Phase 19 Plan 02: Consolidated Gateway + Embed on worker-vm Summary

Stood up the consolidated **ai-gateway** (gateway + fresh Redis) and **bge-m3 embed** as two UI-editable, digest-pinned Portainer swarm stacks on worker-vm (endpoint 6), wired to the dev-style target env (`bd_ai_gateway` + R2 + `infra-redis-1` + local embed + tier-1 LLM/STT fallback). Proven green against `bd_ai_gateway` with a DEV tenant (chat 200 tier-1 fallback + billing write, embed 200 from the gateway's own netns + billing write, admin 200) — running in PARALLEL with the untouched live PROD gateway on n8n-ia-vm (still `Up (healthy)`, public `/health` 200 before and after).

## What Was Built

### Task 1 — ai-gateway-prod swarm stack (Portainer stack 38, endpoint 6)
- **Services:** `gateway` (1/1) + `redis` (1/1). UI-editable in Portainer (swarm compose-string, env in env array).
- **Gateway image (DIGEST-PINNED):** `ghcr.io/ifixtelecom/ifix-ai-gateway@sha256:382a0fc80e03603d80681c59ab96e10bf88cd9d53db3d6eca190387223f31ea9` (= currently-running prod `:main`, version `main-5553bd4`, goose-v31-matched).
- **Networks:** `worker_intra` (internal-Traefik routing, SCHEME A) + `ai-gateway` (redis/embed overlay DNS).
- **Router labels (deploy.labels):** `Host(ai-gateway.converse-ai.app)` → `entrypoints=web` → service port 8080 → `traefik.docker.network=worker_intra`.
- **Fresh Redis:** `redis:7.4.2`, no persistence, no auth, network alias **`infra-redis-1`** on the `ai-gateway` overlay → the hardcoded `AI_GATEWAY_REDIS_ADDR=infra-redis-1:6379` resolves with zero env change. This is the LIVE gateway Redis (ephemeral cache; leadership locks + FSM mirror rebuilt from Postgres). Cold-Redis-at-cutover is safe.
- **Boot proof (logs):** `tenants refreshed rows=4`, `upstreams refreshed rows=9`, `model aliases count=10`, `prices rows=5` (all from `bd_ai_gateway`); `acquired leadership` + `acquired primary leadership` (Redis locks via `infra-redis-1`); `upstream_embed=http://embed:7997`; `schedule_disabled=true`; `vast.Ping ok`.

### Task 2 — ai-gateway-embed swarm stack (Portainer stack 39, endpoint 6)
- **Service:** `embed` (1/1). **Image DIGEST-PINNED:** `michaelf34/infinity:0.0.77@sha256:11e8b3921b9f1a58965afaad4a844c435c9807cbc82c51e47cb147b7d977fc88`.
- **Model:** `BAAI/bge-m3` served as `bge-m3`, `--engine torch --device cpu --dtype float32 --url-prefix /v1 --port 7997` (matches prod embed shape exactly). Resource caps: 6 GB / 3.0 CPUs. Named volume `ai-gateway-embed-model-cache` for HF cache.
- **On `ai-gateway` overlay only**, no host port — reachable by overlay DNS `embed:7997`.
- **Gateway UPSTREAM_EMBED_URL** set to `http://embed:7997` (worker-vm-local) — NOT `http://10.10.10.20:7997` (n8n-ia-vm). Embed no longer dies at n8n-ia-vm decommission (research Pitfall 1 closed).
- **Netns verification (Codex HIGH):** the gateway image is distroless (no `sh`), so verified from the gateway's EXACT network namespace via `docker run --network container:<gateway> curlimages/curl http://embed:7997/health` → **HTTP 200**, DNS `embed` resolves through the gateway's own overlay attachment. Round-trip `/health` ~4 ms.

### Task 3 — Pre-cutover green baseline (DEV tenant, bd_ai_gateway)
Validated via internal Traefik (`http://10.10.10.50:80` + `Host: ai-gateway.converse-ai.app`) using freshly-minted DEV creds (`gatewayctl` inside the running gateway):
- Dev tenant key: `converseai` (normal) `ifix_sk_****fyei` (id 443dde0e…).
- Dev admin key: `19-02-validation` `ifix_admin_****eb79` (id a1aa115b…).

| Check | Result |
|-------|--------|
| `GET /health` | **200** `{"status":"ok","version":"main-5553bd4"}` |
| `POST /v1/chat/completions` (model `qwen`) | **200** — routed tier-1 `openrouter-chat` → `deepseek/deepseek-v4-flash` (Novita), content `ping`, ttfb ~2.4 s (local pod dead → tier-1 as designed) |
| `POST /v1/embeddings` (model `bge-m3`) | **200** — 1024-dim vectors from worker-vm embed; latency ~0.46–0.78 s (CPU bge-m3, 3 samples) |
| `GET /admin/metrics` (X-Admin-Key) | **200** |
| `billing_events` in `bd_ai_gateway` | **918 → 923**: 1 row `route=chat upstream=openrouter-chat`, 4 rows `route=embed upstream=local-embed` — end-to-end proof the gateway reached `embed:7997` through its own netns |

## 172.18.0.1:18000 — tier-1-only decision (recorded)
No pod runs on worker-vm (GPU-less) and `PRIMARY_POD_SCHEDULE_DISABLED=true`, so `http://172.18.0.1:18000` (LLM/STT/HEALTH) is **intentionally dormant/unreachable**. The gateway operates on tier-1 fallback exactly as dev does today. Its upstream probe for `:18000` WILL show unhealthy — EXPECTED, not a defect; breaker-open on the tier-0 local upstream falls through to tier-1 (proven: chat 200 via `openrouter-chat`). Future path when a pod is added: switch to host LAN IP `10.10.10.50:18000` or attach the gateway to the host bridge (research Pitfall 3).

## Deviations from Plan

### Auto-fixed / blocking-resolved

**1. [Rule 3 — blocking] worker-vm `.env` was absent; derived from prod secrets with dev-style deltas**
- **Found during:** Task 1 setup. The plan `user_setup` expected an operator-provided `/opt/ai-gateway-prod/.env` root-600 on worker-vm — it did not exist.
- **Fix:** built it by piping the known-good prod `.env` (n8n-ia-vm `/opt/ai-gateway-prod/.env`) VM→VM (secrets never touched disk/.planning) and applying dev-style deltas: DSN dbname `bd_ai_gateway_prod` → `bd_ai_gateway`; `AI_GATEWAY_REDIS_ADDR` → `infra-redis-1:6379`; `AI_GATEWAY_REDIS_PASSWORD` → empty; `UPSTREAM_LLM/STT/HEALTH` → `http://172.18.0.1:18000`; `UPSTREAM_EMBED_URL` → `http://embed:7997`; `PRIMARY_POD_SCHEDULE_DISABLED` → true; `LOG_LEVEL` → debug; `MINIO_*` → R2 (endpoint `71a124f3…r2.cloudflarestorage.com`, bucket `ai-gateway-weights`, R2 S3 creds). `AI_GATEWAY_MIGRATE_ON_BOOT=false` preserved (T-19-02-02).
- **Files:** worker-vm `/opt/ai-gateway-prod/.env` (root-600, NOT committed).

**2. [Rule 1 — correctness] Gateway image name corrected**
- Plan text said `ghcr.io/ifixtelecom/converseai-gateway:main`; the real running prod image is `ghcr.io/ifixtelecom/ifix-ai-gateway`. Pinned its live digest `@sha256:382a0fc…`.

**3. [Rule 3 — blocking] Portainer rejected duplicate env keys**
- The prod `.env` carried 4 duplicate keys (weights SHA/KEY vars). Portainer's swarm `environment` array requires uniqueness (HTTP 500). Deduped last-wins in the generator (79 → 75 unique vars).

**4. [Rule 1 — verification method] Netns proof adapted to distroless gateway**
- Gateway image has no shell (`docker exec sh` = exec 127). Used `--network container:<gateway>` shared-netns curl (identical DNS/attachments to the gateway) for the embed reachability proof, plus the `route=embed upstream=local-embed` billing row as end-to-end confirmation.

**5. [Scope — routing scheme] SCHEME A (zero Traefik mutation)**
- Adopted 19-01's recommended SCHEME A (gateway on `worker_intra` + `ai-gateway`, `traefik.docker.network=worker_intra`). The deferred optional `docker service update --network-add ai-gateway traefik-internal_traefik` (SCHEME B) was NOT run — unnecessary and would rolling-restart a shared service.

## Verification Results
- `docker service ls`: `ai-gateway-prod_gateway` 1/1 (digest-pinned `@sha256:382a0fc…`), `ai-gateway-prod_redis` 1/1, `ai-gateway-embed_embed` 1/1 (digest-pinned `@sha256:11e8b3…`). ✅
- Portainer stacks 38 (`ai-gateway-prod`) + 39 (`ai-gateway-embed`) on endpoint 6, type=1 (swarm), status=1 (active), UI-editable. ✅
- Internal Traefik `http://10.10.10.50:80/health` (Host route) → 200. ✅
- embed `:7997` resolves + healthy FROM the gateway netns (shared-netns curl 200); latency sampled. ✅
- DEV tenant chat 200 + `billing_events` row (route=chat) in `bd_ai_gateway`; embed 200 + row (route=embed, upstream=local-embed); admin 200. ✅
- **Live PROD gateway (n8n-ia-vm) unaffected:** `ifix-ai-gateway Up 27 hours (healthy)` + public `/health` 200 before AND after. Same DO cluster, DIFFERENT db (`_prod` vs `bd_ai_gateway`) — no write contention. ✅
- worker-vm RAM after (embed model loaded): 7.3 GiB available. ✅

## Notes for 19-03 / 19-04 / 19-05
- Reuse the same create route `POST /api/stacks/create/swarm/string?endpointId=6`, swarmID `wg4ns7gcgbf0lygbmah5k3vxv`, and the on-VM generator pattern (`/root/gw-deploy/`). Compose reference kept at worker-vm `/root/gw-deploy/{compose.yml,embed_compose.yml}` (no secrets).
- The consolidated stack currently reads `bd_ai_gateway` (4 dev tenants). 19-04 migrates the 18 prod tenants + api_keys into this DB — the running gateway picks them up via `LISTEN tenants_changed` (no redeploy).
- The two minted validation creds (dev tenant `ifix_sk_****fyei`, admin `ifix_admin_****eb79`) live in `bd_ai_gateway`; they are pre-cutover DEV creds and will be superseded by the 19-04 migration.
- R2 (`MINIO_*`) is configured but not load-exercised (no pod) — validate only if primary/emergency pods are reactivated.

## Known Stubs
None — this plan deployed existing images (digest-pinned) as swarm stacks; no application code or placeholder data introduced.

## Self-Check: PASSED
- `.planning/phases/19-gateway-consolidation-worker-vm/19-02-SUMMARY.md` — FOUND.
- Infra artifacts verified live: Portainer stacks 38 + 39 (endpoint 6, status active), gateway service image digest-pinned `@sha256:382a0fc…`, embed digest-pinned `@sha256:11e8b3…`, all services 1/1, `/health` 200 via internal Traefik, billing_events 918→923 in `bd_ai_gateway`.
- No per-task code commits (infra plan — deliverables are live swarm stacks + on-VM root-600 `.env`, documented above).
