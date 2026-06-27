---
milestone: v1
audited: 2026-06-27
status: gaps_found
scores:
  requirements: 70/82 satisfied (10 unsatisfied UM-*, 2 partial effectively-satisfied)
  phases: 23/24 executed (Phase 13 never executed); 2 missing VERIFICATION.md (06, 06.5)
  integration: 3/5 E2E flows complete
  flows: 3/5
gaps:
  requirements:
    - id: "UM-01..UM-10"
      status: "unsatisfied"
      phase: "13-dashboard-user-management"
      claimed_by_plans: ["13-01..13-05-PLAN.md"]
      completed_by_plans: []
      verification_status: "missing"
      evidence: "Phase 13 has 5 PLANs + CONTEXT/RESEARCH/UI-SPEC/VALIDATION but 0 SUMMARY = never executed. Better Auth admin plugin deliberately NOT installed (auth-client.ts:14 only twoFactorClient). /settings/operadores is a read-only roster (Phase 11) — 'Provisionar operador' button has no onClick. No changePassword/createUser/removeUser/resetPassword/reset2FA. No admin_audit_log migration."
  integration:
    - flow: "STT/embed billing metering"
      status: "broken"
      affected_requirements: ["OBS-09 (partial)", "TEN-04 (quota enforcement)"]
      evidence: "billing.Slot.AudioSecondsMs10 + EmbedsCount declared (accountant.go:31-32) + read (interceptor_usage.go:207-208) but NEVER written. STT proxies (buildOpenAIWhisperProxy/buildGeminiSTTProxy/buildGroqWhisperProxy/NewDynamicOverrideSTTProxy, main.go:587) pass zero usage interceptors; embed proxy (NewEmbeddingsProxy) has none. → billing_events.audio_seconds & embeds_count always 0 → /consumo shows 0 → per-tenant audio/embed quotas (quota/counters.go:87) effectively UNENFORCED."
    - flow: "Client apps → gateway (6 integrations)"
      status: "unconfirmed (process gap)"
      affected_requirements: ["INT-01..06"]
      evidence: "Gateway-side complete (6 tenants provisioned, keys minted, smoke scripts + runbooks). 08-HUMAN-UAT.md + 09-HUMAN-UAT.md final_status: pending — no operator sign-off that converseai-v4/chat-ifix/telefonia/cobrancas/campanhas/voice-api route through gateway in prod. Code/infra exists; live confirmation absent."
tech_debt:
  - phase: cross-cutting
    items:
      - "47 stale checkboxes: GW-03/04/05/06, RES-01..08, TEN-03..07, LSH-01..05, PRV-01..10, OBS-01..08, PRD-07, INT-01..06 are [ ] in REQUIREMENTS.md but IMPLEMENTED+tested (code:line evidence). Need ticking + traceability Pending→Complete."
      - "Phase 06.5 (auto-provisioning, PRV-01..10) has NO VERIFICATION.md though emerg/ + emerg/vast/ code fully present + verified. Backfill artifact."
      - "Phase 06 (emergency-pod-template-refactor) no VERIFICATION.md (superseded by 06.5)."
      - "PRD-07 DNS/TLS satisfied under renamed domain ai-gateway.converse-ai.app (was gateway.ifix.com.br) — update req text."
      - "OBS-02 Prometheus cardinality ≤10k static-verified, live count unconfirmed."
      - "Phase 03 scaffold_imports.go dep-pin file may be uncleaned (verify)."
---

# Milestone v1 — Audit Report

**Status:** gaps_found
**Definition of done:** "Ship the first working gateway with pod + auth + failover + auto-provisioning + 6 app integrations."

## Headline

The 5 v1 pillars (pod, auth, failover, auto-provisioning, 6 integrations) are **implemented in code and live in PROD**. The REQUIREMENTS.md checkboxes are badly stale: of ~57 unchecked, **47 are actually done** (code+tests verified). Only **10 are genuinely missing** — all Phase 13 (dashboard user-management), which was planned but **never executed**. Two E2E flows have real breaks (STT/embed metering; client-app live-UAT sign-off).

## Requirements Coverage (3-source cross-ref + code verification)

| Category | Count |
|----------|-------|
| Total v1 requirements | 82 |
| Genuinely satisfied (code+tests) | 70 |
| └ stale `[ ]` checkbox (need ticking) | 47 |
| Genuinely unsatisfied | 10 (UM-01..10) |
| Partial/uncertain (effectively satisfied) | 2 (OBS-02, PRD-04) |
| Orphaned (no VERIFICATION.md) | 10 (UM-* — Phase 13 unexecuted) |

## Critical Gaps (blockers to v1 "complete")

### 1. Phase 13 user-management — NEVER EXECUTED (UM-01..10)
Dashboard operator CRUD + self-service change-password absent. `/settings/operadores` is a read-only roster; buttons are visual-only. `seed-admins.sh` is the only provisioning path. → Execute Phase 13, OR formally defer UM-* to v2.

### 2. STT/embed billing metering broken
`AudioSecondsMs10`/`EmbedsCount` never incremented → `billing_events` audio/embeds always 0 → `/consumo` shows 0 AND **per-tenant audio/embed quotas unenforced** (TEN-04 hole — tenant can exceed embed quota without block). Fix: wire usage interceptor into STT + embed proxies. (Known: memory `dashboard-cost-price-sync`.)

## Warnings

- **Client integration UAT (Phase 8+9) not signed off** — 6 apps' gateway routing unconfirmed in prod (HUMAN-UATs pending). Code/infra present.
- **47 stale checkboxes** — REQUIREMENTS.md understates reality; reconcile before close.
- **Phase 06.5/06 missing VERIFICATION.md** — PRV-* code present + verified; artifact gap only.

## E2E Flows

| Flow | Status |
|------|--------|
| Auth + request (P2) | ✅ wired |
| Fallback chain (P3/06.9/12) — incl. RES-08 sensitive 503, RES-13 dial-fallthrough | ✅ wired (prod chaos confirmed) |
| Auto-provisioning / primary pod (P6/06.5/06.8/12) — incl. RES-11 death detection | ✅ wired |
| Observability → dashboard (P7/15) | ⚠️ wired, but /consumo audio/embeds = 0 (metering gap) |
| Client integrations (P8/9) | ⚠️ infra wired, live sign-off pending |

## Recommended Path

v1 is ~95% real. To close honestly:
1. **Reconcile 47 stale checkboxes** (mechanical, evidence in this audit).
2. **Decide UM-01..10:** execute Phase 13 OR defer to v2 (the only true feature gap).
3. **Fix STT/embed metering** (small gateway change; closes TEN-04 quota hole + /consumo 0s).
4. **Sign off Phase 8/9 client UAT** (operator confirms 6 apps route through gateway).
5. Backfill 06.5 VERIFICATION.md; fix PRD-07 domain text.
