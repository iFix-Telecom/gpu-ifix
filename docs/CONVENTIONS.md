# gpu-ifix Conventions

One-page reference for code style, naming, logging, and tooling in this
repo. Follows Ifix-wide conventions from `/home/pedro/projetos/pedro/CLAUDE.md`
unless noted; Go-specific deviations are called out inline.

## File naming

- **kebab-case for all files:** `build-pod.yml`, `qwen3.5-27b-tool-calling.jinja`,
  `health-bridge/main.go`, `onstart.sh`, `report-schema.json`.
- **Go tests colocated with source** using `*_test.go` — Go mandates the
  suffix, so `types_test.go` sits next to `types.go`. This mirrors Ifix TS
  `.test.ts` colocation.
- Kebab-case applies to directories too: `health-bridge/`, `smoke/`,
  `weights/`.

## Go formatter + linter

- `gofmt -w .` — MUST be clean before commit (`gofmt -l .` returns empty).
- `go vet ./...` — MUST exit 0 before commit.
- `golangci-lint run` — optional in Phase 1, required in Phase 2 once the
  gateway pulls external deps (chi, pgx, go-redis).
- **No Biome.** Biome is the Ifix TS/JS formatter; Go uses the stdlib
  toolchain exclusively.

## Go identifiers

- **camelCase** for unexported functions/variables: `probeUpstream`,
  `defaultClient`.
- **PascalCase** for exported symbols: `ChatCompletionRequest`, `NewProber`.
- **PascalCase for package-level constants** — Go idiom, e.g.
  `const DefaultProbeInterval = 10 * time.Second`. This **diverges from the
  Ifix TS convention of `UPPER_SNAKE_CASE`** (see CLAUDE.md Naming Patterns),
  because Go compilers and linters enforce PascalCase for exported constants
  and the entire stdlib + ecosystem follows it. Document the deviation in
  code comments when first introducing a constant that a TS-reader might
  expect to see in `UPPER_SNAKE_CASE`.
- **Env vars: `UPPER_SNAKE_CASE`** — matches Ifix convention and Unix
  tradition. Read once at startup into a config struct; never re-read at
  runtime.

## Structured logging

- **Go: stdlib `log/slog`.**
  - Production: `slog.New(slog.NewJSONHandler(os.Stdout, nil))` — NDJSON to
    stdout, one record per line.
  - Development: `slog.New(slog.NewTextHandler(os.Stdout, nil))` when
    `ENV=development` is set.
  - Every logger seeded with a `module` attribute in UPPER_SNAKE_CASE to
    match the Ifix TS pattern `createLogger('MODULE_NAME')`:
    ```go
    logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).
        With("module", "HEALTH_BRIDGE")
    ```
  - Propagate `logger` via context or as an explicit parameter; never use a
    package-level global.
- **Python (`pod/smoke/smoke.py`): `structlog`** with JSON renderer — same
  pattern as `converseai-v4/agents/src/main.py`.

## Timezone

- All containers set `TZ=America/Sao_Paulo` in docker-compose env.
- Go reads `time.Local` after the TZ env is set at container start. Do NOT
  hardcode a fixed zone — prefer the env so infra can override for tests.
- Timestamps serialized as RFC3339 (`time.Now().Format(time.RFC3339)`).

## Error handling (Go)

- **Sentinel errors at package level** for known failure modes:
  ```go
  var ErrUpstreamDown = errors.New("upstream not reachable")
  ```
- **Wrap with context** when returning up the stack:
  ```go
  return fmt.Errorf("probing %s: %w", upstream, err)
  ```
- Use `errors.Is` / `errors.As` at the caller for typed checks — never
  `strings.Contains(err.Error(), "...")`.
- No retry at the health-bridge layer in Phase 1 — the probe ticker is the
  retry loop (next tick is the next attempt). Retryable/non-retryable
  classification (5xx vs 4xx) lives in the Phase 2 gateway.

## HTTP response shape

- **Health-bridge endpoints return flat OpenAI-compat-style JSON** per D-12:
  ```json
  { "status": "healthy", "latency_ms": 120, "last_probe": "2026-04-17T10:00:00-03:00" }
  ```
  with optional `"error": "..."` when `status != healthy`.
- **This is NOT the Ifix TS `{ data: T }` wrapper.** Reason: health endpoints
  expose runtime observability, not CRUD entities. The `{ data }` wrapper is
  reserved for CRUD responses in the Phase 2 gateway.
- Return HTTP 503 whenever `status != healthy` so compose + Kubernetes-style
  probes treat the upstream as down automatically.

## Docker image tagging

Pod image: `ghcr.io/ifixtelecom/ifix-ai-pod`. Tags per D-21 / D-23:

| Tag | Promotion | Source |
|---|---|---|
| `{branch}` | automatic on push | `build-pod.yml` |
| `{branch}-{sha}` | automatic on push | `build-pod.yml` |
| `v1.0.0`, `latest` | **manual promotion** | only after smoke-test gates D-19 pass |

Branch = `develop` or `main` (two-branch flow, matches `converseai-v4`).

## Package comments (Go)

Every Go file begins with a package-level doc comment above the `package`
clause for the first file in the package:

```go
// Package openai defines OpenAI-compatible request/response types shared
// between the pod health-bridge (Phase 1) and the Go gateway (Phase 2).
package openai
```

Exported functions, types, and constants get a godoc comment that starts
with the symbol name:

```go
// NewProber returns a probe worker configured to hit upstream on interval.
func NewProber(upstream string, interval time.Duration) *Prober { ... }
```

## Commit message style

Conventional commits, scope = subsystem:

- `feat(pod): ...` — new pod feature
- `fix(health-bridge): ...` — bug fix in the health-bridge binary
- `ci(build-pod): ...` — GitHub Actions change
- `docs(conventions): ...` — docs change
- `chore(repo): ...` — repo scaffolding, dependency bumps

Subject line ≤72 chars, body wraps at 100, imperative mood.
