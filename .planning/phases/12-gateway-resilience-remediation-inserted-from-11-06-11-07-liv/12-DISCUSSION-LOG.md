# Phase 12: gateway-resilience-remediation - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-06-12
**Phase:** 12-gateway-resilience-remediation
**Areas discussed:** Política pós-morte FSM (RES-11), Fallthrough tier-1 (RES-13), Parity prober (RES-12), Validação chaos + CAP-01

---

## Política pós-morte FSM (RES-11)

| Option | Description | Selected |
|--------|-------------|----------|
| Asleep + schedule decide | Loop de schedule re-provisiona naturalmente se na janela; zero retry novo; billing-stop não loopa | ✓ |
| Re-provision imediato | Provisioning na hora; em billing-stop entraria em provision-fail loop | |
| Re-provision só se não-billing | Classificação de causa vira lógica crítica nova | |

**User's choice:** Asleep + schedule decide (Recomendado)

| Option | Description | Selected |
|--------|-------------|----------|
| 3-strike pra ambos | Mesmo contador do 404 (01e7558) pra exited/stopped; ~15s extra elimina falso-positivo | ✓ |
| Imediato pra exited/stopped | Reage no 1º tick; risco de exited transiente | |
| 2-strike | Meio-termo ~10s | |

**User's choice:** 3-strike pra ambos (Recomendado)

| Option | Description | Selected |
|--------|-------------|----------|
| Critical via alerter existente | Fan-out Chatwoot + ClickUp + Brevo; título distinto pra billing-stop | ✓ |
| Critical só billing, warning yank | Diferencia urgência | |
| Só log + métrica | Billing-stop às 3h passa despercebido | |

**User's choice:** Critical via alerter existente (Recomendado)

| Option | Description | Selected |
|--------|-------------|----------|
| Abrir deterministicamente | FSM sabe que pod morreu; elimina janela; breaker = verdade do FSM | ✓ |
| Observação natural | Mantém princípio observation-driven puro; janela coberta por RES-13 | |
| Você decide | | |

**User's choice:** Abrir deterministicamente (Recomendado)

---

## Fallthrough tier-1 (RES-13)

| Option | Description | Selected |
|--------|-------------|----------|
| Connection-class só | refused/no-route/DNS/dial-timeout — pré-byte, retry 100% seguro | ✓ |
| Connection + response timeout | Risco de tool-call duplicado | |
| Qualquer erro de transporte | Máxima cobertura, máximo risco de duplicação | |

**User's choice:** Connection-class só (Recomendado)

| Option | Description | Selected |
|--------|-------------|----------|
| Stream também, se dial falhou | Dial fail é pré-byte SSE; retry invisível; nunca pós-stream-iniciado | ✓ |
| Só non-stream | Manteria parte dos 502 (chat é majoritariamente stream) | |

**User's choice:** Stream também, se dial falhou (Recomendado)

| Option | Description | Selected |
|--------|-------------|----------|
| Cascateia toda a chain | Loop tier_priority ASC; 502 só quando chain esgotar | ✓ |
| 1 fallthrough só | OpenRouter down + pod down = 502 mesmo com OpenAI saudável | |
| Cascateia com timeout global | Budget de tempo somado; complexidade extra | |

**User's choice:** Cascateia toda a chain (Recomendado)

| Option | Description | Selected |
|--------|-------------|----------|
| Sim, registra no breaker | Fallthrough = ponte, breaker = estado estável | ✓ |
| Não, só fallthrough | Cada request paga dial timeout até prober abrir | |
| Você decide | | |

**User's choice:** Sim, registra no breaker (Recomendado)

---

## Parity prober (RES-12)

| Option | Description | Selected |
|--------|-------------|----------|
| Não proba overridden | Probe via Resolve(role,0) só no tier-0 efetivo; zero ruído | ✓ |
| Proba ambos, breaker separado | Em prod reproduz o flap "esperado" | |
| Você decide | | |

**User's choice:** Não proba overridden (Recomendado)

| Option | Description | Selected |
|--------|-------------|----------|
| Reset/force-close no markReady | Estado OPEN herdado é stale; simétrico ao force-open na morte | ✓ |
| Deixa half-open resolver | Janela ~cooldown com tráfego válido caindo pra tier-1 | |
| Você decide | | |

**User's choice:** Reset/force-close no markReady (Recomendado)

| Option | Description | Selected |
|--------|-------------|----------|
| Tier-0 efetivo + flag override | Health = verdade do tráfego real; rows estáticas como standby | ✓ |
| Só tier-0 efetivo | Menos contexto pro operador | |
| Você decide | | |

**User's choice:** Tier-0 efetivo + flag override (Recomendado)

| Option | Description | Selected |
|--------|-------------|----------|
| Manter gating D-A2 | Zero probe cost em steady-state; sinal de tier-1 morto vem do fallthrough | ✓ |
| Baseline lento no tier-1 | Detecta key expirada antes do incidente; muda contrato D-A2 | |
| Você decide | | |

**User's choice:** Manter gating D-A2 (Recomendado)

---

## Validação chaos + CAP-01

| Option | Description | Selected |
|--------|-------------|----------|
| Dev primeiro, depois prod | Valida fixes barato em dev, prod como gate final zero-502 | (base) |
| Direto em prod | Um kill só; se fix falhar vira debug session | |
| Só dev, prod confia | Requisito fala de chaos em prod | |

**User's choice (free-text):** "dev primeiro, mas antes de escolher qual o pod, o preço deve ser o 1º fator de escolha."
**Notes:** Pod do chaos UAT selecionado por preço PRIMEIRO — kill é destrutivo, offer mais barata qualificada ganha (allowlist/blocklist ainda aplicam).

| Option | Description | Selected |
|--------|-------------|----------|
| Doc-only nesta phase | Analisa dados 11-06, escreve decisão; implementação é phase futura | ✓ |
| Split pra phase própria | Phase 12 = só RES-11/12/13 | |
| Doc + implementa cap simples | Risco de inchar a phase | |

**User's choice:** Doc-only nesta phase (Recomendado)

| Option | Description | Selected |
|--------|-------------|----------|
| Sim, junto do RES-11 | Mesmo código (state hash vs routing table); operador depende nos runbooks | ✓ |
| Não, deferred | Cosmético | |
| Só se for trivial | Planner investiga | |

**User's choice (display bug gatewayctl primary state):** Sim, junto do RES-11 (Recomendado)

| Option | Description | Selected |
|--------|-------------|----------|
| Zero 502 connection-class | Nenhum upstream_unreachable; 503 sensitive_block esperado; latência degradada OK | ✓ |
| Budget <1% 502 | Tolera residuais mid-flight | |
| Zero 502 + p95 SLO | Tier-1 pode não cumprir p95 5s — gate falharia fora do escopo | |

**User's choice:** Zero 502 connection-class (Recomendado)

---

## Claude's Discretion

- Shape do código de classificação de morte no Ready tick (reuso do recover path vs extração comum)
- Detecção connection-class no Go e ponto de interceptação no proxy
- Buffering de body pra retry (multipart STT) — limites e implementação
- Shape do payload `/v1/health/upstreams` (campos da flag override)
- Métricas Prometheus novas
- Ordem dos plans e waves

## Deferred Ideas

- Implementação da decisão CAP-01 (cap/queue/shape) — phase futura se exigir código
- Baseline periódico de probe tier-1 — revisitar se incidente de tier-1 morto-sem-detecção
- Re-provision imediato pós-host-yank — revisitar se MTTR do schedule loop for lento em incidente real
- Todo `260522-allowlist-steering-bug.md` revisado mas não foldado (já RESOLVED 2026-05-22)
