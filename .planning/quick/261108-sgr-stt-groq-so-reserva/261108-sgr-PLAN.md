---
quick_id: 261108-sgr
slug: stt-groq-so-reserva
card: 17u65kmj81y
---

# Quick 261108-sgr — STT: Groq só como reserva real

## Problema (diagnóstico 2026-11-08, dados de produção)
- groq-whisper (tier-1, prioridade 15) só deveria servir quando o gemini-stt (prioridade 10) estiver fora.
- Causa 1: `ResponseHeaderTimeout` fixo de 25 s no gemini-stt. Chamada longa (ex.: 1027 s em 07/11 11:28)
  estoura → cascata pro Groq. 529 casos em ~30 dias.
- Causa 2: cada estouro grava falha no breaker. Sucesso em CLOSED nunca é gravado, então
  `ConsecutiveFailures` acumula entre dias; no 3º estouro o breaker abre e ~35 s de TODO STT vão pro
  Groq (371 casos em ~30 dias; 7.501 em 21–22/10 com o breaker preso, já corrigido em 5c44c46).
- Gemini: média 2,2 s, p95 4,5 s — não está fora, só demora em áudio longo.

## Mudança
1. Limite de cabeçalho do gemini-stt por requisição: `25 s + 0,1 s × segundos de áudio`, teto 150 s.
   Segundos = estimativa conservadora (WAV exato; demais formatos a 8 kbps — as gravações de
   ligação são mp3 ~8 kbps; a estimativa de billing assume 128 kbps e subestima ~16×).
   Cadeia pior caso: 150 (gemini) + 60 (groq) = 210 s < write timeout de áudio 290 s < n8n 300 s.
2. Estouro de tempo em upstream STT continua cascateando, mas NÃO grava falha no breaker
   (`SkipBreaker()`). Erro real (conexão recusada, DNS, 5xx, envelope de erro) continua contando.
3. Chat não muda (NewChatFallthroughTransport intacto).

## Verificação
- Testes unitários novos em transport (orçamento, estouro → sentinela que pula breaker, cancelamento
  do cliente não cascateia, sucesso libera o ctx só no Close do corpo) e no cálculo do limite.
- `go test ./internal/proxy/...`, `go vet`, `gofmt -l`.

## Fora do escopo (anotado)
- Billing de STT subconta mp3 de baixa taxa (assumedKbps=128).
- Sucesso em CLOSED não zera `ConsecutiveFailures` dos tier-1 (falhas reais acumulam entre dias).
