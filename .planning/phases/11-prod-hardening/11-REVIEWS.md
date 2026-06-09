---
phase: 11
reviewers: [gemini, codex]
reviewed_at: 2026-05-27T12:29:30Z
plans_reviewed:
  - 11-01-PLAN.md
  - 11-02-PLAN.md
  - 11-03-PLAN.md
  - 11-04-PLAN.md
  - 11-05-PLAN.md
  - 11-06-PLAN.md
  - 11-07-PLAN.md
  - 11-08-PLAN.md
  - 11-09-PLAN.md
  - 11-10-PLAN.md
---

# Cross-AI Plan Review — Phase 11

> Reviewers invoked: **Gemini** (gemini-cli), **Codex** (codex exec). Claude skipped (self).

## Gemini Review

# Plan Review: Phase 11 — prod-hardening

The implementation plans for Phase 11 provide a comprehensive and highly disciplined approach to securing and verifying the `ifix-ai-gateway` production environment. The strategy effectively balances automated scaffolding with high-stakes live UAT (Load/Chaos testing) while strictly adhering to the project's established architectural patterns and security mandates.

### Strengths
- **Surgical Tooling:** The creation of `load-replay.py` and `audit-log-export.py` reuses existing `httpx`/`psycopg` patterns, ensuring consistency while implementing critical PII sanitization (Pitfall 1).
- **Security Hygiene:** Plan 11-02 includes a mandatory "slopcheck" audit for new UI dependencies and a blocking DB migration step, preventing runtime failures related to schema drift.
- **Realistic Chaos:** PRD-02 and PRD-03 use authentic failure modes (Vast API DELETE and `iptables` DROP) rather than simulated "force-open" states, providing a true test of the "invisible failover" core value.
- **Operational Maturity:** The inclusion of a Google SRE-style blameless postmortem template and a structured incident runbook ensures that the technical hardening is matched by operational readiness.
- **Spend Discipline:** Explicit budget monitoring ($5 cap) and pre-flight static checks before spinning up Vast resources demonstrate high cost-awareness.
- **GHA Recovery:** The fix for the tag-SHA dedup issue (D-18.4) via `workflow_dispatch` is a clever solution to a subtle CI/CD blocker encountered in Phase 10.

### Concerns

#### 1. Cloudflare IP Rotation during Chaos (PRD-03)
- **Severity: MEDIUM**
- **Risk:** OpenRouter uses Cloudflare Workers. While the plan includes broad CIDR drops (104.18.0.0/15, etc.), Cloudflare's IP space is vast. If OpenRouter traffic rotates to an unblocked IP mid-test, the "failure" will vanish, invalidating the UAT.
- **Mitigation:** The plan already suggests `dig +short` at runtime. Ensuring the script is run on the same host as the gateway (`n8n-ia-vm`) is critical.

#### 2. Next.js Middleware Edge Constraints (PRD-06)
- **Severity: LOW**
- **Risk:** Accessing `twoFactorEnabled` or `twoFactorVerified` flags in the Next.js Middleware (Edge runtime) usually requires these fields to be encoded in the session cookie/JWT.
- **Mitigation:** Plan 11-02 mentions enabling `cookieCache`. The executor must ensure the Better Auth instance is configured to expose these specific claims to the client-side/edge-readable cookie.

#### 3. Load Replay "Peak" Intensity (PRD-01)
- **Severity: LOW**
- **Risk:** Replaying a 1:1 audit log preserves the original timing. If the "peak" window chosen doesn't actually hit the saturation thresholds, the load-shedding and latency-aware routing (Phase 5) might not be fully exercised.
- **Mitigation:** The operator should ensure the chosen `audit_log` window contains at least one sustained burst.

#### 4. Vast DELETE Idempotency (PRD-02)
- **Severity: LOW**
- **Risk:** As noted in Research Assumption A1, the behavior of a DELETE on an already-deleted instance is unconfirmed.
- **Mitigation:** The script in 11-07 correctly handles 404 as "idempotent gone," which is the most likely behavior.

### Suggestions
- **2FA Recovery:** Consider adding a task to `seed-admins.sh` or a section in `RUNBOOK-INCIDENTS.md` specifically for resetting an admin's 2FA (e.g., via `gatewayctl` or a SQL snippet) in case they lose their device and backup codes.
- **Dashboard Health Check:** Since the dashboard is now critical for operations, consider adding a basic `/health` or `/api/auth/health` endpoint check to the `preflight.sh` script to ensure SSO hardening hasn't broken the container's own health status.
- **Replay Parallelism:** If the 30-min window is too "quiet," the `load-replay.py` script could support a `--speedup` multiplier (e.g., `2x`) to compress the traffic and increase RPS density.

### Risk Assessment: LOW
The overall risk is **LOW**. The plans are exceptionally detailed, with clear "Pitfall" mitigations and "Truths" that lock down the scope. The use of waves ensures that infrastructure is ready before live spend occurs. The transition from Phase 10's "initial deploy" to Phase 11's "hardened prod" is logically sound and follows industry best practices for resilience and observability.

**Approval Recommendation:** Approved. The plans are ready for execution.

---

## Codex Review

### Summary

The Phase 11 plan set is strong overall: it decomposes prod hardening into sensible waves, keeps live-risk work behind explicit human gates, and ties most tasks back to PRD-01..PRD-06 and D-01..D-19. The best parts are the evidence discipline, SLO contract, chaos cleanup requirements, and explicit treatment of LGPD and incident runbooks as first-class deliverables. Main risks are execution complexity, several likely implementation mismatches around Better Auth, schema/middleware behavior, gateway admin routing, load-test replay fidelity, and a few places where the plans ask for live secret-bearing commands or broad iptables drops in ways that could create operational or security exposure.

### Strengths

- Clear wave structure: scaffolding and docs first, live UAT second, final runbook/verification last.
- Good separation between autonomous code/doc work and operator-gated live actions.
- PRD traceability is strong; every PRD has an evidence artifact and rollup path.
- SLO names are standardized early in `11-01`, which gives later plans a stable validation contract.
- Chaos plans explicitly require cleanup and evidence, which is essential for prod tests.
- LGPD plan correctly treats legal signature as an external gate while still producing useful artifacts.
- Runbook plan is well scoped to exactly four incident classes and avoids uncontrolled incident taxonomy growth.
- Sensitive-tenant invariant is repeatedly called out, which matches the core safety requirement.

### Concerns

#### HIGH

- **11-02 Better Auth implementation risk is under-estimated.** The plan assumes `auth.options` is introspectable in tests, assumes middleware can read `twoFactorEnabled` / `twoFactorVerified` from cookie/session reliably, and assumes Better Auth CLI migration behavior. These are fragile integration points and could break build or, worse, create redirect loops.
- **11-02 adds frontend dependencies despite the global "zero new dependencies" refrain.** The plan qualifies this as dashboard-only, but it still expands supply chain and install surface during a prod-hardening phase. That is acceptable only if the UI truly cannot use existing components.
- **11-04 admin panic route wiring is risky.** Adding `/admin/debug/panic` to production is useful for Sentry proof, but the plan must verify it is gated by admin auth in integration, not only by code placement. A mis-mounted route would be an intentional unauthenticated panic endpoint.
- **11-07 and 11-08 include live secret examples in instructions.** Several UAT snippets show full-looking `ifix_sk_...` keys. Even if placeholders, the plan encourages copying secrets into docs/evidence. Use key labels or env names only.
- **11-08 broad Cloudflare CIDR DROP can affect unrelated production egress.** Blocking `104.18.0.0/15` and `172.64.0.0/13` may disrupt other Cloudflare-fronted services from the host. The plan acknowledges this but treats it too lightly for prod.

#### MEDIUM

- **11-01 exporter likely lacks the data needed to replay.** The SELECT shown only includes metadata columns, but the action references `sanitize_body(body, data_class)`. If audit request payloads live in another table or column, the plan must name the source precisely.
- **11-01 embeds `_replay_api_key` into JSONL fixtures.** Even gitignored, writing API keys into fixture files increases leakage risk. Better to store tenant slug in fixture and resolve keys at replay time from env.
- **11-01 report gate `zero_5xx_panic` is weak.** Detecting panic by response body content is unreliable because recoverers should not expose panic strings. This gate should rely on Sentry/log query or a separate `panic_count` observation.
- **11-02 schema task is internally inconsistent.** It says prefer CLI auto-migrate, but the done criteria require `schema.ts` contains `twoFactor` or `twoFactorEnabled`. Pick one canonical source of schema truth.
- **11-04 `gatewayctl key list` may not have the needed sqlc query shape.** The plan references `ListActiveKeysAll` / `ListActiveKeysByTenant`, but if those queries do not expose tenant slug/status/data class as planned, implementation will drift or require query changes not declared in `files_modified`.
- **11-05 `seed-admins.sh` provisioning via sign-up API creates password handling risk.** Printing generated passwords to stdout is operationally convenient but weak. Prefer invite/reset flow or one-time password file with restrictive permissions and explicit deletion.
- **11-06 requires committing live load reports.** Reports may contain request IDs, tenant slugs, upstream labels, and timing data. That is probably acceptable, but the schema and evidence instructions should explicitly forbid request bodies, headers, keys, DSNs, and raw error payloads.
- **11-07 says primary cleanup via `force-up` after DELETE.** If emergency/primary lifecycle already has reconciliation behavior, manual `force-up` may fight the controller. The plan needs a clearer "observe first, then intervene if stuck" sequence.
- **11-10 changes `STATE.md` and `ROADMAP.md` manually.** If GSD has a canonical state transition command, manual edits can corrupt state. The plan mentions optional `gsd-sdk query` but should prefer the canonical transition path.

#### LOW

- Some plans are very verbose and prescriptive, increasing the chance executors follow stale line numbers rather than current code.
- Several `grep`-based verifications are brittle and can pass while behavior is wrong.
- Mixed language in docs is acceptable here, but operational runbooks should use one dominant language for incident clarity.
- Some verification commands assume tools like `jsonschema`, `jq`, `dig`, `psql`, and `gh` are installed everywhere; fallback checks should be explicit where live gates depend on them.
- `estimated_duration` looks optimistic for 11-02, 11-06, 11-07, 11-08, and 11-10.

### Plan-by-Plan Review

#### 11-01 Load-Test Scaffolding
**Assessment:** Good foundation, but the exporter/replayer contract needs tightening before implementation.

**Concerns:**
- **HIGH:** Export action references `body` but SELECT does not fetch a request body/content column.
- **MEDIUM:** `_replay_api_key` in JSONL is a secret-at-rest risk.
- **MEDIUM:** Multipart/STT replay is underspecified; forcing `Content-Type: application/json` will not replay audio transcriptions correctly.
- **LOW:** `zero_5xx_panic` cannot be reliably inferred from response body.

**Suggestions:**
- Define exact audit payload source table/column.
- Store tenant slug only; resolve API keys at replay time.
- Add explicit multipart handling for `/v1/audio/transcriptions`.
- Make panic gate an external Sentry/log observation.

#### 11-02 Dashboard SSO Hardening
**Assessment:** Covers PRD-06 comprehensively, but this is the highest-risk implementation plan.

**Concerns:**
- **HIGH:** Middleware/session assumptions may cause redirect loops.
- **HIGH:** Better Auth migration and schema mirror strategy is ambiguous.
- **MEDIUM:** New dependencies may be avoidable.
- **MEDIUM:** Tests based on `auth.options` may fail if Better Auth does not expose stable internals.
- **LOW:** Rate-limit storage set to memory may reset on container restart and is weaker than Redis-backed enforcement.

**Suggestions:**
- Add a staging smoke before prod migration.
- Decide whether schema is CLI-owned or Drizzle-mirrored.
- Add Playwright/manual route tests for enroll/challenge redirect behavior.
- Prefer existing UI primitives unless QR/input/dialog packages are truly necessary.
- Consider Redis/secondary storage for rate limit if available.

#### 11-03 LGPD Docs
**Assessment:** Strong doc-only plan with clear scope.

**Concerns:**
- **MEDIUM:** MinIO as "sub-processor" may be legally debatable if self-hosted/internal; wording should say "infrastructure component/sub-processor if applicable" unless legal already classified it.
- **LOW:** Letter includes specific model/provider names that may drift.

**Suggestions:**
- Add a "classification to confirm with jurídico" note for MinIO if ownership is internal.
- Keep provider/model details in canonical subprocessors doc and reference that doc from the letter.

#### 11-04 Gatewayctl + Phase 10 Fold
**Assessment:** Useful operator tooling, but debug panic route needs strict safety controls.

**Concerns:**
- **HIGH:** `/admin/debug/panic` must be proven admin-gated with an automated HTTP test.
- **MEDIUM:** `key list` may require additional SQL/query files not listed.
- **MEDIUM:** Smoke script SSH/docker assumptions may not hold across environments.
- **LOW:** Placeholder tests give little confidence.

**Suggestions:**
- Add an integration test: unauthenticated request returns 401/403, authenticated returns 500 through recoverer.
- Include any sqlc query file changes in `files_modified`.
- Parameterize gatewayctl path/container name/SSH host.
- Avoid relying only on skipped tests.

#### 11-05 Per-Env Keys + Deploy Runbook
**Assessment:** Good operational documentation, but secret-handling in the seed script needs tightening.

**Concerns:**
- **MEDIUM:** `seed-admins.sh` printing passwords to stdout is risky.
- **MEDIUM:** Direct SQL probing plus API signup can race or diverge from Better Auth schema.
- **LOW:** Per-env key verification via `diff` can accidentally expose key values.

**Suggestions:**
- Print only generated password file path with `0600` permissions, or require reset flow.
- Sanitize diff to hashes/prefixes only.
- Prefer Better Auth admin/provisioning API if available.

#### 11-06 Load-Test Live UAT
**Assessment:** Good live-gate structure and evidence plan.

**Concerns:**
- **MEDIUM:** The plan depends on 11-01 correctness for replay realism.
- **MEDIUM:** SLO evaluation during mixed tier-0/tier-1 needs clear attribution.
- **LOW:** Static schema sample with empty routes does not prove real report shape.

**Suggestions:**
- Add a 2-minute dry run before the 30-minute run.
- Capture upstream distribution by route.
- Add fixture row-count and route-mix checks before full run.

#### 11-07 Primary Kill Chaos
**Assessment:** Valuable chaos test with good cleanup awareness.

**Concerns:**
- **MEDIUM:** Manual `force-up` may interfere with controller behavior if used too early.
- **MEDIUM:** Pass criteria around 503 blip vs non-503 error budget need stricter definitions.
- **LOW:** Instance ID extraction from `gatewayctl primary state` is brittle.

**Suggestions:**
- Define exact allowed error classes during the first 60 seconds.
- Observe reconciliation for a fixed window before manual intervention.
- Prefer JSON output from `gatewayctl primary state` if available.

#### 11-08 OpenRouter DROP Chaos
**Assessment:** Achieves PRD-03 intent, but network blast radius is the main risk.

**Concerns:**
- **HIGH:** Broad Cloudflare DROP can impact unrelated services.
- **MEDIUM:** "Normal tenant returns 200 via Vast primary" does not prove tier-2 OpenAI fallthrough if tier-0 is healthy.
- **MEDIUM:** Combining iptables DROP with `smoke-sensitive-failover.py --induce gatewayctl` may muddle natural breaker evidence.

**Suggestions:**
- Prefer a deterministic OpenRouter-only route override or egress proxy rule scoped to gateway container/user.
- Force or simulate tier-0 unavailable only if proving normal tier-2 fallback is required.
- Keep natural-observation test separate from forced-open smoke.

#### 11-09 Runbook + Postmortem
**Assessment:** Strong plan and well aligned with PRD-04.

**Concerns:**
- **LOW:** Depends on evidence files that may not exist if Wave 2 is partial.
- **LOW:** "Exactly four classes" is good, but runbook should include an "other incident" escalation path.

**Suggestions:**
- Add fallback references if one Wave 2 UAT is `passed_partial`.
- Add a short "unknown incident triage" section without creating a fifth class.

#### 11-10 Human UAT + Verification
**Assessment:** Good closure discipline, but it mixes too many live operations and state mutations.

**Concerns:**
- **MEDIUM:** Scenario docs include raw-looking API keys.
- **MEDIUM:** Manual state edits can drift from GSD tooling.
- **MEDIUM:** S7 can alter release artifacts late in the phase; if it fails, closure becomes ambiguous.
- **LOW:** Evidence requirements may encourage screenshots of sensitive 2FA material despite warning.

**Suggestions:**
- Replace all keys with env vars or labels.
- Use canonical GSD state-advance command if available.
- Treat GHA/GHCR image build as a separate gate before final UAT, not mixed with auth/security scenarios.
- Require redacted evidence only.

### Risk Assessment

**Overall risk: MEDIUM-HIGH.**

The phase goals are achievable and the plans are thoughtfully structured, but the execution surface is large: dashboard auth changes, live prod load, live prod chaos, key rotation, CI artifact retrigger, admin tooling, and final state mutation all happen in one phase. The main risks are not missing requirements; they are integration fragility and operational blast radius. With the suggested tightening around Better Auth validation, secret handling, iptables scope, replay payload fidelity, and admin panic-route safety, the risk drops to **MEDIUM**.

---

## Consensus Summary

Both reviewers agree the phase is **execution-ready in structure** but flag the same operational hot spots. Gemini grades overall risk **LOW** (process discipline strong); Codex grades **MEDIUM-HIGH** (integration surface large). The divergence is calibration, not direction — both surface the same top concerns.

### Agreed Strengths
- Wave decomposition (scaffolding → live UAT → runbook) keeps live-risk work behind explicit gates.
- Realistic chaos via authentic failure modes (Vast DELETE, iptables DROP) over forced-breaker simulation.
- PRD↔plan↔evidence traceability is end-to-end; SLO contract is locked early in 11-01.
- Operational maturity: blameless postmortem + 4-class incident runbook + spend discipline ($5 cap).
- LGPD plan correctly treats legal signature as external gate while still shipping artifacts.

### Agreed Concerns (highest priority)

| # | Concern | Severity | Plans |
|---|---------|----------|-------|
| 1 | **Cloudflare CIDR DROP blast radius / IP rotation** — broad `/15`+`/13` drop can hit unrelated egress and OpenRouter may rotate to unblocked IPs mid-test | HIGH (Codex) / MEDIUM (Gemini) | 11-08 |
| 2 | **Next.js middleware session fields** — `twoFactorEnabled`/`twoFactorVerified` must be exposed in cookie/JWT or middleware will redirect-loop | HIGH (Codex) / LOW (Gemini) | 11-02 |
| 3 | **Live secrets in plans/evidence** — `ifix_sk_...` examples + committed load reports encourage secret leakage | HIGH (Codex) / implicit (Gemini) | 11-07, 11-08, 11-10, 11-06 |
| 4 | **Replay fidelity** — 1:1 timing may not hit saturation; SELECT may lack body column; multipart STT underspecified | HIGH+MEDIUM (Codex) / LOW (Gemini) | 11-01, 11-06 |
| 5 | **Admin panic route** — `/admin/debug/panic` must have automated unauth→401 integration test | HIGH (Codex only) | 11-04 |
| 6 | **2FA recovery / device-loss path** — no documented reset for lost device + lost backup codes | MEDIUM (Gemini only) | 11-02, 11-05, 11-09 |
| 7 | **seed-admins.sh password handling** — printing generated passwords to stdout is weak | MEDIUM (Codex only) | 11-05 |

### Divergent Views
- **Overall risk grade:** Gemini = LOW, Codex = MEDIUM-HIGH. Worth investigating before execute-phase — Codex's grade weights blast radius and integration surface; Gemini's weights process maturity.
- **Better Auth integration risk (11-02):** Codex flags HIGH (redirect loops, schema mirror ambiguity, `auth.options` introspection). Gemini flags LOW. Recommendation: treat 11-02 as the gate plan — add staging smoke + Playwright route tests before prod migration.
- **Cloudflare DROP scope (11-08):** Codex pushes for container/user-scoped egress rule instead of host-wide CIDR. Gemini accepts host-wide if run on `n8n-ia-vm`. Recommendation: scope egress to gateway container if feasible.

### Top 3 Actions Before Execute-Phase
1. **Tighten 11-08 iptables scope** — prefer per-container/per-user egress rule over host-wide `/15`+`/13` DROP; document rollback if unrelated services break.
2. **Validate 11-02 Better Auth integration** — confirm `twoFactorEnabled`/`twoFactorVerified` flow through cookie/session before middleware refactor; add staging smoke.
3. **Purge live secrets from plans + evidence schema** — replace `ifix_sk_...` with env-var labels in 11-07/11-08/11-10; add explicit "no raw keys/headers/bodies" rule to load-report schema (11-01/11-06).

### Apply Feedback
```
/gsd:plan-phase 11 --reviews
```
