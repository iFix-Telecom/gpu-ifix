// Package proxy (tokencount.go): pre-dispatch token-count enforcement for
// the chat (32k) and embed (8k BGE-M3) caps per CONTEXT.md RES-07 / SC-5.
//
// TokenCounter queries llama.cpp's built-in /tokenize endpoint to obtain
// the authoritative token count for the resolved model, with a 60-second
// Redis cache keyed on sha256(body) PLUS the model name (Pitfall 6 — two
// models with different tokenizers can encode the same body to different
// counts; sharing a cache slot would silently approve over-cap requests).
//
// Fail-open policy: any error talking to /tokenize or the cache returns
// (0, nil) so the request proceeds to the dispatcher. The breaker on the
// local-llm upstream catches actual outage; we never block legitimate
// requests because the tokenizer endpoint hiccupped.
package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// TokenCounter queries llama.cpp /tokenize with a Redis cache to enforce
// the 32k context window cap (RES-07 / SC-5). Cache key includes the
// resolved model name to prevent cross-tokenizer collisions (Pitfall 6).
type TokenCounter struct {
	rdb    *redis.Client
	llmURL string
	client *http.Client
	log    *slog.Logger
}

const (
	// tokenCacheTTL is the Redis cache TTL for /tokenize results. 60s
	// matches the auth cache TTL and is short enough that a freshly
	// edited prompt template propagates within the cache window.
	tokenCacheTTL = 60 * time.Second

	// ChatContextCap is the input-token ceiling for /v1/chat/completions
	// per CONTEXT.md "Enforcement do cap de contexto" (RES-07). Equals the
	// PER-SLOT context of llama-server: total --ctx-size 65536 split
	// across -np 2 slots = 32768 tokens/slot. The 65536 total is only
	// affordable on the 24 GB 3090 because the KV cache is quantized
	// (--cache-type-k/v q8_0 + the mandatory -fa on); see
	// pod/primary/supervisord.conf, which is the actual runtime.
	//
	// The cap MUST track the PER-SLOT value, never the TOTAL --ctx-size:
	// when it was calibrated against the total (16384 total / 2 slots =
	// 8192/slot), an 11184-token request passed the gateway guard and
	// 400'd at the slot with exceed_context_size_error (OPERACOES-26306,
	// chamada_id 12294412, 2026-08-21).
	ChatContextCap = 32768

	// EmbedContextCap is the input-token ceiling for /v1/embeddings.
	// BGE-M3 native max sequence length is 8192; longer inputs would
	// silently truncate and the caller would not know. Reject pre-flight.
	EmbedContextCap = 8192

	// tokenizeTimeout bounds the /tokenize HTTP call. Conservative: 1s
	// ensures the dispatcher path stays responsive even when llama-server
	// is briefly busy. Failures here fail-open (caller proceeds without
	// enforcement; breaker handles real outage).
	tokenizeTimeout = 1 * time.Second
)

// NewTokenCounter constructs a TokenCounter. llmURL is the PRIMARY
// local-llm base URL (typically resolved from upstreams loader as the
// tier-0 llm row) — /tokenize is appended on Enforce.
func NewTokenCounter(rdb *redis.Client, llmURL string, log *slog.Logger) *TokenCounter {
	return &TokenCounter{
		rdb:    rdb,
		llmURL: llmURL,
		client: &http.Client{Timeout: tokenizeTimeout},
		log:    log.With("module", "TOKENIZE"),
	}
}

// tokenCacheKey returns the namespaced Redis key. Includes both model and
// body hash so two different tokenizers cannot poison each other's slot.
func tokenCacheKey(model, bodyHash string) string {
	return "gw:tokenize:" + model + ":" + bodyHash
}

// Enforce extracts tokenizable text from body, counts tokens via /tokenize
// (Redis-cached), and returns ErrContextLengthExceeded if count > cap.
//
// Returns (count, nil) on success, (count, ErrContextLengthExceeded) if
// over cap, or (0, nil) fail-open on any /tokenize or Redis transport
// error so the dispatcher can proceed.
func (t *TokenCounter) Enforce(ctx context.Context, body []byte, model string, cap int) (int, error) {
	if t.rdb == nil || t.llmURL == "" {
		// Fail-open if not wired (tests / boot before loader is ready).
		return 0, nil
	}
	sum := sha256.Sum256(body)
	key := tokenCacheKey(model, hex.EncodeToString(sum[:]))

	// Cache hit?
	if raw, err := t.rdb.Get(ctx, key).Bytes(); err == nil {
		if n, perr := strconv.Atoi(string(raw)); perr == nil {
			if n > cap {
				return n, ErrContextLengthExceeded
			}
			return n, nil
		}
	} else if !errors.Is(err, redis.Nil) {
		// Real Redis error (connection refused, etc.) — fail-open below.
		t.log.Warn("tokencount cache get failed", "err", err)
	}

	// Extract tokenizable text from the body (chat messages OR embed input).
	text := extractTokenizeText(body)
	reqBody, err := json.Marshal(map[string]any{"content": text})
	if err != nil {
		return 0, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.llmURL+"/tokenize", bytes.NewReader(reqBody))
	if err != nil {
		return 0, nil
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		t.log.Warn("tokencount /tokenize request failed", "err", err)
		return 0, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.log.Warn("tokencount /tokenize non-200", "status", resp.StatusCode)
		return 0, nil
	}
	var parsed struct {
		Tokens []int `json:"tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.log.Warn("tokencount /tokenize decode failed", "err", err)
		return 0, nil
	}
	n := len(parsed.Tokens)

	// Cache the count (best-effort; failures are non-fatal).
	if err := t.rdb.Set(ctx, key, strconv.Itoa(n), tokenCacheTTL).Err(); err != nil {
		t.log.Warn("tokencount cache set failed", "err", err)
	}
	if n > cap {
		return n, ErrContextLengthExceeded
	}
	return n, nil
}

// extractTokenizeText pulls the tokenizable text from a request body.
// Supports OpenAI chat messages[*].content (string only — vision message
// arrays return only the textual parts) and embeddings input (string or
// array of strings). On parse failure, returns the raw body as-is so the
// tokenizer at least sees the data; this is conservative (over-counts)
// rather than under-counts.
func extractTokenizeText(body []byte) string {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return string(body)
	}
	// Chat: concatenate message contents (string-only). Vision arrays drop
	// to fall-through (raw body) — Phase 5 may revisit when image support
	// lands; for now we conservatively over-count by passing the JSON.
	if msgsAny, ok := m["messages"]; ok {
		if msgs, ok := msgsAny.([]any); ok {
			var buf bytes.Buffer
			for _, m := range msgs {
				mm, _ := m.(map[string]any)
				if content, ok := mm["content"].(string); ok {
					buf.WriteString(content)
					buf.WriteByte('\n')
				}
			}
			return buf.String()
		}
	}
	// Embedding: input is either a single string or array of strings.
	if in, ok := m["input"]; ok {
		switch v := in.(type) {
		case string:
			return v
		case []any:
			var buf bytes.Buffer
			for _, s := range v {
				if str, ok := s.(string); ok {
					buf.WriteString(str)
					buf.WriteByte('\n')
				}
			}
			return buf.String()
		}
	}
	return string(body)
}

// extractModelName pulls the "model" field from a JSON request body.
// Returns an empty string on parse failure or missing field so callers
// can fall back to a safe default (e.g. the role name). Used by the
// dispatcher to build a cache key that is specific to the tokenizer of
// the requested model, preventing cross-tokenizer cache collisions
// (Pitfall 6 / HIGH-04 fix: cfg.Role was previously passed as model).
func extractModelName(body []byte) string {
	var m struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	return m.Model
}

// readAndRestoreBody is a helper for directors and the dispatcher. Reads
// the body, then restores it into a fresh ReadCloser so downstream handlers
// can read it again. Caller must set Content-Length if the body was
// modified after restore.
func readAndRestoreBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}
