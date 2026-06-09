# Phase 11: prod-hardening — Research

**Researched:** 2026-05-27
**Domain:** Prod load testing (httpx replay), chaos engineering (Vast API + iptables), incident-runbook authoring (Google SRE), Better Auth 1.4 plugin hardening (twoFactor + built-in rate-limit), LGPD doc-only sign-off process
**Confidence:** HIGH

## Summary

Phase 11 endurece prod pós-Phase-10. Todas as decisões já estão locked em CONTEXT.md — replay audit_log dev sanitizado via httpx async, Vast API DELETE raw, iptables DROP egress, Google SRE blameless template, better-auth twoFactor + built-in rate-limit. A pesquisa **confirma codebase-truth e versão-truth** dos anchors citados em CONTEXT.md e bloqueia ambiguidades técnicas que vão emergir no plan-phase (e.g., DELETE Vast retorna 200/404 idempotente? OpenRouter está atrás de Cloudflare → iptables DROP por destination domain não funciona — precisa estratégia adaptada; twoFactor `~1.4.18` exporta o plugin em `better-auth/plugins`; better-auth tem rate-limit **built-in**, NÃO plugin separado).

**Primary recommendation:** O plano deve assumir 7 plans em 3 waves — Wave 1 paralelo (load-test infra + dashboard SSO + LGPD docs + per-env keys), Wave 2 sequencial (load-test execução → chaos PRD-02 → chaos PRD-03), Wave 3 (RUNBOOK + POSTMORTEM agregam dados das execuções). Phase 10 deferred items (`gatewayctl debug emit-error`, `gatewayctl key list`, `smoke-sensitive-failover.py` race fix, GHA retrigger) bundle em Wave 1 paralelo. SLO oficial v1.0 (D-04) é o anchor — toda evidência aponta pra ele.

## Architectural Responsibility Map

| Capability | Primary Tier | Secondary Tier | Rationale |
|------------|-------------|----------------|-----------|
| Load replay generation | CLI / Python (ops-claude) | — | D-05/D-06: standalone script disparado em VM separada via HTTPS, sem dependência runtime do gateway |
| Chaos primary kill | External API (Vast.ai) | — | D-07: gateway descobre por probe natural; controlador externo |
| Chaos tier-1 kill | OS (iptables n8n-ia-vm) | — | D-08: egress block ao nível network namespace do host docker; gateway breaker observation-driven |
| 2FA enroll/verify | API (Better Auth) | Browser (TOTP input) | D-12: betterAuth plugin instancia endpoints; React UI consome via authClient |
| Email allowlist | API (Better Auth signUp hook) | — | D-13: server-side validation; client UI mostra erro |
| Rate-limit /login | API (Better Auth built-in) | — | D-14: customRules path-specific, retorna X-Retry-After |
| Session hardening | API (Better Auth session config) | Browser (cookie attrs) | D-15: `expiresIn` server-side + cookie attrs default no Better Auth |
| Incident runbook | Doc (Markdown) | — | D-10/D-11: doc-only deliverable |
| LGPD sign-off | Doc (Markdown) + external legal | — | D-16/D-17: doc-only; assinatura é gate externo |
| gatewayctl debug emit-error | CLI (Go) | API handler (panic) | D-18.2: subcommand triggera handler `/admin/debug/panic` que panica dentro de Recoverer chain |
| gatewayctl key list | CLI (Go) | DB (Postgres) | D-18.3: read-only query em `ai_gateway.api_keys` |
| Per-env keys | Ops (operator + .env) | — | D-19: env vars em `/opt/ai-gateway-prod/.env`; rotação manual |

## Standard Stack

### Core (já instalado — Phase 11 não adiciona deps novas no gateway/python)

| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `better-auth` | `~1.4.18` | Auth framework com twoFactor + rate-limit built-in | [VERIFIED: dashboard/package.json] `better-auth/plugins/two-factor` shipa SHA-1 TOTP por default (compatível Google Authenticator/1Password) — verified em `node_modules/@better-auth/utils/dist/otp.mjs:12 hash = "SHA-1"` |
| `httpx` | `>=0.27.0` | Async HTTP client Python pra load replay | [VERIFIED: scripts/integration-smoke/requirements.txt L12] já em uso pelos smoke-*.py |
| `structlog` | `>=24.1.0` | Logger estruturado dos smokes | [VERIFIED: scripts/integration-smoke/requirements.txt L13] |
| `psycopg` | `>=3.1.0 (binary)` | Reader direto do audit_log (export script) | [VERIFIED: scripts/integration-smoke/requirements.txt L15] |
| `jsonschema` | `>=4.21.0` | Validação de report JSONL output | [VERIFIED: scripts/integration-smoke/requirements.txt L14] |

### Supporting (também já instalado)

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `getsentry/sentry-go` | (current go.mod) | Panic capture path | `gatewayctl debug emit-error` exercita via `httpx.Recoverer` → `sentry.CurrentHub().Recover()` |
| Go `text/tabwriter` stdlib | Go 1.24 | `gatewayctl key list` formatação tabela | Mesmo pattern de `admin-key list` (admin_key.go:264) |
| Go `flag` stdlib | Go 1.24 | Subcommand flags | Mesmo pattern de `key revoke` (key.go:100) |

### Alternatives Considered (já rejected via CONTEXT.md decisions)

| Instead of | Could Use | Why rejected (D-XX ref) |
|------------|-----------|-------------------------|
| `httpx async + custom script` | k6 / locust / vegeta | D-05: zero precedente no repo + perde timing audit_log original; vegeta usado só em Phase 10 S8 ad-hoc burst |
| `Google SRE blameless template` | 5-whys / Linear template | D-10: 5-whys superficializa multi-causa; Linear não usado na Ifix |
| `Vast API DELETE raw` | `gatewayctl primary force-down` / SIGKILL llama | D-07: graceful drain não testa "host yank" realista; SIGKILL é granular demais |
| `iptables DROP egress` | FORCED_OPEN breaker / fake DNS | D-08: FORCED_OPEN já validado Phase 10 S4 (não exercita trip natural); fake DNS connection-refused é fast-fail, não simula timeout |
| `better-auth twoFactor plugin` | speakeasy + custom flow / Authy SDK | D-12: já instalado em `~1.4.18`; reduz manutenção |

**Installation:** **NENHUMA nova dependência runtime.** Phase 11 reusa stack existente.

**Version verification:**

```bash
# Já no repo:
grep '"better-auth"' dashboard/package.json    # → ~1.4.18 ✓
cat scripts/integration-smoke/requirements.txt  # → httpx/structlog/psycopg/jsonschema ✓
```

## Package Legitimacy Audit

| Package | Registry | Age | Downloads | Source Repo | slopcheck | Disposition |
|---------|----------|-----|-----------|-------------|-----------|-------------|
| `better-auth` | npm | ~3 yrs (1.4.18 current) | ~500K/wk | github.com/better-auth/better-auth | [OK] | Approved (already installed) |
| `httpx` | PyPI | ~7 yrs (0.27.x) | ~50M/mo | github.com/encode/httpx | [OK] | Approved (already installed) |
| `structlog` | PyPI | ~12 yrs | ~10M/mo | github.com/hynek/structlog | [OK] | Approved (already installed) |
| `psycopg` | PyPI | ~3 yrs (v3) | ~5M/mo | github.com/psycopg/psycopg | [OK] | Approved (already installed) |
| `jsonschema` | PyPI | ~10 yrs | ~80M/mo | github.com/python-jsonschema/jsonschema | [OK] | Approved (already installed) |

**Packages removed due to slopcheck [SLOP] verdict:** none
**Packages flagged as suspicious [SUS]:** none

slopcheck 0.6.1 invocação confirmada 2026-05-27: `slopcheck install --ecosystem npm better-auth` + `slopcheck install --ecosystem pypi httpx structlog psycopg jsonschema` retornaram `[OK]` pra todos os 5 pacotes (4 já no PyPI desde 2018-2023; better-auth no npm desde 2024-09).

## Architecture Patterns

### System Architecture Diagram

```
PRD-01 Load Test:
  ops-claude (10.10.10.10)  → HTTPS Public NAT  → vps-ifix-vm:443 (edge Traefik)
       ↓                                              ↓
   load-replay.py                              ai-gateway-prod.yml route
   (asyncio + httpx                                   ↓
   + audit_log JSONL                          n8n-ia-vm:8080 (host-port bypass)
   replay)                                            ↓
                                              ai-gateway container (PROD)
                                                     ↓
                                       ┌─────────────┴─────────────┐
                                       ↓                           ↓
                                tier-0 (Vast 2×3090)         tier-1 (OpenRouter/OpenAI)

PRD-02 Chaos primary kill (DURING load):
  ops-claude → Vast API DELETE /api/v0/instances/{id}/  → host yank
       ↓                                                       ↓
       (curl Bearer)                                    gateway probe timeout
                                                              ↓
                                                       breaker `local-llm` OPEN
                                                       (observation, NOT force-open)
                                                              ↓
                                                       FSM Ready → Draining → Asleep
                                                              ↓
                                                       tier-1 takes over

PRD-03 Chaos OpenRouter down (DURING load):
  ssh n8n-ia-vm:                                       gateway probe timeout
   iptables -I OUTPUT -d <or.IP/IP-range> -j DROP    →      ↓
                                                       breaker `openrouter-chat` OPEN
                                                              ↓
                                                       For data_class=normal: try tier-2 fallback (OpenAI direct)
                                                       For data_class=sensitive: 503 sensitive_block (RES-08)

PRD-06 Dashboard SSO hardening:
  Browser → ai-dashboard.converse-ai.app (Next.js 15)
       ↓
   Better Auth instance (dashboard/src/lib/auth.ts)
       ↓
   plugins: [ twoFactor({ issuer: "Ifix AI Gateway" }) ]
   rateLimit: { customRules: { "/sign-in/email": { window: 900, max: 5 } } }
   emailAndPassword: { ..., autoSignIn: false, signUp: { ..., hook: allowlistDomain } }
   session: { expiresIn: 1800, freshAge: 60 }   ← idle 30min (D-15)
       ↓
   Drizzle adapter → DO Postgres bd_ai_dashboard_prod
   (twoFactor + backupCodes tables auto-migrated via `npx auth@latest migrate`)

PRD-04 Incident Runbook:
  gateway/docs/RUNBOOK-INCIDENTS.md (4 classes — D-11)
       ↓ cross-refs
  RUNBOOK-FAILOVER.md / RUNBOOK-PRIMARY-POD.md / RUNBOOK-OBSERVABILITY-ALERTING.md / RUNBOOK-QUOTAS-BILLING.md
  POSTMORTEM-TEMPLATE.md (Google SRE blameless 9-section)

D-18.2 Panic-path:
  curl <admin>:/admin/debug/panic    OR   gatewayctl debug emit-error
       ↓                                                      ↓
       handler `panic("synthetic")`                  (same handler invoked)
       ↓
   httpx.Recoverer (gateway/internal/httpx/recoverer.go:18-30)
       ↓
   sentry.CurrentHub().Recover(rec) + sentry.Flush(500ms)
       ↓
   Sentry project ifix-ai-gateway-prod (id 4511455942017024) event lands
```

### Recommended Project Structure

```
gateway/cmd/gatewayctl/
├── debug.go              # NET-NEW: runDebug → runDebugEmitError (D-18.2)
├── key.go                # EDIT: + runKeyList subcommand (D-18.3)
├── key_test.go           # NET-NEW: runKeyList unit test
└── main.go               # EDIT: register "debug" + extend "key" usage

gateway/internal/httpx/  (or /admin)
└── debug_panic.go        # NET-NEW: HTTP handler /admin/debug/panic (operator-only, X-Admin-Key gated)

gateway/docs/
├── RUNBOOK-INCIDENTS.md           # NET-NEW: 4 classes (D-11)
├── POSTMORTEM-TEMPLATE.md         # NET-NEW: Google SRE blameless (D-10)
├── LGPD-SIGNOFF-PROCESS.md        # NET-NEW: D-16
├── LGPD-SIGNOFF-LETTER-TEMPLATE.md # NET-NEW: D-16
├── RUNBOOK-DEPLOY.md              # EDIT: + GHA retrigger procedure (D-18.4) + per-env keys (D-19)
└── RUNBOOK-FAILOVER.md            # EDIT (light): cross-ref RUNBOOK-INCIDENTS

dashboard/src/lib/
├── auth.ts               # EDIT: + twoFactor + rate-limit + signUp allowlist + session expiresIn
├── auth-client.ts        # EDIT: + twoFactorClient plugin
├── schema.ts             # EDIT: + twoFactor + (optional) twoFactorEnabled column added by twoFactor plugin
└── allowlist.ts          # NET-NEW: domain check helper

dashboard/src/app/
├── 2fa/
│   ├── enroll/page.tsx           # NET-NEW (UI-SPEC step 1-3)
│   └── challenge/page.tsx        # NET-NEW (UI-SPEC challenge)
├── signup/page.tsx               # NET-NEW (allowlist-rejected message UI)
└── login/page.tsx                # EDIT: + rate-limit Alert + session-expired Alert

scripts/dashboard/
└── seed-admins.sh                # NET-NEW: manual provisioning 4 admins (D-13)

scripts/integration-smoke/
├── load-replay.py                # NET-NEW: PRD-01 (D-05)
├── load-replay-report-schema.json # NET-NEW: report contract
├── audit-log-export.py           # NET-NEW: sanitize audit_log dev → JSONL fixture
└── smoke-sensitive-failover.py   # EDIT: accept FORCED_OPEN as equiv natural-open (D-18.1)

.planning/load-test-fixtures/     # NET-NEW: gitignored — audit_log JSONL sanitized

.planning/legal/                  # NET-NEW: gitignored — signed PDF (D-16)
```

### Pattern 1: Reuse `gatewayctl` Subcommand Shape

**What:** New subcommands `debug`, `key list` seguem o pattern já estabelecido em `admin_key.go` (most canonical — single-file family `create/revoke/list`).

**When to use:** Para CADA novo subcommand do gatewayctl.

**Example (compactado):**

```go
// gateway/cmd/gatewayctl/debug.go (NEW)
// Source: mirrors admin_key.go:50-66 dispatcher pattern
package main

import (
    "context"
    "flag"
    "fmt"
    "log/slog"
    "net/http"
    "os"
)

func runDebug(ctx context.Context, args []string, log *slog.Logger) int {
    if len(args) == 0 {
        fmt.Fprintln(os.Stderr, "Usage: gatewayctl debug emit-error [flags]")
        return 2
    }
    switch args[0] {
    case "emit-error":
        return runDebugEmitError(ctx, args[1:], log)
    default:
        fmt.Fprintf(os.Stderr, "unknown debug subcommand: %s\n", args[0])
        return 2
    }
}

func runDebugEmitError(ctx context.Context, args []string, log *slog.Logger) int {
    fs := flag.NewFlagSet("debug emit-error", flag.ExitOnError)
    gwURL := fs.String("gateway", os.Getenv("AI_GATEWAY_URL"), "gateway base URL (env AI_GATEWAY_URL)")
    adminKey := fs.String("admin-key", os.Getenv("AI_GATEWAY_ADMIN_KEY"), "admin key (env AI_GATEWAY_ADMIN_KEY)")
    if err := fs.Parse(args); err != nil { return 2 }
    if *gwURL == "" || *adminKey == "" {
        fmt.Fprintln(os.Stderr, "error: --gateway and --admin-key required")
        return 2
    }
    // POST /admin/debug/panic — handler panics inside httpx.Recoverer chain.
    // Expect HTTP 500 + OpenAI envelope back ({"error":{"type":"api_error",...}}).
    req, _ := http.NewRequestWithContext(ctx, "POST", *gwURL + "/admin/debug/panic", nil)
    req.Header.Set("X-Admin-Key", *adminKey)
    resp, err := http.DefaultClient.Do(req)
    // ... assertion: status 500 + Sentry event should land in ~500ms (Flush budget)
}
```

### Pattern 2: Better Auth twoFactor Plugin Registration

**What:** Plugin instancia via `betterAuth({ plugins: [twoFactor({...})] })` no servidor + `createAuthClient({ plugins: [twoFactorClient()] })` no cliente. Schema do plugin (`twoFactor` table + `user.twoFactorEnabled` column) gerado por `npx auth@latest migrate`.

**Example (auth.ts edit):**

```typescript
// dashboard/src/lib/auth.ts
import { betterAuth } from "better-auth";
import { drizzleAdapter } from "better-auth/adapters/drizzle";
import { twoFactor } from "better-auth/plugins";
import { db, schema } from "./db";
import { isAllowedEmail } from "./allowlist";

export const auth = betterAuth({
  baseURL: process.env.BETTER_AUTH_URL,
  secret: process.env.BETTER_AUTH_SECRET,
  database: drizzleAdapter(db, { provider: "pg", schema }),
  emailAndPassword: {
    enabled: true,
    autoSignIn: false,  // D-12: never auto-sign in pré-enrollment
  },
  session: {
    expiresIn: 30 * 60,        // D-15: 30min (vs prior 7 days)
    updateAge: 5 * 60,         // refresh window
    cookieCache: { enabled: true, maxAge: 60 },
  },
  rateLimit: {                 // BUILT-IN, NOT plugin — [CITED: better-auth.com/docs/concepts/rate-limit]
    enabled: true,
    window: 60, max: 100,      // global default
    customRules: {
      "/sign-in/email": { window: 900, max: 5 },     // D-14: 5/15min/IP
      "/sign-up/email": { window: 900, max: 5 },
      "/two-factor/verify-totp": { window: 60, max: 5 },
    },
    storage: "secondary-storage", // Redis-backed if SECONDARY_STORAGE wired; else "memory"
  },
  plugins: [
    twoFactor({
      issuer: "Ifix AI Gateway",   // discretion: confirmed in CONTEXT.md specifics
      // digits: 6,    // default
      // period: 30,   // default — SHA-1 by default per @better-auth/utils/otp.mjs:12
    }),
  ],
  advanced: { database: { generateId: () => crypto.randomUUID() } },
  user: {
    additionalFields: {},
    // signUp hook (D-13): email allowlist
  },
  databaseHooks: {
    user: {
      create: {
        before: async (user) => {
          if (!isAllowedEmail(user.email)) {
            throw new Error("E-mail fora do allowlist @ifixtelecom.com.br");
          }
          return { data: user };
        },
      },
    },
  },
});
```

### Pattern 3: httpx Async Replay with Timing

**What:** `load-replay.py` herda imports + helpers da família `smoke-*.py`. JSONL input (sanitized audit_log) drive timing via `asyncio.sleep(delta_ms / 1000)` entre requests do mesmo `tenant_id`. JSONL output per-request + sumário P50/P95/P99 + counters por status_code.

**Example skeleton (sketch — planner expande):**

```python
# scripts/integration-smoke/load-replay.py
import asyncio, httpx, json, time, statistics
from pathlib import Path
from typing import AsyncIterator

async def replay_request(client: httpx.AsyncClient, gateway_url: str,
                          rec: dict, results: list) -> None:
    """One audit_log replay record → one HTTPS POST → metric record."""
    api_key = rec["_replay_api_key"]   # injected by orchestrator per tenant_id
    route   = rec["route"]              # /v1/chat/completions, /v1/embeddings, /v1/audio/transcriptions
    body    = rec["_sanitized_body"]    # PII-stripped (whisper payloads + tool args)
    headers = {"Authorization": f"Bearer {api_key}",
               "Content-Type": "application/json",
               "X-Idempotency-Key": rec["request_id"]}  # echo audit RID as idempotency
    t0 = time.monotonic()
    try:
        r = await client.post(gateway_url + route, headers=headers, content=body, timeout=60.0)
        elapsed_ms = int((time.monotonic() - t0) * 1000)
        results.append({"request_id": rec["request_id"],
                        "tenant": rec["tenant_slug"],
                        "route": route,
                        "status_code": r.status_code,
                        "elapsed_ms": elapsed_ms,
                        "upstream": r.headers.get("X-Upstream", "")})
    except Exception as e:
        elapsed_ms = int((time.monotonic() - t0) * 1000)
        results.append({"request_id": rec["request_id"],
                        "tenant": rec["tenant_slug"],
                        "route": route,
                        "status_code": -1,
                        "elapsed_ms": elapsed_ms,
                        "error": str(e)[:300]})

async def orchestrate(gateway_url: str, fixture_jsonl: Path,
                       duration_s: float, max_concurrency: int = 50) -> dict:
    """Read sanitized JSONL fixture, replay with original deltas, bounded concurrency."""
    sem = asyncio.Semaphore(max_concurrency)
    results: list = []
    async with httpx.AsyncClient(http2=True) as client:
        tasks = []
        t_start = time.monotonic()
        with fixture_jsonl.open() as f:
            for line in f:
                if time.monotonic() - t_start >= duration_s:
                    break
                rec = json.loads(line)
                # rec["_replay_delay_s"] = computed in export: ts - prev_ts
                await asyncio.sleep(rec.get("_replay_delay_s", 0))
                async def go(r):
                    async with sem:
                        await replay_request(client, gateway_url, r, results)
                tasks.append(asyncio.create_task(go(rec)))
        await asyncio.gather(*tasks)
    # Summary
    latencies_per_route = {}
    for x in results:
        latencies_per_route.setdefault(x["route"], []).append(x["elapsed_ms"])
    summary = {}
    for route, lats in latencies_per_route.items():
        lats.sort()
        n = len(lats)
        summary[route] = {
          "n": n,
          "p50": lats[n // 2] if n else 0,
          "p95": lats[int(n * 0.95)] if n else 0,
          "p99": lats[int(n * 0.99)] if n else 0,
        }
    return {"summary": summary, "results_count": len(results),
            "error_count": sum(1 for r in results if r["status_code"] >= 500 or r["status_code"] < 0)}
```

### Pattern 4: Vast API DELETE Idempotent

**What:** `curl -X DELETE https://console.vast.ai/api/v0/instances/{id}/ -H "Authorization: Bearer $VAST_API_KEY"`. Success body: `{"success": true}`. **Idempotency on 404 unverified** — research couldn't confirm (only docs example shows the happy path).

**Recommendation for chaos:** Treat 200 = killed, 404 = already gone (idempotent OK), 5xx = retry once com 2s sleep. Cleanup post-chaos: re-run `gatewayctl primary force-up` se a FSM ficou stuck em Draining/Asleep.

### Anti-Patterns to Avoid

- **iptables DROP por destination domain (`-d openrouter.ai`):** kernel resolve DNS apenas no momento da regra; OpenRouter está atrás de Cloudflare (verified via WebSearch — OpenRouter uses Cloudflare Workers) → IPs rotacionam. Use **um destes 3 patterns:**
  1. `dig +short openrouter.ai` no momento → DROP por IP (curto prazo, pode quebrar mid-test se CF rota mudar — aceito pra 30min window)
  2. `iptables -I OUTPUT -p tcp --dport 443 -m string --string "openrouter.ai" --algo bm -j DROP` (deep packet inspection no SNI plain) — mais robusto
  3. `iptables -I OUTPUT -m owner --uid-owner <gateway_uid> -p tcp --dport 443 -j REJECT` filtrado por hash de SNI via `iptables-extensions`
  **Preferred:** option 1 (simples), com nota no runbook que regra DEVE ser refrescada a cada teste (DNS resolve right before).
- **`gatewayctl primary force-down` para PRD-02:** drain graceful; perde a "host yank" realista. Use Vast DELETE raw (D-07 já locked).
- **`smoke-sensitive-failover.py` polling `state=="open"`:** breaker oscila open↔half-open natural; o smoke trata só "open" exato. Fix (D-18.1): aceitar `{"open", "FORCED_OPEN", "forced-open"}` como equivalentes; usar `last_state in {open*}` matcher.
- **Hard-coded `customRules` paths errados:** Better Auth path canônico é `/sign-in/email` (NÃO `/login`). [CITED: better-auth.com/docs/concepts/rate-limit] — UI-SPEC mostra "/login" como rota Next.js, mas a rota Better Auth interna é `/api/auth/sign-in/email`. Mapping correto: `customRules: { "/sign-in/email": {...} }`.
- **`autoSignIn: true` antes de twoFactor enroll forced:** usuário criado via signUp logaria sem TOTP. Force `autoSignIn: false` + enroll-redirect no first sign-in (Phase 11 middleware extension).
- **POSTMORTEM template sem `Lessons`:** Google SRE blameless **sempre** inclui Lessons separado de Action Items — lições vs deliverables.

## Don't Hand-Roll

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| TOTP generation/verification | Custom HOTP via crypto.HMAC | `better-auth/plugins twoFactor` | Edge cases: clock skew tolerance, anti-replay, backup codes encryption, base32 secret encoding |
| /login rate-limit | Custom Redis Lua + middleware | Better Auth built-in `rateLimit.customRules` | Already X-Retry-After + storage abstraction (memory/db/secondary); ~1.4.18 covers /sign-in/email + /two-factor/verify-totp natively |
| Backup codes generation | crypto.randomBytes + custom hash | `authClient.twoFactor.generateBackupCodes()` | One-time encryption + auto-consume on use + DB schema managed |
| Load generator timing/concurrency | Multi-process Python threading | `asyncio.Semaphore + httpx.AsyncClient` | Single-threaded asyncio sufficient pra ~50 conc requests; threading adds GIL serialization on httpx |
| Postmortem template | Inline ad-hoc per incident | Google SRE blameless 9-section markdown skeleton | Industry standard; allows cross-postmortem trend analysis later |
| Vast API client em Python | Hand-rolled httpx wrapper for one DELETE | Inline `curl -X DELETE` em shell command no runbook | Single endpoint; full Go client em `gateway/internal/vast/` é overkill pra chaos hook |
| Email domain validation | Regex hand-rolled | `email.split("@")[1].lower() == "ifixtelecom.com.br"` | Phase 11 não suporta subdomains/aliases; simple equality cobre 4 admins (no risk of bypass) |

**Key insight:** Phase 11 é deliberadamente **construção zero**. Stack está fechado (PROJECT.md Phase 06.9). Toda decisão técnica reusa código existente (smoke-*.py family, gatewayctl subcommand shape, audit_log schema, Better Auth instance). A única coisa que precisa de "scaffolding" é doc estrutural (RUNBOOK-INCIDENTS + POSTMORTEM + LGPD-SIGNOFF).

## Runtime State Inventory

> **Phase 11 has both rename-like changes AND runtime state changes** — D-19 introduces new OR/OpenAI keys, D-12 adds twoFactor table, D-18 adds gatewayctl subcommands. Inventory below.

| Category | Items Found | Action Required |
|----------|-------------|------------------|
| Stored data | (1) `dashboard_auth.twoFactor` table + `twoFactor_backupCodes` (added by `npx auth@latest migrate` on first deploy of new auth.ts). (2) `dashboard_auth.user.twoFactorEnabled` column (added by twoFactor plugin schema). (3) `ai_dashboard_prod.session.expiresAt` rows existing — operator must purge stale sessions OR accept they auto-expire on next idle (7 days max). (4) `ai_gateway_prod.audit_log` rows continuam (load test adiciona ~mil rows / 30min). | Run `npx auth@latest migrate` in `dashboard/` against `DASHBOARD_DATABASE_URL` pointing at prod DSN (mode 600 .env temp) BEFORE first dashboard container restart with new image. NO data migration needed; new columns default NULL. |
| Live service config | (1) Vast.ai dashboard: NO label change needed (Vast API doesn't support per-key labels — confirm via API docs check). (2) OpenRouter dashboard: create new API key with label `env=prod`; revoke key shared dev/prod after rotation. (3) OpenAI dashboard: same shape — new key labeled `env=prod`. | Operator manual via 3 web UIs. Doc procedure in RUNBOOK-DEPLOY.md (D-19). |
| OS-registered state | (1) `/opt/ai-gateway-prod/.env` on n8n-ia-vm — `UPSTREAM_LLM_OPENROUTER_AUTH_BEARER`, `UPSTREAM_STT_OPENAI_AUTH_BEARER`, `UPSTREAM_EMBED_OPENAI_AUTH_BEARER` swap to new prod keys. (2) iptables rules added during PRD-03 chaos — MUST be cleaned up via `iptables -D OUTPUT <same rule>` (rule numbering shifts; use `iptables -L OUTPUT --line-numbers` then `-D OUTPUT <N>`). (3) Vast.ai instances spawned during PRD-02 chaos — if `BestEffortDestroy` is not invoked manually, billing continues. | `.env` patch via `ssh n8n-ia-vm + sudoedit`. iptables cleanup hardcoded in chaos plan task. Vast cleanup explicit `curl -X DELETE` after PRD-02. |
| Secrets/env vars | (1) `OPENROUTER_API_KEY` rotates per D-19; SOPS not used here (raw mode 600 .env). (2) `BETTER_AUTH_SECRET` UNCHANGED (rotation would invalidate all sessions — Phase 11 explicit NOT to rotate). (3) New env var `DASHBOARD_ALLOWED_EMAIL_DOMAINS=ifixtelecom.com.br` recommended (config-driven allowlist instead of hardcoded). | OR + OpenAI key rotation procedure. Add `DASHBOARD_ALLOWED_EMAIL_DOMAINS` to `dashboard/.env.example` + prod env. |
| Build artifacts | (1) `ghcr.io/ifixtelecom/ifix-ai-gateway:v1.0.0` ainda NÃO existe no registry (Phase 10 deferred — D-18.4). After GHA retrigger workflow lands, image tagged `:v1.0.0` should appear. (2) `ghcr.io/ifixtelecom/ifix-ai-dashboard:v1.0.0` — same situation. (3) `node_modules/` reinstall NOT required — better-auth twoFactor plugin already shipped in `~1.4.18` installed; only `auth.ts` change. | Run GHA retrigger workflow (D-18.4 fix). Confirm `:v1.0.0` images land in GHCR before flipping prod compose. |

**Nothing found in category:** none — every category has Phase 11 items.

## Common Pitfalls

### Pitfall 1: Sensitive content in load replay
**What goes wrong:** audit_log export contains real tool-call args + Whisper audio metadata; replay against prod gateway re-injects PII into request body → re-writes to audit_log_content + re-billed.
**Why it happens:** "Sanitization" stops at fields like `prompt.content`, missing nested `tool_calls[].function.arguments` (often free-text user data) or `audio.filename`.
**How to avoid:**
- Export script must replace `tool_calls[].function.arguments` with `{"_replay_placeholder": true}`.
- Whisper multipart `file` part must be replaced with a known-safe stub WAV (use `fixtures/whatsapp-audio.opus` from Phase 8 smokes — already PII-free).
- For replays of `sensitive` tenants: SKIP entirely OR use a synthetic tenant key with normal class.
**Warning signs:** Replay run shows `audit_log_content` rows for sensitive tenants (should be 0).

### Pitfall 2: Vast DELETE during PRD-02 wins the race before gateway probes
**What goes wrong:** DELETE returns 200 in ~500ms; gateway's natural probe interval is ~5-10s; for the first 5s clients ainda routam pra tier-0 e tomam 503 connection-refused (NOT invisible failover).
**Why it happens:** "Invisible failover" depends on probe-driven breaker open BEFORE next request fans out — the chaos test inject window between DELETE and breaker observation generates real 5xx visible to client.
**How to avoid:**
- Pass criteria D-04 allows `<1% non-503 errors`; ~5s × 5-10 RPS ≈ 25-50 errors (acceptable as 503 transient, not 5xx panic).
- Measure separately: "invisible-during-stable" (P95 + error rate baseline) vs "blip-during-cutover" (count 503s during 10s window after DELETE).
- Document the expected blip in RUNBOOK-INCIDENTS class 1 (NOT a regression).
**Warning signs:** Error rate ≥1% non-503 during chaos window → real regression in probe loop or breaker FSM, NOT chaos-expected blip.

### Pitfall 3: OpenRouter Cloudflare IP rotation breaks iptables DROP mid-test
**What goes wrong:** `iptables -I OUTPUT -d 104.18.X.Y -j DROP` works for 1-5 minutes; CF rotates assignment; gateway recovers tier-1 mid-test invalidating chaos.
**Why it happens:** OpenRouter is fronted by Cloudflare (verified: `dig openrouter.ai` returns CF range 104.18.0.0/16 + 172.64.0.0/13).
**How to avoid:**
- Resolve `dig +short openrouter.ai` THEN apply iptables for **specific resolved IPs** + `iptables -I OUTPUT -d 104.18.0.0/15 -j DROP` cobre maioria do CF range (broad enough to cover rotation).
- Better: use `iptables -m string --string "openrouter" --algo bm` SNI-pattern DROP (TLS ClientHello plain SNI byte match) — survives IP rotation BUT may DROP other CF-fronted services. Document this tradeoff.
- Alternative (less brittle): set `UPSTREAM_LLM_OPENROUTER_URL=https://127.0.0.1:65535/api` temporarily, restart gateway, observe probe-timeout-driven OPEN. Doesn't simulate "OpenRouter degraded" as faithfully but completely deterministic.
**Warning signs:** During 30min chaos, mid-test the gateway's `/v1/health/upstreams` shows `openrouter-chat` flipping back to `closed` without operator intervention → DNS resolution moved to non-DROPped IP.

### Pitfall 4: Better Auth twoFactor enrollment redirect loop
**What goes wrong:** User logs in successfully → middleware redirects to `/2fa/challenge` (because `session.twoFactorVerified === false`) → user has never enrolled → no TOTP secret → endless loop OR generic 500.
**Why it happens:** Phase 11 enables `twoFactor` but doesn't gate enrollment via dashboard sidebar nav — user gets a fresh session with `twoFactorEnabled === false` and middleware logic must distinguish "must enroll" from "must challenge".
**How to avoid:**
- Middleware logic (UI-SPEC says it's a two-stage check): (1) cookie present, (2) IF `user.twoFactorEnabled === false` → redirect to `/2fa/enroll`, IF `true` AND `!session.twoFactorVerified` → redirect to `/2fa/challenge`.
- Use the `customSession` Better Auth hook to inject `twoFactorEnabled` into session payload so middleware can read it without a DB hit (use `cookieCache: { enabled: true, maxAge: 60 }`).
**Warning signs:** New admin signed in → infinite redirect 307 chain.

### Pitfall 5: Stale sessions after `expiresIn` reduction (D-15)
**What goes wrong:** Existing logged-in admins have sessions stamped `expiresAt` = `created_at + 7 days`. After D-15 changes `expiresIn` to 30min, those existing rows still report `expiresAt` in the future → sessions persist >30min until DB row expires.
**Why it happens:** `expiresIn` only affects NEW sessions; existing rows in `dashboard_auth.session` are not retroactively rewritten.
**How to avoid:**
- Documented in operator step: `DELETE FROM dashboard_auth.session WHERE expires_at > NOW() + INTERVAL '30 minutes'` once after deploy (forces re-login of all 4 admins; coordinated downtime <1min).
- OR: live with the 7-day legacy session and let it expire naturally — accepted because Phase 11 is starting from clean prod (Phase 10 only ~2 days live).
**Warning signs:** Admin reports "I didn't get logged out at 30min" after Phase 11 deploy — verify row in `session` table has post-deploy `created_at`.

### Pitfall 6: GHA retrigger workflow can't bypass tag-SHA dedup
**What goes wrong:** Tag `v1.0.0` and `develop` tip point to same SHA; GitHub treats workflow event as already-run; even `workflow_dispatch` with `ref: v1.0.0` may dedupe via cache.
**Why it happens:** GitHub Actions cancels duplicate concurrent runs by `github.workflow + github.ref` (see `concurrency: group: ${{ github.workflow }}-${{ github.ref }}`); ref `refs/tags/v1.0.0` vs `refs/heads/develop` are different group keys so this is fine — but cache reuse confuses operators.
**How to avoid:**
- Use `workflow_dispatch` with `inputs.tag=v1.0.0` (build-gateway.yml already wires this — line 18-23 verified).
- Force re-build: `gh workflow run build-gateway.yml --ref v1.0.0 -f tag=v1.0.0`.
- If dedup-suspected: `git tag -d v1.0.0 && git push origin :refs/tags/v1.0.0 && git tag -a v1.0.0 develop-tip-sha -m '...' && git push origin v1.0.0` — generates a new tag push event.
**Warning signs:** `gh run list --limit 5 --workflow build-gateway.yml` shows no run for the tag ref after push.

### Pitfall 7: better-auth migrate runs against wrong DB
**What goes wrong:** `npx auth@latest migrate` reads from `BETTER_AUTH_URL` and `DATABASE_URL` env vars in `dashboard/.env` — operator runs it locally on ops-claude pointing at DEV DSN, not PROD.
**Why it happens:** Migration command is environment-driven; operator must explicitly point at prod for the prod schema.
**How to avoid:**
- Step in RUNBOOK-DEPLOY.md (Phase 11 addition): "Migrate prod dashboard schema": `cd dashboard && DASHBOARD_DATABASE_URL=$PROD_DSN npx auth@latest migrate`.
- Wait, actually — the prod stack already had a successful initial migrate in Phase 10 (`bd_ai_dashboard_prod` created + `migrate-dashboard.sh` ran). For Phase 11 we just need to migrate the NEW twoFactor + twoFactor_backupCodes tables. Same script + same env. Verify in `migrate-dashboard.sh` that it re-reads the updated `schema.ts`.
**Warning signs:** Dashboard container starts; sign-in works; TOTP enroll endpoint returns 500 with `relation "twoFactor" does not exist`.

### Pitfall 8: smoke-sensitive-failover.py race fix accidentally accepts CLOSED
**What goes wrong:** Fix relaxes `last_state == "open"` to `last_state in {"open", "forced-open", "FORCED_OPEN"}`; bug: typo `"closed"` or `"half-open"` slipped into accepted set → false GREEN even with healthy breaker.
**Why it happens:** Multi-value comparison ergonomics — easy to add too many states.
**How to avoid:**
- Whitelist explicit: `OPEN_LIKE_STATES = frozenset({"open", "forced-open", "FORCED_OPEN"})`.
- Add unit test in `smoke-sensitive-failover.py` test suite: `assert "closed" not in OPEN_LIKE_STATES` (defensive).
**Warning signs:** Smoke passes against a healthy gateway (no chaos applied) — should NEVER happen.

## Code Examples

### Better Auth twoFactor server-side enroll endpoint usage (verified vs node_modules)

```typescript
// dashboard/src/app/2fa/enroll/page.tsx (NEW)
// Source: better-auth/dist/plugins/two-factor/index.mjs:90-95 (read 2026-05-27)
"use client";
import { useState } from "react";
import { authClient } from "@/lib/auth-client";

export default function EnrollPage() {
  const [step, setStep] = useState<"qr"|"verify"|"backup">("qr");
  const [secret, setSecret] = useState("");
  const [qrURI, setQrURI] = useState("");
  const [backupCodes, setBackupCodes] = useState<string[]>([]);

  // Step 1: client requests TOTP URI (server generates secret + stores it)
  const generate = async (password: string) => {
    const res = await authClient.twoFactor.enable({ password });  // SHA-1 by default
    setQrURI(res.data?.totpURI ?? "");
    setSecret(res.data?.secret ?? "");
    setBackupCodes(res.data?.backupCodes ?? []);  // backupCodes returned on enable
    setStep("qr");
  };

  // Step 2: verify code from app → confirms enrollment
  const verify = async (code: string) => {
    const res = await authClient.twoFactor.verifyTotp({ code });  // sets session.twoFactorVerified=true
    if (res.error) { /* show error */ return; }
    setStep("backup");
  };
}
```

### Email allowlist via `databaseHooks.user.create.before`

```typescript
// dashboard/src/lib/allowlist.ts (NEW)
const ALLOWED = (process.env.DASHBOARD_ALLOWED_EMAIL_DOMAINS ?? "ifixtelecom.com.br")
  .split(",").map(s => s.trim().toLowerCase());

export function isAllowedEmail(email: string): boolean {
  const at = email.lastIndexOf("@");
  if (at < 0) return false;
  return ALLOWED.includes(email.slice(at + 1).toLowerCase());
}
```

### iptables DROP cleanup helper (chaos plan task)

```bash
# scripts/chaos/openrouter-iptables-drop.sh (NEW — recommended; planner discretion)
# Run on n8n-ia-vm via ssh. Idempotent insert + numbered cleanup.
set -euo pipefail
ACTION="${1:?usage: $0 apply|cleanup}"
DOMAIN="openrouter.ai"

if [[ "$ACTION" == "apply" ]]; then
  # Resolve current Cloudflare-fronted IPs for openrouter.ai
  IPS=$(dig +short "$DOMAIN" | sort -u)
  if [[ -z "$IPS" ]]; then echo "no IPs resolved for $DOMAIN" >&2; exit 1; fi
  # Tag rules with --comment for later cleanup
  for ip in $IPS; do
    sudo iptables -I OUTPUT 1 -d "$ip" -p tcp --dport 443 -j DROP -m comment --comment "phase11-chaos-openrouter"
  done
  echo "applied $(echo "$IPS" | wc -l) DROP rules"
  # Also DROP CF wider ranges for safety (rotation buffer):
  sudo iptables -I OUTPUT 1 -d 104.18.0.0/15 -p tcp --dport 443 -j DROP -m comment --comment "phase11-chaos-openrouter"
  sudo iptables -I OUTPUT 1 -d 172.64.0.0/13 -p tcp --dport 443 -j DROP -m comment --comment "phase11-chaos-openrouter"
elif [[ "$ACTION" == "cleanup" ]]; then
  # Remove all comment-tagged rules. Order matters — delete by exact match.
  while sudo iptables -S OUTPUT | grep -q "phase11-chaos-openrouter"; do
    RULE=$(sudo iptables -S OUTPUT | grep -m1 "phase11-chaos-openrouter" | sed 's/^-A/-D/')
    sudo iptables $RULE
  done
  echo "all phase11-chaos-openrouter rules removed"
fi
```

### Vast DELETE chaos hook (plan task body)

```bash
# Get current primary lifecycle instance ID
INSTANCE_ID=$(ssh n8n-ia-vm \
  'docker exec ifix-ai-gateway /gatewayctl primary state' \
  | grep -oP 'vast_instance_id=\K\d+')

if [[ -z "$INSTANCE_ID" ]]; then echo "no active primary lifecycle"; exit 1; fi

# DELETE raw. 200 = killed; 404 = idempotent gone.
HTTP=$(curl -s -o /tmp/vast-delete.json -w '%{http_code}' \
  -X DELETE "https://console.vast.ai/api/v0/instances/${INSTANCE_ID}/" \
  -H "Authorization: Bearer $VAST_AI_API_KEY")
echo "vast delete status=$HTTP body=$(cat /tmp/vast-delete.json)"

# Wait for breaker to OPEN via natural observation (NOT force-open). Timeout 60s.
END=$(($(date +%s) + 60))
while (( $(date +%s) < END )); do
  STATE=$(curl -s 'https://ai-gateway.converse-ai.app/v1/health/upstreams' \
    | jq -r '.upstreams["local-llm"].state // "unknown"')
  echo "local-llm state=$STATE"
  if [[ "$STATE" == "open" ]]; then echo "natural-OPEN observed; chaos active"; break; fi
  sleep 5
done
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Better Auth rate-limit as separate plugin (community speculation pre-1.0) | rate-limit BUILT-IN since 1.0 | better-auth 1.0 (2024-Q3) | No `import { rateLimit } from "better-auth/plugins"` — it's a top-level `rateLimit: {...}` config |
| TOTP via `speakeasy` library + custom DB schema | better-auth `twoFactor` plugin manages secret + backup codes + URI generation | better-auth 1.0 | Drop ~250 LOC custom code |
| iptables `-d <domain>` (deprecated since iptables 1.6+ silently doesn't resolve on apply) | Resolve domain at script time + apply by IP/CIDR | iptables 1.6 (2016) | DNS resolution must happen in userspace explicitly |
| Vast.ai HTTP API path `console.vast.ai/api/v0/instances/{id}` | Same — stable across the project's Phase 6/06.6/06.8 lifecycle | (no change) | Continue with current `gateway/internal/vast/` client patterns where applicable |
| audit_log Accept-Encoding gzip bug | Fixed via `r.Header.Del("Accept-Encoding")` in BuildDirector | commit 5bd79d1 (2026-05-26 Phase 10) | All Phase 11 load-test audit rows land cleanly; no gzip-magic-byte regression |

**Deprecated/outdated:**

- `--induce-failure-via=gatewayctl` mode in `smoke-sensitive-failover.py` (lines 307-327): the script's TODO comment says "gatewayctl has NO breaker force-open subcommand" — but Phase 06.9 Plan 04 LANDED `gatewayctl breaker force-open` (verified: `gateway/cmd/gatewayctl/breaker.go:117`). The smoke is stale. Phase 11 D-18.1 fix bundle: ALSO update the gatewayctl mode to actually invoke `gatewayctl breaker force-open --upstream=local-llm --ttl=300s` instead of erroring out.

## Validation Architecture

### Test Framework

| Property | Value |
|----------|-------|
| Framework | Go: stdlib `testing` 1.24 + `testcontainers-go` for integration. Python: `pytest` (smokes already use it). Dashboard: `vitest` 3.0 + `@testing-library/react` 16.2 (already in `dashboard/package.json`). |
| Config file | `gateway/go.mod`, `scripts/integration-smoke/requirements.txt`, `dashboard/vitest.config.ts` |
| Quick run command | Go: `cd gateway && go test ./cmd/gatewayctl/... -count=1 -race`. Dashboard: `cd dashboard && bun test`. Smoke: `python scripts/integration-smoke/<name>.py --help` (sanity) |
| Full suite command | `cd gateway && go test ./... -count=1 -race -timeout=5m && go test -tags=integration ./internal/integration_test/... -count=1 -timeout=10m`. Dashboard: `cd dashboard && bun test`. Smokes: live UAT (manual). |

### Phase Requirements → Test Map

| Req ID | Behavior | Test Type | Automated Command | File Exists? |
|--------|----------|-----------|-------------------|-------------|
| PRD-01 | Load test 30min sustained passes SLO v1.0 | manual-only (live UAT) | `python scripts/integration-smoke/load-replay.py --gateway-url https://ai-gateway.converse-ai.app --fixture /tmp/audit.jsonl --duration 1800 --out /tmp/load-report.json` | ❌ Wave 1 NET-NEW |
| PRD-02 | Kill primary mid-load: P95 + error rate stays within SLO; breaker `local-llm` reaches OPEN via observation | manual-only (live UAT) | `scripts/chaos/vast-delete.sh && python load-replay.py + jq` post-analysis | ❌ Wave 2 NET-NEW |
| PRD-03 | OpenRouter degraded: `data_class=normal` falls through OpenAI; `sensitive` hits 503 RES-08 | manual-only (live UAT) | `scripts/chaos/openrouter-iptables-drop.sh apply` + `smoke-sensitive-failover.py` | ❌ Wave 2 NET-NEW |
| PRD-04 | RUNBOOK-INCIDENTS.md exists and cross-refs 7 sibling runbooks for all 4 classes | linter (positive-assertion grep) | `grep -q "Primary pod down\|OpenRouter / OpenAI degraded\|Audit/billing pipeline broken\|Rate-limit / quota lockout" gateway/docs/RUNBOOK-INCIDENTS.md` | ❌ Wave 3 NET-NEW |
| PRD-05 | LGPD-SIGNOFF-PROCESS.md + LGPD-SIGNOFF-LETTER-TEMPLATE.md exist and reference 4 sub-processors | linter (grep) | `grep -q "Vast.ai\|OpenAI\|OpenRouter\|MinIO" gateway/docs/LGPD-SIGNOFF-*.md` | ❌ Wave 1 NET-NEW |
| PRD-06 | TOTP enroll + verify works end-to-end; rate-limit /sign-in/email returns 429 after 5 attempts; signUp rejects non-@ifixtelecom domains | unit + manual UAT | `cd dashboard && bun test` (unit) + manual login UAT | ❌ Wave 1 NET-NEW (unit tests for auth.ts plugin wiring) |
| PRD-06 (rate-limit) | After 5 failed /sign-in/email in 15min from same IP, response 429 with X-Retry-After | manual (live) | `for i in 1..6; do curl -s -X POST .../api/auth/sign-in/email -d ...; done` | ❌ Wave 3 manual UAT |
| D-18.1 | `smoke-sensitive-failover.py` accepts FORCED_OPEN | unit (in-script) | `python -c 'from scripts.integration_smoke.smoke_sensitive_failover import OPEN_LIKE_STATES; assert "FORCED_OPEN" in OPEN_LIKE_STATES'` | ❌ Wave 1 NET-NEW assertion |
| D-18.2 | `gatewayctl debug emit-error` → Sentry event lands in `ifix-ai-gateway-prod` within 5s | manual (live) + Sentry API verify | `gatewayctl debug emit-error && sleep 5 && curl Sentry API` | ❌ Wave 1 NET-NEW |
| D-18.3 | `gatewayctl key list` returns aligned table with key id + prefix + status | unit (in-process) | `go test ./gateway/cmd/gatewayctl/ -run TestRunKeyList -count=1` | ❌ Wave 1 NET-NEW |
| D-18.4 | `gh workflow run build-gateway.yml --ref v1.0.0 -f tag=v1.0.0` produces `:v1.0.0` image in ghcr.io | manual | `gh workflow run + docker pull ghcr.io/ifixtelecom/ifix-ai-gateway:v1.0.0` | ❌ Wave 1 manual (no automation needed — workflow_dispatch already wired) |
| D-19 | `/opt/ai-gateway-prod/.env` UPSTREAM_*_AUTH_BEARER values differ from `/opt/ai-gateway-dev/.env` | manual (ssh diff) | `diff <(ssh n8n-ia-vm 'grep AUTH_BEARER /opt/ai-gateway-prod/.env') <(ssh vps-ifix-vm 'grep AUTH_BEARER /opt/ai-gateway-dev/.env')` (must show diff) | ❌ Wave 1 manual |

### Sampling Rate

- **Per task commit:** `go test ./gateway/cmd/gatewayctl/... -count=1 -race` (`debug emit-error` + `key list` unit) + `cd dashboard && bun test` (auth.ts edits) + `python -m pytest scripts/integration-smoke/` if any added.
- **Per wave merge:** Full suite — `go test ./... -count=1 -race -timeout=5m && go test -tags=integration ./internal/integration_test/... -timeout=10m && cd dashboard && bun test`.
- **Phase gate:** Live UAT — `load-replay.py` runs against prod gateway 30min sustained, evidência salva em `11-VERIFICATION.md`. Chaos run with cleanup verified post-run. All RUNBOOK + POSTMORTEM docs exist + cross-refs pass grep. Dashboard 2FA UAT end-to-end (enroll → logout → login with TOTP).

### Wave 0 Gaps

- [ ] `gateway/cmd/gatewayctl/debug.go` + `debug_test.go` — covers D-18.2 (panic-path proof)
- [ ] `gateway/cmd/gatewayctl/key.go` extension (add `runKeyList`) + `key_test.go` extension — covers D-18.3
- [ ] `gateway/internal/admin/debug_panic.go` (or similar) — HTTP handler `/admin/debug/panic` gated by X-Admin-Key middleware, calls `panic("synthetic")` inside Recoverer chain
- [ ] `dashboard/src/lib/auth.ts` — twoFactor plugin + rateLimit customRules + session expiresIn change
- [ ] `dashboard/src/lib/allowlist.ts` + unit test
- [ ] `dashboard/src/app/2fa/enroll/page.tsx` + `dashboard/src/app/2fa/challenge/page.tsx` — UI per UI-SPEC
- [ ] `dashboard/src/app/login/page.tsx` extension (rate-limit Alert + session-expired Alert)
- [ ] `dashboard/src/middleware.ts` extension (two-stage check for twoFactorEnabled + twoFactorVerified)
- [ ] `dashboard/src/lib/schema.ts` extension (twoFactor plugin auto-adds `twoFactor` + `twoFactorEnabled` columns; verify schema imports plugin shapes)
- [ ] `scripts/integration-smoke/load-replay.py` + `load-replay-report-schema.json` + `audit-log-export.py` — covers PRD-01
- [ ] `scripts/chaos/vast-delete.sh` + `scripts/chaos/openrouter-iptables-drop.sh` — covers PRD-02/PRD-03 chaos hooks
- [ ] `scripts/dashboard/seed-admins.sh` — covers D-13 manual provisioning
- [ ] `scripts/integration-smoke/smoke-sensitive-failover.py` edit — covers D-18.1
- [ ] `gateway/docs/RUNBOOK-INCIDENTS.md` — covers PRD-04 (D-11 four classes)
- [ ] `gateway/docs/POSTMORTEM-TEMPLATE.md` — covers PRD-04 (D-10 Google SRE blameless)
- [ ] `gateway/docs/LGPD-SIGNOFF-PROCESS.md` + `LGPD-SIGNOFF-LETTER-TEMPLATE.md` — covers PRD-05 (D-16)
- [ ] `gateway/docs/RUNBOOK-DEPLOY.md` extension — covers D-18.4 (GHA retrigger) + D-19 (per-env keys procedure)
- [ ] `.planning/load-test-fixtures/.gitignore` + `.planning/legal/.gitignore` — exclude fixtures + signed PDFs from git

## Security Domain

### Applicable ASVS Categories

| ASVS Category | Applies | Standard Control |
|---------------|---------|-----------------|
| V2 Authentication | yes | Better Auth `emailAndPassword` + `twoFactor` plugin (TOTP SHA-1 default, 6 digits, 30s period). Backup codes encrypted at rest by plugin. |
| V3 Session Management | yes | Better Auth session: `expiresIn=30min` (D-15), `cookieCache.maxAge=60s`, `Secure + HttpOnly + SameSite=Lax` defaults (verify `SameSite=strict` toggle). Session table FK CASCADE on user. |
| V4 Access Control | yes | dashboard middleware redirect: anon → /login; logged-in + no 2FA enrolled → /2fa/enroll; logged-in + enrolled + not verified → /2fa/challenge. signUp gated by email-domain allowlist. |
| V5 Input Validation | yes | better-auth uses Zod schemas internally for endpoints (verified `verifyTOTPBodySchema = z.object({...})` at `node_modules/better-auth/dist/plugins/two-factor/totp/index.mjs:14-18`). load-replay.py validates JSONL via jsonschema (existing smoke pattern). |
| V6 Cryptography | yes | Better Auth `BETTER_AUTH_SECRET` is HMAC key for cookie signing. TOTP secret encrypted with symmetric crypto (verified `import { symmetricDecrypt } from "../../../crypto/index.mjs"` at totp/index.mjs:1). **Never hand-roll** — re-using plugin. |
| V7 Error Handling | yes | OpenAI envelope error format in gateway (existing). Dashboard login error message generic ("E-mail ou senha inválidos") per Phase 07; matches OWASP recommendation. |
| V9 Communication | yes | TLS end-to-end via edge Traefik + LE cert (already live Phase 10). HSTS on `*.converse-ai.app` (Cloudflare default). |
| V10 Malicious Code | yes | slopcheck verified all packages OK 2026-05-27. No new deps. |
| V12 File Resources | partial | LGPD evidence PDFs gitignored at `.planning/legal/*.pdf`. Load-test fixtures gitignored at `.planning/load-test-fixtures/*.jsonl`. |

### Known Threat Patterns for the Phase 11 stack

| Pattern | STRIDE | Standard Mitigation |
|---------|--------|---------------------|
| TOTP code replay within window | Tampering | better-auth twoFactor stores last-used counter; reject re-use (plugin handles) |
| TOTP brute-force on /two-factor/verify-totp | Spoofing | rate-limit customRules `/two-factor/verify-totp: { window: 60, max: 5 }` (D-12 + D-14 combined) |
| Backup code re-use | Tampering | Plugin auto-consumes (each code one-time per docs); verify `node_modules/better-auth/dist/plugins/two-factor/backup-codes/` shape |
| Credential stuffing on /sign-in/email | Spoofing | rate-limit customRules `/sign-in/email: { window: 900, max: 5 }` per-IP (D-14); generic error message (V7) |
| Email allowlist bypass via header injection | Spoofing | databaseHooks.user.create.before runs server-side; client cannot bypass |
| Session fixation post-2FA | Spoofing | better-auth regenerates session ID on signIn (verified — `setSessionCookie` called at twoFactor verify endpoint) |
| Gateway panic exposing stack trace | Information Disclosure | `httpx.Recoverer` writes OpenAI envelope with generic message; full panic only to Sentry (verified gateway/internal/httpx/recoverer.go:26-28) |
| Audit log content leak via load replay | Information Disclosure | `audit-log-export.py` sanitizes tool_calls args + Whisper filename (Pitfall 1 above) |
| Chaos iptables left applied → permanent prod degradation | Denial of Service | Tagged rules with `--comment phase11-chaos-openrouter` + cleanup task in same plan (mandatory) |
| Vast.ai key leaking via shell history during chaos | Information Disclosure | Use env var `$VAST_AI_API_KEY` (already in ~/.claude/CLAUDE.md, gitignored), never inline literal token |
| GHA workflow_dispatch unauthorized | Spoofing | GitHub already gates workflow_dispatch by repo write permission; PAT in ~/.git-credentials (mode 600) |

## Project Constraints (from CLAUDE.md)

Verified directives apply to Phase 11:

- **GSD Workflow Enforcement:** No direct edits outside GSD; all Phase 11 changes via `/gsd:execute-phase 11`.
- **Communication Rules (NEVER speculative language):** Phase 11 plans + tasks must avoid "provavelmente", "geralmente", "talvez". Verifiable assertions only.
- **Naming Patterns:** kebab-case files (`load-replay.py`, `vast-delete.sh`, `seed-admins.sh`), `.test.ts` test suffix (dashboard tests), `RUNBOOK-INCIDENTS.md` matches existing 7 runbooks naming.
- **Code Style (cobrancas-api inferred):** 2-space indent, single quotes — applies to Python smokes (existing convention) and dashboard TypeScript (2-space).
- **Hetzner topology (FROM Infra section):**
  - `ops-claude` (10.10.10.10) = load-test source — confirmed PRD-01 D-06
  - `n8n-ia-vm` (10.10.10.20) = where iptables DROP rule applies — confirmed PRD-03 D-08
  - `vps-ifix-vm` (10.10.10.30) = edge Traefik — confirmed PRD-01 traffic path
  - Vast API key in `~/.claude/CLAUDE.md` "Vast.ai API Key" — confirmed D-07
- **SSH Pattern:** `ssh n8n-ia-vm 'command'` direct (NOT `qm guest exec`) — applies to all chaos task commands.

## Phase Requirements

| ID | Description | Research Support |
|----|-------------|------------------|
| PRD-01 | Load test 3+ tenants concurrent using prod profile; P95 baseline | D-01..D-06 locked in CONTEXT.md; httpx asyncio pattern verified vs `scripts/integration-smoke/smoke-*.py` family; audit_log schema verified vs `gateway/db/migrations/0003_*.sql` |
| PRD-02 | Chaos kill primary mid-load; measure recovery; invisible failover SLO | D-07 (Vast API DELETE) verified vs `docs.vast.ai/api-reference/instances/destroy-instance`; natural breaker observation path verified vs `gateway/internal/breaker/` patterns |
| PRD-03 | Chaos OpenRouter down; validate sensitive-tenant 503 + normal-tenant fallthrough | D-08 (iptables DROP) — research-flagged Cloudflare-rotation gotcha (Pitfall 3); D-18.1 fix to smoke-sensitive-failover.py verified vs Phase 06.9 Plan 04 landing `gatewayctl breaker force-open` |
| PRD-04 (full) | Runbook documented (detection → diagnosis → rollback → postmortem) | D-10 (Google SRE blameless 9-section) + D-11 (4 classes: primary down, tier-1 degraded, audit/billing broken, rate-limit lockout); cross-ref pattern verified vs existing 7 runbooks |
| PRD-05 | LGPD review concluded before activating sensitive tenants | D-16/D-17 doc-only deliverable; verified existing `gateway/docs/LGPD-SUBPROCESSORS.md` + `LGPD-REVIEW-CHECKLIST.md` as base; net-new SIGNOFF-PROCESS + SIGNOFF-LETTER-TEMPLATE |
| PRD-06 | Dashboard SSO/Better Auth admin access hardened | D-12 (twoFactor verified @ 1.4.18) + D-13 (allowlist via databaseHooks) + D-14 (rate-limit built-in) + D-15 (session expiresIn 30min); UI-SPEC complete and ready for executor |

## Assumptions Log

| # | Claim | Section | Risk if Wrong |
|---|-------|---------|---------------|
| A1 | Vast API DELETE returns 200/404 idempotently (404 acceptable as "already gone") | Architecture Patterns Pattern 4 | If DELETE returns 5xx for already-deleted instances, chaos cleanup script must retry with 2s sleep. Mitigation: docs.vast.ai doesn't specify — verify in first PRD-02 chaos rehearsal |
| A2 | OpenRouter IPs concentrate in CF ranges `104.18.0.0/15` + `172.64.0.0/13` | Pitfall 3 | If openrouter.ai uses additional CF subnets at chaos time, the broad CIDR DROP may miss some — gateway recovers mid-test. Mitigation: `dig +short` resolves current IPs at runtime; broad CIDRs are insurance |
| A3 | `npx auth@latest migrate` auto-adds twoFactor schema when plugin loaded | Pitfall 7 | If CLI doesn't pick up plugin schema, ALTER TABLE manual fallback needed. Mitigation: dashboard/src/lib/schema.ts pattern already used; CLI runs against same schema entry-point |
| A4 | Cleanup of legacy 7-day sessions can be done via `DELETE FROM session WHERE expires_at > NOW() + 30min` without breaking concurrent admin work | Pitfall 5 | If admins are mid-action, force-logout disrupts. Mitigation: only 4 admins, coordinate via WhatsApp before SQL |
| A5 | Existing dashboard auth.ts location/structure unchanged since Phase 07 (`emailAndPassword: { enabled: true }` baseline) | Pattern 2 | If Phase 10 added any plugins (unlikely — code read confirmed empty plugins), edits could conflict. Mitigation: file read 2026-05-27 confirmed no plugins, only emailAndPassword + session 7d |
| A6 | Better Auth twoFactor plugin's `databaseHooks` mechanism is compatible with the existing `drizzleAdapter` schema (twoFactor table auto-created on migrate without schema.ts edit) | Wave 0 Gaps schema.ts line | If plugin requires manual schema.ts update, the dashboard migrate step will fail. Mitigation: better-auth/plugins/two-factor/schema.d.mts defines fields shape; CLI migrate handles auto-discovery |
| A7 | Spend $5 absolute cap covers PRD-01 (~$0.30) + PRD-02 chaos cycle ($0.30 primary re-up post-kill) + retries ($0.50) + contingency | Specifics in CONTEXT.md | If chaos requires 3+ primary spin-cycles, cap blown. Mitigation: operator-aborts at $5 documented as Phase 11 hard gate |
| A8 | Phase 11 introduces NO new gateway/Go dependencies (only edits + new docs + new scripts) | Standard Stack | If twoFactor plugin transitively requires a peer dep not in `~1.4.18`, install needed. Mitigation: node_modules verified to ship plugin at `dist/plugins/two-factor/` 2026-05-27 |

**If this table is empty:** All claims in this research were verified or cited — no user confirmation needed.

*(This table is NOT empty; planner must keep these assumptions visible. Most are LOW-impact (A1/A2/A3/A4 have clear mitigations); A6 is the highest-risk because if wrong, dashboard deploy breaks — planner should add a verification task: `cd dashboard && DASHBOARD_DATABASE_URL=$STAGE_DSN npx auth@latest migrate --dry-run` before the prod migrate.)*

## Open Questions

1. **Should load-replay.py replay sensitive-tenant audit_log records?**
   - What we know: D-01 says "replay audit_log dev sanitizado"; D-02 says tier-0+tier-1 mix; CONTEXT specifics say PII placeholders for non-deterministic bytes.
   - What's unclear: If we replay `telefonia/cobrancas` records with sanitized bodies, do they still trip RES-08 `blocked_sensitive` paths during chaos? (Yes — the data_class is on the tenant, not the body.) So sensitive replay during PRD-02 chaos = additional invariant proof. But during PRD-01 baseline (no chaos), sensitive tenants would all go tier-0 anyway (Vast UP) — adds no signal, costs PII handling complexity.
   - Recommendation: Phase 11 plans should SKIP sensitive tenants in PRD-01 baseline load replay, INCLUDE them only in PRD-02/PRD-03 chaos windows where their distinct 503 behavior is the proof.

2. **Should load-replay.py respect original audit_log inter-request timing or batch-fire at fixed RPS?**
   - What we know: D-03 says "30min sustained, replay janela de pico real (~14-15 BRT)"; CONTEXT.md Claude's Discretion mentions structure-internal choices.
   - What's unclear: "Replay janela de pico" can be interpreted as (a) preserve original timing → variable RPS within the window, OR (b) calculate average RPS from window + fire constant → smoother.
   - Recommendation: (a) preserve timing — matches "replay audit_log" wording most faithfully; reveals real-world burstiness patterns that constant-RPS hides.

3. **What is the rollback procedure if Better Auth migrate adds twoFactor table but Phase 11 deploy fails?**
   - What we know: Phase 10 ran `migrate-dashboard.sh` against `bd_ai_dashboard_prod`; that migrate is forward-only.
   - What's unclear: If Phase 11 dashboard image fails to boot post-migrate, rolling back to `:v1.0.0` (without twoFactor plugin) is fine (extra columns don't break old code), but if we rolled back further to Phase 10 dashboard image, that's also fine. Real risk: `better-auth/cli` migrate is idempotent (created-if-not-exists shape).
   - Recommendation: Document in RUNBOOK-DEPLOY.md that twoFactor migrate is forward-only safe; rollback is image-only (no DB rollback needed).

4. **For D-19 per-env key separation: does Vast.ai support per-key labels?**
   - What we know: CONTEXT.md D-19 says "criar key separada pra prod (ou continuar shared se label não suportado por Vast API — confirmar na pesquisa)".
   - What's unclear: Vast.ai API key creation UI may not expose labels; spend tracking is by account, not by key.
   - Recommendation: Plan must include operator step to attempt Vast key duplication; if not supported, document "shared Vast key acceptable per D-19 fallback clause" in RUNBOOK-DEPLOY.md.

## Environment Availability

| Dependency | Required By | Available | Version | Fallback |
|------------|------------|-----------|---------|----------|
| Python 3.12+ | load-replay.py + smokes | ✓ (ops-claude) | 3.11+ confirmed | — |
| httpx 0.27+ | load-replay.py | ✓ | in scripts/integration-smoke/requirements.txt | — |
| Go 1.24+ | gatewayctl debug + key list build | ✓ | go.mod | — |
| Vast API key | PRD-02 chaos | ✓ | in ~/.claude/CLAUDE.md | — |
| OpenRouter API key | PRD-03 chaos verify post-recover | ✓ | in /opt/ai-gateway-prod/.env (current shared key; new prod key from D-19) | — |
| ssh access to n8n-ia-vm | PRD-03 iptables apply | ✓ | alias in ~/.ssh/config | — |
| ssh access to vps-ifix-vm | edge Traefik logs cross-check | ✓ | alias | — |
| iptables on n8n-ia-vm | PRD-03 DROP rule | ✓ | confirmed default Debian 12 | — |
| `dig` on n8n-ia-vm | resolve openrouter.ai IPs | ✓ | dnsutils pkg default | If absent: `getent hosts openrouter.ai` fallback |
| Better Auth `~1.4.18` (twoFactor + rate-limit) | PRD-06 | ✓ | dashboard/package.json | — |
| `npx auth@latest` (Better Auth CLI) | dashboard migrate | ✓ via npx (no global install needed) | — | If npx unreachable, `bunx auth@latest migrate` |
| Sentry project `ifix-ai-gateway-prod` | D-18.2 panic verify | ✓ | id 4511455942017024 (live since Phase 10) | — |
| GHA self-hosted runners | build-gateway.yml + build-dashboard.yml | ✓ | per `.planning/STATE.md` 7 runners on vps-ifix-vm | If runner down: `gh workflow run --runs-on=ubuntu-latest` fallback |
| `gh` CLI on ops-claude | D-18.4 workflow retrigger | ✓ | confirmed installed | — |

**Missing dependencies with no fallback:** none

**Missing dependencies with fallback:** none (all critical paths available with primary)

## Sources

### Primary (HIGH confidence)

- `dashboard/node_modules/better-auth/dist/plugins/two-factor/` — confirms `twoFactor` plugin shipped at version ~1.4.18, including totp/, backup-codes/, schema.d.mts (read 2026-05-27)
- `dashboard/node_modules/@better-auth/utils/dist/otp.mjs:12` — `hash = "SHA-1"` default (verified — Google Authenticator/1Password compatible)
- `gateway/cmd/gatewayctl/admin_key.go` — canonical pattern for new gatewayctl subcommands (list/create/revoke shape)
- `gateway/cmd/gatewayctl/breaker.go` — Phase 06.9 patterns reusable for new subcommands (currentOperator, audit-write, redis-key conventions)
- `gateway/internal/httpx/recoverer.go:18-30` — panic-recovery middleware path; entry-point for D-18.2 emit-error proof
- `gateway/internal/proxy/director.go:80-92` — explains Phase 10 audit/billing 0x8b bug fix; reference for RUNBOOK-INCIDENTS class 3
- `gateway/docs/LGPD-SUBPROCESSORS.md` + `LGPD-REVIEW-CHECKLIST.md` — Phase 09 baseline + checklist that PRD-05 sign-off extends
- `gateway/db/migrations/0003_create_audit_log_partitioned.sql` + `0022_audit_log_reason.sql` — audit_log schema canonical (PK request_id+ts, RANGE-partitioned by month, columns include data_class enum NOT NULL)
- `scripts/integration-smoke/smoke-sensitive-failover.py` + `requirements.txt` — pattern foundation for load-replay.py
- `.github/workflows/build-gateway.yml:18-23` — confirms `workflow_dispatch.inputs.tag` already wired (D-18.4 fix path)
- `.planning/phases/10-prod-deploy-ai-gateway/10-VERIFICATION.md` — Phase 10 deferred items canonical list (D-18 source)
- `.planning/PROJECT.md` Phase 06.9 section — tier-1 stack final + per-upstream model targets; Phase 11 doesn't change

### Secondary (MEDIUM confidence)

- [better-auth.com/docs/plugins/2fa](https://www.better-auth.com/docs/plugins/2fa) — twoFactor plugin API surface, backup-codes generation pattern, trustDevice semantic (web-fetched 2026-05-27)
- [better-auth.com/docs/concepts/rate-limit](https://www.better-auth.com/docs/concepts/rate-limit) — confirms rate-limit is BUILT-IN (not plugin), `customRules: {"/sign-in/email": {window, max}}` shape, X-Retry-After header
- [docs.vast.ai/api-reference/instances/destroy-instance](https://docs.vast.ai/api-reference/instances/destroy-instance) — DELETE endpoint URL pattern (https://console.vast.ai/api/v0/instances/{id}/)
- [better-auth.com/docs/installation](https://www.better-auth.com/docs/installation) — `npx auth@latest migrate` is the canonical CLI invocation
- [developers.cloudflare.com/ai-gateway/usage/providers/openrouter](https://developers.cloudflare.com/ai-gateway/usage/providers/openrouter) — confirms OpenRouter integrates with CF infrastructure (basis for Pitfall 3 IP-rotation note)

### Tertiary (LOW confidence)

- Vast.ai DELETE 404 idempotency behavior — official docs don't specify the response status for already-deleted IDs (Assumption A1, flagged for first chaos rehearsal verification)

## Metadata

**Confidence breakdown:**

- Standard stack: HIGH — every package version confirmed against installed node_modules / requirements.txt; slopcheck all OK
- Architecture: HIGH — all data flows backed by file reads + Phase 10 evidence
- Pitfalls: HIGH — 8 pitfalls derived from concrete codebase + ecosystem knowledge (Cloudflare/OpenRouter, Better Auth migrate semantics, smoke script race conditions)
- Code examples: HIGH — all examples extracted from or verified against existing patterns (admin_key.go, smoke-*.py, recoverer.go, director.go)
- Decision support: HIGH — every D-01..D-19 from CONTEXT.md has a codebase anchor or external-doc citation

**Research date:** 2026-05-27
**Valid until:** 2026-06-27 (30 days — better-auth stable since 1.0, Vast API stable since project Phase 6, Hetzner topology stable since Phase 3 cutover)
