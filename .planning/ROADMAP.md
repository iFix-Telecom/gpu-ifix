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
**Plans:** 10/10 plans complete

Plans:

**Wave 1**

- [x] 11-01-PLAN.md — Wave 1: PRD-01 load-test scaffolding (audit-log-export.py + load-replay.py + load-replay-report-schema.json + .gitignore)
- [x] 11-02-PLAN.md — Wave 1: PRD-06 dashboard SSO hardening (twoFactor + rateLimit + allowlist + session expiresIn=30min + 2FA enroll/challenge UI + BLOCKING migrate)
- [x] 11-03-PLAN.md — Wave 1: PRD-05 LGPD signoff docs (LGPD-SIGNOFF-PROCESS.md + LGPD-SIGNOFF-LETTER-TEMPLATE.md)
- [x] 11-04-PLAN.md — Wave 1: Phase 10 fold D-18.1..D-18.3 (gatewayctl debug emit-error + key list + smoke-sensitive-failover race fix)
- [x] 11-05-PLAN.md — Wave 1: D-19 per-env upstream keys + D-18.4 GHA retrigger doc + scripts/dashboard/seed-admins.sh

**Wave 2** *(blocked on Wave 1 completion)*

- [x] 11-06-PLAN.md — Wave 2: PRD-01 30-min sustained load-test live UAT (Vast 2×3090 primary UP; ~$1-3 spend)
- [x] 11-07-PLAN.md — Wave 2: PRD-02 chaos primary kill live UAT (Vast API DELETE; ~$0.30 spend)
- [x] 11-08-PLAN.md — Wave 2: PRD-03 chaos OpenRouter iptables DROP live UAT (sensitive 503 + normal fallthrough + cleanup)

**Wave 3** *(blocked on Wave 2 completion)*

- [x] 11-09-PLAN.md — Wave 3: PRD-04 RUNBOOK-INCIDENTS.md (4 classes D-11) + POSTMORTEM-TEMPLATE.md (Google SRE blameless 9-section)
- [x] 11-10-PLAN.md — Wave 3: HUMAN-UAT S1..S8 + 11-VERIFICATION.md final phase rollup + STATE/ROADMAP advance

### Phase 13: dashboard-user-management — Gestão de operadores (owner-only) + self-service change-password no dashboard ai-gateway

**Goal:** Operadores do dashboard ai-gateway conseguem trocar a própria senha (self-service)
e o owner consegue gerenciar operadores pelo browser, sem `seed-admins.sh` nem SQL manual.

**Scope:**

1. **Self-service change-password** — operador logado troca a própria senha
   (`authClient.changePassword`, exige senha atual; sem admin). Página em `/settings`.

2. **Gestão de operadores (owner-only):**
   - Criar/convidar operador `@ifixtelecom.com.br` (allowlist D-13 já existe).
   - Remover operador (+ revogar todas as sessões dele).
   - Resetar senha de operador (nova senha temp).
   - Resetar 2FA de operador (perdeu authenticator).
   - Tornar funcionais os botões placeholder de `settings/operadores/page.tsx`
     ("+ Provisionar operador", menu "···").

**Decisões locked (discuss 2026-06-14):** owner-only authz (1º operador = owner);
todas as 4 operações admin; entregar junto com a troca de senha.

**Security surface (rodar /gsd:secure-phase depois):**

- Roles (owner vs operator): admin plugin better-auth OU coluna `role` + migração CLI-canônica.
- Owner-gating em server-actions/route-handlers (NÃO só na UI) + reforço no middleware se aplicável.
- **Reset-2FA controlado:** o CR-01 do `auth.ts` bloqueia `/two-factor/enable` quando já habilitado
  (anti-rotação de credencial). O reset-2FA admin precisa de caminho controlado + audit, sem
  reabrir esse vetor (ex: clear 2FA → operador re-enrolla no próximo login via /2fa/enroll).

- Audit log de toda ação admin (quem, alvo, quando, ação).
- Revogar sessões ao remover/resetar.
- Não expor hashes/secrets/backup-codes na UI (regra de privacidade já no operadores page).

**Requirements**: UM-01, UM-02, UM-03, UM-04, UM-05, UM-06, UM-07, UM-08, UM-09, UM-10
**Depends on:** Phase 11 (auth/2FA base), Phase 12
**Plans:** 5 plans

Plans:

**Wave 1**

- [ ] 13-01-PLAN.md — Wave 0: RED test stubs UM-01..UM-10 (reuse auth.test.ts memoryAdapter) + shadcn dialog/dropdown-menu/alert-dialog + nodemailer [ASSUMED] legitimacy gate (autonomous: false)

**Wave 2** *(blocked on Wave 1)*

- [ ] 13-02-PLAN.md — admin plugin (adminRoles:["owner"]) + Brevo nodemailer sendResetPassword + CLI-canonical schema regen + admin_audit_log (schema-custom) + db/drizzle wiring + seed-owner; [BLOCKING] staging-first drizzle-kit push → IMMEDIATE owner-seed (autonomous: false)

**Wave 3** *(blocked on Wave 2)*

- [ ] 13-03-PLAN.md — 4 owner-gated Server Actions (invite/remove/reset-pw/reset-2FA CR-01-safe) + audit.ts writeAuditLog (D-03/D-08/D-09)
- [ ] 13-04-PLAN.md — self-service change-password /settings (UM-01, not audited) + /reset-password/[token] set-password landing

**Wave 4** *(blocked on Wave 3)*

- [ ] 13-05-PLAN.md — operadores/page.tsx real role (D-02) + owner-gate + + Provisionar dialog + ··· dropdown-menu + destructive alert-dialogs wired to server actions (UM-10)

### Phase 14: vram-adaptive-stt — gateway auto-decides pod STT via pod-reported whisper device (SEED-019 parts 2+3)

**Goal:** Remove the manual `PRIMARY_POD_SERVE_STT` env flag (commit 4021901, currently `false` in prod for the 1×3090 shape) and make the gateway decide STT-from-pod automatically and shape-agnostically. **Part 2 (pod):** `onstart.sh` detects total VRAM via `nvidia-smi` and sets the whisper device through a container env-file (removing the build-baked `WHISPER__INFERENCE_DEVICE="cpu"` pin at `supervisord.conf:46`); on VRAM-capable shapes (≥~30GB: 2×3090, 5090) whisper loads on GPU with `device_index` pinned OFF the Qwen GPU to dodge CUDA OOM, on 24GB (1×3090) whisper stays off-GPU/disabled; the health-bridge `aggregateResponse` (`pod/health-bridge/handlers.go:58`, :9100) surfaces a `whisper_device` field. **Part 3 (gateway):** add `WhisperDevice` to `primaryPodURLs` (`lifecycle.go:112`), probe `:9100/health/ready` at Ready to capture it, and swap the flag-gate to a per-lifecycle device boolean at the 3 reconciler override sites (`reconciler.go:448/:875/:1631`); delete `Config.PrimaryPodServeSTT`. Net: pod that runs whisper on GPU → gateway overrides STT to pod; CPU/no-whisper pod → STT falls to tier-1 `gemini-stt` automatically. **Excludes** SEED-019 part 1 (N-shape cascade 2×3090→5090→3090) — separate phase. Research: `.planning/seeds/SEED-019-IMPL-SURFACES.md` + `SEED-019-multi-pod-shape-cascade-vram-adaptive-stt.md`.
**Requirements**: STT-AUTO, STT-FAILSAFE, STT-PROBE, POD-VRAM, FLAG-REMOVE, STT-SHAPE-3090, STT-SHAPE-GPU, STT-MIGRATE (derived in plan-phase; CHANNEL: D-14-01 — health-bridge :9100 NOT viable in PROD, replaced by an onstart static device-report responder on a newly-forwarded :9100/whisper_device — see 14-01/14-02 PLANs)
**Depends on:** Phase 11.2 (local whisper + gemini-stt tier-1 cascade); flag scaffolding commit 4021901 (the 3 gated override sites to convert)
**Plans:** 3/3 plans complete

Plans:

- [x] 14-01-PLAN.md — Wave 1: gateway device-gate (Wave 0 test rework RED→GREEN) — WhisperDevice on primaryPodURLs, gate 3 stt override sites on device==cuda, delete Config.PrimaryPodServeSTT, wire main.go DeviceReport fetch
- [x] 14-02-PLAN.md — Wave 2: pod VRAM-adaptive whisper — onstart.go nvidia-smi device export + :9100 /whisper_device responder, drop supervisord.conf:46 cpu pin (keep HF_HUB_CACHE), forward -p 9100:9100
- [x] 14-03-PLAN.md — Wave 3 (autonomous:false): rebuild+push+promote image + uat-14.sh + 2 live Vast UATs (1×3090→gemini; ≥30GB→pod-GPU <5s no OOM) + remove prod PRIMARY_POD_SERVE_STT + 14-VERIFICATION rollup

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
**Plans:** 9 plans

Plans:

- [x] 06.6.X-01-PLAN.md — Wave 1: code-side shape var map (grep + alias mapping quote)
- [x] 06.6.X-02-PLAN.md — Wave 1: runtime env capture from vps-ifix-vm
- [x] 06.6.X-03-PLAN.md — Wave 1: docs-side var values (runbooks + .env.examples + MEMORY)
- [x] 06.6.X-04-PLAN.md — Wave 2: iter-1 spike via gateway live-tick + 4-signal parallel capture (autonomous:false)
- [x] 06.6.X-05-PLAN.md — Wave 3: iter-1 analysis (suspect ranking by evidence + iter-2 go/no-go)
- [x] 06.6.X-06-PLAN.md — Wave 4: iter-2 SKIP-rationale (top-1 already D-15 confirmed; $0 spend)
- [x] 06.6.X-07-PLAN.md — Wave 5: RESEARCH-ENV-PRECEDENCE.md synthesis from 3 audit inputs
- [x] 06.6.X-08-PLAN.md — Wave 5: RESEARCH-COLD-START.md synthesis from iter-1 evidence
- [x] 06.6.X-09-PLAN.md — Wave 6: phase VERIFICATION rollup — verdict=passed, spend $0.040/$1.20

### Phase 6.6.Y: Pod cold-start fix + env precedence canonical migration (INSERTED, from 06.6.X RESEARCH hand-off)

**Goal:** Implement the fixes diagnosed by Phase 06.6.X: (1) **Cold-start Option A+B bundle** — offer-side RFC1918/port-count filter (exclude offers advertising private `public_ipaddr` 10/8, 172.16/12, 192.168/16; code points `gateway/internal/emerg/vast/types.go DefaultSearchFilter` + `gateway/internal/primary/reconciler.go:769` caller) + reconciler health-gate with fail-fast (bounded wait on endpoint-reachability instead of silent indefinite `waitForReadyOrDestroy` loop — iter-1 evidence: 18min silent hang on host 51096 Brazil with ssh_host=null); (2) **Env-precedence canonical migration with hard fail-fast** — canonical = `PRIMARY_VAST_{NUM_GPUS,GPU_NAME}_{PRIMARY,FALLBACK}`; DELETE legacy `PRIMARY_NUM_GPUS`/`PRIMARY_GPU_NAME` from all surfaces (compose default poison, .env.prod.example, MEMORY, runbook); gateway boot FAILS if legacy var present (hard fail-fast — soft warn rejected per RESEARCH); (3) **Boot-time resolved-shape log dump** — gateway emits resolved `cfg.PrimaryNumGPUs` + allowlist at startup (fix-option-agnostic, same wave); (4) **Allowlist refresh** — verify machines 43803/55158 still list 2×3090 offers; correct or document. Optional parallel item (ordered AFTER A+B so next UAT reaches weights stage): Option C whisper pre-bake evaluation. Live UAT closes with pod-ON cold-start ≤ budget on 2×3090 shape.
**Requirements:** PC-COLD-START-FIX, PC-ENV-PRECEDENCE
**Depends on:** Phase 06.6.X (closed passed — provides 06.6.X-RESEARCH-COLD-START.md + 06.6.X-RESEARCH-ENV-PRECEDENCE.md as canonical inputs)
**Research inputs:** .planning/phases/06.6.X-pod-cold-start-env-precedence-audit/06.6.X-RESEARCH-COLD-START.md; .planning/phases/06.6.X-pod-cold-start-env-precedence-audit/06.6.X-RESEARCH-ENV-PRECEDENCE.md
**Plans:** 7/7 plans complete
**Completed:** 2026-06-11 (VERIFICATION passed 2/2 requirements)

Plans:

- [x] 6.6.Y-01-PLAN.md — Wave 0: D-01 disambiguation spike (operator, ~$0.30) — 2nd-host port-bind + offer-side public_ipaddr availability → freeze Option A site
- [x] 6.6.Y-02-PLAN.md — Wave 1: config.go hard fail-fast on legacy primary vars + delete legacy fields/reads + 2 cold-start readers + main.go boot-shape log
- [x] 6.6.Y-03-PLAN.md — Wave 2: cold-start Option A (RFC1918 offer reject) + Option B (120s port-bind fail-fast + per-strike log) in vast/types.go + reconciler.go
- [x] 6.6.Y-04-PLAN.md — Wave 2: canonical migration of docs/env surfaces (compose, .env.prod.example, runbook, MEMORY) + live allowlist refresh
- [x] 6.6.Y-05-PLAN.md — Wave 3: D-04 SAFETY-CRITICAL operator stack.env migration BEFORE fail-fast deploy (autonomous: false)
- [x] 6.6.Y-06-PLAN.md — Wave 4: Option C whisper 3-way onstart download (D-03) + fail-fast image build/deploy with GHCR PAT-retag fallback (autonomous: false)
- [x] 6.6.Y-07-PLAN.md — Wave 5: live UAT (2×3090 cold-start ≤ budget, suspects #1/#3 retired, ≤ $2.00) + VERIFICATION (autonomous: false)

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

### Phase 12: gateway-resilience-remediation (INSERTED, from 11-06/11-07 live UAT findings)

**Goal:** Fix the three resilience gaps proven live in the Phase 11 PRD-01/PRD-02 UATs (2026-06-12) so the prod gateway survives a primary-pod death autonomously and its health surfaces tell the truth. (1) SEED-011 — primary reconciler steady-state Ready loop is blind to instance death: FSM stayed `ready` through the full 90s window after a real Vast DELETE (and earlier after a billing-stop with `actual_status in {exited,stopped}`); the startup recover path already classifies both correctly — port that classification into the Ready-state reconcile tick (404 3-strike from 01e7558 AND exited/stopped detection), advance Ready → Draining → Asleep, BestEffortDestroy, and emit a distinct critical alert for billing-stop (`account lacks credit`). (2) SEED-012 — prober resolves tier-0 via loader.All() which ignores tier0Override, so local-* breakers flap open forever in prod and /v1/health/upstreams returns 503 with a healthy pod; make the probe tick resolve tier-0 through the same Resolve(role,0) path the dispatcher uses. (3) NO tier-1 failover on dead tier-0: 11-07 chaos produced 100× HTTP 502 upstream_unreachable in T+0..60s with OpenRouter healthy and closed — dispatcher must fall through to tier-1 when the tier-0 dial fails (connection-refused class), not only when the breaker is open. Stretch (capacity, may split): PRD-01 saturation baseline (chat p95 21.7s @ concurrency 50 on 1×5090) feeds a queue-depth/concurrency-cap or shape decision — document-only acceptance is OK for this item.

**Requirements:** RES-11 (FSM death detection on Ready tick), RES-12 (prober/dispatcher tier-0 resolution parity), RES-13 (dial-failure tier-1 fallthrough, zero-502 budget under chaos), CAP-01 (saturation baseline decision doc)
**Depends on:** Phase 11 (11-06/11-07 evidence + seeds SEED-011/SEED-012); Phase 06.9 (tier-1 model rewrite working — failover target must actually serve)
**Blocks:** continuous prod primary operation (24/7 tier-0 unsafe until RES-11+RES-13 land)
**Mode:** sequential (not MVP)
**Plans:** 5/5 plans complete
**Cost:** chaos re-validation UAT ~$0.50-1.00 Vast spend (re-run 11-07 recipe expecting zero-502); dev-stack testing otherwise

Plans:
**Wave 1**

- [x] 12-01-PLAN.md — RES-12 prober/health tier-0 parity (Resolve(role,0)) + breaker force-close mechanism (D-13) + obs counters

**Wave 2** *(blocked on Wave 1 completion)*

- [x] 12-02-PLAN.md — RES-11 Ready-tick death detection + D-05 trackedID repair + D-04 force-open + D-03 distinct billing-stop alert
- [x] 12-03-PLAN.md — RES-13 connection-class dial-failure fallthrough to tier-1 cascade (sensitive 503 preserved)

**Wave 3** *(blocked on Wave 2 completion)*

- [x] 12-04-PLAN.md — Dev chaos UAT (HUMAN-UAT): cheap pod kill validates all 3 fixes together (D-16 dev-first)

**Wave 4** *(blocked on Wave 3 completion)*

- [x] 12-05-PLAN.md — Prod chaos gate (HUMAN-UAT, zero-502 D-18) + CAP-01 saturation decision doc (D-19)

### Phase 13: dashboard-economia-e-historico (from /gsd:explore 2026-06-26)

**Goal:** Dar ao operador o número que mais importa — **se a GPU própria economiza de verdade vs OpenRouter** — e tornar o histórico navegável. (1) OBS-09 — destravar o painel de Economia hoje deferred: o gateway ganha uma soma `cost_local_phantom_brl` **gateway-wide** (todos tenants, query sem filtro de tenant — o blocker atual) por período, cruzada com o custo real Vast (`primary_lifecycles.total_cost_brl` para lifecycles fechados + accrual `accepted_dph × horas-desde-started` para o lifecycle aberto). O dashboard exibe **3 números lado a lado** — líquido R$ (phantom − Vast), recorte janela pod-up (só horas com pod UP; alinhamento natural pois phantom só é gravado quando servido local), e multiplicador ROI (phantom evitado por R$1 de GPU) — mais um **gráfico de economia como série temporal real** (eixo X = tempo), que de quebra resolve a queixa do gráfico atual (latency chart usa eixo = rota, não tempo). Assume preço phantom confiável (decisão da exploração: daily timer OpenRouter+forex já popula). (2) OBS-10 — `/incidents` (audit log) ganha filtro de data + busca + total count (hoje só pager limit/offset, sem range nem COUNT no handler).

**Requirements:** OBS-09 (painel economia phantom vs Vast + série temporal), OBS-10 (filtro/busca/count no histórico de incidentes)
**Depends on:** Phase 7 (dashboard base + `/admin/usage` billing_events), Phase 12 (Vast cost em `primary_lifecycles` com `total_cost_brl`/`accepted_dph` confiável)
**Mode:** sequential (não MVP)
**Plans:** TBD (rodar /gsd:plan-phase)
**Cost:** dev-only, sem spend Vast/GPU (lê dados existentes)

**Decisões da exploração (2026-06-26):**
- Fórmula economia = `soma(phantom) − custo_real_Vast` por período
- Confia no preço phantom — NÃO validar antes (já corrigido via daily timer)
- Gaps NÃO incluídos nesta fase (capturados em note/seed): metering `audio_seconds`/`embeds_count`=0 não gravam; latency chart vira série temporal (seed separado); Tier 3 GPU/RAM/CPU (HANDOFF-tier3-gpu-metrics.md)
