package alert

// alerter_test.go — Task 3 (07-05) unit tests for the Run(ctx) goroutine.
// Drives miniredis + redisx.Publish* against an Alerter wired with the
// recording Fake* channels, asserting: critical fans out to 3 channels,
// warning to 2, duplicates are deduped, malformed JSON is survived, ctx
// cancel exits cleanly, and a blocking Send never stalls classification.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// ----------------------------------------------------------------------
// Test channel adapter.
//
// The recording fakes in testsupport.go expose Send(ctx, title, body) —
// a (title, body) shape, not the Channel interface's Send(ctx, Message).
// recordingChannel adapts a fake into a Channel: it satisfies the
// interface, forwards to the fake's Send, and is mutex-guarded because
// the alerter's per-channel workers run in their own goroutines (the
// fakes themselves are documented as NOT mutex-guarded).
// ----------------------------------------------------------------------

// fakeSender is the (title, body) Send shape every testsupport.Fake* has.
type fakeSender interface {
	Name() string
	Send(ctx context.Context, title, body string) error
}

// recordingChannel wraps a fakeSender as a Channel, threading the
// Message's Title/Body through the fake's (title, body) Send signature.
// An optional block channel lets a test make Send hang until released.
type recordingChannel struct {
	mu    sync.Mutex
	fake  fakeSender
	block chan struct{} // when non-nil, Send waits on it before returning
	calls []Message
}

func newRecordingChannel(f fakeSender) *recordingChannel {
	return &recordingChannel{fake: f}
}

func (r *recordingChannel) Name() string { return r.fake.Name() }

func (r *recordingChannel) Send(ctx context.Context, msg Message) error {
	if r.block != nil {
		select {
		case <-r.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r.mu.Lock()
	r.calls = append(r.calls, msg)
	r.mu.Unlock()
	return r.fake.Send(ctx, msg.Title, msg.Body)
}

// callCount returns how many Send calls this channel has recorded.
func (r *recordingChannel) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// newAlerterTestRig spins up miniredis + a redis client + three
// recordingChannels wrapping fresh fakes, and returns the Alerter ready
// to Run. t.Cleanup tears everything down.
func newAlerterTestRig(t *testing.T) (*Alerter, *redis.Client, *miniredis.Miniredis, *recordingChannel, *recordingChannel, *recordingChannel) {
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
	chatwoot := newRecordingChannel(&FakeChatwoot{})
	clickup := newRecordingChannel(&FakeClickUp{})
	brevo := newRecordingChannel(&FakeBrevo{})
	a := NewAlerter(rdb, []Channel{chatwoot, clickup, brevo}, nil)
	return a, rdb, mr, chatwoot, clickup, brevo
}

// waitFor polls cond until it is true or the deadline elapses.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// TestAlerter_CriticalFansOutToAllThree covers behavior 1: a critical
// breaker event reaches all three channels exactly once.
func TestAlerter_CriticalFansOutToAllThree(t *testing.T) {
	a, rdb, _, chatwoot, clickup, brevo := newAlerterTestRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go a.Run(ctx)
	time.Sleep(100 * time.Millisecond) // let SUBSCRIBE register

	if err := redisx.PublishBreakerEvent(ctx, rdb, redisx.BreakerEvent{
		Upstream: "local-llm", State: "open", SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	waitFor(t, "all three channels to receive the critical alert", func() bool {
		return chatwoot.callCount() == 1 && clickup.callCount() == 1 && brevo.callCount() == 1
	})
}

// TestAlerter_WarningSkipsChatwoot covers behavior 2: a warning event
// reaches clickup + brevo but NOT chatwoot.
func TestAlerter_WarningSkipsChatwoot(t *testing.T) {
	a, rdb, _, chatwoot, clickup, brevo := newAlerterTestRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go a.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	// A shed FSM → "on" event is warning-tier.
	if err := redisx.PublishShedEvent(ctx, rdb, redisx.ShedEvent{
		Upstream: "local-llm", State: "on", SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	waitFor(t, "clickup + brevo to receive the warning alert", func() bool {
		return clickup.callCount() == 1 && brevo.callCount() == 1
	})
	// Give any erroneous chatwoot fan-out a chance to land before asserting.
	time.Sleep(100 * time.Millisecond)
	if chatwoot.callCount() != 0 {
		t.Errorf("chatwoot received %d sends, want 0 (warning tier does not page WhatsApp)", chatwoot.callCount())
	}
}

// TestAlerter_DuplicateIsDeduped covers behavior 3: the SAME critical
// event published twice within the window reaches each channel exactly
// once.
func TestAlerter_DuplicateIsDeduped(t *testing.T) {
	a, rdb, _, chatwoot, clickup, brevo := newAlerterTestRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go a.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	ev := redisx.BreakerEvent{Upstream: "local-llm", State: "open", SinceUnix: time.Now().Unix()}
	for i := 0; i < 2; i++ {
		if err := redisx.PublishBreakerEvent(ctx, rdb, ev); err != nil {
			t.Fatalf("publish #%d: %v", i, err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	waitFor(t, "first event to fan out", func() bool {
		return chatwoot.callCount() == 1 && clickup.callCount() == 1 && brevo.callCount() == 1
	})
	// Let any (incorrect) second fan-out land.
	time.Sleep(150 * time.Millisecond)
	if chatwoot.callCount() != 1 || clickup.callCount() != 1 || brevo.callCount() != 1 {
		t.Errorf("dedup failed: chatwoot=%d clickup=%d brevo=%d, want 1/1/1",
			chatwoot.callCount(), clickup.callCount(), brevo.callCount())
	}
}

// TestAlerter_MalformedJSONSurvived covers behavior 4: a malformed
// payload logs a WARN and the loop continues — a subsequent valid event
// still fans out.
func TestAlerter_MalformedJSONSurvived(t *testing.T) {
	a, rdb, _, chatwoot, clickup, brevo := newAlerterTestRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go a.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	// Publish raw garbage onto the breaker channel — bypasses the typed
	// PublishBreakerEvent helper to inject an unparseable payload.
	if err := rdb.Publish(ctx, redisx.BreakerEventsChannel(), "{not valid json").Err(); err != nil {
		t.Fatalf("publish garbage: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// A valid critical event after the garbage must still fan out — the
	// loop survived the malformed payload.
	if err := redisx.PublishBreakerEvent(ctx, rdb, redisx.BreakerEvent{
		Upstream: "local-llm", State: "open", SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("publish valid: %v", err)
	}

	waitFor(t, "valid event after malformed JSON to still fan out", func() bool {
		return chatwoot.callCount() == 1 && clickup.callCount() == 1 && brevo.callCount() == 1
	})
}

// TestAlerter_CtxCancelExitsCleanly covers behavior 5: cancelling ctx
// makes Run return within a short bound.
func TestAlerter_CtxCancelExitsCleanly(t *testing.T) {
	a, _, _, _, _, _ := newAlerterTestRig(t)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		a.Run(ctx)
		close(done)
	}()
	time.Sleep(100 * time.Millisecond) // let Run subscribe

	cancel()
	select {
	case <-done:
		// Run returned cleanly.
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of ctx cancel")
	}
}

// TestAlerter_BlockingSendDoesNotStallClassification covers behavior 6:
// a Channel whose Send blocks must NOT stall the consume loop — a
// subsequent event for a DIFFERENT, healthy channel is still classified
// and delivered.
func TestAlerter_BlockingSendDoesNotStallClassification(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close(); mr.Close() })

	// chatwoot's Send blocks until we release it. clickup + brevo are
	// healthy. A critical event enqueues to all three; chatwoot's worker
	// blocks, but the CONSUME LOOP must not — clickup + brevo still
	// deliver, proving classification was not stalled.
	release := make(chan struct{})
	chatwoot := newRecordingChannel(&FakeChatwoot{})
	chatwoot.block = release
	clickup := newRecordingChannel(&FakeClickUp{})
	brevo := newRecordingChannel(&FakeBrevo{})
	a := NewAlerter(rdb, []Channel{chatwoot, clickup, brevo}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	if err := redisx.PublishBreakerEvent(ctx, rdb, redisx.BreakerEvent{
		Upstream: "local-llm", State: "open", SinceUnix: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// clickup + brevo must deliver even though chatwoot's worker is
	// blocked — the consume loop enqueued and moved on.
	waitFor(t, "clickup + brevo to deliver while chatwoot's Send blocks", func() bool {
		return clickup.callCount() == 1 && brevo.callCount() == 1
	})
	if chatwoot.callCount() != 0 {
		t.Errorf("chatwoot.callCount() = %d, want 0 (its Send is still blocked)", chatwoot.callCount())
	}
	// Release chatwoot — it should now deliver too.
	close(release)
	waitFor(t, "chatwoot to deliver after release", func() bool {
		return chatwoot.callCount() == 1
	})
}

// TestAlerter_FullQueueIncrementsDropCounter covers the Pitfall 5 /
// T-07-15 back-pressure path: when a channel's bounded worker queue is
// saturated, handle() drops the fan-out and bumps obs.AlertDroppedTotal
// rather than blocking the consume loop.
//
// Strategy: block chatwoot's worker so its queue cannot drain, then push
// more than channelQueueDepth distinct critical events through handle()
// directly. Distinct fingerprints (via the emerg channel, varying the
// event Type — severityFor classifies on State, so State stays
// "emergency_active" = critical while Type varies the fingerprint) mean
// no dedup collapse, so every event reaches the fan-out. Once chatwoot's
// queue is full the drop branch fires; the healthy channels still drain
// everything, proving handle() never blocked.
func TestAlerter_FullQueueIncrementsDropCounter(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close(); mr.Close() })

	release := make(chan struct{})
	chatwoot := newRecordingChannel(&FakeChatwoot{})
	chatwoot.block = release
	clickup := newRecordingChannel(&FakeClickUp{})
	brevo := newRecordingChannel(&FakeBrevo{})
	a := NewAlerter(rdb, []Channel{chatwoot, clickup, brevo}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Start the per-channel worker goroutines (Run does this) without
	// the Pub/Sub loop — this test feeds handle() directly.
	for name, w := range a.workers {
		go a.runWorker(ctx, name, w)
	}

	// More events than chatwoot's queue can buffer (1 in-flight, blocked,
	// + channelQueueDepth buffered) → the surplus hits the drop branch.
	total := channelQueueDepth + 20
	startDrops := testutil.ToFloat64(obs.AlertDroppedTotal)
	for i := 0; i < total; i++ {
		ev := redisx.EmergEvent{
			Type:  "transition-" + itoa(i), // distinct → distinct fingerprint
			State: "emergency_active",      // critical → fans out to all 3
		}
		a.handle(ctx, redisx.EmergEventsChannel, mustJSON(t, ev))
	}
	endDrops := testutil.ToFloat64(obs.AlertDroppedTotal)

	if endDrops <= startDrops {
		t.Errorf("AlertDroppedTotal did not increase: start=%v end=%v (want end > start once chatwoot's queue saturates)",
			startDrops, endDrops)
	}
	// clickup + brevo are healthy — they must drain every fan-out, which
	// proves handle() never blocked on the full chatwoot queue.
	waitFor(t, "healthy channels to drain every fan-out", func() bool {
		return clickup.callCount() == total && brevo.callCount() == total
	})
	close(release)
}

// itoa is a tiny strconv.Itoa to keep the test import block minimal.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

// TestAlerter_ReconcileBootSurfacesActiveIncident covers Pitfall 4 /
// T-07-18: a gateway restarting while gw:emerg:state shows an active
// incident still surfaces a critical alert.
func TestAlerter_ReconcileBootSurfacesActiveIncident(t *testing.T) {
	a, rdb, _, chatwoot, clickup, brevo := newAlerterTestRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Simulate "an emergency incident was active before this process
	// booted" by writing the emergency state mirror Hash directly.
	if err := redisx.WriteEmergState(ctx, rdb, "emergency_active", "42", "http://pod", "inst-1", time.Now().Unix()); err != nil {
		t.Fatalf("WriteEmergState: %v", err)
	}

	go a.Run(ctx)
	time.Sleep(100 * time.Millisecond) // let the workers start

	a.ReconcileBoot(ctx)

	waitFor(t, "ReconcileBoot to surface a critical alert for the active incident", func() bool {
		return chatwoot.callCount() == 1 && clickup.callCount() == 1 && brevo.callCount() == 1
	})
}

// TestAlerter_ReconcileBootRePagesActiveCriticalDespiteDedupKey covers
// WR-03/WR-04: when the live alerter ALREADY claimed the dedup key for
// the active critical incident (simulating "the original alerter paged,
// then the gateway crashed"), a restart's ReconcileBoot must STILL page
// — a silenced active critical incident after a crash is worse than a
// duplicate page. The pre-claimed key would suppress a live event, so
// this proves the critical-state dedup bypass works.
func TestAlerter_ReconcileBootRePagesActiveCriticalDespiteDedupKey(t *testing.T) {
	a, rdb, _, chatwoot, clickup, brevo := newAlerterTestRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := redisx.WriteEmergState(ctx, rdb, "emergency_active", "42", "http://pod", "inst-1", time.Now().Unix()); err != nil {
		t.Fatalf("WriteEmergState: %v", err)
	}

	// Simulate the live alerter having already paged this incident: claim
	// the dedup key for the fingerprint a "transition → emergency_active"
	// event produces. WR-04: assert the synthetic ReconcileBoot event and
	// a live transition event yield the IDENTICAL fingerprint, so the
	// pre-claimed key is genuinely the one that would dedup a live event.
	liveEv := redisx.EmergEvent{Type: "transition", State: "emergency_active"}
	livePayload, err := json.Marshal(liveEv)
	if err != nil {
		t.Fatalf("marshal live event: %v", err)
	}
	_, liveMsg, err := severityFor(redisx.EmergEventsChannel, livePayload)
	if err != nil {
		t.Fatalf("severityFor live event: %v", err)
	}
	synthEv := redisx.EmergEvent{Type: "transition", State: "emergency_active"}
	synthPayload, err := json.Marshal(synthEv)
	if err != nil {
		t.Fatalf("marshal synthetic event: %v", err)
	}
	_, synthMsg, err := severityFor(redisx.EmergEventsChannel, synthPayload)
	if err != nil {
		t.Fatalf("severityFor synthetic event: %v", err)
	}
	if liveMsg.Fingerprint != synthMsg.Fingerprint {
		t.Fatalf("live vs synthetic fingerprint mismatch: live=%q synthetic=%q",
			liveMsg.Fingerprint, synthMsg.Fingerprint)
	}

	// Claim the dedup key exactly as the live alerter would have.
	if err := rdb.Set(ctx, redisx.AlertDedupKey(liveMsg.Fingerprint), "1", 5*time.Minute).Err(); err != nil {
		t.Fatalf("pre-claim dedup key: %v", err)
	}

	go a.Run(ctx)
	time.Sleep(100 * time.Millisecond) // let the workers start

	a.ReconcileBoot(ctx)

	waitFor(t, "ReconcileBoot to re-page the active critical incident despite the pre-claimed dedup key", func() bool {
		return chatwoot.callCount() == 1 && clickup.callCount() == 1 && brevo.callCount() == 1
	})
}

// TestAlerter_ReconcileBootBenignStateNoAlert confirms ReconcileBoot
// does NOT alert when the emergency FSM is in a benign state.
func TestAlerter_ReconcileBootBenignStateNoAlert(t *testing.T) {
	a, rdb, _, chatwoot, clickup, brevo := newAlerterTestRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := redisx.WriteEmergState(ctx, rdb, "healthy", "", "", "", time.Now().Unix()); err != nil {
		t.Fatalf("WriteEmergState: %v", err)
	}

	go a.Run(ctx)
	time.Sleep(100 * time.Millisecond)
	a.ReconcileBoot(ctx)
	time.Sleep(150 * time.Millisecond)

	if chatwoot.callCount() != 0 || clickup.callCount() != 0 || brevo.callCount() != 0 {
		t.Errorf("ReconcileBoot alerted on a benign state: chatwoot=%d clickup=%d brevo=%d, want 0/0/0",
			chatwoot.callCount(), clickup.callCount(), brevo.callCount())
	}
}
