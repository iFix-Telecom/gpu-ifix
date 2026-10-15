---
phase: 23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
plan: 16
subsystem: deploy/rollout-prod
tags: [rollout, prod, migrate, portainer, gatewayctl, haiku, openrouter, deepseek, billing]
requires: [23-15]
provides:
  - "bd_ai_gateway em goose 42 (0041_haiku_chain, 0042_aux_pod_lifecycles)"
  - "gateway e dashboard develop-99d73b2 em prod (stacks 38/40, digests pinados)"
  - "cadeia ①(dormente)→②→③ configurada via gatewayctl: 20 linhas de alias novas, anthropic-haiku e openrouter-deepseek enabled"
  - "10 preços anthropic claude-haiku-5-5 / :long"
  - "3060: unified3060.py com record_aux_lifecycle + GATEWAY_ADMIN_* nos segredos; price-sync novo instalado"
  - "3090 desligada formalmente (schedule_disabled=t, 0 instâncias Vast)"
affects: [23-17, 23-18]
tech-stack:
  added: []
  patterns:
    - "Prova do passo ③ por alias canário pinado num único upstream (sem force-open global de breaker)"
    - "Ordem alias → upstreams enable para o ③ não entrar no roster sem alias"
key-files:
  created:
    - .planning/phases/23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max/23-ROLLOUT-EVIDENCE.md
  modified: []
decisions:
  - "Envs novas do stack 38 aplicadas (Env 86→88 + 2 linhas no environment:), conforme a lista aprovada"
  - "unified3060.py instalado com modo 0644 (o do arquivo original e da lista aprovada), não 0755 como dizia o plano"
  - "anthropic-haiku habilitado conforme o plano; fica dormente sem envs até o 23-18"
  - "Achado effort high × max_tokens baixo NÃO corrigido (fora da lista aprovada); levado ao Pedro"
metrics:
  duration: "~35 min de execução + ~30 min de observação"
  completed: 2026-10-15
  tasks: 3
  files: 1
requirements: [HAIKU-07, HAIKU-03, HAIKU-04, HAIKU-05, HAIKU-06, HAIKU-09]
---

# Phase 23 Plan 16: rollout de produção — Summary

A fase 23 está em produção. O banco foi migrado para a versão 42, o gateway e o dashboard rodam `develop-99d73b2` e a cadeia Haiku ②→③ foi configurada só via `gatewayctl`, com ① habilitado mas dormente sem chaves. Os smokes, o canário do ③, o 503 para o tenant sensível, o billing `provider_reported` com cache e a 3090 desligada foram validados ao vivo. A confirmação de 24 h está **pendente**: abaixo está a recontagem que precisa ser feita.

## Commits

| Task | SHA | Mensagem |
|------|-----|----------|
| 1 | 28dedc2 | docs(23-16): evidência Task 1 do rollout (migrate 42, stacks 38/40, 3060, price-sync) |
| 2 | fbb22c1 | docs(23-16): evidência Task 2 (aliases, enable 1/3, preços, smokes, canário, 3090) |
| 3 | 434b43a | docs(23-16): validação imediata (~30 min) e checkpoint Task 3 aguardando 24 h |

As mudanças de prod não geram commit de código. O que existe é o artefato de evidência, `23-ROLLOUT-EVIDENCE.md`.

## FATOS (fonte: 23-ROLLOUT-EVIDENCE.md)

- **Migrate.** `migrate up` one-off com a imagem nova: 0041 levou 533 ms e 0042 419 ms, e o goose está em **42** sem Pending. O env-file foi apagado com `shred`.
- **Stack 38.** PUT às 05:50:37 UTC. O `/health` voltou 200 às 05:50:58, um gap de cerca de 15 s. A task roda `develop-99d73b2@sha256:89abdfe1…` e o Env foi de **86 para 88**. O log de boot tem `anthropic-haiku dormant (no keys)` e não tem o WARN de build do proxy `openrouter-deepseek`.
- **Stack 40.** O dashboard roda `develop-99d73b2@sha256:334baca6…`, com Env de 78 vars sem mudança, e responde 307.
- **Aliases.** 27 `model-alias set` com `CFG_FAIL=0`. O diff contra o snapshot dá **+20 / −0 / ~0**, e `maestro-summary` está idêntico.
- **Enable.** As linhas de alias terminaram às 05:57:05, e os enables foram depois: `openrouter-deepseek` às 05:57:26 e `anthropic-haiku` às 05:57:29.
- **Preços.** 10 `prices set` com `PRICE_FAIL=0`. A precisão de `cache_write` foi reduzida, como já se previa.
- **Smokes:**
  - 9/9 aliases dão 200 via `openrouter-chat`, sem nenhum 400 de prefill ou sampling;
  - stream termina em `[DONE]`;
  - `tool_choice` forçado devolve `tool_calls`;
  - o cache funciona: 2º request com `tokens_cache_read=19021` e `provider_reported`.
- **Canário do ③.** O `p23-canary-step3` respondeu 200 com `audit_log.upstream=openrouter-deepseek` e billing `provider_reported`, e depois foi removido. **Nenhum force-open de breaker foi executado.**
- **RES-08.** `telefonia` recebeu 503 `upstream_unavailable_for_sensitive_tenant`.
- **3090.** `schedule_disabled=t` e a API da Vast mostra `instances_found: 0`.
- **`/admin/economy`.** Responde 200 com os campos novos (`custo_creditos_haiku_brl`, `custo_externo_caixa_brl`, `pct_servido_local_tokens`, `vast_3060_brl`).
- **Validação de ~31 min:**
  - 0 respostas 400;
  - 0 chats 200 sem billing;
  - nenhum 5xx novo. Os `local-embed` 502 já existiam: foram 66 nas 6 h anteriores;
  - tráfego real de cliente (`analise-transcr-voip`) passou por `openrouter-chat` com `cache_read`.

## Achados (para decisão do Pedro)

1. **O effort high do ② trunca respostas com `max_tokens` baixo.** Esse é o achado mais relevante.
   - FATO medido: com `max_tokens` 300–516, 2 de 3 requests saíram com `finish=length` e conteúdo cortado. Com 40, o conteúdo veio vazio. Com `reasoning:{"enabled":false}`, a resposta terminou em `stop`.
   - Causa no código: o PASS 6 do `openrouter_director` injeta `reasoning.effort=high` sem headroom no `max_tokens`, enquanto o ① soma 4096.
   - Exposição em 3 dias: `campanhas` com 198 requests (`max_tokens` 487–541), chat-ifix com 51 requests abaixo de 2000, e alguns converseai-*.
   - HIPÓTESE: no campanhas, a validação Zod do JSON cortado cai no fallback do antiban.
   - Mitigação sem deploy: `gatewayctl tenant set-llm-effort --slug campanhas --effort low` (ou `off`). A alternativa em código é somar headroom no ②.
   - **Não apliquei nada**: fica fora da lista aprovada.
2. **`prices.unit_cost_usd` NUMERIC(12,8)**, confirmado em prod: precisa de uma migration de escala antes do 23-18.
3. **Ruído de WARN `upstream url env var missing; row skipped`**: 2 por minuto (`anthropic-haiku` e `openrouter-tts`). Não afeta o tráfego. Some no 23-17/23-18, ou desabilitando `anthropic-haiku` até lá.
4. O DeepSeek do canário respondeu `"/no_think\n\nOk."`. O gateway não injeta isso (FATO, via grep). HIPÓTESE: é eco do modelo ou do provider Morph.

## Aguardando confirmação de 24 h

A partir de **2026-10-16 02:51 BRT**, rodar a consulta só de leitura que está em `23-ROLLOUT-EVIDENCE.md`, seção "Checkpoint — Task 3", bloco `bash`. Ela mostra:
- 400 de prefill/sampling, com 0 esperado;
- 4xx/5xx por upstream;
- % do chat servido pelo openrouter-deepseek;
- `price_table` no openrouter, com ≈ 0 esperado;
- `finish_reason=length` por tenant, que mede o achado 1.

O Pedro também confere `/economia` contra `openrouter.ai/activity` e confirma que chat-ifix e Maestro não tiveram erro novo. O force-open global continua opcional e **só com autorização escrita**.

## Deviations from Plan

**1. [Rule 3, ajuste trivial] Modo do `unified3060.py`.** O plano dizia `install -m 0755`, e a lista aprovada e o arquivo original usam 0644. Mantive 0644: o script é chamado via `python3`.

**2. Item (e2), price-sync, executado na Task 1.** Estava na lista aprovada do 23-15, mas não no texto da Task 1. Fiz depois do migrate, como manda a lista: backup, install e `DRY_RUN=1` OK. A primeira execução real será no timer das 08:30 BRT.

**3. Smokes extras.** Além do previsto, repeti os 9 aliases com `max_tokens` 2000, porque com 40 o conteúdo vinha vazio (achado 1). Também fiz o par de requests com cache para provar `tokens_cache_read` em prod. Todos foram com a chave de teste `uat10-test`.

**4. Task 3 (checkpoint de 24 h) não aguardada.** Foi por instrução do orquestrador. A validação imediata está registrada, e o status é "aguardando".

## Rollback

Não foi acionado. Nenhuma condição de falha ocorreu: migrate OK, `/health` 200, smokes 200, sem 5xx novo, billing presente.

## Known Stubs

Nenhum.

## Threat Flags

Nenhuma superfície nova.
- T-23-53: as chaves dos smokes não estão no evidence (grep negativo OK).
- T-23-56: `GATEWAY_ADMIN_KEY` está no arquivo root-600, sem eco.
- T-23-74: não houve force-open global.
- T-23-55: aliases editados via gatewayctl, com diff conferido contra o snapshot.

## Self-Check: PASSED

- FOUND: `23-ROLLOUT-EVIDENCE.md`. Os verifies automatizados das Tasks 1, 2 e 3 passaram (`/health` 200 + `0042`, `provider_reported`, `upstream_unavailable_for_sensitive_tenant`, `openrouter-deepseek`, nenhum segredo, cabeçalho do checkpoint).
- FOUND: commits 28dedc2, fbb22c1, 434b43a
- FOUND: `/opt/vast-3060/unified3060.py` com `record_aux_lifecycle` (4)
