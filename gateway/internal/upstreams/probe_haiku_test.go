// Package upstreams (probe_haiku_test.go): Phase 23-14 — probes sintéticos dos
// upstreams novos da cadeia Haiku (anthropic-haiku, openrouter-deepseek,
// openrouter-tts). Nenhum faz geração/síntese paga.
package upstreams

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
)

type probeCapture struct {
	method, path, query string
	header              http.Header
	calls               atomic.Int32
}

func newCaptureServer(t *testing.T, status int) (*httptest.Server, *probeCapture) {
	t.Helper()
	c := &probeCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.calls.Add(1)
		c.method = r.Method
		c.path = r.URL.Path
		c.query = r.URL.RawQuery
		c.header = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

func newProbeWithLog(name string, buf *bytes.Buffer, cfgs ...UpstreamConfig) *Probe {
	log := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	l := NewLoaderInMemory(cfgs...)
	bs := breaker.NewSet(nil, log, breaker.Options{}, []string{name})
	return NewProbe(l, bs, nil, ProbeConfig{}, log)
}

const probeSecret1 = "sk-ant-api03-PRIMEIRA-SECRETA"
const probeSecret2 = "sk-ant-api03-SEGUNDA-SECRETA"

func TestProbe_AnthropicHaiku_GetModelsWithXAPIKey(t *testing.T) {
	srv, c := newCaptureServer(t, http.StatusOK)
	var logs bytes.Buffer
	u := UpstreamConfig{
		Name: "anthropic-haiku", Role: "llm", Tier: 1, URL: srv.URL, Enabled: true,
		AuthBearer: "pedro=" + probeSecret1 + "@5,socio=" + probeSecret2,
	}
	p := newProbeWithLog(u.Name, &logs, u)
	resp, err := p.dispatch(context.Background(), u)
	if err != nil {
		t.Fatalf("dispatch(anthropic-haiku) erro em 200: %v", err)
	}
	if resp == nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("esperava 200, veio %+v", resp)
	}
	if c.method != http.MethodGet {
		t.Errorf("method = %q, want GET", c.method)
	}
	if c.path != "/v1/models" {
		t.Errorf("path = %q, want /v1/models", c.path)
	}
	if got := c.header.Get("x-api-key"); got != probeSecret1 {
		t.Errorf("x-api-key = %q, want a 1ª chave sem rótulo nem @dia", got)
	}
	if got := c.header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q", got)
	}
	if got := c.header.Get("Authorization"); got != "" {
		t.Errorf("Authorization não pode ir para a Anthropic (T-23-48), veio %q", got)
	}
	if strings.Contains(logs.String(), probeSecret1) || strings.Contains(logs.String(), probeSecret2) {
		t.Fatalf("log do probe contém a chave")
	}
}

func TestProbe_AnthropicHaiku_KeyWithoutDay(t *testing.T) {
	srv, c := newCaptureServer(t, http.StatusOK)
	u := UpstreamConfig{Name: "anthropic-haiku", Role: "llm", Tier: 1, URL: srv.URL, Enabled: true,
		AuthBearer: " pedro = " + probeSecret1 + " "}
	p := newTTSProbe(u.Name, u)
	if _, err := p.dispatch(context.Background(), u); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := c.header.Get("x-api-key"); got != probeSecret1 {
		t.Errorf("x-api-key = %q, want %q", got, probeSecret1)
	}
}

func TestProbe_AnthropicHaiku_NoKeyIsConfigWithoutRequest(t *testing.T) {
	for _, raw := range []string{"", "   ", "sem-igual", "=semrotulo", "rotulo="} {
		t.Run("raw="+raw, func(t *testing.T) {
			srv, c := newCaptureServer(t, http.StatusOK)
			u := UpstreamConfig{Name: "anthropic-haiku", Role: "llm", Tier: 1, URL: srv.URL, Enabled: true, AuthBearer: raw}
			loader := NewLoaderForTest(u)
			p, _ := newProbeFor(t, loader, u.Name)
			p.probeOne(context.Background(), u)
			if n := c.calls.Load(); n != 0 {
				t.Fatalf("probe enviou %d requests sem chave válida", n)
			}
			select {
			case ev := <-p.updates:
				if ev.LastProbeStatus.String != "config" {
					t.Errorf("status = %q, want config", ev.LastProbeStatus.String)
				}
				if raw != "" && strings.TrimSpace(raw) != "" && strings.Contains(ev.LastProbeError.String, raw) {
					t.Errorf("erro do probe ecoa o AuthBearer cru")
				}
			default:
				t.Fatalf("probeOne não enfileirou writeback")
			}
		})
	}
}

func TestProbe_OpenRouterDeepseek_SameAsChat(t *testing.T) {
	srv, c := newCaptureServer(t, http.StatusOK)
	u := UpstreamConfig{Name: "openrouter-deepseek", Role: "llm", Tier: 1, URL: srv.URL, Enabled: true, AuthBearer: "sk-or-x"}
	p := newTTSProbe(u.Name, u)
	if _, err := p.dispatch(context.Background(), u); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if c.method != http.MethodPost || c.path != "/v1/chat/completions" {
		t.Errorf("deepseek probe = %s %s, want POST /v1/chat/completions", c.method, c.path)
	}
	if got := c.header.Get("Authorization"); got != "Bearer sk-or-x" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestProbe_OpenRouterTTS_ListsSpeechModels(t *testing.T) {
	srv, c := newCaptureServer(t, http.StatusOK)
	u := UpstreamConfig{Name: "openrouter-tts", Role: "tts", Tier: 1, URL: srv.URL, Enabled: true, AuthBearer: "sk-or-tts"}
	p := newTTSProbe(u.Name, u)
	resp, err := p.dispatch(context.Background(), u)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	if c.method != http.MethodGet {
		t.Errorf("method = %q, want GET (sem síntese paga)", c.method)
	}
	if c.path != "/v1/models" || c.query != "output_modalities=speech" {
		t.Errorf("url = %s?%s, want /v1/models?output_modalities=speech", c.path, c.query)
	}
	if got := c.header.Get("Authorization"); got != "Bearer sk-or-tts" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestFirstAnthropicKeySecret(t *testing.T) {
	cases := map[string]string{
		"a=k1@5,b=k2":     "k1",
		"a=k1":            "k1",
		"a=k@x@12":        "k@x",
		"a=k@notnum":      "k@notnum",
		"":                "",
		"semigual":        "",
		"=k":              "",
		"a=":              "",
		"a=@5":            "",
		" a = k1 @3 , b=": "k1",
	}
	for in, want := range cases {
		if got := firstAnthropicKeySecret(in); got != want {
			t.Errorf("firstAnthropicKeySecret(%q) = %q, want %q", in, got, want)
		}
	}
}
