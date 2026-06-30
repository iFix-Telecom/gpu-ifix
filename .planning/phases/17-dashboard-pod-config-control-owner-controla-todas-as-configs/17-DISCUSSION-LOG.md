# Phase 17: Dashboard pod-config control - Discussion Log

> **Audit trail only.** Do not use as input to planning, research, or execution agents.
> Decisions are captured in CONTEXT.md — this log preserves the alternatives considered.

**Date:** 2026-06-30
**Phase:** 17-dashboard-pod-config-control-owner-controla-todas-as-configs
**Areas discussed:** Escopo dos campos editáveis, Força do confirm, Painel ao vivo, Onde audita, Bounds (A3)

---

## Escopo dos campos editáveis (Área 1)

| Option | Description | Selected |
|--------|-------------|----------|
| Read-only display, sem editar | Estruturais só exibidos; hot = únicos editáveis | ✓ |
| Editáveis com confirm+restart | Form completo p/ os 35 campos | |
| Subconjunto editável | Shape editável, images/weights read-only | |

**User's choice:** Read-only display, sem editar.
**Notes:** Estrutural fica env-only read-only. Editar SHA de weights em form web é raro+perigoso.

### Follow-up (escopo derivado)

| Option | Description | Selected |
|--------|-------------|----------|
| Só hot no DB, sem restart endpoint | pod_config = 16 hot; estrutural env-only; self-restart fora | ✓ |
| Todos no DB, mantém restart endpoint | 35 no DB, restart p/ uso futuro | |
| Só hot no DB, mantém restart endpoint | 16 hot; restart vira utilitário operacional | |

**User's choice:** Só hot no DB, sem restart endpoint.
**Notes:** Phase 17 encolhe — foca na dor real (hot-reload blocklist/cap/schedule). Mata Pitfall 2 e 5.

---

## Força do confirm em perigosos (Área 2)

| Option | Description | Selected |
|--------|-------------|----------|
| Dialog simples + warning específico | Modal 1-clique, texto carrega o impacto | ✓ |
| Type-to-confirm nos críticos | Digitar valor/CONFIRMAR em cap-down + schedule | |
| Só warning inline, sem modal | Banner inline + salvar direto | |

**User's choice:** Dialog simples + warning específico.
**Notes:** Confirm é UX anti-fat-finger; segurança real é requireOwner server-side.

---

## Conteúdo do painel ao vivo (Área 3)

| Option | Description | Selected |
|--------|-------------|----------|
| FSM + lifecycle atual + event trail | Estado + lifecycle aberta com trail/shutdown_reason | ✓ |
| Só FSM state + pod URL | Mínimo, não diagnostica o flap | |
| FSM + histórico recente completo | + tabela N-últimas com custo | |

**User's choice:** FSM + lifecycle atual + event trail.
**Notes:** Cobre o diagnóstico do flap bad-host sem virar tela de histórico.

---

## Onde audita (Área 4)

| Option | Description | Selected |
|--------|-------------|----------|
| Dashboard admin_audit_log apenas | Reusa tabela Phase 13, dashboard-side | ✓ |
| Dual-write também no gateway audit | Visão unificada; quebra isolamento | |

**User's choice:** Dashboard admin_audit_log apenas.
**Notes:** Preserva isolamento Phase 13 (dashboard nunca toca schema ai_gateway).

---

## Bounds de validação (A3)

| Option | Description | Selected |
|--------|-------------|----------|
| Aceitar todos travados | Ranges research viram CHECK hardcoded | |
| Ajustar o cap | Mudar range do price cap | ✓ (→ refinado) |
| Revisar vários | Revisar mais de um bound | |

**User's choice (refinado):** Bounds NÃO hardcoded — **todos os bounds viram editáveis no dashboard**.
**Notes:** Owner pediu cap como variável no dashboard; expandiu p/ todos os bounds editáveis.
Seed = defaults research, depois owner redefine o envelope. Operator read-only. ~Dobra a superfície de config.

### Cascata da decisão de bounds

| Pergunta | Resposta |
|----------|----------|
| Ajustar cap → qual range? | "Mudar piso também" → "isso deve ser uma variável no dashboard" |
| Só cap ou todos? | **Todos os bounds editáveis** |

---

## Claude's Discretion

- Storage dos bounds (colunas `*_min`/`*_max` vs tabela `pod_config_bounds`).
- Schema `pod_config` single-row vs key-value.
- Limpeza do env morto `PRIMARY_POD_SERVE_STT`.
- Layout/UX exato dos forms e painel.

## Deferred Ideas

- Self-restart endpoint + edição de campos estruturais (shape/image/weights) — fase futura.
- Histórico de lifecycles com custo/tendência no painel.
- Dual-write no gateway `/admin/audit` (audit unificado).
- SEED-020 allowlist policy (preference vs hard-filter) — mudança de lógica, não config-control.
