---
quick_id: 261112-pxz
slug: webhook-dev-portainer-non-fatal
status: planned
date: 2026-11-12
---

# Quick Task: step Portainer webhook (dev) não-fatal no deploy-dev

## Problema (FATO)

O job `deploy-dev` (step "Trigger Portainer webhook (dev)") é o **único** job vermelho
dos runs de `build-gateway` / `build-dashboard` na `develop` desde ~21/out. Build e
testes passam; só o webhook derruba o run inteiro, mascarando o sinal real.

Root cause (gh run `43190794450`, job `131094942067`):
```
curl: (22) The requested URL returned error: 404
##[error]Process completed with exit code 22.
```
O secret `PORTAINER_WEBHOOK_URL_DEV_GATEWAY` está setado, mas aponta para a stack
`ai-gateway-dev` (era Portainer stack 34) que foi **decomissionada** — confirmado via
API do Portainer (`GET /api/stacks`): nenhuma stack `ai-gateway-dev` existe hoje; só
as stacks de prod no endpoint 6. O `curl --fail` transforma o 404 em exit 22.

## Escopo

- **DEV apenas.** Não tocar `deploy-prod` nem o secret `PORTAINER_WEBHOOK_URL_PROD_GATEWAY`.
  Prod permanece fatal de propósito (um deploy de prod que falha DEVE ficar vermelho).

## Solução

Nos dois arquivos, no step dev, substituir:
```
curl -X POST "${WEBHOOK}" --fail --silent --show-error
echo "Portainer dev webhook triggered — ..."
```
por uma chamada que captura o HTTP code e **nunca** derruba o build:
```
# O webhook de deploy DEV não é gate: build/test já falham nos próprios jobs.
# Webhook morto/obsoleto (ex.: stack ai-gateway-dev removida) só avisa, não derruba.
CODE="$(curl -X POST "${WEBHOOK}" --silent --show-error --output /dev/null --write-out '%{http_code}' || echo 000)"
if [[ "${CODE}" =~ ^2 ]]; then
  echo "Portainer dev webhook disparado (HTTP ${CODE}) — ai-gateway-dev re-fetch + recreate"
else
  echo "::warning::Webhook dev retornou HTTP ${CODE} (alvo ausente/obsoleto?) — deploy dev pulado, build segue verde"
fi
```

## Arquivos

- `.github/workflows/build-gateway.yml` — job `deploy-dev`, step "Trigger Portainer webhook (dev)" (~linha 237-246)
- `.github/workflows/build-dashboard.yml` — job `deploy-dev`, step "Trigger Portainer webhook (dev)" (~linha 163-172)

## Critério de pronto

- Os dois arquivos têm o step dev non-fatal (warning no HTTP erro, exit 0).
- `deploy-prod` e o secret de prod intocados (diff só mexe em linhas do job dev).
- YAML válido (`actionlint` se disponível, senão `python -c yaml.safe_load`).

## Tarefas

1. Editar step dev em `build-gateway.yml` → non-fatal com HTTP code + warning.
2. Editar step dev em `build-dashboard.yml` → idem.
3. Validar YAML dos dois arquivos; confirmar diff não toca prod.
