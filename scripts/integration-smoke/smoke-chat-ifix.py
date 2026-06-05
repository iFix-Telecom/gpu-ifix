"""scripts/integration-smoke/smoke-chat-ifix.py — INT-02 gateway transcription smoke.

Validates that Chat Ifix's WhatsApp-audio Whisper transcription works through the
gateway at equivalent latency + quality to the prior direct integration (SC2).
Posts the committed WhatsApp voice-note fixture to the gateway
`/v1/audio/transcriptions` multipart endpoint with the `chat-ifix` tenant key,
then gates BOTH transcription latency AND quality within ±10% of a recorded
baseline. Writes a machine-readable JSON report the Phase 8 HUMAN-UAT asserts on,
and maps gate failures to distinct non-zero exit codes.

This is the `pod/smoke/smoke.py` `run_whisper()` shape, retargeted from the raw
pod STT endpoint to the gateway `/v1/audio/transcriptions` endpoint. The one
structural difference vs the raw-pod smoke: the request carries an
`Authorization: Bearer <tenant-key>` header — the gateway requires auth, and
redacts that header from its logs (gateway/README.md). The other difference vs
the pod smoke: the pod smoke only checks Whisper returned 200 + non-empty text;
this script computes a word error rate (WER) against a reference transcript and a
latency ratio against a recorded baseline, and gates both within ±10%.

Secret-once discipline: the tenant key is supplied ONLY via `--api-key` or the
`SMOKE_API_KEY` env var. There is NO committed default — the script refuses to
run (argparse error, no network call) when no key is provided.

Entry point:
    python scripts/integration-smoke/smoke-chat-ifix.py \\
        --gateway-url https://gateway.ifix.com.br \\
        --api-key <chat-ifix tenant key> \\
        --out smoke-chat-ifix-report.json

Exit codes:
    0  all 3 gates passed
    2  transcription gate failed (only) — non-200 / auth / quota / upstream error
    3  quality gate failed (only) — WER > baseline wer_threshold
    4  latency gate failed (only) — latency ratio > 1 + latency_tolerance
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
import unicodedata
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import httpx
import structlog

# --- Constants ------------------------------------------------------------

SCHEMA_VERSION = "1.0.0"

STT_MODEL = "whisper"  # gateway STT alias — Phase 11.1 SEED-010 D-A5: resolves
# via the (whisper, openai-whisper) -> whisper-1 alias to the OpenAI tier-1
# Whisper upstream (pod-side STT was removed in this phase).

DEFAULT_FIXTURE = "fixtures/whatsapp-sample.ogg"
DEFAULT_BASELINE = "fixtures/whatsapp-sample.baseline.json"

# Fallback gate thresholds — used only if the baseline JSON omits them.
DEFAULT_WER_THRESHOLD = 0.10
DEFAULT_LATENCY_TOLERANCE = 0.10

log = structlog.get_logger().bind(module="SMOKE_CHAT_IFIX")


# --- Config + CLI ---------------------------------------------------------

@dataclasses.dataclass
class Config:
    gateway_url: str
    api_key: str
    out_path: str
    fixture_path: str
    baseline_path: str


def parse_args() -> Config:
    ap = argparse.ArgumentParser(
        description="Chat Ifix integration smoke — INT-02 gateway transcription "
                    "contract (WhatsApp-audio Whisper transcription, ±10% latency "
                    "+ quality gates vs a recorded baseline)"
    )
    here = Path(__file__).parent
    ap.add_argument(
        "--gateway-url",
        default=os.getenv("SMOKE_GATEWAY_URL"),
        help="base URL of the deployed gateway (e.g. https://gateway.ifix.com.br)",
    )
    ap.add_argument(
        "--api-key",
        default=os.getenv("SMOKE_API_KEY"),
        help="chat-ifix tenant API key — sent as Authorization: Bearer. "
             "Pass via --api-key or the SMOKE_API_KEY env var; NEVER committed.",
    )
    ap.add_argument(
        "--out",
        default=os.getenv("SMOKE_OUT", "smoke-chat-ifix-report.json"),
        help="path to write the JSON report",
    )
    ap.add_argument(
        "--fixture",
        default=str(here / DEFAULT_FIXTURE),
        help="path to the WhatsApp audio fixture to transcribe",
    )
    ap.add_argument(
        "--baseline",
        default=str(here / DEFAULT_BASELINE),
        help="path to the recorded baseline JSON the ±10% gates compare against",
    )
    args = ap.parse_args()

    if not args.gateway_url:
        ap.error("--gateway-url or SMOKE_GATEWAY_URL required")
    if not args.api_key:
        ap.error("--api-key or SMOKE_API_KEY required (the chat-ifix tenant key)")

    return Config(
        gateway_url=args.gateway_url.rstrip("/"),
        api_key=args.api_key,
        out_path=args.out,
        fixture_path=args.fixture,
        baseline_path=args.baseline,
    )


# --- Text normalization + word error rate ---------------------------------

def normalize_text(s: str) -> str:
    """Lowercase, strip punctuation, collapse whitespace to single spaces, trim.

    Punctuation is stripped by dropping any character whose Unicode category
    starts with 'P' (punctuation) or 'S' (symbols) — accents are preserved
    (Portuguese needs them), only separators are normalized.

    WORD-COUNT SENSITIVITY (WR-07): each P/S-category char is replaced with a
    SPACE, not with the empty string — so intra-word punctuation becomes a word
    boundary. "bem-vindo" -> "bem vindo" (2 words); "R$50" -> "r 50" (2 words).
    This is applied identically to the reference and the hypothesis, so a
    matching pair stays consistent — but the baseline transcript MUST be
    authored with this in mind: if the reference writer spells a compound
    "bemvindo" while the STT returns "bem-vindo", the word counts diverge and
    WER is inflated. Author baseline transcripts to match the STT's spacing
    and hyphenation conventions.
    """
    s = s.lower()
    out_chars: list[str] = []
    for ch in s:
        cat = unicodedata.category(ch)
        if cat[0] in ("P", "S"):
            out_chars.append(" ")
        else:
            out_chars.append(ch)
    # collapse all whitespace runs to a single space, trim
    return " ".join("".join(out_chars).split())


def word_error_rate(reference: str, hypothesis: str) -> float:
    """Classic word-level Levenshtein edit distance / len(reference words).

    Both inputs are assumed already normalized (see normalize_text). Returns:
    - 0.0 when the two word lists are identical
    - 1.0 when the hypothesis is empty and the reference is non-empty
    - edit_distance / max(len(reference_words), 1) otherwise

    Hand-rolled DP table — no `jiwer` dependency.

    LIMITATIONS (WR-07): this is the textbook WER formula (normalized by
    reference length only), so the single `wer` number cannot distinguish
    "STT returned nothing" (empty hypothesis -> exactly 1.0) from "STT
    returned one wrong word" (also near 1.0) — both correctly fail the gate,
    but the signal is lost in the report. A hypothesis far LONGER than the
    reference (Whisper hallucination / repetition loop) is dominated by
    insertions and yields WER > 1.0 — which still fails the gate, so that
    direction is safe. Word-count divergence from punctuation is documented in
    normalize_text.
    """
    ref_words = reference.split()
    hyp_words = hypothesis.split()

    if not ref_words:
        # Empty reference: WER is 0 if hypothesis is also empty, else 1.0.
        return 0.0 if not hyp_words else 1.0
    if not hyp_words:
        return 1.0

    n = len(ref_words)
    m = len(hyp_words)
    # dp[i][j] = edit distance between ref_words[:i] and hyp_words[:j]
    dp = [[0] * (m + 1) for _ in range(n + 1)]
    for i in range(n + 1):
        dp[i][0] = i
    for j in range(m + 1):
        dp[0][j] = j
    for i in range(1, n + 1):
        for j in range(1, m + 1):
            if ref_words[i - 1] == hyp_words[j - 1]:
                dp[i][j] = dp[i - 1][j - 1]
            else:
                dp[i][j] = 1 + min(
                    dp[i - 1][j],      # deletion
                    dp[i][j - 1],      # insertion
                    dp[i - 1][j - 1],  # substitution
                )
    return dp[n][m] / max(n, 1)


# --- Transcription request ------------------------------------------------

async def run_transcription(
    client: httpx.AsyncClient,
    gateway_url: str,
    fixture_bytes: bytes,
    fixture_name: str,
) -> dict[str, Any]:
    """Multipart POST /v1/audio/transcriptions; return the transcription result.

    Copies the pod/smoke/smoke.py `run_whisper()` shape, retargeted to the
    gateway. The `httpx.AsyncClient` already carries the tenant key as an
    `Authorization: Bearer` header.

    Returns on non-200: {status_code, ok: False, latency_s, text: "", raw_error_body}
    Returns on 200:      {status_code, ok: True, latency_s, text}
    Returns on exception: {status_code: -1, ok: False, latency_s, text: "", raw_error_body}
    """
    files = {"file": (fixture_name, fixture_bytes, "audio/ogg")}
    data = {"model": STT_MODEL}
    start = time.monotonic()
    try:
        r = await client.post(
            gateway_url + "/v1/audio/transcriptions",
            files=files,
            data=data,
            timeout=600.0,
        )
        latency_s = time.monotonic() - start
        if r.status_code != 200:
            return {
                "status_code": r.status_code,
                "ok": False,
                "latency_s": round(latency_s, 3),
                "text": "",
                "raw_error_body": r.text[:500],
            }
        return {
            "status_code": r.status_code,
            "ok": True,
            "latency_s": round(latency_s, 3),
            "text": r.json().get("text", ""),
        }
    except Exception as e:
        latency_s = time.monotonic() - start
        return {
            "status_code": -1,
            "ok": False,
            "latency_s": round(latency_s, 3),
            "text": "",
            "raw_error_body": str(e)[:500],
        }


# --- Gates + exit codes ---------------------------------------------------

def apply_gates(report: dict[str, Any]) -> dict[str, bool]:
    """Gate transcription status, quality (WER), and latency (ratio).

    - transcription_ok:     transcription.ok is True (status 200)
    - quality_within_10pct: comparison.wer <= baseline wer_threshold (default 0.10)
    - latency_within_10pct: comparison.latency_ratio <= 1 + latency_tolerance
                            (default tolerance 0.10 -> ratio <= 1.10)
    - all_passed:           all three above
    """
    transcription_ok = report["transcription"].get("ok") is True

    wer = report["comparison"].get("wer")
    wer_threshold = report["baseline"].get("wer_threshold", DEFAULT_WER_THRESHOLD)
    quality_within_10pct = wer is not None and wer <= wer_threshold

    latency_ratio = report["comparison"].get("latency_ratio")
    latency_tolerance = report["baseline"].get(
        "latency_tolerance", DEFAULT_LATENCY_TOLERANCE
    )
    latency_within_10pct = (
        latency_ratio is not None and latency_ratio <= 1.0 + latency_tolerance
    )

    all_passed = transcription_ok and quality_within_10pct and latency_within_10pct
    return {
        "transcription_ok": transcription_ok,
        "quality_within_10pct": quality_within_10pct,
        "latency_within_10pct": latency_within_10pct,
        "all_passed": all_passed,
    }


def exit_code_for_gates(gates: dict[str, bool]) -> int:
    """Map gate failures to distinct non-zero exit codes (bitmask pattern).

    Same shape as the converseai smoke's exit_code_for_gates:
    0 all pass; 2/3/4 single-gate failure; 6 multiple; 1 fallback.
    """
    if gates["all_passed"]:
        return 0
    failing = 0
    if not gates["transcription_ok"]:
        failing |= 0b001
    if not gates["quality_within_10pct"]:
        failing |= 0b010
    if not gates["latency_within_10pct"]:
        failing |= 0b100
    if bin(failing).count("1") > 1:
        return 6
    if failing == 0b001:
        return 2
    if failing == 0b010:
        return 3
    if failing == 0b100:
        return 4
    return 1


# --- Orchestration --------------------------------------------------------

async def main_async(cfg: Config) -> int:
    log.info("smoke starting", gateway=cfg.gateway_url,
             fixture=cfg.fixture_path, baseline=cfg.baseline_path)

    started_at = datetime.now(timezone.utc).isoformat()

    # Load the committed fixture + baseline before any network call. A
    # misnamed --fixture / --baseline arg (or a malformed baseline JSON) is an
    # operator error — fail with a clear log line + exit 1 ("fallback /
    # unexpected" per the exit-code contract) rather than letting a raw
    # traceback escape with no JSON report for the HUMAN-UAT asserter.
    fixture_file = Path(cfg.fixture_path)
    try:
        fixture_bytes = fixture_file.read_bytes()
    except OSError as e:
        log.error("cannot read fixture", path=cfg.fixture_path, err=str(e))
        return 1
    try:
        baseline = json.loads(Path(cfg.baseline_path).read_text())
    except (OSError, json.JSONDecodeError) as e:
        log.error("cannot read/parse baseline", path=cfg.baseline_path, err=str(e))
        return 1

    # Single client carries the tenant key on every request — the structural
    # difference vs the raw-pod smoke (the gateway requires auth).
    async with httpx.AsyncClient(
        headers={"Authorization": f"Bearer {cfg.api_key}"}
    ) as client:
        transcription = await run_transcription(
            client, cfg.gateway_url, fixture_bytes, fixture_file.name
        )

    finished_at = datetime.now(timezone.utc).isoformat()

    # --- compute comparison metrics ---
    ref_norm = normalize_text(baseline.get("transcript", ""))
    hyp_norm = normalize_text(transcription.get("text", ""))
    wer = word_error_rate(ref_norm, hyp_norm)

    baseline_latency_s = baseline.get("baseline_latency_s", 0.0)
    # latency_evaluable is an explicit carry rather than overloading the ratio
    # with an `inf` / `1e9` sentinel: a sentinel magic number can collide with
    # a real (absurd) ratio and the inf->1e9 substitution in the report dict
    # was a non-obvious coupling. When the baseline latency is missing /
    # non-positive, latency_ratio stays None — apply_gates already treats
    # `latency_ratio is None` as a hard gate failure.
    latency_evaluable = bool(baseline_latency_s and baseline_latency_s > 0)
    latency_ratio = (
        transcription.get("latency_s", 0.0) / baseline_latency_s
        if latency_evaluable
        else None
    )

    # --- aggregate errors ---
    errors: list[str] = []
    if not transcription.get("ok") and transcription.get("raw_error_body"):
        errors.append(f"transcription: {transcription['raw_error_body']}")
    if not baseline_latency_s or baseline_latency_s <= 0:
        errors.append(
            "baseline: baseline_latency_s missing or non-positive — "
            "latency gate cannot be evaluated"
        )

    report = {
        "schema_version": SCHEMA_VERSION,
        "started_at": started_at,
        "finished_at": finished_at,
        "target": {
            "gateway_url": cfg.gateway_url,
            "tenant": "chat-ifix",
        },
        "transcription": transcription,
        "baseline": {
            "transcript": baseline.get("transcript", ""),
            "baseline_latency_s": baseline_latency_s,
            "duration_s": baseline.get("duration_s", 0.0),
        },
        "comparison": {
            "wer": round(wer, 4),
            # null (not a sentinel) when the baseline latency was unusable —
            # the schema allows ["number", "null"] and apply_gates fails the
            # latency gate on None.
            "latency_ratio": (
                round(latency_ratio, 4) if latency_ratio is not None else None
            ),
        },
        "errors": errors,
        "gates": {},  # filled in below
    }
    # apply_gates reads the thresholds off baseline — carry them through so the
    # gate logic and the report agree, without leaking them into the schema.
    report["baseline"]["wer_threshold"] = baseline.get(
        "wer_threshold", DEFAULT_WER_THRESHOLD
    )
    report["baseline"]["latency_tolerance"] = baseline.get(
        "latency_tolerance", DEFAULT_LATENCY_TOLERANCE
    )
    report["gates"] = apply_gates(report)
    # Drop the threshold helper keys before writing — they are not schema fields
    # (baseline sub-object is additionalProperties: false).
    report["baseline"].pop("wer_threshold", None)
    report["baseline"].pop("latency_tolerance", None)

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
        schema = json.loads(
            (Path(__file__).parent / "chat-ifix-report-schema.json").read_text()
        )
        Draft202012Validator(schema).validate(report)
    except Exception as e:
        log.warning("report does not match schema; writing anyway for debugging",
                    err=str(e))

    Path(cfg.out_path).write_text(json.dumps(report, indent=2, sort_keys=True))
    log.info("report written", path=cfg.out_path, gates=report["gates"],
             wer=report["comparison"]["wer"],
             latency_ratio=report["comparison"]["latency_ratio"])

    code = exit_code_for_gates(report["gates"])
    if code != 0:
        log.error("INT-02 GATES FAILED", gates=report["gates"], exit=code)
    else:
        log.info("INT-02 GATES PASSED")
    return code


def main() -> None:
    cfg = parse_args()
    code = asyncio.run(main_async(cfg))
    sys.exit(code)


if __name__ == "__main__":
    main()
