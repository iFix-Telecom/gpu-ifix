---
phase: 06-emergency-pod-template-refactor
plan: 03
subsystem: gateway/internal/emerg/vast
tags: [vast-types, dto, refactor, phase-6-pr1, strategy-b]
dependency_graph:
  requires: [06-01]
  provides: [06-04]
  affects: []
tech_stack:
  added: []
  patterns: ["Go struct JSON tag omitempty"]
key_files:
  created:
    - gateway/internal/emerg/vast/types_test.go
  modified:
    - gateway/internal/emerg/vast/types.go
decisions:
  - "Added BOTH Args and Entrypoint fields to CreateRequest (plan said Args only; spike + WAVE0-GATES Decision 4 mandated Entrypoint)"
  - "Created types_test.go to pin wire format (package convention: _test.go alongside source, testify/require)"
metrics:
  duration_minutes: 6
  completed_date: "2026-05-17"
  tasks_completed: 1
  files_modified: 1
  files_created: 1
  lines_added: 38
  lines_removed: 8
requirements: [PRV-06]
validates_sc: [SC-3]
---

# Phase 6 Plan 03: Vast CreateRequest DTO Extension Summary

DTO layer extended with `Args []string` and `Entrypoint string` fields to support Strategy B Locked emergency-pod payloads — replaces the Phase 6.5 `Runtype="ssh"` pattern that silently dropped the image CMD.

## What Was Built

### `gateway/internal/emerg/vast/types.go` — CreateRequest struct extended

- New field `Args []string` with JSON tag `args,omitempty` (Pitfall 5: NOT `image_args`, NOT `args_str` — verified via `vast-cli/vast.py:2509`).
- New field `Entrypoint string` with JSON tag `entrypoint,omitempty` — REQUIRED Strategy B override per `06-SPIKE-runtype-args.md` Round 2.
- `Runtype` field comment rewritten from `// "ssh"` (single-value, stale) to 5-line block documenting all 3 valid values:
  - `args` — Strategy B Locked (preserves ENTRYPOINT)
  - `ssh_proxy` — legacy sshd-sidecar injection
  - `ssh` — deprecated alias; Phase 6 root cause (STATE.md:85 bug)
- Package godoc top block extended with a 5th bullet covering Phase 6 Strategy B + entrypoint rationale + spike reference.
- Field ordering preserved (ClientID, Image, Env, Onstart, Runtype, Entrypoint, Args, Disk, Label, TargetState).

### `gateway/internal/emerg/vast/types_test.go` — new file pinning wire format

Three test functions, six sub-tests, all GREEN:

1. `TestCreateRequest_ArgsOmitempty`
   - `populated_emits_args_key` — `json.Marshal({Args: [...]})` contains `"args":["--host","0.0.0.0"]`
   - `zero_value_omits_args_key` — `json.Marshal({})` does NOT contain `"args"`
   - `wrong_keys_never_appear` — `image_args` / `args_str` never serialized
2. `TestCreateRequest_EntrypointOmitempty`
   - `populated_emits_entrypoint_key` — `"entrypoint":"/bin/bash"` serialized
   - `zero_value_omits_entrypoint_key` — empty string suppressed
3. `TestCreateRequest_StrategyB_FullShape` — golden payload pin matching `06-WAVE0-GATES.md` Decision 4 (runtype=args + entrypoint=/bin/bash + args=["-c","exec /app/llama-server ..."]).

## Verification

- `cd gateway && go build ./internal/emerg/vast/...` — GREEN
- `cd gateway && go vet ./internal/emerg/vast/...` — clean
- `cd gateway && go build ./...` — GREEN (no downstream regression)
- `cd gateway && go test ./internal/emerg/vast/...` — PASS (all existing tests + 6 new sub-tests)
- `grep -c 'json:"args,omitempty"' types.go` = 1 ✓
- `grep -c 'json:"entrypoint,omitempty"' types.go` = 1 ✓
- `grep -c 'Runtype values:' types.go` = 1 ✓
- `grep -c 'image_args\|args_str' types.go` = 2 (both inside WARNING comments — plan explicitly allowed "excepto para warning")
- `git diff --quiet HEAD~1 -- gateway/internal/emerg/vast/client.go` → exit 0 ✓ (client.go untouched; `json.Marshal(body)` auto-serializes via JSON tags)

## Deviations from Plan

### Rule 3 — Add `Entrypoint` field (mandated by Wave 0 spike + operator gate)

- **Found during:** Pre-execution context read (06-WAVE0-GATES.md Decision 4 + 06-SPIKE-runtype-args.md Round 2).
- **Issue:** Plan 06-03 as-written only specifies `Args []string`. The spike empirically discovered that `Runtype="args"` alone does NOT shell-wrap the args slice — image ENTRYPOINT `llama-server` would receive raw tokens. Strategy B requires `--entrypoint /bin/bash --args -c "<bash script>"` pattern (Round 2 success vs Round 1 OCI runtime error).
- **Fix:** Added `Entrypoint string` field with `json:"entrypoint,omitempty"` adjacent to Runtype, with explicit godoc citing spike empirical validation.
- **Files modified:** `gateway/internal/emerg/vast/types.go` (1 extra field + 1 extra comment block).
- **Commit:** `d8c322c`
- **Downstream impact for plan 06-04:** `buildCreateRequest` will set `Entrypoint: "/bin/bash"` + `Args: []string{"-c", emergencyOnstart}` (2-element slice, not 15 tokens as CONTEXT.md D-07-B verbatim suggested).

### Test file created (plan said "only if convention exists")

- Plan said: "se ja existe `types_test.go` no diretorio, adicionar 1 funcao". Did not exist.
- Decision: Created it anyway because (a) other tests in the package use `_test.go` files (`client_test.go` with testify/require), (b) the JSON tag wire format is a CORRECTNESS contract worth pinning — a future refactor renaming `Args` to `ImageArgs` would silently break Strategy B, (c) test runtime is sub-millisecond.
- Justification: Convention DOES exist in this package — single test file is consistent.

## Authentication Gates

None.

## Known Stubs

None.

## Deferred Issues

None.

## Commit

- `d8c322c` — `feat(06-03): add Args + Entrypoint fields to vast.CreateRequest`

## Self-Check: PASSED

- File `gateway/internal/emerg/vast/types.go` exists and contains both new fields.
- File `gateway/internal/emerg/vast/types_test.go` exists with 3 test functions.
- Commit `d8c322c` exists in `git log`.
- Plan acceptance criteria all met (build GREEN, grep counts match, client.go untouched).
