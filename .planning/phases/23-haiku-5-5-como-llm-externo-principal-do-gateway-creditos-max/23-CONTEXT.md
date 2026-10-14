# Phase 23: Haiku 5.5 como LLM externo principal do gateway - Context

**Gathered:** 2026-10-14
**Status:** Ready for planning
**Source:** ROADMAP + 23-HANDOFF.md + decisões do Pedro sobre as perguntas abertas do 23-RESEARCH.md

<decisions>
## Implementation Decisions (LOCKED)

### Cadeia (decisão 2026-10-13, não rediscutir)
- ① Haiku 5.5 direto na Anthropic (créditos API das 2–3 contas Max) → ② `anthropic/claude-haiku-5.5` no OpenRouter (provedor Anthropic preferido) → ③ DeepSeek v4.1 Flash no OpenRouter.
- `maestro-summary` intocado.
- RES-08: tenant sensível nunca sai para externo.
- Chaves Max ainda NÃO existem: ① deve ser desenvolvido/testado com fakes; ativa sozinho quando chaves forem configuradas. ② segue servindo até lá.

### Prefill (resposta à pergunta 1 do research)
- Gateway anexa mensagem de usuário neutra no fim quando a conversa termina em fala do assistant, SÓ para alvos Anthropic (① e ②). Não depender do Maestro.

### Chave esgotada (pergunta 2)
- Cada chave guarda o dia de renovação do ciclo da sua conta Max; esgotada fica marcada até essa data, com re-teste periódico (ex.: 1×/6h) que libera antes se voltar saldo. NÃO usar "até dia 1º".
- Ordem fixa de uso (primeira com saldo), não rodízio por chamada (cache é por org).

### Effort (pergunta 3)
- High por padrão quando o cliente não especifica. Se o request pedir explicitamente off/low/medium, RESPEITAR. Override por tenant disponível (tenant > padrão). (Isto substitui o "cliente ignorado" da tabela de testes do research.)

### Painel Economia (pergunta 4)
- Custo do passo ① aparece a preço de lista, separado/marcado como "coberto por créditos"; custo real em caixa = 0.

### Claude's Discretion
- Desenho do tradutor OpenAI↔Messages (research recomenda upstream próprio sobre httputil.ReverseProxy, sem SDK como transporte).
- Cascata por status HTTP (429/5xx/529) em chat.
- Provedor TTS: research propõe OpenRouter TTS; escolha final de voz via teste cego de ouvido com Pedro (checkpoint humano).
- Histórico de custo da 3060: só daqui para frente, a menos que a API Vast entregue histórico fácil.

### Pendências do Pedro (não bloqueiam o plano)
- Gerar chaves API nas contas Max (vincular org do Console).
- Confirmar termos suplementares dos créditos para uso em gateway da empresa (antes de ligar ① em prod).
</decisions>

<canonical_refs>
- `.planning/phases/23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max/23-RESEARCH.md`
- `.planning/phases/23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max/23-HANDOFF.md`
- `.planning/phases/23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max/23-model-aliases-pre-haiku-20261013.json`
- `gateway/internal/proxy/openrouter_director.go`
</canonical_refs>
