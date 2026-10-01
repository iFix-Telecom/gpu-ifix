---
quick_id: 260930-wpv
type: quick
wave: 1
autonomous: true
clickup: 86akrbgbt
---

# Quick 260930-wpv — revoke de API key invalida cache (Redis + L1) imediatamente

## Contexto (FATOS, 2026-10-01)

- Integração pós-260930-vkt: `TestIntegration_02_AuthFlow` falha em `auth_flow_test.go:80`
  (`after revoke got err <nil> want ErrInvalidAPIKey`). O teste revoga (`q.RevokeAPIKey`) e faz
  `rdb.FlushDB`; o L1 in-process (novo, 30s) continua servindo a key.
- Revoke em prod (`internal/admin/keys_admin_http.go:223` e `cmd/gatewayctl/key.go:157`) só faz
  `UPDATE ... status='revoked'` — nunca invalidou cache. Janela de revogação hoje ≤60s (TTL Redis
  `cacheTTL`), L1 não piora o máximo mas quebra o procedimento "flush Redis = revogação imediata".
- `api_keys.key_lookup_hash` = `sha256(raw)` = `auth.LookupHash(raw)`; cache Redis positivo =
  `gw:apikey:<hex(sha256(raw))>` (`internal/auth/cache.go:42`), negativo `gw:apikey:neg:<hex>`.
  L1 usa a mesma chave `cacheKeyFor(raw)` (`internal/auth/l1cache.go`).
- Padrão pub/sub existente: `internal/redisx/shed.go:145-153` (Publish/Subscribe).
- `TestRunPrimaryLifecyclesIntegration_FetchesFromDB` / `_RespectsLimitFlag`
  (`cmd/gatewayctl/primary_lifecycles_integration_test.go:188,231`): saída é tabwriter (tabs viram
  espaços) e o teste procura `"%d\t"` → falha com output correto. Seed já corrigido na vkt.

## Objetivo

Revogação passa a ser **imediata** em todas as réplicas (melhor que antes: ≤60s → ~0s), sem depender
de flush manual do Redis.

## Task 1 — revoke com invalidação (tdd)

files: gateway/db/queries/admin.sql, gateway/internal/db/gen (sqlc v1.30.0), gateway/internal/auth/
(novo revoke.go + teste), gateway/internal/redisx (canal), gateway/internal/admin/keys_admin_http.go,
gateway/cmd/gatewayctl/key.go, gateway/cmd/gateway/main.go, gateway/internal/integration_test/auth_flow_test.go

action:
1. Query `RevokeAPIKey` passa a `:one`/`:many` com `RETURNING key_lookup_hash` (manter idempotência:
   segunda chamada → 0 rows, não é erro). Se mudar assinatura quebrar callers, criar query nova
   `RevokeAPIKeyReturningHash` e migrar os callers. `sqlc generate` sem diff residual.
2. `auth.InvalidateKey(ctx, rdb, lookupHash []byte)`: `DEL gw:apikey:<hex>` + `PUBLISH
   gw:apikey:revoked <hex>`. Best-effort: erro de Redis é logado e retornado, mas o revoke no DB já
   vale (janela volta a ≤60s só nesse caso). Nunca logar a key crua.
3. Verifier: método `EvictL1(hexHash)` + goroutine de subscribe (`StartRevocationListener(ctx, rdb, log)`)
   que evicta do L1 ao receber mensagem; reconecta com backoff (go-redis PubSub já reconecta —
   confirmar). Iniciar em main.go junto do Verifier.
4. Admin `POST /admin/keys/{id}/revoke` e `gatewayctl key revoke` chamam o revoke+invalidate.
   gatewayctl precisa de Redis: verificar se já tem config de Redis (AI_GATEWAY_REDIS_*); se não
   tiver, invalidar só via DB+... — NÃO: preferir que gatewayctl use a mesma env de Redis do gateway
   (o container de prod tem). Se Redis indisponível no gatewayctl, avisar no stderr que a revogação
   propaga em ≤60s.
5. `auth_flow_test.go`: trocar `q.RevokeAPIKey + FlushDB` pelo caminho novo de revoke (o mesmo que
   admin usa) e manter a asserção estrita `ErrInvalidAPIKey` imediata. Adicionar caso: 2 Verifiers
   (simulando 2 réplicas) com L1 aquecido → revoke → ambos rejeitam em <1s.

verify: unit tests auth (revoke/evict/listener com miniredis se já for dep; senão fake), `go test ./...`,
`-race` em auth (com `-skip 'TestGenerateAPIKey_UniquePer1000|TestGenerateAPIKey_Format'`).

done: revoke imediato provado em teste multi-verifier; admin + gatewayctl usam o caminho novo.

## Task 2 — fix asserção PrimaryLifecycles

files: gateway/cmd/gatewayctl/primary_lifecycles_integration_test.go

action: trocar `require.Contains(stdout, fmt.Sprintf("%d\t", id))` por regex multiline
`(?m)^%d\s` (e equivalentes no teste de --limit, inclusive asserções de ausência). Manter ordem DESC.

verify: `go vet -tags integration ./...` (Docker roda no orquestrador).

done: asserções independentes de tab vs espaço.

## Restrições

- gofmt vazio; go build/vet (+ -tags integration); go test ./...; sem push/deploy; sem docker.
- Commits atômicos por task, terminando com `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- SUMMARY em `260930-wpv-SUMMARY.md` (não commitar).
