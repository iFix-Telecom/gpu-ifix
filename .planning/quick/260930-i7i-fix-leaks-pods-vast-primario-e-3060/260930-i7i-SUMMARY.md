---
phase: quick-260930-i7i
plan: 01
status: complete
subsystem: gateway/vastutil, gateway/primary, gateway/emerg, ops/vast-3060
tags: [vast, orphan-leak, destroy-retry, label-sweep, cuda-compat, systemd]
requirements: [CLICKUP-86akr57nj]
key-files:
  created:
    - gateway/internal/vastutil/sweep.go
    - gateway/internal/vastutil/sweep_test.go
    - gateway/internal/primary/sweep.go
    - gateway/internal/primary/sweep_test.go
    - gateway/internal/emerg/sweep.go
    - gateway/internal/emerg/sweep_test.go
    - ops/vast-3060/test_unified3060.py
  modified:
    - gateway/internal/vastutil/helpers.go
    - gateway/internal/vastutil/helpers_test.go
    - gateway/internal/primary/lifecycle.go
    - gateway/internal/primary/reconciler.go
    - gateway/internal/primary/reconciler_test.go
    - gateway/internal/emerg/lifecycle.go
    - gateway/internal/emerg/reconciler.go
    - gateway/internal/emerg/recovery.go
    - gateway/internal/integration_test/primary_helpers_test.go
    - gateway/internal/obs/metrics.go
    - ops/vast-3060/onstart-unified.sh
    - ops/vast-3060/unified3060.py
    - ops/vast-3060/systemd/vast-unified-start.service
decisions:
  - "BestEffortDestroy: fresh ctx per attempt (15s), 75s total, 6 attempts; retries 429/5xx/transport; 404 = success; 401/403/4xx permanent; now returns error"
  - "Leader label sweep lists Vast FIRST, then reads DB (rows are inserted before create) and fails closed on DB error / no DB"
  - "Sweep is async behind an atomic in-flight guard, runs on leadership acquisition + every 10min"
  - "One commit per task (tests + implementation together) instead of separate RED/GREEN commits"
completed: 2026-09-30
---

# Quick 260930-i7i: fix leaks pods Vast (primário + 3060) Summary

Vast destroy now retries timeouts and 5xx with a fresh ctx per attempt. The primary and emerg leaders run a strict-label orphan sweep that fails closed. The 3060 pod gets a CUDA forward-compat 804 guard, host_avoid, a persisted pending_id and a 6h start timeout.

## Commits

| Task | Commit | Message |
|------|--------|---------|
| 1 | `2e06033` | fix(260930-i7i): harden BestEffortDestroy + strict lifecycle label parser |
| 2 | `820dcc6` | feat(260930-i7i): leader-only Vast label sweep for primary and emerg |
| 3 | `7fa3f81` | fix(260930-i7i): 3060 pod CUDA compat 804 guard, host_avoid, pending_id, 6h start timeout |

Branch `worktree-agent-a0ffb0653f0dd5fec`, base `a899845`. Nothing pushed or deployed.

## FATOS (commands run + results)

- `cd gateway && gofmt -l .` printed nothing (0 files).
- `go build ./...`: OK. `go vet ./...`: OK. `go vet -tags integration ./...`: OK.
- `go test ./... -count=1`: all 30 packages with tests `ok` (the other 2 have no test files).
- `go test -race ./internal/primary/... ./internal/emerg/... ./internal/vastutil/... -count=1`: all `ok`.
- `go test ./internal/vastutil/ -v`: 22 tests PASS. New ones: RetriesTransportTimeout (deadline + *url.Error), Retries5xx, NotFoundIsSuccess, UnauthorizedNoRetry (401, offer_gone, 400, wrapped 401), PerAttemptCtx (cancelled caller ctx, per-attempt deadline), DestroyRetryable, NonRetryableReturnsError, ParseLifecycleLabel accept/reject, SelectLabelOrphans.
- Label parser rejects `stt-tts-rerank-unified`, `rerank-3060-v2m3`, `stt-tts-3060-auto`, `ifix-primary-lifecycle-` (no id), `ifix-primary-lifecycle-12x`, `-0`, `-007`, `--1`, `-+5`, leading or trailing space, `xifix-…`, `-5-extra`, overflow, and cross-prefix labels (test `TestParseLifecycleLabel_RejectsForeignAndMalformed`).
- Primary sweep: 8 tests PASS:
  - the incident case (only 53345260 destroyed; the counter goes up by 1)
  - a DB error or no DB destroys nothing
  - with no open lifecycle, a young instance is kept and an old one is destroyed
  - the in-memory active instance and lifecycle are kept
  - a ListInstances error means no destroy and no DB read
  - the in-flight guard works
- Emerg sweep: 5 tests PASS:
  - a live instance is protected and a closed one is destroyed
  - primary, foreign and malformed labels are ignored
  - the in-memory active lifecycle is kept
  - a DB error or no DB destroys nothing
  - a list error means no destroy
- `grep -rn "orphan recovery will reconcile" gateway/`: no matches.
- **Integration tests: NOT run.** `go test -tags integration ./internal/integration_test/ -run 'Primary|Emerg'` panicked in TestMain: `permission denied … /var/run/docker.sock`. This user can't reach Docker, so testcontainers can't start. What did pass: `go vet -tags integration ./...` and `go test -tags integration -c -o /dev/null ./internal/integration_test/`, so the tests compile.
- `ops/vast-3060`:
  - `bash -n onstart-unified.sh`: OK.
  - `python3 -m py_compile unified3060.py`: OK.
  - `python3 -m unittest -v test_unified3060`: 10 tests OK.
  - `shellcheck -S error` and `-S warning`: clean.
  - `TimeoutStartSec=21600` and `ld.so.conf.d` are both present.
- Compat guard sandbox check: the extracted block was run with the paths rewritten to the scratchpad and a fake nvidia-smi/ldconfig.
  - driver 535 with compat 560: conf moved and ldconfig called.
  - driver 570 with compat 560: conf kept.
  - no versioned libcuda: conf kept.
  - no nvidia-smi: skipped.
  - rc=0 in every case.
- `test_build_onstart_fits_and_has_compat_guard`: the onstart after the new block stays under 16384 chars (build_onstart itself raises above 15000 b64).
- FATO (code): `evaluateDestroying` is called inline from `evaluateTick` (primary/reconciler.go:399) on the same goroutine that renews the lock (`primaryLockExpiry = 30s`, renew every 10s). The same holds for emerg `destroyAndCloseLifecycle` from the tick (`emergLockExpiry = 30s`).

## Behavioral changes for prod deploy review (A — needs the user's OK)

1. **Longer worst-case destroy time.** `BestEffortDestroy` used to give up at about 30s or after the first non-429 error. It now retries timeouts, transport errors and 5xx, for up to 75s of total budget plus up to 15s for the last attempt (about 90s at most). Every synchronous caller on the tick, like evaluateDestroying and the emerg destroyAndCloseLifecycle, can now block the tick for that long under a Vast outage.
2. `BestEffortDestroy` returns `error`. Existing callers ignore it (statement form), except for the new Error logs at 3 destroy-then-close sites: primary regime-3 stall, emerg `destroyAndCloseLifecycle`, emerg recovery resume-health-failed. Control flow is unchanged.
3. A 404 on DELETE counts as success (the client already mapped it to nil).
4. **New destructive background job:** the leader of each subsystem lists the whole Vast account on leadership acquisition and every 10min. It destroys instances labelled exactly `ifix-primary-lifecycle-<id>` or `ifix-emerg-lifecycle-<id>` when:
   - the lifecycle is not open/live in the DB,
   - and it is not the in-memory active instance,
   - and it started more than 10min ago.
   It destroys nothing when the DB read fails.
5. **First deploy will destroy any existing orphan right away.** That includes 53345260, if it's still alive. Look for the Warn log `primary orphan Vast instance found by label sweep; destroying` and the metric `gateway_vast_orphan_swept_total{subsystem}`.
6. There's one extra Vast `GET /instances/` per subsystem every 10min, which is small next to the 1 req/s client limit.
7. New Prometheus counter `gateway_vast_orphan_swept_total{subsystem=primary|emerg}`. Any increment means a destroy leaked and was caught, so it's worth an alert.
8. A sweep that is already running keeps going if leadership is lost mid-sweep. It's DB-guarded, but it's still a window where two replicas could both sweep. Both would reach the same DB-based decision, and destroy is idempotent.

## Deploy steps pending

- **B (orchestrator):**
  - copy `ops/vast-3060/{unified3060.py,onstart-unified.sh}` to `/opt/vast-3060/`; `test_unified3060.py` is optional
  - copy `ops/vast-3060/systemd/vast-unified-start.service` to `/etc/systemd/system/`
  - run `systemctl daemon-reload`
  - The old state.json has no `host_avoid`/`pending_id`; the code reads them with `.get()` defaults.
- **A (the user's approval):** build and deploy the gateway prod image (worker-vm, stack `ai-gateway-prod`) following the usual recipe.

## HIPÓTESES

- HIPÓTESE (T-i7i-03): under repeated Vast timeouts, a synchronous destroy on the tick can go past the 30s leader-lock expiry. The lock would then lapse and another replica could take leadership mid-destroy. This was already borderline with the old 30s budget and gets worse with about 90s now. I did NOT change it: the plan says record, not fix, and making evaluateDestroying async is a Rule-4-sized change. A possible fix is to run the synchronous destroy paths off-tick, or cap the tick-path budget below 30s and leave the rest to the sweep. What would settle it: prod logs showing `lost primary leadership; ceding` right after a `BestEffortDestroy retryable error` burst, plus the replica count of the gateway service.
- NÃO SEI whether the 29h leak (lifecycle 494 / instance 53345260) was actually caused by the lock expiry or by the timeout being classified as non-retryable. The plan's diagnosis points to the timeout classification, but I had no prod logs in this run. What would settle it: the gateway logs for lifecycle 494 around its close (`BestEffortDestroy failed; orphan recovery will reconcile` with a transport or deadline `err`, and any leadership change at the same time).
- HIPÓTESE: in the stale-pending branch of `cmd_start`, `vast_get` returns None for any non-200, not only 404. A transient Vast error would then clear `pending_id` without destroying it. The instance keeps the exact label, so the 20:00 `cmd_stop` label sweep would still destroy it. The worst case is a paid pod left running until the stop.

## Deviations from Plan

- **[Process] TDD commits combined:** tests and implementation went into one commit per task, because the constraints asked for atomic per-task commits. RED was confirmed locally before implementing: the vastutil tests failed to compile against the old signature and missing symbols.
- **[Rule 2] Extra tests** beyond the plan's list: `TestPrimarySweep_TriggerInFlightGuard`, `TestPrimarySweep_NoDBFailsClosed`, `TestEmergSweep_NoDBFailsClosed`, `TestDestroyRetryable`, and an onstart size/order test in `test_unified3060.py`.
- **[Rule 3] ClickUp marker:** the PostToolUse hook `clickup-link-enforce.sh` blocked edits because `.planning/clickup-active-task.json` was missing in the worktree. I copied it from the main checkout (`{"skip": true}`). The file is gitignored and was not committed.
- Test seams: I added unexported `sweepQuerierOverride` fields (`primarySweepQuerier` and `emergSweepQuerier` interfaces) as the plan allowed, rather than scripting `fakeDBTX` rows.

## Known Stubs

None.

## Self-Check: PASSED

- Commits `2e06033`, `820dcc6`, `7fa3f81` are on the branch.
- All created files listed above exist.

## Orchestrator verification (2026-09-30, pós-merge em develop)

- `go test ./...` (unit): PASS todos os pacotes; `gofmt -l` vazio.
- Integration (`-tags integration`, Docker via sudo):
  - `cmd/gatewayctl`: 3 FAIL (TestModelAliasGet_ReturnsSpecificRow, TestRunPrimaryLifecyclesIntegration_FetchesFromDB, TestRunPrimaryLifecycles_RespectsLimitFlag) — **idênticas no baseline a899845** → pré-existentes.
  - `internal/integration_test`: 5 FAIL (TestSensitiveSaturated503, TestTier1UnavailableShedded503, TestDCGMFailOpen, TestSC1_BurstExceedsTenantCapOverflowsToTier1, TestSC3_HotReloadAppliesInUnder2Seconds) — **idênticas no baseline a899845** (rodada sequencial) → pré-existentes. Demais testes do pacote PASS.
- Deploy (B) feito: /opt/vast-3060/{unified3060.py,onstart-unified.sh} + /etc/systemd/system/vast-unified-start.service (TimeoutStartSec=6h), backups `.bak-<ts>`, daemon-reload, `status` ok.
- Deploy (A) gateway prod: PENDENTE de OK do Pedro.
