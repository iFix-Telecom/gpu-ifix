// Package main contains the health-bridge service.
//
// The service runs inside the GPU pod on port 9100 and exposes aggregated
// and per-upstream health endpoints (D-10, D-12).
package main

import (
	"sync"
	"time"
)

// ProbeStatus is the health state of an upstream.
type ProbeStatus string

const (
	StatusUnknown  ProbeStatus = "unknown"
	StatusHealthy  ProbeStatus = "healthy"
	StatusDegraded ProbeStatus = "degraded"
	StatusFailed   ProbeStatus = "failed"
)

// Upstream names as used in /health/{upstream} paths and state map keys.
const (
	UpstreamLLM   = "llm"
	UpstreamSTT   = "stt"
	UpstreamEmbed = "embed"
)

// DegradationLatencyMs is the latency threshold above which a successful
// probe is marked degraded rather than healthy (per research STACK.md
// §Health-Checking the GPU Pod: p95 > 5s = degraded).
const DegradationLatencyMs = 5000

// ProbeResult is the outcome of a single probe execution.
type ProbeResult struct {
	Status    ProbeStatus `json:"status"`
	LatencyMs int64       `json:"latency_ms"`
	LastProbe time.Time   `json:"last_probe"`
	Error     string      `json:"error,omitempty"`
}

// ClassifyLatency downgrades a healthy result to degraded when latency
// breaches DegradationLatencyMs.
func ClassifyLatency(r ProbeResult) ProbeResult {
	if r.Status == StatusHealthy && r.LatencyMs >= DegradationLatencyMs {
		r.Status = StatusDegraded
	}
	return r
}

// State holds per-upstream probe results.
type State struct {
	mu        sync.RWMutex
	results   map[string]ProbeResult
	startedAt time.Time
}

// NewState returns an initialized State with all three upstreams seeded at
// StatusUnknown so /health responses include a deterministic shape even
// before the first probe tick.
func NewState() *State {
	return &State{
		results: map[string]ProbeResult{
			UpstreamLLM:   {Status: StatusUnknown},
			UpstreamSTT:   {Status: StatusUnknown},
			UpstreamEmbed: {Status: StatusUnknown},
		},
		startedAt: time.Now(),
	}
}

// Set atomically replaces the probe result for the given upstream.
func (s *State) Set(upstream string, r ProbeResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[upstream] = r
}

// Get returns the current probe result for the given upstream.
func (s *State) Get(upstream string) (ProbeResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.results[upstream]
	return r, ok
}

// Snapshot returns a deep copy of the current state.
//
// Callers are free to mutate the returned map without affecting internal
// state (see TestState_SnapshotIsolation).
func (s *State) Snapshot() map[string]ProbeResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]ProbeResult, len(s.results))
	for k, v := range s.results {
		out[k] = v
	}
	return out
}

// Uptime returns the time since NewState was called.
func (s *State) Uptime() time.Duration {
	return time.Since(s.startedAt)
}

// AggregateStatus returns the worst-of across all upstreams:
//
//	any failed     -> failed
//	else any degraded  -> degraded
//	else any unknown   -> degraded (startup grace still active)
//	else healthy
func (s *State) AggregateStatus() ProbeStatus {
	snap := s.Snapshot()
	worst := StatusHealthy
	for _, r := range snap {
		switch r.Status {
		case StatusFailed:
			return StatusFailed
		case StatusDegraded:
			if worst != StatusFailed {
				worst = StatusDegraded
			}
		case StatusUnknown:
			if worst == StatusHealthy {
				worst = StatusDegraded
			}
		}
	}
	return worst
}
