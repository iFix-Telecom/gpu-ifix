package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)

// sttLocalAliasFixture is the (whisper, local-stt) -> Systran/faster-whisper-large-v3
// row that migration 0029 step 3 seeds in production. The two Speaches-bound STT
// paths (local-stt + emergency_pod_stt) resolve against "local-stt" so this row is
// the one that converts the public alias to the pod's installed model id.
var sttLocalAliasFixture = map[[2]string]string{
	{"whisper", "local-stt"}: "Systran/faster-whisper-large-v3",
}

// ---------------------------------------------------------------------------
// Task 1 — local-stt audio path (NewAudioProxy reuses BuildOpenAIWhisperDirector
// with an EMPTY bearer + upstreamName "local-stt").
// ---------------------------------------------------------------------------

// TestLocalSTTDirector_RewritesModelToUpstreamTarget asserts a multipart body
// with model=whisper is forwarded with model=Systran/faster-whisper-large-v3
// when resolved against the local-stt upstream.
func TestLocalSTTDirector_RewritesModelToUpstreamTarget(t *testing.T) {
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	resolver := models.NewResolverForTesting(sttLocalAliasFixture)
	// authBearer="" — local-stt has NO bearer (it is a tier-0 Speaches pod).
	director := BuildOpenAIWhisperDirector(upstream, "", resolver, "local-stt", discardLogger())

	wav := loadProbeWAV(t)
	body, ct := buildMultipartBody(t, []string{"whisper"}, "probe.wav", wav)
	req, forwarded := applyDirector(t, director, http.MethodPost, "/v1/audio/transcriptions", ct, body, nil, nil)

	gotModel, gotFile, perr := parseMultipartFromBytes(forwarded, req.Header.Get("Content-Type"))
	if perr != nil {
		t.Fatalf("forwarded body parse: %v", perr)
	}
	if gotModel != "Systran/faster-whisper-large-v3" {
		t.Errorf("model = %q, want Systran/faster-whisper-large-v3", gotModel)
	}
	if !bytes.Equal(gotFile, wav) {
		t.Errorf("audio bytes mutated: got %d bytes, want %d bytes (byte-identical required)",
			len(gotFile), len(wav))
	}
}

// TestLocalSTTDirector_AudioBytesByteIdentical stresses byte preservation with a
// tricky payload (0x00 / 0xff / a fake \r\n--boundary sequence). If the rewrite
// decoded part bodies as text or re-encoded, these would corrupt.
func TestLocalSTTDirector_AudioBytesByteIdentical(t *testing.T) {
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	resolver := models.NewResolverForTesting(sttLocalAliasFixture)
	director := BuildOpenAIWhisperDirector(upstream, "", resolver, "local-stt", discardLogger())

	tricky := bytes.Join([][]byte{
		{0x00, 0x01, 0x02, 0x03, 0xff, 0xfe, 0xfd},
		[]byte("\r\n--fake-boundary-123\r\n"),
		{0x00, 0x00, 0x00, 0x00},
		bytes.Repeat([]byte{0xab, 0xcd}, 100),
	}, nil)

	body, ct := buildMultipartBody(t, []string{"whisper"}, "tricky.wav", tricky)
	req, forwarded := applyDirector(t, director, http.MethodPost, "/v1/audio/transcriptions", ct, body, nil, nil)

	gotModel, gotFile, perr := parseMultipartFromBytes(forwarded, req.Header.Get("Content-Type"))
	if perr != nil {
		t.Fatalf("forwarded body parse: %v", perr)
	}
	if gotModel != "Systran/faster-whisper-large-v3" {
		t.Errorf("model = %q, want Systran/faster-whisper-large-v3", gotModel)
	}
	if !bytes.Equal(gotFile, tricky) {
		t.Errorf("audio bytes mutated for tricky payload (zero bytes + boundary-like sequences)")
	}
}

// TestLocalSTTDirector_NoAuthorizationHeader asserts no Authorization header is
// injected on the local-stt path (authBearer must be "" — Speaches pods have no
// bearer; injecting one would leak / break the pod).
func TestLocalSTTDirector_NoAuthorizationHeader(t *testing.T) {
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	resolver := models.NewResolverForTesting(sttLocalAliasFixture)
	director := BuildOpenAIWhisperDirector(upstream, "", resolver, "local-stt", discardLogger())

	body, ct := buildMultipartBody(t, []string{"whisper"}, "a.wav", []byte("RIFFAUDIO"))
	req, _ := applyDirector(t, director, http.MethodPost, "/v1/audio/transcriptions", ct, body, nil, nil)

	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty (local-stt has no bearer)", got)
	}
}

// TestLocalSTTDirector_ResolverMissPassesThrough asserts an empty resolver leaves
// model=whisper unchanged (passthrough — no crash, no auth header). The pod then
// 4xx's; the breaker classifies 4xx as non-failure.
func TestLocalSTTDirector_ResolverMissPassesThrough(t *testing.T) {
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	resolver := models.NewResolverForTesting(nil)
	director := BuildOpenAIWhisperDirector(upstream, "", resolver, "local-stt", discardLogger())

	body, ct := buildMultipartBody(t, []string{"whisper"}, "a.wav", []byte("RIFFAUDIO"))
	req, forwarded := applyDirector(t, director, http.MethodPost, "/v1/audio/transcriptions", ct, body, nil, nil)

	gotModel, _, perr := parseMultipartFromBytes(forwarded, req.Header.Get("Content-Type"))
	if perr != nil {
		t.Fatalf("forwarded body parse: %v", perr)
	}
	if gotModel != "whisper" {
		t.Errorf("model = %q, want whisper (alias passes through on resolver miss)", gotModel)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty", got)
	}
}

// TestLocalSTTDirector_MissingModelInjectsTarget asserts a multipart WITHOUT a
// model field gets the resolved target injected via the canonicalAliasForUpstream
// "local-stt" entry.
func TestLocalSTTDirector_MissingModelInjectsTarget(t *testing.T) {
	srv, _, _ := captureUpstream(t)
	upstream, _ := url.Parse(srv.URL)
	resolver := models.NewResolverForTesting(sttLocalAliasFixture)
	director := BuildOpenAIWhisperDirector(upstream, "", resolver, "local-stt", discardLogger())

	wav := loadProbeWAV(t)
	body, ct := buildMultipartBody(t, nil, "probe.wav", wav)
	req, forwarded := applyDirector(t, director, http.MethodPost, "/v1/audio/transcriptions", ct, body, nil, nil)

	gotModel, gotFile, perr := parseMultipartFromBytes(forwarded, req.Header.Get("Content-Type"))
	if perr != nil {
		t.Fatalf("forwarded body parse: %v", perr)
	}
	if gotModel != "Systran/faster-whisper-large-v3" {
		t.Errorf("model = %q, want Systran/faster-whisper-large-v3 (injected for missing-model)", gotModel)
	}
	if !bytes.Equal(gotFile, wav) {
		t.Errorf("audio bytes mutated when injecting model field")
	}
}

// TestNewAudioProxy_RewritesModelViaResolver is an end-to-end proxy-level test
// (not just the director) proving NewAudioProxy now passes the resolver through
// and rewrites the model field against local-stt — and forwards NO Authorization
// header to the Speaches pod.
func TestNewAudioProxy_RewritesModelViaResolver(t *testing.T) {
	var fwdCT, fwdAuth string
	var fwdBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fwdCT = r.Header.Get("Content-Type")
		fwdAuth = r.Header.Get("Authorization")
		fwdBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	resolver := models.NewResolverForTesting(sttLocalAliasFixture)
	rp, err := NewAudioProxy(srv.URL, discardLogger(), resolver)
	if err != nil {
		t.Fatalf("NewAudioProxy: %v", err)
	}

	wav := loadProbeWAV(t)
	body, ct := buildMultipartBody(t, []string{"whisper"}, "probe.wav", wav)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(body))
	req.Header.Set("Content-Type", ct)
	// A client-supplied Authorization header must NOT be forwarded to the pod.
	req.Header.Set("Authorization", "Bearer client-token-should-be-stripped")
	rec := httptest.NewRecorder()
	rp.ServeHTTP(rec, req)

	gotModel, gotFile, perr := parseMultipartFromBytes(fwdBody, fwdCT)
	if perr != nil {
		t.Fatalf("forwarded body parse: %v", perr)
	}
	if gotModel != "Systran/faster-whisper-large-v3" {
		t.Errorf("model = %q, want Systran/faster-whisper-large-v3", gotModel)
	}
	if !bytes.Equal(gotFile, wav) {
		t.Errorf("audio bytes mutated through NewAudioProxy")
	}
	if fwdAuth != "" {
		t.Errorf("Authorization = %q forwarded to local-stt pod, want empty", fwdAuth)
	}
}
