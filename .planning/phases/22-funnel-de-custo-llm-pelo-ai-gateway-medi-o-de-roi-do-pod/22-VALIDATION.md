---
phase: 22
slug: funnel-de-custo-llm-pelo-ai-gateway-medi-o-de-roi-do-pod
status: approved
nyquist_compliant: true
wave_0_complete: false
created: 2026-07-19
---

# Phase 22 — Validation Strategy

> Fase de INFRA/OPS: "testes" = comandos empíricos (gatewayctl, psql SELECT, logs de container), não framework de teste. Toda validação é observável em prod (worker-vm) ou no DB `bd_ai_gateway`.
>
> **Convenção de projeto (evita falso-positivo Dimension 8):** os planos usam `<acceptance_criteria>` com comando+output esperado embutido, NÃO a tag `<verify><automated>`. É o mesmo padrão já aprovado na Phase 20 deste repo — funcionalmente equivalente (comando objetivo, não subjetivo). Não há framework de teste a instalar.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | Nenhum — validação empírica via CLI/SQL/logs em prod |
| **Config file** | none — sem suíte automatizada |
| **Quick run command** | `ssh worker-vm 'docker exec $(docker ps -q -f name=ai-gateway-prod_gateway\|head -1) /gatewayctl usage --tenant <slug>'` |
| **Full suite command** | `psql bd_ai_gateway -c "SELECT upstream, tenant, sum(cost_external_brl) FROM billing_events WHERE ts >= date_trunc('month',now()) GROUP BY 1,2"` |
| **Estimated runtime** | ~5s por comando |

---

## Sampling Rate

- **Após cada mudança (env key / price / upstream):** rodar o comando de validação do requirement afetado (abaixo)
- **Após cada wave:** SELECT em `billing_events` confirmando que o upstream/tenant esperado aparece com custo > 0
- **Antes de `/gsd:verify-work`:** `GET /admin/economy` retorna ROI com preços realistas (não R$0/subvalorizado)
- **Max feedback latency:** minutos (rollout gradual, observar prod entre passos)

---

## Per-Task Verification Map

| Req | Comportamento esperado | Test Type | Comando de prova |
|-----|------------------------|-----------|------------------|
| PRICE-01 | `prices` tem unit_cost_usd realista por (model,provider,unit); hot-reload sem deploy | smoke | `gatewayctl prices list` mostra preço novo; após 1 request, `billing_events.cost_external_brl` > valor irreal anterior |
| CV-01 | Roteamento STT via gateway ATIVO no runtime (não só env setada) | smoke | GATE: confirmar imagem do stack 15 contém código 113 (`grep gateway_key` no bundle rodando); depois log `gateway_enabled=True` + `gatewayctl usage --tenant converseai-stt` > 0 |
| CV-01 | classifier/format-hint/ai-match via gateway, uma por vez | smoke | `gatewayctl usage --tenant converseai-classifier` (e format-hint/ai-match) > 0 após UAT |
| CV-02 | Gemini (classifier/format-hint = R$238) roteado via gateway; upstream/alias resolve | smoke | `gatewayctl model-alias list` mostra alias gemini→upstream; `billing_events` mostra upstream gemini com custo; comportamento converseai preservado (UAT qualitativo) |
| CV-03 | followup worker TS chama gateway (model=qwen) | smoke | `gatewayctl usage --tenant <followup>` > 0; log do worker mostra base_url do gateway |
| MEASURE-01 | ROI mensal objetivo: external vs custo pod | query | `GET /admin/economy` retorna roi_multiplier/phantom_brl/vast_brl com preços PRICE-01; SQL sum(cost_external_brl) por upstream/tenant cruza |

*Status: ⬜ pending · ✅ green · ❌ red*

---

## Wave 0 Requirements

- [ ] Confirmar acesso a `ssh worker-vm` + `gatewayctl` + psql `bd_ai_gateway` (read) antes de qualquer mudança
- [ ] **GATE CV-01:** verificar se a imagem em execução no stack 15 (Portainer ep3) contém o código de roteamento da Phase 113 — se NÃO, re-landar o código é pré-requisito (achado da pesquisa: commits 113 estão dangling em converseai-v4, não em HEAD)

*Sem framework de teste a instalar — infra existente (CLI/SQL/logs) cobre a validação.*

---

## Manual-Only Verifications

| Comportamento | Req | Por que manual | Instruções |
|---------------|-----|----------------|------------|
| UAT qualitativo converseai (classifier/format-hint/ai-match respondem igual pós-gateway) | CV-01/CV-02 | Qualidade de resposta LLM não é assertável por comando | Enviar mensagens de teste, comparar saída antes/depois do roteamento |
| Rollback (esvaziar key + redeploy restaura fornecedor direto) | CV-01 | Requer redeploy Portainer | Esvaziar `*_GATEWAY_KEY`, redeploy stack 15, confirmar tráfego volta ao provider direto |

*Rollout gradual: uma key por vez, observar prod, rollback disponível.*

---

## Validation Sign-Off

- [x] Cada requirement tem comando de prova empírico (gatewayctl/SQL/log) — mapeado no Per-Task Verification Map
- [x] GATE PRICE-01 validado antes de qualquer conclusão de ROI — 22-01 wave 1, 22-07 depende dele
- [x] GATE CV-01 (imagem contém código 113) resolvido antes de setar env keys — 22-02 Task 1
- [x] Rollback documentado por passo do rollout — tasks CV-01 (esvaziar key + redeploy)
- [x] `nyquist_compliant: true` set após planos cobrirem todos os comandos acima
- [ ] `wave_0_complete` — flip após executar o GATE de acesso/runtime na execução

**Approval:** approved 2026-07-19 (plan-checker: 0 blockers)
