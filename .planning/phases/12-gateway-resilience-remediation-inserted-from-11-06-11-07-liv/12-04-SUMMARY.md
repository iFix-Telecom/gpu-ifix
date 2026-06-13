---
phase: 12-gateway-resilience-remediation
plan: 04
subsystem: infra
tags: [chaos-testing, vast, resilience, failover, cold-start, minio]

requires:
  - phase: 12-01
    provides: RES-12 prober/health tier-0 parity + override flag + D-13 force-CLOSE + obs counters
  - phase: 12-02
    provides: RES-11 Ready-tick death detection (3-strike not_found) + drain + critical alert
  - phase: 12-03
    provides: RES-13 dial-failure tier-1 fallthrough interceptor
provides:
  - Dev-first live chaos validation (D-16) — all three remediation gates proven together on a real Vast kill
  - Three pod cold-start infra fixes required to reach a Ready pod (mc bake, cold-start budget+geo, chatterbox TTS pre-provision)
affects: [12-05]

tech-stack:
  added: [chatterbox TTS weights pre-provisioned to MinIO]
  patterns: [4th weight follows the Qwen/whisper/bge MinIO+offline strategy]

key-files:
  created:
    - .planning/phases/12-gateway-resilience-remediation-inserted-from-11-06-11-07-liv/12-04-DEV-CHAOS-UAT.md
  modified:
    - gateway/internal/primary/onstart.go (mc timeout + 4th download + hub/ extract)
    - gateway/internal/primary/lifecycle.go (chatterbox env + SHA gate)
    - gateway/internal/config/config.go (chatterbox weights config)
    - gateway/internal/primary/lifecycle_test.go (chatterbox SHA fixture)
    - gateway/docker-compose.yml (chatterbox env + cold-start budget)
    - pod/primary/Dockerfile (bake mc + openssh)
    - pod/primary/supervisord.conf (HF_HUB_OFFLINE for chatterbox)

key-decisions:
  - "Dev chaos passed ALL gates (S1-S5) on a real Vast kill — cleared for prod gate 12-05"
  - "Three pod cold-start infra bugs found+fixed; all orthogonal to RES-11/12/13 (gateway resilience) but blocking the live test"
  - "S2 fallthrough served via breaker-open path (breaker open at kill); RES-13 dial-failure interceptor covered by 12-03 tests, end-to-end D-18 zero-502 confirmed live"

patterns-established:
  - "Pod weights: ALL model artifacts pre-provisioned to MinIO + offline load (no runtime HF/dl.min.io fetch)"
  - "Vast host selection for fast cold-start: pin US allowlist (MinIO is in São Paulo BR)"
---

## What was delivered

Live dev-first chaos validation (D-16) of the Phase 12 resilience remediation. A
real Vast primary pod (machine 129536, California) was killed via Vast API DELETE
during ~20-concurrency load with a sensitive-tenant stream. **All five scenarios
passed** — full results + evidence in `12-04-DEV-CHAOS-UAT.md`.

### Chaos result (vs the 11-07 failure this remediates)

| Gate | 11-07 (broken) | 12-04 (fixed) |
|------|----------------|---------------|
| RES-11 death detection | FSM stayed `ready` 25+min on dead pod | death confirmed in ~6s (3-strike not_found) → drain → asleep; counter +1; critical alert fired |
| RES-13 zero-502 (D-18) | 100× `upstream_unreachable` 502 | **0** `upstream_unreachable`; 649 normal reqs served 200 via tier-1 OpenRouter |
| RES-08 sensitive (D-10) | n/a | 72 sensitive reqs → 503 `blocked_sensitive`, never tier-1 |

### Cold-start infra fixes (prerequisites, all committed)

Reaching a Ready pod required fixing three latent pod-provisioning bugs, none of
which are part of the RES-11/12/13 gateway code but all of which blocked the live
test (and would affect production):

1. **`8bf983b`** — bake `mc` + `openssh-server` into the pod image. The onstart
   fetched `mc` from dl.min.io at boot with no timeout; dl.min.io throttled to
   ~45 KB/s → pod hung forever in "installing mc", supervisord never started.
2. **`f8a7de4`** — raise `PRIMARY_PROVISION_COLDSTART_BUDGET_SECONDS` to 3600 and
   pin a US machine allowlist. The MinIO weights store is in São Paulo (not the
   assumed Hetzner DE); 16GB+ download from distant hosts overran the 40min budget.
3. **`f8a7de4` + `cc4b07d`** — pre-provision the chatterbox TTS model to MinIO and
   load it offline (`HF_HUB_OFFLINE=1`, cache in `…/models/hub/`). `from_pretrained()`
   hit huggingface.co at boot, crash-looping on HF-unreachable hosts so the TTS
   `/health` never came up and the pod never reached Ready (latent prod bug).

### Verification

`go build ./...` clean; `go test ./internal/...` green (chatterbox SHA fixtures
added). Live chaos D-18 gate: authoritative audit_log query returns 0
connection-class 502 for normal tenants in the kill window. Vast spend ≈ $1.49
(within the $1.50 dev budget), 0 orphan instances post-kill.

## Self-Check: PASSED
