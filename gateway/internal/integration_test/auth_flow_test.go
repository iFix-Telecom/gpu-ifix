//go:build integration

package integration

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// TestIntegration_02_AuthFlow exercises the end-to-end auth verification
// path: GenerateAPIKey → InsertAPIKey → Verify (cache miss) → Verify
// (cache hit) → Revoke (auth.RevokeAPIKey: DB + DEL + PUBLISH) → as duas
// réplicas (Verifiers com L1 aquecido) devolvem ErrInvalidAPIKey em < 1s,
// sem FlushDB manual (quick 260930-wpv).
func TestIntegration_02_AuthFlow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, rdb := freshSchema(t, ctx)

	q := gen.New(pool)
	tenant, err := q.GetTenantBySlug(ctx, "converseai")
	if err != nil {
		t.Fatal(err)
	}

	// Issue a key via the same code path gatewayctl uses.
	raw, hash, lookupHash, prefix, err := auth.GenerateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	inserted, err := q.InsertAPIKey(ctx, gen.InsertAPIKeyParams{
		TenantID:      tenant.ID,
		KeyHash:       hash,
		KeyLookupHash: lookupHash,
		KeyPrefix:     prefix,
		DataClass:     string(auth.DataClassNormal),
	})
	if err != nil {
		t.Fatal(err)
	}

	v := auth.NewVerifier(pool, rdb, discardLogger(), nil)
	// Quick 260930-wpv: segunda réplica (cliente Redis próprio, mesmo DB/Redis).
	rdb2 := redis.NewClient(rdb.Options())
	t.Cleanup(func() { _ = rdb2.Close() })
	v2 := auth.NewVerifier(pool, rdb2, discardLogger(), nil)
	for i, vv := range []*auth.Verifier{v, v2} {
		select {
		case <-vv.StartRevocationListener(ctx):
		case <-time.After(5 * time.Second):
			t.Fatalf("réplica %d: revocation listener não assinou", i+1)
		}
	}

	// Valid — cache miss → DB + argon2.
	ac, err := v.Verify(ctx, raw)
	if err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	if ac.TenantID != tenant.ID.String() {
		t.Errorf("tenant id got %q want %q", ac.TenantID, tenant.ID.String())
	}
	if ac.DataClass != auth.DataClassNormal {
		t.Errorf("data_class got %q", ac.DataClass)
	}

	// Second call — cache hit (L1).
	ac2, err := v.Verify(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if ac2.TenantID != ac.TenantID {
		t.Errorf("cache returned different tenant: %q vs %q", ac2.TenantID, ac.TenantID)
	}

	// Aquece o L1 da réplica 2: apaga a entrada Redis para v2 ir ao DB (hit
	// no Redis não popula L1). As duas réplicas ficam com a key no L1.
	redisKey := "gw:apikey:" + hex.EncodeToString(lookupHash)
	if err := rdb.Del(ctx, redisKey).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := v2.Verify(ctx, raw); err != nil {
		t.Fatalf("réplica 2: valid key rejected: %v", err)
	}

	// Revoke pelo mesmo caminho do admin HTTP / gatewayctl: DB + DEL do cache
	// + PUBLISH. SEM FlushDB — a revogação tem que ser imediata.
	start := time.Now()
	res, err := auth.RevokeAPIKey(ctx, q, rdb, inserted.ID)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if !res.RevokedNow || !res.Invalidated {
		t.Fatalf("revoke result %+v want RevokedNow+Invalidated", res)
	}
	// After revocation the row no longer satisfies `status = 'active'` in
	// GetActiveKeyByLookupHash → ErrNoRows → ErrInvalidAPIKey. This is D-A4
	// intentional: revoked keys are indistinguishable from deleted from the
	// caller's perspective. As duas réplicas precisam rejeitar em < 1s (a
	// evicção do L1 chega via Pub/Sub, assíncrona).
	for i, vv := range []*auth.Verifier{v, v2} {
		for {
			_, err = vv.Verify(ctx, raw)
			if err != nil {
				break
			}
			if time.Since(start) > time.Second {
				t.Fatalf("réplica %d ainda aceita a key %v após revoke", i+1, time.Since(start))
			}
			time.Sleep(5 * time.Millisecond)
		}
		if err != auth.ErrInvalidAPIKey {
			t.Errorf("réplica %d: after revoke got err %v want ErrInvalidAPIKey", i+1, err)
		}
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("revogação levou %v (want < 1s)", elapsed)
	}

	// Revoke idempotente: segunda chamada não é erro e re-invalida.
	res, err = auth.RevokeAPIKey(ctx, q, rdb, inserted.ID)
	if err != nil || res.RevokedNow || !res.Invalidated {
		t.Errorf("2º revoke: res=%+v err=%v want !RevokedNow+Invalidated", res, err)
	}

	// Wrong key.
	_, err = v.Verify(ctx, "ifix_sk_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != auth.ErrInvalidAPIKey {
		t.Errorf("wrong key got err %v want ErrInvalidAPIKey", err)
	}

	// Malformed key — no DB hit.
	_, err = v.Verify(ctx, "not_ifix_prefix")
	if err != auth.ErrMalformedKey {
		t.Errorf("malformed got err %v want ErrMalformedKey", err)
	}

	// Empty key.
	_, err = v.Verify(ctx, "")
	if err != auth.ErrMissingAPIKey {
		t.Errorf("empty got err %v want ErrMissingAPIKey", err)
	}
}
