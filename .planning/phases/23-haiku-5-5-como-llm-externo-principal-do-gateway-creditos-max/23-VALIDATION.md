---
phase: 23
slug: haiku-5-5-como-llm-externo-principal-do-gateway-creditos-max
status: draft
nyquist_compliant: false
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

(Preenchido pelo planner/executor a partir do mapa Requirements → Test do RESEARCH.)

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|

## Wave 0 Requirements

- [ ] `gateway/internal/proxy/testdata/anthropic/` — fixtures
- [ ] fake Anthropic server (httptest) reutilizável
- [ ] `anthropic_translate_*_test.go`, `anthropic_keypool_test.go`, `chat_retryable_status_test.go`
- [ ] integration `haiku_chain_test.go`
- [ ] bump `db.Down(n)` em `migration_0026_test.go` e `migration_0029_test.go`

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Smoke ① com chave real | HAIKU-01/02 | depende das chaves Max | tool loop + forced tool + stream |
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
