package auth

// Quick 260930-vkt: testes de singleflight + L1 do Verifier. Reaproveitam
// fakeQueries/newTestVerifierFull de apikey_test.go.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countArgon2 troca v.verifyHash por um wrapper que conta chamadas argon2.
func countArgon2(v *Verifier) *int64 {
	var n int64
	orig := v.verifyHash
	v.verifyHash = func(raw, hash string) (bool, error) {
		atomic.AddInt64(&n, 1)
		return orig(raw, hash)
	}
	return &n
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout esperando %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestVerify_SingleflightCoalescesConcurrentMiss(t *testing.T) {
	_, q, v, _ := newTestVerifierFull(t)
	argon := countArgon2(v)
	raw, _, _, _, _ := GenerateAPIKey()
	row := q.addKey(t, raw, "active", "normal")
	q.gate = make(chan struct{})

	const N = 50
	var wg sync.WaitGroup
	errs := make([]error, N)
	tenants := make([]string, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ac, err := v.Verify(context.Background(), raw)
			errs[i] = err
			tenants[i] = ac.TenantID
		}(i)
	}
	waitFor(t, func() bool { return atomic.LoadInt64(&q.entered) >= 1 }, "líder no lookup")
	// Dá tempo para os outros 49 entrarem no singleflight antes de liberar.
	time.Sleep(200 * time.Millisecond)
	close(q.gate)
	wg.Wait()

	for i := 0; i < N; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if tenants[i] != row.TenantID.String() {
			t.Fatalf("goroutine %d tenant=%q want %q", i, tenants[i], row.TenantID)
		}
	}
	if got := atomic.LoadInt64(&q.lookupCalls); got != 1 {
		t.Fatalf("lookupCalls=%d want 1 (singleflight)", got)
	}
	if got := atomic.LoadInt64(argon); got != 1 {
		t.Fatalf("argon2 calls=%d want 1 (singleflight)", got)
	}
}

func TestVerify_SingleflightDistinctKeysNotCoalesced(t *testing.T) {
	_, q, v, _ := newTestVerifierFull(t)
	rawA, _, _, _, _ := GenerateAPIKey()
	rawB, _, _, _, _ := GenerateAPIKey()
	q.addKey(t, rawA, "active", "normal")
	q.addKey(t, rawB, "active", "sensitive")
	q.gate = make(chan struct{})

	var wg sync.WaitGroup
	var errA, errB error
	var acA, acB AuthContext
	wg.Add(2)
	go func() { defer wg.Done(); acA, errA = v.Verify(context.Background(), rawA) }()
	go func() { defer wg.Done(); acB, errB = v.Verify(context.Background(), rawB) }()
	waitFor(t, func() bool { return atomic.LoadInt64(&q.entered) >= 2 }, "2 lookups em voo")
	close(q.gate)
	wg.Wait()
	if errA != nil || errB != nil {
		t.Fatalf("errA=%v errB=%v", errA, errB)
	}
	if acA.DataClass != DataClassNormal || acB.DataClass != DataClassSensitive {
		t.Fatalf("resultado misturado entre keys: A=%s B=%s", acA.DataClass, acB.DataClass)
	}
	if got := atomic.LoadInt64(&q.lookupCalls); got != 2 {
		t.Fatalf("lookupCalls=%d want 2 (keys distintas não coalescem)", got)
	}
}

func TestVerify_L1ServesWhenRedisDown(t *testing.T) {
	mr, q, v, _ := newTestVerifierFull(t)
	argon := countArgon2(v)
	raw, _, _, _, _ := GenerateAPIKey()
	q.addKey(t, raw, "active", "normal")
	if _, err := v.Verify(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	mr.Close() // Redis passa a dar erro em GET.
	for i := 0; i < 20; i++ {
		if _, err := v.Verify(context.Background(), raw); err != nil {
			t.Fatalf("iter %d com Redis fora: %v", i, err)
		}
	}
	if got := atomic.LoadInt64(&q.lookupCalls); got != 1 {
		t.Fatalf("lookupCalls=%d want 1 (L1 deve servir)", got)
	}
	if got := atomic.LoadInt64(argon); got != 1 {
		t.Fatalf("argon2 calls=%d want 1 (L1 deve servir)", got)
	}
}

func TestVerify_L1ExpiresAndRevocationPropagates(t *testing.T) {
	mr, q, v, _ := newTestVerifierFull(t)
	var mu sync.Mutex
	now := time.Now()
	v.l1.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	raw, _, _, _, _ := GenerateAPIKey()
	q.addKey(t, raw, "active", "normal")
	if _, err := v.Verify(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	// Revoga: a query real filtra status='active' → ErrNoRows.
	q.mu.Lock()
	delete(q.rows, hexLookup(raw))
	q.mu.Unlock()
	mr.FlushAll()

	// Dentro da janela L1 ainda passa (documenta a janela ≤ l1TTL).
	if _, err := v.Verify(context.Background(), raw); err != nil {
		t.Fatalf("dentro da janela L1 esperava sucesso, got %v", err)
	}
	mu.Lock()
	now = now.Add(l1TTL + time.Second)
	mu.Unlock()
	if _, err := v.Verify(context.Background(), raw); !errors.Is(err, ErrInvalidAPIKey) {
		t.Fatalf("após l1TTL err=%v want ErrInvalidAPIKey (revogação propagada)", err)
	}
	if l1TTL > cacheTTL {
		t.Fatalf("l1TTL=%s > cacheTTL=%s viola D-A2", l1TTL, cacheTTL)
	}
}

func TestVerify_ErrorsNotCachedInL1(t *testing.T) {
	_, q, v, _ := newTestVerifierFull(t)
	raw, _, _, _, _ := GenerateAPIKey()
	q.addKey(t, raw, "active", "normal")
	q.mu.Lock()
	q.forceErr = errors.New("db down")
	q.mu.Unlock()
	if _, err := v.Verify(context.Background(), raw); err == nil {
		t.Fatal("esperava erro de DB")
	}
	if n := v.l1.len(); n != 0 {
		t.Fatalf("l1.len=%d want 0 após erro de DB", n)
	}
	q.mu.Lock()
	q.forceErr = nil
	q.mu.Unlock()
	if _, err := v.Verify(context.Background(), raw); err != nil {
		t.Fatalf("após DB voltar: %v", err)
	}
	if got := atomic.LoadInt64(&q.lookupCalls); got != 2 {
		t.Fatalf("lookupCalls=%d want 2 (erro não cacheado)", got)
	}

	// Key desconhecida e key revogada nunca entram no L1.
	_, q2, v2, _ := newTestVerifierFull(t)
	if _, err := v2.Verify(context.Background(), "ifix_sk_zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"); !errors.Is(err, ErrInvalidAPIKey) {
		t.Fatalf("unknown err=%v", err)
	}
	rawR, _, _, _, _ := GenerateAPIKey()
	q2.addKey(t, rawR, "revoked", "normal")
	if _, err := v2.Verify(context.Background(), rawR); !errors.Is(err, ErrRevokedAPIKey) {
		t.Fatalf("revoked err=%v", err)
	}
	if n := v2.l1.len(); n != 0 {
		t.Fatalf("l1.len=%d want 0 (inválida/revogada não entram no L1)", n)
	}
}

func TestVerify_LeaderCancelDoesNotFailWaiters(t *testing.T) {
	_, q, v, _ := newTestVerifierFull(t)
	raw, _, _, _, _ := GenerateAPIKey()
	q.addKey(t, raw, "active", "normal")
	q.gate = make(chan struct{})

	leaderCtx, cancel := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, err := v.Verify(leaderCtx, raw)
		leaderDone <- err
	}()
	waitFor(t, func() bool { return atomic.LoadInt64(&q.entered) >= 1 }, "líder no lookup")

	waiterDone := make(chan error, 1)
	go func() {
		_, err := v.Verify(context.Background(), raw)
		waiterDone <- err
	}()
	time.Sleep(100 * time.Millisecond) // waiter entra no singleflight
	cancel()
	if err := <-leaderDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("líder err=%v want context.Canceled", err)
	}
	close(q.gate)
	if err := <-waiterDone; err != nil {
		t.Fatalf("waiter falhou por cancelamento do líder: %v", err)
	}
	if got := atomic.LoadInt64(&q.lookupCalls); got != 1 {
		t.Fatalf("lookupCalls=%d want 1", got)
	}
}
