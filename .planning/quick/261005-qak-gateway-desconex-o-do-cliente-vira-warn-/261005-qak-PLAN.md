---
phase: quick-261005-qak
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - gateway/internal/httpx/recoverer.go
  - gateway/internal/httpx/logger.go
  - gateway/internal/httpx/recoverer_test.go
  - gateway/internal/proxy/toolcall.go
  - gateway/internal/proxy/toolcall_abort_test.go
autonomous: true
requirements: [QAK-01, QAK-02, QAK-03, QAK-04, QAK-05]
clickup: 86akt86dc

must_haves:
  truths:
    - "A client disconnect mid-body-copy (http.ErrAbortHandler) logs WARN client_disconnected with request_id/method/path, not ERROR 'panic recovered'"
    - "On abort the gateway writes NO 500 / no OpenAI envelope and re-panics http.ErrAbortHandler so http.Server still aborts the connection (no clean close of a truncated chunked body)"
    - "The access log line 'request' is still emitted for aborted requests, with the status actually sent (first WriteHeader) and aborted=true"
    - "Any other panic keeps the current behavior: ERROR 'panic recovered' + Sentry + OpenAI envelope 500"
    - "ToolCallTerminalGuard does not send http.ErrAbortHandler to Sentry, still writes its terminal SSE frame when applicable and still re-panics"
    - "No more 'superfluous response.WriteHeader' from statusWriter on the abort path"
  artifacts:
    - path: "gateway/internal/httpx/recoverer.go"
      provides: "ErrAbortHandler branch: WARN + re-panic"
      contains: "http.ErrAbortHandler"
    - path: "gateway/internal/httpx/logger.go"
      provides: "deferred access log with aborted flag + first-WriteHeader-wins status"
      contains: "aborted"
    - path: "gateway/internal/httpx/recoverer_test.go"
      provides: "abort vs ordinary panic coverage through Logger(Recoverer(h))"
    - path: "gateway/internal/proxy/toolcall_abort_test.go"
      provides: "guard does not capture ErrAbortHandler in Sentry, still re-panics"
  key_links:
    - from: "gateway/cmd/gateway/main.go buildRouter"
      to: "httpx.Logger -> httpx.Recoverer"
      via: "r.Use order (RequestID, Logger, Recoverer) — Logger is OUTER, so the re-panic from Recoverer passes through Logger's defer"
      pattern: "r.Use\\(httpx.Recoverer"
---

<objective>
Treat `http.ErrAbortHandler` (panic raised by `httputil.ReverseProxy.ServeHTTP` when copying the
response body fails — in production the client, Maestro, cancelled) as an expected client
disconnect: WARN `client_disconnected`, no Sentry, no 500 write, connection-abort semantics
preserved via re-panic, and the access log keeps reporting the REAL status plus `aborted=true`.

Purpose: production log 2026-10-05 showed `ERROR panic recovered panic="net/http: abort Handler"`,
a fake `status:500` on the access line (upstream headers had already been sent as 200) and
`http: superfluous response.WriteHeader ... logger.go:52`. This is noise in ERROR/Sentry and a
wrong status in the access log.

Output: patched recoverer.go, logger.go, toolcall.go + unit tests. ClickUp card 86akt86dc.
</objective>

<execution_context>
@$HOME/.claude/get-shit-done/workflows/execute-plan.md
@$HOME/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md
@gateway/internal/httpx/recoverer.go
@gateway/internal/httpx/logger.go
@gateway/internal/proxy/toolcall.go

<interfaces>
Middleware chain (gateway/cmd/gateway/main.go buildRouter, ~line 1630):
  r.Use(httpx.RequestID); r.Use(httpx.Logger(log)); r.Use(httpx.Recoverer(log))
  => effective wrap: RequestID(Logger(Recoverer(routes))). Logger is OUTSIDE Recoverer.

httpx (existing):
  func Recoverer(base *slog.Logger) func(http.Handler) http.Handler
  func Logger(base *slog.Logger) func(http.Handler) http.Handler
  func RequestID(next http.Handler) http.Handler            // requestid.go
  func RequestIDFrom(ctx context.Context) string
  func WithLogger(ctx, *slog.Logger) context.Context; func LoggerFrom(ctx) *slog.Logger
  func WriteOpenAIError(w, status int, typ, code, msg string)   // envelope.go
  type statusWriter struct { http.ResponseWriter; status int; bytes int }  // WriteHeader always overwrites status today

proxy (existing, toolcall.go ~205-240):
  func NewToolCallInterceptor() *ToolCallInterceptor
  func (t *ToolCallInterceptor) Flag(reqID string) *atomic.Bool
  func (t *ToolCallInterceptor) Clear(reqID string)
  func ToolCallTerminalGuard(next http.Handler, tci *ToolCallInterceptor, upstreamName, route string) http.Handler
    deferred: rec := recover(); terminal SSE frame logic; if rec != nil { sentry.CurrentHub().RecoverWithContext(r.Context(), rec); sentry.Flush(200ms); panic(rec) }

Sentry test-transport pattern (copy, do not import): gateway/internal/emerg/budget_test.go
  `recordingTransport` (Configure/SendEvent/Flush/FlushWithContext/Close per sentry-go Transport
  interface) + sentry.Init(ClientOptions{Dsn:"https://public@sentry.example.com/1", Transport: tr})
  with t.Cleanup restoring sentry.Init(sentry.ClientOptions{}).
</interfaces>
</context>

<tasks>

<task type="auto" tdd="true">
  <name>Task 1: httpx — Recoverer abort branch + Logger aborted flag/real status (with tests)</name>
  <files>gateway/internal/httpx/recoverer.go, gateway/internal/httpx/logger.go, gateway/internal/httpx/recoverer_test.go</files>
  <behavior>
    All tests drive the real chain RequestID(Logger(base)(Recoverer(base)(h))) with base = slog JSON
    handler writing to a bytes.Buffer (level Debug), httptest.NewRecorder, and call ServeHTTP inside a
    helper that recover()s and returns the recovered value. Parse buffer line-by-line as JSON.
    - TestRecoverer_ClientAbort: h sets a header, WriteHeader(200), writes "data: partial\n\n", then
      panic(http.ErrAbortHandler). Expect: recovered value == http.ErrAbortHandler (re-panic propagated);
      recorder Code == 200; body does NOT contain "internal_error"; one record level WARN msg
      "client_disconnected" with request_id (non-empty), method POST, path "/v1/chat/completions";
      NO record with msg "panic recovered"; exactly one "request" record with status 200,
      aborted true, bytes == len("data: partial\n\n").
    - TestRecoverer_ClientAbortBeforeHeaders: h panics ErrAbortHandler without writing anything.
      Expect re-panic propagated, no 500 written by the gateway (recorder body empty, no envelope),
      WARN client_disconnected present, "request" record has aborted true.
    - TestRecoverer_OrdinaryPanic: h panic("boom"). Expect: no panic escapes (recovered nil);
      recorder Code 500 with body containing "internal_error"; ERROR record "panic recovered";
      "request" record status 500 and aborted absent or false.
    - TestStatusWriter_FirstWriteHeaderWins: statusWriter WriteHeader(200) then WriteHeader(500)
      → sw.status == 200.
  </behavior>
  <action>
    Write the tests first (RED), then implement (GREEN). Package `httpx`, stdlib `testing` +
    `encoding/json` (match envelope_test.go / requestid_test.go style; check which assertion lib
    they use and follow it).

    recoverer.go: inside the deferred recover, add a first branch `if rec == http.ErrAbortHandler`
    (compare with `==`; it is a sentinel error value). In it: `base.WarnContext(r.Context(),
    "client_disconnected", "request_id", RequestIDFrom(r.Context()), "method", r.Method, "path",
    r.URL.Path)`; do NOT call sentry; do NOT call WriteOpenAIError; then `panic(http.ErrAbortHandler)`
    to keep stdlib abort semantics (http.Server recovers ErrAbortHandler silently and kills the
    connection — returning normally instead would let a chunked response terminate cleanly and a
    still-connected client would see a truncated body as complete). Leave the existing ERROR +
    Sentry + 500 path byte-for-byte unchanged for every other panic value. Update the package/func
    doc comments (English) to describe the ErrAbortHandler exception.

    logger.go: move the "request" emission into a defer so it runs on the re-panic path. Pattern:
    deferred func does `rec := recover()`, sets `aborted := rec == http.ErrAbortHandler`, emits
    reqLog.Info("request", status, bytes, latency_ms, plus "aborted", true only when aborted — keep
    the normal line's attribute set unchanged otherwise), then `if rec != nil { panic(rec) }` so
    any panic (abort or otherwise, e.g. if Recoverer were ever missing) keeps propagating. Change
    statusWriter to record only the FIRST WriteHeader (add a `wroteHeader bool`; Write without a
    prior WriteHeader also marks it with the implicit 200) while still forwarding every call to the
    underlying ResponseWriter — this makes the access status the one actually sent on the wire.
    Keep Flush passthrough intact. Update the file doc comment to mention the aborted field.
    Do not touch main.go ordering (Logger outer, Recoverer inner is what makes this work).
  </action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix/gateway && gofmt -l ./internal/httpx && go vet ./internal/httpx/... && go test ./internal/httpx/... -run 'Recoverer|StatusWriter' -count=1 -v</automated>
  </verify>
  <done>4 new tests pass; gofmt -l prints nothing; abort path: WARN + re-panic + real status + aborted=true; ordinary panic path unchanged (ERROR + 500).</done>
</task>

<task type="auto" tdd="true">
  <name>Task 2: ToolCallTerminalGuard — skip Sentry for ErrAbortHandler (with test) + full gates</name>
  <files>gateway/internal/proxy/toolcall.go, gateway/internal/proxy/toolcall_abort_test.go</files>
  <behavior>
    Install a recording Sentry transport (copy the recordingTransport pattern from
    internal/emerg/budget_test.go into this test file under a unique name, e.g.
    `guardRecordingTransport`, to avoid symbol clashes in package proxy; check `grep -n
    recordingTransport internal/proxy/*_test.go` first).
    - TestToolCallTerminalGuard_AbortNotSentToSentry: next handler writes 200 + an SSE chunk then
      panic(http.ErrAbortHandler); request ctx carries a request id (use whatever helper the existing
      toolcall_test.go uses to set it, see its dummyCtxKey note, or httpx.RequestID middleware).
      Expect: recovered value == http.ErrAbortHandler (still re-panics); transport recorded 0 events.
    - TestToolCallTerminalGuard_OtherPanicStillSentToSentry: next panics "boom". Expect re-panic of
      "boom" and transport recorded >= 1 event.
    - If the request's tool_call flag is set (tci.Flag(reqID).Store(true)) on the abort test, the
      terminal SSE frame code "tool_call_partial_stream" still appears in the recorder body.
  </behavior>
  <action>
    In toolcall.go's deferred func, wrap the `sentry.CurrentHub().RecoverWithContext(...)` +
    `sentry.Flush(200ms)` pair in `if rec != http.ErrAbortHandler { ... }`; keep `panic(rec)` for
    all non-nil rec, and keep the terminal SSE frame logic before it unchanged. Adjust the existing
    comment (English) to say ErrAbortHandler is an expected client/upstream disconnect and is
    re-panicked without Sentry capture (httpx.Recoverer logs it as WARN client_disconnected).
    Write tests first, then the change. Then run full gates. Commit per task, conventional English
    messages, e.g. `fix(gateway): log client disconnect (ErrAbortHandler) as WARN, keep real access status`
    and `fix(gateway): skip Sentry capture for ErrAbortHandler in tool-call guard`, each ending with
    `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Do not push/deploy (not requested).
  </action>
  <verify>
    <automated>cd /home/pedro/projetos/pedro/gpu-ifix/gateway && test -z "$(gofmt -l .)" && go build ./... && go vet ./... && go test ./internal/httpx/... ./internal/proxy/... -count=1 && go test ./... -count=1</automated>
  </verify>
  <done>Guard tests pass; gofmt clean; go build/vet clean; go test ./... green (integration-tagged tests are out of scope of this gate).</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| client -> gateway | client may disconnect at any time during streaming |
| gateway -> Sentry | error telemetry egress |

## STRIDE Threat Register

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-qak-01 | Tampering/Integrity | Recoverer abort branch | mitigate | re-panic ErrAbortHandler so http.Server aborts the conn; a truncated chunked body is never closed cleanly (Task 1 test asserts re-panic) |
| T-qak-02 | Repudiation | access log | mitigate | "request" line still emitted on abort with real status + aborted=true (Task 1 test) |
| T-qak-03 | Denial of Service | Sentry quota | mitigate | client disconnects no longer captured in Recoverer or ToolCallTerminalGuard (Task 2 test asserts 0 events) |
| T-qak-04 | Elevation/Info disclosure | non-abort panics | accept | path unchanged: sanitized OpenAI envelope 500, no panic detail to client |
</threat_model>

<verification>
- `cd gateway && gofmt -l .` empty; `go build ./...`, `go vet ./...`, `go test ./...` green.
- grep: `grep -n "ErrAbortHandler" gateway/internal/httpx/recoverer.go gateway/internal/httpx/logger.go gateway/internal/proxy/toolcall.go` shows a branch in each.
</verification>

<success_criteria>
- Abort: WARN client_disconnected, no Sentry, no 500, re-panic, access log real status + aborted=true.
- Other panics: unchanged ERROR + Sentry + 500.
- Tool-call guard: no Sentry for abort, SSE terminal frame + re-panic preserved.
</success_criteria>

<output>
Create `.planning/quick/261005-qak-gateway-desconex-o-do-cliente-vira-warn-/261005-qak-SUMMARY.md` when done
</output>
