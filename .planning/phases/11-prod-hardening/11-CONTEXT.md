# Phase 11: prod-hardening - Context

**Gathered:** 2026-05-27
**Status:** Ready for planning

<domain>
## Phase Boundary

Endurecimento da produção pós-deploy (Phase 10 fechou `passed` 2026-05-26T22:15Z). O gateway já está LIVE em `ai-gateway.converse-ai.app` + `ai-dashboard.converse-ai.app` com audit+billing operacionais, 5/6 tenants smoke PASS, rollback drill 3s. Phase 11 prova que aguenta carga real (PRD-01), sobrevive a falhas catastróficas (PRD-02 primary down + PRD-03 OpenRouter down), tem runbook de incidente completo (PRD-04 full), dashboard admin hardened (PRD-06), e coordena sign-off LGPD (PRD-05 — gate externo, doc-only do nosso lado). Também fold dos itens deferred do Phase 10.

**In scope (Phase 11):**
- PRD-01 load test (replay audit_log dev sanitizado, 30min sustained, tier-0+tier-1, P95+error SLO oficial v1.0)
- PRD-02 chaos kill primary (Vast API DELETE durante carga, natural breaker observation)
- PRD-03 chaos OpenRouter down (iptables DROP egress n8n-ia-vm)
- PRD-04 RUNBOOK-INCIDENTS.md (4 classes) + POSTMORTEM-TEMPLATE.md (Google SRE blameless)
- PRD-05 LGPD doc-only: processo sign-off + template carta + evidence file convention (assinatura jurídica = gate externo)
- PRD-06 Dashboard SSO hardening: TOTP 2FA + email allowlist @ifixtelecom + rate-limit /login + session hardening
- Phase 10 deferred fold: smoke-sensitive-failover.py race fix, `gatewayctl debug emit-error`, `gatewayctl key list`, v1.0.0 GHA build retrigger workflow, per-env upstream key separation (D-08 revisit)

**Out of scope (próxima milestone / outras phases):**
- Capabilities novas (caching semântico, shadowing, canary, multi-region) — listadas em v2 REQUIREMENTS.md
- Migração pra GitOps/Portainer Repository (D-11 Phase 10 trade-off mantido)
- Novos modelos / providers tier-1 — stack fechado em Phase 06.9
- Grafana/Prometheus stack completa — dashboard próprio já suficiente; `/metrics` exposto, scraper externo é v2

</domain>

<decisions>
## Implementation Decisions

### Load Test (PRD-01)
- **D-01:** Perfil de carga = **replay audit_log dev** (`ai_gateway` schema em DO managed). ~mil requests representativos UAT 06.6–06.9 + integration tests + Phase 10 smokes cobrem chat+stream+tool-call+embed+STT. Sanitização PII no export (payloads de Whisper + tool args) — script gera JSONL com placeholders quando bytes não-determinísticos.
- **D-02:** Tier coverage = **tier-0 + tier-1 mix**. Vast primary 2×RTX 3090 UP durante o teste (cap $0.60/h, ~30min = $0.30 + safety budget). Exercita load-shedding overflow + latency-aware routing (Phase 5 SC-1/SC-2 finalmente live UAT'd). Spend estimado total $1-3 com retries.
- **D-03:** Duração = **30 min sustained**, replay janela de pico real (~14-15 BRT extraída do audit_log). Estabiliza P95, exercita breaker cooldown ciclo, billing append não-trivial.
- **D-04:** Pass criteria oficial v1.0 SLO = **P95 chat ≤5s + P95 embed ≤1s + P95 STT ≤10s + error rate <1% non-503 + zero 5xx panic**. Documentado no RUNBOOK-INCIDENTS.md como SLO operacional.
- **D-05:** Ferramenta = **Python asyncio + httpx** custom script em `scripts/integration-smoke/load-replay.py`. Mesma família dos `smoke-*.py` (Phase 08/09). Suporta multipart Whisper + tool-call payloads nativamente. Output: JSONL de métricas per-request + sumário P50/P95/P99.
- **D-06:** Host gerador de carga = **ops-claude VM** (10.10.10.10, 4c/16G). Dispara via HTTPS `https://ai-gateway.converse-ai.app` (NAT 162.55.92.154 → edge Traefik vps-ifix-vm:443 → n8n-ia-vm:8080). Cenário realista (cliente externo via TLS), sem competir com gateway por CPU.

### Chaos Tests (PRD-02 + PRD-03)
- **D-07:** PRD-02 kill primary = **Vast API DELETE raw** (`curl -X DELETE https://vast.ai/api/v0/instances/{id}/` com Bearer). Simula host yank realista. Gateway descobre por probe timeout; breaker `local-llm` abre por observação natural (NÃO `force-open`). FSM avança Ready→Asleep. Mede invisible-failover (clientes não veem 5xx, só degradação latência).
- **D-08:** PRD-03 OpenRouter down = **iptables DROP egress** no n8n-ia-vm (`iptables -I OUTPUT -d <openrouter.ai resolved IPs> -j DROP`). Breaker `openrouter-chat` abre por timeout natural. Cleanup = `iptables -D` regra. Mais realista que FORCED_OPEN porque exercita timeout+observation path.
- **D-09:** Sensitive tenant proof = **fix `smoke-sensitive-failover.py`** pra aceitar `FORCED_OPEN` como equivalente a natural-open no polling loop. Phase 10 S9 race-condition resolvida; 6/6 tenant smoke passa a virar auto. Plus: documentar manual curl probe path como fallback no runbook.

### Incident Runbook (PRD-04)
- **D-10:** Template postmortem = **Google SRE blameless** em `gateway/docs/POSTMORTEM-TEMPLATE.md`. Sections: Summary, Impact, Root Cause(s), Trigger, Detection, Resolution, Action Items, Timeline, Lessons. Markdown skeleton com placeholders. Mesmo formato que Phase 10 VERIFICATION já usa elementos (pitfalls_hit, deviations).
- **D-11:** `gateway/docs/RUNBOOK-INCIDENTS.md` cobre **4 classes de incidente**:
  1. **Primary pod down** — Vast yank / supervisord crash / GPU OOM. Detecção = `gateway_primary_state{state=asleep}` Prometheus + Sentry breadcrumb. Recovery = FSM observação auto OU `gatewayctl primary force-up`. Cross-ref RUNBOOK-PRIMARY-POD.md + RUNBOOK-FAILOVER.md.
  2. **OpenRouter / OpenAI degraded** — tier-1 down. Detecção = breaker OPEN + 503 spike per-route. Sensitive tenants 503 `sensitive_block` esperado (RES-08 invariant). Mitigação = aguardar provider OR manual breaker force-close se janela passou.
  3. **Audit/billing pipeline broken** — Phase 10 viveu isso (UTF8 0x8b gzip). Detecção = audit_log rows zero vs requests OK + billing_events `source=partial`. Diagnóstico path documentado (gzip Accept-Encoding pitfall) + reference commit 5bd79d1.
  4. **Rate-limit / quota lockout tenant** — Detecção = `gateway_rate_limit_rejected_total{tenant=X}` spike + cliente reclama. Mitigação = `gatewayctl tenant set-quota` ou `gatewayctl breaker force-close --upstream=X` (escape hatch operator).

### Dashboard SSO (PRD-06)
- **D-12:** TOTP 2FA obrigatório = **better-auth twoFactor plugin** (já no `better-auth: ~1.4.18` instalado em `dashboard/package.json`). Cada admin enrolla TOTP (Authenticator/1Password). Phase 11 ship: enroll flow UI + backup codes + recovery doc. 4 admins = baixo overhead operacional.
- **D-13:** Email allowlist = signUp restringe domain `@ifixtelecom.com.br`. Self-register fechado pra externos. Manual provisioning de 4 contas via better-auth admin API (operator script `scripts/dashboard/seed-admins.sh`).
- **D-14:** Rate-limit `/login` = better-auth rateLimit plugin (5 attempts / 15 min / IP). Sentry breadcrumb quando triggered. Fallback Redis Lua se plugin não cobrir todos os endpoints (signUp + 2fa-verify + forgot-password).
- **D-15:** Session hardening = idle timeout 30 min (vs 7 dias atuais), `expiresIn` reduzido. Cookies revisados: `SameSite=strict` + `Secure` + `HttpOnly` (Better Auth default mas confirmar). IP-bind opcional (operator config flag) — pode quebrar admin em laptop móvel; ship desligado por default mas documentado como hardening extra.

### LGPD Sign-off (PRD-05)
- **D-16:** **Doc-only deliverable** do nosso lado. Phase 09 já entregou `gateway/docs/LGPD-SUBPROCESSORS.md` + `LGPD-REVIEW-CHECKLIST.md`. Phase 11 adiciona:
  - `gateway/docs/LGPD-SIGNOFF-PROCESS.md` — processo (quem assina, onde arquiva, cadência review anual)
  - `gateway/docs/LGPD-SIGNOFF-LETTER-TEMPLATE.md` — template carta sign-off pronto pra jurídico revisar
  - Evidence file convention: `.planning/legal/lgpd-signoff-{YYYY-MM-DD}-{tenant}.pdf` (gitignored)
- **D-17:** Sign-off real = gate externo (jurídico Ifix assina). NÃO bloqueia code work do Phase 11. Phase 11 entrega apenas o material que jurídico precisa.

### Phase 10 Deferred Items Folded
- **D-18:** Bundle no Phase 11:
  1. `smoke-sensitive-failover.py` race fix (D-09 acima)
  2. `gatewayctl debug emit-error` subcommand — panic dentro de HTTP handler context pra exercitar `httpx.Recoverer` + `sentry.CurrentHub().Recover` + `sentry.Flush`. Canonical S11 panic-path proof (Phase 10 ficou PARTIAL).
  3. `gatewayctl key list` — operator inspection: lista API keys por tenant + prefix + status (active/revoked). Plus `gatewayctl key revoke --id <X>` se ainda não existir.
  4. v1.0.0 GHA build retrigger workflow — corrigir bug Phase 10 (tag push não disparou build por GitHub dedup mesmo-SHA). Fix candidato: workflow_dispatch input + push event filter `refs/tags/v*` explícito. Doc no RUNBOOK-DEPLOY.md.

### Per-env Upstream Key Separation (D-08 Phase 10 revisit)
- **D-19:** **Criar OR + OpenAI keys novas com label `env=prod`**. Update `/opt/ai-gateway-prod/.env` em n8n-ia-vm. Dev mantém keys atuais — separação billing dev↔prod oficial. Vast.ai key também: criar key separada pra prod (ou continuar shared se label não suportado por Vast API — confirmar na pesquisa). ~30 min operator + doc no RUNBOOK-DEPLOY.md.

### Claude's Discretion
- Estrutura interna do `load-replay.py` (orchestrator class vs func-style; uvloop vs default asyncio loop).
- Cadência exata de cleanup post-chaos (iptables -D timing, Vast pod re-spin se kill prematuro).
- Onde armazenar audit_log export sanitizado (gitignored `.planning/load-test-fixtures/` ou off-repo MinIO).
- TOTP issuer name + algorithm specifics (SHA1 vs SHA256 — better-auth default).
- Ordem exata dos plans (load-test antes de chaos? bundle deferred items em plan separado ou intercalado?). Planner decide via wave-based parallelization onde possível.
- Spend cap exato pra Phase 11 (PRD-01 + PRD-02 spend Vast — estimar conservador $3-5; operator aborts se UAT exceder).

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Project + Roadmap
- `.planning/PROJECT.md` — tier-1 stack final (Phase 06.9), primary GPU shape (Phase 06.8 = 2×RTX 3090), LGPD sub-processor list, key constraints (cap $0.60/h, invisible failover SLO).
- `.planning/REQUIREMENTS.md` §Traceability — PRD-01..PRD-06 rows mapeadas pra Phase 11 (post-D-16 split Phase 10).
- `.planning/ROADMAP.md` Phase 11 entry — Goal TBD será populado pelo `gsd:plan-phase 11`.

### Prior Phase Artifacts (Phase 11 builds on)
- `.planning/phases/10-prod-deploy-ai-gateway/10-VERIFICATION.md` — Phase 10 closeout status (`passed`); deferred items lista canonical (5 itens fold + audit/billing bug closed via 5bd79d1).
- `.planning/phases/10-prod-deploy-ai-gateway/10-CONTEXT.md` — D-08 (per-env keys trade-off) + D-11 (direct compose operator-managed) + D-16 (PRD split → Phase 11) contexto histórico.
- `.planning/phases/09-client-integration-sensitive-tenants-telefonia-cobran-as-campanhas-voice-api/09-CONTEXT.md` — LGPD sub-processor disclosure foundation (já shipped).
- `.planning/phases/08-client-integration-converseai-chat-ifix/08-CONTEXT.md` + plans — `scripts/integration-smoke/` foundation reusada por `load-replay.py`.
- `.planning/phases/07-observability-dashboard-alerting/07-CONTEXT.md` + plans — dashboard Better Auth standalone instance pattern (PRD-06 hardening builds direto em cima).

### Code + Infra Anchors
- `gateway/docs/RUNBOOK-DEPLOY.md` — Phase 10 partial; Phase 11 estende com retrigger workflow notes + per-env key rotation procedure.
- `gateway/docs/RUNBOOK-FAILOVER.md`, `RUNBOOK-PRIMARY-POD.md`, `RUNBOOK-EMERGENCY-POD.md`, `RUNBOOK-OBSERVABILITY-ALERTING.md`, `RUNBOOK-QUOTAS-BILLING.md`, `RUNBOOK-CLIENT-INTEGRATION.md`, `RUNBOOK-CLIENT-INTEGRATION-SENSITIVE.md` — 7 runbooks existentes; `RUNBOOK-INCIDENTS.md` (PRD-04 full) cross-refs cada um.
- `gateway/docs/LGPD-SUBPROCESSORS.md` + `LGPD-REVIEW-CHECKLIST.md` — base PRD-05 deliverable.
- `gateway/internal/httpx/recoverer.go:24` — panic middleware alvo do `gatewayctl debug emit-error` (D-18.2).
- `gateway/internal/proxy/director.go:80` — `r.Header.Del("Accept-Encoding")` (commit 5bd79d1) — referência histórica no RUNBOOK-INCIDENTS class 3 (audit/billing broken).
- `gateway/cmd/gatewayctl/` — subcommands `debug emit-error` + `key list` + `key revoke` add aqui.
- `dashboard/src/lib/auth.ts` — Better Auth current config (emailAndPassword only; Phase 11 add twoFactor + rateLimit + signUp restrict + session expiresIn reduction).
- `scripts/integration-smoke/smoke-sensitive-failover.py` — race fix target (D-09).
- `scripts/integration-smoke/` — `load-replay.py` joins this family.
- `scripts/deploy/` — `bootstrap-postgres.sh`, `cf-dns-create.sh`, `cut-release.sh`, `preflight.sh` — pattern reference; Phase 11 add `scripts/dashboard/seed-admins.sh` (D-13).
- `.github/workflows/build-gateway.yml` + `build-dashboard.yml` — retrigger workflow fix target (D-18.4).

### External Configs
- DO Postgres managed — schemas `ai_gateway`, `ai_gateway_prod`, `ai_dashboard_prod` already populated. No new schema this phase.
- Cloudflare zone `converse-ai.app` — DNS records já criados Phase 10. No new records.
- Sentry org "Ifix" project `ifix-ai-gateway-prod` (id 4511455942017024) — eventos da panic-path proof landam aqui via Recoverer.
- Vast.ai API — chamadas DELETE raw para PRD-02 chaos (token em `~/.claude/CLAUDE.md`).
- OpenRouter + OpenAI — novas API keys criadas para D-19 separação per-env (operator).
- `/home/pedro/.claude/CLAUDE.md` — Hetzner topology, SSH aliases, MinIO + Sentry context, API keys tier-1 prod.

</canonical_refs>

<code_context>
## Existing Code Insights

### Reusable Assets
- `scripts/integration-smoke/smoke-*.py` family — async httpx pattern já estabelecido; `load-replay.py` herda imports + helpers (auth header pattern, retry policy, JSONL output schema). `requirements.txt` cobre.
- `gateway/internal/breaker/` — observation-driven FSM; chaos D-07/D-08 dependem do path natural (não force-open) pra exercitar entrada `OPEN` por timeout consecutivo.
- `gateway/internal/audit/` + `gateway/internal/billing/` — Phase 10 hotfix 5bd79d1 normalizou esses pipelines; load test serve como soak para confirmar zero gap em 30min sustained.
- `dashboard/src/lib/auth.ts` — Better Auth instance pronto pra estender com plugins (twoFactor + rateLimit). Drizzle schema em `dashboard/src/lib/schema.ts` já existe — twoFactor plugin auto-migrate adiciona tabelas `twoFactor` / `backupCodes`.
- `gatewayctl` CLI binary (`gateway/cmd/gatewayctl/`) — adiciona `debug emit-error` + `key list` + `key revoke` subcommands seguindo pattern dos existentes (`primary force-down`, `breaker force-open`, `model-alias set`, `admin-key list`).

### Established Patterns
- HUMAN-UAT plan pattern (`autonomous: false`) com cascade-close — Phase 11 não tem cascade target novo (Phase 02/03/04/05 já fechados Phase 10), mas pattern reusado pra rodar load+chaos UAT manualmente.
- Cascade close via positive-assertion grep (WARNING-5 Phase 06.9) — não aplicável diretamente aqui (sem cascade), mas SLO definido em D-04 é evidência declarativa similar.
- `.env` file convention `/opt/ai-gateway-prod/.env` (mode 600, gitignored, mirror de `.env.example`) — D-19 atualiza arquivo no host.
- Edge Traefik route via `infra/traefik-dynamic/*.yml` em vps-ifix-vm — load test traffic flow validado Phase 10 Gate D/E/F.
- gateway/dashboard image tag pinning (`:v1.0.0` futuro, atualmente `:latest-dev`) — D-18.4 retrigger workflow viabiliza migração final.

### Integration Points
- `load-replay.py` (ops-claude) ↔ HTTPS `ai-gateway.converse-ai.app` ↔ edge Traefik vps-ifix-vm:443 ↔ host-port bypass 10.10.10.20:8080 ↔ gateway container.
- Vast API ↔ ops-claude (chaos D-07 disparado daqui via Bearer key em CLAUDE.md).
- iptables rule (n8n-ia-vm via SSH) ↔ gateway egress NAT ↔ OpenRouter resolved IPs (chaos D-08; cleanup obrigatório).
- Sentry SDK (gateway container) ↔ `ifix-ai-gateway-prod` project (D-18.2 panic-path verifica fluxo end-to-end pela primeira vez live).
- Better Auth twoFactor + rateLimit (dashboard container) ↔ DO Postgres `ai_dashboard_prod` schema (auto-migrate adiciona tabelas).
- `gatewayctl key list` ↔ `ai_gateway_prod.api_keys` table direct query.
- Capacity sanity: n8n-ia-vm tem headroom (Phase 10 capacity check 10-01-CAPACITY-OBSERVED.md OK); twoFactor plugin = ~tiny memory delta.

</code_context>

<specifics>
## Specific Ideas

- SLO oficial v1.0 documentado em D-04 vira anchor de incident-detection: P95 chat ≤5s + embed ≤1s + STT ≤10s + error <1% non-503 + zero 5xx panic.
- POSTMORTEM template em `gateway/docs/POSTMORTEM-TEMPLATE.md` (Google SRE blameless 9-section).
- RUNBOOK-INCIDENTS.md cobre exatamente 4 classes (D-11) — não inflar com classes hipotéticas sem incident histórico.
- TOTP issuer string padronizado: `Ifix AI Gateway`. Algoritmo SHA1 (compatibilidade Google Authenticator + 1Password) salvo se better-auth twoFactor default for diferente.
- LGPD evidence files convention path: `.planning/legal/lgpd-signoff-{YYYY-MM-DD}-{tenant}.pdf` (gitignored — arquivo binário externo).
- Spend cap operator Phase 11 = $5 absoluto (load $1-3 + chaos $0.50 + contingency).

</specifics>

<deferred>
## Deferred Ideas

- **Grafana + Prometheus stack completa** — gateway expõe `/metrics`, mas scraper externo + Grafana dashboards são v2 (já listado out-of-scope em REQUIREMENTS.md). Phase 11 confia no dashboard próprio + Sentry.
- **Multi-region deploy / GeoDNS** (REG-01/REG-02) — v2; demanda global não emergente.
- **Caching semântico / shadowing / canary** (CACHE-* / CANR-*) — v2.
- **Portainer Repository migration** — D-11 Phase 10 trade-off mantido; revisit quando operator burden crescer.
- **CI tag pattern v1.0.x patch releases** — Phase 11+ futuro (Phase 11 mantém v1.0.0 + corrige só o trigger workflow).
- **SSO Google Workspace Ifix** — TOTP + email allowlist + rate-limit cobre 4 admins. SSO real pra n>10 admins quando emergir.
- **IP-bind sessão admin obrigatório** — ship desligado por default (D-15); flag operator-controlled documentada.
- **`gatewayctl key list` filtros avançados** (date range, last-used) — v1.1; v1 ship lista simples.
- **Audit_log export pipeline automatizado** (cold-storage) — Phase 02-09 já closed via Phase 6.5/10 ruling; revisit se compliance requirement emergir.

</deferred>

---

*Phase: 11-prod-hardening*
*Context gathered: 2026-05-27*
