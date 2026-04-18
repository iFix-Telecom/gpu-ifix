// Package main_test exercises the chi router + /health + scaffold stubs
// + /metrics endpoint against an in-process httptest server. No external
// services are required; Sentry/DB/Redis wiring happens elsewhere.
package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/pkg/openai"
)

func newTestRouter(t *testing.T) http.Handler {
	t.Helper()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil)).With("module", "GATEWAY_TEST")
	return buildRouter(log, time.Now())
}

func TestHealth_200(t *testing.T) {
	srv := httptest.NewServer(newTestRouter(t))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("get /health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status want 200 got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type want application/json got %q", ct)
	}
	reqID := resp.Header.Get("X-Request-ID")
	if _, err := uuid.Parse(reqID); err != nil {
		t.Fatalf("X-Request-ID not a UUID: %q (%v)", reqID, err)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status want ok got %v", body["status"])
	}
	if body["version"] == nil {
		t.Errorf("missing version field: %+v", body)
	}
	if _, ok := body["uptime_s"]; !ok {
		t.Errorf("missing uptime_s field: %+v", body)
	}
}

func TestScaffold_ReturnsOpenAIEnvelope(t *testing.T) {
	srv := httptest.NewServer(newTestRouter(t))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status want 501 got %d", resp.StatusCode)
	}

	var env openai.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error.Code != "not_implemented" {
		t.Errorf("Error.Code want not_implemented got %q", env.Error.Code)
	}
	if env.Error.Type != "api_error" {
		t.Errorf("Error.Type want api_error got %q", env.Error.Type)
	}
}

func TestNotFound_ReturnsOpenAIEnvelope(t *testing.T) {
	srv := httptest.NewServer(newTestRouter(t))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/nonsense")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status want 404 got %d", resp.StatusCode)
	}
	var env openai.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error.Code != "not_found" {
		t.Errorf("Error.Code want not_found got %q", env.Error.Code)
	}
}

func TestMetrics_Exposed(t *testing.T) {
	// Prometheus CounterVec only emits HELP/TYPE lines after a label tuple is
	// observed at least once. Request instrumentation middleware lands in
	// Plan 02-04 (proxy layer); here we warm up the counter explicitly so
	// the scrape proves both the registration + the /metrics wiring.
	obs.RequestsTotal.WithLabelValues("/health", "200").Add(0)

	srv := httptest.NewServer(newTestRouter(t))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("get /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status want 200 got %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, "gateway_requests_total") {
		t.Errorf("missing gateway_requests_total in /metrics body:\n%s", s)
	}
	if !strings.Contains(s, "gateway_audit_dropped_total") {
		t.Errorf("missing gateway_audit_dropped_total in /metrics body:\n%s", s)
	}
}

func TestHealthEmbedsClientRequestID(t *testing.T) {
	srv := httptest.NewServer(newTestRouter(t))
	defer srv.Close()

	clientID := "018fb10c-1b36-7000-8000-000000000000"
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/health", nil)
	req.Header.Set("X-Request-ID", clientID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	// The gateway MUST reply with its own UUIDv7, not the client-supplied ID.
	gotID := resp.Header.Get("X-Request-ID")
	if gotID == clientID {
		t.Fatalf("gateway echoed client id verbatim; expected gateway-generated id")
	}
	if _, err := uuid.Parse(gotID); err != nil {
		t.Fatalf("gateway id not UUID: %q (%v)", gotID, err)
	}
}
