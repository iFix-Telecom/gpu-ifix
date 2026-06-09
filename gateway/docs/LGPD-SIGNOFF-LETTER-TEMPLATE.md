# Carta de Sign-off LGPD — ifix-ai-gateway

**Last updated: 2026-05-27.**

> Template versionado em git. **Não inclua dados reais aqui** — preencha uma
> cópia desta carta com os placeholders `{{...}}` substituídos pelos valores
> do tenant, imprima/assinem, e arquive o PDF em
> `.planning/legal/lgpd-signoff-{{SIGNOFF_DATE}}-{{TENANT_SLUG}}.pdf`
> (binário gitignored — ver `gateway/docs/LGPD-SIGNOFF-PROCESS.md`).

## Identificação

- **Tenant:** `{{TENANT_SLUG}}`
- **Classe de dados (`data_class`):** `{{DATA_CLASS}}` (`normal` ou `sensitive`)
- **Data da assinatura (`SIGNOFF_DATE`):** `{{SIGNOFF_DATE}}` (`YYYY-MM-DD`)
- **Encarregado de Dados (DPO) da Ifix:** `{{SIGNATORY_DPO}}`
- **Diretor jurídico:** `{{SIGNATORY_LEGAL}}`
- **Owner técnico do gateway (Plataforma):** `{{SIGNATORY_PLATFORM}}`

## Escopo

O `ifix-ai-gateway` processa requisições do tenant `{{TENANT_SLUG}}` (classe
`{{DATA_CLASS}}`) e roteia dados de prompt, áudio e embedding para o **pod GPU
primário** (caminho local) e, quando aplicável segundo a classe de dados, para
sub-processadores externos de failover. Esta carta declara os sub-processadores
envolvidos no plano de dados e o conjunto de garantias técnicas em vigor.

## Sub-processadores declarados

Os sub-processadores externos com papel formal no fluxo de dados do tenant
estão enumerados abaixo. Os detalhes técnicos atuais (shape de GPU, modelos
em uso, provedores de roteamento, SKUs) **não** são inline aqui por design,
para evitar drift entre revisões — esta carta delega esses detalhes ao
documento canônico `gateway/docs/LGPD-SUBPROCESSORS.md`, e cada assinatura
âncora-se a uma revisão específica daquele documento (ver seção seguinte).

- **Vast.ai** — provedor de GPU primária; processa payloads no pod efêmero
  alugado, sem persistência além do ciclo de vida do pod. Detalhes técnicos
  atuais (shape de GPU, allowlist de hosts, hardware) estão em
  `gateway/docs/LGPD-SUBPROCESSORS.md`.
- **OpenAI** — fallback de failover para STT e embeddings; recebe dados
  **apenas** de tenants `data_class: normal` por força do invariante RES-08.
  Modelos e SKUs atuais estão em `gateway/docs/LGPD-SUBPROCESSORS.md`.
- **OpenRouter** — fallback de failover para LLM; recebe dados **apenas** de
  tenants `data_class: normal`; bloqueio explícito para tenants `sensitive`
  por força do invariante RES-08. Provedor de roteamento e SKU atual estão em
  `gateway/docs/LGPD-SUBPROCESSORS.md`.
- **MinIO — componente de infraestrutura (sub-processador se aplicável — confirmar com jurídico)** — armazenamento S3-compatible de pesos de modelos
  open-source consumidos pelo pod GPU primário no boot; **nenhum dado de
  cliente é persistido**. Detalhes técnicos (bucket, endpoint, artefatos
  armazenados) estão em `gateway/docs/LGPD-SUBPROCESSORS.md`. **Nota
  classificatória:** o MinIO é self-hosted internamente em
  `s3.ifixtelecom.com.br` sem fornecedor externo; a classificação final
  (sub-processador formal versus componente de infraestrutura interna) é
  **decisão do jurídico da Ifix**. A inclusão aqui é divulgação completa
  (full-disclosure) do plano de dados, ainda que o componente não receba PII
  por design.

## Modelos e provedores upstream

Os modelos e provedores upstream atuais estão documentados em `gateway/docs/LGPD-SUBPROCESSORS.md` (revisão `{{SUBPROCESSORS_REVIEW_DATE}}`, commit `{{SUBPROCESSORS_REVIEW_SHA}}`).

Esta carta intencionalmente não inline slugs de modelo para evitar drift entre
revisões; cada assinatura âncora-se na revisão canônica acima, identificada
pela data da revisão e pelo hash do commit. Quando os modelos ou provedores
mudarem, uma nova revisão será anexada ao documento canônico e uma nova carta
será assinada (cadência event-driven — ver `LGPD-SIGNOFF-PROCESS.md`).

## Garantia "never-external" para tenants sensíveis

**Tenants `data_class: sensitive` (por exemplo `telefonia`, `cobrancas`)
NUNCA roteiam para sub-processadores externos de failover (tier-1).** Quando
o pod GPU primário (tier-0) está indisponível, requisições desses tenants
respondem com `HTTP 503 upstream_unavailable_for_sensitive_tenant` em vez de
serem proxiadas externamente. Esta garantia é imposta em código no
`gateway/internal/proxy/dispatcher.go` e é a materialização do invariante
RES-08 (ver também `gateway/docs/LGPD-SUBPROCESSORS.md` — seção *Mechanism*).

## Base legal e retenção

- **Base legal para o tratamento:** `{{LEGAL_BASIS}}`
- **Período de retenção:** `{{RETENTION_PERIOD}}`

Estes campos são preenchidos pelo jurídico da Ifix conforme a base legal
aplicável ao tenant `{{TENANT_SLUG}}` e à classe `{{DATA_CLASS}}`.

## Assinaturas

| Papel                                       | Nome                       | Assinatura | Data             |
|---------------------------------------------|----------------------------|------------|------------------|
| Encarregado de Dados (DPO) da Ifix          | `{{SIGNATORY_DPO}}`        | _________  | `{{SIGNOFF_DATE}}` |
| Diretor jurídico                            | `{{SIGNATORY_LEGAL}}`      | _________  | `{{SIGNOFF_DATE}}` |
| Owner técnico do gateway (Plataforma)       | `{{SIGNATORY_PLATFORM}}`   | _________  | `{{SIGNOFF_DATE}}` |

## Referências

- `gateway/docs/LGPD-SUBPROCESSORS.md` — documento canônico de sub-processadores,
  com detalhes técnicos atuais (autoridade sobre modelos e SKUs em uso).
- `gateway/docs/LGPD-REVIEW-CHECKLIST.md` — checklist de revisão LGPD com os
  controles técnicos exigidos antes da ativação de tenants sensíveis.
- `gateway/docs/LGPD-SIGNOFF-PROCESS.md` — processo de sign-off (quem assina,
  onde arquivar, cadência de revisão).
