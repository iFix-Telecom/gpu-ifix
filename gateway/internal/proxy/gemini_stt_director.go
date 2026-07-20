// Package proxy (gemini_stt_director.go): Director + ModifyResponse pair
// for the Gemini 2.5 Flash Lite tier-1 STT fallback (Phase 11.2 D-B4).
//
// Unlike the OpenAI Whisper director (multipart→multipart with only a "model"
// field rewrite), Gemini requires a wholesale wire-shape translation:
//
//   - Inbound:  OpenAI-style POST /v1/audio/transcriptions multipart/form-data
//   - Outbound: Gemini POST /v1beta/models/<model>:generateContent application/json
//     with `contents[0].parts[]` = [{text:<prompt>}, {inline_data:{mime_type, data:base64}}]
//
// ModifyResponse flattens the Gemini envelope
// `{candidates:[{content:{parts:[{text:"..."}]}}]}` back into the OpenAI
// shape `{"text":"..."}` so downstream consumers (and the dispatcher's
// uniform success contract) see the same response as openai-whisper.
//
// AUTH HEADER (Pitfall 3): Gemini expects `x-goog-api-key: <key>` — NOT
// `Authorization: Bearer ...` (which Gemini rejects). The director:
//  1. lets BuildDirector's clientAuthHeaders strip the inbound Authorization,
//  2. explicitly r.Header.Del("Authorization") again as a belt-and-suspenders
//     guard against any future re-injection in the base wrapper, and
//  3. r.Header.Set("x-goog-api-key", apiKey).
//
// CRITICAL: header name is "x-goog-api-key" NOT "X-API-Key" — the latter is
// stripped by BuildDirector (see director.go:32 clientAuthHeaders).
//
// SIZE CAP: Gemini's inline_data path accepts up to ~20 MB of request body.
// Base64 inflates audio by ~33%, so audio >18 MB pre-base64 exceeds that cap
// (see RESEARCH §Pitfall 2). The director only WARN-logs over the cap and
// forwards the request; Gemini will 400/413 and the breaker IsSuccessful
// filter keeps the breaker CLOSED on 4xx. A wrapper handler that 413s
// pre-flight is deferred to a follow-up (D-B4 scope).
package proxy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)

// geminiMaxInlineBytes is the soft cutoff for inline_data base64 (~20 MB
// request body limit at the Gemini side). Director WARN-logs above this
// threshold; an explicit 413 wrapper is deferred to a follow-up.
const geminiMaxInlineBytes = 20 * 1024 * 1024

// geminiTranscribePrompt is the language-neutral fallback instruction prepended
// to the audio inline_data part. It pins verbatim transcription in the ORIGINAL
// spoken language and forbids translation — the previous prompt omitted both and
// Gemini defaulted to English on short/ambiguous pt-BR audio (Phase 22-03 UAT).
const geminiTranscribePrompt = "Transcribe this audio verbatim in its original spoken language. Do NOT translate. Return only the transcription text, no commentary."

// geminiTranscribePromptFor builds the transcription instruction, pinning the
// output language when the client supplied one via the OpenAI `language` form
// field (ISO-639-1, e.g. "pt"). Whisper honors that field; the Gemini director
// must fold it into the prompt because generateContent has no language param.
func geminiTranscribePromptFor(language string) string {
	if language = strings.TrimSpace(language); language != "" {
		return fmt.Sprintf(
			"Transcribe this audio verbatim in the language with ISO-639 code %q. Do NOT translate. Return only the transcription text, no commentary.",
			language,
		)
	}
	return geminiTranscribePrompt
}

// geminiDefaultModel is the fallback when neither env nor schema yields a
// target via resolver.Resolve (D-B7 default).
const geminiDefaultModel = "gemini-2.5-flash-lite"

type geminiInlineData struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"` // base64-encoded audio bytes
}

type geminiPart struct {
	Text       string            `json:"text,omitempty"`
	InlineData *geminiInlineData `json:"inline_data,omitempty"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiRequest struct {
	Contents []geminiContent `json:"contents"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error,omitempty"`
}

// BuildGeminiSTTDirector returns a Director + ModifyResponse pair suitable
// for httputil.ReverseProxy.
//
// Director rewrites:
//   - URL path → /v1beta/models/<model>:generateContent (model via resolver)
//   - Auth: Del("Authorization") + Set("x-goog-api-key", apiKey)
//   - Body multipart → JSON geminiRequest (inline_data base64)
//   - Content-Type → application/json
//   - Method → POST
//
// ModifyResponse:
//   - HTTP 200 + non-error envelope → flatten to {"text": "<extracted>"}
//   - Gemini error envelope (`error.code/message/status`) → translate to OpenAI
//     envelope {error:{message,type:"upstream_error",code}} and stamp HTTP 502
//   - Non-200 without parseable error → pass through unchanged
func BuildGeminiSTTDirector(
	upstream *url.URL,
	apiKey string,
	resolver *models.Resolver,
	upstreamName string,
	log *slog.Logger,
) (func(*http.Request), func(*http.Response) error) {
	base := BuildDirector(upstream)

	director := func(r *http.Request) {
		base(r)

		// Resolve target model. Env-override-wins via upstreamEnvVarMap
		// (UPSTREAM_STT_FALLBACK_1_MODEL for "gemini-stt"). Fall back to
		// the documented default when neither env nor schema yields a
		// value.
		model := resolver.Resolve("whisper", upstreamName)
		if model == "" || model == "whisper" {
			// "whisper" is the passthrough alias — treat as unset.
			model = geminiDefaultModel
		}

		// Auth header swap. BuildDirector already stripped Authorization
		// via clientAuthHeaders, but Del is idempotent and pinned here for
		// review-visibility (Pitfall 3).
		r.Header.Del("Authorization")
		if apiKey != "" {
			// CRITICAL: header name is "x-goog-api-key" NOT "X-API-Key"
			// — the latter is stripped by BuildDirector (director.go:32).
			r.Header.Set("x-goog-api-key", apiKey)
		}

		ct := r.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "multipart/form-data") || r.Body == nil {
			// Defensive: shouldn't happen on /v1/audio/transcriptions.
			return
		}

		body, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			return
		}

		audioBytes, mimeType, perr := extractAudioFromMultipart(body, ct)
		if perr != nil || len(audioBytes) == 0 {
			// Pass-through — upstream will reject; breaker stays CLOSED on
			// 4xx per IsSuccessful.
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			return
		}

		if len(audioBytes) > geminiMaxInlineBytes {
			// Soft cap — see Pitfall 2. Explicit 413 deferred to a wrapper.
			if log != nil {
				log.Warn("gemini_stt audio exceeds inline cutoff",
					"upstream", upstreamName,
					"bytes", len(audioBytes),
					"cutoff", geminiMaxInlineBytes,
				)
			}
		}

		// Fold the OpenAI `language` form field into the prompt — generateContent
		// has no language param, so without this Gemini defaults to English on
		// short/ambiguous audio (Phase 22-03 UAT regression).
		language := extractMultipartField(body, ct, "language")

		req := geminiRequest{
			Contents: []geminiContent{{Parts: []geminiPart{
				{Text: geminiTranscribePromptFor(language)},
				{InlineData: &geminiInlineData{
					MimeType: mimeType,
					Data:     base64.StdEncoding.EncodeToString(audioBytes),
				}},
			}}},
		}
		jsonBody, jerr := json.Marshal(req)
		if jerr != nil {
			return
		}

		// Rewrite the URL path. BuildDirector joined upstream.Path with the
		// inbound path; we replace it with the Gemini-specific endpoint
		// rooted at upstream.Path (if any). Match openai-whisper's pattern:
		// strip back to the upstream base and append the Gemini suffix.
		newPath := fmt.Sprintf("/v1beta/models/%s:generateContent", model)
		if upstream.Path != "" && upstream.Path != "/" {
			// upstream.Path already contains the API base (e.g. "/v1beta").
			// In that case the new path is just `/models/<model>:generateContent`
			// joined under upstream.Path. BuildDirector's path.Join already
			// produced upstream.Path + /v1/audio/transcriptions; we overwrite.
			if strings.HasSuffix(upstream.Path, "/v1beta") || strings.HasSuffix(upstream.Path, "/v1beta/") {
				newPath = strings.TrimSuffix(upstream.Path, "/") + fmt.Sprintf("/models/%s:generateContent", model)
			} else {
				newPath = strings.TrimSuffix(upstream.Path, "/") + fmt.Sprintf("/v1beta/models/%s:generateContent", model)
			}
		}
		r.URL.Path = newPath
		r.URL.RawPath = ""

		r.Body = io.NopCloser(bytes.NewReader(jsonBody))
		r.ContentLength = int64(len(jsonBody))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Content-Length", strconv.Itoa(len(jsonBody)))
		r.Method = http.MethodPost
	}

	modifyResponse := func(resp *http.Response) error {
		// Non-200 with parseable Gemini error envelope → translate; non-200
		// without one → pass through (dispatcher 502 envelope wraps).
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			resp.Body = io.NopCloser(bytes.NewReader(nil))
			return err
		}

		var gresp geminiResponse
		_ = json.Unmarshal(body, &gresp) // tolerate parse miss; handled below

		if gresp.Error != nil {
			oa, _ := json.Marshal(map[string]any{
				"error": map[string]any{
					"message": gresp.Error.Message,
					"type":    "upstream_error",
					"code":    gresp.Error.Status,
				},
			})
			resp.Body = io.NopCloser(bytes.NewReader(oa))
			resp.ContentLength = int64(len(oa))
			resp.StatusCode = http.StatusBadGateway
			resp.Header.Set("Content-Type", "application/json")
			resp.Header.Set("Content-Length", strconv.Itoa(len(oa)))
			// RES-13 intra-tier failover: a provider error (e.g. Gemini
			// UNAVAILABLE) is retryable. Return the sentinel so the
			// dispatcher's ErrorHandler suppresses this 502 and cascadeTier1
			// advances to the next STT candidate (openai-whisper) AND records
			// gemini-stt's breaker failure. Pre-byte-safe: STT never streams,
			// so no byte reached the client yet.
			return errUpstreamRetryable
		}

		if resp.StatusCode != http.StatusOK {
			// Non-error, non-200 (no parseable envelope) — still a failure;
			// fall through to the next STT candidate rather than serving the
			// raw non-200 body to the client.
			resp.Body = io.NopCloser(bytes.NewReader(body))
			resp.ContentLength = int64(len(body))
			return errUpstreamRetryable
		}

		// Flatten candidates[0].content.parts[*].text. Concatenate parts to
		// guard against A5 (chunked transcription split across parts).
		var sb strings.Builder
		if len(gresp.Candidates) > 0 {
			for _, p := range gresp.Candidates[0].Content.Parts {
				sb.WriteString(p.Text)
			}
		}
		out, _ := json.Marshal(map[string]string{"text": sb.String()})
		resp.Body = io.NopCloser(bytes.NewReader(out))
		resp.ContentLength = int64(len(out))
		resp.Header.Set("Content-Type", "application/json")
		resp.Header.Set("Content-Length", strconv.Itoa(len(out)))
		return nil
	}

	return director, modifyResponse
}

// extractAudioFromMultipart parses an OpenAI-style multipart body and
// returns the "file" part's bytes + its declared MIME type. Other parts
// are drained. Returns an error when the body is not multipart or no
// "file" part is found.
func extractAudioFromMultipart(body []byte, contentType string) ([]byte, string, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, "", err
	}
	if !strings.HasPrefix(mediaType, "multipart/") {
		return nil, "", fmt.Errorf("not multipart: %s", mediaType)
	}
	boundary, ok := params["boundary"]
	if !ok || boundary == "" {
		return nil, "", fmt.Errorf("missing boundary in multipart content-type")
	}
	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			return nil, "", perr
		}
		if part.FormName() == "file" {
			audio, rerr := io.ReadAll(part)
			_ = part.Close()
			if rerr != nil {
				return nil, "", rerr
			}
			// Trust the declared Content-Type ONLY if it's a concrete audio/*
			// type. Otherwise (empty, application/octet-stream, or any
			// non-audio/* value) sniff the real format from magic bytes —
			// Gemini rejects generic MIME (application/octet-stream) with
			// HTTP 502 INVALID_ARGUMENT even for perfectly valid audio.
			declared := part.Header.Get("Content-Type")
			var mimeType string
			if strings.HasPrefix(declared, "audio/") {
				mimeType = declared
			} else {
				mimeType = sniffAudioMIME(audio)
			}
			return audio, mimeType, nil
		}
		_, _ = io.Copy(io.Discard, part)
		_ = part.Close()
	}
	return nil, "", fmt.Errorf("no file part")
}

// extractMultipartField returns the value of a non-file form field (e.g.
// "language") from an OpenAI-style multipart body, or "" when absent/unparseable.
// Best-effort: a second lightweight pass over the in-memory body — the "file"
// part is drained without buffering its bytes.
func extractMultipartField(body []byte, contentType, fieldName string) string {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		return ""
	}
	boundary, ok := params["boundary"]
	if !ok || boundary == "" {
		return ""
	}
	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, perr := mr.NextPart()
		if perr != nil {
			break
		}
		if part.FormName() == fieldName {
			val, rerr := io.ReadAll(part)
			_ = part.Close()
			if rerr != nil {
				return ""
			}
			return strings.TrimSpace(string(val))
		}
		_, _ = io.Copy(io.Discard, part)
		_ = part.Close()
	}
	return ""
}

// sniffAudioMIME detects the audio container/codec from the leading magic
// bytes and returns the matching audio/* MIME type. It is used when the
// multipart "file" part carries no (or a non-audio/*) Content-Type — Gemini
// rejects a generic MIME (e.g. application/octet-stream) even for valid audio.
// Unknown or too-short buffers fall back to "audio/wav" (a safe default that
// Gemini accepts). All length checks are guarded before indexing.
func sniffAudioMIME(audio []byte) string {
	n := len(audio)

	// OggS — Ogg (Opus/Vorbis). 0x4F 0x67 0x67 0x53.
	if n >= 4 && bytes.HasPrefix(audio, []byte("OggS")) {
		return "audio/ogg"
	}
	// RIFF....WAVE — WAV. "RIFF" at 0-3, "WAVE" at 8-11.
	if n >= 12 && bytes.Equal(audio[0:4], []byte("RIFF")) && bytes.Equal(audio[8:12], []byte("WAVE")) {
		return "audio/wav"
	}
	// fLaC — native FLAC. 0x66 0x4C 0x61 0x43.
	if n >= 4 && bytes.HasPrefix(audio, []byte("fLaC")) {
		return "audio/flac"
	}
	// EBML header 0x1A 0x45 0xDF 0xA3 — Matroska/WebM.
	if n >= 4 && audio[0] == 0x1A && audio[1] == 0x45 && audio[2] == 0xDF && audio[3] == 0xA3 {
		return "audio/webm"
	}
	// ....ftyp — ISO-BMFF (M4A/mp4/isom). "ftyp" at offset 4-7.
	if n >= 8 && bytes.Equal(audio[4:8], []byte("ftyp")) {
		return "audio/mp4"
	}
	// ID3 tag — MP3 with metadata. 0x49 0x44 0x33.
	if n >= 3 && audio[0] == 'I' && audio[1] == 'D' && audio[2] == '3' {
		return "audio/mpeg"
	}
	// MPEG frame sync — MP3 without ID3. 0xFF followed by 0xE0-mask top bits
	// set (0xFB/0xF3/0xF2/0xFA/0xF9/... — top 3 bits of byte 1 all 1s).
	if n >= 2 && audio[0] == 0xFF && (audio[1]&0xE0) == 0xE0 {
		return "audio/mpeg"
	}

	// Unknown / too short — safe fallback Gemini accepts.
	return "audio/wav"
}
