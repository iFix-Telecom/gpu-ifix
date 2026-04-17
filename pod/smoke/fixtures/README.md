# Smoke Fixtures

**Status:** No binary fixtures committed. All test audio is generated in-memory at test time.

## Files

| File | Purpose |
|---|---|
| `__gen_audio.py` | Pure-Python 16 kHz mono PCM WAV generator. Imported by `smoke.py` and directly runnable to produce standalone files for local development |
| `README.md` | This file |

## Why no committed WAVs

An 8-minute 16 kHz mono 16-bit WAV is ~15 MB. Committing binaries bloats git history and Docker build contexts. The deterministic generator (`__gen_audio.py`) produces identical bytes given the same seed — sufficient for smoke-test reproducibility without file-level storage.

## Regenerating a file manually (for debugging only)

```bash
cd /home/pedro/projetos/pedro/gpu-ifix
python3 pod/smoke/fixtures/__gen_audio.py 480 /tmp/smoke-audio.wav
# /tmp/smoke-audio.wav is now an 8-minute WAV
```

The `.gitignore` at repo root excludes `pod/smoke/fixtures/*.wav` to prevent accidental commits.
