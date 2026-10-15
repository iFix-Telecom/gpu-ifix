# 23-ROLLOUT-CHANGES — o que muda em produção no rollout da Phase 23

Preparado no plano 23-15 em 2026-10-15, entre 02:20 e 02:45 BRT. Este plano **não alterou nada em produção**. Foram feitas só leituras, o snapshot, o dry-run num Postgres descartável e o build/pré-pull das imagens. A aplicação fica com o 23-16, que deve aplicar exatamente esta lista.

Nenhum token, chave ou DSN aparece neste arquivo. Os snapshots ficam fora do repositório.

---

## 0. Pré-requisitos (FATOS medidos)

### 0.1 Gates no topo de develop (`99d73b2`)

| Gate | Resultado |
|------|-----------|
| `gofmt -l gateway pkg` | vazio |
| `go build ./gateway/... ./pkg/...` | OK |
| `go test ./gateway/... ./pkg/openai/... -count=1 -race` | todos os pacotes `ok`, exit 0 |
| `go test -tags=integration ./internal/integration_test/... -count=1` (local, via sudo, `CI_ALLOW_TIGHT_SHED_TIMING=1`) | `ok ... 358.372s`, exit 0. É a suíte completa, incluindo `TestHaikuChain_*`, `TestSensitiveNeverExternalHaiku` e `Migration0041`, que o 23-14 não tinha conseguido rodar |
| `dashboard: npx vitest run` | 19 arquivos, 106 testes, todos passaram |
| `dashboard: npx tsc --noEmit` | sem erros |
| `python3 -m pytest -q ops/vast-3060/test_unified3060.py` | 134 passaram |
| CI `build-gateway` run 38687183280 | unit success, integration (testcontainers) success, **Build & push success**. `Trigger Portainer redeploy (dev)` deu failure, que é o exit 22 conhecido do webhook do stack 34 deletado |
| CI `build-dashboard` run 38687183279 | lint, type-check e vitest success, **Build & push success**. O redeploy dev falhou do mesmo jeito conhecido |

Push: `origin/develop` foi de `2886a7a..99d73b2`.

### 0.2 Imagens (digest pinado = RepoDigest/index, mesma convenção do 23-02)

| Serviço | Linha nova do stack |
|---------|---------------------|
| gateway (stack 38) | `ghcr.io/ifixtelecom/ifix-ai-gateway:develop-99d73b2@sha256:89abdfe10663307f407c14f3fd64f56de595b9fcdd347c878ebdc82dfb0cfa9b` |
| dashboard (stack 40) | `ghcr.io/ifixtelecom/ifix-ai-dashboard:develop-99d73b2@sha256:334baca631b9a704502ee25c6222ab0956b969d2567428178c904a8d6125ed89` |

O **pré-pull no worker-vm** foi feito para as duas imagens (`Status: Downloaded newer image for ...develop-99d73b2`).

O que roda hoje em prod, e é o alvo do rollback:
- gateway: `develop-2886a7a@sha256:3ee34afc6dfd19496d6dc11d2e6743f93675728dbc812dc5474577de9fbf5185` (hotfix do 23-02)
- dashboard: `develop-9101882@sha256:415e6a8a692133a0d08b2dc1954ff089617a9e36f9f131a2588e031bca475dd7`

### 0.3 Snapshot (antes de qualquer mudança)

Fica em `ops-claude:~/gw-phase23-rollout/`. O diretório tem modo 700 e todos os arquivos têm modo `-rw-------`, com timestamp `20261015T022546`.

| Arquivo | Conteúdo |
|---------|----------|
| `stack38-….json` / `stack38-file-….json` | `GET /api/stacks/38` (Env completo, **86 vars**) e `/file` |
| `stack40-….json` / `stack40-file-….json` | `GET /api/stacks/40` (Env **78 vars**) e `/file` |
| `service-gateway-….json` | `docker service inspect ai-gateway-prod_gateway` |
| `bd_ai_gateway-data-….sql` | `pg_dump --data-only -t ai_gateway.upstreams -t ai_gateway.model_aliases -t ai_gateway.tenants -t ai_gateway.prices` (pg_dump 16.15). A tabela `api_keys` **não** foi incluída (T-23-49) |
| `prod-readonly-….txt` | goose version, upstreams, contagens de fidelidade, tenants, preços e `pod_config` |
| `model_aliases-….txt` | as 28 linhas de `model_aliases` de prod |

- `goose_db_version` atual em prod: **max applied = 40**
- `pod_config`: `schedule_disabled = t`, up 8 / down 19, seg–sex, `updated_at 2026-10-14 11:15:42 UTC`

### 0.4 `migrate status` com a IMAGEM NOVA, contra prod (só leitura)

One-off no worker-vm, `docker run --rm --env-file <envs do container vivo> --entrypoint /gatewayctl …:develop-99d73b2 migrate status`:

```
… 0040_drop_audit_log_cost_brl.sql   (Wed Oct  7 23:53:15 2026)
Pending                  -- 0041_haiku_chain.sql
Pending                  -- 0042_aux_pod_lifecycles.sql
```

**Pending: 0041, 0042.** O env-file foi criado com umask 077 e apagado com `shred` no fim.

---

## 0b. Estado real de upstreams llm/tts em prod antes do migrate (4b, FATO)

```
      name       | role | tier | tier_priority | enabled
-----------------+------+------+---------------+---------
 local-llm       | llm  |    0 |             0 | f
 openrouter-chat | llm  |    1 |             0 | t
 kokoro-tts      | tts  |    0 |             0 | t
 local-tts       | tts  |    1 |             0 | t
```

Isso bate com o que a 0041 espera. No tier 1 de llm só existe o `openrouter-chat`, na prio 0, então as posições 10 e 20 estão livres e a guarda (a) não dispara. No tier 1 de tts só existe o `local-tts`, na prio 0, abaixo de 990, então a guarda (c) também não dispara. A divergência de TTS citada no adendo de 2026-09-01 é que o `local-tts` está na prio 0, e não na prio que as migrations 0024/0032 deixariam. A 0041 trata isso com o deslocamento relativo +10.

## 0c. Dry-run das migrations numa cópia de prod (4c, FATO)

**Montagem.** No worker-vm subi `postgres:16-alpine` com o nome `pg23dry` e **`--network none`**. O `SHOW server_version` deu 16.15 na cópia e 16.15 no cluster DO.

Restaurei dois dumps:
- `pg_dump --schema-only -n ai_gateway --no-owner --no-privileges`
- `--data-only` de upstreams, model_aliases, tenants, prices e goose_db_version

O `gatewayctl` da imagem nova rodou com `--network container:pg23dry`. Assim ele **não tinha rota de rede para o banco de prod**: o único destino alcançável era o loopback da cópia. O harness foi conferido antes com a imagem de prod (`migrate status` listou o histórico copiado).

### Conferências de fidelidade (iguais em prod e na cópia)

| # | Conferência | Prod | Cópia |
|---|-------------|------|-------|
| i | `SELECT extname FROM pg_extension` | `plpgsql` | `plpgsql` |
| ii | colunas em `information_schema.columns` (schema ai_gateway) | 642 | 642 |
| ii | constraints (`pg_constraint`), contagem e md5 de `conrelid::regclass:conname` | 77 / `50aad97f69d4a3d2ef928639c592e9fc` | 77 / `50aad97f…` |
| ii | triggers não-internos, contagem e md5 de `tgrelid::regclass:tgname` | 10 / `da568389c18868d4995d760550e82d0d` | 10 / `da568389…` |
| iii | `max(version_id) WHERE is_applied` | 40 | 40 |

As 28 linhas de `model_aliases` também saíram idênticas (diff vazio). O único diff entre as duas saídas é o `pod_config`, que não foi copiado de propósito.

### `migrate up` na cópia

```
OK   0041_haiku_chain.sql (55.99ms)
OK   0042_aux_pod_lifecycles.sql (35.68ms)
goose: successfully migrated database to version: 42
```

Upstreams llm/tts **depois do Up**:

```
        name         | role | tier | tier_priority | enabled
---------------------+------+------+---------------+---------
 local-llm           | llm  |    0 |             0 | f
 anthropic-haiku     | llm  |    1 |             0 | f
 openrouter-chat     | llm  |    1 |            10 | t
 openrouter-deepseek | llm  |    1 |            20 | f
 kokoro-tts          | tts  |    0 |             0 | t
 openrouter-tts      | tts  |    1 |             0 | t
 local-tts           | tts  |    1 |            10 | t
```

Também conferido na cópia:
- `aux_pod_lifecycles` criada, com UNIQUE (pod, vast_instance_id) e os 2 índices
- `prices_unit_check` com as 7 units
- colunas `tenants.llm_effort`, `billing_events.tokens_cache_read`, `tokens_cache_write` e `cost_source` presentes

### Config da seção (b) aplicada 2 vezes na cópia (prova de idempotência)

Rodei o script da seção (b), com 27 `model-alias set` e 10 `prices set`, duas vezes seguidas.
- `CFG_FAIL=0` nas duas rodadas.
- O dump de `model_aliases` e dos preços ativos depois da 1ª rodada é **byte a byte igual** ao da 2ª (`cmp` → `IDEMPOTENT`).
- Nenhum (model, provider, unit) ficou com mais de 1 linha ativa.

### `migrate down 2` na cópia

```
OK   0042_aux_pod_lifecycles.sql (14.94ms)
OK   0041_haiku_chain.sql (53.79ms)
```

Depois do Down, o SELECT de upstreams llm/tts ficou **idêntico ao 0b**, com `openrouter-chat` de volta à prio 0 e `local-tts` de volta à prio 0. A fidelidade também voltou ao ponto de partida: 642 colunas, as mesmas 77 constraints (mesmo md5), os mesmos 10 triggers (mesmo md5) e goose em 40.

O Down não remove dois resíduos, e os dois são esperados:
- as linhas `openrouter-chat` dos aliases **novos** `anthropic/claude-haiku-5.5` e `claude-haiku-5-5`, que a migration não conhece; o passo 1 do rollback as remove;
- os 4 preços input/output de `claude-haiku-5-5` e `:long`, que ficam inertes (passo 4 do rollback).

O Down apaga as units de cache e as linhas de alias de `anthropic-haiku`/`openrouter-deepseek`, como previsto.

Fim do dry-run: o container `pg23dry` foi derrubado, e os dumps e env-files em `/root/p2315` foram apagados (env-files com `shred`).

## 0d. Tenants com `provider_prefs` próprias (4d)

`SELECT slug, provider_prefs FROM ai_gateway.tenants WHERE provider_prefs IS NOT NULL` → **nenhum**, 0 linhas. Nenhum tenant tem prefs que venceriam as do alias no passo ③, então não há tratamento a propor.

## 0e. Tenants fora de 24/7 (4e)

`SELECT mode, count(*) FROM ai_gateway.tenants GROUP BY mode` → `24/7 | 26`. **Nenhum** tenant está em peak-mode, e a lista é só informativa.

## 0f. Outras leituras relevantes

- **Price-sync (timer diário de ops-claude):** `gateway-price-sync.timer` chama `ExecStart=/home/pedro/bin/gateway-price-sync.sh`. Esse arquivo é uma **cópia** de 2026-10-07 (12119 bytes), não um symlink para o checkout, e é diferente do script do 23-10 (0 ocorrências de `claude-haiku-5-5`). Logo, **a mudança do 23-10 NÃO está live**. A próxima execução é às 08:30 BRT de hoje e vai usar o script antigo, sem efeito sobre o rollout. O script novo grava units de cache, que o CHECK atual de `prices` rejeita. Por isso a instalação dele entra como item (e2), **depois** do migrate.
- **3060:** `/opt/vast-3060/unified3060.py` é a versão de 2026-10-07. Os timers `vast-unified-{start,watchdog,disk,stop}` estão `disabled` (desligados em 2026-10-14). O arquivo de segredos `/etc/onboard/secrets/vast-3060.env` tem 0 linhas `GATEWAY_ADMIN_URL`/`GATEWAY_ADMIN_KEY`.
- **3090:** `pod_config.schedule_disabled = t`, `local-llm` com `enabled = f`, e a API da Vast lista **0 instâncias** na conta (nenhuma `ifix-primary-*`).
- **Informativo:** existe um service `ai-gateway-dev_gateway` no worker-vm, com imagem `latest-dev`, **0 réplicas** e o DSN apontando para o mesmo `bd_ai_gateway`. Com 0 réplicas não interfere em nada. Se alguém escalar esse service, ele roda uma imagem antiga contra o schema 42. Não mexer.

---

## (a) Banco via migration (`gatewayctl migrate up` one-off com a imagem nova)

**0041_haiku_chain:**
- `openrouter-chat` (llm, tier 1) sai da prio 0 para a **prio 10**.
- Novo `anthropic-haiku` (llm, tier 1, prio 0, `url_env=UPSTREAM_LLM_ANTHROPIC_URL`, `auth=UPSTREAM_LLM_ANTHROPIC_API_KEYS`), **nasce enabled=false**.
- Novo `openrouter-deepseek` (llm, tier 1, prio 20, mesmo env do OpenRouter), **nasce enabled=false**.
- Os dois só são habilitados por `gatewayctl upstreams enable --name=…` **depois** de três coisas: o swap de imagem, o healthcheck da imagem nova e a gravação das linhas de alias de ① e ③ (23-16 Task 2, passo 2b).
- TTS tier 1 deslocado +10 (`local-tts` 0→10). Novo `openrouter-tts` na prio 0, **nasce enabled**, mas fica dormente porque falta `UPSTREAM_TTS_OPENROUTER_URL`. Esse env só entra no 23-17.
- `tenants.llm_effort` (TEXT NULL, CHECK off/low/medium/high/xhigh/max) e trigger `tenants_update_notify` recriado com a coluna.
- `prices_unit_check` passa a aceitar `cache_read_token`, `cache_write_token` e `cache_write_1h_token`.
- `billing_events`: `tokens_cache_read INT NOT NULL DEFAULT 0`, `tokens_cache_write INT NOT NULL DEFAULT 0`, `cost_source TEXT NULL`. O ALTER é no pai particionado e propaga para as partições.

**0042_aux_pod_lifecycles:** tabela nova `aux_pod_lifecycles` com 2 índices.

**Efeito comportamental com a imagem nova**, porque todos os 26 tenants estão com `llm_effort` NULL: requests sem `reasoning` do cliente passam a sair com **effort high**.

HIPÓTESE: o `ADD COLUMN … DEFAULT 0` em `billing_events` é só alteração de metadado, sem reescrita, porque o PG ≥ 11 usa fast default para default constante. O lock ACCESS EXCLUSIVE por partição deve durar milissegundos, mas pode esperar atrás de uma transação longa. Na cópia a migration levou 56 ms, mas a cópia não tem dados de billing. O que resolve: medir o tempo do `migrate up` real no 23-16.

## (b) Config via API oficial (`gatewayctl`, NÃO SQL)

Prefs usadas:
- `HAIKU_PREFS` = `{"order":["anthropic"],"allow_fallbacks":true,"max_price":{"prompt":0.1,"completion":0.5},"data_collection":"deny"}`, as prefs atuais de prod
- `DS_PREFS` = `{"sort":"price","preferred_min_throughput":{"p90":100},"max_price":{"prompt":0.4},"quantizations":["fp8"],"data_collection":"deny"}`, o PADRÃO DeepSeek de 2026-10-12, sem `require_parameters`

Os comandos:
- `gatewayctl model-alias set -alias <a> -upstream anthropic-haiku -target claude-haiku-5-5`
- `gatewayctl model-alias set -alias <a> -upstream openrouter-chat -target anthropic/claude-haiku-5.5 -provider-prefs "$HAIKU_PREFS"`
- `gatewayctl model-alias set -alias <a> -upstream openrouter-deepseek -target deepseek/deepseek-v4.1-flash:exacto -provider-prefs "$DS_PREFS"`

### Aliases × upstreams: ação idempotente a partir do snapshot

| Alias | `anthropic-haiku` → `claude-haiku-5-5` (dormente até o 23-18) | `openrouter-chat` → `anthropic/claude-haiku-5.5` + HAIKU_PREFS | `openrouter-deepseek` → `deepseek/deepseek-v4.1-flash:exacto` + DS_PREFS |
|-------|------|------|------|
| `qwen` | criar | sem mudança (mesmo target e prefs) | criar |
| `deepseek-flash` | criar | sem mudança | criar |
| `deepseek-v4-flash` | criar | sem mudança | criar |
| `gpt-4o` | criar | sem mudança | criar |
| `gpt-5.4-mini` | criar | sem mudança | criar |
| `gpt-5.4-mini-2026-03-17` | criar | sem mudança | criar |
| `ia-kanban` | criar | sem mudança | criar |
| `anthropic/claude-haiku-5.5` | criar | **criar** (alias novo) | criar |
| `claude-haiku-5-5` | criar | **criar** (alias novo) | criar |

Total: **20 linhas a criar**, 7 sem mudança e 0 a atualizar.

O comando `set` é UPSERT por (alias, upstream_name), e o dry-run provou que reaplicar dá o mesmo estado. Rodar as 27 linhas é seguro: nas 7 linhas "sem mudança" o target e as prefs passados são idênticos aos atuais.

**Intocados:**
- linhas `local-llm` (qwen, gpt-4o, gpt-5.4-mini, gpt-5.4-mini-2026-03-17, ia-kanban → `qwen`)
- **`maestro-summary`** (openrouter-chat → `deepseek/deepseek-v4.1-flash:nitro`, `order:["BaseTen"]`)
- `google/gemini-2.5-flash-lite`
- todas as linhas de stt e embed

**TTS:** não existe alias de TTS no banco. FATO (migration 0005 l.7): `model_aliases.upstream` tem `CHECK IN ('llm','stt','embed')`. O pin do TTS externo vem da prioridade tier-1 da 0041, com `openrouter-tts` na prio 0. Se o 23-17 quiser alias de TTS, precisa de migration que amplie o CHECK. Fica registrado como pendência do 23-17.

### Preços: `gatewayctl prices set -model <m> -provider anthropic -unit <u> -usd <v>`

| model | input_token | cache_write_token | cache_write_1h_token | cache_read_token | output_token | ação |
|-------|-------------|-------------------|----------------------|------------------|--------------|------|
| `claude-haiku-5-5` | 0.0000001 | 0.000000125 | 0.0000002 | 0.00000001 | 0.0000005 | criar (5 linhas; nenhuma em prod) |
| `claude-haiku-5-5:long` | 0.0000005 | 0.000000625 | 0.000001 | 0.00000005 | 0.0000025 | criar (5 linhas; nenhuma em prod) |

Fica sem mudança: `anthropic/claude-haiku-5.5 @ openrouter-fireworks`, input 1e-7 e output 5e-7, ativos desde 2026-10-14. `prices set` expira o preço ativo e insere o novo, então reaplicar com o mesmo valor deixa 1 linha ativa (verificado no dry-run).

**ACHADO no dry-run (FATO): perda de precisão.** `prices.unit_cost_usd` é `NUMERIC(12,8)` (migration 0012), e por isso os valores gravados ficaram assim:
- `cache_write_token` 0.000000125 → **0.00000012** (−4%)
- `:long` 0.000000625 → **0.00000063** (+0.8%)

As outras 8 units são gravadas exatas. Impacto:
- o custo de cache write do passo ① sai subestimado em 4%. O ① fica dormente até o 23-18;
- vale o mesmo para as linhas de cache que o price-sync novo gravar para o OpenRouter. No ②, o custo vem em geral do `usage.cost` (provider_reported), e a tabela só entra como fallback.

Proposta: **pendência para antes do 23-18**, com uma migration que amplie a escala para `NUMERIC(14,10)` ou mais. Isso não bloqueia este rollout e **não foi alterado aqui**.

### (b2) Habilitação de ① e ③ (23-16 Task 2, passo 2b, depois das linhas de alias)

- `gatewayctl upstreams enable --name=openrouter-deepseek` liga o ③.
- `gatewayctl upstreams enable --name=anthropic-haiku` pode ficar habilitado sem chaves. O 23-14 mostrou que ele fica dormente, com 1 WARN por upstream e Debug depois. Para manter o tráfego no ② até o 23-18, o 23-16 pode também deixá-lo disabled. As duas formas são seguras.

## (c) Stack 38 (gateway), via `PUT /api/stacks/38?endpointId=6`

- **image:** `develop-2886a7a@sha256:3ee34afc…` → `develop-99d73b2@sha256:89abdfe10663307f407c14f3fd64f56de595b9fcdd347c878ebdc82dfb0cfa9b`
- **Envs novas:** `UPSTREAM_LLM_EXTERNAL_HEADER_TIMEOUT_SECONDS=120` e `ANTHROPIC_KEY_RENEWAL_TZ=America/Sao_Paulo`.
  - FATO: o compose do stack 38 usa uma lista explícita `environment: - KEY=${KEY}`. Uma variável só no Env do Portainer **não chega ao container**. A mudança, portanto, é de **2 linhas no `environment:` do service gateway** + **2 entradas no Env**.
  - FATO (config.go l.451-452): os dois valores são iguais aos defaults do código. Setar deixa o valor explícito e editável na UI. Omitir não muda o comportamento e reduz o diff à linha `image:`.
- **Contagem de Env:** 86 antes → **88 depois**. O Env vai completo no PUT.
- **NÃO setar:** `UPSTREAM_LLM_ANTHROPIC_URL` e `UPSTREAM_LLM_ANTHROPIC_API_KEYS` (ficam para o 23-18) nem `UPSTREAM_TTS_OPENROUTER_URL` (fica para o 23-17).
- **Mudança de comportamento:** o ② deixa de usar o timeout de cabeçalho fixo de 30 s e passa a 120 s, e então cascateia para o ③ (`NewChatFallthroughTransport`).
- **Ordem obrigatória:** pré-pull, que já está feito → `migrate up` one-off com a imagem nova → PUT (`Prune:false`, `PullImage:true`) → health → aliases (b) → enable (b2).

## (d) Stack 40 (dashboard)

- **image:** `develop-9101882@sha256:415e6a8a…` → `develop-99d73b2@sha256:334baca631b9a704502ee25c6222ab0956b969d2567428178c904a8d6125ed89`
- Env: 78 vars, sem mudança, enviado completo
- O stack 40 tem uma única linha `image:`
- Traz o painel Economia (23-13) e as superfícies de prefs para `openrouter-deepseek` (23-20). Depende do gateway novo para os campos novos da economia.

## (e) ops-claude: 3060

- Backup em `/opt/vast-3060/unified3060.py.bak-phase23-<data>`, depois `sudo install -m 644 ops/vast-3060/unified3060.py /opt/vast-3060/`.
- Acrescentar `GATEWAY_ADMIN_URL=https://ai-gateway.converse-ai.app` e `GATEWAY_ADMIN_KEY=<admin key>` em `/etc/onboard/secrets/vast-3060.env`, o `SECRETS_PATH` do script (root-600). Hoje o arquivo tem 0 linhas dessas. O valor da chave **não** vai para nenhum arquivo do repo.
- Os timers continuam `disabled`. Religar a 3060 é uma decisão separada.

## (e2) ops-claude: price-sync, só **depois** do `migrate up`

- `cp /home/pedro/bin/gateway-price-sync.sh /home/pedro/bin/gateway-price-sync.sh.bak-phase23-<data>`, depois instalar `scripts/price-sync/gateway-price-sync.sh` no mesmo caminho e rodar `DRY_RUN=1`.
- Se for instalado antes da 0041, as units de cache são rejeitadas pelo CHECK antigo.

## (f) 3090: formalizar o desligamento (só conferência, sem mudança)

`pod_config.schedule_disabled = true` e 0 instâncias Vast (`ifix-primary-*` ou qualquer outra) já estavam confirmados em 2026-10-15 02:3x BRT. Basta repetir as duas leituras no 23-16 depois do swap.

## (g) Avaliação API oficial vs banco, por item

| Item | Caminho | Por quê |
|------|---------|---------|
| DDL 0041/0042 | `gatewayctl migrate up` (goose, imagem nova) | É o caminho oficial do schema, com Down testado. Não há API para DDL |
| Aliases | `gatewayctl model-alias set` (equivalente a `PUT /admin/model-aliases`) | Faz validação estrita de prefs, NOTIFY de hot-reload e UPSERT idempotente. **Nada de SQL** |
| Preços | `gatewayctl prices set` | Expira e insere na mesma transação, valida a unit. **Nada de SQL** |
| Enable ①/③ | `gatewayctl upstreams enable` (ou `POST /admin/upstreams/{name}/enabled`) | NOTIFY `upstreams_changed` e 1 audit row |
| Stacks 38/40 | API do Portainer (`PUT /api/stacks/{id}`) | Mantém a fonte do stack sincronizada, ao contrário de um `docker service update` direto |
| 3060 / price-sync | `install` + edição do arquivo de segredos | Arquivos locais de ops-claude; não existe API |
| Banco direto | **nenhum item** | Só leitura (snapshot e conferências) |

## (h) Rollback — NESTA ordem

1. **Aliases:** `gatewayctl model-alias delete -alias <a> -upstream anthropic-haiku` e `-upstream openrouter-deepseek` nos 9 aliases (18 linhas). Depois, `-upstream openrouter-chat` nos aliases novos `anthropic/claude-haiku-5.5` e `claude-haiku-5-5` (2 linhas). As 7 linhas `openrouter-chat` existentes não mudam; se mudarem, restaurar target e prefs de `~/gw-phase23-rollout/model_aliases-20261015T022546.txt`. Isso é feito **antes** de trocar a imagem, para que a imagem antiga não veja linhas de upstream que não conhece.
   - 1b. `gatewayctl upstreams disable --name=openrouter-deepseek` e `--name=anthropic-haiku`, com a imagem nova ainda de pé.
2. **Stack 38:** `ssh worker-vm docker service update --rollback ai-gateway-prod_gateway`, que volta para `develop-2886a7a@sha256:3ee34afc…`. O rollback durável é um PUT com `~/gw-phase23-rollout/stack38-file-20261015T022546.json` e o Env do `stack38-20261015T022546.json`.
3. **`migrate down 2`** one-off com a imagem **NOVA** (`develop-99d73b2`), logo depois do passo 2, porque a imagem antiga não contém os arquivos 0041/0042. O Down foi testado na cópia e devolveu o estado exato do 0b.
   - A janela em que a imagem antiga roda no schema 42 é segura por construção. Depois do 1b, o loader da imagem antiga só carrega linhas enabled (`ListEnabledUpstreams` … `WHERE enabled = TRUE`, gateway/db/queries/upstreams.sql l.1-11, igual em `2886a7a`; a referência "loader.go l.176-187" do plano não bate com o código atual). Como tier-1 llm, ele vê apenas o `openrouter-chat` (prio 10), que tem proxy. O `openrouter-tts` é pulado por falta de env. O resto da 0041/0042 é aditivo.
4. **Preços:** os preços `claude-haiku-5-5`/`:long` de input/output que sobrarem ficam inertes, porque nenhum upstream os consulta. Expirar só se o Pedro pedir. As units de cache são removidas pelo Down.
5. **Stack 40:** `docker service update --rollback ai-gateway-dashboard_dashboard` (independente), ou PUT com `stack40-file-20261015T022546.json`.
6. **ops-claude:**
   - restaurar `/opt/vast-3060/unified3060.py.bak-phase23-<data>`;
   - se o (e2) já tiver sido feito, restaurar `/home/pedro/bin/gateway-price-sync.sh.bak-phase23-<data>`;
   - remover as 2 linhas `GATEWAY_ADMIN_*` do arquivo de segredos.

Perda aceita no rollback: o Down remove as colunas de cache e `cost_source` de `billing_events`, levando o que tiver sido gravado nelas entre o apply e o rollback.

## (i) Janela de indisponibilidade

O update é stop-first. O gap de health fica em **≈60 s por stack**: o gateway no PUT do 38 e o dashboard no PUT do 40. Com o pré-pull feito, não deve se repetir o outage de 2026-08-29. Os pods 3090 e 3060 estão desligados, então não há lifecycle em provisioning para derrubar.

---

## Condições da pré-aprovação (conferidas)

| Condição | Resultado |
|----------|-----------|
| (a) dry-run da 0041/0042 na cópia passou, com as 3 conferências de fidelidade, e Up + Down 2 voltaram ao estado anterior | **OK** (seção 0c) |
| (b) nenhum tenant com provider_prefs que quebre o ③ | **OK**: nenhum tenant tem provider_prefs (0d) |
| (c) nada inesperado no estado de prod (upstreams llm/tts e o que a 0041 espera) | **OK** (0b). Achados não bloqueantes: escala NUMERIC(12,8) dos preços de cache write e o `ai-gateway-dev_gateway` com 0 réplicas, os dois registrados acima |

