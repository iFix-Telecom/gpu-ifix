# Phase 1: GPU Pod Image & Smoke-Test — Pattern Map

**Mapped:** 2026-04-17
**Files analyzed:** 16 new (greenfield repo, zero existing analogs inside `gpu-ifix/`)
**Analogs found (cross-repo, Ifix ecosystem):** 9 / 16 (remaining 7 are novel; use conceptual neighbors)

> **Note on codebase reality:** `gpu-ifix/` is greenfield — the only artifact is `ConverseAI_GPU_Stack_Guide.docx`. All analog hunting was done in sibling repos:
> - `/home/pedro/projetos/pedro/converseai-v4/` (Turborepo + Python agents + Docker + GH Actions + Portainer)
> - `/home/pedro/projetos/pedro/cobrancas-api/` (Bun standalone Dockerfile + simpler GH Actions)
> - `/home/pedro/projetos/pedro/auvo/`, `/home/pedro/projetos/pedro/campanhas-chatifix/` (Biome configs, docker-compose)
>
> Zero Go code exists in any Ifix repo. Everything `pod/health-bridge/*.go` is a new pattern — closest conceptual neighbors are the Python FastAPI `main.py` (probe + health endpoint layout) and the Bun-worker entrypoint (signal handling + config switch).

---

## File Classification

| Phase 1 file | Role | Data Flow | Closest Analog | Match Quality |
|--------------|------|-----------|----------------|---------------|
| `pod/Dockerfile` | Infra / Docker | build pipeline | `converseai-v4/agents/Dockerfile` + `cobrancas-api/Dockerfile` | role-match (both multi-stage, but neither has CUDA/GPU) |
| `pod/docker-compose.yml` | Infra / Docker | service orchestration | `converseai-v4/docker-compose.yml` | role-match (service shape same; no GPU directive in analog) |
| `pod/onstart.sh` | Infra / shell | provisioning | NO ANALOG — new pattern | conceptual: agents Dockerfile `CMD ["uvicorn" ...]`; closest shell script is `converseai-v4/scripts/backup/` (not read — low relevance) |
| `pod/health-bridge/main.go` | Go service (HTTP server) | request-response | NO ANALOG — first Go service in Ifix | conceptual: `converseai-v4/agents/src/main.py` (FastAPI + `/health` + `/health/ready`) |
| `pod/health-bridge/probes.go` | Go service (probe worker) | event-driven (ticker) | NO ANALOG — new pattern | conceptual: `converseai-v4/agents/src/main.py` lifespan (background tasks via `asyncio.create_task`) |
| `pod/health-bridge/types.go` | Go service (shared structs) | transform / data model | NO ANALOG — first Go types | conceptual: `converseai-v4/packages/shared/` Zod schemas (shape pattern: colocated types + validators) |
| `pod/health-bridge/go.mod` | Config | module manifest | NO ANALOG | conceptual: `converseai-v4/package.json` workspace root |
| `pod/health-bridge/Dockerfile` | Infra / Docker | build pipeline | `converseai-v4/agents/Dockerfile` (multi-stage builder → runtime) | role-match (same 2-stage pattern, Go binary replaces Python install) |
| `pod/smoke/smoke.py` | Python script (asyncio benchmark) | batch / streaming | NO ANALOG — new pattern | conceptual: `converseai-v4/agents/src/main.py` (asyncio patterns); `converseai-v4/agents/tests/` (pytest + AsyncMock) — not a bench tool, but closest |
| `pod/smoke/requirements.txt` | Config | Python deps | `converseai-v4/agents/requirements.txt` | exact (same file role) |
| `pod/smoke/fixtures/synthetic-audio-8min.wav` | Test fixture | data | NO ANALOG | N/A — binary fixture |
| `pod/smoke/report-schema.json` | Config | data schema | NO ANALOG — new JSON schema | conceptual: `converseai-v4/packages/shared/` Zod output types |
| `pod/templates/qwen3.5-27b-tool-calling.jinja` | Config | data | NO ANALOG — community template | N/A — carry external gist verbatim |
| `.github/workflows/build-pod.yml` | CI (GitHub Actions) | build + push | `converseai-v4/.github/workflows/deploy-dev.yml` | exact (ghcr.io/ifixtelecom push, GHA cache, matrix-style) |
| `.github/workflows/smoke.yml` | CI (GitHub Actions) | workflow_dispatch / external API | NO ANALOG — Vast.ai REST from GHA is new | conceptual: `converseai-v4/.github/workflows/ci.yml` deploy job (SSH + curl via `appleboy/ssh-action@v1`) — Vast.ai API replaces SSH |
| `pod/README.md` | Docs | — | `cobrancas-api/README.md` (not read but exists) | role-match |
| `pod/weights/README.md` | Docs | — | no direct analog | conceptual: `converseai-v4/agents/` README (if exists) |
| `go.mod` (repo root) | Config | Go monorepo manifest | NO ANALOG — first Go monorepo in Ifix | N/A — see "Monorepo Go structure" below |

---

## Pattern Assignments

### `pod/Dockerfile` (Infra / Docker, multi-stage CUDA build)

**Primary analog:** `/home/pedro/projetos/pedro/converseai-v4/agents/Dockerfile`
**Secondary analog:** `/home/pedro/projetos/pedro/cobrancas-api/Dockerfile`

**Multi-stage pattern (builder → production)** from `converseai-v4/agents/Dockerfile` lines 1-23:

```dockerfile
FROM python:3.12-slim AS builder
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends libpq-dev gcc && rm -rf /var/lib/apt/lists/*

# Install dependencies first (layer cache optimization)
COPY agents/requirements.txt .
RUN pip install --no-cache-dir --prefix=/install -r requirements.txt

# Production stage
FROM python:3.12-slim AS production
WORKDIR /app
ENV PYTHONUNBUFFERED=1
RUN apt-get update && apt-get install -y --no-install-recommends libpq5 && rm -rf /var/lib/apt/lists/*

# Copy installed packages
COPY --from=builder /install /usr/local

# Copy source code
COPY agents/src/ ./src/

EXPOSE 8000
STOPSIGNAL SIGTERM
CMD ["uvicorn", "src.main:app", "--host", "0.0.0.0", "--port", "8000", "--timeout-graceful-shutdown", "25"]
```

**Cobrancas three-stage install/build/release** from `cobrancas-api/Dockerfile` lines 1-31 (shows `USER bun` non-root pattern + `EXPOSE`):

```dockerfile
FROM oven/bun:1.3-alpine AS base
WORKDIR /app

# Stage 1: Install production dependencies only
FROM base AS install
COPY package.json bun.lock ./
RUN bun install --frozen-lockfile --production

# ... (build stage) ...

# Stage 3: Release — production image
FROM base AS release
COPY --from=install /app/node_modules ./node_modules
COPY --from=build /app/dist ./dist
COPY --from=build /app/package.json ./

ENV NODE_ENV=production
EXPOSE 3400

USER bun
CMD ["bun", "dist/index.js"]
```

**What to copy:**
- Multi-stage with `AS builder` / `AS production` labels
- `--no-install-recommends` + `rm -rf /var/lib/apt/lists/*` to keep image small
- `STOPSIGNAL SIGTERM` for graceful shutdown (D-03 onstart orchestration depends on this)
- `EXPOSE <port>` explicit
- `ENV PYTHONUNBUFFERED=1` equivalent for any Python layer (smoke runner)

**Divergences for `pod/Dockerfile` (novel, no analog):**
- Base image is `nvidia/cuda:12.x-cudnn-runtime-ubuntu22.04` (not slim Python or Bun Alpine)
- `COPY --from=ghcr.io/ggml-org/llama.cpp:server-cuda` to pull the llama-server binary out of the upstream image (multi-source build)
- No `USER` directive — GPU device access typically requires root inside container on Vast.ai (verify in Phase 1 execution)

---

### `pod/docker-compose.yml` (Infra / Docker, 5-service pod)

**Analog:** `/home/pedro/projetos/pedro/converseai-v4/docker-compose.yml` lines 1-360

**Shared `x-common-env` anchor + per-service override pattern** from lines 4-46:

```yaml
x-common-env: &common-env
  NODE_ENV: development
  TZ: America/Sao_Paulo
  DATABASE_URL: ${DATABASE_URL}
  # ... ~40 more env vars ...
  SENTRY_DSN: ${SENTRY_DSN:-}

services:
  api:
    image: ghcr.io/ifixtelecom/converseai-api:develop
    container_name: converseai-dev-api
    environment:
      <<: *common-env
      PORT: 3333
    networks:
      - traefik-public
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "--no-verbose", "--tries=1", "--spider", "http://127.0.0.1:3333/health/live"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 40s
```

**Compose healthcheck pattern** (lines 58-63, 130-135, 148-152 — used across 15+ services):

```yaml
healthcheck:
  test: ["CMD", "wget", "--no-verbose", "--tries=1", "--spider", "http://127.0.0.1:<port>/health/live"]
  interval: 30s
  timeout: 10s
  retries: 3
  start_period: 40s
```

Note: `agents` service uses a Python-based healthcheck (lines 130-135), because the container has no `wget`:

```yaml
healthcheck:
  test: ["CMD", "python3", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8000/health/live')"]
```

**What to copy:**
- `x-common-env` YAML anchor for env block shared between llama/speaches/infinity/health-bridge
- `${VAR}` interpolation (defaults to Portainer UI or `.env`) — no `.env.example` in the pod itself (D-06 versioned URLs go to env vars)
- `container_name: ifix-ai-pod-<service>` prefix convention (e.g. `ifix-ai-pod-llama`, `ifix-ai-pod-health-bridge`)
- `restart: unless-stopped`
- Every service gets a `healthcheck:` (except weights-only services)

**Divergences for `pod/docker-compose.yml` (novel):**
- `deploy.resources.reservations.devices` with `driver: nvidia, capabilities: [gpu]` on the 3 inference services + dcgm-exporter (no analog in Ifix)
- `ipc: host` or `shm_size: 1gb` may be needed for Whisper (research PITFALLS §1)
- `network_mode: host` acceptable on Vast.ai (single-tenant pod) OR shared internal network `ifix-ai-pod` — decision for planner
- No Traefik labels (pod doesn't terminate TLS; Vast.ai port-forwards directly)

---

### `pod/onstart.sh` (Infra / shell, Vast.ai onstart hook)

**Analog:** NO ANALOG — new pattern. Ifix has zero shell scripts that bootstrap a container orchestration from zero.

**Conceptual neighbor:** `converseai-v4/agents/Dockerfile` line 23 (`CMD ["uvicorn", ...]`) — the concept of "one entrypoint that starts many things" exists only in code form.

**Structural guidance (no excerpt to copy from):**

```bash
#!/usr/bin/env bash
set -euo pipefail

# Log to stdout + file so Vast.ai web console captures it
exec > >(tee -a /var/log/onstart.log) 2>&1

# 1. Pull weights in parallel from MinIO (D-02, D-03, D-05)
mkdir -p /weights/qwen /weights/whisper /weights/bge-m3
(
  aws s3 cp --endpoint-url "$MINIO_ENDPOINT" \
    "s3://$MINIO_BUCKET/qwen3.5-27b-Q4_K_M/v1.0.0/model.gguf" /weights/qwen/model.gguf \
  && sha256sum -c /weights/qwen/model.gguf.sha256
) &
QWEN_PID=$!
# (same for whisper + bge-m3 in parallel)
wait $QWEN_PID $WHISPER_PID $BGE_PID

# 2. Start compose once weights are in place
docker compose -f /opt/ifix-ai-pod/docker-compose.yml up -d

# 3. (Optional) Block until health-bridge /health returns healthy
until curl -sf http://localhost:9100/health | grep -q '"status":"healthy"'; do
  sleep 5
done
```

**What to copy (from Ifix conventions, not code):**
- `#!/usr/bin/env bash` + `set -euo pipefail` (strict bash — standard 2026 convention)
- `TZ=America/Sao_Paulo` exported early (matches Ifix convention — every compose file sets `TZ`)
- Checksum validation with `sha256sum -c` (D-05 requirement)
- Parallel downloads with `&` + `wait $PID1 $PID2 $PID3` (per D-03 target of 2-3 min)

---

### `pod/health-bridge/main.go` (Go service, HTTP server port 9100)

**Analog:** NO ANALOG — first Go service in Ifix.
**Closest conceptual neighbor:** `/home/pedro/projetos/pedro/converseai-v4/agents/src/main.py` lines 148-230

**Health endpoint trio (the pattern to mirror in Go):**

```python
@app.get("/health")
async def health():
    return {
        "status": "healthy",
        "service": "agent-runner",
        "version": "0.1.0",
    }


@app.get("/health/live")
async def health_live():
    """Simple liveness probe — process is alive."""
    return {"status": "ok"}


@app.get("/health/ready")
async def health_ready():
    """Readiness probe — checks PostgreSQL and RabbitMQ dependencies."""
    checks: dict = {}
    all_ok = True

    pg_start = time.monotonic()
    try:
        await asyncio.wait_for(db.fetch_one("SELECT 1 AS ok"), timeout=5.0)
        checks["postgres"] = {
            "status": "ok",
            "latencyMs": round((time.monotonic() - pg_start) * 1000),
        }
    except Exception as e:
        all_ok = False
        checks["postgres"] = {
            "status": "error",
            "latencyMs": round((time.monotonic() - pg_start) * 1000),
            "error": str(e),
        }

    # ... same for RabbitMQ ...

    status_str = "ok" if all_ok else "error"
    return JSONResponse(
        content={
            "status": status_str,
            "timestamp": datetime.now(timezone.utc).isoformat(),
            "uptime": time.monotonic(),
            "checks": checks,
        },
        status_code=200 if all_ok else 503,
    )
```

**What to copy (Go translation — see D-12 payload spec):**
- Three endpoints: `/health` (aggregate), `/health/live` (trivial liveness — returns 200 if process up), `/health/ready` (runs real probes per D-11)
- `GET /health/llm`, `GET /health/stt`, `GET /health/embed` (per-upstream, D-12) — NEW, no analog
- Response body JSON: `{status, latency_ms, last_probe, error?}` per D-12
- Return 503 when `status != healthy` (matches cobrancas pattern — `set.status = 503` in `cobrancas-api/src/modules/health/index.ts` line 66)
- Record `time.monotonic()` (Go: `time.Now()` delta) for latency measurement

**Go-specific structure the planner should assume (not a code excerpt — new pattern):**
- `net/http` stdlib server — DO NOT pull chi yet (reserved for gateway in Phase 2). Single binary, ~200 LOC.
- `log/slog` for structured logging (matches research STACK.md decision).
- Graceful shutdown: `http.Server.Shutdown(ctx)` on SIGTERM (matches `STOPSIGNAL SIGTERM` in Ifix Dockerfiles).
- Context propagation to probe goroutines via `context.WithCancel` derived from server context.

---

### `pod/health-bridge/probes.go` (Go service, ticker-driven probe loop)

**Analog:** NO ANALOG — new pattern.
**Closest conceptual neighbor:** `/home/pedro/projetos/pedro/converseai-v4/agents/src/main.py` lines 89-116 (background task pattern)

**Background task pattern (Python asyncio — translate to Go goroutine):**

```python
# Start the keyspace listener as a background task.
listener_task = asyncio.create_task(debounce_manager.start_listener())
stack.push_async_callback(lambda: _cancel_task(listener_task))

consumer_task = asyncio.create_task(
    rabbitmq.start_consumer(
        queue_name=QUEUES.ASSISTANT_V4,
        routing_key="process.#",
        callback=debounce_manager.on_message_received,
        env_prefix=env_prefix,
    )
)

# ...

async def _cancel_task(task: asyncio.Task) -> None:
    """Cancel a background task and await its termination (AsyncExitStack helper)."""
    task.cancel()
    try:
        await task
    except asyncio.CancelledError:
        pass
```

**What to copy (Go idioms):**
- One goroutine per probed upstream (llm, stt, embed) — each with its own `time.Ticker` on 10s (D-11)
- Shared `sync.RWMutex`-protected state struct holding latest probe result per upstream
- Context cancellation path: `ctx, cancel := context.WithCancel(ctx)` at main; cancel on shutdown → `<-ctx.Done()` breaks the ticker loop
- `http.Client` with tuned `Transport` (`MaxIdleConns`, `IdleConnTimeout`, `ResponseHeaderTimeout`) shared across all probes — per PITFALLS §12 goroutine/connection hygiene
- Always pass `ctx` to `http.NewRequestWithContext(ctx, ...)` — critical per PITFALLS §12

**Probes themselves (per D-11):**
- LLM probe: `POST /v1/chat/completions` with trivial prompt to `llama-server:8000`
- STT probe: `POST /v1/audio/transcriptions` with synthetic-audio fixture to `speaches:8001`
- Embed probe: `POST /v1/embeddings` with 1-token text to `infinity:8002`
- Record `time.Since(start)` as `latency_ms` in state

---

### `pod/health-bridge/types.go` (Go service, SHARED OpenAI-compat structs)

**Analog:** NO ANALOG — first Go type file in Ifix.
**Closest conceptual neighbor:** `/home/pedro/projetos/pedro/converseai-v4/packages/shared/` — the "shared types between apps" concept exists as Zod schemas in `packages/shared/` workspace.

**Pattern to mirror (from `packages/shared/` conventions per CLAUDE.md):**
- All cross-component types live in ONE location
- Types are colocated with their validators (Zod in TS; explicit struct tags in Go)
- Inferred/exported TypeScript types (`export type X = z.infer<typeof XSchema>`) → in Go, exported struct with `json:` tags

**D-13 Locked decision — monorepo Go with shared structs:**
> Structs de request/response OpenAI-compat **compartilhadas** com o gateway Go (Phase 2) — mesmo módulo no repo (monorepo Go).

**Monorepo Go structure (CRITICAL for planner):**

```
gpu-ifix/
├── go.mod                         ← module github.com/ifixtelecom/gpu-ifix
├── go.sum
├── pod/
│   └── health-bridge/
│       ├── main.go                ← package main
│       ├── probes.go              ← package main
│       └── types.go               ← DEPRECATED placement
└── pkg/
    └── openai/                    ← NEW: shared package
        └── types.go               ← package openai — ChatCompletionRequest, etc.
```

**Recommendation to planner:** Put shared structs under `pkg/openai/` (or `internal/openai/` if never exported to other repos) at repo root — NOT under `pod/health-bridge/`. Reason: Phase 2 gateway will `import "github.com/ifixtelecom/gpu-ifix/pkg/openai"` for the same structs. Keeping types inside `pod/health-bridge/` creates an awkward import path (`github.com/.../pod/health-bridge` for the gateway to import).

**Single `go.mod` at repo root** (not per-service). This is the monorepo Go pattern intended by D-13.

**Structs to define (from research SUMMARY.md):**
- `ChatCompletionRequest`, `ChatCompletionResponse`, `ChatCompletionChoice`, `ChatCompletionMessage`
- `EmbeddingRequest`, `EmbeddingResponse`, `Embedding`
- `TranscriptionRequest`, `TranscriptionResponse`
- `ErrorResponse` (OpenAI error envelope)
- All with `json:"..."` tags + `json:"...,omitempty"` where appropriate

---

### `pod/health-bridge/go.mod` (Config, Go module manifest)

**Analog:** NO ANALOG — first Go module in Ifix.

**Pattern (new; from Go ecosystem conventions):**

```
module github.com/ifixtelecom/gpu-ifix

go 1.23

// Phase 1: stdlib only for health-bridge. No external deps.
// Phase 2 will add: chi, pgx, go-redis, gobreaker, backoff, prometheus, sentry-go
```

**Key decision for planner (per D-13):**
- ONE `go.mod` at repo root (monorepo Go), NOT one per subdirectory.
- Phase 1 goal: stdlib-only health-bridge. Avoids committing to chi/etc. before Phase 2 decides the gateway structure.

---

### `pod/health-bridge/Dockerfile` (Infra / Docker, Go multi-stage)

**Analog:** `/home/pedro/projetos/pedro/converseai-v4/agents/Dockerfile` lines 1-23 (two-stage builder → slim runtime)

**Pattern to adapt:**

```dockerfile
# Stage 1: builder with Go toolchain
FROM golang:1.23-alpine AS builder
WORKDIR /build

# Deps first for layer cache
COPY go.mod go.sum ./
RUN go mod download

# Source
COPY pkg/openai ./pkg/openai
COPY pod/health-bridge ./pod/health-bridge
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /health-bridge ./pod/health-bridge

# Stage 2: minimal runtime
FROM gcr.io/distroless/static-debian12 AS production
COPY --from=builder /health-bridge /health-bridge
EXPOSE 9100
STOPSIGNAL SIGTERM
ENTRYPOINT ["/health-bridge"]
```

**What to copy from agents/Dockerfile:**
- Two-stage with `AS builder` and `AS production`
- `EXPOSE` + `STOPSIGNAL SIGTERM`
- Deps-first `COPY go.mod go.sum && RUN go mod download` (layer caching — matches `COPY agents/requirements.txt` pattern)

**Divergences:**
- Base stage: `golang:1.23-alpine` (not Python slim)
- Runtime base: `gcr.io/distroless/static-debian12` (zero attack surface; no shell, no libc) — safer than `alpine` for a static Go binary
- `CGO_ENABLED=0` + `-ldflags="-s -w"` for a small static binary

---

### `pod/smoke/smoke.py` (Python asyncio benchmark script)

**Analog:** NO ANALOG — new pattern. Ifix has no load-testing code.
**Closest conceptual neighbor 1:** `/home/pedro/projetos/pedro/converseai-v4/agents/src/main.py` lines 58-116 (asyncio + `asyncio.gather`, `asyncio.create_task`, `asyncio.wait_for`)
**Closest conceptual neighbor 2:** `/home/pedro/projetos/pedro/converseai-v4/agents/tests/test_agent_worker.py` (pytest asyncio structure — BUT smoke.py is NOT a pytest, it's a standalone script)

**Asyncio concurrent-task pattern from agents/main.py lines 100-116:**

```python
consumer_task = asyncio.create_task(
    rabbitmq.start_consumer(
        queue_name=QUEUES.ASSISTANT_V4,
        routing_key="process.#",
        callback=debounce_manager.on_message_received,
        env_prefix=env_prefix,
    )
)

label_consumer_task = asyncio.create_task(
    rabbitmq.start_consumer(
        queue_name=QUEUES.AGENT_LABEL,
        routing_key="process.label.#",
        callback=process_label_event,
        env_prefix=env_prefix,
    )
)

# Wait for shutdown
await asyncio.gather(consumer_task, label_consumer_task)
```

**Timing pattern from main.py lines 190-194:**

```python
pg_start = time.monotonic()
try:
    await asyncio.wait_for(db.fetch_one("SELECT 1 AS ok"), timeout=5.0)
    checks["postgres"] = {
        "status": "ok",
        "latencyMs": round((time.monotonic() - pg_start) * 1000),
    }
```

**What to copy for `smoke.py`:**
- `asyncio.gather(chat_task_1, chat_task_2, whisper_task, embed_batch_task)` — 2 concurrent chats + 1 Whisper + 10 embeddings (D-17)
- `time.monotonic()` before/after for latency (not `time.time()`)
- `structlog` (if `agents/` is followed) OR plain `print(json.dumps(...))` — smoke.py output is intentionally machine-readable per D-18
- HTTP client: `httpx.AsyncClient` (already in `agents/requirements.txt` — same convention)
- Streaming the `/v1/chat/completions` SSE: `async for chunk in response.aiter_lines():` (stdlib-adjacent httpx pattern)
- dcgm-exporter scraping: `asyncio.create_task(scrape_loop(interval=1.0))` with cancel-on-main-done

**Output format per D-18 (mandatory fields):**
```json
{
  "vram_peak_gb": 20.3,
  "vram_p95_gb": 19.8,
  "llm_p95_ttft_ms": 2100,
  "llm_p95_tpot_ms": 45,
  "whisper_latency_s": 12.4,
  "embed_p95_ms": 85,
  "tool_call_valid": true,
  "errors": []
}
```

---

### `pod/smoke/requirements.txt` (Config, Python deps)

**Analog:** `/home/pedro/projetos/pedro/converseai-v4/agents/requirements.txt` lines 1-30

**Pattern to copy (version-pinned, stdlib-preferred):**

```
fastapi>=0.109.0
uvicorn[standard]>=0.27.0
...
httpx>=0.27.0
...
structlog>=24.1.0
orjson>=3.9.0
...
pytest>=8.0.0
pytest-asyncio>=0.23.0
```

**What to copy:**
- `>=X.Y.Z` version floor (not `==` — the Ifix convention)
- Split runtime vs test deps? Agents file mixes them; smoke.py can follow suit (tiny script, no test deps likely).

**Smoke-test deps (from D-17, D-18):**
```
httpx>=0.27.0
structlog>=24.1.0       # optional — or use stdlib logging
prometheus-client>=0.20.0  # parse dcgm-exporter /metrics
numpy>=1.26.0           # p95 / percentile math
```

---

### `pod/smoke/report-schema.json` (Config, JSON schema for smoke-report)

**Analog:** NO ANALOG — new file.
**Conceptual neighbor:** Ifix uses Zod schemas for validation, not JSON Schema. The closest convention to mirror is colocation.

**What to produce (new pattern):**
- Standard JSON Schema Draft 2020-12 (`"$schema": "https://json-schema.org/draft/2020-12/schema"`)
- Required fields from D-18: `vram_peak_gb`, `vram_p95_gb`, `llm_p95_ttft_ms`, `llm_p95_tpot_ms`, `whisper_latency_s`, `embed_p95_ms`, `tool_call_valid`, `errors`
- `additionalProperties: false` to prevent silent drift
- Versioned: `"version": "1.0.0"` inside the schema (so Phase 5 can evolve it without breaking baselines)

---

### `pod/templates/qwen3.5-27b-tool-calling.jinja` (Config, community patched template)

**Analog:** NO ANALOG — external resource committed verbatim.
**Source:** https://gist.github.com/sudoingX/c2facf7d8f7608c65c1024ef3b22d431 (per D-14, canonical_refs).

**Pattern:** Just commit the file as-is. Add a top-of-file comment with:
- Source URL (gist permalink)
- Date fetched
- SHA-256 of file content (so drift is detectable)
- D-16 note: "Review upstream Qwen/unsloth each major release; migrate to stock template if bug fixed"

---

### `.github/workflows/build-pod.yml` (CI, GitHub Actions build + push)

**Analog:** `/home/pedro/projetos/pedro/converseai-v4/.github/workflows/deploy-dev.yml` lines 110-160

**Docker build + push pattern (lines 110-159):**

```yaml
  docker:
    name: Build & Push (${{ matrix.app }})
    runs-on: ubuntu-latest
    needs: [changes]
    if: needs.changes.outputs.matrix != '[]'
    strategy:
      fail-fast: false
      matrix:
        app: ${{ fromJSON(needs.changes.outputs.matrix) }}
    steps:
      - uses: actions/checkout@v4

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Login to GHCR
        uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Build and push
        uses: docker/build-push-action@v6
        with:
          context: .
          file: ${{ steps.dockerfile.outputs.path }}
          push: true
          tags: |
            ghcr.io/ifixtelecom/converseai-${{ matrix.app }}:develop
            ghcr.io/ifixtelecom/converseai-${{ matrix.app }}:dev-${{ github.sha }}
          cache-from: type=gha,scope=dev-${{ matrix.app }}
          cache-to: type=gha,scope=dev-${{ matrix.app }},mode=max
          build-args: |
            DATABASE_URL=postgresql://ci:ci@localhost:5432/ci
            ...
```

**Tag convention (lines 149-151):** `<image>:<branch>` + `<image>:<branch>-<sha>` (e.g. `develop`, `dev-abc123`).
**Main branch convention from `ci.yml` lines 332-334:** `<image>:latest` + `<image>:sha-<sha>`.

**Portainer webhook trigger from deploy-dev.yml lines 161-173:**

```yaml
  deploy:
    name: Trigger Portainer Redeploy
    runs-on: ubuntu-latest
    needs: [docker]
    if: always() && needs.docker.result == 'success'
    steps:
      - name: Trigger Portainer redeploy
        if: env.PORTAINER_WEBHOOK_URL_DEV != ''
        env:
          PORTAINER_WEBHOOK_URL_DEV: ${{ secrets.PORTAINER_WEBHOOK_URL_DEV }}
        run: |
          curl -X POST "$PORTAINER_WEBHOOK_URL_DEV" --fail --silent --show-error
          echo "Portainer webhook triggered — containers will be recreated with new images"
```

**What to copy:**
- `uses: actions/checkout@v4` + `uses: docker/setup-buildx-action@v3` + `uses: docker/login-action@v3` (exact actions, not newer versions)
- `permissions: { contents: read, packages: write }` — needed to push to ghcr.io
- `username: ${{ github.actor }}` + `password: ${{ secrets.GITHUB_TOKEN }}` (works because `ifixtelecom` org has GHCR enabled)
- Tag pattern: `ghcr.io/ifixtelecom/ifix-ai-pod:{branch}` + `ghcr.io/ifixtelecom/ifix-ai-pod:{branch}-{sha}` (per D-21)
- `cache-from` / `cache-to` with `type=gha,scope=<unique>` (build speed)
- `concurrency: { group: ..., cancel-in-progress: true }` (from both workflows, top)

**Divergences for `build-pod.yml`:**
- NO Portainer webhook (D-21 — pod runs on Vast.ai, not Portainer)
- NO matrix (single Dockerfile `pod/Dockerfile`)
- Trigger: `on: { push: { branches: [main, develop] }, workflow_dispatch: {} }` (D-21)
- Stable tag `:v1.0.0` + `:latest` is PROMOTED manually per D-23 — not auto-tagged

---

### `.github/workflows/smoke.yml` (CI, Vast.ai pod lifecycle + smoke run)

**Analog:** NO direct analog — Vast.ai REST API from GHA is new.
**Closest conceptual neighbor:** `/home/pedro/projetos/pedro/converseai-v4/.github/workflows/ci.yml` lines 404-427 (SSH-driven deploy via `appleboy/ssh-action@v1`)

**SSH-action deploy pattern (the closest "remote orchestration from GHA" we have):**

```yaml
      - name: Deploy to production via SSH
        uses: appleboy/ssh-action@v1
        with:
          host: ${{ secrets.DEPLOY_SSH_HOST }}
          username: ${{ secrets.DEPLOY_SSH_USER }}
          password: ${{ secrets.DEPLOY_SSH_PASSWORD }}
          script: |
            echo "=== Deploying converseai stack ==="
            cd /opt/converseai
            curl -sL "https://raw.githubusercontent.com/IfixTelecom/Converseai-V2/main/docker-stack.yml" -o docker-stack.yml.new
            if [ -s docker-stack.yml.new ]; then
              mv docker-stack.yml.new docker-stack.yml
              echo "Stack file updated from GitHub"
            else
              echo "::warning::Failed to download stack file, using existing"
              rm -f docker-stack.yml.new
            fi
            docker stack deploy -c docker-stack.yml --with-registry-auth converseai
            echo "=== Stack deployed ==="
            sleep 10
            docker service ls --format "{{.Name}}\t{{.Replicas}}" | grep converseai | sort
```

**What to copy (pattern level, not code):**
- `workflow_dispatch:` trigger with manual button (per D-22)
- `secrets.VAST_AI_API_KEY` stored in GH Secrets (matches `DEPLOY_SSH_PASSWORD` secret pattern)
- Multi-step with explicit sections: `echo "=== Creating pod ==="` / `echo "=== Running smoke ==="` / `echo "=== Destroying pod ==="` (operator-readable logs)
- `actions/upload-artifact@v4` for the `smoke-report.json` (artifact per commit — D-18, D-20)

**Structural guidance for smoke.yml (new, no excerpt to copy):**
- Step 1: Create pod via Vast.ai REST (`PUT /asks/{id}/` — research STACK.md line 152)
- Step 2: Poll `GET /instances/{id}/` for `actual_status == "running"` (with 10-min timeout)
- Step 3: Run `python pod/smoke/smoke.py --target <pod-ip>:9100` from GHA runner, write `smoke-report.json`
- Step 4: `actions/upload-artifact@v4` the report
- Step 5: `DELETE /instances/{id}/` in a `if: always()` cleanup (guaranteed teardown per D-22 cost cap)
- Step 6: Assert gates D-19 → `exit 1` if any fail (so the workflow run is red)

---

### `pod/README.md` (Docs)

**Analog:** `/home/pedro/projetos/pedro/cobrancas-api/README.md` (exists — not read; role-match only)

**What to copy (from Ifix conventions per CLAUDE.md):**
- Operator runbook sections: "How to run locally (docker compose up)", "How to interpret smoke-report.json", "Gates + what to do if they fail"
- Pt-BR for operator-facing prose (Ifix convention)
- Code blocks with language tags (` ```bash ` not ` ``` `)

---

### `pod/weights/README.md` (Docs)

**Analog:** None direct.
**Content (per CONTEXT.md Claude's Discretion — "Estratégia de upload inicial dos weights para MinIO — script one-shot a ser documentado"):**
- How to fetch weights from HuggingFace (Unsloth Qwen3.5-27B-GGUF Q4_K_M)
- How to upload to MinIO with SHA-256 sidecar file
- URL versioning convention per D-06 (`s3://ifix-ai-weights/qwen3.5-27b-Q4_K_M/v1.0.0/model.gguf`)

---

### `go.mod` (repo root)

**Covered above under `pod/health-bridge/go.mod`.** Key decision: single `go.mod` at repo root per monorepo Go D-13.

---

## Shared Patterns

### Timezone / locale
**Source:** every Ifix docker-compose (e.g., `converseai-v4/docker-compose.yml` line 6)
**Apply to:** `pod/docker-compose.yml`, `pod/onstart.sh`, Go binary env
```yaml
TZ: America/Sao_Paulo
```
Enforced across all Ifix services. Log timestamps + ISO strings must reflect São Paulo offset.

---

### Structured logging (language-specific)
**TypeScript/Node source:** `/home/pedro/projetos/pedro/cobrancas-api/src/lib/logger.ts` lines 74-92 + `converseai-v4/apps/worker/src/shared/logger.ts` lines 1-40 (pino wrapper).
**Python source:** `/home/pedro/projetos/pedro/converseai-v4/agents/src/main.py` lines 36-45 (structlog).
**Apply to:** health-bridge Go binary + smoke.py

**Ifix convention:** `createLogger('MODULE_NAME')` — module name is always uppercase snake_case:

```typescript
// cobrancas-api/src/lib/logger.ts:74-92
export function createLogger(module: string, baseContext: Record<string, unknown> = {}): Logger {
  function log(level: LogLevel, msg: string, extra?: Record<string, unknown>): void {
    emit({
      level,
      msg,
      ts: nowLocal(),
      module,
      ...baseContext,
      ...extra,
    })
  }
  return {
    info: (msg, extra) => log('info', msg, extra),
    warn: (msg, extra) => log('warn', msg, extra),
    error: (msg, extra) => log('error', msg, extra),
    child: (extraContext) => createLogger(module, { ...baseContext, ...extraContext }),
  }
}
```

**Go translation (new pattern):** Use stdlib `log/slog` with JSON handler. Attach a `module` attribute on every log line:

```go
logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("module", "HEALTH_BRIDGE")
logger.Info("probe completed", "upstream", "llm", "latency_ms", 120)
```

**For smoke.py:** import `structlog` (matches `agents/src/main.py` lines 36-45) — same config recipe.

---

### Healthcheck three-tier pattern
**Source:** `/home/pedro/projetos/pedro/converseai-v4/agents/src/main.py` lines 167-230
**Apply to:** `pod/health-bridge/main.go`
- `/health` — quick aggregate status (no external deps)
- `/health/live` — liveness (process up — always 200 if reachable)
- `/health/ready` — readiness (runs real probes; 503 if any upstream unhealthy)

Docker-compose healthcheck hits `/health/live` (so restarts don't cascade when upstream is flaky). Gateway (Phase 2) hits `/health/ready` (or the per-upstream endpoints `/health/llm`, `/health/stt`, `/health/embed`).

---

### Graceful shutdown on SIGTERM
**Source:** `converseai-v4/agents/Dockerfile` line 22 + `converseai-v4/apps/worker/src/entrypoint.ts` lines 40-44
**Apply to:** `pod/health-bridge/Dockerfile` + Go code + smoke.py (best-effort)

```dockerfile
STOPSIGNAL SIGTERM
CMD [..., "--timeout-graceful-shutdown", "25"]
```

```typescript
// worker/src/entrypoint.ts:38-44
const shutdown = async () => {
  log.info('Shutting down email worker...')
  await worker.close()
  process.exit(0)
}
process.on('SIGTERM', shutdown)
process.on('SIGINT', shutdown)
```

**Go translation:** `http.Server.Shutdown(ctx)` called from a `signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)` handler with a bounded context (e.g., 25s to match Ifix default).

---

### GitHub Actions concurrency + permissions
**Source:** both `converseai-v4/.github/workflows/ci.yml` and `deploy-dev.yml`
**Apply to:** `.github/workflows/build-pod.yml` and `.github/workflows/smoke.yml`

```yaml
concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true

permissions:
  contents: read
  packages: write
```

`smoke.yml` should use `cancel-in-progress: false` (a smoke run should NOT be cancelled mid-flight — cost risk of leaked Vast.ai pod).

---

### .dockerignore
**Source:** `converseai-v4/.dockerignore` lines 1-49 (comprehensive) + `cobrancas-api/.dockerignore` lines 1-10 (minimal)
**Apply to:** `.dockerignore` (repo root of gpu-ifix)

Key exclusions (from converseai-v4):
```
.git
.planning
*.md
!README.md
.env
.env.*
!.env.example
node_modules
dist
*.png
*.jpg
docker-compose*.yml
Dockerfile*
```

For gpu-ifix specifically: also exclude `pod/weights/*` (no weights in image — D-01) and `pod/smoke/fixtures/*.wav` (only needed at smoke-run time; not in pod image).

---

## Ifix-Wide Conventions to Honor

Extracted from `/home/pedro/projetos/pedro/CLAUDE.md` and confirmed by inspecting sibling repos:

| Convention | Source in CLAUDE.md | Apply to |
|---|---|---|
| **File naming: kebab-case** | "Use kebab-case for all file names" | `pod/health-bridge/main.go`, `pod/onstart.sh`, `pod/templates/qwen3.5-27b-tool-calling.jinja`, `.github/workflows/build-pod.yml` |
| **Logger: `createLogger('MODULE_NAME')`** | "Logger instances: `const log = createLogger('MODULE_NAME')`" | Go: `slog.New(...).With("module", "HEALTH_BRIDGE")`. Python smoke: `structlog.get_logger().bind(module="SMOKE")` |
| **Constants: UPPER_SNAKE_CASE** | "Constants use UPPER_SNAKE_CASE: `BASE_URL`, `MAX_PAGES`" | Go: `const DefaultProbeInterval = 10 * time.Second` + env vars like `PROBE_INTERVAL_SECONDS` |
| **camelCase for Go identifiers** | (implicit — Go stdlib) | Go idiomatic; no override |
| **Error handling in external clients: retryable vs non-retryable** | "Retryable: 5xx errors, rate limits (with backoff from `retryAfterMs`). Non-retryable: 4xx" | Phase 2 gateway; Phase 1 health-bridge just records probe error state — no retry (ticker retries next tick) |
| **Logger NDJSON in prod, pretty in dev** | "NDJSON in production, pretty-printed in development" | Go slog: `slog.NewJSONHandler(os.Stdout, nil)` always (structured = NDJSON). Use `slog.NewTextHandler` if `ENV=development` is set |
| **Timezone-aware timestamps (America/Sao_Paulo)** | "Timezone-aware timestamps (America/Sao_Paulo)" | Go: `time.FixedZone("America/Sao_Paulo", -3*3600)` OR env `TZ=America/Sao_Paulo` + `time.Local` |
| **JSDoc `@module` tag** | "JSDoc block comments on module entry points with `@module` tag" | Python: `"""Module docstring"""` at top (matches agents/src/main.py). Go: package comment above `package main` |
| **API response shape `{ data: T }` or `{ data: T[], total: N }`** | "API endpoints return `{ data: T }` or `{ data: T[], total: number }`" | Health-bridge responses per D-12: flat payload `{status, latency_ms, last_probe, error?}` — NOT `{ data: ... }`. Reason: OpenAI-compat health endpoints are flat; the `{data}` convention is for CRUD responses only |
| **No explicit formatter for .go files** | (new territory) | **Recommend to planner:** `gofmt` + `goimports` + `golangci-lint` with stdlib + chi rulesets. No Biome for Go. |
| **No `.env.example` at pod level** | (inferred — secrets live in GH Secrets + Portainer UI) | For pod: Vast.ai env vars injected at pod creation via API (smoke.yml). For GHA: `secrets.VAST_AI_API_KEY`, `secrets.MINIO_ACCESS_KEY` |
| **Ifix GHCR namespace: `ghcr.io/ifixtelecom/`** | (from converseai-v4/ci.yml:333) | Pod image: `ghcr.io/ifixtelecom/ifix-ai-pod:<tag>` (D-01) |
| **Branch tagging: `{branch}` + `{branch}-{sha}`** | (from converseai-v4/deploy-dev.yml:149-151) | Exact: `ghcr.io/ifixtelecom/ifix-ai-pod:develop`, `ghcr.io/ifixtelecom/ifix-ai-pod:dev-<sha>`, `ghcr.io/ifixtelecom/ifix-ai-pod:main`, `ghcr.io/ifixtelecom/ifix-ai-pod:sha-<sha>` |

---

## Cross-Repo Dependencies for Future Phases

**Critical for planner:** `pod/health-bridge/types.go` (or `pkg/openai/types.go` per recommendation above) WILL be shared with `gateway/` in Phase 2. The monorepo Go layout must be correct from Phase 1.

**Recommended repo structure (planner should confirm or adjust):**

```
gpu-ifix/
├── go.mod                    ← single module: github.com/ifixtelecom/gpu-ifix
├── go.sum
├── pkg/
│   └── openai/
│       ├── types.go          ← ChatCompletionRequest, EmbeddingRequest, etc.
│       └── types_test.go
├── pod/
│   ├── Dockerfile
│   ├── docker-compose.yml
│   ├── onstart.sh
│   ├── health-bridge/
│   │   ├── main.go
│   │   ├── probes.go
│   │   ├── Dockerfile
│   │   └── main_test.go
│   ├── smoke/
│   │   ├── smoke.py
│   │   ├── requirements.txt
│   │   ├── report-schema.json
│   │   └── fixtures/
│   │       └── synthetic-audio-8min.wav
│   ├── templates/
│   │   └── qwen3.5-27b-tool-calling.jinja
│   ├── weights/
│   │   └── README.md
│   └── README.md
├── gateway/                  ← Phase 2 (empty now; structure reserved)
├── .github/
│   └── workflows/
│       ├── build-pod.yml
│       └── smoke.yml
├── .dockerignore
├── .gitignore
└── README.md
```

**Why this structure:**
- `pkg/openai/` is importable by both `pod/health-bridge/` (Phase 1) and `gateway/` (Phase 2) via the same import path `github.com/ifixtelecom/gpu-ifix/pkg/openai`. No import path churn between phases.
- One `go.mod` at repo root = true monorepo Go (per D-13).
- `pod/` isolates everything pod-specific (Dockerfile, compose, smoke, weights) — makes it trivial to reason about "what goes in the image" vs "what runs in CI".
- Workflows at `.github/workflows/` at repo root (GH mandates this location).

---

## No Analog Found

Files with no close match in the Ifix codebase (planner should use RESEARCH.md patterns + conceptual neighbors noted above):

| File | Role | Why no analog |
|------|------|---------------|
| `pod/Dockerfile` (CUDA multi-stage) | Docker/CUDA | Ifix has no GPU workloads; closest is agents/Dockerfile (Python slim) |
| `pod/onstart.sh` | Shell provisioning | Ifix has no Vast.ai-style onstart hooks; compose-up-after-prep is a new pattern |
| `pod/health-bridge/*.go` (4 files) | Go HTTP service | Zero Go code across all Ifix repos |
| `pod/smoke/smoke.py` | Asyncio benchmark | No load-test code in Ifix; agents/main.py provides asyncio vocabulary only |
| `pod/smoke/report-schema.json` | JSON Schema | Ifix uses Zod, not JSON Schema |
| `pod/templates/qwen3.5-27b-tool-calling.jinja` | LLM chat template | External community artifact |
| `.github/workflows/smoke.yml` | GHA workflow with Vast.ai REST | Ifix uses SSH + Portainer webhook; Vast.ai REST-from-GHA is new |
| `go.mod` (repo root) | Monorepo Go manifest | First Go project in the Ifix org |

For these, the planner should lean on:
- **Research STACK.md** for Go library choices (stdlib `net/http` + `log/slog` in Phase 1)
- **Research PITFALLS.md §12** for Go concurrency hygiene (context propagation, `goleak` tests)
- **Research SUMMARY.md** for OpenAI-compat struct shapes
- **MCP `context7`** if the planner needs Go stdlib documentation (available per env)

---

## Metadata

**Analog search scope:**
- `/home/pedro/projetos/pedro/converseai-v4/` (Dockerfile, docker-compose.yml, .github/workflows/, agents/, apps/api/, apps/worker/, apps/router/, biome.json, .dockerignore)
- `/home/pedro/projetos/pedro/cobrancas-api/` (Dockerfile, docker-compose.yml, docker-compose.swarm.yml, .github/workflows/deploy.yml, src/lib/logger.ts, src/modules/health/index.ts, package.json, .dockerignore)
- `/home/pedro/projetos/pedro/auvo/` (biome.json)

**Files read:** 20+ (every read informed at least one pattern assignment; none re-read)

**Pattern extraction date:** 2026-04-17
