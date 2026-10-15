---
phase: 23
slug: haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
status: draft
nyquist_compliant: true
wave_0_complete: false
created: 2026-10-14
---

# Phase 23 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution. Source: 23-RESEARCH.md § Validation Architecture.

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | Go `testing` + testify; testcontainers-go (integration); vitest (dashboard) |
| **Config file** | `.github/workflows/build-gateway.yml`, `build-dashboard.yml` |
| **Quick run command** | `cd gateway && gofmt -l . && go test ./internal/proxy/ -run 'Anthropic\|KeyPool\|RetryableStatus\|TTSOpenRouter' -count=1` |
| **Full suite command** | `gofmt -l . && go test ./gateway/... ./pkg/openai/... -count=1 -race && go test -tags=integration ./gateway/internal/integration_test/... -count=1` ; `cd dashboard && npx vitest run` |
| **Estimated runtime** | ~180 seconds |

## Sampling Rate

- **After every task commit:** quick run + `gofmt -l .`
- **After every plan wave:** full suite incl. `-tags integration`
- **Before `/gsd:verify-work`:** full suite green + live smoke dos aliases + 24 h de `audit_log` sem 400 de prefill/sampling
- **Max feedback latency:** 180 seconds

## Per-Task Verification Map

(Preenchido pelo planner em 2026-10-14. Comandos rodam a partir de `gateway/` salvo indicação; integração via `sudo env PATH=/usr/local/go/bin:$PATH HOME=$HOME GOCACHE=$HOME/.cache/go-build GOPATH=$HOME/go go test -tags=integration ...`.)

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 23-01-T1 | 01 | 1 | HAIKU-01 | T-23-01 | prefill/sampling só em alvo Anthropic; JSON inválido intacto | unit | `cd gateway && go test ./internal/proxy/ -run TestAnthropicCompat -count=1` | ❌ W0 (cria) | ⬜ |
| 23-01-T2 | 01 | 1 | HAIKU-01, HAIKU-07 | T-23-02 | PASS 5 DEPOIS do PASS 4 no openrouter-chat (breakpoint no turno real, sintético sem cache_control); não-Anthropic byte-idêntico | unit | `cd gateway && go test ./internal/proxy/ -count=1` | ❌ W0 (cria) | ⬜ |
| 23-02-T1/T3 | 02 | 2 | HAIKU-07 | T-23-04..06 | hotfix isolado (diff só 23-01, Pending 0), 0 prefill 400 | manual/live | `curl /health` + query audit_log (evidence) | n/a | ⬜ |
| 23-03-T1 | 03 | 2 | HAIKU-03/04/05/09 | T-23-07..09, T-23-66 | 0041/0042 com ON CONFLICT (name) e RAISE EXCEPTION de posição | grep + build | `grep -c 'RAISE EXCEPTION' db/migrations/0041_haiku_chain.sql && go build ./...` | ❌ W0 (cria) | ⬜ |
| 23-03-T2 | 03 | 2 | HAIKU-03/04/05/09 | T-23-66 | Up/Down simétricos, prioridades TTS divergentes, posições (llm,1,10)/(llm,1,20) ocupadas falham com msg própria, homônimo com definição divergente falha; anthropic-haiku/openrouter-deepseek nascem enabled=false e o Loader não os vê; bumps db.Down +2 | integration | `go test -tags=integration ./internal/integration_test/ -run 'Migration\|Migrate'` | ❌ W0 (cria migration_0041_test.go) | ⬜ |
| 23-04-T1 | 04 | 2 | HAIKU-03, HAIKU-06 | T-23-11..14 | cascata por status pré-byte; 400 não cascateia; RES-08 intocado | unit | `go test ./internal/proxy/ -run 'RetryableStatus\|DispatchPinned\|Sensitive' -race` | ❌ W0 (cria) | ⬜ |
| 23-04-T2 | 04 | 2 | HAIKU-06 | T-23-12 | Resolve(role,1) = menor tier_priority | unit | `go test ./internal/upstreams/ ./internal/proxy/` | ✅ | ⬜ |
| 23-04-T3 | 04 | 2 | HAIKU-03, HAIKU-06 | T-23-72 | override de schedule/shed: pinado percorre ①→②→③ só externos; não pinado cascateia entre tier-1 (dispatchOverrideCascade); D-D1 só com todos os tier-1 fora; envelopes preservados | unit + integration | `go test ./internal/proxy/ -run 'DispatchPinned\|Override' -race` + `-tags=integration -run 'Schedule\|Shed\|OffHours\|Peak'` | ❌ W0 (cria casos) | ⬜ |
| 23-05-T1/T2 | 05 | 2 | HAIKU-02 | T-23-15..17 | ordem fixa, esgotada até renovação, re-teste 6 h, revoked re-teste 24 h, reconciliação Redis↔memória, chave nunca em log | unit (tdd) | `go test ./internal/proxy/ -run 'KeyPool\|ClassifyAnthropicKeyError\|ParseAnthropicKeys' -race` | ❌ W0 (cria) | ⬜ |
| 23-06-T1 | 06 | 2 | HAIKU-01 | T-23-20 | JSON Messages→OpenAI, erro com status preservado | unit | `go test ./internal/proxy/ -run 'TestAnthropicTranslateResponse\|TestAnthropicTranslateError\|TestAnthropicTranslateInterceptor'` | ❌ W0 (cria + fixtures) | ⬜ |
| 23-06-T2 | 06 | 2 | HAIKU-01 | T-23-19, T-23-21 | SSE→chunks, tool_calls incrementais, thinking descartado; EOF/erro de leitura → erro sintético + [DONE]; CRLF; data: múltiplo; usage cumulativo emitido 1× | unit | `go test ./internal/proxy/ -run TestAnthropicSSE -race` | ❌ W0 (cria + fixtures) | ⬜ |
| 23-07-T1 | 07 | 2 | HAIKU-08 | T-23-22 | mp3 forçado, voz mapeada, bearer próprio | unit | `go test ./internal/proxy/ -run 'TestOpenRouterTTSDirector\|TestParseTTSVoiceMap'` | ❌ W0 (cria) | ⬜ |
| 23-08-T1/T2 | 08 | 2 | HAIKU-01, HAIKU-05 | T-23-25..27, T-23-75 | OpenAI→Messages com allowlist; effort; cache_control; IsToolLoopContinuation + ToolLoopMode omit/disable; system sem user adjacente | unit (golden) | `go test ./internal/proxy/ -run 'TestAnthropicTranslateRequest\|TestAnthropicCompat'` | ❌ W0 (cria + golden) | ⬜ |
| 23-20-T1 | 20 | 2 | HAIKU-03, HAIKU-08 | T-23-69 | gatewayctl/admin aceitam anthropic-haiku/openrouter-deepseek/openrouter-tts; prefs só em openrouter-chat e openrouter-deepseek; paridade com fixture único | unit | `go test ./cmd/gatewayctl/ -run 'ModelAlias\|ParityFixture' && go test ./internal/admin/ -run 'ModelAlias\|ParityFixture'` | ✅ (estende) | ⬜ |
| 23-20-T2 | 20 | 2 | HAIKU-03 | T-23-70/71 | dashboard /modelos edita prefs do openrouter-deepseek; paridade com o fixture do gateway | vitest | `cd dashboard && npx vitest run src/lib/admin-actions.test.ts && npx tsc --noEmit` | ✅ (estende) | ⬜ |
| 23-19-T1 | 19 | 3 | HAIKU-04 | T-23-67/68 | sqlc em sincronia; Event com cache/cost_source | unit | `sqlc generate && sqlc diff && go test ./internal/billing/... ./internal/db/...` | ✅ | ⬜ |
| 23-19-T2 | 19 | 3 | HAIKU-05 | — | LLMEffortByTenantID; round-trip banco→sqlc→Refresh incl. NULL | unit + integration | `go test ./internal/tenants/...` + `-tags=integration -run TenantLLMEffort` | ❌ W0 (cria tenant_llm_effort_test.go) | ⬜ |
| 23-09-T1 | 09 | 4 | HAIKU-05 | T-23-28 | cliente > tenant > high; PASS 6 no ② | unit | `go test ./internal/proxy/ -run 'TestResolveLLMEffort\|TestOpenRouterDirector'` | ❌ W0 (cria) | ⬜ |
| 23-09-T2 | 09 | 4 | HAIKU-05 | T-23-30 | gatewayctl tenant set-llm-effort valida | unit | `go test ./cmd/gatewayctl/ -run 'LLMEffort\|Tenant'` | ❌ W0 (cria) | ⬜ |
| 23-10-T1 | 10 | 4 | HAIKU-04, HAIKU-09 | T-23-31 | usage.cost real (guarda 0 ≤ cost < 100 US$); ① com cache e degrau 100k; phantom a preço Haiku; TTS externo pela chave (tts-1, openrouter, input_token), sem colidir com o phantom do Kokoro | unit | `go test ./internal/proxy/ -run 'Usage\|Interceptor'` | ❌ W0 (cria interceptor_usage_haiku_test.go) | ⬜ |
| 23-10-T2 | 10 | 4 | HAIKU-04 | T-23-33 | units de cache no CLI e no price-sync | unit + lint | `bash -n scripts/price-sync/gateway-price-sync.sh && go test ./cmd/gatewayctl/ -run Price` | ✅ | ⬜ |
| 23-11-T1 | 11 | 4 | HAIKU-09 | T-23-34/35 | POST aux-pod-lifecycles idempotente (start repetido, stop repetido → already_closed, 404 só sem start) e validado | unit | `go test ./internal/admin/ -run AuxPod` | ❌ W0 (cria) | ⬜ |
| 23-11-T2 | 11 | 4 | HAIKU-09 | — | economy: % tokens, créditos × caixa, 3060 | unit | `go test ./internal/admin/ -count=1` | ✅ parcial | ⬜ |
| 23-12-T1 | 12 | 5 | HAIKU-01/02/05 | T-23-37..39, T-23-76 | proxy ① ponta a ponta contra fake (JSON, SSE, cascata, troca de chave, lastBody do ① em envelope OpenAI, timeout de cabeçalho cascateia, tradução falha sem rede, skip de tool loop, effort off) | unit | `go test ./internal/proxy/ -run 'TestBuildAnthropicProxy\|TestNewChatFallthroughTransport\|Fallthrough' -race` | ❌ W0 (cria) | ⬜ |
| 23-12-T2 | 12 | 5 | HAIKU-02 | T-23-37 | admin keys sem expor chave | unit | `go test ./internal/admin/ -run AnthropicKeys` | ❌ W0 (cria) | ⬜ |
| 23-13-T1 | 13 | 5 | HAIKU-09 | T-23-43 | cards créditos/caixa/% tokens/3060 | vitest | `cd dashboard && npx vitest run src/lib/gateway.test.ts src/components/economy-panel.test.tsx && npx tsc --noEmit` | ❌ W0 (cria economy-panel.test.tsx) | ⬜ |
| 23-13-T2 | 13 | 5 | HAIKU-09 | T-23-41/42 | 3060 grava custo best-effort; start logo após o create (inclui provisionamento); stop em todo vast_destroy | pytest | `cd ops/vast-3060 && python3 -m pytest -q test_unified3060.py` | ✅ | ⬜ |
| 23-14-T1 | 14 | 6 | HAIKU-03 | T-23-47/48 | probe sem custo e sem Bearer com lista de chaves; ANTHROPIC_TOOL_LOOP_MODE | unit | `go test ./internal/config/ ./internal/upstreams/` | ✅ | ⬜ |
| 23-14-T2 | 14 | 6 | HAIKU-03/08 | T-23-44/46, T-23-73 | wiring ①②③ + openrouter-tts + rotas admin; ExternalChatInterceptors + NewChatFallthroughTransport | build + unit | `go vet ./cmd/gateway/ && go build ./... && go test ./internal/proxy/ -run ExternalChatInterceptors` | ✅ | ⬜ |
| 23-14-T3 | 14 | 6 | HAIKU-03, HAIKU-06 | T-23-45, T-23-73 | cadeia ①→②→③ (status, timeout, replay do body OpenAI, paridade de effort off) e TTS openrouter-tts→kokoro com a composição de produção; RES-08 | integration | `go test -tags=integration ./internal/integration_test/ -run 'HaikuChain\|SensitiveNeverExternalHaiku\|TTS'` | ❌ W0 (cria) | ⬜ |
| 23-15-T1/T2 | 15 | 7 | HAIKU-07 | T-23-49..51 | gates + snapshot + dry-run na cópia de prod com conferência de fidelidade (extensões, colunas/constraints/triggers, goose 40) + idempotência de aliases/preços + rollback ordenado + tenants com prefs/peak + aprovação | manual | evidence + grep negativo de segredos | n/a | ⬜ |
| 23-16-T1..T3 | 16 | 8 | HAIKU-07 (+03/04/05/06/09 live) | T-23-52..56, T-23-74 | 9 aliases 200, ③ por alias canário isolado (force-open global só opcional com autorização), 503 sensível, 3090 off, 24 h sem 400 | manual/live | queries audit_log/billing_events (evidence) | n/a | ⬜ |
| 23-17-T1..T3 | 17 | 9 | HAIKU-08 | T-23-57..60 | 23-TTS-EVIDENCE.md criado na T1 com contrato MP3/voz por candidato; voz escolhida por teste cego; smoke mp3; fallback Kokoro por alias canário | manual/live | evidence | n/a | ⬜ |
| 23-18-T1..T3 | 18 | 9 | HAIKU-01/02/05/07 | T-23-61..65, T-23-77 | gate g1–g5 direto na API ANTES do PUT (escolhe ANTHROPIC_TOOL_LOOP_MODE); ① ao vivo; cache; rollback amplo com verificação +30 min/+2 h | manual/live | evidence | n/a | ⬜ |

## Wave 0 Requirements

- [ ] `gateway/internal/proxy/testdata/anthropic/` — fixtures
- [ ] fake Anthropic server (httptest) reutilizável
- [ ] `anthropic_translate_*_test.go`, `anthropic_keypool_test.go`, `chat_retryable_status_test.go`
- [ ] integration `haiku_chain_test.go`
- [ ] bump `db.Down(n)` em `migration_0026_test.go` e `migration_0029_test.go`
- [ ] golden `req_messages_tool_loop_disable.golden.json` (23-08) — corpo do gate g2 do 23-18
- [ ] fixture `gateway/internal/admin/testdata/provider_prefs_upstreams.json` (23-20)
- [ ] integration `tenant_llm_effort_test.go` (23-19)

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Smoke ① com chave real | HAIKU-01/02 | depende das chaves Max | gate g1–g5 direto na API antes de ligar; depois tool loop + forced tool + stream via gateway |
| Rollout prod | HAIKU-07 | prod | queries `audit_log`/`billing_events` |
| Voz TTS PT-BR | HAIKU-08 | qualidade subjetiva | teste cego com Pedro |

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 180s
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
