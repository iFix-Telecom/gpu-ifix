package auth

// Quick 260930-wpv: revogação imediata — InvalidateKey (DEL + PUBLISH),
// RevokeAPIKey (DB + invalidação), EvictL1 com tombstone e o listener
// cross-réplica.

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// fakeRevoker modela RevokeAPIKeyReturningHash: id → lookup hash; a primeira
// chamada devolve RevokedNow=true, as seguintes false (UPDATE escopado em
// status='active'); id desconhecido → pgx.ErrNoRows. onRevoke (opcional) roda
// na transição active→revoked (os testes usam para "apagar" a row ativa do
// fakeQueries do Verifier).
type fakeRevoker struct {
	mu       sync.Mutex
	hashes   map[uuid.UUID][]byte
	revoked  map[uuid.UUID]bool
	onRevoke func()
	forceErr error
}

func newFakeRevoker() *fakeRevoker {
	return &fakeRevoker{hashes: map[uuid.UUID][]byte{}, revoked: map[uuid.UUID]bool{}}
}

func (f *fakeRevoker) RevokeAPIKeyReturningHash(_ context.Context, id uuid.UUID) (gen.RevokeAPIKeyReturningHashRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forceErr != nil {
		return gen.RevokeAPIKeyReturningHashRow{}, f.forceErr
	}
	h, ok := f.hashes[id]
	if !ok {
		return gen.RevokeAPIKeyReturningHashRow{}, pgx.ErrNoRows
	}
	now := !f.revoked[id]
	f.revoked[id] = true
	if now && f.onRevoke != nil {
		f.onRevoke()
	}
	return gen.RevokeAPIKeyReturningHashRow{KeyLookupHash: h, RevokedNow: now}, nil
}

func newMiniRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return mr, rdb
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestInvalidateKey_DeletesCacheAndPublishesHex(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	raw, _, _, _, _ := GenerateAPIKey()
	lookup := LookupHash(raw)
	hexHash := hex.EncodeToString(lookup)
	if err := mr.Set(cacheKeyFor(raw), `{"status":"active"}`); err != nil {
		t.Fatal(err)
	}

	sub := rdb.Subscribe(context.Background(), redisx.APIKeyRevokedChannel)
	defer sub.Close()
	if _, err := sub.Receive(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := InvalidateKey(context.Background(), rdb, lookup); err != nil {
		t.Fatalf("InvalidateKey: %v", err)
	}
	if mr.Exists(cacheKeyFor(raw)) {
		t.Fatal("cache positivo deveria ter sido apagado")
	}
	select {
	case msg := <-sub.Channel():
		if msg.Payload != hexHash {
			t.Fatalf("payload=%q want hex do lookup hash", msg.Payload)
		}
		if msg.Payload == raw {
			t.Fatal("key crua NUNCA pode ir no canal")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PUBLISH não recebido")
	}
}

func TestInvalidateKey_RejectsBadInput(t *testing.T) {
	_, rdb := newMiniRedis(t)
	if err := InvalidateKey(context.Background(), nil, make([]byte, 32)); !errors.Is(err, ErrCacheInvalidation) {
		t.Fatalf("rdb nil: err=%v want ErrCacheInvalidation", err)
	}
	if err := InvalidateKey(context.Background(), rdb, []byte{1, 2}); !errors.Is(err, ErrCacheInvalidation) {
		t.Fatalf("hash curto: err=%v want ErrCacheInvalidation", err)
	}
}

func TestRevokeAPIKey_RevokesInvalidatesAndIsIdempotent(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	raw, _, _, _, _ := GenerateAPIKey()
	id := uuid.New()
	rv := newFakeRevoker()
	rv.hashes[id] = LookupHash(raw)

	_ = mr.Set(cacheKeyFor(raw), `{"status":"active"}`)
	res, err := RevokeAPIKey(context.Background(), rv, rdb, id)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if !res.RevokedNow || !res.Invalidated {
		t.Fatalf("res=%+v want RevokedNow+Invalidated", res)
	}
	if mr.Exists(cacheKeyFor(raw)) {
		t.Fatal("cache não invalidado")
	}

	// Segunda chamada: no-op no DB, mas re-invalida (retry cobre Redis que falhou).
	_ = mr.Set(cacheKeyFor(raw), `{"status":"active"}`)
	res, err = RevokeAPIKey(context.Background(), rv, rdb, id)
	if err != nil {
		t.Fatalf("revoke 2: %v", err)
	}
	if res.RevokedNow || !res.Invalidated {
		t.Fatalf("res2=%+v want !RevokedNow + Invalidated", res)
	}
	if mr.Exists(cacheKeyFor(raw)) {
		t.Fatal("retry deveria re-invalidar")
	}
}

func TestRevokeAPIKey_UnknownIDAndDBError(t *testing.T) {
	_, rdb := newMiniRedis(t)
	rv := newFakeRevoker()
	if _, err := RevokeAPIKey(context.Background(), rv, rdb, uuid.New()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("id desconhecido: err=%v want pgx.ErrNoRows", err)
	}
	rv.forceErr = errors.New("boom")
	_, err := RevokeAPIKey(context.Background(), rv, rdb, uuid.New())
	if err == nil || errors.Is(err, ErrCacheInvalidation) {
		t.Fatalf("erro de DB deve propagar sem ser ErrCacheInvalidation: %v", err)
	}
}

func TestRevokeAPIKey_RedisDownStillRevokes(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	raw, _, _, _, _ := GenerateAPIKey()
	id := uuid.New()
	rv := newFakeRevoker()
	rv.hashes[id] = LookupHash(raw)
	mr.Close()

	res, err := RevokeAPIKey(context.Background(), rv, rdb, id)
	if !errors.Is(err, ErrCacheInvalidation) {
		t.Fatalf("err=%v want ErrCacheInvalidation", err)
	}
	if !res.RevokedNow || res.Invalidated {
		t.Fatalf("res=%+v want RevokedNow && !Invalidated (DB vale, cache não)", res)
	}
	// rdb nil (gatewayctl sem Redis) idem.
	res, err = RevokeAPIKey(context.Background(), rv, nil, id)
	if !errors.Is(err, ErrCacheInvalidation) || res.Invalidated {
		t.Fatalf("rdb nil: res=%+v err=%v", res, err)
	}
}

func TestEvictL1_RemovesEntry(t *testing.T) {
	_, q, v, _ := newTestVerifierFull(t)
	raw, _, _, _, _ := GenerateAPIKey()
	q.addKey(t, raw, "active", "normal")
	if _, err := v.Verify(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if v.l1.len() != 1 {
		t.Fatalf("L1 len=%d want 1", v.l1.len())
	}
	v.EvictL1(hexLookup(raw))
	if v.l1.len() != 0 {
		t.Fatalf("L1 len=%d want 0 após evict", v.l1.len())
	}
}

// Lookup em voo que leu a row ANTES do revoke não pode repovoar L1 nem Redis
// depois que a revogação chegou (tombstone).
func TestEvictL1_TombstoneBlocksInFlightRepopulate(t *testing.T) {
	mr, q, v, _ := newTestVerifierFull(t)
	raw, _, _, _, _ := GenerateAPIKey()
	q.addKey(t, raw, "active", "normal")
	q.gate = make(chan struct{})

	done := make(chan error, 1)
	go func() {
		_, err := v.Verify(context.Background(), raw)
		done <- err
	}()
	waitFor(t, func() bool { return atomic.LoadInt64(&q.entered) >= 1 }, "lookup em voo")
	v.EvictL1(hexLookup(raw)) // revogação chega no meio do lookup
	close(q.gate)
	if err := <-done; err != nil {
		t.Fatalf("verify em voo: %v (resultado do snapshot antigo pode ser servido uma vez)", err)
	}
	if v.l1.len() != 0 {
		t.Fatal("tombstone deveria impedir l1.put pós-revogação")
	}
	if mr.Exists(cacheKeyFor(raw)) {
		t.Fatal("tombstone deveria impedir/desfazer cachePut no Redis pós-revogação")
	}
}

// 2 Verifiers (2 réplicas) com L1 aquecido → revoke → ambos rejeitam em < 1s.
func TestRevocationListener_TwoReplicasRejectWithinOneSecond(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	q := newFakeQueries()
	log := discardLogger()
	v1 := NewVerifierWithQueries(q, rdb, log, nil)
	rdb2 := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb2.Close() })
	v2 := NewVerifierWithQueries(q, rdb2, log, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, v := range []*Verifier{v1, v2} {
		select {
		case <-v.StartRevocationListener(ctx):
		case <-time.After(3 * time.Second):
			t.Fatal("listener não subscreveu")
		}
	}

	raw, _, _, _, _ := GenerateAPIKey()
	q.addKey(t, raw, "active", "normal")
	for _, v := range []*Verifier{v1, v2} {
		if _, err := v.Verify(ctx, raw); err != nil {
			t.Fatal(err)
		}
	}
	// Garante que ambos têm L1 aquecido (v2 pode ter batido no Redis; força L1).
	v2.l1.put(cacheKeyFor(raw), cacheEntry{Status: "active", TenantID: "t"})
	if v1.l1.len() != 1 || v2.l1.len() != 1 {
		t.Fatalf("L1 não aquecido: v1=%d v2=%d", v1.l1.len(), v2.l1.len())
	}

	id := uuid.New()
	rv := newFakeRevoker()
	rv.hashes[id] = LookupHash(raw)
	rv.onRevoke = func() {
		q.mu.Lock()
		delete(q.rows, hexLookup(raw)) // GetActiveKeyByLookupHash filtra status='active'
		q.mu.Unlock()
	}
	start := time.Now()
	if _, err := RevokeAPIKey(ctx, rv, rdb, id); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	for i, v := range []*Verifier{v1, v2} {
		for {
			_, err := v.Verify(ctx, raw)
			if errors.Is(err, ErrInvalidAPIKey) {
				break
			}
			if time.Since(start) > time.Second {
				t.Fatalf("réplica %d ainda aceita a key %v após revoke (err=%v)", i+1, time.Since(start), err)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// Após reconexão do PubSub (mensagens podem ter sido perdidas), o listener
// esvazia o L1 inteiro.
func TestRevocationListener_FlushesL1OnResubscribe(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	v := NewVerifierWithQueries(newFakeQueries(), rdb, discardLogger(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	select {
	case <-v.StartRevocationListener(ctx):
	case <-time.After(3 * time.Second):
		t.Fatal("listener não subscreveu")
	}
	v.l1.put("gw:apikey:x", cacheEntry{Status: "active"})
	time.Sleep(100 * time.Millisecond)
	if v.l1.len() != 1 {
		t.Fatalf("pré-condição: L1 len=%d want 1 (sem flush antes da queda)", v.l1.len())
	}

	addr := mr.Addr()
	mr.Close()
	if err := mr.StartAddr(addr); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for v.l1.len() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("L1 não foi esvaziado após resubscribe")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
