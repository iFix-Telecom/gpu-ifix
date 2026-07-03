# Phase 19: Gateway consolidation → worker-vm — Research

**Researched:** 2026-07-03
**Domain:** Infra migration — Docker Swarm / Portainer stack deploy + cross-DB Postgres tenant/key migration + zero-break DNS/Traefik cutover
**Confidence:** HIGH (schema, Traefik router, config env verified in-repo; Swarm/Portainer API behavior CITED from prior memories + Docker docs)

## Summary

This is a lift-and-consolidate of five ai-gateway containers (prod gateway + dashboard + embed + rerank + redis on n8n-ia-vm, plus the dev gateway stack 34 on vps-ifix-vm) onto **worker-vm (10.10.10.50)**, a GPU-less **Docker Swarm** node. The consolidated gateway runs the DEV-style config (points at `bd_ai_gateway`, R2 weights, local `:18000` pod-when-present + tier-1 fallback) but must **inherit the production tenant/key set** from `bd_ai_gateway_prod` so the ~18 real client apps keep authenticating without key regeneration.

Two risk centers dominate. (1) **The DB migration** — 18 tenants / 19 api_keys must land in `bd_ai_gateway` preserving `key_lookup_hash` and `key_hash` byte-for-byte, resolving slug collisions against the 4 throwaway dev tenants, and keeping the `tenants → api_keys → usage_counters → billing_events` FK order. (2) **The cutover** — the public hostname `ai-gateway.converse-ai.app` is served by the **edge Traefik file-provider on vps-ifix-vm**, whose service `n8n-ia-prod-internal` points at `http://10.10.10.20:80`. Cutover is a **one-line edit** of that server URL to `http://10.10.10.50:80` (hot-reload, no DNS/Cloudflare change); rollback is reverting that line. DNS stays `162.55.92.154` throughout.

**Primary recommendation:** Stand the consolidated gateway up on worker-vm in parallel against `bd_ai_gateway` (migration idle/no-write to prod), migrate tenants+keys with a DELETE-dev-first + COPY strategy inside one transaction, validate a real tenant key returns 200 + writes a billing row, then flip the single edge-Traefik server line. Decommission n8n-ia-vm **only after** the embed (`bge-m3` Infinity) service is also running on worker-vm — the gateway's `UPSTREAM_EMBED_URL=http://10.10.10.20:7997` dies with n8n-ia-vm otherwise.

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Public TLS ingress `ai-gateway.converse-ai.app` | Edge Traefik (vps-ifix-vm, file-provider) | — | Owns cert (TLS-ALPN-01) + Host routing; cutover happens HERE, not DNS |
| Internal routing to gateway container | worker-vm swarm Traefik (`traefik-internal_traefik` v2.11) | — | Terminates edge forward at `:80`, routes to `gateway:8080` overlay |
| Gateway app (auth/proxy/billing) | Swarm service on worker-vm | — | Stateless; reads DB+Redis, proxies to upstreams |
| Tenant/key/quota state | DO managed PG `bd_ai_gateway` | — | Source of truth post-migration |
| Redis (rate-limit, quota counters, primary FSM mirror) | worker-vm swarm service aliased `infra-redis-1` | — | Env hardcodes `infra-redis-1:6379` |
| Embed (bge-m3 Infinity) | Swarm service on worker-vm (CPU) | tier-1 OpenAI embed fallback | Must move off n8n-ia-vm before decommission |
| LLM/STT local pod `:18000` | worker-vm docker bridge `172.18.0.1:18000` | tier-1 OpenRouter/Gemini | No pod runs now; gateway operates tier-1 (as dev does today) |

## Standard Stack

This phase installs **no new packages** — it redeploys existing images. Versions in play (verified in recon):

| Component | Version | Source | Notes |
|-----------|---------|--------|-------|
| Docker Swarm (worker-vm) | engine per VM | recon | services n8n/kestra/rabbitmq/postgres/traefik-internal, overlay `worker_intra` |
| Portainer agent (endpoint 6) | 2.39.1 | recon | swarm-aware; deploy via `type=1` (swarm) |
| Traefik internal (worker-vm) | v2.11 | recon | `traefik-internal_traefik` |
| Traefik edge (vps-ifix-vm) | v3.6 | 10-PATTERNS.md | file-provider `/etc/traefik/dynamic/` |
| Redis | 7.4.2 | recon | `redis_redis` exists; need alias `infra-redis-1` |
| Gateway image | `ghcr.io/ifixtelecom/converseai-gateway` (`:main` prod / `:develop` dev) | STATE | `AI_GATEWAY_MIGRATE_ON_BOOT` gated |
| DB target | Postgres (DO managed, schema `ai_gateway`, goose **v31**) | recon | 33 tables |

**No `npm/pip/cargo` install → Package Legitimacy Audit N/A** (no external packages introduced).

## Architecture Patterns

### System data flow (post-cutover)

```
client app (Bearer ifix_sk_…)
   │  https://ai-gateway.converse-ai.app        DNS → 162.55.92.154 (UNCHANGED)
   ▼
[Hetzner host DNAT :443] ──▶ [Edge Traefik v3.6 @ vps-ifix-vm]   ← CUTOVER POINT
   │  file-provider service n8n-ia-prod-internal
   │  server url: http://10.10.10.20:80   ⟶  CHANGE TO ⟶  http://10.10.10.50:80
   ▼
[Traefik-internal v2.11 @ worker-vm :80]  Host(`ai-gateway.converse-ai.app`) → gateway:8080
   ▼
[gateway swarm service @ worker-vm]
   ├─▶ bd_ai_gateway (DO PG :25060)      auth (key_lookup_hash), quota, billing write
   ├─▶ infra-redis-1:6379 (worker-vm)     rate-limit / counters / FSM mirror
   ├─▶ 172.18.0.1:18000  LLM/STT          (pod-when-present; else ↓)
   ├─▶ tier-1 OpenRouter / Gemini         fallback (active now — no local pod)
   └─▶ UPSTREAM_EMBED_URL  bge-m3 Infinity (MUST be on worker-vm post-decommission)
```

### Pattern 1: Portainer swarm stack via API string method (endpoint 6)

**What:** Deploy each stack as a **compose-string** stack (not git) so it stays UI-editable and avoids the agent-endpoint relative-bind-mount breakage.
**Why (CITED memory `gateway-prod-build-deploy`):** git-stacks with relative `./docker/...` bind-mounts break on agent endpoints (4/6/7) — the repo files are not co-located on the agent host. Use compose-string with **absolute paths** or baked images.

```bash
# Swarm stack create via string (type=1 swarm, method=string), endpoint 6 = worker-vm
PTR="ptr_jBtR69HsSXBT4hL4RPdMKe6hQ17q7jQOyuF3dGZhKHQ="
BASE="https://portainer3.ifixtelecom.com.br/api"
SWARM_ID=$(curl -s -H "X-API-Key: $PTR" "$BASE/endpoints/6/docker/swarm" | jq -r .ID)

curl -s -X POST "$BASE/stacks/create/swarm/string?endpointId=6" \
  -H "X-API-Key: $PTR" -H "Content-Type: application/json" -d @- <<JSON
{ "name":"ai-gateway-prod",
  "swarmID":"$SWARM_ID",
  "stackFileContent":"<docker-compose.yml as a JSON-escaped string>",
  "env":[{"name":"AI_GATEWAY_PG_DSN","value":"..."}, ...] }
JSON
```
- Note: newer Portainer also accepts the legacy form `POST /api/stacks?type=1&method=string&endpointId=6`. Confirm the running Portainer build's route with `GET /api/stacks` shape first; both create an **editable** stack.
- **Env vars** go in the `env` array (UI "Environment variables" panel) — do NOT bake secrets into `stackFileContent`. Real secret values live in the worker-vm `.env` root-600 (per CONTEXT).

### Pattern 2: Standalone-compose → Swarm compose conversion

The 5 non-dev stacks are standalone (`container_name`, bridge). For swarm each service needs:

| Standalone key | Swarm behavior | Action |
|----------------|----------------|--------|
| `container_name: ifix-ai-gateway` | **Ignored** by swarm | Remove; rely on service DNS name |
| `restart: unless-stopped` | Ignored | Replace with `deploy.restart_policy.condition: any` |
| bridge network | Not overlay | Attach to an overlay (`worker_intra` or a new `ai-gateway` overlay) |
| depends-on ordering | Not honored on swarm | Gateway must retry DB/Redis on boot (it does) |
| `172.18.0.1:18000` (bridge gw) | **Reachable only from a container on the default bridge, not an overlay-only service** | See Pitfall 3 |
| published port | `deploy` + `ports` (ingress mesh) | Publish `:80`/`:8080` via internal Traefik labels, not host ports |

### Anti-Patterns to Avoid
- **Git-stack with relative bind-mounts on endpoint 6** — breaks (memory). Compose-string + absolute paths or baked config.
- **Changing DNS/Cloudflare for cutover** — unnecessary and slow to roll back. The edge Traefik server-URL line is the correct, hot-reloadable seam.
- **Regenerating client keys** — apps break. `key_hash`/`key_lookup_hash` must migrate verbatim.
- **Relying on Portainer webhook to pull new images** — it does not (memory + STATE). Pull+recreate manually.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Cross-DB row copy | Custom SELECT→INSERT script | `pg_dump --data-only` piped to `psql`, both on same DO host:port different dbname | No dblink/fdw guaranteed on DO managed PG; pg_dump preserves BYTEA/UUID exactly |
| Key hash re-derivation | Recompute argon2id/SHA-256 | Copy `key_hash`+`key_lookup_hash` columns as-is | Hashes are opaque; recompute impossible (raw keys not stored) |
| Redis rename | Rewrite gateway env | Swarm service **network alias** `infra-redis-1` | Zero code/env change; DNS resolves the hardcoded name |
| Ingress cutover | New DNS record + TTL wait | Edit edge file-provider server URL | Sub-second hot-reload + instant rollback |

**Key insight:** every risky step in this phase has a native, reversible mechanism — swarm aliases, pg_dump COPY, and Traefik file hot-reload. Hand-rolling any of them adds an irreversible failure mode.

## Runtime State Inventory (migration phase)

| Category | Items Found | Action Required |
|----------|-------------|------------------|
| Stored data | `bd_ai_gateway_prod`: 18 tenants, 19 api_keys, 33 082 billing_events, admin_keys (prod admin `ifix_admin_49e4…`), usage_counters (current-period quota state), **model_aliases** (prod-only aliases e.g. `gpt-5.4-mini-2026-03-17`, `whisper` mirror — memory `stt-model-alias-whisper-large-v3`), upstreams | **Data migration** of tenants+api_keys+admin_keys+usage_counters; **reconcile** model_aliases/upstreams; **decide** billing (archive vs copy) |
| Live service config | Edge Traefik file `ai-gateway-prod.yml` on vps-ifix-vm (server → 10.10.10.20:80); Portainer stack 34 (dev, endpoint 3) | Edit edge server URL at cutover; delete/stop stack 34 + n8n-ia-vm compose stacks at decommission |
| OS-registered state | prod gateway = **manual `docker compose`** at `/opt/ai-gateway-prod` on n8n-ia-vm (NOT Portainer); systemd user timers on **ops-claude** (primary schedule, daily report — memory `prod-primary-schedule-and-email`) | Stop compose on n8n-ia-vm at decommission; **re-point** ops-claude timers/report at the new gateway if they call the prod host |
| Secrets/env vars | Real DSN/bearers/R2/SSH in worker-vm `.env` root-600 (user-provided); `WEIGHTS_QWEN_KEY=qwen3.5-27b…` references a key **absent from R2** (R2 has qwen3.6) | Env is code-rename only; the qwen3.5 gap is a **known non-blocker** (no pod running → gateway tier-1) |
| Build artifacts | Gateway image tags `:main` (prod) vs `:develop` (dev) diverge; prod VM build bakes no git-rev (version="dev", STATE) | Choose ONE image tag for the consolidated stack; confirm deploy by billing/behavior, not rev label |

**Nothing found for:** cron in system crontab (host uses systemd **user** timers — memory), and no local `:18000`/`:7997` listeners on worker-vm today (recon).

## Critical Path — Tenant/Key DB Migration

**Tables + FK order (verified from `gateway/db/migrations/`):**

```
tenants (PK id UUID, UNIQUE slug)                     ← 0001, +cols 0013/0016
   └─ api_keys (FK tenant_id ON DELETE CASCADE,        ← 0002
                UNIQUE key_lookup_hash BYTEA)
   └─ usage_counters (PK (tenant_id,date),             ← 0006/0011  [quota source]
                      FK tenant_id ON DELETE CASCADE)
   └─ billing_events (FK tenant_id, PK (request_id,ts),← 0010  [PARTITIONED by ts]
                      PARTITION BY RANGE (ts))
admin_keys (no tenant FK — independent)                ← 0014
```

**Operationally required vs historical:**

| Table | Class | Migrate? |
|-------|-------|----------|
| `tenants` | required | YES — preserve `id` (UUID) so api_keys FK resolves |
| `api_keys` | **required, client-breaking** | YES — `key_lookup_hash`+`key_hash`+`key_prefix`+`data_class`+`status` verbatim |
| `admin_keys` | required | YES (or mint a fresh admin key on target and revoke old) — else `X-Admin-Key` admin ops break |
| `usage_counters` | required if quotas enforced | YES for **current period** rows — else today's consumed quota resets to 0 (grace, not break; `QuotaFailOpen=false` means a mid-cycle heavy tenant could briefly under-count) |
| `model_aliases` | required | **RECONCILE** — port any prod-only alias a client model string depends on, else that call 404s |
| `upstreams` | required | Keep TARGET (dev-style, per locked env) but diff against prod for any prod-only healthy upstream |
| `billing_events` (33k) | historical | **RECOMMEND: leave in `_prod` as archive.** `/economia` history discontinuity is cosmetic. Copy only if history continuity is required (needs partition pre-creation — see below) |

**Collision strategy (target already has 4 dev tenants incl. seeded `converseai`):**
Dev tenants are throwaway; production UUIDs must win so prod api_keys FK resolve. Use **DELETE-dev-colliding-first + COPY**, one transaction:

```bash
# 1. Dump prod core tables data-only, preserving UUID/BYTEA (run from ops-claude — DO whitelists 162.55.92.154)
SRC="postgresql://<user>:<pw>@db-grupoifix-...:25060/bd_ai_gateway_prod?sslmode=require"
DST="postgresql://<user>:<pw>@db-grupoifix-...:25060/bd_ai_gateway?sslmode=require"

pg_dump "$SRC" --data-only --no-owner --schema=ai_gateway \
  --table=ai_gateway.tenants \
  --table=ai_gateway.api_keys \
  --table=ai_gateway.admin_keys \
  --table=ai_gateway.usage_counters \
  -f /tmp/gw-core.sql          # COPY-format dump (fast, exact)

# 2. On TARGET, inside ONE transaction: delete colliding dev tenants (CASCADE removes dev keys/usage),
#    then restore. Slugs to check: converseai, chat-ifix (seed + dev overlap).
psql "$DST" <<'SQL'
BEGIN;
SET search_path = ai_gateway, public;
-- remove EVERY dev tenant whose slug also exists in the prod dump set
-- (safest: clear all non-prod dev rows; dev data is throwaway)
DELETE FROM ai_gateway.tenants
 WHERE slug IN ('converseai','chat-ifix','uat10-test', /* …all dev slugs… */);
-- (CASCADE drops their api_keys + usage_counters automatically)
SQL
# 3. load the prod rows (COPY has no ON CONFLICT — table is now clear of collisions)
psql "$DST" -1 -f /tmp/gw-core.sql     # -1 = single transaction; aborts whole load on any error
```

- **Verify counts before COMMIT-equivalent:** `SELECT count(*) FROM tenants` = 18 (+ surviving non-colliding dev), `api_keys` = 19, `admin_keys` matches source. Spot-check a known key: `SELECT tenant_id,key_prefix,status,data_class FROM api_keys WHERE key_prefix='ifix_sk_****…'`.
- **UUID cross-collision (prod id == dev id, different slug):** astronomically unlikely with `gen_random_uuid()`; the `-1` transactional load will hard-error on any PK dup and roll back — safe.
- **admin_keys** has no tenant FK → loads independently; if the prod admin key collides with a target dev admin key on `key_lookup_hash` UNIQUE, delete the dev admin row first.

**If billing history IS required** (not recommended): pre-create partitions covering the prod `ts` span before COPY, else rows fail the partition router —
```sql
-- inspect span then create monthly partitions billing_events_YYYYMM for each month present
SELECT date_trunc('month',min(ts)), date_trunc('month',max(ts)) FROM bd_ai_gateway_prod…billing_events;
-- OR add a DEFAULT partition to absorb them:
CREATE TABLE ai_gateway.billing_events_default PARTITION OF ai_gateway.billing_events DEFAULT;
```

**Migrations already applied:** `bd_ai_gateway` is at goose **v31** (0031_create_pod_config) — the migration set matches the code. `MIGRATE_ON_BOOT=false` (prod pattern) → run `gatewayctl migrate up` manually only if a pending migration exists; **none pending** for v31. Do NOT flip MIGRATE_ON_BOOT=true on the consolidated stack.

## Redis `infra-redis-1` on worker-vm

Env hardcodes `AI_GATEWAY_REDIS_ADDR=infra-redis-1:6379`. worker-vm has `redis_redis` but not that name. Cleanest swarm approach — **network alias**, no new Redis instance needed:

```yaml
services:
  gateway:
    networks:
      ai-gateway: {}
  # reuse existing redis under the required DNS name via alias:
  redis:
    image: redis:7.4.2
    deploy: { replicas: 1 }
    networks:
      ai-gateway:
        aliases: [infra-redis-1]     # gateway resolves infra-redis-1 → this service
networks:
  ai-gateway:
    driver: overlay
```
- Attaching the **existing** `redis_redis` to the gateway overlay with an `infra-redis-1` alias also works and avoids a second Redis. Either way the gateway's hardcoded `infra-redis-1:6379` resolves with **zero env change**.
- Redis holds only ephemeral state (rate-limit windows, quota counters rebuilt from DB, FSM mirror). A fresh empty Redis on worker-vm is acceptable — no Redis data migration required.

## Cutover + Rollback Sequencing

**Zero/low-downtime, DNS-stable, single-seam:**

1. **Parallel bring-up** — deploy gateway+embed+redis+dashboard swarm stacks on worker-vm against `bd_ai_gateway` (post tenant/key migration). n8n-ia-vm prod keeps serving; both read the SAME DO cluster but DIFFERENT dbs (`_prod` vs target) → no write contention.
2. **Validate on worker-vm directly** (before flip): `curl http://10.10.10.50:80/health` via internal Traefik; real tenant key → `POST /v1/chat/completions` HTTP 200; confirm a `billing_events` row appears in `bd_ai_gateway`; embed call 200; `X-Admin-Key` → `/admin/metrics` 200.
3. **Flip the seam** — on vps-ifix-vm edit `/home/pedro/projetos/pedro/infra/traefik-dynamic/ai-gateway-prod.yml`: service `n8n-ia-prod-internal` server `http://10.10.10.20:80` → `http://10.10.10.50:80`. Hot-reload (no restart). Same file also fronts `ai-dashboard.converse-ai.app` → point dashboard to worker-vm too (or split the service if dashboard cutover lags).
4. **Watch** edge logs + first live 200s + billing writes to `bd_ai_gateway`.
5. **Decommission** (only after steady state): stop `/opt/ai-gateway-prod` compose on n8n-ia-vm, stop embed/rerank compose, delete Portainer stack 34 on vps-ifix-vm. **Embed dependency gate:** the gateway's `UPSTREAM_EMBED_URL=http://10.10.10.20:7997` lives on n8n-ia-vm — the worker-vm embed service MUST be up and the env re-pointed (to the worker-vm embed service DNS / `172.18.0.1:7997`) BEFORE n8n-ia-vm goes down, or all embed traffic 500s.

**Rollback:** revert the edge server URL line to `http://10.10.10.20:80`, hot-reload. Instant. (Valid until n8n-ia-vm is decommissioned — keep it running through a soak window before tearing down.)

## Common Pitfalls

### Pitfall 1: Embed upstream dies with n8n-ia-vm
`UPSTREAM_EMBED_URL=http://10.10.10.20:7997` is the bge-m3 Infinity server on n8n-ia-vm. Decommissioning it kills embeds silently. **Avoid:** deploy `ai-gateway-embed` (Infinity, CPU — worker-vm has ~9G free, bge-m3 CPU ~2-4G) as a swarm service and re-point the env before decommission. Decide same for `ai-gateway-rerank` (CONTEXT deferred: consolidate vs drop).

### Pitfall 2: git-stack relative bind-mount on agent endpoint 6
Breaks — files not on agent host (memory). **Avoid:** compose-string stacks with absolute paths or baked config.

### Pitfall 3: `172.18.0.1:18000` unreachable from an overlay-only service
`172.18.0.1` is worker-vm's default **docker bridge** gateway. A swarm service on an overlay network does NOT automatically route to the host bridge. If/when a local pod binds `:18000`, either attach the gateway service to the host bridge network too, or use the host's LAN IP `10.10.10.50:18000`. **Non-blocking now** (no pod runs; gateway is tier-1), but bake the reachability into the stack so a future pod works. `PRIMARY_POD_SCHEDULE_DISABLED=true` keeps this dormant.

### Pitfall 4: Portainer webhook won't pull the new image
Webhook fires but image stays at old digest (memory + STATE). **Avoid:** after any image push, `docker service update --image <tag> --force <svc>` (swarm equivalent of pull+recreate).

### Pitfall 5: MIGRATE_ON_BOOT + wrong DB
Target `bd_ai_gateway` is v31, matching code — no pending migrations. Keep `AI_GATEWAY_MIGRATE_ON_BOOT=false`. Running against `_prod` by mistake would double-serve; the DSN in `.env` must point at `bd_ai_gateway`.

### Pitfall 6: model_alias / upstream drift
Prod DB carries client-depended aliases (`gpt-5.4-mini-2026-03-17`, `whisper` STT mirror) not present in the dev-origin target. A client calling an un-ported alias gets 404. **Avoid:** diff `model_aliases` (and `upstreams`) between the two DBs and port prod-only, client-facing aliases.

### Pitfall 7: qwen3.5 vs qwen3.6 weights key
`WEIGHTS_QWEN_KEY=qwen3.5-27b…` doesn't exist in R2 (has qwen3.6). **Non-blocking** — no primary/emergency pod runs; gateway serves tier-1. Documented known-gap; fix only if pods are reactivated (CONTEXT deferred).

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Docker Swarm (worker-vm) | stack deploy | ✓ | engine | — |
| Portainer agent endpoint 6 | UI-editable deploy | ✓ | 2.39.1 | manual `docker stack deploy` via SSH |
| Redis on worker-vm | rate-limit/quota | ✓ (`redis_redis`) | 7.4.2 | alias existing or add one |
| Traefik internal (worker-vm) | route edge→gateway | ✓ | v2.11 | — |
| Edge Traefik (vps-ifix-vm) | public TLS ingress | ✓ | v3.6 | — |
| `psql`/`pg_dump` reaching DO :25060 | DB migration | ✓ from ops-claude (IP whitelisted) | client 14+ | — |
| bge-m3 Infinity (embed) on worker-vm | embed traffic post-decommission | ✗ (only on n8n-ia-vm) | — | tier-1 OpenAI embed |
| local pod `:18000` | tier-0 LLM/STT | ✗ (no GPU) | — | tier-1 OpenRouter/Gemini (current mode) |
| R2 weights `qwen3.5` | primary pod | ✗ (R2 has 3.6) | — | none needed (no pod) |

**Missing with fallback:** embed → must be stood up on worker-vm before decommission (Pitfall 1); local pod → tier-1 (as dev runs today, acceptable).
**Missing, blocking:** none if the embed service moves before n8n-ia-vm teardown.

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | Portainer build accepts `POST /api/stacks/create/swarm/string` (or legacy `?type=1&method=string`) | Pattern 1 | Wrong route → 404; verify `GET /api/stacks` shape on the live build first |
| A2 | worker-vm internal Traefik will route `Host(ai-gateway.converse-ai.app)` at `:80` to the gateway service once labeled | Cutover step 3 | If internal Traefik config differs, edge must point at the gateway's published host port instead |
| A3 | Dev tenant slugs colliding with prod = `converseai`, `chat-ifix` (+ dev-only `uat10-test` etc.); prod set = 18 real tenants | Migration | Enumerate actual dev slugs via `SELECT slug FROM bd_ai_gateway…tenants` before the DELETE |
| A4 | admin_keys prod row(s) needed for `X-Admin-Key` ops to keep working | Migration | If a fresh admin key is minted on target instead, old `ifix_admin_49e4…` stops working (update CLAUDE.md) |
| A5 | billing_events left in `_prod` as archive is acceptable (no `/economia` history continuity requirement) | Migration | If continuity required, pre-create partitions / DEFAULT partition before COPY |
| A6 | worker-vm ~9G free RAM fits gateway + embed(CPU bge-m3 ~2-4G) + redis + dashboard | Env | If RAM tight, embed on CPU may OOM under load; measure |

## Open Questions

1. **Does the consolidated stack keep prod's tuned `upstreams`/`model_aliases`, or the dev target's?**
   - Known: locked env is dev-style (`bd_ai_gateway`, R2). Prod DB has client-facing aliases.
   - Unclear: which upstream set is "correct" for merged traffic.
   - Recommendation: keep target upstreams; **additively port** prod-only client-facing model_aliases. Diff both DBs in a planning task.

2. **Migrate `usage_counters` current-period rows or accept a quota reset?**
   - Known: `QuotaFailOpen=false`; counters rebuildable from billing but not automatically.
   - Recommendation: copy current-month `usage_counters` for the 18 tenants (small) to avoid mid-cycle under-count.

3. **Rerank service — consolidate onto worker-vm or drop?** (CONTEXT deferred.) Confirm any client depends on `/rerank` before dropping.

## Sources

### Primary (HIGH confidence)
- `gateway/db/migrations/0001,0002,0006,0010,0011,0013,0014,0016.sql` — exact DDL: FK graph, UNIQUE(slug), UNIQUE(key_lookup_hash), partitioned billing_events, tenant quota/shed columns
- `gateway/internal/config/config.go:358-537` — env→field map (`AI_GATEWAY_PG_DSN`, `AI_GATEWAY_REDIS_ADDR`, `UPSTREAM_EMBED_URL`, `MINIO_*`, `WEIGHTS_QWEN_KEY`, `MIGRATE_ON_BOOT`)
- `.planning/phases/10-prod-deploy-ai-gateway/10-PATTERNS.md:160-213` — edge Traefik file-provider router; the exact `n8n-ia-prod-internal → http://10.10.10.20:80` cutover seam + hot-reload/rollback mechanic
- `19-CONTEXT.md` — locked decisions, worker-vm recon (swarm, no GPU, redis_redis, no infra-redis-1, endpoint 6)

### Secondary (MEDIUM confidence)
- Auto-memories: `gateway-prod-build-deploy` (agent-endpoint bind-mount breakage, prod build recipe), `openrouter-token-and-stack-location` (stack 34 = dev, endpoint 3), `stt-model-alias-whisper-large-v3` (prod-only aliases + pg_notify), `prod-primary-schedule-and-email` (ops-claude systemd user timers)
- STATE.md — Portainer webhook no-pull; prod manual compose at /opt/ai-gateway-prod; goose v31; MIGRATE_ON_BOOT=false pattern

### Tertiary (LOW confidence)
- Portainer swarm-string API route exact path (A1) — verify against the live Portainer build before use

## Metadata

**Confidence breakdown:**
- DB migration (tables/FK/hash preservation): HIGH — DDL read directly
- Cutover seam (edge Traefik line): HIGH — router file documented in 10-PATTERNS
- Swarm/Portainer deploy mechanics: MEDIUM — behavior from memories + Docker semantics; exact API route needs live confirm
- Redis alias / embed move: HIGH — standard swarm patterns, recon-confirmed gaps

**Research date:** 2026-07-03
**Valid until:** 2026-08-02 (infra topology stable; re-confirm Portainer API route + live tenant/key counts at execution time)
