// Package proxy (openai_whisper_director.go): Director extension for the
// openai-whisper fallback upstream. Phase 06.9 — Whisper uses multipart/form-data;
// the director rewrites the "model" form-field value via the resolver
// (per-upstream-name lookup, Phase 06.9 Plan 02 semantics) while preserving
// the file part's audio bytes BYTE-IDENTICAL through io.Copy (never decoding
// binary as text — Pitfall #6).
//
// Edge-case behaviors (R6, WARNING-3 — duplicate-model abort wired in this phase):
//
//   - Missing "model" form field: helper writes the resolved target for the
//     canonical alias of this upstream as a fresh "model" part. The audio
//     file part is preserved byte-identical.
//
//   - Duplicate "model" form field: helper returns statusCode=400 + nil body.
//     The WhisperAbortGuard wrapper (registered at the handler chain in
//     main.go) writes HTTP 400 + a JSON error envelope and returns BEFORE
//     invoking the proxy. The request never reaches upstream.
//
//   - Resolver miss: helper proceeds with resolver.Resolve which returns the
//     alias unchanged. The multipart "model" part keeps the alias string.
//     OpenAI may 400 the request; the breaker's IsSuccessful filter (D-A4)
//     classifies 4xx as non-failure so the breaker stays CLOSED.
//
//   - Parse error (malformed multipart): helper returns statusCode=0 + err.
//     The director caller falls through to forward the ORIGINAL body
//     unchanged. The downstream parser will reject; the gateway never 500s.
//
//   - Non-multipart Content-Type: director skips the rewrite entirely and
//     forwards the body untouched (defensive — shouldn't happen on the
//     /v1/audio/transcriptions route but if it does, don't corrupt).
//
// The env-override-wins precedence per D-06 lives inside Resolver.Resolve
// (Plan 02). When UPSTREAM_STT_OPENAI_MODEL is set, the resolver returns
// the env-overridden target transparently; this director contains no
// separate env-read logic.
package proxy

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)

// whisperDuplicateModelMessage is the JSON error body returned by the
// WhisperAbortGuard when a multipart request contains two or more "model"
// form fields. The message intentionally NEVER includes the offending
// values (sensitive data hygiene per R12).
const whisperDuplicateModelMessage = `{"error":{"message":"duplicate 'model' field in multipart request","type":"invalid_request_error"}}`

// canonicalAliasForUpstream maps a tier-1 upstream name to the canonical
// alias clients are expected to send. Used by rewriteMultipartModelViaResolver
// when injecting a "model" form field into a request that did not include
// one. Mirrors the (alias, upstream_name) seed rows from migration 0026.
//
// New tier-1 upstreams MUST add an entry here alongside their schema row
// AND their entry in models.upstreamEnvVarMap.
var canonicalAliasForUpstream = map[string]string{
	"openai-whisper": "whisper",
	// Phase 11.2 D-B8 — Groq STT endpoint is OpenAI-compatible
	// (`https://api.groq.com/openai/v1/audio/transcriptions`). It REUSES
	// BuildOpenAIWhisperDirector verbatim — only URL + bearer + model
	// differ. The canonical alias mapping is the gate that lets the
	// shared director resolve groq-whisper's target via the resolver.
	"groq-whisper": "whisper",
}

// BuildOpenAIWhisperDirector returns a Director for the openai-whisper
// fallback upstream. Wraps BuildDirector by:
//
//   - injecting `Authorization: Bearer <authBearer>` (from
//     upstreams.UpstreamConfig.AuthBearer), and
//   - rewriting the multipart "model" form-field value via the resolver
//     while preserving the file part's audio bytes byte-identical.
//
// Non-multipart Content-Type: body passes through untouched (defensive).
//
// Duplicate-model abort: the WhisperAbortGuard wrapper (registered in
// main.go around the proxy handler) catches the duplicate-model case
// BEFORE the proxy runs and returns HTTP 400 to the client. The director
// would also detect duplicates if reached (statusCode=400 short-circuit
// to passthrough), but the guard is the authoritative abort path
// (WARNING-3 wired in Phase 06.9 Plan 03).
func BuildOpenAIWhisperDirector(
	upstream *url.URL,
	authBearer string,
	resolver *models.Resolver,
	upstreamName string,
	log *slog.Logger,
) func(*http.Request) {
	base := BuildDirector(upstream)
	return func(r *http.Request) {
		base(r)
		if authBearer != "" {
			r.Header.Set("Authorization", "Bearer "+authBearer)
		}
		// Defensive skip: only attempt rewrite when the request is multipart.
		ct := r.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "multipart/form-data") {
			return
		}
		if r.Body == nil {
			return
		}
		body, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil || len(body) == 0 {
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			return
		}
		newBody, newCT, statusCode, rerr := rewriteMultipartModelViaResolver(body, ct, resolver, upstreamName)
		switch {
		case statusCode == http.StatusBadRequest:
			// Duplicate-model branch — the WhisperAbortGuard wrapper has
			// already returned 400 to the client BEFORE the proxy was
			// invoked. If the director still gets here (e.g. wrapper not
			// wired in some scaffold test), defensively forward the
			// ORIGINAL body unchanged — upstream will reject, gateway
			// will not 500. This avoids a paradoxical "we 400'd but also
			// forwarded a tampered body" state.
			if log != nil {
				log.Warn("whisper_director duplicate model field detected (guard should have aborted)",
					"upstream", upstreamName,
				)
			}
			rewriteRequestBody(r, body)
		case rerr != nil:
			// Parse error — pass through original body unchanged.
			if log != nil {
				log.Debug("whisper_director multipart parse failed",
					"upstream", upstreamName,
					"error_class", fmt.Sprintf("%T", rerr),
				)
			}
			rewriteRequestBody(r, body)
		default:
			// Success — forward the rewritten body with the new boundary.
			r.Body = io.NopCloser(bytes.NewReader(newBody))
			r.ContentLength = int64(len(newBody))
			r.Header.Set("Content-Type", newCT)
			r.Header.Set("Content-Length", strconv.Itoa(len(newBody)))
		}
	}
}

// rewriteMultipartModelViaResolver parses a multipart/form-data body,
// rewrites the "model" form-field value via the resolver, and returns the
// new body bytes + the new Content-Type header (with a fresh boundary).
//
// Return semantics:
//   - (newBody, newCT, 0, nil)     — success.
//   - (nil,     "",    400, nil)   — duplicate "model" field detected; caller
//     SHOULD abort the request with HTTP 400
//     (WhisperAbortGuard does this). The
//     director treats 400 as a defensive no-op.
//   - (nil,     "",    0, err)     — parse error; caller falls back to forwarding
//     the original body.
//
// Audio file bytes are streamed via io.Copy through the multipart.Writer's
// CreatePart — never decoded as text, never re-encoded (Pitfall #6).
//
// Behaviors:
//   - Missing "model" part: a fresh "model" part is appended with the resolved
//     target for the canonical alias of this upstream (per canonicalAliasForUpstream).
//   - Resolver miss: the resolved value equals the alias; the helper writes
//     the alias back unchanged.
//   - Empty alias from canonicalAliasForUpstream (new tier-1 upstream not yet
//     registered here): the helper writes an empty "model" part. OpenAI will
//     400; breaker classifies 4xx as non-failure. New tier-1 onboarding MUST
//     update canonicalAliasForUpstream.
func rewriteMultipartModelViaResolver(
	body []byte,
	contentType string,
	resolver *models.Resolver,
	upstreamName string,
) ([]byte, string, int, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, "", 0, err
	}
	if !strings.HasPrefix(mediaType, "multipart/") {
		return nil, "", 0, fmt.Errorf("content-type is not multipart: %s", mediaType)
	}
	boundary, ok := params["boundary"]
	if !ok || boundary == "" {
		return nil, "", 0, fmt.Errorf("missing boundary in multipart content-type")
	}

	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	var out bytes.Buffer
	w := multipart.NewWriter(&out)

	modelSeen := false
	for {
		part, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			return nil, "", 0, perr
		}
		// Identify the "model" field by form name.
		if part.FormName() == "model" {
			if modelSeen {
				// R6 / WARNING-3 — duplicate "model" form field. Abort with
				// statusCode=400. Drain the duplicate part to keep the
				// reader healthy (idiomatic) even though we won't write it.
				_, _ = io.Copy(io.Discard, part)
				_ = part.Close()
				return nil, "", http.StatusBadRequest, nil
			}
			modelSeen = true

			// Drain the original alias value (we don't need it — resolver is
			// looked up against the body's existing alias text).
			aliasBytes, rerr := io.ReadAll(part)
			_ = part.Close()
			if rerr != nil {
				return nil, "", 0, rerr
			}
			alias := string(aliasBytes)
			target := resolver.Resolve(alias, upstreamName)

			// Write a fresh "model" part with the same headers but the new value.
			fw, werr := w.CreatePart(part.Header)
			if werr != nil {
				return nil, "", 0, werr
			}
			if _, werr := fw.Write([]byte(target)); werr != nil {
				return nil, "", 0, werr
			}
			continue
		}

		// Non-model part: stream through via io.Copy (binary-safe; preserves
		// audio file bytes byte-identical).
		fw, werr := w.CreatePart(part.Header)
		if werr != nil {
			return nil, "", 0, werr
		}
		if _, werr := io.Copy(fw, part); werr != nil {
			return nil, "", 0, werr
		}
		_ = part.Close()
	}

	// R6 — missing model field: inject a synthetic "model" part with the
	// resolved target for the canonical alias of this upstream.
	if !modelSeen {
		canonAlias := canonicalAliasForUpstream[upstreamName]
		target := resolver.Resolve(canonAlias, upstreamName)
		fw, werr := w.CreateFormField("model")
		if werr != nil {
			return nil, "", 0, werr
		}
		if _, werr := fw.Write([]byte(target)); werr != nil {
			return nil, "", 0, werr
		}
	}

	if cerr := w.Close(); cerr != nil {
		return nil, "", 0, cerr
	}
	return out.Bytes(), w.FormDataContentType(), 0, nil
}

// WhisperAbortGuard wraps the Whisper proxy handler with a pre-flight check
// that catches duplicate-"model" multipart requests and returns HTTP 400 to
// the client BEFORE the proxy runs. WARNING-3 (Phase 06.9 Plan 03): this is
// the wired abort path; there is no escape hatch / no degraded fallback.
//
// Behavior:
//
//   - Reads the request body into memory once (request bodies are capped at
//     server level — 25 MiB — so this is bounded).
//   - Restores r.Body to a fresh reader so the inner proxy can re-read.
//   - Pre-flight runs rewriteMultipartModelViaResolver. On statusCode=400,
//     writes HTTP 400 + JSON envelope + emits log.Warn(presence-only) and
//     RETURNS without invoking the inner handler.
//   - On any other case, invokes the inner handler. The director then re-runs
//     the parse (idempotent — same helper, same body). This duplicates the
//     parse work; future optimization may cache the result on the request
//     context.
//
// log is required for the WARN emission; passing nil is permitted but the
// rejection path will be silent in that case.
func WhisperAbortGuard(inner http.Handler, resolver *models.Resolver, upstreamName string, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "multipart/form-data") || r.Body == nil {
			inner.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			// Body unreadable — let the inner proxy emit the standard error
			// envelope; we have no reliable way to inspect for duplicate-model.
			r.Body = io.NopCloser(bytes.NewReader(nil))
			inner.ServeHTTP(w, r)
			return
		}
		_, _, statusCode, _ := rewriteMultipartModelViaResolver(body, ct, resolver, upstreamName)
		// Restore body so the inner proxy (director) can re-read.
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))

		if statusCode == http.StatusBadRequest {
			if log != nil {
				log.Warn("whisper_abort_guard rejected duplicate model field",
					"upstream", upstreamName,
				)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(whisperDuplicateModelMessage))
			return
		}
		inner.ServeHTTP(w, r)
	})
}
