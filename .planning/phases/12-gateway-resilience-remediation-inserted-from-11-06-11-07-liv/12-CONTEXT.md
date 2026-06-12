# Phase 12: gateway-resilience-remediation - Context

**Gathered:** 2026-06-12
**Status:** Ready for planning

<domain>
## Phase Boundary

Corrigir os 3 gaps de resiliência provados ao vivo nos UATs 11-06/11-07 (2026-06-12) para que o gateway prod sobreviva à morte do primary pod autonomamente e suas superfícies de health digam a verdade:

1. **RES-11 (SEED-011):** Ready-tick do reconciler cego a instância morta — FSM ficou `ready` 25+ min com pod billing-stopped (`actual_status=exited`) e depois DELETE real. Portar a classificação do startup recover path (`primary recover: instance not running; closing as orphan`) pro tick steady-state: 404 3-strike (de 01e7558) E detecção exited/stopped; avançar Ready → Draining → Asleep + BestEffortDestroy; alert crítico distinto pra billing-stop.
2. **RES-12 (SEED-012):** prober resolve tier-0 via `loader.All()` (`gateway/internal/upstreams/probe.go:160`) que ignora `tier0Override` — breakers `local-*` flapam pra sempre em prod e `/v1/health/upstreams` retorna 503 com pod saudável. Probe tick deve resolver tier-0 pelo mesmo `Resolve(role, 0)` do dispatcher.
3. **RES-13:** dispatcher só cai pra tier-1 quando breaker OPEN — dial failure (connection-refused class) com breaker CLOSED gera 502 direto. 11-07 chaos: 100× HTTP 502 `upstream_unreachable` em T+0..60s com OpenRouter saudável e CLOSED. Dispatcher deve fall through pra tier-1 quando o dial tier-0 falha.
4. **CAP-01 (stretch, doc-only):** baseline de saturação do 11-06 (chat p95 21.7s @ concurrency 50 em 1×5090) → documento de decisão queue-depth/concurrency-cap/shape.

**Out of scope:** implementação da decisão CAP-01 (se exigir código, vira phase futura); capabilities novas de routing; mudanças no shape prod (1×5090 cap $1.50 mantido).

</domain>

<decisions>
## Implementation Decisions

### RES-11 — Política pós-morte do FSM
- **D-01 (pós-detecção):** Após morte confirmada: Ready → Draining → Asleep + BestEffortDestroy, e **o loop de schedule existente decide** o re-provisionamento (re-provisiona naturalmente se dentro da janela de pico). Zero lógica nova de retry. Billing-stop não entra em loop de provision-fail (sem crédito falharia de novo).
- **D-02 (confirmação):** **3-strike pra ambos os sinais** — mesmo contador/padrão do 404 (01e7558) aplicado a `actual_status in {exited, stopped}` (ou `intended_status=stopped`). Vast reporta `exited` transiente em alguns cenários; ~15s de latência extra elimina falso-positivo.
- **D-03 (alert):** **Critical via pacote `alert` existente** (severity critical → fan-out Chatwoot + ClickUp + Brevo). Billing-stop com título distinto ("Vast account sem crédito — primary billing-stopped"); morte normal (host yank/404) também alerta, com causa diferente. Zero infra nova de alerting.
- **D-04 (breakers):** Na detecção confirmada, FSM **abre `local-llm`/`local-stt`/`local-tts` deterministicamente** — elimina a janela de requests indo pra endereço morto enquanto observação acumula. Estado breaker = verdade do FSM. Combinado com RES-13, dispatcher cai pra tier-1 instantaneamente.
- **D-05 (display bug):** Bug secundário do SEED-011 (`gatewayctl primary state` mostra `pod_url`/`lifecycle_id` vazios com proxy ainda roteando — state hash vs routing table fora de sync) **entra no escopo**, colado no trabalho do reconciler. Operador depende desse comando nos runbooks de incidente.

### RES-13 — Fallthrough tier-1 em dial failure
- **D-06 (classes de erro):** **Connection-class só** — connection refused, no route, DNS fail, dial timeout: erros ANTES de qualquer byte chegar ao upstream. Retry 100% seguro (request nunca foi processada). Timeout de resposta e 5xx continuam com a observação do breaker como hoje (princípio tool-call no-retry do Phase 3 preservado).
- **D-07 (streaming):** **Streams (SSE) caem também quando o dial falha** — dial fail é pré-byte, retry no tier-1 é invisível pro cliente. NUNCA retry depois que o stream começou (headers/chunks já enviados) — que de qualquer forma não é connection-class.
- **D-08 (cascade):** Se o tier-1 escolhido também falhar dial, **cascateia a chain inteira** no mesmo loop `tier_priority` ASC (Phase 11.2 D-B5′): registra falha no breaker do candidato → tenta próximo CLOSED. 502 só quando a chain esgotar.
- **D-09 (observação):** Dial failure no tier-0 **registra como falha no breaker `local-*`** além de disparar o fallthrough — breaker abre naturalmente após N dials, requests seguintes pulam o dial morto. Fallthrough = ponte; breaker = estado estável.
- **D-10 (sensitive — carried forward RES-08, LOCKED):** Sensitive tenants (telefonia, cobrancas) NUNCA caem pra tier-1 externo. Dial failure tier-0 pra sensitive = HTTP 503 `upstream_unavailable_for_sensitive_tenant`, como hoje.

### RES-12 — Parity prober/dispatcher
- **D-11 (resolução):** Probe tick resolve tier-0 via **`Resolve(role, 0)`** — mesmo path do dispatcher, honrando `tier0Override`. Prober e dispatcher concordam sobre o que tier-0 É.
- **D-12 (rows overridden):** Com override ativo, **prober NÃO proba as rows estáticas tier-0 substituídas** (URLs mortas em prod tipo `10.10.10.20:8000`) — breakers delas não flapam. Quando override cai, volta a probá-las.
- **D-13 (reset no markReady):** Quando pod fica Ready (override ativado), **force-close/reset dos breakers `local-*`** que estavam OPEN por probar URL morta — estado herdado é stale por definição (pod acabou de passar nos probes de provisioning). Simétrico ao force-open na morte (D-04).
- **D-14 (health semantics):** `/v1/health/upstreams` lista o **tier-0 efetivo + flag de override** ("override ativo, origem: primary pod"); rows estáticas substituídas aparecem como standby/overridden sem afetar o status agregado. Health = verdade do tráfego real.
- **D-15 (gating D-A2 mantido):** Com tier-0 saudável, probes de tier-1 (OpenRouter/OpenAI) PARAM — era a intenção original do D-A2, o flap do SEED-012 é que quebrava. Primeiro sinal de tier-1 morto vem do fallthrough RES-13 (D-09 registra no breaker).

### Validação chaos + CAP-01
- **D-16 (sequência):** **Dev primeiro, depois prod como gate final.** Dev-stack valida os 3 fixes com kill barato; só então re-run da receita 11-07 em prod (1×5090) esperando zero-502. Spend total ~$0.80-1.50.
- **D-17 (escolha de pod chaos — user-specified):** Na escolha do pod pro chaos UAT, **preço é o 1º fator de seleção** — kill é destrutivo, não precisa de shape premium; a offer mais barata qualificada ganha (allowlist/blocklist continuam aplicando, mas custo manda).
- **D-18 (gate):** **Zero 502 connection-class** durante T+0..fim do chaos — nenhum `upstream_unreachable`. 503 `sensitive_block` continua esperado (RES-08). Latência degradada durante failover OK (sem gate de p95 nesse UAT).
- **D-19 (CAP-01):** **Doc-only nesta phase** — analisa dados do 11-06, escreve decisão (cap de concorrência / queue / shape) em documento. Implementação vira phase futura se exigir código.

### Claude's Discretion
- Shape exato do código de classificação de morte no Ready tick (reuso direto do helper do recover path vs extração comum).
- Detecção da connection-class no Go (`net.OpError`/`syscall.ECONNREFUSED` matching, etc.) e onde intercepta no proxy.
- Buffering de body pra retry (multipart STT) — limites e implementação.
- Shape do payload `/v1/health/upstreams` (nomes de campos da flag override).
- Métricas Prometheus novas pros paths de fallthrough/death-detection.
- Ordem dos plans e divisão por waves.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Seeds (definição canônica dos bugs)
- `.planning/seeds/SEED-011-fsm-ready-with-stopped-instance.md` — FSM ready com instância morta: timeline live, root cause hypothesis, expected behavior, repro sketch, display bug secundário.
- `.planning/seeds/SEED-012-prober-ignores-tier0-override.md` — mecanismo exato (probe.go:160 `All()` vs loader.go:222 `Resolve`), evidência live, knock-on effects (D-A2 burn, chaos gate unverifiable).

### Evidência live Phase 11 (UATs que provaram os gaps)
- `.planning/phases/11-prod-hardening/11-06-EVIDENCE.md` — saga load-test 2026-06-12: billing-stop, 25+ min FSM ready, breaker flap, apêndice com ambos os seeds.
- Artefatos do plan 11-07 (chaos kill primary) na mesma phase dir — 100×502 em T+0..60s, receita de chaos a re-rodar (D-16/D-18).
- `.planning/phases/11-prod-hardening/11-CONTEXT.md` — D-07 (chaos = Vast API DELETE raw, breaker por observação natural), D-04 (SLO oficial v1.0), decisões PRD-01..06.

### Código-alvo
- `gateway/internal/primary/reconciler.go` — `evaluateReady` (L403) = alvo RES-11; `recoverOpenLifecycle` + `waitForReadyOrDestroy` (L931, contador ErrInstanceNotFound 3-strike de 01e7558) = classificação a portar.
- `gateway/internal/primary/fsm.go` — 5-state FSM (Asleep/Provisioning/Ready/Draining/Destroying), transições atômicas.
- `gateway/internal/upstreams/probe.go:160` — `p.loader.All()` = alvo RES-12.
- `gateway/internal/upstreams/loader.go` — `Resolve(role, 0)` L222 (honra tier0Override) vs `All()` L358 (raw snapshot).
- `gateway/internal/proxy/dispatcher.go` — tabela de dispatch breaker-driven (header do arquivo), loop tier-1 `tier_priority` ASC, emergency-pod bypass D-E3 = alvo RES-13.
- `gateway/internal/alert/` — alerter com severity fan-out (critical → Chatwoot + ClickUp + Brevo) = canal do D-03.
- `gateway/internal/breaker/` — breaker.Set, force-open/force-close existentes (gatewayctl) = mecanismo de D-04/D-13.

### Requirements + roadmap
- `.planning/ROADMAP.md` Phase 12 entry — goal verbatim com os 3 gaps + stretch CAP-01.
- `.planning/REQUIREMENTS.md` — RES-11, RES-12, RES-13, CAP-01; RES-08 (sensitive nunca tier-1 externo, invariante LOCKED).

### Referência de fix anterior (padrão a seguir)
- Commit `01e7558` — "primary reconciler tolerates transient ErrInstanceNotFound (3-strike confirm)" — padrão de contador que D-02 estende.

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- `recoverOpenLifecycle` (reconciler.go) — já classifica `instance not running` corretamente no startup; RES-11 porta/extrai essa classificação pro `evaluateReady` tick.
- Contador 3-strike de `ErrInstanceNotFound` em `waitForReadyOrDestroy` (01e7558) — padrão direto pra D-02.
- `loader.Resolve(role, 0)` — path correto já existe e é usado pelo dispatcher; RES-12 é trocar a chamada no probe tick, não criar resolução nova.
- Loop tier-1 `tier_priority` ASC no dispatcher (Phase 11.2 D-B5′) — RES-13 D-08 reusa o mesmo loop de candidatos pra cascade de dial failure.
- Pacote `alert` completo (severity.go, dedup.go, 3 canais) — D-03 só emite mensagem nova, sem infra.
- `gatewayctl breaker force-open/force-close` — mecanismo determinístico existente que D-04/D-13 invocam programaticamente do FSM.

### Established Patterns
- Breakers observation-driven (Phase 3) — preservado; D-04/D-13 adicionam transições determinísticas FSM-driven nos eventos de vida/morte do pod, D-09 adiciona observação por request de dial failure.
- Tool-call no-retry (Phase 3) — preservado por D-06 (connection-class é pré-processamento, retry seguro).
- Sensitive-tenant block RES-08 — invariante intocada (D-10).
- HUMAN-UAT plan pattern (`autonomous: false`) — chaos re-run dev+prod segue o pattern do 11-07.
- Dispatcher emergency-pod bypass D-E3 (Plan 06-08) — interage com D-13; planner deve verificar se o bypass já mitiga parcialmente o estado OPEN herdado.

### Integration Points
- FSM (`primary/`) ↔ breaker.Set (`breaker/`) — acoplamento novo: force-open na morte (D-04), force-close no markReady (D-13).
- FSM ↔ alerter (`alert/`) — emissão de critical alert na detecção de morte com causa classificada (D-03).
- Prober (`upstreams/probe.go`) ↔ loader tier0Override — troca `All()` → `Resolve(role,0)` por role (D-11/D-12).
- Dispatcher (`proxy/dispatcher.go`) ↔ transport/dial errors — interceptação connection-class + re-dispatch tier-1 (D-06..D-09).
- Chaos UAT: ops-claude → Vast API DELETE → gateway prod n8n-ia-vm; carga durante kill via receita 11-07.

</code_context>

<specifics>
## Specific Ideas

- Alert billing-stop com texto operator-actionable: "Vast account sem crédito" — distinto de host-death; o operador precisa saber que a ação é PÔR CRÉDITO, não debugar pod.
- Pod pro chaos UAT: preço primeiro (D-17) — frase do usuário: "antes de escolher qual o pod, o preço deve ser o 1º fator de escolha".
- Gate zero-502 é especificamente connection-class (`upstream_unreachable`) — o exato erro dos 100×502 do 11-07.
- `gatewayctl primary state` deve voltar a mostrar `pod_url`/`lifecycle_id` coerentes com a routing table do proxy (D-05).

</specifics>

<deferred>
## Deferred Ideas

- **Implementação da decisão CAP-01** (concurrency cap / queue / shape upgrade) — Phase 12 entrega só o doc de decisão (D-19); código vira phase futura.
- **Baseline periódico de probe tier-1** (detectar key OpenRouter expirada antes de incidente) — D-15 manteve gating D-A2; revisitar se incidente de tier-1 morto-sem-detecção ocorrer.
- **Re-provision imediato pós-host-yank** (sem esperar tick do schedule) — D-01 escolheu schedule-driven; revisitar se MTTR do schedule loop se mostrar lento demais em incidente real.

### Reviewed Todos (not folded)
- `260522-allowlist-steering-bug.md` — já RESOLVED 2026-05-22 (deploy staleness, Plan 06.8-05); match do scorer foi por keywords genéricas. Nada a foldar.

</deferred>

---

*Phase: 12-gateway-resilience-remediation*
*Context gathered: 2026-06-12*
