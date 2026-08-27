# RESULTADO — A/B de QUALIDADE: Qwen3.8-27B vs Qwen3-30B-A3B (2026-08-27)

Executado conforme `HANDOFF-ab-qualidade-qwen38-vs-30ba3b.md`. Prompts 100% reais
(nenhum sintético). Judge cego LLM (claude-sonnet-4.5 via OpenRouter, ordem
aleatória seed 42) + validação programática de schema.

## Setup executado

- **Braço A (27B):** pod Vast 48882107, 1×3090 Ontario/CA $0,142/h, llama.cpp
  server-cuda, `-np 2 -c 65536 -ctk q8_0 -ctv q8_0 -fa on --jinja`, GGUF do R2
  (r2.dev, download+boot ~6min). Destruído ao fim (custo total ~US$0,30).
- **Braço B (30B-A3B):** pod primário PROD (lifecycle-412), direto na porta
  (135.19.209.101:10047), payloads idênticos aos de produção.
- **FATO validado:** `chat_template_kwargs: {enable_thinking: false}` DESLIGA o
  thinking do 27B (reasoning=None, 4 tokens de output no smoke). A hipótese do
  handoff está confirmada.

## Amostras (24, todas reais)

| Caso | n | Fonte |
|------|---|-------|
| caso1 — analise-transcr-voip (maior volume: 6k req/7d) | 14 | `bd_sincron_nextbilling.chamadas_sincronizadas.transcricao_texto` + prompt real de `prompts_transcricao_ia` (12 padrão + 2 personalizados: clientes 627 e 1127) |
| caso2 — ia-kanban (540 req/7d) | 10 | `bd_chatifix.ia_b_decisions.prompt_text` (mensagens reais reconstruídas; header/trailer W8 removidos) |
| chat-ifix/Maestro copilot | 0 | **Limitação:** Maestro não persiste payloads e teve ~0 tráfego copilot nas 48h — sem fonte real. Function-calling NÃO coberto neste A/B. |

## Resultados

### Veredito cego (27B thinking-off vs 30B, 24 pares)

| | 27B vence | 30B vence | empate |
|---|---|---|---|
| caso1 (n=14) | 3 | 4 | 7 |
| caso2 (n=10) | 2 | 2 | 6 |
| **total** | **5** | **6** | **13** |

### Scores médios (0-10)

| Critério | caso1 27B | caso1 30B | caso2 27B | caso2 30B |
|---|---|---|---|---|
| correção | 6,2 | **7,4** | **8,3** | 7,8 |
| formato | 7,3 | **9,0** | 9,8 | 9,8 |
| pt-BR | 9,9 | 9,7 | 9,9 | 9,8 |
| alucinação (10=zero) | 7,6 | 7,9 | **8,9** | 8,2 |

### Achados qualitativos

1. **27B tem modo de falha grave no caso1:** em 3/14 amostras respondeu "Áudio
   sem diálogo" / ignorou o diálogo existente com transcrição válida no prompt
   (verificado: payload correto, falha genuína do modelo). O 30B nunca fez isso.
2. **27B ganhou os DOIS prompts personalizados longos** (system 4,7k-5,6k chars,
   transcrição 2,7k-4,4k): análise mais profunda, menos alucinação de dados
   (30B inventou cidade/ticket médio numa delas). Nicho real do 27B = prompt
   complexo + contexto longo.
3. **caso2 (JSON estrito):** 30B 10/10 schema válido; 27B 9/10 (1 falha:
   `reasoning` estourou o max 500 chars — verbosidade).
4. **Latência (por request, sequencial):** caso1 mediana 0,7s (27B) vs 2,6s
   (30B) em respostas curtas, mas máx 39s (27B) vs 12s (30B) nas longas;
   caso2 4,2s vs 1,4s. Em respostas longas o 27B é ~4-5× mais lento (consistente
   com bench 42 vs 190 tok/s).
5. **Thinking ON (default) é inviável em produção:** caso1 com max_tokens 1536
   estoura o budget só em thinking (mediana 36,6s, output cortado); caso2 mediana
   10,2s máx 89s. Uso do 27B exigiria `enable_thinking:false` sempre.
6. **Risco operacional:** llama-server **crashou 1×** (SIGSEGV em `llama_decode`)
   durante o braço thinking-on e auto-reiniciou (~2min indisponível). Não
   reproduzido no retry. Com 30B nunca observado.

## Recomendação

**Manter Qwen3-30B-A3B como primário. NÃO criar alias/pod dedicado 27B agora.
Arquivar o GGUF no R2 (já pago, 15GB).**

- Handoff previa: "empate/perda → mantém 30B-A3B, arquiva GGUF". Resultado =
  empate estatístico (13/24 empates), com 30B melhor no caso de MAIOR volume
  (analise-transcr: correção 7,4 vs 6,2 + zero falhas graves) e no formato JSON.
- O único nicho onde o 27B ganhou claro (prompts personalizados longos) tem
  volume baixo demais hoje p/ justificar +$0,14/h de pod dedicado + 4,5× a
  latência + o risco de crash observado.
- Se o volume de prompts personalizados complexos crescer, reavaliar alias
  `qwen-27b` sob demanda — os dados deste A/B já indicam vantagem ali.

## Artefatos (efêmeros — scratchpad da sessão)

`scratchpad/ab/`: `caso1-analise-transcr.json`, `caso2-ia-kanban.json` (amostras
reais — contêm dados de clientes, NÃO commitar), `results-{27b-thinkoff,
27b-default,27b-default-retry,30b-baseline}.json`, `judge-27b-thinkoff-vs-30b-baseline.json`,
`runner.py`, `judge.py`. Pod destruído; recriável pela receita do handoff.

## Pendência residual

- Cobertura de **function-calling** (copilot Maestro) ficou fora — sem payloads
  reais persistidos. Se virar decisão relevante, capturar payloads no Maestro
  (log temporário) e repetir só esse caso.
