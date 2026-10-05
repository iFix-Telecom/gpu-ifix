// Package httpx (recoverer.go): panic-recovery middleware that writes an
// OpenAI envelope 500 and sends the panic to Sentry (if initialized).
//
// Exception: http.ErrAbortHandler (raised e.g. by httputil.ReverseProxy when
// copying the response body fails because the client went away) is an
// expected client disconnect. It is logged as WARN "client_disconnected",
// never sent to Sentry, never answered with a 500, and is re-panicked so
// net/http aborts the connection instead of cleanly terminating a truncated
// response.
package httpx

import (
	"log/slog"
	"net/http"
	"time"

	sentry "github.com/getsentry/sentry-go"
)

// Recoverer catches panics in downstream handlers, reports to Sentry (if
// initialized), writes an OpenAI envelope 500, and keeps the server alive.
// http.ErrAbortHandler is the exception: it is logged as WARN
// "client_disconnected" and re-panicked (no Sentry, no 500 write) so the
// stdlib server silently aborts the connection.
func Recoverer(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler {
					base.WarnContext(r.Context(), "client_disconnected",
						"request_id", RequestIDFrom(r.Context()),
						"method", r.Method,
						"path", r.URL.Path,
					)
					panic(http.ErrAbortHandler)
				}
				base.ErrorContext(r.Context(), "panic recovered",
					"panic", rec,
					"request_id", RequestIDFrom(r.Context()),
				)
				sentry.CurrentHub().Recover(rec)
				sentry.Flush(500 * time.Millisecond)
				WriteOpenAIError(w, http.StatusInternalServerError,
					"api_error", "internal_error",
					"The gateway encountered an unexpected error.")
			}()
			next.ServeHTTP(w, r)
		})
	}
}
