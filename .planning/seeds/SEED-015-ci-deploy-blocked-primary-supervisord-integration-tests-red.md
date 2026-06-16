# SEED-015 — CI deploy pipeline blocked: 6 primary/supervisord integration tests red → nothing ships

**Discovered:** 2026-06-16 while root-causing the primary-pod cold-start loop (SEED-014).
**Severity:** HIGH — the entire deploy pipeline is gated shut. `Build & push gateway image` + `Trigger Portainer redeploy (dev/prod)` are SKIPPED whenever the integration-test gate is red, which it has been since at least 2026-06-14. Nothing has deployed to prod since. Today's probe-truth fix (SEED-013, commit 844bf2a) is on `develop` but **did NOT deploy** for this reason.
**Related:** [[SEED-014-primary-coldstart-failure-loop-invisible-alerting-gap]] (the pod loop this blocks remediation of), [[SEED-013-probe-hardcodes-qwen-model-no-per-upstream-rewrite]] (fix pushed but undeployed).

## Evidence

`build-gateway.yml` job graph (run 27630470565, develop, 2026-06-16):

```
success | Go unit tests + sqlc codegen verify
failure | Integration tests (testcontainers-go)      ← GATE
success | Compute image tags
skipped | Build & push gateway image                 ← never runs
skipped | Trigger Portainer redeploy (dev)           ← never runs
skipped | Trigger Portainer redeploy (prod)
```

All recent `build-gateway` + `build-dashboard` runs (06-14, 06-16, both develop and main) = `failure`. Unit tests + sqlc pass; the **integration suite** is the blocker.

Failing tests (`gateway/internal/integration_test`, 284s, exit 1):

```
--- FAIL: TestPrimaryCancelInflight_TripleLayer (6.09s)
--- FAIL: TestPrimaryDisabled_ForceUpRequest_Provisions (7.18s)
--- FAIL: TestPrimaryProbe_MarkReady_OverridesTier03Roles_4EndpointsReachable (20.09s)
--- FAIL: TestSupervisord_4ServicesReachableOnLocalhost (20.08s)
--- FAIL: TestSupervisord_OneEndpointDown_DoesNotPromoteToReady (5.09s)
--- FAIL: TestSupervisord_AutorestartSimulated_RecoveryAfterTransientFailure (25.09s)
```

All 6 are in the **primary-pod / supervisord** area — the same subsystem as the cold-start health_timeout loop (SEED-014).

## Why it went unnoticed

- Phase 12 verification (2026-06-13) ran `go test ./internal/...` and passed — but the `integration_test` package is testcontainers-based; it is likely **skipped in the local/verification run** (no Docker/testcontainers there) and only executed in CI. So these can fail in CI while local `go test` is green → a verified-PASSED phase coexists with a red CI gate.
- Combined with SEED-014 (no deploy-failure alerting / no digest), a red deploy pipeline produced no visible signal.

## Consequences (compounding chain)

1. Integration gate red → no `gateway` image built/pushed/deployed since the break.
2. → primary-pod image (`build-primary-pod`, develop-only; newest GHCR image 2026-06-08) is stale vs pod code that advanced to 2026-06-13 (cc4b07d/f8a7de4/8bf983b chatterbox+mc fixes).
3. → SEED-013 probe fix (844bf2a, today) is undeployed; the tier-1 probe still lies in prod.
4. → any SEED-014 pod-loop remediation also cannot ship until the gate is green.

## Open questions for the debug

- Are the 6 failures a **real regression** (a commit after the last-green run broke supervisord/primary behavior) or **CI-environment/flake** (testcontainers timing, Docker-in-CI, the 20–25s ones look timeout-shaped)? Find the last green `build-gateway` run and bisect the commits between.
- Do these tests pass **locally** with Docker available? If yes → CI-environment issue (runner Docker/testcontainers config). If no → real break — likely in the 06-13 pod commits or a primary reconciler change.
- The `TestSupervisord_*` names + the live pod health_timeout (SEED-014) being the SAME subsystem is suspicious — a real supervisord/4-endpoint-readiness regression would explain BOTH the red tests AND the live cold-start failures.

## Immediate implication

**Fix this FIRST.** Until the integration gate is green, no fix for SEED-013, SEED-014, or anything else can reach prod via the normal pipeline. Manual image build + PAT-push + manual Portainer pull is the only bypass (see ops memory `ghcr-actions-install-broken` for the manual-retag recipe), but that ships unverified images around the gate.
