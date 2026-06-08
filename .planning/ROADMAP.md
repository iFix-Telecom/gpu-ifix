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

### Phase 10: prod-deploy-ai-gateway

**Goal:** First production deploy of the ifix-ai-gateway (gateway + dashboard) — operator-managed `docker compose` stack at /opt/ai-gateway-prod/ on n8n-ia-vm (VM 101), public hostnames ai-gateway.converse-ai.app + ai-dashboard.converse-ai.app served via edge Traefik on vps-ifix-vm, new Postgres prod databases bd_ai_gateway_prod + bd_ai_dashboard_prod, new Sentry project ifix-ai-gateway-prod, develop→main fast-forward, cut release v1.0.0, per-tenant golden-path smoke for the 6 client apps, cascade-close Phase 02/03/04/05 live-UAT deferrals.
**Requirements:** INT-06, PRD-04 (partial), PRD-07
<!-- PRD-04 (partial) = RUNBOOK-DEPLOY.md only per D-18; full incident runbook deferred to Phase 11. See REQUIREMENTS.md §Traceability for the partial/full split. -->
**Depends on:** Phase 9
**Plans:** 6/6 plans complete

Plans:

**Wave 1**

- [x] 10-01-PLAN.md — Wave 0 reconciliation + compose/env scaffolds + capacity gate (Pitfall 1/2/4 fixes: network intra, new DB not new schema, edge certResolver letsencrypt)
- [x] 10-02-PLAN.md — Postgres prod databases + dashboard better-auth migrations (bootstrap-postgres.sh + migrate-dashboard.sh)
- [x] 10-03-PLAN.md — Edge Traefik file-provider entry + Cloudflare DNS records (ai-gateway-prod.yml + cf-dns-create.sh)

**Wave 2** *(blocked on Wave 1 completion)*

- [x] 10-04-PLAN.md — RUNBOOK-DEPLOY + REQUIREMENTS remap + ROADMAP Phase 11 placeholder (PRD-04 partial; D-16 split)

**Wave 3** *(blocked on Wave 2 completion)*

- [x] 10-05-PLAN.md — develop→main promotion + v1.0.0 tag + GHA build verify (cut-release.sh + 10-05-RELEASE-CHECKLIST.md)

**Wave 4** *(blocked on Wave 3 completion)*

- [x] 10-06-PLAN.md — HUMAN-UAT (autonomous: false; deploy + 8 smoke scenarios S1-S8 + S9 per-tenant + S10 rollback + S11 Sentry + 4 cascade-close commits)

### Phase 11: prod-hardening

**Goal:** Endurecer prod pós-Phase 10 — PRD-01 load test 30min sustained com SLO v1.0 D-04, PRD-02 chaos primary kill (Vast API DELETE), PRD-03 chaos OpenRouter DROP egress (iptables on n8n-ia-vm), PRD-04 RUNBOOK-INCIDENTS.md (4 classes D-11) + POSTMORTEM-TEMPLATE.md (Google SRE blameless 9-section), PRD-05 LGPD signoff doc-only deliverables, PRD-06 dashboard SSO hardening (better-auth twoFactor + rateLimit + allowlist + session 30min). Fold Phase 10 deferred items (D-18.1..D-18.4) e separação per-env keys (D-19).
**Requirements:** PRD-01, PRD-02, PRD-03, PRD-04 (full), PRD-05, PRD-06
**Depends on:** Phase 10
**Plans:** 7/10 plans complete (passed_partial; 11-06/07/08 live UATs deferred — see 11-VERIFICATION.md carry-forward tech debt)

Plans:

**Wave 1**

- [x] 11-01-PLAN.md — Wave 1: PRD-01 load-test scaffolding (audit-log-export.py + load-replay.py + load-replay-report-schema.json + .gitignore)
- [x] 11-02-PLAN.md — Wave 1: PRD-06 dashboard SSO hardening (twoFactor + rateLimit + allowlist + session expiresIn=30min + 2FA enroll/challenge UI + BLOCKING migrate)
- [x] 11-03-PLAN.md — Wave 1: PRD-05 LGPD signoff docs (LGPD-SIGNOFF-PROCESS.md + LGPD-SIGNOFF-LETTER-TEMPLATE.md)
- [x] 11-04-PLAN.md — Wave 1: Phase 10 fold D-18.1..D-18.3 (gatewayctl debug emit-error + key list + smoke-sensitive-failover race fix)
- [x] 11-05-PLAN.md — Wave 1: D-19 per-env upstream keys + D-18.4 GHA retrigger doc + scripts/dashboard/seed-admins.sh

**Wave 2** *(blocked on Wave 1 completion)*

- [ ] 11-06-PLAN.md — Wave 2: PRD-01 30-min sustained load-test live UAT (Vast 2×3090 primary UP; ~$1-3 spend)
- [ ] 11-07-PLAN.md — Wave 2: PRD-02 chaos primary kill live UAT (Vast API DELETE; ~$0.30 spend)
- [ ] 11-08-PLAN.md — Wave 2: PRD-03 chaos OpenRouter iptables DROP live UAT (sensitive 503 + normal fallthrough + cleanup)

**Wave 3** *(blocked on Wave 2 completion)*

- [x] 11-09-PLAN.md — Wave 3: PRD-04 RUNBOOK-INCIDENTS.md (4 classes D-11) + POSTMORTEM-TEMPLATE.md (Google SRE blameless 9-section)
- [x] 11-10-PLAN.md — Wave 3: HUMAN-UAT S1..S8 + 11-VERIFICATION.md final phase rollup + STATE/ROADMAP advance

---

### Phase 11.1: shrink-pod-remove-whisper (INSERTED)

**Goal:** Shrink the primary pod by removing the Speaches/faster-whisper-large-v3 tier-0 STT service (workflow batch volume insufficient to justify GPU residency — tier-1 OpenAI whisper-1 absorbs all STT via existing fallback chain). Bundles Phase 06.7 D-03 Infinity venv dead-code rollback. Refactors PRIMARY_GPU_SHAPE to 1×RTX 3090 primary (cap $0.30/h) + 1×RTX 4090 fallback (cap $0.40/h), unlocking -50% Vast cost, -5GB cold-start weight download, -3-5GB VRAM, and 1-GPU footprint vs current 2×3090.
**Requirements**: D-A1, D-A2, D-A3, D-A4, D-A5, D-A6, D-A7 (see 11.1-CONTEXT.md)
**Depends on:** Phase 11 (closed passed_partial; D-A7 confirms deferred UATs do NOT block)
**Plans:** 7/7 plans complete

Plans:

- [x] 11.1-01-PLAN.md — Wave 1: reconciler/lifecycle drop role=stt + Vast DefaultSearchFilters primary+fallback + config field rename + gatewayctl upstreamNameRole cleanup
- [x] 11.1-02-PLAN.md — Wave 2: migration 0028 DELETE upstreams.local-stt + model_aliases (whisper, local-stt); restorative Down; integration test fixtures simplified
- [x] 11.1-03-PLAN.md — Wave 3: pod Dockerfile drop speaches+Infinity venv stages + supervisord drop [program:speaches]+[program:infinity] + onstart.sh drop whisper tarball download
- [x] 11.1-04-PLAN.md — Wave 3: pod health-bridge drop probeSTT/:8001 + scripts/integration-smoke prune tier-0 STT references
- [x] 11.1-05-PLAN.md — Wave 4: pod .env.example + docker-compose.yml + READMEs + runbooks (FAILOVER/DEPLOY/PRIMARY-POD) updated; RUNBOOK-DEPLOY adds operator UPSTREAM_STT_URL removal task (T-11.1-02)
- [x] 11.1-06-PLAN.md — Wave 5: Vast 3090+4090 fleet survey checkpoint (T-11.1-04) + cold-start UAT on 1×3090 + tier-1 STT live curl prod gateway + memory note primary-gpu-shape-11.1-final superseding 06.8-final

### Phase 11.2: readd-whisper-local-gemini-fallback (INSERTED)

**Goal:** Restore tier-0 local Whisper STT on the primary pod (recover the "free when pod ON" property removed by Phase 11.1) AND swap the tier-1 STT fallback from OpenAI `whisper-1` ($0.36/h) to Google Gemini 2.5 Flash Lite (~$0.05/h audio tokens) — 7× cheaper tier-1 + zero marginal cost when local pod is ON. Requires new `gemini-stt` upstream + multipart→`files.upload`+`generateContent` director adapter (Gemini API differs from OpenAI Whisper schema). Re-adds Speaches venv + whisper weights bootstrap to pod image; restores `role=stt` to primary reconciler trio (back to 3-role llm/stt/tts); migration 0029 re-INSERTs `local-stt` upstream + `(whisper, local-stt)` alias + adds `gemini-stt` upstream + `(whisper, gemini-stt)` alias at tier-1; gateway breaker chain: local-stt (tier-0) → gemini-stt (tier-1) → openai-whisper (tier-1 safety net).
**Requirements**: D-B1, D-B2, D-B3, D-B4, D-B5′, D-B6′, D-B7, D-B8, D-B9, D-B10, D-B11, D-B12, D-B13, D-B14, UAT-CASCADE-LIVE, UAT-COLD-START (operator decisions in 11.2-CONTEXT.md)
**Depends on:** Phase 11.1 (closed passed_partial — provides Config split + DefaultSearchFilters + 2-role reconciler foundation to extend)
**Plans:** 8/8 plans complete

Plans:

- [x] 11.2-01-PLAN.md — Wave 0: Test stubs (RED) per VALIDATION.md + UAT script skeleton + BLOCKING operator gate D-B10 (Gemini + Groq keys in vps-ifix-vm .env)
- [x] 11.2-02-PLAN.md — Wave 1: Config restore PrimaryWhisperWeights* + PrimarySpeachesImage + add 6 new UPSTREAM_STT_{GEMINI,GROQ}_* envs; pod .env.example + docker-compose.yml restored
- [x] 11.2-03-PLAN.md — Wave 2: Migration 0029 (additive tier_priority col + UNIQUE swap + INSERT local-stt + gemini-stt prio=10 cooldown_s=120 + groq-whisper prio=15 + openai-whisper prio=20) + Loader.ResolveAllTier1 + types.TierPriority + reconciler/lifecycle restore role=stt; BLOCKING migrate up
- [x] 11.2-04-PLAN.md — Wave 3a (pod, parallel with 05): Restore Dockerfile speaches venv + supervisord [program:speaches] + onstart WHISPER guards + download-weights 3-way parallel; BLOCKING rebuild + push + promote PRIMARY_TEMPLATE_IMAGE
- [x] 11.2-05-PLAN.md — Wave 3b (pod, parallel with 04): Health-bridge restore probeSTT + /health/stt + UpstreamSTT const + TestProbeSTT (verbatim 39bec50^)
- [x] 11.2-06-PLAN.md — Wave 4: NEW gemini_stt_director.go (multipart→JSON + flatten resp + x-goog-api-key) + dispatcher multi-tier-1 loop (ResolveAllTier1) + resolver upstreamEnvVarMap +2 entries + openai_whisper_director canonicalAlias +groq-whisper (D-B8 REUSE) + main.go wireup 2 new proxies
- [x] 11.2-07-PLAN.md — Wave 5: RUNBOOK-OPS Gemini + Groq operator sections (key mint/rotate, D-B10 verbatim cmd, cooldown tuning, Pitfalls) + gateway/.env.example + PROJECT.md tech-stack
- [x] 11.2-08-PLAN.md — Wave 6: Implement 6 UAT scenarios in pod/smoke/uat-11.2.sh + BLOCKING operator live UAT (pod-ON local-stt, pod-OFF gemini, gemini-open→groq, gemini+groq-open→openai, sensitive pod-ON local, sensitive pod-OFF→503 RES-08) + 11.2-VERIFICATION.md rollup

### Phase 6.6.X: Pod cold-start hardening + env precedence audit (INSERTED, from 11.2-GAPS-DECISION)

**Goal:** Resolve the 2 Phase 11.2 carry-forward items that belong to Phase 6.6 primary-pod plane (not the cascade objective): (1) cold-start flakiness where `actual_status=created` permanent with no port mapping after image-load — blocks D-B13 cenários 1+5 pod-ON UAT; (2) dual-shape env precedence drift where `PRIMARY_NUM_GPUS=2` is set in vps-ifix-vm env but `PRIMARY_VAST_NUM_GPUS_PRIMARY=1` wins the Vast.ai search (1× RTX 3090 provisioned in 11.2 UAT) — conflicts with MEMORY `primary-gpu-shape-06.8-final` (2×3090 standing config). Pod weights pre-bake (whisper baked into image vs onstart download) may eliminate item 1 — investigate as part of this.
**Requirements:** PC-COLD-START-FIX, PC-ENV-PRECEDENCE
**Depends on:** Phase 11.2 (closed passed_partial — provides carry-forward decision document); Phase 06.8 (provides 2×3090 baseline shape)
**Carry-forward source:** .planning/phases/11.2-readd-whisper-local-gemini-fallback/11.2-GAPS-DECISION.md
**Plans:** TBD via /gsd-discuss-phase 6.6.X

### Phase 06.9: OpenRouter model-rewrite per-upstream — close Phase 03 SC-1 fallback chain (INSERTED, promoted from SEED-004)

**Goal:** Fix the gateway dispatcher → tier-1 fallback model-name rewriting gap so `POST /v1/chat/completions {"model":"qwen"}` against ai-gateway-dev (with primary pod down) returns a real OpenRouter Qwen 3.5 completion instead of the current HTTP 404 "Not Found" HTML. Wave 0 Gate A (Phase 03, 2026-04-20) defined `UPSTREAM_LLM_OPENROUTER_MODEL=qwen/qwen3.5-27b` as the env var operator must set; Plan 03-06 implementation never wired it. Bug masked through Phase 04-08 because integration tests use a fake upstream that accepts any model name + live UAT was always deferred. Also surfaced same-shape gaps for openai-whisper (`UPSTREAM_STT_OPENAI_MODEL`) and openai-embed (`UPSTREAM_EMBED_OPENAI_MODEL`) — verify and bundle. Reference fix-path = SEED-004 Option B (schema-driven `model_aliases` PK widened to `(alias, upstream_name)`). Per D-06: schema row is the default, env vars remain the per-instance escape hatch (env wins over schema when set) — both supported permanently.

**Requirements:** OR-FIX (model rewrite per-upstream), STT-OAI-FIX (whisper), EMBED-OAI-FIX (embed), SC1-CLOSE (Phase 03 SC-1 live UAT closes via this fix)
**Depends on:** Phase 03 (fallback chain code in tree); Phase 06.8 (live primary FSM available for breaker-OPEN testing)
**Blocks:** Phase 02 SC-5 step 7 chat E2E; Phase 03 SC-1 live UAT; Phase 05 SC-1 full overflow; Phase 07 dashboard accuracy (tier-1 cost rows currently mislabeled when model never rewrote)
**Mode:** sequential (not MVP)
**Plans:** 7/7 plans complete
**Cost:** zero Vast spend (testable via existing /opt/ai-gateway-dev/ + live OpenRouter direct); ~2-3h wall

Plans:

- [x] 06.9-01-PLAN.md — Wave 0: Migration 0026 PK widening (alias, upstream_name) + 3 tier-1 seed rows + sqlc regen + migrate_test.go list update + 03-WAVE0-GATES.md URL convention correction (/api/v1 → /api) + D-06 env-override-wins doc (env stays as documented fallback override; NOT deprecated)
- [x] 06.9-02-PLAN.md — Wave 1: Resolver refactor — Refresh consumes UpstreamName column; aliasKey semantics ROLE → NAME; D-06 env-override-wins precedence layer (env → schema → passthrough) inside Resolve via curated upstreamEnvVarMap; 4 base + 4 env-override + 1 renamed unit tests; Handler middleware godoc deprecation
- [x] 06.9-03-PLAN.md — Wave 2: 3 Directors (OpenRouter + Whisper-multipart + Embed-refactor) gain (resolver, upstreamName) and rewrite body.model via per-upstream lookup; main.go removes models.Handler wraps + threads resolver+name into each Build*Director; WhisperAbortGuard wraps the Whisper handler chain (WARNING-3: duplicate-model HTTP 400 abort wired in this phase, no escape hatch)
- [x] 06.9-04-PLAN.md — Wave 2: Config fail-fast on UPSTREAM_*_URL ending in /v1 + INFO log on active D-06 env overrides (NOT deprecation WARN) + gatewayctl breaker {list,force-open,force-close} + gatewayctl model-alias {list,set,get,delete} CLI subcommands (operator surface for live UAT); breaker FSM force-override seam patched on existing eval-tick cadence (≤1ms overhead) per WARNING-4 entry-gate
- [x] 06.9-05a-PLAN.md — Wave 3a (split): R8 freshSchema gate + body-capturing upstreamMock + newSelectiveMock + 3 model-rewrite integration tests (OR/Whisper/Embed) + 3 R6 Whisper edge-case tests (missing/duplicate/resolver-miss — all PASS, no SKIP per WARNING-3 wiring)
- [x] 06.9-05b-PLAN.md — Wave 3b (split, depends_on 05a): R4 local-tier byte-identical (chat + embed) + R13 historical-bug regression (selective-reject mock) + R1 breaker force-override TTL restoration + R3 migration 0026 Down-abort guard + Up→Down→Up round-trip + BLOCKER-1/D-06 end-to-end env-override-wins integration tests (3 cases) + PROJECT.md tier-1 stack confirmation + D-06 coequal-paths doc note
- [x] 06.9-06-PLAN.md — Wave 5 (autonomous: false): 06.9-HUMAN-UAT.md author with R2 hardened Pre-UAT preconditions + D-06 coequal-paths setup options (S1 schema CLI vs env var) + WARNING-6 dual breaker drivers (S1 force-open + docker-stop fallback; S2/S3 REQUIRE force-open) + operator-driven S1-S6 live UAT on dev stack (~$0.05 spend, no Vast/GPU) + cascade close Phase 02/03/05 VERIFICATION.md (3 small commits, WARNING-5 positive-assertion grep) + write 06.9-VERIFICATION.md
