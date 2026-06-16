---
phase: quick-260616-gtj
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - gateway/internal/upstreams/probe.go
  - gateway/internal/upstreams/probe_test.go
autonomous: true
requirements: [QUICK-260616-gtj]
must_haves:
  truths:
    - "A 4xx probe response no longer records last_probe_status=failed for a breaker-healthy upstream"
    - "A 5xx probe response still records last_probe_status=failed (genuine health failure)"
    - "A probe timeout still records last_probe_status=timeout"
    - "A 2xx probe response still records last_probe_status=ok"
  artifacts:
    - path: gateway/internal/upstreams/probe.go
      provides: "4xx-aware probe status classification in probeOne"
      contains: "errors.As"
    - path: gateway/internal/upstreams/probe_test.go
      provides: "Tests asserting status classification for 2xx/4xx/5xx/timeout"
      contains: "TestProbe_StatusClassification"
  key_links:
    - from: "probeOne status switch"
      to: "breaker.HTTPError.Status"
      via: "errors.As type assertion + 400<=Status<500 range check"
      pattern: "errors\\.As"
---

<objective>
Fix the tier-1 probe false-negative: `probeOne` in `gateway/internal/upstreams/probe.go`
records `last_probe_status="failed"` for 4xx upstream responses, even though the
breaker already classifies 4xx as SUCCESS (client/config issue, not upstream health).
Prod symptom: `openrouter-chat` breaker = `OBSERVATION_closed` (healthy, serving HTTP 200
live) but `gatewayctl upstreams list` shows `LAST_PROBE_STATUS=failed` — the probe lies
about a healthy upstream (12-FIELD-FINDINGS finding 2).

Root cause: `probeOne` (L231-243) status switch only checks `err == nil` → "ok",
otherwise `default` → "failed". A 4xx is returned by `breaker.Execute` as a non-nil
`*breaker.HTTPError` (the breaker treats it as success internally, but still surfaces the
error), so the writeback records "failed".

Purpose: Make the recorded probe status reflect the breaker's own 4xx-vs-5xx
classification, so observability (`/v1/health/upstreams` CLI text + `gatewayctl upstreams
list`) stops reporting healthy tier-1 upstreams as failed.

Output: Updated `probeOne` classification + new probe_test cases. Build + tests green.
Deploy (push develop → Actions → Portainer) is OUT OF SCOPE for this quick task.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md
@.planning/phases/12-gateway-resilience-remediation-inserted-from-11-06-11-07-liv/12-FIELD-FINDINGS-2026-06-16.md

<interfaces>
<!-- Contracts the executor needs. DO NOT change breaker logic. -->

From gateway/internal/breaker/breaker.go (DO NOT MODIFY — read-only contract):
```go
// HTTPError is the typed error the dispatcher emits for non-2xx upstream status.
type HTTPError struct {
    Status int
    Msg    string
}
func (e *HTTPError) Error() string { return e.Msg }

// IsSuccessful: 4xx (incl 429) → true (client error, not health); 5xx → false.
// errors.As(err, &he) + he.Status>=400 && he.Status<500 is the canonical 4xx test.
func IsSuccessful(err error) bool { ... }
```

From gateway/internal/upstreams/probe.go — probeOne status switch (CURRENT, the bug):
```go
var status, errMsg string
switch {
case err == nil:
    status = "ok"
case ctx.Err() == context.DeadlineExceeded:
    status = "timeout"
    errMsg = "probe budget exceeded"
    obs.ProbeFailureTotal.WithLabelValues(u.Name, "timeout").Inc()
default:
    status = "failed"          // <-- a 4xx HTTPError lands here. BUG.
    errMsg = err.Error()
    obs.ProbeFailureTotal.WithLabelValues(u.Name, "error").Inc()
}
p.enqueueUpdate(u.Name, dur, status, errMsg)
```

dispatch already wraps 4xx and 5xx into *breaker.HTTPError (probe.go L320-328) — no
change needed there; the 4xx wrap is the input this fix classifies.

DOWNSTREAM CONSUMER ANALYSIS (already verified — informs the chosen status value):
- last_probe_status is a free-text DB column (gen/models.go: LastProbeStatus pgtype.Text).
- gateway/internal/upstreams/health.go computes the /v1/health/upstreams AGGREGATE from
  BREAKER STATE (stateFor / "closed"), NOT from last_probe_status. So the aggregate is
  already correct; only the per-row text column lies.
- No code branches on the specific last_probe_status string — gatewayctl upstreams list
  prints it verbatim. Introducing a new value ("config") breaks no consumer.
</interfaces>
</context>

<tasks>

<task type="auto" tdd="true">
  <name>Task 1: Classify 4xx probe responses as "config", not "failed"</name>
  <files>gateway/internal/upstreams/probe.go, gateway/internal/upstreams/probe_test.go</files>
  <read_first>
    - gateway/internal/upstreams/probe.go (probeOne L223-245 status switch; dispatch L251-330, esp. 4xx wrap L323-328)
    - gateway/internal/breaker/breaker.go (HTTPError L408-420 + IsSuccessful L389-406 — the 4xx-vs-5xx contract to stay consistent with; DO NOT modify)
    - gateway/internal/upstreams/probe_test.go (existing test scaffolding: newProbeFor / httptest patterns, errors.As usage at L120-126)
  </read_first>
  <behavior>
    - Test 2xx: dispatch hits a 200 httptest server → probeOne records status == "ok".
    - Test 4xx: dispatch hits a 400 (and a 404) server → probeOne records status NOT
      "failed". Asserted value: "config". (4xx is a client/config issue, breaker stays
      closed; breaker.IsSuccessful returns true for 4xx.)
    - Test 5xx: dispatch hits a 502 server → probeOne records status == "failed"
      (genuine upstream health failure; unchanged).
    - Test timeout: a probe whose context deadline is exceeded → status == "timeout"
      (unchanged).
  </behavior>
  <action>
    In probeOne (gateway/internal/upstreams/probe.go), add a case to the status switch
    BETWEEN the DeadlineExceeded case and the default case: detect a 4xx by extracting a
    *breaker.HTTPError via errors.As (NOT string-matching err.Error()), and when the
    extracted Status is >= 400 && < 500, set status = "config" and errMsg = err.Error().
    Do NOT increment obs.ProbeFailureTotal for the 4xx case (it is not a failure — keep the
    failure counters reserved for timeout and real "error"). Leave the DeadlineExceeded
    ("timeout"), the err==nil ("ok"), and the remaining default ("failed", 5xx + transport
    errors) branches exactly as they are. Import the existing errors package if not already
    imported in probe.go (probe_test.go already imports it; check probe.go's import block).
    Rationale for "config" over "ok": preserves the truth that the probe got a non-2xx
    while NOT signalling a health failure. Verified safe: last_probe_status is free text,
    the /v1/health/upstreams aggregate derives from breaker state (health.go), and no
    consumer branches on the string — so a new value has zero blast radius. Add a short
    code comment on the new case referencing D-A4 / breaker.IsSuccessful 4xx-as-success and
    the field-findings false-negative this fixes.

    In probe_test.go, add TestProbe_StatusClassification driving probeOne (not just
    dispatch) end-to-end through the breaker against httptest servers returning 200, 400,
    404, and 502, plus a timeout case (a context already past its deadline, or a server that
    blocks past a tiny probe budget). Read enqueued status via the probe's update channel /
    writeback inspection — follow the existing newProbeFor wiring (q==nil); if probeOne's
    enqueueUpdate output is not directly observable in tests, capture it via the buffered
    updates channel exposed to the Probe (inspect NewProbe signature + enqueueUpdate at
    probe.go L336+ to find the observable seam). Assert: 200→"ok", 400→"config",
    404→"config", 502→"failed", timeout→"timeout". DO NOT modify gateway/internal/breaker/.
  </action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix/gateway && go build ./... && go test ./internal/upstreams/ -run Probe -count=1</automated>
  </verify>
  <done>
    - probeOne classifies 4xx (*breaker.HTTPError, 400<=Status<500) as status "config" via errors.As, not "failed".
    - 5xx → "failed", timeout → "timeout", 2xx → "ok" all unchanged.
    - probe_test.go contains TestProbe_StatusClassification asserting 200/400/404/502/timeout → ok/config/config/failed/timeout.
    - `go build ./...` exits 0 and `go test ./internal/upstreams/ -run Probe` exits 0.
    - No file under gateway/internal/breaker/ was modified.
  </done>
</task>

</tasks>

<verification>
- `cd gateway && go build ./...` exits 0.
- `cd gateway && go test ./internal/upstreams/ -run Probe -count=1` exits 0.
- `git diff --name-only` shows ONLY probe.go + probe_test.go changed (no breaker/ files).
- New 4xx test case asserts status != "failed" (specifically "config").
</verification>

<success_criteria>
- A breaker-healthy upstream returning 4xx to the probe (e.g. openrouter-chat with the
  hardcoded `{"model":"qwen"}` body) records last_probe_status="config", so
  `gatewayctl upstreams list` no longer shows it as "failed".
- Genuine 5xx and timeout failures still record "failed"/"timeout".
- Build + targeted tests green. Breaker package untouched.
</success_criteria>

<output>
Create `.planning/quick/260616-gtj-fix-tier-1-probe-false-negative-status-4/260616-gtj-SUMMARY.md` when done.
</output>
