# SEED-002 — Emergency = full primary-pod hot-standby + embed back on pod (per-role tier-1 fallbacks)

**Status:** captured 2026-05-21 (during 06.7 live UAT). Future planned phase.
**Origin:** operator decision during 06.7-09 UAT — emergency LLM-only fallback leaves STT dead and TTS degraded during a primary-pod outage; a 4090 cannot fit the full 4-service stack ("não cabe").

## Problem

Today (Phase 6 emergency + 06.7):
- **Emergency pod is LLM-only** (`EMERGENCY_TEMPLATE_IMAGE` = llama.cpp + Qwen, 4090, cap 0.40). On a primary-pod outage it overrides only the `llm` role.
- Consequence during an outage (see 06.7-HUMAN-UAT.md S-table): **LLM survives** (emerg pod), **embed survives** (CPU 24/7), but **STT dies** (no emerg STT, OpenAI Whisper tier-1 unset) and **TTS degrades** to Piper (no clone).
- **embed** was moved OFF the pod to CPU-only static tier-0 (06.7 D-03), so it never uses GPU even when the pod is up.

## Target design (locked with operator 2026-05-21)

**Single pattern for all roles — GPU pod is primary, always-on fallback is tier-1:**

| Role | tier-0 (GPU pod, primary) | tier-1 (always-on fallback) |
|------|---------------------------|------------------------------|
| LLM  | pod | OpenRouter (`UPSTREAM_LLM_OPENROUTER_URL`) |
| STT  | pod | OpenAI Whisper (`UPSTREAM_STT_OPENAI_URL`) |
| TTS  | pod Chatterbox | voice-api Piper (already wired) |
| embed| **pod GPU** | **CPU 24/7 Infinity (n8n-ia-vm:7997)** |

- **Emergency pod becomes a FULL primary-pod hot-standby**: same image (`PRIMARY_TEMPLATE_IMAGE` = converseai-primary-pod, 4-service supervisord), same weights (Qwen+whisper+chatterbox+bge), same onstart, **5090**, same price cap (1.00), same machine blocklist + offer filter (no verified, no geo) + machine_id logging as primary. Only the TRIGGER differs (emerg = breaker-driven failover; primary = scheduled/force-up).
- **embed returns to the pod** as tier-0 (GPU, fast) — **reverses 06.7 D-03**. The standalone 24/7 CPU Infinity (n8n-ia-vm) becomes embed tier-1, so embed still answers when the pod sleeps/falls.

## Scope of change (touch list)

- `gateway/internal/emerg/lifecycle.go` `buildCreateRequest` + offer search: use `PrimaryTemplateImage`, all `PRIMARY_*` weights, primary onstart, `PrimaryGPUName` (5090), `PrimaryVastPriceCapDPH`, `PrimaryVastMachineBlocklist`, machine_id logging — i.e. mirror the primary provisioner exactly (consider extracting a shared provision-config struct so emerg and primary cannot diverge).
- Emergency now overrides ALL dynamic roles (llm/stt/tts + embed) on activation, not just llm.
- **Fix the latent `emergency_pod_<role>` proxy gap for llm + stt** (TTS already fixed in 06.7-07 via `NewDynamicTTSProxy`) — llm/stt resolve to `emergency_pod_llm`/`emergency_pod_stt` but no proxy is registered, so they 503 when actually pod-routed. Register dynamic override-aware proxies for them (mirror NewDynamicTTSProxy).
- embed: pod embed tier-0 (back in the pod image/supervisord), CPU 24/7 → tier-1. Update `ai_gateway.upstreams` rows + reconciler override roster (add embed back to the dynamic-override map) + `UPSTREAM_EMBED_URL` semantics (CPU becomes tier-1, pod becomes tier-0 via override).
- Set the external tier-1 envs in dev/prod (`UPSTREAM_LLM_OPENROUTER_URL`, `UPSTREAM_STT_OPENAI_URL`) so LLM/STT have a real always-on fallback when both pods are down.
- Cost: emergency now ~$0.6–1.0/h (full 5090) vs ~$0.15–0.40 (4090 LLM-only) — accepted for full service parity during outage.

## Open questions for planning

- Budget guard: `MONTHLY_EMERGENCY_BUDGET_BRL` (200) likely needs raising for a full 5090 emerg.
- Does emerg ever need to coexist with primary (both 5090) — double GPU spend during the cutover window? Confirm the cutback timing (Pitfall #11 re-assert) still holds with embed in the override roster.
- DCGM/health gating: full emerg pod must pass all 4 health endpoints (not just LLM) before EmergencyActive.

## Cross-refs

- 06.7-HUMAN-UAT.md (live UAT results 5/6 PASS + the bug log that surfaced this)
- 06.7-CONTEXT.md D-03 (embed→CPU; this seed reverses it)
- SEED-001 (emergency pod template vast vs custom image)
