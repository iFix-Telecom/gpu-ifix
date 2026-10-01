#!/usr/bin/env python3
"""Scheduler do pod 3060 UNIFICADO (STT+TTS+rerank+embed) — modelo up→destroy.

Historia:
- ate 2026-08-25: vast3060.py, up→destroy speaches-only.
- 2026-08-26..09-06: stop/start preservando instancia (era pre-freeze: recriar
  do zero era fragil). Morreu 2026-09-07: GPU da machine alugada por terceiro
  de noite → manha sem pod (PUT running = success:false resources_unavailable).
- desde 2026-09-07 (ordem Pedro): **up→destroy diario** — 07:00 provisiona
  POD FRESCO no mercado (nunca preso a maquina ocupada), 20:00 DESTROI (custo
  noturno zero). Viavel porque o install do Infinity ficou deterministico:
  pip PINADO de infinity-freeze.txt (o pip solto quebrou: colpali-engine novo
  × transformers git-pinado = ResolutionImpossible).

Uso: unified3060.py {start|stop|status|disk|watchdog}
  start  — provisiona pod novo, valida (health+GPU+STT/TTS/embed/rerank),
           seta url_override das 4 rows via gatewayctl (hot-reload, SEM
           recriar a task do gateway), valida via edge, destroi instancia
           anterior, persiste instance_id no state.
  stop   — DESTROI a instancia do state.
  watchdog — (timer 3 min, 07-19h BRT) detecta pod preemptado/morto, destroi,
           conta preempcao do dia, notifica e dispara o start via systemd.
  Desde 2026-10-01 (quick 261001-cwi): oferta escolhida por custo REAL
  (preco/h + storage_cost*DISK_GB/730), disco 30G, pod interruptivel (bid)
  por padrao com fallback on-demand. Rollback: VAST3060_MODE=ondemand no
  /etc/onboard/secrets/vast-3060.env.
  flip-stack-legacy — LEGADO: flipa as 4 envs do stack 38 via PUT no
           Portainer (recria a task do gateway). So manual, p/ rollback da
           migration 0038 / url_override indisponivel.
  Desde 2026-10 (quick 260930-uru): o flip diario NAO faz mais PUT no stack
  38. A URL vive em ai_gateway.upstreams.url_override, setada por
  `ssh root@10.10.10.50 docker exec <gateway> /gatewayctl upstreams update
  --name X --url Y`; o gateway recarrega por LISTEN/NOTIFY (+ backstop 60s).
  Desde 2026-09-17: start e stop RECONCILIAM por label (sweep de orfas) —
  provision falho destroi a nova sempre e o sweep varre o que tenha sobrado
  do label "stt-tts-rerank-unified" (leak: 4 orfas / $13,01).
State:   /var/lib/vast-3060/state.json (compartilhado com o legado vast3060:
         instance_id, machine_id, machine_avoid[], host_avoid[], pending_id)
         host_avoid = host_id fisico (varias machine_id no mesmo host);
         pending_id = instancia criada mas ainda nao validada (sobrevive a
         kill do systemd; retomada/destruida no proximo start).
Secrets: /etc/onboard/secrets/vast-3060.env (VAST_API_KEY, PORTAINER_API_KEY,
         DINASTIA_BASE_URL, DINASTIA_TOKEN, NOTIFY_PHONE, GW_STT_KEY, GW_TTS_KEY)
         PORTAINER_API_KEY so e necessario para o flip-stack-legacy.
Arquivos irmaos (deployados em /opt/vast-3060/): onstart-unified.sh,
         infinity-freeze.txt, vast3060.py (helpers reutilizados).
"""
import base64, copy, fcntl, json, os, shlex, subprocess, sys, time, urllib.parse, urllib.request
from datetime import datetime
from zoneinfo import ZoneInfo

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import vast3060 as v  # helpers: http, http_json, load_env, validate_*, notify

STACK = 38  # ai-gateway-prod
PORTAINER = "https://portainer3.ifixtelecom.com.br/api"
VAST = "https://console.vast.ai/api/v0"
STATE_PATH = "/var/lib/vast-3060/state.json"
HERE = os.path.dirname(os.path.abspath(__file__))
# quick 261001-cwi: 40 -> 30G foi REVERTIDO no mesmo dia. Medido no pod vivo
# 2026-10-01: stack instalado ocupa ~30G (a estimativa 19-23G era pre-XTTS);
# com 30G o disk-guard (limpa em >=85%) dispararia o tempo todo e o pod lotaria.
DISK_GB = 40
HOURS_PER_MONTH = 730  # storage_cost da Vast = US$/GB/mes -> /730 = US$/GB/h
# Modo de aluguel (quick 261001-cwi). Overrides em /etc/onboard/secrets/vast-3060.env:
#   VAST3060_MODE=bid|ondemand  VAST3060_BID_MARGIN=<float 1..5>  VAST3060_MAX_PREEMPT=<int>=0>
# Rollback para on-demand puro: VAST3060_MODE=ondemand (sem redeploy de codigo).
MODE_DEFAULT = "bid"
BID_MARGIN_DEFAULT = 1.15
MAX_PREEMPT_DEFAULT = 2
TZ = ZoneInfo("America/Sao_Paulo")
LOCK_PATH = "/var/lib/vast-3060/start.lock"
# label do pod unificado. Usado tanto no create quanto no sweep de orfas —
# o sweep casa por IGUALDADE (nunca substring/prefixo), senao varreria o pod
# primary do gateway (label "ifix-primary-lifecycle-*", 3090).
LABEL = "stt-tts-rerank-unified"
SSH_KEY = "/home/pedro/.ssh/id_ed25519"
# envs do stack 38 -> porta INTERNA do pod
# TTS: 8021 = wrapper XTTS-v2 (decisao Pedro 2026-09-07; Kokoro segue vivo
# na 8000 do speaches, piper CPU segue tier-1)
ENVMAP = {
    "UPSTREAM_STT_URL": "8000/tcp",
    "UPSTREAM_TTS_KOKORO_URL": "8021/tcp",
    "UPSTREAM_RERANK_URL": "7998/tcp",
    "UPSTREAM_EMBED_GPU_URL": "7998/tcp",
}
# quick 260930-uru: row de ai_gateway.upstreams -> porta INTERNA do pod.
# Ordem estavel (dict preserva insercao). Mapeamento p/ as envs antigas:
#   local-stt  = UPSTREAM_STT_URL        kokoro-tts = UPSTREAM_TTS_KOKORO_URL
#   rerank-gpu = UPSTREAM_RERANK_URL     embed-gpu  = UPSTREAM_EMBED_GPU_URL
UPSTREAM_PORTS = {
    "local-stt": "8000/tcp",
    "kokoro-tts": "8021/tcp",
    "rerank-gpu": "7998/tcp",
    "embed-gpu": "7998/tcp",
}
# worker-vm (gateway prod). IP fixo: o script roda como ROOT via systemd e o
# alias ssh "worker-vm" so existe no ~/.ssh/config do pedro; usa o mesmo
# SSH_KEY explicito do ssh_pod.
GATEWAY_HOST = "root@10.10.10.50"
GATEWAY_CONTAINER_FILTER = "name=ai-gateway-prod_gateway"

def log(m): print(f"[unified3060] {m}", flush=True)

def load_state():
    try:
        return json.load(open(STATE_PATH))
    except Exception:
        return {"instance_id": None, "machine_avoid": [], "host_avoid": [],
                "pending_id": None}

def save_state(st):
    tmp = STATE_PATH + ".tmp"
    with open(tmp, "w") as f:
        json.dump(st, f, indent=2)
    os.replace(tmp, STATE_PATH)

def vast_get(env, iid):
    c, raw = v.http("GET", f"{VAST}/instances/{iid}/",
                    {"Authorization": f"Bearer {env['VAST_API_KEY']}"})
    if c != 200:
        return None
    return json.loads(raw).get("instances")

def vast_destroy(env, iid):
    c, _ = v.http("DELETE", f"{VAST}/instances/{iid}/",
                  {"Authorization": f"Bearer {env['VAST_API_KEY']}"}, timeout=40)
    return c


def vast_list(env):
    """Todas as instancias da conta. None = "nao sei" (HTTP != 200) — o sweep
    trata None como "nao mexe em nada"."""
    c, data = v.http_json("GET", f"{VAST}/instances/",
                          {"Authorization": f"Bearer {env['VAST_API_KEY']}"},
                          timeout=30)
    if c != 200:
        return None
    return data.get("instances") or []


def select_orphans(instances, label=LABEL, keep_id=None):
    """PURA (sem I/O): instancias do label que NAO sao a corrente.

    Filtro por IGUALDADE de label — e isso que protege o pod primary do
    gateway ("ifix-primary-lifecycle-*", 3090) e labels legados
    ("stt-tts-3060-auto"). NUNCA usar `in`/startswith/substring aqui.
    """
    out = []
    for i in instances or []:
        if (i.get("label") or "") != label:
            continue
        iid = i.get("id")
        if iid is None:
            continue
        if keep_id is not None and int(iid) == int(keep_id):
            continue
        out.append(i)
    return out


def sweep_orphans(env, keep_id=None, context=""):
    """Reconciliacao por label: destroi instancias orfas do pod unificado.

    BEST-EFFORT integral — qualquer falha (listagem HTTP != 200, excecao de
    rede, destroy que explode) apenas loga e devolve []; nunca derruba
    provision nem stop. Notifica SO quando achou orfa (blip de API nao vira
    spam). Fecha o leak de 2026-09-17 (orfa nunca entrava no state, entao
    sobrevivia a todo stop das 20:00).
    """
    try:
        instances = vast_list(env)
        if instances is None:
            log(f"sweep({context}): listagem HTTP != 200, sweep pulado")
            return []
        orphans = select_orphans(instances, keep_id=keep_id)
        if not orphans:
            log(f"sweep({context}): nenhuma orfa")
            return []
        killed, freed = [], 0.0
        for o in orphans:
            iid = int(o["id"])
            c = vast_destroy(env, iid)
            freed += float(o.get("dph_total") or 0)
            killed.append(iid)
            log(f"sweep({context}): orfa {iid} machine {o.get('machine_id')} "
                f"${float(o.get('dph_total') or 0):.4f}/h destruida -> HTTP {c}")
        v.notify(env, f"pod 3060 sweep ({context}): {len(killed)} orfa(s) "
                      f"destruida(s) ids={killed} — liberado ${freed:.4f}/h")
        return killed
    except Exception as e:
        log(f"sweep({context}): excecao {e} — best-effort")
        return []

def health(ip, port, timeout=8):
    c, _ = v.http("GET", f"http://{ip}:{port}/health", timeout=timeout)
    return c == 200

def ssh_pod(inst, remote, timeout=90):
    host, port = inst.get("ssh_host"), inst.get("ssh_port")
    if not (host and port):
        return None
    return subprocess.run(
        ["ssh", "-i", SSH_KEY, "-p", str(port),
         "-o", "StrictHostKeyChecking=no", "-o", "BatchMode=yes",
         "-o", "ConnectTimeout=15", f"root@{host}", remote],
        capture_output=True, text=True, timeout=timeout)

def scp_pod(inst, local, remote_path, timeout=120):
    host, port = inst.get("ssh_host"), inst.get("ssh_port")
    return subprocess.run(
        ["scp", "-i", SSH_KEY, "-P", str(port),
         "-o", "StrictHostKeyChecking=no", "-o", "BatchMode=yes",
         local, f"root@{host}:{remote_path}"],
        capture_output=True, text=True, timeout=timeout)


GUARD_SCRIPT = r"""#!/bin/bash
while true; do
  USO=$(df --output=pcent / | tail -1 | tr -dc 0-9)
  if [ "${USO:-0}" -ge 85 ]; then
    echo "$(date -Is) uso=${USO}% -> limpando" >> /root/disk-guard.log
    rm -rf /root/.cache/huggingface/xet
    find /tmp -type f -mmin +120 -delete 2>/dev/null
    for f in /root/unified-*.log /root/onstart.log; do
      [ -f "$f" ] && [ $(stat -c%s "$f") -gt 52428800 ] && : > "$f"
    done
    echo "$(date -Is) pos-limpeza uso=$(df --output=pcent / | tail -1 | tr -dc 0-9)%" >> /root/disk-guard.log
  fi
  sleep 900
done
"""

def ensure_guard(inst):
    """Planta o disk-guard no pod via ssh (best-effort)."""
    remote = (
        "cat > /root/disk-guard.sh <<'DGEOF'\n" + GUARD_SCRIPT + "DGEOF\n"
        "chmod +x /root/disk-guard.sh\n"
        "pgrep -f 'bash /root/disk-guard.sh' >/dev/null || "
        "setsid /root/disk-guard.sh </dev/null >/dev/null 2>&1 &\n"
        "sleep 1; pgrep -f 'bash /root/disk-guard.sh' >/dev/null && echo GUARD_OK"
    )
    try:
        r = ssh_pod(inst, remote, timeout=60)
        ok = r is not None and "GUARD_OK" in r.stdout
        log(f"ensure_guard: {'ok' if ok else 'FALHOU'}")
        return ok
    except Exception as e:
        log(f"ensure_guard: excecao {e}"); return False


def filter_offers(offers, machine_avoid=(), host_avoid=()):
    """PURA (sem I/O): remove ofertas de machine_avoid, host_avoid e CN (portas
    publicas de maquinas CN inacessiveis — 38103/146752, 2026-09-02); devolve
    ordenado por dph_total.

    host_avoid existe porque machine_avoid sozinho re-escolhia o MESMO host
    fisico: machines 19775/20570/20571 = um host so (IP 91.150.160.38,
    2026-09-30). Oferta sem host_id nao e' barrada pelo host_avoid.
    """
    machine_avoid, host_avoid = set(machine_avoid or ()), set(host_avoid or ())
    out = [o for o in offers or []
           if o.get("machine_id") not in machine_avoid
           and not (o.get("host_id") is not None and o.get("host_id") in host_avoid)
           and "CN" not in (o.get("geolocation") or "")]
    out.sort(key=lambda o: o.get("dph_total", 9))
    return out


# ------------------------------------------------------------------ custo real
# quick 261001-cwi: ranking/teto pelo custo REAL = preco/h (dph_base on-demand
# ou o lance no modo bid) + storage_cost * DISK_GB / 730. O dph_total das ofertas
# nao reflete o disco pedido (HIPOTESE: embute ~5G default do search) e nao vale
# para bid.

def real_cost(offer, mode="ondemand", bid=None, disk_gb=DISK_GB):
    """PURA. -> {hourly, storage_h, total, src}. Nunca levanta: sem dados cai em
    dph_total; sem nada -> total 9.0 (inelegivel em qualquer teto)."""
    sc = offer.get("storage_cost")
    if mode == "bid" and bid is not None:
        storage_h = float(sc) * disk_gb / HOURS_PER_MONTH if sc is not None else 0.0
        hourly = float(bid)
        return {"hourly": hourly, "storage_h": storage_h, "total": hourly + storage_h,
                "src": "bid+storage" if sc is not None else "bid-nostorage"}
    base = offer.get("dph_base")
    if base is not None and sc is not None:
        storage_h = float(sc) * disk_gb / HOURS_PER_MONTH
        return {"hourly": float(base), "storage_h": storage_h,
                "total": float(base) + storage_h, "src": "base+storage"}
    tot = offer.get("dph_total")
    tot = float(tot) if tot is not None else 9.0
    return {"hourly": tot, "storage_h": 0, "total": tot, "src": "dph_total-fallback"}


def bid_price_for(offer, margin=BID_MARGIN_DEFAULT, cap_total=None, storage_h=0):
    """PURA. Lance = min_bid * margem (4 casas). Com cap_total, o lance e' reduzido
    para caber (lance + storage_h <= cap_total); se ficar abaixo do min_bid a
    oferta e' inelegivel (None). Sem min_bid -> None."""
    mb = offer.get("min_bid")
    if mb is None:
        return None
    mb = float(mb)
    bid = round(mb * margin, 4)
    if cap_total is not None and bid + storage_h > cap_total + 1e-12:
        bid = round(cap_total - storage_h, 4)
        if bid + storage_h > cap_total + 1e-12:
            bid = round(bid - 0.0001, 4)
    if bid < mb - 1e-12:
        return None
    return bid


def choose_mode(mode_cfg, preempt_today=0, max_preempt=MAX_PREEMPT_DEFAULT):
    """PURA. "ondemand" explicito ou >= max_preempt preempcoes hoje -> on-demand;
    senao bid. Valor invalido -> default bid (com log)."""
    m = mode_cfg.strip().lower() if isinstance(mode_cfg, str) else ""
    if m not in ("bid", "ondemand"):
        log(f"choose_mode: modo invalido {mode_cfg!r} -> default {MODE_DEFAULT}")
        m = MODE_DEFAULT
    if m == "ondemand":
        return "ondemand"
    if preempt_today >= max_preempt:
        return "ondemand"
    return "bid"


def cfg(env):
    """Config VAST3060_* do env (parse tolerante: invalido -> default + log)."""
    env = env or {}
    mode = (env.get("VAST3060_MODE") or MODE_DEFAULT).strip().lower().replace("-", "")
    if mode not in ("bid", "ondemand"):
        log(f"cfg: VAST3060_MODE invalido {env.get('VAST3060_MODE')!r} -> {MODE_DEFAULT}")
        mode = MODE_DEFAULT
    margin = BID_MARGIN_DEFAULT
    if env.get("VAST3060_BID_MARGIN"):
        try:
            margin = float(env["VAST3060_BID_MARGIN"])
            if not (1.0 <= margin <= 5.0):
                raise ValueError("fora de [1,5]")
        except Exception as e:
            log(f"cfg: VAST3060_BID_MARGIN invalido ({e}) -> {BID_MARGIN_DEFAULT}")
            margin = BID_MARGIN_DEFAULT
    maxp = MAX_PREEMPT_DEFAULT
    if env.get("VAST3060_MAX_PREEMPT"):
        try:
            maxp = int(env["VAST3060_MAX_PREEMPT"])
            if maxp < 0:
                raise ValueError("negativo")
        except Exception as e:
            log(f"cfg: VAST3060_MAX_PREEMPT invalido ({e}) -> {MAX_PREEMPT_DEFAULT}")
            maxp = MAX_PREEMPT_DEFAULT
    return {"mode": mode, "margin": margin, "max_preempt": maxp}


def rank_candidates(ondemand_offers, bid_offers, mode, machine_avoid=(), host_avoid=(),
                    disk_gb=DISK_GB, margin=BID_MARGIN_DEFAULT, price_cap=None,
                    cap_steps=None):
    """PURA. Para cada degrau do teto (price_cap * cap_steps) monta candidatos
    on-demand (sempre) e bid (so mode == "bid" com lance valido) cujo custo real
    total cabe no degrau; devolve o de MENOR total (empate -> on-demand) como
    {offer, mode, bid, cost, cap_mult}. Nenhum -> None."""
    price_cap = v.PRICE_CAP if price_cap is None else price_cap
    cap_steps = v.CAP_STEPS if cap_steps is None else cap_steps
    od = filter_offers(ondemand_offers, machine_avoid, host_avoid)
    bd = filter_offers(bid_offers, machine_avoid, host_avoid) if mode == "bid" else []
    for mult in cap_steps:
        cap = price_cap * mult
        cands = []
        for o in od:
            c = real_cost(o, "ondemand", disk_gb=disk_gb)
            if c["total"] <= cap + 1e-12:
                cands.append((c["total"], 0, {"offer": o, "mode": "ondemand", "bid": None,
                                              "cost": c, "cap_mult": mult}))
        for o in bd:
            sc = o.get("storage_cost")
            sh = float(sc) * disk_gb / HOURS_PER_MONTH if sc is not None else 0.0
            b = bid_price_for(o, margin, cap_total=cap, storage_h=sh)
            if b is None:
                continue
            c = real_cost(o, "bid", bid=b, disk_gb=disk_gb)
            if c["total"] <= cap + 1e-12:
                cands.append((c["total"], 1, {"offer": o, "mode": "bid", "bid": b,
                                              "cost": c, "cap_mult": mult}))
        if cands:
            cands.sort(key=lambda t: (round(t[0], 9), t[1]))
            return cands[0][2]
    return None


def today_brt():
    return datetime.now(TZ).strftime("%Y-%m-%d")


def preempt_today(st, today):
    """PURA, le sem mutar: preempcoes registradas hoje (0 se o dia virou)."""
    if st.get("preempt_day") != today:
        return 0
    return int(st.get("preempt_count") or 0)


def bump_preempt(st, today):
    """Muta st: conta +1 preempcao no dia (zera na virada). Devolve a contagem."""
    if st.get("preempt_day") != today:
        st["preempt_day"] = today
        st["preempt_count"] = 0
    st["preempt_count"] = int(st.get("preempt_count") or 0) + 1
    return st["preempt_count"]


def offer_query(kind):
    """Copia profunda de v.OFFER_QUERY com type = "on-demand" | "bid" (o legado
    vast3060.py nao e' tocado)."""
    q = copy.deepcopy(v.OFFER_QUERY)
    q["type"] = kind
    return q


_LOCK_FD = None


def acquire_start_lock(path=LOCK_PATH):
    """flock exclusivo nao-bloqueante. True = adquirido (fd fica aberto ate o fim
    do processo); False = outro start em andamento."""
    global _LOCK_FD
    if _LOCK_FD is not None:
        return True  # mesmo processo (loop de tentativas do __main__)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    fd = open(path, "a")
    try:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError:
        fd.close()
        return False
    _LOCK_FD = fd
    return True


def start_lock_held(path=LOCK_PATH):
    """True se OUTRO processo segura o lock de start (tenta e solta)."""
    if _LOCK_FD is not None:
        return False
    try:
        fd = open(path, "a")
    except OSError:
        return False
    try:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError:
        fd.close()
        return True
    fcntl.flock(fd, fcntl.LOCK_UN)
    fd.close()
    return False


def fetch_offers(env, kind):
    """GET /bundles/ com offer_query(kind). None = HTTP != 200."""
    q = urllib.parse.quote(json.dumps(offer_query(kind)))
    c, data = v.http_json("GET", f"{VAST}/bundles/?q={q}",
                          {"Authorization": f"Bearer {env['VAST_API_KEY']}"},
                          timeout=40)
    if c != 200:
        log(f"fetch_offers({kind}): HTTP {c}")
        return None
    return data.get("offers", []) or []


def describe_pick(pick):
    """Linha de log/notify com o custo decomposto da escolha."""
    o, c = pick["offer"], pick["cost"]
    if pick["mode"] == "bid":
        hourly = f"lance ${pick['bid']:.4f} (min_bid ${float(o.get('min_bid') or 0):.4f})"
    else:
        hourly = f"base ${c['hourly']:.4f}"
    return (f"modo={pick['mode']} machine {o.get('machine_id')} host {o.get('host_id')} "
            f"{o.get('geolocation')} | {hourly} + storage ${c['storage_h']:.4f} "
            f"(storage_cost {o.get('storage_cost')} x {DISK_GB}G) = ${c['total']:.4f}/h "
            f"[{c['src']}] teto {pick['cap_mult']}x")


def pick_offer(env, avoid, host_avoid=(), mode="ondemand", margin=BID_MARGIN_DEFAULT):
    """Melhor oferta 3060 por custo REAL (rank_candidates). Busca on-demand sempre
    e bid so em mode == "bid" (falha da busca bid = lista vazia, nao aborta).
    Geolocation so e' registrada (sem restricao de regiao); CN/avoid via
    filter_offers. -> dict {offer, mode, bid, cost, cap_mult} ou None."""
    od = fetch_offers(env, "on-demand") or []
    bd = []
    if mode == "bid":
        bd = fetch_offers(env, "bid")
        if bd is None:
            log("pick_offer: busca bid falhou — seguindo so com on-demand")
            bd = []
    pick = rank_candidates(od, bd, mode, avoid, host_avoid, disk_gb=DISK_GB,
                           margin=margin)
    if pick is None:
        log(f"pick_offer: nenhuma oferta (od={len(od)} bid={len(bd)}) ate teto "
            f"{v.CAP_STEPS[-1]}x")
        return None
    if pick["cap_mult"] > 1.0:
        log(f"pick_offer: teto escalado {pick['cap_mult']}x -> "
            f"${v.PRICE_CAP * pick['cap_mult']:.4f}")
    if mode == "bid" and pick["mode"] == "ondemand":
        log("pick_offer: nenhuma bid elegivel/mais barata -> on-demand")
    log("pick_offer: " + describe_pick(pick))
    return pick


def build_onstart():
    """Onstart AUTO-CONTIDO: prologo escreve infinity-freeze.txt + disk-guard.sh
    via heredoc, depois roda o corpo do onstart-unified.sh. Zero ssh/scp no
    caminho feliz (key ssh nao injeta em instancia nova sem reboot)."""
    freeze = open(os.path.join(HERE, "infinity-freeze.txt")).read()
    xtts = open(os.path.join(HERE, "xtts-server.py")).read()
    body = open(os.path.join(HERE, "onstart-unified.sh")).read()
    prolog = ("#!/bin/bash\n"
              "cat > /root/infinity-freeze.txt <<'FREEZEEOF'\n" + freeze +
              "FREEZEEOF\n"
              "cat > /root/disk-guard.sh <<'DGEOF'\n" + GUARD_SCRIPT +
              "DGEOF\n"
              "cat > /root/xtts-server.py <<'XTTSEOF'\n" + xtts +
              "XTTSEOF\n")
    raw = (prolog + body).encode()
    # gzip: a API Vast limita onstart a 16384 chars (erro 400/3471 em
    # 2026-09-08 quando o xtts-server engordou o b64 puro pra >16KB)
    import gzip
    b64 = base64.b64encode(gzip.compress(raw, 9)).decode()
    if len(b64) > 15000:
        raise RuntimeError(f"onstart b64 {len(b64)} perto do limite 16384")
    return (f"echo {b64} | base64 -d | gunzip > /root/onstart-unified.sh && "
            "chmod +x /root/onstart-unified.sh && /root/onstart-unified.sh")


def install_model(ip, port, m):
    """POST /v1/models/{m} baixa o modelo segurando a conexao — NAT/proxy pode
    derrubar no meio (HTTP 0, visto 2026-09-07 machine 146849). Apos falha do
    POST, polla GET /v1/models: o download server-side pode ter completado; se
    nao aparecer em 10min, re-POSTa (ate 3 ciclos)."""
    for _ in range(3):
        c, _ = v.http_json("POST", f"http://{ip}:{port}/v1/models/{m}",
                           None, None, timeout=900)
        log(f"install {m} -> HTTP {c}")
        if c in (200, 201):  # 201 = ja instalado
            return True
        for _ in range(40):  # 10min de poll
            time.sleep(15)
            c2, r2 = v.http_json("GET", f"http://{ip}:{port}/v1/models",
                                 None, timeout=15)
            ids = [x.get("id") for x in (r2.get("data") or [])] if c2 == 200 else []
            if m in ids:
                log(f"install {m}: presente via poll")
                return True
    return False


def validate_url(u):
    """Mesma regra do ValidateUpstreamURL (Go): scheme http/https + host."""
    if not isinstance(u, str) or not u:
        raise ValueError(f"url invalida: {u!r}")
    p = urllib.parse.urlparse(u)
    if p.scheme not in ("http", "https") or not p.netloc or not p.hostname:
        raise ValueError(f"url invalida (precisa http(s)://host): {u!r}")
    return u


def build_flip_targets(ip, ports):
    """[(row, url)] na ordem de UPSTREAM_PORTS. Porta ausente = erro explicito
    (nunca monta 'http://ip:None')."""
    if not ip:
        raise ValueError("ip vazio")
    out = []
    for name, internal in UPSTREAM_PORTS.items():
        if internal not in ports or ports[internal] in (None, ""):
            raise KeyError(f"porta {internal} ausente em ports (row {name})")
        url = validate_url(f"http://{ip}:{int(ports[internal])}")
        out.append((name, url))
    return out


def gatewayctl_remote(args):
    """Comando remoto: docker exec no container do gateway + /gatewayctl.
    O container prod e distroless (sem sh/env): /gatewayctl e chamado direto.
    O $(docker ps ...) fica FORA do quote — expandido no shell do worker-vm."""
    quoted = " ".join(shlex.quote(a) for a in args)
    return (f"docker exec $(docker ps -q -f {GATEWAY_CONTAINER_FILTER} | head -1) "
            f"/gatewayctl {quoted}")


def gateway_ssh_argv(remote):
    return ["ssh", "-i", SSH_KEY, "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
            GATEWAY_HOST, remote]


def gatewayctl_update_cmd(name, url):
    """argv do ssh que seta url_override da row `name` (url validada antes)."""
    validate_url(url)
    if not name:
        raise ValueError("name vazio")
    return gateway_ssh_argv(gatewayctl_remote(
        ["upstreams", "update", "--name", name, "--url", url]))


def flip_upstreams(env, ip, ports):
    """Seta url_override das 4 rows via gatewayctl no container do gateway.

    Sem idempotencia por leitura previa (parse da tabela do `upstreams list`
    seria fragil): sempre seta. UPDATE com o mesmo valor NAO dispara NOTIFY
    (trigger 0038 usa IS DISTINCT FROM), entao repetir e inofensivo.
    Retorna a lista de mudancas (mesmo contrato do flip_stack)."""
    targets = build_flip_targets(ip, ports)
    changed = []
    for name, url in targets:
        argv = gatewayctl_update_cmd(name, url)
        r = subprocess.run(argv, capture_output=True, text=True, timeout=60)
        if r.returncode != 0:
            raise RuntimeError(f"gatewayctl update {name} rc={r.returncode}: "
                               f"{(r.stderr or '')[-500:]}")
        changed.append(f"{name} -> {url}")
    log(f"upstreams flipados via url_override: {changed}")
    # LISTEN aplica em <2s; folga antes do validate_edge
    time.sleep(5)
    return changed


def flip_stack(env, ip, ports):
    """LEGADO: recria a task do gateway; usar so se url_override indisponivel
    (ex. rollback da migration 0038). Acessivel apenas pelo subcomando
    `flip-stack-legacy` — o caminho automatico usa flip_upstreams."""
    hdr = {"X-API-Key": env["PORTAINER_API_KEY"]}
    c, raw = v.http("GET", f"{PORTAINER}/stacks/{STACK}", hdr)
    stack = json.loads(raw)
    changed = []
    for e in stack["Env"]:
        want_port = ENVMAP.get(e["name"])
        if want_port:
            want = f"http://{ip}:{ports[want_port]}"
            if e["value"] != want:
                changed.append(f"{e['name']} -> {want}")
                e["value"] = want
    if not changed:
        log("envs do stack 38 ja corretas"); return []
    c2, raw2 = v.http("GET", f"{PORTAINER}/stacks/{STACK}/file", hdr)
    content = json.loads(raw2)["StackFileContent"]
    c3, r3 = v.http_json(
        "PUT", f"{PORTAINER}/stacks/{STACK}?endpointId={stack['EndpointId']}",
        hdr, {"stackFileContent": content, "env": stack["Env"],
              "prune": False, "pullImage": False}, timeout=180)
    if c3 != 200:
        raise RuntimeError(f"PUT stack {STACK} HTTP {c3}: {r3}")
    log(f"stack 38 flipado: {changed}")
    return changed


def diag(env, inst):
    """Dump de logs do pod via ssh pro journal (falha NAO destroi evidencia)."""
    try:
        r = ssh_pod(inst,
                    "echo ==HEALTH-INT==; curl -sm5 localhost:8000/health; echo; "
                    "curl -sm5 localhost:7998/health; echo; "
                    "echo ==ONSTART==; tail -15 /root/onstart.log 2>/dev/null; "
                    "echo ==PIP==; tail -8 /root/unified-pipinstall.log 2>/dev/null; "
                    "echo ==INF==; tail -15 /root/unified-infinity.log 2>/dev/null; "
                    "df -h / | tail -1")
        if r is not None:
            log("DIAG:\n" + (r.stdout or r.stderr)[-3000:])
    except Exception as e:
        log(f"DIAG falhou: {e}")


def cmd_start(env, resume_id=None):
    """Provisiona pod FRESCO, valida, seta url_override (gatewayctl), destroi o anterior.
    resume_id: continua orquestracao numa instancia ja criada (pos-falha
    transiente) em vez de criar outra."""
    # lock de start (quick 261001-cwi): watchdog e runs manuais nao podem
    # provisionar em paralelo. exit 0 = nao dispara retry no loop do __main__.
    if not acquire_start_lock():
        log("start ja em andamento (start.lock ocupado) — saindo sem acao")
        sys.exit(0)
    st = load_state()
    old_id = st.get("instance_id")
    conf = cfg(env)
    n_pre = preempt_today(st, today_brt())
    mode = choose_mode(conf["mode"], n_pre, conf["max_preempt"])
    if conf["mode"] == "bid" and mode == "ondemand":
        log(f"modo: {n_pre} preempcao(oes) hoje >= {conf['max_preempt']} -> on-demand")
    log(f"modo pedido={conf['mode']} efetivo={mode} margem={conf['margin']} "
        f"preempcoes_hoje={n_pre}")
    avoid = list(st.get("machine_avoid", []))
    host_avoid = list(st.get("host_avoid", []))
    log(f"PROVISION start; old={old_id} resume={resume_id} avoid={avoid} "
        f"host_avoid={host_avoid}")

    # pending_id de execucao anterior (systemd matou o start no meio, 2026-09-30:
    # pod vivo ficou fora do state e o dia inteiro rodou em fallback).
    pending = st.get("pending_id")
    if not resume_id and pending and pending != old_id:
        pinst = vast_get(env, pending)
        pstatus = (pinst or {}).get("actual_status")
        if pinst is None:
            log(f"pending {pending} nao existe mais (ou GET falhou) — limpando")
            st["pending_id"] = None
            save_state(st)
        elif pstatus in ("running", "loading"):
            log(f"pending {pending} vivo de execucao anterior — resumindo")
            resume_id = pending
        else:
            c = vast_destroy(env, pending)
            log(f"pending {pending} status={pstatus} -> destruida HTTP {c}")
            st["pending_id"] = None
            save_state(st)

    if resume_id:
        new_id = resume_id
        if old_id == new_id:
            old_id = None  # nao destruir a si mesma no final
        rinst = vast_get(env, new_id) or {}
        offer = {"machine_id": rinst.get("machine_id"),
                 "host_id": rinst.get("host_id"),
                 "dph_total": 0.0, "geolocation": "resume"}
        # modo/custo da instancia retomada: o do state (gravado na criacao)
        pick = {"offer": offer, "mode": st.get("pending_mode") or "resume",
                "bid": st.get("pending_bid"), "cap_mult": None,
                "cost": {"hourly": float(rinst.get("dph_total") or 0), "storage_h": 0,
                         "total": float(rinst.get("dph_total") or 0),
                         "src": "resume-dph_total"}}
        st["pending_id"] = new_id
        save_state(st)
    else:
        pick = pick_offer(env, avoid, host_avoid, mode=mode, margin=conf["margin"])
        if pick is None:
            v.notify(env, "pod 3060: SEM oferta elegivel (nem teto 2x, custo real) — "
                          "tier-0 fora, gateway nos fallbacks")
            log("sem oferta"); sys.exit(1)
        offer = pick["offer"]
        log(f"oferta: {describe_pick(pick)}")

        body = {"client_id": "me", "image": v.IMAGE, "disk": DISK_GB,
                "label": LABEL,
                "onstart": build_onstart(),
                "env": {"-p 8000:8000": "1", "-p 7998:7998": "1",
                        "-p 8021:8021": "1",
                        "WHISPER_MODEL": v.MODELS[0],
                        "HF_HOME": "/root/.cache/huggingface"},
                "runtype": "ssh"}
        if pick["mode"] == "bid":
            # interruptivel: chave `price` (US$/h) no PUT /asks/{id}/ (vast-cli
            # build_create_instance_payload). Sem ela nasce on-demand.
            body["price"] = pick["bid"]
        c, resp = v.http_json(
            "PUT", f"{VAST}/asks/{offer['id']}/",
            {"Authorization": f"Bearer {env['VAST_API_KEY']}"}, body, timeout=60)
        new_id = resp.get("new_contract")
        if c != 200 or not new_id:
            v.notify(env, f"pod 3060: create falhou HTTP {c}")
            log(f"create falhou {c}: {resp}"); sys.exit(1)
        log(f"criada {new_id} modo={pick['mode']} lance={pick['bid']}")
        # persiste JA: se o systemd matar este run, o proximo start acha a
        # instancia (resume/destroy) em vez de deixa-la paga fora do state
        st["pending_id"] = new_id
        st["pending_mode"] = pick["mode"]
        st["pending_bid"] = pick["bid"]
        save_state(st)

    def fail(step, inst=None):
        """Falha de provision: diagnostica via journal e SEMPRE destroi a nova
        (leak de GPU paga era o bug 2026-09-17: 4 orfas / $13,01 queimados —
        os caminhos health/install/validacao/flip chamavam fail() sem pedir o
        destroy e a instancia viva nunca voltava pra ninguem).
        Ordem importa: diag() ANTES do destroy, senao a evidencia morre com a
        instancia. Machine + host fisico -> avoid; pending_id limpo."""
        log(f"FALHA em '{step}'")
        if inst:
            diag(env, inst)
        c = vast_destroy(env, new_id)
        log(f"nova {new_id} destruida -> HTTP {c}")
        bad = offer.get("machine_id")
        bad_host = offer.get("host_id")
        if bad and bad not in st.get("machine_avoid", []):
            st.setdefault("machine_avoid", []).append(bad)
        if bad_host and bad_host not in st.get("host_avoid", []):
            st.setdefault("host_avoid", []).append(bad_host)
        st["pending_id"] = None
        save_state(st)
        v.notify(env, f"pod 3060: provision falhou em '{step}' "
                      f"(machine {bad} host {bad_host} -> avoid; nova {new_id} DESTRUIDA, "
                      "diagnostico no journal/log); anterior intacta se existia")
        sys.exit(1)

    inst = None
    for _ in range(60):  # 15min boot
        time.sleep(15)
        inst = vast_get(env, new_id)
        if inst and inst.get("actual_status") == "running" and inst.get("ports"):
            break
    else:
        return fail("boot timeout 15min", inst)
    ip = inst["public_ipaddr"]
    ports = {k: int(p[0]["HostPort"]) for k, p in inst["ports"].items()}
    log(f"running ip={ip} ports={ports}")

    # freeze + guard ja vao BAKED no onstart (build_onstart) — sem scp/ssh aqui
    for _ in range(80):  # 20min (pip do infinity concorre por CPU no boot)
        if health(ip, ports["8000/tcp"]): break
        time.sleep(15)
    else:
        return fail("speaches health timeout 20min", inst)
    log("speaches healthy")

    for m in v.MODELS:
        if not install_model(ip, ports["8000/tcp"], m):
            return fail(f"install {m}", inst)

    for _ in range(120):  # 30min (pip pinado + modelos HF ~5G)
        if health(ip, ports["7998/tcp"]): break
        time.sleep(15)
    else:
        return fail("infinity health timeout 30min", inst)
    log("infinity healthy")

    # XTTS (:8021): pip ~6G + download do modelo ~2G — ate 40min
    for _ in range(160):
        if health(ip, ports["8021/tcp"]): break
        time.sleep(15)
    else:
        return fail("xtts health timeout 40min", inst)
    log("xtts healthy")
    c, _ = v.http_json("POST", f"http://{ip}:{ports['8021/tcp']}/v1/audio/speech",
                       None, {"input": "validação de provisão", "voice": "luis"},
                       timeout=120)
    if c != 200:
        return fail(f"xtts speech HTTP {c}", inst)
    log("xtts speech ok")

    # gate GPU com RETRY: gpu_temp da API Vast atrasa em instancia nova
    # (2026-09-07: falso negativo derrubou provision com XTTS ja rodando em
    # CUDA — telemetria populou minutos depois). XTTS healthy ja exige CUDA
    # (.to("cuda") aborta sem GPU), entao 0 persistente + servicos ok e' quase
    # certamente lag; ainda assim falha apos 10min sem leitura.
    fresh = inst
    gpu_temp = 0
    for _ in range(20):  # ate 10min
        fresh = vast_get(env, new_id) or inst
        gpu_temp = fresh.get("gpu_temp") or 0
        if gpu_temp > 0:
            break
        time.sleep(30)
    if gpu_temp <= 0:
        # telemetria gpu_temp e' NAO-CONFIAVEL em varias maquinas (2026-09-08:
        # 0 persistente por 10min+ com XTTS gerando audio em CUDA — 2 pods
        # bons queimados). O XTTS ja passou em health+speech acima e
        # .to("cuda") aborta sem GPU — isso E' o gate real. Segue com WARN.
        log(f"WARN: gpu_temp={gpu_temp} mas XTTS/CUDA validado — prosseguindo")
        v.notify(env, "pod 3060: telemetria gpu_temp zerada mas XTTS em CUDA ok; "
                      "prosseguindo (verificar tok/s se desconfiar)")
    else:
        log(f"gpu gate ok ({gpu_temp})")

    ok, why = v.validate_pod_direct(ip, ports["8000/tcp"])
    if not ok:
        return fail(f"validacao STT/TTS: {why}", inst)
    c, r2 = v.http_json("POST", f"http://{ip}:{ports['7998/tcp']}/v1/embeddings",
                        None, {"input": "ping", "model": "bge-m3"}, timeout=60)
    dims = len((r2.get("data") or [{}])[0].get("embedding") or [])
    if c != 200 or dims != 1024:
        return fail(f"embed HTTP {c} dims={dims}", inst)
    c, _ = v.http_json("POST", f"http://{ip}:{ports['7998/tcp']}/v1/rerank",
                       None, {"model": "bge-reranker-v2-m3", "query": "ping",
                              "documents": ["a", "b"]}, timeout=60)
    if c != 200:
        return fail(f"rerank HTTP {c}", inst)
    log("validacao direta ok (stt/tts/embed1024/rerank)")

    ensure_guard(fresh)  # best-effort: guard ja vai no onstart; ssh pode falhar

    try:
        flip_upstreams(env, ip, ports)
    except Exception as e:
        return fail(f"flip upstreams: {e}", inst)

    edge_ok, why = False, ""
    for _ in range(10):
        time.sleep(20)
        edge_ok, why = v.validate_edge(env)
        if edge_ok: break
        log(f"edge ainda nao ok ({why})")
    if not edge_ok:
        # flip ja feito — NAO reverter as cegas; anterior mantida p/ rollback
        st.update(instance_id=new_id, machine_id=offer.get("machine_id"),
                  pending_id=None, mode=pick["mode"], bid_price=pick["bid"],
                  cost_total=round(pick["cost"]["total"], 5),
                  geolocation=offer.get("geolocation"))
        save_state(st)
        v.notify(env, f"pod 3060: novo {new_id} flipado mas edge falhou ({why}); "
                      f"anterior {old_id} MANTIDA p/ rollback manual")
        log("edge FALHOU — anterior preservada"); sys.exit(2)
    log("edge ok")

    if old_id:
        c = vast_destroy(env, old_id)
        log(f"anterior {old_id} destruida -> HTTP {c}")
    cost = pick["cost"]
    st.update(instance_id=new_id, machine_id=offer.get("machine_id"),
              pending_id=None, mode=pick["mode"], bid_price=pick["bid"],
              cost_total=round(cost["total"], 5),
              geolocation=offer.get("geolocation"),
              wd_needs_pod=False, wd_fail_streak=0)
    st.pop("pending_mode", None)
    st.pop("pending_bid", None)
    save_state(st)
    v.notify(env, f"pod 3060 UP (fresco): {new_id} machine {offer.get('machine_id')} "
                  f"({offer.get('geolocation')}) modo={pick['mode']} "
                  f"${cost['hourly']:.4f} + storage ${cost['storage_h']:.4f} = "
                  f"${cost['total']:.4f}/h, disco {DISK_GB}G) {ip} "
                  f"8000->{ports['8000/tcp']} 7998->{ports['7998/tcp']}")
    # id bom JA persistido no state -> seguro varrer o resto do label
    sweep_orphans(env, keep_id=new_id, context="start")
    log("PROVISION completo")


def cmd_stop(env):
    """20:00 — DESTROI (custo noturno zero; manha nasce fresco no mercado).

    State vazio NAO e' mais early-return: e' justamente o caso em que a orfa
    sobrevivia (nunca entrou no state) — segue pro sweep.
    """
    st = load_state()
    iid = st.get("instance_id")
    if not iid:
        log("stop: sem instancia no state — seguindo pro sweep por label")
    else:
        c = vast_destroy(env, iid)
        log(f"destroy noturno {iid} -> HTTP {c}")
        st.update(instance_id=None)
        save_state(st)
    # no stop o alvo e' destruir TUDO do label, sem excecao
    sweep_orphans(env, keep_id=None, context="stop")
    # pending (se havia) e' do mesmo label -> ja varrido acima
    st = load_state()
    if st.get("pending_id"):
        log(f"stop: limpando pending_id {st.get('pending_id')}")
        st["pending_id"] = None
        save_state(st)


# ------------------------------------------------------------------ watchdog
# quick 261001-cwi (A3): pod interruptivel so e' aceitavel com watchdog que
# reprovisiona sozinho. Timer a cada 3 min 07:00-19:59 BRT.
#
# Discricao (documentada): sinal Vast terminal + health OK NAO destroi de
# imediato porque `exited` ja foi visto transiente (Phase 12 D-02) -> exige K
# checagens. Vast terminal + health falhando = imediato (assinatura de outbid:
# "When outbid, the instance moves to stopped", vast-cli SKILL.md). "Health
# falha" = as 3 portas mortas (pod/container fora); falha parcial so loga —
# reprovisionar o pod inteiro por 1 servico deixaria os 4 upstreams em fallback
# por 1-2h. API Vast com erro NUNCA conta como preempcao.
WATCHDOG_K = 3            # 3 checagens x 3 min = 9 min
WINDOW_START_H = 7
WINDOW_END_H = 20         # exclusivo: ultima checagem 19:59
REPROVISION_CUTOFF_H = 18  # provisao leva ~1-2h; stop das 20:00 mataria o pod novo
RETRIGGER_MIN = 30
HEALTH_PORTS = ("8000/tcp", "7998/tcp", "8021/tcp")
START_UNIT = "vast-unified-start.service"


def _brt(dt):
    return dt.astimezone(TZ)


def in_watchdog_window(dt):
    """PURA. True em [07:00, 20:00) BRT (dt timezone-aware)."""
    return WINDOW_START_H <= _brt(dt).hour < WINDOW_END_H


def reprovision_allowed(dt):
    """PURA. False a partir de 18:00 BRT."""
    return _brt(dt).hour < REPROVISION_CUTOFF_H


def is_terminal(inst):
    """PURA. Instancia parada/saida (shape de outbid: exited + intended stopped)."""
    if not inst:
        return False
    return (inst.get("actual_status") in ("exited", "stopped")
            or inst.get("intended_status") == "stopped"
            or inst.get("cur_state") == "stopped")


def watchdog_decision(in_window, start_running, instance_id, vast_state, inst, health_ok,
                      fail_streak, k=WATCHDOG_K, needs_pod=False, minutes_since_trigger=None):
    """PURA. -> (action, new_streak). action em: noop_window, noop_start,
    noop_none, retrigger, noop_api, preempted, suspect, ok."""
    streak = int(fail_streak or 0)
    if not in_window:
        return ("noop_window", streak)
    if start_running:
        return ("noop_start", 0)
    if instance_id is None:
        if needs_pod and (minutes_since_trigger is None
                          or minutes_since_trigger >= RETRIGGER_MIN):
            return ("retrigger", 0)
        return ("noop_none", 0)
    if vast_state == "error":
        return ("noop_api", streak)
    if vast_state == "gone":
        return ("preempted", 0)
    if is_terminal(inst):
        if not health_ok:
            return ("preempted", 0)
        streak += 1
        return ("preempted", 0) if streak >= k else ("suspect", streak)
    if not health_ok:
        streak += 1
        return ("preempted", 0) if streak >= k else ("suspect", streak)
    return ("ok", 0)


def vast_get_state(env, iid):
    """-> ("ok", inst) | ("gone", None) | ("error", None).
    200 + instances preenchido = ok; 200 + instances vazio/None ou 404 = gone
    (HIPOTESE: shape exato de instancia inexistente nao confirmado — ambos
    tratados); qualquer outro codigo (incl. 0 = rede) ou JSON invalido = error."""
    c, raw = v.http("GET", f"{VAST}/instances/{iid}/",
                    {"Authorization": f"Bearer {env['VAST_API_KEY']}"})
    if c == 404:
        return ("gone", None)
    if c != 200:
        return ("error", None)
    try:
        inst = json.loads(raw).get("instances")
    except Exception:
        return ("error", None)
    if isinstance(inst, list):
        inst = inst[0] if inst and isinstance(inst[0], dict) else None
    if not inst:
        return ("gone", None)
    return ("ok", inst)


def pod_health(inst):
    """False SO quando as 3 portas (8000/7998/8021) falham; parcial = True + WARN.
    Sem ip/ports = False."""
    ip = (inst or {}).get("public_ipaddr")
    ports = (inst or {}).get("ports") or {}
    if not ip or not ports:
        return False
    alive, dead = [], []
    for k in HEALTH_PORTS:
        try:
            hp = int(ports[k][0]["HostPort"])
        except Exception:
            dead.append(k)
            continue
        (alive if health(ip, hp) else dead).append(k)
    if alive and dead:
        log(f"watchdog: WARN health parcial — mortas {dead}, vivas {alive} (sem acao)")
    return bool(alive)


def start_running():
    """True se o start.service esta active/activating OU o start.lock esta
    ocupado. Na duvida (excecao) -> True (conservador: nao dispara)."""
    try:
        r = subprocess.run(["systemctl", "is-active", START_UNIT],
                           capture_output=True, text=True, timeout=15)
        if r.stdout.strip() in ("active", "activating"):
            return True
        return start_lock_held()
    except Exception as e:
        log(f"watchdog: start_running excecao {e} -> assumindo True")
        return True


def trigger_start():
    """Dispara o start via systemd (--no-block): reusa as 3 tentativas, o
    TimeoutStartSec de 6h e o lock nativo do systemd (nao herda o timeout do
    watchdog). argv fixo, sem input externo."""
    r = subprocess.run(["systemctl", "start", "--no-block", START_UNIT],
                       capture_output=True, text=True, timeout=30)
    log(f"watchdog: systemctl start --no-block {START_UNIT} -> rc={r.returncode} "
        f"{(r.stderr or '').strip()[-300:]}")
    return r.returncode == 0


def _minutes_since(iso, now):
    if not iso:
        return None
    try:
        return (now - datetime.fromisoformat(iso)).total_seconds() / 60
    except Exception:
        return None


def cmd_watchdog(env, now=None):
    """Checagem de preempcao (timer 3 min). Fora da janela: sai sem I/O de rede."""
    now = now or datetime.now(TZ)
    if not in_watchdog_window(now):
        log(f"watchdog: fora da janela ({_brt(now):%H:%M} BRT) — noop")
        return "noop_window"
    st = load_state()
    iid = st.get("instance_id")
    pending = st.get("pending_id")
    running = start_running() or bool(pending and pending != iid)
    vstate, inst, hok = None, None, False
    if iid is not None and not running:
        vstate, inst = vast_get_state(env, iid)
        if vstate == "ok":
            hok = pod_health(inst)
    action, streak = watchdog_decision(
        True, running, iid, vstate, inst, hok, st.get("wd_fail_streak", 0),
        k=WATCHDOG_K, needs_pod=bool(st.get("wd_needs_pod")),
        minutes_since_trigger=_minutes_since(st.get("wd_last_trigger"), now))
    if st.get("wd_fail_streak", 0) != streak:
        st["wd_fail_streak"] = streak
        save_state(st)

    if action == "ok":
        log(f"watchdog: ok ({iid})")
    elif action == "suspect":
        log(f"watchdog: SUSPEITO {iid} streak {streak}/{WATCHDOG_K} "
            f"vast={(inst or {}).get('actual_status')}/{(inst or {}).get('intended_status')} "
            f"health={hok}")
    elif action == "preempted":
        why = (f"vast={vstate} actual={(inst or {}).get('actual_status')} "
               f"intended={(inst or {}).get('intended_status')} "
               f"cur_state={(inst or {}).get('cur_state')} "
               f"msg={((inst or {}).get('status_msg') or '')[:120]!r} health={hok}")
        log(f"watchdog: PREEMPTADO {iid} — {why}")
        c = vast_destroy(env, iid)  # parada segue cobrando storage; 404 ok
        log(f"watchdog: destroy {iid} -> HTTP {c}")
        today = today_brt()
        n = bump_preempt(st, today)
        machine, geo, old_mode = st.get("machine_id"), st.get("geolocation"), st.get("mode")
        st.update(instance_id=None, wd_fail_streak=0)
        save_state(st)
        conf = cfg(env)
        next_mode = choose_mode(conf["mode"], n, conf["max_preempt"])
        if reprovision_allowed(now):
            st.update(wd_needs_pod=True, wd_last_trigger=now.isoformat())
            save_state(st)
            trigger_start()
            v.notify(env, f"pod 3060 PREEMPTADO/morto ({iid}, machine {machine}, {geo}, "
                          f"modo {old_mode}): {why}. Preempcoes hoje: {n}. Instancia "
                          f"destruida; reprovisionando agora em modo {next_mode} "
                          f"(fallback do gateway ate o pod novo subir)")
        else:
            st.update(wd_needs_pod=False)
            save_state(st)
            v.notify(env, f"pod 3060 preemptado apos {REPROVISION_CUTOFF_H}h ({iid}, machine "
                          f"{machine}, {geo}) — sem reprovisao hoje, fallback ate amanha. "
                          f"Preempcoes hoje: {n}")
    elif action == "retrigger":
        if reprovision_allowed(now):
            st.update(wd_last_trigger=now.isoformat())
            save_state(st)
            log("watchdog: pod ainda ausente apos reprovisao — re-disparando start")
            trigger_start()
        else:
            st.update(wd_needs_pod=False)
            save_state(st)
            log(f"watchdog: retrigger apos {REPROVISION_CUTOFF_H}h — desistindo hoje")
    else:
        log(f"watchdog: {action} (instance={iid} pending={pending})")
    return action


def disk_pct(inst):
    space, usage = inst.get("disk_space") or 0, inst.get("disk_usage") or 0
    return round(usage / space * 100) if space else None


def cmd_disk(env):
    st = load_state()
    iid = st.get("instance_id")
    inst = vast_get(env, iid) if iid else None
    if not inst:
        log("disk: sem instancia"); return
    pct = disk_pct(inst)
    log(f"disk: {pct}% ({inst.get('disk_usage')}/{inst.get('disk_space')} GB)")
    if pct is not None and pct >= 90:
        v.notify(env, f"DISCO do pod 3060 em {pct}% — guard nao deu conta")


def cmd_status(env):
    st = load_state()
    iid = st.get("instance_id")
    inst = (vast_get(env, iid) or {}) if iid else {}
    print(json.dumps({"instance_id": iid,
                      "actual_status": inst.get("actual_status"),
                      "ip": inst.get("public_ipaddr"),
                      "gpu_temp": inst.get("gpu_temp"),
                      "ports": inst.get("ports")}, indent=1))


def cmd_flip_stack_legacy(env):
    """LEGADO (manual): flipa as 4 envs do stack 38 p/ a instancia do state
    via PUT no Portainer. Recria a task do gateway."""
    st = load_state()
    iid = st.get("instance_id")
    if not iid:
        log("flip-stack-legacy: sem instance_id no state"); sys.exit(1)
    inst = vast_get(env, iid) or {}
    ip = inst.get("public_ipaddr")
    if not ip or not inst.get("ports"):
        log(f"flip-stack-legacy: instancia {iid} sem ip/ports"); sys.exit(1)
    ports = {k: int(p[0]["HostPort"]) for k, p in inst["ports"].items()}
    flip_stack(env, ip, ports)  # legacy: so via subcomando manual


USAGE = "uso: unified3060.py {start [instance_id]|stop|status|disk|watchdog|flip-stack-legacy}"

if __name__ == "__main__":
    if len(sys.argv) < 2:
        print(USAGE, file=sys.stderr)
        sys.exit(64)
    cmd = sys.argv[1]
    if cmd not in ("start", "stop", "status", "disk", "watchdog", "flip-stack-legacy"):
        print(f"comando desconhecido '{cmd}'. {USAGE}", file=sys.stderr)
        sys.exit(64)
    e = v.load_env()
    if cmd == "start":
        resume = int(sys.argv[2]) if len(sys.argv) > 2 else None
        if resume:
            cmd_start(e, resume)
        else:
            # ate 3 tentativas por manha: maquina ruim entra no avoid dentro
            # do fail() e a proxima tentativa pega outra (2026-09-08: boot
            # timeout unico deixou o dia inteiro sem pod)
            for tent in range(1, 4):
                log(f"tentativa {tent}/3")
                try:
                    cmd_start(e)
                    break
                except SystemExit as ex:
                    # exit 2 = flip ja feito com edge duvidoso — decisao
                    # humana, NAO re-provisionar por cima
                    if tent == 3 or ex.code != 1:
                        raise
                    log(f"tentativa {tent} falhou (exit {ex.code}); re-tentando")
    elif cmd == "stop":
        cmd_stop(e)
    elif cmd == "status":
        cmd_status(e)
    elif cmd == "disk":
        cmd_disk(e)
    elif cmd == "watchdog":
        cmd_watchdog(e)
    elif cmd == "flip-stack-legacy":
        cmd_flip_stack_legacy(e)
