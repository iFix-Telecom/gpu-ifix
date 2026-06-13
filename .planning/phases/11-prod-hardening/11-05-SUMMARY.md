---
phase: 11-prod-hardening
plan: 11-05
subsystem: infra
tags: [runbook, deploy, better-auth, bash, gha, key-rotation, preflight]

# Dependency graph
requires:
  - phase: 10-prod-deploy-ai-gateway
    provides: bootstrap-postgres.sh + preflight.sh + RUNBOOK-DEPLOY.md skeleton + .github/workflows/build-gateway.yml workflow_dispatch.inputs.tag wiring
provides:
  - RUNBOOK-DEPLOY.md "GHA retrigger procedure (D-18.4)" section with gh workflow run recipe + delete-and-recreate-tag fallback
  - RUNBOOK-DEPLOY.md "Per-env key rotation (D-19)" 8-step operator procedure + sanitized diff verification (Recipe A SHA-256 hash, Recipe B first-4-char prefix awk projection) + RUNBOOK-2FA-RECOVERY.md cross-ref + per-env key matrix
  - scripts/dashboard/seed-admins.sh — HTTP-only single-path admin provisioning honoring reviews MEDIUM #1 (no stdout password — sink to chmod 600 file) + #2 (single path — never mixes SQL probe + HTTP signup) + LOW #4 (2FA recovery cross-ref)
  - scripts/deploy/preflight.sh Section 4b — dashboard /api/auth/ok health probe (Gemini suggestion) with fail-fast exit code 5
affects: [11-09, 11-10]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Pattern E (bash skeleton) extended for HTTP-only single-path provisioning with chmod-600-creds-sink"
    - "Sanitized key diff via openssl sha256 hash pipe OR first-4-char awk projection (never raw key material in terminal)"
    - "RUNBOOK 2-step heading shape: H2 title + step-numbered procedure + cross-ref bullet"

key-files:
  created:
    - "scripts/dashboard/seed-admins.sh"
    - ".planning/phases/11-prod-hardening/11-05-SUMMARY.md"
  modified:
    - "gateway/docs/RUNBOOK-DEPLOY.md"
    - "scripts/deploy/preflight.sh"

key-decisions:
  - "Better Auth /api/auth/ok chosen as dashboard health endpoint (verified against better-auth ~1.4.18 ok.mjs source) — HIDE_METADATA built-in, returns {ok:true} HTTP 200; fallback list documented for future major-version drops"
  - "seed-admins.sh provisioning-path detection ordered admin/create-user → admin/invite → forget-password → sign-up/email; current dashboard config (no admin plugin) resolves to sign-up/email — script auto-selects, never mixes"
  - "Sanitized key diff offers TWO recipes (operator picks): SHA-256 hash diff (fully opaque) OR first-4-char awk projection (granular per-var); plan explicitly forbids any literal multi-char-tail key substring in terminal output"
  - "Generated passwords sink EXCLUSIVELY to /tmp/admin-creds-{ts}.txt (chmod 600 BEFORE first write); script summary block prints only the file path"
  - "Domain allowlist (@ifixtelecom.com.br) hard-fails BEFORE any HTTP call (defense-in-depth with auth.ts databaseHooks delivered in 11-02)"

patterns-established:
  - "Pattern: HTTP-only single-path provisioning script — probe one health endpoint, decide once at script-scope, never mix two write paths in the same run (mitigates T-11-OPS-10 race + schema drift + two-path authz)"
  - "Pattern: sanitized key diff in operator runbooks — provide openssl sha256 hash pipe OR awk first-4-char projection; explicit hard rule that any literal multi-char-tail of ifix_/sk-or-/sk- in output is a procedure failure"
  - "Pattern: preflight section 4b dashboard /api/auth/ok probe — Better Auth built-in HIDE_METADATA endpoint; fail-fast non-200 with new dedicated exit code"

requirements-completed: [PRD-04]

# Metrics
duration: ~75min
completed: 2026-05-27
---

# Phase 11 Plan 11-05: Per-Env Keys + Deploy Runbook Summary

**Operator-managed per-env upstream key separation (D-19) + GHA dedup retrigger procedure (D-18.4) shipped via RUNBOOK-DEPLOY.md sections; D-13 admin provisioning script delivered HTTP-only with chmod-600 creds sink (zero stdout password leak) and single-path detection (zero SQL probe); preflight extended with Better Auth /api/auth/ok dashboard probe.**

## Performance

- **Duration:** ~75 min (15:17 → 18:26 UTC wall-clock; ~75 min execution time)
- **Started:** 2026-05-27T18:11:00Z (worktree HEAD assertion)
- **Completed:** 2026-05-27T18:26:00Z
- **Tasks:** 3
- **Files modified:** 3 (2 modified, 1 created)

## Accomplishments

- **RUNBOOK-DEPLOY.md** extended with 148 lines covering two new H2 sections: "GHA retrigger procedure (D-18.4)" and "Per-env key rotation (D-19)". Per-env section is step-numbered (8 steps), includes BOTH sanitized diff recipes (openssl sha256 hash diff AND first-4-char awk projection), per-env key matrix (4 rows), 2FA recovery cross-ref to Plan 11-09 RUNBOOK-2FA-RECOVERY.md, and Pitfall 5 session-cleanup advisory.
- **scripts/dashboard/seed-admins.sh** (424 lines, 18.3 KB) created executable + idempotent. HTTP-only single-path provisioning — probes /api/auth/ok health BEFORE the per-email loop, then commits to ONE of four provisioning paths (admin/create-user, admin/invite, forget-password, sign-up/email) per run. Zero psql / pg_dump invocations. Generated passwords write EXCLUSIVELY to /tmp/admin-creds-{ts}.txt with mode 600 set BEFORE first byte of secret material. Domain allowlist (@ifixtelecom.com.br) hard-fails before any HTTP call.
- **scripts/deploy/preflight.sh** extended with Section 4b "Dashboard health probe" (46 added lines). Probes `$DASHBOARD_HEALTH_URL` (default https://ai-dashboard.converse-ai.app/api/auth/ok) with curl -fs + max-time 15. Non-200 emits FATAL log + exit code 5. Endpoint choice (Better Auth built-in /ok) is justified in an inline comment with verification date.

## Task Commits

Each task was committed atomically:

1. **Task 1: Extend RUNBOOK-DEPLOY.md with GHA retrigger + per-env key sections** — `c93bbe4` (docs)
2. **Task 2: scripts/dashboard/seed-admins.sh — single-path provisioning + 0600 creds file** — `3cfd484` (feat)
3. **Task 3: Extend preflight.sh with dashboard /api/auth/ok probe** — `0e315cb` (feat)

## Files Created/Modified

- `gateway/docs/RUNBOOK-DEPLOY.md` — +148 lines, +2 H2 sections inserted between Cut-Release Procedure and Verification: "GHA retrigger procedure (D-18.4)" + "Per-env key rotation (D-19)". Pre-edit sections preserved verbatim.
- `scripts/dashboard/seed-admins.sh` — NEW, 424 lines, 18329 bytes, mode 0755. HTTP-only single-path admin provisioning. Pattern E bash skeleton extended with chmod-600 creds sink + script-scope BETTER_AUTH_PATH detection.
- `scripts/deploy/preflight.sh` — +46 lines (new Section 4b dashboard probe + header docblock updates for Sections list + exit code 5).

## Decisions Made

- **Better Auth /api/auth/ok chosen as dashboard health endpoint.** Verified 2026-05-27 against better-auth ~1.4.18 source at /tmp/dash-call-center-phase2/node_modules/better-auth/dist/api/routes/ok.mjs:4-25 (HIDE_METADATA flagged, returns `{"ok": true}` with HTTP 200). Fallback list documented in inline comment for future major-version drops: `/api/auth/get-session`, `/health`, root `/`.
- **seed-admins.sh path detection order: admin/create-user → admin/invite → forget-password → sign-up/email.** First candidate that does NOT return 404 wins. Current dashboard config (auth.ts has only `emailAndPassword: { enabled: true }`, no admin or invite plugin) resolves to sign-up/email. If a future plan installs the admin plugin, the script auto-upgrades to the more secure admin/create-user path WITHOUT code changes.
- **Two sanitized diff recipes (operator picks one).** Recipe A (`openssl sha256` hash diff) is fully opaque; Recipe B (first-4-char awk projection) is granular per-var. Plan explicitly forbids literal multi-char tails like `=ifix_xxxxxxx` in terminal output — operator must scrub shell history and re-run with the sanitized recipe if such output appears.
- **Creds file sink instead of stdout/email-link delivery.** Even when the FALLBACK signup path is used (no admin/invite plugin), generated passwords are written to a mode-600 file the operator transfers to their password manager via a secondary channel. The script's summary block prints ONLY the file path, never the password value.
- **Domain allowlist as defense-in-depth.** Hard-fails before any HTTP call. The dashboard's auth.ts databaseHooks (Plan 11-02) provide server-side enforcement; this script's allowlist catches operator typos client-side.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Hard-rule example in RUNBOOK-DEPLOY.md tripped the no-raw-prefix gate**
- **Found during:** Task 1 (RUNBOOK-DEPLOY.md edit)
- **Issue:** The example literal `=ifix_xxxxxxx, =sk-or-xxxxxxx, =sk-xxxxxxx` in the "Hard rule" admonition matched the acceptance-criteria regex `=(ifix_|sk-or-|sk-)[a-zA-Z0-9]{6,}`, failing the gate that requires zero such substrings in non-comment lines.
- **Fix:** Reworded the hard rule to describe the failure mode without using literal multi-char alphanumeric tails after the prefix. Replaced `=ifix_xxxxxxx` with `=ifix_****` (mask form) and added explicit guidance that only mask-suffix forms are acceptable.
- **Files modified:** gateway/docs/RUNBOOK-DEPLOY.md (1 paragraph)
- **Verification:** `grep -vE '^#' gateway/docs/RUNBOOK-DEPLOY.md | grep -cE '=(ifix_|sk-or-|sk-)[a-zA-Z0-9]{6,}'` returns 0.
- **Committed in:** `c93bbe4` (Task 1 commit, after in-task fix)

**2. [Rule 1 - Bug] Docblock comments tripped seed-admins.sh hard gates**
- **Found during:** Task 2 (seed-admins.sh creation)
- **Issue:** Two comment lines in the script's header docstring contained the literal substrings `psql` (in a warning against using SQL probing) and `echoes a password` (in a privacy invariant statement). The acceptance criteria use raw `grep -c` without `-v '^#'`, so comments count toward the gate. Both `grep -c 'echo.*[Pp]assword' == 0` and `grep -cE '\bpsql\b|\bpg_dump\b' == 0` failed.
- **Fix:** Rephrased the two offending comments to convey the same invariants without the trigger substrings: `psql` → "direct SQL client" / "any SQL client directly (no postgres-client binary invocation)"; `echoes a password` → "writes a secret value".
- **Files modified:** scripts/dashboard/seed-admins.sh (2 comment paragraphs)
- **Verification:** `grep -c 'echo.*[Pp]assword' scripts/dashboard/seed-admins.sh` returns 0; `grep -cE '\bpsql\b|\bpg_dump\b' scripts/dashboard/seed-admins.sh` returns 0.
- **Committed in:** `3cfd484` (Task 2 commit, after in-task fix)

---

**Total deviations:** 2 auto-fixed (Rule 1 bug × 2 — both were doc-text adjustments to satisfy literal grep gates without changing semantics)
**Impact on plan:** Both auto-fixes preserve the original intent and improve clarity (the new wording is more precise about what the invariants forbid). No scope creep. All Task acceptance criteria + Plan-level verification gates pass.

## Self-Check

Verification gate results (run after final commit `0e315cb`):

- **(a)** `^## .*(GHA retrigger|Per-env key rotation)` in RUNBOOK-DEPLOY.md: **2** (≥ 2 required) — PASS
- **(b)** Zero raw `=ifix_|=sk-or-|=sk-` substrings in non-comment RUNBOOK lines: **0** (must be 0) — PASS
- **(c)** `RUNBOOK-2FA-RECOVERY.md` cross-ref: **1** in RUNBOOK-DEPLOY.md, **3** in seed-admins.sh — PASS
- **(d)** Zero `echo .*[Pp]assword` in seed-admins.sh: **0** (must be 0) — PASS
- **(e)** Zero `psql|pg_dump` in seed-admins.sh: **0** (must be 0) — PASS
- **(f)** `DASHBOARD_HEALTH_URL` in preflight.sh: **11** (≥ 2 required) — PASS
- **Sanitized diff recipe present** (`openssl sha256` OR `XXXX****` OR first-4 substr): PASS
- **bash -n syntax check:** seed-admins.sh OK, preflight.sh OK
- **FATAL on missing env (seed-admins.sh):** verified — `DASHBOARD_BASE_URL="" bash scripts/dashboard/seed-admins.sh` emits `FATAL.*DASHBOARD_BASE_URL` to stderr and exits 1
- **All commits exist:**
  - `c93bbe4 docs(11-05): add GHA retrigger + per-env key rotation sections to RUNBOOK-DEPLOY` — verified via `git log`
  - `3cfd484 feat(11-05): add scripts/dashboard/seed-admins.sh — HTTP-only single-path admin provisioning` — verified via `git log`
  - `0e315cb feat(11-05): extend preflight.sh with dashboard /api/auth/ok health probe` — verified via `git log`

**## Self-Check: PASSED**

## Issues Encountered

None during planned work. The two auto-fix deviations above were grep-gate friction (literal trigger substrings in comments / examples), not implementation issues.

## User Setup Required

None. All deliverables are doc + script; the actual D-19 key rotation and D-13 admin seeding are operator work tracked in Plan 11-10 HUMAN-UAT.

## Next Phase Readiness

- **11-10 HUMAN-UAT readiness:** The 11-05 deliverables (RUNBOOK-DEPLOY.md sections + seed-admins.sh + preflight.sh dashboard probe) are now the operator's reference for the 11-10 scenarios that execute the actual key rotation and admin seeding live.
- **11-09 cross-reference target:** RUNBOOK-DEPLOY.md and seed-admins.sh now cite `gateway/docs/RUNBOOK-2FA-RECOVERY.md`. Plan 11-09 will create that runbook; the cross-refs become resolvable once 11-09 lands.
- **Defensive note for 11-10 operator:** the per-env key diff procedure in RUNBOOK-DEPLOY.md is the canonical recipe — operators MUST use Recipe A (openssl sha256) or Recipe B (first-4 awk projection); any output containing literal multi-char tails of `=ifix_…` / `=sk-or-…` / `=sk-…` is a procedure failure and the operator must scrub shell history before re-running.

---
*Phase: 11-prod-hardening*
*Plan: 11-05*
*Completed: 2026-05-27*
