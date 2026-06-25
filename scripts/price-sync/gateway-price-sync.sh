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
# WHERE: Runs on ops-claude (10.10.10.10), the control plane. It reaches the gateway
#        over SSH: `ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl <args>'`.
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
  ssh -o ConnectTimeout=15 n8n-ia-vm docker exec ifix-ai-gateway /gatewayctl "$@"
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

  local gw_key or_slug prompt completion
  for gw_key in "${!MODEL_MAP[@]}"; do
    or_slug="${MODEL_MAP[$gw_key]}"
    prompt="$(jq -r --arg id "$or_slug" '.data[] | select(.id==$id) | .pricing.prompt // empty' "$tmp" 2>/dev/null || true)"
    completion="$(jq -r --arg id "$or_slug" '.data[] | select(.id==$id) | .pricing.completion // empty' "$tmp" 2>/dev/null || true)"

    if ! is_pos_number "$prompt" || ! is_pos_number "$completion"; then
      warn "pricing: $gw_key ($or_slug) invalid prompt='${prompt:-<none>}' completion='${completion:-<none>}' — skipping this model"
      (( MODELS_SKIPPED++ )) || true
      continue
    fi

    log "pricing: $gw_key ($or_slug) input=$prompt output=$completion → prices set"
    gatewayctl prices set -model "$gw_key" -provider "$PHANTOM_PROVIDER" \
      -unit input_token -usd "$prompt" -notes "phantom=$or_slug, $NOTES_TAG"
    gatewayctl prices set -model "$gw_key" -provider "$PHANTOM_PROVIDER" \
      -unit output_token -usd "$completion" -notes "phantom=$or_slug, $NOTES_TAG"
    (( MODELS_UPDATED++ )) || true
  done
}

# --- main --------------------------------------------------------------------
main() {
  log "=== gateway-price-sync start (DRY_RUN=$DRY_RUN, token=$([[ -n "$OPENROUTER_TOKEN" ]] && echo set || echo unset)) ==="
  sync_forex
  sync_pricing
  log "=== gateway-price-sync done: fx_updated=$FX_UPDATED models_updated=$MODELS_UPDATED models_skipped=$MODELS_SKIPPED ==="
}

main "$@"
