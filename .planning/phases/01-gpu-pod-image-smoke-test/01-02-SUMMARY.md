---
phase: 01-gpu-pod-image-smoke-test
plan: 02
subsystem: infra

tags: [qwen3.5, llama.cpp, jinja, tool-calling, sha-256, chat-template]

# Dependency graph
requires: []
provides:
  - Qwen 3.5 27B tool-calling Jinja template (community-patched, committed verbatim to pod/templates/)
  - SHA-256 sidecar for drift detection (T-01-02-01 mitigation)
  - Operator documentation (pt-BR) covering provenance, validation, replacement, and upstream review cadence
affects:
  - 01-03 (pod Dockerfile will COPY pod/templates/ into /app/templates/)
  - 01-04 (pod docker-compose.yml passes --chat-template-file to llama-server)
  - 01-06 (smoke.py validates tool_call_valid behavior against this template)
  - 01-08 (smoke.yml gates image promotion based on tool_call_valid per D-19)

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "External artifact adoption with provenance header + SHA-256 drift pinning (applicable to future upstream vendoring)"

key-files:
  created:
    - pod/templates/qwen3.5-27b-tool-calling.jinja
    - pod/templates/qwen3.5-27b-tool-calling.jinja.sha256
    - pod/templates/README.md
  modified: []

key-decisions:
  - "Template fetched from the canonical sudoingX gist (id c2facf7d8f7608c65c1024ef3b22d431), committed byte-for-byte; only a Jinja block-comment provenance header was prepended."
  - "SHA-256 sidecar stores the digest of the FINAL file (including header), so any edit — header or body — is detected by CI/smoke drift checks."

patterns-established:
  - "Provenance header convention for vendored external Jinja/config: {# ... #} block with Source, Fetched, Purpose, Provenance, Review cadence, Validation fields"
  - "Sidecar SHA-256 format: single 64-hex-digit line, no filename suffix, trailing newline — compatible with `sha256sum -c` via inline printf"

requirements-completed: [POD-05]

# Metrics
duration: ~4min
completed: 2026-04-17
---

# Phase 01 Plan 02: Qwen 3.5 27B tool-calling Jinja template Summary

**Community-patched Qwen 3.5 27B tool-calling Jinja template (8595 bytes) committed verbatim from sudoingX gist, pinned by SHA-256 drift-detection sidecar, with pt-BR operator README covering provenance and D-16 review cadence.**

## Performance

- **Duration:** ~4 min
- **Started:** 2026-04-17T22:58:21Z
- **Completed:** 2026-04-17T23:00:37Z
- **Tasks:** 2 / 2
- **Files created:** 3

## Accomplishments

- Downloaded the canonical Qwen 3.5 27B tool-calling Jinja template from the sudoingX gist (`c2facf7d8f7608c65c1024ef3b22d431`) via GitHub API + raw URL.
- Prepended a Jinja block-comment provenance header (source URL, fetch date `2026-04-17`, D-14/D-15/D-16 rationale, smoke-test validation reference) while keeping the gist body byte-for-byte.
- Computed SHA-256 over the final file and wrote `pod/templates/qwen3.5-27b-tool-calling.jinja.sha256` (single-line digest, no filename suffix) to satisfy threat T-01-02-01 (tampering — high severity, mitigate).
- Authored `pod/templates/README.md` in pt-BR with sections: Arquivos, Por que este template existe (D-14), Como o template é consumido, Revisão upstream (D-16), Substituição do template, Validação (D-15, D-19).

## Task Commits

Each task was committed atomically (--no-verify per worktree parallel-executor policy):

1. **Task 1: Download + commit template + SHA-256 sidecar** — `5067af8` (feat)
2. **Task 2: Write pod/templates/README.md** — `cbe1242` (docs)

## Files Created

- `pod/templates/qwen3.5-27b-tool-calling.jinja` (8595 bytes) — Qwen 3.5 27B tool-calling Jinja template with 11-line provenance header (Jinja block comment) + 7876-byte verbatim gist body.
- `pod/templates/qwen3.5-27b-tool-calling.jinja.sha256` (65 bytes) — Single-line SHA-256 digest of the template file, trailing newline.
- `pod/templates/README.md` — Operator documentation (pt-BR) covering provenance, llama-server wiring, upstream review cadence, replacement procedure, and validation gates.

## Artifact Metadata

| Field | Value |
|---|---|
| Gist ID | `c2facf7d8f7608c65c1024ef3b22d431` |
| Gist author | `sudoingX` |
| Gist filename (raw) | `qwen3.5_chat_template.jinja` |
| Raw URL resolved | `https://gist.githubusercontent.com/sudoingX/c2facf7d8f7608c65c1024ef3b22d431/raw/4a09f1c37716e5888cc88512bf727c2568227c11/qwen3.5_chat_template.jinja` |
| Gist revision hash (path segment) | `4a09f1c37716e5888cc88512bf727c2568227c11` |
| Gist body size | 7876 bytes |
| Final file size | 8595 bytes (header adds 719 bytes) |
| SHA-256 of final file | `1067302cc6d927210a84775b9a060f724da15debc168c79710cbf763512e9f67` |
| Fetch date (header) | 2026-04-17 |

## Decisions Made

- **Raw URL resolved via GitHub API, not hardcoded.** Used `curl https://api.github.com/gists/<id>` + `jq` to extract the current raw URL (revision pinned: `4a09f1c3...`). This captures the exact revision seen during the fetch and is reproducible. The pinned SHA-256 catches any downstream revisions if a future re-fetch is attempted without updating the header.
- **Header format: Jinja block-comment (`{# ... #}`) on a single contiguous block at the top.** Chosen because (a) the Jinja renderer in llama.cpp silently strips block comments at render time (no runtime overhead, no stray whitespace in the rendered prompt), and (b) operators can grep the file directly without having to read raw gist JSON.

## Deviations from Plan

**1. [Rule 1 - Spec ambiguity] README H2 count: plan action lists 6 H2 headings but acceptance criteria say "7 H2 sections"**
- **Found during:** Task 2 (README authoring)
- **Issue:** The plan's `<action>` enumerates items 1-8: item 1 is H1 `Chat Templates`, item 2 is an intro paragraph (no heading), items 3-8 are H2 sections. That totals 6 H2 sections, but the acceptance criteria string is `"contains all 7 H2 sections listed in the action"` — an off-by-one.
- **Resolution:** Implemented exactly the 6 H2 sections the action text explicitly names, with their exact titles and required content. Did NOT fabricate a 7th H2 — all required keywords (`D-14`, `D-15`, `D-16`, `sudoingX`, `chat-template-file`, `tool_call_valid`, `sha256sum`) are present, all `<automated>` verify checks pass.
- **Files modified:** `pod/templates/README.md`
- **Verification:** `grep -c '^## '` returns 6; all 8 checks in the `<automated>` verify block pass; no missing semantic content.
- **Committed in:** `cbe1242` (Task 2 commit)

---

**Total deviations:** 1 spec-ambiguity deviation (Rule 1 interpretation, no code change implication)
**Impact on plan:** None — all verify/acceptance substance requirements satisfied. Recorded for plan-authoring feedback only.

## Issues Encountered

- **Network reachability to gist:** Successfully fetched via GitHub API (HTTP 200, 26817-byte metadata response; 7876-byte raw body download). No rate-limit or DNS issue. Fallback path ("fork própria" escalation) not needed.

## Self-Check: PASSED

- `pod/templates/qwen3.5-27b-tool-calling.jinja` — FOUND (8595 bytes, starts with `{#`)
- `pod/templates/qwen3.5-27b-tool-calling.jinja.sha256` — FOUND (single 64-hex line, matches file digest)
- `pod/templates/README.md` — FOUND (6 H2 sections, all required keywords)
- `5067af8` — FOUND in git log
- `cbe1242` — FOUND in git log
- Drift-detection `sha256sum -c` on final file — OK

## Next Phase Readiness

- Plan 01-03 (pod Dockerfile) can `COPY pod/templates/ /app/templates/` deterministically.
- Plan 01-04 (pod docker-compose.yml) can pass `--chat-template-file /app/templates/qwen3.5-27b-tool-calling.jinja` to llama-server without ambiguity.
- Plan 01-06 (smoke.py) has a concrete template file to validate tool-call shape against (D-15 get_weather test).
- Plan 01-08 (smoke.yml CI gate) has a concrete SHA-256 that can be asserted inside the image at build-time / runtime to block tampered images.

---
*Phase: 01-gpu-pod-image-smoke-test*
*Plan: 02*
*Completed: 2026-04-17*
