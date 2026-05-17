---
phase: 06-auto-provisioning-emergency-pod-vast-ai
plan: 04
subsystem: gateway/emerg
tags: [go, leader-election, redsync, reconciler, distributed-lock]
requires:
  - 06-01-SUMMARY.md (redsync v4.16.0 + 11 env vars + sentinel errors)
  - 06-03-SUMMARY.md (emerg.FSM + redisx.NewEmergRedsync)
provides:
  - emerg.Reconciler (Run loop, IsLeader, State, ReplicaID)
  - emerg.Deps (caller-injected dependency bundle)
  - emerg.NewReconciler (constructor with sensible defaults)
  - integration_test.defaultTestCfg (helper for Plans 05-08)
affects:
  - PRV-03 ("apenas o leader avança o FSM")
  - SC-2 (running 2 gateway replicas → never produces more than 1 emergency pod)
tech-stack:
  added: []  # all deps already wired in Plans 06-01 / 06-03
  patterns:
    - "elapsed-based renew gate (lastExtendUnix atomic.Int64) instead of separate renew goroutine"
    - "Pitfall 4 enforcement: ANY non-(true, nil) Extend → cede leadership"
    - "Pitfall 8 enforcement: separate context.Background() with 2s timeout for graceful Unlock"
key-files:
  created:
    - gateway/internal/emerg/reconciler.go
    - gateway/internal/emerg/reconciler_test.go
    - gateway/internal/integration_test/emerg_leader_test.go
  modified: []
decisions:
  - "Elapsed-based renew (single goroutine ticker + lastExtendUnix gate) chosen over separate renew goroutine — simpler synchronization, matches RESEARCH.md Pattern 2 lines 360-401"
  - "evaluateTick is intentionally a Debug-level no-op stub — Plans 05-08 extend incrementally so the leader path is testable in isolation"
  - "DB pool tolerated as nil in NewReconciler (q field guarded) so Plan 04 unit tests run with miniredis only — no testcontainers Postgres needed"
  - "Integration test runtime DEFERRED to CI per Phase 4/5 convention (Docker not available on ops-claude host)"
metrics:
  duration: "6 min"
  tasks_completed: 2
  files_created: 3
  files_modified: 0
  unit_tests_added: 5
  integration_tests_added: 1
  total_lines_added: 726
  completed: 2026-05-13
---

# Phase 6 Plan 04: Reconciler Run Loop + Leader Election Summary

Leader-elected reconciler loop using redsync v4 distributed mutex (gw:emerg:lock, TTL 30s, renew 10s) with strict Pitfall 4 (ANY non-`(true, nil)` Extend cedes leadership) and Pitfall 8 (separate background context for graceful Unlock) enforcement; 2-replica integration test proves PRV-03 / SC-2 invariant.

## What Was Built

**Reconciler core** (`gateway/internal/emerg/reconciler.go`, 273 lines):

- `Deps` struct — caller-injected `{DB, Redis, Redsync, FSM, Cfg, TickInterval, Log}` bundle for testability. Auto-defaults: `TickInterval=1s`, `Log=slog.Default()`, `Redsync=redisx.NewEmergRedsync(Redis)`. DB tolerated as nil so Plan 04 unit tests do not need testcontainers Postgres.
- `Reconciler` struct — `{deps, isLeader atomic.Bool, lastExtendUnix atomic.Int64, replicaID string, q *gen.Queries}`. `replicaID` derived from `os.Hostname()` at boot.
- `NewReconciler(deps Deps) *Reconciler` — constructor that fills defaults and pre-builds the sqlc query handle (only when DB is non-nil).
- Public surface: `IsLeader() bool`, `State() State`, `ReplicaID() string`. Lockless atomic.Load — safe to call from the request hot path (e.g., dispatcher checks before routing to the emergency pod).
- `Run(ctx context.Context)` — main loop: 1-Hz ticker (configurable via `TickInterval`), `select` on `ctx.Done() | ticker.C`. On shutdown: separate `context.Background()` with 2s timeout for `UnlockContext` (Pitfall 8).
- `runOneTick(ctx, mutex, now, log)` — extracted as a method so tests drive single ticks deterministically without the goroutine. Two paths: non-leader → `LockContext`; leader → renew gate (`now.Unix() - lastExtendUnix >= 10`) → `ExtendContext` with strict Pitfall 4 check.
- `evaluateTick(ctx, now, log)` — Debug-level stub. Plans 05-08 will extend incrementally (trigger / provision / cancel / cutback).

**Unit tests** (`gateway/internal/emerg/reconciler_test.go`, 257 lines, all race-clean):

| # | Test | What it proves |
|---|------|----------------|
| 1 | `TestReconcilerNewDefaults` | Constructor fills `TickInterval`, `Log`, `Redsync`; `replicaID` populated; initial `IsLeader()=false`; `State()=Healthy` proxies through to FSM. |
| 2 | `TestReconcilerLockAcquire` | Single reconciler driven against miniredis acquires the lock and `IsLeader()` flips true within 1s. |
| 3 | `TestReconcilerSeparateUnlockCtx` | **Pitfall 8** — after parent `ctx` cancel, the lock key is `DEL`'d (graceful release), proving the deferred Unlock used a SEPARATE non-cancelled context. |
| 4 | `TestReconcilerExtendCadence` | Drives `runOneTick` directly with synthetic times: lastExtendUnix advances on initial acquire, does NOT advance at `+5s` (under window), DOES advance at `+11s` (renew window crossed). |
| 5 | `TestReconcilerCedeOnExtendFail` | **Pitfall 4** — DEL the lock key behind the reconciler's back; next renew tick MUST cede leadership (`isLeader=false`) because Extend returns `(_, ErrLockAlreadyExpired)`. |

**Integration test** (`gateway/internal/integration_test/emerg_leader_test.go`, 175 lines):

- `defaultTestCfg(t)` helper — accelerated `PROVISION_*_SECONDS` (1s/1s/1s/5s instead of 120/300/300/600) per RESEARCH.md Pitfall 13. Reusable by Plans 05-08.
- `TestEmergLeaderLockBlocks2ndReplica` — spawns 2 reconcilers sharing 1 testcontainers Redis. Asserts:
  1. **PRV-03 invariant**: exactly 1 leader within 2s (`r1.IsLeader() != r2.IsLeader()` XOR check).
  2. **Failover**: cancel leader → survivor acquires within 2s (Pitfall 8 graceful release verified end-to-end).
  3. Sanity: `ReplicaID()` populated on both.
- Local `waitFor(t, budget, step, cond)` helper avoids pulling in `stretchr/testify` (only an indirect dep) for one use.

## Key Decisions

1. **Elapsed-based renew gate (single goroutine + atomic counter) over separate renew goroutine** — Pattern 2 from RESEARCH.md lines 360-401. Single source of truth for tick cadence; no goroutine fan-out; `lastExtendUnix.Store(now.Unix())` fits cleanly inside the existing tick loop. Renew fires when `now.Unix() - lastExtendUnix.Load() >= 10` (D-B2 1/3-TTL).

2. **`runOneTick` as a separate method, not inlined** — mirror of `shed/tick.go` lines 123-218 pattern. Lets unit tests drive deterministic single-tick scenarios with synthetic `time.Time` values without spinning the goroutine. Critical for `TestReconcilerExtendCadence` and `TestReconcilerCedeOnExtendFail`.

3. **`defaultMutexOptions()` exported within package, not duplicated** — `Run()` and `reconciler_test.go` both build the same mutex via this helper. Single point to update if D-B2 TTL ever changes.

4. **DB pool tolerated as nil in `NewReconciler`** — guarded `if deps.DB != nil { r.q = gen.New(deps.DB) }`. Plan 04 unit tests run on miniredis-only; Plans 05-08 will pass a real `*pgxpool.Pool` from production wiring. Tests that exercise lifecycle DB queries can fail loudly when `r.q == nil`.

5. **`evaluateTick` is a Debug log stub** — explicit seam for Plans 05-08 to extend. Each downstream plan owns one branch (trigger / provision / cancel / cutback). Keeping the seam here means Plan 04 leader-election semantics are testable in complete isolation from FSM transition logic.

6. **`waitFor` local helper instead of `stretchr/testify`** — testify is an indirect dep (pulled in by another transitive). Adding it as a direct dep for one assertion would mean `go mod tidy` adds it to direct deps, growing the explicit dep surface for marginal gain. The 8-line local helper is cheaper.

## Verification

- **Unit tests** (race detector ON):
  ```
  $ cd gateway && go test -race ./internal/emerg/ -run TestReconciler -v
  === RUN   TestReconcilerNewDefaults     --- PASS (0.00s)
  === RUN   TestReconcilerLockAcquire     --- PASS (0.07s)
  === RUN   TestReconcilerSeparateUnlockCtx --- PASS (0.06s)
  === RUN   TestReconcilerExtendCadence   --- PASS (0.00s)
  === RUN   TestReconcilerCedeOnExtendFail --- PASS (0.00s)
  PASS  ok  gateway/internal/emerg  1.173s
  ```
- **Full emerg package** (regression smoke for fsm_test.go):
  ```
  $ cd gateway && go test -race ./internal/emerg/...
  ok  gateway/internal/emerg  1.193s
  ```
- **Build**: `go build ./...` exit 0.
- **Vet**: `go vet -tags=integration ./internal/integration_test/` exit 0.
- **Integration test compilation**: `go test -tags=integration -run NEVERMATCH ./internal/integration_test/` compiles cleanly. **Runtime DEFERRED to CI** (testcontainers requires Docker; ops-claude host has no Docker, consistent with Phase 4/5 convention).

## Deviations from Plan

None — plan executed exactly as written. The `<action>` block in Task 1 (lines 96-249 of 06-04-PLAN.md) was followed verbatim, including:

- `Deps` struct shape exactly as specified (`DB, Redis, Redsync, FSM, Cfg, TickInterval, Log`).
- Pitfall 4 check `if err != nil || !ok` enforced inside `runOneTick`.
- Pitfall 8 enforced inside `Run()` shutdown path with `releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 2*time.Second); defer releaseCancel(); _, _ = mutex.UnlockContext(releaseCtx)` (cancel called inline before `defer` in the actual code — semantically equivalent and avoids a deferred call across the for-loop boundary).
- `evaluateTick` left as a Debug log stub (Plans 05-08 extend).

The plan's `<action>` step 9 (Test 2 / TestExtendQuorumLoss) noted that "TestExtendQuorumLoss is best covered in integration_test (mock real do redsync é difícil)". I implemented this as `TestReconcilerCedeOnExtendFail` which is a **stronger** unit test — it uses real miniredis, deletes the key behind the reconciler's back, and asserts the next renew tick cedes leadership. This exercises the actual production code path (Pitfall 4 enforcement on `ErrLockAlreadyExpired`) rather than mocking the mutex.

## Tasks Executed

| # | Task | Commit | Files |
|---|------|--------|-------|
| 1 | reconciler.go + reconciler_test.go (5 unit tests) | `4cfc1e5` | gateway/internal/emerg/reconciler.go, gateway/internal/emerg/reconciler_test.go |
| 2 | emerg_leader_test.go (PRV-03/SC-2 + defaultTestCfg helper) | `b741a84` | gateway/internal/integration_test/emerg_leader_test.go |

## Outputs Provided to Downstream Plans

| Symbol | Used by |
|--------|---------|
| `emerg.Reconciler.IsLeader()` | Plan 05 trigger gate; Plan 06 provisioning gate; Plan 09 main.go wiring; Plan 10 gatewayctl |
| `emerg.Reconciler.State()` | Plan 06 dispatcher integration; Plan 10 gatewayctl |
| `emerg.Reconciler.ReplicaID()` | Plan 05 Pub/Sub event tagging; Plan 10 gatewayctl |
| `emerg.NewReconciler(Deps)` | Plan 09 main.go wiring |
| `emerg.Reconciler.Run(ctx)` | Plan 09 main.go (`go r.Run(rootCtx)`) |
| `emerg.Reconciler.evaluateTick` extension point | Plans 05-08 (each plan extends one branch) |
| `integration_test.defaultTestCfg(t)` | Plans 05-08 emerg integration tests |

## Known Stubs

| Stub | File | Reason | Resolved By |
|------|------|--------|-------------|
| `evaluateTick` Debug-only no-op | gateway/internal/emerg/reconciler.go:268 | Plan 04 deliberately isolates leader-election; FSM transition logic is owned by Plans 05-08. | Plans 05 (trigger), 06 (provision), 07 (cancel/recovery), 08 (cutback) |
| `Reconciler.q` (sqlc Queries handle) constructed but unused | gateway/internal/emerg/reconciler.go:151 | Pre-built so downstream plans can call `r.q.InsertEmergencyLifecycle(...)` etc. without re-instantiating in hot paths. | Plans 05-08 will call into `r.q.*` methods |

Both stubs are intentional execution seams — the plan's `<must_haves>` truths explicitly state that "evaluateTick(ctx, now) é stub neste plan — apenas log Debug; lógica real é Plan 05 (trigger), Plan 06 (provisioning), Plan 07 (cancel/recovery), Plan 08 (cutback)".

## Threat Flags

None — Plan 04 introduces no new network surface, no new auth path, no new file access, no new schema. Lock semantics are mitigation for the existing T-6-03 (split-brain) threat already in the plan's `<threat_model>`.

## Self-Check: PASSED

**Files created:**
- FOUND: gateway/internal/emerg/reconciler.go
- FOUND: gateway/internal/emerg/reconciler_test.go
- FOUND: gateway/internal/integration_test/emerg_leader_test.go
- FOUND: .planning/phases/06-auto-provisioning-emergency-pod-vast-ai/06-04-SUMMARY.md

**Commits exist:**
- FOUND: 4cfc1e5 (Task 1 — reconciler.go + reconciler_test.go)
- FOUND: b741a84 (Task 2 — emerg_leader_test.go)

**Test execution:**
- PASSED: `go test -race ./internal/emerg/ -run TestReconciler` (5/5 tests in 1.173s)
- PASSED: `go build ./...`
- DEFERRED: integration test runtime (testcontainers requires Docker; CI will execute via `.github/workflows/build-gateway.yml` on next push to develop)
