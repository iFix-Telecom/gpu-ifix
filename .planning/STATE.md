---
gsd_state_version: 1.0
milestone: v1.0
milestone_name: milestone
status: executing
last_updated: "2026-04-19T00:21:32.764Z"
progress:
  total_phases: 10
  completed_phases: 1
  total_plans: 18
  completed_plans: 16
  percent: 89
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

Phase: 02 (gateway-core-multi-tenant-auth) — EXECUTING
Plan: 7 of 9 complete (Wave 6 done)

- **Phase:** Phase 2 execution in progress. Waves 1–6 complete (02-01..02-07). 02-07 shipped the testcontainers-go integration test harness: 12 tests (counting 04b separately) exercising migrations, auth flow, audit writer, idempotency single + concurrent replay (D-C2 + B2 audit flag), model aliases, partition automation, upstream health caching, gateway subprocess E2E, and goroutine-leak regression — all behind `//go:build integration` so `go test ./...` stays unit-fast. Full suite wall time ~20s warm (60-90s first-run). Rule-1 fix during execution: `gateway/internal/db/pool.go` now registers `ai_gateway.{api_key_status,data_class}` ENUM OIDs via `conn.LoadType` in AfterConnect + `pool.Reset()` in `cmd/gateway/main.go` after boot-migration. Without this, sqlc-generated `interface{}` scan targets fail with "cannot scan unknown type (OID ...)" on fresh DBs — a latent bug that neither unit tests nor previous plans would have caught.
- **Reviews cycle (2026-04-18):** `/gsd-review --phase 2 --all` invoked Codex (Gemini/OpenCode/Qwen/Cursor/CodeRabbit missing; Claude skipped for independence). `02-REVIEWS.md` committed with 4 HIGH/MEDIUM + 2 LOW concerns. `/gsd-plan-phase 2 --reviews` revised 8/9 plans across 2 iterations. All 02-05/02-06/02-07 Codex concerns now resolved in shipped code (see 02-07-SUMMARY.md — B2 contract + goroutine leak + partition auto + auth hot path under load all covered by integration tests).
- **Plan:** next wave is 02-08 (Dockerfile + build-gateway.yml + Portainer stack, `autonomous: false` — human-verify first live deploy).
- **Status:** Executing Phase 02
- **Progress:** [█████████░] 89%

## Performance Metrics

- **Phases completed:** 1 / 10
- **Plans completed:** 16 / 18 (9 in Phase 1 + 7 executed in Phase 2 waves 1–6 + 1 optional staging plan)
- **v1 requirements covered by plans:** 21 / 70 (POD-01..POD-07 from Phase 1 + GW-01..GW-10, TEN-01, TEN-02, TEN-08, TEN-09 newly planned in Phase 2)
- **Plan 02-05:** duration 820s, 2 tasks, 14 files created, 1 file modified, 28 tests added
- **Plan 02-06:** duration 1100s, 2 tasks, 8 files created, 1 file modified, 32 tests added (19 hash+store + 13 middleware, all -race clean)
- **Plan 02-07:** duration 1200s, 2 tasks, 13 files created, 2 files modified, 12 integration tests added (testcontainers Postgres 16 + Redis 7; full suite ~20s wall time warm)

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

### Open Todos (for upcoming phases)

- [ ] Phase 3: Revisit per-route WriteTimeout (chat=0 for SSE, embeddings=30s, audio=120s) to restore slow-client-DoS defense on non-streaming routes (introduced by 02-01 config.go; acceptable for Phase 2 because Phase 4 adds rate-limiting)
- [ ] Phase 4: Wire request instrumentation middleware that calls `obs.RequestsTotal.WithLabelValues(route, status).Inc()` on the proxy layer (02-04 responsibility; the counter is already registered by 02-01)
- [ ] Phase 1 HUMAN-UAT: Validate Qwen 3.5 27B patched Jinja template on real Vast.ai pod (tool-call correctness — blocked on smoke.yml run)
- [ ] Phase 1 HUMAN-UAT: Empirical VRAM ceiling under load (2×8k-token chats + 1 long Whisper — blocked on smoke.yml run)
- [ ] Phase 1 HUMAN-UAT: Cold-start ≤5 min on fresh Vast.ai 4090 (blocked on smoke.yml run)
- [ ] Phase 3: Confirm OpenRouter upstream provider for Qwen 3.5 27B (Together? Fireworks? DeepInfra?)
- [ ] Phase 5: Tune saturation thresholds (inflight N, P95 ms, VRAM GB) from Phase 1 baseline
- [ ] Phase 6: Timeboxed (3h) Vast.ai REST API spike before committing the phase scope
- [ ] Phase 7: Confirm Ifix WhatsApp provider (Evolution API / Z-API / Chatwoot / proprietary)
- [ ] Phase 7: Choose dashboard auth (Better Auth instance vs shared with ConverseAI vs SSO)
- [ ] Phase 9: Obtain LGPD review sign-off from Ifix legal before activating sensitive tenants

### Blockers

None at present. Roadmap is ready for planning.

## Session Continuity

- **Last session:** 2026-04-19T00:21:32.759Z
- **Next session should:** Continue `/gsd-execute-phase 2` with Wave 7 (02-08 deploy) — `autonomous: false` — operator must human-verify first live deploy on dev VPS. 02-09 (audit export/retention, optional per Codex review scope-creep flag) can be deferred until Phase 7 dashboard work demands the cold-storage story. 02-07 integration tests provide the regression net for all Phase 2 cross-subsystem contracts (B2 audit replay flag, concurrent idempotency serialization, auth hot path under load, SSE mid-disconnect goroutine cleanup).

---

*State created: 2026-04-17*
