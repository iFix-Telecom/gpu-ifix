---
quick_id: 261001-fjk
clickup: 86akreh6u
subsystem: gateway/proxy (STT cascade + breaker)
tags: [stt, breaker, oom, observability]
key-files:
  created:
    - gateway/internal/proxy/stt_oom_test.go
  modified:
    - gateway/internal/proxy/audio.go
    - gateway/internal/proxy/errors.go
    - gateway/internal/proxy/dispatcher.go
    - gateway/internal/proxy/stt_routing_robustness_test.go
completed: 2026-10-01
---

# Quick 261001-fjk: STT OOM não abre breaker + fallthrough carrega status/corpo

Um HTTP 500 "CUDA ... out of memory" do local-stt agora cascateia para o tier-1 sem contar falha no breaker. O erro de fallthrough de qualquer status >= 400 do STT passa a carregar o status do upstream e um trecho do corpo com no máximo 300 caracteres, em uma linha.

## Commits
- `b0be90a` test(261001-fjk): testes que falham (RED)
- `e6386e2` feat(261001-fjk): implementação (GREEN)

## O que mudou
- `audio.go`: `sttRetryableStatusInterceptor` lê até 2 KiB do corpo de erro e devolve o corpo ao `resp.Body` (o trecho lido seguido do resto, com o Closer original). Retorna `*sttUpstreamStatusError`:
  - `errors.Is(err, errUpstreamRetryable)` é sempre verdadeiro, então a cascata e a supressão de escrita no ErrorHandler continuam iguais. A decisão de 2026-08-27 foi mantida.
  - `errors.Is(err, errSTTResourceExhausted)` é verdadeiro quando os 2 KiB lidos contêm `out of memory`, sem diferenciar maiúsculas.
  - Mensagem: `proxy: upstream retryable error, ... (stt upstream status 500: <trecho>)`.
- `errors.go`: novo sentinel `errSTTResourceExhausted`, com comentário sobre o diagnóstico.
- `dispatcher.go`: novo `recordFallthroughFailure`, usado nos 4 pontos que antes chamavam `recordUpstreamFailure` num fallthrough: tier-0 CLOSED, tier-0 sensível depois do retry, pinned e cascata tier-1. Em caso de OOM, não registra a falha e loga em WARN `stt upstream resource exhausted; cascading without breaker penalty` com upstream, status, err e request_id. A exceção de over-context (T-ucv-05) continua nos mesmos lugares.
- O RES-08 continua igual. Para tenant sensível, OOM no tier-0 continua resultando no bloqueio 503, sem ir para o tier-1. Teste: `TestDispatcher_STTOOMSensitiveStillBlocked`.

## Mudanças de comportamento em prod (depois do deploy, que NÃO foi feito aqui)
1. Uma rajada de OOM no local-stt (áudio longo) não abre mais o breaker do local-stt. Áudio curto continua indo para o pod.
2. O OOM continua cascateando para o tier-1. O áudio longo é transcrito no externo pago, como já acontecia antes.
3. Cada OOM gera uma linha WARN, que é visível com o LOG_LEVEL padrão info.
4. Outros status >= 400 (por exemplo, 500 sem OOM ou 400 de disco cheio) continuam penalizando o breaker como antes. A diferença é que a mensagem de erro agora traz status e trecho do corpo.
5. Pod com OOM permanente, que falha em todo request: o breaker do local-stt NÃO abre por causa desses OOMs. Cada request vai primeiro ao pod, recebe o 500 e cai no tier-1. O custo é a latência de uma ida ao pod por request. Esse é o mesmo trade-off da exceção de over-context.

## FATOS (com fonte)
- Testes: `go test ./...` todos ok, `go test -race ./internal/proxy/` ok, `gofmt -l` vazio, `go build ./...`, `go vet ./...` e `go vet -tags integration ./...` ok. Tudo rodado na worktree em 2026-10-01.
- O log `"upstream failure; suppressing 502 for fallthrough"` do ErrorHandler (`errors.go`), que é o log que já existe no modo dispatcher, está em nível **DEBUG**. O padrão de `LOG_LEVEL` é `info` (`internal/config/config.go:440`).
- O log `ERROR "upstream error"` (proxy sem dispatcher, ou candidato final) também passou a mostrar status e trecho. Isso aparece no output do teste: `err="proxy interceptor #0: proxy: upstream retryable error, ... (stt upstream status 500: Internal Server Error)"`.

## HIPÓTESES
- HIPÓTESE: se o gateway de prod roda com `LOG_LEVEL=info`, um fallthrough STT sem OOM (por exemplo, 500 genérico ou 400) **não** aparece com status e corpo no log em modo dispatcher, porque o log que já existe é DEBUG. Só o OOM ganha o WARN novo. Para confirmar, olhar a env `LOG_LEVEL` do serviço `ai-gateway-prod_gateway` no worker-vm. Não verifiquei porque estava fora das restrições desta tarefa (sem docker/prod). Se ficar confirmado e for desejado, a correção é pequena: subir para WARN o log de fallthrough STT em `recordFallthroughFailure` para qualquer `sttUpstreamStatusError`.
- HIPÓTESE: o speaches sempre coloca o texto `out of memory` nos primeiros 2 KiB do corpo do 500. O diagnóstico do card mostra o texto `RuntimeError: CUDA failed with error out of memory`, mas não medi em que posição do corpo ele aparece. Para confirmar, capturar o corpo cru de um 500 de OOM no pod.

## Deviations from Plan
- O plano citava 2 pontos do dispatcher. Apliquei a mesma função nos 4 pontos que registram falha de fallthrough. Os outros 2 são o tier-0 sensível depois do retry e a cascata tier-1 em `cascadeTier1`, que é diferente da linha 588, que é pinned. Fiz isso para manter a regra igual em todo lugar. No ponto do sensível depois do retry, o bloqueio 503 continua incondicional.
- A classificação de OOM usa os 2 KiB lidos, não o trecho de 300 caracteres. Assim uma traceback longa no começo do corpo não esconde o marcador.

## Self-Check: PASSED
- Arquivos existem: `stt_oom_test.go`, `audio.go`, `errors.go`, `dispatcher.go`.
- Commits `b0be90a` e `e6386e2` estão presentes em `git log`.
