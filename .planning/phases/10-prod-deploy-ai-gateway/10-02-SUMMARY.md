---
phase: 10-prod-deploy-ai-gateway
plan: 02
subsystem: data-layer-bootstrap
tags: [phase-10, prod-deploy, postgres, migrations, dashboard, better-auth]
requires:
  - .planning/phases/10-prod-deploy-ai-gateway/10-01-SUMMARY.md
provides:
  - "scripts/deploy/bootstrap-postgres.sh — idempotent CREATE DATABASE wrapper for bd_ai_gateway_prod + bd_ai_dashboard_prod (Pitfall 2 implementable form of D-05/D-06)"
  - "scripts/deploy/migrate-dashboard.sh — one-shot @better-auth/cli migrate wrapper that creates the 4 dashboard_auth tables inside bd_ai_dashboard_prod"
affects:
  - .planning/phases/10-prod-deploy-ai-gateway/10-06-PLAN.md (HUMAN-UAT Step 2 + Step 5 — these scripts are the operator handles for both)
tech-stack:
  added: []     # zero new packages; both scripts are bash-only wrappers around psql + docker run
  patterns: ["idempotent SELECT-then-CREATE", "pinned image tag for CLI tool version invariance"]
key-files:
  created:
    - scripts/deploy/bootstrap-postgres.sh
    - scripts/deploy/migrate-dashboard.sh
  modified: []
decisions:
  - "Bootstrap uses pg_database probe BEFORE CREATE DATABASE (idempotent path)."
  - "Bootstrap never echoes DO_ADMIN_DSN; final summary uses <PASS> placeholder so the script's stdout is safe to copy/paste/redirect."
  - "Dashboard migration runs INSIDE the pinned image (D-13 v1.0.0) — @better-auth/cli version travels with the dashboard binary."
  - "Dashboard migration joins the `intra` overlay so DO egress traverses 162.55.92.154 (already in DO Trusted Sources per CLAUDE.md)."
  - "Sanity probe uses postgres:16-alpine ephemeral container because the dashboard image does not ship psql."
metrics:
  duration_seconds: 217
  duration_human: "~3 minutes"
  completed: 2026-05-26T08:40:09Z
  tasks_completed: 2
  tasks_total: 2
  files_created: 2
  files_modified: 0
  commits: 2
---

# Phase 10 Plan 02: Postgres + Dashboard Bootstrap Scripts Summary

Idempotent bash wrappers that the operator runs during Plan 10-06 HUMAN-UAT to (a) create the two NEW prod Postgres databases on the existing DO managed instance and (b) bring the dashboard's Better Auth schema up to current head — without handwriting SQL or remembering Better-Auth CLI invocation syntax mid-UAT.

## What changed

Two new scripts under `scripts/deploy/`, both `chmod +x`, both passing `bash -n`. Together they cover RESEARCH §How To #3 Steps 1+2 (database creation) and Step 7 (dashboard schema bootstrap). The gateway's own 27 goose migrations are NOT in scope here — they run inside the gateway container itself when `AI_GATEWAY_MIGRATE_ON_BOOT=true` is set on first compose-up, per `gateway/internal/db/migrate.go`.

### `scripts/deploy/bootstrap-postgres.sh` (commit `73ad322`)

- Validates `DO_ADMIN_DSN` env var + `psql` binary at startup; fail-fast with a SPECIFIC pointer (DO_ADMIN_DSN is easy to confuse with `AI_GATEWAY_PG_DSN` or `DASHBOARD_DATABASE_URL`, so the error message disambiguates).
- For each of `bd_ai_gateway_prod` + `bd_ai_dashboard_prod`:
  1. Probes `pg_database` for an existing row with that name (`SELECT 1 FROM pg_database WHERE datname='…'`).
  2. If absent → `CREATE DATABASE "…"`. If present → skip + log `"already exists — skipping"`.
  3. Post-CREATE sanity probe: reconnect using a DSN rewritten via pure-bash parameter expansion (the `://USER:PASS@HOST:PORT/DB?query` → swap `DB`) and assert `current_database()` matches.
- Final stdout block prints the two DSN templates the operator must paste into `/opt/ai-gateway-prod/.env`. The password is printed as the literal placeholder `<PASS>` — the script NEVER echoes the actual password from `DO_ADMIN_DSN`. This closes the T-10-02-01 information-disclosure threat: even if stdout is redirected to a logfile, no secrets land in the log.
- No destructive SQL anywhere; verification grep on the regex `\bDROP\b|\bTRUNCATE\b` returns empty.

**Why a new DATABASE and not a new schema** (Pitfall 2 — the most important pitfall in this plan): the gateway hardcodes the schema name `ai_gateway` in 27 migration files, in `gateway/internal/db/pool.go:35` (`SET search_path = ai_gateway, public`), and in every sqlc-generated query (`ai_gateway.api_key_status`, `ai_gateway.data_class`, …). Renaming the schema to `ai_gateway_prod` (D-05 literal) is a Phase 11+ refactor that would touch ≥30 files plus regenerate sqlc. The implementable form of D-05 is a NEW DATABASE per environment with the SAME hardcoded schema name inside it. This script formalises that implementable form.

### `scripts/deploy/migrate-dashboard.sh` (commit `efcd27c`)

- Validates `DASHBOARD_DATABASE_URL` env var; helper docs include the `set -a; source /opt/ai-gateway-prod/.env; set +a` recipe so the operator can populate it directly from the prod env file.
- Defaults `DASHBOARD_IMAGE_TAG=v1.0.0` (D-13 immutable first release); the CLI thus runs at the exact `@better-auth/cli` version baked into the dashboard image at build time. Closes T-10-02-05 (CLI version drift between operator runs).
- Cheap SSH probe (`-o BatchMode=yes`) confirms `n8n-ia-vm` is reachable before launching the docker container, so a stale ~/.ssh/config surfaces immediately.
- Runs `docker run --rm --network intra -e DASHBOARD_DATABASE_URL=… ghcr.io/ifixtelecom/ifix-ai-dashboard:v1.0.0 npx @better-auth/cli migrate --yes`. The `--network intra` flag puts the ephemeral container on the same overlay as the prod stack, so DO Postgres egress traverses the 162.55.92.154 NAT IP already in DO Trusted Sources.
- Post-migrate sanity probe spawns a one-shot `postgres:16-alpine` container on the same overlay and runs `SELECT count(*) FROM information_schema.tables WHERE table_schema='dashboard_auth' AND table_name IN ('user','session','account','verification')` — asserts the result is exactly `4`. The probe uses `postgres:16-alpine` because the dashboard image itself does not ship `psql`.
- On migration failure, the script prints 4 ranked root-cause hints (wrong DSN, image tag not yet published, DO Trusted Sources gap, database not yet bootstrapped) so the operator does not have to recall the order during HUMAN-UAT.
- No destructive SQL; verification grep on the regex `\bDROP\b|\bTRUNCATE\b` returns empty.

## Verification

```bash
# Both scripts pass static parse:
bash -n scripts/deploy/bootstrap-postgres.sh && bash -n scripts/deploy/migrate-dashboard.sh

# Both scripts are executable:
test -x scripts/deploy/bootstrap-postgres.sh && test -x scripts/deploy/migrate-dashboard.sh

# No destructive SQL anywhere:
! grep -qE '\bDROP\b|\bTRUNCATE\b' scripts/deploy/bootstrap-postgres.sh
! grep -qE '\bDROP\b|\bTRUNCATE\b' scripts/deploy/migrate-dashboard.sh

# Bootstrap uses the idempotent pg_database probe:
grep -q "SELECT 1 FROM pg_database WHERE datname" scripts/deploy/bootstrap-postgres.sh

# Dashboard script invokes the right CLI + targets the right schema:
grep -q "better-auth/cli migrate" scripts/deploy/migrate-dashboard.sh
grep -q "dashboard_auth" scripts/deploy/migrate-dashboard.sh
```

All assertions PASS as committed.

Live SSH execution against DO Postgres + n8n-ia-vm happens during Plan 10-06 HUMAN-UAT Step 2 + Step 5 (operator holds `DO_ADMIN_DSN`; autonomous executor does not).

## Decisions made during execution

1. **DSN rewriting is pure-bash parameter expansion.** The bootstrap script needs to reconnect to each newly-created DB for its sanity probe. Rather than re-invoking the operator with two different DSNs, the script rewrites the `/DBNAME` segment of `DO_ADMIN_DSN` via bash `${var%/*}` + `${var%%\?*}` / `${var#*\?}`. No `eval`, no `sed`, no command substitution that could leak the DSN into `ps`-visible argv or stderr.
2. **Sanity-probe psql container = `postgres:16-alpine`, not the dashboard image.** The dashboard image does not ship `psql`. Spinning a 12 MB postgres-client image gives us a clean probe path that does not depend on the operator having `psql` on the n8n-ia-vm host.
3. **`--yes` flag for Better Auth CLI.** Default invocation is interactive ("apply N changes?"); the autonomous-style script needs non-interactive runs from a single shell command, so `--yes` accepts the diff plan without a prompt. Per Better Auth 1.4 CLI semantics, the underlying operations remain idempotent — re-running on an already-migrated DB is a no-op.
4. **`-o BatchMode=yes` SSH probe.** Confirms `n8n-ia-vm` is reachable before the long-running `docker run`, but never prompts the operator interactively. A typo'd alias or a key-authorization regression fails the probe in ~5 s instead of hanging during the actual docker run.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 — Blocking issue] Self-documenting comments tripped the plan's `DROP|TRUNCATE` verification grep**

- **Found during:** Task 1 + Task 2 verification step
- **Issue:** Both scripts had a documentation comment phrased as "No DROP, no TRUNCATE" (Task 1) and "DROP / TRUNCATE to assert" (Task 2). The plan's verify regex `! grep -qE '\bDROP\b|\bTRUNCATE\b'` is intentionally strict (catches destructive SQL in any context — code OR comments). The literal words `DROP` and `TRUNCATE` inside the SECURITY documentation block tripped the regex even though the scripts contain no destructive operations.
- **Fix:** Rephrased both comments to describe the absence of destructive operations without using the literal keywords (e.g. `"The script never issues a deletion or truncation statement"` / `"any deletion / truncation keyword"`). Substantive meaning is unchanged; verifier regex now passes.
- **Files modified:** `scripts/deploy/bootstrap-postgres.sh` (comment block at top), `scripts/deploy/migrate-dashboard.sh` (Threat model bullet)
- **Commits:** Edits were folded into the same Task-1 and Task-2 commits (no separate fix commit — the issue was caught BEFORE the commit landed).

No other deviations. No architectural changes. No auth gates. Plan executed as written modulo the strict-verifier rephrase above.

## Threat model coverage

| Threat ID | Disposition | How this plan handles it |
|-----------|-------------|--------------------------|
| T-10-02-01 | mitigate | Bootstrap script's `log()` writes to stderr only; the final summary on stdout uses `<PASS>` placeholder (never echoes `DO_ADMIN_DSN`). Operator hint to `unset HISTFILE` is in the fail-fast error message. |
| T-10-02-02 | mitigate | Verify regex `\bDROP\b\|\bTRUNCATE\b` catches accidental destructive SQL in either script. Idempotent path = SELECT-then-CREATE only. |
| T-10-02-03 | mitigate | Both DB names are constants (`bd_ai_gateway_prod`, `bd_ai_dashboard_prod`); operator cannot pass a "which DB to create" parameter, eliminating injection. Pre-CREATE `pg_database` probe + post-CREATE `current_database()` probe defend against schema collision with dev. |
| T-10-02-04 | accept | Out of scope; Plan 10-06 Gate B re-verifies egress IP. Bootstrap script does not attempt to whitelist itself. |
| T-10-02-05 | mitigate | `DASHBOARD_IMAGE_TAG=v1.0.0` default pins the CLI to the immutable D-13 release. Override exists for patch releases but is explicit. |
| T-10-02-SC | accept | Zero new packages installed. `@better-auth/cli` ships inside the dashboard image; `postgres:16-alpine` is a stock public image. |

## Self-Check: PASSED

Verified via the script body, file system, and git log:

- `scripts/deploy/bootstrap-postgres.sh` — present, executable, parses, contains `SELECT 1 FROM pg_database`, no destructive keywords. Committed at `73ad322`.
- `scripts/deploy/migrate-dashboard.sh` — present, executable, parses, invokes `better-auth/cli migrate`, asserts `dashboard_auth` schema, no destructive keywords. Committed at `efcd27c`.
- Both commits in `git log --oneline -5`:
  - `efcd27c feat(10-02): add scripts/deploy/migrate-dashboard.sh`
  - `73ad322 feat(10-02): add scripts/deploy/bootstrap-postgres.sh`

No missing artifacts.
