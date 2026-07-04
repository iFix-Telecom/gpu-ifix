---
quick_id: 260704-rkc
slug: stt-gemini-mime-sniff
type: quick
date: 2026-07-04
status: complete
files_modified:
  - gateway/internal/proxy/gemini_stt_director.go
  - gateway/internal/proxy/gemini_stt_director_test.go
commits:
  - 05b742a fix(260704-rkc): sniff audio MIME from magic bytes for Gemini STT
  - d89a707 test(260704-rkc): cover audio MIME sniffing for Gemini STT
---

# Quick 260704-rkc: STT Gemini MIME sniffing fix — Summary

Corrige o STT tier-1 do gateway (Gemini 2.5 Flash Lite) quando o cliente envia
áudio sem `Content-Type` de áudio válido (chega como `application/octet-stream`).
Antes, `extractAudioFromMultipart` repassava o MIME genérico verbatim ao Gemini,
que respondia HTTP 502 `INVALID_ARGUMENT: Unsupported MIME type:
application/octet-stream`. Agora o tipo real é detectado pelos magic bytes.

## Diff summary

### Task 1 — `gateway/internal/proxy/gemini_stt_director.go` (commit `05b742a`)

- **Novo helper `sniffAudioMIME(audio []byte) string`** — detecta o formato pelos
  magic bytes, com guardas de comprimento antes de qualquer indexação:
  - `OggS` → `audio/ogg`
  - `RIFF`…`WAVE` (0-3 + 8-11) → `audio/wav`
  - `fLaC` → `audio/flac`
  - EBML `0x1A 45 DF A3` → `audio/webm`
  - offset 4-7 = `ftyp` → `audio/mp4`
  - `ID3` → `audio/mpeg`
  - MPEG frame-sync (`0xFF` + `(b1 & 0xE0)==0xE0`) → `audio/mpeg`
  - desconhecido / curto / vazio → `audio/wav` (fallback seguro que o Gemini aceita)
- **`extractAudioFromMultipart`** — bloco de MIME substituído: confia no declarado
  APENAS se começar com `audio/`; caso contrário (vazio, `application/octet-stream`,
  qualquer não-`audio/*`) usa `sniffAudioMIME(audio)`.
- 1 file changed, 54 insertions(+), 3 deletions(-).

### Task 2 — `gateway/internal/proxy/gemini_stt_director_test.go` (commit `d89a707`)

- `TestSniffAudioMIME` — table-driven: ogg/wav/flac/webm/mp4_ftyp/mp3_id3/
  mp3_framesync/unknown/empty/short_1byte/short_riff_only (11 subtests, cobre
  fallback + guarda contra panic em buffers curtos).
- `TestExtractAudioFromMultipart_MissingContentTypeSniffsOgg` — CT ausente + OggS →
  `audio/ogg` (pré-fix retornava `audio/wav` hardcoded).
- `TestExtractAudioFromMultipart_OctetStreamSniffsWav` — `application/octet-stream`
  + RIFF/WAVE → `audio/wav` (NÃO repassa octet-stream).
- `TestExtractAudioFromMultipart_ConcreteAudioMimePassthrough` — `audio/mpeg`
  declarado → passthrough intacto.
- Helper `forwardedInlineMime` reaproveita a fixture existente (`newGeminiFixture`
  + `buildMultipart` + `doRequest`) para ler o `inline_data.mime_type` que o
  director encaminhou ao fake Gemini upstream.
- 1 file changed, 80 insertions(+).

## Verification output

```
$ cd gateway && gofmt -l internal/proxy/gemini_stt_director.go internal/proxy/gemini_stt_director_test.go
(vazio — clean)

$ go build ./...
(ok — sem erros)

$ go test ./internal/proxy/ -run 'Gemini|SniffAudio|ExtractAudio|MultipartMime' -count=1 -v
--- PASS: TestBuildGeminiSTTDirector_SetsXGoogApiKeyHeader
--- PASS: TestBuildGeminiSTTDirector_StripsAuthorizationHeader
--- PASS: TestBuildGeminiSTTDirector_MultipartToJSON_AudioBytesPreserved
--- PASS: TestBuildGeminiSTTDirector_ResolvesModelViaEnvOverride
--- PASS: TestBuildGeminiSTTDirector_FlattenResponse
--- PASS: TestSniffAudioMIME (11 subtests PASS)
--- PASS: TestExtractAudioFromMultipart_MissingContentTypeSniffsOgg
--- PASS: TestExtractAudioFromMultipart_OctetStreamSniffsWav
--- PASS: TestExtractAudioFromMultipart_ConcreteAudioMimePassthrough
--- PASS: TestBuildGeminiSTTDirector_TranslatesGeminiErrorEnvelope
PASS
ok  github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy  0.038s

$ go test ./internal/proxy/ -count=1
ok  github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy  13.689s
(suíte completa verde — zero regressão)
```

## Deviations from Plan

None — plano executado exatamente como escrito.

## Notes

- SEM build/push/deploy — código + testes unitários apenas. O deploy (rebuild da
  imagem gateway + recreate no worker-vm, endpoint atual pós-Phase 19 cutover) é
  um passo de ops separado.

## Self-Check: PASSED

- `gateway/internal/proxy/gemini_stt_director.go` — FOUND
- `gateway/internal/proxy/gemini_stt_director_test.go` — FOUND
- commit `05b742a` — FOUND
- commit `d89a707` — FOUND
