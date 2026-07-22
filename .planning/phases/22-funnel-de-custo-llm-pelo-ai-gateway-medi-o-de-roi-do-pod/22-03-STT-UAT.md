# 22-03 STT-UAT — ativar STT da converseia pelo gateway (CV-01)

Data: 2026-07-19 · Requirement: CV-01

## Baseline (ANTES)
- Gate 113: **PRESENTE** (22-02, container digest `b19252ba`).
- `gatewayctl usage report --tenant converseai-stt --from HOJE --to HOJE` = **0 rows** (só header).
- Stack 15 env: `AGENT_GATEWAY_BASE_URL=https://ai-gateway.converse-ai.app/v1` SET; **`STT_GATEWAY_KEY` ausente** (inerte). Compose NÃO referenciava `STT_GATEWAY_KEY`.
- Digest imagem agents (rollback): `sha256:b19252ba22eb81bbb79aea5dd0b2cd90ef6946cce25cf39c17c5ee16b584832e`.

## Fallback anti-DoS (T-22-07) — PROVADO no código (FATO)
`media/transcription.py` (commit 680accfa re-landado):
```python
if settings.stt_gateway_key:
    try:
        gateway_client = AsyncOpenAI(base_url=agent_gateway_base_url, api_key=stt_gateway_key,
                                     timeout=agent_gateway_timeout_seconds, max_retries=0)
        transcript = await gateway_client.audio.transcriptions.create(model="whisper", ...)
    except Exception:
        logger.warning("stt.gateway_fallback", ...)
        transcript = None   # força path direto
if transcript is None:
    direct_client = _get_openai_client()   # OpenAI direto (comportamento antigo)
    transcript = await direct_client.audio.transcriptions.create(model=settings.stt_model, ...)
```
⇒ falha do gateway (timeout curto, max_retries=0) → **fallback whisper direto**. Mitigação T-22-07 é REAL (degradação graciosa, não quebra de STT). Log emitido: `stt.gateway_fallback` (erro) / `audio_transcribed stt_model=whisper` (sucesso via gateway).

## ATIVAÇÃO + UAT (checkpoint) — RESULTADO: ROLLBACK (regressão de idioma)

### Ativação
Stack 15: add 4 refs 113 no compose (`:-` inertes) + set `STT_GATEWAY_KEY=ifix_sk_jj7h…` (só STT) → PUT 200 → agents recriou, `key_set=True`.

### Prova de funnel (funcionou tecnicamente)
- POST /v1/audio/transcriptions (tenant converseai-stt, model=whisper), áudio 1.44s → **200 `{"text":"Okay."}`**.
- usage converseai-stt **0→1 req** (audio_s 0.2).
- billing_events tenant 11bfafdf: upstream=**gemini-stt** (não unknown), brl 0.00001 (0.2s × 0.0000096 × 5.10 ✓).

### FALHA UAT (qualitativo, reportada pelo Pedro)
Áudio pt-BR **transcrito em INGLÊS** ("Okay."). O mic da UI também. **Regressão de idioma.**

### ROOT CAUSE (FATO — código gpu-ifix)
`gateway/internal/proxy/gemini_stt_director.go`:
- Converte multipart→Gemini `generateContent` extraindo SÓ audioBytes+mimeType — **descarta o campo `language`**.
- Prompt hardcoded inglês (const `geminiTranscribePrompt = "Transcribe this audio. Return only the transcription text, no commentary."`) → sem dica de idioma, Gemini emite/traduz p/ inglês (pior em áudio curto/ambíguo).
- Contraste: `openai_whisper_director.go` faz multipart→multipart **preservando todos os campos** (linha 254 "Non-model part: stream through") → whisper-1 recebe `language=pt` → pt-BR correto.
- Path direto (`gpt-4o-mini-transcribe`, rollback) honra `language=pt` → pt-BR correto.

Gatilho: pod local-stt DOWN → whisper alias cascata → **gemini-stt** (tier1 priorizado sobre openai-whisper). **Bug pré-existe o funnel** — afeta `transcricao-voip` (que já roteia STT pelo gateway) sempre que o pod está down.

### ROLLBACK executado
`STT_GATEWAY_KEY=""` no stack 15 (PUT 200) → agents recriou, `key_set=False` → STT volta ao `gpt-4o-mini-transcribe` direto (pt-BR). Refs 113 no compose mantidas (inertes). Env exata do rollback: `STT_GATEWAY_KEY` (vazia).

### Bloqueio p/ re-attempt
Re-ativar o funnel STT exige um dos: (A) fix `gemini_stt_director.go` (honrar language + prompt não-traduzível) — gateway redeploy; (B) `gatewayctl upstreams disable --name gemini-stt` → STT usa openai-whisper (honra language, ~10x custo/s, afeta todos tenants); (C) só funnelar STT com pod UP (whisper local honra language); (D) adiar.

## RE-ATIVAÇÃO (pós-fix do director) — RESULTADO: ATIVO

Incidente 21/07 22h40 (n8n /transcription → 429) revelou que **AS 2 CONTAS OpenAI** (gateway openai-whisper `sk-proj-MNUqB` + converseai direto `sk-proj-dLD68`) estão `insufficient_quota` (429 confirmado ao vivo). Logo o rollback (STT direto OpenAI) estava QUEBRADO. Cascata STT quando pod down: gemini-stt(tier1, funciona) → openai-whisper(tier2, 429). Das 19-22h gemini-stt teve outage → tudo no openai-whisper esgotado → 429.

### Fix do director (caminho A do bloqueio) — DEPLOYADO
`gateway/internal/proxy/gemini_stt_director.go`: `extractMultipartField(body,ct,"language")` + `geminiTranscribePromptFor(lang)` (injeta ISO + "Do NOT translate"; fallback neutro "original spoken language"). 2 testes novos + suite proxy verde. Commit `44b1574` → build-gateway CI → stack 38 pinado `develop-44b1574@sha256:8e56a674`. Prova ao vivo: áudio pt-BR via gemini-stt → **"Bom dia, meu nome é Joel e estou ligando para resolver um problema com a minha fatura."** (pt-BR).

### Funnel re-ativado (contorna OpenAI morta)
`STT_GATEWAY_KEY=ifix_sk_jj7h…` re-setada no stack 15 + 4 refs 113 re-adicionadas no compose. Agents recriou (started 02:46Z). Prova: STT via gateway (tenant converseai-stt) → **gemini-stt** → `"Bom dia, tudo bem com você? Gostaria de falar sobre minha fatura."` (pt-BR, 200). billing gemini-stt cost coerente.

### Durabilidade
Deploy Dev re-sincroniza o compose do REPO (apagou as refs via-API 02:50). Ref fixada no repo converseai-v4 `docker-compose.yml` (4 keys 113, commit `e6b1f6ff` pushed) → sobrevive ao Deploy Dev. STT_GATEWAY_KEY value persiste no stack env Portainer.

### Pendências
- **Billing OpenAI**: 2 contas sem quota (top-up se precisar OpenAI direto). gemini-stt cobre STT no interim.
- **Pod (local-stt tier0) down**: decisão do Pedro = aguardar subir sozinho (Seg 9-17 BRT). Quando up, STT vai local (grátis) e o fix do director fica de reserva.
- Outage gemini-stt 19-22h: causa não determinada (logs truncados por restart; recuperou).

### Rollback
STT_GATEWAY_KEY="" no stack 15 — MAS não usar enquanto contas OpenAI sem quota (direto = 429). Preferir gemini-stt.
