# LGPD — Checklist de Revisão (ativação de tenants sensíveis)

**Last updated: 2026-05-14.**

## Purpose

Este checklist é trabalhado pelo operador e apresentado ao setor jurídico da
Ifix antes da ativação em produção dos tenants `data_class: sensitive`
(Telefonia, Cobranças). O **checklist é o artefato** que o gpu-ifix entrega; a
**assinatura** (sign-off) é um **gate externo** — obtida do jurídico da Ifix e
capturada como um checkpoint **bloqueante** no plano Phase-9 HUMAN-UAT
(`09-HUMAN-UAT.md`).

Tenants sensíveis **não podem** ser declarados prontos para produção até que o
checklist abaixo esteja completo e a tabela de Sign-off esteja assinada pelo
revisor jurídico.

## Checklist

Trabalhe cada item antes de levar este documento ao jurídico da Ifix:

- [ ] `gateway/docs/LGPD-SUBPROCESSORS.md` revisado e atualizado — os três
      sub-processadores externos (**OpenAI**, **OpenRouter**, **Vast.ai**) estão
      divulgados, cada um com seu papel e a classe de dados que pode receber.
- [ ] A topologia de upstreams do gateway foi conferida contra
      `LGPD-SUBPROCESSORS.md` — não há sub-processador recebendo dados que não
      esteja divulgado (nenhum upstream novo desde a última revisão).
- [ ] Base legal para o tratamento de **áudio de ligações** (Telefonia) e de
      **dados financeiros/de cobrança** (Cobranças) está documentada.
- [ ] A garantia **never-external** para tenants sensíveis foi verificada — o
      smoke `scripts/integration-smoke/smoke-sensitive-failover.py` rodou contra
      uma chave de tenant sensível e passou (`exit 0`, `gates.all_passed == true`).
- [ ] A política de privacidade publicada dos apps afetados (Telefonia,
      Cobranças, Campanhas, voice-api) divulga OpenAI, OpenRouter e Vast.ai como
      sub-processadores.
- [ ] A postura de retenção de dados do `audit_log` foi confirmada, e foi
      confirmado que `audit_log_content` **não** armazena conteúdo de tenants
      sensíveis (D-B2 — conteúdo sensível nunca é persistido).
- [ ] O procedimento de rollback dos 2 apps sensíveis (Telefonia, Cobranças)
      foi **drilado** (timed, <5 min por app) contra
      `gateway/docs/RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md`.
- [ ] Este checklist completo foi apresentado ao setor jurídico da Ifix e a
      tabela de Sign-off abaixo está assinada.

## Sign-off

A ser preenchida pelo revisor jurídico da Ifix. O checklist assinado é a
**evidência atribuível** de que a revisão LGPD ocorreu antes da ativação dos
tenants sensíveis.

| Reviewer | Role | Date | Signature / approval reference | Notes |
|----------|------|------|--------------------------------|-------|
|          |      |      |                                |       |

Este checklist assinado é a evidência anexada no Phase-9 HUMAN-UAT
(`09-HUMAN-UAT.md`) — onde o sign-off jurídico é um checkpoint **bloqueante** —
e, conforme o ROADMAP, é carregado para a Fase 10 / PRD-05 (evidência de
revisão LGPD antes de ativar tenants sensíveis).

## Related Docs

- `gateway/docs/LGPD-SUBPROCESSORS.md` — o documento de divulgação de
  sub-processadores (OpenAI, OpenRouter, Vast.ai) que o primeiro item deste
  checklist verifica.
- `gateway/docs/RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md` — o runbook de
  integração dos tenants sensíveis; o procedimento de rollback drilado é um
  item deste checklist.
- `.planning/phases/09-client-integration-sensitive-tenants-telefonia-cobran-as-campanhas-voice-api/09-HUMAN-UAT.md`
  — o plano de UAT que captura o sign-off jurídico como checkpoint bloqueante e
  anexa este checklist assinado como evidência.
