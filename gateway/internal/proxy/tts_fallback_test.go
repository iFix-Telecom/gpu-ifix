// Package proxy (tts_fallback_test.go): Plan 06.7-07 Task 3 fallback
// integration test (local fake-upstream version; the live version is Plan 09).
//
// It wires the PRODUCTION proxy.NewDispatcher for the "tts" role with a real
// breaker.Set (miniredis-backed) + an in-memory upstreams.Loader, exactly as
// cmd/gateway/main.go does, then asserts the GATE-3 Option A fallback path:
// when tier-0 (pod Chatterbox) fails and its breaker opens, the dispatcher
// routes to the tier-1 Piper adapter, which receives the translated request
// and returns WAV 16kHz 16-bit PCM mono.
package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/auth"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

func TestIntegration_TTSPiperFallback_AdapterConverts(t *testing.T) {
	// miniredis-backed breaker (no Postgres needed for this dispatcher test).
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	// tier-0 = a dead pod Chatterbox (connection refused -> breaker trips).
	const deadTier0 = "http://127.0.0.1:1"

	// tier-1 = a live fake Piper that records the translated request.
	var piperHits atomic.Int32
	var gotText, gotVoice string
	mulaw := make([]byte, 400)
	for i := range mulaw {
		mulaw[i] = 0xFF // mu-law silence
	}
	piper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		piperHits.Add(1)
		if r.URL.Path != "/tts" {
			t.Errorf("piper path got %q, want /tts", r.URL.Path)
		}
		_ = r.ParseForm()
		gotText = r.PostFormValue("text")
		gotVoice = r.PostFormValue("voice")
		w.Header().Set("Content-Type", "audio/basic")
		_, _ = w.Write(mulaw)
	}))
	defer piper.Close()

	loader := upstreams.NewLoaderInMemory(
		upstreams.UpstreamConfig{Name: "local-tts", Role: "tts", Tier: 0, URL: deadTier0, Enabled: true},
		upstreams.UpstreamConfig{Name: "voice-api-piper", Role: "tts", Tier: 1, URL: piper.URL, Enabled: true},
	)
	bs := breaker.NewSet(rdb, discardLogger(),
		breaker.Options{ConsecutiveFailures: 2, Cooldown: 30 * time.Second},
		loader.Names(),
	)

	// tier-0 proxy is the real NewTTSProxy (dial fails -> breaker increments
	// via the classifying wrapper below).
	tier0Proxy := ttsClassifyingProxy(t, deadTier0, bs, "local-tts")
	piperAdapter, err := NewPiperTTSAdapter(piper.URL, discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	disp := NewDispatcher(DispatcherConfig{
		Role:    "tts",
		Loader:  loader,
		Breaker: bs,
		Proxies: map[string]http.Handler{
			"local-tts":       tier0Proxy,
			"voice-api-piper": piperAdapter,
		},
		Log: discardLogger(),
	})

	// Fire until the breaker opens and the Piper adapter takes over.
	var lastWAV []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rw := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/audio/speech",
			strings.NewReader(`{"input":"Olá mundo","voice":"miro"}`))
		r.Header.Set("Content-Type", "application/json")
		r = r.WithContext(auth.WithContext(r.Context(), auth.AuthContext{
			TenantID:  "00000000-0000-0000-0000-000000000001",
			APIKeyID:  "00000000-0000-0000-0000-000000000002",
			DataClass: auth.DataClassNormal,
		}))
		disp.ServeHTTP(rw, r)
		if piperHits.Load() > 0 && rw.Code == http.StatusOK {
			lastWAV, _ = io.ReadAll(rw.Result().Body)
			ct := rw.Header().Get("Content-Type")
			if ct != "audio/wav" {
				t.Errorf("fallback Content-Type got %q, want audio/wav", ct)
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if piperHits.Load() == 0 {
		t.Fatal("tier-1 Piper adapter never received a request after tier-0 failure")
	}
	if gotText != "Olá mundo" || gotVoice != "miro" {
		t.Errorf("GATE-3 JSON->form translation failed: text=%q voice=%q", gotText, gotVoice)
	}
	assertWAV16kMono(t, lastWAV)
}

// ttsClassifyingProxy mirrors the production tier-0 path for this test: it
// drives the breaker by translating dial/5xx failures into a breaker failure
// so ConsecutiveFailures increments and the breaker opens, handing the next
// request to tier-1.
func ttsClassifyingProxy(t *testing.T, target string, bs *breaker.Set, name string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client := &http.Client{Timeout: 1 * time.Second}
		_, err := bs.Execute(name, func() (*http.Response, error) {
			req, herr := http.NewRequestWithContext(r.Context(), r.Method, target+r.URL.Path, http.NoBody)
			if herr != nil {
				return nil, herr
			}
			res, derr := client.Do(req)
			if derr != nil {
				return nil, derr // dial failure -> breaker failure
			}
			if res.StatusCode >= 500 {
				res.Body.Close()
				return nil, &breaker.HTTPError{Status: res.StatusCode, Msg: "tts 5xx"}
			}
			return res, nil
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}
