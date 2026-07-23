---
quick_id: 260723-sgx
slug: stt-3060-cascata-override
date: 2026-07-23
status: in-progress
---

# Quick: STT no pod 3060 dedicado + cascata no override emergency_pod_stt

## Problema (evidência live 2026-07-23)

1. Gravação real de 17,3min (NB 21103252) → 500 no pod 3090 compartilhado (CUDA OOM,
   330MiB VRAM livre; Qwen 18GB + whisper 3,2GB co-residentes). Mesma gravação na 3060
   dedicada (Vast 45646652, 86.125.131.3:40046, $0,0348/h, vetada) → 200 em 42s, RTF 25×.
2. O 500 vaza CRU pro cliente: dispatcher roteia STT pro `emergency_pod_stt`
   (`NewDynamicOverrideSTTProxy`, main.go:704) que NÃO tem `sttRetryableStatusInterceptor`.
   O fix da Phase 22 foi aplicado só no genérico `NewDynamicOverrideProxy`
   (dynamic_override.go:58-60) — o irmão STT-aware ficou de fora. Sem cascata
   gemini-stt → openai-whisper; n8n re-tenta no mesmo pod.

## Fix

### Task 1 — cascata no NewDynamicOverrideSTTProxy
- `gateway/internal/proxy/dynamic_override.go:142`: prepend
  `sttRetryableStatusInterceptor{}` aos interceptors (espelho exato das linhas 54-60
  do genérico; retryable roda ANTES do usageInterceptor → resposta falha nunca billa).
- Teste em `stt_model_rewrite_test.go` (padrão existente): upstream 500 →
  ModifyResponse levanta `errUpstreamRetryable` → ErrorHandler suprime write +
  fallthrough (mesmo contrato do NewAudioProxy). RES-08 preservado (gate é no
  dispatcher, não tocado).

### Task 2 — primary vira LLM-only (threshold)
- `gateway/internal/primary/onstart.go:242`: `WHISPER_GPU_THRESHOLD_MIB=22000` → `30000`.
  3090 (24576) deixa de qualificar → onstart exporta `WHISPER_DEVICE=cpu` → report
  `:9100/whisper_device=cpu` → gateway NÃO registra override stt (lifecycle.go:135 —
  só "cuda" gateia) → dispatcher usa `local-stt` estático = 3060
  (`UPSTREAM_STT_URL=http://86.125.131.3:40046`, já flipado no stack 38, probe ok).
- Atualizar comentário stale (linhas 221-225): Phase 21 baixou 30000→22000 pra STT
  in-house na 1×3090; quick 260723-sgx REVERTE — STT movido pra pod 3060 dedicado
  (OOM em áudio longo no card compartilhado), 3090 dedicado ao Qwen.
- Multi-GPU (≥30000, ex 2×3090) continua qualificando pra cuda — comportamento intacto.

### Task 3 — build/test/commit/push
- `go build ./... && go test ./internal/proxy/... ./internal/primary/...`
- 1 commit por task (`fix(gateway/stt): ...`), push develop → CI builda
  `ghcr.io/ifixtelecom/ifix-ai-gateway:develop-<sha>`.

### Task 4 — deploy + bounce + validação live
- PUT stack 38 (Portainer) trocando a image do gateway pra `develop-<sha>` (PullImage).
- Bounce primary: `gatewayctl primary force-down` + `force-up` (re-roda onstart novo;
  pod_config: schedule_disabled=true, force_machine_id=33953 pinado).
- Validar: (a) log `dispatching upstream=local-stt`; (b) 17min via edge
  (key transcricao-voip, model=whisper) → 200; (c) áudio curto → 200; (d) LLM qwen → 200
  do pod; (e) upstreams list: local-stt ok.

## Fora de escopo
- Bump de --ctx-size do llama (compose da imagem do pod, não gateway) — task futura.
- Truncamento LLM body >128KiB → 502 (bug separado, GSD próprio).
- Funil n8n 134k gravações/mês.
