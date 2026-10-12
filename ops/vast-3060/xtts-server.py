#!/usr/bin/env python3
"""Servidor TTS XTTS-v2 OpenAI-compatible no pod unificado (porta 8021).

POST /v1/audio/speech {input, voice?, speed?} -> audio/wav
GET  /health -> 200 (so depois do modelo carregado)

Vozes: ana/alma/luis/marcos (estudio XTTS) + aliases de compat com as vozes
Kokoro que os clientes ja mandam (pm_alex/pf_dora/pm_santa). Velocidade
default 1.15 (decisao Pedro 2026-09-07: 1.0 arrastado p/ atendimento).
Idioma fixo pt. Formato fixo wav (response_format ignorado).
Texto gerado frase a frase sem '.' final (XTTS le '.' como 'ponto'), R$ por
extenso em reais/centavos e '8h' -> '8 horas' (A/B 2026-10-12, card 86aku8ute).
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
    """Clientes mandam texto com markdown e o XTTS le 'asterisco asterisco'.
    NADA e' descartado em silencio (2026-09-08: tabela deletada = 'audio
    cortado' pro ouvinte): tabela vira fala (celulas por virgula, linha por
    ponto) e bloco de codigo vira aviso explicito."""
    t = re.sub(r"```.*?```", " Trecho de código omitido. ", t, flags=re.S)
    t = re.sub(r"`([^`]*)`", r"\1", t)
    t = re.sub(r"!\[[^\]]*\]\([^)]*\)", " ", t)
    t = re.sub(r"\[([^\]]+)\]\([^)]*\)", r"\1", t)
    t = re.sub(r"^\s{0,3}#{1,6}\s+", "", t, flags=re.M)
    t = re.sub(r"(\*\*|~~|\*)", "", t)
    t = re.sub(r"_", " ", t)
    t = re.sub(r"^\s*[-*•>]\s+", "", t, flags=re.M)

    def linha_tabela(m):
        cells = [c.strip() for c in m.group(0).strip().strip("|").split("|")]
        if all(re.fullmatch(r":?-{2,}:?", c) for c in cells if c):
            return " "  # linha separadora |---|---|
        falavel = [c for c in cells if c]
        return (", ".join(falavel) + ". ") if falavel else " "
    t = re.sub(r"^\s*\|.*\|\s*$", linha_tabela, t, flags=re.M)
    t = t.replace("→", ", ").replace("←", ", ")
    return re.sub(r"\s+", " ", t).strip()


MESES = {1: "janeiro", 2: "fevereiro", 3: "março", 4: "abril", 5: "maio",
         6: "junho", 7: "julho", 8: "agosto", 9: "setembro", 10: "outubro",
         11: "novembro", 12: "dezembro"}


def normaliza_ptbr(t):
    """Termos que o XTTS fala errado (validado 2026-09-08, nota Maestro):
    '#135'->'Cardinal 135', '08/09'->'8-9', '11:00'->'11-0', 'Av'->'Ave',
    '...'->'ponto ponto', '<x>' engolido."""
    t = re.sub(r"<[^>\n]{0,60}>", " ", t)          # placeholders <vacina>
    t = re.sub(r"[.]{2,}|…", ".", t)                # reticencias
    t = re.sub(r"#\s?(?=\d)", "número ", t)         # protocolo/card
    t = t.replace("#", " ")

    def data(m):
        d, mo, y = int(m.group(1)), int(m.group(2)), m.group(3)
        if not 1 <= mo <= 12 or not 1 <= d <= 31:
            return m.group(0)
        s = f"{d} de {MESES[mo]}"
        if y:
            yy = int(y)
            s += f" de {yy + 2000 if yy < 100 else yy}"
        return s
    t = re.sub(r"\b(\d{1,2})/(\d{1,2})(?:/(\d{2,4}))?\b", data, t)

    def hora(m):
        h, mi = int(m.group(1)), int(m.group(2))
        if h > 23 or mi > 59:
            return m.group(0)
        return f"{h} horas" if mi == 0 else f"{h} e {mi:02d}"
    t = re.sub(r"\b(\d{1,2})[:h](\d{2})\b", hora, t)

    abrev = [(r"\bAv\.?(?=\s)", "Avenida"), (r"\bR\.(?=\s)", "Rua"),
             (r"\bDra\.?(?=\s)", "Doutora"), (r"\bDr\.?(?=\s)", "Doutor"),
             (r"\bSra\.?(?=\s)", "Senhora"), (r"\bSr\.?(?=\s)", "Senhor"),
             (r"\bn[º°](?=\s?\d)", "número "), (r"\bhrs?\b", "horas")]
    for pat, rep in abrev:
        t = re.sub(pat, rep, t)
    return re.sub(r"\s+", " ", t)


_UNI = ["zero", "um", "dois", "três", "quatro", "cinco", "seis", "sete",
        "oito", "nove", "dez", "onze", "doze", "treze", "quatorze", "quinze",
        "dezesseis", "dezessete", "dezoito", "dezenove"]
_DEZ = ["", "", "vinte", "trinta", "quarenta", "cinquenta", "sessenta",
        "setenta", "oitenta", "noventa"]
_CEN = ["", "cento", "duzentos", "trezentos", "quatrocentos", "quinhentos",
        "seiscentos", "setecentos", "oitocentos", "novecentos"]


def _ate_999(n):
    if n == 100:
        return "cem"
    c, r = divmod(n, 100)
    p = [_CEN[c]] if c else []
    if r:
        p.append(_UNI[r] if r < 20 else
                 _DEZ[r // 10] + (" e " + _UNI[r % 10] if r % 10 else ""))
    return " e ".join(p)


def por_extenso(n):
    """Inteiro 0..999.999.999 por extenso em pt-BR (sem dependencia externa:
    o venv /opt/xtts nao garante num2words)."""
    if n == 0:
        return "zero"
    mi, resto = divmod(n, 1_000_000)
    mil, u = divmod(resto, 1000)
    p = []
    if mi:
        p.append("um milhão" if mi == 1 else _ate_999(mi) + " milhões")
    if mil:
        p.append("mil" if mil == 1 else _ate_999(mil) + " mil")
    if u:
        p.append(_ate_999(u))
    # 'e' antes do ultimo grupo so quando ele e' < 100 ou centena redonda:
    # 'mil e duzentos', 'mil duzentos e cinquenta'
    if len(p) > 1 and u and (u < 100 or u % 100 == 0):
        return " ".join(p[:-1]) + " e " + p[-1]
    return " ".join(p)


def dinheiro(t):
    """'R$ 1.250,90' -> 'mil duzentos e cinquenta reais e noventa centavos'.
    Sem isso o XTTS le 'R$' como 'dolar'/nada e erra o valor (A/B 2026-10-12)."""
    def rep(m):
        inteiro = int(m.group(1).replace(".", ""))
        cent = int((m.group(2) or "0").ljust(2, "0"))
        p = []
        if inteiro:
            de = " de" if inteiro % 1_000_000 == 0 else ""
            p.append(f"{por_extenso(inteiro)}{de} {'real' if inteiro == 1 else 'reais'}")
        if cent:
            p.append(f"{por_extenso(cent)} {'centavo' if cent == 1 else 'centavos'}")
        return " e ".join(p) or "zero reais"
    return re.sub(r"R\$\s?(\d{1,3}(?:\.\d{3})+|\d+)(?:,(\d{1,2}))?(?!\d)", rep, t)


def horas(t):
    """'8h' -> '8 horas' (normaliza_ptbr so cobre 8h30/08:00; antes saia
    'entre 8 e 12')."""
    return re.sub(r"\b(\d{1,2})h\b",
                  lambda m: m.group(1) + (" hora" if int(m.group(1)) == 1 else " horas"), t)


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
    completamente').

    Borda de chunk PRECISA terminar em pontuacao forte: terminando em ':',
    ',' ou palavra cortada o XTTS alucina silabas/'ponto'/palavras no fim
    (ouvido em prod 2026-09-08: 'conversa.i', 'analisados.oi', 'ponto' falado).
    Por isso NAO se quebra em ;/: e todo chunk e' fechado com '.'."""
    partes = re.split(r"(?<=[.!?\n])\s+", t)
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
    fechados = []
    for c in out:
        c = c.strip()
        if not c:
            continue
        if c[-1] in ",;:—-":
            c = c[:-1].rstrip() + "."
        elif c[-1] not in ".!?":
            c += "."
        fechados.append(c)
    return fechados


def em_frases(t):
    """Uma frase por geracao, SEM o '.' final: o XTTS le '.' como 'ponto'.
    A/B 2026-10-12 (15 amostras, transcricao literal): chunks com '.' = 9/15
    falaram 'ponto'; so tirando o '.' do fim do chunk = 6/15 (os '.' do meio
    ainda vazam); frase a frase sem '.' = 0/15, sem silaba alucinada no fim.
    '!' e '?' ficam (entonacao)."""
    out = []
    for c in em_chunks(t):
        for s in re.split(r"(?<=[.!?])\s+", c):
            s = s.strip()
            if s.endswith("."):
                s = s[:-1].rstrip()
            if s:
                out.append(s)
    return out


PAUSA_FRASES_S = 0.15  # silencio entre frases geradas separadamente

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
            text = agrupa_numeros(horas(dinheiro(normaliza_ptbr(
                limpa_markdown(text)))))
            pedacos = em_frases(text) or [text]
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
                pausa = b"\x00" * (int(params.framerate * PAUSA_FRASES_S)
                                   * params.sampwidth * params.nchannels)
                with wave.open(outp, "wb") as w:
                    w.setparams(params)
                    for i, fr in enumerate(frames):
                        if i:
                            w.writeframes(pausa)
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
