---
quick_id: 260704-ubt
slug: kokoro-tts-upstream
type: quick
date: 2026-07-04
autonomous: true
files_modified:
  - gateway/internal/config/config.go
  - gateway/internal/config/config_test.go
  - gateway/cmd/gateway/main.go
  - gateway/db/migrations/0032_replace_piper_with_kokoro_tts.sql
---

<objective>
Substituir o TTS tier-1 morto (`voice-api-piper`, sumiu do vps-ifix-vm) por `kokoro-tts` — um Kokoro-FastAPI OpenAI-compatível (`POST /v1/audio/speech`) já rodando no worker-vm (stack Portainer 42, overlay `ai-gateway`, DNS `kokoro-tts:8880`). Diferente do piper (que precisa de adapter form `/tts`), o Kokoro fala OpenAI 1:1 → usa o `NewTTSProxy` (passthrough), igual ao tier-0.
Simetria final: pod GPU up → tier-0 Chatterbox; pod down → tier-1 Kokoro (CPU, ~real-time).
</objective>

<context>
Já validado ao vivo (NÃO reimplementar, só wirar):
- Kokoro-FastAPI responde `POST http://kokoro-tts:8880/v1/audio/speech` {model,input,voice,response_format} → 200 WAV 24kHz PT-BR (voz `pf_dora`), ~1.1× real-time.
- `NewTTSProxy(base, log)` usa `BuildDirector` (path-join base+inbound), então a URL base é `http://kokoro-tts:8880` e o inbound `/v1/audio/speech` resolve certo.
- `probe.go` (case "tts") já trata qualquer upstream != "voice-api-piper" com o probe OpenAI `/v1/audio/speech` (branch else) → `kokoro-tts` funciona sem tocar probe.
- Schema `ai_gateway.upstreams(name, role, tier, url_env, auth_bearer_env)` com UNIQUE(role,tier). piper ocupa ('tts',1). Última migration = 0031.
NÃO fazer build/push/deploy nem rodar migration no DB — isso é passo de ops separado (orquestrador). Só código + testes.
</context>

<tasks>

<task type="auto">
  <name>Task 1: config env UPSTREAM_TTS_KOKORO_URL</name>
  <files>gateway/internal/config/config.go, gateway/internal/config/config_test.go</files>
  <action>
    Em `config.go`: adicionar campo `UpstreamTTSKokoroURL string` (comentário: `// UPSTREAM_TTS_KOKORO_URL (tier-1 Kokoro-FastAPI OpenAI-compat TTS, replaces dead Piper)`) logo após `UpstreamTTSPiperURL`. No load (junto de `UpstreamTTSPiperURL: os.Getenv("UPSTREAM_TTS_PIPER_URL")`), adicionar `UpstreamTTSKokoroURL: os.Getenv("UPSTREAM_TTS_KOKORO_URL")`.
    Em `config_test.go`: estender o teste de parse de env pra cobrir `UPSTREAM_TTS_KOKORO_URL` → `UpstreamTTSKokoroURL` (setenv + assert), no mesmo padrão do teste do PIPER se existir; senão adicionar um caso mínimo.
  </action>
  <verify><automated>cd gateway && gofmt -l internal/config/config.go internal/config/config_test.go (vazio) && go build ./... && go test ./internal/config/ -count=1</automated></verify>
  <done>Campo + getenv + teste de parse do UPSTREAM_TTS_KOKORO_URL.</done>
</task>

<task type="auto">
  <name>Task 2: wirar kokoro-tts no main.go (NewTTSProxy passthrough)</name>
  <files>gateway/cmd/gateway/main.go</files>
  <action>
    No bloco de `ttsRoleProxies` (perto de onde registra `voice-api-piper` via `NewPiperTTSAdapter`), adicionar registro do `kokoro-tts` usando o proxy passthrough OpenAI `NewTTSProxy` (NÃO o adapter piper), gate por env não-vazio:
    ```go
    if cfg.UpstreamTTSKokoroURL != "" {
        kokoroRP, kerr := proxy.NewTTSProxy(cfg.UpstreamTTSKokoroURL, log)
        if kerr != nil {
            log.Warn("build kokoro-tts proxy", "err", kerr)
        } else {
            ttsRoleProxies["kokoro-tts"] = kokoroRP
        }
    }
    ```
    Manter o bloco existente do `voice-api-piper` intacto (fica inerte: UPSTREAM_TTS_PIPER_URL não será setado). Atualizar o comentário do bloco tts pra mencionar que o tier-1 agora é kokoro-tts (OpenAI passthrough) e o piper está deprecado/removido do DB.
  </action>
  <verify><automated>cd gateway && gofmt -l cmd/gateway/main.go (vazio) && go build ./...</automated></verify>
  <done>ttsRoleProxies["kokoro-tts"] registrado via NewTTSProxy quando UPSTREAM_TTS_KOKORO_URL setado; piper mantido inerte.</done>
</task>

<task type="auto">
  <name>Task 3: migration 0032 — piper→kokoro-tts (mesmo slot tts,1)</name>
  <files>gateway/db/migrations/0032_replace_piper_with_kokoro_tts.sql</files>
  <action>
    Criar migration goose 0032. Como UNIQUE(role,tier) impede 2 rows ('tts',1), fazer UPDATE in-place da row piper → kokoro-tts (reversível), NÃO delete+insert:
    ```sql
    -- +goose Up
    -- +goose StatementBegin
    SET search_path = ai_gateway, public;
    -- Replace the dead voice-api-piper tier-1 TTS (removed from vps-ifix-vm) with
    -- kokoro-tts: a Kokoro-FastAPI OpenAI-compatible /v1/audio/speech server on the
    -- worker-vm ai-gateway overlay. Same (role,tier)=('tts',1) slot; url_env repoints
    -- to UPSTREAM_TTS_KOKORO_URL. No-op if already migrated.
    UPDATE ai_gateway.upstreams
       SET name = 'kokoro-tts', url_env = 'UPSTREAM_TTS_KOKORO_URL'
     WHERE name = 'voice-api-piper';
    -- +goose StatementEnd

    -- +goose Down
    -- +goose StatementBegin
    SET search_path = ai_gateway, public;
    UPDATE ai_gateway.upstreams
       SET name = 'voice-api-piper', url_env = 'UPSTREAM_TTS_PIPER_URL'
     WHERE name = 'kokoro-tts';
    -- +goose StatementEnd
    ```
    Seguir o header-comment style das outras migrations (ex.: 0024). Se houver teste de migration up/down count (ex.: integration_test/migration_00XX_test.go) que fixe o HEAD, verificar que renomear NÃO altera contagem de rows (é UPDATE, row-neutral) — não deve quebrar os testes de Down-count; se algum teste referenciar explicitamente o HEAD número, ajustar minimamente.
  </action>
  <verify><automated>cd gateway && test -f db/migrations/0032_replace_piper_with_kokoro_tts.sql && grep -q "kokoro-tts" db/migrations/0032_replace_piper_with_kokoro_tts.sql && go build ./...</automated></verify>
  <done>Migration 0032 criada (UPDATE reversível piper→kokoro-tts), row-neutral.</done>
</task>

<task type="auto">
  <name>Task 4: suíte verde (sem regressão)</name>
  <files>gateway (build + test)</files>
  <action>
    Rodar `cd gateway && gofmt -l` (vazio nos arquivos tocados), `go build ./...`, `go test ./internal/config/ ./internal/proxy/ -count=1`. Se existir teste de migração que conte HEAD/rows e quebrar por causa da 0032, corrigir minimamente (a 0032 é row-neutral, então contagens de Down não devem mudar). Incluir a saída no SUMMARY.
  </action>
  <verify><automated>cd gateway && go build ./... && go test ./internal/config/ ./internal/proxy/ -count=1</automated></verify>
  <done>gofmt limpo, build ok, testes proxy+config verdes, zero regressão.</done>
</task>

</tasks>

<verification>
- config expõe UpstreamTTSKokoroURL ← UPSTREAM_TTS_KOKORO_URL (com teste).
- main.go registra ttsRoleProxies["kokoro-tts"] via NewTTSProxy quando env setado.
- migration 0032 renomeia piper→kokoro-tts (reversível, row-neutral).
- build + gofmt + go test (config, proxy) verdes.
</verification>

<success_criteria>
Gateway passa a rotear o TTS tier-1 pro Kokoro-FastAPI OpenAI-compat (`kokoro-tts:8880`) quando UPSTREAM_TTS_KOKORO_URL está setado, substituindo o piper morto — via passthrough NewTTSProxy, migration reversível, sem regressão. SEM build/push/deploy/migrate (ops separado).
</success_criteria>

<output>
Criar `.planning/quick/260704-ubt-kokoro-tts-upstream/260704-ubt-SUMMARY.md` com o diff-resumo, a migration, e a saída dos testes.
</output>
