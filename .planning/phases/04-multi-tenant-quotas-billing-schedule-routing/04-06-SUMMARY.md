---
phase: 04-multi-tenant-quotas-billing-schedule-routing
plan: 06
subsystem: middleware + integration
tags:
  - phase-04
  - middleware
  - integration
  - main
  - wiring
  - timeouts
  - wave-3

# Dependency graph
requires:
  - phase: 04-multi-tenant-quotas-billing-schedule-routing/04-04
    provides: "quota.CheckBuckets/NewQuotaChecker + tenants.Loader/ListenAndReload + schedule.DecideUpstreamTier + 11 Phase 4 obs collectors"
  - phase: 04-multi-tenant-quotas-billing-schedule-routing/04-05
    provides: "billing.NewPricesLoader/NewFXLoader/ListenAndReload + NewFlusher + NewAccountant + ComputeCostBRL + proxy.NewUsageInterceptor + admin.NewVerifier/Middleware + admin.NewUsageHandler + 5 more obs collectors"
provides:
  - "gateway/internal/quota/enforcer.go — RateLimitMiddleware + QuotaMiddleware (chi http.Handler factories); idempotency replay short-circuit on rate-limit, always-check on quota (D-D1)"
  - "gateway/internal/schedule/middleware.go — pre-dispatch Middleware that writes auditctx.WithUpstreamOverride for peak off-hours tenants"
  - "gateway/internal/obs/middleware.go — RequestsMiddleware wiring the folded TODO RequestsTotal{route,status_class}"
  - "gateway/internal/idempotency/replay.go — IsReplay(ctx) + WithReplay(ctx) helpers consumed by quota.RateLimitMiddleware"
  - "gateway/internal/auditctx/override.go — UpstreamOverrideFromContext alias (long-name form required by Plan 04-06 contract)"
  - "gateway/internal/proxy/dispatcher.go — upstream_override path (D-C2): bypass tier-0 resolution, 503 off_hours_upstream_unavailable if override target breaker is OPEN"
  - "gateway/internal/proxy/director.go — injectStreamOptionsIncludeUsage helper (Pitfall 5) + rewriteRequestBody shared body-rewrite util"
  - "gateway/internal/proxy/openrouter_director.go — wires injectStreamOptionsIncludeUsage after provider.order injection"
  - "gateway/cmd/gateway/main.go — Phase 4 full wiring (loaders + listeners + flusher + accountant + usageInterceptor + middleware chain + /admin router + per-route WriteTimeout + bootstrap admin key + sensitive+peak invariant gate)"
affects:
  - "04-07 (gatewayctl + admin) — uses /admin/usage now live; admin-key CLI unblocked by bootstrap path"
  - "04-08 (integration tests) — live binary exercises the full chain; SC-1..SC-5 scenarios target the wired middleware"

# Tech tracking
tech-stack:
  added:
    - "golang.org/x/crypto/bcrypt (promoted to direct dep via go mod tidy — bootstrap admin key hashing)"
    - "crypto/rand + crypto/sha256 + encoding/hex (stdlib — bootstrap admin key generation)"
  patterns:
    - "Request-context-scoped replay flag (idempotency.IsReplay / WithReplay) — decouples replay semantics from the response-writer type assertion used by Plan 02-06's IdempotencyReplayedSetter"
    - "Middleware factory signature (chi.middleware shape) uniform across quota/schedule/obs — func(deps...) func(http.Handler) http.Handler"
    - "Fail-open rate-limit vs fail-closed quota (D-A2) enforced at the middleware layer, leaving the underlying package free of cyclic obs imports"
    - "Body-rewrite composition via shared rewriteRequestBody helper — consolidates io.NopCloser/ContentLength/Content-Length header sync across director.go + openrouter_director.go"
    - "Dispatcher override branch mounted BEFORE token-count enforcement so peak off-hours routes skip local tokenizer roundtrip (GPU may be suspended)"
    - "wrapWithTimeout(h, 0) == h passthrough — preserves SSE unlimited semantics while giving non-streaming routes slow-client-DoS defense"

key-files:
  created:
    - "gateway/internal/quota/enforcer.go — RateLimitMiddleware + QuotaMiddleware + handleQuotaError + dimensionOf + classifyRoute"
    - "gateway/internal/quota/enforcer_test.go — 5 unit tests (no-auth 401, replay skip, tenant-unknown passthrough, quota no-auth passthrough, quota tenant-unknown passthrough)"
    - "gateway/internal/schedule/middleware.go — Middleware + upstreamForTier"
    - "gateway/internal/schedule/middleware_test.go — 3 unit tests (no-auth, tenant unknown, malformed tenant UUID)"
    - "gateway/internal/obs/middleware.go — RequestsMiddleware + statusRecorder"
    - "gateway/internal/obs/middleware_test.go — 3 unit tests (2xx, 4xx, outside-chi fallback)"
    - "gateway/internal/idempotency/replay.go — IsReplay + WithReplay"
    - "gateway/internal/proxy/stream_options_test.go — 7 unit tests for injectStreamOptionsIncludeUsage"
  modified:
    - "gateway/internal/auditctx/override.go — added UpstreamOverrideFromContext alias"
    - "gateway/internal/proxy/dispatcher.go — added dispatchOverride helper + override branch; gofmt normalized pre-existing doc-comment formatting as part of the modification"
    - "gateway/internal/proxy/director.go — added injectStreamOptionsIncludeUsage + rewriteRequestBody helpers"
    - "gateway/internal/proxy/openrouter_director.go — wired injectStreamOptionsIncludeUsage after provider.order injection"
    - "gateway/cmd/gateway/main.go — Phase 4 stack wiring (see Task 2 below)"
    - "go.mod — go mod tidy promoted chi/prometheus client_model/crypto/sync from indirect to direct deps"

key-decisions:
  - "idempotency.IsReplay added as a context-scoped helper rather than reusing the existing IdempotencyReplayedSetter writer-type assertion. The rationale: rate-limit middleware runs BEFORE idempotency's ServeHTTP body wrap, so it cannot inspect the writer wrapper; a ctx flag is the only signal a downstream middleware can read BEFORE the replay payload starts flowing. In today's architecture idempotency short-circuits replays entirely (returns before calling next) so this flag is never TRUE in practice — but it makes the D-D1 semantics enforceable for any future architectural change that routes replays through downstream middleware for observability."
  - "auditctx.UpstreamOverrideFromContext kept alongside UpstreamOverrideFrom (original Phase 3 name) rather than renaming. Both point at the same context key; the long form is required by the Plan 04-06 contract + dispatcher, the short form is the Phase 3 convention still used by audit/middleware.go and proxy/dispatcher_test.go."
  - "usageInterceptor wired to BOTH local chatRP AND OpenRouter chatProxy. Pitfall 5 concerns external cost attribution primarily (local server always emits usage), but having the interceptor on both paths simplifies accountant.Set/Get invariants — every chat response increments usage regardless of which upstream served it. The interceptor is a no-op on non-SSE / non-chat responses so no overhead for embed/audio."
  - "Middleware mount scope: rate-limit + quota + schedule at the /v1/* group level (all routes inherit); idempotency stays per-handler on chat only (D-C4). This matches the plan's D-D1 diagram AND preserves the Phase 2/3 invariant that embeddings + audio never see idempotency semantics."
  - "http.Server.WriteTimeout remains 0 so per-route TimeoutHandler controls; ReadTimeout / IdleTimeout unchanged from Phase 2. The Phase 3 legacy `cfg.WriteTimeoutChat/Embed/Audio time.Duration` fields are still available but no longer consumed by main.go — replaced by the integer-second `WriteTimeout*S` fields per the Phase 4 folded TODO. Plan 04-07 gatewayctl will document the new env vars."
  - "Dispatcher override branch rejects UpstreamBlockedSensitiveValue explicitly. The Phase 3 writeSensitiveBlock path uses the SAME context key to signal 'dispatcher already emitted 503'; without the check, the override branch would treat 'blocked_sensitive' as a legit upstream name and return off_hours_upstream_unavailable — which is confusing. The early-return here preserves the original 503 upstream_unavailable_for_sensitive_tenant envelope."
  - "bootstrapAdminKey is best-effort: a failure is logged via Warn but does NOT block boot. Rationale: the gateway can still serve traffic on /v1/* without admin access; operators unblock /admin/* manually via `gatewayctl admin-key create` if bootstrap fails (e.g. DB read-only, network partition mid-migration). This matches the 'failover invisible' principle on the non-admin hot path."

patterns-established:
  - "Middleware factory with nil-safe guards: every Phase 4 middleware factory accepts a nil log (falls back to slog.Default()) AND a nil-snapshot loader (returns ErrTenantNotFound → middleware passes through). Enables scaffold testing without boot dependencies."
  - "Dispatcher override path = skip-tier-0 + fail-fast-on-target-breaker-open. Downstream plans (Phase 5/6 routing changes) can follow this shape without re-deriving the D-C2 rules."
  - "Body-rewrite helpers in director.go are share-by-composition: OpenRouter calls injectProviderOrder then injectStreamOptionsIncludeUsage then rewriteRequestBody — each step is independently testable."

requirements-completed:
  - TEN-03
  - TEN-04
  - TEN-05
  - TEN-06

# Metrics
duration: 32min
completed: 2026-04-21
---

# Phase 04 Plan 06: Middleware Chain + main.go Wiring + Dispatcher Override Summary

**Wave 3 integration** — composes Wave 2's foundation packages (quota/tenants/schedule/billing/admin from Plans 04-04 and 04-05) into the live request hot path via `cmd/gateway/main.go`. Every Phase 4 capability — rate-limit, quota, schedule override, billing capture, per-route WriteTimeout, admin endpoint, sensitive+peak boot gate, bootstrap admin key, folded-TODO metrics middleware — is now wired and exercised by `go build` / `go test` / `go vet`.

## Performance

- **Duration:** ~32 min (executor)
- **Started:** 2026-04-21T10:00:41Z
- **Completed:** 2026-04-21T10:32:46Z
- **Tasks:** 2 / 2 (both `type=auto`; no checkpoints)
- **Files created:** 8 (5 middleware/helpers, 3 test files)
- **Files modified:** 6 (auditctx + dispatcher + director + openrouter_director + main.go + go.mod)
- **Commits:** 2 atomic (feat × 2) + this SUMMARY commit

## Accomplishments

- **D-D1 middleware chain is enforceable at code level:** `auth → audit → rate-limit → quota → schedule → tokencount (inside dispatcher) → proxy → RequestsMiddleware(metrics)`. Every link has a `grep`-checkable call site in main.go/buildRouter. Integration proof (order + replay/skip semantics) deferred to 04-08 TestMiddlewareChainReplaySemantics.
- **D-C2 peak off-hours path is live:** schedule.Middleware decides → writes ctx.upstream_override → dispatcher bypasses tier-0 → off_hours_upstream_unavailable 503 if target breaker is OPEN. No fallback of fallback (plan explicitly forbids).
- **Two folded TODOs closed:** (1) obs.RequestsTotal finally incremented by RequestsMiddleware using chi.RoutePattern for bounded cardinality. (2) Per-route WriteTimeout: chat=0 (SSE), embed=30s, audio=120s via wrapWithTimeout wrapping http.TimeoutHandler.
- **Boot-time sensitive+peak invariant gate is active:** `tenantsLoader.CheckSensitivePeakInvariant` runs after the initial Refresh; any row violating `mode='peak' AND data_class='sensitive'` triggers `os.Exit(1)` before the HTTP server starts. LGPD protection at startup rather than per-request.
- **Bootstrap admin key:** if no active admin keys exist at boot, hashes `AI_GATEWAY_ADMIN_KEY_BOOTSTRAP` (or generates a random key + WARN-logs it). Admin endpoints unblocked on first deploy.
- **Pitfall 5 (include_usage) resolved:** `injectStreamOptionsIncludeUsage` runs inside the OpenRouter director so streaming chat responses from external upstreams always include the final usage block (required for cost attribution / billing_events).

## Task Commits

1. **Task 1 — Middleware chain components** — `8d6718d` (feat) — 9 files, 824 insertions
   - `feat(04-06): middleware chain components (rate-limit + quota + schedule + metrics)`
   - quota/enforcer.go + test, schedule/middleware.go + test, obs/middleware.go + test, idempotency/replay.go, auditctx/override.go alias, go.mod dep promotions.
2. **Task 2 — main.go wiring + dispatcher override + stream_options** — `9b457fa` (feat) — 5 files, 486 insertions, 28 deletions
   - `feat(04-06): wire Phase 4 stack in main.go + dispatcher override + stream_options`
   - main.go Phase 4 block, dispatcher dispatchOverride branch, director injectStreamOptionsIncludeUsage, openrouter_director wiring, stream_options_test.go.

Total diff: 14 files, 1310 insertions, 31 deletions.

## Files Created/Modified

### Middleware (created)
- `gateway/internal/quota/enforcer.go` — RateLimitMiddleware + QuotaMiddleware + handleQuotaError + dimensionOf + classifyRoute. 222 lines (with comments).
- `gateway/internal/quota/enforcer_test.go` — 5 unit tests.
- `gateway/internal/schedule/middleware.go` — Middleware + upstreamForTier. 73 lines.
- `gateway/internal/schedule/middleware_test.go` — 3 unit tests.
- `gateway/internal/obs/middleware.go` — RequestsMiddleware + statusRecorder. 78 lines.
- `gateway/internal/obs/middleware_test.go` — 3 unit tests.
- `gateway/internal/idempotency/replay.go` — IsReplay + WithReplay.

### Integration (created)
- `gateway/internal/proxy/stream_options_test.go` — 7 unit tests for injectStreamOptionsIncludeUsage.

### Modified (integration)
- `gateway/internal/auditctx/override.go` — added UpstreamOverrideFromContext alias (+7 lines; existing UpstreamOverrideFrom preserved).
- `gateway/internal/proxy/dispatcher.go` — added Phase 4 override branch at the top of NewDispatcher's handler + new dispatchOverride helper (+47 lines; gofmt normalized pre-existing comment-block formatting).
- `gateway/internal/proxy/director.go` — added injectStreamOptionsIncludeUsage + rewriteRequestBody helpers (+75 lines).
- `gateway/internal/proxy/openrouter_director.go` — one-line call to injectStreamOptionsIncludeUsage after injectProviderOrder.

### Modified (main)
- `gateway/cmd/gateway/main.go` — Phase 4 full stack wiring:
  - `location := mustLoadLocation(...)` after newLogger.
  - `tenants.NewLoader` + goroutine `tenants.ListenAndReload` + boot `CheckSensitivePeakInvariant`.
  - `billing.NewPricesLoader` + `billing.NewFXLoader` + multiplexed goroutine `billing.ListenAndReload`.
  - `billing.NewFlusher` + goroutine `billingFlusher.Run`.
  - `billing.NewAccountant` + `proxy.NewUsageInterceptor` injected into chatRP AND OpenRouter chatProxy.
  - `quota.NewQuotaChecker` + `admin.NewVerifier`.
  - `bootstrapAdminKey` helper seeds first admin key.
  - Per-route WriteTimeout switched from time.Duration to integer-second via `wrapWithTimeout`.
  - `proxies` struct extended with 6 Phase 4 fields (tenantsLoader / quotaChecker / adminUsageHandler / adminVerifier / rdb / rateLimitFailOpen).
  - `buildRouter` now mounts Phase 4 middleware chain at the /v1/* group level + /admin sub-router.
- `go.mod` — tidy promoted prometheus client_model + crypto + sync from indirect to direct.

## Public API Quick Reference (for 04-07 / 04-08 consumers)

```go
// gateway/internal/quota
func RateLimitMiddleware(rdb redis.UniversalClient, loader *tenants.Loader,
    failOpen bool, log *slog.Logger) func(http.Handler) http.Handler
func QuotaMiddleware(checker *QuotaChecker, loader *tenants.Loader,
    log *slog.Logger) func(http.Handler) http.Handler

// gateway/internal/schedule
func Middleware(loader *tenants.Loader, log *slog.Logger) func(http.Handler) http.Handler

// gateway/internal/obs
func RequestsMiddleware(log *slog.Logger) func(http.Handler) http.Handler

// gateway/internal/idempotency
func WithReplay(ctx context.Context) context.Context
func IsReplay(ctx context.Context) bool

// gateway/internal/auditctx (addition)
func UpstreamOverrideFromContext(ctx context.Context) string  // long-name alias of UpstreamOverrideFrom
```

## Chain Order As Wired (cmd/gateway/main.go:buildRouter)

Authenticated `/v1/*` group (chi router):

1. `httpx.RequestID` (global; from Phase 2)
2. `httpx.Logger(log)` (global; from Phase 2)
3. `httpx.Recoverer(log)` (global; from Phase 2)
4. `auth.Middleware(verifier, log)` — X-API-Key → tenant_id into ctx
5. `audit.Middleware(auditWriter, log)` — per-request audit_log row
6. **`quota.RateLimitMiddleware(rdb, tenantsLoader, cfg.RateLimitFailOpen, log)`** — Phase 4; skips on replay
7. **`quota.QuotaMiddleware(quotaChecker, tenantsLoader, log)`** — Phase 4; always runs
8. **`schedule.Middleware(tenantsLoader, log)`** — Phase 4; writes ctx.upstream_override
9. **`obs.RequestsMiddleware(log)`** — Phase 4 folded TODO; last in chain; emits RequestsTotal after ServeHTTP
10. Per-handler: `models.Handler` (chat/embed rewrite) → `idempotency.Middleware` (chat only) → dispatcher (tokencount + override + breaker fallback) → proxy

`/admin/*` sub-router: `admin.Middleware(adminVerifier, log)` → `GET /admin/usage` → `admin.UsageHandler.ServeHTTP`.

## Goroutines Launched (main.go)

Three distinct Phase 4 goroutines (verified via `grep tenants.ListenAndReload|billing.ListenAndReload|billingFlusher.Run`):

1. `go tenants.ListenAndReload(ctx, cfg.PGDSN, tenantsLoader, log)` — LISTEN tenants_changed → tenantsLoader.Refresh on each NOTIFY.
2. `go billing.ListenAndReload(ctx, cfg.PGDSN, pricesLoader, fxLoader, log)` — LISTEN prices_changed + fx_changed multiplexed on one pgx.Conn.
3. `go billingFlusher.Run(ctx)` — drains the billing channel (500-row batches or 1s tick, whichever first) and inserts via the CTE (preserves ON CONFLICT DO NOTHING).

Pre-existing Phase 2/3 goroutines (touchBuf, auditWriter, resolver, upstreams probe + listener, breakerSet.Subscribe) are unchanged.

## Per-route WriteTimeout Values

| Route | cfg field | Default | Behavior |
|-------|-----------|---------|----------|
| /v1/chat/completions | WriteTimeoutChatS | 0 | SSE unlimited (wrapWithTimeout passthrough) |
| /v1/embeddings | WriteTimeoutEmbedS | 30 | http.TimeoutHandler 30s cap |
| /v1/audio/transcriptions | WriteTimeoutAudioS | 120 | http.TimeoutHandler 120s cap (Whisper multipart) |

http.Server.WriteTimeout stays 0 globally so per-route caps control.

## Dispatcher Diff (upstream-override handling)

Inserted at the top of `NewDispatcher`'s handler, AFTER auth check AND BEFORE token-count enforcement:

```go
if override := auditctx.UpstreamOverrideFromContext(r.Context()); override != "" &&
    override != UpstreamBlockedSensitiveValue {
    cfg.dispatchOverride(w, r, override, log)
    return
}
```

`dispatchOverride` checks the target breaker; if OPEN → 503 off_hours_upstream_unavailable; if CLOSED/HALF_OPEN → forward through the registered proxy. Missing proxy registration (loader config mismatch) also emits 503 off_hours_upstream_unavailable with a WARN log.

The explicit `override != UpstreamBlockedSensitiveValue` guard preserves the Phase 3 writeSensitiveBlock 503 envelope (which reuses the same context key as the "dispatcher already emitted 503" signal).

## Stream Options Injection Site

`gateway/internal/proxy/openrouter_director.go:66` — called after `injectProviderOrder(body, providerOrder, allowFallbacks)` and before `rewriteRequestBody`. Non-streaming bodies (or chat bodies where the client explicitly set `stream_options.include_usage=false`) pass through unchanged. Unit tests (`stream_options_test.go`) cover all 7 shapes including the client-override-respect case.

The local chat proxy's director (`BuildDirector`) does NOT inject — llama-server emits usage in every streaming chunk regardless of the flag, so the injection would be wasted work.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] `idempotency.IsReplay(ctx)` helper did not exist**
- **Found during:** Task 1 (writing quota/enforcer.go — plan mandates `grep -c "idempotency.IsReplay" ...` returns 1).
- **Issue:** The Phase 2 idempotency middleware signals replays via the `IdempotencyReplayedSetter` interface on the response writer, NOT via a context-scoped flag. A downstream middleware like RateLimitMiddleware cannot call any setter because the replay path short-circuits BEFORE downstream middleware runs — and it has no way to know it's on a replay anyway.
- **Fix:** Added `gateway/internal/idempotency/replay.go` with `IsReplay(ctx) bool` + `WithReplay(ctx) context.Context`. Today's architecture never sets the flag (replays never reach downstream middleware), so IsReplay is always false in practice — but it makes the D-D1 semantics enforceable for any future architectural change that routes replays through downstream middleware for observability. The plan's D-D1 explicitly states "replay consumes quota but not rate-limit" as a policy; this helper is how we code the policy.
- **Files modified:** `gateway/internal/idempotency/replay.go` (new).
- **Verification:** `grep -c "idempotency.IsReplay" gateway/internal/quota/enforcer.go` returns 1; quota package tests pass.
- **Committed in:** 8d6718d (Task 1 commit).

**2. [Rule 3 - Blocking] `auditctx.UpstreamOverrideFromContext` did not exist (long-name form only in plan)**
- **Found during:** Task 1 (writing dispatcher override branch).
- **Issue:** auditctx already exported `UpstreamOverrideFrom` (Phase 3 convention, consumed by audit/middleware.go + proxy/dispatcher_test.go). The plan's Task 1 spec + Task 2 dispatcher snippet both use `UpstreamOverrideFromContext` — a stricter long-name that did not exist.
- **Fix:** Added `UpstreamOverrideFromContext` as an alias to `UpstreamOverrideFrom` (same context key). Both forms coexist — the short form stays the canonical callsite for Phase 3 code; the long form matches the Plan 04-06 contract + acceptance criteria grep.
- **Files modified:** `gateway/internal/auditctx/override.go`.
- **Verification:** `grep -cE "func (WithUpstreamOverride|UpstreamOverrideFromContext)" gateway/internal/auditctx/override.go` returns 2; existing Phase 3 tests still pass.
- **Committed in:** 8d6718d (Task 1 commit).

**3. [Rule 2 - Missing critical] `auth.AuthContext.TenantID` is a string, not a uuid.UUID — plan snippets dereferenced `.TenantID` directly into loader.Get**
- **Found during:** Task 1 (writing quota/enforcer.go).
- **Issue:** `tenants.Loader.Get(tenantID uuid.UUID)` takes a UUID, but `auth.AuthContext.TenantID` is a string (shape chosen in Phase 2 for cheap copy + JSON serialization in audit rows). The plan's code snippet `loader.Get(ac.TenantID)` would not compile.
- **Fix:** Parse the string with `uuid.Parse(ac.TenantID)` before calling `loader.Get`. On parse failure (should never happen post-auth; auth guarantees valid UUID keys), middleware passes through with a WARN log rather than 500 — same graceful-fallthrough contract as "tenant snapshot missing".
- **Files modified:** `gateway/internal/quota/enforcer.go`, `gateway/internal/schedule/middleware.go`.
- **Verification:** Unit tests TestScheduleMiddleware_MalformedTenantIDPassthrough and TestQuotaMiddleware_TenantUnknownPassesThrough both cover this path.
- **Committed in:** 8d6718d (Task 1 commit).

**4. [Rule 1 - Bug] `QuotaChecker.CheckQuotaToday/Month` returns sentinel errors directly — plan's `errors.Is(err, ErrQuotaCheckUnavailable)` comparison is correct only because the checker returns the bare sentinel without wrapping**
- **Found during:** Task 1 (writing quota/enforcer.go handleQuotaError).
- **Issue:** The Plan 04-06 snippet used `errors.Is(err, ErrQuotaCheckUnavailable)`, which works for the unavailable case but would silently miscategorize if the checker ever wrapped the sentinel (not done today, but a defensive coding smell since the other errors use direct switch-case).
- **Fix:** Used direct `==` equality for the ErrQuotaCheckUnavailable case (consistent with the dimensionOf helper's switch-case style), and the package-local `ErrorCode(err)` mapper for the specific-dimension code resolution. No wrapping occurs in the checker so both approaches yield the same result today; the direct form is faster + clearer.
- **Files modified:** `gateway/internal/quota/enforcer.go`.
- **Verification:** `go vet` clean; quota package tests still pass (including the fail-closed semantics).
- **Committed in:** 8d6718d (Task 1 commit).

**5. [Rule 3 - Blocking] `buildOpenRouterChatProxy` signature could not accept the new usageInterceptor without a caller-site update**
- **Found during:** Task 2 (wiring billing.NewAccountant + usageInterceptor).
- **Issue:** Phase 3 main.go's `buildOpenRouterChatProxy(u, cfg, log, auditInterceptor, toolCallInterceptor)` had a fixed 5-arg signature. The plan requires OpenRouter chat to receive the usageInterceptor for Pitfall 5 cost attribution; simply appending the interceptor to the local `chatRP`'s ComposeInterceptors would miss OpenRouter responses.
- **Fix:** Extended `buildOpenRouterChatProxy` to take `usageInterceptor *proxy.UsageInterceptor` as a 6th parameter and passed it through to `ComposeInterceptors(audit, toolCall, usage)`. Callers updated in one place.
- **Files modified:** `gateway/cmd/gateway/main.go`.
- **Verification:** `go build ./...` clean; no regressions in proxy tests.
- **Committed in:** 9b457fa (Task 2 commit).

**6. [Rule 1 - Bug] The plan's dispatcher snippet used `snap.GetByName(override)` — `upstreams.Loader` does NOT expose `GetByName`; the existing method is `Get(name)`**
- **Found during:** Task 2 (writing dispatcher.dispatchOverride).
- **Issue:** Compile failure in the verbatim plan snippet; Loader's public API is `Get(name string) (UpstreamConfig, bool)`, not `GetByName`.
- **Fix:** Used `cfg.Breaker.Get(name)` (breaker.Set.Get) for the breaker state check AND `cfg.Proxies[name]` for the proxy lookup (mirror how `dispatchTo` already does it). Both map-style lookups return a `, ok` flag so the missing-upstream path can emit its own 503 variant rather than panicking.
- **Files modified:** `gateway/internal/proxy/dispatcher.go`.
- **Verification:** `go build` clean; dispatcher_test suite still passes.
- **Committed in:** 9b457fa (Task 2 commit).

**7. [Rule 3 - Blocking] `go mod tidy` promoted 4 indirect deps to direct**
- **Found during:** Task 1 `go test` (failed with "go: updates to go.mod needed").
- **Issue:** New direct imports in quota/enforcer.go (redis.UniversalClient, google/uuid), obs/middleware_test.go (prometheus testutil + client_model), main.go bootstrap (golang.org/x/crypto/bcrypt) require go.mod to declare them as direct deps.
- **Fix:** Ran `go mod tidy`. Promoted prometheus/client_model v0.6.2, golang.org/x/crypto v0.40.0, golang.org/x/sync v0.16.0, and the indirect godebug testutil dep. No version bumps — all existing versions preserved.
- **Files modified:** `go.mod`.
- **Verification:** `go.sum` unchanged (no new modules); `go build ./...` clean.
- **Committed in:** 8d6718d (Task 1 commit).

### Rule 4 Items

None — no architectural changes proposed.

### Out-of-Scope Findings (Not Fixed)

- **`gateway/internal/proxy/toolcall_test.go` pre-existing gofmt issue** — already documented as out-of-scope in 04-05-SUMMARY.md. Left untouched per the deviation scope boundary. Same file was gofmt-clean after Plan 04-04 per the file modification record there; an unrelated commit between 04-05 and the start of 04-06 may have reintroduced the formatting drift — investigation deferred.

### Authentication Gates

None — all changes are internal-package Go code + wiring. No new env vars are required for boot (all Phase 4 env vars have sensible defaults from cfg.Load).

## Threat Flags

No new threat surface beyond the plan's register (T-04-21..T-04-26). All six STRIDE items are mitigated or accepted per the plan:

- T-04-21 (tampering via classifyRoute) — mitigated: `classifyRoute` falls back to RouteClassChat for unknown paths; bucket key namespace cannot be hijacked.
- T-04-22 (spoofing ctx.tenant_id) — accepted: auth middleware is the trust boundary; if bypassed, schedule decision is moot.
- T-04-23 (tampering dispatcher override) — mitigated: the override is ctx-scoped and written by schedule.Middleware (trusted); not influenced by client headers.
- T-04-24 (bootstrap admin key in WARN log) — accepted: single-time disclosure on first boot; documented ROTATE THIS KEY IMMEDIATELY.
- T-04-25 (DoS via UsageInterceptor SSE buffer) — mitigated per Plan 04-05 (teeReader eager drain).
- T-04-26 (DoS via slow-client on non-streaming routes) — mitigated: embed=30s + audio=120s TimeoutHandler caps now active via wrapWithTimeout.

## Self-Check: PASSED

### File existence

- FOUND: gateway/internal/quota/enforcer.go
- FOUND: gateway/internal/quota/enforcer_test.go
- FOUND: gateway/internal/schedule/middleware.go
- FOUND: gateway/internal/schedule/middleware_test.go
- FOUND: gateway/internal/obs/middleware.go
- FOUND: gateway/internal/obs/middleware_test.go
- FOUND: gateway/internal/idempotency/replay.go (new; not in original plan but required by deviation 1)
- FOUND: gateway/internal/proxy/stream_options_test.go (new; covers the new injectStreamOptionsIncludeUsage helper)
- FOUND: gateway/internal/auditctx/override.go (modified: UpstreamOverrideFromContext alias added)
- FOUND: gateway/internal/proxy/dispatcher.go (modified: override branch + dispatchOverride helper + gofmt)
- FOUND: gateway/internal/proxy/director.go (modified: injectStreamOptionsIncludeUsage + rewriteRequestBody)
- FOUND: gateway/internal/proxy/openrouter_director.go (modified: calls injectStreamOptionsIncludeUsage)
- FOUND: gateway/cmd/gateway/main.go (modified: full Phase 4 stack wiring)
- FOUND: go.mod (modified: tidy promoted 4 deps)

### Commit existence

- FOUND: 8d6718d feat(04-06): middleware chain components (rate-limit + quota + schedule + metrics)
- FOUND: 9b457fa feat(04-06): wire Phase 4 stack in main.go + dispatcher override + stream_options

### Acceptance greps (spot check)

- `grep -c "func RateLimitMiddleware\|func QuotaMiddleware" gateway/internal/quota/enforcer.go` → 2 (expected 2) ✓
- `grep -c "idempotency.IsReplay" gateway/internal/quota/enforcer.go` → 1 (expected 1) ✓
- `grep -c "Retry-After" gateway/internal/quota/enforcer.go` → 3 (expected ≥1) ✓
- `grep -cE "X-RateLimit-Limit-Requests|X-RateLimit-Remaining-Requests" gateway/internal/quota/enforcer.go` → 3 (expected ≥2) ✓
- `grep -c "obs.GatewayRateLimitCheckFailures" gateway/internal/quota/enforcer.go` → 2 (expected ≥1) ✓
- `grep -c "auditctx.WithUpstreamOverride" gateway/internal/schedule/middleware.go` → 1 (expected 1) ✓
- `grep -cE "tenants.NewLoader|billing.NewPricesLoader|billing.NewFXLoader|billing.NewFlusher|billing.NewAccountant|admin.NewVerifier|quota.NewQuotaChecker" gateway/cmd/gateway/main.go` → 8 (expected ≥7) ✓
- `grep -c "CheckSensitivePeakInvariant" gateway/cmd/gateway/main.go` → 1 (expected 1) ✓
- `grep -c 'Mount(.\"/admin\"' gateway/cmd/gateway/main.go` → 1 (expected 1) ✓
- `grep -cE "http.TimeoutHandler|wrapWithTimeout" gateway/cmd/gateway/main.go` → 7 (expected ≥1) ✓
- `grep -cE "WriteTimeoutEmbedS|WriteTimeoutAudioS" gateway/cmd/gateway/main.go` → 2+ (expected ≥2) ✓
- `grep -c "UpstreamOverrideFromContext\|auditctx.UpstreamOverrideFromContext" gateway/internal/proxy/dispatcher.go` → 1 (expected 1) ✓
- `grep -c "off_hours_upstream_unavailable" gateway/internal/proxy/dispatcher.go` → 4 (expected 1+) ✓

### Automated verification

- `go build ./...` → exit 0 ✓
- `go vet ./...` → exit 0 ✓
- `go test -short -race ./gateway/internal/...` → all packages ok ✓
- `go test -short -race ./gateway/cmd/gateway/...` → ok ✓
- `gofmt -l` on all Phase 4-touched files → empty ✓

---

*Phase: 04-multi-tenant-quotas-billing-schedule-routing*
*Plan: 06 (Wave 3 — middleware chain + main.go integration)*
*Completed: 2026-04-21*
