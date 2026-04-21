---
phase: 04-multi-tenant-quotas-billing-schedule-routing
plan: 01
subsystem: scaffolding
tags:
  - sentinel-errors
  - tzdata
  - config
  - openai-envelope
  - operator-gates

requires:
  - phase: 02-auth-multi-upstream
    provides: config.Load + envOr/atoiOr/boolOr helpers, cmd/gateway/main.go structure, pkg/openai ErrorResponse/ErrorDetail
  - phase: 03-resilience-fallback-chain
    provides: sentinel-error pattern established (proxy, auth, idempotency, upstreams, breaker packages)

provides:
  - 17 sentinel errors across 5 new packages (quota, billing, schedule, admin, tenants)
  - 13 new openai envelope constants (3 type + 10 code)
  - tzdata embedded in binary (distroless-safe time.LoadLocation)
  - mustLoadLocation helper (boot-time fail-fast if tzdata missing)
  - 7 new Phase 4 config fields with documented defaults
  - floatOr config helper (new; complements envOr/atoiOr/boolOr)
  - 04-WAVE0-GATES.md (operator-confirmed seed values for Plan 04-03)

affects:
  - 04-02 (migrations reference sentinel packages in future go code)
  - 04-03 (migration 0015 seed reads gate doc)
  - 04-04 (quota Lua + tenants loader + schedule policy import sentinels)
  - 04-05 (billing flusher + admin middleware + interceptor_usage import sentinels)
  - 04-06 (middleware chain uses errors.Is against sentinels)
  - 04-07 (gatewayctl error messages use openai codes)
  - 04-08 (integration tests assert on sentinel errors + envelope codes)

tech-stack:
  added:
    - Go stdlib time/tzdata (blank import, ~400 KB binary cost)
  patterns:
    - "Sentinel-per-package: new package = new errors.go with var (...) block, message prefix <pkg>: lowercase"
    - "Discriminated error envelope: openai codes separate from Go sentinels (1:1 mapping documented in plan)"
    - "Config extension: extend Config struct + Load() + add typed helper if needed (floatOr pattern mirrors atoiOr)"
    - "Operator gate docs: .planning gate file captures volatile external facts (pricing, provider availability) before migrations encode them"

key-files:
  created:
    - gateway/internal/quota/errors.go
    - gateway/internal/billing/errors.go
    - gateway/internal/schedule/errors.go
    - gateway/internal/admin/errors.go
    - gateway/internal/tenants/errors.go
    - .planning/phases/04-multi-tenant-quotas-billing-schedule-routing/04-WAVE0-GATES.md
  modified:
    - gateway/cmd/gateway/main.go (added _ "time/tzdata" + mustLoadLocation)
    - gateway/internal/config/config.go (7 new fields + floatOr helper)
    - gateway/internal/config/config_test.go (3 new tests: Phase4Defaults, Phase4FromEnv, Phase4FloatOrBogusValue)
    - pkg/openai/types.go (13 new constants under "Phase 4 — discriminated error envelope")

key-decisions:
  - "Sentinel D-A1: 17 sentinels across 5 packages, NO typed wrapper structs (matches idempotency/errors.go canonical shape)"
  - "Envelope D-A4: 13 openai constants (3 type + 10 code) cover all Phase 4 rejection paths (rate-limit, quota daily/monthly × 3 dims, fail-closed unavailable, off-hours block)"
  - "Distroless-safe D-Build: _ \"time/tzdata\" blank import embeds zones; ~400 KB binary cost acceptable vs runtime zoneinfo.zip dependency"
  - "D-C1/C2 follow-on risk: config.go:112 still has UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER=['novita'] — Plan 04-09 UAT to revalidate against live Fireworks availability"
  - "A1/A2/A5 gate closed via RESEARCH placeholder values (not live dashboard fetch) — migration 0015 seeds from these; 04-09 UAT MUST revalidate and emit migration 0016 if drift >10%"
  - "A3 quota defaults: operator accepted v1 baseline (daily 10M tokens, 600 audio-min, 100k embeds; monthly 30× daily; rps 20 / rpm 600)"

patterns-established:
  - "Per-package errors.go: each new internal package ships a per-file sentinel var block before any logic-bearing file"
  - "mustLoadLocation: fail-fast on boot if tzdata-dependent feature cannot initialize"
  - "Operator gate doc: volatile external facts committed in .planning/ BEFORE migration encodes them, with explicit 'revalidate in UAT' note when gate closed from secondary source"

requirements-completed:
  - TEN-03
  - TEN-04
  - TEN-05
  - TEN-06
  - TEN-07

duration: 22min
completed: 2026-04-21
---

# Plan 04-01: Wave 0 Scaffolding + Operator Gates Summary

**Named all Phase 4 rejection sentinels, embedded tzdata, extended config with 7 Fase 4 env vars, and locked the A1/A2/A5 pricing/availability gate that Plan 04-03 migration 0015 seeds from.**

## Performance

- **Duration:** ~22 min (executor) + gate writeback
- **Started:** 2026-04-21 (orchestrator spawn)
- **Completed:** 2026-04-21
- **Tasks:** 3/3 (Task 1 auto, Task 2 auto, Task 3 human-verify closed by operator via orchestrator)
- **Files created:** 6 (5 sentinel Go files + 1 gate doc)
- **Files modified:** 4 (main.go, config.go, config_test.go, pkg/openai/types.go)

## Accomplishments

- Every downstream Phase 4 plan can now `errors.Is(err, quota.ErrRateLimitRPS)` / `schedule.ErrOffHoursUpstreamUnavailable` / `admin.ErrInvalidAdminKey` without waiting on their own scaffolding.
- Binary is distroless-safe: `time.LoadLocation("America/Sao_Paulo")` works in the Phase 2 02-08 distroless image without mounting OS tzdata.
- `config.Config` has the 7 new fields (`AdminKeyBootstrap`, `RateLimitFailOpen`, `QuotaFailOpen`, `USDBRLDefault`, `WriteTimeoutChatS/EmbedS/AudioS`) with defaults verified by 3 new tests.
- `pkg/openai` has the full discriminated error-envelope vocabulary from D-A4 (3 types, 10 codes), so the proxy-layer error mapper in Plan 04-06 can return them uniformly.
- Operator gate closed with RESEARCH placeholder values + explicit "revalidate in 04-09 UAT" flag; Plan 04-03 unblocked to run migration 0015 with documented seed values.

## Task Commits

1. **Task 1: 5 sentinel error files + pkg/openai/types.go constants** — `71b28ed` (feat)
2. **Task 2: tzdata + mustLoadLocation + config.go Phase 4 fields + tests** — `98215dd` (feat)
3. **Task 3: Operator gate A1/A2/A5 sign-off** — `fba6954` (docs) — closed with RESEARCH placeholder + UAT revalidation note

## Files Created

- `gateway/internal/quota/errors.go` — 9 sentinels (ErrRateLimitRPS/RPM, 6 × ErrQuotaExceeded*, ErrQuotaCheckUnavailable)
- `gateway/internal/billing/errors.go` — 3 sentinels (ErrFlushFailed, ErrPriceMissing, ErrFXMissing)
- `gateway/internal/schedule/errors.go` — 1 sentinel (ErrOffHoursUpstreamUnavailable)
- `gateway/internal/admin/errors.go` — 2 sentinels (ErrMissingAdminKey, ErrInvalidAdminKey)
- `gateway/internal/tenants/errors.go` — 2 sentinels (ErrTenantNotFound, ErrSensitivePeakInvariant)
- `.planning/phases/04-multi-tenant-quotas-billing-schedule-routing/04-WAVE0-GATES.md` — operator gate doc (A1/A2/A5 + A3 quota defaults + FX rate)

## Files Modified

- `gateway/cmd/gateway/main.go` — blank import `_ "time/tzdata"` + `mustLoadLocation(name, log)` helper (not yet called from main; Plan 04-06 wires it)
- `gateway/internal/config/config.go` — 7 new struct fields + corresponding `Load()` lines + `floatOr` helper (new)
- `gateway/internal/config/config_test.go` — 3 new tests (TestLoad_Phase4Defaults, TestLoad_Phase4FromEnv, TestLoad_Phase4FloatOrBogusValue)
- `pkg/openai/types.go` — 13 new constants under "Phase 4 — discriminated error envelope types/codes (D-A4)"

## Verification Evidence

- `go build ./internal/quota/... ./internal/billing/... ./internal/schedule/... ./internal/admin/... ./internal/tenants/... ../pkg/openai/...` — exit 0
- `gofmt -l ./internal/quota ./internal/billing ./internal/schedule ./internal/admin ./internal/tenants ./cmd/gateway ./internal/config` — empty
- `go vet ./internal/quota/... ./internal/billing/... ./internal/schedule/... ./internal/admin/... ./internal/tenants/... ./cmd/gateway/... ./internal/config/...` — exit 0
- `go test -run TestLoad_Phase4 ./internal/config/...` — PASS (3 tests)
- Full `go test ./internal/config/...` — PASS (no regression on Phase 2/3 tests)
- `grep -c 'errors.New('` on new sentinel files → 9 / 3 / 1 / 2 / 2 (matches plan acceptance)
- `grep -cE "QuotaExceededDaily(Tokens|AudioMinutes|Embeds)Code" pkg/openai/types.go` → 3; Monthly → 3; RateLimit(RPS|RPM)Code → 2 (matches plan acceptance)

## Deviations from Plan

1. **Task 3 gate closed from RESEARCH, not live dashboard** — operator chose the RESEARCH placeholder path via orchestrator question (labeled "Usar valores do RESEARCH como placeholder agora"). Gate doc explicitly marks seeds as placeholder and requires Plan 04-09 UAT revalidation against live OpenRouter/OpenAI + potential migration 0016. This is acknowledged divergence from the "live dashboard" instruction; documented in both commit message and gate doc body.

2. **No other deviations** — Tasks 1 and 2 implemented exactly per plan specification (sentinel names, error messages, config field names + defaults, test shape).

## Open Follow-ups for Later Phases / Plans

- **Plan 04-06:** Call `mustLoadLocation("America/Sao_Paulo", log)` from main.go when wiring the schedule middleware. Helper is ready; wiring is deferred per this plan's "Do NOT call from main() yet" note.
- **Plan 04-09 UAT:** Revalidate A1/A2/A5 against live OpenRouter + OpenAI dashboards. If live prompt/completion USD diverges >10% from the 0.195/1.56 seed, emit migration 0016 (`UPDATE prices`) and `UPDATE fx_rates`. If Fireworks no longer serves qwen3.5-27b, reset `UPSTREAM_LLM_OPENROUTER_PROVIDER_ORDER` away from `["novita"]`.
- **Phase 3 carry-over:** WriteTimeoutChatS=0 intentionally keeps SSE alive (no DoS defense on non-streaming routes). Plan 04-06 adds the rate-limit middleware that compensates by rejecting excess traffic before per-route timeouts matter. Re-evaluate the timeout numbers after Phase 4 live (STATE.md Phase 4 TODO).

## Self-Check: PASSED
