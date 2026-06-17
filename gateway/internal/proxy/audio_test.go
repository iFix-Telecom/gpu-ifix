package proxy

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
	"github.com/ifixtelecom/gpu-ifix/pkg/openai"
)

func TestAudioProxy_MultipartPreservedAndAuthStripped(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("path got %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("Authorization leaked: %q", r.Header.Get("Authorization"))
		}
		ct := r.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "multipart/form-data") {
			t.Errorf("Content-Type got %q, expected multipart/form-data", ct)
		}
		if !strings.Contains(ct, "boundary=") {
			t.Errorf("no boundary in Content-Type: %q", ct)
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		if r.FormValue("model") == "" {
			t.Errorf("missing model field")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(openai.TranscriptionResponse{Text: "silence"})
	}))
	defer upstream.Close()

	// Empty resolver → passthrough: model=whisper forwarded unchanged, exercising
	// the same auth-strip + multipart-preservation contract this test asserts.
	rp, err := NewAudioProxy(upstream.URL, discardLogger(), models.NewResolverForTesting(nil))
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(wrapWithMiddleware(rp))
	defer gateway.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "silence.wav")
	_, _ = fw.Write(bytes.Repeat([]byte{0x00}, 1024))
	_ = mw.WriteField("model", "whisper")
	_ = mw.Close()

	req, _ := http.NewRequest("POST", gateway.URL+"/v1/audio/transcriptions", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer ifix_sk_secret")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("got %d want 200", resp.StatusCode)
	}
	var out openai.TranscriptionResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Text != "silence" {
		t.Errorf("text got %q", out.Text)
	}
}
