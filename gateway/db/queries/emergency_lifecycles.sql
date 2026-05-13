-- name: InsertEmergencyLifecycle :one
-- Creates a new emergency lifecycle row with started_at = NOW(). Returns the
-- BIGSERIAL id so the caller can attach Vast IDs later via UpdateEmergencyLifecycleVastIDs.
-- The partial unique index `emergency_live_singleton` (D-B5) guarantees that
-- only one row may exist with ended_at IS NULL at any time.
INSERT INTO ai_gateway.emergency_lifecycles (started_at, trigger_reason, leader_replica)
VALUES (NOW(), $1, $2)
RETURNING id;

-- name: UpdateEmergencyLifecycleVastIDs :exec
-- Called immediately after vast.create_instance succeeds. Records the Vast
-- offer_id, instance_id, and accepted dph price; appends an event to the JSONB
-- timeline. event_json is a single object {ts, from_state, to_state, reason, payload}.
UPDATE ai_gateway.emergency_lifecycles
SET vast_offer_id = $2,
    vast_instance_id = $3,
    accepted_dph = $4,
    events = events || sqlc.arg('event_json')::jsonb
WHERE id = $1;

-- name: MarkEmergencyLifecycleHealthy :exec
-- Called when pod /health first returns healthy. Sets first_health_pass_at
-- (used by D-D4 cost calculation: hours_active = ended_at - first_health_pass_at).
-- Appends an event to the JSONB timeline.
UPDATE ai_gateway.emergency_lifecycles
SET first_health_pass_at = NOW(),
    events = events || sqlc.arg('event_json')::jsonb
WHERE id = $1;

-- name: CloseEmergencyLifecycle :exec
-- Called when lifecycle terminates (any shutdown_reason). Sets ended_at = NOW()
-- which releases the partial unique index slot, allowing a future lifecycle to
-- be inserted. Records final shutdown_reason + total_cost_brl + appends event.
UPDATE ai_gateway.emergency_lifecycles
SET ended_at = NOW(),
    shutdown_reason = $2,
    total_cost_brl = $3,
    events = events || sqlc.arg('event_json')::jsonb
WHERE id = $1;

-- name: ListLiveEmergencyLifecycles :many
-- Used by leader recovery (D-D5) on leader acquisition. The partial unique
-- index `emergency_live_singleton` guarantees ≤1 row is returned. Returns
-- enough state for recovery: vast IDs (to GetInstance) + events (to resume FSM).
SELECT id, vast_offer_id, vast_instance_id, started_at, events
FROM ai_gateway.emergency_lifecycles
WHERE ended_at IS NULL;

-- name: GetMonthlyCostBRL :one
-- Aggregate query for the budget alert (D-D2). Sums total_cost_brl for all
-- closed lifecycles started in the current month (America/Sao_Paulo timezone
-- not enforced here — date_trunc uses session timezone; gateway sets UTC).
-- Only counts ended lifecycles so the alert reflects realised spend.
SELECT COALESCE(SUM(total_cost_brl), 0)::numeric AS month_cost
FROM ai_gateway.emergency_lifecycles
WHERE started_at >= date_trunc('month', NOW())
  AND ended_at IS NOT NULL;

-- name: ListEmergencyLifecycles :many
-- Used by `gatewayctl emerg lifecycles --since N --limit M`. Excludes the
-- events JSONB column (callers fetch via id when needed) so the listing is
-- compact for tabwriter rendering.
SELECT id, started_at, ended_at, trigger_reason, vast_offer_id, vast_instance_id,
       accepted_dph, total_cost_brl, shutdown_reason, leader_replica
FROM ai_gateway.emergency_lifecycles
WHERE started_at >= $1
ORDER BY started_at DESC
LIMIT $2;
