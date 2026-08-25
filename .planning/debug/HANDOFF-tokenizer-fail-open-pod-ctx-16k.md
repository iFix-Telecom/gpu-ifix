> **✅ RESOLVIDO 2026-08-24 (quick 260824-ucv, gateway `develop-06e209a` em prod).**
> A (cascata over-context) + B (tokenizer no tier-0 efetivo) implementados e validados E2E:
> 72k tokens → tier-1 200 com log de cascata e métrica `gateway_over_context_cascaded_total{tenant,upstream}`;
> guard contou 72.015 vs 72.017 do provider; zero WARN de tokenize pós-deploy; 20.9k fica no pod.
> MUDANÇA DE POLÍTICA (Pedro): sensitive TAMBÉM cascateia em over-context (RES-08 intacto nos demais caminhos).
> Item E era falso alarme: 2026-08-24 é SEGUNDA (não domingo) e o gatewayctl lia schedule do env, não do pod_config.
> Bônus: ctx do pod dobrou p/ 32k/slot (KV q8_0, quick 260824-s2t) — os 400 de 18-21k morreram por DOIS caminhos.
> Fix C (alertas) segue aberto. Bug NOVO descoberto: audit trunca request >128KiB (STATE.md 260824-ucv).

# HANDOFF — guard de contexto fail-open deixa passar 18k p/ slot de 16384 (400 no cliente)

**Aberto por:** sessão do crm-dev/Maestro (campanhas-chatifix), 2026-08-24. **Não executei nada no gateway** — trabalho é desta sessão (gpu-ifix).

## Sintoma (cliente)

Turnos do Maestro (crm-dev, tenant `chat-ifix`) morrem com HTTP 400 do gateway:

```
{'error': {'code': 400, 'message': 'request (18068 tokens) exceeds the available context size (16384 tokens), try increasing it', 'type': 'exceed_context_size_error', ...}}
```

O erro vem do **llama-server do pod**, não do gateway. Maestro classifica 400 como `permanent` → turno morre sem retry (`turn_failed`, `turn_served_by_fallback served_by: []`; o `FALLBACK_MODELS` também aponta pro gateway → mesma rota, mesmo 400).

## FATOS medidos (2026-08-24, worker-vm, container `ai-gateway-prod_gateway`)

1. **O guard existe e não disparou.** `docker logs --since 12h $G | grep -c context_length_exceeded` → **0**. Nenhum `context_length_exceeded` emitido enquanto 5 requests 400avam no upstream.
2. **Motivo: tokenizer inalcançável → fail-open.** 8× no mesmo minuto dos 400:
   ```
   WARN "tokencount /tokenize request failed" module=TOKENIZE
   err="Post \"http://172.18.0.1:18000/tokenize\": dial tcp 172.18.0.1:18000: connect: connection refused"
   ```
   `ss -tlnp | grep 18000` no worker-vm → **nada escutando**. `cfg.UpstreamLLMURL` (env `UPSTREAM_LLM_URL=http://172.18.0.1:18000`) é o local-llm ESTÁTICO, que está morto (breaker `local-llm` = open o dia todo).
3. **Quem serve o chat é o pod dinâmico**, não o 18000: `internal/upstreams/loader.go` `OverrideTier0("llm", podURL)` → dispatch com `upstream="emergency_pod_llm"`. Contagem de dispatch LLM hoje: `17 emergency_pod_llm / 12 openrouter-chat`.
4. **Cap vs realidade batem** — o problema é só o wiring: `internal/proxy/tokencount.go:56` `ChatContextCap = 16384` = per-slot (`--ctx-size 32768`, `-np 2`) em `pod/docker-compose.yml:49`, `internal/emerg/lifecycle.go:648`, `internal/primary/onstart.go:29`. Ou seja, o cap está CORRETO; ele só nunca é aplicado quando o `/tokenize` do endereço estático não responde.
5. **Janela do incidente:** até 07:42 BRT o chat ia pro `openrouter-chat` (ctx grande) e os mesmos ~18k passavam; primary pod ficou `ready` às **07:43:27** (lifecycle 368, vast instance 48549916) → tier-0 assumiu → 400. Impacto no dia: `/v1/chat/completions` = **24×200, 5×400**.
6. **O request de 18k é legítimo, não é histórico inchado.** Cliente: Maestro, conversa com 3 mensagens / 95 chars. O volume vem de **`n_tools: 101`** (schemas JSON das tools) + system prompt. Ou seja: requests grandes vão continuar existindo.

### Repro rápido

```bash
ssh worker-vm 'G=$(docker ps -q -f name=ai-gateway-prod_gateway|head -1)
  docker logs --since 12h $G | grep -i TOKENIZE | tail
  docker logs --since 12h $G | grep -c context_length_exceeded
  docker logs --since 12h $G | grep dispatching | grep "\"role\":\"llm\"" | grep -oE "\"upstream\":\"[a-z_-]+\"" | sort | uniq -c'
ss -tlnp | grep 18000   # no worker-vm: vazio
```

## Análise

`proxy.NewTokenCounter(rdb, cfg.UpstreamLLMURL, log)` (`cmd/gateway/main.go:809`) pina o tokenizer no **URL estático de boot**. O dispatcher (`internal/proxy/dispatcher.go:239-259`) chama `Enforce` antes de resolver tier-0 — então quando `OverrideTier0` aponta pro pod (ou quando o 18000 simplesmente morre), o guard vira no-op por política fail-open (`tokencount.go` cabeçalho: "any error talking to /tokenize or the cache returns (0, nil)").

Consequência: a proteção RES-07 só funciona no cenário em que o local-llm estático está vivo — justamente o cenário em que ela é menos necessária. Com pod dinâmico servindo, o excesso vira 400 do upstream, que o gateway repassa cru e **sem failover** (400 não é `errDialFailedFallthrough` nem `errUpstreamRetryable`).

## Opções (sessão do gateway decide)

| # | Mudança | Nota |
|---|---|---|
| **B** | Tokenizer segue o **tier-0 efetivo** (consultar `Loader.Resolve(role,0)`/override em vez do URL de boot) **e** over-cap **roteia pro tier-1** em vez de 400 | Fix da classe: request grande é legítimo, openrouter aguenta. Cuidado: `Enforce` hoje roda ANTES do resolve — inverter ordem ou resolver 2×. |
| **C** | Tratar `exceed_context_size_error` do pod como retryable → cascata tier-1 (padrão `errUpstreamRetryable` já existe p/ STT em `gemini_stt_director.go`) | Paliativo mais barato que B; **atenção D-07**: chat com SSE já pode ter flushado bytes — só é seguro pré-byte/non-stream. |
| **D** | Subir ctx do pod (`--ctx-size 65536 -np 2` = 32k/slot) + `ChatContextCap` | Depende de VRAM da RTX 3090; empurra o teto, não resolve a classe. |
| **E** | (Lateral, custo) Investigar por que o **primary pod provisionou domingo 07:30** com `PRIMARY_POD_SCHEDULE_DISABLED=true` e `Days=[mon..fri]` — lifecycle 368. Nenhum `force` nos logs. `gatewayctl primary state` = ready, `schedule` Disabled:true, "Next transition: 2026-08-24T09:00:00-03:00 (up)". | Vast.ai cobrando fora da janela. |

Recomendação de quem abriu: **B** (com C como degrau intermediário se B for grande).

## Do lado do cliente (higiene, NÃO é a correção)

O fallback é do gateway (ver decisão de arquitetura na PARTE 2). O que segue reduz o TAMANHO do request,
mas não substitui o fix — requests grandes são legítimos e vão continuar existindo:

- Cortar `enabled_tools` dos agentes (98–101 tools ligadas por agente ⇒ os ~18k vêm dos schemas).
- Contexto: o crm-dev subiu Maestro v0.1.60 e passou a mandar `EMBEDDING_DIM=1024`, o que **reativou o KB
  retrieve** — turnos tendem a ficar maiores, não menores.

---

# PARTE 2 — "por que não cai no fallback?" (resposta medida) + correção proposta

Atualização 2026-08-24 (mesma sessão do crm-dev). Volume real: **63 `exceed_context_size_error` em 2h**, cada um matando um turno do Maestro na conta 1 (0800 iFix) — conversas 3610, 1027 etc., requests de 18.7k e 21k tokens.

## Por que o tier-1 NÃO entra

O failover para tier-1 (`cascadeTier1`, `internal/proxy/dispatcher.go:393`) só acontece em **três** situações:

1. **breaker do tier-0 OPEN** (cascata normal, `dispatcher.go:329`);
2. **falha de dial pré-byte** — `errDialFailedFallthrough`, gravado como `fallthrough_ && !wrote` (`dispatcher.go:363`, `errors.go`);
3. **`errUpstreamRetryable`** — que hoje é emitido **APENAS no caminho STT**: `sttRetryableStatusInterceptor` (`audio.go:66-74`) e o director do Gemini (`gemini_stt_director.go:269,278`).

O pipeline de **chat** (`chat.go:46`) monta `ModifyResponse: ComposeInterceptors(...)` **sem nenhum interceptor que classifique status HTTP**. Consequência: o upstream conectou e respondeu — logo, do ponto de vista do dispatcher, "deu certo". O 400 é copiado verbatim para o cliente e **nenhuma cascata é tentada**.

Ou seja: o gateway trata `exceed_context_size_error` como **erro do cliente** (4xx = "seu request está errado"). Mas ele não é: o MESMO request de 18k funciona no `openrouter-chat` (ctx grande) — foi o que aconteceu até 07:42, antes do pod subir. É uma condição de **capacidade do upstream escolhido**, portanto um sinal de ROTEAMENTO, não de request inválido.

Fecha o quadro com a Parte 1: o guard que deveria ter barrado isso ANTES (RES-07, cap 16384) está inerte porque tokeniza contra o `UPSTREAM_LLM_URL` estático, que está morto → fail-open. Então hoje não há nem prevenção nem recuperação.

> **DECISÃO DE ARQUITETURA (Pedro, 2026-08-24): o fallback é responsabilidade do AI-GATEWAY. O cliente
> não deve implementar fallback próprio.** O Maestro chama um alias e espera uma resposta; escolher
> upstream, cascatar e degradar é contrato do gateway. Logo, o que está descrito abaixo não é uma
> "melhoria opcional" — é o gateway deixando de cumprir o contrato dele numa classe de erro.
>
> Corolário: o `FALLBACK_MODELS` do Maestro é inócuo por construção (aponta para o MESMO gateway →
> mesmo alias → mesmo upstream) e **não deve** ser usado como remédio. Nenhuma correção aqui pode ser
> fechada com "o cliente que se vire".

## Correção proposta (ordem de valor)

### A. Classificar over-context como retryable no chat *(o fix que resolve o sintoma)*
Criar um `chatRetryableStatusInterceptor`, espelho do de STT, que devolve `errUpstreamRetryable` quando o upstream responde **400 com `error.type == "exceed_context_size_error"`** (e/ou 413). O `ErrorHandler` já sabe suprimir o write e sinalizar fallthrough para esse sentinel (`errors.go:86`), então a cascata para o tier-1 sai de graça.

Restrições que o texto do fix precisa respeitar:
- **Só non-streaming.** `ModifyResponse` roda depois do corpo bufferizado e antes do primeiro byte ir ao cliente — seguro. Para SSE, se a tee já tiver flushado, vale a regra D-07 (não fazer failover). Gate por `streaming == false`.
- **NUNCA cascatar tenant `sensitive`.** RES-08 proíbe tier-1 externo para `telefonia`/`cobrancas`. Para esses, over-cap deve continuar erro explícito (400/503 com envelope claro), jamais vazar payload para OpenRouter. O dispatcher já conhece `ac.DataClass` (`dispatcher.go`, `sensitive := ac.DataClass == auth.DataClassSensitive`) — o gate tem que estar no interceptor ou imediatamente antes da cascata.
- Emitir métrica/log próprio (`over_context_cascaded{tenant,upstream}`) — senão vira degradação invisível e cara (todo request grande passa a rodar no provider pago).

### B. Fazer o guard voltar a funcionar (tokenizer no tier-0 EFETIVO)
`cmd/gateway/main.go:809` fixa o tokenizer em `cfg.UpstreamLLMURL` (endereço de boot). Resolver em request-time via `Loader.Resolve(role, 0)` — que já reflete o `OverrideTier0` do pod dinâmico — e usar a URL do upstream que REALMENTE vai atender; o estático fica como fallback.
Detalhe de ordem: hoje `Enforce` roda **antes** do resolve de tier-0 (`dispatcher.go:239` vs `:265`). Ou inverte a ordem, ou resolve o tier-0 mais cedo só para obter a URL.
Com A + B juntos, o caminho ideal fica: **pré-dispatch, detectou over-cap → manda direto para o tier-1** (sem gastar a chamada que vai falhar) e sem 400 nenhum.

### C. Não deixar o guard morrer em silêncio
`tokencount.go` é fail-open por design (correto), mas a falha do `/tokenize` sai só como `WARN`. Foram 8 avisos no minuto dos 400 e ninguém viu. Adicionar contador + alerta ("token guard inerte há N minutos") — a política fail-open é aceitável; a fail-open *silenciosa e indefinida* não.

### D. (Opcional, ortogonal) subir a janela do pod
`--ctx-size 32768 -np 2` = 16384/slot (`pod/docker-compose.yml:49`, `internal/emerg/lifecycle.go:648`, `internal/primary/onstart.go:29`). Ir para `65536 -np 2` (32k/slot) empurra o teto e depende da VRAM da 3090 — mas não substitui A/B: requests maiores sempre voltarão a existir.

### E. (Lateral, custo) provisionamento fora da janela
`gatewayctl primary state` mostrou `ready` com `PRIMARY_POD_SCHEDULE_DISABLED=true` e `Days=[mon..fri]`, tendo provisionado **domingo 07:30** (lifecycle 368, vast 48549916). Nenhum `force` nos logs. Vale entender quem disparou — é GPU paga rodando fora do previsto.

## Como validar o fix

1. Reproduzir: `POST /v1/chat/completions` com ~18k tokens, tenant `chat-ifix`, com o pod servindo → hoje 400.
2. Com A: mesma chamada → **200 servido por `openrouter-chat`**, log de cascata, métrica incrementada.
3. Tenant sensitive (`telefonia`) com mesmo payload → **continua erro**, sem tráfego externo (esse teste é obrigatório).
4. Com B: `docker logs` do gateway não mostra mais `tokencount /tokenize request failed` e `context_length_exceeded` volta a aparecer quando cabível.
5. Regressão STT: `errUpstreamRetryable` do Gemini continua funcionando (não mexer no `audio.go`).
