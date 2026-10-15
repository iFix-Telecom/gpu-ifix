---
phase: 23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
plan: 15
subsystem: deploy/rollout-prep
tags: [rollout, prod, snapshot, dry-run, migrations, portainer, gatewayctl]
requires: [23-02, 23-13, 23-14, 23-20]
provides:
  - "Imagens develop-99d73b2 no GHCR e pré-puxadas no worker-vm: gateway sha256:89abdfe10663307f407c14f3fd64f56de595b9fcdd347c878ebdc82dfb0cfa9b, dashboard sha256:334baca631b9a704502ee25c6222ab0956b969d2567428178c904a8d6125ed89"
  - "Snapshot root-600 em ops-claude:~/gw-phase23-rollout/ (stacks 38/40, service inspect, data dump de upstreams/model_aliases/tenants/prices)"
  - "23-ROLLOUT-CHANGES.md aprovado (approve via pré-aprovação condicional) — insumo exato do 23-16"
affects: [23-16, 23-17, 23-18]
tech-stack:
  added: []
  patterns:
    - "Dry-run de migration numa cópia em Postgres descartável --network none, com gatewayctl em --network container:<pg> (sem rota para prod)"
    - "Fidelidade da cópia por contagem + md5 de constraints/triggers"
key-files:
  created:
    - .planning/phases/23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max/23-ROLLOUT-CHANGES.md
  modified: []
decisions:
  - "Aprovação do checkpoint Task 2 = approve, via pré-aprovação condicional do orquestrador; as 3 condições (dry-run com fidelidade, nenhum tenant com prefs, prod no estado esperado) foram conferidas"
  - "Digest pinado = RepoDigest (index), mesma convenção do 23-02"
  - "Envs novas do stack 38 exigem linha no environment: do compose além do Env; valores iguais aos defaults do código, então são opcionais"
  - "Price-sync novo só pode ser instalado depois do migrate (o CHECK antigo rejeita units de cache) — item (e2)"
metrics:
  duration: "~30 min"
  completed: 2026-10-15
  tasks: 2
  files: 1
requirements: [HAIKU-07]
---

# Phase 23 Plan 15: preparo do rollout de produção — Summary

O rollout da fase 23 está pronto para o 23-16. As duas imagens (`develop-99d73b2`) já estão no worker-vm, o snapshot foi tirado e as migrations 0041/0042 foram exercitadas numa cópia fiel de prod, com Up e Down 2 voltando ao estado exato. A lista de mudanças em `23-ROLLOUT-CHANGES.md` foi aprovada como está. Nada foi alterado em produção.

## Commits

| Task | SHA | Mensagem |
|------|-----|----------|
| 1 | 91c5739 | docs(23-15): lista de mudanças do rollout (gates, imagens, snapshot, dry-run) |
| 2 | c41ad70 | docs(23-15): registra aprovação approve (checkpoint Task 2, pré-aprovação condicional conferida) |

Push para `origin/develop`: `2886a7a..99d73b2`, que é a base da wave 6, para o CI gerar as imagens. Os commits deste plano ficam na branch da worktree.

## FATOS (fonte: 23-ROLLOUT-CHANGES.md)

- **Gates em `99d73b2`:**
  - `gofmt` vazio; build ok; `go test -race` ok
  - **integração completa local ok (358 s)**, incluindo `TestHaikuChain`, `TestSensitiveNeverExternalHaiku` e `Migration0041`, que o 23-14 não tinha conseguido rodar
  - vitest 106/106; `tsc` ok; pytest 134 ok
  - CI: Build & push success nos dois workflows
- **Prod:**
  - goose em 40, `migrate status` com a imagem nova → **Pending: 0041, 0042**
  - tier 1 de llm = só `openrouter-chat` na prio 0; tier 1 de tts = só `local-tts` na prio 0. É o estado que a 0041 espera
  - 0 tenants com `provider_prefs`; os 26 tenants estão em `24/7`
  - `schedule_disabled = t`; 0 instâncias Vast
- **Dry-run:**
  - fidelidade igual nas três conferências: `plpgsql`, 642 colunas, 77 constraints com o mesmo md5, 10 triggers com o mesmo md5, goose 40
  - Up ok (56 ms + 36 ms) com o resultado esperado
  - config da seção (b) aplicada 2× = **idempotente**
  - Down 2 devolve o SELECT de upstreams idêntico ao de antes

## Achados

1. **`prices.unit_cost_usd` é NUMERIC(12,8)** (migration 0012). `cache_write_token` 0.000000125 é gravado como 0.00000012 (−4%), e o `:long` 0.000000625 como 0.00000063. É uma pendência para antes do 23-18 (migration de escala). Não bloqueia este rollout.
2. **O price-sync live não é o do 23-10.** `/home/pedro/bin/gateway-price-sync.sh` é uma cópia de 2026-10-07, não um symlink. Ele precisa ser instalado depois do migrate, como item (e2).
3. **O compose do stack 38 lista as envs explicitamente.** Por isso uma env nova exige uma linha em `environment:` e uma entrada no Env.
4. **`ai-gateway-dev_gateway`** (`latest-dev`, 0 réplicas) aponta para `bd_ai_gateway`. É só informativo.
5. A referência "loader.go l.176-187" do plano não bate com o código atual. O fato que ela queria sustentar (o loader só carrega linhas enabled) vem de `ListEnabledUpstreams … WHERE enabled = TRUE` (db/queries/upstreams.sql), igual em `2886a7a`. A referência foi corrigida no arquivo de mudanças.

## Deviations from Plan

**1. [Rule 2] Config da seção (b) aplicada também na cópia.** O plano pedia só migrate up e down na cópia. Rodei também os 27 `model-alias set` e os 10 `prices set`, duas vezes, para provar que a sintaxe funciona e que a aplicação é idempotente. Foi assim que apareceu o achado 1. Tudo rodou só na cópia, sem rota de rede para prod.

**2. Checkpoint Task 2 resolvido sem retorno ao orquestrador.** A pré-aprovação condicional do prompt valia, porque as três condições foram conferidas. A resposta foi registrada como `approve` com timestamp. O Pedro não respondeu diretamente a este agente.

**3. node_modules do dashboard.** A worktree não tinha `node_modules`. Criei um symlink temporário para o do checkout principal, que tem o mesmo `package.json` e `package-lock.json` (conferido com `cmp`), e apaguei o symlink no fim.

## Known Stubs

Nenhum.

## Threat Flags

Nenhuma superfície nova.
- T-23-49: snapshot em diretório 700 com arquivos 600, sem `api_keys`.
- T-23-50: o grep negativo de segredos passa.
- T-23-73: dry-run up + down 2 feito.
- T-23-74: 0 tenants com prefs.
- T-23-51: digests registrados e pré-puxados.

## Self-Check: PASSED

- FOUND: 23-ROLLOUT-CHANGES.md (verify automatizado das Tasks 1 e 2 → VERIFY_OK)
- FOUND: commits 91c5739, c41ad70
- FOUND: `~/gw-phase23-rollout/` com 8 arquivos `-rw-------`
