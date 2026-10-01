---
phase: quick-261001-cwi
plan: 01
subsystem: ops/vast-3060 (pod 3060 unificado STT+TTS+rerank+embed)
tags: [vast, cost, interruptible, bid, watchdog, systemd]
clickup: 86akregky
requires: []
provides: [real_cost, bid_price_for, choose_mode, cfg, rank_candidates, bump_preempt, preempt_today, offer_query, start.lock, watchdog_decision, cmd_watchdog, vast-unified-watchdog.timer]
affects: [vast-unified-start.service (lock), vast-unified-stop.service (ExecStartPre)]
tech-stack:
  added: []
  patterns: [pure decision function + thin I/O shell, fcntl flock start lock, systemctl --no-block handoff]
key-files:
  created:
    - ops/vast-3060/systemd/vast-unified-watchdog.service
    - ops/vast-3060/systemd/vast-unified-watchdog.timer
  modified:
    - ops/vast-3060/unified3060.py
    - ops/vast-3060/test_unified3060.py
    - ops/vast-3060/systemd/vast-unified-stop.service
decisions:
  - "Offer ranking/teto pelo custo real (dph_base|lance + storage_cost*30/730), nao mais dph_total"
  - "DISK_GB 40 -> 30; thresholds disk-guard 85% / cmd_disk 90% mantidos"
  - "VAST3060_MODE=bid default, lance = min_bid*1.15 (capado ao degrau do teto); fallback on-demand sem bid elegivel ou >=2 preempcoes/dia"
  - "Watchdog: API Vast com erro nunca conta; gone ou terminal+3 portas mortas = imediato; demais suspeitas exigem K=3 (9 min)"
  - "Reprovisao via systemctl start --no-block (nao in-process); corte 18:00 BRT; retrigger <= 1x/30 min"
metrics:
  completed: 2026-10-01
  tasks: 3
  tests: 84 (20 antigos + 64 novos)
---

# Quick 261001-cwi: pod 3060 com custo real, disco 30G e modo interruptivel com watchdog

O pod 3060 agora escolhe a oferta pelo custo real por hora (preço/h ou lance + storage_cost × 30G / 730) e nasce interruptível (bid, chave `price` no PUT /asks) por padrão. Volta sozinho para on-demand quando não há lance elegível ou após 2 preempções no dia. Um watchdog a cada 3 min (07:00–19:59 BRT) destrói o pod preemptado, conta, notifica e reprovisiona via systemd.

## Commits

| Task | Commit | Mensagem |
|------|--------|----------|
| 1 RED | 5c899de | test(261001-cwi): add failing tests for real cost, bid price, mode and ranking |
| 1 GREEN | 69af02c | feat(261001-cwi): real-cost offer ranking, 30G disk, bid mode with on-demand fallback |
| 2 RED | 2efc724 | test(261001-cwi): add failing tests for preemption watchdog decision |
| 2 GREEN | 03c955e | feat(261001-cwi): preemption watchdog subcommand |
| 3 | c2921c8 | feat(261001-cwi): watchdog systemd units; stop interrupts in-flight start |

## Mudanças de comportamento

1. **Seleção de oferta (C):** `pick_offer` busca `/bundles/` com `type:"on-demand"` sempre e `type:"bid"` em modo bid (falha da busca bid = lista vazia, não aborta). `rank_candidates` aplica os degraus `PRICE_CAP(0.035) × [1.0,1.3,1.6,2.0]` sobre o custo **real total** e devolve o menor total do primeiro degrau com candidato (empate = on-demand). Log de uma linha: modo, machine, host, geolocation, base ou lance (+min_bid), storage (storage_cost × 30G), total, src, degrau.
   - Conta: o pod de hoje (base 0.0533 + storage 30G a ~0.2/GB/mês ≈ 0.0082) dá ~0.0615 → só cabe no degrau 2.0x (0.070). Um lance de ~0.0345 + 0.0082 ≈ 0.0427 cabe no 1.3x (0.0455).
2. **Disco (D):** `DISK_GB = 30`.
3. **Modo bid (A1/A2):** `price = round(min_bid × margem, 4)`, reduzido para caber no degrau (lance + storage ≤ teto). Se ficar abaixo do min_bid, a oferta é inelegível. A chave `price` vai no PUT **só** quando `mode == "bid"`. O state passa a guardar `mode`, `bid_price`, `cost_total`, `geolocation` e, durante a criação, `pending_mode`/`pending_bid`. O notify "UP (fresco)" mostra o modo e o custo decomposto.
4. **start.lock:** `cmd_start` faz `flock` em `/var/lib/vast-3060/start.lock`. Se já estiver ocupado, loga e sai com `exit 0`, que não dispara retry.
5. **Watchdog (A3):** `unified3060.py watchdog`. Fora da janela não faz I/O de rede. Com start rodando (systemd active/activating, lock ocupado ou `pending_id != instance_id`) não faz nada. Tabela de decisão em `watchdog_decision`:
   - `gone` ou (terminal + 3 portas mortas) → preempted na hora.
   - Terminal + health ok, ou 3 portas mortas com status não terminal → `suspect` até K=3.
   - Erro da API → `noop_api`, sem mexer no streak.
   - Preempted → destroy, `bump_preempt`, `instance_id=None`. Antes das 18h: `wd_needs_pod=True` + `systemctl start --no-block vast-unified-start.service` + notify com o próximo modo. Depois das 18h: só notify ("fallback até amanhã").
   - Retrigger se `wd_needs_pod` e o último disparo foi há ≥30 min.
   - O start bem-sucedido limpa `wd_needs_pod` e zera `wd_fail_streak`.
6. **stop 20:00:** `ExecStartPre=-/bin/systemctl stop vast-unified-start.service` antes do destroy + sweep. Description corrigida.

## Passos de deploy (orquestrador — NÃO executados aqui)

```bash
# no ops-claude, como root, a partir do repo em develop após o merge
sudo install -m 0644 ops/vast-3060/unified3060.py /opt/vast-3060/unified3060.py
sudo install -m 0644 ops/vast-3060/systemd/vast-unified-watchdog.service /etc/systemd/system/
sudo install -m 0644 ops/vast-3060/systemd/vast-unified-watchdog.timer   /etc/systemd/system/
sudo install -m 0644 ops/vast-3060/systemd/vast-unified-stop.service     /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now vast-unified-watchdog.timer
systemctl list-timers 'vast-unified-*'          # confirmar o próximo disparo em múltiplo de 3 min
sudo journalctl -u vast-unified-watchdog.service -n 20
```
- Não é preciso mexer no secrets: os defaults (bid, 1.15, 2) bastam.
- O pod que está no ar agora (on-demand, 40G) segue até o stop das 20:00. O bid só vale a partir do próximo start (07:00 ou manual).
- Atenção: o watchdog vigia o pod atual assim que o timer for habilitado. Se ele estiver saudável, `ok`.

## Rollback

- **Forçar on-demand sem redeploy:** adicionar `VAST3060_MODE=ondemand` em `/etc/onboard/secrets/vast-3060.env`. Vale a partir do próximo start.
- Ajustes finos: `VAST3060_BID_MARGIN=<1..5>` (default 1.15) e `VAST3060_MAX_PREEMPT=<int>` (default 2; `0` força on-demand sempre).
- Desligar o watchdog: `systemctl disable --now vast-unified-watchdog.timer`.
- Código: reverter os commits 69af02c, 03c955e e c2921c8 e reinstalar `/opt/vast-3060/unified3060.py` + `stop.service`.

## Verificação executada

- `python3 -m py_compile unified3060.py test_unified3060.py`: ok.
- `cd ops/vast-3060 && python3 -m unittest test_unified3060 -v`: **84 OK**. Inclui testes com I/O mockado do PUT de criação (`price` só em bid, `disk=30`) e do `cmd_watchdog` (destroy/trigger/notify/corte das 18h/erro de API/pending em voo).
- `python3 unified3060.py bogus` → rc 64, USAGE lista `watchdog`.
- `systemd-analyze calendar '*-*-* 07..19:00/3:00'` → 09:30, 09:33, 09:36… A partir das 19:55 os próximos são 19:57 e depois 07:00 do dia seguinte.
- `systemd-analyze verify` nos 3 units: rc 0. Os únicos avisos são de `/etc/systemd/system/claude-tmux-keepalive@.service`, que não tem relação com esta tarefa.
- `bash -n` + `shellcheck -S warning` em `onstart-unified.sh` (que não foi alterado): limpo. Nenhum shell novo.
- Nenhuma chamada real a Vast/Portainer/gateway, nenhum ssh/systemctl, nada tocado em /opt ou /etc.

## FATOS (com fonte)

- A chave `price` no `PUT /asks/{id}/` cria instância interruptível; sem ela nasce on-demand. Fonte: vast-cli `api-instances.md`/`SKILL.md` (facts do PLAN, não reconsultados).
- Instância outbid vai para `stopped` e o storage continua cobrando. Por isso o watchdog destrói. Mesma fonte.
- Timer: `systemd-analyze calendar` neste host mostrou disparos em múltiplos de 3 min entre 07 e 19h, no fuso -03.
- Os 84 testes passam (saída do unittest acima).

## HIPÓTESES (a medir na 1ª provisão bid)

- HIPÓTESE: `min_bid` é comparável a `dph_base` (só máquina, sem storage). Como resolver: comparar o `dph_total` da instância criada com o `lance + storage_h` logado em `pick_offer`.
- HIPÓTESE: `storage_cost` vem em US$/GB/mês. Como resolver: mesma comparação acima (`storage_h` logado vs. o delta real no `dph_total`).
- HIPÓTESE: instância inexistente devolve 200 com `instances` vazio/null **ou** 404. O código trata os dois como `gone`. Como resolver: log do primeiro destroy ou preempção real.
- HIPÓTESE: o shape de preempção é `actual_status=exited` + `intended_status=stopped` (visto no SEED-011 para parada, não para outbid especificamente). Como resolver: o log `watchdog: PREEMPTADO ... actual=... intended=... msg=...` na primeira preempção.
- HIPÓTESE: o mercado bid 3060 continua por volta de US$0,034/h (dado do PLAN). Não sei o estado atual, porque não houve consulta à API.

## Deviations from Plan

- **[Rule 2] Testes extras com I/O mockado:** `CreatePayloadTest`, `CmdWatchdogTest`, `HealthAllTest`, `VastGetStateTest`, `CfgTest`, `OfferQueryTest`, `IsTerminalTest`, além das classes pedidas. Cobrem os key_links (`price` só em bid, systemctl `--no-block`) sem nenhuma chamada real.
- **[Rule 2] `cfg`:** além do default para valor inválido, rejeita margem fora de [1,5] e `MAX_PREEMPT` negativo (mitigação do T-cwi-03).
- **Resume path:** o modo e o lance da instância retomada vêm de `pending_mode`/`pending_bid`, gravados no state logo após o create. O custo exibido nesse caminho é o `dph_total` da instância.
- **`vast_get_state`:** também trata `instances` em forma de lista e JSON inválido (inválido → `error`).
- Nenhuma mudança arquitetural.

## Known Stubs

Nenhum.

## Threat Flags

Nenhuma superfície nova além do threat_model: o `systemctl start/stop` tem argv fixo e já estava previsto no T-cwi-05.

## Self-Check: PASSED

- Arquivos: unified3060.py, test_unified3060.py, os dois units do watchdog e o stop.service estão no repo.
- Commits 5c899de, 69af02c, 2efc724, 03c955e e c2921c8 estão no `git log` da branch worktree-agent-aa50e34da3fc2abef.
