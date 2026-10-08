package audit

import "testing"

// quick-261007-t9f: /v1/audio/speech (TTS) gets its own route-derived default
// label "tts" so undispatched TTS failures are no longer mislabeled "stt".
func TestUpstreamForRoute(t *testing.T) {
	cases := map[string]string{
		"/v1/chat/completions":     "llm",
		"/v1/embeddings":           "embed",
		"/v1/audio/transcriptions": "stt",
		"/v1/audio/speech":         "tts",
		"/v1/rerank":               "",
	}
	for path, want := range cases {
		if got := upstreamForRoute(path); got != want {
			t.Errorf("upstreamForRoute(%q) = %q, want %q", path, got, want)
		}
	}
}
