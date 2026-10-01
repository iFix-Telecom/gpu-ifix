---
phase: quick-260930-wpv
plan: 01
status: complete-pending-integration-run
subsystem: gateway (auth revoke, admin keys, gatewayctl)
tags: [auth, revoke, cache, redis, pubsub, l1, integration-tests]
clickup: 86akrbgbt
requires: [quick-260930-vkt (L1 cache)]
provides:
  - "auth.RevokeAPIKey / auth.InvalidateKey (DB + DEL + PUBLISH)"
  - "Verifier.EvictL1 + StartRevocationListener (cross-replica L1 eviction)"
  - "query RevokeAPIKeyReturningHash"
affects: [POST /admin/keys/{id}/revoke, gatewayctl key revoke, gateway boot]
key-files:
  created:
    - gateway/internal/auth/revoke.go
    - gateway/internal/auth/revoke_test.go
    - gateway/internal/redisx/apikey.go
    - gateway/cmd/gatewayctl/key_revoke_test.go
  modified:
    - gateway/db/queries/admin.sql
    - gateway/internal/db/gen/admin.sql.go
    - gateway/internal/db/gen/querier.go
    - gateway/internal/auth/apikey.go
    - gateway/internal/auth/cache.go
    - gateway/internal/auth/l1cache.go
    - gateway/internal/admin/keys_admin_http.go
    - gateway/internal/admin/keys_admin_http_test.go
    - gateway/cmd/gatewayctl/key.go
    - gateway/cmd/gateway/main.go
    - gateway/internal/integration_test/auth_flow_test.go
    - gateway/cmd/gatewayctl/primary_lifecycles_integration_test.go
decisions:
  - "Query nova RevokeAPIKeyReturningHash (:one, CTE) em vez de mudar RevokeAPIKey; a antiga fica sem caller de prod"
  - "Revoke repetido devolve o hash (revoked_now=false) e re-invalida o cache: retry conserta Redis que falhou na 1ª"
  - "Tombstone de revogação no L1 (60s) impede lookup em voo de repovoar L1/Redis"
  - "Listener esvazia o L1 inteiro a cada (re)subscribe (Pub/Sub at-most-once)"
  - "Admin: id desconhecido continua 200 no-op (contrato existente); campo novo cache_invalidated"
metrics:
  completed: 2026-09-30
  tasks: 2
  commits: 3
---

# Quick 260930-wpv: revogação de API key imediata em todas as réplicas

Revogar uma key agora faz três coisas: grava no DB, apaga o cache Redis (`gw:apikey:<hex>`) e publica em `gw:apikey:revoked`. Cada réplica do gateway ouve esse canal e tira a key do L1 in-process. A janela de revogação cai de ≤60s para o tempo de entrega do Pub/Sub, sem precisar de flush manual do Redis. Sem push e sem deploy.

**Worktree:** `/home/pedro/projetos/pedro/gpu-ifix/.claude/worktrees/agent-a97f1cb5ac7b83561`
**Branch:** `worktree-agent-a97f1cb5ac7b83561` | **Base esperada:** `569ab6416a9026e752e4db4d79ff98b202c6dbb3`. O worktree estava em `5553bd4`; resetei para a base.

## Commits

| Task | Hash | Mensagem |
|------|------|----------|
| 1 (RED) | `787d3db` | test(260930-wpv): add failing tests for immediate API key revocation |
| 1 (GREEN) | `390d41b` | feat(260930-wpv): immediate API key revocation across replicas |
| 2 | `7fdac9f` | test(260930-wpv): make primary lifecycles assertions tab-independent |

## Mudanças de comportamento em prod (revisar antes do deploy)

1. **Boot do gateway**: `main.go` chama `verifier.StartRevocationListener(ctx)`. Isso abre 1 conexão Redis a mais por réplica, presa em SUBSCRIBE. O boot não espera a assinatura.
2. **`POST /admin/keys/{id}/revoke`**:
   - Passa por `auth.RevokeAPIKey`.
   - A resposta ganha o campo `cache_invalidated` (bool). `status` e `id` continuam iguais. O dashboard repassa o body sem validar, então o campo a mais não quebra nada.
   - Id desconhecido continua respondendo 200, como antes.
   - Se o DB for atualizado e o Redis falhar, responde 200 com `cache_invalidated=false` e loga um warn.
   - Erro de DB continua 500.
   - `NewKeysAdminHandler` agora recebe `rdb`.
3. **`gatewayctl key revoke`**:
   - Conecta no Redis com a mesma config do gateway (`config.Load` → `redisx.NewClient`).
   - Sem Redis, avisa no stderr que a propagação leva até 60s e sai com 0.
   - A saída ganha `cache_invalidated=...`.
   - **Mudou:** rodar em key já revogada antes só imprimia "already revoked". Agora também re-invalida o cache.
4. **Corrida com lookup em voo**: se um lookup leu a row antes do UPDATE, o resultado desse lookup ainda pode ser servido uma vez. Mas ele não volta para o L1 (por causa do tombstone) e não fica no Redis (o check pós-`cachePut` ou o DEL do listener apaga).
5. **Redis caído durante o revoke**: o revoke no DB vale e a janela volta a ≤60s. Rodar o revoke de novo resolve.
6. **PubSub desconectado**: mensagens publicadas durante a queda se perdem. Ao reassinar, o listener esvazia o L1 inteiro. O custo é 1 DB + 1 argon2 por key ativa em uso naquela réplica, coalescido pelo singleflight.

## Testes executados (FATOS: rodei e vi o resultado neste worktree)

- `gofmt -l gateway/` → vazio.
- `sqlc diff` (v1.30.0) → limpo depois do `sqlc generate`.
- `go build ./...`, `go vet ./...` e `go vet -tags integration ./...` → OK.
- `go test ./...` (raiz) → todos ok.
- `go test -race -skip 'TestGenerateAPIKey_UniquePer1000|TestGenerateAPIKey_Format'` em `internal/auth`, `internal/admin`, `cmd/gatewayctl`, `cmd/gateway` → ok.
- Testes novos do auth rodados com `-race -count=3`, todos PASS:
  - `TestRevocationListener_TwoReplicasRejectWithinOneSecond`: 2 Verifiers com L1 aquecido, revoke, os dois rejeitam em <1s.
  - `TestEvictL1_TombstoneBlocksInFlightRepopulate`
  - `TestRevocationListener_FlushesL1OnResubscribe`: miniredis derrubado e religado no mesmo endereço, L1 esvaziado.
- RED confirmado: antes da implementação o pacote auth não compilava (`undefined: InvalidateKey`).

## NÃO executado (precisa de Docker → orquestrador)

- `go test -tags integration ./internal/integration_test/ -run TestIntegration_02_AuthFlow`. Reescrevi o teste: 2 Verifiers, `auth.RevokeAPIKey` sem FlushDB, as duas réplicas têm que devolver exatamente `ErrInvalidAPIKey` em <1s, e o 2º revoke é idempotente.
- `go test -tags integration ./cmd/gatewayctl/ -run TestRunPrimaryLifecycles`. Só passa pelo vet.
- A CTE `RevokeAPIKeyReturningHash` só foi validada pelo parser do sqlc, nunca contra um Postgres real.

## Deviations from Plan

1. **[Rule 2] Tombstone no L1 + DEL pós-`cachePut`**: não estava no plano. Sem isso, um lookup que já estava em voo durante o revoke repovoaria o L1 por 30s e o Redis por 60s, e a revogação deixaria de ser imediata justamente nesse caso.
2. **[Rule 2] Flush do L1 a cada (re)subscribe**: cobre as mensagens perdidas enquanto o PubSub estava desconectado.
3. **Asserção "imediata" no teste de integração**: a evicção do L1 chega via Pub/Sub, que é assíncrono. Por isso o teste faz polling até 1s e depois exige exatamente `ErrInvalidAPIKey`. Fazer a asserção síncrona logo depois do revoke ficaria flaky.
4. **Query nova em vez de alterar `RevokeAPIKey`**: o plano permite as duas opções. A query `RevokeAPIKey` antiga continua gerada, mas ficou sem caller em produção.
5. Copiei `.planning/clickup-active-task.json` do repo principal (é gitignored) por precaução, conforme as constraints.

## FATOS vs HIPÓTESES

**FATOS (com fonte):**
- O go-redis v9.18.0 reassina sozinho: `pubsub.go:676-694` faz health-check com PING a cada 3s e reconecta. O doc de `ChannelWithSubscriptions` (`pubsub.go:575-577`) diz "Subscription messages can be used to detect reconnections". O teste de flush pós-restart do miniredis passou.
- `GetActiveKeyByLookupHash` filtra `status='active'` (`db/queries/auth.sql:18`), então key revogada vira `ErrInvalidAPIKey`.
- O `cmd/gateway` passa um `*redis.Client` não-nil para o admin handler, porque `redisx.NewClient` faz ping fail-fast no boot (`main.go:231`).

**HIPÓTESES:**
- HIPÓTESE: o container de prod do gatewayctl (dentro da task do gateway) tem as mesmas envs `AI_GATEWAY_REDIS_*` e alcança o Redis. É provável porque roda no mesmo container, mas não verifiquei em prod. Para resolver: rodar `gatewayctl key revoke` numa key de teste em prod e ver `cache_invalidated=true`.
- HIPÓTESE: a CTE roda sem erro no Postgres de prod (o `$1` uuid é usado duas vezes). Para resolver: a integração `TestIntegration_02_AuthFlow` no orquestrador.
- HIPÓTESE: a latência real do Pub/Sub entre réplicas no worker-vm fica bem abaixo de 1s. No miniredis local ficou em ms, mas não medi em prod.

## Known Stubs

Nenhum.

## Threat Flags

| Flag | File | Description |
|------|------|-------------|
| threat_flag: new-redis-channel | gateway/internal/redisx/apikey.go | Canal `gw:apikey:revoked`. Quem consegue publicar nele só provoca evicção de L1 (DoS de cache, não bypass de auth): o payload é validado como hex de 32 bytes e a key crua nunca trafega. Redis já é trust boundary interna. |

## Self-Check: PASSED

- FOUND: gateway/internal/auth/revoke.go, gateway/internal/auth/revoke_test.go, gateway/internal/redisx/apikey.go, gateway/cmd/gatewayctl/key_revoke_test.go
- FOUND commits: 787d3db, 390d41b, 7fdac9f
