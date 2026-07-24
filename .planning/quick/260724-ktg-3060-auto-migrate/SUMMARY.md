---
quick_id: 260724-ktg
slug: 3060-auto-migrate
date: 2026-07-24
status: complete
---

# SUMMARY — auto-migração do pod STT/TTS 3060

## Entregue

`ops/vast-3060/vast3060.py` (Python stdlib, substitui o bash) — ensure/stop/
migrate/status. State em `/var/lib/vast-3060/state.json` (instance id fora do
hardcode + `machine_avoid`). Secrets `/etc/onboard/secrets/vast-3060.env`.
Units systemd apontadas pro script novo (timers 15min/22h inalterados).

Gatilho: 8 ensures falhos consecutivos (~2h bloqueada na janela 7-22) →
migrate: offer ≤$0,035 (3060/3060Ti/3080, rel≥0,97, excl. machine_avoid) →
cria → health → instala whisper+kokoro → valida direto → flipa stack 38
(UPSTREAM_STT_URL + UPSTREAM_TTS_KOKORO_URL) → valida via EDGE c/ retry →
destrói a velha → state novo → WhatsApp (Dinastia /chat/send/text).
Falha pré-flip → destrói a nova, velha mantida, machine → avoid, WhatsApp ❌.
Falha pós-flip na validação edge → velha MANTIDA pra rollback manual + alerta.

## Drills E2E reais (2026-07-24)

- **Drill 1 (caminho de falha):** pegou 38103 CN (mais barata) → boot timeout
  8min → bail limpo: nova destruída, velha intacta, WhatsApp ❌ entregue.
  Expôs furo: sem memória de máquina ruim → fix `machine_avoid` no bail.
- **Drill 2 (caminho feliz):** machine 143726 KR $0,0316/h — **6min57s**
  fim-a-fim: healthy 4m43 → modelos 1m17 → flip → edge ok (1 retry na janela
  de restart do gateway) → velha 45646652 destruída → WhatsApp ✅.

## Estado final

Pod novo: instance 45729612, machine 143726 (KR), 58.79.62.163:35327,
$0,0316/h. ensure no-op ✔ · TTS edge 200 ✔ · timers ativos ✔.
