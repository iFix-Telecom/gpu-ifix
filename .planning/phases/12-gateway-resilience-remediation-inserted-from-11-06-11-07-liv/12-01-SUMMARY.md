---
phase: 12-gateway-resilience-remediation
plan: 01
subsystem: gateway-resilience
tags: [resilience, observability, breaker, prober, health, RES-12, D-13]
requires:
  - "loader.Resolve(role,0) override-honoring path (Phase 06-08 D-E3)"
  - "breaker force-override read path (Phase 06.9 Plan 04)"
provides:
  - "Loader.ResolveTier0Roles() — override-honoring per-role tier-0 resolution"
  - "prober/health tier-0 parity (no dead static-row flap under override)"
  - "additive override-flagged /v1/health/upstreams payload (backward compatible)"
  - "breaker.WriteForceOverride / ClearForceOverride / CheckForceClose (D-13 force-CLOSE)"
  - "EffectiveState/EffectiveStateSnapshot honor State==closed"
  - "obs.DialFallthroughTotal + obs.PrimaryDeathDetectedTotal counters"
affects:
  - "Plan 12-02 (death detection — force-OPEN/force-CLOSE writes + PrimaryDeathDetectedTotal)"
  - "Plan 12-03 (dial fallthrough — DialFallthroughTotal)"
  - "Plan 12-04/05 (chaos gate reads health endpoint + breaker truthfulness)"
tech-stack:
  added: []
  patterns:
    - "tier-0 override-honoring resolution shared by dispatcher/prober/health"
    - "symmetric breaker force-override (open + closed) with TTL semantics godoc"
    - "additive omitempty JSON payload fields for backward compatibility"
key-files:
  created: []
  modified:
    - gateway/internal/upstreams/loader.go
    - gateway/internal/upstreams/probe.go
    - gateway/internal/upstreams/health.go
    - gateway/internal/upstreams/probe_test.go
    - gateway/internal/upstreams/health_test.go
    - gateway/internal/breaker/force_override.go
    - gateway/internal/breaker/breaker.go
    - gateway/internal/breaker/force_override_test.go
    - gateway/internal/obs/metrics.go
decisions:
  - "ResolveTier0Roles() added as a shared helper over roles {llm,stt,tts,embed} to dedupe the prober + health tier-0 resolution (Resolve already exists; not reimplemented)"
  - "Health override surface = 3 additive omitempty fields (override_active, override_source, overridden); override_source is a role/source LABEL ('primary pod'), never the raw pod URL (T-12-02)"
  - "Replaced static tier-0 row stays listed (additive 'overridden' marker) but is excluded from aggregate-status computation so a live pod never yields aggregate=failed (D-14)"
  - "Force-CLOSE implemented by extending ForceOverrideValue.State to 'closed' + new CheckForceClose read-honor (Pitfall 4 forward-compat realized), reusing the same gw:breaker:force:* key/value shape as the open-only writer"
metrics:
  duration: "~50min"
  completed: "2026-06-12"
  tasks: 3
  files: 9
---

# Phase 12 Plan 01: Prober/Health Tier-0 Parity + Breaker Force-CLOSE + Obs Counters Summary

RES-12 tier-0 parity (prober + `/v1/health/upstreams` now resolve tier-0 through the same override-honoring `Resolve(role,0)` path the dispatcher uses), the D-13 programmatic breaker force-CLOSE primitive, and the two Wave-2 Prometheus counters — landed with additive backward-compatible health payload and full RED/GREEN TDD coverage.

## What Shipped

### Task 1 — RES-12 prober + health tier-0 parity (TDD)
- Added `Loader.ResolveTier0Roles()` (loader.go): resolves the effective tier-0 for each role in `{llm,stt,tts,embed}` via the existing `Resolve(role,0)`, reporting `Overridden` + `ReplacedStaticName` when an emergency override is active.
- `probe.go doTick`: replaced the `loader.All()` tier-0 enumeration with `ResolveTier0Roles()`. Effective tier-0 (the live pod under override) is probed; the dead static tier-0 row is skipped this tick (D-12) so its breaker no longer flaps (SEED-012). Tier-1 rows still come from `All()` (filtered to `Tier!=0`), gated by the *resolved* tier-0 breaker state — D-15 gating intent preserved.
- `health.go buildHealthResponse`: same swap (Pitfall 3 — both call sites changed). Effective tier-0 entry carries additive `override_active`/`override_source`; the replaced static row is listed with an additive `overridden` marker and excluded from the aggregate, so a healthy pod yields aggregate `ok` (HTTP 200), not `failed` (HTTP 503).

### Task 2 — Breaker programmatic force-CLOSE (TDD)
- `force_override.go`: added `WriteForceOverride(ctx,rdb,name,state,ttl,setBy)` (accepts `open`|`closed`, Redis EX=ttl, reuses the gatewayctl key/value shape) with a godoc documenting the short-close (~30-60s, markReady) / long-open (~10min, death) TTL semantics and their interaction with the Ready death poll. Added idempotent `ClearForceOverride`.
- `breaker.go`: added `CheckForceClose` (cache read for `State=="closed"`); `EffectiveState` short-circuits to `StateClosed` and `EffectiveStateSnapshot` reports `"closed"` when force-closed. Force-OPEN path unchanged (regression-tested).

### Task 3 — Obs counters (single-owner)
- `obs/metrics.go`: added `DialFallthroughTotal{role,outcome}` (`gateway_dial_fallthrough_total`) and `PrimaryDeathDetectedTotal{cause}` (`gateway_primary_death_detected_total`), both `promauto.NewCounterVec`, low-cardinality enum labels, with alertable label values documented (`outcome=chain_exhausted`, `cause=billing_stopped`). Increments are intentionally NOT wired here — Plans 02/03 consume them.

## Tests

- `TestProbe_HonorsTier0Override`, `TestProbe_TierGatingPreserved` (probe_test.go)
- `TestHealth_OverrideEffectiveTier0`, `TestHealth_BackwardCompatNoOverride`, `TestHealth_OverrideFieldsAreAdditive` (health_test.go)
- `TestForceOverride_CloseShortCircuits`, `TestForceOverride_OpenStillWorks`, `TestForceOverride_WriteCloseRoundTrips`, `TestForceOverride_DeleteClearsOverride` (force_override_test.go)

Verification (all green):
- `go build ./...` exit 0
- `go test ./internal/upstreams/ ./internal/breaker/ ./internal/obs/ -count=1` — all pass
- `go test ./internal/...` — all pass (proxy, the main `Resolve`/`EffectiveState` consumer, included)

## Deviations from Plan

None — plan executed exactly as written. Rules 1-3 were not triggered; the plan's interfaces matched the codebase. The `tts` and `embed` roles are both in the resolution roster (`tts` has an override slot, `embed` does not — `Resolve("embed",0)` serves the static row unchanged), matching the loader's `newTier0OverrideMap` roster.

## TDD Gate Compliance

Both behavior-adding tasks followed RED → GREEN:
- Task 1: `test(12-01)` 80c719b (RED) → `feat(12-01)` acf377f (GREEN)
- Task 2: `test(12-01)` 0e1b69b (RED) → `feat(12-01)` 139774f (GREEN)
- Task 3 (non-TDD, additive metric definitions): `feat(12-01)` 5fea821

## Known Stubs

None. The two new counters are intentionally defined-but-not-incremented (single-owner pattern for Wave-2 parallelism) — this is documented in the plan (Task 3) and in the metrics.go comment, and is resolved by Plans 12-02/12-03, not a stub blocking this plan's goal.

## Commits

- 80c719b `test(12-01): add failing tests for tier-0 override parity in prober + health`
- acf377f `feat(12-01): RES-12 prober + health tier-0 parity via Resolve(role,0)`
- 0e1b69b `test(12-01): add failing tests for breaker programmatic force-CLOSE`
- 139774f `feat(12-01): breaker programmatic force-CLOSE (D-13) + force-OPEN reuse`
- 5fea821 `feat(12-01): add DialFallthroughTotal + PrimaryDeathDetectedTotal counters`

## Self-Check: PASSED

- All modified files present on disk (9/9 FOUND).
- All 5 commit hashes present in git history.
