// main_haiku_test.go — Phase 23-14: as rotas admin novas
// (/admin/anthropic/keys e /admin/aux-pod-lifecycles) ficam DENTRO do
// adminRouter autenticado por X-Admin-Key (T-23-46). Usa o buildRouter real
// do main; a chave válida é semeada no cache Redis do Verifier (miniredis),
// então não precisa de Postgres.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/admin"
)

func TestAdminHaikuRoutesRequireKey(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	const goodKey = "ifix_admin_teste_23_14"
	sum := sha256.Sum256([]byte(goodKey))
	if err := mr.Set("gw:admin:"+hex.EncodeToString(sum[:]),
		`{"admin_key_id":"6f1c1f7e-8e0b-4c39-9a51-6d1f2f3c0a11","label":"t","key_prefix":"ifix_admin_****"}`); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var keysHits, auxHits atomic.Int32
	keysSpy := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		keysHits.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	auxSpy := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		auxHits.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	r := buildRouter(log, time.Now(), nil, proxies{
		adminVerifier:               admin.NewVerifier(nil, rdb, log),
		adminAnthropicKeysHandler:   keysSpy,
		adminAuxPodLifecycleHandler: auxSpy,
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	do := func(method, path, key string) int {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		if key != "" {
			req.Header.Set("X-Admin-Key", key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/admin/anthropic/keys"},
		{http.MethodPost, "/admin/aux-pod-lifecycles"},
	} {
		if got := do(rt.method, rt.path, ""); got != http.StatusUnauthorized {
			t.Errorf("%s %s sem X-Admin-Key = %d, want 401", rt.method, rt.path, got)
		}
		if got := do(rt.method, rt.path, "ifix_admin_errada"); got != http.StatusUnauthorized {
			t.Errorf("%s %s com chave errada = %d, want 401", rt.method, rt.path, got)
		}
	}
	if keysHits.Load() != 0 || auxHits.Load() != 0 {
		t.Fatalf("handlers rodaram sem auth: keys=%d aux=%d", keysHits.Load(), auxHits.Load())
	}

	// Com chave válida as rotas existem e chegam ao handler (montadas no adminRouter).
	if got := do(http.MethodGet, "/admin/anthropic/keys", goodKey); got != http.StatusOK {
		t.Errorf("GET /admin/anthropic/keys autenticado = %d, want 200", got)
	}
	if got := do(http.MethodPost, "/admin/aux-pod-lifecycles", goodKey); got != http.StatusOK {
		t.Errorf("POST /admin/aux-pod-lifecycles autenticado = %d, want 200", got)
	}
	if keysHits.Load() != 1 || auxHits.Load() != 1 {
		t.Fatalf("handlers autenticados: keys=%d aux=%d, want 1/1", keysHits.Load(), auxHits.Load())
	}
}
