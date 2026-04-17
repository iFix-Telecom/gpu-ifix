# gpu-ifix

**Status:** v1 in development (Phase 1: GPU pod image + smoke-test)

Monorepo for the Ifix AI inference stack.

## Layout

| Path | Purpose | Phase |
|---|---|---|
| `pkg/openai/` | Shared OpenAI-compat request/response structs (health-bridge + gateway) | 1 |
| `pod/` | GPU pod image: Dockerfile, docker-compose, onstart, health-bridge, smoke-test | 1 |
| `gateway/` | Go HTTP gateway (reserved; populated in Phase 2) | 2 |
| `.github/workflows/` | CI: `build-pod.yml` (image publish) + `smoke.yml` (Vast.ai validation) | 1 |
| `.planning/` | GSD planning artifacts (not shipped in any image) | — |

## Monorepo Go

Single `go.mod` at root: `module github.com/ifixtelecom/gpu-ifix`.

Import shared types from anywhere in the repo:
```go
import "github.com/ifixtelecom/gpu-ifix/pkg/openai"
```

## How to build and test locally

```bash
go mod tidy
go vet ./...
go test ./...
```

## See also

- `docs/CONVENTIONS.md` — file naming, logger, timezone, structured logging
- `pod/README.md` — operator runbook (Phase 1, plan 09)
- `.planning/ROADMAP.md` — 10-phase plan
