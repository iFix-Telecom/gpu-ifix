package audit

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

// IdempotencyReplayedSetter is implemented by the audit response-writer
// wrapper installed by audit.Middleware. Plan 02-06 (idempotency
// middleware) type-asserts the outer http.ResponseWriter against this
// interface and calls SetIdempotencyReplayed(true) on the replay path.
// The flag is read by audit.Middleware AFTER next.ServeHTTP returns and
// recorded in the audit_log row.
type IdempotencyReplayedSetter interface {
	SetIdempotencyReplayed(bool)
}

// Middleware returns a chi-compatible middleware that records per-request
// audit rows. Must be applied AFTER auth.Middleware so AuthContext is in
// ctx. The middleware:
//   - captures the request body up to 128 KB (for data_class=normal only)
//   - wraps the response writer to capture status + body up to 128 KB
//   - on response close, builds an Event and calls writer.Enqueue
//
// SSE responses: capture goes through the AuditInterceptor that plugs into
// proxy.NewChatProxy via the formal ProxyResponseInterceptor contract
// (Codex review [HIGH/MEDIUM] 02-05 decoupling). The audit middleware
// reads StatusCode from the writer wrapper post-ServeHTTP but does NOT
// duplicate body capture for SSE — that lives in TeeBody. For non-SSE,
// the writer wrapper captures the body up to 128 KB directly.
func Middleware(writer *Writer, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			ac, _ := auth.FromContext(r.Context())
			// Parse request_id (Plan 02-01 emits UUIDv7 string in ctx).
			rid, _ := uuid.Parse(httpx.RequestIDFrom(r.Context()))

			isNormal := ac.DataClass == auth.DataClassNormal

			// Capture request body (for POST JSON routes; audio multipart
			// path skips capture — raw audio is PII per D-B6, we only
			// keep form metadata which we read from FormValue post-proxy).
			var reqBody []byte
			if isNormal && r.Body != nil && !isAudioRoute(r.URL.Path) {
				reqBody, _ = io.ReadAll(io.LimitReader(r.Body, contentCapBytes))
				r.Body = io.NopCloser(bytes.NewReader(reqBody))
			}

			// Wrap writer to capture status + response body.
			aw := &auditResponseWriter{
				ResponseWriter: w,
				captureBody:    isNormal,
				capLeft:        contentCapBytes,
				buf:            &bytes.Buffer{},
				status:         200,
			}

			next.ServeHTTP(aw, r)

			// Build Event from the captured state. Upstream defaults to
			// the route-derived value (llm/embed/stt); handlers may
			// override via audit.WithUpstreamOverride (e.g. dispatcher's
			// sensitive-block path → "blocked_sensitive" per D-B3).
			upstream := upstreamForRoute(r.URL.Path)
			if override := auditctx.UpstreamOverrideFrom(r.Context()); override != "" {
				upstream = override
			}
			event := Event{
				TS:         start,
				RequestID:  rid,
				TenantID:   parseUUIDorZero(ac.TenantID),
				APIKeyID:   parseUUIDorZero(ac.APIKeyID),
				DataClass:  fallbackDataClass(ac.DataClass),
				Route:      routeTemplate(r.URL.Path),
				Method:     r.Method,
				Upstream:   upstream,
				StatusCode: aw.status,
				LatencyMs:  time.Since(start).Milliseconds(),
				Stream:     aw.sawSSE,
				Truncated:  aw.truncated,
			}
			if isNormal {
				event.Prompt = reqBody
				event.Response = append([]byte(nil), aw.buf.Bytes()...)
				if len(event.Response) == 0 {
					event.Response = nil
				}
				if len(event.Prompt) == 0 {
					event.Prompt = nil
				}
			}
			// Whisper metadata: if route is audio, attempt to extract.
			if isAudioRoute(r.URL.Path) {
				populateAudioMetadata(&event, r, aw)
			}
			// IdempotencyReplayed flag set by Plan 02-06 via the exported
			// IdempotencyReplayedSetter interface on auditResponseWriter.
			// This avoids ctx.WithValue() mutation, which does NOT propagate
			// back to the outer middleware's captured r reference.
			event.IdempotencyReplayed = aw.idempotencyReplayed

			writer.Enqueue(event)
		})
	}
}

// --- Helpers ---

func fallbackDataClass(d auth.DataClass) string {
	if d == "" {
		return string(auth.DataClassNormal)
	}
	return string(d)
}

func parseUUIDorZero(s string) uuid.UUID {
	if s == "" {
		return uuid.Nil
	}
	u, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil
	}
	return u
}

func routeTemplate(path string) string {
	switch {
	case strings.HasPrefix(path, "/v1/chat/completions"):
		return "/v1/chat/completions"
	case strings.HasPrefix(path, "/v1/embeddings"):
		return "/v1/embeddings"
	case strings.HasPrefix(path, "/v1/audio/transcriptions"):
		return "/v1/audio/transcriptions"
	case strings.HasPrefix(path, "/v1/health/upstreams"):
		return "/v1/health/upstreams"
	default:
		return path
	}
}

func upstreamForRoute(path string) string {
	switch {
	case strings.HasPrefix(path, "/v1/chat"):
		return "llm"
	case strings.HasPrefix(path, "/v1/embeddings"):
		return "embed"
	case strings.HasPrefix(path, "/v1/audio"):
		return "stt"
	default:
		return ""
	}
}

// UpstreamBlockedSensitive is written to audit_log.upstream when a
// data_class=sensitive request is blocked from external fallback per
// CONTEXT.md D-B3. Reserved value distinct from the route-derived
// upstream defaults (llm/embed/stt) so dashboards can isolate sensitive-
// blocked events without a join. Consistent with Phase 2 D-B2 — no
// audit_log_content row is written for sensitive (no content ever
// persists for sensitive tenants).
//
// Handlers (e.g. the Phase 3 dispatcher's sensitive-block path) stamp
// this value via auditctx.WithUpstreamOverride; the middleware reads
// it back during Event construction.
const UpstreamBlockedSensitive = "blocked_sensitive"

func isAudioRoute(path string) bool {
	return strings.HasPrefix(path, "/v1/audio/transcriptions")
}

// populateAudioMetadata extracts Whisper metadata from the multipart form
// and the upstream JSON response. Uses structured json.Decoder (Codex
// review [HIGH] 02-05 — replaces fragile fmt.Sscan-on-prefix parsing).
// Partial bodies (truncated at 128 KB cap) may not decode cleanly; in
// that case we accept the loss.
func populateAudioMetadata(e *Event, r *http.Request, aw *auditResponseWriter) {
	if err := r.ParseMultipartForm(32 << 20); err == nil && r.MultipartForm != nil {
		for name, files := range r.MultipartForm.File {
			if name == "file" && len(files) > 0 {
				e.AudioFilename = files[0].Filename
				e.AudioMime = files[0].Header.Get("Content-Type")
				e.AudioSizeBytes = files[0].Size
				break
			}
		}
		if lang := r.FormValue("language"); lang != "" {
			e.AudioLanguage = lang
		}
	}
	// audio_duration_s parsed from response using structured JSON decoding.
	// Speaches returns `{"text":"...", "duration": <seconds>, ...}` in the
	// OpenAI-compat verbose_json schema.
	if aw.buf.Len() > 0 {
		type whisperPartial struct {
			Duration *float64 `json:"duration"`
			Language *string  `json:"language"`
		}
		var wp whisperPartial
		if err := json.Unmarshal(aw.buf.Bytes(), &wp); err == nil {
			if wp.Duration != nil {
				e.AudioDurationS = *wp.Duration
			}
			if wp.Language != nil && e.AudioLanguage == "" {
				e.AudioLanguage = *wp.Language
			}
		}
		// Truncated bodies fail json.Unmarshal; that's acceptable — we keep
		// metadata partially populated rather than guess with substring scans.
	}
}

// --- auditResponseWriter ---

type auditResponseWriter struct {
	http.ResponseWriter
	status              int
	captureBody         bool
	capLeft             int
	buf                 *bytes.Buffer
	truncated           bool
	sawSSE              bool
	idempotencyReplayed bool // set by Plan 02-06 via IdempotencyReplayedSetter
	wroteHeader         bool
}

// SetIdempotencyReplayed implements IdempotencyReplayedSetter. Called by
// idempotency.Middleware on the replay path BEFORE returning, so
// audit.Middleware reads the flag after next.ServeHTTP returns.
func (a *auditResponseWriter) SetIdempotencyReplayed(flag bool) {
	a.idempotencyReplayed = flag
}

func (a *auditResponseWriter) WriteHeader(code int) {
	if a.wroteHeader {
		return
	}
	a.wroteHeader = true
	a.status = code
	if ct := a.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/event-stream") {
		a.sawSSE = true
	}
	a.ResponseWriter.WriteHeader(code)
}

func (a *auditResponseWriter) Write(b []byte) (int, error) {
	if !a.wroteHeader {
		// Go's default ResponseWriter emits 200 on first Write; mirror that
		// into our capture so Stream detection works even when the handler
		// doesn't explicitly call WriteHeader.
		if ct := a.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/event-stream") {
			a.sawSSE = true
		}
		a.wroteHeader = true
	}
	if a.captureBody && a.capLeft > 0 {
		take := len(b)
		if take > a.capLeft {
			take = a.capLeft
			a.truncated = true
		}
		a.buf.Write(b[:take])
		a.capLeft -= take
	} else if a.captureBody && len(b) > 0 {
		a.truncated = true
	}
	return a.ResponseWriter.Write(b)
}

func (a *auditResponseWriter) Flush() {
	if f, ok := a.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
