---
gsd_state_version: 1.0
milestone: v1.0
milestone_name: milestone
status: executing
last_updated: "2026-04-17T23:29:00.000Z"
progress:
  total_phases: 10
  completed_phases: 0
  total_plans: 9
  completed_plans: 6
  percent: 0
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

Phase: 1 (GPU Pod Image & Smoke-Test) — EXECUTING
Plan: 7 of 9

- **Phase:** Phase 1 (Waves 1-3 complete: 01-01..01-06)
- **Plan:** Wave 4 next (01-07: GHA build-pod.yml)
- **Status:** Executing Phase 1
- **Progress:** `[──────────]` 0/10 phases complete (0%)

## Performance Metrics

- **Phases completed:** 0 / 10
- **Plans completed:** 0 / 0 (plans TBD per phase)
- **v1 requirements covered by plans:** 0 / 70

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
- Pre-baked pod Docker image (`ghcr.io/ifixtelecom/ifix-ai-pod`) with weights embedded — NOT re-download on provision

### Open Todos (for upcoming phases)

- [ ] Phase 1: Validate Qwen 3.5 27B patched Jinja template against upstream
- [ ] Phase 1: Empirical VRAM ceiling under load (2×8k-token chats + 1 long Whisper)
- [ ] Phase 3: Confirm OpenRouter upstream provider for Qwen 3.5 27B (Together? Fireworks? DeepInfra?)
- [ ] Phase 5: Tune saturation thresholds (inflight N, P95 ms, VRAM GB) from Phase 1 baseline
- [ ] Phase 6: Timeboxed (3h) Vast.ai REST API spike before committing the phase scope
- [ ] Phase 7: Confirm Ifix WhatsApp provider (Evolution API / Z-API / Chatwoot / proprietary)
- [ ] Phase 7: Choose dashboard auth (Better Auth instance vs shared with ConverseAI vs SSO)
- [ ] Phase 9: Obtain LGPD review sign-off from Ifix legal before activating sensitive tenants

### Blockers

None at present. Roadmap is ready for planning.

## Session Continuity

- **Last session:** 2026-04-17T21:39:49.161Z
- **Next session should:** Run `/gsd-plan-phase 1` to decompose Phase 1 (GPU Pod Image & Smoke-Test) into executable plans.

---

*State created: 2026-04-17*
