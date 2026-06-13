# SEED-012 — Prober uses loader.All() and ignores tier0Override → permanent breaker flap in prod

**Discovered:** 2026-06-12 during 11-06 live load-test UAT.
**Severity:** HIGH — observability lies: `/v1/health/upstreams` returns 503 `status=failed` and `local-*` breakers flap open/half-open forever even with a perfectly healthy primary pod. Real traffic is UNAFFECTED (dispatcher path is correct), but operators and monitors cannot trust breaker state or the health endpoint.

## Mechanism

- `gateway/internal/upstreams/probe.go:160` — probe tick enumerates upstreams via `p.loader.All()`.
- `loader.All()` (`gateway/internal/upstreams/loader.go:358`) returns the raw DB snapshot (`s.ordered`) — it does NOT consult `l.tier0Override`.
- `loader.Resolve(role, 0)` (`loader.go:222`) DOES consult the override and returns the synthetic `emergency_pod_<role>` upstream pointing at the live pod URL.
- Prod static env rows point at dead addresses (`UPSTREAM_LLM_URL=http://10.10.10.20:8000`, STT :8001 — nothing listens there; n8n-ia-vm has no pod services).
- Result: probes dial the dead static URLs → fail → breakers `local-llm`/`local-stt`/`local-tts` open. Half-open trials re-probe the same dead URL → re-open. Forever.

## Live evidence (2026-06-12)

- Pod healthy: instance 40697682 (1×5090), direct `/health` 200 on all 3 service ports from both ops-claude and n8n-ia-vm.
- Real chat request via gateway: 200, `model.gguf`, served by pod (llama.cpp fingerprint), 1370ms.
- Simultaneously: `gatewayctl breaker list` shows local-llm/stt/tts `OBSERVATION_open`, flapping open↔half-open every ~40s for 25+ min; `/v1/health/upstreams` → 503 `status=failed`.

## Knock-on effects

1. 11-07 chaos plan's "breaker opens by natural observation" gate is unverifiable in prod (breaker already open pre-chaos).
2. Tier-1 probe gating (D-A2: probe tier-1 only when tier-0 open/half-open) burns OpenRouter/OpenAI probe cost continuously, since tier-0 always looks open.
3. Any future alerting on breaker state or health endpoint = permanent false alarm.

## Expected behavior

Probe tick should resolve each role's tier-0 through the SAME path the dispatcher uses (`Resolve(role, 0)` honoring tier0Override), or `All()` should overlay active overrides. Either way prober and dispatcher must agree on what tier-0 IS.

## Related

- SEED-011 (FSM ready-with-stopped-instance) — together these mean: FSM blind to instance death + prober blind to instance life. Both feed the Phase 12 reconciler/observability remediation.
- 11-06-EVIDENCE.md (2026-06-12 appendix) — full live documentation.
