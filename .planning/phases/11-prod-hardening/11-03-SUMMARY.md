---
phase: 11-prod-hardening
plan: 11-03
subsystem: docs
tags: [lgpd, compliance, signoff, subprocessors, minio, jurídico, pt-br]

# Dependency graph
requires:
  - phase: 09-client-integration-sensitive-tenants-telefonia-cobran-as-campanhas-voice-api
    provides: LGPD-SUBPROCESSORS.md (3-row canonical) + LGPD-REVIEW-CHECKLIST.md
provides:
  - LGPD sign-off process document (Quem assina / O que é entregue / Onde arquivar / Cadência)
  - Legal letter template with placeholders ({{TENANT_SLUG}}, {{DATA_CLASS}}, {{SIGNATORY_*}}, {{SUBPROCESSORS_REVIEW_DATE}}, {{SUBPROCESSORS_REVIEW_SHA}})
  - 4th sub-processor (MinIO) added to canonical LGPD-SUBPROCESSORS.md with hedge wording
  - Evidence file convention .planning/legal/lgpd-signoff-{YYYY-MM-DD}-{tenant}.pdf (gitignored)
affects: [11-09 RUNBOOK-INCIDENTS LGPD class refs, 11-10 HUMAN-UAT PRD-05 closure gate, future tenant ativação sensível]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - LGPD doc voice (Pattern H — pt-BR inherited from LGPD-SUBPROCESSORS.md)
    - Dated cross-ref placeholder pattern (revisão {YYYY-MM-DD} + commit {SHA}) for drift prevention between letter and canonical
    - Hedge-wording mirror invariant: same classification phrase in letter and canonical doc

key-files:
  created:
    - gateway/docs/LGPD-SIGNOFF-PROCESS.md
    - gateway/docs/LGPD-SIGNOFF-LETTER-TEMPLATE.md
    - .planning/legal/.gitignore
  modified:
    - gateway/docs/LGPD-SUBPROCESSORS.md

key-decisions:
  - "[reviews MEDIUM #1] MinIO classification hedged as 'componente de infraestrutura (sub-processador se aplicável — confirmar com jurídico)' in BOTH letter template and canonical doc — jurídico Ifix retains final classification authority since MinIO is internally self-hosted (s3.ifixtelecom.com.br)"
  - "[reviews LOW #2] Legal letter contains ZERO inline model/routing slugs (Qwen/Novita/gpt-X/Whisper/BGE-M3/Chatterbox/llama); current model+provider details delegated to LGPD-SUBPROCESSORS.md via dated revisão+SHA cross-ref so each signed letter anchors to a specific canonical-doc revision"
  - "Evidence path convention .planning/legal/lgpd-signoff-{YYYY-MM-DD}-{tenant}.pdf gitignored at directory level (.planning/legal/.gitignore *.pdf) instead of relying solely on root .gitignore — defense in depth for T-11-LGPD-01"
  - "D-17 external sign-off gate explicitly stated as out-of-scope for code work — Phase 11 ships only the material jurídico needs, not the signed PDF itself"

patterns-established:
  - "Pattern: hedge-wording mirror — same classification phrase carried verbatim in letter template AND canonical doc; updates require coordinated edit to both files"
  - "Pattern: dated cross-ref placeholder — letter delegates volatile details (model/provider slugs) to canonical doc with {{REVIEW_DATE}}+{{REVIEW_SHA}} placeholders so drift surfaces at next review"

requirements-completed: [PRD-05]

# Metrics
duration: ~25 min
completed: 2026-05-27
---

# Phase 11 Plan 11-03: LGPD Sign-off Docs Summary

**PRD-05 LGPD doc-only deliverables shipped: sign-off process document, jurídico-ready letter template (placeholders only, 4 sub-processor companies enumerated, MinIO classification hedged for legal review), and canonical LGPD-SUBPROCESSORS.md extended with the MinIO 4th row so letter and canonical agree.**

## Performance

- **Duration:** ~25 min
- **Started:** 2026-05-27T18:00Z (worktree spawn approx)
- **Completed:** 2026-05-27T18:22:14Z
- **Tasks:** 3 / 3
- **Files modified:** 4 (3 created + 1 modified)

## Accomplishments

- New `gateway/docs/LGPD-SIGNOFF-PROCESS.md` (64 lines, 3022 bytes) with all 4 required H2 sections (Quem assina / O que é entregue / Onde arquivar / Cadência) plus Referências cruzadas.
- New `gateway/docs/LGPD-SIGNOFF-LETTER-TEMPLATE.md` (102 lines, 5744 bytes) — legal-ready pt-BR letter listing all 4 sub-processor companies (Vast.ai, OpenAI, OpenRouter, MinIO), MinIO hedged with the classification-pending wording, ZERO inline model slugs, dated cross-ref to canonical doc.
- Modified `gateway/docs/LGPD-SUBPROCESSORS.md` (90 lines) — added MinIO as 4th sub-processor row, bumped `Last updated` to 2026-05-27, added data-path rationale paragraph spelling out the hedge.
- `.planning/legal/.gitignore` (`*.pdf`) — directory-level reinforcement that signed PDFs never enter git (T-11-LGPD-01 mitigation; root `.gitignore` does not yet carry this rule, so the directory-level file is the active line of defense).

## Task Commits

Each task was committed atomically:

1. **Task 11-03-01: LGPD-SIGNOFF-PROCESS.md authoring + .planning/legal/.gitignore** — `5c32802` (docs)
2. **Task 11-03-02: LGPD-SIGNOFF-LETTER-TEMPLATE.md authoring with MinIO hedge + zero-slug invariant** — `5aa54b8` (docs)
3. **Task 11-03-03: MinIO row + Last updated bump in canonical LGPD-SUBPROCESSORS.md** — `21cca0b` (docs)

## Files Created/Modified

- `gateway/docs/LGPD-SIGNOFF-PROCESS.md` (NEW, 64 lines / 3022 bytes) — sign-off process document; quem assina / o que é entregue / onde arquivar / cadência.
- `gateway/docs/LGPD-SIGNOFF-LETTER-TEMPLATE.md` (NEW, 102 lines / 5744 bytes) — legal-ready letter template with 10 placeholder fields + 4 sub-processor company enumeration + MinIO hedge + dated canonical cross-ref + RES-08 never-external callout.
- `gateway/docs/LGPD-SUBPROCESSORS.md` (MOD) — Last updated header bumped 2026-05-14 → 2026-05-27; new MinIO row added between OpenRouter row and the "Garantia never-external" callout; data-path rationale paragraph added.
- `.planning/legal/.gitignore` (NEW, 1 line) — `*.pdf` directory marker so signed PDFs are gitignored regardless of root `.gitignore` content.

## Acceptance Evidence

### (a) PRD-05 4-sub-processor company grep gate (LETTER)

```
$ grep -c 'Vast.ai\|OpenAI\|OpenRouter\|MinIO' gateway/docs/LGPD-SIGNOFF-LETTER-TEMPLATE.md
5
```

Lines hit: 37 (Vast.ai), 41 (OpenAI), 44 (OpenRouter), 48 (MinIO row), 52 (MinIO classification note). PRD-05 grep gate per `11-RESEARCH.md` line 654 requires ≥4 — PASS.

### (b) Canonical doc MinIO addition

```
$ grep -n 'MinIO' gateway/docs/LGPD-SUBPROCESSORS.md
26:| **MinIO — componente de infraestrutura (sub-processador se aplicável — confirmar com jurídico)** | ...
28:> **MinIO bucket `ai-gateway` em `s3.ifixtelecom.com.br` armazena pesos GGUF/safetensors ...

$ grep -c '^| \*\*' gateway/docs/LGPD-SUBPROCESSORS.md
4
```

Table rows starting with `| **` went from 3 → 4 (PASS — verify gate requires ≥4).

### (c) [reviews MEDIUM #1] MinIO classification hedge in BOTH files

```
$ grep -nE 'componente de infraestrutura.*sub-processador se aplicável.*confirmar com jurídico' \
    gateway/docs/LGPD-SIGNOFF-LETTER-TEMPLATE.md gateway/docs/LGPD-SUBPROCESSORS.md
gateway/docs/LGPD-SUBPROCESSORS.md:26:| **MinIO — componente de infraestrutura (sub-processador se aplicável — confirmar com jurídico)** | ...
gateway/docs/LGPD-SIGNOFF-LETTER-TEMPLATE.md:48:- **MinIO — componente de infraestrutura (sub-processador se aplicável — confirmar com jurídico)** ...
```

Hedge phrase present verbatim in BOTH files — letter and canonical agree on classification posture; jurídico retains final classification authority. T-11-LGPD-04 mitigated.

### (d) [reviews LOW #2] Zero model/routing slugs in LETTER

```
$ grep -cEi 'Qwen|Novita|gpt-[345]|Whisper|BGE-M3|Chatterbox|llama' gateway/docs/LGPD-SIGNOFF-LETTER-TEMPLATE.md
0

$ grep -n 'SUBPROCESSORS_REVIEW' gateway/docs/LGPD-SIGNOFF-LETTER-TEMPLATE.md
61:Os modelos e provedores upstream atuais estão documentados em `gateway/docs/LGPD-SUBPROCESSORS.md` (revisão `{{SUBPROCESSORS_REVIEW_DATE}}`, commit `{{SUBPROCESSORS_REVIEW_SHA}}`).
```

Letter contains ZERO inline model slugs; dated revisão+SHA cross-ref placeholders present on a single line so the regex `revisão.*{{SUBPROCESSORS_REVIEW_DATE}}.*commit.*{{SUBPROCESSORS_REVIEW_SHA}}` matches. T-11-LGPD-03 drift between letter and canonical eliminated.

### Evidence file gitignore

```
$ git check-ignore .planning/legal/lgpd-signoff-2026-05-27-telefonia.pdf
.planning/legal/lgpd-signoff-2026-05-27-telefonia.pdf
```

PDF path returned (gitignore active) — T-11-LGPD-01 mitigated.

## Decisions Made

- **MinIO classification posture (reviews MEDIUM #1):** Hedged as "componente de infraestrutura (sub-processador se aplicável — confirmar com jurídico)" in BOTH letter and canonical doc. Rationale: MinIO is internally self-hosted at `s3.ifixtelecom.com.br` with no external vendor — the binary "sub-processador formal" vs "internal infra component" call belongs to jurídico Ifix, not the platform team. Full-disclosure inclusion preserves the audit trail without pre-empting legal classification.
- **Zero inline model slugs in letter (reviews LOW #2):** Current model SKUs and provider routing details (Qwen 3.5 27B, Novita, OpenAI Whisper, BGE-M3, etc.) live ONLY in `LGPD-SUBPROCESSORS.md`; the letter cross-refs them via dated `{{SUBPROCESSORS_REVIEW_DATE}}` + `{{SUBPROCESSORS_REVIEW_SHA}}` placeholders. Rationale: models rotate between phases (Qwen 3.5 → Qwen 3.6 in Phase 06.6 already happened); inlining slugs in a legal letter would create drift at every model rollover. Anchoring each signature to a canonical-doc revision is the drift-prevention contract.
- **Directory-level `.planning/legal/.gitignore`:** Added the marker file with `*.pdf` even though the plan says root `.gitignore` already covers it. Verification revealed the root `.gitignore` does NOT currently contain a `.planning/legal/*.pdf` rule (Plan 11-01 has not yet been executed in this worktree), so the directory-level file is the active defense. Defense in depth — once Plan 11-01 lands the root rule, both layers will be active.
- **Placeholder set ≥ minimum:** Letter includes 10 placeholder fields ({{TENANT_SLUG}}, {{DATA_CLASS}}, {{SIGNOFF_DATE}}, {{SIGNATORY_DPO}}, {{SIGNATORY_LEGAL}}, {{SIGNATORY_PLATFORM}}, {{LEGAL_BASIS}}, {{RETENTION_PERIOD}}, {{SUBPROCESSORS_REVIEW_DATE}}, {{SUBPROCESSORS_REVIEW_SHA}}) — exceeds the ≥4 floor by design; the legal-basis and retention-period fields give jurídico explicit slots to fill.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Two grep gates were failing because pt-BR sentences were wrapped across multiple lines**

- **Found during:** Task 11-03-02 (letter authoring) verification step
- **Issue:** The verify gates `grep -qE 'componente de infraestrutura.*sub-processador se aplicável.*confirmar com jurídico'` and `grep -q 'revisão.*{{SUBPROCESSORS_REVIEW_DATE}}.*commit.*{{SUBPROCESSORS_REVIEW_SHA}}'` are line-oriented; the initial draft had the MinIO hedge and the dated cross-ref split across two lines for prose wrap, so both greps returned non-zero exit and the plan's `<verify><automated>` expression failed.
- **Fix:** Joined the two affected sentences into single physical lines while preserving Markdown rendering (Markdown collapses consecutive non-empty lines into the same paragraph anyway, so semantics are unchanged for human readers but `grep` now matches).
- **Files modified:** `gateway/docs/LGPD-SIGNOFF-LETTER-TEMPLATE.md` (lines 48 — MinIO hedge bullet; lines 61 — dated cross-ref sentence)
- **Verification:** Re-ran the full `<verify><automated>` expression — `full <verify><automated> PASSED`.
- **Committed in:** `5aa54b8` (the fix landed inside the Task 11-03-02 commit, not a separate commit, because the bug was caught before commit)

---

**Total deviations:** 1 auto-fixed (1 blocking — grep-gate line wrap)
**Impact on plan:** Auto-fix was a pure cosmetic line-join; semantic content of the legal letter is unchanged. No scope creep, no extra files, no extra deps.

## Issues Encountered

- None beyond the line-wrap grep-gate issue documented above. The plan was very prescriptive (verbatim hedge wording, verbatim placeholder names) which made execution mechanical once the line-orientation of the verify greps was respected.

## Known Stubs

None — this plan is pure documentation. Template placeholders (`{{TENANT_SLUG}}`, `{{SUBPROCESSORS_REVIEW_DATE}}`, etc.) are NOT stubs; they are the contract for jurídico to fill when signing a real letter. They are intentional and documented.

## User Setup Required

None — pure docs. The external action this plan enables (jurídico of Ifix signing a real letter using the template) is the D-17 external gate and is explicitly out of scope for code work.

## Next Phase Readiness

- PRD-05 doc-only deliverables done from the platform team side. Phase 11 (or any future plan) can now hand the three files to jurídico Ifix when activating a `data_class: sensitive` tenant.
- 11-09 RUNBOOK-INCIDENTS.md (in this same phase) can cross-ref `LGPD-SIGNOFF-PROCESS.md` if the runbook surfaces a sub-processor-change incident.
- No blockers introduced. The MinIO hedge gives jurídico an explicit decision to make (classify formally or not) instead of pre-empting it.

## Self-Check: PASSED

- gateway/docs/LGPD-SIGNOFF-PROCESS.md — FOUND
- gateway/docs/LGPD-SIGNOFF-LETTER-TEMPLATE.md — FOUND
- gateway/docs/LGPD-SUBPROCESSORS.md — FOUND (modified)
- .planning/legal/.gitignore — FOUND
- Commit `5c32802` — FOUND (`docs(11-03): add LGPD-SIGNOFF-PROCESS.md and legal evidence directory`)
- Commit `5aa54b8` — FOUND (`docs(11-03): add LGPD-SIGNOFF-LETTER-TEMPLATE.md`)
- Commit `21cca0b` — FOUND (`docs(11-03): add MinIO as 4th sub-processor in LGPD-SUBPROCESSORS.md`)
- All `<verify><automated>` expressions PASSED
- All `<verification>` phase gates PASSED
- 4-sub-processor PRD-05 grep gate: 5 line hits (≥4 required) PASSED
- MEDIUM #1 hedge in BOTH files: PASSED
- LOW #2 zero model slugs in letter + dated cross-ref placeholders: PASSED

---
*Phase: 11-prod-hardening*
*Plan: 11-03*
*Completed: 2026-05-27*
