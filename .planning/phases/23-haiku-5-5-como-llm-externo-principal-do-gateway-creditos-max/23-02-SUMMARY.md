---
phase: 23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
plan: 02
subsystem: gateway/deploy
tags: [hotfix, prefill, anthropic, haiku-5.5, prod, portainer, stack-38]
status: aguardando recontagem ≥ 2 h
requires:
  - "23-01: PASS 5 (normalizeAnthropicChatBody) no BuildOpenRouterDirector"
provides:
  - "Gateway prod (stack 38, worker-vm) em ghcr.io/ifixtelecom/ifix-ai-gateway:develop-2886a7a@sha256:3ee34afc6dfd19496d6dc11d2e6743f93675728dbc812dc5474577de9fbf5185"
affects: [23-08, 23-09]
tech-stack:
  added: []
  patterns: ["deploy por PUT Portainer trocando só a linha image:, com guarda cmp contra o snapshot e Env completo"]
key-files:
  created:
    - .planning/phases/23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max/23-02-HOTFIX-EVIDENCE.md
  modified: []
decisions:
  - "Sem branch hotfix nem cherry-pick: o diff f06681f..2886a7a em gateway/ pkg/ go.mod go.sum tem só os 4 arquivos do 23-01; S = 2886a7a foi direto para develop (FF de dab16b8)"
  - "Digest pinado = RepoDigest (index), mesma convenção da imagem anterior de prod"
  - "Aprovação do checkpoint Task 2 = deploy-now, repassada pelo orquestrador (pré-aprovação do Pedro: 'execute, seja autônomo')"
metrics:
  duration: "~50 min"
  completed: 2026-10-15
requirements: [HAIKU-07]
---

# Phase 23 Plan 02: hotfix de prefill em produção — Summary

A correção de prefill e sampling do 23-01 (PASS 5) está em produção sozinha, sem outra mudança de código e sem migration. O stack 38 agora roda `develop-2886a7a@sha256:3ee34afc…`. O smoke com conversa terminando em assistant e alvo `anthropic/claude-haiku-5.5` retorna 200, e não houve nenhum 400 de prefill desde o deploy. A recontagem com janela de ≥ 2 h fica pendente.

## Commits

| Task | SHA | Mensagem |
|------|-----|----------|
| 1 | b94c31a | docs(23-02): evidência pré-deploy do hotfix de prefill (snapshot, isolamento, digest, baseline) |
| 2 | 8f0e263 | docs(23-02): registra aprovação deploy-now do hotfix (checkpoint Task 2) |
| 3 | 34b1e38 | docs(23-02): hotfix de prefill em prod (stack 38 → develop-2886a7a), smoke 200 |

Push para `origin/develop`: `dab16b8..2886a7a`, só a base com o 23-01 e docs. Os commits deste plano ficam na branch da worktree.

## FATOS (fonte: 23-02-HOTFIX-EVIDENCE.md)

- **Antes:** a imagem de prod era `develop-f06681f@sha256:3cc6fa18…`. Stack 38 com 86 envs. Snapshot root-600 em `ops-claude:~/gw-phase23-hotfix/`.
- **Isolamento:** `git diff --stat f06681f..2886a7a -- gateway/ pkg/ go.mod go.sum` mostra 4 arquivos, todos do 23-01, com 326 inserções.
- **Baseline:** 37 erros 400 `assistant message prefill` nas 24 h anteriores, que são 100% dos 400 da janela.
- **CI:** run 38677210461. Depois de re-rodar o flake `TestChatProxy_SSEStreamingFlushesPerChunk`, os jobs unit, integration e Build & push deram success.
- **`migrate status`** com a imagem nova: última aplicada 0040, Pending 0.
- **Deploy:** 2026-10-15 00:53:58 BRT. O diff do StackFileContent foi de uma linha (`image:` do gateway). Env 86 antes e 86 depois.
- **Pós-deploy:**
  - imagem do service = digest do evidence
  - log de boot com `version=develop-2886a7a`, 0 panic/fatal/ERROR
  - health 200
  - smoke de prefill com 200 nos dois casos: `deepseek-flash` e o alias do Maestro `gpt-5.4-mini-2026-03-17`, ambos resolvendo para `anthropic/claude-haiku-5.5`
  - audit desde o deploy: 18 × 200 e 0 erros 400 de prefill

## PENDENTE: recontagem ≥ 2 h

O critério "contagem de 400 de prefill em janela ≥ 2 h = 0" ainda não fecha, porque a execução terminou cerca de 2 min depois do deploy. Para retomar: rodar só a query da seção 4 do evidence com `a.ts >= '2026-10-15 03:53:58+00'` e anexar o resultado na seção 8. Não repetir deploy nem smoke.

Observação: os 37 casos do baseline foram todos em horário comercial (06:36 a 20:31 BRT). Por isso a recontagem só tem valor se for feita depois das ~12:00 BRT de 2026-10-15.

## Deviations from Plan

**1. [Rule 3 - Bloqueio] Integração local não ficou verde de uma vez por causa da carga do host**
- **Encontrado na:** Task 1, passo 3
- **Problema:** a 1ª rodada estourou o timeout padrão de 10 min. A 2ª teve 12 FAILs, com o host em load 16-20 por causa de outros executores rodando testcontainers em paralelo.
- **Tratamento:** classifiquei os FAILs antes de liberar o push.
  - (a) Os mesmos testes no código de prod f06681f, via `git archive`: 11/12 PASS. `TestSC3_HotReloadAppliesInUnder2Seconds` falha também em prod.
  - (b) Em S, os 4 testes de upstream isolados: 4/4 PASS. Eram cascata de estado deixado pelos testes de shed.
  - (c) Em S, os testes de shed que falharam, isolados: 3/3 PASS.
  - O gate final foi a integração completa no CI, que deu success.
- **Arquivos:** nenhum. O código não foi alterado.

**2. [Rule 3 - Bloqueio] Flake do CI no job unit**
- `TestChatProxy_SSEStreamingFlushesPerChunk` falhou no runner. O teste usa `NewChatProxy` (passthrough local), não o director, e localmente passou 5/5. Re-run dos jobs que falharam deu success.

**3. Smoke adicional (fora do plano):** além do `deepseek-flash` pedido no plano, rodei o smoke também com o alias `gpt-5.4-mini-2026-03-17`, que é o que o Maestro/copilot usa no chat-ifix. É só leitura e não muda nada.

## Observações de processo

- O scratchpad da sessão é compartilhado com outros executores. O `integ.sh` foi sobrescrito pelo executor do 23-04 no meio da execução. Os scripts deste plano passaram a usar o prefixo `p2302-`.
- O hook `clickup-link-enforce.sh` avisou que não há tarefa ClickUp vinculada no worktree. O vínculo fica com o orquestrador.

## Threat Flags

Nenhuma superfície nova.
- T-23-04: o evidence passa no grep sem `ptr_`, `ifix_admin_`, `sk-`, `ifix_sk_`, e o snapshot fica em diretório 700 com arquivos 600.
- T-23-05: pré-pull antes do PUT, Env completo (86 = 86), rollback documentado.
- T-23-06: diff limitado aos 4 arquivos e migrate Pending 0.

## Self-Check: PASSED

- 23-02-HOTFIX-EVIDENCE.md existe, contém `sha256:` e não contém segredos.
- Commits b94c31a, 8f0e263 e 34b1e38 estão presentes em `git log`.
- O service em prod está com o digest `sha256:3ee34afc…` e o health responde 200. A verificação automatizada da Task 3 foi rodada.
