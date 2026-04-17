package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleLive_Always200(t *testing.T) {
	state := NewState()
	srv := httptest.NewServer(mux(state))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health/live")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("got %d want 200", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("body=%+v want status:ok", body)
	}
}

func TestHandleUpstream_Healthy_200(t *testing.T) {
	state := NewState()
	state.Set(UpstreamLLM, ProbeResult{Status: StatusHealthy, LatencyMs: 42, LastProbe: time.Now()})
	srv := httptest.NewServer(mux(state))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health/llm")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("got %d want 200", resp.StatusCode)
	}
	var body ProbeResult
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Status != StatusHealthy {
		t.Errorf("got status %q want healthy", body.Status)
	}
	if body.LatencyMs != 42 {
		t.Errorf("got latency %d want 42", body.LatencyMs)
	}
}

func TestHandleUpstream_Failed_503(t *testing.T) {
	state := NewState()
	state.Set(UpstreamLLM, ProbeResult{Status: StatusFailed, LatencyMs: 5000, Error: "timeout"})
	srv := httptest.NewServer(mux(state))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health/llm")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("got %d want 503", resp.StatusCode)
	}
}

func TestHandleAggregate_AllHealthy_200(t *testing.T) {
	state := NewState()
	state.Set(UpstreamLLM, ProbeResult{Status: StatusHealthy})
	state.Set(UpstreamSTT, ProbeResult{Status: StatusHealthy})
	state.Set(UpstreamEmbed, ProbeResult{Status: StatusHealthy})
	srv := httptest.NewServer(mux(state))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("got %d want 200", resp.StatusCode)
	}
	var body aggregateResponse
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Status != StatusHealthy {
		t.Errorf("aggregate status %q want healthy", body.Status)
	}
	if _, ok := body.Services[UpstreamLLM]; !ok {
		t.Errorf("aggregate missing llm service")
	}
}

func TestHandleAggregate_OneFailed_503(t *testing.T) {
	state := NewState()
	state.Set(UpstreamLLM, ProbeResult{Status: StatusHealthy})
	state.Set(UpstreamSTT, ProbeResult{Status: StatusFailed, Error: "boom"})
	state.Set(UpstreamEmbed, ProbeResult{Status: StatusHealthy})
	srv := httptest.NewServer(mux(state))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("got %d want 503", resp.StatusCode)
	}
}

func TestUnknownPath_404(t *testing.T) {
	state := NewState()
	srv := httptest.NewServer(mux(state))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/no-such-endpoint")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("got %d want 404", resp.StatusCode)
	}
}

func TestHealthReady_IsAggregate(t *testing.T) {
	state := NewState()
	state.Set(UpstreamLLM, ProbeResult{Status: StatusHealthy})
	state.Set(UpstreamSTT, ProbeResult{Status: StatusHealthy})
	state.Set(UpstreamEmbed, ProbeResult{Status: StatusHealthy})
	srv := httptest.NewServer(mux(state))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health/ready")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("got %d want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"services"`) {
		t.Errorf("body missing services field: %s", body)
	}
}
