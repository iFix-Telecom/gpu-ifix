---
phase: 19-gateway-consolidation-worker-vm
plan: 05
subsystem: infra
tags: [traefik, cutover, ingress, ai-gateway, worker-vm, billing, hot-reload]

# Dependency graph
requires:
  - phase: 19-04
    provides: "prod tenants + api_keys migrated verbatim into bd_ai_gateway; live auth proofs vs worker-vm gateway (pre-cutover)"
  - phase: 19-02
    provides: "consolidated gateway + redis stacks live on worker-vm (Portainer swarm 38/39)"
  - phase: 19-03
    provides: "dashboard + rerank consolidated on worker-vm; model_aliases reconciled into bd_ai_gateway"
provides:
  - "Public ai-gateway.converse-ai.app + ai-dashboard.converse-ai.app served by the consolidated worker-vm gateway (edge Traefik server URL → 10.10.10.50:80)"
  - "Recorded cutover_ts + one-line reversible rollback + billing reconciliation procedure"
  - "Live-verified prod traffic + billing landing in bd_ai_gateway"
affects: [19-06]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Ingress cutover via edge Traefik file-provider server-URL edit (hot-reload, DNS-stable, single-line reversible)"

key-files:
  created:
    - ".planning/phases/19-gateway-consolidation-worker-vm/19-05-SUMMARY.md"
  modified:
    - "vps-ifix-vm:/home/pedro/projetos/pedro/infra/traefik-dynamic/ai-gateway-prod.yml (2 service-block server URLs → http://10.10.10.50:80)"

key-decisions:
  - "Cutover via the edge Traefik server-URL line (not DNS) — reversible seam, no cert re-issue"
  - "Both hostnames flipped together (gateway + dashboard) per user approval"
  - "n8n-ia-vm left running as instant rollback target through the 19-06 soak"
  - "Rollback billing model = accept-split-and-record (no double-count); window rows in bd_ai_gateway are authoritative"

patterns-established:
  - "Cutover pattern: edge preflight (Host-routed 200 from edge→target) BEFORE flip; identical backup as rollback target; live monitoring window with armed auto-rollback"

requirements-completed: [CUT-01]

# Metrics
duration: 20min
completed: 2026-07-04
---

# Phase 19 Plan 05: Ingress Cutover to worker-vm Summary

**Flipped the public prod ingress (`ai-gateway`/`ai-dashboard.converse-ai.app`) from n8n-ia-vm to the consolidated worker-vm gateway via a single hot-reloaded edge Traefik server-URL edit — DNS unchanged, real ~18-tenant traffic + billing now served by worker-vm, verified through a 15-min armed monitoring window with zero breach.**

## Performance

- **Duration:** ~20 min (flip + 16-min monitoring window)
- **cutover_ts:** `2026-07-04T19:22:52Z` (UTC, recorded immediately before the edit)
- **Monitoring window:** `2026-07-04T19:25:39Z` → `2026-07-04T19:41:45Z` (15 samples × 60s)
- **Tasks:** 2 auto tasks (checkpoint pre-approved this session)
- **Files modified:** 1 (edge file-provider on vps-ifix-vm)

## Accomplishments

- **Ingress flipped, both hostnames.** Edge Traefik file-provider `ai-gateway-prod.yml` on vps-ifix-vm: both service blocks (`ai-gateway-prod-upstream` was `http://10.10.10.20:8080`; `ai-dashboard-prod-upstream` was `http://10.10.10.20:3001`) → `http://10.10.10.50:80`. `grep -c "10.10.10.50:80"` = 2; residual `10.10.10.20` in service URLs = 0. Traefik v3.6 file-watcher hot-reloaded — no restart, no cert re-issue (DNS unchanged, TLS-ALPN certs already cached).
- **Public served by worker-vm.** `https://ai-gateway.converse-ai.app/health` → 200 `{"version":"main-5553bd4"}` (worker-vm build; pre-flip was n8n-ia-vm). Latency 0.074s (better than the 0.098s n8n-ia-vm baseline). `https://ai-dashboard.converse-ai.app/` → 307 (dashboard redirect, reachable).
- **Live smoke over the PUBLIC hostname (real prod keys):** normal tenant (chat-ifix) chat → 200 (openrouter/Novita `deepseek-v4-flash`); embeddings → 200 (`captain-embed`); sensitive tenant (telefonia) → 503 `upstream_unavailable_for_sensitive_tenant` (RES-08 auth-OK); bogus key → 401 (negative control proves the 503 is auth-preserved); prod admin `****613f` `/admin/metrics` → 200.
- **Billing lands in bd_ai_gateway from live traffic.** Since cutover: **42 billing_events** — chat=29, stt=12, embed=1 — proving real ~18-tenant production traffic (including voip STT transcription) now flows through worker-vm and meters correctly into the new DB.
- **Monitoring window HELD.** All 15 samples green: canary=200 throughout, edge 5xx(2m)=0, gateway 401/403(2m)=0, gateway DB/redis errors(2m)=0, billing continuously landing (2–8 rows/2m). Auto-rollback armed but never triggered.
- **Rollback target preserved.** n8n-ia-vm `ifix-ai-gateway` Up 46h (healthy), direct `:8080/health`=200 — left UP for the 19-06 soak. DNS/Cloudflare untouched (still 162.55.92.154). ops timers remain MASKED (19-06 unmasks).

## Task Commits

Cutover is a live config edit on a remote host (vps-ifix-vm), not a repo change, so there are no per-task source commits. The only repo artifact is this SUMMARY + STATE/ROADMAP updates:

1. **Task 1: Record cutover_ts + flip edge Traefik server URLs** — remote edit on vps-ifix-vm (no repo commit)
2. **Task 2: Public smoke + live monitoring + rollback/reconciliation notes** — remote verification (no repo commit)

**Plan metadata:** see final `docs(19-05)` commit below.

## Files Created/Modified

- `vps-ifix-vm:/home/pedro/projetos/pedro/infra/traefik-dynamic/ai-gateway-prod.yml` — 2 service-block server URLs flipped to `http://10.10.10.50:80` (hot-reload). Backup `.bak-pre19-05` (identical pre-flip snapshot) retained as rollback target.
- `.planning/phases/19-gateway-consolidation-worker-vm/19-05-SUMMARY.md` — this file.

## The Exact Edit (diff)

```diff
   services:
     ai-gateway-prod-upstream:
       loadBalancer:
         servers:
-          - url: "http://10.10.10.20:8080"
+          - url: "http://10.10.10.50:80"
         passHostHeader: true

     ai-dashboard-prod-upstream:
       loadBalancer:
         servers:
-          - url: "http://10.10.10.20:3001"
+          - url: "http://10.10.10.50:80"
         passHostHeader: true
```

## Rollback Command (verbatim)

Instant, one-line, hot-reloaded — valid until 19-06 teardown:

```bash
ssh vps-ifix-vm 'cp /home/pedro/projetos/pedro/infra/traefik-dynamic/ai-gateway-prod.yml.bak-pre19-05 \
                    /home/pedro/projetos/pedro/infra/traefik-dynamic/ai-gateway-prod.yml'
# Traefik v3.6 file-watcher hot-reloads within ~1s → public traffic returns to n8n-ia-vm (10.10.10.20).
```

The backup was verified `diff`-identical to the pre-flip file (rollback restores exactly `:8080` + `:3001` on 10.10.10.20). Keep n8n-ia-vm running until the 19-06 soak passes.

## Monitoring Commands + Thresholds (recorded)

Observation window: 15 samples × 60s (~16 min), plus spot-checks across the soak.

- **Edge 5xx (2m):** `ssh vps-ifix-vm "docker logs infra-traefik-1 --since 2m 2>&1 | grep -cE ' 50[0-9] '"` — baseline 0, held 0.
- **Gateway 401/403 (2m):** `ssh worker-vm "docker service logs ai-gateway-prod_gateway --since 2m 2>&1 | grep -cE ' 40[13] '"` — baseline 0, held 0.
- **Gateway DB/redis errors (2m), excluding dormant-pod noise:** `... | grep -iE 'infra-redis|failed to insert|billing.*error|pq:|dial tcp' | grep -vc '172.18.0.1:18000'` — held 0. (The `172.18.0.1:18000` tokenize dial-refused WARNs are the dormant local pod = expected tier-1 fallback, excluded.)
- **Billing lands:** `psql "$AI_GATEWAY_PG_DSN" -tAc "SELECT count(*) FROM ai_gateway.billing_events WHERE ts > now() - interval '2 min'"` — held 2–8/2m.
- **Latency:** `curl -w '%{time_total}'` on public /health — 0.074s (< n8n-ia-vm 0.098s baseline).

**Abort thresholds (auto-rollback if sustained ~2 min / 2 consecutive samples):** edge 5xx >2% of requests; gateway 401/403 above pre-cutover baseline (auth regression); any billing-insert failure; genuine redis/pg dial error; p95 latency >2× the n8n-ia-vm baseline. **None tripped** — cutover holds.

## Rollback Reconciliation (recorded)

Default = **accept-split-and-record, do NOT double-count**. Post-cutover writes land in `bd_ai_gateway`; a rollback at `rollback_ts` resumes writes to `bd_ai_gateway_prod`, splitting billing across `[cutover_ts, rollback_ts]`. Handling:

- Keep the window's rows in `bd_ai_gateway` as the authoritative append-only audit for that window (do NOT replay into `_prod` → avoids double counting).
- Audit export of the window (replay source if exact continuity is ever required):
  ```sql
  SELECT tenant_id, route, ts, cost_external_brl
  FROM ai_gateway.billing_events
  WHERE ts BETWEEN '<cutover_ts>' AND '<rollback_ts>';
  ```
- `usage_counters` are per-(tenant,date) upserts the app rebuilds from source-of-truth each cycle — a brief split self-heals; no manual replay.

Since the window HELD (no rollback), the authoritative record is simply all `bd_ai_gateway` rows from `cutover_ts` onward.

## Decisions Made

- **Backup not overwritten at flip.** The pre-existing `.bak-pre19-05` was verified `diff`-identical to the live pre-flip file, so it is the correct rollback target — left untouched rather than re-created, preserving the proven snapshot.

## Deviations from Plan

None — plan executed exactly as written. The blocking pre-cutover checkpoint was approved by the user ("flip both") before this continuation; the edge preflight (Host-routed 200/307 from vps-ifix-vm → 10.10.10.50:80) was re-run read-only before the flip.

## Issues Encountered

- An unrelated ACME `ERR` appeared in the edge Traefik log at flip time for `mcp-gateway-dev.converse-ai.app` (NXDOMAIN — a different, DNS-less domain). Not related to this cutover; our two domains' certs were already cached and DNS was unchanged. No action.

## Known Stubs

None.

## Threat Flags

None — config edit only; no new network surface, auth path, or schema change introduced by this plan.

## Post-cutover state (hand-off to 19-06)

- Public `ai-gateway` + `ai-dashboard.converse-ai.app` served by worker-vm (`ai-gateway-prod_gateway`, `main-5553bd4`); billing → `bd_ai_gateway`.
- n8n-ia-vm gateway still UP (Up 46h healthy) as the instant rollback target; DNS/Cloudflare unchanged; ops timers MASKED.
- 19-06 = soak validation → unmask + repoint ops timers → decommission n8n-ia-vm/vps-ifix-vm gateway stacks.

## Self-Check: PASSED

- SUMMARY file exists: `.planning/phases/19-gateway-consolidation-worker-vm/19-05-SUMMARY.md` — FOUND.
- Edge flip verified live: `grep -c "10.10.10.50:80"` on vps-ifix-vm = 2; residual `10.10.10.20` in service URLs = 0.
- Public served by worker-vm: `curl https://ai-gateway.converse-ai.app/health` → 200 `version=main-5553bd4`.
- Billing landing: 42 rows in `bd_ai_gateway` since `cutover_ts` (chat=29, stt=12, embed=1).
- Rollback target n8n-ia-vm: Up 46h healthy, `:8080/health`=200.
- No per-task repo commits (cutover is a remote config edit, not a source change) — nothing to grep in git log for tasks.
