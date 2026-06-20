---
phase: 14-vram-adaptive-stt-gateway-auto-decides-pod-stt-via-pod-repor
fixed_at: 2026-06-19T00:00:00Z
review_path: .planning/phases/14-vram-adaptive-stt-gateway-auto-decides-pod-stt-via-pod-repor/14-REVIEW.md
iteration: 1
findings_in_scope: 7
fixed: 7
skipped: 0
status: all_fixed
---

# Phase 14: Code Review Fix Report

**Fixed at:** 2026-06-19
**Source review:** .planning/phases/14-vram-adaptive-stt-gateway-auto-decides-pod-stt-via-pod-repor/14-REVIEW.md
**Iteration:** 1

**Summary:**
- Findings in scope: 7 (1 Critical + 6 Warning; Info findings out of scope)
- Fixed: 7
- Skipped: 0

## Fixed Issues

### CR-01: Drain-complete gate ignores in-flight STT/TTS requests and counts an off-pod upstream

**Files modified:** `gateway/internal/primary/reconciler.go`, `gateway/internal/primary/lifecycle.go`
**Commit:** 98a1b12
**Status:** fixed: requires human verification (drain-correctness logic change)
**Applied fix:** Changed the `evaluateDraining` inflight sum from `local-llm + local-embed` to `local-llm + local-stt + local-tts` — the 3 upstreams that actually live on the primary pod (llama/speaches/chatterbox). Dropped the off-pod `local-embed` term (relocated to a 24/7 CPU host per D-03). This makes the Draining→Destroying gate hold open until in-flight STT/TTS requests finish, instead of cutting them off. Also rewrote the stale `InflightAdapter` doc comment in `lifecycle.go` that claimed "local-stt term removed" to reflect the restored 3 on-pod upstreams. Flagged for human verification because this is a semantic drain-correctness change, not a syntax/structure fix.

### WR-01: `:9100` device-report responder startup race with single-shot gateway probe

**Files modified:** `gateway/cmd/gateway/main.go`
**Commit:** 7c3403e
**Applied fix:** Refactored `primaryDeviceReport` into an inner single-attempt `deviceReportOnce` plus an outer retry loop (3 attempts, 500ms backoff, ctx-cancellation aware). A retry fires only on a transient miss (empty/non-200/parse error), never on a legitimate `"cpu"` terminal answer, so a GPU pod whose `:9100` responder loses its async bind race no longer silently routes STT to the costlier tier-1 cascade for its whole lifecycle. Kept the pod fail-open (gateway-side retry is the smaller change per the review).

### WR-02: `recoverOpenLifecycle` log says "3-endpoint" but checks 4

**Files modified:** `gateway/internal/primary/reconciler.go`
**Commit:** ce6ba1a
**Applied fix:** Changed the warn log message from "3-endpoint health check failed" to "4-endpoint health check failed" to match the actual LLM+STT+TTS+DCGM probe set. (Committed together with WR-03 as both findings are doc/log corrections in the same files and cannot be cleanly separated at file granularity.)

### WR-03: Stale embed:8002 references in doc comments

**Files modified:** `gateway/internal/primary/reconciler.go`, `gateway/internal/primary/lifecycle.go`
**Commit:** ce6ba1a
**Applied fix:** Replaced "embed"/"8002" references with "TTS"/"8003" in the comments that described the live 4-endpoint set incorrectly: `reconciler.go` `waitForReadyOrDestroy` doc ("LLM + STT + embed + DCGM" → "LLM + STT + TTS + DCGM"), port list `8000/8001/8002/9400` → `8000/8001/8003/9400`, the file-header `8002 embed` → `8003 TTS`, and `lifecycle.go` `buildCreateRequest` env-forward comment + `podPortURL` 4-services model comment. Code was already correct (probes TTS:8003); only the comments were stale.

### WR-04: STT override skip is silently lossy on cuda-misreport (no metric)

**Files modified:** `gateway/internal/primary/reconciler.go`
**Commit:** de3c993
**Applied fix:** Added an `else if urls.STT != ""` branch to the `markReady` STT override site that emits a `log.Warn` when the pod health-passed (STT service is up) but `WhisperDevice != "cuda"`, so the tier-0 STT override is skipped and STT routes to the tier-1 cascade. This surfaces a chronically mis-read device report (combined with the WR-01 retry) vs. a legitimately cpu/24GB shape, making the cost regression visible to operators.

### WR-05: onstart whisper GPU index assumes speaches and llama.cpp share identical CUDA ordering

**Files modified:** `gateway/internal/primary/onstart.go`, `pod/primary/supervisord.conf`
**Commit:** 49364ea
**Applied fix:** Exported `CUDA_DEVICE_ORDER=PCI_BUS_ID` in the gateway-built onstart before `exec supervisord` so both the llama and speaches children inherit it and enumerate GPUs identically + deterministically (index 0 for qwen `--main-gpu 0` and index `NUM_GPUS-1` for whisper always resolve to distinct physical cards on multi-GPU shapes). Documented the shared-ordering contract at the `[program:llama]`/`[program:speaches]` boundary in `supervisord.conf`, including a do-NOT-override warning against setting per-process `CUDA_VISIBLE_DEVICES`/`CUDA_DEVICE_ORDER` that would reintroduce the UAT-B OOM.

### WR-06: `evaluateDraining` swallows ctx via `_ = ctx`

**Files modified:** `gateway/internal/primary/reconciler.go`
**Commit:** b6567c1
**Applied fix:** Removed the `_ = ctx` swallow and replaced it with a one-line comment explaining that `ctx` is retained for signature parity with the sibling `evaluate*` dispatchers (Tick/Asleep/Ready/Destroying all share `(ctx, now, log)`), and that any future DB/Redis call added to the drain path must thread ctx for cancellation. Go does not flag unused function parameters, so the build remains clean.

---

_Fixed: 2026-06-19_
_Fixer: Claude (gsd-code-fixer)_
_Iteration: 1_
