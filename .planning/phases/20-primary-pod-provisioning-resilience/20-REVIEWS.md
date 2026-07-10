---
phase: 20
reviewers: [codex]
reviewed_at: 2026-07-10
plans_reviewed: [20-01-PLAN.md, 20-02-PLAN.md, 20-03-PLAN.md, 20-04-PLAN.md, 20-05-PLAN.md, 20-06-PLAN.md]
note: gemini skipped (GEMINI_API_KEY unset); claude skipped (self, running inside Claude Code)
---

# Cross-AI Plan Review — Phase 20

## Codex Review

**Summary**

The plans are strong on config plumbing, test intent, and mapping to the primary reconciler. But I would rate the set **HIGH risk** as written because the FF-02 signal design is internally inconsistent: plan 20-02 emits heartbeat timestamps even when bytes are not progressing, while plan 20-04 treats newer heartbeat timestamps as forward progress. That means a truly stalled `mc cp` can keep “proving” progress forever.

There is also meaningful risk of false blocklisting and false pod kills because FF-02 and BL-01 currently do not classify failure causes tightly enough.

**Strengths**

- Good scope correction: plans correctly target `gateway/internal/primary/`, not `emerg/`.
- CFG/UI work in 20-01 and 20-05 is appropriately boring and follows existing Phase 17 patterns.
- 20-04 correctly catches the `failStreak` off-by-one: blocklist on pre-attempt `failStreak >= 1`.
- The created-vs-loading distinction is explicitly protected. That is essential for regime 2.
- Plans include useful negative tests, especially `created -> loading` must not fast-fail.
- UAT plan correctly treats live Vast behavior as mandatory, especially `actual_status` during download and logs API shape.

**Concerns**

- **[HIGH] 20-02 + 20-04: heartbeat timestamp does not equal download progress.**  
  20-02 proposes a background loop that logs every 15s while `mc cp` runs, even if file size is unchanged. 20-04 then treats the newest heartbeat timestamp advancing as progress. A hung transfer will still emit fresh timestamps forever, so `progress_stall_timeout` may never fire. The dry-run mock `sleep 40; touch dest` would actually validate the broken behavior: heartbeat without byte progress.

- **[HIGH] 20-04: FF-02 is armed too broadly and may kill healthy pods after download.**  
  The detector appears to stay armed until health-ready. After all weights finish, `[download-weights]` heartbeats stop, but Docker compose/model startup/health readiness may still legitimately take longer than `progress_stall_budget_s`. That can produce false `progress_stall_timeout` after a successful download.

- **[HIGH] 20-04: “running + no heartbeat” backstop conflates telemetry failure with pod failure.**  
  If Vast logs API is delayed/down, or the heartbeat parser misses lines, a healthy running pod can be destroyed after `progress_stall_budget_s`. This is especially risky because 20-03 defers SSH fallback. Empty logs should probably mean “telemetry unavailable” unless another signal proves onstart is stuck.

- **[HIGH] 20-03: SSH-tail fallback is a locked requirement but is explicitly deferred.**  
  The roadmap says FF-03 is a fallback chain: Vast logs API primary, SSH tail fallback. Plan 20-03 ships only the Vast API. That may be a reasonable scope decision, but then the plan set no longer fully satisfies FF-03 as written. Either implement fallback now or explicitly amend the requirement before execution.

- **[HIGH] 20-04: auto-blocklist on any `waitForReadyOrDestroy` error can poison the picker.**  
  Failures caused by bad R2 creds, broken image, global Vast/API issues, context cancellation, operator shutdown, deploy interruption, or logs API failure are not necessarily machine-specific. Blocklisting the picked machine after two such failures can shrink the market for the wrong reason.

- **[MEDIUM] 20-04: cancellation and deliberate schedule-down need explicit exclusion.**  
  `ctx.Done()` currently closes lifecycle as `cancelled_in_flight`. If the outcome hook treats all non-nil errors as machine failures, normal shutdowns could mutate blocklist/allowlist. The plan should whitelist blocklist-worthy shutdown reasons.

- **[MEDIUM] 20-04: list updates are read-modify-write and can clobber dashboard edits.**  
  Fresh read helps, but two full-array writes are still non-atomic relative to a manual dashboard edit. This is probably low-concurrency, but the failure mode is silent loss of an operator’s list change.

- **[MEDIUM] 20-03: `result_url` SSRF is acknowledged but left as ponytail.**  
  Since the gateway will GET a URL returned by Vast, at minimum the UAT should promote host allowlisting from “maybe later” to “required once the observed host shape is known.” Do not attach auth to the GET, which the plan already gets right.

- **[MEDIUM] 20-06: regime-3 UAT using broken R2 creds can contaminate blocklist results.**  
  Bad credentials are a global config failure, not a bad machine. If BL-01 is active during that test, it may blocklist innocent hosts. The UAT mentions pruning, but the implementation should avoid blocklisting non-machine failures in the first place.

- **[LOW] 20-01: migration trigger recreation should match repo/Postgres version exactly.**  
  If existing migrations use `DROP TRIGGER` then `CREATE TRIGGER`, copy that. Do not rely on `CREATE OR REPLACE TRIGGER` unless the deployed Postgres version supports it.

**Suggestions**

- Change OBS-11 to emit **progress-bearing heartbeats**, not just liveness. Include `{key, bytes}` and only treat progress as forward when bytes increase for an active file. Better: use `mc --json` if it gives reliable byte progress in non-TTY mode.

- Scope FF-02 to the download phase. Arm on `[download-weights] fetching`, update on byte growth, and disarm once all expected files log `ok` or once onstart moves past the download step. Do not keep a download-stall timer running through compose/model startup.

- Make `OnstartLog` return a telemetry status, not just text: for example `available`, `not_ready`, `fetch_error`, `empty`. FF-02 should distinguish “no progress observed” from “could not observe progress.”

- Either implement SSH-tail fallback in 20-03 or split FF-03 into “logs API first” and “SSH fallback follow-up” with the roadmap updated. As written, the plan knowingly misses a locked requirement.

- Add a failure-classification layer before blocklisting. Blocklist only machine-likely reasons such as `created_state_timeout`, confirmed terminal/offline, repeated port bind/reachability failures, maybe `progress_stall_timeout` only when telemetry proves download bytes stopped. Skip context cancellation, operator shutdown, image/config/R2/auth errors, and telemetry-unavailable kills.

- Add UAT cases for false positives: healthy slow download, healthy slow model startup after download, and logs API unavailable while pod is otherwise progressing. The current UAT mostly proves intended kills, not safety.

**Overall Risk: HIGH**

The config/UI and created-state fast-fail portions are solid. The risky part is FF-02 plus auto-blocklist: the current heartbeat design may fail to detect real stalls, and the current failure handling may kill or blocklist healthy machines when telemetry or global config is the actual problem. Fixing progress semantics and failure classification would bring the plan set much closer to the stated goal.

---

## Consensus Summary

Single external reviewer (Codex). Gemini unavailable (no API key), Claude self-excluded. Codex rates the set **HIGH risk** — CFG/UI/created-state-fast-fail solid, but FF-02 (progress-stall) + BL-01 (auto-blocklist) have correctness/safety flaws that would kill or blocklist healthy machines.

### Must-fix before execute (HIGH)
1. **Heartbeat must be progress-BEARING, not liveness (OBS-11 + FF-02).** Dropping `--quiet` + a 15s tick still emits fresh timestamps while bytes are frozen → a hung `mc cp` "proves progress" forever → `progress_stall_timeout` never fires. Emit `{key, bytes}` and treat progress as forward ONLY when bytes increase for an active file (or `mc --json` byte progress in non-TTY). The 20-04 dry-run mock `sleep 40; touch dest` would validate the BROKEN behavior.
2. **Scope FF-02 to the download phase only.** Arm on `[download-weights] fetching`, update on byte growth, DISARM once all files log `ok`/onstart passes the download step. Else compose/model-startup after a successful download exceeds `progress_stall_budget_s` → false kill of a good pod.
3. **Telemetry-unavailable ≠ pod-stuck.** `OnstartLog` returning "" (Vast logs API down/delayed, parser miss) must NOT destroy a running pod. Make `OnstartLog` return a status (`available|not_ready|fetch_error|empty`); FF-02 distinguishes "no progress observed" from "cannot observe". Aggravated by SSH fallback being deferred (20-03).
4. **Failure classification before auto-blocklist (BL-01).** Blocklisting on ANY `waitForReadyOrDestroy` error poisons the picker — R2-creds/bad-image/global-Vast/ctx-cancel/operator-shutdown/logs-API failures are NOT machine-specific. Blocklist ONLY machine-likely reasons: `created_state_timeout`, confirmed terminal/offline, repeated port-bind/reachability; `progress_stall_timeout` only when bytes provably stopped. Skip cancellation, shutdown, image/config/R2/auth, telemetry-unavailable.
5. **FF-03 SSH-tail deferred but is a LOCKED requirement.** Either implement the fallback now OR formally amend FF-03 (roadmap + CONTEXT) to "logs API first, SSH follow-up". As written the set knowingly misses a locked requirement.

### Should-fix (MEDIUM)
- Exclude `cancelled_in_flight` / deliberate schedule-down from the outcome hook (no list mutation on normal shutdown).
- List updates are read-modify-write → can clobber a concurrent dashboard edit (silent loss). Low-concurrency but named.
- `result_url` SSRF: promote host-allowlisting from ponytail to REQUIRED once UAT observes the real host shape. (Plan already correctly attaches NO auth to the GET.)
- Regime-3 UAT (20-06) uses broken R2 creds = a GLOBAL failure, not a bad machine → with BL-01 active it blocklists innocent hosts. Fix by NOT blocklisting non-machine failures (see #4), not by pruning after.

### Low
- Migration 0033 trigger: match the repo's existing `DROP TRIGGER; CREATE TRIGGER` idiom; don't rely on `CREATE OR REPLACE TRIGGER` unless the deployed Postgres supports it.

### Agreed Strengths
- Correct package targeting (`primary/` not `emerg/`); Phase-17-patterned CFG/UI; BL-01 `failStreak>=1` off-by-one correct; created-vs-loading (regime 1 vs 2) protected with negative tests; UAT treats live Vast behavior as mandatory.

### Root theme
Two orthogonal fixes collapse most HIGH concerns: (A) make the download signal carry BYTES + scope the stall timer to the download phase + treat empty-telemetry as unknown-not-dead; (B) a failure-classification gate so only machine-attributable outcomes touch the blocklist. Both are corrections to 20-02 + 20-04 (+ an FF-03 scope decision), not new plans.
