---
quick_id: 260907-pfa
slug: provision-unified-aware-fallback
date: 2026-09-07
status: complete
---

# SUMMARY — unified3060.py vira up→destroy diário (provision unified-aware)

## Resultado

`unified3060.py` REESCRITO: modelo **up→destroy diário** (ordem Pedro 2026-09-07 —
"não quero agendar/pagar instância parada esperando máquina; locação é rápida").
- **start (07:00):** provisiona pod FRESCO — oferta não-CN + machine_avoid, disco
  40G, onstart AUTO-CONTIDO (freeze pinado + disk-guard embutidos no b64),
  health speaches/Infinity, installs speaches resilientes (retry+poll), gate GPU,
  validação direta (STT/TTS/embed 1024/rerank), flip 4 envs stack 38, edge
  validate, destrói instância anterior, persiste instance_id no state.json.
  Suporta `start <id>` = resume em instância existente.
- **stop (20:00):** DESTRÓI (custo noturno zero). TimeoutStartSec 2400→5400.
- instance_id no state.json (compartilhado com legado vast3060) — nada hardcoded.

Provision E2E validado HOJE: pod **50153689** (machine 146849, KR, $0,0289/h,
125.240.239.50, 8000→35570, 7998→35526, gpu 56°C, disco 46%). Antigos destruídos:
49644867 (preso na fila da machine ocupada) + órfãs 50151819/50152334.

## Gatilho

07:00 de 2026-09-07: GPU da machine 148305 alugada por terceiro de madrugada →
PUT running = 200 com `success:false resources_unavailable` (cmd_start antigo nem
checava o corpo) → manhã sem pod; TTS caiu pro piper CPU, STT pro externo pago.
Exatamente o risco residual do modelo stop/start.

## Gotchas novos (válidos p/ qualquer provision Vast)

1. **SSH key NÃO injeta em instância nova** via conta (Permission denied
   persistente); attach via API `POST /instances/{id}/ssh/` retorna success mas
   sshd não reconhece sem reboot. Fix estrutural: onstart auto-contido
   (heredocs), SSH só best-effort de diagnóstico. (Machine 146849 aceitou SSH
   normalmente — comportamento varia por máquina/imagem.)
2. **POST /v1/models do speaches segura a conexão** durante o download → NAT
   corta → HTTP 0/500 com download seguindo server-side. Fix: `install_model`
   com retry + poll de GET /v1/models.
3. ssh_host/ssh_port da API mudam entre polls da mesma instância (proxy remapeia).

## Pendências

- Timers seguem 07:00/20:00 seg-dom. Primeira manhã autônoma = 2026-09-08.
- vast3060.py legado mantido como biblioteca (helpers importados); timers
  vast-3060-* continuam desabilitados.
