// Package upstreams (probe_test.go): Phase 06.7 Wave 0 RED scaffolding
// (Nyquist gate). Skip stub binding the `tts` probe behavior to its owning
// implementation plan. These assertions are ENGINE-AGNOSTIC: they cover the
// `tts` ROLE plumbing inside Probe.dispatch (probe path + success/failure
// classification) regardless of whether the TTS server on :8003 is
// Chatterbox Multilingual (the Wave 0 GATE 1 engine swap from Kani) or any
// other OpenAI-compatible /v1/audio/speech server.
//
// OWNER map (authority: 06.7-02-PLAN.md <stub_ownership_map>):
//   - TestProbe_TTS_PostsAudioSpeech -> Plan 06.7-03
package upstreams

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
)

// newProbeRedis spins a miniredis-backed go-redis client for probe tests
// that drive the breaker via Execute (which lazily reads the force-override
// key from Redis). Cleanup closes both.
func newProbeRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// newTTSProbe builds a Probe wired to an in-memory loader + a breaker for the
// given upstream name. Only Probe.dispatch (which uses p.client) is exercised
// by the TTS probe test, so q==nil (no Postgres writeback).
func newTTSProbe(name string, cfgs ...UpstreamConfig) *Probe {
	l := NewLoaderInMemory(cfgs...)
	bs := breaker.NewSet(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), breaker.Options{}, []string{name})
	return NewProbe(l, bs, nil, ProbeConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestProbe_TTS_PostsAudioSpeech asserts that Probe.dispatch, when given an
// UpstreamConfig with Role=="tts", POSTs to <URL>/v1/audio/speech with a
// synthetic JSON speech body, treats a 200 response carrying audio bytes as
// breaker SUCCESS, and treats a 5xx as a *breaker.HTTPError failure (mirror
// the existing "embed"/"llm" case assertions in probe.go dispatch switch).
//
// OWNER: Plan 06.7-03 — unskipped + asserting real path + status handling.
func TestProbe_TTS_PostsAudioSpeech(t *testing.T) {
	// --- 200 + audio bytes -> success (no error) ---
	t.Run("200_success", func(t *testing.T) {
		var gotPath, gotMethod, gotCT string
		var gotBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotMethod = r.Method
			gotCT = r.Header.Get("Content-Type")
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &gotBody)
			w.Header().Set("Content-Type", "audio/pcm")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte{0x00, 0x01, 0x02, 0x03}) // synthetic audio bytes
		}))
		defer srv.Close()

		u := UpstreamConfig{Name: "primary-tts", Role: "tts", Tier: 0, URL: srv.URL, Enabled: true}
		p := newTTSProbe(u.Name, u)

		resp, err := p.dispatch(context.Background(), u)
		if err != nil {
			t.Fatalf("dispatch(tts) returned error on 200: %v", err)
		}
		if resp == nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 response, got %+v", resp)
		}
		if gotMethod != http.MethodPost {
			t.Errorf("method = %q, want POST", gotMethod)
		}
		if gotPath != "/v1/audio/speech" {
			t.Errorf("path = %q, want /v1/audio/speech", gotPath)
		}
		if gotCT != "application/json" {
			t.Errorf("content-type = %q, want application/json", gotCT)
		}
		if gotBody["input"] != "ping" {
			t.Errorf("body.input = %v, want ping", gotBody["input"])
		}
		if gotBody["response_format"] != "pcm" {
			t.Errorf("body.response_format = %v, want pcm", gotBody["response_format"])
		}
	})

	// --- 5xx -> *breaker.HTTPError failure ---
	t.Run("5xx_failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer srv.Close()

		u := UpstreamConfig{Name: "primary-tts", Role: "tts", Tier: 0, URL: srv.URL, Enabled: true}
		p := newTTSProbe(u.Name, u)

		_, err := p.dispatch(context.Background(), u)
		if err == nil {
			t.Fatalf("dispatch(tts) returned nil error on 502, want *breaker.HTTPError")
		}
		var he *breaker.HTTPError
		if !errors.As(err, &he) {
			t.Fatalf("error type = %T, want *breaker.HTTPError", err)
		}
		if he.Status != http.StatusBadGateway {
			t.Errorf("HTTPError.Status = %d, want 502", he.Status)
		}
	})
}

// newProbeFor builds a Probe wired to the loader + a breaker over the
// supplied names. q==nil so no Postgres writeback is exercised.
func newProbeFor(t *testing.T, loader *Loader, names ...string) (*Probe, *breaker.Set) {
	t.Helper()
	rdb := newProbeRedis(t)
	bs := breaker.NewSet(rdb, slog.New(slog.NewTextHandler(io.Discard, nil)), breaker.Options{ConsecutiveFailures: 3, Cooldown: time.Hour}, names)
	p := NewProbe(loader, bs, nil, ProbeConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return p, bs
}

// TestProbe_HonorsTier0Override: with a tier0Override active for "llm",
// doTick probes the emergency_pod_llm URL (the Resolve(role,0) result), NOT
// the static local-llm URL, and the static row's breaker is never driven.
func TestProbe_HonorsTier0Override(t *testing.T) {
	var staticHit, podHit atomic.Int32
	staticSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		staticHit.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer staticSrv.Close()
	podSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		podHit.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer podSrv.Close()

	loader := NewLoaderForTest(
		UpstreamConfig{Name: "local-llm", Role: "llm", Tier: 0, URL: staticSrv.URL, Enabled: true},
	)
	loader.OverrideTier0("llm", podSrv.URL)

	// Breaker name set must include the emergency synthetic name so Execute
	// drives a real breaker for the resolved tier-0.
	p, bs := newProbeFor(t, loader, "local-llm", "emergency_pod_llm")
	p.doTick(context.Background())

	if podHit.Load() == 0 {
		t.Errorf("emergency pod URL was NOT probed under active override; want ≥1 hit")
	}
	if staticHit.Load() != 0 {
		t.Errorf("static local-llm URL was probed %d times under override; want 0", staticHit.Load())
	}
	// The static row's breaker must not have been touched (no recorded state
	// transition driven through Execute for it).
	snap := bs.Snapshot()
	if snap["local-llm"] != "closed" {
		t.Errorf("static local-llm breaker = %q after override tick; want untouched closed", snap["local-llm"])
	}
}

// TestProbe_TierGatingPreserved: when the resolved tier-0 breaker is CLOSED,
// tier-1 external probes are gated OFF (D-15). When the resolved tier-0 is
// NOT closed, tier-1 is probed.
func TestProbe_TierGatingPreserved(t *testing.T) {
	var t1Hit atomic.Int32
	t0Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer t0Srv.Close()
	t1Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t1Hit.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer t1Srv.Close()

	loader := NewLoaderForTest(
		UpstreamConfig{Name: "local-llm", Role: "llm", Tier: 0, URL: t0Srv.URL, Enabled: true},
		UpstreamConfig{Name: "openrouter-chat", Role: "llm", Tier: 1, URL: t1Srv.URL, Enabled: true},
	)

	// tier-0 CLOSED → tier-1 must be skipped.
	p, _ := newProbeFor(t, loader, "local-llm", "openrouter-chat")
	p.doTick(context.Background())
	if t1Hit.Load() != 0 {
		t.Errorf("tier-1 probed %d times while tier-0 CLOSED; want 0 (D-15 gating)", t1Hit.Load())
	}

	// Drive tier-0 OPEN, then tier-1 must be probed.
	bs2 := breaker.NewSet(newProbeRedis(t), slog.New(slog.NewTextHandler(io.Discard, nil)), breaker.Options{ConsecutiveFailures: 1, Cooldown: time.Hour}, []string{"local-llm", "openrouter-chat"})
	for i := 0; i < 2; i++ {
		_, _ = bs2.Execute("local-llm", func() (*http.Response, error) {
			return nil, &breaker.HTTPError{Status: 503, Msg: "trip"}
		})
	}
	time.Sleep(20 * time.Millisecond)
	p2 := NewProbe(loader, bs2, nil, ProbeConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t1Hit.Store(0)
	p2.doTick(context.Background())
	if t1Hit.Load() == 0 {
		t.Errorf("tier-1 NOT probed while tier-0 OPEN; want ≥1 (D-15 gating)")
	}
}
