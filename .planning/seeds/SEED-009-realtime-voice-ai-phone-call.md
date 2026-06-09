---
seed: SEED-009
title: Real-time voice AI for live phone calls
status: backlog
priority: future
phase_target: 12+
captured_at: 2026-06-04
captured_by: pedro
related: [SEED-002 (emergency hot-standby), voice-api, Asterisk PJSIP]
---

# SEED-009 — Real-time voice AI conectado a ligação telefônica ao vivo

## Intent

Habilitar humano conversar com IA em tempo real durante ligação telefônica (PJSIP/Asterisk → gateway → IA → fala de volta). Diferente do `PROCESSADOR-IA-GRAVACOES-V3` atual que processa áudio POST-call em batch.

## Por que whisper batch não serve

- `whisper-1` exige áudio inteiro antes de transcrever → latência 3-10s = inviável conversação ao vivo (limite humano ~300ms turn-taking).
- `gpt-4o-transcribe` suporta streaming, viável.
- End-to-end voice models (OpenAI Realtime, Gemini Live) eliminam STT separado.

## 2 arquiteturas candidatas (decisão Phase 12+)

### Arquitetura A — Pipeline streaming STT → LLM → TTS

| Componente | Opções avaliar |
|-----------|----------------|
| Streaming STT | `gpt-4o-transcribe` (WebSocket), Deepgram Nova-3, AssemblyAI, faster-whisper local + VAD |
| LLM rápido | `gpt-4o-mini`, `google/gemini-2.5-flash-lite`, `anthropic/claude-haiku-4` |
| Streaming TTS | OpenAI TTS streaming, ElevenLabs Turbo, Cartesia Sonic, Piper local |

Latência típica: 500-1500ms turn. Custo: $0.02-0.05/min. Complexidade: alta (3 streams concorrentes + interrupção handling).

### Arquitetura B — End-to-end voice

| Modelo | Latência | $/min |
|--------|----------|-------|
| OpenAI Realtime API (`gpt-4o-realtime-preview`) | ~300ms | $0.06 in + $0.24 out |
| Gemini 2.5 Live API | ~200ms | $0.30 (preview) |

Latência típica: 200-500ms. Custo: $0.10-0.30/min. Complexidade: média (1 WebSocket bidirecional).

## Mudanças necessárias no gateway

- Novo endpoint `/v1/realtime` (WebSocket bidirectional)
- Upstream registry suporte `realtime` role
- Add upstreams: `openai-realtime`, `gemini-live`
- Tier-fallback chain real-time (Gemini Live → OpenAI Realtime)
- Bilhetagem por minuto streaming (não por token)
- Audit log adapter pra WebSocket sessions

## Integração com infra telefonia

- voice-api ifix (DEV-only atualmente, decommission pending Phase 11.5+)
- Asterisk PJSIP → ARI (Asterisk REST Interface) para captura áudio in-call
- OU Twilio Media Streams se telefonia externa
- Codec: PCM 16khz mono → OpenAI/Gemini esperam g.711/Opus

## Pré-requisitos antes do phase 12 voice-realtime

1. Phase 11 prod-hardening fechado (corpus + SLO baseline)
2. Gateway WebSocket infra (não existe hoje — só HTTP)
3. Avaliação latência real PJSIP → gateway → OpenAI realtime end-to-end (PoC 1-2 dias)
4. Decisão arquitetura A vs B (custo × latência × PT-BR quality)

## Quick-wins até lá

- Workflow atual `PROCESSADOR-IA-GRAVACOES-V3` (batch pós-call) continua útil pra análise/CRM
- Whisper batch tier-1 (OpenAI whisper-1) é suficiente para esse caso
- Gemini multimodal pode substituir cadeia STT+chat batch para reduzir custo + diarização nativa
