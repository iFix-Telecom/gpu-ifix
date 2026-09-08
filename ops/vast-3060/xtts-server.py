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
import json, os, tempfile, threading
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
DEFAULT_SPEED = 1.15

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
            with lock:
                with tempfile.NamedTemporaryFile(suffix=".wav", delete=False) as f:
                    path = f.name
                tts.tts_to_file(text=text, language="pt", speaker=voice,
                                speed=speed, file_path=path)
            data = open(path, "rb").read()
            os.unlink(path)
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
