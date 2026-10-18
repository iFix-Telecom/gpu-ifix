// Package proxy (systemone.go): POST /v1/systemone — rota do Jev (quick 261018).
//
// O converseai chama a decisão tipada do modelo jev-1.13 em
// `POST https://openrouter.ai/api/v1/systemone` (NÃO é chat-completions):
//
//	requisição {"model":"jev-1.13","state":"<texto>","questions":{nome:{type,instructions,criteria?}}}
//	200        {"answers":{nome:{type,...}},"usage":{"input_tokens":N,"cost":US$}}
//
// Esta rota passa a ser o caminho autenticado por tenant para essa chamada:
// o corpo é repassado VERBATIM ao OpenRouter `<UPSTREAM_LLM_OPENROUTER_URL>/v1/systemone`
// com a MESMA credencial do openrouter-chat (UPSTREAM_LLM_OPENROUTER_AUTH_BEARER).
//
// Diferenças deliberadas em relação ao dispatcher de papel:
//   - destino ÚNICO, sem cascata e sem breaker: não existe segundo provedor do
//     Jev; quem chama já trata falha como fail-open (jev.py devolve None);
//   - timeout curto e próprio (UPSTREAM_SYSTEMONE_TIMEOUT_SECONDS): estouro →
//     504 upstream_timeout, cliente que desistiu → 499;
//   - RES-08: tenant sensitive recebe o MESMO 503 de bloqueio do dispatcher —
//     o OpenRouter é tier-1 externo, e a rota não pode virar atalho para ele.
//
// Audit: a linha vem do audit.Middleware do grupo /v1/* (corpo JSON capturado
// pelo ResponseWriter); o upstream factual é carimbado via
// auditctx.SetDispatchedUpstream, igual ao dispatchTo.
//
// Billing: route=systemone, upstream=SystemOneUpstreamName. O custo vem do
// usage.cost reportado pelo OpenRouter (cost_source=provider_reported) e
// tokens_in de usage.input_tokens; a resposta não traz `model`, então o model
// gravado é o `model` do corpo da requisição. Sem usage.cost a cobrança cai na
// tabela de preços com (model, "openrouter-systemone"), que hoje não tem linha
// → custo 0 + WARN de preço ausente (limitação documentada no PR).
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

// SystemOneUpstreamName é o rótulo de upstream gravado em audit_log e
// billing_events para a rota do Jev. Distinto de openrouter-chat para que o
// gasto do Jev não se misture com o de chat nos painéis.
const SystemOneUpstreamName = "openrouter-systemone"

// SystemOneBillingRoute é o billing_events.route da rota.
const SystemOneBillingRoute = "systemone"

// DefaultSystemOneTimeout é o prazo total quando a env não define outro. O
// cliente do converseai corta em 6 s (JEV_TIMEOUT_SECONDS); o gateway fica um
// pouco acima para que quem decide desistir seja o cliente.
const DefaultSystemOneTimeout = 10 * time.Second

// SystemOneMaxBodyBytes limita o corpo aceito. Um `choice` com o teto de 255
// critérios descritos cabe com folga; acima disso é abuso.
const SystemOneMaxBodyBytes = 1 << 20

// SystemOneConfig agrupa o que a rota precisa.
type SystemOneConfig struct {
	BaseURL      string        // UPSTREAM_LLM_OPENROUTER_URL (ex.: https://openrouter.ai/api)
	AuthBearer   string        // UPSTREAM_LLM_OPENROUTER_AUTH_BEARER (nunca logado)
	Timeout      time.Duration // prazo total da chamada; <=0 → DefaultSystemOneTimeout
	Transport    http.RoundTripper
	Log          *slog.Logger
	Interceptors []ProxyResponseInterceptor // billing (UsageInterceptor)
}

// systemOneModelKey carrega o `model` do corpo até o UsageInterceptor, que
// roda no ModifyResponse e não enxerga mais a requisição.
type systemOneModelKey struct{}

func withSystemOneModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, systemOneModelKey{}, model)
}

func systemOneModelFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(systemOneModelKey{}).(string)
	return s
}

// NewSystemOneHandler monta o handler de POST /v1/systemone. Erro só quando
// BaseURL é inválida — URL vazia é tratada por quem monta (rota 501).
func NewSystemOneHandler(cfg SystemOneConfig) (http.Handler, error) {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	if base.Scheme == "" || base.Host == "" {
		return nil, errors.New("systemone: base URL missing scheme or host")
	}
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	log = log.With("module", "SYSTEMONE")
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultSystemOneTimeout
	}
	transport := cfg.Transport
	if transport == nil {
		transport = &http.Transport{
			MaxIdleConns:          20,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: timeout,
		}
	}
	baseDirector := BuildDirector(base)
	bearer := cfg.AuthBearer
	fallbackErr := ErrorHandler(SystemOneUpstreamName, log)
	rp := &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			baseDirector(r) // remove auth do cliente, carimba X-Request-ID, junta o path
			if bearer != "" {
				r.Header.Set("Authorization", "Bearer "+bearer)
			}
		},
		Transport:      transport,
		ModifyResponse: ComposeInterceptors(cfg.Interceptors...),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if !httpx.ClientClosed(r) && isTimeoutErr(err) {
				log.WarnContext(r.Context(), "systemone upstream timeout",
					"request_id", httpx.RequestIDFrom(r.Context()),
					"timeout_s", timeout.Seconds())
				httpx.WriteOpenAIError(w, http.StatusGatewayTimeout,
					"timeout", "upstream_timeout", "The upstream did not answer in time.")
				return
			}
			fallbackErr(w, r, err)
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// RES-08: OpenRouter é externo. Tenant sensitive nunca passa.
		if ac, ok := auth.FromContext(r.Context()); ok && ac.DataClass == auth.DataClassSensitive {
			writeSensitiveBlock(w, r)
			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, SystemOneMaxBodyBytes))
		_ = r.Body.Close()
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				httpx.WriteOpenAIError(w, http.StatusRequestEntityTooLarge,
					"invalid_request_error", "request_too_large", "Request body exceeds the systemone limit.")
				return
			}
			httpx.WriteOpenAIError(w, http.StatusBadRequest,
				"invalid_request_error", "invalid_body", "Could not read the request body.")
			return
		}
		var probe struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &probe); err != nil {
			httpx.WriteOpenAIError(w, http.StatusBadRequest,
				"invalid_request_error", "invalid_body", "Request body is not valid JSON.")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		ctx = auditctx.WithBillingUpstream(ctx, SystemOneUpstreamName)
		ctx = auditctx.WithBillingRoute(ctx, SystemOneBillingRoute)
		ctx = withSystemOneModel(ctx, probe.Model)
		auditctx.SetDispatchedUpstream(httpx.RequestIDFrom(r.Context()), SystemOneUpstreamName)

		out := r.WithContext(ctx)
		out.Body = io.NopCloser(bytes.NewReader(body))
		out.ContentLength = int64(len(body))
		out.Header.Set("Content-Length", strconv.Itoa(len(body)))
		rp.ServeHTTP(w, out)
	}), nil
}

// isTimeoutErr reconhece o estouro do prazo próprio da rota (ctx) e o
// ResponseHeaderTimeout do transport.
func isTimeoutErr(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
