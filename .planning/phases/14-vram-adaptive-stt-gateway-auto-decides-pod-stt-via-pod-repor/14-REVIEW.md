---
phase: 14-vram-adaptive-stt-gateway-auto-decides-pod-stt-via-pod-repor
reviewed: 2026-06-19T00:00:00Z
depth: standard
files_reviewed: 8
files_reviewed_list:
  - gateway/cmd/gateway/main.go
  - gateway/internal/config/config.go
  - gateway/internal/primary/lifecycle.go
  - gateway/internal/primary/lifecycle_test.go
  - gateway/internal/primary/onstart.go
  - gateway/internal/primary/reconciler.go
  - gateway/internal/primary/reconciler_test.go
  - pod/primary/supervisord.conf
findings:
  critical: 1
  warning: 6
  info: 4
  total: 11
status: issues_found
---

# Phase 14: Code Review Report

**Reviewed:** 2026-06-19
**Depth:** standard
**Files Reviewed:** 8
**Status:** issues_found

## Summary

Reviewed the Phase 14 VRAM-adaptive STT change set: the gateway now auto-decides
whether the primary pod serves STT (tier-0 local Whisper) based on a pod-reported
`whisper_device` value, instead of a manual config flag. The pod-side onstart
(`onstart.go`) computes the whisper device from total VRAM via `nvidia-smi`, pins
Qwen to GPU0 (`--split-mode none --main-gpu 0` in `supervisord.conf`), dedicates the
last GPU to whisper on multi-GPU shapes, and stands up a `:9100/whisper_device`
JSON responder. The gateway (`reconciler.go` + `main.go` `primaryDeviceReport`
closure) reads that report at pod-Ready and gates the `stt` tier-0 override on a
`{cuda,cpu}` whitelist (default-deny → tier-1 gemini-stt).

The device-gating logic (whitelist, three override sites: `markReady`,
`evaluateReady` re-assert, `recoverOpenLifecycle`) is consistent and well-tested.
The bounded `io.LimitReader` on the pod report is a correct hardening against a
hostile pod. The onstart device math correctly avoids `bc` (awk only) and uses
raw-string concatenation (no `fmt.Sprintf`).

However, the drain-complete gate in `evaluateDraining` was NOT updated when Phase
11.2 / Phase 14 restored STT (and TTS) to the pod: it still sums `local-llm +
local-embed` inflight, which both omits the now-on-pod `local-stt`/`local-tts`
counters AND counts an upstream (`local-embed`) that no longer lives on the pod.
This is a real drain-correctness defect (BLOCKER). Several stale comments and one
incorrect log line ("3-endpoint" while checking 4) accompany the multiple
phase-revert cycles.

## Critical Issues

### CR-01: Drain-complete gate ignores in-flight STT/TTS requests and counts an off-pod upstream

**File:** `gateway/internal/primary/reconciler.go:748-752`
**Issue:**
`evaluateDraining` computes the inflight count that gates the
`Draining→Destroying` transition (i.e. "is it safe to destroy the pod yet?") as:

```go
inflight := int64(0)
if r.deps.Inflight != nil {
    inflight = r.deps.Inflight.Count("local-llm") +
        r.deps.Inflight.Count("local-embed")
}
```

This is wrong on both sides after Phase 11.2 / Phase 14 restored STT to the pod:

1. **Counts an upstream that is NOT on the pod.** Per D-03 (and the comment block
   at `lifecycle.go:68-71`, `supervisord.conf:91-95`), `embed` was relocated OFF
   the GPU pod to a 24/7 CPU host and is a static tier-0 row. `local-embed`
   inflight has nothing to do with whether the *primary pod* is safe to destroy,
   yet it can hold the drain open (or, more importantly, its irrelevance hides
   the real omission below).

2. **Omits the upstreams that ARE on the pod.** The pod serves `local-llm`,
   `local-stt`, and `local-tts` (supervisord children llama/speaches/chatterbox).
   The drain gate never counts `local-stt` or `local-tts`. A transcription or TTS
   request in flight against the dying pod is invisible to the drain-complete
   check, so `inflight == 0` can be satisfied while real STT/TTS requests are
   still streaming to the pod → those requests are cut off when
   `evaluateDestroying` calls `BestEffortDestroy`. This defeats the entire point
   of the grace-ramp-down drain for the two services Phase 14 is specifically
   about (STT) and its sibling (TTS).

The `lifecycle.go:65-71` `InflightAdapter` doc even states the stale intent:
"sum local-llm + local-embed inflight (Phase 11.1 D-A4: local-stt term removed —
Whisper deleted from pod and DB)". Phase 11.2 D-B5′ reverted that deletion
(STT is back on the pod, asserted by `TestSupervisordConf_3ProgramBlocks` and the
restored `markReady`/`startDrain`/`recoverOpenLifecycle` STT override sites), but
the drain counter was never re-aligned.

**Fix:**
```go
inflight := int64(0)
if r.deps.Inflight != nil {
    // Sum the upstreams that actually live on the primary pod
    // (llama/speaches/chatterbox). embed is off-pod (D-03) — do not count it.
    inflight = r.deps.Inflight.Count("local-llm") +
        r.deps.Inflight.Count("local-stt") +
        r.deps.Inflight.Count("local-tts")
}
```
Also update the `InflightAdapter` doc comment at `lifecycle.go:65-71` to drop the
"local-stt term removed" wording and reflect the 3 on-pod upstreams.

## Warnings

### WR-01: `:9100` device-report responder has a startup race with the gateway probe and no readiness signal

**File:** `gateway/internal/primary/onstart.go:256-282`
**Issue:**
The device-report responder is launched as a backgrounded `nohup python3 ... &`
wrapped in `|| true`, immediately before `exec /usr/bin/supervisord`. The gateway
reads `:9100/whisper_device` once at pod-Ready (`reconciler.go:545-553` →
`primaryDeviceReport`, single GET, 5s timeout, no retry). There is no ordering
guarantee that the Python `TCPServer` has bound `:9100` before the gateway's
4-endpoint health gate passes and the device read fires — the responder binds
asynchronously while supervisord is simultaneously bringing up llama/speaches/
chatterbox/dcgm (whose health is what gates Ready). If the bind loses the race,
`primaryDeviceReport` returns `""` → fail-safe to no-STT-override → a GPU pod that
*can* serve STT silently routes STT to the more expensive tier-1 gemini cascade
for the entire pod lifecycle. This is fail-safe (no crash) but defeats the
feature's cost objective non-deterministically.

**Fix:** Have the gateway retry the device report a few times (e.g. 3 attempts
with a short backoff) inside `primaryDeviceReport` / `deviceReport`, OR make the
onstart block until the responder confirms a successful bind before
`exec supervisord` (e.g. a short `curl -fsS --retry` loop against
`127.0.0.1:9100/whisper_device`). Retrying on the gateway side is the smaller
change and keeps the pod fail-open.

### WR-02: `recoverOpenLifecycle` log message says "3-endpoint health check failed" but the code checks 4

**File:** `gateway/internal/primary/reconciler.go:1615-1621`
**Issue:**
The health gate checks LLM + STT + TTS + DCGM (4 endpoints):

```go
if r.deps.HealthCheck == nil ||
    !r.deps.HealthCheck(ctx, urls.LLM) ||
    !r.deps.HealthCheck(ctx, urls.STT) ||
    !r.deps.HealthCheck(ctx, urls.TTS) ||
    !r.deps.HealthCheck(ctx, urls.DCGM) {
    r.deps.Log.Warn("primary recover: 3-endpoint health check failed; closing as unhealthy orphan",
```

The log says "3-endpoint". This will mislead an operator debugging a recovery
failure into thinking only 3 probes ran. Stale from a prior phase.

**Fix:** Change the message to "4-endpoint health check failed".

### WR-03: `waitForReadyOrDestroy` doc + multiple comments reference the deleted embed:8002 endpoint

**File:** `gateway/internal/primary/reconciler.go:1258-1259, 1266-1269, 1467-1470`; `lifecycle.go:354, 482-487`; `reconciler.go:48-49`
**Issue:**
After Phase 06.7 D-03 (embed off-pod) and Phase 11.2 D-B5′ (STT back), the live
4-endpoint set is LLM(8000)/STT(8001)/TTS(8003)/DCGM(9400). Numerous doc comments
still describe the set as "LLM + STT + embed + DCGM" on ports "8000/8001/8002/9400"
(e.g. `reconciler.go:1259` "ALL 4 health endpoints pass (LLM + STT + embed + DCGM)",
`reconciler.go:1267` "8000/8001/8002/9400", `lifecycle.go:354` "8000 LLM + 8001 STT
+ 8002 embed + 9400 DCGM", `lifecycle.go:483-486` "container ports 8000 (LLM ...),
8002 (embed ...)"). The code is correct (it probes TTS:8003), but the comments
describe a topology that no longer exists, which is actively misleading during
incident triage of the exact drain/health path CR-01 lives in.

**Fix:** Replace "embed"/"8002" references in these comments with "tts"/"8003" to
match the live supervisord/port-forward set.

### WR-04: `markReady` cost / DPH path unaffected, but STT override skip is silently lossy on cuda-misreport

**File:** `gateway/internal/primary/reconciler.go:879-881`
**Issue:**
`markReady` overrides the `stt` tier-0 slot only when
`urls.WhisperDevice == "cuda"`. The whitelist is enforced twice (once in the
production `primaryDeviceReport` closure at `main.go:1029-1032`, once here), which
is correct defense-in-depth. The risk: if a healthy GPU pod's onstart exported
`cuda` but the `:9100` responder lost the race (WR-01) so the gateway recorded
`""`, the pod will pass all 4 health probes (STT is up on the pod) yet the gateway
deliberately routes STT to tier-1 — paying gemini/groq/openai per-minute cost for
a service that is running locally and idle. No metric is emitted to flag this
"pod-can-serve-STT-but-we-chose-tier-1" divergence, so the cost regression is
invisible.

**Fix:** Emit a counter/log when `urls.STT != ""` and the pod health-passed but
`WhisperDevice != "cuda"`, so operators can detect a chronically mis-read device
report (vs. a legitimately cpu/24GB shape). Combine with the WR-01 retry.

### WR-05: onstart whisper GPU index assumes speaches and llama.cpp share identical CUDA device ordering

**File:** `gateway/internal/primary/onstart.go:230-241`; `pod/primary/supervisord.conf:44`
**Issue:**
The coordination contract is: llama pins Qwen to GPU0 (`--main-gpu 0`), onstart
pins whisper to `NUM_GPUS-1` via `WHISPER__DEVICE_INDEX`. This is only OOM-safe if
llama.cpp's `--main-gpu` index space and faster-whisper's
`WHISPER__DEVICE_INDEX`/CUDA ordinal space enumerate the GPUs in the same order.
Both default to the CUDA runtime ordering, so today they agree — but if
`CUDA_VISIBLE_DEVICES` or `CUDA_DEVICE_ORDER` is ever set differently for the two
supervisord children (or Vast injects a per-process visibility mask), index 0 for
llama and index `NUM_GPUS-1` for whisper could resolve to the SAME physical card,
reintroducing the exact UAT-B OOM this change set fixes. There is no assertion or
guard that the two index spaces are aligned.

**Fix:** Document the shared-ordering assumption explicitly at the supervisord
`[program:llama]`/`[program:speaches]` boundary, and consider pinning both via the
same mechanism (e.g. export `CUDA_DEVICE_ORDER=PCI_BUS_ID` in onstart before
`exec supervisord` so both children enumerate identically and deterministically).

### WR-06: `evaluateDraining` swallows the context — `_ = ctx` masks a missing cancellation check

**File:** `gateway/internal/primary/reconciler.go:759`
**Issue:**
`evaluateDraining` takes a `ctx context.Context` but only uses it via `_ = ctx`
after the transition. The function performs the `FSM.Transition` and returns;
the ctx is never consulted. This is dead-parameter usage that hides whether the
drain loop should honor cancellation. It is not a crash, but `_ = ctx` is a code
smell that suppresses the unused-parameter signal and makes it easy to miss that
a future DB/Redis call added here would not be ctx-bound.

**Fix:** Drop the unused `ctx` swallow; either remove the parameter (it is called
only from `evaluateTick` which has the ctx) or thread it into a real
cancellation-aware operation. If kept for signature parity with sibling
evaluators, leave a one-line comment instead of `_ = ctx`.

## Info

### IN-01: Duplicated per-endpoint HTTP probe closures in main.go

**File:** `gateway/cmd/gateway/main.go:941-958, 1002-1037`
**Issue:** `primaryHealthCheck` and `primaryDeviceReport` each build their own
`http.Client{Timeout: 5s}` + `context.WithTimeout(..., 5s)` + request + 2xx check
with near-identical structure. `primaryReachable` is a third variant. This is
acceptable but invites drift (e.g. one gets a retry per WR-01/WR-04 and the others
do not).

**Fix:** Extract a small `podGet(ctx, url, timeout)` helper returning
`(*http.Response, error)` and build the three closures on top of it.

### IN-02: Test function name `TestSupervisordConf_3ProgramBlocks` asserts 4 program blocks

**File:** `gateway/internal/primary/lifecycle_test.go:575`
**Issue:** The test is named `..._3ProgramBlocks` but asserts the presence of
4 active programs (llama, speaches, chatterbox, dcgm) and the absence of infinity.
The doc comment above it (line 569) correctly says "exactly 4". The function name
is stale from the Phase 11.1 3-block era.

**Fix:** Rename to `TestSupervisordConf_4ProgramBlocks` for accuracy.

### IN-03: `markReady` STT comment cross-references a "manual STT-serve config flag" that is fully deleted

**File:** `gateway/internal/primary/reconciler.go:874-878` (and `lifecycle.go:118-127`, `config.go:252-256`)
**Issue:** Multiple comments narrate the SEED-019 history ("Replaces the deleted
manual STT-serve config flag", "PRIMARY_POD_SERVE_STT DELETED"). This historical
breadcrumb is fine once, but it is repeated at ~5 sites and adds noise around the
load-bearing device-gate logic. Not a defect.

**Fix:** Optional — consolidate the rationale into one canonical doc location and
trim the repeated callouts.

### IN-04: onstart device-report responder serves a hardcoded fallback that can disagree with the exported device

**File:** `gateway/internal/primary/onstart.go:266-267`
**Issue:** If `/weights/whisper/whisper_device.json` cannot be opened, the Python
responder returns a hardcoded `{"whisper_device":"cpu"}`. This is a safe default,
but if the actual exported device was `cuda` and the file read transiently fails,
the gateway reads `cpu` and permanently routes STT to tier-1 for the lifecycle
(no re-read). Low-probability (the file is written immediately before the server
starts and never removed), so info-level.

**Fix:** None required; noting for completeness. The WR-01 gateway-side retry
would also smooth a transient file-read blip here.

---

_Reviewed: 2026-06-19_
_Reviewer: Claude (gsd-code-reviewer)_
_Depth: standard_
