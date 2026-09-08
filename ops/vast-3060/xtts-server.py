#!/usr/bin/env python3
"""Servidor TTS XTTS-v2 OpenAI-compatible no pod unificado (porta 8021).

POST /v1/audio/speech {input, voice?, speed?} -> audio/wav
GET  /health -> 200 (so depois do modelo carregado)

Vozes: ana/alma/luis/marcos (estudio XTTS) + aliases de compat com as vozes
Kokoro que os clientes ja mandam (pm_alex/pf_dora/pm_santa). Velocidade
default 1.15 (decisao Pedro 2026-09-07: 1.0 arrastado p/ atendimento).
Idioma fixo pt. Formato fixo wav (response_format ignorado).
Licenca XTTS = Coqui CPML (nao-comercial) — decisao Pedro 2026-09-07 de usar
mesmo assim, registrada no SUMMARY da quick 260907-poc.
"""
import json, os, re, tempfile, threading, wave
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

os.environ.setdefault("COQUI_TOS_AGREED", "1")
from TTS.api import TTS  # noqa: E402

VOICES = {
    "ana": "Ana Florence",
    "alma": "Alma María",
    "luis": "Luis Moray",
    "marcos": "Marcos Rudaski",
    # compat: vozes Kokoro usadas pelos clientes atuais
    "pf_dora": "Ana Florence",
    "pm_alex": "Luis Moray",
    "pm_santa": "Marcos Rudaski",
}
DEFAULT_SPEED = 1.2  # 1.15 ainda lento (feedback Pedro 2026-09-08)


def limpa_markdown(t):
    """Clientes mandam texto com markdown e o XTTS le 'asterisco asterisco'."""
    t = re.sub(r"```.*?```", " ", t, flags=re.S)
    t = re.sub(r"`([^`]*)`", r"\1", t)
    t = re.sub(r"!\[[^\]]*\]\([^)]*\)", " ", t)
    t = re.sub(r"\[([^\]]+)\]\([^)]*\)", r"\1", t)
    t = re.sub(r"^\s{0,3}#{1,6}\s+", "", t, flags=re.M)
    t = re.sub(r"(\*\*|__|~~|\*|_)", "", t)
    t = re.sub(r"^\s*[-*•>]\s+", "", t, flags=re.M)
    t = re.sub(r"^\s*\|.*\|\s*$", " ", t, flags=re.M)
    return re.sub(r"\s+", " ", t).strip()


def agrupa_numeros(t):
    """Numero grande (id/protocolo) lido em grupos de 3 digitos com pausa —
    '50.255.728' vira '502, 557, 28' em vez de 'cinquenta milhoes...'.
    Valores monetarios (R$ ...) e decimais com virgula ficam intactos."""
    money = []

    def keep(m):
        money.append(m.group(0))
        return f"\x00M{len(money)-1}\x00"

    t = re.sub(r"R\$\s?[\d.,]+", keep, t)

    def rep(m):
        digits = re.sub(r"\D", "", m.group(0))
        gs = [digits[i:i+3] for i in range(0, len(digits), 3)]
        if len(gs) > 1 and len(gs[-1]) == 1:  # evita grupo solto de 1 digito
            ult4 = gs[-2] + gs[-1]
            gs[-2:] = [ult4[:2], ult4[2:]]
        return ", ".join(gs)

    t = re.sub(r"\b\d{1,3}(?:\.\d{3})+\b(?!,\d)|\b\d{5,}\b", rep, t)
    for i, mv in enumerate(money):
        t = t.replace(f"\x00M{i}\x00", mv)
    return t


def em_chunks(t, cap=180):
    """XTTS trunca texto longo numa geracao so — dividir por sentencas e
    concatenar os wavs (feedback 2026-09-08: 'ultimo texto nao foi lido
    completamente')."""
    partes = re.split(r"(?<=[.!?;:\n])\s+", t)
    out, cur = [], ""
    for p in partes:
        while len(p) > cap:
            corte = p.rfind(",", 40, cap)
            if corte < 0:
                corte = p.rfind(" ", 40, cap)
            if corte < 0:
                corte = cap
            if cur:
                out.append(cur)
                cur = ""
            out.append(p[:corte + 1])
            p = p[corte + 1:].lstrip()
        if len(cur) + len(p) + 1 <= cap:
            cur = (cur + " " + p).strip()
        else:
            if cur:
                out.append(cur)
            cur = p
    if cur:
        out.append(cur)
    return [c for c in out if c.strip()]

print("carregando XTTS-v2...", flush=True)
tts = TTS("tts_models/multilingual/multi-dataset/xtts_v2").to("cuda")
try:
    KNOWN = set(tts.synthesizer.tts_model.speaker_manager.speaker_names)
except Exception:
    KNOWN = set()
print("XTTS pronto", flush=True)
lock = threading.Lock()


class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def do_GET(self):
        if self.path == "/health":
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b"OK")
        else:
            self.send_response(404)
            self.end_headers()

    def do_POST(self):
        if self.path != "/v1/audio/speech":
            self.send_response(404)
            self.end_headers()
            return
        try:
            n = int(self.headers.get("content-length", 0))
            body = json.loads(self.rfile.read(n))
            text = body["input"]
            raw_voice = str(body.get("voice") or "ana")
            voice = VOICES.get(raw_voice.lower()) or \
                (raw_voice if raw_voice in KNOWN else "Ana Florence")
            speed = float(body.get("speed") or DEFAULT_SPEED)
            text = agrupa_numeros(limpa_markdown(text))
            pedacos = em_chunks(text) or [text]
            paths = []
            with lock:
                for pd in pedacos:
                    with tempfile.NamedTemporaryFile(suffix=".wav",
                                                     delete=False) as f:
                        paths.append(f.name)
                    tts.tts_to_file(text=pd, language="pt", speaker=voice,
                                    speed=speed, file_path=paths[-1])
            if len(paths) == 1:
                data = open(paths[0], "rb").read()
            else:  # concatena wavs (mesmo sr/canais — tudo do mesmo modelo)
                frames, params = [], None
                for p in paths:
                    with wave.open(p, "rb") as w:
                        params = params or w.getparams()
                        frames.append(w.readframes(w.getnframes()))
                with tempfile.NamedTemporaryFile(suffix=".wav", delete=False) as f:
                    outp = f.name
                with wave.open(outp, "wb") as w:
                    w.setparams(params)
                    for fr in frames:
                        w.writeframes(fr)
                data = open(outp, "rb").read()
                paths.append(outp)
            for p in paths:
                try:
                    os.unlink(p)
                except OSError:
                    pass
            self.send_response(200)
            self.send_header("content-type", "audio/wav")
            self.send_header("content-length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
        except Exception as e:
            msg = json.dumps({"error": {"message": str(e)[:300]}}).encode()
            self.send_response(400)
            self.send_header("content-type", "application/json")
            self.send_header("content-length", str(len(msg)))
            self.end_headers()
            self.wfile.write(msg)


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 8021), H).serve_forever()
