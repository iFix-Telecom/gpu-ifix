package audit

import (
	"net/http"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy"
)

// logWarner is the minimal interface AuditInterceptor needs from a logger.
// Using an interface keeps audit decoupled from slog and lets tests inject
// a no-op.
type logWarner interface {
	Warn(msg string, args ...any)
}

// AuditInterceptor implements proxy.ProxyResponseInterceptor. It installs a
// TeeBody on SSE responses so the middleware-captured Event gets the
// buffered response content on body close. For non-SSE, this is a no-op —
// the audit.Middleware ResponseWriter wrapper captures non-SSE bodies
// directly.
//
// The interceptor runs inside httputil.ReverseProxy.ModifyResponse, which
// fires AFTER upstream headers arrive but BEFORE the proxy writes them to
// the client. Codex review [HIGH/MEDIUM] 02-05 — no direct mutation of
// rp.ModifyResponse; this type is passed into proxy.NewChatProxy(...).
type AuditInterceptor struct {
	writer       *Writer
	onDisconnect func(requestID string)
	log          logWarner
}

// NewAuditInterceptor constructs the interceptor. onDisconnect is called
// from TeeBody.Close on any termination path (normal, abort, 5xx, cap).
// In Plan 02-05 it's a tiny closure; audit.Middleware's ServeHTTP return
// path handles Event enqueue via the ResponseWriter wrapper.
func NewAuditInterceptor(writer *Writer, onDisconnect func(requestID string), log logWarner) *AuditInterceptor {
	return &AuditInterceptor{writer: writer, onDisconnect: onDisconnect, log: log}
}

// Intercept satisfies proxy.ProxyResponseInterceptor. For SSE bodies, wrap
// resp.Body in TeeBody. For non-SSE, do nothing — the middleware
// ResponseWriter wrapper captures the body.
func (a *AuditInterceptor) Intercept(resp *http.Response) error {
	if !proxy.IsSSEResponse(resp) {
		return nil
	}
	// Request id extracted from the upstream response X-Request-ID header
	// (director sets it before upstream sees the request — same id is echoed
	// back when upstream does not override it). Fallback to empty string —
	// audit.Middleware correlates via ctx anyway.
	requestID := resp.Header.Get("X-Request-ID")
	onClose := func() {
		if a.onDisconnect != nil {
			a.onDisconnect(requestID)
		}
	}
	resp.Body = NewTeeBody(resp.Body, onClose)
	return nil
}

// Compile-time check: AuditInterceptor satisfies proxy.ProxyResponseInterceptor.
var _ proxy.ProxyResponseInterceptor = (*AuditInterceptor)(nil)
