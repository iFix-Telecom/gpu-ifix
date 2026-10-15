package admin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy"
)

// Chaves fictícias (nenhuma chave real existe nos testes).
const (
	anthKeyA = "sk-test-admin-a-0123456789abcdef"
	anthKeyB = "sk-test-admin-b-fedcba9876543210"
)

const anthCreditLow = `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."}}`

// newExhaustedPool devolve um pool com 2 chaves em que a 1ª foi marcada
// esgotada por uma resposta real de "credit balance is too low" vinda de um
// fake da Anthropic (passando pelo transporte do pool, API pública).
func newExhaustedPool(t *testing.T) *proxy.KeyPool {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close(); mr.Close() })

	keys, err := proxy.ParseAnthropicKeys("contaA=" + anthKeyA + "@15,contaB=" + anthKeyB)
	if err != nil {
		t.Fatalf("ParseAnthropicKeys: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := proxy.NewKeyPool(keys, rdb, nil, nil, log)

	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("x-api-key") == anthKeyA {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, anthCreditLow)
			return
		}
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","content":[]}`)
	}))
	t.Cleanup(fake.Close)
	rt := proxy.NewAnthropicKeyPoolTransport(http.DefaultTransport, pool, log)
	req, _ := http.NewRequest(http.MethodPost, fake.URL+"/v1/messages", strings.NewReader(`{}`))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	return pool
}

type anthKeysResp struct {
	Keys []struct {
		Label       string  `json:"label"`
		Fingerprint string  `json:"fingerprint"`
		State       string  `json:"state"`
		Until       *string `json:"until"`
		NextProbeAt *string `json:"next_probe_at"`
		Reason      string  `json:"reason"`
	} `json:"keys"`
	Configured *bool `json:"configured"`
}

func TestAnthropicKeysHandler_ListsPoolState(t *testing.T) {
	pool := newExhaustedPool(t)
	h := NewAnthropicKeysHandler(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/anthropic/keys", nil))

	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q", ct)
	}
	var out anthKeysResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if len(out.Keys) != 2 {
		t.Fatalf("keys = %d, want 2", len(out.Keys))
	}
	a, b := out.Keys[0], out.Keys[1]
	if a.Label != "contaA" || b.Label != "contaB" {
		t.Fatalf("rótulos/ordem = %q,%q", a.Label, b.Label)
	}
	if a.State != "exhausted" || b.State != "ok" {
		t.Fatalf("estados = %q,%q, want exhausted,ok", a.State, b.State)
	}
	if len(a.Fingerprint) != 8 || len(b.Fingerprint) != 8 || a.Fingerprint == b.Fingerprint {
		t.Fatalf("fingerprints inválidos: %q %q", a.Fingerprint, b.Fingerprint)
	}
	if a.Until == nil {
		t.Fatal("until ausente na chave esgotada")
	}
	if _, err := time.Parse(time.RFC3339, *a.Until); err != nil {
		t.Fatalf("until não é RFC3339: %q", *a.Until)
	}
	if a.Reason == "" {
		t.Fatal("reason ausente na chave esgotada")
	}
	if out.Configured == nil || !*out.Configured {
		t.Fatal("configured deveria ser true com pool")
	}
}

func TestAnthropicKeysHandler_NeverLeaksKey(t *testing.T) {
	pool := newExhaustedPool(t)
	h := NewAnthropicKeysHandler(pool, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/anthropic/keys", nil))
	body := rec.Body.String()
	for _, k := range []string{anthKeyA, anthKeyB, "sk-test-admin"} {
		if strings.Contains(body, k) {
			t.Fatalf("corpo da resposta contém segredo %q: %s", k, body)
		}
	}
}

func TestAnthropicKeysHandler_NilPool(t *testing.T) {
	h := NewAnthropicKeysHandler(nil, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/anthropic/keys", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"keys":[],"configured":false}` {
		t.Fatalf("body = %s", got)
	}
}

func TestAnthropicKeysHandler_MethodNotAllowed(t *testing.T) {
	h := NewAnthropicKeysHandler(nil, nil)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, "/admin/anthropic/keys", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: status = %d, want 405", m, rec.Code)
		}
		if allow := rec.Header().Get("Allow"); allow != http.MethodGet {
			t.Fatalf("%s: Allow = %q", m, allow)
		}
	}
}
