---
phase: 19-gateway-consolidation-worker-vm
plan: 03
subsystem: infra/supporting-surfaces
tags: [portainer, docker-swarm, dashboard, rerank, model-aliases, bd_ai_gateway, better-auth, digest-pin]
requires:
  - "19-01: ai-gateway overlay + swarm-string create route + swarmID + internal-Traefik Host routing (worker_intra)"
  - "19-02: consolidated gateway (stack 38) + embed (stack 39) live on worker-vm reading bd_ai_gateway + infra-redis-1"
provides:
  - "worker-vm ai-gateway-dashboard swarm stack (Portainer stack 40) — UI-editable, digest-pinned, login-proven against bd_ai_dashboard_prod, gateway data from bd_ai_gateway"
  - "worker-vm ai-gateway-rerank swarm stack (Portainer stack 41) — UI-editable, digest-pinned, public /v1/rerank verified 200 (host port 7998)"
  - "bd_ai_gateway model_aliases reconciled additively (10→19): 9 prod-only client-facing aliases ported + real-call verified (chat/embed/stt), closing Pitfall 6 404 drift"
affects:
  - "19-04 (DB migration — the 18 prod tenants land in bd_ai_gateway; the ported aliases already resolve for merged traffic)"
  - "19-05 (cutover — dashboard/rerank/gateway host routes ready; rerank cutover = repoint agents 10.10.10.20:7998 → 10.10.10.50:7998, one-line)"
  - "19-06 (decommission — dashboard + rerank now consolidated off n8n-ia-vm)"
tech-stack:
  added: []
  patterns:
    - "Two-DSN dashboard: DASHBOARD_DATABASE_URL (Better-Auth login store, bd_ai_dashboard_prod, sslmode=no-verify + search_path=public) kept as-is; AI_GATEWAY_PG_DSN (gateway DATA) repointed to bd_ai_gateway"
    - "Login-time DB-connectivity proof via Better-Auth sign-in (clean 401 INVALID_EMAIL_OR_PASSWORD, not 500) — catches wrong search_path/sslmode that landing 200/302 hides"
    - "Rerank consumed DIRECTLY (not gateway-fronted): host-published port 7998 mirrors prod topology → cutover is a one-line agents URL change"
    - "model_aliases hot-reload = 60s resolver POLL (models/resolver.go refreshInterval), NOT LISTEN/NOTIFY — additive INSERT picked up within 60s, no restart"
    - "Strictly-additive alias port: INSERT ... ON CONFLICT (alias, upstream_name) DO NOTHING — never UPDATE/DELETE a target row (threat T-19-03)"
key-files:
  created:
    - ".planning/phases/19-gateway-consolidation-worker-vm/19-03-SUMMARY.md"
  modified: []
  infra:
    - "portainer: stack 40 ai-gateway-dashboard (endpoint 6, swarm) — CREATED"
    - "portainer: stack 41 ai-gateway-rerank (endpoint 6, swarm) — CREATED"
    - "bd_ai_gateway: ai_gateway.model_aliases +9 rows (10→19), additive — MODIFIED"
    - "worker-vm: /root/gw-deploy/{dashboard.env(root-600),dashboard_compose.yml,rerank_compose.yml,gen_*.py} — CREATED (no secrets in compose)"
decisions:
  - "Dashboard login store (DASHBOARD_DATABASE_URL → bd_ai_dashboard_prod) kept unchanged — it is the Better-Auth owner/operator account store, OUT of the gateway tenant migration scope (19-04). Repointing it to bd_ai_gateway would break login (no auth tables there)."
  - "Rerank is NOT gateway-fronted (gateway has zero rerank route/upstream/env) — verified the REAL client-facing route: direct Infinity /v1/rerank. Published host port 7998 (deviated from plan's 'no host port') so the cross-swarm consumer (agents on vps-ifix-vm) can reach it and cutover stays a one-line URL change."
  - "All 9 prod-only aliases classified PORT (their upstream_name already exists in the identical DST upstream set) — zero ADD-UPSTREAM/MAP/REJECT needed."
  - "Dashboard image pinned to GHCR :latest-dev digest @sha256:07b7d18… (the running n8n-ia-vm image was built locally with empty RepoDigests; GHCR latest-dev is the promoted tag)."
metrics:
  duration: "~17m"
  completed: "2026-07-04"
  tasks: 3
  files_created: 1
---

# Phase 19 Plan 03: Dashboard + Rerank on worker-vm + model_alias reconcile Summary

Moved the two supporting surfaces onto worker-vm as UI-editable, digest-pinned Portainer swarm stacks — the **dashboard** (stack 40, login-proven against its Better-Auth store + gateway data from `bd_ai_gateway`) and the **rerank** service (stack 41, real `/v1/rerank` route verified 200) — and closed the **model_alias drift** (research Pitfall 6) by additively porting the 9 prod-only client-facing aliases into `bd_ai_gateway` (10→19), each proven with a REAL gateway call (chat + embed + STT transcription). Ran in parallel with the untouched live PROD gateway (n8n-ia-vm still Up 28h healthy, public `/health` 200); `bd_ai_gateway_prod` was read-only and remains at 17 aliases.

## What Was Built

### Task 1 — ai-gateway-dashboard swarm stack (Portainer stack 40, endpoint 6)
- **Service:** `dashboard` 1/1. **Image DIGEST-PINNED:** `ghcr.io/ifixtelecom/ifix-ai-dashboard@sha256:07b7d182aa53ba5da83c4f310afdc2267fa024a96accb1eecea62928a4afd753` (GHCR `:latest-dev`, force-pulled on worker-vm — the running n8n-ia-vm image had empty RepoDigests / built locally).
- **Networks:** `worker_intra` (internal-Traefik routing, SCHEME A) + `ai-gateway` (overlay to reach `gateway:8080` + `infra-redis-1`).
- **Router labels (deploy.labels):** `Host(ai-dashboard.converse-ai.app)` → `entrypoints=web` → service port 3001 → `traefik.docker.network=worker_intra`.
- **Two-DSN wiring (the crux):**
  - `DASHBOARD_DATABASE_URL` → **kept** `bd_ai_dashboard_prod?sslmode=no-verify&options=-c search_path=public` (Better-Auth login/owner store; out of 19-04 scope).
  - `AI_GATEWAY_PG_DSN` → **repointed** `bd_ai_gateway` (consolidated gateway DATA, matches 19-02; was `bd_ai_gateway_prod`).
  - `AI_GATEWAY_REDIS_ADDR` → `infra-redis-1:6379`, `AI_GATEWAY_REDIS_PASSWORD` → empty, `GATEWAY_BASE_URL` → `http://gateway:8080` (worker swarm gateway service DNS).
- **Login / DB-connectivity PROOF (not just landing):**
  - landing `/` → **307** → `/login` → **200** (via internal Traefik Host route `http://10.10.10.50:80`).
  - `POST /api/auth/sign-in/email` (bogus creds) → **HTTP 401 `INVALID_EMAIL_OR_PASSWORD`** — a CLEAN auth rejection, i.e. the login-time DB query executed against `bd_ai_dashboard_prod` (search_path/sslmode correct). A wrong search_path/sslmode would have returned 500.
  - Corroboration: `bd_ai_dashboard_prod public."user"` = **1 user / 1 owner** (seeded pedro) → a real login would succeed; `bd_ai_gateway ai_gateway.tenants` = 4 (dashboard's gateway-data DSN reachable).

### Task 2 — ai-gateway-rerank swarm stack (Portainer stack 41, endpoint 6)
- **Service:** `rerank` 1/1. **Image DIGEST-PINNED:** `michaelf34/infinity:0.0.77@sha256:11e8b3921b9f1a58965afaad4a844c435c9807cbc82c51e47cb147b7d977fc88` (same pin as embed). Model `BAAI/bge-reranker-base` served as `bge-reranker-base`, `--engine torch --device cpu --dtype float32 --url-prefix /v1 --port 7998`. Caps 6 GB / 4.0 CPU, named volume `ai-gateway-rerank-model-cache`.
- **On `ai-gateway` overlay + host-published port `7998`.**
- **PUBLIC route verified (the one clients actually use):** the gateway has **NO** rerank route/upstream/env (confirmed by repo grep + prod/worker gateway env inspect) — rerank is consumed **directly** (agents RAG hits `http://10.10.10.20:7998/v1/rerank`). Verified the real route on worker-vm: `POST http://10.10.10.50:7998/v1/rerank` with a 3-doc payload → **HTTP 200**, correctly ranked (relevant doc `relevance_score=0.935`, irrelevant `≈0.00004`). Backend `/health` → 200.
- **Consumer status (Open Q3):** prod rerank (n8n-ia-vm) shows **only healthcheck GETs** over the last 48h — no live `/rerank` POSTs; compose declares the consumer = converseai-v4 agents RAG (different repo, not in this tree). Log-absence is weak evidence (Codex LOW) → **consolidated regardless (safe default); drop deferred** to a later cleanup once consumers are confirmed absent.

### Task 3 — model_aliases reconcile into bd_ai_gateway (additive, real-call verified)

**Diff artifact (composite key `(alias, upstream_name)`):** SRC `bd_ai_gateway_prod` = 17 rows, DST `bd_ai_gateway` = 10 rows. **Upstream sets are IDENTICAL** in both DBs (10 upstreams each: gemini-stt, groq-whisper, local-embed, local-llm, local-stt, local-tts, openai-embed, openai-whisper, openrouter-chat, voice-api-piper) → every prod-only alias's `upstream_name` already exists in DST ⇒ all straight **PORT** (no ADD-UPSTREAM / MAP / REJECT).

**Prod-only set (9 rows) + classification:**

| alias | role | upstream_name | target | class | rationale |
|-------|------|---------------|--------|-------|-----------|
| `captain-embed` | embed | openai-embed | text-embedding-3-small | PORT | prod client embed alias; upstream present |
| `gpt-5.4-mini` | llm | openrouter-chat | deepseek/deepseek-v4-flash:nitro | PORT | Maestro copilot alias (memory) |
| `gpt-5.4-mini-2026-03-17` | llm | openrouter-chat | deepseek/deepseek-v4-flash:nitro | PORT | Maestro copilot exact model string (memory) |
| `ia-kanban` | llm | local-llm | qwen | PORT | ia-kanban tenant alias |
| `ia-kanban` | llm | openrouter-chat | deepseek/deepseek-v4-flash:nitro | PORT | ia-kanban tier-1 fallback |
| `whisper-large-v3` | stt | gemini-stt | gemini-2.5-flash-lite | PORT | voip n8n workflows hardcode this STT model string (memory stt-model-alias-whisper-large-v3) |
| `whisper-large-v3` | stt | groq-whisper | whisper-large-v3 | PORT | same alias, STT cascade slot |
| `whisper-large-v3` | stt | local-stt | Systran/faster-whisper-large-v3 | PORT | same alias, tier-0 slot |
| `whisper-large-v3` | stt | openai-whisper | whisper-1 | PORT | same alias, tier-1 openai slot |

**Applied:** one `BEGIN…INSERT…ON CONFLICT (alias,upstream_name) DO NOTHING…COMMIT` → `INSERT 0 9`, count 10→19. Strictly additive — no UPDATE/DELETE of any DST row (threat T-19-03). Reload = the resolver's 60s poll (`gateway/internal/models/resolver.go refreshInterval=60s`; NO pg_notify channel for aliases) — gateway logged `model aliases refreshed count=19` within 60s, no restart.

**Real-call verification (via internal Traefik `http://10.10.10.50:80`, fresh converseai key `ifix_sk_****7rsc`):**

| alias | call | result |
|-------|------|--------|
| `gpt-5.4-mini-2026-03-17` | `POST /v1/chat/completions` | **200** (resolved, not 404) |
| `gpt-5.4-mini` | `POST /v1/chat/completions` | **200** |
| `ia-kanban` | `POST /v1/chat/completions` | **200** |
| `captain-embed` | `POST /v1/embeddings` | **200** (1024-dim; routed to tier-0 local bge-m3 — same embed-tier behavior all embed traffic gets on this stack) |
| `whisper-large-v3` | `POST /v1/audio/transcriptions` (espeak WAV) | **200** — text `"Hello, this is a transcription."` |

Pitfall 6 (merged-traffic model string 404) closed for these client-facing aliases.

## Deviations from Plan

**1. [Rule 1 — plan premise wrong] Rerank is NOT gateway-fronted**
- **Found during:** Task 2. Plan text assumed a gateway `/rerank` route + `UPSTREAM_RERANK_URL` env. The gateway has **zero** rerank route/upstream/env (repo grep + prod/worker env inspect confirm). Rerank is a standalone Infinity service consumed directly by the agents RAG at `:7998/v1/rerank`.
- **Resolution:** verified the REAL client-facing route (direct Infinity `/v1/rerank`) instead of a nonexistent gateway route. No gateway env change made (there is none to make).

**2. [Rule 2 — reachability correctness] Published rerank host port 7998 (plan said "no host port")**
- **Why:** the rerank consumer (agents on vps-ifix-vm 10.10.10.30) is OUTSIDE worker-vm's swarm overlay and reaches rerank by host IP:port. An overlay-DNS-only service would be unreachable by that consumer and unverifiable as a client-facing route. Publishing `7998` (swarm ingress mesh) mirrors the current prod topology exactly (`http://10.10.10.50:7998/v1/rerank` ≡ prod `http://10.10.10.20:7998/…`), making cutover a one-line agents URL change.

**3. [Rule 1 — correctness] Dashboard two-DSN split**
- Plan said "DSN → bd_ai_gateway". The dashboard has TWO DSNs. Only `AI_GATEWAY_PG_DSN` (gateway DATA) was repointed to `bd_ai_gateway`; `DASHBOARD_DATABASE_URL` (Better-Auth LOGIN store) was kept at `bd_ai_dashboard_prod` — repointing it would break login (no auth tables in bd_ai_gateway). The no-verify/search_path quirks live on the login DSN and were exercised by the sign-in proof.

**4. [Rule 3 — image pin source] Dashboard digest from GHCR, not the running image**
- The running n8n-ia-vm dashboard image had empty RepoDigests (built locally). Pinned the GHCR `:latest-dev` digest `@sha256:07b7d18…` (the promoted tag), force-pulled on worker-vm.

## Verification Results
- Portainer stacks 40 (`ai-gateway-dashboard`) + 41 (`ai-gateway-rerank`) on endpoint 6, type=1 (swarm), status=1 (active), UI-editable. ✅
- Dashboard 1/1 digest-pinned; landing 307→/login 200; sign-in DB query returns clean 401 (login-time DB connectivity to bd_ai_dashboard_prod proven); owner row present; bd_ai_gateway reachable via the gateway-data DSN. ✅
- Rerank 1/1 digest-pinned; public `/v1/rerank` → 200 ranked; consumer status recorded (healthcheck-only 48h, drop deferred). ✅
- model_aliases 10→19 additive; SRC bd_ai_gateway_prod UNCHANGED at 17 (read-only); 5 ported aliases each real-call-verified 200 (chat×3, embed×1, STT transcription×1). ✅
- **Live PROD gateway (n8n-ia-vm) unaffected:** `ifix-ai-gateway Up 28 hours (healthy)`, public `/health` 200; prod dashboard + rerank still running (parallel). ✅

## Notes for 19-04 / 19-05 / 19-06
- 19-04 migrates the 18 prod tenants+keys into `bd_ai_gateway`; the ported aliases already resolve so merged traffic won't 404 on `gpt-5.4-mini-2026-03-17` / `whisper-large-v3` / etc.
- 19-05 cutover: dashboard host route `Host(ai-dashboard.converse-ai.app)` is live on worker-vm internal Traefik; rerank cutover = repoint the agents consumer `10.10.10.20:7998` → `10.10.10.50:7998` (one line).
- Fresh verification key `ifix_sk_****7rsc` (converseai) minted into bd_ai_gateway — a DEV verify cred, superseded by the 19-04 migration (same pattern as 19-02's creds).
- Reference (no secrets): worker-vm `/root/gw-deploy/{dashboard_compose.yml,rerank_compose.yml,gen_dashboard.py,gen_rerank.py}`; `dashboard.env` is root-600 (secrets, NOT committed).
- `captain-embed` resolves but routes to tier-0 local bge-m3 (1024-dim), not OpenAI 1536 — this is the consolidated stack's embed-tier behavior (local-embed primary), not an alias defect. If a captain-embed client needs 1536-dim OpenAI vectors, revisit at cutover.

## Known Stubs
None — this plan deployed existing images (digest-pinned) as swarm stacks and inserted classified DB rows; no application code or placeholder data introduced.

## Self-Check: PASSED
- `.planning/phases/19-gateway-consolidation-worker-vm/19-03-SUMMARY.md` — FOUND.
- Infra/DB artifacts verified live: Portainer stacks 40 + 41 (endpoint 6, status active), both services 1/1 digest-pinned; dashboard login 401-clean + landing 307/200; rerank public /v1/rerank 200; bd_ai_gateway model_aliases 10→19 with 5 aliases real-call-verified 200; bd_ai_gateway_prod unchanged (17); prod gateway Up 28h healthy + public 200.
- No per-task code commits (infra/DB plan — deliverables are live swarm stacks + additive DB rows, documented above), consistent with 19-01/19-02.
