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
- **Status:** ✅ concluída 2026-09-23 — ver *Adendo — Etapa 0* no fim do documento.

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

---

## Adendo — Etapa 0 (pré-voo) executada em 2026-09-23

Só leitura: guia oficial `developers.openai.com/api/docs/guides/realtime-sip` + repo `voip-api` (commit `b5c2924c`, `voip-api/docker/asterisk/`). Nenhuma ação externa.

### FATOS — OpenAI SIP (fonte: guia oficial, lido 2026-09-23)

| Item | Valor confirmado |
|---|---|
| URI | `sip:$PROJECT_ID@sip.api.openai.com;transport=tls` (UE: `sip-eu.api.openai.com`) |
| Sinalização | TLS sobre TCP, porta **5061**; liberar saída TCP 5061 para os IPs que o DNS devolver |
| Mídia | **SRTP obrigatório** ("requires SRTP for call audio"), UDP bidirecional com `13.79.45.80/28`, `23.98.140.64/28`, `40.67.149.176/28`, `40.83.204.240/28`; IP/porta de mídia vêm no SDP |
| Codecs | G.711 μ-law e A-law, 8 kHz. Omitir `audio.format` no accept (SIP negocia) |
| Auth do INVITE | Nenhum digest documentado — roteamento pelo `PROJECT_ID` no URI |
| Webhook | evento `realtime.call.incoming`, `data.call_id` + `data.sip_headers` (tratar como não-confiável); headers `webhook-id`, `webhook-timestamp`, `webhook-signature` (v1); verificar com `client.webhooks.unwrap(body, headers)` do SDK oficial |
| Aceitar | `POST /v1/realtime/calls/{call_id}/accept` body `{type:"realtime", model, instructions, audio:{output:{voice}}, ...}` com `Authorization: Bearer` |
| Recusar | `POST .../reject` `{status_code:486}` (default 603) |
| Transferir | `POST .../refer` `{target_uri:"sip:..."\|"tel:..."}` |
| Desligar | `POST .../hangup` |
| Monitorar | WebSocket `wss://api.openai.com/v1/realtime?call_id={call_id}` (eventos Realtime padrão, ex. `response.create`) |

### FATOS — Asterisk do voip-api (fonte: repo, só leitura)

- Asterisk 22 (`andrius/asterisk:22`, pinado por digest), `modules.conf` `autoload = yes`.
- `transport-tls` (5071) e `transport-tls-5061` (5061) existem, `method = tlsv1_2`, **`verify_server = no`** (Asterisk não valida o certificado do servidor remoto).
- `rtp.conf`: RTP 30000–30100, `icesupport = yes` global.
- **Endpoints/AORs/identify 100% realtime** (`sorcery.conf` → `ps_endpoints`, `ps_aors`, `ps_identify` via ODBC; sem fallback de arquivo). ⇒ endpoint da PoC = **linhas no Postgres do voip-api**, sem rebuild nem restart.
- **Dialplan é estático** (`extensions.conf` embutido na imagem; `extconfig.conf` não tem extensions realtime). ⇒ contexto novo `[poc-openai-realtime]` exigiria rebuild da imagem + redeploy do stack 36 (restart do Asterisk de produção).

### Decisão proposta (substitui o "contexto novo" da Etapa 4) — sem tocar dialplan

Disparar a ligação por **originate**, sem contexto novo:

```
asterisk -rx "channel originate PJSIP/<ramal-teste> application Dial PJSIP/openai-realtime"
```

Toca o softphone de teste; ao atender, o Asterisk disca a OpenAI. Zero mudança em `extensions.conf`, zero rebuild, zero restart.

Mudanças no Asterisk (todas no Postgres realtime, reversíveis com `DELETE`):

1. `ps_aors` id `openai-realtime`: `contact = sip:<PROJECT_ID>@sip.api.openai.com:5061;transport=tls`, `max_contacts = 1`.
2. `ps_endpoints` id `openai-realtime`: `transport = transport-tls`, `aors = openai-realtime`, `context = poc-openai-sink` (contexto **inexistente** → qualquer INVITE/REFER vindo da OpenAI morre sem cair em produção), `disallow = all`, `allow = ulaw,alaw`, `media_encryption = sdes`, `direct_media = no`, `ice_support = no`, `rtp_symmetric = yes`, `force_rport = yes`, `rewrite_contact = yes`, sem `auth`/`outbound_auth`.
3. Ramal de teste: reutilizar ramal de teste existente OU nova linha em `ps_endpoints`/`ps_auths`/`ps_aors` só para a PoC (decidir na Etapa 4).

**NÃO muda:** `[from-external]`, `[from-extensions]`, demais contextos, trunk 8880, transports, `rtp.conf`, imagem, stack 36.

**Rollback:** `DELETE` das 2–5 linhas + `asterisk -rx "pjsip reload"` (ou nada, realtime relê por consulta).

### HIPÓTESES (resolvem na Etapa 4/5)

- HIPÓTESE: a OpenAI aceita **SDES-SRTP** (`media_encryption=sdes`) — o guia diz "SRTP" sem dizer SDES vs DTLS; SDES é o usual em tronco SIP/TLS. Resolve: 1ª chamada; se 488, trocar para `dtls`.
- HIPÓTESE: `res_srtp` está carregado no container em produção (o webphone usa DTLS-SRTP, que depende dele). Resolve: `asterisk -rx "module show like srtp"` no container (leitura).
- HIPÓTESE: `gpt-realtime-2.1-mini` é aceito no `accept` via SIP (o exemplo do guia usa `gpt-realtime-2.1`; não há restrição escrita). Resolve: 1ª chamada.
- HIPÓTESE: originate com `application Dial` funciona sem interferir no Stasis `voip-api` (billing/ARI). Resolve: conferir no log do voip-api que a chamada não gera CDR/cobrança de cliente.

### NÃO SEI (dado insuficiente — checar antes da Etapa 5, leitura apenas)

- Egress do Worker-Oracle: saída TCP 5061 e UDP para os 4 blocos de mídia liberados na security list da OCI?
- Ingress UDP 30000–30100 aberto para os blocos de mídia da OpenAI (hoje o RTP do trunk vem de outra origem)?
- Risco `verify_server = no`: sinalização TLS sem validar certificado (MITM possível). Aceitável para PoC; para produção exigiria transport dedicado com `verify_server = yes` + `ca_list_file` — mudança de config estática (rebuild).
- Timeout do webhook: o guia não documenta; o serviço deve responder rápido e aceitar a chamada de forma assíncrona.

## Adendo — Etapa 1 (parcial, 2026-09-23)

- Chave de projeto (`sk-proj-…`) recebida do Pedro e guardada em `/etc/onboard/secrets/openai-poc-voz.env` (root:root 600). Fora do repo.
- FATO: `GET /v1/models` → HTTP 200 (x-request-id `133c9dfa-76cc-4e71-9ffc-97d2fe50043d`); 136 modelos visíveis, incluindo `gpt-realtime-2.1-mini` e `gpt-realtime-2.1`.
- ✅ Project ID recebido e gravado no mesmo arquivo (`OPENAI_PROJECT_ID`). FATO: chave + header `OpenAI-Project: <id>` → HTTP 200; controle com id inválido → HTTP 401 ⇒ chave pertence a esse projeto. URI SIP: `sip:<OPENAI_PROJECT_ID>@sip.api.openai.com;transport=tls`.
- PENDENTE / NÃO SEI: se o projeto tem limite de gasto configurado — chave de projeto não lê billing; confirmar no painel.

## Adendo — Etapa 1 fechada + Etapa 2 executada (2026-09-23)

- Etapa 1: limite de gasto **US$ 20** configurado no projeto pelo Pedro (confirmado por ele; chave de projeto não lê billing).
- Etapa 2 (aprovada pelo Pedro): serviço `ops/poc-voz-realtime/` (Bun + SDK `openai` 7.23.0) na **worker-vm** `/opt/poc-voz-realtime`, container `poc-voz-realtime`, bind **só interno** `10.10.10.50:8099`. Fora de qualquer stack de produção.
  - Travas: fail-closed sem `OPENAI_WEBHOOK_SECRET` (503); só aceita From user = `POC_CALLER_TOKEN` (resto → 603); 1 chamada simultânea (486); hangup automático em 720 s.
  - Log NDJSON por chamada: `turn_latency` (speech_stopped → 1º `response.output_audio.delta`), transcrições user/IA, `usage` por `response.done`.
  - Secrets em `/opt/poc-voz-realtime/secrets.env` (600); `POC_CALLER_TOKEN` gerado e guardado também em `ops-claude:/etc/onboard/secrets/openai-poc-voz.env`.
- FATOS (testes): local — assinatura válida 200, inválida 400, timestamp de 1 h atrás 400, sem secret 503; typecheck ok. Deploy — container Up, `/health` 200 alcançado a partir da vps-ifix-vm (edge), `POST /webhook` sem secret → 503.
- HIPÓTESE: `audio.input.transcription` (gpt-4o-mini-transcribe) é aceito no `accept` via SIP e soma custo pequeno à ligação. Resolve: 1ª chamada (`accept_fail` no log se não).
- PENDENTE p/ Etapa 3: DNS + rota Traefik (path `/webhook` só) → criar webhook no painel OpenAI → colar `OPENAI_WEBHOOK_SECRET` no secrets.env + `docker compose up -d`.

## Adendo — Etapa 3 (DNS + Traefik) executada (2026-09-23, aprovada pelo Pedro)

- DNS Cloudflare: A `poc-voz.ifixtelecom.com.br` → `162.55.92.154`, proxied=false, TTL 120, record id `2b0493574865203d00436dd5d5540754`. Propagação confirmada (1.1.1.1, 8.8.8.8, NS autoritativo, resolver da vps-ifix-vm) ANTES de criar a rota.
- Traefik edge (vps-ifix-vm): `/home/pedro/projetos/pedro/infra/traefik-dynamic/poc-voz.yml`, router `Host && Path(/webhook) && Method(POST)` → `http://10.10.10.50:8099`, certResolver letsencrypt.
- FATOS: `POST /webhook` público → 503 (fail-closed, sem secret); `GET /health` → 404; `GET /webhook` → 404; cert Let's Encrypt `CN=poc-voz.ifixtelecom.com.br`, expira 2026-12-22.
- Rollback: `rm poc-voz.yml` no edge + DELETE do record DNS pelo id acima.
- PENDENTE (Pedro): criar webhook no painel OpenAI → URL `https://poc-voz.ifixtelecom.com.br/webhook`, evento `realtime.call.incoming` → passar o signing secret.
- ✅ Webhook criado pelo Pedro no painel OpenAI; signing secret aplicado em `/opt/poc-voz-realtime/secrets.env` (worker-vm) e em `ops-claude:/etc/onboard/secrets/openai-poc-voz.env`. Container recriado.
- FATOS (2026-09-24 00:28Z): `/health` → `secret_configured:true`; POST público assinado com o secret real → 200 (`ignored_event`); POST público sem assinatura → 400 (`bad_signature`). **Etapa 3 fechada.**

## Adendo — Etapa 4 (Asterisk) executada (2026-09-24, aprovada pelo Pedro: "cria ramal novo só pra PoC")

Pré-checks (leitura, container `voip-asterisk` no worker-oracle — atenção: no mesmo host roda `voice-api-asterisk-1`, sempre mirar pelo nome):
- FATO: Asterisk 22.8.2; `res_srtp.so` **Running**; transports `transport-tls` 5071 / `transport-tls-5061` 5061 / udp 5070 / wss 5060; `net=host`, `external_media_address = 137.131.194.252`.
- FATO: egress TLS do Oracle → `sip.api.openai.com:5061` OK (cert `CN=api.openai.com`, Verify return code 0).
- FATO: iptables do host — `OUTPUT` policy ACCEPT; `INPUT -p udp -j ACCEPT` (RTP entrante aceito no host).
- NÃO SEI: security list da OCI para UDP 30000–30100 vindo dos blocos de mídia da OpenAI. HIPÓTESE: security list stateful + Asterisk manda RTP primeiro (rtp_symmetric) ⇒ retorno passa. Resolve: 1ª chamada (áudio mudo = bloqueio).
- FATO (código voip-api): rotinas que apagam `ps_*` usam id exato de entidades do app (`inArray`/`eq`, "never LIKE/prefix"); ids `poc_*` não são tocados.

Linhas criadas no Postgres `voip_api` (realtime):
- `ps_aors.poc_openai_realtime` — contact `sip:<OPENAI_PROJECT_ID>@sip.api.openai.com:5061^3Btransport=tls`, qualify 0.
- `ps_endpoints.poc_openai_realtime` — `transport-tls-5061`, `!all,ulaw,alaw`, `media_encryption=sdes` (optimistic no), `direct_media=no`, `rtp_symmetric/force_rport/rewrite_contact=yes`, `ice_support=no`, `from_user=POC_CALLER_TOKEN`, context `poc-openai-sink` (inexistente).
- `ps_auths.poc_voz_ramal_auth` (user `poc_voz_ramal`, senha em `ops-claude:/etc/onboard/secrets/openai-poc-voz.env` → `POC_RAMAL_PASSWORD`), `ps_aors.poc_voz_ramal`, `ps_endpoints.poc_voz_ramal` (context `poc-openai-sink` ⇒ softphone não disca pra lugar nenhum).
- **GOTCHA medido:** realtime corta valor no `;` → contact virou `transport=tls` (`Error parsing contact`). Fix: gravar `;` como `^3B`. Pós-fix: `pjsip show aor` mostra contact correto, status `NonQual` (esperado, qualify 0), sem novos erros.
- Rollback: `ops/poc-voz-realtime/asterisk-rollback.sql` (DELETE por id exato).

Softphone da PoC: servidor `137.131.194.252:5070` UDP, usuário `poc_voz_ramal`, codecs G.711.
Disparo da chamada (Etapa 5): `docker exec voip-asterisk asterisk -rx "channel originate PJSIP/poc_voz_ramal application Dial PJSIP/poc_openai_realtime"`.

## Adendo — Etapa 5: 1ª ligação (2026-09-24 00:44–00:5x UTC) — ✅ SUCESSO

Ramal PoC registrado via **TLS 5061** (`poc-voz.sip.ifixtelecom.com.br`, cert `*.sip` wildcard + DNS wildcard; UDP 5070 de fora não chegou no host — só IPs de tronco aparecem na captura). Ramal ajustado p/ `media_encryption=sdes` + optimistic.
Snapshot pré-chamada: 0 canais, 15 endpoints livres, 3 contatos Avail. Disparo: `channel originate PJSIP/poc_voz_ramal application Dial PJSIP/poc_openai_realtime`.

FATOS (logs Asterisk + webhook, call_id `rtc_u1_ERRs5p3oNFJVuJV5u5NHCV6uDzTt0DAJ`):
- OpenAI atendeu, bridge montado; **sem 488** ⇒ SDES-SRTP aceito; `res_srtp` ok; áudio nos 2 sentidos (fala do Pedro transcrita). Hipóteses SDES / res_srtp / firewall de mídia **resolvidas**.
- `accept` com `gpt-realtime-2.1-mini`, voz `marin`, transcrição `gpt-4o-mini-transcribe` — aceito.
- Pedro: "sucesso".
- 6 respostas; tokens somados: input audio 540 (64 cached), input text 962 (320 cached), output audio 913, output text 457 (reasoning 177).
- Latência fim-da-fala → transcrição completa da IA: 937 / 949 / 1084 / 1176 / 2685 ms (p50 1084). É **proxy tardio** (não 1º áudio).
- 1 transcrição do usuário vazia (1º turno).
- TLS Asterisk→OpenAI: NOTICE "certificate is untrusted" / "server identity does not match" — passa só porque `verify_server=no`.

Bugs do serviço achados e corrigidos (mesmo dia):
1. `response.output_audio.delta` **não chega** no WebSocket de monitoramento ⇒ `turn_latency` nunca logou. Fix: medir no 1º `response.output_audio_transcript.delta`.
2. WS de monitoramento caiu com **1006 aos 92 s** e o serviço apagou o estado **cancelando a trava de 12 min**, enquanto os 2 canais seguiam Up no Asterisk. Fix: timer não é mais cancelado; fechamento anormal ⇒ `hangup` explícito. Canais `poc_*` derrubados manualmente; `POST /hangup` → 404 (chamada já não existia na OpenAI).

Custo — HIPÓTESE (preço de texto do mini não levantado; usar painel p/ confirmar):
- áudio: 476×$10/1M + 64×$0,30/1M + 913×$20/1M ≈ **$0,023**; texto (se ~$0,60 in / $2,40 out por 1M) ≈ $0,0015; total ≈ **$0,025** na janela de ~92 s monitorada ⇒ ~**$0,016/min**, abaixo da estimativa de $0,018.
- NÃO SEI: se a sessão OpenAI seguiu após o 1006 (entre 00:46:34 e o hangup). Resolve: Usage do projeto no painel.

Próximo: ligações de medição de 3 e 10 min (Etapa 7) com a métrica corrigida + teste de barge-in; conferir custo no painel.

## Adendo — Ligação 2: voz OpenAI + cérebro ai-gateway (2026-09-24 01:24 UTC)

Setup: tenant gateway `voz-realtime-poc` (id `3abf488a-7de6-4216-85ae-0125e48b377e`, key normal, prefix `…rxzw`); tool `consultar_atendente` no accept; alias `qwen` → à noite fallback `deepseek/deepseek-v4-flash-0731` (pod primário fora do horário). call_id `rtc_u0_ERSU1p3oMpIh47QCR5NkVr5tUmOTqYKM`, 114 s.

FATOS (log):
- **1º som da IA após fim da fala: 219–396 ms** (8 turnos; `turn_latency` via 1º transcript delta) — é o "só um instante"/resposta social do realtime.
- 4 tool calls; **cérebro: 3.442 / 5.898 / 6.938 / 7.125 ms** (deepseek fallback) ⇒ 3,4–7,1 s de silêncio após o "só um instante".
- Voz falou a resposta do cérebro **praticamente literal** (brain_done.text ≈ ai_transcript).
- Cérebro não inventou valor de conta: pediu CPF e disse que vai verificar. ✅
- ❌ **Realtime ALUCINOU o CPF no argumento da tool:** usuário falou "sete dois, quatro, sete meia, dois, sete, oito, zero, oito" e a tool recebeu `"CPF 1234567890"`.
- ❌ Reescrita errada: usuário "Como faz para ver a minha fatura?" → tool `"Como faço para pagar minha fatura?"` (tool_call às 34.428 saiu ANTES da transcrição do usuário 34.499).
- ❌ Resposta do cérebro truncada: `"…instabilidade na sua região. Enquanto isso,"` foi falada cortada. HIPÓTESE: deepseek gasta tokens de raciocínio e estoura `max_tokens` 400. Resolve: logar `finish_reason`.
- Na saudação a voz disse "Só um instante enquanto vejo como responder ao seu pedido." antes do "Olá" (instrução de espera vazou pra saudação).
- Transcrição ruim em 2 falas ("Você está me apontando.", "onu").
- `ws_close 1006` aos 114 s coincidiu com o hangup do Pedro; `POST /hangup` → 404 "No session found" ⇒ HIPÓTESE: 1006 é o fechamento normal do sideband no fim da chamada SIP (não falha). Canais Asterisk: 0 após.

Correções propostas (não aplicadas):
1. Tool passa ao cérebro a **transcrição literal** das últimas falas do usuário (fonte de verdade), e o argumento da tool vira só "intenção"; dados (CPF, números) NUNCA saem do argumento gerado pelo modelo de voz.
2. Logar `finish_reason`; se `length`, subir `max_tokens` ou desligar raciocínio no alias.
3. Saudação com `response.create` + instrução própria (sem frase de espera).
4. Repetir de dia com o pod Qwen no ar para medir o cérebro local.
