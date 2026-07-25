#!/usr/bin/env python3
"""Agenda + auto-migração do pod STT/TTS dedicado (Vast, classe 3060).

quick 260723 (agenda 7-22) + quick 260724-ktg (auto-migração).

O pod roda speaches (faster-whisper-large-v3 + Kokoro-82M TTS) numa GPU barata
de mercado spot. Stop/start do Vast prende a instância no MESMO host (o disco é
local); se um terceiro aluga a GPU enquanto estamos parados, o start fica
bloqueado indefinidamente. Este script:

  ensure   (timer 15min, 07-21h59) — religa se não estiver running+healthy;
           fail_count >= MIGRATE_THRESHOLD (~2h) dispara migrate().
  stop     (timer 22:00) — para a instância (paga só storage).
  migrate  — provisiona substituta, instala modelos, valida direto E via edge,
           flipa os envs do stack 38 (Portainer), destrói a velha, atualiza o
           state e notifica no WhatsApp. Falha pré-flip → destrói a nova e
           mantém a velha (estado intacto).
  status   — dump do state + situação live.

State:   /var/lib/vast-3060/state.json  {instance_id, machine_id, ip, port,
         fail_count, machine_avoid[]}
Secrets: /etc/onboard/secrets/vast-3060.env (root-600)
Log:     /var/log/vast-3060-sched.log
"""

from __future__ import annotations

import io
import json
import math
import struct
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from datetime import datetime
from pathlib import Path

STATE_PATH = Path("/var/lib/vast-3060/state.json")
SECRETS_PATH = Path("/etc/onboard/secrets/vast-3060.env")
LOG_PATH = Path("/var/log/vast-3060-sched.log")

VAST = "https://console.vast.ai/api/v0"
PORTAINER = "https://portainer3.ifixtelecom.com.br/api"
STACK_ID = 38
STACK_ENDPOINT = 6
EDGE = "https://ai-gateway.converse-ai.app"

IMAGE = "ghcr.io/speaches-ai/speaches:0.9.0-rc.3-cuda-12.6.3"
MODELS = ["Systran/faster-whisper-large-v3", "speaches-ai/Kokoro-82M-v1.0-ONNX"]

WINDOW_START_H = 7   # janela 07:00–21:59 (stop às 22:00 pelo timer)
WINDOW_END_H = 22
MIGRATE_THRESHOLD = 8   # ensures falhos consecutivos (~2h a cada 15min)

# Oferta substituta: mesma régua do vetting manual de 2026-07-23.
# PRICE_CAP é o teto do Pedro; quando o mercado seca (0 ofertas — visto
# 2026-07-25, dia inteiro sem pod por $0,005/h), escala 1.3x/1.6x/2x em vez de
# ficar mudo. A escalação é POR TENTATIVA (não persiste): a próxima migração
# volta a tentar o teto base primeiro.
PRICE_CAP = 0.035
CAP_STEPS = [1.0, 1.3, 1.6, 2.0]
OFFER_QUERY = {
    "gpu_name": {"in": ["RTX 3060", "RTX 3060 Ti", "RTX 3080"]},
    "num_gpus": {"eq": 1},
    "rentable": {"eq": True},
    "type": "on-demand",
    "gpu_ram": {"gte": 8000},
    "reliability2": {"gte": 0.97},
    "inet_down": {"gte": 100},
}


def log(msg: str) -> None:
    line = f"[{datetime.now():%F %T}] {msg}"
    print(line)
    with LOG_PATH.open("a") as f:
        f.write(line + "\n")


def load_env() -> dict:
    env = {}
    for raw in SECRETS_PATH.read_text().splitlines():
        raw = raw.strip()
        if raw and not raw.startswith("#") and "=" in raw:
            k, v = raw.split("=", 1)
            env[k] = v
    return env


def http(method: str, url: str, headers: dict | None = None, body: bytes | None = None,
         timeout: int = 30) -> tuple[int, bytes]:
    req = urllib.request.Request(url, data=body, headers=headers or {}, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()
    except Exception as e:  # dial/timeout — chamador decide
        return 0, str(e).encode()


def http_json(method: str, url: str, headers: dict | None = None, payload=None,
              timeout: int = 30):
    h = dict(headers or {})
    body = None
    if payload is not None:
        h["Content-Type"] = "application/json"
        body = json.dumps(payload).encode()
    code, raw = http(method, url, h, body, timeout)
    try:
        return code, json.loads(raw)
    except Exception:
        return code, {"_raw": raw[:300].decode(errors="replace")}


def load_state() -> dict:
    return json.loads(STATE_PATH.read_text())


def save_state(st: dict) -> None:
    STATE_PATH.parent.mkdir(parents=True, exist_ok=True)
    tmp = STATE_PATH.with_suffix(".tmp")
    tmp.write_text(json.dumps(st, indent=2))
    tmp.replace(STATE_PATH)


# ---------------------------------------------------------------- Vast helpers

def vast_headers(env: dict) -> dict:
    return {"Authorization": f"Bearer {env['VAST_API_KEY']}"}


def get_instance(env: dict, inst_id: int) -> dict | None:
    code, data = http_json("GET", f"{VAST}/instances/", vast_headers(env), timeout=25)
    if code != 200:
        return None
    for i in data.get("instances", []):
        if i.get("id") == inst_id:
            return i
    return None


def pod_health(ip: str, port: int) -> bool:
    code, raw = http("GET", f"http://{ip}:{port}/health", timeout=6)
    return code == 200 and b"OK" in raw


def instance_addr(inst: dict) -> tuple[str, int] | None:
    ip = inst.get("public_ipaddr")
    p = (inst.get("ports") or {}).get("8000/tcp")
    if not ip or not p:
        return None
    return ip, int(p[0]["HostPort"])


# ------------------------------------------------------------------- WhatsApp

def notify(env: dict, text: str) -> None:
    """Best-effort — nunca derruba o fluxo por falha de notificação."""
    try:
        code, _ = http_json(
            "POST", f"{env['DINASTIA_BASE_URL']}/chat/send/text",
            {"token": env["DINASTIA_TOKEN"]},
            {"Phone": env["NOTIFY_PHONE"], "Body": text}, timeout=20)
        log(f"notify -> HTTP {code}")
    except Exception as e:
        log(f"notify FALHOU: {e}")


# ------------------------------------------------------------------ validação

def synth_wav(seconds: float = 2.0) -> bytes:
    """WAV 16k mono senoide — suficiente pro caminho STT responder 200."""
    sr = 16000
    n = int(sr * seconds)
    frames = b"".join(
        struct.pack("<h", int(3000 * math.sin(2 * math.pi * 440 * i / sr)))
        for i in range(n))
    hdr = (b"RIFF" + struct.pack("<I", 36 + len(frames)) + b"WAVEfmt "
           + struct.pack("<IHHIIHH", 16, 1, 1, sr, sr * 2, 2, 16)
           + b"data" + struct.pack("<I", len(frames)))
    return hdr + frames


def multipart(fields: dict, file_field: str, filename: str, blob: bytes) -> tuple[bytes, str]:
    boundary = uuid.uuid4().hex
    out = io.BytesIO()
    for k, v in fields.items():
        out.write(f"--{boundary}\r\nContent-Disposition: form-data; "
                  f'name="{k}"\r\n\r\n{v}\r\n'.encode())
    out.write(f"--{boundary}\r\nContent-Disposition: form-data; "
              f'name="{file_field}"; filename="{filename}"\r\n'
              f"Content-Type: audio/wav\r\n\r\n".encode())
    out.write(blob)
    out.write(f"\r\n--{boundary}--\r\n".encode())
    return out.getvalue(), f"multipart/form-data; boundary={boundary}"


def validate_pod_direct(ip: str, port: int) -> tuple[bool, str]:
    body, ct = multipart({"model": MODELS[0], "language": "pt"}, "file", "probe.wav",
                         synth_wav())
    code, _ = http("POST", f"http://{ip}:{port}/v1/audio/transcriptions",
                   {"Content-Type": ct}, body, timeout=90)
    if code != 200:
        return False, f"STT direto HTTP {code}"
    code, _ = http_json("POST", f"http://{ip}:{port}/v1/audio/speech", None,
                        {"model": "tts-1", "input": "validação de migração",
                         "voice": "pm_alex", "response_format": "wav"}, timeout=90)
    if code != 200:
        return False, f"TTS direto HTTP {code}"
    return True, "ok"


def validate_edge(env: dict) -> tuple[bool, str]:
    body, ct = multipart({"model": "whisper"}, "file", "probe.wav", synth_wav())
    code, _ = http("POST", f"{EDGE}/v1/audio/transcriptions",
                   {"Content-Type": ct,
                    "Authorization": f"Bearer {env['GW_STT_KEY']}"}, body, timeout=90)
    if code != 200:
        return False, f"STT edge HTTP {code}"
    code, _ = http_json("POST", f"{EDGE}/v1/audio/speech",
                        {"Authorization": f"Bearer {env['GW_TTS_KEY']}"},
                        {"model": "tts-1", "input": "gateway ok", "voice": "pm_alex",
                         "response_format": "wav"}, timeout=90)
    if code != 200:
        return False, f"TTS edge HTTP {code}"
    return True, "ok"


# ------------------------------------------------------------------ Portainer

def portainer_flip(env: dict, ip: str, port: int) -> None:
    h = {"X-API-Key": env["PORTAINER_API_KEY"]}
    _, stack = http_json("GET", f"{PORTAINER}/stacks/{STACK_ID}", h)
    _, filed = http_json("GET", f"{PORTAINER}/stacks/{STACK_ID}/file", h)
    envs = stack["Env"]
    target = f"http://{ip}:{port}"
    hit = 0
    for e in envs:
        if e["name"] in ("UPSTREAM_STT_URL", "UPSTREAM_TTS_KOKORO_URL"):
            e["value"] = target
            hit += 1
    if hit != 2:
        raise RuntimeError(f"esperava 2 envs no stack {STACK_ID}, achei {hit}")
    code, resp = http_json(
        "PUT", f"{PORTAINER}/stacks/{STACK_ID}?endpointId={STACK_ENDPOINT}", h,
        {"StackFileContent": filed["StackFileContent"], "Env": envs,
         "Prune": False, "PullImage": False}, timeout=180)
    if code != 200:
        raise RuntimeError(f"PUT stack {STACK_ID} HTTP {code}: {resp}")
    log(f"stack {STACK_ID} flipado -> {target}")


# -------------------------------------------------------------------- migrate

def pick_offer(env: dict, avoid: list[int]) -> dict | None:
    q = urllib.parse.quote(json.dumps(OFFER_QUERY))
    code, data = http_json("GET", f"{VAST}/bundles/?q={q}", vast_headers(env), timeout=40)
    if code != 200:
        return None
    offers = [o for o in data.get("offers", []) if o.get("machine_id") not in avoid]
    offers.sort(key=lambda o: o.get("dph_total", 9))
    for mult in CAP_STEPS:
        cap = PRICE_CAP * mult
        hit = [o for o in offers if o.get("dph_total", 9) <= cap]
        if hit:
            if mult > 1.0:
                log(f"pick_offer: teto escalado {mult}x -> ${cap:.4f} (mercado seco no base)")
            return hit[0]
    return None


def migrate(env: dict, st: dict, reason: str) -> None:
    old_id = st["instance_id"]
    avoid = list(set(st.get("machine_avoid", []) + [st.get("machine_id")]))
    log(f"MIGRATE start (motivo: {reason}); avoid={avoid}")

    offer = pick_offer(env, avoid)
    if offer is None:
        log("MIGRATE abortada: nenhuma oferta elegível (mesmo com teto 2x)")
        # dedup: notifica só na TRANSIÇÃO pra "sem oferta" (2026-07-25 o loop
        # de 15min virou spam de WhatsApp o dia todo).
        if not st.get("alerted_no_offer"):
            st["alerted_no_offer"] = True
            save_state(st)
            notify(env, f"⚠️ pod 3060: bloqueado ({reason}) e SEM oferta "
                        f"substituta até ${PRICE_CAP * CAP_STEPS[-1]:.3f}/h. "
                        f"STT em tier-1, TTS mudo. Re-tento a cada 15min "
                        f"(silencioso até resolver).")
        return

    code, resp = http_json(
        "PUT", f"{VAST}/asks/{offer['id']}/", vast_headers(env),
        {"client_id": "me", "image": IMAGE, "disk": 20,
         "label": "stt-tts-3060-auto",
         "env": {"-p 8000:8000": "1", "WHISPER_MODEL": MODELS[0]},
         "runtype": "args"}, timeout=60)
    new_id = resp.get("new_contract")
    if code != 200 or not new_id:
        log(f"MIGRATE: create falhou HTTP {code}: {resp}")
        notify(env, f"❌ pod 3060: migração falhou no create ({code}). Velha mantida.")
        return
    log(f"MIGRATE: criada {new_id} (machine {offer.get('machine_id')}, "
        f"${offer.get('dph_total', 0):.4f}/h, {offer.get('geolocation')})")

    def bail(step: str) -> None:
        log(f"MIGRATE: {step} — destruindo a nova {new_id}, velha mantida")
        http("DELETE", f"{VAST}/instances/{new_id}/", vast_headers(env), timeout=40)
        # máquina que falhou o provision entra no avoid — sem isso o próximo
        # migrate escolhe a mesma barata-ruim de novo (drill 1: 38103 CN).
        bad = offer.get("machine_id")
        if bad and bad not in st.get("machine_avoid", []):
            st.setdefault("machine_avoid", []).append(bad)
            save_state(st)
        notify(env, f"❌ pod 3060: migração abortada em '{step}' "
                    f"(machine {bad} → avoid). Instância velha mantida; "
                    f"re-tento no próximo ciclo.")

    # boot + health (~8min cap)
    addr = None
    for _ in range(32):
        time.sleep(15)
        inst = get_instance(env, new_id)
        if inst and inst.get("actual_status") == "running":
            addr = instance_addr(inst)
            if addr and pod_health(*addr):
                break
    else:
        return bail("boot/health timeout 8min")
    ip, port = addr
    log(f"MIGRATE: nova healthy em {ip}:{port}")

    for m in MODELS:
        code, resp = http_json("POST", f"http://{ip}:{port}/v1/models/{m}", None, None,
                               timeout=600)
        if code != 200:
            return bail(f"install {m} HTTP {code}")
    log("MIGRATE: modelos instalados")

    ok, why = validate_pod_direct(ip, port)
    if not ok:
        return bail(f"validação direta: {why}")
    log("MIGRATE: validação direta ok")

    try:
        portainer_flip(env, ip, port)
    except Exception as e:
        return bail(f"flip portainer: {e}")

    # gateway reinicia no PUT — dar folga e validar via edge com retry
    edge_ok = False
    for _ in range(10):
        time.sleep(20)
        ok, why = validate_edge(env)
        if ok:
            edge_ok = True
            break
        log(f"MIGRATE: edge ainda não ok ({why}), retry")
    if not edge_ok:
        # flip já aconteceu — NÃO reverter às cegas; humano decide.
        notify(env, f"⚠️ pod 3060: migrei pra {ip}:{port} e flipei o gateway, mas a "
                    f"validação via edge falhou ({why}). Instância velha {old_id} "
                    f"MANTIDA pra rollback manual. Verificar!")
        st.update(instance_id=new_id, machine_id=offer.get("machine_id"),
                  ip=ip, port=port, fail_count=0, alerted_no_offer=False)
        save_state(st)
        return
    log("MIGRATE: validação via edge ok")

    http("DELETE", f"{VAST}/instances/{old_id}/", vast_headers(env), timeout=40)
    log(f"MIGRATE: velha {old_id} destruída")

    # machine_avoid persiste APENAS máquinas que falharam provision (bail).
    # A máquina "atual" (ocupada por terceiro) NÃO entra: ela é boa — só
    # estava alugada — e pode voltar ao pool no futuro. (Bug 2026-07-25:
    # persistir o avoid da busca baniu as 2 melhores máquinas pra sempre.)
    st.update(instance_id=new_id, machine_id=offer.get("machine_id"),
              ip=ip, port=port, fail_count=0, alerted_no_offer=False)
    save_state(st)
    notify(env, f"✅ pod 3060 MIGRADO: machine {offer.get('machine_id')} "
                f"({offer.get('geolocation')}, ${offer.get('dph_total', 0):.4f}/h), "
                f"{ip}:{port}. STT+TTS validados via gateway. Velha destruída.")
    log("MIGRATE: completa")


# ------------------------------------------------------------------- comandos

def cmd_ensure(env: dict, st: dict) -> None:
    h = datetime.now().hour
    if h < WINDOW_START_H or h >= WINDOW_END_H:
        return  # fora da janela — quem manda é o stop das 22h
    inst = get_instance(env, st["instance_id"])
    if inst and inst.get("actual_status") == "running":
        addr = instance_addr(inst)
        if addr and pod_health(*addr):
            if st.get("fail_count"):
                st["fail_count"] = 0
                save_state(st)
            return
    # não está servindo — tenta religar
    http_json("PUT", f"{VAST}/instances/{st['instance_id']}/", vast_headers(env),
              {"state": "running"}, timeout=40)
    for _ in range(8):
        time.sleep(15)
        if pod_health(st["ip"], st["port"]):
            log("ensure: START OK")
            st["fail_count"] = 0
            save_state(st)
            return
    st["fail_count"] = st.get("fail_count", 0) + 1
    save_state(st)
    log(f"ensure: bloqueada (fail_count={st['fail_count']}/{MIGRATE_THRESHOLD})")
    if st["fail_count"] >= MIGRATE_THRESHOLD:
        migrate(env, st, f"{st['fail_count']} ensures falhos (~2h) na "
                         f"machine {st.get('machine_id')}")


def cmd_stop(env: dict, st: dict) -> None:
    code, _ = http_json("PUT", f"{VAST}/instances/{st['instance_id']}/",
                        vast_headers(env), {"state": "stopped"}, timeout=40)
    log(f"stop -> HTTP {code}")


def cmd_status(env: dict, st: dict) -> None:
    inst = get_instance(env, st["instance_id"])
    live = inst.get("actual_status") if inst else "GONE"
    healthy = pod_health(st["ip"], st["port"]) if inst else False
    print(json.dumps({"state": st, "live_status": live, "healthy": healthy}, indent=2))


def main() -> int:
    cmd = sys.argv[1] if len(sys.argv) > 1 else ""
    if cmd not in ("ensure", "stop", "migrate", "status"):
        print("uso: vast3060.py ensure|stop|migrate|status")
        return 1
    env = load_env()
    st = load_state()
    if cmd == "ensure":
        cmd_ensure(env, st)
    elif cmd == "stop":
        cmd_stop(env, st)
    elif cmd == "migrate":
        migrate(env, st, "manual/drill")
    else:
        cmd_status(env, st)
    return 0


if __name__ == "__main__":
    sys.exit(main())
