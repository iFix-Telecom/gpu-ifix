# Phase 11: prod-hardening - Pattern Map

**Mapped:** 2026-05-27
**Files analyzed:** 24 (16 NEW + 8 MOD)
**Analogs found:** 24 / 24 (every file has an exact or strong role-match analog in-repo)

> **Conventions used below.**
> - "NEW" = file does not exist yet; executor creates it.
> - "MOD" = file exists; executor edits in-place.
> - Line numbers cite files read 2026-05-27; if the executor finds drift, prefer the named symbol/section over the literal line offset.
> - All paths absolute from repo root `/home/pedro/projetos/pedro/gpu-ifix/`.

---

## File Classification

| File | NEW/MOD | Role | Data Flow | Closest Analog | Match Quality |
|------|---------|------|-----------|----------------|---------------|
| `scripts/integration-smoke/load-replay.py` | NEW | script | request-response (replay JSONL → HTTPS) | `scripts/integration-smoke/smoke-sensitive-failover.py` | exact (same family) |
| `scripts/integration-smoke/audit-log-export.py` | NEW | script | batch (psycopg read → JSONL write, sanitize) | `scripts/deploy/bootstrap-postgres.sh` (psql probe pattern) + `smoke-sensitive-failover.py:query_audit` (psycopg shape) | role-match (no audit export precedent) |
| `scripts/integration-smoke/load-replay-report-schema.json` | NEW | config (JSON Schema 2020-12) | doc (machine-readable contract) | `scripts/integration-smoke/sensitive-failover-report-schema.json` | exact |
| `scripts/chaos/vast-delete.sh` | NEW | script (bash) | external-API (Vast DELETE) | `pod/scripts/vast-ai.sh` (auth + curl wrapper) | exact |
| `scripts/chaos/openrouter-iptables-drop.sh` | NEW | script (bash) | OS-state (iptables -I / -D) | `scripts/deploy/bootstrap-postgres.sh` (set -euo + log() + idempotency block) | role-match (no chaos precedent in-repo; uses deploy bash skeleton) |
| `scripts/dashboard/seed-admins.sh` | NEW | script (bash) | provisioning (Better Auth admin API or DB INSERT) | `scripts/deploy/bootstrap-postgres.sh` (env validation + ssh probe + idempotency) | role-match |
| `gateway/cmd/gatewayctl/debug.go` | NEW | CLI subcommand (Go) | request-response (HTTP POST /admin/debug/panic) | `gateway/cmd/gatewayctl/admin_key.go` (dispatcher) + `gateway/cmd/gatewayctl/breaker.go:117-...` (flags + admin HTTP shape) | role-match |
| `gateway/cmd/gatewayctl/debug_test.go` | NEW | unit test (Go) | in-process | `gateway/cmd/gatewayctl/admin_key_test.go` | exact |
| `gateway/internal/admin/debug_panic.go` | NEW | HTTP handler (Go) | request-response (panic) | `gateway/internal/admin/middleware.go` (admin context FromContext + WriteOpenAIError) | role-match (handler wired AFTER Recoverer wraps `/admin/*` chain) |
| `dashboard/src/lib/allowlist.ts` | NEW | utility (TS) | pure-function (validate email domain) | `dashboard/src/lib/utils.ts` (`cn()` pure helper) + `smoke.test.ts` (vitest skeleton) | role-match |
| `dashboard/src/lib/allowlist.test.ts` | NEW | unit test (vitest) | in-process | `dashboard/src/lib/smoke.test.ts` | exact |
| `dashboard/src/app/2fa/enroll/page.tsx` | NEW | UI page (Next.js client) | request-response (authClient.twoFactor.enable + verifyTotp) | `dashboard/src/app/login/page.tsx` | exact (Card+form pattern) |
| `dashboard/src/app/2fa/challenge/page.tsx` | NEW | UI page (Next.js client) | request-response (authClient.twoFactor.verifyTotp) | `dashboard/src/app/login/page.tsx` | exact |
| `gateway/docs/RUNBOOK-INCIDENTS.md` | NEW | doc (Markdown) | doc | `gateway/docs/RUNBOOK-FAILOVER.md` (header + Quick Diagnosis + Mental Model 30s) + `RUNBOOK-PRIMARY-POD.md` (cross-ref sibling list) | exact (existing 7-runbook family) |
| `gateway/docs/POSTMORTEM-TEMPLATE.md` | NEW | doc template | doc | `.planning/phases/10-prod-deploy-ai-gateway/10-VERIFICATION.md` (pitfalls_hit/deviations sections — closest in-repo precedent for postmortem-style sections) | role-match (no postmortem template in repo) |
| `gateway/docs/LGPD-SIGNOFF-PROCESS.md` | NEW | doc | doc | `gateway/docs/LGPD-SUBPROCESSORS.md` (LGPD doc voice + structure) + `gateway/docs/LGPD-REVIEW-CHECKLIST.md` | exact (LGPD-doc family) |
| `gateway/docs/LGPD-SIGNOFF-LETTER-TEMPLATE.md` | NEW | doc template | doc | `gateway/docs/LGPD-SUBPROCESSORS.md` (sub-processor table + pt-BR voice) | role-match |
| `dashboard/src/lib/auth.ts` | MOD | Better Auth config | request-response (auth endpoints) | self (current file 26 LOC) + RESEARCH §Pattern 2 example | exact (in-place extension) |
| `dashboard/src/lib/schema.ts` | MOD | Drizzle schema | data-model | self + better-auth twoFactor plugin schema shape | exact |
| `dashboard/src/middleware.ts` | MOD | Next.js edge middleware | request-response (cookie gate + redirect) | self (current 25 LOC, two-stage 2FA insertion site) | exact |
| `dashboard/src/app/login/page.tsx` | MOD | UI page | request-response | self (Alert insertion sites) | exact |
| `gateway/cmd/gatewayctl/key.go` | MOD | CLI subcommand | DB query | `gateway/cmd/gatewayctl/admin_key.go:runAdminKeyList` (lines 240-280) | exact |
| `scripts/integration-smoke/smoke-sensitive-failover.py` | MOD | script | request-response | self (D-18.1 + D-09 fix sites at `OPEN_LIKE_STATES` + `induce_failure_via_gatewayctl`) | exact |
| `gateway/docs/RUNBOOK-DEPLOY.md` | MOD | doc | doc | self (extend with D-18.4 + D-19 sections, follow existing header/steps prose voice) | exact |
| `.gitignore` | MOD | config | doc | self (lines 24-29 `!scripts/integration-smoke/fixtures/...` allow-rule pattern) | exact |

---

## Pattern Assignments

### `scripts/integration-smoke/load-replay.py` (NEW, script, request-response)

**Analog:** `scripts/integration-smoke/smoke-sensitive-failover.py` (verified read 2026-05-27)

**Why this analog:** The smoke-*.py family establishes the EXACT shape the gateway team uses for async-httpx + structlog + jsonschema + secret-once CLI patterns. `load-replay.py` extends it by adding bounded-concurrency replay over a JSONL fixture and a P50/P95/P99 sumário, but every other dimension (config dataclass, argparse argument shape, log fields, schema validation tail, exit codes) is the same.

**Imports + constants pattern** (smoke-sensitive-failover.py:76-103):

```python
from __future__ import annotations
import argparse, asyncio, dataclasses, json, os, subprocess, sys, time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import httpx
import structlog
import psycopg                       # only needed by audit-log-export.py; load-replay drops this
from jsonschema import Draft202012Validator
from jsonschema.exceptions import ValidationError

SCHEMA_VERSION = "1.0.0"
log = structlog.get_logger().bind(module="LOAD_REPLAY")     # mirror MODULE name caps
```

**Config dataclass + argparse pattern** (smoke-sensitive-failover.py:140-238):

```python
@dataclasses.dataclass
class Config:
    gateway_url: str
    fixture_path: str
    duration_s: float
    max_concurrency: int
    out_path: str
    # NOTE: API keys per-tenant come from the fixture (sanitized), NOT CLI

def parse_args() -> Config:
    ap = argparse.ArgumentParser(description="...")
    ap.add_argument("--gateway-url", default=os.getenv("SMOKE_GATEWAY_URL"), help="...")
    # ... etc. Secret-once: NO committed default for any token; argparse-error
    # when missing. Mirror lines 159-228.
```

**Async orchestration skeleton** (research RESEARCH.md Pattern 3 + smoke-sensitive-failover.py main_async lines 579-783):

The planner's task body for this file is the structure shown in `11-RESEARCH.md` lines 326-394 — a single `httpx.AsyncClient(http2=True)` + `asyncio.Semaphore(max_concurrency)` + `asyncio.sleep(rec["_replay_delay_s"])` between successive records to preserve audit-log timing.

**Schema-validate tail** (smoke-sensitive-failover.py:756-770):

```python
schema_invalid = False
schema = json.loads((Path(__file__).parent / "load-replay-report-schema.json").read_text())
try:
    Draft202012Validator(schema).validate(report)
except ValidationError as e:
    schema_invalid = True
    errors.append(f"report failed schema validation: {e.message}")
Path(cfg.out_path).write_text(json.dumps(report, indent=2, sort_keys=True))
if schema_invalid:
    return 1
```

**What executor must replicate:** the entire 8-section spine — `Constants → Config + CLI → Helper funcs → Gateway requests → Gates/derived metrics → Orchestrator → Schema validate → main()`. Specifically: secret-once discipline (no committed default for `SMOKE_API_KEY` even though replay uses per-tenant keys from fixture — gateway URL still requires explicit arg), structlog bind module label, `from __future__ import annotations`, and the `WR-05` invariant where a schema-invalid report is a hard error (exit 1) but still written for debugging.

---

### `scripts/integration-smoke/audit-log-export.py` (NEW, script, batch)

**Analog:**
- For psycopg shape: `scripts/integration-smoke/smoke-sensitive-failover.py:442-505` (`query_audit` function — read-only `SELECT` against `ai_gateway.audit_log`).
- For sanitization invariant (SELECT COUNT only / never content): same file lines 38-40 + 485-489.
- For bash glue (if executor opts for a tiny Python script + bash wrapper): `scripts/deploy/bootstrap-postgres.sh:88-120`.

**psycopg connect pattern** (smoke-sensitive-failover.py:472-498):

```python
try:
    with psycopg.connect(pg_dsn, connect_timeout=10) as conn:
        with conn.cursor() as cur:
            cur.execute(
                "SELECT request_id, ts, tenant_id, route, upstream, status_code "
                "FROM ai_gateway.audit_log WHERE ts >= %s AND ts < %s "
                "ORDER BY ts ASC",
                (window_start, window_end),
            )
            for row in cur:
                # ... sanitize + emit JSONL line
                pass
except Exception as e:
    # Do NOT include the DSN in the error string (threat T-09-09).
    log.error("audit-DB query failed", err=f"{str(e)[:300]}")
```

**Sanitization invariant** (RESEARCH.md Pitfall 1):

```python
# Replace tool_calls[].function.arguments with placeholder; drop whisper file bytes.
def sanitize_body(body: dict, tenant_data_class: str) -> dict:
    if tenant_data_class == "sensitive":
        return None   # caller MUST skip; PRD-01 baseline excludes sensitive replay
    # ... tool_calls placeholder + audio.filename stripping
```

**What executor must replicate:** psycopg context-manager double-`with`, never-log-DSN invariant (threat T-09-09), `connect_timeout=10`, and the explicit skip rule for `data_class=sensitive` tenants in baseline replay per RESEARCH.md Open Question #1. Schema column names match `gateway/db/queries/auth.sql` + audit migrations (`0003_create_audit_log_partitioned.sql`, `0020_audit_log_event_kind.sql`).

---

### `scripts/integration-smoke/load-replay-report-schema.json` (NEW, JSON Schema)

**Analog:** `scripts/integration-smoke/sensitive-failover-report-schema.json` (verified read 2026-05-27, 117 lines)

**Top-of-file shape** (lines 1-19):

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://ifixtelecom.com.br/schemas/integration-smoke/load-replay-report/1.0.0",
  "title": "Load Replay Smoke Report",
  "version": "1.0.0",
  "type": "object",
  "additionalProperties": false,
  "required": [
    "schema_version", "started_at", "finished_at", "target",
    "summary", "errors", "gates"
  ],
  "properties": {
    "schema_version": { "type": "string", "const": "1.0.0" },
    "started_at": { "type": "string", "format": "date-time" },
    "finished_at": { "type": "string", "format": "date-time" },
    "target": {
      "type": "object",
      "required": ["gateway_url", "tenant"],
      "properties": {
        "gateway_url": { "type": "string", "format": "uri" },
        "tenant": { "type": "string" }
      },
      "additionalProperties": false
    },
    "git_sha": { "type": "string", "pattern": "^[0-9a-f]{7,40}$" },
    "summary": {
      "type": "object",
      "description": "P50/P95/P99 per route + counters",
      "additionalProperties": false,
      "required": ["routes", "total_requests", "error_count"],
      "properties": {
        "routes": {
          "type": "object",
          "additionalProperties": {
            "type": "object",
            "required": ["n", "p50_ms", "p95_ms", "p99_ms"],
            "properties": {
              "n": { "type": "integer", "minimum": 0 },
              "p50_ms": { "type": "integer", "minimum": 0 },
              "p95_ms": { "type": "integer", "minimum": 0 },
              "p99_ms": { "type": "integer", "minimum": 0 }
            }
          }
        },
        "total_requests": { "type": "integer", "minimum": 0 },
        "error_count": { "type": "integer", "minimum": 0 }
      }
    },
    "gates": {
      "type": "object",
      "required": ["p95_chat_ms_le_5000", "p95_embed_ms_le_1000",
                   "p95_stt_ms_le_10000", "error_rate_lt_1pct",
                   "zero_5xx_panic", "all_passed"],
      "additionalProperties": false,
      "properties": {
        "p95_chat_ms_le_5000": { "type": "boolean" },
        "p95_embed_ms_le_1000": { "type": "boolean" },
        "p95_stt_ms_le_10000": { "type": "boolean" },
        "error_rate_lt_1pct": { "type": "boolean" },
        "zero_5xx_panic": { "type": "boolean" },
        "all_passed": { "type": "boolean" }
      }
    }
  }
}
```

**What executor must replicate:** `additionalProperties: false` everywhere, `$id` URL convention, `schema_version` const-pinned to `"1.0.0"`, mandatory `target.tenant + gateway_url`, `git_sha` 7-40 hex regex. Gates names map 1:1 to D-04 SLO v1.0 (P95 chat ≤5s + embed ≤1s + STT ≤10s + error <1% non-503 + zero 5xx panic).

---

### `scripts/chaos/vast-delete.sh` (NEW, bash, external-API)

**Analog:** `pod/scripts/vast-ai.sh:1-60` (verified read 2026-05-27)

**Auth + helper pattern** (lines 14-37):

```bash
#!/usr/bin/env bash
set -euo pipefail

: "${VAST_AI_API_KEY:?missing}"
VAST_BASE="${VAST_BASE:-https://vast.ai/api/v0}"

log() { printf '[%s] [vast-delete] %s\n' "$(date -Iseconds)" "$*" >&2; }

api() {
  # api METHOD PATH [JSON_BODY]
  local method="$1" path="$2" body="${3:-}"
  local url="${VAST_BASE}${path}"
  local args=(-sS -X "${method}" -H "Authorization: Bearer ${VAST_AI_API_KEY}")
  if [[ -n "${body}" ]]; then
    args+=(-H "Content-Type: application/json" --data-raw "${body}")
  fi
  curl "${args[@]}" "${url}"
}
```

**DELETE body shape** (RESEARCH.md lines 596-619 — the planner already drafted the chaos hook body):

```bash
# Get current primary lifecycle instance ID
INSTANCE_ID=$(ssh n8n-ia-vm \
  'docker exec ifix-ai-gateway /gatewayctl primary state' \
  | grep -oP 'vast_instance_id=\K\d+')

HTTP=$(curl -s -o /tmp/vast-delete.json -w '%{http_code}' \
  -X DELETE "https://console.vast.ai/api/v0/instances/${INSTANCE_ID}/" \
  -H "Authorization: Bearer $VAST_AI_API_KEY")
echo "vast delete status=$HTTP body=$(cat /tmp/vast-delete.json)"
```

**What executor must replicate:** `set -euo pipefail`, `: "${VAR:?missing}"` env validation, `log()` to stderr (stdout reserved for parseable summary), 200 = killed / 404 = idempotent gone (RESEARCH Assumption A1), and post-chaos cleanup advice (re-run `gatewayctl primary force-up` if FSM stuck).

> **Endpoint correction:** Vast docs base = `https://console.vast.ai/api/v0` (per RESEARCH Pattern 4). The `pod/scripts/vast-ai.sh` uses `https://vast.ai/api/v0` which redirects but the DELETE chaos hook should use the canonical `console.vast.ai` form.

---

### `scripts/chaos/openrouter-iptables-drop.sh` (NEW, bash, OS-state)

**Analog:**
- For `set -euo` + `log()` skeleton: `scripts/deploy/bootstrap-postgres.sh:51-56`.
- For full body: RESEARCH.md lines 566-591 (already drafted complete script body).

**Skeleton from bootstrap-postgres.sh** (lines 52-56):

```bash
set -euo pipefail

log() { printf '[%s] [openrouter-iptables-drop] %s\n' "$(date -Iseconds)" "$*" >&2; }
```

**RESEARCH-drafted body** (RESEARCH.md:567-591):

```bash
ACTION="${1:?usage: $0 apply|cleanup}"
DOMAIN="openrouter.ai"

if [[ "$ACTION" == "apply" ]]; then
  IPS=$(dig +short "$DOMAIN" | sort -u)
  for ip in $IPS; do
    sudo iptables -I OUTPUT 1 -d "$ip" -p tcp --dport 443 -j DROP \
      -m comment --comment "phase11-chaos-openrouter"
  done
  # CF wider ranges for rotation buffer:
  sudo iptables -I OUTPUT 1 -d 104.18.0.0/15 -p tcp --dport 443 -j DROP \
    -m comment --comment "phase11-chaos-openrouter"
  sudo iptables -I OUTPUT 1 -d 172.64.0.0/13 -p tcp --dport 443 -j DROP \
    -m comment --comment "phase11-chaos-openrouter"
elif [[ "$ACTION" == "cleanup" ]]; then
  while sudo iptables -S OUTPUT | grep -q "phase11-chaos-openrouter"; do
    RULE=$(sudo iptables -S OUTPUT | grep -m1 "phase11-chaos-openrouter" | sed 's/^-A/-D/')
    sudo iptables $RULE
  done
fi
```

**What executor must replicate:** `--comment phase11-chaos-openrouter` tag on EVERY rule (RESEARCH Pitfall 3 cleanup pattern), broad CF CIDR DROP as rotation insurance, idempotent apply (multiple `-I OUTPUT 1` is OK) + cleanup that loops until no comment-tagged rule remains.

---

### `scripts/dashboard/seed-admins.sh` (NEW, bash, provisioning)

**Analog:** `scripts/deploy/bootstrap-postgres.sh` (verified read 2026-05-27, full file)

**Env validation pattern** (lines 62-79):

```bash
if [[ -z "${DASHBOARD_DATABASE_URL:-}" ]]; then
  log "FATAL: DASHBOARD_DATABASE_URL env var is required (not set)."
  log ""
  log "  This must be the prod DSN pointing at bd_ai_dashboard_prod, e.g."
  log "    postgres://doadmin:<PASS>@db-...ondigitalocean.com:25060/bd_ai_dashboard_prod?..."
  exit 1
fi
```

**Idempotent SELECT-then-INSERT pattern** (lines 127-163, `ensure_database`):

```bash
ensure_admin() {
  local email="$1" name="$2"
  log "checking '$email' in dashboard_auth.user"
  local existing
  existing="$(psql "$DASHBOARD_DATABASE_URL" -tAc \
    "SELECT 1 FROM dashboard_auth.user WHERE email='${email}'")"
  if [[ -z "$existing" ]]; then
    log "seeding admin '$email'"
    # ... call Better Auth signUp via httpie/curl OR direct INSERT with bcrypt hash
  else
    log "admin '$email' already exists — skipping (Pitfall 2 idempotent path)"
  fi
}
```

**What executor must replicate:** `set -euo pipefail`, ISO-8601 `log()` to stderr, fail-fast env validation with specific pointer (not bare "missing"), idempotent re-run safety (probe-before-mutate), final `cat <<EOF` summary block printed to stdout (the only stdout content). NEVER echo the DSN.

---

### `gateway/cmd/gatewayctl/debug.go` (NEW, Go CLI subcommand, request-response)

**Analog:**
- For dispatcher shape: `gateway/cmd/gatewayctl/admin_key.go:50-66`.
- For flag parsing + admin HTTP shape: `gateway/cmd/gatewayctl/breaker.go:117-...`.

**Dispatcher pattern** (admin_key.go:49-66):

```go
// runDebug dispatches `gatewayctl debug <subcommand>`.
func runDebug(ctx context.Context, args []string, log *slog.Logger) int {
    if len(args) == 0 {
        fmt.Fprintln(os.Stderr, "Usage: gatewayctl debug <emit-error> [flags]")
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
```

**Flag parsing + HTTP shape** (admin_key.go pattern + breaker.go:117-130 for flag plumbing):

```go
func runDebugEmitError(ctx context.Context, args []string, log *slog.Logger) int {
    fs := flag.NewFlagSet("debug emit-error", flag.ExitOnError)
    gwURL := fs.String("gateway", os.Getenv("AI_GATEWAY_URL"),
                        "gateway base URL (env AI_GATEWAY_URL)")
    adminKey := fs.String("admin-key", os.Getenv("AI_GATEWAY_ADMIN_KEY"),
                          "admin key (env AI_GATEWAY_ADMIN_KEY)")
    if err := fs.Parse(args); err != nil { return 2 }
    if *gwURL == "" || *adminKey == "" {
        fmt.Fprintln(os.Stderr, "error: --gateway and --admin-key required")
        return 2
    }
    req, _ := http.NewRequestWithContext(ctx, "POST", *gwURL+"/admin/debug/panic", nil)
    req.Header.Set("X-Admin-Key", *adminKey)
    resp, err := http.DefaultClient.Do(req)
    if err != nil { ... return 1 }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusInternalServerError {
        fmt.Fprintf(os.Stderr, "expected 500 (panic recovered), got %d\n", resp.StatusCode)
        return 1
    }
    log.Info("debug emit-error sent", "gateway", *gwURL, "status", resp.StatusCode)
    return 0
}
```

**main.go registration** (main.go:48-101 — operator must add `case "debug"` + extend usage()):

```go
case "debug":
    os.Exit(runDebug(ctx, args, log))
```

**What executor must replicate:** the EXACT dispatcher signature `(ctx context.Context, args []string, log *slog.Logger) int`, exit codes 0/1/2 convention (0=ok, 1=runtime fail, 2=usage fail), `fmt.Fprintln(os.Stderr, ...)` for errors / `fmt.Printf(stdout, ...)` for parseable results, slog-only-for-metadata (never raw secret), and add the new command to `gateway/cmd/gatewayctl/main.go:48-101` switch + `usage()` block (lines 20-46).

---

### `gateway/cmd/gatewayctl/debug_test.go` (NEW, Go unit test)

**Analog:** `gateway/cmd/gatewayctl/admin_key_test.go:1-14` (verified read 2026-05-27)

**Full file pattern** (admin_key_test.go all 14 lines):

```go
package main_test

import "testing"

// TestDebugEmitErrorPlaceholder pins the contract that `gatewayctl debug
// emit-error` has a binary entry point. Real coverage is in 11 HUMAN-UAT
// (panic-path Sentry verify lives in live UAT, not unit).
func TestDebugEmitErrorPlaceholder(t *testing.T) {
    t.Skip("integration coverage lives in 11 HUMAN-UAT against deployed gateway")
}
```

**What executor must replicate:** placeholder skipped test so `go test ./cmd/gatewayctl/...` keeps building. Real verification = live UAT calling the deployed gateway + querying Sentry API for the event landing within 5s (D-18.2 acceptance).

---

### `gateway/internal/admin/debug_panic.go` (NEW, Go HTTP handler)

**Analog:**
- For handler signature + context recovery: `gateway/internal/admin/middleware.go:159-183` (Middleware + FromContext pattern).
- For panic emit: it MUST panic INSIDE the handler so `gateway/internal/httpx/recoverer.go:18-30` catches it AFTER the Recoverer middleware wraps the chain.

**Existing Recoverer behavior** (recoverer.go:15-34 — verbatim):

```go
func Recoverer(base *slog.Logger) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            defer func() {
                if rec := recover(); rec != nil {
                    base.ErrorContext(r.Context(), "panic recovered",
                        "panic", rec,
                        "request_id", RequestIDFrom(r.Context()),
                    )
                    sentry.CurrentHub().Recover(rec)
                    sentry.Flush(500 * time.Millisecond)
                    WriteOpenAIError(w, http.StatusInternalServerError,
                        "api_error", "internal_error",
                        "The gateway encountered an unexpected error.")
                }
            }()
            next.ServeHTTP(w, r)
        })
    }
}
```

**Handler skeleton** (mirror middleware.go style):

```go
// Package admin (debug_panic.go): operator-only synthetic panic emitter
// used by `gatewayctl debug emit-error` to exercise the httpx.Recoverer
// → sentry.CurrentHub().Recover → sentry.Flush(500ms) chain in PROD.
//
// Wiring: this handler is mounted at POST /admin/debug/panic under the
// admin auth chain (admin.Middleware) AND under httpx.Recoverer. The
// Recoverer wrap order is critical — if a future refactor inverts it,
// the panic will crash the process instead of returning a 500.
package admin

import (
    "log/slog"
    "net/http"
)

// DebugPanicHandler always panics. Used by `gatewayctl debug emit-error`
// to prove the Recoverer + Sentry path end-to-end in PROD.
func DebugPanicHandler(log *slog.Logger) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        ac, _ := FromContext(r.Context())
        log.WarnContext(r.Context(), "synthetic panic about to fire",
            "admin_key_id", ac.AdminKeyID.String(),
            "label", ac.Label,
        )
        panic("synthetic panic emitted by gatewayctl debug emit-error")
    })
}
```

**Mount location:** wherever the gateway wires admin routes (`gateway/internal/admin/` or `gateway/cmd/gateway/main.go` where the router is composed). Executor MUST verify the wrap order is `Recoverer(Middleware(DebugPanicHandler))` — Recoverer outermost.

**What executor must replicate:** package `admin`, `FromContext(r.Context())` to recover AdminContext for audit logging, slog warn BEFORE panic (so even if Sentry is misconfigured we have a local breadcrumb), and the explicit synthetic panic message (Sentry event search-string).

---

### `dashboard/src/lib/allowlist.ts` (NEW, TS pure utility)

**Analog:**
- For pure-function shape + named export: `dashboard/src/lib/utils.ts` (the `cn()` helper).
- For env-var configurability: RESEARCH.md Pattern §"Email allowlist" (lines 553-561).

**File body** (RESEARCH.md verbatim):

```typescript
/**
 * Domain allowlist for dashboard signUp. Env-driven so SREs can extend without
 * a code change (e.g. add a second domain for a partner audit). Default scoped
 * to the single Ifix domain per D-13.
 */
const ALLOWED = (process.env.DASHBOARD_ALLOWED_EMAIL_DOMAINS ?? "ifixtelecom.com.br")
  .split(",").map((s) => s.trim().toLowerCase());

export function isAllowedEmail(email: string): boolean {
  const at = email.lastIndexOf("@");
  if (at < 0) return false;
  return ALLOWED.includes(email.slice(at + 1).toLowerCase());
}
```

**What executor must replicate:** named export (project convention — `dashboard/src/lib/utils.ts` uses named exports, no default), env-var with comma-separated fallback, `lastIndexOf("@")` (not regex — handles edge cases like `quoted@local@domain.com` safely enough for the 4-admin use case).

---

### `dashboard/src/lib/allowlist.test.ts` (NEW, vitest)

**Analog:** `dashboard/src/lib/smoke.test.ts:1-12` (verified read 2026-05-27)

**File body pattern** (smoke.test.ts verbatim):

```typescript
import { describe, expect, it } from "vitest";
import { isAllowedEmail } from "@/lib/allowlist";

describe("isAllowedEmail", () => {
  it("accepts @ifixtelecom.com.br", () => {
    expect(isAllowedEmail("admin@ifixtelecom.com.br")).toBe(true);
  });
  it("rejects non-allowlisted domains", () => {
    expect(isAllowedEmail("user@gmail.com")).toBe(false);
  });
  it("rejects malformed input", () => {
    expect(isAllowedEmail("no-at-sign")).toBe(false);
    expect(isAllowedEmail("")).toBe(false);
  });
  it("is case-insensitive", () => {
    expect(isAllowedEmail("Admin@IFIXTELECOM.COM.BR")).toBe(true);
  });
});
```

**What executor must replicate:** vitest imports from "vitest" (NOT "jest"), `@/lib/...` path alias (project convention — `dashboard/vitest.config.ts` already wires this; verified via smoke.test.ts which uses `@/lib/utils`), 4 minimum cases (positive, negative, malformed, case-insensitive).

---

### `dashboard/src/app/2fa/enroll/page.tsx` (NEW, Next.js client page)

**Analog:** `dashboard/src/app/login/page.tsx` (verified read 2026-05-27, full 99 lines)

**Full structure from login/page.tsx** (lines 1-99):

```tsx
"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import {
  Card, CardContent, CardDescription, CardHeader, CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { authClient } from "@/lib/auth-client";

export default function EnrollPage() {
  const router = useRouter();
  const [step, setStep] = useState<"qr"|"verify"|"backup">("qr");
  const [qrURI, setQrURI] = useState("");
  const [secret, setSecret] = useState("");
  const [backupCodes, setBackupCodes] = useState<string[]>([]);
  // ...

  return (
    <main className="flex min-h-screen items-center justify-center p-6">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>Configurar 2FA</CardTitle>     {/* UI-SPEC copywriting */}
          <CardDescription>
            Escaneie o QR code abaixo com seu app autenticador.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {/* form per step */}
        </CardContent>
      </Card>
    </main>
  );
}
```

**twoFactor enroll call sequence** (RESEARCH.md lines 520-547):

```tsx
const generate = async (password: string) => {
  const res = await authClient.twoFactor.enable({ password });
  setQrURI(res.data?.totpURI ?? "");
  setSecret(res.data?.secret ?? "");
  setBackupCodes(res.data?.backupCodes ?? []);
};
const verify = async (code: string) => {
  const res = await authClient.twoFactor.verifyTotp({ code });
  if (res.error) return;
  setStep("backup");
};
```

**UI components needed** (from UI-SPEC component inventory):
- Existing in `dashboard/src/components/ui/`: `card`, `input`, `button`, `alert`, `skeleton`, `sonner`, `separator`.
- **Net-new shadcn blocks to install** (UI-SPEC line 226-228): `input-otp` (6-digit TOTP input), `dialog` (backup-codes display modal). Install via `npx shadcn add input-otp dialog`.

**What executor must replicate:** `"use client";` directive, `flex min-h-screen items-center justify-center p-6` outer layout, `Card className="w-full max-w-sm"`, named exports from `@/components/ui/*`, pt-BR copywriting from UI-SPEC table (Card title "Configurar 2FA" + 3-step flow titles "Configurar 2FA"/"Confirmar código"/"Códigos de backup"), `font-mono` Tailwind class for backup codes + manual TOTP secret display (UI-SPEC Typography section).

---

### `dashboard/src/app/2fa/challenge/page.tsx` (NEW, Next.js client page)

**Analog:** `dashboard/src/app/login/page.tsx` (same as enroll)

**Structural difference vs login:** ONE `input-otp` field instead of email+password; CTA "Confirmar código"; on success `router.push("/")` + `router.refresh()` (same as login lines 45-46).

**Verify call** (RESEARCH.md lines 541-544):

```tsx
const verify = async (code: string) => {
  const res = await authClient.twoFactor.verifyTotp({ code });
  if (res.error) {
    setError("Código incorreto. Confirme o código atual no seu app autenticador.");
    return;
  }
  router.push("/");
  router.refresh();
};
```

**What executor must replicate:** identical layout as login page (max-w-sm Card centered), input-otp 6-digit slot block, pt-BR error copy from UI-SPEC §Error states ("Código incorreto. Confirme o código atual no seu app autenticador e tente novamente.").

---

### `gateway/docs/RUNBOOK-INCIDENTS.md` (NEW, Markdown runbook)

**Analog:** `gateway/docs/RUNBOOK-FAILOVER.md` (verified read 2026-05-27, lines 1-100)

**Header + Read-when pattern** (RUNBOOK-FAILOVER.md:1-15):

```markdown
# Incident Response Runbook

**Phase 11 (`ifix-ai-gateway`) production incident playbook.** Read this when:

- A new incident is suspected (operator instinct / alert / client report).
- A historical incident is being post-mortem'd (cross-ref POSTMORTEM-TEMPLATE.md).
- Operator handoff between shifts mentions one of the 4 incident classes below.

Related runbooks:
- `gateway/docs/RUNBOOK-DEPLOY.md`
- `gateway/docs/RUNBOOK-FAILOVER.md` — Phase 3 circuit-breaker + tier-0 ↔ tier-1 fallback.
- `gateway/docs/RUNBOOK-PRIMARY-POD.md` — Phase 6.6 Vast primary lifecycle.
- `gateway/docs/RUNBOOK-EMERGENCY-POD.md` — Phase 6 emergency pod (breaker-driven).
- `gateway/docs/RUNBOOK-OBSERVABILITY-ALERTING.md` — Sentry + Prometheus.
- `gateway/docs/RUNBOOK-QUOTAS-BILLING.md` — Phase 4 rate-limit + quota.
- `gateway/docs/RUNBOOK-CLIENT-INTEGRATION.md` + `RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md`
```

**Mental Model (30 seconds) pattern** (RUNBOOK-FAILOVER.md:17-46): each runbook opens with a compact mental model. INCIDENTS should open with the SLO v1.0 D-04 thresholds as the detection baseline:

```markdown
## Mental Model (30 seconds)

SLO v1.0 thresholds — any sustained breach = incident:

| Metric | Threshold | Source |
|--------|-----------|--------|
| P95 chat completion | ≤5s | D-04 |
| P95 embed | ≤1s | D-04 |
| P95 STT | ≤10s | D-04 |
| Error rate (non-503) | <1% | D-04 |
| 5xx panic count | 0 | D-04 |

4 incident classes (D-11):
1. Primary pod down (Vast yank / supervisord crash / GPU OOM)
2. OpenRouter / OpenAI degraded
3. Audit/billing pipeline broken
4. Rate-limit / quota lockout (single-tenant)
```

**Per-class section structure** (mirror RUNBOOK-FAILOVER.md "Quick Diagnosis" + "Diagnose → Mitigate → Verify" flow):

```markdown
## Class 1: Primary pod down

**Detection signals:**
- `gateway_primary_state{state="asleep"}` Prometheus
- Sentry breadcrumb `subsystem:primary, shutdown_reason:health_timeout`
- `/v1/health/upstreams` shows `local-llm.state="open"`

**Diagnose:** ...
**Mitigate:** ...
**Verify:** ...
**Cross-ref:** RUNBOOK-PRIMARY-POD.md + RUNBOOK-FAILOVER.md.
```

**What executor must replicate:** the 4 D-11 classes EXACTLY by name ("Primary pod down" / "OpenRouter / OpenAI degraded" / "Audit/billing pipeline broken" / "Rate-limit / quota lockout tenant") so the PRD-04 grep-acceptance test passes (`grep -q "Primary pod down\|OpenRouter / OpenAI degraded\|..."`). Cross-ref each of the 7 sibling runbooks (RUNBOOK-FAILOVER, RUNBOOK-PRIMARY-POD, RUNBOOK-EMERGENCY-POD, RUNBOOK-OBSERVABILITY-ALERTING, RUNBOOK-QUOTAS-BILLING, RUNBOOK-CLIENT-INTEGRATION, RUNBOOK-CLIENT-INTEGRATION-SENSITIVE). Class 3 cites commit 5bd79d1 (`r.Header.Del("Accept-Encoding")` fix) per CONTEXT.md D-11 line 49.

---

### `gateway/docs/POSTMORTEM-TEMPLATE.md` (NEW, Markdown template)

**Analog:** `.planning/phases/10-prod-deploy-ai-gateway/10-VERIFICATION.md` (in-repo postmortem-style precedent for pitfalls_hit + deviations sections); no other postmortem template exists in-repo.

**Google SRE blameless 9-section structure** (D-10):

```markdown
# Postmortem: {Incident Title}

**Date:** YYYY-MM-DD
**Duration:** {start UTC} → {end UTC} ({wall-clock minutes})
**Severity:** SEV-{1|2|3}
**Author:** {operator}
**Status:** draft | review | published

## 1. Summary
{2-3 sentences: what broke, who was affected, how long, root cause one-liner}

## 2. Impact
{tenants affected, requests failed, revenue impact if any, SLO budget burned}

## 3. Root Cause(s)
{technical cause(s); blameless — describe systems, not people}

## 4. Trigger
{specific event that started the incident — deploy, traffic spike, dep change, ...}

## 5. Detection
{how it was first noticed — alert? client report? internal observation? time-to-detect}

## 6. Resolution
{step-by-step what fixed it; cite commits / runbook steps}

## 7. Action Items
{deliverables — link to GitHub issues; owners + due dates}

| # | Action | Owner | Due | Status |
|---|--------|-------|-----|--------|
| 1 | ... | @user | YYYY-MM-DD | open |

## 8. Timeline
{UTC timestamps, terse events; pull from Sentry, audit_log, operator notes}

## 9. Lessons Learned
{insights that don't map to a single action item — "the system surprised us by..."}
```

**What executor must replicate:** the EXACT 9-section spelling so future postmortems are diff-comparable. CONTEXT.md D-10 specifies "Sections: Summary, Impact, Root Cause(s), Trigger, Detection, Resolution, Action Items, Timeline, Lessons" — DO NOT add or omit sections. The Action Items section has a Markdown table skeleton (Owner/Due/Status are tracking-essential).

---

### `gateway/docs/LGPD-SIGNOFF-PROCESS.md` + `LGPD-SIGNOFF-LETTER-TEMPLATE.md` (NEW, Markdown)

**Analog:** `gateway/docs/LGPD-SUBPROCESSORS.md:1-60` (verified read 2026-05-27)

**Header convention** (LGPD-SUBPROCESSORS.md:1-3):

```markdown
# LGPD — Sub-processadores do ifix-ai-gateway

**Last updated: YYYY-MM-DD.**

## Purpose

{pt-BR explanatory paragraph}
```

**Sub-processor citation invariant** (LGPD-SUBPROCESSORS.md:19-27 — the canonical 3-sub-processor table):

The `LGPD-SIGNOFF-LETTER-TEMPLATE.md` MUST reference the 4 sub-processors Phase 09 already declared: **Vast.ai, OpenAI, OpenRouter, MinIO** (RESEARCH.md PRD-05 test row line 654 — `grep -q "Vast.ai\|OpenAI\|OpenRouter\|MinIO"`). MinIO is added relative to LGPD-SUBPROCESSORS.md (which only lists 3) because MinIO stores Whisper weight artifacts and is part of the data path.

**LGPD-SIGNOFF-PROCESS.md skeleton:**

```markdown
# LGPD — Processo de Sign-off

**Last updated: YYYY-MM-DD.**

## Purpose
Define o processo de sign-off LGPD que precede a ativação de qualquer tenant
`data_class: sensitive` em produção.

## Quem assina
- Encarregado de Dados (DPO) da Ifix
- Diretor jurídico
- Owner técnico do gateway (Plataforma)

## O que é entregue ao jurídico
- `gateway/docs/LGPD-SUBPROCESSORS.md` (lista atualizada)
- `gateway/docs/LGPD-REVIEW-CHECKLIST.md` (controles técnicos)
- `gateway/docs/LGPD-SIGNOFF-LETTER-TEMPLATE.md` (carta a assinar)

## Onde arquivar a carta assinada
`.planning/legal/lgpd-signoff-{YYYY-MM-DD}-{tenant}.pdf` — gitignored (binário).

## Cadência
Revisão anual + revisão event-driven em mudanças de sub-processador.
```

**What executor must replicate:** pt-BR doc voice from LGPD-SUBPROCESSORS.md ("ifix-ai-gateway", "tenants `sensitive`", "controlador-operador"), header `**Last updated: YYYY-MM-DD.**`, evidence file convention path `.planning/legal/lgpd-signoff-{YYYY-MM-DD}-{tenant}.pdf` (CONTEXT.md D-16). The LETTER-TEMPLATE must list all 4 sub-processors so the grep acceptance test passes.

---

### `dashboard/src/lib/auth.ts` (MOD, Better Auth config)

**Analog:** self — current file is 26 LOC, fully read.

**Current state** (lines 14-26):

```typescript
import { betterAuth } from "better-auth";
import { drizzleAdapter } from "better-auth/adapters/drizzle";
import { db, schema } from "./db";

export const auth = betterAuth({
  baseURL: process.env.BETTER_AUTH_URL,
  secret: process.env.BETTER_AUTH_SECRET,
  database: drizzleAdapter(db, { provider: "pg", schema }),
  emailAndPassword: { enabled: true },
  session: { expiresIn: 60 * 60 * 24 * 7 }, // 7 days  ← D-15 reduces this
  advanced: { database: { generateId: () => crypto.randomUUID() } },
});

export type Auth = typeof auth;
```

**Insertion sites for Phase 11:**

| Site | Edit |
|------|------|
| import block (after line 16) | `import { twoFactor } from "better-auth/plugins"; import { isAllowedEmail } from "./allowlist";` |
| `emailAndPassword` field (line 22) | extend to `{ enabled: true, autoSignIn: false }` (D-12: never auto-sign in pre-enrollment per Pitfall 4) |
| `session` field (line 23) | replace `expiresIn: 60 * 60 * 24 * 7` with `expiresIn: 30 * 60, updateAge: 5 * 60, cookieCache: { enabled: true, maxAge: 60 }` (D-15) |
| new top-level `rateLimit` (after session) | full block from RESEARCH §Pattern 2 — `customRules: { "/sign-in/email": { window: 900, max: 5 }, "/sign-up/email": { window: 900, max: 5 }, "/two-factor/verify-totp": { window: 60, max: 5 } }` (D-14) |
| new top-level `plugins` (after rateLimit) | `[twoFactor({ issuer: "Ifix AI Gateway" })]` (D-12 — issuer string locked in CONTEXT.md specifics line 159) |
| new top-level `databaseHooks` (after plugins) | full block from RESEARCH §Pattern 2 lines 304-315 — `user.create.before` throws on non-allowlist email (D-13) |

**Full target shape** (from RESEARCH §Pattern 2 — already drafted lines 262-317):

See RESEARCH.md lines 262-317 for the complete target `auth.ts` body. Executor copies it verbatim with these confirmations:
- `rateLimit` is BUILT-IN (top-level), NOT a plugin import (RESEARCH State of the Art row 1).
- `twoFactor` uses SHA-1 default (Google Authenticator + 1Password compatibility — verified at `@better-auth/utils/dist/otp.mjs:12`).
- `customRules` keys use Better Auth canonical paths `/sign-in/email`, `/sign-up/email`, `/two-factor/verify-totp` (NOT `/login` — that's the Next.js route, NOT the Better Auth endpoint).

**What executor must replicate:** preserve the existing `baseURL`, `secret`, `database`, `advanced` blocks verbatim; preserve `export type Auth = typeof auth;` at end-of-file; never rotate `BETTER_AUTH_SECRET` (Pitfall 5 invariant).

---

### `dashboard/src/lib/schema.ts` (MOD, Drizzle schema)

**Analog:** self — current file is 77 LOC, fully read.

**Plugin schema convention.** Better Auth's `npx @better-auth/cli migrate` auto-adds `twoFactor` table + `user.twoFactorEnabled` column when the plugin is registered (RESEARCH Assumption A6). Two valid approaches:

1. **Let CLI auto-add (preferred per RESEARCH Pitfall 7):** keep schema.ts as-is; run `bunx @better-auth/cli migrate` inside the dashboard container against prod DSN — CLI inspects loaded plugins and ALTERs as needed.

2. **Mirror in schema.ts for type safety (optional):** append a `twoFactor` table mirroring better-auth's plugin schema shape. Pattern from existing schema.ts user table (lines 13-27):

```typescript
export const twoFactor = pgTable("twoFactor", {
  id: text("id").primaryKey(),
  secret: text("secret").notNull(),
  backupCodes: text("backup_codes").notNull(),
  userId: text("user_id")
    .notNull()
    .references(() => user.id, { onDelete: "cascade" }),
});

// Extend user table with twoFactorEnabled column.
// NOTE: this column is added in-place to the existing `user` table — Drizzle
// `pgTable` does not support post-hoc extension; if mirror approach taken,
// EDIT the existing user pgTable definition to include the column.
```

**What executor must replicate:** `pgTable("name", { ... })` syntax, `text("col_name")` for IDs (Better Auth convention is text PKs not UUIDs in the dashboard tables — see existing schema.ts line 14), `withTimezone: true` on every timestamp, `onDelete: "cascade"` FKs to user.

**Recommendation:** approach #1 (CLI auto-migrate). Verify via `bunx @better-auth/cli migrate --dry-run` first (RESEARCH Pitfall 7 / Assumption A6 mitigation).

---

### `dashboard/src/middleware.ts` (MOD, Next.js edge middleware)

**Analog:** self — current file 29 LOC, fully read.

**Current logic** (lines 17-29):

```typescript
export function middleware(req: NextRequest) {
  const session = getSessionCookie(req);
  if (!session && !req.nextUrl.pathname.startsWith("/login")) {
    return NextResponse.redirect(new URL("/login", req.url));
  }
  return NextResponse.next();
}
export const config = {
  matcher: ["/((?!login|api/auth|_next|favicon).*)"],
};
```

**Insertion sites:**

| Site | Edit |
|------|------|
| `matcher` array (line 28) | add `2fa` and `signup` to exclusion list: `"/((?!login|signup|2fa|api/auth|_next|favicon).*)"` (RESEARCH §Inheritance Notes / UI-SPEC line 276) |
| inside `middleware()` body | add two-stage 2FA gate per RESEARCH Pitfall 4: cookie present + (user.twoFactorEnabled === false → redirect `/2fa/enroll`) OR (true && !session.twoFactorVerified → redirect `/2fa/challenge`) |

**Two-stage gate sketch** (RESEARCH Pitfall 4 lines 476-479):

```typescript
export async function middleware(req: NextRequest) {
  const session = getSessionCookie(req);
  if (!session) {
    if (!req.nextUrl.pathname.startsWith("/login")) {
      return NextResponse.redirect(new URL("/login?session_expired=1", req.url));
    }
    return NextResponse.next();
  }
  // Read session payload (twoFactorEnabled + twoFactorVerified) from cookie
  // cache (Better Auth cookieCache enabled in auth.ts, maxAge=60s — no DB hit).
  // Use better-auth/cookies helper to decode; if not available at edge runtime
  // (jose/Edge limitation), fall back to a `customSession` hook injected into
  // the JWT payload (RESEARCH Pitfall 4).
  // ... decision tree ...
  return NextResponse.next();
}
```

**What executor must replicate:** keep `getSessionCookie` for the cheap cookie-presence gate (Edge runtime constraint — full session validation needs Node), add `?session_expired=1` query param on logout-redirect so login page can show the session-expired Alert (UI-SPEC §Error states line 188).

---

### `dashboard/src/app/login/page.tsx` (MOD, UI page)

**Analog:** self — current file 99 LOC, fully read.

**Insertion sites:**

| Site | Edit |
|------|------|
| imports (line 11-22) | add `import { Alert, AlertDescription } from "@/components/ui/alert"; import { useSearchParams } from "next/navigation";` |
| top of `LoginPage()` function (after line 24) | `const params = useSearchParams(); const sessionExpired = params.get("session_expired") === "1"; const rateLimited = params.get("rate_limited") === "1";` |
| inside `<CardContent>` BEFORE the existing `<form>` (after line 58) | render `<Alert variant="destructive">` when `rateLimited` (D-14 copy "Muitas tentativas. Aguarde {n}s antes de tentar novamente.") + `<Alert variant="default">` when `sessionExpired` (D-15 copy "Sessão encerrada por inatividade. Faça login novamente.") |

**What executor must replicate:** preserve the existing form skeleton EXACTLY (Card + form + email + password inputs + Entrar button) — UI-SPEC §Inheritance Notes line 273 says "Existing form layout, copy unchanged"; only ADD the Alert components above the form. Use pt-BR copy from UI-SPEC §Error states table line 186-188. Countdown timer on rate-limit Alert reads `Retry-After` header from the 429 response.

---

### `gateway/cmd/gatewayctl/key.go` (MOD, Go CLI subcommand)

**Analog:** `gateway/cmd/gatewayctl/admin_key.go:runAdminKeyList` (lines 240-280, verified read 2026-05-27)

**Current dispatcher** (key.go:17-31 — verified):

```go
func runKey(ctx context.Context, args []string, log *slog.Logger) int {
    if len(args) == 0 {
        fmt.Fprintln(flag.CommandLine.Output(), "Usage: gatewayctl key create|revoke [flags]")
        return 2
    }
    switch args[0] {
    case "create":
        return runKeyCreate(ctx, args[1:], log)
    case "revoke":
        return runKeyRevoke(ctx, args[1:], log)
    default:
        // ...
    }
}
```

**Insertion sites:**

| Site | Edit |
|------|------|
| dispatcher switch (line 22-30) | add `case "list": return runKeyList(ctx, args[1:], log)` |
| Usage string (line 19) | extend to `"Usage: gatewayctl key create|revoke|list [flags]"` |
| new function after `runKeyRevoke` (line 150) | full `runKeyList` mirroring `admin_key.go:runAdminKeyList` lines 244-280 |

**Target `runKeyList` body** (mirror admin_key.go:244-280):

```go
func runKeyList(ctx context.Context, args []string, log *slog.Logger) int {
    fs := flag.NewFlagSet("key list", flag.ExitOnError)
    tenantSlug := fs.String("tenant", "", "filter by tenant slug (optional)")
    if err := fs.Parse(args); err != nil { return 2 }

    _, pool, err := loadAndPool(ctx, log)
    if err != nil { fmt.Fprintf(os.Stderr, "error: %v\n", err); return 1 }
    defer pool.Close()
    q := gen.New(pool)

    var rows []gen.ListActiveKeysByTenantRow  // or ListActiveKeysAllRow if --tenant empty
    // ... if *tenantSlug != "" → q.ListActiveKeysByTenant(ctx, tenantUUID)
    // ... else                 → q.ListActiveKeysAll(ctx)

    tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
    fmt.Fprintln(tw, "ID\tTENANT\tPREFIX\tSTATUS\tDATA_CLASS\tCREATED\tLAST_USED")
    for _, r := range rows {
        lu := "-"
        if r.LastUsedAt.Valid {
            lu = r.LastUsedAt.Time.UTC().Format(time.RFC3339)
        }
        fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
            r.ID.String(), r.TenantID.String(), r.KeyPrefix,
            r.Status, r.DataClass,
            r.CreatedAt.UTC().Format(time.RFC3339), lu)
    }
    return tw.Flush()
}
```

**SQL queries available** (verified `gateway/db/queries/auth.sql:1-28`):
- `ListActiveKeysByTenant :many` (filter by tenant_id)
- `ListActiveKeysAll :many` (no filter)
- `GetAPIKeyByID :one` (already used by revoke)

**What executor must replicate:** never print raw key (only `key_prefix`), `tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)` exactly (mirrors admin_key.go:264 spacing — UI-SPEC line 258 says "Format = aligned table via stdlib `text/tabwriter`, matching existing `gatewayctl primary state` pattern"), RFC3339 UTC timestamps, `-` for nullable last_used_at when `!Valid`.

---

### `scripts/integration-smoke/smoke-sensitive-failover.py` (MOD, race fix)

**Analog:** self — full file 849 LOC read.

**Insertion sites:**

| Site | Edit |
|------|------|
| Constants block (after line 116 `AUDIT_UPSTREAM_BLOCKED`) | add `OPEN_LIKE_STATES = frozenset({"open", "forced-open", "FORCED_OPEN"})` (D-18.1, RESEARCH Pitfall 8) |
| `ensure_tier0_open()` function (line 269) | replace `if last_state == "open":` with `if last_state in OPEN_LIKE_STATES:` |
| `induce_failure_via_gatewayctl()` function (lines 307-327) | replace stub with real invocation: `subprocess.run([gatewayctl_path, "breaker", "force-open", "--upstream=local-llm", "--ttl=300s"], check=True)` (RESEARCH State of the Art row "Deprecated" — `gatewayctl breaker force-open` LANDED Phase 06.9 Plan 04 at `gateway/cmd/gatewayctl/breaker.go:117`) |
| docstring (line 193) | update mode description: gatewayctl now invokes real `breaker force-open` (not error-out) |

**Defensive test** (RESEARCH Pitfall 8 lines 510-511):

```python
# At module-load assert (or under TestOpenLikeStates if a test suite is added):
assert "closed" not in OPEN_LIKE_STATES
assert "half-open" not in OPEN_LIKE_STATES
```

**What executor must replicate:** `frozenset` (not `set`) so the constant is immutable; whitelist EXPLICIT enumeration (never add "closed" or "half-open"); preserve every other line of the file unchanged (CR-01 invariant, schema validation tail, threat-T-09-* annotations).

---

### `gateway/docs/RUNBOOK-DEPLOY.md` (MOD, doc extension)

**Analog:** self — current file 80+ lines read.

**Two new sections** (per CONTEXT.md D-18.4 + D-19):

#### Insertion site 1: GHA retrigger workflow (D-18.4)

Insert after the "Cut-release procedure" subsection. Document:

- The `:v1.0.0` dedup symptom (CONTEXT.md D-18 line 70: "tag push não disparou build por GitHub dedup mesmo-SHA").
- The fix: `gh workflow run build-gateway.yml --ref v1.0.0 -f tag=v1.0.0` (RESEARCH Pitfall 6 lines 493-494; `workflow_dispatch.inputs.tag` already wired per RESEARCH Sources line 823: `.github/workflows/build-gateway.yml:18-23` verified).
- Fallback if dedup-suspected: delete + re-push tag (RESEARCH Pitfall 6 line 495).

#### Insertion site 2: Per-env upstream key separation (D-19)

Insert as a new top-level section "Per-env key rotation (D-19)". Document:

- 3-step operator procedure: (1) create new OR + OpenAI keys in their dashboards with label `env=prod`, (2) ssh n8n-ia-vm + sudoedit `/opt/ai-gateway-prod/.env` to swap `UPSTREAM_LLM_OPENROUTER_AUTH_BEARER`, `UPSTREAM_STT_OPENAI_AUTH_BEARER`, `UPSTREAM_EMBED_OPENAI_AUTH_BEARER`, (3) `docker compose -f /opt/ai-gateway-prod/docker-compose.yml restart ai-gateway`.
- Vast.ai falls back to shared key if Vast API doesn't support per-key labels (RESEARCH Open Question #4 + CONTEXT.md D-19 "ou continuar shared se label não suportado").

**What executor must replicate:** existing RUNBOOK-DEPLOY voice (operational, step-numbered, "Pitfall N" callouts when relevant), Markdown table for the per-env key matrix, reference to `/opt/ai-gateway-prod/.env` exact path (mode 600 invariant from CONTEXT.md line 138).

---

### `.gitignore` (MOD, config)

**Analog:** self — current file ~50 lines read.

**Insertion sites:** add at the bottom of the file (preserving existing `.claude/` block at end):

```gitignore
# Phase 11: load-test fixtures (sanitized audit_log JSONL — gitignored binary)
.planning/load-test-fixtures/

# Phase 11: LGPD signed letters (binary PDFs — gitignored)
.planning/legal/*.pdf
```

**Existing allow-rule pattern reference** (.gitignore:27-29):

```gitignore
# Phase 8 INT-02 deliberately commits ONE short real WhatsApp speech clip —
# the transcription-quality gate needs real speech, not synthetic noise.
!scripts/integration-smoke/fixtures/whatsapp-sample.ogg
!scripts/integration-smoke/fixtures/whatsapp-sample.baseline.json
```

**What executor must replicate:** explanatory comment ABOVE each new pattern (project convention — every gitignore block has a "why" comment), trailing slash on `.planning/load-test-fixtures/` (directory not file), specific glob `.planning/legal/*.pdf` (allows other Markdown files in `.planning/legal/` like the process doc to be committed — the SIGNED PDFs are what's gitignored, not the templates).

---

## Shared Patterns

### Pattern A: Secret-Once CLI Discipline

**Source:** `scripts/integration-smoke/smoke-sensitive-failover.py:151-228`

**Apply to:** Every Python script (`load-replay.py`, `audit-log-export.py`), every bash script that handles a token (`vast-delete.sh`, `seed-admins.sh`), every Go subcommand that creates keys (`debug.go`).

```python
# argparse pattern: env-var fallback BUT NO committed default.
ap.add_argument(
    "--api-key",
    default=os.getenv("SMOKE_API_KEY"),
    help="... NEVER committed, NEVER logged.",
)
if not args.api_key:
    ap.error("--api-key or SMOKE_API_KEY required ...")

# Raw key NEVER passed to structlog / slog. ONLY status/prefix logged.
log.info("operation done", gateway=cfg.gateway_url)  # NO api_key
```

```go
// gateway/cmd/gatewayctl/key.go:85-95 (verified) — raw key to stdout ONCE.
fmt.Printf("key=%s\nid=%s\nprefix=%s\n", raw, id, prefix)  // STDOUT
log.Info("api key issued", "key_prefix", prefix)            // NEVER raw key
```

---

### Pattern B: 8-Section Smoke Spine

**Source:** `scripts/integration-smoke/smoke-sensitive-failover.py` (entire file 849 lines)

**Apply to:** Any new Python smoke (`load-replay.py`, `audit-log-export.py`).

8 sections in order:
1. Module docstring (why, what, gates, exit codes)
2. `--- Constants ---` (SCHEMA_VERSION, gate thresholds, etc.)
3. `--- Config + CLI ---` (dataclass + parse_args)
4. Per-domain helpers (e.g. `--- Induced-failure pre-condition ---`, `--- Gateway requests ---`)
5. `--- Audit-DB gates ---` (psycopg work, if any)
6. `--- Gates + exit codes ---` (apply_gates + exit_code_for_gates)
7. `--- Orchestration ---` (main_async + _write_unevaluated_report)
8. `main()` (asyncio.run wrapper)

Header marker line convention: `# --- Section Name ---------` (matches column 75).

---

### Pattern C: Schema-Validate Report Tail

**Source:** `scripts/integration-smoke/smoke-sensitive-failover.py:756-783`

**Apply to:** Every Python smoke that writes a JSONL/JSON report.

```python
# Validate against the schema before writing. WR-05: a schema-INVALID report
# is a hard failure but report is STILL written (for debugging).
schema = json.loads((Path(__file__).parent / "<name>-schema.json").read_text())
try:
    Draft202012Validator(schema).validate(report)
except ValidationError as e:
    errors.append(f"report failed schema validation: {e.message}")
    schema_invalid = True
Path(cfg.out_path).write_text(json.dumps(report, indent=2, sort_keys=True))
if schema_invalid:
    return 1
```

**Why apply:** load-replay produces a report consumed by HUMAN-UAT assertion; a structurally broken report that the asserter misparses must NOT exit 0.

---

### Pattern D: gatewayctl Subcommand Shape

**Source:** `gateway/cmd/gatewayctl/admin_key.go:50-66, 240-280` + `gateway/cmd/gatewayctl/breaker.go:69-89`

**Apply to:** `debug.go` (NEW) + `key.go` list extension (MOD).

5 invariants:
1. Function signature: `runCmdName(ctx context.Context, args []string, log *slog.Logger) int`.
2. Exit codes: `0` = OK, `1` = runtime failure, `2` = usage / flag failure.
3. Flags: `flag.NewFlagSet("<cmd> <subcmd>", flag.ExitOnError)`.
4. DB access: `loadAndPool(ctx, log)` + `defer pool.Close()` + `gen.New(pool)`.
5. Output split: stdout = parseable result (one-liner or tabwriter); stderr = `log` lines + error messages.

Register in `gateway/cmd/gatewayctl/main.go:48-101` switch + extend `usage()` (lines 20-46).

---

### Pattern E: Bash Script Skeleton

**Source:** `scripts/deploy/bootstrap-postgres.sh:52-95`

**Apply to:** `scripts/chaos/vast-delete.sh` (NEW), `scripts/chaos/openrouter-iptables-drop.sh` (NEW), `scripts/dashboard/seed-admins.sh` (NEW).

```bash
#!/usr/bin/env bash
set -euo pipefail

log() { printf '[%s] [<script-name>] %s\n' "$(date -Iseconds)" "$*" >&2; }

# --- prereq: required env -------------------------------------------------
if [[ -z "${REQUIRED_VAR:-}" ]]; then
  log "FATAL: REQUIRED_VAR env var is required (not set)."
  log "  This must be ..."
  exit 1
fi

# --- main -----------------------------------------------------------------
main() {
  log "starting <description>"
  # ... idempotent operations only
  cat <<EOF

============================================================================
  SUMMARY (stdout only)
============================================================================
...
EOF
}

main "$@"
```

Invariants: `set -euo pipefail`, ISO-8601 `log()` to STDERR, fail-fast env validation with SPECIFIC pointer (not bare "missing"), idempotent re-run safety, final summary block to STDOUT only.

---

### Pattern F: Dashboard Page Layout (Card-centered auth)

**Source:** `dashboard/src/app/login/page.tsx` (entire 99 lines)

**Apply to:** `2fa/enroll/page.tsx` (NEW), `2fa/challenge/page.tsx` (NEW).

Required structural elements:
- `"use client";` directive line 1.
- Imports: `useRouter` from `next/navigation`, `useState` from `react`, shadcn `Card{,Content,Description,Header,Title}` + `Button` + `Input` from `@/components/ui/*`, `authClient` from `@/lib/auth-client`.
- Layout: `<main className="flex min-h-screen items-center justify-center p-6">` outer, `<Card className="w-full max-w-sm">` inner (UI-SPEC §Layout Constraints).
- Pattern: CardTitle (pt-BR from UI-SPEC table) + CardDescription + CardContent containing the step form.
- Error display: `<p className="text-xs text-destructive" role="alert">` for inline errors (login/page.tsx:86-89) OR `<Alert variant="destructive">` for above-form alerts.
- Submit pattern: `setLoading(true) → await authClient.x(...) → setLoading(false) → if (error) setError("pt-BR copy") else router.push("/")`.

---

### Pattern G: Runbook Markdown Header

**Source:** `gateway/docs/RUNBOOK-FAILOVER.md:1-13` + `gateway/docs/RUNBOOK-PRIMARY-POD.md:1-12`

**Apply to:** `RUNBOOK-INCIDENTS.md` (NEW), `RUNBOOK-DEPLOY.md` (MOD — already follows this).

5 elements in order:
1. `# <Name> Runbook` H1.
2. `**Phase N (`module`) <one-line scope>.** Read this when:` followed by 3-6 bulleted triggers.
3. Optional `**Last updated: YYYY-MM-DD.**` + ownership metadata.
4. `Related runbooks:` bulleted cross-ref list to ≥3 sibling docs.
5. `## Mental Model (30 seconds)` first content section.

---

### Pattern H: LGPD Doc Voice (pt-BR)

**Source:** `gateway/docs/LGPD-SUBPROCESSORS.md:1-60`

**Apply to:** `LGPD-SIGNOFF-PROCESS.md` (NEW), `LGPD-SIGNOFF-LETTER-TEMPLATE.md` (NEW).

Voice constants:
- `**Last updated: YYYY-MM-DD.**` at top.
- `## Purpose` first section, pt-BR explanatory paragraph.
- Tenant classes referenced as `data_class: sensitive` / `data_class: normal` (lowercase, backticks).
- Sub-processor terminology: "controlador-operador", "sub-processador", "failover", "upstream local" — match exact wording.
- Bold callout for the never-external invariant: `> **Garantia "never-external" para tenants sensíveis.** ...`.

---

## No Analog Found

Files with no close match in the codebase (executor leans on RESEARCH.md drafts directly):

| File | Role | Reason |
|------|------|--------|
| `gateway/docs/POSTMORTEM-TEMPLATE.md` | doc template | No postmortem template exists in-repo; `.planning/phases/10-prod-deploy-ai-gateway/10-VERIFICATION.md` has postmortem-like sections (pitfalls_hit, deviations) but is not a reusable template. Use Google SRE blameless 9-section structure (CONTEXT.md D-10 lists exact sections) per RESEARCH §Common Pitfalls anti-pattern "POSTMORTEM template sem `Lessons`". |
| `scripts/chaos/*` directory | chaos scripts | No precedent for "chaos engineering" scripts in-repo. RESEARCH.md drafted complete bodies (lines 566-619); use Pattern E (bash skeleton) for set/log/env discipline, and rely on RESEARCH-drafted bodies for actual iptables / Vast DELETE logic. |

---

## Metadata

**Analog search scope:**
- `scripts/integration-smoke/` (smoke-*.py family — 3 files, all read)
- `scripts/deploy/` (5 deploy bash scripts — bootstrap-postgres.sh + migrate-dashboard.sh read)
- `pod/scripts/` (vast-ai.sh read for Vast auth pattern)
- `gateway/cmd/gatewayctl/` (24 .go files — admin_key.go, key.go, breaker.go, main.go read in full)
- `gateway/internal/httpx/` (recoverer.go read in full)
- `gateway/internal/admin/` (middleware.go read in full)
- `gateway/docs/` (8 runbook + LGPD docs — RUNBOOK-FAILOVER, RUNBOOK-PRIMARY-POD, RUNBOOK-DEPLOY, LGPD-SUBPROCESSORS read)
- `gateway/db/queries/` + `gateway/internal/db/gen/` (verified SQL + generated Go signatures for `ListActiveKeysByTenant`, `ListActiveKeysAll`, `InsertAPIKey`, `RevokeAPIKey`)
- `dashboard/src/lib/` (auth.ts, schema.ts, auth-client.ts, smoke.test.ts read in full)
- `dashboard/src/app/login/` (page.tsx read in full)
- `dashboard/src/middleware.ts` (read in full)
- `dashboard/src/components/ui/` (listed — 18 blocks; `input-otp` + `dialog` confirmed NET-NEW)
- `.gitignore` (read in full)

**Files scanned:** 28 source files + 9 SQL/schema files + 4 docs.

**Pattern extraction date:** 2026-05-27.

**Key codebase invariants discovered:**
1. Every smoke is JSONL-validated against a sibling `*-report-schema.json` (Draft 2020-12 + `additionalProperties: false`).
2. Every Go gatewayctl subcommand follows `runCmd(ctx, args, log) int` with exit codes 0/1/2.
3. Bash scripts ALL use `set -euo pipefail` + ISO-8601 `log()` to stderr + idempotent SELECT-then-INSERT.
4. Better Auth dashboard uses TEXT primary keys (not UUID) — `dashboard/src/lib/schema.ts:14`.
5. Runbook cross-refs are reciprocal (every new runbook cites its siblings, and siblings should cite it back — Phase 11 may need light edits to existing runbooks to add the back-reference to RUNBOOK-INCIDENTS, but this is doc-only and CONTEXT.md D-11 already says cross-ref each).
