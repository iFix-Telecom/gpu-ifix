---
phase: 23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
plan: 07
subsystem: gateway/proxy
tags: [tts, openrouter, director, haiku-08]
requires: [23-01]
provides: [BuildOpenRouterTTSDirector, ParseTTSVoiceMap]
affects: [23-14, 23-17]
tech-stack:
  added: []
  patterns: [director-wrap-BuildDirector, map-json-RawMessage-rewrite]
key-files:
  created:
    - gateway/internal/proxy/openrouter_tts_director.go
    - gateway/internal/proxy/openrouter_tts_director_test.go
  modified: []
decisions:
  - "Sem linha no resolver é detectado por Resolve(alias, upstream) == alias (passthrough) → usa defaultModel; se defaultModel vazio, mantém o alias"
  - "Voz não mapeada (ou ausente) cai em defaultVoice só quando defaultVoice != \"\"; sem default a voz do cliente segue intacta"
  - "response_format vazio ou só espaços também vira mp3 (Pitfall 13)"
metrics:
  duration: ~6min
  completed: 2026-10-15
requirements: [HAIKU-08]
---

# Phase 23 Plan 07: Director do openrouter-tts Summary

Director do upstream TTS externo `openrouter-tts` (OpenRouter `POST /api/v1/audio/speech`): reescreve model via resolver/defaultModel, mapeia voz (voiceMap/defaultVoice), força `response_format: mp3` só quando ausente, injeta o bearer do OpenRouter e preserva os demais campos byte a byte. O wiring fica para o 23-14.

## Tasks

| Task | Nome | Commits |
|------|------|---------|
| 1 | BuildOpenRouterTTSDirector + ParseTTSVoiceMap (TDD) | e5a6d95 (RED), 2f8053c (GREEN) |

## Verificação (FATOS)

- `gofmt -l ./internal/proxy/` vazio; `go vet ./internal/proxy/` limpo.
- `go test ./internal/proxy/ -run 'TestOpenRouterTTSDirector|TestParseTTSVoiceMap'`: 12 testes PASS (model resolvido, wav preservado, "" → mp3, defaultModel, defaultVoice para voz não mapeada e ausente, voz mantida sem default, speed/instructions byte a byte, Authorization trocado + X-API-Key removido, URL final `https://openrouter.ai/api/v1/audio/speech`, JSON inválido intacto, Content-Length).
- Pacote `internal/proxy` inteiro: `ok` (20,5 s).
- RED confirmado antes do GREEN (build falhou com `undefined: BuildOpenRouterTTSDirector`).

## Deviations from Plan

None - plan executed exactly as written.

## TDD Gate Compliance

`test(23-07)` e5a6d95 → `feat(23-07)` 2f8053c. Sem refactor.

## Threat Flags

Nenhuma superfície nova além do threat model (T-23-22 coberto por teste de auth; T-23-24: log só `upstream`/`reason`/`error_class`; T-23-23 fica no 23-14).

## Self-Check: PASSED

- FOUND: gateway/internal/proxy/openrouter_tts_director.go
- FOUND: gateway/internal/proxy/openrouter_tts_director_test.go
- FOUND: e5a6d95, 2f8053c
