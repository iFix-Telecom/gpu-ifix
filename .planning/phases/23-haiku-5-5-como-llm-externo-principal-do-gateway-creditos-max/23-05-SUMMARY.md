---
phase: 23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
plan: 05
subsystem: gateway/proxy
tags: [anthropic, keypool, redis, haiku, tdd]
requires: [23-01]
provides:
  - ParseAnthropicKeys / AnthropicKey (env UPSTREAM_LLM_ANTHROPIC_API_KEYS)
  - KeyPool / NewKeyPool / Snapshot / AnthropicKeyStatus (consumido pelo admin GET do 23-12)
  - NewAnthropicKeyPoolTransport (consumido pelo wiring do 23-14)
  - errNoUsableAnthropicKey (Is errUpstreamRetryable + SkipBreaker()=true, contrato do 23-04)
affects: [23-12, 23-14, 23-04]
tech-stack:
  added: []
  patterns: [RoundTripper com troca de chave pré-byte, estado Redis JSON com prefixo constante (molde force_override.go), memória ⊕ Redis pelo mais restritivo]
key-files:
  created:
    - gateway/internal/proxy/anthropic_keypool.go
    - gateway/internal/proxy/anthropic_keypool_test.go
  modified: []
decisions:
  - "Parse aceita '@' dentro da chave: só trata o sufixo após o último '@' como dia se for numérico; numérico fora de 1-31 é erro"
  - "Rótulo duplicado no env é erro de parse"
  - "Reconciliação não sobrescreve marcação mais restritiva já presente no Redis (outra réplica)"
  - "Limpeza (DEL) que falhou com Redis fora fica pendente e é reaplicada na reconciliação; enquanto pendente, a marcação do Redis é ignorada"
  - "reason gravado = '<verdict>_http_<status>' (ex.: exhausted_http_400) — nunca o corpo da Anthropic"
  - "Cada operação Redis do pool tem timeout de 300 ms (não trava requisição do cliente com Redis fora)"
metrics:
  duration: ~25min
  completed: 2026-10-15
  tasks: 2
  files: 2
---

# Phase 23 Plan 05: KeyPool da Anthropic Summary

Pool de chaves das contas Max em ordem fixa, com esgotamento marcado até o dia de renovação de cada conta (clamp ao fim do mês), re-teste a cada 6 h (revoked: 24 h) usando a própria requisição real, troca de chave antes de qualquer byte ir ao cliente e estado em memória + Redis (`gw:anthropic:key:<fp>`) reconciliado pelo mais restritivo. Tudo provado sem chave real (fake Anthropic em httptest + miniredis).

## O que foi feito

- **Task 1 (RED, `fd4bbf7`):** `anthropic_keypool_test.go` com 21 `t.Run` literais (37 subtestes executados contando a tabela do classificador e o loop de parse malformado): parse, classificador (crédito baixo 400/403, 402, spend limit 429, usage limit 400, 401, 429 com/sem retry-after, 500/529/504, 400 de prefill), transporte (troca A→B, ida direta a B, re-teste em 6 h que limpa, sem dia → now+6h, todas esgotadas → erro retryable sem chamar o fake, 529 passa direto, cooldown 429, clamp dia 31, virada de mês, headers, body reenviado idêntico), Redis (fora → memória + WARN uma vez; reconciliação após Restart; marcação só no Redis respeitada; divergência → mais restritivo nos dois sentidos), revoked 24 h com ERROR re-armado e prova de que logs/erro/Redis/Snapshot não contêm a chave.
- **Task 2 (GREEN, `6dd8081`):** `anthropic_keypool.go` implementando os contratos de `<interfaces>`. Passou na primeira execução.

## Verificação

- FATO: `go test ./gateway/internal/proxy/ -run 'KeyPool|ClassifyAnthropicKeyError|ParseAnthropicKeys' -count=1 -race` → `ok` (4.1 s).
- FATO: pacote `./gateway/internal/proxy/` inteiro → `ok` (21 s); `gofmt -l` vazio; `go vet` limpo.
- FATO: critérios de aceite por grep: `gw:anthropic:key:` = 1, `SkipBreaker` = 3, `rand.|roundRobin|round-robin` = 0.
- Observação: o go.mod fica na raiz do repo (não em `gateway/`), então o comando real é `go test ./gateway/internal/proxy/` a partir da raiz, e não o `cd gateway && go test ./internal/proxy/` que o plano escreveu.

## Deviations from Plan

Nenhuma de comportamento. O plano foi executado como estava escrito; as decisões de detalhe estão no frontmatter (`decisions`).

## Pendências / HIPÓTESES herdadas

- HIPÓTESE H1 (RESEARCH): o status HTTP de "credit balance is too low" não está documentado. O classificador casa a substring em qualquer 4xx. Para confirmar, é preciso capturar a resposta real de uma chave sem saldo.
- O wiring (`fallthroughRoundTripper` por fora para falhas de dial, leitura do env, admin GET) é responsabilidade dos planos 23-12 e 23-14.
- O hook `clickup-link-enforce` avisou que não há tarefa ClickUp vinculada a esta worktree. Não rodei o wizard dentro do executor paralelo; o vínculo fica com o orquestrador da fase.

## TDD Gate Compliance

RED `test(23-05)` (fd4bbf7) → GREEN `feat(23-05)` (6dd8081). Não houve REFACTOR separado.

## Self-Check: PASSED

- FOUND: gateway/internal/proxy/anthropic_keypool.go
- FOUND: gateway/internal/proxy/anthropic_keypool_test.go
- FOUND: fd4bbf7, 6dd8081
