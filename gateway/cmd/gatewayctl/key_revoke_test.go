// Package main (key_revoke_test.go): quick 260930-wpv — `gatewayctl key
// revoke` passa por auth.RevokeAPIKey (DB + DEL cache + PUBLISH).
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

type fakeRevokeQueries struct {
	found   bool
	revoked bool
	hash    []byte
}

func (f *fakeRevokeQueries) GetAPIKeyByID(_ context.Context, id uuid.UUID) (gen.GetAPIKeyByIDRow, error) {
	if !f.found {
		return gen.GetAPIKeyByIDRow{}, pgx.ErrNoRows
	}
	return gen.GetAPIKeyByIDRow{ID: id, KeyPrefix: "ifix_sk_****abcd"}, nil
}

func (f *fakeRevokeQueries) RevokeAPIKeyReturningHash(_ context.Context, _ uuid.UUID) (gen.RevokeAPIKeyReturningHashRow, error) {
	now := !f.revoked
	f.revoked = true
	return gen.RevokeAPIKeyReturningHashRow{KeyLookupHash: f.hash, RevokedNow: now}, nil
}

func TestRevokeKey_InvalidatesCacheAndIsIdempotent(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	hash := bytes.Repeat([]byte{0xab}, 32)
	cacheKey := "gw:apikey:" + strings.Repeat("ab", 32)
	_ = mr.Set(cacheKey, `{"status":"active"}`)
	q := &fakeRevokeQueries{found: true, hash: hash}
	id := uuid.New()

	var out, errb bytes.Buffer
	if code := revokeKey(context.Background(), q, rdb, id, &out, &errb, discardKeyLog()); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "revoked: id="+id.String()) || !strings.Contains(out.String(), "cache_invalidated=true") {
		t.Fatalf("stdout=%q", out.String())
	}
	if mr.Exists(cacheKey) {
		t.Fatal("cache Redis não invalidado")
	}

	// Segunda vez: already revoked, mas re-invalida.
	_ = mr.Set(cacheKey, `{"status":"active"}`)
	out.Reset()
	if code := revokeKey(context.Background(), q, rdb, id, &out, &errb, discardKeyLog()); code != 0 {
		t.Fatalf("exit 2ª=%d", code)
	}
	if !strings.Contains(out.String(), "already revoked") || mr.Exists(cacheKey) {
		t.Fatalf("2ª chamada: stdout=%q cache_exists=%v", out.String(), mr.Exists(cacheKey))
	}
}

func TestRevokeKey_NoRedisWarnsAndSucceeds(t *testing.T) {
	q := &fakeRevokeQueries{found: true, hash: bytes.Repeat([]byte{1}, 32)}
	var out, errb bytes.Buffer
	if code := revokeKey(context.Background(), q, nil, uuid.New(), &out, &errb, discardKeyLog()); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(errb.String(), "60s") {
		t.Fatalf("stderr deveria avisar janela de 60s: %q", errb.String())
	}
	if !strings.Contains(out.String(), "cache_invalidated=false") {
		t.Fatalf("stdout=%q", out.String())
	}
}

func TestRevokeKey_NotFound(t *testing.T) {
	q := &fakeRevokeQueries{found: false}
	var out, errb bytes.Buffer
	if code := revokeKey(context.Background(), q, nil, uuid.New(), &out, &errb, discardKeyLog()); code != 1 {
		t.Fatalf("exit=%d want 1", code)
	}
	if q.revoked {
		t.Fatal("não deveria chamar revoke para id inexistente")
	}
}
