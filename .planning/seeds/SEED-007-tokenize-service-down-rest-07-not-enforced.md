# SEED-007 — `tokencount /tokenize` service unreachable; RES-07 token-cap silently bypassed

**Planted:** 2026-05-28
**Discovered during:** Phase 11 11-06 corpus exerciser run (2026-05-28T19:30Z)
**Status:** seed — not yet promoted to phase
**Related:** [[audit-blocked-sensitive-override-not-propagated]] debug session siblings; RES-07 context-cap enforcement (`gateway/internal/proxy/dispatcher.go:174-194` + `gateway/internal/proxy/tokencount.go`)

## Problem

The ai-gateway prod stack on `n8n-ia-vm` (10.10.10.20) is configured to call an out-of-process tokenize service at `http://10.10.10.20:8000/tokenize` for context-cap enforcement (RES-07 — block requests whose model+prompt would exceed `ContextCap` before they hit the upstream). The service is **down**: every prod request emits

```
{"level":"WARN","msg":"tokencount /tokenize request failed","module":"GATEWAY","module":"TOKENIZE","err":"Post \"http://10.10.10.20:8000/tokenize\": dial tcp 10.10.10.20:8000: connect: connection refused"}
```

`tokencount.Enforce` is wrapped in a tolerant `if err == nil { ... }` path — when the call fails, RES-07 falls through to the dispatcher's downstream proxy without blocking. So the **gate silently no-ops in prod**: a request that should be rejected for `context_length_exceeded` (HTTP 400) instead reaches the upstream, which then either fails noisily, eats expensive tokens, or in the worst case returns truncated output.

## Empirical evidence (2026-05-28T19:30Z, exerciser corpus run)

```
$ ssh n8n-ia-vm 'docker logs ifix-ai-gateway --since 20m 2>&1 | grep -c "tokencount /tokenize request failed"'
1700+
```

Every chat completion (650 requests + their model-rewrite probes) emitted the WARN; zero RES-07 rejections were recorded in `bd_ai_gateway_prod.ai_gateway.audit_log` for the entire run.

## Why It Was Missed

- The integration tests under `gateway/internal/integration_test/` mock the tokenizer at a fake HTTP server pinned in `phase4_fixtures.go`; the production wiring to a real out-of-process tokenize service is environment-specific and never re-asserted at deploy time.
- The dispatcher's tolerant-failure path is by design (RES-07 is best-effort — a tokenize outage shouldn't take down the gateway), but there is no Prometheus alert, no Sentry tag, and no audit_log column that distinguishes "request passed RES-07" from "request bypassed RES-07 due to tokenize error".
- Phase 06.7 weights deploy refresh added new model aliases but didn't re-validate the tokenize service health.

## Impact

1. **Cost surprise**: a tenant could send a 200K-token prompt and the gateway happily proxies it to OpenRouter / OpenAI, eating $$$ before the upstream rejects (some providers truncate silently — invisible until the response is empty).
2. **SLO drift**: RES-07 is what keeps P95 latency bounded — without the gate, a single oversized prompt can saturate the chat queue.
3. **Audit forensics gap**: no record of "should-have-rejected" requests, so post-incident review can't quantify the bypass.

## Scope of Fix

### Option A — Bring the tokenize service back up (minimal, scoped)

1. SSH `n8n-ia-vm`, identify which container hosts the tokenize service (likely `whisper`-style sidecar from the Phase 06.7 stack or a missing `tokenize` container in `/opt/ai-gateway-prod/docker-compose.yml`).
2. Restart / re-deploy. Verify `curl http://10.10.10.20:8000/tokenize -d '{"model":"qwen","text":"hi"}'` returns a JSON token count.
3. Drop the WARN count to zero across a 10-min observation window.

### Option B — Add a Prometheus alert + audit column (defence in depth)

1. New `obs.TokenizeRequestsTotal{outcome="ok|err"}` counter (already exists? check `gateway/internal/proxy/tokencount.go`). If missing, add.
2. Sentry tag `tokencount_bypassed=true` on every request whose RES-07 gate skipped due to err.
3. New `audit_log.tokencount_bypassed BOOLEAN DEFAULT FALSE` (migration) so the bypass is forensically recoverable.
4. Operator runbook (`gateway/docs/RUNBOOK-INCIDENTS.md` Class 3) updated to point at this dashboard panel.

### Recommendation

**Option A first** (bring the service up — the immediate exposure ends), **then Option B** as a follow-up to prevent silent recurrence. Option A is operator-driven, ~10 min of investigation. Option B is a small phase plan (~3-5 LOC + migration + runbook update).

## Test Plan (when promoted)

- Sanity (after Option A): `for i in $(seq 1 20); do curl ...chat/completions -d '{"model":"qwen","messages":[...10K tokens...],"max_tokens":1}'; done` — expect rejections with `context_length_exceeded` envelope when ContextCap exceeded.
- Sentinel: `ssh n8n-ia-vm 'docker logs ifix-ai-gateway --since 5m | grep -c "tokencount /tokenize request failed"'` returns 0.
- Regression (after Option B): kill tokenize service mid-run, assert `obs.TokenizeRequestsTotal{outcome="err"}` increments and `audit_log.tokencount_bypassed=true` rows appear.

## Files

- `gateway/internal/proxy/dispatcher.go:174-194` (RES-07 call site)
- `gateway/internal/proxy/tokencount.go` (Enforce + HTTP client)
- `/opt/ai-gateway-prod/docker-compose.yml` on n8n-ia-vm (likely missing tokenize container)
- `gateway/db/migrations/` (if Option B — new audit column)
- `gateway/docs/RUNBOOK-INCIDENTS.md` (if Option B — Class 3 runbook annex)
