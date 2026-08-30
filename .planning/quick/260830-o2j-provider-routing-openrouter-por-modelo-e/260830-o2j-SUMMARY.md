---
phase: quick-260830-o2j
status: complete
completed: 2026-08-30
commits: [49b8be9, 60e0a87, 060c581, 4d258fc]
deployed:
  gateway: "ghcr.io/ifixtelecom/ifix-ai-gateway:develop-4d258fc@sha256:cffdcf7e (stack 38)"
  dashboard: "ghcr.io/ifixtelecom/ifix-ai-dashboard:develop-4d258fc@sha256:364a28d7 (stack 40)"
  migration: "0037_model_aliases_provider_prefs (bd_ai_gateway v37)"
---

# quick 260830-o2j — Provider routing OpenRouter por tenant/modelo + controle total de pod/LLM no ai-dashboard

## Decisões (Pedro, 2026-08-30)

1. Precedência do `provider` OpenRouter: **tenant > alias (openrouter-chat) > env global**.
2. Gateway sempre vence o `provider` mandado pelo cliente.
3. Controles do pod validados em prod só até o dialog de confirmação (sem force real).
4. `claude.ops` promovido a owner SÓ durante a validação; revertido a operator no fim.
5. Fora de escopo: start/stop do pod 3060 (timers), edição estrutural do pod.

## Entregue

**Gateway (49b8be9 + 060c581 + 4d258fc)**
- Migration 0037: `model_aliases.provider_prefs` + `tenants.provider_prefs` (jsonb NULL), trigger `tenants_update_notify` recriado com a coluna.
- `models.ValidateProviderPrefs` (schema OpenRouter estrito, canônico) + cache no Resolver + `tenants.Loader.ProviderPrefsByTenantID`.
- `openrouter_director`: injeta objeto verbatim (tenant > alias), senão pin legado do env.
- Admin API: `GET/PUT /admin/model-aliases`, `DELETE /admin/model-aliases/{alias}/{upstream}` (refresh imediato), `PUT /admin/tenants/{slug}/provider-prefs`, `GET /admin/upstreams`, `POST /admin/upstreams/{name}/enabled` (409 último do role), `POST /admin/primary/force-up|force-down`.
- gatewayctl: `model-alias set --provider-prefs|--clear-provider-prefs`, `tenant set-provider-prefs`.
- Testes: unit (models/proxy/admin/tenants) + integration verde no CI (HEAD bump Down(n) 0026/0029).

**Dashboard (60e0a87)**
- `/modelos`: aliases agrupados + editor estruturado de provider prefs (preview JSON) + upstreams com switch.
- `/tenants/gerenciar`: "Roteamento OpenRouter (tenant)" + dialog.
- `/operacao`: card "Controle do pod" (Ligar/Desligar, AlertDialog + motivo), página virou RSC + client.
- 5 server actions owner-gated com audit; `gateway-admin` ganha PUT/DELETE; vitest 26/26 no arquivo, tsc limpo.

## Validação em prod (FATOS)

| Cenário | Resultado |
|---|---|
| `deepseek-flash` (sem prefs) | `provider: Novita` (pin global) |
| alias `qa-provider-test` `only:[deepinfra]` via API | `provider: DeepInfra` |
| tenant `uat10-test` `only:[baidu]` + mesmo alias | `provider: Baidu` (tenant vence) |
| tenant limpo (`null`) | volta a `DeepInfra` |
| PUT prefs inválidas (`quantizations:[q4]`) | 400 |
| UI editar alias (only=baidu, zdr, p90=3) → Salvar | API reflete; audit `model_alias.upsert`; OpenRouter 404 ZDR (Baidu não é ZDR — política aplicada) |
| UI excluir alias → confirm | 26 rows; audit `model_alias.delete` |
| UI desligar `kokoro-tts` (único tts) | toast 409 "único upstream"; enabled permanece true |
| UI Ligar/Desligar pod → dialogs | renderizam; Cancelar; 0 eventos publicados; pod `asleep` |
| UI tenant "Editar roteamento" | dialog + editor renderizam |

Rollout gateway: gap de /health ≈60s (stop-first), leadership reacquired, 26 aliases / 21 tenants carregados.

## Gotchas registrados (memória `openrouter-provider-prefs-and-dashboard-control`)

- `.gitignore` `gatewayctl` engolia arquivo novo em `cmd/gatewayctl/` → corrigido `/gatewayctl`.
- Migration nova → HEAD bump dos `db.Down(n)` nos testes 0026/0029.
- `zdr:true` com provedor não-ZDR → 404 do OpenRouter.
- ops-claude "Cannot fork" = cgroup `pids.max` 2338 estourado por Chromes órfãos do playwright-mcp.
- Cosmético: botão destrutivo do AlertDialogAction rende verde (override de classe não aplica).

## Deviation

Plano escrito e executado direto pelo orquestrador (pesquisa já feita); sem planner/executor subagents.
