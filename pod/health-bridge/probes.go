package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"time"

	"github.com/ifixtelecom/gpu-ifix/pkg/openai"
)

// probeTimeout is the per-probe deadline. 5s covers warm llama inference
// for a trivial 1-token request; larger budgets invite false positives.
const probeTimeout = 5 * time.Second

// sttProbeTimeout is the larger timeout for STT (audio decode + whisper
// forward pass).
const sttProbeTimeout = 10 * time.Second

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

// probeSTT posts a 1-second silent WAV via multipart form to
// /v1/audio/transcriptions. The WAV is generated in-memory so no fixture
// file has to ship with the binary.
func probeSTT(ctx context.Context, client *http.Client, base string, log *slog.Logger) ProbeResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, sttProbeTimeout)
	defer cancel()

	wav := generateSilentWAV(1) // 1 second of 16kHz PCM silence

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "probe.wav")
	if err != nil {
		return failed(start, fmt.Errorf("form file: %w", err))
	}
	if _, err := fw.Write(wav); err != nil {
		return failed(start, fmt.Errorf("write wav: %w", err))
	}
	// STT model name is configurable — Speaches deployments that use a
	// different Whisper checkpoint would otherwise false-positive the probe.
	sttModel := os.Getenv("SPEACHES_MODEL")
	if sttModel == "" {
		sttModel = "Systran/faster-whisper-large-v3"
	}
	if err := mw.WriteField("model", sttModel); err != nil {
		return failed(start, fmt.Errorf("write field: %w", err))
	}
	if err := mw.Close(); err != nil {
		return failed(start, fmt.Errorf("mw close: %w", err))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/audio/transcriptions", &buf)
	if err != nil {
		return failed(start, fmt.Errorf("new request: %w", err))
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := client.Do(req)
	if err != nil {
		return failed(start, fmt.Errorf("do: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return failed(start, fmt.Errorf("status %d: %s", resp.StatusCode, string(raw)))
	}
	var out openai.TranscriptionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return failed(start, fmt.Errorf("decode: %w", err))
	}
	log.Debug("probe stt ok", "latency_ms", time.Since(start).Milliseconds(), "text_len", len(out.Text))
	return ClassifyLatency(success(start))
}

// generateSilentWAV returns a minimal RIFF WAV with the given seconds of
// 16 kHz mono PCM silence. First four bytes spell "RIFF" (validated by
// acceptance criteria).
func generateSilentWAV(seconds int) []byte {
	const sampleRate = 16000
	const bitsPerSample = 16
	numSamples := sampleRate * seconds
	dataSize := numSamples * (bitsPerSample / 8)

	// RIFF header (44 bytes) + data
	buf := make([]byte, 44+dataSize)
	copy(buf[0:4], "RIFF")
	// Chunk size = 36 + dataSize
	writeU32LE(buf[4:8], uint32(36+dataSize))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	writeU32LE(buf[16:20], 16)                    // PCM fmt chunk size
	writeU16LE(buf[20:22], 1)                     // PCM format
	writeU16LE(buf[22:24], 1)                     // mono
	writeU32LE(buf[24:28], sampleRate)            // sample rate
	writeU32LE(buf[28:32], sampleRate*2)          // byte rate (sr * channels * bits/8)
	writeU16LE(buf[32:34], 2)                     // block align
	writeU16LE(buf[34:36], uint16(bitsPerSample)) // bits per sample
	copy(buf[36:40], "data")
	writeU32LE(buf[40:44], uint32(dataSize))
	// Samples remain zero-initialized = silence
	return buf
}

func writeU16LE(b []byte, v uint16) { b[0] = byte(v); b[1] = byte(v >> 8) }
func writeU32LE(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
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
