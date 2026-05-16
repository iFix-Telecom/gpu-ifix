---
gsd_state_version: 1.0
milestone: v1.0
milestone_name: milestone
status: executing
last_updated: "2026-05-15T10:58:06.000Z"
progress:
  total_phases: 10
  completed_phases: 6
  total_plans: 71
  completed_plans: 65
  percent: 60
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

Phase: 09 (client-integration-sensitive-tenants) — EXECUTING
Plan: 1 of 4
Next autonomous-eligible work: Phase 07 (Observability — Dashboard & Alerting)

- **Phases 1–5:** COMPLETE on disk (all autonomous plans + VERIFICATION). Each carries a `human_needed` / `passed_partial` live-UAT deferral — the standard pattern when the dev stack is not yet deployed:
  - Phase 1: smoke.yml Vast.ai HUMAN-UAT pending
  - Phase 2: live deploy UAT pending (`02-VERIFICATION.md` human_needed); 02-09 cold-storage export is OPTIONAL — deferred to Phase 7/10 per Codex scope-creep ruling (GW-10 closed by 02-02)
  - Phase 3: SC-1 live failover UAT pending (`03-VERIFICATION.md` human_needed)
  - Phase 4: SC-1/SC-2/SC-4 live UAT deferred pending ai-gateway-dev stack deploy (`04-VERIFICATION.md` human_needed)
  - Phase 5: SC-4 + SC-5 deferred (`05-VERIFICATION.md` passed_partial)
- **Phase 6:** 10/11 plans executed (06-01..06-10 GREEN + summaries). 06-11 is `autonomous: false` HUMAN-UAT — Tasks 1+2 done (06-HUMAN-UAT.md + docs/RUNBOOK-EMERGENCY-POD.md created, commit 2b539fc); Task 3 is a **blocking** human-verify checkpoint (6 LIVE Vast.ai UAT scenarios, ~R$10-15). No 06-11-SUMMARY.md, no 06-VERIFICATION.md yet.
  - **Integration tests (emerg suite): RESOLVED 2026-05-14.** First real CI run of `gateway/internal/integration_test/emerg_*` (Phase 6 deferred them to CI runtime — never executed before) failed 8 tests. 3 root causes found+fixed via `/gsd-debug`: (1) `freshSchema` missing `emergency_lifecycles` TRUNCATE → cross-test DB contamination (commit 9772d71); (2) stale Plan 06-05 force-provision/D-C5 test assertions vs reconciler evolved by 06-06+ (commit 355843b); (3) re-trigger oscillation race — `offer_race_lost` abort returned FSM straight to Healthy instead of Cooldown, `evaluateHealthy` re-fired the trigger every tick — fixed via new `ProvisionFailureCooldownSeconds` config (commit 85ba3da). All 22 emerg integration tests GREEN in CI run 25891568768 (build-gateway, develop). Debug sessions: `.planning/debug/emerg-integration-tests-ci.md` + `.planning/debug/emerg-bid-race-lost.md`.

- **Phases 7–10:** Not started (no phase directories).
- **Status:** Executing Phase 09

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
- [ ] Tech debt (Phase 6, descoberto 2026-05-16 durante UAT live lifecycle 29): `audit flush failed: invalid input value for enum data_class: "" (SQLSTATE 22P02)` a cada FSM state-change. Root cause: `gateway/internal/emerg/fsm.go:331` chama `auditWriter.WriteStateChange("fsm_transition", audit.Event{...})` sem setar `DataClass`; zero-value "" viola enum `ai_gateway.data_class` (`normal|sensitive`) que é NOT NULL desde migration 0019. State-change audit rows perdidos em batches de 2; FSM e Sentry breadcrumb não afetados. Fix candidato A (simples): `WriteStateChange` em `gateway/internal/audit/writer.go:167` seta default `ev.DataClass = "normal"`. Fix candidato B (semanticamente correto): nova migration adiciona valor `system` ao enum + `WriteStateChange` usa `system`. Fix via `/gsd-quick` pós-UAT.
- [ ] Tech debt (Phase 6, descoberto 2026-05-16 durante UAT live lifecycle 29): reconciler `provisionLifecycle` não detecta `status_msg=Error: GPU error, unable to start instance.` retornado pelo Vast.ai quando host tem hardware fault na 4090. Sintoma observado: instance 36904592 (host 1647/machine 15489) ficou ~6min em `actual=created` + `cur=running`/`stopped`/`running` oscilando, com `status_msg="Error: GPU error..."` desde o início, mas reconciler só fazia health-poll e iria dar `health_timeout` em 30min (coldstart_budget=1800s). Fix: em `gateway/internal/emerg/lifecycle.go` (loop de health-poll) adicionar checagem paralela de `vast.GetInstance(id).status_msg` — se contém `"Error:"`, `"GPU error"`, ou `"unable to start"` → `DestroyInstance` + `closeLifecycle(reason="vast_gpu_error")` + novo `ErrVastGPUError` em `errors.go` + breadcrumb Sentry. Reduz failed-host detection de 30min pra ~10s. Workaround atual: operator vê erro na UI Vast.ai + `gatewayctl emerg force-destroy` manual. Fix via `/gsd-quick` pós-UAT.
- [x] ~~Tech debt CRÍTICO (Phase 6, descoberto 2026-05-16 durante UAT live lifecycle 31): `handleForceProvision` não trata FSM em estado `cooldown`.~~ **RESOLVIDO 2026-05-16 via quick 260516-rym.** Fix: precheck `FSM.State()` rejeita força-provision se estado in `{EmergencyProvisioning, EmergencyActive, Recovering}`; substitui 2x `Transition(from, to)` por `SetState(StateEmergencyProvisioning)` que CAS-loops até commit regardless of current state. 2 regression tests em `emerg_force_command_cooldown_test.go`. Ver `.planning/quick/260516-rym-fix-force-provision-cooldown-transition/SUMMARY.md`.

### Blockers

- **Phase 6 cannot reach COMPLETE without operator action:** 06-11 Task 3 is a blocking human-verify checkpoint requiring real Vast.ai spend. Autonomous mode cannot satisfy it. Phases 7-10 do not hard-depend on Phase 6 *verification* (they depend on Phase 6 FSM states/code, which exist) — but Phase 7's goal explicitly visualizes Phase 6 FSM states, so plan Phase 7 with Phase 6 code as-built.

### Quick Tasks Completed

| # | Description | Date | Commit | Directory |
|---|-------------|------|--------|-----------|
| 260515-ayc | Fix STATE.md corruption (heading `## Current Position` duplicado injetado no meio do Phase 6 bullet) | 2026-05-15 | f44cf11 | [260515-ayc-fix-state-md-corruption-linha-40-42-tem-](./quick/260515-ayc-fix-state-md-corruption-linha-40-42-tem-/) |
| 260516-rym | Fix handleForceProvision não trata FSM em cooldown — pod Vast.ai órfão queimava $$ quando operator force-provision após falha. Precheck FSM.State() + SetState(EmergencyProvisioning) em vez de 2x Transition. +2 regression tests. | 2026-05-16 | 5aec0eb | [260516-rym-fix-force-provision-cooldown-transition](./quick/260516-rym-fix-force-provision-cooldown-transition/) |

## Session Continuity

- **Last session:** 2026-05-14T08:54:58.082Z
- **Next session should:** Run `/gsd-autonomous --from 7` to plan+execute Phases 7-10. Phase 6 stays at 10/11 pending operator HUMAN-UAT — track via Open Todos above, not as an autonomous blocker.

---

*State created: 2026-04-17*
*State repaired against disk: 2026-05-14*
