# LGPD — Sub-processadores do ifix-ai-gateway

**Last updated: 2026-05-27.**

## Purpose

Este documento divulga os **sub-processadores externos** que podem receber
dados roteados através do `ifix-ai-gateway`. É uma exigência da revisão LGPD
que deve ser concluída **antes** de tenants `sensitive` (Telefonia, Cobranças)
serem ativados em produção.

O gateway é o controlador-operador do tráfego de IA da Ifix. Quando o upstream
local (GPU própria) está disponível, **todos** os dados são processados
localmente e nenhum sub-processador externo recebe dados. Os sub-processadores
externos só entram em cena durante um **failover** — e, mesmo assim, apenas
para tenants de classe `normal`. Tenants de classe `sensitive` **nunca** são
proxiados para um provedor externo.

## Sub-processadores

| Sub-processador | Papel                                                              | Classe de dados que pode receber                        | Quando o dado flui para ele                                                                 |
|-----------------|--------------------------------------------------------------------|---------------------------------------------------------|---------------------------------------------------------------------------------------------|
| **Vast.ai**     | Host da GPU (RTX 4090 alugada) — executa o **upstream local** (Qwen 3.5 27B + Whisper + BGE-M3) | Dados de **todos** os tenants — `normal` **e** `sensitive` | **Sempre** que o gateway está saudável. É o caminho primário; o pod roda na infraestrutura da Vast.ai. |
| **OpenAI**      | Provedor de **failover** (LLM, Whisper STT, embeddings)            | **Somente** dados de tenants `normal` (Campanhas, voice-api) | Apenas durante failover, quando o upstream local cai/satura, **e somente** para tenants `normal`. |
| **OpenRouter**  | Provedor de **failover** (LLM Qwen 3.5 27B via Novita)             | **Somente** dados de tenants `normal` (Campanhas, voice-api) | Apenas durante failover, quando o upstream local cai/satura, **e somente** para tenants `normal`. |
| **MinIO — componente de infraestrutura (sub-processador se aplicável — confirmar com jurídico)** | Armazenamento S3-compatible de pesos de modelos open-source (`s3.ifixtelecom.com.br`, bucket `ai-gateway`); self-hosted internamente — classificação final (sub-processador formal vs componente interno) é decisão do jurídico Ifix | **Nenhum dado de cliente** — apenas artefatos de modelo open-source pré-treinados consumidos pelo pod no boot | **Sempre** no boot do pod GPU primário — o pod baixa os pesos do bucket via `mc cp`/curl antes de servir tráfego. |

> **MinIO bucket `ai-gateway` em `s3.ifixtelecom.com.br` armazena pesos GGUF/safetensors
> do pod GPU primário — nenhum dado de cliente persistido, apenas artefatos de modelo
> open-source pré-treinados.** A inclusão aqui é divulgação completa (full-disclosure)
> do plano de dados, ainda que o sub-processador (se assim classificado pelo jurídico)
> não receba PII por design. A classificação final (sub-processador formal versus
> componente de infraestrutura interno) é decisão do jurídico da Ifix dado o caráter
> self-hosted — esta hedge é intencional e espelhada na carta de sign-off
> (`gateway/docs/LGPD-SIGNOFF-LETTER-TEMPLATE.md`).

> **Garantia "never-external" para tenants sensíveis.** Dados de tenants
> `data_class: sensitive` (Telefonia — áudio de ligações; Cobranças — dados
> financeiros/de cobrança) **NUNCA** são proxiados para OpenAI ou OpenRouter.
> Durante um failover, requisições desses tenants são **enfileiradas** (retry
> limitado de ~4s) ou **falham fechado** com `503` — elas jamais cruzam a
> fronteira para um provedor externo. Esta é a política RES-08 da Fase 3.
> Vast.ai recebe dados sensíveis porque hospeda o upstream **local** (o
> caminho primário), não como provedor de failover externo.

## Mapeamento tenant → classe de dados → sub-processadores

| Tenant      | App (repo sibling)                      | `data_class` | Sub-processadores que podem receber seus dados                           |
|-------------|-----------------------------------------|--------------|--------------------------------------------------------------------------|
| `telefonia` | `fallback-register-ramais-nextbilling`  | **sensitive**| **Vast.ai apenas** (upstream local). NUNCA OpenAI/OpenRouter.            |
| `cobrancas` | `cobrancas-api`                         | **sensitive**| **Vast.ai apenas** (upstream local). NUNCA OpenAI/OpenRouter.            |
| `campanhas` | `campanhas-chatifix`                    | `normal`     | Vast.ai (local) **e**, durante failover, OpenAI / OpenRouter.            |
| `voice-api` | `voice-api`                             | `normal`     | Vast.ai (local) **e**, durante failover, OpenAI / OpenRouter. (TTS roda em CPU local e não passa pelo gateway.) |

## Mechanism

A garantia "never-external" para tenants sensíveis é **imposta pelo código**,
não apenas documentada. Quando um request de um tenant `sensitive` chega
durante uma indisponibilidade do upstream local, o gateway:

1. Executa um retry limitado de 3 tentativas (~200ms / 800ms / 3s ≈ 4s)
   aguardando o circuit breaker do upstream local voltar a `CLOSED` — esse
   retry **nunca** roteia para um upstream externo.
2. Se o retry se esgota, responde `503` com o envelope
   `upstream_unavailable_for_sensitive_tenant` e `Retry-After: 30`.
3. Registra uma linha em `audit_log` com `upstream = 'blocked_sensitive'` — a
   prova de runtime de que o request **não** foi proxiado externamente.

Esse comportamento é verificado de ponta a ponta pelo smoke
`scripts/integration-smoke/smoke-sensitive-failover.py`, que induz uma falha de
upstream e afirma que o request enfileira/falha fechado, registra a decisão em
`audit_log` e nunca alcança um provedor externo. Conteúdo de tenants sensíveis
**nunca** é persistido — o gateway grava a linha de decisão em `audit_log` mas
**não** grava `audit_log_content` para requests sensíveis (D-B2).

Para o detalhamento operacional do caminho RES-08, ver
`gateway/docs/RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md` (Symptom 3) e
`gateway/docs/RUNBOOK-FAILOVER.md` (Symptom 3 — Sensitive tenant reports 503s).

## Related Docs

- `gateway/docs/LGPD-REVIEW-CHECKLIST.md` — o checklist de revisão LGPD que
  usa este documento como item de verificação, com a tabela de sign-off do
  setor jurídico da Ifix.
- `gateway/docs/RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md` — o runbook de
  integração dos tenants sensíveis; documenta o caminho RES-08 e a garantia
  never-external no nível operacional.
- `gateway/docs/RUNBOOK-FAILOVER.md` — o mecanismo de failover + circuit
  breakers que define quando (e para quem) os sub-processadores externos
  entram em cena.
