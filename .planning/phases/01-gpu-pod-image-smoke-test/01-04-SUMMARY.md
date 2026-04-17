---
phase: 01-gpu-pod-image-smoke-test
plan: 04
subsystem: infra
tags: [go, health-bridge, http-server, probes, distroless, slog, sync.rwmutex, context-propagation]

# Dependency graph
requires:
  - phase: 01-gpu-pod-image-smoke-test/01
    provides: "github.com/ifixtelecom/gpu-ifix/pkg/openai — ChatCompletionRequest/Response, EmbeddingRequest/Response, TranscriptionResponse (imported by probes.go)"
  - phase: 01-gpu-pod-image-smoke-test/03
    provides: "pod/docker-compose.yml health-bridge service entry (image ghcr.io/ifixtelecom/ifix-ai-pod-health-bridge:latest, LLAMA_URL/SPEACHES_URL/INFINITY_URL/HEALTH_BRIDGE_PORT/PROBE_INTERVAL_SECONDS/LOG_LEVEL/ENV/SPEACHES_MODEL env contract, healthcheck=[/health-bridge --self-check])"
provides:
  - "pod/health-bridge/state.go — ProbeStatus (unknown/healthy/degraded/failed), ProbeResult, State (sync.RWMutex + deep-copy Snapshot), AggregateStatus worst-of, ClassifyLatency >=5000ms->degraded"
  - "pod/health-bridge/probes.go — newHTTPClient (MaxIdleConns=10, IdleConnTimeout=90s, ResponseHeaderTimeout=5s per PITFALLS §12), probeLLM/probeEmbed (5s ctx), probeSTT (10s ctx + in-memory silent WAV generator + SPEACHES_MODEL env), ProbeLoop with immediate-first + 10s ticker"
  - "pod/health-bridge/handlers.go — mux with 6 D-12 routes; /health exact-path check so /health/nonsense returns 404; statusCodeFor maps healthy->200 / degraded|failed|unknown->503"
  - "pod/health-bridge/main.go — Config loader for 7 env vars; slog JSON (prod) / Text (dev) with module=HEALTH_BRIDGE; SIGTERM/SIGINT -> context cancel -> srv.Shutdown(25s); --self-check flag for docker-compose healthcheck"
  - "pod/health-bridge/Dockerfile — multi-stage (golang:1.23-alpine builder + gcr.io/distroless/static-debian12 runtime), CGO_ENABLED=0 static build with -trimpath -ldflags=-s -w, 9100 EXPOSE, STOPSIGNAL SIGTERM, ENTRYPOINT [/health-bridge]"
  - "17 tests passing under -race (10 probe/state + 7 handler)"
  - "HTTP contract for Phase 2 gateway: flat {status, latency_ms, last_probe, error?} per upstream + {status, services, uptime_s, timestamp} for aggregate"
affects: [01-05, 01-06, 01-07, 01-08, 01-09, Phase 2 gateway]

# Tech tracking
tech-stack:
  added:
    - "log/slog (stdlib, Go 1.21+) — first use in Ifix ecosystem"
    - "net/http + httptest (stdlib) — probe servers mocked in tests"
    - "mime/multipart (stdlib) — probeSTT form-data request body"
    - "gcr.io/distroless/static-debian12 base image — first use in Ifix"
  patterns:
    - "Monorepo Go: pod/health-bridge imports github.com/ifixtelecom/gpu-ifix/pkg/openai via single repo-root go.mod (D-13 confirmed)"
    - "TDD RED/GREEN per task: failing tests committed as test(...) before feat(...) implementation"
    - "http.Client tuning: MaxIdleConns=10, MaxIdleConnsPerHost=4, IdleConnTimeout=90s, ResponseHeaderTimeout=5s — applies PITFALLS §Pitfall 12 verbatim"
    - "Always http.NewRequestWithContext for outbound calls (probe functions) — never the pre-context NewRequest variant"
    - "State snapshot deep copy to keep handler responses immutable from outside mutation"
    - "Distroless static Go binary: zero shell, zero libc, zoneinfo copied for TZ=America/Sao_Paulo runtime"
    - "Self-check flag for docker-compose healthcheck: container has no wget / python, healthcheck just re-invokes the binary with --self-check"

key-files:
  created:
    - "pod/health-bridge/state.go (130 lines)"
    - "pod/health-bridge/probes.go (256 lines)"
    - "pod/health-bridge/probes_test.go (174 lines)"
    - "pod/health-bridge/handlers.go (103 lines)"
    - "pod/health-bridge/main.go (172 lines)"
    - "pod/health-bridge/main_test.go (152 lines)"
    - "pod/health-bridge/Dockerfile (54 lines)"
  modified: []

key-decisions:
  - "ProbeLoop runs one probe synchronously BEFORE entering the ticker loop so the first /health call isn't guaranteed-degraded for the full 10s probe interval. State still seeds at StatusUnknown so /health during the first synchronous probe (which can take up to 5s for LLM, 10s for STT) still has a defined shape."
  - "AggregateStatus treats StatusUnknown as degraded (not healthy) so the aggregate /health endpoint reports 503 during startup grace — matches the D-12 semantics (gateway should wait for upstreams to become healthy, not pipe traffic blindly)."
  - "ClassifyLatency is exported (not an internal helper) so the Phase 2 gateway can reuse the same threshold when it implements its own secondary probe."
  - "probeSTT uses a 10s timeout (vs 5s for LLM/embed) because Whisper cold-forward on a 1-second silent WAV still takes ~1-2s on a warm GPU and the network round-trip + decode accounts for another 500ms. 10s leaves headroom without masking genuine failures."
  - "The --self-check flag PRECEDES loadConfig so the docker-compose healthcheck never initialises the probe loops or HTTP server just to print 'ok'. This keeps the healthcheck cost at single-digit milliseconds per probe."
  - "Distroless static-debian12 over Alpine: zero shell means exec-based injection attacks are literally impossible inside the container; static binary has no libc dependency so the runtime image is the Go binary + CA certs + zoneinfo, period."
  - "No USER directive in health-bridge Dockerfile: the distroless runtime runs as uid=0 by default (nonroot variant exists but complicates the base-image Pinning story). Health-bridge does not need GPU access, but running as root here matches the rest of the pod (llama/speaches/infinity all root for /dev/nvidia* access). Not a meaningful widening of the trust boundary since the container has no shell to exploit."

patterns-established:
  - "Go TDD gate sequence: test() commit with failing expectations (undefined symbols is a valid RED) -> feat() commit that introduces exports to satisfy the test. No REFACTOR needed when gofmt-clean on first pass."
  - "In-memory RIFF WAV generator for probe bodies: eliminates the need to bundle a fixture WAV inside the image. Phase 2 gateway can reuse generateSilentWAV if it adds a similar probe path."
  - "Env-var override with typed default: atoiOr / envOr helpers pattern for HEALTH_BRIDGE_PORT, PROBE_INTERVAL_SECONDS, LOG_LEVEL. Use this pattern in the Phase 2 gateway for DATABASE_URL / REDIS_URL / etc."
  - "slog.With('module', 'HEALTH_BRIDGE') at logger creation: every log line carries the module tag identically to Ifix cobrancas-api createLogger('MODULE_NAME'). Extend this to all future Go services."

requirements-completed: [POD-03]

# Metrics
duration: ~6 min
completed: 2026-04-17
---

# Phase 01 Plan 04: Health-bridge Go service Summary

**First Go service in the Ifix ecosystem: a single-binary health-bridge on port 9100 that probes llama/speaches/infinity every 10s with real OpenAI-compat calls, exposes 6 D-12 endpoints (/health, /health/live, /health/ready, /health/llm, /health/stt, /health/embed), and ships as a ~5.9 MB stripped static binary inside a distroless-static-debian12 image.**

## Performance

- **Duration:** ~6 min
- **Started:** 2026-04-17T23:19:50Z (worktree spawn)
- **Completed:** 2026-04-17T23:25:33Z
- **Tasks:** 3 / 3 (Tasks 1 + 2 had TDD sub-cycles)
- **Files created:** 7, modified: 0

## Accomplishments

- `pod/health-bridge/state.go` (130 lines): `ProbeStatus` enum (unknown/healthy/degraded/failed), `ProbeResult`, `State` with `sync.RWMutex`, `NewState` seeded with all three upstreams at StatusUnknown so JSON responses have a deterministic shape before the first probe tick. `Snapshot` returns a deep copy (verified by `TestState_SnapshotIsolation`). `AggregateStatus` is worst-of: failed > degraded > unknown > healthy. `ClassifyLatency` downgrades healthy >= 5000ms to degraded (D-12 semantics).
- `pod/health-bridge/probes.go` (256 lines): `newHTTPClient` tuned per PITFALLS §12 (MaxIdleConns=10, MaxIdleConnsPerHost=4, IdleConnTimeout=90s, ResponseHeaderTimeout=5s). `probeLLM` POSTs `{model:qwen, messages:[{role:user, content:ping}], max_tokens:1}` to `/v1/chat/completions`, 5s ctx. `probeEmbed` POSTs `{model:BAAI/bge-m3, input:[ping]}` to `/v1/embeddings`, 5s ctx, requires non-empty `data[0].embedding`. `probeSTT` POSTs a 1-second mono 16 kHz silent WAV (generated in-memory via `generateSilentWAV`) as multipart/form-data to `/v1/audio/transcriptions`, 10s ctx, with SPEACHES_MODEL env override (default `Systran/faster-whisper-large-v3`). `ProbeLoop` runs one probe immediately before entering the 10s ticker so state is defined on first scrape.
- `pod/health-bridge/handlers.go` (103 lines): `mux` wires all 6 D-12 routes. `/health` uses `strings.TrimRight(path, "/") != "/health"` to return 404 on `/health/nonsense` (net/http `ServeMux` prefix semantics would otherwise swallow the sub-path). `statusCodeFor` maps healthy->200, degraded/failed/unknown->503 (Kubernetes readiness-drain contract). Response body is flat per 01-PATTERNS.md §API response shape (NOT the `{data:T}` Ifix CRUD wrapper).
- `pod/health-bridge/main.go` (172 lines): `Config` loaded from HEALTH_BRIDGE_PORT/LLAMA_URL/SPEACHES_URL/INFINITY_URL/PROBE_INTERVAL_SECONDS/LOG_LEVEL/ENV with `atoiOr`/`envOr` helpers. `newLogger` returns `slog.JSON` in prod, `slog.Text` in dev, with `.With("module", "HEALTH_BRIDGE")` (matches Ifix `createLogger('MODULE_NAME')` convention). SIGTERM/SIGINT cancels the root context; probes exit on `<-ctx.Done()`; HTTP server shuts down with a 25s deadline. `--self-check` flag prints `ok` and exits 0 BEFORE config load so the docker-compose healthcheck has near-zero cost.
- `pod/health-bridge/Dockerfile` (54 lines): two-stage (golang:1.23-alpine builder + gcr.io/distroless/static-debian12 runtime). Static build with `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w"`. Zoneinfo copied from the builder stage so `TZ=America/Sao_Paulo` resolves at runtime. `EXPOSE 9100`, `STOPSIGNAL SIGTERM`, `ENTRYPOINT ["/health-bridge"]`.
- **17 tests pass under `-race`**: 10 probe/state tests (TestProbeLLM_Success/NonOK/Timeout, TestProbeEmbed_Success/Malformed, TestProbeSTT_Success, TestState_ConcurrentSet/SnapshotIsolation, TestClassifyLatency_Degraded, TestAggregateStatus) + 7 handler tests (TestHandleLive_Always200, TestHandleUpstream_Healthy_200 / Failed_503, TestHandleAggregate_AllHealthy_200 / OneFailed_503, TestUnknownPath_404, TestHealthReady_IsAggregate).

## Final Binary Size

| Variant | Size |
|---|---|
| Default `go build` | 8 921 146 bytes (~8.7 MB) — includes debug info, symbol table, DWARF |
| Stripped `-ldflags="-s -w" -trimpath` (Dockerfile default) | 6 066 436 bytes (~5.9 MB) |
| Expected container image (runtime stage) | ~8 MB (distroless-static-debian12 ≈ 2 MB + stripped binary 5.9 MB + zoneinfo ~500 KB) |

The plan target was ~10 MB; final image stays comfortably under that.

## Source Lines Per File

| File | Lines | Role |
|---|---|---|
| `state.go` | 130 | ProbeStatus / ProbeResult / State / NewState / Set / Get / Snapshot / Uptime / AggregateStatus / ClassifyLatency + DegradationLatencyMs |
| `probes.go` | 256 | newHTTPClient / probeLLM / probeEmbed / probeSTT / generateSilentWAV / writeU16LE / writeU32LE / success / failed / ProbeLoop |
| `probes_test.go` | 174 | 10 tests (probe + state + ClassifyLatency + AggregateStatus) |
| `handlers.go` | 103 | writeJSON / statusCodeFor / handleLive / handleUpstream / aggregateResponse / handleAggregate / mux |
| `main.go` | 172 | Config / loadConfig / envOr / atoiOr / newLogger / main (flag, signals, probes, HTTP, shutdown) |
| `main_test.go` | 152 | 7 handler tests |
| `Dockerfile` | 54 | 2-stage distroless build |
| **Total (source only, excluding tests)** | **715** | |
| **Total (all files, incl. tests + Dockerfile)** | **1 041** | |

## Task Commits

Each task committed atomically via `--no-verify` per worktree parallel-executor policy:

1. **Task 1: state + probes + tests (TDD)**
   - RED: `765de22` (test) — 10 failing tests (undefined symbols)
   - GREEN: `f5d6d2e` (feat) — state.go + probes.go
   - REFACTOR: none needed (gofmt-clean on first pass)
2. **Task 2: main + handlers + tests (TDD)**
   - RED: `44f836d` (test) — 7 failing tests (mux + aggregateResponse undefined)
   - GREEN: `dda5836` (feat) — main.go + handlers.go
   - REFACTOR: none needed
3. **Task 3: Dockerfile** — `88967ef` (feat)

**Plan metadata commit:** this SUMMARY.md commit, made after self-check.

## Files Created/Modified

| Path | Role | Notes |
|---|---|---|
| `pod/health-bridge/state.go` | Go source (package main) | 130 lines. ProbeStatus enum, ProbeResult, State with sync.RWMutex, ClassifyLatency, AggregateStatus worst-of. |
| `pod/health-bridge/probes.go` | Go source (package main) | 256 lines. Tuned http.Client (MaxIdleConns=10, IdleConnTimeout=90s, ResponseHeaderTimeout=5s), 3 probe functions (5s LLM/embed, 10s STT), in-memory RIFF WAV generator, ProbeLoop. |
| `pod/health-bridge/probes_test.go` | Go test | 174 lines. 10 tests. |
| `pod/health-bridge/handlers.go` | Go source (package main) | 103 lines. 6 D-12 routes wired in mux, exact /health path check, statusCodeFor mapping. |
| `pod/health-bridge/main.go` | Go source (package main) | 172 lines. Config loader, slog JSON/Text handler, SIGTERM/SIGINT -> 25s graceful shutdown, --self-check flag. |
| `pod/health-bridge/main_test.go` | Go test | 152 lines. 7 tests. |
| `pod/health-bridge/Dockerfile` | Dockerfile (distroless) | 54 lines. golang:1.23-alpine builder + gcr.io/distroless/static-debian12 runtime, CGO_ENABLED=0, -trimpath -ldflags=-s -w, TZ=America/Sao_Paulo. |

## Decisions Made

- **`ProbeLoop` runs one probe synchronously before entering the ticker:** otherwise /health would stay `unknown` (and therefore 503) for a full 10s after boot, even when all three upstreams are actually reachable. The immediate probe takes at most 10s (STT budget) which still fits inside the Docker start_period=120s declared in 01-03 compose.
- **AggregateStatus maps `StatusUnknown` to degraded, not healthy:** matches the D-12 semantics — a gateway polling `/health/ready` during startup should see 503 until the first successful probe round, then 200. Reporting healthy before any probe ran would silently route traffic to upstreams that may not even be listening yet.
- **ClassifyLatency exported, not internal:** Phase 2 gateway will implement a secondary latency gate during failover decisions; re-using the same `DegradationLatencyMs` threshold and classifier prevents drift between the two services.
- **STT probe timeout 10s vs 5s for LLM/embed:** Whisper cold-forward on a 1-second silent WAV on a warm GPU still takes ~1-2s; network RTT + multipart decode add ~500ms. 10s is generous enough to avoid false-positive "failed" on healthy upstreams, tight enough to surface genuine hangs within a probe cycle.
- **`--self-check` flag consumed BEFORE `loadConfig`:** the docker-compose healthcheck defined in 01-03 calls `/health-bridge --self-check` on every interval. Making that path skip env-var parsing / logger creation / probe spin-up keeps the per-healthcheck cost at ~1ms (binary exec + flag parse + fmt.Println + exit) vs ~50ms+ if we ran loadConfig.
- **Distroless `static-debian12` over Alpine or scratch:** scratch has no CA certs (breaks outbound HTTPS if a future probe hits an HTTPS upstream); Alpine has a shell + libc and isn't strictly necessary for a static Go binary. distroless-static-debian12 is the Ifix-ready sweet spot.
- **No USER directive:** distroless-static runs as root by default (uid=0). The health-bridge container has no GPU access and no write paths, so running as root is a cosmetic concern only. Switching to the `nonroot` distroless variant is a single-line change if Phase 2 security review flags it.

## Deviations from Plan

None — plan executed exactly as written.

One minor adjustment: the plan's Task 1 action block showed the `probes_test.go` imports grouped into two blocks (`log/slog` and `os` in a second block after `testing`). Go's `gofmt` folds these into a single alphabetically-sorted import block; the final file matches `gofmt` output. No semantic deviation.

## Authentication Gates

None — this plan is pure Go source + Dockerfile authoring. No external auth, no registries touched (GHCR push happens in plan 01-07 CI).

## Issues Encountered

- **Stray `health-bridge` binary at repo root after `go build ./pod/health-bridge/...` without `-o`:** Go builds the package and places the resulting binary named after the last path segment in the current working directory. Caught by `git status --short` during the post-commit deletion check; removed before the SUMMARY commit. The repo-root `.gitignore` excludes `*.exe`, `*.out`, `*.test`, and `bin/` but not an argv[0]-named binary — this is a known foot-gun of `go build ./...` and is handled in CI by always passing `-o <path>` (as the Dockerfile does).
- **`go test -race` is slow (5-7s):** one test (`TestProbeLLM_Timeout`) waits for the context deadline to expire (5s + buffer). Non-race run completes in <1s. Both pass. No action needed — this is the intended test semantics.

## User Setup Required

None — no external service configuration required for this plan. The health-bridge binary will be built by the CI workflow in plan 01-07 and pulled by the docker-compose file in plan 01-03 at pod bootstrap time via the `IFIX_AI_POD_HEALTH_BRIDGE_IMAGE` env var.

## Threat Flags

None — all new security-relevant surface is covered by the plan's `<threat_model>` (T-01-04-01 through T-01-04-05). No new endpoints beyond the six D-12 paths; no new auth paths (health-bridge is unauthenticated inside the pod internal network per the trust-boundary table); no new trust boundaries.

Mitigations applied per the threat model:
- T-01-04-01 (DoS HTTP server): ReadHeaderTimeout=5s, WriteTimeout=15s applied to the srv struct. Handler responses are bounded-size JSON of the current state snapshot.
- T-01-04-02 (DoS upstream probes): per-probe `context.WithTimeout` (5s LLM/embed, 10s STT); shared tuned `http.Client` with `MaxIdleConns=10`.
- T-01-04-04 (goroutine leak): all outbound requests use `http.NewRequestWithContext`; `defer resp.Body.Close()` on every response; `ProbeLoop` exits on `<-ctx.Done()`.

## Next Phase Readiness

This plan unblocks every remaining Phase 1 plan and the Phase 2 gateway:

- **Plan 01-05 (onstart.sh)** can rely on the health-bridge container coming up within the docker-compose start_period=30s declared in 01-03, and polling `curl -sf http://127.0.0.1:9100/health/ready` to detect when all three upstreams have passed their first probe.
- **Plan 01-06 (smoke.py)** can scrape `http://<pod-ip>:9100/health/llm | jq .latency_ms` to corroborate its own LLM p95 TTFT measurement, and `http://<pod-ip>:9100/health | jq .status` as a secondary gate before declaring the pod ready for the smoke sequence.
- **Plan 01-07 (build-pod.yml CI)** can build this Dockerfile with `docker buildx build --platform linux/amd64 -f pod/health-bridge/Dockerfile -t ghcr.io/ifixtelecom/ifix-ai-pod-health-bridge:<tag> .`. No build-args needed. Layer cache key is pkg/openai + pod/health-bridge — changes to llama-server flags in plan 01-03 won't invalidate this image's cache.
- **Plan 01-08 (smoke.yml CI)** can hit `:9100/health/ready` during pod startup polling (D-04 3-5 min cold-start) to decide when to launch the smoke.py workload.
- **Phase 2 gateway** imports `github.com/ifixtelecom/gpu-ifix/pkg/openai` identically to this plan, reuses `ClassifyLatency` / `DegradationLatencyMs` for its own failover thresholds, and polls the D-12 endpoints without parsing changes.

**TDD Gate Compliance:** Plan frontmatter is `type: auto` (not `type: tdd`), with two TDD-flagged tasks (Task 1 and Task 2). Gate sequence verified in git log:
- `765de22` (test) — Task 1 RED
- `f5d6d2e` (feat) — Task 1 GREEN
- `44f836d` (test) — Task 2 RED
- `dda5836` (feat) — Task 2 GREEN
- `88967ef` (feat) — Task 3 (no TDD)

REFACTOR gate not applicable — gofmt-clean / vet-clean on first pass for both tasks.

## Self-Check

**File existence (`[ -f path ]`):**
- `pod/health-bridge/state.go` — FOUND
- `pod/health-bridge/probes.go` — FOUND
- `pod/health-bridge/probes_test.go` — FOUND
- `pod/health-bridge/handlers.go` — FOUND
- `pod/health-bridge/main.go` — FOUND
- `pod/health-bridge/main_test.go` — FOUND
- `pod/health-bridge/Dockerfile` — FOUND

**Commit existence (`git log --oneline | grep`):**
- `765de22` (test RED Task 1) — FOUND
- `f5d6d2e` (feat GREEN Task 1) — FOUND
- `44f836d` (test RED Task 2) — FOUND
- `dda5836` (feat GREEN Task 2) — FOUND
- `88967ef` (feat Task 3 Dockerfile) — FOUND

**Plan-level verification block (from 01-04-PLAN.md lines 1297-1311):**
- `go build ./pod/health-bridge/...` — exit 0
- `go vet ./pod/health-bridge/...` — exit 0, empty output
- `go test ./pod/health-bridge/... -count=1 -race` — `ok github.com/ifixtelecom/gpu-ifix/pod/health-bridge 6.539s`
- `go build -o /tmp/health-bridge ./pod/health-bridge && /tmp/health-bridge --self-check` — prints `ok`, exit 0

**Task-level verify blocks (every grep/command in the plan):**
- Task 1 (probes/state + SPEACHES_MODEL env): all 10 tests pass, `SPEACHES_MODEL` grep OK, `os.Getenv("SPEACHES_MODEL")` grep OK, `Systran/faster-whisper-large-v3` default OK.
- Task 2 (handlers + --self-check): binary `--self-check` prints `ok`, all 7 handler tests pass.
- Task 3 (Dockerfile): all 11 acceptance greps pass (`FROM golang:1.23-alpine AS builder`, `FROM gcr.io/distroless/static-debian12 AS runtime`, `CGO_ENABLED=0`, `go build -trimpath`, `EXPOSE 9100`, `STOPSIGNAL SIGTERM`, `ENTRYPOINT ["/health-bridge"]`, `TZ=America/Sao_Paulo`, `COPY pkg/openai`, `COPY pod/health-bridge`).

**Additional checks:**
- `go.mod` unchanged (still stdlib-only: module github.com/ifixtelecom/gpu-ifix, go 1.23, no deps); no `go.sum` file.
- `gofmt -l pod/health-bridge/` — empty output.
- 17 `--- PASS:` lines in verbose test output (10 probe/state + 7 handler).
- No TODO/FIXME/placeholder in source.
- No stray build artifacts left in the tree.

## Self-Check: PASSED

---
*Phase: 01-gpu-pod-image-smoke-test*
*Plan: 04*
*Completed: 2026-04-17*
