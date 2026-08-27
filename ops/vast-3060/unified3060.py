#!/usr/bin/env python3
"""Scheduler do pod 3060 UNIFICADO (STT+TTS+rerank+embed, instance fixa).

Substitui o vast3060.py (up->destroy, speaches-only) para a era unificada
(2026-08-26): a instancia 48611358 e' PRESERVADA — stop de noite, start de
manha. No start, se o Vast remapear IP/portas, flipa as 4 envs do stack 38
e valida via edge. Sem provision de maquina nova (pod tem Infinity dual-model
instalado no disco; recriar do zero exige o onstart dual + pip install).

Uso: unified3060.py {start|stop|status|disk}
Secrets: /etc/onboard/secrets/vast-3060.env (VAST_API_KEY, PORTAINER_API_KEY,
DINASTIA_BASE_URL, DINASTIA_TOKEN, NOTIFY_PHONE).
"""
import json, os, sys, time, urllib.request

INSTANCE = 48611358
STACK = 38  # ai-gateway-prod
PORTAINER = "https://portainer3.ifixtelecom.com.br/api"
VAST = "https://console.vast.ai/api/v0"
# envs do stack 38 -> porta INTERNA do pod
ENVMAP = {
    "UPSTREAM_STT_URL": "8000/tcp",
    "UPSTREAM_TTS_KOKORO_URL": "8000/tcp",
    "UPSTREAM_RERANK_URL": "7998/tcp",
    "UPSTREAM_EMBED_GPU_URL": "7998/tcp",
}

def log(m): print(f"[unified3060] {m}", flush=True)

def envfile():
    out = {}
    with open("/etc/onboard/secrets/vast-3060.env") as f:
        for l in f:
            l = l.strip()
            if l and not l.startswith("#") and "=" in l:
                k, v = l.split("=", 1)
                out[k] = v.strip().strip('"')
    return out

def http(method, url, headers=None, body=None, timeout=30):
    req = urllib.request.Request(url, method=method,
        data=json.dumps(body).encode() if body is not None else None)
    for k, v in (headers or {}).items(): req.add_header(k, v)
    if body is not None: req.add_header("content-type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()
    except Exception as e:
        return 0, str(e)

def vast_get(env):
    c, raw = http("GET", f"{VAST}/instances/{INSTANCE}/",
                  {"Authorization": f"Bearer {env['VAST_API_KEY']}"})
    if c != 200: return None
    return json.loads(raw).get("instances")

def vast_state(env, state):
    return http("PUT", f"{VAST}/instances/{INSTANCE}/",
                {"Authorization": f"Bearer {env['VAST_API_KEY']}"},
                {"state": state})

def notify(env, msg):
    base, tok, phone = env.get("DINASTIA_BASE_URL"), env.get("DINASTIA_TOKEN"), env.get("NOTIFY_PHONE")
    if not (base and tok and phone): return
    http("POST", f"{base}/chat/send/text", {"token": tok},
         {"Phone": phone, "Body": f"[unified3060] {msg}"})

def health(ip, port, path="/health"):
    c, _ = http("GET", f"http://{ip}:{port}{path}", timeout=8)
    return c == 200


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
    """Planta/garante o disk-guard NO POD via ssh (best-effort).

    Necessario porque o PUT de onstart na API Vast retorna success mas NAO
    aplica (gotcha validado 2026-08-27) — entao o guard nao sobrevive ao
    stop/start diario por si so; o start das 07:00 re-garante aqui. Key do
    pedro (registrada na conta Vast); ssh_host/ssh_port vem da API.
    """
    import subprocess
    host, port = inst.get("ssh_host"), inst.get("ssh_port")
    if not (host and port):
        log("ensure_guard: sem ssh_host/port na API"); return False
    remote = (
        "cat > /root/disk-guard.sh <<'DGEOF'\n" + GUARD_SCRIPT + "DGEOF\n"
        "chmod +x /root/disk-guard.sh\n"
        "pgrep -f 'bash /root/disk-guard.sh' >/dev/null || "
        "setsid /root/disk-guard.sh </dev/null >/dev/null 2>&1 &\n"
        "sleep 1; pgrep -f 'bash /root/disk-guard.sh' >/dev/null && echo GUARD_OK"
    )
    try:
        r = subprocess.run(
            ["ssh", "-i", "/home/pedro/.ssh/id_ed25519", "-p", str(port),
             "-o", "StrictHostKeyChecking=no", "-o", "BatchMode=yes",
             "-o", "ConnectTimeout=15", f"root@{host}", remote],
            capture_output=True, text=True, timeout=60)
        ok = "GUARD_OK" in r.stdout
        log(f"ensure_guard: {'ok' if ok else 'FALHOU: ' + (r.stderr or r.stdout)[-200:]}")
        return ok
    except Exception as e:
        log(f"ensure_guard: excecao {e}"); return False

def disk_pct(inst):
    space, usage = inst.get("disk_space") or 0, inst.get("disk_usage") or 0
    return round(usage / space * 100) if space else None

def cmd_disk(env):
    """Check de saude do disco via API Vast (disk_usage/disk_space, em GB).

    >=90% -> alerta WhatsApp (disco cheio quebra o multipart spool do speaches:
    upload >1MiB vira HTTP 400 — incidente 25-27/08, transcricoes perdidas).
    O guard no pod limpa em 85%; se chegou a 90% a limpeza nao deu conta.
    """
    inst = vast_get(env)
    if not inst:
        log("disk: instancia nao encontrada"); return
    pct = disk_pct(inst)
    log(f"disk: {pct}% ({inst.get('disk_usage')}/{inst.get('disk_space')} GB)")
    if pct is not None and pct >= 90:
        notify(env, f"DISCO do pod 3060 em {pct}% — guard nao deu conta; "
                    "limpar manualmente ou recriar o pod")

def cmd_stop(env):
    c, _ = vast_state(env, "stopped")
    log(f"stop -> HTTP {c}")

def cmd_start(env):
    vast_state(env, "running")
    inst = None
    for _ in range(60):  # ate 15min
        time.sleep(15)
        inst = vast_get(env)
        if inst and inst.get("actual_status") == "running" and inst.get("ports"):
            break
    else:
        notify(env, "pod unificado NAO subiu em 15min — verificar")
        log("timeout start"); sys.exit(1)
    ip = inst["public_ipaddr"]
    ports = {k: v[0]["HostPort"] for k, v in inst["ports"].items()}
    log(f"running ip={ip} ports={ports}")
    # espera servicos (speaches + infinity dual sobem via onstart; infinity
    # carrega 2 modelos, da' uns minutos)
    ok8000 = ok7998 = False
    for _ in range(60):  # ate 15min
        ok8000 = ok8000 or health(ip, ports["8000/tcp"])
        ok7998 = ok7998 or health(ip, ports["7998/tcp"])
        if ok8000 and ok7998: break
        time.sleep(15)
    if not (ok8000 and ok7998):
        notify(env, f"pod up mas servicos nao-healthy (8000={ok8000} 7998={ok7998})")
    # compara/flipa envs do stack 38
    hdr = {"X-API-Key": env["PORTAINER_API_KEY"]}
    c, raw = http("GET", f"{PORTAINER}/stacks/{STACK}", hdr)
    stack = json.loads(raw)
    changed = []
    for e in stack["Env"]:
        want_port = ENVMAP.get(e["name"])
        if want_port:
            want = f"http://{ip}:{ports[want_port]}"
            if e["value"] != want:
                changed.append(f"{e['name']}: {e['value']} -> {want}")
                e["value"] = want
    if changed:
        c2, raw2 = http("GET", f"{PORTAINER}/stacks/{STACK}/file", hdr)
        content = json.loads(raw2)["StackFileContent"]
        c3, _ = http("PUT", f"{PORTAINER}/stacks/{STACK}?endpointId={stack['EndpointId']}",
                     hdr, {"stackFileContent": content, "env": stack["Env"],
                           "prune": False, "pullImage": False}, timeout=120)
        log(f"envs flipadas ({len(changed)}) -> PUT {c3}: {changed}")
        notify(env, f"pod remapeado; stack 38 atualizado: {', '.join(changed)}")
    else:
        log("envs do stack 38 ja corretas")
    # disk-guard: re-planta a cada start (onstart PUT nao persiste — gotcha) e
    # ja reporta o estado do disco do dia.
    ensure_guard(inst)
    pct = disk_pct(inst)
    if pct is not None and pct >= 90:
        notify(env, f"pod 3060 iniciado com disco em {pct}% — atencao")

def cmd_status(env):
    inst = vast_get(env) or {}
    print(json.dumps({"actual_status": inst.get("actual_status"),
                      "ip": inst.get("public_ipaddr"),
                      "ports": inst.get("ports")}, indent=1))

if __name__ == "__main__":
    e = envfile()
    {"start": cmd_start, "stop": cmd_stop, "status": cmd_status, "disk": cmd_disk}[sys.argv[1]](e)
