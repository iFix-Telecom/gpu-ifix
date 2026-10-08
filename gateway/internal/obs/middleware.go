// Package obs (middleware.go): request instrumentation middleware that
// finally wires obs.RequestsTotal{route,status} — a folded TODO left by
// Phase 2 (collector registered but never incremented) per STATE.md +
// CONTEXT.md D-D1 "metricsMiddleware (folded TODO)".
//
// Design:
//   - route is chi's RoutePattern (e.g. "/v1/chat/completions"), NOT the
//     raw URL path, so cardinality stays bounded per CONTEXT.md Plumbing.
//     If the middleware runs outside a chi route (e.g. unmatched path)
//     we fall back to the raw path; the /metrics dashboard treats
//     unmatched routes as an alert worth investigating.
//   - status is the status CLASS ("2xx"/"3xx"/"4xx"/"5xx") so the label
//     space stays at O(4) per route (Pitfall 13).
//   - Mounts last in the chain so it observes the final status code
//     (after auth/rate-limit/quota/dispatcher potentially wrote 4xx/5xx).
//
// Phase 7 (OBS-02): the same middleware also records the two bounded
// latency histograms from obs/metrics.go — RequestDurationByRoute and
// RequestDurationByUpstream. Each carries exactly ONE bounded label
// (route template or resolved upstream); no tenant label is added
// (07-02 Pitfall 1 — a tenant×route×upstream cross would blow the
// cardinality budget). The upstream label is resolved the same way the
// audit middleware resolves it: a route-derived default
// (llm/embed/stt), overridden by the factual upstream the dispatcher
// stamps on the request context via auditctx.WithBillingUpstream.
package obs

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
)

// statusRecorder captures the response status for post-hoc label lookup.
// Mirrors the pattern used by httpx.Logger. Preserves all underlying
// interfaces by using http.ResponseController in tests — this simple
// wrapper is sufficient for status-only capture.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader records the status then forwards.
func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

// Write ensures a default 200 is recorded even if the handler writes the
// body without calling WriteHeader first (the stdlib behavior).
func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Flush is forwarded so SSE streams keep their per-chunk flush behavior.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// RequestsMiddleware instruments every request with RequestsTotal{route,
// status_class} AFTER the response has been written. Accepts the logger
// as a debug hook; the middleware itself is log-silent in the hot path.
func RequestsMiddleware(_ *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)

			// elapsedMs is the end-to-end request duration in milliseconds
			// as a float64 — the unit the Phase 7 latency histograms use.
			elapsedMs := float64(time.Since(start).Microseconds()) / 1000.0

			// Label lookup: prefer the chi route pattern (bounded cardinality).
			// Falls back to raw path if the middleware runs outside chi.
			route := ""
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				route = rctx.RoutePattern()
			}
			if route == "" {
				route = r.URL.Path
			}
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			statusClass := strconv.Itoa(rec.status/100) + "xx"
			RequestsTotal.WithLabelValues(route, statusClass).Inc()

			// Phase 7 (OBS-02) — record the two bounded latency histograms.
			// Both labels are bounded: routeLabel is the chi template (above);
			// upstreamLabel is a route-derived default overridden by the
			// factual upstream the dispatcher stamps on the request context.
			RequestDurationByRoute.WithLabelValues(route).Observe(elapsedMs)
			RequestDurationByUpstream.WithLabelValues(upstreamLabel(r)).Observe(elapsedMs)
		})
	}
}

// upstreamLabel resolves the bounded upstream label for the latency
// histogram. It mirrors audit.Middleware's resolution: a route-derived
// default (llm/embed/stt), overridden — when present — by the factual
// upstream the dispatcher stamped via auditctx.WithBillingUpstream, or
// the schedule/dispatcher intent stamped via auditctx.WithUpstreamOverride.
// All sources are bounded value sets — never a raw path or request ID.
func upstreamLabel(r *http.Request) string {
	if u := auditctx.BillingUpstreamFrom(r.Context()); u != "" {
		return u
	}
	if u := auditctx.UpstreamOverrideFrom(r.Context()); u != "" {
		return u
	}
	return upstreamForRoute(r.URL.Path)
}

// upstreamForRoute is the route-derived upstream default. Kept in sync
// with audit.upstreamForRoute — the two middlewares deliberately agree on
// the same bounded default set so /metrics histograms and audit_log rows
// label the same request identically.
func upstreamForRoute(path string) string {
	switch {
	case strings.HasPrefix(path, "/v1/chat"):
		return "llm"
	case strings.HasPrefix(path, "/v1/embeddings"):
		return "embed"
	case strings.HasPrefix(path, "/v1/audio/speech"):
		return "tts" // quick-261007-t9f — mirrors audit.upstreamForRoute
	case strings.HasPrefix(path, "/v1/audio"):
		return "stt"
	default:
		return "unknown"
	}
}
