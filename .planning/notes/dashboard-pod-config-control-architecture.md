---
title: Dashboard pod-config control — architecture decisions
date: 2026-06-29
context: /gsd:explore session 2026-06-29 (post-Phase-16, triggered by the SSH+sed blocklist-append pain)
phase: 17
---

# Dashboard pod-config control — locked architecture

Decisions from the /gsd:explore session that should drive Phase 17 plan-phase. The motivating pain: every pod config change today (blocklist append, cap, schedule, shape) is `ssh n8n-ia-vm → sed .env → docker compose recreate` — manual, error-prone, no audit, no operator visibility.

## Core architecture (LOCKED)

1. **env-at-boot → DB-backed config.** Pod config moves from env vars (read by `gateway` `config.go` at import, fail-fast) into a `pod_config` table the gateway reads. This is the central refactor.

2. **Hybrid apply by config class:**
   - **Hot configs** → the reconciler re-reads from the DB each tick; changes take effect in seconds with NO restart. Candidates: `PRIMARY_VAST_MACHINE_BLOCKLIST`, `PRIMARY_VAST_MACHINE_ALLOWLIST`, price cap (`shape0_cap`), schedule (`UpHour`/`DownHour`/`provision_lead_seconds`), `coldstart_budget`, port-bind budget.
   - **Structural configs** → boot-only; require a gateway restart to apply. Candidates: `PRIMARY_NUM_GPUS`/shape, template image, DCGM/DSN/infra.
   - **Exact hot-vs-structural classification of the full surface is an OPEN research question** (see `research/questions.md`).

3. **Self-restart, NOT docker orchestration from the dashboard.** The dashboard is a Next.js app on n8n-ia-vm and must NEVER get the docker socket (web-app-as-root = unacceptable). To apply structural changes: dashboard calls `POST /admin/gateway/restart` → gateway flushes in-flight + `os.Exit(0)` → docker `restart: unless-stopped` policy (CONFIRMED present on prod `ifix-ai-gateway`, `HostConfig.RestartPolicy.Name=unless-stopped`) restarts the container → it re-reads the DB config. No `.env` edits, no docker socket exposure. The dashboard only ever talks to the gateway admin API.

4. **Live initialization status.** Dashboard polls `GET /admin/primary/lifecycle` → current FSM state + the `primary_lifecycles` event trail (offer_accepted → health checks → ready, or shutdown_reason on failure). This surfaces provisioning progress live and makes failures like the 2026-06-29 bad-host flap diagnosable from the UI instead of via psql.

## Security / guardrails (LOCKED)

- **Authz:** owner-only edits; operator read-only (same owner/operator model as Phase 13 user-mgmt).
- **Bounds per field:** validate ranges before save (e.g. cap $0.10–$1.50, UpHour 0–23, NUM_GPUS ∈ {1,2}); reject out-of-range. These configs control real money + prod availability — a bad cap never provisions, a wrong schedule keeps the pod down, a bad blocklist starves hosts, wrong NUM_GPUS OOMs.
- **Confirmation on dangerous actions:** restart, lowering the cap, changing shape → explicit confirm.
- **Audit everything:** every change records who/when/what (reuse Phase 13 `admin_audit_log`).

## Scope boundary

- **Restart = the GATEWAY container** (to apply structural config). NOT the Vast pod — the pod already has force-up/force-down via gatewayctl/admin.
- This is the gateway-side + dashboard-side; no client-app changes.

## Reuse anchors

- Phase 13: owner/operator authz, `admin_audit_log`, dashboard server-action + owner-gate pattern.
- Phase 15: dashboard `/economia` + the `/admin/*` admin-proxy route (`dashboard/src/app/api/gateway/[...path]/route.ts` injects X-Admin-Key) + `lib/gateway.ts` wrappers.
- Gateway `config.go` fail-fast env-at-boot = the thing being refactored to DB-backed.
- `primary_lifecycles` table (cols: started_at, first_health_pass_at, drain_started_at, ended_at, trigger_reason, vast_offer_id, vast_instance_id, accepted_dph, total_cost_brl, shutdown_reason, events jsonb, leader_replica) = the live-status data source.
