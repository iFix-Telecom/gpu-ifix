# Phase 11: prod-hardening - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-05-27
**Phase:** 11-prod-hardening
**Areas discussed:** Load test methodology (PRD-01), Chaos tests (PRD-02 + PRD-03), Full incident runbook (PRD-04), Dashboard SSO (PRD-06), LGPD scope (PRD-05), Phase 10 deferred fold, Per-env upstream key separation

---

## Initial Area Selection

User selected ALL 4 gray areas to discuss:
- Load test methodology (PRD-01)
- Chaos tests (PRD-02 + PRD-03)
- Full incident runbook (PRD-04)
- Dashboard SSO (PRD-06)

User also selected scope-split items: PRD-05 LGPD = doc-only, bundle Phase 10 deferred items here, per-env upstream key separation (D-08) fold em Phase 11.

---

## Load Test (PRD-01)

### Profile

| Option | Description | Selected |
|--------|-------------|----------|
| Replay audit_log | Export 24h audit_log production, replay com timing original. Máxima fidelidade. Exige parser + sanitize PII | ✓ |
| Sintético calibrado | Vegeta/k6 mix derivado de Prometheus métricas. Faster setup, não cobre edge cases reais | |
| Híbrido | Sintético base + injetar 10-20 audit_log RIDs reais por minuto | |

**User's choice:** Replay audit_log.

### Tier Coverage

| Option | Description | Selected |
|--------|-------------|----------|
| Tier-1 only (sem Vast spend) | local-llm + local-stt FORCED_OPEN; tudo via OpenRouter/OpenAI/Infinity | |
| Tier-0 + tier-1 mix | Vast primary UP, ~$1-3 spend; valida Phase 5 SC-1/SC-2 live | ✓ |
| Tier-0 only | Force tier-1 OFF; testa headroom puro do pod | |

**User's choice:** Tier-0 + tier-1 mix.

### Duration

| Option | Description | Selected |
|--------|-------------|----------|
| 30 min sustained | Replay 30min peak hour real ~14-15 BRT. Vast ~$0.30 | ✓ |
| 2h soak | Cobre cooldown + rate-limit reset + audit_log GB. Vast ~$1.20 | |
| Step-load 5→20→50 RPS | Ramp pra encontrar joelho da curva | |

**User's choice:** 30 min sustained.

### Pass Criteria

| Option | Description | Selected |
|--------|-------------|----------|
| P95 + error rate | P95 chat ≤5s + embed ≤1s + STT ≤10s + error <1% non-503 + zero 5xx panic. SLO v1.0 oficial | ✓ |
| Invisible-failover assertion | Foco PRD-02: zero 5xx durante kill primary | |
| Cost-per-1k-requests | PASS = custo/req ≤ X. Útil pra billing planning | |

**User's choice:** P95 + error rate (SLO v1.0 oficial).

### Tool

| Option | Description | Selected |
|--------|-------------|----------|
| Python asyncio + httpx | Script custom em scripts/integration-smoke/load-replay.py. Reusa pattern smoke-*.py | ✓ |
| Vegeta + targets file | Rate-shaped mas perde timing original | |
| k6 (golang JS engine) | Output HTML rico mas sem precedente no repo | |
| locust | Web UI live, modelo user class, mais cerimônia | |

**User's choice:** Python asyncio + httpx.

### Load Source

| Option | Description | Selected |
|--------|-------------|----------|
| ops-claude VM | 10.10.10.10, 4c/16G, NAT via 162.55.92.154 → edge Traefik. Realista, sem competir CPU | ✓ |
| Laptop via Tailscale | Variabilidade bandwidth polui métricas | |
| GHA runner self-hosted | Compete com dev stack por CPU em vps-ifix-vm | |

**User's choice:** ops-claude VM.

### Replay Source

| Option | Description | Selected |
|--------|-------------|----------|
| dev audit_log atual | ~mil requests UAT 06.6-10. Suficiente | ✓ |
| ai_gateway_prod audit_log | Só ~30min de tráfego pós-deploy. Insuficiente | |
| Sintético inflar dev export | Duplicar/shuffle pra atingir duration | |

**User's choice:** dev audit_log atual.

---

## Chaos Tests (PRD-02 + PRD-03)

### PRD-02 Kill Primary Mechanism

| Option | Description | Selected |
|--------|-------------|----------|
| Vast API DELETE raw | curl -X DELETE durante load. Natural breaker observation, não force-open. Realista | ✓ |
| gatewayctl primary force-down | Caminho operator-graceful drain. Já testado Phase 10 S10. Menos canonical pra invisible | |
| supervisord SIGKILL llama PID | Pod vivo mas LLM 404. Mais granular | |

**User's choice:** Vast API DELETE raw.

### PRD-03 OpenRouter Down Simulation

| Option | Description | Selected |
|--------|-------------|----------|
| iptables DROP egress | Regra no n8n-ia-vm. Timeout natural, breaker abre por observação | ✓ |
| FORCED_OPEN breaker | Trivial mas não testa trip natural. Phase 10 S4 já valida force-open | |
| Fake DNS via /etc/hosts | Connection-refused fast. Reduz wall-time | |

**User's choice:** iptables DROP egress.

### Sensitive Tenant Proof

| Option | Description | Selected |
|--------|-------------|----------|
| Fix smoke-sensitive-failover.py | Aceitar FORCED_OPEN como equiv natural-open. Race condition resolvida | ✓ |
| Manual curl probe documentado | Replicar Phase 10 S9 manual; documentar como golden no runbook | |

**User's choice:** Fix smoke-sensitive-failover.py.

---

## Incident Runbook (PRD-04)

### Postmortem Template

| Option | Description | Selected |
|--------|-------------|----------|
| Google SRE blameless | Summary/Impact/RC/Trigger/Detection/Resolution/AI/Timeline/Lessons | ✓ |
| 5-whys simplificado | Mais rápido mas pode ficar superficial em multi-causa | |
| Linear/Jira ticket-style | Bom se workflow Ifix usar Linear. Não usado visivelmente | |

**User's choice:** Google SRE blameless.

### Incident Classes (multi-select)

| Option | Description | Selected |
|--------|-------------|----------|
| Primary pod down | Vast yank / supervisord crash / GPU OOM | ✓ |
| OpenRouter / OpenAI degraded | tier-1 down; sensitive 503 sensitive_block | ✓ |
| Audit/billing pipeline broken | Phase 10 viveu (UTF8 0x8b gzip pitfall) | ✓ |
| Rate-limit / quota lockout tenant | Tenant atingiu limit; mitigation operator | ✓ |

**User's choice:** Todas 4 classes.

---

## Dashboard SSO (PRD-06)

### Hardening Layers (multi-select)

| Option | Description | Selected |
|--------|-------------|----------|
| TOTP 2FA obrigatório | better-auth twoFactor plugin. Enroll + backup codes. 4 admins | ✓ |
| Email allowlist | signUp restrict @ifixtelecom.com.br domain. Trivial | ✓ |
| Rate-limit + brute-force | better-auth rateLimit plugin OR middleware Redis Lua | ✓ |
| Session hardening | Idle 30min, SameSite=strict, IP bind opcional | ✓ |

**User's choice:** Todas 4 layers.

---

## Scope Split

### PRD-05 LGPD + Phase 10 Deferred + Per-env Keys

| Option | Description | Selected |
|--------|-------------|----------|
| PRD-05 LGPD = doc-only | Processo sign-off + template carta. External gate jurídico | ✓ |
| Bundle Phase 10 deferred items aqui | smoke-sensitive fix, gatewayctl debug emit-error, gatewayctl key list, GHA retrigger | ✓ |
| Per-env upstream key separation (D-08) | OR + OpenAI keys novas env=prod | ✓ |

**User's choice:** Todos os 3 (LGPD doc-only + Phase 10 deferred fold + per-env keys).

### Phase 10 Deferred Items Detail (multi-select)

| Option | Description | Selected |
|--------|-------------|----------|
| smoke-sensitive-failover.py fix | FORCED_OPEN equiv natural-open | ✓ |
| gatewayctl debug emit-error | Panic-recovery path canonical proof | ✓ |
| gatewayctl key list | Operator inspection | ✓ |
| v1.0.0 GHA build retrigger workflow | Bug: tag push não disparou build por dedup | ✓ |

**User's choice:** Todos 4.

### Per-env Upstream Key Separation Decision

| Option | Description | Selected |
|--------|-------------|----------|
| Fold em Phase 11 | Criar OR+OpenAI keys novas label env=prod. ~30 min operator + doc | ✓ |
| Adiar pra v1.1 | D-08 trade-off aceito; Phase 11 já tem 6 PRDs + 4 deferred | |

**User's choice:** Fold em Phase 11.

---

## Claude's Discretion

- Estrutura interna do `load-replay.py` (orchestrator class vs func-style; uvloop vs default asyncio loop).
- Cadência exata de cleanup post-chaos (iptables -D timing, Vast pod re-spin se kill prematuro).
- Onde armazenar audit_log export sanitizado (gitignored `.planning/load-test-fixtures/` ou off-repo MinIO).
- TOTP issuer name + algorithm specifics (SHA1 vs SHA256).
- Ordem exata dos plans (load-test antes de chaos? bundle deferred items em plan separado ou intercalado?).
- Spend cap operator Phase 11 = $5 absoluto (load $1-3 + chaos $0.50 + contingency).

---

## Deferred Ideas

- Grafana + Prometheus stack completa → v2.
- Multi-region / GeoDNS → v2.
- Caching semântico / shadowing / canary → v2.
- Portainer Repository migration → revisit operator burden.
- CI tag pattern v1.0.x patch releases → Phase 11+ futuro.
- SSO Google Workspace Ifix → quando n>10 admins emergir.
- IP-bind sessão admin obrigatório → flag operator desligada por default.
- `gatewayctl key list` filtros avançados → v1.1.
- Audit_log export pipeline automatizado (cold-storage) → revisit se compliance requirement emergir.

---

*User typed during discussion:* "como vamos gerar carga ? de teste ?" — answered via Python asyncio + httpx + ops-claude + dev audit_log replay decisions above.
