# SEED-013 — LLM probe hardcodes `model:"qwen"`, no per-upstream model rewrite → tier-1 probe never gets a real 200

**Discovered:** 2026-06-16 during live OpenRouter-fallback investigation (prod rev 99e4e09).
**Severity:** LOW-MED — observability only. Real traffic UNAFFECTED (dispatcher applies the model_alias rewrite correctly). The probe just can't produce a true health signal for tier-1 LLM upstreams; it always gets a client-error response.
**Related:** [[SEED-012-prober-ignores-tier0-override]] (same "observability lies" family). Builds on quick task `260616-gtj` (which fixed the *status classification* — 4xx now records `config` not `failed` — but did NOT make the probe succeed).

## Mechanism

`gateway/internal/upstreams/probe.go:258` (the `llm` case in `dispatch`):

```go
body := []byte(`{"model":"qwen","messages":[{"role":"user","content":"ping"}],"max_tokens":1,"temperature":0}`)
req, err = http.NewRequestWithContext(ctx, http.MethodPost, u.URL+"/v1/chat/completions", bytes.NewReader(body))
```

- The model string `"qwen"` is hardcoded for **every** llm upstream, including tier-1 `openrouter-chat` (`u.URL = https://openrouter.ai/api`).
- OpenRouter has no model named `qwen` → returns 4xx (404/400 `no such model`).
- The **dispatcher** path rewrites `qwen` → the per-upstream `model_aliases` TARGET (`deepseek/deepseek-v4-flash:nitro` for openrouter-chat) before forwarding, so real chat traffic gets 200. The **probe** does not consult `model_aliases` → sends raw `qwen` → 4xx.

## Live evidence (2026-06-16, prod)

- `gatewayctl upstreams list`: `openrouter-chat probe=failed` (pre-260616-gtj) / will read `config` post-deploy.
- Direct gateway chat while tier-1 was reached (primary override cleared via force-down): `model=deepseek/deepseek-v4-flash-20260423`, `provider=Novita`, **HTTP 200** — upstream is healthy, only the probe's synthetic request is malformed for it.
- tier-0 local-llm probe is unaffected by this (local llama.cpp accepts any model string / ignores it), so this bites tier-1 (and any future provider that validates the model field).

## Fix direction (for a future phase or quick task)

Make the probe resolve the per-upstream model the same way the dispatcher does, so it sends a model the provider actually serves and gets a real 200/health signal:

- In `dispatch` (llm case), look up the upstream's `model_aliases` TARGET for role=llm and substitute it into the probe body instead of the literal `"qwen"`. Same idea likely applies to `embed` (`model:"probe-default"` L265) and any provider that validates the model field.
- Keep the 260616-gtj 4xx→`config` classification as a safety net for genuine config drift.
- Validation: after fix, `openrouter-chat probe` should read `ok` (200) in prod, and a unit test should assert the probe body carries the resolved per-upstream model, not the literal alias name.

## Why deferred (not folded into 260616-gtj)

260616-gtj was scoped to stop the **lie** (status said `failed` on a healthy upstream) with zero blast radius. Making the probe *succeed* requires wiring `model_aliases` resolution into the probe path — a larger change touching the loader/alias lookup, worth its own task. With 260616-gtj deployed, the surface is already honest (`config`, not `failed`); this SEED upgrades it from honest-but-blind to a true health signal.
