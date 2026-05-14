---
phase: 09-client-integration-sensitive-tenants-telefonia-cobran-as-campanhas-voice-api
plan: 01
subsystem: integration-smoke / tenant provisioning
tags: [provisioning, gatewayctl, data-class, quotas, lgpd-sensitive]
requires:
  - gatewayctl tenant create / tenant set-quota / key create / admin-key create (Phase 2/4)
  - scripts/integration-smoke/provision-tenants.sh (Phase 8 08-01)
provides:
  - idempotent seed for the 4 Phase-9 mixed-data-class tenants (telefonia, cobrancas, campanhas, voice-api)
  - per-tenant quotas on cobrancas + campanhas via gatewayctl tenant set-quota
  - per-tenant data_class key minting (sensitive for telefonia + cobrancas, normal for campanhas + voice-api)
affects:
  - Phase 9 plan 09-02 (sensitive-failover smoke consumes the minted sensitive-tenant keys)
  - Phase 9 HUMAN-UAT plan (Prerequisites run provision-tenants.sh)
tech-stack:
  added: []
  patterns:
    - gatewayctl-wrapper idempotency discipline (run_gatewayctl + exact-message grep -F)
    - secret-once discipline (raw keys to stdout only, never log())
    - parallel-array tenant model extended with a TENANT_DATA_CLASS array
key-files:
  created: []
  modified:
    - scripts/integration-smoke/provision-tenants.sh
    - scripts/integration-smoke/README.md
decisions:
  - "Quota starting values: cobrancas 2M daily-tokens / 120 rpm; campanhas 5M daily-tokens / 300 rpm — conservative per-tenant ceilings, audio/embed/monthly/rps flags left at the -1 unchanged sentinel"
  - "set-quota runs on every invocation (not gated by --mint-keys) because it is an idempotent UPDATE; any non-zero exit is fatal (it can only mean tenant-create failed)"
  - "tenant create takes no --data-class flag — data_class is carried by the key; the TENANT_DATA_CLASS array is consumed in the key-mint step"
metrics:
  duration: ~15min
  completed: 2026-05-14
  tasks: 2
  files: 2
---

# Phase 9 Plan 01: Phase-9 Tenant Provisioning + Quotas Summary

Extended the Phase-8 idempotent seed script to provision the four Phase-9 client tenants with a per-tenant `data_class` (telefonia + cobrancas = sensitive, campanhas + voice-api = normal) and to apply per-tenant quotas to cobrancas + campanhas via `gatewayctl tenant set-quota` — no new Go code, just a bash extension wrapping existing `gatewayctl` subcommands.

## What Changed

### Task 1 — `scripts/integration-smoke/provision-tenants.sh` (commit 25ad726)

- Rewrote the header docstring for the Phase-9 4-tenant mixed-data-class model and the new quota step; noted `set-quota` is an idempotent UPDATE that always runs.
- Removed the scalar `DATA_CLASS="normal"`. Replaced the 2-element `TENANT_SLUGS`/`TENANT_NAMES` with the 4 Phase-9 tenants and added a parallel `TENANT_DATA_CLASS=("sensitive" "sensitive" "normal" "normal")` array.
- Tenant-create loop unchanged in structure — keeps the exit-1 + `grep -qF "tenant slug '$slug' already exists"` idempotency branch — now iterates the 4 slugs; dropped the removed `${DATA_CLASS}` reference from the leading `log` line.
- **NEW quota step** after tenant-create, **before** the `--mint-keys` gate: `tenant set-quota` for `cobrancas` + `campanhas` driven by parallel `QUOTA_TENANTS`/`QUOTA_DAILY_TOKENS`/`QUOTA_RPM` arrays, passing `--daily-tokens` + `--rpm`. Any non-zero exit is fatal (threat T-09-02).
- Key-mint step: added a `mint_tenant_key` helper that mints one tenant key with a given per-tenant `data_class` and echoes the raw key (never to `log()`); mints all 4 tenant keys via it, plus `admin-key create --label "phase-9-sensitive"`. Preserves the `--mint-keys` gate, `parse_key` (exactly-one-`^key=` assertion), `parse_id`.
- Secret-once heredoc extended to surface 4 tenant keys + 1 admin key, each labelled with its target client repo.

### Task 2 — `scripts/integration-smoke/README.md` (commit ee54b46)

- Status line + Files table now cover Phase 8 **and** Phase 9; added `smoke-sensitive-failover.py` + `sensitive-failover-report-schema.json` rows (added by plan 09-02).
- Provisioning section documents the 4-tenant mixed-data-class model (with a per-tenant table), the per-tenant quotas on cobrancas + campanhas, the exact 2-step invocation, and the always-idempotent `set-quota` note.
- Scope section restates the gpu-ifix-side-only boundary naming the 4 client sibling repos; cross-links `RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md`, `LGPD-SUBPROCESSORS.md`, `LGPD-REVIEW-CHECKLIST.md`, and the Phase-9 HUMAN-UAT plan as "see also".

## Verification

- `bash -n` passes; script is executable (0755); `set -euo pipefail` + `log()` + `run_gatewayctl` + arg-parse loop + `AI_GATEWAY_PG_DSN` fail-fast all preserved.
- `--dry-run` prints the 4 `tenant create` lines + the 2 `set-quota` lines and the "re-run with --mint-keys" message, touching no DB — confirmed by running it.
- README verify: contains `provision-tenants`, `idempotent`, `scope`, `data_class`, `smoke-sensitive-failover`, `telefonia` — all present.

## Deviations from Plan

None — plan executed exactly as written.

## Threat Model Coverage

- **T-09-01 (Information Disclosure)** — raw keys captured into shell vars (`mint_tenant_key` echoes to stdout via command substitution), emitted only via the final heredoc, never via `log()`. `parse_key` asserts exactly one `^key=` line.
- **T-09-02 (Tampering — half-provisioned tenant)** — the quota step treats any non-zero `set-quota` exit as fatal `exit 1` with an explanatory log line; `set -euo pipefail` set.
- **T-09-03 (EoP — wrong data_class)** — `mint_tenant_key` is called with the explicit per-tenant `data_class` for each of the 4 tenants (`sensitive` for telefonia + cobrancas, `normal` for campanhas + voice-api).
- **T-09-04 (EoP — duplicate keys)** — `key create` / `admin-key create` stay behind the `--mint-keys` opt-in; the default re-run path runs only idempotent `tenant create` + `tenant set-quota`.

No new threat surface introduced beyond the plan's `<threat_model>`.

## Self-Check: PASSED

- `scripts/integration-smoke/provision-tenants.sh` — FOUND
- `scripts/integration-smoke/README.md` — FOUND
- commit 25ad726 — FOUND
- commit ee54b46 — FOUND
