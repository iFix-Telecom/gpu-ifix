---
quick_id: 261108-sgr
status: complete
card: 17u65kmj81y
---

# 261108-sgr — STT: Groq só como reserva real — SUMMARY

## Feito
- `proxy.NewSTTBudgetFallthroughTransport`: limite de cabeçalho do gemini-stt por requisição
  (25 s + 0,1 s/segundo de áudio, teto 150 s). Timer próprio cancela um ctx filho só até os
  headers chegarem; leitura do corpo não é cortada (ctx liberado no Close do corpo).
- Duração conservadora para o limite (`withSTTAudioUpperSeconds`), gravada pelo middleware de
  STT: WAV exato; demais a 8 kbps. Não mexe no valor de billing.
- `sttSlowTimeoutError`: estouro de tempo em upstream STT cascateia (Is errDialFailedFallthrough)
  com `SkipBreaker()` → não abre o breaker. Vale para gemini-stt e groq-whisper. Dial/5xx/envelope
  de erro continuam contando. Chat inalterado.
- `ResponseHeaderTimeout` do transport do gemini-stt = teto + 5 s (rede de segurança).

## Verificação
- `go test ./internal/proxy/ ./cmd/gateway/` ok; `go vet` ok; `gofmt -l` vazio.
- Testes novos: `transport_stt_budget_test.go` (orçamento, estimativa 8 kbps/WAV, estouro sem
  penalidade, corpo lento após o limite legível, cancelamento do cliente não cascateia, dial conta).

## Risco aceito
- Gemini totalmente travado não abre mais o breaker por timeout: cada requisição espera o limite
  (≥25 s) e cai no Groq. Falha de conexão/5xx continua abrindo.

## Desvios anotados (fora do escopo)
- Billing STT subconta mp3 de baixa taxa ~16× (`assumedKbps=128`; gravações ~8 kbps).
- Sucesso em CLOSED não zera `ConsecutiveFailures` dos tier-1 (falhas reais acumulam entre dias).
