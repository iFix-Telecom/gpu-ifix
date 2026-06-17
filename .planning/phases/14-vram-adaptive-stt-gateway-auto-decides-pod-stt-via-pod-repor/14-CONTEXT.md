# Phase 14: vram-adaptive-stt — Context

**Gathered:** 2026-06-17
**Status:** Ready for planning
**Source:** Operator decision (live session) + SEED-019

<domain>
## Phase Boundary

Make the gateway decide STT-from-pod automatically and shape-agnostically,
removing the manual `PRIMARY_POD_SERVE_STT` env flag (commit 4021901, currently
hand-set `false` in prod for the 1×3090 shape). SEED-019 parts 2+3 ONLY.

**IN scope:**
- Part 2 (pod): VRAM-adaptive whisper device + pod self-reports `whisper_device`.
- Part 3 (gateway): gateway reads the reported device at pod-Ready and gates the
  STT tier-0 override on it; `Config.PrimaryPodServeSTT` deleted.

**OUT of scope:**
- SEED-019 part 1 (N-shape cascade 2×3090→5090→3090) — separate phase.
- Cold-start fail-fast / provisioning hang (Phase 6.6.Y / 12) — separate.
- Any change to the tier-1 STT cascade (gemini-stt → groq-whisper → openai-whisper).
</domain>

<decisions>
## Implementation Decisions (LOCKED — do not revisit)

### Signal channel: pod self-reports device (operator chose this over shape-derivation or latency-probing)
- The pod is the source of truth for whether its whisper runs on GPU. It reports
  `whisper_device` (`cuda` | `cpu`, or an equivalent `stt_on_gpu` boolean) and the
  gateway routes on that. Shape-agnostic: works for any future shape (1×3090,
  2×3090, 5090, …) with NO VRAM math in the gateway.
- **Rejected:** gateway deriving from provisioned shape (fragile VRAM modeling);
  latency-based dynamic routing (overkill, probe cost).

### Part 2 — pod VRAM-adaptive whisper
- `onstart.sh` detects total VRAM via `nvidia-smi --query-gpu=memory.total` (no
  nvidia-smi call exists today) BEFORE `docker compose up -d` (~onstart.sh:86).
- Device decision plumbed as a CONTAINER ENV via the existing `--env-file`
  mechanism (onstart.sh:101-104). The build-baked `supervisord.conf:46`
  `WHISPER__INFERENCE_DEVICE="cpu"` pin is REMOVED — value comes from env now.
  Keep `HF_HUB_CACHE="/weights/whisper"` (the conf:46 line carries both vars).
- Threshold: VRAM ≥ ~30GB (2×3090=48, 5090=32) → whisper on GPU; < 30GB (1×3090=24)
  → whisper NOT on GPU (disabled / not advertised as STT-ready). Final threshold +
  the disabled-vs-CPU choice for <30GB is Claude's discretion in the plan, but a
  24GB pod MUST NOT serve slow-CPU whisper that the gateway then routes to.
- On GPU shapes, pin whisper `device_index` to the GPU WITH headroom (NOT the one
  holding Qwen) to dodge CUDA OOM — this is why the original CPU pin existed.
- Pod surfaces `whisper_device` in the health-bridge `aggregateResponse`
  (`pod/health-bridge/handlers.go:58-79`, served on :9100). Speaches' own
  `:8001/health` is 3rd-party and CANNOT carry the field.

### Part 3 — gateway conditional override
- Gateway probes the health-bridge `:9100/health/ready` aggregate at pod-Ready to
  read `whisper_device`, stores it as a new field on `primaryPodURLs`
  (`gateway/internal/primary/lifecycle.go:112-117`).
- The 3 reconciler override sites (`reconciler.go:448`, `:875`, `:1631`) swap their
  `r.cfg.PrimaryPodServeSTT` gate for a per-lifecycle "pod serves whisper on GPU"
  boolean derived from the reported device. Clear paths (`startDrain`,
  `closeLifecycle`) already call `RestoreTier0("stt")` unconditionally — safe, no change.
- `Config.PrimaryPodServeSTT` (config.go:252 + env load :517) is DELETED. Remove
  `PRIMARY_POD_SERVE_STT` from prod `.env` as part of rollout.
- Existing tests `reconciler_test.go:642-717` + `lifecycle_test.go:52` are reworked
  to drive off the device signal instead of the config flag.

### Migration / rollback safety
- Until the pod reports the field (old pod image), the gateway must default safely:
  treat MISSING/unknown `whisper_device` as "do NOT override STT → use gemini-stt"
  (fail-safe to the cheap-fast tier-1, never the slow CPU pod). This preserves
  today's prod behavior during the pod-image rollout window.
</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### SEED-019 source
- `.planning/seeds/SEED-019-multi-pod-shape-cascade-vram-adaptive-stt.md` — full decision + 3-part split.
- `.planning/seeds/SEED-019-IMPL-SURFACES.md` — exact file:line code-surface map (research output).

### Pod surfaces
- `pod/onstart.sh` (:86 pre-compose, :100-105 compose up, :107-130 readiness gate)
- `pod/primary/supervisord.conf:44-82` (speaches block; :46 CPU pin to remove)
- `pod/health-bridge/handlers.go:58-79` (aggregateResponse — add whisper_device), `probes.go:121-174` (probeSTT)

### Gateway surfaces
- `gateway/internal/primary/reconciler.go` (:443-456 site A, :871-878 site B, :1629-1634 site C; buildPodURLs :1532/:1595)
- `gateway/internal/primary/lifecycle.go:112-117` (primaryPodURLs struct), `:482-503` (pod*URL probes)
- `gateway/internal/config/config.go:252` + `:517` (PrimaryPodServeSTT — delete)
- `gateway/internal/upstreams/loader.go:361-412` (OverrideTier0/RestoreTier0/Tier0OverrideURL), `:320` (STT tier-1 cascade)
- Tests: `gateway/internal/primary/reconciler_test.go:642-717`, `lifecycle_test.go:42-52`
</canonical_refs>

<specifics>
## Specific Ideas

- Live-proven baseline: flag `PRIMARY_POD_SERVE_STT=false` already deployed (rev
  30f4c81) → STT routes to gemini at ~2.0-2.3s HTTP 200 (vs pod CPU ~17s). This
  phase makes that routing automatic + bidirectional (GPU pod → use pod).
- Validation must include the 1×3090 (STT→gemini) AND a VRAM-capable shape
  (2×3090 or 5090: STT→pod on GPU, <~5s, no CUDA OOM) — real Vast UAT.
</specifics>

<deferred>
## Deferred Ideas
- N-shape cascade (SEED-019 part 1).
- Cold-start fail-fast destroy (Phase 6.6.Y/12) — surfaced live this session
  (host 7970 stuck `loading` ~37min before manual destroy).
</deferred>

---

*Phase: 14-vram-adaptive-stt*
*Context gathered: 2026-06-17 via operator decision*
