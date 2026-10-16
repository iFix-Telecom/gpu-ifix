package proxy

import (
	"net/http"
	"testing"
	"time"
)

// O proxy de embeddings não pode cortar antes de 30 s: o lote noturno do
// chat-ifix leva 6–10 s no bge-m3 em CPU e caía em 502 com o corte de 10 s.
func TestDynamicEmbeddingsProxy_HeaderTimeout30s(t *testing.T) {
	p := NewDynamicEmbeddingsProxy(nil, discardLogger())
	rt, ok := p.Transport.(unresolvedTargetRoundTripper)
	if !ok {
		t.Fatalf("transport inesperado: %T", p.Transport)
	}
	ft, ok := rt.base.(fallthroughRoundTripper)
	if !ok {
		t.Fatalf("base inesperada: %T", rt.base)
	}
	tr, ok := ft.base.(*http.Transport)
	if !ok {
		t.Fatalf("transport base inesperado: %T", ft.base)
	}
	if tr.ResponseHeaderTimeout != 30*time.Second {
		t.Fatalf("ResponseHeaderTimeout = %s, want 30s", tr.ResponseHeaderTimeout)
	}
}
