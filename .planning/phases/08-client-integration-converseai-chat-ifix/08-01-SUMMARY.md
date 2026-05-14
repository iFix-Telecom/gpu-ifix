---
phase: 08-client-integration-converseai-chat-ifix
plan: 01
subsystem: client-integration
tags: [seed-script, gatewayctl, tenant-provisioning, idempotent, bash]
requires:
  - "gatewayctl CLI (tenant/key/admin-key create) — Phase 2 + Phase 4"
  - "gateway DB schema (tenants, api_keys, admin_keys) — Phase 2"
provides:
  - "scripts/integration-smoke/provision-tenants.sh — idempotent Phase-8 tenant seed script"
  - "scripts/integration-smoke/README.md — operator usage doc for the integration-smoke directory"
affects:
  - "Phase 8 HUMAN-UAT plan (08-04) — consumes provision-tenants.sh in its Prerequisites"
tech-stack:
  added: []
  patterns:
    - "Idempotent CLI-wrapping: tenant-create exit-1 'already exists' treated as success"
    - "Secret-once stdout discipline: raw keys via stdout heredoc, never via log() (stderr)"
    - "Guarded non-idempotent steps behind explicit --mint-keys opt-in flag"
    - "bash structure analog from pod/scripts/vast-ai.sh + upload-weights.sh"
key-files:
  created:
    - "scripts/integration-smoke/provision-tenants.sh"
    - "scripts/integration-smoke/README.md"
  modified: []
decisions:
  - "Env var for the gateway DB DSN is AI_GATEWAY_PG_DSN (confirmed via gateway/internal/config/config.go:173 + gatewayctl tests)"
  - "--gatewayctl accepts a multi-word value (e.g. docker exec wrapper); only the leading executable is command -v checked"
  - "Key minting gated behind --mint-keys (not a sentinel file) — explicit operator opt-in, mirrors plan + threat T-08-02"
metrics:
  duration: "~12 min"
  completed: "2026-05-14"
  tasks: 2
  files: 2
---

# Phase 8 Plan 01: Tenant-Provisioning Seed Script Summary

Idempotent `provision-tenants.sh` that wraps the compiled `gatewayctl` CLI to seed the two Phase-8 client tenants (`converseai`, `chat-ifix`) and, behind an explicit `--mint-keys` opt-in, mint their API keys + the dashboard admin key — with raw keys surfaced to stdout exactly once.

## What Was Built

### Task 1 — `scripts/integration-smoke/provision-tenants.sh` (commit 429f169)

A 0755 bash seed script mirroring the `pod/scripts/vast-ai.sh` structure:

- Header comment block with `# Usage:` + `# Env:` sections and an idempotency/secrets note.
- `set -euo pipefail`, a `log()` helper writing to **stderr**, fail-fast on missing `AI_GATEWAY_PG_DSN` via `: "${VAR:?missing}"`, and a prereq `command -v` loop.
- Arg-parse `while/case` loop: `--gatewayctl PATH` (default `gatewayctl` on PATH; accepts a multi-word `docker exec` wrapper), `--mint-keys`, `--dry-run`, unknown arg → `log` + `exit 2`.
- **Idempotent tenant-create step** (always runs, both tenants): treats `gatewayctl tenant create` exit-0 as "created" and exit-1-with-`already exists`-in-output as "already provisioned — OK"; any other non-zero exit prints the captured output and exits 1.
- **Guarded key-mint step** (only under `--mint-keys`): without the flag the script logs the "re-run with --mint-keys" instruction and exits 0; with it, mints the `converseai` tenant key, the `chat-ifix` tenant key, and the `phase-8-dashboard` admin key. Each raw key is captured into a shell variable (never passed to `log()`).
- **Secret-once surfacing**: a stdout final-instructions heredoc shows the three raw keys labelled for the operator's Portainer stack env vars, with a one-line "shown ONCE / never re-derivable" warning.
- `--dry-run`: every `gatewayctl` invocation prints `[dry-run] would run: ...` and executes nothing.

Seeds exactly two tenants — `converseai` ("ConverseAI v4") and `chat-ifix` ("Chat Ifix") — both `data_class normal` per 08-CONTEXT.md `## Decisions`.

### Task 2 — `scripts/integration-smoke/README.md` (commit 126bcab)

Operator usage doc mirroring the `pod/smoke/README.md` structure: a `**Status:**` line, a `## Files` table (lists `provision-tenants.sh` now + `smoke-converseai.py` / `smoke-chat-ifix.py` / `report-schema.json` / `fixtures/` as added by 08-02/08-03), a `## Provisioning the tenants` section with the exact two-step `AI_GATEWAY_PG_DSN=... --gatewayctl ... --mint-keys` invocation and the idempotency / "mint once" note, a `## Scope` section restating the gpu-ifix-side-only boundary, and a `## See also` cross-link to `docs/RUNBOOK-CLIENT-INTEGRATION.md` + the Phase 8 HUMAN-UAT plan (noted as produced by 08-04).

## Verification

- `provision-tenants.sh` is 0755, passes `bash -n`, and the plan's full verify command-chain passes (`already exists`, `--mint-keys`, `converseai`, `chat-ifix`, `--dry-run` greps + a live `--dry-run` invocation emitting `dry-run`).
- `--dry-run` and `--dry-run --mint-keys` both run end-to-end without a DB, printing the would-run `gatewayctl` commands.
- Missing `AI_GATEWAY_PG_DSN` → exit 1 with `AI_GATEWAY_PG_DSN: missing`. Unknown arg → exit 2.
- `README.md` exists and passes its verify chain (`provision-tenants`, `idempotent`, `scope` greps).

## Deviations from Plan

None - plan executed exactly as written. The plan instructed confirming the gatewayctl DB DSN env var name; it is `AI_GATEWAY_PG_DSN` (confirmed in `gateway/internal/config/config.go:173` and the gatewayctl test files), matching the name already used in the plan's verify command.

## Threat Model Coverage

- **T-08-01 (Information Disclosure)** — mitigated: raw keys captured into shell variables, emitted only via the stdout heredoc, never passed to `log()`.
- **T-08-02 (Elevation of Privilege)** — mitigated: non-idempotent `key create` / `admin-key create` gated behind the explicit `--mint-keys` opt-in; default re-run only runs idempotent `tenant create`.
- **T-08-03 (Tampering)** — mitigated: only the literal `already exists` substring is the success-on-exit-1 signal; any other non-zero exit prints captured output and exits 1; `set -euo pipefail` set.
- **T-08-04 (Repudiation)** — accepted per plan: DB-level `created_at` timestamps + the HUMAN-UAT sign-off cover provenance; no script-side mitigation needed.

## Known Stubs

None.

## Self-Check: PASSED

- FOUND: scripts/integration-smoke/provision-tenants.sh
- FOUND: scripts/integration-smoke/README.md
- FOUND: commit 429f169 (Task 1)
- FOUND: commit 126bcab (Task 2)
