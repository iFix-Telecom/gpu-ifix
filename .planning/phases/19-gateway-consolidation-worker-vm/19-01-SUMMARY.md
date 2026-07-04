---
phase: 19-gateway-consolidation-worker-vm
plan: 01
subsystem: infra/swarm-foundation
tags: [portainer, docker-swarm, traefik, redis, overlay-network, recon]
requires: []
provides:
  - "worker-vm external overlay network 'ai-gateway' (attachable)"
  - "confirmed Portainer swarm-string create route + swarmID (endpoint 6)"
  - "confirmed worker-vm internal Traefik host-routing scheme + downstream router labels"
  - "proven infra-redis-1 network-alias resolution mechanism"
affects:
  - "19-02 (gateway/redis/embed stack deploy — consumes create route, labels, overlay)"
  - "19-03 (dashboard + downstream stacks — consumes same)"
tech-stack:
  added: []
  patterns:
    - "Portainer compose-string swarm stack create (avoids agent bind-mount breakage)"
    - "Redis reachable under hardcoded DNS name via swarm network alias (zero env change)"
key-files:
  created:
    - ".planning/phases/19-gateway-consolidation-worker-vm/19-01-SUMMARY.md"
  modified: []
  infra:
    - "worker-vm: docker overlay network 'ai-gateway' (external, attachable) — CREATED"
decisions:
  - "Confirmed create route = POST /api/stacks/create/swarm/string?endpointId=6 (legacy ?type=1&method=string is 405/removed)"
  - "Deferred the optional traefik-internal_traefik --network-add ai-gateway to 19-02 (no backend to route to yet; avoids premature rolling restart of a shared service)"
  - "Documented two routing schemes; recommend SCHEME A (gateway on worker_intra+ai-gateway, zero Traefik mutation)"
metrics:
  duration: "5m 4s"
  completed: "2026-07-04"
  tasks: 3
  files_created: 1
---

# Phase 19 Plan 01: Worker-VM Swarm Foundation + A1/A2 Live-Confirm Summary

Non-disruptive prep: created the shared `ai-gateway` overlay on worker-vm, proved the `infra-redis-1` alias mechanism end-to-end, and closed both research open-items (A1 Portainer swarm-string create route + swarmID; A2 internal Traefik `:80` host-routing scheme) — all while the live PROD gateway on n8n-ia-vm stayed untouched (health 200 before and after).

## What Was Built

### Task 1 — A1: Portainer swarm-string create route + swarmID (RESOLVED)
- **Confirmed create route (this Portainer build, agent 2.39.1):**
  `POST /api/stacks/create/swarm/string?endpointId=6`
- **Legacy route removed:** `POST /api/stacks?type=1&method=string&endpointId=6` → **HTTP 405**.
- **swarmID for endpoint 6 (worker-vm):** `wg4ns7gcgbf0lygbmah5k3vxv` (not a secret).
- **Required JSON body fields** (validation order proven via empty/partial probes — no stack was ever created):
  1. `name` → "Invalid stack name" when empty
  2. `swarmID` → "Invalid Swarm ID" when missing (lowercase field name `swarmID`)
  3. `stackFileContent` → "Invalid stack file content" when empty
  4. `env: [{name, value}]` → environment variables go here, **not** baked into `stackFileContent`
- **Stack type semantics:** Type=1 = swarm, Type=2 = compose. Endpoint 6 already runs Type=1 swarm stacks (ids 24 `integracoes-crms`, 28 `integracoes_crm-v2-dev`) — but those are git stacks (`EntryPoint deploy/*.yml`). 19-02/03 must use the **string** method to avoid the agent bind-mount breakage (memory `gateway-prod-build-deploy`).

Example body shape for 19-02:
```json
{ "name":"ai-gateway-prod",
  "swarmID":"wg4ns7gcgbf0lygbmah5k3vxv",
  "stackFileContent":"<compose yaml as JSON-escaped string>",
  "env":[{"name":"AI_GATEWAY_PG_DSN","value":"..."}] }
```

### Task 2 — A2: worker-vm internal Traefik host-routing at :80 (RESOLVED)
- Service `traefik-internal_traefik` (v2.11); provider = **docker, swarmMode=TRUE** → reads **service-level `deploy.labels`**, not container labels.
- `exposedByDefault=FALSE` → every backend needs `traefik.enable=true`.
- Watched provider network: **`worker_intra`** (`--providers.docker.network=worker_intra`).
- Entrypoint bound to `:80` = **`web`** (PublishMode=host → host `:80` maps straight to Traefik).
- Traefik currently attached only to `worker_intra` (attachable=true), alias `traefik`.

**Exact router labels the future gateway swarm service must carry (`deploy.labels`):**
```
traefik.enable=true
traefik.http.routers.ai-gateway.rule=Host(`ai-gateway.converse-ai.app`)
traefik.http.routers.ai-gateway.entrypoints=web
traefik.http.services.ai-gateway.loadbalancer.server.port=8080
traefik.docker.network=<net Traefik uses to reach the service VIP — see schemes>
```

**Two viable routing schemes (19-02 picks one):**
- **SCHEME A (RECOMMENDED — zero Traefik mutation):** gateway attaches to **both** `worker_intra` (routing) and `ai-gateway` (redis/embed). Label `traefik.docker.network=worker_intra`. Traefik already watches `worker_intra` → routes with no change to the shared service.
- **SCHEME B (plan's stated key_link — Traefik on ai-gateway):** run `docker service update --network-add ai-gateway traefik-internal_traefik` (causes a rolling restart of the shared Traefik), then gateway label `traefik.docker.network=ai-gateway`.

A2 is fully resolved with docker/swarm host routing confirmed — **no fallback** to an edge→published-host-port scheme is needed.

### Task 3 — Shared `ai-gateway` overlay + infra-redis-1 alias proof (DONE)
- Created external, attachable overlay on worker-vm: `docker network create -d overlay --attachable ai-gateway` → id `sv5po43sbxeq3u4szzqkmu98j`.
- Proved the hardcoded-DNS mechanism: deployed a throwaway `redis:7.4.2` swarm service with network alias `infra-redis-1` on `ai-gateway`; a one-off container on the overlay resolved `infra-redis-1` → `10.0.4.2` and `redis-cli -h infra-redis-1 ping` → **PONG**.
- Tore down the throwaway service; overlay persists empty.
- This confirms NET-01: the gateway env `AI_GATEWAY_REDIS_ADDR=infra-redis-1:6379` will resolve with **zero env change** once 19-02 deploys redis with the `infra-redis-1` alias on this overlay.

## Deviations from Plan

**1. [Rule 3 — routing/verification correctness] PROD baseline health path corrected**
- **Found during:** pre-execution baseline.
- **Issue:** Plan/success-criteria assert `curl http://10.10.10.20:80/health` → 200. That path returns **404** even at baseline: on n8n-ia-vm the `:80` internal Traefik (swarmMode, watches network `intra`) does not front the standalone compose gateway container.
- **Resolution:** established the true PROD baseline via the gateway container directly (`http://10.10.10.20:8080/health` → 200, `{"status":"ok","version":"main-5553bd4"}`) and the public ingress (`https://ai-gateway.converse-ai.app/health` → 200). Both were 200 before and after this plan; container `ifix-ai-gateway` stayed "Up 27 hours (healthy)" and uptime only increased — PROD untouched.
- **Files modified:** none (recon/verification only).

**2. [Scope — deferred optional step] Traefik --network-add ai-gateway deferred to 19-02**
- Task 3 offers an optional `docker service update --network-add ai-gateway traefik-internal_traefik` "if needed to reach backends." No gateway service exists yet, and SCHEME A needs no Traefik mutation. Running it now would rolling-restart a shared service (n8n/kestra/rabbitmq/traefik on worker-vm) for no benefit. The exact command + condition are documented above for 19-02 to execute if it picks SCHEME B.

## Verification Results

- `ai-gateway` overlay present on worker-vm: `overlay`, `attachable=true`. ✅
- A1 recorded: confirmed swarm-string create route + swarmID + body fields. ✅
- A2 recorded: provider (docker/swarm), `:80` entrypoint (`web`), router labels, watched network (`worker_intra`), + two schemes. ✅
- infra-redis-1 alias resolution proven end-to-end (PONG) and throwaway torn down. ✅
- **Zero impact on live PROD gateway** (n8n-ia-vm): `:8080/health` 200 + public 200 after the plan; all 4 prod containers (`ifix-ai-gateway`, `ai-gateway-embed`, `ai-gateway-rerank`, `redis-gateway-prod`) healthy/untouched. ✅

## Notes for 19-02 / 19-03

- Use `POST /api/stacks/create/swarm/string?endpointId=6` with body `{name, swarmID:"wg4ns7gcgbf0lygbmah5k3vxv", stackFileContent, env:[]}`. Secrets stay in `env[]` / worker-vm `.env` root-600 — never in `stackFileContent`.
- In swarm mode Traefik reads `deploy.labels` (service-level), not container labels.
- Pitfall 3 (research): `172.18.0.1:18000` (host bridge) is NOT reachable from an overlay-only service. Non-blocking now (no pod), but bake host-bridge reachability or use `10.10.10.50:18000` when a pod is added.
- Pitfall 4 (research): Portainer webhook won't pull new images — use `docker service update --image <tag> --force <svc>` after any image push.

## Known Stubs

None — this plan created infra (overlay network) and recon artifacts only; no application code or placeholder data introduced.

## Self-Check: PASSED

- `.planning/phases/19-gateway-consolidation-worker-vm/19-01-SUMMARY.md` — FOUND.
- No per-task code commits (recon/infra plan — artifacts are the overlay network + recon findings in this SUMMARY). Infra artifact `ai-gateway` overlay verified live on worker-vm (`attachable=true`).
