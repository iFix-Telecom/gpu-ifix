# Phase 4 Wave 0 Operator Gates

**Confirmed:** 2026-04-21 (placeholder — operador optou por seed com valores do RESEARCH; revalidar em UAT 04-09)
**Confirmed by:** Pedro (operador da VPS dev — placeholder sign-off)

> **ATENÇÃO:** Estes valores vieram do RESEARCH §State of the Art (snapshot Apr 2026), não de fetch ao vivo do dashboard OpenRouter no momento da abertura do gate. Plan 04-09 (Wave 6 human-UAT) deve revalidar A1/A2/A5 contra dados ao vivo e emitir migration 0016 (`ALTER prices`) + `UPDATE fx_rates` se houver drift > 10%. Até lá, os valores aqui são o seed de partida para Plan 04-03 (migration 0015).

## A1 — OpenRouter qwen/qwen3.5-27b pricing (snapshot do RESEARCH)

Source: `.planning/phases/04-multi-tenant-quotas-billing-schedule-routing/04-RESEARCH.md` §State of the Art (Apr 2026 snapshot). Dashboard vivo NÃO consultado neste momento.

| Field | Value | Notes |
|-------|-------|-------|
| Aggregate prompt USD/1M | 0.195 | top of page (research snapshot) |
| Aggregate completion USD/1M | 1.56 | top of page (research snapshot) |
| Fireworks provider prompt USD/1M | 0.195 | assumido igual ao agregado — revalidar em UAT |
| Fireworks provider completion USD/1M | 1.56 | assumido igual ao agregado — revalidar em UAT |
| **Seed value to use (prompt USD/token)** | 0.000000195 | aggregate / 1_000_000 — vai em migration 0015 |
| **Seed value to use (completion USD/token)** | 0.00000156 | aggregate / 1_000_000 — vai em migration 0015 |

**Bifrost cost calculator divergence (RESEARCH noted $0.30/$2.40, 50% higher):** Não resolvido. Plan 04-09 operador deve confrontar dashboard ao vivo. Se drift real >10%, emitir migration 0016.

## A2 — Fireworks provider availability for qwen3.5-27b

| Question | Answer |
|----------|--------|
| Is Fireworks listed in the providers tab? | ASSUMED YES (Phase 3 D-C1 pinned fireworks — revalidar em UAT) |
| Does the provider tab indicate tool-calling support? | ASSUMED YES (Phase 3 D-C2 requires tool-calling; pin inválido sem isso) |
| If Fireworks unavailable, fallback provider chosen | Together AI (primary fallback), DeepInfra (secondary) — per RESEARCH §Open Questions |
| Does Phase 3 `UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER` env need updating? | **VERIFICAR em 04-09 UAT** — valor atual em `gateway/internal/config/config.go:112` é `["novita"]` (override). Se Fireworks indisponível, mudar para `["together","fireworks"]`. Se indisponível E Together listada, mudar para `["together"]`. |

## A5 — Whisper STT pricing (snapshot do RESEARCH)

Source: `.planning/phases/04-multi-tenant-quotas-billing-schedule-routing/04-RESEARCH.md` §State of the Art lines 828 (whisper-1 remains active; gpt-4o-transcribe retiring 2026-02-28).

| Field | Value |
|-------|-------|
| whisper-1 USD/minute | $0.006 (research snapshot) |
| whisper-1 USD/second (seed value) | 0.0001 (= 0.006 / 60) — vai em migration 0015 |

## A3 — Quota defaults sanity check

Operator manteve defaults de partida para v1 (refinar per-tenant depois via `gatewayctl tenant set-quota`):

| Dimension | Default | Operator OK? |
|-----------|---------|--------------|
| daily_tokens | 10_000_000 | YES |
| monthly_tokens | 300_000_000 | YES |
| daily_audio_minutes | 600 | YES |
| monthly_audio_minutes | 18_000 | YES |
| daily_embeds | 100_000 | YES |
| monthly_embeds | 3_000_000 | YES |
| rps | 20 | YES |
| rpm | 600 | YES |

## Sign-off

**Operator:** Pedro (placeholder sign-off via orquestrador /gsd-execute-phase 4)
**Date:** 2026-04-21 (placeholder)
**Note:** Plan 04-03 (migration 0015 seed) lê este arquivo. **Os valores A1/A2/A5 são do RESEARCH, não do dashboard vivo** — Plan 04-09 UAT deve revalidar e emitir correção (migration 0016) se necessário. Este gate desbloqueia Wave 1 com a condição de revalidação obrigatória em UAT.

### FX rate inicial

| Field | Value |
|-------|-------|
| Par | USD/BRL |
| Rate inicial | 5.10 |
| Source | `AI_GATEWAY_USD_BRL_RATE_DEFAULT` default (config Fase 4) — operador ajusta via `gatewayctl prices set-fx` após deploy |
