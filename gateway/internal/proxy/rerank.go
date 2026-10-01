// Package proxy (rerank.go): the rerank request pipeline for POST /v1/rerank.
//
// Two upstream tiers, both Infinity (michaelf34/infinity) servers speaking the
// same wire shape — POST /v1/rerank {model, query, documents[]} ->
// {results:[{index, relevance_score}], ...} — with served-model-name
// bge-reranker-v2-m3:
//
//   - tier-0 = rerank-gpu: the unified Vast pod (STT+TTS+rerank on one GPU).
//   - tier-1 = rerank-cpu: the worker-vm CPU Infinity fallback.
//
// Because both tiers serve the SAME model name there is no director rewrite:
// the proxy is a plain JSON->JSON passthrough (mold: embeddings.go), wired
// into the role-based dispatcher exactly like chat/embed/stt/tts so the
// tier-0->tier-1 breaker fallback works.
//
// ResponseHeaderTimeout is 30s: GPU rerank of a 30-doc pool answers in
// ~150-650ms, but the CPU tier-1 takes up to ~20s for the same pool — the
// timeout must give the fallback room to finish rather than tripping the
// breaker on every large CPU rerank.
package proxy

import (
	"log/slog"
	"net/http/httputil"
)

// NewRerankProxy constructs a reverse proxy for POST /v1/rerank. Same
// constructor structure as NewEmbeddingsProxy (Director=BuildDirector strips
// client auth + stamps X-Request-ID, ErrorHandler emits an OpenAI-shaped 502,
// fallthroughRoundTripper surfaces pre-byte dial failures so the dispatcher
// re-routes to the next tier). Buffered (no FlushInterval): the response is a
// single JSON body, never SSE.
func NewRerankProxy(upstreamURL string, log *slog.Logger, interceptors ...ProxyResponseInterceptor) (*httputil.ReverseProxy, error) {
	u, err := parseStaticUpstream("rerank", upstreamURL)
	if err != nil {
		return nil, err
	}
	// Quick 260930-uru: alvo fixo delegado ao proxy dinâmico (mesmo
	// transport/ErrorHandler); cmd/gateway usa NewDynamicRerankProxy.
	return NewDynamicRerankProxy(staticTarget(u), log, interceptors...), nil
}
