package proxy_test

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy"
)

// buildMultipartAudio builds a multipart/form-data body with a single "file"
// part carrying the given audio bytes + Content-Type. Returns the body bytes
// and the full Content-Type header value (with boundary).
func buildMultipartAudio(t *testing.T, fileMime string, audio []byte) (body []byte, contentType string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	hdr := make(map[string][]string)
	hdr["Content-Disposition"] = []string{`form-data; name="file"; filename="a.wav"`}
	hdr["Content-Type"] = []string{fileMime}
	part, err := mw.CreatePart(hdr)
	if err != nil {
		t.Fatalf("CreatePart: %v", err)
	}
	if _, err := part.Write(audio); err != nil {
		t.Fatalf("part write: %v", err)
	}
	_ = mw.WriteField("model", "whisper-1")
	if err := mw.Close(); err != nil {
		t.Fatalf("mw close: %v", err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

// TestRequestAudioSecondsMiddlewareStampsAndReplays: a multipart POST to
// /v1/audio/transcriptions has its derived request-audio seconds stamped on
// the ctx (non-zero) AND the body replays byte-identical via BOTH r.Body and
// r.GetBody — proving the dispatcher replay contract is intact.
func TestRequestAudioSecondsMiddlewareStampsAndReplays(t *testing.T) {
	wav := buildWAV(16000, 1, 16, 32000) // exactly 2.0s
	wantSeconds := proxy.DeriveAudioSeconds(wav, "audio/wav")
	if wantSeconds <= 0 {
		t.Fatalf("precondition: DeriveAudioSeconds returned %.4f", wantSeconds)
	}

	body, ct := buildMultipartAudio(t, "audio/wav", wav)

	var (
		gotSeconds   float64
		downstream   []byte
		getBodyBytes []byte
	)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSeconds = auditctx.RequestAudioSecondsFrom(r.Context())
		// Body must be readable byte-identical.
		downstream, _ = io.ReadAll(r.Body)
		// GetBody must be set and replay byte-identical (dispatcher contract).
		if r.GetBody != nil {
			rc, err := r.GetBody()
			if err == nil {
				getBodyBytes, _ = io.ReadAll(rc)
				_ = rc.Close()
			}
		}
	})

	h := proxy.RequestAudioSecondsMiddleware(nil, "")(next)
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(body))
	req.Header.Set("Content-Type", ct)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if gotSeconds <= 0 {
		t.Fatalf("ctx seconds: want >0 (stamped), got %.4f", gotSeconds)
	}
	if diff := gotSeconds - wantSeconds; diff > 0.001 || diff < -0.001 {
		t.Fatalf("ctx seconds: want %.4f, got %.4f", wantSeconds, gotSeconds)
	}
	if !bytes.Equal(downstream, body) {
		t.Fatalf("downstream body not byte-identical: want %d bytes, got %d", len(body), len(downstream))
	}
	if r := req; r.GetBody == nil {
		t.Fatal("r.GetBody must be set by the middleware (dispatcher replay contract)")
	}
	if !bytes.Equal(getBodyBytes, body) {
		t.Fatalf("r.GetBody() replay not byte-identical: want %d bytes, got %d", len(body), len(getBodyBytes))
	}
}

// TestRequestAudioSecondsMiddlewareNonAudioRoutePassthrough: a non-audio route
// is passed through with NO stamp and the body untouched.
func TestRequestAudioSecondsMiddlewareNonAudioRoutePassthrough(t *testing.T) {
	wav := buildWAV(16000, 1, 16, 32000)
	body, ct := buildMultipartAudio(t, "audio/wav", wav)

	var (
		gotSeconds float64
		downstream []byte
	)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSeconds = auditctx.RequestAudioSecondsFrom(r.Context())
		downstream, _ = io.ReadAll(r.Body)
	})

	h := proxy.RequestAudioSecondsMiddleware(nil, "")(next)
	// /v1/chat/completions is not a transcription route → no stamp.
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", ct)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if gotSeconds != 0 {
		t.Fatalf("non-audio route: want no stamp (0), got %.4f", gotSeconds)
	}
	if !bytes.Equal(downstream, body) {
		t.Fatalf("non-audio route body mutated: want %d bytes, got %d", len(body), len(downstream))
	}
}

// TestRequestAudioSecondsMiddlewareNonMultipartPassthrough: a transcription
// route that is NOT multipart (e.g. a JSON body) passes through unstamped and
// unread.
func TestRequestAudioSecondsMiddlewareNonMultipartPassthrough(t *testing.T) {
	jsonBody := []byte(`{"not":"multipart"}`)
	var (
		gotSeconds float64
		downstream []byte
	)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSeconds = auditctx.RequestAudioSecondsFrom(r.Context())
		downstream, _ = io.ReadAll(r.Body)
	})
	h := proxy.RequestAudioSecondsMiddleware(nil, "")(next)
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if gotSeconds != 0 {
		t.Fatalf("non-multipart: want no stamp (0), got %.4f", gotSeconds)
	}
	if !bytes.Equal(downstream, jsonBody) {
		t.Fatalf("non-multipart body mutated: %q", downstream)
	}
}

// parseMultipartField returns the value of a non-file form field from a
// multipart body, or "" if absent.
func parseMultipartField(t *testing.T, body []byte, contentType, name string) string {
	t.Helper()
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("ParseMediaType: %v", err)
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		p, err := mr.NextPart()
		if err != nil {
			return ""
		}
		if p.FormName() == name {
			v, _ := io.ReadAll(p)
			_ = p.Close()
			return string(v)
		}
		_ = p.Close()
	}
}

// TestRequestAudioSecondsMiddlewareForcesLanguage: regardless of what the client
// sends for `language` (nothing / wrong ISO / blank), the middleware rewrites it
// to the single default (pt) — the chatifix/Chatwoot mic sends language=en which
// mis-transcribes pt-BR as fluent English. The "file" audio + duration stamp stay
// intact, and exactly one `language` reaches downstream (fallbacks read the first).
func TestRequestAudioSecondsMiddlewareForcesLanguage(t *testing.T) {
	wav := buildWAV(16000, 1, 16, 32000) // 2.0s
	cases := []struct {
		name     string
		clientLg *string // nil = field omitted
	}{
		{"omitted", nil},
		{"wrong-en", ptr("en")},
		{"blank", ptr("")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			mw := multipart.NewWriter(&buf)
			hdr := map[string][]string{
				"Content-Disposition": {`form-data; name="file"; filename="a.wav"`},
				"Content-Type":        {"audio/wav"},
			}
			part, _ := mw.CreatePart(hdr)
			_, _ = part.Write(wav)
			_ = mw.WriteField("model", "whisper-1")
			if tc.clientLg != nil {
				_ = mw.WriteField("language", *tc.clientLg)
			}
			_ = mw.Close()
			body := buf.Bytes()

			var (
				gotSeconds float64
				downstream []byte
				downCT     string
			)
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotSeconds = auditctx.RequestAudioSecondsFrom(r.Context())
				downCT = r.Header.Get("Content-Type") // boundary may have changed
				downstream, _ = io.ReadAll(r.Body)
			})
			h := proxy.RequestAudioSecondsMiddleware(nil, "pt")(next)
			req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(body))
			req.Header.Set("Content-Type", mw.FormDataContentType())
			h.ServeHTTP(httptest.NewRecorder(), req)

			// Exactly one language field, value pt.
			if all := allMultipartFields(t, downstream, downCT, "language"); len(all) != 1 || all[0] != "pt" {
				t.Fatalf("forced language: want [pt], got %v", all)
			}
			// file part preserved byte-identical through the re-encode.
			if got := parseMultipartFileBytes(t, downstream, downCT); !bytes.Equal(got, wav) {
				t.Fatalf("file part mutated: want %d bytes, got %d", len(wav), len(got))
			}
			if gotSeconds <= 0 {
				t.Fatalf("ctx seconds: want >0, got %.4f", gotSeconds)
			}
		})
	}
}

func ptr(s string) *string { return &s }

// allMultipartFields returns every value of a repeated form field.
func allMultipartFields(t *testing.T, body []byte, contentType, name string) []string {
	t.Helper()
	_, params, _ := mime.ParseMediaType(contentType)
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	var vals []string
	for {
		p, err := mr.NextPart()
		if err != nil {
			return vals
		}
		if p.FormName() == name {
			v, _ := io.ReadAll(p)
			vals = append(vals, string(v))
		}
		_ = p.Close()
	}
}

// parseMultipartFileBytes returns the "file" part bytes from a multipart body.
func parseMultipartFileBytes(t *testing.T, body []byte, contentType string) []byte {
	t.Helper()
	_, params, _ := mime.ParseMediaType(contentType)
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		p, err := mr.NextPart()
		if err != nil {
			return nil
		}
		if p.FormName() == "file" {
			b, _ := io.ReadAll(p)
			_ = p.Close()
			return b
		}
		_ = p.Close()
	}
}
