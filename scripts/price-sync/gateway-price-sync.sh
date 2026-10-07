#!/usr/bin/env bash
# scripts/price-sync/gateway-price-sync.sh — daily OpenRouter price + USD/BRL fx sync.
#
# WHAT:  Fetches OpenRouter reference pricing (per model) and the live USD/BRL
#        forex rate, then writes them into the ifix-ai-gateway pricing tables
#        (ai_gateway.prices / ai_gateway.fx_rates) via `gatewayctl`.
#
# WHY:   The gateway's cost-attribution lookup is an EXACT {model, provider, unit}
#        map-key match. The local pod reports `model.gguf` verbatim (the dominant
#        ~83% of weekly chat traffic), but the only seeded price row is keyed
#        `qwen3.5-27b` — so today `cost_local_phantom_brl` reports R$0. Writing a
#        phantom reference price keyed (model=model.gguf, provider=openrouter-fireworks,
#        unit=input_token|output_token) turns GPU-saved-cost reporting back on.
#
# DISCOVERY (261007-gyq): besides the fixed MODEL_MAP, the script auto-discovers
#        the chat models actually routed through OpenRouter (upstream=openrouter-chat)
#        in the last 7 days, read from ai_gateway.billing_events, and prices every one
#        whose name matches an OpenRouter `/models` `.id` EXACTLY (provider
#        openrouter-fireworks = providerForUpstream("openrouter-chat")). A discovered
#        model absent from OpenRouter is WARNed and counted in `models_unmatched`; it
#        never fails the run. Keys already covered by MODEL_MAP are not duplicated.
#        Motivation: OpenRouter default moved to deepseek/deepseek-v4.1-flash on
#        2026-09-29, was never added to MODEL_MAP, and ~35k calls were billed at 0.
#        DB access: on worker-vm, the DSN is read from the gateway container env
#        (AI_GATEWAY_PG_DSN via `docker inspect`) and handed to a throwaway
#        postgres:16-alpine psql ONLY via container env — never argv, disk or log.
#        A failed discovery query is a WARN (MODEL_MAP sync still runs).
#
# WHERE: Runs on ops-claude (10.10.10.10), the control plane. It reaches the
#        CONSOLIDATED gateway on worker-vm (10.10.10.50) over SSH. Since Phase 19-06
#        (gateway consolidation), the gateway is a Portainer Swarm service
#        `ai-gateway-prod_gateway` whose task-container name is dynamic, so the
#        container is resolved at call time via `docker ps -q -f name=...`:
#          `ssh worker-vm 'docker exec $(docker ps -q -f name=ai-gateway-prod_gateway|head -1) /gatewayctl <args>'`.
#        (Was `ssh n8n-ia-vm 'docker exec ifix-ai-gateway ...'` before 19-06 decommission.)
#        The container has NO shell — the binary is called directly (no `sh -c`).
#
# LIVE:  Pricing AND forex are fetched live every run. NO USD value is hardcoded.
#        OpenRouter model slugs move and the seeded fx (5.10) is stale, so both are
#        pulled fresh and every value is gated by a strict positive-number guard.
#
# FAIL-SAFE: A failed or garbled OpenRouter/forex fetch never overwrites a good
#        existing row — the affected write is skipped (warn + continue), the prior
#        active row survives. `gatewayctl prices set/set-fx` auto-expire the prior
#        active row, so re-running is idempotent.
#
# DRY_RUN: set `DRY_RUN=1` to log every gatewayctl mutation instead of executing it
#        (preview without writing). OPENROUTER_TOKEN is optional (endpoint serves
#        unauthenticated); when set it is sent as a Bearer header. The token is NEVER
#        embedded here — it comes from the EnvironmentFile / process env.
#
# Exit codes:
#   0 — normal run (partial sync acceptable: individual models may be skipped)
#   non-zero — only on an unexpected failure caught by `set -e`
set -euo pipefail

LOG_FILE="${LOG_FILE:-$HOME/gateway-price-sync.log}"
DRY_RUN="${DRY_RUN:-0}"
OPENROUTER_TOKEN="${OPENROUTER_TOKEN:-}"

OR_MODELS_URL="https://openrouter.ai/api/v1/models"
FOREX_URL="https://open.er-api.com/v6/latest/USD"
PHANTOM_PROVIDER="openrouter-fireworks"
NOTES_TAG="synced 260625-s3i"

# Counters for the end-of-run summary.
FX_UPDATED=0
MODELS_UPDATED=0
MODELS_SKIPPED=0
MODELS_DISCOVERED=0
MODELS_UNMATCHED=0

# --- logging helpers ---------------------------------------------------------
# Append all output to the log AND echo to the terminal (oneshot service captures
# stdout to the journal; the log file is the durable record).
exec > >(tee -a "$LOG_FILE") 2>&1

log()  { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
warn() { printf '%s WARN %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >&2; }

# --- numeric guard -----------------------------------------------------------
# Returns success ONLY for a strictly-positive decimal. Rejects empty, "null",
# "0", "0.0", scientific-but-empty, and any non-numeric string. This is the gate
# that prevents a failed/mis-parsed API response from writing garbage as a price.
is_pos_number() {
  local v="${1:-}"
  [[ -n "$v" && "$v" != "null" ]] || return 1
  awk -v x="$v" 'BEGIN {
    if (x ~ /^[0-9]*\.?[0-9]+([eE][-+]?[0-9]+)?$/ && (x + 0) > 0) exit 0;
    exit 1;
  }'
}

# --- gateway wrapper ---------------------------------------------------------
# Run a gatewayctl subcommand on the gateway container, or echo it under DRY_RUN.
# The remote side is NOT wrapped in `sh -c` — the container has no shell.
gatewayctl() {
  if [[ "$DRY_RUN" == "1" ]]; then
    log "DRY_RUN gatewayctl $*"
    return 0
  fi
  # 19-06: consolidated gateway on worker-vm; swarm task-container name is dynamic.
  ssh -o ConnectTimeout=15 worker-vm \
    "docker exec \$(docker ps -q -f name=ai-gateway-prod_gateway | head -1) /gatewayctl" "$@"
}

# --- model map ---------------------------------------------------------------
# gateway-stored model key  →  OpenRouter base slug (the undated .id in /models).
# The gateway keys are date-suffixed (e.g. ...-20260423); OpenRouter exposes the
# undated slug, so we map the gateway key to its undated OpenRouter equivalent.
# `model.gguf` is the verbatim local-pod key carrying ~83% of weekly chat traffic.
# Operators EXTEND this map as new upstream models appear.
declare -A MODEL_MAP=(
  ["model.gguf"]="deepseek/deepseek-v4-flash"
  ["deepseek/deepseek-v4-flash-20260423"]="deepseek/deepseek-v4-flash"
  ["qwen/qwen3.5-27b-20260224"]="qwen/qwen3.5-27b"
  ["meta-llama/llama-3.1-8b-instruct"]="meta-llama/llama-3.1-8b-instruct"
)

# --- discovery: OpenRouter chat models seen in billing ------------------------
# Prints one model name per line (DISTINCT model, upstream=openrouter-chat, last
# 7 days). Read-only, so it also runs under DRY_RUN (nothing is mutated).
# The remote script reads the DSN from the gateway container env and passes it to
# psql only through container env vars (D, Q) — the DSN never reaches argv/disk/log.
DISCOVERY_SQL="SELECT DISTINCT model FROM ai_gateway.billing_events WHERE upstream='openrouter-chat' AND ts > now() - interval '7 days' AND model <> '' ORDER BY 1"

discover_models() {
  # The heredoc is quoted: every $-expansion below happens on the REMOTE shell.
  # The SQL is %q-quoted so the remote shell receives it as a single word.
  ssh -o ConnectTimeout=15 worker-vm "env Q=$(printf '%q' "$DISCOVERY_SQL") bash -s" <<'REMOTE'
set -euo pipefail
C="$(docker ps -q -f name=ai-gateway-prod_gateway | head -1)"
[ -n "$C" ] || { echo "gateway container not found" >&2; exit 2; }
D="$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$C" | sed -n 's/^AI_GATEWAY_PG_DSN=//p' | head -1)"
[ -n "$D" ] || { echo "AI_GATEWAY_PG_DSN not found in gateway env" >&2; exit 3; }
export D Q
exec docker run --rm -e D -e Q postgres:16-alpine sh -c 'exec psql "$D" -v ON_ERROR_STOP=1 -Atc "$Q"'
REMOTE
}

# --- forex sync (independent, guarded) ---------------------------------------
sync_forex() {
  log "forex: fetching $FOREX_URL"
  local body
  if ! body="$(curl -sS -m 15 "$FOREX_URL")"; then
    warn "forex: curl failed — skipping fx (prior fx row survives)"
    return 0
  fi

  local result brl
  result="$(printf '%s' "$body" | jq -r '.result // empty' 2>/dev/null || true)"
  brl="$(printf '%s' "$body" | jq -r '.rates.BRL // empty' 2>/dev/null || true)"

  if [[ "$result" != "success" ]]; then
    warn "forex: .result != success (got '${result:-<none>}') — skipping fx"
    return 0
  fi
  if ! is_pos_number "$brl"; then
    warn "forex: invalid USD/BRL '${brl:-<none>}' — skipping fx"
    return 0
  fi

  log "forex: USD/BRL=$brl → prices set-fx"
  gatewayctl prices set-fx -usd-brl "$brl"
  FX_UPDATED=1
}

# --- pricing sync (independent, guarded) -------------------------------------
sync_pricing() {
  log "pricing: fetching $OR_MODELS_URL"
  local tmp
  tmp="$(mktemp "${TMPDIR:-/tmp}/or-models.XXXXXX.json")"
  # shellcheck disable=SC2064
  trap "rm -f '$tmp'" RETURN

  local -a curl_args=(-sS -m 30 -o "$tmp")
  if [[ -n "$OPENROUTER_TOKEN" ]]; then
    curl_args+=(-H "Authorization: Bearer $OPENROUTER_TOKEN")
  fi

  if ! curl "${curl_args[@]}" "$OR_MODELS_URL"; then
    warn "pricing: curl failed — skipping all pricing writes (existing rows survive)"
    return 0
  fi

  local count
  count="$(jq -r '.data | length' "$tmp" 2>/dev/null || echo 0)"
  if ! [[ "$count" =~ ^[0-9]+$ ]] || (( count == 0 )); then
    warn "pricing: empty/invalid model dump (count=${count:-?}) — skipping all pricing writes"
    return 0
  fi
  log "pricing: fetched $count OpenRouter models"

  local gw_key
  for gw_key in "${!MODEL_MAP[@]}"; do
    price_model "$tmp" "$gw_key" "${MODEL_MAP[$gw_key]}" || true
  done

  # Auto-discovery (261007-gyq): price every OpenRouter-routed model seen in
  # billing whose name is an exact OpenRouter .id. Never fails the run.
  local discovered
  if ! discovered="$(discover_models)"; then
    warn "discovery: billing query failed — skipping auto-discovery (MODEL_MAP sync done)"
    return 0
  fi
  local model
  while IFS= read -r model; do
    [[ -n "$model" ]] || continue
    if [[ -n "${MODEL_MAP[$model]+x}" ]]; then
      log "discovery: $model already covered by MODEL_MAP — skip"
      continue
    fi
    if ! jq -e --arg id "$model" 'any(.data[]; .id==$id)' "$tmp" >/dev/null 2>&1; then
      warn "discovery: $model not found in OpenRouter /models (exact .id) — unmatched, not priced"
      (( MODELS_UNMATCHED++ )) || true
      continue
    fi
    (( MODELS_DISCOVERED++ )) || true
    log "discovery: $model discovered (openrouter-chat, last 7d)"
    price_model "$tmp" "$model" "$model" || true
  done <<< "$discovered"
}

# price_model <models-json> <gateway-model-key> <openrouter-slug>
# Writes the input_token/output_token phantom rows for one model, guarded by
# is_pos_number. Returns 1 (and counts a skip) when the OpenRouter price is invalid.
price_model() {
  local tmp="$1" gw_key="$2" or_slug="$3" prompt completion
  prompt="$(jq -r --arg id "$or_slug" 'first(.data[] | select(.id==$id) | .pricing.prompt // empty)' "$tmp" 2>/dev/null || true)"
  completion="$(jq -r --arg id "$or_slug" 'first(.data[] | select(.id==$id) | .pricing.completion // empty)' "$tmp" 2>/dev/null || true)"

  if ! is_pos_number "$prompt" || ! is_pos_number "$completion"; then
    warn "pricing: $gw_key ($or_slug) invalid prompt='${prompt:-<none>}' completion='${completion:-<none>}' — skipping this model"
    (( MODELS_SKIPPED++ )) || true
    return 1
  fi

  log "pricing: $gw_key ($or_slug) input=$prompt output=$completion → prices set"
  gatewayctl prices set -model "$gw_key" -provider "$PHANTOM_PROVIDER" \
    -unit input_token -usd "$prompt" -notes "phantom=$or_slug, $NOTES_TAG"
  gatewayctl prices set -model "$gw_key" -provider "$PHANTOM_PROVIDER" \
    -unit output_token -usd "$completion" -notes "phantom=$or_slug, $NOTES_TAG"
  (( MODELS_UPDATED++ )) || true
}

# --- main --------------------------------------------------------------------
main() {
  log "=== gateway-price-sync start (DRY_RUN=$DRY_RUN, token=$([[ -n "$OPENROUTER_TOKEN" ]] && echo set || echo unset)) ==="
  sync_forex
  sync_pricing
  log "=== gateway-price-sync done: fx_updated=$FX_UPDATED models_updated=$MODELS_UPDATED models_skipped=$MODELS_SKIPPED models_discovered=$MODELS_DISCOVERED models_unmatched=$MODELS_UNMATCHED ==="
}

main "$@"
