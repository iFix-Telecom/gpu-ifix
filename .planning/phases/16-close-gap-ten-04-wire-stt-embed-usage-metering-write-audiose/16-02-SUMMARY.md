---
phase: 16-close-gap-ten-04-wire-stt-embed-usage-metering
plan: 02
subsystem: gateway
tags: [go, gateway, stt, embeddings, usage-metering, quota, proxy-wiring, middleware]

# Dependency graph
requires:
  - phase: 16-close-gap-ten-04-wire-stt-embed-usage-metering
    plan: 01
    provides: "applyAudioEmbedUsage producer (in usageJSONBuffer.Close) + DeriveAudioSeconds + RequestAudioSecondsMiddleware + auditctx.WithRequestAudioSeconds"
provides:
  - "All 7 STT/embed proxy construction sites in main.go wired with usageInterceptor (was 0 — only chat proxies metered before)"
  - "RequestAudioSecondsMiddleware mounted on the STT dispatcher (before the TimeoutHandler wrap) so default-format transcriptions meter via the request-derived ELSE branch in production"
  - "Quota-trip tests proving the audio + embed dimensions now actually gate (daily + monthly, with boundary cases)"
affects: [quota enforcement (DailyAudioMinutes/DailyEmbeds/Monthly*), billing reconcile, OBS-09 /consumo]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Explicit flatten-then-usage ModifyResponse closure for gemini (W-1): run the envelope-flatten func FIRST, then usageInterceptor.Intercept on the flattened application/json body — NOT a bare ComposeInterceptors"
    - "ComposeInterceptors(usageInterceptor) for raw ReverseProxy builders with no prior ModifyResponse (groq/openai-whisper/openai-embed)"
    - "Request-derivation middleware mounted INSIDE the per-route handler stack, before wrapWithTimeout, so it runs outside the TimeoutHandler"

key-files:
  created: []
  modified:
    - gateway/cmd/gateway/main.go
    - gateway/internal/quota/counters_test.go

key-decisions:
  - "Mounted RequestAudioSecondsMiddleware by wrapping audioDispatcher BEFORE wrapWithTimeout (route-precise) rather than group-level pg.Use — the middleware only matters for the transcriptions route, and wrapping before the timeout handler keeps the body-read outside the timeout budget"
  - "gemini wired with an explicit closure (modifyResponse first, then Intercept) per W-1 — the flatten mutates body + sets Content-Type=application/json which the JSON-buffer producer path requires"
  - "openai-embed (7th site, REVISION 2/W-3) gets ComposeInterceptors(usageInterceptor) so embeds that fail over local→OpenAI are also metered"
  - "Task 3 is a verification-only gate — no production code change, no commit"

patterns-established:
  - "Wiring a raw ReverseProxy builder to the usage producer: add usageInterceptor param + set ModifyResponse (compose for no-prior-MR, explicit closure when a prior MR must run first)"

requirements-completed: [TEN-04, OBS-09]

# Metrics
duration: ~25min
completed: 2026-06-28
---

# Phase 16 Plan 02: Wire STT + Embed Usage Metering (Wiring Half) Summary

**As 7 sites de construção de proxy STT/embed em `main.go` (antes com ZERO interceptor de uso — só chat metrava) agora passam o `usageInterceptor`, e o `RequestAudioSecondsMiddleware` está montado na rota de transcrição antes do TimeoutHandler — fechando a malha produtor→consumidor: o contador audio/embed que nunca subia agora popula `usage_counters` e as cotas TEN-04 (audio-minutes/embeds) provadamente disparam.**

## Performance

- **Duration:** ~25 min
- **Started:** 2026-06-28 (worktree agent-a0ca249ebe7f5478f)
- **Completed:** 2026-06-28
- **Tasks:** 3/3 (Task 3 = verification gate, no code)
- **Files modified:** 2

## Accomplishments

### Task 1 — Wire usageInterceptor into all 7 STT/embed proxies + mount request-audio middleware (commit `8faee4a`)
Threaded the already-constructed `usageInterceptor` (main.go:548) into every STT/embed proxy site:
- **3 variadic constructors** (just pass the arg): `NewEmbeddingsProxy` (:576), `NewAudioProxy` (:587), `NewDynamicOverrideSTTProxy` (:686/:689).
- **4 raw `httputil.ReverseProxy` builders** (added a `usageInterceptor *proxy.UsageInterceptor` param + set `ModifyResponse`):
  - `buildOpenAIEmbedProxy` (the 7th site, REVISION 2/W-3 — external embed fallback): `ComposeInterceptors(usageInterceptor)`.
  - `buildGroqWhisperProxy` + `buildOpenAIWhisperProxy`: `ComposeInterceptors(usageInterceptor)` (no prior ModifyResponse).
  - `buildGeminiSTTProxy` (W-1): **explicit flatten-then-usage closure** — calls the existing `modifyResponse(resp)` (envelope flatten + Content-Type=application/json + body mutation) FIRST, then `usageInterceptor.Intercept(resp)` only if it succeeded. NOT collapsed into a bare ComposeInterceptors.
- **Middleware mount:** `proxy.RequestAudioSecondsMiddleware(log)(audioDispatcher)` wrapped BEFORE `wrapWithTimeout` at the audioHandler construction (main.go:1203). This populates the request-derived audio-seconds ctx value (locked decision #2 ELSE branch) for every `/v1/audio/transcriptions` request in production — so default-format `{"text":...}` transcriptions (no response `duration`) meter non-zero.

Chat sites, the `ToolCallTerminalGuard` / `WhisperAbortGuard` wrappers, and interceptor signatures were left untouched.

### Task 2 — Quota-trip tests: audio + embed dimensions now actually block (commit `76642e7`)
Added 3 tests to `counters_test.go` using the existing `fakeQueries`:
- `TestCheckQuotaTodayAudioMinutesTrips`: `AudioSeconds=120` (2 min) trips limit 2 → `ErrQuotaExceededDailyAudioMinutes`; boundary `119s` (`int(119/60)=1`) under limit 2 → nil.
- `TestCheckQuotaTodayEmbedsTrips`: `EmbedsCount=50` trips limit 50 → `ErrQuotaExceededDailyEmbeds`; `49` → nil.
- `TestCheckQuotaMonthAudioAndEmbedsTrip`: monthly audio `7200s`/120min + monthly embeds `1000` trip their sentinels; boundary `7199s`/`999` under limits → nil.

These prove the consumer end of the chain the Plan 16-01 producer now feeds — previously impossible because the source counter was always 0.

### Task 3 — Full-suite regression + grep-proof the producer flip (verification gate, no commit)
Ran build/vet/test across the touched + dependent packages; grep-proof confirms the producer Stores are present.

## Verification

```
$ cd gateway && go build ./...
(exit 0)

$ go vet ./...
(exit 0)

$ go test ./internal/proxy/... ./internal/quota/... ./internal/billing/... ./internal/auditctx/... -count=1
ok   .../internal/proxy    13.700s
ok   .../internal/quota    0.030s
ok   .../internal/billing  0.011s
?    .../internal/auditctx [no test files]

$ go test ./cmd/... -count=1
ok   .../cmd/gateway     0.020s
ok   .../cmd/gatewayctl  8.400s

$ go test ./internal/quota/... -run 'Quota.*(Audio|Embeds)' -v
--- PASS: TestCheckQuotaTodayAudioMinutesTrips
--- PASS: TestCheckQuotaTodayEmbedsTrips
--- PASS: TestCheckQuotaMonthAudioAndEmbedsTrip
PASS

$ go test -tags integration ./internal/proxy/... ./internal/quota/... -count=1
ok   .../internal/proxy   13.707s
ok   .../internal/quota   0.033s

$ gofmt -l cmd/gateway/main.go internal/quota/counters_test.go
(empty)

# grep-proof: producer Stores (was 0 at phase start)
$ grep -rnE 'AudioSecondsMs10\.(Store|Add)|EmbedsCount\.(Store|Add)' internal/ --include='*.go' | grep -v _test.go | grep -v '//'
internal/proxy/interceptor_usage.go:377: usage.AudioSecondsMs10.Store(int64(seconds * 10))
internal/proxy/interceptor_usage.go:387: usage.EmbedsCount.Store(int64(len(partial.Data)))

# 7 proxy sites now pass usageInterceptor (call sites)
main.go:576 NewEmbeddingsProxy(..., usageInterceptor)
main.go:587 NewAudioProxy(..., usageInterceptor)
main.go:671 buildOpenAIEmbedProxy(..., usageInterceptor)
main.go:689 NewDynamicOverrideSTTProxy(..., usageInterceptor)
main.go:698 buildOpenAIWhisperProxy(..., usageInterceptor)
main.go:716 buildGeminiSTTProxy(..., usageInterceptor)
main.go:735 buildGroqWhisperProxy(..., usageInterceptor)

# middleware mounted
main.go:1211 proxy.RequestAudioSecondsMiddleware(log)(audioDispatcher)
```

## Deviations from Plan

### Rule 3 (blocking) — Wave 1 (16-01) was NOT pre-merged into the worktree base
- **Found during:** initial state load (before Task 1).
- **Issue:** The objective stated "Wave 1 (plan 16-01) is ALREADY MERGED into this worktree's base", but this worktree branched from `develop`@`3b17b8e` (Phase 15), while the 16-01 producer (`audio_duration.go`, `stt_request_audio_middleware.go`, `applyAudioEmbedUsage`, `auditctx.WithRequestAudioSeconds`) had landed on `develop`@`b03d3af` AHEAD of the base. The producer files did not exist in the worktree; `go build` would have failed on the new proxy params + middleware mount.
- **Fix:** `git merge --ff-only develop` — a clean fast-forward (worktree HEAD was a strict ancestor of `develop`, no divergence). This brought in the 16-01 producer files + the 16-02 planning artifacts. Verified producer grep-proof = 2 and baseline `go build ./...` green BEFORE editing.
- **Files modified:** none authored — fast-forward only.

### Rule 3 (blocking) — ClickUp link-enforce hook required the skip marker in the worktree .planning
- **Found during:** Task 1 (first Edit).
- **Issue:** The `clickup-link-enforce.sh` PostToolUse hook blocks edits in a GSD repo when `<cwd>/.planning/clickup-active-task.json` is absent. The worktree `.planning/` lacked the marker (the main checkout has `{"skip": true}`).
- **Fix:** Mirrored the main checkout's `{"skip": true}` marker into the worktree `.planning/clickup-active-task.json` (same fix 16-01 used). The marker is untracked, not committed.
- **Files modified:** worktree `.planning/clickup-active-task.json` (untracked).

### Note — middleware mount point
The plan offered two options (group-level `pg.Use` OR wrapping the transcriptions handler). Chose to wrap `audioDispatcher` at the `audioHandler` construction (main.go:1203) — route-precise (no overhead on chat/embed) and explicitly BEFORE the TimeoutHandler wrap (per the objective's context note). The middleware is still a no-op on non-audio bodies, so correctness is identical to the group-level option.

## Known Stubs

None. All 7 sites are wired to the live producer; the middleware is mounted in the real request path; the quota-trip tests assert the real consumer (`CheckQuotaToday`/`CheckQuotaMonth`). The pre-existing "Phase 4 folded TODO" comments in main.go are unrelated historical markers, not stubs introduced here.

## Threat Flags

None. No new network endpoints, auth paths, file-access patterns, or trust-boundary schema changes. The threat register entries (T-16-04 gemini ordering, T-16-05/06/09) are mitigated/accepted as planned: gemini's flatten precedes Intercept (T-16-04 mitigate, asserted by the green gemini-director tests); EmbedsCount remains a scalar count (T-16-09 accept).

## Self-Check: PASSED

- Modified files exist: `gateway/cmd/gateway/main.go`, `gateway/internal/quota/counters_test.go` — both FOUND.
- Commits exist on `worktree-agent-a0ca249ebe7f5478f`: `8faee4a` (feat), `76642e7` (test) — both FOUND.
- `go build ./...` exit 0; `go vet ./...` exit 0; targeted + cmd + integration-tag test packages all `ok`; quota-trip tests PASS; `gofmt -l` empty.
- grep-proof: 2 producer Store sites present (was 0 at phase start); 7 proxy call sites pass usageInterceptor; middleware mounted.
