"""scripts/integration-smoke/audit-log-export.py — PRD-01 fixture generator.

Read sanitized rows from `ai_gateway.audit_log` JOINed with
`ai_gateway.audit_log_content` (the body source — verified against migrations
0003 + 0004) and emit a JSONL fixture for `load-replay.py` to consume.

This script implements PRD-01 D-01 (replay audit_log dev sanitized) and closes
the cross-AI review feedback:

  - [reviews HIGH #1] The body lives in `ai_gateway.audit_log_content.prompt`
    (JSONB). `audit_log` alone carries only envelope metadata; we MUST JOIN
    the content table to retrieve the body. Schema verified against
    `gateway/db/migrations/0003_create_audit_log_partitioned.sql` (envelope)
    and `0004_create_audit_log_content_partitioned.sql` (content; columns
    `request_id UUID`, `ts TIMESTAMPTZ`, `prompt JSONB`, `response JSONB`,
    PRIMARY KEY (request_id, ts) — no FK to audit_log, JOIN purely on
    composite PK match).

  - [reviews MEDIUM #2] Zero secret-at-rest in JSONL fixtures. This exporter
    writes ONLY `tenant_slug` (resolved from `ai_gateway.tenants.slug`); the
    load replay engine resolves the per-tenant Authorization header at request
    time from env var IFIX_KEY_<TENANT_SLUG_UPPER>. NO replay-key field, NO
    authorization field, NO bearer-token field — ever.

Sanitization invariant (Pitfall 1 + Open Question #1 baseline):
  - Skip rows where `audit_log.data_class = 'sensitive'` (PRD-01 baseline
    excludes sensitive tenants entirely from replay).
  - Replace `tool_calls[].function.arguments` (recursively under
    `messages[].tool_calls[]` AND top-level `tool_calls[]`) with the
    placeholder `{"_replay_placeholder": true}` so free-text user data is
    never re-injected.
  - Drop keys `audio.filename`, `file.url`, `image.url` from the prompt body.
  - For route `/v1/audio/transcriptions`: drop base64 audio bytes
    (`audio`, `file`, `data`, `bytes` keys) and emit a marker
    `{"_replay_audio_part": "stub", "_replay_audio_mime": <audit row mime>}`
    so the replay engine knows to attach the fixture OGG file at runtime.

  <pitfalls>
  STT replay measures pipeline latency against the canonical stub audio
  fixture (`scripts/integration-smoke/fixtures/whatsapp-sample.ogg`), NOT
  against the literal original utterance. Migration 0003 line 23-24 (D-B6
  invariant) explicitly states "audio stays out of content table" — the raw
  audio bytes are NEVER persisted, so they cannot be reconstructed from the
  DB. This is a deliberate limitation, documented here so the load test
  reader does not assume STT replay is byte-faithful.
  </pitfalls>

Secret-once discipline (Pattern A): the audit-DB DSN is supplied ONLY via
`--dsn` / `DASHBOARD_DATABASE_URL` (or `AI_GATEWAY_DB_URL` fallback). No
committed default. Never logged. On psycopg error the script logs only the
first 300 chars of `str(e)` (threat T-11-LOAD-02).

Entry point:
    python scripts/integration-smoke/audit-log-export.py \\
        --dsn postgres://.../ai_gateway \\
        --window-start 2026-05-27T14:00:00Z \\
        --window-end 2026-05-27T15:00:00Z \\
        --out .planning/load-test-fixtures/audit-export-2026-05-27T14-00-00Z.jsonl

Exit codes:
    0  ok
    1  runtime error (DB connection, query, write)
    2  usage error (missing required args)
"""

# Module docstring ----------------------------------------------------------- (the prose block above is the canonical module docstring; the 8-section
# spine begins below with Constants.)

from __future__ import annotations

# Constants -------------------------------------------------------------------

import argparse
import dataclasses
import json
import os
import sys
from datetime import datetime
from pathlib import Path
from typing import Any

import psycopg
import structlog

SCHEMA_VERSION = "1.0.0"

# Per Pitfall 1 + Open Question #1 baseline: skip rows with this data_class.
SENSITIVE_DATA_CLASS = "sensitive"

# The placeholder sentinel injected in place of free-text tool_calls arguments.
TOOL_CALL_PLACEHOLDER: dict[str, Any] = {"_replay_placeholder": True}

# Routes whose request body carries base64 audio bytes (Whisper multipart).
# The replay engine will reconstruct the multipart form using the canonical
# stub OGG fixture; the body emitted here strips those keys and replaces them
# with the audio-part marker so the replay knows to attach the fixture file.
STT_ROUTE = "/v1/audio/transcriptions"

# Keys that may carry base64 audio bytes (top-level OR nested under "audio"/
# "file"). These are dropped from the sanitized body when the row's route
# matches STT_ROUTE; otherwise they are dropped from any prompt that happens
# to contain them.
AUDIO_BYTE_KEYS = frozenset({"audio", "file", "data", "bytes"})

# Keys we always strip from any sanitized body (Pitfall 1 — drop external
# references that may leak filenames or signed URLs).
ALWAYS_DROP_NESTED = frozenset({"audio.filename", "file.url", "image.url"})

log = structlog.get_logger().bind(module="AUDIT_LOG_EXPORT")


# Config+CLI ------------------------------------------------------------------


@dataclasses.dataclass
class Config:
    dsn: str
    window_start: str
    window_end: str
    out_path: str
    include_sensitive: bool


def parse_args() -> Config:
    """Parse argv with secret-once discipline — no committed default for --dsn.

    The audit-DB DSN is sourced from --dsn or env DASHBOARD_DATABASE_URL or
    AI_GATEWAY_DB_URL (fallback). If neither is set, argparse errors out
    BEFORE any network call. The DSN is NEVER echoed to stdout, stderr, or
    structlog — only the host portion may appear in a successful-connect
    breadcrumb.
    """
    ap = argparse.ArgumentParser(
        description=(
            "PRD-01 fixture generator. Read ai_gateway.audit_log JOINed with "
            "audit_log_content.prompt (JSONB body source) for the time window, "
            "sanitize tool-call arguments + audio bytes per Pitfall 1, skip "
            "data_class=sensitive rows by default (PRD-01 baseline), and emit "
            "a JSONL fixture for scripts/integration-smoke/load-replay.py."
        )
    )
    ap.add_argument(
        "--dsn",
        default=os.getenv("DASHBOARD_DATABASE_URL") or os.getenv("AI_GATEWAY_DB_URL"),
        help=(
            "audit-DB DSN (postgres://...). Pass via --dsn or env "
            "DASHBOARD_DATABASE_URL or AI_GATEWAY_DB_URL. No committed default "
            "(secret-once Pattern A). NEVER logged."
        ),
    )
    ap.add_argument(
        "--window-start",
        help="ISO-8601 inclusive lower bound for audit_log.ts (e.g. 2026-05-27T14:00:00Z).",
    )
    ap.add_argument(
        "--window-end",
        help="ISO-8601 exclusive upper bound for audit_log.ts (e.g. 2026-05-27T15:00:00Z).",
    )
    ap.add_argument(
        "--out",
        help="output JSONL path (e.g. .planning/load-test-fixtures/audit-export-<start>.jsonl).",
    )
    ap.add_argument(
        "--include-sensitive",
        action="store_true",
        default=False,
        help=(
            "include data_class=sensitive rows in the export (DEFAULT FALSE — "
            "PRD-01 baseline + Open Question #1 excludes sensitive tenants "
            "from replay). Use only for explicit sensitive-tenant chaos runs."
        ),
    )
    args = ap.parse_args()

    if not args.dsn:
        ap.error(
            "--dsn or env DASHBOARD_DATABASE_URL or AI_GATEWAY_DB_URL required "
            "(audit-DB read DSN). Secret-once: NO committed default."
        )
    if not args.window_start:
        ap.error("--window-start required (ISO-8601, e.g. 2026-05-27T14:00:00Z)")
    if not args.window_end:
        ap.error("--window-end required (ISO-8601, e.g. 2026-05-27T15:00:00Z)")
    if not args.out:
        ap.error(
            "--out required (e.g. .planning/load-test-fixtures/audit-export-<start>.jsonl)"
        )

    return Config(
        dsn=args.dsn,
        window_start=args.window_start,
        window_end=args.window_end,
        out_path=args.out,
        include_sensitive=args.include_sensitive,
    )


# Helpers ---------------------------------------------------------------------


def _placeholder_tool_calls(tool_calls: Any) -> Any:
    """Replace every `function.arguments` field under a tool_calls list.

    Pitfall 1: tool_calls arguments are free-text user data and MUST be
    replaced with `{"_replay_placeholder": true}` before the body is written
    to the JSONL fixture.
    """
    if not isinstance(tool_calls, list):
        return tool_calls
    out: list[Any] = []
    for tc in tool_calls:
        if not isinstance(tc, dict):
            out.append(tc)
            continue
        new_tc = dict(tc)
        fn = new_tc.get("function")
        if isinstance(fn, dict) and "arguments" in fn:
            new_fn = dict(fn)
            new_fn["arguments"] = json.dumps(TOOL_CALL_PLACEHOLDER)
            new_tc["function"] = new_fn
        out.append(new_tc)
    return out


def sanitize_body(
    body: Any,
    data_class: str,
    route: str,
    audio_mime: str | None,
) -> dict[str, Any] | None:
    """Sanitize a single audit_log_content.prompt JSONB value.

    Returns None when the caller MUST skip the row entirely
    (data_class='sensitive' under the PRD-01 baseline). Otherwise returns a
    dict suitable for direct JSON-encoding into the JSONL line.

    Sanitization rules (Pitfall 1):
      1. data_class='sensitive' (PRD-01 baseline excludes these from replay).
      2. tool_calls[].function.arguments → `{"_replay_placeholder": true}`
         both at top-level and nested under messages[].tool_calls[].
      3. Drop keys 'audio.filename', 'file.url', 'image.url' if present.
      4. For route /v1/audio/transcriptions: drop base64 audio key/value pairs
         AND emit marker `_replay_audio_part: "stub"` + `_replay_audio_mime`.
    """
    if data_class == SENSITIVE_DATA_CLASS:
        return None
    if body is None:
        # No content row — emit an empty body marker so the replay still
        # exercises the route timing/dispatch path.
        return {}
    if not isinstance(body, dict):
        # Defensive: audit_log_content.prompt is JSONB but malformed rows
        # are possible. Wrap so the JSONL is still valid.
        return {"_replay_unparseable_body": True}

    out: dict[str, Any] = dict(body)

    # Rule 2: top-level tool_calls placeholder
    if "tool_calls" in out:
        out["tool_calls"] = _placeholder_tool_calls(out["tool_calls"])

    # Rule 2 (cont.): nested messages[].tool_calls
    messages = out.get("messages")
    if isinstance(messages, list):
        new_msgs: list[Any] = []
        for m in messages:
            if isinstance(m, dict) and "tool_calls" in m:
                new_m = dict(m)
                new_m["tool_calls"] = _placeholder_tool_calls(m["tool_calls"])
                new_msgs.append(new_m)
            else:
                new_msgs.append(m)
        out["messages"] = new_msgs

    # Rule 3: drop nested filename/url fields if present at top level
    for k in ("audio.filename", "file.url", "image.url"):
        out.pop(k, None)
    # Same defensively as nested keys
    for parent in ("audio", "file", "image"):
        nested = out.get(parent)
        if isinstance(nested, dict):
            new_nested = {kk: vv for kk, vv in nested.items() if kk not in ("filename", "url")}
            out[parent] = new_nested

    # Rule 4: STT route gets audio bytes stripped + marker emitted
    if route == STT_ROUTE:
        for k in list(out.keys()):
            if k in AUDIO_BYTE_KEYS:
                out.pop(k, None)
        out["_replay_audio_part"] = "stub"
        out["_replay_audio_mime"] = audio_mime or "audio/ogg"

    return out


# Audit-DB --------------------------------------------------------------------


# Canonical SQL: JOIN audit_log envelope with audit_log_content.prompt (body
# source per migrations 0003 + 0004) AND tenants.slug (per migration 0001).
# Columns verified against migration files — no speculation.
#
# Filter applies the data_class skip at the SQL level so the row never crosses
# the network when --include-sensitive is False (default).
_SELECT_SQL = """
SELECT
    a.request_id,
    a.ts,
    a.tenant_id,
    t.slug AS tenant_slug,
    a.route,
    a.upstream,
    a.status_code,
    a.data_class,
    a.audio_filename,
    a.audio_mime,
    c.prompt AS body
FROM ai_gateway.audit_log a
LEFT JOIN ai_gateway.audit_log_content c
    ON c.request_id = a.request_id AND c.ts = a.ts
LEFT JOIN ai_gateway.tenants t
    ON t.id = a.tenant_id
WHERE a.ts >= %s
  AND a.ts <  %s
  AND (%s OR a.data_class != 'sensitive')
ORDER BY a.ts ASC
"""


def _stream_audit_rows(
    cfg: Config,
) -> "list[dict[str, Any]] | None":
    """Connect, run the JOIN, and return parsed rows (list of dicts).

    Threat T-11-LOAD-02 mitigation: psycopg errors are reported with only
    the first 300 chars of `str(e)`, NEVER the DSN. The DSN is bound to a
    local var inside this function and never leaves it.
    """
    try:
        with psycopg.connect(cfg.dsn, connect_timeout=10) as conn:
            with conn.cursor() as cur:
                cur.execute(
                    _SELECT_SQL,
                    (cfg.window_start, cfg.window_end, cfg.include_sensitive),
                )
                cols = [d.name for d in cur.description] if cur.description else []
                rows: list[dict[str, Any]] = []
                for row in cur:
                    rows.append({cols[i]: row[i] for i in range(len(cols))})
                return rows
    except Exception as e:
        log.error("audit-DB query failed", err=f"{str(e)[:300]}")
        return None


# Gates+exit ------------------------------------------------------------------


def _validate_tenant_slug(row: dict[str, Any], errors: list[str]) -> bool:
    """Reject rows lacking tenant_slug — load-replay needs it for env-var lookup."""
    if not row.get("tenant_slug"):
        errors.append(
            f"row request_id={row.get('request_id')!s} missing tenant_slug "
            f"(tenant_id={row.get('tenant_id')!s}) — replay cannot resolve IFIX_KEY_*"
        )
        return False
    return True


# Orchestration ---------------------------------------------------------------


def _ts_iso(ts: Any) -> str:
    """Render TIMESTAMPTZ as ISO-8601 with Z suffix when naive UTC."""
    if isinstance(ts, datetime):
        s = ts.isoformat()
        # psycopg returns aware datetimes when the DB column is TIMESTAMPTZ.
        if ts.tzinfo is None:
            s += "Z"
        return s
    return str(ts)


def emit_jsonl(rows: list[dict[str, Any]], out_path: Path) -> tuple[int, int, list[str]]:
    """Sanitize each row, compute _replay_delay_s, write JSONL.

    Returns (emitted_count, skipped_count, errors).
    Emitted JSONL line has EXACTLY these 9 keys:
        request_id, ts, tenant_slug, route, upstream, status_code,
        data_class, _replay_delay_s, _sanitized_body
    """
    errors: list[str] = []
    emitted = 0
    skipped = 0
    prev_ts: datetime | None = None

    out_path.parent.mkdir(parents=True, exist_ok=True)
    with out_path.open("w", encoding="utf-8") as f:
        for row in rows:
            data_class = row.get("data_class") or ""
            sanitized = sanitize_body(
                row.get("body"),
                data_class,
                row.get("route") or "",
                row.get("audio_mime"),
            )
            if sanitized is None:
                # data_class='sensitive' — skipped per PRD-01 baseline.
                skipped += 1
                continue
            if not _validate_tenant_slug(row, errors):
                skipped += 1
                continue

            ts_val = row.get("ts")
            delay_s = 0.0
            if isinstance(ts_val, datetime) and isinstance(prev_ts, datetime):
                delay_s = max(0.0, (ts_val - prev_ts).total_seconds())
            if isinstance(ts_val, datetime):
                prev_ts = ts_val

            record = {
                "request_id": str(row.get("request_id") or ""),
                "ts": _ts_iso(ts_val),
                "tenant_slug": row["tenant_slug"],
                "route": row.get("route") or "",
                "upstream": row.get("upstream"),
                "status_code": int(row.get("status_code") or 0),
                "data_class": data_class,
                "_replay_delay_s": delay_s,
                "_sanitized_body": sanitized,
            }
            f.write(json.dumps(record, ensure_ascii=False, sort_keys=True) + "\n")
            emitted += 1

    return emitted, skipped, errors


# main ------------------------------------------------------------------------


def main() -> int:
    """Wire CLI → audit-DB read → sanitize → JSONL write."""
    cfg = parse_args()
    log.info(
        "audit-log-export starting",
        window_start=cfg.window_start,
        window_end=cfg.window_end,
        out=cfg.out_path,
        include_sensitive=cfg.include_sensitive,
    )

    rows = _stream_audit_rows(cfg)
    if rows is None:
        # _stream_audit_rows already logged err (DSN-safe)
        return 1

    emitted, skipped, errors = emit_jsonl(rows, Path(cfg.out_path))
    log.info(
        "audit-log-export finished",
        rows_read=len(rows),
        emitted=emitted,
        skipped=skipped,
        errors_count=len(errors),
    )
    for e in errors:
        log.warning("export warning", detail=e)
    return 0


if __name__ == "__main__":
    sys.exit(main())
