---
phase: 4
slug: multi-tenant-quotas-billing-schedule-routing
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-04-20
---

# Phase 4 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | Go stdlib `testing` + `testcontainers-go` (Postgres 16 + Redis 7) — Phase 2 harness extends in `gateway/internal/integration_test/` |
| **Config file** | `gateway/go.mod` (deps); no separate config |
| **Quick run command** | `go test -short ./gateway/...` |
| **Full suite command** | `go test -race ./gateway/...` |
| **Estimated runtime** | ~20s warm (short), ~60s cold (full incl. testcontainers boot) |

---

## Sampling Rate

- **After every task commit:** Run `go test -race -short ./gateway/internal/<touched-package>`
- **After every plan wave:** Run `go test -race ./gateway/...`
- **Before `/gsd-verify-work`:** Full suite must be green
- **Max feedback latency:** 60 seconds

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 04-XX-XX | rate-limit | 2 | TEN-03 | T-04-01 | RPS=20: 21 reqs in 1s → 21st returns 429 `rate_limit_rps` + `Retry-After` | integration | `go test -run TestRateLimitRPS ./gateway/internal/quota` | ❌ W0 | ⬜ pending |
| 04-XX-XX | rate-limit | 2 | TEN-03 | T-04-01 | RPM=600: 601 reqs in 60s → 601st returns 429 `rate_limit_rpm` | integration | `go test -run TestRateLimitRPM ./gateway/internal/quota` | ❌ W0 | ⬜ pending |
| 04-XX-XX | rate-limit | 2 | TEN-03 / SC-5 | T-04-01 | 1000 goroutines / rps=100 → exactly 100 pass, 900 → 429 (Lua atomic) | integration (concurrent) | `go test -race -run TestRateLimitAtomic1000Concurrent ./gateway/internal/quota` | ❌ W0 | ⬜ pending |
| 04-XX-XX | quota | 2 | TEN-04 | T-04-02 | Daily tokens quota: (N-1) pass, N blocked with `quota_exceeded_daily_tokens` | integration | `go test -run TestQuotaDailyTokens ./gateway/internal/quota` | ❌ W0 | ⬜ pending |
| 04-XX-XX | quota | 2 | TEN-04 | T-04-02 | Monthly quota enforced (`quota_exceeded_monthly_tokens`) | integration | `go test -run TestQuotaMonthlyTokens ./gateway/internal/quota` | ❌ W0 | ⬜ pending |
| 04-XX-XX | quota | 2 | TEN-04 | T-04-02 | Daily rollover at 00:00 BRT — yesterday's quota does NOT block today | integration (fake clock) | `go test -run TestQuotaDailyRolloverBRT ./gateway/internal/quota` | ❌ W0 | ⬜ pending |
| 04-XX-XX | quota | 2 | TEN-04 | T-04-03 | Quota fail-closed — Postgres usage lookup down → 503 `quota_check_unavailable` | integration | `go test -run TestQuotaFailClosed ./gateway/internal/quota` | ❌ W0 | ⬜ pending |
| 04-XX-XX | rate-limit | 2 | TEN-03 | T-04-04 | Rate-limit fail-open — Redis EVALSHA transport error → request passes; metric `gateway_rate_limit_check_failures_total` incremented | integration | `go test -run TestRateLimitFailOpenOnRedisError ./gateway/internal/quota` | ❌ W0 | ⬜ pending |
| 04-XX-XX | schedule | 3 | TEN-05 / SC-4 | T-04-05 | Peak mode + off-hours → dispatcher selects `tier=1` (`openrouter-chat`) — skips local even if breaker CLOSED | integration | `go test -run TestSchedulePeakOffHours ./gateway/internal/schedule` | ❌ W0 | ⬜ pending |
| 04-XX-XX | schedule | 3 | TEN-05 | T-04-05 | 24/7 mode → always tier=0 regardless of clock | integration | `go test -run TestSchedule24x7 ./gateway/internal/schedule` | ❌ W0 | ⬜ pending |
| 04-XX-XX | schedule | 3 | TEN-05 | T-04-06 | sensitive+peak CHECK constraint rejects raw INSERT/UPDATE | integration (testcontainers PG) | `go test -run TestSensitivePeakCheckConstraint ./gateway/internal/tenants` | ❌ W0 | ⬜ pending |
| 04-XX-XX | schedule | 3 | TEN-05 | T-04-06 | sensitive+peak boot-time invariant → `slog.Error` + `os.Exit(1)` | unit (mock query) | `go test -run TestBootInvariant ./gateway` | ❌ W0 | ⬜ pending |
| 04-XX-XX | gatewayctl | 4 | TEN-05 | T-04-06 | `gatewayctl tenant set-mode --mode peak` rejects sensitive tenants pre-DB | unit (CLI) | `go test -run TestGatewayctlSetMode_RejectSensitivePeak ./gateway/cmd/gatewayctl` | ❌ W0 | ⬜ pending |
| 04-XX-XX | schedule | 3 | TEN-05 | — | Off-hours external upstream down → 503 `off_hours_upstream_unavailable` (no fallback to OpenAI direct chat — Phase 3 D-C4) | integration | `go test -run TestOffHoursExternalDown ./gateway/internal/schedule` | ❌ W0 | ⬜ pending |
| 04-XX-XX | billing | 3 | TEN-06 / SC-2 | — | Non-streaming flush: request completes → row in `billing_events` with tokens_in/out + cost columns | integration | `go test -run TestBillingFlushNonStream ./gateway/internal/billing` | ❌ W0 | ⬜ pending |
| 04-XX-XX | billing | 3 | TEN-06 | — | Streaming SSE usage parser: OpenAI shape (final chunk, empty `choices[]`) | unit (parser) | `go test -run TestUsageExtractorOpenAIShape ./gateway/internal/proxy` | ❌ W0 | ⬜ pending |
| 04-XX-XX | billing | 3 | TEN-06 | — | Streaming SSE usage parser: llama.cpp shape (`choices[0].finish_reason=stop` + `usage` in same chunk) | unit (parser) | `go test -run TestUsageExtractorLlamaCppShape ./gateway/internal/proxy` | ❌ W0 | ⬜ pending |
| 04-XX-XX | billing | 3 | TEN-06 | — | Streaming abnormal close → row written with `source='partial'` and tokens captured up to disconnect | integration | `go test -run TestBillingAbnormalClose ./gateway/internal/billing` | ❌ W0 | ⬜ pending |
| 04-XX-XX | billing | 3 | TEN-06 | T-04-07 | Replay retries idempotent — same `request_id` → exactly 1 `billing_events` row, `usage_counters` incremented once (CTE) | integration | `go test -run TestBillingIdempotentReplay ./gateway/internal/billing` | ❌ W0 | ⬜ pending |
| 04-XX-XX | billing | 3 | TEN-06 | T-04-07 | `usage_counters` stays consistent with `SUM(billing_events)` on replay (CTE prevents double-count) | integration | `go test -run TestUsageCountersCTEConsistency ./gateway/internal/billing` | ❌ W0 | ⬜ pending |
| 04-XX-XX | gatewayctl | 4 | TEN-06 | — | `gatewayctl billing reconcile` detects drift > 0.1% | integration | `go test -run TestBillingReconcileDrift ./gateway/cmd/gatewayctl` | ❌ W0 | ⬜ pending |
| 04-XX-XX | billing | 3 | TEN-06 | — | `cost_external_brl=0` when upstream=tier-0; `cost_local_phantom_brl=0` when upstream=tier-1 | integration | `go test -run TestBillingCostColumnSplit ./gateway/internal/billing` | ❌ W0 | ⬜ pending |
| 04-XX-XX | billing | 3 | TEN-06 | — | Price hot-reload via NOTIFY `prices_changed` — next flush uses new price | integration | `go test -run TestPricesHotReload ./gateway/internal/billing` | ❌ W0 | ⬜ pending |
| 04-XX-XX | billing | 3 | TEN-06 | — | fx hot-reload via NOTIFY | integration | `go test -run TestFXHotReload ./gateway/internal/billing` | ❌ W0 | ⬜ pending |
| 04-XX-XX | admin | 4 | TEN-07 / SC-3 | — | `GET /admin/usage` response shape exactly: `{tenant, range, summary, rows}` with all SC-3 fields | integration | `go test -run TestAdminUsageResponseShape ./gateway/internal/admin` | ❌ W0 | ⬜ pending |
| 04-XX-XX | admin | 4 | TEN-07 | T-04-08 | `GET /admin/usage` authenticates via `X-Admin-Key` bcrypt verify | integration | `go test -run TestAdminUsageAuthBCrypt ./gateway/internal/admin` | ❌ W0 | ⬜ pending |
| 04-XX-XX | admin | 4 | TEN-07 | T-04-08 | `GET /admin/usage` denies missing/invalid admin key (401) | integration | `go test -run TestAdminUsageUnauthorized ./gateway/internal/admin` | ❌ W0 | ⬜ pending |
| 04-XX-XX | gatewayctl | 4 | TEN-07 | T-04-08 | `gatewayctl admin-key create/revoke/list` round-trip | unit (CLI) | `go test -run TestGatewayctlAdminKey ./gateway/cmd/gatewayctl` | ❌ W0 | ⬜ pending |
| 04-XX-XX | middleware | 3 | TEN-03 / TEN-04 | — | Idempotency replay still consumes quota but skips rate-limit (D-D1) | integration | `go test -run TestMiddlewareChainReplaySemantics ./gateway/internal/integration_test` | ❌ W0 | ⬜ pending |
| 04-XX-XX | obs (folded) | 3 | — | — | Metrics middleware emits `obs.RequestsTotal{route,status}` per request | integration | `go test -run TestMetricsMiddleware ./gateway/internal/obs` | ❌ W0 | ⬜ pending |
| 04-XX-XX | timeouts (folded) | 3 | — | — | Per-route WriteTimeout: chat=0, embed=30s, audio=120s from env | unit | `go test -run TestPerRouteWriteTimeout ./gateway/internal/config` | ❌ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `gateway/internal/quota/enforcer_test.go` — rate-limit + quota middleware (TEN-03, TEN-04)
- [ ] `gateway/internal/quota/lua_test.go` — Lua script correctness incl. SC-5 1000 concurrent
- [ ] `gateway/internal/quota/counters_test.go` — `usage_counters` UPSERT CTE, fail-closed semantics
- [ ] `gateway/internal/billing/flusher_test.go` — idempotent INSERT + CTE prevents double-count
- [ ] `gateway/internal/billing/prices_test.go` — hot-reload via NOTIFY `prices_changed`
- [ ] `gateway/internal/billing/accountant_test.go` — on-emission atomic counter
- [ ] `gateway/internal/tenants/loader_test.go` — hot-reload + sensitive+peak CHECK
- [ ] `gateway/internal/schedule/policy_test.go` + `window_test.go` — `time.Location`, peak vs 24/7 windows
- [ ] `gateway/internal/admin/middleware_test.go` — bcrypt verify + Redis cache
- [ ] `gateway/internal/admin/usage_test.go` — SC-3 response shape
- [ ] `gateway/internal/proxy/interceptor_usage_test.go` — dual-shape SSE usage parsing (OpenAI + llama.cpp)
- [ ] `gateway/internal/integration_test/phase4_test.go` — end-to-end middleware chain
- [ ] `gateway/internal/integration_test/phase4_fixtures.go` — shared seed (tenants, prices, fx, admin key)
- [ ] `gateway/cmd/gatewayctl/{prices,billing,admin_key}_test.go` + `tenant_test.go` extensions

*Framework already installed (Go stdlib + testcontainers-go from Phase 2).*

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| 1000 concurrent goroutines against real Redis Ifix infra (network-latency races) | TEN-03 / SC-5 | testcontainers covers logic; only network-latency races require real infra | `go test -count=10 -race -run TestRateLimitAtomic1000Concurrent ./gateway/internal/quota` against dev Redis (`redis://infra-redis-1:6379`) |
| `gatewayctl billing reconcile --apply` against seeded drift | TEN-06 | One-shot operator command — no CI target; verifies drift correction loop | Seed PG with intentional `usage_counters` ≠ `SUM(billing_events)` mismatch; run `gatewayctl billing reconcile --from 2026-04-01 --to 2026-04-30 --apply`; verify counters match SUM after |
| Pricing seed values confirmed pre-migration | TEN-06 | Operator-gated (Apr 2026 pricing must come from live Fireworks/OpenRouter/OpenAI dashboards) | Wave 0 task — operator confirms in `gatewayctl prices set` before `0015_seed_prices_and_quotas.sql` runs |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 60s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
