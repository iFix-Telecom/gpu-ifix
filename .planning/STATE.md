---
gsd_state_version: 1.0
milestone: v1.0
milestone_name: milestone
status: executing
last_updated: "2026-04-18T10:56:16.000Z"
progress:
  total_phases: 10
  completed_phases: 1
  total_plans: 9
  completed_plans: 9
  percent: 10
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

Phase: 2 (Gateway Core + Multi-tenant Auth) — CONTEXT CAPTURED
Plan: — (not started)

- **Phase:** Phase 2 in discuss-phase complete — `02-CONTEXT.md` committed (4 areas discussed: API keys, Audit log, Idempotency-Key, Data layer foundations)
- **Plan:** run `/gsd-plan-phase 2` next
- **Status:** Phase 2 context gathered; planning pending
- **Progress:** `[█─────────]` 1/10 phases complete (10%)

## Performance Metrics

- **Phases completed:** 1 / 10
- **Plans completed:** 9 / 9 (Phase 1)
- **v1 requirements covered by plans:** 7 / 70 (POD-01..POD-07 — runtime validation on 3 items pending in 01-HUMAN-UAT.md)

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

- **Last session:** 2026-04-18T10:56:16Z
- **Next session should:** Run `/gsd-plan-phase 2` to plan Phase 2 (Gateway Core + Multi-tenant Auth) from `.planning/phases/02-gateway-core-multi-tenant-auth/02-CONTEXT.md`. Separately, set up GH Secrets + MinIO per `.planning/MINIO-SETUP.md` and run `smoke.yml` workflow_dispatch to close Phase 1 HUMAN-UAT items.

---

*State created: 2026-04-17*
