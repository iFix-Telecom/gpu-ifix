---
phase: 16-close-gap-ten-04-wire-stt-embed-usage-metering
verified: 2026-06-28T00:00:00Z
status: human_needed
score: 8/8 must-haves verified
overrides_applied: 0
human_verification:
  - test: "Send a real STT request (audio file) through the live gateway and confirm billing_events row carries non-zero audio_seconds"
    expected: "SELECT audio_seconds FROM billing_events WHERE tenant_id='<id>' ORDER BY created_at DESC LIMIT 1 returns > 0"
    why_human: "End-to-end path requires a running gateway instance with a live STT upstream pod; the DB write path (flusher → billing_events → usage_counters) cannot be verified without Postgres and an actual upstream response"
  - test: "Send a real embed request through the live gateway and confirm billing_events row carries non-zero embeds_count"
    expected: "SELECT embeds_count FROM billing_events WHERE tenant_id='<id>' ORDER BY created_at DESC LIMIT 1 returns > 0"
    why_human: "Same dependency on a running gateway + Postgres + embed upstream"
  - test: "Verify /admin/usage and /consumo audio/embed columns are non-zero after the above requests"
    expected: "Dashboard /consumo shows audio minutes > 0 and embed count > 0 for the tested tenant (OBS-09 resolution)"
    why_human: "UI read-through requires browser + live stack"
  - test: "Confirm a tenant with a tight audio quota is blocked on the next STT request once usage_counters exceeds the limit"
    expected: "Gateway returns HTTP 429 with quota_exceeded_daily_audio_minutes error code"
    why_human: "Requires setting a quota limit in DB, sending enough audio requests to trip it, and observing the block response (TEN-04 enforcement)"
---

# Phase 16: Close TEN-04 (STT/Embed Usage Metering) Verification Report

**Phase Goal:** Wire usage-metering producers for the STT + embed proxies so `AudioSecondsMs10`/`EmbedsCount` get written (declared+read+quota-gated but never produced). Result: audio/embed billing_events populate, usage_counters populate, per-tenant audio/embed quotas actually enforce (TEN-04), and /consumo/OBS-09 stop showing 0.
**Verified:** 2026-06-28
**Status:** human_needed (all code-level must-haves VERIFIED; live E2E is deferred-live-UAT per project standard pattern)
**Re-verification:** No — initial verification

---

## Step 0: Previous Verification

None found. Initial mode.

---

## Goal Achievement

### Observable Truths

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | `AudioSecondsMs10` written non-zero for verbose_json STT (response `duration` present) | VERIFIED | `applyAudioEmbedUsage` line 377: `usage.AudioSecondsMs10.Store(int64(seconds * 10))` where seconds = response `duration`; `TestApplyAudioEmbedUsageDurationPresent` passes (90s → 900) |
| 2 | `AudioSecondsMs10` written non-zero for default-format STT (no `duration` field) — ELSE branch | VERIFIED | ELSE branch reads `auditctx.RequestAudioSecondsFrom(reqCtx)`; `TestApplyAudioEmbedUsageNoDurationDerivesFromRequest` passes (30s ctx → AudioSecondsMs10==300) |
| 3 | `EmbedsCount` written non-zero = `len(data[])` | VERIFIED | `applyAudioEmbedUsage` line 387: `usage.EmbedsCount.Store(int64(len(partial.Data)))`; `TestApplyAudioEmbedUsageEmbedBatch` passes (3 elements → 3) |
| 4 | 90-second audio → `DailyAudioMinutes=1` via unit chain (×10 store → /10 flush → /60 minutes) | VERIFIED | `TestApplyAudioEmbedUsageDurationPresent` explicitly asserts: `AudioSecondsMs10==900`, flush restores 90.0s, `int(90/60)==1` |
| 5 | Audio-only or embed-only request (zero tokens) still enqueues a billing.Event (all-zero guard fires ONLY on all-zeros, not on audio/embed) | VERIFIED | Guard at interceptor_usage.go:210 checks `tokensIn == 0 && tokensOut == 0 && audioSeconds == 0 && embedsCount == 0`; once audio/embed are non-zero the guard no longer drops the event |
| 6 | Chat/token path unchanged (no regression) | VERIFIED | `ExtractFromBody` at line 152 is untouched; flush reads at lines 207-208 unchanged; `TestApplyAudioEmbedUsageChatNoOp` passes (chat route → AudioSecondsMs10==0, EmbedsCount==0); full proxy test suite green |
| 7 | `applyAudioEmbedUsage` is a pure helper callable Postgres-free | VERIFIED | All 5 unit tests call it directly with `&billing.RequestUsage{}`, no flusher/slot/DB; tests pass |
| 8 | `AudioSecondsMs10.Store` / `EmbedsCount.Store` grep-proof: present (were 0 tree-wide) | VERIFIED | `grep -rnE "AudioSecondsMs10\.(Store|Add)|EmbedsCount\.(Store|Add)" gateway/ --include='*.go' | grep -v _test.go` returns 2 lines, both inside `applyAudioEmbedUsage` |

**Score: 8/8 truths verified**

---

### Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `gateway/internal/proxy/audio_duration.go` | `DeriveAudioSeconds` — WAV RIFF parse + compressed bitrate fallback | VERIFIED | `func DeriveAudioSeconds(audio []byte, mime string) float64` at line 46; WAV/compressed/empty tests all pass |
| `gateway/internal/proxy/audio_duration_test.go` | Tests: WAV known-duration, compressed non-zero, empty→0 | VERIFIED | 3 test functions present and passing |
| `gateway/internal/proxy/stt_request_audio_middleware.go` | `RequestAudioSecondsMiddleware` — stamps ctx, restores r.Body + r.GetBody, bounded to 25 MiB | VERIFIED | `LimitReader` at line 93, `r.GetBody` set at line 129; `TestRequestAudioSecondsMiddlewareStampsAndReplays` verifies ctx stamp non-zero AND GetBody byte-identical |
| `gateway/internal/proxy/stt_request_audio_middleware_test.go` | Middleware tests | VERIFIED | 3 test functions; replay byte-identical assertion present |
| `gateway/internal/auditctx/override.go` | `WithRequestAudioSeconds` / `RequestAudioSecondsFrom` | VERIFIED | Both functions at lines 90 and 96 |
| `gateway/internal/proxy/interceptor_usage.go` | `applyAudioEmbedUsage` pure helper; `usageJSONBuffer.Close` calls it; ExtractFromBody unchanged | VERIFIED | Helper at line 356; Close calls at line 608; ExtractFromBody at line 152 unchanged |
| `gateway/internal/proxy/interceptor_usage_test.go` | 5 unit tests calling helper directly (Postgres-free) | VERIFIED | All 5 `TestApplyAudioEmbedUsage*` functions present and passing |
| `gateway/cmd/gateway/main.go` | 7 STT/embed proxy sites wired with usageInterceptor + middleware mounted | VERIFIED | Lines 576, 587, 671, 689, 698, 716, 735 all pass usageInterceptor; middleware mounted at line 1211 |
| `gateway/internal/quota/counters_test.go` | 3 quota-trip tests (daily audio, daily embeds, monthly audio+embeds) | VERIFIED | `TestCheckQuotaTodayAudioMinutesTrips`, `TestCheckQuotaTodayEmbedsTrips`, `TestCheckQuotaMonthAudioAndEmbedsTrip` all pass |

---

### Key Link Verification

| From | To | Via | Status | Details |
|------|-----|-----|--------|---------|
| `usageJSONBuffer.Close` (interceptor_usage.go:608) | `applyAudioEmbedUsage → AudioSecondsMs10.Store / EmbedsCount.Store` | `routeToBillingRoute(b.reqPath)` dispatch; response duration ELSE `RequestAudioSecondsFrom(b.reqCtx)` | WIRED | Call site confirmed at line 608; Store at lines 377/387 |
| `stt_request_audio_middleware.go` | `applyAudioEmbedUsage` ELSE branch | `auditctx.WithRequestAudioSeconds` stamped on ctx pre-proxy; read inside helper via `RequestAudioSecondsFrom(reqCtx)` | WIRED | Middleware confirmed at main.go:1211 before `wrapWithTimeout`; helper reads it at line ~370 |
| `main.go:576` NewEmbeddingsProxy | `proxy.UsageInterceptor.Intercept` | variadic `...ProxyResponseInterceptor` | WIRED | `proxy.NewEmbeddingsProxy(cfg.UpstreamEmbedURL, log, usageInterceptor)` |
| `main.go:587` NewAudioProxy | `proxy.UsageInterceptor.Intercept` | variadic | WIRED | `proxy.NewAudioProxy(cfg.UpstreamSTTURL, log, resolver, usageInterceptor)` |
| `main.go:689` NewDynamicOverrideSTTProxy | `proxy.UsageInterceptor.Intercept` | variadic | WIRED | `resolver, log, usageInterceptor)` confirmed |
| `main.go:1594` buildOpenAIEmbedProxy (7th site) | `proxy.UsageInterceptor.Intercept` | `ComposeInterceptors(usageInterceptor)` as ModifyResponse | WIRED | Line 1618 confirmed |
| `main.go:1675` buildGroqWhisperProxy | `proxy.UsageInterceptor.Intercept` | `ComposeInterceptors(usageInterceptor)` | WIRED | Line 1696 confirmed |
| `main.go:1704` buildOpenAIWhisperProxy | `proxy.UsageInterceptor.Intercept` | `ComposeInterceptors(usageInterceptor)` | WIRED | Line 1729 confirmed |
| `main.go:1632` buildGeminiSTTProxy | `proxy.UsageInterceptor.Intercept` | Explicit flatten-then-usage closure (W-1): `modifyResponse(resp)` FIRST, then `usageInterceptor.Intercept(resp)` | WIRED | Lines 1653-1656 confirmed; ordering correct |

---

### Data-Flow Trace (Level 4)

| Artifact | Data Variable | Source | Produces Real Data | Status |
|----------|--------------|--------|--------------------|--------|
| `interceptor_usage.go applyAudioEmbedUsage` | `seconds` (STT) | `partial.Duration` OR `auditctx.RequestAudioSecondsFrom(reqCtx)` | Yes — response duration field or request-derived from audio bytes | FLOWING |
| `interceptor_usage.go applyAudioEmbedUsage` | `partial.Data` (embed) | JSON response body `data[]` array | Yes — actual upstream response | FLOWING |
| `usageJSONBuffer.Close` → `FinalizeRequest` | `audioSeconds`, `embedsCount` | `AudioSecondsMs10.Load() / 10.0`, `EmbedsCount.Load()` | Yes — from Stores above | FLOWING |
| `counters.go CheckQuotaToday` | `row.AudioSeconds`, `row.EmbedsCount` | DB query via `countersQueries` interface | Yes (at runtime, from `usage_counters` UPSERTED from billing_events) | FLOWING (code-level verified; live DB requires human UAT) |

---

### Behavioral Spot-Checks

| Behavior | Command | Result | Status |
|----------|---------|--------|--------|
| `go build ./...` (full gateway build) | `cd /home/pedro/projetos/pedro/gpu-ifix && go build ./gateway/...` | exit 0 | PASS |
| `go test ./gateway/internal/proxy/...` | Full proxy test suite | `ok ... 13.693s` | PASS |
| `go test ./gateway/internal/quota/...` | Quota test suite | `ok ... 0.027s` | PASS |
| `go test ./gateway/internal/billing/...` | Billing test suite | `ok ... 0.009s` | PASS |
| `TestApplyAudioEmbedUsage*` (5 tests, verbose) | `-run 'ApplyAudioEmbedUsage' -v` | All 5 PASS | PASS |
| Quota-trip tests (audio + embed, verbose) | `-run 'Quota.*(Audio|Embeds)' -v` | All 5 PASS (3 new + 2 pre-existing) | PASS |
| `gofmt -l` on all changed files | All 7 modified/created files | Output empty (no formatting issues) | PASS |
| Grep-proof: Store sites (was 0 before phase) | `grep -rnE "AudioSecondsMs10\.(Store|Add)|EmbedsCount\.(Store|Add)" gateway/ --include='*.go' | grep -v _test.go` | 2 lines (interceptor_usage.go:377, :387) | PASS |

---

### Probe Execution

Step 7c: SKIPPED — no `scripts/*/tests/probe-*.sh` files declared for this phase. Phase is a metering-wiring phase with Go unit tests as the verification mechanism.

---

### Requirements Coverage

| Requirement | Source Plan | Description | Status | Evidence |
|------------|------------|-------------|--------|----------|
| TEN-04 | 16-01-PLAN, 16-02-PLAN | Per-tenant audio/embed daily+monthly quota enforcement | SATISFIED (code-level) | Quota-trip tests prove `CheckQuotaToday`/`CheckQuotaMonth` gate on audio_seconds/embeds_count; producer now writes non-zero values; live trip requires human UAT |
| OBS-09 | 16-01-PLAN, 16-02-PLAN | Audio/embed usage visibility in /consumo + economy panel (was 0) | SATISFIED (code-level) | Producer Stores are now live; dashboard already reads `usage_counters` columns; they will be non-zero once billing_events populate — requires live E2E to observe the UI |

---

### Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
|------|------|---------|----------|--------|
| None | — | — | — | — |

No `TBD`, `FIXME`, or `XXX` markers in any of the 7 changed files. No placeholder returns, empty handlers, or hardcoded-empty state found in production code paths.

---

### Human Verification Required

#### 1. Live STT billing_events row — audio_seconds non-zero

**Test:** Send a real `/v1/audio/transcriptions` request (any audio format) through the production or staging gateway with a valid tenant API key. Query `SELECT audio_seconds FROM billing_events WHERE tenant_id='<id>' ORDER BY created_at DESC LIMIT 1`.
**Expected:** `audio_seconds > 0`
**Why human:** Requires a running gateway with a live STT upstream pod and Postgres; the flusher write path (`FinalizeRequest` → `billing_events` UPSERT) is not exercised by unit tests (uses a concrete `*pgxpool.Pool`).

#### 2. Live embed billing_events row — embeds_count non-zero

**Test:** Send a real `/v1/embeddings` request with a multi-element input array. Query `SELECT embeds_count FROM billing_events WHERE tenant_id='<id>' ORDER BY created_at DESC LIMIT 1`.
**Expected:** `embeds_count > 0`, equal to the number of input strings sent
**Why human:** Same dependency — live gateway + Postgres + embed upstream.

#### 3. Dashboard /consumo audio/embed columns show non-zero (OBS-09)

**Test:** After items 1 and 2, open `ai-dashboard.converse-ai.app` /consumo for the tested tenant.
**Expected:** Audio minutes and embed count columns are non-zero.
**Why human:** Browser + live stack required; UI reads `usage_counters` which is populated by the billing_events flush.

#### 4. TEN-04 quota enforcement — audio request blocked after limit exceeded

**Test:** Set a tight `daily_audio_minutes` limit (e.g., 1 minute) for a test tenant in `quota_limits`. Send 2+ minutes of audio. Observe the gateway response.
**Expected:** HTTP 429 with `error.code == "quota_exceeded_daily_audio_minutes"`
**Why human:** Requires DB state manipulation + live request traffic. Code-level proof exists (quota-trip tests), but the E2E gate (quota layer consulted per-request against live DB) requires an integration test with a real DB.

---

### Gaps Summary

No code-level gaps. All 8 must-have truths are VERIFIED against the actual codebase on the `develop` branch. The 4 human verification items are live-UAT confirmation that the wired producer actually writes through to the DB (the standard deferred-live-UAT pattern for this project — the same applied to Phase 13 and earlier phases).

The `develop` branch contains 5 commits for Phase 16:
- `3b8292d` — feat(16-01): request-audio duration helper + STT ctx-stamping middleware
- `a1693b2` — feat(16-01): applyAudioEmbedUsage producer wired into usageJSONBuffer.Close
- `04233a7` — test(16-01): applyAudioEmbedUsage unit tests via Postgres-free seam
- `8faee4a` — feat(16-02): wire usageInterceptor into 7 STT/embed proxies + mount request-audio middleware
- `76642e7` — test(16-02): quota-trip tests prove audio + embed dimensions now gate

All unit tests pass. Build is clean. Formatting is clean. The producer gap (AudioSecondsMs10.Store / EmbedsCount.Store — was 0 matches tree-wide, now 2 production sites) is closed.

---

_Verified: 2026-06-28_
_Verifier: Claude (gsd-verifier)_
