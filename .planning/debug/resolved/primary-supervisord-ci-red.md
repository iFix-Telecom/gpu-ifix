---
status: resolved
trigger: "6 integration tests fail in build-gateway CI (run 27630470565, develop, 2026-06-16) gating ALL deploys since ~06-14. Build/push/redeploy steps SKIPPED while integration job red. Probe fix 844bf2a + any pod fix cannot ship. DIAGNOSE ONLY — no speculative edits (prod-dev impact)."
created: 2026-06-16
updated: 2026-06-16
goal: find_root_cause_only
---

# Debug Session: primary-supervisord-ci-red

## Current Focus

ROOT CAUSE FOUND — diagnose-only complete.

reasoning_checkpoint:
  hypothesis: |
    Commit f8a7de4 added a 4th fail-fast SHA gate in buildCreateRequest
    (ErrMissingChatterboxSHA when PrimaryChatterboxWeightsSHA256 == "") and updated
    the UNIT test helper cfgWithDefaults() (lifecycle_test.go) to set the new SHA, but
    did NOT update the parallel INTEGRATION test helper primaryTestCfg()
    (primary_helpers_test.go). At integration-test runtime PrimaryChatterboxWeightsSHA256
    is empty → buildCreateRequest fails → provisionLifecycle returns err → spawnProvisioning's
    goroutine bounces the FSM back to Asleep (reconciler.go:1086 SetState(StateAsleep)).
    All 6 tests assert the FSM leaves Asleep (→Provisioning/→Ready) and never see it.
  confirming_evidence:
    - "git bisect (good 8d676b8c / bad HEAD) → first bad commit = f8a7de4 deterministically."
    - "f8a7de4 added `if cfg.PrimaryChatterboxWeightsSHA256 == \"\" { return ..., ErrMissingChatterboxSHA }` at lifecycle.go:361."
    - "f8a7de4 added PrimaryChatterboxWeightsSHA256 to UNIT helper cfgWithDefaults (lifecycle_test.go:70) but `grep Chatterbox internal/integration_test/` = EMPTY (integration helper never updated)."
    - "Local repro (sudo+Docker, -tags=integration) reproduces CI exactly: same 5 FAIL + 2 PASS, identical 'got asleep' messages."
    - "Green run 80417702795 (06-09, 8d676b8c): all 6 PASSED fast (ForceUp 1.29s, 4Services 6.24s). Now ForceUp runs full 6s never satisfying, 4Services times out at 20s."
    - "All 6 failures share ONE shape: FSM stuck Asleep ('got asleep' / 'must transition Asleep→Provisioning'). NoEvent_StaysAsleep PASSES (leadership + schedule loop fine)."
    - "buildCreateRequest reached via provisionLifecycle (reconciler.go:1205); on err closeLifecycle+return → goroutine SetState(StateAsleep) reconciler.go:1086."
  falsification_test: "Add cfg.PrimaryChatterboxWeightsSHA256 = \"...\" to primaryTestCfg() → all 6 tests pass. (NOT APPLIED — diagnose-only.)"
  fix_rationale: |
    Root cause is test-fixture drift, NOT a product regression. The integration helper
    primaryTestCfg() must set cfg.PrimaryChatterboxWeightsKey + cfg.PrimaryChatterboxWeightsSHA256
    (parity with the unit cfgWithDefaults() and with the other 3 weights it already sets).
    Field exists in config.Config (config.go:220-221) so it compiles. ONLY the test file
    primary_helpers_test.go changes — NO gateway/ product code touched (no prod-dev impact).
  blind_spots: |
    Did not yet confirm whether other -tags=integration tests that call primaryTestCfg AND
    drive provisioning also pass once fixed (likely yes — they reach Ready in green). The 6th
    test (Autorestart) timing not captured in truncated local run but shares identical mechanism.
  next_action: "Return ROOT CAUSE FOUND. Fix = add Chatterbox Key+SHA to primaryTestCfg in primary_helpers_test.go (test-only). NOT applied per diagnose-only constraint."

## Symptoms

- **Expected:** integration job green → `Build & push gateway image` + `Trigger Portainer redeploy (dev/prod)` run → images ship.
- **Actual:** integration job FAILS (284s, exit 1) → those steps SKIPPED → nothing deployed to prod since ~2026-06-14. Deployed gateway = rev 99e4e09 (06-14); probe fix 844bf2a NOT in prod.
- **Failing tests (gateway/internal/integration_test, testcontainers-go):**
  - TestPrimaryCancelInflight_TripleLayer (6.09s) — assertion-shaped
  - TestPrimaryDisabled_ForceUpRequest_Provisions (7.18s) — assertion-shaped
  - TestPrimaryProbe_MarkReady_OverridesTier03Roles_4EndpointsReachable (20.09s) — timeout-shaped
  - TestSupervisord_4ServicesReachableOnLocalhost (20.08s) — timeout-shaped, NEW test (not in 05-18 session)
  - TestSupervisord_OneEndpointDown_DoesNotPromoteToReady (5.09s) — assertion-shaped, NEW
  - TestSupervisord_AutorestartSimulated_RecoveryAfterTransientFailure (25.09s) — timeout-shaped, NEW
- **Timeline:** all build-gateway runs (06-14, 06-16, develop+main) = failure. Prior debug session `primary-integration-tests-ci.md` (05-18) RESOLVED a DIFFERENT failure set (freshSchema contamination + Pitfall #11); it recorded TestPrimaryCancelInflight_TripleLayer + TestPrimaryProbe_* PASSING after that fix — so they regressed since. TestSupervisord_* appear newer. Phase 12 verified PASSED 06-13 via local `go test ./internal/...` — but the testcontainers integration_test pkg is likely SKIPPED locally without Docker, so it passed verification while red in CI.
- **Repro:** `cd gateway && go test ./internal/integration_test/ -run 'TestPrimary|TestSupervisord' -count=1 -v` (needs Docker + testcontainers).

## Investigation leads (from pre-debug recon)

1. Distinguish real regression (5-7s assertion failures: CancelInflight, ForceUpRequest, OneEndpointDown) vs CI-env/timing (20-25s timeout-shaped: MarkReady, 4Services, Autorestart).
2. Run locally with Docker → pass local + fail CI = environment; fail local = real bug.
3. Bisect: `gh run list --workflow build-gateway` → last GREEN run → its commit → diff to HEAD. Focus on primary reconciler + supervisord + pod commits cc4b07d/f8a7de4/8bf983b (06-13: baked chatterbox + mc + openssh into pod image; check if supervisord service list / ports / readiness contract changed).
4. Confirm whether TestSupervisord_* were EVER green in CI or always failing since introduction.
5. Cross-link SEED-014 (live cold-start health_timeout loop) + SEED-015 (this CI block). If the supervisord 4-endpoint-readiness contract regressed, it would explain BOTH.

## CI references

- Failing run: 27630470565 (build-gateway, develop, 2026-06-16). Integration job: 81704965998.
- Workflow: .github/workflows/build-gateway.yml. gh PAT in CLAUDE.md.
- Prior resolved session (different failures, same subsystem): .planning/debug/primary-integration-tests-ci.md

## Evidence

- timestamp 2026-06-16: 6 FAIL lines + `FAIL ...integration_test 284.244s` + exit 1 (run 27630470565). Unit job + sqlc codegen = success; only integration job red.
- checked: CI failure-message shape (run 27630470565 log lines 646-722). found: ALL 6 share ONE failure mode — FSM never leaves Asleep. ForceUp "got asleep"; MarkReady/4Services/Autorestart "must reach Ready... got asleep"; CancelInflight/OneEndpointDown "must transition Asleep→Provisioning". implication: prior hypothesis (assertion-shape REAL vs timeout-shape FLAKE) REFUTED — single common cause. TestPrimaryDisabled_NoEvent_StaysAsleep PASSES (leadership + schedule loop OK).
- checked: local repro `sudo go test -tags=integration ... -run 'TestPrimary|TestSupervisord'` (host Docker). found: IDENTICAL to CI — 5 FAIL + 2 PASS, same 'got asleep' messages. implication: NOT a CI-env/flake; deterministic REAL regression (reproduces on this host).
- checked: last GREEN build-gateway run. found: 27232634386 / job 80417702795 (develop, sha 8d676b8c, 06-09) — integration green; all 6 now-failing tests PASSED there (ForceUp 1.29s, CancelInflight 1.44s, 4Services 6.24s, OneEndpointDown 9.18s, MarkReady 6.23s, Autorestart 18.23s). implication: regression introduced in 8d676b8c..HEAD; tests were genuinely green, not skipped.
- checked: are failing test files changed since green? found: `git diff --stat 8d676b8c..HEAD -- <4 test files>` = EMPTY. implication: regression is in NON-test source only.
- checked: git bisect (good 8d676b8c, bad HEAD) running TestPrimaryDisabled_ForceUpRequest_Provisions. found: first bad commit = **f8a7de4** "fix(pod): pre-provision chatterbox TTS model to MinIO (offline load)". implication: exact regression point isolated.
- checked: f8a7de4 diff. found: added `ErrMissingChatterboxSHA` + `if cfg.PrimaryChatterboxWeightsSHA256 == "" { return ..., ErrMissingChatterboxSHA }` at lifecycle.go:361 (4th fail-fast gate); added PrimaryChatterboxWeightsKey/SHA256 to config.Config + Load(); updated UNIT helper cfgWithDefaults (lifecycle_test.go:69-70) with the new SHA. implication: a new mandatory config field was introduced.
- checked: `grep Chatterbox gateway/internal/integration_test/`. found: EMPTY — integration helper primaryTestCfg (primary_helpers_test.go) sets Qwen/Whisper/BGEM3 SHAs but NOT Chatterbox. implication: ROOT CAUSE — unit fixture updated, integration fixture NOT. buildCreateRequest fail-fast fires only in integration_test → FSM bounces to Asleep (reconciler.go:1086).
- checked: SEED-014 cross-link. found: deployed prod gateway (n8n-ia-vm `ifix-ai-gateway`, rev 99e4e09) HAS PRIMARY_CHATTERBOX_WEIGHTS_SHA256=c47cd41e... set in env. implication: live SEED-014 cold-start health_timeout loop is NOT this bug (prod gate passes). "ONE root cause for both" hypothesis REFUTED — CI-red (SEED-015) and live-loop (SEED-014) are DISTINCT root causes.

## Eliminated

- hypothesis: CI-environment difference or timing flake (the 20-25s timeout-shaped tests).
  evidence: Reproduces identically on local host with Docker (5 FAIL + 2 PASS, same messages). Deterministic, not environmental.
  timestamp: 2026-06-16
- hypothesis: supervisord 4-endpoint-readiness contract regressed (service list / ports changed in pod-image commits cc4b07d/8bf983b).
  evidence: bisect isolates f8a7de4 (a config/fail-fast change), not a supervisord-contract change; tests fail BEFORE any endpoint probe because provisioning aborts at buildCreateRequest.
  timestamp: 2026-06-16
- hypothesis: assertion-shaped (5-7s) failures are a different/real bug from timeout-shaped (20-25s) failures.
  evidence: All 6 share one mechanism (FSM stuck Asleep from buildCreateRequest fail-fast). The shape difference is only the assertion's wait budget, not distinct causes.
  timestamp: 2026-06-16
- hypothesis: ONE root cause explains both the CI red AND the live SEED-014 cold-start loop.
  evidence: Prod gateway env HAS the chatterbox SHA set; SEED-014 failures are pod-side (TTS endpoint never healthy + ghcr TLS), unrelated to the test-fixture gap.
  timestamp: 2026-06-16

## Resolution

root_cause: |
  Test-fixture drift (NOT a product regression). Commit f8a7de4 (2026-06-13) added a 4th
  fail-fast SHA gate in buildCreateRequest — returns ErrMissingChatterboxSHA when
  cfg.PrimaryChatterboxWeightsSHA256 == "". It updated the UNIT test helper cfgWithDefaults()
  (lifecycle_test.go) to set the new SHA but did NOT update the parallel INTEGRATION test
  helper primaryTestCfg() (primary_helpers_test.go). In integration_test the field stays empty
  → buildCreateRequest fails on every provision attempt → provisionLifecycle returns err →
  spawnProvisioning's goroutine SetState(StateAsleep) (reconciler.go:1086). All 6 tests assert
  the FSM leaves Asleep and never observe it. Unit job passes (helper updated); integration job
  fails (helper not updated); Phase-12 local verify passed because integration_test is skipped
  without -tags=integration + Docker.
fix: |
  PROPOSED (NOT applied — diagnose-only). Add to primaryTestCfg() in
  gateway/internal/integration_test/primary_helpers_test.go (after the BGEM3 lines ~63-64):
    cfg.PrimaryChatterboxWeightsKey = "chatterbox-mtl-v2/v1.0.0/cache.tar.gz"
    cfg.PrimaryChatterboxWeightsSHA256 = "ch4tt3rb0xsh4test256"
  (parity with unit cfgWithDefaults lifecycle_test.go:69-70). TEST-ONLY change — no gateway/
  product code touched, zero prod-dev impact. Unblocks CI → probe fix 844bf2a can ship.
verification: |
  PENDING (no fix applied). Falsification: with the two lines added, all 6 tests pass locally
  via `sudo go test -tags=integration ./gateway/internal/integration_test/ -run 'TestPrimary|TestSupervisord'`.
files_changed: []
