---
seed: SEED-009
title: Real-time voice AI for live phone calls
status: poc-done
priority: future
phase_target: 12+
captured_at: 2026-06-04
captured_by: pedro
poc_opened_at: 2026-09-23
poc_plan: .planning/quick/260923-sho-poc-voz-realtime-openai-sip/POC-PLAN.md
related: [SEED-002 (emergency hot-standby), voice-api, Asterisk PJSIP]
---

# SEED-009 — Real-time voice AI conectado a ligação telefônica ao vivo

## Status 2026-09-23 — PoC aberta

Decisão do Pedro (2026-09-23): a PoC começa pela **OpenAI `gpt-realtime-2.1-mini` via SIP nativo** (Arquitetura B). O Asterisk do voip-api fala SIP direto com a OpenAI; o controle da sessão (instructions, tools, voz) é feito por um serviço nosso que recebe o webhook `realtime.call.incoming` e aceita a chamada pela API.

Ordem das etapas:

1. **OpenAI `gpt-realtime-2.1-mini` via SIP nativo** — primeira etapa, plano detalhado em [POC-PLAN.md](../quick/260923-sho-poc-voz-realtime-openai-sip/POC-PLAN.md).
2. **Gemini Live** — mais barato, mas só WebSocket (sem SIP); exige ponte ARI `externalMedia` → WebSocket. Mesmas métricas e roteiro da etapa 1, para comparação direta.
3. **ElevenLabs Agents** — só se nenhum dos dois anteriores passar no teste de ouvido.

Guardrail: **nada foi executado em produção.** A abertura da PoC é apenas documental. Cada ação externa ou em produção (projeto/chave OpenAI, webhook, DNS/Traefik, Asterisk do Worker-Oracle, ligações de teste) exige aprovação explícita do Pedro, etapa por etapa, conforme o POC-PLAN.md.

## Intent

Habilitar humano conversar com IA em tempo real durante ligação telefônica (PJSIP/Asterisk → gateway → IA → fala de volta). Diferente do `PROCESSADOR-IA-GRAVACOES-V3` atual que processa áudio POST-call em batch.

## Por que whisper batch não serve

- `whisper-1` exige áudio inteiro antes de transcrever → latência 3-10s = inviável conversação ao vivo (limite humano ~300ms turn-taking).
- `gpt-4o-transcribe` suporta streaming, viável.
- End-to-end voice models (OpenAI Realtime, Gemini Live) eliminam STT separado.

## 2 arquiteturas candidatas (decisão Phase 12+)

### Arquitetura A — Pipeline streaming STT → LLM → TTS

| Componente | Opções avaliar |
|-----------|----------------|
| Streaming STT | `gpt-4o-transcribe` (WebSocket), Deepgram Nova-3, AssemblyAI, faster-whisper local + VAD |
| LLM rápido | `gpt-4o-mini`, `google/gemini-2.5-flash-lite`, `anthropic/claude-haiku-4` |
| Streaming TTS | OpenAI TTS streaming, ElevenLabs Turbo, Cartesia Sonic, Piper local |

Latência típica: 500-1500ms turn. Custo: $0.02-0.05/min. Complexidade: alta (3 streams concorrentes + interrupção handling).

### Arquitetura B — End-to-end voice

#### Preços oficiais 2026-09-23 (FATOS)

Levantados em 2026-09-23 nas páginas oficiais de cada fornecedor.

| Modelo | Entrada | Entrada cached | Saída | Unidade | Fonte |
|--------|---------|----------------|-------|---------|-------|
| OpenAI `gpt-realtime-2.1` | $32 | $0,40 | $64 | por 1M tokens de áudio | https://developers.openai.com/api/docs/pricing |
| OpenAI `gpt-realtime-2.1-mini` | $10 | $0,30 | $20 | por 1M tokens de áudio | https://developers.openai.com/api/docs/pricing |
| Google `gemini-3.8-live` | $3 ($0,005/min) | — | $12 ($0,018/min) | por 1M tokens de áudio (e $/min equivalente publicado) | https://ai.google.dev/gemini-api/docs/pricing |
| ElevenLabs Agents | $0,08/min (burst $0,16/min acima da concorrência contratada) | — | incluído no $/min | por minuto de conversa; **LLM cobrado à parte** | https://elevenlabs.io/pricing/agents |

FATO (fonte: https://developers.openai.com/api/docs/guides/realtime-sip): a OpenAI tem **SIP nativo** para a Realtime API. A chamada SIP chega na OpenAI, que dispara o webhook `realtime.call.incoming` (configurado em platform > Project > Webhooks); o nosso serviço aceita a chamada via API, passando instructions/tools/voz.

FATO (fonte: levantamento de 2026-09-23 registrado na decisão D-02 do plano 260923-sho; página de referência https://ai.google.dev/gemini-api/docs/pricing): Gemini Live é **só WebSocket, sem SIP** — integrar com Asterisk exige ponte (ARI `externalMedia` → WebSocket).

FATO: ElevenLabs Agents aceita SIP trunk (fonte: https://elevenlabs.io/pricing/agents).

#### Custo por minuto estimado

- HIPÓTESE: `gpt-realtime-2.1-mini` custa ~$0,018/min de ligação. Conversão de tokens para minutos usa a razão antiga ~600 tok/min de entrada e ~1200 tok/min de saída, **não confirmada** para os modelos 2.1, e depende da fração do minuto em que cada lado fala (não medida).
  Resolve: ligação de teste + usage do projeto no dashboard OpenAI (tokens in/cached/out por ligação).
- HIPÓTESE: `gpt-realtime-2.1` (full) custa ~$0,058/min, com a mesma razão de tokens não confirmada.
  Resolve: mesma medição da PoC (usage por ligação), se o full for testado.
- HIPÓTESE: `gemini-3.8-live` custa ~$0,014/min de ligação.
  Resolve: ligação de teste na etapa 2 + fatura/usage do projeto Google.
- HIPÓTESE: ElevenLabs Agents custa $0,08/min **mais o LLM** (valor do LLM não estimado).
  Resolve: só avaliar se as etapas 1 e 2 reprovarem; nesse caso, ligação de teste + fatura.
- HIPÓTESE: o custo real é maior que a estimativa linear, porque o contexto da sessão é re-cobrado como entrada a cada turno (parte como cached input). HIPÓTESE: no Gemini, silêncio na linha conta como entrada de áudio.
  Resolve: ligações de 3 min e 10 min comparadas com a fatura/usage (se o custo por minuto cresce com a duração, a re-cobrança de contexto está confirmada).
- HIPÓTESE: latência speech-to-speech (Arquitetura B) é menor que o pipeline STT → LLM → TTS (Arquitetura A).
  Resolve: medir latência de turno na PoC.
- HIPÓTESE: a qualidade em PT-BR com áudio telefônico de 8 kHz (G.711) é aceitável — **não medida**.
  Resolve: teste de ouvido + WER na PoC.

#### Tabela antiga (DESATUALIZADA — capturada 2026-06-04, substituída pelos preços de 2026-09-23)

| Modelo | Latência | $/min |
|--------|----------|-------|
| OpenAI Realtime API (`gpt-4o-realtime-preview`) | ~300ms | $0.06 in + $0.24 out |
| Gemini 2.5 Live API | ~200ms | $0.30 (preview) |

Latência típica: 200-500ms. Custo: $0.10-0.30/min. Complexidade: média (1 WebSocket bidirecional).

## Mudanças necessárias no gateway

- Novo endpoint `/v1/realtime` (WebSocket bidirectional)
- Upstream registry suporte `realtime` role
- Add upstreams: `openai-realtime`, `gemini-live`
- Tier-fallback chain real-time (Gemini Live → OpenAI Realtime)
- Bilhetagem por minuto streaming (não por token)
- Audit log adapter pra WebSocket sessions

## Integração com infra telefonia

- voice-api ifix (DEV-only atualmente, decommission pending Phase 11.5+)
- Asterisk PJSIP → ARI (Asterisk REST Interface) para captura áudio in-call
- OU Twilio Media Streams se telefonia externa
- Codec: PCM 16khz mono → OpenAI/Gemini esperam g.711/Opus

## Pré-requisitos antes do phase 12 voice-realtime

1. Phase 11 prod-hardening fechado (corpus + SLO baseline)
2. Gateway WebSocket infra (não existe hoje — só HTTP)
3. Avaliação latência real PJSIP → gateway → OpenAI realtime end-to-end (PoC 1-2 dias)
4. Decisão arquitetura A vs B (custo × latência × PT-BR quality)

Atualização 2026-09-23:

- O item 3 (PoC de latência) agora é coberto pelo [POC-PLAN.md](../quick/260923-sho-poc-voz-realtime-openai-sip/POC-PLAN.md).
- A PoC via SIP nativo **não depende** do item 2 (WebSocket no gateway): o Asterisk fala SIP direto com a OpenAI e o controle é por webhook + API HTTP. A integração no ai-gateway (endpoint realtime, bilhetagem por minuto) fica para depois do resultado da PoC.

## Quick-wins até lá

- Workflow atual `PROCESSADOR-IA-GRAVACOES-V3` (batch pós-call) continua útil pra análise/CRM
- Whisper batch tier-1 (OpenAI whisper-1) é suficiente para esse caso
- Gemini multimodal pode substituir cadeia STT+chat batch para reduzir custo + diarização nativa


## Resultado da PoC (2026-09-26)

Laboratório concluído — relatório consolidado: `.planning/quick/260923-sho-poc-voz-realtime-openai-sip/LAB-REPORT.md`.
Decisão Pedro: voz principal `gemini-3.8-live-extended-thinking` (thinking medium) via ponte ARI externalMedia,
fallback OpenAI `gpt-realtime-2.1-mini` via SIP; cérebro `gpt-oss-120b@Cerebras` (cadeia por tokens/s, OpenRouter direto).
Próximo: implementar no DiscLight (voip-api).
