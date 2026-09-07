---
quick_id: 260907-poc
slug: poc-tts-ptbr-gpu
date: 2026-09-07
status: complete
---

# SUMMARY — POC TTS pt-BR em GPU (Chatterbox × XTTS-v2 × Kokoro)

## Resultado

Artifact A/B publicado: https://claude.ai/code/artifact/c4a6d5e8-1a92-4d3c-b0e4-09cdfea87383
— 5 frases de atendimento × 4 vozes (Chatterbox pt, XTTS-v2 Ana Florence,
Kokoro pf_dora, Kokoro pm_alex), com latência por frase. Decisão de ouvido = Pedro.

Pod POC 50176646 (3060 KR, $0,0289/h, ~1h de vida) DESTRUÍDO após download.

## Números (RTX 3060)

| Modelo | Lat. média/frase | VRAM pico | Licença |
|--------|-----------------|-----------|---------|
| Chatterbox multilingual (pt) | ~5,4s (warmup 17,8s) | 3,2 GB | MIT |
| XTTS-v2 | ~3,3s | 1,9 GB | Coqui CPML (não-comercial) |
| Kokoro (prod atual) | ~2,0s | <1 GB | Apache 2.0 |

## Gotchas de instalação (valem p/ eventual produtização)

1. `chatterbox-tts`: pip limpo funciona em venv (torch próprio). API:
   `ChatterboxMultilingualTTS.from_pretrained(device="cuda").generate(txt, language_id="pt")`.
2. `coqui-tts` 0.27.5: NÃO puxa torch (venv puro quebra) e exige janela
   `transformers >=4.54 <5` (`is_torchcodec_available` + `isin_mps_friendly`);
   com transformers 5.x ou 4.51 quebra em imports diferentes. Funcionou:
   python do sistema (imagem pytorch, torch 2.5.1) + `transformers==4.57.1`.
   `COQUI_TOS_AGREED=1` obrigatório.
3. Padrão POC sem SSH: onstart auto-contido + `http.server` na porta mapeada
   (status.json/logs/áudios via HTTP) — funcionou 100%.

## Pendente

- Pedro ouvir e escolher. Se Chatterbox/XTTS: produtizar = server
  OpenAI-compatible no pod unificado + upstream tts no gateway + voz clonada
  iFix (5-10s de referência). XTTS tem trava de licença comercial.
