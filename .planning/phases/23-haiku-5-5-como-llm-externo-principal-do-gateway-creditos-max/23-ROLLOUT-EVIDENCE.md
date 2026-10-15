# 23-ROLLOUT-EVIDENCE — aplicação do rollout da Phase 23 em produção (plano 23-16)

Executado em 2026-10-15, a partir de 02:49 BRT (05:49 UTC), aplicando a lista aprovada em `23-ROLLOUT-CHANGES.md`.

Nenhum token, chave ou DSN aparece neste arquivo. As chaves de tenant dos smokes foram lidas do arquivo local de credenciais e não foram gravadas aqui.

Horários em UTC, salvo indicação.

---

## Task 1 — migrate, stacks 38/40, 3060 e price-sync

### 1.0 Conferências antes de começar

- Snapshot do 23-15 presente em `ops-claude:~/gw-phase23-rollout/` (diretório 700, arquivos 600, timestamp `20261015T022546`).
- Snapshot adicional desta execução, tirado antes de qualquer mudança: `admin-model-aliases-pre-20261015T024935.json` (`GET /admin/model-aliases`, **28 linhas**) e `admin-upstreams-pre-20261015T024935.json` (`GET /admin/upstreams`). Os dois estão em modo 600.
- Imagens pré-puxadas no worker-vm (`docker images --digests`):
  - `ifix-ai-gateway:develop-99d73b2` `sha256:89abdfe10663307f407c14f3fd64f56de595b9fcdd347c878ebdc82dfb0cfa9b`
  - `ifix-ai-dashboard:develop-99d73b2` `sha256:334baca631b9a704502ee25c6222ab0956b969d2567428178c904a8d6125ed89`
- O stack 38 (Env 86 vars, compose) e o stack 40 (Env 78 vars, compose) estavam **idênticos ao snapshot** do 23-15. A conferência foi feita por comparação do JSON de `GET /api/stacks/{id}` e `/file`.

### 1.1 `migrate up` (one-off, imagem NOVA)

O env-file foi derivado do container vivo (89 linhas), criado com umask 077 em `/root/p2316/` e apagado com `shred -u` ao fim de cada execução.

`migrate status`, antes, às 05:49:53:

```
… 0040_drop_audit_log_cost_brl.sql   (Wed Oct  7 23:53:15 2026)
Pending                  -- 0041_haiku_chain.sql
Pending                  -- 0042_aux_pod_lifecycles.sql
```

`migrate up`, entre 05:49:56 e 05:50:00:

```
OK   0041_haiku_chain.sql (532.94ms)
OK   0042_aux_pod_lifecycles.sql (418.72ms)
goose: successfully migrated database to version: 42
```

`migrate status`, depois:

```
Wed Oct  7 23:53:15 2026 -- 0040_drop_audit_log_cost_brl.sql
Thu Oct 15 05:49:59 2026 -- 0041_haiku_chain.sql
Thu Oct 15 05:49:59 2026 -- 0042_aux_pod_lifecycles.sql
```

**`bd_ai_gateway` em goose version 42, sem Pending.** A 0041 levou 533 ms em prod, contra 56 ms na cópia sem dados de billing. Isso responde a HIPÓTESE da seção (a) do 23-ROLLOUT-CHANGES: o lock das partições de `billing_events` não esperou atrás de transação longa. O gateway antigo seguiu respondendo `/health` 200 logo depois.

### 1.2 Stack 38 (gateway): `PUT /api/stacks/38?endpointId=6`

- `image:` passou de `develop-2886a7a@sha256:3ee34afc…` para `develop-99d73b2@sha256:89abdfe10663307f407c14f3fd64f56de595b9fcdd347c878ebdc82dfb0cfa9b`.
- Entraram 2 linhas no `environment:` do service gateway, logo após `UPSTREAM_LLM_OPENROUTER_ALLOW_FALLBACKS`:
  - `UPSTREAM_LLM_EXTERNAL_HEADER_TIMEOUT_SECONDS=${…}`
  - `ANTHROPIC_KEY_RENEWAL_TZ=${…}`
- No Env entraram `UPSTREAM_LLM_EXTERNAL_HEADER_TIMEOUT_SECONDS=120` e `ANTHROPIC_KEY_RENEWAL_TZ=America/Sao_Paulo`. **A contagem foi de 86 para 88**, e o Env foi enviado completo. A resposta do PUT também mostra 88.
- O PUT foi às 05:50:37 com `Prune:false, PullImage:true` e respondeu HTTP 200.
- O `/health` público, sondado a cada 5 s, deu 502 às 05:50:43, 404 às 05:50:48 e 05:50:53, e **200 às 05:50:58**. O gap foi de cerca de 15 s.
- `docker service ps ai-gateway-prod_gateway` mostrou a task `Running` em `ghcr.io/ifixtelecom/ifix-ai-gateway:develop-99d73b2@sha256:89abdfe10663307f407c14f3fd64f56de595b9fcdd347c878ebdc82dfb0cfa9b` e a anterior (`develop-2886a7a`) em `Shutdown`.
- Log de boot, com `version=develop-99d73b2`:
  - `anthropic-haiku dormant (no keys)` com `reason="UPSTREAM_LLM_ANTHROPIC_URL vazio"`
  - `upstream url env var missing; row skipped` para `openrouter-tts` (esperado; o env entra no 23-17)
  - `model aliases refreshed count=28`, `upstreams refreshed rows=11`, `tenants refreshed rows=26`, `prices refreshed rows=40`
  - `pod_config refreshed schedule_disabled=true`, `acquired leadership`, `acquired primary leadership`, `fsm_state=asleep`, `http listening`
  - nenhum panic, fatal ou ERROR
- **Proxy `openrouter-deepseek` registrado no boot.** FATO: `gateway/cmd/gateway/main.go` l.797-816 monta o proxy do ③ a partir do config, sem depender do banco, e só registra log quando falha (`log.Warn("build openrouter-deepseek proxy", …)`). Essa linha de WARN **não aparece** no log de boot, então o build do proxy não falhou.
- `gatewayctl upstreams list`, ao fim da Task 1:

```
anthropic-haiku      llm     1     false    UPSTREAM_LLM_ANTHROPIC_URL    …
openrouter-chat      llm     1     true     UPSTREAM_LLM_OPENROUTER_URL   …
openrouter-deepseek  llm     1     false    UPSTREAM_LLM_OPENROUTER_URL   …
openrouter-tts       tts     1     true     UPSTREAM_TTS_OPENROUTER_URL   …
local-tts            tts     1     true     UPSTREAM_TTS_URL              …
```

  **`anthropic-haiku` e `openrouter-deepseek` estão com enabled=false.** Eles só são habilitados no passo 2b da Task 2.
- Smoke rápido depois do swap: o alias `qwen` com tenant normal deu 200, com `"model":"anthropic/claude-haiku-5.5","provider":"Anthropic"`.

### 1.3 Stack 40 (dashboard): `PUT /api/stacks/40?endpointId=6`

- `image:` passou de `develop-9101882@sha256:415e6a8a…` para `develop-99d73b2@sha256:334baca631b9a704502ee25c6222ab0956b969d2567428178c904a8d6125ed89`.
- O Env de 78 vars não mudou e foi enviado completo.
- O PUT foi às 05:53:42 e respondeu HTTP 200. A task ficou `Running` na imagem nova, e a anterior em `Shutdown`.
- `https://ai-dashboard.converse-ai.app/` respondeu **307**.

### 1.4 ops-claude: 3060, item (e)

- Backup em `/opt/vast-3060/unified3060.py.bak-phase23-20261015`, depois `sudo install -m 0644 -o root -g root ops/vast-3060/unified3060.py /opt/vast-3060/unified3060.py`.
  - Mantive o modo 0644 do arquivo original e da lista aprovada. O plano dizia 0755, mas o script é chamado via `python3` pelos units.
  - `py_compile` deu OK.
  - `grep -c record_aux_lifecycle` encontrou **4** ocorrências.
  - `vast3060.py`, `onstart-unified.sh` e `xtts-server.py` já eram iguais aos do repo (`cmp`).
- No arquivo de segredos `/etc/onboard/secrets/vast-3060.env` (o `SECRETS_PATH` de `vast3060.py` l.43, root-600):
  - backup em `vast-3060.env.bak-phase23-20261015`;
  - acrescentadas `GATEWAY_ADMIN_URL` e `GATEWAY_ADMIN_KEY`, sem eco do valor;
  - `grep -c '^GATEWAY_ADMIN_'` deu 2.
- Os timers `vast-unified-*` continuam `disabled`, sem mudança.

### 1.5 ops-claude: price-sync, item (e2), depois do migrate

- Backup em `/home/pedro/bin/gateway-price-sync.sh.bak-phase23-20261015`, depois `install -m 0755 scripts/price-sync/gateway-price-sync.sh /home/pedro/bin/gateway-price-sync.sh`. O script instalado tem 5 ocorrências de `claude-haiku-5-5`.
- `DRY_RUN=1` terminou com `fx_updated=1 anthropic_updated=2 models_updated=7 models_skipped=0 models_discovered=2 models_unmatched=0`. Ele já lista as units de cache (`cache_read_token`, `cache_write_token`), que o CHECK da 0041 agora aceita. A primeira execução real será a do timer das 08:30 BRT.

### 1.6 Achado durante a Task 1: effort high no ② trunca respostas com `max_tokens` baixo

**FATO medido**, com tenant normal, alias `qwen` (que sai pelo ② Haiku 5.5) e um prompt de redação de campanha:

| `max_tokens` | `reasoning` do cliente | finish | conteúdo (chars) | reasoning_tokens |
|---|---|---|---|---|
| 516 | (ausente → default high) | stop | 434 | 120 |
| 516 | (ausente → default high) | **length** | 255 (cortado) | 392 |
| 300 | (ausente → default high) | **length** | 331 (cortado) | 141 |
| 516 | `{"enabled":false}` | stop | 572 | 0 |
| 40 (smoke inicial) | (ausente → default high) | length | **null** | — |

- **Mecanismo** (FATO, no código): `applyAnthropicEffort` (`gateway/internal/proxy/llm_effort.go`, PASS 6 do `openrouter_director.go`) injeta `reasoning:{"effort":"high"}` quando nem o cliente nem o tenant especificam. Diferente do passo ① (`ThinkingHeadroom` 4096 em `anthropic_translate_req.go`), o ② **não soma headroom ao `max_tokens`**. O reasoning consome o orçamento do cliente. Esse é o Pitfall 3 do 23-RESEARCH, mitigado no ① mas não no ②.
- **Exposição**, medida em `audit_log` + `audit_log_content` nos últimos 3 dias (rotas de chat, por tenant):
  - `campanhas`: 198 req, todas com `max_tokens` (487–541), nenhuma com reasoning. O antiban-rewrite usa saída estruturada validada por Zod strict (`campanhas-chatifix/.../antiban-rewrite/structured-chat.ts`), com política de fallback em `providers.ts`.
  - `chat-ifix`: 51 req com `max_tokens` < 2000. 11.974 das 14.238 trazem reasoning explícito, que é respeitado.
  - `converseai-contacts-eval`: 4 req com `max_tokens` 1–10.
  - `uat10-test`: 12 req.
  - `converseai-classifier`, `-ai-match` e `-format-hint`: 1 cada, com `max_tokens` 300.
- HIPÓTESE: no `campanhas`, um JSON cortado falha na validação e cai no fallback do antiban (envia sem reescrita), sem mandar texto truncado ao cliente final. Para confirmar, seria preciso olhar o log do backend do campanhas num disparo depois do deploy.
- **Não alterei nada por conta disso.** O ajuste fica fora da lista aprovada e é decisão do Pedro. Mitigações disponíveis, via API oficial e sem deploy:
  - `gatewayctl tenant set-llm-effort --slug <slug> --effort low|off` para `campanhas` e os tenants de `max_tokens` curto;
  - ou, em código, somar headroom no PASS 6 do ②, como já é feito no ①.

### 1.7 Rollback

Não foi necessário: nenhuma das condições de falha ocorreu (migrate OK, `/health` 200, imagem certa, sem erros no boot).
