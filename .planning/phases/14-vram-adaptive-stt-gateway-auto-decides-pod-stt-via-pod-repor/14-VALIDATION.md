---
phase: 14
slug: vram-adaptive-stt
status: draft
nyquist_compliant: false
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
| **Framework** | go test (gateway, table-driven) |
| **Config file** | none — gateway uses stdlib testing |
| **Quick run command** | `cd gateway && go test ./internal/primary/... -run STT -count=1` |
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
| (gateway) device-gate replaces flag at reconciler :448/:875/:1631 | TBD | TBD | STT-AUTO | unit | `go test ./internal/primary/... -run PrimaryPodServeSTT\|WhisperDevice -count=1` | ⬜ pending |
| (gateway) missing/unknown device → no override (fail-safe gemini) | TBD | TBD | STT-FAILSAFE | unit | `go test ./internal/primary/... -run FailSafe -count=1` | ⬜ pending |
| (gateway) WhisperDevice captured into primaryPodURLs from :9100 | TBD | TBD | STT-PROBE | unit | `go test ./internal/primary/... -run Probe -count=1` | ⬜ pending |
| (pod) onstart VRAM→device computation | TBD | TBD | POD-VRAM | unit/script | shellcheck + a device-decision unit on the computed env | ⬜ pending |
| (config) PrimaryPodServeSTT removed | TBD | TBD | FLAG-REMOVE | source | `! grep -rn PrimaryPodServeSTT gateway/internal` | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] Rework `gateway/internal/primary/reconciler_test.go:642-717` to drive off the device signal (was flag).
- [ ] `gateway/internal/primary/lifecycle_test.go:42-52` fixture: replace `PrimaryPodServeSTT` with device-bearing `primaryPodURLs`.

*Existing go test infrastructure covers all gateway requirements; no new framework.*

---

## Manual-Only Verifications (live Vast UAT — phase gate)

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| 1×3090 (24GB): STT routes to gemini-stt, 200, ~2-3s | STT-SHAPE-3090 | needs real Vast pod + real audio | provision 1×3090, assert pod whisper_device=cpu/disabled, STT real-audio → gemini (200, not pod), no override set |
| ≥30GB (2×3090 or 5090): STT routes to pod whisper-GPU, <5s, no CUDA OOM | STT-SHAPE-GPU | needs real multi-GPU Vast pod | provision shape, assert whisper_device=cuda, STT real-audio < ~5s served by pod, assert NO CUDA OOM in pod logs (device_index off the Qwen-loaded GPU) |
| Rollout window: old pod image (no whisper_device field) → gateway uses gemini | STT-MIGRATE | timing-dependent on image rollout | with a pod reporting no field, assert gateway does NOT override STT |

---

## Validation Sign-Off

- [ ] All gateway tasks have automated go-test verify
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 reworks the 2 flag-pinned tests
- [ ] Both live Vast UATs (3090 + ≥30GB) pass before verify-work
- [ ] `nyquist_compliant: true` set after planner fills Task IDs

**Approval:** pending
