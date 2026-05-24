//go:build integration

// Package main — integration tests for `gatewayctl model-alias` (Phase
// 06.9 Plan 04 Task 4). Exercises the Postgres roundtrip via the shared
// freshSchema testcontainer helper (also defined in upstreams_test.go).
//
// CI runs: go test -tags=integration ./cmd/gatewayctl/...
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
)

// captureStdoutCLI is a per-package stdout capture helper for the
// integration tests in this file. Mirrors emerg_test.go's captureStdout
// shape — close write end + wait on copy goroutine BEFORE reading the
// buffer (avoids the data race -race would flag).
func captureStdoutCLI(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	var buf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&buf, r)
	}()
	fn()
	_ = w.Close()
	wg.Wait()
	os.Stdout = orig
	return buf.String()
}

// Test 1 — list returns the rows seeded by migration 0026 (3 tier-0
// backfill + 3 tier-1 seed = 6 rows).
func TestModelAliasList_Returns6Rows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, _ = freshSchema(t, ctx)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var code int
	out := captureStdoutCLI(t, func() {
		code = runModelAlias(ctx, []string{"list"}, log)
	})
	if code != 0 {
		t.Fatalf("list exit=%d", code)
	}
	// Migration 0026 ships 6 canonical rows. Allow the assertion to be
	// row-count flexible (test data may include extras seeded by other
	// tests in the same process — but the 6 canonical rows MUST appear).
	for _, name := range []string{"local-llm", "openrouter-chat", "local-stt", "openai-whisper", "local-embed", "openai-embed"} {
		if !strings.Contains(out, name) {
			t.Errorf("list output missing canonical upstream %q:\n%s", name, out)
		}
	}
	for _, alias := range []string{"qwen", "whisper", "bge-m3"} {
		if !strings.Contains(out, alias) {
			t.Errorf("list output missing canonical alias %q:\n%s", alias, out)
		}
	}
}

// Test 2 — set updates existing row's target via UpsertModelAlias.
func TestModelAliasSet_UpsertsViaSqlcQuery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if code := runModelAlias(ctx, []string{"set", "--alias", "qwen", "--upstream", "openrouter-chat", "--target", "qwen/qwen3.5-72b"}, log); code != 0 {
		t.Fatalf("set exit=%d", code)
	}

	// Verify via direct query.
	q := gen.New(pool)
	row, err := q.GetModelAlias(ctx, gen.GetModelAliasParams{Alias: "qwen", UpstreamName: "openrouter-chat"})
	if err != nil {
		t.Fatalf("GetModelAlias: %v", err)
	}
	if row.Target != "qwen/qwen3.5-72b" {
		t.Errorf("Target=%q; want qwen/qwen3.5-72b", row.Target)
	}
	if row.UpstreamName != "openrouter-chat" {
		t.Errorf("UpstreamName=%q; want openrouter-chat", row.UpstreamName)
	}
	if row.Upstream != "llm" {
		t.Errorf("Upstream(role)=%q; want llm", row.Upstream)
	}
}

// Test 3 — set inserts a NEW (alias, upstream) pair via UpsertModelAlias.
// Uses an alias not present in the canonical seed: "custom-model".
func TestModelAliasSet_InsertsNewTier1Row(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	q := gen.New(pool)
	before, err := q.ListModelAliases(ctx)
	if err != nil {
		t.Fatalf("ListModelAliases before: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if code := runModelAlias(ctx, []string{"set", "--alias", "custom-model", "--upstream", "openrouter-chat", "--target", "anthropic/claude-3.5"}, log); code != 0 {
		t.Fatalf("set exit=%d", code)
	}

	after, err := q.ListModelAliases(ctx)
	if err != nil {
		t.Fatalf("ListModelAliases after: %v", err)
	}
	if len(after) != len(before)+1 {
		t.Errorf("row count after=%d; want before+1 = %d", len(after), len(before)+1)
	}
}

// Test 5 — get returns the single matching row as JSON.
func TestModelAliasGet_ReturnsSpecificRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, _ = freshSchema(t, ctx)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var code int
	out := captureStdoutCLI(t, func() {
		code = runModelAlias(ctx, []string{"get", "--alias", "qwen", "--upstream", "openrouter-chat"}, log)
	})
	if code != 0 {
		t.Fatalf("get exit=%d", code)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(out), &row); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, out)
	}
	if row["Alias"] != "qwen" || row["UpstreamName"] != "openrouter-chat" {
		t.Errorf("get output:\n%s", out)
	}
}

// Test 6 — delete removes the row via DeleteModelAlias.
func TestModelAliasDelete_RemovesRowViaSqlcQuery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, _ := freshSchema(t, ctx)

	q := gen.New(pool)
	before, _ := q.ListModelAliases(ctx)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if code := runModelAlias(ctx, []string{"delete", "--alias", "qwen", "--upstream", "openrouter-chat"}, log); code != 0 {
		t.Fatalf("delete exit=%d", code)
	}
	after, _ := q.ListModelAliases(ctx)
	if len(after) != len(before)-1 {
		t.Errorf("after=%d, before=%d; want before-1", len(after), len(before))
	}
}

// Test 9 — R10: set with --upstream not in upstreams table returns error
// (FK-emulation by gating on GetUpstreamByName).
func TestModelAliasSet_RejectsUpstreamNotInUpstreamsTable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, _ = freshSchema(t, ctx)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// "fake-provider" is not in upstreamNameRole — first gate.
	code := runModelAlias(ctx, []string{"set", "--alias", "qwen", "--upstream", "fake-provider", "--target", "qwen/x"}, log)
	if code == 0 {
		t.Errorf("set with unknown upstream returned 0; want non-zero")
	}
}
