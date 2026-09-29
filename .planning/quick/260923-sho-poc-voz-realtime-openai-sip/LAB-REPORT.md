# Laboratório — IA de voz em tempo real em ligação VoIP (SEED-009)

**Período:** 2026-09-23 → 2026-09-26 · **Dono:** Pedro · **Próximo passo:** implementar no DiscLight (voip-api)
**Detalhe cronológico completo:** `POC-PLAN.md` (mesma pasta). Este relatório é o consolidado.

---

## 1. Resultado

Uma IA atende/faz ligação telefônica real em PT-BR, conversando em tempo real, com a **voz** num modelo
speech-to-speech e o **conteúdo** (fatos, dados do cliente) num LLM separado ("cérebro") chamado por
function calling. Funcionou em três caminhos:

| Caminho | Voz | Status |
|---|---|---|
| Softphone TLS → Asterisk → **OpenAI Realtime via SIP nativo** | `gpt-realtime-2.1-mini` | ✅ |
| Ramal voip-api → `[from-extensions]` → tronco NextBilling → **celular** → OpenAI via SIP | `gpt-realtime-2.1-mini` | ✅ (ARI originate com callerId) |
| Softphone TLS → Asterisk → **ponte ARI externalMedia ↔ Gemini Live** | `gemini-3.8-live-extended-thinking` | ✅ |

**Configuração escolhida pelo Pedro (2026-09-26):**
- **Voz principal:** `gemini-3.8-live-extended-thinking`, thinking `medium`, voz `Kore`.
- **Voz fallback:** OpenAI `gpt-realtime-2.1-mini` via SIP (entra no mesmo bridge se a sessão Gemini cair).
- **Cérebro:** cadeia por tokens/s, direto no OpenRouter (sem ai-gateway):
  `openai/gpt-oss-120b@Cerebras` → `openai/gpt-oss-safeguard-20b@Groq` → `google/gemini-3.1-flash-lite`,
  reasoning `low`, provedor fixado (`allow_fallbacks:false`), timeout 4 s por elo.

Avaliação do Pedro na última ligação (Gemini + gpt-oss-120b): fluxo completo, respostas corretas; resta
atraso da voz (thinking) e a voz "narrando" ações.

---

## 2. Arquitetura final (Gemini)

```
Cliente (celular/softphone)
   │  SIP/RTP G.711
Asterisk 22 (voip-api, worker-oracle, net=host)
   │  bridge mixing: [perna do cliente] + [externalMedia slin 8 kHz]
   │  RTP 127.0.0.1  (PT 10/11 = L16 8 kHz)
poc-gemini-bridge (Bun, container network host, /opt/poc-gemini-bridge)
   ├─ WebSocket → Gemini Live (BidiGenerateContent): áudio 16 kHz in / 24 kHz out, transcrições in/out
   ├─ toolCall consultar_atendente → cérebro (OpenRouter, cadeia) → toolResponse
   └─ Gemini caiu → cria PJSIP/poc_openai_realtime no mesmo bridge (header X-Poc-Fallback: 1)
                     └─ OpenAI SIP → webhook poc-voz-realtime (worker-vm) aceita/monitora
```

Fluxo de saída da ponte: `create` do canal de mídia → `create` + `dial` da perna do cliente com o canal de
mídia como chamador (equivale ao `Dial()`), Gemini abre só no `ChannelStateChange Up` (sem early media).

Arquitetura OpenAI (fallback / caminho alternativo): Asterisk disca `sip:<proj>@sip.api.openai.com:5061;transport=tls`
(SRTP SDES, G.711) → OpenAI chama webhook `realtime.call.incoming` → serviço aceita com
`POST /v1/realtime/calls/{id}/accept` e monitora pelo WebSocket `?call_id=`.

---

## 3. Medições (FATOS, logs)

**Voz**
| | OpenAI realtime-2.1-mini | Gemini 3.8 Live | 3.8 ext. thinking low | 3.8 ext. thinking medium |
|---|---|---|---|---|
| 1º áudio após fim da fala | 0,22–0,4 s (VAD 300 ms) | ~0,4–0,7 s | 0,5–1,7 s | 0,6–1,0 s (1× 4 s) |
| Texto de entrada (ligação) | — | 7.591 / 76 s | 35.852 / 91 s | 45.265 / 84 s; 50.949 / 69 s |
| Tokens de raciocínio | 8–52/resposta | ~60/resposta | 2.232 | 3.674–8.530 |
| Custo estimado (teto, soma de tokens) | ~US$ 0,021/min (voz+transcr.) ≈ R$ 0,11 | ~US$ 0,031/min ≈ R$ 0,16 | ~2× | ~US$ 0,062/min ≈ R$ 0,32 |

HIPÓTESE: custo é teto (supõe re-cobrança do contexto a cada turno). Confirmar no billing real dos projetos
OpenAI (`proj_zZFn…`, limite US$ 20) e Google (`projects/770905845436` "Gemini Voz"). Câmbio usado R$ 5,18.
`gpt-realtime-2.1` (full) testado: sem ganho percebido pelo Pedro, 3,2× o preço → descartado.

**Cérebro — latência total (4 cenários de atendimento, do Oracle)**
| Via | Modelo | Mediana |
|---|---|---|
| ai-gateway | qwen (pod local) | 1,5–2,1 s |
| ai-gateway | deepseek-v4-flash (fallback noturno) | 1,7–7,1 s |
| ai-gateway | gemini-3.5-flash-lite | 0,8–1,4 s |
| OpenRouter direto | gpt-oss-safeguard-20b | ~0,57 s |
| **OpenRouter direto** | **gpt-oss-120b@Cerebras (cadeia)** | **0,26–0,48 s** |

**Cérebro — tokens/s (streaming ~300 tokens, critério do Pedro)**: gpt-oss-120b@Cerebras 1.013;
gpt-oss-safeguard-20b@Groq 967; gpt-oss-20b@Groq 965; llama-3.1-8b@Groq 688; flash-lite 238–270;
haiku-4.5 106; llama-3.3-70b@Novita 48. **O provedor define o tok/s** — fixar provedor.
Modelos inadequados: `gemini-2.5-flash-lite` (inventou CPF), `gpt-4.1-nano` ("12 dígitos"),
`morph/morph-v3-fast` (modelo de edição de código: "Multi-turn conversations are not supported").

---

## 4. Problemas encontrados e causa raiz

| Sintoma | Causa raiz (evidência) | Correção |
|---|---|---|
| Contact OpenAI virou `transport=tls` | Realtime do Asterisk corta valor no `;` | gravar `;` como `^3B` |
| Softphone não registrava em UDP 5070 | Só IPs de tronco chegam no host (captura) | TLS 5061 + wildcard `*.sip.ifixtelecom.com.br` |
| OpenAI `From: Anonymous` / sessão caía | `channel originate` CLI sem caller ID | `Dial(...,f(token))` / ARI `callerId` |
| Celular mudo / caixa postal / "não receber recados" | Canal de `channel originate` CLI sai **anônimo** | **ARI originate com `callerId`** |
| `/dialer` recusa ramal-IA (422) | Preflight exige endpoint `online` (REGISTER); contato estático fica `offline` | Disparo via ARI em `[from-extensions]` |
| Ramal no iFix Master sem tronco | Workspace sem `outbound_trunk_id`/route group ⇒ fallback inexistente | Usar workspace com rota (3:16) |
| IA respondia a toque/caixa postal | Early media chega antes do atendimento | OpenAI: `create_response:false` até fala humana + regex de anúncio ⇒ hangup; Gemini: abre só no `Up` |
| Ponte Gemini muda (Asterisk descartava) | externalMedia `slin16` envia PT 118 mas **só aceita PT 10/11** de volta (varredura em bancada) | TX com PT 11 |
| Voz "robótica e lenta" | PT 10/11 = L16 **8 kHz** no Asterisk; ponte mandava 16 kHz | canal `slin` 8 kHz (RX 8→16k p/ Gemini, TX 24→8k) |
| Áudio áspero | 24→8 kHz sem anti-aliasing | FIR passa-baixa Blackman 63 taps, corte 3,6 kHz |
| addChannel 422 "not in Stasis" | corrida externalMedia × StasisStart | esperar StasisStart da mídia |
| Mudo por espera mútua | ponte só enviava após 1º pacote | endereço de retorno via `UNICASTRTP_LOCAL_*` + silêncio contínuo |
| Cérebro 400 (Google) | "Requests ending with a model turn are not supported" | histórico sempre termina em fala do cliente |
| Cérebro 429 | provedor compartilhado do safeguard saturado (não é limite da conta: `limit:null`) | cadeia com provedor fixo + reserva |
| Modelo de voz inventou CPF na tool | argumento gerado pelo modelo | tool recebe só `intencao`; dados vêm da **transcrição literal** |
| `1007 Thinking level must be specified` | ext. thinking exige `thinkingConfig.thinkingLevel` | enviar só p/ esse modelo; 3.8-live não aceita |
| Gemini respondia em inglês/misturado | 3.8-live escolhe idioma sozinho (não aceita `languageCode`) | instrução "Fale SEMPRE em PT-BR" |

---

## 5. Pendências conhecidas (não resolvidas)

1. **Voz narra ações fictícias** ("estou gerando o link", "estou processando") — Gemini ignora "fique em silêncio".
2. **Cérebro inventa capacidades** ("gerar link para download") e às vezes "anotei/registrei" sem sistema.
   Definir lista fechada de ações possíveis + **integração real** (ticket/CRM) para "anotar".
3. **Atraso da voz** com thinking `medium` (até 4 s num turno; 8.530 tokens de raciocínio em 139 s).
4. **Picotes leves** — HIPÓTESE: jitter do timer de 20 ms da ponte + sem jitter buffer; RTT do softphone 0,16–1,4 s.
5. **Fallback OpenAI** e **reservas do cérebro** nunca exercitados numa ligação real.
6. Ramal cenário "ramal parado": cérebro pede só CPF, não o número do ramal.
7. `gpt-live` (OpenAI full-duplex) e `gpt-live-transcribe` não testados.
8. LGPD: logs NDJSON guardam transcrições (nome, telefone, e-mail ditados) — política de retenção/mascaramento.
9. Ponte de WhatsApp do voip-api usa o PT aprendido (118) no TX — HIPÓTESE de mesmo defeito; conferir.

---

## 6. Inventário do que está NO AR (para desmontar ou migrar)

| Onde | O quê | Rollback |
|---|---|---|
| Cloudflare | A `poc-voz.ifixtelecom.com.br` → 162.55.92.154 (id `2b0493574865203d00436dd5d5540754`) | DELETE do record |
| Edge Traefik (vps-ifix-vm) | `/home/pedro/projetos/pedro/infra/traefik-dynamic/poc-voz.yml` (só `POST /webhook`) | apagar arquivo |
| worker-vm | container `poc-voz-realtime` (`/opt/poc-voz-realtime`, 10.10.10.50:8099) | `docker compose down` |
| worker-oracle | container `poc-gemini-bridge` (`/opt/poc-gemini-bridge`, network host, 127.0.0.1:8110 com token) | `docker compose down` |
| voip_api (Postgres) | `ps_*`: `poc_openai_realtime`, `poc_voz_ramal`(+auth) | `ops/poc-voz-realtime/asterisk-rollback.sql` |
| voip_api (API) | ramal **9990 "IA PoC SEED-009" no 3:16 PUBLICIDADE** (id 2164), endpoint apontado p/ OpenAI | `DELETE /extensions/2164` (backup linhas: `ops/poc-voz-realtime/ramal-9990-316-original.json`) |
| voip_api | token API 3:16 (last4 WcdA, expira 2026-10-02) | revogar `DELETE /admin/workspaces/:id/api-tokens/:tokenId` |
| ai-gateway | tenant `voz-realtime-poc` (id `3abf488a…`) | desativar key |
| OpenAI | projeto `proj_zZFn…` + webhook + chave (passaram pelo chat) | revogar chave/webhook |
| Google | chave "Gemini Voz" (passou pelo chat) | revogar |
| Já removido | ramal 9990 iFix Master (id 2163), token iFix Master (revogado) | — |

Segredos: `ops-claude:/etc/onboard/secrets/openai-poc-voz.env` (root 600) e `secrets.env` 600 em cada host.
Chamadas de teste do celular entraram no CDR/billing do **3:16 PUBLICIDADE** (acordado com Pedro).

---

## 7. Roteiro para o DiscLight (voip-api)

DiscLight = integração PABX do voip-api (workspace **3:16 PUBLICIDADE**, `01501455`, cliente NB 140) —
onde o ramal 9990 da PoC já vive. Pontos de decisão/implementação:

1. **Onde a ponte vive:** dentro do voip-api (módulo como o `services/whatsapp/media-bridge.ts`, que já faz
   externalMedia ↔ WebSocket) em vez de container avulso. Reusar: app Stasis, B2BUA/CDR/billing, cos-check.
2. **Ramal-IA como tipo de destino:** hoje DIDs apontam p/ `extension/queue/ivr/...`; criar destino "agente IA"
   (entrada) e aceitar ramal-IA sem REGISTER no `/dialer` (saída, preflight 422).
3. **Mídia:** externalMedia `slin` 8 kHz, TX PT 10/11, anti-aliasing, retorno via `UNICASTRTP_LOCAL_*`.
4. **Caller ID** sempre presente (canal sem CID sai anônimo).
5. **Cérebro:** cadeia OpenRouter por tok/s com provedor fixado; tool só com intenção; dados da transcrição;
   histórico terminando em fala do cliente; lista fechada de ações + integrações reais (CRM/ticket/fatura).
6. **Fallback de voz** Gemini → OpenAI no mesmo bridge.
7. **Custos:** billing real por minuto/ligação; tarifa NextBilling.
8. **LGPD:** aviso de assistente virtual na abertura, retenção/mascaramento de transcrições.
9. Pendências da seção 5 (narração de ações, atraso do thinking, picotes).

## Commits da PoC (repo gpu-ifix, branch develop, sem push)
`6e9b283` … `d26702f` (ver `git log --oneline -- ops/poc-voz-realtime ops/poc-gemini-bridge .planning/quick/260923-sho-poc-voz-realtime-openai-sip`).

---

## 8. Contexto e alternativas avaliadas (antes/durante o lab)

- **GPU própria (pergunta inicial):** a 3090 do pod primário roda o Qwen3-30B-A3B em ~21 GB com ~190–202 tok/s, sobra
  ~3,8 GB — não cabe STT+TTS em streaming junto. XTTS no pod 3060 = ~3,3 s/frase sem streaming e licença não-comercial.
  Opções levantadas: 2×3090 (~US$ 0,26/h) ou 5090 (~US$ 0,32/h). Não seguido: optou-se por APIs speech-to-speech.
  Open source (Moshi, PersonaPlex, Qwen-Omni): sem PT-BR confirmado; review 2026 diz que ainda não substituem pipeline
  STT+LLM+TTS em produção. Vantagem única da cascata local: LGPD (áudio não sai de casa).
- **Preços oficiais levantados (2026-09-23/26):** OpenAI `gpt-realtime-2.1` $32/$64 e `-mini` $10/$20 por 1M tokens de
  áudio (texto mini $0,60/$2,40); `gpt-live-1` $0,05/min + backend; transcrição `gpt-4o-transcribe` $0,006/min,
  `gpt-transcribe` $0,0045/min, `gpt-4o-mini-transcribe` $0,003/min, `gpt-live-transcribe` $0,017/min (só em sessão de
  transcrição, não dentro do Realtime). Gemini `3.8-live`, `3.8-live-extended-thinking` e `3.1-flash-live-preview`:
  mesmas tarifas (áudio $3 in / $12 out por 1M = $0,005/$0,018 por min; texto $0,75/$4,50). ElevenLabs Agents
  $0,08/min + LLM à parte (descartado: ~6× o Gemini por minuto de voz).
- **GPT-Live (OpenAI full-duplex):** voz com delegação `client` p/ cérebro próprio; mesmo SIP; não testado.
  Realtime não permite trocar o cérebro — só via tools (o que foi feito).
- **Gemini Live e SIP:** Gemini não tem SIP; OpenRouter não expõe Live API; proxy SIP LiveTok descartado (sem licença,
  sem tools, sem SRTP). A ponte seguiu o padrão da ponte de WhatsApp do voip-api (sugestão do Pedro).
- **AI Studio:** `/live` abriu com modelo de transcrição; Pedro conseguiu conversar depois. `gemini-3.8-live` só fala
  PT-BR de forma estável com instrução explícita (escolhe idioma sozinho).
- **Transcrição (OpenAI):** `gpt-4o-mini-transcribe` errou feio em 8 kHz; `gpt-4o-transcribe` e `gpt-transcribe`
  zeraram os erros nas amostras (ficou `gpt-transcribe`, 25% mais barato).
- **Cérebros descartados:** `morph/morph-v3-fast` (edição de código), `gemini-2.5-flash-lite` (inventou CPF),
  `gpt-4.1-nano`, `qwen3-32b` (9 s), `llama-3.3-70b` via Novita (48 tok/s).
- **Segurança:** endpoints de controle da ponte exigem token + loopback (achado de revisão automática, commit `d26702f`).
  Chaves OpenAI/Google/token iFix Master passaram pelo chat (iFix Master já revogado).

## 9. Estado do repositório

Todos os commits da PoC estão **só locais** em `gpu-ifix` branch `develop` (ops-claude), **sem push**.
