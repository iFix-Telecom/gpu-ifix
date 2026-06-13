# Phase 10: prod-deploy-ai-gateway - Pattern Map

**Mapped:** 2026-05-25
**Files analyzed:** 6 NEW + 6 MODIFY + 2 verify-only
**Analogs found:** 6 / 6 NEW (100%); 6 / 6 MODIFY (100%); 2 / 2 verify-only confirmed

## File Classification

### NEW files (Wave 0-3 autonomous + HUMAN-UAT deliverables)

| New File | Role | Data Flow | Closest Analog | Match Quality |
|----------|------|-----------|----------------|---------------|
| `gateway/docker-compose.prod.yml` | compose stack (config/infra) | declarative-state | `gateway/docker-compose.yml` (Swarm template) | exact-role / different-orchestrator (Compose v2 vs Swarm) |
| `gateway/.env.prod.example` | env contract (config) | declarative-state | `gateway/.env.portainer.example` | exact |
| `.planning/phases/10-prod-deploy-ai-gateway/artifacts/ai-gateway-prod.yml` | Traefik route (config/ingress) | request-response (HTTP routing) | `/home/pedro/projetos/pedro/infra/traefik-dynamic/voice-api.yml` (local) + `n8n-ia.yml` on vps-ifix-vm | exact |
| `gateway/docs/RUNBOOK-DEPLOY.md` | RUNBOOK (operator doc) | doc / batch playbook | `gateway/docs/RUNBOOK-FAILOVER.md` + `RUNBOOK-PRIMARY-POD.md` + `RUNBOOK-OBSERVABILITY-ALERTING.md` | exact (operator-doc family) |
| `.planning/phases/10-prod-deploy-ai-gateway/10-HUMAN-UAT.md` | HUMAN-UAT plan (`autonomous: false`) | operator-driven scenario list | `.planning/phases/06.9-…/06.9-HUMAN-UAT.md` (cascade-close pattern) + `.planning/phases/09-…/09-HUMAN-UAT.md` (per-tenant smoke + sign-off) | exact (06.9 for cascade close + Sentry/spend gates; 09 for per-tenant smoke + final sign-off) |
| `.planning/phases/10-prod-deploy-ai-gateway/10-VERIFICATION.md` (created by `/gsd:verify-work`) | VERIFICATION report (frontmatter + body) | doc / static state | `.planning/phases/06.9-…/06.9-VERIFICATION.md` | exact |

### MODIFY files (Wave 3-4 documentation + cascade-close)

| Modified File | Role | Data Flow | Change Pattern | Source Analog |
|---------------|------|-----------|----------------|---------------|
| `.planning/ROADMAP.md` | roadmap state | static doc | populate Phase 10 goal + add Phase 11 placeholder | inspect existing ROADMAP §Phase headers |
| `.planning/REQUIREMENTS.md` (§Traceability) | requirements map | static doc | remap PRD-01/02/03/05/06 from Phase 10 → Phase 11; keep PRD-07 + INT-06 + PRD-04 partial in Phase 10 | grep `Phase 10` rows in existing table |
| `.planning/phases/02-…/02-VERIFICATION.md` | VERIFICATION frontmatter | static doc | cascade-close: `passed` (already flipped by Phase 06.9) — re-validate SC-5 step 7 under prod URL; append `gaps_closed_phase_10_…` stanza | Phase 06.9 commit `e3be97b` |
| `.planning/phases/03-…/03-VERIFICATION.md` | VERIFICATION frontmatter | static doc | cascade-close: re-confirm SC-1 under prod URL; append `gaps_closed_phase_10_…` stanza | Phase 06.9 commit `e3be97b` |
| `.planning/phases/04-…/04-VERIFICATION.md` | VERIFICATION frontmatter | static doc | cascade-close: `passed_partial` → `passed`; SC-1/SC-2/SC-4 stanzas | Phase 06.9 commit `e3be97b` |
| `.planning/phases/05-…/05-VERIFICATION.md` | VERIFICATION frontmatter | static doc | cascade-close: SC-1 PASS under prod URL (mirrors 06.9 S6 vegeta); append stanza | Phase 06.9 commit `e3be97b` |

### Verify-only (no edits — researcher confirmed pattern already supports `:v*` tag flow)

| File | Disposition |
|------|-------------|
| `.github/workflows/build-gateway.yml` | NO-OP. Already supports `tags: ['v*']` + `branches: [main]`; `compute-tags` step emits `:v1.0.0`, `:latest`, `:v1.0.0-<sha>` (RESEARCH §How To #6). Plan adds a "verify-only" task that greps the workflow for the expected tag pattern. |
| `.github/workflows/build-dashboard.yml` | NO-OP. Symmetric to gateway workflow. Verify-only task per RESEARCH §How To #6. |

---

## Pattern Assignments

### 1. `gateway/docker-compose.prod.yml` (compose stack, declarative-state)

**Analog:** `gateway/docker-compose.yml` (Swarm template, lines 1-151)

**Diff vs analog** (research §How To #4 enumerates 9 differences):

| Item | Swarm template (analog) | Prod compose (NEW) |
|------|--------------------------|--------------------|
| Network declared | `worker_intra` external | `intra` external |
| Restart | `deploy.restart_policy` Swarm-only | top-level `restart: unless-stopped` |
| Resources | `deploy.resources` Swarm-only | top-level `resources` (Compose v2 supports it for non-Swarm) |
| Labels | under `deploy.labels` | top-level `labels` (plain compose semantics) |
| `entrypoints` value | `websecure` | `web` (internal Traefik on n8n-ia-vm is :80 only) |
| `tls.certresolver` label | `letsencryptresolver` | OMIT entirely (TLS terminates at edge, label is meaningless on :80 internal) |
| Env source | individual `environment:` block with `${VAR}` interpolation | `env_file: .env` (mirrors dev stack's `/opt/ai-gateway-dev/` pattern) |
| Container names | none (Swarm picks) | explicit `container_name: ifix-ai-gateway` (so `docker exec ifix-ai-gateway /gatewayctl …` works) |
| Redis service | not in canonical template (dev uses `infra-redis-1` shared) | NEW dedicated `redis-gateway-prod` with `--requirepass`, `--save ""`, `--appendonly no` |
| Image tag default | `${TAG:-latest-dev}` | `${TAG:-v1.0.0}` (D-12 pinned) |

**Reuse from analog** (Swarm template lines 27-91 — gateway service block):
```yaml
# Keep IDENTICAL to analog:
services:
  ifix-ai-gateway:
    image: ghcr.io/ifixtelecom/ifix-ai-gateway:${TAG:-v1.0.0}   # tag default changed
    healthcheck:
      test: ["CMD", "/gateway", "--self-check"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 60s
    labels:
      - "traefik.enable=true"
      - "traefik.http.routers.ai_gateway.rule=Host(`ai-gateway.converse-ai.app`)"
      - "traefik.http.services.ai_gateway.loadbalancer.server.port=8080"
      - "traefik.http.services.ai_gateway.loadbalancer.passHostHeader=true"
```

**Full target shape:** RESEARCH §Code Examples lines 680-759 (already drafted in research; planner copies verbatim).

**Anti-patterns inherited from analog to AVOID** (RESEARCH §Anti-Patterns):
- DO NOT use `worker_intra` (rename to `intra`)
- DO NOT use `deploy:` block (use top-level `restart` + `resources`)
- DO NOT publish gateway:8080 or dashboard:3001 on host interface
- DO NOT add `tls.certresolver=letsencryptresolver` label (only meaningful on internal Traefik; n8n-ia-vm internal Traefik is :80 only)

---

### 2. `gateway/.env.prod.example` (env contract, declarative-state)

**Analog:** `gateway/.env.portainer.example` (60+ lines visible; comment block header + sectioned env vars)

**Header comment pattern** (analog lines 1-9):
```bash
# Portainer stack environment — ai-gateway-dev / ai-gateway-prod
# Cole no campo "Environment variables" do Portainer (botão "Advanced mode"
# aceita colagem em bloco no formato KEY=value uma por linha).
#
# Substitua TODOS os <PLACEHOLDER> antes de salvar. Linhas com #DEV e #PROD
# marcam diferenças entre os dois stacks.
#
# Referência: gateway/docker-compose.yml + gateway/internal/config/config.go
```

**Adaptation for prod (`.env.prod.example`):**
```bash
# /opt/ai-gateway-prod/.env  (mode 600, owner pedro:pedro, NEVER commit)
#
# Source of values:
#   - ~/.claude/CLAUDE.md       — CF, OpenRouter, MinIO, Vast.ai, OpenAI, GitHub PAT
#   - sentry.io UI               — new project ifix-ai-gateway-prod DSN
#   - openssl rand -hex 24       — Redis password + (optional) BETTER_AUTH_SECRET
#   - gatewayctl admin-key create — dashboard admin key
#
# This file mirrors gateway/.env.prod.example one-to-one. Add new keys in BOTH places.
```

**Section structure** (analog lines 10-60 use `# ====` banner separators):
- Runtime / TZ / GATEWAY_PORT / LOG_LEVEL
- Postgres (DO managed) — NEW: dsn points at `bd_ai_gateway_prod` database (NOT `defaultdb`); same `ai_gateway` schema hardcoded in migrations (RESEARCH §How To #3 Pitfall 2)
- Redis (local container redis-gateway-prod) — NEW: dedicated, password-protected
- Upstreams (Phase 1 pod) — IDENTICAL contract to dev (`UPSTREAM_LLM_URL`, `UPSTREAM_STT_URL`, `UPSTREAM_EMBED_URL`, `UPSTREAM_HEALTH_BRIDGE_URL`)
- Tier-1 fallback (REUSE dev keys per D-08) — IDENTICAL to dev `.env` already on vps-ifix-vm
- Vast.ai (REUSE dev key + budgets) — copies CLAUDE.md memory `primary-gpu-shape-06.8-final`
- MinIO (REUSE dev creds) — copies CLAUDE.md MinIO block verbatim
- Sentry — NEW prod DSN (operator-created project `ifix-ai-gateway-prod`)
- Dashboard — `BETTER_AUTH_URL=https://ai-dashboard.converse-ai.app`, `DASHBOARD_DATABASE_URL` points at `bd_ai_dashboard_prod`
- Bootstrap (BOOTSTRAP_TENANT_SLUG=converseai) — IDENTICAL to dev

**Full target shape:** RESEARCH §Code Examples lines 584-676 (already drafted in research; planner copies verbatim).

---

### 3. `.planning/phases/10-prod-deploy-ai-gateway/artifacts/ai-gateway-prod.yml` (Traefik file-provider route)

**Analog:** `/home/pedro/projetos/pedro/infra/traefik-dynamic/voice-api.yml` (local copy, full contents):

```yaml
http:
  routers:
    voice-api:
      rule: "Host(`voice-api.ifixtelecom.com.br`)"
      entryPoints:
        - websecure
      tls:
        certResolver: letsencrypt
      service: voice-api

  services:
    voice-api:
      loadBalancer:
        servers:
          - url: "http://172.20.0.1:3333"
```

**Strong second analog:** `n8n-ia.yml` on `vps-ifix-vm:/home/pedro/projetos/pedro/infra/traefik-dynamic/n8n-ia.yml` — routes 4 hostnames to `http://10.10.10.20:80` with `passHostHeader: true`. NOT on ops-claude filesystem; researcher inspected via SSH.

**Adaptation (NEW file):**
```yaml
# Phase 10 — ai-gateway prod ingress.
# Edge Traefik v3.6 on vps-ifix-vm forwards 2 hostnames to internal Traefik v2.11 on n8n-ia-vm:80.
# Hot-reload via file provider watch on /etc/traefik/dynamic/.
# Mirror pattern: /home/pedro/projetos/pedro/infra/traefik-dynamic/n8n-ia.yml (4 hostnames → 10.10.10.20:80).
#
# certResolver = LITERAL `letsencrypt` (NOT `letsencryptresolver`).
# Edge static config (vps-ifix-vm /home/pedro/projetos/pedro/infra/docker-compose.yml:14)
# defines `--certificatesresolvers.letsencrypt.acme.email=...`. The compose-label
# `letsencryptresolver` literal in gateway/docker-compose.yml applies only to the
# INTERNAL Traefik (different config), not this file.
#
# ACME challenge = TLS-ALPN-01 (`tlschallenge=true` in edge static config), NOT DNS-01.
# Cert first-issue requires public 443 reachability + DNS A record pointing to 162.55.92.154.
# Land this file BEFORE DNS flip — edge logs `"no certificate for X"` until first :443 hits.
#
# passHostHeader=true: gateway + dashboard read original Host header
# (Better Auth cookie scope + audit log Host context).

http:
  routers:
    ai-gateway-prod:
      rule: "Host(`ai-gateway.converse-ai.app`)"
      service: n8n-ia-prod-internal
      entryPoints: [websecure]
      tls:
        certResolver: letsencrypt

    ai-dashboard-prod:
      rule: "Host(`ai-dashboard.converse-ai.app`)"
      service: n8n-ia-prod-internal
      entryPoints: [websecure]
      tls:
        certResolver: letsencrypt

  services:
    n8n-ia-prod-internal:
      loadBalancer:
        servers:
          - url: "http://10.10.10.20:80"
        passHostHeader: true
```

**Deploy mechanic** (rsync to vps-ifix-vm, hot-reload, no restart):
```bash
scp .planning/phases/10-prod-deploy-ai-gateway/artifacts/ai-gateway-prod.yml \
    vps-ifix-vm:/home/pedro/projetos/pedro/infra/traefik-dynamic/ai-gateway-prod.yml

# Watch edge Traefik tail (no restart needed)
ssh vps-ifix-vm 'docker logs -f --tail 0 $(docker ps -q -f name=traefik) 2>&1 | grep -i "ai-gateway"'
# Expected: "router ai-gateway-prod@file with rule Host(`ai-gateway.converse-ai.app`)"
```

**Rollback:** `ssh vps-ifix-vm 'rm /home/pedro/projetos/pedro/infra/traefik-dynamic/ai-gateway-prod.yml'` — hot-reload removes routes.

---

### 4. `gateway/docs/RUNBOOK-DEPLOY.md` (operator playbook)

**Primary analog:** `gateway/docs/RUNBOOK-FAILOVER.md` (header structure)

**Header pattern (analog lines 1-13):**
```markdown
# Failover & Circuit Breaker Runbook

**Phase 3 (`ifix-ai-gateway`) resilience layer.** Read this when:

- `/v1/health/upstreams` shows `status != "ok"`
- Alert fires on `gateway_breaker_trips_total > 0` in the last 5 min
- A client app reports `503` with `code: "upstream_unavailable*"`
- A client app reports `502` with `code: "tool_call_partial_stream"`
- Post-incident review of a failover event

This document is the operator's first-line reference. ...
```

**Section structure (analog grep `^##\|^### `):**
- `## Mental Model (30 seconds)` — diagram + mental model
- `## Quick Diagnosis (~2 minutes)`
- `## Incident Response by Symptom` — sub-`###` Symptom 1..N
- `## Operator Commands (gatewayctl …)`
- `## Required Env Vars`
- `## Escalation`
- `## Related Docs`

**Strong second analog:** `gateway/docs/RUNBOOK-PRIMARY-POD.md` (53277 bytes) — same operator-playbook family; mirrors Triggers/Preconditions/Steps/Verification headers more closely than RUNBOOK-FAILOVER.

**Phase 10 adaptation** — research §How To #8 supplies the full 80-line target draft. Required headers (positive-assertion grep gate `grep -E "^## (Triggers|Preconditions|Steps|Verification|Rollback)$" gateway/docs/RUNBOOK-DEPLOY.md | wc -l` expects ≥ 5):

```markdown
# RUNBOOK: Production Deploy & Cut-Release

## Triggers
- First-time prod bring-up of `ai-gateway.converse-ai.app` + `ai-dashboard.converse-ai.app`
- Roll-forward to a new GA tag (`v1.0.X`, `v1.1.0`, …)
- Rollback to a previous GA tag after a regression
- Cut-release procedure (develop → main → tag → GHA build → manual deploy)

## Preconditions
- SSH access to `n8n-ia-vm` (alias) + `vps-ifix-vm` (alias) from ops-claude.
- `~/.claude/CLAUDE.md` open for CF token + MinIO creds + OpenRouter key.
- DO Postgres console access.
- Sentry org Ifix admin access.
- GitHub repo write + tag push permission (PAT in `~/.git-credentials`).
- All Phase 02-09 tests green on develop tip.

## Steps — First-Time Bring-Up (~45 min including DNS propagation)
### Step 1 — Edge Traefik route (NO DNS YET)
### Step 2 — Postgres bootstrap (new DB `bd_ai_gateway_prod` + `bd_ai_dashboard_prod`)
### Step 3 — First `docker compose up -d` (MIGRATE=true)
### Step 4 — Flip MIGRATE off
### Step 5 — Dashboard schema bootstrap
### Step 6 — DNS flip (FINAL — triggers ACME challenge)
### Step 7 — Tenant provisioning + per-tenant golden-path

## Steps — Roll-Forward to new :v1.0.X
## Steps — Rollback to previous :v1.0.X-1
## Steps — Cut-Release Procedure

## Verification (post-deploy gate)
- [ ] `curl -sS https://ai-gateway.converse-ai.app/health | jq` returns gateway healthy
- [ ] `curl -sS https://ai-gateway.converse-ai.app/v1/health/upstreams | jq` shows 6 upstreams
- [ ] `docker exec ifix-ai-gateway /gatewayctl upstreams list` matches expected (6 rows)
- [ ] Sentry releases tab shows release tagged `v1.0.0` environment `production`
- [ ] `curl -sS -I https://ai-dashboard.converse-ai.app/` returns 200

## Rollback (escape hatch)
1. DNS panic-revert
2. Container-only revert (image tag swap)
3. Full revert to dev stack as fallback

## Postmortem stub
(Date / Trigger / Detection / Mitigation / Recovery / Action items)
```

---

### 5. `.planning/phases/10-prod-deploy-ai-gateway/10-HUMAN-UAT.md` (autonomous=false)

**Primary analog:** `.planning/phases/06.9-…/06.9-HUMAN-UAT.md` (cascade-close pattern + spend gates)

**Header table pattern (analog lines 1-19):**
```markdown
# Phase 06.9 — Live HUMAN-UAT: OpenRouter Model-Rewrite Per-Upstream + Cascade Close

**Phase gate.** These 6 scenarios prove the dispatcher → tier-1 director model-rewrite chain works end-to-end on the live dev stack, then cascade-close 3 prior phase deferrals (Phase 02 SC-5 step 7, Phase 03 SC-1, Phase 05 SC-1) that were blocked by the same root cause. **Requires real OpenRouter + OpenAI API spend (≤ $0.05 expected) → autonomous mode cannot satisfy it; an operator must run it. Zero Vast/GPU spend.**

| Header | Value |
|---|---|
| **Phase** | 06.9 — openrouter-model-rewrite-per-upstream |
| **Date** | 2026-05-MM (operator fills in) |
| **Status** | in_progress |
| **Operator** | __________ |
| **Expected wall time** | 55–70 min (incl. Pre-UAT preconditions) |
| **Expected $ spend (target)** | ≤ $0.05 (OpenRouter + OpenAI combined) |
| **R2 hard abort criterion (cumulative)** | $1.00 — STOP and triage |
| **R2 hard abort criterion (per-call)** | $0.02 for any single S1/S4 OpenRouter call — STOP and investigate |
| **Vast / GPU spend** | $0 (no pod provisioning this UAT) |
| **OPENROUTER_HTTP_REFERER** | `ifix-uat-069-<OPERATOR_INITIALS>` (traceability on OpenRouter dashboard) |
```

**Pre-UAT gate pattern (analog lines 30-72):** numbered gates with per-gate sign-off line + `[ ] PASS [ ] FAIL` + evidence + operator + date. Reuse verbatim for Phase 10 — Gate A (OpenAI key), Gate B (OpenRouter key), Gate C (migration), Gate D (image tag green on GHA), Gate E (DNS resolves), Gate F (TLS cert issued).

**Strong second analog:** `.planning/phases/09-…/09-HUMAN-UAT.md` (per-tenant smoke + final external sign-off)

**Section pattern (Phase 09 lines 51-452, grep `^##`):**
```
## Prerequisites
## Current Test
## Tests
### 1. UAT-1 — Telefonia sensitive-failover smoke
### 2. UAT-2 — Cobranças + Campanhas quotas
### 3. UAT-3 — voice-api LLM-via-gateway
### 4. UAT-4 — Per-app rollback drill, timed <5 min each
## Summary
## Sign-off
## Final Sign-off — LGPD legal review (BLOCKING, external gate)
## passed_partial fallback
## Gaps
```

**Phase 10 adaptation (8 scenarios + 4 cascade-close commits):**
- S1 — chat E2E under `https://ai-gateway.converse-ai.app` (cascades Phase 02 SC-5 step 7)
- S2 — embed via tier-0 Infinity (UPSTREAM_EMBED_URL local hop)
- S3 — STT via OpenAI whisper-1 (cascade verify)
- S4 — force-open primary breaker → tier-1 chat 200 (cascades Phase 03 SC-1)
- S5 — rate-limit burst → 429 + headers (cascades Phase 04 SC-1)
- S6 — `billing_events` row insert (cascades Phase 04 SC-2)
- S7 — peak schedule routing → `openrouter-chat` (cascades Phase 04 SC-4)
- S8 — vegeta burst 5 RPS × 30 s, ≥99% success (cascades Phase 05 SC-1)

**Cascade-close 4 commits** (after S1-S8 pass) — see `## Shared Patterns` § Cascade-Close.

---

### 6. `.planning/phases/10-prod-deploy-ai-gateway/10-VERIFICATION.md` (post-UAT, created by `/gsd:verify-work`)

**Analog:** `.planning/phases/06.9-…/06.9-VERIFICATION.md`

**Frontmatter pattern (analog lines 1-36):**
```yaml
---
phase: 06.9-openrouter-model-rewrite-per-upstream
verified: 2026-05-25T19:50:00Z
status: passed
score: 4/4 requirements + 6/6 HUMAN-UAT scenarios live PASS + 5/5 PRs merged
requirements:
  OR-FIX:
    covered_by_plan: [06.9-01, 06.9-02, 06.9-03]
    covered_by_pr: [1, 3, 4, 5]
    status: complete
    evidence: "S1 chat: POST /v1/chat/completions {model:qwen} → HTTP 200 + model=deepseek/deepseek-v4-flash-20260423 ..."
  ...
overrides_applied: 0
regressions: []
deferred: [...]
human_verification: []
---
```

**Body sections (analog `## Summary`, `## Live UAT Results` table, `## Cascade Closes` table, `## Engineering Insights Surfaced by Live UAT`, `## Commit Trail`):** mirror verbatim. Phase 10 maps S1-S8 to Phase 10 requirements (INT-06, PRD-04 partial, PRD-07, cascade-closes 02/03/04/05).

**Phase 10 frontmatter target:**
```yaml
---
phase: 10-prod-deploy-ai-gateway
verified: 2026-05-XX
status: passed
score: 8/8 HUMAN-UAT scenarios live PASS + 4/4 cascade-closes committed
requirements:
  INT-06:
    covered_by_plan: [10-01..10-05]
    status: complete
    evidence: "Per-tenant golden path: smoke-converseai.py + smoke-chat-ifix.py + smoke-sensitive-failover.py against https://ai-gateway.converse-ai.app — all reports.summary.passed=total."
  PRD-04 (partial):
    covered_by_plan: [10-04]
    status: complete
    evidence: "RUNBOOK-DEPLOY.md grep `^## (Triggers|Preconditions|Steps|Verification|Rollback)$` ≥ 5."
  PRD-07:
    covered_by_plan: [10-03]
    status: complete
    evidence: "dig +short ai-gateway.converse-ai.app → 162.55.92.154; curl -sS -I https://ai-gateway.converse-ai.app/health → HTTP/2 200; acme.json contains both vhosts."
overrides_applied: 0
regressions: []
deferred: [PRD-01, PRD-02, PRD-03, PRD-04 (full), PRD-05, PRD-06 → Phase 11]
---
```

---

## Shared Patterns

### Cascade-Close (WARNING-5 positive-assertion grep)

**Source:** Phase 06.9 commit `e3be97b` + `.planning/phases/06.9-…/06.9-VERIFICATION.md` lines 22-26 + RESEARCH §How To #9
**Apply to:** Phase 02/03/04/05 VERIFICATION.md edits (4 commits, one per cascade-closed phase)

**Recipe (per phase) — RESEARCH §How To #9 lines 1201-1233:**

```bash
# 1) Flip frontmatter status (idempotent sed against the literal `human_needed` or `passed_partial`)
sed -i 's/^status: human_needed$/status: passed/' \
    .planning/phases/03-resilience-fallback-chain/03-VERIFICATION.md

# 2) Insert evidence stanza under existing re_verification: block (operator pastes the dated log line)
#   Append a `gaps_closed_phase_10_2026_05_XX:` block referencing 10-HUMAN-UAT.md S<n>:
cat >> /tmp/03-evidence.txt <<EOF
  gaps_closed_phase_10_2026_05_XX:
    - "SC-1 LIVE — re-verified 2026-05-XX against ai-gateway.converse-ai.app (image v1.0.0).
       2 consecutive chat probes with local-llm FORCED_OPEN both returned HTTP 200 + DeepSeek
       v4 Flash via openrouter-chat; audit_log.upstream='openrouter-chat' for each request_id.
       Mirrors Phase 06.9 S4 under the prod hostname. See 10-HUMAN-UAT.md S4."
EOF

# 3) Commit (separate small commit per phase)
git add .planning/phases/03-resilience-fallback-chain/03-VERIFICATION.md
git commit -m 'docs(03-VERIFICATION): cascade-close SC-1 via Phase 10 HUMAN-UAT S4'

# 4) Positive-assertion grep (MUST return non-empty for both)
grep -E "^status: passed$" .planning/phases/03-resilience-fallback-chain/03-VERIFICATION.md
grep -E "gaps_closed_phase_10_2026" .planning/phases/03-resilience-fallback-chain/03-VERIFICATION.md
```

**Reference re_verification stanza pattern** (already in Phase 02 VERIFICATION.md lines 7-22):
```yaml
re_verification:
  previous_status: passed_partial
  previous_score: "4/5 SC fully PASS; SC-5 step 7 chat E2E deferred to Phase 03/06.6"
  gaps_closed:
    - "SC-5 live deploy 10-step checklist re-run on 2026-05-23 against ai-gateway-dev ..."
  gaps_closed_2026_05_25:
    - "Step 7 chat E2E (was 503 on 2026-05-23) re-run on 2026-05-25 against ai-gateway-dev ..."
  gaps_remaining: []
  regressions: []
```

**Phase 10 adds a new key** `gaps_closed_phase_10_2026_05_XX:` under `re_verification:` (Phase 02 will have TWO entries: `gaps_closed_2026_05_25` from 06.9 + `gaps_closed_phase_10_2026_05_XX` from Phase 10 prod URL re-verify).

### Pre-UAT Gates (R2 BLOCKING)

**Source:** `.planning/phases/06.9-…/06.9-HUMAN-UAT.md` lines 30-72
**Apply to:** Phase 10 HUMAN-UAT before S1-S8

**Per-gate template:**
```markdown
### Gate X — <name> — ~N min

<one-line description>

\`\`\`bash
<probe command — short, copy-pasteable>
\`\`\`

**Expected:** <terminal output expectation>

**If FAIL — application procedure:**
\`\`\`bash
<remediation commands>
\`\`\`

- **Sign-off Gate X:** [ ] PASS [ ] FAIL · **Evidence:** ______ · **Operator:** __________ · **Date:** __________
```

**Phase 10 gates (proposed by planner):**
- Gate A — DO databases `bd_ai_gateway_prod` + `bd_ai_dashboard_prod` exist
- Gate B — `intra` overlay attachable from n8n-ia-vm (`docker network inspect intra | grep -i attachable`)
- Gate C — GHA `:v1.0.0` tag build green (`gh run list -L 1 --workflow build-gateway.yml -b v1.0.0`)
- Gate D — Edge Traefik route loaded (`docker logs $(docker ps -q -f name=traefik) | grep ai-gateway-prod`)
- Gate E — DNS resolves (`dig +short ai-gateway.converse-ai.app @1.1.1.1` → `162.55.92.154`)
- Gate F — TLS cert issued (`curl -sS -I https://ai-gateway.converse-ai.app/health | head -1` → `HTTP/2 200`)

### Operator-managed direct `docker compose` deploy

**Source:** Dev pattern `/opt/ai-gateway-dev/` on vps-ifix-vm (CLAUDE.md memory `openrouter-token-and-stack-location`)
**Apply to:** All Wave 2-3 plans that touch `/opt/ai-gateway-prod/` on n8n-ia-vm

**Recipe (RESEARCH §How To #4 Pattern 1):**
```bash
# 1) operator creates the directory
ssh n8n-ia-vm 'sudo mkdir -p /opt/ai-gateway-prod && sudo chown pedro:pedro /opt/ai-gateway-prod'

# 2) operator copies compose file + .env
scp gateway/docker-compose.prod.yml n8n-ia-vm:/opt/ai-gateway-prod/docker-compose.yml
scp /tmp/ai-gateway-prod.env n8n-ia-vm:/opt/ai-gateway-prod/.env
ssh n8n-ia-vm 'chmod 600 /opt/ai-gateway-prod/.env'

# 3) operator brings up the stack (first-deploy flips MIGRATE on)
ssh n8n-ia-vm 'cd /opt/ai-gateway-prod && docker compose pull && docker compose up -d'

# 4) operator verifies healthcheck + Traefik discovery
ssh n8n-ia-vm 'docker compose -f /opt/ai-gateway-prod/docker-compose.yml ps; docker logs ifix-ai-gateway --tail 50'

# 5) operator flips MIGRATE off after first successful migration
ssh n8n-ia-vm "sed -i 's/^AI_GATEWAY_MIGRATE_ON_BOOT=true/AI_GATEWAY_MIGRATE_ON_BOOT=false/' /opt/ai-gateway-prod/.env"
ssh n8n-ia-vm 'cd /opt/ai-gateway-prod && docker compose up -d --force-recreate ifix-ai-gateway'
```

### Cloudflare DNS A record creation

**Source:** `~/.claude/CLAUDE.md` (CF token) + RESEARCH §How To #2 + Pattern 3 lines 410-438
**Apply to:** Plan 10-03 (DNS flip in `## How To #2` block)

**Recipe (idempotent POST):**
```bash
CF_TOKEN="$(grep '^cfut_' ~/.claude/CLAUDE.md | head -1 | tr -d ' ')"
ZONE_ID="0e779b74b86957bdb628d646dbf33978"   # converse-ai.app

for host in ai-gateway ai-dashboard; do
  curl -sS -X POST \
    -H "Authorization: Bearer ${CF_TOKEN}" \
    -H "Content-Type: application/json" \
    "https://api.cloudflare.com/client/v4/zones/${ZONE_ID}/dns_records" \
    -d "{
      \"type\":\"A\",
      \"name\":\"${host}.converse-ai.app\",
      \"content\":\"162.55.92.154\",
      \"proxied\":false,
      \"ttl\":300,
      \"comment\":\"Phase 10 prod ifix-ai-gateway (created $(date -Iseconds))\"
    }" | jq -r '.success, .errors // empty'
done

# Verify
dig +short ai-gateway.converse-ai.app @1.1.1.1
dig +short ai-dashboard.converse-ai.app @1.1.1.1
```

**Anti-pattern:** DO NOT set `proxied=true` — TLS-ALPN-01 challenge requires direct origin reachability (RESEARCH §Anti-Patterns).

### Env-override-wins (Phase 06.9 D-06 precedence)

**Source:** Phase 06.9 D-06 (env override > schema row > passthrough)
**Apply to:** `gateway/.env.prod.example` design — `UPSTREAM_LLM_OPENROUTER_MODEL=` (empty) lets the schema row from migration 0027 (`deepseek-v4-flash:nitro`) win. RESEARCH Assumption A9 verified.

### Sentry release tag wiring (DO NOT modify)

**Source:** `gateway/internal/obs/sentry.go:24` + `gateway/Dockerfile:42` `-ldflags '-X .../obs.BuildVersion=v1.0.0'` + GHA `compute-tags` sets `gateway_version=v1.0.0` when ref matches `refs/tags/v*`
**Apply to:** Phase 10 confirms the chain via the `develop → main → tag v1.0.0` flow only — no code edits.

**Pitfall (RESEARCH §Pitfall 5):** if operator builds locally or pushes to `main` without tagging first, `BuildVersion` stays at `main-<sha>` and Sentry releases tag as `main-<sha>`. The cut-release order in RUNBOOK-DEPLOY.md (§Steps — Cut-Release Procedure) enforces tag-push BEFORE deploy.

---

## No Analog Found

None. All 6 NEW files + 6 MODIFY files have direct codebase analogs. Two notes:

- **`n8n-ia.yml`** (the natural mirror for `ai-gateway-prod.yml`) is NOT in `gpu-ifix` repo — lives only on `vps-ifix-vm:/home/pedro/projetos/pedro/infra/traefik-dynamic/n8n-ia.yml`. Researcher confirmed via SSH probe; plans MUST NOT attempt to read it locally. The local `voice-api.yml` is a strong same-shape analog.
- **GHA workflows** (build-gateway.yml / build-dashboard.yml) have NO new analog because they require NO edits — `compute-tags` already emits the `:v1.0.0` + `:latest` + `:v1.0.0-<sha>` triplet (RESEARCH §How To #6 line 1020). Verify-only task in plans.

---

## Metadata

**Analog search scope:**
- `gateway/docker-compose.yml` (Swarm template — primary compose analog)
- `gateway/.env.portainer.example` (env contract analog)
- `gateway/docs/RUNBOOK-{FAILOVER,PRIMARY-POD,OBSERVABILITY-ALERTING,EMERGENCY-POD}.md` (RUNBOOK structure family)
- `.planning/phases/06.9-…/06.9-{HUMAN-UAT,VERIFICATION}.md` (HUMAN-UAT cascade pattern + VERIFICATION frontmatter)
- `.planning/phases/09-…/09-HUMAN-UAT.md` (per-tenant smoke + external sign-off pattern)
- `.planning/phases/02-…/02-VERIFICATION.md` (cascade-close target frontmatter shape)
- `/home/pedro/projetos/pedro/infra/traefik-dynamic/voice-api.yml` (Traefik file-provider analog, local copy)
- `/home/pedro/projetos/pedro/infra/traefik-dynamic/n8n-ia.yml` (researcher inspected via SSH; cited only)
- `.github/workflows/build-gateway.yml` + `build-dashboard.yml` (verify-only — no edits)

**Files scanned:** 12 direct reads + grep on 4 RUNBOOK headers + researcher's 8 SSH probes already documented in RESEARCH §Sources.

**Pattern extraction date:** 2026-05-25
