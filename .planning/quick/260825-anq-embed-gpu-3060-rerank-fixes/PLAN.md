---
phase: quick-260825-anq
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - gateway/db/migrations/0036_embed_gpu_tier0.sql
  - gateway/internal/integration_test/migration_0026_test.go
  - gateway/internal/integration_test/migration_0029_test.go
  - gateway/internal/config/config.go
  - gateway/cmd/gateway/main.go
  - gateway/internal/upstreams/loader.go
  - gateway/internal/upstreams/loader_test.go
  - gateway/internal/upstreams/health_test.go
  - gateway/internal/proxy/interceptor_usage.go
  - gateway/internal/proxy/interceptor_usage_test.go
  - gateway/internal/billing/flusher.go
autonomous: true
requirements: [EMBED-GPU-TIER0, FIX-PROBER-RERANK, FIX-BILLING-RERANK]

must_haves:
  truths:
    - "Após migrate up + env setada, embed role tem tier-0 = embed-gpu (pod 3060, Infinity bge-m3) e tier-1 = local-embed (worker-vm CPU, mesmas dims 1024)"
    - "openai-embed (dims 1536) fica em tier 2 — FORA da cascata (ResolveAllTier1 só pega tier==1); pod+CPU down = 503, nunca vetor 1536 em índice 1024"
    - "rerank-gpu passa a ser probado pelo prober (roster tier0Roles inclui rerank) — LAST_PROBE deixa de ser '-'"
    - "rerank tier-0 open + rerank-cpu closed = gateway 'degraded' (200), NUNCA 'failed' (503)"
    - "POST /v1/rerank 200 gera billing_event com route='rerank', tokens_in>0 (usage.prompt_tokens do Infinity) e cost_external_brl=0 (self-hosted)"
    - "Upstreams self-hosted (rerank-gpu, rerank-cpu, embed-gpu) NÃO disparam WARN 'price missing' nem incrementam gateway_prices_missing por request no caminho cost_external"
    - "Migration 0036 Down é simétrico e o ritual Down(N) dos testes 0026/0029 foi bumpado (CI integration verde)"
  artifacts:
    - path: "gateway/db/migrations/0036_embed_gpu_tier0.sql"
      provides: "re-tier embed: openai-embed 1→2, local-embed 0→1 (prio 10), seed embed-gpu tier-0 UPSTREAM_EMBED_GPU_URL"
      contains: "UPSTREAM_EMBED_GPU_URL"
    - path: "gateway/internal/upstreams/loader.go"
      provides: "roster tier0Roles com rerank"
      contains: "\"rerank\""
    - path: "gateway/internal/proxy/interceptor_usage.go"
      provides: "case /v1/rerank → route 'rerank' + isSelfHostedUpstream gate no costExternal"
      contains: "/v1/rerank"
  key_links:
    - from: "gateway/cmd/gateway/main.go"
      to: "embedRoleProxies"
      via: "proxy embed-gpu (NewEmbeddingsProxy com usageInterceptor, guard cfg.UpstreamEmbedGPUURL != \"\")"
      pattern: "embed-gpu"
    - from: "gateway/cmd/gateway/main.go"
      to: "proxy.NewRerankProxy"
      via: "usageInterceptor passado nos DOIS call sites (rerank-gpu + rerank-cpu)"
      pattern: "NewRerankProxy\\(.*usageInterceptor"
    - from: "gateway/internal/proxy/dispatcher.go"
      to: "routeToBillingRoute"
      via: "stamp ctx WithBillingRoute no inbound path /v1/rerank (dispatcher.go:185, já existe — só o case novo faz ele parar de stampar 'chat')"
      pattern: "WithBillingRoute"
---

<objective>
Embed na 3060 unificada espelhando o padrão rerank da migration 0035 (decisão Pedro 2026-08-25, HANDOFF §"Embed na 3060") + 2 fixes de carona no role rerank:

- **A (feature):** novo tier-0 `embed-gpu` (pod Vast 3060 unificado, Infinity bge-m3, dims 1024, env `UPSTREAM_EMBED_GPU_URL`); `local-embed` demovido 0→1 (fallback CPU, MESMO modelo/dims); `openai-embed` (1536≠1024 = corrupção silenciosa de índice) demovido para tier 2, fora da cascata — melhor 503 que vetor errado.
- **B (fix prober):** `tier0Roles` em loader.go:284 não tem "rerank" → rerank-gpu NUNCA é probado (FATO verificado em prod: LAST_PROBE = "-"). Adicionar ao roster.
- **C (fix billing):** POST /v1/rerank 200 não gera NADA em usage (FATO verificado em prod). Causa dupla: NewRerankProxy construído SEM usageInterceptor (main.go ~813/821) + routeToBillingRoute sem case "/v1/rerank" (cai no default "chat", e o guard tokensIn==0 descarta).

**GATEWAY CODE ONLY.** Infra (pod Infinity multi-model, stack 38 env, migrate up em prod) é do orquestrador pós-merge — ver "Deploy steps" no fim.

FORA DO ESCOPO: interceptor over-context para embed (deviation Rule 1 do 260824-ucv permanece — todos os tiers bge-m3 têm o mesmo limite 8192, EmbedContextCap inalterado); custo phantom (costPhantom continua computado como hoje); dashboard; qualquer env/Portainer/pod.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/debug/HANDOFF-validacao-3060-rerank-e-3090-ctx.md
@gateway/db/migrations/0035_upstreams_rerank_role.sql
@gateway/db/migrations/0029_readd_whisper_add_gemini_groq.sql
@gateway/internal/upstreams/loader.go
@gateway/internal/upstreams/health.go
@gateway/internal/proxy/interceptor_usage.go
@gateway/cmd/gateway/main.go

<interfaces>
<!-- FATOS verificados 2026-08-25 lendo o código + prod. Use direto, não re-investigue. -->

Schema upstreams (0007 + 0029):
  - CHECK role IN ('llm','stt','embed','tts','rerank') — 'embed' JÁ permitido, 0036 NÃO mexe no CHECK.
  - tier NÃO tem CHECK → tier=2 é válido sem DDL.
  - UNIQUE (role, tier, tier_priority) [0029 trocou o UNIQUE(role,tier) original] — ordem dos UPDATEs importa.
  - Rows embed atuais: local-embed (embed, 0, prio 0, UPSTREAM_EMBED_URL) e openai-embed (embed, 1, prio 0, UPSTREAM_EMBED_OPENAI_URL).

gateway/internal/upstreams/loader.go:
  - :284 `var tier0Roles = []string{"llm", "stt", "tts", "embed"}` ← FALTA "rerank".
  - LoadFromDB SKIPA row cujo url_env não está setado (log status=missing_url_env) → embed-gpu sem env = sem tier-0 = dispatcher 503 (HAZARD de deploy, ver Deploy steps).
  - ResolveAllTier1(role) filtra `Tier == 1` estrito → openai-embed em tier 2 sai da cascata sem código novo.
  - ListEnabledUpstreams (db/queries/upstreams.sql) não filtra tier — row tier-2 entra no snapshot mas nunca é resolvida pelo dispatch. Inofensivo.

gateway/internal/upstreams/health.go:
  - buildHealthResponse: tier-0 via ResolveTier0Roles (roster); demais rows (tier 1+) no loop 2.
  - Semântica pós-fix B: rerank-gpu open + rerank-cpu closed → allTier0Closed=false, allRolesHaveAnyClosed=true → "degraded" (HTTP 200). Ambos down → "failed" 503. Igual aos outros roles — comportamento desejado.

gateway/internal/upstreams/probe.go:
  - case "rerank" (:315-321) JÁ existe e funciona (POST /v1/rerank 1-query/1-doc). Testes em probe_test.go:~370+.
  - case "embed" (:278) já existe: POST /v1/embeddings {"input":"ping","model":"probe-default"} — funciona contra Infinity (probe do local-embed CPU usa exatamente isso hoje).
  - Prober: tier-0 via ResolveTier0Roles (roster); tier-1+ probados no loop All(). rerank-cpu JÁ é probado hoje; só o gpu (tier-0) fica fora por causa do roster.

gateway/cmd/gateway/main.go:
  - :595 `embedRP, err := proxy.NewEmbeddingsProxy(cfg.UpstreamEmbedURL, log, usageInterceptor)` — molde do proxy novo.
  - :688 `embedRoleProxies := map[string]http.Handler{"local-embed": embedRP}` + openai-embed condicional.
  - :811-827 rerankRoleProxies: `proxy.NewRerankProxy(cfg.UpstreamRerankURL, log)` — SEM interceptor (bug C.1). Assinatura JÁ é variádica: `NewRerankProxy(upstreamURL string, log *slog.Logger, interceptors ...ProxyResponseInterceptor)` → fix = passar usageInterceptor nos 2 call sites.

gateway/internal/config/config.go:
  - :61-62 UpstreamRerankURL / UpstreamRerankFallbackURL (molde: opcional, os.Getenv em :391-392, FORA da lista de required).

gateway/internal/proxy/interceptor_usage.go:
  - routeToBillingRoute (:418): switch por prefixo; sem case /v1/rerank → default "chat" (bug C.2). O dispatcher stampa o route no ctx a partir do INBOUND path (dispatcher.go:185) — o case novo corrige o stamp e o fallback.
  - usageJSONBuffer.Close: parseia usage top-level {prompt_tokens, completion_tokens} → TokensIn/TokensOut. Infinity rerank responde `{"usage":{"prompt_tokens":29,"total_tokens":29}}` (FATO, medido em prod) → TokensIn=29 sem código novo. applyAudioEmbedUsage default = no-op para "rerank" — CORRETO, não criar branch (tokens já vêm do parse top-level, não há dimensão extra tipo seconds/embeds).
  - FinalizeRequest :257 `isLocal := strings.HasPrefix(upstream, "local-")` — rerank-gpu/rerank-cpu/embed-gpu NÃO casam o prefixo → priceTokens roda no caminho costExternal → prices.Get miss → WARN "price missing" + obs.GatewayPricesMissing.Inc() POR REQUEST (cost.go:35-44). Precisa de gate self-hosted.
  - Guard :210: tokensIn==0 && tokensOut==0 && audioSeconds==0 && embedsCount==0 → skip enqueue. É por isso que rerank hoje some do billing mesmo se o route estivesse certo.

billing_events.route (0010): TEXT NOT NULL, SEM CHECK/enum → valor novo "rerank" não precisa de migration. usage_counters idem. gatewayctl usage report agrega billing_events sem filtro de route.

Model-name parity (FATO, inspecionado live no worker-vm 2026-08-25):
  ai-gateway-embed_embed args = ["v2","--model-id","BAAI/bge-m3","--served-model-name","bge-m3",...,"--url-prefix","/v1","--port","7997"]
  → clientes mandam model="bge-m3"; o pod DEVE servir `--served-model-name bge-m3` (não BAAI/bge-m3). Passthrough sem director exige paridade exata.
  ⚠️ deploy/embed/docker-compose.yml no repo está STALE (multilingual-e5-large) — NÃO usar como referência; a fonte é o serviço swarm live acima.

Ritual Down(N) (commit 06e209a como receita — ler antes de editar):
  Quando 0036 entrar no HEAD, bumpar os Down relativos que marcham do HEAD:
  - migration_0026_test.go:110 Down(8)→Down(9) (+ comentário "quick-260825-anq HEAD bump ... 0036")
  - migration_0026_test.go:243 Down(10)→Down(11) (+ comentário + t.Fatal message)
  - migration_0029_test.go:189 Down(7)→Down(8) (+ comentário)
  - migration_0028_test.go usa Down(1) ancorado (não marcha do HEAD) → NÃO bumpar.
</interfaces>
</context>

<tasks>

<task type="auto" tdd="true">
  <name>Task 1: Migration 0036 — embed-gpu tier-0, local-embed 0→1, openai-embed 1→2 + ritual Down(N)</name>
  <files>gateway/db/migrations/0036_embed_gpu_tier0.sql, gateway/internal/integration_test/migration_0026_test.go, gateway/internal/integration_test/migration_0029_test.go</files>
  <behavior>
    - Integration (ritual): TestIntegration_Migration0026_UpDownUp verde com Down(9); TestIntegration_Migration0026_DownAbortsOnDuplicateAliases verde com Down(11); TestIntegration_Migration0029_Down_Symmetric verde com Down(8).
    - Up→Down→Up da suíte completa idempotente (goose full march passa 0036 duas vezes sem erro — ON CONFLICT + UPDATEs com WHERE de estado).
  </behavior>
  <action>
Criar `gateway/db/migrations/0036_embed_gpu_tier0.sql` espelhando o estilo/comentários da 0035 (header explicando topologia, engine note, idempotência).

Up (SET search_path = ai_gateway, public; a ORDEM respeita UNIQUE(role,tier,tier_priority)):
1. `UPDATE ai_gateway.upstreams SET tier = 2 WHERE name = 'openai-embed' AND tier = 1;` — PRIMEIRO, para liberar (embed,1,0)… mas o passo 2 usa prio 10, então a ordem aqui é por clareza/segurança; manter assim mesmo. Comentário obrigatório: tier 2 = FORA da cascata tier-1 (ResolveAllTier1 filtra tier==1); dims 1536 ≠ índice 1024 = corrupção silenciosa, melhor 503; re-enable manual = `UPDATE ... SET tier = 1 WHERE name='openai-embed'`. Row/env retidas de propósito.
2. `UPDATE ai_gateway.upstreams SET tier = 1, tier_priority = 10 WHERE name = 'local-embed' AND tier = 0;` — prio 10 segue a convenção do cascade STT (0029: primário tier-1 = 10).
3. `INSERT INTO ai_gateway.upstreams (name, role, tier, tier_priority, url_env, auth_bearer_env) VALUES ('embed-gpu', 'embed', 0, 0, 'UPSTREAM_EMBED_GPU_URL', NULL) ON CONFLICT (name) DO NOTHING;`

NÃO tocar no CHECK de role ('embed' já permitido desde 0007) nem criar CHECK de tier (não existe). NÃO tocar em model_aliases (embed-gpu é passthrough sem director; o alias bge-m3→openai-embed existente continua servindo só o director do tier-2).

Down simétrico (ordem reversa):
1. `DELETE FROM ai_gateway.upstreams WHERE name = 'embed-gpu';`
2. `UPDATE ... SET tier = 0, tier_priority = 0 WHERE name = 'local-embed' AND tier = 1;`
3. `UPDATE ... SET tier = 1 WHERE name = 'openai-embed' AND tier = 2;`
Com o mesmo WARNING de operador da 0035 (rows extras criadas manualmente precisam de limpeza manual antes de full-down).

Ritual Down(N) — ler `git show 06e209a` antes; aplicar os 3 bumps listados em <interfaces> (0026:110 → Down(9), 0026:243 → Down(11), 0029:189 → Down(8)), SEMPRE atualizando o comentário narrativo ("quick-260825-anq HEAD bump: ... when 0036 (embed-gpu tier-0) landed on HEAD") e as mensagens de t.Fatal que enumeram as migrations peladas. migration_0028_test.go NÃO se bumpa (Down(1) ancorado).

Commit: `feat(gateway): migration 0036 — embed-gpu tier-0 na 3060, local-embed vira fallback, openai-embed fora da cascata`
  </action>
  <verify>
    <automated>sudo env PATH=/usr/local/go/bin:$PATH HOME=$HOME GOCACHE=$HOME/.cache/go-build GOPATH=$HOME/go go test -tags integration ./internal/integration_test/ -run Migration -count=1 (cwd gateway/)</automated>
  </verify>
  <done>0036 Up/Down simétricos e idempotentes; testes de migration 0026/0028/0029 verdes com os Down(N) bumpados.</done>
</task>

<task type="auto">
  <name>Task 2: Config + wiring embed-gpu no main.go</name>
  <files>gateway/internal/config/config.go, gateway/cmd/gateway/main.go</files>
  <action>
config.go (molde: UpstreamRerankURL, linhas 60-62 e 391-392):
- Adicionar campo `UpstreamEmbedGPUURL string // UPSTREAM_EMBED_GPU_URL (optional — tier-0 embed-gpu, pod Vast 3060 unificado, Infinity bge-m3 dims 1024)` no bloco dos upstreams, ao lado dos rerank.
- `UpstreamEmbedGPUURL: os.Getenv("UPSTREAM_EMBED_GPU_URL"),` no Load. NÃO adicionar à lista de required (UPSTREAM_EMBED_URL segue required; a env nova é opcional como as de rerank).

main.go:
- Perto do bloco embedRoleProxies (:688), construir o proxy tier-0 novo com o MESMO construtor do local-embed (passthrough, sem director — ambos os tiers servem served-model-name `bge-m3`, dims 1024; paridade verificada live no worker-vm):
  ```
  if cfg.UpstreamEmbedGPUURL != "" → embedGPU, err := proxy.NewEmbeddingsProxy(cfg.UpstreamEmbedGPUURL, log, usageInterceptor)
  err → log.Warn (mesmo padrão do openai-embed guard) ; ok → embedRoleProxies["embed-gpu"] = embedGPU
  ```
  A chave do map DEVE ser exatamente `embed-gpu` (nome da row 0036 — dispatcher resolve por nome).
- Adicionar `"upstream_embed_gpu", cfg.UpstreamEmbedGPUURL,` no log de boot (:176, ao lado de upstream_embed).
- Comentário no bloco: tier-1 = local-embed (mesmo modelo/dims, fallback via breaker); openai-embed em tier 2 fica no map mas nunca é resolvido pela cascata (ResolveAllTier1 filtra tier==1) — entrada dormente para re-enable manual.
- NENHUMA mudança no dispatcher/probe: tier0Roles já tem "embed", probe case "embed" já funciona contra Infinity, EmbedContextCap 8192 inalterado.

Commit: `feat(gateway): wiring embed-gpu — UPSTREAM_EMBED_GPU_URL + proxy tier-0 no role embed`
  </action>
  <verify>
    <automated>cd gateway && gofmt -l . | (! grep .) && go build ./... && go vet ./... && go test ./internal/config/... ./cmd/... -count=1</automated>
  </verify>
  <done>Env opcional nova plumbada; embedRoleProxies ganha "embed-gpu" com usageInterceptor; build+vet+unit verdes.</done>
</task>

<task type="auto" tdd="true">
  <name>Task 3: Fix prober — "rerank" no roster tier0Roles + semântica de health</name>
  <files>gateway/internal/upstreams/loader.go, gateway/internal/upstreams/loader_test.go, gateway/internal/upstreams/health_test.go</files>
  <behavior>
    - loader_test: snapshot com rows rerank-gpu (tier 0) + rerank-cpu (tier 1) → ResolveTier0Roles() inclui uma resolução com Role=="rerank" e Effective.Name=="rerank-gpu" (molde: testes existentes de ResolveTier0Roles/Resolve em loader_test.go).
    - health_test: rerank-gpu breaker OPEN + rerank-cpu CLOSED (demais roles tier-0 closed) → status "degraded", HTTP 200 — NÃO "failed"/503 (molde: testes existentes de buildHealthResponse).
    - health_test (ou caso na mesma tabela): rerank-gpu E rerank-cpu open → "failed" 503 (paridade com os outros roles).
  </behavior>
  <action>
loader.go:284: `var tier0Roles = []string{"llm", "stt", "tts", "embed", "rerank"}` + atualizar o godoc do roster citando quick-260825-anq (rerank-gpu nunca era probado; LAST_PROBE "-" em prod).

Consequências automáticas (verificar via testes, sem código novo): prober passa a probar rerank-gpu via ResolveTier0Roles (probe case "rerank" já existe em probe.go:315); health passa a incluir rerank no aggregate tier-0 — a semântica allTier0Closed/allRolesHaveAnyClosed já dá "degraded" (não "failed") quando só o tier-0 está open com CPU closed, que é o comportamento correto lido em health.go:237-247.

Commit: `fix(gateway): adiciona rerank ao roster tier0Roles — rerank-gpu nunca era probado (LAST_PROBE '-')`
  </action>
  <verify>
    <automated>cd gateway && go test ./internal/upstreams/... -count=1</automated>
  </verify>
  <done>Roster com rerank; testes provam probe-eligibility do rerank-gpu e que rerank tier-0 down NÃO derruba o health pra failed com CPU up.</done>
</task>

<task type="auto" tdd="true">
  <name>Task 4: Fix billing rerank — usageInterceptor no proxy + route "rerank" + gate self-hosted no costExternal</name>
  <files>gateway/cmd/gateway/main.go, gateway/internal/proxy/interceptor_usage.go, gateway/internal/proxy/interceptor_usage_test.go, gateway/internal/billing/flusher.go</files>
  <behavior>
    - routeToBillingRoute("/v1/rerank") == "rerank" (test table junto dos casos existentes em interceptor_usage_test.go:~266).
    - Resposta JSON de rerank do Infinity `{"results":[...],"usage":{"prompt_tokens":29,"total_tokens":29}}` atravessando usageJSONBuffer.Close com ctx stampado route=rerank → RequestUsage.TokensIn==29, TokensOut==0; FinalizeRequest com upstream "rerank-gpu" enfileira billing.Event com Route=="rerank", TokensIn==29, CostExternalBRL==0 (molde: testes de Close/Finalize existentes no arquivo; usar o harness DB-free com flusher nil OU o padrão de captura já usado lá — ler antes).
    - applyAudioEmbedUsage(usage, "rerank", body, ctx) → no-op (nenhum atomic tocado) — teste direto no padrão da seção "Phase 16-01 Task 3" (:211+).
    - isSelfHostedUpstream: true para "local-*" (prefixo), "rerank-gpu", "rerank-cpu", "embed-gpu"; false para "openrouter-chat", "openai-embed", "gemini-stt".
  </behavior>
  <action>
main.go (:813 e :821): passar `usageInterceptor` como terceiro argumento nos DOIS `proxy.NewRerankProxy(...)` (assinatura já é variádica — nenhuma mudança em rerank.go). Atualizar o comentário do bloco (:805) registrando que o billing agora captura usage.prompt_tokens do Infinity.

interceptor_usage.go:
1. routeToBillingRoute: adicionar `case strings.HasPrefix(path, "/v1/rerank"): return "rerank"` antes do default. Isso corrige TANTO o stamp do dispatcher (dispatcher.go:185, inbound path) QUANTO o fallback b.reqPath.
2. Extrair o gate de custo para `func isSelfHostedUpstream(upstream string) bool` — `strings.HasPrefix(upstream, "local-") || upstream == "rerank-gpu" || upstream == "rerank-cpu" || upstream == "embed-gpu"` — e usar no lugar do `isLocal := strings.HasPrefix(upstream, "local-")` em FinalizeRequest (:257). Godoc: upstreams self-hosted têm custo externo 0 por definição; sem o gate, cada request roda prices.Get miss → WARN "price missing" + gateway_prices_missing.Inc() por request (cost.go:35-44) e polui o alerta ME-05. NÃO mexer no costPhantom (comportamento existente preservado).
3. applyAudioEmbedUsage: NÃO adicionar branch "rerank" — atualizar apenas o godoc registrando a decisão (tokens de rerank vêm do parse top-level de usage no Close, igual chat/embed; não há dimensão extra).

flusher.go:33: atualizar comentário do campo Route para `"chat" | "embed" | "stt" | "rerank"`.

billing_events.route é TEXT sem CHECK (0010) e usage report não filtra route — nenhuma migration adicional.

Commit: `fix(gateway): billing de rerank — usageInterceptor no proxy, route 'rerank', custo externo 0 p/ self-hosted`
  </action>
  <verify>
    <automated>cd gateway && go test ./internal/proxy/... ./internal/billing/... -count=1</automated>
  </verify>
  <done>Rerank 200 produz billing.Event route=rerank com tokens>0 e custo externo 0; sem WARN de price-missing para upstreams self-hosted; testes unit verdes.</done>
</task>

<task type="auto">
  <name>Task 5: Gate de CI local — gofmt + suíte completa + integration com Docker</name>
  <files>(nenhum novo — só correções se algo falhar)</files>
  <action>
Rodar, a partir de `gateway/`:
1. `gofmt -l .` → saída VAZIA obrigatória (memória gateway-integration-tests-not-in-executor-check: CI roda gofmt; executor local não).
2. `go build ./... && go vet ./...`
3. `go test ./... -count=1`
4. Integration com Docker (testcontainers precisa de root/env):
   `sudo env PATH=/usr/local/go/bin:$PATH HOME=$HOME GOCACHE=$HOME/.cache/go-build GOPATH=$HOME/go go test -tags integration ./internal/integration_test/ -run Migration -count=1`
5. Rodar também os demais integration afetáveis: `sudo env ... go test -tags integration ./... -count=1` — se algum falhar por flake conhecido (testcontainers), re-rodar 1x antes de investigar.

Qualquer falha → corrigir na task de origem e emendar/commit fix atômico (`test(quick-260825-anq): ...`). NÃO considerar o plano pronto com integration vermelho — o 0035 entrou sem o ritual e deixou develop vermelho (06e209a).
  </action>
  <verify>
    <automated>cd gateway && gofmt -l . | (! grep .) && go test ./... -count=1</automated>
  </verify>
  <done>gofmt limpo, unit + integration (Migration no mínimo) verdes localmente com os 4 commits das tasks 1-4 no develop.</done>
</task>

</tasks>

<verification>
1. `gofmt -l gateway` vazio; `go build ./...`; `go vet` limpo; `go test ./... -count=1` verde.
2. Integration verde: `sudo env PATH=/usr/local/go/bin:$PATH HOME=$HOME GOCACHE=$HOME/.cache/go-build GOPATH=$HOME/go go test -tags integration ./internal/integration_test/ -run Migration -count=1` (cwd gateway/).
3. `git log --oneline` mostra 4 commits atômicos (migration+ritual / wiring embed-gpu / roster rerank / billing rerank), mensagens em português no estilo do repo.
4. `git diff develop@{start} --name-only` restrito a `gateway/db/migrations/0036*`, `gateway/internal/{config,upstreams,proxy,billing,integration_test}/`, `gateway/cmd/gateway/main.go` (+ `.planning/`). `gateway/internal/proxy/rerank.go`, `dispatcher.go`, `probe.go`, `deploy/embed/docker-compose.yml` INTOCADOS.
5. Grep de sanidade: `grep -n '"rerank"' gateway/internal/upstreams/loader.go` (roster); `grep -n 'v1/rerank' gateway/internal/proxy/interceptor_usage.go` (route case); `grep -n 'usageInterceptor' gateway/cmd/gateway/main.go | grep -i rerank` (2 call sites).
</verification>

<success_criteria>
- Migration 0036 espelha a 0035 (idempotente, Down simétrico, WARNING de operador) e o ritual Down(N) foi aplicado (0026 ×2, 0029 ×1) — CI integration não repete o vermelho do 0035.
- `embed-gpu` resolvível como tier-0 do role embed assim que UPSTREAM_EMBED_GPU_URL existir; local-embed vira tier-1; openai-embed inerte em tier 2 (row+env+proxy retidos para re-enable manual).
- rerank-gpu entra no ciclo do prober (roster) sem tornar o gateway "failed" quando só o tier-0 rerank cai.
- POST /v1/rerank 200 gera billing_event {route:"rerank", tokens_in>0, cost_external_brl:0} e aparece no `gatewayctl usage report`.
</success_criteria>

<deploy_steps>
## Deploy steps — ORQUESTRADOR pós-merge (NÃO são tasks deste plano)

⚠️ **ORDEM IMPORTA (hazard verificado no código):** o loader SKIPA rows com url_env não setada. Rodar `migrate up` (0036) com o gateway SEM `UPSTREAM_EMBED_GPU_URL` deixa o role embed SEM tier-0 → `Resolve("embed",0)` falha → **503 em /v1/embeddings** até a env chegar. Sequência: pod → env+imagem → migrate.

1. **Pod unificado 3060 (91.150.160.38):** adicionar bge-m3 ao Infinity que já serve o rerank (multi-model no MESMO processo/porta): args adicionais `--model-id BAAI/bge-m3 --served-model-name bge-m3`. Persistir no onstart do pod (mesma config da sessão do rerank). **Paridade obrigatória:** served-model-name = `bge-m3` exato — é o que o worker-vm serve (verificado live: `["v2","--model-id","BAAI/bge-m3","--served-model-name","bge-m3",...]`) e o model string que os clientes mandam; passthrough sem director não tolera divergência. Sanidade: `curl pod:PORTA/v1/embeddings -d '{"input":"ping","model":"bge-m3"}'` → 200, `len(embedding)==1024`.
2. **Stack 38 (Portainer, worker-vm):** no MESMO update — `UPSTREAM_EMBED_GPU_URL=http://91.150.160.38:15165` (porta pública mapeada do 7998 — a MESMA do rerank, Infinity multi-model num processo só) + imagem nova do gateway (com Tasks 2-4). GOTCHA Portainer: env + imagem juntos num único Update da UI.
3. **Migration:** `gatewayctl migrate up` (aplica 0036). O loader recarrega via LISTEN upstreams_changed / restart do task swarm.
4. **Validação E2E:**
   - Embed tier-0: request /v1/embeddings model=bge-m3 → 200 dims 1024; log do gateway despachando embed-gpu; `gatewayctl` upstreams/health mostra embed-gpu closed com LAST_PROBE preenchido.
   - Failover: `gatewayctl breaker force-open --upstream embed-gpu` → request cai no local-embed (CPU, mesmas dims) → `force-close`.
   - Rerank probe: LAST_PROBE do rerank-gpu deixa de ser "-" no ciclo seguinte do prober.
   - Rerank billing: POST /v1/rerank (tenant real) → `gatewayctl usage report --tenant <slug>` mostra route=rerank com tokens_in>0 e custo 0.
   - Health: com embed-gpu E rerank-gpu up → status "ok"; derrubando só um tier-0 → "degraded" (nunca "failed" com CPU up).
5. **Rollback:** `gatewayctl migrate down 1` (0036 Down restaura local-embed tier-0 + openai-embed tier-1) + remover env do stack. openai-embed re-enable manual: `UPDATE ai_gateway.upstreams SET tier=1 WHERE name='openai-embed'` (só se aceitar dims 1536 no índice — corrupção silenciosa; evitar).
</deploy_steps>

<output>
Create `.planning/quick/260825-anq-embed-gpu-3060-rerank-fixes/SUMMARY.md` when done
</output>
