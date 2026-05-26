---
phase: 10-prod-deploy-ai-gateway
status: passed_partial
verified_at: 2026-05-26T16:15:00Z
operator: claude (autonomous follow-up session — Pedro authorized smokes + cascade-close)
session_wall_time_minutes: ~150 (live deploy ~80 + follow-up smokes ~70)
session_spend_usd: ~0.01 (S1 ~$0.001, S3 STT ~$0.006, S4 ~$0.001, S5 ~$0.002, S7 ~$0.001, S8 150 chats ~$0.005)
gaps_closed_phase_10_2026_05_26:
  primary_evidence:
    s1_chat_e2e_https: ai-gateway.converse-ai.app/v1/chat/completions returned 200 + provider=Novita (tier-1 OpenRouter fallback)
    s2_embed_e2e_https: ai-gateway.converse-ai.app/v1/embeddings returned 200 + 1024-dim multilingual-e5-large vector
    s3_stt_tier1: OpenAI Whisper smoke PASS — POST /v1/audio/transcriptions with probe.wav + model=whisper while local-stt FORCED_OPEN → HTTP 200 {"text":"you","usage":{"type":"duration","seconds":1}} (request_id=req_4156ec01f81a46d8af7f6e6da0d85d0f)
    s4_breaker_force_open_explicit: PASS — local-llm FORCED_OPEN + 2 chat probes both HTTP 200 (RIDs 019e6505-e1a5 + 019e6505-ea0a) via openrouter-chat tier-1; gateway logs confirm. NOTE audit_log evidence blocked by audit-flush UTF8 0x8b bug (see tech_debt below) — gateway request log + breaker state are primary evidence.
    s5_rate_limit_burst: PASS — uat10-test rps=5 + 10 parallel chats → 5x HTTP 200 + 5x HTTP 429 with Retry-After:1 + X-RateLimit-Limit-Requests:5 + X-RateLimit-Remaining-Requests:0; Prometheus gateway_rate_limit_rejected_total{tenant="uat10-test",window="rps"} incremented from 1 to 6 (delta=5 matches observed 429s).
    s6_billing_events: 1 row in ai_gateway.billing_events (request_id=019e6416-48c8-79cb-8f7b-d5958d664f11, tenant_id=b1acf9e1-9bee-4e3c-82d1-81a60b9a9cef, upstream=openrouter-chat, tokens_in=20, tokens_out=50, ts=2026-05-26T11:40:46Z) — direct psql access via $AI_GATEWAY_PG_DSN closed the original Phase 04 SC-2 deferral. SUBSEQUENT chat bursts S3-S8 (156+ requests) did NOT add billing rows — captured as tech-debt below.
    s7_peak_schedule_routing: PASS — uat10-test set-mode peak --window 14-15 --tz America/Sao_Paulo + chat probe at 13:08 BRT (NOW off-peak relative to peak window) → HTTP 200; Prometheus gateway_schedule_routing_total{decision="off_hours_external",tenant="uat10-test"} incremented from 0 to 1. Cleanup: tenant restored to 24/7.
    s8_vegeta_burst_5rps: PASS — vegeta 5 RPS × 30s (150 requests) against https://ai-gateway.converse-ai.app/v1/chat/completions with local-llm FORCED_OPEN; 100.00% success ratio (150/150 HTTP 200, zero errors); p50 4.13s, p95 5.93s, max 16.93s.
    s11_sentry_wiring: SENTRY_DSN populated in /opt/ai-gateway-prod/.env; project ifix-ai-gateway-prod (id=4511455942017024) created via Sentry API; 0 events captured yet (no panic / 5xx occurred during UAT).
    pre_uat_gate_a_preflight: 4/5 probes PASS (Connectivity/Capacity/Intra-attachable/GHA-runners); Probe 4 (Traefik internal swarm-mode discovery) FAILed as expected — operator-approved host-port bypass landed in commit 75bf0a5
    pre_uat_gate_b_postgres: bd_ai_gateway_prod + bd_ai_dashboard_prod created on DO managed instance; both sanity-probed
    pre_uat_gate_d_traefik_route: routers ai-gateway-prod@file + ai-dashboard-prod@file loaded on edge Traefik (v3.6) on vps-ifix-vm
    pre_uat_gate_e_dns: A records ai-gateway + ai-dashboard.converse-ai.app → 162.55.92.154 created + propagated (1.1.1.1 + 8.8.8.8 confirmed)
    pre_uat_gate_f_tls: LE cert issued via TLS-ALPN-01 after Pitfall 3 recovery (rm + re-add file-provider entry — see traefik-ops-rule-dns-first memory)
    gateway_health: /health 200 over https://ai-gateway.converse-ai.app; container healthy; uptime 3+ h at follow-up session
    dashboard_health: 307 → /login over https://ai-dashboard.converse-ai.app (Better Auth flow active)
    cascade_close_02_sc_5_step_7: commit 727dafb — Phase 02 VERIFICATION updated with prod-URL re-verify stanza
    cascade_close_03_sc_1: commit b5f310d — Phase 03 VERIFICATION updated; status remains passed
    cascade_close_04_sc_1_2_4: commit 8516113 — Phase 04 VERIFICATION updated; status FLIPPED passed_partial → passed (SC-1+SC-2+SC-4 all evidenced)
    cascade_close_05_sc_1: commit ec7260a — Phase 05 VERIFICATION updated; status remains passed
    cleanup_breakers: all FORCED_OPEN breakers force-closed; gatewayctl breaker list shows zero forced rows; uat10-test back to 24/7
  deferred_gaps:
    s9_per_tenant_smoke_6_tenants: only uat10-test tenant provisioned; converseai/chat-ifix/telefonia/cobrancas/campanhas/voice-api tenants need separate provision session (provision-tenants.sh covers 4 Phase-9 tenants; converseai+chat-ifix need manual gatewayctl provision)
    s10_rollback_drill_timed: deferred — v1.0.0 is first release tag, no previous main-<sha> wired in prod compose. Re-run when v1.0.1 is cut.
    s11_explicit_error_event_synthetic: PARTIAL PASS 2026-05-26T19:45Z — synthetic Sentry envelope POSTed directly to the DSN endpoint (manual SDK, not via gateway code path). Event ID 44c2bc7a9cef4afcafa988e5f0e58f13 indexed by Sentry; queryable via /api/0/organizations/grupo-ifix-telecom/issues/?query=release:develop-5bd79d1 → group 7507758942 (IFIX-AI-GATEWAY-PROD-1) at https://grupo-ifix-telecom.sentry.io/issues/7507758942/. Confirms DSN routes correctly, project ifix-ai-gateway-prod receives, release+environment tag routing works. CAVEAT — gateway's runtime panic-recovery path (httpx.Recoverer middleware at gateway/internal/httpx/recoverer.go:24) NOT verified live; no externally triggerable panic path identified in the current handler set (4xx/5xx error envelopes do NOT auto-emit Sentry events — only panics and emerg/toolcall capture sites do). Full canonical proof requires either (a) a `gatewayctl debug emit-error` subcommand (Phase 11 candidate) or (b) a known panic-inducing input path. Defer to Phase 11 prod-hardening.
    cut_release_v1_0_0_image_in_ghcr: tag pushed to git (refs/tags/v1.0.0 + main fast-forwarded) but GHA build-gateway.yml + build-dashboard.yml did NOT fire workflow run for the tag (same SHA as develop tip 1311a25 — GitHub deduped tag push). Prod stack pinned to :latest-dev (functionally identical, same SHA). Operator must re-trigger GHA via `gh workflow run` or re-push tag after deleting + recreating
    rotate_bootstrap_admin_key: DONE 2026-05-26T20:09Z — new admin key minted (id eaedc78d-8085-4341-ba2c-40444e06f898, label prod-ops-2026-05-26, prefix ifix_admin_****613f) and bootstrap key (id 58b9816c-a23f-47f1-86f6-b7f704859701, label bootstrap-random, prefix ifix_admin_****b259) revoked. Verified live: new key returns HTTP 200 on /admin/metrics; bootstrap returns HTTP 401. Both rows visible in `gatewayctl admin-key list` (active vs revoked).
  tech_debt: {}  # Both items below CLOSED in same sitting via single fix (commit 8e4298f / 5bd79d1) — see tech_debt_closed.
  tech_debt_closed:
    audit_flush_utf8_0x8b: CLOSED 2026-05-26T19:30Z by commit 8e4298f (gsd/phase-06.9-close) + cherry-pick 5bd79d1 (develop). Root cause was NOT in the audit package — BuildDirector forwarded client Accept-Encoding to the upstream verbatim. When the client sent Accept-Encoding:gzip (curl/browser default), Go http.Transport assumed end-to-end client compression and passed the gzipped resp.Body through unchanged. The audit middleware's auditResponseWriter.buf captured gzip bytes; ai_gateway.audit_log_content jsonb insert rejected 0x8b (gzip magic byte 2 of 1f 8b); transaction rolled back the matching audit_log row too. Fix is `r.Header.Del("Accept-Encoding")` in gateway/internal/proxy/director.go:80, which lets Transport negotiate + auto-decompress on its own. Verified live 2026-05-26T19:30Z on PROD (image develop-5bd79d1, RID 019e65ce-9ae2-728c-bae5-2eaae3dc20b4): audit_log row landed with upstream=llm + status_code=200. Regression test TestBuildDirector_StripsClientAcceptEncoding in director_test.go.
    billing_events_partial_capture: CLOSED 2026-05-26T19:30Z by the same commit. Same root cause — usageJSONBuffer.Close() tries to json.Unmarshal the buffered upstream body to extract tokens_in/tokens_out for billing.Event. When buf held gzipped bytes (Accept-Encoding path), Unmarshal silently failed and billing landed source=partial with tokens_in=0/tokens_out=0. With the Accept-Encoding strip, Transport hands plain JSON to usageJSONBuffer and the usage block parses successfully. Verified live 2026-05-26T19:30Z on PROD (same RID 019e65ce): billing_events row inserted with upstream=openrouter-chat + tokens_out=5 + source=final (was missing entirely pre-fix for all S3-S8 burst chats). Phase 04 SC-2 cascade-close stanza (commit 8516113) referenced this as separate Phase 11 tech debt; closure is upstream of the original scope — both observability streams (audit + billing) restored by one director fix.
  pitfalls_hit:
    pitfall_3_acme_order: rsynced edge Traefik route BEFORE creating DNS records — LE issued 5 NXDOMAIN auths, hit rate limit ("retry after 11:31:38 UTC"), Traefik backed off. Recovery: rm + re-rsync file-provider entry after DNS propagated → ACME retried + cert issued. Captured as memory `traefik-ops-rule-dns-first.md` for future sessions.
    pitfall_traefik_internal_swarm_mode: discovered during preflight probe 4 — traefik-internal_traefik on n8n-ia-vm runs v2.11 with --providers.docker.swarmMode=true; in v2 this flag makes docker provider ignore standalone (non-Swarm) containers. D-11 direct-compose gateway/dashboard would never be discovered via Traefik labels. Operator-approved fix: host-port bypass (publish 10.10.10.20:8080 + 10.10.10.20:3001) + edge Traefik routes directly. Plan/CONTEXT updated in commit 75bf0a5.

  deviations:
    d_02_ingress_path: CONTEXT.md D-02 said edge Traefik on ifix-prod-01 host; actual is vps-ifix-vm:443. RESEARCH already reconciled this.
    d_11_traefik_label_discovery: dropped per host-port bypass (above); Traefik labels removed from compose.prod.yml; edge file-provider points to host ports.
    n8n_ia_yml_mirror: artifacts/ai-gateway-prod.yml mirrors voice-api.yml shape (NOT n8n-ia.yml — which uses a different service grouping) — `letsencrypt` literal cert resolver.

  manual_state_to_persist:
    api_key_uat10_test: ifix_sk_ugsxqzp2wrnzassn7mrpl242mltyrsm5 (tenant_id=b1acf9e1-9bee-4e3c-82d1-81a60b9a9cef, data_class=normal — rotate or keep for follow-up smokes)
    bootstrap_admin_key: REVOKED 2026-05-26T20:09Z (id 58b9816c-a23f-47f1-86f6-b7f704859701). Active prod-ops admin key now is id eaedc78d-8085-4341-ba2c-40444e06f898 / prefix ifix_admin_****613f / label prod-ops-2026-05-26 — stored in operator credential vault, NOT in this committed doc.
    sentry_project_id: 4511455942017024 (DSN https://6bd9537d...@o4511455911608320.ingest.us.sentry.io/4511455942017024 wired into prod .env)
---

# Phase 10: prod-deploy-ai-gateway — Verification

## Status: passed_partial

A primeira produção do `ifix-ai-gateway` está LIVE em `https://ai-gateway.converse-ai.app` + `https://ai-dashboard.converse-ai.app`. Dashboard atrás de Better Auth; gateway atendendo `/v1/chat/completions` + `/v1/embeddings` em tier-1 (OpenRouter fallback) + tier-0 (Infinity embed local). Postgres prod databases criados e migrados. Sentry project criado e DSN populado. Edge TLS cert issuado por LE via TLS-ALPN-01 após recovery de Pitfall 3 (route loaded antes de DNS).

**Phase 10 GOAL alcançado:** prod stack reachable + first release-cut workflow exercitado (tag v1.0.0 pushed; GHA image-build deferred por dedup GitHub).

## What ran (autonomous live UAT + follow-up session)

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
| S3 Whisper STT tier-1 | PASS | force-open local-stt + probe.wav → HTTP 200 {"text":"you"} |
| S4 breaker force-open explicit | PASS | local-llm FORCED_OPEN + 2 chats 200 (audit_log gap due to UTF8 bug) |
| S5 rate-limit burst | PASS | 5×200 + 5×429 + headers + Prometheus delta=5 |
| S6 billing_events row | PASS | 1 row in ai_gateway.billing_events (psql direct) |
| S7 peak schedule routing | PASS | peak mode + chat → Prometheus off_hours_external=1 |
| S8 vegeta burst 5 RPS × 30s | PASS | 150/150 HTTP 200 (100% success); p95 5.93s via tier-1 |
| S11 Sentry wiring | PASS_PARTIAL | DSN + project OK; 0 events (no error during UAT) |
| Cascade-close Phase 02 SC-5 | PASS | commit 727dafb |
| Cascade-close Phase 03 SC-1 | PASS | commit b5f310d |
| Cascade-close Phase 04 SC-1+SC-2+SC-4 (status flip) | PASS | commit 8516113; status now passed |
| Cascade-close Phase 05 SC-1 | PASS | commit ec7260a |
| Cleanup mandatory | PASS | breaker list shows zero FORCED_OPEN; uat10-test back to 24/7 |
| S9 6-tenant smoke | DEFERRED | provision-tenants.sh covers 4; converseai+chat-ifix need manual provision |
| S10 rollback drill timed | DEFERRED | v1.0.0 is first release tag; re-run when v1.0.1 cut |
| S11 explicit error event | PARTIAL | Synthetic POST to DSN landed (event 44c2bc7a, group IFIX-AI-GATEWAY-PROD-1); gateway runtime panic path still unverified — Phase 11 task |

## What's left (deferred to follow-up session)

1. **Re-trigger GHA build for v1.0.0 tag** — operator runs `gh workflow run build-gateway.yml --ref v1.0.0` (or deletes/recreates tag). Once `:v1.0.0` image lands in ghcr.io, flip prod compose `${TAG:-v1.0.0}` (already default) and `docker compose up -d` to swap from `:latest-dev`.
2. ~~**Rotate bootstrap admin key**~~ — **DONE 2026-05-26T20:09Z**. New active key id=eaedc78d-8085-4341-ba2c-40444e06f898 (label prod-ops-2026-05-26). Bootstrap id=58b9816c revoked. See `gaps_closed_phase_10_2026_05_26.deferred_gaps.rotate_bootstrap_admin_key`.
3. **S9 per-tenant smoke** — provision 6 tenants (converseai/chat-ifix/telefonia/cobrancas/campanhas/voice-api) via `scripts/integration-smoke/provision-tenants.sh --mint-keys` + manual gatewayctl tenant create for converseai+chat-ifix; run `smoke-converseai.py` + `smoke-chat-ifix.py` + `smoke-sensitive-failover.py` against prod URL.
4. **S10 rollback drill timed** — re-run when v1.0.1 is cut (currently no previous `:main-<sha>` wired in prod compose).
5. **S11 explicit Sentry error probe (canonical, via gateway code path)** — synthetic POST to DSN was completed 2026-05-26T19:45Z (event 44c2bc7a, see `gaps_closed_phase_10_2026_05_26.primary_evidence.s11_explicit_error_event_synthetic`), but the gateway runtime panic-recovery middleware (httpx.Recoverer) was NOT exercised. Phase 11 should add a `gatewayctl debug emit-error` subcommand that invokes `panic()` inside an HTTP handler context, so the Recoverer + Sentry.CurrentHub().Recover + sentry.Flush chain can be operator-tested without contrived input attacks.
6. ~~**Audit-flush UTF8 0x8b bug**~~ — **DONE 2026-05-26T19:30Z** via commit 8e4298f + cherry-pick 5bd79d1. Root cause was BuildDirector forwarding client Accept-Encoding (NOT the audit package). Fix: `r.Header.Del("Accept-Encoding")` so Transport auto-decompresses. See `tech_debt_closed.audit_flush_utf8_0x8b` for full evidence.
7. ~~**Billing-events partial-capture bug**~~ — **DONE 2026-05-26T19:30Z** via same fix. usageJSONBuffer also choked on gzip bytes when extracting "usage" block for billing; one director fix restored both audit + billing pipelines. See `tech_debt_closed.billing_events_partial_capture`.
8. **Phase 11 (prod-hardening)** — covers PRD-01 load test, PRD-02/03 chaos tests, PRD-04 full incident runbook, PRD-05 LGPD sign-off, PRD-06 dashboard SSO. ROADMAP placeholder already created.

## Operator notes

- `/opt/ai-gateway-prod/.env` on n8n-ia-vm contains live secrets (doadmin DSN, OpenRouter key, OpenAI key, Vast key, MinIO creds, Sentry DSN, Better Auth secret). Mode 600. NOT committed.
- `AI_GATEWAY_MIGRATE_ON_BOOT` flipped to `false` after first deploy (operator restart ok).
- prod stack uses `:latest-dev` temporarily — swap to `:v1.0.0` once GHA build lands.
- Edge Traefik route at `vps-ifix-vm:/home/pedro/projetos/pedro/infra/traefik-dynamic/ai-gateway-prod.yml` (root-owned after re-rsync; chmod handled).
- Persistent learnings saved to `/home/pedro/.claude/projects/-home-pedro-projetos-pedro-gpu-ifix/memory/`: traefik-ops-rule-dns-first, sentry-user-token.
