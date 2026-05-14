# Integration-Smoke Fixtures

**Status:** A SHORT real-speech clip **IS committed here** — `whatsapp-sample.ogg` (~45 KB, 5.14 s).
This is a deliberate divergence from `pod/smoke/fixtures/`, which commits **no** binaries and
generates synthetic noise in-memory. The INT-02 transcription smoke (`smoke-chat-ifix.py`) gates
transcription **quality** (word error rate) against a reference transcript — synthetic tones
cannot validate quality, so a real speech sample is required. The clip is short enough
(WhatsApp-style Opus/OGG, ~45 KB) that committing it does not bloat git history.

## Files

| File | Purpose |
|---|---|
| `whatsapp-sample.ogg` | A ~5 s clip of clear Brazilian-Portuguese speech in WhatsApp's native voice-note format (Opus codec, OGG container, 16 kHz mono). The audio input `smoke-chat-ifix.py` POSTs to the gateway `/v1/audio/transcriptions` endpoint. |
| `whatsapp-sample.baseline.json` | The recorded baseline the `±10%` latency + quality gates compare against — the ground-truth transcript, the (placeholder) direct-integration latency, and the gate thresholds. |
| `README.md` | This file. |

## Why a real clip is committed here

`pod/smoke/fixtures/README.md` explains the pod smoke commits no WAVs: an 8-min 16 kHz WAV is
~15 MB, and the pod smoke only needs Whisper to return `200` + non-empty text — synthetic
silence/noise from `__gen_audio.py` is sufficient for that liveness check.

The Phase 8 Chat Ifix smoke is different. INT-02 / SC2 requires validating that transcription
**quality** through the gateway stays within `±10%` of the prior direct integration. Quality is
measured as word error rate (WER) between the live transcript and a known reference transcript —
which is only meaningful against **real human speech**. A synthetic tone has no "correct"
transcript to compare against. So this directory commits a deliberately short, generic real-speech
clip: small enough for git, real enough to exercise (and grade) the Whisper decode path.

## Provenance & format

- **Provenance:** The clip was synthesized from a fixed text string using the `piper` neural
  text-to-speech engine (`pt_BR-faber-medium` voice) on the ops box, then encoded to Opus/OGG.
  It is **not** a real customer message and contains **no PII** — it is a single generic sentence
  about audio transcription, chosen for codec/container realism, not content. Using a fixed
  TTS string also makes the ground-truth `transcript` in `whatsapp-sample.baseline.json` exact.
- **Spoken content (the reference transcript):**
  `"Olá, este é um teste de transcrição de áudio para o gateway de inteligência artificial."`
- **Format:** Opus codec inside an OGG container — WhatsApp's native voice-note format —
  16 kHz, mono. Duration ~5.14 s, ~45 KB on disk.
- **Why this format:** WhatsApp delivers voice notes as Opus/OGG; transcribing that container
  end-to-end through the gateway is the exact INT-02 integration surface. Committing the clip in
  WhatsApp's real codec (rather than a decoded WAV) keeps the smoke test faithful to production.

## Baseline

`whatsapp-sample.baseline.json` is loaded by `smoke-chat-ifix.py`; the live transcription result
is gated against it within `±10%`:

| Field | Meaning |
|---|---|
| `audio_file` | The clip this baseline pairs with (`whatsapp-sample.ogg`). |
| `format` / `codec` / `sample_rate_hz` / `channels` | The committed clip's container/codec/format. |
| `duration_s` | Clip length in seconds (~5.14). |
| `transcript` | The ground-truth / reference transcript — the exact text spoken in the clip. The quality gate computes WER between this and the live transcript (both normalized: lowercased, punctuation stripped, whitespace collapsed). |
| `baseline_latency_s` | The reference transcription latency the latency gate compares against. **Currently a conservative placeholder, NOT a measured direct-integration number** — see `baseline_latency_note`. The Phase 8 HUMAN-UAT must re-measure the real direct-integration latency for a clip this size and update this field. |
| `baseline_latency_note` | Documents that `baseline_latency_s` is a placeholder pending re-measurement during the HUMAN-UAT. |
| `wer_threshold` | The quality gate passes when `WER <= wer_threshold` (`0.10` — at most 10% of reference words wrong). |
| `latency_tolerance` | The latency gate passes when `live_latency_s <= baseline_latency_s * (1 + latency_tolerance)` (`0.10` — within +10%). |

## Regenerating the clip (for debugging only)

The clip was produced on the ops box with:

```bash
# 1. download a Brazilian-Portuguese piper voice
python3 -m piper.download_voices --download-dir /tmp/piper-voices pt_BR-faber-medium

# 2. synthesize the fixed sentence to a WAV
echo "Olá, este é um teste de transcrição de áudio para o gateway de inteligência artificial." \
  | piper -m /tmp/piper-voices/pt_BR-faber-medium.onnx -f /tmp/whatsapp-sample.wav

# 3. encode WAV -> 16 kHz mono Opus/OGG (WhatsApp voice-note format)
#    any Opus encoder works: ffmpeg, opusenc, or pyav (used here)
#    ffmpeg equivalent:  ffmpeg -i /tmp/whatsapp-sample.wav -ar 16000 -ac 1 -c:a libopus whatsapp-sample.ogg
```

If you regenerate the clip, you MUST also update `transcript` (if the spoken text changes) and
`duration_s` in `whatsapp-sample.baseline.json` to keep the baseline in sync.
