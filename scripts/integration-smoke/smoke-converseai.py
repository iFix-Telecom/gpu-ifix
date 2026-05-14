"""scripts/integration-smoke/smoke-converseai.py — INT-01 gateway contract smoke.

Exercises the OpenAI-compat contract the ConverseAI v4 consumers (apps/api Elysia
+ agents Python) depend on, against the deployed gateway with the `converseai`
tenant key. Covers the 4 INT-01 surfaces: non-streaming chat, SSE streaming chat,
tool calls, embeddings. Writes a machine-readable JSON report the Phase 8
HUMAN-UAT asserts on, and maps gate failures to distinct non-zero exit codes.

This is the `pod/smoke/smoke.py` shape, retargeted from the raw pod endpoints to
the gateway `/v1/*` endpoints. The one structural difference: every request
carries an `Authorization: Bearer <tenant-key>` header — the gateway requires
auth, and redacts that header from its logs (gateway/README.md).

Secret-once discipline: the tenant key is supplied ONLY via `--api-key` or the
`SMOKE_API_KEY` env var. There is NO committed default — the script refuses to
run (argparse error, no network call) when no key is provided.

Entry point:
    python scripts/integration-smoke/smoke-converseai.py \\
        --gateway-url https://gateway.ifix.com.br \\
        --api-key <converseai tenant key> \\
        --out smoke-converseai-report.json

Exit codes:
    0  all 4 gates passed
    2  chat gate failed (only)
    3  streaming gate failed (only)
    4  tool-call gate failed (only)
    5  embeddings gate failed (only)
    6  multiple gates failed
    1  fallback / unexpected
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
import numpy as np
import structlog

# --- Constants ------------------------------------------------------------

SCHEMA_VERSION = "1.0.0"

CHAT_MODEL = "qwen"            # gateway LLM alias
EMBED_MODEL = "bge-m3"        # gateway embed alias (resolves to BAAI/bge-m3)

DEFAULT_EMBED_BATCH = 10
FAST_EMBED_BATCH = 3

log = structlog.get_logger().bind(module="SMOKE_CONVERSEAI")


# --- Config + CLI ---------------------------------------------------------

@dataclasses.dataclass
class Config:
    gateway_url: str
    api_key: str
    out_path: str
    fast: bool


def parse_args() -> Config:
    ap = argparse.ArgumentParser(
        description="ConverseAI integration smoke — INT-01 gateway contract "
                    "(chat / streaming / tool-calls / embeddings)"
    )
    ap.add_argument(
        "--gateway-url",
        default=os.getenv("SMOKE_GATEWAY_URL"),
        help="base URL of the deployed gateway (e.g. https://gateway.ifix.com.br)",
    )
    ap.add_argument(
        "--api-key",
        default=os.getenv("SMOKE_API_KEY"),
        help="converseai tenant API key — sent as Authorization: Bearer. "
             "Pass via --api-key or the SMOKE_API_KEY env var; NEVER committed.",
    )
    ap.add_argument(
        "--out",
        default=os.getenv("SMOKE_OUT", "smoke-converseai-report.json"),
        help="path to write the JSON report",
    )
    ap.add_argument(
        "--fast",
        action="store_true",
        help="fewer embeddings (dev only)",
    )
    args = ap.parse_args()

    if not args.gateway_url:
        ap.error("--gateway-url or SMOKE_GATEWAY_URL required")
    if not args.api_key:
        ap.error("--api-key or SMOKE_API_KEY required (the converseai tenant key)")

    return Config(
        gateway_url=args.gateway_url.rstrip("/"),
        api_key=args.api_key,
        out_path=args.out,
        fast=args.fast,
    )


# --- Chat (non-streaming) -------------------------------------------------

async def run_chat(client: httpx.AsyncClient, url: str) -> dict[str, Any]:
    """Non-streaming POST /v1/chat/completions; return {status_code, ok, raw_error_body?}."""
    payload = {
        "model": CHAT_MODEL,
        "messages": [{"role": "user", "content": "Say 'pong' and nothing else."}],
        "max_tokens": 32,
        "stream": False,
    }
    try:
        r = await client.post(url + "/v1/chat/completions", json=payload, timeout=120.0)
        if r.status_code != 200:
            return {"status_code": r.status_code, "ok": False, "raw_error_body": r.text[:500]}
        return {"status_code": r.status_code, "ok": True}
    except Exception as e:
        return {"status_code": -1, "ok": False, "raw_error_body": str(e)[:500]}


# --- Chat (streaming SSE) -------------------------------------------------

async def run_chat_stream(client: httpx.AsyncClient, url: str) -> dict[str, Any]:
    """Streaming POST /v1/chat/completions; return {ttft_ms, chunks, flushed, status_code, raw_error_body?}.

    `flushed` is True when at least 2 SSE chunks arrived incrementally — evidence
    the gateway flushes the stream (it runs with FlushInterval: -1) rather than
    buffering the whole response.

    `status_code` is ALWAYS carried (HTTP status on success/non-200, -1 on
    exception) so a `not flushed` gate failure is never silent — see main_async,
    which synthesises a diagnostic from status_code + chunks when a 200 simply
    did not flush enough chunks and there is no `raw_error_body`.
    """
    payload = {
        "model": CHAT_MODEL,
        "messages": [{"role": "user", "content": "Count slowly from one to ten."}],
        "max_tokens": 128,
        "stream": True,
    }
    start = time.monotonic()
    ttft_ms = -1
    chunks = 0
    try:
        async with client.stream(
            "POST", url + "/v1/chat/completions", json=payload, timeout=120.0
        ) as r:
            if r.status_code != 200:
                body = (await r.aread()).decode(errors="replace")
                return {
                    "ttft_ms": -1,
                    "chunks": 0,
                    "flushed": False,
                    "status_code": r.status_code,
                    "raw_error_body": body[:500],
                }
            status_code = r.status_code
            async for line in r.aiter_lines():
                if not line.startswith("data:"):
                    continue
                if line.strip() == "data: [DONE]":
                    break
                if ttft_ms < 0:
                    ttft_ms = int((time.monotonic() - start) * 1000)
                chunks += 1
    except Exception as e:
        return {
            "ttft_ms": -1,
            "chunks": chunks,
            "flushed": False,
            "status_code": -1,
            "raw_error_body": str(e)[:500],
        }
    return {
        "ttft_ms": ttft_ms,
        "chunks": chunks,
        "flushed": chunks >= 2,
        "status_code": status_code,
    }


# --- Tool-call validation -------------------------------------------------

async def run_tool_call_test(client: httpx.AsyncClient, url: str) -> tuple[bool, list[str]]:
    """Send a get_weather tool-calling request; return (valid, errors).

    Copied verbatim (payload + assertions) from pod/smoke/smoke.py lines 204-254.
    """
    payload = {
        "model": CHAT_MODEL,
        "messages": [{"role": "user", "content": "What's the weather in São Paulo? Use the tool."}],
        "tools": [{
            "type": "function",
            "function": {
                "name": "get_weather",
                "description": "Get the weather for a city",
                "parameters": {
                    "type": "object",
                    "properties": {"location": {"type": "string"}},
                    "required": ["location"],
                },
            },
        }],
        "max_tokens": 256,
    }
    errors: list[str] = []
    try:
        r = await client.post(url + "/v1/chat/completions", json=payload, timeout=60.0)
        if r.status_code != 200:
            errors.append(f"tool-call status {r.status_code}: {r.text[:500]}")
            return False, errors
        body = r.json()
        calls = body.get("choices", [{}])[0].get("message", {}).get("tool_calls") or []
        if not calls:
            errors.append(f"tool-call missing: response={json.dumps(body)[:500]}")
            return False, errors
        c = calls[0]
        if c.get("type") != "function":
            errors.append(f"tool-call type != function: {c}")
            return False, errors
        fn = c.get("function", {})
        if fn.get("name") != "get_weather":
            errors.append(f"tool-call name != get_weather: {fn.get('name')}")
            return False, errors
        # arguments is a JSON string per OpenAI wire
        try:
            args = json.loads(fn.get("arguments", "{}"))
        except json.JSONDecodeError as je:
            errors.append(f"tool-call arguments not valid JSON: {je}")
            return False, errors
        if "location" not in args:
            errors.append(f"tool-call arguments missing 'location': {args}")
            return False, errors
        return True, []
    except Exception as e:
        errors.append(f"tool-call exception: {e}")
        return False, errors


# --- Embeddings -----------------------------------------------------------

async def run_embeddings(client: httpx.AsyncClient, url: str, batch: int) -> dict[str, Any]:
    """Batched POST /v1/embeddings; return {p95_ms, successes, errors}."""
    async def one(i: int) -> dict[str, Any]:
        payload = {"model": EMBED_MODEL, "input": [f"smoke test {i}"]}
        start = time.monotonic()
        try:
            r = await client.post(url + "/v1/embeddings", json=payload, timeout=30.0)
            elapsed_ms = int((time.monotonic() - start) * 1000)
            if r.status_code != 200:
                return {"error": f"status {r.status_code}: {r.text[:500]}"}
            return {"latency_ms": elapsed_ms}
        except Exception as e:
            return {"error": str(e)[:500]}

    results = await asyncio.gather(*[one(i) for i in range(batch)])
    latencies = [r["latency_ms"] for r in results if "latency_ms" in r]
    errors = [r["error"] for r in results if "error" in r]
    p95 = int(np.percentile(latencies, 95)) if latencies else -1
    return {"p95_ms": p95, "successes": len(latencies), "errors": errors}


# --- Gates + exit codes ---------------------------------------------------

def apply_gates(report: dict[str, Any]) -> dict[str, bool]:
    chat_ok = report["chat"].get("ok") is True
    streaming_flushes = report["chat_stream"].get("flushed") is True
    tool_call_valid = report["tool_call"].get("valid") is True
    emb = report["embeddings"]
    embeddings_ok = emb.get("successes", 0) > 0 and not emb.get("errors")
    all_passed = chat_ok and streaming_flushes and tool_call_valid and embeddings_ok
    return {
        "chat_ok": chat_ok,
        "streaming_flushes": streaming_flushes,
        "tool_call_valid": tool_call_valid,
        "embeddings_ok": embeddings_ok,
        "all_passed": all_passed,
    }


def exit_code_for_gates(gates: dict[str, bool]) -> int:
    if gates["all_passed"]:
        return 0
    failing = 0
    if not gates["chat_ok"]:
        failing |= 0b0001
    if not gates["streaming_flushes"]:
        failing |= 0b0010
    if not gates["tool_call_valid"]:
        failing |= 0b0100
    if not gates["embeddings_ok"]:
        failing |= 0b1000
    # Map to specific exit codes per interface contract
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
    log.info("smoke starting", gateway=cfg.gateway_url, fast=cfg.fast)

    started_at = datetime.now(timezone.utc).isoformat()

    # Single client carries the tenant key on every request — the structural
    # difference vs the raw-pod smoke (the gateway requires auth).
    async with httpx.AsyncClient(
        headers={"Authorization": f"Bearer {cfg.api_key}"}
    ) as client:
        chat = await run_chat(client, cfg.gateway_url)
        chat_stream = await run_chat_stream(client, cfg.gateway_url)
        tool_valid, tool_errors = await run_tool_call_test(client, cfg.gateway_url)
        embed_batch = FAST_EMBED_BATCH if cfg.fast else DEFAULT_EMBED_BATCH
        embeddings = await run_embeddings(client, cfg.gateway_url, embed_batch)

    finished_at = datetime.now(timezone.utc).isoformat()

    # --- aggregate errors ---
    errors: list[str] = []
    if not chat.get("ok") and chat.get("raw_error_body"):
        errors.append(f"chat: {chat['raw_error_body']}")
    if not chat_stream.get("flushed"):
        # A non-200 / exception carries raw_error_body; a 200 that simply did
        # not flush enough chunks does not — synthesise a diagnostic from the
        # status_code + chunk count so a streaming-gate failure is never a
        # silent exit 3 with an empty errors array.
        reason = chat_stream.get("raw_error_body") or (
            f"stream returned status {chat_stream.get('status_code')} "
            f"with only {chat_stream.get('chunks', 0)} chunk(s) — "
            f"expected >= 2 (gateway not flushing incrementally?)"
        )
        errors.append(f"chat_stream: {reason}")
    errors.extend(f"tool_call: {e}" for e in tool_errors)
    errors.extend(f"embeddings: {e}" for e in embeddings.get("errors", []))

    report = {
        "schema_version": SCHEMA_VERSION,
        "started_at": started_at,
        "finished_at": finished_at,
        "target": {
            "gateway_url": cfg.gateway_url,
            "tenant": "converseai",
        },
        "chat": chat,
        "chat_stream": chat_stream,
        "tool_call": {"valid": tool_valid, "errors": tool_errors},
        "embeddings": embeddings,
        "errors": errors,
        "gates": {},  # filled in below
    }
    report["gates"] = apply_gates(report)

    # git_sha (optional)
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

    # Validate against schema before writing
    try:
        from jsonschema import Draft202012Validator
        schema = json.loads((Path(__file__).parent / "report-schema.json").read_text())
        Draft202012Validator(schema).validate(report)
    except Exception as e:
        log.warning("report does not match schema; writing anyway for debugging", err=str(e))

    Path(cfg.out_path).write_text(json.dumps(report, indent=2, sort_keys=True))
    log.info("report written", path=cfg.out_path, gates=report["gates"])

    code = exit_code_for_gates(report["gates"])
    if code != 0:
        log.error("INT-01 GATES FAILED", gates=report["gates"], exit=code)
    else:
        log.info("INT-01 GATES PASSED")
    return code


def main() -> None:
    cfg = parse_args()
    code = asyncio.run(main_async(cfg))
    sys.exit(code)


if __name__ == "__main__":
    main()
