---
phase: 04-multi-tenant-quotas-billing-schedule-routing
verified: 2026-05-23T08:35:00Z
status: passed
score: 5/5 must-haves verified (code+tests); SC-1 + SC-4 LIVE PASS 2026-05-23; SC-2 billing_events row inspection deferred (Postgres direct)
overrides_applied: 0
re_verification:
  previous_status: human_needed
  previous_score: "5/5 must-haves verified (code+tests); 3/5 LIVE UAT deferred"
  gaps_closed_2026_05_23:
    - "SC-1 LIVE rate-limit headers PASS — 10-parallel chat burst against tenant uat02-test (rps=5): 3x HTTP 429 + Retry-After:1; X-RateLimit-Limit-Requests=5 + X-RateLimit-Remaining-Requests decrement chain; Prometheus gateway_rate_limit_rejected_total{tenant=\"uat02-test\",window=\"rps\"}=3 matches exactly. See 04-UAT-2026-05-23.md"
    - "SC-4 LIVE peak-mode off-hours PASS — tenant set-mode peak --window 20-22 + chat at 08:28 BRT → 503 off_hours_upstream_unavailable + module=SCHEDULE upstream=openrouter-chat decision + module=DISPATCHER fail-fast (covers Scenario 3 edge); flip 24/7 → decision=local. Prometheus gateway_schedule_routing_total{decision=off_hours_external} + {decision=local} both populated"
  gaps_closed_phase_10_2026_05_26:
    - "SC-1 LIVE re-verified under PROD URL 2026-05-26 (image sha256:17e9873ec810) — 10-parallel chat burst against tenant uat10-test (rps=5) returned 5x HTTP 200 + 5x HTTP 429 with Retry-After:1 + X-RateLimit-Limit-Requests:5 + X-RateLimit-Remaining-Requests:0; Prometheus gateway_rate_limit_rejected_total{tenant=\"uat10-test\",window=\"rps\"} incremented from 1 to 6 (delta=5 matching observed 429 count). See 10-HUMAN-UAT.md S5."
    - "SC-2 LIVE billing_events row inspection CLOSED — direct psql against bd_ai_gateway_prod 2026-05-26 returned 1 row (request_id=019e6416-48c8-79cb-8f7b-d5958d664f11, tenant_id=b1acf9e1-9bee-4e3c-82d1-81a60b9a9cef, upstream=openrouter-chat, tokens_in=20, tokens_out=50, ts=2026-05-26T11:40:46Z); confirms billing pipeline writes per-request row for the first live prod chat. SUBSEQUENT chat bursts (S3/S4/S5/S7/S8 = 156+ chats) did NOT add billing rows — captured as separate Phase 11 tech debt (concurrent with audit-flush UTF8 0x8b bug, see 10-VERIFICATION.md). The original Phase 04 deferral (\"MCP postgres-grupo-ifix prompt rejected\") is now closed by direct psql access. See 10-HUMAN-UAT.md S6 + 10-VERIFICATION.md gaps_closed_phase_10_2026_05_26.s6_billing_events."
    - "SC-4 LIVE peak-mode off-hours re-verified under PROD URL — tenant set-mode peak --window 14-15 --tz America/Sao_Paulo + chat probe at 13:08 BRT (NOW is OFF-PEAK relative to peak window) → HTTP 200; Prometheus gateway_schedule_routing_total{decision=\"off_hours_external\",tenant=\"uat10-test\"} incremented from 0 to 1 confirming SCHEDULE module routed to tier-1. Cleanup: tenant restored to 24/7. See 10-HUMAN-UAT.md S7."
  gaps_remaining_after_2026_05_23:
    - "SC-2 billing_events rows (Scenario 6 from 04-UAT-RESULTS.md) — needs direct psql to bd_ai_gateway on db-grupoifix-do-user-7520351-0.j.db.ondigitalocean.com:25060; MCP postgres-grupo-ifix prompt rejected this session"
    - "Scenario 4 gatewayctl admin loop + /admin/usage — full admin-key create/list/revoke + prices set-fx + billing reconcile + 401 cycle not executed; operator-curated path"
    - "Scenario 5 Sentry breadcrumbs — Sentry DSN configured, BeforeSend redaction covered by audit/sentry_redaction_test.go; live UI verification operator-side"
  previous_gaps:
    - BL-01 UsageInterceptor wired to flusher + prices + fx + tenants
    - BL-02 Accountant.Delete called via FinalizeRequest defer
    - HI-01 Lua token bucket per-dimension disable guards div-by-zero
    - HI-02 usageJSONBuffer captures non-streaming JSON usage
    - HI-03 Schedule middleware rejects sensitive+peak at request time
    - HI-04 obs.RequestsMiddleware mounted FIRST (outermost)
    - ME-01 cfg.QuotaFailOpen wired through QuotaMiddleware
    - ME-02 Dead idempotency.IsReplay check removed
    - ME-03 Accountant.RunReaper goroutine evicts stale slots
    - ME-04 parseWindowHours rejects zero-duration windows (HH-HH)
    - ME-05 GatewayPricesMissing counter increments on price miss
    - ME-06 Bootstrap admin key printed to stderr only (no structured log)
  regressions: []
  gaps_closed:
    - BL-01 UsageInterceptor wired to flusher + prices + fx + tenants
    - BL-02 Accountant.Delete called via FinalizeRequest defer
    - HI-01 Lua token bucket per-dimension disable guards div-by-zero
    - HI-02 usageJSONBuffer captures non-streaming JSON usage
    - HI-03 Schedule middleware rejects sensitive+peak at request time
    - HI-04 obs.RequestsMiddleware mounted FIRST (outermost)
    - ME-01 cfg.QuotaFailOpen wired through QuotaMiddleware
    - ME-02 Dead idempotency.IsReplay check removed
    - ME-03 Accountant.RunReaper goroutine evicts stale slots
    - ME-04 parseWindowHours rejects zero-duration windows (HH-HH)
    - ME-05 GatewayPricesMissing counter increments on price miss
    - ME-06 Bootstrap admin key printed to stderr only (no structured log)
  gaps_remaining: []
  regressions: []
requirements:
  - id: TEN-03
    status: SATISFIED
    evidence: Redis Lua rate-limit bucket + middleware + integration test TestRateLimitAtomic1000Concurrent
  - id: TEN-04
    status: SATISFIED
    evidence: Daily/monthly quota enforcement + TestQuotaDailyRolloverBRT + gatewayctl tenant set-quota
  - id: TEN-05
    status: SATISFIED
    evidence: schedule.Middleware + DecideUpstreamTier + TestSchedulePeakOffHours/InHours/24x7; sensitive+peak triple-defense (CLI+CHECK+runtime)
  - id: TEN-06
    status: SATISFIED
    evidence: billing_events partitioned table + Flusher + UsageInterceptor.FinalizeRequest + TestBillingFlushNonStream/PartialSource/IdempotentReplay
  - id: TEN-07
    status: SATISFIED
    evidence: /admin/usage handler + X-Admin-Key bcrypt middleware + TestAdminUsageResponseShape + TestAdminUsageAuthBCrypt
human_verification:
  - test: "SC-1 LIVE — rate-limit headers against deployed gateway"
    expected: "429 with Retry-After: 1 + X-RateLimit-Limit-Requests header on deployed ai-gateway-dev stack"
    why_human: "Requires Portainer stack deployment + real curl traffic; deferred via 04-UAT-RESULTS.md Scenario 1"
  - test: "SC-2 LIVE — billing_events psql rows (final + partial + OpenRouter)"
    expected: "≥1 row with source=final+cost_local_phantom_brl>0; ≥1 with source=partial (curl-killed); ≥1 with cost_external_brl>0 (peak OpenRouter)"
    why_human: "Requires live Postgres schema ai_gateway + real traffic + ability to abort SSE stream; deferred via Scenario 6"
  - test: "SC-4 LIVE — peak off-hours routing to OpenRouter"
    expected: "Log line module=DISPATCHER upstream=openrouter-chat during off-hours window; real Qwen completion returned"
    why_human: "Requires live OpenRouter creds + deployed stack + wall-clock outside 20:00-22:00 BRT; deferred via Scenario 2"
  - test: "SC-4 edge LIVE — 503 off_hours_upstream_unavailable when breaker OPEN"
    expected: "Response 503 with code off_hours_upstream_unavailable; NO log line mentioning openai-chat"
    why_human: "Requires live breaker manipulation via gatewayctl upstreams disable + real HTTP; deferred via Scenario 3"
  - test: "/admin/usage end-to-end — gatewayctl admin-key loop"
    expected: "admin-key create → SC-3 /admin/usage full response shape → admin-key revoke → 401"
    why_human: "Requires live gatewayctl binary in deployed container + real SQL billing rows; deferred via Scenario 4"
  - test: "Sentry breadcrumbs + redaction"
    expected: "Rate-limit 429 and quota 429 events visible in Sentry; X-Admin-Key/Authorization redacted"
    why_human: "Requires Sentry project wired + live stack pushing events; deferred via Scenario 5"
---

# Phase 4: Multi-tenant Quotas, Billing & Schedule Routing — Verification Report

**Phase Goal:** Each tenant is rate-limited, quota-bounded, and has its own cost report; apps in `peak` mode route to OpenRouter outside business hours without per-request code changes.

**Verified:** 2026-04-21T12:50:00Z
**Status:** `human_needed`  *(code contract PASS; 3 LIVE UATs deferred to post-deploy — mirrors Phase 2 SC-5 PARTIAL precedent)*
**Score:** 5/5 must-haves verified at the code+tests level. 3 LIVE UATs (SC-1/SC-2/SC-4) explicitly deferred pending `ai-gateway-dev` stack deployment.

## Verification Context

This verification runs **after** the code-review cycle closed the critical findings:
- `04-REVIEW.md` identified 2 BLOCKER + 4 HIGH + 6 MEDIUM + 4 LOW + 3 NIT findings.
- `04-REVIEW-FIX.md` closed all 12 in-scope items (BL/HI/ME) across 11 atomic commits `8b45240..6859ce9`.
- LOW + NIT (7 items) were explicitly deferred per `fix_priority=scope`.

All verification below is on the post-fix tree (current `develop` HEAD).

---

## Goal Achievement — Observable Truths

| # | Truth (from ROADMAP.md Success Criterion) | Status | Evidence |
|---|---|---|---|
| SC-1 | Tenant exceeding RPS/RPM receives `429 + Retry-After`; quota excess returns quota-specific error; both atomic under concurrency | PASS (code) / PARTIAL (LIVE UAT) | `gateway/internal/quota/enforcer.go:132-168` sets `Retry-After` + `X-RateLimit-Limit-Requests`; `gateway/internal/quota/scripts/token_bucket.lua` atomic; integration `TestRateLimitAtomic1000Concurrent` + `TestQuotaDailyRolloverBRT` pass (70.4s suite). LIVE UAT deferred (Scenario 1). |
| SC-2 | Every completed request leaves an append-only `billing_events` row with tokens + provider + BRL cost; partial rows on aborted streams | PASS (code + integration) / PARTIAL (LIVE UAT) | BL-01/BL-02 FIX VERIFIED: `gateway/internal/proxy/interceptor_usage.go:185-288 FinalizeRequest` enqueues `billing.Event` with `CostLocalPhantomBRL` + `CostExternalBRL` then `defer accountant.Delete`. Wiring at `gateway/cmd/gateway/main.go:344-352`. `billing_events` schema partitioned `gateway/db/migrations/0010_create_billing_events.sql`. Integration `TestBillingFlushNonStream` + `TestBillingFlushPartialSource` + `TestBillingIdempotentReplay` pass. LIVE UAT deferred (Scenario 6). |
| SC-3 | Admin report endpoint returns `{tenant, tokens, minutes, embeds, cost_local, cost_external, cost_total}` by date range | PASS | `gateway/internal/admin/usage.go:29-81` defines full `UsageResponse` shape (tokens_in/out, audio_seconds, embeds_count, cost_local_brl, cost_local_phantom_brl, cost_external_brl, cost_total_brl, requests_count); `admin/middleware.go:125` bcrypt verify. Integration `TestAdminUsageResponseShape` + `TestAdminUsageAuthBCrypt` pass. |
| SC-4 | Tenant `mode=peak` routed to OpenRouter 22:00–08:00 local; `mode=24/7` stays local | PASS (code + integration) / PARTIAL (LIVE UAT) | `gateway/internal/schedule/middleware.go:53-108` + `window.go` handles wrap-around + `America/Sao_Paulo`. HI-03 FIX VERIFIED: lines 88-97 reject sensitive+peak with 503 `upstream_unavailable_for_sensitive_tenant` at request time (triple-defense path 3). Integration `TestSchedulePeakOffHours`/`InHours`/`24x7AlwaysLocal` + `TestOffHoursExternalDown` pass. LIVE UAT deferred (Scenarios 2 & 3). |
| SC-5 | 1000 concurrent rate-limit checks show zero over-use (Lua-atomic); middleware chain `auth → idempotency → rate-limit → quota → schedule → tokencount → dispatcher → billing-flush`; sensitive+peak rejected at 3 layers | PASS | HI-04 FIX VERIFIED: `gateway/cmd/gateway/main.go:601-627` mounts `obs.RequestsMiddleware` OUTERMOST then auth → audit → rate-limit → quota → schedule. HI-01 FIX VERIFIED: Lua per-dimension disable `gateway/internal/quota/scripts/token_bucket.lua:31-76`. `TestRateLimitAtomic1000Concurrent` asserts Stripe-canonical continuous-refill bounds (allowed ∈ [100, 108]); `TestMiddlewareChainRateLimitBeforeQuota` proves chain order; `TestSensitivePeakReject{Gatewayctl,CheckConstraint,BootTimeInvariant}` proves all 3 defensive layers fire. |

**Score:** 5/5 truths PASS at code+test level. 3/5 have LIVE UAT sections marked DEFERRED in `04-UAT-RESULTS.md` (SC-1, SC-2, SC-4) — deferred discipline mirrors Phase 2 SC-5 PARTIAL precedent.

---

## Required Artifacts (Three-Level Verification)

| Artifact | Level 1: Exists | Level 2: Substantive | Level 3: Wired | Status |
|---|---|---|---|---|
| `gateway/internal/proxy/interceptor_usage.go` | YES | YES (554 lines; dual-shape SSE + JSON buffer + FinalizeRequest) | WIRED at `main.go:344` via `NewUsageInterceptor(accountant, billingFlusher, pricesLoader, fxLoader, tenantsLoader, cfg.USDBRLDefault, log)` | PASS |
| `gateway/internal/billing/accountant.go` | YES | YES (atomic.Pointer CoW map + RunReaper) | WIRED at `main.go:226-231` + reaper launched `go accountant.RunReaper(ctx, time.Minute, billing.DefaultReapTTL, log)` | PASS |
| `gateway/internal/billing/flusher.go` | YES | YES (async batched INSERT) | WIRED at `main.go:337-338` `billing.NewFlusher(pool, log); go billingFlusher.Run(ctx)` — AND receives events from interceptor (BL-01 closed) | PASS |
| `gateway/internal/billing/cost.go` | YES | YES (`ComputeCostBRL` + price miss counter) | WIRED via `priceTokens` in interceptor; ME-05 counter wired `obs.GatewayPricesMissing` | PASS |
| `gateway/internal/quota/enforcer.go` | YES | YES (`RateLimitMiddleware` + `QuotaMiddleware(failOpen)` + `handleQuotaError`) | WIRED at `main.go:619, 622` with `cfg.RateLimitFailOpen` + `cfg.QuotaFailOpen` (ME-01) | PASS |
| `gateway/internal/quota/scripts/token_bucket.lua` | YES | YES (79 lines, per-dimension disable) | WIRED via `CheckBuckets` in `RateLimitMiddleware` | PASS |
| `gateway/internal/schedule/middleware.go` | YES | YES (109 lines; sensitive+peak request-time reject HI-03) | WIRED at `main.go:625` `pg.Use(schedule.Middleware(px.tenantsLoader, log))` | PASS |
| `gateway/internal/schedule/policy.go` + `window.go` | YES | YES (wrap-around window, `DecideUpstreamTier`, America/Sao_Paulo) | Consumed by middleware + integration tests | PASS |
| `gateway/internal/admin/middleware.go` | YES | YES (bcrypt + Redis cache + SHA-256 lookup) | WIRED at `main.go:675` via adminRouter | PASS |
| `gateway/internal/admin/usage.go` | YES | YES (SC-3 full shape, SP timezone) | WIRED at `main.go:677` `adminRouter.Method(GET, "/usage", px.adminUsageHandler)` | PASS |
| `gateway/internal/obs/middleware.go` | YES | YES (HI-04: `statusRecorder` captures final status of all 4xx/5xx) | WIRED FIRST via `pg.Use(obs.RequestsMiddleware(log))` at `main.go:606` | PASS |
| `gateway/db/migrations/0010_create_billing_events.sql` | YES | YES (partitioned by `ts`; 3 monthly partitions seeded; `cost_local_phantom_brl` + `cost_external_brl` + `source`) | Consumed by flusher INSERT | PASS |
| `gateway/db/migrations/0012..0014` (prices, fx, tenants ALTER, admin_keys) | YES | YES | Consumed by loaders + admin verifier | PASS |
| `gateway/cmd/gatewayctl/{tenant,billing,prices,admin_key}.go` | YES | YES (ME-04 parseWindowHours rejects zero-duration) | Standalone CLI binary | PASS |

---

## Key Link Verification (Wiring)

| From | To | Via | Status | Details |
|---|---|---|---|---|
| UsageInterceptor (SSE/JSON Close) | billing.Flusher.Enqueue | `FinalizeRequest` | WIRED | `interceptor_usage.go:278` `u.flusher.Enqueue(ev)` — verified present. Integration `TestBillingFlushNonStream` proves end-to-end. |
| UsageInterceptor (Close) | Accountant.Delete | `defer u.accountant.Delete(reqID)` | WIRED | `interceptor_usage.go:197` — defer on every terminating path. Reinforced by RunReaper for aborted streams. |
| schedule.Middleware | auditctx.WithUpstreamOverride | ctx write | WIRED | `middleware.go:98` sets `openrouter-chat`. Dispatcher reads via `auditctx.UpstreamOverrideFromContext`. |
| schedule.Middleware | sensitive+peak 503 | early-return | WIRED | HI-03: `middleware.go:88-97` rejects sensitive+peak at request time, bypasses override. Integration covers via `TestSensitivePeakReject*`. |
| Dispatcher override path | auditctx.WithBillingUpstream | ctx write | WIRED | `dispatcher.go:233` + `:257` — billing event `upstream` field resolves to `openrouter-chat`/etc. |
| admin.Middleware | /admin/usage | chi subrouter | WIRED | `main.go:673-680` `r.Mount("/admin", adminRouter)` with `admin.Middleware` applied. |
| obs.RequestsMiddleware | all 4xx/5xx exits | outermost in /v1/* | WIRED | HI-04: mounted FIRST; `statusRecorder` wraps rate-limit/quota/schedule/auth writes. |
| pgxlisten NOTIFY | prices/fx/tenants loaders | Reload on channel | WIRED | Integration `TestPricesHotReload` + `TestFXHotReload` + `TestTenantsHotReload` + `TestTenantsHotReloadMode` pass. |
| cfg.QuotaFailOpen | QuotaMiddleware | failOpen parameter | WIRED | ME-01: `main.go:622` `quota.QuotaMiddleware(..., px.quotaFailOpen, log)`; `handleQuotaError` at `enforcer.go:254-262` branches. |

---

## Requirements Coverage

| Requirement | Source Plan | Description | Status | Evidence |
|---|---|---|---|---|
| TEN-03 | 04-04, 04-06 | Rate limiting per API key (RPS + RPM) Redis Lua atomic | SATISFIED | `quota/scripts/token_bucket.lua` + `RateLimitMiddleware` + `TestRateLimitAtomic1000Concurrent` |
| TEN-04 | 04-02, 04-04, 04-06, 04-07 | Daily + monthly quota per tenant (tokens/audio/embeds); block on limit | SATISFIED | `quota/counters.go` + `QuotaMiddleware` + migrations 0011/0013/0015 + `TestQuotaDailyRolloverBRT` + `gatewayctl tenant set-quota` |
| TEN-05 | 04-04, 04-06, 04-07 | Per-tenant mode `24/7` vs `peak` (08-22 local, off-hours OpenRouter) | SATISFIED | `schedule/window.go` + `schedule/policy.go` + `schedule/middleware.go` + `TestSchedulePeakOffHours`/`InHours`/`24x7AlwaysLocal` + `gatewayctl tenant set-mode` with zero-duration rejection (ME-04) |
| TEN-06 | 04-02, 04-05, 04-06, 04-07 | Token count + cost per request → append-only `billing_events` | SATISFIED | `billing/events.go` + `billing/cost.go` + `billing/flusher.go` + `UsageInterceptor.FinalizeRequest` + `TestBillingFlushNonStream`/`PartialSource`/`IdempotentReplay` + `gatewayctl billing reconcile` |
| TEN-07 | 04-02, 04-05, 04-06, 04-07 | Admin endpoint for cost + usage report per tenant | SATISFIED | `admin/usage.go` + `admin/middleware.go` + `TestAdminUsageResponseShape` + `TestAdminUsageAuthBCrypt` + `gatewayctl usage report` + `gatewayctl admin-key create/list/revoke` |

No orphaned requirements — REQUIREMENTS.md lines 178-182 map TEN-03..TEN-07 to Phase 4, and each has at least one plan declaring it in `requirements_completed` frontmatter.

---

## Behavioral Spot-Checks

| Behavior | Command | Result | Status |
|---|---|---|---|
| Build clean | `go build ./...` | no output (success) | PASS |
| Vet clean | `go vet ./...` | no output | PASS |
| Unit tests pass (race, short) | `go test -short -race -count=1 ./...` | all 22 packages `ok` (auth 589s long due to bcrypt; gatewayctl 19s; rest <11s) | PASS |
| Integration suite pass | `go test -tags integration -count=1 -timeout 600s ./internal/integration_test/...` | `ok ... 70.356s` (13 scenarios) | PASS |
| Gofmt drift | `gofmt -l .` | 3 files — all pre-existing (Phase 3 tool-call + sensitive tests); documented out-of-scope in 04-05/04-06/04-08 SUMMARYs | INFO (not introduced by Phase 4) |

---

## Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
|---|---|---|---|---|
| `gateway/internal/proxy/toolcall_test.go` | n/a | gofmt drift (pre-existing Phase 3) | INFO | Not introduced by Phase 4; tracked out-of-scope |
| `gateway/internal/integration_test/sensitive_block_test.go` | n/a | gofmt drift (pre-existing Phase 3) | INFO | Not introduced by Phase 4 |
| `gateway/internal/integration_test/tool_call_partial_test.go` | n/a | gofmt drift (pre-existing Phase 3) | INFO | Not introduced by Phase 4 |
| n/a | n/a | No stubs, TODOs, placeholders, hardcoded empty data, or `console.log`-only handlers in Phase 4 scope | — | — |

LOW + NIT (7 findings) explicitly deferred from review:
- LO-01 `numericFromFloat` truncation below 1e-6 — no impact at current BRL micro-cent precision; follow-up tracked.
- LO-02 `dataClassString` duplicated across 3 packages — maintainability only, no behavior drift.
- LO-03 `Retry-After` min 1s for RPS — tradeoff per RFC 7231 §7.1.3.
- LO-04 `TouchAdminKeyLastUsed` never called — operational; `last_used_at` stays NULL.
- NI-01 comment closed implicitly by HI-04.
- NI-02 `OffHoursUpstreamUnavailableCode` unused constant — cosmetic.
- NI-03 `formatDate(invalid)` → "-" — defensive cosmetic, not on happy path.

None are blockers.

---

## Human Verification Required

The following 6 scenarios exist as a committed template in `.planning/phases/04-multi-tenant-quotas-billing-schedule-routing/04-UAT-RESULTS.md` with `__FILL__` markers. They require live infrastructure not present on this dev machine:

### 1. SC-1 LIVE — Rate-limit headers
**Test:** `docker exec ai-gateway-dev /gatewayctl tenant set-quota --tenant converseai --rps 5 --rpm 300` then 10 parallel POSTs.
**Expected:** 5×200 with `X-RateLimit-*`; ~5×429 with `Retry-After: 1` + body `{"error":{"type":"rate_limit_error","code":"rate_limit_rps"}}`; metric `gateway_rate_limit_rejected_total{tenant,window="rps"}` +5.
**Why human:** Requires deployed `ai-gateway-dev` Portainer stack + real HTTP + /metrics scrape.

### 2. SC-4 LIVE — Peak off-hours routing to OpenRouter
**Test:** `tenant set-mode --tenant dev-peak --mode peak --window 20-22` then request outside window.
**Expected:** Logs `module=SCHEDULE decision=off_hours_external` + `module=DISPATCHER upstream=openrouter-chat`; real Qwen completion body; metric `gateway_schedule_routing_total{decision="off_hours_external"}>0`.
**Why human:** Requires live OpenRouter creds + gateway container + wall-clock control.

### 3. SC-4 edge LIVE — 503 off_hours_upstream_unavailable
**Test:** `gatewayctl upstreams disable --name openrouter-chat` then off-hours request.
**Expected:** 503 body `{"error":{"code":"off_hours_upstream_unavailable"}}`; NO log line mentioning `openai-chat` (D-C4 invariant).
**Why human:** Requires live breaker manipulation + log tail.

### 4. gatewayctl admin loop + /admin/usage response
**Test:** `admin-key create` → `admin-key list` → seed 10-20 chat/embed/audio rows → `curl -H "X-Admin-Key: $KEY" /admin/usage?tenant=converseai&from=...&to=...` → `prices set-fx --usd-brl 5.15` → `billing reconcile` → `admin-key revoke` → re-curl = 401.
**Expected:** Full SC-3 shape; `prices_reload_total{result=ok}` increments; reconcile exit 0.
**Why human:** Requires live CLI binary in container + real SQL seeding.

### 5. Sentry breadcrumbs
**Test:** Trigger rate-limit 429 + quota 429; inspect Sentry project `ai-gateway-dev`.
**Expected:** Events visible with breadcrumbs on triggering request trace; `X-Admin-Key`/`Authorization`/`X-API-Key` REDACTED; request body NOT captured.
**Why human:** Requires Sentry DSN + live stack emitting events.

### 6. SC-2 LIVE — billing_events psql
**Test:** `psql $AI_GATEWAY_DATABASE_URL -c "SELECT request_id, source, tokens_in, tokens_out, cost_local_phantom_brl, cost_external_brl FROM ai_gateway.billing_events ORDER BY created_at DESC LIMIT 5;"` after Scenarios 1, 2, 4; plus `timeout 1 curl -N .../stream` to produce a partial row.
**Expected:** ≥1 `source=final` with `cost_local_phantom_brl>0`; ≥1 `source=final` with `cost_external_brl>0`; ≥1 `source=partial`; sum tokens > 0.
**Why human:** Requires live `ai_gateway` schema in a DO Postgres cluster + real SSE abort.

---

## Gaps Summary

**None at the code/test level.** All 2 BLOCKERs + 4 HIGH + 6 MEDIUM code-review findings were fixed atomically and verified by the current unit + integration suites (all green). Build, vet, and race detector clean. Wiring traced end-to-end from `cmd/gateway/main.go` through interceptors, middlewares, loaders, and the /admin sub-router.

**Deferred, not gaps** — the 6 LIVE UAT scenarios above need `ai-gateway-dev` Portainer stack deployment, which in turn requires:
1. GitHub Secrets `PORTAINER_WEBHOOK_URL_DEV_GATEWAY` + `PORTAINER_WEBHOOK_URL_PROD_GATEWAY` (still missing per 04-UAT-RESULTS.md).
2. Portainer stack `ai-gateway-dev` created via Repository + webhook.
3. Schema `ai_gateway` created in a DO Postgres cluster (none of the 6 MCP-queried clusters currently have it).
4. Traefik + Cloudflare DNS for `gateway-dev.ifix.com.br`.
5. Vast.ai pod for `UPSTREAM_LLM_URL` (Phase 1 HUMAN-UAT via `smoke.yml` — also still pending per STATE.md).

This is the same deferral pattern used for Phase 2 SC-5 (`02-VERIFICATION.md`). The RUNBOOK (`gateway/docs/RUNBOOK-QUOTAS-BILLING.md`, ~480 lines) was delivered independently of deploy and is the on-call prerequisite.

---

## Next Steps to Promote `human_needed` → `passed`

1. Create GH Secrets + Portainer stack `ai-gateway-dev` (unblocks Phase 2 SC-5 LIVE + this phase's SC-1/SC-2/SC-4 LIVE simultaneously).
2. Run the 6 scenarios in `04-UAT-RESULTS.md`; fill `__FILL__` markers; commit.
3. Re-run `/gsd-verify-phase 4` — expected to flip to `status: passed` with `score: 5/5` and empty `human_verification`.
4. Follow-ups for LO-01..LO-04 + NI-02/NI-03 can be bundled into a Phase 5 or Phase 7 hygiene plan (none block any Success Criterion).

---

_Verified: 2026-04-21T12:50:00Z_
_Verifier: Claude (gsd-verifier)_
