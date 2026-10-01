// Package auth (revoke.go): revogação imediata de API key (quick 260930-wpv).
//
// Antes: revoke = só UPDATE status='revoked' no DB; a key continuava válida
// até o TTL do cache Redis (≤ 60s) e, depois do L1 in-process (260930-vkt),
// "flush do Redis" deixou de ser revogação imediata.
//
// Agora o revoke (admin HTTP e gatewayctl) chama RevokeAPIKey:
//  1. UPDATE ... RETURNING key_lookup_hash (RevokeAPIKeyReturningHash);
//  2. InvalidateKey: DEL gw:apikey:<hex> + PUBLISH gw:apikey:revoked <hex>;
//  3. cada réplica (StartRevocationListener) recebe o hex, evicta o L1 com
//     tombstone e faz DEL do cache Redis de novo (cobre lookup em voo que
//     recriou a entrada depois do DEL do revogador).
//
// Best-effort no Redis: se a invalidação falhar, o revoke no DB já vale e a
// janela volta a ser a de antes (≤ 60s). A key crua nunca é logada nem
// publicada — só o hex do sha256.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// ErrCacheInvalidation sinaliza que o revoke no DB foi aplicado mas a
// invalidação do cache (Redis/PubSub) falhou — a revogação propaga em ≤ 60s
// (TTL do cache) em vez de imediatamente.
var ErrCacheInvalidation = errors.New("auth: cache invalidation failed (revocation propagates within 60s)")

// invalidateTimeout limita DEL + PUBLISH (mesmo teto dos helpers de redisx).
const invalidateTimeout = 2 * time.Second

// KeyRevoker é a superfície sqlc usada por RevokeAPIKey.
type KeyRevoker interface {
	RevokeAPIKeyReturningHash(ctx context.Context, id uuid.UUID) (gen.RevokeAPIKeyReturningHashRow, error)
}

// RevokeResult descreve o que aconteceu num revoke.
type RevokeResult struct {
	// RevokedNow: true se esta chamada fez a transição active→revoked; false
	// se a key já estava revogada (chamada idempotente).
	RevokedNow bool
	// Invalidated: true se DEL + PUBLISH no Redis deram certo.
	Invalidated bool
}

// RevokeAPIKey revoga a key no DB e invalida o cache em todas as réplicas.
//
// Erros:
//   - pgx.ErrNoRows: id inexistente (nada foi feito);
//   - erro de DB qualquer: propagado, nada foi feito;
//   - ErrCacheInvalidation (wrapped): o DB foi atualizado, só o cache falhou —
//     o chamador deve tratar como sucesso com aviso.
//
// Chamar de novo para uma key já revogada re-invalida o cache (útil quando a
// primeira tentativa pegou o Redis fora).
func RevokeAPIKey(ctx context.Context, q KeyRevoker, rdb redis.UniversalClient, id uuid.UUID) (RevokeResult, error) {
	row, err := q.RevokeAPIKeyReturningHash(ctx, id)
	if err != nil {
		return RevokeResult{}, err
	}
	res := RevokeResult{RevokedNow: row.RevokedNow}
	if err := InvalidateKey(ctx, rdb, row.KeyLookupHash); err != nil {
		return res, err
	}
	res.Invalidated = true
	return res, nil
}

// InvalidateKey apaga o cache Redis positivo da key (gw:apikey:<hex>) e
// publica o hex em redisx.APIKeyRevokedChannel para as réplicas evictarem o
// L1. lookupHash = sha256(raw) = api_keys.key_lookup_hash. Erro sempre wrapa
// ErrCacheInvalidation.
func InvalidateKey(ctx context.Context, rdb redis.UniversalClient, lookupHash []byte) error {
	if len(lookupHash) != sha256.Size {
		return fmt.Errorf("%w: lookup hash com %d bytes (want %d)", ErrCacheInvalidation, len(lookupHash), sha256.Size)
	}
	if rdb == nil {
		return fmt.Errorf("%w: redis indisponível", ErrCacheInvalidation)
	}
	hexHash := hex.EncodeToString(lookupHash)
	ctx, cancel := context.WithTimeout(ctx, invalidateTimeout)
	defer cancel()
	if err := rdb.Del(ctx, cacheKeyPrefix+hexHash).Err(); err != nil {
		return fmt.Errorf("%w: del: %v", ErrCacheInvalidation, err)
	}
	if err := rdb.Publish(ctx, redisx.APIKeyRevokedChannel, hexHash).Err(); err != nil {
		return fmt.Errorf("%w: publish: %v", ErrCacheInvalidation, err)
	}
	return nil
}

// EvictL1 remove do L1 a key cujo hex(sha256(raw)) é hexHash e grava um
// tombstone para que um lookup em voo não a reinsira.
func (v *Verifier) EvictL1(hexHash string) {
	v.l1.evict(cacheKeyPrefix + hexHash)
}

// StartRevocationListener assina redisx.APIKeyRevokedChannel numa goroutine
// que vive até ctx ser cancelado. Para cada mensagem: EvictL1 + DEL do cache
// Redis positivo (idempotente com o DEL do revogador; cobre a corrida com
// lookup em voo). A cada (re)subscribe o L1 inteiro é esvaziado, porque o
// Pub/Sub é at-most-once e mensagens publicadas durante a queda se perdem.
//
// Reconexão: o PubSub do go-redis v9 reconecta e reassina sozinho
// (health-check PING a cada 3s + reconnect em erro de conexão); a
// confirmação de reassinatura chega como *redis.Subscription em
// ChannelWithSubscriptions, que é o que dispara o flush.
//
// O canal retornado fecha na primeira assinatura confirmada (útil para
// testes e para logar no boot). Com Redis nil, retorna canal já fechado e
// não faz nada (revogação fica por TTL).
func (v *Verifier) StartRevocationListener(ctx context.Context) <-chan struct{} {
	ready := make(chan struct{})
	if v.redis == nil {
		close(ready)
		v.log.Warn("revocation listener desligado: redis nil (revogação propaga por TTL ≤ 60s)")
		return ready
	}
	ps := v.redis.Subscribe(ctx, redisx.APIKeyRevokedChannel)
	ch := ps.ChannelWithSubscriptions()
	go func() {
		defer func() { _ = ps.Close() }()
		first := true
		for {
			select {
			case <-ctx.Done():
				return
			case m, ok := <-ch:
				if !ok {
					return
				}
				switch msg := m.(type) {
				case *redis.Subscription:
					if msg.Kind != "subscribe" {
						continue
					}
					v.l1.flush()
					if first {
						first = false
						close(ready)
						v.log.Info("revocation listener subscribed", "channel", redisx.APIKeyRevokedChannel)
					} else {
						v.log.Warn("revocation listener resubscribed; L1 flushed", "channel", redisx.APIKeyRevokedChannel)
					}
				case *redis.Message:
					v.handleRevocation(ctx, msg.Payload)
				}
			}
		}
	}()
	return ready
}

func (v *Verifier) handleRevocation(ctx context.Context, hexHash string) {
	if b, err := hex.DecodeString(hexHash); err != nil || len(b) != sha256.Size {
		v.log.Warn("revocation message ignorada: payload inválido", "len", len(hexHash))
		return
	}
	v.EvictL1(hexHash)
	dctx, cancel := context.WithTimeout(ctx, invalidateTimeout)
	defer cancel()
	if err := v.redis.Del(dctx, cacheKeyPrefix+hexHash).Err(); err != nil {
		v.log.Warn("revocation: DEL do cache Redis falhou", "err", err)
	}
	v.log.Info("api key revocation applied", "lookup_hash_prefix", hexHash[:12])
}
