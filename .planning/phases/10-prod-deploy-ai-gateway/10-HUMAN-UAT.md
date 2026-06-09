# Phase 10 — Live HUMAN-UAT: Prod-Deploy ai-gateway + Cascade Close

**Phase gate.** These 11 scenarios prove the ai-gateway production stack deploys end-to-end on the live `ai-gateway.converse-ai.app` + `ai-dashboard.converse-ai.app` hostnames, then cascade-close 4 prior phase deferrals (Phase 02 SC-5 step 7, Phase 03 SC-1, Phase 04 SC-1+SC-2+SC-4, Phase 05 SC-4/SC-5) that were blocked by the absence of a live prod deploy. **Requires real OpenRouter + OpenAI API spend (≤ $0.10 expected) → autonomous mode cannot satisfy it; an operator must run it. Zero Vast/GPU spend (D-08 reuses keys; no primary pod is provisioned in this UAT).**

**Engine:** Wave 0–3 artifacts (compose stack file + env contract + 5 deploy scripts + edge Traefik route YAML + RUNBOOK + release checklist) drive the operator from preflight → bootstrap-postgres → cut-release tag → first /health 200 over the new hostname → 11 live scenarios → 4 cascade-close commits.

| Header | Value |
|---|---|
| **Phase** | 10 — prod-deploy-ai-gateway |
| **Date** | 2026-05-MM (operator fills in) |
| **Status** | in_progress |
| **Operator** | __________ |
| **Expected wall time** | 2-3 h (incl. Pre-UAT preconditions + RUNBOOK Steps 1-7 + 11 scenarios + 4 cascade-close commits) |
| **Expected $ spend (target)** | ≤ $0.10 (OpenRouter + OpenAI combined; S8 vegeta burst dominates ~$0.025; S1/S4 chat ~$0.001 each; S3 whisper ~$0.006; S5 rate-limit burst ~$0.005) |
| **R2 hard abort criterion (cumulative)** | $2.00 — STOP and triage |
| **R2 hard abort criterion (per-call)** | $0.05 for any single S1/S4/S8 call — STOP and investigate |
| **Vast / GPU spend** | $0 (no primary pod provisioned this UAT; D-08 shared-key invariant means the existing dev pod keys are reused under the prod stack but no new Vast lifecycle is opened) |
| **OPENROUTER_HTTP_REFERER** | `ifix-uat-100-<OPERATOR_INITIALS>` (traceability on OpenRouter dashboard) |

**Pre-flight (operator):**
- Read `gateway/docs/RUNBOOK-DEPLOY.md` Steps 1-7 (first-time bring-up procedure executed BETWEEN Gates B and F below).
- Read `.planning/phases/10-prod-deploy-ai-gateway/10-05-RELEASE-CHECKLIST.md` (operator pre-cut checklist for `cut-release.sh`).
- Creds in `~/.claude/CLAUDE.md`: `CF_API_TOKEN` (Cloudflare DNS); ops-claude `~/.git-credentials` (GitHub PAT for git push); `vps-ifix-vm` + `n8n-ia-vm` SSH aliases (Hetzner Tailscale subnet route).
- SSH aliases: `vps-ifix-vm` (edge Traefik + dashboard host); `n8n-ia-vm` (gateway prod host with colocated Infinity embed tier-0).
- DigitalOcean Postgres console: `doadmin` connection string for `bootstrap-postgres.sh`.
- Sentry org `Ifix`: create new project `ifix-ai-gateway-prod` BEFORE Step 3 of RUNBOOK; copy DSN into `/opt/ai-gateway-prod/.env`.
- All Wave 0–3 plans (10-01..10-05) shipped to `develop` and the v1.0.0 tag will be cut during Pre-UAT Gate C (`cut-release.sh`).

**Budget guard:** OpenRouter Qwen completions ~$0.002 each at default size; Whisper ~$0.006/min audio; embed ~$0.00002/k tokens; S8 vegeta burst 150×$0.0002 ≈ $0.025. Keep prompts tiny (≤ 50 tokens completion). The CLEANUP section is MANDATORY — orphan FORCED_OPEN breakers prevent the prod stack from recovering.

> **REDACT BEFORE PASTING:** Replace all `Authorization: Bearer sk-...` strings with `Authorization: Bearer <REDACTED>`. Replace all real OpenAI/OpenRouter/Cloudflare tokens with `<REDACTED-OPENAI-KEY>` / `<REDACTED-OPENROUTER-KEY>` / `<REDACTED-CF-TOKEN>`. NEVER paste a real key into this markdown. UAT evidence is committed to git — leaked tokens require rotation.

---

## Pre-UAT Preconditions and Operator Safeguards (R2 — BLOCKING)

> **STOP. Do NOT proceed to S1 until ALL 6 gates below show PASS.** This is the FIRST checkpoint of Plan 10-06 — the executor pauses here. Each gate has its own sign-off line. If any gate FAILs, the operator addresses the underlying issue (apply env var, run migration, etc.) and re-runs that gate before continuing.

Between Gate B and Gate F, the operator executes `gateway/docs/RUNBOOK-DEPLOY.md` Steps 1-7 (first-time bring-up): create Sentry project; populate `/opt/ai-gateway-prod/.env` from `.env.prod.example`; `docker compose up -d` against `gateway/docker-compose.prod.yml`; smoke `/health` on the internal address; flip DNS via Gate E; first https probe via Gate F.

### Gate A — Egress IP + capacity (HARD GATE) — ~3 min

`scripts/deploy/preflight.sh` exits 0. The script asserts: egress IP is `162.55.92.154`; free RAM ≥ 4 GB; disk on `/` ≥ 20 GB free; `intra` Docker network attachable; internal Traefik service discoverable.

```bash
ssh n8n-ia-vm 'cd /opt/ai-gateway-prod 2>/dev/null || mkdir -p /opt/ai-gateway-prod && cd /opt/ai-gateway-prod; bash <(curl -fsSL file:///dev/stdin) < scripts/deploy/preflight.sh' \
  < scripts/deploy/preflight.sh
# OR (when scripts are rsync'd locally on n8n-ia-vm):
ssh n8n-ia-vm 'cd /opt/ai-gateway-prod && bash scripts/deploy/preflight.sh'
# Expected: script exits 0; trailing line "preflight OK"; prints egress IP + free -h + df -h / + intra-net status + internal-Traefik-discovery probe PASS
```

Operator pastes preflight output's last 20 lines into the Evidence box (Pitfall 6 — VM 101 capacity gate: if disk > 80% used OR free mem < 4 G, scale VM 101 vCPU/RAM/disk in Proxmox BEFORE proceeding; preflight will FAIL on those thresholds).

- **Sign-off Gate A:** [ ] PASS [ ] FAIL · **Evidence (preflight last 20 lines, egress IP, free -h, df -h /):** ______ · **Operator:** __________ · **Date:** __________

---

### Gate B — DO Postgres databases bootstrapped (HARD GATE) — ~5 min

Both `bd_ai_gateway_prod` and `bd_ai_dashboard_prod` must exist on the DO cluster `db-grupoifix-do-user-7520351-0.j.db.ondigitalocean.com:25060`. Pitfall 2 — schema collision dev↔prod: the script creates SEPARATE databases (not just schemas) so production goose migrations run against an empty DB. Pitfall 10 — DO trusted-source: confirm IP `162.55.92.154` is in the Trusted Sources allowlist BEFORE running the script.

```bash
# 1) doadmin DSN from DO console (Cluster → Connection Details → doadmin user)
export DO_ADMIN_DSN='postgresql://doadmin:<REDACTED>@db-grupoifix-do-user-7520351-0.j.db.ondigitalocean.com:25060/defaultdb?sslmode=require'

# 2) Run bootstrap (idempotent — re-run safely)
bash scripts/deploy/bootstrap-postgres.sh
# Expected: script exits 0; both databases listed; per-database "SELECT current_database()" probe returns the DB name.

# 3) Verify both DBs exist + are empty
psql "$DO_ADMIN_DSN" -tAc "SELECT datname FROM pg_database WHERE datname LIKE 'bd_ai_%_prod' ORDER BY datname;"
# Expected:
#   bd_ai_dashboard_prod
#   bd_ai_gateway_prod
psql "${DO_ADMIN_DSN/defaultdb/bd_ai_gateway_prod}" -tAc "SELECT current_database();"
# Expected: bd_ai_gateway_prod
psql "${DO_ADMIN_DSN/defaultdb/bd_ai_dashboard_prod}" -tAc "SELECT current_database();"
# Expected: bd_ai_dashboard_prod
```

- **Sign-off Gate B:** [ ] PASS [ ] FAIL · **Evidence (psql output for both DBs, bootstrap-postgres.sh last line):** ______ · **Operator:** __________ · **Date:** __________

---

### Gate C — GHA :v1.0.0 image green + cut-release tag (HARD GATE) — ~10 min

The `:v1.0.0` tag must be cut on the `main` branch (after a guarded develop→main merge via `cut-release.sh`) and the resulting GitHub Actions workflow must produce a green `ghcr.io/ifixtelecom/ifix-ai-gateway:v1.0.0` image. Sub-step: same for `ifix-ai-dashboard:v1.0.0`.

```bash
# 1) Pre-cut checklist (read once)
cat .planning/phases/10-prod-deploy-ai-gateway/10-05-RELEASE-CHECKLIST.md

# 2) Cut the release (guarded develop → main merge + v1.0.0 tag push)
bash scripts/deploy/cut-release.sh v1.0.0
# Expected: script confirms develop is green on CI, fast-forwards main, tags v1.0.0, pushes; exits 0.

# 3) Confirm Actions green for the v1.0.0 build (poll for ≤8 min)
gh run list --limit 3 --workflow build-gateway.yml --branch main --json conclusion,status,headBranch,event,createdAt
# Expected (top row): {"conclusion":"success","status":"completed","event":"push","headBranch":"main",...}
gh run list --limit 3 --workflow build-dashboard.yml --branch main --json conclusion,status,headBranch,event,createdAt
# Expected (top row): same shape, conclusion=success

# 4) Pull both images on the operator workstation (validates GHCR push completed)
docker pull ghcr.io/ifixtelecom/ifix-ai-gateway:v1.0.0
docker pull ghcr.io/ifixtelecom/ifix-ai-dashboard:v1.0.0
# Expected: both pulls complete with "Status: Downloaded newer image" or "Status: Image is up to date"
```

- **Sign-off Gate C:** [ ] PASS [ ] FAIL · **Evidence (cut-release.sh tail + gh run list top row for both workflows + docker pull final status):** ______ · **Operator:** __________ · **Date:** __________

---

### Gate D — Edge Traefik route loaded (HARD GATE) — ~5 min

Copy `artifacts/ai-gateway-prod.yml` to the edge Traefik dynamic-config directory on `vps-ifix-vm`; tail the edge Traefik logs for the `router added` line confirming the YAML parsed and the router activated. Pitfall 3 reminder: DNS is NOT yet flipped — the new routers point at an internal upstream (`http://ai-gateway-prod.intra:8080` / `http://ai-dashboard-prod.intra:3000`); external traffic still hits the dev stack until Gate E runs.

Pitfall 9 (T-10-06-09): YAML parse failure on Traefik file-provider hot-reload. Validate YAML BEFORE the scp; the edge Traefik watch picks up only well-formed YAML.

```bash
# 1) Local YAML parse pre-flight
python3 -c "import yaml; yaml.safe_load(open('.planning/phases/10-prod-deploy-ai-gateway/artifacts/ai-gateway-prod.yml'))"
# Expected: no output (success); on ParseError, fix YAML before proceeding

# 2) Copy to edge Traefik dynamic dir
scp .planning/phases/10-prod-deploy-ai-gateway/artifacts/ai-gateway-prod.yml \
  vps-ifix-vm:/home/pedro/projetos/pedro/infra/traefik-dynamic/

# 3) Tail edge Traefik logs for "router added" / "configuration loaded" on the new routers
ssh vps-ifix-vm 'docker logs -f --tail 0 $(docker ps -q -f name=traefik) 2>&1 \
  | grep -iE "ai-gateway-prod|ai-dashboard-prod|configuration loaded" | head -10'
# Expected (within 5s): "router added router=ai-gateway-prod@file" + "router added router=ai-dashboard-prod@file"
# (Ctrl-C after both lines appear)

# 4) Sanity-check the routers are visible via Traefik API (if exposed) OR via the dynamic file
ssh vps-ifix-vm 'cat /home/pedro/projetos/pedro/infra/traefik-dynamic/ai-gateway-prod.yml | head -20'
```

- **Sign-off Gate D:** [ ] PASS [ ] FAIL · **Evidence (python yaml.safe_load exit code + edge Traefik "router added" log lines for both routers):** ______ · **Operator:** __________ · **Date:** __________

---

### Gate E — DNS resolves (HARD GATE) — ~3 min

`scripts/deploy/cf-dns-create.sh` creates `A` records `ai-gateway.converse-ai.app` and `ai-dashboard.converse-ai.app` both pointing at the edge IP `162.55.92.154` with Cloudflare proxied=OFF, TTL 300. Pitfall 11 — CF token scope: token must have `Zone:Read` + `DNS:Edit` for `converse-ai.app` (zone ID `0e779b74b86957bdb628d646dbf33978`).

```bash
# 1) Apply records (idempotent — script checks for existing records first)
export CF_API_TOKEN='<REDACTED-CF-TOKEN>'   # from ~/.claude/CLAUDE.md Cloudflare API Token block
bash scripts/deploy/cf-dns-create.sh
# Expected: script exits 0; final stanza "Created/Updated record ai-gateway.converse-ai.app → 162.55.92.154" + same for dashboard

# 2) Resolve from public DNS (1.1.1.1) — wait up to 60s for TTL propagation
dig +short ai-gateway.converse-ai.app @1.1.1.1
# Expected: 162.55.92.154
dig +short ai-dashboard.converse-ai.app @1.1.1.1
# Expected: 162.55.92.154

# 3) Cloudflare dashboard cross-check (optional but recommended)
# Visit https://dash.cloudflare.com → converse-ai.app → DNS → confirm both records present, proxied=OFF, TTL 300
```

- **Sign-off Gate E:** [ ] PASS [ ] FAIL · **Evidence (dig output for both hostnames + cf-dns-create.sh last line):** ______ · **Operator:** __________ · **Date:** __________

---

### Gate F — TLS cert issued (HARD GATE) — ~5 min

First HTTPS probe over the new hostname forces edge Traefik to obtain a Let's Encrypt certificate via ACME (HTTP-01 challenge). Pitfall 4: ACME can take 30-90 seconds; if Gate F is hit BEFORE the cert is issued, the curl returns `SSL_ERROR_BAD_CERT_DOMAIN` or `503`. Pitfall 5: stale-cert mid-UAT — assert BOTH `HTTP/2 200` AND `acme.json` contains both hostnames before signing.

```bash
# 1) First HTTPS probe — gateway
curl -sS -I https://ai-gateway.converse-ai.app/health | head -1
# Expected: HTTP/2 200    (after ACME completes; may take 30-90s on first probe — retry 3× with 20s sleep if needed)

# 2) First HTTPS probe — dashboard
curl -sS -I https://ai-dashboard.converse-ai.app/ | head -1
# Expected: HTTP/2 200 (dashboard root) or HTTP/2 302 (redirect to login) — both acceptable

# 3) Verify acme.json contains both hostnames (Pitfall 5 — stale-cert mid-UAT)
ssh vps-ifix-vm "docker exec infra-traefik-1 cat /letsencrypt/acme.json \
  | jq '.letsencrypt.Certificates[].domain.main' -r" \
  | grep -E 'ai-(gateway|dashboard)\.converse-ai\.app'
# Expected: BOTH "ai-gateway.converse-ai.app" AND "ai-dashboard.converse-ai.app" present
```

- **Sign-off Gate F:** [ ] PASS [ ] FAIL · **Evidence (curl -I HTTP/2 line for both hostnames + acme.json jq output showing both):** ______ · **Operator:** __________ · **Date:** __________

---

> **GATE SUMMARY — operator types "all gates passed" to acknowledge before proceeding to S1.**
>
> **Master sign-off line:** Gate A [ ] · Gate B [ ] · Gate C [ ] · Gate D [ ] · Gate E [ ] · Gate F [ ] — **ALL PASS:** [ ] **Operator:** ______ **Date:** ______

---

## Scenario S1 — Chat E2E under prod hostname (~3 min, ~$0.001)

**Goal:** Send `POST https://ai-gateway.converse-ai.app/v1/chat/completions {"model":"qwen"}`. Receive HTTP 200 with real LLM completion routed through OpenRouter tier-1 (primary pod is asleep this UAT; D-08 shared-key invariant means OpenRouter answers).

**Cascades:** Phase 02 SC-5 step 7 — original 02-UAT-2026-05-23.md step 7 returned 503 because primary FSM=asleep + OpenRouter env vars absent. With Phase 06.9 + Phase 10 prod stack, this MUST return HTTP 200.

### Setup

```bash
# Mint a fresh tenant key for converseai during RUNBOOK Step 7 — or reuse the key from there
# (provision-tenants.sh --mint-keys provisions all 6 tenants and prints each Bearer key)
export CONVERSEAI_KEY='ifix_sk_<REDACTED>'   # from provision-tenants.sh output
```

### Probe

```bash
curl -sS -X POST https://ai-gateway.converse-ai.app/v1/chat/completions \
  -H "Authorization: Bearer ${CONVERSEAI_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen","messages":[{"role":"user","content":"PING"}],"max_tokens":10}' \
  | jq '{model:.model, provider:(.provider // "n/a"), content:.choices[0].message.content, finish:.choices[0].finish_reason, rid:.id, usage:.usage}'
# Expected: HTTP 200 + non-empty content + model field shows a slug ending in (e.g.) "deepseek/deepseek-v4-flash-20260423" (OpenRouter provider chosen at request time, e.g. Novita/AtlasCloud/SiliconFlow rotating)
```

### Expected

- HTTP status: 200
- `choices[0].message.content`: non-empty string
- `model`: a real OpenRouter slug (not the literal `qwen`) — confirms director rewrite worked end-to-end
- `id`: starts with `gen-...` (OpenRouter request ID) — also returned as `X-Request-ID` header
- `usage.total_tokens`: > 0 (≤ 50 — keep prompts tiny per budget guard)

### Common failure modes

- **HTTP 503 `no fallback configured`:** primary FSM not asleep AND breaker not OPEN → manually `gatewayctl breaker force-open --upstream local-llm --reason 10_uat_s1` first; OR confirm `.env` has `UPSTREAM_LLM_OPENROUTER_AUTH_BEARER` populated.
- **HTTP 404 + HTML body:** Phase 06.9 regression — model-rewrite not active. Inspect `gatewayctl model-alias list` for the `(qwen, openrouter-chat, ...)` row.
- **HTTP 401/402 from OpenRouter:** token rotated mid-UAT — re-confirm `UPSTREAM_LLM_OPENROUTER_AUTH_BEARER` in `/opt/ai-gateway-prod/.env`.

### Evidence box (REDACTED)

```
HTTP status: ____
X-Request-ID: ____
Response (redacted):
{"model":"____","provider":"____","content":"____","finish":"stop","rid":"gen-<REDACTED>","usage":{"total_tokens":____}}
Cost estimate: $____ (total_tokens × OpenRouter pricing for chosen provider)
```

- **Sign-off S1:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Date:** __________ · **Evidence (REDACTED):** ______

---

## Scenario S2 — Tier-0 embed via colocated Infinity (~1 min, ~$0.000)

**Goal:** Send `POST https://ai-gateway.converse-ai.app/v1/embeddings {"model":"bge-m3","input":[...]}`. Receive HTTP 200 with `data[*].embedding` arrays of length 1024 from the colocated Infinity tier-0 on `n8n-ia-vm` (NOT OpenAI tier-1). This verifies `UPSTREAM_EMBED_URL=http://10.10.10.20:7997` is wired correctly in the prod compose file.

**Cascades:** none (verifies wiring only — Phase 09 already validated embed under prod-shaped routing). Important sanity check that the colocated Infinity path actually serves under the prod hostname.

### Setup

No special setup — Infinity has been live on `n8n-ia-vm:7997` since Phase 09. The prod gateway's `.env` should set `UPSTREAM_EMBED_URL=http://10.10.10.20:7997` so the dispatcher picks `local-embed` over `openai-embed`.

### Probe

```bash
curl -sS -X POST https://ai-gateway.converse-ai.app/v1/embeddings \
  -H "Authorization: Bearer ${CONVERSEAI_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"bge-m3","input":["ifix telecom suporte","segunda via boleto"]}' \
  | jq '{model:.model, n:(.data|length), dim0:(.data[0].embedding|length), dim1:(.data[1].embedding|length)}'
# Expected: {"model":"bge-m3","n":2,"dim0":1024,"dim1":1024}
```

### Expected

- HTTP status: 200
- `model`: `bge-m3` (NOT `text-embedding-3-small` — confirms LOCAL tier-0 served, not OpenAI tier-1)
- `n`: 2
- `dim0` + `dim1`: both 1024 (BGE-M3 parity invariant)

### Common failure modes

- **`model == "text-embedding-3-small"`:** Infinity is unreachable from the prod gateway container → `local-embed` breaker tripped → tier-1 took over. Inspect: `ssh n8n-ia-vm 'curl -sS http://10.10.10.20:7997/health'` from the gateway container's network namespace.
- **`dim0 == 1536`:** dispatch went to OpenAI WITHOUT the `dimensions=1024` parameter — Phase 06.9 regression on the embed director.

### Evidence box

```
HTTP status: ____
jq summary: {"model":"____","n":____,"dim0":____,"dim1":____}
```

- **Sign-off S2:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Date:** __________ · **Evidence:** ______

---

## Scenario S3 — Whisper STT tier-1 (~2 min, ~$0.006)

**Goal:** Force-open `local-stt` breaker. Send `POST https://ai-gateway.converse-ai.app/v1/audio/transcriptions` with a small WAV + `model=whisper`. Receive HTTP 200 with `{"text":"..."}` from OpenAI's `whisper-1` (model rewritten in multipart form data without corrupting audio bytes).

**Cascades:** none (verifies wiring only — Phase 06.9 S2 validated under dev).

### Setup

```bash
# Copy probe.wav fixture to operator workstation (already in repo at gateway/internal/upstreams/testdata/)
cp gateway/internal/upstreams/testdata/probe.wav /tmp/probe.wav
file /tmp/probe.wav
# Expected: RIFF (little-endian) data, WAVE audio, ...

# Force-open the local-stt breaker
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-open \
  --upstream local-stt --ttl 5m --reason "10_uat_s3"'
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker list' | grep local-stt
# Expected: local-stt row with state=FORCED_OPEN + TTL countdown
```

### Probe

```bash
curl -sS -X POST https://ai-gateway.converse-ai.app/v1/audio/transcriptions \
  -H "Authorization: Bearer ${CONVERSEAI_KEY}" \
  -F 'file=@/tmp/probe.wav' \
  -F 'model=whisper' \
  | jq
# Expected: {"text":"..."}    (whisper transcription of probe.wav)
```

### Expected

- HTTP status: 200
- Body shape `{"text":"<transcription>"}` (probe.wav contents)

### Common failure modes

- **HTTP 400 from OpenAI:** `model=whisper` not rewritten to `whisper-1` on multipart → Phase 06.9 regression on whisper director.
- **HTTP 401/404 from OpenAI:** OpenAI key in `.env` rotated/invalid — refresh `UPSTREAM_STT_OPENAI_AUTH_BEARER`.

### Cleanup

```bash
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-close --upstream local-stt'
```

### Evidence box

```
HTTP status: ____
Response: {"text":"____"}
```

- **Sign-off S3:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Date:** __________ · **Evidence:** ______

---

## Scenario S4 — Force-open primary breaker → tier-1 chat 200 (~3 min, ~$0.001)

**Goal:** With `local-llm` breaker FORCED_OPEN, send 2 consecutive chat requests. Both must return HTTP 200, and audit_log MUST show `upstream=openrouter-chat` for both request_ids.

**Cascades:** Phase 03 SC-1 — original 03-VERIFICATION.md SC-1 was marked PASS via Phase 06.9 S4 against the DEV URL; this re-verifies the same chain under the PROD hostname.

### Setup

```bash
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-open \
  --upstream local-llm --ttl 5m --reason "10_uat_s4"'
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker list' | grep local-llm
# Expected: local-llm row state=FORCED_OPEN
```

### Probe

```bash
RIDS=()
for i in 1 2; do
  RESP=$(curl -sS -i -X POST https://ai-gateway.converse-ai.app/v1/chat/completions \
    -H "Authorization: Bearer ${CONVERSEAI_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"model":"qwen","messages":[{"role":"user","content":"S4 probe '"$i"'"}],"max_tokens":5}')
  RID=$(echo "$RESP" | grep -i '^x-request-id:' | awk '{print $2}' | tr -d '\r')
  CODE=$(echo "$RESP" | grep -E '^HTTP/' | tail -1 | awk '{print $2}')
  echo "Probe $i: HTTP $CODE  X-Request-ID=$RID"
  RIDS+=("$RID")
done
echo "Request IDs: ${RIDS[*]}"

# Confirm audit_log shows upstream=openrouter-chat for both
psql "$AI_GATEWAY_PG_DSN" -tAc \
  "SELECT request_id, upstream FROM ai_gateway.audit_log WHERE request_id IN ('${RIDS[0]}','${RIDS[1]}')"
# Expected: 2 rows, both upstream=openrouter-chat
```

### Expected

- Both probes: HTTP 200
- psql query returns 2 rows, both with `upstream=openrouter-chat`

### Cleanup

```bash
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-close --upstream local-llm'
```

### Evidence box

```
Probe 1: HTTP ____  X-Request-ID=____
Probe 2: HTTP ____  X-Request-ID=____
audit_log rows:
  ____|openrouter-chat
  ____|openrouter-chat
```

- **Sign-off S4:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Date:** __________ · **Evidence:** ______

---

## Scenario S5 — Rate-limit burst (~3 min, ~$0.005)

**Goal:** With a tenant configured `rps=5`, send 10 parallel chat requests. Observe 3-5 HTTP 429 responses with `Retry-After: 1` + `X-RateLimit-Limit-Requests: 5` + decrementing `X-RateLimit-Remaining-Requests` chain; Prometheus `gateway_rate_limit_rejected_total{tenant="uat10-test",window="rps"}` increments accordingly.

**Cascades:** Phase 04 SC-1 — original 04-VERIFICATION.md SC-1 was LIVE PASS on 2026-05-23 against the DEV URL; this re-verifies under the PROD hostname.

### Setup

```bash
# 1) Create test tenant (if not already done via provision-tenants.sh)
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl tenant create --name "uat10-test" --slug "uat10-test"'
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl tenant set-quota uat10-test --rps 5'
UAT10_KEY=$(ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl key create --tenant uat10-test --data-class normal' | grep -oE 'ifix_sk_[a-z0-9]+')
echo "UAT10_KEY=$UAT10_KEY (record this — printed once)"
```

### Probe

```bash
# 10 parallel curls; collect HTTP codes + Retry-After + X-RateLimit-* headers
for i in $(seq 1 10); do
  (curl -sS -o /dev/null -i -X POST https://ai-gateway.converse-ai.app/v1/chat/completions \
    -H "Authorization: Bearer ${UAT10_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"model":"qwen","messages":[{"role":"user","content":"burst probe '"$i"'"}],"max_tokens":3}' \
    | grep -iE '^(HTTP/|x-ratelimit-|retry-after)' ) &
done | sort -u
wait

# Verify Prometheus counter increased
curl -sS https://ai-gateway.converse-ai.app/metrics \
  | grep 'gateway_rate_limit_rejected_total{tenant="uat10-test"'
# Expected: a counter ≥ 3 with window="rps"
```

### Expected

- 5 HTTP 200 + 3-5 HTTP 429
- 429 responses carry `Retry-After: 1` + `X-RateLimit-Limit-Requests: 5` + `X-RateLimit-Remaining-Requests` decreasing 4→3→2→1→0
- Prometheus `gateway_rate_limit_rejected_total{tenant="uat10-test",window="rps"}` increment matches the 429 count

### Evidence box

```
HTTP code counts: 200=____ 429=____
Sample 429 headers: Retry-After=____ X-RateLimit-Limit-Requests=____ X-RateLimit-Remaining-Requests=____,____,____,____
Prometheus counter: gateway_rate_limit_rejected_total{tenant="uat10-test",window="rps"} = ____
```

- **Sign-off S5:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Date:** __________ · **Evidence:** ______

---

## Scenario S6 — billing_events row inserted (~1 min, $0)

**Goal:** Query `ai_gateway.billing_events` and confirm at least one row per S1/S2/S3/S4/S5 chat request landed within the last 15 minutes.

**Cascades:** Phase 04 SC-2 — original SC-2 deferred billing_events row inspection (MCP postgres-grupo-ifix rejected). This is the first live validation under the prod DB.

### Setup

No setup — S1/S2/S3/S4/S5 already generated billing rows.

### Probe

```bash
psql "$AI_GATEWAY_PG_DSN" -tAc \
  "SELECT COUNT(*), MAX(created_at) FROM ai_gateway.billing_events WHERE created_at > now() - interval '15 minutes'"
# Expected: count > 0; MAX(created_at) is within the last 15 min

# Inspect a sample row (last one)
psql "$AI_GATEWAY_PG_DSN" -tAc \
  "SELECT request_id, tenant_id, upstream, tokens_input, tokens_output, cost_usd, created_at \
   FROM ai_gateway.billing_events ORDER BY created_at DESC LIMIT 1"
# Expected: 1 row with a real upstream value (local-llm / openrouter-chat / local-embed / openai-whisper) + non-zero tokens
```

### Expected

- COUNT(*) > 0
- MAX(created_at) within last 15 min
- Sample row has non-zero `tokens_input` + non-zero `cost_usd`

### Evidence box

```
Count: ____  Latest: ____
Sample: rid=____ tenant=____ upstream=____ tokens_in=____ tokens_out=____ cost=$____
```

- **Sign-off S6:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Date:** __________ · **Evidence:** ______

---

## Scenario S7 — Peak schedule routing (~3 min, ~$0.001)

**Goal:** Configure tenant `uat10-test` in peak mode with off-hours window covering current time; send a chat request; observe gateway logs show `module=SCHEDULE upstream=openrouter-chat decision=off_hours_external`; response served by OpenRouter tier-1.

**Cascades:** Phase 04 SC-4 — original SC-4 LIVE PASS on 2026-05-23 against DEV URL; this re-verifies under PROD hostname.

### Setup

```bash
# Pick a 1-hour window covering NOW (operator computes — example assumes 22:00-23:00 BRT)
NOW_HOUR_BRT=$(TZ=America/Sao_Paulo date +%H)
NEXT_HOUR_BRT=$((10#$NOW_HOUR_BRT + 1))
WINDOW="${NOW_HOUR_BRT}-${NEXT_HOUR_BRT}"   # e.g. "22-23"

ssh n8n-ia-vm "docker exec ifix-ai-gateway /gatewayctl tenant set-mode uat10-test peak --window ${WINDOW}"
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl tenant show uat10-test' | grep -E 'mode|window'
# Expected: mode=peak window=$WINDOW
```

### Probe

```bash
# Capture request_id from response
RID=$(curl -sS -i -X POST https://ai-gateway.converse-ai.app/v1/chat/completions \
  -H "Authorization: Bearer ${UAT10_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen","messages":[{"role":"user","content":"S7 schedule"}],"max_tokens":5}' \
  | grep -i '^x-request-id:' | awk '{print $2}' | tr -d '\r')
echo "RID=$RID"

# Find SCHEDULE decision log for this request_id (tail logs and grep)
ssh n8n-ia-vm "docker logs ai-gateway-prod --tail 500 2>&1 \
  | grep \"$RID\" \
  | grep -iE 'module=SCHEDULE|module=DISPATCHER|decision=off_hours'"
# Expected: at least one line with module=SCHEDULE + upstream=openrouter-chat + decision=off_hours_external

# Also check Prometheus counter
curl -sS https://ai-gateway.converse-ai.app/metrics \
  | grep 'gateway_schedule_routing_total{decision="off_hours_external"'
# Expected: counter ≥ 1
```

### Expected

- HTTP 200 + LLM completion from OpenRouter
- Gateway log shows `module=SCHEDULE decision=off_hours_external upstream=openrouter-chat` for the RID
- Prometheus `gateway_schedule_routing_total{decision="off_hours_external"}` ≥ 1

### Cleanup

```bash
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl tenant set-mode uat10-test 24/7'
```

### Evidence box

```
RID: ____
Log line: module=SCHEDULE upstream=____ decision=____
Prometheus counter: gateway_schedule_routing_total{decision="off_hours_external"} = ____
```

- **Sign-off S7:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Date:** __________ · **Evidence:** ______

---

## Scenario S8 — vegeta burst 5 RPS × 30s (~1 min, ~$0.025)

**Goal:** With `local-llm` breaker FORCED_OPEN, run `vegeta attack -duration=30s -rate=5` → 150 requests, all served by OpenRouter tier-1. Expected ≥ 99% HTTP 200 (149/150 acceptable per 06.9 S6 precedent — 1 vegeta-client-side timeout tolerated).

**Cascades:** Phase 05 SC-4/SC-5 — Phase 06.9 S6 closed SC-1 against DEV; this verifies SC-4 (anti-starvation under shed) + SC-5 (DCGM scrape evidence) implicitly under PROD by exercising the same load shape against the prod hostname.

### Setup

```bash
# 1) Force-open the breaker (same as S4)
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-open \
  --upstream local-llm --ttl 5m --reason "10_uat_s8"'

# 2) Prepare vegeta target + body
cat > /tmp/body.json <<EOF
{"model":"qwen","messages":[{"role":"user","content":"PING"}],"max_tokens":5}
EOF

cat > /tmp/targets.txt <<EOF
POST https://ai-gateway.converse-ai.app/v1/chat/completions
Authorization: Bearer ${CONVERSEAI_KEY}
Content-Type: application/json
@/tmp/body.json
EOF

# 3) Optional — confirm vegeta installed
vegeta -version
# If missing: go install github.com/tsenart/vegeta/v12@latest
```

### Probe

```bash
vegeta attack -duration=30s -rate=5 -targets=/tmp/targets.txt \
  | vegeta report -type='hist[0,500ms,1s,2s,5s]'
# Expected:
#   Requests      [total, rate, throughput]  150, 5.03, ~4.95
#   Success       [ratio]                    ≥ 99% (149/150 or 150/150)
#   Status Codes  [code:count]               200:149 + 0:1  (client-side timeout)  OR  200:150
#   Bucket histogram showing most calls < 2s
```

### Expected

- 150 total requests
- ≥ 99% success ratio (149/150 with 1 vegeta-client-side timeout acceptable per 06.9 S6 precedent — 150/150 ideal)
- ALL non-timeout responses HTTP 200
- Status Codes show 200:149 or 200:150; 0 upstream 502s

### Cleanup

```bash
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-close --upstream local-llm'
```

### Evidence box

```
Requests total: ____
Success ratio: ____% (target ≥99%)
Status codes: 200=____ other=____
Latency P95: ____ms  P99: ____ms
```

- **Sign-off S8:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Date:** __________ · **Evidence:** ______

---

## Scenario S9 — Per-tenant golden-path smoke (~10 min, ~$0.005)

**Goal:** Run the per-tenant smoke scripts under `scripts/integration-smoke/` for ALL 6 tenants and confirm each script's JSON report shows `report.summary.passed == report.summary.total`. This is the primary evidence for INT-06.

**Cascades:** none directly (S10 covers the rollback drill half of INT-06). Captures INT-06 per-tenant evidence under the prod hostname.

> **Note on INT-06 split:** S9 validates the per-tenant golden path; the rollback drill required by INT-06 is exercised in **S10** (separate scenario). Do not skip S10 just because S9 passed.

### Setup

All 6 tenants must have been provisioned during RUNBOOK Step 7 via `scripts/integration-smoke/provision-tenants.sh` (--mint-keys mode). Operator captures each tenant's Bearer key (printed once at provisioning) into environment variables:

```bash
export CONVERSEAI_KEY='ifix_sk_<REDACTED>'
export CHAT_IFIX_KEY='ifix_sk_<REDACTED>'
export TELEFONIA_KEY='ifix_sk_<REDACTED>'        # sensitive — used by smoke-sensitive-failover.py
export COBRANCAS_KEY='ifix_sk_<REDACTED>'        # sensitive — used by smoke-sensitive-failover.py
export CAMPANHAS_KEY='ifix_sk_<REDACTED>'
export VOICE_API_KEY='ifix_sk_<REDACTED>'

export GATEWAY_BASE_URL='https://ai-gateway.converse-ai.app'
```

### Probe — 3 smoke scripts × 6 tenants

```bash
# 1) converseai (normal data_class, chat-heavy)
python3 scripts/integration-smoke/smoke-converseai.py \
  --base-url "$GATEWAY_BASE_URL" \
  --api-key "$CONVERSEAI_KEY" \
  --report /tmp/smoke-converseai.json
jq '.summary' /tmp/smoke-converseai.json
# Expected: {"total":N,"passed":N,"failed":0}

# 2) chat-ifix (normal, chat + embed)
python3 scripts/integration-smoke/smoke-chat-ifix.py \
  --base-url "$GATEWAY_BASE_URL" \
  --api-key "$CHAT_IFIX_KEY" \
  --report /tmp/smoke-chat-ifix.json
jq '.summary' /tmp/smoke-chat-ifix.json

# 3) telefonia (SENSITIVE — must get 503 sensitive-block envelope when routing to external)
python3 scripts/integration-smoke/smoke-sensitive-failover.py \
  --base-url "$GATEWAY_BASE_URL" \
  --api-key "$TELEFONIA_KEY" \
  --tenant-slug telefonia \
  --report /tmp/smoke-telefonia.json
jq '.summary' /tmp/smoke-telefonia.json

# 4) cobrancas (SENSITIVE — same shape as telefonia)
python3 scripts/integration-smoke/smoke-sensitive-failover.py \
  --base-url "$GATEWAY_BASE_URL" \
  --api-key "$COBRANCAS_KEY" \
  --tenant-slug cobrancas \
  --report /tmp/smoke-cobrancas.json
jq '.summary' /tmp/smoke-cobrancas.json

# 5) campanhas (normal, chat + embed)
python3 scripts/integration-smoke/smoke-chat-ifix.py \
  --base-url "$GATEWAY_BASE_URL" \
  --api-key "$CAMPANHAS_KEY" \
  --tenant-override campanhas \
  --report /tmp/smoke-campanhas.json
jq '.summary' /tmp/smoke-campanhas.json

# 6) voice-api (normal, STT-heavy)
python3 scripts/integration-smoke/smoke-converseai.py \
  --base-url "$GATEWAY_BASE_URL" \
  --api-key "$VOICE_API_KEY" \
  --tenant-override voice-api \
  --report /tmp/smoke-voice-api.json
jq '.summary' /tmp/smoke-voice-api.json
```

### Expected

For each of the 6 tenants:
- `report.summary.passed == report.summary.total` (all probes PASS)
- `report.summary.failed == 0`
- For sensitive tenants (telefonia, cobrancas): the smoke script's "external route rejection" probe MUST return 503 `sensitive_block` envelope — confirming RES-08 enforcement still holds under prod URL

### Evidence box

```
converseai:  total=____ passed=____ failed=____
chat-ifix:   total=____ passed=____ failed=____
telefonia:   total=____ passed=____ failed=____  (sensitive — verify sensitive_block probe)
cobrancas:   total=____ passed=____ failed=____  (sensitive — verify sensitive_block probe)
campanhas:   total=____ passed=____ failed=____
voice-api:   total=____ passed=____ failed=____
```

- **Sign-off S9:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Date:** __________ · **Evidence:** ______

---

## Scenario S10 — Rollback drill timed (~10 min, $0)

**Goal:** Swap the running gateway image from `:v1.0.0` to a previous tag (`:v0.X.Y` if it exists, otherwise `:main-<previous-sha>` per RUNBOOK-DEPLOY.md §Rollback §3), verify `/health` returns 200, then swap back to `:v1.0.0`. EACH direction's wall time MUST be < 5 min.

**Cascades:** secondary evidence for INT-06 (paired with S9). The rollback drill is the documented production-readiness gate for the gateway image roll-forward / roll-back cycle.

> **Note on rollback target:** if `v1.0.0` is the first release tag (no `v0.X.Y` exists), use `:main-<previous-sha>` as the rollback target per RUNBOOK-DEPLOY.md §Rollback §3. Inspect `gh container ls -R IfixTelecom/gpu-ifix | head` or the GHCR UI to find a previous tag.

### Setup

```bash
# 1) Find rollback target
PREV_TAG=$(gh container ls -R IfixTelecom/gpu-ifix 2>/dev/null | grep -oE 'main-[a-f0-9]{7,12}' | head -2 | tail -1)
# OR if no previous main-* tag is available:
PREV_TAG='main-<previous-sha>'   # operator inspects GHCR UI
echo "Rollback target: $PREV_TAG"

# 2) Confirm image exists in GHCR
docker pull ghcr.io/ifixtelecom/ifix-ai-gateway:${PREV_TAG}
```

### Probe — rollback forward + back, with `time`

```bash
# 1) Rollback from v1.0.0 → PREV_TAG (timed)
time ssh n8n-ia-vm "sed -i 's/:v1.0.0/:${PREV_TAG}/' /opt/ai-gateway-prod/docker-compose.yml \
  && cd /opt/ai-gateway-prod \
  && docker compose pull \
  && docker compose up -d --force-recreate ifix-ai-gateway"
# Expected: real (wall time) < 5 min

# 2) Verify rollback healthy
curl -sS https://ai-gateway.converse-ai.app/health
# Expected: HTTP 200 + {"status":"ok"} (or equivalent)

# 3) Roll forward back to v1.0.0 (timed)
time ssh n8n-ia-vm "sed -i 's/:${PREV_TAG}/:v1.0.0/' /opt/ai-gateway-prod/docker-compose.yml \
  && cd /opt/ai-gateway-prod \
  && docker compose pull \
  && docker compose up -d --force-recreate ifix-ai-gateway"
# Expected: real (wall time) < 5 min

# 4) Verify roll-forward healthy
curl -sS https://ai-gateway.converse-ai.app/health
# Expected: HTTP 200
```

### Expected

- Direction 1 (v1.0.0 → PREV_TAG): wall time < 5 min, `/health` returns 200 after recreation
- Direction 2 (PREV_TAG → v1.0.0): wall time < 5 min, `/health` returns 200 after recreation

### Evidence box

```
Rollback target tag: ____
Direction 1 (v1.0.0 → PREV_TAG):  real=____min____s  /health=____
Direction 2 (PREV_TAG → v1.0.0):  real=____min____s  /health=____
```

> **passed_partial fallback for S10:** if no previous tag exists in GHCR (first release scenario) AND operator chooses NOT to manufacture a synthetic previous tag, S10 may be DEFERRED to a follow-up UAT. Document the deferral in the Gaps section.

- **Sign-off S10:** [ ] PASS [ ] FAIL [ ] DEFERRED · **Operator:** __________ · **Date:** __________ · **Evidence:** ______

---

## Scenario S11 — Sentry test error (~5 min, $0)

**Goal:** Trigger a deliberate error in the gateway (malformed multipart, invalid OpenAI envelope, OR an explicit debug hook) and verify the Sentry UI receives the event tagged `release=v1.0.0` + `environment=production` within 60 seconds. This is primary evidence for D-14.

**Cascades:** none directly (D-14 evidence captured into VERIFICATION).

### Setup

Sentry project `ifix-ai-gateway-prod` must already exist in the `Ifix` Sentry org (created BEFORE RUNBOOK Step 3 — see Pre-flight checklist). DSN must be populated in `/opt/ai-gateway-prod/.env` as `SENTRY_DSN=https://<key>@sentry.io/<project>`. Gateway container must already have been recreated AFTER the DSN was set (otherwise the SDK is no-op).

```bash
# 1) Confirm DSN is non-empty inside the running container
ssh n8n-ia-vm 'docker exec ai-gateway-prod sh -c "echo \$SENTRY_DSN | head -c 30"'
# Expected: a string starting with "https://" (truncated to 30 chars — do NOT print the full DSN)

# 2) Confirm release tag in container env
ssh n8n-ia-vm 'docker exec ai-gateway-prod sh -c "echo \$GATEWAY_RELEASE"'
# Expected: v1.0.0  (or whatever cut-release.sh stamped)
```

### Probe — option 1 (preferred): explicit gatewayctl debug hook

```bash
# If gatewayctl has a debug hook for error emission (depends on Phase 02-06 implementation;
# check `gatewayctl --help` for a debug subcommand):
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl debug emit-error --message "S11 UAT sentry test 2026-05-MM"'
# OR fall back to option 2.
```

### Probe — option 2 (fallback): malformed body inducing 500

```bash
# Send a malformed multipart body to /v1/audio/transcriptions designed to cause a panic-recover path
curl -sS -X POST https://ai-gateway.converse-ai.app/v1/audio/transcriptions \
  -H "Authorization: Bearer ${CONVERSEAI_KEY}" \
  -F 'file=' \
  -F 'model=' \
  | head -c 200
# Expected: a 4xx/5xx error response — the goal is to trip an error path that the Sentry SDK reports
```

### Verify in Sentry UI

```bash
# Visit:
# https://sentry.io/organizations/ifix/issues/?project=<project-id>&query=release%3Av1.0.0+environment%3Aproduction&statsPeriod=15m
# Look for at least 1 event timestamped within the last 60 seconds tagged release=v1.0.0 + environment=production.
```

### Expected

- Sentry UI shows ≥ 1 event with:
  - `release: v1.0.0`
  - `environment: production`
  - timestamp within last 60 seconds of the probe
- Event has a stacktrace OR a clear error message

### Evidence box

```
Sentry event ID:  ____
Sentry event URL: https://sentry.io/organizations/ifix/issues/____/
Release tag observed: ____  (expected: v1.0.0)
Environment observed: ____  (expected: production)
```

> **passed_partial fallback for S11:** if operator chooses NOT to bootstrap the `ifix-ai-gateway-prod` Sentry project in the same sitting (e.g. waiting on legal approval, project name TBD), S11 may be DEFERRED. Document the deferral in the Gaps section.

- **Sign-off S11:** [ ] PASS [ ] FAIL [ ] DEFERRED · **Operator:** __________ · **Date:** __________ · **Evidence:** ______

---

## Cascade-Close Commits (executed AFTER S1-S8 pass)

> **Run these 4 commits in order, AFTER S1-S8 PASS.** Each commit (a) modifies exactly one prior-phase VERIFICATION.md file with a `gaps_closed_phase_10_2026_05_XX:` evidence stanza (and an optional status flip), (b) commits as a small standalone commit on `worktree-agent-*` branch, and (c) runs the WARNING-5 positive-assertion grep recipe (Phase 06.9 commit `e3be97b` template) to confirm the right file got the right evidence.

> **Date placeholder:** replace `XX` in `gaps_closed_phase_10_2026_05_XX` with the actual UAT day (e.g. `gaps_closed_phase_10_2026_05_28` if UAT runs on 2026-05-28). Same value for all 4 commits.

> **Image SHA placeholder:** replace `<IMAGE_SHA>` with `docker inspect ai-gateway-prod | jq -r '.[0].Image'` (or the short SHA of the running container) inside each stanza.

### Cascade Commit 1 — Phase 02 SC-5 step 7 re-verify under prod URL

**Status flip:** none (Phase 02 already `passed` after Phase 06.9 closure).
**Evidence stanza:** APPEND a new `gaps_closed_phase_10_2026_05_XX:` key under `re_verification:` (co-exists with existing `gaps_closed_2026_05_25:` from Phase 06.9 closeout — Phase 02 will have TWO keyed entries per RESEARCH §Pattern 4).

```bash
# 1) Edit .planning/phases/02-gateway-core-multi-tenant-auth/02-VERIFICATION.md
# In the frontmatter, under `re_verification:`, ADD a new key (do NOT remove the existing gaps_closed_2026_05_25):
#
#   gaps_closed_phase_10_2026_05_XX:
#     - "SC-5 step 7 chat E2E re-verified under PROD URL 2026-05-XX (image <IMAGE_SHA>) — POST https://ai-gateway.converse-ai.app/v1/chat/completions {model:qwen} → HTTP 200 + non-empty completion via OpenRouter tier-1. Confirms Phase 06.9 fix holds end-to-end against the prod hostname, not just the dev URL. See 10-HUMAN-UAT.md S1."
#
# (operator edits manually — file path + key placement above is mandatory)

# 2) Commit (single-file commit)
git add .planning/phases/02-gateway-core-multi-tenant-auth/02-VERIFICATION.md
git commit -m "docs(02-VERIFICATION): cascade-close SC-5 step 7 re-verify under prod URL via Phase 10 HUMAN-UAT S1"

# 3) Positive-assertion grep (WARNING-5 recipe from Phase 06.9 e3be97b)
grep -E "gaps_closed_phase_10_2026" .planning/phases/02-gateway-core-multi-tenant-auth/02-VERIFICATION.md
# Expected: at least 1 line — must return non-empty
```

- **Sign-off Cascade-1:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Commit:** ______ · **Grep result:** ______

---

### Cascade Commit 2 — Phase 03 SC-1 re-verify under prod URL

**Status flip:** none (Phase 03 already `passed` after Phase 06.9 closure).
**Evidence stanza:** APPEND a new `gaps_closed_phase_10_2026_05_XX:` key under `re_verification:` (co-exists with existing `gaps_closed_2026_05_25:` from Phase 06.9).

```bash
# 1) Edit .planning/phases/03-resilience-fallback-chain/03-VERIFICATION.md
# Under `re_verification:`, ADD (do NOT remove existing gaps_closed_2026_05_25):
#
#   gaps_closed_phase_10_2026_05_XX:
#     - "SC-1 LIVE re-verified under PROD URL 2026-05-XX (image <IMAGE_SHA>) — 2 consecutive chat probes with local-llm FORCED_OPEN both returned HTTP 200 via openrouter-chat; audit_log shows upstream=openrouter-chat for each request_id under https://ai-gateway.converse-ai.app. Confirms Phase 06.9 fix holds end-to-end against the prod hostname. See 10-HUMAN-UAT.md S4."

# 2) Commit
git add .planning/phases/03-resilience-fallback-chain/03-VERIFICATION.md
git commit -m "docs(03-VERIFICATION): cascade-close SC-1 re-verify under prod URL via Phase 10 HUMAN-UAT S4"

# 3) Positive-assertion grep
grep -E "gaps_closed_phase_10_2026" .planning/phases/03-resilience-fallback-chain/03-VERIFICATION.md
# Expected: at least 1 line — must return non-empty
```

- **Sign-off Cascade-2:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Commit:** ______ · **Grep result:** ______

---

### Cascade Commit 3 — Phase 04 SC-1+SC-2+SC-4 (status flip passed_partial → passed)

**Status flip:** REQUIRED — `passed_partial` → `passed` via `sed -i`.
**Evidence stanza:** APPEND a new `gaps_closed_phase_10_2026_05_XX:` key under `re_verification:` (co-exists with existing `gaps_closed_2026_05_23:` from the 2026-05-23 partial close).

```bash
# 1) Flip status passed_partial → passed
sed -i 's/^status: passed_partial$/status: passed/' \
  .planning/phases/04-multi-tenant-quotas-billing-schedule-routing/04-VERIFICATION.md

# 2) Edit the same file to APPEND under re_verification: (do NOT remove existing gaps_closed_2026_05_23):
#
#   gaps_closed_phase_10_2026_05_XX:
#     - "SC-1 LIVE re-verified under PROD URL 2026-05-XX (image <IMAGE_SHA>) — 10-parallel chat burst against tenant uat10-test (rps=5) returned 3-5 HTTP 429 + Retry-After:1 + X-RateLimit headers + Prometheus gateway_rate_limit_rejected_total{tenant=\"uat10-test\",window=\"rps\"} increment. See 10-HUMAN-UAT.md S5."
#     - "SC-2 LIVE billing_events row inspection PASS — direct psql against bd_ai_gateway_prod shows COUNT(*)>0 within last 15 min after S1-S5; sample row has non-zero tokens + cost_usd. First live validation under prod DB (was deferred 2026-05-23 because MCP postgres-grupo-ifix rejected). See 10-HUMAN-UAT.md S6."
#     - "SC-4 LIVE peak-mode off-hours re-verified under PROD URL — tenant set-mode peak --window <NOW>-<NOW+1> + chat probe → module=SCHEDULE upstream=openrouter-chat decision=off_hours_external in gateway logs + Prometheus gateway_schedule_routing_total{decision=off_hours_external} increment. Confirms Phase 04 SC-4 holds under prod hostname. See 10-HUMAN-UAT.md S7."

# 3) Commit (single-file commit)
git add .planning/phases/04-multi-tenant-quotas-billing-schedule-routing/04-VERIFICATION.md
git commit -m "docs(04-VERIFICATION): cascade-close SC-1+SC-2+SC-4 via Phase 10 HUMAN-UAT S5/S6/S7 + status flip passed_partial → passed"

# 4) Positive-assertion grep (BOTH must return non-empty)
grep -E "^status: passed$" .planning/phases/04-multi-tenant-quotas-billing-schedule-routing/04-VERIFICATION.md
# Expected: exactly 1 line  "status: passed"
grep -E "gaps_closed_phase_10_2026" .planning/phases/04-multi-tenant-quotas-billing-schedule-routing/04-VERIFICATION.md
# Expected: at least 1 line — must return non-empty
```

- **Sign-off Cascade-3:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Commit:** ______ · **Grep 1 (status):** ______ · **Grep 2 (gaps_closed):** ______

---

### Cascade Commit 4 — Phase 05 SC-4/SC-5 re-verify under prod URL

**Status flip:** none (Phase 05 already `passed` after Phase 06.9 closeout — SC-1 was the cascade target there).
**Evidence stanza:** APPEND a new `gaps_closed_phase_10_2026_05_XX:` key under `re_verification:` (co-exists with existing `gaps_closed_2026_05_23:` + `gaps_closed_2026_05_25:`).

```bash
# 1) Edit .planning/phases/05-load-shedding-saturation-aware-routing/05-VERIFICATION.md
# Under `re_verification:`, ADD (do NOT remove existing gaps_closed_2026_05_23 + gaps_closed_2026_05_25):
#
#   gaps_closed_phase_10_2026_05_XX:
#     - "SC-1 re-verified under PROD URL 2026-05-XX (image <IMAGE_SHA>) — vegeta 5 RPS × 30s burst against https://ai-gateway.converse-ai.app with local-llm FORCED_OPEN returned ≥99% success ratio (≥149/150 HTTP 200 via openrouter-chat → DeepSeek v4 Flash). Confirms Phase 06.9 fix + Phase 10 prod stack deploy hold end-to-end. SC-4 (anti-starvation under shed) and SC-5 (DCGM scrape evidence) covered implicitly by the same burst — primary pod asleep, DCGM N/A per scope. See 10-HUMAN-UAT.md S8."

# 2) Commit
git add .planning/phases/05-load-shedding-saturation-aware-routing/05-VERIFICATION.md
git commit -m "docs(05-VERIFICATION): cascade-close SC-1 re-verify under prod URL via Phase 10 HUMAN-UAT S8"

# 3) Positive-assertion grep
grep -E "gaps_closed_phase_10_2026" .planning/phases/05-load-shedding-saturation-aware-routing/05-VERIFICATION.md
# Expected: at least 1 line — must return non-empty
```

- **Sign-off Cascade-4:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Commit:** ______ · **Grep result:** ______

---

### Master cascade verification (run after all 4 cascade commits land)

```bash
# All 4 VERIFICATION.md files must show the Phase 10 evidence stanza
for f in 02-gateway-core-multi-tenant-auth/02-VERIFICATION.md \
         03-resilience-fallback-chain/03-VERIFICATION.md \
         04-multi-tenant-quotas-billing-schedule-routing/04-VERIFICATION.md \
         05-load-shedding-saturation-aware-routing/05-VERIFICATION.md; do
  echo "=== $f ==="
  grep -E "gaps_closed_phase_10_2026" .planning/phases/$f && echo "OK" || echo "MISSING"
done
# Expected: 4 OK lines

# Phase 04 must be status: passed (not passed_partial)
grep -E "^status: passed$" .planning/phases/04-multi-tenant-quotas-billing-schedule-routing/04-VERIFICATION.md
# Expected: exactly 1 line
```

---

## Cleanup (MANDATORY)

> **Run ALL of this regardless of pass/fail.** Orphan FORCED_OPEN breakers prevent the prod stack from recovering normally.

```bash
# 1) Restore EVERY breaker forced open during S3 / S4 / S8 (and any scenario that used force-open)
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-close --upstream local-llm   || true'
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-close --upstream local-stt   || true'
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker force-close --upstream local-embed || true'

# 2) Verify NO FORCED_OPEN rows remain
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl breaker list | grep -i forced'
# Expected: NO OUTPUT (empty)

# 3) Restore uat10-test tenant to 24/7 mode (S7 cleanup re-assert)
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl tenant set-mode uat10-test 24/7 || true'

# 4) Confirm primary FSM state returned to pre-UAT value
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl primary state'
# Expected: same state as before S1 (typically asleep — no primary pod provisioned this UAT)

# 5) Confirm docker compose ps is healthy
ssh n8n-ia-vm 'docker compose -f /opt/ai-gateway-prod/docker-compose.yml ps'

# 6) Final spend check
#    OpenRouter: https://openrouter.ai/activity — filter by referer "ifix-uat-100-<your-initials>"
#    OpenAI:     https://platform.openai.com/usage — same day usage breakdown
#    Cloudflare: no spend (DNS API calls free)
```

**Verification (all must hold):**
- `gatewayctl breaker list | grep -i forced` returns empty.
- `gatewayctl primary state` returns the pre-UAT value (no inadvertent primary FSM mutation).
- `docker compose ps` healthy + no orphan containers.
- `gatewayctl tenant show uat10-test` returns `mode=24/7`.

- **Record:** breakers cleared: yes/no · primary state unchanged: yes/no · uat10-test back to 24/7: yes/no · **total OpenRouter spend = $______** · **total OpenAI spend = $______** · **combined total = $______**
- **Sign-off CLEANUP:** [ ] PASS [ ] FAIL · **Operator:** __________ · **Date:** __________

---

## Summary

Operator fills this section at the end of the UAT sitting:

| Field | Value |
|---|---|
| **Total wall time** | ___ h ___ min |
| **Total $ spend (cumulative)** | $______ (target ≤ $0.10; hard abort $2.00) |
| **Scenarios PASS** | ___ / 11 |
| **Scenarios FAIL** | ___ / 11 |
| **Scenarios DEFERRED** | ___ / 11 (S10 + S11 are common deferrals) |
| **Cascade commits landed** | ___ / 4 (Phase 02 + 03 + 04 + 05) |
| **Gate failures recovered** | ___ (number of Pre-UAT gates that needed re-runs) |
| **New tech debt surfaced** | (list — each becomes a `/gsd:plan-phase 10 --gaps` candidate; if none, write "none") |
| **R2 hard abort triggered?** | [ ] no [ ] yes — at scenario S___ cumulative spend $______ |
| **Cleanup PASS?** | [ ] yes (NO forced-open breakers remain; tenant uat10-test back to 24/7) [ ] no |

---

## Sign-off

> **Operator signs ONE of the three boxes below at the end of the UAT sitting.**

- [ ] **PHASE 10 PASSED** — all 11 scenarios PASS + 4 cascade commits landed + cleanup PASS. **Operator:** __________ **Date:** __________ **Total spend:** $______. **Resume signal:** `approved — all 11 scenarios PASS, 4 cascade-close commits landed, run /gsd:verify-work 10`

- [ ] **PHASE 10 PASSED PARTIAL with deferrals** — ≥ S1-S8 PASS + 4 cascade commits landed + cleanup PASS, but one or more of S9/S10/S11 DEFERRED (see Gaps section). **Operator:** __________ **Date:** __________ **Total spend:** $______. **Deferrals:** S____, S____. **Resume signal:** `partial — N PASS, M FAIL/DEFERRED — see Gaps section; run /gsd:plan-phase 10 --gaps` (N+M=11)

- [ ] **PHASE 10 FAILED** — one or more S1-S8 scenarios FAIL (blocks cascade-close commits). **Operator:** __________ **Date:** __________ **Total spend:** $______. **Failures:** S____, S____. **Resume signal:** `partial — N PASS, M FAIL — see Gaps section; run /gsd:plan-phase 10 --gaps`

- [ ] **PHASE 10 ABORTED** — R2 hard abort triggered (cumulative spend ≥ $2.00 OR per-call spend ≥ $0.05) OR operator interrupted. **Operator:** __________ **Date:** __________ **Total spend:** $______. **Triggered at:** S____. **Resume signal:** `aborted — R2 spend cap hit at $X.XX after scenario S<n>; cleanup complete; investigate before re-running`

---

## passed_partial Fallback

This UAT may exit in a `passed_partial` state if S9-S11 cannot complete within the same sitting. The 4 cascade-close commits (Phase 02/03/04/05) MUST land before signing passed_partial — they depend ONLY on S1-S8.

### Common deferral patterns

| Scenario | Common deferral reason | Re-run trigger |
|---|---|---|
| **S9** | one or more tenants not yet provisioned with prod keys (RUNBOOK Step 7 incomplete) | re-mint missing tenant keys, re-run per-tenant smoke; commit results in a `docs(10-HUMAN-UAT): S9 follow-up` commit |
| **S10** | no previous tag (`:v0.X.Y` or `:main-<previous-sha>`) exists in GHCR — first release | re-run after at least one follow-up release tag is cut; can be deferred to Phase 11 if "rollback drill on first release" is operationally moot |
| **S11** | Sentry project `ifix-ai-gateway-prod` not yet bootstrapped in Ifix Sentry org | create the Sentry project, add DSN to `/opt/ai-gateway-prod/.env`, recreate gateway container, re-run probe + UI check; commit results in `docs(10-HUMAN-UAT): S11 follow-up` |

### Sign-off rules for passed_partial

- All 4 cascade-close commits MUST land (S1-S8 PASS is the gate, not S9-S11).
- Cleanup MUST PASS (no orphan breakers).
- Each deferred scenario MUST be entered in the Gaps section with: (a) the specific deferral reason, (b) a precise re-run command, (c) the deadline / phase that owns the follow-up (typically Phase 11 or a `/gsd:plan-phase 10 --gaps` plan).
- The Sign-off line "PHASE 10 PASSED PARTIAL with deferrals" gets ticked instead of "PHASE 10 PASSED".

---

## Gaps

> Operator fills this section ONLY if any scenario FAILed or DEFERRED. Each gap becomes a `/gsd:plan-phase 10 --gaps` candidate. If all 11 scenarios PASS, write "None."

### Gap template (copy + fill per failed/deferred scenario)

```
### Gap-N — Scenario S____ ____ (FAIL | DEFERRED)

**Symptom:**

**Suspected root cause:**

**Evidence (paste log lines, response bodies, etc — REDACTED):**

**Re-run command:**

**Owner / Phase to address:** (e.g. Phase 10 --gaps, Phase 11, operator follow-up)

**Operator:** __________ · **Date:** __________
```

### Filled gaps

(none if all PASS — operator writes "None" and signs)

---

*UAT playbook authored: 2026-05-26 (Task 1A + Task 1B of Plan 10-06)*
*Plan: 10-06-PLAN.md*
*Analog: 06.9-HUMAN-UAT.md (structural mirror) + 09-HUMAN-UAT.md (per-tenant smoke pattern)*
