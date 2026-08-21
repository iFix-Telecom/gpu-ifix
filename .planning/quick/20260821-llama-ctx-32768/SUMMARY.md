---
phase: quick-20260821-llama-ctx-32768
plan: 01
status: complete
completed: 2026-08-21T15:29Z
duration: ~4min
requirements: [OPERACOES-26306]
subsystem: pod/llama-server + gateway pre-dispatch cap
key-files:
  modified:
    - gateway/internal/primary/onstart.go
    - gateway/internal/emerg/lifecycle.go
    - gateway/internal/proxy/tokencount.go
    - gateway/internal/proxy/errors.go
    - pod/primary/supervisord.conf
    - pod/docker-compose.yml
    - pod/README.md
    - gateway/docs/RUNBOOK-PRIMARY-POD.md
    - gateway/docs/RUNBOOK-EMERGENCY-POD.md
commits:
  - "18e2910: fix(pod): raise llama --ctx-size 16384->32768 for 16k per-slot context (OPERACOES-26306)"
---

# Quick Task: llama --ctx-size 16384 -> 32768 Summary

**One-liner:** Raised llama-server total --ctx-size to 32768 in all 4 command sources (primary supervisord.conf, primaryLlamaArgsDefault, emergencyLlamaArgsDefault, legacy pod compose) so per-slot context (-np 2) becomes 16384 — matching the existing gateway ChatContextCap and fixing 400 exceed_context_size_error on 38-min-call analyses.

## What Changed

### Flag value 16384 -> 32768
- gateway/internal/primary/onstart.go — primaryLlamaArgsDefault
- gateway/internal/emerg/lifecycle.go — emergencyLlamaArgsDefault
- pod/primary/supervisord.conf:44 — actual primary-pod runtime command (mirrored with onstart.go)
- pod/docker-compose.yml — llama service command + "Flags locked" comment (with per-slot note)
- Docs: gateway/docs/RUNBOOK-PRIMARY-POD.md, gateway/docs/RUNBOOK-EMERGENCY-POD.md, pod/README.md troubleshooting row

### Comment-only (value stays 16384)
- gateway/internal/proxy/tokencount.go — ChatContextCap doc comment rewritten: cap now equals PER-SLOT context (32768 total / -np 2 = 16384/slot); documents the OPERACOES-26306 miscalibration (cap vs TOTAL ctx let an 11184-token request 400 at the slot). RES-07 / "Enforcement do 16k cap" reference kept.
- gateway/internal/proxy/errors.go — ErrContextLengthExceeded comment now says "chat per-slot cap, see ChatContextCap in tokencount.go".

### Non-targets confirmed untouched
- gateway/internal/proxy/audio_duration_test.go (16384 = mp3 fixture byte count)
- ChatContextCap consumers (dispatcher.go, main.go, tests) — constant value unchanged
- .planning/** historical docs

## Validation Results

- go build ./... — OK
- go test ./internal/primary/... ./internal/emerg/... ./internal/proxy/... — all ok (primary 22.9s, emerg 5.4s, emerg/vast 0.08s, proxy 13.7s)
- gofmt -l . — empty (clean)
- Grep gates: exactly 2 x '"--ctx-size", "32768"' in Go code; exactly 2 x 'ctx-size 32768' in pod artifacts; zero 'ctx-size 16384' remaining outside .planning/ history
- Integration tests (-tags integration): SKIPPED — docker unavailable on this host (testcontainers cannot run). Per memory gateway-integration-tests-not-in-executor-check, CI runs them; unit suite is the local baseline.

## Deviations from Plan

None - plan executed exactly as written.

## Out of Scope (deliberately NOT done)

- Pod image rebuild / reprovision (running pods still serve 16384 total until next provision)
- n8n workflow changes
- Push to remote (commit-only per instruction)

## Self-Check: PASSED

- All 9 modified files exist and contain expected values (grep gates above)
- Commit 18e2910 present on develop referencing OPERACOES-26306
