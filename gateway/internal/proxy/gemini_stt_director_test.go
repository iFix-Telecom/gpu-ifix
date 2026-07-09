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
	"errors"
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

func TestSniffAudioMIME(t *testing.T) {
	cases := []struct {
		name  string
		input []byte
		want  string
	}{
		{"ogg", []byte("OggS\x00\x02\x00\x00"), "audio/ogg"},
		{"wav", []byte("RIFF\x24\x00\x00\x00WAVEfmt "), "audio/wav"},
		{"flac", []byte("fLaC\x00\x00\x00\x22"), "audio/flac"},
		{"webm", []byte{0x1A, 0x45, 0xDF, 0xA3, 0x00}, "audio/webm"},
		{"mp4_ftyp", []byte("\x00\x00\x00\x18ftypM4A "), "audio/mp4"},
		{"mp3_id3", []byte("ID3\x04\x00\x00\x00"), "audio/mpeg"},
		{"mp3_framesync", []byte{0xFF, 0xFB, 0x90, 0x00}, "audio/mpeg"},
		{"unknown", []byte("not-audio-bytes-here"), "audio/wav"},
		{"empty", []byte{}, "audio/wav"},
		{"short_1byte", []byte{0xFF}, "audio/wav"},
		{"short_riff_only", []byte("RIFF"), "audio/wav"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sniffAudioMIME(tc.input)
			if got != tc.want {
				t.Fatalf("sniffAudioMIME(%q)=%q; want %q", tc.input, got, tc.want)
			}
		})
	}
}

// forwardedInlineMime drives a request through the fixture and returns the
// inline_data.mime_type the director forwarded to the (fake) Gemini upstream.
func forwardedInlineMime(t *testing.T, f *geminiTestFixture, body []byte, ct string) string {
	t.Helper()
	_ = doRequest(t, f, body, ct)
	f.mu.Lock()
	captured := append([]byte(nil), f.capturedBody...)
	f.mu.Unlock()
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
	return inline.MimeType
}

func TestExtractAudioFromMultipart_MissingContentTypeSniffsOgg(t *testing.T) {
	f := newGeminiFixture(t, "k", nil)
	// Content-Type absent on the file part + Ogg magic bytes → sniff audio/ogg
	// (pre-fix this returned the hardcoded audio/wav fallback).
	body, ct := buildMultipart(t, "whisper", []byte("OggS\x00\x02\x00\x00opusdata"), "")
	if got := forwardedInlineMime(t, f, body, ct); got != "audio/ogg" {
		t.Fatalf("mime_type=%q; want audio/ogg (sniffed from OggS magic)", got)
	}
}

func TestExtractAudioFromMultipart_OctetStreamSniffsWav(t *testing.T) {
	f := newGeminiFixture(t, "k", nil)
	// Generic application/octet-stream + RIFF/WAVE magic → sniff audio/wav
	// (must NOT forward octet-stream, which Gemini 502s on).
	body, ct := buildMultipart(t, "whisper", []byte("RIFF\x24\x00\x00\x00WAVEfmt "), "application/octet-stream")
	if got := forwardedInlineMime(t, f, body, ct); got != "audio/wav" {
		t.Fatalf("mime_type=%q; want audio/wav (sniffed, not octet-stream)", got)
	}
}

func TestExtractAudioFromMultipart_ConcreteAudioMimePassthrough(t *testing.T) {
	f := newGeminiFixture(t, "k", nil)
	// A concrete audio/* declared MIME is trusted verbatim even if the bytes
	// don't self-describe (no sniff override).
	body, ct := buildMultipart(t, "whisper", []byte("opaque-audio-bytes"), "audio/mpeg")
	if got := forwardedInlineMime(t, f, body, ct); got != "audio/mpeg" {
		t.Fatalf("mime_type=%q; want audio/mpeg (declared passthrough)", got)
	}
}

func TestBuildGeminiSTTDirector_TranslatesGeminiErrorEnvelope(t *testing.T) {
	// New contract (RES-13 intra-tier failover): on a Gemini error envelope the
	// ModifyResponse (a) mutates resp to a 502 + OpenAI upstream_error envelope
	// (so a STANDALONE consumer still gets a meaningful body) AND (b) returns
	// errUpstreamRetryable so the dispatcher's ErrorHandler suppresses the write
	// and cascadeTier1 falls through to the next STT candidate. We drive
	// ModifyResponse directly (not through the ReverseProxy, whose default
	// ErrorHandler would discard the body on the returned error).
	gURL, _ := url.Parse("https://gemini.example/v1")
	_, modifyResponse := BuildGeminiSTTDirector(
		gURL, "k",
		newDirectorTestResolver(nil),
		"gemini-stt", slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	resp := &http.Response{
		StatusCode: http.StatusOK, // Gemini often 200s with an error envelope
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"error":{"code":429,"message":"quota exceeded","status":"RESOURCE_EXHAUSTED"}}`)),
	}
	err := modifyResponse(resp)

	if !errors.Is(err, errUpstreamRetryable) {
		t.Fatalf("modifyResponse err=%v; want errUpstreamRetryable (fall through)", err)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("resp.StatusCode=%d; want 502", resp.StatusCode)
	}
	out, _ := io.ReadAll(resp.Body)
	bodyStr := string(out)
	if !strings.Contains(bodyStr, `"type":"upstream_error"`) {
		t.Fatalf("resp body missing OpenAI envelope type=upstream_error; got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "quota exceeded") {
		t.Fatalf("resp body missing translated message; got: %s", bodyStr)
	}
}
