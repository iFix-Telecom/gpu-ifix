# 23-TTS-EVIDENCE: teste cego de voz TTS PT-BR (HAIKU-08)

Plano 23-17. Amostras geradas em 2026-10-15 com `scripts/tts-blind-test/gen_samples.py`.
Nenhum segredo neste arquivo: a chave do OpenRouter veio do env do container do
gateway e a do tenant claude-tts veio de `~/.config/claude-tts/config.env`, as duas
só no ambiente do processo.

## Candidatos

Fonte da lista de modelos e vozes: `GET https://openrouter.ai/api/v1/models?output_modalities=speech`
(35 modelos em 2026-10-15), campos `supported_voices`, `description` e `pricing.prompt`.
Contrato da API: doc OpenRouter TTS (`/docs/api/api-reference/tts/create-speech`):
`response_format` aceita `mp3|pcm` e o default é `pcm`.

Critério de seleção: modelos multilíngues que dizem cobrir português. Só o MAI-Voice-2.1
tem voz pt-BR nomeada (`pt-BR-*`). ElevenLabs e Grok detectam o idioma pelo texto. Escolhi
vozes masculinas para ficarem comparáveis ao controle (`pm_alex`). A voz `eric` da ElevenLabs
foi escolha minha, sem medição prévia. Por isso a mesma voz foi usada nos três modelos
ElevenLabs: assim a comparação fica entre os modelos.

Todas as sínteses pediram `response_format: "mp3"`. O script conferiu o cabeçalho MP3
(`ID3` ou frame sync `0xFFF`) em cada resposta.

| Modelo | Voz | Fonte da voz | US$/M chars | Content-Type | MP3 ok? | Situação |
|--------|-----|--------------|-------------|--------------|---------|----------|
| `elevenlabs/eleven-multilingual-v2` | `eric` | supported_voices; 29 idiomas | 40 | audio/mpeg | sim (5/5) | no teste cego |
| `elevenlabs/eleven-v3` | `eric` | supported_voices; 70+ idiomas | 40 | audio/mpeg | sim (5/5) | no teste cego |
| `elevenlabs/eleven-v4` | `eric` | supported_voices; 90+ idiomas | 40 | audio/mpeg | sim (5/5) | no teste cego |
| `microsoft/mai-voice-2.1` | `pt-BR-Rafael:MAI-Voice-2.1` | supported_voices, voz pt-BR nativa | 22 | audio/mpeg | sim (5/5) | no teste cego |
| `x-ai/grok-voice-tts-1.0` | `rex` | supported_voices; 20+ idiomas, detecção automática | 15 | audio/mpeg | sim (5/5) | no teste cego |
| `google/gemini-3.8-flash-tts` | `Charon` | supported_voices (vozes Gemini) | 0,5 in + 9 out (tokens) | application/json (400) | não | **DESCARTADO**: `Gemini TTS only supports response_format="pcm". Got "mp3".` (HTTP 400). Os clientes atuais pedem mp3 (claude-tts manda `mp3` explícito) e o Director só força mp3 quando o campo vem ausente, então o Gemini não cumpre o contrato. |
| Controle: Kokoro atual via gateway | `tts-1` / `pm_alex` | cadeia TTS atual do gateway (tenant claude-tts) | 0 (CPU local) | audio/mpeg | sim (5/5) | no teste cego (letra oculta) |

Resultado: 6 letras × 5 frases = **30 amostras MP3**.

Primeira rodada (descartada e refeita): o controle deu 401 `malformed_api_key`. O script de
execução não tirava as aspas simples do `TTS_TOKEN` no config.env. Corrigi e rodei tudo de
novo. Os arquivos da primeira rodada foram apagados antes da segunda, então as letras são
as do sorteio da segunda rodada.

Latência medida (FATO, cliente em ops-claude, uma requisição por frase):
- externos aceitos: de 1,2 s a 4,7 s por frase (a frase de 275 caracteres é a mais lenta);
- controle: de 8,5 s a 41,4 s por frase (41,4 s na frase de 275 caracteres).
O valor por candidato fica no gabarito e só é revelado depois da escolha, junto com a letra.
Com isso a HIPÓTESE H8 do research (1 a 3 s por frase no externo) fica **parcialmente
confirmada**: frases curtas ficaram entre 1,2 e 3,2 s, e a frase longa chegou a 4,7 s em um
dos modelos.

Custo das amostras (5 frases = 632 caracteres, preço × caracteres):
US$ 0,0253 para cada ElevenLabs, US$ 0,0139 para o MAI-Voice-2.1 e US$ 0,0095 para o Grok.
Somando as duas rodadas, o gasto ficou abaixo de US$ 0,20. No volume medido no research
(21.013 caracteres em 30 dias), o mês custaria US$ 0,84 a US$ 40/M, US$ 0,46 a US$ 22/M e
US$ 0,32 a US$ 15/M.

## Método cego

- Diretório: `/home/pedro/tts-blind-test/2026-10-15/` (fora do repo).
- Arquivos: `amostra-<A..F>-frase<1..5>.mp3`. O nome não revela o modelo.
- As 5 frases são as mesmas para todas as letras:
  1. Saudação no estilo do claude-wpp ("Oi, Pedro! Aqui é o assistente da iFix...").
  2. Valor: "O valor total da fatura é de R$ 1.234,56...".
  3. Data e hora: "...marcada para 22/10/2026, às 14h30...".
  4. Protocolo: "Seu protocolo de atendimento é 2026-4471-88...".
  5. Resposta técnica longa (275 caracteres).
- As letras foram sorteadas com `random.SystemRandom` só entre os candidatos aceitos. Uma
  delas é o controle (Kokoro atual), sem marcação.
- `.gabarito.json` tem modo 600 e não deve ser aberto antes da escolha.
- `candidatos.json` no mesmo diretório repete a tabela acima, sem letras.

## Gabarito

Revelado em 2026-10-15 ~16:20 BRT, depois da escolha.

| Letra | Modelo | Voz | Latência média (s) | US$/char |
|---|---|---|---|---|
| **A (escolhida)** | `elevenlabs/eleven-v3` | `eric` | 3,20 | 0,000040 |
| B | `x-ai/grok-voice-tts-1.0` | `rex` | 1,67 | 0,000015 |
| C | `microsoft/mai-voice-2.1` | `pt-BR-Rafael:MAI-Voice-2.1` | 1,69 | 0,000022 |
| D (controle) | `tts-1` Kokoro CPU via gateway | `pm_alex` | 17,04 | 0 |
| E | `elevenlabs/eleven-multilingual-v2` | `eric` | 1,57 | 0,000040 |
| F | `elevenlabs/eleven-v4` | `eric` | 2,64 | 0,000040 |

FATO: a letra escolhida é a de maior latência média entre os externos (3,2 s; 4,66 s na frase longa) e está no maior preço (US$ 40/M chars). No volume do research (21.013 chars/30 dias) o mês fica em ~US$ 0,84.

## Checkpoint — Task 2: Pedro escolhe a voz no teste cego

- 2026-10-15 ~16:15 BRT, resposta literal do Pedro: "voz a".
- Decisão: `elevenlabs/eleven-v3`, voz `eric`.

## Task 3: o que será alterado em prod (registrado antes da mudança)

Snapshot: `~/gw-phase23-tts/stack38{,-file}-pre-20261015T191956.json` (600). Env 93 itens, imagem `develop-5ce151e` (sem troca de imagem).

1. Compose do stack 38: +3 linhas `UPSTREAM_TTS_OPENROUTER_URL`, `OPENROUTER_TTS_DEFAULT_MODEL`, `OPENROUTER_TTS_DEFAULT_VOICE`.
2. Env do stack 38: +3 valores `https://openrouter.ai/api`, `elevenlabs/eleven-v3`, `eric`. Sem `OPENROUTER_TTS_VOICE_MAP`: o Director usa a voz default para qualquer voz do cliente que não esteja no mapa (`openrouter_tts_director.go`), então `pm_alex`, `alloy` etc. viram `eric`.
3. Auth: `openrouter-tts` já lê `UPSTREAM_LLM_OPENROUTER_AUTH_BEARER` (coluna `auth_bearer_env`), presente no Env. Nada novo.
4. Preço: `gatewayctl prices set -model tts-1 -provider openrouter -unit input_token -usd 0.00004` (billing mapeia `openrouter-tts` → provider `openrouter`, `interceptor_usage.go` l.505-508). A linha `(tts-1, openrouter-fireworks)` (phantom do Kokoro) não é tocada.
5. Alias: nenhum. `model_aliases.upstream` tem CHECK `('llm','stt','embed')` (migration 0005), então não há linha TTS possível; a ordem vem do tier/prioridade (tier 0 `kokoro-tts` com breaker OPEN → tier 1 `openrouter-tts` prio 0 → `local-tts` prio 10).
6. No mesmo PUT: `UPSTREAM_LLM_ANTHROPIC_API_KEYS` ganha os dias de renovação informados pelo Pedro (`max-pedro` dia 19, `max-financeiro` dia 13); ver 23-MAXKEYS-EVIDENCE.

Rollback: PUT com o snapshot acima.

## Ajustes necessários para a Task 3 (registrados antes da execução)

Estado de prod lido em 2026-10-15 via `GET /admin/upstreams` (somente leitura), role tts:

| Upstream | Tier | Prioridade | Enabled | Env da URL | Observação |
|----------|------|------------|---------|------------|------------|
| `kokoro-tts` | 0 | 0 | sim | `UPSTREAM_TTS_KOKORO_URL` | aponta para o IP do pod 3060 desligado (memória pods-desligados-20261014); fica enabled de propósito, para não gerar 503 de "no primary" |
| `openrouter-tts` | 1 | 0 | sim | `UPSTREAM_TTS_OPENROUTER_URL` | env ausente no stack 38, então a linha é ignorada no boot (WARN do 23-16) |
| `local-tts` | 1 | 10 | sim | `UPSTREAM_TTS_URL` | Kokoro CPU (`kokoro-tts:8880`) na worker-vm, que é o fallback real hoje |

1. **Não existe alias TTS no banco.** FATO: `model_aliases.upstream` tem
   `CHECK (upstream IN ('llm','stt','embed'))` (migration 0005, l.7; a 0026 mantém o CHECK e a
   0041 não o amplia). Por isso as linhas `gatewayctl model-alias set` de `tts-1 → openrouter-tts`,
   `tts-1 → kokoro-tts` e o canário `p23-canary-tts` do passo 1/4 da Task 3 **vão falhar**.
   O 23-14 SUMMARY registra o mesmo ponto: o pino TTS no teste de integração usa um
   `ModelPinner` fake.
   Ajuste: o modelo e a voz externos entram pelos envs do Director
   (`OPENROUTER_TTS_DEFAULT_MODEL`, `OPENROUTER_TTS_DEFAULT_VOICE`, `OPENROUTER_TTS_VOICE_MAP`;
   23-07: o Director reescreve o model via resolver ou defaultModel). A ordem
   openrouter-tts → local-tts sai da prioridade do tier 1 da 0041 (openrouter-tts prio 0,
   local-tts prio 10). A cascata por status no TTS está provada em
   `TestHaikuChain_TTSStatusCascade` (23-14).
2. **Nome do fallback.** O plano chama o fallback de "kokoro-tts (tier 1, prio 10)". Em prod,
   o tier 1 prio 10 é o **`local-tts`** (Kokoro CPU). O `kokoro-tts` é o tier 0, o pod 3060
   desligado.
3. **Tier 0 morto na frente.** Sem pino, o `kokoro-tts` (tier 0, IP morto) é tentado antes do
   tier 1. HIPÓTESE: com o breaker dele aberto, o custo é só o salto; com o breaker fechado,
   cada TTS gasta um dial-fail antes de chegar ao openrouter-tts. Para resolver: medir no smoke
   da Task 3 a latência e o `audit_log.upstream`, e checar o breaker do `kokoro-tts`.
4. **Prova de fallback sem canário.** Como não dá para criar alias canário de TTS, o fallback
   ao vivo para o `local-tts` precisa de outro caminho isolado. Opções: (a) aceitar a prova do
   23-14 (`TestHaikuChain_TTSStatusCascade`) mais o histórico de prod, onde o `local-tts` já
   atende hoje; (b) force-open global de `openrouter-tts`, que exige autorização escrita do
   Pedro (por até 60 s todo o TTS de todos os tenants vai para o Kokoro CPU, com voz diferente
   e latência de cerca de 10 a 40 s); (c) migration ampliando o CHECK para `tts`, o que muda o
   escopo do plano.
5. Se a letra escolhida for o controle (Kokoro), o openrouter-tts **não** é ligado e só se
   registra a decisão.

## Task 3: execução (2026-10-15 19:20 UTC)

- `PUT /api/stacks/38?endpointId=6` → HTTP 200. Imagem mantida (`develop-5ce151e`). Compose +3 linhas; Env 93 → 96.
- Task nova `healthy`, `/health` 200. Container com `UPSTREAM_TTS_OPENROUTER_URL=https://openrouter.ai/api`, `OPENROUTER_TTS_DEFAULT_MODEL=elevenlabs/eleven-v3`, `OPENROUTER_TTS_DEFAULT_VOICE=eric`. Sem panic/fatal/ERROR no boot.
- Preço: `prices` id `bb8f94d5…` (`tts-1`, `openrouter`, `input_token`, US$ 0,00004).

### Smoke (tenant claude-tts, `{model:"tts-1",voice:"pm_alex"}`, sem response_format)

| request_id | HTTP | Content-Type | Latência cliente | Cabeçalho | upstream (billing) | tokens_in | cost_external_brl |
|---|---|---|---|---|---|---|---|
| 01a14103-89d1… | 200 | audio/mpeg | 2,20 s | `ID3` v2.4, MPEG layer III 128 kbps 44,1 kHz | openrouter-tts | 34 | 0,006848 |
| 01a14103-92ff… | 200 | audio/mpeg | 1,79 s | `ID3` | openrouter-tts | 34 | 0,006848 |
| 01a14103-9a05… | 200 | audio/mpeg | 1,77 s | `ID3` | openrouter-tts | 34 | 0,006848 |

- `cost_source=price_table`; 34 chars × US$ 0,00004 = US$ 0,00136.
- Log do dispatcher: `role=tts upstream=openrouter-tts` direto, sem tentativa no `kokoro-tts` (tier 0, breaker OPEN). O item 3 dos ajustes (dial-fail no tier 0 morto) não ocorreu nas 3 chamadas.

### RES-08

Chave sensível (telefonia) em `/v1/audio/speech` → HTTP 503 `upstream_unavailable_for_sensitive_tenant`.

### Fallback

Alias canário `p23-canary-tts` impossível (CHECK da migration 0005). Ficou a opção (a): cascata openrouter-tts → local-tts provada em `TestHaikuChain_TTSStatusCascade` (23-14) + `local-tts` servindo TTS em prod até hoje. Sem force-open global (sem autorização do Pedro).

Smoke LLM pós-redeploy (uat10-test, `qwen`) → `claude-haiku-5-5`, `finish_reason: stop`.
