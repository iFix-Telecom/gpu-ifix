---
phase: quick-260923-sho
plan: 01
status: complete
subsystem: planning/seeds
tags: [seed-009, voz-realtime, openai-realtime, sip, poc, docs-only]
requires: []
provides:
  - SEED-009 em poc-open com preços 2026-09-23 e decisão D-01
  - POC-PLAN.md da PoC OpenAI gpt-realtime-2.1-mini via SIP nativo
affects: [SEED-009]
key-files:
  created:
    - .planning/quick/260923-sho-poc-voz-realtime-openai-sip/POC-PLAN.md
    - .planning/quick/260923-sho-poc-voz-realtime-openai-sip/260923-sho-SUMMARY.md
  modified:
    - .planning/seeds/SEED-009-realtime-voice-ai-phone-call.md
decisions:
  - "PoC de voz realtime começa por OpenAI gpt-realtime-2.1-mini via SIP nativo; Gemini Live é a etapa 2; ElevenLabs só se nenhum passar no teste de ouvido (D-01, Pedro 2026-09-23)"
  - "Toda ação externa/prod da PoC exige GATE: APROVAÇÃO PEDRO (D-05)"
completed: 2026-09-23
---

# Quick 260923-sho: abertura documental da PoC de voz realtime OpenAI SIP — Summary

SEED-009 passou para `poc-open` com a decisão do Pedro (OpenAI `gpt-realtime-2.1-mini` via SIP nativo primeiro), tabela de preços oficiais de 2026-09-23 com fonte por linha e estimativas de custo rotuladas `HIPÓTESE:`; foi criado um POC-PLAN.md de 261 linhas com 11 etapas (0–10), 8 gates `GATE: APROVAÇÃO PEDRO`, métricas, critérios numéricos propostos, rollback, riscos e separação FATOS / HIPÓTESES / NÃO SEI.

## Tarefas

| Task | Descrição | Resultado |
|------|-----------|-----------|
| 1 | Atualizar SEED-009 (status, seção Status 2026-09-23, preços 2026-09-23, custo/min como HIPÓTESE, tabela antiga marcada DESATUALIZADA, nota nos pré-requisitos) | verify automatizado: OK |
| 2 | Criar POC-PLAN.md (10 seções, na ordem do plano) | verify automatizado: OK (261 linhas, 9 ocorrências de "APROVAÇÃO PEDRO") |

Sem commits: o orquestrador faz o commit.

## Arquivos alterados

- `.planning/seeds/SEED-009-realtime-voice-ai-phone-call.md` (modificado; todo o conteúdo original preservado)
- `.planning/quick/260923-sho-poc-voz-realtime-openai-sip/POC-PLAN.md` (novo)
- `.planning/quick/260923-sho-poc-voz-realtime-openai-sip/260923-sho-SUMMARY.md` (novo)

## Desvios do plano

Nenhum desvio de escopo. Observações:

- Os valores de custo/min de D-03 (mini ~$0,018, full ~$0,058, Gemini ~$0,014) **não saem de forma direta** da razão de ~600/~1200 tok/min aplicada aos preços de D-02 (ex.: mini com o minuto inteiro de entrada e o inteiro de saída daria ~$0,030); a diferença depende da fração do minuto em que cada lado fala, que não foi medida. Os números foram mantidos como em D-03, rotulados `HIPÓTESE:`, com nota explícita de que a derivação não está confirmada.
- Leitura extra (só leitura) de `voip-api/docker/asterisk/rtp.conf` e dos transports de `pjsip.conf.template`: incluídos no POC-PLAN como FATOS (RTP 30000–30100 com ICE; portas 5070/5071/5061).
- O host `sip.api.openai.com` no diagrama veio do plano (key link/verify), não foi conferido por mim no guia oficial; o POC-PLAN registra formato de URI, transporte, porta e codecs como "NÃO SEI", a confirmar no pré-voo (etapa 0).
- A afirmação "Gemini Live é só WebSocket, sem SIP" tem como fonte a decisão D-02 (levantamento de 2026-09-23), e não uma URL própria; está atribuída assim nos dois documentos.

## Guardrails respeitados

Nenhum SSH, nenhuma chamada de API externa, nenhuma alteração em Asterisk, stack 36, trunk 8880, OpenAI, Traefik ou DNS. STATE.md não foi tocado. Nenhuma credencial foi escrita nos documentos.

## Self-Check: PASSED

- FOUND: .planning/seeds/SEED-009-realtime-voice-ai-phone-call.md (`status: poc-open`)
- FOUND: .planning/quick/260923-sho-poc-voz-realtime-openai-sip/POC-PLAN.md
- Os dois verifies automatizados do plano retornaram OK.
