---
quick_id: 260528-h12
slug: breaker-snapshot-honor-force-override
status: complete
started_at: 2026-05-28T15:15:41Z
completed_at: 2026-05-28T15:30:00Z
branch: gsd/phase-06.9-close
commits:
  - 87f3060  # RED breaker
  - 64e5eab  # GREEN breaker
  - 6d1a96f  # RED health
  - 7d0b345  # GREEN health
seed: SEED-005
---

# 260528-h12 — `breaker-snapshot-honor-force-override`

## Goal

Promote SEED-005 to executable code: `/v1/health/upstreams` must reflect the same routing-relevant state the dispatcher uses (`EffectiveState`), not the raw gobreaker FSM state. After commit `23bbe01` (audit override propagation fix), validation surfaced that `gatewayctl breaker force-open` correctly drives sensitive-tenant 503 + `upstream='blocked_sensitive'` audit rows, but `/v1/health/upstreams` still reports `state="half-open"` — masking the operator action and breaking the smoke `ensure_tier0_open` pre-condition.

## What shipped

- `gateway/internal/breaker/breaker.go` — added `Set.EffectiveStateSnapshot() map[string]string`. Emits `"forced-open"` when `CheckForceOverride(name)` is true, else `cb.State().String()`. Force-override cache refresh happens BEFORE `s.mu.RLock()` so lock-hold time matches `Snapshot()`. Legacy `Snapshot()` left byte-identical (callers explicitly wanting raw FSM keep their contract).
- `gateway/internal/upstreams/health.go` — `buildHealthResponse` switched from `bs.Snapshot()` to `bs.EffectiveStateSnapshot()`. Status derivation (`health.go:166`) already treats anything ≠ `"closed"` as unhealthy → `"forced-open"` automatically classifies `degraded`/`failed`, no enum-broadening required.
- `gateway/internal/breaker/breaker_test.go` — `TestEffectiveStateSnapshot` (4 sub-tests: natural-closed, natural-open, force-override active, force-override-wins-over-natural-open).
- `gateway/internal/upstreams/health_test.go` — `TestHealthHandler_ForceOverrideEmitsForcedOpen` asserts `.upstreams.{name}.state="forced-open"` + `.status="degraded"`.

## Why it matters

- Restores parity between routing layer and observability surface — operators reading the health endpoint now see the same picture the dispatcher uses for routing decisions.
- Unblocks `smoke-sensitive-failover.py` `ensure_tier0_open()` pre-condition on a force-open-driven test bed where the local-llm pod is reachable (it already accepts `"forced-open"` in `OPEN_LIKE_STATES`, line 150).
- Lays the groundwork for SEED-006 (the smoke audit-query race) — once SEED-006 ships, the smoke runs end-to-end against any prod state without operator pod-shutdown gymnastics.

## Verification

- `go build ./...` green.
- `go vet ./...` clean.
- `go test ./internal/breaker/... ./internal/upstreams/...` green (4 new breaker sub-tests + 1 new health sub-test, plus 4 pre-existing health tests).
- Manual sanity (post-deploy operator step, not yet run):
  ```
  ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-open -upstream=local-llm -ttl=1m'
  curl -sS https://ai-gateway.converse-ai.app/v1/health/upstreams | jq '.upstreams."local-llm".state, .status'
  # expect: "forced-open" \n "degraded"
  ```

## Non-goals respected

- `Snapshot()` body unchanged.
- `scripts/integration-smoke/smoke-sensitive-failover.py` not touched (SEED-006 handles the smoke retry loop).
- Dispatcher, audit middleware, gatewayctl, Redis keys, `CheckForceOverride` semantics — all unchanged.
- No new `/admin/health/effective-state` endpoint.

## Files

- `gateway/internal/breaker/breaker.go`
- `gateway/internal/breaker/breaker_test.go`
- `gateway/internal/upstreams/health.go`
- `gateway/internal/upstreams/health_test.go`

## Follow-up

- Operator: merge `gsd/phase-06.9-close` → develop, deploy, run sanity recipe above.
- On green sanity: mark `.planning/seeds/SEED-005-health-endpoint-snapshot-ignores-force-override.md` `status: shipped`.
- SEED-006 (smoke audit-query retry loop) tracked as next quick task.
