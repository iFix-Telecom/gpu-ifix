package proxy

// Phase 11.2 Plan 06 — gemini_stt_director (D-B4) unit tests.
//
// Pattern: build a minimal ReverseProxy with the gemini director + fake
// upstream httptest.Server, POST an OpenAI-shaped multipart audio body,
// capture the rewritten request at the upstream and the modified response
// at the client. Assertions pin the header swap (Pitfall 3), multipart
// byte fidelity, env-override model resolution, flatten of the Gemini
// envelope, and error envelope translation.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)

// geminiTestFixture wraps a fake Gemini upstream + a ReverseProxy mounted
// with the director-under-test. The fake upstream captures the last
// request shape and replies with a configurable response body/status.
type geminiTestFixture struct {
	upstream     *httptest.Server
	proxy        *httputil.ReverseProxy
	mu           sync.Mutex
	capturedReq  *http.Request
	capturedBody []byte
	respStatus   int
	respBody     []byte
}

func newGeminiFixture(t *testing.T, apiKey string, resolverMap map[string]string) *geminiTestFixture {
	t.Helper()
	f := &geminiTestFixture{
		respStatus: http.StatusOK,
	}
	// Default response — single candidate with `text: "ok"`.
	f.respBody = []byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`)

	f.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		f.mu.Lock()
		// Clone the request shape we care about (headers + URL + method).
		f.capturedReq = r.Clone(r.Context())
		f.capturedBody = append([]byte(nil), body...)
		status := f.respStatus
		respBody := f.respBody
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(respBody)
	}))

	upURL, err := url.Parse(f.upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}

	// Wire a resolver from the in-memory map. We use a manual
	// model.Resolver pre-populated via the same helper the resolver tests
	// use — but it's package-private. Instead we use NewResolver with no
	// pool and inject aliases via the exported test seam: there isn't
	// one, so we build aliases by triggering Refresh? Simpler: rely on
	// env-override to drive the model and skip the schema layer.
	r := newDirectorTestResolver(resolverMap)
	director, modifyResponse := BuildGeminiSTTDirector(upURL, apiKey, r, "gemini-stt",
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	f.proxy = &httputil.ReverseProxy{
		Director:       director,
		ModifyResponse: modifyResponse,
	}
	t.Cleanup(func() { f.upstream.Close() })
	return f
}

// newDirectorTestResolver builds an empty Resolver. Tests drive lookups
// via env-override (UPSTREAM_STT_FALLBACK_1_MODEL) rather than priming
// the schema cache, because aliasKey is package-private to models. The
// passthrough behavior (alias returned unchanged) is exercised in the
// "no env" path which the director maps to geminiDefaultModel.
func newDirectorTestResolver(_ map[string]string) *models.Resolver {
	// NewResolver with nil pool — Resolve() never touches the pool; only
	// Refresh does, which we never call.
	return models.NewResolver(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// buildMultipart constructs an OpenAI-style multipart body with a "model"
// field and a "file" part. Returns body bytes + Content-Type header value.
func buildMultipart(t *testing.T, model string, audio []byte, audioMIME string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("model", model); err != nil {
		t.Fatalf("WriteField model: %v", err)
	}
	hdr := make(map[string][]string)
	hdr["Content-Disposition"] = []string{`form-data; name="file"; filename="audio.wav"`}
	if audioMIME != "" {
		hdr["Content-Type"] = []string{audioMIME}
	}
	fw, err := w.CreatePart(hdr)
	if err != nil {
		t.Fatalf("CreatePart file: %v", err)
	}
	if _, err := fw.Write(audio); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return buf.Bytes(), w.FormDataContentType()
}

// doRequest serves a synthetic POST through the proxy to drive the
// director + ModifyResponse. Returns the ResponseRecorder for caller
// assertions on the *outbound* response (i.e. what the client would see).
func doRequest(t *testing.T, f *geminiTestFixture, body []byte, ct string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(body))
	r.Header.Set("Content-Type", ct)
	r.Header.Set("Authorization", "Bearer caller-supplied-secret")
	r.ContentLength = int64(len(body))
	for _, o := range opts {
		o(r)
	}
	rw := httptest.NewRecorder()
	f.proxy.ServeHTTP(rw, r)
	return rw
}

func TestBuildGeminiSTTDirector_SetsXGoogApiKeyHeader(t *testing.T) {
	f := newGeminiFixture(t, "test-api-key", nil)
	body, ct := buildMultipart(t, "whisper", []byte("FAKEAUDIOBYTES"), "audio/wav")
	_ = doRequest(t, f, body, ct)

	f.mu.Lock()
	got := f.capturedReq.Header.Get("x-goog-api-key")
	f.mu.Unlock()
	if got != "test-api-key" {
		t.Fatalf("x-goog-api-key=%q; want test-api-key", got)
	}
}

func TestBuildGeminiSTTDirector_StripsAuthorizationHeader(t *testing.T) {
	f := newGeminiFixture(t, "test-api-key", nil)
	body, ct := buildMultipart(t, "whisper", []byte("FAKEAUDIOBYTES"), "audio/wav")
	_ = doRequest(t, f, body, ct)

	f.mu.Lock()
	got := f.capturedReq.Header.Get("Authorization")
	f.mu.Unlock()
	if got != "" {
		t.Fatalf("Authorization header should be empty after director; got %q", got)
	}
}

func TestBuildGeminiSTTDirector_MultipartToJSON_AudioBytesPreserved(t *testing.T) {
	original := []byte("RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00@\x1f\x00\x00\x80>\x00\x00\x02\x00\x10\x00")
	f := newGeminiFixture(t, "k", nil)
	body, ct := buildMultipart(t, "whisper", original, "audio/wav")
	_ = doRequest(t, f, body, ct)

	f.mu.Lock()
	captured := append([]byte(nil), f.capturedBody...)
	contentType := f.capturedReq.Header.Get("Content-Type")
	f.mu.Unlock()

	if contentType != "application/json" {
		t.Fatalf("forwarded Content-Type=%q; want application/json", contentType)
	}
	var payload geminiRequest
	if err := json.Unmarshal(captured, &payload); err != nil {
		t.Fatalf("unmarshal forwarded body: %v; raw=%s", err, string(captured))
	}
	if len(payload.Contents) != 1 || len(payload.Contents[0].Parts) < 2 {
		t.Fatalf("contents shape wrong: %+v", payload)
	}
	inline := payload.Contents[0].Parts[1].InlineData
	if inline == nil {
		t.Fatalf("inline_data missing in part[1]")
	}
	if inline.MimeType != "audio/wav" {
		t.Errorf("mime_type=%q; want audio/wav", inline.MimeType)
	}
	decoded, derr := base64.StdEncoding.DecodeString(inline.Data)
	if derr != nil {
		t.Fatalf("base64 decode: %v", derr)
	}
	if !bytes.Equal(decoded, original) {
		t.Fatalf("decoded audio bytes mismatch:\ngot=%x\nwant=%x", decoded, original)
	}
}

func TestBuildGeminiSTTDirector_ResolvesModelViaEnvOverride(t *testing.T) {
	t.Setenv("UPSTREAM_STT_FALLBACK_1_MODEL", "gemini-2.5-flash")
	f := newGeminiFixture(t, "k", nil)
	body, ct := buildMultipart(t, "whisper", []byte("AUDIO"), "audio/wav")
	_ = doRequest(t, f, body, ct)

	f.mu.Lock()
	path := f.capturedReq.URL.Path
	f.mu.Unlock()
	if !strings.Contains(path, "gemini-2.5-flash") {
		t.Fatalf("URL path=%q; want to contain env-resolved model slug 'gemini-2.5-flash'", path)
	}
	if !strings.Contains(path, ":generateContent") {
		t.Fatalf("URL path=%q; want to contain ':generateContent' suffix", path)
	}
}

func TestBuildGeminiSTTDirector_FlattenResponse(t *testing.T) {
	f := newGeminiFixture(t, "k", nil)
	f.mu.Lock()
	f.respBody = []byte(`{"candidates":[{"content":{"parts":[{"text":"transcribed words"}]}}]}`)
	f.mu.Unlock()

	body, ct := buildMultipart(t, "whisper", []byte("AUDIO"), "audio/wav")
	rw := doRequest(t, f, body, ct)

	if rw.Code != http.StatusOK {
		t.Fatalf("response status=%d; want 200", rw.Code)
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal client response: %v; raw=%s", err, rw.Body.String())
	}
	if out.Text != "transcribed words" {
		t.Fatalf("text=%q; want 'transcribed words'", out.Text)
	}
}

func TestBuildGeminiSTTDirector_TranslatesGeminiErrorEnvelope(t *testing.T) {
	f := newGeminiFixture(t, "k", nil)
	f.mu.Lock()
	// Upstream returns 200 but with an error envelope (Gemini sometimes does
	// this); the director MUST translate to 502 + OpenAI envelope.
	f.respStatus = http.StatusOK
	f.respBody = []byte(`{"error":{"code":429,"message":"quota exceeded","status":"RESOURCE_EXHAUSTED"}}`)
	f.mu.Unlock()

	body, ct := buildMultipart(t, "whisper", []byte("AUDIO"), "audio/wav")
	rw := doRequest(t, f, body, ct)

	if rw.Code != http.StatusBadGateway {
		t.Fatalf("response status=%d; want 502", rw.Code)
	}
	bodyStr := rw.Body.String()
	if !strings.Contains(bodyStr, `"type":"upstream_error"`) {
		t.Fatalf("response body missing OpenAI envelope type=upstream_error; got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "quota exceeded") {
		t.Fatalf("response body missing translated message; got: %s", bodyStr)
	}
}
