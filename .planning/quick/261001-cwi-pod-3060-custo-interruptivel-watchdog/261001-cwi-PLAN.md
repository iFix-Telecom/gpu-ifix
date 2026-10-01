---
phase: quick-261001-cwi
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - ops/vast-3060/unified3060.py
  - ops/vast-3060/test_unified3060.py
  - ops/vast-3060/systemd/vast-unified-watchdog.service
  - ops/vast-3060/systemd/vast-unified-watchdog.timer
  - ops/vast-3060/systemd/vast-unified-stop.service
autonomous: true
requirements: [CWI-C-real-cost, CWI-D-disk30, CWI-A-bid, CWI-A-fallback, CWI-A-watchdog]
clickup: 86akregky

must_haves:
  truths:
    - "A escolha de oferta ordena e aplica teto (PRICE_CAP x CAP_STEPS) sobre o custo REAL = preco/h (dph_base ou lance) + storage_cost * DISK_GB / 730, e loga o custo decomposto"
    - "Pod nasce com disco de 30 GB"
    - "Por padrao (VAST3060_MODE=bid) o pod e criado como interruptivel (campo price no PUT /asks/{id}/), com lance = min_bid x margem; cai para on-demand se nao houver oferta bid elegivel ou se houve >= 2 preempcoes no dia"
    - "A cada 3 min entre 07:00 e 19:59 (horario de Brasilia), o watchdog detecta pod preemptado/morto, destroi o resto, registra preempcao diaria, notifica e dispara reprovisao sem rodar em cima de um start em andamento"
    - "O stop das 20:00 segue destruindo tudo do label e agora tambem interrompe um start em andamento"
  artifacts:
    - path: "ops/vast-3060/unified3060.py"
      provides: "real_cost, bid_price_for, rank_candidates, choose_mode, bump_preempt, in_watchdog_window, watchdog_decision, cmd_watchdog, start lock"
      contains: "def watchdog_decision"
    - path: "ops/vast-3060/test_unified3060.py"
      provides: "testes stdlib das funcoes puras novas (>= 20 testes antigos + novos)"
      contains: "class WatchdogDecisionTest"
    - path: "ops/vast-3060/systemd/vast-unified-watchdog.timer"
      provides: "timer a cada 3 min, horas 07-19"
      contains: "OnCalendar"
    - path: "ops/vast-3060/systemd/vast-unified-watchdog.service"
      provides: "oneshot unified3060.py watchdog"
      contains: "unified3060.py watchdog"
  key_links:
    - from: "cmd_start"
      to: "rank_candidates"
      via: "pick_offer devolve (offer, mode, bid, breakdown)"
      pattern: "rank_candidates\\("
    - from: "cmd_start create payload"
      to: "Vast PUT /asks/{id}/"
      via: "chave price so quando mode == bid"
      pattern: "\"price\""
    - from: "cmd_watchdog"
      to: "vast-unified-start.service"
      via: "systemctl start --no-block"
      pattern: "systemctl.*start.*--no-block|--no-block.*vast-unified-start"
---

<objective>
Reduzir o custo do pod 3060 unificado em tres frentes decididas pelo Pedro (ClickUp 86akregky):
C) ranking de oferta por custo REAL (preco/h + disco pedido), D) disco 40 → 30 GB,
A) pod interruptivel (bid) com fallback on-demand + watchdog de preempcao com reprovisao automatica.

Purpose: hoje o pod custa US$0,0644/h (base 0,0533 + storage 40G 0,0111) e o ranking por `dph_total`
ignora o storage do disco pedido; o mercado bid 3060 esta em ~US$0,034/h. Interruptivel so e aceitavel
com watchdog que reprovisiona sozinho apos preempcao.
Output: unified3060.py com selecao bid/ondemand por custo real + subcomando `watchdog`; testes; units systemd.
NADA de deploy, NADA de chamada real a API Vast/Portainer/gateway no executor.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md
@ops/vast-3060/unified3060.py
@ops/vast-3060/test_unified3060.py
@ops/vast-3060/systemd/vast-unified-start.service
@ops/vast-3060/systemd/vast-unified-stop.service

<facts_vast_api>
<!-- FATOS com fonte. O executor NAO deve consultar a API real. -->
- Criar interruptivel: `PUT /api/v0/asks/{offer_id}/` com a chave JSON **`price`** (float, US$/h) no corpo.
  Fonte: vast-cli `_autodocs/api-reference/api-instances.md` — `build_create_instance_payload` emite `price`;
  SDK `create_instance(..., bid_price=...)` vira `price` no JSON. Sem `price` a instancia nasce ON-DEMAND ao
  `dph_total` (vast-cli `vastai/SKILL.md`, "Interruptible (spot) rentals"). Mesma conclusao em
  `.planning/seeds/SEED-003-vast-spot-interruptible-emergency-burst.md` item 3.
- Buscar ofertas bid: o corpo de busca usa `type: "bid"` ("interruptible" e normalizado para "bid") —
  vast-cli `api-offers.md` (search_offers / POST /bundles/). Hoje o codigo usa `GET /bundles/?q=<json>` com
  `"type": "on-demand"` dentro do q (v.OFFER_QUERY); trocar o valor para `"bid"` mantendo o mesmo endpoint.
- Campos de oferta (vast-cli `_autodocs/types.md`, Offer = linha de /bundles/): `dph_base`, `dph_total`,
  `min_bid`, `storage_cost`, `storage_total_cost`, `is_bid`, `machine_id`, `host_id`, `geolocation` — todos
  Optional. `storage_cost` = US$/GB/mes (descricao do Pedro + nome da coluna; o types.md so diz "pricing").
- Preempcao: "When outbid, the instance moves to `stopped` (not destroyed) and storage charges continue"
  (vast-cli `SKILL.md`). Logo o watchdog precisa DESTRUIR a instancia parada (storage segue cobrando).
- Shape de instancia parada ja visto em prod: `actual_status=exited` + `intended_status=stopped`
  (SEED-011); `exited` pode ser transiente (Phase 12 D-02, 12-CONTEXT.md).
- Host ops-claude esta em America/Sao_Paulo (`timedatectl`, 2026-10-01) — OnCalendar local = BRT.
</facts_vast_api>

<hypotheses>
- HIPOTESE: `min_bid` e comparavel a `dph_base` (so GPU/maquina, sem storage). Resolve: primeira provisao
  real em modo bid — comparar `dph_total` da instancia criada com lance + storage logado.
- HIPOTESE: o `dph_total` das ofertas em GET /bundles/ sem `storage` embute ~5 GB de storage padrao (default
  `storage=5.0` do search_offers), por isso nao reflete 30 GB. Irrelevante para o codigo: usamos dph_base.
- HIPOTESE: `GET /instances/{id}/` de instancia inexistente devolve 200 com `instances` vazio/null OU 404.
  O codigo trata ambos como "gone" e qualquer outro codigo como "nao sei" (sem acao).
</hypotheses>

<interfaces>
Existentes (unified3060.py) a reutilizar, nao reescrever:
- `filter_offers(offers, machine_avoid=(), host_avoid=())` — remove avoid/host_avoid/CN, ordena por dph_total.
- `pick_offer(env, avoid, host_avoid=())` — hoje: GET /bundles/?q=v.OFFER_QUERY, CAP_STEPS sobre dph_total.
- `cmd_start(env, resume_id=None)` — provisiona; usa `offer["id"]`, `offer["machine_id"]`, `offer.get("host_id")`,
  `offer.get("dph_total")`, `offer.get("geolocation")`; `st["pending_id"]`; `fail()` faz avoid + destroy.
- `vast_get(env, iid)` -> dict|None ; `vast_destroy(env, iid)` -> http code ; `health(ip, port, timeout=8)` -> bool
- `sweep_orphans(env, keep_id=None, context="")`, `load_state()`, `save_state(st)`, `log(m)`, `LABEL`.
- vast3060 (como `v`): `v.PRICE_CAP=0.035`, `v.CAP_STEPS=[1.0,1.3,1.6,2.0]`, `v.OFFER_QUERY` (dict com
  `"type": "on-demand"`), `v.notify(env, text)`, `v.http`, `v.http_json`, `v.load_env()`.
- Portas internas: "8000/tcp" (speaches STT), "7998/tcp" (infinity embed/rerank), "8021/tcp" (XTTS).
- `__main__`: "start" roda ate 3 tentativas; SystemExit(1)=retry, SystemExit(2)=edge duvidoso (nao retry).
</interfaces>
</context>

<tasks>

<task type="auto" tdd="true">
  <name>Task 1: Custo real + disco 30G + selecao bid/on-demand com fallback (C, D, A1, A2)</name>
  <files>ops/vast-3060/unified3060.py, ops/vast-3060/test_unified3060.py</files>
  <behavior>
    - real_cost(offer, mode="ondemand", bid=None, disk_gb=30): hourly = dph_base (ou bid se mode bid); storage_h = storage_cost*disk_gb/730; total = hourly+storage_h. Ex: dph_base 0.0533, storage_cost 0.2 (US$/GB/mes), 30G -> storage_h 0.00822, total 0.06152. Retorna dict {hourly, storage_h, total, src}.
    - real_cost sem dph_base/storage_cost: cai em dph_total como total, storage_h 0, src "dph_total-fallback" (nunca estoura).
    - bid_price_for(offer, margin=1.15, cap_total=None, storage_h=0): lance = round(min_bid*margin, 4); se cap_total dado e lance+storage_h > cap_total, lance = cap_total - storage_h; se o lance resultante < min_bid -> None (inelegivel). Sem min_bid -> None.
    - choose_mode("ondemand", ...) -> "ondemand"; choose_mode("bid", preempt_today=2, max_preempt=2) -> "ondemand"; choose_mode("bid", 1, 2) -> "bid"; valor invalido -> "bid" com log (default).
    - rank_candidates(ondemand_offers, bid_offers, mode, machine_avoid, host_avoid, disk_gb, margin): aplica filter_offers nas duas listas; para cada degrau de CAP_STEPS monta candidatos (on-demand sempre; bid so se mode=="bid" e lance valido) com total <= cap; devolve o de MENOR total (empate -> on-demand) como dict {offer, mode, bid, cost, cap_mult}; nenhum em nenhum degrau -> None.
    - rank_candidates: oferta on-demand mais barata por dph_total mas mais cara por custo real (storage alto) perde para outra mais barata no total.
    - rank_candidates em mode bid sem nenhuma bid elegivel devolve a on-demand (fallback A2); CN/machine_avoid/host_avoid excluidos tambem na lista bid.
    - bump_preempt(st, today_str): se st["preempt_day"] != today -> zera e conta 1; senao +1; devolve contagem. preempt_today(st, today) le sem mutar (0 em dia diferente).
  </behavior>
  <action>
RED primeiro: adicionar ao test_unified3060.py as classes RealCostTest, BidPriceTest, ChooseModeTest, RankCandidatesTest, PreemptCounterTest cobrindo o behavior acima (stdlib unittest, sem I/O; mesmo estilo dos 20 testes existentes). Rodar e ver falhar. Commit `test(261001-cwi): ...`.

GREEN em unified3060.py:
1. Per D: `DISK_GB = 30` (comentario: stack instalado 19-23G; 25G vivia a 90%; 30G = minimo seguro, decisao Pedro 2026-10-01). Thresholds existentes ficam: disk-guard limpa em >=85% (=25,5G) e cmd_disk alerta em >=90% — coerentes com 30G, nao mudar; registrar isso num comentario junto do DISK_GB.
2. Constantes/config: `HOURS_PER_MONTH = 730`; `MODE_DEFAULT = "bid"`; `BID_MARGIN_DEFAULT = 1.15`; `MAX_PREEMPT_DEFAULT = 2`. Ler overrides do dict env (v.load_env le /etc/onboard/secrets/vast-3060.env): `VAST3060_MODE` (bid|ondemand), `VAST3060_BID_MARGIN` (float), `VAST3060_MAX_PREEMPT` (int); helper `cfg(env)` com parse tolerante (valor invalido -> default + log). Nao editar o arquivo de secrets (defaults bastam).
3. Funcoes puras: `real_cost`, `bid_price_for`, `choose_mode`, `rank_candidates`, `bump_preempt`, `preempt_today`, `today_brt()` (data YYYY-MM-DD via `datetime.now(ZoneInfo("America/Sao_Paulo"))`), `offer_query(kind)` (copia profunda de v.OFFER_QUERY trocando "type" para "on-demand" ou "bid"; NAO editar vast3060.py — e o legado). Teto aplicado sobre `cost["total"]` (per C), degraus v.PRICE_CAP*v.CAP_STEPS.
4. Reescrever `pick_offer(env, avoid, host_avoid=(), mode="ondemand", margin=1.15)`: GET /bundles/ com offer_query("on-demand") sempre e offer_query("bid") so se mode=="bid" (falha HTTP da busca bid = lista vazia + log, nao aborta); delega para rank_candidates; loga a escolha decomposta numa linha: modo, machine, host, geolocation, hourly (base ou lance + min_bid), storage_h (storage_cost x DISK_GB), total, degrau do teto. Retorna o dict do rank_candidates ou None. Geolocation e so registrada (sem restricao de regiao, per decisao); exclusao CN/machine_avoid/host_avoid mantida via filter_offers.
5. `cmd_start`: modo = choose_mode(cfg.mode, preempt_today(st, today_brt()), cfg.max_preempt); logar o motivo quando cair em on-demand por preempcoes. Usar o retorno novo: `offer = pick["offer"]`. No PUT /asks/{id}/ incluir `"price": pick["bid"]` SOMENTE quando pick["mode"]=="bid" (per A1; chave `price`, fonte nos facts). Persistir no state `mode`, `bid_price` (ou None) e `cost_total` junto do instance_id no sucesso. Notify de sucesso passa a mostrar modo, custo real decomposto (hourly + storage = total), geolocation e disco. Caminho resume continua igual (offer sintetico) com mode lido do state/"resume". No sucesso final, limpar `wd_needs_pod` (usado na Task 2) e zerar `wd_fail_streak`.
6. Lock de start (necessario para a Task 2 e para runs manuais): no inicio de cmd_start abrir `/var/lib/vast-3060/start.lock` e `fcntl.flock(LOCK_EX|LOCK_NB)`; se ja travado -> log "start ja em andamento" e `sys.exit(0)` (exit 0 = nao dispara retry no loop do __main__). Manter o fd aberto ate o fim do processo. Funcao `start_lock_held()` (tenta LOCK_NB e solta) para o watchdog consultar.

Imports novos permitidos: fcntl, copy, datetime/zoneinfo (stdlib). Nao mudar LABEL, sweep, flip_upstreams, fail().
  </action>
  <verify>
    <automated>cd ops/vast-3060 && python3 -m unittest -v test_unified3060 2>&1 | tail -5 && grep -n '^DISK_GB = 30' unified3060.py && grep -n '"price"' unified3060.py</automated>
  </verify>
  <done>Todos os testes (20 antigos + novos de custo/lance/modo/ranking/contador) passam; DISK_GB=30; PUT de criacao envia `price` apenas em modo bid; pick_offer loga custo decomposto e geolocation; start.lock impede start concorrente.</done>
</task>

<task type="auto" tdd="true">
  <name>Task 2: Watchdog de preempcao (A3) — decisao pura + subcomando</name>
  <files>ops/vast-3060/unified3060.py, ops/vast-3060/test_unified3060.py</files>
  <behavior>
    - in_watchdog_window(dt): True para 07:00..19:59 BRT, False 06:59 e 20:00 (dt timezone-aware; converte para America/Sao_Paulo).
    - reprovision_allowed(dt): False a partir de 18:00 BRT (REPROVISION_CUTOFF_H=18: provisao leva ~1-2h e o stop das 20:00 destruiria o pod recem-nascido).
    - watchdog_decision(in_window, start_running, instance_id, vast_state, inst, health_ok, fail_streak, k=3, needs_pod=False, minutes_since_trigger=None) -> (action, new_streak), tabela:
      fora da janela -> ("noop_window", streak inalterado);
      start_running -> ("noop_start", 0);
      instance_id None e needs_pod e (minutes_since_trigger None ou >= 30) -> ("retrigger", 0); instance_id None demais -> ("noop_none", 0);
      vast_state "error" (API falhou) -> ("noop_api", streak inalterado) — nunca conta como preempcao;
      vast_state "gone" -> ("preempted", 0);
      inst terminal (actual_status in {exited, stopped} ou intended_status == "stopped" ou cur_state == "stopped") E health_ok False -> ("preempted", 0);
      inst terminal mas health_ok True -> ("suspect", streak+1) e vira "preempted" quando streak+1 >= k;
      health_ok False (status nao-terminal: running/loading/offline/unknown) -> streak+1; >= k -> ("preempted", 0) senao ("suspect", streak+1);
      health_ok True e nao terminal -> ("ok", 0).
    - health_ok = False somente quando as TRES portas (8000, 7998, 8021) falham; falha parcial = True + log WARN (ver action).
  </behavior>
  <action>
RED: classes WatchdogWindowTest e WatchdogDecisionTest (tabela de estados -> acao, incluindo: API error nao incrementa; exited transiente com health ok so preempta apos k; gone imediato; start rodando zera; retrigger respeita 30 min). Ver falhar; commit test.

GREEN em unified3060.py:
1. Funcoes puras `in_watchdog_window(dt)`, `reprovision_allowed(dt)`, `is_terminal(inst)`, `watchdog_decision(...)` exatamente pela tabela do behavior. Constantes: `WATCHDOG_K = 3` (3 checagens x 3 min = 9 min), `WINDOW_START_H = 7`, `WINDOW_END_H = 20`, `REPROVISION_CUTOFF_H = 18`, `RETRIGGER_MIN = 30`.
   Discricao documentada em comentario: sinal Vast terminal + health ok NAO destroi de imediato porque `exited` ja foi visto transiente (Phase 12 D-02); Vast terminal + health falhando = imediato (assinatura de outbid: Vast move para stopped). "Health falha" = as 3 portas mortas (assinatura de pod/container fora); falha parcial so loga — reprovisionar o pod inteiro por um servico deixaria os 4 upstreams em fallback por 1-2h.
2. `vast_get_state(env, iid)` -> ("ok", inst) | ("gone", None) | ("error", None): 200 com instances dict -> ok; 200 com instances vazio/None ou 404 -> gone; demais (incl. http 0) -> error. Nao alterar vast_get existente.
3. `start_running()`: True se `systemctl is-active vast-unified-start.service` imprime "active" ou "activating" OU `start_lock_held()` (Task 1). Excecao do subprocess -> True (conservador: na duvida nao dispara).
4. `cmd_watchdog(env)`: now=datetime.now(ZoneInfo("America/Sao_Paulo")); fora da janela -> return sem I/O de rede. Le state; respeita pending_id (pending definido e diferente do instance_id = start em voo -> tratar como start_running). Coleta vast_state + health das 3 portas (ip/ports do inst via `public_ipaddr` + `ports[...][0]["HostPort"]`; sem ip/ports = health False). Chama watchdog_decision; persiste `wd_fail_streak`. Acoes:
   - "suspect": log com streak/k.
   - "preempted": log com status Vast (actual/intended/cur_state, status_msg); `vast_destroy` do instance_id (best-effort, ignora 404); `bump_preempt(st, today_brt())`; `instance_id=None`; `wd_fail_streak=0`; save_state; se `reprovision_allowed(now)`: `wd_needs_pod=True`, `wd_last_trigger=now.isoformat()`, save, dispara `systemctl start --no-block vast-unified-start.service` (reusa cmd_start com as 3 tentativas, o TimeoutStartSec de 6h e o lock nativo do systemd — decisao: nao chamar cmd_start in-process para nao herdar o timeout do watchdog); v.notify com motivo, machine, geolocation, contador de preempcoes do dia, modo atual e se o proximo start sera on-demand (choose_mode); se nao permitido (>=18:00): notify "pod 3060 preemptado apos 18h — sem reprovisao hoje, fallback ate amanha".
   - "retrigger": mesmo disparo systemctl + atualiza wd_last_trigger + log (sem notify repetido: notify so na 1a reprovisao e quando o start notificar sucesso/falha por conta propria).
   - noops: so log curto.
   O cmd_start ja notifica sucesso ("UP fresco") e falhas; sweep_orphans no start/stop cobre restos do label. Nao tocar em outros labels.
5. `__main__`: aceitar "watchdog" em USAGE e no dispatch.
  </action>
  <verify>
    <automated>cd ops/vast-3060 && python3 -m unittest -v test_unified3060 2>&1 | tail -5 && python3 unified3060.py bogus; test $? -eq 64 && grep -n 'watchdog' unified3060.py | grep -c USAGE</automated>
  </verify>
  <done>Tabela de estados do watchdog coberta por testes e passando; `unified3060.py watchdog` existe; preempcao destroi o resto, incrementa contador diario (com virada de dia), notifica e dispara o start via systemd sem concorrer com start em andamento; nenhuma chamada real a Vast nos testes.</done>
</task>

<task type="auto">
  <name>Task 3: Units systemd do watchdog + stop interrompe start em voo</name>
  <files>ops/vast-3060/systemd/vast-unified-watchdog.service, ops/vast-3060/systemd/vast-unified-watchdog.timer, ops/vast-3060/systemd/vast-unified-stop.service</files>
  <action>
1. `vast-unified-watchdog.service`: [Unit] Description "Watchdog de preempcao do pod 3060 unificado (bid)"; [Service] Type=oneshot, ExecStart=/usr/bin/python3 /opt/vast-3060/unified3060.py watchdog, TimeoutStartSec=120 (3 health x 8s + GET Vast + destroy cabem; o start e disparado com --no-block). Sem User= (igual aos outros units: roda como root).
2. `vast-unified-watchdog.timer`: OnCalendar=*-*-* 07..19:00/3:00 (a cada 3 min, horas 07-19 locais; host em America/Sao_Paulo — fato timedatectl), Persistent=false (nao recuperar disparos perdidos), AccuracySec=30s, [Install] WantedBy=timers.target. A janela tambem e checada dentro do codigo (defesa dupla).
3. `vast-unified-stop.service`: adicionar `ExecStartPre=-/bin/systemctl stop vast-unified-start.service` antes do ExecStart (o "-" ignora falha). Motivo em comentario: um start disparado tarde pelo watchdog (ou start da manha atrasado) nao pode sobreviver ao stop das 20:00 e criar pod noturno; o pending_id + sweep por label destroem o que ele tiver criado. Corrigir a Description obsoleta "(preserva disco)" para "Destroi pod 3060 unificado + sweep por label (20:00)".
4. NAO copiar para /opt nem /etc, NAO rodar systemctl enable/daemon-reload — deploy e do orquestrador. Validar sintaxe com `systemd-analyze verify` apontando os arquivos do repo (aviso sobre /opt/vast-3060 inexistente e aceitavel se o binario nao existir no path; o importante e nao haver erro de parse) e `systemd-analyze calendar '*-*-* 07..19:00/3:00'` mostrando proximas execucoes em minutos multiplos de 3 entre 07 e 19h.
  </action>
  <verify>
    <automated>cd ops/vast-3060/systemd && systemd-analyze calendar --iterations=3 '*-*-* 07..19:00/3:00' && grep -n 'unified3060.py watchdog' vast-unified-watchdog.service && grep -n 'ExecStartPre=-/bin/systemctl stop vast-unified-start.service' vast-unified-stop.service</automated>
  </verify>
  <done>Dois units novos no repo com o calendario correto; stop.service interrompe start em voo antes de destruir; nada instalado no host.</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| ops-claude (root, systemd) -> API Vast | Bearer VAST_API_KEY; cria/destroi instancias pagas |
| ops-claude -> pod publico (HTTP health) | portas publicas de host de terceiro; resposta nao confiavel |
| /etc/onboard/secrets/vast-3060.env -> processo | config VAST3060_* lida do arquivo root-600 |

## STRIDE Threat Register

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-cwi-01 | Denial of service (custo) | cmd_watchdog loop de reprovisao | mitigate | retrigger no maximo a cada 30 min; corte as 18:00; >=2 preempcoes/dia forca on-demand; start.lock + systemd impedem starts paralelos |
| T-cwi-02 | Denial of service | falso positivo do watchdog destroi pod bom | mitigate | API Vast com erro nunca conta; terminal+health ok exige K=3; health falho exige as 3 portas mortas |
| T-cwi-03 | Tampering | VAST3060_* invalido no env | mitigate | parse tolerante: valor invalido -> default + log; lance nunca acima do teto escalonado |
| T-cwi-04 | Repudiation | decisao de modo/lance sem rastro | mitigate | log decomposto (hourly/storage/total/degrau) + state mode/bid_price/cost_total + notify |
| T-cwi-05 | Elevation of privilege | systemctl chamado pelo watchdog | accept | unit ja roda como root (padrao dos outros units); argv fixo, sem input externo |
| T-cwi-06 | Information disclosure | notify com machine/geo | accept | sem segredo no texto; canal WhatsApp interno ja usado |
</threat_model>

<verification>
- `cd ops/vast-3060 && python3 -m unittest -v test_unified3060` — tudo verde (20 antigos + novos).
- `grep -n '"price"' ops/vast-3060/unified3060.py` — so dentro do ramo mode == "bid".
- `python3 -c "import sys; sys.path.insert(0,'ops/vast-3060'); import unified3060 as u; print(u.DISK_GB)"` -> 30 (import sem side effect).
- Nenhuma chamada real a Vast/Portainer/gateway executada.
</verification>

<success_criteria>
- Selecao por custo real (base/lance + storage_cost*30/730) com teto escalonado e log decomposto.
- Pod nasce com 30 GB.
- Modo bid por padrao com lance min_bid x 1.15 (configuravel), fallback on-demand sem bid elegivel ou apos 2 preempcoes no dia.
- Watchdog a cada 3 min 07:00-19:59 BRT que destroi/conta/notifica/reprovisiona sem concorrer com start; stop 20:00 mata start em voo e segue varrendo o label.
</success_criteria>

<output>
Create `.planning/quick/261001-cwi-pod-3060-custo-interruptivel-watchdog/261001-cwi-SUMMARY.md` when done.
Incluir no SUMMARY: passos de deploy para o orquestrador (copiar unified3060.py -> /opt/vast-3060/, units -> /etc/systemd/system/, daemon-reload, enable --now vast-unified-watchdog.timer) e as HIPOTESES a medir na 1a provisao bid.
</output>
