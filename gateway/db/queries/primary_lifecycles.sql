-- name: InsertPrimaryLifecycle :one
-- Creates a new primary lifecycle row with started_at = NOW(). Returns the
-- BIGSERIAL id + started_at so the caller can attach Vast IDs later via
-- UpdatePrimaryLifecycleVastIDs and compute durations.
-- The partial unique index `primary_live_singleton` guarantees that only one
-- row may exist with ended_at IS NULL at any time.
INSERT INTO ai_gateway.primary_lifecycles (trigger_reason, leader_replica)
VALUES ($1, $2)
RETURNING id, started_at;

-- name: UpdatePrimaryLifecycleVastIDs :exec
-- Called immediately after vast.create_instance succeeds. Records the Vast
-- offer_id, instance_id, and accepted dph price; appends an event to the JSONB
-- timeline. event_json is a single object {ts, from_state, to_state, reason, payload}.
UPDATE ai_gateway.primary_lifecycles
SET vast_offer_id = $2,
    vast_instance_id = $3,
    accepted_dph = $4,
    events = events || sqlc.arg('event_json')::jsonb
WHERE id = $1;

-- name: MarkPrimaryLifecycleHealthy :exec
-- Called when pod /health first returns healthy. Sets first_health_pass_at
-- (used by cost calculation: hours_active = ended_at - first_health_pass_at).
-- Appends an event to the JSONB timeline.
UPDATE ai_gateway.primary_lifecycles
SET first_health_pass_at = NOW(),
    events = events || sqlc.arg('event_json')::jsonb
WHERE id = $1;

-- name: MarkPrimaryLifecycleDraining :exec
-- NEW vs emerg (Phase 6.6): records when reconciler entered drain ramp-down
-- window (PRIMARY_POD_SCHEDULE_GRACE_RAMP_DOWN_SECONDS gate). The row stays
-- open (ended_at IS NULL) until grace expires + destroy succeeds → then
-- ClosePrimaryLifecycle finalises.
UPDATE ai_gateway.primary_lifecycles
SET drain_started_at = NOW(),
    events = events || sqlc.arg('event_json')::jsonb
WHERE id = $1;

-- name: ClosePrimaryLifecycle :exec
-- Called when lifecycle terminates (any shutdown_reason). Sets ended_at = NOW()
-- which releases the partial unique index slot, allowing a future lifecycle to
-- be inserted. Records final shutdown_reason + total_cost_brl + appends event.
-- Guarded by `ended_at IS NULL` to make this idempotent for retry storms.
UPDATE ai_gateway.primary_lifecycles
SET ended_at = NOW(),
    shutdown_reason = $2,
    total_cost_brl = $3,
    events = events || sqlc.arg('event_json')::jsonb
WHERE id = $1 AND ended_at IS NULL;

-- name: GetOpenPrimaryLifecycle :one
-- NEW per reviews consensus action #4 — restart recovery for
-- primary.Reconciler.recoverOpenLifecycle (Plan 06.6-06a).
-- Returns the single open lifecycle row (ended_at IS NULL) or pgx.ErrNoRows.
-- Bounded LIMIT 1 because primary_live_singleton unique index guarantees at most 1.
SELECT id, started_at, first_health_pass_at, drain_started_at, ended_at,
       trigger_reason, vast_offer_id, vast_instance_id, accepted_dph,
       total_cost_brl, shutdown_reason, events, leader_replica
FROM ai_gateway.primary_lifecycles
WHERE ended_at IS NULL
LIMIT 1;

-- name: ListPrimaryLifecycles :many
-- Used by `gatewayctl primary lifecycles --since N --limit M` (Plan 06.6-09).
-- Excludes the events JSONB column (callers fetch via id when needed) so the
-- listing is compact for tabwriter rendering. Mirrors ListEmergencyLifecycles
-- shape from emergency_lifecycles.sql (parity per 06.6-PATTERNS.md).
SELECT id, started_at, drain_started_at, ended_at, trigger_reason,
       vast_offer_id, vast_instance_id, accepted_dph, total_cost_brl,
       shutdown_reason, leader_replica
FROM ai_gateway.primary_lifecycles
WHERE started_at >= $1
ORDER BY started_at DESC
LIMIT $2;

-- name: ListPrimaryLifecyclesInRange :many
-- Range-overlap variant of ListPrimaryLifecycles for GET /admin/economy
-- (OBS-09). Same compact column list (no events JSONB). Filters by the
-- lifecycle's started_at into the [from, to) window -- one lifecycle = one BRT
-- day (CONTEXT A1: pod runs 9-17h BRT same-day windows). No LIMIT -- the
-- handler reduces over every lifecycle in the period for the Vast accrual.
SELECT id, started_at, drain_started_at, ended_at, trigger_reason,
       vast_offer_id, vast_instance_id, accepted_dph, total_cost_brl,
       shutdown_reason, leader_replica
FROM ai_gateway.primary_lifecycles
WHERE started_at >= $1
  AND started_at <  $2
ORDER BY started_at DESC;
