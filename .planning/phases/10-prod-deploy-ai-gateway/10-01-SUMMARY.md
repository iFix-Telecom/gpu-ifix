---
phase: 10-prod-deploy-ai-gateway
plan: 01
subsystem: infra
tags: [phase-10, prod-deploy, compose, env-contract, capacity-gate, network-reconciliation, traefik-discovery, preflight]

# Dependency graph
requires:
  - phase: 06.8
    provides: "Primary-pod GPU shape (2×RTX 3090 allowlist 43803/55158 cap $0.60) — pinned in env contract"
  - phase: 06.9
    provides: "D-06 env-override-wins precedence + UPSTREAM_<UPSTREAM>_MODEL empty-string-as-unset rule — applied to UPSTREAM_LLM_OPENROUTER_MODEL= row"
provides:
  - "Operator-managed prod compose stack file (gateway + dashboard + redis-gateway-prod) with network: intra external — closes RESEARCH Pitfalls 1, 3, 4"
  - "Documented prod env contract — 53 KEY= rows, every value placeholder or safe default, header WARNING for D-08 shared-key invariant"
  - "Idempotent preflight script (5 probes: ssh / capacity / intra attachable / Traefik discovery / GHA runners) with FALLBACK REQUIRED messaging for Open Question 2"
  - "Capacity-observed scaffold matching the 5 probe sections — populated live during Plan 10-06 HUMAN-UAT Gate B"
affects: [10-02, 10-03, 10-04, 10-05, 10-06]

# Tech tracking
tech-stack:
  added: []   # No new packages installed (Phase 10 is config-only per RESEARCH §Package Legitimacy Audit)
  patterns:
    - "Operator-managed direct compose (D-11) — sibling file pattern (gateway/docker-compose.prod.yml alongside the Portainer Swarm template gateway/docker-compose.yml)"
    - "env_file: .env contract — populated from ~/.claude/CLAUDE.md secrets + Sentry UI + openssl rand + gatewayctl admin-key"
    - "Preflight gate pattern — log_section banners, trap-on-EXIT ephemeral container cleanup, distinct exit codes per gate (0/1/2/3/4)"

key-files:
  created:
    - "gateway/.env.prod.example (187 lines; 53 KEY= rows across 13 sections)"
    - "gateway/docker-compose.prod.yml (117 lines; 3 services + external intra network)"
    - "scripts/deploy/preflight.sh (291 lines; 5 probes, executable)"
    - ".planning/phases/10-prod-deploy-ai-gateway/10-01-CAPACITY-OBSERVED.md (79 lines; scaffold)"
  modified: []

key-decisions:
  - "Phase 10 implementable form of D-05 = NEW DATABASE bd_ai_gateway_prod (NOT new schema) — schema name ai_gateway is hardcoded in 27 migrations + pool.go + sqlc queries (RESEARCH Pitfall 2)"
  - "Symmetric form of D-06 = NEW DATABASE bd_ai_dashboard_prod + hardcoded dashboard_auth schema name (Better Auth)"
  - "Prod stack uses `intra` external network — NOT `worker_intra` (which does not exist on n8n-ia-vm Swarm; RESEARCH Pitfall 1)"
  - "Prod compose OMITS `tls.certresolver` label and uses `entrypoints=web` (NOT websecure) — internal Traefik on n8n-ia-vm listens on :80 only; TLS terminates at edge Traefik on vps-ifix-vm using literal `letsencrypt` resolver (RESEARCH Pitfall 4)"
  - "Prod compose uses top-level `restart: unless-stopped` + top-level `labels:` — NO `deploy:` block (Swarm-only, silently ignored by docker compose up -d; RESEARCH Pitfall 3)"
  - "UPSTREAM_LLM_OPENROUTER_MODEL= left empty per Phase 06.9 D-06 — schema row from migration 0027 (deepseek-v4-flash:nitro) wins; env var is per-instance escape hatch only"
  - "Live capacity capture INTENTIONALLY DEFERRED to Plan 10-06 HUMAN-UAT Gate B — autonomous executor has no SSH session credentials; Wave 0 ships scaffolds + verification script only"

patterns-established:
  - "Sibling-file prod compose: keep canonical Portainer Swarm template untouched; new operator-managed file lives at gateway/docker-compose.prod.yml with documented diff from the analog"
  - "Env contract header WARNING block: cite the cross-environment invariant (D-08 shared keys) at the top of .env.prod.example so operator sees rotation requirements before reading the KEY= rows"
  - "Preflight log_section banner + trap-on-EXIT cleanup: every probe section has a printf banner; ephemeral containers are removed in an EXIT trap regardless of which gate failed"

requirements-completed: []   # Plan 10-01 frontmatter declares requirements: [] (Wave 0 scaffolding — no requirement IDs satisfied directly here)

# Metrics
duration: ~25min
completed: 2026-05-26
---

# Phase 10 Plan 01: Wave 0 — Network & Env Reconciliation Summary

**Three Wave 0 scaffolds (`docker-compose.prod.yml` with network=intra, `.env.prod.example` with 53 documented KEY= rows + D-08 shared-key WARNING, `scripts/deploy/preflight.sh` with 5 probes including the Traefik Swarm-discovery proof) that close RESEARCH Pitfalls 1, 3, and 4 before any downstream Phase 10 plan deploys against `n8n-ia-vm`.**

## Performance

- **Duration:** ~25 min (read context + 3 file authoring + verify + commit)
- **Started:** 2026-05-26T05:26 (worktree spawn)
- **Completed:** 2026-05-26T05:32 (last task commit)
- **Tasks:** 3 / 3 GREEN
- **Files created:** 4
- **Files modified:** 0

## Accomplishments

- Resolved the three CONTEXT-vs-reality mismatches RESEARCH surfaced (network name `worker_intra` → `intra`; schema name path = new DATABASE not new schema; edge Traefik lives on vps-ifix-vm not the Proxmox host) by shipping authoritative scaffolds every downstream plan can reference.
- Authored a 53-KEY env contract that documents the source of every value (~/.claude/CLAUDE.md / Sentry UI / openssl rand / gatewayctl admin-key) and surfaces the D-08 shared-key rotation invariant in a header WARNING block.
- Authored an operator-managed Compose v2 prod stack file with 3 services (gateway + dashboard + redis-gateway-prod) that closes Pitfalls 1, 3, and 4 by file shape — independent of any operator discipline.
- Authored an idempotent preflight script (5 probes, distinct exit codes per gate, trap-on-EXIT cleanup) that the operator runs once during Plan 10-06 Gate B; the script populates `10-01-CAPACITY-OBSERVED.md` with timestamped probe output and emits explicit `FALLBACK REQUIRED` directive if the Traefik Swarm-discovery probe (Open Question 2 / Assumption A2) fails.

## Task Commits

Each task was committed atomically against `worktree-agent-ab931c9f7b41e9a9a`:

1. **Task 1: Author `gateway/.env.prod.example`** — `a221086` (feat)
2. **Task 2: Author `gateway/docker-compose.prod.yml`** — `fa91792` (feat)
3. **Task 3: Author `scripts/deploy/preflight.sh` + capacity scaffold** — `7667fda` (feat)

## Files Created/Modified

- `gateway/.env.prod.example` — Documented prod env contract (53 KEY= rows; 13 banner-separated sections; D-08 WARNING header). Mirror of `gateway/.env.portainer.example` adapted for operator-managed direct-compose at `/opt/ai-gateway-prod/.env`.
- `gateway/docker-compose.prod.yml` — Operator-managed prod compose stack (3 services; network `intra` external; container_name set on all 3 services; healthchecks on gateway + redis; Traefik labels with `entrypoints=web` and no certresolver). Header documents the 4 pitfalls closed.
- `scripts/deploy/preflight.sh` — Idempotent operator probe script (5 sections: connectivity / capacity / intra attachable / Traefik discovery / GHA runners). Distinct exit codes 0/1/2/3/4. Trap-on-EXIT removes the ephemeral `preflight-hello` container regardless of probe result.
- `.planning/phases/10-prod-deploy-ai-gateway/10-01-CAPACITY-OBSERVED.md` — Scaffold with frontmatter (phase 10, plan 01, host n8n-ia-vm, expected_egress_ip 162.55.92.154) + 5 section anchors matching the probe order. Populated live during Plan 10-06 HUMAN-UAT Gate B.

## Decisions Made

- **Implementable D-05 = new DATABASE not new schema.** RESEARCH Pitfall 2 confirms `ai_gateway` schema name is hardcoded in 27 migrations + `pool.go:35` + every sqlc query. Renaming it during Phase 10 would touch every migration + sqlc regen — explicitly out of scope. The DSN points at `bd_ai_gateway_prod` (new DB on same DO instance) and migrations create their hardcoded `ai_gateway` schema inside it. Documented in `.env.prod.example` Postgres section comment block.
- **Symmetric D-06 = new DATABASE for dashboard.** `DASHBOARD_DATABASE_URL` points at `bd_ai_dashboard_prod` with `search_path=dashboard_auth` (Better Auth's hardcoded schema name).
- **Wave 0 scope = files only; live probes deferred.** Autonomous executor has no SSH credentials; the preflight script EXISTS in Wave 0 but RUNS in Plan 10-06 Gate B. The `<verification>` section of the plan explicitly states "actual execution of `./scripts/deploy/preflight.sh` against the live n8n-ia-vm host is INTENTIONALLY deferred to Plan 10-06 HUMAN-UAT Gate B".
- **Followed RESEARCH §Code Examples verbatim.** Both Task 1 (.env contract) and Task 2 (compose stack) mirror the verified drafts in RESEARCH lines 584-676 + 678-759 — the planner front-loaded the content so the executor's job is faithful reproduction + verification, not authorship.

## Deviations from Plan

**None — plan executed exactly as written.**

All three tasks completed on the first pass with verification commands passing as specified. No bugs surfaced. No missing critical functionality (the env contract already documents secret rotation per D-08; the compose stack already omits host port publishes per Anti-Patterns; the preflight script already includes the trap-on-EXIT cleanup per RESEARCH How To #1). No architectural ambiguity that would require Rule 4.

The verification grep `grep -c '^[A-Z_]\+=' gateway/.env.prod.example` in the plan returned 47 (a regex artifact of `+` matching short keys only); the broader `^[A-Z_][A-Z_0-9]*=` grep returned 53. Both are well above the `≥30` gate, so the discrepancy is documentation-only, not a deviation.

## Issues Encountered

**None.** The worktree base reset (`25f4f21`) ran cleanly. All four target paths were absent (clean slate confirmed). The three commits landed with HEAD safety + cwd-drift guards passing on every attempt.

## User Setup Required

None at Wave 0. The operator's setup actions (DO database creation, Sentry project create, CF DNS POST, populating `.env` from `~/.claude/CLAUDE.md` + `openssl rand`) are documented in Wave 0 deliverables but EXECUTED in Plan 10-06 HUMAN-UAT (per D-19 + D-10 — operator-driven cut-release plan).

## Next Phase Readiness

**Plan 10-02 (Postgres bootstrap) unblocked:**
- `.env.prod.example` documents the exact `AI_GATEWAY_PG_DSN=…/bd_ai_gateway_prod?sslmode=require` shape Plan 10-02 references.
- `docker-compose.prod.yml` has `AI_GATEWAY_MIGRATE_ON_BOOT=true` plumbing via `env_file: .env` so first-deploy migration runs on container start.

**Plan 10-03 (Traefik route + DNS) unblocked:**
- `docker-compose.prod.yml` Traefik labels use the correct `entrypoints=web` value matching internal Traefik on n8n-ia-vm:80.
- The edge Traefik file-provider route artifact (`infra/traefik-dynamic/ai-gateway-prod.yml`) was NOT created by this plan — that is Plan 10-03's deliverable; RESEARCH §How To #2 + PATTERNS §Pattern 3 supply the verbatim content.

**Plan 10-06 (HUMAN-UAT) unblocked:**
- `scripts/deploy/preflight.sh` exists and is verified syntactically clean (`bash -n` + executable bit).
- `10-01-CAPACITY-OBSERVED.md` scaffold has the section anchors the script will populate.
- Gate B of the HUMAN-UAT can now reference `bash scripts/deploy/preflight.sh` as a single-command preflight before the operator burns Vast spend on the cut-release.

**No blockers introduced.** Phase 10 Wave 1 (Plan 10-02) can start immediately once all Wave 0 worktree agents merge.

## Threat Surface Scan

Threat register from PLAN.md (`<threat_model>`) covered by Wave 0 file shape:

- **T-10-01-01** (Information disclosure — `.env.prod.example` containing real secrets) — MITIGATED. Every value is `<PLACEHOLDER>` syntax or empty `=`; no real bearer tokens, keys, passwords, or DSN credentials in the committed file. Confirmed by audit grep for known live-secret prefixes (`sk-or-v1-`, `cfut_`, `ghp_`, MinIO access key literal) — all absent.
- **T-10-01-02** (Tampering — YAML parse failure) — MITIGATED. Task 2 verify ran `python3 -c "yaml.safe_load(...)"` + 3-service set assertion + external-network assertion + no-deploy-block + no-certresolver-label + no-host-ports — all PASS.
- **T-10-01-03** (DoS — capacity ceiling) — MITIGATED. `scripts/deploy/preflight.sh` §2 aborts (exit 2) if disk `/` > 80%; aborts (exit 2) if egress IP ≠ 162.55.92.154 (DO Trusted Sources guard). Capacity values recorded in `10-01-CAPACITY-OBSERVED.md` for audit.
- **T-10-01-04** (Information disclosure — D-08 shared-key separation confusion) — ACCEPTED. Documented as known limitation in `.env.prod.example` header WARNING block.
- **T-10-01-05** (Tampering — Traefik discovery silently fails) — MITIGATED. Preflight §4 spawns synthetic `preflight-hello` container with Traefik labels; greps Swarm service logs for `router added` match; emits explicit `FALLBACK REQUIRED: switch traefik-internal to dual-provider` directive + exit 3 on failure.
- **T-10-01-SC** (Supply chain) — ACCEPTED. Phase 10 installs ZERO new packages (confirmed). No new dependency vectors introduced by this plan.

No new threat flags surfaced. The 4 files created are config-and-script artifacts; no new network endpoints, auth paths, file access patterns, or schema changes were introduced at trust boundaries.

## Self-Check: PASSED

- `gateway/.env.prod.example` — FOUND (187 lines, 53 KEY= rows).
- `gateway/docker-compose.prod.yml` — FOUND (117 lines, 3 services, intra external).
- `scripts/deploy/preflight.sh` — FOUND (291 lines, executable, `bash -n` clean).
- `.planning/phases/10-prod-deploy-ai-gateway/10-01-CAPACITY-OBSERVED.md` — FOUND (79 lines, frontmatter + 5 section anchors).
- Commit `a221086` (Task 1) — FOUND in `git log`.
- Commit `fa91792` (Task 2) — FOUND in `git log`.
- Commit `7667fda` (Task 3) — FOUND in `git log`.

All claims in this SUMMARY verified against worktree state before write.

---

*Phase: 10-prod-deploy-ai-gateway*
*Completed: 2026-05-26*
