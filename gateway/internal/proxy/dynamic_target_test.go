package proxy

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/models"
)

// switchableTarget is a TargetFunc whose answer the test can swap atomically.
type switchableTarget struct {
	p atomic.Pointer[url.URL]
}

func (s *switchableTarget) set(t *testing.T, raw string) {
	t.Helper()
	if raw == "" {
		s.p.Store(nil)
		return
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	s.p.Store(u)
}

func (s *switchableTarget) fn() TargetFunc {
	return func() (*url.URL, bool) {
		u := s.p.Load()
		return u, u != nil
	}
}

// namedServer answers 200 with its own name in the body and counts hits.
func namedServer(t *testing.T, name string, hits *int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		atomic.AddInt64(hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"server":"` + name + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

type dynCase struct {
	name  string
	build func(TargetFunc) *httputil.ReverseProxy
	req   func(t *testing.T) *http.Request
}

func jsonReq(path, body string) func(t *testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		return r
	}
}

func audioReq(t *testing.T) *http.Request {
	body, ct := buildMultipartBody(t, []string{"whisper"}, "a.wav", []byte("RIFFAUDIO"))
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(body))
	r.Header.Set("Content-Type", ct)
	return r
}

func dynCases() []dynCase {
	resolver := models.NewResolverForTesting(sttLocalAliasFixture)
	return []dynCase{
		{"embed", func(f TargetFunc) *httputil.ReverseProxy {
			return NewDynamicEmbeddingsProxy(f, discardLogger())
		}, jsonReq("/v1/embeddings", `{"model":"bge-m3","input":"x"}`)},
		{"rerank", func(f TargetFunc) *httputil.ReverseProxy {
			return NewDynamicRerankProxy(f, discardLogger())
		}, jsonReq("/v1/rerank", `{"model":"bge-reranker-v2-m3","query":"q","documents":["a"]}`)},
		{"tts", func(f TargetFunc) *httputil.ReverseProxy {
			return NewDynamicTTSTargetProxy(f, discardLogger())
		}, jsonReq("/v1/audio/speech", `{"model":"tts-1","input":"oi","voice":"pm_alex"}`)},
		{"stt", func(f TargetFunc) *httputil.ReverseProxy {
			return NewDynamicAudioProxy(f, discardLogger(), resolver)
		}, audioReq},
	}
}

// Swapping the target makes the SAME proxy instance hit the new server on
// the next request — no rebuild.
func TestDynamicTarget_SwitchesTargetWithoutRebuild(t *testing.T) {
	for _, tc := range dynCases() {
		t.Run(tc.name, func(t *testing.T) {
			var aHits, bHits int64
			a := namedServer(t, "A", &aHits)
			b := namedServer(t, "B", &bHits)
			var tgt switchableTarget
			tgt.set(t, a.URL)
			rp := tc.build(tgt.fn())

			rw := httptest.NewRecorder()
			rp.ServeHTTP(rw, tc.req(t))
			if rw.Code != 200 || !strings.Contains(rw.Body.String(), `"A"`) {
				t.Fatalf("first request: code=%d body=%s", rw.Code, rw.Body.String())
			}

			tgt.set(t, b.URL)
			rw = httptest.NewRecorder()
			rp.ServeHTTP(rw, tc.req(t))
			if rw.Code != 200 || !strings.Contains(rw.Body.String(), `"B"`) {
				t.Fatalf("after swap: code=%d body=%s", rw.Code, rw.Body.String())
			}
			if atomic.LoadInt64(&aHits) != 1 || atomic.LoadInt64(&bHits) != 1 {
				t.Fatalf("hits A=%d B=%d, want 1/1", aHits, bHits)
			}
		})
	}
}

// No target → fallthrough sentinel, no write, dispatchResult flagged.
func TestDynamicTarget_UnresolvedFallsThrough(t *testing.T) {
	for _, tc := range dynCases() {
		t.Run(tc.name, func(t *testing.T) {
			var tgt switchableTarget // empty → (nil,false)
			rp := tc.build(tgt.fn())

			res := &dispatchResult{}
			r := tc.req(t)
			r = r.WithContext(withDispatchResult(r.Context(), res))
			rw := newRecordingRW()
			rp.ServeHTTP(rw, r)

			if rw.headerCalls() != 0 || rw.bodyCalls() != 0 {
				t.Fatalf("unresolved target must not write; WriteHeader=%d Write=%d", rw.headerCalls(), rw.bodyCalls())
			}
			if !res.fallthrough_ || res.wrote {
				t.Fatalf("dispatchResult = %+v, want fallthrough=true wrote=false", res)
			}
			if !errors.Is(res.err, errDialFailedFallthrough) {
				t.Fatalf("err = %v, want errDialFailedFallthrough", res.err)
			}
		})
	}
}

// countingRT records whether the base transport was reached at all.
type countingRT struct{ n int64 }

func (c *countingRT) RoundTrip(*http.Request) (*http.Response, error) {
	atomic.AddInt64(&c.n, 1)
	return nil, errors.New("base must not be called")
}

// The unresolved short-circuit happens before ANY base RoundTrip (no DNS, no dial).
func TestUnresolvedTargetRoundTripper_NoNetworkIO(t *testing.T) {
	base := &countingRT{}
	rt := unresolvedTargetRoundTripper{base: base}
	var tgt switchableTarget
	d := dynamicTargetDirector(tgt.fn(), BuildDirector)

	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader("{}"))
	r.Header.Set("Authorization", "Bearer client-secret")
	d(r)
	if r.URL.Host != unresolvedTargetHost || r.Host != unresolvedTargetHost {
		t.Fatalf("director must mark unresolved host, got URL.Host=%q Host=%q", r.URL.Host, r.Host)
	}
	if r.Header.Get("Authorization") != "" {
		t.Fatal("client auth must be stripped even when unresolved")
	}
	resp, err := rt.RoundTrip(r)
	if resp != nil || !errors.Is(err, errDialFailedFallthrough) {
		t.Fatalf("RoundTrip = (%v, %v), want (nil, errDialFailedFallthrough)", resp, err)
	}
	if atomic.LoadInt64(&base.n) != 0 {
		t.Fatalf("base transport called %d times, want 0", base.n)
	}
}

// Dynamic STT keeps the resolver model rewrite (whisper → pod model id) and
// the retryable-status interceptor (upstream 500 → cascade, nothing written).
func TestDynamicAudioProxy_RewriteAndRetryableCascade(t *testing.T) {
	var gotModel atomic.Value
	status := int64(200)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m, _, err := parseMultipartFromBytes(body, r.Header.Get("Content-Type"))
		if err == nil {
			gotModel.Store(m)
		}
		w.WriteHeader(int(atomic.LoadInt64(&status)))
		_, _ = w.Write([]byte(`{"text":"oi"}`))
	}))
	t.Cleanup(srv.Close)

	var tgt switchableTarget
	tgt.set(t, srv.URL)
	rp := NewDynamicAudioProxy(tgt.fn(), discardLogger(), models.NewResolverForTesting(sttLocalAliasFixture))

	rw := httptest.NewRecorder()
	rp.ServeHTTP(rw, audioReq(t))
	if rw.Code != 200 {
		t.Fatalf("code=%d body=%s", rw.Code, rw.Body.String())
	}
	if m, _ := gotModel.Load().(string); m != "Systran/faster-whisper-large-v3" {
		t.Fatalf("forwarded model = %q, want Systran/faster-whisper-large-v3", m)
	}

	atomic.StoreInt64(&status, 500)
	res := &dispatchResult{}
	r := audioReq(t)
	r = r.WithContext(withDispatchResult(r.Context(), res))
	rec := newRecordingRW()
	rp.ServeHTTP(rec, r)
	if rec.headerCalls() != 0 || rec.bodyCalls() != 0 {
		t.Fatalf("upstream 500 must not be written; WriteHeader=%d Write=%d", rec.headerCalls(), rec.bodyCalls())
	}
	if !res.fallthrough_ || !errors.Is(res.err, errUpstreamRetryable) {
		t.Fatalf("dispatchResult = %+v, want fallthrough with errUpstreamRetryable", res)
	}
}

// The string constructors keep rejecting invalid URLs.
func TestStaticConstructors_RejectInvalidURL(t *testing.T) {
	if _, err := NewEmbeddingsProxy("not a url", discardLogger()); err == nil {
		t.Error("NewEmbeddingsProxy must reject invalid url")
	}
	if _, err := NewAudioProxy("http://", discardLogger(), nil); err == nil {
		t.Error("NewAudioProxy must reject url without host")
	}
	if _, err := NewTTSProxy("://bad", discardLogger()); err == nil {
		t.Error("NewTTSProxy must reject invalid url")
	}
	if _, err := NewRerankProxy("", discardLogger()); err == nil {
		t.Error("NewRerankProxy must reject empty url")
	}
}
