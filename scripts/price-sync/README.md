# gateway-price-sync

Daily job that syncs **OpenRouter reference pricing** + the **live USD/BRL forex
rate** into the `ifix-ai-gateway` pricing tables (`ai_gateway.prices` /
`ai_gateway.fx_rates`) via `gatewayctl`. Runs on **ops-claude** (the control plane);
reaches the consolidated gateway on worker-vm over
`ssh worker-vm 'docker exec $(docker ps -q -f name=ai-gateway-prod_gateway|head -1) /gatewayctl ...'`
(swarm task-container name is dynamic; was `n8n-ia-vm`/`ifix-ai-gateway` before Phase 19-06).

## Why this exists

The gateway's cost-attribution lookup is an **exact `{model, provider, unit}`
map-key match**. The local pod reports the model verbatim as `model.gguf` (the
dominant ~83% of weekly chat traffic), but the only seeded price row is keyed
`qwen3.5-27b` — which matches none of the billed model strings. Result: the
dashboard's `cost_local_phantom_brl` reads **R$0** today.

This job writes a *phantom reference price* keyed
`(model=model.gguf, provider=openrouter-fireworks, unit=input_token|output_token)`
— `openrouter-fireworks` is hardcoded in the gateway as the provider for **every**
local row, regardless of the real upstream. That turns GPU-saved-cost reporting on.

Pricing is fetched live (OpenRouter slugs move) and forex is fetched live (the seed
is stale at 5.10). A failed/garbled fetch never clobbers a good existing row.

## Secret setup (token is optional)

The OpenRouter `/models` endpoint serves unauthenticated, so the token is optional;
set it only to avoid any future rate-limit. On ops-claude:

```bash
install -m 600 /dev/stdin ~/.config/gateway-price-sync.env <<'EOF'
OPENROUTER_TOKEN=<value of stack 34 env UPSTREAM_LLM_OPENROUTER_AUTH_BEARER>
EOF
```

The token value lives in Portainer stack 34 env `UPSTREAM_LLM_OPENROUTER_AUTH_BEARER`
(see memory `openrouter-token-and-stack-location`). It is **never** committed and
**never** embedded in the script — the `.service` loads it via `EnvironmentFile=-`.

## Deploy (run by the orchestrator on ops-claude — NOT part of the plan)

```bash
install -m 755 scripts/price-sync/gateway-price-sync.sh /home/pedro/bin/gateway-price-sync.sh
cp scripts/price-sync/gateway-price-sync.service ~/.config/systemd/user/
cp scripts/price-sync/gateway-price-sync.timer   ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now gateway-price-sync.timer
systemctl --user list-timers gateway-price-sync.timer   # confirm next Mon-Fri 08:30 BRT
```

## Manual run / preview

```bash
DRY_RUN=1 /home/pedro/bin/gateway-price-sync.sh   # preview: logs the set-fx + set lines, writes nothing
/home/pedro/bin/gateway-price-sync.sh             # real run
tail ~/gateway-price-sync.log                     # durable log
```

A `DRY_RUN=1` preview prints the expected `prices set-fx -usd-brl <live>` line plus,
for each mapped model, the `model.gguf` input/output `prices set` lines.

## Smoke / verify cost populated

```bash
ssh worker-vm 'docker exec $(docker ps -q -f name=ai-gateway-prod_gateway|head -1) /gatewayctl prices list'   # should show model.gguf rows
```

After a real run, `gatewayctl prices list` shows live `model.gguf` input/output rows.
The dashboard `cost_local_phantom_brl` becomes non-zero on subsequent billed traffic
(observe via `/admin/usage`).

## Fail-safe / idempotency

- A failed OpenRouter or forex fetch (curl error, `.result != success`, empty dump,
  or any value failing the strict positive-number guard) **skips that write** and
  leaves the existing gateway row intact — no garbage overwrite.
- `gatewayctl prices set` / `set-fx` auto-expire the prior active row, so re-running
  the job (e.g. the next day) is idempotent.

## Auto-discovery (261007-gyq)

Besides `MODEL_MAP`, every run discovers the chat models routed through OpenRouter
(`upstream='openrouter-chat'`) in the last 7 days from `ai_gateway.billing_events`
and prices each one whose name is an **exact** OpenRouter `/models` `.id`
(provider `openrouter-fireworks`). A discovered model missing from OpenRouter is a
WARN + `models_unmatched` counter (never fails the run); keys already in
`MODEL_MAP` are skipped. The query runs on worker-vm in a throwaway
`postgres:16-alpine` container; the DSN is read from the gateway container env
(`AI_GATEWAY_PG_DSN`) and passed only via container env (never argv/disk/log).
A failed query is a WARN; the `MODEL_MAP` sync still runs. The final log line
includes `models_discovered=N models_unmatched=M`.

## Adding a new model

Only needed for keys that are NOT an OpenRouter `.id` (e.g. `model.gguf`, dated
upstream keys); exact-`.id` models are picked up by auto-discovery.

Edit the `MODEL_MAP` associative array in `gateway-price-sync.sh`: map the
gateway-stored model key (often date-suffixed, e.g. `...-20260423`) to its undated
OpenRouter base slug (the `.id` in `/api/v1/models`).
