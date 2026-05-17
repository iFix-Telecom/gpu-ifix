# Roadmap: ifix-ai-gateway

**Created:** 2026-04-17
**Core Value:** Nenhuma aplicação da Ifix sente quando a GPU cai. Failover invisível.
**Granularity:** fine (10 phases)
**Total v1 Requirements:** 70 (all mapped)

## Phases

- [x] **Phase 1: GPU Pod Image & Smoke-Test** — Build and validate the pre-baked inference pod image (llama.cpp + Speaches + Infinity + dcgm-exporter) with VRAM measured under load
- [x] **Phase 2: Gateway Core + Multi-tenant Auth** — Go gateway serves OpenAI-compatible endpoints with API key auth, data-class tagging, and idempotency — single upstream, no failover yet
- [x] **Phase 3: Resilience & Fallback Chain** — Circuit breakers + retries + local→OpenRouter→OpenAI fallback chain with explicit streaming policy and LGPD-safe routing for sensitive tenants
- [x] **Phase 4: Multi-tenant Quotas, Billing & Schedule Routing** — Rate limiting, daily/monthly quotas, token counting, cost attribution per tenant, 24/7 vs peak schedules (COMPLETE 2026-04-21 — 3 SC LIVE UAT deferred pending ai-gateway-dev stack deploy; 04-VERIFICATION.md status=human_needed; 04-REVIEW-FIX.md closes 2 BLOCKER + 4 HIGH + 6 MEDIUM)
- [x] **Phase 5: Load Shedding (Saturation-aware Routing)** — Composite saturation signal (inflight + P95 + VRAM) with hysteresis overflows traffic to OpenRouter before local fails
- [ ] **Phase 6: Emergency-Pod Template Refactor (Vast.ai + llama-server binary)** — Replace custom GHCR image with Vast.ai Ubuntu+CUDA template + pinned llama-server binary (SEED-001); cuts cold-start ~2-4min, fixes runtype=ssh CMD-ignore bug. Foundation for emergency-pod auto-provisioning.
- [ ] **Phase 6.5: Auto-provisioning Emergency Pod (Vast.ai)** — Leader-elected state machine spins up emergency Vast.ai pod on sustained failure and tears it down after primary recovers (10/11 plans done sob numeração antiga Phase 6; aguarda 06-11 HUMAN-UAT desbloqueado por Phase 6 template refactor)
- [x] **Phase 7: Observability — Dashboard & Alerting** — Next.js dashboard, Prometheus metrics, WhatsApp/email alerts with severity tiers, Sentry, audit log (9/9 plans executed; code review 3 BLOCKER + 11 WARNING resolved; 07-VERIFICATION.md status=human_needed — 5 live-UAT items deferred pending dev stack deploy)
- [x] **Phase 8: Client Integration — ConverseAI + Chat Ifix** — First two integrations (low risk, well-known apps) switch base_url to gateway (4/4 plans executed; code review 2 BLOCKER + 7 WARNING resolved, re-review clean; 08-VERIFICATION.md status=human_needed — SC1-SC4 live-UAT deferred pending dev stack deploy)
- [x] **Phase 9: Client Integration — Sensitive Tenants (Telefonia, Cobranças, Campanhas, voice-api)** — Integrate `data_class: sensitive` apps behind LGPD review and secondary-tier policies (4/4 plans executed; code review 1 BLOCKER + 6 WARNING resolved, re-review clean; 09-VERIFICATION.md status=human_needed — SC1-SC4 + LGPD legal sign-off deferred)
- [ ] **Phase 10: Production Hardening & GA** — Load tests, chaos drills, runbook, SSO/Better Auth, DNS/TLS, LGPD sign-off

---

## Phase Details

### Phase 1: GPU Pod Image & Smoke-Test

**Goal:** Ship a reproducible pre-baked pod image that boots the 3 inference servers on a Vast.ai 4090 in ≤5 min with ≥3 GB VRAM headroom under realistic load.
**Depends on:** Nothing (foundation)
**Requirements:** POD-01, POD-02, POD-03, POD-04, POD-05, POD-06, POD-07
**Success Criteria** (what must be TRUE):

  1. Operator can `docker run` the image on a fresh Vast.ai 4090 instance and see all three OpenAI-compat endpoints (`:8000` LLM, `:8001` STT, `:8002` embed) return healthy within 5 minutes of pod creation.
  2. Operator can hit per-model `/health` endpoints on the pod's health-bridge (port 9100) and get true latency-based health (not just "container running").
  3. Operator can pull live VRAM totals/free/per-process metrics from `dcgm-exporter` (port 9400) during inference.
  4. Operator can run the documented smoke-load (2 concurrent 8k-token chats + 1 long Whisper) and observe sustained VRAM usage ≤21 GB with `max_model_len=16384` enforced.
  5. Operator can issue a tool-call request to the LLM server and receive a well-formed tool call (Qwen 3.5 27B patched template validated).

**Plans:** 9 plans
Plans:

- [x] 01-01-PLAN.md — Repo scaffolding + Go monorepo + Ifix conventions (POD-01)
- [x] 01-02-PLAN.md — Qwen 3.5 27B tool-calling Jinja template (community patched, pinned by SHA-256) (POD-05)
- [x] 01-03-PLAN.md — Pod Dockerfile + docker-compose (5 services, GPU share, healthchecks) (POD-01, POD-02, POD-04, POD-06)
- [x] 01-04-PLAN.md — Health-bridge Go service (port 9100, 10s probe loop, per-upstream state) (POD-03)
- [x] 01-05-PLAN.md — Vast.ai onstart.sh — MinIO weight download + SHA-256 + docker compose up (POD-02)
- [x] 01-06-PLAN.md — Smoke-test (asyncio benchmark + report schema + D-19 gate enforcement) (POD-06, POD-07)
- [x] 01-07-PLAN.md — GitHub Actions build-pod.yml — build + push ghcr.io/ifixtelecom/ifix-ai-pod (POD-01)
- [x] 01-08-PLAN.md — GitHub Actions smoke.yml — Vast.ai pod lifecycle + D-19 gate enforcement (POD-07)
- [x] 01-09-PLAN.md — Operator runbook — MinIO weight upload + pod operation + baseline archival (POD-02)

**Research hint:** yes (Qwen tool-calling template patch, Q4_K_M vs Q5_K_M tradeoff, empirical VRAM ceiling)
**UI hint:** no

### Phase 2: Gateway Core + Multi-tenant Auth

**Goal:** Apps can point their OpenAI SDKs at `gateway.ifix.com.br` with a per-tenant API key and get authenticated, auditable, streaming-capable responses from the primary pod.
**Depends on:** Phase 1 (pod endpoints to proxy to)
**Requirements:** GW-01, GW-02, GW-03, GW-04, GW-05, GW-06, GW-07, GW-08, GW-09, GW-10, TEN-01, TEN-02, TEN-08, TEN-09
**Success Criteria** (what must be TRUE):

  1. A client using the OpenAI SDK with `base_url=https://gateway.ifix.com.br/v1` + a valid API key receives real Qwen completions (including SSE streaming with per-chunk flush) and real Whisper/embedding responses.
  2. An unauthenticated request returns a 401 in the OpenAI error envelope; a rate-limit or quota rejection would return 429 (wiring in place, values exercised later).
  3. Every request is traceable end-to-end: a unique `X-Request-ID` header is returned, logs carry the same ID, and a row lands in `audit_log` Postgres table.
  4. An API key carries a `data_class` (`normal` | `sensitive`) that downstream policies can read from the authenticated request context.
  5. The gateway deploys via the standard Ifix flow (GitHub → Actions → Portainer webhook) on the dedicated 4 vCPU VPS and model aliases (client sends `model: "qwen"`) resolve to the current primary model.

**Plans:** 8/9 plans executed (02-09 optional — deferred to Phase 7/10 per Codex scope-creep ruling)
**Research hint:** no (Go + chi + pgx + go-redis is well-trodden; schema design is standard)
**UI hint:** no

### Phase 3: Resilience & Fallback Chain

**Goal:** When any local model dies or degrades, requests continue to succeed via OpenRouter/OpenAI without scrambling streams, duplicating tool calls, or leaking sensitive data.
**Depends on:** Phase 2 (gateway + auth + data_class field)
**Requirements:** RES-01, RES-02, RES-03, RES-04, RES-05, RES-06, RES-07, RES-08
**Success Criteria** (what must be TRUE):

  1. An operator can kill the local LLM process and, within ≤10s, new chat requests succeed via OpenRouter with no client-visible error (for non-streaming) or a documented fail-fast 503 (for streaming already in flight).
  2. An operator can hit `/v1/health/upstreams` and see live per-upstream state (closed/half-open/open) for all six upstreams; breaker trips and health-probe results are visible there and in metrics.
  3. A request from a tenant flagged `data_class: sensitive` is never proxied to OpenAI/OpenRouter during failover — it either queues (short retry) or returns a controlled error; an audit row records the decision.
  4. A tool-calling request that arrives mid-failover never executes the same tool twice: the gateway returns 502 after a tool call was emitted and relies on the agent layer to retry with a separate idempotency key.
  5. Context window is normalized: a 20k-token prompt is rejected with the same behavior whether primary or OpenRouter is serving (both capped at 16k).

**Plans:** 8 plans
Plans:

- [x] 03-01-PLAN.md — Wave 0 scaffolding: 3 Go deps + sentinel errors + probe.wav fixture + operator gates (Fireworks slug + /tokenize)
- [x] 03-02-PLAN.md — DB foundation: 3 migrations (upstreams table + seed + NOTIFY trigger) + sqlc queries + config extension (RES-01,03,04,07)
- [x] 03-03-PLAN.md — breaker package: gobreaker v2 wrapper + Redis mirror + Pub/Sub subscriber + 9 obs metrics (RES-01,04)
- [x] 03-04-PLAN.md — upstreams loader + pgxlisten hot-reload (RES-03,04)
- [x] 03-05-PLAN.md — probe goroutine (zero-value errgroup) + refactored /v1/health/upstreams handler (RES-04,01)
- [x] 03-06-PLAN.md — proxy refactor: tokencount + directors + dispatcher + sensitive retry + tool-call interceptor + streaming + main.go wiring (RES-01..03,05..08)
- [x] 03-07-PLAN.md — gatewayctl upstreams CLI + 5 integration tests (state machine, fallback, sensitive block, hot reload, tool-call partial) (RES-01,03,04,06,08)
- [x] 03-08-PLAN.md — HUMAN-UAT: SC-1 live failover + Sentry breadcrumbs + RUNBOOK-FAILOVER.md (RES-01,03,04,06)

**Research hint:** yes (OpenRouter upstream provider for Qwen 3.5 27B tool-calling behavior, exact streaming policy SSE event shape)
**UI hint:** no

### Phase 4: Multi-tenant Quotas, Billing & Schedule Routing

**Goal:** Each tenant is rate-limited, quota-bounded, and has its own cost report; apps in `peak` mode route to OpenRouter outside business hours without per-request code changes.
**Depends on:** Phase 2 (auth + schema), Phase 3 (so overflow direction is known)
**Requirements:** TEN-03, TEN-04, TEN-05, TEN-06, TEN-07
**Success Criteria** (what must be TRUE):

  1. A tenant exceeding its RPS/RPM limit receives 429 with `Retry-After`; a tenant exceeding daily or monthly quota receives a quota-specific error; both are enforced atomically under concurrent load.
  2. Every completed request leaves an append-only `billing_events` row with tokens (local tokenizer count), provider, and BRL cost computed.
  3. An admin can call a reporting endpoint and retrieve `{tenant, tokens, minutes, embeds, cost_local, cost_external, cost_total}` for any date range.
  4. A tenant configured `mode=peak` has requests routed to OpenRouter between 22:00 and 08:00 local time automatically; a tenant in `mode=24/7` stays on local primary at all hours.
  5. Load-test of 1000 concurrent rate-limit checks against Redis shows zero over-use (Lua-atomic).

**Plans:** 9 plans
Plans:

- [x] 04-01-PLAN.md — Wave 0 scaffolding: 5 sentinel-error files + tzdata import + config env vars + pkg/openai constants + operator gates (A1/A2 pricing) (TEN-03..07)
- [x] 04-02-PLAN.md — DB foundation: migrations 0010..0014 (billing_events partitioned, usage_counters evolve, prices+fx, tenants ALTER + chk_sensitive_no_peak, admin_keys) + 6 sqlc query files (TEN-03..07)
- [x] 04-03-PLAN.md — Migration 0015 seed prices + fx + per-tenant quota overrides (operator-gated values from 04-WAVE0-GATES.md) (TEN-04, TEN-05, TEN-06)
- [x] 04-04-PLAN.md — Foundation A: quota Lua bucket + counters + tenants loader + listen + schedule policy/window + 5 obs collectors (TEN-03, TEN-04, TEN-05)
- [x] 04-05-PLAN.md — Foundation B: billing prices/fx loaders + listen + accountant + flusher + cost helper + admin middleware/usage + dual-shape SSE usage interceptor + 4 obs collectors (TEN-06, TEN-07)
- [x] 04-06-PLAN.md — Middleware integration: rate-limit + quota + schedule + metrics middlewares; main.go wires loaders + listeners + flusher + boot-time invariant + per-route WriteTimeout + /admin sub-router; dispatcher upstream-override + director stream_options injection (TEN-03..06)
- [x] 04-07-PLAN.md — gatewayctl subcommands: tenant set-mode/set-quota + prices set/list/set-fx + billing reconcile + usage report + admin-key create/revoke/list (TEN-04..07)
- [x] 04-08-PLAN.md — Integration tests (testcontainers): SC-1/3/5 + sensitive+peak triple-defense + hot-reload (prices, fx, tenants) + reconcile drift + middleware chain replay semantics (TEN-03..07)
- [x] 04-09-PLAN.md — HUMAN-UAT: SC-1 LIVE rate-limit headers + SC-4 LIVE peak routing + Sentry breadcrumbs + RUNBOOK-QUOTAS-BILLING.md (TEN-03..07)

**Research hint:** no (Redis Lua rate-limit is standard; only open question is Postgres DO headroom for billing writes — covered by batching)
**UI hint:** no

### Phase 5: Load Shedding (Saturation-aware Routing)

**Goal:** The gateway detects real saturation before it becomes failure and sheds overflow to OpenRouter while the primary recovers, preserving low-latency service for remaining tenants.
**Depends on:** Phase 3 (fallback chain exists), Phase 4 (inflight counters per tenant)
**Requirements:** LSH-01, LSH-02, LSH-03, LSH-04, LSH-05
**Success Criteria** (what must be TRUE):

  1. Under a burst where inflight on local LLM exceeds the configured slot count, excess requests are routed to OpenRouter automatically; below threshold, traffic returns to local.
  2. Under sustained P95 latency spike or VRAM > 21 GB, shedding activates within 30s; no flapping occurs during 60s of oscillating load (hysteresis verified).
  3. Thresholds (inflight_max, P95_ms, VRAM_bytes, hysteresis_seconds) can be changed by updating rows in Postgres and take effect within 2s without restarting the gateway.
  4. During shedding, one tenant's burst does not starve other tenants — per-tenant inflight quotas keep smaller apps responsive while overflow from the noisy tenant hits OpenRouter.

**Plans:** 8 plans
Plans:

- [x] 05-01-PLAN.md — Wave 0 scaffolding: promote expfmt direct + vegeta dep + sentinel errors (shed, dcgm) + 14 obs collectors + auditctx shed helpers + 3 operator gates (LSH-01..05)
- [x] 05-02-PLAN.md — DB foundation: migrations 0016/0017/(0018 conditional) + TenantConfig/CircuitConfig struct extensions + sqlc UpdateTenantShedLimits (LSH-02, LSH-04, LSH-05)
- [x] 05-03-PLAN.md — shed package core: FSM 4-state (atomic.Int32 + CAS) + LatencyRing + InflightRegistry + Set aggregator + unit tests (LSH-01, LSH-02, LSH-03)
- [x] 05-04-PLAN.md — dcgm.Scraper: HTTP poller with expfmt parser + 3-strikes fail-open + sanity bounds (LSH-02)
- [x] 05-05-PLAN.md — redisx/shed.go + mirror publish + Subscribe + ReconcileLoop (Pitfall 3 forward-compat Fase 6) + RunTicker with shed-force override (LSH-03, LSH-04)
- [x] 05-06-PLAN.md — shed.Middleware + dispatcher precedence (tier-1 unavailable 503 with hardcoded Retry-After:30) + main.go wiring of 4 goroutines (LSH-01..05)
- [x] 05-07-PLAN.md — gatewayctl shed-state + shed-force + thresholds set (JSONB merge) + tenant set-shed-limits (LSH-04, LSH-05)
- [x] 05-08-PLAN.md — integration tests: SC-1 burst + SC-2 hysteresis (opt-in slow) + SC-3 hot-reload + SC-4 anti-starvation + 5 edge cases + mirror convergence (LSH-01..05)

**Research hint:** yes (real saturation thresholds must be tuned from Phase 1 baseline data; histeresis window needs empirical validation)
**UI hint:** no

### Phase 6: Emergency-Pod Template Refactor (Vast.ai + llama-server binary)

**Goal:** Trocar custom image `ghcr.io/ifixtelecom/ifix-ai-pod` por template Vast.ai Ubuntu+CUDA pré-cacheado + llama-server binário pinned (sha256 verificado, fallback MinIO). Cold-start cai de ~7-12min pra ~5min, fix do bug runtype=ssh ignorando CMD, iteração dev sem rebuild image (10x mais rápida).
**Depends on:** Phase 1 (pod image arch + MinIO weights pipeline), Phase 6.5 (lifecycle.go + reconciler — refactor TROCA imagem usada pelo reconciler já implementado)
**Requirements:** Derived from SEED-001 (refactor scope — não cria reqs novos PRV-XX; reutiliza PRV-01..10 com source diferente)
**Success Criteria** (what must be TRUE):

  1. Operator pode disparar `gatewayctl emerg force-provision` e ver pod Vast.ai bootando a partir do template oficial (e.g., `nvidia/cuda:12.X-runtime-ubuntu22.04`) com llama-server baixado em runtime do GitHub release pinned (sha256 conferido) + fallback MinIO mirror se GitHub falhar.
  2. Cold-start total (search → create → llama load → /health green) mede ≤6min em 90% dos hosts Vast 4090 (vs ~7-12min do custom image baseline), evidenciado em log lifecycle.
  3. Runtype Vast.ai NÃO usa mais `"ssh"` (bug STATE.md:84 — ignora CMD). Estratégia escolhida no CONTEXT.md (args + onstart inline vs script remoto) funciona end-to-end.
  4. Mudança no `pod/onstart.sh` (qualquer linha) chega no próximo pod emergency sem rebuild de imagem GHCR — só commit + redeploy gateway.
  5. Fallback ao custom image baked: mantido como rollback em `EMERGENCY_POD_IMAGE_TAG=fallback-baked` env var (operator pode reverter sem code change) OU descontinuado com documentação. Decisão no CONTEXT.md.

**Plans:** 7 plans (5 waves, PR1/PR2 split per CONTEXT.md D-08-B-risk mitigation)
Plans:
**Wave 1**

- [x] 06-01-PLAN.md — Wave 0 spike Vast.ai runtype=args + Jinja strategy decision (B1/B2) + grep survey (PRV-06) — commit b997d25
- [x] 06-02-PLAN.md — Wave 1 config refactor: 4 fields novos (EmergencyTemplateImage, EmergencyJinjaTemplate*, EmergencyLlamaArgs) + remove EmergencyPodImageTag (PRV-06) — commit 881e9c6
- [x] 06-03-PLAN.md — Wave 1 vast types.go: add Args []string field + Runtype comment update (PRV-06) — commit d8c322c

**Wave 2** *(blocked on Wave 1 completion)*

- [x] 06-04-PLAN.md — Wave 2 lifecycle.go buildCreateRequest Strategy B (Image upstream + Runtype=args + Args + Onstart inline MinIO+sha256) + 9 unit tests RED-GREEN (PRV-06, PRV-01, PRV-05, PRV-07, PRV-08, PRV-09, PRV-10) — commits 50e606d + 19942bc

**Wave 3** *(blocked on Wave 2 completion)*

- [x] 06-05-PLAN.md — Wave 3 integration test update (emerg_leader_test EmergencyTemplateImage) + full-suite gate (PRV-06) — commit e179104

**Wave 4** *(blocked on Wave 3 completion)*

- [ ] 06-06-PLAN.md — Wave 4 HUMAN-UAT 3 lifecycles LIVE Vast.ai + RUNBOOK update (PRV-01..10) [BLOCKING]

**Wave 5** *(blocked on Wave 4 completion)*

- [ ] 06-07-PLAN.md — Wave 5 PR2 cleanup: delete pod/Dockerfile + emerg-bootstrap.sh + build-pod.yml + runbook revert section update (PRV-06) [BLOCKING gate operator]

**Research hint:** yes (Vast.ai cache-hit rates por template/GPU classe, CUDA matrix compat com llama.cpp release, runtype "args" + onstart inline best-practice)
**UI hint:** no

### Phase 6.5: Auto-provisioning Emergency Pod (Vast.ai)

**Goal:** When the primary GPU is gone for minutes, the system stands up an emergency Vast.ai pod, routes to it, and tears it down once primary is stable — with guardrails that prevent runaway cost or duplicated pods.
**Depends on:** Phase 1 (image to boot), Phase 3 (fallback fills the gap during provisioning), Phase 5 (saturation != failure — clean trigger signal)
**Requirements:** PRV-01, PRV-02, PRV-03, PRV-04, PRV-05, PRV-06, PRV-07, PRV-08, PRV-09, PRV-10
**Success Criteria** (what must be TRUE):

  1. After primary has been in `FAILED_OVER` for the configured trigger duration (e.g., 2 min), exactly one emergency pod is provisioned on Vast.ai (a distinct host from the primary) and added to the routing pool within ≤10 minutes once `/health` passes.
  2. Running two gateway replicas simultaneously during a failure never produces more than one emergency pod (Redis distributed lock + single-reconciler confirmed).
  3. If the primary recovers while provisioning is in flight, the emergency pod creation is cancelled/destroyed before it starts serving traffic (cancel-in-flight).
  4. Once primary is healthy for 5 minutes, traffic cuts back to primary; after an additional 5-minute idle grace period, the emergency pod is destroyed automatically.
  5. A Vast.ai offer priced above the configured cap ($0.40/h) is never accepted; each lifecycle emits a full audit record (trigger, offer accepted, duration, total cost, shutdown reason).

**Plans:** 11 plans
Plans:

- [x] 06-01-PLAN.md — Wave 0 scaffolding: redsync v4 + sentinel errors emerg/vast + namespace mirror + 11 env vars config + 7 obs collectors emergency_* + operator gates (price cap, budget, BRL rate) (PRV-01, PRV-02, PRV-05)
- [x] 06-02-PLAN.md — DB foundation: migration 0019_emergency_lifecycles.sql (11 cols + 4 indexes + first_health_pass_at + partial unique singleton) + 7 sqlc queries + 2 integration tests (singleton + migration) (PRV-05, PRV-10)
- [x] 06-03-PLAN.md — emerg/fsm.go (9-state atomic + transition CAS + onChange + sentry breadcrumb) + redisx/emerg.go (Hash + Pub/Sub + redsync factory) + unit tests (PRV-02)
- [x] 06-04-PLAN.md — Reconciler Run loop + leader election via redsync (Pitfall 4 Extend quorum case + Pitfall 8 separate Unlock ctx) + integration test 2-replicas-1-leader (PRV-03, SC-2)
- [x] 06-05-PLAN.md — Subscribe gw:breaker:events + localLlmTracker (openSince + sustained timer) + evaluateHealthy trigger Healthy→FailedOver→EmergencyProvisioning + 3 integration tests (sustained, transient, no-spawn-if-live) (PRV-04, SC-1)
- [x] 06-06-PLAN.md — Vast spike port discovery + vast/client.go (5 ops + parseErrorBody + T-6-01 enforced) + lifecycle.go (provisionLifecycle search→create→/health + bid race retry 3x + Pitfalls 1+5+9 enforced) + 3 integration tests (happy + price cap + bid race lost) (PRV-01, PRV-05, PRV-06, PRV-07, SC-1, SC-5)
- [x] 06-07-PLAN.md — cancelActiveLifecycle (3 layers: ctx + Pub/Sub + post-create destroy) + recovery.go (D-D5 3 cenários: pre-create orphan + lost + zombie) + 3 integration tests (cancel pre-create + cancel post-create + leader recovery zombie) (PRV-09, SC-3)
- [x] 06-08-PLAN.md — upstreams.Loader OverrideTier0/RestoreTier0 (atomic.Pointer LLM-only) + dispatcher RegisterTraffic hook + evaluateActive/Recovering/Cooldown (cutback + idle destroy + multi-failover ride-out) + 3 integration tests (cutback + idle destroy + ride-out) (PRV-08, SC-4)
- [x] 06-09-PLAN.md — budget.go (checkBudget + dedupe Pitfall 11 CORRECT + Sentry warning) + 60s tick wrapper + 2 integration tests (budget alert + audit completeness 11 fields) (PRV-05, PRV-10)
- [x] 06-10-PLAN.md — gatewayctl emerg state|force-provision|force-destroy|lifecycles + main.go wiring (NewReconciler + 2 goroutines + Vast.Ping at boot) (PRV-08, PRV-10)
- [ ] 06-11-PLAN.md — HUMAN-UAT: 6 cenários LIVE Vast.ai (force-provision, budget tally, force-destroy, Sentry, budget alert, cancel-in-flight) + RUNBOOK-EMERGENCY-POD.md (PRV-01..10, SC-1..5)

**Research hint:** yes (Vast.ai REST API quirks — SSH/onstart/port exposure/bid acceptance timing; 3h timeboxed spike before commit)
**UI hint:** no

### Phase 7: Observability — Dashboard & Alerting

**Goal:** Operators can see — in one place — how the gateway is behaving per tenant, get paged when it matters, and have an audit trail for every significant state change.
**Depends on:** Phase 6.5 (FSM states to visualize and alert on); data sources from Phases 2-6.5
**Requirements:** OBS-01, OBS-02, OBS-03, OBS-04, OBS-05, OBS-06, OBS-07, OBS-08
**Success Criteria** (what must be TRUE):

  1. An admin can open the Next.js dashboard and see live per-tenant latency (P50/P95/P99), error rate, daily/monthly cost, and the current failover FSM state.
  2. When primary GPU goes down for ≥30s (critical), the on-call Ifix operator receives a WhatsApp message and an email within 60s; the same event raises a dashboard banner.
  3. When a warning-tier event repeats within 5 minutes, alerts are deduplicated (one notification, not ten).
  4. Every FSM transition, tenant activation/deactivation, pod spin-up/shutdown, and threshold change leaves an entry in the `audit_log` table accessible via dashboard or SQL.
  5. Prometheus scrape of `/metrics` stays under 10k active series and is consumable by standard Prometheus tooling.
  6. Sentry captures panics/circuit trips/provisioning failures with `authorization`, `x-api-key`, and payload bodies redacted.

**Plans:** 9 plans
Plans:

- [x] 07-01-PLAN.md — Wave 0 scaffolding: 12 alert env vars in config + migration 0020 (audit_log.event_kind) + ListAuditStateChanges query + 2 bounded latency histograms + gateway_alert_dropped_total + alert package test fakes (OBS-01,02,04,05,07)
- [x] 07-02-PLAN.md — Gateway obs extensions: middleware records latency histograms + Sentry BeforeSend scrubs request/response bodies + audit.Writer EventKind field + WriteStateChange helper (OBS-02,07,08)
- [x] 07-03-PLAN.md — Admin JSON handlers: TenantLatencyPercentiles query + GET /admin/metrics (per-tenant P50/P95/P99 + FSM state + inflight) + GET /admin/audit (paginated state-change history) (OBS-01,07)
- [x] 07-04-PLAN.md — Alert external clients: redisx/alert.go dedup namespace + Channel interface + Chatwoot/ClickUp/Brevo Go clients (gobreaker + backoff.Permanent + net/smtp; zero new deps) (OBS-04,05)
- [x] 07-05-PLAN.md — Alert core: severity.go (event→tier→channel matrix) + dedup.go (SET NX EX 300, fail-open critical) + alerter.go (Run goroutine, 3-channel subscribe, bounded workers, ReconcileBoot) (OBS-04,06)
- [x] 07-06-PLAN.md — Gateway wiring: main.go constructs alert clients from config + spawns alerter goroutine early + mounts /admin/metrics + /admin/audit + FSM transitions emit fsm_transition audit rows (OBS-01,04,05,07)
- [x] 07-07-PLAN.md — Dashboard scaffold: greenfield dashboard/ Next.js 15 + shadcn radix-nova + standalone Better Auth (emailAndPassword) + server-side gateway proxy + unauthed→/login + Dockerfile + build-dashboard.yml + docker-compose service (OBS-03)
- [x] 07-08-PLAN.md — Dashboard UI: React Query 5-10s polling + (dashboard) layout + sidebar + critical banner + KPI cards + Recharts latency chart + FSM panel + tenant/audit tables + Overview/Tenants/Incidents pages + human-verify checkpoint (OBS-03)
- [x] 07-09-PLAN.md — HUMAN-UAT: RUNBOOK-OBSERVABILITY-ALERTING.md + 07-HUMAN-UAT.md (SC-2 live WhatsApp/email/ClickUp, SC-3 live dedup, SC-5 Prometheus cardinality, SC-6 Sentry redaction) + sign-off (OBS-02,04,05,08)

**Research hint:** yes (confirm Ifix WhatsApp provider — Evolution API vs Z-API vs Chatwoot; confirm Better Auth vs SSO for dashboard)
**UI hint:** yes

### Phase 8: Client Integration — ConverseAI + Chat Ifix

**Goal:** The first two production workloads (chat+agents on ConverseAI v4; audio transcription on Chat Ifix) run through the gateway, validating the OpenAI-compat contract with real traffic.
**Depends on:** Phase 2 (endpoints live), Phase 3 (fallback), Phase 7 (observability to catch regressions)
**Requirements:** INT-01, INT-02
**Success Criteria** (what must be TRUE):

  1. ConverseAI v4 (api Elysia + agents Python) points to `gateway.ifix.com.br` via env-var changes only; chat completions, streaming, tool calls, and embeddings all work in staging and are validated against dev traffic.
  2. Chat Ifix transcribes a sample of real Whatsapp audios via gateway Whisper; transcription quality and latency are equivalent to the prior direct integration within ±10%.
  3. A documented rollback plan (revert env vars, redeploy) is tested end-to-end and measured at <5 minutes from decision to fully-rolled-back.
  4. Dashboard shows both apps' traffic as separate tenants with independent latency and cost panels.

**Plans:** 4 plans (3 waves)
Plans:

- [x] 08-01-PLAN.md — Idempotent provision-tenants.sh seed script (wraps gatewayctl tenant/key/admin-key create) + integration-smoke README (INT-01, INT-02)
- [x] 08-02-PLAN.md — smoke-converseai.py (chat/streaming/tool-call/embedding gateway smoke) + report schema + trimmed requirements (INT-01)
- [x] 08-03-PLAN.md — smoke-chat-ifix.py (Whisper transcription smoke, ±10% latency + WER quality gates) + committed WhatsApp audio fixture + baseline (INT-02)
- [x] 08-04-PLAN.md — RUNBOOK-CLIENT-INTEGRATION.md (<5-min rollback procedure) + 08-HUMAN-UAT.md scenario sheet + blocking live-UAT checkpoint (INT-01, INT-02)

**Research hint:** no (straightforward env-var migration)
**UI hint:** no

### Phase 9: Client Integration — Sensitive Tenants (Telefonia, Cobranças, Campanhas, voice-api)

**Goal:** Integrate the LGPD-sensitive and remaining business workloads behind the data-class-aware failover policy, with legal sign-off before turning on sensitive tenants in production.
**Depends on:** Phase 3 (sensitive-class policy), Phase 7 (audit log), Phase 8 (pattern established)
**Requirements:** INT-03, INT-04, INT-05
**Success Criteria** (what must be TRUE):

  1. Telefonia/NextBilling transcribes call audios through the gateway with its API key marked `data_class: sensitive`; during induced primary failure the requests queue or fail closed — never reaching OpenAI/OpenRouter — and the audit table records the decision.
  2. Cobranças and Campanhas send LLM personalization + embedding lookups through the gateway with tenant-specific quotas, and metrics confirm cost-per-request is reported correctly.
  3. voice-api continues to run TTS locally on CPU but retrieves LLM-generated scripts through the gateway.
  4. Each app has an individual smoke-test on production and a <5 min rollback playbook; LGPD review is documented before sensitive tenants go live in prod.

**Plans:** 4 plans (2 waves)
Plans:

- [x] 09-01-PLAN.md — Extend provision-tenants.sh to 4 mixed-data-class tenants (telefonia + cobrancas sensitive, campanhas + voice-api normal) + per-tenant quotas + README update (INT-03, INT-04, INT-05)
- [x] 09-02-PLAN.md — smoke-sensitive-failover.py (RES-08 fail-closed + never-external + audit gates) + sensitive-failover-report-schema.json (INT-03, INT-04)
- [x] 09-03-PLAN.md — RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md (4 per-app <5-min rollback procedures) + LGPD-SUBPROCESSORS.md + LGPD-REVIEW-CHECKLIST.md (INT-03, INT-04, INT-05)
- [x] 09-04-PLAN.md — HUMAN-UAT: 09-HUMAN-UAT.md scenario sheet (SC1-SC4) + blocking live-UAT checkpoint + blocking LGPD legal sign-off gate (INT-03, INT-04, INT-05)

**Research hint:** yes (LGPD review with Ifix legal — external input required before GA of sensitive tenants)
**UI hint:** no

### Phase 10: Production Hardening & GA

**Goal:** Close the loop with realistic load tests, chaos drills, runbooks, secure admin access, and public DNS/TLS — declare GA once the "failover invisible" core value is evidenced.
**Depends on:** Phases 1-9 (everything integrated)
**Requirements:** INT-06, PRD-01, PRD-02, PRD-03, PRD-04, PRD-05, PRD-06, PRD-07
**Success Criteria** (what must be TRUE):

  1. A load test using three tenants' real-production traffic profile establishes baseline P95 latency and capacity; results are committed to the repo and dashboard.
  2. A chaos drill kills the primary pod during sustained load and operators observe: streaming requests fail fast, non-streaming requests succeed on fallback, emergency pod spins up, cutback works, audit log and alerts reflect the incident — with client apps unaware.
  3. A second chaos drill simulates OpenRouter unavailability during failover and the system degrades deterministically (queues, OpenAI fallback, or controlled failure) — no silent corruption.
  4. A runbook document covers detection → diagnosis → rollback → postmortem for the five most-likely incident classes; on-call operator executes one scenario from the runbook successfully.
  5. The dashboard is reachable at a HTTPS URL behind admin authentication (Better Auth or SSO); `gateway.ifix.com.br` resolves via Cloudflare with valid TLS.
  6. LGPD review sign-off is attached to the repo and all sensitive tenants have confirmed privacy-policy disclosures listing OpenAI/OpenRouter/Vast.ai as sub-processadores.

**Plans:** 8 plans (estimate — not yet planned; run /gsd-plan-phase 10)
**Research hint:** no (execution phase — no open research)
**UI hint:** yes

---

## Progress

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 1. GPU Pod Image & Smoke-Test | 9/9 | Complete (human_needed — smoke.yml UAT pending) | 2026-04-18 |
| 2. Gateway Core + Multi-tenant Auth | 8/9 | Complete (human_needed — live deploy UAT; 02-09 deferred to P7/P10) | 2026-04-18 |
| 3. Resilience & Fallback Chain | 8/8 | Complete (human_needed — SC-1 live failover UAT) | 2026-04-20 |
| 4. Multi-tenant Quotas, Billing & Schedule Routing | 9/9 | Complete (human_needed — 3 SC live UAT deferred) | 2026-04-21 |
| 5. Load Shedding | 8/8 | Complete (passed_partial — SC-4/SC-5 deferred) | 2026-05-11 |
| 6. Emergency-Pod Template Refactor | 0/7 | Planned (7 plans, 5 waves, PR1/PR2 split) | - |
| 6.5. Auto-provisioning Emergency Pod | 10/11 | In Progress (autonomous plans done sob numeração antiga; 06-11 HUMAN-UAT blocking + VERIFICATION pending — desbloqueado por Phase 6) | - |
| 7. Observability — Dashboard & Alerting | 0/9 | Planned (9 plans, 5 waves) | - |
| 8. Client Integration — ConverseAI + Chat Ifix | 0/4 | Planned (4 plans, 3 waves) | - |
| 9. Client Integration — Sensitive Tenants | 0/4 | Planned (4 plans, 2 waves) | - |
| 10. Production Hardening & GA | 0/? | Not started | - |

---

## Coverage Summary

**Total v1 requirements:** 70
**Mapped:** 70 (100%)
**Orphaned:** 0

| Category | Count | Phase(s) |
|----------|-------|----------|
| POD (Inference Pod)       | 7  | 1 |
| GW (Gateway Core)         | 10 | 2 |
| TEN (Multi-tenant)        | 9  | 2 (auth: 01,02,08,09) + 4 (quotas: 03-07) |
| RES (Resilience)          | 8  | 3 |
| LSH (Load Shedding)       | 5  | 5 |
| PRV (Auto-provisioning)   | 10 | 6 |
| OBS (Observability)       | 8  | 7 |
| INT (Integrations)        | 6  | 8 (01,02) + 9 (03,04,05) + 10 (06) |
| PRD (Production Hardening)| 7  | 10 |

---

*Roadmap created: 2026-04-17*
*Phase 1 plans created: 2026-04-17*
*Phase 6.5 plans created: 2026-05-13 (11 plans, originally numbered Phase 6 — renamed 2026-05-16 after SEED-001 swap)*
*Phase 7 plans created: 2026-05-14 (9 plans, 5 waves)*
*Phase 8 plans created: 2026-05-14 (4 plans, 3 waves)*
*Phase 9 plans created: 2026-05-14 (4 plans, 2 waves)*
*Phase 6 plans created: 2026-05-16 (7 plans, 5 waves, PR1/PR2 burnt-bridge split — Strategy B Locked emerged from research)*
