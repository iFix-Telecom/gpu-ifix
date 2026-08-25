# Deferred items — quick-260825-anq

- **TestChatProxy_SSEStreamingFlushesPerChunk (gateway/internal/proxy/chat_test.go:91) é flaky sob carga paralela.**
  Teste de wall-clock (3 chunks com sleep 80ms, asserta gap entre chegadas). Passa 30x isolado e 4x
  rodando só o pacote; falha intermitente quando a suíte inteira roda em paralelo (contenção de CPU).
  Já teve um fix de flake anterior documentado no próprio comentário do teste (Read 128-byte → line-oriented).
  Fora do escopo deste quick (chat.go/chat_test.go intocados). Candidato: aumentar o sleep/gap ou
  medir via canal de eventos em vez de wall-clock.
