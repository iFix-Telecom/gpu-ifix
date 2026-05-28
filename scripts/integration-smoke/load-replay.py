"""scripts/integration-smoke/load-replay.py — PRD-01 sustained load replay.

Replay a sanitized audit_log JSONL fixture (produced by audit-log-export.py)
against the deployed ai-gateway and emit a D-04 SLO v1.0 gates report.

Per CONTEXT.md D-05 + D-06: this is a Python asyncio + httpx async script
disparado from ops-claude (10.10.10.10) over HTTPS through the public NAT
to vps-ifix-vm:443 (edge Traefik) → n8n-ia-vm:8080 → ai-gateway container.
The replay preserves the original audit-log timing per record via
`asyncio.sleep(_replay_delay_s / args.speedup)` and bounds concurrency with
`asyncio.Semaphore(max_concurrency)`.

This script closes the cross-AI review feedback:

  - [reviews MEDIUM #2] Tenant API keys are resolved at runtime from env
    `IFIX_KEY_<TENANT_SLUG_UPPER>` (uppercase, hyphens→underscores). The
    JSONL fixture carries ONLY `tenant_slug`; there is NO replay-key field
    in the fixture file. At startup the script enumerates the unique
    tenant_slug values, computes the env-var names, and exits 2 BEFORE any
    HTTP traffic flows if any required env var is missing or empty.

  - [reviews MEDIUM #3] `/v1/audio/transcriptions` is replayed as
    multipart/form-data using the canonical stub fixture
    (scripts/integration-smoke/fixtures/whatsapp-sample.ogg). Migration
    0003 D-B6 invariant: audio bytes are NEVER persisted in
    audit_log_content, so they cannot be reconstructed from DB. JSON
    Content-Type does NOT apply to STT routes — httpx's `files=` kwarg
    computes the multipart boundary automatically.

  - [reviews LOW #4] `zero_5xx_panic` is an external observation, not a
    body-content heuristic. This script writes `null` for the gate and
    populates `gates_external_inputs.sentry_query_url` +
    `log_grep_command` so the operator can flip the gate post-run from a
    Sentry query (level=fatal AND service=ifix-ai-gateway over the run
    window) AND a gateway-log grep (count of lines matching `^panic:`).
    `all_passed` is null until every gate has a non-null value.

  - [reviews LOW #5] Optional `--speedup` multiplier compresses replay
    timing (Gemini suggestion) when the chosen audit_log window is too
    quiet to hit saturation. Replay delays are divided by this value;
    `<= 0` is rejected with exit 2.

  <pitfalls>
  STT replay uses fixed stub audio (whatsapp-sample.ogg) because
  audit_log_content does NOT persist audio bytes per migration 0003 D-B6.
  Replay therefore measures the STT pipeline latency on a representative
  file, NOT on the literal original utterance. This is a deliberate
  limitation, documented here so the load-test reader does not assume
  byte-faithful replay.
  </pitfalls>

Secret-once discipline (Pattern A): tenant API keys come ONLY from env
vars at startup. No committed default key. No fixture-embedded secret.
The script logs only the env var NAME plus a 4-character prefix of the
key value for diagnostics — NEVER the full key (threat T-11-LOAD-05).

Entry point:
    python scripts/integration-smoke/load-replay.py \\
        --gateway-url https://ai-gateway.converse-ai.app \\
        --fixture .planning/load-test-fixtures/audit-export-2026-05-27T14-00-00Z.jsonl \\
        --duration 1800 \\
        --out load-replay-report.json

Exit codes:
    0  ok (report written + schema validated)
    1  runtime error (HTTP transport, IO, schema invalid)
    2  usage error (missing env var, --speedup<=0, missing args)
"""

# Module docstring -----------------------------------------------------------

from __future__ import annotations

# Constants -------------------------------------------------------------------

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
from jsonschema import Draft202012Validator
from jsonschema.exceptions import ValidationError

SCHEMA_VERSION = "1.0.0"

# D-04 SLO v1.0 gate thresholds (CONTEXT.md). Locked verbatim — these names
# also appear in load-replay-report-schema.json. Do NOT rename.
P95_CHAT_MS_LIMIT = 5000
P95_EMBED_MS_LIMIT = 1000
P95_STT_MS_LIMIT = 10000
ERROR_RATE_LIMIT_FRACTION = 0.01  # 1%

# Routes (must match the values stored in audit_log.route).
CHAT_ROUTE = "/v1/chat/completions"
EMBED_ROUTE = "/v1/embeddings"
STT_ROUTE = "/v1/audio/transcriptions"

# Sentinel for the multipart marker emitted by audit-log-export.py.
AUDIO_STUB_MARKER_KEY = "_replay_audio_part"
AUDIO_STUB_MIME_KEY = "_replay_audio_mime"

# Pre-vetted error_class labels (schema enforces additionalProperties:false
# on the errors[] objects). NEVER attach raw bodies or headers to these.
ERROR_TIMEOUT = "timeout"
ERROR_CONNECTION_REFUSED = "connection_refused"
ERROR_CONNECTION_RESET = "connection_reset"
ERROR_SCHEMA_INVALID = "schema_invalid"
ERROR_UPSTREAM_5XX = "upstream_5xx"

log = structlog.get_logger().bind(module="LOAD_REPLAY")


# Config+CLI ------------------------------------------------------------------


@dataclasses.dataclass
class Config:
    gateway_url: str
    fixture_path: str
    duration_s: float
    max_concurrency: int
    out_path: str
    speedup: float
    audio_stub_path: str


def parse_args() -> Config:
    """Parse argv. Secret-once: no committed default for tenant keys.

    The tenant API keys are resolved at runtime from env vars (see
    resolve_tenant_keys), NOT from CLI flags — that closes reviews MEDIUM #2.
    """
    ap = argparse.ArgumentParser(
        description=(
            "PRD-01 sustained load replay (D-04 SLO v1.0). Reads sanitized "
            "audit_log JSONL fixture and replays against the deployed "
            "ai-gateway, preserving original timing modulo --speedup. Writes "
            "a gates report against load-replay-report-schema.json."
        )
    )
    ap.add_argument(
        "--gateway-url",
        default=os.getenv("SMOKE_GATEWAY_URL", "https://ai-gateway.converse-ai.app"),
        help="base URL of the deployed gateway (default: prod env var SMOKE_GATEWAY_URL "
        "or https://ai-gateway.converse-ai.app)",
    )
    ap.add_argument(
        "--fixture",
        required=False,
        help="JSONL fixture path (output of audit-log-export.py).",
    )
    ap.add_argument(
        "--duration",
        type=float,
        default=1800.0,
        help="sustained run window in seconds (D-03 default 1800 = 30 min).",
    )
    ap.add_argument(
        "--max-concurrency",
        type=int,
        default=50,
        help="max in-flight HTTP requests (default 50).",
    )
    ap.add_argument(
        "--out",
        required=False,
        help="output report JSON path (validated against load-replay-report-schema.json).",
    )
    ap.add_argument(
        "--speedup",
        type=float,
        default=1.0,
        help=(
            "OPTIONAL [reviews LOW #5] Multiplier applied to fixture replay "
            "delays — `_replay_delay_s` is divided by this value. Use to "
            "compress traffic (e.g. --speedup 2.0) when the chosen audit_log "
            "window is too quiet to hit saturation. Values <= 0 are rejected."
        ),
    )
    ap.add_argument(
        "--audio-stub",
        default=os.getenv(
            "SMOKE_AUDIO_STUB",
            "scripts/integration-smoke/fixtures/whatsapp-sample.ogg",
        ),
        help=(
            "path to the canonical OGG/Opus stub used for "
            "/v1/audio/transcriptions multipart replay. Migration 0003 D-B6 "
            "invariant: audio bytes are NEVER stored in audit_log_content, "
            "so this fixture is the only legitimate source for STT replay."
        ),
    )
    args = ap.parse_args()

    if not args.fixture:
        ap.error("--fixture required (JSONL fixture path from audit-log-export.py)")
    if not args.out:
        ap.error("--out required (report.json output path)")
    if args.speedup <= 0:
        ap.error("--speedup must be > 0 (got %r)" % (args.speedup,))

    return Config(
        gateway_url=args.gateway_url.rstrip("/"),
        fixture_path=args.fixture,
        duration_s=float(args.duration),
        max_concurrency=int(args.max_concurrency),
        out_path=args.out,
        speedup=float(args.speedup),
        audio_stub_path=args.audio_stub,
    )


# Helpers ---------------------------------------------------------------------


def _tenant_env_name(slug: str) -> str:
    """Compute the env-var name for a tenant_slug.

    Closes reviews MEDIUM #2 — slug `chat-ifix` becomes `IFIX_KEY_CHAT_IFIX`.
    Uppercase + hyphens→underscores. The slug originates from
    ai_gateway.tenants.slug (verified migration 0001).
    """
    return "IFIX_KEY_" + slug.upper().replace("-", "_")


def resolve_tenant_keys(fixture_path: Path) -> dict[str, str]:
    """Pre-flight: scan fixture, resolve env-var keys, hard-fail if missing.

    Returns dict tenant_slug → api_key. Raises SystemExit(2) BEFORE any HTTP
    traffic flows when any required env var is missing OR empty (threat
    T-11-LOAD-05). Logs only the env-var NAME + 4-char prefix of the key for
    diagnostics — NEVER the full key value.
    """
    slugs: set[str] = set()
    with fixture_path.open(encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                rec = json.loads(line)
            except json.JSONDecodeError:
                continue
            slug = rec.get("tenant_slug")
            if isinstance(slug, str) and slug:
                slugs.add(slug)

    keys: dict[str, str] = {}
    missing: list[str] = []
    for slug in sorted(slugs):
        env_name = _tenant_env_name(slug)
        val = os.environ.get(env_name, "")
        if not val:
            missing.append(env_name)
            continue
        keys[slug] = val
        log.info(
            "tenant key resolved",
            tenant_slug=slug,
            env_var=env_name,
            key_prefix=val[:4] + "...",
        )

    if missing:
        # NEVER log the value — only the var names.
        log.error(
            "missing tenant API key env vars; aborting BEFORE traffic flows",
            missing_env_vars=missing,
        )
        sys.exit(2)

    return keys


def _git_sha() -> str | None:
    """Return short git SHA of HEAD or None if not in a git checkout."""
    try:
        out = subprocess.run(
            ["git", "rev-parse", "--short=12", "HEAD"],
            capture_output=True,
            text=True,
            check=False,
            timeout=2.0,
        )
        if out.returncode == 0:
            sha = out.stdout.strip()
            if sha:
                return sha
    except Exception:
        pass
    return None


def _ranked_percentiles(latencies: list[int]) -> tuple[int, int, int]:
    """Return (p50, p95, p99) using integer-rank lookup; no numpy."""
    if not latencies:
        return 0, 0, 0
    s = sorted(latencies)
    n = len(s)
    p50 = s[min(n - 1, n // 2)]
    p95 = s[min(n - 1, int(n * 0.95))]
    p99 = s[min(n - 1, int(n * 0.99))]
    return p50, p95, p99


# Gateway requests ------------------------------------------------------------


def _classify_error(exc: BaseException, status_code: int) -> str | None:
    """Map an exception or 5xx to a pre-vetted short error_class label.

    Returns None for clean 2xx/4xx (not an error from the gates' POV).
    """
    if isinstance(exc, asyncio.TimeoutError) or isinstance(exc, httpx.TimeoutException):
        return ERROR_TIMEOUT
    if isinstance(exc, httpx.ConnectError):
        msg = str(exc).lower()
        if "refused" in msg:
            return ERROR_CONNECTION_REFUSED
        if "reset" in msg:
            return ERROR_CONNECTION_RESET
        return ERROR_CONNECTION_REFUSED
    if isinstance(exc, httpx.ReadError):
        return ERROR_CONNECTION_RESET
    if status_code >= 500:
        return ERROR_UPSTREAM_5XX
    return None


async def replay_one(
    client: httpx.AsyncClient,
    cfg: Config,
    rec: dict[str, Any],
    keys: dict[str, str],
    results: list[dict[str, Any]],
) -> None:
    """Replay one JSONL record. Closes reviews MEDIUM #2 + MEDIUM #3."""
    slug = rec.get("tenant_slug") or ""
    route = rec.get("route") or ""
    request_id = rec.get("request_id") or ""
    body = rec.get("_sanitized_body") or {}
    api_key = keys.get(slug, "")

    if not api_key:
        # Should never happen — resolve_tenant_keys is the gate.
        results.append(
            {
                "request_id": request_id,
                "tenant_slug": slug,
                "route": route,
                "status_code": -1,
                "elapsed_ms": 0,
                "upstream": None,
                "error_class": ERROR_SCHEMA_INVALID,
            }
        )
        return

    headers = {
        "Authorization": f"Bearer {api_key}",
        "X-Idempotency-Key": request_id,
    }
    url = cfg.gateway_url + route

    t0 = time.monotonic()
    status_code = -1
    upstream: str | None = None
    error_class: str | None = None

    try:
        if route == STT_ROUTE:
            # Closes reviews MEDIUM #3: multipart/form-data with stub OGG.
            # httpx computes the multipart boundary from `files=`; do NOT set
            # Content-Type manually. The body dict may carry `model`,
            # `language`, and other Whisper form fields recovered from the
            # sanitized prompt — pass them through as form fields (minus the
            # internal markers).
            form_data: dict[str, str] = {}
            for k, v in body.items():
                if k in (AUDIO_STUB_MARKER_KEY, AUDIO_STUB_MIME_KEY):
                    continue
                if isinstance(v, (str, int, float)):
                    form_data[k] = str(v)
            form_data.setdefault("model", "whisper-1")
            form_data.setdefault("language", "pt")
            mime = body.get(AUDIO_STUB_MIME_KEY) or "audio/ogg"
            audio_path = Path(cfg.audio_stub_path)
            with audio_path.open("rb") as fp:
                files = {"file": (audio_path.name, fp.read(), mime)}
            r = await client.post(url, headers=headers, data=form_data, files=files)
        else:
            # JSON path — application/json with byte-exact framing.
            r = await client.post(
                url,
                headers={**headers, "Content-Type": "application/json"},
                content=json.dumps(body).encode("utf-8"),
            )
        status_code = r.status_code
        upstream = r.headers.get("X-Upstream") or None
        # WR-03: classify by status code directly on the success path —
        # no exception happened, so feeding _classify_error a sentinel
        # BaseException() was opaque. A 5xx response from the gateway
        # still counts as ERROR_UPSTREAM_5XX; 2xx/4xx are clean.
        error_class = ERROR_UPSTREAM_5XX if status_code >= 500 else None
    except Exception as e:  # WR-03: NOT BaseException — preserve
        # KeyboardInterrupt + SystemExit + GeneratorExit. Operator pressing
        # Ctrl-C during a 30-min replay used to be absorbed here and
        # recorded as a timeout; the script would only stop at the
        # orchestrator signal layer. Catch only transport errors now.
        error_class = _classify_error(e, status_code) or ERROR_TIMEOUT

    elapsed_ms = int((time.monotonic() - t0) * 1000)

    # Only the pre-vetted metric columns — no raw bodies, no headers.
    results.append(
        {
            "request_id": request_id,
            "tenant_slug": slug,
            "route": route,
            "status_code": status_code,
            "elapsed_ms": elapsed_ms,
            "upstream": upstream,
            "error_class": error_class,
        }
    )


# Gates+exit ------------------------------------------------------------------


def compute_gates(
    summary_by_route: dict[str, dict[str, int]],
    total_requests: int,
    error_count_non_503: int,
) -> dict[str, Any]:
    """Compute D-04 SLO v1.0 gate booleans.

    Closes reviews LOW #4: `zero_5xx_panic` is set to None (the operator
    flips it post-run); `all_passed` is None whenever any gate is None.
    """
    chat = summary_by_route.get(CHAT_ROUTE, {})
    embed = summary_by_route.get(EMBED_ROUTE, {})
    stt = summary_by_route.get(STT_ROUTE, {})

    p95_chat_ok = chat.get("p95_ms", 0) <= P95_CHAT_MS_LIMIT if chat else True
    p95_embed_ok = embed.get("p95_ms", 0) <= P95_EMBED_MS_LIMIT if embed else True
    p95_stt_ok = stt.get("p95_ms", 0) <= P95_STT_MS_LIMIT if stt else True

    error_rate_ok = True
    if total_requests > 0:
        error_rate_ok = (error_count_non_503 / total_requests) < ERROR_RATE_LIMIT_FRACTION

    # Closes reviews LOW #4: engine writes None — operator MUST verify externally.
    zero_5xx_panic: bool | None = None

    booleans = [p95_chat_ok, p95_embed_ok, p95_stt_ok, error_rate_ok]
    all_passed: bool | None
    if zero_5xx_panic is None:
        all_passed = None
    else:
        all_passed = all(booleans) and zero_5xx_panic

    return {
        "p95_chat_ms_le_5000": bool(p95_chat_ok),
        "p95_embed_ms_le_1000": bool(p95_embed_ok),
        "p95_stt_ms_le_10000": bool(p95_stt_ok),
        "error_rate_lt_1pct": bool(error_rate_ok),
        "zero_5xx_panic": zero_5xx_panic,
        "all_passed": all_passed,
    }


def build_gates_external_inputs(
    started_at: str, finished_at: str, gateway_url: str
) -> dict[str, Any]:
    """Audit-trail block — Sentry query + log grep + verified_at:null.

    Closes reviews LOW #4: this block documents the dual external observation
    the operator MUST perform before flipping zero_5xx_panic. verified_at is
    None until the operator sets it.
    """
    sentry_url = (
        "https://ifix.sentry.io/issues/?project=ifix-ai-gateway-prod&level=fatal"
        f"&statsPeriod=custom&start={started_at}&end={finished_at}"
    )
    log_grep = (
        f"docker logs ifix-ai-gateway --since {started_at} --until {finished_at} "
        '| grep -c "^panic:"'
    )
    return {
        "sentry_query_url": sentry_url,
        "log_grep_command": log_grep,
        "verified_at": None,
    }


# Orchestration ---------------------------------------------------------------


async def orchestrate(cfg: Config, keys: dict[str, str]) -> dict[str, Any]:
    """Read JSONL, replay records, compute summary + gates."""
    started_dt = datetime.now(tz=timezone.utc)
    started_at = started_dt.strftime("%Y-%m-%dT%H:%M:%SZ")

    # WR-04: bounded queue + fixed consumer pool replaces the previous
    # task-per-record spawn loop. The prior shape created one
    # asyncio.Task plus one closure capturing `rec` for EVERY fixture
    # line; for a 30-min replay at 50 req/s that was 90k+ task handles
    # and 90k+ result records held in memory the entire run. The new
    # shape caps in-flight work at cfg.max_concurrency, and the queue
    # bounds backpressure into the fixture-read loop.
    results: list[dict[str, Any]] = []
    queue_max = max(cfg.max_concurrency * 2, 8)
    queue: asyncio.Queue[dict[str, Any] | None] = asyncio.Queue(maxsize=queue_max)

    timeout = httpx.Timeout(60.0, connect=10.0)
    async with httpx.AsyncClient(http2=False, timeout=timeout) as client:
        async def consumer() -> None:
            while True:
                rec = await queue.get()
                try:
                    if rec is None:
                        return
                    await replay_one(client, cfg, rec, keys, results)
                finally:
                    queue.task_done()

        consumers: list[asyncio.Task[None]] = [
            asyncio.create_task(consumer()) for _ in range(cfg.max_concurrency)
        ]

        t_start = time.monotonic()
        try:
            with Path(cfg.fixture_path).open(encoding="utf-8") as f:
                for line in f:
                    if time.monotonic() - t_start >= cfg.duration_s:
                        break
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        rec = json.loads(line)
                    except json.JSONDecodeError:
                        continue

                    delay_s = float(rec.get("_replay_delay_s", 0.0))
                    # Closes reviews LOW #5: --speedup divides the delay.
                    if delay_s > 0.0:
                        await asyncio.sleep(delay_s / cfg.speedup)

                    # put() blocks when the queue is full — natural
                    # backpressure into the fixture-read loop. Tasks
                    # consume in parallel up to max_concurrency.
                    await queue.put(rec)
        finally:
            # Signal each consumer to exit. queue.join() then waits for
            # all in-flight work + sentinels to be picked up.
            for _ in consumers:
                await queue.put(None)
            await asyncio.gather(*consumers, return_exceptions=False)

    finished_dt = datetime.now(tz=timezone.utc)
    finished_at = finished_dt.strftime("%Y-%m-%dT%H:%M:%SZ")

    # Summary aggregation
    lat_by_route: dict[str, list[int]] = {}
    err_counts: dict[str, int] = {}
    total = 0
    err_count_non_503 = 0
    for res in results:
        total += 1
        lat_by_route.setdefault(res["route"], []).append(int(res["elapsed_ms"]))
        sc = int(res["status_code"])
        ec = res.get("error_class")
        if ec:
            err_counts[ec] = err_counts.get(ec, 0) + 1
        # D-04 "error <1% non-503": exclude 503s from the count (sensitive
        # tenant blocks expected per RES-08).
        is_error = (sc >= 500 or sc < 0)
        if is_error and sc != 503:
            err_count_non_503 += 1

    summary_routes: dict[str, dict[str, int]] = {}
    for route, lats in lat_by_route.items():
        p50, p95, p99 = _ranked_percentiles(lats)
        summary_routes[route] = {
            "n": len(lats),
            "p50_ms": p50,
            "p95_ms": p95,
            "p99_ms": p99,
        }

    errors_array = [
        {"error_class": k, "count": v} for k, v in sorted(err_counts.items())
    ]

    gates = compute_gates(summary_routes, total, err_count_non_503)
    gates_external = build_gates_external_inputs(started_at, finished_at, cfg.gateway_url)

    report: dict[str, Any] = {
        "schema_version": SCHEMA_VERSION,
        "started_at": started_at,
        "finished_at": finished_at,
        "target": {
            "gateway_url": cfg.gateway_url,
            "tenant": "multi",
        },
        "summary": {
            "routes": summary_routes,
            "total_requests": total,
            "error_count": sum(err_counts.values()),
        },
        "errors": errors_array,
        "gates": gates,
        "gates_external_inputs": gates_external,
    }
    sha = _git_sha()
    if sha:
        report["git_sha"] = sha
    return report


def write_and_validate(report: dict[str, Any], out_path: str) -> bool:
    """Write report.json and validate against the sibling schema (WR-05).

    Always writes (even on validation failure) so forensics is preserved;
    returns True iff the report passes schema validation.
    """
    schema_path = Path(__file__).parent / "load-replay-report-schema.json"
    schema = json.loads(schema_path.read_text())
    valid = True
    try:
        Draft202012Validator(schema).validate(report)
    except ValidationError as e:
        valid = False
        log.error("report failed schema validation", err=str(e)[:300])
    Path(out_path).parent.mkdir(parents=True, exist_ok=True)
    Path(out_path).write_text(json.dumps(report, indent=2, sort_keys=True))
    return valid


# main ------------------------------------------------------------------------


def main() -> int:
    """Wire CLI → preflight env-var resolve → async replay → schema-validate."""
    cfg = parse_args()
    log.info(
        "load-replay starting",
        gateway_url=cfg.gateway_url,
        fixture=cfg.fixture_path,
        duration_s=cfg.duration_s,
        max_concurrency=cfg.max_concurrency,
        speedup=cfg.speedup,
    )

    fixture_path = Path(cfg.fixture_path)
    if not fixture_path.is_file():
        log.error("fixture not found", path=cfg.fixture_path)
        return 1
    if not Path(cfg.audio_stub_path).is_file():
        log.error(
            "audio stub not found (required for /v1/audio/transcriptions replay)",
            path=cfg.audio_stub_path,
        )
        return 1

    # Closes reviews MEDIUM #2 — hard exit BEFORE traffic flows on missing keys.
    keys = resolve_tenant_keys(fixture_path)
    log.info("tenant keys resolved", tenant_count=len(keys))

    report = asyncio.run(orchestrate(cfg, keys))
    valid = write_and_validate(report, cfg.out_path)
    log.info(
        "load-replay finished",
        report_out=cfg.out_path,
        total_requests=report["summary"]["total_requests"],
        error_count=report["summary"]["error_count"],
        schema_valid=valid,
    )
    return 0 if valid else 1


if __name__ == "__main__":
    sys.exit(main())
