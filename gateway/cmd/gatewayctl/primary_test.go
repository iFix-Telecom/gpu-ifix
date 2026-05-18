// Package main: unit tests for the `gatewayctl primary` subcommand
// family (Plan 06.6-09 D-08). Tests focus on:
//   - dispatcher exit codes (no args, unknown subcommand)
//   - state subcommand against a miniredis-backed gw:primary:state Hash
//   - force-up / force-down PUBLISH typed PrimaryEvent payloads on
//     gw:primary:events — reviews consensus action #3 REAL CONSUMER
//     contract is honoured downstream by Plan 06.6-06a
//     primary.Reconciler.handleForceUpRequest / handleForceDownRequest
//   - schedule subcommand: stable resolved-rule print INCLUDING
//     ProvisionLeadSeconds (reviews consensus action #8 — operator
//     pre-flight visibility into the pre-warm offset for the 25–30min
//     cold-start reality of the upstream image)
//
// Tests that need a Redis client use miniredis; tests that only
// exercise flag parsing call the inner runPrimary* helpers directly.
// The DB-backed `lifecycles` subcommand is exercised via flag parsing
// only here — the full SQL round-trip is integration-test territory,
// deferred to Plan 06.6-10 Task 3 per the explicit t.Skip pointer in
// TestRunPrimaryLifecycles_FetchesFromDB.
//
// Stdout/stderr capture mirrors the helper pattern from emerg_test.go
// (close pipe write end + wait on copy goroutine BEFORE reading buffer
// — go test -race catches the alternative).
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

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// newPrimaryMiniRedis spins up an in-memory Redis backed by miniredis
// and returns a connected *redis.Client. Cleanup hooks close both.
// Mirrors newEmergMiniRedis.
func newPrimaryMiniRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
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

// capturePrimaryStdout / capturePrimaryStderr are renamed copies of the
// emerg helpers so the two test files don't share package-level test
// helpers (avoids order-dependent flakes if one file is run in
// isolation under -run). Same ordering invariant: close pipe write end
// AND wait on the io.Copy goroutine BEFORE reading the buffer.
func capturePrimaryStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
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

func capturePrimaryStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
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
	os.Stderr = orig
	return buf.String()
}

// defaultPrimaryCfg returns a config.Config populated with the
// Phase-6.6 PrimaryPodSchedule* defaults (Plan 06.6-03):
//   - Timezone:                 America/Sao_Paulo
//   - UpHour:                   8
//   - DownHour:                 22
//   - Days:                     mon..fri
//   - GraceRampDownSeconds:     300
//   - ProvisionLeadSeconds:     1800   (reviews #8)
//   - Disabled:                 true   (WAVE0-GATES Decision 5)
//
// Anything outside the PrimaryPodSchedule* group stays zero-valued —
// the schedule subcommand only consumes those fields.
func defaultPrimaryCfg() config.Config {
	return config.Config{
		PrimaryPodScheduleTimezone:             "America/Sao_Paulo",
		PrimaryPodScheduleUpHour:               8,
		PrimaryPodScheduleDownHour:             22,
		PrimaryPodScheduleDays:                 []string{"mon", "tue", "wed", "thu", "fri"},
		PrimaryPodScheduleGraceRampDownSeconds: 300,
		PrimaryPodScheduleProvisionLeadSeconds: 1800,
		PrimaryPodScheduleDisabled:             true,
	}
}

// --- Dispatcher tests --------------------------------------------------------

func TestRunPrimary_NoArgs(t *testing.T) {
	ctx := context.Background()
	stderr := capturePrimaryStderr(t, func() {
		if code := runPrimary(ctx, []string{}, slog.Default()); code != 2 {
			t.Errorf("expected exit 2, got %d", code)
		}
	})
	if !strings.Contains(stderr, "Usage: gatewayctl primary") {
		t.Errorf("stderr missing usage hint: %q", stderr)
	}
}

func TestRunPrimary_UnknownSubcommand(t *testing.T) {
	ctx := context.Background()
	stderr := capturePrimaryStderr(t, func() {
		if code := runPrimary(ctx, []string{"xxx"}, slog.Default()); code != 2 {
			t.Errorf("expected exit 2, got %d", code)
		}
	})
	if !strings.Contains(stderr, "unknown subcommand: xxx") {
		t.Errorf("stderr missing unknown-subcommand message: %q", stderr)
	}
}

// --- state subcommand --------------------------------------------------------

func TestRunPrimaryState_HappyPath(t *testing.T) {
	_, rdb := newPrimaryMiniRedis(t)
	ctx := context.Background()

	// Pre-populate gw:primary:state via the canonical helper (Plan
	// 06.6-07 WritePrimaryState). Use a recognisable state token + a
	// numeric lifecycle_id so the assertion can grep for both.
	if err := redisx.WritePrimaryState(ctx, rdb,
		"ready", "42", "http://10.0.0.5:9100", "12345", 1700000000); err != nil {
		t.Fatalf("WritePrimaryState: %v", err)
	}
	stdout := capturePrimaryStdout(t, func() {
		if code := runPrimaryStateWithRedis(ctx, rdb, []string{"--format", "table"}, slog.Default()); code != 0 {
			t.Errorf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(stdout, "ready") {
		t.Errorf("table output missing state=ready: %q", stdout)
	}
	if !strings.Contains(stdout, "42") {
		t.Errorf("table output missing lifecycle_id=42: %q", stdout)
	}
}

// --- force-up subcommand: reviews #3 contract -------------------------------

func TestRunPrimaryForceUp_PublishesEvent(t *testing.T) {
	_, rdb := newPrimaryMiniRedis(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Subscribe BEFORE publishing — Pub/Sub is at-most-once.
	ps := redisx.SubscribePrimaryEvents(ctx, rdb)
	t.Cleanup(func() { _ = ps.Close() })
	// Wait a beat for the subscription to register against miniredis.
	time.Sleep(50 * time.Millisecond)

	stdout := capturePrimaryStdout(t, func() {
		if code := runPrimaryForceUpWithRedis(ctx, rdb,
			[]string{"--reason", "smoke-test"}, slog.Default()); code != 0 {
			t.Errorf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(stdout, "force-up request published") {
		t.Errorf("stdout missing publish confirmation: %q", stdout)
	}

	msg, err := ps.ReceiveMessage(ctx)
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}
	if msg.Channel != redisx.PrimaryEventsChannel {
		t.Fatalf("msg.Channel = %q, want %q", msg.Channel, redisx.PrimaryEventsChannel)
	}
	var got redisx.PrimaryEvent
	if err := json.Unmarshal([]byte(msg.Payload), &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if got.Type != "force_up_request" {
		t.Errorf("Type = %q, want force_up_request", got.Type)
	}
	if got.Reason != "smoke-test" {
		t.Errorf("Reason = %q, want smoke-test", got.Reason)
	}
	if got.ReplicaID != "gatewayctl-cli" {
		t.Errorf("ReplicaID = %q, want gatewayctl-cli", got.ReplicaID)
	}
	if got.SinceUnix == 0 {
		t.Error("SinceUnix is zero, want non-zero unix timestamp")
	}
}

// --- force-down subcommand: reviews #3 contract -----------------------------

func TestRunPrimaryForceDown_PublishesEvent(t *testing.T) {
	_, rdb := newPrimaryMiniRedis(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ps := redisx.SubscribePrimaryEvents(ctx, rdb)
	t.Cleanup(func() { _ = ps.Close() })
	time.Sleep(50 * time.Millisecond)

	stdout := capturePrimaryStdout(t, func() {
		if code := runPrimaryForceDownWithRedis(ctx, rdb,
			[]string{}, slog.Default()); code != 0 {
			t.Errorf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(stdout, "force-down request published") {
		t.Errorf("stdout missing publish confirmation: %q", stdout)
	}

	msg, err := ps.ReceiveMessage(ctx)
	if err != nil {
		t.Fatalf("ReceiveMessage: %v", err)
	}
	var got redisx.PrimaryEvent
	if err := json.Unmarshal([]byte(msg.Payload), &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if got.Type != "force_down_request" {
		t.Errorf("Type = %q, want force_down_request", got.Type)
	}
	if got.Reason != "manual_gatewayctl" {
		t.Errorf("Reason = %q, want manual_gatewayctl (default)", got.Reason)
	}
	if got.ReplicaID != "gatewayctl-cli" {
		t.Errorf("ReplicaID = %q, want gatewayctl-cli", got.ReplicaID)
	}
}

// --- schedule subcommand: stable rule print ---------------------------------

func TestRunPrimarySchedule_PrintsResolvedRule(t *testing.T) {
	ctx := context.Background()
	stdout := capturePrimaryStdout(t, func() {
		if code := runPrimaryScheduleWithCfg(ctx, defaultPrimaryCfg(), slog.Default()); code != 0 {
			t.Errorf("expected exit 0, got %d", code)
		}
	})
	for _, want := range []string{
		"America/Sao_Paulo",
		"UpHour:",
		"8",
		"DownHour:",
		"22",
		"Disabled:",
		"true",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("schedule output missing %q: %q", want, stdout)
		}
	}
}

// --- schedule subcommand: reviews #8 pre-warm offset visibility -------------

// TestRunPrimarySchedule_ShowsProvisionLeadSeconds proves the reviews
// consensus action #8 contract: the operator MUST be able to verify
// PROVISION_LEAD_SECONDS via `gatewayctl primary schedule` so the
// pre-warm offset (default 1800s = 30min) can be cross-checked against
// the 25–30min cold-start reality of the upstream image. Without this,
// the operator has no way to confirm the env knob is wired correctly
// before the soak gate (PRIMARY_POD_SCHEDULE_DISABLED) is flipped to
// false.
func TestRunPrimarySchedule_ShowsProvisionLeadSeconds(t *testing.T) {
	ctx := context.Background()
	stdout := capturePrimaryStdout(t, func() {
		if code := runPrimaryScheduleWithCfg(ctx, defaultPrimaryCfg(), slog.Default()); code != 0 {
			t.Errorf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(stdout, "ProvisionLeadSeconds") {
		t.Errorf("schedule output missing ProvisionLeadSeconds label (reviews #8): %q", stdout)
	}
	if !strings.Contains(stdout, "1800") {
		t.Errorf("schedule output missing default value 1800 (reviews #8): %q", stdout)
	}
}

// --- lifecycles subcommand: DB-fetch deferred to Plan 06.6-10 ---------------

// TestRunPrimaryLifecycles_FetchesFromDB is an INTEGRATION test
// placeholder. The real DB round-trip is exercised in
// gateway/cmd/gatewayctl/primary_lifecycles_integration_test.go which
// lands in Plan 06.6-10 Task 3 — that plan closes BLOCKER 3 of the
// gatewayctl-primary coverage gap by spinning a testcontainers
// Postgres + applying migrations + seeding rows + asserting tabwriter
// output. Until then, the flag-parse paths are covered below.
func TestRunPrimaryLifecycles_FetchesFromDB(t *testing.T) {
	t.Skip("integration test — see gateway/cmd/gatewayctl/primary_lifecycles_integration_test.go (Plan 06.6-10 Task 3 closes BLOCKER 3 coverage gap)")
}

// --- lifecycles flag-parse-only tests ---------------------------------------

func TestRunPrimaryLifecycles_UnknownFormat(t *testing.T) {
	ctx := context.Background()
	stderr := capturePrimaryStderr(t, func() {
		if code := runPrimaryLifecycles(ctx, []string{"--format", "xml"}, slog.Default()); code != 2 {
			t.Errorf("expected exit 2 for unknown format, got %d", code)
		}
	})
	if !strings.Contains(stderr, "unknown format") {
		t.Errorf("stderr missing format error: %q", stderr)
	}
}

func TestRunPrimaryLifecycles_BadSince(t *testing.T) {
	ctx := context.Background()
	stderr := capturePrimaryStderr(t, func() {
		if code := runPrimaryLifecycles(ctx, []string{"--since", "garbage"}, slog.Default()); code != 2 {
			t.Errorf("expected exit 2 for bad --since, got %d", code)
		}
	})
	if !strings.Contains(stderr, "invalid --since") {
		t.Errorf("stderr missing --since error: %q", stderr)
	}
}

func TestRunPrimaryLifecycles_NegativeLimit(t *testing.T) {
	ctx := context.Background()
	stderr := capturePrimaryStderr(t, func() {
		if code := runPrimaryLifecycles(ctx, []string{"--limit", "-5"}, slog.Default()); code != 2 {
			t.Errorf("expected exit 2 for negative --limit, got %d", code)
		}
	})
	if !strings.Contains(stderr, "--limit") {
		t.Errorf("stderr missing --limit error: %q", stderr)
	}
}
