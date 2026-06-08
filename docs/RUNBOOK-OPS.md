# RUNBOOK-OPS — STT Cascade Operations

**Scope:** Day-2 operator playbook for the tier-1 STT cascade introduced in Phase 11.2
(`local-stt` → `gemini-stt` → `groq-whisper` → `openai-whisper`).
Covers key minting, env-var wiring, operator gates, breaker cooldown tuning,
quota monitoring, common pitfalls, and rotation procedures for the two new
tier-1 STT upstreams (Gemini 2.5 Flash Lite + Groq Whisper-large-v3).

**Related runbooks (do not duplicate — cross-reference instead):**

- `gateway/docs/RUNBOOK-FAILOVER.md` — cascade architecture + tier table (canonical).
- `gateway/docs/RUNBOOK-DEPLOY.md` — full env-var reference for prod compose deploys.
- `gateway/docs/RUNBOOK-PRIMARY-POD.md` — pod-side `local-stt` (Speaches) ops.
- `gateway/docs/RUNBOOK-QUOTAS-BILLING.md` — cost ledger + per-tenant quota math.

**Env-var naming convention (slot-based):** Phase 11.2 introduced
`UPSTREAM_STT_FALLBACK_1_*` and `UPSTREAM_STT_FALLBACK_2_*` to keep the env
var name decoupled from the provider. Current slot assignment:

| Slot | Provider | DB row `name` | Director | Default model |
|------|----------|---------------|----------|---------------|
| 1 | Google AI Studio (Gemini) | `gemini-stt` | `gemini_stt_director.go` (multipart→JSON + `x-goog-api-key`) | `gemini-2.5-flash-lite` |
| 2 | Groq Cloud | `groq-whisper` | **REUSES** `BuildOpenAIWhisperDirector` (OpenAI-compatible) | `whisper-large-v3` |

DB row names are provider-specific because director selection depends on them.
Env names are slot-based so the operator can swap a provider into a slot
without renaming envs.

---

## Gemini STT Operations

Slot 1 of the tier-1 STT cascade. Cheapest of the three tier-1 fallbacks
(~$0.05/h batch). Primary fallback when the pod is asleep.

### Key minting

1. Go to **https://aistudio.google.com/app/apikey**.
2. Sign in with the operator Google account that owns the Ifix AI Studio project.
3. Click **Create API key** → **Create API key in existing project** →
   select the Ifix project (currently shared dev+prod per Phase 11.2 D-B9).
4. Copy the value once — AI Studio does **not** show it again.

> **D-B9 — one key shared dev+prod (deferred separation):**
> A single Gemini key is used for both `/opt/ai-gateway-dev/.env` (vps-ifix-vm)
> and any future prod stack. Justification: the converseai-v4 prod stack is
> currently decommissioned (CLAUDE.md). Once a new prod target is chosen,
> mint a second key labelled `ifix-ai-gateway-prod` and split envs.

### Env vars

Three slot-1 vars wire Gemini into the gateway. All three must be set
before the gateway boots — empty `AUTH_BEARER` triggers the cascade to
skip slot 1 silently (the breaker stays observation-driven; misconfig
looks like a healthy 2-upstream cascade until the operator notices the
absent `audit_log.upstream=gemini-stt` rows).

```bash
UPSTREAM_STT_FALLBACK_1_URL=https://generativelanguage.googleapis.com/v1beta
UPSTREAM_STT_FALLBACK_1_AUTH_BEARER=<paste from AI Studio>
UPSTREAM_STT_FALLBACK_1_MODEL=gemini-2.5-flash-lite
```

**Model override:** Operator can set `UPSTREAM_STT_FALLBACK_1_MODEL=gemini-2.5-flash`
if Lite degrades on a specific tenant pattern. Per-instance escape hatch —
schema row in `ai_gateway.model_aliases` is the canonical default.

### Operator gate D-B10 (Wave 0 — pre-deploy)

Before merging any Phase 11.2 code that depends on the Gemini upstream,
the operator must confirm the key is present in the running container:

```bash
ssh vps-ifix-vm 'docker inspect ai-gateway-dev --format "{{range .Config.Env}}{{println .}}{{end}}" | grep UPSTREAM_STT_FALLBACK_1_AUTH_BEARER'
```

Output non-empty (`UPSTREAM_STT_FALLBACK_1_AUTH_BEARER=AIza...`) = gate green.
Empty/missing = operator must edit `/opt/ai-gateway-dev/.env` and run
`docker compose up -d gateway` before approving the wave.

No functional smoke gating at this stage — Wave 6 UAT
(`pod/smoke/uat-11.2.sh`) covers end-to-end behaviour with real audio.

### Cooldown tuning (D-B11 — circuit_config JSONB)

Gemini AI Studio enforces per-key RPM/TPM limits. A burst that trips the
free-tier RPM ceiling returns HTTP 429 — the breaker opens. Default
breaker cooldown is too short for Gemini quotas (60s default vs 60s
rolling window): the breaker reopens immediately, oscillates, and burns
budget on the next slot.

**Phase 11.2 default:** `cooldown_s = 120` (2× the 1-min rolling window).
Persisted in `ai_gateway.upstreams.circuit_config` JSONB column on the
`gemini-stt` row (migration `0029_readd_whisper_add_gemini_groq.sql`).
The loader at `gateway/internal/upstreams/types.go:97` parses
`CircuitConfig.CooldownS` and feeds it into the FSM.

**To re-tune** (e.g., move to paid Gemini tier with higher RPM):

```sql
-- Connect to gateway DB (dev: ssh vps-ifix-vm, prod: DO Postgres console)
UPDATE ai_gateway.upstreams
   SET circuit_config = jsonb_set(circuit_config, '{cooldown_s}', '60'::jsonb)
 WHERE name = 'gemini-stt';
```

Resolver picks up the new value within ≤1s via NOTIFY (no gateway restart).
Verify with `gatewayctl upstream show gemini-stt`.

### Quota monitoring

- **AI Studio dashboard:** https://aistudio.google.com/app/usage
  Shows per-key RPM/TPM/RPD usage. Watch for 80%+ utilisation = imminent
  breaker oscillation per Pitfall 1.
- **Gateway-side signal:** Frequent transitions `gemini-stt: CLOSED → OPEN → CLOSED`
  in audit log = quota pressure, not infrastructure. Increase
  `cooldown_s` to push more traffic to slot 2 (Groq) until quota lifts.

### Pitfall reference (from Phase 11.2 RESEARCH.md)

| # | Pitfall | Symptom | Mitigation |
|---|---------|---------|------------|
| 1 | Cascade race / breaker oscillation | `audit_log` flips slot 1↔2 inside 60s | `cooldown_s=120` (D-B11) |
| 2 | 18 MB audio cap (post-base64) | HTTP 413 from gateway pre-flight | Per Plan 04 unit test; Wave 6 covers live cases |
| 3 | `Authorization: Bearer` strip → `x-goog-api-key` | HTTP 401 from Gemini | Director rewrites header — verify with `tcpdump` if 401 appears |
| 6 | Key scoping (one project = mixed dev+prod traffic) | Mixed quota burn | D-B9 deferred — mint second key when prod target chosen |

### Rotation procedure

When the operator rotates the Gemini key (suspected leak, quarterly
hygiene, or quota tier change):

1. **Mint new key** in AI Studio (do not revoke the old one yet).
2. **Update `.env` on dev VM:**
   ```bash
   ssh vps-ifix-vm
   sudo -e /opt/ai-gateway-dev/.env  # edit UPSTREAM_STT_FALLBACK_1_AUTH_BEARER
   ```
3. **Reload gateway:**
   ```bash
   cd /opt/ai-gateway-dev && docker compose up -d gateway
   ```
4. **Verify new key is live (D-B10 gate command):**
   ```bash
   docker inspect ai-gateway-dev --format '{{range .Config.Env}}{{println .}}{{end}}' | grep UPSTREAM_STT_FALLBACK_1_AUTH_BEARER
   ```
   Last 8 chars should match the new key.
5. **Smoke test** (single transcription with pod-OFF to force tier-1):
   ```bash
   ssh vps-ifix-vm 'docker exec ai-gateway-dev /gatewayctl breaker force-open local-stt --ttl=60s'
   curl -X POST https://ai-gateway.converse-ai.app/v1/audio/transcriptions \
        -H "Authorization: Bearer <tenant-key>" \
        -F "file=@/tmp/test-5s.wav" -F "model=whisper-1"
   # Verify audit_log row: upstream=gemini-stt, status=200
   ```
6. **Revoke the old key** in AI Studio once the new one is verified live.

---

## Groq Whisper Operations

Slot 2 of the tier-1 STT cascade. Second fallback when slot 1 (Gemini) is
breaker-open. ~$0.111/h (whisper-large-v3 hosted on Groq's LPU
infrastructure). **REUSES** the existing `BuildOpenAIWhisperDirector` —
Groq's `/openai/v1/audio/transcriptions` endpoint is byte-identically
OpenAI-compatible.

### Key minting

1. Go to **https://console.groq.com/keys**.
2. Sign in with the operator Groq account.
3. Click **Create API Key** → label it `ifix-ai-gateway-dev` (or `-prod`).
4. Copy the value once — Groq does **not** show it again.

### Env vars

```bash
UPSTREAM_STT_FALLBACK_2_URL=https://api.groq.com/openai
UPSTREAM_STT_FALLBACK_2_AUTH_BEARER=<paste from console.groq.com/keys>
UPSTREAM_STT_FALLBACK_2_MODEL=whisper-large-v3
```

**Model is FIXED at `whisper-large-v3`** — Groq's latest stable Whisper
slug. Do not override unless Groq retires it; this minimises drift with
the pod-side `Systran/faster-whisper-large-v3` (tier-0) and matches the
OpenAI tier-1 quality bar.

### Operator gate D-B10 (Wave 0 — pre-deploy)

```bash
ssh vps-ifix-vm 'docker inspect ai-gateway-dev --format "{{range .Config.Env}}{{println .}}{{end}}" | grep UPSTREAM_STT_FALLBACK_2_AUTH_BEARER'
```

Output non-empty = gate green. Same semantics as slot 1 (above).

### Director REUSE — no Groq-specific code

Groq's STT endpoint is an exact OpenAI multipart contract clone. The
gateway treats `groq-whisper` and `openai-whisper` identically at the
director layer: both are built by `BuildOpenAIWhisperDirector` with
URL + bearer + model resolved per-upstream from the loader.

The only mapping change is in `gateway/internal/resolver/resolver.go`
`upstreamEnvVarMap`:

```go
"groq-whisper": "UPSTREAM_STT_FALLBACK_2_MODEL",
```

so `gatewayctl model-alias` + the env-override-wins precedence (D-06)
both work correctly for slot 2.

### Cooldown

Slot 2 uses the **default** breaker cooldown (not custom). Groq does not
exhibit the Gemini RPM-oscillation pattern observed in Phase 11.2 spike;
default observation-driven breaker is sufficient. If oscillation appears
post-launch, add a custom `circuit_config.cooldown_s` row on `groq-whisper`
following the SQL pattern in **Gemini § Cooldown tuning** above.

### Pricing reference

- **Public pricing:** https://console.groq.com/docs/speech-text
- **Phase 11.2 reference:** ~$0.111/h of transcribed audio (whisper-large-v3,
  May 2026 pricing). ~2× more expensive than Gemini Lite, ~3× cheaper than
  OpenAI Whisper.

### Rotation procedure

Identical to **Gemini § Rotation procedure** above — substitute `FALLBACK_2`
for `FALLBACK_1` in steps 2, 4, and 5; substitute `groq-whisper` for
`gemini-stt` in the smoke test verification.

When the Gemini key is rotated, the operator should also verify the Groq
key is still live (run the slot 2 D-B10 gate command) to avoid discovering
a stale key only when slot 1 next opens.

---

## Cross-slot operator commands

### Inspect all three tier-1 STT upstreams at once

```bash
ssh vps-ifix-vm 'docker exec ai-gateway-dev /gatewayctl upstream list --role=stt'
```

Output should list 4 rows: `local-stt` (tier=0), `gemini-stt` (tier=1
prio=10), `groq-whisper` (tier=1 prio=15), `openai-whisper` (tier=1 prio=20).

### Force-open a slot to test takeover (D-B13 UAT scenarios)

```bash
# Force slot 1 OPEN → cascade should jump to slot 2 (groq-whisper)
ssh vps-ifix-vm 'docker exec ai-gateway-dev /gatewayctl breaker force-open gemini-stt --ttl=120s'

# Force slot 1 + slot 2 OPEN → cascade should land on slot 3 (openai-whisper)
ssh vps-ifix-vm 'docker exec ai-gateway-dev /gatewayctl breaker force-open groq-whisper --ttl=120s'
```

`audit_log.upstream` confirms the landing slot. TTL caps at 300s per
RUNBOOK-FAILOVER force-open policy.

### Sensitive-tenant invariant (RES-08)

Sensitive tenants (`telefonia`, `cobrancas` per CLAUDE.md) **never** route
to a tier-1 STT upstream. With `local-stt` breaker OPEN they return
HTTP 503 `upstream_unavailable_for_sensitive_tenant`. The Gemini/Groq
keys are present in the env but are unreachable by these tenants by
design — verified by Wave 6 UAT scenario 6 (D-B13).
