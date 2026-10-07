---
phase: quick-261007-ou0
plan: 01
subsystem: ops/vast-3060 (pod 3060 unificado STT+TTS+rerank+embed)
tags: [vast, watchdog, resiliencia, disco, diag]
clickup: 86aktrqgg
requires: []
provides:
  - start_last_fail / start_last_ok / last_stop no state.json
  - watchdog re-dispara start apos falha final (07:00-19:30 BRT, >=30min, teto 6/dia)
  - disk_shortfall_api + disk_probe (SSH best-effort) pos-running e no loop do infinity
  - diag() com snapshot da API Vast (DIAG API:) sempre
affects: [vast-unified-start.service, vast-unified-watchdog (timer 3min)]
tech-stack:
  added: []
  patterns: [funcoes PURAS testaveis + I/O fino, state.json como memoria entre runs]
key-files:
  modified:
    - ops/vast-3060/unified3060.py
    - ops/vast-3060/test_unified3060.py
  deployed:
    - /opt/vast-3060/unified3060.py (backup /opt/vast-3060/unified3060.py.bak-20261007-1759)
decisions:
  - "Re-disparo por falha de start tem janela propria ate 19:30 BRT (cutoff 18h segue so p/ preempcao)"
  - "MAX_START_RETRIES_DAY=6 (teto anti-custo; cada re-disparo cria ate 3 instancias); ao atingir: 1 notify de desistencia por dia"
  - "30min de espera contam do evento mais recente entre wd_last_trigger e start_last_fail"
  - "exit 0 (lock) e exit 2 (edge duvidoso) NAO registram falha; so exit 1 final"
  - "Caminho de preempcao (wd_needs_pod) inalterado: cutoff 18h, sem notify no retrigger"
metrics:
  duration: ~25min
  completed: 2026-10-07
  tasks: 3
  tests: 116 (88 antigos + 28 novos), todos OK
---

# Quick 261007-ou0: Resiliencia do start do pod 3060 (retry pelo watchdog, disk check, diag via API)

Agora o watchdog re-dispara o start depois de uma falha final 3/3 (state `start_last_fail`, espera de 30min, janela até 19:30, no máximo 6 por dia, 1 notify por re-disparo). Disco pequeno é detectado logo depois do `running`: pela API e, quando o SSH responde, pelo probe. O `diag()` passou a logar o snapshot da API Vast mesmo quando o SSH é recusado.

## Commits (branch worktree-agent-a1971ab015ee79e9a)

| Task | Commit | Mensagem |
|------|--------|----------|
| 1 RED | b3175a5 | test(261007-ou0): add failing tests for watchdog retrigger after start failure |
| 1 GREEN | 9f6de43 | feat(261007-ou0): watchdog re-dispara start apos falha final |
| 2 RED | e890560 | test(261007-ou0): add failing tests for disk check and diag API snapshot |
| 2 GREEN | d54eac5 | feat(261007-ou0): disk check pos-running + diag com snapshot da API Vast |
| 3 | (sem commit de codigo) | deploy em /opt/vast-3060 — o codigo ja estava nos commits acima |

## O que mudou

- **Task 1:** novas funções `start_retry_allowed`, `start_fail_needs_pod` (pura), `record_start_fail` e `_retrigger_start_fail`. O `__main__` registra a falha final (exit 1 na 3ª tentativa, ou no `start <id>` de resume). O `cmd_start` grava `start_last_ok` quando dá certo e o `cmd_stop` grava `last_stop`. No `cmd_watchdog`, `needs_pod = wd_needs_pod OR start_fail_needs_pod`. A assinatura de `watchdog_decision` não mudou.
- **Task 2:** novas funções `disk_shortfall_api`, `parse_disk_probe` (parse estrito), `disk_probe_verdict`, `inst_snapshot`, `disk_probe` (best-effort, nunca chama fail) e a constante `DISK_PROBE_CMD`. O `cmd_start` checa a API e o probe logo depois de "running ip=...", e roda o probe a cada ~5min (i % 20 == 19) dentro do loop de 30min do infinity. O `diag()` loga `DIAG API: ...` antes do SSH e `DIAG SSH rc=...` quando o rc não é 0.

## Deploy (Task 3)

### FATOS (com fonte)
- Antes do deploy: `systemctl is-active vast-unified-start.service` = `activating`. Em `state.json`, `instance_id=null`, `pending_id=54715069`, `wd_fail_streak=0`, `wd_last_trigger=2026-10-06T08:45`, e o `start_last_fail` ainda não existia.
- O sha256 de `/opt/vast-3060/unified3060.py` antes do deploy era igual ao da base `d8157c4` (`fd1779...`), então o diff aplicado é só o deste plano.
- Backup em `/opt/vast-3060/unified3060.py.bak-20261007-1759` (cp -p). Instalado com modo 644 root:root, igual ao original. O sha256 do repo e o do /opt são iguais: `eb6f739a...`.
- `sudo python3 /opt/vast-3060/unified3060.py status` rodou com rc=0, sem traceback.
- Um tick de `watchdog` deu `watchdog: noop_start (instance=None pending=54715069)`, rc=0, e o state.json ficou inalterado. Antes de rodar, confirmei lendo o código: `start_running()` dá True com a unit em activating e `pending_id != instance_id`. O retorno `noop_start` sai antes de qualquer I/O com a Vast e não salva o state, porque o streak continuou 0.
- O start.service continuou `activating` depois do deploy e do tick, sem interrupção. Não rodei start, stop nem restart.
- O start em andamento (PID 2330922) começou às 17:52:16 (journal), antes da cópia às 17:59. Ele criou a instância 54715069 (bid, machine 136979, Texas).

### Limitação do start em andamento
- O processo em execução carregou o módulo ANTIGO. Se ele falhar 3/3, ele NÃO grava `start_last_fail`, e o watchdog novo não re-dispara hoje por causa dele. O comportamento novo vale a partir do próximo start (timer 07:00 ou um disparo do watchdog).
- Comando manual opcional para o Pedro, só se o start atual falhar e ele quiser o re-disparo ainda hoje (NÃO executado). Precisa rodar DEPOIS que a unit sair de activating, porque o start antigo regrava o state.json inteiro no fim e apagaria a chave:
  ```bash
  systemctl is-active vast-unified-start.service   # tem que ser failed/inactive
  sudo python3 -c "import json,datetime,zoneinfo as z;p='/var/lib/vast-3060/state.json';d=json.load(open(p));d['start_last_fail']=datetime.datetime.now(z.ZoneInfo('America/Sao_Paulo')).isoformat();json.dump(d,open(p,'w'),indent=1)"
  ```
  O watchdog re-dispara cerca de 30min depois disso, desde que seja antes de 19:30.

## HIPÓTESES (não verificadas)
- HIPÓTESE: a API Vast pode reportar `disk_space=40` mesmo quando o overlay real é de 19G, como no incidente de 2026-10-07 com a machine 145593. Se isso acontecer, `disk_shortfall_api` não pega, e só o probe SSH detecta. Para resolver: comparar `disk_space` da API com o `df` real numa instância nova da mesma machine.
- HIPÓTESE: logo depois do "running", o SSH muitas vezes ainda não está pronto, então o probe pós-running tende a cair em "sem SSH — seguindo". A proteção efetiva no caso do incidente seria o probe a cada ~5min dentro do loop do infinity. Para resolver: olhar no journal dos próximos starts as linhas `disk probe:` e `disk probe sem SSH`.
- HIPÓTESE: o `df -BG --output=size,avail /` existe na imagem do pod (coreutils GNU). Se não existir, o rc continua 0 por causa do `|| echo 0` só no grep, e o parse devolve `size_gb=None`, ou seja, sem ação (seguro). Para resolver: checar a primeira linha `disk probe:` no journal.

## Limitação conhecida
Sem SSH não existe leitura confiável do `df` real do container: o onstart não expõe endpoint próprio, e as portas abertas são de speaches, infinity e xtts. Quando o SSH é recusado, a única defesa é a API Vast, que pode estar errada (hipótese acima). Nesse caso o comportamento volta ao antigo (timeout de 30min do infinity), mas agora o `diag` deixa o snapshot da API no journal.

## Deviations from Plan
- O plano dizia para commitar no `develop`. Por instrução do orquestrador, os commits ficaram na branch do worktree. A Task 3 não gerou commit próprio porque o código já estava commitado nas Tasks 1 e 2 (TDD: test + feat por task).
- Testes extras além do behavior: teto diário (notifica 1x e não repete no tick seguinte), contador que zera ao virar o dia, `last_stop` depois da falha → noop, `disk_probe` sem SSH/exceção → None, e `diag` com SSH levantando exceção ainda loga `DIAG API`.
- O hook `clickup-link-enforce` avisou "repo GSD sem tarefa ClickUp associada" em cada Write dentro do worktree. Não rodei o link-wizard no worktree (alteraria `.planning` do worktree). O card do plano é o 86aktrqgg, e o vínculo fica com o orquestrador.

## Threat model
- T-ou0-01 (mitigado): `parse_disk_probe` só aceita linhas exatas `==DF==`/`==NOSPACE==` seguidas de inteiros. Lixo vira None/False, e nada da saída vira argv ou comando (teste `test_garbage`).
- T-ou0-02 (mitigado): espera de 30min a partir do evento mais recente, teto de 6 por dia, cutoff 19:30, exigência de falha de HOJE mais recente que o último stop/sucesso, e o `fail()` sempre destrói a instância.
- T-ou0-03/04: aceitos conforme o plano. `DISK_PROBE_CMD` é constante e o `status_msg` é truncado em 200 caracteres.

## TDD Gate Compliance
Task 1: test b3175a5 seguido de feat 9f6de43. Task 2: test e890560 seguido de feat d54eac5. No RED, os testes negativos que passaram (5 na Task 1) cobrem casos que já eram noop no código antigo, como esperado.

## Self-Check: PASSED
- ops/vast-3060/unified3060.py e test_unified3060.py: FOUND
- /opt/vast-3060/unified3060.py.bak-20261007-1759: FOUND
- commits b3175a5, 9f6de43, e890560, d54eac5: FOUND (git log)
