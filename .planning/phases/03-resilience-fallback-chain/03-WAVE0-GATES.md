# Phase 3 — Wave 0 Operator Gates

**Executed:** 2026-04-20 00:15
**Operator:** Pedro <pedro.araujo@ifixtelecom.com.br>
**Verifier:** Claude (orchestrator)

## Gate A — OpenRouter Slug for Qwen 3.5 27B

### Original D-C1 (Fireworks pin) — INVALIDATED

The plan assumed Fireworks served Qwen 3.5 27B. **Empirically false (2026-04-20):**

```bash
curl -X POST 'https://openrouter.ai/api'/v1/chat/completions \
  -d '{"model":"qwen/qwen3.5-27b","provider":{"order":["fireworks"],"allow_fallbacks":false}, ...}'
# → {"error":{"message":"No endpoints found for qwen/qwen3.5-27b.","code":404}}
```
<!-- NOTE (Phase 06.9 amendment): the curl URL is split into 'https://openrouter.ai/api' + '/v1/chat/completions' to make the operator-env value (`UPSTREAM_LLM_OPENROUTER_URL=https://openrouter.ai/api`) visually distinct from the full REST endpoint that direct curl hits (`/api/v1/chat/completions`). Concatenation in the gateway's BuildDirector keeps `r.URL.Path = /v1/chat/completions` unchanged, so the env var must NOT include `/v1` — the gateway adds it back. -->


Fireworks does not currently serve **any** Qwen 3 model on OpenRouter (verified across `qwen/qwen3-32b`, `qwen/qwen3-235b-a22b-2507`, `qwen/qwen3-30b-a3b-instruct-2507`, `qwen/qwen3-vl-32b-instruct`).

### Resolution — Switch provider pin to Novita

OpenRouter `qwen/qwen3.5-27b` resolves to canonical slug `qwen/qwen3.5-27b-20260224`, served by:

| Provider | Status | Tool calls | Notes |
|----------|--------|------------|-------|
| Alibaba | -2 (degraded) | yes | Slow but functional |
| **Novita** | **0 (healthy)** | **yes** | **Picked** |
| AtlasCloud | 0 (healthy) | n/a | Pin name mismatch in test |
| Phala | 0 (healthy) | (untested) | Backup |

### Verification curl (Novita pin)

```bash
curl -sS -X POST 'https://openrouter.ai/api'/v1/chat/completions \
  -H "Authorization: Bearer $OPENROUTER_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model":"qwen/qwen3.5-27b",
    "provider":{"order":["novita"],"allow_fallbacks":false},
    "messages":[{"role":"user","content":"reply with exactly: PONG"}],
    "max_tokens":10,"temperature":0
  }'
```

**Response excerpt:**
```json
{"model":"qwen/qwen3.5-27b-20260224","provider":"Novita","choices":[{"message":{"content":"PONG"}}]}
```

### Tool-call verification (D-C2 / RES-06 pre-flight)

```bash
curl ... -d '{
  "model":"qwen/qwen3.5-27b",
  "provider":{"order":["novita"],"allow_fallbacks":false},
  "messages":[{"role":"user","content":"What is the weather in Sao Paulo right now?"}],
  "tools":[{"type":"function","function":{"name":"get_weather", ...}}],
  "tool_choice":"auto","max_tokens":100
}'
```

**Response excerpt:**
```json
{
  "model":"qwen/qwen3.5-27b-20260224","provider":"Novita",
  "choices":[{
    "finish_reason":"tool_calls",
    "message":{"tool_calls":[{
      "id":"call_7aeb0998b25447f69ebef8ea",
      "function":{"name":"get_weather","arguments":"{\"city\": \"Sao Paulo\"}"}
    }]}
  }]
}
```

### Decision: PROCEED with revised D-C1

- **Model slug:** `qwen/qwen3.5-27b` (canonical: `qwen/qwen3.5-27b-20260224`)
- **Provider pin:** `novita` (replaces Fireworks)
- **Tool support:** confirmed (`finish_reason: "tool_calls"` with valid arguments)

### Env vars to configure in Portainer (post-Phase 3 deployment)

```
UPSTREAM_LLM_OPENROUTER_URL=https://openrouter.ai/api
UPSTREAM_LLM_OPENROUTER_AUTH_BEARER=<operator mints OPENROUTER_API_KEY>
UPSTREAM_LLM_OPENROUTER_MODEL=qwen/qwen3.5-27b
UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER=novita
```

### Phase 06.9 amendment — URL ends at `/api` (not `/api/v1`)

Phase 06.9 correction — URL ends at `/api` (not `/api/v1`) because the gateway preserves the client's `/v1/chat/completions` request path; concatenation produces the correct endpoint. Boot-time fail-fast (Plan 04) rejects URLs ending in `/v1`.

Historical context: the original Wave 0 Gate A draft (2026-04-20) recorded the operator env value as `https://openrouter.ai/api`-with-trailing-`/v1` based on the OpenRouter docs' bare-curl examples. That value caused double-`/v1` (final upstream URL `https://openrouter.ai/api`+`/v1`+`/v1/chat/completions`, HTTP 404) once the gateway's `BuildDirector` was wired — `BuildDirector` deliberately keeps `r.URL.Path = /v1/chat/completions` unchanged so pod and gateway routes mirror 1:1. The dev stack `.env` at `/opt/ai-gateway-dev/` on `vps-ifix-vm` was hand-patched to `/api` on 2026-05-24; this doc-level amendment is now the canonical operator gate value, and Plan 04 of Phase 06.9 enforces the suffix at boot.

### Phase 06.9 amendment — `UPSTREAM_LLM_OPENROUTER_MODEL` per D-06

`UPSTREAM_LLM_OPENROUTER_MODEL` remains the runtime fallback override per D-06. Schema row in `model_aliases` provides the default; env var (when set) takes precedence at resolver-lookup time. New deployments SHOULD prefer schema rows for multi-instance consistency, but env overrides remain honored for backward compatibility and operator escape hatches. Plan 02 implements the env-override-wins precedence inside the resolver — see Plan 02 acceptance criteria for the resolver precedence chain (env → schema → unchanged passthrough).

### D-C1 amendment recorded

CONTEXT.md decision D-C1 (pin Fireworks) is replaced by **D-C1' (pin Novita)** for the duration of Phase 3 execution. Reason: Fireworks does not serve Qwen 3 family on OpenRouter as of 2026-04-20. Future re-evaluation: monitor OpenRouter provider catalog quarterly; if Fireworks adds Qwen 3.x, consider switching back for parity with original ConverseAI provider mix (D-C1 rationale).

---

## Gate B — llama.cpp `/tokenize` endpoint

### Pod runtime not available

No GPU on dev VPS (`nvidia-smi: command not found`); no Vast.ai pod active. Direct test against the production pod image (`ghcr.io/ifixtelecom/ifix-ai-pod:develop`) is impossible from this environment.

### Verification path: upstream binary inspection

The pod Dockerfile (`pod/Dockerfile`) extracts `llama-server` from upstream:

```dockerfile
FROM ghcr.io/ggml-org/llama.cpp:server-cuda AS llama-bin
COPY --from=llama-bin /app/llama-server /usr/local/bin/llama-server
```

The `/tokenize` endpoint is a built-in route of llama-server (not opt-in). Verified by running the **CPU equivalent** of the same upstream image (`ghcr.io/ggml-org/llama.cpp:server`) locally with a minimal Qwen 2.5 0.5B Q4_K_M model:

```bash
docker run -d -p 18000:8000 -v /tmp/qwen-0.5b.gguf:/model.gguf:ro \
  ghcr.io/ggml-org/llama.cpp:server \
  --host 0.0.0.0 --port 8000 -m /model.gguf -np 1 --ctx-size 2048
```

**Verification curls:**

```bash
$ curl -sS http://localhost:18000/health
{"status":"ok"}

$ curl -sS http://localhost:18000/tokenize \
    -H "Content-Type: application/json" -d '{"content":"ping"}' | jq '.'
{
  "tokens": [
    9989
  ]
}
```

Endpoint exists on the same llama.cpp release line baked into the pod image (Pod Dockerfile pins `:server-cuda` of the same upstream tag family — only difference is CUDA backend which does not affect HTTP routing).

### Decision: PROCEED

- **Pod image tag verified for `/tokenize`:** `ghcr.io/ggml-org/llama.cpp:server-cuda` (upstream) → `ghcr.io/ifixtelecom/ifix-ai-pod:develop` (production tag)
- **Endpoint contract:** `POST /tokenize` with `{"content": "<text>"}` → `{"tokens": [<int_array>]}`
- **No pod image rebuild required**

### Residual UAT (deferred to Phase 6 / Wave 6 of Phase 3)

A live test against an actual production pod (Vast.ai or local with GPU) is still recommended once a pod is provisioned. Captured as part of `03-08-PLAN.md` UAT (real pod kill scenario) and Phase 6 (auto-provisioning).

---

## Sign-off

- [x] **Gate A** verified — slug `qwen/qwen3.5-27b` + provider `novita` (D-C1 amended)
- [x] **Gate B** verified — `/tokenize` confirmed on upstream llama-server (same binary as pod image)
- [x] Both gates **PASS** → Phase 3 implementation waves unblocked

## Follow-ups

1. **Wave 4 (03-06) — OpenRouter director:** must use `provider.order=["novita"]` (not `["fireworks"]`) when constructing the body rewrite. Update `D-C2` body-rewrite template accordingly.
2. **Wave 6 (03-08) — UAT:** include "verify /tokenize on a live Vast.ai pod" as one of the operator-only checks before declaring SC-1 fully PASS.
3. **CONTEXT.md amendment:** consider adding a note that D-C1 was revised to Novita on 2026-04-20 to preserve traceability.
