---
quick_id: 260724-ktg
slug: 3060-auto-migrate
date: 2026-07-24
status: in-progress
---

# Quick: auto-migração do pod STT/TTS 3060

## Problema

Pod 3060 (Vast 45646652, machine 143493) é agendado 7-22 via systemd timers com
`ensure` a cada 15min. Stop/start prende no MESMO host (disco local, não migra):
se terceiro aluga a GPU, o ensure retenta infinito naquela máquina — degradação
silenciosa (STT→gemini pago, TTS 503). Aconteceu 2026-07-24 07:00-09:07.

## Solução

Reescrever o bash `vast-3060-sched.sh` como **Python stdlib único**
(`ops/vast-3060/vast3060.py` no repo gpu-ifix), subcomandos:

- `ensure` — dentro da janela 7-21h59: running+healthy → zera fail_count, sai.
  Senão: tenta start; falha → fail_count++. **fail_count ≥ 8 (~2h) → migrate()**.
- `stop` — 22:00, para a instância do state.
- `migrate` — (1) busca offer 3060/3060Ti/3080, ≤$0,035, rel≥0,97, VRAM≥8GB,
  inet≥100, excluindo machine atual + `machine_avoid` do state; (2) cria com
  speaches 0.9.0-rc.3-cuda; (3) espera running+health (~8min cap); (4) instala
  whisper-large-v3 + Kokoro-82M-ONNX (2 POSTs); (5) valida STT+TTS direto (200);
  (6) PUT stack 38 Portainer: `UPSTREAM_STT_URL` + `UPSTREAM_TTS_KOKORO_URL` →
  novo IP:porta (sem pull); (7) valida via EDGE (STT com key transcricao-voip +
  TTS com key chat-ifix = prova gateway→pod novo); (8) destrói instância velha;
  (9) grava state novo; (10) notifica Pedro no WhatsApp (Dinastia
  `POST /chat/send/text`, header `token:`). Falha em qualquer passo pré-flip →
  destrói a nova, mantém a velha, notifica erro (estado intacto).
- `status` — dump do state + situação live.

**State**: `/var/lib/vast-3060/state.json` — `{instance_id, machine_id, ip,
port, fail_count, machine_avoid[]}`. Instance id sai do hardcode.

**Secrets**: `/etc/onboard/secrets/vast-3060.env` (root-600) — VAST_API_KEY,
PORTAINER_API_KEY, DINASTIA_BASE_URL, DINASTIA_TOKEN, NOTIFY_PHONE,
GW_STT_KEY, GW_TTS_KEY. (Sem ssh: validação de gateway é via edge público.)

**Systemd**: `vast-3060-ensure.service`/`stop.service` passam a chamar
`/usr/bin/python3 /opt/vast-3060/vast3060.py <cmd>`. Timers inalterados.

## Tasks

1. Script `ops/vast-3060/vast3060.py` + self-check (`python3 -m py_compile` +
   `status` dry).
2. Deploy: secrets, `/opt/vast-3060/`, state seed da instância atual, units.
3. **Drill E2E real**: `migrate` agora — provisiona nova, valida, flipa stack 38,
   destrói a 45646652, notifica. Prova o caminho inteiro (~$0,04).
4. Pós-drill: ensure no-op na nova; edge STT+TTS 200; timers apontando certo.
5. Commit (repo) + push + SUMMARY + STATE.md.

## Fora de escopo
- Migração do 3090 (reconciler próprio já cobre).
- Blocklist de qualidade compartilhada com o gateway.
