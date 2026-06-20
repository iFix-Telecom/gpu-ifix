---
phase: 14-vram-adaptive-stt
plan: 02
subsystem: pod-runtime
tags: [pod, onstart, bash, nvidia-smi, whisper, speaches, supervisord, device-report, gateway, vast]

# Dependency graph
requires:
  - phase: 14-01
    provides: "gateway probes GET :9100/whisper_device and gates the STT tier-0 override on whisper_device==cuda (whitelist {cuda,cpu}, default-deny)"
provides:
  - "Pod-side fulfillment of the device-report CONTRACT: onstart serves GET :9100/whisper_device -> {\"whisper_device\":\"cuda\"|\"cpu\"}"
  - "VRAM-adaptive whisper device: onstart exports WHISPER__INFERENCE_DEVICE + WHISPER__DEVICE_INDEX + WHISPER_DEVICE before exec supervisord (cuda >=30GB pinned to max-free GPU, cpu below / nvidia-smi absent)"
  - "supervisord.conf [program:speaches] no longer pins WHISPER__INFERENCE_DEVICE=cpu (inherits onstart env); HF_HUB_CACHE preserved"
  - "buildCreateRequest forwards -p 9100:9100 so the gateway can reach the responder"
affects: [14-03, primary-pod-rollout]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Pod self-decides whisper placement at boot from one-shot nvidia-smi VRAM sum (awk, no bc); gateway stays shape-agnostic"
    - "supervisord env inheritance (Option A) — onstart export reaches [program:speaches] because the conf no longer overrides the key; fail-open to speaches default auto/0"
    - "Minimal in-pod static HTTP responder (python3 http.server, image-baked) on :9100 reads the device JSON at request time; launch wrapped in || true so a transient bind never aborts the pod"

key-files:
  created: []
  modified:
    - gateway/internal/primary/onstart.go
    - gateway/internal/primary/lifecycle.go
    - gateway/internal/primary/lifecycle_test.go
    - pod/primary/supervisord.conf

key-decisions:
  - "VRAM threshold = 30000 MiB (2x3090=48 / 5090=32 → cuda; 1x3090=24 → cpu) — matches CONTEXT >=~30GB and the 'a 24GB pod MUST NOT serve slow CPU whisper' constraint"
  - "WHISPER__DEVICE_INDEX pinned to the MAX-FREE GPU index (nvidia-smi sort -k2 desc), NOT 'the non-Qwen GPU' — RESEARCH gap #2: llama --split-mode layer spreads Qwen across all GPUs so no card is Qwen-free (OPEN Q1 option (a), large 2x3090 headroom; live no-OOM UAT is the gate in 14-03)"
  - "<30GB shape reports cpu and KEEPS speaches UP (does not disable the child) so the 4-endpoint :8001 health gate still passes — gateway fail-safes STT to gemini via whisper_device=cpu (RESEARCH OPEN Q2 recommendation)"
  - "Device-report channel = one-process static :9100 responder launched from onstart bash, NOT the health-bridge (D-14-01: health-bridge is local-dev/compose-only, never COPY'd into the PROD image) — minimal PROD surface, honors CONTEXT intent"
  - "nvidia-smi absent/empty → fail-safe to cpu (gateway → gemini), never a crash"

requirements-completed: [POD-VRAM, STT-AUTO, STT-PROBE]

# Metrics
duration: 5min
completed: 2026-06-17
---

# Phase 14 Plan 02: Pod VRAM-adaptive whisper + :9100 device-report Summary

**The primary pod now self-decides whether its whisper runs on GPU from a one-shot nvidia-smi VRAM sum at onstart (cuda on >=30 GB pinned to the max-free GPU index, cpu below threshold), exports the device into the inherited env before `exec supervisord`, and serves the exact `GET :9100/whisper_device -> {"whisper_device":"cuda"|"cpu"}` contract Plan 14-01 reads — with `-p 9100:9100` forwarded and the build-baked cpu pin removed (HF_HUB_CACHE intact).**

## Performance

- **Duration:** ~5 min
- **Started:** 2026-06-17
- **Completed:** 2026-06-17
- **Tasks:** 2/2 (Task 1 TDD RED→GREEN, Task 2 implementation)
- **Files modified:** 4

## Accomplishments

### Task 1 — onstart VRAM block + :9100 responder (RED 0c… GREEN)
- **RED (commit bb5eed3):** Added `TestPrimaryOnstart_VRAMAdaptiveWhisperDevice` (asserts `nvidia-smi --query-gpu=memory.total`, `30000`, `WHISPER__INFERENCE_DEVICE`/`WHISPER__DEVICE_INDEX`/`WHISPER_DEVICE`, `nvidia-smi --query-gpu=index,memory.free` max-free pick, `/whisper_device`, `9100`, ordering before `exec supervisord`, and no `\bbc\b`), `TestPrimaryOnstart_NoFmtSprintf` (Pitfall #9), and `TestBuildPrimaryCreateRequest_Forwards9100` to `lifecycle_test.go` (there is no separate onstart_test.go). Confirmed RED: VRAM + 9100 tests failed.
- **GREEN (commit 9b2097b):** Extended `primaryOnstartHead` (onstart.go) just before its closing backtick with two blocks appended by plain raw-string concatenation:
  - **Block A (VRAM detect + export):** `TOTAL_VRAM_MIB` via `nvidia-smi --query-gpu=memory.total ... | awk '{s+=$1} END{print s}'`; empty/absent → `WHISPER_DEVICE=cpu` fail-safe; `>= 30000` → `WHISPER__INFERENCE_DEVICE=cuda` + `WHISPER__DEVICE_INDEX=<max-free index via sort -t, -k2 -n -r | head -1 | cut -d, -f1 | tr -d ' '>` + `WHISPER_DEVICE=cuda`; else `cpu`. All exports precede `exec supervisord` so `[program:speaches]` inherits them.
  - **Block B (responder):** `printf '{"whisper_device":"%s"}' "$WHISPER_DEVICE" > /weights/whisper/whisper_device.json`, then a backgrounded `python3 -c` `http.server` on `0.0.0.0:9100` returning 200 application/json for `GET /whisper_device` (reads the JSON at request time) and 404 otherwise. Launch wrapped `{ nohup … & } || true` (T-14-06 fail-open under set -e).
  - No `fmt.Sprintf`, no `bc`. `bash -n` clean on the rendered script.

### Task 2 — drop cpu pin + forward 9100 (commit 3d1c7b1)
- `pod/primary/supervisord.conf:46`: `environment=HF_HUB_CACHE="/weights/whisper",WHISPER__INFERENCE_DEVICE="cpu"` → `environment=HF_HUB_CACHE="/weights/whisper"` (HF cache gate preserved, Pitfall 4). Rewrote the UAT-17 CPU-pin comment block to document the Phase 14 VRAM-adaptive inheritance + fail-open semantics.
- `gateway/internal/primary/lifecycle.go` buildCreateRequest env map: added `"-p 9100:9100": "1"` next to the existing 4 forwards.

## Verification Results

- `cd gateway && go build ./...` → exit 0
- `cd gateway && go test ./internal/primary/... -count=1` → `ok  github.com/ifixtelecom/gpu-ifix/gateway/internal/primary  10.6s`
- `bash -n` on the rendered onstart (via a throwaway dump test) → clean
- Acceptance counts:
  - onstart.go: `nvidia-smi --query-gpu=memory.total`=1, `WHISPER__DEVICE_INDEX`=3, `WHISPER_DEVICE`=5, `/whisper_device`=4, `9100`=6, `bc` (wordbound)=0, `fmt.Sprintf`=0
  - supervisord.conf: `environment=HF_HUB_CACHE="/weights/whisper"`=1, non-comment `WHISPER__INFERENCE_DEVICE="cpu"`=0, `HF_HUB_CACHE`=3
  - lifecycle.go: `9100:9100`=1; lifecycle_test.go: `9100`=11

## Threat Model Mitigations Applied

- **T-14-04 (DoS — supervisord boot if device unset):** onstart exports a definite `WHISPER__INFERENCE_DEVICE` in every branch (cuda / cpu / nvidia-smi-absent); env inheritance (Option A) fails open to speaches default — never `%(ENV_x)s` crash.
- **T-14-05 (Tampering — HF_HUB_CACHE dropped):** edit keeps `HF_HUB_CACHE="/weights/whisper"`; acceptance grep asserts it survives (count 3, the environment= line count 1).
- **T-14-06 (DoS — :9100 bind failure aborts pod):** responder launch is `{ nohup … & } || true`; gateway treats unreachable :9100 as device="" fail-safe (Plan 14-01).
- **T-14-07 (Tampering — shell-quoting via template expansion):** raw-string concatenation only; `fmt.Sprintf`=0 asserted.
- **T-14-SC (package installs):** none — uses image-baked python3 + nvidia-smi + coreutils. No legitimacy gate required.

## Deviations from Plan

None — both tasks executed exactly as written. Two in-task corrections (not plan deviations) during GREEN:
- Backticks inside the new bash comments (`` `exec supervisord` ``, `` `|| true` ``) terminated the Go raw string → reworded to plain text. Build then clean.
- The word "bc" in a new bash comment ("bc is NOT installed") tripped the `\bbc\b` grep-gate → reworded to "no calculator binary". Both are the grep-gate doing its job.

## Device-Report Contract (served by this plan, consumed by 14-01)

```
GET http://<pod-ip>:<host-port-for-9100>/whisper_device
  -> 200 application/json
  body: {"whisper_device":"cuda"}  |  {"whisper_device":"cpu"}
```
Gateway whitelists exactly `{cuda,cpu}`; any other value / non-200 / parse error / unreachable → no STT override → gemini-stt (fail-safe).

## Known Stubs

None. The onstart device block + :9100 responder are fully implemented bash. The image must be rebuilt for the supervisord.conf:46 edit and onstart changes to take effect — that rollout (image build/push + live Vast UAT: 1×3090→gemini, ≥30GB→pod-GPU no-OOM) is the explicit Wave 3 / Plan 14-03 deliverable (`autonomous: false`), NOT this plan.

## Open Items Carried to 14-03 (live UAT)

- OPEN Q1 sequencing: onstart measures `memory.free` before llama loads Qwen, so the max-free pick is effectively index 0 on a fresh 2×3090. Accepted (option (a)) — per-card headroom is large; the live no-OOM UAT in 14-03 is the gate.
- Confirm `:9100` is reachable end-to-end once forwarded (responder actually listening) during the 14-03 live UAT.

## Self-Check: PASSED

All 4 modified files + this SUMMARY.md present on disk; commits bb5eed3 (RED), 9b2097b (Task 1 GREEN), 3d1c7b1 (Task 2) found in git log.
