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

---

## Task 2 — aliases, preços, enable, smokes e 3090

### 2.1 Snapshot dos aliases

O snapshot é o `admin-model-aliases-pre-20261015T024935.json` (seção 1.0): `GET /admin/model-aliases`, 28 linhas, root-600 em `~/gw-phase23-rollout/`.

### 2.2 Linhas de alias (passo 2), via `gatewayctl model-alias set` (docker exec no container novo)

Rodaram de **05:56:0x a 05:57:05** as 27 linhas da seção (b) do 23-ROLLOUT-CHANGES: 9 aliases × {`anthropic-haiku` → `claude-haiku-5-5`, `openrouter-chat` → `anthropic/claude-haiku-5.5` + HAIKU_PREFS, `openrouter-deepseek` → `deepseek/deepseek-v4.1-flash:exacto` + DS_PREFS}. **`CFG_FAIL=0`.**

O diff de `GET /admin/model-aliases` contra o snapshot, conferido logo depois, dá **28 → 48 linhas: 20 adicionadas, 0 removidas, 0 alteradas**.
- Adicionadas: `anthropic-haiku` e `openrouter-deepseek` nos 9 aliases (18 linhas), mais `openrouter-chat` nos aliases novos `anthropic/claude-haiku-5.5` e `claude-haiku-5-5` (2 linhas).
- As 7 linhas `openrouter-chat` que já existiam ficaram idênticas. As linhas `local-llm`, stt e embed não foram tocadas.
- **`maestro-summary` está idêntico ao snapshot**: `openrouter-chat` → `deepseek/deepseek-v4.1-flash:nitro`, prefs `{"order":["BaseTen"],"quantizations":["fp8"],"allow_fallbacks":true,"data_collection":"deny"}`.
- Prefs por tenant (item 4d): nenhuma ação, porque 0 tenants têm `provider_prefs`.

### 2.3 Habilitação de ③ e ① (passo 2b), DEPOIS das linhas de alias

| Horário (UTC) | Comando | Resultado |
|---|---|---|
| 05:57:05 | (última linha de alias gravada) | — |
| 05:57:26 → 05:57:29 | `gatewayctl upstreams enable --name=openrouter-deepseek` | `upstream "openrouter-deepseek" enabled=true` |
| 05:57:29 → 05:57:31 | `gatewayctl upstreams enable --name=anthropic-haiku` | `upstream "anthropic-haiku" enabled=true` |

`gatewayctl upstreams list` às 05:57:3x:

```
local-llm            llm     0     false
anthropic-haiku      llm     1     true     UPSTREAM_LLM_ANTHROPIC_URL   (sem probe: dormente, sem env)
openrouter-chat      llm     1     true     … config 23ms
openrouter-deepseek  llm     1     true     … config 23ms
```

O `anthropic-haiku` segue dormente até o 23-18, porque não há `UPSTREAM_LLM_ANTHROPIC_URL`/`_API_KEYS` no stack. Todos os smokes abaixo, feitos depois do enable, saíram por `openrouter-chat`, então ① não interceptou tráfego.

### 2.4 Preços (passo 3), `gatewayctl prices set -provider anthropic`, de 05:57:47 a 05:58:13

Foram gravadas 10 linhas, com **`PRICE_FAIL=0`**. `gatewayctl prices list` mostra todas `active`:

| model | input | cache_write | cache_write_1h | cache_read | output |
|---|---|---|---|---|---|
| `claude-haiku-5-5` | 0.00000010 | **0.00000012** | 0.00000020 | 0.00000001 | 0.00000050 |
| `claude-haiku-5-5:long` | 0.00000050 | **0.00000063** | 0.00000100 | 0.00000005 | 0.00000250 |

Os valores em negrito confirmam em prod a perda de precisão prevista (`NUMERIC(12,8)`): 1.25e-7 virou 1.2e-7 e 6.25e-7 virou 6.3e-7. É **pendência antes do 23-18**, com migration de escala. `anthropic/claude-haiku-5.5 @ openrouter-fireworks` (1e-7 / 5e-7) não mudou.

### 2.5 Smokes (passo 4)

Feitos com uma chave real de tenant normal (`uat10-test`), entre 05:58:28 e 05:59:xx, mais de 70 s depois da última linha de alias. O upstream de cada request vem de `audit_log.upstream` pelo `request_id`.

**Não-stream, com mensagem final do assistant + `temperature:0`, 9 de 9 aliases → 200, todos servidos por `openrouter-chat`:**

| alias | HTTP | `audit_log.upstream` | `billing_events.cost_source` | max_tokens 40 | max_tokens 2000 |
|---|---|---|---|---|---|
| `qwen` | 200 | openrouter-chat | provider_reported | finish=length, content vazio | finish=stop, content ok |
| `deepseek-flash` | 200 | openrouter-chat | provider_reported | idem | stop, ok |
| `deepseek-v4-flash` | 200 | openrouter-chat | provider_reported | idem | stop, ok |
| `gpt-4o` | 200 | openrouter-chat | provider_reported | idem | stop, ok |
| `gpt-5.4-mini` | 200 | openrouter-chat | provider_reported | idem | stop, ok |
| `gpt-5.4-mini-2026-03-17` | 200 | openrouter-chat | provider_reported | idem | stop, ok |
| `ia-kanban` | 200 | openrouter-chat | provider_reported | idem | stop, ok |
| `anthropic/claude-haiku-5.5` | 200 | openrouter-chat | provider_reported | idem | stop, ok |
| `claude-haiku-5-5` | 200 | openrouter-chat | provider_reported | idem | stop, ok |

- Nenhum 400 de prefill ou sampling, que era o objetivo do HAIKU-04.
- O conteúdo vazio com `max_tokens` 40 é o mesmo achado da seção 1.6: o effort high padrão do ② consome o orçamento.
- **Stream** (`deepseek-flash`, `stream:true`): 200, e o SSE termina em `data: [DONE]`. `audit_log.stream=t`, upstream openrouter-chat, `provider_reported`.
- **tool_choice forçado** (`qwen`, `get_weather`): 200, `finish_reason=tool_calls`, 1 `tool_calls` `get_weather:{"city": "Goiania"}`.
- **Cache e billing** (HAIKU-06): mandei 2 requests seguidos com o mesmo system de ~19k tokens no alias `qwen`. O gateway injeta o `cache_control` (PASS do `openrouter_director.go`).

| request | `usage.prompt_tokens_details` | `billing_events` |
|---|---|---|
| 1º | cache_write_tokens 19021 | `tokens_cache_write=19021`, `tokens_cache_read=0`, `provider_reported`, R$ 0,012108 |
| 2º | cached_tokens 19021 | **`tokens_cache_read=19021`**, `provider_reported`, R$ 0,001138 |

### 2.6 Passo ③ ao vivo por alias canário (passo 5), sem force-open de breaker

- (5a) às 05:59:56 → 05:59:59: `gatewayctl model-alias set -alias p23-canary-step3 -upstream openrouter-deepseek -target deepseek/deepseek-v4.1-flash:exacto -provider-prefs <DS_PREFS>`.
- Às 06:01:13, mais de 70 s depois, um POST chat com `model:"p23-canary-step3"` (tenant normal) deu **200**. Detalhes:
  - resposta com `"model":"deepseek/deepseek-v4.1-flash"` e `"provider":"Morph"`;
  - `audit_log.upstream = openrouter-deepseek`;
  - `billing_events`: `upstream=openrouter-deepseek`, `model=deepseek/deepseek-v4.1-flash`, `cost_source=provider_reported`, 40 in / 8 out, R$ 0,000027.
  - Isso prova que proxy, alias, prefs e billing do ③ estão vivos.
- Observação: o conteúdo devolvido pelo DeepSeek foi `"/no_think\n\nOk."`. O gateway não injeta `/no_think`: `grep` em `gateway/internal` e `gateway/cmd` não encontra nada. HIPÓTESE: é eco do próprio modelo ou do provider Morph para um prompt curto com prefill. Para tirar a dúvida, seria preciso repetir com outro prompt ou provider.
- (5b) a cascata ②→③ por status e por timeout de cabeçalho está coberta pelo 23-14 (`ExternalChatInterceptors`, `NewChatFallthroughTransport`) e pela integração do 23-15 (`TestHaikuChain_*`).
- (5c) às 06:01:35: `gatewayctl model-alias delete -alias p23-canary-step3 -upstream openrouter-deepseek`. Depois disso, `GET /admin/model-aliases` voltou a 48 linhas, o diff contra o snapshot segue em 20 adicionadas, 0 removidas e 0 alteradas, e **o canário sumiu**.
- **Nenhum force-open de breaker foi executado.**

### 2.7 RES-08 (passo 6)

Com a chave `telefonia` (sensitive), um POST chat `deepseek-flash` às 06:00:03 deu **HTTP 503** `{"code":"upstream_unavailable_for_sensitive_tenant"}`, com `audit_log.upstream=blocked_sensitive`. Não houve billing.

### 2.8 maestro-summary (passo 7)

A linha está idêntica ao snapshot (seção 2.2), conferida de novo depois da remoção do canário: `maestro identical: True`.

### 2.9 Billing desde o deploy (passo 8), `ts > 2026-10-15 05:50:37+00`, consultado às ~06:02

| upstream | cost_source | requests |
|---|---|---|
| openrouter-chat | provider_reported | 27 |
| openrouter-deepseek | provider_reported | 1 |
| local-embed | (NULL, local) | 8 |

`openrouter-*` com `price_table` deu **0**: todas as requests do OpenRouter trouxeram `usage.cost`.

### 2.10 3090 desligada formalmente (passo 9, HAIKU-07)

- `SELECT schedule_disabled FROM ai_gateway.pod_config` retornou **`t`** (`updated_at 2026-10-14 11:15:42 UTC`). O log de boot do gateway novo também mostra `schedule_disabled=true` e `fsm_state=asleep`.
- A API da Vast (`GET /api/v0/instances/?owner=me`) às 06:00:19 retornou `{"instances_found": 0, "instances": []}`: **0 instâncias** e 0 `ifix-primary-*`/`ifix-emerg-*`. O orphan sweep do boot também listou 0.
- `local-llm` está com enabled=false.
- **3090 desligada formalmente (HAIKU-07).**

### 2.11 `GET /admin/economy` (passo 10)

Retornou HTTP 200 (range 2026-10-01 → 2026-10-15, America/Sao_Paulo). Os campos novos estão presentes:
- `pct_servido_local_tokens` = 0.125
- `custo_creditos_haiku_brl` = 0
- `custo_externo_caixa_brl` = 288.57
- `vast_3060_brl` = 0

Os campos antigos (`phantom_brl`, `vast_brl`, `custo_openrouter_brl`, `pct_servido_local`, `series[]`) continuam lá.

---

## Task 3 — validação imediata (cerca de 30 min depois do deploy)

Janela: deploy às 05:50:37 UTC, consulta às 06:21:20 UTC (~31 min). `/health` 200 às 06:21. Às 03h BRT o tráfego de clientes é baixo, então esta janela **não substitui** a recontagem de 24 h.

| # | Medida, desde 05:50:37 UTC | Resultado |
|---|---|---|
| 1 | 400 com `prefill`/`temperature`/`top_p` em `audit_log_content.response` | **0**; 400 no total: **0** |
| 2 | 4xx/5xx por upstream | `blocked_sensitive` 503 ×1 (smoke RES-08 proposital); `local-embed` 502 ×15 |
| 3 | % do chat por upstream | openrouter-chat 32 (94,1%); openrouter-deepseek 1 (2,9%, só o canário); blocked_sensitive 1 (2,9%) |
| 4 | billing por upstream | openrouter-chat 32 `provider_reported` (cache_read 19.599, cache_write 20.433); openrouter-deepseek 1 `provider_reported`; gemini-stt 3 `price_table`; local-embed 24 |
| 5 | chat 200 **sem** billing_events | **0** |
| 6 | tráfego real de clientes | `analise-transcr-voip` chat 3× 200 via openrouter-chat (com cache_read: 19.599 − 19.021 do smoke = 578 tokens de cliente); `transcricao-voip` STT 3× 200; `chat-ifix` embeddings 23× 200 / 15× 502 |

**`local-embed` 502 não é novo.** FATO: nas 6 h anteriores ao deploy (23:50–05:50 UTC), a mesma consulta deu `local-embed 502 ×66` e 200 ×57. O log mostra `upstream error … embed … timeout awaiting response headers`, igual ao container antigo (`ERROR upstream error` ×12 nos últimos 20 min dele). O embed não é tocado pela fase 23.

**Logs do gateway novo (WARN/ERROR desde 05:50:30, contagem por mensagem), comparados com o container antigo (`c27f77128e46`, 05:30–05:50):**

| mensagem | novo (~31 min) | antigo (~20 min) |
|---|---|---|
| `upstream url env var missing; row skipped` | 38 | 6 |
| `auth rejected` (request sem chave) | 34 | 35 |
| `tokencount /tokenize non-200` (404) | 23 | 35 |
| `ERROR upstream error` (embed timeout) | 8 | 12 |
| `probe UPDATE failed` / `pgxlisten error` / `listener exited` | 0 | 10 / 4 / 4 |

- **Achado menor (FATO):** `row skipped` agora aparece **2× por minuto**, uma para `anthropic-haiku` e outra para `openrouter-tts`, a cada refresh do loader. O 23-14 previa "1 WARN por upstream e Debug depois" para o caminho de dormência do director. O WARN do **loader de upstreams** (`UPSTREAMS … missing_url_env`) é outro caminho e se repete. Não afeta o tráfego: é ruído de log até o 23-17/23-18 setarem os envs. Alternativas: desabilitar `anthropic-haiku` até o 23-18, opção prevista na seção (b2), ou rebaixar o log para Debug.
- Nenhum panic ou fatal. O container antigo tinha erros de listener e probe que não aparecem no novo.

## Checkpoint — Task 3: Pedro confirma o rollout após 24 h

- **Status: AGUARDANDO confirmação de 24 h.** Ainda não há resposta do Pedro. Por instrução do orquestrador, o executor não esperou 24 h e registrou só a validação imediata acima.
- **Registrado em:** 2026-10-15 03:21 BRT (06:21 UTC).
- **Force-open global de breaker: NÃO executado.** Era o item opcional, e não houve autorização escrita do Pedro.
- **Recontagem de 24 h**, a partir de 2026-10-16 02:51 BRT (05:51 UTC). É uma consulta só de leitura em `bd_ai_gateway`, rodada no worker-vm com o DSN do container vivo:

```bash
ssh worker-vm 'C=$(docker ps -q -f name=ai-gateway-prod_gateway|head -1); DSN=$(docker inspect --format "{{range .Config.Env}}{{println .}}{{end}}" $C | sed -n "s/^AI_GATEWAY_PG_DSN=//p"); docker run --rm -i -e PGDSN="$DSN" --entrypoint sh postgres:16-alpine -c "psql \"\$PGDSN\" -X -P pager=off"' <<'SQL'
SET default_transaction_read_only = on;
-- (1) 400 de prefill/sampling (esperado 0)
select count(*) from ai_gateway.audit_log a join ai_gateway.audit_log_content c using (request_id)
 where a.ts > '2026-10-15 05:50:37+00' and c.ts > '2026-10-15 05:50:37+00' and a.status_code = 400
   and (c.response::text ilike '%prefill%' or c.response::text ilike '%temperature%' or c.response::text ilike '%top_p%');
-- (2) 5xx/4xx por upstream
select upstream, status_code, count(*) from ai_gateway.audit_log
 where ts > '2026-10-15 05:50:37+00' and status_code >= 400 group by 1,2 order by 1,2;
-- (3) % do chat servido por openrouter-deepseek (cascata)
select upstream, count(*), round(100.0*count(*)/sum(count(*)) over (),2) pct from ai_gateway.audit_log
 where ts > '2026-10-15 05:50:37+00' and route like '%chat%' group by 1 order by 2 desc;
-- (4) billing: openrouter-* com price_table (esperado ≈ 0)
select upstream, cost_source, count(*), sum(tokens_cache_read) from ai_gateway.billing_events
 where ts > '2026-10-15 05:50:37+00' group by 1,2 order by 1,2;
-- (5) finish truncado por effort high (achado 1.6): chat 200 com finish_reason=length por tenant
select t.slug, count(*) filter (where c.response::text like '%"finish_reason": "length"%' or c.response::text like '%"finish_reason":"length"%') n_length, count(*) n
 from ai_gateway.audit_log a join ai_gateway.audit_log_content c using (request_id) left join ai_gateway.tenants t on t.id=a.tenant_id
 where a.ts > '2026-10-15 05:50:37+00' and c.ts > '2026-10-15 05:50:37+00' and a.route like '%chat%' and a.status_code=200 group by 1 order by 2 desc;
SQL
```

- O que o Pedro confere na UI está no plano, Task 3 itens 2 e 3: `https://ai-dashboard.converse-ai.app/economia`, com os cards "Haiku (coberto por créditos)" e "Custo externo (caixa)", comparados com `openrouter.ai/activity`. Também precisa confirmar que chat-ifix e Maestro não tiveram erro novo.
- **Pontos para a decisão do Pedro junto da confirmação:**
  1. achado 1.6, effort high truncando `max_tokens` baixo (`campanhas` e outros);
  2. escala NUMERIC dos preços de cache;
  3. ruído de WARN `row skipped`.

## Correção pós-rollout — folga de max_tokens com effort (2026-10-15 06:36 UTC)

- **Problema (FATO, medido no 23-16):** effort high por padrão consome o `max_tokens` do cliente no OpenRouter; com 300–516, 2 de 3 respostas cortadas; com 40, vazias. Exposto: `campanhas` (max_tokens 487–541), parte de `chat-ifix` e `converseai-*`.
- **Fix:** commit `114d7ab` (`fix(23-09)`): soma `OpenRouterThinkingHeadroom` = 4096 a `max_tokens`/`max_completion_tokens` do cliente quando o effort não é off/low (mesma regra do passo ①). Teste `TestOpenRouterDirector_Effort_HeadroomNoMaxTokensDoCliente`.
- **CI:** build-gateway em `7bbf9cb` — unit, sqlc, integração e build&push success (redeploy dev falhou, conhecido).
- **Deploy:** snapshot `~/gw-phase23-rollout/stack38-*-pre-headroom-*.json` (600); `PUT /api/stacks/38?endpointId=6` às 06:36:16 UTC, só a linha `image:` → `develop-7bbf9cb@sha256:7f2da57cce6c853b6ded96ddbf50364a0898cfdd39c0dc2ae353f248b92d4708`, Env 88 inalterado. `/health`: 504 único às 06:36:54, 200 a partir de 06:36:59. Task `develop-7bbf9cb` Running; sem panic/fatal/ERROR no log.
- **Smoke (tenant campanhas, alias deepseek-flash → Haiku 5.5):** `max_tokens:500` → `finish_reason:stop`, 97 chars; `max_tokens:40` → `finish_reason:stop`, 115 chars.
- **Rollback:** PUT do stack 38 com `stack38-file-pre-headroom-*.json` (imagem `develop-99d73b2`).
