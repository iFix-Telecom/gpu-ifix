# SEED-018 — Gateway does not rewrite the STT/audio model field; primary override sends literal alias `whisper` to speaches → 404 when pod is UP

**Discovered:** 2026-06-17 right after the primary pod came back up (SEED-014 resolved).
**Severity:** HIGH (functional) — STT is BROKEN whenever the primary pod is Ready. The pod override routes `/v1/audio/transcriptions` to the pod's speaches with the literal public alias `whisper`, which speaches rejects: `{"detail":"Model 'whisper' is not installed locally"}` (HTTP 404). Ironically STT WORKS when the pod is down (tier-1 gemini-stt tolerates `whisper`). So bringing the pod up regresses STT.
**Related:** [[SEED-013-probe-hardcodes-qwen-model-no-per-upstream-rewrite]] (same "model not rewritten per upstream" family, LLM probe). [[SEED-014]] (the pod that, once up, exposed this).

## Mechanism

- model_alias (verified): `whisper` → upstream `local-stt` → target **`Systran/faster-whisper-large-v3`**.
- The pod's speaches HAS that model installed: direct probe `model=Systran/faster-whisper-large-v3` → **200** `{"text":"Thank you."}`. Direct probe `model=whisper` → 404.
- `gateway/internal/proxy/audio.go` is a plain `httputil.ReverseProxy` whose director **preserves the multipart body untouched** (by design — "Multipart body preservation: BuildDirector does NOT touch Content-Type"). It never rewrites the `model` form field. For LLM (JSON body) the director DOES rewrite (`director.go rewriteRequestBody`); for STT (multipart) it does not.
- The primary override has precedence (same as the breaker-bypass finding): `gatewayctl upstreams disable local-stt` did NOT help — the override still routes STT to the pod's speaches.

## Live evidence (2026-06-17, pod 41344114 on machine 7970, Ready)

```
gateway /v1/audio/transcriptions model=whisper          → 404 "Model 'whisper' is not installed"
pod speaches direct model=Systran/faster-whisper-large-v3 → 200 {"text":"Thank you."}
pod speaches /v1/models                                   → lists Systran/faster-whisper-large-v3
upstreams disable local-stt                               → no effect (override precedence)
```

## Fix options

1. **Pod-side speaches alias (recommended — aligns with override architecture):** bake a speaches `model_aliases.json` entry `{"whisper": "Systran/faster-whisper-large-v3"}` into the pod image so speaches accepts the literal `whisper` the gateway sends. No gateway multipart complexity; works on the override path. Cost: pod image rebuild + re-provision (~15min build + ~10min cold-start on the now-blocklisted-clean allowlist).
2. **Gateway multipart rewrite:** buffer the audio body, parse multipart, replace the `model` field with the resolved per-upstream target, re-encode. Fixes all STT upstreams uniformly + keeps the pod up (gateway-only rebuild ~5min). More invasive — conflicts with the streaming/body-cap/replay design of `audio.go`; must also apply on the override path.
3. **Client stopgap (NOT durable):** n8n sends `model=Systran/faster-whisper-large-v3`. Works while pod up; breaks the gemini-stt fallback when pod down (gemini lacks that model id). Only acceptable during pod hours, defeats the alias abstraction.

## Note

The same gap likely affects TTS/embed if any upstream validates the model id strictly (TTS chatterbox accepted `tts-1` → 200, so it's tolerant; speaches is strict). A general "rewrite model per resolved upstream on every role, including the override path" is the root fix (supersedes SEED-013 + this).
