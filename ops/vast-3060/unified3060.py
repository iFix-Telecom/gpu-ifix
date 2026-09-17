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

Uso: unified3060.py {start|stop|status|disk}
  start  — provisiona pod novo, valida (health+GPU+STT/TTS/embed/rerank),
           flipa 4 envs do stack 38, valida via edge, destroi instancia
           anterior, persiste instance_id no state.
  stop   — DESTROI a instancia do state.
State:   /var/lib/vast-3060/state.json (compartilhado com o legado vast3060:
         instance_id, machine_id, machine_avoid[])
Secrets: /etc/onboard/secrets/vast-3060.env (VAST_API_KEY, PORTAINER_API_KEY,
         DINASTIA_BASE_URL, DINASTIA_TOKEN, NOTIFY_PHONE, GW_STT_KEY, GW_TTS_KEY)
Arquivos irmaos (deployados em /opt/vast-3060/): onstart-unified.sh,
         infinity-freeze.txt, vast3060.py (helpers reutilizados).
"""
import base64, json, os, subprocess, sys, time, urllib.parse, urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import vast3060 as v  # helpers: http, http_json, load_env, validate_*, notify

STACK = 38  # ai-gateway-prod
PORTAINER = "https://portainer3.ifixtelecom.com.br/api"
VAST = "https://console.vast.ai/api/v0"
STATE_PATH = "/var/lib/vast-3060/state.json"
HERE = os.path.dirname(os.path.abspath(__file__))
DISK_GB = 40  # stack instalado ~19-23G; 25G vivia a 90% (incidente multipart 400)
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

def log(m): print(f"[unified3060] {m}", flush=True)

def load_state():
    try:
        return json.load(open(STATE_PATH))
    except Exception:
        return {"instance_id": None, "machine_avoid": []}

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


def pick_offer(env, avoid):
    """Oferta 3060 mais barata, excluindo machine_avoid e CN (portas publicas
    de maquinas CN inacessiveis — 38103/146752, 2026-09-02)."""
    q = urllib.parse.quote(json.dumps(v.OFFER_QUERY))
    c, data = v.http_json("GET", f"{VAST}/bundles/?q={q}",
                          {"Authorization": f"Bearer {env['VAST_API_KEY']}"},
                          timeout=40)
    if c != 200:
        return None
    offers = [o for o in data.get("offers", [])
              if o.get("machine_id") not in avoid
              and "CN" not in (o.get("geolocation") or "")]
    offers.sort(key=lambda o: o.get("dph_total", 9))
    for mult in v.CAP_STEPS:
        cap = v.PRICE_CAP * mult
        hit = [o for o in offers if o.get("dph_total", 9) <= cap]
        if hit:
            if mult > 1.0:
                log(f"pick_offer: teto escalado {mult}x -> ${cap:.4f}")
            return hit[0]
    return None


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


def flip_stack(env, ip, ports):
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
    """Provisiona pod FRESCO, valida, flipa stack 38, destroi o anterior.
    resume_id: continua orquestracao numa instancia ja criada (pos-falha
    transiente) em vez de criar outra."""
    st = load_state()
    old_id = st.get("instance_id")
    avoid = list(st.get("machine_avoid", []))
    log(f"PROVISION start; old={old_id} resume={resume_id} avoid={avoid}")

    if resume_id:
        new_id = resume_id
        if old_id == new_id:
            old_id = None  # nao destruir a si mesma no final
        offer = {"machine_id": (vast_get(env, new_id) or {}).get("machine_id"),
                 "dph_total": 0.0, "geolocation": "resume"}
    else:
        offer = pick_offer(env, avoid)
        if offer is None:
            v.notify(env, "pod 3060: SEM oferta elegivel (nem teto 2x) — tier-0 fora, "
                          "gateway nos fallbacks")
            log("sem oferta"); sys.exit(1)
        log(f"oferta: machine {offer['machine_id']} ${offer.get('dph_total', 0):.4f}/h "
            f"{offer.get('geolocation')}")

        c, resp = v.http_json(
            "PUT", f"{VAST}/asks/{offer['id']}/",
            {"Authorization": f"Bearer {env['VAST_API_KEY']}"},
            {"client_id": "me", "image": v.IMAGE, "disk": DISK_GB,
             "label": "stt-tts-rerank-unified",
             "onstart": build_onstart(),
             "env": {"-p 8000:8000": "1", "-p 7998:7998": "1",
                     "-p 8021:8021": "1",
                     "WHISPER_MODEL": v.MODELS[0],
                     "HF_HOME": "/root/.cache/huggingface"},
             "runtype": "ssh"}, timeout=60)
        new_id = resp.get("new_contract")
        if c != 200 or not new_id:
            v.notify(env, f"pod 3060: create falhou HTTP {c}")
            log(f"create falhou {c}: {resp}"); sys.exit(1)
        log(f"criada {new_id}")

    def fail(step, inst=None):
        """Falha de provision: diagnostica via journal e SEMPRE destroi a nova
        (leak de GPU paga era o bug 2026-09-17: 4 orfas / $13,01 queimados —
        os caminhos health/install/validacao/flip chamavam fail() sem pedir o
        destroy e a instancia viva nunca voltava pra ninguem).
        Ordem importa: diag() ANTES do destroy, senao a evidencia morre com a
        instancia. Machine -> avoid."""
        log(f"FALHA em '{step}'")
        if inst:
            diag(env, inst)
        c = vast_destroy(env, new_id)
        log(f"nova {new_id} destruida -> HTTP {c}")
        bad = offer.get("machine_id")
        if bad and bad not in st.get("machine_avoid", []):
            st.setdefault("machine_avoid", []).append(bad)
            save_state(st)
        v.notify(env, f"pod 3060: provision falhou em '{step}' "
                      f"(machine {bad} -> avoid; nova {new_id} DESTRUIDA, "
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
        flip_stack(env, ip, ports)
    except Exception as e:
        return fail(f"flip stack: {e}", inst)

    edge_ok, why = False, ""
    for _ in range(10):
        time.sleep(20)
        edge_ok, why = v.validate_edge(env)
        if edge_ok: break
        log(f"edge ainda nao ok ({why})")
    if not edge_ok:
        # flip ja feito — NAO reverter as cegas; anterior mantida p/ rollback
        st.update(instance_id=new_id, machine_id=offer.get("machine_id"))
        save_state(st)
        v.notify(env, f"pod 3060: novo {new_id} flipado mas edge falhou ({why}); "
                      f"anterior {old_id} MANTIDA p/ rollback manual")
        log("edge FALHOU — anterior preservada"); sys.exit(2)
    log("edge ok")

    if old_id:
        c = vast_destroy(env, old_id)
        log(f"anterior {old_id} destruida -> HTTP {c}")
    st.update(instance_id=new_id, machine_id=offer.get("machine_id"))
    save_state(st)
    v.notify(env, f"pod 3060 UP (fresco): {new_id} machine {offer.get('machine_id')} "
                  f"({offer.get('geolocation')}, ${offer.get('dph_total', 0):.4f}/h, "
                  f"disco {DISK_GB}G) {ip} "
                  f"8000->{ports['8000/tcp']} 7998->{ports['7998/tcp']}")
    log("PROVISION completo")


def cmd_stop(env):
    """20:00 — DESTROI (custo noturno zero; manha nasce fresco no mercado)."""
    st = load_state()
    iid = st.get("instance_id")
    if not iid:
        log("stop: sem instancia no state"); return
    c = vast_destroy(env, iid)
    log(f"destroy noturno {iid} -> HTTP {c}")
    st.update(instance_id=None)
    save_state(st)


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


if __name__ == "__main__":
    e = v.load_env()
    cmd = sys.argv[1]
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
