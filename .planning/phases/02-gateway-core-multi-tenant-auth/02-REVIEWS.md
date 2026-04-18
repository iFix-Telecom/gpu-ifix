---
phase: 2
reviewers: [codex]
skipped: [claude]
skip_reason: "claude foi skipada porque este review está rodando dentro de uma sessão Claude Code (CLAUDE_CODE_ENTRYPOINT=cli); manter independência"
missing_clis: [gemini, coderabbit, opencode, qwen, cursor]
reviewed_at: 2026-04-18T19:26:08Z
plans_reviewed:
  - 02-01-PLAN.md
  - 02-02-PLAN.md
  - 02-03-PLAN.md
  - 02-04-PLAN.md
  - 02-05-PLAN.md
  - 02-06-PLAN.md
  - 02-07-PLAN.md
  - 02-08-PLAN.md
  - 02-09-PLAN.md
model: gpt-5.4 (codex default)
---

# Cross-AI Plan Review — Phase 2: Gateway Core + Multi-tenant Auth

> **Aviso:** apenas uma CLI externa (Codex) estava disponível no ambiente. Reviews de outros modelos (Gemini, OpenCode, Qwen, Cursor, CodeRabbit) não foram coletadas. Para um adversarial review mais robusto, instale pelo menos mais uma CLI e rode novamente.

## Codex Review

# Revisão dos Planos da Fase 2

## Plano 02-01

### Resumo
O plano 02-01 é forte como fundação técnica: define bootstrap, middleware base, config fail-fast, observabilidade mínima e contratos reutilizáveis para o restante da fase. Está muito claro, com interfaces e critérios bem especificados. O principal problema é que ele tenta carregar dependências demais cedo demais e mistura scaffold puro com escolhas que já congelam comportamento operacional importante.

### Strengths
- Define bem os contratos-base que os outros planos reutilizam (`config.Load`, `httpx.WriteOpenAIError`, `RequestID`, `obs.Init`).
- Fecha cedo partes críticas de segurança: redaction em `slog` e Sentry, `X-Request-ID` autoritativo, envelope OpenAI consistente.
- Os testes propostos são objetivos e verificáveis.
- O middleware ordering está explicitado e coerente.
- Dá boa base para GW-01, GW-05, GW-08 e TEN-08.

### Concerns
- [MEDIUM] `go.mod` já adiciona dependências de fases futuras (`pgx`, `go-redis`, `goose`, `argon2id`, `testcontainers`) antes de serem usadas. Isso aumenta superfície de churn logo no primeiro plano.
- [MEDIUM] O `main.go` inicial fala em “wiring stub” de DB/Redis/auth, mas o escopo declarado diz “no DB or auth wiring yet”; há pequena tensão entre scaffold e bootstrap semi-real.
- [LOW] `WriteTimeout: 0` já no primeiro plano é correto para SSE, mas congela uma decisão operacional antes dos proxies existirem.
- [LOW] O helper `writeJSON` local no `main.go` é aceitável, mas cria duplicação leve com `httpx/envelope.go`.

### Suggestions
- Reduzir dependências no 02-01 ao mínimo necessário e mover `argon2id`, `goose`, `testcontainers` para os planos onde passam a ser usados.
- Tornar explícito que 02-01 não inicializa DB/Redis ainda; deixar esse wiring só no 02-03/02-05.
- Consolidar helpers HTTP em `httpx` ou justificar claramente por que `/health` usa helper local.

---

## Plano 02-02

### Resumo
O plano 02-02 é sólido no desenho de schema e prepara corretamente o terreno para auth, audit e aliases. A separação entre migrations, sqlc e pool é boa. O maior risco está no particionamento e na estratégia de queries/auth: algumas decisões são funcionais hoje, mas frágeis para evolução e carga real.

### Strengths
- Bom alinhamento com GW-10 e com D-A4, D-B1, D-B3, D-B6, D-D1, D-D4, D-D5.
- Schema de `audit_log` já antecipa Fase 3 sem refactor grande.
- `search_path` no `AfterConnect` + nomes qualificados em SQL dão boa defesa.
- `model_aliases` e seeds estão bem amarrados ao contexto.
- Migrations têm rollback e shape bem definido.

### Concerns
- [HIGH] `ListActiveKeysAll` para auth é um atalho perigoso. Funciona com poucos tenants/keys, mas institucionaliza scan completo de hashes Argon2 no hot path em caso de cache miss.
- [MEDIUM] `goose` usando bookkeeping no schema corrente via `search_path` pode surpreender tooling externo; precisa ser documentado de forma mais explícita.
- [MEDIUM] Partições seedadas como “mês atual + 2 próximos” ajudam no curto prazo, mas falta estratégia concreta para criação automática antes da Fase 9; o plano joga isso para depois.
- [LOW] `audit_log_content` sem FK é intencional por LGPD, mas perde integridade referencial; isso deveria ser chamado explicitamente como tradeoff.

### Suggestions
- Trocar `ListActiveKeysAll` por query mais seletiva. Mesmo um filtro por `key_prefix`/suffix já reduz muito custo.
- Adicionar critério explícito de verificação de criação do `goose_db_version` no schema certo.
- Documentar melhor o tradeoff de `audit_log_content` sem FK.
- Acrescentar teste de migração validando colunas e tipos essenciais, não só existência de tabelas.

---

## Plano 02-03

### Resumo
O 02-03 entrega o núcleo funcional de multi-tenant auth e bootstrap administrativo. Está bem pensado em termos de UX operacional (`gatewayctl`) e segurança básica. O problema principal é que o caminho de verificação ainda é caro e tem alguns detalhes de concorrência/logging que podem gerar comportamento ruim sob carga.

### Strengths
- Bom fechamento de TEN-01, TEN-02 e TEN-08.
- `Authorization` > `X-API-Key` está bem definido e testado.
- `GenerateAPIKey` e armazenamento hash/prefix seguem D-A1/D-A2 com boa disciplina.
- `gatewayctl` como superfície admin mínima está alinhado a D-A3.
- Curto-circuito para chave malformada reduz custo de Argon2 contra lixo óbvio.

### Concerns
- [HIGH] Verificar `ListActiveKeysAll` + Argon2 em cada cache miss é um vetor de degradação claro numa VPS 4 vCPU. Um atacante com chaves bem-formadas mas inválidas força trabalho pesado.
- [MEDIUM] O goroutine assíncrono para `TouchKeyLastUsed` por request pode virar ruído excessivo sob carga.
- [MEDIUM] O negative cache de 5s em `Verify` aparece como adição local, mas não estava claramente decidido em D-A2; é útil, porém muda comportamento operacional.
- [LOW] `safeKeyPrefix` e logging de rejeição estão ok, mas seria melhor garantir zero logging para malformed keys repetidas em alta taxa.

### Suggestions
- Reestruturar lookup para reduzir o conjunto de hashes comparados: índice por prefix visual, tenant conhecido, ou hash rápido auxiliar.
- Substituir `TouchKeyLastUsed` por debounce/coalescing ou update amostrado.
- Formalizar o negative cache como decisão explícita.
- Adicionar teste de carga simples para estimar throughput de `Verify` com cache miss.

---

## Plano 02-04

### Resumo
O 02-04 está muito bom para o objetivo central da fase: proxy OpenAI-compatible com SSE e multipart preservados. O desenho é claro e os testes são fortes. O principal cuidado é que ele simplifica alguns aspectos de `ReverseProxy` que depois serão estendidos por audit/idempotency, e há risco de acoplamento não trivial entre essas camadas.

### Strengths
- Fecha bem GW-02, GW-03, GW-04, GW-06 e parte de TEN-08.
- `FlushInterval: -1` está corretamente tratado como requisito crítico.
- Header stripping e propagação de `X-Request-ID` estão bem especificados.
- Testes de SSE e multipart são de alto valor.
- Boa separação entre `Director`, `ErrorHandler` e construtores de proxy.

### Concerns
- [MEDIUM] O plano assume que `ModifyResponse` ficará livre para o 02-05, mas isso já cria acoplamento implícito entre proxy base e audit tee.
- [MEDIUM] `FlushInterval: -1` em embeddings e áudio é inofensivo, mas desnecessário; ruído técnico.
- [LOW] A detecção SSE por `Content-Type` apenas é suficiente aqui, mas não captura edge cases de upstream malformado.
- [LOW] Não há teste explícito de preservação de status/header não-200 além do 502 synthetic.

### Suggestions
- Definir desde já um ponto de extensão explícito para `ModifyResponse`/wrappers, para evitar remendo no 02-05.
- Deixar `FlushInterval` especial só para chat, a menos que haja justificativa operacional forte.
- Adicionar um teste de passthrough de status 4xx/5xx do upstream quando a conexão existe.
- Garantir que `Host` rewrite não quebre upstreams que dependam de path base futura.

---

## Plano 02-05

### Resumo
O 02-05 é o plano mais complexo da fase e, conceitualmente, o mais arriscado. Ele resolve audit, aliases e health, mas junta três subsistemas de naturezas bem diferentes. As ideias estão boas e alinhadas com D-B2/D-B4/D-B5, porém há fragilidade real na instrumentação do audit em torno de `ReverseProxy`, especialmente para SSE e para captura correta de conteúdo sem interferir no fluxo.

### Strengths
- Fecha GW-05, GW-07, GW-08 e reforça TEN-02.
- A decisão LGPD “normal grava conteúdo / sensitive não grava” está bem preservada.
- Buffer não-bloqueante com drop + métrica é pragmático para VPS pequena.
- Resolver de aliases em memória com refresh é suficiente para Fase 2.
- Cache de `/v1/health/upstreams` em 5s é boa escolha operacional.

### Concerns
- [HIGH] A estratégia de audit middleware para capturar resposta em conjunto com `ReverseProxy` é a parte mais delicada da fase. O plano descreve duas abordagens parcialmente sobrepostas: `auditResponseWriter` e `tee` via `ModifyResponse`. Isso pode gerar comportamento divergente entre SSE e non-SSE.
- [HIGH] `populateAudioMetadata` faz parsing frágil da resposta (`fmt.Sscan` em JSON parcial). Isso é pouco robusto.
- [MEDIUM] `Writer.flush` com `CopyFrom` + inserts row-by-row para content pode alongar transação sob batches grandes.
- [MEDIUM] O resolver ignora `upstream` no mapa, apesar de a tabela ter essa coluna. Isso funciona hoje, mas desperdiça modelagem e pode virar bug quando alias repetir entre tipos.
- [LOW] `routeTemplate` e `upstreamForRoute` por string prefix são frágeis a mudanças futuras de rota.

### Suggestions
- Separar claramente audit non-SSE e SSE em caminhos distintos, com um único mecanismo autoritativo de captura por tipo.
- Não parsear JSON de Whisper com `fmt.Sscan`; usar decode parcial estruturado.
- Considerar batch insert também para `audit_log_content`, ou pelo menos agrupar melhor.
- Usar `(alias, upstream)` no resolver desde já, mesmo que hoje haja poucos aliases.
- Adicionar teste de integração mais forte especificamente para SSE + audit + disconnect.

---

## Plano 02-06

### Resumo
O 02-06 está bem alinhado com TEN-09 e tem boa semântica inspirada em Stripe. A separação entre store/hash/middleware é boa. O maior mérito é endereçar replay, conflito e route gating de forma verificável. O principal risco está no locking/concurrency e na interação com audit, que ainda depende de contrato sutil com o 02-05.

### Strengths
- Fecha TEN-09 de forma bem definida.
- `HashBody` canônico é uma escolha correta e bem testável.
- Regras de replay, conflito e stream unsupported seguem D-C3/D-C4.
- Escopo por `tenant_id` está certo e é crítico.
- Boa decisão de não cachear 5xx/502.

### Concerns
- [HIGH] Há inconsistência entre objetivo declarado “first-writer-wins com serialization via SETNX” e o middleware proposto: ele quase não usa `Acquire/WaitForEntry` de fato no fluxo descrito, então a serialização concorrente parece incompleta.
- [MEDIUM] `MaxBodySize = 1 MiB` é arbitrário e menor que o limite geral do gateway; pode surpreender requests legítimos de chat maiores.
- [MEDIUM] Replay depende do `audit.IdempotencyReplayedSetter`; isso é inteligente, mas é um acoplamento transversal delicado entre 02-05 e 02-06.
- [LOW] `cacheable(400)` é amplo demais; nem todo 400 é necessariamente determinístico no futuro.

### Suggestions
- Implementar de fato o caminho `Acquire -> execute winner -> losers WaitForEntry`, ou remover essa promessa do plano.
- Tornar o body cap configurável ou pelo menos explicitamente justificado frente ao payload real esperado.
- Restringir caching de 400 a erros deterministicamente reexecutáveis conhecidos.
- Adicionar teste explícito de concorrência real com lock, não só replay serial.

---

## Plano 02-07

### Resumo
O 02-07 é muito valioso e melhora bastante a credibilidade da fase. Ele cobre os acoplamentos que os planos anteriores introduzem. Há boa noção de reutilização de containers e de build tags. O risco aqui é mais de execução/manutenção: o pacote de integração está ambicioso e tem chances de ficar frágil se a base mudar.

### Strengths
- Excelente cobertura de integração para pontos realmente arriscados: migrations, audit, idempotency, gateway subprocess.
- `//go:build integration` está correto e protege o fluxo default.
- Boa escolha de reutilizar Postgres/Redis via `TestMain`.
- O teste 04b de `idempotency_replayed` é especialmente valioso.
- Ajuda a verificar de fato os Success Criteria 1-4.

### Concerns
- [MEDIUM] Os testes de integração estão bastante acoplados a detalhes internos e seeds fixos; isso pode gerar manutenção pesada.
- [MEDIUM] `freshSchema` com truncates manuais pode não limpar tudo em cenários de partições/DDL mais complexos.
- [LOW] Alguns helper snippets no plano ainda estão incompletos/placeholder-ish (`imports placeholder`, etc.), o que sugere risco de fricção na implementação real.
- [LOW] O subprocess E2E do gateway pode ficar flakey por timing/boot.

### Suggestions
- Padronizar helpers para seed, issue key e wait loops para reduzir duplicação.
- Validar explicitamente limpeza de partições/rows por teste.
- Priorizar poucos integration tests robustos em vez de muitos medianos, se houver pressão de tempo.
- Adicionar timeouts e logs de diagnóstico mais explícitos para subprocess failures.

---

## Plano 02-08

### Resumo
O 02-08 é bom e necessário para GW-09. Ele segue o padrão Ifix e cobre CI, build, publish e deploy. O maior problema é algum overreach operacional e algumas decisões que podem ser boas para dev mas perigosas como default de produção, principalmente `AI_GATEWAY_MIGRATE_ON_BOOT=true`.

### Strengths
- Fecha GW-09 de maneira prática e alinhada ao ecossistema já usado.
- Workflow CI está bem detalhado e inclui unit + integration + sqlc drift check.
- Dockerfile distroless é apropriado para um gateway Go.
- README e compose documentam bem secrets/env vars.
- Build arg para `BuildVersion` está bem amarrado.

### Concerns
- [HIGH] `AI_GATEWAY_MIGRATE_ON_BOOT=true` como default no compose/prod é arriscado. Em produção, migration automática no boot pode complicar rollback e debugging.
- [MEDIUM] Workflow CI está bastante pesado; para cada push em `develop`, unit + integration + build + deploy pode ficar lento.
- [MEDIUM] `.dockerignore` exclui `docs/` e `*.md`; tudo bem para runtime, mas exige cuidado se algum build futuro depender desses assets.
- [LOW] O job `compute-tags` depende só de `test`, não de `integration-test`; não é grave, mas a lógica fica um pouco assimétrica.

### Suggestions
- Mudar recomendação operacional: `AI_GATEWAY_MIGRATE_ON_BOOT=true` só no primeiro deploy/dev; em prod, preferir `gatewayctl migrate up` explícito ou toggle controlado.
- Considerar permitir deploy dev só após unit tests em alguns branches, deixando integration como required em main/tag.
- Adicionar smoke test pós-build da imagem (`/gateway --self-check`) dentro do workflow.
- Explicitar rollback path quando migration e deploy falham em conjunto.

---

## Plano 02-09

### Resumo
O 02-09 é tecnicamente bom, mas é o plano com maior cheiro de escopo lateral dentro da Fase 2. Ele fecha a história de retenção de D-B3, porém extrapola o núcleo “Gateway Core + Multi-tenant Auth”. A solução é detalhada e cuidadosa, mas adiciona MinIO, Parquet e retenção operacional que não são necessários para cumprir os Success Criteria da fase.

### Strengths
- Muito bom cuidado com safety invariant upload-before-drop.
- `--dry-run` e gate por retenção são escolhas corretas.
- Boa extensão de config sem tornar MinIO obrigatório para o gateway.
- A modelagem de Parquet e layout em bucket estão claras.
- A CLI está bem pensada para operação manual.

### Concerns
- [HIGH] Forte sinal de scope creep. Export para MinIO + Parquet não é necessário para atingir os objetivos centrais de GW-01..GW-09/TEN-01/02/08/09.
- [MEDIUM] `DROP TABLE` montado por concatenação com nome de partição calculado é aceitável nesse contexto, mas merece sanitização/validação explícita do mês.
- [MEDIUM] Introduz duas dependências novas relevantes (`minio-go`, `parquet-go`) já no fim da fase, aumentando custo e superfície de falha.
- [LOW] Testabilidade real fica incompleta sem integration test com MinIO de verdade.

### Suggestions
- Reclassificar 02-09 como opcional ou mover para fase posterior de observabilidade/retention.
- Se ficar na Fase 2, reduzir escopo: export CSV/JSONL primeiro, Parquet depois.
- Validar partição-alvo contra regex/mês parseado e nomes esperados antes de executar DROP.
- Adicionar um integration test com MinIO container antes de considerar isso “done”.

---

# Revisão da Fase

## Dependency ordering
A ordem geral é coerente: `02-01` base, `02-02` schema, `02-03` auth, `02-04` proxies, `02-05` audit/aliases/health, `02-06` idempotency, `02-07` integração, `02-08` deploy. Isso faz sentido.

Os acoplamentos ocultos mais importantes:
- `02-05` depende mais do `02-04` do que o texto sugere, por causa de `ReverseProxy.ModifyResponse`, SSE tee e lifecycle.
- `02-06` depende de um contrato sutil com `02-05` (`audit.IdempotencyReplayedSetter`), não só do auth/proxy.
- `02-08` depende implicitamente de `sqlc generate` estar estável e de `02-07` não ficar flakey.
- `02-09` é praticamente independente do core da fase e poderia sair do caminho crítico.

Minha leitura: a ordem está boa, mas `02-05` e `02-06` são os pontos de maior coupling real.

## Scope creep / over-engineering
Há dois focos claros de over-engineering:
- `02-05` concentra audit async, SSE tee, model resolver e upstream health. Dá para justificar, mas é um plano pesado demais.
- `02-09` é o maior caso de scope creep. Parquet + MinIO + retenção de partição é útil, mas não é necessário para cumprir os Success Criteria 1-5 da Fase 2.

Também há um pouco de excesso em:
- Admin CLI já muito completa na 02-03.
- Pipeline CI/CD bem robusta na 02-08 para uma fase ainda validando produto.

## Missing edge cases
Pontos que ainda merecem atenção:
- `sensitive` + idempotency replay: o plano reconhece Redis com body por 24h (D-C5), mas isso é um risco LGPD explícito que deveria ser melhor destacado.
- SSE mid-stream com client disconnect: o 02-05 fala disso, mas esse é o lugar mais provável de bug real.
- Tool calls em streaming: há pass-through no 02-04, mas pouca validação de tool calls em SSE incremental.
- Redis outage: auth degrada para DB, mas idempotency sem Redis vira bypass silencioso; isso precisa ser decisão explícita.
- Pool exhaustion: `pgxpool MaxConns: 10` pode ser apertado com audit flush + auth + health + migrations.
- Partições futuras: schema cria algumas partições, mas a operação contínua depende de jobs futuros.
- Argon2id DoS: mitigado parcialmente por malformed reject e cache, mas o scan completo continua sendo problema.
- UUIDv7 collision risk é irrelevante na prática; não vejo issue real aqui.

## Security
No geral, boa disciplina:
- API keys em logs/Sentry estão bem cuidadas desde 02-01/02-03.
- Header redaction está bem pensada.
- `Authorization` stripping do gateway para o pod está correto.
- Timing attacks foram considerados, embora o real gargalo seja mais custo do Argon2 do que timing.

Riscos principais:
- `ListActiveKeysAll` + Argon2 scan em 02-03 é o maior risco prático de segurança/performance.
- `sensitive` bodies em Redis por idempotency (D-C5) é aceitável por design, mas é exposição deliberada.
- `DROP TABLE` em 02-09 precisa validação rigorosa do alvo.
- SSRF via model aliases não aparece como problema imediato porque alias resolve para string de model, não URL; isso está ok.
- Header-size limits e body limits foram tratados razoavelmente bem em 02-01.

## Performance on 4 vCPU VPS
O desenho funciona, mas sem muita folga:
- Argon2id com `64 MiB / 3 / 2` é defensável, porém caro. Com scan amplo de chaves, vira gargalo real.
- `pgxpool MaxConns: 10` parece suficiente no início, mas audit flusher + auth + health + request path simultâneo pode pressionar.
- SSE com flush por chunk tem custo, mas é necessário. O risco maior é memória/lifecycle, não CPU.
- Audit buffer `1000` / batch `500` é uma escolha razoável para essa VPS.
- Async audit drop-on-full é pragmaticamente correto.
- Health cache 5s ajuda.
- Resolver refresh 60s é trivial em custo.

Resumo: o gargalo provável da fase não é proxying; é auth miss path e escrita de audit sob burst.

## Testability
A fase está bem servida de testes.
- Unit tests: fortes, especialmente 02-01, 02-04, 02-06.
- Integration: 02-07 cobre os acoplamentos certos.
- E2E: o subprocess test do gateway é um bom fechamento.

O ponto de atenção é manutenção:
- Muita cobertura, mas alguns testes podem ficar frágeis.
- A promessa “testcontainers-go” está consistente, sim.
- Os Success Criteria são majoritariamente verificáveis pelos planos, com exceção parcial de deploy real e de 02-09 sem MinIO integration real.

## Goal alignment
No conjunto, os planos entregam os Success Criteria da fase:

1. OpenAI SDK apontando para gateway com completions/stream/STT/embed:
   - Sim, por 02-03 + 02-04 + 02-05 + 02-07.
2. 401 em envelope OpenAI; wiring para 429:
   - Sim, 02-01 + 02-03. 429 fica preparado via helper.
3. Trace completo com `X-Request-ID`, logs e `audit_log`:
   - Sim, 02-01 + 02-05 + 02-07.
4. `data_class` disponível no contexto:
   - Sim, 02-03.
5. Deploy padrão Ifix + aliases:
   - Sim, 02-05 + 02-08.

O único ponto fora do core é 02-09, que não é necessário para esses critérios.

## Overall Risk Level
**MEDIUM**

Justificativa:
- A arquitetura geral está boa e os planos são detalhados o suficiente para execução.
- Os maiores riscos estão concentrados e conhecidos:
  - `02-03` auth miss path com `ListActiveKeysAll` + Argon2.
  - `02-05` audit em torno de SSE/ReverseProxy.
  - `02-06` concorrência/idempotency serialization incompleta.
  - `02-09` scope creep.
- Se esses quatro pontos forem ajustados, o risco cai para baixo-médio.

# Veredito
A fase está **bem planejada e majoritariamente executável**, com bom rigor técnico e boa cobertura de testes. Eu aprovaria a direção geral, mas **não aprovaria sem revisão** dos seguintes pontos antes de executar:

- Redesenhar o hot path de auth do `02-03`.
- Simplificar e endurecer a integração audit/proxy do `02-05`.
- Fechar de verdade a serialização concorrente no `02-06`.
- Tirar `02-09` do caminho crítico ou rebaixá-lo para escopo opcional/posterior.
tokens used
154,076
# Revisão dos Planos da Fase 2

## Plano 02-01

### Resumo
O plano 02-01 é forte como fundação técnica: define bootstrap, middleware base, config fail-fast, observabilidade mínima e contratos reutilizáveis para o restante da fase. Está muito claro, com interfaces e critérios bem especificados. O principal problema é que ele tenta carregar dependências demais cedo demais e mistura scaffold puro com escolhas que já congelam comportamento operacional importante.

### Strengths
- Define bem os contratos-base que os outros planos reutilizam (`config.Load`, `httpx.WriteOpenAIError`, `RequestID`, `obs.Init`).
- Fecha cedo partes críticas de segurança: redaction em `slog` e Sentry, `X-Request-ID` autoritativo, envelope OpenAI consistente.
- Os testes propostos são objetivos e verificáveis.
- O middleware ordering está explicitado e coerente.
- Dá boa base para GW-01, GW-05, GW-08 e TEN-08.

### Concerns
- [MEDIUM] `go.mod` já adiciona dependências de fases futuras (`pgx`, `go-redis`, `goose`, `argon2id`, `testcontainers`) antes de serem usadas. Isso aumenta superfície de churn logo no primeiro plano.
- [MEDIUM] O `main.go` inicial fala em “wiring stub” de DB/Redis/auth, mas o escopo declarado diz “no DB or auth wiring yet”; há pequena tensão entre scaffold e bootstrap semi-real.
- [LOW] `WriteTimeout: 0` já no primeiro plano é correto para SSE, mas congela uma decisão operacional antes dos proxies existirem.
- [LOW] O helper `writeJSON` local no `main.go` é aceitável, mas cria duplicação leve com `httpx/envelope.go`.

### Suggestions
- Reduzir dependências no 02-01 ao mínimo necessário e mover `argon2id`, `goose`, `testcontainers` para os planos onde passam a ser usados.
- Tornar explícito que 02-01 não inicializa DB/Redis ainda; deixar esse wiring só no 02-03/02-05.
- Consolidar helpers HTTP em `httpx` ou justificar claramente por que `/health` usa helper local.

---

## Plano 02-02

### Resumo
O plano 02-02 é sólido no desenho de schema e prepara corretamente o terreno para auth, audit e aliases. A separação entre migrations, sqlc e pool é boa. O maior risco está no particionamento e na estratégia de queries/auth: algumas decisões são funcionais hoje, mas frágeis para evolução e carga real.

### Strengths
- Bom alinhamento com GW-10 e com D-A4, D-B1, D-B3, D-B6, D-D1, D-D4, D-D5.
- Schema de `audit_log` já antecipa Fase 3 sem refactor grande.
- `search_path` no `AfterConnect` + nomes qualificados em SQL dão boa defesa.
- `model_aliases` e seeds estão bem amarrados ao contexto.
- Migrations têm rollback e shape bem definido.

### Concerns
- [HIGH] `ListActiveKeysAll` para auth é um atalho perigoso. Funciona com poucos tenants/keys, mas institucionaliza scan completo de hashes Argon2 no hot path em caso de cache miss.
- [MEDIUM] `goose` usando bookkeeping no schema corrente via `search_path` pode surpreender tooling externo; precisa ser documentado de forma mais explícita.
- [MEDIUM] Partições seedadas como “mês atual + 2 próximos” ajudam no curto prazo, mas falta estratégia concreta para criação automática antes da Fase 9; o plano joga isso para depois.
- [LOW] `audit_log_content` sem FK é intencional por LGPD, mas perde integridade referencial; isso deveria ser chamado explicitamente como tradeoff.

### Suggestions
- Trocar `ListActiveKeysAll` por query mais seletiva. Mesmo um filtro por `key_prefix`/suffix já reduz muito custo.
- Adicionar critério explícito de verificação de criação do `goose_db_version` no schema certo.
- Documentar melhor o tradeoff de `audit_log_content` sem FK.
- Acrescentar teste de migração validando colunas e tipos essenciais, não só existência de tabelas.

---

## Plano 02-03

### Resumo
O 02-03 entrega o núcleo funcional de multi-tenant auth e bootstrap administrativo. Está bem pensado em termos de UX operacional (`gatewayctl`) e segurança básica. O problema principal é que o caminho de verificação ainda é caro e tem alguns detalhes de concorrência/logging que podem gerar comportamento ruim sob carga.

### Strengths
- Bom fechamento de TEN-01, TEN-02 e TEN-08.
- `Authorization` > `X-API-Key` está bem definido e testado.
- `GenerateAPIKey` e armazenamento hash/prefix seguem D-A1/D-A2 com boa disciplina.
- `gatewayctl` como superfície admin mínima está alinhado a D-A3.
- Curto-circuito para chave malformada reduz custo de Argon2 contra lixo óbvio.

### Concerns
- [HIGH] Verificar `ListActiveKeysAll` + Argon2 em cada cache miss é um vetor de degradação claro numa VPS 4 vCPU. Um atacante com chaves bem-formadas mas inválidas força trabalho pesado.
- [MEDIUM] O goroutine assíncrono para `TouchKeyLastUsed` por request pode virar ruído excessivo sob carga.
- [MEDIUM] O negative cache de 5s em `Verify` aparece como adição local, mas não estava claramente decidido em D-A2; é útil, porém muda comportamento operacional.
- [LOW] `safeKeyPrefix` e logging de rejeição estão ok, mas seria melhor garantir zero logging para malformed keys repetidas em alta taxa.

### Suggestions
- Reestruturar lookup para reduzir o conjunto de hashes comparados: índice por prefix visual, tenant conhecido, ou hash rápido auxiliar.
- Substituir `TouchKeyLastUsed` por debounce/coalescing ou update amostrado.
- Formalizar o negative cache como decisão explícita.
- Adicionar teste de carga simples para estimar throughput de `Verify` com cache miss.

---

## Plano 02-04

### Resumo
O 02-04 está muito bom para o objetivo central da fase: proxy OpenAI-compatible com SSE e multipart preservados. O desenho é claro e os testes são fortes. O principal cuidado é que ele simplifica alguns aspectos de `ReverseProxy` que depois serão estendidos por audit/idempotency, e há risco de acoplamento não trivial entre essas camadas.

### Strengths
- Fecha bem GW-02, GW-03, GW-04, GW-06 e parte de TEN-08.
- `FlushInterval: -1` está corretamente tratado como requisito crítico.
- Header stripping e propagação de `X-Request-ID` estão bem especificados.
- Testes de SSE e multipart são de alto valor.
- Boa separação entre `Director`, `ErrorHandler` e construtores de proxy.

### Concerns
- [MEDIUM] O plano assume que `ModifyResponse` ficará livre para o 02-05, mas isso já cria acoplamento implícito entre proxy base e audit tee.
- [MEDIUM] `FlushInterval: -1` em embeddings e áudio é inofensivo, mas desnecessário; ruído técnico.
- [LOW] A detecção SSE por `Content-Type` apenas é suficiente aqui, mas não captura edge cases de upstream malformado.
- [LOW] Não há teste explícito de preservação de status/header não-200 além do 502 synthetic.

### Suggestions
- Definir desde já um ponto de extensão explícito para `ModifyResponse`/wrappers, para evitar remendo no 02-05.
- Deixar `FlushInterval` especial só para chat, a menos que haja justificativa operacional forte.
- Adicionar um teste de passthrough de status 4xx/5xx do upstream quando a conexão existe.
- Garantir que `Host` rewrite não quebre upstreams que dependam de path base futura.

---

## Plano 02-05

### Resumo
O 02-05 é o plano mais complexo da fase e, conceitualmente, o mais arriscado. Ele resolve audit, aliases e health, mas junta três subsistemas de naturezas bem diferentes. As ideias estão boas e alinhadas com D-B2/D-B4/D-B5, porém há fragilidade real na instrumentação do audit em torno de `ReverseProxy`, especialmente para SSE e para captura correta de conteúdo sem interferir no fluxo.

### Strengths
- Fecha GW-05, GW-07, GW-08 e reforça TEN-02.
- A decisão LGPD “normal grava conteúdo / sensitive não grava” está bem preservada.
- Buffer não-bloqueante com drop + métrica é pragmático para VPS pequena.
- Resolver de aliases em memória com refresh é suficiente para Fase 2.
- Cache de `/v1/health/upstreams` em 5s é boa escolha operacional.

### Concerns
- [HIGH] A estratégia de audit middleware para capturar resposta em conjunto com `ReverseProxy` é a parte mais delicada da fase. O plano descreve duas abordagens parcialmente sobrepostas: `auditResponseWriter` e `tee` via `ModifyResponse`. Isso pode gerar comportamento divergente entre SSE e non-SSE.
- [HIGH] `populateAudioMetadata` faz parsing frágil da resposta (`fmt.Sscan` em JSON parcial). Isso é pouco robusto.
- [MEDIUM] `Writer.flush` com `CopyFrom` + inserts row-by-row para content pode alongar transação sob batches grandes.
- [MEDIUM] O resolver ignora `upstream` no mapa, apesar de a tabela ter essa coluna. Isso funciona hoje, mas desperdiça modelagem e pode virar bug quando alias repetir entre tipos.
- [LOW] `routeTemplate` e `upstreamForRoute` por string prefix são frágeis a mudanças futuras de rota.

### Suggestions
- Separar claramente audit non-SSE e SSE em caminhos distintos, com um único mecanismo autoritativo de captura por tipo.
- Não parsear JSON de Whisper com `fmt.Sscan`; usar decode parcial estruturado.
- Considerar batch insert também para `audit_log_content`, ou pelo menos agrupar melhor.
- Usar `(alias, upstream)` no resolver desde já, mesmo que hoje haja poucos aliases.
- Adicionar teste de integração mais forte especificamente para SSE + audit + disconnect.

---

## Plano 02-06

### Resumo
O 02-06 está bem alinhado com TEN-09 e tem boa semântica inspirada em Stripe. A separação entre store/hash/middleware é boa. O maior mérito é endereçar replay, conflito e route gating de forma verificável. O principal risco está no locking/concurrency e na interação com audit, que ainda depende de contrato sutil com o 02-05.

### Strengths
- Fecha TEN-09 de forma bem definida.
- `HashBody` canônico é uma escolha correta e bem testável.
- Regras de replay, conflito e stream unsupported seguem D-C3/D-C4.
- Escopo por `tenant_id` está certo e é crítico.
- Boa decisão de não cachear 5xx/502.

### Concerns
- [HIGH] Há inconsistência entre objetivo declarado “first-writer-wins com serialization via SETNX” e o middleware proposto: ele quase não usa `Acquire/WaitForEntry` de fato no fluxo descrito, então a serialização concorrente parece incompleta.
- [MEDIUM] `MaxBodySize = 1 MiB` é arbitrário e menor que o limite geral do gateway; pode surpreender requests legítimos de chat maiores.
- [MEDIUM] Replay depende do `audit.IdempotencyReplayedSetter`; isso é inteligente, mas é um acoplamento transversal delicado entre 02-05 e 02-06.
- [LOW] `cacheable(400)` é amplo demais; nem todo 400 é necessariamente determinístico no futuro.

### Suggestions
- Implementar de fato o caminho `Acquire -> execute winner -> losers WaitForEntry`, ou remover essa promessa do plano.
- Tornar o body cap configurável ou pelo menos explicitamente justificado frente ao payload real esperado.
- Restringir caching de 400 a erros deterministicamente reexecutáveis conhecidos.
- Adicionar teste explícito de concorrência real com lock, não só replay serial.

---

## Plano 02-07

### Resumo
O 02-07 é muito valioso e melhora bastante a credibilidade da fase. Ele cobre os acoplamentos que os planos anteriores introduzem. Há boa noção de reutilização de containers e de build tags. O risco aqui é mais de execução/manutenção: o pacote de integração está ambicioso e tem chances de ficar frágil se a base mudar.

### Strengths
- Excelente cobertura de integração para pontos realmente arriscados: migrations, audit, idempotency, gateway subprocess.
- `//go:build integration` está correto e protege o fluxo default.
- Boa escolha de reutilizar Postgres/Redis via `TestMain`.
- O teste 04b de `idempotency_replayed` é especialmente valioso.
- Ajuda a verificar de fato os Success Criteria 1-4.

### Concerns
- [MEDIUM] Os testes de integração estão bastante acoplados a detalhes internos e seeds fixos; isso pode gerar manutenção pesada.
- [MEDIUM] `freshSchema` com truncates manuais pode não limpar tudo em cenários de partições/DDL mais complexos.
- [LOW] Alguns helper snippets no plano ainda estão incompletos/placeholder-ish (`imports placeholder`, etc.), o que sugere risco de fricção na implementação real.
- [LOW] O subprocess E2E do gateway pode ficar flakey por timing/boot.

### Suggestions
- Padronizar helpers para seed, issue key e wait loops para reduzir duplicação.
- Validar explicitamente limpeza de partições/rows por teste.
- Priorizar poucos integration tests robustos em vez de muitos medianos, se houver pressão de tempo.
- Adicionar timeouts e logs de diagnóstico mais explícitos para subprocess failures.

---

## Plano 02-08

### Resumo
O 02-08 é bom e necessário para GW-09. Ele segue o padrão Ifix e cobre CI, build, publish e deploy. O maior problema é algum overreach operacional e algumas decisões que podem ser boas para dev mas perigosas como default de produção, principalmente `AI_GATEWAY_MIGRATE_ON_BOOT=true`.

### Strengths
- Fecha GW-09 de maneira prática e alinhada ao ecossistema já usado.
- Workflow CI está bem detalhado e inclui unit + integration + sqlc drift check.
- Dockerfile distroless é apropriado para um gateway Go.
- README e compose documentam bem secrets/env vars.
- Build arg para `BuildVersion` está bem amarrado.

### Concerns
- [HIGH] `AI_GATEWAY_MIGRATE_ON_BOOT=true` como default no compose/prod é arriscado. Em produção, migration automática no boot pode complicar rollback e debugging.
- [MEDIUM] Workflow CI está bastante pesado; para cada push em `develop`, unit + integration + build + deploy pode ficar lento.
- [MEDIUM] `.dockerignore` exclui `docs/` e `*.md`; tudo bem para runtime, mas exige cuidado se algum build futuro depender desses assets.
- [LOW] O job `compute-tags` depende só de `test`, não de `integration-test`; não é grave, mas a lógica fica um pouco assimétrica.

### Suggestions
- Mudar recomendação operacional: `AI_GATEWAY_MIGRATE_ON_BOOT=true` só no primeiro deploy/dev; em prod, preferir `gatewayctl migrate up` explícito ou toggle controlado.
- Considerar permitir deploy dev só após unit tests em alguns branches, deixando integration como required em main/tag.
- Adicionar smoke test pós-build da imagem (`/gateway --self-check`) dentro do workflow.
- Explicitar rollback path quando migration e deploy falham em conjunto.

---

## Plano 02-09

### Resumo
O 02-09 é tecnicamente bom, mas é o plano com maior cheiro de escopo lateral dentro da Fase 2. Ele fecha a história de retenção de D-B3, porém extrapola o núcleo “Gateway Core + Multi-tenant Auth”. A solução é detalhada e cuidadosa, mas adiciona MinIO, Parquet e retenção operacional que não são necessários para cumprir os Success Criteria da fase.

### Strengths
- Muito bom cuidado com safety invariant upload-before-drop.
- `--dry-run` e gate por retenção são escolhas corretas.
- Boa extensão de config sem tornar MinIO obrigatório para o gateway.
- A modelagem de Parquet e layout em bucket estão claras.
- A CLI está bem pensada para operação manual.

### Concerns
- [HIGH] Forte sinal de scope creep. Export para MinIO + Parquet não é necessário para atingir os objetivos centrais de GW-01..GW-09/TEN-01/02/08/09.
- [MEDIUM] `DROP TABLE` montado por concatenação com nome de partição calculado é aceitável nesse contexto, mas merece sanitização/validação explícita do mês.
- [MEDIUM] Introduz duas dependências novas relevantes (`minio-go`, `parquet-go`) já no fim da fase, aumentando custo e superfície de falha.
- [LOW] Testabilidade real fica incompleta sem integration test com MinIO de verdade.

### Suggestions
- Reclassificar 02-09 como opcional ou mover para fase posterior de observabilidade/retention.
- Se ficar na Fase 2, reduzir escopo: export CSV/JSONL primeiro, Parquet depois.
- Validar partição-alvo contra regex/mês parseado e nomes esperados antes de executar DROP.
- Adicionar um integration test com MinIO container antes de considerar isso “done”.

---

# Revisão da Fase

## Dependency ordering
A ordem geral é coerente: `02-01` base, `02-02` schema, `02-03` auth, `02-04` proxies, `02-05` audit/aliases/health, `02-06` idempotency, `02-07` integração, `02-08` deploy. Isso faz sentido.

Os acoplamentos ocultos mais importantes:
- `02-05` depende mais do `02-04` do que o texto sugere, por causa de `ReverseProxy.ModifyResponse`, SSE tee e lifecycle.
- `02-06` depende de um contrato sutil com `02-05` (`audit.IdempotencyReplayedSetter`), não só do auth/proxy.
- `02-08` depende implicitamente de `sqlc generate` estar estável e de `02-07` não ficar flakey.
- `02-09` é praticamente independente do core da fase e poderia sair do caminho crítico.

Minha leitura: a ordem está boa, mas `02-05` e `02-06` são os pontos de maior coupling real.

## Scope creep / over-engineering
Há dois focos claros de over-engineering:
- `02-05` concentra audit async, SSE tee, model resolver e upstream health. Dá para justificar, mas é um plano pesado demais.
- `02-09` é o maior caso de scope creep. Parquet + MinIO + retenção de partição é útil, mas não é necessário para cumprir os Success Criteria 1-5 da Fase 2.

Também há um pouco de excesso em:
- Admin CLI já muito completa na 02-03.
- Pipeline CI/CD bem robusta na 02-08 para uma fase ainda validando produto.

## Missing edge cases
Pontos que ainda merecem atenção:
- `sensitive` + idempotency replay: o plano reconhece Redis com body por 24h (D-C5), mas isso é um risco LGPD explícito que deveria ser melhor destacado.
- SSE mid-stream com client disconnect: o 02-05 fala disso, mas esse é o lugar mais provável de bug real.
- Tool calls em streaming: há pass-through no 02-04, mas pouca validação de tool calls em SSE incremental.
- Redis outage: auth degrada para DB, mas idempotency sem Redis vira bypass silencioso; isso precisa ser decisão explícita.
- Pool exhaustion: `pgxpool MaxConns: 10` pode ser apertado com audit flush + auth + health + migrations.
- Partições futuras: schema cria algumas partições, mas a operação contínua depende de jobs futuros.
- Argon2id DoS: mitigado parcialmente por malformed reject e cache, mas o scan completo continua sendo problema.
- UUIDv7 collision risk é irrelevante na prática; não vejo issue real aqui.

## Security
No geral, boa disciplina:
- API keys em logs/Sentry estão bem cuidadas desde 02-01/02-03.
- Header redaction está bem pensada.
- `Authorization` stripping do gateway para o pod está correto.
- Timing attacks foram considerados, embora o real gargalo seja mais custo do Argon2 do que timing.

Riscos principais:
- `ListActiveKeysAll` + Argon2 scan em 02-03 é o maior risco prático de segurança/performance.
- `sensitive` bodies em Redis por idempotency (D-C5) é aceitável por design, mas é exposição deliberada.
- `DROP TABLE` em 02-09 precisa validação rigorosa do alvo.
- SSRF via model aliases não aparece como problema imediato porque alias resolve para string de model, não URL; isso está ok.
- Header-size limits e body limits foram tratados razoavelmente bem em 02-01.

## Performance on 4 vCPU VPS
O desenho funciona, mas sem muita folga:
- Argon2id com `64 MiB / 3 / 2` é defensável, porém caro. Com scan amplo de chaves, vira gargalo real.
- `pgxpool MaxConns: 10` parece suficiente no início, mas audit flusher + auth + health + request path simultâneo pode pressionar.
- SSE com flush por chunk tem custo, mas é necessário. O risco maior é memória/lifecycle, não CPU.
- Audit buffer `1000` / batch `500` é uma escolha razoável para essa VPS.
- Async audit drop-on-full é pragmaticamente correto.
- Health cache 5s ajuda.
- Resolver refresh 60s é trivial em custo.

Resumo: o gargalo provável da fase não é proxying; é auth miss path e escrita de audit sob burst.

## Testability
A fase está bem servida de testes.
- Unit tests: fortes, especialmente 02-01, 02-04, 02-06.
- Integration: 02-07 cobre os acoplamentos certos.
- E2E: o subprocess test do gateway é um bom fechamento.

O ponto de atenção é manutenção:
- Muita cobertura, mas alguns testes podem ficar frágeis.
- A promessa “testcontainers-go” está consistente, sim.
- Os Success Criteria são majoritariamente verificáveis pelos planos, com exceção parcial de deploy real e de 02-09 sem MinIO integration real.

## Goal alignment
No conjunto, os planos entregam os Success Criteria da fase:

1. OpenAI SDK apontando para gateway com completions/stream/STT/embed:
   - Sim, por 02-03 + 02-04 + 02-05 + 02-07.
2. 401 em envelope OpenAI; wiring para 429:
   - Sim, 02-01 + 02-03. 429 fica preparado via helper.
3. Trace completo com `X-Request-ID`, logs e `audit_log`:
   - Sim, 02-01 + 02-05 + 02-07.
4. `data_class` disponível no contexto:
   - Sim, 02-03.
5. Deploy padrão Ifix + aliases:
   - Sim, 02-05 + 02-08.

O único ponto fora do core é 02-09, que não é necessário para esses critérios.

## Overall Risk Level
**MEDIUM**

Justificativa:
- A arquitetura geral está boa e os planos são detalhados o suficiente para execução.
- Os maiores riscos estão concentrados e conhecidos:
  - `02-03` auth miss path com `ListActiveKeysAll` + Argon2.
  - `02-05` audit em torno de SSE/ReverseProxy.
  - `02-06` concorrência/idempotency serialization incompleta.
  - `02-09` scope creep.
- Se esses quatro pontos forem ajustados, o risco cai para baixo-médio.

# Veredito
A fase está **bem planejada e majoritariamente executável**, com bom rigor técnico e boa cobertura de testes. Eu aprovaria a direção geral, mas **não aprovaria sem revisão** dos seguintes pontos antes de executar:

- Redesenhar o hot path de auth do `02-03`.
- Simplificar e endurecer a integração audit/proxy do `02-05`.
- Fechar de verdade a serialização concorrente no `02-06`.
- Tirar `02-09` do caminho crítico ou rebaixá-lo para escopo opcional/posterior.

---

## Consensus Summary

> Só um revisor respondeu. Os pontos abaixo refletem apenas a opinião do Codex — não há triangulação entre modelos.

### Pontos fortes apontados
- Boa fundação técnica no 02-01 (bootstrap, middleware, redaction em slog/Sentry, envelope OpenAI consistente).
- Schema e migrações (02-02) alinhados com decisões D-A4, D-B1, D-B3, D-B6, D-D1, D-D4, D-D5; partições mensais bem desenhadas.
- Proxy OpenAI-compat (02-04) com `FlushInterval: -1`, header stripping e propagação de `X-Request-ID` corretos para SSE e multipart.
- Cobertura de testes unit + integration (testcontainers-go) consistente com a fase.
- `gatewayctl` como superfície admin mínima (D-A3) fecha bem a dor de produto sem antecipar o dashboard da Fase 7.

### Preocupações prioritárias (bloqueadores antes da execução)

1. **[HIGH] 02-03 — Hot path de auth degenerado**
   - `ListActiveKeysAll` + Argon2 em cada cache miss é vetor de DoS na VPS 4 vCPU; um atacante com chaves bem-formadas mas inválidas força scan completo + verify argon2 para cada tentativa.
   - Ação sugerida: lookup indexado por `key_prefix` visual (últimos 4 chars ou hash rápido auxiliar). Formalizar negative cache (atualmente aparece como adição local sem decisão em D-A2).

2. **[HIGH/MEDIUM] 02-05 — Acoplamento audit ↔ ReverseProxy**
   - Integração do tee writer em `ModifyResponse` está acoplada demais ao proxy base do 02-04; pode gerar bugs sutis em streaming + truncation flag.
   - Risco de goroutine leak em streams longos (Pitfall 9 do research) se o cleanup do buffer não cobrir todas as saídas (close, timeout, error).
   - Ação: definir ponto de extensão explícito para `ModifyResponse` no 02-04 e isolar a captura de chunks num wrapper testável em unit tests.

3. **[MEDIUM] 02-06 — Serialização da idempotency-key incompleta**
   - Concorrência entre dois requests com a mesma `Idempotency-Key` chegando simultaneamente não está claramente resolvida (janela entre read e write no Redis).
   - Ação: usar SETNX + marcador "in-flight" com TTL curto, bloqueando o segundo request até o primeiro completar (ou retornar 409/423 determinístico).

4. **[MEDIUM] 02-01 — Over-provisioning de dependências**
   - `go.mod` traz pgx, go-redis, goose, argon2id, testcontainers já no scaffold, antes de serem usados. Aumenta churn e risco de "semi-wiring".
   - Ação: cada plano adiciona só as dependências que consome; 02-01 fica realmente scaffold.

5. **[MEDIUM] 02-09 — Scope creep potencial**
   - Codex sinaliza que 02-09 não é necessário para os 5 Success Criteria da fase. Recomendação: rebaixar para opcional ou mover para Fase 7.

6. **[LOW] 02-02 — Particionamento sem automação**
   - Seed de "mês atual + 2" resolve curto prazo; falta job/cron para rolar partições antes de expirar (pode quebrar writes em produção se ninguém rodar `gatewayctl`).

### Veredito do Codex

**Risco geral: MEDIUM.** Aprovaria direção, mas não aprovaria execução sem endereçar os 4 itens acima (auth hot path, audit/proxy, idempotency serialization, 02-09 como opcional).

### Como incorporar

Rodar `/gsd-plan-phase 2 --reviews` para que o planner reescreva os planos afetados (02-01, 02-03, 02-05, 02-06, 02-09) usando este feedback como insumo.
