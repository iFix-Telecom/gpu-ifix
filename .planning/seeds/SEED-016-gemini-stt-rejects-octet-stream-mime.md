# SEED-016 — gemini-stt upstream rejects `application/octet-stream`; gateway forwards client MIME unchanged → 502 on audio without explicit type

**Discovered:** 2026-06-16 wiring the gemini-stt tier-1 fallback into prod (n8n-ia-vm).
**Severity:** LOW-MED — STT fallback works ONLY when the client sends a real audio MIME. Any client (or the gateway's own probe) sending `application/octet-stream` gets a 502, silently defeating the gemini fallback.
**Related:** the STT fallback chain (gemini-stt FALLBACK_1 → groq → openai-whisper). [[SEED-014]] context (pod off → STT relies on tier-1).

## Mechanism

- `gemini-stt` upstream = Google `https://generativelanguage.googleapis.com/v1beta`, model `gemini-2.5-flash-lite`.
- Google's API requires a concrete audio MIME (`audio/wav`, `audio/mpeg`, …). It rejects `application/octet-stream` with: `{"error":{"code":"INVALID_ARGUMENT","message":"Unsupported MIME type: application/octet-stream"}}` → gateway returns **HTTP 502** `upstream_error`.
- The gateway forwards the multipart file's `Content-Type` as-is to the gemini adapter. If the client doesn't set an explicit audio type, multipart defaults to `application/octet-stream` → 502.

## Live evidence (prod, 2026-06-16, pod off)

```
curl -F "file=@t.wav"                       → 502 "Unsupported MIME type: application/octet-stream"
curl -F "file=@t.wav;type=audio/wav"        → 200 {"text":"Hi there, I'm Jeff Bezos."}
curl -F "file=@t.mp3;type=audio/mpeg"       → 200 {"text":"Yes"}
```

## Impact on the voice-api n8n flow

The 2-step flow (download mp3 → STT `model=whisper` → LLM diarize) works only if the downloaded mp3 binary carries `audio/mpeg`. n8n inherits the binary `mimeType` from the S3 `Content-Type`. If MinIO/S3 serves `.mp3` as `application/octet-stream`, the gemini STT step 502s. Operator check:
`curl -sI https://s3.ifixtelecom.com.br/integracoesclientes/gravacoes_ligacoes/<id>.mp3 | grep -i content-type`

## Fix directions

1. **Gateway hardening (preferred):** in the gemini-stt (and STT) adapter, when the incoming `Content-Type` is empty/`application/octet-stream`, infer a real audio MIME from the filename extension (`.mp3`→`audio/mpeg`, `.wav`→`audio/wav`, `.ogg`→`audio/ogg`, …) before forwarding to Google. One small adapter change makes the fallback robust to any client.
2. **Probe parity:** the STT probe sends a synthetic `probe.wav` (multipart) — verify it sets `audio/wav`, else gemini-stt probe would 4xx/502 (relates to the probe-truth family SEED-012/013; note gemini-stt probe currently reads `-`).
3. **Operator stopgap:** ensure the S3 bucket serves audio objects with correct `Content-Type`, or have n8n force the binary mimeType before the STT node.
