# ifix-ai-gateway

Gateway OpenAI-compat multi-tenant para a infra de inferência Ifix.
Escrito em Go 1.23+ com `chi v5` + `pgx v5` + `go-redis v9` + `slog`.

## Status

Fase 2 — scaffold + entrypoints. DB, auth, proxy, audit e idempotency são
montados por planos subsequentes (02-02 a 02-06).

## Build

```bash
cd /home/pedro/projetos/pedro/gpu-ifix
go build ./gateway/cmd/gateway
go build ./gateway/cmd/gatewayctl
```

## Env vars

| Var | Obrigatório | Default | Descrição |
|---|---|---|---|
| `AI_GATEWAY_PG_DSN` | sim | - | DSN Postgres (schema `ai_gateway`) |
| `AI_GATEWAY_REDIS_ADDR` | sim | - | `host:port` Redis |
| `AI_GATEWAY_REDIS_PASSWORD` | não | "" | senha Redis |
| `UPSTREAM_LLM_URL` | sim | - | URL do llama-server no pod |
| `UPSTREAM_STT_URL` | sim | - | URL do Speaches no pod |
| `UPSTREAM_EMBED_URL` | sim | - | URL do Infinity no pod |
| `UPSTREAM_HEALTH_BRIDGE_URL` | sim | - | URL do health-bridge `:9100` |
| `GATEWAY_PORT` | não | `8080` | porta HTTP |
| `ENV` | não | `production` | `production` \| `development` |
| `LOG_LEVEL` | não | `info` | `debug` \| `info` \| `warn` \| `error` |
| `SENTRY_DSN` | não | "" | DSN Sentry (vazio = Sentry off) |
| `AI_GATEWAY_PG_MAX_CONNS` | não | `10` | tamanho do pool pgxpool |
| `BOOTSTRAP_TENANT_SLUG` | não | `converseai` | slug do tenant seed |

Na ausência de qualquer variável obrigatória o gateway sai com código 2 e
lista os nomes que faltam em `stderr`.

## Rotas

- `GET /health` — gateway liveness (sempre 200)
- `GET /metrics` — exposição Prometheus (2 counters na Fase 2)
- `POST /v1/chat/completions` — **Plano 02-04**
- `POST /v1/embeddings` — **Plano 02-04**
- `POST /v1/audio/transcriptions` — **Plano 02-04**
- `GET /v1/health/upstreams` — **Plano 02-05**

## Convenções

- Redis namespace: `gw:*` (ex: `gw:apikey:<sha>`, `gw:idem:<tenant>:<key>`)
- Schema Postgres: `ai_gateway` com role `ai_gateway_app`
- Logs: NDJSON (slog) com header sensível redactado (`Authorization`,
  `X-API-Key`, `Cookie`, `Proxy-Authorization`, `api_key`, `apikey`)
- Container: `ghcr.io/ifixtelecom/ifix-ai-gateway`, stack Portainer
  `ai-gateway-{dev,prod}`

## Admin

```bash
./gatewayctl tenant --name "ConverseAI" --slug converseai
./gatewayctl key create --tenant converseai --data-class normal
./gatewayctl key revoke <uuid>
./gatewayctl migrate --dir up
./gatewayctl audit --month 2026-04
```

Subcomandos implementados gradualmente em 02-02 (migrate), 02-03 (tenant/
key), 02-09 (audit).
