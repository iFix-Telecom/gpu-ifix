# Phase 4: Multi-tenant Quotas, Billing & Schedule Routing - Research

**Researched:** 2026-04-20
**Domain:** Multi-tenant rate-limiting (Redis Lua atomic) + quota enforcement + cost attribution with streaming + price table hot-reload + schedule-based upstream routing
**Confidence:** HIGH overall (all load-bearing external facts verified or cited; gaps flagged)

## Summary

Phase 4 transforms the resilient multi-upstream gateway from Phase 3 into a multi-tenant gateway with economic isolation. The core technical surface is well-understood — Redis Lua token-bucket + Postgres partitioned append-only billing + `httputil.ReverseProxy.ModifyResponse` interceptor for streaming usage + `pgxlisten`-multiplexed hot-reload — all patterns either already live in the codebase (audit pipeline, upstreams loader, Redis cache) or are textbook (Stripe Lua, OpenAI SSE usage chunk).

Three findings are load-bearing for the plan:

1. **Fireworks does NOT list "Qwen 3.5 27B" in their serverless catalog as of Apr 2026.** Fireworks lists Qwen3 8B, Qwen3 30B, Qwen3 VL 30B, Qwen3 Max, Qwen3 Coder Plus, Qwen3.6 Plus — but no "Qwen 3.5 27B". The OpenRouter model page (`openrouter.ai/qwen/qwen3.5-27b`) DOES exist and reports **$0.195 / 1M input, $1.56 / 1M output** at the top-level aggregate. The Bifrost cost calculator reports $0.30/$2.40. This conflict is an operator-resolvable price-seed question — pricing seed must be confirmed ONE MORE TIME by operator against the live OpenRouter provider page for Qwen3.5-27B the day migration 0015 runs. **Pricing values below are tagged `[CITED]` but the seed is operator-gated.**

2. **llama.cpp's streaming-with-usage shape differs from OpenAI's.** llama.cpp emits `usage` in the *same* chunk as `finish_reason=stop` (with empty `delta`); OpenAI emits `usage` in a *separate* final chunk with empty `choices[]`. This is github issue [#15443](https://github.com/ggml-org/llama.cpp/issues/15443), unresolved as of Aug 2025. OpenRouter matches OpenAI. **The Phase 4 SSE interceptor MUST tolerate both shapes.**

3. **`CURRENT_DATE AT TIME ZONE 'America/Sao_Paulo'` is invalid SQL.** `DATE` has no timezone component, so AT TIME ZONE is a syntax error. The correct idiom is `(now() AT TIME ZONE 'America/Sao_Paulo')::date`. CONTEXT.md D-B1 uses the invalid form — every `usage_counters` query and the daily rollover logic must use the correct expression. Flag this for the planner.

Everything else is standard: Stripe/Cloudflare canonical Lua token bucket, Postgres 16 supports `ON CONFLICT` on RANGE-partitioned tables when the conflict target is part of the partition key (which `(request_id, ts)` is), `pgxlisten` multiplexes N channels over one connection (existing `listen.go` uses it — just add more `listener.Handle` calls), bcrypt cost 10 is ~86ms/verify (fine for low-frequency admin path, cached in Redis 60s).

**Primary recommendation:** Execute the 7 decisions in CONTEXT.md with minor corrections — the timezone SQL expression, the split Lua script for RPS+RPM (Stripe's canonical returns only `{allowed, remaining}`; we need `{allowed, remaining, reset_ms}` so script must be extended), the dual-shape SSE interceptor, and the operator-gated price seed confirmation.

## User Constraints (from CONTEXT.md)

### Locked Decisions

All decisions marked **D-*** in CONTEXT.md `<decisions>` are locked:

**Rate-limit + quota (D-A1..A4):**
- D-A1: Redis Lua atomic token bucket for RPS+RPM; namespaced `gw:rate:{tenant_id}:{route_class}:{window}`; route_class ∈ {chat, embed, stt}; window ∈ {rps, rpm}
- D-A2: Rate-limit **fail-open** on Redis transport error; quota **fail-closed** with 503 `quota_check_unavailable`; audit entry `upstream='quota_check_unavailable'`
- D-A3: Rollover timezone `America/Sao_Paulo`; one `*time.Location` loaded at boot
- D-A4: Discriminated error codes (`rate_limit_rps`, `rate_limit_rpm`, `quota_exceeded_daily_tokens`, `quota_exceeded_daily_audio_minutes`, `quota_exceeded_daily_embeds`, `quota_exceeded_monthly_*` ×3); OpenAI envelope (`type='rate_limit_error'` or `type='insufficient_quota'`)

**Billing schema & cost (D-B1..B4):**
- D-B1: New `billing_events` table append-only, `PARTITION BY RANGE (ts)` monthly, PK `(request_id, ts)`, `INSERT ON CONFLICT DO NOTHING` idempotency; `usage_counters` evolves (ADD COLUMN) as daily cache
- D-B2: Streaming on-emission counter in-process, flush in `defer { flushBilling() }`, SOURCE='final' OR 'partial'
- D-B3: Prices table + fx_rates table with hot-reload via LISTEN/NOTIFY `prices_changed`
- D-B4: `cost_local_brl=0` always (GPU is fixed-cost); `cost_local_phantom_brl` = tokens × openrouter-fireworks unit cost; `cost_external_brl` = real external cost

**Schedule routing (D-C1..C4):**
- D-C1: `tenants` ALTER ADD `mode`, `peak_window_start/end`, `schedule_timezone`; CHECK constraint `chk_sensitive_no_peak`
- D-C2: Off-hours peak routes direct to `openrouter-chat` tier-1 (skip tier-0 even if CLOSED); 503 `off_hours_upstream_unavailable` if both down (no fallback-of-fallback per Phase 3 D-C4)
- D-C3: Triple defense (gatewayctl + CHECK + boot-time fail-fast)
- D-C4: In-memory cache of tenants config + hot-reload via NOTIFY `tenants_changed`

**Admin surface (D-D1..D4):**
- D-D1: Middleware order `auth → idempotency → rate-limit → quota → schedule → tokencount → dispatcher → billing-flush`
  - Idempotency replay consumes quota but not rate-limit
  - Rate-limit rejection does not consume quota
  - Schedule runs only if quota passes (no routing leak on blocked requests)
- D-D2: `GET /admin/usage?tenant=X&from=Y&to=Z&granularity=day|month` — query `billing_events` direct (authoritative), not `usage_counters`
- D-D3: `X-Admin-Key: ifix_admin_<hex>` bcrypt cost 10, 60s Redis cache, bootstrap via `AI_GATEWAY_ADMIN_KEY_BOOTSTRAP` env
- D-D4: Suite of `gatewayctl` subcommands: `tenant set-mode`, `tenant set-quota`, `prices set|list|set-fx`, `billing reconcile`, `usage report`, `admin-key create|revoke|list`

**Folded from STATE.md:**
- Wire `obs.RequestsTotal.WithLabelValues(route, status).Inc()` at end of middleware chain
- Restore per-route `WriteTimeout` (chat=0, embed=30s, audio=120s)

### Claude's Discretion

Explicitly delegated:
- Lua script shape (KEYS/ARGV/return) — this research lands the recommendation
- `X-RateLimit-*` header shape (OpenAI-compat)
- Quota defaults seed values (`daily_tokens=10M`, `monthly_tokens=300M`, etc.)
- Pricing seed values — **research CONFIRMS/FLAGS per below**
- Audit log `upstream` values for rejections (`rate_limited`, `quota_exceeded`, etc.)
- Per-route WriteTimeout config via env vars
- Metrics middleware placement
- Package layout (`quota/`, `billing/`, `tenants/`, `schedule/`, `admin/`)
- Migrations 0010..0015 numbering + content
- sqlc query files organization
- Whether to unify loaders or keep 3 separate (prices, fx_rates, tenants) — **recommendation below**

### Deferred Ideas (OUT OF SCOPE)

Per CONTEXT.md `<deferred>` — do NOT plan tasks for these:
- Per-tenant inflight fairness (→ Phase 5)
- Fallback-of-fallback off-hours → OpenAI direct chat (rejected — Qwen fixo)
- Per-tenant pricing override (v1 = global by model,provider,unit)
- Auto-rotate USD/BRL fx via external API (manual via CLI)
- Cost reconciliation vs external invoice (→ Phase 7/10)
- Quota soft-warning thresholds (→ Phase 7)
- Multiple peak windows per tenant (→ Phase 9 if needed)
- Idempotency-Key on `/v1/embeddings` and `/v1/audio/transcriptions` (still deferred)
- Per-route P95/P99 histograms with tenant label (→ Phase 7, cardinality budget OBS-02)
- Better Auth/SSO admin (→ Phase 10 PRD-06)
- Audit cold-storage export (→ Phase 7/10)
- Phantom cost for STT/embed local (OK — D-B4 implements this)
- Char-count fast-path (still deferred)
- Per-app breaker trip (→ Phase 5)

## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| TEN-03 | Rate limiting per API key (RPS and RPM) using Redis Lua atomic | D-A1 Lua script shape below (§Code Examples); SC-5 covered by Redis Lua's single-threaded atomic guarantees — 1000 concurrent goroutines observe exactly N allowed per window |
| TEN-04 | Daily + monthly quotas per tenant (tokens, audio-minutes, embeds); block at limit | Postgres `usage_counters` UPSERT in same txn as `billing_events` INSERT; daily rollover via `(now() AT TIME ZONE 'America/Sao_Paulo')::date` primary-key column; 6 quota dimensions (3 × {daily, monthly}); discriminated error codes per D-A4 |
| TEN-05 | Per-tenant mode `24/7` OR `peak` (08-22h local → OpenRouter outside) | D-C1 ALTER TABLE + CHECK; D-C2 schedule middleware pre-dispatch; D-C3 triple defense for sensitive+peak; one `time.Location` loaded at boot and reused |
| TEN-06 | Token counting + BRL cost per request in `billing_events` (append-only) | D-B1 partitioned table + idempotent INSERT; D-B2 on-emission streaming accounting via SSE interceptor; D-B3 prices hot-reload; D-B4 phantom + external cost columns; reuse Phase 3 `TokenCounter` fallback when upstream omits `usage` |
| TEN-07 | `GET /admin/usage` report endpoint | D-D2 query-from-billing-events (not cache) shape; D-D3 bcrypt admin key; response shape in CONTEXT.md lines 166-182 matches SC-3 exactly |

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Rate-limit check (RPS/RPM) | Redis (Lua) | — | Single-threaded atomic is the only way to guarantee zero over-use under 1000 concurrent (SC-5). In-process `x/time/rate` cannot coordinate across gateway replicas (Phase 6 will have 2). |
| Quota lookup | Postgres `usage_counters` (PK read) | — | Daily row is a single indexed lookup, <1ms in DO Postgres warm. `billing_events` is authoritative for reports but too slow for hot-path quota check (full partition scan). |
| Quota update (increment) | Postgres `usage_counters` (UPSERT in billing flush txn) | — | Same transaction as `billing_events` INSERT guarantees counter ≤ Σ(events). Atomic. |
| Billing event persist | Postgres `billing_events` (partitioned, append-only) | — | Single writer pattern — async batched via goroutine mirror of audit pipeline (D-B4 shape). |
| Price lookup (cost calc) | In-process map (hot-reloaded via NOTIFY) | — | Zero RTT on hot path. Reload triggered by `prices_changed`. |
| Schedule decision (mode=peak window check) | In-process tenants config (hot-reloaded) | — | Zero RTT; `time.Now().In(tz)` and window check is nanoseconds. |
| Admin key verify | In-process LRU + Redis cache (60s) + Postgres bcrypt | — | Same pattern as Phase 2 D-A2 API keys. Low frequency; 86ms bcrypt acceptable on cache miss. |
| Tenants config | Postgres `tenants` | In-memory snapshot + LISTEN/NOTIFY | Mirror of Phase 3 D-D4 upstreams loader. |
| Prices config | Postgres `prices` | In-memory snapshot + LISTEN/NOTIFY | Mirror; operator edits via CLI `gatewayctl prices set`. |
| FX rates | Postgres `fx_rates` | In-memory snapshot + LISTEN/NOTIFY | Same mirror. Operator `gatewayctl prices set-fx`. |
| Admin keys | Postgres `admin_keys` | Redis cache (60s) | Low frequency; read-heavy. |
| Cost calculation | In-process pure func (tokens × price × fx) | — | No I/O; consumes cached price + fx maps. |
| SSE usage extraction | In-process interceptor (`ModifyResponse`) extends Phase 3 tool-call detector | Phase 3 `TokenCounter` (fallback) | `delta.usage` parsed per chunk; fallback re-tokenize via `/tokenize` + `tokencount.go`. |

## Standard Stack

### Core

All already present in go.mod from Phase 2/3 — Phase 4 adds **zero new top-level deps**.

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `github.com/redis/go-redis/v9` | v9.x (already in go.mod) | Lua script execution via `redis.NewScript(src)` + `.Run(ctx, client, keys, args...)` | `NewScript` auto-handles EVALSHA → EVAL fallback; no manual SCRIPT LOAD needed [CITED: https://context7.com/redis/go-redis/llms.txt §Lua Scripting] |
| `github.com/jackc/pgx/v5` | v5.x (already) | pgxpool + Tx for atomic `billing_events` INSERT + `usage_counters` UPSERT | Same pool shared with audit writer; new `billing/dbflush.go` follows `audit/writer.go` shape |
| `github.com/jackc/pgxlisten` | already (Phase 3 in go.sum) | Multiplexed LISTEN on single conn | `listener.Handle(channel, handler)` supports arbitrarily many channels [CITED: https://pkg.go.dev/github.com/jackc/pgxlisten] |
| `github.com/pressly/goose/v3` | already | 6 new migrations 0010..0015 | Embedded via //go:embed in main.go |
| `github.com/sqlc-dev/sqlc` (tool) | already | codegen for billing.sql, usage_counters.sql, prices.sql, fx_rates.sql, tenants_admin.sql, admin_keys.sql | |
| `golang.org/x/crypto/bcrypt` | already (Phase 2 uses argon2id for api_keys; bcrypt is new for admin_keys) | Admin key hashing cost=10 (~86ms verify on entry VPS — acceptable low-frequency) [CITED: https://github.com/nsmithuk/bcrypt-cost-go] | v1 admin is 1-3 operators; Redis 60s cache means bcrypt hits once per minute per key |
| `github.com/prometheus/client_golang` | already | 9 new collectors (listed in CONTEXT.md Plumbing) | |

### Supporting

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `github.com/google/uuid` | already | uuidv7 for new `billing_events.request_id` (same as audit_log) | Phase 2 convention |
| `github.com/testcontainers/testcontainers-go` | already | Integration test Postgres 16 + Redis 7 harness | SC-5 concurrent test; rollover boundary test |

### Alternatives Considered

| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| Redis Lua token bucket | `github.com/go-redis/redis_rate` (GCRA leaky-bucket in go-redis/extra) | GCRA forces even spacing, no burst allowance — wrong for RPS (first request should pass, not block for 1/rate seconds) [CITED: research/STACK.md §Resilience `uber-go/ratelimit`]; Lua token bucket gives bursting correctly |
| In-process breaker mirror (Phase 3) | Redis-distributed-lock for quota check | Rejected — daily counter is not a lock, it's a counter; Postgres UPSERT atomic in same txn is simpler |
| `pg_partman` extension | Manual partition DDL in migration + cron | pg_partman has [known ON CONFLICT bugs on partitioned tables](https://github.com/pgpartman/pg_partman/issues/105) and may not be installed in DO managed Postgres. Phase 2 already uses manual partitions for audit_log — keep consistent. [CITED: crunchydata blog] |
| Separate loaders (prices, fx_rates, tenants) | Unified `internal/config/registry.go` multiplexing NOTIFY | Recommendation below: **keep 3 separate loaders**; reuse `listen.go` to multiplex all NOTIFY channels via single `pgxlisten.Listener.Handle()` calls. Isolation is cheap, trivial to test. |
| bcrypt cost 10 | argon2id (Phase 2 api_keys) | argon2id is better for memory-hardness but bcrypt cost 10 is sufficient for admin path; both are OWASP-acceptable 2026. Reuse bcrypt because `admin_keys` is a new table and argon2id requires param persistence in hash string which admin CLI code doesn't need |

**Installation (no new deps):**

```bash
# All deps already in go.mod. Verify versions are current on branch:
go list -m all | grep -E "go-redis|pgx|bcrypt|goose|sqlc"
```

**Version verification (performed 2026-04-20):**

- `github.com/redis/go-redis/v9` — latest stable per Context7 `/redis/go-redis` (benchmark score 78.57, 167 snippets) — [VERIFIED via Context7]
- `github.com/jackc/pgx/v5` v5.x (needs Go 1.25+ for latest per STACK.md §Data Access) — [VERIFIED via STACK.md]
- `golang.org/x/crypto/bcrypt` — stdlib-adjacent, stable — [VERIFIED via pkg.go.dev]

## Architecture Patterns

### System Architecture Diagram

```
  HTTP Request (chat | embed | audio)
         │
         ▼
  [ chi Router ]
         │  ├─ route template injected into ctx for metrics
         ▼
  [ authMiddleware ]                       ── Phase 2 D-A5 (unchanged)
         │  tenant_id, data_class, api_key_id → ctx
         ▼
  [ idempotencyMiddleware ]                ── Phase 2 D-C1..C5 (unchanged)
         │  replay hit? → short-circuit response; mark ctx.idempotency_replayed=true
         ▼                                          │
  [ rateLimitMiddleware ]  ◄───── Redis (Lua EVALSHA)│
         │  RPS check + RPM check atomic            │
         │  429 rate_limit_{rps|rpm} + X-RateLimit-*│
         │  replay? → SKIP rate-limit (D-D1 nuance)│
         ▼                                          │
  [ quotaMiddleware ]  ◄───── Postgres usage_counters (PK read)
         │  6 dimensions × {daily, monthly} = 6 rows consulted
         │  fail-closed on unavailable → 503 quota_check_unavailable
         │  replay? → STILL checks (idempotency consumes quota)
         ▼
  [ scheduleMiddleware ]  ◄───── in-memory tenants config
         │  mode=peak AND !inWindow() → force tier=1 upstream
         │  cfg.tz loaded once at boot (America/Sao_Paulo)
         │  decision written to ctx.upstream_override
         ▼
  [ tokencountMiddleware ]  ◄─── Phase 3 TokenCounter (unchanged for chat/embed)
         │  16k cap for chat, 8k cap for embed (unchanged)
         ▼
  [ dispatcher ]  ◄───────────── Phase 3 (Director, breaker, fallback chain)
         │                       respects ctx.upstream_override
         │  ModifyResponse interceptor chain (Phase 3 tool-call + NEW usage extractor)
         ▼
  Upstream Response (local-llm | openrouter-chat | openai-whisper | ...)
         │  SSE chunks flow; interceptor increments requestUsage atomic
         ▼
  [ client receives streamed response ]
         │
         │   ┌── defer {
         │   │     flushBilling(ctx, requestUsage) ← ALWAYS runs (normal or abnormal close)
         │   │     └─ Postgres tx {
         │   │          INSERT billing_events ON CONFLICT (request_id, ts) DO NOTHING
         │   │          INSERT usage_counters ... ON CONFLICT (tenant_id, date)
         │   │             DO UPDATE SET tokens_in=tokens_in+EXCLUDED.tokens_in, ...
         │   │        }
         │   │     source='final' if normal close; 'partial' if abnormal
         │   └── }
         ▼
  [ metricsMiddleware ]
         │  obs.RequestsTotal.WithLabelValues(route, statusClass).Inc()
         ▼
  End
```

### Recommended Project Structure

```
gateway/internal/
├── quota/
│   ├── scripts/
│   │   └── token_bucket.lua       # //go:embed source for redis.NewScript
│   ├── lua.go                     # NewScript wrapper + Run helper
│   ├── bucket.go                  # BucketConfig struct (capacity, refill_rate, window)
│   ├── enforcer.go                # http.Handler middleware (rate + quota check)
│   ├── counters.go                # Postgres quota lookup + UPSERT helpers
│   └── errors.go                  # ErrRateLimitRPS, ErrRateLimitRPM, ErrQuotaExceededDailyTokens, ...
├── billing/
│   ├── accountant.go              # requestUsage struct (atomic); extract on-emission
│   ├── prices.go                  # in-memory price map + USD→BRL conversion
│   ├── prices_loader.go           # Postgres SELECT + LISTEN prices_changed
│   ├── fx_loader.go               # Postgres SELECT + LISTEN fx_changed (same channel or split)
│   ├── flusher.go                 # Async batched writer (mirror of audit/writer.go)
│   ├── events.go                  # sqlc wrappers for billing_events UPSERT
│   ├── usage_counters.go          # sqlc wrappers for usage_counters UPSERT
│   └── errors.go
├── tenants/
│   ├── loader.go                  # Same shape as upstreams/loader.go
│   ├── listen.go                  # Delegates to shared listen.go OR own file
│   ├── config.go                  # TenantConfig struct (mode, peak_window, quotas, rps_limit)
│   └── errors.go
├── schedule/
│   ├── policy.go                  # DecideUpstreamTier(cfg, now) → tier
│   └── window.go                  # inWindow(now time.Time, start, end time.Time) bool
├── admin/
│   ├── middleware.go              # adminAuthMiddleware using admin_keys table + Redis cache
│   ├── usage.go                   # GET /admin/usage handler
│   ├── usage_queries.sql          # sqlc queries
│   └── errors.go
└── proxy/
    └── interceptor_usage.go       # NEW: extends Phase 3 interceptor.go for delta.usage + dual-shape (OpenAI vs llama.cpp)

gateway/db/migrations/
├── 0010_create_billing_events.sql       # partitioned table + 3 seed partitions
├── 0011_evolve_usage_counters.sql       # ADD COLUMNs: audio_seconds, embeds_count, cost_local_phantom_brl, cost_external_brl
├── 0012_create_prices_and_fx.sql        # prices + fx_rates + notify triggers
├── 0013_evolve_tenants_schedule_quota.sql # ADD COLUMNs: mode, peak_window_*, schedule_timezone, daily_quota_*, monthly_quota_*, rps_limit, rpm_limit + CHECK chk_sensitive_no_peak + notify trigger tenants_changed
├── 0014_create_admin_keys.sql
└── 0015_seed_prices_and_quotas.sql      # SEED values — operator-gated confirmation required (see Assumptions Log)

gateway/db/queries/
├── billing.sql                          # InsertBillingEvent ON CONFLICT DO NOTHING
├── usage_counters.sql                   # UpsertUsageCounter ... + DailyQuotaLookup (PK read)
├── prices.sql                           # ListPrices (by model,provider,unit), InsertPrice
├── fx_rates.sql                         # GetCurrentFX, InsertFX
├── tenants_admin.sql                    # UpdateTenantMode, UpdateTenantQuota, SelectSensitivePeakInvariant (boot-time validation)
└── admin_keys.sql                       # GetAdminKeyByPrefix, InsertAdminKey, RevokeAdminKey

gateway/cmd/gatewayctl/
├── tenant.go      (extend)              # add set-mode, set-quota subcommands
├── prices.go      (new)                 # set, list, set-fx, reconcile
├── billing.go     (new)                 # reconcile, usage report
└── admin-key.go   (new)                 # create, revoke, list
```

### Pattern 1: Redis Lua Token Bucket (RPS + RPM in ONE script)

**What:** A single EVALSHA-cached Lua script atomically checks + updates two token buckets per call. RPS and RPM share the script but use distinct Redis keys. Reasoning: a request either passes both windows or fails the tighter one; checking both atomically prevents a scenario where the RPS slot is consumed but the RPM check later denies the same request.

**When to use:** `rateLimitMiddleware` pre-dispatch. Called once per non-replayed request.

**Key design choices beyond CONTEXT.md:**

- **`{allowed_bool, remaining_rps, reset_rps_ms, remaining_rpm, reset_rpm_ms, which_failed}`** return shape. Stripe canonical only returns `{allowed, remaining}` — we extend to support the OpenAI-compat `X-RateLimit-*` headers and discriminated `rate_limit_rps` vs `rate_limit_rpm` codes.
- **SETEX TTL** = `math.floor(capacity / refill_rate * 2)` so abandoned keys self-evict.
- **`ARGV`** in order: `now_ms, rps_capacity, rps_refill_per_ms, rpm_capacity, rpm_refill_per_ms, requested_tokens`.
- **`KEYS`** = 4 keys: `gw:rate:{tenant}:{route_class}:rps:tokens`, `gw:rate:{tenant}:{route_class}:rps:ts`, `gw:rate:{tenant}:{route_class}:rpm:tokens`, `gw:rate:{tenant}:{route_class}:rpm:ts`.
- **Script load on boot:** `redis.NewScript(src)` caches the SHA in the `*Script` struct; `.Run` uses EVALSHA and transparently falls back to EVAL after Redis restart (script cache flushed on restart). No manual `SCRIPT LOAD` needed. [CITED: go-redis README + Context7 `/redis/go-redis`]

**Full Lua source (Example in §Code Examples below).**

### Pattern 2: Partitioned `billing_events` + idempotent INSERT

**What:** `CREATE TABLE billing_events (...) PARTITION BY RANGE (ts)` with PK `(request_id, ts)`. Monthly partitions seeded at migration time (0010) plus an upcoming 3-month window; a `gatewayctl billing partition-forward` cron job (deferred to Phase 7) rolls partitions forward.

**Why this works on PG16:** Postgres 11+ supports `INSERT ... ON CONFLICT (col1, col2) DO NOTHING` on RANGE-partitioned tables provided **the conflict target is a subset of (or equal to) the primary key AND the partition key is part of the PK.** Our PK `(request_id, ts)` includes `ts`, which is the partition key — valid. [CITED: postgresql.org docs §INSERT, dbi-services 2018 post on PG11 partitioned-table ON CONFLICT]

**Idempotency shape:**
```sql
INSERT INTO ai_gateway.billing_events (request_id, ts, tenant_id, api_key_id, route, upstream, model,
    tokens_in, tokens_out, audio_seconds, embeds_count,
    cost_local_brl, cost_local_phantom_brl, cost_external_brl, currency, source, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, DEFAULT)
ON CONFLICT (request_id, ts) DO NOTHING;
```

Retries (client retry + gateway internal retry) never duplicate. Pitfall 8 D-B1 requirement.

### Pattern 3: Streaming on-emission accounting with dual-shape SSE interceptor

**What:** The Phase 3 `ModifyResponse` interceptor already parses SSE deltas for tool-call detection. Phase 4 adds a second inspector that extracts `usage` from either:

- **OpenAI/OpenRouter shape:** separate final chunk `{"choices":[], "usage":{...}}` (empty choices array)
- **llama.cpp shape:** same chunk as finish_reason `{"choices":[{"finish_reason":"stop","delta":{},"index":0}], "usage":{...}}`

Both satisfy the contract: **if the SSE event JSON contains a top-level `"usage"` object, extract `prompt_tokens`/`completion_tokens`/`total_tokens` and atomically set the `requestUsage` struct.** The interceptor should not care about the position.

**When `usage` is absent in streaming:** fallback to Phase 3 `TokenCounter.Enforce()` post-hoc to estimate tokens_in from request body + count tokens_out from concatenated SSE chunks (expensive but rare).

**`stream_options.include_usage: true`:** The gateway MUST inject this into the request body when streaming is on, else OpenAI/OpenRouter don't emit the usage chunk. [CITED: https://developers.openai.com/api/docs/guides/streaming-responses + https://openrouter.ai/docs/api/reference/streaming §Usage Accounting] OpenRouter's own doc notes that `stream_options: { include_usage: true }` is "deprecated and has no effect; full usage details are now always included automatically in every response" — but including it is safe-by-default.

**llama.cpp:** does not honor `stream_options` but emits `usage` in final chunk always. Same extraction logic works.

### Pattern 4: Hot-reload via `pgxlisten` multiplexing 3 channels

**What:** Extend existing `gateway/internal/upstreams/listen.go` pattern. Single dedicated pgx connection; `pgxlisten.Listener.Handle("channel", handler)` called 3x for new channels:
- `tenants_changed` → `tenantsLoader.Refresh(ctx)` → publish `tenants_reload_total{result=ok}`
- `prices_changed` → `pricesLoader.Refresh(ctx)` + `fxLoader.Refresh(ctx)` → publish `prices_reload_total{result=ok}`
- `upstreams_changed` → already handled (Phase 3)

**Why one connection is safe:** `pgxlisten` multiplexes — it reads from the single LISTEN conn and dispatches to handlers by channel name. [CITED: https://pkg.go.dev/github.com/jackc/pgxlisten] Phase 3 `listen.go` shows the pattern; just add `listener.Handle("tenants_changed", ...)` and `listener.Handle("prices_changed", ...)` inside the Listener setup. **This contradicts my initial exploration: no split-loaders-for-multiple-conns decision is needed. One `Listener.Listen(ctx)` goroutine handles all 3 channels.**

**Plumbing recommendation:** Refactor `listen.go` into `gateway/internal/notifylisten/` package accepting a `map[string]pgxlisten.HandlerFunc` so Phase 4 doesn't duplicate listen boilerplate. Alternative: keep per-loader `listen.go` files but share the single `*pgxlisten.Listener` instance across them (pass it in via constructor). Either works; keep-3-separate is the cheaper diff.

### Pattern 5: Schedule pre-dispatch in hot path

**What:** `scheduleMiddleware` reads `ctx.tenant_config` (set by `authMiddleware` from loader cache) and decides `ctx.upstream_override`:

```go
// schedule/policy.go
func DecideUpstreamTier(cfg TenantConfig, now time.Time) int {
    if cfg.Mode == "24/7" {
        return 0  // always try local first; normal fallback chain applies
    }
    if cfg.Mode == "peak" {
        localNow := now.In(cfg.Location)
        if inWindow(localNow, cfg.PeakWindowStart, cfg.PeakWindowEnd) {
            return 0
        }
        return 1  // off-hours → skip tier-0 even if CLOSED
    }
    return 0
}
```

Decision: tier int placed in ctx. Dispatcher (Phase 3) already selects tier-0 or tier-1 based on breaker state; the `upstream_override` forces tier=1 selection regardless of tier-0 breaker state.

**Sensitive+peak invariant:** boot-time check via `q.CountSensitivePeakInvariant(ctx) > 0 → slog.Error + os.Exit(1)` before the HTTP server starts. Catches CHECK-constraint bypass via replication-role tricks.

### Anti-Patterns to Avoid

- **Don't check quota inside the Lua script.** Rate-limit is Redis-authoritative; quota is Postgres-authoritative. Keeping them separate is correct — mixing complicates the script and leaks cache coherence concerns to Lua.
- **Don't compute cost synchronously in the hot path.** Price map lookup is fine (map[string]float), but PLEASE don't INSERT into `billing_events` on the hot path — use buffered-channel flusher (same as audit writer). The `defer` in the dispatcher handler triggers the enqueue, not the write.
- **Don't use `CURRENT_DATE AT TIME ZONE ...` — invalid syntax.** Use `(now() AT TIME ZONE 'America/Sao_Paulo')::date`. CONTEXT.md D-B1 shows the wrong form. [CITED: postgresql.org §Date/Time Types]
- **Don't cache quota check results in Redis.** The quota check IS a PK read on `usage_counters` — fast enough. Adding a Redis cache creates a cache-coherence problem when flush updates the row; not worth it.
- **Don't strip `stream_options` from client requests.** Pass it through. If client didn't send it, gateway should inject `stream_options: {"include_usage": true}` when routing to OpenRouter/OpenAI so usage arrives in the stream. (llama.cpp ignores it but emits usage anyway.)
- **Don't re-create `*time.Location` per request.** Load once at boot; pass via config struct.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Atomic rate-limit check | Hand-rolled Redis GET+SET with WATCH/MULTI | `redis.NewScript(lua).Run(ctx, client, keys, args)` | EVALSHA fallback auto-handled by go-redis; Lua is single-threaded in Redis → atomic by construction |
| Token bucket math | Custom goroutine with time.Ticker refilling counters | Lua script refills on-read (`filled = min(cap, last_tokens + (now-last_ts)*rate)`) | Passive refill (Stripe pattern) has no separate goroutine to run across N tenants — computation happens on request |
| Partitioned table management | Custom Go code inserting into child partition by name | Native PG11+ partitioning with declarative parent INSERT | Postgres handles routing — INSERT into parent table lands in right partition |
| LISTEN/NOTIFY multiplexing | Dedicated pgx.Conn per channel × 3 channels | Single `pgxlisten.Listener` with N `Handle(channel, handler)` calls | Already pattern in Phase 3; scales to arbitrary channel count on one conn |
| bcrypt hashing/verify | Custom | `golang.org/x/crypto/bcrypt.GenerateFromPassword` + `.CompareHashAndPassword` | Stdlib-adjacent, constant-time compare baked in |
| USD → BRL FX | Live HTTP to BCB API on every request | `fx_rates` table hot-reloaded; CLI manual update weekly | External API dependency adds failure mode + latency; manual weekly update is operationally simpler for 6 tenants |
| Cost calc precision | `float64` arithmetic | `NUMERIC(12,8)` for unit price; `NUMERIC(10,6)` for cost_brl (Postgres); `big.Float` or fixed-point Go only if needed | Cents precision matters for accurate billing; float64 rounding can produce R$ 0.000000001 drift |

**Key insight:** Every heavy lift in Phase 4 has a canonical pattern in the Go or Postgres ecosystem — the gateway just wires them together. Phase 4 adds zero novel infrastructure.

## Runtime State Inventory

Phase 4 is primarily a greenfield schema additive — nothing is being renamed. But Phase 4 DOES evolve existing tables (`tenants`, `usage_counters`) and introduce new cross-process coordination (billing flush, prices hot-reload). The relevant "what runtime state persists?" questions:

| Category | Items Found | Action Required |
|----------|-------------|------------------|
| Stored data | (a) Existing `tenants` rows need default values for new columns (mode='24/7' default). (b) Existing `usage_counters` rows (likely empty but schema-only pre-Phase4) get new NULL columns — need DEFAULT 0 on ALTER to avoid broken aggregations. (c) `billing_events` is brand new. | (a) ALTER ADD COLUMN ... DEFAULT '24/7' NOT NULL — backfills existing rows. (b) ALTER ADD COLUMN ... DEFAULT 0 NOT NULL — backfills. (c) No migration data needed (new table). |
| Live service config | Operator will need to set `AI_GATEWAY_ADMIN_KEY_BOOTSTRAP` env var in Portainer stack BEFORE deploying Phase 4 binary. Otherwise bootstrap generates random key and logs warn (acceptable fallback but requires operator action to capture it from logs) | Document in runbook. Phase 4 plan should include operator-pre-deploy checklist entry. |
| OS-registered state | None — gateway runs in Docker; no systemd/cron/launchd registrations | None — verified by STATE.md Phase 2 deploy pattern (Docker Compose + Portainer only) |
| Secrets / env vars | **NEW:** `AI_GATEWAY_ADMIN_KEY_BOOTSTRAP`, `AI_GATEWAY_USD_BRL_RATE_DEFAULT=5.10` (fallback if fx_rates row absent), `AI_GATEWAY_RATE_LIMIT_FAIL_OPEN=true`, `AI_GATEWAY_QUOTA_FAIL_OPEN=false`, `GATEWAY_WRITE_TIMEOUT_{CHAT,EMBED,AUDIO}_S` | Operator must set in Portainer stack environment **before** deploy. Document in README + runbook. |
| Build artifacts / installed packages | Gateway binary re-embeds migrations 0010..0015 via `//go:embed`. Existing running binary (Phase 3) will NOT auto-apply new migrations until redeploy; `AI_GATEWAY_MIGRATE_ON_BOOT=true` in Portainer ensures application | No action — existing boot-migrate flag (Phase 2 Plan 02-08) handles this automatically |

**Nothing found in category:** OS-registered state — verified by grep of Phase 1/2/3 code; Docker Compose is the only registration surface.

## Common Pitfalls

### Pitfall 1: Rate-limit Lua script works per-key but races across replicas when Redis restarts

**What goes wrong:** Two gateway processes each cached the SHA at boot. Redis restarts → script cache cleared → first EVALSHA on each replica fails with `NOSCRIPT`, go-redis auto-retries with EVAL → OK. But during the ~100ms window between first request post-restart and cache repopulation, rate-limit still works (each EVAL is fresh) — no actual race.

**Why safe:** `redis.NewScript` holds the script source AND the SHA; `.Run` tries EVALSHA first, falls back to EVAL on `NOSCRIPT` transparently. [CITED: Context7 `/redis/go-redis` §Lua Scripting — "Scripts are automatically handled via EVALSHA with fallback to EVAL"]

**How to avoid:** Use `redis.NewScript` not raw `EVAL`. Already the recommended pattern in go-redis docs.

**Warning signs:** `EVALSHA` errors in logs; `gateway_rate_limit_check_failures_total{reason="noscript"}` metric incrementing (but script re-cached in ~1 request).

### Pitfall 2: Quota UPSERT deadlocks under high concurrency for same tenant+date

**What goes wrong:** Two concurrent flushes for tenant=X, date=today both try `INSERT ... ON CONFLICT (tenant_id, date) DO UPDATE SET tokens_in = usage_counters.tokens_in + EXCLUDED.tokens_in`. Postgres can deadlock on update lock ordering when many concurrent inserts target same row.

**Why it happens:** Under 1000 concurrent requests from one tenant, all UPDATEs target the same counter row. Postgres handles this with row-level locking, but contention can spike latency.

**How to avoid:**
1. **Batch flushes per tenant** — flusher goroutine aggregates 500 rows for same tenant in-memory then issues ONE UPSERT with summed deltas. Hot path stays lock-free.
2. **Use `FOR UPDATE SKIP LOCKED`** pattern if batching is insufficient — but likely not needed for v1 at 6 tenants × 20 RPS.
3. **Test in SC-5 integration test** (1000 concurrent) — if deadlock errors appear in test, batching is the answer.

**Warning signs:** `ERROR: deadlock detected` in Postgres logs; flusher goroutine falling behind (metric `gateway_billing_flush_lag_seconds` growing).

### Pitfall 3: Streaming `usage` chunk arrives AFTER client disconnects

**What goes wrong:** Client's SSE connection times out / abandons stream at chunk 200 of 500. Upstream still emits all chunks including final `usage`. Gateway's `ModifyResponse` interceptor still sees the usage chunk because the interceptor reads from the upstream connection (not the client connection). Good — flush proceeds with real tokens.

BUT if gateway's HTTP write to client fails early (client TCP closed), the dispatcher may cancel the upstream context, killing the proxy goroutine BEFORE the usage chunk is parsed. Then `requestUsage` has partial counts.

**How to avoid:**
1. `source='partial'` column in `billing_events` — honest accounting when abnormal close detected.
2. `defer { flushBilling() }` uses the counts captured up to the moment of disconnect. Accurate within the chunks received.
3. Pitfall 8 from research/PITFALLS.md is exactly this; D-B2 design is the mitigation.

**Warning signs:** `gateway_billing_flush_total{source="partial"} / gateway_billing_flush_total{source="final"}` ratio climbing above 5%. Healthy is <1%. Audit dashboard should surface this.

### Pitfall 4: `time.LoadLocation("America/Sao_Paulo")` fails in distroless image

**What goes wrong:** Distroless base images may omit the tzdata package. `time.LoadLocation` returns `(nil, error)` silently; gateway boots with nil Location; schedule middleware panics on `now.In(nil)`.

**Why it happens:** Phase 2 Plan 02-08 uses a distroless base — tzdata is usually baked in for `distroless/base` but may be missing in `distroless/static`. 

**How to avoid:**
1. Verify Dockerfile builds include tzdata: `FROM gcr.io/distroless/base-debian12` (has tzdata) vs `distroless/static` (no tzdata).
2. Go `time/tzdata` embedded package: `import _ "time/tzdata"` in main.go — embeds all zones into the binary, works regardless of image. Adds ~400 KB to binary (27.7 MB → ~28.1 MB). Safest.
3. Fail-fast at boot: if `time.LoadLocation("America/Sao_Paulo")` errors → `os.Exit(1)` with clear message.

**Warning signs:** Boot log `time: unknown time zone America/Sao_Paulo`; CI test `TestLoadLocation` fails.

### Pitfall 5: `include_usage` not injected when client forgot stream_options

**What goes wrong:** Client sends `{"stream": true}` without `stream_options`. OpenAI/OpenRouter don't emit the usage chunk. Gateway's interceptor never sees usage. Billing falls back to `TokenCounter` which is heavy and may be skipped. Counter has zeros. Under-billing.

**How to avoid:** Dispatcher MUST inject `stream_options: {"include_usage": true}` into the request body when `stream=true` AND client didn't already set it. Check before marshaling to upstream:

```go
// pseudocode in Director
if req.Stream && req.StreamOptions == nil {
    req.StreamOptions = &StreamOptions{IncludeUsage: true}
}
```

Note: OpenRouter says the flag is deprecated and usage is included always — but belt-and-suspenders doesn't hurt. For llama.cpp the flag is ignored and usage is always emitted.

**Warning signs:** `gateway_billing_flush_total` divided by `gateway_requests_total` drops below ~95% for chat route. `source='partial'` spike.

### Pitfall 6: Price hot-reload races with in-flight flush (stale price applied)

**What goes wrong:** Operator updates price at t=0. `prices_changed` NOTIFY arrives. Loader refreshes in-memory map. But a flush goroutine picked up an event from the buffer at t=-1s using the OLD price. Flush writes the old cost to DB. Audit is slightly stale.

**Why okay:** Prices change ~monthly; fx weekly. Drift of seconds is negligible. `billing_events.created_at` is an auditable column; reconcile job (`gatewayctl billing reconcile`) can detect if many rows have old price applied past a NOTIFY timestamp — but this is not Phase 4 scope.

**How to avoid (if strictness needed in future):**
1. Loader snapshot carries a `version_id`; flush captures `version_id` when scheduling and uses that version for cost calc — reads are always consistent with the snapshot at enqueue time.
2. Alternative: don't compute cost until flush time (lazy); use whichever price is current at flush. For Phase 4 this is what we do implicitly.

**Warning signs:** None operationally significant at Phase 4 scope. Reconcile job (Phase 7) surfaces this if needed.

### Pitfall 7: Quota check passes but billing flush eventually writes 2x intended (idempotency replay bug)

**What goes wrong:** Client retries after timeout. Gateway's idempotency middleware returns cached response. CTX marks `idempotency_replayed=true`. Quota middleware STILL runs (D-D1: replay consumes quota). But flush middleware ALSO runs and enqueues a billing event. Result: one original flush + one replay flush = 2× billed tokens in `billing_events`.

**Why D-B1 saves us:** `INSERT ON CONFLICT (request_id, ts) DO NOTHING`. The replay uses the SAME `request_id` as the original. The conflict target hits and the second INSERT is a no-op. `billing_events` has exactly one row per unique request.

**BUT `usage_counters` UPSERT adds delta each time** — naively that double-counts.

**Resolution:** the `usage_counters` UPDATE must be conditional on the billing_events INSERT actually taking effect:

```sql
WITH inserted AS (
    INSERT INTO ai_gateway.billing_events (...) VALUES (...)
    ON CONFLICT (request_id, ts) DO NOTHING
    RETURNING tokens_in, tokens_out, audio_seconds, embeds_count, tenant_id
)
INSERT INTO ai_gateway.usage_counters (tenant_id, date, tokens_in, ...)
SELECT tenant_id, (now() AT TIME ZONE 'America/Sao_Paulo')::date,
       tokens_in, ...
FROM inserted
ON CONFLICT (tenant_id, date) DO UPDATE SET
    tokens_in = usage_counters.tokens_in + EXCLUDED.tokens_in,
    ...;
```

CTE `inserted` returns zero rows when the conflict fires — the second INSERT also no-ops. Both counters stay consistent. The replay increments neither.

**Warning signs:** `usage_counters.tokens_in` > SUM(`billing_events.tokens_in`) for same day — caught by `gatewayctl billing reconcile`.

### Pitfall 8: `include_usage` token count differs from `/tokenize` re-count (drift between providers)

**What goes wrong:** Pitfall 6 from research/PITFALLS.md — local tokenizer and OpenRouter-Fireworks tokenizer produce slightly different counts for the same input. Billing uses whichever count the upstream reported.

**Why not an issue for Phase 4:** D-B4 clearly separates `cost_local_phantom_brl` (always computed via local-tokenizer count × openrouter-fireworks unit cost) from `cost_external_brl` (real upstream-reported cost from whatever provider served). The phantom cost is our INTERNAL notional cost — consistent tokenizer. The external cost is ACTUAL — whatever OpenRouter billed. Operator can reconcile external cost against OpenRouter invoice monthly (Phase 7 job).

**Warning signs:** `cost_external_brl` sum diverges > 5% from OpenRouter monthly invoice — investigate at Phase 7 reconciliation.

## Code Examples

Verified patterns from authoritative sources.

### Redis Lua Token Bucket (RPS + RPM combined)

**Source:** [Stripe canonical](https://gist.github.com/ptarjan/e38f45f2dfe601419ca3af937fff574d) — extended to handle two windows + return reset timestamps.

```lua
-- gateway/internal/quota/scripts/token_bucket.lua
-- Checks BOTH RPS and RPM buckets atomically. Refills each bucket on-read
-- based on time elapsed since last refresh. Returns which window failed
-- (if any) so middleware can discriminate error codes.
--
-- KEYS[1] = gw:rate:{tenant}:{route_class}:rps:tokens
-- KEYS[2] = gw:rate:{tenant}:{route_class}:rps:ts
-- KEYS[3] = gw:rate:{tenant}:{route_class}:rpm:tokens
-- KEYS[4] = gw:rate:{tenant}:{route_class}:rpm:ts
--
-- ARGV[1] = now_ms           (current epoch ms)
-- ARGV[2] = rps_capacity     (burst bucket size, e.g. 20)
-- ARGV[3] = rps_refill_per_ms (e.g. 20/1000 = 0.02 tokens/ms)
-- ARGV[4] = rpm_capacity     (e.g. 600)
-- ARGV[5] = rpm_refill_per_ms (e.g. 600/60000 = 0.01 tokens/ms)
-- ARGV[6] = requested        (always 1 for chat; could be >1 for batches later)
--
-- Returns: {allowed (0|1), remaining_rps, reset_rps_ms, remaining_rpm, reset_rpm_ms, which_failed_str}
--   which_failed_str in {"", "rps", "rpm"}; "" when allowed=1

local now = tonumber(ARGV[1])
local rps_cap = tonumber(ARGV[2])
local rps_rate = tonumber(ARGV[3])
local rpm_cap = tonumber(ARGV[4])
local rpm_rate = tonumber(ARGV[5])
local req = tonumber(ARGV[6])

-- RPS bucket
local rps_tokens = tonumber(redis.call("get", KEYS[1])) or rps_cap
local rps_ts = tonumber(redis.call("get", KEYS[2])) or now
local rps_delta = math.max(0, now - rps_ts)
local rps_filled = math.min(rps_cap, rps_tokens + rps_delta * rps_rate)

-- RPM bucket
local rpm_tokens = tonumber(redis.call("get", KEYS[3])) or rpm_cap
local rpm_ts = tonumber(redis.call("get", KEYS[4])) or now
local rpm_delta = math.max(0, now - rpm_ts)
local rpm_filled = math.min(rpm_cap, rpm_tokens + rpm_delta * rpm_rate)

-- Both must have capacity
local rps_ok = rps_filled >= req
local rpm_ok = rpm_filled >= req

if not rps_ok then
    -- Don't consume on denial; client must retry later
    -- Reset = ms until bucket has 1 token
    local reset_rps_ms = math.ceil((req - rps_filled) / rps_rate)
    return {0, math.floor(rps_filled), reset_rps_ms, math.floor(rpm_filled), 0, "rps"}
end
if not rpm_ok then
    local reset_rpm_ms = math.ceil((req - rpm_filled) / rpm_rate)
    return {0, math.floor(rps_filled), 0, math.floor(rpm_filled), reset_rpm_ms, "rpm"}
end

-- Allowed: deduct from both buckets
local new_rps = rps_filled - req
local new_rpm = rpm_filled - req

-- TTL = time to full refill × 2, bounded [60, 7200]
local rps_ttl = math.max(60, math.min(7200, math.floor((rps_cap / rps_rate) / 1000 * 2)))
local rpm_ttl = math.max(60, math.min(7200, math.floor((rpm_cap / rpm_rate) / 1000 * 2)))

redis.call("setex", KEYS[1], rps_ttl, new_rps)
redis.call("setex", KEYS[2], rps_ttl, now)
redis.call("setex", KEYS[3], rpm_ttl, new_rpm)
redis.call("setex", KEYS[4], rpm_ttl, now)

return {1, math.floor(new_rps), 0, math.floor(new_rpm), 0, ""}
```

```go
// gateway/internal/quota/lua.go
package quota

import (
    _ "embed"
    "context"
    "github.com/redis/go-redis/v9"
)

//go:embed scripts/token_bucket.lua
var tokenBucketLua string

var tokenBucketScript = redis.NewScript(tokenBucketLua)

// CheckBuckets executes the atomic RPS+RPM check. Returns (allowed, remRPS,
// resetRPSms, remRPM, resetRPMms, whichFailed).
func CheckBuckets(ctx context.Context, rdb *redis.Client,
    tenantKeyPrefix, routeClass string,
    rpsCap, rpmCap int, rpsRatePerMs, rpmRatePerMs float64,
    requested int, nowMs int64,
) (allowed bool, remRPS int, resetRPSms int, remRPM int, resetRPMms int, failedWindow string, err error) {
    keys := []string{
        tenantKeyPrefix + ":" + routeClass + ":rps:tokens",
        tenantKeyPrefix + ":" + routeClass + ":rps:ts",
        tenantKeyPrefix + ":" + routeClass + ":rpm:tokens",
        tenantKeyPrefix + ":" + routeClass + ":rpm:ts",
    }
    result, err := tokenBucketScript.Run(ctx, rdb, keys,
        nowMs, rpsCap, rpsRatePerMs, rpmCap, rpmRatePerMs, requested,
    ).Slice()
    if err != nil {
        return false, 0, 0, 0, 0, "", err
    }
    // result = [allowed(int), remRPS(int), resetRPSms(int), remRPM(int), resetRPMms(int), failedWindow(str)]
    allowed = result[0].(int64) == 1
    remRPS = int(result[1].(int64))
    resetRPSms = int(result[2].(int64))
    remRPM = int(result[3].(int64))
    resetRPMms = int(result[4].(int64))
    failedWindow = result[5].(string)
    return
}
```

### Atomic billing flush (mirror of audit writer)

```go
// gateway/internal/billing/flusher.go — sketches the async pattern
type BillingEvent struct {
    RequestID uuid.UUID
    TS time.Time
    TenantID uuid.UUID
    APIKeyID uuid.UUID
    Route, Upstream, Model string
    TokensIn, TokensOut int
    AudioSeconds float64
    EmbedsCount int
    CostLocalPhantomBRL, CostExternalBRL float64
    Source string // "final" | "partial"
}

// Flush in same transaction: billing_events INSERT + usage_counters UPSERT.
// The CTE-returning form defeats Pitfall 7 (replay double-count).
func (f *BillingFlusher) flushOne(ctx context.Context, tx pgx.Tx, e BillingEvent) error {
    _, err := tx.Exec(ctx, `
        WITH inserted AS (
            INSERT INTO ai_gateway.billing_events
                (request_id, ts, tenant_id, api_key_id, route, upstream, model,
                 tokens_in, tokens_out, audio_seconds, embeds_count,
                 cost_local_brl, cost_local_phantom_brl, cost_external_brl,
                 currency, source)
            VALUES ($1, $2, $3, $4, $5, $6, $7,
                    $8, $9, $10, $11,
                    0, $12, $13,
                    'BRL', $14)
            ON CONFLICT (request_id, ts) DO NOTHING
            RETURNING tenant_id, tokens_in, tokens_out, audio_seconds, embeds_count,
                      cost_local_phantom_brl, cost_external_brl
        )
        INSERT INTO ai_gateway.usage_counters
            (tenant_id, date, tokens_in, tokens_out, audio_seconds, embeds_count,
             cost_local_phantom_brl, cost_external_brl, requests_count)
        SELECT tenant_id,
               (now() AT TIME ZONE 'America/Sao_Paulo')::date,
               tokens_in, tokens_out, audio_seconds, embeds_count,
               cost_local_phantom_brl, cost_external_brl, 1
        FROM inserted
        ON CONFLICT (tenant_id, date) DO UPDATE SET
            tokens_in = usage_counters.tokens_in + EXCLUDED.tokens_in,
            tokens_out = usage_counters.tokens_out + EXCLUDED.tokens_out,
            audio_seconds = usage_counters.audio_seconds + EXCLUDED.audio_seconds,
            embeds_count = usage_counters.embeds_count + EXCLUDED.embeds_count,
            cost_local_phantom_brl = usage_counters.cost_local_phantom_brl + EXCLUDED.cost_local_phantom_brl,
            cost_external_brl = usage_counters.cost_external_brl + EXCLUDED.cost_external_brl,
            requests_count = usage_counters.requests_count + 1;
    `, e.RequestID, e.TS, e.TenantID, e.APIKeyID, e.Route, e.Upstream, e.Model,
        e.TokensIn, e.TokensOut, e.AudioSeconds, e.EmbedsCount,
        e.CostLocalPhantomBRL, e.CostExternalBRL, e.Source)
    return err
}
```

### Streaming SSE dual-shape `usage` interceptor

```go
// gateway/internal/proxy/interceptor_usage.go
// NEW — runs AFTER the tool-call interceptor (Phase 3) in the ComposeInterceptors chain.
// Parses each SSE chunk looking for a top-level "usage" object. Works for both
// OpenAI shape (empty choices[] in separate chunk) and llama.cpp shape (usage
// in same chunk as finish_reason=stop).

type UsageExtractor struct {
    usage *requestUsage // pointer to per-request atomic counters
    log   *slog.Logger
}

// Intercept wraps resp.Body in a tee that inspects each SSE event.
func (u *UsageExtractor) Intercept(resp *http.Response) error {
    // SSE stream? Check Content-Type.
    if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
        // Non-streaming: usage already in the top-level response body.
        // Phase 4 dispatcher reads response.usage in the usual path.
        return nil
    }
    resp.Body = &usageTeeReader{rc: resp.Body, usage: u.usage, log: u.log}
    return nil
}

// SSE chunk body: "data: {...json...}\n\n"
// Parse JSON; if it has a top-level "usage" object, extract and atomic-store.
// Tolerates both OpenAI (empty choices) and llama.cpp (choices with finish_reason=stop).
type sseUsageFrame struct {
    Usage *openai.Usage `json:"usage,omitempty"`
}

func (r *usageTeeReader) Read(p []byte) (int, error) {
    n, err := r.rc.Read(p)
    if n > 0 {
        // Append to internal buffer; scan for \n\n frame boundaries.
        r.buf.Write(p[:n])
        for {
            frame, ok := r.extractFrame()
            if !ok {
                break
            }
            // frame has "data: " prefix stripped; attempt JSON parse.
            var f sseUsageFrame
            if jerr := json.Unmarshal(frame, &f); jerr == nil && f.Usage != nil {
                atomic.StoreInt64(&r.usage.tokensIn, int64(f.Usage.PromptTokens))
                atomic.StoreInt64(&r.usage.tokensOut, int64(f.Usage.CompletionTokens))
                r.log.Debug("usage extracted from SSE",
                    "prompt", f.Usage.PromptTokens, "completion", f.Usage.CompletionTokens)
            }
        }
    }
    return n, err
}
```

### Boot-time sensitive+peak invariant check

```go
// main.go — before server.ListenAndServe()
func assertSensitivePeakInvariant(ctx context.Context, q *gen.Queries, log *slog.Logger) {
    n, err := q.CountSensitivePeakInvariant(ctx)
    if err != nil {
        log.Error("failed to check sensitive+peak invariant at boot", "err", err)
        os.Exit(1)
    }
    if n > 0 {
        log.Error("CRITICAL: sensitive tenants with mode=peak detected — CHECK constraint bypassed",
            "count", n)
        os.Exit(1)
    }
}
```

```sql
-- gateway/db/queries/tenants_admin.sql
-- name: CountSensitivePeakInvariant :one
SELECT COUNT(*)::bigint FROM ai_gateway.tenants
WHERE mode = 'peak' AND data_class = 'sensitive';
```

### `time.Location` loaded at boot (fail-fast)

```go
// main.go
import (
    _ "time/tzdata" // embed all zones — works in distroless/static
    "time"
)

var appLocation *time.Location

func mustLoadLocation(name string, log *slog.Logger) *time.Location {
    loc, err := time.LoadLocation(name)
    if err != nil {
        log.Error("failed to load time.Location; tzdata missing?", "zone", name, "err", err)
        os.Exit(1)
    }
    return loc
}

// In main:
appLocation = mustLoadLocation("America/Sao_Paulo", log)
// Brazil eliminated DST in 2019 [CITED: Wikipedia/timeanddate]; LoadLocation
// returns a zone with fixed UTC-3 offset year-round. No DST transitions to
// handle at quota-rollover midnight.
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Per-request `/health` check before dispatching | Periodic background probes + in-memory state | Phase 3 (already done) | — |
| ORM for SQL | sqlc codegen + raw SQL | Phase 2 | Already in place |
| `EVAL` with string scripts | `redis.NewScript` with EVALSHA cache | 2019+ | Auto-handled by go-redis v9 |
| `CURRENT_DATE AT TIME ZONE` | `(now() AT TIME ZONE 'zone')::date` | N/A — just correct SQL | Flag in CONTEXT.md — must use correct form in migrations + queries |
| Static image with tzdata external | `import _ "time/tzdata"` | Go 1.15+ | ~400 KB binary cost for guaranteed zone availability |
| `stream_options.include_usage` required | Always-on auto (OpenRouter) | OpenRouter ~2024 Q2 | Inject the flag anyway for backward compat + llama.cpp ignores it gracefully |

**Deprecated/outdated:**
- `whisper-1` is listed as "legacy" vs newer `gpt-4o-transcribe` and `gpt-4o-mini-transcribe` — BUT `whisper-1` remains active as of Apr 2026 [CITED: developers.openai.com/api/docs/models/whisper-1]. gpt-4o-transcribe is being retired Feb 28, 2026 (per Microsoft Q&A). For the price seed: use `whisper-1` @ $0.006/min (canonical "whisper API" pricing); add gpt-4o-mini-transcribe as a v2 consideration.
- `gpt-4o-transcribe` being retired Feb 28, 2026 [CITED: learn.microsoft.com/en-us/answers/questions/5760183] — do NOT seed this model.

## Environment Availability

Phase 4 has no external dependencies beyond Phase 2/3's already-verified stack.

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| PostgreSQL 16 (DO managed, shared `ai_gateway` schema) | All migrations + quota/billing/prices/admin paths | ✓ | 16.x (verified Phase 2) | — |
| Redis 7 (shared Ifix, namespace `gw:`) | Lua rate-limit + admin key cache | ✓ | 7.x (verified Phase 2; infra-redis-1) | Rate-limit fail-open per D-A2 |
| Go 1.23+ | gateway binary | ✓ | Phase 2 tag build | — |
| Docker + Portainer | Deploy | ✓ | Phase 2 tag build | — |
| `tzdata` (America/Sao_Paulo) | Schedule routing | ✓ | Embedded via `import _ "time/tzdata"` | None — fail-fast at boot |
| golang.org/x/crypto/bcrypt | Admin key hashing | ✓ | stdlib-adjacent | — |

**Missing dependencies with no fallback:** None.
**Missing dependencies with fallback:** Redis for rate-limit (fail-open) — existing Phase 2 decision.

## Validation Architecture

### Test Framework

| Property | Value |
|----------|-------|
| Framework | Go stdlib `testing` + `testcontainers-go` for Postgres 16 + Redis 7 (Phase 2 harness, `gateway/internal/integration_test/`) |
| Config file | `gateway/go.mod` (deps); no separate config |
| Quick run command | `go test -short ./gateway/...` |
| Full suite command | `go test -race ./gateway/...` (warm ≈20s; cold ≈60s incl. testcontainers) |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| TEN-03 | RPS limit (e.g., 20) — 21 req in 1s → first 20 pass, 21st gets 429 rate_limit_rps + Retry-After | integration (testcontainers Redis) | `go test -run TestRateLimitRPS ./gateway/internal/quota` | ❌ Wave 0 |
| TEN-03 | RPM limit (e.g., 600) — 601 req in 60s → 601st gets 429 rate_limit_rpm | integration | `go test -run TestRateLimitRPM ./gateway/internal/quota` | ❌ Wave 0 |
| TEN-03 | SC-5: 1000 goroutines concurrent with rps=100 → EXACTLY 100 pass, 900 get 429 | integration (concurrent) | `go test -run TestRateLimitAtomic1000Concurrent -race ./gateway/internal/quota` | ❌ Wave 0 |
| TEN-04 | Daily tokens quota enforced — (N-1) passes, N blocks with quota_exceeded_daily_tokens | integration (testcontainers Postgres) | `go test -run TestQuotaDailyTokens ./gateway/internal/quota` | ❌ Wave 0 |
| TEN-04 | Monthly quota enforced | integration | `go test -run TestQuotaMonthlyTokens ./gateway/internal/quota` | ❌ Wave 0 |
| TEN-04 | Daily rollover at 00:00 BRT — quota from yesterday does NOT block today | integration (testcontainers Postgres, fake clock) | `go test -run TestQuotaDailyRolloverBRT ./gateway/internal/quota` | ❌ Wave 0 |
| TEN-04 | Quota check fail-closed — Postgres lookup fails → 503 `quota_check_unavailable` | integration | `go test -run TestQuotaFailClosed ./gateway/internal/quota` | ❌ Wave 0 |
| TEN-05 | Schedule peak + off-hours → dispatcher picks tier=1 upstream | integration | `go test -run TestSchedulePeakOffHours ./gateway/internal/schedule` | ❌ Wave 0 |
| TEN-05 | Schedule 24/7 → always tier=0 | integration | `go test -run TestSchedule24x7 ./gateway/internal/schedule` | ❌ Wave 0 |
| TEN-05 | sensitive+peak CHECK rejects INSERT | integration (testcontainers Postgres) | `go test -run TestSensitivePeakCheckConstraint ./gateway/internal/tenants` | ❌ Wave 0 |
| TEN-05 | sensitive+peak boot-time invariant fails fast | unit (mock query) | `go test -run TestBootInvariant ./gateway` | ❌ Wave 0 |
| TEN-05 | sensitive+peak `gatewayctl tenant set-mode` rejects with clear error | unit (CLI) | `go test -run TestGatewayctlSetMode_RejectSensitivePeak ./gateway/cmd/gatewayctl` | ❌ Wave 0 |
| TEN-06 | Non-streaming billing flush — request completes, row in billing_events with tokens_in+out | integration | `go test -run TestBillingFlushNonStream ./gateway/internal/billing` | ❌ Wave 0 |
| TEN-06 | Streaming SSE usage extraction from OpenAI-shape chunk (empty choices) | unit (parser) | `go test -run TestUsageExtractorOpenAIShape ./gateway/internal/proxy` | ❌ Wave 0 |
| TEN-06 | Streaming SSE usage extraction from llama.cpp-shape chunk (choices[0].finish_reason=stop) | unit (parser) | `go test -run TestUsageExtractorLlamaCppShape ./gateway/internal/proxy` | ❌ Wave 0 |
| TEN-06 | Streaming abnormal close — row has source='partial' with captured-up-to tokens | integration | `go test -run TestBillingAbnormalClose ./gateway/internal/billing` | ❌ Wave 0 |
| TEN-06 | Replay retries don't duplicate billing — SAME request_id, one billing_events row | integration | `go test -run TestBillingIdempotentReplay ./gateway/internal/billing` | ❌ Wave 0 |
| TEN-06 | usage_counters stays consistent with SUM(billing_events) on replay | integration | `go test -run TestUsageCountersCTEConsistency ./gateway/internal/billing` | ❌ Wave 0 |
| TEN-06 | Reconcile detects drift > 0.1% | integration | `go test -run TestBillingReconcileDrift ./gateway/cmd/gatewayctl` | ❌ Wave 0 |
| TEN-06 | cost_external_brl=0 when upstream=tier-0; cost_local_phantom_brl=0 when upstream=tier-1 | integration | `go test -run TestBillingCostColumnSplit ./gateway/internal/billing` | ❌ Wave 0 |
| TEN-06 | Price hot-reload via NOTIFY prices_changed — next flush uses new price | integration | `go test -run TestPricesHotReload ./gateway/internal/billing` | ❌ Wave 0 |
| TEN-06 | fx hot-reload via NOTIFY | integration | `go test -run TestFXHotReload ./gateway/internal/billing` | ❌ Wave 0 |
| TEN-07 | GET /admin/usage response shape matches SC-3 (tenant, range, summary, rows) | integration (testcontainers + seed data) | `go test -run TestAdminUsageResponseShape ./gateway/internal/admin` | ❌ Wave 0 |
| TEN-07 | GET /admin/usage authenticates via X-Admin-Key bcrypt | integration | `go test -run TestAdminUsageAuthBCrypt ./gateway/internal/admin` | ❌ Wave 0 |
| TEN-07 | GET /admin/usage denies without admin key | integration | `go test -run TestAdminUsageUnauthorized ./gateway/internal/admin` | ❌ Wave 0 |
| TEN-07 | gatewayctl admin-key create/revoke/list | unit (CLI) | `go test -run TestGatewayctlAdminKey ./gateway/cmd/gatewayctl` | ❌ Wave 0 |
| TEN-03,04 | Middleware chain order correctness: idempotency replay runs quota + flush but SKIPS rate-limit | integration | `go test -run TestMiddlewareChainReplaySemantics ./gateway/internal/integration_test` | ❌ Wave 0 |
| (folded) | Metrics middleware emits obs.RequestsTotal per request | integration | `go test -run TestMetricsMiddleware ./gateway/internal/obs` | ❌ Wave 0 |
| (folded) | Per-route WriteTimeout: chat=0, embed=30s, audio=120s from env vars | unit | `go test -run TestPerRouteWriteTimeout ./gateway/internal/config` | ❌ Wave 0 |

### Sampling Rate

- **Per task commit:** `go test -race -short ./gateway/internal/<touched-package>`
- **Per wave merge:** `go test -race ./gateway/...` full suite (~60s)
- **Phase gate:** Full suite green before `/gsd-verify-work`

### Wave 0 Gaps

- [ ] `gateway/internal/quota/enforcer_test.go` — rate-limit + quota middleware tests (TEN-03, TEN-04)
- [ ] `gateway/internal/quota/lua_test.go` — Lua script correctness (SC-5 1000 concurrent)
- [ ] `gateway/internal/quota/counters_test.go` — usage_counters UPSERT CTE, fail-closed
- [ ] `gateway/internal/billing/flusher_test.go` — idempotent INSERT, CTE prevents double-count
- [ ] `gateway/internal/billing/prices_test.go` — hot-reload via NOTIFY
- [ ] `gateway/internal/billing/accountant_test.go` — on-emission counter; atomic ops
- [ ] `gateway/internal/tenants/loader_test.go` — hot-reload + sensitive+peak CHECK
- [ ] `gateway/internal/schedule/policy_test.go` + `window_test.go` — time.Location, peak vs 24/7, DST-free BRT
- [ ] `gateway/internal/admin/middleware_test.go` — bcrypt verify + Redis cache
- [ ] `gateway/internal/admin/usage_test.go` — SC-3 response shape
- [ ] `gateway/internal/proxy/interceptor_usage_test.go` — dual-shape SSE usage parsing
- [ ] `gateway/internal/integration_test/phase4_test.go` — end-to-end: auth → rate → quota → schedule → tokencount → dispatcher → flush
- [ ] `gateway/cmd/gatewayctl/{prices,billing,admin-key}_test.go` + `tenant_test.go` extensions
- [ ] Shared fixtures in `gateway/internal/integration_test/phase4_fixtures.go`

### Manual-only tests

- [ ] Live 1000 concurrent goroutines against real Redis Ifix infrastructure — testcontainers covers the scenario, but operator may want to run `go test -run TestRateLimitAtomic1000Concurrent -count=10 -race` against dev Redis to catch any network-latency-driven races.
- [ ] `gatewayctl billing reconcile --apply` against a seeded Postgres with intentional drift (one-shot, no CI target).

## Security Domain

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | yes (admin endpoint) | bcrypt cost 10 via `golang.org/x/crypto/bcrypt` for admin_keys hash; X-Admin-Key header (not cookie); Redis 60s cache invalidated by CLI revoke |
| V3 Session Management | no | Stateless; no sessions. API key + admin key are the only auth. |
| V4 Access Control | yes | Admin endpoints gated by `adminAuthMiddleware`; sensitive+peak CHECK + boot-time invariant enforces LGPD routing constraint |
| V5 Input Validation | yes | `t.Object` schema on all `/admin/*` endpoints (Elysia-style — Phase 2 pattern); sqlc typed queries prevent SQL injection; JSONB params (prices.circuit_config, tenants) validated via Go struct unmarshal |
| V6 Cryptography | yes | bcrypt (admin_keys); no custom crypto; argon2id from Phase 2 (api_keys) unchanged |
| V7 Error Handling & Logging | yes | slog JSON with `module` attr; API keys + Authorization header + admin key redacted via existing `httpx.NewRedactor` (Phase 2) |
| V8 Data Protection | yes | D-B2 LGPD — sensitive tenants continue to have no `audit_log_content` row (Phase 2 rule); billing_events has no prompt content — only metadata + token counts |
| V9 Communication | no (Phase 4) | TLS termination deferred to Phase 10 PRD-07 |
| V10 Malicious Code | no | No plugin system; no dynamic code exec; Lua scripts are embedded at compile time |
| V11 Business Logic | yes | Idempotency replay semantics (Pitfall 7 CTE) prevents double-billing; quota fail-closed prevents runaway external cost; sensitive+peak triple-defense |

### Known Threat Patterns for Go + Postgres + Redis + go-chi

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| SQL injection via tenant slug or admin key filter | Tampering | sqlc typed queries + `pgx` parameterized statements (mandatory per Phase 2 D-D2) |
| Admin key leak via logs | Info disclosure | `httpx.NewRedactor` redacts `X-Admin-Key` alongside `Authorization` header (Phase 2 D-B7) — extend redactor config |
| Quota bypass via race (TOCTOU between check and increment) | Tampering | Postgres UPSERT in same transaction as billing_events INSERT; atomic via PG row lock |
| Rate-limit bypass via time manipulation | Tampering | `now_ms` is server-supplied from `time.Now()` — not client-supplied; Redis keys are tenant-scoped |
| Price-table tampering (operator error or SQL injection) | Tampering | `prices` has `valid_from/valid_to` append-only model; historical costs are auditable; CHECK on unit type ∈ {input_token, output_token, audio_second, embed_request} |
| Replay attack via Idempotency-Key reuse with different body | Tampering | Phase 2 D-C3 returns 422 `idempotency_key_reused_with_different_body` |
| Admin session fixation | Spoofing | No sessions — X-Admin-Key is per-request; rotation via `gatewayctl admin-key revoke` |
| Sensitive data to external provider (LGPD) | Info disclosure | CHECK constraint + boot-time validation; schedule middleware never routes sensitive to tier-1 |
| Cost runaway via Redis outage | Denial of service (economic) | D-A2 quota fail-closed; 503 `quota_check_unavailable` prevents torched OpenRouter bill |
| Boot-time race: read prices table before goose migration finishes | Tampering | Migrations apply first (AI_GATEWAY_MIGRATE_ON_BOOT=true), then loader constructs; fail-fast on missing table |

## Assumptions Log

Claims tagged `[ASSUMED]` in this research — planner should confirm with operator before execution.

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | OpenRouter's `qwen/qwen3.5-27b` model page lists $0.195/1M input + $1.56/1M output at the aggregate (top-of-page) level as of 2026-04-20 — AND Fireworks is one of its backing providers | §Pricing seed values | Seed prices in migration 0015 are wrong; `cost_local_phantom_brl` and `cost_external_brl` values in billing_events will be systematically off until operator updates via `gatewayctl prices set`. Reconcile job at Phase 7 catches this. **Mitigation:** Plan must include operator-gated task "Pedro confirms current OpenRouter Qwen3.5-27B pricing + provider availability on fireworks specifically (not just generic 'any provider') 24h before migration 0015 runs". |
| A2 | Fireworks AI is actually still one of OpenRouter's backing providers for Qwen3.5-27B as of Apr 2026 | §Pricing / D-C1 Phase 3 | If Fireworks is no longer a provider, Phase 3 D-C1 `provider.order: ["fireworks"]` pin breaks too — breaker opens on 404. **Mitigation:** Phase 3 D-C2 config via env var allows changing to Together/DeepInfra without redeploy; Phase 4 plan must include "Pedro confirms which providers currently serve Qwen3.5-27B on OpenRouter and picks the most stable one for tool-calling" as part of Wave 0 (before any other task runs). |
| A3 | Quota defaults `daily_tokens=10M, monthly_tokens=300M, daily_audio_minutes=600, monthly_audio_minutes=18000, daily_embeds=100k, monthly_embeds=3M, rps=20, rpm=600` are conservative starting values appropriate for each of the 6 Ifix apps | §CONTEXT.md "Claude's Discretion" | Under-provisioning at these values would cause early 429s that don't reflect intended policy. **Mitigation:** CONTEXT.md explicitly frames these as operator-refined via `gatewayctl tenant set-quota`; the migration only seeds defaults. Phase 4 runbook task must document "operator must refine per-tenant quotas before Phase 8 deployment". |
| A4 | Postgres DO managed instance supports partitioned-table `ON CONFLICT DO NOTHING` without any extension install (native PG16 feature) | §Partitioning | If DO restricts some PG feature, migration 0010 fails. **Mitigation:** Phase 2 already uses partitioned `audit_log` + `audit_log_content` with PG16 successfully — this is verified by running code. Risk is very low. |
| A5 | `whisper-1` at $0.006/min is the correct price unit to seed for local-equivalent phantom cost for STT (D-B4) | §Pricing seed | Phantom cost numbers will drift ~50% if gpt-4o-transcribe ($0.006/min) or gpt-4o-mini-transcribe ($0.003/min) should be the reference instead. **Mitigation:** whisper-1 is the CURRENT OpenAI Whisper API product; gpt-4o-mini-transcribe is a subjective replacement that hasn't removed whisper-1. Keep whisper-1 as seed; revisit in Phase 7. |
| A6 | BGE-M3 text-embedding-3-small phantom cost mapping: multiply `embeds_count × avg_tokens_per_embed × $0.02/1M` — with avg_tokens_per_embed estimated at ~50 tokens based on typical customer record sizes | §D-B4 | Phantom cost for embed may be 2-5× off depending on actual embedding payload sizes. **Mitigation:** Revisit after Phase 7 operator sees real data; Phase 4 v1 uses 50-token default. Document as a configurable constant. |
| A7 | The operator has access to Portainer Stack env variable editor and will set `AI_GATEWAY_ADMIN_KEY_BOOTSTRAP`, `AI_GATEWAY_USD_BRL_RATE_DEFAULT` before deploying the Phase 4 binary | §Environment Availability | Absent operator action, bootstrap generates random admin key + logs warn; operator must then read log to capture it — extra step but not blocker. **Mitigation:** Phase 4 runbook includes pre-deploy checklist. |

**Table summary:** 7 assumptions. 2 are high-risk (A1, A2 — pricing seed + provider availability) and are explicitly flagged for operator confirmation during Wave 0 before any migration runs. 5 are low-medium risk with acceptable fallbacks.

## Open Questions

1. **Which OpenRouter provider serves Qwen3.5-27B most stably as of Apr 2026?**
   - What we know: CONTEXT.md D-C1 (Phase 3) pins `provider.order: ["fireworks"]`. Research confirms Fireworks lists Qwen3 variants (30B, Max, Coder) but NOT specifically Qwen3.5-27B. OpenRouter has the model page with aggregate pricing but provider list isn't fetched cleanly.
   - What's unclear: Whether Fireworks is actually serving Qwen3.5-27B on OpenRouter's routing at Apr 2026, vs. Together AI or DeepInfra; whether tool-calling stability differs among providers.
   - Recommendation: **Wave 0 task**: `curl https://openrouter.ai/api/v1/models/qwen/qwen3.5-27b` (or web inspect `/providers`) to confirm current list. If Fireworks absent, update Phase 3 D-C2 config and Phase 4 price seed in same PR.

2. **Does `gateway/db/migrations/0013_evolve_tenants_schedule_quota.sql` need ALTER ADD COLUMN with DEFAULT values on existing rows?**
   - What we know: Existing `tenants` table has `converseai` seed row from 0001. Adding `mode TEXT NOT NULL DEFAULT '24/7'` backfills; adding `rps_limit INT NOT NULL DEFAULT 20` backfills. 
   - What's unclear: Whether Phase 4 operator wants to set per-tenant quotas in migration 0015 (explicit values per app) OR leave defaults + operator configures via CLI (D-D4).
   - Recommendation: Migration 0015 seeds QUOTAS by tenant slug (`UPDATE tenants SET rps_limit = 50 WHERE slug = 'converseai'` etc.) for the 6 known tenants. Operator can still override via CLI.

3. **Is there a cleaner path than using CTE-returning INSERT for the billing flush?**
   - What we know: The CTE form (Pitfall 7 fix) correctly handles idempotent replay. It's 15 lines of SQL.
   - What's unclear: Whether a simpler DB trigger (AFTER INSERT ON billing_events → UPDATE usage_counters) would be cleaner.
   - Recommendation: Trigger-based has edge case around trigger-fires-per-row in batch COPY, easier to reason about at row level via CTE. Stick with CTE. Phase 7 can revisit if audit becomes complex.

4. **Should `gateway_inflight{tenant}` be introduced in Phase 4?**
   - What we know: CONTEXT.md D-D Plumbing says yes ("Phase 5 consume"). Phase 5 SC-4 explicitly needs this.
   - What's unclear: Whether introducing the metric in Phase 4 without using it (Phase 5 uses it) risks cardinality if 6 tenants × 3 routes = 18 series.
   - Recommendation: Yes, introduce `gateway_inflight{tenant, route}` gauge in Phase 4 metricsMiddleware. 18 series is trivially under Pitfall 13's 10k budget.

5. **Is there a risk the default `(tenant_id, date)` PK on `usage_counters` hits heavy contention under concurrent burst?**
   - What we know: Same row written N times per day by N concurrent flushes. Postgres row-level lock handles this.
   - What's unclear: Peak contention at ~50 RPS per tenant could stall.
   - Recommendation: Wave 0 test: `TestUsageCountersConcurrentUpdate` with 100 goroutines same tenant — asserts no deadlock and SUM correct. If contention issue, batch by tenant in flusher (Pitfall 2 mitigation).

## Sources

### Primary (HIGH confidence)

- [Context7 `/redis/go-redis`](https://context7.com/redis/go-redis/llms.txt) — Lua scripting (`redis.NewScript`, EVALSHA fallback semantics, example rate-limiter), topic: EvalSha Lua ScriptLoad atomic rate limit. Fetched 2026-04-20.
- [postgresql.org §Date/Time Types](https://www.postgresql.org/docs/current/functions-datetime.html) — Confirmed `CURRENT_DATE AT TIME ZONE` is invalid; correct form is `(now() AT TIME ZONE 'zone')::date`.
- [postgresql.org §INSERT documentation](https://www.postgresql.org/docs/current/sql-insert.html) — ON CONFLICT semantics on partitioned tables (PG11+ supports DO NOTHING / DO UPDATE when conflict target includes partition key).
- [Stripe canonical token bucket Lua](https://gist.github.com/ptarjan/e38f45f2dfe601419ca3af937fff574d) — Canonical Lua script shape; extended to {allowed, remaining, reset_ms} for our use case.
- [pkg.go.dev `github.com/jackc/pgxlisten`](https://pkg.go.dev/github.com/jackc/pgxlisten) — Multiplexed LISTEN on single conn with `Handle(channel, handler)`.
- [developers.openai.com/api/docs/guides/streaming-responses](https://developers.openai.com/api/docs/guides/streaming-responses) — `stream_options.include_usage` semantics; OpenAI chunk shape (separate final chunk, empty choices).
- [github.com/ggml-org/llama.cpp/issues/15443](https://github.com/ggml-org/llama.cpp/issues/15443) — llama.cpp streaming usage placement differs from OpenAI; unresolved as of Aug 2025.
- [github.com/nsmithuk/bcrypt-cost-go](https://github.com/nsmithuk/bcrypt-cost-go) — bcrypt cost 10 benchmark: 86.25ms on DO VPS.
- [timeanddate.com 2019 article](https://www.timeanddate.com/news/time/brazil-scraps-dst.html) — Brazil eliminated DST in 2019; America/Sao_Paulo has fixed UTC-3 year-round.
- `pkg/openai/types.go` — existing in-repo contract Phase 4 extends.
- `gateway/internal/audit/writer.go` — Phase 2 async batched write pattern Phase 4 replicates for billing.
- `gateway/internal/upstreams/loader.go` + `listen.go` — Phase 3 hot-reload pattern Phase 4 extends.
- `gateway/internal/proxy/tokencount.go` + `interceptor.go` — Phase 3 interfaces Phase 4 extends.

### Secondary (MEDIUM confidence)

- [crunchydata.com: Native Partitioning with Postgres](https://www.crunchydata.com/blog/native-partitioning-with-postgres) — pg_partman vs native discussion; native is preferred for our use case.
- [dbi-services.com: INSERT ON CONFLICT with partitions works in PG11](https://www.dbi-services.com/blog/insert-on-conflict-with-partitions-finally-works-in-postgresql-11/) — confirms PG11+ supports conflict detection across partitions.
- [openrouter.ai/docs/api/reference/streaming](https://openrouter.ai/docs/api/reference/streaming) — OpenRouter's streaming behavior and `stream_options.include_usage` deprecation note.
- [openrouter.ai/qwen/qwen3.5-27b](https://openrouter.ai/qwen/qwen3.5-27b) — Top-level model page: $0.195/1M input, $1.56/1M output (aggregate). Provider list not cleanly fetched — flagged in Assumptions A1/A2.
- [fireworks.ai/pricing](https://fireworks.ai/pricing) — Fireworks serverless catalog; Qwen3.5-27B NOT listed as a distinct SKU.
- [developers.openai.com/api/docs/pricing](https://developers.openai.com/api/docs/pricing) — gpt-4o-mini-transcribe $0.003/min; gpt-4o-transcribe $0.006/min (retiring Feb 28, 2026); whisper-1 not on main pricing page.
- [diyai.io: OpenAI Whisper API Pricing 2026](https://diyai.io/ai-tools/speech-to-text/openai-whisper-api-pricing-2026/) — whisper-1 at $0.006/min (confirmed by multiple sources).
- [platform.openai.com rate-limit headers](https://developers.openai.com/api/docs/guides/rate-limits) — X-RateLimit-* header format.
- [redis.io/tutorials/howtos/ratelimiting](https://redis.io/tutorials/howtos/ratelimiting/) — Token bucket reference.

### Tertiary (LOW confidence)

- [getmaxim.ai Bifrost cost calculator](https://www.getmaxim.ai/bifrost/llm-cost-calculator/provider/openrouter/model/qwen3.5-27b) — Reports Qwen3.5-27B at $0.30/$2.40; CONTRADICTS OpenRouter's own model page. Flagged — operator must resolve this discrepancy before 0015 runs. **This is why A1/A2 are operator-gated.**

## Metadata

**Confidence breakdown:**
- Standard stack: **HIGH** — all libs already in go.mod from Phase 2/3; patterns verified by existing code.
- Architecture: **HIGH** — mirrors Phase 3 patterns (loader+listen, async writer); minimal novel design.
- Lua rate-limit: **HIGH** — Stripe canonical pattern, Context7-verified go-redis API.
- Billing partitioning: **HIGH** — Phase 2 audit_log proves PG16 pattern works.
- Hot-reload multi-channel: **HIGH** — pgxlisten multiplex is documented; current `listen.go` proves pattern.
- Pricing seed values: **MEDIUM** — Whisper and embedding confirmed authoritatively; Qwen3.5-27B has conflicting sources (aggregate vs calculator), flagged as A1/A2 for operator confirmation.
- Streaming usage extraction: **HIGH** — both shapes documented; dual-shape parser is straightforward.
- Timezone correctness: **HIGH** — Brazil DST eliminated in 2019 verified; `(now() AT TIME ZONE ...)::date` is idiomatic.
- bcrypt choice: **HIGH** — benchmarks verified; acceptable for admin-path volume.

**Research date:** 2026-04-20
**Valid until:** 2026-05-20 (30 days — stable external deps) — EXCEPT pricing (A1/A2) which should be re-confirmed within 24h of migration 0015 running.

---

*Phase: 04-multi-tenant-quotas-billing-schedule-routing*
*Research completed: 2026-04-20*
