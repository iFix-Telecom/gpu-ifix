# 22-03 SUMMARY — CV-01: STT da converseia pelo gateway

**Status:** ✅ complete (ativo) · **Data:** 2026-07-19→22 · **Requirement:** CV-01

## Resultado
STT da converseia roteia pelo gateway (tenant converseai-stt) → **gemini-stt** (pt-BR), capturado em billing_events com preço do 22-01. Fallback fornecedor direto provado no código (T-22-07 real).

## Percurso
1. **1ª ativação → rollback:** funnel tecnicamente OK (usage 0→N, billing gemini-stt), mas gemini-stt transcrevia **inglês**. Root cause: `gemini_stt_director.go` descartava `language` + prompt inglês fixo.
2. **Fix do director (commit `44b1574`, gateway `develop-44b1574`):** `extractMultipartField` pega `language` + `geminiTranscribePromptFor` injeta ISO + "Do NOT translate". Provado ao vivo: gemini-stt → pt-BR. Corrige também voip STT.
3. **Incidente 21/07 (429 n8n):** descoberto que **as 2 contas OpenAI estão sem quota** → STT direto (rollback) estava quebrado. Re-ativado o funnel → gemini-stt contorna a OpenAI morta. Mic volta a funcionar (pt-BR).
4. **Durável:** 4 refs 113 fixadas no `docker-compose.yml` do repo converseai-v4 (commit `e6b1f6ff`) — sobrevive ao Deploy Dev.

## Prova
- STT via gateway (converseai-stt) → gemini-stt → `"Bom dia, tudo bem com você? Gostaria de falar sobre minha fatura."` (pt-BR, 200).
- billing_events tenant 11bfafdf: upstream=gemini-stt, cost coerente (audio_s × 0.0000096 × fx).
- Phase 21 STT cascade intacta (local-stt tier0 / gemini-stt tier1 / openai-whisper tier2).

## Artefatos
- `22-03-STT-UAT.md` — baseline, fallback, incidente, fix do director, re-ativação, pendências.

## Pendências (Pedro)
- Billing OpenAI (2 contas sem quota) — top-up se precisar direto.
- Pod local-stt (tier0) down — aguardar subir sozinho (Seg 9-17 BRT).
- WIP do Pedro em `stash@{0}` do converseai-v4 (conflito phone.ts a resolver).

## Self-Check: PASSED
