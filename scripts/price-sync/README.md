# gateway-price-sync

Daily job that syncs **OpenRouter reference pricing** + the **live USD/BRL forex
rate** into the `ifix-ai-gateway` pricing tables (`ai_gateway.prices` /
`ai_gateway.fx_rates`) via `gatewayctl`. Runs on **ops-claude** (the control plane);
reaches the gateway over `ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl ...'`.

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
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl prices list'   # should show model.gguf rows
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

## Adding a new model

Edit the `MODEL_MAP` associative array in `gateway-price-sync.sh`: map the
gateway-stored model key (often date-suffixed, e.g. `...-20260423`) to its undated
OpenRouter base slug (the `.id` in `/api/v1/models`).
