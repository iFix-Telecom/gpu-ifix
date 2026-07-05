---
quick_id: 260704-ubt
slug: kokoro-tts-upstream
type: quick
date: 2026-07-05
status: complete
tasks_completed: 4
files_created:
  - gateway/db/migrations/0032_replace_piper_with_kokoro_tts.sql
files_modified:
  - gateway/internal/config/config.go
  - gateway/internal/config/config_test.go
  - gateway/cmd/gateway/main.go
  - gateway/internal/db/migrate_test.go
commits:
  - b821620
  - 8935425
  - c9aba2b
---

# Quick 260704-ubt: Wire kokoro-tts as tier-1 TTS upstream (replaces dead Piper) — Summary

Gateway now routes tier-1 TTS to a Kokoro-FastAPI OpenAI-compatible server
(`kokoro-tts:8880` on the worker-vm `ai-gateway` overlay) via the passthrough
`NewTTSProxy`, replacing the dead `voice-api-piper` that was removed from
vps-ifix-vm. Config + wiring + reversible DB migration, code-only — no build,
push, deploy, or live migration run.

## Tasks

### Task 1 — config `UPSTREAM_TTS_KOKORO_URL` (commit b821620)
- `config.go`: added `UpstreamTTSKokoroURL string` field right after
  `UpstreamTTSPiperURL` (doc: tier-1 Kokoro-FastAPI OpenAI-compat TTS,
  passthrough via NewTTSProxy) + `os.Getenv("UPSTREAM_TTS_KOKORO_URL")` in the
  Load literal. Marked `UpstreamTTSPiperURL` DEPRECATED in its doc comment.
- `config_test.go`: added `TestLoad_UpstreamTTSKokoroURL` — asserts empty
  default when unset (fallback disabled, mirrors Piper optional semantics) and
  verbatim parse of `http://kokoro-tts:8880` when set.

### Task 2 — wire kokoro-tts in main.go (commit 8935425)
- In the `ttsRoleProxies` block: registered `ttsRoleProxies["kokoro-tts"]` via
  `proxy.NewTTSProxy(cfg.UpstreamTTSKokoroURL, log)` gated on non-empty env
  (OpenAI passthrough, NOT the Piper adapter). `tts.go` / `probe.go` untouched
  (probe.go already handles non-piper TTS via its OpenAI `/v1/audio/speech`
  branch).
- The existing `voice-api-piper` `NewPiperTTSAdapter` block was left intact but
  is inert in prod (`UPSTREAM_TTS_PIPER_URL` unset) and marked DEPRECATED.
- Updated the tts block comment to state tier-1 is now kokoro-tts (OpenAI
  passthrough) and Piper is deprecated/DB-repointed by migration 0032.

### Task 3 — migration 0032 piper→kokoro-tts (commit c9aba2b)
- `0032_replace_piper_with_kokoro_tts.sql`: reversible in-place `UPDATE` of the
  existing tier-1 row (`UNIQUE(role,tier)` forbids a second `('tts',1)` row, so
  UPDATE not delete+insert). Up: `voice-api-piper`→`kokoro-tts`,
  url_env→`UPSTREAM_TTS_KOKORO_URL`. Down: exact reverse. Row-neutral, no-op if
  already migrated. Header-comment style matches 0024.
- `migrate_test.go`: appended `0032_replace_piper_with_kokoro_tts.sql` to the
  `TestEmbedFS_HasAllMigrations` `want` slice (it enumerates every embedded
  migration filename + asserts count — would have gone red otherwise). This is
  the only migration-referencing test that pins the set; the 0032 UPDATE is
  row-neutral so no Down/row-count assertion changed.

### Task 4 — green suite (no code change needed)
Verification only; nothing broke that required a fix.

## Migration 0032 (full)

```sql
-- +goose Up
SET search_path = ai_gateway, public;
UPDATE ai_gateway.upstreams
   SET name = 'kokoro-tts', url_env = 'UPSTREAM_TTS_KOKORO_URL'
 WHERE name = 'voice-api-piper';
-- +goose Down
SET search_path = ai_gateway, public;
UPDATE ai_gateway.upstreams
   SET name = 'voice-api-piper', url_env = 'UPSTREAM_TTS_PIPER_URL'
 WHERE name = 'kokoro-tts';
```

## Verification output

```
=== gofmt -l (touched files) ===
(empty = clean)  # config.go, config_test.go, main.go, migrate_test.go, 0032*.sql

=== go build ./... ===
BUILD_OK

=== go test ./internal/config/ ./internal/proxy/ -count=1 ===
ok  github.com/ifixtelecom/gpu-ifix/gateway/internal/config  0.019s
ok  github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy   13.705s

=== go test ./internal/db/ -count=1 (touched migrate_test.go) ===
ok  github.com/ifixtelecom/gpu-ifix/gateway/internal/db      0.007s
```

## Deviations from Plan

None beyond one plan-anticipated adjustment: the plan flagged that a
migration-enumerating test might need a minimal touch — `migrate_test.go`'s
`TestEmbedFS_HasAllMigrations` `want` slice required appending the new filename.
Row-count/Down assertions were unaffected (0032 is a row-neutral UPDATE), as the
plan predicted.

## Ops follow-up (NOT done here — orchestrator/ops)
- Build + push + deploy the gateway image.
- Run `gatewayctl migrate up` against the live DB (applies 0032).
- Set `UPSTREAM_TTS_KOKORO_URL=http://kokoro-tts:8880` in the worker-vm gateway
  stack env.

## Self-Check: PASSED
- FOUND: gateway/db/migrations/0032_replace_piper_with_kokoro_tts.sql
- FOUND commit b821620 (config)
- FOUND commit 8935425 (main.go wiring)
- FOUND commit c9aba2b (migration + embed test)
