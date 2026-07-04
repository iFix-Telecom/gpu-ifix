---
phase: 19-gateway-consolidation-worker-vm
plan: 04
subsystem: infra/db-migration
tags: [postgres, atomic-migration, tenants, api_keys, admin_keys, hash-verbatim, bd_ai_gateway, RES-08, auth-preservation]
requires:
  - "19-02: consolidated gateway (stack 38) live on worker-vm reading bd_ai_gateway"
  - "19-03: model_aliases reconciled additively into bd_ai_gateway (10→19) so merged traffic resolves"
provides:
  - "bd_ai_gateway now carries the 18 PROD tenants + 19 PROD api_keys (key_hash + key_lookup_hash byte-for-byte identical to _prod) + 2 PROD admin_keys + 31 current-period usage_counters"
  - "The ~18 real client apps authenticate against the consolidated worker-vm gateway WITHOUT key regeneration — proven live pre-cutover"
  - "admin_keys purged to prod-only (approved adjustment): exactly 2 rows (prod-ops-2026-05-26 active + bootstrap-random revoked); the 5 dev admin keys incl. 19-02-validation removed"
affects:
  - "19-05 (cutover — the DB target now holds the prod tenant/key set; cutover is a pure edge-Traefik ingress flip, no key work)"
  - "19-06 (decommission — bd_ai_gateway_prod retained as archive; masked ops timers restored here)"
tech-stack:
  added: []
  patterns:
    - "Cross-DB migration via transient staging schema in the TARGET (ai_gateway_migration.*) — Postgres cannot INSERT…SELECT across databases, so prod rows were pre-loaded into the target DB then moved atomically"
    - "Single-invocation atomic tx: one `psql -v ON_ERROR_STOP=1 -f migrate.sql` with BEGIN…COMMIT + in-tx DO-block RAISE-EXCEPTION guards (count + byte-for-byte hash JOIN) → full ROLLBACK on any mismatch"
    - "NO-ACTION FK pre-delete: billing_events (partitioned parent + 3 partitions, all confdeltype='a') dev rows deleted in-tx BEFORE the tenant DELETE — a DELETE on the partitioned parent propagates to all partitions in one statement"
    - "BYTEA hash fidelity: CSV COPY with bytea_output=hex on both ends + in-tx hash JOIN proof (not trusting transport)"
    - "Sensitivity gate keys off api_keys.data_class (NOT tenants.data_class): sensitive keys → 503 upstream_unavailable_for_sensitive_tenant when primary breaker open (RES-08, auth-OK)"
key-files:
  created:
    - ".planning/phases/19-gateway-consolidation-worker-vm/19-04-SUMMARY.md"
  modified: []
  infra:
    - "bd_ai_gateway: ai_gateway.tenants 4→18 (dev deleted, prod inserted) — MIGRATED"
    - "bd_ai_gateway: ai_gateway.api_keys 22→19 (dev CASCADE-deleted, 19 prod hash-verbatim) — MIGRATED"
    - "bd_ai_gateway: ai_gateway.admin_keys 5→2 (all 5 dev purged, 2 prod inserted) — MIGRATED (approved adjustment)"
    - "bd_ai_gateway: ai_gateway.usage_counters 10→31 (dev CASCADE-deleted, prod current-period) — MIGRATED"
    - "bd_ai_gateway: ai_gateway.billing_events 928→0 (dev rows pre-deleted in-tx; prod history NOT copied) — MIGRATED"
    - "bd_ai_gateway: ai_gateway_migration.* transient staging schema — DROPPED post-COMMIT"
    - "ops-claude:~/gw-migration-19/ root-600 backups (both DBs + affected target tables + migrate.sql) — RETAINED; CSVs shredded post-COMMIT"
    - "bd_ai_gateway_prod: SOURCE — READ-ONLY, UNTOUCHED (still 18 tenants / 33k billing archive)"
decisions:
  - "APPROVED ADJUSTMENT (checkpoint resolution): purge ALL dev admin_keys so admin_keys ends at exactly 2 (prod only). Implemented as a scoped in-tx DELETE (NOT EXISTS in staging prod set) + the plan's colliding-hash DELETE + an in-tx guard `admin_total<>2 → RAISE`. The 5 dev admin keys (incl. 19-02-validation ifix_admin_****eb79) removed; prod-ops-2026-05-26 (ifix_admin_****613f) verified present + active."
  - "data_class copied VERBATIM per checkpoint instruction — no override of telefonia/cobrancas. tenants.data_class is all 'normal' (18/18) in _prod, but api_keys.data_class carries the sensitivity (telefonia+cobrancas = sensitive) → those keys correctly 503 (RES-08) while normal keys 200. This is auth-preserved behavior, proven distinct from a 401 via a bogus-key negative control."
  - "billing_events PROD history (33k rows) intentionally NOT migrated — stays archived in bd_ai_gateway_prod (research A5). Only the 928 DEV target rows removed in-tx to satisfy the NO-ACTION FK."
  - "FK-enumeration at execution time returned billing_events PARENT + 3 partitions (202605/06/07), all NO ACTION — broader than the pre-checkpoint record (parent only). Covered by the single parent DELETE (propagates to partitions); no extra pre-delete lines needed."
  - "SOURCE ∩ TARGET columns were IDENTICAL for all 4 tables (both at goose v31) → shared column list = full column set, zero target-only columns to DEFAULT."
metrics:
  duration: "~25m (continuation post-checkpoint-approval)"
  completed: "2026-07-04"
  tasks: 2
  files_created: 1
---

# Phase 19 Plan 04: Atomic PROD tenant/key migration → bd_ai_gateway Summary

Migrated the full production tenant/key set from `bd_ai_gateway_prod` into `bd_ai_gateway` in **one atomic, FK-safe, hash-verbatim transaction** so the ~18 real client apps authenticate against the consolidated worker-vm gateway with **no key regeneration**. Executed as a CONTINUATION after the BLOCKING human-verify checkpoint was **approved with one adjustment** (purge dev admin_keys to prod-only). The irreversible DB step: dev tenants + their FK children deleted, prod rows loaded, byte-for-byte hash identity proven in-transaction, and every tested prod key proven live pre-cutover. `bd_ai_gateway_prod` stayed read-only/untouched.

## What Was Done

### Pre-mutation verification (state preserved from the halted agent)
Confirmed intact before touching live tables: root-600 backups in `~/gw-migration-19/` (main pre-dump `bd_ai_gateway.pre.20260704-1426.dump`, path in `.backup_main_path`), the staging schema `ai_gateway_migration.*` (tenants=18, api_keys=19, admin_keys=2, usage_counters=31), the `dev_tenants_to_delete` snapshot (4 rows), the DSNs (`.dsn.env`, mode 600), and the two masked ops timers (`gateway-price-sync.timer`, `prod-primary-report.timer`).

### Fresh preflight (re-run at execution)
- **Slug collision:** 1 (`converseai` — dev 7eb14066 vs prod) → resolved DELETE-dev-first, prod UUID wins.
- **Tenant-id / api_keys-hash / admin_keys-hash collisions:** 0 / 0 / 0.
- **Orphan risk (usage_counters, api_keys):** 0 / 0.
- **FK non-CASCADE children of tenants:** `billing_events` (partitioned parent) + `billing_events_202605/202606/202607`, all `confdeltype='a'` (NO ACTION). Single parent DELETE covers all partitions.
- **data_class (staging tenants):** all `normal` (18/18), copied verbatim.
- **Shared columns:** SRC == DST for all 4 tables (both goose v31) → full column list, no target-only DEFAULT columns.

### Task 2 — the atomic migrate.sql (`~/gw-migration-19/migrate.sql`, root-600)
One `psql -v ON_ERROR_STOP=1 -f migrate.sql`, single BEGIN…COMMIT. Statement results:
```
DELETE 928   -- dev billing_events pre-deleted (NO-ACTION FK, all partitions)
DELETE 0     -- colliding dev admin hashes (none)
DELETE 5     -- APPROVED ADJUSTMENT: all 5 dev admin_keys purged
DELETE 4     -- dev tenants (CASCADE: 22 api_keys + 10 usage_counters + voices)
INSERT 0 18  -- prod tenants
INSERT 0 19  -- prod api_keys (orphan-guarded)
INSERT 0 2   -- prod admin_keys
INSERT 0 31  -- prod current-period usage_counters (orphan-guarded)
NOTICE: migration guards passed: tenants=18, api_keys=19, api_hash_match=19, admin_hash_match=2, admin_total=2
COMMIT
```
In-tx guards (RAISE EXCEPTION → full ROLLBACK on any mismatch): tenants=18, api_keys=19, **api_key hash-verbatim JOIN=19** (key_hash + key_lookup_hash byte-for-byte), admin hash-verbatim=2, **admin_total=2** (the approved-adjustment guard). Then `DROP SCHEMA ai_gateway_migration CASCADE` (separate statement post-COMMIT).

### Final live state (bd_ai_gateway)
| table | before | after |
|-------|--------|-------|
| tenants | 4 (dev) | **18** (prod) |
| api_keys | 22 (dev) | **19** (prod, hash-verbatim) |
| admin_keys | 5 (dev) | **2** (prod only) |
| usage_counters | 10 | **31** (prod current-period) |
| billing_events | 928 (dev) | **0** (prod history stays in _prod) |

Prod admin key `ifix_admin_****613f` (**prod-ops-2026-05-26, active**) present. All 18 prod slugs live (converseai, chat-ifix, telefonia, cobrancas, ia-kanban, campanhas, voice-api, converseai-*, transcricao-voip, analise-transcr-voip, uat10-test, claude-wpp, hermes).

## Auth Proofs (live worker-vm gateway, internal Traefik `Host: ai-gateway.converse-ai.app` → 10.10.10.50:80, pre-cutover)

| # | Key | Endpoint | Result | Meaning |
|---|-----|----------|--------|---------|
| 1 | chat-ifix `ifix_sk_****lo4w` (normal) | POST /v1/chat/completions | **HTTP 200** → openrouter/deepseek-v4-flash (Novita) tier-1 + fresh `billing_events` row (route=chat, upstream=openrouter-chat) | normal key routes + meters |
| 2 | prod admin `ifix_admin_****613f` | GET /admin/metrics | **HTTP 200** (fsm healthy) | admin ops intact |
| 3 | telefonia `ifix_sk_****zbnj` (sensitive) | POST /v1/chat/completions | **HTTP 503** `upstream_unavailable_for_sensitive_tenant` | RES-08 auth-OK (primary breaker open, sensitive can't route external) |
| 4 | cobrancas `ifix_sk_****ef5p` (sensitive) | POST /v1/chat/completions | **HTTP 503** `upstream_unavailable_for_sensitive_tenant` | RES-08 auth-OK |
| — | bogus key | POST /v1/chat/completions | **HTTP 401** | negative control — proves the 503s are auth-PRESERVED, not auth-failures |

Key finding: the gateway's sensitivity gate keys off **`api_keys.data_class`** (telefonia + cobrancas = `sensitive`), NOT `tenants.data_class` (all `normal`). So the plan's original "sensitive → 503" proof DID apply — the checkpoint note's "no 503 expected" was based on tenant-level class; the migrated key-level class produced the correct RES-08 503, further confirming hash-verbatim keys carry their full authorization semantics.

## Deviations from Plan

### Approved adjustment (checkpoint resolution)
**[Rule 4 - Approved] Purge dev admin_keys to prod-only.** Beyond the plan's INSERT of 2 prod admin_keys, added a scoped in-tx `DELETE FROM admin_keys WHERE NOT EXISTS (… staging prod set)` (removed 5 dev rows incl. `19-02-validation`) + an `admin_total<>2 → RAISE` guard. Final admin_keys = exactly 2 (prod). Approved by the user at the blocking checkpoint.

### Auto-adjustments (Rule 3 — matched to live reality)
**[Rule 3] FK-enumeration returned partitions.** At execution the NO-ACTION child list was `billing_events` + 3 monthly partitions (not just the parent as pre-recorded). No migrate.sql change needed — the parent DELETE propagates to partitions. Documented.

**[Rule 3] Sensitive-503 auth proof reinstated.** The checkpoint note predicted "no 503 (all tenants normal)". Live behavior gave the correct sensitive-503 for telefonia/cobrancas because sensitivity lives on `api_keys.data_class`. Verified this is auth-OK (not 401) via a bogus-key control. Not a defect — the plan's original expectation held.

## Backups & Retention (for 19-05 / 19-06)
- Root-600 dir `ops-claude:~/gw-migration-19/` (mode 700). Main rollback dump: `bd_ai_gateway.pre.20260704-1426.dump` (path in `.backup_main_path`). Also: `bd_ai_gateway_prod.source.20260704-1426.dump`, `dst-affected-tables.pre.20260704-1428.dump`, `migrate.sql`.
- **CSVs shredded** post-COMMIT (`shred -u tenants/api_keys/admin_keys/usage_counters.csv` — they carried key hashes; T-19-04 retention).
- **Dumps retained 14 days** for rollback; shred after 19-06 soak passes.
- **Masked timers left masked** (`.masked_timers`: `gateway-price-sync.timer`, `prod-primary-report.timer`) — 19-06 unmasks + repoints them.
- Rollback path if needed: restore from the root-600 dumps + `ssh worker-vm docker service update --force ai-gateway-prod_gateway` to reload.

## Threat Flags
None — no new security surface introduced. Migration touched only tenant/key state in the target DB; source DB read-only; secrets confined to root-600 env/dumps (never in .planning/commits).

## Self-Check: PASSED
- 19-04-SUMMARY.md exists ✓
- ops-claude:~/gw-migration-19/migrate.sql + main backup dump exist ✓
- Live bd_ai_gateway: tenants=18, api_keys=19, admin_keys=2 (authoritative counts re-queried post-COMMIT) ✓
- Auth proofs recorded (chat-ifix 200 + billing row, admin 200, telefonia/cobrancas correct-503, bogus 401) ✓
