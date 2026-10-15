// Package proxy (anthropic_director.go): passo ① da cadeia LLM externa
// (Phase 23, HAIKU-01/02/05) — Claude Haiku 5.5 direto na Messages API da
// Anthropic, com os créditos de API das contas Max.
//
// Construído sobre o mesmo httputil.ReverseProxy dos outros upstreams (sem
// SDK como transporte):
//
//   - Director: recebe chat/completions OpenAI, resolve modelo + effort e
//     traduz para POST /v1/messages (TranslateOpenAIToMessages).
//   - Transport, de fora para dentro: anthropicLocalGuard (respostas locais
//     sem rede) → NewAnthropicKeyPoolTransport (x-api-key do pool, troca de
//     chave pré-byte) → NewChatFallthroughTransport (dial/timeout de cabeçalho
//     cascateiam) → http.Transport.
//   - ModifyResponse: AnthropicTranslateInterceptor (saída → OpenAI) →
//     ChatRetryableStatusInterceptor (429/5xx/529 cascateiam) → interceptors
//     do main (audit, toolcall, usage).
//
// Exportado em `proxy` (e não montado no main) para que o teste de integração
// da cadeia (23-14) use o construtor real.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)

// Padrões do passo ① quando o campo da config vem zerado.
const (
	// anthropicDefaultHeaderTimeout: resposta não-streaming com effort high
	// pode levar dezenas de segundos até o primeiro header (Pitfall 4).
	anthropicDefaultHeaderTimeout    = 120 * time.Second
	anthropicDefaultThinkingHeadroom = 4096
	anthropicDefaultMaxTokensCap     = 128000
	// anthropicDefaultModel é o ID nativo quando o resolver não tem linha
	// (alias, anthropic-haiku).
	anthropicDefaultModel = "claude-haiku-5-5"
	// anthropicDefaultUpstreamName é o nome do upstream na tabela upstreams.
	anthropicDefaultUpstreamName = "anthropic-haiku"
)

// anthropicInvalidRequestBody é a resposta sintética (formato de erro da
// Anthropic) para um request que o tradutor não consegue traduzir. O
// AnthropicTranslateInterceptor a converte em envelope OpenAI.
const anthropicInvalidRequestBody = `{"type":"error","error":{"type":"invalid_request_error","message":"invalid chat completion request"}}`

// AnthropicProxyConfig configura BuildAnthropicProxy.
type AnthropicProxyConfig struct {
	// Upstream é a base da API (ex.: https://api.anthropic.com).
	Upstream *url.URL
	// Pool são as chaves das contas Max (obrigatório).
	Pool *KeyPool
	// Resolver resolve o alias do cliente para o ID nativo (nil = fallback).
	Resolver *models.Resolver
	// UpstreamName ("" = "anthropic-haiku") — chave do resolver e rótulo de log.
	UpstreamName string
	// Tenants fornece o override de effort por tenant (nil = sem camada tenant).
	Tenants TenantEffortLookup
	// HeaderTimeout é o ResponseHeaderTimeout (0 = 120 s).
	HeaderTimeout time.Duration
	// DefaultMaxTokens quando o cliente não informa (0 = 16000).
	DefaultMaxTokens int
	// ThinkingHeadroom somado ao max_tokens do cliente (0 = 4096).
	ThinkingHeadroom int
	// MaxTokensCap é o teto do max_tokens (0 = 128000).
	MaxTokensCap int
	// ToolLoopMode ∈ omit|disable|skip ("" = omit).
	ToolLoopMode string
	// Interceptors extras, nesta ordem: audit, toolcall, usage. Recebem o body
	// já traduzido para OpenAI.
	Interceptors []ProxyResponseInterceptor
	// Log (nil = slog.Default()).
	Log *slog.Logger
}

// BuildAnthropicProxy monta o ReverseProxy do passo ①. Erro só para config
// inválida (Upstream sem scheme/host, Pool nil).
func BuildAnthropicProxy(cfg AnthropicProxyConfig) (*httputil.ReverseProxy, error) {
	if cfg.Upstream == nil || cfg.Upstream.Scheme == "" || cfg.Upstream.Host == "" {
		return nil, errors.New("anthropic proxy: upstream url inválida (scheme/host ausente)")
	}
	if cfg.Pool == nil {
		return nil, errors.New("anthropic proxy: pool de chaves ausente")
	}
	if cfg.UpstreamName == "" {
		cfg.UpstreamName = anthropicDefaultUpstreamName
	}
	if cfg.HeaderTimeout <= 0 {
		cfg.HeaderTimeout = anthropicDefaultHeaderTimeout
	}
	if cfg.DefaultMaxTokens <= 0 {
		cfg.DefaultMaxTokens = anthropicDefaultMaxTokens
	}
	if cfg.ThinkingHeadroom <= 0 {
		cfg.ThinkingHeadroom = anthropicDefaultThinkingHeadroom
	}
	if cfg.MaxTokensCap <= 0 {
		cfg.MaxTokensCap = anthropicDefaultMaxTokensCap
	}
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}

	transport := anthropicLocalGuard{
		next: NewAnthropicKeyPoolTransport(NewChatFallthroughTransport(&http.Transport{
			MaxIdleConns:          50,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: cfg.HeaderTimeout,
		}), cfg.Pool, log),
	}

	ics := append([]ProxyResponseInterceptor{AnthropicTranslateInterceptor(), ChatRetryableStatusInterceptor()}, cfg.Interceptors...)
	return &httputil.ReverseProxy{
		Director:      buildAnthropicDirector(cfg, log),
		Transport:     transport,
		FlushInterval: -1, // SSE
		ErrorHandler:  ErrorHandler(cfg.UpstreamName, log),
		// Ordem: tradução PRIMEIRO, para o corpo de erro não-2xx já estar em
		// envelope OpenAI quando o interceptor de status o guarda como lastBody.
		ModifyResponse: ComposeInterceptors(ics...),
	}, nil
}

// ---------------------------------------------------------------------------
// Director
// ---------------------------------------------------------------------------

// anthropicLocalAction é o que o guard deve responder sem ir à rede.
type anthropicLocalAction int

const (
	anthropicLocalNone anthropicLocalAction = iota
	// anthropicLocalInvalid: tradução falhou → 400 sintético.
	anthropicLocalInvalid
	// anthropicLocalToolLoopSkip: continuação de tool loop com ToolLoopMode
	// skip → vai direto ao ② sem chamar a Anthropic.
	anthropicLocalToolLoopSkip
)

type anthropicLocalCtxKey struct{}

func withAnthropicLocal(r *http.Request, a anthropicLocalAction) {
	*r = *r.WithContext(context.WithValue(r.Context(), anthropicLocalCtxKey{}, a))
	r.Body = http.NoBody
	r.ContentLength = 0
	r.Header.Del("Content-Length")
}

func anthropicLocalFrom(ctx context.Context) anthropicLocalAction {
	a, _ := ctx.Value(anthropicLocalCtxKey{}).(anthropicLocalAction)
	return a
}

func buildAnthropicDirector(cfg AnthropicProxyConfig, log *slog.Logger) func(*http.Request) {
	base := BuildDirector(cfg.Upstream) // remove auth do cliente, X-Request-ID, Accept-Encoding
	return func(r *http.Request) {
		base(r)
		if !strings.HasSuffix(r.URL.Path, "/v1/chat/completions") {
			return
		}
		var body []byte
		if r.Body != nil {
			b, err := io.ReadAll(r.Body)
			_ = r.Body.Close()
			if err != nil {
				withAnthropicLocal(r, anthropicLocalInvalid)
				return
			}
			body = b
		}

		if cfg.ToolLoopMode == ToolLoopModeSkip && IsToolLoopContinuation(body) {
			log.Debug("anthropic_director tool loop continuation skipped",
				"upstream", cfg.UpstreamName)
			withAnthropicLocal(r, anthropicLocalToolLoopSkip)
			return
		}

		var m map[string]json.RawMessage
		if err := json.Unmarshal(body, &m); err != nil {
			// Body não traduzível NUNCA vai cru à Anthropic (T-23-76).
			log.Debug("anthropic_director invalid body",
				"upstream", cfg.UpstreamName, "error_class", fmt.Sprintf("%T", err))
			withAnthropicLocal(r, anthropicLocalInvalid)
			return
		}

		effort, source := ResolveLLMEffort(r.Context(), m, cfg.Tenants)
		out, err := TranslateOpenAIToMessages(body, AnthropicRequestOptions{
			Model:            anthropicTargetModel(cfg.Resolver, modelAliasFromBody(body), cfg.UpstreamName),
			Effort:           effort,
			DefaultMaxTokens: cfg.DefaultMaxTokens,
			ThinkingHeadroom: cfg.ThinkingHeadroom,
			MaxTokensCap:     cfg.MaxTokensCap,
			Cache:            true,
			ToolLoopMode:     cfg.ToolLoopMode,
		})
		if err != nil {
			log.Debug("anthropic_director translate failed",
				"upstream", cfg.UpstreamName, "error_class", fmt.Sprintf("%T", err))
			withAnthropicLocal(r, anthropicLocalInvalid)
			return
		}
		log.Debug("anthropic_director effort resolved",
			"upstream", cfg.UpstreamName, "effort", effort, "effort_source", source)

		r.URL.Path = strings.TrimSuffix(r.URL.Path, "/v1/chat/completions") + "/v1/messages"
		r.URL.RawPath = ""
		r.Header.Set("anthropic-version", anthropicAPIVersion) // "2023-06-01"
		r.Header.Set("Content-Type", "application/json")
		// Texto puro na resposta para o tradutor (BuildDirector já remove o do
		// cliente; reforço explícito).
		r.Header.Del("Accept-Encoding")
		r.Body = io.NopCloser(bytes.NewReader(out))
		r.ContentLength = int64(len(out))
		r.Header.Set("Content-Length", strconv.Itoa(len(out)))
	}
}

// anthropicTargetModel resolve o ID nativo: linha (alias, upstream) do
// resolver; sem linha (passthrough devolve o próprio alias) e alias que não
// é um ID Claude nativo → claude-haiku-5-5.
func anthropicTargetModel(resolver *models.Resolver, alias, upstreamName string) string {
	if resolver != nil && alias != "" {
		target := resolver.Resolve(alias, upstreamName)
		if target != "" && (target != alias || strings.HasPrefix(target, "claude-")) {
			return target
		}
	}
	if strings.HasPrefix(alias, "claude-") {
		return alias
	}
	return anthropicDefaultModel
}

// ---------------------------------------------------------------------------
// Guard local (sem rede)
// ---------------------------------------------------------------------------

// anthropicToolLoopSkipError: continuação de tool loop pulada no ① (modo
// skip). Cascateia (Is errUpstreamRetryable) sem contar no breaker — a
// Anthropic nem foi chamada.
type anthropicToolLoopSkipError struct{}

func (*anthropicToolLoopSkipError) Error() string {
	return errUpstreamRetryable.Error() + " (anthropic: tool loop continuation skipped)"
}

func (*anthropicToolLoopSkipError) Is(target error) bool { return target == errUpstreamRetryable }

// SkipBreaker: decisão de roteamento, não degradação do upstream.
func (*anthropicToolLoopSkipError) SkipBreaker() bool { return true }

var errAnthropicToolLoopSkip error = &anthropicToolLoopSkipError{}

// anthropicLocalGuard responde sem rede o que o Director marcou no ctx; o
// resto segue para next (pool de chaves).
type anthropicLocalGuard struct {
	next http.RoundTripper
}

func (g anthropicLocalGuard) RoundTrip(r *http.Request) (*http.Response, error) {
	switch anthropicLocalFrom(r.Context()) {
	case anthropicLocalToolLoopSkip:
		closeReqBody(r)
		return nil, errAnthropicToolLoopSkip
	case anthropicLocalInvalid:
		closeReqBody(r)
		b := []byte(anthropicInvalidRequestBody)
		return &http.Response{
			Status:        "400 Bad Request",
			StatusCode:    http.StatusBadRequest,
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Header:        http.Header{"Content-Type": {"application/json"}, "Content-Length": {strconv.Itoa(len(b))}},
			Body:          io.NopCloser(bytes.NewReader(b)),
			ContentLength: int64(len(b)),
			Request:       r,
		}, nil
	}
	return g.next.RoundTrip(r)
}

func closeReqBody(r *http.Request) {
	if r.Body != nil {
		_ = r.Body.Close()
	}
}
