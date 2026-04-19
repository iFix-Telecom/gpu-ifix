# Roadmap: ifix-ai-gateway

**Created:** 2026-04-17
**Core Value:** Nenhuma aplicação da Ifix sente quando a GPU cai. Failover invisível.
**Granularity:** fine (10 phases)
**Total v1 Requirements:** 70 (all mapped)

## Phases

- [x] **Phase 1: GPU Pod Image & Smoke-Test** — Build and validate the pre-baked inference pod image (llama.cpp + Speaches + Infinity + dcgm-exporter) with VRAM measured under load
- [ ] **Phase 2: Gateway Core + Multi-tenant Auth** — Go gateway serves OpenAI-compatible endpoints with API key auth, data-class tagging, and idempotency — single upstream, no failover yet
- [ ] **Phase 3: Resilience & Fallback Chain** — Circuit breakers + retries + local→OpenRouter→OpenAI fallback chain with explicit streaming policy and LGPD-safe routing for sensitive tenants
- [ ] **Phase 4: Multi-tenant Quotas, Billing & Schedule Routing** — Rate limiting, daily/monthly quotas, token counting, cost attribution per tenant, 24/7 vs peak schedules
- [ ] **Phase 5: Load Shedding (Saturation-aware Routing)** — Composite saturation signal (inflight + P95 + VRAM) with hysteresis overflows traffic to OpenRouter before local fails
- [ ] **Phase 6: Auto-provisioning Emergency Pod (Vast.ai)** — Leader-elected state machine spins up emergency Vast.ai pod on sustained failure and tears it down after primary recovers
- [ ] **Phase 7: Observability — Dashboard & Alerting** — Next.js dashboard, Prometheus metrics, WhatsApp/email alerts with severity tiers, Sentry, audit log
- [ ] **Phase 8: Client Integration — ConverseAI + Chat Ifix** — First two integrations (low risk, well-known apps) switch base_url to gateway
- [ ] **Phase 9: Client Integration — Sensitive Tenants (Telefonia, Cobranças, Campanhas, voice-api)** — Integrate `data_class: sensitive` apps behind LGPD review and secondary-tier policies
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
**Plans:** 7/9 plans executed
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
**Plans:** TBD
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
**Plans:** TBD
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
**Plans:** TBD
**Research hint:** yes (real saturation thresholds must be tuned from Phase 1 baseline data; histeresis window needs empirical validation)
**UI hint:** no

### Phase 6: Auto-provisioning Emergency Pod (Vast.ai)
**Goal:** When the primary GPU is gone for minutes, the system stands up an emergency Vast.ai pod, routes to it, and tears it down once primary is stable — with guardrails that prevent runaway cost or duplicated pods.
**Depends on:** Phase 1 (image to boot), Phase 3 (fallback fills the gap during provisioning), Phase 5 (saturation != failure — clean trigger signal)
**Requirements:** PRV-01, PRV-02, PRV-03, PRV-04, PRV-05, PRV-06, PRV-07, PRV-08, PRV-09, PRV-10
**Success Criteria** (what must be TRUE):
  1. After primary has been in `FAILED_OVER` for the configured trigger duration (e.g., 2 min), exactly one emergency pod is provisioned on Vast.ai (a distinct host from the primary) and added to the routing pool within ≤10 minutes once `/health` passes.
  2. Running two gateway replicas simultaneously during a failure never produces more than one emergency pod (Redis distributed lock + single-reconciler confirmed).
  3. If the primary recovers while provisioning is in flight, the emergency pod creation is cancelled/destroyed before it starts serving traffic (cancel-in-flight).
  4. Once primary is healthy for 5 minutes, traffic cuts back to primary; after an additional 5-minute idle grace period, the emergency pod is destroyed automatically.
  5. A Vast.ai offer priced above the configured cap ($0.40/h) is never accepted; each lifecycle emits a full audit record (trigger, offer accepted, duration, total cost, shutdown reason).
**Plans:** TBD
**Research hint:** yes (Vast.ai REST API quirks — SSH/onstart/port exposure/bid acceptance timing; 3h timeboxed spike before commit)
**UI hint:** no

### Phase 7: Observability — Dashboard & Alerting
**Goal:** Operators can see — in one place — how the gateway is behaving per tenant, get paged when it matters, and have an audit trail for every significant state change.
**Depends on:** Phase 6 (FSM states to visualize and alert on); data sources from Phases 2-6
**Requirements:** OBS-01, OBS-02, OBS-03, OBS-04, OBS-05, OBS-06, OBS-07, OBS-08
**Success Criteria** (what must be TRUE):
  1. An admin can open the Next.js dashboard and see live per-tenant latency (P50/P95/P99), error rate, daily/monthly cost, and the current failover FSM state.
  2. When primary GPU goes down for ≥30s (critical), the on-call Ifix operator receives a WhatsApp message and an email within 60s; the same event raises a dashboard banner.
  3. When a warning-tier event repeats within 5 minutes, alerts are deduplicated (one notification, not ten).
  4. Every FSM transition, tenant activation/deactivation, pod spin-up/shutdown, and threshold change leaves an entry in the `audit_log` table accessible via dashboard or SQL.
  5. Prometheus scrape of `/metrics` stays under 10k active series and is consumable by standard Prometheus tooling.
  6. Sentry captures panics/circuit trips/provisioning failures with `authorization`, `x-api-key`, and payload bodies redacted.
**Plans:** TBD
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
**Plans:** TBD
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
**Plans:** TBD
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
**Plans:** TBD
**Research hint:** no (execution phase — no open research)
**UI hint:** yes

---

## Progress

| Phase | Plans Complete | Status | Completed |
|-------|----------------|--------|-----------|
| 1. GPU Pod Image & Smoke-Test | 9/9 | Complete (human_needed) | 2026-04-18 |
| 2. Gateway Core + Multi-tenant Auth | 3/9 | In Progress|  |
| 3. Resilience & Fallback Chain | 0/? | Not started | - |
| 4. Multi-tenant Quotas, Billing & Schedule Routing | 0/? | Not started | - |
| 5. Load Shedding | 0/? | Not started | - |
| 6. Auto-provisioning Emergency Pod | 0/? | Not started | - |
| 7. Observability — Dashboard & Alerting | 0/? | Not started | - |
| 8. Client Integration — ConverseAI + Chat Ifix | 0/? | Not started | - |
| 9. Client Integration — Sensitive Tenants | 0/? | Not started | - |
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
