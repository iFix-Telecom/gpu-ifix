---
phase: 11
slug: prod-hardening
artifact: HUMAN-UAT
author: pedro (operator)
created: 2026-05-27
status: in_progress
scenarios_total: 8
scenarios_ordered: [S0, S1, S2, S3, S4, S5, S6, S8]
s0_outcome: pending
reviews_folded:
  M1_zero_raw_keys: enforced (env-var labels only)
  M2_canonical_state_advance: pending (gsd-sdk query phase complete 11)
  M3_s0_prerequisite_gate: enforced (S0 first; ABORT on fail)
  L4_redacted_evidence: enforced (no QR codes, no TOTP digits, no backup codes)
---

# Phase 11 — HUMAN-UAT Scenario Sheet

| Field | Value |
|-------|-------|
| Phase | 11 (prod-hardening) |
| Operator | pedro |
| Date | 2026-05-27 |
| Status | in_progress |
| S0 outcome | pending |
| Status (final) | TBD — `passed` / `passed_partial` / `failed` / `blocked` |
| Sign-off | pending |

---

## REDACTED EVIDENCE — Cross-cutting rule (read FIRST)

> **REDACTED EVIDENCE ONLY.** For 2FA scenarios (S1, S2, S3), NEVER screenshot
> or paste:
>
> - The TOTP QR code itself (capture the post-enrollment SUCCESS screen only —
>   e.g. "2FA enabled" confirmation toast).
> - The 6-digit TOTP code (capture textual confirmation: "code accepted,
>   redirected to /").
> - The 10 backup codes (capture the "codes saved successfully" /
>   "10 códigos copiados" toast confirmation only).
> - Passwords (including disposable test passwords).
> - Any `Bearer ifix_sk_...` Authorization header value.
> - Any raw `ifix_admin_...` admin key value.
> - Any postgres DSN containing user:pass@host literals.
>
> Evidence MUST be textual confirmation OR sanitized HTTP status sequences
> OR first-4-char prefixes (e.g. `ifix_admin_****613f`). When in doubt,
> redact. The REDACTED EVIDENCE rule appears 3 times in this sheet
> (here, S1 enroll, S3 rate-limit) — those occurrences are the post-commit
> grep gate (`grep -ic REDACTED ≥ 3`).

---

## Pre-UAT environment checklist (operator signs before running any scenario)

- [ ] Wave 1 plans (11-01..11-05) committed and merged to `develop`
- [ ] Wave 2 plans (11-06 / 11-07 / 11-08) committed with EVIDENCE files; verdicts
      reflected accurately in scenario carry-forward (11-06 BLOCKED on primary
      reconciler silent-hang, 11-07 artifact-shipped-live-deferred, 11-08
      Segment A LIVE PASS 3/3 + Segment B 2/4)
- [ ] Wave 3 plan 11-09 committed (RUNBOOK-INCIDENTS.md + POSTMORTEM-TEMPLATE.md
      + RUNBOOK-2FA-RECOVERY.md)
- [ ] `11-02-staging-smoke.md` committed showing dashboard SSO staging smoke
      green BEFORE the prod migrate
- [ ] Operator has Sentry web access for project `ifix-ai-gateway-prod`
      (id `4511455942017024`)
- [ ] Operator has Vast.ai dashboard access (Vast token in
      `~/.claude/CLAUDE.md`, local-only — REDACTED in this sheet)
- [ ] Operator has GitHub repo write permission; `gh` CLI authenticated
- [ ] Operator has Authenticator app ready (1Password / Google Authenticator)
- [ ] Spend cap acknowledged: Phase 11 total ≤ $5 absolute
- [ ] **Tenant API keys**: operator runs `source /etc/ifix/keys.env` (file
      mode 0600, owner-only) to populate the env vars below BEFORE running
      any scenario. After all scenarios complete, operator runs the matching
      `unset` line.

      Required env-var labels (no literal `ifix_sk_...` allowed in this sheet):

      - `${IFIX_KEY_CONVERSEAI}`
      - `${IFIX_KEY_TELEFONIA}`
      - `${IFIX_KEY_CHAT_IFIX}`
      - `${IFIX_KEY_COBRANCAS}`
      - `${IFIX_KEY_CAMPANHAS}`
      - `${IFIX_KEY_VOICE_API}`
      - `${IFIX_KEY_UAT10_TEST}`

      And admin / DSN env vars (also via `/etc/ifix/keys.env`):

      - `${AI_GATEWAY_ADMIN_KEY}` (no literal `ifix_admin_...` in this sheet)
      - `${AI_GATEWAY_PG_DSN}` (gateway prod DSN; mode-0600)
      - `${PROD_DSN}` (dashboard prod DSN; mode-0600)
      - `${VAST_AI_API_KEY}` (Vast token if needed for S0 retrigger backstop)

- [ ] Shell history disabled for this session: `export HISTFILE=/dev/null`
      before sourcing `/etc/ifix/keys.env`. Inline literal `ifix_sk_...` keys
      are PROHIBITED in this sheet and in shell history.
- [ ] Post-session cleanup line acknowledged (run AFTER all scenarios):
      ```
      unset IFIX_KEY_CONVERSEAI IFIX_KEY_TELEFONIA IFIX_KEY_CHAT_IFIX \
            IFIX_KEY_COBRANCAS IFIX_KEY_CAMPANHAS IFIX_KEY_VOICE_API \
            IFIX_KEY_UAT10_TEST AI_GATEWAY_ADMIN_KEY AI_GATEWAY_PG_DSN \
            PROD_DSN VAST_AI_API_KEY
      ```

---

## S0 — GHA workflow_dispatch produces :v1.0.0 image (D-18.4) — PREREQUISITE GATE [reviews MEDIUM M3]

> **PREREQUISITE GATE — RUN FIRST. ABORT ON FAIL.**
>
> This scenario alters release artifacts (`ghcr.io` image tags). It MUST run
> BEFORE S1-S6 + S8 (sequential dependency, not parallel). Running it
> concurrently with auth / security UAT muddles which run produced which
> evidence.
>
> **If S0 FAILS, ABORT — do NOT proceed to S1-S6, S8. Phase status =
> `blocked`, NOT `passed_partial`.** Restore release artifacts via
> `gateway/docs/RUNBOOK-DEPLOY.md` §GHA retrigger procedure; resume from S0
> on next attempt.

### Steps

1. Trigger the gateway build:
   ```bash
   gh workflow run build-gateway.yml --ref v1.0.0 -f tag=v1.0.0
   ```
   Capture the run id.
2. Watch to completion:
   ```bash
   gh run watch <run-id>
   ```
   Run completes with `conclusion: success`.
3. Pull the image and capture the manifest digest (digest is non-secret):
   ```bash
   docker pull ghcr.io/ifixtelecom/ifix-ai-gateway:v1.0.0
   docker inspect --format='{{index .RepoDigests 0}}' \
     ghcr.io/ifixtelecom/ifix-ai-gateway:v1.0.0
   ```
4. Repeat steps 1-3 for `build-dashboard.yml`.

### Evidence (paste below; digest is non-secret — full digest OK)

```
gh run id (gateway):
gh run id (dashboard):
gateway manifest digest @sha256:
dashboard manifest digest @sha256:
```

### Verdict

[PASS] / [FAIL] — {operator initials} {timestamp}

**If FAIL**: ABORT Wave 3 UAT. Phase status = `blocked`. Do NOT continue to
S1-S6, S8. Capture the failure mode and restore via
`RUNBOOK-DEPLOY.md` §GHA retrigger procedure before next attempt.

---

## S1 — Dashboard 2FA enroll end-to-end (D-12, PRD-06) [REDACTED EVIDENCE ONLY]

### Steps

1. Hit `https://ai-dashboard.converse-ai.app/signup` with email
   `<your-handle>@ifixtelecom.com.br` + a disposable password (NEVER a real
   admin password; NEVER paste the password into this sheet).
2. Verify the allowlist accepts the email; redirected to `/2fa/enroll`.
3. Scan the QR with the Authenticator app; type the 6-digit code on
   `/2fa/enroll` step 2.

   > **REDACTED EVIDENCE RULE**: DO NOT screenshot the QR code, the secret
   > string, or the 6-digit code. Capture textual confirmation only
   > ("QR rendered, 6-digit code accepted").

4. Backup codes are shown in a Dialog; copy via "Copiar tudo" → sonner toast
   "10 códigos copiados".

   > **REDACTED EVIDENCE RULE**: DO NOT screenshot the backup codes
   > themselves. Capture only the "10 códigos copiados" toast confirmation.
   > Operator manually transfers codes to a password manager
   > out-of-band.

5. Click "Salvar e continuar" → redirected to `/`.

### Evidence (REDACTED FORM ONLY)

Textual confirmation only — NO TOTP digits, NO QR images, NO backup codes:

```
QR rendered (post-success screen captured, NOT the QR itself): _____
6-digit code accepted (digit NOT captured): _____
10 backup codes displayed (toast captured, NOT the codes): _____
Redirected to /: _____
```

### Verdict

[PASS] / [FAIL] — {operator initials} {timestamp}

---

## S2 — Dashboard 2FA challenge on subsequent login (D-12, PRD-06) [REDACTED EVIDENCE ONLY]

### Steps

1. Logout from the dashboard.
2. Hit `/login` → email + disposable password → HTTP 200 → middleware
   redirects to `/2fa/challenge`.
3. Type the 6-digit TOTP code → `router.push("/")` → reach dashboard home.

   > **REDACTED EVIDENCE RULE**: NO TOTP digit capture in this evidence
   > block.

### Evidence (REDACTED FORM ONLY)

```
Logout OK: _____
Login redirected to /2fa/challenge: _____
TOTP accepted (digit NOT captured): _____
Reached dashboard home: _____
```

### Verdict

[PASS] / [FAIL] — {operator initials} {timestamp}

---

## S3 — Rate-limit /sign-in/email returns 429 after 5 attempts (D-14, PRD-06) [REDACTED EVIDENCE ONLY]

### Steps

1. Operator MUST use a DISPOSABLE test email (e.g.
   `uat-rate-limit@ifixtelecom.com.br`) and a random wrong password
   (NEVER a real admin password). The shell loop posts to
   `https://ai-dashboard.converse-ai.app/api/auth/sign-in/email` 6 times
   with `Content-Type: application/json` body
   `{"email":"uat-rate-limit@ifixtelecom.com.br","password":"<random-wrong-value>"}`
   and captures HTTP status only:

   ```bash
   for i in $(seq 1 6); do
     curl -s -o /dev/null -w '%{http_code}\n' \
       -X POST https://ai-dashboard.converse-ai.app/api/auth/sign-in/email \
       -H 'Content-Type: application/json' \
       -d '{"email":"uat-rate-limit@ifixtelecom.com.br","password":"'"$(openssl rand -hex 8)"'"}'
   done
   ```

   > **REDACTED EVIDENCE RULE**: NEVER paste the password into this sheet
   > or commit history. The body is suppressed via `-o /dev/null`. Use
   > `openssl rand -hex 8` to generate a random wrong value per request.

2. Verify the 6th request returns HTTP 429.
3. Verify `X-Retry-After` header is present:
   ```bash
   curl -I -X POST https://ai-dashboard.converse-ai.app/api/auth/sign-in/email \
     -H 'Content-Type: application/json' \
     -d '{"email":"uat-rate-limit@ifixtelecom.com.br","password":"'"$(openssl rand -hex 8)"'"}'
   ```

### Evidence (REDACTED FORM ONLY)

Paste the 6 HTTP status codes from the loop — NEVER the password, NEVER the
request body:

```
HTTP status sequence (6 requests):  e.g. 401 401 401 401 401 429
X-Retry-After header (curl -I): present? _____ value: _____
```

### Verdict

[PASS] / [FAIL] — {operator initials} {timestamp}

---

## S4 — signUp rejects non-@ifixtelecom (D-13, PRD-06)

### Steps

1. POST to the dashboard signup endpoint with a non-allowlisted domain:

   ```bash
   curl -s -o /tmp/s4-body.json -w '%{http_code}\n' \
     -X POST https://ai-dashboard.converse-ai.app/api/auth/sign-up/email \
     -H 'Content-Type: application/json' \
     -d '{"email":"foo@gmail.com","password":"'"$(openssl rand -hex 12)"'","name":"foo"}'
   ```

2. Verify HTTP `400` or `422` with body containing `"fora do allowlist"` or
   `email_domain_not_allowed` (error string from `dashboard/src/lib/allowlist.ts`).
3. Sanitize before pasting: `jq 'del(.password)' /tmp/s4-body.json` to ensure
   the disposable password is NOT in the captured body.

### Evidence

```
HTTP status: _____
Error message excerpt (no password):
```

### Verdict

[PASS] / [FAIL] — {operator initials} {timestamp}

---

## S5 — gatewayctl debug emit-error → Sentry event lands (D-18.2)

### Steps

1. Operator ensures `${AI_GATEWAY_ADMIN_KEY}` is sourced from
   `/etc/ifix/keys.env` (NEVER inline literal). Then:

   ```bash
   ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl debug emit-error \
     --gateway=http://localhost:8080 \
     --admin-key="$AI_GATEWAY_ADMIN_KEY"'
   ```

   Expect exit 0 + body `status=500` (the synthetic panic IS the expected
   path through the recoverer).

2. Open the Sentry UI for project `ifix-ai-gateway-prod`
   (id `4511455942017024`); filter recent events for the breadcrumb message
   "synthetic panic emitted by gatewayctl debug emit-error" (or whatever
   marker the implementation chose — verify via `gateway/cmd/gatewayctl/debug.go`).

3. Confirm the event landed within ~5s of the step-1 command.

### Evidence

```
gatewayctl exit code:
gatewayctl response (status=500): _____
Sentry event URL (non-secret):
Latency from emit to Sentry: ~_____s
```

### Verdict

[PASS] / [FAIL] — {operator initials} {timestamp}

---

## S6 — gatewayctl key list returns aligned table (D-18.3)

### Steps

1. List keys (no tenant filter):

   ```bash
   ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl key list'
   ```

   Expect a tabwriter-aligned table with columns:
   `ID  TENANT  PREFIX  STATUS  DATA_CLASS  CREATED  LAST_USED`.

2. Verify NO raw API key string appears in the output. The `PREFIX` column
   is the first-4-char marker only (e.g. `ifix_sk_****`).
3. Verify the `--tenant` filter narrows results:

   ```bash
   ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl key list \
     --tenant=converseai'
   ```

   Expect a subset of rows where TENANT column == `converseai`.

### Evidence

Paste output but TRUNCATE PREFIX column to first 4 chars before pasting
(defense-in-depth — even though `key_prefix` is already non-secret):

```
gatewayctl key list (truncated PREFIX column):
ID                                      TENANT       PREFIX        STATUS  ...
xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx    converseai   ifix_sk_****  active  ...
...

gatewayctl key list --tenant=converseai:
(subset of the above; only TENANT=converseai rows)
```

### Verdict

[PASS] / [FAIL] — {operator initials} {timestamp}

---

## S8 — Per-env keys diff verification (D-19) — sanitized first-4-char prefix recipe

> **S7 omitted.** Per plan 11-10 frontmatter `scope_note`, the scenario set
> is S0 (prerequisite) + S1..S6 + S8 (eight total). The slot formerly known
> as S7 was re-ordered to S0 (PREREQUISITE gate, GHA workflow_dispatch) for
> reviews MEDIUM M3. There is no separate S7.

### Steps

1. Operator already executed the D-19 rotation procedure per
   `gateway/docs/RUNBOOK-DEPLOY.md` (Phase 11 plan 11-05). This scenario
   verifies the prod and dev `.env` files diverge at the upstream-bearer
   prefix level.

2. Run the SANITIZED diff (first-4-char prefixes only — per 11-05 recipe;
   NEVER expose raw values):

   ```bash
   # Pipe each grep through awk BEFORE the diff. The awk pipeline emits
   # only `KEY=PREFIX...` (first 4 chars only) — raw values never reach the
   # diff input.
   diff \
     <(ssh n8n-ia-vm 'sudo grep -E "^UPSTREAM_.*_AUTH_BEARER" /opt/ai-gateway-prod/.env' \
        | awk -F= '{key=$1; val=$2; printf "%s=%s...\n", key, substr(val,1,4)}') \
     <(ssh vps-ifix-vm 'sudo grep -E "^UPSTREAM_.*_AUTH_BEARER" /opt/ai-gateway-dev/.env' \
        | awk -F= '{key=$1; val=$2; printf "%s=%s...\n", key, substr(val,1,4)}')
   ```

   Expect 4 prefix-differing lines (one per OpenRouter / OpenAI Whisper /
   OpenAI Embed / Vast bearer) → confirms dev↔prod separation.

3. Verify `seed-admins.sh` ran successfully (dashboard table has ≥4
   @ifixtelecom rows). Use sanitized SELECT (operator sources `${PROD_DSN}`
   from `/etc/ifix/keys.env`; NEVER inline DSN literal):

   ```bash
   psql "$PROD_DSN" -t -c \
     "SELECT email FROM dashboard_auth.user WHERE email LIKE '%@ifixtelecom.com.br'" \
     | wc -l
   ```

   Expect ≥ 4.

### Evidence

```
Sanitized diff output (var names + first-4-char prefixes only;
NO raw key values):
< UPSTREAM_LLM_OPENROUTER_AUTH_BEARER=ABCD...
> UPSTREAM_LLM_OPENROUTER_AUTH_BEARER=WXYZ...
< UPSTREAM_STT_OPENAI_AUTH_BEARER=...
...

Admin row count: _____ (≥4 expected)
```

### Verdict

[PASS] / [FAIL] — {operator initials} {timestamp}

---

## Cumulative spend rollup

| Plan | Activity | Spend (USD) | Notes |
|------|----------|-------------|-------|
| 11-06 | Load-test live UAT (Vast 2×3090) | $0.04 | Orphan primary destroyed mid-attempt; UAT BLOCKED on reconciler silent-hang |
| 11-07 | Chaos primary kill (Vast API DELETE) | $0.00 | Script shipped; live exec deferred (depends_on 11-06) |
| 11-08 | Chaos OpenRouter DROP (iptables) | $0.00 | No Vast spend; iptables-only chaos on n8n-ia-vm |
| **TOTAL** | | **$0.04** | Well within $5 Phase 11 absolute cap |

---

## Evidence Appendix — REDACTED EVIDENCE RULE summary [reviews LOW L4]

Before commit, operator MUST verify each line:

- [ ] No QR code images attached to S1 / S2 evidence blocks
- [ ] No 6-digit TOTP numbers in S1 / S2 / S3 evidence text
- [ ] No 10-set alphanumeric backup-code strings anywhere
- [ ] No `ifix_sk_...` literals anywhere
- [ ] No `ifix_admin_...` literals anywhere
- [ ] No raw passwords (disposable OR real) anywhere
- [ ] No raw `postgres://user:pass@host/db` DSNs anywhere
- [ ] REDACTED EVIDENCE rule statement appears in this sheet ≥3 times
      (top cross-cutting block, S1 enroll, S3 rate-limit — verify via
      `grep -ic REDACTED 11-HUMAN-UAT.md` returns ≥3)

Pre-commit grep gates (operator runs these and the commit is BLOCKED if any
fail):

```bash
# Raw API keys
! grep -E 'ifix_sk_[a-z0-9]{20,}' .planning/phases/11-prod-hardening/11-HUMAN-UAT.md

# Raw admin keys
! grep -E 'ifix_admin_[a-z0-9]{20,}' .planning/phases/11-prod-hardening/11-HUMAN-UAT.md

# Raw bearer tokens (60+ hex)
! grep -E 'Bearer [a-fA-F0-9]{60,}' .planning/phases/11-prod-hardening/11-HUMAN-UAT.md

# Raw postgres DSNs with credentials
! grep -E 'postgres://[^[:space:]]{10,}' .planning/phases/11-prod-hardening/11-HUMAN-UAT.md

# REDACTED rule preserved (≥3 occurrences)
[ "$(grep -ic REDACTED .planning/phases/11-prod-hardening/11-HUMAN-UAT.md)" -ge 3 ]
```

---

## Operator sign-off

After all scenarios complete, operator confirms:

- [ ] S0 PREREQUISITE gate PASSED (if FAILED → Status: `blocked`, halt here)
- [ ] S1..S6 + S8 verdicts captured (PASS / FAIL each)
- [ ] Post-session env cleanup line executed
- [ ] All pre-commit grep gates PASS

Final status (operator selects ONE):

- [ ] `passed` — S0 PASS + S1..S6 + S8 all PASS
- [ ] `passed_partial` — S0 PASS + 1-2 FAIL in S1-S6/S8 with documented
      justification (e.g. carry-forward tech debt from 11-06/11-07/11-08)
- [ ] `failed` — S0 PASS + ≥3 FAIL in S1-S6/S8 (triggers
      `/gsd:plan-phase 11 --gaps`)
- [ ] `blocked` — S0 FAIL (NEVER `passed_partial` when S0 fails)

Sign-off:

```
Operator: pedro
Initials:
Timestamp:
```

---

*Sheet authored: 2026-05-27 by Task 11-10-01 (autonomous executor).
Operator-driven execution (Task 11-10-02) signs each scenario verdict and
fills the evidence blocks per the REDACTED EVIDENCE RULE. Final phase status
rolled up into `11-VERIFICATION.md` by Task 11-10-03.*
