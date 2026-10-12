"""Testes do pre-processamento de texto do xtts-server.py (sem carregar o modelo).

Rodar: python3 -m pytest ops/vast-3060/test_xtts_text.py -q
"""
import os
import re

HERE = os.path.dirname(os.path.abspath(__file__))
_src = open(os.path.join(HERE, "xtts-server.py")).read()
_src = _src[:_src.index('print("carregando XTTS-v2')].replace(
    "from TTS.api import TTS  # noqa: E402", "")
NS = {}
exec(_src, NS)


def test_por_extenso():
    pe = NS["por_extenso"]
    assert pe(0) == "zero"
    assert pe(1) == "um"
    assert pe(16) == "dezesseis"
    assert pe(21) == "vinte e um"
    assert pe(100) == "cem"
    assert pe(101) == "cento e um"
    assert pe(189) == "cento e oitenta e nove"
    assert pe(1000) == "mil"
    assert pe(1200) == "mil e duzentos"
    assert pe(1250) == "mil duzentos e cinquenta"
    assert pe(2005) == "dois mil e cinco"
    assert pe(1_000_000) == "um milhão"
    assert pe(2_500_000) == "dois milhões quinhentos mil"


def test_dinheiro():
    d = NS["dinheiro"]
    assert d("valor de R$ 189,90, vence") == \
        "valor de cento e oitenta e nove reais e noventa centavos, vence"
    assert d("R$ 1.250,00") == "mil duzentos e cinquenta reais"
    assert d("R$ 0,50") == "cinquenta centavos"
    assert d("R$ 1,00") == "um real"
    assert d("R$1,01") == "um real e um centavo"
    assert d("R$ 99,9") == "noventa e nove reais e noventa centavos"
    assert d("R$ 10") == "dez reais"
    assert d("R$ 1.000.000,00") == "um milhão de reais"


def test_horas():
    h = NS["horas"]
    assert h("entre 8h e 12h de amanhã") == "entre 8 horas e 12 horas de amanhã"
    assert h("às 1h") == "às 1 hora"
    assert h("8h30") == "8h30"  # normaliza_ptbr cuida desse formato


def test_em_frases_sem_ponto_final():
    fr = NS["em_frases"]("Recebemos o seu pagamento. Obrigado por escolher a iFix. "
                         "Tudo certo? Tenha um ótimo dia!")
    assert fr == ["Recebemos o seu pagamento", "Obrigado por escolher a iFix",
                  "Tudo certo?", "Tenha um ótimo dia!"]
    assert not any(s.endswith(".") for s in fr)


def test_em_frases_texto_longo_sem_ponto():
    t = ("Depois que você enviar, a operadora anterior tem até três dias úteis para "
         "liberar a linha, e durante esse período o seu número continua funcionando "
         "normalmente, e você recebe um SMS assim que a troca for concluída. Ok.")
    fr = NS["em_frases"](t)
    assert all(len(s) <= 181 for s in fr)
    assert not any(s.endswith(".") for s in fr)
    assert "".join(fr).replace(" ", "").replace(",", "").replace(".", "") == \
        t.replace(" ", "").replace(",", "").replace(".", "")


def test_pipeline_fatura():
    ns = NS
    t = "Sua fatura, no valor de R$ 189,90, vence no dia 15/10. Técnico entre 8h e 12h."
    out = ns["em_frases"](ns["agrupa_numeros"](ns["horas"](ns["dinheiro"](
        ns["normaliza_ptbr"](ns["limpa_markdown"](t))))))
    assert out == ["Sua fatura, no valor de cento e oitenta e nove reais e noventa "
                   "centavos, vence no dia 15 de outubro",
                   "Técnico entre 8 horas e 12 horas"]
    assert not re.search(r"R\$", " ".join(out))
