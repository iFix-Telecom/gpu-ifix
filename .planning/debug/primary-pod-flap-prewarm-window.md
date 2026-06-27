---
status: resolved
trigger: "Pod primary do gateway FLAPA durante a janela de pre-warm (provision-lead): cria+destrói 6-14 lifecycles Vast/dia, todos durante UpHour−lead até UpHour. MODE --diagnose ONLY."
created: 2026-06-25
updated: 2026-06-25
---

## Current Focus

hypothesis: CONFIRMED — provision gate uses ShouldBeProvisioned (includes lead) but drain gate uses IsInPeak (excludes lead); during [UpHour-lead, UpHour) the two disagree → provision then immediate drain → flap.
test: read schedule.go + reconciler.go; confirm callers; confirm live logs.
expecting: drain reason = schedule_window_exited fired inside lead window while ShouldBeProvisioned=true.
next_action: deliver Root Cause Report (diagnose-only, NO edits).

## Symptoms

expected: pre-warm provisions pod at 08:30 (UpHour-lead) and KEEPS it up through 09:00 so it is Ready by UpHour.
actual: pod reaches Ready then drains ~0.4s later, repeating 6-14 times between 08:30 and 09:00; one lifecycle started ~08:59:49 finally stays past 09:00.
errors: no error; INFO "primary drain complete; transitioning to Destroying" inflight:0 elapsed_seconds:0 grace_seconds:300, repeating.
reproduction: schedule UpHour=9 DownHour=17 Days=mon-fri ProvisionLeadSeconds=1800, DISABLED=false; observe 08:30-09:00 BRT.
started: structural since the pre-warm offset (reviews #8) was added.

## Eliminated

- hypothesis: whisper_device WARN triggers the drain
  evidence: WARN is emitted inside markReady (Provisioning→Ready) as a pure cost-regression log; it does not call startDrain. Drain transition follows 0.4s later via evaluateReady's IsInPeak gate. STT routing to tier-1 is expected for a cpu shape.
  timestamp: 2026-06-25
- hypothesis: Ready-tick death poll (handleConfirmedDeath) drains the pod
  evidence: death drain reason is "primary_instance_dead_*" and requires 3-strike confirmed terminal/not-found; pod is freshly healthy. No such reason in logs. Only schedule_window_exited path matches.
  timestamp: 2026-06-25

## Evidence

- timestamp: 2026-06-25
  checked: gateway/internal/primary/schedule.go ShouldBeProvisioned vs IsInPeak
  found: ShouldBeProvisioned (212-226) returns true when IsInPeak OR (next transition is "up" AND 0 < delta <= lead). IsInPeak simple-window branch (172-177) requires hour >= UpHour. So in [UpHour-lead, UpHour) ShouldBeProvisioned=true but IsInPeak=false.
  implication: provision-on vs stay-up gates disagree by exactly the lead offset.

- timestamp: 2026-06-25
  checked: reconciler.go evaluateAsleep (393) and evaluateReady (479-481)
  found: provision gate = ShouldBeProvisioned (393, includes lead); drain gate = IsInPeak (479) → startDrain("schedule_window_exited", 480).
  implication: the two load-bearing conditions are at reconciler.go:393 and reconciler.go:479.

- timestamp: 2026-06-25
  checked: live docker logs ifix-ai-gateway 08:30-09:00 BRT on n8n-ia-vm
  found: provisioning(08:30:00) → ready(08:37:32) → draining(08:37:32, +0.4s) → drain complete(inflight:0 elapsed:0) → destroying → asleep → provisioning, looping 6 times until provisioning at 08:59:49 reaches ready after 09:00 and stays.
  implication: matches predicted flap exactly; the surviving lifecycle is the first one that reaches Ready at/after UpHour when IsInPeak flips true.

- timestamp: 2026-06-25
  checked: all startDrain / IsInPeak / ShouldBeProvisioned callers
  found: only schedule-driven drain is reconciler.go:480 (IsInPeak gate); only schedule-driven provision is reconciler.go:393 (ShouldBeProvisioned gate). No other auto-drain path active (force-down requires operator; death requires 3-strike).
  implication: no alternate cause; offset discordance is the sole mechanism.

## Resolution

root_cause: evaluateAsleep provisions during the pre-warm lead window via ShouldBeProvisioned (now >= UpHour-lead), but evaluateReady decides whether to keep the pod up via IsInPeak (now >= UpHour). For now in [UpHour-lead, UpHour) the provision gate says "provision" while the drain gate says "drain" → Ready pod is immediately drained+destroyed and re-provisioned every ~5min until clock reaches UpHour.
fix: (diagnose-only — not applied) make the keep-up/drain gate honor the lead window during pre-warm, e.g. evaluateReady should not drain while ShouldBeProvisioned(now)==true; OR add a ShouldStayUp(now) = IsInPeak(now) || (within lead before next up) used at reconciler.go:479. See report for options + blast radius.
verification: pending fix.
files_changed: []
