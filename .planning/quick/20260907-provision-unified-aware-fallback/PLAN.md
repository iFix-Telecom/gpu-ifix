---
quick_id: 260907-pfa
slug: provision-unified-aware-fallback
date: 2026-09-07
status: in-progress
---

# Quick: unified3060.py vira up→destroy diário (provision unified-aware)

## Contexto

2026-09-07 07:00: GPU da machine 148305 alugada por terceiro → pod 49644867 preso
na fila (`success:false resources_unavailable` que o cmd_start nem checava).
**Decisão Pedro (mensagem 2026-09-07): NÃO quer stop/start — não pagar instância
parada esperando máquina; locação provada simples/rápida/disponível. Modelo =
up→destroy diário** (07h provisiona fresco, 20h destrói; custo noturno zero,
nunca preso a máquina ocupada). Viável agora porque o install do Infinity ficou
determinístico (freeze pinado, quick 260902-be9).

## Tarefas

1. `ops/vast-3060/onstart-unified.sh` versionado: pip do Infinity PINADO
   (`--no-deps --extra-index-url cu121 -r /root/infinity-freeze.txt`); sem
   freeze no disco, adia Infinity (provisioner scp + relança).
2. Reescrever `unified3060.py`:
   - `start` (timer 07:00) → `provision()`: oferta não-CN + machine_avoid
     (state.json compartilhado com vast3060), create disco 40G + onstart do
     arquivo, scp freeze, health speaches 20min, modelos speaches (2xx),
     Infinity 30min, gate GPU, validação direta (STT/TTS/embed 1024/rerank),
     disk-guard, flip 4 envs stack 38, edge validate, destrói instância
     anterior se existir, persiste instance_id no state.json, WhatsApp.
   - `stop` (timer 20:00) → DESTRÓI instância do state (não mais "stopped").
   - `status`/`disk` lêem instance_id do state.json (nada hardcoded).
3. Deploy /opt + rodar provision AGORA (destrói 49644867 enfileirado após
   validar o novo).
4. Commit + STATE.md + memória.

## Validação

- Provision E2E: edge STT/TTS 200, embed 1024, rerank 200, gpu_temp>0
- `status` mostra instância nova via state.json
- stop de teste NÃO rodado hoje (destroy às 20h pelo timer; validação = code review)
