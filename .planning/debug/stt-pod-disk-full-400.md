# Debug — STT 400 em upload >1 MiB (disco do pod cheio) + cascade 4xx + transcode

status: RESOLVED (pod limpo + código no develop) — 2026-08-27
rota: POST /v1/audio/transcriptions (alias `whisper`, tier-0 `local-stt`)
reporte: cliente M4ia (WIHELP) — ligação 4min26s ramal 2000 sem transcrição no DiscLight

## Sintoma

Desde 2026-08-25, TODA transcrição de áudio >~65s falhava: gateway devolvia o
HTTP 400 do local-stt (`{"detail":"There was an error parsing the body"}`) ao
cliente em <1s, sem cascadear pros tier-1 saudáveis. No DiscLight: 8 CallRecords
`iaStatus=failed` / `iaError="[pipeline-ia-worker] STT gateway 400"` (25–27/08),
todos ≥1min21s; o maior `done` do período tinha 52s.

## Causa raiz (medida, não hipótese)

Pod STT unificado 3060 (Vast instance 48611358 "stt-tts-rerank-unified",
91.150.160.38:15167, speaches 0.9.0-rc.3, subiu 25/08) estava com **disco 100%
cheio** (25/25 GB — 13 GB de HF cache, sendo 3.3 GB de cache `xet` descartável).

Starlette (0.41.3) faz spool do file part multipart em
`SpooledTemporaryFile(max_size=1 MiB)`: até 1 MiB fica em RAM, acima rola pra
disco → `OSError: No space left on device` → FastAPI traduz em **HTTP 400**
"There was an error parsing the body". Por isso o threshold binário exato de
1.048.576 bytes (testado: 1048576 passa, 1050000 falha). WAV 8 kHz/16-bit mono
= 16 KB/s → 1 MiB ≈ 65s.

Segundo defeito: o gateway tratava 400 do upstream como erro TERMINAL do
cliente (política Phase 22 `isRetryableSTTStatus` excluía 4xx) — nenhum
failover pra gemini/groq/openai apesar de saudáveis.

## Fixes

1. **Pod (imediato):** `rm -rf /root/.cache/huggingface/xet` → 3.3 GB livres
   (88%). Validado E2E: gravação real 4.27 MB → gateway → 200, transcrição
   íntegra em ~10s.
2. **Cascade (gateway):** `isRetryableSTTStatus` agora retorna true pra QUALQUER
   status >= 400 (audio.go) — upstream bug pode vestir 4xx, e o gateway já
   validou o áudio no RequestAudioSecondsMiddleware. groq-whisper ganhou o
   mesmo interceptor (main.go) pra cascadear pro openai-whisper; o ÚLTIMO
   candidato (openai-whisper) fica sem interceptor de propósito — preserva o
   erro real em vez do envelope de exaustão.
3. **Transcode (gateway):** `stt_transcode.go` — WAV > 1 MiB é transcodado
   wav→Ogg/Opus 16 kHz mono 24 kbps via ffmpeg static (novo na imagem,
   `mwader/static-ffmpeg:7.1.1`, `FFMPEG_PATH=/usr/local/bin/ffmpeg`) dentro do
   RequestAudioSecondsMiddleware, ANTES do dispatch — ~16x menos bytes pro pod
   e pros tier-1. Fail-open (sem ffmpeg / erro / output maior → forward
   original). Kill-switch: `STT_TRANSCODE_DISABLED=1`. Duração de billing
   derivada do WAV original antes do transcode.

## Pendências / follow-ups

- Disco do pod segue 88% — se re-download de modelos acontecer, o xet cache
  re-enche. Opcional: `HF_HUB_DISABLE_XET=1` + `rm -rf xet` no onstart da
  instância 48611358.
- Reprocessar os 8 CallRecords failed no DiscLight via
  `POST /api/admin/calls/reprocess-ia` após deploy.

## Validação

- Unit: `go test ./gateway/internal/proxy/` (novos: stt_transcode_test.go;
  atualizados: stt_routing_robustness_test.go — 400 agora retryable).
- Live (pré-código): upload direto no pod reproduz 400 >1 MiB; após limpeza de
  disco, gravação real da M4ia transcrita via gateway edge (200, 3695 chars).
