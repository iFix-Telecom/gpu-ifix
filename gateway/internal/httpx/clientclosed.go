// Package httpx (clientclosed.go): status 499 para o request cujo cliente
// desistiu antes da resposta.
//
// http.TimeoutHandler escreve 503 (sem corpo) tanto no estouro do próprio
// prazo quanto quando o CLIENTE cancela o contexto. O segundo caso não é falha
// de upstream, mas entrava no audit_log como 503 e inflava a taxa 5xx — ex.:
// rerank do converseai, que corta em 4 s (22 casos em 15/10/2026).
package httpx

import (
	"context"
	"errors"
	"net/http"
)

// StatusClientClosedRequest (499, convenção do nginx): o cliente fechou a
// conexão antes da resposta. Nada chega a ele; o código só fica no
// audit_log, no log de request e nas métricas.
const StatusClientClosedRequest = 499

// ClientClosed reports whether the request's context was canceled by the
// client (not by a server-side deadline).
func ClientClosed(r *http.Request) bool {
	return errors.Is(r.Context().Err(), context.Canceled)
}

// RewriteClientClosed wraps next so that a 503 written after the client
// canceled the request is recorded as 499 instead. Every other status passes
// through untouched, including a 503 from a request whose client is still
// connected (real upstream unavailability or TimeoutHandler's own deadline).
func RewriteClientClosed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&clientClosedWriter{ResponseWriter: w, r: r}, r)
	})
}

type clientClosedWriter struct {
	http.ResponseWriter
	r *http.Request
}

func (w *clientClosedWriter) WriteHeader(code int) {
	if code == http.StatusServiceUnavailable && ClientClosed(w.r) {
		code = StatusClientClosedRequest
	}
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *clientClosedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
