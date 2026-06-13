# Postmortem: {Incident Title}

**Date:** YYYY-MM-DD
**Duration:** {start UTC} → {end UTC} ({wall-clock minutes})
**Severity:** SEV-{1|2|3}
**Author:** {operator email or handle}
**Status:** draft | review | published

> **Blameless.** This template follows Google SRE blameless postmortem
> convention. Describe systems, decisions, and signals — never assign
> blame to a named person. If a human decision is part of the
> narrative, frame it as the system that put the human into that
> position (training, runbook gap, alert deficit, …).
>
> **Severity guide** (use the lowest applicable):
> - **SEV-1** — customer data loss, full outage > 5 min, LGPD breach,
>   or any sensitive-tenant data leak.
> - **SEV-2** — partial outage, SLO breach > 15 min on any single
>   route, or audit/billing pipeline gap.
> - **SEV-3** — single-tenant impact, no SLO breach, internal-only
>   dashboard / tooling regression.

---

## 1. Summary

{2–3 sentences. What broke, who was affected, how long, root cause
one-liner. Optimised for the on-call channel auto-preview — should be
readable in 10 seconds.}

---

## 2. Impact

{Tenants affected, requests failed, revenue impact (if any), SLO budget
burned. Quantify everything that can be quantified.}

| Dimension                  | Value |
|----------------------------|-------|
| Tenants affected           | {slug list or count} |
| Requests failed            | {count + % of window total} |
| Routes affected            | {list — `/v1/chat/completions`, `/v1/audio/transcriptions`, etc.} |
| SLO budget burned (P95)    | {minutes over D-04 threshold} |
| Audit/billing rows lost    | {count or "0"} |
| User-facing communication  | {sent at HH:MM UTC via {channel} / none} |

---

## 3. Root Cause(s)

{Technical cause(s). Blameless — describe systems, not people.
Multiple root causes are common; list every contributing factor that
must be true for the incident to recur. Cite commits, configs, and
runbook lines with permalinks.}

- **Primary cause:** …
- **Contributing cause #1:** …
- **Contributing cause #2:** …

---

## 4. Trigger

{The specific event that started the incident. Deploy SHA + tag,
traffic spike (with magnitude), dependency change, schedule transition,
… Be specific enough that a second reader can rebuild the timeline.}

---

## 5. Detection

{How was the incident first noticed? Page time, automated alert vs
client report vs internal observation. Quote the alert message or the
client message verbatim where possible.}

| Aspect                | Value |
|-----------------------|-------|
| First signal source   | {Sentry alert / Prometheus rule / client WhatsApp / internal observation} |
| Time-to-detect (TTD)  | {trigger → detection, minutes} |
| Time-to-acknowledge   | {detection → on-call ACK, minutes} |
| Time-to-resolve (TTR) | {detection → resolution, minutes} |
| Alert fired correctly? | yes / no — if no, action item to fix the alert |

---

## 6. Resolution

{Step-by-step what fixed it. Cite commits, runbook steps, and operator
commands. Include the runbook(s) consulted and any deviation from the
documented procedure (deviations are postmortem actionable items).}

1. {first action taken, timestamp UTC}
2. {second action, timestamp UTC}
3. …

Final state restored: {confirmation that SLOs are back to baseline
with link to dashboard or query results}.

---

## 7. Action Items

{Deliverables — link to GitHub issues; owners + due dates. Action
items are the contract between the postmortem and the next planning
cycle. Every "we got lucky" or "the runbook didn't cover this" finding
must produce at least one action item.}

| # | Action | Owner | Due | Status |
|---|--------|-------|-----|--------|
| 1 |        | @user | YYYY-MM-DD | open |
| 2 |        | @user | YYYY-MM-DD | open |
| 3 |        | @user | YYYY-MM-DD | open |

**Status legend:** `open` → `in_progress` → `done` → `cancelled` (with
justification in the cell).

---

## 8. Timeline

{UTC timestamps, terse events. Pull from Sentry, audit_log, operator
notes, WhatsApp escalation thread. Aim for one event per minute of
incident wall-clock; never abbreviate the duration to "approximately".}

| UTC time           | Event |
|--------------------|-------|
| YYYY-MM-DDTHH:MM:SSZ | {event} |
| YYYY-MM-DDTHH:MM:SSZ | {event} |
| YYYY-MM-DDTHH:MM:SSZ | {event} |

---

## 9. Lessons Learned

{Insights that don't map to a single action item — "the system
surprised us by …", "the runbook assumed X but the world had Y",
"this is the third time we've seen a variant of Z". This section is
where the team's collective intuition gets durably recorded; future
postmortems will reference it.}

- {lesson 1}
- {lesson 2}
- {lesson 3}

---

## How to use this template

1. **Copy, don't edit.** From the repo root:

   ```bash
   DATE=$(date -u +%Y-%m-%d)
   SLUG=primary-pod-down       # short kebab-case incident slug
   cp gateway/docs/POSTMORTEM-TEMPLATE.md \
      .planning/postmortems/postmortem-${DATE}-${SLUG}.md
   ```

2. **Fill in per incident.** Each instance gets its own committed
   file under `.planning/postmortems/postmortem-{YYYY-MM-DD}-{slug}.md`.

3. **Never edit this template in-place to "fix" a single postmortem.**
   Treat the template as authoritative; fixes go through a regular
   doc plan that updates the template AND back-references every
   existing instance.

4. **Cross-ref.** Every postmortem links back to the incident class in
   [`RUNBOOK-INCIDENTS.md`](./RUNBOOK-INCIDENTS.md) that triggered it
   (or to the "Triagem de Incidente Desconhecido" section if the
   incident did not match an existing class). 2FA-recovery–related
   postmortems also cite [`RUNBOOK-2FA-RECOVERY.md`](./RUNBOOK-2FA-RECOVERY.md).

5. **PII discipline.** No raw API keys, no tenant payloads, no
   request bodies, no DSNs in the committed postmortem. Replace with
   placeholders (`{TENANT_SLUG}`, `{REQUEST_ID}`, `{ADMIN_EMAIL}`).

6. **Section invariant.** The 9 numbered sections (Summary, Impact,
   Root Cause(s), Trigger, Detection, Resolution, Action Items,
   Timeline, Lessons Learned) are the D-10 invariant — do not add or
   omit sections. If a section is empty for a given incident, write
   "Not applicable for this incident class." rather than removing the
   heading.

---

*Phase 11 (`ifix-ai-gateway`) postmortem template — Google SRE
blameless 9-section, D-10. Source-of-truth lives at
`gateway/docs/POSTMORTEM-TEMPLATE.md`; instance files live at
`.planning/postmortems/`.*
