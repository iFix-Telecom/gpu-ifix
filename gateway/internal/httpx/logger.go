// Package httpx (logger.go): per-request slog enrichment middleware.
// Builds a logger with request_id, method, path, optional client_request_id
// and stores it in ctx via WithLogger. Emits a single "request" Info record
// when the handler returns (or panics), with status + bytes + latency.
// status is the FIRST status actually written to the wire. When the handler
// unwinds with http.ErrAbortHandler (client disconnect) the record also
// carries aborted=true and the panic keeps propagating.
package httpx

import (
	"log/slog"
	"net/http"
	"time"
)

// Logger is middleware that binds a per-request logger (with module,
// request_id, client_request_id, method, path) into ctx and logs one
// summary record after the handler returns. The logger is wrapped in
// NewRedactor() upstream so sensitive attr VALUES are always redacted.
// The summary is emitted from a defer so aborted requests (re-panicked
// http.ErrAbortHandler from Recoverer) are still logged; any panic is
// re-raised after logging.
func Logger(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			reqID := RequestIDFrom(r.Context())
			cliID := ClientRequestIDFrom(r.Context())
			attrs := []any{
				"request_id", reqID,
				"method", r.Method,
				"path", r.URL.Path,
			}
			if cliID != "" {
				attrs = append(attrs, "client_request_id", cliID)
			}
			reqLog := base.With(attrs...)
			ctx := WithLogger(r.Context(), reqLog)

			sw := &statusWriter{ResponseWriter: w, status: 200}
			defer func() {
				rec := recover()
				fields := []any{
					"status", sw.status,
					"bytes", sw.bytes,
					"latency_ms", time.Since(start).Milliseconds(),
				}
				if rec == http.ErrAbortHandler {
					fields = append(fields, "aborted", true)
				}
				reqLog.Info("request", fields...)
				if rec != nil {
					panic(rec)
				}
			}()
			next.ServeHTTP(sw, r.WithContext(ctx))
		})
	}
}

// statusWriter records the first status written (explicitly or implicitly
// via Write) while forwarding every call to the wrapped ResponseWriter.
type statusWriter struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.status = http.StatusOK
		w.wroteHeader = true
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Flush passes through for SSE (reverse proxy relies on this when
// FlushInterval:-1 is configured).
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
