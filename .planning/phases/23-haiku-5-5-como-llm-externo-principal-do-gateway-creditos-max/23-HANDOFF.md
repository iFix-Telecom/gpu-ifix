# HANDOFF — Haiku 5.5 como LLM principal externo do ai-gateway (créditos Max)

Pedido do Pedro em 2026-10-13. Abrir como fase GSD nova (gateway). Card de origem: ClickUp 86akuaa73.

## Decisão do Pedro (não rediscutir)

Tudo o que não roda no pod local passa a seguir esta ordem:

1. **Claude Haiku 5.5 na API direta da Anthropic**, usando os créditos mensais das contas Claude Max
   (2–3 contas). Rodízio entre as chaves; quando uma esgota, passa para a próxima.
2. **Claude Haiku 5.5 no OpenRouter**, priorizando o provedor de maior vazão.
3. **DeepSeek v4.1 Flash no OpenRouter** (o que existe hoje).

Depois de validado em produção: **desligar a 3090** (pod primário Qwen3-30B-A3B).

## Evidência que sustenta a decisão

- `RESULTADO-abc-haiku55-vs-30b-vs-deepseek.md` (commit 186b199): juiz cego, 37 payloads reais.
  Haiku 5.5 effort **high** foi o melhor (13 primeiros lugares, correção 9,0); DeepSeek 8,5; Qwen 30B 7,1 (último).
- Custo da 3090: US$23–34/mês (jul–set, `ai_gateway.primary_lifecycles`) para tokens que custariam
  US$2–6/mês fora. ~80% do uso é `analise-transcr-voip`.
- Volume de chat 30 dias: 662 M tokens entrada / 110 M saída (chat-ifix = 633 M / 102 M).
  HIPÓTESE: Haiku high ~US$140/mês; créditos 2–3× Max 20x = US$400–600.

## Fatos do Haiku 5.5 (verificados em 2026-10-13)

- Model id `claude-haiku-5-5`; OpenRouter `anthropic/claude-haiku-5.5`.
- US$0,10 entrada / US$0,50 saída por M tokens **até 100k tokens por request**; acima, tudo 5×.
  Cache: leitura US$0,01/M, escrita US$0,125 (5 min) / US$0,20 (1 h).
- **`temperature`, `top_p`, `top_k` → 400.** No A/B também foram removidos `frequency_penalty`/`presence_penalty`.
  Clientes que mandam isso hoje: converseai (temperature 0, top_p 1, penalties), analise-transcr (temperature 0).
- **`tool_choice` forçado (`{"type":"function",...}`) FUNCIONA** (testado via OpenRouter provider Anthropic, 37/37).
- Raciocínio adaptativo ligado por padrão (effort `medium`). Decisão: usar **high**.
- Contexto 1M, saída máx 128K.
- Endpoints OpenRouter com o mesmo preço base: Anthropic, Google Vertex global, Azure global,
  Amazon Bedrock, Claude Platform on AWS (regionais +10%).

## Fatos dos créditos Max (anúncio 2026-10-07)

- Crédito de **API** (Claude Platform/Console), não da assinatura. Max 20x = US$200/mês, Max 5x = US$100.
- Sem rollover. Cada plano vincula **1** organização do Console (permanente); cada org recebe de 1 plano.
  → 2–3 contas = 2–3 orgs = 2–3 chaves de API, saldos separados.
- Só vale na API direta da Anthropic (não OpenRouter/Bedrock/Vertex).
- Saldo zerado: a API recusa com "Your credit balance is too low to access the Anthropic API"
  (nunca cobra no plano). O gateway precisa tratar isso como "chave esgotada" e ir para a próxima.
- **Pendente do Pedro:** em cada conta Max, claude.ai → Settings → Billing → Link organization,
  aceitar os termos suplementares, gerar API key. Sem as chaves, dá para desenvolver e testar a
  cadeia com o passo ② (OpenRouter).

## Estado atual do gateway (relevante)

- Prod no worker-vm (Portainer endpoint 6, stack `ai-gateway-prod`), banco `bd_ai_gateway`.
- `ai_gateway.model_aliases` (alias, upstream_name, target, provider_prefs) — precedência
  tenant > alias > env. Aliases do padrão DeepSeek Flash (todos `openrouter-chat`,
  target `deepseek/deepseek-v4.1-flash:exacto`): `qwen`, `deepseek-flash`, `deepseek-v4-flash`,
  `gpt-4o`, `gpt-5.4-mini`, `gpt-5.4-mini-2026-03-17`, `ia-kanban`.
  Prefs atuais (2026-10-12): `{"sort":"price","preferred_min_throughput":{"p90":100},"max_price":{"prompt":0.4},"quantizations":["fp8"],"data_collection":"deny"}`.
  `maestro-summary` é exceção (BaseTen :nitro) — não mexer sem perguntar.
- Upstreams LLM: `local-llm` (tier 0, pod) e `openrouter-chat` (tier 1). Não existe upstream Anthropic.
- Tenants sensíveis (telefonia, cobrancas) NUNCA podem ir para externo (RES-08). Hoje têm zero tráfego de chat.
- Gateway relê aliases a cada 1 min.
- Memórias úteis: `openrouter-provider-prefs-and-dashboard-control`, `gateway-prod-build-deploy`,
  `gateway-integration-tests-not-in-executor-check`, `billing-phantom-economia-semantics`,
  `haiku55-vs-pod-decision`.

## Perguntas em aberto para a fase

1. Adaptador nativo (OpenAI chat/completions → Anthropic Messages, SDK Go oficial) ou o endpoint
   OpenAI-compatível da Anthropic? A skill claude-api recomenda o SDK oficial.
2. Onde guardar as 2–3 chaves e como detectar/marcar chave esgotada até virar o mês.
3. Como representar a cadeia de 3 passos no modelo atual (aliases × upstreams × tiers).
4. Preço do Haiku na tabela `prices` (billing e dashboard), incluindo o degrau de 100k.
5. Mapeamento de effort (high) e se algum tenant precisa de outro nível.
6. Cache de prompt para o chat-ifix (maior volume).

## Outras pendências desta sessão (fora da fase)

- Áudio do WhatsApp cortado: `/opt/claude-wpp/src/voice/prose-select.ts` `PROSE_CHAR_CAP = 600`.
  Pedro ainda não escolheu: dividir em até 4 áudios (recomendado) / subir o limite / cortar em fim de frase.
- XTTS: correção do "ponto" e R$ por extenso no commit e7f7471, entra no pod 3060 de 2026-10-13 07:00 —
  validar ouvindo. Protocolo "2026-4471-88" sai picotado (não tratado).
- llama.cpp novo (b10772) dá 400 "failed to parse grammar" no format-hint do converseai —
  relevante só se o pod for atualizado.
