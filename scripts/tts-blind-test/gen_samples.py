#!/usr/bin/env python3
"""Gerador de amostras cegas de TTS PT-BR (fase 23, plano 23-17, HAIKU-08).

Sintetiza as mesmas 5 frases em cada candidato externo (OpenRouter
POST /api/v1/audio/speech, response_format mp3) e no Kokoro atual via gateway
(controle), confere o contrato MP3 de cada resposta, descarta quem falhar e
grava as amostras com letras embaralhadas.

Somente stdlib. Segredos SÓ por env (nunca em arquivo):
  OPENROUTER_API_KEY  chave OpenRouter (exportada do container do gateway)
  GATEWAY_TTS_KEY     chave de tenant do gateway (claude-tts) para o controle

Uso:
  python3 gen_samples.py [--out DIR] [--only modelo1,modelo2] [--no-control]

Saída em DIR (default /home/pedro/tts-blind-test/AAAA-MM-DD):
  amostra-<letra>-frase<n>.mp3   amostras cegas (nome não revela o modelo)
  .gabarito.json (modo 600)      letra -> modelo, voz, latência, custo
  candidatos.json                contrato por candidato SEM letra (vai p/ evidence)
"""

import argparse
import datetime as dt
import json
import os
import random
import string
import sys
import time
import urllib.error
import urllib.request

OPENROUTER_URL = "https://openrouter.ai/api/v1/audio/speech"
OPENROUTER_MODELS_URL = "https://openrouter.ai/api/v1/models?output_modalities=speech"
GATEWAY_URL = "https://ai-gateway.converse-ai.app/v1/audio/speech"

FRASES = [
    # 1. saudação no estilo das respostas faladas do claude-wpp
    "Oi, Pedro! Aqui é o assistente da iFix. Terminei a tarefa que você pediu "
    "e já deixei tudo registrado no card.",
    # 2. valor em reais
    "O valor total da fatura é de R$ 1.234,56, com vencimento no próximo dia útil.",
    # 3. data e hora
    "A visita técnica ficou marcada para 22/10/2026, às 14h30, no horário de Brasília.",
    # 4. protocolo
    "Seu protocolo de atendimento é 2026-4471-88. Guarde esse número para "
    "acompanhar o chamado.",
    # 5. resposta técnica longa (~300 caracteres)
    "Verifiquei o servidor e o problema estava no certificado do proxy, que tinha "
    "vencido ontem à noite. Renovei o certificado, reiniciei o serviço e conferi "
    "os acessos: está tudo respondendo normalmente agora. Se o erro voltar, me "
    "avise que eu reviso os registros com mais calma.",
]

# Candidatos externos. Voz escolhida por modelo conforme `supported_voices` de
# GET /api/v1/models?output_modalities=speech (consultado em 2026-10-15).
# Modelos sem voz pt-BR nomeada (ElevenLabs, Gemini, Grok) são multilíngues com
# detecção automática de idioma pelo texto (descrição do modelo na mesma API);
# a voz foi escolhida masculina para ficar comparável ao controle (pm_alex).
CANDIDATOS = [
    {"model": "elevenlabs/eleven-multilingual-v2", "voice": "eric",
     "fonte": "supported_voices (lista ElevenLabs); modelo 29 idiomas"},
    {"model": "elevenlabs/eleven-v3", "voice": "eric",
     "fonte": "supported_voices (lista ElevenLabs); modelo 70+ idiomas"},
    {"model": "elevenlabs/eleven-v4", "voice": "eric",
     "fonte": "supported_voices (lista ElevenLabs); modelo 90+ idiomas"},
    {"model": "google/gemini-3.8-flash-tts", "voice": "Charon",
     "fonte": "supported_voices (vozes Gemini TTS); idioma pelo texto"},
    {"model": "microsoft/mai-voice-2.1", "voice": "pt-BR-Rafael:MAI-Voice-2.1",
     "fonte": "supported_voices: voz pt-BR nativa"},
    {"model": "x-ai/grok-voice-tts-1.0", "voice": "rex",
     "fonte": "supported_voices; modelo 20+ idiomas com detecção automática"},
]

CONTROLE = {"model": "tts-1", "voice": "pm_alex", "via": "gateway",
            "fonte": "Kokoro atual servido pelo gateway (tenant claude-tts)"}


def is_mp3(data: bytes) -> bool:
    """Cabeçalho MP3: tag ID3 ou frame sync 0xFFF (11 bits)."""
    if len(data) < 4:
        return False
    if data[:3] == b"ID3":
        return True
    return data[0] == 0xFF and (data[1] & 0xE0) == 0xE0


def post(url: str, token: str, body: dict, timeout: int):
    req = urllib.request.Request(
        url,
        data=json.dumps(body).encode(),
        headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
        method="POST",
    )
    t0 = time.monotonic()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            data = resp.read()
            return resp.status, resp.headers.get("Content-Type", ""), data, time.monotonic() - t0
    except urllib.error.HTTPError as e:
        return e.code, e.headers.get("Content-Type", ""), e.read(), time.monotonic() - t0
    except Exception as e:  # timeout, DNS, TLS
        return 0, "", str(e).encode(), time.monotonic() - t0


def precos() -> dict:
    """US$ por caractere (pricing.prompt) e por token de saída, por modelo."""
    try:
        with urllib.request.urlopen(OPENROUTER_MODELS_URL, timeout=30) as r:
            d = json.load(r)["data"]
        return {m["id"]: (float(m["pricing"].get("prompt") or 0),
                          float(m["pricing"].get("completion") or 0)) for m in d}
    except Exception:
        return {}


def sintetiza(cand: dict, or_key: str, gw_key: str) -> dict:
    res = {"model": cand["model"], "voice": cand["voice"], "fonte": cand["fonte"],
           "via": cand.get("via", "openrouter"), "frases": [], "audios": []}
    for i, frase in enumerate(FRASES, 1):
        if res["via"] == "gateway":
            body = {"model": cand["model"], "input": frase, "voice": cand["voice"],
                    "response_format": "mp3"}
            st, ct, data, lat = post(GATEWAY_URL, gw_key, body, 300)
        else:
            body = {"model": cand["model"], "input": frase, "voice": cand["voice"],
                    "response_format": "mp3"}
            st, ct, data, lat = post(OPENROUTER_URL, or_key, body, 120)
        ok = st == 200 and is_mp3(data)
        info = {"frase": i, "status": st, "content_type": ct, "bytes": len(data),
                "latencia_s": round(lat, 2), "mp3_ok": ok}
        if not ok:
            info["erro"] = data[:300].decode("utf-8", "replace")
        res["frases"].append(info)
        res["audios"].append(data if ok else None)
        print(f"  {cand['model']} frase{i}: {st} {ct} {len(data)}B {lat:.1f}s mp3={ok}",
              file=sys.stderr)
        if not ok:
            break  # descarta o candidato inteiro: não gasta nas frases restantes
    res["mp3_ok"] = all(f["mp3_ok"] for f in res["frases"]) and len(res["frases"]) == len(FRASES)
    if not res["mp3_ok"]:
        f = res["frases"][-1]
        res["descartado"] = f"frase{f['frase']}: HTTP {f['status']} {f['content_type']} " \
                            f"mp3_ok={f['mp3_ok']} {f.get('erro', '')[:200]}"
    return res


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default=f"/home/pedro/tts-blind-test/{dt.date.today().isoformat()}")
    ap.add_argument("--only", default="", help="modelos separados por vírgula")
    ap.add_argument("--no-control", action="store_true")
    a = ap.parse_args()

    or_key = os.environ.get("OPENROUTER_API_KEY", "")
    gw_key = os.environ.get("GATEWAY_TTS_KEY", "")
    if not or_key or (not gw_key and not a.no_control):
        print("faltam OPENROUTER_API_KEY e/ou GATEWAY_TTS_KEY no env", file=sys.stderr)
        return 2

    cands = [c for c in CANDIDATOS if not a.only or c["model"] in a.only.split(",")]
    if not a.no_control:
        cands.append(CONTROLE)
    preco = precos()
    chars = sum(len(f) for f in FRASES)

    resultados = []
    for c in cands:
        print(f"== {c['model']} / {c['voice']}", file=sys.stderr)
        resultados.append(sintetiza(c, or_key, gw_key))

    aceitos = [r for r in resultados if r["mp3_ok"]]
    letras = list(string.ascii_uppercase[: len(aceitos)])
    random.SystemRandom().shuffle(aceitos)

    os.makedirs(a.out, exist_ok=True)
    gabarito = {}
    for letra, r in zip(letras, aceitos):
        for i, audio in enumerate(r["audios"], 1):
            with open(os.path.join(a.out, f"amostra-{letra}-frase{i}.mp3"), "wb") as fh:
                fh.write(audio)
        p_in, p_out = preco.get(r["model"], (0.0, 0.0))
        lats = [f["latencia_s"] for f in r["frases"]]
        gabarito[letra] = {
            "model": r["model"], "voice": r["voice"], "via": r["via"],
            "latencia_media_s": round(sum(lats) / len(lats), 2),
            "latencias_s": lats,
            "chars_5_frases": chars,
            "usd_por_char": p_in,
            "custo_estimado_usd_5_frases": round(chars * p_in, 6),
            "obs_custo": ("controle local (CPU), custo externo 0" if r["via"] == "gateway"
                          else ("tem preço de token de áudio de saída além do texto; "
                                "estimativa só da entrada" if p_out else "")),
        }

    gpath = os.path.join(a.out, ".gabarito.json")
    fd = os.open(gpath, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as fh:
        json.dump({"gerado_em": dt.datetime.now().isoformat(timespec="seconds"),
                   "frases": FRASES, "letras": gabarito}, fh, ensure_ascii=False, indent=2)
    os.chmod(gpath, 0o600)

    # Relatório SEM letra (ordem original dos candidatos) para o evidence.
    rel = []
    for r in resultados:
        cts = sorted({f["content_type"] for f in r["frases"]})
        rel.append({"model": r["model"], "voice": r["voice"], "via": r["via"],
                    "fonte": r["fonte"], "content_types": cts, "mp3_ok": r["mp3_ok"],
                    "descartado": r.get("descartado", "")})
    with open(os.path.join(a.out, "candidatos.json"), "w") as fh:
        json.dump(rel, fh, ensure_ascii=False, indent=2)

    print(f"aceitos={len(aceitos)} descartados={len(resultados) - len(aceitos)} out={a.out}",
          file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
