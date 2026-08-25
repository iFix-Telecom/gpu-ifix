---
phase: quick-260824-ucv
plan: 01
subsystem: api
tags: [go, ai-gateway, reverse-proxy, fallback, tokenizer, prometheus, llama.cpp, openrouter]

requires:
  - phase: debug/HANDOFF-tokenizer-fail-open-pod-ctx-16k
    provides: measured root cause (guard inert by fail-open against a dead static tokenizer + no HTTP-status classifier in the chat pipeline)
provides:
  - "chatOverContextInterceptor: a tier-0 400/413 exceed_context_size_error becomes a routing signal (errOverContextFallthrough) instead of reaching the client"
  - "gateway_over_context_cascaded_total{tenant,upstream}: the cost/audit trail for every over-context cascade"
  - "TokenCounter.Enforce tokenizes against the EFFECTIVE tier-0 (the live pod under OverrideTier0), with the boot URL demoted to fallback"
  - "Pre-dispatch over-cap routing: an llm request that does not fit tier-0 goes straight to tier-1 without spending the doomed pod call"
  - "Over-context never opens the tier-0 breaker (big requests cannot divert all traffic to the paid provider)"
affects: [ai-gateway resilience, billing/cost observability, maestro/crm-dev chat reliability, Fix C guard-inert alerting]

tech-stack:
  added: []
  patterns:
    - "Sentinel wrapping: a new fallthrough cause wraps errUpstreamRetryable so the existing ErrorHandler suppression works unchanged while the dispatcher can still branch on the CAUSE via errors.Is"
    - "Dispatch-time facts stamped into ctx for ModifyResponse (streaming flag), since the request body is gone by then"
    - "Cache keys fingerprint the tokenizer ENDPOINT, not just the model"

key-files:
  created:
    - gateway/internal/proxy/overcontext.go
    - gateway/internal/proxy/overcontext_test.go
  modified:
    - gateway/internal/proxy/dispatcher.go
    - gateway/internal/proxy/tokencount.go
    - gateway/internal/proxy/chat.go
    - gateway/internal/proxy/dynamic_override.go
    - gateway/internal/proxy/errors.go
    - gateway/internal/obs/metrics.go

key-decisions:
  - "Sensitive tenants DO cascade on over-context (Pedro, 2026-08-24) — revokes plan invariant #1 for this error class only"
  - "The over-cap pre-dispatch cascade is an llm-role behaviour; embed keeps the explicit 400 even though seed 0008 defines a tier-1 embed upstream"
  - "openrouter-chat (tier-1) deliberately does NOT get the interceptor — its 400 is terminal, legitimate information"
  - "Over-context never feeds recordUpstreamFailure, in either cascade loop"

patterns-established:
  - "Upstream error envelopes are decoded tolerantly (code as json.RawMessage) because llama.cpp emits a NUMBER where OpenAI emits a STRING"
  - "Interceptors that peek a body restore it byte-identically via io.MultiReader on EVERY path, including non-matching ones"

requirements-completed: [FIX-A, FIX-B]

duration: 52min
completed: 2026-08-24
---

# Quick 260824-ucv: Over-context cascade + effective tier-0 tokenizer Summary

**A tier-0 `exceed_context_size_error` is now a routing signal that cascades to tier-1 instead of killing the client's turn, and the RES-07 guard tokenizes against the pod that will actually serve rather than a dead boot-time address.**

## Performance

- **Duration:** ~52 min
- **Tasks:** 3 of 3 (Task 3 is verification-only — no code changes)
- **Files modified:** 10 (2 created, 8 modified)
- **Commits:** 6

## Accomplishments

- **Fix A (recovery):** `chatOverContextInterceptor` classifies a tier-0 400/413 over-context envelope pre-byte and raises `errOverContextFallthrough`. The 63 `exceed_context_size_error` per 2h that were killing Maestro turns now become tier-1 responses.
- **Fix B (prevention):** `Enforce` receives the resolved tier-0 URL, so with `OverrideTier0` active the guard tokenizes against the live pod. Over-cap `llm` requests are routed to tier-1 pre-dispatch — the doomed pod call is never spent.
- **Cost visibility:** `gateway_over_context_cascaded_total{tenant,upstream}` increments on BOTH paths, always labelled with the tier-0 that could not serve. This is the alertable series for invisible external spend and, post-policy-change, the audit trail for sensitive payloads leaving the perimeter.
- **Breaker safety (T-ucv-05):** an over-context fallthrough is excluded from `recordUpstreamFailure` in both the tier-0 loop and `cascadeTier1`. Without this, a handful of big requests would open the pod's breaker and divert ALL traffic — including everything that fits — to the paid provider.

## Task Commits

1. **Task 1: Fix A — over-context as a routing signal** — `bc20cd5` (test, RED) → `f5be5de` (feat, GREEN) → `11b53d8` (feat, policy change)
2. **Task 2: Fix B — effective tier-0 tokenizer + pre-dispatch routing** — `2a91867` (test, RED) → `a014e5a` (feat, GREEN) → `c2dbec0` (fix, embed guard)
3. **Task 3: repo-wide gates + regression sweep** — no code changes (verification only)

## Files Created/Modified

- `gateway/internal/proxy/overcontext.go` (new) — sentinel, ctx streaming flag, interceptor with 5 eligibility gates, tolerant envelope classifier
- `gateway/internal/proxy/overcontext_test.go` (new) — eligibility matrix + body-restore contract (13 cases)
- `gateway/internal/proxy/dispatcher.go` — tier-0 Resolve moved ahead of enforcement; over-cap pre-dispatch routing; streaming flag stamped in `dispatchTo`; breaker-failure skip on over-context; header godoc corrected (step order + stale 16k→32k)
- `gateway/internal/proxy/tokencount.go` — `Enforce(..., tokenizeURL)`, URL fingerprint in the cache key, godoc rewrite
- `gateway/internal/proxy/chat.go` / `dynamic_override.go` — interceptor prepended on the TIER-0 chat proxies only
- `gateway/internal/proxy/errors.go` — `errUpstreamRetryable` godoc now documents both emitters and the D-07 constraint
- `gateway/internal/obs/metrics.go` — `OverContextCascadedTotal` with documented label semantics
- `gateway/internal/proxy/dispatcher_test.go`, `tokencount_test.go` — new/rewritten coverage

## Decisions Made

1. **The sentinel wraps `errUpstreamRetryable`** rather than being a peer. The `ErrorHandler` needed zero changes (write suppression + `fallthrough_` come free) while `errors.Is(res.err, errOverContextFallthrough)` still distinguishes the cause for the breaker decision.
2. **Streaming is decided at dispatch, read at ModifyResponse.** `IsStreamingRequest` cannot run inside the interceptor (body consumed), so `dispatchTo` stamps the flag. `dispatchOverride` does not stamp it, which makes that path ineligible for free — a second lock alongside the `dispatchResult` gate.
3. **`NewTokenCounter`'s signature was left alone** so `cmd/gateway/main.go` stayed out of the blast radius; only `Enforce` grew a parameter.
4. **Metric label `upstream` is always the tier-0 that failed**, never the tier-1 that served — otherwise the series cannot answer "which upstream is too small".

## Deviations from Plan

### 1. [Coordinator directive] Sensitive tenants now cascade on over-context

- **Found during:** Task 1 (mid-execution policy change from Pedro, relayed by the coordinator)
- **Decision:** Pedro, 2026-08-24. Rationale, verbatim: *"fallback client-side do n8n já manda pro OpenRouter sem billing/log — bloqueio no gateway era teatro"*. The clients already fall back straight to OpenRouter when the gateway errors, so blocking sensitive over-context at the gateway never kept the payload inside the perimeter — it only stripped billing, audit and metrics from the path.
- **Change:** the `data_class` gate was removed from the interceptor (gate (e) now only requires an AuthContext, for the tenant label) and from the pre-dispatch path. The dispatcher's `writeSensitiveBlock` hard gate is bypassed for over-context ONLY (`sensitive && !overCtx`).
- **Scope limit:** RES-08 is untouched for every other fallthrough cause — dial failure, breaker OPEN, shed. The existing sensitive tests for those paths stay green.
- **Tests:** inverted, not deleted. `TestOverContext_SensitiveAlsoCascades` and `TestDispatcher_OverContext400_SensitiveAlsoCascades` / `TestDispatcher_OverCap_SensitiveAlsoCascades` now assert the cascade AND the tenant-labelled metric (the audit trail).
- **Committed in:** `11b53d8` (Fix A), `a014e5a` (Fix B)

### 2. [Rule 1 - Bug in the plan's mechanism] Embed over-cap would have cascaded

- **Found during:** Task 3 (regression sweep)
- **Issue:** the plan gated the pre-dispatch cascade on `len(ResolveAllTier1(role)) == 0`, explicitly intending to preserve the embed 400 because "BGE-M3 8192 é físico". That test does not hold: `gateway/db/migrations/0008_seed_upstreams.sql:11` defines `openai-embed` at tier 1. An over-cap embed request would therefore have been shipped to an external provider only to be rejected there too (`text-embedding-3-small` caps at 8191) — external payload exposure and latency for zero benefit.
- **Fix:** the over-cap cascade is now conditioned on `cfg.Role == "llm"` (where tier-1 genuinely has a far larger window), with the seed row cited in the comment.
- **Verification:** `TestDispatcher_OverCap_EmbedNeverCascades` — embed over-cap WITH a tier-1 configured still returns 400 and the tier-1 backend records zero hits.
- **Committed in:** `c2dbec0`

---

**Total deviations:** 2 (1 coordinator policy directive, 1 Rule 1 auto-fix)
**Impact on plan:** no scope creep. The policy change narrows a security gate on one error class by explicit owner decision; the Rule 1 fix makes the code match the plan's own stated intent.

## Issues Encountered

- **Integration suite cannot run in this environment.** `go test -tags integration ./gateway/internal/...` panics in `integration_test` because testcontainers cannot reach the Docker socket — the executing user is not in the `docker` group (`id -nG` has no `docker`), so this is not a sandbox artifact. Mitigation applied: `go vet -tags integration ./gateway/...` is clean (proving the signature change breaks no integration-tagged file) and every other integration-tagged package compiles and loads. CI is the real gate — memory `gateway-integration-tests-not-in-executor-check`.
- **Static check that the integration chat tests are unaffected:** `tool_call_partial_test.go` and `goroutine_leak_test.go` both drive `NewChatProxy` with 200 responses, so the new interceptor returns nil at gate (b).

## Verification Performed

| Gate | Result |
|------|--------|
| `gofmt -l gateway` | 0 files |
| `go build ./...` | clean |
| `go vet ./gateway/...` (with and without `-tags integration`) | clean |
| `go test ./gateway/... -count=1` | all packages ok |
| `go test -tags integration ./gateway/internal/...` | blocked — no Docker access (see Issues) |
| diff restricted to `gateway/internal/{proxy,obs}/` | 0 files outside |
| STT regression: `audio.go` / `gemini_stt_director.go` untouched | confirmed by `git diff --name-only` |
| `.Enforce(` / `tokenCacheKey(` call-site sweep | no remaining old-signature callers; `cmd/gateway/main.go` absent as predicted |

## Known Stubs

None.

## Threat Flags

None — no new network endpoint, auth path, file access pattern or schema change. The one trust-boundary change (sensitive payloads may now reach the external tier-1 on over-context) is an explicit owner decision, documented above and instrumented by `gateway_over_context_cascaded_total{tenant,upstream}`.

## Next Phase Readiness

Ready for deploy by the orchestrator (this plan did NOT deploy). Post-deploy validation from the plan's `<verification>` §5:

1. ~20k-token request, tenant `chat-ifix`, pod serving → expect 200 served by `openrouter-chat`.
2. Same payload, tenant `telefonia` → NOW ALSO expects 200 via tier-1 (policy change), with `gateway_over_context_cascaded_total{tenant="telefonia"}` incremented — no longer the 400/503 the plan originally specified.
3. `docker logs` should no longer show `tokencount /tokenize request failed` pointing at `172.18.0.1:18000` while the pod serves.
4. Watch `gateway_over_context_cascaded_total` — sustained growth means external spend and argues for Fix D (bigger pod window).

Out of scope and still open from the HANDOFF: **Fix C** (alert on a guard that has been fail-open for N minutes — now cheap to build, the counter exists), **Fix E** (pod provisioned outside the schedule window).

## Self-Check: PASSED

- All 6 claimed files present on disk (2 created, 8 modified — verified via `ls` + `git diff --name-only`).
- All 6 claimed commit hashes present in `git log` (`bc20cd5`, `f5be5de`, `11b53d8`, `2a91867`, `a014e5a`, `c2dbec0`).
- `gofmt -l gateway` = 0, `go vet ./gateway/...` clean, `go test ./gateway/... -count=1` all ok.

---
*Quick task: 260824-ucv*
*Completed: 2026-08-24*
