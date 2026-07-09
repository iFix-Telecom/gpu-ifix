# Debug — STT sem failover intra-tier (gemini-stt → openai-whisper)

status: FIXED (código) — pendente deploy + re-enable gemini-stt
mitigação live: `gatewayctl upstreams disable --name gemini-stt` (2026-07-09 13:15 -03) — STT cai direto no openai-whisper AGORA
data: 2026-07-09
rota: POST /v1/audio/transcriptions (alias `whisper`)

## Sintoma

Com local-stt (t0) breaker OPEN e gemini-stt (t1) retornando UNAVAILABLE/502/timeout,
o gateway devolve 502 ao cliente sem tentar openai-whisper (t1, probe ok, ocioso).
Breaker do gemini-stt NUNCA abre apesar de 502/503 consecutivos.

## Causa raiz — DOIS defeitos estruturais independentes

### Gap A — cascade só avança em falha de DIAL (pré-byte), não em erro de resposta

`cascadeTier1` (dispatcher.go:401-417) só pula pro próximo candidato quando
`res.fallthrough_ && !res.wrote`. Esse sinal SÓ existe pra falha connection-class
pré-byte (`fallthroughRoundTripper` em transport.go:37-45 + `isConnectionClass`:60-82
— só `dial`/ECONNREFUSED/DNS). Decisão D-06 explícita: timeout de header, 5xx, erro
de resposta → NÃO fazem fallthrough.

gemini-stt retornando UNAVAILABLE cai no `ModifyResponse` (gemini_stt_director.go:229-243):
reescreve pra 502 e `return nil` → ReverseProxy COMMITA o 502 ao cliente →
`dispatchTo` marca `res.wrote=true` → `cascadeTier1` trata como "served" (terminal,
retorna true, dispatcher.go:415-416). **Nunca chega em openai-whisper.**

Mesmo pro caso mp3 grande: gemini pendura até ResponseHeaderTimeout 60s → context
canceled → erro NÃO-dial → ErrorHandler escreve 502, wrote=true → terminal.

### Gap B — breaker não é alimentado por tráfego real

Únicas fontes que alimentam o breaker de inference (grep `Breaker.Execute`):
1. `probe.go:227` — loop de health probe.
2. `dispatcher.go:542 recordDialFailure` — sintético, SÓ no fallthrough de dial.

`dispatchTo` chama `proxy.ServeHTTP` DIRETO — NÃO via `Breaker.Execute`. Logo o
resultado real (502/timeout do gemini) é invisível pro breaker. O probe do gemini-stt
é "config ok" (não bate no modelo que falha) → breaker fica CLOSED pra sempre →
dispatcher sempre escolhe gemini-stt. Por isso zero eventos de breaker do gemini-stt
nos logs.

### Gap C (secundário) — timeout por-upstream == deadline do request

`NewAudioProxy` (audio.go:50) usa `ResponseHeaderTimeout: 60s` = deadline do request.
Se gemini pendura, consome todo o budget, não sobra pra fallback mesmo se A for
corrigido. gemini-stt (buildGeminiSTTProxy, main.go:1750+) herda transport próprio —
conferir/baixar pra ~20-30s.

## Fix proposto (cirúrgico, reusa a plumbing existente)

**A — sinalizar erro retryável de upstream como fallthrough:**
- Novo sentinel `errUpstreamRetryable` (errors.go).
- gemini `ModifyResponse:242` → `return errUpstreamRetryable` em vez de `return nil`
  (o ModifyResponse roda ANTES de qualquer byte ir pro cliente em STT não-streaming →
  pré-byte-safe). Idem `WhisperAbortGuard`/openai-whisper pra 5xx.
- `ErrorHandler` (errors.go:69) trata `errUpstreamRetryable` igual ao
  `errDialFailedFallthrough`: suprime write, seta `fallthrough_=true` → cascade avança.
- Guardas: só STT/não-stream (nunca depois de byte escrito — D-07 preservado).

**B — alimentar breaker no fallthrough de erro:** `cascadeTier1`/`dispatchTo` já
chamam `recordDialFailure(name)` no fallthrough. Com A rotulando erro-de-upstream
como fallthrough, o gemini-stt passa a acumular falhas → breaker abre após N →
dispatcher pula direto. (Renomear `recordDialFailure`→`recordUpstreamFailure`.)

**C — timeout por-upstream:** baixar ResponseHeaderTimeout do gemini pra ~25s.

## Validação

Teste: local-stt down + mock gemini-stt retornando UNAVAILABLE →
`POST /v1/audio/transcriptions model=whisper` deve retornar 200 servido por
openai-whisper. Existe `fallthrough_test.go` + `gemini_stt_director_test.go` como base.
RES-08: tenant sensitive NUNCA cascateia pra tier-1 externo — preservar (dispatcher.go:310).

## Escopo tocado
- gateway/internal/proxy/errors.go (sentinel + ErrorHandler)
- gateway/internal/proxy/gemini_stt_director.go (ModifyResponse return)
- gateway/internal/proxy/dispatcher.go (rename + comentário)
- gateway/internal/proxy/audio.go + cmd/gateway/main.go (timeout gemini)
- testes: fallthrough_test.go / gemini_stt_director_test.go
