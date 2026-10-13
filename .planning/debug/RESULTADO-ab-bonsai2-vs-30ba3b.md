# RESULTADO — A/B: Bonsai 2 27B (ternário) vs Qwen3-30B-A3B (2026-10-13)

Card ClickUp 86aku9fqu. Mesmo método do A/B de 2026-08-27 (`RESULTADO-ab-qualidade-qwen38-vs-30ba3b.md`),
agora com payloads EXATOS de produção tirados do `audit_log_content` do gateway e um caso novo de
tool calling (converseai).

## Setup

- Pod Vast 55647364, 1×RTX 3090 24 GB (US), destruído ao fim. Mesmo binário para os dois braços:
  fork `PrismML-Eng/llama.cpp` release `prism-b10772-e8fd209` (linux-cuda-12.8). O llama.cpp normal não
  carrega os GGUF ternários.
- **Bonsai:** `prism-ml/Ternary-Bonsai-2-27B-gguf` `PQ2_0` (7,2 GB, sha256 3907dc16…),
  `-ngl 99 -fa on -c 65536 -np 2 --jinja`, **KV f16**, `chat_template_kwargs.enable_thinking=false`.
- **30B-A3B:** GGUF de produção (R2, sha256 6c997b8a…), flags de produção
  (`-c 65536 -np 2 -ctk q8_0 -ctv q8_0 -fa on --jinja`), payloads sem alteração.
- Juiz cego: `anthropic/claude-sonnet-5.5` via OpenRouter, temperatura 0, ordem A/B sorteada (seed 42).

## Amostras (37 reais, últimos 7–14 dias)

| Caso | n | Fonte |
|---|---|---|
| transcr — analise-transcr-voip | 12 (4 longas >6k chars) | payload de prod do tenant |
| kanban — ia-kanban (JSON estrito) | 10 | payload de prod do tenant |
| tools — converseai classifier / format-hint / goal | 15 | payload de prod (tool_choice forçado) |

4 format-hint ficaram fora do julgamento: os DOIS modelos deram 400 `failed to parse grammar` (schema
grande demais para o gerador de gramática deste build). Em prod (build mais antigo) o 30B atende 18/18 —
é diferença de versão do llama.cpp, não do modelo. **Atenção para upgrades do pod.**

## Resultado (33 pares julgados)

| Caso | Bonsai vence | 30B vence | Empate |
|---|---|---|---|
| transcr (12) | 5 | 2 | 5 |
| kanban (10) | 2 | 6 | 2 |
| tools (11) | 6 | 1 | 4 |
| **Total** | **13** | **9** | **11** |

Notas médias 0–10 (Bonsai / 30B): correção 8,2 / 7,2 · formato 8,9 / 8,5 · pt-BR 8,9 / 9,2 · alucinação 8,7 / 8,2.

- **transcr:** Bonsai inventou menos (30B inventou e-mail, vagas, cidade em transcr_08; tratou reunião
  interna como venda em transcr_09).
- **kanban:** Bonsai pior — usou ação inexistente (`criar_kanban_item`), envolveu JSON em bloco markdown,
  reasoning com erros de digitação. 30B mais conservador e limpo.
- **tools:** Bonsai melhor em extração de dados (normaliza horário/data, mantém e-mail coletado).

## Velocidade e memória (mesma 3090)

| | Bonsai PQ2_0 | Bonsai PTQ1_0 | 30B-A3B |
|---|---|---|---|
| Geração (tok/s, mediana) | 57–68 | ~71 | 110–151 |
| Latência mediana tools / kanban | 3,3 s / 3,4 s | — | 1,2 s / 1,3 s |
| VRAM com 64k ctx | 11,4 GB (KV f16) · 9,5 GB (KV q8_0) | 10,3 GB (KV f16) | 21,2 GB |

## Problemas encontrados (FATO, reproduzidos)

1. **KV q8_0 quebra o Bonsai:** com `-ctk/-ctv q8_0` (config de prod) o modelo degenera em "//////…"
   a partir de ~3k tokens de prompt (6/37 amostras). Com KV f16 some. Obrigatório usar f16.
2. **Template recusa 2+ mensagens system:** ia-kanban manda 2 → HTTP 500 `System message must be at the
   beginning`. Contornado juntando as system (o gateway teria de fazer o mesmo).
3. **Thinking ligado por padrão:** precisa `enable_thinking:false` em toda request (o converseai não manda).
4. Requer o fork do llama.cpp (imagem do pod mudaria).

## Conclusão

Qualidade: Bonsai empata/ganha (13×9×11), ganha em análise e tool calling, perde em JSON do ia-kanban.
Velocidade: 30B é ~2,2× mais rápido. Memória: Bonsai usa metade (cabe em GPU de 12 GB).

HIPÓTESE (não testada): Bonsai num pod 3060 12 GB (~US$0,06/h) como substituto barato do 3090
(~US$0,15–0,23/h), aceitando ~2× mais latência. Validar: rodar o mesmo A/B numa 3060 e medir tok/s.
