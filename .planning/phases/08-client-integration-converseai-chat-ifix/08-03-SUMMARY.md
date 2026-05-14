---
phase: 08-client-integration-converseai-chat-ifix
plan: 03
subsystem: integration-smoke
tags: [smoke-test, int-02, gateway-contract, whisper-transcription, audio-fixture]
requires:
  - "gateway /v1/audio/transcriptions multipart endpoint (Phase 2)"
  - "chat-ifix tenant key (provisioned via provision-tenants.sh, plan 08-01)"
  - "scripts/integration-smoke/ scaffolding (smoke-converseai.py + report-schema.json + requirements.txt, plan 08-02)"
provides:
  - "scripts/integration-smoke/smoke-chat-ifix.py — INT-02 gateway Whisper-transcription smoke with ±10% latency + quality gates vs a recorded baseline"
  - "scripts/integration-smoke/chat-ifix-report-schema.json — committed JSON Schema for the chat-ifix transcription smoke report"
  - "scripts/integration-smoke/fixtures/whatsapp-sample.ogg — committed short real pt-BR WhatsApp voice-note clip (Opus/OGG, 16kHz mono, ~45KB)"
  - "scripts/integration-smoke/fixtures/whatsapp-sample.baseline.json — recorded baseline (ground-truth transcript + latency + gate thresholds)"
  - "scripts/integration-smoke/fixtures/README.md — provenance/format/no-PII + baseline-field documentation"
affects:
  - "Phase 8 HUMAN-UAT plan — runs smoke-chat-ifix.py against the dev gateway, asserts on its JSON report + exit code, and re-measures baseline_latency_s against the real direct integration"
tech-stack:
  added: []
  patterns:
    - "smoke-report + distinct-exit-code contract (mirrors pod/smoke/smoke.py + 08-02 smoke-converseai.py)"
    - "gateway request auth via Authorization: Bearer on httpx.AsyncClient"
    - "secret-once discipline — no committed default api key, argparse error if absent"
    - "committed short real-speech fixture (deliberate divergence from pod/smoke/fixtures/ synthetic-only convention) for transcription-quality validation"
    - "hand-rolled word-level Levenshtein WER (no jiwer dependency)"
key-files:
  created:
    - scripts/integration-smoke/smoke-chat-ifix.py
    - scripts/integration-smoke/chat-ifix-report-schema.json
    - scripts/integration-smoke/fixtures/whatsapp-sample.ogg
    - scripts/integration-smoke/fixtures/whatsapp-sample.baseline.json
    - scripts/integration-smoke/fixtures/README.md
  modified:
    - .gitignore
decisions:
  - "Audio fixture: committed a SHORT real pt-BR speech clip (5.14s, Opus/OGG, 16kHz mono, ~45KB) rather than MinIO-hosting it — keeps the smoke self-contained and CI-runnable with no external storage dependency"
  - "Fixture generated with the piper neural TTS engine (pt_BR-faber-medium voice) from a fixed generic sentence — real human-like speech with NO PII, and a fixed TTS string makes the ground-truth baseline transcript exact"
  - "STT model alias is `whisper` (confirmed against gateway/db/README.md:49 — resolves to stt/Systran/faster-whisper-large-v3)"
  - "baseline_latency_s is a conservative placeholder (4.0s), NOT a measured direct-integration number — documented in the baseline JSON + fixtures/README; the HUMAN-UAT must re-measure it"
  - "Quality gate = hand-rolled word error rate (word-level Levenshtein DP) vs the reference transcript, both normalized (lowercase, strip punctuation/symbols, collapse whitespace; accents preserved for Portuguese); no jiwer dependency"
  - "Throwaway __verify_chat_ifix.py used to satisfy the verify gate, then deleted (mirrors 08-02 decision) — directory ships only the 2 script/schema deliverables + the fixtures/ dir"
metrics:
  duration: ~25m
  completed: 2026-05-14
---

# Phase 8 Plan 03: Chat Ifix Integration Smoke Test Summary

INT-02 gateway transcription smoke — `smoke-chat-ifix.py` posts a committed real WhatsApp voice-note fixture to the gateway `/v1/audio/transcriptions` multipart endpoint with the `chat-ifix` tenant key, computes a hand-rolled word error rate + latency ratio against a recorded baseline, and gates BOTH transcription quality AND latency within ±10% (SC2) with distinct non-zero exit codes.

## What Was Built

### Task 1 — WhatsApp audio fixture + baseline + README + .gitignore carve-in (commit `1490112`)

- **`scripts/integration-smoke/fixtures/whatsapp-sample.ogg`** — a 5.14-second clip of clear Brazilian-Portuguese speech in WhatsApp's native voice-note format (Opus codec, OGG container, 16 kHz mono), ~45 KB on disk. Synthesized with the `piper` neural TTS engine (`pt_BR-faber-medium` voice) from a fixed generic sentence, then encoded to 16 kHz mono Opus/OGG. Contains **no PII** — a single generic sentence about audio transcription, chosen for codec/container realism. Using a fixed TTS string makes the ground-truth baseline transcript exact.
- **`scripts/integration-smoke/fixtures/whatsapp-sample.baseline.json`** — the recorded baseline the ±10% gates compare against: `transcript` (the exact spoken text — the WER reference), `baseline_latency_s` (4.0, a documented conservative placeholder pending HUMAN-UAT re-measurement), `wer_threshold` 0.10, `latency_tolerance` 0.10, plus `duration_s`/`codec`/`sample_rate_hz`/`channels`/`format` metadata and a `baseline_latency_note` explaining the placeholder.
- **`scripts/integration-smoke/fixtures/README.md`** — mirrors `pod/smoke/fixtures/README.md`: a `**Status:**` line explaining a short real clip IS committed here (the deliberate divergence from the pod fixtures' synthetic-only convention — transcription-quality validation needs real speech); a `## Files` table; `## Why a real clip is committed here`; `## Provenance & format` (TTS origin, no-PII, codec/container/duration); `## Baseline` (every baseline field documented + the latency-re-measurement caveat); `## Regenerating the clip`.
- **`.gitignore`** — an explicit allow-rule (`!scripts/integration-smoke/fixtures/whatsapp-sample.ogg` + `!...baseline.json`) placed next to the existing pod-fixtures binary-exclusion block, with a comment that Phase 8 deliberately commits the short real clip. `git check-ignore` confirms the clip is NOT ignored.

### Task 2 — `chat-ifix-report-schema.json` + `smoke-chat-ifix.py` (commit `2d626b8`)

- **`scripts/integration-smoke/chat-ifix-report-schema.json`** — JSON Schema (Draft 2020-12), `$id` `https://ifixtelecom.com.br/schemas/integration-smoke/chat-ifix-report/1.0.0`, `additionalProperties: false`. Required top-level keys: `schema_version` (const `"1.0.0"`), `started_at`/`finished_at` (date-time), `target` (`gateway_url` uri + `tenant` string — **not** the api key), `transcription` (`status_code`/`ok`/`latency_s`/`text` + optional `raw_error_body`), `baseline` (`transcript`/`baseline_latency_s`/`duration_s`), `comparison` (`wer`/`latency_ratio`), `errors`, `gates`. Optional `git_sha` (hex pattern). The `gates` sub-object requires `transcription_ok`/`quality_within_10pct`/`latency_within_10pct`/`all_passed` (all boolean), `additionalProperties: false`.
- **`scripts/integration-smoke/smoke-chat-ifix.py`** (435 lines, `min_lines` 140 satisfied) — the `pod/smoke/smoke.py` `run_whisper()` shape, reusing the 08-02 `smoke-converseai.py` scaffolding (Config dataclass, `parse_args()` with `--api-key`/`SMOKE_API_KEY` env fallback + `ap.error` guard, the `Authorization: Bearer` AsyncClient, the report-build + schema-validate + write block, `main()`):
  - `normalize_text(s)` — lowercase, strip punctuation/symbols (any Unicode category `P*`/`S*`), collapse whitespace, trim. Accents preserved (Portuguese needs them).
  - `word_error_rate(reference, hypothesis)` — classic word-level Levenshtein edit distance over the normalized word lists / `max(len(reference_words), 1)`. Hand-rolled DP table — **no `jiwer` dependency**. Returns 0.0 for identical, 1.0 for empty-hyp-vs-nonempty-ref, proportional otherwise.
  - `run_transcription()` — copies the `run_whisper()` shape: `files = {"file": (name, bytes, "audio/ogg")}`, `data = {"model": "whisper"}` (the gateway STT alias), multipart POST `/v1/audio/transcriptions`, `timeout=600.0`; measures `latency_s`; captures `status_code` + truncated `raw_error_body` on non-200 or exception.
  - `apply_gates()` — `transcription_ok` (status 200), `quality_within_10pct` (`wer <= baseline wer_threshold`, default 0.10), `latency_within_10pct` (`latency_ratio <= 1 + latency_tolerance`, default ratio ≤ 1.10), `all_passed` (all three). `exit_code_for_gates()` — same bitmask-to-distinct-code pattern as the converseai smoke: 0 all-pass, 2/3/4 single-gate failure, 6 multiple, 1 fallback.
  - `main_async()` — loads the fixture bytes + baseline JSON before any network call; runs the transcription; computes `comparison.wer` (from the normalized transcripts) and `comparison.latency_ratio` (`live / baseline_latency_s`, `inf`→`1e9` sentinel + an error string if the baseline latency is missing/non-positive); builds the report; jsonschema-validates (warn-but-write on failure); writes `json.dumps(report, indent=2, sort_keys=True)`; returns the gate exit code.
- Secret-once discipline: `parse_args()` has no hardcoded key — `--api-key` defaults to `os.getenv("SMOKE_API_KEY")` and `ap.error(...)` exits non-zero (no network call) when absent.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] throwaway verify test used an input that did not isolate what it claimed to test**
- **Found during:** Task 2 (verify gate)
- **Issue:** The `__verify_chat_ifix.py` normalization assertion compared `normalize_text("Olá, mundo!")` against `normalize_text("ola MUNDO")` expecting WER 0.0 — but `normalize_text` deliberately *preserves accents* (Portuguese needs them), so `olá` ≠ `ola` and the WER was 0.5, not 0.0. The test input, not the `smoke-chat-ifix.py` code, was wrong: it conflated a case/punctuation/whitespace difference with an accent difference.
- **Fix:** Corrected the test input to `"olá   MUNDO"` (differs only in case/whitespace → WER 0.0) and added a second assertion that a *real* accent difference (`olá` vs `ola`) IS counted as a word error (0.5). `smoke-chat-ifix.py` itself was unchanged.
- **Files modified:** `scripts/integration-smoke/__verify_chat_ifix.py` (throwaway, deleted after the verify gate passed)
- **Commit:** n/a (file deleted, not committed — see Decisions)

### Build-environment notes (not code deviations)

- The ops box had no `ffmpeg`/`opusenc`/`espeak` and an externally-managed Python (`pip install` blocked by PEP 668). The fixture was produced by: (a) `piper` neural TTS (already installed) for the speech WAV, after downloading the `pt_BR-faber-medium` voice; (b) a throwaway venv with `pip` bootstrapped via `get-pip.py` and `pyav` installed *only as a build-time Opus encoder*. None of this is a runtime dependency of `smoke-chat-ifix.py` — the script needs only `httpx`/`structlog`/`jsonschema` (already in `requirements.txt`). The regeneration steps in `fixtures/README.md` document the simpler `ffmpeg`/`opusenc` path for anyone with those tools.

## Verification Results

All plan `<verification>` items pass:
- `scripts/integration-smoke/fixtures/whatsapp-sample.ogg` is committed, tracked (`git check-ignore` confirms NOT ignored), non-empty, 44.9 KB (< 200 KB), real human-like speech (not synthetic tones). `whatsapp-sample.baseline.json` (non-empty `transcript`, `baseline_latency_s` 4.0, `wer_threshold`/`latency_tolerance` 0.10) and `fixtures/README.md` (mirrors `pod/smoke/fixtures/README.md`, documents provenance/format/no-PII + baseline fields) exist.
- `chat-ifix-report-schema.json` is valid Draft 2020-12 JSON Schema with `transcription`/`baseline`/`comparison`/`gates` in `required` and a `gates` sub-object requiring `transcription_ok`/`quality_within_10pct`/`latency_within_10pct`/`all_passed`; `additionalProperties: false`.
- `smoke-chat-ifix.py` compiles (`py_compile`), refuses to run without an api key (`ap.error`, exit 2, no network call), and sends the request as multipart with an `Authorization: Bearer` header.
- The throwaway `__verify_chat_ifix.py` import-and-assert check passed (`chat-ifix gate logic OK`): `word_error_rate` is 0.0 identical / 1.0 empty-vs-nonempty / 0.2 single-substitution-in-5 / proportional; `apply_gates` gates BOTH quality (WER ≤ 0.10) AND latency (ratio ≤ 1.10) AND status 200; single-gate failures map to distinct exit codes 2/3/4, multiple to 6; boundary cases (WER exactly 0.10, ratio exactly 1.10) pass; the built report dict validates against `chat-ifix-report-schema.json` and `additionalProperties: false` rejects an unknown top-level key.
- An end-to-end offline run (`--gateway-url http://127.0.0.1:1`) exercised the load-fixture → compute-WER → build-report → schema-validate → write path: connection failure correctly flips `transcription_ok` + `quality_within_10pct` (WER 1.0 on empty text) → exit 6, and the produced report validates against the schema.

The live run against a deployed gateway is a Phase 8 HUMAN-UAT action — the gateway is not deployed yet (build-gateway blocked on Phase 6 emerg integration tests, per 08-CONTEXT.md Deferred Ideas). The HUMAN-UAT must also re-measure `baseline_latency_s` against the real direct integration before the `latency_within_10pct` gate is meaningful (documented in the baseline JSON + `fixtures/README.md`).

## Threat Mitigations Applied

- **T-08-09 (committed audio fixture containing real customer audio / PII):** the clip is a `piper`-synthesized generic sentence — not a real customer message, no PII. `fixtures/README.md` `## Provenance & format` documents this explicitly.
- **T-08-10 (tenant key as a committed default):** `parse_args()` has no hardcoded key — `--api-key` defaults to `os.getenv("SMOKE_API_KEY")` and `ap.error(...)` exits non-zero when absent. The script cannot run (or be committed) with a baked-in credential.
- **T-08-11 (report leaking the tenant key):** `chat-ifix-report-schema.json` `target` carries `gateway_url` + `tenant` slug only; the api key is not a schema field and `additionalProperties: false` rejects any report that adds it. `transcription.text` is the fixture's generic phrase, not customer data.
- **T-08-12 (a gateway 401/429/503 silently passing as a successful transcription):** `run_transcription` captures `status_code` + truncated `raw_error_body` on non-200; `apply_gates()` keys `transcription_ok` off `transcription.ok` (which is `status_code == 200`), so any auth/quota/upstream failure flips the gate and yields a distinct non-zero exit code (2 alone, 6 with others).
- **T-08-13 (a degraded gateway returning slow-but-200 transcriptions going unnoticed):** the `latency_within_10pct` gate (`latency_ratio <= 1 + latency_tolerance`) actively fails on latency regression — a slow-but-successful transcription still flips the gate and produces exit 4.

## Known Stubs

`baseline_latency_s` (4.0) in `whatsapp-sample.baseline.json` is a **documented conservative placeholder**, not a measured direct-integration number. It is intentionally a placeholder, not an oversight: the prior direct-integration latency for Chat Ifix has not been measured (the gateway is not deployed yet). The placeholder is explicitly documented in the baseline JSON (`baseline_latency_note`) and in `fixtures/README.md` `## Baseline`, and the Phase 8 HUMAN-UAT plan is the designated point where it must be re-measured against the real direct integration. Until then the `latency_within_10pct` gate compares against the conservative estimate — the smoke test is fully runnable, only the latency baseline needs a real number.

## Self-Check: PASSED

- `scripts/integration-smoke/smoke-chat-ifix.py` — FOUND
- `scripts/integration-smoke/chat-ifix-report-schema.json` — FOUND
- `scripts/integration-smoke/fixtures/whatsapp-sample.ogg` — FOUND
- `scripts/integration-smoke/fixtures/whatsapp-sample.baseline.json` — FOUND
- `scripts/integration-smoke/fixtures/README.md` — FOUND
- `.gitignore` — FOUND (modified)
- Commit `1490112` (Task 1) — FOUND
- Commit `2d626b8` (Task 2) — FOUND
