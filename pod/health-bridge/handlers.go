package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// writeJSON writes a JSON body and status code.
//
// A write error is ignored because it only happens on client disconnect
// mid-write (browser closed tab, gateway timed out) — the payload is a
// bounded state snapshot and there is nothing useful to log beyond what
// net/http already emits when its connection goes away.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// statusCodeFor maps our ProbeStatus to HTTP status codes.
//
// Kubernetes convention: any non-healthy upstream yields 503 so readiness
// probes drain the pod from the load balancer.
func statusCodeFor(s ProbeStatus) int {
	switch s {
	case StatusHealthy:
		return http.StatusOK
	case StatusDegraded, StatusFailed, StatusUnknown:
		return http.StatusServiceUnavailable
	default:
		return http.StatusServiceUnavailable
	}
}

// handleLive is the always-200 liveness probe (D-12 /health/live).
//
// Docker-compose healthcheck can use this without cascading on upstream
// flakiness — the process being up is sufficient.
func handleLive(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleUpstream returns the per-upstream ProbeResult (D-12
// /health/{llm,embed}). STT route removed in Phase 11.1 (SEED-010 Mudança 7).
func handleUpstream(state *State, upstream string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		r, ok := state.Get(upstream)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown upstream"})
			return
		}
		writeJSON(w, statusCodeFor(r.Status), r)
	}
}

// aggregateResponse is the /health and /health/ready body.
type aggregateResponse struct {
	Status    ProbeStatus            `json:"status"`
	Services  map[string]ProbeResult `json:"services"`
	UptimeS   int64                  `json:"uptime_s"`
	Timestamp time.Time              `json:"timestamp"`
}

// handleAggregate returns the aggregate health of all upstreams.
func handleAggregate(state *State) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		snap := state.Snapshot()
		agg := state.AggregateStatus()
		resp := aggregateResponse{
			Status:    agg,
			Services:  snap,
			UptimeS:   int64(state.Uptime().Seconds()),
			Timestamp: time.Now(),
		}
		writeJSON(w, statusCodeFor(agg), resp)
	}
}

// mux builds the HTTP router with the surviving D-12 endpoints.
//
// Route precedence note: net/http ServeMux uses longest-prefix matching.
// "/health/live", "/health/ready", "/health/llm", and "/health/embed" are
// registered as exact patterns. "/health" is then registered with an
// explicit path check so that e.g. "/health/nonsense" returns 404 rather
// than the aggregate response. "/health/stt" was removed in Phase 11.1
// (SEED-010 Mudança 7) along with the :8001 Speaches probe.
func mux(state *State) http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/health/live", handleLive)
	m.HandleFunc("/health/ready", handleAggregate(state))
	m.HandleFunc("/health/llm", handleUpstream(state, UpstreamLLM))
	m.HandleFunc("/health/embed", handleUpstream(state, UpstreamEmbed))
	m.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimRight(r.URL.Path, "/") != "/health" {
			http.NotFound(w, r)
			return
		}
		handleAggregate(state)(w, r)
	})
	return m
}
