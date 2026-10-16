package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

// Consulta de cota que falha porque o CLIENTE cancelou não é queda do banco:
// grava 499. Com o cliente conectado, segue o fail-closed 503.
func TestHandleQuotaError_ClientClosedIs499(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rw := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/rerank", nil).WithContext(ctx)
	if handleQuotaError(rw, r, "t", ErrQuotaCheckUnavailable, "daily", false, nil) {
		t.Fatal("não deveria seguir para o próximo handler")
	}
	if rw.Code != httpx.StatusClientClosedRequest {
		t.Fatalf("status = %d, want 499", rw.Code)
	}

	rw = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/v1/rerank", nil)
	handleQuotaError(rw, r, "t", ErrQuotaCheckUnavailable, "daily", false, nil)
	if rw.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (fail-closed com cliente conectado)", rw.Code)
	}
}
