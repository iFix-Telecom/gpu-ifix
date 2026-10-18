package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

func gateTestLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// blockingUpstream responde só quando release fecha; started recebe um sinal
// por chamada que entrou no upstream.
func blockingUpstream(started chan<- struct{}, release <-chan struct{}, status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
}

func gateRequest(ctx context.Context) (*http.Request, *dispatchResult) {
	res := &dispatchResult{}
	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{}`))
	return r.WithContext(withDispatchResult(ctx, res)), res
}

func TestLocalGate_LimitsConcurrencyAndQueues(t *testing.T) {
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	g := NewLocalGate("local-embed", blockingUpstream(started, release, http.StatusOK), 1, 2*time.Second, gateTestLogger())

	done := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() {
			r, _ := gateRequest(context.Background())
			w := httptest.NewRecorder()
			g.ServeHTTP(w, r)
			done <- w.Code
		}()
	}
	<-started // 1ª chamada dentro do upstream
	select {
	case <-started:
		t.Fatal("2ª chamada entrou no upstream com a única vaga ocupada")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-started // 2ª entra depois que a vaga libera
	for i := 0; i < 2; i++ {
		if code := <-done; code != http.StatusOK {
			t.Fatalf("status = %d, want 200", code)
		}
	}
}

func TestLocalGate_QueueFullReturns503WithoutFallthrough(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	defer close(release)
	g := NewLocalGate("local-embed", blockingUpstream(started, release, http.StatusOK), 1, 50*time.Millisecond, gateTestLogger())

	go func() {
		r, _ := gateRequest(context.Background())
		g.ServeHTTP(httptest.NewRecorder(), r)
	}()
	<-started

	r, res := gateRequest(context.Background())
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "local_queue_full") {
		t.Fatalf("body = %s, want local_queue_full", w.Body.String())
	}
	if w.Header().Get("Retry-After") != "1" {
		t.Fatalf("Retry-After = %q, want 1", w.Header().Get("Retry-After"))
	}
	if !res.wrote || res.fallthrough_ {
		t.Fatalf("dispatchResult = %+v, want wrote=true sem fallthrough (breaker intocado)", res)
	}
}

func TestLocalGate_ClientCancelWhileQueuedReturns499(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	defer close(release)
	g := NewLocalGate("local-embed", blockingUpstream(started, release, http.StatusOK), 1, time.Minute, gateTestLogger())

	go func() {
		r, _ := gateRequest(context.Background())
		g.ServeHTTP(httptest.NewRecorder(), r)
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	r, _ := gateRequest(ctx)
	w := httptest.NewRecorder()
	time.AfterFunc(30*time.Millisecond, cancel)
	g.ServeHTTP(w, r)
	if w.Code != httpx.StatusClientClosedRequest {
		t.Fatalf("status = %d, want 499", w.Code)
	}
}

func TestLocalGate_Busy(t *testing.T) {
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	now := time.Date(2026, 10, 18, 2, 33, 0, 0, time.UTC)
	g := NewLocalGate("local-embed", blockingUpstream(started, release, http.StatusOK), 0, 0, gateTestLogger())
	g.now = func() time.Time { return now }

	if g.Busy(time.Minute) {
		t.Fatal("ocioso e sem 2xx: Busy deve ser false")
	}

	// Uma chamada termina com 200 → lastOK = now.
	release2 := make(chan struct{})
	close(release2)
	g.next = blockingUpstream(started, release2, http.StatusOK)
	r, _ := gateRequest(context.Background())
	g.ServeHTTP(httptest.NewRecorder(), r)
	<-started
	if g.Busy(time.Minute) {
		t.Fatal("sem chamada em andamento: Busy deve ser false")
	}

	// Chamada em andamento + 2xx recente → ocupado.
	g.next = blockingUpstream(started, release, http.StatusOK)
	go func() {
		r, _ := gateRequest(context.Background())
		g.ServeHTTP(httptest.NewRecorder(), r)
	}()
	<-started
	if !g.Busy(time.Minute) {
		t.Fatal("em andamento + 2xx recente: Busy deve ser true")
	}
	// 2xx velho (fora da janela) → não é mais "ocupado": travou, sonda volta.
	g.mu.Lock()
	g.now = func() time.Time { return now.Add(2 * time.Minute) }
	g.mu.Unlock()
	if g.Busy(time.Minute) {
		t.Fatal("2xx fora da janela: Busy deve ser false")
	}
	close(release)
}

func TestLocalGate_Non2xxDoesNotRefreshLastOK(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	close(release)
	g := NewLocalGate("local-embed", blockingUpstream(started, release, http.StatusBadGateway), 0, 0, gateTestLogger())
	r, _ := gateRequest(context.Background())
	g.ServeHTTP(httptest.NewRecorder(), r)
	if !g.lastOK.IsZero() {
		t.Fatalf("lastOK = %v, want zero após 502", g.lastOK)
	}
}
