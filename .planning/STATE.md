---
gsd_state_version: 1.0
milestone: v1.0
milestone_name: milestone
status: executing
last_updated: "2026-04-27T02:29:33.426Z"
progress:
  total_phases: 10
  completed_phases: 3
  total_plans: 43
  completed_plans: 34
  percent: 79
---

# STATE: ifix-ai-gateway

> Project memory. Single source of truth for "where am I now?"
> Updated on phase/plan transitions.

## Project Reference

- **Project:** ifix-ai-gateway
- **Core Value:** Nenhuma aplicação da Ifix sente quando a GPU cai. Failover invisível.
- **Current Milestone:** v1 — Ship the first working gateway with pod + auth + failover + auto-provisioning + 6 app integrations
- **Granularity:** fine (10 phases)
- **Mode:** yolo

## Current Position

Phase: 05 (load-shedding-saturation-aware-routing) — PARTIAL_PASS (2026-05-13)
Plan: 1 of 8

- **Phase:** 5 complete (3/5 SCs validated; SC-4 + SC-5 deferred); next: Phase 6 (Auto-provisioning Emergency Pod)
- **Phase 4 close (2026-04-21):** All 9 plans (04-01..04-09) shipped + 13-scenario integration suite green (70s testcontainers). Code review found 2 BLOCKERs + 4 HIGH + 6 MEDIUM — all fixed in commits `8b45240..6859ce9`. `04-VERIFICATION.md` status=human_needed (SC-1/SC-2/SC-4 LIVE UAT deferred pending ai-gateway-dev Portainer stack deploy — same pattern as Phase 2 SC-5 PARTIAL).
- **Status:** Phase 5 partial pass; transitioning to Phase 6
- **Progress:** [██████████] 77% (34/44 plans; 02-09 + Phase 4 live UAT deferred + Phase 1 HUMAN-UAT smoke.yml pending)

## Performance Metrics

- **Phases completed:** 1 / 10
- **Plans completed:** 17 / 18 (9 in Phase 1 + 8 executed in Phase 2 waves 1–7 + 1 optional staging plan)
- **v1 requirements covered by plans:** 21 / 70 (POD-01..POD-07 from Phase 1 + GW-01..GW-10, TEN-01, TEN-02, TEN-08, TEN-09 newly planned in Phase 2)
- **Plan 02-05:** duration 820s, 2 tasks, 14 files created, 1 file modified, 28 tests added
- **Plan 02-06:** duration 1100s, 2 tasks, 8 files created, 1 file modified, 32 tests added (19 hash+store + 13 middleware, all -race clean)
- **Plan 02-07:** duration 1200s, 2 tasks, 13 files created, 2 files modified, 12 integration tests added (testcontainers Postgres 16 + Redis 7; full suite ~20s wall time warm)
- **Plan 02-08:** duration 783s, 2 tasks committed + 1 deferred (human-verify), 5 files created (Dockerfile, .dockerignore, docker-compose.yml, build-gateway.yml, deferred-items.md) + 1 file modified (gateway/README.md); docker image 27.7 MB; CI pipeline 7 jobs mirroring build-pod.yml

## Accumulated Context

### Key Decisions (from research + PROJECT)

- Gateway language: Go (chi v5 + stdlib `httputil.ReverseProxy` + slog)
- LLM server: `llama.cpp` native (not `llama-cpp-python`)
- STT server: `speaches-ai/speaches` (not custom FastAPI)
- Embedding server: `michaelf34/infinity` (not `sentence-transformers`)
- Saturation signal: composite (inflight + P95 + VRAM + hysteresis), not GPU util alone
- Primary GPU: Vast.ai RTX 4090 (cost) with emergency Vast.ai pod failover (not RunPod Secure)
- LLM model: Qwen 3.5 27B Q4_K_M GGUF, fixed both primary and OpenRouter fallback
- Deploy: Docker Compose + Portainer + webhook GitHub (standard Ifix)
- Postgres: shared DO cluster with dedicated `ai_gateway` schema
- Pre-baked pod Docker image (`ghcr.io/ifixtelecom/ifix-ai-pod`, slim ~2 GB) with weights downloaded from Ifix MinIO at boot via `onstart.sh` (revised by Phase 1 per D-01/D-02/D-04 — image stays small, weights versioned by key path with SHA-256 integrity D-05)
- Plan 02-08: ship `/gateway` + `/gatewayctl` in the same distroless image (27.7 MB total) — ops model is `docker exec ifix-ai-gateway /gatewayctl <cmd>` rather than a separate sidecar image
- Plan 02-08: boot-time migrations via `AI_GATEWAY_MIGRATE_ON_BOOT` env flag instead of a dedicated CI migration job; goose idempotency makes this safe across restarts
- Plan 02-08: GitHub Actions `paths:` filter on pull_request only (not push) — mirrors build-pod.yml to avoid silently skipping stable-release tag pushes when the tag commit itself doesn't touch gateway/**

### Open Todos (for upcoming phases)

- [ ] Phase 3: Revisit per-route WriteTimeout (chat=0 for SSE, embeddings=30s, audio=120s) to restore slow-client-DoS defense on non-streaming routes (introduced by 02-01 config.go; acceptable for Phase 2 because Phase 4 adds rate-limiting)
- [ ] Phase 4: Wire request instrumentation middleware that calls `obs.RequestsTotal.WithLabelValues(route, status).Inc()` on the proxy layer (02-04 responsibility; the counter is already registered by 02-01)
- [ ] Phase 1 HUMAN-UAT: Validate Qwen 3.5 27B patched Jinja template on real Vast.ai pod (tool-call correctness — blocked on smoke.yml run)
- [ ] Phase 1 HUMAN-UAT: Empirical VRAM ceiling under load (2×8k-token chats + 1 long Whisper — blocked on smoke.yml run)
- [ ] Phase 1 HUMAN-UAT: Cold-start ≤5 min on fresh Vast.ai 4090 (blocked on smoke.yml run)
- [ ] Phase 3: Confirm OpenRouter upstream provider for Qwen 3.5 27B (Together? Fireworks? DeepInfra?)
- [ ] Phase 3: Add `UPSTREAM_LLM_AUTH_BEARER` (+ STT/EMBED variants) env to inject Authorization header in proxy Director (currently strips client auth but never adds upstream auth; needed for OpenRouter/cloud fallback). Required as part of failover/circuit-breaker work.
- [ ] Phase 5: Tune saturation thresholds (inflight N, P95 ms, VRAM GB) from Phase 1 baseline
- [ ] Phase 6: Timeboxed (3h) Vast.ai REST API spike before committing the phase scope
- [ ] Phase 7: Confirm Ifix WhatsApp provider (Evolution API / Z-API / Chatwoot / proprietary)
- [ ] Phase 7: Choose dashboard auth (Better Auth instance vs shared with ConverseAI vs SSO)
- [ ] Phase 9: Obtain LGPD review sign-off from Ifix legal before activating sensitive tenants

### Blockers

None at present. Roadmap is ready for planning.

## Session Continuity

- **Last session:** 2026-04-22T23:49:43.828Z
- **Next session should:** Phase 2 COMPLETE (verified by gsd-verifier — `02-VERIFICATION.md`, 4/5 SC PASS + 14/14 reqs PASS, 0 FAIL; SC-5 PARTIAL only because live deploy is human-verify checkpoint). Recommended next: `/clear` then `/gsd-plan-phase 3` (failover + circuit breakers). In parallel, user closes the SC-5 PARTIAL: (a) set GitHub Secrets `PORTAINER_WEBHOOK_URL_DEV_GATEWAY` + `PORTAINER_WEBHOOK_URL_PROD_GATEWAY`; (b) create Portainer stack `ai-gateway-dev` via "Repository + webhook" pointing at `gateway/docker-compose.yml` on `develop`; (c) `git push origin master` (or merge to `develop`); (d) run the 10-step post-push checklist in `02-08-SUMMARY.md`. 02-09 (cold-storage audit export) deferred to Phase 7/10 per Codex scope-creep ruling — re-evaluate when audit_log grows past ~60 days of production traffic.

---

*State created: 2026-04-17*
