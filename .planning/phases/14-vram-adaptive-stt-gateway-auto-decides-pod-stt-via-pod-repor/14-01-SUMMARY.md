---
phase: 14-vram-adaptive-stt
plan: 01
subsystem: infra
tags: [gateway, go, primary-pod, stt, whisper, device-gate, reconciler, vast]

# Dependency graph
requires:
  - phase: 11-2-primary-stt-restore
    provides: "speaches/whisper back on the primary pod (8001) + 3-role tier-0 roster {llm,stt,tts}"
provides:
  - "Device-report CONTRACT (GET :9100/whisper_device -> {\"whisper_device\":\"cuda\"|\"cpu\"}) that Plan 14-02 must serve on the pod side"
  - "primaryPodURLs.WhisperDevice field carrying the pod-reported device captured at Ready"
  - "Deps.DeviceReport seam + podDeviceReportURL(:9100) helper + nil-safe deviceReport() reconciler method"
  - "device-gated stt tier-0 override at the 3 reconciler sites (re-assert loop, markReady, recoverOpenLifecycle) — fires IFF WhisperDevice==cuda"
  - "Config.PrimaryPodServeSTT fully deleted (field + env load); gateway/internal has zero references"
  - "main.go primaryDeviceReport closure: bounded-body, whitelisted {cuda,cpu} fetch of the pod report"
affects: [14-02, 14-03, primary-pod-rollout]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Per-lifecycle device bool replaces config flag — compute once in buildPodURLs, carry on primaryPodURLs, read at all override sites"
    - "Whitelist / default-deny parse of an untrusted pod report — any non-{cuda,cpu} value, non-200, parse error, or unreachable pod fail-safes to \"\" (no override)"
    - "Scriptable closure seam (Deps.DeviceReport) mirrors Deps.HealthCheck for deterministic unit injection"

key-files:
  created: []
  modified:
    - gateway/internal/primary/lifecycle.go
    - gateway/internal/primary/reconciler.go
    - gateway/internal/config/config.go
    - gateway/cmd/gateway/main.go
    - gateway/internal/primary/reconciler_test.go
    - gateway/internal/primary/lifecycle_test.go

key-decisions:
  - "stt override gates on WhisperDevice == \"cuda\" exactly; cpu/missing/unknown all fail-safe to gemini-stt (preserves prod behavior during pod-image rollout)"
  - "DeviceReport whitelist enforced in BOTH the main.go closure (parse-time) AND the reconciler gate (cuda-only check) — defence in depth against a non-whitelisted value reaching the gate"
  - "nil DeviceReport / unmapped :9100 → device \"\" → no override (no hard dependency on the pod shipping the report before the gateway deploys)"
  - "Shared markReadyURLs() test helper + cuda-seeded urls literals updated so pre-existing 3-role-override assertions stay green under the new gate"

patterns-established:
  - "Pattern 1: device-report contract is interface-first — the gateway defines the parsed JSON field name + URL path here; Plan 14-02 implements the pod responder against it"
  - "Pattern 2: io.LimitReader-bounded read (8 KiB) before JSON decode on an externally-forwarded pod port (threat T-14-02 DoS mitigation, parity with upstreams/alert probes)"

requirements-completed: [STT-AUTO, STT-FAILSAFE, STT-PROBE, FLAG-REMOVE]

# Metrics
duration: 35min
completed: 2026-06-17
---

# Phase 14 Plan 01: Gateway device-gate Summary

**Replaced the manual PRIMARY_POD_SERVE_STT config flag with a per-lifecycle device gate that overrides STT tier-0 onto the pod IFF the pod self-reports whisper_device=cuda at Ready — fail-safing to gemini-stt for cpu/missing/garbage — and deleted the flag entirely.**

## Performance

- **Duration:** ~35 min
- **Started:** 2026-06-17
- **Completed:** 2026-06-17
- **Tasks:** 2/2 (RED rework + GREEN implementation)
- **Files modified:** 6

## Accomplishments

### Task 1 — RED (commit 82a1e6c)
- Renamed `TestEvaluateProvisioning_PrimaryPodServeSTT` → `TestEvaluateProvisioning_WhisperDevice`, table-driven over four cases: `cuda` (stt fires), `cpu` (skip), missing `""` (fail-safe skip), garbage `gpu0` (fail-safe skip).
- Each case drives off a scriptable `Deps.DeviceReport` closure (mirrors `HealthCheck`) and asserts both the captured `primaryPodURLs.WhisperDevice` value and the stt-override presence/absence.
- Extended `runningInstanceWithAllPorts` fixture with a `"9100/tcp"` → HostPort `33100` mapping (the device-report channel).
- Removed the `PrimaryPodServeSTT: true` seed from `cfgWithDefaults` (flag being deleted).
- Verified RED: tests referenced the not-yet-existing `DeviceReport` Deps field (`go vet` → unknown field).

### Task 2 — GREEN (commit 75a837a)
- Added `WhisperDevice string` to `primaryPodURLs` and `DeviceReport func(ctx, url) string` to `Deps`.
- Added `podDeviceReportURL(inst) = podPortURL(inst, "9100", "/whisper_device")` and a nil-safe `deviceReport(inst)` method (returns `""` when DeviceReport nil or :9100 unmapped).
- `buildPodURLs` now sets `WhisperDevice: r.deviceReport(inst)`.
- Converted the 3 override sites to gate on `urls.WhisperDevice == "cuda"`:
  - reconciler.go re-assert loop (Pitfall #11): `if role == "stt" && urls.WhisperDevice != "cuda" { continue }`
  - reconciler.go `markReady`: `if urls.WhisperDevice == "cuda" { OverrideTier0("stt", …) }`
  - reconciler.go `recoverOpenLifecycle`: same gate
- Deleted `Config.PrimaryPodServeSTT` field (config.go) and its `PRIMARY_POD_SERVE_STT` env load.
- Wired `main.go` `primaryDeviceReport` closure: 5s-timeout GET of `:9100/whisper_device`, `io.LimitReader`-bounded (8 KiB) body, JSON-decode, return value only when exactly `"cuda"`/`"cpu"` else `""`. Passed `DeviceReport: primaryDeviceReport` into `primary.Deps`.
- Updated 7 pre-existing tests that assert the 3-role override (markReady / recover / re-assert / breaker-force) to seed `WhisperDevice: "cuda"` (direct-urls tests) or wire `DeviceReport: cudaDeviceReport` (buildPodURLs-path tests), plus a shared `cudaDeviceReport` helper.

## Verification Results

- `cd gateway && go build ./...` → exit 0
- `cd gateway && go test ./internal/primary/... ./internal/config/... -count=1` →
  `ok  github.com/ifixtelecom/gpu-ifix/gateway/internal/primary  10.6s`
  `ok  github.com/ifixtelecom/gpu-ifix/gateway/internal/config  0.02s`
- `grep -rn "PrimaryPodServeSTT" gateway/internal` → CLEAN (no matches)
- `go vet ./internal/primary/ ./internal/config/ ./cmd/gateway/` → clean
- Acceptance counts: WhisperDevice lifecycle.go=3 (≥1), reconciler.go=4 (≥3); 9100 lifecycle.go=8 (≥1); DeviceReport main.go=3 (≥2).

## Threat Model Mitigations Applied

- **T-14-01 (Tampering):** Whitelist `{cuda,cpu}` enforced in the main.go parse closure AND the reconciler `== "cuda"` gate. Any other value → no override → gemini-stt (default-deny).
- **T-14-02 (DoS):** `io.LimitReader(resp.Body, 8*1024)` before JSON decode + 5s client/context timeout on the pod GET.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 — Bug] Seven pre-existing 3-role-override tests broke under the new gate**
- **Found during:** Task 2 (first `go test` run after deleting the flag).
- **Issue:** `TestEvaluateProvisioning_AllFourEndpointsHealthy_PromotesToReady`, `TestMarkReady_OverridesTier0_2Roles`, `TestRecoverOpenLifecycle_HealthyInstanceRestoresReady`, `TestEvaluateReady_ReassertsTier0WhenCleared`, `TestMarkReady_OverridesTTSNotEmbed`, `TestMarkReady_OverrideTier0_CalledFor_STT`, `TestMarkReady_ForceCloseAfterOverrideTier0` asserted the stt override fires but relied on the now-deleted `PrimaryPodServeSTT:true` default; with the new device gate, `WhisperDevice==""` skipped stt.
- **Fix:** Seeded `WhisperDevice: "cuda"` on the direct-`urls` literals (and the shared `markReadyURLs()` helper), and wired `DeviceReport: cudaDeviceReport` on the buildPodURLs-path tests. Added a shared `cudaDeviceReport` helper.
- **Files modified:** gateway/internal/primary/reconciler_test.go
- **Commit:** 75a837a
- **Rationale:** In-scope — these failures are a direct consequence of this task's gate swap, not pre-existing unrelated failures.

## Device-Report Contract (for Plan 14-02)

```
GET http://<pod-ip>:<host-port-for-9100>/whisper_device
  -> 200 application/json
  body: {"whisper_device":"cuda"}  |  {"whisper_device":"cpu"}
```
Gateway whitelists exactly `{"cuda","cpu"}`. ANY other value, non-200, parse error, missing 9100 port mapping, or unreachable pod → device `""` → NO stt override (fail-safe → tier-1 gemini-stt). Plan 14-02 must stand a static responder on container port 9100 serving this path, and the pod's `-p 9100:9100` forward must be added so the gateway can reach it.

## Known Stubs

None — the device-report fetch is fully wired in main.go; the only "missing" piece is the pod-side responder, which is the explicit Wave 2 deliverable (Plan 14-02) and is covered by the documented fail-safe (unknown device → gemini-stt) during the rollout window.

## Self-Check: PASSED

All 6 modified files + SUMMARY.md present on disk; both commits (82a1e6c RED, 75a837a GREEN) found in git log.
