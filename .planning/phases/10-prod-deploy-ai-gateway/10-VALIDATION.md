---
phase: 10
slug: prod-deploy-ai-gateway
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-05-25
---

# Phase 10 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.
>
> Validation architecture is sourced from `10-RESEARCH.md` §Validation Architecture (lines 835–874). Planner fills the per-task map below from `10-PLAN.md` outputs.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | Go `testing` + testcontainers-go (Postgres + Redis) — already running in CI via `.github/workflows/build-gateway.yml`. Dashboard: vitest + `tsc --noEmit` + `npm run build`. |
| **Config file** | `gateway/go.mod` (Go deps); `dashboard/package.json` (vitest config) |
| **Quick run command** | `cd /home/pedro/projetos/pedro/gpu-ifix && go test ./gateway/... -count=1 -race -timeout=5m` (unit only) |
| **Full suite command** | `cd /home/pedro/projetos/pedro/gpu-ifix && go test -tags=integration ./gateway/... -count=1 -v -timeout=10m && (cd dashboard && npm run build && npx tsc --noEmit && npx vitest run)` |
| **Estimated runtime** | ~6–8 min full suite (≈90 s gateway integration, ≈3 min dashboard build, ≈90 s vitest) |

---

## Sampling Rate

- **After every task commit (Wave 0–2 autonomous plans):** `go test ./gateway/... -count=1 -race -timeout=5m`
- **After every plan wave:** Full suite command above
- **Before `/gsd:verify-work`:** Full suite green + HUMAN-UAT plan passed
- **Max feedback latency:** 5 min (quick), 10 min (full)

---

## Per-Task Verification Map

> Planner fills from `10-PLAN.md` per-task `<acceptance_criteria>` blocks. Cascade-close commits (Phase 02/03/04/05) verify via positive-assertion grep per WARNING-5 (Phase 06.9 pattern).

| Task ID | Plan | Wave | Requirement | Threat Ref | Secure Behavior | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|------------|-----------------|-----------|-------------------|-------------|--------|
| TBD     | TBD  | TBD  | TBD         | TBD        | TBD             | TBD       | TBD               | TBD         | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

- [ ] `gateway/docker-compose.prod.yml` — NEW; covers RESEARCH Pitfall 1 (network `intra`, not `worker_intra`)
- [ ] `gateway/.env.prod.example` — NEW; full prod env contract (gateway + dashboard + redis)
- [ ] `/home/pedro/projetos/pedro/infra/traefik-dynamic/ai-gateway-prod.yml` — NEW; edge file-provider route mirroring `n8n-ia.yml`
- [ ] `gateway/docs/RUNBOOK-DEPLOY.md` — NEW; mirrors RUNBOOK-FAILOVER.md header structure (Triggers/Preconditions/Steps/Verification/Rollback/Postmortem)
- [ ] Capacity gate: `ssh n8n-ia-vm 'free -h; df -h /var/lib/docker; docker network ls; docker info | grep -i swarm; curl -s ifconfig.io'` — record observed values in plan
- [ ] Internal-Traefik discovery probe: deploy ephemeral hello-world container on `intra` overlay with Traefik labels; observe `docker service logs traefik-internal_traefik` for router-added line. If absent, switch internal Traefik to dual-provider (Swarm + Docker) — small Wave 0 patch.

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| HUMAN-UAT scenario S1 — golden chat on `ai-gateway.converse-ai.app` | INT-06 + Cascade Phase 02 SC-5 | Live deploy requires operator-owned DNS + Sentry project + secret population | See `10-HUMAN-UAT.md` step 1 |
| HUMAN-UAT scenario S2 — rollback timed < 5 min | INT-06 | Real container swap + `docker compose up -d` requires the live host | See `10-HUMAN-UAT.md` step 2 |
| HUMAN-UAT scenario S3 — TLS cert valid + DNS resolves | PRD-07 | LE issuance only happens after DNS propagates + edge Traefik reload | See `10-HUMAN-UAT.md` step 3 |
| HUMAN-UAT scenario S4 — force-open primary breaker → tier-1 fallback | Cascade Phase 03 SC-1 | Needs the live gateway + live OpenRouter call | See `10-HUMAN-UAT.md` step 4 |
| HUMAN-UAT scenario S5 — rate-limit headers + 429 under burst | Cascade Phase 04 SC-1 | Multi-tenant key + Redis burst on prod | See `10-HUMAN-UAT.md` step 5 |
| HUMAN-UAT scenario S6 — `billing_events` row inserted | Cascade Phase 04 SC-2 | Live psql + real chat round-trip | See `10-HUMAN-UAT.md` step 6 |
| HUMAN-UAT scenario S7 — peak off-hours routes to openrouter-chat | Cascade Phase 04 SC-4 | Real-time clock + tenant schedule mode | See `10-HUMAN-UAT.md` step 7 |
| HUMAN-UAT scenario S8 — vegeta burst → ≥99% 200s (overflow) | Cascade Phase 05 SC-1 | Concurrent live traffic load | See `10-HUMAN-UAT.md` step 8 |
| Cascade-close commits — positive-assertion grep | Phase 02/03/04/05 VERIFICATION.md flip | Four separate commits — one per phase closed | See `10-HUMAN-UAT.md` cascade-close step (WARNING-5 pattern) |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency < 600s
- [ ] `nyquist_compliant: true` set in frontmatter (planner flips after per-task map filled)

**Approval:** pending
