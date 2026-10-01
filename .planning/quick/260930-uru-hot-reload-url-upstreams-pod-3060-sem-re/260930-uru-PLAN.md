---
phase: quick-260930-uru
plan: 01
type: execute
wave: 1
depends_on: []
clickup: 86akrbegb
files_modified:
  - gateway/db/migrations/0038_upstreams_url_override.sql
  - gateway/db/queries/upstreams.sql
  - gateway/internal/db/gen/upstreams.sql.go
  - gateway/internal/db/gen/models.go
  - gateway/internal/db/migrate_test.go
  - gateway/internal/upstreams/loader.go
  - gateway/internal/upstreams/url.go
  - gateway/internal/upstreams/exports_helpers.go
  - gateway/internal/upstreams/loader_override_test.go
  - gateway/cmd/gatewayctl/upstreams.go
  - gateway/cmd/gatewayctl/upstreams_test.go
  - gateway/internal/proxy/dynamic_target.go
  - gateway/internal/proxy/dynamic_target_test.go
  - gateway/internal/proxy/embeddings.go
  - gateway/internal/proxy/audio.go
  - gateway/internal/proxy/tts.go
  - gateway/internal/proxy/rerank.go
  - gateway/internal/breaker/breaker.go
  - gateway/internal/breaker/breaker_reset_test.go
  - gateway/internal/shed/latency.go
  - gateway/cmd/gateway/main.go
  - gateway/internal/primary/sweep.go
  - gateway/internal/emerg/sweep.go
  - ops/vast-3060/unified3060.py
  - ops/vast-3060/test_unified3060.py
autonomous: true
requirements: [QUICK-260930-uru]

must_haves:
  truths:
    - "Trocar a URL de local-stt, kokoro-tts, rerank-gpu e embed-gpu via `gatewayctl upstreams update --name X --url Y` muda o alvo do proxy no próximo request SEM recriar a task do gateway"
    - "url_override válido tem precedência sobre os.Getenv(url_env); override inválido gera WARN e cai na env; row com só override (env vazia) carrega"
    - "Se a URL efetiva de uma row muda no reload, o breaker dessa row volta CLOSED (e a janela de latência do shed é zerada)"
    - "Request para upstream registrado sem URL efetiva cascateia pro tier-1 (fallthrough), nunca panic nem 503 terminal 'proxy not registered'"
    - "Um Refresh periódico (60s) recarrega a tabela mesmo se o NOTIFY for perdido"
    - "unified3060.py start troca as 4 URLs via url_override (ssh worker-vm + /gatewayctl), sem PUT no Portainer stack 38"
    - "Cada execução do label sweep (primary e emerg) emite 1 log INFO com listed, matched_label, orphans, destroyed, failed"
  artifacts:
    - path: "gateway/db/migrations/0038_upstreams_url_override.sql"
      provides: "coluna url_override + trigger upstreams_update_notify recriado"
      contains: "url_override"
    - path: "gateway/internal/proxy/dynamic_target.go"
      provides: "Director/RoundTripper que resolvem alvo por request"
    - path: "gateway/internal/upstreams/url.go"
      provides: "ValidateUpstreamURL compartilhado loader+gatewayctl"
      exports: ["ValidateUpstreamURL"]
  key_links:
    - from: "gateway/cmd/gateway/main.go"
      to: "loader.TargetURL(name)"
      via: "closure passada aos 4 proxies dinâmicos"
      pattern: "TargetURL\\(\""
    - from: "gateway/internal/upstreams/loader.go Refresh"
      to: "breakerSet.Reset"
      via: "hook OnURLChange registrado em main.go"
      pattern: "OnURLChange"
    - from: "ops/vast-3060/unified3060.py flip_upstreams"
      to: "/gatewayctl upstreams update --url"
      via: "ssh root@10.10.10.50 docker exec"
      pattern: "upstreams\", \"update\""
---

<objective>
Eliminar o restart diário do gateway prod causado pelo flip das 4 URLs do pod 3060. Hoje `flip_stack()` (ops/vast-3060/unified3060.py:293) faz PUT no Portainer stack 38 mudando envs → swarm recria a task (stop-first). Depois deste plano a URL vive em `ai_gateway.upstreams.url_override`, é recarregada por NOTIFY/LISTEN (+ refresh periódico) e os proxies resolvem o alvo por request. Bônus: label sweep (quick 260930-i7i) passa a ser observável.

Purpose: zero blip no gateway durante a troca diária do pod; breaker limpo para o pod novo.
Output: migration 0038, loader com override, 4 proxies dinâmicos, gatewayctl --url/--clear-url, unified3060 sem PUT, logs do sweep.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md
@./CLAUDE.md

<interfaces>
<!-- Extraídos do código. Usar direto, sem exploração extra. -->

gateway/internal/upstreams/loader.go:
- `type snapshot struct { byName map[string]UpstreamConfig; byRoleTier map[RoleTier]UpstreamConfig; ordered []UpstreamConfig }`
- `type Loader struct { pool; q loaderQueries; snap atomic.Pointer[snapshot]; log; tier0Override map[string]*atomic.Pointer[string] }`
- `func (l *Loader) Refresh(ctx) error` — L132: `url := os.Getenv(r.UrlEnv)`; vazio → WARN "upstream url env var missing; row skipped" + continue. Loga INFO "upstreams refreshed" a cada chamada.
- `Get(name) (UpstreamConfig,bool)`, `Resolve`, `Names()`, `All()`, `OverrideTier0/RestoreTier0/Tier0OverrideURL` (roles llm/stt/tts, usados pelos reconcilers primary/emerg — NÃO TOCAR/REUSAR).
- `NewLoaderInMemory(cfgs ...UpstreamConfig) *Loader` em exports_helpers.go (testes).
- UpstreamConfig.URL string (types.go:34). Prober usa u.URL do loader → segue override automaticamente.

gateway/internal/upstreams/listen.go:
- `ListenAndReload(ctx, dsn, loader, onReload func(), log) error` — NOTIFY upstreams_changed → loader.Refresh → onReload().

gateway/cmd/gateway/main.go:
- L520-538: onReload closure inline: `breakerSet.Rebuild(loader.Names()); shedSet.Rebuild(...); shedLatency[n] = shed.NewLatencyRing(...) se faltar; shedInflight.AddUpstream(n)`.
- L182/184: log de boot com cfg.UpstreamSTTURL / cfg.UpstreamEmbedGPUURL (manter).
- L613-614: `if cfg.UpstreamSTTURL != "" { audioRP, err = proxy.NewAudioProxy(cfg.UpstreamSTTURL, log, resolver, usageInterceptor) }` → sttRoleProxies["local-stt"] (L736-738).
- L704-711: `if cfg.UpstreamEmbedGPUURL != "" { proxy.NewEmbeddingsProxy(cfg.UpstreamEmbedGPUURL, log, usageInterceptor) }` → embedRoleProxies["embed-gpu"].
- L809-816: `if cfg.UpstreamTTSKokoroURL != "" { proxy.NewTTSProxy(cfg.UpstreamTTSKokoroURL, log) }` → ttsRoleProxies["kokoro-tts"] (sem usageInterceptor hoje — manter igual).
- L838-845: `if cfg.UpstreamRerankURL != "" { proxy.NewRerankProxy(cfg.UpstreamRerankURL, log, usageInterceptor) }` → rerankRoleProxies["rerank-gpu"].

gateway/internal/proxy:
- `BuildDirector(u *url.URL) func(*http.Request)` (director.go:41) — reescreve scheme/host/path-join/Host + strip client auth.
- `BuildOpenAIWhisperDirector(upstream *url.URL, authBearer string, resolver *models.Resolver, upstreamName string, log) func(*http.Request)` — usado por NewAudioProxy com ("", resolver, "local-stt").
- `fallthroughRoundTripper{base}` (transport.go) — converte erro connection-class (DNS, ECONNREFUSED, dial OpError) em `errDialFailedFallthrough`; ErrorHandler suprime e dispatcher cascateia pro tier-1. Erro "no Host in request URL" NÃO é connection-class.
- `dynamicOverrideDirector` (dynamic_override.go) com `!ok` só faz return (deixa host vazio) — padrão a NÃO copiar para o caso "sem URL" (geraria erro não-fallthrough).
- dispatcher.go:729 `proxy, ok := cfg.Proxies[name]; if !ok → 503 terminal (wrote=true)` — por isso os 4 proxies têm que ser registrados SEMPRE.
- Transports atuais: embed 50/10/90s/RHT 10s; audio 20/4/90s/RHT 60s + interceptor sttRetryableStatusInterceptor prepended; tts 20/4/90s/RHT 60s; rerank 20/4/90s/RHT 30s. ErrorHandler roles: "embed","stt","tts","rerank".

gateway/internal/breaker/breaker.go:
- `type Set struct { mu sync.RWMutex; cbs map[string]*gobreaker.CircuitBreaker[*http.Response]; remoteOpen map[string]time.Time; forceCache *forceCache; ... }`
- `Rebuild(names)` preserva breakers existentes; `newBreaker(n)` (privado). Não existe Reset.

gateway/internal/shed/latency.go: `type LatencyRing struct { buf []uint32; size uint64; idx atomic.Uint64 }`, `Record(ms)`, `P95()`. Não existe Reset.

gateway/db/migrations/0009_upstreams_notify_trigger.sql: trigger `upstreams_update_notify` AFTER UPDATE WHEN pg_trigger_depth()=0 AND (name|role|tier|url_env|auth_bearer_env|enabled|weight|circuit_config IS DISTINCT FROM) EXECUTE FUNCTION ai_gateway.notify_upstreams_changed(). Nenhuma migration posterior redefiniu esse trigger.

gateway/db/queries/upstreams.sql: ListEnabledUpstreams / ListAllUpstreams / GetUpstreamByName selecionam colunas explícitas (`id, name, role, tier, tier_priority, url_env, auth_bearer_env, enabled, weight, circuit_config, last_probe_*, created_at, updated_at`). UpdateUpstreamAdmin usa COALESCE (não serve para limpar → query nova).

gateway/internal/db/migrate_test.go:79 — lista explícita de migrations esperadas termina em "0037_model_aliases_provider_prefs.sql" → adicionar 0038.

CI sqlc: `.github/workflows/build-gateway.yml` SQLC_VERSION 'v1.30.0', roda `cd gateway && sqlc generate` e falha se o tree mudar. sqlc NÃO está instalado localmente.

gateway/internal/primary/sweep.go `sweepOrphanInstances`: ListInstances → GetOpenPrimaryLifecycle → `vastutil.SelectLabelOrphans(instances, vastutil.PrimaryLabelPrefix, live, keep, now, orphanSweepMinAge)` → loop destroy (`log.Error` + continue em falha; `obs.GatewayVastOrphanSweptTotal.WithLabelValues("primary").Inc()` em sucesso). emerg/sweep.go idêntico com `vastutil.EmergLabelPrefix` e mensagens "emerg orphan ...".

ops/vast-3060/unified3060.py:
- `ENVMAP = {"UPSTREAM_STT_URL":"8000/tcp","UPSTREAM_TTS_KOKORO_URL":"8021/tcp","UPSTREAM_RERANK_URL":"7998/tcp","UPSTREAM_EMBED_GPU_URL":"7998/tcp"}` (L52-57).
- `flip_stack(env, ip, ports)` L293-316 (PUT Portainer); chamado em cmd_start L516-518 dentro de try/except → `fail("flip stack: ...")`; depois loop `v.validate_edge(env)` 10x20s (manter).
- Roda como ROOT via systemd (sem User= em ops/vast-3060/systemd/*.service); `SSH_KEY="/home/pedro/.ssh/id_ed25519"`; já usa `subprocess.run(["ssh","-i",SSH_KEY,...])` em ssh_pod (L155-163).
- Testes: stdlib unittest só de funções PURAS (`python3 -m unittest -v test_unified3060` em ops/vast-3060).
- Container do gateway prod: `docker ps -q -f name=ai-gateway-prod_gateway | head -1` no worker-vm (10.10.10.50); `/gatewayctl` dentro do container já tem DSN (padrão documentado em CLAUDE.md: `docker exec $(...) /gatewayctl admin-key list`).
</interfaces>
</context>

<tasks>

<task type="auto" tdd="true">
  <name>Task 1: Migration 0038 url_override + sqlc + loader com precedência/validação/hook + gatewayctl --url/--clear-url</name>
  <files>gateway/db/migrations/0038_upstreams_url_override.sql, gateway/db/queries/upstreams.sql, gateway/internal/db/gen/* (regenerado), gateway/internal/db/migrate_test.go, gateway/internal/upstreams/url.go, gateway/internal/upstreams/loader.go, gateway/internal/upstreams/exports_helpers.go, gateway/internal/upstreams/loader_override_test.go, gateway/cmd/gatewayctl/upstreams.go, gateway/cmd/gatewayctl/upstreams_test.go</files>
  <behavior>
    - ValidateUpstreamURL("http://1.2.3.4:5000") = nil; "https://h" = nil; "" / "ftp://h" / "http://" / "1.2.3.4:80" / "not a url" → erro.
    - Loader (stub loaderQueries): row com url_override válido + env setada → URL = override.
    - Row com url_override inválido + env setada → URL = env, WARN logado com status "invalid_url_override".
    - Row com url_override válido + env vazia → row CARREGA (antes era skipped).
    - Row sem override e sem env → continua skipped (comportamento atual).
    - loader.TargetURL(name) devolve *url.URL parseado do snapshot (mesmo ponteiro em chamadas repetidas até o próximo Refresh); (nil,false) para nome ausente.
    - Hook OnURLChange: Refresh 1 com URL A, Refresh 2 com URL B → hook chamado 1x com (name, A, B); Refresh 3 igual → não chama; row nova (ausente no snapshot anterior) → não chama.
    - Refresh sem mudança efetiva loga em Debug (não INFO) — refresh periódico não pode spammar.
    - gatewayctl: `--url` e `--clear-url` juntos → exit 2; `--url ftp://x` → exit 2 com mensagem de validação; `--url` válido sozinho (sem outros flags) é uma atualização válida.
  </behavior>
  <action>
1. Migration `gateway/db/migrations/0038_upstreams_url_override.sql` (goose, mesmo estilo de 0009 com `-- +goose StatementBegin/End` e `SET search_path = ai_gateway, public;`):
   Up: `ALTER TABLE ai_gateway.upstreams ADD COLUMN IF NOT EXISTS url_override TEXT NULL;` + `COMMENT ON COLUMN ai_gateway.upstreams.url_override IS 'URL efetiva sobrescrita em runtime (hot-reload, sem restart). Quando NULL usa os.Getenv(url_env). Setada por gatewayctl upstreams update --url; limpa com --clear-url.';` + `DROP TRIGGER IF EXISTS upstreams_update_notify` e recriar com o MESMO corpo do 0009 acrescentando `OR NEW.url_override IS DISTINCT FROM OLD.url_override` e também `OR NEW.tier_priority IS DISTINCT FROM OLD.tier_priority` (lacuna atual: tier_priority não dispara reload). Não mexer na função notify_upstreams_changed nem no trigger insert/delete.
   Down: recriar o trigger exatamente como no 0009 (sem url_override/tier_priority) e `ALTER TABLE ai_gateway.upstreams DROP COLUMN IF EXISTS url_override;`.
   Compatibilidade com binário antigo: ADD COLUMN NULL não altera os SELECTs de colunas explícitas do binário atual, e o trigger só ganha condições a mais — confirmar lendo queries/upstreams.sql que nenhuma query usa `SELECT *` em upstreams (registrar resultado no SUMMARY).
   Adicionar "0038_upstreams_url_override.sql" na lista de gateway/internal/db/migrate_test.go:79.

2. `gateway/db/queries/upstreams.sql`: acrescentar `url_override` (depois de `url_env`) nos 3 SELECTs (ListEnabledUpstreams, ListAllUpstreams, GetUpstreamByName). Query nova `-- name: SetUpstreamURLOverride :exec` → `UPDATE ai_gateway.upstreams SET url_override = sqlc.narg('url_override')::text, updated_at = NOW() WHERE name = sqlc.arg('name');` (narg = permite NULL para limpar; comentário dizendo que dispara NOTIFY via trigger 0038). Instalar sqlc NA MESMA VERSÃO do CI: `go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0` e rodar `cd gateway && $(go env GOPATH)/bin/sqlc generate`. Commitar o gen/ resultante. Ajustar qualquer stub de teste que construa `gen.ListEnabledUpstreamsRow` posicionalmente (grep no pacote upstreams e cmd/gatewayctl).

3. `gateway/internal/upstreams/url.go`: `func ValidateUpstreamURL(s string) error` — url.Parse sem erro, Scheme ∈ {http, https}, Host != "". Comentário pt-BR explicando que é a mesma regra dos constructors de proxy.

4. `loader.go`:
   - snapshot ganha `parsed map[string]*url.URL` (preenchido no Refresh e no NewLoaderInMemory a partir de cfg.URL quando ValidateUpstreamURL passa).
   - Refresh: `effective := ""`; se `r.UrlOverride.Valid && r.UrlOverride.String != ""`: se ValidateUpstreamURL ok → effective = override; senão WARN "upstream url_override invalid; falling back to url_env" (fields upstream, url_override, status "invalid_url_override"). Se effective vazio → `os.Getenv(r.UrlEnv)`. Se ainda vazio → manter WARN/skip atual. Adicionar campo `URLSource string` ("override"|"env") em UpstreamConfig com tag json `url_source` (visível em /v1/health/upstreams se esse endpoint serializa UpstreamConfig — não mudar mais nada lá).
   - Hook: `func (l *Loader) OnURLChange(fn func(name, oldURL, newURL string))` guardado em `atomic.Pointer` (ou campo setado antes do primeiro reload, documentado). Após `l.snap.Store(s)`, para cada nome presente no snapshot anterior E no novo com URL diferente → log INFO "upstream effective url changed" (upstream, old_url, new_url, url_source) e chama o hook. Rows removidas/novas não chamam.
   - Log "upstreams refreshed" vira INFO só se algo mudou (conjunto de nomes ou qualquer URL/tier/enabled diferente vs snapshot anterior — comparar campos relevantes; manter simples); caso contrário Debug. Métrica UpstreamsReloadTotal inalterada.
   - `func (l *Loader) TargetURL(name string) (*url.URL, bool)` lock-free via snap.Load().parsed.
   - NÃO tocar OverrideTier0/RestoreTier0/Tier0OverrideURL/tier0Override.

5. `gateway/cmd/gatewayctl/upstreams.go` runUpstreamsUpdate: flags `--url` (string) e `--clear-url` (bool). Mutuamente exclusivos (exit 2). `--url` validado com upstreams.ValidateUpstreamURL antes de abrir pool (exit 2). Se só url/clear-url foram passados, não chamar UpdateUpstreamAdmin (ou chamar — tanto faz desde que nenhum campo mude; preferir pular para não gerar NOTIFY duplo). Chamar `q.SetUpstreamURLOverride(ctx, gen.SetUpstreamURLOverrideParams{Name, UrlOverride: pgtype.Text{String:u, Valid:true}})` ou Valid:false para clear. Imprimir "upstream X url_override set to Y" / "cleared". runUpstreamsList: coluna `URL_OVERRIDE` após `URL_ENV` ("-" quando NULL). Atualizar o comentário do usage/help da subcomando. Para testabilidade, extrair a validação de flags numa função pura (`parseUpstreamsUpdateFlags(args) (opts, exitCode)` ou similar) e testá-la em upstreams_test.go sem DB.

Testes em loader_override_test.go usando stub de loaderQueries (padrão já existente em loader_test.go) e t.Setenv para as envs.
  </action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix/gateway && $(go env GOPATH)/bin/sqlc generate && git diff --exit-code -- internal/db/gen >/dev/null; go build ./... && go test ./internal/upstreams/... ./cmd/gatewayctl/... ./internal/db/...</automated>
  </verify>
  <done>Migration 0038 up/down presente; gen/ regenerado com sqlc v1.30.0 e estável (segundo generate não muda nada); loader respeita precedência override>env com fallback em override inválido; row só-override carrega; TargetURL e OnURLChange funcionando com testes; gatewayctl aceita --url/--clear-url com validação e lista URL_OVERRIDE.</done>
</task>

<task type="auto" tdd="true">
  <name>Task 2: Proxies dinâmicos para os 4 upstreams + registro incondicional em main.go + breaker/latency reset + refresh periódico + log do label sweep</name>
  <files>gateway/internal/proxy/dynamic_target.go, gateway/internal/proxy/dynamic_target_test.go, gateway/internal/proxy/embeddings.go, gateway/internal/proxy/audio.go, gateway/internal/proxy/tts.go, gateway/internal/proxy/rerank.go, gateway/internal/breaker/breaker.go, gateway/internal/breaker/breaker_reset_test.go, gateway/internal/shed/latency.go, gateway/cmd/gateway/main.go, gateway/internal/primary/sweep.go, gateway/internal/emerg/sweep.go</files>
  <behavior>
    - httptest server A e B; proxy dinâmico (embed, rerank, tts, audio) com resolver retornando A → request chega em A; troca resolver para B → próximo request chega em B, mesma instância de proxy (sem rebuild).
    - Resolver retorna (nil,false) → RoundTrip retorna errDialFailedFallthrough SEM I/O de rede (nenhum DNS lookup); ErrorHandler não escreve resposta (fallthrough sinalizado no dispatchResult — usar o mesmo helper que fallthrough_test.go usa para afirmar isso).
    - NewEmbeddingsProxy/NewAudioProxy/NewTTSProxy/NewRerankProxy com string continuam funcionando e rejeitando URL inválida com erro (testes existentes passam sem alteração).
    - STT dinâmico mantém model-rewrite do resolver (local-stt) e o sttRetryableStatusInterceptor (upstream 500 → cascade) — cobrir com 1 teste reaproveitando o padrão de stt_model_rewrite_test.go.
    - breaker.Set.Reset(name): breaker que estava OPEN volta CLOSED e remoteOpen[name] some; nome desconhecido = no-op; forceCache NÃO é tocado (override de operador prevalece).
    - LatencyRing.Reset(): após Reset, P95() = 0 (mesma semântica de ring recém-criado).
  </behavior>
  <action>
1. `gateway/internal/proxy/dynamic_target.go` (novo):
   - `type TargetFunc func() (*url.URL, bool)`.
   - Constante `unresolvedTargetHost = "ifix-upstream-unresolved.invalid"`.
   - `dynamicTargetDirector(target TargetFunc, build func(*url.URL) func(*http.Request)) func(*http.Request)`: por request chama target(); se ok && u != nil → `build(u)(r)`; senão seta `r.URL.Scheme="http"; r.URL.Host=unresolvedTargetHost; r.Host=unresolvedTargetHost` (e strip de client auth igual BuildDirector — reaproveitar a lista clientAuthHeaders). Observação de custo: `build(u)` cria closure por request; aceitável (alocação pequena) — se quiser, cachear por ponteiro *url.URL (o loader devolve o mesmo ponteiro até o próximo Refresh) com atomic.Pointer de par {u, director}. Escolher e documentar no comentário.
   - `type unresolvedTargetRoundTripper struct{ base http.RoundTripper }` → se `r.URL.Host == unresolvedTargetHost` retorna `nil, errDialFailedFallthrough` imediatamente; senão delega. Ordem de wrap: `unresolvedTargetRoundTripper{base: fallthroughRoundTripper{base: &http.Transport{...}}}`.
   - Constructors novos que PRESERVAM transport/timeouts/ErrorHandler/interceptors/FlushInterval exatos de cada original: `NewDynamicEmbeddingsProxy(target, log, interceptors...)`, `NewDynamicAudioProxy(target, log, resolver, interceptors...)` (build = `func(u) { return BuildOpenAIWhisperDirector(u, "", resolver, "local-stt", log) }` + sttRetryableStatusInterceptor prepended), `NewDynamicTTSTargetProxy(target, log, interceptors...)` (nome diferente de NewDynamicTTSProxy que já existe para emergency_pod_tts — não mexer nele), `NewDynamicRerankProxy(target, log, interceptors...)`. Retornam `*httputil.ReverseProxy`.
   - Os 4 constructors antigos (string) passam a: validar/parsear a URL como hoje (mesmas mensagens de erro) e delegar ao dinâmico com closure constante `func() (*url.URL,bool){ return u, true }`. Assinaturas inalteradas.
   - NÃO alterar dynamic_override.go (emergency_pod_*).

2. `breaker.go`: `func (s *Set) Reset(name string)` — sob s.mu.Lock: se existe em cbs, substitui por `s.newBreaker(name)` e `delete(s.remoteOpen, name)`; log INFO "breaker reset (upstream url changed)". Não publicar evento Redis novo (HIPÓTESE a registrar no SUMMARY: outra réplica com remoteOpen para esse nome expira no Cooldown — hoje prod tem 1 réplica). Não tocar forceCache.
   `latency.go`: `func (r *LatencyRing) Reset()` — zera buf com atomic-safe write (mesma disciplina do Record: escrever slots e `r.idx.Store(0)`); documentar que corrida com Record concorrente só pode deixar 1 amostra antiga (aceitável). Se a leitura de latency.go mostrar que P95 depende de idx de forma que Reset não seja seguro, NÃO implementar e registrar como HIPÓTESE no SUMMARY.

3. `main.go`:
   - Substituir os 4 blocos condicionais (L613-620 audio, L704-711 embed-gpu, L809-816 kokoro-tts, L838-845 rerank-gpu) por registro INCONDICIONAL: `sttRoleProxies["local-stt"] = proxy.NewDynamicAudioProxy(func() (*url.URL,bool){ return loader.TargetURL("local-stt") }, log, resolver, usageInterceptor)`; idem `embedRoleProxies["embed-gpu"]` (NewDynamicEmbeddingsProxy + usageInterceptor), `ttsRoleProxies["kokoro-tts"]` (NewDynamicTTSTargetProxy SEM usageInterceptor, igual hoje), `rerankRoleProxies["rerank-gpu"]` (NewDynamicRerankProxy + usageInterceptor). Remover a variável audioRP e o `if audioRP != nil`. Manter os comentários de fase relevantes, atualizando-os ("URL resolvida por request via loader — url_override > env"). Usos de cfg.UpstreamSTTURL etc. restantes só no log de boot L182-184 (manter). Dispatcher só escolhe nomes presentes no loader, então proxy registrado de row skipped é inerte.
   - Extrair o closure inline de onReload (L520-538) para uma variável `onUpstreamsReload := func() {...}` reutilizada pelo LISTEN e pelo backstop.
   - Antes de iniciar o LISTEN: `loader.OnURLChange(func(name, oldURL, newURL string) { breakerSet.Reset(name); if r, ok := shedLatency[name]; ok { r.Reset() } })` — atenção: shedLatency é map lido/escrito no onReload; o hook roda dentro do Refresh (mesma goroutine do LISTEN ou do ticker). Para não introduzir corrida nova entre ticker e LISTEN, serializar ambos com um `sync.Mutex reloadMu` em volta de `loader.Refresh + onUpstreamsReload` no backstop e passar ao ListenAndReload um onReload que também pega o mutex? O Refresh do LISTEN acontece dentro de listen.go antes do onReload — então: o backstop deve chamar Refresh + onUpstreamsReload sob reloadMu, e o onReload do LISTEN pega reloadMu também; o hook OnURLChange só lê o map (lookup) — documentar que escrita no map só ocorre em onUpstreamsReload (já existente) e que a corrida residual LISTEN-Refresh×ticker-onReload é a mesma classe que o código atual já aceita (comentário "pruning would risk racing"). Se o executor achar a solução simples mais segura (fazer o backstop disparar via o próprio caminho LISTEN, p.ex. chamar uma função exportada `upstreams.RefreshAndNotify`), pode — desde que sem data race em `go test -race ./cmd/gateway/...` se houver teste ali.
   - Backstop periódico: goroutine com `time.NewTicker(60 * time.Second)` (constante nomeada `upstreamsBackstopRefreshInterval`) que chama loader.Refresh(ctx) (erro → WARN, mantém último snapshot) e depois onUpstreamsReload; sai em ctx.Done(). Verificado: não existe hoje refresh periódico do loader de upstreams em main.go (só LISTEN).

4. Label sweep observável (primary/sweep.go e emerg/sweep.go, sweepOrphanInstances): contar `matched := 0; for _, inst := range instances { if strings.HasPrefix(inst.Label, vastutil.PrimaryLabelPrefix) { matched++ } }` (EmergLabelPrefix no emerg), contadores destroyed/failed no loop existente, e ao final 1 único `log.Info("primary orphan sweep done", "listed", len(instances), "matched_label", matched, "orphans", len(orphans), "destroyed", destroyed, "failed", failed)` (mensagem "emerg orphan sweep done" no emerg). Caminhos de skip (ListInstances falhou / DB indisponível / querier nil) continuam como estão (já têm WARN; o caso querier nil é silencioso — acrescentar Debug é opcional). Sem mudança de cadência.

Testes: dynamic_target_test.go (2 httptest servers, troca de alvo, unresolved → errDialFailedFallthrough sem rede, STT rewrite+cascade), breaker_reset_test.go (forçar OPEN com N falhas via Execute com fn retornando erro conforme Options do teste existente, Reset → State CLOSED). Se existir teste do sweep (grep sweep_test.go em primary/emerg), adicionar asserção do log via slog handler de captura; senão não é obrigatório.
  </action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix/gateway && test -z "$(gofmt -l .)" && go build ./... && go vet ./... && go vet -tags integration ./... && go test -race ./internal/proxy/... ./internal/breaker/... ./internal/shed/... ./internal/upstreams/... ./internal/primary/... ./internal/emerg/... && go test ./...</automated>
  </verify>
  <done>Os 4 proxies resolvem alvo por request via loader.TargetURL, registrados sempre; URL ausente cascateia via errDialFailedFallthrough; constructors antigos intactos na assinatura; Reset de breaker (e ring, se seguro) disparado por mudança de URL efetiva; refresh backstop 60s; 1 log INFO por sweep com os 5 contadores; gofmt/vet/vet-integration/test verdes.</done>
</task>

<task type="auto" tdd="true">
  <name>Task 3: unified3060 flipa via url_override (ssh worker-vm + /gatewayctl) em vez de PUT no Portainer</name>
  <files>ops/vast-3060/unified3060.py, ops/vast-3060/test_unified3060.py</files>
  <behavior>
    - `build_flip_targets(ip, ports)` (pura) → lista ordenada [("local-stt","http://IP:P8000"), ("kokoro-tts","http://IP:P8021"), ("rerank-gpu","http://IP:P7998"), ("embed-gpu","http://IP:P7998")]; porta ausente em ports → KeyError/ValueError explícito (não monta URL "None").
    - `gatewayctl_update_cmd(name, url)` (pura) → argv do ssh exatamente: ["ssh","-i",SSH_KEY,"-o","BatchMode=yes","-o","ConnectTimeout=10", "root@10.10.10.50", "<remote>"] onde remote contém `docker exec $(docker ps -q -f name=ai-gateway-prod_gateway | head -1) /gatewayctl upstreams update --name <name> --url <url>` com name/url passados por shlex.quote.
    - URL não http(s) ou sem host → ValueError antes de qualquer subprocess (mesma regra do ValidateUpstreamURL Go).
  </behavior>
  <action>
1. Nova constante `UPSTREAM_PORTS = {"local-stt":"8000/tcp","kokoro-tts":"8021/tcp","rerank-gpu":"7998/tcp","embed-gpu":"7998/tcp"}` (ordem estável; comentário mapeando para as envs antigas do ENVMAP). `GATEWAY_HOST = "root@10.10.10.50"` (worker-vm; IP fixo porque o script roda como ROOT via systemd e o alias `worker-vm` só existe no ~/.ssh/config do pedro — usar o mesmo `SSH_KEY` explícito que ssh_pod já usa). `GATEWAY_CONTAINER_FILTER = "name=ai-gateway-prod_gateway"`.
2. Funções puras: `validate_url(u)` (urllib.parse: scheme in http/https e netloc não vazio), `build_flip_targets(ip, ports)`, `gatewayctl_update_cmd(name, url)` (remote com shlex.quote; o `$(docker ps ...)` fica fora do quote — é expandido no shell remoto).
3. `flip_upstreams(env, ip, ports)`: para cada alvo roda subprocess.run(argv, capture_output=True, text=True, timeout=60); returncode != 0 → `raise RuntimeError(f"gatewayctl update {name} rc={rc}: {stderr[-500:]}")`. Antes de setar, ler o estado atual com 1 chamada `/gatewayctl upstreams list` e pular rows cujo URL_OVERRIDE já é o alvo (idempotência, log "url_override de X ja correto") — se o parse da tabela for frágil, pode pular a idempotência e sempre setar (o UPDATE com mesmo valor não dispara NOTIFY porque o trigger usa IS DISTINCT FROM; registrar a escolha). Após setar, `time.sleep(5)` para o LISTEN aplicar (<2s documentado) antes do loop validate_edge existente. Retorna lista de mudanças (mesmo contrato de flip_stack). Log "upstreams flipados via url_override: [...]".
4. cmd_start: trocar `flip_stack(env, ip, ports)` por `flip_upstreams(env, ip, ports)` mantendo o try/except → `fail("flip upstreams: ...")` e o bloco validate_edge/rollback intacto. Decisão sobre o PUT: REMOVER do caminho normal; manter `flip_stack` como função de rollback manual acessível por subcomando novo `flip-stack-legacy` no dispatcher de argv do `__main__` (só quem invocar explicitamente), com docstring "LEGADO: recria a task do gateway; usar só se url_override indisponível (ex. rollback da migration 0038)". Sem flag de env no caminho automático (evita reintroduzir restart silenciosamente). Atualizar docstring do módulo (L16-17 "flipa 4 envs do stack 38" → "seta url_override das 4 rows via gatewayctl") e mencionar que PORTAINER_API_KEY só é necessário para o legacy.
5. Testes em test_unified3060.py: classes FlipTargetsTest e GatewayctlCmdTest cobrindo o behavior acima (sem I/O; não chamar subprocess). Manter os testes existentes passando.

NÃO executar o script contra prod, NÃO rodar ssh real no worker-vm (deploy é do orquestrador).
  </action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix/ops/vast-3060 && python3 -m unittest -v test_unified3060 && python3 -c "import ast,sys; ast.parse(open('unified3060.py').read())" && ! grep -n "flip_stack(env, ip, ports)" unified3060.py | grep -v "def \|legacy"</automated>
  </verify>
  <done>cmd_start usa flip_upstreams (4 × gatewayctl update --url via ssh root@10.10.10.50); flip_stack só acessível via subcomando flip-stack-legacy; funções puras testadas; testes antigos verdes.</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| ops-claude(root) → worker-vm(root) via ssh | unified3060 executa comando remoto no host do gateway prod |
| operador → ai_gateway.upstreams.url_override | coluna passa a determinar para onde o gateway envia tráfego (inclui dados de tenants) |
| pod Vast (IP público) ← gateway | alvo dinâmico recebe payloads STT/TTS/embed/rerank |

## STRIDE Threat Register

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-uru-01 | Tampering | url_override (redirecionar tráfego p/ host arbitrário) | mitigate | Escrita só via gatewayctl (DSN do container) / acesso DB admin — mesma superfície que já controla tier/enabled; ValidateUpstreamURL restringe a http/https+host; loader rejeita inválido e cai na env |
| T-uru-02 | Tampering/Injection | gatewayctl_update_cmd (shell remoto) | mitigate | name/url passados por shlex.quote; url validada antes; names vêm de constante UPSTREAM_PORTS, IP de ports da API Vast |
| T-uru-03 | Denial of Service | URL efetiva vazia/ inválida em request | mitigate | unresolvedTargetRoundTripper → errDialFailedFallthrough → cascade tier-1 (sem 503 terminal, sem panic) |
| T-uru-04 | Denial of Service | NOTIFY perdido deixa alvo velho | mitigate | backstop Refresh 60s |
| T-uru-05 | Information Disclosure | logs com URL do pod | accept | URL = IP:porta Vast, já logada hoje por unified3060 e pelo prober; sem credencial na URL |
| T-uru-06 | Elevation | ssh root com key sem passphrase | accept | padrão existente (ssh_pod, CLAUDE.md); nenhuma credencial nova |
| T-uru-SC | Tampering | `go install sqlc@v1.30.0` | accept | mesma ferramenta/versão pinada do CI (build-gateway.yml SQLC_VERSION); nenhuma dependência nova no go.mod nem pacote npm/pip |
</threat_model>

<verification>
- `cd gateway && test -z "$(gofmt -l .)" && go build ./... && go vet ./... && go vet -tags integration ./... && go test ./...`
- `cd gateway && sqlc generate && git diff --exit-code internal/db/gen` (codegen estável, igual CI)
- `cd ops/vast-3060 && python3 -m unittest -v test_unified3060`
- Integração local (Docker via sudo) é do orquestrador, não do executor: rodar `go test -tags integration ./internal/integration_test/...` incluindo migrate up/down da 0038.
</verification>

<success_criteria>
- Nenhum caminho automático de unified3060 faz PUT no Portainer stack 38.
- Trocar url_override muda o alvo dos 4 proxies no request seguinte (teste httptest A→B) sem rebuild.
- Mudança de URL efetiva reseta breaker da row.
- Sweep primary/emerg loga 1 INFO por execução com listed/matched_label/orphans/destroyed/failed.
- SUMMARY registra: ordem de deploy, rollback, e as HIPÓTESES abaixo com o resultado do que foi verificado.

SUMMARY deve conter (seção "Deploy — fora do executor"):
1. A migration 0038 tem que estar aplicada ANTES do binário novo servir (ele SELECTa url_override; AI_GATEWAY_MIGRATE_ON_BOOT=false). O container atual não embute a 0038, então o `migrate up` roda a partir da imagem NOVA. Ordem: (a) build imagem nova; (b) `docker run --rm --env-file <env do stack 38> <imagem-nova> /gatewayctl migrate up` no worker-vm (binário antigo segue OK: SELECTs explícitos + coluna NULL); (c) PUT stack 38 com a imagem nova (ÚLTIMO restart planejado); (d) seed das 4 rows: `/gatewayctl upstreams update --name <X> --url <URL atual da env>` (sem efeito no tráfego, prepara o terreno); (e) deploy do unified3060.py em /opt/vast-3060/; (f) observar próximo start 07h: nenhum restart da task (`docker service ps ai-gateway-prod_gateway`), log "upstream effective url changed" + "breaker reset".
2. Rollback: `/gatewayctl upstreams update --clear-url` nas 4 rows (volta a env) → `unified3060.py flip-stack-legacy` se preciso → imagem anterior no stack 38 → `gatewayctl migrate down` (0038 down) só depois do binário antigo rodando.

HIPÓTESES a validar e marcar no SUMMARY (não são fatos):
- HIPÓTESE: root do ops-claude consegue `ssh -i /home/pedro/.ssh/id_ed25519 root@10.10.10.50` (resolve: `sudo ssh -i ... -o BatchMode=yes root@10.10.10.50 true` pelo orquestrador).
- HIPÓTESE: /gatewayctl no container prod tem DSN e permissão de UPDATE em ai_gateway.upstreams (resolve: rodar `upstreams list` via docker exec).
- HIPÓTESE: reset de breaker local basta com 1 réplica (resolve: `docker service ls` replicas do gateway).
</success_criteria>

<output>
Create `.planning/quick/260930-uru-hot-reload-url-upstreams-pod-3060-sem-re/260930-uru-SUMMARY.md` when done
</output>
