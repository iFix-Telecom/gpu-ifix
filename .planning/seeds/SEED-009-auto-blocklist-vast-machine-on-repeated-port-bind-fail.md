# SEED-009 — auto-blocklist a Vast machine after repeated port-bind-timeout (no self-defense today)

**Planted:** 2026-07-06
**Discovered during:** enabling the primary pod schedule (`schedule_disabled` false) — pod stuck in a provision→fail→retry loop against one bad machine
**Status:** seed — not yet promoted to phase
**Related:** `gateway/internal/emerg/` (primary reconciler FSM), `gateway/internal/emerg/vast/` (Vast client), pod_config `vast_machine_blocklist`, `gateway/internal/admin/config_write.go` (`case "blocklist"`)

## Problem

When a Vast.ai machine boots the instance (image loads, `actual_status: running`, ports published) but the host is **TCP-unreachable** from the gateway, the reconciler hits `"primary: public port bind timeout"` (`port_bind_budget_s`, default 120s), destroys the instance, sleeps `failure_cooldown_s` (300s), then **retries and picks the exact same cheapest machine again**. Infinite loop until a human blocklists it.

Observed 2026-07-06: machine `97859` (offer `39707892`, South Africa `197.185.167.123`, RTX 3090 $0.1155/h) — the market-cheapest RTX 3090 — failed port-bind **6 consecutive times** (lifecycles 130→132) over ~30min. Each attempt spun a real Vast instance for ~2min (billed per-second) then destroyed it. `allowlist_preferred` mode did NOT help: the allowlist `[43803,55158]` had no live offers, so it broadened to the full pool and re-picked the same bad machine. Only a manual `PATCH /admin/primary/config {kind:config,field:blocklist,...}` adding `97859` broke the loop (next pick → Norway machine `24953`, provisioned clean).

## Root cause

Two missing self-defense mechanisms:

1. **No auto-blocklist.** grep of `gateway/internal/emerg/` + `internal/emerg/vast/` confirms the failure path only calls `DestroyInstance` (DELETE `/instances/{id}`). A `port-bind-timeout` (or any provision failure) records lifecycle events + fires an alerter classification, but never appends the offending `machine_id` to `vast_machine_blocklist`. The blocklist is **manual-only** (config PATCH / env `PRIMARY_VAST_MACHINE_BLOCKLIST`).
2. **Deterministic re-pick.** `market_cheapest` / `allowlist_preferred` fallback sorts by DPH and takes the top offer. With no memory of recent failures, the same cheapest-but-broken machine wins every round.

Net: a single misbehaving cheap host can pin the primary pod down indefinitely (tier-0 never comes up; all LLM traffic rides tier-1 openrouter) and slowly burn cents on doomed ~2min instances.

## Proposal (sketch)

Add an in-FSM failure memory that auto-appends a `machine_id` to the effective blocklist after N consecutive provision failures on that machine, so the picker skips it without a human:

- Track `(machine_id → consecutive_provision_failures)` in the reconciler (in-memory is enough; the leader owns provisioning). Reset the counter for a machine on any successful `first_health_pass`.
- Threshold (e.g. **2** consecutive `port-bind-timeout` / `provisionLifecycle` errors on the same `machine_id`) → persist it into pod_config `vast_machine_blocklist` via the existing `UpdatePodConfigFieldBlocklist` query (durable across restarts + visible in the dashboard), OR keep an ephemeral per-leader "cooldown blocklist" with a TTL (e.g. 24h) if we don't want the DB list to grow unbounded.
- Emit an alerter event (`primary:machine_auto_blocklisted`) so operators see which host was parked and why.
- Optional TTL/expiry so a temporarily-flaky host can re-enter the pool later (port-unreachable is often a transient host firewall/NAT state, not permanent).

Design open question: **persist to pod_config (durable, human-visible, but grows and needs pruning) vs ephemeral per-leader set with TTL (self-healing, but lost on gateway restart / leader change).** Leaning ephemeral+TTL for the auto path, keeping the DB `vast_machine_blocklist` for deliberate operator bans — the two lists union at search time.

## Not in scope

- Reporting the bad host to Vast.ai. The Vast client exposes only `search/create/get/destroy/ping` — there is **no** "report/rate host" API call, and Vast's site-side host rating would require a new endpoint the gateway doesn't implement. Out of scope for this seed (would be a separate integration).

## Empirical evidence (2026-07-06)

```
# 6 consecutive picks, all machine 97859 (only instance_id changes per retry):
$ ssh worker-vm 'docker logs <gw> --since 90m 2>&1 | grep "offer picked" | grep -oE "machine_id\":[0-9]+" | sort | uniq -c'
      6 machine_id":97859

# every attempt: instance runs, ports published, host TCP-unreachable → bind timeout
"primary provisioning: running, ports published, host TCP-unreachable" ... "llm_url":"http://197.185.167.123:40803/v1/models" ... budget_s:120
"primary provisionLifecycle returned error" ... err="primary: public port bind timeout"
"Primary pod FSM → asleep"   # cooldown 300s, then re-picks 97859

# no auto-blocklist in code:
$ grep -rniE "blocklist|blacklist|append.*machine" gateway/internal/emerg/ --include=*.go | grep -v _test
# → only reads VastMachineBlocklist; nothing writes/appends on failure

# manual fix that broke the loop:
PATCH /admin/primary/config {kind:config, field:blocklist, value:[...,97859]}  → next pick = machine 24953 (Norway), provisioned clean
```

## Effort

Small–medium. Self-contained in the primary reconciler FSM + a threshold const + reuse of the existing `UpdatePodConfigFieldBlocklist` query (or a new in-memory TTL set). No schema change if ephemeral; if persisted, reuse the existing `vast_machine_blocklist` column. Add one alerter fingerprint + a unit test for the counter/threshold/reset logic.
