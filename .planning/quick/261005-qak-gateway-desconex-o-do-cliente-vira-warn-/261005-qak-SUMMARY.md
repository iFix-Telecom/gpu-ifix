---
phase: quick-261005-qak
plan: 01
status: complete
subsystem: gateway/httpx, gateway/proxy
tags: [logging, sentry, client-disconnect, ErrAbortHandler]
clickup: 86akt86dc
requirements: [QAK-01, QAK-02, QAK-03, QAK-04, QAK-05]
key-files:
  modified:
    - gateway/internal/httpx/recoverer.go
    - gateway/internal/httpx/logger.go
    - gateway/internal/proxy/toolcall.go
  created:
    - gateway/internal/httpx/recoverer_test.go
    - gateway/internal/proxy/toolcall_abort_test.go
decisions:
  - "ErrAbortHandler is re-panicked (not swallowed) so net/http aborts the connection and a truncated chunked body is never terminated cleanly"
  - "statusWriter records the first WriteHeader (or implicit 200 on Write), so the access log shows the status actually sent on the wire"
metrics:
  completed: 2026-10-05
  tasks: 2
---

# Quick 261005-qak: client disconnect becomes WARN — Summary

When a client disconnects (`http.ErrAbortHandler`), the gateway now logs `WARN client_disconnected` with request_id/method/path. It no longer sends the event to Sentry, writes no 500 envelope, and re-panics so net/http aborts the connection. The access log line `request` keeps the real status, adds `aborted=true`, and no longer triggers a "superfluous WriteHeader" warning.

## Commits

| Task | Commit | Subject |
|------|--------|---------|
| 1 | cd91c68 | fix(gateway): log client disconnect (ErrAbortHandler) as WARN, keep real access status |
| 2 | 65bdb23 | fix(gateway): skip Sentry capture for ErrAbortHandler in tool-call guard |

## Facts (test evidence)

- `TestRecoverer_ClientAbort`: after a 200 response plus one chunk, the abort produces exactly one WARN `client_disconnected` and zero `panic recovered`. The recorder still shows 200 and no envelope. The access line has status 200, `aborted=true`, and bytes equal to the chunk length. ErrAbortHandler is re-panicked.
- `TestRecoverer_ClientAbortBeforeHeaders`: the body stays empty (no 500 written), the WARN is present, and `aborted=true`.
- `TestRecoverer_OrdinaryPanic`: behaviour is unchanged: ERROR `panic recovered`, a 500 with `internal_error`, an access status of 500, and no `aborted`.
- `TestStatusWriter_FirstWriteHeaderWins`: the first WriteHeader wins, and so does the implicit 200 from Write.
- **The plan's HYPOTHESIS is now a FACT** (`TestRecoverer_ClientAbortOverRealServer`, httptest server, run 3 times): the client gets 200 plus the partial chunk, then `io.ReadAll` returns `unexpected EOF`, so the body is truncated and the connection aborted. The server's ErrorLog contains no "panic". Before the fix the same test showed the stream ending cleanly with the 500 envelope appended after the partial chunk.
- `TestToolCallTerminalGuard_AbortNotSentToSentry`: the abort produces 0 Sentry events, the `tool_call_partial_stream` frame is still written, and ErrAbortHandler is re-panicked. Before the fix the test recorded 1 event.
- `TestToolCallTerminalGuard_OtherPanicStillSentToSentry`: a "boom" panic produces at least 1 event and is re-panicked.

## Gates (gateway/)

- `gofmt -l .`: empty
- `go build ./...`: OK
- `go vet ./...`: OK
- `go vet -tags integration ./...`: OK
- `go test ./... -count=1`: all packages ok

## Deviations from Plan

- To arm the flag, the abort test uses `tci.flags.set(reqID, flag)` (the same call `Intercept` makes) instead of `tci.Flag(reqID).Store(true)`. `Flag` returns nil when no flag has been installed, so the planned call would have hit a nil pointer.
- I added a 5th test, `TestRecoverer_ClientAbortOverRealServer`, which the orchestrator had requested to resolve the hypothesis.

## Notes

- The `clickup-link-enforce.sh` hook warned on every edit because this worktree has no active ClickUp link. The plan's card is 86akt86dc, and linking is the orchestrator's job.
- Nothing was pushed or deployed, and ROADMAP was not touched.

## Self-Check: PASSED
- Files present: recoverer.go, logger.go, recoverer_test.go, toolcall.go, toolcall_abort_test.go
- Commits present: cd91c68, 65bdb23
