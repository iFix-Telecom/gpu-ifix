---
phase: 23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
plan: 18
subsystem: gateway-llm
tags: [anthropic, haiku, creditos-max, prod]
requires: [23-16]
provides:
  - "passo ① anthropic-haiku ativo em prod com 2 contas Max (max-pedro, max-financeiro)"
key-files:
  created:
    - .planning/phases/23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max/23-MAXKEYS-EVIDENCE.md
decisions:
  - "Chaves sk-ant-usr- exigem header anthropic-workspace-id; env rotulo=<chave>#<workspace>@<dia>"
  - "Termos dos créditos confirmados pelo Pedro; chaves não serão rotacionadas"
  - "Dias de renovação: max-pedro 19, max-financeiro 13"
metrics:
  completed: 2026-10-15
  tasks: 3
---

# Phase 23 Plan 18: chaves Max e ativação do passo ① — Summary

O passo ① (Haiku 5.5 direto na Anthropic, pago com créditos das contas Max) está ativo em prod desde 2026-10-15 15:29 UTC, com ② OpenRouter Haiku e ③ DeepSeek como cascata.

| Task | Resultado |
|------|-----------|
| 1 | Pedro gerou as chaves das 2 contas Max e os workspaces; termos confirmados; dias de renovação 19 e 13 |
| 2 | Gates na API, ativação no stack 38, smokes, verificação +30 min e +2 h |
| 3 | Pedro: "approved" — Console US$ 2,26 ≈ R$ 12,72 no billing; 99,9 % do chat servido pelo ① sem erros |

## Pendências fora do plano

- Validação de 24 h a partir de 2026-10-16 02:51 BRT.
- Conta adm@ifixtelecom.com.br (US$ 5 manual) sem papel definido.
