# Teste cego de voz TTS PT-BR (fase 23, plano 23-17)

Gera amostras das mesmas 5 frases em cada candidato de TTS externo (OpenRouter
`POST /api/v1/audio/speech`) e no TTS atual do gateway (Kokoro, controle), com
nomes que não revelam o modelo. A pessoa ouve, escolhe uma letra, e só depois o
gabarito é aberto.

## Uso

Os segredos entram só por variável de ambiente, nunca em arquivo:

| Variável | De onde vem |
|----------|-------------|
| `OPENROUTER_API_KEY` | env `UPSTREAM_LLM_OPENROUTER_AUTH_BEARER` do container do gateway (`docker inspect` na worker-vm) |
| `GATEWAY_TTS_KEY` | chave do tenant `claude-tts` (`TTS_TOKEN` em `~/.config/claude-tts/config.env`) |

```bash
OPENROUTER_API_KEY=... GATEWAY_TTS_KEY=... python3 scripts/tts-blind-test/gen_samples.py
# opções: --out DIR  --only modelo1,modelo2  --no-control
```

Saída em `/home/pedro/tts-blind-test/AAAA-MM-DD/`:

- `amostra-<letra>-frase<n>.mp3`: as amostras cegas, 5 por letra.
- `.gabarito.json` (modo 600): para cada letra, modelo, voz, latência por frase
  e média, e custo estimado (caracteres × preço por caractere da API de modelos).
- `candidatos.json`: o contrato de cada candidato **sem a letra** (Content-Type
  devolvido, MP3 ok, motivo do descarte). É o que vai para o evidence.

## Método

1. As mesmas 5 frases em todos os candidatos: saudação no estilo do claude-wpp,
   valor em reais (`R$ 1.234,56`), data e hora, protocolo (`2026-4471-88`) e uma
   resposta técnica de cerca de 300 caracteres.
2. Contrato antes do teste: toda síntese pede `response_format: "mp3"`. O script
   guarda o `Content-Type` e confere o cabeçalho MP3 (tag `ID3` ou frame sync
   `0xFFF`). Se um candidato devolver erro, rejeitar a voz ou não devolver MP3
   válido, ele sai do teste e o motivo fica em `candidatos.json`.
3. As letras são sorteadas com `random.SystemRandom` só entre os candidatos
   aceitos. O controle (Kokoro atual via gateway, `tts-1`/`pm_alex`) recebe uma
   letra como os demais, sem marcação.
4. Quem ouve não abre `.gabarito.json` antes de escolher.
