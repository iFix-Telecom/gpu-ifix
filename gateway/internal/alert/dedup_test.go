package alert

// dedup_test.go — Task 2 (07-05) unit tests for the Redis SET NX EX 300
// fingerprint dedup gate. miniredis drives the happy paths (first call
// claims the key, second call inside the window is a duplicate); a
// closed miniredis drives the fail-open / fail-closed branches.

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newDedupTestRedis returns a redis client wired to a fresh miniredis
// plus the miniredis handle (so a test can Close() it to simulate a
// Redis outage). t.Cleanup tears both down.
func newDedupTestRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = rdb.Close()
		mr.Close()
	})
	return rdb, mr
}

// TestDedupShouldSend_FirstThenDuplicate covers behaviors 1+2: the first
// call with a fingerprint claims the key (shouldSend true); a second
// call with the same fingerprint inside the TTL window is a duplicate
// (shouldSend false).
func TestDedupShouldSend_FirstThenDuplicate(t *testing.T) {
	rdb, _ := newDedupTestRedis(t)
	ctx := context.Background()

	const fp = "breaker:local-llm:open"

	if !dedupShouldSend(ctx, rdb, SeverityCritical, fp) {
		t.Fatal("first call: shouldSend = false, want true (SET NX should succeed)")
	}
	if dedupShouldSend(ctx, rdb, SeverityCritical, fp) {
		t.Error("second call within window: shouldSend = true, want false (duplicate)")
	}
	// A different fingerprint is independent — not deduped.
	if !dedupShouldSend(ctx, rdb, SeverityWarning, "shed:local-llm:on") {
		t.Error("distinct fingerprint: shouldSend = false, want true")
	}
}

// TestDedupShouldSend_TTLExpiry confirms the key is set with a TTL — once
// it expires, the same fingerprint sends again. miniredis lets us
// fast-forward time past the 5-minute window.
func TestDedupShouldSend_TTLExpiry(t *testing.T) {
	rdb, mr := newDedupTestRedis(t)
	ctx := context.Background()

	const fp = "emerg:transition:emergency_active"
	if !dedupShouldSend(ctx, rdb, SeverityCritical, fp) {
		t.Fatal("first call: want shouldSend true")
	}
	if dedupShouldSend(ctx, rdb, SeverityCritical, fp) {
		t.Fatal("within window: want shouldSend false")
	}
	// Advance miniredis past the 5-minute dedup TTL.
	mr.FastForward(dedupTTL + 1)
	if !dedupShouldSend(ctx, rdb, SeverityCritical, fp) {
		t.Error("after TTL expiry: shouldSend = false, want true (window elapsed)")
	}
}

// TestDedupShouldSend_FailOpenCritical covers behavior 3: a Redis error
// on a critical event fails OPEN — better a duplicate page than a
// missed one.
func TestDedupShouldSend_FailOpenCritical(t *testing.T) {
	rdb, mr := newDedupTestRedis(t)
	ctx := context.Background()
	mr.Close() // every subsequent Redis op now errors

	if !dedupShouldSend(ctx, rdb, SeverityCritical, "breaker:local-llm:open") {
		t.Error("Redis error + critical: shouldSend = false, want true (fail-open)")
	}
}

// TestDedupShouldSend_FailClosedWarningInfo covers behavior 4: a Redis
// error on a warning or info event fails CLOSED — alert fatigue is the
// larger risk than a missed warning.
func TestDedupShouldSend_FailClosedWarningInfo(t *testing.T) {
	rdb, mr := newDedupTestRedis(t)
	ctx := context.Background()
	mr.Close() // every subsequent Redis op now errors

	if dedupShouldSend(ctx, rdb, SeverityWarning, "shed:local-llm:on") {
		t.Error("Redis error + warning: shouldSend = true, want false (fail-closed)")
	}
	if dedupShouldSend(ctx, rdb, SeverityInfo, "breaker:local-llm:closed") {
		t.Error("Redis error + info: shouldSend = true, want false (fail-closed)")
	}
}

// Card 17u65kmhj0r: fingerprint de contexto longo segura 1 h, não 5 min.
func TestDedupShouldSend_LongContextOneHour(t *testing.T) {
	rdb, mr := newDedupTestRedis(t)
	ctx := context.Background()
	const fp = "longctx:converseai-main:anthropic-haiku"
	if !dedupShouldSend(ctx, rdb, SeverityWarning, fp) {
		t.Fatal("first: want send")
	}
	mr.FastForward(30 * time.Minute)
	if dedupShouldSend(ctx, rdb, SeverityWarning, fp) {
		t.Error("after 30 min: want suppressed")
	}
	mr.FastForward(31 * time.Minute)
	if !dedupShouldSend(ctx, rdb, SeverityWarning, fp) {
		t.Error("after 61 min: want send again")
	}
}
