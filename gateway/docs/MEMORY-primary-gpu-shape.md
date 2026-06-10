# MEMORY (repo-local mirror) — Primary GPU shape, canonical env names

> **OPERATOR-SYNC NOTE.** This repo-local mirror is **authoritative for the canonical
> env migration (Phase 6.6.Y)**. The external auto-memory file
> `~/.claude/projects/-home-pedro-projetos-pedro-gpu-ifix/memory/primary-gpu-shape-06.8-final.md`
> is **outside the repo writable root** and must be **manually synced by the operator**.
> Operator: copy the canonical guidance below into that external file (replacing the stale
> legacy-var block) so the pre-primary-UAT env check teaches canonical names.

## Standing shape — what "primary" actually means

Phase 06.8 settled on **2×RTX 3090 single-pod, 48 GB total** as the validated standing
shape for the primary pod (Qwen3.x-27B-Q4 + STT + embed + TTS co-resident; llama
auto-tensor-splits). NOT 5090 (Phase 06.6 UAT 18 default) and NOT 4090 (stale compose
defaults referenced outdated 4090 numbers).

**Critical naming fact:** the standing 2×3090 config is the **FALLBACK shape (shape 1)**,
NOT the primary. The old deprecated num-gpus alias (value `2`) mapped to
`PrimaryVastNumGPUsFallback` (config.go:217). After the Phase 6.6.Y canonical migration
the shape pair is:

| Shape | Role | Canonical vars | Value |
|-------|------|----------------|-------|
| shape 0 | PRIMARY (searched first) | `PRIMARY_VAST_NUM_GPUS_PRIMARY` / `PRIMARY_VAST_GPU_NAME_PRIMARY` / `PRIMARY_VAST_PRICE_CAP_PRIMARY` | `1` / `RTX 3090` / `0.30` |
| shape 1 | FALLBACK (standing config) | `PRIMARY_VAST_NUM_GPUS_FALLBACK` / `PRIMARY_VAST_GPU_NAME_FALLBACK` / `PRIMARY_VAST_PRICE_CAP_FALLBACK` | `2` / `RTX 3090` / `0.60` |

Allowlist hosts `43803,55158` are the LIVE-UAT-proven gateway-provisioned 2×3090
single-pods (both in `06.8-VERIFICATION.md` 05-T1/T3) and belong to the **FALLBACK** shape.

## Canonical standing operator config (gateway stack.env)

```
PRIMARY_VAST_NUM_GPUS_PRIMARY=1
PRIMARY_VAST_NUM_GPUS_FALLBACK=2
PRIMARY_VAST_GPU_NAME_PRIMARY=RTX 3090
PRIMARY_VAST_GPU_NAME_FALLBACK=RTX 3090
PRIMARY_VAST_PRICE_CAP_PRIMARY=0.30
PRIMARY_VAST_PRICE_CAP_FALLBACK=0.60
PRIMARY_VAST_MACHINE_ALLOWLIST=43803,55158
PRIMARY_VAST_REJECT_PRIVATE_IP=true
PRIMARY_PUBLIC_PORT_BIND_BUDGET_SECONDS=120
```

## HARD-FAIL — legacy vars now refuse boot (Phase 6.6.Y-02)

The deprecated unprefixed primary aliases (the old num-gpus / gpu-name / dph-cap names)
now **HARD-FAIL gateway boot** — the gateway refuses to start if any are present in the
stack.env. The previous caveat that "compose default 0.40 is outdated / falls back to
5090" no longer applies: the compose defaults are the canonical per-shape vars and the
legacy aliases are deleted from every surface.

**Pre-primary-UAT env check (use CANONICAL names):**

```
ssh vps-ifix-vm 'grep -E "PRIMARY_VAST_(NUM_GPUS|GPU_NAME|PRICE_CAP)_(PRIMARY|FALLBACK)|PRIMARY_VAST_MACHINE_ALLOWLIST" /path/to/stack.env'
```

If the canonical per-shape vars are missing, the gateway will boot on the compose
`:-` defaults (shape0 1×3090@0.30 / shape1 2×3090@0.60) — verify the allowlist is set
before any UAT so the FALLBACK pass targets the proven 43803/55158 hosts. Env drift was
discovered 2026-05-22 during td #4 UAT (Phase 06.8 deploy never updated the stack.env);
always verify before primary UAT. See `vast-multi-gpu-cdi-risk` for CDI/host-fault
patterns on multi-GPU inventory.

## Why 2×3090 (not 4090 / 5090)

24 GB on a single 4090 cannot fit Qwen-27B-Q4 + KV cache + whisper-large-v3 GPU + embed
(UAT 06.6 #16 OOM). 32 GB 5090 fits but costs more than 2×3090 ($0.60 cap vs 5090 single
$0.65–2.00/h). Phase 06.8 settled 2×3090 as the production primary (FALLBACK shape);
SEED-002 emergency hot-standby will mirror.
