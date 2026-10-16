"""Avaliação PT-BR de rerank: top-1 e MRR + latência (10 docs/consulta, ~600-900 chars)."""
import json, sys, time, urllib.request, statistics
URL, MODEL = sys.argv[1], sys.argv[2]
pad = " Atendimento de segunda a sexta, das 8h às 18h, pelo WhatsApp e pelo telefone 0800."
D = {
 "fibra500": "O plano Fibra 500 Mega custa R$ 99,90 por mês, com instalação gratuita, roteador Wi-Fi 6 em comodato e fidelidade de 12 meses.",
 "fibra1g": "O plano Fibra 1 Giga custa R$ 149,90 por mês e inclui ponto adicional de Wi-Fi mesh sem custo nos primeiros seis meses.",
 "cancel": "Para cancelar o contrato basta pedir pela central; se ainda houver fidelidade, a multa é proporcional aos meses restantes.",
 "segunda_via": "A segunda via do boleto pode ser emitida no aplicativo, no site ou pedindo ao atendente, que envia o PDF por WhatsApp.",
 "atraso": "Faturas pagas com atraso têm multa de 2% e juros de 1% ao mês; após 15 dias o serviço pode ser reduzido.",
 "lento": "Se a internet estiver lenta, reinicie o roteador por 30 segundos e teste pelo cabo; persistindo, abrimos chamado técnico.",
 "mudanca": "A mudança de endereço é gratuita se o novo endereço tiver cobertura; o técnico agenda a reinstalação em até 5 dias úteis.",
 "vacina_gripe": "A vacina da gripe tetravalente custa R$ 120 e é aplicada a partir de 6 meses de idade, sem necessidade de agendamento.",
 "vacina_hpv": "A vacina HPV nonavalente é aplicada em duas doses para pessoas de 9 a 14 anos e em três doses a partir dos 15 anos.",
 "horario_clinica": "A clínica funciona de segunda a sábado, das 7h às 19h; aos domingos apenas para vacinação agendada.",
 "convenio": "Aceitamos Unimed, Bradesco Saúde e SulAmérica para consultas; vacinas são particulares, com parcelamento em 3x.",
 "senha_wifi": "Para trocar a senha do Wi-Fi acesse 192.168.0.1 com o usuário admin e a senha da etiqueta do roteador.",
 "tv": "O pacote de TV por streaming tem 80 canais por R$ 39,90 e pode ser adicionado a qualquer plano de fibra.",
 "telefone_fixo": "A linha de telefone fixo VoIP custa R$ 19,90 com ligações ilimitadas para fixos de todo o Brasil.",
}
Q = [
 ("quanto custa a internet de 500 megas?", "fibra500"),
 ("qual o valor do plano de 1 giga?", "fibra1g"),
 ("quero cancelar, tem multa?", "cancel"),
 ("como eu tiro a segunda via da conta?", "segunda_via"),
 ("paguei atrasado, quanto de juros?", "atraso"),
 ("minha net tá muito lenta o que eu faço", "lento"),
 ("vou mudar de casa, como fica a internet?", "mudanca"),
 ("preço da vacina da gripe", "vacina_gripe"),
 ("quantas doses da vacina de hpv para minha filha de 12 anos?", "vacina_hpv"),
 ("vocês abrem domingo?", "horario_clinica"),
 ("aceita unimed?", "convenio"),
 ("como mudo a senha do wifi", "senha_wifi"),
 ("tem pacote de canais de tv?", "tv"),
]
keys = list(D)
top1, rr, lat = 0, [], []
for q, gold in Q:
    others = [k for k in keys if k != gold]
    cand = [gold] + others[:9]
    cand = cand[3:] + cand[:3]          # ouro fora da 1ª posição
    docs = [(D[k] + pad) * 4 for k in cand]
    docs = [d[:800] for d in docs]
    body = json.dumps({"model": MODEL, "query": q, "documents": docs}).encode()
    t = time.time()
    res = json.loads(urllib.request.urlopen(urllib.request.Request(URL, body, {"Content-Type": "application/json"}), timeout=180).read())
    lat.append(time.time() - t)
    order = [r["index"] for r in sorted(res["results"], key=lambda r: -r["relevance_score"])]
    rank = order.index(cand.index(gold)) + 1
    top1 += rank == 1; rr.append(1 / rank)
print(json.dumps({"model": MODEL, "top1": f"{top1}/{len(Q)}", "mrr": round(statistics.mean(rr), 3),
                  "lat_p50_s": round(statistics.median(lat), 2), "lat_max_s": round(max(lat), 2)}))
