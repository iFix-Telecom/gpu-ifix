# SEED-014 — Primary cold-start failure loop is invisible: provisioning-failure alerts are info-only, breaker noise drowns the real signal, no digest, no failure-count circuit-break

**Discovered:** 2026-06-16 during live pod-traffic validation (prod gateway n8n-ia-vm, rev 99e4e09).
**Severity:** MED — no user-facing outage (OpenRouter tier-1 covers), but the primary pod was DOWN ~3.5h across 5 consecutive failed cold-starts and nobody was notified; wasted Vast spend + chronic run on the more expensive external tier, indefinitely, silently.
**Related:** [[SEED-011-fsm-ready-with-stopped-instance]] (death-of-ready-pod, RES-11 — different failure mode), [[SEED-012-prober-ignores-tier0-override]] + [[SEED-013-probe-hardcodes-qwen-model-no-per-upstream-rewrite]] (probe-truth — the chronic `local-llm` breaker noise originates here), 12-FIELD-FINDINGS-2026-06-16. Target phase: Phase 7 (observability/alerting) and/or Phase 12 follow-up.

## What happened (live evidence)

Prod, 2026-06-16. After an operator `primary force-down` (~14:09 UTC), the schedule re-provisioned the primary pod and **5 consecutive attempts failed over ~3.5h**, all served by OpenRouter tier-1 (no outage):

```
LC43 17:29Z provisioning (current)
LC42 17:24Z ✗ vast_status_msg_error: ghcr.io TLS handshake timeout (image pull)
LC41 16:19Z→17:19Z ✗ health_timeout (~1h, never reached 4 healthy endpoints)
LC40 15:14Z→16:14Z ✗ health_timeout
LC39 14:09Z→15:09Z ✗ health_timeout
```

`gatewayctl primary state` = `provisioning`, `pod_url` empty. A live `POST /v1/chat/completions` was served by `deepseek/deepseek-v4-flash` / provider Novita (tier-1), confirming **tier-0 receives zero traffic** — the pod is not serving.

## Why it was invisible (root causes)

1. **The useful alerts are info-only and never dispatched externally.** Every `primary:*` FSM alert (`provisioning_started`, `transition`, `health_timeout` path, `draining`, etc.) is classified **severity=info** → alerter logs `"no external channels for tier"` → never sent to ClickUp/Brevo. Over 6h: 21 `primary:*` events, all `info`. There is no distinct `primary:provisioning_failed` / `primary:health_timeout` / `primary:tier0_down_extended` alert at warning/critical.

2. **The alert that DOES fire is chronic noise.** `breaker:local-llm:open` (critical) dispatched **40×** to clickup+brevo in 4h (plus 278 deduplicated). But the `local-llm` breaker flaps open routinely (the probe-truth gap — SEED-012/013 family: probe hits a dead static URL / sends a model the upstream rejects), so operators are trained to ignore "Circuit breaker local-llm → open". When the pod ACTUALLY dies, the same low-context alert fires — drowned in the noise. The title says nothing actionable ("pod failed cold-start 5×", "tier-0 down 3.5h", "running on OpenRouter", "burning $") — just a breaker state.

3. **No daily digest on the prod host.** `systemctl --user list-timers` on n8n-ia-vm = **0 timers**; no crontab. The "daily report 9:30 via systemd user timer + Brevo" referenced in ops memory is NOT present/running on this host, so no aggregate ("primary failed 5× today") catches what the alert stream buried.

4. **Failover masks the user symptom.** OpenRouter tier-1 serves everything → apps get 200s → zero complaints. The "failover invisível" core value doubles as a blind spot.

5. **Near-zero organic traffic** (~1 req/day) → nobody exercising the gateway → no discovery through use. Only surfaced via manual investigation.

## Kill-and-retry IS happening — but the loop is broken

The operator-expected "breaker open → kill pod → try a new one" is occurring, driven by the FSM health-poll timeout (NOT the breaker): each pod health_timeouts at ~1h → destroy → schedule re-provisions next cycle (5 `provisioning_started` events). Problems:
- **~1h burned per failed attempt** before giving up (health budget).
- **All attempts fail identically** (health_timeout = pod boots but ≥1 of the 4 endpoints — llama/speaches/infinity/dcgm — never reaches healthy; plus one ghcr image-pull TLS error) → an infinite *broken* retry loop that cannot converge by retrying.
- **No "N consecutive cold-start failures → stop + escalate"** — provisioning is never circuit-broken; it will loop indefinitely, info-level, burning 3090 pods while OpenRouter carries load.
- Distinct from RES-11 (Phase 12): RES-11 detects a pod that was **Ready then died** (fast 3-strike). These die **in provisioning, never Ready** → slow health_timeout path, no fast detection, no distinct alert.

## Fix directions (Phase 7 / Phase 12 follow-up)

1. **Distinct, high-severity, contextful alert** on provisioning failure: emit `warning`→escalating-`critical` on `health_timeout` / Vast instance error / `provisioning_failed`, with attempt count + reason + "serving on tier-1 since T" + cost-so-far. Route to external channels (these are exactly the events currently info-only).
2. **Provisioning circuit-breaker:** after N consecutive cold-start failures (e.g. 3), STOP auto-retrying the schedule loop, fire ONE critical "primary cold-start failing repeatedly — manual intervention" alert, and stay on tier-1 (stop burning dead pods). Reset on operator action or backoff window.
3. **"tier-0 down while serving tier-1 for > X min"** alert — the single most useful operator signal; independent of breaker flap.
4. **De-noise the breaker alert** — gate `breaker:local-llm` external dispatch on probe-truth (depends on SEED-013/012) so it only fires on genuine pod-down, not chronic probe false-positives.
5. **Restore/confirm the daily digest** on the prod host (n8n-ia-vm) so a once-a-day aggregate catches silent streaks regardless of per-event alerting.

## Open item

- Could not extract literal alert destinations (`CLICKUP_ALERT_LIST_ID`, `ALERT_EMAIL_TO`) — gateway is distroless and `/proc/<pid>/environ` returned empty (config likely via mounted secret, not process env). Channels ARE configured (proven: 40 dispatches to clickup+brevo). Follow-up: confirm the destination ClickUp list + Brevo recipient are actually watched by a human (if not, that's the final link in why-not-perceived). Pull from the Portainer stack config to verify.
