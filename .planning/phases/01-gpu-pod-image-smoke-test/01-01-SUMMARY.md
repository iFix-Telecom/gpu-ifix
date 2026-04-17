---
phase: 01-gpu-pod-image-smoke-test
plan: 01
subsystem: infra
tags: [go, golang, monorepo, openai-compat, scaffolding, docker, gitignore]

# Dependency graph
requires: []
provides:
  - "Go monorepo at module github.com/ifixtelecom/gpu-ifix with single go.mod at root (Go 1.23)"
  - "pkg/openai shared structs: 16 types (ChatCompletion*, Embedding*, Transcription*, ErrorResponse, Tool/ToolCall family) with OpenAI-compat JSON tags"
  - ".gitignore excluding bin/, weights, fixtures, smoke-report*, .env, IDE/OS files"
  - ".dockerignore excluding .planning, weights, fixtures, *.md (except README.md), compose files, Zone.Identifier"
  - "docs/CONVENTIONS.md one-pager: kebab-case filenames, gofmt+go vet (no Biome), log/slog with module attr, TZ=America/Sao_Paulo, ghcr.io/ifixtelecom/ifix-ai-pod tagging"
  - "pod/README.md operator stub listing 5 ports (8000 LLM, 8001 STT, 8002 embed, 9100 health-bridge, 9400 dcgm)"
  - "Compilable Go module (go build ./... + go vet ./... + go test ./... clean)"
affects: [01-02, 01-03, 01-04, 01-05, 01-06, 01-07, 01-08, 01-09, Phase 2 gateway]

# Tech tracking
tech-stack:
  added: [go 1.23, stdlib encoding/json]
  patterns:
    - "Monorepo Go (single go.mod at repo root per D-13)"
    - "Shared contract under pkg/openai/ importable by pod/health-bridge (Phase 1) and gateway/ (Phase 2) via identical import path"
    - "TDD for shared types (RED: failing round-trip tests → GREEN: types.go)"
    - "External test package (openai_test) forces testing through public API"

key-files:
  created:
    - "go.mod (module github.com/ifixtelecom/gpu-ifix, go 1.23, stdlib only)"
    - "pkg/openai/types.go (16 OpenAI-compat structs with json tags)"
    - "pkg/openai/types_test.go (5 round-trip tests)"
    - ".gitignore"
    - ".dockerignore"
    - "README.md (repo root)"
    - "docs/CONVENTIONS.md"
    - "pod/README.md (operator runbook stub)"
  modified: []

key-decisions:
  - "pkg/openai placed at repo root (not pod/health-bridge/types.go) so Phase 2 gateway imports same path without path churn — confirms recommendation in 01-PATTERNS.md"
  - "External test package (package openai_test) chosen over internal tests — forces tests through exported API, catches accidental privacy breaks"
  - "Sentinel ErrorResponse pattern uses dedicated ErrorDetail struct (not inline) so Code field omitempty serialization is easy to assert"
  - "Kept stdlib-only (no go.sum generated): encoding/json is the only import, matches D-13 Phase 1 posture (chi/etc. deferred to Phase 2)"
  - "Documented PascalCase-for-Go-constants as explicit deviation from Ifix TS UPPER_SNAKE_CASE convention (Go ecosystem mandate)"

patterns-established:
  - "Go TDD with external test package: write types_test.go referencing exported symbols, expect compile failure as RED, implement types.go to reach GREEN"
  - ".dockerignore prevents .planning/, weights (D-01), fixtures, and Zone.Identifier files from leaking into GHCR-published images"
  - "Package doc comment block pattern (// Package X ... with blank line before package clause) to be followed by all future Go packages"

requirements-completed: [POD-01]

# Metrics
duration: ~11 min
completed: 2026-04-17
---

# Phase 01 Plan 01: Repo scaffolding + Go monorepo + Ifix conventions Summary

**Bootstrapped gpu-ifix as a Go 1.23 monorepo with 16 shared OpenAI-compat structs under pkg/openai and 5 round-trip tests, plus repo-hygiene scaffolding (.gitignore, .dockerignore, README) and Ifix-conventions one-pager (docs/CONVENTIONS.md) + pod operator stub.**

## Performance

- **Duration:** ~11 min
- **Started:** 2026-04-17T22:57 (worktree spawn)
- **Completed:** 2026-04-17T23:02:39Z
- **Tasks:** 3 (one with TDD sub-cycle)
- **Files modified:** 8 created, 0 modified

## Accomplishments

- Go monorepo manifest at repo root: `module github.com/ifixtelecom/gpu-ifix` on Go 1.23, stdlib-only (no `go.sum` yet)
- `pkg/openai/types.go` exports 16 types covering chat, tool-calling, embeddings, transcription, and error envelopes — locked contract for plan 04 health-bridge and Phase 2 gateway
- 5 round-trip tests (`go test ./... -count=1` green) verifying omitempty semantics on `content`/`code` fields, tool_call arguments remaining a JSON string, float32 embedding preservation
- `.gitignore` + `.dockerignore` threat-modeled per the plan's STRIDE register — excludes weights (D-01), `.planning/`, Zone.Identifier files, smoke reports
- `docs/CONVENTIONS.md` anchors Go-specific style: gofmt+go vet (no Biome), `log/slog` with module attribute matching Ifix `createLogger('MODULE_NAME')` TS convention, `TZ=America/Sao_Paulo`, flat health payload shape (not `{data:T}`), conventional commit scopes
- `pod/README.md` stub enumerates all 5 pod ports and forwards to plan 09 for the full runbook

## Task Commits

Each task committed atomically (all commits via `--no-verify` per worktree parallel executor rules):

1. **Task 1: Go monorepo manifest + gitignore + dockerignore + README** — `765c70a` (chore)
2. **Task 2: pkg/openai shared structs (TDD)**
   - RED: `080ead8` (test) — 5 failing round-trip tests
   - GREEN: `8049587` (feat) — 16 exported types
   - REFACTOR: none needed (gofmt-clean, vet-clean on first pass)
3. **Task 3: docs/CONVENTIONS.md + pod/README.md stub** — `e9ae801` (docs)

**Plan metadata (to be appended):** this SUMMARY.md commit, made after self-check.

## Files Created/Modified

| Path | Role | Notes |
|---|---|---|
| `go.mod` | Go module manifest | module github.com/ifixtelecom/gpu-ifix, go 1.23, no deps |
| `.gitignore` | Repo hygiene | bin/, weights, fixtures, smoke-report*, .env, IDE/OS |
| `.dockerignore` | Image hygiene | .planning, weights, *.md except README.md, compose files, Zone.Identifier |
| `README.md` | Root doc | Monorepo layout table, how to build/test, links |
| `pkg/openai/types.go` | Shared contract | 16 structs, stdlib-only (encoding/json for RawMessage) |
| `pkg/openai/types_test.go` | Shared contract tests | external package (openai_test), 5 functions |
| `docs/CONVENTIONS.md` | Developer doc | Go-specific style + Ifix alignment one-pager |
| `pod/README.md` | Operator doc | Port table, forward-ref to plan 09 runbook |

## Decisions Made

- **pkg/openai at repo root (not pod/health-bridge/types.go):** confirms 01-PATTERNS.md recommendation. Phase 2 gateway imports same path with zero churn.
- **External test package:** `package openai_test` ensures tests exercise the exported API, not internals. Caught early in RED that all required symbols must be exported.
- **Stdlib-only for Phase 1:** No go.sum generated; `go mod tidy` warns "no packages matched all" which is expected for a module containing a single library package. `go build ./...` and `go test ./...` still succeed. Per D-13, chi/pgx/etc. arrive in Phase 2.
- **Documented PascalCase-constants deviation explicitly** in CONVENTIONS.md so TS-native readers aren't surprised that Go exported constants aren't UPPER_SNAKE_CASE.

## Deviations from Plan

None - plan executed exactly as written.

The plan's `<interfaces>` block specified 15 struct definitions (counting the distinct struct names). Implementation exports 16 `type` declarations — the 16th is `ErrorDetail`, which is explicitly referenced by `ErrorResponse.Error` in the plan's spec. The plan's success_criteria line 511 says "exactly the 16 types listed in `<interfaces>`" so this is the intended count.

## Issues Encountered

- **Go toolchain absent from VPS.** Installed Go 1.23.4 locally to `$HOME/.local/go/` from the upstream tarball; not a permanent change (no root write needed, no system package installed). Commands run with `PATH="$HOME/.local/go/bin:$PATH"`. The CI (plan 07 `build-pod.yml`) will use `actions/setup-go@v5` and is independent of local toolchain.
- `go mod tidy` emits `warning: "all" matched no packages` on the empty-package-yet module. Expected — resolves once pkg/openai contains buildable code. Not a real failure (exit 0).

## User Setup Required

None - no external service configuration required for this plan.

## Next Phase Readiness

Plan 01 delivers the contract that unblocks every downstream Wave 1+ plan:

- **Plan 04 (health-bridge Go binary)** can `import "github.com/ifixtelecom/gpu-ifix/pkg/openai"` for all probe request/response types — zero exploration needed.
- **Plan 03 (Dockerfile)** can rely on `.dockerignore` to keep the image under D-01's 2 GB target (weights + .planning + fixtures excluded).
- **Phase 2 gateway (future)** inherits the same import path without breaking changes.

**TDD Gate Compliance:** Plan has `type: auto` at the plan level with one TDD task (task 2). Gate sequence verified in `git log`:
- `test(01-01): add failing round-trip tests` (`080ead8`) — RED
- `feat(01-01): implement pkg/openai shared structs` (`8049587`) — GREEN
- REFACTOR gate not applicable (code gofmt/vet clean on first pass).

## Self-Check

**File existence:**
- go.mod — FOUND
- .gitignore — FOUND
- .dockerignore — FOUND
- README.md — FOUND
- pkg/openai/types.go — FOUND
- pkg/openai/types_test.go — FOUND
- docs/CONVENTIONS.md — FOUND
- pod/README.md — FOUND

**Commit existence:**
- 765c70a (chore scaffolding) — FOUND
- 080ead8 (test RED) — FOUND
- 8049587 (feat GREEN) — FOUND
- e9ae801 (docs) — FOUND

**Verification suite (from plan line 497-507):**
- `go mod tidy` — exit 0 (warning only; acceptable)
- `go vet ./...` — exit 0, empty output
- `go test ./... -count=1` — `ok github.com/ifixtelecom/gpu-ifix/pkg/openai 0.003s`
- `gofmt -l .` — empty output

## Self-Check: PASSED

---
*Phase: 01-gpu-pod-image-smoke-test*
*Completed: 2026-04-17*
