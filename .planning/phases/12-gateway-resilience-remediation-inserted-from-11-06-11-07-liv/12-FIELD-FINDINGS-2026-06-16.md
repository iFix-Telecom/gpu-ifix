# Phase 12 — Field Findings 2026-06-16 (post-verification, prod live)

**Captured:** 2026-06-16 ~11:10 BRT, prod gateway `ifix-ai-gateway` on n8n-ia-vm.
**Deployed build:** `ghcr.io/ifixtelecom/ifix-ai-gateway:main` rev **`99e4e09`** (created 2026-06-14T16:32Z — AFTER Phase 12 merge of 06-13, so RES-11/12/13 ARE live in this build).
**Trigger:** investigating a voice-api n8n flow ("dashboard sem dados" / LLM fallback question). Findings below are live field evidence layered on top of the PASSED 12-VERIFICATION.

Relates to [[SEED-011-fsm-ready-with-stopped-instance]], [[SEED-012-prober-ignores-tier0-override]], RES-11/RES-12/RES-13.

---

## 1. ✅ POSITIVE — tier-1 OpenRouter/deepseek confirmed HEALTHY & serving in prod

Graceful `gatewayctl primary force-down --reason openrouter_isolation_test` cleared the tier-0 override (drain → `RestoreTier0`). Two `/v1/chat/completions` requests sent immediately after, while FSM = `destroying`:

```
model:    deepseek/deepseek-v4-flash-20260423
provider: Novita
HTTP 200, cost ~$0.00001/req, real completion
```

The **failover target is healthy and serves correctly when reached.** RES-13's tier-1 fallthrough destination is validated live in prod (not just unit/integration). During the subsequent pod cold-start window, llm traffic is covered by OpenRouter — no outage.

## 2. ❌ RESIDUAL GAP — `openrouter-chat` (tier-1) probe is a FALSE-NEGATIVE; SEED-012 fix did not cover tier-1

`gatewayctl upstreams list` + `breaker list` (rev 99e4e09, prod):

```
upstream  openrouter-chat  probe=failed  LAST_PROBE_AT=2026-06-15T19:59:57Z (current)
breaker   openrouter-chat  OBSERVATION_closed
```

Yet the same upstream serves HTTP 200 (see finding 1). **The probe lies about a healthy tier-1 upstream.**

RES-12/SEED-012 fixed the **tier-0** case: prober now resolves tier-0 via `loader.Resolve(role,0)` (override-honoring) so `local-*` breakers stop flapping. It did **not** touch **tier-1** probing. So the "observability lies" class (SEED-012) still applies to `openrouter-chat` — `/v1/health/upstreams` + breaker/probe view cannot be trusted for tier-1.

**Likely mechanism (to confirm in Phase 12 follow-up):** the tier-1 probe issues a health call (e.g. GET /models or a HEAD) that OpenRouter rejects (auth/path/method) while POST `/v1/chat/completions` works. Real traffic UNAFFECTED (dispatcher path correct), but operators/monitors see a false `failed`.

**Action candidate:** extend the SEED-012 probe-truth fix to tier-1 upstreams, or change the openrouter-chat probe to a method the provider actually accepts.

## 3. ⚠️ OBSERVATION — FSM state-mirror under-populated

Before force-down, `gatewayctl primary state` showed:

```
state            ready
lifecycle_id     <empty>
pod_instance_id  <empty>
pod_url          <empty>
entered_at       1781524843  (2026-06-15T12:00:43Z = 09:00 BRT schedule UpHour)
```

— `ready` with EMPTY `lifecycle_id`/`pod_instance_id`/`pod_url` while a real Vast instance (41049940) was running and serving. The mirror is under-populated. Verify RES-11 D-05 `trackedID` repair actually writes lifecycle/instance into `gw:primary:state` on Ready; a blind mirror weakens operator death-diagnosis (the very thing RES-11 targets).

## 4. ✅ Clean teardown — no orphan

Force-down destroyed old instance 41049940; schedule (`Disabled: false`, mon-fri 9-17h BRT, "should be provisioned now: true") auto-re-provisioned a fresh pod (41196431, RTX 3090, $0.18/h). Vast instance count stayed at 1 throughout. No orphan, no money leak. Graceful drain → schedule re-up cycle works end-to-end.

## 5. 🔧 METHOD NOTE — breaker force-open is NOT a valid tier-1 failover trigger while primary override active

`breaker force-open --upstream local-llm --ttl 120s` then chat → request STILL served by the local pod (`model.gguf`, `system_fingerprint b9191-4f13cb742`). The **primary tier-0 override has precedence over the breaker**; forcing local-llm open does not divert traffic while the pod is alive.

To exercise tier-1 in prod, the override must be cleared: graceful `primary force-down` (drain path, tested here) or the death-detection path (RES-11). Operators reasoning about RES-13 should not expect breaker state to drive failover under an active primary override.

---

## Net for Phase 12

- RES-13 failover **destination** (tier-1 OpenRouter) is **proven good in prod** — positive evidence.
- One **residual "observability lies" gap** survives the Phase 12 closeout: tier-1 `openrouter-chat` probe false-negative (SEED-012 scope was tier-0 only). Candidate for a Phase 12 follow-up or a `/gsd:quick`.
- One **verification-worthy observation**: primary state-mirror showing `ready` with empty lifecycle/instance — confirm RES-11 D-05 mirror writes are landing in prod.
