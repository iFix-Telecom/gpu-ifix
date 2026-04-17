"""Synthetic WAV generator for Whisper smoke-test.

Produces a 16 kHz mono 16-bit PCM WAV with alternating silence and
pseudo-speech noise so Whisper actually exercises the decoder path
(pure silence is a trivial case and doesn't stress the model).

The file is generated in-memory at smoke-test time and never committed to git.
"""

from __future__ import annotations

import io
import math
import random
import struct
import wave
from typing import Union


SAMPLE_RATE = 16_000  # Whisper expects 16 kHz
CHANNELS = 1
BITS_PER_SAMPLE = 16


def generate_wav_bytes(duration_seconds: int = 480, seed: int = 42) -> bytes:
    """Generate `duration_seconds` of alternating silence/noise 16 kHz mono PCM WAV.

    - Every 10s: 7s of silence + 3s of low-amplitude noise (simulates speech bursts).
    - Seed makes output reproducible across smoke runs.
    """
    rng = random.Random(seed)
    buf = io.BytesIO()
    with wave.open(buf, "wb") as wav:
        wav.setnchannels(CHANNELS)
        wav.setsampwidth(BITS_PER_SAMPLE // 8)
        wav.setframerate(SAMPLE_RATE)

        total_samples = SAMPLE_RATE * duration_seconds
        period_samples = SAMPLE_RATE * 10  # 10-second cycles
        noise_start = SAMPLE_RATE * 7      # silence for first 7s of each cycle
        amplitude = 1200                   # quiet — well below 16-bit max (32767)

        frames = bytearray()
        for i in range(total_samples):
            phase = i % period_samples
            if phase < noise_start:
                sample = 0
            else:
                # Low-amplitude brownian-ish noise — easy for Whisper to classify
                sample = int(amplitude * (rng.random() - 0.5) * 2 * math.sin(i / 53.0))
                sample = max(min(sample, 32767), -32768)
            frames.extend(struct.pack("<h", sample))
        wav.writeframes(bytes(frames))
    return buf.getvalue()


def write_wav(path: str, duration_seconds: int = 480, seed: int = 42) -> None:
    data = generate_wav_bytes(duration_seconds, seed)
    with open(path, "wb") as fh:
        fh.write(data)


if __name__ == "__main__":
    import sys

    secs = int(sys.argv[1]) if len(sys.argv) > 1 else 480
    out = sys.argv[2] if len(sys.argv) > 2 else "synthetic-audio.wav"
    write_wav(out, duration_seconds=secs)
    print(f"wrote {out} ({secs}s @ {SAMPLE_RATE} Hz mono)")
