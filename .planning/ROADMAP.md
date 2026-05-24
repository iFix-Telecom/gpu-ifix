### Phase 06.8: Multi-pod GPU topology + sizing + STT fix (INSERTED)

**Goal:** Make the gateway provision + health-poll the primary pod across multiple GPU topologies (preferring a 2×RTX 3090 single-pod, ~60% cheaper than a single 5090 with deeper Vast inventory) via runtime env (PRIMARY_NUM_GPUS=2 + allowlist), prove it end-to-end with a live force-up UAT, and fix the STT model-resolution bug (whisper tarball → HF-hub-cache layout + HF_HUB_CACHE) that blocks /v1/audio/transcriptions on every topology. Decides the GPU shape the SEED-002 emergency hot-standby will mirror, so it runs before SEED-002.
**Requirements**: STT-FIX, GW-2GPU, LADDER
**Depends on:** Phase 6, Phase 06.6 (primary pod Strategy B image + reconciler), Phase 06.7 (STT/speaches stack)
**Plans:** 5 plans (4 complete + 1 gap-closure)

Plans:

- [x] 06.8-01-PLAN.md — Wave 1: STT fix prep — regenerate whisper tarball in HF-hub-cache layout (upload-weights.sh) + HF_HUB_CACHE on [program:speaches] (supervisord.conf)
- [x] 06.8-02-PLAN.md — Wave 2: STT live-pod validation gate (rebuild image, spin pod, assert /v1/audio/transcriptions 200, propagate new SHA) — CLAUDE.md anti-blind-commit gate
- [x] 06.8-03-PLAN.md — Wave 3: Gateway 2×3090 live-UAT (A2 search pre-check + gatewayctl primary force-up + 4-endpoint health + nvidia-smi split) → SEED-002 shape input
- [x] 06.8-04-PLAN.md — Wave 1: Fallback topology ladder runbook + per-shape env presets (2×3090 → 5090 → Shape C deferred)
- [x] 06.8-05-PLAN.md — Wave 4 (gap closure): diagnose + fix the PRIMARY_VAST_MACHINE_ALLOWLIST steering bug (diagnose-first, operator-approval gate, minimal fix + unit test) → re-run 2×3090 force-up UAT targeting 43803 → markReady + STT 200 + nvidia-smi 2-GPU split

---

### Phase 06.9: OpenRouter model-rewrite per-upstream — close Phase 03 SC-1 fallback chain (INSERTED, promoted from SEED-004)

**Goal:** Fix the gateway dispatcher → tier-1 fallback model-name rewriting gap so `POST /v1/chat/completions {"model":"qwen"}` against ai-gateway-dev (with primary pod down) returns a real OpenRouter Qwen 3.5 completion instead of the current HTTP 404 "Not Found" HTML. Wave 0 Gate A (Phase 03, 2026-04-20) defined `UPSTREAM_LLM_OPENROUTER_MODEL=qwen/qwen3.5-27b` as the env var operator must set; Plan 03-06 implementation never wired it. Bug masked through Phase 04-08 because integration tests use a fake upstream that accepts any model name + live UAT was always deferred. Also surfaced same-shape gaps for openai-whisper (`UPSTREAM_STT_OPENAI_MODEL`) and openai-embed (`UPSTREAM_EMBED_OPENAI_MODEL`) — verify and bundle. Reference fix-path = SEED-004 Option B (schema-driven `model_aliases` PK widened to `(alias, upstream_name)`). Per D-06: schema row is the default, env vars remain the per-instance escape hatch (env wins over schema when set) — both supported permanently.

**Requirements:** OR-FIX (model rewrite per-upstream), STT-OAI-FIX (whisper), EMBED-OAI-FIX (embed), SC1-CLOSE (Phase 03 SC-1 live UAT closes via this fix)
**Depends on:** Phase 03 (fallback chain code in tree); Phase 06.8 (live primary FSM available for breaker-OPEN testing)
**Blocks:** Phase 02 SC-5 step 7 chat E2E; Phase 03 SC-1 live UAT; Phase 05 SC-1 full overflow; Phase 07 dashboard accuracy (tier-1 cost rows currently mislabeled when model never rewrote)
**Mode:** sequential (not MVP)
**Plans:** 4/7 plans executed
**Cost:** zero Vast spend (testable via existing /opt/ai-gateway-dev/ + live OpenRouter direct); ~2-3h wall

Plans:

- [x] 06.9-01-PLAN.md — Wave 0: Migration 0026 PK widening (alias, upstream_name) + 3 tier-1 seed rows + sqlc regen + migrate_test.go list update + 03-WAVE0-GATES.md URL convention correction (/api/v1 → /api) + D-06 env-override-wins doc (env stays as documented fallback override; NOT deprecated)
- [x] 06.9-02-PLAN.md — Wave 1: Resolver refactor — Refresh consumes UpstreamName column; aliasKey semantics ROLE → NAME; D-06 env-override-wins precedence layer (env → schema → passthrough) inside Resolve via curated upstreamEnvVarMap; 4 base + 4 env-override + 1 renamed unit tests; Handler middleware godoc deprecation
- [x] 06.9-03-PLAN.md — Wave 2: 3 Directors (OpenRouter + Whisper-multipart + Embed-refactor) gain (resolver, upstreamName) and rewrite body.model via per-upstream lookup; main.go removes models.Handler wraps + threads resolver+name into each Build*Director; WhisperAbortGuard wraps the Whisper handler chain (WARNING-3: duplicate-model HTTP 400 abort wired in this phase, no escape hatch)
- [x] 06.9-04-PLAN.md — Wave 2: Config fail-fast on UPSTREAM_*_URL ending in /v1 + INFO log on active D-06 env overrides (NOT deprecation WARN) + gatewayctl breaker {list,force-open,force-close} + gatewayctl model-alias {list,set,get,delete} CLI subcommands (operator surface for live UAT); breaker FSM force-override seam patched on existing eval-tick cadence (≤1ms overhead) per WARNING-4 entry-gate
- [ ] 06.9-05a-PLAN.md — Wave 3a (split): R8 freshSchema gate + body-capturing upstreamMock + newSelectiveMock + 3 model-rewrite integration tests (OR/Whisper/Embed) + 3 R6 Whisper edge-case tests (missing/duplicate/resolver-miss — all PASS, no SKIP per WARNING-3 wiring)
- [ ] 06.9-05b-PLAN.md — Wave 3b (split, depends_on 05a): R4 local-tier byte-identical (chat + embed) + R13 historical-bug regression (selective-reject mock) + R1 breaker force-override TTL restoration + R3 migration 0026 Down-abort guard + Up→Down→Up round-trip + BLOCKER-1/D-06 end-to-end env-override-wins integration tests (3 cases) + PROJECT.md tier-1 stack confirmation + D-06 coequal-paths doc note
- [ ] 06.9-06-PLAN.md — Wave 5 (autonomous: false): 06.9-HUMAN-UAT.md author with R2 hardened Pre-UAT preconditions + D-06 coequal-paths setup options (S1 schema CLI vs env var) + WARNING-6 dual breaker drivers (S1 force-open + docker-stop fallback; S2/S3 REQUIRE force-open) + operator-driven S1-S6 live UAT on dev stack (~$0.05 spend, no Vast/GPU) + cascade close Phase 02/03/05 VERIFICATION.md (3 small commits, WARNING-5 positive-assertion grep) + write 06.9-VERIFICATION.md
