# RESULTADO — A/B/C: Haiku 5.5 (low/medium/high) vs Qwen3-30B-A3B (pod) vs DeepSeek v4.1 Flash (2026-10-13)

Card ClickUp 86akuaa73. Mesmas 37 amostras reais de produção do A/B do Bonsai
(`RESULTADO-ab-bonsai2-vs-30ba3b.md`): 12 analise-transcr-voip, 10 ia-kanban, 15 tool calling converseai.

## Braços

| Braço | Modelo | Onde |
|---|---|---|
| A | Qwen3-30B-A3B Q4_K_M | pod 3090 (rodada de 2026-10-13, flags de prod). Format-hint (4): resposta real de prod do 30B (llama novo falhou) |
| B | deepseek/deepseek-v4.1-flash:exacto | OpenRouter, prefs atuais do gateway (atendeu InferenceNet/Morph) |
| C1/C2/C3 | anthropic/claude-haiku-5.5 effort low/medium/high | OpenRouter, provider Anthropic fixo |

Haiku 5.5: removidos `temperature`/`top_p`/`frequency_penalty`/`presence_penalty` (dão 400). `tool_choice`
forçado FUNCIONOU sem adaptação. 37/37 OK em todos os braços.

Juiz cego: `anthropic/claude-sonnet-5.5`, as 5 respostas embaralhadas por amostra, nota 0–10 por critério + ranking.

## Resultado (37 amostras)

| Braço | 1º lugar | Posição média (1=melhor) | Correção | Formato | pt-BR | Alucinação (10=nenhuma) |
|---|---|---|---|---|---|---|
| A Qwen 30B (pod) | 6 | 3,46 | 7,1 | 8,2 | 9,3 | 8,3 |
| B DeepSeek Flash | 7 | 3,14 | 8,5 | 9,1 | 9,6 | 8,7 |
| C1 Haiku low | 9 | 2,86 | 8,7 | 9,1 | 9,6 | 9,0 |
| C2 Haiku medium | 2 | 3,05 | 8,9 | 9,1 | 9,6 | 9,1 |
| **C3 Haiku high** | **13** | **2,49** | **9,0** | 9,1 | 9,6 | **9,3** |

Por caso (correção): transcr A 5,9 · B 7,2 · C1 7,8 · C2 7,9 · C3 7,8 — kanban A 6,9 · B 9,3 · C 9,5–9,6 —
tools A 8,1 · B 8,9 · C1 8,9 · C2 9,3 · C3 9,5.

## Latência mediana e custo das 37 amostras

| Braço | Latência mediana | Custo (37 req) | Tokens de raciocínio |
|---|---|---|---|
| A | 1,2 s (local no pod, sem rede — não comparável) | aluguel do pod | — |
| B | 3,4 s | US$0,016 | 3.498 |
| C1 | 3,4 s | US$0,029 | 5.103 |
| C2 | 3,5 s | US$0,032 | 10.705 |
| C3 | 3,9 s | US$0,036 | 19.547 |

## Conclusão

- O 30B do pod ficou em ÚLTIMO em qualidade (pior em correção nos 3 casos). Junto com o custo
  (pod US$23–34/mês para ~US$2–6/mês de tokens equivalentes), não se sustenta.
- Haiku 5.5 high = melhor qualidade geral; custa ~2,2× o DeepSeek por requisição, ainda centavos.
- Ordem proposta pelo Pedro validada: Anthropic (créditos Max) → Haiku OpenRouter → DeepSeek Flash.
