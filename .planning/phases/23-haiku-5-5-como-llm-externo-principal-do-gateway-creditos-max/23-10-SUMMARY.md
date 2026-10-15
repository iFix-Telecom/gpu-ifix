---
phase: 23-haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
plan: 10
subsystem: gateway/billing + proxy + gatewayctl + price-sync
tags: [billing, haiku, cache, openrouter, phantom, prices]
requires: [23-03, 23-06, 23-19]
provides:
  - "FinalizeRequest: cost_source provider_reported (usage.cost) | price_table"
  - "priceAnthropicDirect: preço de lista cache-aware com degrau :long (>100k)"
  - "billing.RequestUsage.TokensCacheRead / TokensCacheWrite / ReportedCostUSD()"
  - "phantom de chat self-hosted a preço anthropic/claude-haiku-5.5"
  - "gatewayctl prices set com units cache_read_token|cache_write_token|cache_write_1h_token"
  - "price-sync grava linhas anthropic fixas + units de cache do OpenRouter"
affects: [23-16, 23-17, dashboard de billing]
tech-stack:
  added: []
  patterns: ["applyUsageFrame: único ponto de escrita do usage para os 3 consumidores", "guarda de faixa em valor reportado por terceiro com fallback para tabela"]
key-files:
  created:
    - gateway/internal/proxy/interceptor_usage_haiku_test.go
  modified:
    - gateway/internal/proxy/interceptor_usage.go
    - gateway/internal/proxy/interceptor_usage_cost_test.go
    - gateway/internal/billing/accountant.go
    - gateway/internal/billing/prices.go
    - gateway/cmd/gatewayctl/prices.go
    - gateway/cmd/gatewayctl/prices_test.go
    - scripts/price-sync/gateway-price-sync.sh
    - scripts/price-sync/README.md
decisions:
  - "maxReportedCostUSD = 100 fixo (não configurável), justificado no godoc: guarda contra valor corrompido, não limite de negócio"
  - "Sem linha de cache_read/cache_write para o passo ①, os tokens são cobrados como input_token (fallback conservador) e o WARN/métrica prices_missing existente denuncia a lacuna. O mesmo vale para cache_write"
  - "cache_write do passo ① usa sempre cache_write_token (TTL 5m), porque o usage não informa o TTL. A linha cache_write_1h_token é gravada mas ainda não é consumida"
  - "anthropic/claude-haiku-5.5 entrou no MODEL_MAP do price-sync para garantir a chave do phantom, sem depender da discovery"
  - "A discovery do price-sync passa a incluir upstream openrouter-deepseek, além de openrouter-chat"
metrics:
  duration: "~25min"
  completed: 2026-10-15
  tasks: 2
  files: 9
---

# Phase 23 Plan 10: Billing fiel (usage.cost, cache, degrau 100k, phantom Haiku) — Summary

O custo de cada request passou a refletir o que o provedor cobra de fato. No OpenRouter, o `usage.cost` reportado vira `cost_external_brl` com `cost_source=provider_reported`, protegido por uma guarda de faixa 0 ≤ cost < US$ 100. No passo ① (anthropic-haiku) o custo é calculado a preço de lista e considera cache read, cache write e o degrau `:long` acima de 100k tokens de entrada. As colunas `tokens_cache_read` e `tokens_cache_write` agora são gravadas. A economia (phantom) de chat servido pelo pod é medida a preço Haiku. Os preços novos podem ser operados pelo `gatewayctl` e são mantidos pelo timer de price-sync.

## Tarefas

| Task | Nome | Commits | Arquivos |
|------|------|---------|----------|
| 1 | usage com cost/cache, custo ① com degrau, providers novos, phantom Haiku | 7ab1842 (RED), 9b914de (GREEN) | interceptor_usage.go, interceptor_usage_haiku_test.go, interceptor_usage_cost_test.go, accountant.go |
| 2 | gatewayctl prices e price-sync com units de cache e linhas anthropic | c749a62 (RED), 51debce (GREEN) | cmd/gatewayctl/prices.go, prices_test.go, billing/prices.go, gateway-price-sync.sh, README.md |

## Verificação (FATOS, com fonte)

- RED confirmado nos dois ciclos. Na Task 1 falharam os 12 testes novos. Na Task 2 o build falhou com `undefined: validPriceUnit`.
- `go test ./gateway/... -count=1` passou em todos os pacotes. `gofmt -l gateway` saiu vazio. `go vet` passou em proxy, billing e gatewayctl. `go build ./gateway/...` passou.
- Os casos do `<behavior>` estão cobertos por `interceptor_usage_haiku_test.go`:
  - JSON e SSE do openrouter-chat dão 0.00615 BRL, provider_reported, cache read 11800.
  - SSE sem `cost` cai em price_table.
  - openrouter-deepseek usa a chave openrouter-fireworks.
  - Haiku com cache dá US$ 0,000555 × fx.
  - Prompt de 120000 usa a linha `:long`.
  - Sem linha de cache, os tokens são cobrados como input.
  - Phantom de local-llm a preço Haiku.
  - cost −0.5 e 150 são rejeitados com WARN `usage_cost_out_of_range`; cost 0 é aceito.
  - openrouter-tts usa a chave (tts-1, openrouter); kokoro-tts usa (tts-1, openrouter-fireworks).
  - O mapeamento de `providerForUpstream` foi testado.
- O `DRY_RUN=1` do price-sync rodou com um `ssh` falso no PATH, para não tocar a worker-vm. Saída:
  - imprimiu as 5 units de `claude-haiku-5-5` e as 5 de `claude-haiku-5-5:long` (provider anthropic);
  - imprimiu `anthropic/claude-haiku-5.5` com `cache_read_token` 0.00000001 e `cache_write_token` 0.000000125, lidos ao vivo do `/models` do OpenRouter. Isso prova que o OpenRouter expõe `input_cache_read`/`input_cache_write` para esse modelo;
  - terminou com `anthropic_updated=2 models_updated=5`. A discovery deu WARN, como esperado com o ssh falso.
- `bash -n` sem erro.
- Greps do critério de aceite em interceptor_usage.go: `provider_reported` 2, `maxReportedCostUSD` 5, `usage_cost_out_of_range` 2, `claude-haiku-5-5:long` 2, `"anthropic-haiku"` 2, `CachedTokens` 2. Em prices.go, `cache_write_1h_token` ≥ 1, e `claude-haiku-5-5:long` aparece no script.

## Desvios do plano

### Corrigidos automaticamente

**1. [Rule 3 - Bloqueio] Campos novos em `billing/accountant.go`**
- **Problema:** o plano pede para propagar cached, cache_write e reportedCost "até o objeto de usage lido em FinalizeRequest", mas esse objeto é `billing.RequestUsage`, que fica em accountant.go, arquivo fora da lista do plano.
- **Correção:** adicionei os atômicos `TokensCacheRead`/`TokensCacheWrite` e `reportedCostUSD atomic.Pointer[float64]`, com `SetReportedCostUSD`/`ReportedCostUSD()`.
- **Commit:** 9b914de

**2. [Rule 1 - Regressão esperada] `TestFinalizeEmergencyPodIsSavings` ajustado**
- **Problema:** o teste antigo esperava o phantom de chat pelo model real (qwen). A decisão HAIKU-09 deste plano muda isso para preço Haiku.
- **Correção:** o teste agora semeia `anthropic/claude-haiku-5.5 @ openrouter-fireworks` e garante que a linha `qwen` é ignorada.
- **Commit:** 9b914de

**3. [Rule 2 - Crítico] Chave do phantom garantida e discovery do ②**
- **Problema:** o phantom de chat depende da linha `anthropic/claude-haiku-5.5 @ openrouter-fireworks`, que até aqui só existia via discovery. A discovery, por sua vez, só olhava `upstream='openrouter-chat'`, e o ② agora é `openrouter-deepseek`.
- **Correção:** `anthropic/claude-haiku-5.5` entrou no `MODEL_MAP` e a query de discovery passou a incluir `openrouter-deepseek`. O README foi atualizado.
- **Commit:** 51debce

**4. [Nota] prices_test.go mudou de pacote**
- O arquivo era um placeholder em `package main_test`, com skip. Ele passou para `package main` para testar `validPriceUnit`/`priceUnitList` e o retorno 2 de `runPricesSet`, que acontece antes de qualquer acesso ao banco.

## Known Stubs

Nenhum.

## Threat Flags

Nenhuma superfície nova além das previstas no threat model. T-23-31 foi mitigado pela guarda de faixa. Em T-23-33, o script continua sem imprimir a chave do OpenRouter: o DRY_RUN mostra só os comandos `prices set`.

## Hipóteses

- HIPÓTESE (herdada do plano): o contexto máximo do Haiku 5.5 é de 1M tokens. Ela só sustenta a margem do teto de US$ 100 e não muda o comportamento. Resolve consultando a ficha do modelo na Anthropic.
- HIPÓTESE: o `cache_write_tokens` do passo ① é cobrado como escrita de 5 minutos. O usage traduzido não informa o TTL. Resolve comparando com a fatura da Anthropic, ou com o campo `cache_creation.ephemeral_1h_input_tokens` se o 23-06 passar a expô-lo.

## Self-Check: PASSED

- FOUND: gateway/internal/proxy/interceptor_usage_haiku_test.go
- FOUND commits: 7ab1842, 9b914de, c749a62, 51debce
