package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// O TimeoutHandler da stdlib escreve 503 quando o cliente cancela; com o
// wrapper isso vira 499. O estouro do prazo do próprio gateway segue 503.
func TestRewriteClientClosed_TimeoutHandler(t *testing.T) {
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusOK)
		}
	})

	t.Run("cliente cancela → 499", func(t *testing.T) {
		h := RewriteClientClosed(http.TimeoutHandler(slow, 5*time.Second, "timeout"))
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(50*time.Millisecond, cancel)
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, httptest.NewRequest(http.MethodPost, "/v1/rerank", nil).WithContext(ctx))
		if rw.Code != StatusClientClosedRequest {
			t.Fatalf("status = %d, want 499", rw.Code)
		}
	})

	t.Run("prazo do gateway estoura → 503", func(t *testing.T) {
		h := RewriteClientClosed(http.TimeoutHandler(slow, 50*time.Millisecond, "timeout"))
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, httptest.NewRequest(http.MethodPost, "/v1/rerank", nil))
		if rw.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rw.Code)
		}
	})

	t.Run("503 real com cliente conectado passa intacto", func(t *testing.T) {
		h := RewriteClientClosed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, httptest.NewRequest(http.MethodPost, "/v1/rerank", nil))
		if rw.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rw.Code)
		}
	})
}

// O wrapper fica na cadeia do /v1 inteiro: não pode esconder http.Flusher do
// streaming SSE do chat.
func TestRewriteClientClosed_KeepsFlusher(t *testing.T) {
	var ok bool
	h := RewriteClientClosed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, ok = w.(http.Flusher)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	if !ok {
		t.Fatal("ResponseWriter perdeu http.Flusher")
	}
}
