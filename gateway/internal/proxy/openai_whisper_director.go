// Package proxy (openai_whisper_director.go): Director extension for the
// openai-whisper fallback upstream. Whisper uses multipart/form-data;
// rewriting the body would require re-encoding with a new boundary, which
// we deliberately AVOID per Phase 2 audio.go policy. Only headers are
// modified: auth bearer added (here) + client auth stripped (in base
// BuildDirector).
//
// The "model" field selection (whisper-1) is the caller's responsibility:
// when routed to OpenAI Whisper the client MUST send model=whisper-1 in
// the multipart form. The Phase 2 model alias resolver (models.Resolver)
// works only on JSON bodies and would need a multipart-aware rewriter
// to swap the form field; that's deferred to a future phase. For now,
// the upstream returns 400 if the client sends an unsupported alias —
// the breaker IsSuccessful filter (D-A4) classifies 4xx as non-failure
// so this does not poison the breaker.
package proxy

import (
	"net/http"
	"net/url"
)

// BuildOpenAIWhisperDirector returns a Director for the openai-whisper
// fallback upstream. Body passes through untouched (multipart preserved);
// only the Authorization header is added.
func BuildOpenAIWhisperDirector(upstream *url.URL, authBearer string) func(*http.Request) {
	base := BuildDirector(upstream)
	return func(r *http.Request) {
		base(r)
		if authBearer != "" {
			r.Header.Set("Authorization", "Bearer "+authBearer)
		}
		// Body passes through untouched (multipart). Content-Type header
		// (with boundary) survives because BuildDirector does not touch it.
	}
}
