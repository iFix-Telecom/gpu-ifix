---
quick_id: 261108-pnd
slug: stt-pendencias
cards: [17u65kmj82f, 17u65kmj82g]
parent_quick: 261108-sgr
---

# Quick 261108-pnd — pendências do 261108-sgr

1. **17u65kmj82g** — sucesso em CLOSED não zerava `ConsecutiveFailures` dos tier-1. Capturar status
   comitado em `dispatchTo`; resposta <500 grava sucesso (CLOSED zera, HALF_OPEN fecha); 5xx não grava.
2. **17u65kmj82f** — billing STT subconta mp3 de baixa taxa (~16×). Ler duração do próprio container:
   mp3 Layer III (header CBR / Xing-Info VBR, pula ID3v2) e Ogg/Opus (granule final − pre-skip).
   Limite de espera STT usa o mesmo valor quando exato.

Verificação: testes com fixtures ffmpeg (ffprobe como referência), testes de dispatcher
(falha/200/falha fica CLOSED; falha/502/falha abre), `go test ./internal/proxy ./internal/billing ./cmd/gateway`.
