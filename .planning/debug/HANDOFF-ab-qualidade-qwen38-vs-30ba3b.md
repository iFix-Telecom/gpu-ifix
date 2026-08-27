# HANDOFF — A/B de QUALIDADE: Qwen3.8-27B vs Qwen3-30B-A3B (próxima sessão)

**Pedido Pedro 2026-08-27:** rodar A/B de qualidade antes de decidir se o 27B vira
primário ou alias. Bench de PERFORMANCE já feito (memória `qwen38-27b-bench-3090`):
1×3090 24GB carrega 2×32k ctx, ~42 tok/s (4,5× mais lento que o MoE atual). Falta
só QUALIDADE nos casos reais ifix.

## Setup (tudo pronto)
- GGUF validado no R2: `ai-gateway-weights/qwen3.8-27b-UD-Q4_K_M/v1.0.0/model.gguf`
  (sha256 322e194f...; r2.dev público habilitado: `https://pub-9fa93abf9f4e411a89bfb52ff3e3135a.r2.dev/qwen3.8-27b-UD-Q4_K_M/v1.0.0/model.gguf`)
- Receita pod de teste 1×3090 (payload `bench1x-65k.json` da sessão 2026-08-26, recriável):
  imagem `ghcr.io/ggml-org/llama.cpp:server-cuda`, runtype args, SEM entrypoint override
  (API Vast ignora o campo!), args: `-m /root/model.gguf -mu <r2.dev url> --host 0.0.0.0
  --port 8000 -ngl 999 -np 2 -c 65536 -ctk q8_0 -ctv q8_0 -fa on --jinja`,
  env `{"-p 8000:8000":"1"}`, disk 40. Hosts BG/CZ baixam R2 a ~90MB/s (evitar
  machines 134131/43503/43488). Boot→healthy ~6-8min.
- Modelo B (baseline) = pod primário PROD (Qwen3-30B-A3B, sobe 9-17h BRT — usar o
  próprio, via gateway com tenant uat10, ou direto :18000 pelo worker-vm).

## Protocolo A/B proposto
1. Coletar 10-20 PROMPTS REAIS por caso de uso (fontes: logs do gateway/billing por
   tenant — ia-kanban, converseai-classifier, converseai-format-hint, chat-ifix/copilot
   Maestro; ou n8n workflows). NÃO inventar prompts sintéticos.
2. Rodar cada prompt nos DOIS modelos (27B: testar com reasoning default E com
   thinking desligado — validar HIPÓTESE de que template kwarg controla; Qwen3 usava
   enable_thinking).
3. Julgar cego (LLM-judge via openrouter + spot-check manual Pedro): correção,
   aderência a formato (JSON dos classifiers!), pt-BR, alucinação.
4. Entregável: tabela por caso de uso + recomendação (primário / alias / descartar).

## Decisão pendente que o resultado alimenta
- 27B ganha claro → discutir troca de primário (aceitando 42 tok/s) OU alias `qwen-27b`
  em pod dedicado sob demanda pra rotas de qualidade.
- Empate/perda → mantém 30B-A3B, arquiva o GGUF no R2 (já pago, 15GB).

## Gotchas herdados (não repetir)
- aria2 -x16 corrompeu GGUF (mesmo size, sha errado, output lixo) — sempre validar sha.
- llama-server novo: sem `-m` = router mode (ignora -mu). Presigned R2 dá 403 (re-encoding).
- 2×3090 não acelera geração (layer split) — irrelevante pro A/B.
