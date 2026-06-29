// Package proxy (interceptor_usage.go): dual-shape SSE usage extractor.
// Wraps streaming responses in a tee reader that scans for top-level
// "usage" objects emitted by both OpenAI-compatible upstreams (separate
// final chunk with empty choices[]) AND llama.cpp-flavoured upstreams
// (usage in the same chunk as finish_reason=stop).
//
// For non-streaming responses the interceptor is a no-op on Intercept;
// the caller (dispatcher/JSON path) must invoke ExtractFromBody after
// buffering. ExtractFromBody ALSO enqueues the billing.Event + deletes
// the accountant slot, keeping the non-streaming path billing-correct.
//
// Pitfall 5 guard (SSE [DONE] sentinel): the OpenAI/llama.cpp protocol
// terminates the stream with `data: [DONE]\n\n`. The tee reader MUST
// tolerate this frame — we filter explicitly before invoking json.Unmarshal.
//
// BL-01 fix (Phase 4 review): the interceptor now carries the full
// billing pipeline wiring (flusher, prices, fx, tenants loader) so the
// Close() hook on the tee can compute cost_local_phantom_brl and
// cost_external_brl, enqueue a billing.Event, and Delete the accountant
// slot (BL-02 fix — prevents copy-on-write map leak).
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auditctx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/billing"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/httpx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/tenants"
)

// UsageInterceptor implements ProxyResponseInterceptor. One instance per
// gateway process; threadsafe. Holds the billing pipeline wiring so the
// tee's Close hook can enqueue a billing.Event with full cost attribution
// per BL-01.
type UsageInterceptor struct {
	accountant    *billing.Accountant
	flusher       *billing.Flusher
	prices        *billing.PricesLoader
	fx            *billing.FXLoader
	tenantsLoader *tenants.Loader
	defaultUSDBRL float64
	log           *slog.Logger
}

// Compile-time assertion that UsageInterceptor satisfies ProxyResponseInterceptor.
var _ ProxyResponseInterceptor = (*UsageInterceptor)(nil)

// NewUsageInterceptor wires the interceptor. All billing collaborators are
// optional — when nil the interceptor degrades to the Phase 4 Plan 04-05
// behaviour (capture usage into the accountant but do NOT enqueue billing
// events). Production wiring (main.go) supplies all of them; unit tests
// may supply only the accountant.
//
// Signature change (BL-01): the Phase 4 Plan 04-06 review flagged that the
// original (accountant, log) form dropped the billing pipeline entirely —
// billing_events never received rows. The flusher / prices / fx / tenants
// parameters plug that gap.
func NewUsageInterceptor(
	accountant *billing.Accountant,
	flusher *billing.Flusher,
	prices *billing.PricesLoader,
	fx *billing.FXLoader,
	tenantsLoader *tenants.Loader,
	defaultUSDBRL float64,
	log *slog.Logger,
) *UsageInterceptor {
	if log == nil {
		log = slog.Default()
	}
	return &UsageInterceptor{
		accountant:    accountant,
		flusher:       flusher,
		prices:        prices,
		fx:            fx,
		tenantsLoader: tenantsLoader,
		defaultUSDBRL: defaultUSDBRL,
		log:           log.With("module", "USAGE_INTERCEPTOR"),
	}
}

// Intercept wraps the response body with a tee that either (a) scans SSE
// frames for a top-level "usage" object, or (b) for non-streaming JSON
// buffers the entire body and parses "usage" on Close. HI-02 fix (Phase 4
// review): previously JSON responses silently lost usage data — every
// non-streaming chat/embed request wrote a billing.Event with tokens_in=
// tokens_out=0.
//
// Binary/multipart responses (audio/*, application/octet-stream, etc.)
// are passed through untouched.
func (u *UsageInterceptor) Intercept(resp *http.Response) error {
	if resp == nil || resp.Request == nil {
		return nil
	}
	reqID := httpx.RequestIDFrom(resp.Request.Context())
	if reqID == "" {
		return nil
	}
	contentType := resp.Header.Get("Content-Type")

	switch {
	case strings.HasPrefix(contentType, "text/event-stream"):
		usage := &billing.RequestUsage{}
		u.accountant.Set(reqID, usage)
		resp.Body = &usageTeeReader{
			upstream: resp.Body,
			usage:    usage,
			reqCtx:   resp.Request.Context(),
			reqPath:  resp.Request.URL.Path,
			reqID:    reqID,
			start:    time.Now(),
			ix:       u,
			log:      u.log,
		}
	case strings.HasPrefix(contentType, "application/json"):
		// HI-02 fix: buffer the JSON body so Close can extract usage
		// AND FinalizeRequest can enqueue the billing.Event. Without
		// this the caller must have a ctx-aware codepath to invoke
		// ExtractFromBody — which the dispatcher did not.
		usage := &billing.RequestUsage{}
		u.accountant.Set(reqID, usage)
		resp.Body = &usageJSONBuffer{
			upstream: resp.Body,
			usage:    usage,
			reqCtx:   resp.Request.Context(),
			reqPath:  resp.Request.URL.Path,
			reqID:    reqID,
			start:    time.Now(),
			ix:       u,
			log:      u.log,
		}
	}
	return nil
}

// ExtractFromBody parses a non-streaming response body for `usage` and
// writes it onto the per-request RequestUsage. Caller is responsible for
// invoking after Read of the buffered body.
//
// Unit-test variant — does NOT finalize. For end-to-end non-streaming
// billing capture, use ExtractFromBodyAndFinalize (HI-02 handling).
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
	if f.Model != "" {
		usage.SetModel(f.Model)
	}
}

// FinalizeRequest builds a billing.Event from the accountant slot, enqueues
// it on the flusher, and deletes the accountant slot. Safe to call multiple
// times for the same reqID — the second call sees a nil usage (already
// deleted) and no-ops. Exported so the JSON/non-streaming dispatcher path
// can drive billing without duplicating the Close() logic on the tee.
//
// BL-02 fix: the deferred Delete below runs on every terminating path
// (including the "nothing to bill" early-returns) so the copy-on-write
// accountant map never leaks. Slot is released once per reqID.
//
// Called from:
//   - usageTeeReader.Close (SSE happy path + abort + upstream cut)
//   - Non-streaming callers once they have the buffered body
func (u *UsageInterceptor) FinalizeRequest(ctx context.Context, reqID string, route string, source string) {
	if u == nil || u.accountant == nil || reqID == "" {
		return
	}
	usage := u.accountant.Get(reqID)
	if usage == nil {
		// Nothing to bill — either the request never started SSE
		// (Content-Type was not event-stream) or a prior Finalize
		// already drained the slot. No-op.
		return
	}
	// Always release the slot on exit — this is the BL-02 fix.
	defer u.accountant.Delete(reqID)

	if u.flusher == nil {
		// Billing pipeline not wired (e.g. older unit test harness). Slot
		// still gets Deleted above so the memory leak closes.
		return
	}

	tokensIn := usage.TokensIn.Load()
	tokensOut := usage.TokensOut.Load()
	audioSeconds := float64(usage.AudioSecondsMs10.Load()) / 10.0
	embedsCount := usage.EmbedsCount.Load()

	if tokensIn == 0 && tokensOut == 0 && audioSeconds == 0 && embedsCount == 0 {
		// No billable work recorded — skip the enqueue (the row would
		// be an empty marker). Accountant slot is still Deleted by the
		// deferred cleanup above.
		return
	}

	reqUUID, perr := uuid.Parse(reqID)
	if perr != nil {
		u.log.Warn("billing finalize: request_id is not a UUID; skipping enqueue",
			"request_id", reqID, "err", perr)
		return
	}

	// Tenant + api_key from auth context. Both zero-UUID if auth was
	// bypassed (defensive).
	var tenantID, apiKeyID uuid.UUID
	if ctx != nil {
		if ac, ok := auth.FromContext(ctx); ok {
			tenantID, _ = uuid.Parse(ac.TenantID)
			apiKeyID, _ = uuid.Parse(ac.APIKeyID)
		}
	}

	// Upstream name: prefer the dispatch-set value (Phase 4 BL-01). Fall
	// back to "unknown" — the reconcile CLI will flag these rows.
	upstream := ""
	if ctx != nil {
		upstream = auditctx.BillingUpstreamFrom(ctx)
	}
	if upstream == "" {
		upstream = "unknown"
	}

	routeName := routeToBillingRoute(route)
	model := usage.Model()

	// Cost attribution:
	//   - cost_external_brl: the upstream's real pricing when non-local.
	//     0 when upstream is local-*.
	//   - cost_local_phantom_brl: openrouter-fireworks reference pricing
	//     regardless of the actual upstream (D-B4) so reports can answer
	//     "how much did the GPU save us".
	isLocal := strings.HasPrefix(upstream, "local-")
	costExternal := 0.0
	if !isLocal {
		provider := providerForUpstream(upstream)
		costExternal = priceTokens(u, model, provider, tokensIn, tokensOut, audioSeconds, embedsCount)
	}
	costPhantom := priceTokens(u, model, "openrouter-fireworks", tokensIn, tokensOut, audioSeconds, embedsCount)

	ev := billing.Event{
		TS:                  time.Now(),
		RequestID:           reqUUID,
		TenantID:            tenantID,
		APIKeyID:            apiKeyID,
		Route:               routeName,
		Upstream:            upstream,
		Model:               model,
		TokensIn:            int(tokensIn),
		TokensOut:           int(tokensOut),
		AudioSeconds:        audioSeconds,
		EmbedsCount:         int(embedsCount),
		CostLocalBRL:        0, // D-B4 — GPU is fixed-cost
		CostLocalPhantomBRL: costPhantom,
		CostExternalBRL:     costExternal,
		Source:              source,
	}
	u.flusher.Enqueue(ev)
	u.log.Debug("billing event enqueued",
		"request_id", reqID,
		"tenant_id", tenantID.String(),
		"route", routeName,
		"upstream", upstream,
		"tokens_in", tokensIn,
		"tokens_out", tokensOut,
		"source", source,
	)
}

// priceTokens resolves unit prices for the given (model, provider) tuple
// and returns the summed BRL cost across input/output tokens, audio
// seconds, and embeds. Returns 0 when all relevant prices are missing.
func priceTokens(u *UsageInterceptor, model, provider string, tokensIn, tokensOut int64, audioSeconds float64, embedsCount int64) float64 {
	var total float64
	if tokensIn > 0 {
		total += billing.ComputeCostBRL(float64(tokensIn), model, provider, "input_token",
			u.prices, u.fx, u.defaultUSDBRL, u.log)
	}
	if tokensOut > 0 {
		total += billing.ComputeCostBRL(float64(tokensOut), model, provider, "output_token",
			u.prices, u.fx, u.defaultUSDBRL, u.log)
	}
	if audioSeconds > 0 {
		total += billing.ComputeCostBRL(audioSeconds, model, provider, "audio_second",
			u.prices, u.fx, u.defaultUSDBRL, u.log)
	}
	if embedsCount > 0 {
		total += billing.ComputeCostBRL(float64(embedsCount), model, provider, "embed_request",
			u.prices, u.fx, u.defaultUSDBRL, u.log)
	}
	return total
}

// providerForUpstream maps an upstream name ("openrouter-chat",
// "openai-embed", etc.) to the provider column in the prices table.
// Upstream names are the authoritative wire identifier; providers group
// pricing by commercial vendor.
func providerForUpstream(upstream string) string {
	switch upstream {
	case "openrouter-chat":
		return "openrouter-fireworks"
	case "openai-embed":
		return "openai"
	case "openai-whisper":
		return "openai"
	default:
		return upstream
	}
}

// applyAudioEmbedUsage is the PURE Phase 16 producer for the audio + embed
// usage dimensions. It operates on a passed-in *billing.RequestUsage so the
// unit tests can observe the Stored atomics WITHOUT a flusher / accountant
// slot / Postgres (the production JSON-buffer Close path deletes the slot via
// FinalizeRequest's deferred accountant.Delete, and the production flusher
// needs a *pgxpool.Pool — so the Store is otherwise unobservable DB-free).
//
// Implements LOCKED CONTEXT DECISION #2 (TEN-04 + OBS-09 producer half):
//   - route "stt": seconds = response `duration` field WHEN present and > 0,
//     ELSE auditctx.RequestAudioSecondsFrom(reqCtx) (the request-derived
//     fallback stamped by RequestAudioSecondsMiddleware). The gateway does NOT
//     force response_format=verbose_json, so the default {"text":"..."}
//     transcription response carries NO duration — without the ELSE branch the
//     common-case client would meter 0. seconds>0 → AudioSecondsMs10.Store(
//     int64(seconds*10)). The ×10 is mandatory for the FinalizeRequest /10.0
//     flush + quota/counters.go /60 math.
//   - route "embed": EmbedsCount.Store(len(data[])) — one per data[] element.
//   - route "chat" / default / nil usage: no-op (the chat token path is
//     produced elsewhere and MUST NOT be touched here).
//
// Called by usageJSONBuffer.Close ONLY (it has both reqCtx + reqPath).
// Deliberately NOT added to ExtractFromBody: that path has no reqCtx (can't
// read the ELSE branch), no reqPath (can't route-dispatch), and early-returns
// on f.Usage==nil — exactly the default STT shape; per HI-02 the dispatcher
// never calls it.
func applyAudioEmbedUsage(usage *billing.RequestUsage, route string, body []byte, reqCtx context.Context) {
	if usage == nil {
		return
	}
	switch route {
	case "stt":
		var seconds float64
		if len(body) > 0 {
			var partial struct {
				Duration *float64 `json:"duration"`
			}
			if err := json.Unmarshal(body, &partial); err == nil &&
				partial.Duration != nil && *partial.Duration > 0 {
				seconds = *partial.Duration
			}
		}
		// ELSE branch: response carried no usable duration → request-derived.
		if seconds <= 0 && reqCtx != nil {
			seconds = auditctx.RequestAudioSecondsFrom(reqCtx)
		}
		if seconds > 0 {
			usage.AudioSecondsMs10.Store(int64(seconds * 10))
		}
	case "embed":
		if len(body) == 0 {
			return
		}
		var partial struct {
			Data []json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(body, &partial); err == nil && len(partial.Data) > 0 {
			usage.EmbedsCount.Store(int64(len(partial.Data)))
		}
	default:
		// "chat" and anything else: no-op.
	}
}

// routeToBillingRoute maps a URL path to the billing route label used
// by billing_events.route.
func routeToBillingRoute(path string) string {
	switch {
	case strings.HasPrefix(path, "/v1/chat"):
		return "chat"
	case strings.HasPrefix(path, "/v1/embeddings"):
		return "embed"
	case strings.HasPrefix(path, "/v1/audio"):
		return "stt"
	default:
		return "chat"
	}
}

// sseUsageFrame is the minimal shape we need from an SSE data frame. Both
// OpenAI and llama.cpp emit usage as a top-level key; we ignore everything
// else (choices, id, created, etc.) so the tee is tolerant of shape drift.
// Model is optional — both shapes may emit it; when absent the accountant
// retains an empty model and the flusher logs a warn at cost-lookup time.
type sseUsageFrame struct {
	Model string `json:"model,omitempty"`
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
//
// The tee carries the request ctx + path so Close() can invoke
// ix.FinalizeRequest without needing a second channel back to the handler.
type usageTeeReader struct {
	upstream io.ReadCloser
	usage    *billing.RequestUsage
	reqCtx   context.Context
	reqPath  string
	reqID    string
	start    time.Time
	ix       *UsageInterceptor
	log      *slog.Logger
	buf      bytes.Buffer
	closed   bool
}

func (r *usageTeeReader) Read(p []byte) (int, error) {
	n, err := r.upstream.Read(p)
	if n > 0 {
		_, _ = r.buf.Write(p[:n])
		r.scanFrames()
	}
	return n, err
}

// Close flushes the buffer (last frame may not include the \n\n terminator),
// enqueues the billing event, and delegates to the upstream Close. Per
// proxy/interceptor.go godoc, wrappers MUST delegate Close.
//
// Determines "source": if the upstream Close returns an error OR no usage
// was captured, source="partial" (abnormal close, upstream cut, or the
// tenant disconnected before finish_reason=stop). Otherwise source="final".
func (r *usageTeeReader) Close() error {
	r.scanFrames()
	closeErr := r.upstream.Close()
	if r.closed {
		return closeErr
	}
	r.closed = true

	source := "final"
	if closeErr != nil {
		source = "partial"
	}
	if r.usage != nil {
		// No usage frame observed → abnormal close. The billing.Event
		// still gets written (source=partial) so reconcile sees the
		// request, but tokens_in/out may be 0.
		if r.usage.TokensIn.Load() == 0 && r.usage.TokensOut.Load() == 0 {
			source = "partial"
		}
	}
	if r.ix != nil && r.reqID != "" {
		r.ix.FinalizeRequest(r.reqCtx, r.reqID, r.reqPath, source)
	}
	return closeErr
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
			if f.Model != "" {
				r.usage.SetModel(f.Model)
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

// usageJSONBuffer tees a non-streaming JSON response body while forwarding
// bytes to the client. On Close the buffered body is parsed for the
// top-level "usage" key (OpenAI + OpenRouter + llama.cpp shape), the
// accountant slot is updated, and FinalizeRequest enqueues the
// billing.Event (HI-02 fix).
//
// Size-bounded at jsonBufferCap bytes so a malicious upstream cannot
// exhaust memory via a giant response. The cap exceeds any plausible
// chat/embed JSON response (128 KiB — same ceiling as the audit body
// capture) — larger bodies simply miss the usage capture and produce
// source=partial, which is fine.
const jsonBufferCap = 128 * 1024

type usageJSONBuffer struct {
	upstream io.ReadCloser
	usage    *billing.RequestUsage
	reqCtx   context.Context
	reqPath  string
	reqID    string
	start    time.Time
	ix       *UsageInterceptor
	log      *slog.Logger
	buf      bytes.Buffer
	capped   bool
	closed   bool
}

// Read forwards bytes to the client AND copies up to jsonBufferCap into
// the in-memory buffer for post-close parsing.
func (b *usageJSONBuffer) Read(p []byte) (int, error) {
	n, err := b.upstream.Read(p)
	if n > 0 && !b.capped {
		remaining := jsonBufferCap - b.buf.Len()
		if remaining > 0 {
			copyN := n
			if copyN > remaining {
				copyN = remaining
				b.capped = true
			}
			_, _ = b.buf.Write(p[:copyN])
		} else {
			b.capped = true
		}
	}
	return n, err
}

// Close parses the buffered body for "usage", updates the accountant,
// and calls FinalizeRequest. The slot is deleted by FinalizeRequest's
// deferred cleanup (BL-02).
func (b *usageJSONBuffer) Close() error {
	closeErr := b.upstream.Close()
	if b.closed {
		return closeErr
	}
	b.closed = true

	// Parse the buffered body for top-level "usage".
	var f sseUsageFrame
	if b.buf.Len() > 0 {
		if err := json.Unmarshal(b.buf.Bytes(), &f); err == nil {
			if f.Model != "" && b.usage != nil {
				b.usage.SetModel(f.Model)
			}
			if f.Usage != nil && b.usage != nil {
				b.usage.TokensIn.Store(int64(f.Usage.PromptTokens))
				b.usage.TokensOut.Store(int64(f.Usage.CompletionTokens))
				if b.log != nil {
					b.log.Debug("usage extracted from JSON",
						"prompt", f.Usage.PromptTokens,
						"completion", f.Usage.CompletionTokens)
				}
			}
		}
	}

	// Phase 16 (TEN-04 + OBS-09): produce the audio + embed usage atomics from
	// the same buffered body. Route-dispatched + ELSE-derived inside the pure
	// helper so the unit tests can exercise it Postgres-free. No-op for chat.
	applyAudioEmbedUsage(b.usage, routeToBillingRoute(b.reqPath), b.buf.Bytes(), b.reqCtx)

	source := "final"
	if closeErr != nil || b.capped {
		source = "partial"
	}
	if b.usage != nil && b.usage.TokensIn.Load() == 0 && b.usage.TokensOut.Load() == 0 {
		source = "partial"
	}
	if b.ix != nil && b.reqID != "" {
		b.ix.FinalizeRequest(b.reqCtx, b.reqID, b.reqPath, source)
	}
	return closeErr
}
