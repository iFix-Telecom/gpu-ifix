---
phase: quick-20260821-llama-ctx-32768
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - gateway/internal/primary/onstart.go
  - gateway/internal/emerg/lifecycle.go
  - gateway/internal/proxy/tokencount.go
  - gateway/internal/proxy/errors.go
  - pod/docker-compose.yml
  - pod/primary/supervisord.conf
  - pod/README.md
  - gateway/docs/RUNBOOK-PRIMARY-POD.md
  - gateway/docs/RUNBOOK-EMERGENCY-POD.md
autonomous: true
requirements: [OPERACOES-26306]

must_haves:
  truths:
    - "Every hardcoded llama-server --ctx-size in the repo reads 32768 (no stray 16384 flag values remain)"
    - "ChatContextCap value stays 16384 but its comment explains it as PER-SLOT context (32768 total / -np 2)"
    - "gateway builds and unit tests pass; gofmt reports no unformatted files"
  artifacts:
    - path: "gateway/internal/primary/onstart.go"
      provides: "primaryLlamaArgsDefault with --ctx-size 32768"
      contains: '"--ctx-size", "32768"'
    - path: "gateway/internal/emerg/lifecycle.go"
      provides: "emergencyLlamaArgsDefault with --ctx-size 32768"
      contains: '"--ctx-size", "32768"'
    - path: "pod/primary/supervisord.conf"
      provides: "primary pod runtime llama command with --ctx-size 32768"
      contains: "--ctx-size 32768"
    - path: "pod/docker-compose.yml"
      provides: "legacy/dev pod llama command with --ctx-size 32768"
      contains: "--ctx-size 32768"
  key_links:
    - from: "gateway/internal/primary/onstart.go"
      to: "pod/primary/supervisord.conf"
      via: "mirrored llama args (onstart.go doc comment: source-of-truth sync with supervisord.conf)"
      pattern: "ctx-size.*32768"
    - from: "gateway/internal/proxy/tokencount.go"
      to: "pod/primary/supervisord.conf"
      via: "ChatContextCap == per-slot ctx (32768 / -np 2 = 16384)"
      pattern: "per-slot"
---

<objective>
Raise llama-server `--ctx-size` from 16384 to 32768 everywhere the flag is hardcoded in the repo (keeping `-np 2`, so per-slot context becomes 32768/2 = 16384 tokens), and fix now-stale documentation around the gateway pre-dispatch cap.

Motivation (OPERACOES-26306): IA analysis of a 38-min call (chamada_id 12294412, 2026-08-21) failed with llama-server 400 `exceed_context_size_error` — "request (11184 tokens) exceeds the available context size (8192 tokens)". llama.cpp splits total `--ctx-size` across `-np` slots: 16384/2 = 8192/slot. The gateway pre-dispatch guard `ChatContextCap=16384` was calibrated against TOTAL ctx, not per-slot, so the 11184-token request passed the gateway and blew the slot. LLM runs alone on a dedicated RTX 3090 24GB (STT is on a separate 3060); KV headroom exists (~+1.5GiB going 16k→32k total for Qwen3-30B-A3B GQA). After this change, `ChatContextCap=16384` exactly equals the real per-slot limit — the VALUE stays, its rationale changes.

Purpose: 38-min-call-class requests (≤16384 input tokens) succeed instead of 400ing at the slot.
Output: Repo-only edits — Go arg slices, pod compose + supervisord.conf, cap comments, runbooks. NO pod reprovisioning, NO n8n changes.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@gateway/internal/primary/onstart.go
@gateway/internal/emerg/lifecycle.go
@gateway/internal/proxy/tokencount.go
@gateway/internal/proxy/errors.go
@pod/docker-compose.yml
@pod/primary/supervisord.conf

<interfaces>
Verified edit inventory (grepped 2026-08-21; line numbers current):

Flag value changes (16384 → 32768):
- gateway/internal/primary/onstart.go:29    `"--ctx-size", "16384",` in primaryLlamaArgsDefault
- gateway/internal/emerg/lifecycle.go:648   `"--ctx-size", "16384",` in emergencyLlamaArgsDefault
- pod/primary/supervisord.conf:44           `command=/app/llama-server ... -np 2 --ctx-size 16384 --split-mode none --main-gpu 0 --jinja` (the ACTUAL primary-pod runtime command; onstart.go doc comment declares it must mirror this file)
- pod/docker-compose.yml:49                 `--ctx-size 16384` in llama service command
- pod/docker-compose.yml:36                 comment "Flags locked: -np 2, --ctx-size 16384, ..."
- gateway/docs/RUNBOOK-PRIMARY-POD.md:108   diagram line `--ctx-size 16384 --jinja`
- gateway/docs/RUNBOOK-EMERGENCY-POD.md:402 `-ngl 99 -np 2 --ctx-size 16384 --jinja --chat-template-file <path>`
- pod/README.md:138                         troubleshooting row citing `--ctx-size 16384`

Comment-only changes (value STAYS 16384):
- gateway/internal/proxy/tokencount.go:48-51  ChatContextCap doc comment currently says "Matches the llama-server --ctx-size baked into the pod image" — WRONG after this change
- gateway/internal/proxy/errors.go:28-29      ErrContextLengthExceeded comment "input_tokens > 16384 (chat)" — value correct, semantics need per-slot note

Confirmed NON-targets (do NOT touch):
- gateway/internal/proxy/audio_duration_test.go:63-64 — "16384" is a byte count (mp3 fixture), unrelated
- No *_test.go asserts the literal ctx-size flag value. lifecycle_test.go (primary) asserts supervisord.conf STRUCTURE (program blocks, --jinja present, no --chat-template-file) but not the numeric ctx-size; TestPrimaryLlamaArgsDefault_NoChatTemplateFile checks flag presence/absence only.
- ChatContextCap consumers (dispatcher.go, tokencount_test.go, dispatcher_test.go, cmd/gateway/main.go) reference the CONSTANT, whose value is unchanged — no edits needed.
- .planning/** historical docs — leave as historical record.
</interfaces>
</context>

<tasks>

<task type="auto">
  <name>Task 1: Bump --ctx-size to 32768 in Go arg slices and rewrite stale cap comments</name>
  <files>gateway/internal/primary/onstart.go, gateway/internal/emerg/lifecycle.go, gateway/internal/proxy/tokencount.go, gateway/internal/proxy/errors.go</files>
  <action>
    1. gateway/internal/primary/onstart.go:29 — change `"--ctx-size", "16384",` to `"--ctx-size", "32768",` in primaryLlamaArgsDefault. Leave `-np 2` and all other flags untouched.
    2. gateway/internal/emerg/lifecycle.go:648 — change `"--ctx-size", "16384",` to `"--ctx-size", "32768",` in emergencyLlamaArgsDefault. Leave the 13-flag structure otherwise intact (comment at :636 says "13-flag" — flag COUNT is unchanged, no comment edit needed there).
    3. gateway/internal/proxy/tokencount.go:48-51 — keep `ChatContextCap = 16384` but REWRITE the doc comment. It currently claims the cap "Matches the llama-server --ctx-size baked into the pod image", which is stale. New comment must state: cap equals the PER-SLOT context of llama-server — total `--ctx-size 32768` split across `-np 2` slots = 16384 tokens/slot; previously the cap was calibrated to TOTAL ctx (16384/2 = 8192/slot), which let an 11184-token request pass the gateway and 400 at the slot (OPERACOES-26306, chamada_id 12294412). Keep the RES-07 / CONTEXT.md "Enforcement do 16k cap" reference.
    4. gateway/internal/proxy/errors.go:28-29 — keep ErrContextLengthExceeded as-is; adjust the comment so "input_tokens > 16384 (chat)" reads as the per-slot ceiling, e.g. "input_tokens > 16384 (chat per-slot cap, see ChatContextCap in tokencount.go)". Value references stay 16384 — that is still the enforced number.
    Do NOT touch ChatContextCap consumers (dispatcher.go, main.go, tests) — the constant value is unchanged.
  </action>
  <verify>
    <automated>cd gateway && go build ./... && go test ./internal/primary/... ./internal/emerg/... ./internal/proxy/... && test -z "$(gofmt -l .)" && grep -v '^\s*//' internal/primary/onstart.go internal/emerg/lifecycle.go | grep -c '"--ctx-size", "32768"' | grep -qx 2 && ! grep -rn '"--ctx-size", "16384"' internal/</automated>
  </verify>
  <done>Both Go arg slices carry --ctx-size 32768; ChatContextCap still 16384 with per-slot rationale in comments; gateway builds, unit tests for primary/emerg/proxy pass, gofmt clean. (Note memory gateway-integration-tests-not-in-executor-check: integration tests need `-tags integration` — unit suite here is the CI-blocking baseline; gofmt check included per that memory.)</done>
</task>

<task type="auto">
  <name>Task 2: Bump pod artifacts + runbooks, then atomic commit</name>
  <files>pod/primary/supervisord.conf, pod/docker-compose.yml, pod/README.md, gateway/docs/RUNBOOK-PRIMARY-POD.md, gateway/docs/RUNBOOK-EMERGENCY-POD.md</files>
  <action>
    1. pod/primary/supervisord.conf:44 — in the `command=/app/llama-server ...` line, change `--ctx-size 16384` to `--ctx-size 32768`. This file is the ACTUAL runtime command of the primary pod image and onstart.go's doc comment declares they mirror each other — skipping it would silently reintroduce the 8192/slot limit on the next image build.
    2. pod/docker-compose.yml:49 — change `--ctx-size 16384` to `--ctx-size 32768` in the llama service command; line 36 comment — update "Flags locked: -np 2, --ctx-size 16384, ..." to `--ctx-size 32768` and append a per-slot note, e.g. "(32768 total / -np 2 = 16384/slot — OPERACOES-26306)".
    3. gateway/docs/RUNBOOK-PRIMARY-POD.md:108 — update diagram text `--ctx-size 16384 --jinja` to `--ctx-size 32768 --jinja`.
    4. gateway/docs/RUNBOOK-EMERGENCY-POD.md:402 — update `-ngl 99 -np 2 --ctx-size 16384 --jinja ...` to `--ctx-size 32768`.
    5. pod/README.md:138 — update the troubleshooting row citing `--ctx-size 16384` to `--ctx-size 32768` (keep the D-09 fallback guidance wording; the row is about REDUCING ctx on VRAM pressure, so only the cited current value changes). Line 140 mentions no literal value — leave as-is.
    6. Repo-wide leftover sweep: confirm no remaining hardcoded 16384 ctx-size flag outside .planning/ historical docs.
    7. Single atomic commit of ALL files from Tasks 1+2:
       `git add gateway/internal/primary/onstart.go gateway/internal/emerg/lifecycle.go gateway/internal/proxy/tokencount.go gateway/internal/proxy/errors.go pod/primary/supervisord.conf pod/docker-compose.yml pod/README.md gateway/docs/RUNBOOK-PRIMARY-POD.md gateway/docs/RUNBOOK-EMERGENCY-POD.md`
       Commit message:
       `fix(pod): raise llama --ctx-size 16384->32768 for 16k per-slot context (OPERACOES-26306)`
       with body noting: -np 2 kept → 16384 tokens/slot; ChatContextCap value unchanged (now == per-slot limit); repo-only — pod image rebuild/reprovision handled separately.
    Do NOT reprovision pods, rebuild images, or touch n8n — out of scope.
  </action>
  <verify>
    <automated>grep -v '^\s*#' pod/docker-compose.yml pod/primary/supervisord.conf | grep -c 'ctx-size 32768' | grep -qx 2 && ! grep -rn 'ctx-size 16384' pod/ gateway/ --include='*.go' --include='*.yml' --include='*.conf' --include='*.md' && git log -1 --pretty=%s | grep -q 'OPERACOES-26306'</automated>
  </verify>
  <done>All pod artifacts and runbooks read --ctx-size 32768; no non-historical file still carries the 16384 flag; single conventional commit referencing OPERACOES-26306 exists on develop.</done>
</task>

</tasks>

<verification>
- `cd gateway && go build ./... && go test ./internal/primary/... ./internal/emerg/... ./internal/proxy/...` → green
- `cd gateway && gofmt -l .` → empty (memory: run before push; integration suite needs `-tags integration` and is not gated here)
- `grep -rn 'ctx-size 16384\|"--ctx-size", "16384"' gateway/ pod/` → zero hits
- `grep -n 'ChatContextCap = 16384' gateway/internal/proxy/tokencount.go` → still present (value intentionally unchanged)
- Comment in tokencount.go explains per-slot semantics (32768 / -np 2)
</verification>

<success_criteria>
- llama-server will start with 32768 total ctx (-np 2 → 16384/slot) from ALL three command sources: primary supervisord.conf, primaryLlamaArgsDefault, emergencyLlamaArgsDefault — plus the legacy pod/docker-compose.yml.
- Gateway pre-dispatch guard ChatContextCap=16384 now exactly matches the real per-slot limit; docs/comments no longer claim it matches total --ctx-size.
- CI-relevant unit tests pass; gofmt clean; one atomic commit referencing OPERACOES-26306.
- No pod reprovisioning or n8n changes in this change set.
</success_criteria>

<output>
Create `.planning/quick/20260821-llama-ctx-32768/SUMMARY.md` when done.
</output>
