---
plan: 04
phase: 2
title: Reverse proxies for /v1/chat/completions, /v1/embeddings, /v1/audio/transcriptions + ProxyResponseInterceptor
status: complete
completed_at: "2026-04-18"
requirements_addressed: [GW-02, GW-03, GW-04, GW-06, TEN-08]
---

# Plan 02-04 — SUMMARY

## What shipped

Three OpenAI-compatible reverse proxies wired into the auth-protected `/v1/*`
chi group, with a formal `ProxyResponseInterceptor` extension contract used
by Plan 02-05's audit tee.

### Files added/modified

**Proxy package (`gateway/internal/proxy/`):**
- `director.go` — shared `Director` builder: rewrites scheme/host, strips
  client auth headers (`Authorization`, `X-Api-Key`, `Cookie`,
  `OpenAI-Organization`, `OpenAI-Project`), propagates the gateway-generated
  `X-Request-ID` upstream (client-provided IDs are NOT forwarded).
- `detect.go` — `isSSE(*http.Response)` helper.
- `errors.go` — OpenAI-envelope `502` error handler:
  `{error:{type:"api_error", code:"upstream_unreachable"}}`.
- `interceptor.go` — `ProxyResponseInterceptor` interface
  (`Intercept(*http.Response) error`) + `composeInterceptors(...)` adapter
  that folds a variadic slice into a single `ModifyResponse`. **02-05
  audit tee plugs in here** — it does NOT mutate the `ReverseProxy` struct.
- `chat.go` — `NewChatProxy(upstream, log, interceptors ...)` with
  `FlushInterval: -1` for SSE per-chunk flush.
- `embeddings.go` — `NewEmbeddingsProxy(upstream, log, interceptors ...)`
  with default 0 (buffered — embeddings never stream).
- `audio.go` — `NewAudioProxy(upstream, log, interceptors ...)` with
  default 0 (Whisper response is JSON, not streamed); multipart boundary
  is preserved by NOT touching `Content-Type`.
- `*_test.go` — full coverage of the truths above (SSE first-byte timing,
  multipart preservation, 502 envelope on upstream-unreachable,
  non-200 passthrough, header stripping, X-Request-ID propagation,
  tool-call passthrough, interceptor composition + error handling).

**Router (`gateway/cmd/gateway/main.go`):**
- New `proxies` struct (chat / embed / audio) passed into `buildRouter`.
- Production `main` builds all three proxies (no interceptors yet — those
  arrive in 02-05) and mounts them under the auth group.
- Test variant tolerates nil fields → falls back to scaffold 501 so
  existing scaffold tests in `main_test.go` continue working without
  booting upstreams.
- `/v1/health/upstreams` stays 501 — wired in 02-05.

## Tasks

| Task | Description | TDD |
|------|-------------|-----|
| 1 | director + detect + errors + ProxyResponseInterceptor extension | RED `f5f67e2` → GREEN `bd50dd5` |
| 2 | chat/embeddings/audio proxies + main.go wiring | RED `3446b43` → GREEN `64edd18` |

## Commits

- `f5f67e2` test(02-04): add failing tests for director + detect + interceptor
- `bd50dd5` feat(02-04): implement director + detect + errors + interceptor extension
- `3446b43` test(02-04): add failing tests for chat/embeddings/audio proxies
- `64edd18` feat(02-04): implement chat/embeddings/audio proxies + wire into router

## Quality gates

- `gofmt -l gateway/...` → empty
- `go vet ./gateway/...` → clean
- `go build ./gateway/...` → clean
- `go test ./gateway/... -count=1` → all packages green
  (proxy 0.278s, cmd/gateway 0.040s, all others unchanged)

## Deviations

1. **Auto-resume after subagent quota hit (Rule 4 — checkpoint).**
   The original gsd-executor agent ran out of model quota partway through
   Task 2 GREEN. The orchestrator inspected the on-disk state, confirmed
   `go build ./gateway/...` and `go test ./gateway/internal/proxy/...`
   were green, and committed the remaining work as `64edd18` inline.
   No code-level deviation — the agent had already produced the correct
   implementation; only the final commit + this SUMMARY were carried
   over by the orchestrator.

2. **None at the code/contract level.** All Codex review revisions
   addressed in 02-REVIEWS.md were honored: ProxyResponseInterceptor is
   a documented extension point, FlushInterval -1 scoped to chat only,
   non-200 upstream passthrough test in place.

## Hand-off to 02-05

- `ProxyResponseInterceptor` contract is stable. 02-05's audit tee
  implements it and is passed via the variadic slot in `NewChatProxy`,
  `NewEmbeddingsProxy`, `NewAudioProxy` — no changes needed inside the
  proxy package.
- `/v1/health/upstreams` is still scaffold 501; 02-05 wires the real
  aggregator that fans out to UPSTREAM_LLM_URL/EMBED/STT.
- `cmd/gateway/main.go` `proxies` struct is the integration seam — 02-05
  adds the interceptor to all three constructors at the call site.
