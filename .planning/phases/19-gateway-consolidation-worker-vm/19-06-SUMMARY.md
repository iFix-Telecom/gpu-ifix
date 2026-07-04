---
phase: 19-gateway-consolidation-worker-vm
plan: 06
subsystem: infra/decommission
tags: [decommission, teardown, ai-gateway, worker-vm, consolidation, archive, rollback-by-rebuild, systemd-timers, portainer]

# Dependency graph
requires:
  - phase: 19-05
    provides: "public ingress flipped to worker-vm; recorded cutover_ts + monitoring baselines; one-line edge rollback target (n8n-ia-vm still UP)"
  - phase: 19-04
    provides: "prod tenants/keys live in bd_ai_gateway; masked ops timers + timer-unit backups in ~/gw-migration-19/timer-units-backup/"
  - phase: 19-02
    provides: "consolidated gateway + embed live on worker-vm (so teardown does not kill embeds)"
provides:
  - "worker-vm is the SINGLE consolidated ai-gateway (gateway+embed+rerank+dashboard+redis) — old n8n-ia-vm prod stacks + vps-ifix-vm dev stack 34 decommissioned"
  - "Root-owned pre-teardown rebuild archive (ops-claude:~/gw-decomm-archive-19/) + retained /opt dirs + volumes + bd_ai_gateway_prod → rollback-by-rebuild is concrete"
  - "ops-claude systemd user timers (gateway-price-sync, prod-primary-report) unmasked + repointed at the worker-vm gateway + validated live"
affects: []

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Decommission-with-rebuild-path: root-owned secret-free archive (compose + env VAR NAMES + image digests + redacted inspect + logs) captured BEFORE teardown; docker compose down WITHOUT -v (retain volumes+dirs); source DB kept as billing archive"
    - "Timer repoint via dynamic swarm-task resolution: ssh worker-vm 'docker exec $(docker ps -q -f name=ai-gateway-prod_gateway|head -1) /gatewayctl ...' (swarm task-container name is non-deterministic)"

key-files:
  created:
    - ".planning/phases/19-gateway-consolidation-worker-vm/19-06-SUMMARY.md"
    - "ops-claude:~/gw-decomm-archive-19/ (root-600 rebuild archive — not in repo)"
  modified:
    - "ops-claude:~/bin/gateway-price-sync.sh (repoint n8n-ia-vm → worker-vm swarm container; backup .bak-19-06)"
    - "ops-claude:~/bin/prod-primary-schedule-report.sh (repoint FSM query → worker-vm; verdict now treats asleep+serving as OK on the schedule-disabled consolidated gateway; backup .bak-19-06)"
    - "ops-claude:~/.claude/CLAUDE.md (LOCAL-ONLY infra note — worker-vm = single consolidated gateway; stale n8n-ia-vm ops commands repointed)"
  infra:
    - "n8n-ia-vm: /opt/ai-gateway-{prod,embed,rerank} compose DOWN (ifix-ai-gateway + ifix-ai-dashboard + redis-gateway-prod + embed + rerank) — 0 containers; volumes + dirs + .env RETAINED (no -v)"
    - "vps-ifix-vm: Portainer stack 34 (ai-gateway-dev, endpoint 3) DELETED (HTTP 204; container gone)"
    - "ops-claude: 4 systemd user units restored from backup + unmasked + enabled (gateway-price-sync.{timer,service}, prod-primary-report.{timer,service})"
    - "bd_ai_gateway_prod: RETAINED as billing archive — 18 tenants / 34,130 billing rows — NOT dropped"

key-decisions:
  - "Blocking soak checkpoint PRE-APPROVED by user ('decommission now') on a thin ~54-min soak — accepted removing the one-line edge rollback target; teardown gated on soak evidence being CLEAN (abort-teardown rule honored)"
  - "docker compose down WITHOUT -v — volumes (embed/rerank model caches) + /opt dirs + .env retained as rebuild source for the retention window"
  - "prod-primary-report verdict adjusted: worker-vm is GPU-less + schedule-DISABLED (LOCKED DEC-01, tier-1 only), so FSM=asleep is the NORMAL steady state — report now alerts only on chat!=200 (prevents daily false-ALARM emails)"

patterns-established:
  - "Pre-teardown archive must be VERIFIED complete + secret-free BEFORE any destructive step (it is the sole rebuild path); inspect Env→names + Cmd/Args/Healthcheck redacted to avoid leaking runtime-resolved secrets (redis requirepass)"

requirements-completed: [DEC-01]

# Metrics
duration: ~50min
completed: 2026-07-04
---

# Phase 19 Plan 06: Gateway Decommission → worker-vm is the Single Gateway — Summary

**Decommissioned the redundant gateways after a clean (pre-approved) soak: captured a root-owned secret-free rebuild archive, verified the ~54-min soak breached NO abort trigger, unmasked+repointed the two ops timers at the worker-vm gateway, then tore down the n8n-ia-vm prod stacks (gateway+dashboard+embed+rerank+redis, volumes/dirs retained) and deleted Portainer dev stack 34 — worker-vm is now the ONLY ai-gateway, serving the unchanged public hostname with no fallback, billing still landing in bd_ai_gateway.**

## Performance

- **Duration:** ~50 min (archive + soak evidence + timer repoint + teardown + docs)
- **soak window:** cutover_ts `2026-07-04T19:22:52Z` → teardown ~`2026-07-04T20:20Z` (~54 min live)
- **Tasks:** Task 1 (archive) + Task 1b (timers) + Task 2 (teardown/docs) auto; blocking soak checkpoint pre-approved this session
- **Files modified:** 2 timer scripts + 1 local-only infra note; remote infra teardown (no repo source commits)

## Task 1 — Pre-teardown rebuild archive (root-owned, secret-free)

Built `ops-claude:~/gw-decomm-archive-19/` (dir 700, files 600, **chown root:root**) WHILE the old stacks were still UP. Contents (13 files):

```
n8n-ia-vm/ai-gateway-prod.docker-compose.yml       # gateway + dashboard + redis-gateway-prod (single compose)
n8n-ia-vm/ai-gateway-embed.docker-compose.yml
n8n-ia-vm/ai-gateway-rerank.docker-compose.yml
n8n-ia-vm/ai-gateway-prod.env-VARNAMES.txt         # env VAR NAMES ONLY (no values)
n8n-ia-vm/image-digests.txt                        # running tag + image_id + repodigest per container
n8n-ia-vm/inspect-all.redacted.json                # docker inspect; Env→names, Cmd/Args/Healthcheck redacted
n8n-ia-vm/logs/{ifix-ai-gateway,ifix-ai-dashboard,ai-gateway-embed,ai-gateway-rerank,redis-gateway-prod}.tail500.log
vps-ifix-vm-stack34/stack34.docker-compose.yml     # Portainer stack 34 resolved file (git-based)
vps-ifix-vm-stack34/stack34.metadata.redacted.json # stack def + env NAMES only + GitConfig
```

**Secret hygiene (verified):** env values never captured (VAR NAMES only); `docker inspect` `.Config.Env` reduced to names and `.Config.Cmd`/`.Args`/`.Config.Healthcheck` redacted (these carried the runtime-resolved `redis --requirepass <pw>`). Full-archive scan for `ifix_sk_`/`ifix_admin_`/`sk-`/`AKIA`/`Bearer <tok>`/`-----BEGIN`/`requirepass <literal>` → **CLEAN**. Dir mode = 700.

**Image digests captured:** gateway `ghcr.io/ifixtelecom/ifix-ai-gateway@sha256:382a0fc80e...` (identical to the running worker-vm consolidated gateway), infinity embed/rerank `michaelf34/infinity@sha256:11e8b39...`, redis `redis@sha256:6ab0b6e...`, dashboard `:latest-dev` (no repodigest — local tag).

### EMERGENCY REBUILD PROCEDURE (rollback = rebuild)

The old stacks are gone; rollback is now a rebuild. Sources, in priority order:

1. **Fastest (dirs intact):** the `/opt/ai-gateway-{prod,embed,rerank}` dirs on **n8n-ia-vm** were RETAINED with their `docker-compose.yml` + `.env` (root-600) + named volumes. To rebuild the old prod gateway:
   ```bash
   ssh n8n-ia-vm 'cd /opt/ai-gateway-prod && docker compose up -d'   # gateway+dashboard+redis-gateway-prod
   ssh n8n-ia-vm 'cd /opt/ai-gateway-embed && docker compose up -d'
   ssh n8n-ia-vm 'cd /opt/ai-gateway-rerank && docker compose up -d'
   ```
   The retained `.env` still points at `bd_ai_gateway_prod` (the archived prod DB, kept intact).
2. **If dirs were pruned:** rebuild from `~/gw-decomm-archive-19/` — copy the compose files back, repopulate `.env` using `ai-gateway-prod.env-VARNAMES.txt` (names) with values sourced from the **worker-vm** `.env` (root-600) or the retained n8n-ia-vm `.env.bak-*`, pull the pinned digests from `image-digests.txt`, `docker compose up -d`.
3. **Flip ingress back to the rebuilt old gateway** (19-05 one-liner, still valid — restores edge → 10.10.10.20:8080/:3001):
   ```bash
   ssh vps-ifix-vm 'cp /home/pedro/projetos/pedro/infra/traefik-dynamic/ai-gateway-prod.yml.bak-pre19-05 \
                       /home/pedro/projetos/pedro/infra/traefik-dynamic/ai-gateway-prod.yml'
   ```
4. **Dev stack 34** rebuild = re-create the Portainer git-stack (repo `IfixTelecom/gpu-ifix`, `gateway/docker-compose.yml`, endpoint 3) — env NAMES in `stack34.metadata.redacted.json`.

## Soak evidence (RECORDED; abort-teardown rule honored)

The user's pre-approval was conditional on a CLEAN soak. All metrics gathered 2026-07-04 ~20:16–20:22Z (~54 min post-cutover). **No abort trigger tripped:**

| Metric | Value | Verdict |
|--------|-------|---------|
| Duration since cutover | ~54 min | thin soak, user pre-approved |
| billing_events since cutover (bd_ai_gateway) | 90 rows (42→90 growing); last_5min=6, last_15min=18; max_ts 20:14; 4 tenants | GROWING ✓ |
| billing routes | chat=53, stt=36, embed=1 | real multi-route prod (incl voip STT) ✓ |
| edge 5xx (vps-ifix-vm traefik, 30m) | 0 | ✓ |
| gateway 401/403 (worker-vm, 30m) | 0 | no auth regression ✓ |
| gateway db/redis errors (30m, excl dormant :18000) | 0 | ✓ |
| public /health | 200 `main-5553bd4` (worker-vm) | ✓ |
| chat-ifix chat / embed | 200 / 200 (embed upstream=`local-embed` on worker-vm) | ✓ embed on worker-vm |
| prod admin `/admin/metrics` | 200 | ✓ |
| bogus key (neg control) | 401 | auth preserved ✓ |
| dashboard `/` | 307 reachable | ✓ |

## Task 1b — Timers unmasked + repointed at worker-vm

The two ops-claude systemd USER timers (masked in 19-04) were unmasked and repointed **before** teardown so nothing pointed at the dead n8n-ia-vm gateway.

- **Recovery gotcha:** `systemctl --user unmask` removed the `/dev/null` mask symlinks, but 19-04's masking had left NO real unit behind them → `enable` failed "unit does not exist". Restored the 4 real units (`gateway-price-sync.{timer,service}`, `prod-primary-report.{timer,service}`) from the backup `~/gw-migration-19/timer-units-backup/`, `daemon-reload`, `enable --now`.
- **Repoint (both scripts SSH'd into `n8n-ia-vm docker exec ifix-ai-gateway`):** changed to the worker-vm consolidated gateway via dynamic swarm-task resolution `ssh worker-vm 'docker exec $(docker ps -q -f name=ai-gateway-prod_gateway|head -1) /gatewayctl ...'`. The report's live-chat check already used the public hostname (already worker-vm) — unchanged.
- **Validated LIVE (real runs, exit 0):**
  - `gateway-price-sync`: `fx_updated=1 models_updated=4 models_skipped=0` — wrote pricing into the consolidated `bd_ai_gateway` via worker-vm `gatewayctl` (`price updated` log lines from the worker-vm container). Repoint proven end-to-end.
  - `prod-primary-report`: log `Sat, 04 Jul 2026 17:21:07 sent FSM=asleep chat=200` — FSM resolved from worker-vm, chat 200, email sent.
- **Final state:** both timers `enabled` (NOT masked), next run **Mon 2026-07-06 08:30 / 09:30 BRT**.

## Task 2 — Teardown (volumes/dirs retained) + docs

- **n8n-ia-vm:** `docker compose down` (NO `-v`) in `/opt/ai-gateway-prod` (removed `ifix-ai-gateway` + `ifix-ai-dashboard` + `redis-gateway-prod`), `/opt/ai-gateway-embed`, `/opt/ai-gateway-rerank`. **0** gateway containers remain. Volumes `ai-gateway-embed-model-cache` + `ai-gateway-rerank-model-cache` **retained**; `/opt/ai-gateway-*` dirs + `.env` + compose **retained**.
- **vps-ifix-vm:** Portainer stack 34 (ai-gateway-dev, endpoint 3) `DELETE` → **HTTP 204**; GET → 404; stack list count 0; no leftover container on vps-ifix-vm.
- **bd_ai_gateway_prod:** RETAINED as billing archive — 18 tenants / **34,130** billing rows — NOT dropped.
- **Post-teardown public smoke (worker-vm is now the ONLY gateway, no fallback):** `/health` 200 `main-5553bd4`; chat-ifix chat 200; embed 200; prod admin `/admin/metrics` 200; dashboard 307; billing still landing (last_5min=7, fresh max_ts).
- **Topology docs:** updated `~/.claude/CLAUDE.md` (LOCAL-ONLY) — worker-vm VM-row now notes the consolidated gateway; added a Phase-19-06 topology block under the API Keys header; repointed the stale `ssh n8n-ia-vm 'docker exec ifix-ai-gateway ...'` admin-key/key list commands to the worker-vm swarm-task form. No secrets added.

## Final consolidated topology

worker-vm (10.10.10.50), Portainer swarm endpoint 6 — the SINGLE gateway:

| service | replicas | image |
|---------|----------|-------|
| ai-gateway-prod_gateway | 1/1 | ifix-ai-gateway@sha256:382a0fc8 |
| ai-gateway-prod_redis | 1/1 | redis:7.4.2 (infra-redis-1 alias) |
| ai-gateway-embed_embed | 1/1 | infinity:0.0.77 (bge-m3) |
| ai-gateway-rerank_rerank | 1/1 | infinity:0.0.77 (bge-reranker-base) |
| ai-gateway-dashboard_dashboard | 1/1 | ifix-ai-dashboard@sha256:07b7d18 |

Reads `bd_ai_gateway` (live billing) + R2 weights + `infra-redis-1`. Public ingress unchanged (edge Traefik vps-ifix-vm → 10.10.10.50:80). `bd_ai_gateway_prod` = read-only billing archive.

## Deviations from Plan

### Auto-fixed / adjustments

**1. [Rule 3 - Blocking] Timer units restored from backup after unmask.** Unmasking deleted the `/dev/null` mask symlinks but 19-04 left no real unit behind them → `enable` errored. Restored the 4 real units from `~/gw-migration-19/timer-units-backup/` (the plan-anticipated backup), then enabled. Timers now active.

**2. [Rule 1 - Correctness] prod-primary-report verdict adjusted for the consolidated gateway.** worker-vm is GPU-less + `PRIMARY_POD_SCHEDULE_DISABLED` (LOCKED DEC-01, tier-1 only), so FSM=asleep is now the steady state. Left unchanged, the report would email a daily false "ALERTA — pod ASLEEP" every business day. Adjusted so the verdict keys off live-serving (chat==200 → OK; asleep is normal) and only ALERTs when the gateway stops serving. Email wording updated to reflect worker-vm consolidation; troubleshooting hint repointed to `docker service logs ai-gateway-prod_gateway`. Backups `.bak-19-06` retained for both scripts.

## Retention / cleanup notes (for the retention window)

- Retained until rollback confidence lapses (~14 days, matching 19-04's dump retention): n8n-ia-vm `/opt/ai-gateway-*` dirs + `.env` + volumes; `~/gw-decomm-archive-19/`; `~/gw-migration-19/` dumps; `bd_ai_gateway_prod` DB.
- Out-of-scope leftover (NOT touched): a scaled-to-zero swarm service `ai-gateway-dev_gateway` (0/0) exists on worker-vm from an earlier phase — unrelated to stack 34; left as-is (logged to deferred).

## Threat Flags

None — teardown + config edits only; no new network surface, auth path, or schema change. Archive is secret-free (scan clean); `bd_ai_gateway_prod` retained (no billing-history repudiation); rollback path preserved (archive + retained dirs/volumes/DB).

## Known Stubs

None.

## Self-Check: PASSED

- `19-06-SUMMARY.md` exists ✓
- Rebuild archive `ops-claude:~/gw-decomm-archive-19/` present (13 files, root-600, dir 700, secret-scan clean) ✓
- n8n-ia-vm gateway containers = 0 (teardown confirmed); volumes + /opt dirs + .env retained ✓
- Portainer stack 34 deleted (204 → 404) ✓
- Public `ai-gateway.converse-ai.app/health` = 200 (worker-vm, no fallback) post-teardown ✓
- Both ops timers `enabled` (not masked) + repointed at worker-vm + validated live ✓
- `bd_ai_gateway_prod` retained (18 tenants / 34,130 billing) ✓
- No per-task repo commits (decommission is remote infra ops, not source changes) — final metadata commit below.
