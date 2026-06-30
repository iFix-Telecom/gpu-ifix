-- name: GetPodConfig :one
-- Hot-path single-row read at boot and on every pod_config_changed NOTIFY
-- (Phase 17 D-01). The boolean PK CHECK (id = TRUE) guarantees at most one row;
-- returns pgx.ErrNoRows when the table is still empty (pre-seed).
SELECT * FROM ai_gateway.pod_config WHERE id = TRUE;

-- name: SeedPodConfig :exec
-- Idempotent env->DB first-boot seed (Plan 17-03). Inserts the 16 hot fields +
-- 10 numeric bound pairs from the current env-derived config. ON CONFLICT (id)
-- DO NOTHING makes a second boot a no-op so operator edits are NEVER overwritten
-- (T-17-01). The id column defaults to TRUE (single-row guard).
INSERT INTO ai_gateway.pod_config (
    vast_machine_blocklist, vast_machine_allowlist, cap_primary, cap_fallback,
    host_id, reject_private_ip, coldstart_budget_s, port_bind_budget_s,
    failure_cooldown_s, monthly_budget_brl, schedule_up_hour, schedule_down_hour,
    schedule_days, grace_ramp_down_s, provision_lead_s, schedule_disabled,
    cap_primary_min, cap_primary_max, cap_fallback_min, cap_fallback_max,
    coldstart_budget_s_min, coldstart_budget_s_max, port_bind_budget_s_min, port_bind_budget_s_max,
    failure_cooldown_s_min, failure_cooldown_s_max, monthly_budget_brl_min, monthly_budget_brl_max,
    schedule_up_hour_min, schedule_up_hour_max, schedule_down_hour_min, schedule_down_hour_max,
    grace_ramp_down_s_min, grace_ramp_down_s_max, provision_lead_s_min, provision_lead_s_max
) VALUES (
    $1, $2, $3, $4,
    $5, $6, $7, $8,
    $9, $10, $11, $12,
    $13, $14, $15, $16,
    $17, $18, $19, $20,
    $21, $22, $23, $24,
    $25, $26, $27, $28,
    $29, $30, $31, $32,
    $33, $34, $35, $36
)
ON CONFLICT (id) DO NOTHING;

-- ----------------------------------------------------------------------------
-- UpdatePodConfigField — one :exec per editable HOT column (Plan 17-04). One
-- column per query keeps the dashboard audit diff clean (PATTERNS.md line 184).
-- Every UPDATE sets updated_at = now() and targets the single row (id = TRUE).
-- ----------------------------------------------------------------------------

-- name: UpdatePodConfigFieldBlocklist :exec
UPDATE ai_gateway.pod_config SET vast_machine_blocklist = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigFieldAllowlist :exec
UPDATE ai_gateway.pod_config SET vast_machine_allowlist = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigFieldCapPrimary :exec
UPDATE ai_gateway.pod_config SET cap_primary = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigFieldCapFallback :exec
UPDATE ai_gateway.pod_config SET cap_fallback = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigFieldHostID :exec
UPDATE ai_gateway.pod_config SET host_id = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigFieldRejectPrivateIP :exec
UPDATE ai_gateway.pod_config SET reject_private_ip = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigFieldColdstartBudgetS :exec
UPDATE ai_gateway.pod_config SET coldstart_budget_s = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigFieldPortBindBudgetS :exec
UPDATE ai_gateway.pod_config SET port_bind_budget_s = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigFieldFailureCooldownS :exec
UPDATE ai_gateway.pod_config SET failure_cooldown_s = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigFieldMonthlyBudgetBRL :exec
UPDATE ai_gateway.pod_config SET monthly_budget_brl = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigFieldScheduleUpHour :exec
UPDATE ai_gateway.pod_config SET schedule_up_hour = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigFieldScheduleDownHour :exec
UPDATE ai_gateway.pod_config SET schedule_down_hour = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigFieldScheduleDays :exec
UPDATE ai_gateway.pod_config SET schedule_days = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigFieldGraceRampDownS :exec
UPDATE ai_gateway.pod_config SET grace_ramp_down_s = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigFieldProvisionLeadS :exec
UPDATE ai_gateway.pod_config SET provision_lead_s = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigFieldScheduleDisabled :exec
UPDATE ai_gateway.pod_config SET schedule_disabled = $1, updated_at = NOW() WHERE id = TRUE;

-- ----------------------------------------------------------------------------
-- UpdatePodConfigBound — one :exec per owner-editable bound column (D-03). The
-- bounds gate operator-supplied config values; they are themselves editable.
-- ----------------------------------------------------------------------------

-- name: UpdatePodConfigBoundCapPrimaryMin :exec
UPDATE ai_gateway.pod_config SET cap_primary_min = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigBoundCapPrimaryMax :exec
UPDATE ai_gateway.pod_config SET cap_primary_max = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigBoundCapFallbackMin :exec
UPDATE ai_gateway.pod_config SET cap_fallback_min = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigBoundCapFallbackMax :exec
UPDATE ai_gateway.pod_config SET cap_fallback_max = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigBoundColdstartBudgetSMin :exec
UPDATE ai_gateway.pod_config SET coldstart_budget_s_min = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigBoundColdstartBudgetSMax :exec
UPDATE ai_gateway.pod_config SET coldstart_budget_s_max = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigBoundPortBindBudgetSMin :exec
UPDATE ai_gateway.pod_config SET port_bind_budget_s_min = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigBoundPortBindBudgetSMax :exec
UPDATE ai_gateway.pod_config SET port_bind_budget_s_max = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigBoundFailureCooldownSMin :exec
UPDATE ai_gateway.pod_config SET failure_cooldown_s_min = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigBoundFailureCooldownSMax :exec
UPDATE ai_gateway.pod_config SET failure_cooldown_s_max = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigBoundMonthlyBudgetBRLMin :exec
UPDATE ai_gateway.pod_config SET monthly_budget_brl_min = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigBoundMonthlyBudgetBRLMax :exec
UPDATE ai_gateway.pod_config SET monthly_budget_brl_max = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigBoundScheduleUpHourMin :exec
UPDATE ai_gateway.pod_config SET schedule_up_hour_min = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigBoundScheduleUpHourMax :exec
UPDATE ai_gateway.pod_config SET schedule_up_hour_max = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigBoundScheduleDownHourMin :exec
UPDATE ai_gateway.pod_config SET schedule_down_hour_min = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigBoundScheduleDownHourMax :exec
UPDATE ai_gateway.pod_config SET schedule_down_hour_max = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigBoundGraceRampDownSMin :exec
UPDATE ai_gateway.pod_config SET grace_ramp_down_s_min = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigBoundGraceRampDownSMax :exec
UPDATE ai_gateway.pod_config SET grace_ramp_down_s_max = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigBoundProvisionLeadSMin :exec
UPDATE ai_gateway.pod_config SET provision_lead_s_min = $1, updated_at = NOW() WHERE id = TRUE;

-- name: UpdatePodConfigBoundProvisionLeadSMax :exec
UPDATE ai_gateway.pod_config SET provision_lead_s_max = $1, updated_at = NOW() WHERE id = TRUE;
