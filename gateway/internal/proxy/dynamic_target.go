// Package proxy (dynamic_target.go): proxies cujo alvo é resolvido a cada
// request (quick 260930-uru).
//
// Os 4 upstreams do pod 3060 (local-stt, kokoro-tts, rerank-gpu, embed-gpu)
// trocam de IP:porta todo dia. Antes a URL era fixada no boot a partir da env
// do stack 38, então trocar o pod exigia recriar a task do gateway. Agora o
// alvo vem de uma TargetFunc (em produção: loader.TargetURL(name), que segue
// ai_gateway.upstreams.url_override > os.Getenv(url_env) e é recarregado por
// LISTEN/NOTIFY), e o mesmo *httputil.ReverseProxy passa a mirar o novo host
// no request seguinte, sem rebuild.
//
// Sem alvo (row ausente, desabilitada ou sem URL) o Director aponta para um
// host sentinela .invalid e o unresolvedTargetRoundTripper devolve
// errDialFailedFallthrough sem nenhum I/O de rede — o ErrorHandler suprime o
// 502 e o dispatcher cascateia para o tier-1. Isto é diferente de
// dynamicOverrideDirector (dynamic_override.go), que deixa o host vazio e gera
// um erro "no Host in request URL" que NÃO é connection-class.
package proxy

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)

// TargetFunc devolve o alvo atual do proxy. (nil,false) = sem alvo.
type TargetFunc func() (*url.URL, bool)

// unresolvedTargetHost é o host sentinela usado quando TargetFunc não tem
// alvo. TLD .invalid (RFC 2606) nunca resolve; mesmo assim o RoundTripper
// abaixo intercepta antes de qualquer DNS lookup.
const unresolvedTargetHost = "ifix-upstream-unresolved.invalid"

// staticTarget adapta uma URL fixa (constructors antigos baseados em string).
func staticTarget(u *url.URL) TargetFunc {
	return func() (*url.URL, bool) { return u, true }
}

// cachedDirector associa um *url.URL ao director construído para ele.
type cachedDirector struct {
	u *url.URL
	d func(*http.Request)
}

// dynamicTargetDirector devolve um Director que resolve o alvo por request.
//
// Custo: build(u) só é chamado quando o ponteiro *url.URL muda. O loader
// devolve o MESMO ponteiro até o próximo Refresh, então o caso comum é um
// atomic.Load + comparação de ponteiro. Duas goroutines podem construir o
// director em paralelo logo após uma troca; ambas produzem directors
// equivalentes e a última vence o cache — inofensivo.
func dynamicTargetDirector(target TargetFunc, build func(*url.URL) func(*http.Request)) func(*http.Request) {
	var cache atomic.Pointer[cachedDirector]
	return func(r *http.Request) {
		u, ok := target()
		if !ok || u == nil {
			r.URL.Scheme = "http"
			r.URL.Host = unresolvedTargetHost
			r.Host = unresolvedTargetHost
			for _, h := range clientAuthHeaders {
				r.Header.Del(h)
			}
			return
		}
		c := cache.Load()
		if c == nil || c.u != u {
			c = &cachedDirector{u: u, d: build(u)}
			cache.Store(c)
		}
		c.d(r)
	}
}

// unresolvedTargetRoundTripper curto-circuita requests marcados pelo
// dynamicTargetDirector como "sem alvo": devolve errDialFailedFallthrough
// sem tocar a rede. Qualquer outro request é delegado ao base.
type unresolvedTargetRoundTripper struct {
	base http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (u unresolvedTargetRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL != nil && r.URL.Host == unresolvedTargetHost {
		return nil, errDialFailedFallthrough
	}
	return u.base.RoundTrip(r)
}

// dynamicTransport monta a cadeia padrão: unresolved → fallthrough → base.
func dynamicTransport(base *http.Transport) http.RoundTripper {
	return unresolvedTargetRoundTripper{base: fallthroughRoundTripper{base: base}}
}

// parseStaticUpstream reproduz a validação histórica dos constructors por
// string (mesmas mensagens de erro).
func parseStaticUpstream(kind, upstreamURL string) (*url.URL, error) {
	u, err := url.Parse(upstreamURL)
	if err != nil {
		return nil, fmt.Errorf("proxy/%s: parse %q: %w", kind, upstreamURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("proxy/%s: invalid upstream url %q", kind, upstreamURL)
	}
	return u, nil
}

// NewDynamicEmbeddingsProxy é o proxy de POST /v1/embeddings com alvo
// resolvido por request. Transport/timeouts/ErrorHandler idênticos ao
// NewEmbeddingsProxy histórico. Sem FlushInterval (embeddings nunca fazem
// stream; default 0 = buffered).
func NewDynamicEmbeddingsProxy(target TargetFunc, log *slog.Logger, interceptors ...ProxyResponseInterceptor) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Director: dynamicTargetDirector(target, BuildDirector),
		Transport: dynamicTransport(&http.Transport{
			MaxIdleConns:          50,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
		}),
		ErrorHandler:   ErrorHandler("embed", log),
		ModifyResponse: ComposeInterceptors(interceptors...),
	}
}

// NewDynamicAudioProxy é o proxy local-stt de /v1/audio/transcriptions com
// alvo resolvido por request. Mantém o model-rewrite do resolver
// (BuildOpenAIWhisperDirector com bearer vazio e upstream "local-stt") e o
// sttRetryableStatusInterceptor na frente dos demais (status >= 400 cascateia
// e nunca fatura).
func NewDynamicAudioProxy(target TargetFunc, log *slog.Logger, resolver *models.Resolver, interceptors ...ProxyResponseInterceptor) *httputil.ReverseProxy {
	build := func(u *url.URL) func(*http.Request) {
		return BuildOpenAIWhisperDirector(u, "", resolver, "local-stt", log)
	}
	return &httputil.ReverseProxy{
		Director: dynamicTargetDirector(target, build),
		// FlushInterval deliberately omitted (default 0 = buffered)
		Transport: dynamicTransport(&http.Transport{
			MaxIdleConns:          20,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
		}),
		ErrorHandler: ErrorHandler("stt", log),
		ModifyResponse: ComposeInterceptors(
			append([]ProxyResponseInterceptor{sttRetryableStatusInterceptor{}}, interceptors...)...,
		),
	}
}

// NewDynamicTTSTargetProxy é o proxy kokoro-tts de /v1/audio/speech com alvo
// resolvido por request. Nome distinto de NewDynamicTTSProxy, que é o proxy
// do override emergency_pod_tts (dynamic_override.go) e não muda.
func NewDynamicTTSTargetProxy(target TargetFunc, log *slog.Logger, interceptors ...ProxyResponseInterceptor) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Director: dynamicTargetDirector(target, BuildDirector),
		// FlushInterval deliberately omitted: resposta é um corpo binário único.
		Transport: dynamicTransport(&http.Transport{
			MaxIdleConns:          20,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
		}),
		ErrorHandler:   ErrorHandler("tts", log),
		ModifyResponse: ComposeInterceptors(interceptors...),
	}
}

// NewDynamicRerankProxy é o proxy rerank-gpu de /v1/rerank com alvo
// resolvido por request. Transport idêntico ao NewRerankProxy histórico.
func NewDynamicRerankProxy(target TargetFunc, log *slog.Logger, interceptors ...ProxyResponseInterceptor) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Director: dynamicTargetDirector(target, BuildDirector),
		Transport: dynamicTransport(&http.Transport{
			MaxIdleConns:          20,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		}),
		ErrorHandler:   ErrorHandler("rerank", log),
		ModifyResponse: ComposeInterceptors(interceptors...),
	}
}
