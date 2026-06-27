# HANDOFF — Tier 3 (GPU/RAM/CPU no dashboard) + verificação vast_cost

**Escrito:** 2026-06-26 (sessão longa, checkpoint por contexto)

## Estado do "melhorar dados" — concluído nesta sessão
- ✅ Tier 1 página `/consumo` (custo/tokens/uso por tenant) — live
- ✅ Price-sync job (custo tenant real, daily timer ops-claude) — live
- ✅ Churn do pod (pre-warm flap, `ShouldStayUp`) — corrigido, **verificar amanhã**: `ssh n8n-ia-vm 'docker exec ifix-ai-gateway /gatewayctl primary lifecycles'` → count do dia deve ser ~1 (hoje foi só o 122; antes 6-14)
- ✅ Tier 2 página `/operacao` (FSM + lifecycles + breakers + custo Vast) — live
- ✅ vast_cost fix (commits 5e58696 + c890b97, deploy gateway sha b2fa04a): `calculatePrimaryCostBRL` agora usa `row.AcceptedDph` (não o param que vinha 0 do evaluateDestroying) + fallback `first_health_pass_at`→`started_at`. **Verificar amanhã**: pod fecha 17:00 → `/admin/operations` `vast_cost.month_brl` > 0 (hoje 122 fechou sob binário velho = 0).

## next_transition — NÃO era bug (campo é `next_transition_at`, funciona)

## TIER 3 — GPU/RAM/CPU (NÃO iniciado)
Objetivo: painel GPU util / VRAM / RAM / CPU do pod no `/operacao`.

**Achados (já investigado):**
- O pod tem **DCGM**: `markReady` (reconciler.go:858) recebe `urls.DCGM` e faz `DCGMScraper.SetURL(<dcgm>/metrics)` (porta :9400). Container separado `ifix-ai-pod-health-bridge`. Então o gateway JÁ tem um scraper DCGM apontado.
- **A pesquisar:** (a) o que o `DCGMScraper` coleta hoje e onde guarda (grep `DCGMScraper`/`dcgm` em gateway/internal/) — já agrega GPU util/mem? expõe via Prometheus/struct? (b) o pod expõe RAM/CPU além de GPU? (health-bridge :9100 só readiness; dcgm :9400 = GPU. RAM/CPU host pode não existir → talvez precise pod expor nvidia-smi + /proc, ou aceitar só GPU no v1). (c) se o scraper já tem os dados → só adicionar ao endpoint `/admin/operations` (campo `gpu{}`) + painel; se não → precisa o pod expor + gateway scrape.
- **Caminho provável:** estender `/admin/operations` com seção `gpu` (lida do DCGMScraper snapshot) + painel no `/operacao`. Se GPU já vem do scraper = só-leitura (barato). RAM/CPU pode ficar pra v2 se o pod não expõe.
- **Deploy:** gateway (rebuild contexto-raiz, ver memória `gateway-prod-build-deploy`) + dashboard.
- **Começar com:** `/gsd:quick --research` scopando o DCGMScraper (o que coleta, snapshot acessível no wiring tipo breakerSet, e se RAM/CPU existem) → plan → execute backend+frontend → 2 deploys.

## Outros follow-ups abertos
- ⚠️ **Rotacionar token OpenRouter** (vazado em vários docs do repo — `06.9-CONTEXT.md` etc)
- Economia vs OpenRouter (painel Vast real × phantom) — deferida (phantom é tenant-scoped, precisa query gateway-wide sem filtro)
- Metering audio_seconds/embeds (vêm 0 — não gravam)
- Backfill histórico de cost_brl=0 (lifecycles fechados sob código velho) — opcional, menor

Memórias relevantes: `gateway-prod-build-deploy`, `dashboard-cost-price-sync`, `primary-gpu-shape-06-8-final`.
