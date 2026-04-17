# Smoke Test

Valida POD-07 (D-17) e aplica os gates de promoção de imagem (D-19).

## Uso rápido

```bash
pip install -r pod/smoke/requirements.txt

# Pod rodando localmente / em Vast.ai — passe o IP:porta base
python pod/smoke/smoke.py --target http://<pod-ip> --out smoke-report.json

# Modo rápido para dev (Whisper 30s, 1 chat só) — NÃO valida os gates completos
python pod/smoke/smoke.py --target http://localhost --fast --out smoke-report.dev.json
```

## Carga (D-17)

| Trabalho | Paralelo? | Descrição |
|---|---|---|
| 2 chats de ~8000 tokens | sim (concurrent) | streaming SSE, espera até 500 tokens de completion |
| 1 Whisper de 8 min | sim | WAV sintético gerado em memória via `fixtures/__gen_audio.py` |
| 1 batch de 10 embeddings | sim | `BAAI/bge-m3` dense |
| DCGM scrape @ 1 Hz | background | coleta `DCGM_FI_DEV_FB_USED` para VRAM peak/p95 |
| Tool-call validation | após workload | `get_weather(location=São Paulo)` — D-15 |

## Gates D-19 (bloqueantes)

| Gate | Limite | Exit code |
|---|---|---|
| `vram_peak_gb` | ≤ 21.0 | 2 |
| `tool_call_valid` | == true | 3 |
| `errors` sem OOM/CUDA | true | 4 |
| `llm_p95_ttft_ms` | ≤ 3000 | 5 |
| Múltiplos falharam | — | 6 |
| Script crash | — | 1 |
| Todos passaram | — | 0 |

## Output

`smoke-report.json` — validado contra `pod/smoke/report-schema.json` (JSON Schema 2020-12).

Arquivado pelo CI (plan 08 smoke.yml) como artifact + copiado para `.planning/phases/01-gpu-pod-image-smoke-test/baseline/smoke-report-<sha>.json` para alimentar Phase 5 (D-20).

## Dependência com outros planos

- Depende de plan 02 (Qwen template) para `tool_call_valid` passar
- Depende de plan 03 (docker-compose + flags llama) para `vram_peak_gb`
- Depende de plan 04 (health-bridge) só indiretamente — smoke fala direto com os servidores
- Consumido por plan 08 (.github/workflows/smoke.yml)
