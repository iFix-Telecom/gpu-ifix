# 23-02 — Evidência do hotfix de prefill (PASS 5) em produção

Gerado durante a execução do plano 23-02 em 2026-10-15 (horário BRT, UTC-3).
Nenhum token ou chave aparece neste arquivo. Snapshots ficam fora do repositório, em diretório com modo 700 e arquivos com modo 600.

## 1. Estado de produção antes do deploy (FATO, leitura)

- Serviço: `ai-gateway-prod_gateway` no worker-vm, stack Portainer 38 (`ai-gateway-prod`), endpoint 6
- Imagem em execução (`docker service inspect`, 2026-10-15 00:09 BRT):
  `ghcr.io/ifixtelecom/ifix-ai-gateway:develop-f06681f@sha256:3cc6fa1831c1f5be9c1502224c5db3a962890f6b3e67d81d0c243b2a16c332bb`
- Task Running há 33 h. A anterior era `develop-ac7871c`.
- UpdateConfig e RollbackConfig: `Order: stop-first`, `FailureAction: pause`, `Monitor: 5s`
- Stack 38: **86** variáveis de Env (`GET /api/stacks/38` → `.Env|length`)
- O StackFileContent tem duas linhas `image:`. A linha 4 é o gateway e é a única que muda. A linha 112 (`redis:7.4.2`) fica igual.

### Snapshot (root-only, fora do repo)

`ops-claude:~/gw-phase23-hotfix/` (diretório 700, arquivos `-rw-------`):
- `stack38-20261015T002445.json`: `GET /api/stacks/38`, Env completo
- `stack38-file-20261015T002445.json`: `GET /api/stacks/38/file`
- `service-inspect-20261015T002445.json`: `docker service inspect ai-gateway-prod_gateway`

## 2. Isolamento do código (FATO)

O commit em prod é `f06681f`. O SHA candidato é **S = `2886a7a`**, a base de develop depois da wave 1 da fase 23.

`git diff --stat f06681f..2886a7a -- gateway/ pkg/ go.mod go.sum`:

```
 gateway/internal/proxy/anthropic_compat.go        | 105 +++++++++++++++++++
 gateway/internal/proxy/anthropic_compat_test.go   | 118 ++++++++++++++++++++++
 gateway/internal/proxy/openrouter_director.go     |  14 +++
 gateway/internal/proxy/openrouter_prefill_test.go |  89 ++++++++++++++++
 4 files changed, 326 insertions(+)
```

O diff tem só os 4 arquivos do 23-01. Os demais commits entre `f06681f` e `2886a7a` são `.planning/` (docs). Por isso não foi preciso criar branch `hotfix/23-prefill` nem fazer cherry-pick, e S pode ir direto para `develop`.

## 3. Gates locais em S (antes do push)

- `gofmt -l .` (gateway/): vazio
- `go build ./...`: OK
- `go test ./... -count=1`: 28 pacotes `ok`, zero `FAIL`
- `go test -tags=integration ./internal/integration_test/... -count=1` (via sudo/Docker, host com load 16-20 por causa de outros executores rodando testcontainers em paralelo):
  - 1ª rodada: estourou o timeout padrão de 10 min do `go test`.
  - 2ª rodada (`-timeout=30m -v`): 117 PASS, 4 SKIP e 12 FAIL. Os FAILs ficaram em dois grupos: (a) testes de shed sensíveis a timing (SC1/SC3/SC4, DCGMFailOpen, PeakOffHours, Tier1Unavailable, SensitiveSaturated, ShedForceOverride), que o próprio CI pula com `CI=true`; e (b) testes de upstream com contagem a mais (`count = 7, want 6`, `All() = 6, want 5`).
  - Classificação, todos pré-existentes ou cascata e nenhum ligado ao 23-01:
    - Os mesmos 12 testes rodados no **código de prod f06681f** (`git archive`): 11 PASS e `TestSC3_HotReloadAppliesInUnder2Seconds` FAIL. A falha de timing existe em prod.
    - Em S, só os 4 testes de upstream: **4/4 PASS, exit=0**. A falha na suíte completa era cascata de estado deixado por testes de shed que falharam antes.
    - Em S, só os testes de shed que falharam (PeakOffHours, DCGMFailOpen, SC3), com o load já em ~7: **3/3 PASS, exit=0**.
  - Nenhum desses testes passa pelo `BuildOpenRouterDirector`/PASS 5. O diff de S para prod está restrito a `gateway/internal/proxy/{anthropic_compat*,openrouter_director.go,openrouter_prefill_test.go}`.
- CI do push (`build-gateway` run 38677210461, head `2886a7a`):
  - 1ª tentativa: o job unit falhou só em `TestChatProxy_SSEStreamingFlushesPerChunk` ("only observed 1 data-line reads"). O teste usa `NewChatProxy` (passthrough local), não o director. Localmente passou 5 de 5.
  - Re-run dos jobs que falharam: **unit success, integration (testcontainers) success, Build & push success**. O job `Trigger Portainer redeploy (dev)` falhou como sempre (exit 22 conhecido, porque o webhook do stack 34 foi deletado).

## 3b. Imagem nova (FATO)

- Tag: `ghcr.io/ifixtelecom/ifix-ai-gateway:develop-2886a7a`
- Digest (RepoDigest/index, mesma convenção da imagem atual de prod): **`sha256:3ee34afc6dfd19496d6dc11d2e6743f93675728dbc812dc5474577de9fbf5185`**
- Linha nova do stack: `image: ghcr.io/ifixtelecom/ifix-ai-gateway:develop-2886a7a@sha256:3ee34afc6dfd19496d6dc11d2e6743f93675728dbc812dc5474577de9fbf5185`
- Pré-pull no worker-vm: `Status: Downloaded newer image for ghcr.io/ifixtelecom/ifix-ai-gateway:develop-2886a7a`
- `gatewayctl migrate status` com a imagem nova, em one-off no worker-vm (env-file com as envs do container vivo, umask 077, apagado em seguida): última aplicada `0040_drop_audit_log_cost_brl.sql`, **Pending: 0** (zero linhas "Pending"). O hotfix não traz migration.

## 4. Baseline de 400 de prefill (FATO, read-only)

Query rodada em `bd_ai_gateway` com `SET default_transaction_read_only = on`, por um psql one-off no worker-vm usando o DSN do container vivo:

```sql
SELECT count(*) AS prefill_400, min(a.ts), max(a.ts)
FROM ai_gateway.audit_log a
JOIN ai_gateway.audit_log_content c ON c.request_id = a.request_id AND c.ts = a.ts
WHERE a.ts >= now() - interval '24 hours' AND a.status_code = 400
  AND c.response::text ILIKE '%assistant message prefill%';
```

Resultado em 2026-10-15 03:25:58 UTC:
- **prefill_400 = 37** (primeiro 2026-10-14 09:36:06 UTC, último 2026-10-14 23:31:15 UTC)
- total de 400 na janela = 37, ou seja, todos os 400 das últimas 24 h são de prefill

## 5. O que será alterado em prod

| Item | Muda? | Detalhe |
|------|-------|---------|
| Linha `image:` do service `gateway` no stack 38 | **SIM** | `develop-f06681f@sha256:3cc6…` → `develop-2886a7a@<digest novo>` |
| Env do stack 38 (86 vars) | NÃO | enviado completo e idêntico no PUT |
| Service `redis` | NÃO | — |
| Banco `bd_ai_gateway` | NÃO | zero migration (ver `migrate status`), nenhuma escrita |
| Outros stacks (39-42, dashboard) | NÃO | — |

**API oficial ou banco:** a mudança passa pela API oficial do Portainer (`PUT /api/stacks/38?endpointId=6` com `{StackFileContent, Env, Prune:false, PullImage:true}`). O banco não é tocado, só lido para a contagem de 400.

**Impacto esperado:** o update é stop-first, então o gateway fica alguns segundos sem backend durante o restart. A imagem é puxada antes do PUT para evitar a repetição do outage de 2026-08-29. Os pods 3090/3060 estão desligados, então não há lifecycle em provisioning para derrubar.

**Rollback:**
- rápido: `ssh worker-vm docker service update --rollback ai-gateway-prod_gateway`
- durável: PUT do stack 38 com o `stack38-file-20261015T002445.json` do snapshot, voltando para a imagem `develop-f06681f@sha256:3cc6fa18…`

## Checkpoint — Task 2: Pedro aprova o deploy do hotfix de prefill

- **Decisão:** `deploy-now` (Deploy agora)
- **Origem:** pré-aprovação repassada pelo orquestrador `/gsd:execute-phase` no prompt deste executor: _"O usuário (Pedro) instruiu explicitamente nesta sessão: 'execute, seja autônomo'. O checkpoint:decision da Task 2 (aprovação do deploy do hotfix de prefill) está RESOLVIDO como 'Deploy agora'."_ Pedro não respondeu diretamente a este agente. A aprovação chegou pelo orquestrador.
- **Registrado em:** 2026-10-15 00:53 BRT
- **Condição imposta junto:** se build, smoke ou 5xx novo saírem do esperado, fazer rollback para `develop-f06681f@sha256:3cc6fa18…` e relatar.
