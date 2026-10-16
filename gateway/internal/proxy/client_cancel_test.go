// Package proxy (client_cancel_test.go): o cliente que desiste no meio da
// cascata tier-1 não é "upstream indisponível". Antes deste teste o gateway
// gravava 503 upstream_unavailable no audit (rerank do converseai com timeout
// de 4 s, 22 casos em 15/10/2026), inflando a taxa de erro de upstream.
package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

func TestDispatcher_ClientCancelDuringTier1_Is499(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	const deadTier0 = "http://127.0.0.1:1"
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[]}`))
		}
	}))
	defer slow.Close()

	loader := upstreams.NewLoaderInMemory(
		upstreams.UpstreamConfig{Name: "rerank-gpu", Role: "rerank", Tier: 0, URL: deadTier0, Enabled: true},
		upstreams.UpstreamConfig{Name: "rerank-cpu", Role: "rerank", Tier: 1, URL: slow.URL, Enabled: true},
	)
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 1, Cooldown: time.Minute},
		loader.Names(),
	)
	// Tier-0 OPEN, como em prod com a 3060 desligada.
	_, _ = bs.Execute("rerank-gpu", func() (*http.Response, error) {
		return nil, &breaker.HTTPError{Status: 503, Msg: "pod off"}
	})

	t0, err := NewRerankProxy(deadTier0, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	t1, err := NewRerankProxy(slow.URL, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	disp := NewDispatcher(DispatcherConfig{
		Role:    "rerank",
		Loader:  loader,
		Breaker: bs,
		Proxies: map[string]http.Handler{"rerank-gpu": t0, "rerank-cpu": t1},
		Log:     discardLogger(),
	})

	r := httptest.NewRequest(http.MethodPost, "/v1/rerank",
		strings.NewReader(`{"model":"m","query":"q","documents":["a","b"]}`))
	r.Header.Set("Content-Type", "application/json")
	cctx, ccancel := context.WithCancel(auth.WithContext(r.Context(), auth.AuthContext{
		TenantID:  "00000000-0000-0000-0000-000000000001",
		APIKeyID:  "00000000-0000-0000-0000-000000000002",
		DataClass: auth.DataClassNormal,
	}))
	time.AfterFunc(200*time.Millisecond, ccancel)
	rw := httptest.NewRecorder()
	disp.ServeHTTP(rw, r.WithContext(cctx))

	if rw.Code != httpx.StatusClientClosedRequest {
		t.Fatalf("status = %d (body=%q), want %d (cliente desistiu, não é upstream indisponível)",
			rw.Code, rw.Body.String(), httpx.StatusClientClosedRequest)
	}
	if st := bs.EffectiveState("rerank-cpu"); st.String() != "closed" {
		t.Errorf("breaker rerank-cpu = %s, want closed (cancelamento do cliente não é falha do upstream)", st)
	}
}
