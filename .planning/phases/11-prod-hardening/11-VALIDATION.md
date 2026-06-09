---
phase: 11
slug: prod-hardening
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-05-27
---

# Phase 11 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.
> Source: `11-RESEARCH.md` §Validation Architecture.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | Go: stdlib `testing` 1.24 + `testcontainers-go`. Python: `pytest` (smokes). Dashboard: `vitest` 3.0 + `@testing-library/react` 16.2 (already in `dashboard/package.json`). |
| **Config file** | `gateway/go.mod` · `scripts/integration-smoke/requirements.txt` · `dashboard/vitest.config.ts` |
| **Quick run command** | `cd gateway && go test ./cmd/gatewayctl/... -count=1 -race` · `cd dashboard && bun test` · `python scripts/integration-smoke/<name>.py --help` |
| **Full suite command** | `cd gateway && go test ./... -count=1 -race -timeout=5m && go test -tags=integration ./internal/integration_test/... -count=1 -timeout=10m && cd ../dashboard && bun test` |
| **Estimated runtime** | ~120s (quick) · ~10min (full) · live UAT 30+ min |

---

## Sampling Rate

- **After every task commit:** Run quick run command (scoped to changed surface — gatewayctl unit OR dashboard bun test OR smoke `--help`)
- **After every plan wave:** Run full suite command
- **Before `/gsd:verify-work`:** Full suite green + live UAT evidence committed in `11-VERIFICATION.md`
- **Max feedback latency:** 120 seconds (quick); 10 min (full)

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| 11-01-XX | 01 (load-test scaffold) | 1 | PRD-01 | — | sanitized JSONL replay; no PII | unit + manual | `python scripts/integration-smoke/load-replay.py --help` + jsonschema validate | ❌ W0 | ⬜ pending |
| 11-02-XX | 02 (dashboard SSO) | 1 | PRD-06 | T-11-AUTH-01..04 | TOTP enroll/verify; allowlist rejects non-@ifixtelecom; rate-limit 429 after 5 | unit (vitest) + manual UAT | `cd dashboard && bun test` | ❌ W0 | ⬜ pending |
| 11-03-XX | 03 (LGPD docs) | 1 | PRD-05 | — | signoff process + letter ref 4 sub-processors | linter (grep) | `grep -q "Vast.ai\\|OpenAI\\|OpenRouter\\|MinIO" gateway/docs/LGPD-SIGNOFF-*.md` | ❌ W0 | ⬜ pending |
| 11-04-XX | 04 (Phase 10 fold + gatewayctl) | 1 | D-18.1..4 | T-11-OPS-01 | panic→Sentry; key list aligned; smoke accepts FORCED_OPEN; GHA retrigger documented | go test + manual | `go test ./gateway/cmd/gatewayctl/ -count=1 -race` | ❌ W0 | ⬜ pending |
| 11-05-XX | 05 (per-env keys D-19) | 1 | — | T-11-OPS-02 | prod .env keys diff from dev | manual (ssh diff) | `diff <(ssh n8n-ia-vm 'grep AUTH_BEARER /opt/ai-gateway-prod/.env') <(ssh vps-ifix-vm 'grep AUTH_BEARER /opt/ai-gateway-dev/.env')` | ❌ W0 | ⬜ pending |
| 11-06-XX | 06 (load-test UAT) | 2 | PRD-01 | — | 30min sustained P95 chat ≤5s + embed ≤1s + STT ≤10s + error <1% | manual (live UAT) | `python scripts/integration-smoke/load-replay.py --duration 1800 --out /tmp/load-report.json` | ❌ W0 | ⬜ pending |
| 11-07-XX | 07 (chaos PRD-02 Vast DELETE) | 2 | PRD-02 | T-11-CHAOS-01 | invisible failover; breaker observation-driven OPEN | manual (live UAT) | `scripts/chaos/vast-delete.sh && python load-replay.py + jq` | ❌ W0 | ⬜ pending |
| 11-08-XX | 08 (chaos PRD-03 OpenRouter DROP) | 2 | PRD-03 | T-11-CHAOS-02 | sensitive 503 `sensitive_block`; normal fallthrough OpenAI; cleanup mandatory | manual (live UAT) | `scripts/chaos/openrouter-iptables-drop.sh apply` + `smoke-sensitive-failover.py` + `... cleanup` | ❌ W0 | ⬜ pending |
| 11-09-XX | 09 (RUNBOOK-INCIDENTS + POSTMORTEM) | 3 | PRD-04 (full) | — | 4 classes documented; cross-ref 7 sibling runbooks | linter (grep) | `grep -q "Primary pod down\\|OpenRouter / OpenAI degraded\\|Audit/billing pipeline broken\\|Rate-limit / quota lockout" gateway/docs/RUNBOOK-INCIDENTS.md` | ❌ W0 | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

> Task IDs `XX` filled by planner per plan task breakdown.

---

## Wave 0 Requirements

- [ ] `gateway/cmd/gatewayctl/debug.go` + `debug_test.go` — D-18.2 panic-path
- [ ] `gateway/cmd/gatewayctl/key.go` extension (`runKeyList` + tests) — D-18.3
- [ ] `gateway/internal/admin/debug_panic.go` — HTTP handler `/admin/debug/panic` gated by X-Admin-Key, triggers Recoverer
- [ ] `dashboard/src/lib/auth.ts` — twoFactor plugin + rateLimit customRules + session expiresIn=30min
- [ ] `dashboard/src/lib/allowlist.ts` + unit test
- [ ] `dashboard/src/app/2fa/enroll/page.tsx` + `dashboard/src/app/2fa/challenge/page.tsx`
- [ ] `dashboard/src/app/login/page.tsx` extension (rate-limit Alert + session-expired Alert)
- [ ] `dashboard/src/middleware.ts` extension (two-stage twoFactorEnabled + twoFactorVerified)
- [ ] `dashboard/src/lib/schema.ts` extension (twoFactor plugin schema)
- [ ] `scripts/integration-smoke/load-replay.py` + `load-replay-report-schema.json` + `audit-log-export.py`
- [ ] `scripts/chaos/vast-delete.sh` + `scripts/chaos/openrouter-iptables-drop.sh`
- [ ] `scripts/dashboard/seed-admins.sh`
- [ ] `scripts/integration-smoke/smoke-sensitive-failover.py` edit — FORCED_OPEN polling + `gatewayctl breaker force-open` repoint
- [ ] `gateway/docs/RUNBOOK-INCIDENTS.md` (4 classes)
- [ ] `gateway/docs/POSTMORTEM-TEMPLATE.md` (Google SRE 9-section blameless)
- [ ] `gateway/docs/LGPD-SIGNOFF-PROCESS.md` + `LGPD-SIGNOFF-LETTER-TEMPLATE.md`
- [ ] `gateway/docs/RUNBOOK-DEPLOY.md` extension — D-18.4 retrigger + D-19 per-env keys
- [ ] `.planning/load-test-fixtures/.gitignore` + `.planning/legal/.gitignore`

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Load-test 30min sustained meets SLO v1.0 | PRD-01 | Live prod gateway + Vast 2×3090 + real audit_log replay (~$1-3 Vast spend) | Run `load-replay.py --duration 1800`; assert report JSON: P95_chat≤5000ms · P95_embed≤1000ms · P95_stt≤10000ms · error_rate_non_503<0.01 · zero 5xx panic logs |
| Invisible failover during primary kill | PRD-02 | Requires real Vast API DELETE + active load | Start load-replay 5min; trigger `scripts/chaos/vast-delete.sh`; assert breaker `local-llm` reaches OPEN via natural observation (NOT force-open); P95 stays within SLO degradation budget; clients see latency bump but zero panic |
| OpenRouter degradation → sensitive 503 + normal fallthrough | PRD-03 | iptables DROP on n8n-ia-vm modifies live prod egress | `ssh n8n-ia-vm scripts/chaos/openrouter-iptables-drop.sh apply` → run `smoke-sensitive-failover.py` → assert sensitive HTTP 503 `sensitive_block`, normal completes via OpenAI; cleanup mandatory: `... cleanup` then verify `iptables -L OUTPUT \| grep openrouter` empty |
| TOTP enroll + verify end-to-end | PRD-06 | Requires real Authenticator app + live dashboard | Login pedro@ifixtelecom → /2fa/enroll → scan QR → enter 6-digit → backup codes shown → logout → login → /2fa/challenge → enter TOTP → /admin reached |
| Rate-limit /sign-in/email triggers 429 after 5 attempts | PRD-06 | Live Better Auth endpoint | `for i in $(seq 1..6); do curl -s -X POST .../api/auth/sign-in/email -d '{"email":"x@ifixtelecom.com.br","password":"wrong"}'; done` → 6th returns HTTP 429 with `X-Retry-After` |
| signUp rejects non-@ifixtelecom | PRD-06 | databaseHooks runtime check | `curl -X POST .../api/auth/sign-up/email -d '{"email":"foo@gmail.com","password":"x","name":"x"}'` → HTTP 400/422 with `email_domain_not_allowed` |
| `gatewayctl debug emit-error` → Sentry event in `ifix-ai-gateway-prod` | D-18.2 | Requires live Sentry project + gateway container | `ssh n8n-ia-vm 'docker exec ai-gateway gatewayctl debug emit-error'`; wait 5s; query Sentry API for new event tagged `synthetic.panic=true` |
| GHA workflow_dispatch produces `:v1.0.0` image | D-18.4 | Requires GitHub Actions + ghcr.io | `gh workflow run build-gateway.yml --ref v1.0.0 -f tag=v1.0.0`; wait completion; `docker pull ghcr.io/ifixtelecom/ifix-ai-gateway:v1.0.0` returns 200 |
| Per-env keys procedure (D-19) | — | Operator credential rotation | `diff <(ssh n8n-ia-vm 'sudo grep -E "^UPSTREAM_.*_AUTH_BEARER" /opt/ai-gateway-prod/.env') <(ssh vps-ifix-vm 'sudo grep -E "^UPSTREAM_.*_AUTH_BEARER" /opt/ai-gateway-dev/.env')` → non-empty diff (4 vars differ) |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify (live-UAT plans 06/07/08 in Wave 2 are sequential — flagged for explicit operator gate)
- [ ] Wave 0 covers all MISSING references above
- [ ] No watch-mode flags
- [ ] Feedback latency < 120s (quick) / 600s (full)
- [ ] `nyquist_compliant: true` set in frontmatter after planner consumes this contract

**Approval:** pending
