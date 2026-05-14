"""scripts/integration-smoke/smoke-sensitive-failover.py — RES-08 sensitive-class failover smoke.

Black-box proof that Phase 3's RES-08 machinery holds end-to-end against the
deployed gateway: a `data_class: sensitive` tenant request (telefonia /
cobrancas), during an INDUCED upstream failure, **fails closed** with a 503
envelope — it is NEVER proxied to OpenAI/OpenRouter, an `audit_log` row records
the decision (`upstream='blocked_sensitive'`), and ZERO `audit_log_content` rows
are persisted (D-B2 — sensitive content is never stored).

This is the SC1 verification artifact for INT-03 (Telefonia sensitive call-audio)
and the sensitive half of INT-04 (Cobranças sensitive). It composes two analogs:
the `smoke-chat-ifix.py` script skeleton (CLI, config, report-write, exit-code
contract, schema-validate tail) and the `sensitive_block_test.go` integration
test (what to assert — the in-process test's black-box equivalent).

The gates (each maps `sensitive_block_test.go` assertions to a black-box check):
  - fail_closed:    sensitive POST /v1/chat/completions while tier-0 is OPEN
                    returns 503, body contains
                    `upstream_unavailable_for_sensitive_tenant`, Retry-After: 30
  - never_external: the X-Request-ID, looked up in ai_gateway.audit_log, has
                    `upstream = 'blocked_sensitive'` — the black-box proof the
                    request never reached an external provider (the in-process
                    test asserts `tier1.hits.Load() == 0`)
  - audit_decision: an audit_log row exists for the request_id AND
                    `SELECT COUNT(*) FROM ai_gateway.audit_log_content` is 0
  - streaming_fail_fast (optional): sensitive + stream:true 503s in <500ms

WHY direct DB access: the gateway `/admin/audit` endpoint is FILTERED to
`event_kind IS NOT NULL` (FSM/state-change rows only) — it does NOT surface
request-level `blocked_sensitive` rows. So the smoke reads
`ai_gateway.audit_log` + `ai_gateway.audit_log_content` directly via the same
`AI_GATEWAY_PG_DSN` env var `provision-tenants.sh` uses. The audit_log_content
gate uses `SELECT COUNT(*)` ONLY — it never SELECTs content columns, so no
sensitive prompt/response body is ever pulled into this process (threat T-09-07).

WHY the induced-failure step is a HARD pre-condition (threat T-09-08): a GREEN
that did not actually trip the breaker would be a false positive — the sensitive
request would just succeed normally and never exercise RES-08. With
`--induce-failure-via operator-prestep` (the default) the smoke polls
`GET /v1/health/upstreams` and ONLY proceeds once `local-llm` shows `open`; if
it never opens within the bounded wait, the smoke records an `errors` entry and
exits 1 — the gates are NOT evaluated against a healthy upstream.

Secret-once discipline: the sensitive tenant key is supplied ONLY via
`--api-key` / `SMOKE_API_KEY`; the audit-DB DSN ONLY via `--pg-dsn` /
`AI_GATEWAY_PG_DSN`. Neither has a committed default, neither is ever passed to
`log()` / structlog, neither is written to the JSON report (the schema's
`additionalProperties: false` on `target` permits only `gateway_url` + `tenant`).
The script refuses to run (argparse error / clear pre-flight error, no network
or DB call) when either is absent.

Entry point:
    python scripts/integration-smoke/smoke-sensitive-failover.py \\
        --gateway-url https://gateway.ifix.com.br \\
        --api-key <telefonia or cobrancas sensitive tenant key> \\
        --pg-dsn postgres://.../ai_gateway \\
        --induce-failure-via operator-prestep \\
        --out smoke-sensitive-failover-report.json

Exit codes:
    0  all gates passed
    2  fail_closed gate failed (only)
    3  never_external gate failed (only)
    4  audit_decision gate failed (only)
    5  streaming_fail_fast gate failed (only)
    6  multiple gates failed
    1  fallback / unexpected (incl. induced-failure pre-condition not met)
"""

from __future__ import annotations

import argparse
import asyncio
import dataclasses
import json
import os
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import httpx
import structlog

# psycopg is the audit-DB reader — see the module docstring for WHY direct DB
# access is required (the /admin/audit endpoint is event_kind-filtered).
import psycopg

# --- Constants ------------------------------------------------------------

SCHEMA_VERSION = "1.0.0"

# The envelope code RES-08 returns for a sensitive tenant whose tier-0 upstream
# is OPEN and the bounded retry exhausted (gateway/internal/proxy/sensitive.go
# -> ErrSensitiveRetryExhausted -> caller maps to 503).
SENSITIVE_ENVELOPE_CODE = "upstream_unavailable_for_sensitive_tenant"

# The audit_log.upstream sentinel for a blocked sensitive request
# (Go const audit.UpstreamBlockedSensitive). Its presence in audit_log IS the
# black-box proof the request never went external.
AUDIT_UPSTREAM_BLOCKED = "blocked_sensitive"

# Expected Retry-After header value on the 503 (sensitive_block_test.go:110-112).
EXPECTED_RETRY_AFTER = "30"

# The tier-0 LLM upstream name in /v1/health/upstreams whose breaker the
# induced-failure step must observe as `open` before the gates run.
TIER0_UPSTREAM_NAME = "local-llm"

# Bounded wait for the induced-failure pre-condition: poll
# /v1/health/upstreams until local-llm shows `open`.
INDUCE_POLL_TIMEOUT_S = 30.0
INDUCE_POLL_INTERVAL_S = 2.0

# streaming_fail_fast gate: sensitive + stream:true must 503 in under this
# (sensitive_block_test.go:198-201, D-B4 — fail-fast pre-flight, no retry loop).
STREAMING_FAIL_FAST_MAX_MS = 500

log = structlog.get_logger().bind(module="SMOKE_SENSITIVE_FAILOVER")


# --- Config + CLI ---------------------------------------------------------


@dataclasses.dataclass
class Config:
    gateway_url: str
    api_key: str
    pg_dsn: str
    out_path: str
    induce_failure_via: str
    gatewayctl_path: str
    skip_streaming_gate: bool


def parse_args() -> Config:
    ap = argparse.ArgumentParser(
        description="Sensitive-failover integration smoke — RES-08 fail-closed "
        "end-to-end (Phase 9 SC1). Induces a tier-0 upstream failure, then "
        "proves a data_class=sensitive tenant request fails closed with a 503 "
        "envelope, is audited as blocked_sensitive with zero content rows, and "
        "NEVER reaches an external provider."
    )
    ap.add_argument(
        "--gateway-url",
        default=os.getenv("SMOKE_GATEWAY_URL"),
        help="base URL of the deployed gateway (e.g. https://gateway.ifix.com.br)",
    )
    ap.add_argument(
        "--api-key",
        default=os.getenv("SMOKE_API_KEY"),
        help="data_class=sensitive tenant API key (telefonia or cobrancas) — "
        "sent as Authorization: Bearer. Pass via --api-key or the "
        "SMOKE_API_KEY env var; NEVER committed, NEVER logged.",
    )
    ap.add_argument(
        "--pg-dsn",
        default=os.getenv("AI_GATEWAY_PG_DSN"),
        help="audit-DB read DSN (postgres://...) — the smoke reads "
        "ai_gateway.audit_log + audit_log_content directly because /admin/audit "
        "is event_kind-filtered. Pass via --pg-dsn or AI_GATEWAY_PG_DSN; "
        "NEVER committed, NEVER logged. Required for the audit gates.",
    )
    ap.add_argument(
        "--out",
        default=os.getenv("SMOKE_OUT", "smoke-sensitive-failover-report.json"),
        help="path to write the JSON report",
    )
    ap.add_argument(
        "--induce-failure-via",
        choices=["operator-prestep", "gatewayctl"],
        default=os.getenv("SMOKE_INDUCE_FAILURE_VIA", "operator-prestep"),
        help="how the tier-0 LLM breaker is tripped before the sensitive "
        "request. 'operator-prestep' (default): the operator kills the local "
        "llama-server / points the local-llm upstream at a dead host, and the "
        "smoke polls /v1/health/upstreams until local-llm shows `open`. "
        "'gatewayctl': invoke a gatewayctl breaker force-open subcommand "
        "(NOT IMPLEMENTED — no such subcommand exists; this mode errors out "
        "telling the operator to use operator-prestep).",
    )
    ap.add_argument(
        "--gatewayctl",
        default=os.getenv("SMOKE_GATEWAYCTL", "gatewayctl"),
        help="path to the gatewayctl binary — only used when "
        "--induce-failure-via gatewayctl",
    )
    ap.add_argument(
        "--skip-streaming-gate",
        action="store_true",
        default=os.getenv("SMOKE_SKIP_STREAMING_GATE", "").lower()
        in ("1", "true", "yes"),
        help="skip the optional streaming_fail_fast gate (sensitive + "
        "stream:true 503s in <500ms). The gate is optional per the schema.",
    )
    args = ap.parse_args()

    if not args.gateway_url:
        ap.error("--gateway-url or SMOKE_GATEWAY_URL required")
    # Secret-once: NO committed default. argparse-error with no network/DB call
    # when the sensitive tenant key is absent (threat T-09-06).
    if not args.api_key:
        ap.error(
            "--api-key or SMOKE_API_KEY required (the data_class=sensitive "
            "tenant key — telefonia or cobrancas)"
        )
    # The audit gates cannot run without DB read access — fail before any
    # gateway request (threat T-09-09: the DSN is a credential, no default).
    if not args.pg_dsn:
        ap.error(
            "--pg-dsn or AI_GATEWAY_PG_DSN required (the audit-DB read DSN — "
            "the never_external + audit_decision gates query ai_gateway.audit_log "
            "directly because /admin/audit is event_kind-filtered)"
        )

    return Config(
        gateway_url=args.gateway_url.rstrip("/"),
        api_key=args.api_key,
        pg_dsn=args.pg_dsn,
        out_path=args.out,
        induce_failure_via=args.induce_failure_via,
        gatewayctl_path=args.gatewayctl,
        skip_streaming_gate=args.skip_streaming_gate,
    )


# --- Induced-failure pre-condition ----------------------------------------


async def ensure_tier0_open(client: httpx.AsyncClient, gateway_url: str) -> dict[str, Any]:
    """Hard pre-condition: the tier-0 LLM breaker MUST be OPEN before the gates run.

    Threat T-09-08 (false GREEN): if the breaker was never tripped, the
    sensitive request would just succeed normally and never exercise RES-08.
    This polls GET /v1/health/upstreams (a 200|503 JSON body whose
    `.upstreams.local-llm.state` is one of closed|half-open|open|unknown) until
    `local-llm` shows `open`, bounded by INDUCE_POLL_TIMEOUT_S.

    Returns {opened: bool, last_state: str, error: str|None}. The caller exits 1
    (fallback) when opened is False — the gates are NOT meaningfully evaluable
    against a healthy upstream.
    """
    deadline = time.monotonic() + INDUCE_POLL_TIMEOUT_S
    last_state = "unknown"
    last_error: str | None = None
    while time.monotonic() < deadline:
        try:
            r = await client.get(gateway_url + "/v1/health/upstreams", timeout=10.0)
            # 503 is expected here — `failed` status returns 503 — so do NOT
            # gate on the HTTP code, parse the body.
            body = r.json()
            ups = body.get("upstreams", {})
            entry = ups.get(TIER0_UPSTREAM_NAME, {})
            last_state = entry.get("state", "unknown")
            if last_state == "open":
                return {"opened": True, "last_state": last_state, "error": None}
        except Exception as e:  # network / JSON error — keep polling, record last
            last_error = str(e)[:300]
        await asyncio.sleep(INDUCE_POLL_INTERVAL_S)
    return {"opened": False, "last_state": last_state, "error": last_error}


def print_operator_prestep(gateway_url: str) -> None:
    """Print the EXACT pre-step the operator must run to trip the tier-0 breaker.

    `operator-prestep` mode (the default): there is no gatewayctl breaker
    force-open subcommand, so the operator induces the failure manually. The
    smoke then polls /v1/health/upstreams (see ensure_tier0_open) to confirm.
    """
    msg = (
        "\n"
        "=== INDUCED-FAILURE PRE-STEP (operator-prestep) ===\n"
        "Before the gates can run, the tier-0 local-llm breaker MUST be OPEN.\n"
        "Run ONE of the following on the gateway host, THEN leave this smoke\n"
        "running — it polls /v1/health/upstreams for up to "
        f"{int(INDUCE_POLL_TIMEOUT_S)}s waiting for\n"
        f"`{TIER0_UPSTREAM_NAME}` to show state=open:\n"
        "\n"
        "  a) Stop the local LLM container so its health probe fails:\n"
        "       docker stop ifix-ai-pod-llm     # or: pkill -f llama-server\n"
        "\n"
        "  b) OR point the local-llm upstream URL env at a dead host and\n"
        "     restart the gateway so the breaker trips on the failed probes.\n"
        "\n"
        "Recovery after the smoke finishes: restart the LLM container / restore\n"
        "the upstream URL — the breaker auto-closes on the next healthy probe.\n"
        f"Polling {gateway_url}/v1/health/upstreams ...\n"
        "===================================================\n"
    )
    print(msg, file=sys.stderr)


def induce_failure_via_gatewayctl(gatewayctl_path: str) -> dict[str, Any]:
    """`gatewayctl` mode: there is NO breaker force-open subcommand.

    Inspected gateway/cmd/gatewayctl/ — `upstreams` has list/update/enable/
    disable only, no breaker force-open. So this mode errors out telling the
    operator to use operator-prestep. Kept as an explicit branch so the CLI
    contract (`--induce-failure-via {operator-prestep,gatewayctl}`) is honest
    rather than silently falling through.

    Returns {ok: False, error: str}.
    """
    return {
        "ok": False,
        "error": (
            "--induce-failure-via gatewayctl is not supported: gatewayctl has "
            "no breaker force-open subcommand (gateway/cmd/gatewayctl/upstreams.go "
            "exposes only list/update/enable/disable). Re-run with "
            "--induce-failure-via operator-prestep (the default) and follow the "
            "printed pre-step."
        ),
    }


# --- Gateway requests -----------------------------------------------------


async def run_fail_closed_request(
    client: httpx.AsyncClient, gateway_url: str
) -> dict[str, Any]:
    """POST /v1/chat/completions (non-streaming) with the sensitive tenant key
    while tier-0 is OPEN.

    Mirrors sensitive_block_test.go:90-115 — the black-box equivalent. Asserts
    503 + body contains `upstream_unavailable_for_sensitive_tenant` +
    `Retry-After: 30`. Also captures the `X-Request-ID` response header — the
    correlation id the audit gates query audit_log by.

    Returns {status_code, ok, retry_after, envelope_code, request_id,
             raw_error_body?}.
    `ok` is True only when ALL of (503, envelope code present, Retry-After: 30).
    """
    body = {
        "model": "qwen",
        "messages": [{"role": "user", "content": "smoke-sensitive-failover probe"}],
    }
    try:
        r = await client.post(
            gateway_url + "/v1/chat/completions",
            json=body,
            timeout=30.0,
        )
        text = r.text
        retry_after = r.headers.get("Retry-After", "")
        request_id = r.headers.get("X-Request-ID", "")
        envelope_present = SENSITIVE_ENVELOPE_CODE in text
        ok = (
            r.status_code == 503
            and envelope_present
            and retry_after == EXPECTED_RETRY_AFTER
        )
        result: dict[str, Any] = {
            "status_code": r.status_code,
            "ok": ok,
            "retry_after": retry_after,
            "envelope_code": SENSITIVE_ENVELOPE_CODE if envelope_present else "",
            "request_id": request_id,
        }
        if not ok:
            result["raw_error_body"] = text[:500]
        return result
    except Exception as e:
        return {
            "status_code": -1,
            "ok": False,
            "retry_after": "",
            "envelope_code": "",
            "request_id": "",
            "raw_error_body": str(e)[:500],
        }


async def run_streaming_fail_fast_request(
    client: httpx.AsyncClient, gateway_url: str
) -> dict[str, Any]:
    """POST /v1/chat/completions with stream:true while tier-0 is OPEN; time it.

    Mirrors sensitive_block_test.go:154-205 (D-B4) — sensitive + streaming must
    503 fail-fast in <500ms with no retry-loop pre-flight.

    Returns {status_code, ok, elapsed_ms, raw_error_body?}.
    `ok` is True only when (503 AND elapsed_ms < 500).
    """
    body = {
        "model": "qwen",
        "stream": True,
        "messages": [{"role": "user", "content": "smoke-sensitive-failover stream probe"}],
    }
    start = time.monotonic()
    try:
        r = await client.post(
            gateway_url + "/v1/chat/completions",
            json=body,
            timeout=30.0,
        )
        elapsed_ms = int((time.monotonic() - start) * 1000)
        text = r.text
        ok = r.status_code == 503 and elapsed_ms < STREAMING_FAIL_FAST_MAX_MS
        result: dict[str, Any] = {
            "status_code": r.status_code,
            "ok": ok,
            "elapsed_ms": elapsed_ms,
        }
        if not ok:
            result["raw_error_body"] = text[:500]
        return result
    except Exception as e:
        elapsed_ms = int((time.monotonic() - start) * 1000)
        return {
            "status_code": -1,
            "ok": False,
            "elapsed_ms": elapsed_ms,
            "raw_error_body": str(e)[:500],
        }


# --- Audit-DB gates -------------------------------------------------------


def query_audit(pg_dsn: str, request_id: str) -> dict[str, Any]:
    """Query ai_gateway.audit_log + audit_log_content for the request_id.

    The black-box equivalent of sensitive_block_test.go:126-148:
      - audit_log.upstream MUST equal `blocked_sensitive` — this IS the proof
        the request never reached an external provider (never_external gate)
      - an audit_log row MUST exist for the request_id (audit_decision)
      - COUNT(*) on audit_log_content MUST be 0 — D-B2, sensitive content is
        never persisted (audit_decision)

    Threat T-09-07: the audit_log_content query is `SELECT COUNT(*)` ONLY — it
    NEVER selects content columns, so no sensitive prompt/response body is ever
    pulled into this process. The DSN (threat T-09-09) is used here and is never
    logged or written to the report.

    Returns {ok, audit_log_row_found, audit_upstream, audit_log_content_rows,
             error?}.
    """
    result: dict[str, Any] = {
        "ok": False,
        "audit_log_row_found": False,
        "audit_upstream": "",
        "audit_log_content_rows": -1,
    }
    if not request_id:
        result["error"] = (
            "no X-Request-ID captured from the fail_closed request — cannot "
            "correlate the audit_log row"
        )
        return result
    try:
        with psycopg.connect(pg_dsn, connect_timeout=10) as conn:
            with conn.cursor() as cur:
                cur.execute(
                    "SELECT upstream FROM ai_gateway.audit_log "
                    "WHERE request_id = %s",
                    (request_id,),
                )
                row = cur.fetchone()
                if row is not None:
                    result["audit_log_row_found"] = True
                    # upstream is a nullable column; render NULL as "".
                    result["audit_upstream"] = row[0] if row[0] is not None else ""
                # COUNT(*) ONLY — never the content columns (threat T-09-07).
                cur.execute(
                    "SELECT COUNT(*) FROM ai_gateway.audit_log_content "
                    "WHERE request_id = %s",
                    (request_id,),
                )
                count_row = cur.fetchone()
                result["audit_log_content_rows"] = (
                    int(count_row[0]) if count_row is not None else -1
                )
    except Exception as e:
        # Do NOT include the DSN in the error string (threat T-09-09).
        result["error"] = f"audit-DB query failed: {str(e)[:300]}"
        return result

    result["ok"] = (
        result["audit_log_row_found"]
        and result["audit_upstream"] == AUDIT_UPSTREAM_BLOCKED
        and result["audit_log_content_rows"] == 0
    )
    return result


# --- Gates + exit codes ---------------------------------------------------


def apply_gates(report: dict[str, Any], streaming_evaluated: bool) -> dict[str, bool]:
    """Derive the per-gate booleans + all_passed from the per-check objects.

    - fail_closed:         report.fail_closed.ok (503 + envelope + Retry-After:30)
    - never_external:      report.never_external.ok (audit_log.upstream ==
                           blocked_sensitive — the black-box proof of no
                           external routing)
    - audit_decision:      report.audit_decision.ok (audit_log row found AND
                           audit_log_content_rows == 0)
    - streaming_fail_fast: report.streaming_fail_fast.ok — ONLY included when
                           the streaming gate was evaluated (it is optional per
                           the schema; skipped via --skip-streaming-gate)
    - all_passed:          all evaluated gates above
    """
    gates: dict[str, bool] = {
        "fail_closed": report["fail_closed"].get("ok") is True,
        "never_external": report["never_external"].get("ok") is True,
        "audit_decision": report["audit_decision"].get("ok") is True,
    }
    if streaming_evaluated:
        gates["streaming_fail_fast"] = (
            report["streaming_fail_fast"].get("ok") is True
        )
    gates["all_passed"] = all(
        v for k, v in gates.items() if k != "all_passed"
    )
    return gates


def exit_code_for_gates(gates: dict[str, bool]) -> int:
    """Map gate failures to distinct non-zero exit codes (bitmask pattern).

    Same shape as the chat-ifix / converseai smokes:
    0 all pass; 2/3/4/5 single-gate failure; 6 multiple; 1 fallback.
      2 = fail_closed, 3 = never_external, 4 = audit_decision,
      5 = streaming_fail_fast
    """
    if gates["all_passed"]:
        return 0
    failing = 0
    if not gates["fail_closed"]:
        failing |= 0b0001
    if not gates["never_external"]:
        failing |= 0b0010
    if not gates["audit_decision"]:
        failing |= 0b0100
    # streaming_fail_fast is optional — only counts when it was evaluated.
    if "streaming_fail_fast" in gates and not gates["streaming_fail_fast"]:
        failing |= 0b1000
    if bin(failing).count("1") > 1:
        return 6
    if failing == 0b0001:
        return 2
    if failing == 0b0010:
        return 3
    if failing == 0b0100:
        return 4
    if failing == 0b1000:
        return 5
    return 1


# --- Orchestration --------------------------------------------------------


async def main_async(cfg: Config) -> int:
    # NOTE: cfg.api_key and cfg.pg_dsn are NEVER passed to log() (threats
    # T-09-06 / T-09-09).
    log.info(
        "smoke starting",
        gateway=cfg.gateway_url,
        induce_failure_via=cfg.induce_failure_via,
        skip_streaming_gate=cfg.skip_streaming_gate,
    )

    started_at = datetime.now(timezone.utc).isoformat()
    errors: list[str] = []

    # --- Step 1: induce the tier-0 failure (HARD pre-condition) -----------
    # Threat T-09-08: the gates are NOT evaluated against a healthy upstream.
    if cfg.induce_failure_via == "gatewayctl":
        gw_result = induce_failure_via_gatewayctl(cfg.gatewayctl_path)
        if not gw_result["ok"]:
            log.error("induced-failure step failed", err=gw_result["error"])
            errors.append(gw_result["error"])
            _write_unevaluated_report(cfg, started_at, errors)
            return 1
    else:
        print_operator_prestep(cfg.gateway_url)

    # The single client carries the sensitive tenant key on every request.
    async with httpx.AsyncClient(
        headers={"Authorization": f"Bearer {cfg.api_key}"}
    ) as client:
        induce = await ensure_tier0_open(client, cfg.gateway_url)
        if not induce["opened"]:
            msg = (
                f"induced-failure pre-condition not met: {TIER0_UPSTREAM_NAME} "
                f"breaker never reached state=open within "
                f"{int(INDUCE_POLL_TIMEOUT_S)}s (last state="
                f"{induce['last_state']!r}). The gates cannot be meaningfully "
                "evaluated against a healthy upstream — run the operator "
                "pre-step first."
            )
            if induce.get("error"):
                msg += f" Last poll error: {induce['error']}"
            log.error("induced-failure pre-condition not met", detail=msg)
            errors.append(msg)
            _write_unevaluated_report(cfg, started_at, errors)
            return 1

        log.info("tier-0 breaker confirmed OPEN — running gates")

        # --- Step 2: fail_closed gate -------------------------------------
        fail_closed = await run_fail_closed_request(client, cfg.gateway_url)
        request_id = fail_closed.pop("request_id", "")
        if not fail_closed["ok"] and fail_closed.get("raw_error_body"):
            errors.append(f"fail_closed: {fail_closed['raw_error_body']}")

        # --- Step 3: streaming_fail_fast gate (optional) ------------------
        streaming_evaluated = not cfg.skip_streaming_gate
        streaming_fail_fast: dict[str, Any] | None = None
        if streaming_evaluated:
            streaming_fail_fast = await run_streaming_fail_fast_request(
                client, cfg.gateway_url
            )
            if not streaming_fail_fast["ok"] and streaming_fail_fast.get(
                "raw_error_body"
            ):
                errors.append(
                    f"streaming_fail_fast: {streaming_fail_fast['raw_error_body']}"
                )

    # --- Step 4: audit-DB gates (never_external + audit_decision) ---------
    # Runs once after the gateway requests — sync psycopg is fine here.
    audit = query_audit(cfg.pg_dsn, request_id)
    if audit.get("error"):
        errors.append(audit["error"])

    finished_at = datetime.now(timezone.utc).isoformat()

    # never_external: the audit_log.upstream == blocked_sensitive value IS the
    # black-box proof the request never went external.
    never_external_ok = (
        audit["audit_log_row_found"]
        and audit["audit_upstream"] == AUDIT_UPSTREAM_BLOCKED
    )
    never_external: dict[str, Any] = {
        "status_code": fail_closed["status_code"],
        "ok": never_external_ok,
        "audit_upstream": audit["audit_upstream"],
    }
    if not never_external_ok and audit.get("error"):
        never_external["raw_error_body"] = audit["error"]

    # audit_decision: row found AND zero content rows.
    audit_decision: dict[str, Any] = {
        "status_code": fail_closed["status_code"],
        "ok": audit["ok"],
        "audit_log_row_found": audit["audit_log_row_found"],
        "audit_log_content_rows": max(audit["audit_log_content_rows"], 0),
    }
    if not audit["ok"] and audit.get("error"):
        audit_decision["raw_error_body"] = audit["error"]

    report: dict[str, Any] = {
        "schema_version": SCHEMA_VERSION,
        "started_at": started_at,
        "finished_at": finished_at,
        "target": {
            "gateway_url": cfg.gateway_url,
            # The tenant SLUG only — NEVER the key value (threat T-09-06,
            # enforced by the schema's additionalProperties: false on target).
            "tenant": "sensitive",
        },
        "fail_closed": fail_closed,
        "never_external": never_external,
        "audit_decision": audit_decision,
        "errors": errors,
        "gates": {},  # filled in below
    }
    if streaming_fail_fast is not None:
        report["streaming_fail_fast"] = streaming_fail_fast

    report["gates"] = apply_gates(report, streaming_evaluated=streaming_fail_fast is not None)

    # git_sha (optional, best-effort).
    try:
        sha = subprocess.check_output(
            ["git", "rev-parse", "--short", "HEAD"],
            cwd=Path(__file__).resolve().parents[2],
            stderr=subprocess.DEVNULL,
        ).decode().strip()
        if sha:
            report["git_sha"] = sha
    except Exception:
        pass

    # Validate against the schema before writing — warn-don't-fail on mismatch
    # (a schema drift should not crash a smoke that produced a real report).
    try:
        from jsonschema import Draft202012Validator

        schema = json.loads(
            (
                Path(__file__).parent / "sensitive-failover-report-schema.json"
            ).read_text()
        )
        Draft202012Validator(schema).validate(report)
    except Exception as e:
        log.warning(
            "report does not match schema; writing anyway for debugging",
            err=str(e),
        )

    Path(cfg.out_path).write_text(json.dumps(report, indent=2, sort_keys=True))
    log.info("report written", path=cfg.out_path, gates=report["gates"])

    code = exit_code_for_gates(report["gates"])
    if code != 0:
        log.error("RES-08 SENSITIVE-FAILOVER GATES FAILED", gates=report["gates"], exit=code)
    else:
        log.info("RES-08 SENSITIVE-FAILOVER GATES PASSED")
    return code


def _write_unevaluated_report(
    cfg: Config, started_at: str, errors: list[str]
) -> None:
    """Write a minimal schema-shaped report when the induced-failure
    pre-condition was not met and the gates were NEVER evaluated.

    Threat T-09-08: a run that did not trip the breaker must NOT produce a
    GREEN — every gate is False, all_passed is False, and the `errors` array
    carries the reason. The HUMAN-UAT asserter sees exit 1 + all_passed:false.
    """
    finished_at = datetime.now(timezone.utc).isoformat()
    unevaluated_check: dict[str, Any] = {"status_code": -1, "ok": False}
    report: dict[str, Any] = {
        "schema_version": SCHEMA_VERSION,
        "started_at": started_at,
        "finished_at": finished_at,
        "target": {"gateway_url": cfg.gateway_url, "tenant": "sensitive"},
        "fail_closed": {**unevaluated_check, "retry_after": "", "envelope_code": ""},
        "never_external": {**unevaluated_check, "audit_upstream": ""},
        "audit_decision": {
            **unevaluated_check,
            "audit_log_row_found": False,
            "audit_log_content_rows": 0,
        },
        "errors": errors,
        "gates": {
            "fail_closed": False,
            "never_external": False,
            "audit_decision": False,
            "all_passed": False,
        },
    }
    try:
        from jsonschema import Draft202012Validator

        schema = json.loads(
            (
                Path(__file__).parent / "sensitive-failover-report-schema.json"
            ).read_text()
        )
        Draft202012Validator(schema).validate(report)
    except Exception as e:
        log.warning(
            "unevaluated report does not match schema; writing anyway",
            err=str(e),
        )
    Path(cfg.out_path).write_text(json.dumps(report, indent=2, sort_keys=True))
    log.info("unevaluated report written", path=cfg.out_path)


def main() -> None:
    cfg = parse_args()
    code = asyncio.run(main_async(cfg))
    sys.exit(code)


if __name__ == "__main__":
    main()
