---
created: 2026-05-22
priority: high
source: 06.8-03 live-UAT
tags: [gateway, primary, allowlist, vast, bug]
---

# BUG: PRIMARY_VAST_MACHINE_ALLOWLIST does not steer primary offer selection

Discovered during Phase 06.8 Plan 03 live-UAT (2026-05-22).

**Symptom:** the primary reconciler's allowlist-first preference pass is not effective.
With `PRIMARY_VAST_MACHINE_ALLOWLIST=43803` (host confirmed available, $0.401, rel 0.995),
`gatewayctl primary force-up` still picked an unrelated global-cheapest host (47525 Quebec).
Same with a 4-host allowlist → picked 76546 California (not in list). Selection always
broadens to the global-cheapest qualifying offer regardless of the allowlist env.

**Impact:** cannot deterministically target a known-good-CDI 2×3090 host (43803). Combined
with widespread broken-CDI on 2×3090 hosts (55942, 45778 confirmed), the gateway cannot
reliably bring a 2×3090 primary pod to markReady. Blocks GW-2GPU full validation (Phase 06.8).

**Where:** gateway/internal/config/config.go L196 (PrimaryVastMachineAllowlist parsed) +
gateway/internal/emerg/vast/types.go WithMachineAllowlist (L258) — verify the primary
reconciler actually calls the allowlist-scoped search FIRST and only broadens on zero offers.
The runtime evidence says the allowlist pass is skipped or returns the broadened result.

**Fix + re-test:** wire/repair the allowlist-first pass; then re-run the 06.8-03 2×3090
force-up UAT targeting 43803 → expect markReady + LLM/STT/TTS/DCGM 200 + nvidia-smi split.

**Catalog from this session:** broken-CDI machine_ids 55942, 45778 (now in dev blocklist);
known-good 43803 (Estonia, spike-proven).
