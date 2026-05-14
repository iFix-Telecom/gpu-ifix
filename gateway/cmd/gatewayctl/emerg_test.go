// Package main: unit tests for the `gatewayctl emerg` subcommand family
// (Plan 06-10 D-E1). Tests focus on:
//   - dispatcher exit codes (no args, unknown subcommand, --help)
//   - parseDurationDays helper covering the operator-friendly "Nd" suffix
//   - state-format flag validation (table|json allow-list)
//   - JSON output structure for `emerg state` against a miniredis-backed client
//   - force-provision / force-destroy publish typed EmergEvent payloads on
//     gw:emerg:events (BLOCKER 2 functional contract — they are NOT
//     placeholder logging-only stubs)
//
// Tests that need a Redis client use miniredis; tests that only exercise
// flag parsing call the inner runEmerg* helpers directly with no Redis
// dependency. The DB-backed `lifecycles` subcommand is exercised via flag
// parsing only here — the full SQL round-trip is integration-test
// territory (testcontainers, deferred to CI per Plan 06-09 convention).
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

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// newEmergMiniRedis spins up an in-memory Redis backed by miniredis and
// returns a connected *redis.Client. Cleanup hooks close both. Mirrors
// the helper in internal/redisx/emerg_test.go.
func newEmergMiniRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return mr, rdb
}

// captureStdout swaps os.Stdout for a pipe + buffer for the duration of
// fn() and returns the captured bytes. Used to assert JSON / tabwriter
// output without exec'ing the binary.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	var wg sync.WaitGroup
	var buf bytes.Buffer
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&buf, r)
	}()
	defer func() {
		_ = w.Close()
		wg.Wait()
		os.Stdout = orig
	}()
	fn()
	return buf.String()
}

// captureStderr is the stderr counterpart to captureStdout.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	var wg sync.WaitGroup
	var buf bytes.Buffer
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&buf, r)
	}()
	defer func() {
		_ = w.Close()
		wg.Wait()
		os.Stderr = orig
	}()
	fn()
	return buf.String()
}

// --- Dispatcher tests --------------------------------------------------------

func TestEmergUsage_NoArgs(t *testing.T) {
	ctx := context.Background()
	stderr := captureStderr(t, func() {
		if code := runEmerg(ctx, []string{}, slog.Default()); code != 2 {
			t.Errorf("expected exit 2, got %d", code)
		}
	})
	if !strings.Contains(stderr, "Usage: gatewayctl emerg") {
		t.Errorf("stderr missing usage hint: %q", stderr)
	}
}

func TestEmergUnknownSubcommand(t *testing.T) {
	ctx := context.Background()
	stderr := captureStderr(t, func() {
		if code := runEmerg(ctx, []string{"bogus"}, slog.Default()); code != 2 {
			t.Errorf("expected exit 2, got %d", code)
		}
	})
	if !strings.Contains(stderr, "unknown subcommand: bogus") {
		t.Errorf("stderr missing unknown-subcommand message: %q", stderr)
	}
}

// --- parseDurationDays helper -----------------------------------------------

func TestParseDurationDays_Cases(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"7d", 7 * 24 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"30d", 30 * 24 * time.Hour, false},
		{"24h", 24 * time.Hour, false},
		{"45m", 45 * time.Minute, false},
		{"500ms", 500 * time.Millisecond, false},
		{"", 0, true},
		{"garbage", 0, true},
		{"7days", 0, true}, // only "Nd" with bare number is the operator suffix
	}
	for _, tc := range cases {
		got, err := parseDurationDays(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseDurationDays(%q) = (%v, nil), want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDurationDays(%q) = (%v, %v), want (%v, nil)", tc.in, got, err, tc.want)
			continue
		}
		if got != tc.want {
			t.Errorf("parseDurationDays(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// --- state subcommand --------------------------------------------------------

func TestEmergStateFlag_UnknownFormat(t *testing.T) {
	ctx := context.Background()
	stderr := captureStderr(t, func() {
		if code := runEmergState(ctx, []string{"--format", "xml"}, slog.Default()); code != 2 {
			t.Errorf("expected exit 2 for unknown format, got %d", code)
		}
	})
	if !strings.Contains(stderr, "unknown format") {
		t.Errorf("stderr missing format error: %q", stderr)
	}
}

func TestEmergState_JSON_EmptyHash(t *testing.T) {
	_, rdb := newEmergMiniRedis(t)
	ctx := context.Background()
	stdout := captureStdout(t, func() {
		if code := runEmergStateWithRedis(ctx, rdb, []string{"--format", "json"}, slog.Default()); code != 0 {
			t.Errorf("expected exit 0, got %d", code)
		}
	})
	// Empty hash → empty JSON object. Whitespace and newlines tolerated.
	out := strings.TrimSpace(stdout)
	if out != "{}" {
		t.Errorf("expected `{}` for empty hash, got %q", out)
	}
}

func TestEmergState_JSON_PopulatedHash(t *testing.T) {
	_, rdb := newEmergMiniRedis(t)
	ctx := context.Background()

	// Pre-populate the gw:emerg:state hash via the canonical helper.
	if err := redisx.WriteEmergState(ctx, rdb,
		"emergency_active", "42", "http://1.2.3.4:9100", "12345", 1700000000); err != nil {
		t.Fatalf("WriteEmergState: %v", err)
	}

	stdout := captureStdout(t, func() {
		if code := runEmergStateWithRedis(ctx, rdb, []string{"--format", "json"}, slog.Default()); code != 0 {
			t.Errorf("expected exit 0, got %d", code)
		}
	})

	var got map[string]string
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, stdout)
	}
	want := map[string]string{
		"state":           "emergency_active",
		"lifecycle_id":    "42",
		"pod_url":         "http://1.2.3.4:9100",
		"pod_instance_id": "12345",
		"entered_at":      "1700000000",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("json[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestEmergState_Table(t *testing.T) {
	_, rdb := newEmergMiniRedis(t)
	ctx := context.Background()
	if err := redisx.WriteEmergState(ctx, rdb,
		"healthy", "0", "", "", 1700000000); err != nil {
		t.Fatalf("WriteEmergState: %v", err)
	}
	stdout := captureStdout(t, func() {
		if code := runEmergStateWithRedis(ctx, rdb, []string{"--format", "table"}, slog.Default()); code != 0 {
			t.Errorf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(stdout, "KEY") || !strings.Contains(stdout, "VALUE") {
		t.Errorf("table output missing header KEY/VALUE: %q", stdout)
	}
	if !strings.Contains(stdout, "state") || !strings.Contains(stdout, "healthy") {
		t.Errorf("table output missing state=healthy row: %q", stdout)
	}
}

// --- force-provision: BLOCKER 2 contract ------------------------------------

func TestEmergForceProvision_PublishesEvent(t *testing.T) {
	_, rdb := newEmergMiniRedis(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Subscribe BEFORE publishing — Pub/Sub is at-most-once.
	ps := redisx.SubscribeEmergEvents(ctx, rdb)
	t.Cleanup(func() { _ = ps.Close() })
	// Wait a beat for the subscription to register against miniredis.
	time.Sleep(50 * time.Millisecond)

	stdout := captureStdout(t, func() {
		if code := runEmergForceProvisionWithRedis(ctx, rdb,
			[]string{"--reason", "smoke-test"}, slog.Default()); code != 0 {
			t.Errorf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(stdout, "force-provision request published") {
		t.Errorf("stdout missing publish confirmation: %q", stdout)
	}

	msg, err := ps.ReceiveMessage(ctx)
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}
	if msg.Channel != redisx.EmergEventsChannel {
		t.Fatalf("msg.Channel = %q, want %q", msg.Channel, redisx.EmergEventsChannel)
	}
	var got redisx.EmergEvent
	if err := json.Unmarshal([]byte(msg.Payload), &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if got.Type != "force_provision_request" {
		t.Errorf("Type = %q, want force_provision_request", got.Type)
	}
	if got.Reason != "smoke-test" {
		t.Errorf("Reason = %q, want smoke-test", got.Reason)
	}
	if got.ReplicaID != "gatewayctl" {
		t.Errorf("ReplicaID = %q, want gatewayctl", got.ReplicaID)
	}
	if got.SinceUnix == 0 {
		t.Error("SinceUnix is zero, want non-zero unix timestamp")
	}
}

// --- force-destroy: BLOCKER 2 contract --------------------------------------

func TestEmergForceDestroy_PublishesEvent(t *testing.T) {
	_, rdb := newEmergMiniRedis(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ps := redisx.SubscribeEmergEvents(ctx, rdb)
	t.Cleanup(func() { _ = ps.Close() })
	time.Sleep(50 * time.Millisecond)

	stdout := captureStdout(t, func() {
		if code := runEmergForceDestroyWithRedis(ctx, rdb,
			[]string{}, slog.Default()); code != 0 {
			t.Errorf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(stdout, "force-destroy request published") {
		t.Errorf("stdout missing publish confirmation: %q", stdout)
	}

	msg, err := ps.ReceiveMessage(ctx)
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}
	var got redisx.EmergEvent
	if err := json.Unmarshal([]byte(msg.Payload), &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if got.Type != "force_destroy_request" {
		t.Errorf("Type = %q, want force_destroy_request", got.Type)
	}
	if got.Reason != "manual" {
		t.Errorf("Reason = %q, want manual", got.Reason)
	}
	if got.ReplicaID != "gatewayctl" {
		t.Errorf("ReplicaID = %q, want gatewayctl", got.ReplicaID)
	}
}

// --- lifecycles flag-parse-only tests ---------------------------------------

func TestEmergLifecycles_UnknownFormat(t *testing.T) {
	ctx := context.Background()
	stderr := captureStderr(t, func() {
		if code := runEmergLifecycles(ctx, []string{"--format", "xml"}, slog.Default()); code != 2 {
			t.Errorf("expected exit 2 for unknown format, got %d", code)
		}
	})
	if !strings.Contains(stderr, "unknown format") {
		t.Errorf("stderr missing format error: %q", stderr)
	}
}

func TestEmergLifecycles_BadSince(t *testing.T) {
	ctx := context.Background()
	stderr := captureStderr(t, func() {
		if code := runEmergLifecycles(ctx, []string{"--since", "garbage"}, slog.Default()); code != 2 {
			t.Errorf("expected exit 2 for bad --since, got %d", code)
		}
	})
	if !strings.Contains(stderr, "invalid --since") {
		t.Errorf("stderr missing --since error: %q", stderr)
	}
}

func TestEmergLifecycles_NegativeLimit(t *testing.T) {
	ctx := context.Background()
	stderr := captureStderr(t, func() {
		if code := runEmergLifecycles(ctx, []string{"--limit", "-5"}, slog.Default()); code != 2 {
			t.Errorf("expected exit 2 for negative --limit, got %d", code)
		}
	})
	if !strings.Contains(stderr, "--limit") {
		t.Errorf("stderr missing --limit error: %q", stderr)
	}
}
