# Phase 5: Load Shedding (Saturation-aware Routing) - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-04-22
**Phase:** 05-load-shedding-saturation-aware-routing
**Areas discussed:** Sinal de saturação & thresholds, Fairness per-tenant (SC-4), Hysteresis FSM & interação com breaker, Política para edge cases

---

## Sinal de saturação & thresholds

### Q1: Qual regra de composição do sinal de saturação (inflight + P95 + VRAM)?

| Option | Description | Selected |
|--------|-------------|----------|
| 2-of-3 | ≥2 dos 3 sinais passam threshold simultaneamente (FEATURES.md recommended) | ✓ |
| OR-qualquer time-sustained | Qualquer sinal acima por ≥30s; mais sensível, exige hysteresis agressiva | |
| Inflight primário + confirmadores | Inflight ground-truth + P95/VRAM só confirmam casos marginais | |
| Weighted score | Score contínuo com pesos; flexível mas over-engineering para 6 tenants | |

**User's choice:** 2-of-3 (Recommended)
**Notes:** 2 de 3 sinais ortogonais força concordância antes de acionar — captura saturação real sem falsos positivos de spike único.

---

### Q2: Como medir P95 de latência por upstream?

| Option | Description | Selected |
|--------|-------------|----------|
| Ring buffer últimas N (~200) requests | In-memory lockless, sem deps, exato para esta réplica | ✓ |
| Prometheus histogram com window query | Reusa obs; overhead de self-scrape + buckets | |
| HDR/tdigest sliding 30s | Precisão p99/p99.9 alta; overkill para p95 6 upstreams | |
| EWMA | Média exponencial; reativo mas não é percentil literal | |

**User's choice:** Ring buffer últimas ~200 requests (Recommended)
**Notes:** Count-based estável sob baixa carga; sem dep externa; FEATURES.md recomenda padrão.

---

### Q3: Como ler VRAM do DCGM exporter do pod (:9400)?

| Option | Description | Selected |
|--------|-------------|----------|
| HTTP pull periodic | Goroutine 5s + cache + fail-open 2s | ✓ |
| Pull on-demand cached | Latência cold na 1ª req do ciclo; sem goroutine | |
| Pod publica em Redis | Mudança no pod + network extra | |
| Adiar VRAM para Phase 6+ | Viola LSH-02 literal | |

**User's choice:** HTTP pull periodic (Recommended)
**Notes:** Fail-open via timeout 2s evita fate-share; padrão idêntico ao probe goroutine Fase 3.

---

### Q4: Onde os thresholds de shedding ficam armazenados?

| Option | Description | Selected |
|--------|-------------|----------|
| Estender `upstreams.circuit_config` JSONB | Reusa trigger NOTIFY existente; zero plumbing novo | ✓ |
| Nova tabela `shedding_policies` | Normalizado mas duplica loader/listen/gatewayctl | |
| Novas colunas em `upstreams` | Query-friendly mas ALTER mais pesado | |
| Env vars + per-upstream override | Viola LSH-04 (requer restart) | |

**User's choice:** Estender `upstreams.circuit_config` JSONB (Recommended)
**Notes:** Phase 3 já deixou `circuit_config` pronta para Phase 5 popular; JSONB evolve é transparente ao loader.

---

## Fairness per-tenant (SC-4)

### Q5: Como alocar slots do local entre tenants durante saturação?

| Option | Description | Selected |
|--------|-------------|----------|
| Hard cap per-tenant + overflow tier-1 | Determinístico, fácil de explicar, tratável para 6 tenants | ✓ |
| Priority tiers com pre-emption | Mais fair mas FSM complexo; starvation risk | |
| Weighted fair-share (DRR/WRR) | Elegante mas "justiça matemática" conflita com SLA real | |
| Global cap + shed quem consome mais | Reativo vs preventivo | |

**User's choice:** Hard cap per-tenant + overflow tier-1 (Recommended)
**Notes:** 6 tenants conhecidos → config manual é tratável; priority_tier fica como metadata para runbook/dashboard.

---

### Q6: Onde armazenar limites/weight per-tenant?

| Option | Description | Selected |
|--------|-------------|----------|
| Colunas novas em `tenants` | Reusa canal `tenants_changed`; coerente com `rps_limit`/`rpm_limit` | ✓ |
| Nova tabela `tenant_inflight_limits` | Normalizada per-role mas duplica plumbing | |
| JSONB `shedding_config` em tenants | Flexível mas perde CHECK + sqlc type-safety | |

**User's choice:** Colunas novas em `tenants` (Recommended)
**Notes:** Consistente com padrão estabelecido na Fase 4.

---

### Q7: Tenant `sensitive` satura o local — como responder?

| Option | Description | Selected |
|--------|-------------|----------|
| 503 imediato com Retry-After | Honest; consistente com Fase 3 D-B4; sem handler hold | ✓ |
| Retry in-memory 4s (padrão Fase 3 D-B1) | Pega micro-spikes mas saturação não é blip | |
| Fila bounded in-memory com prioridade | Complexo; risco starvation | |

**User's choice:** 503 imediato com Retry-After (Recommended)
**Notes:** Saturação é condição sustentada (ARMED 30s + ON), diferente de breaker-open blip — aguardar 4s raramente resolve.

---

### Q8: Onde o shedding middleware entra na chain (Fase 4 D-D1)?

| Option | Description | Selected |
|--------|-------------|----------|
| Entre schedule e tokencount | Schedule decide tier inicial; shed override; tokencount sobre upstream final | ✓ |
| Dentro do dispatcher pre-breaker | Centraliza seleção; dispatcher gordo; tokencount desperdiçado | |
| Middleware dedicado após tokencount | Acoplamento extra; tokencount sempre roda | |

**User's choice:** Entre schedule e tokencount (Recommended)
**Notes:** Mantém chain declarativo; shed é "schedule override"; tokencount roda sobre upstream final.

---

## Hysteresis FSM & interação com breaker

### Q9: Qual máquina de estados para hysteresis?

| Option | Description | Selected |
|--------|-------------|----------|
| 4 estados: OFF→ARMED→ON→RECOVERING→OFF | Hysteresis nos dois caminhos; bate SC-2 + LSH-03 literal | ✓ |
| 3 estados: OFF→ON→RECOVERING→OFF | Sem ARMED; shed-flap em spikes <30s | |
| Leaky-bucket counter | Flexível mas menos legível | |

**User's choice:** 4 estados (Recommended)
**Notes:** ARMED aguarda 30s sustentado antes de ON; RECOVERING aguarda 60s limpo antes de OFF.

---

### Q10: Qual o escopo do FSM?

| Option | Description | Selected |
|--------|-------------|----------|
| Per-upstream global (3 FSMs) | Sinais VRAM/P95 são shared; inflight per-tenant no middleware | ✓ |
| Per-(upstream, tenant) | 18 FSMs sem benefício (sinais não-inflight são shared) | |
| Per-role global (1 por role) | Menos states mas perde distinção quando tier-0 diverge | |

**User's choice:** Per-upstream global (Recommended)
**Notes:** Future-proof para quando tier-0 divergir (STT em pod dedicado em v2, por exemplo).

---

### Q11: Como persistir estado do FSM?

| Option | Description | Selected |
|--------|-------------|----------|
| Redis mirror + Pub/Sub (pattern Fase 3 D-D1) | Cross-replica convergência <1s; Fase 6+7 sem refactor | ✓ |
| In-process only | Perde em restart; réplicas divergem | |
| DB-persisted | Overkill; estado reconstrói em 30s | |

**User's choice:** Redis mirror + Pub/Sub (Recommended)
**Notes:** `gw:shed:*` paralelo a `gw:breaker:*`.

---

### Q12: Precedência breaker × shed?

| Option | Description | Selected |
|--------|-------------|----------|
| Breaker wins | if breaker.open→t1; elif shed.on→t1; else t0 | ✓ |
| Shed wins durante HALF_OPEN | Evita flap mas complexidade no gobreaker callback | |
| Independentes (both checked, logged) | Sem precedência explícita; audit precisa distinguir | |

**User's choice:** Breaker wins (Recommended)
**Notes:** Upstream morto trumps upstream estressado; audit_log.upstream distingue causa.

---

### Q13: Hot-reload mid-shedding (FSM=ON)?

| Option | Description | Selected |
|--------|-------------|----------|
| Aplicar imediato + re-avaliar FSM | Preserva hysteresis (transita para RECOVERING se aplicável); bate SC-3 | ✓ |
| Aplicar imediato + reset FSM para OFF | Perde hysteresis se reload acidental | |
| Diferir até FSM sair de ON | Viola SC-3 (<2s literal) | |

**User's choice:** Aplicar imediato + re-avaliar FSM (Recommended)
**Notes:** Operador que precisa forçar shed off usa `gatewayctl shed-force --ttl 300s`.

---

## Política para edge cases

### Q14: Tier-1 também saturado/rate-limited durante shed?

| Option | Description | Selected |
|--------|-------------|----------|
| 503 `all_chat_upstreams_saturated` + Retry-After 30 | Consistente Fase 3 D-C4 (sem fallback-de-fallback chat) | ✓ |
| Forcar dispatch local mesmo saturado | Viola core value (latency degrada) | |
| Retry exp-backoff aguardando algum | Apps cliente têm retry próprio | |

**User's choice:** 503 `all_chat_upstreams_saturated` (Recommended)
**Notes:** STT e embed mantêm fallback Fase 3 independente.

---

### Q15: Shedding e streaming — decisão pre-dispatch only?

| Option | Description | Selected |
|--------|-------------|----------|
| Pre-dispatch only | Alinhado RES-05 e Fase 3 D-B4 | ✓ |
| Abort em-flight quando FSM transita | Pitfall 3: duplica output risk | |
| Não aplicar shedding a streams | Streams são a carga principal de Qwen | |

**User's choice:** Pre-dispatch only (Recommended)
**Notes:** Request em-flight continua no upstream original; FSM transita só afeta requests novas.

---

### Q16: Peak-off-hours interação com shedding?

| Option | Description | Selected |
|--------|-------------|----------|
| Shedding é noop em peak off-hours | Schedule middleware já selecionou tier-1 antes | ✓ |
| Shed executa para observabilidade | Métrica `decision='skipped_peak_offhours'` | |

**User's choice:** Shedding é noop em peak off-hours (Recommended)
**Notes:** Ordem da chain garante; métrica ainda incrementa para diferenciar em dashboard.

---

### Q17: Como marcar shedding no audit_log + Prometheus?

| Option | Description | Selected |
|--------|-------------|----------|
| Audit `upstream='shed_saturated'` + métrica com reason | Consistente com Fase 3 `blocked_sensitive` + Fase 4 `rate_limited` | ✓ |
| Audit mantém `upstream='openrouter-chat'` real dispatched | Menos visibilidade histórica | |
| Coluna nova `shed_reason` em audit_log | Migration pesada; low gain | |

**User's choice:** Audit `upstream='shed_saturated'` + métrica (Recommended)
**Notes:** 3 valores reservados: `shed_saturated`, `shed_blocked_sensitive`, `shed_tier1_unavailable`.

---

## Claude's Discretion

- Inflight counter implementação (atomic + map per-upstream/tenant)
- Ring buffer latência struct + serialização
- dcgmScraper package Prometheus text parser sem dep
- FSM tick goroutine (1s global)
- shedMiddleware plumbing
- gatewayctl subcommands (shed-state, shed-force, thresholds set, tenant set-shed-limits)
- Testes integration com mock upstream HTTP saturation-controllable
- Migrations 0016 (tenants shedding limits), 0017 (upstreams shed_* JSONB seed)

## Deferred Ideas

- Priority tier pre-emption real (Fase 10/v2)
- Autoscaling de cap per-tenant histórico (Fase 10+)
- Per-tenant FSM (v2)
- Abortar streams em-flight (rejeitado Pitfall 3)
- Fallback-de-fallback chat OpenAI (constraint PROJECT.md)
- HDR/tdigest para P95 (overkill)
- Nova tabela `shedding_policies` (JSONB existente suficiente)
- Weighted fair-share (hard caps suficientes para 6 tenants)
- JSONB `shedding_config` em tenants (colunas permitem CHECK)
- Retry in-memory 4s para sensitive saturado (saturação não é blip)
