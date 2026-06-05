# Failover & Circuit Breaker Runbook

**Phase 3 (`ifix-ai-gateway`) resilience layer.** Read this when:

- `/v1/health/upstreams` shows `status != "ok"`
- Alert fires on `gateway_breaker_trips_total > 0` in the last 5 min
- A client app reports `503` with `code: "upstream_unavailable*"`
- A client app reports `502` with `code: "tool_call_partial_stream"`
- Post-incident review of a failover event

This document is the operator's first-line reference. Phase 7 will replace
the manual diagnosis steps with a dashboard + alerts; until then, follow
the diagnose → mitigate → verify cycle below.

---

## Mental Model (30 seconds)

The gateway has 5 upstreams (Phase 11.1 shrunk STT to tier-1 only):

| Role  | tier-0 (primary) | tier-1 (fallback) | Model contract                      |
|-------|------------------|-------------------|-------------------------------------|
| LLM   | `local-llm`      | `openrouter-chat` | Qwen 3.5 27B (local llama.cpp ↔ OpenRouter via Novita) |
| STT   | _(none)_         | `openai-whisper`  | OpenAI `whisper-1` (no local fallback — STT tier-0 removed in Phase 11.1) |
| EMBED | `local-embed`    | `openai-embed`    | local Infinity (BGE-M3) ↔ OpenAI `text-embedding-3-small` (`dimensions=1024`) |

Each upstream has an in-process circuit breaker (`sony/gobreaker/v2`):

```
CLOSED ── 3 consecutive failures ──▶ OPEN
                                       │
                                       ▼ 30s cooldown
                                    HALF_OPEN
                                       │
                          1 success ──┴── 1 failure ──▶ OPEN (reset cooldown)
                                       │
                                     CLOSED
```

A probe goroutine fires synthetic E2E requests every 10s against each
upstream (chat/STT/embed) so the breaker opens within ~30s even when no
real client traffic exposes the failure.

Breaker state is mirrored to Redis (`gw:breaker:{name}` Hash + `gw:breaker:events`
Pub/Sub) so other gateway replicas converge on OPEN within <1s. Phase 3
runs single-replica today; cross-replica convergence is exercised in
Phase 6 when the second replica + emergency pod ship.

### Policy differences by tenant + stream type

| tier-0 state | data_class  | streaming     | Action                                            |
|--------------|-------------|---------------|---------------------------------------------------|
| CLOSED       | normal      | any           | dispatch tier-0                                   |
| CLOSED       | sensitive   | any           | dispatch tier-0                                   |
| OPEN/HALF    | normal      | any           | dispatch tier-1 if CLOSED, else 503               |
| OPEN/HALF    | sensitive   | streaming     | 503 immediate (D-B4 fail-fast)                    |
| OPEN/HALF    | sensitive   | non-streaming | 3-attempt retry (200ms / 800ms / 3s ≈ 4s) → tier-0 if CLOSED, else 503 |

**Sensitive tenants are NEVER routed to OpenRouter/OpenAI** (LGPD policy
RES-08). They block at the gateway with `code: "upstream_unavailable_for_sensitive_tenant"`
and `Retry-After: 30`.

**Tool-call streams never failover.** If the SSE response body contains
`"tool_calls"` in the first 8 KB and the upstream connection drops, the
gateway emits a terminal SSE event:

```
event: error
data: {"error":{"type":"api_error","code":"tool_call_partial_stream", ...}}
```

The agent layer must detect this and retry with a NEW idempotency key.

### Measured baselines (test machine, integration suite)

These are observed wall times from `gateway/internal/integration_test/` —
Phase 3 Plan 03-07 results. Operators should reproduce on production
hardware before declaring SC-1 production-ready (Plan 03-08 UAT).

| Property                             | Budget   | Observed (test machine) |
|--------------------------------------|----------|-------------------------|
| SC-1 — failover (tier-0 dead → tier-1) | ≤10s   | **207 ms**              |
| D-D4 — hot-reload disable            | <1s      | **53 ms**               |
| D-D4 — hot-reload re-enable          | <1s      | **51 ms**               |
| D-B4 — sensitive streaming fail-fast | <500ms   | <100 ms                 |
| D-B1 — sensitive non-stream retry budget | ~4s | ~4.0 – 4.3 s            |

---

## Quick Diagnosis (~2 minutes)

Run these in order; stop at the first one that points to a clear cause.

```bash
# 1. Live per-upstream state from the gateway itself
curl -sS https://gateway-dev.ifixtelecom.com.br/v1/health/upstreams | jq

# 2. gatewayctl table view (includes URL_ENV / AUTH_BEARER_ENV / probe metadata)
docker exec ifix-ai-gateway /gatewayctl upstreams list

# 3. Recent breaker mirror state in Redis
redis-cli -h infra-redis-1 KEYS 'gw:breaker:*'
redis-cli -h infra-redis-1 HGETALL gw:breaker:local-llm
# Subscribe live to transitions:
redis-cli -h infra-redis-1 SUBSCRIBE gw:breaker:events

# 4. Audit log — recent dispatches + outcomes
psql "$AI_GATEWAY_PG_DSN" -c "
  SELECT created_at, request_id, tenant_id, route, upstream, status_code, error_code, latency_ms
    FROM ai_gateway.audit_log
   WHERE created_at > now() - interval '10 minutes'
   ORDER BY created_at DESC
   LIMIT 30;"

# 5. Sensitive-tenant blocks in the same window
psql "$AI_GATEWAY_PG_DSN" -c "
  SELECT count(*) AS blocked_5min, tenant_id
    FROM ai_gateway.audit_log
   WHERE upstream = 'blocked_sensitive'
     AND created_at > now() - interval '5 minutes'
   GROUP BY tenant_id;"

# 6. Prometheus metrics
curl -sS https://gateway-dev.ifixtelecom.com.br/metrics \
  | grep -E '^gateway_(breaker|probe|sensitive|tool_call_partial|upstream_throttled|upstreams_reload)'
```

Key metrics:

- `gateway_breaker_state{upstream}` — 0=closed, 1=half-open, 2=open
- `gateway_breaker_trips_total{upstream}` — counter (CLOSED→OPEN events)
- `gateway_breaker_mirror_failures_total` — Redis HSET/PUBLISH failures
- `gateway_probe_duration_ms{upstream}` — synthetic probe wall time
- `gateway_probe_failure_total{upstream,reason}` — probe failures
- `gateway_sensitive_retry_total{outcome}` — outcomes: `closed | exhausted | canceled | blocked_response`
- `gateway_tool_call_partial_total{route,upstream}` — tool-call streams that triggered the terminal SSE error event
- `gateway_upstream_throttled_total{upstream,status}` — 429 responses (capacity, NOT health; does not trip the breaker)
- `gateway_upstreams_reload_total{result}` — LISTEN/NOTIFY-driven reloads

---

## Incident Response by Symptom

### Symptom 1 — `local-llm.state == "open"` for >2 minutes

**Likely cause:** Vast.ai pod is dead, unreachable, or the llama-server
process crashed (OOM is the most common).

**Diagnose:**

1. SSH or web-shell into the pod host.
2. `docker ps` — is the `llama-server` container running?
3. `curl http://<pod-ip>:8000/health` — does it respond?
4. `curl -sS -X POST http://<pod-ip>:8000/tokenize -H 'Content-Type: application/json' -d '{"content":"ping"}'` — `/tokenize` is the contract the gateway depends on for the 16k context cap (RES-07 / SC-5); a 404 here means the pod image was rebuilt with a llama.cpp version that dropped the route.
5. If the container is stopped, inspect `docker logs llama-server | tail -100` for OOM, panic, or model-load failure.

**Mitigate:**

- **Pod is stopped and recoverable:** restart via `docker compose up -d llama-server` or re-run the pod's `onstart.sh`.
- **Pod is lost (instance evicted / Vast.ai outage):** Phase 6 auto-provisioning will spin a replacement. Until Phase 6 ships, manually destroy + recreate the Vast.ai instance — the procedure lives in `.planning/phases/01-gpu-pod-image-smoke-test/01-09-SUMMARY.md`.
- **Meanwhile:** normal tenants are routed to `openrouter-chat` automatically (Novita-pinned Qwen 3.5 27B); sensitive tenants receive 503s with `Retry-After: 30`. Client apps should back off per their own retry policy.

**Recovery:** once the pod is healthy and `/tokenize` + `/health` pass, the
probe loop will close the breaker within 30s (cooldown timeout + 1
successful probe cycle). Verify:

```bash
curl -sS https://gateway-dev.ifixtelecom.com.br/v1/health/upstreams \
  | jq '.upstreams["local-llm"]'
# {"state":"closed","role":"llm","tier":0,"last_probe_ms":120,"last_probe_at":"…","last_probe_status":"ok"}
```

### Symptom 2 — `openrouter-chat.state == "open"`

**Likely cause:** OpenRouter API down, the pinned Novita provider down,
or our `UPSTREAM_LLM_OPENROUTER_AUTH_BEARER` key was revoked / hit a hard
spend cap.

**Diagnose:**

1. Test directly:
   ```bash
   curl -sS -H "Authorization: Bearer $UPSTREAM_LLM_OPENROUTER_AUTH_BEARER" \
        https://openrouter.ai/api/v1/models | jq '.data[0]'
   ```
   401/403 → key issue. 5xx / timeout → OpenRouter outage.
2. Test the pinned provider exactly:
   ```bash
   curl -sS -X POST https://openrouter.ai/api/v1/chat/completions \
     -H "Authorization: Bearer $UPSTREAM_LLM_OPENROUTER_AUTH_BEARER" \
     -H "Content-Type: application/json" \
     -d '{"model":"qwen/qwen3.5-27b","provider":{"order":["novita"],"allow_fallbacks":false},"messages":[{"role":"user","content":"PING"}],"max_tokens":5}'
   ```
   404 "No endpoints found" → Novita dropped the model; see
   `03-WAVE0-GATES.md` for the procedure to re-evaluate the provider pin
   (Wave 0 originally ruled out Fireworks for the same reason, 2026-04-20).
3. Check status pages:
   - https://status.openrouter.ai
   - Novita status (https://novita.ai)

**Mitigate (critical when `local-llm` is also OPEN):**

- Normal tenants will receive 503 `upstream_unavailable` → client apps MUST back off.
- **There is no tier-2 for chat** (decision **D-C4** — drift Qwen → GPT is too large to substitute under the "Qwen fixo" project decision). Document in comms: chat is unavailable until one of (`local-llm`, `openrouter-chat`) recovers.
- If OpenRouter itself is down: contact OpenRouter, wait for status-page recovery.
- If our key is the problem: rotate in Portainer stack env `UPSTREAM_LLM_OPENROUTER_AUTH_BEARER`, then restart the gateway container (env vars are read at boot; the upstream Loader resolves bearer values via `os.Getenv` per the row's `auth_bearer_env` column on each Refresh).
- If Novita dropped Qwen 3.5 27B: temporarily switch the provider pin via Portainer env without redeploy:
  ```
  UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER=alibaba   # or other healthy provider per OpenRouter dashboard
  ```
  Restart the gateway container; the next request rebuilds the OpenRouter Director with the new provider order.

### Symptom 3 — Sensitive tenant reports 503s during normal load

**Likely cause:** `local-llm` (or `local-embed`) breaker is
OPEN and the sensitive 4s retry budget exhausted (D-B1). (Phase 11.1
note: STT no longer has a tier-0 — `openai-whisper` tier-1 is the only
STT upstream, and sensitive tenants never route to OpenAI per RES-08, so
STT sensitive requests fail-fast at the gateway regardless of breaker state.)

**Verify:**

```bash
psql "$AI_GATEWAY_PG_DSN" -c "
  SELECT count(*) AS blocked_5min
    FROM ai_gateway.audit_log
   WHERE upstream = 'blocked_sensitive'
     AND created_at > now() - interval '5 minutes';"
```

Any non-zero count means sensitive requests hit the LGPD block path. The
audit row contains `tenant_id`, `route`, `request_id`, `latency_ms`, and
`error_code = "upstream_unavailable_for_sensitive_tenant"`. There is **no
matching `audit_log_content` row** — Phase 2 D-B2 forbids persisting
sensitive content even on failure.

**Mitigate:**

- This is **expected behavior per LGPD policy (RES-08)** — sensitive data
  MUST NOT cross our trust boundary into OpenAI/OpenRouter, even during
  primary outages. There is no override.
- Recover the relevant tier-0 (Symptom 1 runbook for `local-llm`; the
  same shape applies to `local-embed`).
- After recovery, `Retry-After: 30` means client apps can replay the
  same request safely (the gateway honors idempotency keys per Phase 2
  D-C contract).

**Streaming variant (D-B4):** sensitive + `stream:true` + tier-0 OPEN
returns 503 immediately (no 4s retry — fail-fast pre-flight). The audit
row still records `upstream='blocked_sensitive'`. No client-side retry
loop is needed — the 503 envelope is identical to the non-streaming
case.

### Symptom 4 — Tool-call request returned 502 / `tool_call_partial_stream`

**This is expected behavior, not an incident.** Per RES-06 / SC-4, the
gateway intentionally does NOT retry tool-call streams after the first
`tool_calls` delta has been emitted to the client.

What the client sees on a tool-call stream that the upstream then drops:

- The opening `data:` chunks containing the partial tool_call delta.
- A terminal SSE event:
  ```
  event: error
  data: {"error":{"type":"api_error","code":"tool_call_partial_stream","message":"…"}}
  ```
- HTTP status 200 (the response was already in flight when the error happened).

The agent layer (the application code consuming the gateway) MUST:

1. Detect the terminal `event: error` with `code: "tool_call_partial_stream"`.
2. Generate a NEW idempotency key (do not reuse the partial stream's key — that key is now associated with side-effects-already-executed; replaying it would either return the same partial stream from the idempotency cache or risk double execution depending on the agent's contract).
3. Retry the request from scratch.

If an agent is looping on the same idempotency key, that's a **client
bug** — file against the agent owner. Confirm via:

```bash
psql "$AI_GATEWAY_PG_DSN" -c "
  SELECT request_id, tenant_id, count(*) AS attempts
    FROM ai_gateway.audit_log
   WHERE error_code = 'tool_call_partial_stream'
     AND created_at > now() - interval '10 minutes'
   GROUP BY request_id, tenant_id
   HAVING count(*) > 1;"
```

Same `request_id` appearing >1 time = client retried with the same key.

### Symptom 5 — `gatewayctl upstreams update` did not reload the gateway

**Diagnose:**

1. **Did the UPDATE actually change a config column?** Probe writebacks
   (`last_probe_at`, `last_probe_ms`, `last_probe_status`,
   `last_probe_error`, `updated_at`) intentionally do **NOT** trigger a
   reload — see migration `0009_upstreams_notify_trigger.sql` and
   research **Pitfall 7** (the WHEN clause filters out probe-only writes
   so the gateway is not flooded with reloads at probe cadence).
2. **Is the gateway's LISTEN connection alive?**
   ```bash
   psql "$AI_GATEWAY_PG_DSN" -c "
     SELECT pid, application_name, query
       FROM pg_stat_activity
      WHERE query LIKE '%LISTEN upstreams_changed%';"
   ```
   Should return exactly 1 row per gateway replica with `query =
   'LISTEN upstreams_changed'`. If 0 rows, the dedicated LISTEN
   connection died and the auto-reconnect backoff has not kicked in
   yet (or is failing — check next step).
3. **Check gateway logs for LISTEN errors:**
   ```bash
   docker logs ifix-ai-gateway 2>&1 | grep -E 'module=LISTEN|module=UPSTREAMS' | tail -50
   ```
4. **Verify the reload metric:**
   ```bash
   curl -sS https://gateway-dev.ifixtelecom.com.br/metrics | grep gateway_upstreams_reload_total
   # gateway_upstreams_reload_total{result="ok"}    47
   # gateway_upstreams_reload_total{result="error"}  0
   ```
   `error` counter increasing = NOTIFY arriving but the loader's
   `SELECT * FROM upstreams` is failing.

**Mitigate:** restart the gateway container — the listener reconnects on
boot and reloads the in-memory snapshot from the table. No data loss
(the table is the source of truth; in-memory map is just a cache).

```bash
docker restart ifix-ai-gateway
# Then verify the listener reconnected:
psql "$AI_GATEWAY_PG_DSN" -c "
  SELECT pid FROM pg_stat_activity WHERE query LIKE '%LISTEN upstreams_changed%';"
```

---

## Operator Commands (`gatewayctl upstreams`)

The Phase 3 admin surface (Plan 03-07) is `gatewayctl upstreams`. All
mutations write to `ai_gateway.upstreams` and trigger the LISTEN/NOTIFY
hot-reload pipeline; no gateway restart required.

```bash
# Inspect current state
docker exec ifix-ai-gateway /gatewayctl upstreams list
# NAME             ROLE   TIER  ENABLED  URL_ENV                       AUTH_BEARER_ENV                       LAST_PROBE_STATUS  LAST_PROBE_MS  LAST_PROBE_AT
# local-llm        llm    0     true     UPSTREAM_LLM_URL              -                                     ok                 120            2026-04-20T01:35:50Z
# openrouter-chat  llm    1     true     UPSTREAM_LLM_OPENROUTER_URL   UPSTREAM_LLM_OPENROUTER_AUTH_BEARER   ok                 320            2026-04-20T01:35:30Z

# Disable a fallback before rotating its key
docker exec ifix-ai-gateway /gatewayctl upstreams disable --name=openrouter-chat
# Re-enable after rotation
docker exec ifix-ai-gateway /gatewayctl upstreams enable --name=openrouter-chat

# Tune breaker thresholds (MERGES into circuit_config JSONB — does not overwrite Phase 5 saturation thresholds)
docker exec ifix-ai-gateway /gatewayctl upstreams update \
  --name=local-llm --circuit-failures=5 --circuit-cooldown-s=60

# Move a row's tier (rare; only when restructuring fallback chains)
docker exec ifix-ai-gateway /gatewayctl upstreams update --name=openrouter-chat --tier=1
```

Non-zero exit codes:

- `1` — lookup failure (e.g. `--name=foo` for a row that doesn't exist), or DB error.
- `2` — usage error (missing required flag, malformed value).

### Why operators should NOT edit env vars without restarting

URLs and bearer secrets live in env vars (Portainer stack); the
`upstreams` table only stores the env var **name** in `url_env` /
`auth_bearer_env`. Changing the env var value in Portainer requires a
container restart for the new value to propagate. The Loader resolves
`os.Getenv(row.url_env)` and `os.Getenv(row.auth_bearer_env)` on each
Refresh, but Refresh only re-reads the table — the OS environment is
captured at process boot.

**To rotate a bearer secret:**

1. Update the env var value in Portainer (e.g. `UPSTREAM_LLM_OPENROUTER_AUTH_BEARER=<new key>`).
2. Restart the gateway container (`docker restart ifix-ai-gateway`).
3. Verify the next dispatch succeeds:
   ```bash
   curl -sS https://gateway-dev.ifixtelecom.com.br/v1/health/upstreams | jq '.upstreams["openrouter-chat"]'
   ```

---

## Required Env Vars (Portainer stack)

For full Phase 3 operation, the following env vars must be set on the
gateway container (referenced by row entries in
`ai_gateway.upstreams.url_env` / `auth_bearer_env`):

| Var                                         | Required | Purpose                                                                                  |
|---------------------------------------------|----------|------------------------------------------------------------------------------------------|
| `UPSTREAM_LLM_URL`                          | yes      | Local llama-server `:8000` URL (tier-0 LLM)                                              |
| `UPSTREAM_EMBED_URL`                        | yes      | Local Infinity `:8002` URL (tier-0 EMBED)                                                |
| ~~`UPSTREAM_STT_URL`~~                      | _removed_ | _Phase 11.1: local STT removed; speech-to-text now resolves only via `UPSTREAM_STT_OPENAI_*`._ |
| `UPSTREAM_LLM_OPENROUTER_URL`               | for chat fallback | `https://openrouter.ai/api/v1`                                                  |
| `UPSTREAM_LLM_OPENROUTER_AUTH_BEARER`       | for chat fallback | OpenRouter API key                                                              |
| `UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER`    | no       | CSV; default **`novita`** (D-C1' amendment per `03-WAVE0-GATES.md` — Fireworks does NOT serve Qwen 3 family on OpenRouter as of 2026-04-20) |
| `UPSTREAM_LLM_OPENROUTER_ALLOW_FALLBACKS`   | no       | `false` (default); set `true` only to allow OpenRouter to silently failover internally   |
| `UPSTREAM_STT_OPENAI_URL`                   | for stt fallback | `https://api.openai.com/v1`                                                     |
| `UPSTREAM_STT_OPENAI_AUTH_BEARER`           | for stt fallback | OpenAI API key                                                                  |
| `UPSTREAM_EMBED_OPENAI_URL`                 | for embed fallback | `https://api.openai.com/v1`                                                   |
| `UPSTREAM_EMBED_OPENAI_AUTH_BEARER`         | for embed fallback | OpenAI API key                                                                |
| `WRITE_TIMEOUT_CHAT_SECONDS`                | no       | `0` (default; SSE unlimited)                                                             |
| `WRITE_TIMEOUT_EMBED_SECONDS`               | no       | `30` (default)                                                                           |
| `WRITE_TIMEOUT_AUDIO_SECONDS`               | no       | `120` (default; Whisper long multipart)                                                  |

The gateway boots cleanly when fallback bearers are missing — the
dispatcher omits the corresponding tier-1 proxy and returns 503 when
tier-0 OPEN until the bearer is provisioned.

---

## OpenRouter Provider Pin (D-C1 amendment)

The original Phase 3 design pinned **Fireworks** for Qwen 3.5 27B (D-C1
in `03-CONTEXT.md`). Wave 0 operator gates (`03-WAVE0-GATES.md`,
2026-04-20) found that Fireworks does not serve any Qwen 3 family model
on OpenRouter — every probe returned `404 "No endpoints found"`.

**Current production pin: Novita.** Verified via:

```bash
curl -sS -X POST https://openrouter.ai/api/v1/chat/completions \
  -H "Authorization: Bearer $UPSTREAM_LLM_OPENROUTER_AUTH_BEARER" \
  -H "Content-Type: application/json" \
  -d '{
    "model":"qwen/qwen3.5-27b",
    "provider":{"order":["novita"],"allow_fallbacks":false},
    "messages":[{"role":"user","content":"reply with exactly: PONG"}],
    "max_tokens":10,"temperature":0
  }'
# {"model":"qwen/qwen3.5-27b-20260224","provider":"Novita","choices":[{"message":{"content":"PONG"}}]}
```

Tool-call support also confirmed (`finish_reason: "tool_calls"` with
parseable `arguments`). The Director in
`gateway/internal/proxy/openrouter_director.go` injects
`provider.order = ["novita"]` + `allow_fallbacks = false` into every
`/v1/chat/completions` body via `map[string]json.RawMessage` rewrap (the
original request fields including `messages`, `tools`, and `tool_choice`
survive byte-identical — see Threat T-03-06-02 in 03-06-SUMMARY.md).

If Novita drops the Qwen 3.5 27B model in the future, switch the pin
without a code change: set `UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER` to
the next-best healthy provider per the OpenRouter dashboard (Alibaba is
the documented backup; AtlasCloud and Phala were also healthy as of
2026-04-20). Restart the gateway container.

---

## Cross-Replica Convergence (deferred to Phase 6)

Phase 3 ships the contract for cross-replica breaker convergence:

- Each gateway replica writes to `gw:breaker:{name}` Hash on its own state changes.
- Each replica publishes to `gw:breaker:events` Pub/Sub.
- Each replica subscribes to `gw:breaker:events` and applies remote OPEN
  events as a `remoteOpen` overlay (`gateway/internal/breaker/breaker.go`).

**Phase 3 deployment is single-replica**, so the convergence latency is
not exercised in production. The integration test
`TestIntegration_BreakerFullLifecycle` validates the in-process state
machine against a real Redis-backed mirror, but the cross-replica path
(replica A trips OPEN → replica B short-circuits within <1s) is
**deferred to Phase 6** when:

- A second replica ships behind a load balancer.
- Phase 6 spins up emergency Vast.ai pods on `local-llm.state == OPEN`
  via a leader-elected reconciler (Redis lock on `gw:reconciler:lead`).
- The cross-replica convergence test moves from "unexercised" to "load-tested
  with real network latency between replicas" as a Phase 6 entrance criterion.

Do not attempt to add a second gateway replica during Phase 3 operation.
The breakers will operate independently per replica until Phase 6's
reconciler ships.

---

## Escalation

- **1st responder:** on-call Ifix engineer (whoever is paged on alert).
- **Escalation:** Pedro <pedro.araujo@ifixtelecom.com.br>
- **Sustained sensitive-tenant 503s (>15 min):** communicate to affected tenants (Cobranças, Telefonia) via WhatsApp; the gateway will continue to block their writes until the local primary recovers (LGPD policy — no override path).
- **Total chat outage (`local-llm` AND `openrouter-chat` both OPEN):** notify all chat-consuming apps via WhatsApp (ConverseAI, Chat Ifix, Telefonia chat). There is no tier-2 for chat by design (D-C4) — recovery requires fixing one of the two upstreams.

---

## Related Docs

- `.planning/phases/03-resilience-fallback-chain/03-CONTEXT.md` — design decisions D-A1..D-D4 (breaker thresholds, sensitive policy, OpenRouter pin, hot-reload contract).
- `.planning/phases/03-resilience-fallback-chain/03-WAVE0-GATES.md` — operator-verified Novita pin + `/tokenize` contract (D-C1 amendment).
- `.planning/phases/03-resilience-fallback-chain/03-RESEARCH.md` — library choices + 9 pitfalls. Pitfall 7 covers the probe-writeback NOTIFY filter directly relevant to Symptom 5.
- `.planning/phases/03-resilience-fallback-chain/03-08-SUMMARY.md` — UAT results + Sentry breadcrumb observations from the live pod-kill test.
- `.planning/phases/01-gpu-pod-image-smoke-test/01-09-SUMMARY.md` — Vast.ai pod re-create procedure when the instance is lost.
- `gateway/README.md` — gateway operational overview, env vars, and deploy paths.
- Phase 7 will replace manual diagnosis with a Next.js dashboard + Prometheus alerts + WhatsApp/email routing.
