// Package proxy (tts_test.go): Phase 06.7 Wave 0 RED scaffolding (Nyquist
// gate), UNSKIPPED + asserting real behavior by the owning plan 06.7-07.
//
// ENGINE: primary TTS = Chatterbox Multilingual (Wave 0 GATE 1 swap from
// Kani — see 06.7-WAVE0-GATES.md), serving OpenAI-compatible
// POST /v1/audio/speech and emitting 24kHz WAV. The proxy layer itself is
// engine-agnostic (it forwards JSON in -> binary audio out 1:1); the
// fallback adapter converts Piper ulaw 8kHz -> WAV 16kHz (GATE 3 Option A).
//
// OWNER map (authority: 06.7-02-PLAN.md <stub_ownership_map>):
//   - TestTTSProxy_JSONToBinaryAudio          -> Plan 06.7-07 (implemented)
//   - TestTTSProxy_ErrorEnvelope              -> Plan 06.7-07 (implemented)
//   - TestTTSProxy_PiperFallback_AdapterConverts -> Plan 06.7-07 (implemented)
package proxy

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTTSProxy_JSONToBinaryAudio asserts that NewTTSProxy forwards a JSON
// POST /v1/audio/speech body to the upstream Chatterbox server 1:1 (same
// path, same method), preserves the upstream Content-Type (audio/wav) and the
// binary audio bytes on the response, and STRIPS the client Authorization
// header via BuildDirector.
func TestTTSProxy_JSONToBinaryAudio(t *testing.T) {
	wantAudio := []byte("RIFF....WAVEfake24khz")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/speech" {
			t.Errorf("path got %q, want /v1/audio/speech", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method got %q, want POST", r.Method)
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("Authorization leaked to upstream: %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Request-ID") == "" {
			t.Errorf("no X-Request-ID propagated to upstream")
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "boleto") {
			t.Errorf("body did not contain the JSON input: %s", body)
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(wantAudio)
	}))
	defer upstream.Close()

	rp, err := NewTTSProxy(upstream.URL, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(wrapWithMiddleware(rp))
	defer gateway.Close()

	reqBody := `{"input":"Olá, seu boleto venceu","voice":"v1"}`
	req, _ := http.NewRequest("POST", gateway.URL+"/v1/audio/speech", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer ifix_sk_secret")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status got %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "audio/wav" {
		t.Errorf("Content-Type got %q, want audio/wav", ct)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, wantAudio) {
		t.Errorf("binary audio body not preserved: got %q", got)
	}
}

// TestTTSProxy_ErrorEnvelope asserts that when the upstream TTS server is
// unreachable, the proxy ErrorHandler returns an OpenAI-shaped error envelope
// with HTTP 502 ({"error":{...}}), not a bare Go transport error or a 500.
func TestTTSProxy_ErrorEnvelope(t *testing.T) {
	// Point at an address that refuses connections.
	rp, err := NewTTSProxy("http://127.0.0.1:1", discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(wrapWithMiddleware(rp))
	defer gateway.Close()

	req, _ := http.NewRequest("POST", gateway.URL+"/v1/audio/speech",
		strings.NewReader(`{"input":"hi","voice":"v1"}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status got %d, want 502", resp.StatusCode)
	}
	var env struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("response is not a JSON error envelope: %v", err)
	}
	if env.Error.Code == "" || env.Error.Message == "" {
		t.Errorf("error envelope missing code/message: %+v", env.Error)
	}
}

// TestTTSProxy_PiperFallback_AdapterConverts asserts the GATE 3 Option A
// gateway adapter (06.7-WAVE0-GATES.md §GATE 3): the adapter translates the
// OpenAI JSON {input,voice} into Piper's form POST /tts {text,voice}, then
// converts Piper's raw mu-law 8kHz response into WAV 16kHz 16-bit PCM mono via
// the pure-Go mu-law LUT + RIFF writer (NO ffmpeg), setting Content-Type
// audio/wav. response_format=pcm yields audio/pcm; unsupported -> clean 400.
func TestTTSProxy_PiperFallback_AdapterConverts(t *testing.T) {
	// Fake Piper: assert the JSON->form translation, return mu-law 8kHz.
	var gotText, gotVoice string
	mulawSilence := bytes.Repeat([]byte{0xFF}, 800) // 0xFF mu-law decodes to ~0 (silence)
	piper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tts" {
			t.Errorf("piper path got %q, want /tts", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		gotText = r.PostFormValue("text")
		gotVoice = r.PostFormValue("voice")
		w.Header().Set("Content-Type", "audio/basic")
		_, _ = w.Write(mulawSilence)
	}))
	defer piper.Close()

	adapter, err := NewPiperTTSAdapter(piper.URL, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(wrapWithMiddleware(adapter))
	defer gateway.Close()

	// --- wav (default) path ---
	resp, err := http.Post(gateway.URL+"/v1/audio/speech", "application/json",
		strings.NewReader(`{"input":"Olá mundo","voice":"miro"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status got %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "audio/wav" {
		t.Errorf("Content-Type got %q, want audio/wav", ct)
	}
	if gotText != "Olá mundo" {
		t.Errorf("JSON->form translation failed: text got %q", gotText)
	}
	if gotVoice != "miro" {
		t.Errorf("JSON->form translation failed: voice got %q", gotVoice)
	}
	wav, _ := io.ReadAll(resp.Body)
	assertWAV16kMono(t, wav)

	// --- pcm path ---
	resp2, err := http.Post(gateway.URL+"/v1/audio/speech", "application/json",
		strings.NewReader(`{"input":"x","voice":"miro","response_format":"pcm"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if ct := resp2.Header.Get("Content-Type"); ct != "audio/pcm" {
		t.Errorf("pcm Content-Type got %q, want audio/pcm", ct)
	}

	// --- unsupported format -> clean 400 ---
	resp3, err := http.Post(gateway.URL+"/v1/audio/speech", "application/json",
		strings.NewReader(`{"input":"x","voice":"miro","response_format":"mp3"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusBadRequest {
		t.Errorf("unsupported response_format status got %d, want 400", resp3.StatusCode)
	}
}

// assertWAV16kMono validates the RIFF/WAVE header declares 16000 Hz, mono,
// 16-bit PCM — the locked GATE 3 conversion target.
func assertWAV16kMono(t *testing.T, b []byte) {
	t.Helper()
	if len(b) < 44 {
		t.Fatalf("WAV too short: %d bytes", len(b))
	}
	if string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		t.Fatalf("not a RIFF/WAVE file: %q %q", b[0:4], b[8:12])
	}
	audioFormat := binary.LittleEndian.Uint16(b[20:22])
	channels := binary.LittleEndian.Uint16(b[22:24])
	sampleRate := binary.LittleEndian.Uint32(b[24:28])
	bitsPerSample := binary.LittleEndian.Uint16(b[34:36])
	if audioFormat != 1 {
		t.Errorf("audioFormat got %d, want 1 (PCM)", audioFormat)
	}
	if channels != 1 {
		t.Errorf("channels got %d, want 1 (mono)", channels)
	}
	if sampleRate != 16000 {
		t.Errorf("sampleRate got %d, want 16000", sampleRate)
	}
	if bitsPerSample != 16 {
		t.Errorf("bitsPerSample got %d, want 16", bitsPerSample)
	}
}
