# SEED-004 — OpenRouter fallback: per-upstream model rewriting

**Planted:** 2026-05-24
**Discovered during:** Zero-spend UAT batch session 2026-05-24 (Phase 02/04/05 closeout + post-token-apply chat fallback test)
**Status:** seed — not yet promoted to phase
**Related:** Phase 03 SC-1 live UAT deferral (`03-VERIFICATION.md`); Phase 02 step 7 chat E2E deferred; Phase 05 SC-1 PARTIAL (vegeta 2026-05-13)

## Problem

The OpenRouter tier-1 fallback chain is wired end-to-end EXCEPT for one missing piece: model-name rewriting per upstream. Today:

- Gateway request: `POST /v1/chat/completions {"model":"qwen", ...}`
- `models/rewrite.go` Handler rewrites alias `qwen` → target `qwen` (per `model_aliases` table seed, single-row PK on `alias`)
- Dispatcher selects tier-1 `openrouter-chat` (because tier-0 `local-llm` breaker OPEN)
- `openrouter_director.go` BuildOpenRouterDirector:
  - ✓ Strips client auth headers
  - ✓ Injects `Authorization: Bearer $UPSTREAM_LLM_OPENROUTER_AUTH_BEARER`
  - ✓ Injects `provider: {order:[novita], allow_fallbacks:false}` into body
  - ✓ Injects `stream_options.include_usage=true` if streaming
  - ✗ Does NOT rewrite `model` field — sends `qwen` literal
- OpenRouter `https://openrouter.ai/api/v1/chat/completions` returns **HTTP 404 Next.js "Not Found"** because `qwen` is not a valid OpenRouter model slug

Wave 0 Gate A (2026-04-20, `03-WAVE0-GATES.md`) defined `UPSTREAM_LLM_OPENROUTER_MODEL=qwen/qwen3.5-27b` as the env var operator must set. Plan 03-06 implementation **never wired it** — only present in `.env.example`, `.env.portainer.dev`, and a single line in `interceptor_usage.go:259` (cost lookup).

## Why It Was Missed

- Phase 03 SC-1 was deferred to live UAT (no SSH-able pod, no live OpenRouter key in dev env at the time)
- Integration tests (`gateway/internal/integration_test/*_failover_test.go`) used a fake upstream that accepts any model name in the JSON body
- Phase 04/05 vegeta tests (2026-05-13) hit tier-1 because local-llm was saturated, but the spec only checked HTTP status (502 from fake upstream counted as breaker-correct behavior — same 502 OpenRouter returns when the actual model is unknown happens to look identical at the metrics layer)

## Verified Live 2026-05-24

| Path | Request | Result |
|------|---------|--------|
| Direct curl (ops-claude → OpenRouter) | `{"model":"qwen/qwen3.5-27b","provider":{"order":["novita"]}...}` | HTTP 200, response from DeepInfra (Novita unavailable that minute), real Qwen completion |
| Gateway-path (browser → ai-gateway-dev → OpenRouter) | `{"model":"qwen", ...}` (gateway forwards literal) | HTTP 404, OpenRouter Next.js "Not Found" HTML (142 KB response body) |

Also surfaced: env var `UPSTREAM_LLM_OPENROUTER_URL` MUST end at `/api` (not `/api/v1`). `BuildDirector` deliberately preserves `r.URL.Path = /v1/chat/completions` because pod routes mirror gateway 1:1. With URL `https://openrouter.ai/api/v1`, path concat would produce `/api/v1/v1/chat/completions` (double `/v1`). Set `UPSTREAM_LLM_OPENROUTER_URL=https://openrouter.ai/api` to compose to correct upstream URL.

## Scope of Fix

Two options, both ship in same phase:

### Option A — Director-level model rewrite (simpler, scoped)

In `gateway/internal/proxy/openrouter_director.go`, add model-rewrite step before `injectProviderOrder`:

```go
// Read UPSTREAM_LLM_OPENROUTER_MODEL from config; rewrite body.model to it.
```

Pros: localized change, easy regression test.
Cons: scattered config — each upstream director that needs per-upstream model name must implement separately (openai-whisper + openai-embed will need same).

### Option B — Per-upstream model resolution in dispatcher (proper fix)

Schema migration: change `model_aliases` PK from `(alias)` to `(alias, upstream)`. Seed adds:
```sql
INSERT INTO ai_gateway.model_aliases (alias, upstream, target) VALUES
  ('qwen',    'llm',   'qwen'),           -- existing (tier-0 local-llm)
  ('qwen',    'llm',   'qwen/qwen3.5-27b'), -- NEW (tier-1 openrouter-chat) — wait, this collides on (alias, upstream)
```

Actually composite would be (alias, upstream-name) so:
```sql
PRIMARY KEY (alias, upstream_name)  -- upstream_name = 'local-llm' | 'openrouter-chat'
```

Then `models/rewrite.go` Handler picks per-resolved-upstream target. Dispatcher already knows which upstream it's dispatching to (logs `upstream=openrouter-chat`).

Pros: schema-driven, extensible to whisper + embed + future tier-1 providers without code changes.
Cons: requires schema migration + resolver refactor + Handler signature change + per-upstream-name lookup path.

**Recommendation:** Option B. It's the right fix and Phase 03/07 dashboard will need per-upstream model visibility anyway.

## Out of Scope for This Seed

- Switching primary LLM to Qwen 3.6 on OpenRouter — Qwen 3.6 not yet published on OpenRouter (per PROJECT.md decision row, drift accepted)
- Fixing the same gap for `openai-whisper` (model `whisper-1` rewrite) and `openai-embed` (`text-embedding-3-small` rewrite) — likely affected by same bug; verify and bundle in same phase
- Phase 1 obsoletion (already handled by commit `82bedc0`)

## Acceptance Criteria (when promoted to phase)

- SC-1 (LIVE): Force `local-llm` breaker OPEN; POST `/v1/chat/completions {"model":"qwen"}` returns HTTP 200 with real Qwen completion from OpenRouter (provider Novita preferred, DeepInfra acceptable fallback)
- SC-2: Same for `openai-whisper` if affected (force `local-stt` OPEN, POST audio, expect 200 from OpenAI)
- SC-3: Same for `openai-embed` if affected
- Regression test: dispatcher test asserts model name rewrite per resolved-upstream
- Phase 03 SC-1 deferral closes ("OpenRouter env vars not set + model rewrite missing" → both fixed)
- Phase 02 step 7 chat E2E PASS (cascades from SC-1)
- Phase 05 SC-1 full overflow PASS (cascades from SC-1)

## Cost to Fix

- Option A: ~30 min code + test + UAT
- Option B: ~2-3 hours (schema migration + resolver refactor + Handler signature + 3 director consumers + 3 regression tests + live UAT)

Zero Vast spend either way — all testable via existing live ai-gateway-dev + OpenRouter direct.

## Notes

- OpenRouter key is live in `/opt/ai-gateway-dev/.env` (since 2026-05-24); applied env passthrough in `docker-compose.yml`. Backups: `.env.bak-pre-openrouter-20260524` + `docker-compose.yml.bak-pre-openrouter-20260524`.
- Key value stored in `~/.claude/CLAUDE.md` (local-only, NOT in git). See memory `openrouter-token-and-stack-location.md`.
- Stack is operator-managed `docker compose` at `/opt/ai-gateway-dev/` on `vps-ifix-vm` (NOT Portainer despite older docs).
