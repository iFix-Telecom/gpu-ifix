# Emergency Pod Runbook — Phase 6 (Vast.ai Auto-provisioning, Strategy B)

**Owner:** IFIX Platform Engineering
**Last updated:** 2026-05-16 (Phase 6 refactor — Strategy B Locked)
**Stack:** `ai-gateway-dev` / `ai-gateway-prod` (Portainer)
**Phase reference (active):** `.planning/phases/06-emergency-pod-template-refactor/06-CONTEXT.md`
**Phase reference (historical Phase 6.5 — autoprov bootstrap):** `.planning/phases/06.5-auto-provisioning-emergency-pod-vast-ai/06.5-CONTEXT.md`

> **Strategy B in effect (2026-05-16).** Emergency pods agora rodam a image
> upstream oficial `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128` (HF-endorsed)
> com `Runtype=args` + onstart inline via `--entrypoint /bin/bash --args -c`.
> A custom image `ghcr.io/ifixtelecom/ifix-ai-pod` foi DEPRECATED — será
> deletada quando PR2 (plan 06-07) merge. Detalhes do refactor + rationale:
> [Image source (Strategy B)](#image-source-strategy-b),
> [Onstart behavior (inline args)](#onstart-behavior-inline-args),
> [Reverting to Strategy A (legacy custom image)](#reverting-to-strategy-a-legacy-custom-image),
> [Phase 6 refactor — appendix](#phase-6-refactor--appendix).

This runbook covers the Phase 6 emergency-pod auto-provisioning subsystem
(`gateway/internal/emerg/` + `gateway/cmd/gatewayctl/emerg.go`). Read this when:

- The primary `local-llm` upstream is down for > 2 min and the gateway
  is supposed to spin up a Vast.ai emergency pod automatically.
- A Sentry alert fires with tag `subsystem:emerg` (any of:
  `alert:budget_exceeded`, `shutdown_reason:offer_race_lost`,
  `shutdown_reason:health_timeout`, `shutdown_reason:leader_recovery_zombie`).
- `/v1/health/upstreams` shows an upstream named `emergency_pod_llm` is
  serving production traffic (Phase 6 is active — operator should know).
- Cost dashboards show unexplained Vast.ai charges.
- Post-incident review of an emergency provisioning event.

Sibling runbooks:

- [`RUNBOOK-FAILOVER.md`](./RUNBOOK-FAILOVER.md) — Phase 3 circuit-breaker
  + tier-0 ↔ tier-1 fallback. Phase 6 sits on top: a sustained `local-llm`
  breaker.OPEN is what triggers the Phase 6 reconciler to provision a pod.
- [`RUNBOOK-QUOTAS-BILLING.md`](./RUNBOOK-QUOTAS-BILLING.md) — Phase 4
  per-tenant rate-limit + quota + billing.

Phase 7 will replace most manual diagnosis with a dashboard + alerts. Until
then, follow the diagnose → mitigate → verify cycles below.

---

## Architecture Overview (60 seconds)

```
                     ┌─────────────────────────────────────────────┐
                     │              gateway server                 │
                     │  ┌────────┐    ┌─────────────────────────┐  │
   gw:upstreams:     │  │breaker │───▶│ emerg.Reconciler (1 Hz) │  │
   events  ───────▶  │  │subscr. │    │  ┌──────────────────┐   │  │
                     │  └────────┘    │  │  FSM (7-state)   │   │  │
                     │                │  │ healthy → ... →  │   │  │
                     │                │  │ emergency_active │   │  │
                     │                │  └──────────────────┘   │  │
                     │                │  ┌──────────────────┐   │  │
                     │                │  │ leader-election  │◀─▶│ Redis
   gw:emerg:state    │                │  │ redsync TTL 30s  │   │  │  (gw:emerg:lock)
   gw:emerg:events   │                │  └──────────────────┘   │  │
        ◀────────────│                │  ┌──────────────────┐   │  │
                     │                │  │ vast.Client REST │◀─▶│ console.vast.ai
                     │                │  │ /search /create  │   │  │
                     │                │  │ /get   /destroy  │   │  │
                     │                │  └──────────────────┘   │  │
                     │                └─────────────────────────┘  │
                     │                                             │
                     │                ┌─────────────────────────┐  │
                     │                │ chat dispatcher         │  │
                     │  request  ───▶ │ if emerg.IsActive():    │  │
                     │                │   route → pod_url       │  │
                     │                │ else: route → local-llm │  │
                     │                └─────────────────────────┘  │
                     └─────────────────────────────────────────────┘
                                              │
                                              ▼
                                     Postgres ai_gateway.emergency_lifecycles
                                     (1 row per provision/destroy cycle)
```

### 7-state FSM

```
HEALTHY ─────▶ DEGRADED ─────▶ FAILED_OVER ────────▶ EMERGENCY_PROVISIONING
   ▲              │                  │                        │
   │              │                  │                        ▼
   │              ▼                  ▼               EMERGENCY_ACTIVE
   │           HEALTHY            HEALTHY                     │
   │                                                          ▼
COOLDOWN ◀────────────────── RECOVERING ◀──────────────────── │
   ▲                              │                           │
   │                              ▼                           │
   └──── auto after PROVISION_IDLE_GRACE_SECONDS ──────────── ┘
```

- **HEALTHY** — primary `local-llm` serving normally.
- **DEGRADED** — primary breaker flap or shedding ARMED; tier-0 still serves.
- **FAILED_OVER** — primary breaker.OPEN; tier-1 (OpenRouter) absorbing
  traffic. Trigger timer armed: if sustained ≥ `PROVISION_TRIGGER_FAILED_OVER_SECONDS`
  (default 120s), advance to PROVISIONING.
- **EMERGENCY_PROVISIONING** — leader bidding+creating Vast.ai pod.
  Cancellable via context.WithCancel if primary recovers mid-flight (D-C3).
- **EMERGENCY_ACTIVE** — pod healthy, dispatcher OverrideTier0 live for LLM role.
- **RECOVERING** — primary recovered; cutback grace `PROVISION_HEALTHY_DURATION_SECONDS`
  (default 300s) before destroy.
- **COOLDOWN** — pod destroyed; suppression hold `PROVISION_IDLE_GRACE_SECONDS`
  (default 300s); auto-returns to HEALTHY.

The full FSM contract lives in `gateway/internal/emerg/fsm.go`.

### Leader election (redsync)

- Lock key: `gw:emerg:lock`, TTL 30s, renew every 10s.
- Only the leader advances the FSM, dispatches Vast API calls, and writes
  audit rows. Non-leader replicas observe via Pub/Sub for visibility.
- Loss of leadership is graceful: leader cedes the local `is_leader=false`
  flag; next tick another replica races for the lock.
- See `gateway/internal/emerg/reconciler.go` for the `acquireLeadership`
  CAS loop and `Pitfall 4` quorum-loss handling.

### Cutback flow

```
EMERGENCY_ACTIVE
    │
    │ local-llm breaker stays CLOSED for PROVISION_HEALTHY_DURATION_SECONDS (300s)
    ▼
RECOVERING (dispatcher routes to BOTH primary and emergency pod briefly)
    │
    │ primary serves for cutback grace; dispatcher RestoreTier0("llm")
    │
    │ emergency pod has zero traffic for PROVISION_IDLE_GRACE_SECONDS (300s)
    ▼
vast.DestroyInstance(pod_id)
    │
    ▼
COOLDOWN (PROVISION_IDLE_GRACE_SECONDS suppression — no re-trigger)
    │
    ▼
HEALTHY
```

---

## Operator Surface — `gatewayctl emerg`

Four FUNCTIONAL subcommands (per CONTEXT.md D-E1, completed in Plan 06-10).

### `gatewayctl emerg state [--format=table|json]`

Read-only snapshot of `gw:emerg:state` Hash mirrored from the leader's FSM.

```
$ docker exec ai-gateway-dev_gateway /gatewayctl emerg state
KEY              VALUE
state            emergency_active
lifecycle_id     42
pod_url          http://140.228.20.111:40713
pod_instance_id  36717044
entered_at       1747201234

$ docker exec ai-gateway-dev_gateway /gatewayctl emerg state --format=json
{
  "state": "emergency_active",
  "lifecycle_id": "42",
  "pod_url": "http://140.228.20.111:40713",
  "pod_instance_id": "36717044",
  "entered_at": "1747201234"
}
```

Empty hash (`{}` in JSON, `(no state mirrored — reconciler may be in HEALTHY)` in
table mode) is normal at boot — the reconciler only mirrors on the FIRST
transition. HEALTHY → HEALTHY no-ops do not write to Redis.

### `gatewayctl emerg force-provision [--reason "<text>"]`

PUBLISH a typed `EmergEvent{Type:"force_provision_request"}` on
`gw:emerg:events`. The reconciler subscriber consumes leader-only and
drives the FSM `HEALTHY → ... → EMERGENCY_PROVISIONING` with audit
`trigger_reason='manual_force'`.

```
$ docker exec ai-gateway-dev_gateway /gatewayctl emerg force-provision --reason "operator_smoke_test"
force-provision request published; reconciler tick (~1s) consumes event and starts provisioning.
Run `gatewayctl emerg state` to confirm the FSM transition.
```

Use cases: smoke test, manual outage drill, debugging.

`gatewayctl` is a CLIENT — it does NOT pre-check leadership. The
reconciler's `applyEmergCommand` does the leader-only filter. So this
command can run on ANY replica (or even outside the cluster, given
Redis access) and the leader does the right thing.

### `gatewayctl emerg force-destroy`

PUBLISH `EmergEvent{Type:"force_destroy_request"}`. Reconciler leader
consumes, calls `destroyAndCloseLifecycle` with `shutdown_reason='manual'`.

```
$ docker exec ai-gateway-dev_gateway /gatewayctl emerg force-destroy
force-destroy request published; reconciler leader consumes event and tears down the live pod.
Run `gatewayctl emerg state` to confirm the FSM transition to COOLDOWN.
```

Use cases: cost cleanup, abort runaway, manual cutback override.

No-op when no live lifecycle exists — the leader's handler logs Warn and returns.

### `gatewayctl emerg lifecycles [--since 7d] [--limit 50] [--format=table|json]`

Query `ai_gateway.emergency_lifecycles` for recent rows.

```
$ docker exec ai-gateway-dev_gateway /gatewayctl emerg lifecycles --since 30d --limit 5
ID  STARTED               ENDED                 TRIGGER         VAST_OFFER  VAST_INST  DPH     COST_BRL  SHUTDOWN              REPLICA
42  2026-05-13T10:00:00Z  2026-05-13T10:08:21Z  manual_force    -           36717044   0.3519  0.2522    manual                ai-gateway-dev-1
41  2026-05-13T08:30:00Z  2026-05-13T08:45:00Z  failed_over_..  ...         ...        0.3500  1.7500    cutback_idle          ai-gateway-dev-1
40  2026-05-12T22:00:00Z  2026-05-12T22:00:30Z  failed_over_..  -           -          -       -         cancelled_in_flight   ai-gateway-dev-1

$ docker exec ai-gateway-dev_gateway /gatewayctl emerg lifecycles --since 7d --format=json | jq '.[0]'
```

`--since` accepts standard Go duration strings PLUS the operator-friendly
`Nd` suffix: `7d`, `30d`, `24h`, `45m`, `500ms`. The bare `Nd` form must be
integer-only (`7days` errors, `7d12h` errors — use `180h` instead).

---

## Deploy

### Pre-deploy checklist

1. CI build green: <https://github.com/IfixTelecom/gpu-ifix/actions> →
   `build-gateway` workflow on `develop` (dev) or `main` (prod).
2. Migration `gateway/db/migrations/0019_emergency_lifecycles.sql` is
   committed and present in the image (boot will run it via
   `AI_GATEWAY_MIGRATE_ON_BOOT=true`).
3. `VAST_AI_API_KEY` GitHub Secret exists:
   `gh secret list -R IfixTelecom/gpu-ifix | grep VAST_AI_API_KEY`.
4. Vast.ai account funded (≥ R$30 / $6) at <https://cloud.vast.ai/account/>.

### Deploy via Portainer

1. Open Portainer: <https://portainer3.ifixtelecom.com.br>.
2. Stacks → `ai-gateway-dev` (or `ai-gateway-prod`) → Editor.
3. Add/update the **14 Phase 6 env vars** in the stack environment block:

   | Env var                                  | Value (Wave-0 accepted default) | Source / decision                                  |
   | ---------------------------------------- | ------------------------------- | -------------------------------------------------- |
   | `VAST_AI_API_KEY`                        | (from GitHub Secret + CLAUDE.md token store; 64 hex chars) | D-A5 — empty disables Phase 6 cleanly |
   | `VAST_PRICE_CAP_DPH`                     | `0.40`                          | D-A2 — RTX 4090 cap; epsilon `cap+0.0001` (Pitfall 5) |
   | `MONTHLY_EMERGENCY_BUDGET_BRL`           | `200`                           | D-D2 — Sentry WARNING only, NO auto-block          |
   | `USD_TO_BRL_RATE`                        | `5.0`                           | D-D4 — operator updates quarterly                  |
   | **`EMERGENCY_TEMPLATE_IMAGE`**           | `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128` | **Phase 6 D-01-B (Strategy B)** — HF-endorsed upstream image; tag SHA-pinned per ggml-org build |
   | **`EMERGENCY_JINJA_TEMPLATE_KEY`**       | `emerg-onstart/templates/qwen3.5-27b-tool-calling-1067302cc6d927210a84775b9a060f724da15debc168c79710cbf763512e9f67.jinja` | **Phase 6 D-04-B B2** — MinIO fetch in onstart |
   | **`EMERGENCY_JINJA_TEMPLATE_SHA256`**    | `1067302cc6d927210a84775b9a060f724da15debc168c79710cbf763512e9f67` | **Phase 6 D-04-B B2** — sha256-verify before exec |
   | **`EMERGENCY_LLAMA_ARGS`**               | (empty)                         | **Phase 6 D-07-B** — CSV/JSON args override; empty → lifecycle.go uses hardcoded const |
   | ~~`EMERGENCY_POD_IMAGE_TAG`~~            | ~~`v1.0`~~                      | **DEPRECATED — Strategy A only; REMOVE from stack post-Phase-6 deploy** |
   | `PROVISION_TRIGGER_FAILED_OVER_SECONDS`  | `120`                           | D-C1 — bate SC-1 example "e.g., 2 min"             |
   | `PROVISION_HEALTHY_DURATION_SECONDS`     | `300`                           | D-D1 — primary healthy this long before cutback    |
   | `PROVISION_IDLE_GRACE_SECONDS`           | `300`                           | D-D1 — emergency pod idle grace before destroy     |
   | `PROVISION_COLDSTART_BUDGET_SECONDS`     | `600`                           | D-A4 — bate SC-1 literal "≤10min once /health passes" |
   | `PRIMARY_HOST_ID`                        | `0`                             | D-A2 — host_id != filter only when known (≠0)      |
   | `VAST_API_QPS_LIMIT`                     | `1`                             | RESEARCH OQ-12 — conservative 1 req/s token bucket |

   The 4 highlighted (**bold**) are Strategy B fields introduced in Phase 6
   refactor and override the old `EMERGENCY_POD_IMAGE_TAG`. Remove the
   strikethrough `EMERGENCY_POD_IMAGE_TAG` entirely from the stack after
   the Phase 6 image is deployed — `config.go` no longer reads it.

   See [Image source (Strategy B)](#image-source-strategy-b) and
   [Onstart behavior (inline args)](#onstart-behavior-inline-args) for what
   these vars wire into.

4. Hit **Update the stack** → triggers webhook → Portainer pulls new
   image via the GitHub Actions `develop` (or `main`) build label.
5. Watch container creation:
   `ssh vps-ifix-vm 'docker ps --filter name=ai-gateway-dev'`.

### Post-deploy checklist

- [ ] **Container running:**
      `ssh vps-ifix-vm 'docker ps --filter name=ai-gateway-dev_gateway --format "{{.Status}}"'`
      shows `Up N seconds (healthy)`.
- [ ] **Vast.Ping ok at boot:**
      `ssh vps-ifix-vm 'docker logs ai-gateway-dev_gateway --since 5m 2>&1 | grep vast.Ping'`
      shows `vast.Ping ok`. If `vast.Ping failed` is logged, the API key is
      wrong/expired — Phase 6 reconciler will still start but every Vast op
      will fail at runtime. Fix the key in Portainer + redeploy.
- [ ] **Reconciler started:**
      `ssh vps-ifix-vm 'docker logs ai-gateway-dev_gateway --since 5m 2>&1 | grep "emergency reconciler started"'`
      shows the boot line with `replica_id`, `trigger_seconds`,
      `healthy_seconds`, `idle_grace_seconds`, `coldstart_budget_seconds`,
      `monthly_budget_brl` — all 11 Phase 6 knobs surfaced for sanity check.
- [ ] **Leadership acquired (single-replica):**
      `ssh vps-ifix-vm 'docker logs ai-gateway-dev_gateway --since 5m 2>&1 | grep "acquired leadership"'`
      should show 1 line within ~30s of boot.
- [ ] **FSM at HEALTHY:**
      `ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway /gatewayctl emerg state --format=json'`
      returns `{}` (empty mirror at boot — HEALTHY is the initial state, the
      reconciler only mirrors on first transition) OR `{"state":"healthy",...}`.
- [ ] **Migration 0019 applied:**
      `ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -c "\d ai_gateway.emergency_lifecycles"'`
      shows the table with 11 columns and 5 indexes (including the
      `emergency_live_singleton` partial unique index per D-B5).
- [ ] **Prometheus metrics exposed:**
      `ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway curl -s http://localhost:8080/metrics | grep ^gateway_emergency'`
      shows the 7 emergency metrics:
      `gateway_emergency_state`, `gateway_emergency_lifecycles_total`,
      `gateway_emergency_active_pod`, `gateway_emergency_provision_duration_seconds`,
      `gateway_emergency_cost_dph`, `gateway_emergency_month_cost_brl`,
      `gateway_vast_api_requests_total`.
- [ ] **Sentry only emits on transitions:** at idle no `subsystem:emerg`
      events should appear in Sentry. The first event will be the first
      FSM transition (degraded/failed_over/manual_force).

### Auto-prereq if Phase 6 disabled by design

To deploy the gateway WITHOUT Phase 6 enabled, leave `VAST_AI_API_KEY`
unset (or `=""`) in the Portainer stack. The boot logs will show
`Phase 6 emergency reconciler DISABLED: VAST_AI_API_KEY not set` and the
reconciler stays nil. The dispatcher's `EmergTraffic` field stays nil and
`emerg.IsActive()` is never reached. Migration 0019 still runs (the empty
table is idle-cheap).

---

## Image source (Strategy B)

Phase 6 refactor (2026-05-16) trocou a source da emergency-pod image:

| Aspect            | Strategy A (legacy, pre-Phase-6)                | Strategy B (current, post-Phase-6)                                     |
| ----------------- | ------------------------------------------------ | ----------------------------------------------------------------------- |
| Image             | `ghcr.io/ifixtelecom/ifix-ai-pod:latest-dev`     | `ghcr.io/ggml-org/llama.cpp:server-cuda-b9128`                          |
| Owner             | Custom (IFIX-built via `pod/Dockerfile`)         | Upstream (ggml-org, official, HF-endorsed)                              |
| Size              | ~6 GB (baked: llama-server + Jinja + sshd)       | ~3 GB (baked: llama-server only; weights+Jinja fetched at onstart)      |
| ENTRYPOINT        | `/usr/sbin/sshd -D` (Runtype=ssh injection)      | `/app/llama-server` (preserved via Runtype=args)                        |
| Build pipeline    | GHCR via `.github/workflows/build-pod.yml`       | None (upstream pull at provision time)                                  |
| Pinning           | Mutable tag (`:latest-dev` / `:v1.0`)            | Build-pinned tag (`:server-cuda-b9128`); operator may pin harder via `@sha256:...` |
| Env var driver    | `EMERGENCY_POD_IMAGE_TAG`                        | `EMERGENCY_TEMPLATE_IMAGE` (full image ref incl. tag)                   |

**Why upstream:** HuggingFace officially endorsed llama.cpp as TGI replacement
(2026-03-21) — ggml-org publishes server-cuda variants to GHCR per release.
Strategy B inherits HF security review + upstream provenance without rebuild
ceremony. Custom image was the only legacy reason `Runtype=ssh` ever existed,
and Runtype=ssh caused the CMD-ignore bug (`STATE.md:85`, lifecycles 29-33
travados em `health_timeout 1800s`). Strategy B fixes both.

### Bumping the image tag

To upgrade to a newer llama.cpp build (e.g., `b9300`):

```bash
# 1. Find the desired tag on ggml-org GHCR:
#    https://github.com/ggml-org/llama.cpp/pkgs/container/llama.cpp
#    Filter "server-cuda-*" — pick build that includes Qwen3 chat-template fixes.
# 2. (RECOMMENDED) Pin via digest for immutability:
docker manifest inspect ghcr.io/ggml-org/llama.cpp:server-cuda-b9300 \
  | jq -r '.manifests[] | select(.platform.architecture=="amd64").digest'
# Outputs: sha256:abc123...

# 3. Update Portainer stack ai-gateway-dev:
#    EMERGENCY_TEMPLATE_IMAGE=ghcr.io/ggml-org/llama.cpp@sha256:abc123...
# 4. Hit "Update the stack" — next emergency pod uses the new tag.
# 5. Validate via short UAT (1 force-provision + force-destroy cycle).
```

No code change, no gateway rebuild, no redeploy outside Portainer env edit.

---

## Onstart behavior (inline args)

Strategy B sends the bootstrap **inline** via Vast.ai's `CreateInstance`
payload rather than baking a script into the image:

```go
// Excerpt from gateway/internal/emerg/lifecycle.go (buildCreateRequest)
vast.CreateRequest{
    Image:       cfg.EmergencyTemplateImage,         // "ghcr.io/ggml-org/llama.cpp:server-cuda-b9128"
    Disk:        40,                                  // GB — 06-WAVE0-GATES.md Decision 1
    Runtype:     "args",                              // PRESERVES image ENTRYPOINT semantics
    Entrypoint:  "/bin/bash",                         // REQUIRED override per 06-SPIKE-runtype-args.md Round 2
    Args:        []string{"-c", emergencyOnstart},   // 2 elements; bash receives the inline script
    Env:         emergencyEnvMap,                     // MinIO creds + Jinja key/sha + weights metadata
}
```

The `emergencyOnstart` raw-string (lifecycle.go) does, in order:

1. **`set -e`** + log timestamps to stdout (captured by `vastai logs`).
2. **`mc`/`curl` Qwen weights** from `s3.ifixtelecom.com.br/ai-gateway/<WEIGHTS_QWEN_KEY>`
   with **`sha256sum -c`** verification (pattern from Phase 1 D-05, reused 1:1).
3. **`mc` Jinja template** from
   `s3.ifixtelecom.com.br/ai-gateway/$EMERGENCY_JINJA_TEMPLATE_KEY` with
   `sha256sum -c` against `$EMERGENCY_JINJA_TEMPLATE_SHA256`. Skipped if
   the key env is empty (legacy/test mode — operator must ensure image
   has bundled template OR llama-server falls back to default).
4. **`exec /app/llama-server --host 0.0.0.0 --port 8000 -m /weights/qwen/model.gguf
   -ngl 99 -np 2 --ctx-size 32768 --jinja --chat-template-file <path>`**.

The `exec` is critical: bash overlays itself with llama-server, so
**llama-server becomes PID 1** in the container. Crash detection works
cleanly (PID 1 dies → container dies → Vast.ai marks failure → reconciler's
post-create polling observes terminal status → audit row closes with
`shutdown_reason='instance_terminal_state'`). This is the inverse of the
Strategy A bug where sshd was PID 1 and llama-server crashes were invisible
(STATE.md:85 root cause).

### Inline limits

- `args[-c "<script>"]` token budget: Vast API limit ~4048 chars total.
  Current script is ~1400 chars (06-SPIKE-runtype-args.md Round 2 used 250
  chars; full onstart is ~1400). Comfortable margin.
- No external script storage required (vs Strategy A's `pod/scripts/emerg-bootstrap.sh`
  baked into the image). Iteration loop: edit lifecycle.go raw-string → rebuild
  gateway (only) → next emergency uses new bash.

---

## Reverting to Strategy A (legacy custom image)

**NO LONGER POSSIBLE via config rollback.** PR2 (Phase 6 plan 06-07) deleted:

- `pod/Dockerfile`
- `pod/scripts/emerg-bootstrap.sh`
- `.github/workflows/build-pod.yml`

To revert to Strategy A:

1. `git revert <PR2-merge-commit>` to restore Dockerfile + script + workflow.
2. `git revert <PR1-merge-commit>` to restore `Cfg.EmergencyPodImageTag` +
   `lifecycle.go` Strategy A `buildCreateRequest` (Runtype=ssh + baked-image
   CMD path).
3. CI re-builds `ghcr.io/ifixtelecom/ifix-ai-pod:latest-dev` (if GHCR package
   still exists — see GHCR cleanup section below).
4. Portainer ai-gateway-dev env: change `EMERGENCY_TEMPLATE_IMAGE` back to
   `EMERGENCY_POD_IMAGE_TAG=latest-dev`.
5. Restart gateway container.

Total rollback time: ~30 min (vs ~2 min for config-only rollback that was
available before PR2).

**Pre-PR2 rollback path (config-only):** documented for historical reference
but no longer applicable. Operator who needs custom image must follow the
`git revert` path above.

Risk acknowledgement: PR2 gate (`06-HUMAN-UAT.md` PR2 GO) was satisfied by
Lifecycle 39 GREEN on Vast 4090 Spain (commit eaa6188); see
`.planning/phases/06-emergency-pod-template-refactor/06-06-SUMMARY.md` for
empirical evidence. If a Strategy-B-specific bug surfaces post-merge, the
`git revert` path above is the documented escape hatch.

## GHCR Cleanup (optional, post-2-week-observability-window)

`ghcr.io/ifixtelecom/ifix-ai-pod` package on GitHub Container Registry was
NOT deleted by PR2 — it remains as a safety net during the 2-week
observability window after PR2 merge. After confidence in Strategy B is
established (2 weeks of stable emergency lifecycles), operator can free the
namespace:

```bash
gh api -X DELETE /orgs/ifixtelecom/packages/container/ifix-ai-pod
```

After this command runs, `git revert` rollback (above) will fail at step 3
(CI re-build needs the package to exist for the build/push to land) unless
operator also re-creates the GHCR package and re-pushes manually.

---

## Phase 6 refactor — appendix

Refactored 2026-05-16 from Strategy A (custom GHCR image
`ghcr.io/ifixtelecom/ifix-ai-pod` + `Runtype=ssh` — bug STATE.md:85
CMD-ignore) to Strategy B (`ghcr.io/ggml-org/llama.cpp:server-cuda-b9128`
upstream + `Runtype=args` + inline onstart via
`--entrypoint /bin/bash --args -c`). See
[`.planning/phases/06-emergency-pod-template-refactor/`](../../.planning/phases/06-emergency-pod-template-refactor/)
for full context:

- `06-CONTEXT.md` — Strategy B Locked decisions (D-01-B through D-08-B-risk)
- `06-RESEARCH.md` — HF endorsement of llama.cpp + upstream image rationale
- `06-SPIKE-runtype-args.md` — empirical validation Round 1+2 (Sichuan offer 33453594)
- `06-WAVE0-GATES.md` — operator decisions: Jinja B2-40GB, MinIO key/sha, args revised pattern
- `06-PATTERNS.md` — code patterns (raw-string Go onstart, sha256 verify reuse)
- `06-VALIDATION.md` — Wave 0..4 verification gates
- `06-06-PLAN.md` + `06-HUMAN-UAT.md` — burnt-bridge mitigation gate (live UAT)
- `06-07-PLAN.md` — PR2 cleanup (delete custom image artifacts) — BLOCKED until UAT 3/3 GREEN
- PR2 (2026-05-17): deleted `pod/Dockerfile`, `pod/scripts/emerg-bootstrap.sh`,
  `.github/workflows/build-pod.yml`. Strategy A no longer reversible without
  `git revert`. See "Reverting to Strategy A (legacy custom image)" above.

---

## Common Operations

### Trigger a manual provisioning (outage drill / smoke test)

```bash
ssh vps-ifix-vm
docker exec ai-gateway-dev_gateway /gatewayctl emerg force-provision --reason "outage_drill"

# Watch the FSM advance every 30s
for i in {1..20}; do
  echo "=== $(date) ==="
  docker exec ai-gateway-dev_gateway /gatewayctl emerg state --format=json
  sleep 30
done
```

Expected: FSM reaches `emergency_active` in ≤10min (SC-1). When done,
`force-destroy` to clean up.

### Inspect live FSM + active lifecycle

```bash
docker exec ai-gateway-dev_gateway /gatewayctl emerg state --format=json
```

If `state` is `emergency_active` or `emergency_provisioning`, also pull
the lifecycle row for events:

```bash
docker exec ai-gateway-dev_gateway /gatewayctl emerg lifecycles --since 1h --format=json | jq '.[0].Events'
```

The `events` JSONB array contains the full transition trail:
`{ts, from_state, to_state, reason, payload}`.

### Force-destroy a runaway pod

```bash
docker exec ai-gateway-dev_gateway /gatewayctl emerg force-destroy
```

Audit row closes with `shutdown_reason='manual'`. Verify in Vast UI that
the instance is gone within 30s.

### Query monthly cost

```bash
# Via the gateway
docker exec ai-gateway-dev_gateway /gatewayctl emerg lifecycles --since 30d --format=json \
  | jq '[.[] | .TotalCostBrl // 0] | add'

# Or directly via SQL (matches the reconciler's checkBudget aggregate)
docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -c "
SELECT COALESCE(SUM(total_cost_brl), 0) AS month_cost_brl
  FROM ai_gateway.emergency_lifecycles
 WHERE started_at >= date_trunc('month', NOW())
   AND ended_at IS NOT NULL;
"
```

### Quarterly USD_TO_BRL_RATE update

The `USD_TO_BRL_RATE` env var is read at boot. Operator updates quarterly
(or on > 10% FX swing) for cost audit accuracy:

1. Fetch current rate: <https://www.bcb.gov.br/estabilidadefinanceira/historicoseries>
   (or any reliable source — pick the average of the last 30 days).
2. Update env in Portainer stack `ai-gateway-dev` and `ai-gateway-prod`.
3. Hit "Update the stack" — webhook redeploys with the new rate.
4. Note: existing closed lifecycle rows are NOT recomputed — `total_cost_brl`
   is frozen at close-time. The new rate only affects future closures.

---

## Incident Playbook

### Budget overrun (Sentry alert: `subsystem:emerg alert:budget_exceeded`)

**Symptom:** Sentry warning event with tags `subsystem=emerg`,
`alert=budget_exceeded`. Email/Slack notification (if configured).

**Diagnosis:**

```bash
# How much have we spent this month?
docker exec ai-gateway-dev_gateway /gatewayctl emerg lifecycles --since 30d --format=json \
  | jq '{total: ([.[] | .TotalCostBrl // 0] | add), count: length}'

# Cross-check Vast.ai bill (browser):
#   https://cloud.vast.ai/billing/

# Top-5 most expensive lifecycles
docker exec ai-gateway-dev_gateway /gatewayctl emerg lifecycles --since 30d --format=json \
  | jq 'sort_by(.TotalCostBrl // 0) | reverse | .[:5] | map({id, dph: .AcceptedDph, brl: .TotalCostBrl, trigger: .TriggerReason, shutdown: .ShutdownReason})'

# Trigger reason distribution
docker exec ai-gateway-dev_gateway /gatewayctl emerg lifecycles --since 30d --format=json \
  | jq '[.[] | .TriggerReason] | group_by(.) | map({(.[0]): length}) | add'
```

**Action:**

- If `manual_force` dominates → investigate operator-driven overuse (was
  there a UAT or smoke test that ran multiple times?). Document and move on.
- If `failed_over_sustained` dominates → primary `local-llm` is unstable.
  Investigate Phase 1 pod health on the primary 4090; the emergency pod is
  doing exactly its job covering for an unreliable primary. Cost growth
  is a symptom, not the disease.
- If trend is legitimately growing (more outages over time, more dependent
  services) → raise the budget:
  Update `MONTHLY_EMERGENCY_BUDGET_BRL=400` (or appropriate) in Portainer
  stack + redeploy.
- The alert is **non-blocking by design** (D-D2). Provisioning continues —
  operator decides when to hard-stop. To hard-stop: set
  `MONTHLY_EMERGENCY_BUDGET_BRL=0` AND set `VAST_AI_API_KEY=""` (the latter
  fully disables Phase 6 — see Rollback section).

The dedupe gate (Pitfall 11) guarantees exactly 1 alert per UTC day across
the cluster. If you see > 1 alert per day, investigate `budgetAlertDedupe`
in `gateway/internal/emerg/budget.go` (CAS race regression).

### Zombie pod (Sentry: `shutdown_reason:leader_recovery_zombie`)

**Symptom:** Sentry CaptureMessage with `shutdown_reason=leader_recovery_zombie`.
Means: a new leader took over and discovered an orphan instance row
referencing a Vast instance that is in a terminal status.

**Diagnosis:**

```bash
# Which lifecycle?
docker exec ai-gateway-dev_gateway /gatewayctl emerg lifecycles --since 1h --format=json \
  | jq '.[] | select(.ShutdownReason == "leader_recovery_zombie")'

# Was the instance still in Vast at recovery time?
# Check the events JSONB for the recovery payload
docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -c "
SELECT id, vast_instance_id, events
  FROM ai_gateway.emergency_lifecycles
 WHERE shutdown_reason = 'leader_recovery_zombie'
 ORDER BY ended_at DESC
 LIMIT 3;
"

# Why did leadership churn? Look for "lost leadership" or "renew failed"
ssh vps-ifix-vm 'docker logs ai-gateway-dev_gateway --since 1h 2>&1 | grep -E "leadership|redsync|extend"'
```

**Action:**

- The recovery already destroyed the pod and closed the row — no manual
  cleanup needed. This is **the system working correctly** (D-D5).
- Investigate the root cause of leadership churn:
  - Redis flapping → check `infra-redis-1` health on `vps-ifix-vm`.
  - Network blip between gateway and Redis → check Traefik / docker network.
  - GC pause / OOM-kill → check container memory limits in Portainer.
- If churn is recurrent (≥ 1 per day), file an issue against `gateway/internal/emerg/recovery.go`
  to look at lock TTL tuning (D-B2 is currently TTL=30s, renew=10s).

### Unique violation (Postgres: `duplicate key violates emergency_live_singleton`)

**Symptom:** Gateway logs show `ERROR: duplicate key value violates unique
constraint "emergency_live_singleton"`. Means: two reconciler ticks tried
to insert a live row simultaneously, OR a leader-recovery race.

**Diagnosis:**

```bash
# Should be exactly 1 (or 0); 2+ is corruption
docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -c "
SELECT id, started_at, vast_instance_id
  FROM ai_gateway.emergency_lifecycles
 WHERE ended_at IS NULL
 ORDER BY id DESC;
"

# Both replicas' FSM state — should differ (only 1 is leader)
ssh vps-ifix-vm 'docker ps --filter name=ai-gateway-dev_gateway --format "{{.Names}}"' | while read c; do
  echo "=== $c ==="
  docker exec "$c" /gatewayctl emerg state --format=json
done
```

**Action:**

- If 2+ live rows: PostgreSQL's defense-in-depth caught a race the leader
  lock missed (D-B5 — by design). Manually close the older row:
  ```sql
  UPDATE ai_gateway.emergency_lifecycles
     SET ended_at = NOW(),
         shutdown_reason = 'manual_split_brain_recovery'
   WHERE id = <older_id>;
  ```
- Investigate redsync logs for quorum loss (Pitfall 4 — `(false, nil)` from
  `Extend()` is the canonical signature). The `TestExtendQuorumLoss` regression
  guard exists in `gateway/internal/emerg/reconciler_test.go`.
- If recurrent, consider restarting both replicas to force fresh leader
  election from a clean state.

### Vast API down (`vast.Ping` fails OR `gateway_vast_api_requests_total{status="transport_error"}` spike)

**Symptom:** Boot logs show `vast.Ping failed` (non-fatal but warning), OR
Prometheus metric spike on `gateway_vast_api_requests_total{status="transport_error"}`.

**Diagnosis:**

```bash
# Is the API reachable from the gateway host?
ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway curl -s -o /dev/null -w "%{http_code}\n" \
  -H "Authorization: Bearer $VAST_AI_API_KEY" \
  https://console.vast.ai/api/v0/users/current/'

# Status from Vast
curl -s https://vast.ai/status

# Confirm API key still valid
# (browser): https://cloud.vast.ai/account/ → API Keys
```

**Action:**

- Vast.ai outage: emergency provisioning will fail until Vast recovers.
  Acceptable degradation — Phase 3 fallback (OpenRouter tier-1) covers
  the LLM role for the duration. Sentry alerts will continue (each
  `failed_over_sustained` trigger that fails to provision will close
  the lifecycle with `shutdown_reason='offer_race_lost'` or similar).
- API key revoked/expired: rotate via <https://cloud.vast.ai/account/> →
  API Keys → New key → update Portainer stack + GitHub Secret +
  CLAUDE.md token store + redeploy.
- The reconciler does NOT halt on Vast errors — it just fails the
  current provisioning attempt and waits for the next trigger
  (Pitfall 6 health/ports null-safety; bid race retry 3x exp backoff).

### Strategy B onstart failed (`actual_status=running` but `:8000/v1/models` refused)

**Symptom:** Vast.ai dashboard mostra `actual_status=running` mas
`curl http://<pod_url>:8000/v1/models` retorna `connection refused` ou
timeout. Reconciler health-poll falha, FSM eventualmente vai pra
`shutdown_reason='health_timeout'`.

**Root cause (Strategy B specific):** onstart bash script falhou ANTES
de chegar no `exec /app/llama-server`. Possíveis pontos de falha:

- `mc cp` Qwen weights timeout (MinIO slow ou rede instável)
- `sha256sum -c` falhou (weights ou Jinja com hash divergente)
- `mc` install falhou (apt error transitório)

**Diagnosis:**

```bash
# Strategy B NÃO permite `vastai ssh <id>` — Runtype=args desabilita
# SSH injection no host Vast. Debug é via vastai logs:
INST=$(docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -t -c \
  "SELECT vast_instance_id FROM ai_gateway.emergency_lifecycles WHERE ended_at IS NULL;" | xargs)
vastai logs $INST | tail -200

# Procurar:
#   - "sha256sum: WARNING: 1 computed checksum did NOT match" — hash mismatch
#   - "ERROR: pulling from Minio failed" — MinIO connectivity
#   - "apt-get install: failed" — package install (rare; image já tem curl)
#   - Pulse exit antes de "version: 9128" — onstart bash exited
```

**Action:**

- **sha256 mismatch:** verificar `WEIGHTS_QWEN_SHA256` (config) vs hash
  real do arquivo em MinIO:
  ```bash
  mc cat ifix/ai-gateway/<WEIGHTS_QWEN_KEY> | sha256sum
  ```
  Se hash do MinIO != hash do config, alguém atualizou o arquivo sem bumpar
  config. Atualizar `WEIGHTS_QWEN_SHA256` env no Portainer stack + redeploy.
  Mesmo padrão pra Jinja: `EMERGENCY_JINJA_TEMPLATE_SHA256` vs
  `mc cat ifix/ai-gateway/$EMERGENCY_JINJA_TEMPLATE_KEY | sha256sum`.
- **MinIO unavailable:** check `curl https://s3.ifixtelecom.com.br/minio/health/live`
  → HTTP 200. Se 5xx, MinIO degraded — escalate to infra. Reconciler bid race
  retry NÃO vai ajudar (problema é network, não bid).
- **Manual destroy + retry:**
  `docker exec ai-gateway-dev_gateway /gatewayctl emerg force-destroy`,
  então `force-provision` novamente após root cause fixed.

### Stuck FSM (e.g. `emergency_provisioning` for > 15min)

**Symptom:** `gatewayctl emerg state` shows `state=emergency_provisioning`
much longer than `PROVISION_COLDSTART_BUDGET_SECONDS` (default 600s) +
buffer. Should self-resolve (`shutdown_reason='health_timeout'`) but if not:

**Diagnosis:**

```bash
# What is the live lifecycle and how old?
docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -c "
SELECT id, started_at, vast_instance_id, events
  FROM ai_gateway.emergency_lifecycles
 WHERE ended_at IS NULL;
"

# Vast says what about that instance?
INST=$(docker exec ai-gateway-dev_gateway psql "$AI_GATEWAY_PG_DSN" -t -c "SELECT vast_instance_id FROM ai_gateway.emergency_lifecycles WHERE ended_at IS NULL;" | xargs)
ssh vps-ifix-vm "docker exec ai-gateway-dev_gateway curl -s -H 'Authorization: Bearer \$VAST_AI_API_KEY' https://console.vast.ai/api/v0/instances/$INST/" | jq

# Reconciler logs in the last 15min
ssh vps-ifix-vm 'docker logs ai-gateway-dev_gateway --since 15m 2>&1 | grep -E "emerg|lifecycle|provision"'
```

**Action:**

- **Strategy B Pitfall 3 — `vastai ssh <id>` does NOT work for emergency pods.**
  With `Runtype=args`, Vast.ai documentation states explicitly: *"If you use
  args/entrypoint launch mode, we create a container from your image as is,
  without attempting to inject ssh and or jupyter."* (verified empirically
  in `06-SPIKE-runtype-args.md`). For interactive debug of a stuck pod, use:
  ```bash
  vastai logs <instance_id>           # stdout/stderr trail
  vastai show instance <instance_id>  # status + ports + image manifest
  ```
  SSH access requires re-provisioning the instance with `Runtype=ssh` (not
  applicable for production emergency lifecycles — that path is the bug we
  fixed).
- If Vast `actual_status` is `running` AND ports are populated AND the
  pod's `/health` is reachable from outside the gateway, but the FSM
  is stuck — manual `force-destroy` to abandon and start fresh:
  `docker exec ai-gateway-dev_gateway /gatewayctl emerg force-destroy`.
- If Vast says `exited` or `unknown` or `offline` — Pitfall 9 terminal-status
  guard should catch it within 1 reconciler tick. If it does not,
  `force-destroy` and file an issue against `gateway/internal/emerg/lifecycle.go`
  health-check loop.
- If the intended_status flipped to `stopped` (one of the failure modes
  observed during the spike — image manifest 404), the reconciler's
  intended-status mismatch detection (Plan 06-07 cancel-in-flight extension)
  should trigger a destroy. If it does not, use `force-destroy`.

---

## Failure Mode Reference

| Mode                                   | Observed via                                                  | Recovery                                                                                                              |
| -------------------------------------- | ------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------- |
| Leader churn → orphan instance live    | Sentry `leader_recovery_zombie`                               | (a) Auto: new leader runs `recoverOrphanLifecycles` → destroy + close. (b) See Zombie Pod incident.                   |
| Leader churn → orphan instance gone    | Sentry `leader_recovery_lost`                                 | Auto: new leader closes row with `leader_recovery_lost`. No action needed.                                            |
| Leader churn → orphan pre-create       | Sentry `leader_recovery_pre_create`                           | Auto: new leader closes row (no instance was ever created). No action needed.                                         |
| Bid race lost (3 retries failed)       | Sentry `offer_race_lost` + audit row                          | Auto: lifecycle closes. If recurrent, Vast 4090 inventory is contested — raise `VAST_PRICE_CAP_DPH` cautiously.       |
| /health timeout (cold-start > budget)  | Sentry `health_timeout` + audit row                           | Auto: lifecycle closes + Vast destroyed. If recurrent, MinIO weights pull may be slow — check `s3.ifixtelecom.com.br` health, raise `PROVISION_COLDSTART_BUDGET_SECONDS=900`. |
| Vast instance terminal (Pitfall 9)     | Sentry `instance_terminal_state` + audit row                  | Auto: lifecycle closes + (if needed) destroy invoked. Vast left the instance in `exited`/`unknown`/`offline` — usually image issue (manifest, onstart script).  |
| Cancel-in-flight (primary recovered)   | Audit `shutdown_reason='cancelled_in_flight'`                 | Auto: D-C3 triple-layer cancel. Zero leak guarantee.                                                                  |
| Budget exceeded                        | Sentry `alert:budget_exceeded` (Warning)                      | Manual review (see Budget Overrun incident). Provisioning continues unless operator hard-stops.                       |

---

## Cost & Budget Reference

- **DPH cap:** `VAST_PRICE_CAP_DPH=0.40` (USD/hour). RTX 4090 at this cap
  ≈ R$2/hour at `USD_TO_BRL_RATE=5.0`. A 1-hour outage costs ~ R$2.
- **Budget alert threshold:** `MONTHLY_EMERGENCY_BUDGET_BRL=200`. At
  R$2/hour, the alert fires at ~ 100 cumulative hours of emergency-pod
  uptime per UTC month.
- **Cost calculation (D-D4):** `total_cost_brl = accepted_dph × hours_active × USD_TO_BRL_RATE`
  where `hours_active = (ended_at - first_health_pass_at) / 3600`.
  Cold-start time (from `started_at` to `first_health_pass_at`) is
  EXCLUDED from the audit — the gateway accounts for "useful service time"
  not "Vast bill time". Vast bill = audit + cold-start (typically 5-10min).
- **USD_TO_BRL_RATE updates:** quarterly. See "Common Operations →
  Quarterly USD_TO_BRL_RATE update" above.

---

## Sentry Forensics Cheat-sheet

Common search filters in the `ifix-ai-gateway` (or `-dev`) Sentry project:

| Search query                                  | Surfaces                                                               |
| --------------------------------------------- | ---------------------------------------------------------------------- |
| `subsystem:emerg`                             | All Phase 6 events (transitions, alerts, terminal closes)              |
| `subsystem:emerg alert:budget_exceeded`       | Budget alert events (≤ 1 per UTC day; dedupe gate Pitfall 11)          |
| `subsystem:emerg shutdown_reason:health_timeout` | Cold-start / weight-pull failures                                  |
| `subsystem:emerg shutdown_reason:offer_race_lost` | Bid race exhaustion (Vast inventory contention)                    |
| `subsystem:emerg shutdown_reason:leader_recovery_zombie` | Leader churn + orphan instance                              |
| `subsystem:emerg shutdown_reason:cancelled_in_flight` | Cancel-in-flight (primary recovered mid-provision)             |
| `lifecycle_id:42`                             | All breadcrumbs for a specific lifecycle (forensics)                   |

Each terminal-state CaptureMessage carries breadcrumbs from every FSM
transition for that lifecycle (D-E4 → `lifecycle.go captureBreadcrumb`).
The breadcrumb category is `emergency`, level varies by transition reason.

---

## Rollback

To **disable** Phase 6 cleanly without rolling back the gateway image:

1. Edit Portainer stack `ai-gateway-dev` env: set
   `MONTHLY_EMERGENCY_BUDGET_BRL=0` (will alert immediately on any new
   spend) AND `VAST_AI_API_KEY=""` (Phase 6 reconciler skips construction
   gracefully — see boot logs `Phase 6 emergency reconciler DISABLED`).
2. Hit "Update the stack" → webhook redeploys.
3. Verify in logs:
   `ssh vps-ifix-vm 'docker logs ai-gateway-dev_gateway --since 5m 2>&1 | grep "emergency reconciler DISABLED"'`.
4. Migration `0019_emergency_lifecycles.sql` does NOT need to be reverted —
   the empty/idle table is essentially free (5 indexes, all small).

To **fully revert** to a pre-Phase-6 image:

1. In Portainer, change the stack image tag to the pre-Phase-6 build
   (look for the GHA build `develop-<sha>` from before commit `213c557`
   added migration 0019).
2. Hit "Update the stack" → webhook redeploys with the old image.
3. Migration 0019 will NOT be rolled back automatically (goose down is
   not run on boot). To revert the schema:
   ```bash
   ssh vps-ifix-vm 'docker exec ai-gateway-dev_gateway goose -dir db/migrations postgres "$AI_GATEWAY_PG_DSN" down'
   ```
   (Omit unless you need to free the schema; the empty table is harmless.)

---

## References

- Phase 6 PRD: [`.planning/phases/06-auto-provisioning-emergency-pod-vast-ai/06-CONTEXT.md`](../../.planning/phases/06-auto-provisioning-emergency-pod-vast-ai/06-CONTEXT.md)
- Phase 6 Research: [`.planning/phases/06-auto-provisioning-emergency-pod-vast-ai/06-RESEARCH.md`](../../.planning/phases/06-auto-provisioning-emergency-pod-vast-ai/06-RESEARCH.md)
- Spike (port mapping resolution): [`.planning/phases/06-auto-provisioning-emergency-pod-vast-ai/06-SPIKE-vast-port-mapping.md`](../../.planning/phases/06-auto-provisioning-emergency-pod-vast-ai/06-SPIKE-vast-port-mapping.md)
- HUMAN-UAT scenarios: [`.planning/phases/06-auto-provisioning-emergency-pod-vast-ai/06-HUMAN-UAT.md`](../../.planning/phases/06-auto-provisioning-emergency-pod-vast-ai/06-HUMAN-UAT.md)
- Wave-0 operator gates: [`.planning/phases/06-auto-provisioning-emergency-pod-vast-ai/06-WAVE0-GATES.md`](../../.planning/phases/06-auto-provisioning-emergency-pod-vast-ai/06-WAVE0-GATES.md)
- Validation matrix: [`.planning/phases/06-auto-provisioning-emergency-pod-vast-ai/06-VALIDATION.md`](../../.planning/phases/06-auto-provisioning-emergency-pod-vast-ai/06-VALIDATION.md)
- Sibling: [`RUNBOOK-FAILOVER.md`](./RUNBOOK-FAILOVER.md), [`RUNBOOK-QUOTAS-BILLING.md`](./RUNBOOK-QUOTAS-BILLING.md)
- Vast.ai API docs: <https://docs.vast.ai/> (note: canonical host changed
  to `https://console.vast.ai/api/v0` per spike doc)
- redsync (leader election): <https://pkg.go.dev/github.com/go-redsync/redsync/v4>
