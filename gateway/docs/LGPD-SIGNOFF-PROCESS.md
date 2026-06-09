# LGPD — Processo de Sign-off

**Last updated: 2026-05-27.**

## Purpose

Este documento define o processo de **sign-off LGPD** que precede a ativação de
tenants `data_class: sensitive` (Telefonia, Cobranças) em produção do
`ifix-ai-gateway`. O processo coordena as três pessoas responsáveis pelo
fechamento da revisão, enumera os artefatos entregues ao jurídico da Ifix,
fixa a convenção de arquivamento da carta assinada e a cadência de revisão.

A **assinatura** da carta de sign-off é um **gate externo** — o jurídico da
Ifix detém a decisão final. Este documento descreve apenas o material que o
operador do gateway entrega; ele não substitui o parecer jurídico.

## Quem assina

- **Encarregado de Dados (DPO) da Ifix** — responsável pela conformidade LGPD
  no controlador.
- **Diretor jurídico** — homologa a base legal e a postura de retenção.
- **Owner técnico do gateway (Plataforma)** — atesta os controles técnicos
  (garantia never-external, audit, retenção) descritos nos documentos abaixo.

## O que é entregue ao jurídico

O operador apresenta ao jurídico o seguinte conjunto de documentos:

- `gateway/docs/LGPD-SUBPROCESSORS.md` — lista atualizada de sub-processadores
  externos com papel e classe de dados que cada um pode receber.
- `gateway/docs/LGPD-REVIEW-CHECKLIST.md` — checklist de revisão LGPD com os
  controles técnicos exigidos antes da ativação de tenants sensíveis.
- `gateway/docs/LGPD-SIGNOFF-LETTER-TEMPLATE.md` — template da carta de
  sign-off a ser preenchido com os identificadores do tenant, classe de dados,
  base legal e período de retenção, e assinado pelos três responsáveis acima.

## Onde arquivar a carta assinada

A carta assinada é arquivada em `.planning/legal/lgpd-signoff-{YYYY-MM-DD}-{tenant}.pdf` —
binário gitignored. Apenas os templates ficam versionados no repositório; PDFs
assinados nunca entram em git (mitigação T-11-LGPD-01).

Convenção dos placeholders:
- `{YYYY-MM-DD}` — data da assinatura.
- `{tenant}` — slug do tenant ativado (por exemplo `telefonia`, `cobrancas`).

## Cadência

A revisão LGPD é renovada com a seguinte cadência:

- **Revisão anual obrigatória** — o operador reapresenta o checklist e a
  carta ao jurídico no aniversário da última assinatura.
- **Revisão event-driven** — qualquer mudança na lista de sub-processadores
  (`gateway/docs/LGPD-SUBPROCESSORS.md`), na classe de dados de um tenant
  existente, ou na ativação de um novo tenant sensível dispara uma nova rodada
  de revisão e re-assinatura, independentemente da cadência anual.

## Referências cruzadas

- `gateway/docs/LGPD-SUBPROCESSORS.md` — lista canônica de sub-processadores.
- `gateway/docs/LGPD-REVIEW-CHECKLIST.md` — checklist de controles técnicos.
- `gateway/docs/RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md` — runbook operacional
  da ativação dos tenants sensíveis (Telefonia, Cobranças) com o caminho
  RES-08 (never-external) descrito no nível operacional.
