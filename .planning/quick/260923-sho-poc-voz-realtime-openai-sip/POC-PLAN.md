# PoC — Voz em tempo real em ligação VoIP com OpenAI gpt-realtime-2.1-mini via SIP nativo

| Campo | Valor |
|-------|-------|
| Data | 2026-09-23 |
| Origem | [SEED-009](../../seeds/SEED-009-realtime-voice-ai-phone-call.md) — Real-time voice AI em ligação telefônica ao vivo |
| Decisão | D-01 (Pedro, 2026-09-23): começar por OpenAI `gpt-realtime-2.1-mini` via SIP nativo (Arquitetura B). Etapa 2 = Gemini Live. ElevenLabs só se nenhum passar no teste de ouvido. |
| Status | **plano — nada executado** |

> Guardrail: este documento é um plano. Nenhuma ação externa ou em produção foi feita ao escrevê-lo. Toda etapa marcada com `GATE: APROVAÇÃO PEDRO` só pode começar depois de aprovação explícita do Pedro para aquela etapa específica. Nenhuma credencial (chave OpenAI, secret de webhook, tokens) deve ser escrita neste arquivo.

---

## 1. Objetivo

Provar (ou reprovar) uma conversa ao vivo humano ↔ IA numa ligação real que passa pelo Asterisk do voip-api, medindo:

- latência de turno (fim da fala do humano → primeiro áudio da IA);
- barge-in (a IA para de falar quando o humano interrompe);
- custo real por ligação (usage do projeto OpenAI);
- qualidade de compreensão e fala em PT-BR com áudio telefônico de 8 kHz (G.711);
- confiabilidade de tool calls (sem ação alucinada).

O resultado alimenta a decisão Arquitetura A (pipeline STT → LLM → TTS) vs Arquitetura B (speech-to-speech) do SEED-009, e a decisão de seguir ou não para a etapa Gemini Live.

---

## 2. Arquitetura

```
PLANO DE MÍDIA (SIP + RTP)

  [Ramal de teste]                [Asterisk voip-api — Worker-Oracle]             [OpenAI Realtime]
  softphone interno   --SIP-->    contexto ISOLADO [poc-openai-realtime]  --SIP/TLS-->  sip.api.openai.com
  (sem cliente)       <--RTP--    endpoint PJSIP `openai-realtime`        <--RTP----   (gpt-realtime-2.1-mini)
                                  via transport-tls (G.711 ulaw/alaw)

PLANO DE CONTROLE (webhook + API HTTP)

  [OpenAI]  --POST realtime.call.incoming-->  [Traefik edge, HTTPS público]  -->  [Serviço PoC nosso]
                                                                                        |
  [OpenAI]  <-------- chamada de aceite na API (instructions, tools, voz) --------------+
```

### Como os dois planos se combinam

- **Mídia (SIP/RTP):** o Asterisk origina uma chamada SIP para a OpenAI a partir de um contexto de dialplan novo e isolado. O áudio da ligação trafega em RTP entre o Asterisk e a OpenAI; o ramal de teste só conversa com o Asterisk.
- **Controle (webhook + API):** quando a chamada SIP chega na OpenAI, a OpenAI dispara o webhook `realtime.call.incoming` para a URL configurada no projeto (platform > Project > Webhooks). O serviço PoC nosso recebe esse evento, valida a assinatura e aceita a chamada via API, passando instructions (prompt de atendimento de teste), tools (1 ou 2 tools inofensivas para o roteiro) e voz. Sem o aceite, a chamada não é atendida.

### Fatos da infra atual que o plano respeita

Lidos no repositório `/home/pedro/projetos/pedro/voip-api/voip-api/docker/asterisk/` em 2026-09-23 (só leitura):

- `pjsip.conf.template` define os transports `[transport-udp]` (5070), `[transport-wss]`, `[transport-tls]` (5071, `method=tlsv1_2`) e `[transport-tls-5061]` (5061, `method=tlsv1_2`).
- `rtp.conf`: `rtpstart = 30000`, `rtpend = 30100`, `icesupport = yes`.
- `sorcery.conf`: endpoint/auth/aor/identify/registration vêm de realtime Postgres (`ps_endpoints`, `ps_auths`, `ps_aors`, `ps_identify`, `ps_registrations`).
- `extensions.conf` tem os contextos `[from-external]` (inbound do trunk → `Stasis(voip-api,...)`), `[from-extensions]`, `[route-cascade]`, `[b2bua-hangup]`, `[internal-hangup]`, `[webphone-out]`, `[from-queue]`, `[webphone-control]`.
- O Asterisk roda no **Worker-Oracle** (Portainer stack 36, endpoint 7, Tailscale 100.96.9.111). O **trunk 8880 é produção**.

Consequência: o endpoint `openai-realtime` e o contexto `[poc-openai-realtime]` são **novos e isolados**. Os contextos existentes (`[from-external]`, `[from-extensions]`, `[route-cascade]` e demais) e o trunk 8880 **não são alterados**. Onde o endpoint é criado (arquivo estático vs linha em `ps_endpoints`) é decisão da etapa 5, sob aprovação.

### Pontos a confirmar no pré-voo (etapa 0)

Não inventar — confirmar no guia oficial https://developers.openai.com/api/docs/guides/realtime-sip:

- formato exato do SIP URI (como o projeto OpenAI é identificado no URI);
- transporte e porta esperados (TLS/TCP/UDP), autenticação SIP (se houver);
- codecs aceitos (esperado G.711; confirmar ulaw vs alaw);
- formato e campos do evento `realtime.call.incoming` e da chamada de aceite;
- método de verificação da assinatura do webhook.

---

## 3. Pré-requisitos

1. **Projeto OpenAI DEDICADO à PoC**, com chave própria (nunca a de outro produto) e **limite de gasto (budget / hard cap)** configurado antes da primeira ligação.
2. **Webhook público via Traefik**: rota no edge Traefik da vps-ifix-vm apontando para o serviço PoC. Regra DNS-first: o registro DNS precisa estar propagado **antes** de carregar o router TLS (evita rate-limit do Let's Encrypt). O serviço valida a **assinatura do webhook** e rejeita qualquer requisição sem assinatura válida.
3. **Ramal + contexto de dialplan ISOLADO**: o ramal de teste só alcança `[poc-openai-realtime]`; esse contexto não recebe nem origina chamada de cliente e **não passa pelo trunk 8880**.
4. **Codec G.711** (ulaw ou alaw, conforme pré-voo) negociado entre Asterisk e OpenAI, e entre ramal e Asterisk (evitar transcodificação desnecessária).
5. **Forma de medir latência**: gravação da chamada no Asterisk (`MixMonitor` no contexto isolado) ou captura RTP no ramal de teste, com marcação de tempo por turno.
6. **Roteiro de teste escrito** (falas do humano, pontos de interrupção, pedidos que disparam tool) antes da etapa 7, para que as ligações sejam comparáveis entre OpenAI e Gemini.

---

## 4. Etapas

Cada etapa lista: o que é feito, onde, quem. Toda ação externa ou em produção tem a linha de gate.

### Etapa 0 — Pré-voo (só leitura)

- **O quê:** reler o guia SIP da OpenAI; confirmar formato do URI, transporte/porta, codecs, auth, payload do webhook e da chamada de aceite, verificação de assinatura. Listar exatamente o que muda no Asterisk (endpoint, aor, contexto, gravação) e o que NÃO muda.
- **Onde:** documentação oficial + leitura do repo voip-api.
- **Quem:** executor (Claude), sem nenhuma ação externa.
- **Saída:** adendo a este plano com os valores confirmados e a lista de mudanças no Asterisk.

### Etapa 1 — Criar projeto OpenAI, limite de gasto e chave

- **O quê:** criar projeto dedicado (ex.: `poc-voz-realtime`), configurar budget/hard cap, gerar chave só desse projeto.
- **Onde:** platform da OpenAI.
- **Quem:** Pedro (ou executor com autorização explícita).
- GATE: APROVAÇÃO PEDRO

### Etapa 2 — Subir serviço PoC do webhook

- **O quê:** serviço mínimo que recebe `realtime.call.incoming`, valida assinatura, aceita a chamada com instructions/tools/voz e registra timestamps e eventos em log.
- **Onde:** host e forma decididos nesta etapa, **sem tocar stacks de produção** (stack 36, stacks do converseai, ai-gateway).
- **Quem:** executor, após aprovação.
- GATE: APROVAÇÃO PEDRO

### Etapa 3 — DNS + rota Traefik do webhook

- **O quê:** registro DNS do subdomínio do webhook; depois da propagação, rota no edge Traefik (file-provider) só com o path necessário.
- **Onde:** Cloudflare (DNS) + edge Traefik da vps-ifix-vm.
- **Quem:** executor, após aprovação.
- GATE: APROVAÇÃO PEDRO

### Etapa 4 — Cadastrar webhook no projeto OpenAI

- **O quê:** configurar o webhook `realtime.call.incoming` apontando para a URL da etapa 3; guardar o secret de assinatura fora do repositório.
- **Onde:** platform > Project > Webhooks.
- **Quem:** Pedro (ou executor com autorização explícita).
- GATE: APROVAÇÃO PEDRO

### Etapa 5 — Endpoint PJSIP + contexto isolado no Asterisk do Worker-Oracle

- **O quê:** adicionar endpoint `openai-realtime` (via transport-tls) e contexto `[poc-openai-realtime]` com gravação; liberar o ramal de teste para discar só para esse contexto.
- **Antes de aplicar:** backup dos arquivos/linhas afetadas (dialplan + tabelas `ps_*` se o endpoint for realtime) e **diff apresentado ao Pedro**.
- **Aplicação:** `reload` (dialplan / pjsip), **não** restart do container; em janela combinada.
- **Onde:** Asterisk no Worker-Oracle (stack 36, endpoint 7).
- **Quem:** executor, após aprovação do diff.
- GATE: APROVAÇÃO PEDRO

### Etapa 6 — Ligação de fumaça (30 s)

- **O quê:** do ramal de teste, uma ligação curta: a chamada é aceita, há áudio nos dois sentidos, a IA responde em PT-BR, a ligação encerra limpa.
- **Onde:** ramal interno de teste → Asterisk → OpenAI.
- **Quem:** Pedro ou pessoa da equipe no ramal; executor acompanha logs.
- GATE: APROVAÇÃO PEDRO

### Etapa 7 — Ligações de medição (3 min e 10 min) + roteiro

- **O quê:** pelo menos uma ligação de 3 min e uma de 10 min seguindo o roteiro: turnos normais, interrupções (barge-in) em pontos definidos, pedidos que disparam tool call e pedidos que **não** devem disparar tool. Limiares da seção 6 confirmados pelo Pedro **antes** desta etapa.
- **Onde:** ramal interno de teste.
- **Quem:** Pedro/equipe no ramal; executor coleta gravação e logs.
- GATE: APROVAÇÃO PEDRO

### Etapa 8 — Coleta de usage/custo e relatório

- **O quê:** extrair do dashboard do projeto OpenAI os tokens in/cached/out por ligação; calcular US$ por ligação e US$/min; medir latência e barge-in a partir das gravações; calcular WER; montar relatório com FATOS (medidos) vs HIPÓTESES.
- **Onde:** dashboard OpenAI (leitura) + gravações + logs do serviço PoC.
- **Quem:** executor (leitura) + Pedro (acesso ao dashboard, se necessário).

### Etapa 9 — Rollback / limpeza

- **O quê:** executar a seção 7 inteira.
- **Onde:** Asterisk Oracle, projeto OpenAI, host do serviço PoC, Traefik, DNS.
- **Quem:** executor, após aprovação.
- GATE: APROVAÇÃO PEDRO

### Etapa 10 — Decisão go/no-go e abertura da etapa Gemini

- **O quê:** Pedro decide, com base no relatório: aprovar a OpenAI como candidata, reprovar, ou pedir mais medições. Em qualquer caso, abrir a etapa 2 (Gemini Live) com as mesmas métricas e roteiro, para comparação direta.
- **Quem:** Pedro.

---

## 5. Métricas

| Métrica | Definição | Como medir |
|---------|-----------|------------|
| Latência de turno | fim da fala do humano → primeiro áudio da IA, em ms; p50 e p95 sobre N turnos (N ≥ 20 somando as ligações da etapa 7) | gravação estéreo/dual-channel no Asterisk ou captura RTP no ramal; marcação manual ou por VAD |
| Barge-in | tempo entre o humano começar a falar e a IA parar de falar, em ms; e taxa de interrupções respeitadas | mesmas gravações, nos pontos de interrupção do roteiro |
| Custo real | tokens in / cached in / out por ligação → US$ por ligação e US$/min, para 3 min e 10 min | usage do projeto dedicado no dashboard OpenAI |
| Qualidade PT-BR (WER) | WER entre transcrição de referência feita por humano e a transcrição da sessão | transcrição manual vs transcript da sessão |
| Naturalidade | nota subjetiva 1–5 (voz, prosódia, compreensão de sotaque/ruído de linha) | teste de ouvido do Pedro/equipe |
| Alucinação de ação | tool calls indevidas ou com argumento inventado ÷ total de tool calls | log de tool calls do serviço PoC vs roteiro |

---

## 6. Critérios de sucesso e de falha

**Os limiares abaixo são metas PROPOSTAS pelo planejamento, não medidas nem acordadas. O Pedro confirma ou ajusta antes da etapa 7.**

| Critério | Sucesso (proposto) | Falha (proposto) |
|----------|--------------------|------------------|
| Latência de turno p50 | ≤ 800 ms | > 1200 ms |
| Latência de turno p95 | ≤ 1500 ms | > 2500 ms |
| Barge-in | IA para em ≤ 500 ms em ≥ 90% das interrupções | > 1000 ms ou < 70% das interrupções respeitadas |
| Custo real | ≤ US$ 0,05/min na ligação de 10 min | > US$ 0,10/min |
| WER PT-BR (8 kHz) | ≤ 15% | > 25% |
| Alucinação de ação | 0 ação alucinada no roteiro | ≥ 1 ação alucinada com efeito (ex.: tool com argumento inventado) |
| Naturalidade | nota média ≥ 4 no teste de ouvido | nota média < 3 |

Resultado entre sucesso e falha = zona cinzenta: decisão do Pedro na etapa 10, podendo pedir mais ligações.

---

## 7. Rollback

1. **Asterisk:** remover endpoint `openai-realtime`, aor/auth associados e contexto `[poc-openai-realtime]`, restaurando o backup da etapa 5; `reload` de dialplan e pjsip.
2. **Webhook:** desativar/remover o webhook `realtime.call.incoming` no projeto OpenAI.
3. **Serviço PoC:** derrubar o serviço e remover seus artefatos do host escolhido na etapa 2.
4. **Traefik/DNS:** remover a rota do file-provider e o registro DNS do subdomínio do webhook.
5. **OpenAI:** revogar a chave do projeto e arquivar o projeto.
6. **Checagem pós-rollback:** comparar dialplan e configuração PJSIP (incluindo tabelas `ps_*`) com o backup da etapa 5 — trunk 8880 e contextos de produção (`[from-external]`, `[from-extensions]`, `[route-cascade]` etc.) devem estar **idênticos** ao backup; confirmar registro do trunk 8880 ativo.

---

## 8. Riscos

| Risco | Mitigação |
|-------|-----------|
| **LGPD** — áudio sai para provider externo (OpenAI) | PoC usa só ramal interno de teste, sem cliente; mesmo trade-off já aceito nos tenants `transcricao-voip`/`analise-transcr-voip`. Uso com cliente real exige nova decisão. |
| **Custo descontrolado** | projeto dedicado com limite de gasto; duração máxima por ligação (ex.: timeout no dialplan do contexto isolado); desligar o webhook após a medição. |
| **Impacto no trunk 8880 de produção** | contexto isolado, sem rota de entrada/saída pelo trunk; backup + diff aprovado antes; checagem pós-rollback. |
| **Webhook público exposto** | verificação de assinatura obrigatória; rota Traefik só com o path necessário; serviço sem outros endpoints. |
| **Regressão no Asterisk do Oracle** | `reload` em vez de restart; janela combinada com o Pedro; backup pronto para restauração imediata. |
| **NAT/RTP entre Oracle e OpenAI** | confirmar no pré-voo e testar na etapa 6 (ver "NÃO SEI" abaixo). |

---

## 9. FATOS vs HIPÓTESES

### FATOS (com fonte)

- Preços 2026-09-23, OpenAI (https://developers.openai.com/api/docs/pricing): `gpt-realtime-2.1` = $32 entrada / $0,40 entrada cached / $64 saída por 1M tokens de áudio; `gpt-realtime-2.1-mini` = $10 / $0,30 / $20.
- Preços 2026-09-23, Gemini (https://ai.google.dev/gemini-api/docs/pricing): `gemini-3.8-live` = $3/1M ($0,005/min) entrada de áudio, $12/1M ($0,018/min) saída.
- Preços 2026-09-23, ElevenLabs Agents (https://elevenlabs.io/pricing/agents): $0,08/min (burst $0,16/min acima da concorrência), LLM cobrado à parte, aceita SIP trunk.
- A OpenAI tem SIP nativo para a Realtime API, com webhook `realtime.call.incoming` configurado em platform > Project > Webhooks e aceite da chamada via API com instructions/tools (fonte: https://developers.openai.com/api/docs/guides/realtime-sip).
- Gemini Live é só WebSocket, sem SIP (fonte: levantamento de 2026-09-23 registrado na decisão D-02).
- Infra (fonte: repo `voip-api/docker/asterisk/`, lido em 2026-09-23): transports `transport-udp` (5070), `transport-wss`, `transport-tls` (5071, tlsv1_2), `transport-tls-5061` (5061, tlsv1_2); RTP 30000–30100 com ICE; sorcery realtime Postgres (`ps_*`); contextos `[from-external]`, `[from-extensions]`, `[route-cascade]`, `[b2bua-hangup]`, `[internal-hangup]`, `[webphone-out]`, `[from-queue]`, `[webphone-control]`.
- Asterisk roda no Worker-Oracle (Portainer stack 36, endpoint 7, Tailscale 100.96.9.111); trunk 8880 = produção (fonte: contexto do plano 260923-sho e CLAUDE.md do host).

### HIPÓTESES

- HIPÓTESE: custo por minuto de ligação — mini ~$0,018, full ~$0,058, Gemini ~$0,014, ElevenLabs $0,08 + LLM. A conversão OpenAI usa a razão antiga ~600 tok/min de entrada e ~1200 tok/min de saída, não confirmada para os modelos 2.1. Premissa da conta: 1 min de ligação = 1 min de áudio de entrada (IA ouve o tempo todo) + 0,5 min de áudio de saída (IA fala metade do tempo) → mini = 600×$10/1M + 600×$20/1M ≈ $0,018; full = 600×$32/1M + 600×$64/1M ≈ $0,058; Gemini = $0,005 + 0,5×$0,018 = $0,014. Se a IA falar 100% do minuto, mini ≈ $0,030.
  Resolve: ligação de teste + usage do projeto no dashboard OpenAI (etapa 8).
- HIPÓTESE: o custo real é maior que a estimativa linear, porque o contexto da sessão é re-cobrado a cada turno.
  Resolve: comparar US$/min das ligações de 3 min e de 10 min com a fatura/usage.
- HIPÓTESE: no Gemini, silêncio na linha conta como entrada de áudio.
  Resolve: ligações de 3 e 10 min na etapa Gemini comparadas com a fatura.
- HIPÓTESE: a latência speech-to-speech é menor que a do pipeline STT → LLM → TTS.
  Resolve: medir latência de turno nesta PoC e comparar com medição equivalente da Arquitetura A (se for testada).
- HIPÓTESE: a qualidade em PT-BR com áudio telefônico de 8 kHz é aceitável — não medida.
  Resolve: WER + teste de ouvido na etapa 7.

### NÃO SEI / dado insuficiente

- Formato exato do SIP URI do projeto OpenAI, transporte/porta e autenticação SIP — resolve no pré-voo (etapa 0) lendo o guia oficial.
- Codecs aceitos pela OpenAI (ulaw vs alaw, outros) — resolve no pré-voo.
- Comportamento de NAT/RTP entre o Worker-Oracle e a OpenAI (endereço externo anunciado, faixa 30000–30100 alcançável, necessidade de ajuste em `external_media_address` para o transport usado) — resolve no pré-voo + ligação de fumaça.
- Se o endpoint deve ficar em arquivo estático ou em `ps_endpoints` sem interferir no provisionamento do voip-api — resolve na etapa 5, com diff aprovado.
- Host onde o serviço PoC do webhook vai rodar — resolve na etapa 2.

---

## 10. Próximos passos

1. **Etapa 2 do SEED-009 — Gemini Live:** ponte ARI `externalMedia` → WebSocket (Gemini Live não tem SIP), com as **mesmas métricas e o mesmo roteiro** desta PoC, para comparação direta de latência, barge-in, custo real e qualidade PT-BR.
2. **ElevenLabs Agents:** só se nem OpenAI nem Gemini passarem no teste de ouvido.
3. **Se aprovada:** integração no ai-gateway conforme SEED-009 — endpoint realtime, upstream com role `realtime`, bilhetagem por minuto, audit log de sessões e fallback entre providers. Uso com cliente real exige nova decisão de LGPD.
