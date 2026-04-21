// Package proxy (interceptor_usage.go): dual-shape SSE usage extractor.
// Wraps streaming responses in a tee reader that scans for top-level
// "usage" objects emitted by both OpenAI-compatible upstreams (separate
// final chunk with empty choices[]) AND llama.cpp-flavoured upstreams
// (usage in the same chunk as finish_reason=stop).
//
// For non-streaming responses the interceptor is a no-op — the dispatcher
// path reads response.usage from the JSON body directly via ExtractFromBody
// after buffering.
//
// Pitfall 5 guard (SSE [DONE] sentinel): the OpenAI/llama.cpp protocol
// terminates the stream with `data: [DONE]\n\n`. The tee reader MUST
// tolerate this frame — we filter explicitly before invoking json.Unmarshal.
package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/billing"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
)

// UsageInterceptor implements ProxyResponseInterceptor. One instance per
// gateway process; threadsafe. Carries a reference to the global
// billing.Accountant so per-request counters can be found by request_id.
type UsageInterceptor struct {
	accountant *billing.Accountant
	log        *slog.Logger
}

// Compile-time assertion that UsageInterceptor satisfies ProxyResponseInterceptor.
var _ ProxyResponseInterceptor = (*UsageInterceptor)(nil)

// NewUsageInterceptor wires the interceptor. The accountant is expected to
// live the lifetime of the gateway process (main owns it).
func NewUsageInterceptor(a *billing.Accountant, log *slog.Logger) *UsageInterceptor {
	if log == nil {
		log = slog.Default()
	}
	return &UsageInterceptor{
		accountant: a,
		log:        log.With("module", "USAGE_INTERCEPTOR"),
	}
}

// Intercept wraps the response body with a tee that scans for SSE frames.
// For non-streaming responses it returns nil and leaves resp.Body alone —
// caller should invoke ExtractFromBody post-Read of the buffered body.
func (u *UsageInterceptor) Intercept(resp *http.Response) error {
	if resp == nil || resp.Request == nil {
		return nil
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return nil
	}
	reqID := httpx.RequestIDFrom(resp.Request.Context())
	if reqID == "" {
		return nil
	}
	usage := &billing.RequestUsage{}
	u.accountant.Set(reqID, usage)
	resp.Body = &usageTeeReader{
		upstream: resp.Body,
		usage:    usage,
		log:      u.log,
	}
	return nil
}

// ExtractFromBody parses a non-streaming response body for `usage` and
// writes it onto the per-request RequestUsage. Caller is responsible for
// invoking after Read of the buffered body.
func (u *UsageInterceptor) ExtractFromBody(reqID string, body []byte) {
	if reqID == "" || u.accountant == nil {
		return
	}
	var f sseUsageFrame
	if err := json.Unmarshal(body, &f); err != nil || f.Usage == nil {
		return
	}
	usage := u.accountant.Get(reqID)
	if usage == nil {
		usage = &billing.RequestUsage{}
		u.accountant.Set(reqID, usage)
	}
	usage.TokensIn.Store(int64(f.Usage.PromptTokens))
	usage.TokensOut.Store(int64(f.Usage.CompletionTokens))
}

// sseUsageFrame is the minimal shape we need from an SSE data frame. Both
// OpenAI and llama.cpp emit usage as a top-level key; we ignore everything
// else (choices, id, created, etc.) so the tee is tolerant of shape drift.
type sseUsageFrame struct {
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
}

// usageTeeReader is an io.ReadCloser wrapper that forwards reads to the
// upstream body while scanning completed SSE frames for a top-level
// "usage" object. Mirrors proxy/toolcall.go toolCallTee but swaps the
// substring scan for a structured JSON parse.
type usageTeeReader struct {
	upstream io.ReadCloser
	usage    *billing.RequestUsage
	log      *slog.Logger
	buf      bytes.Buffer
}

func (r *usageTeeReader) Read(p []byte) (int, error) {
	n, err := r.upstream.Read(p)
	if n > 0 {
		_, _ = r.buf.Write(p[:n])
		r.scanFrames()
	}
	return n, err
}

// Close flushes the buffer (last frame may not include the \n\n terminator)
// then delegates to the upstream Close. Per proxy/interceptor.go godoc,
// wrappers MUST delegate Close.
func (r *usageTeeReader) Close() error {
	r.scanFrames()
	return r.upstream.Close()
}

// scanFrames extracts complete SSE events (terminated by \n\n) from buf,
// attempts JSON parse on each `data:` line, and stores any "usage" it
// finds onto r.usage (atomic). Tolerates both OpenAI and llama.cpp shapes
// and the `data: [DONE]` sentinel (Pitfall 5).
func (r *usageTeeReader) scanFrames() {
	for {
		data := r.buf.Bytes()
		i := bytes.Index(data, []byte("\n\n"))
		if i < 0 {
			return
		}
		// Copy the frame bytes before advancing the buffer — otherwise the
		// slice aliases memory that Next() will recycle.
		frame := make([]byte, i)
		copy(frame, data[:i])
		r.buf.Next(i + 2)

		for _, line := range bytes.Split(frame, []byte("\n")) {
			line = bytes.TrimPrefix(line, []byte("data: "))
			line = bytes.TrimSpace(line)
			if len(line) == 0 || bytes.Equal(line, []byte("[DONE]")) {
				continue
			}
			var f sseUsageFrame
			if err := json.Unmarshal(line, &f); err != nil {
				continue
			}
			if f.Usage == nil {
				continue
			}
			r.usage.TokensIn.Store(int64(f.Usage.PromptTokens))
			r.usage.TokensOut.Store(int64(f.Usage.CompletionTokens))
			if r.log != nil {
				r.log.Debug("usage extracted from SSE",
					"prompt", f.Usage.PromptTokens,
					"completion", f.Usage.CompletionTokens)
			}
		}
	}
}
