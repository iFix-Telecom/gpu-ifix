---
quick_id: 260907-poc
slug: poc-tts-ptbr-gpu
date: 2026-09-07
status: in-progress
---

# Quick: POC TTS pt-BR em GPU dedicada (Chatterbox × XTTS-v2 × Kokoro)

## Contexto

Pedro quer TTS melhor que Kokoro agora que há GPU. Pesquisa (2026-09-07): sem
benchmark público pt-BR head-to-head; candidatos = Chatterbox Multilingual
(MIT, finetune pt-BR, 63,75% vs ElevenLabs) e XTTS-v2 (veterano pt-BR,
licença non-commercial — Pedro mandou ignorar licença). Desempate = A/B de
ouvido. Ordem: POC em **pod GPU dedicado** (não mexer no pod prod unificado).

## Desenho

- Pod Vast 3060-class (mesma query prod + cpu_ram≥12G, não-CN), label
  `tts-poc`, disco 30G, imagem pytorch cuda.
- Onstart AUTO-CONTIDO (gotcha ssh-key): 2 venvs (chatterbox-tts /
  coqui-tts), gera 5 frases pt-BR idênticas nos 2 modelos, mede latência +
  VRAM pico, serve /root/poc via http.server :8600 (status.json + wavs).
- Kokoro: mesmas 5 frases geradas do pod PROD (3 vozes pt-br) via API.
- Entrega: artifact com players agrupados por frase + tabela
  latência/VRAM. Pedro ouve e decide.
- Pod POC destruído ao final (após download dos áudios).

## Validação

- 5 frases × (chatterbox + xtts + kokoro) audíveis no artifact
- results.json com latência por frase e VRAM pico por modelo
