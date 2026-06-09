---
phase: 11
plan: 11-09
slug: runbook-incidents-and-postmortem
subsystem: docs/runbook
tags: [docs, runbook, postmortem, 2fa, incident-response, prd-04]
status: passed
requirements: [PRD-04]
dependency-graph:
  requires: [11-02, 11-04, 11-05]
  requires-deferred: [11-06, 11-07, 11-08]
  provides: [PRD-04 full]
  affects: [Phase 11 closeout]
tech-stack:
  added: []
  patterns: [google-sre-blameless-postmortem, runbook-pattern-G-5-element-header]
key-files:
  created:
    - gateway/docs/RUNBOOK-INCIDENTS.md
    - gateway/docs/POSTMORTEM-TEMPLATE.md
    - gateway/docs/RUNBOOK-2FA-RECOVERY.md
  modified: []
decisions:
  - "Synthesized PRD-04 runbook from D-11 plan spec + Google SRE template, BEFORE Wave 2 live chaos UAT (deferred by operator). Defensive 'if available' evidence fallbacks in every class point to Phase 10 EVIDENCE files so the runbook ships intact regardless of when Wave 2 closes."
  - "Triagem de Incidente Desconhecido authored as an entry-point (NOT a 5th class) per reviews LOW #2. The 4-class taxonomy is a deliberate scope ceiling — unknowns route to the existing classes OR drive a documented D-XX expansion in the next planning cycle."
  - "Admin 2FA recovery treated as a Class 4 sub-class (credential/lockout shape) rather than a 5th class — keeps the D-11 taxonomy stable while still cross-referencing 2FA at the 3 required sites (Class 4 detection, Operator Recovery Procedures, sibling list)."
metrics:
  duration_minutes: 22
  completed_date: 2026-05-27
  commits: 3
  tasks_completed: 3
  files_created: 3
  bytes_created: 46342
---

# Phase 11 Plan 11-09: Runbook + Postmortem Summary

One-liner: PRD-04 full incident response — RUNBOOK-INCIDENTS.md (4 D-11
classes + Triagem entry-point + 2FA sub-class), POSTMORTEM-TEMPLATE.md
(Google SRE blameless 9-section), RUNBOOK-2FA-RECOVERY.md (separation-of-duty
+ audit-row-before-SQL-UPDATE) — all synthesized from plan spec and
defensively cross-referenced to Phase 10 EVIDENCE before Wave 2 chaos
UAT runs.

---

## Scope Delivered

Doc-only plan; no runtime code changes; no new dependencies added.

| File | Status | Bytes | Purpose |
|------|--------|-------|---------|
| `gateway/docs/RUNBOOK-INCIDENTS.md` | NEW | 27,838 | Master incident-response runbook — 4 D-11 classes + Mental Model + Triagem + 8 sibling cross-refs |
| `gateway/docs/POSTMORTEM-TEMPLATE.md` | NEW | 6,517 | Google SRE blameless 9-section postmortem skeleton (D-10) |
| `gateway/docs/RUNBOOK-2FA-RECOVERY.md` | NEW | 11,987 | Admin 2FA device-loss + lost-backup-codes recovery procedure with separation-of-duty + audit-trail |

Total: 46,342 bytes across 3 files.

---

## Grep Gate Results (from plan `<verification>` block)

| Gate | Expected | Observed | Status |
|------|----------|----------|--------|
| 4 D-11 classes verbatim in RUNBOOK-INCIDENTS.md | `grep -q "Primary pod down\|OpenRouter / OpenAI degraded\|Audit/billing pipeline broken\|Rate-limit / quota lockout"` exits 0 | exits 0 | PASS |
| `## Triagem de Incidente Desconhecido` heading present | `grep -q "Triagem de Incidente Desconhecido"` exits 0 | exits 0 | PASS |
| POSTMORTEM-TEMPLATE.md cited within 5 lines after Triagem heading | `grep -A 5 'Triagem de Incidente Desconhecido' \| grep -q 'POSTMORTEM-TEMPLATE.md'` | 1 match | PASS |
| 2FA mentioned ≥ 3 times in RUNBOOK-INCIDENTS.md | ≥ 3 | 16 | PASS |
| "if available" defensive fallbacks in RUNBOOK-INCIDENTS.md | ≥ 4 (one per incident class) | 4 | PASS |
| `RUNBOOK-2FA-RECOVERY` cross-referenced in RUNBOOK-INCIDENTS.md | exits 0 | exits 0 | PASS |
| POSTMORTEM-TEMPLATE.md has exactly 9 numbered sections | `grep -c '^## [1-9]\. ' == 9` | 9 | PASS |
| RUNBOOK-2FA-RECOVERY.md separation-of-duty language | `grep -qE "separation of duty\|secondary channel"` exits 0 | matches | PASS |
| RUNBOOK-2FA-RECOVERY.md cross-refs `seed-admins.sh` | `grep -q "seed-admins.sh"` exits 0 | exits 0 | PASS |
| RUNBOOK-2FA-RECOVERY.md cross-refs the dashboard 2FA enroll flow | `grep -qE "2fa/enroll\|2FA enroll"` exits 0 | exits 0 | PASS |
| Commit `5bd79d1` cited in Class 3 (audit/billing pipeline broken) | `grep -q "5bd79d1"` exits 0 | exits 0 | PASS |
| ≥ 8 sibling runbook cross-references in RUNBOOK-INCIDENTS.md | ≥ 8 | 38 RUNBOOK- mentions covering 9 unique siblings | PASS |
| SQL/audit identifier coverage in RUNBOOK-2FA-RECOVERY.md | `grep -E "two_factor_enabled\|twoFactor\|reset-2fa\|audit_log" \| wc -l` ≥ 4 | 22 | PASS |

All 13 plan-level verification gates PASS.

---

## Reviews Findings Closed

| ID | Source | Severity | Description | Resolution |
|----|--------|----------|-------------|------------|
| LOW #1 | 11-REVIEWS Codex | LOW | "Depends on evidence files that may not exist if Wave 2 is partial." | Every Class Detection section adds an `if available` fallback (×4 — one per class) pointing to Phase 10 EVIDENCE files (`10-VERIFICATION.md`, `10-01-CAPACITY-OBSERVED.md`) OR to the planned re-test path in `.planning/phases/11-prod-hardening/11-VALIDATION.md`. Runbook ships intact even on `passed_partial` Wave 2. |
| LOW #2 | 11-REVIEWS Codex | LOW | "'Exactly four classes' is good, but runbook should include an 'other incident' escalation path." | `## Triagem de Incidente Desconhecido` H2 added as an entry-point (NOT a 5th class) with a 4-step triage protocol; body cites POSTMORTEM-TEMPLATE.md within the first 5 lines so the SEV-2 postmortem becomes a mandatory first artifact for any unknown incident. Document explicitly forbids creating a 5th class without a new D-XX decision. |
| Suggestion #3 | 11-REVIEWS Gemini | (suggestion) | "Consider adding a task for resetting an admin's 2FA in case they lose their device and backup codes." | Three deliverables: (a) RUNBOOK-2FA-RECOVERY.md dedicated runbook with separation-of-duty + audit-row-first invariants; (b) Class 4 sub-detection in RUNBOOK-INCIDENTS.md for admin 2FA lockout (same lockout shape as tenant rate-limit lockout); (c) Operator Recovery Procedures section cross-references the recovery runbook and pairs with `seed-admins.sh` (Plan 11-05) password reset. "2FA" appears 16 times in RUNBOOK-INCIDENTS.md across the three required surfaces. |

---

## Deviation: Synthesized from spec, not live chaos evidence

**This deviation is the headline operational fact for future audits.**

Plan 11-09 frontmatter declares `depends_on: [11-06, 11-07, 11-08]` —
the three Wave 2 live chaos / load UAT plans:

- **11-06** — PRD-01 load-test live UAT (replay 30 min, $1–3 Vast spend)
- **11-07** — PRD-02 chaos primary kill (Vast API DELETE)
- **11-08** — PRD-03 chaos OpenRouter DROP (container-netns iptables)

Those plans were **deferred by operator decision** at execute-phase
time (Phase 11, 2026-05-27). The operator approved synthesizing the
runbook NOW from:

1. The plan-defined 4 D-11 incident classes (see 11-CONTEXT.md D-11
   lines 47–50).
2. The Google SRE blameless 9-section template (see 11-CONTEXT.md D-10
   line 45).
3. The plan-spec mitigation flows for each class (`<action>` block in
   11-09-PLAN.md tasks 1 + 3).
4. The existing 7 sibling runbooks (RUNBOOK-DEPLOY, RUNBOOK-FAILOVER,
   RUNBOOK-PRIMARY-POD, RUNBOOK-EMERGENCY-POD,
   RUNBOOK-OBSERVABILITY-ALERTING, RUNBOOK-QUOTAS-BILLING,
   RUNBOOK-CLIENT-INTEGRATION, RUNBOOK-CLIENT-INTEGRATION-SENSITIVE)
   for tone, command shape, and cross-reference shape.

**The runbook was authored without consulting live chaos traces** because
the operator chose to defer the ~$1.50–3.50 Vast spend to a later
session. This deviation is explicitly anticipated by the plan via the
`[reviews LOW #1]` defensive evidence pattern: every Detection section
names the canonical Wave 2 evidence file `if available` AND falls back
to the closest Phase 10 EVIDENCE file or VALIDATION re-test path.

Operational consequence: when Wave 2 plans execute (later), the
operator MUST update each `Canonical example:` line in
RUNBOOK-INCIDENTS.md to drop the "if available" hedge once the
corresponding 11-06/07/08 EVIDENCE file is committed. A short
`docs(11): post-Wave2 evidence link-up` plan in the next cycle will
close this loop.

---

## Pattern Notes

### Pattern G runbook header (5 elements)

All 3 new docs follow the established Pattern G shape from
`RUNBOOK-FAILOVER.md` / `RUNBOOK-PRIMARY-POD.md`:

1. H1 title
2. Bold scope + bulleted read-when triggers
3. **Last updated: 2026-05-27.**
4. "Related runbooks" bullet list
5. `## Mental Model (30 seconds)`

### Diagnose → Mitigate → Verify per class

Each of the 4 incident classes in RUNBOOK-INCIDENTS.md follows the
exact 5-block shape inherited from RUNBOOK-FAILOVER.md "Quick Diagnosis"
flow:

- **Detection signals** (bulleted Prometheus + Sentry + curl + audit
  log signals) → includes the `if available` defensive evidence ref.
- **Diagnose** (numbered shell commands — gatewayctl / curl / psql).
- **Mitigate** (observation-first rule for Class 1; escape-hatch
  commands; sensitive-tenant invariants for Class 2).
- **Verify** (post-mitigation health checks tied to D-04 SLO).
- **Cross-ref** (links to the 2–3 sibling runbooks for deeper context).

### POSTMORTEM-TEMPLATE 9-section invariant

Section spelling locked per D-10:

1. Summary — 2-3 sentence on-call channel preview.
2. Impact — quantified table (tenants / requests / SLO budget / etc.).
3. Root Cause(s) — blameless system narrative + permalinks.
4. Trigger — specific event start.
5. Detection — TTD / TTA / TTR table + alert-fired-correctly flag.
6. Resolution — numbered timeline with runbook deviations called out.
7. Action Items — table skeleton (# / Action / Owner / Due / Status) +
   status legend.
8. Timeline — UTC-stamped events from Sentry + audit_log + operator
   notes.
9. Lessons Learned — durable insights not captured by single action
   items.

### Separation-of-duty in RUNBOOK-2FA-RECOVERY.md

Mitigation for threat T-11-DOC-03 (Elevation of Privilege via abusive
2FA reset) is encoded as three invariants surfaced at the top of the
Authorization rules section:

1. Locked-out admin requests via secondary channel (voice, in-person,
   password-manager-shared note) — text-only is forbidden.
2. Recovery executed by a DIFFERENT admin (separation of duty).
3. Audit row written BEFORE the SQL UPDATE (repudiation defense).

---

## Commits

| Hash | Subject |
|------|---------|
| `26b64f6` | `docs(11-09): author RUNBOOK-INCIDENTS.md (4 D-11 classes + Triagem + 2FA xref)` |
| `a6752a1` | `docs(11-09): author POSTMORTEM-TEMPLATE.md (Google SRE blameless 9-section)` |
| `92b58ca` | `docs(11-09): author RUNBOOK-2FA-RECOVERY.md (device-loss + lost-backup-codes)` |

---

## Cross-cutting Truths Honored

- **Truth 1 — Zero new server-side runtime dependencies.** Doc-only
  plan; no `package.json` / `go.mod` / Python `requirements.txt`
  touched.
- **Truth 2 — pt-BR/en mixed acceptable.** "Triagem de Incidente
  Desconhecido" deliberately Portuguese per reviews LOW #2 wording;
  rest of the runbook is English for operator audience; pt-BR
  retained inside SQL placeholder strings only.
- **Truth 5 — Plan autonomous.** No async review gate; executor
  authored all 3 docs and committed atomically.
- **Truth 8 — No raw secrets in committed runbooks.** All SQL
  snippets use `{LOCKED_OUT_EMAIL}`, `{EXECUTING_ADMIN_EMAIL}`,
  `{TENANT_SLUG}` placeholders; no real emails, no real DSNs, no
  real API keys. Real keys live only in `~/.claude/CLAUDE.md`
  (local-only) and Portainer / `/opt/ai-gateway-prod/.env`.

---

## Threat Surface Notes

No new attack surface introduced (doc-only plan). The new
RUNBOOK-2FA-RECOVERY.md *documents* an existing recovery path that
operators would otherwise improvise — writing it down with
separation-of-duty + audit invariants *reduces* exposure to T-11-DOC-03.

---

## Self-Check

Created files exist on disk:

- `gateway/docs/RUNBOOK-INCIDENTS.md` — FOUND (27,838 bytes)
- `gateway/docs/POSTMORTEM-TEMPLATE.md` — FOUND (6,517 bytes)
- `gateway/docs/RUNBOOK-2FA-RECOVERY.md` — FOUND (11,987 bytes)

Commits exist in git log:

- `26b64f6` — FOUND (RUNBOOK-INCIDENTS.md)
- `a6752a1` — FOUND (POSTMORTEM-TEMPLATE.md)
- `92b58ca` — FOUND (RUNBOOK-2FA-RECOVERY.md)

All 13 plan-level verification gates PASS (see Grep Gate Results
table above).

## Self-Check: PASSED
