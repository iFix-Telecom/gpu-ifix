---
phase: quick-260917-lp4
plan: 01
subsystem: ops/vast-3060 (scheduler do pod 3060 unificado)
tags: [cost-leak, vast-ai, reconciliation, best-effort, gpu]
requires: [ops/vast-3060/vast3060.py helpers http/http_json/notify]
provides:
  - "fail() de provision destroi a instancia nova em TODOS os caminhos"
  - "sweep_orphans() reconcilia por label exato no start e no stop"
  - "notify de vazamento com quantidade, ids e $/h liberado"
affects: [ops/vast-3060/unified3060.py]
tech-stack:
  added: []
  patterns: [best-effort reconciliation, pure selector + I/O wrapper, fail-closed destroy]
key-files:
  created: []
  modified: [ops/vast-3060/unified3060.py]
decisions:
  - "Filtro de label por IGUALDADE exata (nunca substring) — protege o pod primary do gateway (ifix-primary-lifecycle-*, 3090) e labels legados"
  - "Sweep do start roda DEPOIS do state salvo, para que keep_id=new_id seja um id bom e definido"
  - "vast_list() devolve None em HTTP != 200 = 'nao sei, nao mexe' (sem notify, para blip de API nao virar spam)"
  - "diag() antes do vast_destroy dentro de fail() — evidencia coletada antes de matar a instancia"
metrics:
  duration: ~12min
  completed: 2026-09-17
---

# Quick 260917-lp4: Fix do leak de pods 3060 orfaos no unified3060 — Summary

Provision falho e stop noturno agora deixam custo ZERO de GPU orfa: `fail()`
destroi a instancia nova em todos os caminhos e um sweep por label exato
reconcilia o que tenha sobrado, best-effort, com alerta quando acha vazamento.

## Evidencia que motivou (medida 2026-09-17, antes da mudanca)

5 instancias com label `stt-tts-rerank-unified` vivas simultaneamente; 4 orfas
destruidas a mao (50479985 com 7,4 dias de vida, 51002316, 51201042, 51203451),
**$13,01 queimados**. Burn caiu de **$0,3814/h -> $0,2036/h** pos-limpeza.

Root cause (2 defeitos independentes, ambos fechados aqui):

1. `fail(step, inst=None, destroy_new=False)` so destruia a nova quando o
   caller pedia. Os caminhos `speaches health timeout 20min`, `install {m}`,
   `infinity/xtts health`, `xtts speech`, `validacao STT/TTS`, `embed`,
   `rerank` e `flip stack` chamavam `fail()` sem pedir o destroy -> logavam
   "preservada p/ diagnostico" e `sys.exit(1)`. Ninguem voltava para destruir.
2. `cmd_stop` destruia SOMENTE `state.json["instance_id"]`. A orfa nunca
   entrava no state (sobrescrito pela instancia boa do retry seguinte), logo
   sobrevivia a todo stop das 20:00 — indefinidamente.

## As 3 mudancas

### 1. `fail()` destroi a nova SEMPRE (`fix`, commit 0a879d8)

- Assinatura passa a ser `fail(step, inst=None)` — o parametro que tornava o
  destroy opcional foi removido, junto com o texto "preservada p/ diagnostico".
- Ordem dentro de `fail`: `log` -> `diag(env, inst)` -> `vast_destroy(env,
  new_id)` incondicional -> machine para `machine_avoid` -> `notify` ("nova
  {id} DESTRUIDA, diagnostico no journal/log") -> `sys.exit(1)`.
  O `diag()` antes do destroy e deliberado: a evidencia (onstart.log, pip,
  infinity, df) morre com a instancia, entao e coletada primeiro.
- `sys.exit(1)` preservado — o loop de 3 tentativas em `__main__` depende do
  exit code 1 para re-tentar em outra machine.
- Caminho `edge_ok == False` (`sys.exit(2)`) **intocado**: ali o flip ja foi
  feito, a nova E a instancia corrente persistida no state e o rollback e
  decisao humana. Nao e leak.

### 2. Sweep/reconciliacao por label exato (`feat`, commit 0c9d435)

- `LABEL = "stt-tts-rerank-unified"` no topo; o payload do `PUT /asks/` passa
  a usar `"label": LABEL` (fim do literal duplicado).
- `vast_list(env)`: `GET /instances/` via `v.http_json`. Retorna `None` quando
  HTTP != 200 — sinal explicito de "nao sei, nao mexe em nada".
- `select_orphans(instances, label=LABEL, keep_id=None)`: funcao **pura**, sem
  I/O, testavel com fixtures. Descarta item cujo label nao seja **igual** a
  `LABEL`, item sem `id`, e o `keep_id`.
- `sweep_orphans(env, keep_id=None, context="")`: corpo inteiro em
  `try/except Exception` -> loga e devolve `[]`. Destroi cada orfa, soma
  `dph_total` e notifica **so quando achou >= 1 orfa**.
- Wiring em 2 pontos:
  - `cmd_start`, fim do caminho de sucesso (depois do destroy da anterior, do
    `save_state` com `instance_id=new_id` e do notify "pod 3060 UP"):
    `sweep_orphans(env, keep_id=new_id, context="start")`.
  - `cmd_stop`: `sweep_orphans(env, keep_id=None, context="stop")` — no stop o
    alvo e destruir TUDO do label. O early-return `if not iid: return` virou
    log + segue para o sweep, que e **exatamente** o caso em que a orfa
    sobrevivia.

### 3. Dry-checks de resiliencia (Task 3, sem commit de codigo)

Nenhum ajuste de codigo foi necessario — os 3 cenarios passaram de primeira.

## Por que cada decisao

**Igualdade de label, nunca substring.** O pod primary do gateway usa label
`ifix-primary-lifecycle-*` (3090, ciclo de vida gerido pelo gateway em Go).
Um filtro por `in`/`startswith` varreria esse pod e derrubaria o tier-0 do
gateway. O dry-check prova que `ifix-primary-lifecycle-42`,
`stt-tts-3060-auto` (legado), label `None` e `stt-tts-rerank-unified-old`
todos sobrevivem.

**Sweep do start depois do state salvo.** Antes do flip/validacao o "id bom"
ainda e indefinido — um sweep ali poderia destruir a propria instancia em
provisionamento. Rodando depois do `save_state`, `keep_id=new_id` e
garantidamente o id correto e persistido.

**Best-effort integral.** O sweep e uma otimizacao de custo, nao um requisito
de disponibilidade: nunca pode derrubar o provision (que acabou de validar um
pod bom) nem o stop. Dai `try/except` no corpo todo + `[]` como retorno de
falha + `None` da listagem tratado como "pula".

## Verificacao executada (100% offline)

| Gate | Resultado |
|------|-----------|
| `python3 -m py_compile ops/vast-3060/unified3060.py` | OK-COMPILE |
| `destroy_new` fora de comentario | 0 ocorrencias |
| `preservada p/ diagnostico` fora de comentario | 0 ocorrencias |
| `return fail(` | 11 (>= 9 exigidos) |
| Filtro com fixtures: `keep_id=1` -> `[2]`; `keep_id=None` -> `[1,2]` | OK-FILTER |
| Call sites de `sweep_orphans(env` | exatamente 2 (start + stop) |
| `"label": LABEL` no payload do create | OK-LABEL-CONST |
| `vast_list` -> `None`: `[]`, 0 destroy, 0 notify | OK-BEST-EFFORT |
| `vast_list` levanta excecao: `[]`, 0 destroy, 0 notify | OK-BEST-EFFORT |
| `vast_destroy` levanta excecao no meio da lista: nao propaga | OK-BEST-EFFORT |
| Happy path com stubs: destroy so da orfa + 1 notify com ids e $/h | OK-HAPPY-PATH |

Notify produzido pelo dry-check (stub, nada foi enviado):
`pod 3060 sweep (stop): 2 orfa(s) destruida(s) ids=[1, 2] — liberado $0.1120/h`

**Nenhuma chamada real a API Vast foi feita. Nenhuma instancia foi destruida.
Nada de systemd/deploy foi tocado.** Todos os stubs foram monkeypatch dos
atributos do modulo (`u.vast_list`, `u.vast_destroy`, `u.v.notify`).

## PASSO MANUAL DE DEPLOY PENDENTE (nao executado nesta task)

O codigo esta no repo; o host que roda os timers continua com a versao antiga.

1. Copiar `ops/vast-3060/unified3060.py` do repo para
   `/opt/vast-3060/unified3060.py` no host dos timers.
   **Sem mexer em systemd** (nenhuma unit muda: comando e argv identicos).
2. Primeira validacao real:
   - proximo `start` (07:00) -> conferir no log a linha `sweep(start): ...`
     (esperado `nenhuma orfa` num dia limpo);
   - proximo `stop` (20:00) -> conferir `sweep(stop): ...`.
3. Se aparecer notify de sweep com orfas, significa que havia vazamento
   residual — o valor de `$/h liberado` e a economia recuperada.

## Deviations from Plan

**1. [gate defeituoso no plano] `grep -c 'sweep_orphans(env'` esperava 2, conta 3**

- **Encontrado em:** Task 2, verificacao automatizada
- **Causa:** o padrao tambem casa a propria linha `def sweep_orphans(env,
  keep_id=None, context=""):` — a assinatura e fixada pelo bloco
  `<interfaces>` do plano, entao a colisao e do grep, nao do codigo.
- **Acao:** intencao verificada com `grep -c '^\s*sweep_orphans(env'` = **2**
  (linhas 494 `context="start"` e 514 `context="stop"`). Nenhuma mudanca de
  codigo feita para agradar o grep; a assinatura ficou como o plano especifica.

**2. [Rule 3 - blocking] docstring de `fail()` continha o literal `destroy_new`**

- **Encontrado em:** Task 1, primeira rodada da verificacao
- **Causa:** o gate `grep -v '^\s*#' | grep -c destroy_new = 0` nao ignora
  docstrings, e a explicacao historica citava o nome do parametro removido.
- **Acao:** frase reescrita ("chamavam fail() sem pedir o destroy"), mantendo
  a explicacao historica. Gate verde.

Nenhum outro desvio: o plano executou como escrito.

## Threat Flags

Nenhuma superficie nova fora do `<threat_model>` do plano. Zero dependencia
nova (stdlib Python 3 apenas); nenhum campo externo vira comando ou caminho —
so `label` (igualdade) e `id` (int) decidem o destroy, e `dph_total` entra
apenas em texto de notify.

## Known Stubs

Nenhum. Os stubs usados nas verificacoes existem apenas dentro dos snippets
`python3 -c` de dry-check, nunca no codigo do scheduler.

## Self-Check: PASSED

- `ops/vast-3060/unified3060.py` presente e compila (`py_compile` limpo).
- Commits presentes no repo: `0a879d8` (Task 1), `0c9d435` (Task 2).
- `git diff a66de7a..HEAD --name-only` = apenas `ops/vast-3060/unified3060.py`
  (zero arquivo de deploy/systemd/docs tocado no codigo commitado).
- Task 3 nao gerou commit de codigo: os 3 dry-checks de resiliencia passaram
  sem exigir ajuste.
