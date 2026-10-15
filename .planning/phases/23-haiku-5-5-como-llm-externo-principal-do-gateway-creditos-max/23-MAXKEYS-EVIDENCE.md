# 23-MAXKEYS-EVIDENCE: ativação do passo ① (Haiku direto, créditos Max)

Nenhuma chave neste arquivo. Chaves em `/etc/onboard/secrets/anthropic-max-keys.env` (root 600, ops-claude).

## Checkpoint — Task 1: Pedro gera as chaves, confirma termos e informa dias de renovação

Resposta do Pedro em 2026-10-15 ~12:10 BRT: entregou as chaves de duas contas, cada uma com seu workspace.
- `max-pedro`: conta pe.araujosilvas@gmail.com, workspace `wrkspc_01MPUVEtS6GYKYSLKB41HKfT`
- `max-financeiro`: conta financeiro.ifixtelecom@gmail.com, workspace `wrkspc_0183yAyHeSq9RbMbTi4n5TQj`

Ficaram em aberto, sem resposta do Pedro:
- os dias de renovação: a env vai sem `@dia`, e uma chave esgotada só é marcada até o próximo re-teste de 6 h;
- a confirmação explícita dos termos suplementares dos créditos;
- o spend limit de cada workspace.

As chaves foram coladas no chat da sessão. Pela recomendação do plano (T-23-61), elas deveriam ser rotacionadas depois.

### Formato das chaves (FATO, medido 2026-10-15 ~12:05 BRT)

As duas chaves têm o formato de chave de usuário (prefixo `usr`) e não são presas a um workspace.
- `GET /v1/models` sem o header `anthropic-workspace-id` responde 400: "This API key is not scoped to a workspace, so this request must include the anthropic-workspace-id header".
- Com o header, responde 200 e lista `claude-haiku-5-5`.

O gateway não mandava esse header. Correção no commit `5ce151e`: o env aceita `rotulo=<chave>#<workspace>@<dia>`, e o KeyPool e o probe mandam o header de cada chave.

### Rate limit (FATO, headers da 1ª chamada `/v1/messages` de cada conta)

| Conta | org id | requests/min | input tokens/min | output tokens/min |
|-------|--------|--------------|------------------|-------------------|
| max-pedro | 82b4f92d-… | 1000 | 2.000.000 | 400.000 |
| max-financeiro | 9245443d-… | 1000 | 2.000.000 | 400.000 |

As duas contas estão em orgs diferentes.

## Gates diretos na API (antes de mexer no stack 38, sem tráfego de cliente)

Rodados em 2026-10-15 ~12:20 BRT com a chave `max-pedro` + workspace e `claude-haiku-5-5`. Os corpos foram derivados de `req_messages_tool_loop.golden.json`.

| Gate | Corpo | Resultado |
|------|-------|-----------|
| g1 | tools + effort high, turno 1 | 200, `stop_reason=tool_use`, 2 tool_use, 0 blocos thinking |
| g2 omit | conversa do g1 + tool_result, sem blocos thinking, effort high | **200**, `end_turn` |
| g2 disable | idem, com `thinking:{type:disabled}`, max_tokens 1000 | 200, `end_turn` |
| g3 | `tool_choice` forçado por nome | 200, `tool_use` |
| g4 | `output_config.format` json_schema | 200, JSON válido com as chaves do schema |
| g5 | `stream:true` | 200, 7 eventos, `message_stop`, sem evento `error` |

Decisão: `ANTHROPIC_TOOL_LOOP_MODE=omit`, porque o g2 omit deu 200.

A HIPÓTESE H10 fica confirmada para este caso: com effort high, o turno 1 não emitiu bloco thinking, então o omit não tinha o que omitir. **NÃO SEI** se o 400 aparece quando o turno 1 traz thinking. Para resolver, monitorar nas verificações de +30 min e +2 h os 400 do ① que citam thinking.

## Ativação no stack 38 (2026-10-15 15:28:58 UTC)

- Snapshot: `~/gw-phase23-maxkeys/stack38{,-file}-pre-20261015T152757.json` (600).
- `PUT /api/stacks/38?endpointId=6` respondeu HTTP 200.
  - Imagem: de `develop-56cea8c` para `develop-5ce151e@sha256:f875d84e39d5ce8c452de4dfb549836bc93b9e293bcf6481bb2b58c260d55941`.
  - Compose ganhou 3 linhas. Env passou de 90 para 93: `UPSTREAM_LLM_ANTHROPIC_URL=https://api.anthropic.com`, `UPSTREAM_LLM_ANTHROPIC_API_KEYS` (valor lido com sudo do arquivo root-600, sem eco) e `ANTHROPIC_TOOL_LOOP_MODE=omit`.
- Log de boot: `anthropic-haiku enabled`, `keys=2`, `labels=["max-pedro(4ba45225)","max-financeiro(29710be9)"]`, `tool_loop_mode=omit`. Sem nenhum ERROR, panic ou fatal.
- `GET /admin/anthropic/keys`: as 2 chaves estão `state=ok`, `configured=true`.
- `/health` respondeu 200.

## Smokes via gateway (tenant chat-ifix, modelo `deepseek-flash`)

| Smoke | Resultado |
|-------|-----------|
| (a) não-stream | 200, `claude-haiku-5-5`, `stop` |
| (b) stream | 200, 7 chunks SSE e `[DONE]` |
| (c) tool loop em 2 turnos | turno 1 com 200 e `tool_calls` (1); turno 2 com tool result, 200 e `stop`. Nenhum 400 de thinking |
| (d) tool_choice forçado | 200, `tool_calls` (1) |
| (e) json_schema + `parallel_tool_calls:false` | 200, `{"cidade": "Goiânia", "uf": "GO"}` |
| (f) conversa terminando em assistant | 200, `stop` (prefill normalizado) |
| (g) effort padrão e `reasoning_effort:low` | 200 nos dois |
| (h) `reasoning:{enabled:false}` | 200 |

- No `audit_log`, desde 15:29 UTC, há 14 requisições de chat, todas `anthropic-haiku` 200. Doze são smokes e duas são tráfego real.
- **Gap (NÃO SEI):** a resposta OpenAI não traz `completion_tokens_details.reasoning_tokens`, então não deu para medir pelo cliente a diferença entre high, low e off. No g1 direto, o effort high com um prompt simples também gerou 0 blocos thinking. Para resolver, comparar `output_tokens_details.thinking_tokens` direto na API num prompt que exija raciocínio.

## Cache e billing

- Duas chamadas idênticas com system de cerca de 1,5 k tokens: a 1ª teve `cached_tokens=0`; a 2ª teve **`cached_tokens=1585`**, de 1589 tokens de prompt.
- `billing_events` desde 15:29 UTC: 14 linhas `anthropic-haiku`, `claude-haiku-5-5`, `cost_source=price_table`. Somas: tokens_in 20419, tokens_out 2439, **tokens_cache_read 1585**, tokens_cache_write 17121, R$ 0,01742.
- `GET /admin/economy`: **`custo_creditos_haiku_brl` = 0,017424**, maior que 0.

Critérios de rollback: nenhum disparou. O ① está **mantido**.

## Verificação +30 min (2026-10-15 16:00 UTC)

- Chat desde 15:29 UTC: **329 requisições, todas `anthropic-haiku` com status 200 (100%)**.
  - Nenhum 4xx ou 5xx do ①.
  - Nenhuma requisição caiu para o ② ou o ③.
- Billing: 329 linhas com `cost_source=price_table`, `tokens_cache_read=1.675.176` e R$ 0,8236.
- Chaves: `max-pedro` e `max-financeiro` com `state=ok`.
  - No log, 0 eventos de cooldown, exhausted ou revoked.
- Critérios de rollback: nenhum disparou. O ① continua **mantido**.

## Verificação +2 h (2026-10-15 17:30 UTC)

Janela: 15:29 → 17:30 UTC. Fonte: `ai_gateway.billing_events` (somente leitura), logs do gateway e `/admin/anthropic/keys`.

| upstream | status | req | % |
|---|---|---|---|
| anthropic-haiku | 200 | 1598 | 99,9 |
| openrouter-chat | 200 | 1 | 0,1 |

- 4xx/5xx: 0.
- Billing anthropic-haiku: 1600 eventos, `cost_source=price_table`, `tokens_cache_read` 7.978.191, R$ 4,2902.
- Chaves: `max-pedro` e `max-financeiro` com `state=ok`, sem reason/until.
- A única requisição no openrouter-chat (chat-ifix, 17:13:38 UTC, modelo `deepseek/deepseek-v4.1-flash`) foi despachada direto para o openrouter-chat: o log do request_id não mostra tentativa no anthropic-haiku nem cascata. HIPÓTESE: o cliente pediu esse modelo pelo nome; confirmaria com o corpo da requisição (não registrado).
- O jq do script rodou na versão antiga (carregada antes da correção) e falhou; as chaves foram consultadas à mão.

Critérios de rollback: nenhum disparou. O ① continua **mantido**.
