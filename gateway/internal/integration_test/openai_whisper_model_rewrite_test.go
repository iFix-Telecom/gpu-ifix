//go:build integration

// Phase 06.9 Plan 05a Task 2 — STT-OAI-FIX end-to-end integration test +
// 3 R6 Whisper edge cases.
//
// Phase 11.1 Plan 02 Task 2 amendment: with migration 0028 deleting the
// local-stt row, only (whisper, openai-whisper) → whisper-1 remains.
// The harness now registers a single role=stt upstream (openai-whisper)
// at tier 0 so the dispatcher resolves to it directly — no driveBreaker
// call needed, no tier-0 mock to seed.
//
// Whisper uses multipart/form-data; the director rewrites the "model"
// form-field value while preserving the file part's audio bytes
// byte-identical (Pitfall #6). The duplicate-model abort wired by Plan 03
// (WhisperAbortGuard) is the WARNING-3 closure — these tests prove the
// abort lands HTTP 400 BEFORE the proxy runs and that the request never
// reaches tier-1.
//
// Tests (4):
//  1. TestIntegration_OpenAIWhisperModelRewrite — base case: multipart
//     model="whisper" + WAV → tier-1 receives model="whisper-1" + audio
//     bytes byte-identical.
//  2. R6 Test A — TestIntegration_OpenAIWhisperModelRewrite_R6_MissingModelInjectsTarget
//     — multipart WITHOUT model field → tier-1 receives multipart with
//     synthetic model="whisper-1" injected; audio preserved.
//  3. R6 Test B — TestIntegration_OpenAIWhisperModelRewrite_R6_DuplicateModelRejects
//     — multipart with 2× model fields → WhisperAbortGuard returns HTTP 400
//     with JSON error envelope; tier-1 hits == 0 (never forwarded).
//     WARNING-3: this test MUST PASS (NOT skip) per Plan 03 wire-up.
//  4. R6 Test C — TestIntegration_OpenAIWhisperModelRewrite_R6_ResolverMissPassesThrough
//     — DB has no (whisper, openai-whisper) row → multipart with model="whisper"
//     forwarded with model="whisper" unchanged (pass-through alias).
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// buildWhisperMultipart returns a multipart/form-data body with the given
// model field values (one part per value — multiple values produce a
// duplicate-model fixture) + an "file" part containing fileBytes. Returns
// the body bytes + the Content-Type header (with the writer's boundary).
//
// If modelValues is empty, no "model" form field is written (R6 missing-model
// fixture). If fileName is "" the file part is also omitted (defensive — not
// used by current tests).
func buildWhisperMultipart(t *testing.T, modelValues []string, fileName string, fileBytes []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, mv := range modelValues {
		fw, err := w.CreateFormField("model")
		if err != nil {
			t.Fatalf("CreateFormField(model): %v", err)
		}
		if _, err := fw.Write([]byte(mv)); err != nil {
			t.Fatalf("write model: %v", err)
		}
	}
	if fileName != "" {
		hdr := make(textproto.MIMEHeader)
		hdr.Set("Content-Disposition", `form-data; name="file"; filename="`+fileName+`"`)
		hdr.Set("Content-Type", "audio/wav")
		fw, err := w.CreatePart(hdr)
		if err != nil {
			t.Fatalf("CreatePart(file): %v", err)
		}
		if _, err := fw.Write(fileBytes); err != nil {
			t.Fatalf("write file: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("multipart.Writer.Close: %v", err)
	}
	return buf.Bytes(), w.FormDataContentType()
}

// parseWhisperMultipart parses a forwarded multipart body and returns the
// model field value + file bytes for assertion. Returns "" + nil when a
// field is absent.
func parseWhisperMultipart(body []byte, contentType string) (modelField string, fileBytes []byte, err error) {
	mediaType, params, perr := mime.ParseMediaType(contentType)
	if perr != nil {
		return "", nil, perr
	}
	if !strings.HasPrefix(mediaType, "multipart/") {
		return "", nil, http.ErrNotMultipart
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			return "", nil, perr
		}
		buf, _ := io.ReadAll(part)
		switch part.FormName() {
		case "model":
			modelField = string(buf)
		case "file":
			fileBytes = buf
		}
	}
	return modelField, fileBytes, nil
}

// loadIntegrationProbeWAV reads gateway/internal/upstreams/testdata/probe.wav
// using repoRoot so the path resolves regardless of test run directory.
func loadIntegrationProbeWAV(t *testing.T) []byte {
	t.Helper()
	root := repoRoot(t)
	p := filepath.Join(root, "gateway", "internal", "upstreams", "testdata", "probe.wav")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read probe.wav (%s): %v", p, err)
	}
	if len(b) == 0 {
		t.Fatalf("probe.wav is empty: %s", p)
	}
	return b
}

// whisperHarness bundles the per-test setup: resolver, breaker set,
// tier-1 capturing mock, dispatcher. Centralised here so each R6 test
// stays focused on the behavior under test.
//
// Phase 11.1: tier0 mock dropped — migration 0028 removed the local-stt
// row, so the dispatcher only registers openai-whisper for role=stt.
type whisperHarness struct {
	tier1      *upstreamMock
	bs         *breaker.Set
	resolver   *models.Resolver
	dispatcher http.Handler
}

// newWhisperHarness builds the standard wiring used by all 4 Whisper tests.
//
// Phase 11.1: only the (whisper, openai-whisper) → whisper-1 alias exists
// post-migration 0028 (D-A5 preservation). With only one upstream registered
// for role=stt in the resilienceLoader, the dispatcher routes to tier-1
// directly — no driveBreaker call needed, no tier-0 mock to maintain.
//
//   - freshSchema → real resolver against migrated DB (or fixture-backed if
//     useFixtureResolver != nil, for the R6 Test C resolver-miss case where
//     the DB row is absent).
//   - tier1 = newSuccessMockCapturing (captures forwarded multipart body).
//   - production OpenAIWhisperDirector + httputil.ReverseProxy on tier1.
//   - WhisperAbortGuard wrapping tier1 proxy (WARNING-3 wired by Plan 03).
//   - Dispatcher with role=stt, proxies map keyed by upstream name.
func newWhisperHarness(t *testing.T, ctx context.Context, useFixtureResolver *models.Resolver) *whisperHarness {
	t.Helper()
	pool, rdb := freshSchema(t, ctx)

	// Resolver: production wiring uses NewResolver+Refresh against the live
	// DB. The R6 resolver-miss test passes a fixture-backed resolver instead
	// so the (whisper, openai-whisper) lookup misses without mutating the
	// shared DB schema.
	var resolver *models.Resolver
	if useFixtureResolver != nil {
		resolver = useFixtureResolver
	} else {
		resolver = models.NewResolver(pool, discardLogger())
		if err := resolver.Refresh(ctx); err != nil {
			t.Fatalf("resolver.Refresh: %v", err)
		}
	}

	tier1 := newSuccessMockCapturing(t)

	tier1URL, _ := url.Parse(tier1.server.URL)
	director := proxy.BuildOpenAIWhisperDirector(
		tier1URL,
		"sk-openai-test-bearer",
		resolver,
		"openai-whisper",
		discardLogger(),
	)
	tier1Proxy := &httputil.ReverseProxy{Director: director}
	// WARNING-3 wire-up — wrap the proxy in WhisperAbortGuard. This is the
	// production handler chain (see cmd/gateway/main.go's
	// sttRoleProxies["openai-whisper"] registration); without the wrapper,
	// the duplicate-model abort would not land HTTP 400 before forwarding.
	guardedTier1 := proxy.WhisperAbortGuard(tier1Proxy, resolver, "openai-whisper", discardLogger())

	// Phase 11.1: resilienceLoader passes "" for t1URL when caller wants a
	// single-upstream registry. Here we want exactly one stt upstream
	// (openai-whisper at tier 0 from the loader's perspective so Resolve(stt,0)
	// hits it without needing a breaker drive). Build the in-memory loader
	// directly so we can register openai-whisper as the sole role=stt entry.
	loader := upstreams.NewLoaderInMemory(upstreams.UpstreamConfig{
		Name:    "openai-whisper",
		Role:    "stt",
		Tier:    0,
		URL:     tier1.server.URL,
		Enabled: true,
	})
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 2, Cooldown: 30 * time.Second},
		loader.Names(),
	)

	disp := proxy.NewDispatcher(proxy.DispatcherConfig{
		Role:    "stt",
		Loader:  loader,
		Breaker: bs,
		Proxies: map[string]http.Handler{
			"openai-whisper": guardedTier1,
		},
		Log: slog.New(slog.NewTextHandler(discardWriter{}, nil)),
	})

	return &whisperHarness{
		tier1:      tier1,
		bs:         bs,
		resolver:   resolver,
		dispatcher: disp,
	}
}

// authedWhisperRequest constructs an authenticated multipart POST request
// targeted at /v1/audio/transcriptions.
func authedWhisperRequest(body []byte, contentType string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions",
		bytes.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	ctx := auth.WithContext(r.Context(), auth.AuthContext{
		TenantID:  "00000000-0000-0000-0000-000000000001",
		APIKeyID:  "00000000-0000-0000-0000-000000000002",
		DataClass: auth.DataClassNormal,
	})
	return r.WithContext(ctx)
}

// TestIntegration_OpenAIWhisperModelRewrite — base case (STT-OAI-FIX).
// Client POSTs multipart with model="whisper" + WAV; tier-1 receives the
// rewritten model="whisper-1" + audio bytes byte-identical.
func TestIntegration_OpenAIWhisperModelRewrite(t *testing.T) {
	t.Setenv("UPSTREAM_STT_OPENAI_MODEL", "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	h := newWhisperHarness(t, ctx, nil)
	wav := loadIntegrationProbeWAV(t)

	body, ct := buildWhisperMultipart(t, []string{"whisper"}, "probe.wav", wav)
	rw := httptest.NewRecorder()
	h.dispatcher.ServeHTTP(rw, authedWhisperRequest(body, ct))

	if got := h.tier1.hits.Load(); got < 1 {
		t.Fatalf("tier-1 hits = %d; want >= 1. dispatcher status=%d body=%s",
			got, rw.Code, rw.Body.String())
	}

	captured := h.tier1.LastBody()
	if len(captured) == 0 {
		t.Fatalf("tier-1 captured body is empty")
	}
	capturedCT := h.tier1.LastContentType()
	if !strings.HasPrefix(capturedCT, "multipart/form-data") {
		t.Fatalf("tier-1 Content-Type = %q, want multipart/form-data prefix", capturedCT)
	}
	gotModel, gotFile, perr := parseWhisperMultipart(captured, capturedCT)
	if perr != nil {
		t.Fatalf("captured body parse: %v", perr)
	}
	if gotModel != "whisper-1" {
		t.Errorf("STT-OAI-FIX REGRESSION: forwarded model = %q, want %q (schema-driven rewrite)",
			gotModel, "whisper-1")
	}
	if !bytes.Equal(gotFile, wav) {
		t.Errorf("audio bytes mutated: got %d bytes, want %d bytes (byte-identical required per Pitfall #6)",
			len(gotFile), len(wav))
	}
	t.Logf("STT-OAI-FIX VERIFIED: model rewritten to %q, %d audio bytes preserved",
		gotModel, len(gotFile))
}

// TestIntegration_OpenAIWhisperModelRewrite_R6_MissingModelInjectsTarget —
// R6 edge: client multipart has NO model field. Director MUST inject a
// fresh "model" form-field part with the canonical alias's resolved target.
func TestIntegration_OpenAIWhisperModelRewrite_R6_MissingModelInjectsTarget(t *testing.T) {
	t.Setenv("UPSTREAM_STT_OPENAI_MODEL", "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	h := newWhisperHarness(t, ctx, nil)
	wav := loadIntegrationProbeWAV(t)

	// Empty modelValues → builder does NOT write a model field.
	body, ct := buildWhisperMultipart(t, nil, "probe.wav", wav)
	rw := httptest.NewRecorder()
	h.dispatcher.ServeHTTP(rw, authedWhisperRequest(body, ct))

	if got := h.tier1.hits.Load(); got < 1 {
		t.Fatalf("tier-1 hits = %d; want >= 1 for missing-model injection path. status=%d body=%s",
			got, rw.Code, rw.Body.String())
	}

	captured := h.tier1.LastBody()
	gotModel, gotFile, perr := parseWhisperMultipart(captured, h.tier1.LastContentType())
	if perr != nil {
		t.Fatalf("captured body parse: %v", perr)
	}
	// canonicalAliasForUpstream["openai-whisper"] = "whisper" → resolver returns "whisper-1".
	if gotModel != "whisper-1" {
		t.Errorf("R6 missing-model: injected model = %q, want %q (canonical alias resolved target)",
			gotModel, "whisper-1")
	}
	if !bytes.Equal(gotFile, wav) {
		t.Errorf("R6 missing-model: audio bytes mutated; want byte-identical")
	}
	t.Logf("R6 Test A PASSED: missing-model field injected as %q via canonical alias", gotModel)
}

// TestIntegration_OpenAIWhisperModelRewrite_R6_DuplicateModelRejects —
// R6 edge + WARNING-3 closure: client multipart has 2× "model" fields.
// WhisperAbortGuard MUST return HTTP 400 + JSON error envelope BEFORE the
// proxy runs; tier-1 MUST NOT be hit.
//
// This test MUST PASS (never skipped) per Plan 03 WARNING-3 wire-up: the
// duplicate-model abort handler is wired in production via
// proxy.WhisperAbortGuard; this test sets up the same wrapper in the
// dispatcher and asserts the abort lands.
func TestIntegration_OpenAIWhisperModelRewrite_R6_DuplicateModelRejects(t *testing.T) {
	t.Setenv("UPSTREAM_STT_OPENAI_MODEL", "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	h := newWhisperHarness(t, ctx, nil)
	wav := loadIntegrationProbeWAV(t)

	// 2x model fields → duplicate fixture.
	body, ct := buildWhisperMultipart(t, []string{"whisper", "whisper-1"}, "probe.wav", wav)
	rw := httptest.NewRecorder()
	h.dispatcher.ServeHTTP(rw, authedWhisperRequest(body, ct))

	// PRIMARY assertion — HTTP 400 returned to client.
	if rw.Code != http.StatusBadRequest {
		t.Errorf("WARNING-3 REGRESSION: status = %d, want 400. body=%s", rw.Code, rw.Body.String())
	}

	// JSON error envelope.
	var env map[string]any
	if err := json.Unmarshal(rw.Body.Bytes(), &env); err != nil {
		t.Errorf("response body not valid JSON: %v; raw=%s", err, rw.Body.String())
	} else {
		errBlock, ok := env["error"].(map[string]any)
		if !ok {
			t.Errorf("response missing error block; body=%s", rw.Body.String())
		} else if errBlock["type"] != "invalid_request_error" {
			t.Errorf("error.type = %v, want invalid_request_error", errBlock["type"])
		}
	}

	// CRITICAL — tier-1 MUST NOT have been hit. The abort guard short-circuits
	// before the proxy.
	if got := h.tier1.hits.Load(); got != 0 {
		t.Errorf("WARNING-3 REGRESSION: tier-1 hits = %d, want 0 (abort guard should have blocked forwarding)",
			got)
	}

	t.Logf("R6 Test B (WARNING-3) PASSED: HTTP 400 returned, tier-1 hits=0 (abort blocked forwarding)")
}

// TestIntegration_OpenAIWhisperModelRewrite_R6_ResolverMissPassesThrough —
// R6 edge: the resolver does NOT have a row for (whisper, openai-whisper).
// Director MUST pass the alias through unchanged (resolver returns alias
// when no row found). The audio bytes preservation still holds.
//
// To set this up we use a fixture-backed resolver with an empty fixture map
// — Resolve falls through to passthrough.
func TestIntegration_OpenAIWhisperModelRewrite_R6_ResolverMissPassesThrough(t *testing.T) {
	t.Setenv("UPSTREAM_STT_OPENAI_MODEL", "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Empty fixture → Resolve returns the alias unchanged for any input.
	emptyResolver := models.NewResolverForTesting(map[[2]string]string{})

	h := newWhisperHarness(t, ctx, emptyResolver)
	wav := loadIntegrationProbeWAV(t)

	body, ct := buildWhisperMultipart(t, []string{"whisper"}, "probe.wav", wav)
	rw := httptest.NewRecorder()
	h.dispatcher.ServeHTTP(rw, authedWhisperRequest(body, ct))

	if got := h.tier1.hits.Load(); got < 1 {
		t.Fatalf("tier-1 hits = %d; want >= 1. status=%d body=%s",
			got, rw.Code, rw.Body.String())
	}

	captured := h.tier1.LastBody()
	gotModel, gotFile, perr := parseWhisperMultipart(captured, h.tier1.LastContentType())
	if perr != nil {
		t.Fatalf("captured body parse: %v", perr)
	}
	// Resolver miss → alias forwarded unchanged.
	if gotModel != "whisper" {
		t.Errorf("R6 resolver-miss: forwarded model = %q, want %q (pass-through unchanged)",
			gotModel, "whisper")
	}
	if !bytes.Equal(gotFile, wav) {
		t.Errorf("R6 resolver-miss: audio bytes mutated; want byte-identical")
	}
	t.Logf("R6 Test C PASSED: resolver miss → alias %q passed through unchanged", gotModel)
}

// Compile-time guards.
var (
	_ = proxy.BuildOpenAIWhisperDirector
	_ = proxy.WhisperAbortGuard
)
