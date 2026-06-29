---
milestone: v1
audited: 2026-06-28
status: gaps_found
supersedes: "2026-06-27 audit (stale — predated Phase 13 execution)"
scores:
  requirements: 80/82 satisfied (1 code blocker TEN-04 audio/embed; INT-01..06 live-UAT process-pending)
  phases: 24/24 executed; 2 missing VERIFICATION.md (06, 06.5 — artifact gap only)
  integration: 4/5 E2E flows complete (audio/embed metering broken)
  flows: 4/5
gaps:
  requirements:
    - id: "TEN-04"
      status: "partial"
      phase: "04-multi-tenant-quotas-billing-schedule-routing"
      claimed_by_plans: ["04-02-PLAN.md"]
      completed_by_plans: ["04-02 (token/chat path only)"]
      verification_status: "passed (chat dim); audio/embed dim never wired"
      evidence: "CODE BLOCKER (integration-checker 2026-06-28). billing.RequestUsage.AudioSecondsMs10 (accountant.go:31) + EmbedsCount (:32) declared + READ (interceptor_usage.go:207-208, copied to event :271-272) + quota-gated (quota/counters.go:84,87,111,114) but NEVER WRITTEN — grep AudioSecondsMs10.Store/.Add + EmbedsCount.Store/.Add = 0 matches. STT proxies (NewAudioProxy main.go:587, buildGeminiSTTProxy :1618, buildGroqWhisperProxy :1647, buildOpenAIWhisperProxy :1671, NewDynamicOverrideSTTProxy :686) + embed proxy (NewEmbeddingsProxy :576) pass ZERO usage interceptor (only chat proxies do, :570/:652/:660). Double-fault: interceptor_usage.go:210-215 skips enqueue when all usage==0 → no billing_events row at all. Result: billing_events.audio_seconds & embeds_count always 0 → usage_counters 0 → per-tenant audio/embed quotas can never trip (UNENFORCED). Producer-less dangling-read: full read+persist+report chain dead because step-0 write missing. Single fix point in cmd/gateway/main.go (attach usage interceptor parsing STT {text,duration}/embed {data[],usage} to 4 STT + 2 embed proxies)."
  integration:
    - flow: "STT/embed billing metering"
      status: "broken"
      affected_requirements: ["TEN-04 (quota enforcement)", "OBS-09 (audio/embed usage visibility shows 0)"]
      evidence: "See TEN-04 above. OBS-09 panel + /consumo render audio/embed = 0 as spillover (OBS-09 economy/phantom panel itself works — only the audio/embed usage dimension is dead)."
    - flow: "Client apps → gateway (6 integrations)"
      status: "unconfirmed (process gap, NOT code)"
      affected_requirements: ["INT-01..06"]
      evidence: "PROCESS-GAP-ONLY (integration-checker 2026-06-28). Gateway-side COMPLETE + committed: provision-tenants.sh (sensitive telefonia/cobrancas/campanhas/voice-api :106), smoke-converseai.py/smoke-chat-ifix.py/smoke-sensitive-failover.py, runbooks; 6 tenant keys minted in PROD (CLAUDE.md). 6 client apps are SEPARATE repos. 08-HUMAN-UAT.md + 09-HUMAN-UAT.md final_status: pending = deferred-UAT pattern (08 explicitly 'autonomous build NOT blocked'; 09 adds external LGPD legal gate). No repo code defect — only live operator + LGPD sign-off pending."
tech_debt:
  - phase: cross-cutting
    items:
      - "47 stale checkboxes: GW-03/04/05/06, RES-01..08, TEN-03/05/06/07, LSH-01..05, PRV-01..10, OBS-01..08, PRD-07, INT-01..06 are [ ] in REQUIREMENTS.md but IMPLEMENTED+tested (code:line evidence). Need ticking + traceability Pending→Complete."
      - "Phase 06.5 (auto-provisioning, PRV-01..10) has NO VERIFICATION.md though emerg/ + emerg/vast/ code fully present + verified. Backfill artifact."
      - "Phase 06 (emergency-pod-template-refactor) no VERIFICATION.md (superseded by 06.5)."
      - "PRD-07 DNS/TLS satisfied under renamed domain ai-gateway.converse-ai.app (was gateway.ifix.com.br) — update req text."
      - "OBS-02 Prometheus cardinality ≤10k static-verified, live count unconfirmed."
      - "Phase 13 VERIFICATION.md status field still 'human_needed' but UAT 8/8 PASS (git 2a5d8dc/a1c9adb) — flip to passed. UM-09 PARTIAL was operational (BREVO creds) now closed (git 2b5659e)."
---

# Milestone v1 — Audit Report (re-audit 2026-06-28)

**Status:** gaps_found
**Definition of done:** "Ship the first working gateway with pod + auth + failover + auto-provisioning + 6 app integrations."

> **Supersedes the 2026-06-27 audit**, which was stale: it called Phase 13 (UM-01..10) "never executed" — Phase 13 was executed + deployed live on 2026-06-28 (git `a1c9adb`, 5/5 plans, 8/8 UAT, 15/15 security). That blocker is **CLOSED**.

## Headline

The 5 v1 pillars (pod, auth, failover, auto-provisioning, 6 integrations) are **implemented in code and live in PROD**. All 24 phases executed. The only thing standing between v1 and a clean "complete" is **one genuine code blocker** — TEN-04 audio/embed metering never wired (single fix point in `cmd/gateway/main.go`) — plus the INT-01..06 client live-UAT sign-off, which is a **process gap, not code**.

## Requirements Coverage (3-source cross-ref + code verification)

| Category | Count |
|----------|-------|
| Total v1 requirements | 82 |
| Genuinely satisfied (code+tests) | 80 |
| └ incl. UM-01..10 (Phase 13 — NOW DONE) | 10 |
| └ stale `[ ]` checkbox (need ticking) | 47 |
| Code blocker (forces gaps_found) | 1 (TEN-04 audio/embed dim) |
| Process-pending (live UAT) | 6 (INT-01..06 — gateway-side wired) |
| Spillover-degraded | 1 (OBS-09 audio/embed visibility = 0) |

## Critical Gap (sole blocker to v1 "complete")

### STT/embed billing metering broken — TEN-04 (+ OBS-09 spillover)
`AudioSecondsMs10`/`EmbedsCount` declared + read + quota-gated but **never written** by any producer. STT (4) + embed (2) proxies pass zero usage interceptor; chat proxies do. → `billing_events` audio/embeds always 0 → audio/embed quotas **UNENFORCED** + `/consumo` shows 0. Verified by integration-checker 2026-06-28 (grep = 0 write sites). **Single fix point** in `cmd/gateway/main.go`: attach a usage interceptor parsing STT `{text,duration}` / embed `{data[],usage}` responses to the 6 proxies.

## Warnings

- **Client integration UAT (Phase 8+9) not signed off** — 6 apps' gateway routing unconfirmed in prod (HUMAN-UATs `pending`, 09 has LGPD legal gate). Gateway-side code + provisioning + keys present. **Process gap, not code.**
- **47 stale checkboxes** — REQUIREMENTS.md understates reality; reconcile before close.
- **Phase 06.5/06 missing VERIFICATION.md** — PRV-* code present + verified; artifact gap only.
- **Phase 13 VERIFICATION status field** stale (`human_needed`) vs UAT 8/8 PASS — flip to `passed`.

## E2E Flows (integration-checker verified 2026-06-28)

| Flow | Status |
|------|--------|
| Auth (API-key + admin-key) | ✅ wired |
| Fallback chain (P3/06.9/12) — RES-08 sensitive 503, RES-11/12/13 | ✅ wired (prod chaos-proven, 0× conn-502) |
| Auto-provisioning / primary pod (P6.5/06.6+/06.8/12) | ✅ wired |
| Billing — token/chat | ✅ wired (TokensIn/Out written) |
| Billing/quota — **audio + embeds** | ❌ broken (no producer — TEN-04) |
| Observability → dashboard (P7/15) | ⚠️ wired; /consumo audio/embed cols = 0 (TEN-04 spillover) |
| Client integrations (P8/9) | ⚠️ gateway-side wired; live sign-off pending |

## Recommended Path

v1 is ~98% real, all phases executed. To close honestly:
1. **Fix STT/embed metering** (sole code blocker — small gateway change; closes TEN-04 quota hole + /consumo 0s + OBS-09 audio/embed visibility). → insert closure phase or `/gsd:quick`.
2. **Reconcile 47 stale checkboxes** + flip Phase 13 VERIFICATION → passed (mechanical, evidence in this audit).
3. **Sign off Phase 8/9 client UAT** (operator + LGPD confirm 6 apps route through gateway) — process, can run parallel to close.
4. Backfill 06.5 VERIFICATION.md; fix PRD-07 domain text.
5. Then `/gsd:complete-milestone v1`.
