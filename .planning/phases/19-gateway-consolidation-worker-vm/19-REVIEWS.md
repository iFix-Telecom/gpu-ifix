---
phase: 19
reviewers: [codex]
reviewed_at: 2026-07-03
plans_reviewed: [19-01, 19-02, 19-03, 19-04, 19-05, 19-06]
note: "gemini skipped — GEMINI_API_KEY not set in env; claude skipped (self). Codex (gpt) sole reviewer."
---

# Cross-AI Plan Review — Phase 19

## Codex Review

## Phase 19 Overall

**Summary**  
The phase is generally strong: it separates foundation, parallel deployment, DB migration, ingress cutover, and decommission into sensible waves with explicit checkpoints before irreversible operations. The plan correctly identifies the two main failure domains: preserving production authentication data exactly, and keeping the Traefik cutover reversible until soak completes. The biggest remaining risk is that some DB migration mechanics are underspecified or internally inconsistent, especially around transactional loading, COPY filtering, collision handling, and replacing dev data in a live target DB.

**Strengths**
- Clear public cutover seam: edge Traefik server URL, not DNS.
- Good rollback posture before decommission: old prod gateway remains alive through soak.
- Explicit embed migration gate before tearing down `n8n-ia-vm`.
- Correct emphasis on preserving `key_hash` and `key_lookup_hash` verbatim.
- Good recognition of Portainer agent/git-stack bind-mount problems.
- Blocks irreversible DB migration and decommission behind human checkpoints.
- Separates prod traffic cutover from DB migration, reducing blast radius.

**Concerns**
- **HIGH:** The DB restore sequence is not actually one atomic transaction as written. It suggests opening `BEGIN` in one `psql` session, then running `psql -1 -f /tmp/gw-core.sql` in another session. That does not combine the delete and COPY into one transaction.
- **HIGH:** `pg_dump --table=usage_counters` cannot directly “restrict to current-period rows” without a custom dump/query path. The plan says to filter if large, but the command shown dumps the full table.
- **HIGH:** DELETE-dev-first needs a precise rule. “Delete all dev tenants” may delete target-only data needed for validation, dashboard login, admin state, or aliases unless explicitly scoped and backed up.
- **HIGH:** `admin_keys` collision behavior is underplanned. Because `admin_keys` is independent, restoring prod rows into a target containing dev rows may hit unique constraints. The plan should precompute collisions and either truncate/replace admin keys or preserve both intentionally.
- **MEDIUM:** The phase claims “zero client breakage,” but sensitive tenants may legitimately receive 503 if local primary is unavailable. That may be acceptable, but it is not zero breakage unless current prod already behaves the same way.
- **MEDIUM:** The `172.18.0.1:18000` bridge-gateway assumption conflicts with swarm overlay behavior. It is acknowledged, but the plan leaves the final runtime choice unresolved.
- **MEDIUM:** Billing history being archived in `_prod` may break dashboards or `/economia` expectations if users expect continuity.
- **MEDIUM:** Redis is treated as ephemeral, but rate limits/quota/FSM behavior may change immediately after cutover unless the app truly rebuilds all durable state from Postgres.
- **LOW:** The plan uses real-key smoke tests, but does not define how to avoid billable or customer-visible side effects beyond small prompts.
- **LOW:** Secrets handling is mostly good, but Portainer env arrays still store secrets in Portainer. That should be accepted explicitly as the operational model.

**Suggestions**
- Build a single SQL migration script for 19-04 that performs delete + load + verification in one transaction, or use staging tables plus `INSERT ... SELECT`.
- Prefer staging tables over direct `pg_dump | psql` into live tables. Load prod rows into `ai_gateway_migration.*`, validate counts/hashes/collisions, then swap/insert transactionally.
- Before DB mutation, compute and record:
  - duplicate tenant slugs
  - duplicate tenant IDs
  - duplicate `api_keys.key_lookup_hash`
  - duplicate `admin_keys.key_lookup_hash`
  - orphan risks for `usage_counters`
- Add a hash-verbatim verification query comparing source and target bytea values after migration.
- Define whether target dev tenants survive. If not, explicitly say target tenant/admin/dev data is intentionally discarded.
- Add a “current prod behavior parity” check before claiming client safety: same representative prod keys against old gateway and new gateway, compare status codes and response shape.
- Make `UPSTREAM_LLM/STT/HEALTH` final before cutover: either use `10.10.10.50:18000`, `host.docker.internal` equivalent, host networking, or document that tier-0 is intentionally unreachable.
- Add explicit public smoke for embeddings and rerank, not only chat/dashboard.
- Define soak duration and abort thresholds: edge 5xx rate, gateway 401 spike, billing write failures, Redis errors, dashboard login failures.

**Risk Assessment: HIGH**  
The architecture is sound, but the DB migration plan contains enough transactional and collision ambiguity that execution could partially mutate the target or silently lose expected target state. Once that is tightened, the overall phase drops closer to MEDIUM.

---

## 19-01 — Worker-VM Foundation

**Summary**  
This is a good non-disruptive foundation wave. It validates the two live assumptions that later plans depend on: Portainer swarm compose-string deployment and internal Traefik routing. Creating the shared overlay and proving the Redis alias pattern early is the right ordering.

**Strengths**
- Non-prod-impacting and safe to run before any migration.
- Correctly validates Portainer API behavior instead of assuming route shape.
- Correctly verifies Traefik provider, entrypoint, labels, and network.
- Proves `infra-redis-1` DNS behavior before the gateway relies on it.
- Captures the exact label scheme for later stacks.

**Concerns**
- **MEDIUM:** Attaching `traefik-internal_traefik` to a new overlay may affect routing if the service has network assumptions or labels using another network.
- **MEDIUM:** Testing Redis alias with a throwaway service proves DNS, but not the final stack’s service name/network alias behavior.
- **LOW:** Probing Portainer create routes with invalid bodies can create noisy audit logs or ambiguous errors.
- **LOW:** The plan does not specify cleanup verification for the throwaway Redis service beyond “removed.”

**Suggestions**
- Record pre/post `docker service inspect traefik-internal_traefik` network attachments.
- If Traefik must be attached to `ai-gateway`, do that in a reversible command and verify existing routes still work.
- Add a final check that no throwaway Redis service remains.
- Confirm whether `ai-gateway` overlay should be `--attachable`; useful for debug containers, but document that this is intentional.

**Risk Assessment: LOW**  
Mostly discovery and additive infrastructure. Main risk is accidentally changing Traefik networking in a way that affects existing worker-vm services.

---

## 19-02 — Gateway + Embed Deployment

**Summary**  
This wave correctly deploys the consolidated gateway and embed stack in parallel before touching public traffic. It also correctly removes the dangerous dependency on `10.10.10.20:7997` before decommission. The main issue is that some runtime connectivity assumptions remain unresolved, especially host/bridge access for `172.18.0.1:18000`.

**Strengths**
- Parallel deployment avoids client impact.
- Keeps `MIGRATE_ON_BOOT=false`, which is correct for a known migrated DB.
- Moves embed before old host teardown.
- Uses Portainer compose-string and env array, matching the UI-editable requirement.
- Validates chat, embeddings, admin, health, and billing writes before production key migration.
- Includes force-update behavior for image pull gotchas.

**Concerns**
- **HIGH:** `UPSTREAM_EMBED_URL=http://embed:7997` depends on the gateway and embed being on the same overlay and service DNS resolving as expected across separate Portainer stacks. This should be explicitly verified from inside the gateway task, not only from a debug container.
- **MEDIUM:** `172.18.0.1:18000` is acknowledged as potentially unreachable, but the stack may still repeatedly fail health/upstream checks or behave differently from dev.
- **MEDIUM:** Redis is introduced as a new service in the gateway stack. If an existing `redis_redis` was intended to be reused, this changes runtime state and may confuse operations.
- **MEDIUM:** CPU embed on a non-GPU VM may be functionally up but too slow under real load.
- **LOW:** Health checks are not described as Docker/Swarm healthchecks; service may be `1/1` but functionally unhealthy.
- **LOW:** The selected image tag `:main` may drift. Pinning a digest would make rollback/repro stronger.

**Suggestions**
- Add an exec/debug check from the gateway container/task network namespace to `http://embed:7997/health`.
- Add a latency check for embeddings under expected payload size.
- Decide explicitly: new Redis service vs aliasing existing Redis. If new, name the stack/service clearly and document it as the live gateway Redis.
- Pin gateway and embed images to immutable digests during cutover.
- Add app-level readiness checks before declaring Traefik route green.
- Resolve `172.18.0.1:18000` before cutover, even if by deciding “intentionally unreachable, tier-1 only.”

**Risk Assessment: MEDIUM**  
Safe from a public-traffic standpoint, but runtime parity and embed performance could surprise later if only shallow health checks pass.

---

## 19-03 — Dashboard, Rerank, Model Aliases

**Summary**  
This wave addresses two important breakage risks: dashboard availability and model alias drift. Consolidating rerank rather than dropping it is the safer default. The alias reconciliation is directionally right but needs a stricter data model and verification strategy.

**Strengths**
- Handles dashboard and rerank before decommission.
- Treats model aliases as client-facing API compatibility, not just config.
- Uses additive alias migration to reduce destructive DB changes.
- Keeps dashboard public cutover delayed until ingress cutover.
- Recognizes `pg_notify`/reload needs for running gateway.

**Concerns**
- **HIGH:** `model_aliases` may depend on `upstreams`, provider capabilities, routing policy, or data class. Additive alias copy can still produce runtime failures if target upstream config cannot serve the alias.
- **MEDIUM:** “Only port aliases that resolve to an upstream present in target” may omit aliases clients use, causing 404, or port aliases to semantically different upstreams.
- **MEDIUM:** Rerank port/health endpoint is unspecified.
- **MEDIUM:** Dashboard DB quirks are noted but not concretely defined. A wrong `search_path` or SSL mode can make the dashboard appear up but fail after login.
- **LOW:** Checking gateway logs for rerank consumers is weak; absence in recent logs does not prove no client dependency.

**Suggestions**
- Export source and target `model_aliases` plus referenced `upstreams` into a diff artifact.
- For every prod-only alias, classify it: port, map to existing target upstream, add upstream, or intentionally reject.
- Verify aliases with actual gateway calls using the alias string, not only DB presence.
- Include STT/transcription alias smoke if `whisper` is ported.
- For rerank, verify the public gateway route that clients use, not only the backend `/health`.
- For dashboard, test login/auth flow enough to prove DB connectivity, not just 200/302.

**Risk Assessment: MEDIUM**  
Good sequencing, but alias/upstream compatibility is a real client-breakage vector unless validated with representative model calls.

---

## 19-04 — DB Migration

**Summary**  
This is the critical wave and it correctly receives a blocking checkpoint. The plan has the right intent: migrate tenants, API keys, admin keys, and usage counters while preserving hashes and production UUIDs. However, the implementation details need tightening before execution. The current plan mixes `pg_dump`, manual deletes, and separate `psql` sessions in a way that may not be atomic and does not fully prove byte-for-byte correctness.

**Strengths**
- Correctly identifies FK order: `tenants` before `api_keys`/`usage_counters`; `admin_keys` independent.
- Correctly preserves prod tenant UUIDs so FK relationships remain valid.
- Correctly avoids key regeneration.
- Correctly backs up both source and target before mutation.
- Correctly leaves billing history archived unless continuity is required.
- Human checkpoint before destructive delete is appropriate.
- Tests real prod keys before public cutover.

**Concerns**
- **HIGH:** Delete and restore are not one transaction as written. `BEGIN` in one `psql` session and `psql -1 -f` in another do not share transaction state.
- **HIGH:** Direct `pg_dump --data-only` into tables with existing non-colliding dev data can fail on unique constraints or create mixed prod/dev tenant state.
- **HIGH:** `pg_dump` cannot filter current-period `usage_counters` with the shown command. If filtering is required, use `COPY (SELECT ...)`.
- **HIGH:** No explicit byte-for-byte verification query for `key_hash` and `key_lookup_hash`.
- **HIGH:** No explicit check for `api_keys.key_lookup_hash` collisions before load.
- **HIGH:** `billing_events` not copied means post-migration dashboards or reports may show discontinuity. This is called cosmetic, but that needs product confirmation.
- **MEDIUM:** If target `model_aliases` rows reference old dev tenant assumptions or dashboard state, deleting all dev tenants could have side effects.
- **MEDIUM:** `usage_counters` FK shape is assumed. If primary key includes period/date dimensions, copying source rows can conflict with target rows unless dev rows are removed precisely.
- **MEDIUM:** Backups in `/tmp` containing key hashes should have permissions and retention explicitly managed.
- **LOW:** “Every prod key authenticates” is stronger than the described test of 2-3 keys. Either test all 19 keys or adjust the claim.

**Suggestions**
- Replace direct restore with staging tables:
  ```sql
  BEGIN;
  CREATE SCHEMA IF NOT EXISTS ai_gateway_migration;
  -- load prod dump into staging tables
  -- validate counts, duplicates, hashes
  -- delete target rows intentionally
  -- insert into live tables in FK order
  COMMIT;
  ```
- If using dumps, create a single generated SQL file containing:
  - `BEGIN`
  - target deletes
  - COPY data
  - verification queries
  - `COMMIT`
- Add preflight queries:
  ```sql
  -- tenant slug collisions
  SELECT t.slug FROM target.tenants t JOIN source.tenants s USING (slug);

  -- API key hash collisions
  SELECT encode(t.key_lookup_hash,'hex')
  FROM target.api_keys t JOIN source.api_keys s USING (key_lookup_hash);

  -- admin key collisions
  SELECT encode(t.key_lookup_hash,'hex')
  FROM target.admin_keys t JOIN source.admin_keys s USING (key_lookup_hash);
  ```
- Add post-load hash verification:
  ```sql
  SELECT count(*)
  FROM src.api_keys s
  JOIN dst.api_keys d USING (id)
  WHERE s.key_hash = d.key_hash
    AND s.key_lookup_hash = d.key_lookup_hash;
  ```
  Expected: `19`.
- Test all 19 API keys if raw keys are available. If not, say “representative keys.”
- Define rollback as an actual restore command and whether services must be restarted after restore.
- Delete or chmod dumps after migration according to a retention decision.

**Risk Assessment: HIGH**  
This is the riskiest plan. The goal is correct, but the current mechanics need revision to guarantee atomicity, collision safety, and hash-verbatim proof.

---

## 19-05 — Cutover

**Summary**  
The cutover plan is strong. It uses the right reversible seam, leaves DNS untouched, keeps the old gateway alive, and validates public behavior with real keys. The main gap is rollback completeness after DB migration: rolling ingress back to old prod means old gateway writes to `_prod` while new writes already occurred in `bd_ai_gateway`, causing billing/state split.

**Strengths**
- Correctly avoids DNS/Cloudflare changes.
- One-line Traefik rollback is simple and fast.
- Blocks cutover behind a go/no-go checkpoint.
- Verifies public hostname, real keys, dashboard, and billing writes.
- Keeps `n8n-ia-vm` alive as rollback target.
- Backs up Traefik file before edit.

**Concerns**
- **HIGH:** Rollback restores traffic to old gateway using `bd_ai_gateway_prod`, but any successful post-cutover requests have already written billing/usage to `bd_ai_gateway`. The plan accepts brief split billing but does not define reconciliation.
- **MEDIUM:** If target DB migration deleted dev data and public rollback happens, the new DB may continue accumulating admin/dashboard/timer writes unless timers are controlled.
- **MEDIUM:** Dashboard and gateway may share or not share the same Traefik service. The plan says “if separate,” but this should be known before cutover.
- **MEDIUM:** Public smoke only after flip may be too late for Host routing issues. Preflight should use edge-to-worker path with Host header if possible.
- **LOW:** No explicit check that Cloudflare/proxy/cache behavior is not masking responses.
- **LOW:** No defined observation window before declaring cutover successful.

**Suggestions**
- Add rollback reconciliation instructions:
  - record cutover timestamp
  - if rollback occurs, compare `billing_events` and `usage_counters` between DBs for that window
  - decide whether to replay, ignore, or manually adjust
- Before edit, run an edge-host local curl to `10.10.10.50:80` with `Host: ai-gateway.converse-ai.app`.
- Confirm the exact dashboard route/service block before checkpoint.
- Add immediate post-cutover monitoring commands for:
  - Traefik 5xx
  - gateway 401/403 spike
  - DB insert errors
  - Redis errors
  - latency
- Define rollback trigger thresholds.

**Risk Assessment: MEDIUM**  
Ingress rollback is operationally simple, but data rollback is not complete. That is acceptable if the billing split is explicitly tolerated and reconcilable.

---

## 19-06 — Decommission

**Summary**  
This wave is appropriately gated because it removes the easy rollback target. It correctly includes embed dependency verification, ops timer repointing, old stack teardown, dev stack deletion, and archive retention. The main concern is that teardown is broad and partly irreversible, so the soak criteria and rebuild/rollback path need to be more concrete.

**Strengths**
- Blocking soak checkpoint before removing rollback target.
- Explicitly verifies embed is served from worker-vm before stopping old embed.
- Retains `bd_ai_gateway_prod` as archive.
- Repoints ops timers before teardown.
- Verifies public gateway after old stacks are stopped.
- Updates topology docs.

**Concerns**
- **HIGH:** After this wave, rollback becomes rebuild, but no rebuild procedure is captured.
- **HIGH:** Stopping old redis/gateway/embed/rerank may remove forensic evidence or runtime config needed for emergency restore unless configs/envs/images are archived first.
- **MEDIUM:** “Cutover stable for agreed soak window” does not define duration or metrics.
- **MEDIUM:** Deleting Portainer stack 34 removes dev fallback. That is probably intended, but should be confirmed after prod has run long enough.
- **MEDIUM:** Ops timers could trigger during migration/cutover and mutate the wrong DB unless disabled or audited earlier.
- **LOW:** `docker compose down` may remove networks but not volumes; volume retention/removal policy is not stated.
- **LOW:** CLAUDE.md update may include sensitive topology/secrets if not carefully edited.

**Suggestions**
- Before teardown, archive old compose files, env var names, image digests, and `docker inspect` output into a root-owned archive.
- Define soak minimum, for example:
  - 24 hours or one business cycle
  - no sustained 5xx spike
  - billing rows accumulating in `bd_ai_gateway`
  - representative tenants observed
  - embeddings and admin/dashboard confirmed
- Add a “disable timers during cutover” check earlier, or prove they are safe before 19-05.
- Keep old compose directories and env files on disk for a defined retention period even after `compose down`.
- Delete Portainer dev stack only after confirming no internal tooling still points at it.
- Document emergency rebuild steps or at least the source locations for old stacks.

**Risk Assessment: MEDIUM**  
The decommission sequencing is reasonable, but the rollback posture changes dramatically. It is safe only with a real soak window and retained old config artifacts.

---

## Cross-Cutting Recommendations

1. **Tighten DB migration first.**  
   This is the main blocker. Do not execute 19-04 until delete/load/verify is a single coherent transaction or staging-table workflow.

2. **Add byte-for-byte hash proof.**  
   Counts are not enough. Compare source and target `key_hash` and `key_lookup_hash` directly.

3. **Clarify “zero breakage.”**  
   If sensitive tenants can receive 503 due to tier-1/tier-0 policy, call that “auth preserved, behavior parity expected,” not zero breakage.

4. **Define rollback data reconciliation.**  
   Ingress rollback is easy; billing/quota rollback is not. Add a cutover timestamp and reconciliation plan.

5. **Make soak measurable.**  
   “Stable soak” should have duration and metrics before decommission.

6. **Pin images by digest for migration.**  
   Tags like `:main` and `:latest-dev` add uncertainty during an infra move.

7. **Pre-archive old runtime config.**  
   Before tearing down old stacks, capture compose files, env var names, image digests, service inspect output, and relevant logs.

8. **Verify public routes for all client surfaces.**  
   Include chat, embeddings, rerank if exposed, admin, dashboard, health, and at least one model alias from prod-only reconciliation.

9. **Decide Redis state explicitly.**  
   If Redis is fresh, document accepted behavior changes for rate limits, counters, and FSM mirror.

10. **Resolve host upstream networking.**  
   Do not leave `172.18.0.1:18000` as a latent broken path unless the system is explicitly tier-1-only post-migration.

---

## Consensus Summary (single reviewer — Codex)

Architecture sound; waves + reversible cutover seam + human checkpoints are praised. Overall risk **HIGH**, concentrated in **19-04 (DB migration)**. Once 19-04 mechanics are tightened, phase drops to MEDIUM.

### Agreed Strengths
- Cutover via edge-Traefik server-URL (not DNS) — one-line reversible seam.
- Old prod gateway kept alive through soak = rollback target.
- Embed-move-before-decommission gate.
- Hash-verbatim intent for api_keys; human checkpoints before irreversible steps.

### Agreed Concerns (HIGH — must fix before executing 19-04/05/06)
1. **19-04 not atomic:** `BEGIN` in one psql session + `psql -1 -f` in another ≠ one transaction. Use a SINGLE generated SQL file (BEGIN…deletes…COPY…verify…COMMIT) OR staging schema `ai_gateway_migration.*` + INSERT…SELECT in one tx.
2. **usage_counters filter:** `pg_dump --table` can't restrict to current period — use `COPY (SELECT … WHERE period=…)`.
3. **DELETE-dev-first scoping:** "delete all dev tenants" may nuke target-only dashboard/admin/alias state. Scope precisely + back up first.
4. **Collision precompute missing:** run preflight for duplicate tenant slug/id, `api_keys.key_lookup_hash`, `admin_keys.key_lookup_hash` BEFORE mutation.
5. **Hash proof:** add post-load byte-for-byte verify — `JOIN src/dst api_keys USING(id) WHERE key_hash= AND key_lookup_hash=` → expect 19. Counts alone insufficient.
6. **Cutover rollback = billing split:** post-cutover writes land in `bd_ai_gateway`; rolling ingress back to `_prod` splits billing/usage. Record cutover timestamp + define reconciliation.
7. **19-06 no rebuild path:** teardown removes rollback target with no documented rebuild. Pre-archive old compose/env-names/image-digests/`docker inspect` (root-owned) before down.

### Other MEDIUM (worth addressing)
- Verify `UPSTREAM_EMBED_URL=http://embed:7997` DNS resolves from the GATEWAY task netns (cross-Portainer-stack overlay), not just a debug container.
- Resolve `172.18.0.1:18000` explicitly (tier-1-only vs host.docker.internal vs 10.10.10.50) — don't leave latent broken.
- "zero client breakage" → reword "auth preserved, behavior parity" (sensitive tenants legitimately 503 when primary down).
- model_aliases: verify each ported alias with a REAL gateway call (alias string), not just DB presence; classify prod-only aliases (port/map/add-upstream/reject).
- Pin gateway/embed images by DIGEST during migration (not `:main`/`:latest-dev`).
- Define measurable soak (duration + 5xx/401/billing-write/redis thresholds) before 19-06.
- Disable/audit ops-claude timers during migration+cutover (could mutate wrong DB).

### Divergent Views
n/a — single reviewer.
