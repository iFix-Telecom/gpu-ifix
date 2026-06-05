package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/ifixtelecom/gpu-ifix/pkg/openai"
)

// probeTimeout is the per-probe deadline. 5s covers warm llama inference
// for a trivial 1-token request; larger budgets invite false positives.
const probeTimeout = 5 * time.Second

// newHTTPClient returns a client tuned for long-lived probe loops.
// MaxIdleConns/IdleConnTimeout per PITFALLS §Pitfall 12.
func newHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:          10,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
		},
	}
}

// probeLLM sends a trivial POST /v1/chat/completions and reports latency + status.
// Success: 200 + valid ChatCompletionResponse with at least one Choice.
func probeLLM(ctx context.Context, client *http.Client, base string, log *slog.Logger) ProbeResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	body := openai.ChatCompletionRequest{
		Model:     "qwen",
		Messages:  []openai.ChatCompletionMessage{{Role: "user", Content: "ping"}},
		MaxTokens: 1,
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return failed(start, fmt.Errorf("marshal: %w", err))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return failed(start, fmt.Errorf("new request: %w", err))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return failed(start, fmt.Errorf("do: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return failed(start, fmt.Errorf("status %d: %s", resp.StatusCode, string(raw)))
	}
	var out openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return failed(start, fmt.Errorf("decode: %w", err))
	}
	if len(out.Choices) == 0 {
		return failed(start, fmt.Errorf("no choices returned"))
	}
	log.Debug("probe llm ok", "latency_ms", time.Since(start).Milliseconds())
	return ClassifyLatency(success(start))
}

// probeEmbed sends a single-token embedding request.
func probeEmbed(ctx context.Context, client *http.Client, base string, log *slog.Logger) ProbeResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	body := openai.EmbeddingRequest{
		Model: "BAAI/bge-m3",
		Input: []string{"ping"},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return failed(start, fmt.Errorf("marshal: %w", err))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/embeddings", bytes.NewReader(buf))
	if err != nil {
		return failed(start, fmt.Errorf("new request: %w", err))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return failed(start, fmt.Errorf("do: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return failed(start, fmt.Errorf("status %d: %s", resp.StatusCode, string(raw)))
	}
	var out openai.EmbeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return failed(start, fmt.Errorf("decode: %w", err))
	}
	if len(out.Data) == 0 || len(out.Data[0].Embedding) == 0 {
		return failed(start, fmt.Errorf("no embeddings returned"))
	}
	log.Debug("probe embed ok", "latency_ms", time.Since(start).Milliseconds())
	return ClassifyLatency(success(start))
}

func success(start time.Time) ProbeResult {
	return ProbeResult{
		Status:    StatusHealthy,
		LatencyMs: time.Since(start).Milliseconds(),
		LastProbe: time.Now(),
	}
}

func failed(start time.Time, err error) ProbeResult {
	return ProbeResult{
		Status:    StatusFailed,
		LatencyMs: time.Since(start).Milliseconds(),
		LastProbe: time.Now(),
		Error:     err.Error(),
	}
}

// ProbeLoop runs probe(ctx, client, base) every interval and stores results
// in state[upstream]. Exits when ctx is canceled.
func ProbeLoop(
	ctx context.Context,
	log *slog.Logger,
	state *State,
	upstream string,
	probe func(context.Context, *http.Client, string, *slog.Logger) ProbeResult,
	client *http.Client,
	base string,
	interval time.Duration,
) {
	log = log.With("upstream", upstream, "base", base)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// Run one probe immediately so state is not "unknown" for a full interval.
	state.Set(upstream, probe(ctx, client, base, log))
	for {
		select {
		case <-ctx.Done():
			log.Info("probe loop exiting")
			return
		case <-ticker.C:
			state.Set(upstream, probe(ctx, client, base, log))
		}
	}
}
