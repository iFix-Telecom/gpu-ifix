---
quick_id: 261112-pxz
slug: webhook-dev-portainer-non-fatal
status: complete
date: 2026-11-12
commit: 1eae602
---

# Summary: step Portainer webhook (dev) não-fatal no deploy-dev

## O que mudou

`.github/workflows/build-gateway.yml` e `.github/workflows/build-dashboard.yml`, job
`deploy-dev`, step "Trigger Portainer webhook (dev)": o `curl --fail` foi trocado por
uma captura de HTTP code que trata erro como `::warning::` e sai 0. Webhook morto/obsoleto
deixa de derrubar um build válido. Steps de prod (`deploy-prod`) e o secret
`PORTAINER_WEBHOOK_URL_PROD_GATEWAY` ficaram **intocados** (prod permanece fatal).

## Root cause (FATO)

gh run `43190794450`, job `131094942067`:
```
curl: (22) The requested URL returned error: 404
```
O secret `PORTAINER_WEBHOOK_URL_DEV_GATEWAY` existe, mas aponta para a stack
`ai-gateway-dev` (era Portainer stack 34), **decomissionada** — confirmado via
`GET /api/stacks` no Portainer3: nenhuma stack `ai-gateway-dev` existe hoje (só prod no
endpoint 6). Com `--fail`, o 404 virava exit 22 e derrubava todo o run verde desde ~21/out.

## Verificação

- YAML válido nos dois arquivos (`yaml.safe_load`).
- Lógica do branch 2xx/erro testada sob `set -e`: 200/204 → verde; 404/500/000 → warning, exit 0.
- `git diff` não altera nenhuma linha de prod (só comentário cita `deploy-prod`).

## Desvio anotado (não é desta tarefa)

A stack `ai-gateway-dev` não existe mais no Portainer — o deploy dev **não recria nada**
porque não há alvo. Recriar a stack dev (se o Pedro quiser auto-deploy dev de verdade) é
decisão de infra fora do escopo desta quick. Registrado como aviso ao Pedro.
