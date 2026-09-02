---
quick_id: 260902-be9
slug: recriar-pod-3060-disco-40g
date: 2026-09-02
status: in-progress
---

# Quick: Recriar pod 3060 unificado com disco 40G

## Contexto

Pod unificado 48611358 (STT+TTS+rerank+embed) roda com disco 25G cronicamente a 90%
(medido 2026-09-02: 23G/25G; breakdown = HF cache 9,6G + speaches 8,1G + Infinity
venv 6,2G + base 3,7G — é o stack instalado, não lixo diário). Pedro pediu pod novo
diário; evidência mostrou que não resolveria (pod fresco volta a ~23G no dia 1).
Decisão (AskUserQuestion): **recriar 1x com disco 40G**, manter stop/start noturno.

## Tarefas

1. Capturar config de create da instância atual via API Vast (onstart, runtype, env, image).
2. Provisionar pod novo: offer 3060 (query do vast3060.py, cap 0.035 escalando até 2x),
   disk=40, image speaches 0.9.0-rc.3-cuda-12.6.3, onstart dual (b64 wrapper capturado
   do pod), portas 8000+7998.
3. Instalar modelos speaches (faster-whisper-large-v3 + Kokoro) via POST /v1/models.
4. Esperar health 8000/7998 + gate GPU (gpu_temp>0) + validação direta
   (STT, TTS, embed bge-m3 dims=1024, rerank).
5. Plantar disk-guard no pod novo.
6. Flipar 4 envs do stack 38 (Portainer) → validar via edge.
7. Destruir pod antigo 48611358.
8. Atualizar `INSTANCE` em ops/vast-3060/unified3060.py (repo) + copiar pra /opt.
9. Commit + STATE.md.

## Validação

- `curl edge /v1/audio/transcriptions` 200 (whisper) e TTS 200
- embed via pod: len(embedding)==1024, model bge-m3
- rerank via pod: 200
- gpu_temp > 0 na API Vast
- disco novo ~60% pós-install
