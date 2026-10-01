---
quick_id: 261001-fjk
type: quick
wave: 1
autonomous: true
clickup: 86akreh6u
---

# Quick 261001-fjk — STT: OOM do local-stt não abre breaker + fallthrough loga status/corpo

## Contexto (FATO, diagnóstico 2026-10-01, card 86akreh6u)
- speaches no pod 3060 devolve HTTP 500 `RuntimeError: CUDA failed with error out of memory` em áudio longo
  (reproduzido: áudio 13:14, fp16, pico 11867/12288 MiB). Correção principal no pod (int8_float16, commit 78b062f).
- Gateway: `internal/proxy/audio.go:54-80` `sttRetryableStatusInterceptor` → `errUpstreamRetryable` p/ todo status >=400
  (decisão Pedro 2026-08-27: qualquer erro cascateia — MANTER). Log de fallthrough só mostra
  `proxy interceptor #0: upstream retryable error` (sem status/corpo).
- `internal/proxy/dispatcher.go:404-416` (tier-0) e `:588-592` (cascata) chamam `recordUpstreamFailure` em todo
  fallthrough exceto `errOverContextFallthrough` (T-ucv-05). Uma rajada de OOM (1 request grande por vez) pode
  abrir o breaker do local-stt e desviar TODO o STT p/ externo.

## Task 1 (tdd)
files: gateway/internal/proxy/audio.go, gateway/internal/proxy/errors.go, gateway/internal/proxy/dispatcher.go, testes em gateway/internal/proxy/
action:
1. No interceptor STT: para status >=400, ler até 2 KiB do corpo (io.LimitReader), restaurar `resp.Body` (NopCloser de bytes + resto) para não quebrar nada downstream, e retornar erro que:
   - SEMPRE satisfaz `errors.Is(err, errUpstreamRetryable)` (cascata inalterada);
   - carrega status e trecho do corpo (sanitizado: 1 linha, ≤300 chars) na mensagem → aparece no log existente;
   - quando o trecho contém `out of memory` (case-insensitive) também satisfaz `errors.Is(err, errSTTResourceExhausted)` (novo sentinel em errors.go).
2. dispatcher: em ambos os pontos, não chamar `recordUpstreamFailure` quando `errors.Is(res.err, errSTTResourceExhausted)` (mesma lógica do overCtx). **NÃO** estender o carve-out de sensitive (RES-08 hard gate segue para OOM). Métrica/log: logar WARN `stt upstream resource exhausted; cascading without breaker penalty` com upstream, status e request_id.
3. Comentários explicando o porquê (diagnóstico 86akreh6u).
verify: testes unitários: 500+OOM → retryable + resource-exhausted + breaker NÃO incrementa; 500 sem OOM → retryable + breaker incrementa; 400 genérico → retryable + mensagem com status/corpo; corpo restaurado legível; sensitive + OOM → bloqueio 503 mantido. `gofmt -l` vazio; `go build ./... && go vet ./... && go test ./...`; `-race` em proxy.
done: OOM não penaliza breaker; logs de fallthrough STT mostram status e causa.

## Restrições
Sem push/deploy/docker. Commits atômicos terminando com `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. SUMMARY em 261001-fjk-SUMMARY.md (não commitar).
