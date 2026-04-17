"""pod/smoke/smoke.py — D-17 workload + D-19 gates (Phase 1 validation).

Entry point:
    python pod/smoke/smoke.py --target http://<pod-ip>:<base-port> --out smoke-report.json

Assumptions about target (plan 03 port layout):
- LLM endpoint:   <target>:8000
- STT endpoint:   <target>:8001
- Embed endpoint: <target>:8002
- dcgm-exporter:  <target>:9400

Or pass individual URLs with --llm-url / --stt-url / --embed-url / --dcgm-url.
"""

from __future__ import annotations

import argparse
import asyncio
import dataclasses
import json
import os
import re
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import httpx
import numpy as np
import structlog
from prometheus_client.parser import text_string_to_metric_families

# Import sibling fixtures package. `python3 pod/smoke/smoke.py` sets sys.path[0]
# to pod/smoke/, so `pod.smoke.fixtures` would not resolve. We inject the parent
# of smoke.py (= pod/smoke/) first so `fixtures.__gen_audio` is importable.
sys.path.insert(0, str(Path(__file__).parent))
from fixtures import __gen_audio as audio_gen  # noqa: E402


# --- Constants (D-17, D-18, D-19) -----------------------------------------

SCHEMA_VERSION = "1.0.0"

DEFAULT_CHAT_PROMPT_TOKENS = 8000            # D-17 "chats concorrentes 8k tokens"
DEFAULT_CHAT_MAX_COMPLETION = 500
DEFAULT_CHATS_CONCURRENT = 2                 # D-17
DEFAULT_WHISPER_DURATION = 480               # D-17 "Whisper longa 8+ min"
DEFAULT_EMBED_BATCH = 10                     # D-17 "batch de 10 embeddings"
DEFAULT_DCGM_INTERVAL_S = 1.0                # D-17 "métricas a cada 1s"

# D-19 gates
GATE_VRAM_PEAK_GB_MAX = 21.0
GATE_LLM_P95_TTFT_MS_MAX = 3000
CUDA_ERROR_PATTERNS = [
    re.compile(r"out of memory", re.I),
    re.compile(r"cuda error", re.I),
    re.compile(r"cublas", re.I),
    re.compile(r"oom", re.I),
    re.compile(r"ggml_cuda_host_malloc", re.I),
]

log = structlog.get_logger().bind(module="SMOKE")


# --- Config + CLI ---------------------------------------------------------

@dataclasses.dataclass
class Config:
    llm_url: str
    stt_url: str
    embed_url: str
    dcgm_url: str
    out_path: str
    chats_concurrent: int
    chat_prompt_tokens: int
    chat_max_completion: int
    whisper_duration_s: int
    embed_batch: int
    dcgm_interval_s: float


def parse_args() -> Config:
    ap = argparse.ArgumentParser(description="ifix-ai-pod smoke test (POD-07 / D-17..D-19)")
    ap.add_argument("--target", default=os.getenv("SMOKE_TARGET"),
                    help="base URL of pod (ports 8000/8001/8002/9400 appended automatically)")
    ap.add_argument("--llm-url", default=None)
    ap.add_argument("--stt-url", default=None)
    ap.add_argument("--embed-url", default=None)
    ap.add_argument("--dcgm-url", default=None)
    ap.add_argument("--out", default=os.getenv("SMOKE_OUT", "smoke-report.json"))
    ap.add_argument("--fast", action="store_true", help="shorter Whisper / fewer chats (dev only)")
    args = ap.parse_args()

    if args.target is None and not all([args.llm_url, args.stt_url, args.embed_url, args.dcgm_url]):
        ap.error("provide --target or all of --llm-url/--stt-url/--embed-url/--dcgm-url")

    base = args.target.rstrip("/") if args.target else ""
    host_only = base.split("://")[-1].split(":")[0] if base else ""
    scheme = base.split("://")[0] if "://" in base else "http"

    def default_url(port: int) -> str:
        return f"{scheme}://{host_only}:{port}"

    whisper_s = 30 if args.fast else DEFAULT_WHISPER_DURATION
    chats = 1 if args.fast else DEFAULT_CHATS_CONCURRENT

    return Config(
        llm_url=args.llm_url or default_url(8000),
        stt_url=args.stt_url or default_url(8001),
        embed_url=args.embed_url or default_url(8002),
        dcgm_url=args.dcgm_url or default_url(9400),
        out_path=args.out,
        chats_concurrent=chats,
        chat_prompt_tokens=DEFAULT_CHAT_PROMPT_TOKENS,
        chat_max_completion=DEFAULT_CHAT_MAX_COMPLETION,
        whisper_duration_s=whisper_s,
        embed_batch=DEFAULT_EMBED_BATCH,
        dcgm_interval_s=DEFAULT_DCGM_INTERVAL_S,
    )


# --- DCGM scrape ----------------------------------------------------------

async def dcgm_scrape_loop(
    client: httpx.AsyncClient,
    url: str,
    interval_s: float,
    samples: list[dict[str, float]],
    stop: asyncio.Event,
) -> None:
    """Scrape dcgm-exporter /metrics every `interval_s` until `stop` is set."""
    while not stop.is_set():
        start = time.monotonic()
        try:
            r = await client.get(url + "/metrics", timeout=5.0)
            r.raise_for_status()
            parsed: dict[str, float] = {}
            for fam in text_string_to_metric_families(r.text):
                if fam.name in ("DCGM_FI_DEV_FB_USED", "DCGM_FI_DEV_FB_FREE", "DCGM_FI_DEV_FB_TOTAL",
                                "DCGM_FI_DEV_GPU_UTIL", "DCGM_FI_DEV_GPU_TEMP", "DCGM_FI_DEV_POWER_USAGE"):
                    # Take the first sample (single-GPU pod)
                    for sample in fam.samples:
                        parsed[fam.name] = float(sample.value)
                        break
            parsed["_ts"] = time.time()
            samples.append(parsed)
        except Exception as e:
            log.warning("dcgm scrape failed", err=str(e))
        elapsed = time.monotonic() - start
        try:
            await asyncio.wait_for(stop.wait(), timeout=max(0.01, interval_s - elapsed))
        except asyncio.TimeoutError:
            pass


# --- Chat workload (streaming SSE) ----------------------------------------

def _build_long_prompt(tokens: int) -> str:
    """Produce a prompt of approximately `tokens` tokens (rough 4 chars = 1 token heuristic)."""
    base = "The quick brown fox jumps over the lazy dog. "
    needed_chars = tokens * 4
    return (base * (needed_chars // len(base) + 1))[:needed_chars]


async def run_chat_stream(
    client: httpx.AsyncClient,
    url: str,
    prompt: str,
    max_tokens: int,
) -> dict[str, Any]:
    """Run one streaming chat completion; return {ttft_ms, total_ms, tokens, error?, raw_error_body?}."""
    payload = {
        "model": "qwen",
        "messages": [{"role": "user", "content": prompt}],
        "max_tokens": max_tokens,
        "stream": True,
    }
    start = time.monotonic()
    ttft_ms = -1
    tokens = 0
    try:
        async with client.stream("POST", url + "/v1/chat/completions", json=payload, timeout=120.0) as r:
            if r.status_code != 200:
                body = (await r.aread()).decode(errors="replace")
                return {"error": f"status {r.status_code}", "raw_error_body": body}
            async for line in r.aiter_lines():
                if not line.startswith("data:"):
                    continue
                if line.strip() == "data: [DONE]":
                    break
                if ttft_ms < 0:
                    ttft_ms = int((time.monotonic() - start) * 1000)
                tokens += 1
    except Exception as e:
        return {"error": str(e)}
    total_ms = int((time.monotonic() - start) * 1000)
    return {"ttft_ms": ttft_ms, "total_ms": total_ms, "tokens": tokens}


# --- Tool-call validation (D-15) ------------------------------------------

async def run_tool_call_test(client: httpx.AsyncClient, url: str) -> tuple[bool, list[str]]:
    """Send a get_weather tool-calling request; return (valid, errors)."""
    payload = {
        "model": "qwen",
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


# --- Whisper + embeddings -------------------------------------------------

async def run_whisper(client: httpx.AsyncClient, url: str, duration_s: int) -> dict[str, Any]:
    wav = audio_gen.generate_wav_bytes(duration_seconds=duration_s)
    files = {"file": ("probe.wav", wav, "audio/wav")}
    data = {"model": "Systran/faster-whisper-large-v3"}
    start = time.monotonic()
    try:
        r = await client.post(url + "/v1/audio/transcriptions", files=files, data=data, timeout=600.0)
        total_s = time.monotonic() - start
        if r.status_code != 200:
            return {"error": f"status {r.status_code}", "raw_error_body": r.text[:500]}
        return {"latency_s": total_s, "text": r.json().get("text", "")[:80]}
    except Exception as e:
        return {"error": str(e)}


async def run_embeddings(client: httpx.AsyncClient, url: str, batch: int) -> dict[str, Any]:
    async def one(i: int) -> dict[str, Any]:
        payload = {"model": "BAAI/bge-m3", "input": [f"smoke test {i}"]}
        start = time.monotonic()
        try:
            r = await client.post(url + "/v1/embeddings", json=payload, timeout=30.0)
            elapsed_ms = int((time.monotonic() - start) * 1000)
            if r.status_code != 200:
                return {"error": f"status {r.status_code}", "raw_error_body": r.text[:500]}
            return {"latency_ms": elapsed_ms}
        except Exception as e:
            return {"error": str(e)}

    results = await asyncio.gather(*[one(i) for i in range(batch)])
    latencies = [r["latency_ms"] for r in results if "latency_ms" in r]
    errors = [r for r in results if "error" in r]
    p95 = int(np.percentile(latencies, 95)) if latencies else -1
    return {"p95_ms": p95, "successes": len(latencies), "errors": errors}


# --- Aggregation + gates --------------------------------------------------

def compute_vram_stats(samples: list[dict[str, float]]) -> tuple[float, float]:
    """Returns (peak_gb, p95_gb) of DCGM_FI_DEV_FB_USED (MiB) across samples."""
    used_mib = [s.get("DCGM_FI_DEV_FB_USED", 0.0) for s in samples if "DCGM_FI_DEV_FB_USED" in s]
    if not used_mib:
        return 0.0, 0.0
    arr = np.asarray(used_mib, dtype=float)
    peak_gb = float(arr.max()) / 1024.0
    p95_gb = float(np.percentile(arr, 95)) / 1024.0
    return round(peak_gb, 2), round(p95_gb, 2)


def detect_cuda_errors(error_bodies: list[str]) -> list[str]:
    hits: list[str] = []
    for body in error_bodies:
        if body and any(p.search(body) for p in CUDA_ERROR_PATTERNS):
            hits.append(body[:500])
    return hits


def apply_gates(report: dict[str, Any]) -> dict[str, bool]:
    vram_ok = report["vram_peak_gb"] <= GATE_VRAM_PEAK_GB_MAX
    tool_ok = report["tool_call_valid"] is True
    cuda_ok = not any("cuda" in e.lower() or "out of memory" in e.lower() or "oom" in e.lower() for e in report["errors"])
    ttft_ok = report["llm_p95_ttft_ms"] <= GATE_LLM_P95_TTFT_MS_MAX
    all_ok = vram_ok and tool_ok and cuda_ok and ttft_ok
    return {
        "vram_peak_gb_le_21": vram_ok,
        "tool_call_valid_true": tool_ok,
        "no_cuda_oom_errors": cuda_ok,
        "llm_p95_ttft_ms_le_3000": ttft_ok,
        "all_passed": all_ok,
    }


def exit_code_for_gates(gates: dict[str, bool]) -> int:
    if gates["all_passed"]:
        return 0
    failing = 0
    if not gates["vram_peak_gb_le_21"]:
        failing |= 0b0001
    if not gates["tool_call_valid_true"]:
        failing |= 0b0010
    if not gates["no_cuda_oom_errors"]:
        failing |= 0b0100
    if not gates["llm_p95_ttft_ms_le_3000"]:
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
    log.info("smoke starting",
             llm=cfg.llm_url, stt=cfg.stt_url, embed=cfg.embed_url, dcgm=cfg.dcgm_url,
             chats=cfg.chats_concurrent, whisper_s=cfg.whisper_duration_s, embed_batch=cfg.embed_batch)

    started_at = datetime.now(timezone.utc).isoformat()

    dcgm_samples: list[dict[str, float]] = []
    dcgm_stop = asyncio.Event()

    async with httpx.AsyncClient(http2=False) as client:
        # Background: scrape DCGM every 1s
        dcgm_task = asyncio.create_task(
            dcgm_scrape_loop(client, cfg.dcgm_url, cfg.dcgm_interval_s, dcgm_samples, dcgm_stop)
        )

        # Foreground: main workload
        chat_prompt = _build_long_prompt(cfg.chat_prompt_tokens)
        chat_tasks = [
            run_chat_stream(client, cfg.llm_url, chat_prompt, cfg.chat_max_completion)
            for _ in range(cfg.chats_concurrent)
        ]
        whisper_task = run_whisper(client, cfg.stt_url, cfg.whisper_duration_s)
        embed_task = run_embeddings(client, cfg.embed_url, cfg.embed_batch)

        results = await asyncio.gather(*chat_tasks, whisper_task, embed_task, return_exceptions=True)

        # Tool-call validation runs after main workload so VRAM pressure isn't a confound
        tool_valid, tool_errors = await run_tool_call_test(client, cfg.llm_url)

        # Stop DCGM scrape
        dcgm_stop.set()
        await dcgm_task

    finished_at = datetime.now(timezone.utc).isoformat()

    # --- aggregate ---
    chat_results = [r for r in results[:cfg.chats_concurrent] if isinstance(r, dict)]
    whisper_result = results[cfg.chats_concurrent] if isinstance(results[cfg.chats_concurrent], dict) else {"error": str(results[cfg.chats_concurrent])}
    embed_result = results[cfg.chats_concurrent + 1] if isinstance(results[cfg.chats_concurrent + 1], dict) else {"error": str(results[cfg.chats_concurrent + 1])}

    chat_ttft = [r.get("ttft_ms") for r in chat_results if r.get("ttft_ms", -1) >= 0]
    chat_tpot = []
    for r in chat_results:
        if r.get("ttft_ms", -1) >= 0 and r.get("tokens", 0) > 1:
            gen_time = r["total_ms"] - r["ttft_ms"]
            tpot = gen_time / max(r["tokens"] - 1, 1)
            chat_tpot.append(tpot)

    vram_peak, vram_p95 = compute_vram_stats(dcgm_samples)

    errors: list[str] = []
    errors.extend(tool_errors)
    for r in chat_results:
        if r.get("error"):
            errors.append(f"chat: {r['error']}")
        if r.get("raw_error_body"):
            errors.extend(detect_cuda_errors([r["raw_error_body"]]))
    if whisper_result.get("error"):
        errors.append(f"whisper: {whisper_result['error']}")
    for e in embed_result.get("errors", []):
        if "error" in e:
            errors.append(f"embed: {e['error']}")

    report = {
        "schema_version": SCHEMA_VERSION,
        "started_at": started_at,
        "finished_at": finished_at,
        "target": {
            "llm_url": cfg.llm_url,
            "stt_url": cfg.stt_url,
            "embed_url": cfg.embed_url,
            "dcgm_url": cfg.dcgm_url,
        },
        "workload_spec": {
            "chats_concurrent": cfg.chats_concurrent,
            "chat_prompt_tokens": cfg.chat_prompt_tokens,
            "chat_max_completion_tokens": cfg.chat_max_completion,
            "whisper_duration_s": cfg.whisper_duration_s,
            "embed_batch_size": cfg.embed_batch,
        },
        "vram_peak_gb": vram_peak,
        "vram_p95_gb": vram_p95,
        "llm_p95_ttft_ms": int(np.percentile(chat_ttft, 95)) if chat_ttft else -1,
        "llm_p95_tpot_ms": int(np.percentile(chat_tpot, 95)) if chat_tpot else -1,
        "whisper_latency_s": round(whisper_result.get("latency_s", -1.0), 2),
        "embed_p95_ms": embed_result.get("p95_ms", -1),
        "tool_call_valid": tool_valid,
        "errors": errors,
        "gates": {},  # filled in below
    }
    report["gates"] = apply_gates(report)

    # git_sha (optional)
    try:
        sha = subprocess.check_output(["git", "rev-parse", "--short", "HEAD"], cwd=Path(__file__).resolve().parents[2], stderr=subprocess.DEVNULL).decode().strip()
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
        log.error("D-19 GATES FAILED", gates=report["gates"], exit=code)
    else:
        log.info("D-19 GATES PASSED")
    return code


def main() -> None:
    cfg = parse_args()
    code = asyncio.run(main_async(cfg))
    sys.exit(code)


if __name__ == "__main__":
    main()
