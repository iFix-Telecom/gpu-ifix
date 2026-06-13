---
slug: primary-reconciler-silent-hang
status: resolved
goal: find_root_cause_only
tdd_mode: false
trigger: phase-11-uat-2026-05-27
started_at: 2026-05-28T01:55:12Z
resolved_at: 2026-05-28T02:30:00Z
specialist_hint: go
---

# Primary Reconciler Silent Hang — Root Cause Report

## Status

**root_cause_found** — Not actually a hang. The reconciler returned an error after 4m24s, but a missing log statement on the `ErrInstanceNotFound` branch of `waitForReadyOrDestroy` made the goroutine appear silent. Additionally, the same branch fails to call `BestEffortDestroy`, which is what caused the real-money orphan instance the operator had to clean up manually.

## Summary (1 paragraph)

The primary reconciler did NOT hang. It received `ErrInstanceNotFound` from `vast.Client.GetInstance` during the post-CreateInstance poll loop, transitioned the goroutine through `closeLifecycle` + `return errors.New("primary: instance terminal")`, and the spawnProvisioning wrapper correctly set FSM back to Asleep at 22:06:06 BRT (4m24s after offer-pick). The operator perceived a "silent hang + DB row missing" for three compounding reasons: (a) the `ErrInstanceNotFound` branch in `waitForReadyOrDestroy` (gateway/internal/primary/reconciler.go:894-899) is the **only** terminal branch with no `log.*` call, so 4m24s of polling produced zero log lines before the bubble-up Error; (b) the same branch is the **only** terminal branch that fails to call `vastutil.BestEffortDestroy`, leaving the actual Vast instance running and burning ~$0.04 over 5 minutes until the operator destroyed it manually; (c) the operator's `SELECT COUNT(*) FROM primary_lifecycles WHERE started_at > NOW() - INTERVAL '30 minutes'` was likely run before the second-attempt row landed OR misread — the DB row WAS written (lifecycle_id=2, started_at=2026-05-28T01:01:42Z, shutdown_reason=`instance_terminal_state`, vast_instance_id=38164478) and is still queryable via `gatewayctl primary lifecycles --since 24h`. The 4m24s gap between offer-pick and the terminal-error log indicates Vast.ai returned `{"instances": null}` only after many successful poll cycles — a transient API glitch (instance state-transition flap) rather than a real terminal state.

## Symptom Timeline (UTC, from this session)

| Time (UTC) | Source | Event |
|------------|--------|-------|
| 2026-05-28T00:59:24Z | gateway logs | lifecycle_id=1 first force-up — failed with `PRIMARY_WHISPER_WEIGHTS_SHA256 is empty` (env drift) |
| 2026-05-28T00:59:25Z | DB | lifecycle 1 closed, shutdown_reason=`build_create_request_failed` |
| 2026-05-28T01:00:30Z (approx) | operator | appended 13 missing env vars to /opt/ai-gateway-prod/.env, `docker compose up -d` recreated container |
| 2026-05-28T01:01:26Z | docker inspect | container created (image revision 5bd79d14) |
| 2026-05-28T01:01:34Z | gateway logs | "Phase 6.6 primary reconciler started" + "vast.Ping ok" |
| 2026-05-28T01:01:35Z | gateway logs | "acquired primary leadership" fsm_state=asleep |
| 2026-05-28T01:01:42.083Z | gateway logs | "primary force-up: provisioning by operator request" reason=11-06_load_test_uat_retry |
| 2026-05-28T01:01:42.645Z | gateway logs | "primary offer picked" offer_id=31139421 machine_id=55158 host_id=167329 dph=0.482 |
| 2026-05-28T01:01:42.645Z → 01:06:06.270Z | **BLIND WINDOW** | No `subsys=primary` logs (4m24s) — the bug surface |
| 2026-05-28T01:06:06.270Z | gateway logs | ERROR "primary provisionLifecycle returned error" err=`primary: instance terminal` lifecycle_id=2 |
| 2026-05-28T01:06:10.837Z | gateway logs | operator's gatewayctl primary force-down: "not in Provisioning/Ready/Draining; skipping" state=asleep |
| 2026-05-28T01:06:10Z (approx) | operator | `curl -X DELETE https://console.vast.ai/api/v0/instances/38164478/` → success |

DB rows (`gatewayctl primary lifecycles --since 24h`):
```
ID  STARTED               ENDED                 TRIGGER                                                     VAST_OFFER  VAST_INST  DPH     SHUTDOWN
2   2026-05-28T01:01:42Z  2026-05-28T01:06:06Z  operator_force_up:gatewayctl-cli:11-06_load_test_uat_retry  31139421    38164478   0.4822  instance_terminal_state
1   2026-05-28T00:59:24Z  2026-05-28T00:59:25Z  operator_force_up:gatewayctl-cli:11-06_load_test_uat        -           -          -       build_create_request_failed:primary: PRIMARY_WHISPER_WEIGHTS_SHA256 is empty…
```

## Hypothesis Evaluation

| ID | Hypothesis | Verdict | Evidence |
|----|------------|---------|----------|
| H1 | Goroutine deadlock after `vast.LaunchInstance` | **REFUTED** | Goroutine completed cleanly. Error log fired at 22:06:06 with `err="primary: instance terminal"`, lifecycle_id=2. FSM transitioned back to Asleep via the spawnProvisioning error path (reconciler.go:753 `SetState(StateAsleep, ...)`). |
| H2 | Panic + recoverer swallow | **REFUTED** | No panic logs. `errReason()` (reconciler.go:1154) correctly classified `"primary: instance terminal"` as `instance_terminal_state`. Goroutine path completed via normal `return errors.New(...)` flow at reconciler.go:897. |
| H3 | Silent error in `LaunchInstance` response parsing | **REFUTED** as cause of hang. But adjacent issue: `GetInstance` returns `ErrInstanceNotFound` when Vast returns `{"instances": null}` (vast/client.go:279-282). This is treated as a terminal signal with no retry — the **actual root cause vector**. |
| H4 | Stale leadership + lock not held | **REFUTED** | Logs show "acquired primary leadership" at 01:01:35 and no "lost primary leadership" entry. Lock held throughout the 4m24s window. |
| H5 | DB connection issue (shared with audit_log silent since 2026-05-25) | **REFUTED** | DB writes succeeded — `InsertPrimaryLifecycle` returned row.ID=2 (otherwise no `provisioning_started` and no goroutine spawn). `ClosePrimaryLifecycle` also succeeded (visible in `gatewayctl primary lifecycles`). Audit_log path is separate (`gateway/internal/audit/writer.go`) and shares only the `*pgxpool.Pool`, not the writer or schema role. Cross-ref ruled out. |
| H6 | gw:primary:events Redis double-consumption | **REFUTED** | Single "primary force-up" log + single "primary offer picked" log = single event consumption. Event subscriber goroutine is leader-gated (reconciler.go:249-251). |

## Root Cause

### Primary (the perceived "hang"): missing log on `ErrInstanceNotFound` branch

**File:** `gateway/internal/primary/reconciler.go`
**Function:** `waitForReadyOrDestroy`
**Lines:** 893-900

```go
case <-poll.C:
    inst, err := r.deps.Vast.GetInstance(ctx, instanceID)
    if err != nil {
        if errors.Is(err, vast.ErrInstanceNotFound) {
            _ = r.closeLifecycle(context.Background(), lifecycleID, "instance_terminal_state", 0)
            return errors.New("primary: instance terminal")
        }
        continue
    }
```

This is the ONLY terminal-exit branch in `waitForReadyOrDestroy` that does **NOT** emit a log line before returning. Compare with the three sibling branches:

| Branch | Line | Has log? | Has BestEffortDestroy? |
|--------|------|----------|-------------------------|
| `<-ctx.Done()` | 884-887 | (no, but ctx-cancel is logged upstream by cancelActiveLifecycle) | yes |
| `<-deadline.C` (cold-start budget) | 888-891 | (no, but err is logged by spawnProvisioning wrapper) | yes |
| `ErrInstanceNotFound` | **894-899** | **NO** | **NO** |
| `inst.StatusMsg` "error" | 902-916 | yes (`log.Error`) | yes |
| `inst.IsTerminal()` 3-strike confirm | 917-928 | yes (`log.Warn`) | yes |

The 4m24s blind window observed by the operator IS the elapsed time between `primary offer picked` (logged at line 813-818) and the goroutine's eventual return through the silent `ErrInstanceNotFound` branch. Many poll cycles ran successfully (non-terminal) before one transient `{"instances": null}` response hit. No log => operator concluded "silent hang".

### Secondary (the orphan instance): missing `BestEffortDestroy` on `ErrInstanceNotFound`

Same lines 894-899. Every other terminal-exit branch calls `vastutil.BestEffortDestroy` to ensure the Vast instance is reaped. The `ErrInstanceNotFound` branch assumes the instance is already gone — but Vast.ai's API can return `{"instances": null}` transiently for an instance that is **still alive on the host** (state-transition glitch / eventual consistency). When this happens:

1. Gateway closes the DB row with `instance_terminal_state` (truthy reason).
2. Gateway clears in-memory `activeInstanceID` (reconciler.go:754).
3. Vast instance keeps running, billing accumulates.
4. Operator notices via Vast UI; runs `curl -X DELETE` manually.

This is exactly what burned ~$0.04 in 5 minutes (instance 38164478 @ $0.4822/hr, ~5min before manual destroy).

### Tertiary (asymmetric retry policy): `ErrInstanceNotFound` is non-retried; `IsTerminal()` requires 3 strikes

`inst.IsTerminal()` (line 917) requires `terminalConfirmStrikes = 3` consecutive observations before declaring the instance dead. This was added in UAT 2026-05-18 (lifecycle 4 false-positive — comment on line 877-879). But `ErrInstanceNotFound` is treated as a 1-strike kill. The fix should either:

1. Apply the same 3-strike confirmation to `ErrInstanceNotFound`, OR
2. Re-poll `GetInstance` once with a short sleep (e.g. 2s) to confirm the null response is not transient before closing.

### Operator-misreporting clarifications

- "FSM frozen at provisioning forever" → INACCURATE. FSM correctly transitioned to Asleep at 01:06:06Z (4m24s after force-up). Operator likely captured `gatewayctl primary state` mid-window.
- "No DB row written" → INACCURATE. lifecycle_id=2 row exists; queryable via `gatewayctl primary lifecycles --since 24h`. Operator's 30-minute SQL window should have caught it; possibly they queried before goroutine return (during the 4m24s blind window).
- "Empty `lifecycle_id` in `gatewayctl primary state` output" → ACCURATE OBSERVATION, but **NOT** part of the silent-hang bug. The `gw:primary:state` Redis Hash mirror writes empty strings for `lifecycle_id`/`pod_url`/`pod_instance_id` on every FSM transition. See `gateway/cmd/gateway/main.go:870-871`:
  ```go
  if werr := redisx.WritePrimaryState(context.Background(), rdb,
      to.String(), "", "", "", at.Unix()); werr != nil {
  ```
  This is a separate, pre-existing UX gap (mirror completeness). It is independent of the silent-hang bug. The authoritative `primary_lifecycles` DB table always has correct values; only the gatewayctl-facing Redis mirror lacks them.

## Recommended Fix (PR-shaped, DO NOT APPLY)

**Scope:** `gateway/internal/primary/reconciler.go` (one branch, ~10 lines) + `gateway/internal/primary/reconciler_test.go` (one new test).

**Diff (illustrative — read by operator before applying):**

```diff
     case <-poll.C:
         inst, err := r.deps.Vast.GetInstance(ctx, instanceID)
         if err != nil {
             if errors.Is(err, vast.ErrInstanceNotFound) {
-                _ = r.closeLifecycle(context.Background(), lifecycleID, "instance_terminal_state", 0)
-                return errors.New("primary: instance terminal")
+                // Apply the same 3-strike confirmation used for inst.IsTerminal()
+                // — Vast.ai can return {"instances": null} transiently for an
+                // instance still alive on the host. UAT 2026-05-27 lifecycle 2
+                // captured this: 4m24s of successful polls, then a single null
+                // response closed the DB row + left a $0.04 orphan because no
+                // BestEffortDestroy fired. See debug/primary-reconciler-silent-hang.md.
+                terminalStrikes++
+                log.Warn("primary provisioning: Vast GET returned no_such_instance",
+                    "instance_id", instanceID,
+                    "strike", terminalStrikes,
+                    "confirm_at", terminalConfirmStrikes)
+                if terminalStrikes >= terminalConfirmStrikes {
+                    vastutil.BestEffortDestroy(ctx, r.deps.Vast, r.deps.Log, instanceID)
+                    _ = r.closeLifecycle(context.Background(), lifecycleID, "instance_terminal_state_confirmed", 0)
+                    return errors.New("primary: instance terminal (3-strike confirm via ErrInstanceNotFound)")
+                }
+                continue
             }
+            log.Debug("primary provisioning: GetInstance transient error; will retry",
+                "instance_id", instanceID, "err", err)
             continue
         }
```

**Why this shape:**

1. **Adds the missing log** (`log.Warn` per strike) so operators see the full poll history.
2. **Calls `BestEffortDestroy`** on confirmed-terminal so no more $0.04 orphans.
3. **Reuses the existing `terminalStrikes` counter** (line 879) — same counter resets on any non-terminal observation, so a single transient null between healthy polls won't trip the close. This matches the existing UAT-18 mitigation symmetry.
4. **Distinguishes shutdown_reason** (`instance_terminal_state_confirmed` vs the pre-existing `instance_terminal_state` for the `IsTerminal()` strike-out path) for forensic separation.
5. Optionally adds a debug log on transient GET errors (line 899's `continue`) so the blind window narrows in non-null error scenarios too.

**Migration note:** None. Pure code change in the poll loop. No DB schema, no env vars, no API contracts touched. The new shutdown_reason value is opaque to `primary_lifecycles.shutdown_reason` (TEXT column).

**Cross-cutting consideration:** the symmetric `recoverOpenLifecycle` path (reconciler.go:1020-1032) also treats a single `GetInstance` error as terminal:

```go
inst, err := r.deps.Vast.GetInstance(ctx, open.VastInstanceID.Int64)
if err != nil || inst.ActualStatus != "running" {
    // close as gateway_restart_orphan
}
```

This is OK for restart-recovery (single-shot) but worth a comment annotating the asymmetry vs the active poll loop — leaving as-is for now to keep the fix scope minimal.

## Test Recipe

### Reproduce the bug (DO NOT RUN against live Vast — costs $$$)

Unit test that proves the silent-close behavior using the existing fake VastAPI in `reconciler_test.go`:

```go
// gateway/internal/primary/reconciler_test.go
func TestWaitForReadyOrDestroy_ErrInstanceNotFound_SilentClose(t *testing.T) {
    // RED: assert the bug exists today. fakeVastAPI returns ErrInstanceNotFound
    // on the FIRST GetInstance call (simulating a transient Vast {"instances": null}).
    // Expect: lifecycle closed with reason "instance_terminal_state", no Vast
    // DestroyInstance called, no log emitted on the close path. This test should
    // FAIL after the fix lands (because the new code will require 3 strikes).
    //
    // Steps:
    //   1. Build Reconciler with fake VastAPI that returns ErrInstanceNotFound
    //      on first GetInstance, then "running" forever after.
    //   2. Call provisionLifecycle in a goroutine.
    //   3. Assert: closeLifecycle called with reason "instance_terminal_state".
    //   4. Assert: fakeVastAPI.DestroyCallCount == 0 (THIS IS THE BUG).
    //   5. Assert: no log records contain "no_such_instance" (THIS IS THE BUG).
}
```

### Verify a fix

After applying the patch above:

```go
func TestWaitForReadyOrDestroy_ErrInstanceNotFound_RequiresThreeStrikes(t *testing.T) {
    // GREEN: after fix, the close should require 3 consecutive ErrInstanceNotFound.
    // A single ErrInstanceNotFound followed by a healthy "running" response should
    // RESET the strike counter and continue polling.
    //
    // Assertions:
    //   1. After 1 strike + 1 healthy poll, lifecycle still open.
    //   2. After 3 consecutive strikes, BestEffortDestroy called exactly once.
    //   3. closeLifecycle called with reason "instance_terminal_state_confirmed".
    //   4. Log records contain "Vast GET returned no_such_instance" at strike 1, 2, 3.
}
```

### Manual verification (cheap, no Vast spend)

```bash
# 1. Confirm the bug reproduces in unit test before fix
cd /home/pedro/projetos/pedro/gpu-ifix/gateway
go test ./internal/primary/ -run TestWaitForReadyOrDestroy_ErrInstanceNotFound_SilentClose -v

# 2. Apply the patch above

# 3. Run new green test
go test ./internal/primary/ -run TestWaitForReadyOrDestroy_ErrInstanceNotFound_RequiresThreeStrikes -v

# 4. Verify existing reconciler tests still pass
go test ./internal/primary/...
```

### Live UAT (only after unit tests pass + operator approval)

Trigger a force-up on n8n-ia-vm with a fresh container. With the fix, even if Vast returns a transient `{"instances": null}`, the reconciler should now emit `level=WARN msg="primary provisioning: Vast GET returned no_such_instance" strike=1/2/3` rather than going silent. **Budget:** one Vast cycle (~$0.04–0.10 worst case).

## Related Bug Cross-Reference

**Audit_log silent since 2026-05-25** (mentioned by operator as potential H5 overlap): **No code-level overlap.** The audit pipeline lives in `gateway/internal/audit/{writer.go,middleware.go}` and uses an async batched writer against `ai_gateway.audit_log` / `ai_gateway.audit_log_content`. The only shared resource with the primary reconciler is the `*pgxpool.Pool`, which is demonstrably functional (primary `InsertPrimaryLifecycle` + `ClosePrimaryLifecycle` both succeeded today). Audit silence is its own debug session — start with `gateway/internal/audit/writer.go` async-channel buffer state + Phase 10 prod-deploy diff.

**`gw:primary:state` Redis mirror — empty `lifecycle_id`/`pod_url`/`pod_instance_id`** (mentioned in operator brief): **Pre-existing UX gap, not the silent-hang bug.** `gateway/cmd/gateway/main.go:870-871` writes those 3 fields as empty strings on every FSM transition. The DB table `ai_gateway.primary_lifecycles` is authoritative; gatewayctl users should consult `gatewayctl primary lifecycles` for full state. Worth filing as a separate carry-forward tech debt for Phase 12 / gateway-ops polish.

## Evidence Inventory

- **Source files read:** `gateway/internal/primary/reconciler.go` (1204 lines), `gateway/internal/primary/lifecycle.go` (469 lines), `gateway/internal/emerg/vast/client.go` (385 lines), `gateway/internal/emerg/vast/errors.go`, `gateway/internal/redisx/primary.go`, `gateway/cmd/gateway/main.go` (lines 810-930), `gateway/cmd/gatewayctl/primary.go`, `gateway/internal/db/gen/primary_lifecycles.sql.go`, `gateway/internal/db/pool.go`, `gateway/internal/vastutil/helpers.go`
- **Git history:** `git log --since=2026-05-19` for `gateway/internal/primary/`, `gateway/internal/emerg/vast/`, `gateway/internal/vastutil/`, `gateway/internal/db/gen/`, `gateway/cmd/gateway/main.go`. Confirmed no commits since 2026-05-19 touched the `ErrInstanceNotFound` branch — this bug has been present since Phase 06.6 landing (the silent-close path is original code).
- **Live evidence (n8n-ia-vm):**
  - Container image: `sha256:732293ec634cad414b452b17c3352ea2b925644e327ec8e072d1e034c787ea4f`, revision `5bd79d14b07c74b111b9cc442f5d653e6338caa7`, created 2026-05-28T01:01:26Z.
  - Full reconciler log timeline 22:01:34 → 22:06:10 BRT captured above.
  - DB rows confirmed via `gatewayctl primary lifecycles --since 24h`.
  - Current FSM state confirmed via `gatewayctl primary state` → `state=asleep`.
  - Live Vast instances confirmed via REST `GET /instances/` → 0 instances.
- **Phase 11 plan-06 evidence:** `.planning/phases/11-prod-hardening/11-06-EVIDENCE.md` documents both force-up attempts + the env-drift fix.

## Operator decision (HALT here)

This debug session is **diagnose-only** per project rule:

> "Agentes debugger (gsd-debugger) NÃO devem fazer edits especulativos em arquivos — mudanças incorretas vão para produção dev ao commitar/pushar"

No source edits applied. Recommended next step:

1. Operator reviews this report.
2. Operator authorizes a fix session (separate `/gsd:debug --fix` or planned plan) using the **Recommended Fix** section above as the PR shape.
3. RED unit test lands first; GREEN test + patch land together; CI gate confirms no regression in existing reconciler tests.
4. After CI green, build `latest-dev` image + deploy to n8n-ia-vm + trigger one controlled force-up to live-verify the new log lines fire.
