# Phase 19: Gateway consolidation → worker-vm — Context

**Gathered:** 2026-07-03
**Status:** Ready for planning

> ⚠️ SECRETS: os valores reais de env (senha DB, OpenAI/OpenRouter/Gemini bearers, R2 secret, SSH key)
> foram fornecidos pelo usuário e vivem no `.env` root-600 do worker-vm. NÃO commitar valores em `.planning/`.
> Este doc descreve ESTRUTURA/decisões, não os segredos.

<domain>
## Task Boundary

Consolidar TODAS as stacks ai-gateway numa VM única (**worker-vm**, 10.10.10.50), tornando-a o
gateway unificado (dev+prod fundidos). Descomissionar os gateways atuais de n8n-ia-vm (prod) e
vps-ifix-vm (dev). Todas as stacks passam a ser gerenciadas/editáveis via Portainer.

Três entregas do usuário:
1. Migrar todas as stacks ai-gateway → worker-vm.
2. `ai-gateway-prod` com o env especificado (= config estilo DEV: bd_ai_gateway + R2 + upstreams locais).
3. Todas as stacks acessíveis para edição via Portainer.
</domain>

<decisions>
## Implementation Decisions (LOCKED — respostas do usuário 2026-07-03)

### Consolidação
- **Unificar TUDO no worker-vm.** worker-vm vira o ÚNICO gateway. Descomissiona n8n-ia-vm (prod) E
  vps-ifix-vm (dev). Um só ai-gateway rodando, config = a fornecida (bd_ai_gateway, R2).

### Banco de dados / tenants
- Gateway novo aponta pra **`bd_ai_gateway`** (hoje o DB do dev; 4 tenants / 20 keys / 918 billing, migrado v31).
- Prod atual usa **`bd_ai_gateway_prod`** (18 tenants / 19 keys / 33.082 billing — inclui converseai, chat-ifix,
  telefonia, cobrancas, ia-kanban etc.).
- **DECISÃO: migrar tenants + api_keys (+ billing conforme plano) de `bd_ai_gateway_prod` → `bd_ai_gateway`.**
  Cuidado: dedup por slug (alvo já tem 4 tenants dev), preservar HASH das keys (não regenerar — apps não
  podem quebrar), resolver colisões de slug/id. Definir se migra histórico de billing (33k linhas) ou só
  tenants/keys + arquiva billing antigo.

### Execução
- **Planejar via GSD primeiro** (esta fase). Estruturar: migração DB, redis, network, 5-6 stacks, cutover,
  rollback, descomissionamento. Executar por etapas com checkpoints.

### Dependências no worker-vm
- **Criar redis `infra-redis-1`** no worker-vm (env hardcoda `AI_GATEWAY_REDIS_ADDR=infra-redis-1:6379`).
- **`172.18.0.1:18000`** (UPSTREAM_LLM/STT/HEALTH) = endpoint do **pod local**, populado quando um pod sobe
  (igual o dev roda hoje: sem :18000 ativo → gateway opera em tier-1 fallback openrouter/gemini). Deixar como
  está; nada a subir agora. `172.18.0.1` = gateway do bridge docker no worker-vm.
</decisions>

<code_context>
## Recon do alvo (worker-vm) e estado atual — 2026-07-03

### worker-vm (10.10.10.50)
- **Docker Swarm** (services n8n/kestra/rabbitmq/postgres/traefik-internal; overlay `worker_intra`).
  Deploy = stacks swarm (Portainer endpoint **6**), NÃO compose standalone.
- **SEM GPU.** 8 cores / 15G RAM (~9G livre). Confirma que :18000 não é modelo local.
- Networks: `bridge`, `docker_gwbridge`, `worker_intra` (overlay). **NÃO tem `traefik-public`**
  (essa é da vps-ifix-vm). Tem `traefik-internal_traefik` (v2.11).
- Tem `redis_redis` (redis:7.4.2) mas NÃO `infra-redis-1` → criar.
- `portainer_agent` 2.39.1 presente (endpoint 6).
- Sem listener :18000 / :7997.

### Stacks a migrar (6 containers)
- **n8n-ia-vm (10.10.10.20):** `ifix-ai-gateway` (prod, compose manual /opt/ai-gateway-prod, NÃO Portainer),
  `ifix-ai-dashboard`, `ai-gateway-embed` (/opt/ai-gateway-embed), `ai-gateway-rerank` (/opt/ai-gateway-rerank),
  `redis-gateway-prod`.
- **vps-ifix-vm (10.10.10.30):** `ai-gateway-dev` (Portainer stack **34**, endpoint 3, type compose).
- Portainer hoje: só `ai-gateway-dev` é stack (id 34). prod/embed/rerank/dashboard = compose manual.

### DBs (DO managed postgres, db-grupoifix-...:25060)
- `bd_ai_gateway` (alvo): schema ai_gateway 33 tabelas, goose v31. 4 tenants / 20 keys / 918 billing.
- `bd_ai_gateway_prod` (origem tenants): 18 tenants / 19 keys / 33.082 billing.

### Weights R2 (Cloudflare, bucket ai-gateway-weights)
- Contém `qwen3.6-27b-Q4_K_M/v1.0.0/model.gguf` (16GB) + bge-m3 + whisper.
- Env fornecido referencia `WEIGHTS_QWEN_KEY=qwen3.5-27b...` (NÃO existe no R2 — quirk pré-existente do dev;
  primary/emergency pods desabilitados/não-foco, gateway roda tier-1). Marcar como known-gap, não bloqueia.

### Env-alvo do ai-gateway-prod (worker-vm) — ESTRUTURA (valores reais no .env root-600, NÃO aqui)
- DSN → `bd_ai_gateway` (sslmode=require). Redis → `infra-redis-1:6379`.
- UPSTREAM_LLM/STT/HEALTH → `http://172.18.0.1:18000` (pod local). EMBED → `http://10.10.10.20:7997`
  (n8n-ia-vm embed — ATENÇÃO: se n8n-ia-vm for descomissionada, o embed precisa migrar junto pro worker-vm;
  reavaliar esse upstream no plano).
- MinIO → **R2** (endpoint/bucket ai-gateway-weights/creds R2). Weights keys R2.
- Primary pod: `PRIMARY_POD_SCHEDULE_DISABLED=true`, num_gpus 2, allowlist 43803/55158, blocklist dev.
- Emergency: MONTHLY_EMERGENCY_BUDGET_BRL=200, EMERGENCY_POD_IMAGE_TAG=latest-dev, PROVISION_* tunables.
- STT fallback gemini, PRIMARY_POD_SERVE_STT=false. LOG_LEVEL=debug. POD_DEBUG_SSH pedro@ops-claude.

### Gotchas conhecidos (memórias)
- Portainer git-stack com bind-mount relativo QUEBRA em endpoint agent (6) → usar compose-string com paths
  absolutos OU bakear na imagem. Ver [[gateway-prod-build-deploy]].
- Migration NÃO roda no boot (MIGRATE_ON_BOOT=false) → `gatewayctl migrate up` manual se houver pendente.
- Webhook Portainer prod não puxa imagem → pull+recreate manual.
- worker-vm hostname interno = `debian-template` (não renomeado pós-clone); alias SSH funciona.
</code_context>

<deferred>
## Fora de escopo desta fase (avaliar depois)
- Filtro geo Americas no market-search do primary (issue separada de cold-start — descoberta 2026-07-03,
  Noruega health_timeout). Com R2 global o problema some pros pods, mas o gateway consolidado herda a política
  failStreak (quick 260702-nse). Reavaliar necessidade do filtro geo pós-migração.
- Subir o modelo qwen3.5-27b (ou alinhar em 3.6) no R2 se emergency/primary pods forem reativados.
- embed/rerank: decidir se migram como stacks separadas pro worker-vm ou consolidam.
</deferred>
