---
phase: 12-gateway-resilience-remediation
plan: 03
subsystem: gateway-resilience
tags: [resilience, failover, dispatcher, reverse-proxy, RES-13, D-06, D-07, D-08, D-09, D-10]
requires:
  - "obs.DialFallthroughTotal{role,outcome} counter (Plan 12-01)"
  - "Loader.ResolveAllTier1 tier_priority ASC cascade (Phase 11.2)"
  - "breaker.Set.EffectiveState + Execute (Phase 06.9)"
provides:
  - "fallthroughRoundTripper + isConnectionClass — pre-byte dial-failure classifier (transport.go)"
  - "errDialFailedFallthrough sentinel + sentinel-aware ErrorHandler suppression (errors.go)"
  - "dispatchResult control-flow channel (context-carried) + dispatchTo returns it"
  - "tier-0 dial-failure → tier-1 cascade re-dispatch with safe body replay (D-08)"
  - "DialFallthroughTotal increments wired {tier1_served|chain_exhausted|sensitive_blocked}"
affects:
  - "Plan 12-04/05 (live chaos zero-502 gate D-18 reads this request-path behavior)"
tech-stack:
  added: []
  patterns:
    - "RoundTrip-level sentinel + ErrorHandler write-suppression (Pitfall 2 — closes the pre-byte 502 window)"
    - "request-scoped dispatchResult struct via context.WithValue (explicit control flow, no implicit side effects)"
    - "GetBody-based body replay across cascade hops with bounded buffering + over-cap exemption"
key-files:
  created:
    - gateway/internal/proxy/transport.go
    - gateway/internal/proxy/transport_test.go
    - gateway/internal/proxy/fallthrough_test.go
  modified:
    - gateway/internal/proxy/errors.go
    - gateway/internal/proxy/dispatcher.go
    - gateway/internal/proxy/chat.go
    - gateway/internal/proxy/audio.go
    - gateway/internal/proxy/embeddings.go
    - gateway/internal/proxy/tts.go
    - gateway/internal/proxy/dynamic_override.go
decisions:
  - "New fallthrough_test.go instead of editing dispatcher_test.go — the new tests require REAL ReverseProxies wired with fallthroughRoundTripper + sentinel ErrorHandler (newPassthroughProxy in dispatcher_test.go cannot trigger the dial-fallthrough path); a dedicated file keeps the new fixture isolated from the existing EffectiveState-trip fixtures"
  - "ErrorHandler suppresses the sentinel write ONLY when a request-scoped dispatchResult is present; standalone proxy usage (no dispatcher → no dispatchResult) still writes the normal 502 (prevents a bare-200 regression caught by TestChatProxy_UpstreamUnreachable502Envelope / TestTTSProxy_ErrorEnvelope)"
  - "maxSTTBodyBuffer = 25 MiB = config.MaxBodyBytes (the global http.MaxBytesHandler ceiling at cmd/gateway/main.go:1189), NOT the RESEARCH ~32 MB estimate; comment cites the authoritative source line/value"
  - "DialFallthroughTotal accounting owned by the tier-0 dial-fallthrough caller (cascadeTier1 is metric-free + returns served bool) so the plain tier-0-OPEN cascade — which is NOT a dial fallthrough — never emits the counter"
  - "D-09 breaker-failure recording driven through breaker.Set.Execute with a synthetic 502 HTTPError (IsSuccessful classifies 5xx as failure); breaker.Set exposes no direct record-failure API and forking one was avoided"
  - "Wrapped fallthroughRoundTripper into ALL proxy constructors (chat/audio/embed/tts/dynamic-override), not only chat+audio, so RES-13 fallthrough is consistent across every role (Rule 2 — missing critical functionality for the embed/tts roles)"
metrics:
  duration: "~75min"
  completed: "2026-06-13"
  tasks: 2
  files: 10
---

# Phase 12 Plan 03: Dial-Failure Tier-1 Fallthrough Summary

RES-13 closes the 100×502 window from the 11-07 chaos: a connection-class tier-0 dial failure with the breaker still CLOSED no longer writes a 502 — it is intercepted at `RoundTrip`, suppressed in a sentinel-aware `ErrorHandler` (zero bytes written), carried back via a request-scoped `dispatchResult`, and re-dispatched through the existing `tier_priority` ASC cascade with the body replayed identically each hop. Sensitive tenants still 503-block (D-10 HARD GATE); response-timeouts/5xx are untouched (D-06); the tier-0 breaker records a failure so it opens naturally (D-09); over-cap STT bodies are exempt from buffering and fallthrough (T-12-10).

## What Shipped

### Task 1 — fallthroughRoundTripper + isConnectionClass (D-06, NEW file, TDD)
- `transport.go` (NEW): `fallthroughRoundTripper{base http.RoundTripper}` wraps a base transport; on a pre-byte connection-class dial error it returns `(nil, errDialFailedFallthrough)`; every other outcome (success, post-dial timeout, 5xx, non-dial error) passes through unchanged.
- `isConnectionClass(err)` classifies STRICTLY on the dial phase, extending (not forking) `breaker.IsSuccessful`'s network reasoning: `*net.DNSError` → true, `syscall.ECONNREFUSED` → true, `*net.OpError` with `Op=="dial"` → true; a post-connection `Op=="read"/"write"` OpError or a bare timeout → **false** (the D-06 / Pitfall 2 pre-byte-only guarantee).
- `errors.go`: added the unexported `errDialFailedFallthrough` sentinel.

### Task 2 — sentinel ErrorHandler + dispatchResult + cascade re-dispatch + body replay (RES-13/D-07/D-08/D-09/D-10, TDD)
- `errors.go` `ErrorHandler`: detects `errDialFailedFallthrough`. When a request-scoped `*dispatchResult` is present (dispatcher-driven), it suppresses ALL writes and records `fallthrough_=true, wrote=false`. With no dispatchResult (standalone proxy), it falls through to the normal 502 write. Every non-sentinel error keeps the existing 502 envelope + records `wrote=true`.
- `dispatcher.go`:
  - `dispatchResult{fallthrough_,wrote,err}` carried via `context.WithValue`; `withDispatchResult`/`dispatchResultFrom` helpers.
  - `dispatchTo` installs a fresh `*dispatchResult` before `ServeHTTP` and **returns** it (was void). A "proxy not registered" miss records `wrote=true` (terminal).
  - Tier-0-CLOSED branch: `prepareReplayBody` → `dispatchTo`; on `fallthrough_ && !wrote` → `recordDialFailure(t0)` (D-09) → sensitive→`writeSensitiveBlock` (D-10) | over-cap→normal 502 | normal→`cascadeTier1` with `DialFallthroughTotal{tier1_served|chain_exhausted}` accounting.
  - `cascadeTier1` (NEW): iterates `ResolveAllTier1` CLOSED candidates, restores the body before each hop (D-08), advances on a tier-1 dial fallthrough (recording each candidate's breaker failure), returns `served bool`; writes the existing 503 exhaustion envelope exactly once. Reused by the plain tier-0-OPEN normal-tenant path (no metric there — not a dial fallthrough).
  - Sensitive non-stream retry path now also blocks on a post-retry pre-byte dial failure (`sensitive_blocked` metric).
  - `prepareReplayBody`: uses `r.GetBody` when set; buffers ONCE (bounded by `maxSTTBodyBuffer`) and sets `GetBody` when nil; declared/streamed over-cap → `replayable=false` (skip fallthrough). `maxSTTBodyBuffer = 25 MiB` = `config.MaxBodyBytes` (the authoritative `http.MaxBytesHandler` ceiling).
  - `recordDialFailure` drives one failure through `breaker.Set.Execute` (synthetic 5xx HTTPError) to open the breaker naturally (D-09).
- Wiring: `fallthroughRoundTripper` wraps the Transport in `chat.go`, `audio.go`, `embeddings.go`, `tts.go`, and `dynamic_override.go` — all roles now fall through consistently.

## Tests

All 16 plan tests PASS (`go test ./internal/proxy/ -count=1` green; `go build ./...` exit 0; full `./internal/...` suite green):
- transport_test.go: `TestIsConnectionClass_DialRefused|DNSError|ResponseTimeout|Nil`, `TestFallthroughRoundTripper_SignalsOnDial`
- fallthrough_test.go: `TestErrorHandler_SuppressesSentinelNoWrite|PreservesNonSentinel502`, `TestDispatcher_NoWriteBeforeTier1Dispatch|DialFailureFallsThrough|CascadeOnDialFailure|CascadeExhausted_502|SensitiveNeverFallsThrough|StreamingFallsThroughPreByte|ResponseTimeoutDoesNotFallThrough|BodyReplayedAcrossCascade|GetBodyNilBuffered|STTOverCapSkipsFallthrough`

## Deviations from Plan

### [Rule 2 - Missing critical functionality] Wrapped fallthroughRoundTripper into embed/tts/dynamic-override proxies
- **Found during:** Task 2 wiring.
- **Issue:** The plan's action named only chat.go + audio.go, but embeddings.go, tts.go, and dynamic_override.go also build per-upstream ReverseProxies with the now-sentinel-aware ErrorHandler. Left unwrapped, a dial failure on those roles would never produce the sentinel → no fallthrough for embed/tts and the dynamic primary-pod override path.
- **Fix:** Wrapped all five constructors' Transports with `fallthroughRoundTripper`. Behavior for the unwrapped case would have been a safe 502 (no regression), but RES-13 fallthrough across every role is the correct mitigation.
- **Files modified:** embeddings.go, tts.go, dynamic_override.go.
- **Commit:** 58bba73.

### [Plan-shape] New fallthrough_test.go instead of editing dispatcher_test.go
- The plan listed `dispatcher_test.go` in files_modified. The new behaviors require REAL ReverseProxies wired with the fallthroughRoundTripper + sentinel ErrorHandler; the existing `newPassthroughProxy` helper (which 502s on dial failure) cannot exercise the dial-fallthrough path. A dedicated `fallthrough_test.go` with its own fixture (`newFallthroughFixture`, closed-port proxies, `recordingResponseWriter`) keeps the new wiring isolated. `dispatcher_test.go` is unchanged (all its pre-existing tests still pass).

### [Design correctness — caught by existing tests] ErrorHandler suppresses only when dispatchResult present
- The first ErrorHandler implementation suppressed the sentinel write unconditionally, which broke `TestChatProxy_UpstreamUnreachable502Envelope` + `TestTTSProxy_ErrorEnvelope` (standalone proxies left a bare 200). Fixed so suppression happens ONLY when a request-scoped `dispatchResult` is in context; otherwise the normal 502 is written. This is the correct contract (nobody re-dispatches a standalone proxy).

## TDD Gate Compliance

Both behavior-adding tasks followed RED → GREEN:
- Task 1: `test(12-03)` ad93428 (RED — compile-fail) → `feat(12-03)` 8b3ee40 (GREEN)
- Task 2: `test(12-03)` a0bd3a1 (RED — compile-fail) → `feat(12-03)` 58bba73 (GREEN)

## Known Stubs

None. The `obs.DialFallthroughTotal` counter defined-but-not-incremented in Plan 12-01 is now fully wired here (all three outcomes), resolving that single-owner deferral.

## Threat Flags

None. No new network endpoints, auth paths, or schema. `go.mod` unchanged (zero package installs — T-12-SC). The body-buffering DoS surface (T-12-10) is mitigated by the `maxSTTBodyBuffer` cap + over-cap fallthrough exemption (TestDispatcher_STTOverCapSkipsFallthrough). The sensitive-data-leak surface (T-12-08) is gated by TestDispatcher_SensitiveNeverFallsThrough.

## Commits

- ad93428 `test(12-03): add failing tests for fallthroughRoundTripper + isConnectionClass`
- 8b3ee40 `feat(12-03): fallthroughRoundTripper + isConnectionClass classifier (D-06)`
- a0bd3a1 `test(12-03): add failing tests for dial-fallthrough cascade + dispatchResult`
- 58bba73 `feat(12-03): dial-fallthrough cascade + dispatchResult control flow (RES-13)`

## Self-Check: PASSED

- All created/modified files present on disk (10/10 FOUND).
- All 4 commit hashes present in git history.
