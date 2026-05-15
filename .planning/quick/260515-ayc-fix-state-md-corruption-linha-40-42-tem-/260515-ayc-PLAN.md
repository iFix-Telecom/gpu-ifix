---
phase: quick-260515-ayc
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - .planning/STATE.md
autonomous: true
requirements:
  - QUICK-FIX-STATE-CORRUPTION
must_haves:
  truths:
    - "STATE.md no longer contains a duplicate `## Current Position` heading inside the Phase 6 bullet."
    - "The Phase 6 bullet on line 40 ends with `~R$10-15). No 06-11-SUMMARY.md, no 06-VERIFICATION.md yet.` on a single line."
    - "All other bytes of STATE.md (frontmatter, ToC, the legitimate `## Current Position` heading on line 28, every other section) are unchanged."
  artifacts:
    - path: ".planning/STATE.md"
      provides: "Corrected project state file with surgical fix to corrupted Phase 6 bullet"
      contains: "~R$10-15). No 06-11-SUMMARY.md"
  key_links:
    - from: ".planning/STATE.md line 28"
      to: "the only `## Current Position` heading in the file"
      via: "grep -c '^## Current Position'"
      pattern: "exactly 1 match"
---

<objective>
Surgical 1-line repair of `.planning/STATE.md`. A duplicate `## Current Position` heading was accidentally injected mid-sentence inside the Phase 6 bullet, splitting `~R$10-15).` across lines 40–42 and eating both the `$` and the `1` characters.

Purpose: Restore STATE.md to a parseable, semantically correct state so the GSD state loader and humans can read the Phase 6 bullet without confusion. The duplicate heading also breaks any tooling that counts `## Current Position` occurrences.

Output: `.planning/STATE.md` with the corruption replaced by the intended single-line text `~R$10-15).` joining the broken bullet back together. No other changes.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
</execution_context>

<context>
@.planning/STATE.md

<corruption_evidence>
Verified via `sed -n '40,42p' .planning/STATE.md | cat -A`:

```
- **Phase 6:** 10/11 plans executed (06-01..06-10 GREEN + summaries). 06-11 is `autonomous: false` HUMAN-UAT — Tasks 1+2 done (06-HUMAN-UAT.md + docs/RUNBOOK-EMERGENCY-POD.md created, commit 2b539fc); Task 3 is a **blocking** human-verify checkpoint (6 LIVE Vast.ai UAT scenarios, ~R## Current Position$
$
0-15). No 06-11-SUMMARY.md, no 06-VERIFICATION.md yet.$
```

Three consecutive lines (40, 41-blank, 42) form the corrupted region. The `$` markers at line ends are from `cat -A` (newline indicators), not literal dollar signs in the text. The literal `$` that belongs to `~R$10-15)` was eaten when the heading was injected, as was the `1` in `10-15`. Note: line 42 starts with `0-15).` (no `1` prefix). After the fix, the joined line must read `~R$10-15).`.

The legitimate `## Current Position` heading is on line 28 and must stay untouched.
</corruption_evidence>

<target_state>
After the edit, the Phase 6 bullet must occupy a single line (line 40 in the original, the file shrinks by 2 lines after the fix):

```
- **Phase 6:** 10/11 plans executed (06-01..06-10 GREEN + summaries). 06-11 is `autonomous: false` HUMAN-UAT — Tasks 1+2 done (06-HUMAN-UAT.md + docs/RUNBOOK-EMERGENCY-POD.md created, commit 2b539fc); Task 3 is a **blocking** human-verify checkpoint (6 LIVE Vast.ai UAT scenarios, ~R$10-15). No 06-11-SUMMARY.md, no 06-VERIFICATION.md yet.
```

The next line (was line 43, becomes line 41) continues unchanged: `  - **Integration tests (emerg suite): RESOLVED 2026-05-14.** ...`
</target_state>
</context>

<tasks>

<task type="auto">
  <name>Task 1: Repair the injected heading + restore $10 in the Phase 6 bullet</name>
  <files>.planning/STATE.md</files>
  <action>Use the Edit tool to replace the corrupted 3-line region with the intended single line. The old_string must be the exact byte sequence currently spanning lines 40–42 (ending of the bullet up through `0-15).`), and the new_string is the joined, repaired form.

old_string (exactly, preserving the em-dash U+2014 and the literal newlines between the three lines):
`HUMAN-UAT — Tasks 1+2 done (06-HUMAN-UAT.md + docs/RUNBOOK-EMERGENCY-POD.md created, commit 2b539fc); Task 3 is a **blocking** human-verify checkpoint (6 LIVE Vast.ai UAT scenarios, ~R## Current Position\n\n0-15).`

new_string:
`HUMAN-UAT — Tasks 1+2 done (06-HUMAN-UAT.md + docs/RUNBOOK-EMERGENCY-POD.md created, commit 2b539fc); Task 3 is a **blocking** human-verify checkpoint (6 LIVE Vast.ai UAT scenarios, ~R$10-15).`

Net effect: remove the injected `## Current Position` heading and the blank line after it, prepend the missing `$1` to `0-15).` so it becomes `$10-15).`, and collapse what were three lines (line 40 ended mid-sentence, line 41 was blank, line 42 began with `0-15).`) into a single continuous bullet line.

Do NOT touch the frontmatter, the legitimate `## Current Position` heading on line 28, or any other text. Do NOT commit — the orchestrator handles the git commit in Step 8 of the quick workflow.</action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix && [ "$(grep -c '^## Current Position$' .planning/STATE.md)" = "1" ] && grep -q '~R\$10-15)\. No 06-11-SUMMARY\.md' .planning/STATE.md && ! grep -q '0-15)\. No 06-11-SUMMARY' .planning/STATE.md || (echo "Verify FAILED" && exit 1)</automated>
  </verify>
  <done>
    - `grep -c '^## Current Position$' .planning/STATE.md` returns exactly `1` (only the legitimate line-28 heading remains).
    - `grep` finds the literal string `~R$10-15). No 06-11-SUMMARY.md` in STATE.md on a single line.
    - The Phase 6 bullet is a single continuous line; no blank line splits it.
    - `git diff --stat .planning/STATE.md` shows roughly `1 file changed, 1 insertion(+), 3 deletions(-)` (the 3 broken lines collapsed into 1 repaired line).
    - No other lines of STATE.md changed (`git diff .planning/STATE.md` shows only the targeted region).
  </done>
</task>

</tasks>

<verification>
1. Run the automated check from the task `<verify>` block — must pass.
2. Manually inspect the diff: `git diff .planning/STATE.md` should show only the three corrupted lines being replaced by the one repaired line. Anything else means the executor strayed outside scope.
3. Confirm the legitimate `## Current Position` heading (line 28 of the original file) still exists and is followed by the `Phase: 09 ...` block.
</verification>

<success_criteria>
- `.planning/STATE.md` has exactly one `## Current Position` heading.
- The Phase 6 bullet reads `... ~R$10-15). No 06-11-SUMMARY.md, no 06-VERIFICATION.md yet.` on a single line.
- `git diff .planning/STATE.md` shows ONLY the targeted region changed.
- No commit made by the executor (the orchestrator commits in its Step 8).
</success_criteria>

<output>
After completion, the executor returns control to the `/gsd-quick` orchestrator. No SUMMARY file is required for a single-task quick fix; the orchestrator will commit `.planning/STATE.md` along with the quick directory artifacts in Step 8.
</output>
