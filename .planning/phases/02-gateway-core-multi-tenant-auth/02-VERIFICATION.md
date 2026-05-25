---
phase: 02-gateway-core-multi-tenant-auth
verified: 2026-05-25T19:50:00Z
status: passed
score: 5/5 SC fully PASS — SC-5 step 7 chat E2E closed 2026-05-25 via Phase 06.9 + 4 follow-up PRs
overrides_applied: 0
re_verification:
  previous_status: passed_partial
  previous_score: "4/5 SC fully PASS; SC-5 step 7 chat E2E deferred to Phase 03/06.6"
  gaps_closed:
    - "SC-5 live deploy 10-step checklist re-run on 2026-05-23 against ai-gateway-dev (image develop-d689321): steps 1-6 + 8 + 9 (inferred) + 10 PASS; container Up 2h healthy, /health 200, 25 migrations applied (0001..0025), tenant + key CRUD work, unauth 401 OpenAI envelope confirmed, key revoke works. See 02-UAT-2026-05-23.md"
  gaps_closed_2026_05_25:
    - "Step 7 chat E2E (was 503 on 2026-05-23) re-run on 2026-05-25 against ai-gateway-dev (image develop-560aa2a) — HTTP 200 + DeepSeek v4 Flash completion via OpenRouter (Novita provider). Phase 06.9 (PR #1) wired schema-driven per-upstream model rewrite; follow-up PRs #2 (target → deepseek-v4-flash:nitro), #3 (BuildDirector path-join), #4 (EffectiveState force-override), #5 (HasSuffix chat-path check) closed every link in the dispatcher → tier-1 chain. See 06.9-HUMAN-UAT.md S5 + 06.9-VERIFICATION.md."
  gaps_remaining:
    - "Step 9 audit row count via direct psql — MCP postgres-grupo-ifix prompt rejected; AuditInterceptor wired confirmed via main.go:160 + Integration_03_AuditWrite covers persistence (non-blocking observability gap, not a SC gate)"
  regressions: []
gaps: []
deferred:
  - truth: "Live VPS deploy (Portainer stack `ai-gateway-dev` reachable; real /health 200; gatewayctl tenant+key in prod DB; end-to-end chat through pod)"
    addressed_in: "Phase 2 post-push human verification (02-08 Task 3, explicitly deferred to operator per 02-08-SUMMARY.md §User Must Verify Before/After First Push)"
    evidence: "02-08-SUMMARY.md lines 164-198 document 10-step post-push checklist; STATE.md session-continuity note reiterates `Next session should: (1) User pushes develop ... (2) User runs the post-push verification checklist in 02-08-SUMMARY.md`"
  - truth: "`audit export-month` cold storage + partition drop >90d (Plan 02-09)"
    addressed_in: "Phase 7 (Observability) or Phase 10 (Production Hardening)"
    evidence: "02-09-PLAN.md frontmatter explicit: `optional: true; deferred_to: Phase 7 OR Phase 10 (re-evaluate when audit_log grows ≥60 days). Codex [HIGH] 02-09 scope creep — not required for any Phase 2 Success Criterion.`"
human_verification:
  - test: "Post-push live deploy checklist (02-08-SUMMARY.md §Post-push verification steps 1-10)"
    expected: "Actions green on `develop` → GHCR image pushed → Portainer webhook fires → container healthy → /health 200 → migrate status 6 applied → gatewayctl tenant + key create succeed → end-to-end chat through real pod returns qwen completion → 401 on unauth → audit row present in Postgres"
    why_human: "Requires real VPS (178.156.150.21), live Portainer webhook secrets configured, and live Phase 1 pod at UPSTREAM_*_URL — cannot be verified read-only from code; owner-gated per explicit authorization in 02-08 execution prompt"
---

# Phase 2: Gateway Core + Multi-tenant Auth — Verification Report

**Phase Goal:** Apps can point their OpenAI SDKs at `gateway.ifix.com.br` with a per-tenant API key and get authenticated, auditable, streaming-capable responses from the primary pod.

**Verified:** 2026-04-18
**Status:** human_needed (4/5 auto-PASS; SC-5 partial — live deploy deferred to operator)
**Re-verification:** No — initial verification.

---

## Goal Achievement

### Observable Truths (Phase 2 Success Criteria from ROADMAP.md)

| # | Success Criterion | Status | Evidence |
|---|------------------|--------|----------|
| 1 | OpenAI SDK client with `base_url=/v1` + valid API key → real Qwen completions (incl. SSE per-chunk flush) + Whisper/embeddings | PASS | `proxy/chat.go:35` sets `FlushInterval:-1`; `proxy/embeddings.go` + `proxy/audio.go` use default buffered; `proxy/director.go` propagates request to upstream URL; `cmd/gateway/main.go:185-199` builds all 3 proxies and mounts under `/v1/*`; `proxy/chat_test.go:TestChatProxy_SSEStreamingFlushesPerChunk` asserts per-chunk flush; Integration_06_GatewayE2E (02-07) exercises full stack against fake upstream |
| 2 | Unauth → 401 OpenAI envelope; rate-limit/quota wiring returns 429 (values exercised later) | PASS (auth 401); WIRING-ONLY (429 deferred to Phase 4 as per ROADMAP.md wording "wiring in place, values exercised later") | `auth/apikey.go:ExtractKey` + `auth/apikey.go:Middleware` + `httpx/envelope.go:WriteOpenAIError` → no-key/invalid/revoked/malformed all return 401 with `openai.ErrorResponse{type: authentication_error, code: ...}`; `auth/apikey_test.go` covers the 4 codes; Integration_02_AuthFlow confirms live stack. Rate-limit/quota scope is Phase 4 (RES-01..TEN-07). |
| 3 | Every request traceable: unique `X-Request-ID`, logs carry same ID, `audit_log` row lands | PASS | `httpx/requestid.go:RequestID` middleware generates UUIDv7 and sets response header; `httpx/logger.go` attaches ID to slog attrs; `audit/writer.go` Enqueue + async Run flushes rows via sqlc `InsertAuditLog`; wired in `cmd/gateway/main.go:160` (`go auditWriter.Run(ctx)`); Integration_03_AuditWrite asserts 10 requests → 10 distinct request_ids in audit_log |
| 4 | API key carries `data_class` (`normal`/`sensitive`) exposed in request ctx | PASS | `db/migrations/0002_create_api_keys.sql:10` defines enum `data_class ∈ {normal, sensitive}`; `auth/context.go:AuthContext` exposes `DataClass`; `auth/apikey.go:167+186` threads enum → ctx via `FromContext`; `audit/middleware.go` consults `data_class` to gate `audit_log_content` insertion (D-B2 LGPD default); `gatewayctl key create --data-class` sets it |
| 5 | Deploys via standard Ifix flow (GitHub → Actions → Portainer webhook) on dedicated 4 vCPU VPS; model aliases resolve | PARTIAL — artifacts verified, live-deploy human-verify | PASS components: `gateway/Dockerfile` (2-stage distroless, 27.7 MB, /gateway + /gatewayctl); `gateway/docker-compose.yml` (Portainer stack template, traefik-public, ${VAR} interpolation only); `.github/workflows/build-gateway.yml` (7-job pipeline test → integration → build → deploy-dev/prod webhooks); `models/resolver.go` + migration 0005 seeds `qwen→llm→qwen`, `whisper→stt→Systran/faster-whisper-large-v3`, `bge-m3→embed→BAAI/bge-m3`; `models/rewrite.go` + `cmd/gateway/main.go:301-305` wraps chat + embed handlers. MISSING (human-verify): actual live deploy post-push — 02-08 Task 3 explicitly deferred to operator. |

**Score:** 4/5 fully auto-PASS; SC-5 PARTIAL (the 5 deliverable artifacts exist and build; live-deploy step is a human-verify checkpoint, not a code gap).

### Deferred Items (addressed in later phases or by human)

| # | Item | Addressed In | Evidence |
|---|------|--------------|----------|
| 1 | SC-5 live deploy (Actions green → Portainer recreate → container healthy → /health → end-to-end chat) | Human verification, post-push | 02-08-SUMMARY.md §Post-push verification (10 steps); STATE.md §Session Continuity; 02-PATTERNS.md §Human-verify checkpoint |
| 2 | Cold-storage audit export + retention drop | Phase 7 OR Phase 10 | 02-09-PLAN.md `optional: true`, `deferred_to: "Phase 7 ... OR Phase 10"`; empty `requirements: []`; Codex review [HIGH] 02-09 scope creep ruling |
| 3 | `-race` auth test budget | Next CI-timing plan | 02-deferred-items.md §1 (argon2id serialized under -race; fix via `testing.Short()` or integration-tag split) |

### Required Artifacts (Level 1-3 verification)

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `gateway/cmd/gateway/main.go` | HTTP server + signal handling + full wiring (pool, redis, auth, audit, idem, proxies, health) | VERIFIED | 381 lines; wires pgxpool → Redis → verifier → TouchBuffer → audit writer → AuditInterceptor → model resolver → 3 proxies → idem store → chi router; SIGTERM/SIGINT → graceful shutdown 25s; `--self-check` flag for Docker healthcheck |
| `gateway/cmd/gatewayctl/main.go` + `tenant.go` + `key.go` + `migrate.go` | Admin CLI with tenant/key/migrate subcommands | VERIFIED | `key create` generates `ifix_sk_` + 32-base32, argon2id hashes + stores, prints raw once; `key revoke <id>` flips status; `tenant create --name --slug`; `migrate up/down/status` via goose |
| `gateway/db/migrations/0001..0006` | 6 migrations: tenants + api_keys + audit_log (partitioned) + audit_log_content (partitioned) + model_aliases + usage_counters | VERIFIED | All 6 files present, all `SET search_path = ai_gateway, public`, all use `-- +goose Up/Down StatementBegin/End`; seed `converseai` tenant; seed 3 model aliases; key_lookup_hash UNIQUE index for bounded-argon2 hot path |
| `gateway/internal/auth/*` | argon2id + SHA-256 lookup + positive+negative Redis cache + TouchBuffer | VERIFIED | `apikey.go` (Verifier, ExtractKey respects D-A5 Bearer>X-API-Key), `argon2.go` (OWASP 2026 params), `cache.go` (60s positive, 5s negative TTL), `touch_buffer.go` (debounced 60s flush, 2 counters); hot path asserts ≤1 argon2 per request via `GetActiveKeyByLookupHash` |
| `gateway/internal/proxy/{chat,embeddings,audio,director,interceptor}.go` | Reverse proxies for 3 OpenAI endpoints + shared director stripping auth headers + ProxyResponseInterceptor extension point | VERIFIED | Chat has `FlushInterval:-1` (SSE); embeddings/audio default buffered; `BuildDirector` strips `Authorization`, `X-API-Key`, `Cookie`; propagates gateway UUIDv7 as upstream `X-Request-ID`; `ComposeInterceptors` plugs in audit tee without mutating ReverseProxy struct |
| `gateway/internal/audit/{writer,tee,interceptor,middleware}.go` | Async buffered audit with 128 KB SSE tee, data_class-gated content row | VERIFIED | `writer.go` buffer=1000, flush=500/1s (D-B4); `tee.go` cap 128 KB + `truncated` flag (D-B5); `interceptor.go` implements `proxy.ProxyResponseInterceptor`; `middleware.go` captures non-SSE body; `IdempotencyReplayedSetter` interface wires idem → audit cross-plan contract |
| `gateway/internal/idempotency/*` | Redis-backed idempotency with SET NX EX first-writer-wins + 24h TTL + 422 on body mismatch | VERIFIED | `store.go` AcquireLock via SETNX; `hash.go` canonical JSON SHA-256; `middleware.go` mounts only on chat, 400 on `stream:true` + embeddings/audio (D-C4), 422 on hash mismatch, `X-Idempotency-Replayed: true` + `SetIdempotencyReplayed(true)` on replay |
| `gateway/internal/models/{resolver,rewrite}.go` | Model alias resolution with (alias, upstream) composite key | VERIFIED | `resolver.go` reads `ai_gateway.model_aliases` at boot, refresh loop; `rewrite.go` + `Handler` wraps chat + embed handlers; Integration_05_ModelAlias asserts `{"model":"qwen"}` forwards resolved to upstream |
| `gateway/internal/upstreams/health.go` | `/v1/health/upstreams` aggregates health-bridge :9100, 5s in-memory cache | VERIFIED | Calls `${UPSTREAM_HEALTH_BRIDGE_URL}/health`, caches 5s, returns 503 on unreachable with OpenAI envelope + pod health contract shape |
| `gateway/internal/config/config.go` | Fail-fast env var load | VERIFIED | Loads all 6 required env vars (AI_GATEWAY_PG_DSN, AI_GATEWAY_REDIS_ADDR, UPSTREAM_LLM_URL, UPSTREAM_STT_URL, UPSTREAM_EMBED_URL, UPSTREAM_HEALTH_BRIDGE_URL); `os.Exit(2)` with missing-var error list |
| `gateway/internal/httpx/{requestid,redact,envelope,logger,recoverer}.go` | chi middleware stack + OpenAI error envelope + slog redactor | VERIFIED | `RequestID` UUIDv7 + client-ID kept as `client_request_id`; `Redactor` slog-wrapper redacts Authorization/X-API-Key/Cookie/Proxy-Authorization; `WriteOpenAIError` uses `pkg/openai.ErrorResponse` (no local redefinition) |
| `gateway/internal/obs/{metrics,sentry,version}.go` | Prometheus counters + Sentry BeforeSend redaction + BuildVersion injection | VERIFIED | `gateway_requests_total{route,status}` + `gateway_audit_dropped_total` + `gateway_apikey_touch_buffered_total` + `gateway_apikey_touch_flush_total`; Sentry no-op when DSN unset |
| `gateway/Dockerfile` | 2-stage distroless, /gateway + /gatewayctl, ≤20 MB target (actual 27.7 MB documented) | VERIFIED | `golang:1.23-alpine` builder → `gcr.io/distroless/static-debian12` runtime; includes tzdata fix for builder apk; actual size 27.7 MB — +38% over target but documented rationale in 02-08-SUMMARY.md key-decisions |
| `gateway/docker-compose.yml` | Portainer stack template, ${VAR}-only, traefik-public | VERIFIED | No inline secrets; 11 env vars declared; `ports: 8080:8080`; `networks: traefik-public external: true`; healthcheck uses `/gateway --self-check` |
| `.github/workflows/build-gateway.yml` | 7-job pipeline mirroring build-pod.yml + Portainer webhook deploys | VERIFIED | Jobs: test → integration-test → compute-tags → build-gateway → deploy-dev → deploy-prod → summary; paths filter only on pull_request (D-17); PORTAINER_WEBHOOK_URL_{DEV,PROD}_GATEWAY secrets consumed with empty-secret graceful skip |

### Data-Flow Trace (Level 4)

| Artifact | Data Variable | Source | Produces Real Data | Status |
|----------|---------------|--------|--------------------|--------|
| `audit.Writer.ch` | `Event` channel | `audit.Middleware` + `AuditInterceptor` | Yes — every authed request produces `Event` via captureWriter/TeeBody; `Run` goroutine drains to Postgres CopyFrom | FLOWING |
| `auth.Verifier.redis` positive cache | JSON `{tenant_id, api_key_id, data_class, status, key_prefix}` | Cache miss → `GetActiveKeyByLookupHash` (real Postgres) | Yes — migration 0002 UNIQUE INDEX + sqlc query | FLOWING |
| `models.Resolver.cache` | `(alias, upstream) → target` map | Postgres `ai_gateway.model_aliases` seeded in migration 0005 | Yes — 3 rows seeded; `Refresh(ctx)` fail-fast on boot | FLOWING |
| `upstreams.health.cache` | `cachedResponse` | Real HTTP call to `${UPSTREAM_HEALTH_BRIDGE_URL}/health` | Yes — 2s probe budget + 5s cache; wire-level contract matches Phase 1 pod health-bridge | FLOWING |
| `idempotency.Store.rdb` | Entry JSON `{status, headers, body, request_hash, stored_at}` | Real Redis with SET NX EX sentinel lifecycle | Yes — winner `SET` overwrites IN_FLIGHT on success; winner `DEL`s on 5xx; 24h TTL | FLOWING |

### Behavioral Spot-Checks

| Behavior | Command | Result | Status |
|----------|---------|--------|--------|
| `go build ./gateway/...` succeeds | `PATH=/home/pedro/.local/go/bin:$PATH go build ./gateway/...` | 0 errors, 0 warnings | PASS |
| All unit tests green (single-pass) | `PATH=/home/pedro/.local/go/bin:$PATH go test ./gateway/... -count=1` | All 12 packages OK (auth: 100.7s, gatewayctl: 7.2s, rest <6s) | PASS |
| `cmd/gateway` has `--self-check` flag | `grep selfCheck /home/pedro/projetos/pedro/gpu-ifix/gateway/cmd/gateway/main.go` | Flag declared at line 64, exits 0 on "ok" | PASS |
| Integration tests gated by `//go:build integration` | `grep -r "//go:build integration" gateway/internal/integration_test/` | All 13 test files gated — default `go test ./...` skips them | PASS |
| Integration tests documented as passing | 02-07-SUMMARY.md | 12 integration tests including goroutine-leak, concurrent-idempotency, partition-automation, auth-hotpath-under-load — all green per SUMMARY (not re-run in this verification per verifier instructions) | PASS |

### Requirements Coverage

| Requirement | Source Plan | Description | Status | Evidence |
|-------------|-------------|-------------|--------|----------|
| GW-01 | 02-01 | Binary single with chi+ReverseProxy+slog | SATISFIED | `cmd/gateway/main.go` + `go.mod` imports go-chi/chi/v5 + net/http/httputil; obs.BuildVersion injected |
| GW-02 | 02-04 | `POST /v1/chat/completions` OpenAI-compat + SSE FlushInterval:-1 | SATISFIED | `proxy/chat.go:35`; `TestChatProxy_SSEStreamingFlushesPerChunk` asserts per-chunk flush |
| GW-03 | 02-04 | `POST /v1/embeddings` | SATISFIED | `proxy/embeddings.go`; mounted at `cmd/gateway/main.go:322` |
| GW-04 | 02-04 | `POST /v1/audio/transcriptions` multipart | SATISFIED | `proxy/audio.go` preserves boundary; MaxBytesHandler 25 MB in main.go |
| GW-05 | 02-01 + 02-05 | `GET /health` + `GET /v1/health/upstreams` | SATISFIED | `/health` at `main.go:277`; `/v1/health/upstreams` at `upstreams/health.go` mounted at main.go:325 |
| GW-06 | 02-04 | Tool/function calling pass-through | SATISFIED | `proxy/chat.go` is pure pass-through (no body parsing); `chat_test.go:TestChatProxy_ToolCallPassthrough` asserts tools + tool_calls forwarded as-is |
| GW-07 | 02-05 | Model alias mapping | SATISFIED | `models/resolver.go` + migration 0005 seeds 3 aliases; `models/rewrite.go` Handler wraps chat + embed in `main.go:300-305`; Integration_05_ModelAlias |
| GW-08 | 02-01 | UUID `X-Request-ID` echoed + in logs | SATISFIED | `httpx/requestid.go:RequestID` generates UUIDv7, sets response header; `httpx/logger.go` attaches to slog |
| GW-09 | 02-08 | Docker Compose + Portainer + webhook GitHub | SATISFIED (artifacts); LIVE-DEPLOY HUMAN-VERIFY | `gateway/Dockerfile` + `docker-compose.yml` + `.github/workflows/build-gateway.yml` all present and build locally; live deploy pending operator push |
| GW-10 | 02-02 | Postgres schema `ai_gateway` with tenants/api_keys/audit_log/billing_events/usage_counters + versioned migrations | SATISFIED | 6 migrations in `gateway/db/migrations/`, all use `ai_gateway` schema; goose versioning embedded via `//go:embed`; `billing_events` is explicitly Phase 4 per CONTEXT.md §Deferred (usage_counters skeleton exists in migration 0006) |
| TEN-01 | 02-03 | API key auth `Bearer` or `X-API-Key` + Postgres lookup + Redis cache | SATISFIED | `auth/apikey.go:ExtractKey` (Authorization>X-API-Key per D-A5); `auth/cache.go` Redis positive+negative cache; `auth/apikey.go:Verify` flow matches hot path contract |
| TEN-02 | 02-03 | API key → tenant + `data_class` | SATISFIED | `AuthContext{TenantID, APIKeyID, DataClass}`; `gatewayctl key create --data-class {normal\|sensitive}` |
| TEN-08 | 02-01 (envelope) + 02-03 (auth) + 02-04 (proxy) + 02-06 (idem) | Error format consistent with OpenAI for 401/403/429/5xx | SATISFIED | All error paths route through `httpx.WriteOpenAIError` → `pkg/openai.ErrorResponse`; auth → 401 + `authentication_error`; idem conflict → 422 + `idempotency_conflict`; upstream down → 502 + `api_error`/`upstream_unreachable` |
| TEN-09 | 02-06 | Idempotency-Key header support | SATISFIED | `idempotency/middleware.go` on chat-only; 24h TTL; SET NX EX serialization; 422 on body mismatch; Integration_04_IdempotencyFlow + Integration_09_ConcurrentIdempotency exercise real Redis |

**Coverage:** 14/14 in-scope requirements SATISFIED.

### Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
|------|------|---------|----------|--------|
| (none) | — | — | — | Build is clean; no TODO/FIXME/PLACEHOLDER blockers found in production paths; scaffoldNotImplemented (501) is an intentional fallback at `main.go:347-351` for nil-verifier test variant, not a stub |

**Notable finding (non-blocking):** `.planning/REQUIREMENTS.md` §Traceability is STALE — marks GW-03, GW-04, GW-05, GW-06 as "Pending" even though 02-04 + 02-05 PLANs + SUMMARYs list them as complete and the code ships them (`proxy/embeddings.go`, `proxy/audio.go`, `upstreams/health.go`). ROADMAP.md §Phase 2 Requirements assigns GW-01..GW-10 + TEN-01/02/08/09 to Phase 2 — which matches the code. Recommend updating REQUIREMENTS.md traceability after this verification (single-line change per requirement).

### Human Verification Required

See frontmatter `human_verification` block. The one remaining item is the post-push live-deploy checklist documented in 02-08-SUMMARY.md §Post-push verification (10 steps): Actions green → GHCR image → Portainer webhook → container healthy → `/health` 200 → migrate status shows 6 applied → `gatewayctl tenant create` + `key create` → end-to-end chat via real pod → 401 on unauth → audit row in Postgres.

### Gaps Summary

None. Every Phase 2 success criterion has matching production code, every in-scope requirement (GW-01..GW-10, TEN-01, TEN-02, TEN-08, TEN-09) is satisfied by implementation that compiles, passes unit tests (12 packages green, argon2id auth suite ~100s single-core), and is backed by integration tests documented as green in 02-07-SUMMARY.md. SC-5 is split: the ARTIFACTS (Dockerfile, docker-compose, build-gateway.yml) are all present, build, and validated locally; the LIVE DEPLOY is an operator-owned human-verify checkpoint (02-08 Task 3 explicitly deferred). Plan 02-09 (cold-storage export) is out-of-scope for Phase 2 (optional, requirements: []).

---

*Verified: 2026-04-18*
*Verifier: Claude (gsd-verifier, Opus 4.7 1M)*
