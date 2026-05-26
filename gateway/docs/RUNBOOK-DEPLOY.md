# RUNBOOK: Production Deploy & Cut-Release

**Phase 10 (`ifix-ai-gateway`) production deploy + release-cut playbook.** Read this when:

- You are bringing up `ai-gateway.converse-ai.app` + `ai-dashboard.converse-ai.app` for the FIRST time (Phase 10 HUMAN-UAT).
- You are rolling forward to a new GA tag (`v1.0.X`, `v1.1.0`, …).
- You are rolling back to a previous GA tag after a regression (INT-06 timed-rollback gate).
- You are cutting a new release (develop → main fast-forward + tag push).

This document is the **operator's first-line reference**. It is self-contained: an operator who reads only this file (plus `~/.claude/CLAUDE.md` for secrets) should be able to perform any of the procedures below without touching `10-CONTEXT.md` or `10-RESEARCH.md`.

Related runbooks:

- `gateway/docs/RUNBOOK-FAILOVER.md` — what to do when an upstream goes OPEN post-deploy.
- `gateway/docs/RUNBOOK-PRIMARY-POD.md` — what to do when the Vast.ai primary pod misbehaves.
- `gateway/docs/RUNBOOK-OBSERVABILITY-ALERTING.md` — Sentry + Prometheus dashboards.
- `gateway/docs/RUNBOOK-CLIENT-INTEGRATION.md` + `RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md` — what the 6 client apps need to consume the new prod gateway.

---

## Triggers

- **First-time prod bring-up** of `ai-gateway.converse-ai.app` + `ai-dashboard.converse-ai.app` (Phase 10 HUMAN-UAT, expected ~45 min including DNS propagation).
- **Roll-forward** to a new GA tag (`v1.0.X`, `v1.1.0`, …) — sed the image tag in `/opt/ai-gateway-prod/docker-compose.yml` + `docker compose pull && up -d`.
- **Rollback** to a previous GA tag after a regression — same sed + `up -d --force-recreate`. **INT-06 timed gate: < 5 minutes** end-to-end from operator decision to `/health` returning the previous build version.
- **Cut-release procedure** — `develop` → `main` fast-forward + `git tag -a v1.0.0` + `git push origin v1.0.0` (triggers GHA build for `ghcr.io/ifixtelecom/ifix-ai-{gateway,dashboard}:v1.0.0`).

---

## Preconditions

Before running ANY of the procedures below, verify:

| Precondition | Verification |
|--------------|--------------|
| SSH alias `n8n-ia-vm` works from ops-claude | `ssh n8n-ia-vm 'echo ok'` returns `ok` |
| SSH alias `vps-ifix-vm` works from ops-claude | `ssh vps-ifix-vm 'echo ok'` returns `ok` |
| `~/.claude/CLAUDE.md` is open | needed for the Cloudflare DNS API Token, MinIO weights creds, OpenRouter key, GHCR PAT |
| DO Postgres console access | needed by Step 2 (CREATE DATABASE) — operator confirms login at `https://cloud.digitalocean.com/databases/...` |
| Sentry org Ifix admin access | needed for new prod project DSN |
| GitHub repo write + tag push permission | PAT lives in `~/.git-credentials` on ops-claude (mode 600) |
| All Phase 02-09 tests green on develop tip | `gh run list --limit 5 --workflow build-gateway.yml` shows last 5 runs all green |
| `/opt/ai-gateway-dev/` on `vps-ifix-vm` is RUNNING | rollback target; explicit Phase 10 cutover policy keeps the dev stack online during cutover |

If any precondition fails, STOP and resolve before proceeding — incorrect step ordering causes the ACME challenge to never start (Pitfall 3) or for the gateway to crash-loop on a missing schema (Pitfall 2).

---

## Steps

The four procedures (first-time bring-up, roll-forward, rollback, cut-release) share the same primitives — Postgres bootstrap, compose up, edge Traefik route, Cloudflare DNS, tenant provisioning. Each procedure picks a subset of the steps below. The default reading order is `First-Time Bring-Up` (Steps 1-7); the other three procedures are described in their own sub-sections.

### Steps — First-Time Bring-Up

**Expected wall-clock:** ~45 minutes (Postgres bootstrap 5 min + first compose-up 10 min + DNS propagation 5 min + per-tenant smoke 25 min).

The procedure below follows Plan 10-01 / 10-02 / 10-03 deploy scripts in the order RESEARCH §How To #8 mandates. **DNS comes LAST (Step 6)** — flipping DNS before the edge Traefik route exists causes ACME to never start (Pitfall 3); the router is what triggers `tls-alpn-01` cert acquisition.

### Step 1 — Preflight + edge Traefik route (NO DNS YET)

1. From ops-claude, run the Wave 0 preflight probe — confirms VM 101 capacity, `intra` overlay attachability (Pitfall 1: network name is `intra`, not `traefik-public`), and a recent GHA build exists:

   ```bash
   scripts/deploy/preflight.sh
   # Exit 0 = pass; exit 2 = disk >80% or egress-IP mismatch → resolve before proceeding.
   ```

2. Populate `/tmp/ai-gateway-prod.env` from `gateway/.env.prod.example`. Secrets sources:

   - Cloudflare token: `~/.claude/CLAUDE.md` → "Cloudflare DNS API Token".
   - MinIO weights: `~/.claude/CLAUDE.md` → "MinIO S3 — s3.ifixtelecom.com.br".
   - OpenRouter bearer: `~/.claude/CLAUDE.md` → "OpenRouter API Key".
   - GHCR PAT: `~/.git-credentials` (already populated on ops-claude).
   - Sentry prod DSN: new project `ifix-ai-gateway-prod` in the Ifix org.
   - `GATEWAY_ADMIN_KEY`: `openssl rand -hex 24`.

   Pitfall 7 reminder: the prod OpenRouter / OpenAI keys MUST be separate from the dev keys — otherwise spend will mix across `/opt/ai-gateway-dev/` and `/opt/ai-gateway-prod/`.

3. Copy compose + env to the prod host. Tighten `.env` perms to 600 immediately on landing:

   ```bash
   scp gateway/docker-compose.prod.yml n8n-ia-vm:/opt/ai-gateway-prod/docker-compose.yml
   scp /tmp/ai-gateway-prod.env n8n-ia-vm:/opt/ai-gateway-prod/.env
   ssh n8n-ia-vm 'chmod 600 /opt/ai-gateway-prod/.env && ls -la /opt/ai-gateway-prod/'
   ```

4. Land the edge Traefik file-provider route on `vps-ifix-vm` BEFORE DNS (Pitfall 3). The route uses the LITERAL `certResolver: letsencrypt` — NOT the dev-stack legacy `letsencryptresolver` (Pitfall 4):

   ```bash
   scp .planning/phases/10-prod-deploy-ai-gateway/artifacts/ai-gateway-prod.yml \
       vps-ifix-vm:/home/pedro/projetos/pedro/infra/traefik-dynamic/ai-gateway-prod.yml
   # Edge Traefik file-watcher hot-reloads within ~1s. Tail until you see the router added:
   ssh vps-ifix-vm 'docker logs --since 30s $(docker ps -q -f name=traefik) 2>&1 | grep -i ai-gateway-prod'
   # Expect a line like:
   #   level=info msg="..router added: ai-gateway-prod@file"
   # Plus a benign "no certificate for ai-gateway.converse-ai.app" info-log
   # (cert is issued in Step 6 after DNS flip — see Pitfall 3).
   ```

### Step 2 — Postgres bootstrap (NEW DATABASES, hardcoded `ai_gateway` schema)

Run `bootstrap-postgres.sh` against the DO managed instance. The script is idempotent: it probes `pg_database` and only issues `CREATE DATABASE` if the target name is missing. Pitfall 2: the gateway hardcodes the schema name `ai_gateway` in 27 migration files + `gateway/internal/db/pool.go:35`. Isolation between dev and prod is therefore achieved by **separate database names** (`bd_ai_gateway_prod`, `bd_ai_dashboard_prod`), NOT by a renamed schema.

```bash
# DO_ADMIN_DSN sources doadmin from the DO Postgres console — copy the "Connection String" then
# substitute `defaultdb` with the password-bearing form. Script NEVER echoes the DSN.
export DO_ADMIN_DSN='postgres://doadmin:<PASS>@db-grupoifix-do-user-7520351-0.j.db.ondigitalocean.com:25060/defaultdb?sslmode=require'
scripts/deploy/bootstrap-postgres.sh
# Expected log lines:
#   creating: bd_ai_gateway_prod
#   creating: bd_ai_dashboard_prod
# Re-run is safe and prints "already exists — skipping" for each DB.
```

Sanity-probe the new databases:

```bash
psql "postgres://doadmin:<PASS>@db-grupoifix-do-user-7520351-0.j.db.ondigitalocean.com:25060/bd_ai_gateway_prod?sslmode=require" \
  -c "SELECT current_database();"
# Expect: bd_ai_gateway_prod
```

### Step 3 — First `docker compose up -d` (MIGRATE=true)

The first boot runs `goose up` against the empty `bd_ai_gateway_prod` so the 27 phase-2-through-phase-8 migrations land before any client traffic. `AI_GATEWAY_MIGRATE_ON_BOOT=true` is set in `.env` ONLY for this first boot — Pitfall 8 says leaving it on causes a goose race on every restart (multiple replicas would all try to acquire the migrate lock on boot).

```bash
ssh n8n-ia-vm 'cd /opt/ai-gateway-prod && docker compose pull && docker compose up -d'
# Tail the gateway boot log; expect "goose: applied 27 migrations, current version: 27"
# (the exact migration count may grow if newer phases land; the important contract is
# that the count matches `ls gateway/db/migrations/*.up.sql | wc -l`).
ssh n8n-ia-vm 'docker logs --since 2m ifix-ai-gateway 2>&1 | head -80'

# Run the gateway's own self-check; expect exit code 0:
ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gateway --self-check'
echo "self-check exit: $?"

# Internal-Traefik (Swarm provider on n8n-ia-vm) discovers the gateway via labels on
# the `intra` overlay network. Confirm router-added:
ssh n8n-ia-vm 'docker service logs $(docker service ls -q -f name=traefik-internal_traefik) 2>&1 | grep -i ai_gateway | tail -5'
```

If the internal Traefik does NOT discover the container (Open Question 2 / Assumption A2 failure mode), the operator falls back to dual-provider mode (`--providers.docker=true` alongside `--providers.docker.swarmMode=true`) per the preflight script's documented escape hatch.

### Step 4 — Flip `MIGRATE_ON_BOOT=false` (Pitfall 8)

After the first migration succeeds, flip the env var off and recreate. Subsequent boots will skip `goose up` and the dashboard's `goose down` rollback path remains the documented schema-change recovery.

```bash
ssh n8n-ia-vm "sed -i 's/^AI_GATEWAY_MIGRATE_ON_BOOT=true/AI_GATEWAY_MIGRATE_ON_BOOT=false/' /opt/ai-gateway-prod/.env"
ssh n8n-ia-vm 'cd /opt/ai-gateway-prod && docker compose up -d --force-recreate ifix-ai-gateway'
# Verify boot log no longer says "goose: applied" — should say "migrate skip" or similar:
ssh n8n-ia-vm 'docker logs --since 1m ifix-ai-gateway 2>&1 | grep -E "migrate|goose"'
```

### Step 5 — Dashboard schema bootstrap (Better Auth one-shot)

The dashboard uses Better Auth's `@better-auth/cli migrate` command to create the four auth tables (`user`, `session`, `account`, `verification`) inside `bd_ai_dashboard_prod` under the schema `dashboard_auth`. This is a separate code path from `goose` — Better Auth owns its own migrations.

```bash
# DASHBOARD_DATABASE_URL points at bd_ai_dashboard_prod with search_path=dashboard_auth.
export DASHBOARD_DATABASE_URL='postgres://doadmin:<PASS>@db-grupoifix-do-user-7520351-0.j.db.ondigitalocean.com:25060/bd_ai_dashboard_prod?sslmode=require&options=-c%20search_path%3Ddashboard_auth'
scripts/deploy/migrate-dashboard.sh
# The script runs the migration INSIDE the dashboard prod image (ghcr.io/ifixtelecom/ifix-ai-dashboard:v1.0.0)
# via `docker run --rm`, so the @better-auth/cli version is pinned to the same release
# the dashboard container will use at runtime (T-10-02-05 — prevents CLI version drift).
```

Sanity-probe the tables landed:

```bash
psql "$DASHBOARD_DATABASE_URL" -c "\dt dashboard_auth.*"
# Expect: user, session, account, verification (4 rows).
```

### Step 6 — DNS flip (FINAL — triggers TLS-ALPN-01 ACME challenge)

**This is the last step that can be done before public traffic arrives.** Only at this point do we POST the Cloudflare A records. The first `:443` request to either hostname triggers the `tls-alpn-01` ACME challenge → cert cached in `acme.json` on `vps-ifix-vm`. Pitfall 3 + Pitfall 4 reminders:

- Pitfall 3: **edge Traefik route MUST already exist** (Step 1) — otherwise ACME never starts; clients see the edge default self-signed cert and reject the connection.
- Pitfall 4: the route's `certResolver` literal is `letsencrypt` (verified — `/home/pedro/projetos/pedro/infra/docker-compose.yml:14` declares `--certificatesresolvers.letsencrypt.acme.email=...`). Do NOT use the legacy `letsencryptresolver` name — that resolver does NOT exist on the current edge Traefik.

```bash
# CF_API_TOKEN literal lives in ~/.claude/CLAUDE.md under "Cloudflare DNS API Token".
export CF_API_TOKEN=<REDACTED-CF-TOKEN>
scripts/deploy/cf-dns-create.sh
# Script POSTs 2 A records (ai-gateway.converse-ai.app + ai-dashboard.converse-ai.app)
# → 162.55.92.154 TTL 300 proxied=OFF. Idempotent: re-POST returns the existing record
# unchanged.
```

Wait for propagation + first ACME challenge:

```bash
# DNS propagation — CF NS responds within seconds:
dig +short ai-gateway.converse-ai.app @1.1.1.1
# Expect: 162.55.92.154
dig +short ai-dashboard.converse-ai.app @1.1.1.1
# Expect: 162.55.92.154

# First :443 probe triggers tls-alpn-01:
curl -sS -I https://ai-gateway.converse-ai.app/health
# Expect HTTP/2 200 with a valid Let's Encrypt cert in the TLS handshake.

# Confirm cert landed in acme.json on the edge:
ssh vps-ifix-vm 'docker exec infra-traefik-1 cat /letsencrypt/acme.json | jq ".letsencrypt.Certificates[].domain.main"'
# Expect both hostnames listed:
#   "ai-gateway.converse-ai.app"
#   "ai-dashboard.converse-ai.app"
```

### Step 7 — Tenant provisioning + per-tenant golden-path smoke

Run the idempotent `provision-tenants.sh` (RESEARCH §How To #7 verbatim) against the prod gateway. The script:

- Calls `gatewayctl tenant create` for each tenant (idempotent — "already exists" passes through).
- Calls `gatewayctl quota set` (UPDATE — idempotent).
- With `--mint-keys`: calls `gatewayctl api-key mint` ONCE per tenant (NON-idempotent — keys are printed and MUST be captured immediately).

Tenants seeded by Phase 2 migration 0001 = `converseai`. Tenants seeded by Phase 8 (dev only) = `chat-ifix`. Tenants in script = `telefonia` (sensitive), `cobrancas` (sensitive), `campanhas` (normal), `voice-api` (normal). Confirm `chat-ifix` is present in prod via a one-off `gatewayctl tenant create --slug chat-ifix --name "Chat Ifix"` if the smoke flags it missing.

```bash
# Idempotent tenant + quota create (safe to re-run):
AI_GATEWAY_PG_DSN="postgres://doadmin:<PASS>@db-grupoifix-do-user-7520351-0.j.db.ondigitalocean.com:25060/bd_ai_gateway_prod?sslmode=require" \
  ./scripts/integration-smoke/provision-tenants.sh \
    --gatewayctl "ssh n8n-ia-vm docker exec ifix-ai-gateway /gatewayctl"

# Mint keys (RUN ONCE — keys are printed to stdout; capture immediately):
AI_GATEWAY_PG_DSN="postgres://doadmin:<PASS>@db-grupoifix-do-user-7520351-0.j.db.ondigitalocean.com:25060/bd_ai_gateway_prod?sslmode=require" \
  ./scripts/integration-smoke/provision-tenants.sh --mint-keys \
    --gatewayctl "ssh n8n-ia-vm docker exec ifix-ai-gateway /gatewayctl" \
  2>>/tmp/provision.log >>/tmp/provision-keys.txt
```

Distribute the 6 client keys + 1 admin key:

| Destination | Tenant slug | Env var name |
|-------------|-------------|--------------|
| ConverseAI v4 Portainer stack | `converseai` | `GATEWAY_API_KEY` |
| Chat Ifix Portainer stack | `chat-ifix` | `GATEWAY_API_KEY` |
| `cobrancas-api` Portainer stack | `cobrancas` | `GATEWAY_API_KEY` |
| `fallback-register-ramais-nextbilling` | `telefonia` | `GATEWAY_API_KEY` |
| `campanhas-chatifix` Portainer stack | `campanhas` | `GATEWAY_API_KEY` |
| `voice-api` Portainer stack | `voice-api` | `GATEWAY_API_KEY` |
| `/opt/ai-gateway-prod/.env` on n8n-ia-vm | (admin) | `GATEWAY_ADMIN_KEY` |

Run the per-tenant golden-path smokes (see Verification section for assertions):

```bash
python scripts/integration-smoke/smoke-converseai.py \
  --gateway-url https://ai-gateway.converse-ai.app \
  --api-key "$CONVERSEAI_KEY" --out /tmp/smoke-converseai-prod.json

python scripts/integration-smoke/smoke-chat-ifix.py \
  --gateway-url https://ai-gateway.converse-ai.app \
  --api-key "$CHAT_IFIX_KEY" --out /tmp/smoke-chat-ifix-prod.json

python scripts/integration-smoke/smoke-sensitive-failover.py \
  --gateway-url https://ai-gateway.converse-ai.app \
  --api-key "$TELEFONIA_KEY" --tenant telefonia \
  --out /tmp/smoke-telefonia-prod.json

python scripts/integration-smoke/smoke-sensitive-failover.py \
  --gateway-url https://ai-gateway.converse-ai.app \
  --api-key "$COBRANCAS_KEY" --tenant cobrancas \
  --out /tmp/smoke-cobrancas-prod.json
```

Each script writes a JSON report (schema in `*-report-schema.json`). The Verification section below asserts `exit_code = 0` AND `report.summary.passed == report.summary.total` for all four reports.

---

### Steps — Roll-Forward to new `:v1.0.X`

Roll-forward is a simple image-tag swap. The gateway is stateless against `bd_ai_gateway_prod`; the prod schema is migrated by `goose` ONCE on first boot (Step 3) and any subsequent schema deltas land via a deliberate `MIGRATE_ON_BOOT=true` flip-and-back (the same Step 3 + Step 4 pattern). Pitfall 5 reminder: cut the tag (Cut-Release section) BEFORE the deploy so `obs.BuildVersion=v1.0.X` propagates to Sentry releases on the first boot.

```bash
# 1) Sed the image tag in /opt/ai-gateway-prod/docker-compose.yml.
ssh n8n-ia-vm 'sed -i "s|ifix-ai-gateway:v1.0.X-1|ifix-ai-gateway:v1.0.X|g; s|ifix-ai-dashboard:v1.0.X-1|ifix-ai-dashboard:v1.0.X|g" /opt/ai-gateway-prod/docker-compose.yml'

# 2) Pull + up.
ssh n8n-ia-vm 'cd /opt/ai-gateway-prod && docker compose pull && docker compose up -d'

# 3) Verify boot log shows the new build_version.
ssh n8n-ia-vm 'docker logs --since 1m ifix-ai-gateway 2>&1 | grep -i "build_version\|BuildVersion" | head -3'
# Expect: build_version=v1.0.X (matches the tag you cut).

# 4) Verify Sentry releases tab.
# Visit https://sentry.io/organizations/ifix/releases/ — should show v1.0.X env=production.
```

Roll-forward wall-clock: ~3 minutes (image pull dominates).

---

### Steps — Rollback to previous `:v1.0.X-1`

**INT-06 timed gate: < 5 minutes** end-to-end from operator decision to `/health` returning the previous build version.

```bash
# 1) Sed the image tag back.
ssh n8n-ia-vm 'sed -i "s|ifix-ai-gateway:v1.0.X|ifix-ai-gateway:v1.0.X-1|g; s|ifix-ai-dashboard:v1.0.X|ifix-ai-dashboard:v1.0.X-1|g" /opt/ai-gateway-prod/docker-compose.yml'

# 2) Force-recreate so the running container picks up the previous image.
ssh n8n-ia-vm 'cd /opt/ai-gateway-prod && docker compose pull && docker compose up -d --force-recreate'

# 3) Verify previous build_version in boot log.
ssh n8n-ia-vm 'docker logs --since 1m ifix-ai-gateway 2>&1 | grep -i "build_version" | head -3'

# 4) Verify /health responds with the previous version.
curl -sS https://ai-gateway.converse-ai.app/health | jq '.build_version'
# Expect: "v1.0.X-1"
```

**Schema-down caveat:** if `v1.0.X` shipped a schema migration that isn't backward-compatible (e.g. dropped a column the previous binary still selects), rollback requires running `goose down` ONCE on `bd_ai_gateway_prod` for the corresponding migration. Document the "rollback-safe?" flag for each `v1.0.X` tag in the release CHANGELOG. The default contract is **forward-compatible additive migrations only** so rollback is always pure container-tag swap.

---

### Steps — Cut-Release Procedure

The release procedure is **develop → main fast-forward + signed tag push**. The GHA workflows (`build-gateway.yml` + `build-dashboard.yml`) already watch `push: tags: ['v*']` and build `:v1.0.0` + `:latest` + `:v1.0.0-<sha>` images into GHCR. Pitfall 5: cut the tag BEFORE the deploy so `GATEWAY_VERSION=v1.0.0` is baked into the binary (`-X .../obs.BuildVersion=v1.0.0`) → Sentry releases tag correctly.

D-11 note: the `deploy-prod` GHA job is intentionally a no-op — the `PORTAINER_WEBHOOK_URL_PROD_GATEWAY` secret stays UNSET; the workflow logs `skipping prod deploy` (lines 230-234 of `build-gateway.yml`) — harmless. The operator does the deploy manually via `docker compose pull && docker compose up -d`.

```bash
cd /home/pedro/projetos/pedro/gpu-ifix

# 1) Sync develop.
git checkout develop && git pull

# 2) Sync main; create if missing locally (track origin/main).
git checkout main 2>/dev/null || git checkout -b main origin/main
git pull

# 3) Confirm fast-forwardability. If main diverged, rebase develop onto main BEFORE merging.
git log --oneline develop ^main | head -5

# 4) Fast-forward main to develop tip.
git merge --ff-only develop
git push origin main

# 5) Tag v1.0.0 (signed if a GPG key is available on ops-claude).
gpg --list-secret-keys 2>/dev/null | head -1 && SIGN=-s || SIGN=
git tag -a $SIGN v1.0.0 -m "Phase 10: first GA release — gateway + dashboard prod cutover

- Multi-tenant Auth (Phase 2)
- Resilience + Fallback Chain (Phase 3, 06.9)
- Quotas / Billing / Schedule Routing (Phase 4)
- Load Shedding (Phase 5)
- Emergency Pod + Primary Pod (Phase 6 + 06.6 + 06.8)
- Observability Dashboard + Sentry (Phase 7)
- Client Integrations + Sensitive Tenants (Phase 8 + 9)
- First Production Deploy on n8n-ia-vm (Phase 10)"
git push origin v1.0.0

# 6) Tail the GHA tag-build to green.
gh run watch

# 7) Smoke-pull the new images locally to confirm GHCR has them.
docker pull ghcr.io/ifixtelecom/ifix-ai-gateway:v1.0.0
docker pull ghcr.io/ifixtelecom/ifix-ai-dashboard:v1.0.0
```

After the tag-build is green, run the Roll-Forward procedure above with `v1.0.X-1=<previous>` and `v1.0.X=v1.0.0`. The FIRST cut of `v1.0.0` on a fresh stack does NOT use Roll-Forward — it uses First-Time Bring-Up (Steps 1-7).

---

## Verification

(post-deploy gate)

Run this checklist after EVERY deploy (first-time bring-up, roll-forward, rollback). All items must be green before the deploy is considered complete.

- [ ] `curl -sS https://ai-gateway.converse-ai.app/health | jq` returns `{"status":"ok", ...}` with the expected `build_version`.
- [ ] `curl -sS https://ai-gateway.converse-ai.app/v1/health/upstreams | jq` shows **6 upstreams** with `local-llm`, `openrouter-chat`, `local-stt`, `openai-whisper`, `local-embed`, `openai-embed` — states match the runtime expectation (CLOSED if upstreams reachable; OPEN with documented cause otherwise — see `RUNBOOK-FAILOVER.md`).
- [ ] `ssh n8n-ia-vm docker exec ifix-ai-gateway /gatewayctl upstreams list` matches the 6 rows above (same NAMEs, same ROLEs, same TIERs).
- [ ] Sentry releases tab (`https://sentry.io/organizations/ifix/releases/`) shows release tagged `v1.0.0` environment `production` (Pitfall 5 — if missing, the build did not propagate `GATEWAY_VERSION`).
- [ ] `curl -sS -I https://ai-dashboard.converse-ai.app/` returns `HTTP/2 200` with the prod cert in the TLS handshake.
- [ ] All 4 per-tenant smokes (`smoke-converseai.py`, `smoke-chat-ifix.py`, `smoke-sensitive-failover.py` × 2) exit 0 with `report.summary.passed == report.summary.total`.
- [ ] Audit log records first prod request: `psql "$AI_GATEWAY_PG_DSN" -c "SELECT count(*) FROM ai_gateway.audit_log WHERE created_at > now() - interval '15 minutes';"` returns > 0.

If any item is red, do NOT proceed to client-app key distribution — escalate via the Rollback section below.

---

## Rollback

(escape hatch — 3 tiers)

The rollback path is **tiered** by severity. Pick the smallest tier that resolves the issue.

### Tier 1 — DNS panic-revert (TTL 300s, ~5 min)

The fastest way to remove public traffic from the prod stack without touching the running containers. Reverts both hostnames so clients fall back to dial-failure within the TTL.

```bash
# CF API token same as Step 6.
export CF_API_TOKEN=<REDACTED-CF-TOKEN>
# DELETE both A records (script idempotent — re-create later when ready):
for H in ai-gateway ai-dashboard; do
  RECORD_ID=$(curl -sS -H "Authorization: Bearer $CF_API_TOKEN" \
    "https://api.cloudflare.com/client/v4/zones/0e779b74b86957bdb628d646dbf33978/dns_records?name=${H}.converse-ai.app" \
    | jq -r '.result[0].id')
  curl -sS -X DELETE -H "Authorization: Bearer $CF_API_TOKEN" \
    "https://api.cloudflare.com/client/v4/zones/0e779b74b86957bdb628d646dbf33978/dns_records/$RECORD_ID"
done
```

After Tier 1: investigate offline; the dev stack `/opt/ai-gateway-dev/` on `vps-ifix-vm` is still running for any client app that flips back via Tier 3.

### Tier 2 — Container-only revert (image tag swap, ~3 min)

Use this when the deploy itself was bad (regression in `:v1.0.X`) but DNS + routing are healthy. This is the "Rollback to previous `:v1.0.X-1`" procedure above. INT-06 timed gate applies: < 5 minutes end-to-end.

### Tier 3 — Full revert to dev stack (~10 min)

Phase 10 explicitly keeps `/opt/ai-gateway-dev/` on `vps-ifix-vm` RUNNING during cutover (Discretion Cutover sequence — RESEARCH §Claude's Discretion). If the prod stack is unrecoverable AND the client apps cannot tolerate a chat outage, flip each client app's `gateway_base_url` env var back to `https://gateway-dev.ifixtelecom.com.br` and restart the client container. The dev stack is fully provisioned with the same tenants + keys (Phase 08) so this is a safe escape hatch.

```bash
# Per client app (example: ConverseAI v4):
# 1) Portainer stack env: GATEWAY_BASE_URL=https://gateway-dev.ifixtelecom.com.br
# 2) Force-recreate the container so the new env is picked up:
ssh vps-ifix-vm 'cd /home/pedro/projetos/pedro/converseai-v4 && docker compose up -d --force-recreate converseai-api'
```

After Tier 3: schedule a follow-up window to fix the prod stack offline. The dev stack is NOT a permanent solution — Phase 11 will deprecate `/opt/ai-gateway-dev/` once prod is hardened.

---

## Postmortem stub

Use this template for EVERY incident-driven deploy or rollback. Append to `gateway/docs/POSTMORTEMS/` (create if missing) as `YYYY-MM-DD-<slug>.md`. Phase 11 will formalize the postmortem template — until then, fill in:

- **Date:** YYYY-MM-DD HH:MM UTC
- **Trigger:** what woke the operator (alert, customer report, monitoring drift, scheduled cut)
- **Detection:** how was the issue confirmed (which `gatewayctl` / `psql` / Sentry query)
- **Mitigation:** which tier of Rollback was used (1/2/3) + wall-clock to mitigate
- **Recovery:** how full service was restored + verification step that confirmed it
- **Action items:** changes to this RUNBOOK, scripts, or upstream code to prevent recurrence (link to GitHub issues / PRs)

---

## Pitfall Index (cross-reference to RESEARCH §Pitfalls)

The following Pitfalls are cited inline in the procedure above. Read RESEARCH §Pitfalls for the full background; this table summarizes the one-line mitigation each procedure applies.

| ID | Theme | Mitigation in this RUNBOOK |
|----|-------|---------------------------|
| Pitfall 1 | network name `intra` (not `traefik-public`) | Step 1 + preflight.sh probe Section 3 |
| Pitfall 2 | schema name `ai_gateway` hardcoded → isolate by DB name | Step 2 (CREATE DATABASE, not CREATE SCHEMA) |
| Pitfall 3 | DNS before route → ACME never starts | Step 1 lands route FIRST; Step 6 DNS LAST |
| Pitfall 4 | certResolver literal `letsencrypt` (not `letsencryptresolver`) | artifacts/ai-gateway-prod.yml uses `letsencrypt` |
| Pitfall 5 | Sentry release tag — cut tag BEFORE deploy | Cut-Release Procedure step 5 precedes Roll-Forward |
| Pitfall 6 | VM 101 capacity — n8n cluster competes | preflight.sh aborts on disk > 80% |
| Pitfall 7 | dev + prod sharing OpenRouter/OpenAI keys → spend mixes | Step 1.2 — separate prod keys from `~/.claude/CLAUDE.md` |
| Pitfall 8 | `MIGRATE_ON_BOOT=true` left on → goose race | Step 4 flips off; ONLY Step 3 first-boot uses true |

---

## Related Documents

- `.planning/phases/10-prod-deploy-ai-gateway/10-CONTEXT.md` — decisions D-01..D-22 (operator-managed compose, separate DB, edge route, DNS ordering, cut-release timing, cascade-close pattern).
- `.planning/phases/10-prod-deploy-ai-gateway/10-RESEARCH.md` §How To #1..#10 — full deploy recipes; this RUNBOOK is the operator-facing distillation.
- `.planning/phases/10-prod-deploy-ai-gateway/10-PATTERNS.md` — RUNBOOK family analog (this file follows Pattern 4 — RUNBOOK shape).
- `gateway/docker-compose.prod.yml` + `gateway/.env.prod.example` — operator-deployed assets.
- `scripts/deploy/preflight.sh`, `scripts/deploy/bootstrap-postgres.sh`, `scripts/deploy/migrate-dashboard.sh`, `scripts/deploy/cf-dns-create.sh` — the 4 deploy scripts referenced step-by-step above.
- `.planning/phases/10-prod-deploy-ai-gateway/artifacts/ai-gateway-prod.yml` — edge Traefik file-provider entry (rsynced in Step 1).
- `gateway/docs/RUNBOOK-FAILOVER.md` — what to do when upstream goes OPEN after deploy.
- `gateway/docs/RUNBOOK-PRIMARY-POD.md` — Vast.ai primary pod troubleshooting.
- `gateway/docs/RUNBOOK-OBSERVABILITY-ALERTING.md` — Sentry + Prometheus dashboards.
- `gateway/docs/RUNBOOK-CLIENT-INTEGRATION.md` + `RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md` — what the 6 client apps need to consume `https://ai-gateway.converse-ai.app`.
