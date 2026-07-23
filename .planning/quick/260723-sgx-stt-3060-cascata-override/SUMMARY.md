---
quick_id: 260723-sgx
slug: stt-3060-cascata-override
date: 2026-07-23
status: complete
commits:
  - 2a53ab1 fix(gateway/stt): cascata retryable no override emergency_pod_stt
  - 0bc819b fix(gateway/pod): threshold whisper 22000->30000 — primary vira LLM-only
  - a585956 docs(quick): plan
deployed: develop-a585956@sha256:43c23ddf (stack 38, 2026-07-23 ~20:45 -03)
---

# SUMMARY — STT no pod 3060 dedicado + cascata no override

## O que mudou

1. **`NewDynamicOverrideSTTProxy` ganhou `sttRetryableStatusInterceptor`**
   (dynamic_override.go). O fix da Phase 22 tinha pousado só no construtor genérico;
   o irmão STT-aware (registrado como `emergency_pod_stt`) commitava 500 do pod cru
   pro cliente. Agora 404/408/425/429/5xx levantam `errUpstreamRetryable` → cascata.
   +2 testes (500→502 envelope standalone; 200 passa intocado).

2. **`WHISPER_GPU_THRESHOLD_MIB` 22000→30000** (onstart.go). 1×3090 deixa de
   qualificar pra whisper cuda → pod reporta `whisper_device=cpu` → gateway não
   registra override STT → dispatcher roteia pro `local-stt` estático =
   **pod 3060 dedicado** (Vast 45646652, machine 143493, $0,0348/h,
   `UPSTREAM_STT_URL=http://86.125.131.3:40046` — env flipado no stack 38 antes).

## Validação live (2026-07-23 ~20:50 -03)

- Gravação real 17,3min (NB 21103252) que 500ava: **200 em 47s** via edge,
  dispatch `upstream=local-stt`.
- Pod primary re-provisionado (lifecycle novo, inst 45649533, machine 33953):
  `:9100/whisper_device` = **`{"whisper_device":"cpu"}`** ✔
- Com pod UP: STT curto 200 em 1,6s **ainda via local-stt** (sem regressão de
  override) ✔ LLM `qwen` → `emergency_pod_llm`, resposta do `model.gguf` ✔

## Segue pendente (fora de escopo, anotado)

- Truncamento de body LLM >128KiB → 502 no path do pod (bug separado).
- Bump `--ctx-size` do llama (aproveitar os ~3,2GB liberados) — compose da imagem
  do pod, task própria.
- Funil n8n das 134k gravações/mês (hoje só voice notes WhatsApp fluem).
- IF quebrado no n8n externo do Pedro (`$json.content.parts[0].text` → `$json.text`).
