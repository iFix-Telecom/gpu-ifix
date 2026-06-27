---
title: Fix STT/embed billing metering (audio_seconds + embeds_count sempre 0)
date: 2026-06-27
priority: high
source: v1-MILESTONE-AUDIT.md (Blocker 2)
requirements: [TEN-04, OBS-09]
---

# Blocker — metering STT/embed não grava

## Problema
`billing.Slot.AudioSecondsMs10` + `EmbedsCount` declarados (`gateway/internal/billing/accountant.go:31-32`)
e lidos (`interceptor_usage.go:207-208`) mas **NUNCA escritos** → `billing_events.audio_seconds`
e `embeds_count` sempre 0.

## Impacto (2 frentes)
1. `/consumo` dashboard mostra 0 áudio/embed mesmo com uso pago.
2. **Quota de áudio/embed por tenant NÃO-enforçada** (`quota/counters.go:87` compara contra 0) —
   tenant pode estourar quota de embed sem bloqueio. Buraco funcional no TEN-04.

## Causa
Proxies STT passam zero usage interceptors: `buildOpenAIWhisperProxy` / `buildGeminiSTTProxy` /
`buildGroqWhisperProxy` / `NewDynamicOverrideSTTProxy` (`main.go:587`). Embed proxy
(`NewEmbeddingsProxy`) também sem interceptor.

## Fix
- [ ] Wirar `usageInterceptor` em `ModifyResponse` dos 4 proxies STT (`ComposeInterceptors`).
- [ ] Parsear `duration` da resposta Whisper → `AudioSecondsMs10.Add(n)` no interceptor.
- [ ] Embed proxy: `EmbedsCount.Add(1)` no `ModifyResponse`.
- [ ] Teste: request STT/embed → `billing_events` > 0 → quota enforça.

## Relacionado
Memória `dashboard-cost-price-sync` já registrava o sintoma. Fecha TEN-04 (full) + OBS-09 (/consumo).
