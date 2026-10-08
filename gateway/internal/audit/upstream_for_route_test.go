package audit

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
)

// quick-261007-t9f: a successful /v1/audio/speech response is binary audio.
// Capturing it into audit_log_content.response (JSONB) failed the whole audit
// flush batch in prod ("invalid byte sequence for encoding UTF8: 0xff",
// SQLSTATE 22021) — every successful TTS audit row (and any row batched with
// it) was lost. Audio routes now keep the response only when it is valid JSON.
func TestMiddleware_AudioBinaryResponseNotCaptured(t *testing.T) {
	ac := auth.AuthContext{
		TenantID:  uuid.New().String(),
		APIKeyID:  uuid.New().String(),
		DataClass: auth.DataClassNormal,
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(200)
		_, _ = w.Write([]byte{0xff, 0xfb, 0x90, 0x00, 0x98})
	})
	cw, h := harness(t, ac, handler)
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v1/audio/speech", "application/json", strings.NewReader(`{"input":"oi"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !bytes.Equal(b, []byte{0xff, 0xfb, 0x90, 0x00, 0x98}) {
		t.Fatalf("client body altered: %v", b)
	}
	if len(cw.events) != 1 {
		t.Fatalf("expected 1 event; got %d", len(cw.events))
	}
	if ev := cw.events[0]; ev.Response != nil || ev.StatusCode != 200 || ev.Upstream != "tts" {
		t.Errorf("want nil response, 200, upstream tts; got resp=%v status=%d upstream=%q",
			ev.Response, ev.StatusCode, ev.Upstream)
	}
}

// JSON error bodies on audio routes are still captured.
func TestMiddleware_AudioJSONErrorStillCaptured(t *testing.T) {
	ac := auth.AuthContext{TenantID: uuid.New().String(), DataClass: auth.DataClassNormal}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"error":{"code":"upstream_unavailable"}}`))
	})
	cw, h := harness(t, ac, handler)
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v1/audio/speech", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if ev := cw.events[0]; !bytes.Contains(ev.Response, []byte("upstream_unavailable")) {
		t.Errorf("json error body not captured: %q", ev.Response)
	}
}

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
