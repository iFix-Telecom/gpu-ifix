package proxy

import (
	"log/slog"
	"net/http/httputil"
)

// NewEmbeddingsProxy constructs the reverse proxy for POST /v1/embeddings.
// NO FlushInterval override — embeddings never stream; default (0 =
// buffered) is correct and reduces test flakiness by ensuring the whole
// response body lands in one Write from the client POV. Codex review
// [MEDIUM] 02-04 scope change.
func NewEmbeddingsProxy(upstreamURL string, log *slog.Logger, interceptors ...ProxyResponseInterceptor) (*httputil.ReverseProxy, error) {
	u, err := parseStaticUpstream("embeddings", upstreamURL)
	if err != nil {
		return nil, err
	}
	// Quick 260930-uru: alvo fixo delegado ao proxy dinâmico (mesmo
	// transport/ErrorHandler); cmd/gateway usa NewDynamicEmbeddingsProxy.
	return NewDynamicEmbeddingsProxy(staticTarget(u), log, interceptors...), nil
}
