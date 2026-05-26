---
phase: 10-prod-deploy-ai-gateway
status: passed_partial
verified_at: 2026-05-26T11:45:00Z
operator: claude (sessão autônoma, autorização full deploy concedida pelo Pedro)
session_wall_time_minutes: ~80
session_spend_usd: ~0.00005 (1 chat OpenRouter + 1 embed Infinity tier-0 + 1 invalid chat 400)
gaps_closed_phase_10_2026_05_26:
  primary_evidence:
    s1_chat_e2e_https: ai-gateway.converse-ai.app/v1/chat/completions returned 200 + provider=Novita (tier-1 OpenRouter fallback)
    s2_embed_e2e_https: ai-gateway.converse-ai.app/v1/embeddings returned 200 + 1024-dim multilingual-e5-large vector
    s6_billing_events: 1 row in ai_gateway.billing_events (request_id=019e6416-48c8-79cb-8f7b-d5958d664f11, upstream=openrouter-chat)
    s11_sentry_wiring: SENTRY_DSN populated in /opt/ai-gateway-prod/.env; project ifix-ai-gateway-prod (id=4511455942017024) created via Sentry API; 0 events captured yet (no panic / 5xx occurred during UAT)
    pre_uat_gate_a_preflight: 4/5 probes PASS (Connectivity/Capacity/Intra-attachable/GHA-runners); Probe 4 (Traefik internal swarm-mode discovery) FAILed as expected — operator-approved host-port bypass landed in commit 75bf0a5
    pre_uat_gate_b_postgres: bd_ai_gateway_prod + bd_ai_dashboard_prod created on DO managed instance; both sanity-probed
    pre_uat_gate_d_traefik_route: routers ai-gateway-prod@file + ai-dashboard-prod@file loaded on edge Traefik (v3.6) on vps-ifix-vm
    pre_uat_gate_e_dns: A records ai-gateway + ai-dashboard.converse-ai.app → 162.55.92.154 created + propagated (1.1.1.1 + 8.8.8.8 confirmed)
    pre_uat_gate_f_tls: LE cert issued via TLS-ALPN-01 after Pitfall 3 recovery (rm + re-add file-provider entry — see traefik-ops-rule-dns-first memory)
    gateway_health: /health 200 over https://ai-gateway.converse-ai.app; container healthy; uptime 13+ min
    dashboard_health: 307 → /login over https://ai-dashboard.converse-ai.app (Better Auth flow active)
  deferred_gaps:
    s3_stt_tier1: OpenAI Whisper smoke not run (cost ~$0.006 per minute audio; not critical for first deploy)
    s4_breaker_force_open_explicit: not explicitly run; S1 already used tier-1 fallback implicitly because primary local-llm pod absent on n8n-ia-vm (RES-08 path implicit)
    s5_rate_limit_burst: deferred (needs configured tenant rate-limit + parallel burst test)
    s7_peak_schedule_routing: deferred (needs clock manipulation OR off-hours window)
    s8_vegeta_burst_5rps: deferred (vegeta not installed; large smoke for follow-up)
    s9_per_tenant_smoke_6_tenants: only uat10-test tenant provisioned; converseai/chat-ifix/telefonia/cobrancas/campanhas/voice-api tenants need separate provision session
    s10_rollback_drill_timed: deferred (destructive; needs operator-approved separate session)
    cut_release_v1_0_0_image_in_ghcr: tag pushed to git (refs/tags/v1.0.0 + main fast-forwarded) but GHA build-gateway.yml + build-dashboard.yml did NOT fire workflow run for the tag (same SHA as develop tip 1311a25 — GitHub deduped tag push). Prod stack pinned to :latest-dev (functionally identical, same SHA). Operator must re-trigger GHA via `gh workflow run` or re-push tag after deleting + recreating
    cascade_close_02_sc_5_evidence: S1 success via prod URL confirms but separate cascade-close commit deferred
    cascade_close_03_sc_1_evidence: tier-1 fallback observed implicitly; explicit force-open breaker probe not run
    cascade_close_04_sc_1_2_4_evidence: only SC-2 (billing_events) has evidence (S6 PASS); SC-1 (rate-limit) + SC-4 (peak routing) deferred — Phase 04 stays passed_partial
    cascade_close_05_sc_4_5_evidence: S8 vegeta burst deferred — no evidence
  pitfalls_hit:
    pitfall_3_acme_order: rsynced edge Traefik route BEFORE creating DNS records — LE issued 5 NXDOMAIN auths, hit rate limit ("retry after 11:31:38 UTC"), Traefik backed off. Recovery: rm + re-rsync file-provider entry after DNS propagated → ACME retried + cert issued. Captured as memory `traefik-ops-rule-dns-first.md` for future sessions.
    pitfall_traefik_internal_swarm_mode: discovered during preflight probe 4 — traefik-internal_traefik on n8n-ia-vm runs v2.11 with --providers.docker.swarmMode=true; in v2 this flag makes docker provider ignore standalone (non-Swarm) containers. D-11 direct-compose gateway/dashboard would never be discovered via Traefik labels. Operator-approved fix: host-port bypass (publish 10.10.10.20:8080 + 10.10.10.20:3001) + edge Traefik routes directly. Plan/CONTEXT updated in commit 75bf0a5.

  deviations:
    d_02_ingress_path: CONTEXT.md D-02 said edge Traefik on ifix-prod-01 host; actual is vps-ifix-vm:443. RESEARCH already reconciled this.
    d_11_traefik_label_discovery: dropped per host-port bypass (above); Traefik labels removed from compose.prod.yml; edge file-provider points to host ports.
    n8n_ia_yml_mirror: artifacts/ai-gateway-prod.yml mirrors voice-api.yml shape (NOT n8n-ia.yml — which uses a different service grouping) — `letsencrypt` literal cert resolver.

  manual_state_to_persist:
    api_key_uat10_test: ifix_sk_ugsxqzp2wrnzassn7mrpl242mltyrsm5 (tenant_id=b1acf9e1-9bee-4e3c-82d1-81a60b9a9cef, data_class=normal — rotate or keep for follow-up smokes)
    bootstrap_admin_key: ifix_admin_5ffa147f6933e17a4d05c90b3b51b259 (ROTATE via `gatewayctl admin-key create` + revoke-bootstrap)
    sentry_project_id: 4511455942017024 (DSN https://6bd9537d...@o4511455911608320.ingest.us.sentry.io/4511455942017024 wired into prod .env)
---

# Phase 10: prod-deploy-ai-gateway — Verification

## Status: passed_partial

A primeira produção do `ifix-ai-gateway` está LIVE em `https://ai-gateway.converse-ai.app` + `https://ai-dashboard.converse-ai.app`. Dashboard atrás de Better Auth; gateway atendendo `/v1/chat/completions` + `/v1/embeddings` em tier-1 (OpenRouter fallback) + tier-0 (Infinity embed local). Postgres prod databases criados e migrados. Sentry project criado e DSN populado. Edge TLS cert issuado por LE via TLS-ALPN-01 após recovery de Pitfall 3 (route loaded antes de DNS).

**Phase 10 GOAL alcançado:** prod stack reachable + first release-cut workflow exercitado (tag v1.0.0 pushed; GHA image-build deferred por dedup GitHub).

## What ran (autonomous live UAT)

| Etapa | Status | Evidência |
|-------|--------|-----------|
| Gate A preflight | PASS (4/5 probes) | scripts/deploy/preflight.sh + 10-01-CAPACITY-OBSERVED.md |
| Gate B Postgres bootstrap | PASS | bd_ai_gateway_prod + bd_ai_dashboard_prod sanity-probed |
| Gate C cut-release v1.0.0 | PARTIAL | git tag pushed + main FF — GHA build NOT fired (dedup) |
| Gate D edge Traefik route | PASS | ai-gateway-prod@file + ai-dashboard-prod@file routers enabled |
| Gate E CF DNS | PASS | 2 A records propagated to 1.1.1.1 + 8.8.8.8 |
| Gate F TLS cert | PASS (after Pitfall 3 recovery) | LE cert issued via TLS-ALPN-01 |
| S1 chat E2E HTTPS | PASS | tier-1 OpenRouter via prod URL |
| S2 embed E2E HTTPS | PASS | tier-0 Infinity colocated (1024-dim) |
| S6 billing_events row | PASS | 1 row in ai_gateway.billing_events |
| S11 Sentry wiring | PASS_PARTIAL | DSN + project OK; 0 events (no error during UAT) |
| Cascade-close Phase 02/03/04/05 | DEFERRED | evidence partial; S4/S5/S7/S8 not run in this session |
| S3 STT / S4 breaker / S5 RL / S7 schedule / S8 vegeta / S9 6-tenants / S10 rollback | DEFERRED | follow-up session |

## What's left (deferred to follow-up session)

1. **Re-trigger GHA build for v1.0.0 tag** — operator runs `gh workflow run build-gateway.yml --ref v1.0.0` (or deletes/recreates tag). Once `:v1.0.0` image lands in ghcr.io, flip prod compose `${TAG:-v1.0.0}` (already default) and `docker compose up -d` to swap from `:latest-dev`.
2. **Rotate bootstrap admin key** — `gatewayctl admin-key create --label prod-ops-2026-05-26` then revoke `bootstrap-random` via `admin-key revoke <id>`.
3. **S3 STT smoke** — OpenAI Whisper tier-1, ~$0.006 per minute.
4. **S4 breaker force-open explicit** — `gatewayctl breaker force-open --name local-llm --ttl 60` + 2 chats; assert tier-1 routing.
5. **S5 rate-limit burst** — set tenant rps cap + parallel curl; expect 429 + X-RateLimit-Limit-Requests header.
6. **S7 peak schedule routing** — `gatewayctl tenant set-mode uat10-test peak --window 20-22`; chat at off-hours; expect openrouter-chat in dispatcher logs.
7. **S8 vegeta burst** — `vegeta attack -duration=30s -rate=5 -targets=/tmp/targets.txt`; expect ≥99% 200s, overflow to tier-1 when local saturated.
8. **S9 per-tenant smoke** — provision 6 tenants (converseai/chat-ifix/telefonia/cobrancas/campanhas/voice-api) via `scripts/integration-smoke/provision-tenants.sh`; run `smoke-converseai.py` + `smoke-chat-ifix.py` + `smoke-sensitive-failover.py` against prod URL.
9. **S10 rollback drill timed** — pre-prep `:vX.Y.Z` previous tag, time `docker compose up -d --force-recreate` swap; assert < 5 min recovery.
10. **Cascade-close 4 commits** — after S4/S5/S7/S8 PASS:
    - Phase 02 SC-5: small commit with `gaps_closed_phase_10_2026_05_26` evidence stanza
    - Phase 03 SC-1: small commit + evidence
    - Phase 04 SC-1/SC-2/SC-4: sed-flip `passed_partial` → `passed` + evidence
    - Phase 05 SC-4/SC-5: small commit + evidence
11. **Phase 11 (prod-hardening)** — covers PRD-01 load test, PRD-02/03 chaos tests, PRD-04 full incident runbook, PRD-05 LGPD sign-off, PRD-06 dashboard SSO. ROADMAP placeholder already created.

## Operator notes

- `/opt/ai-gateway-prod/.env` on n8n-ia-vm contains live secrets (doadmin DSN, OpenRouter key, OpenAI key, Vast key, MinIO creds, Sentry DSN, Better Auth secret). Mode 600. NOT committed.
- `AI_GATEWAY_MIGRATE_ON_BOOT` flipped to `false` after first deploy (operator restart ok).
- prod stack uses `:latest-dev` temporarily — swap to `:v1.0.0` once GHA build lands.
- Edge Traefik route at `vps-ifix-vm:/home/pedro/projetos/pedro/infra/traefik-dynamic/ai-gateway-prod.yml` (root-owned after re-rsync; chmod handled).
- Persistent learnings saved to `/home/pedro/.claude/projects/-home-pedro-projetos-pedro-gpu-ifix/memory/`: traefik-ops-rule-dns-first, sentry-user-token.
