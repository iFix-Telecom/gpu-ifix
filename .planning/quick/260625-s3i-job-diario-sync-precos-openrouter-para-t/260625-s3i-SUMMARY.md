---
phase: quick-260625-s3i
plan: 01
subsystem: ai-gateway / cost-attribution
tags: [pricing, forex, systemd, ops-claude, gatewayctl, fail-safe]
requires: [gatewayctl prices set/set-fx, ssh n8n-ia-vm, jq, curl]
provides:
  - scripts/price-sync/gateway-price-sync.sh
  - scripts/price-sync/gateway-price-sync.service
  - scripts/price-sync/gateway-price-sync.timer
  - scripts/price-sync/README.md
affects: [ai_gateway.prices, ai_gateway.fx_rates, dashboard cost_local_phantom_brl]
tech-stack:
  added: []
  patterns: [systemd user timer mirror of prod-primary-report, fail-safe API-to-gatewayctl sync]
key-files:
  created:
    - scripts/price-sync/gateway-price-sync.sh
    - scripts/price-sync/gateway-price-sync.service
    - scripts/price-sync/gateway-price-sync.timer
    - scripts/price-sync/README.md
  modified: []
decisions:
  - "Live fetch only — no USD hardcoded; every value gated by is_pos_number() before any gatewayctl write"
  - "Forex + pricing are independent guarded blocks; one failing never aborts the other; per-model skip never aborts the loop"
  - "Token via optional EnvironmentFile=-; endpoint works unauthenticated, token never committed"
metrics:
  duration: ~8m
  completed: 2026-06-25
  tasks: 2
  files: 4
---

# Phase quick-260625-s3i Plan 01: OpenRouter price-sync job Summary

Versioned, idempotent, fail-safe daily job that syncs live OpenRouter reference pricing + USD/BRL forex into the ifix-ai-gateway via `gatewayctl`, plus a systemd user timer (Mon-Fri 08:30 BRT) and an ops README — turning the dashboard's `cost_local_phantom_brl` (today R$0) non-zero by seeding the `model.gguf` phantom price row.

## What was built

- **`gateway-price-sync.sh`** (179 lines, executable, `set -euo pipefail`): fetches `https://openrouter.ai/api/v1/models` pricing and `https://open.er-api.com/v6/latest/USD` forex live each run. Independent guarded blocks: forex requires `.result=="success"` + `is_pos_number`; pricing skips all writes on curl failure / empty dump, and `continue`s per-model on any invalid value. Hardcoded phantom provider `openrouter-fireworks`. `MODEL_MAP` seeds `model.gguf → deepseek/deepseek-v4-flash` plus 3 others. `DRY_RUN=1` echoes mutations; `OPENROUTER_TOKEN` optional, read from env (never literal). Gateway reached via `ssh -o ConnectTimeout=15 n8n-ia-vm docker exec ifix-ai-gateway /gatewayctl` (no `sh -c`). End-of-run summary line with fx/updated/skipped counts.
- **`gateway-price-sync.service`**: `Type=oneshot`, `After/Wants=network-online.target`, `EnvironmentFile=-%h/.config/gateway-price-sync.env`, `ExecStart=/home/pedro/bin/gateway-price-sync.sh`, no `[Install]`.
- **`gateway-price-sync.timer`**: `OnCalendar=Mon..Fri 08:30` (bare = BRT, before 09:00 pod up), `Persistent=false`, `WantedBy=timers.target`.
- **`README.md`**: why (exact `{model,provider,unit}` key match; `model.gguf` carries ~83% traffic; cost=R$0 today), optional secret setup (mode 600, not committed), orchestrator deploy steps, DRY_RUN/manual usage, smoke check (`gatewayctl prices list`), fail-safe/idempotency note, how to add a model.

## Verification performed

- `bash -n` — clean.
- `shellcheck -S warning` — clean (no findings).
- Executable bit set (`-rwxrwxr-x`, mode 100755 in git).
- All plan grep-asserts pass for both tasks (provider, forex host, model.gguf, timer schedule, oneshot, ExecStart, EnvironmentFile, README DRY_RUN + deploy line).
- **Live DRY_RUN run** (no real writes): forex fetched USD/BRL=5.195439; OpenRouter returned 339 models; all 4 mapped models resolved with positive prices (`model.gguf` input=0.00000009 output=0.00000018 via `deepseek/deepseek-v4-flash`); `fx_updated=1 models_updated=4 models_skipped=0`; exit 0. Confirms live fetch + parse + guard path works end-to-end without touching the gateway.

## Deviations from Plan

None — plan executed exactly as written. The plan's `<facts>` block carried all load-bearing details (the separate RESEARCH.md was created after the executor's base commit and is not in the worktree; no information was missing).

## Deploy / next steps (orchestrator, NOT executor)

Per plan + README: `install -m 755` the script to `/home/pedro/bin/`, copy both units to `~/.config/systemd/user/`, `daemon-reload`, `enable --now gateway-price-sync.timer`, then smoke-test with a real run + `gatewayctl prices list`. Executor did NOT install the unit or perform a real `gatewayctl set` (deploy is the orchestrator's job).

## Known Stubs

None.

## Self-Check: PASSED

- FOUND: scripts/price-sync/gateway-price-sync.sh (committed 74c67b9)
- FOUND: scripts/price-sync/gateway-price-sync.service (committed efb5bae)
- FOUND: scripts/price-sync/gateway-price-sync.timer (committed efb5bae)
- FOUND: scripts/price-sync/README.md (committed efb5bae)
- FOUND commit: 74c67b9 (Task 1 script)
- FOUND commit: efb5bae (Task 2 units + README)
