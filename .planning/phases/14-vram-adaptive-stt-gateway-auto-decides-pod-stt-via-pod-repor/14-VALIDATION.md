---
phase: 14
slug: vram-adaptive-stt
status: planned
nyquist_compliant: true
wave_0_complete: false
created: 2026-06-17
---

# Phase 14 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.
> Source: 14-RESEARCH.md "## Validation Architecture" section.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | go test (gateway, table-driven) + bash -n (pod onstart/UAT) |
| **Config file** | none — gateway uses stdlib testing |
| **Quick run command** | `cd gateway && go test ./internal/primary/... -run "WhisperDevice|Onstart|CreateRequest" -count=1` |
| **Full suite command** | `cd gateway && go test ./internal/primary/... ./internal/config/... -count=1` |
| **Estimated runtime** | ~15 seconds |

---

## Sampling Rate

- **After every task commit:** Run quick run command
- **After every plan wave:** Run full suite command
- **Before `/gsd:verify-work`:** Full suite green + 2 live Vast UATs
- **Max feedback latency:** ~15 seconds (unit); live UAT out-of-band

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Test Type | Automated Command | Status |
|---------|------|------|-------------|-----------|-------------------|--------|
| 14-01-T1 (rework flag-pinned tests → device, RED) | 14-01 | 1 | STT-AUTO, STT-FAILSAFE | unit (RED) | `cd gateway && go vet ./internal/primary/ 2>&1 \| grep -qi "WhisperDevice\|DeviceReport"` | ⬜ pending |
| 14-01-T2 (WhisperDevice field + 3-site gate + flag delete + main.go wire, GREEN) | 14-01 | 1 | STT-AUTO, STT-FAILSAFE, STT-PROBE, FLAG-REMOVE | unit + compile + source-grep | `cd gateway && go build ./... && go test ./internal/primary/... ./internal/config/... -count=1` | ⬜ pending |
| 14-02-T1 (onstart VRAM→device export + :9100 responder, grep-gate) | 14-02 | 2 | POD-VRAM, STT-PROBE | unit (grep-gate) | `cd gateway && go test ./internal/primary/... -run "PrimaryOnstart\|Onstart" -count=1` | ⬜ pending |
| 14-02-T2 (supervisord cpu-pin drop + -p 9100 forward) | 14-02 | 2 | STT-AUTO, POD-VRAM | unit + source-grep | `cd gateway && go test ./internal/primary/... -run "CreateRequest\|Lifecycle" -count=1` | ⬜ pending |
| 14-03-T1 (image build/push + uat-14.sh author) | 14-03 | 3 | STT-SHAPE-3090, STT-SHAPE-GPU | syntax | `bash -n pod/smoke/uat-14.sh` | ⬜ pending |
| 14-03-T2 (2 live Vast UATs) | 14-03 | 3 | STT-SHAPE-3090, STT-SHAPE-GPU, STT-MIGRATE | live UAT (manual) | manual Vast provision + real-audio POST + OOM grep | ⬜ pending |
| 14-03-T3 (verdict rollup) | 14-03 | 3 | FLAG-REMOVE | source | `test -f 14-VERIFICATION.md` | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] Rework `gateway/internal/primary/reconciler_test.go:642-717` to drive off the device signal (was flag) — 14-01-T1.
- [ ] `runningInstanceWithAllPorts` fixture (reconciler_test.go:422) gains a `"9100/tcp"` mapping + scriptable DeviceReport stub — 14-01-T1.
- [ ] `gateway/internal/primary/lifecycle_test.go:42-52` fixture: remove `PrimaryPodServeSTT` — 14-01-T1.
- [ ] onstart grep-gate test asserts the nvidia-smi/device-export/:9100 block — 14-02-T1.

*Existing go test infrastructure covers all gateway requirements; no new framework.*

---

## Manual-Only Verifications (live Vast UAT — phase gate)

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| 1×3090 (24GB): STT routes to gemini-stt, 200, ~2-3s; pod reports cpu | STT-SHAPE-3090 | needs real Vast pod + real audio | provision 1×3090, assert :9100/whisper_device=cpu, STT real-audio → gemini (200, not pod), no override set |
| ≥30GB (2×3090 or 5090): STT routes to pod whisper-GPU, <5s, no CUDA OOM; pod reports cuda | STT-SHAPE-GPU | needs real multi-GPU Vast pod | provision shape, assert :9100/whisper_device=cuda, STT real-audio < ~5s served by pod, assert NO CUDA OOM in pod speaches.log (device_index off the contended GPU) |
| Rollout window: old pod image (no :9100 / no whisper_device) → gateway uses gemini | STT-MIGRATE | timing-dependent on image rollout | with a pod whose :9100 is unreachable/absent, assert gateway device="" → does NOT override STT |

---

## Validation Sign-Off

- [x] All gateway tasks have automated go-test verify
- [x] Sampling continuity: no 3 consecutive tasks without automated verify
- [x] Wave 0 reworks the 2 flag-pinned tests (14-01-T1)
- [ ] Both live Vast UATs (3090 + ≥30GB) pass before verify-work
- [x] `nyquist_compliant: true` set after planner filled Task IDs

**Approval:** task IDs filled; nyquist_compliant=true. Live UATs pending execution (14-03).
