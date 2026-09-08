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


## Rodada 2 (2026-09-07, feedback Pedro: XTTS lento / Chatterbox sotaque / Kokoro erra R)

Mesmo artifact atualizado (label rodada-2): 57 clips —
- XTTS: 4 vozes (Ana, Alma, Luis, Marcos) × 1,15/1,35× (`speed` nativo).
- Chatterbox: **finetune oficial pt-BR** (ResembleAI/Chatterbox-Multilingual-pt-br)
  default + clone, vs base multilingual (default/expressivo/clone).
- Kokoro: 3 vozes (dora/alex/santa) × 1,0/1,25× (`speed` no speaches).
- Frase nova carregada de R ("roteador... Ricardo retorna... regularizar").

Gotchas novos:
- Finetune pt-br via `from_local` exige OVERLAY: repo pt-br só tem
  `t3_pt_br.safetensors`+grapheme; symlink base (ve.pt/s3gen.pt/conds.pt) +
  t3_pt_br renomeado p/ `t3_mtl23ls_v2.safetensors`. Funciona (shapes batem).
- `int(1.15*100)==114` — nome de arquivo com speed float: usar round().
- pip PyPI ReadTimeout transiente no pod KR: `--retries 10 --timeout 60`.
- Latência finetune ~6-9s/frase na 3060 (mais lento que base ~5-7s).
- POC2 pod 50181552 destruído pós-download.


## Produtização (2026-09-07, decisão Pedro: XTTS com as 4 vozes)

Commit b03bf4c: `xtts-server.py` (wrapper OpenAI-compatible stdlib, :8021,
vozes ana/alma/luis/marcos + aliases pm_alex→Luis/pf_dora→Ana/pm_santa→Marcos,
speed default 1.15, pt fixo, wav fixo) embarcado no onstart; venv `/opt/xtts`
pinado (coqui-tts 0.27.5 + transformers 4.57.1 + torch 2.5.1 cu121);
ENVMAP TTS→8021; provisioner espera health (40min) + valida speech.

Validação no provision noturno: chegou até `xtts speech ok` e caiu num FALSO
NEGATIVO do gate de GPU (gpu_temp=0 = telemetria atrasada da API Vast em
instância nova; GPU real — XTTS rodou em CUDA e a API populou 59°C depois).
Fix: gate com retry 10min. Timer das 20h destruiu o pod velho no meio; órfã
50193014 destruída manualmente 21:04, machine 146050 desbanida.

**Primeira subida XTTS 100% autônoma: timer 2026-09-08 07:00.** Kokoro segue
na 8000 (fallback manual), piper CPU tier-1. Latência esperada XTTS ~3s/frase.
