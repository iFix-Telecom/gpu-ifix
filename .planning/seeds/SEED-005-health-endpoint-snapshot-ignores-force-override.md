# SEED-005 — `/v1/health/upstreams` Snapshot ignores breaker force-override

**Planted:** 2026-05-28
**Discovered during:** Phase 11 audit-pipeline RES-08 fix validation (`smoke-sensitive-failover.py` post-23bbe01 deploy)
**Status:** shipped 2026-05-28 — quick `260528-h12` (commit `7d0b345`), validated against prod 2026-05-28T20:34Z (`state=forced-open` + `status=degraded`) + smoke 4/4 GREEN
**Related:** [[audit-blocked-sensitive-override-not-propagated]] debug session; commit 23bbe01; quick `260528-h12-breaker-snapshot-honor-force-override`; surfaced sibling bug in `proxy/sensitive.go:58` `SensitiveRetry` fixed via commit `1365b75` (PR #12)

## Problem

`gateway/internal/breaker/breaker.go:248-256` — `Set.Snapshot()` reads the raw `cb.State().String()` per upstream, ignoring the Redis-backed operator force-override. `gateway/internal/upstreams/health.go:134` (`buildHealthResponse`) consumes that snapshot verbatim into the `/v1/health/upstreams` payload.

Consequence: after `gatewayctl breaker force-open -upstream=local-llm -ttl=5m`, the dispatcher's `EffectiveState(local-llm) == StateOpen` (correct — sensitive 503 + `upstream='blocked_sensitive'` audit row are emitted as designed), but `/v1/health/upstreams` reports `local-llm.state="half-open"` (or `"closed"`) because the natural gobreaker FSM was never tripped by observed failures.

## Empirical evidence (2026-05-28T14:02–14:06 UTC)

```
$ ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-open -upstream=local-llm -ttl=5m'
breaker force-open: local-llm forced OPEN for 5m0s (set_by=root)

$ curl -sS https://ai-gateway.converse-ai.app/v1/health/upstreams | jq .upstreams.\"local-llm\"
{"state":"half-open","role":"llm","tier":0}

$ # but the dispatcher routes correctly via EffectiveState:
$ # row in bd_ai_gateway_prod.ai_gateway.audit_log:
$ # 14:02:54.80 | sensitive | /v1/chat/completions | 503 | blocked_sensitive
```

## Impact

1. **`smoke-sensitive-failover.py` pre-condition flake** — `ensure_tier0_open()` polls `/v1/health/upstreams` until `local-llm.state ∈ {open, forced-open, FORCED_OPEN}`. With Snapshot ignoring force-override, the smoke times out at 30s on a force-open-driven test bed even though the gateway IS correctly emitting `blocked_sensitive`. The smoke validated GREEN previously only because the local-llm pod was *naturally* unreachable (FSM tripped to OPEN organically). When the pod is reachable + breaker is operator-forced, smoke times out unevaluated.
2. **Dashboard / monitoring drift** — any caller (Phase 7 dashboard, external uptime checks) consuming `/v1/health/upstreams` sees a different upstream state than the routing layer actually uses. Operator could think "tier-0 healthy" while EVERY sensitive request is being blocked.
3. **No alarm correlation** — dashboards can't show "force-override active" as a state, even though it's persisted in Redis and exposed via `gatewayctl breaker list`.

## Why It Was Missed

- `Snapshot()` was introduced before `EffectiveState()` (Phase 06.9 Plan 04 added the force-override layer). Snapshot was never updated.
- Integration tests for the health endpoint (`gateway/internal/upstreams/health_test.go`) drive the natural FSM directly via `cb.State()` — never exercise the force-override read path.
- The smoke accepts `forced-open` / `FORCED_OPEN` state strings (`scripts/integration-smoke/smoke-sensitive-failover.py:150` `OPEN_LIKE_STATES`), but no code path in the gateway ever emits those strings into the health body.

## Scope of Fix

### Option A — Snapshot honors EffectiveState

`gateway/internal/breaker/breaker.go` — add `EffectiveStateSnapshot()` returning `map[name]→state-string` that uses `EffectiveState()` per name (refreshes force-override cache once at the top, then maps each known cb). Update `buildHealthResponse` to call the new snapshot. When force-override is in effect, emit `"forced-open"` (lowercase, hyphenated — matches what the smoke + dashboard already accept).

```go
func (s *Set) EffectiveStateSnapshot() map[string]string {
    s.mu.RLock(); defer s.mu.RUnlock()
    out := make(map[string]string, len(s.cbs))
    for n := range s.cbs {
        if s.CheckForceOverride(n) {
            out[n] = "forced-open"
            continue
        }
        out[n] = s.cbs[n].State().String()
    }
    return out
}
```

Pros: minimal change, single read site, additive.
Cons: caller must call `RefreshForceOverride` once per request (Snapshot didn't); cheap (~map read).

### Option B — Add a parallel `force_override` field to the health payload

Keep `state` as natural FSM, add `force_override: bool` per upstream. Smoke + dashboard ingest both.

Pros: preserves separation between observation-driven state + operator action.
Cons: every consumer (smoke, dashboard, external monitors) needs an update; clients that only read `.state` still see misleading data.

### Recommendation

**Option A.** Simpler, contract-correct, no consumer changes (the smoke and any reasonable dashboard already prefer "effective" state — the natural FSM is an implementation detail). Document the change in `/v1/health/upstreams` response contract.

## Test Plan (when promoted)

- Unit: `breaker_test.go` — `EffectiveStateSnapshot()` returns `forced-open` when `CheckForceOverride(name)==true`, otherwise the raw FSM string.
- Integration: `gateway/internal/integration_test/upstreams_listen_test.go` (or new) — `gatewayctl breaker force-open -upstream=local-llm -ttl=1m` + assert `/v1/health/upstreams` `.upstreams.local-llm.state == "forced-open"`.
- Smoke: rerun `smoke-sensitive-failover.py` with `--induce-failure-via=gatewayctl` on prod with a reachable local-llm pod; pre-condition passes without natural FSM trip.

## Files

- `gateway/internal/breaker/breaker.go:231-256` (EffectiveState + Snapshot)
- `gateway/internal/upstreams/health.go:119-160` (buildHealthResponse consumes Snapshot)
- `gateway/internal/upstreams/health_test.go` (add force-override coverage)
- `scripts/integration-smoke/smoke-sensitive-failover.py:150` (OPEN_LIKE_STATES — no change needed once gateway emits the expected string)
