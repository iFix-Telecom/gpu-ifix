package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
)

// runTTSChars passes body through the middleware and returns the stamped
// char count plus the exact bytes the next handler read.
func runTTSChars(t *testing.T, body []byte) (int64, []byte) {
	t.Helper()
	var gotChars int64
	var gotBody []byte
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotChars = auditctx.RequestTTSCharsFrom(r.Context())
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("next read: %v", err)
		}
		gotBody = b
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	TTSRequestCharsMiddleware(discardLog())(next).ServeHTTP(httptest.NewRecorder(), req)
	return gotChars, gotBody
}

func TestTTSRequestCharsCountsRunesNotBytes(t *testing.T) {
	body := []byte(`{"model":"tts-1","input":"Olá mundo","voice":"pm_alex"}`)
	chars, got := runTTSChars(t, body)
	if chars != 9 {
		t.Errorf("chars: want 9 runes (\"Olá mundo\"), got %d", chars)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("body not restored byte-identical:\n got %q\nwant %q", got, body)
	}
}

func TestTTSRequestCharsLargeBodyForwardedWhole(t *testing.T) {
	big := strings.Repeat("a", 70*1024)
	body := []byte(`{"input":"` + big + `"}`)
	chars, got := runTTSChars(t, body)
	if chars != 0 {
		t.Errorf("over-cap body must not be stamped, got %d", chars)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("over-cap body truncated: got %d bytes, want %d", len(got), len(body))
	}
}

func TestTTSRequestCharsInvalidJSONPassesThrough(t *testing.T) {
	for _, body := range [][]byte{[]byte(`{not json`), []byte(`{"voice":"x"}`), []byte(``)} {
		chars, got := runTTSChars(t, body)
		if chars != 0 {
			t.Errorf("body %q: want 0 chars, got %d", body, chars)
		}
		if !bytes.Equal(got, body) {
			t.Errorf("body %q not restored: got %q", body, got)
		}
	}
}

// GetBody must replay the same bytes so the dispatcher's tier fallback
// (prepareReplayBody) forwards an intact body on the next candidate.
func TestTTSRequestCharsRestoresGetBody(t *testing.T) {
	body := []byte(`{"input":"oi"}`)
	var replay []byte
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.GetBody == nil {
			t.Fatal("GetBody not set")
		}
		rc, _ := r.GetBody()
		replay, _ = io.ReadAll(rc)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", bytes.NewReader(body))
	TTSRequestCharsMiddleware(discardLog())(next).ServeHTTP(httptest.NewRecorder(), req)
	if !bytes.Equal(replay, body) {
		t.Errorf("GetBody replay: got %q want %q", replay, body)
	}
}
