---
gsd_state_version: 1.0
milestone: v1.0
milestone_name: milestone
status: executing
last_updated: "2026-05-14T08:54:58.089Z"
progress:
  total_phases: 10
  completed_phases: 5
  total_plans: 54
  completed_plans: 52
  percent: 96
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

Phase: 06 (Auto-provisioning Emergency Pod) — autonomous plans complete; blocked on human UAT
Next autonomous-eligible work: Phase 07 (Observability — Dashboard & Alerting)

- **Phases 1–5:** COMPLETE on disk (all autonomous plans + VERIFICATION). Each carries a `human_needed` / `passed_partial` live-UAT deferral — the standard pattern when the dev stack is not yet deployed:
  - Phase 1: smoke.yml Vast.ai HUMAN-UAT pending
  - Phase 2: live deploy UAT pending (`02-VERIFICATION.md` human_needed); 02-09 cold-storage export is OPTIONAL — deferred to Phase 7/10 per Codex scope-creep ruling (GW-10 closed by 02-02)
  - Phase 3: SC-1 live failover UAT pending (`03-VERIFICATION.md` human_needed)
  - Phase 4: SC-1/SC-2/SC-4 live UAT deferred pending ai-gateway-dev stack deploy (`04-VERIFICATION.md` human_needed)
  - Phase 5: SC-4 + SC-5 deferred (`05-VERIFICATION.md` passed_partial)
- **Phase 6:** 10/11 plans executed (06-01..06-10 GREEN + summaries). 06-11 is `autonomous: false` HUMAN-UAT — Tasks 1+2 done (06-HUMAN-UAT.md + docs/RUNBOOK-EMERGENCY-POD.md created, commit 2b539fc); Task 3 is a **blocking** human-verify checkpoint (6 LIVE Vast.ai UAT scenarios, ~R$10-15). No 06-11-SUMMARY.md, no 06-VERIFICATION.md yet.
- **Phases 7–10:** Not started (no phase directories).
- **Status:** Executing — milestone at 52/54 plans (96%)

## Performance Metrics

- **Phases completed:** 5 / 10 (1–5 on disk; phase 6 plans done, pending human UAT)
- **Plans completed:** 52 / 54 (Phase 1: 9/9 · Phase 2: 8/9, 02-09 deferred · Phase 3: 8/8 · Phase 4: 9/9 · Phase 5: 8/8 · Phase 6: 10/11, 06-11 human UAT)
- **v1 requirements covered by executed plans:** POD-01..07, GW-01..10, TEN-01..09, RES-01..08, LSH-01..05, PRV-01..10 — 49/70 (remaining: OBS-01..08, INT-01..06, PRD-01..07 in Phases 7-10)

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

- [ ] Phase 2 close: live deploy UAT — set GitHub Secrets `PORTAINER_WEBHOOK_URL_DEV_GATEWAY` + `PORTAINER_WEBHOOK_URL_PROD_GATEWAY`; create Portainer stack `ai-gateway-dev` (Repository + webhook → `gateway/docker-compose.yml` on `develop`); run the 10-step post-push checklist in `02-08-SUMMARY.md`
- [ ] Phase 6 close: execute 06-HUMAN-UAT.md (6 LIVE Vast.ai scenarios, ~R$10-15) → fill sign-off → write 06-11-SUMMARY.md + 06-VERIFICATION.md
- [ ] Phase 7: 02-09 cold-storage audit export (Parquet + MinIO + retention DROP) — re-evaluate when audit_log grows past ~60 days of production traffic
- [ ] Phase 7: Confirm Ifix WhatsApp provider (Evolution API / Z-API / Chatwoot / proprietary)
- [ ] Phase 7: Choose dashboard auth (Better Auth instance vs shared with ConverseAI vs SSO)
- [ ] Phase 9: Obtain LGPD review sign-off from Ifix legal before activating sensitive tenants
- [ ] Tech debt (deferred from Phase 6): `gateway/internal/auth` argon2id tests hang under `-race`; `gateway/internal/db/migrate_test.go:53` migration count hard-coded 18, now 19 — fix via `/gsd-quick`

### Blockers

- **Phase 6 cannot reach COMPLETE without operator action:** 06-11 Task 3 is a blocking human-verify checkpoint requiring real Vast.ai spend. Autonomous mode cannot satisfy it. Phases 7-10 do not hard-depend on Phase 6 *verification* (they depend on Phase 6 FSM states/code, which exist) — but Phase 7's goal explicitly visualizes Phase 6 FSM states, so plan Phase 7 with Phase 6 code as-built.

## Session Continuity

- **Last session:** 2026-05-14T08:54:58.082Z
- **Next session should:** Run `/gsd-autonomous --from 7` to plan+execute Phases 7-10. Phase 6 stays at 10/11 pending operator HUMAN-UAT — track via Open Todos above, not as an autonomous blocker.

---

*State created: 2026-04-17*
*State repaired against disk: 2026-05-14*
