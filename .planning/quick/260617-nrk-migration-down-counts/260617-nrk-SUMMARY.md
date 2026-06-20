---
quick_id: 260617-nrk
slug: migration-down-counts
date: 2026-06-17
status: complete
commit: c3655f7
---

# Quick 260617-nrk SUMMARY

## What
Fixed 3 migration integration tests that blocked `build-gateway` on `main`,
which in turn blocked the `:main` image build carrying the
`PRIMARY_POD_SERVE_STT` flag.

## Root cause
Migration `0030_probe_status_allow_config.sql` added a new HEAD migration.
`db.Down(n)` steps `n` migrations down from current HEAD. The 0026/0029
round-trip tests hard-code `n` relative to the prior HEAD (0029), so each Down
walk reverted 0030 (row-neutral) instead of reaching its intended boundary:

- `Migration0026_UpDownUp` — first `Down(2)` → `Down(3)`
- `Migration0026_DownAbortsOnDuplicateAliases` — `Down(4)` → `Down(5)`
- `Migration0029_Down_Symmetric` — `Down(1)` → `Down(2)`

0030's Down only swaps a CHECK constraint on `upstreams` (no model_aliases /
STT row changes), so the +1 shift is clean and row-count assertions hold.

## Verify
`go test -tags integration ./internal/integration_test/ -run 'Migration0026|Migration0029'`
→ 7/7 PASS (7.15s) on testcontainers Postgres.

## Commit
- `c3655f7` fix(test): bump migration Down step counts +1 for 0030 HEAD shift

## Follow-on (ops, this session)
- merge develop→main (`30f4c81`) → build-gateway → deploy prod gateway
- confirm prod rev ≠ d0f1f6b → STT routes to gemini (~2-3s vs pod CPU ~17s)
