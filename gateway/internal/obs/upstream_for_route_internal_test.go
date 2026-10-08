package obs

import "testing"

// quick-261007-t9f: kept in sync with audit.upstreamForRoute — TTS gets "tts".
func TestUpstreamForRouteTTS(t *testing.T) {
	cases := map[string]string{
		"/v1/audio/transcriptions": "stt",
		"/v1/audio/speech":         "tts",
		"/v1/other":                "unknown",
	}
	for path, want := range cases {
		if got := upstreamForRoute(path); got != want {
			t.Errorf("upstreamForRoute(%q) = %q, want %q", path, got, want)
		}
	}
}
