---
status: complete
quick_id: 260527-wgs
slug: fix-primary-reconciler-silent-terminal
date: 2026-05-27
commit: 01e7558
files_modified:
  - gateway/internal/primary/reconciler.go
  - gateway/internal/primary/reconciler_test.go
related:
  - .planning/debug/primary-reconciler-silent-hang.md
blocks_unblocked:
  - Phase 11 plan 11-06 live load-test UAT
  - Phase 11 plan 11-07 chaos primary kill live UAT
---

# 260527-wgs — primary reconciler tolerates transient ErrInstanceNotFound

## Fix shipped

`waitForReadyOrDestroy` ErrInstanceNotFound branch now requires **3 consecutive
observations** before closing — mirrors the existing IsTerminal 3-strike pattern
in the same function. Adds `BestEffortDestroy` on confirmed-terminal to prevent
orphan Vast pods burning $$.

## Files modified

| Path | Change |
|------|--------|
| `gateway/internal/primary/reconciler.go` | Added `notFoundStrikes` local counter alongside existing `terminalStrikes`. ErrInstanceNotFound branch increments + emits `log.Warn` per strike (lifecycle_id, vast_instance_id, strike_count, error_class). Fires `vastutil.BestEffortDestroy` + `closeLifecycle(..., "instance_terminal_state_confirmed", 0)` only after `notFoundStrikes >= terminalConfirmStrikes (3)`. Resets on healthy poll OR different error class. |
| `gateway/internal/primary/reconciler_test.go` | `waitForReadyOrDestroyForTest` seam mirrored for parity. Added `TestEvaluateProvisioning_TolerantOfTransientInstancesNullFlap` with 2 subtests: (a) single null + running → Ready, 0 destroys; (b) 3 consecutive null → BestEffortDestroy fires once + close reason `instance_terminal_state_confirmed`. |

## Validation gates (all PASS)

| Gate | Result |
|------|--------|
| `go vet ./internal/primary/...` | PASS |
| `go vet ./...` | PASS (no regression unrelated packages) |
| `go build ./cmd/gateway/` | PASS |
| `go build ./cmd/gatewayctl/` | PASS |
| `go test ./internal/primary/...` | PASS (all existing + 2 new subtests, 8.685s) |

## Key decisions

1. **Separate counter `notFoundStrikes`** (not shared with `terminalStrikes`) — keeps failure modes orthogonal; both reset on healthy poll.
2. **Distinct `shutdown_reason='instance_terminal_state_confirmed'`** — forensic queries can distinguish 3-strike ErrInstanceNotFound close from IsTerminal close.
3. **Mirror in `waitForReadyOrDestroyForTest` seam** — regression test exercises production logic shape.

## Commit

```
01e7558 fix(11): primary reconciler tolerates transient ErrInstanceNotFound (3-strike confirm)
```

2 files changed, 174 insertions(+), 5 deletions(-).

## Out of scope (carry-forward Phase 11 open items)

- `cmd/gateway/main.go:870-871` mirror callback writes "" for 3 of 5 fields on every transition (separate UX gap).
- Audit pipeline silent since 2026-05-25 (separate root cause; not related to reconciler).

## Cross-references

- **Authoritative debug session:** `.planning/debug/primary-reconciler-silent-hang.md` (status: root_cause_found → now resolved)
- **Branch:** `gsd/phase-06.9-close`
