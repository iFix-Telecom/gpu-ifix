---
phase: 23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
plan: 17
subsystem: gateway-tts
tags: [tts, openrouter, elevenlabs, prod]
requires: [23-07, 23-14, 23-16]
provides:
  - "openrouter-tts ativo em prod com elevenlabs/eleven-v3, voz eric"
  - "preço tts-1/openrouter US$ 0,00004 por caractere"
  - "Director troca response_format fora de mp3|pcm por mp3"
key-files:
  created:
    - scripts/tts-blind-test/gen_samples.py
    - .planning/phases/23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max/23-TTS-EVIDENCE.md
  modified:
    - gateway/internal/proxy/openrouter_tts_director.go
    - gateway/internal/proxy/openrouter_tts_director_test.go
decisions:
  - "Voz escolhida pelo Pedro no teste cego: letra A = elevenlabs/eleven-v3, voz eric (2026-10-15)"
  - "Sem alias TTS (CHECK da 0005): modelo e voz vêm de OPENROUTER_TTS_DEFAULT_MODEL/VOICE; a voz default substitui qualquer voz do cliente"
  - "Fallback ao vivo não provado por canário (impossível sem alias TTS); vale a prova do 23-14 + local-tts em uso até hoje; sem force-open global"
metrics:
  completed: 2026-10-15
  tasks: 3
---

# Phase 23 Plan 17: TTS PT-BR externo com a voz escolhida — Summary

O TTS do gateway sai pelo OpenRouter com `elevenlabs/eleven-v3` (voz `eric`), escolhida pelo Pedro no teste cego entre 6 candidatos (5 externos + o Kokoro atual como controle). O Kokoro em CPU (`local-tts`) fica como fallback.

## Tarefas

| Task | Resultado |
|------|-----------|
| 1 | 30 amostras MP3 (6 letras × 5 frases); Gemini TTS descartado (só aceita pcm) |
| 2 | Pedro: "voz a" → `elevenlabs/eleven-v3` / `eric` |
| 3 | Stack 38 com 3 envs novas; preço `tts-1`/`openrouter`; smokes 3/3 em 200 `audio/mpeg`, 1,8–2,2 s; billing `price_table` R$ 0,006848 por 34 chars; RES-08 503 |

## Desvios

**1. [Rule 1 - Bug] `response_format` fora de mp3|pcm dava 400.** O plano supunha que o OpenRouter respeitaria formato explícito. FATO em prod: o claude-wpp pede `wav` e recebeu 400 ZodError (2 respostas em voz perdidas, 19:52 e 19:59 UTC). O Director agora troca para `mp3` (`b25fc3f`, deploy 20:08 UTC); o `ffmpeg` do claude-wpp converte MP3 sem mudança no cliente.

**2. Sem alias canário de TTS.** A migration 0005 limita `model_aliases.upstream` a llm/stt/embed. Registrado no evidence antes da execução.

## Verificação

- `go test ./internal/proxy/` ok; integração `-run 'TTS|Haiku'` ok; CI `build-gateway` verde em `b25fc3f`.
- Prod: `wav` → 200 `audio/mpeg` (5,1 s); `mp3` → 200 (1,7 s).

## Pendências

- HIPÓTESE: respostas longas do claude-wpp podem passar do timeout de TTS dele (10 s); a frase de 275 chars levou 4,7 s no teste cego. Para resolver: medir a latência do claude-wpp em `audit_log` nos próximos dias.
