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

## Companion problem — slow/stuck coldstart burns money; no fast-fail, no abort control (2026-07-06, same session)

After blocklisting the SA host, the picker chose Norway machine `24953` (instance `44032205`, `51.175.30.169`). It **pulled the image fine** (~4min) then the onstart went **silent** — Vast `actual_status` stuck at `created` (never `running`), zero onstart output after `"Successfully loaded image"` (13:42:35), and the gateway never logged `running, ports published` for it. It sat there **16min** with the instance billing the whole time, and the FSM would have waited the full `coldstart_budget_s` (**3600s / 60min**) before auto-destroying + retrying. A human had to `DELETE /instances/44032205` via the Vast API directly to stop the bleed.

Gaps this exposes (operator words: "ficar à mercê esperando +10min pra ver se deu certo é ruim, gasta dinheiro demais"):

1. **`coldstart_budget_s=3600` is far too long as a stall ceiling.** A container stuck at docker `created` (no process, no port, no health) is indistinguishable from a live-but-slow weights download only if we have no progress signal. We bill up to 60min on a dead container.
2. **No coldstart progress/liveness signal → no fast-fail.** The reconciler polls Vast `get_instance` for `actual_status==running` but does not bound the `created→running` transition separately. Add a short **`created→running` sub-budget** (e.g. 90–180s): if Vast never reports `running` in that window, destroy + retry immediately (distinct from the longer weights-load budget that only starts once the pod process is actually up and health-polling).
3. **No admin abort / force-retry endpoint.** `main.go` mounts only `GET /primary/lifecycle`, `GET/PATCH /primary/config`. To kill a doomed provision I had to go around the gateway to the Vast API. Add `POST /admin/primary/abort` (destroy current instance + close lifecycle + cooldown) so ops (and the dashboard) can one-click retry without raw Vast credentials.
4. **No real-time coldstart monitoring/alerting.** The stall is only visible by manually polling `/admin/primary/lifecycle` + Vast `get_instance` + the S3 onstart log. Surface `elapsed_in_state` per FSM phase + fire an alerter event when `provisioning`/`created` exceeds a threshold, so a stuck coldstart pages instead of silently burning budget.
5. **Open risk — is it the machine or the pod image?** Only ONE reachable machine has been observed and it stalled at `created` with no onstart output. If the NEXT reachable machine also stalls identically, the fault is the **pod image / entrypoint** (weights-load hang, bad R2 creds, OOM on 3090), NOT the host — and blocklisting machines is useless. Needs one clean run to disambiguate. (SA never got far enough to tell; it failed at TCP-reach, a different stage.)

These belong together with the auto-blocklist above under a "primary-pod provisioning resilience + observability" phase.

## Companion problem 2 — cost model ignores storage + bandwidth; cold pulls dominate (2026-07-06)

While chasing a reachable host we hit 3 distinct failure modes on cheap RTX 3090s in a row:
`97859` (SA, TCP-unreachable), `24953` (Norway, stuck at docker `created`), `43503` (15min+ still `Pulling from ...` — slow host). The `allowlist=[43803,55158]` (hosts that would have weights cached) had **no live offers**, so every attempt cold-pulls from scratch.

Cost findings:

1. **Cap checks `dph` (GPU only), not `dph_total`.** The picker/cap uses the offer `dph` (e.g. $0.116/h GPU). The real Vast rate is `dph_total` = GPU + storage: for machine 43503, `dph_base=$0.107` + `storage_total_cost=$0.060` = **`dph_total=$0.167/h`** for a 50GB disk. On a machine with a big/expensive disk this gap grows and the "under-cap" pick can blow the real budget. Cap should be evaluated against `dph_total` (+ a bandwidth estimate), not `dph`. (The operator's reported "$0.61" was a decimal misread of the console's `$0.061/h` disk line — real running rate ≈ $0.168/h, ~$51/mo at 14h×22d. Not unviable steady-state.)

2. **Every cold host re-pulls the 7.46 GB pod image** (`ghcr.io/ifixtelecom/converseai-primary-pod:main`, 30 layers, compressed) **plus the model weights from R2** (Qwen3-30B + Whisper + BGE-M3, tens of GB). Vast bills inet at ~$0.016/GB down → **~$0.5-1 one-time per coldstart just in download**, and **every failed retry re-pays it**. With a tight retry loop against bad hosts this is the dominant burn, not the GPU-hour. Mitigations: (a) fast-fail (companion problem 1) to stop paying downloads on doomed hosts; (b) actually leverage the cached-weights **allowlist** — but it's empty, so either widen it, or bake weights into a Vast **template/volume** that survives, or accept R2 egress and just minimize coldstarts.

3. **Disk billed while allocated.** Console shows 50GB disk = **$43.3/mo if kept allocated 24/7**. Confirm the schedule down-cycle **destroys** the instance (not just stops it) so disk isn't paid overnight/weekends. If it only stops, that's ~$43/mo of pure idle disk.

4. **No admin abort meant manual Vast API `DELETE`** three times this session to stop billing on stuck/slow instances — reinforces companion problem 1's `POST /admin/primary/abort` ask.

**Decision this session (2026-07-06):** paused the primary pod — `schedule_disabled=true` restored, `failure_cooldown_s` restored to 300, all Vast instances destroyed (zero billing), tier-1 openrouter serving 100% of LLM (200 OK). Re-enable only after the provisioning-resilience + cost-model work above lands. `97859` left in the persistent blocklist.

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
