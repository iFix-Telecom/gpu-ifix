# Runbook — validação de pod LLM (Vast.ai / llama.cpp)

Rodar **SEMPRE** após: criar instância, reboot, start pós-stop, ou suspeita de
degradação. Health + "diga pronto" **NÃO basta** — smoke funcional passa mesmo
com CUDA quebrado e inferência em CPU (incidente 2026-08-29: pod 27B respondeu
smoke ok a 1,4 tok/s, GPU parada, só detectado 1 dia depois).

## Checklist (ordem; qualquer ❌ = pod NÃO está válido)

1. **Health:**
   `curl -m 5 http://IP:PORT/health` → `{"status":"ok"}`.

2. **Config viva (ctx/slots):**
   `curl -m 10 http://IP:PORT/props` → conferir `n_ctx` por slot e `total_slots`
   contra o esperado (27B/30B 1×3090: 2 slots × 32768).

3. **Performance medida (o passo que pega CPU-fallback):**
   ```bash
   curl -s -m 120 http://IP:PORT/v1/chat/completions -H "Content-Type: application/json" \
     -d '{"model":"qwen","max_tokens":60,"temperature":0,"messages":[{"role":"user","content":"Conte de 1 a 20."}]}' \
     | python3 -c "import json,sys; t=json.load(sys.stdin)['timings']; print('gen:',round(t['predicted_per_second'],1),'| prefill:',round(t['prompt_per_second'],1))"
   ```
   Pisos (1×3090, Q4_K_M, KV q8_0 + FA):
   | Modelo | gen tok/s mín | prefill tok/s mín |
   |---|---|---|
   | Qwen3-30B-A3B (MoE) | ≥ 120 | ≥ 800 |
   | Qwen3.8-27B (denso) | ≥ 30 | ≥ 600 |
   Abaixo disso = CPU ou GPU degradada → passo 5.

4. **GPU do lado do host (API Vast):**
   `GET /api/v0/instances/` → na instância: `gpu_util` > 0 durante geração,
   `gpu_temp` > 0 SEMPRE. `gpu_temp: 0.0` = GPU não visível no container.

5. **Se falhou (diagnóstico nos logs):**
   `PUT /api/v0/instances/request_logs/{id}/` body `{"tail":"300"}` → baixar
   `result_url` e grep `-iE "cuda|gpu|no usable"`. Assinaturas conhecidas:
   - `ggml_cuda_init: failed to initialize CUDA: unknown error` +
     `warning: no usable GPU found, --gpu-layers option will be ignored`
     → driver do host quebrado; `-ngl` está certo mas é ignorado.
   - `CUDA error: unknown error ... ggml_cuda_graph_evaluate_and_capture`
     em runtime → crash que zumbifica o server (health ok, inferência morta).

6. **Remediação (nesta ordem):**
   1 reboot (`PUT /instances/reboot/{id}/` — rápido, sem re-download).
   Reboot não voltou GPU → **destruir e recriar em OUTRO host** (reboot
   reinicia o container, não o driver do host; anotar machine_id ruim e
   filtrar na busca de oferta).

## Gotchas herdados
- Download R2 (r2.dev) pode cair no meio (~7min) e o retry do llama recomeça em
  silêncio — sem log de progresso; ciclos de ~7-10min. Paciência ou corrida com
  2ª instância noutro host (matar a perdedora).
- Campo `entrypoint` da API Vast é ignorado; usar `runtype: args`.
- Presigned R2 dá 403 no downloader do llama (re-encoding) — usar r2.dev público.
- llama sem `-m` = router mode (ignora `-mu`).
