# Phase 9 Plan 04 — Verification

**Plan:** 09-04 — Sensitive-Tenant HUMAN-UAT Scenario Sheet
**Executed:** 2026-05-14
**Mode:** `/gsd-autonomous` (Task 2 blocking checkpoint handled as a deferred human-verify)

## Task 1 — `09-HUMAN-UAT.md` written + committed

**Status:** PASSED

The plan's automated verification expression ran and passed:

```
test -f "$UAT" && grep -qi 'Sign-off' && grep -qi 'Prerequisites' &&
grep -qi 'passed_partial' && [ UAT- count >= 4 ] &&
grep -q 'smoke-sensitive-failover' && grep -q 'provision-tenants' &&
grep -qi 'LGPD' && grep -qi 'RUNBOOK-CLIENT-INTEGRATION-SENSITIVE' &&
grep -qi 'telefonia' && grep -qi 'voice-api' && grep -qi 'final_status'
→ VERIFY: PASS
```

Acceptance criteria (plan Task 1):
- [x] `09-HUMAN-UAT.md` exists with YAML frontmatter (`status`, `operator`,
      `date_executed`, `final_status: pending`) and a `## Prerequisites`
      checkbox list including the gateway-not-deployed gate, the
      `provision-tenants.sh` run, the per-app env switch for all 4 repos, and
      the LGPD-checklist-submitted item.
- [x] ≥4 numbered UAT scenarios mapped to SC1/SC2/SC3/SC4, each with
      Pre-conditions / Steps (fenced bash) / Expected / pass-fail.
- [x] UAT-1 invokes `smoke-sensitive-failover.py` with a sensitive tenant key +
      `--pg-dsn` and asserts exit 0 + `gates.all_passed`; UAT-4 drills the
      `RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md` ROLLBACK procedure for all 4
      apps and times each <5 min (per-app `measured_rollback_time_*` fields).
- [x] `## Final Sign-off` section makes the LGPD legal sign-off a BLOCKING
      external gate — the operator attaches the signed `LGPD-REVIEW-CHECKLIST.md`.
- [x] Sign-off table (Result / Date / Operator / Notes) + overall phase-status
      line + documented `passed_partial` path.

Committed: `e4d45bc` — `docs(09-04): Phase 9 HUMAN-UAT sensitive-tenant scenario sheet`.

## Task 2 — blocking checkpoint (live UAT + LGPD legal sign-off)

**Status:** DEFERRED (programmatic parts PASSED; human-only parts deferred)

Task 2 is a `checkpoint:human-verify` with `gate="blocking"`. Under
`/gsd-autonomous` the programmatically-checkable parts were run; the live-UAT
execution and the external LGPD legal sign-off are deferred to a human operator
(the established per-phase deferred-gate pattern — 03-08 / 04-09 / 06-11 /
07-09 / 08-04).

**Programmatic checks — PASSED:**

| Check | Result |
|-------|--------|
| `09-HUMAN-UAT.md` exists + committed | FOUND (`e4d45bc`) |
| `scripts/integration-smoke/provision-tenants.sh` (09-01) | FOUND |
| `scripts/integration-smoke/smoke-sensitive-failover.py` (09-02) | FOUND |
| `scripts/integration-smoke/sensitive-failover-report-schema.json` (09-02) | FOUND |
| `gateway/docs/RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md` (09-03) | FOUND |
| `gateway/docs/LGPD-SUBPROCESSORS.md` (09-03) | FOUND |
| `gateway/docs/LGPD-REVIEW-CHECKLIST.md` (09-03) | FOUND |
| 09-01 commits `25ad726`, `ee54b46` | FOUND (`25ad726`) |
| 09-02 commits `1066489`, `7ce79fc` | FOUND |
| 09-03 commits `e233f42`, `3163137` | FOUND |

**Deferred to a human operator (cannot run autonomously):**

1. **Live UAT-1..UAT-4 execution** — double-gated: (a) the gateway is not
   deployed to the `ai-gateway-dev` Portainer stack (blocked on Phase 6
   emergency-pod integration tests — a separate debug session); (b) the
   operator must run `provision-tenants.sh --mint-keys` and switch the
   `base_url`/`api_key` env vars in all 4 client sibling repos. Until the
   gateway is deployed, all 4 UATs are `passed_partial`.
2. **External LGPD legal sign-off** — Ifix legal signs the `## Sign-off` table
   in `gateway/docs/LGPD-REVIEW-CHECKLIST.md`; the operator attaches the signed
   copy to `09-HUMAN-UAT.md` `## Final Sign-off`. Sensitive tenants (telefonia,
   cobrancas) MUST NOT be activated in production until this signature exists
   (ROADMAP Phase 9 SC4 / PRD-05).

**Resume signal:** operator types "approved" with the Sign-off table filled and
the LGPD legal sign-off recorded, or describes the defects found for a
`/gsd-plan-phase --gaps` pass.

## Plan-level verification (plan `<verification>`)

- [x] `09-HUMAN-UAT.md` exists and is committed (`e4d45bc`).
- [ ] The Task 2 blocking checkpoint produces a filled Sign-off table + the
      LGPD legal sign-off recorded — **DEFERRED** to the human operator (the
      sheet is in place with `final_status: pending`; the gateway is not
      deployed and the legal signature is not yet obtained).
- [x] This plan is `autonomous: false` — it cannot reach COMPLETE without
      operator action + external legal sign-off, consistent with 03-08 / 04-09
      / 06-11 / 07-09 / 08-04. The autonomous artifacts (09-01..09-03) are green
      and NOT blocked.

## Outcome

Plan 09-04 is **structurally complete**: the HUMAN-UAT scenario sheet is
written, verified, and committed; the Phase 9 autonomous artifacts it consumes
all exist. The live-UAT execution and the external LGPD legal sign-off are
**deferred to a human operator** — the documented deferred-gate pattern. No
real defects found.
