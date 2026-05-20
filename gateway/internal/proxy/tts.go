// Package proxy (tts.go): the TTS request pipeline for POST /v1/audio/speech.
//
// Two upstream tiers, both engine-OpenAI-shaped on the gateway boundary:
//
//   - tier-0 = the pod Chatterbox Multilingual server (Wave 0 GATE 1 swap from
//     Kani). It speaks OpenAI POST /v1/audio/speech 1:1 and emits 24kHz WAV.
//     NewTTSProxy is a plain JSON->binary reverse proxy for that tier — it
//     forwards the JSON body unchanged and streams the binary audio back with
//     the upstream Content-Type preserved. This is the OPPOSITE data direction
//     to audio.go (which uploads multipart audio and returns JSON), so the
//     body handling is deliberately NOT shared (RESEARCH §Anti-Patterns).
//
//   - tier-1 = the Piper fallback, served through NewPiperTTSAdapter per the
//     locked GATE 3 Option A contract (06.7-WAVE0-GATES.md): translate the
//     OpenAI JSON {input,voice} into a Piper form POST /tts {text:input,voice},
//     then convert Piper's raw mu-law (ulaw) 8kHz response into WAV 16kHz
//     16-bit PCM mono via a pure-Go mu-law LUT + RIFF header (NO ffmpeg).
//     response_format wav (default) + pcm supported; mp3/opus/flac/aac -> a
//     clean OpenAI-shaped 400.
//
// Both tiers are wired into the role-based dispatcher (cmd/gateway/main.go)
// exactly like chat/audio so the tier-0->tier-1 breaker fallback works.
package proxy

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

// NewTTSProxy constructs the tier-0 reverse proxy for POST /v1/audio/speech.
// It mirrors the CONSTRUCTOR structure of NewAudioProxy (Director=BuildDirector
// strips client auth + sets the gateway's X-Request-ID, ErrorHandler emits an
// OpenAI-shaped 502 on dial failure, ModifyResponse runs the usage/billing
// interceptors) but tunes the transport for long synthesis:
//
//   - ResponseHeaderTimeout 60s — Chatterbox RTF ~1.0, so a few seconds of
//     speech can take a few seconds to synthesize; allow generous headroom
//     (RESEARCH §Pattern 3) without hanging forever on a wedged upstream.
//   - default (buffered) FlushInterval — the speech response is a single
//     binary WAV body, not an SSE stream, so no per-chunk flush is needed.
//
// The JSON body is forwarded unchanged (Chatterbox speaks OpenAI 1:1) and the
// binary audio response is streamed back with the upstream Content-Type
// preserved. We do NOT copy audio.go's multipart handling — the data
// direction is opposite (JSON in, binary out).
func NewTTSProxy(upstreamURL string, log *slog.Logger, interceptors ...ProxyResponseInterceptor) (*httputil.ReverseProxy, error) {
	u, err := url.Parse(upstreamURL)
	if err != nil {
		return nil, fmt.Errorf("proxy/tts: parse %q: %w", upstreamURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("proxy/tts: invalid upstream url %q", upstreamURL)
	}
	rp := &httputil.ReverseProxy{
		Director: BuildDirector(u),
		// FlushInterval deliberately omitted (default 0 = buffered): the
		// speech response is a single binary WAV body, not SSE.
		Transport: &http.Transport{
			MaxIdleConns:          20,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
		},
		ErrorHandler:   ErrorHandler("tts", log),
		ModifyResponse: ComposeInterceptors(interceptors...),
	}
	return rp, nil
}

// ttsSpeechRequest is the subset of the OpenAI POST /v1/audio/speech body the
// gateway needs to inspect: the synth text (input), the voice id, and the
// desired response_format. Extra client fields are ignored on the tier-1 Piper
// path (Piper accepts only text + voice) but the tier-0 path forwards the body
// unchanged so unknown fields reach Chatterbox.
type ttsSpeechRequest struct {
	Input          string `json:"input"`
	Voice          string `json:"voice"`
	ResponseFormat string `json:"response_format"`
}

// piperTTSAdapter is the GATE 3 Option A tier-1 fallback handler. It is an
// http.Handler so it slots into the dispatcher's Proxies map next to the
// tier-0 reverse proxy.
type piperTTSAdapter struct {
	piperURL string
	client   *http.Client
	log      *slog.Logger
}

// NewPiperTTSAdapter builds the tier-1 Piper fallback handler per GATE 3
// Option A (06.7-WAVE0-GATES.md §GATE 3, 5 sub-fields locked). piperURL is the
// base URL of the live Piper server (UpstreamTTSPiperURL) whose POST /tts
// endpoint returns raw mu-law 8kHz (Content-Type audio/basic).
func NewPiperTTSAdapter(piperURL string, log *slog.Logger) (http.Handler, error) {
	u, err := url.Parse(piperURL)
	if err != nil {
		return nil, fmt.Errorf("proxy/tts: parse piper url %q: %w", piperURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("proxy/tts: invalid piper url %q", piperURL)
	}
	return &piperTTSAdapter{
		piperURL: strings.TrimRight(piperURL, "/"),
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
		log: log.With("module", "PROXY", "upstream", "tts_piper"),
	}, nil
}

func (a *piperTTSAdapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpx.WriteOpenAIError(w, http.StatusBadRequest,
			"invalid_request_error", "invalid_body",
			"Could not read the request body.")
		return
	}
	var req ttsSpeechRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httpx.WriteOpenAIError(w, http.StatusBadRequest,
			"invalid_request_error", "invalid_json",
			"Request body must be valid JSON.")
		return
	}
	if strings.TrimSpace(req.Input) == "" {
		httpx.WriteOpenAIError(w, http.StatusBadRequest,
			"invalid_request_error", "missing_input",
			"The 'input' field is required.")
		return
	}

	// GATE 3 sub-field 5: response_format wav (default) + pcm only.
	format := strings.ToLower(strings.TrimSpace(req.ResponseFormat))
	if format == "" {
		format = "wav"
	}
	if format != "wav" && format != "pcm" {
		httpx.WriteOpenAIError(w, http.StatusBadRequest,
			"invalid_request_error", "unsupported_response_format",
			fmt.Sprintf("response_format %q is not supported by the Piper fallback (use 'wav' or 'pcm').", req.ResponseFormat))
		return
	}

	// GATE 3 sub-field 3: JSON {input,voice} -> Piper form POST /tts
	// {text:input, voice:voice}.
	form := url.Values{}
	form.Set("text", req.Input)
	if v := strings.TrimSpace(req.Voice); v != "" {
		form.Set("voice", v)
	}
	preq, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		a.piperURL+"/tts", strings.NewReader(form.Encode()))
	if err != nil {
		ErrorHandler("tts_piper", a.log)(w, r, err)
		return
	}
	preq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// X-Request-ID propagation for log correlation (BuildDirector does this
	// for the tier-0 reverse proxy; replicate it on the adapter path).
	if rid := httpx.RequestIDFrom(r.Context()); rid != "" {
		preq.Header.Set("X-Request-ID", rid)
	}

	presp, err := a.client.Do(preq)
	if err != nil {
		ErrorHandler("tts_piper", a.log)(w, r, err)
		return
	}
	defer presp.Body.Close()

	if presp.StatusCode != http.StatusOK {
		// Surface Piper's failure as an OpenAI-shaped 502 (it is an upstream
		// error from the gateway client's POV), not a raw passthrough.
		a.log.WarnContext(r.Context(), "piper upstream non-200",
			"status", presp.StatusCode,
			"request_id", httpx.RequestIDFrom(r.Context()))
		httpx.WriteOpenAIError(w, http.StatusBadGateway,
			"api_error", "upstream_unreachable",
			"The Piper fallback upstream returned an error.")
		return
	}

	ulaw, err := io.ReadAll(presp.Body)
	if err != nil {
		ErrorHandler("tts_piper", a.log)(w, r, err)
		return
	}

	// GATE 3 sub-field 2: pure-Go mu-law 8kHz -> PCM16 decode (256-entry LUT).
	pcm := decodeMuLawToPCM16(ulaw)

	if format == "pcm" {
		// GATE 3 sub-field 4: raw PCM16LE body, audio/pcm Content-Type.
		w.Header().Set("Content-Type", "audio/pcm")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(pcm)
		return
	}

	// GATE 3 sub-field 1+4: wrap PCM16 in a WAV 16kHz 16-bit mono RIFF header,
	// audio/wav Content-Type. NOTE the conversion target sample rate is 16kHz:
	// Piper emits 8kHz mu-law; the locked contract upsamples by 2x via simple
	// sample duplication so the WAV header's declared rate (16000) matches the
	// PCM payload's effective rate. This keeps the adapter pure-Go (no resampler
	// library) while honoring the 16kHz contract.
	pcm16k := upsample2xPCM16(pcm)
	wav := wrapPCM16AsWAV(pcm16k, 16000, 1)
	w.Header().Set("Content-Type", "audio/wav")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(wav)
}

// muLawDecodeTable is the 256-entry G.711 mu-law -> 16-bit linear PCM lookup
// table (GATE 3 sub-field 2: pure-Go mu-law decode, no ffmpeg). Generated once
// at init from the standard mu-law decode formula.
var muLawDecodeTable [256]int16

func init() {
	const bias = 0x84 // 132
	for i := 0; i < 256; i++ {
		// mu-law bytes are stored complemented.
		u := ^uint8(i)
		sign := u & 0x80
		exponent := (u >> 4) & 0x07
		mantissa := u & 0x0F
		sample := (int(mantissa) << 3) + bias
		sample <<= uint(exponent)
		sample -= bias
		if sign != 0 {
			sample = -sample
		}
		muLawDecodeTable[i] = int16(sample)
	}
}

// decodeMuLawToPCM16 decodes a mu-law 8kHz byte stream into little-endian
// 16-bit linear PCM (one input byte -> two output bytes).
func decodeMuLawToPCM16(ulaw []byte) []byte {
	out := make([]byte, 0, len(ulaw)*2)
	var b [2]byte
	for _, u := range ulaw {
		binary.LittleEndian.PutUint16(b[:], uint16(muLawDecodeTable[u]))
		out = append(out, b[0], b[1])
	}
	return out
}

// upsample2xPCM16 doubles the sample rate of a PCM16LE stream by duplicating
// each 16-bit sample. Used to lift Piper's 8kHz decode to the GATE 3 16kHz
// contract without a DSP resampler dependency.
func upsample2xPCM16(pcm []byte) []byte {
	if len(pcm) < 2 {
		return pcm
	}
	out := make([]byte, 0, len(pcm)*2)
	for i := 0; i+1 < len(pcm); i += 2 {
		out = append(out, pcm[i], pcm[i+1], pcm[i], pcm[i+1])
	}
	return out
}

// wrapPCM16AsWAV writes a canonical 44-byte RIFF/WAVE header for 16-bit PCM
// mono/stereo at the given sample rate, followed by the PCM payload. Pure-Go,
// no ffmpeg (GATE 3 sub-field 2).
func wrapPCM16AsWAV(pcm []byte, sampleRate, channels int) []byte {
	const bitsPerSample = 16
	byteRate := sampleRate * channels * bitsPerSample / 8
	blockAlign := channels * bitsPerSample / 8
	dataLen := len(pcm)
	riffLen := 36 + dataLen

	buf := bytes.NewBuffer(make([]byte, 0, 44+dataLen))
	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, uint32(riffLen))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	_ = binary.Write(buf, binary.LittleEndian, uint32(16)) // PCM fmt chunk size
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))  // audio format = PCM
	_ = binary.Write(buf, binary.LittleEndian, uint16(channels))
	_ = binary.Write(buf, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(buf, binary.LittleEndian, uint32(byteRate))
	_ = binary.Write(buf, binary.LittleEndian, uint16(blockAlign))
	_ = binary.Write(buf, binary.LittleEndian, uint16(bitsPerSample))
	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, uint32(dataLen))
	buf.Write(pcm)
	return buf.Bytes()
}
