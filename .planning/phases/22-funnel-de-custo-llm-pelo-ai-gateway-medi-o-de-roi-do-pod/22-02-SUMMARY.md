# 22-02 SUMMARY — GATE runtime CV-01 + re-land Phase 113

**Status:** ✅ complete · **Data:** 2026-07-19 · **Requirement:** CV-01 (gate)

## Gate (Task 1) — VEREDITO inicial: AUSENTE
A imagem em execução no stack 15 (`converseai-dev-agents`, digest `95c9aa22`) **NÃO** tinha o código Phase 113. Prova 2 níveis: config sem os 5 campos `*_gateway_key`; zero consumidor; único gateway na imagem = RAG HyDE (pré-existente). STT hoje = `AsyncOpenAI(openai_api_key)` sem base_url → OpenAI direto (gpt-4o-mini-transcribe). Confirma pesquisa (commits 113 dangling). Setar `STT_GATEWAY_KEY` seria no-op silencioso.

## Re-land (Task 3, checkpoint aprovado)
- Cherry-pick **6 commits** (plano listava 5 — faltava o TDD `1d8825aa` criador de `test_provider_gateway.py`) → branch `phase22-reland-113` sobre develop.
- Único conflito (`test_provider_gateway.py` DU) resolvido incluindo `1d8825aa`.
- **28 testes 113 verdes** (venv uv local — CI py roda `|| true`, não valida).
- PR **#16** → merge `66fcf05c` em origin/develop (113 não-dangling).
- Deploy Dev 29698406637 (dispatch manual — push-merge não disparou run) → build 8 imagens + recreate stack ✅.

## Re-check — VEREDITO final: PRESENTE
Container novo digest `b19252ba`: `hasattr(stt_gateway_key)`+`hasattr(agent_classifier_gateway_key)`=True; consumidores em `llm/provider.py` (classifier/format-hint/ai-match) + `media/transcription.py:90` (`if settings.stt_gateway_key:`). **Inerte** (keys default "" → fornecedor direto). CV-01 STT (22-03) + secundárias (22-06) DESBLOQUEADOS.

## Rollback
Código: `git revert 66fcf05c` + push (CI rebuilda). Imediato: redeploy imagem digest anterior `95c9aa22`.

## Desvio do plano
Plano assumia 5 commits; foram **6** (TDD `1d8825aa` faltava na lista da pesquisa). Registrado no 22-02-GATE.md.

## Artefatos
- `22-02-GATE.md` — gate 2 níveis + digests (rollback) + resultado do re-land.

## Self-Check: PASSED
