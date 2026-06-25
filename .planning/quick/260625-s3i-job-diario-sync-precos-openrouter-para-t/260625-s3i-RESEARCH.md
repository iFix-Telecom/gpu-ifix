# Quick Task 260625-s3i — Sync OpenRouter prices → gateway `ai_gateway.prices`

**Researched:** 2026-06-25 · **Confidence:** HIGH (all 4 points verified live against PROD DB + gateway CLI + OpenRouter API)

## TL;DR — the premise was wrong, and that changes the fix

The task assumed `usage.Model()` writes the alias `qwen`. **It does not.** It writes whatever the
upstream returns in the JSON `usage.model` field. Live PROD `billing_events` (last 7 days):

| model (stored) | upstream | route | reqs/7d |
|---|---|---|---|
| **`model.gguf`** | `emergency_pod_llm` | chat | **3900** ← dominant local traffic |
| `deepseek/deepseek-v4-flash-20260423` | `openrouter-chat` | chat | 731 |
| `meta-llama/llama-3.1-8b-instruct` | `openrouter-chat` | chat | 67 |
| `qwen/qwen3.5-27b-20260224` | `openrouter-chat` | chat | 6 |
| `openai/gpt-oss-120b`, `deepseek/deepseek-chat-v3.1`, `deepseek/deepseek-v3.2-20251201` | `openrouter-chat` | chat | 1 each |

No `embed`/`stt` rows in the last 30 days — **only `chat` needs pricing.** The seed row `qwen3.5-27b`
matches **zero** of these strings → that is why every `cost_local_phantom_brl` = 0.

**The highest-value fix: price `model.gguf`** (the local pod's reported model name, 3900/4700 weekly
chat requests) against the phantom provider. The date-suffixed OpenRouter slugs are a moving target —
the job should enumerate them dynamically (see §3).

---

## 1. Exact name `usage.Model()` writes — `model.gguf` for the local pod

**Mechanism:** `usage.SetModel(f.Model)` where `f.Model` is the top-level `model` field of the upstream
response/usage frame — not the alias, not the resolved target.
- `gateway/internal/proxy/interceptor_usage.go:167-169` (JSON path), `:449-451` (SSE path), `:527` (json buffer), shape at `:351-358`.
- Stored verbatim into `billing_events.model` at `:245,268`.

**Lookup is an EXACT map-key match** on `{Model, Provider, Unit}` — no normalization, no suffix stripping:
- `gateway/internal/billing/prices_loader.go:87` → `s.byKey[PriceKey{Model, Provider, Unit}]`
- `gateway/internal/billing/cost.go:34` → `prices.Get(model, provider, unit)`; miss → returns 0 + WARN + `gateway_prices_missing_total`.

**Phantom provider is hardcoded** `openrouter-fireworks` regardless of real upstream
(`interceptor_usage.go:259`, comment D-B4: *"reference pricing… so reports can answer how much did the GPU save us"*).

➡️ **To make local cost non-zero, insert rows keyed `(model='model.gguf', provider='openrouter-fireworks', unit=input_token|output_token)`.** `emergency_pod_llm` does not start with `local-`, so `isLocal=false` and an *external* cost is also attempted with provider `emergency_pod_llm` (`:253-257`) — no such price row exists → external stays 0 (correct; GPU is fixed-cost). Only the phantom row matters.

> Evidence (live): `SELECT model,upstream,count(*) FROM ai_gateway.billing_events WHERE ts>now()-interval '7 days' GROUP BY 1,2` → row `model.gguf | emergency_pod_llm | 3900`, max(ts)=2026-06-25.

---

## 2. Exact `gatewayctl prices` syntax (verified live)

```bash
# Set a unit price (auto-expires the prior active row for the same key):
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl prices set \
  -model <MODEL> -provider <PROVIDER> \
  -unit <input_token|output_token|audio_second|embed_request> \
  -usd <FLOAT >0> [-notes "<free text>"]'

# Set the USD→BRL fx rate (auto-expires prior active fx row):
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl prices set-fx -usd-brl <FLOAT >0>'

# Inspect:
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl prices list'
```

`prices set` flags (from `gatewayctl prices set` with no args): `-model` (req), `-provider` (req),
`-unit` (req), `-usd` (req, >0), `-notes` (opt). `prices set-fx`: `-usd-brl` (req, >0). Top-level usage:
`gatewayctl prices <set|list|set-fx> [flags]`. Container has no `sh`/shell — call the binary directly via `docker exec`.

Current `prices list` (the stale seed — all `valid_to=active`):
```
qwen3.5-27b            openrouter-fireworks input_token   0.00000020   Phase 4 seed
qwen3.5-27b            openrouter-fireworks output_token  0.00000156   Phase 4 seed
text-embedding-3-small openai               embed_request 0.00000100
text-embedding-3-small openai               input_token   0.00000002
whisper-1              openai               audio_second  0.00010000
```

---

## 3. alias → OpenRouter model + pricing (verified live)

OpenRouter `GET https://openrouter.ai/api/v1/models` returns `.data[].pricing.prompt` /
`.pricing.completion` as **USD per token** (string). Works with the stack token (also works unauthenticated):

```bash
TOKEN=<REDACTED-OPENROUTER-TOKEN>   # stack 34 env UPSTREAM_LLM_OPENROUTER_AUTH_BEARER
curl -s https://openrouter.ai/api/v1/models -H "Authorization: Bearer $TOKEN" \
 | jq -r '.data[] | select(.id=="deepseek/deepseek-v4-flash") | "\(.pricing.prompt) \(.pricing.completion)"'
```

Live prices (USD/token) returned today:

| OpenRouter slug | prompt (input) | completion (output) |
|---|---|---|
| **`deepseek/deepseek-v4-flash`** | **0.00000009** | **0.00000018** |
| `qwen/qwen3.5-27b` | 0.000000195 | 0.00000156 |
| `meta-llama/llama-3.1-8b-instruct` | 0.00000002 | 0.00000003 |
| `deepseek/deepseek-chat-v3.1` | 0.00000021 | 0.00000079 |

**Alias → phantom reference model:** `model-alias list` shows the `qwen` alias' OpenRouter target is
**`deepseek/deepseek-v4-flash:nitro`** (`openrouter-chat` upstream). So the phantom reference for the
local pod (`model.gguf`) = `deepseek/deepseek-v4-flash` pricing. Minimum viable fix = two `set` calls:

```bash
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl prices set -model model.gguf -provider openrouter-fireworks -unit input_token  -usd 0.00000009 -notes "phantom=deepseek-v4-flash, synced 260625-s3i"'
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl prices set -model model.gguf -provider openrouter-fireworks -unit output_token -usd 0.00000018 -notes "phantom=deepseek-v4-flash, synced 260625-s3i"'
```

**Moving-target note (OpenRouter-served rows):** the `openrouter-chat` upstream stores **date-suffixed**
slugs (`deepseek/deepseek-v4-flash-20260423`, `qwen/qwen3.5-27b-20260224`). These won't match the
undated OpenRouter `.id`. Recommended job design: **enumerate distinct `model` from recent
`billing_events`**, for each strip a trailing `-YYYYMMDD`/`-NNNN` suffix to derive the base OpenRouter
slug, look up its pricing from the `/models` dump, and upsert `(model, openrouter-fireworks, input_token/output_token)`. Always also upsert the special-case `model.gguf` → `deepseek/deepseek-v4-flash`. (`gatewayctl prices set` is idempotent — it expires the prior active row, so re-running daily is safe.)

`model_aliases` for completeness (`gatewayctl model-alias list`): `qwen→deepseek/deepseek-v4-flash:nitro` (openrouter-chat) / `qwen` (local-llm); `bge-m3`, `whisper` aliases exist but have no recent billed traffic.

---

## 4. USD→BRL forex source — use `open.er-api.com` (free, no key)

Current `ai_gateway.fx_rates`: column is **`currency_pair`** (not `pair`); single stale seed row
`USD/BRL = 5.100000` from 2026-05-26, never updated. The gateway reads `fx.Get("USD/BRL")`
(`cost.go:48`) and falls back to in-code `defaultUSDBRL` only if absent.

Live test of the three candidates:

| API | Result | Verdict |
|---|---|---|
| `economia.awesomeapi.com.br/last/USD-BRL` | **HTTP 429 QuotaExceeded** | ✗ unreliable free tier |
| `api.exchangerate.host/latest` | `missing_access_key` (now paywalled) | ✗ requires key |
| **`open.er-api.com/v6/latest/USD`** | `result=success`, `rates.BRL=5.195439`, updated daily | ✓ **use this** |

```bash
RATE=$(curl -sS -m 15 https://open.er-api.com/v6/latest/USD | jq -r '.rates.BRL')   # → 5.195439
ssh n8n-ia-vm "docker exec ifix-ai-gateway /gatewayctl prices set-fx -usd-brl $RATE"
```

Guard in the job: only call `set-fx` if `RATE` is a positive number and `.result=="success"`, else keep the prior rate (don't overwrite a good value with a parse error).

---

## 5. Timer pattern — mirror `prod-primary-report.*` (systemd user timer)

ops-claude `Time zone: America/Sao_Paulo (-03)` (`timedatectl`). The existing report timer uses a bare
`OnCalendar=Mon..Fri 09:30` and its description says "09:30 BRT" — so **bare wallclock = BRT** already.
For 08:30 BRT use `OnCalendar=Mon..Fri 08:30`. Host uses **systemd user timers, not crontab**
(the report script's "Installed in crontab" comment is stale; `systemctl --user cat` confirms a unit).

Existing units (to copy):
```ini
# ~/.config/systemd/user/prod-primary-report.timer
[Unit]
Description=Mon-Fri 09:30 BRT report on PROD primary pod up-transition
[Timer]
OnCalendar=Mon..Fri 09:30
Persistent=false
[Install]
WantedBy=timers.target

# ~/.config/systemd/user/prod-primary-report.service
[Unit]
Description=PROD primary GPU pod schedule report (email via Brevo)
After=network-online.target
[Service]
Type=oneshot
ExecStart=/home/pedro/bin/prod-primary-schedule-report.sh
```

New units (same shape, fires 08:30 BRT, before the 09:00 pod up):
```ini
# ~/.config/systemd/user/gateway-price-sync.timer
[Timer]
OnCalendar=Mon..Fri 08:30
Persistent=false
[Install]
WantedBy=timers.target

# ~/.config/systemd/user/gateway-price-sync.service
[Service]
Type=oneshot
ExecStart=/home/pedro/bin/gateway-price-sync.sh
```
Enable: `systemctl --user daemon-reload && systemctl --user enable --now gateway-price-sync.timer`.
(No `WorkingDirectory` in the existing unit — not needed; script uses absolute paths + `ssh n8n-ia-vm`.)

**Script conventions** (from `/home/pedro/bin/prod-primary-schedule-report.sh`): `#!/usr/bin/env bash`,
`set -euo pipefail`, secrets inline at top, log to `$HOME/<name>.log`, reach the gateway via
`ssh -o ConnectTimeout=15 n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl …'`. Brevo SMTP creds
already in that script if the new job should email a summary (optional — not required by the task).

---

## Recommended job outline (`/home/pedro/bin/gateway-price-sync.sh`)

1. `MODELS=$(ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl prices list')` is not needed — instead pull distinct recent models from `billing_events` (psql or a future `gatewayctl` helper) OR just maintain a static map seeded with `model.gguf`.
2. `curl open.er-api.com/v6/latest/USD` → BRL rate → `gatewayctl prices set-fx -usd-brl <rate>` (guarded).
3. `curl openrouter.ai/api/v1/models` once → cache pricing map.
4. For each target model: resolve base slug (strip `-YYYYMMDD` suffix), look up prompt/completion, `gatewayctl prices set` input_token + output_token. Always include `model.gguf → deepseek/deepseek-v4-flash`.
5. Log each `set` result; optional Brevo email summary on failure.

**Minimum viable (covers 83% of weekly chat traffic) = just steps 2 + the two `model.gguf` set calls in §3.**

---

## Sources
- `gateway/internal/proxy/interceptor_usage.go` (L167,245,259,449,527) — model capture + phantom provider
- `gateway/internal/billing/prices_loader.go:87`, `cost.go:34-44` — exact-key lookup, miss→0
- Live: `gatewayctl prices set|set-fx|list`, `model-alias list` on `ifix-ai-gateway` (n8n-ia-vm)
- Live PROD DB `bd_ai_gateway_prod` — `billing_events` model distribution, `fx_rates` schema/seed
- Live `GET openrouter.ai/api/v1/models` pricing; forex probes (open.er-api.com ✓, awesomeapi 429, exchangerate.host paywalled)
- `~/.config/systemd/user/prod-primary-report.{timer,service}`, `/home/pedro/bin/prod-primary-schedule-report.sh`, `timedatectl`
