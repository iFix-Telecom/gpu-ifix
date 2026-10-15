-- name: StartAuxPodLifecycle :one
-- Phase 23 (23-11, HAIKU-09): registra o início de um pod Vast auxiliar
-- (hoje a 3060 unificada, publicado pelo unified3060.py via
-- POST /admin/aux-pod-lifecycles). Idempotente por (pod, vast_instance_id):
-- start repetido devolve o MESMO id, mantém o started_at mais antigo informado
-- e só sobrescreve dph_usd quando o retry traz um valor.
INSERT INTO ai_gateway.aux_pod_lifecycles (pod, vast_instance_id, started_at, dph_usd)
VALUES (sqlc.arg('pod'), sqlc.arg('vast_instance_id'), sqlc.arg('started_at'), sqlc.narg('dph_usd'))
ON CONFLICT (pod, vast_instance_id) DO UPDATE
SET started_at = LEAST(ai_gateway.aux_pod_lifecycles.started_at, EXCLUDED.started_at),
    dph_usd    = COALESCE(EXCLUDED.dph_usd, ai_gateway.aux_pod_lifecycles.dph_usd)
RETURNING id;

-- name: GetAuxPodLifecycle :one
-- Distingue "já fechado" (ended_at NOT NULL → 200 already_closed) de "nunca
-- aberto" (pgx.ErrNoRows → 404) no stop, e fornece started_at + dph_usd para
-- o cálculo do custo quando o stop não traz total_cost_usd.
SELECT id, started_at, ended_at, dph_usd
FROM ai_gateway.aux_pod_lifecycles
WHERE pod = sqlc.arg('pod') AND vast_instance_id = sqlc.arg('vast_instance_id');

-- name: CloseAuxPodLifecycle :execrows
-- Fecha o lifecycle aberto. Guardado por `ended_at IS NULL`: um stop repetido
-- (retry do publisher best-effort) afeta 0 linhas e NÃO sobrescreve o custo.
UPDATE ai_gateway.aux_pod_lifecycles
SET ended_at       = sqlc.arg('ended_at'),
    total_cost_usd = sqlc.arg('total_cost_usd'),
    total_cost_brl = sqlc.arg('total_cost_brl')
WHERE pod = sqlc.arg('pod')
  AND vast_instance_id = sqlc.arg('vast_instance_id')
  AND ended_at IS NULL;

-- name: ListAuxPodLifecyclesInRange :many
-- Leitura para GET /admin/economy (custo da 3060). Mesmo formato de
-- ListPrimaryLifecyclesInRange: filtra pelo started_at dentro de [from, to)
-- (um lifecycle = um dia BRT — a 3060 sobe e é destruída no mesmo dia).
SELECT id, pod, vast_instance_id, started_at, ended_at, dph_usd,
       total_cost_usd, total_cost_brl
FROM ai_gateway.aux_pod_lifecycles
WHERE started_at >= sqlc.arg('from_ts')
  AND started_at <  sqlc.arg('to_ts')
ORDER BY started_at DESC;
