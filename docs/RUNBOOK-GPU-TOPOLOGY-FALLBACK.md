# GPU Topology Fallback Runbook — Phase 06.8 (multi-pod sizing ladder)

**Phase:** 06.8 — multi-pod GPU topology + sizing + STT fix
**Scope:** how an operator falls back from the preferred **2×RTX 3090** single-pod shape to a **single RTX 5090** (and a placeholder for a future degraded split shape) when Vast inventory for the preferred shape dries up.
**Extends:** `docs/RUNBOOK-PRIMARY-POD-TTS.md` (Phase 06.7 — TTS-on-pod + 24/7 CPU embed) and `gateway/docs/RUNBOOK-PRIMARY-POD.md` (Phase 6.6 — FSM, schedule, `gatewayctl primary`, image-bump policy). This runbook documents ONLY the topology-shape ladder; for pod lifecycle / FSM / schedule / image-bump policy, read those runbooks first.

> **Wave-ordering note (this doc was written in wave 1).** The shape-A allowlist below is **seeded with the spike-validated host `43803`** (machine_id, validated 2026-05-21 — see memory `vast-multi-gpu-cdi-risk`). Additional live-validated 2×3090 machine_ids and the promoted STT-fix image digest are **amended into this runbook LATER**, as a post-validation operator follow-up (see [Amend-later checklist](#amend-later-checklist-post-wave-23)). Do **not** block on those amendments — `43803` is enough to bring shape A up today.

---

## 1. Why a runbook, not a config-driven orchestrator

The gateway is **single-primary**: one pod, one lifecycle, one reconciler. It already has everything it needs to stay resilient *within* a shape:

- **Allowlist → broaden** — `provisionLifecycle` searches the `PRIMARY_VAST_MACHINE_ALLOWLIST`-only set first (`machine_id:{in}`), then broadens to the full qualified search when allowlisted hosts are busy. Vast is a spot marketplace — no host can be reserved, so a hard filter would block provisioning; the broaden-fallback keeps the cheap-marketplace economics while still preferring trusted hosts.
- **`FilterBelowCap`** — drops offers above `PRIMARY_VAST_PRICE_CAP_DPH` (epsilon comparison).
- **Cooldown** — after a failed provision the FSM enters a cooldown before retrying, preventing oscillation.

Switching *between* shapes (e.g. 2×3090 inventory dries up → fall back to a 5090) is a **rare operational decision**, not a 1Hz control loop. Encoding it as a config-driven N-shape FSM (ordered preset list + "this shape is exhausted" detection + per-shape re-search) would add real complexity (RESEARCH §"Don't Hand-Roll" + §"Fallback Topology Ladder") for a transition that happens at human timescales. **A config-driven N-shape topology orchestrator is therefore DEFERRED to SEED-002** (if/when the emergency path becomes a full hot-standby, the shape-selection logic is better absorbed there).

So: the ladder is **a runbook + per-shape env presets**. Shape transition = swap the env preset, then `force-down` (if a pod is up on the old shape) → `force-up`.

---

## 2. Shape preset table

Each shape is a set of `PRIMARY_*` env values applied to the `ai-gateway-dev` gateway (Portainer stack UI or the gateway `.env`). Defaults are from `gateway/internal/config/config.go` (`PRIMARY_GPU_NAME` default `"RTX 5090"`, `PRIMARY_NUM_GPUS` default `1`, `PRIMARY_VAST_PRICE_CAP_DPH` default `2.20`, `PRIMARY_VAST_MACHINE_ALLOWLIST` default empty, `MONTHLY_PRIMARY_BUDGET_BRL` default `800.0`).

| Shape | `PRIMARY_GPU_NAME` | `PRIMARY_NUM_GPUS` | `PRIMARY_VAST_PRICE_CAP_DPH` | `PRIMARY_VAST_MACHINE_ALLOWLIST` (seed) | Approx cost |
|-------|--------------------|--------------------|------------------------------|------------------------------------------|-------------|
| **A — preferred** · 2×RTX 3090 single-pod (48 GB) | `RTX 3090` | `2` | `0.60` | `43803` | **~$0.29–0.40/h** (~$212–290/mo) |
| **B — fallback** · 1×RTX 5090 (32 GB) | `RTX 5090` | `1` | `2.20` | _(empty)_ | ~$0.74–2.00/h (~$540/mo) |
| **C — degraded** · 1pod-LLM + tiny-GPU services pod | — | — | — | — | — _(**DEFERRED**, see §6)_ |

Notes per shape:

- **Shape A (preferred).** 48 GB across two 3090s gives huge headroom: full stack ~27 GB (LLM Qwen 27B ~18 GB tensor-split across both GPUs via `llama.cpp -ngl 99`, no `CUDA_VISIBLE_DEVICES` pinning + TTS ~3.6 GB + whisper ~3 GB; embed is off-pod on CPU since 06.7 D-03). LLM ~42 tok/s (inter-GPU PCIe overhead vs ~77 tok/s single-5090 — acceptable for chat). 3090 inventory is **deeper and price-stable** (9–10 offers) than cheap-5090 (one offer, then a cliff). `PRICE_CAP=0.60` keeps the broaden-search from picking an expensive 5090 if no 3090 is below cap.
- **Shape B (fallback).** Single 5090 32 GB fits the same workload (UAT 17/18 validated). This is the gateway's coded default — applying shape B is mostly "clear shape A's overrides back to the defaults". Empty allowlist → search the open marketplace. `PRICE_CAP=2.20` covers EU 5090 inventory (cheapest ~$2.00/h Spain ES; UAT 18 found one at $0.7657/h).
- **Shape C (degraded).** A split topology (one pod runs only the LLM; a separate tiny-GPU pod — A4000 16 GB / 3060 12 GB — runs STT+TTS+embed). **Not specced in this phase** — it depends on **dual-primary role-split routing in the gateway**, which is itself Deferred (CONTEXT.md §Deferred). Placeholder only; design in a future phase. See §6.

`MONTHLY_PRIMARY_BUDGET_BRL` (default `800.0`) is the spend guard that backstops **every** shape — see §7.

---

## 3. Transition procedure (swap shapes)

Transitioning from one shape to another is a manual, explicit operator action. Example: shape A (2×3090) inventory has dried up → fall back to shape B (5090).

```bash
# 1. Apply the target shape's env preset.
#    Portainer: ai-gateway-dev stack → Environment variables → set
#      PRIMARY_GPU_NAME / PRIMARY_NUM_GPUS / PRIMARY_VAST_PRICE_CAP_DPH / PRIMARY_VAST_MACHINE_ALLOWLIST
#    then redeploy the stack so the gateway container restarts with the new env.
#    (Or edit the gateway .env and `docker compose ... up -d gateway`.)

# 2. If a pod is currently UP on the OLD shape, drain + destroy it first.
docker exec ai-gateway-dev_gateway /gatewayctl primary force-down --reason "shape_switch_A_to_B"
docker exec ai-gateway-dev_gateway /gatewayctl primary state      # wait until asleep / no live lifecycle

# 3. Bring a pod up on the NEW shape.
docker exec ai-gateway-dev_gateway /gatewayctl primary force-up --reason "shape_B_5090_fallback"
docker exec ai-gateway-dev_gateway /gatewayctl primary state      # wait Ready (4 endpoints healthy)
docker exec ai-gateway-dev_gateway /gatewayctl primary lifecycles # confirm offer picked matches the new shape
```

- **`force-up` bypasses cooldown** (`handleForceUpRequest`) — you do not have to wait out a prior failed-provision cooldown to switch shapes.
- The reconciler's allowlist→broaden + `FilterBelowCap` + cooldown handle **intra-shape** host unavailability automatically. This runbook covers only the rarer **cross-shape** switch.
- Cold-start: a fresh pod takes several minutes (weight download from MinIO + model load; Chatterbox adds ~30–40 s). Watch `gatewayctl primary state` until Ready.

---

## 4. When to fall back (the signal)

Fall back to the next shape down the ladder when the **preferred shape's inventory is dry**, i.e. the reconciler cannot find a qualifying offer even with a fresh `force-up`:

- **Primary signal:** repeated `provisionLifecycle` close with `no_offers_below_cap` despite a manual `force-up` — the qualified search returns zero offers for the current shape, or none below `PRICE_CAP`.
- Inspect with: `docker exec ai-gateway-dev_gateway /gatewayctl primary lifecycles` (look for the close reason) and the gateway logs (`ssh vps-ifix-vm 'docker logs ai-gateway-dev_gateway --tail 200 | grep -iE "no_offers|offer picked|provision"'`).

If shape A's search keeps returning `no_offers_below_cap` across several force-up attempts → swap to shape B per §3. (One-off allowlist-host-busy is **not** a reason to switch shapes — the broaden-fallback already handles that within the shape.)

> **Search-gate caveat (RESEARCH Pitfall 3 + Assumption A2):** the qualified search applies `cuda_max_good` / `driver_vers` gates. Confirm shape A actually *returns offers* through these gates before concluding the inventory is dry — a silent gate regression (analogous to UAT 18's 5090 `cuda_max_good` zero-offers bug) can masquerade as a dry shape. The shape-A search is validated live in Plan 03 (the A2 pre-check, with a pre-authorized one-line gate relax if it returns zero offers).

---

## 5. Image: which pod image each shape boots

All shapes boot the **same** primary-pod image (`PRIMARY_TEMPLATE_IMAGE`). The STT model-resolution fix (Phase 06.8 Plan 01/02) is image-level and applies to **every** topology.

```
PRIMARY_TEMPLATE_IMAGE = <TODO: set to the promoted STT-fix image digest>
```

> **TODO (amend-later):** point `PRIMARY_TEMPLATE_IMAGE` at the **promoted STT-fix image digest** from Plan 02's live validation (recorded in `06.8-STT-LIVE-VALIDATION.md` once that file exists — it is a wave-2 output and does NOT exist at this runbook's wave-1 write time). Until then, the gateway default (`ghcr.io/ggml-org/llama.cpp:server-cuda-b9191@sha256:cb375311…`) carries the LLM only and does **not** include the STT fix. See [Amend-later checklist](#amend-later-checklist-post-wave-23).

---

## 6. Shape C — DEFERRED placeholder

**Shape C (1pod-LLM + tiny-GPU services pod) is NOT specced in this phase.** It is recorded here only as a placeholder so the ladder is complete.

- **Why deferred:** Shape C splits roles across two pods (LLM on the primary pod; STT+TTS+embed on a separate tiny-GPU pod — A4000 16 GB / 3060 12 GB). That requires **dual-primary role-split routing in the gateway**, which is itself Deferred (CONTEXT.md §Deferred Ideas: "True dual-primary … → only if 2×3090 single-pod proves insufficient"; "Tiny-GPU services pod … part of the N-pods fallback, design later").
- **Dependency chain:** Shape C → dual-primary routing (Deferred) → only revisited if the 2×3090 single-pod (shape A) proves insufficient.
- **No env preset, no cost estimate, no procedure** is given here — those are produced when the dual-primary routing phase is designed.

---

## 7. Spend guard (every shape)

`MONTHLY_PRIMARY_BUDGET_BRL` (default `800.0`, separate from the emergency budget — Pitfall #12) backstops **all** shapes. It is sized with headroom for the preferred shape (2×3090 ~$0.40/h × ~14 h × 22 days ≈ R$130/mo → ~5× headroom) and **does not** get exceeded by shape A or B (shape A is *cheaper* than shape B). The explicit kill is always `gatewayctl primary force-down`.

Mitigations against an expensive shape running unattended (threat T-06.8-09):
- Each shape preset documents its `PRICE_CAP` — keep shape A's cap at `0.60` so the broaden-search cannot silently pick an expensive 5090.
- `MONTHLY_PRIMARY_BUDGET_BRL` caps cumulative spend.
- `gatewayctl primary force-down` is the explicit kill — Vast bills per-second, so destroy stops compute billing immediately.

---

## Amend-later checklist (post wave-2/3)

These items are **intentionally TODO** in this wave-1 runbook. Fold them in after the later waves complete (do NOT block this runbook on them):

- [ ] **Validated 2×3090 machine_ids** → append to shape A's `PRIMARY_VAST_MACHINE_ALLOWLIST` seed. Source: `06.8-GW-2GPU-LIVE-UAT.md` (Plan 03 output — a wave-3 file that does NOT exist yet). Today the seed is `43803` (spike-validated) only.
- [ ] **STT-fix image digest** → set `PRIMARY_TEMPLATE_IMAGE` (§5). Source: `06.8-STT-LIVE-VALIDATION.md` (Plan 02 output — a wave-2 file that does NOT exist yet).

---

## Cross-references

- `docs/RUNBOOK-PRIMARY-POD-TTS.md` — Phase 06.7 deltas (TTS on pod, 24/7 CPU embed, Pitfall #11 re-assert).
- `gateway/docs/RUNBOOK-PRIMARY-POD.md` — Phase 6.6 base (FSM, schedule, image-bump, `gatewayctl primary`).
- `.planning/phases/06.8-multi-pod-gpu-topology-sizing-stt-fix/06.8-RESEARCH.md` — §"Fallback Topology Ladder" (runbook-not-orchestrator rationale), §"Gateway 2-GPU provisioning" (the env knobs).
- `.planning/phases/06.8-multi-pod-gpu-topology-sizing-stt-fix/06.8-CONTEXT.md` — §Deferred Ideas (Shape C / dual-primary), gateway knobs landed (commit 6f57698).
- Memory `vast-multi-gpu-cdi-risk` — the 2×3090 spike (CDI, tensor-split, VRAM, perf, STT bug, host 43803).
