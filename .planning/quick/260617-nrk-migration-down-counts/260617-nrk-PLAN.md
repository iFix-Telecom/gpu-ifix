---
quick_id: 260617-nrk
slug: migration-down-counts
date: 2026-06-17
status: complete
---

# Quick 260617-nrk: Fix migration integration tests broken by 0030 HEAD shift

## Problem
`build-gateway` on `main` failed 3 integration tests, blocking the `:main`
image build → `PRIMARY_POD_SERVE_STT` flag couldn't deploy to prod.

```
FAIL TestIntegration_Migration0026_UpDownUp
FAIL TestIntegration_Migration0026_DownAbortsOnDuplicateAliases
FAIL TestIntegration_Migration0029_Down_Symmetric
```

## Root cause
Migration `0030_probe_status_allow_config.sql` (dbc73ce) added a new HEAD
migration. The three tests hard-code `db.Down(n)` step counts relative to the
previous HEAD (0029). `db.Down(n)` peels `n` migrations from current HEAD, so
each count was off by one — the Down walk reverted 0030 instead of reaching the
target migration boundary. NOT the STT flag, NOT a 0030 down-symmetry bug.
0030's Down is row-neutral (constraint swap on `upstreams`, no model_aliases /
STT row changes), so the counts shift cleanly by +1.

## Tasks
1. `migration_0029_test.go` — `Down_Symmetric`: `Down(1)` → `Down(2)`.
2. `migration_0026_test.go` — `UpDownUp`: first `Down(2)` → `Down(3)`.
3. `migration_0026_test.go` — `DownAbortsOnDuplicateAliases`: `Down(4)` → `Down(5)`.

## Verify
`sudo env PATH=/usr/local/go/bin:$PATH ... go test -tags integration ./internal/integration_test/ -run 'Migration0026|Migration0029' -count=1 -v`
→ all PASS.

## Done
All 7 migration 0026/0029 tests green locally (7.15s). Merge develop→main →
build-gateway green → deploy prod gateway → STT routes to gemini.
