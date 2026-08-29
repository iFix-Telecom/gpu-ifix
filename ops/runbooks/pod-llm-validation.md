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

## Gates AUTOMÁTICOS (já no código — não repetir na mão)
- **Pods auto-provisionados pelo gateway — DOIS onstarts distintos:**
  primário = `gateway/internal/primary/onstart.go` (`primaryOnstartHead`,
  imagem converseai-primary-pod + supervisord); emergency = `gateway/internal/
  emerg/lifecycle.go` (`emergencyOnstartHead`, llama.cpp cru). Ambos têm
  preflight `nvidia-smi` logo após o `set -e` — GPU invisível → exit antes de
  baixar pesos; container morre, FSM re-provisiona noutro host. Deployado:
  emergency em develop-d8f73c7; primário no commit seguinte (2026-08-29). Gap conhecido: nvidia-smi ok mas CUDA init falhando
  em runtime não é pego (llama vira CPU) — exec PID1 impede vigiar log.
- **Pod compose completo (`pod/onstart.sh`, fluxo smoke/manual):** (a)
  preflight `nvidia-smi` aborta antes do compose; (b) pós-compose, vigia 120s
  os logs do service `llama` — `failed to initialize CUDA` / `no usable GPU
  found` → `compose down` + abort (recusa servir em CPU).
- **Pod 3060 unificado (`ops/vast-3060/unified3060.py` cmd_start):** após
  health, `gpu_temp <= 0` na API Vast → NÃO flipa o stack 38 + WhatsApp notify
  (fallbacks tier-1 servem melhor que pod em CPU).
- Pods manuais/teste (imagem crua, sem gates): validar na mão com o checklist
  acima — obrigatório o passo 3 (tok/s) e 4 (gpu_util/gpu_temp).

## Seleção de oferta (antes de criar)
- **Blocklist de machines (download R2 lento / driver ruim):** 134131, 43503,
  43488 (handoff 2026-08-26), 24039 (CUDA morto 2026-08-29), 84216. SEMPRE
  filtrar `machine_id not in BAD` na busca — esquecer a blocklist custou 20min
  num host 43503 em 2026-08-29.
- **Corrida de 2 instâncias só vale com IP público DIFERENTE:** machine_id
  distinto no MESMO IP = mesmo datacenter/uplink = mesmo gargalo (visto
  2026-08-29: 43503 e 84216 ambos em 137.175.76.24).

## Gotchas herdados
- **r2.dev derruba conexões longas (~7-10min) — em QUALQUER host** (2026-08-29:
  4 hosts/IPs distintos, mesmo `download failed: Failed to read connection`).
  O `-mu` do llama recomeça do zero a cada retry → loop. **Receita certa:
  `runtype: "ssh"` + campo `onstart` = script** (mesmo padrão do pod 3060):
  Vast substitui o entrypoint da imagem pelo bootstrap dela e roda o onstart
  via bash. **Throttle do r2.dev é POR CONEXÃO** (medido 2026-08-29: curl
  1 conexão = 8 MB/s; aria2 8 conexões = 34 MiB/s no mesmo host). Script:
  `apt-get install -y -qq aria2` → `aria2c -c -x8 -s8 -k1M --max-tries=0
  --retry-wait=3 --timeout=60 --checksum=sha-256=<sha> -o model.gguf URL`
  (resume por segmento + verifica sha ao fim; sha em `model.gguf.sha256` no
  R2) → `nohup /app/llama-server -m /root/model.gguf ... &`. Bônus: runtype
  ssh dá SSH pro pod (`ssh_host/ssh_port` na API) — dá pra ver progresso.
- Campo `entrypoint` da API Vast com `runtype: args` é **IGNORADO** nesta
  imagem (testado 2026-08-29: llama-server recebeu o `-c` → `error while
  handling argument "-c": stoi`). O `lifecycle.go` do gateway usa
  `Entrypoint: /bin/bash` com a imagem pinada `server-cuda-b9128` — não
  assumir que vale pra `server-cuda` latest.
- Presigned R2 dá 403 no downloader do llama (re-encoding) — usar r2.dev público.
- llama sem `-m` = router mode (ignora `-mu`).
