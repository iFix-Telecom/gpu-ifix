---
phase: 16-close-gap-ten-04-wire-stt-embed-usage-metering
source: v1-MILESTONE-AUDIT.md (re-audit 2026-06-28) + integration-checker report 2026-06-28
generated: 2026-06-28
mode: closure (no discuss-phase — fully diagnosed gap, single fix surface)
---

# Phase 16 CONTEXT — Close TEN-04 (STT/embed usage metering)

## Domain

Gateway billing/metering. The Go gateway (`gateway/`) records per-request usage into
`billing.RequestUsage` atomics, flushes them to `billing_events`, which UPSERTs
`usage_counters`, which the quota layer reads to enforce per-tenant limits and the
dashboard reads for `/consumo` + the OBS-09 economy panel. The **chat/token** path is
fully wired. The **audio (STT) + embed** path is not — the counters are declared, read,
and quota-gated, but **no producer ever writes them**.

## The bug (verified by integration-checker 2026-06-28, code:line)

- **Declared:** `gateway/internal/billing/accountant.go:31` `AudioSecondsMs10 atomic.Int64`, `:32` `EmbedsCount atomic.Int64`.
- **Read at flush:** `gateway/internal/proxy/interceptor_usage.go:207` `usage.AudioSecondsMs10.Load()`, `:208` `usage.EmbedsCount.Load()` → copied into the billing event at `:271-272`.
- **NEVER written:** `grep -rn "AudioSecondsMs10.Store|AudioSecondsMs10.Add|EmbedsCount.Store|EmbedsCount.Add"` over `gateway/**.go` = **0 matches**. By contrast `TokensIn`/`TokensOut` ARE written at `interceptor_usage.go:165-166, 455-456, 531-532` (the chat-path analog to mirror).
- **Proxies wired WITHOUT a usage interceptor** in `gateway/cmd/gateway/main.go`:
  - Embed: `NewEmbeddingsProxy(cfg.UpstreamEmbedURL, log)` — `:576`
  - Local STT: `NewAudioProxy(cfg.UpstreamSTTURL, log, resolver)` — `:587`
  - STT tier-1 fallbacks: `buildGeminiSTTProxy` `:1618`, `buildGroqWhisperProxy` `:1647`, `buildOpenAIWhisperProxy` `:1671`, `NewDynamicOverrideSTTProxy` `:686`
  - All three constructors already accept variadic `...ProxyResponseInterceptor` — capability exists, main.go just never passes one. Chat proxies DO pass `usageInterceptor` at `:570, :652, :660` (the analog).
- **Double-fault:** `gateway/internal/proxy/interceptor_usage.go:210-215` skips the `billing_events` enqueue entirely when `tokensIn==0 && tokensOut==0 && audioSeconds==0 && embedsCount==0`. Once audio/embed are written this self-resolves, but an audio/embed-only request must NOT be dropped by a residual tokens-only guard — confirm the guard already includes the audio/embed terms (it does per the read above) and that the enqueue fires.
- **Downstream (the payoff):** `gateway/internal/quota/counters.go:84,87` (daily) + `:111,114` (monthly) gate on `AudioSeconds`/`EmbedsCount` from `usage_counters`, UPSERTed from `billing_events` (`gateway/internal/billing/events.go:32-33`). Source always 0 → `DailyAudioMinutes`/`DailyEmbeds`/`MonthlyAudioMinutes`/`MonthlyEmbeds` quotas can never trip; `/admin/usage` + dashboard `/consumo` + OBS-09 audio/embed dimension show 0.

## Locked decisions

1. **Mirror the chat-path interceptor.** The fix is a usage interceptor (a `ProxyResponseInterceptor` `ModifyResponse`) for STT and one for embed, modeled on the existing chat `usageInterceptor` in `interceptor_usage.go`. Do NOT invent a new billing path.
2. **Parse the real upstream response bodies:**
   - **STT** (OpenAI/Whisper + Gemini + Groq + local whisper): response JSON carries the transcription; audio duration comes from the `duration` field of the verbose/json response where present, else derive from the request audio length. Write `AudioSecondsMs10` (centi-seconds, matching the `Ms10` = 1/10s unit the field name implies — confirm the existing unit convention against how `counters.go` converts to `DailyAudioMinutes`).
   - **Embed** (OpenAI-compatible `/v1/embeddings`): response JSON `usage.prompt_tokens` + `data[]` length. Write `EmbedsCount` = number of embedding inputs (one per `data[]` element) — confirm against what `counters.go`/`DailyEmbeds` expects (count of embed requests vs count of vectors).
3. **Single fix surface:** the interceptor implementation (`gateway/internal/proxy/`) + wiring the 6 proxy constructors in `gateway/cmd/gateway/main.go`. No schema change (columns + reads already exist).
4. **Unit correctness is the trap.** `AudioSecondsMs10` and the `counters.go` minute/embed conversions must agree. The plan MUST verify the exact unit (read `counters.go` conversion + `events.go` UPSERT) before writing the producer, and add a unit test that asserts a known audio duration → expected `DailyAudioMinutes` and a known embed batch → expected `DailyEmbeds`.

## Scope fence

**IN:** STT + embed usage interceptors; wiring the 6 proxies in main.go; unit tests for
the producers + a quota-trip test (audio/embed quota actually blocks); confirming the
enqueue guard fires for audio/embed-only requests.

**OUT:** schema changes (none needed); the chat/token path (already correct); INT-01..06
client live-UAT (separate process gap); the 47 stale-checkbox reconciliation (separate
bookkeeping); OBS-09 economy/phantom panel logic (works — only the audio/embed *usage*
dimension was dead); dashboard UI changes (it already reads the columns — they'll just be
non-zero once the producer writes).

## Success criteria

- A real STT request (local whisper + at least one tier-1 fallback) writes a non-zero
  `audio_seconds` to `billing_events` → `usage_counters` → visible in `/admin/usage`.
- A real embed request writes a non-zero `embeds_count` through the same chain.
- Per-tenant audio quota and embed quota actually trip when exceeded (quota test green;
  previously impossible).
- `grep` for `AudioSecondsMs10.Store/.Add` + `EmbedsCount.Store/.Add` now returns the new
  producer sites (was 0).
- Unit tests assert unit-conversion correctness (duration→minutes, batch→embeds).
- No regression on the chat/token billing path.

## Requirements

- **TEN-04** — per-tenant audio/embed daily+monthly quota enforcement (currently UNENFORCED).
- **OBS-09** — audio/embed usage visibility in /consumo + economy panel (currently 0).

## Dependencies

- Phase 04 (quota schema, `counters.go`, the dangling-read consumers — the thing we feed).
- Phase 11.2 (local whisper STT path that must be metered).
- Phase 07/15 (dashboard /consumo + economy that reads the counters).
