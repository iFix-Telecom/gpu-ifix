# SEED-011 — Primary FSM stays `ready` when Vast instance is stopped/exited

**Discovered:** 2026-06-12 during 11-06 live load-test UAT (force-up saga).
**Severity:** HIGH — gateway advertises tier-0 Ready while the pod is dead; load lands on `connection refused` 502s instead of failing over cleanly.

## What happened (live evidence)

Timeline 2026-06-12 (UTC), prod gateway n8n-ia-vm:

1. `00:03:49Z` — primary FSM → `ready` (machine 28974, Vietnam, instance 40642262). Pod services all RUNNING per instance log (llama, speaches, chatterbox, dcgm at `00:02:40Z`).
2. `~00:04Z` — Vast **billing-stopped** the instance (account balance hit threshold: balance -$0.056, credit 0). Vast API: `actual_status=exited`, `intended_status=stopped`. Instance still EXISTS (404 never fires).
3. `00:04–00:06Z` — dry-run load replay: all LLM/STT requests → `dial tcp 171.247.180.119:31788: connect: connection refused` → 502 `upstream_5xx`. Embed 200 (local container).
4. `00:06–00:30Z+` — FSM **stayed `ready` for 25+ minutes**. `gatewayctl primary state` showed `state=ready` with EMPTY `lifecycle_id`/`pod_url`/`pod_instance_id` display fields. Breakers `local-llm`/`local-stt`/`local-tts` flapped open/half-open/open the whole time.
5. Recovery only via operator: container recreate → reconciler recover path logged `primary recover: instance not running; closing as orphan` (this path DOES handle it — but only runs at startup).

## Root cause hypothesis

The fix in `01e7558` (quick 260527-wgs) made the reconciler tolerate transient `ErrInstanceNotFound` with a 3-strike confirm — but the steady-state Ready reconcile loop apparently only reacts to **not-found (404)**, not to an instance that exists with `actual_status=exited` / `intended_status=stopped`. Billing-stop and host-stop both produce this shape. The startup recover path (`primary recover: instance not running; closing as orphan`) already classifies it correctly — the steady-state loop needs the same check.

## Expected behavior

Ready-state reconcile tick should treat `actual_status in {exited, stopped}` (or `intended_status=stopped`) as pod-down: advance Ready → Draining → Asleep (or re-provision), open `local-*` breakers deterministically, and emit a critical alert (billing-stop is an operator-actionable event — surface "Vast account lacks credit" distinctly).

## Repro sketch

1. Force-up primary, wait Ready.
2. `vast stop instance <id>` (or drain account credit).
3. Observe FSM stays `ready`; tenant traffic gets 502 connection-refused instead of tier-1 failover.

## Related

- `01e7558` fix(11): primary reconciler tolerates transient ErrInstanceNotFound (3-strike confirm)
- 11-06-EVIDENCE.md (load-test UAT saga, 2026-06-12 appendix)
- Secondary display bug: `gatewayctl primary state` shows empty `pod_url`/`lifecycle_id` while proxy clearly still holds the pod IP/ports — state hash and proxy routing table out of sync.
