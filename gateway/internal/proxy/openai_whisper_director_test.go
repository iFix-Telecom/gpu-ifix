package proxy

import (
	"bytes"
	"net/http"
	"net/url"
	"testing"
)

// TestOpenAIWhisperDirector_MultipartUntouched verifies the body is
// byte-identical after the Director runs — the multipart boundary MUST
// survive (re-encoding the multipart would require generating a new
// boundary, which we deliberately avoid per Phase 2 audio.go policy).
func TestOpenAIWhisperDirector_MultipartUntouched(t *testing.T) {
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	director := BuildOpenAIWhisperDirector(upstream, "sk-openai-test")

	// Synthetic multipart body — what matters is byte-equality post-Director.
	multipartBody := []byte("--boundary123\r\n" +
		"Content-Disposition: form-data; name=\"file\"; filename=\"a.wav\"\r\n" +
		"Content-Type: audio/wav\r\n\r\n" +
		"\x52\x49\x46\x46FAKE_AUDIO_BYTES" +
		"\r\n--boundary123\r\n" +
		"Content-Disposition: form-data; name=\"model\"\r\n\r\n" +
		"whisper-1\r\n--boundary123--\r\n")

	_, out := applyDirector(t, director, http.MethodPost, "/v1/audio/transcriptions",
		"multipart/form-data; boundary=boundary123", multipartBody, nil, nil)
	if !bytes.Equal(out, multipartBody) {
		t.Errorf("multipart body mutated by Director\n got: %q\nwant: %q", string(out), string(multipartBody))
	}
}

// TestOpenAIWhisperDirector_InjectsAuthBearer confirms only headers
// change — Authorization gets set, multipart Content-Type with boundary
// is preserved.
func TestOpenAIWhisperDirector_InjectsAuthBearer(t *testing.T) {
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	director := BuildOpenAIWhisperDirector(upstream, "sk-openai-abc")

	body := []byte("multipart-stub")
	req, _ := applyDirector(t, director, http.MethodPost, "/v1/audio/transcriptions",
		"multipart/form-data; boundary=xyz", body, nil, nil)
	if got := req.Header.Get("Authorization"); got != "Bearer sk-openai-abc" {
		t.Errorf("Authorization = %q, want Bearer sk-openai-abc", got)
	}
	if got := req.Header.Get("Content-Type"); got != "multipart/form-data; boundary=xyz" {
		t.Errorf("Content-Type changed: %q (boundary lost would break multipart parser)", got)
	}
}
