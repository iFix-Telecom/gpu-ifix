package auth

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = c.Close() })
	return mr, c
}

func newTestVerifier(t *testing.T) (*miniredis.Miniredis, *Verifier) {
	t.Helper()
	mr, c := newTestRedis(t)
	v := &Verifier{redis: c}
	return mr, v
}

func TestCache_PutGetRoundTrip(t *testing.T) {
	_, v := newTestVerifier(t)
	ctx := context.Background()
	raw := "ifix_sk_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	want := cacheEntry{
		TenantID:  "tid-1",
		APIKeyID:  "kid-1",
		DataClass: DataClassNormal,
		Status:    "active",
		KeyPrefix: "ifix_sk_****abcd",
	}
	if err := v.cachePut(ctx, raw, want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok, err := v.cacheGet(ctx, raw)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok {
		t.Fatal("ok=false after put")
	}
	if got != want {
		t.Errorf("got %+v want %+v", got, want)
	}
}

func TestCache_Miss(t *testing.T) {
	_, v := newTestVerifier(t)
	_, ok, err := v.cacheGet(context.Background(), "ifix_sk_zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ok {
		t.Fatal("expected miss")
	}
}

func TestCache_TTLExpiry(t *testing.T) {
	mr, v := newTestVerifier(t)
	ctx := context.Background()
	raw := "ifix_sk_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := v.cachePut(ctx, raw, cacheEntry{Status: "active", DataClass: DataClassNormal}); err != nil {
		t.Fatalf("put: %v", err)
	}
	mr.FastForward(cacheTTL + time.Second)
	_, ok, err := v.cacheGet(ctx, raw)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ok {
		t.Fatal("entry should have expired")
	}
}

func TestCacheKeyFor_Stable(t *testing.T) {
	a := cacheKeyFor("ifix_sk_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	b := cacheKeyFor("ifix_sk_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if a != b {
		t.Fatalf("not deterministic: %s != %s", a, b)
	}
	if a == "" {
		t.Fatal("empty cache key")
	}
}

func TestCacheKeyFor_DifferentKeysDiffer(t *testing.T) {
	a := cacheKeyFor("ifix_sk_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	b := cacheKeyFor("ifix_sk_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if a == b {
		t.Fatalf("collision: %s", a)
	}
}

func TestNegCache_TTLExpiry(t *testing.T) {
	mr, v := newTestVerifier(t)
	ctx := context.Background()
	raw := "ifix_sk_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := v.negCachePut(ctx, raw); err != nil {
		t.Fatalf("neg put: %v", err)
	}
	hit, err := v.negCacheCheck(ctx, raw)
	if err != nil {
		t.Fatalf("neg check: %v", err)
	}
	if !hit {
		t.Fatal("expected neg cache hit immediately after put")
	}
	mr.FastForward(negCacheTTL + time.Second)
	hit, err = v.negCacheCheck(ctx, raw)
	if err != nil {
		t.Fatalf("neg check post-expiry: %v", err)
	}
	if hit {
		t.Fatal("neg cache entry should have expired")
	}
}
