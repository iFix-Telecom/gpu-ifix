---
phase: quick-260614-ny5
plan: 01
subsystem: dashboard-auth-middleware
tags: [auth, middleware, edge, ux-fix]
requires: []
provides:
  - "No-session redirect to clean /login (no session_expired param)"
affects:
  - dashboard/src/middleware.ts
  - dashboard/src/middleware.test.ts
tech-stack:
  added: []
  patterns:
    - "Edge middleware cannot distinguish never-logged-in from expired session"
key-files:
  created: []
  modified:
    - dashboard/src/middleware.ts
    - dashboard/src/middleware.test.ts
decisions:
  - "Drop ?session_expired=1 from the Edge no-session redirect; only a client-side flow may set the param. login/page.tsx banner code left untouched so a future flow can still trigger it."
metrics:
  duration: ~6m
  completed: 2026-06-14
requirements: [QUICK-NY5]
---

# Phase quick-260614-ny5 Plan 01: Middleware Redirect to Clean /login Summary

Edge dashboard middleware now redirects unauthenticated visitors to a clean `/login` instead of `/login?session_expired=1`, fixing the false "Sessão encerrada por inatividade" banner that appeared on every first visit.

## What Was Built

The Edge runtime sees only the presence or absence of a session cookie — it cannot tell "never logged in" from "session genuinely expired." Appending `?session_expired=1` on every no-session redirect therefore mislabeled first visits as expired sessions. Task 1 removed the param from the middleware redirect and synced both the file-header decision-tree comment and the inline Stage 1 comment to describe the clean target.

### Changes

- `dashboard/src/middleware.ts`
  - Stage 1 no-session branch: replaced the four-line `new URL` + `searchParams.set("session_expired", "1")` + redirect with a single `return NextResponse.redirect(new URL("/login", req.url));`. Surrounding `if (!claims.hasSession) { ... }` guard preserved.
  - Inline Stage 1 comment rewritten to explain why the param must not be appended at the Edge.
  - File-header decision-tree line 1 changed from `→ redirect to /login?session_expired=1` to `→ redirect to /login`.
  - No `searchParams` reference remains in the file (verified `grep -n searchParams` returns nothing).
- `dashboard/src/middleware.test.ts`
  - Case (a) title: `(a) cookie absent → /login (no session_expired param)`.
  - Case (a) assertion flipped from `expect(loc).toContain("session_expired=1")` to `expect(loc).not.toContain("session_expired")`; `expect(loc).toContain("/login")` kept.
  - Header comment list item `a.` updated to `→ /login (clean, no param)`.

### Untouched (per locked decision)

- `dashboard/src/app/login/page.tsx` — the `session_expired` read (line 69) and banner render remain verbatim, so a future client-side flow can still surface the banner intentionally.

## Verification

- `cd dashboard && bunx tsc --noEmit` → exit 0, no type errors.
- `cd dashboard && bun run test src/middleware.test.ts` → 7/7 tests pass, including the updated case (a).
- `grep -n searchParams dashboard/src/middleware.ts` → no matches (redirect param-builder fully removed).
- `grep -n 'NextResponse.redirect(new URL("/login", req.url))' dashboard/src/middleware.ts` → matches at line 99 (artifact `contains` pattern satisfied).
- `grep -n session_expired dashboard/src/app/login/page.tsx` → still returns read + banner doc lines (login page proven untouched).

Note: `grep session_expired dashboard/src/middleware.ts` still returns one line — the explanatory word inside the new Stage 1 comment ("doing so falsely shows..."). This is prose explaining the fix, not the `?session_expired=1` redirect param. The plan's literal "returns nothing" verification phrasing did not anticipate the explanatory comment; the binding `done` intent (no redirect appends the param) is fully met — no code path constructs or sets the param.

## Deviations from Plan

None — plan executed as written. (The single remaining `session_expired` substring is documentation prose inside the new comment, not a deviation in behavior.)

## Commits

- 3508773: fix(quick-260614-ny5-01): redirect no-session to clean /login (no session_expired param)

## Self-Check: PASSED

- FOUND: dashboard/src/middleware.ts (modified, committed)
- FOUND: dashboard/src/middleware.test.ts (modified, committed)
- FOUND: commit 3508773
