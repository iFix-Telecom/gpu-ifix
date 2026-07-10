# Phase 20 — Deferred / out-of-scope items

## [20-04 discovery] Migration 0033 Down fails: 2BP01 dependency (owner: 20-01)

**Found during:** 20-04 Task 5 (full `-tags integration` gate).
**Not caused by 20-04** — 20-04 touched zero migration/SQL files (`git diff --name-only <base> HEAD` = reconciler.go, reconciler_test.go, primary_helpers_test.go only). Migration `0033_pod_config_coldstart_fastfail_budgets.sql` is commit `4f6e958` (20-01), an ancestor of the 20-04 base `d6a4d6e`. 20-01's SUMMARY states no live-DB `goose up`/`down` was ever run, so this surfaced only now.

**Symptom:** `db.Down` of 0033 →
`ERROR: cannot drop column created_budget_s of table pod_config because other objects depend on it (SQLSTATE 2BP01)`.

**Root cause:** the 0033 **Up** trigger `pod_config_update_notify` WHEN-clause references the 6 new columns (`created_budget_s`, `progress_stall_budget_s`, + 4 bounds). The 0033 **Down** runs `ALTER TABLE ... DROP COLUMN created_budget_s ...` BEFORE `DROP TRIGGER pod_config_update_notify`, so the still-live trigger depends on the column being dropped → Postgres refuses.

**Fix (one-line reorder, 20-01 owns):** in the 0033 Down, `DROP TRIGGER IF EXISTS pod_config_update_notify ...` FIRST, then `ALTER TABLE ... DROP COLUMN ...`, then recreate the pre-0033 trigger. (The Up already `DROP TRIGGER`+`CREATE TRIGGER`; the Down just has the two steps in the wrong order.)

**Cascade:** this failed Down leaves the goose migration/DB in a mixed state, which then fails the 0029 migration tests (`no rows` / `scan NULL`) and the throughput/FSM load tests (`TestSensitiveSaturated503`, `TestDCGMFailOpen`, `TestSC1/SC3/SC4`, `TestTier1UnavailableShedded503`) that were also running under concurrent machine load during the 20-04 run. None touch 20-04's code (primary reconciler). 20-04's own integration subset `go test -tags integration -run Primary ./internal/integration_test/` passes clean (exit 0, 33s).
