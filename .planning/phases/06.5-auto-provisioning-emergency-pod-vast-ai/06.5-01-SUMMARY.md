---
phase: 06-auto-provisioning-emergency-pod-vast-ai
plan: 01
subsystem: infra
tags: [go, redsync, prometheus, config, emergency-pod, vast-ai, scaffolding]

# Dependency graph
requires:
  - phase: 03-circuit-breaker
    provides: "breaker/errors.go + breaker/mirror.go pattern (sentinel errors + namespace const) replicated for emerg/"
  - phase: 05-shed
    provides: "promauto collector registration pattern in obs/metrics.go + Config struct extension pattern in config.go"
provides:
  - "go.mod direct dep github.com/go-redsync/redsync/v4 v4.16.0 (Redlock for emergency lifecycle singleton)"
  - "gateway/internal/emerg/ package skeleton with 6 sentinel errors + namespace constant gw:emerg:"
  - "gateway/internal/emerg/vast/ sub-package with 4 sentinel errors + VastError struct"
  - "11 new Phase 6 env vars in Config struct with conservative defaults (operator-confirmed)"
  - "7 Prometheus collectors registered via promauto (gateway_emergency_* + gateway_vast_api_*)"
  - "Operator gate decisions recorded in 06-WAVE0-GATES.md (all 7 = default-aceito)"
affects: [06-02, 06-03, 06-04, 06-05, 06-06, 06-07, 06-08, 06-09, 06-10, 06-11]

# Tech tracking
tech-stack:
  added:
    - "github.com/go-redsync/redsync/v4 v4.16.0 (Redlock distributed mutex for lifecycle singleton)"
    - "github.com/go-redsync/redsync/v4/redis/goredis/v9 (driver bridging existing redis/go-redis/v9 v9.18.0)"
  patterns:
    - "sentinel errors via var (...) block + errors.New (replicated from breaker package)"
    - "namespace constant per package: const Namespace = \"gw:<pkg>:\""
    - "promauto.NewXxx() global vars in obs/metrics.go (no manual prometheus.MustRegister)"
    - "Config struct extension with parseFloat/parseInt env helpers, graceful degrade for empty VAST_AI_API_KEY (no fail-loud at boot)"

key-files:
  created:
    - "gateway/internal/emerg/errors.go (6 sentinels: ErrOfferRaceLost, ErrHealthTimeout, ErrInstanceTerminal, ErrNoOffersBelowCap, ErrLeaderLost, ErrLifecycleSingleton)"
    - "gateway/internal/emerg/vast/errors.go (4 sentinels: ErrOfferGone, ErrInstanceNotFound, ErrRateLimited, ErrUnauthorized + VastError struct)"
    - "gateway/internal/emerg/mirror.go (const Namespace = \"gw:emerg:\")"
    - ".planning/phases/06-auto-provisioning-emergency-pod-vast-ai/06-WAVE0-GATES.md (operator decisions register)"
    - ".planning/phases/06-auto-provisioning-emergency-pod-vast-ai/deferred-items.md (pre-existing argon2id test hang)"
  modified:
    - "go.mod (added redsync/v4 v4.16.0; toolchain auto-bumped 1.23.0 -> 1.24.9)"
    - "go.sum (transitive deps from redsync resolution)"
    - "gateway/internal/config/config.go (11 new fields + Load() parsing with defaults)"
    - "gateway/internal/config/config_test.go (defaults validation tests)"
    - "gateway/internal/obs/metrics.go (7 new promauto collectors at end of file)"

key-decisions:
  - "All 7 operator gate env vars accepted at documented defaults (no overrides) — see 06-WAVE0-GATES.md"
  - "Empty VAST_AI_API_KEY does NOT fail boot — Phase 6 reconciler will graceful-degrade (disabled) so Phases 1-5 keep running"
  - "VAST_AI_API_KEY confirmed in GitHub Secret IfixTelecom/gpu-ifix; Portainer stack ai-gateway-dev env var planned before Plan 06-11 LIVE UAT"
  - "VAST_AI_API_KEY rotation deferred to Phase 10 GA cutover (operator did not flag transcript leak risk)"
  - "Pre-existing argon2id test hang in gateway/internal/auth confirmed NOT introduced by Plan 06-01; tracked in deferred-items.md (out-of-scope quick-fix candidate)"
  - "Toolchain bump 1.23.0 -> 1.24.9 accepted (forced by redsync v4.16.0 module requirement)"

patterns-established:
  - "Phase-6 env var prefix conventions: VAST_* (vendor api), PROVISION_* (lifecycle timing), MONTHLY_EMERGENCY_* / USD_TO_BRL_RATE (cost guardrails)"
  - "Phase-6 metric prefix: gateway_emergency_* (lifecycle/cost) and gateway_vast_api_* (vendor calls), reusing OBS-02 cardinality budget"
  - "Wave-0 operator-gate template: per-row decision (default-aceito | override:VAL | defer:NN) + checkbox confirmations + resume signal"

requirements-completed: [PRV-01, PRV-02, PRV-05]

# Metrics
duration: ~45min (3 task commits + operator-gate roundtrip)
completed: 2026-05-13
---

# Phase 6 Plan 01: Wave-0 Emergency-Pod Scaffolding + Operator Gate Summary

**redsync v4.16.0 + 11 Phase-6 env vars + 7 Prometheus collectors + emerg package skeleton with 10 sentinel errors and gw:emerg: namespace, gated by operator-confirmed defaults in 06-WAVE0-GATES.md.**

## Performance

- **Duration:** ~45 min (3 commits + operator response cycle)
- **Started:** 2026-05-13 (executor wave start)
- **Completed:** 2026-05-13 (operator gate closed at commit 78adfed)
- **Tasks:** 3 of 3 (Task 1 + Task 2 auto-executed; Task 3 = checkpoint:human-verify, closed by operator)
- **Files modified:** 5 created + 5 modified = 10 total

## Accomplishments

- Added `github.com/go-redsync/redsync/v4 v4.16.0` as a direct dep without breaking any existing build (Redlock primitive available for Plan 06-04 lifecycle singleton)
- Scaffolded `gateway/internal/emerg/` and `gateway/internal/emerg/vast/` packages with 10 total sentinel errors + `VastError` struct + namespace constant `gw:emerg:` — Plans 06-02..06-11 can now import without hitting "undefined" symbols
- Extended `gateway/internal/config/config.go` with 11 new env vars (VAST_AI_API_KEY, VAST_PRICE_CAP_DPH, EMERGENCY_POD_IMAGE_TAG, PRIMARY_HOST_ID, MONTHLY_EMERGENCY_BUDGET_BRL, USD_TO_BRL_RATE, PROVISION_TRIGGER_FAILED_OVER_SECONDS, PROVISION_HEALTHY_DURATION_SECONDS, PROVISION_IDLE_GRACE_SECONDS, PROVISION_COLDSTART_BUDGET_SECONDS, VAST_API_QPS_LIMIT) with conservative defaults
- Registered 7 Phase-6 Prometheus collectors via `promauto` in `obs/metrics.go` with no name collisions and cardinality estimated at ~80 baseline series (well within OBS-02 budget)
- Closed the Wave-0 operator gate: all 7 decisions = `default-aceito`; 2 of 3 confirmation checkboxes ticked (rotation deferred to Phase 10 by operator choice)

## Task Commits

Each task was committed atomically:

1. **Task 1: redsync v4.16.0 + sentinel errors + namespace + 11 config env vars** — `9726632` (feat)
2. **Task 2: 7 emergency-pod Prometheus collectors** — `5a3c13a` (feat)
3. **Task 3a: WAVE-0 operator-gate template** — `436fd34` (docs)
4. **Task 3b: Operator decisions accepted** — `78adfed` (chore — committed by orchestrator after operator response)

**Plan metadata:** this SUMMARY commit (docs)

_Note: Task 3 is a `checkpoint:human-verify` gate; orchestrator handled the operator-response commit, executor returned to write SUMMARY._

## Files Created/Modified

- `gateway/internal/emerg/errors.go` — 6 sentinel errors for emerg package (Phase 6 reconciler vocabulary)
- `gateway/internal/emerg/vast/errors.go` — 4 sentinel errors + `VastError{Status, Code, Msg}` struct for Vast.ai REST client
- `gateway/internal/emerg/mirror.go` — `const Namespace = "gw:emerg:"` (replicates breaker/mirror.go pattern)
- `.planning/phases/06-auto-provisioning-emergency-pod-vast-ai/06-WAVE0-GATES.md` — operator decisions register (7 decisions, all `default-aceito`)
- `.planning/phases/06-auto-provisioning-emergency-pod-vast-ai/deferred-items.md` — out-of-scope discovery log (argon2id pre-existing flake)
- `go.mod` — added `github.com/go-redsync/redsync/v4 v4.16.0` direct dep; toolchain auto-bumped `1.23.0` → `1.24.9`
- `go.sum` — transitive deps from redsync resolution
- `gateway/internal/config/config.go` — Config struct + Load() extended with 11 new Phase-6 fields
- `gateway/internal/config/config_test.go` — `TestConfigLoadDefaults` extended to validate new defaults
- `gateway/internal/obs/metrics.go` — 7 new promauto collectors appended (Phase 6 block)

## Verification Evidence

| Check | Command | Result |
|-------|---------|--------|
| Build clean | `cd gateway && go build ./...` | exit 0 |
| Config tests | `cd gateway && go test -race ./internal/config/...` | exit 0 (1.091s) |
| Obs tests | `cd gateway && go test -race ./internal/obs/...` | exit 0 (1.043s) |
| Emerg tests | `cd gateway && go test -race ./internal/emerg/...` | exit 0 (stub-only) |
| Vet clean | `cd gateway && go vet ./...` | no warnings |
| Sentinel grep emerg | `grep -c "ErrOfferRaceLost\|ErrHealthTimeout\|ErrInstanceTerminal\|ErrNoOffersBelowCap\|ErrLeaderLost\|ErrLifecycleSingleton" gateway/internal/emerg/errors.go` | ≥6 |
| Sentinel grep vast | `grep -c "ErrOfferGone\|ErrInstanceNotFound\|ErrRateLimited\|ErrUnauthorized\|VastError" gateway/internal/emerg/vast/errors.go` | ≥5 |
| Namespace grep | `grep "Namespace = \"gw:emerg:\"" gateway/internal/emerg/mirror.go` | 1 match |
| Collector grep | `grep -c "GatewayEmergencyState\|GatewayEmergencyLifecyclesTotal\|GatewayEmergencyActivePod\|GatewayEmergencyProvisionDurationSeconds\|GatewayEmergencyCostDPH\|GatewayEmergencyMonthCostBRL\|GatewayVastAPIRequestsTotal" gateway/internal/obs/metrics.go` | ≥7 |

## Operator Gate Outcome

Recorded in `.planning/phases/06-auto-provisioning-emergency-pod-vast-ai/06-WAVE0-GATES.md` (commit `78adfed`).

| # | Env var | Default | Operator decision |
|---|---------|---------|-------------------|
| 1 | `VAST_PRICE_CAP_DPH` | `0.40` | `default-aceito` |
| 2 | `MONTHLY_EMERGENCY_BUDGET_BRL` | `200` | `default-aceito` |
| 3 | `USD_TO_BRL_RATE` | `5.0` | `default-aceito` |
| 4 | `EMERGENCY_POD_IMAGE_TAG` | `v1.0` | `default-aceito` |
| 5 | `PRIMARY_HOST_ID` | `0` (unknown) | `default-aceito` |
| 6 | `PROVISION_TRIGGER_FAILED_OVER_SECONDS` | `120` | `default-aceito` |
| 7 | `PROVISION_COLDSTART_BUDGET_SECONDS` | `600` | `default-aceito` |

Confirmation checklist (section 2 of 06-WAVE0-GATES.md):

- [x] **GitHub Secret present:** `VAST_AI_API_KEY` confirmed via `gh secret list -R IfixTelecom/gpu-ifix` (added 2026-05-12 per CLAUDE.md token store)
- [x] **Portainer stack env var planned:** operator will add `VAST_AI_API_KEY` to `ai-gateway-dev` stack before Plan 06-11 LIVE UAT
- [ ] **Optional rotation:** deferred — operator did not flag transcript-leak risk; re-evaluate before Phase 10 GA cutover

**Net result:** Zero operator overrides, zero blockers, no Phase-6 plan deferrals. Plans 06-02..06-11 unblocked.

## Decisions Made

- Accepted all 7 documented defaults as production values (operator decision, recorded in 06-WAVE0-GATES.md)
- Empty `VAST_AI_API_KEY` does NOT fail boot — `Load()` warns and Phase 6 reconciler stays disabled (planned graceful degrade so existing Phases 1-5 still serve)
- Toolchain bump `1.23.0` → `1.24.9` accepted (forced by `go-redsync/redsync/v4 v4.16.0` declaring `go >= 1.24.9`)
- `VAST_AI_API_KEY` rotation deferred to Phase 10 GA cutover (operator did not flag transcript-leak risk)

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Toolchain auto-bump from 1.23.0 to 1.24.9**
- **Found during:** Task 1 (`go get github.com/go-redsync/redsync/v4@v4.16.0`)
- **Issue:** `redsync/v4 v4.16.0` declares `go 1.24.9` in its module file. The local `go.mod` was at `go 1.23.0`. Without a toolchain bump, `go get` would refuse to add the dep.
- **Fix:** Allowed the automatic toolchain directive update so `go.mod` now states `go 1.23.0` + `toolchain go1.24.9`. No code-side adjustments were required (no Go 1.24-only features were used; the bump only changes the toolchain selector).
- **Files modified:** `go.mod`, `go.sum`
- **Verification:** `cd gateway && go build ./...` exit 0; `go vet ./...` clean
- **Committed in:** `9726632` (Task 1 commit)

### Out-of-scope Discoveries

**2. Pre-existing argon2id test hang in `gateway/internal/auth`**
- **Found during:** Task 2 verification (`go test -race ./gateway/internal/...` swept past the obs package into auth)
- **Issue:** `TestGenerateAPIKey_UniquePer1000` and surrounding argon2id-heavy tests hang past 60s with `-race` (90s without). Pure CPU saturation on this VM, not a logic bug.
- **Confirmed pre-existing:** Stashed all Plan 06-01 changes and re-ran the same command on baseline `9726632` minus this plan's edits — the test still hangs.
- **Out-of-scope rationale:** Plan 06-01 only scaffolds `emerg/`, `config/`, `obs/`, and `go.mod`. The auth package is untouched. Per scope-boundary rule, the issue is logged in `deferred-items.md` for a separate `/gsd:quick` plan to address (timeout bump, iteration reduction, or `-short` skip).
- **Files modified:** `.planning/phases/06-auto-provisioning-emergency-pod-vast-ai/deferred-items.md` (created)
- **Action:** None taken in this plan; flagged for separate follow-up. Phase-6 plans 02-11 do not exercise this test path.

---

**Total deviations:** 1 auto-fixed (Rule 3 blocking — toolchain bump) + 1 out-of-scope discovery logged
**Impact on plan:** Toolchain bump was unavoidable (transitive dep requirement, no functional impact). Pre-existing flake correctly deferred. No scope creep.

## Issues Encountered

None during planned work — all three tasks executed cleanly. The single deviation (toolchain bump) was an automatic blocking-fix per Rule 3, not a problem requiring problem-solving.

## must_haves Verification

Restated from PLAN.md `must_haves.truths` (frontmatter) — each item confirmed:

| Truth | Confirmation |
|-------|--------------|
| `github.com/go-redsync/redsync/v4 v4.16.0` resolved in go.mod, no breaking change | `go build ./...` exit 0; commit `9726632` |
| Sentinel errors of `emerg` and `emerg/vast` exist before downstream depends on them | 6 sentinels in `errors.go` + 4 sentinels + `VastError` in `vast/errors.go`; commit `9726632` |
| Config exposes 11 new Phase-6 env vars with conservative defaults | All 11 fields added to Config struct + `Load()`; `TestConfigLoadDefaults` passes; commit `9726632` |
| 7 obs collectors `emergency_*` registered via promauto without `gateway_*` collisions | 7 promauto vars added at end of `obs/metrics.go`; `go test ./internal/obs/...` passes (no duplicate-registration panic); commit `5a3c13a` |
| Namespace const `emerg.Namespace = "gw:emerg:"` defined | `grep` returns 1 match in `gateway/internal/emerg/mirror.go`; commit `9726632` |
| Operator confirmed VAST_AI_API_KEY + 4 cost env vars; values recorded in 06-WAVE0-GATES.md | All 7 decisions = `default-aceito`; section 2 checkboxes 1+2 of 3 ticked (rotation deferred); commits `436fd34` (template) + `78adfed` (operator response) |

All 6 truths met. All 6 artifacts present at the specified paths. All 2 key_links validated by grep + build.

## User Setup Required

`VAST_AI_API_KEY` must be added to the Portainer stack `ai-gateway-dev` env vars **before** running Plan 06-11 (LIVE UAT). Operator confirmed this is planned (06-WAVE0-GATES.md section 2 checkbox 2). All other operator-facing setup is satisfied (GitHub Secret already present).

No `USER-SETUP.md` was generated for this plan because the only external-service requirement was the existing GitHub Secret (already in place per CLAUDE.md token store).

## Next Phase Readiness

- Plans 06-02..06-11 are unblocked: redsync, sentinels, namespace, env vars, and collectors all in place
- `gateway/internal/emerg/` and `gateway/internal/emerg/vast/` packages exist as importable stubs (no FSM/reconciler/client yet — those land in subsequent plans)
- Operator gate closed; no architectural decisions outstanding
- Pre-existing `gateway/internal/auth` test flake tracked in `deferred-items.md` for separate `/gsd:quick` plan; does NOT block Phase 6 progression

---
*Phase: 06-auto-provisioning-emergency-pod-vast-ai*
*Completed: 2026-05-13*

## Self-Check: PASSED

Verified files exist on disk:
- FOUND: `gateway/internal/emerg/errors.go`
- FOUND: `gateway/internal/emerg/vast/errors.go`
- FOUND: `gateway/internal/emerg/mirror.go`
- FOUND: `gateway/internal/config/config.go` (modified)
- FOUND: `gateway/internal/obs/metrics.go` (modified)
- FOUND: `go.mod` (modified)
- FOUND: `.planning/phases/06-auto-provisioning-emergency-pod-vast-ai/06-WAVE0-GATES.md`
- FOUND: `.planning/phases/06-auto-provisioning-emergency-pod-vast-ai/deferred-items.md`

Verified commits exist in git log:
- FOUND: `9726632` (Task 1 — redsync + sentinels + config)
- FOUND: `5a3c13a` (Task 2 — 7 prometheus collectors)
- FOUND: `436fd34` (Task 3a — operator-gate template)
- FOUND: `78adfed` (Task 3b — operator decisions accepted)
