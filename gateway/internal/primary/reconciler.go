// Package primary (reconciler.go): Phase 6.6 Plan 06.6-06a — leader-
// elected 5-state primary-pod FSM dispatcher.
//
// # Goroutine layout
//
// Start(ctx) spawns three concurrent goroutines:
//
//  1. runScheduleLoop  — 1Hz ticker; gw:primary:lock redsync mutex with
//     30s TTL + 10s renew gate (parity emerg.runOneTick).
//     Leader-only path calls evaluateTick.  Reviews #2: DISABLED kill-
//     switch is checked at the per-tick decision point, NOT at Start.
//
//  2. runEventSubscriber — Redis SUBSCRIBE on gw:primary:events. Always
//     spawned, regardless of DISABLED (reviews #2). Leader-gated inside
//     the handler so non-leaders observe events without acting on them.
//     Dispatches force_up_request / force_down_request (reviews #3).
//
//  3. recoverOpenLifecycle (reviews #4) — runs ONCE before the loops to
//     rehydrate FSM state from primary_lifecycles WHERE ended_at IS NULL.
//
// # evaluateTick state branches
//
//   - StateAsleep      → evaluateAsleep      → maybe spawnProvisioning
//   - StateProvisioning→ evaluateProvisioning → no-op (the goroutine is
//     already running; the tick observes activeLifecycleID and short-
//     circuits)
//   - StateReady       → evaluateReady       → maybe startDrain
//   - StateDraining    → evaluateDraining    → maybe transition Destroying
//   - StateDestroying  → evaluateDestroying  → DestroyInstance + close
//
// # Cooldown gate (T-06.6-04 mitigation)
//
// lastProvisionFailureAt is populated on every failure path:
//   - vast_search_failed
//   - vast_create_error
//   - vast_status_msg_error (reviews #11 — Vast reports a `status_msg`
//     substring matching "error" mid-poll)
//   - health_timeout (cold-start budget exhausted)
//   - audit_write_failed
//
// evaluateAsleep refuses to re-provision until
// PrimaryProvisionFailureCooldownSeconds has elapsed since the timestamp.
// force_up_request bypasses the cooldown gate (operator-explicit override).
//
// # Wave 0 orthogonality
//
// The reconciler does NOT know about supervisord — orchestration is opaque
// from the gateway's view. It polls 4 HTTP endpoints on Vast-exposed host
// ports (8000 LLM + 8001 STT + 8002 embed + 9400 DCGM). All 4 endpoints
// land inside ONE container's network namespace (supervisord PID 1 +
// 4 child processes). This file is agnostic to that fact.
package primary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-redsync/redsync/v4"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/vastutil"
)

const (
	// primaryLockExpiry mirrors emergLockExpiry (parity). 30s TTL is
	// drift-tolerant: survives a Pub/Sub blip + Redis pause without
	// losing leadership.
	primaryLockExpiry = 30 * time.Second

	// primaryLockRenewInterval is 1/3 of TTL — so two consecutive
	// Pub/Sub blips can be absorbed without losing the lease.
	primaryLockRenewInterval = 10 * time.Second

	// primaryTickInterval is the cadence of runScheduleLoop. Same 1Hz as
	// emerg. The reconciler hot path is cheap (state read + a handful of
	// atomics); the costly work is gated by tick-level FSM state.
	primaryTickInterval = 1 * time.Second

	// primaryInstancePollInterval is the cadence of GetInstance polling
	// inside startProvisioning's waitForReady loop. 5s matches emerg —
	// fast enough to detect terminal-state transitions without hammering
	// the Vast.ai REST API.
	primaryInstancePollInterval = 5 * time.Second
)

// Start begins the reconciler. Spawns three goroutines:
//
//   - recovery: ONE-SHOT call to recoverOpenLifecycle BEFORE the loops
//     enter — rehydrates FSM state if a prior gateway boot left a row open.
//   - event subscriber: runEventSubscriber (always — even when DISABLED).
//   - schedule loop: runScheduleLoop (DISABLED is gated at tick level).
//
// All three exit cleanly when ctx is cancelled. Safe to call exactly once
// per Reconciler instance; subsequent calls would spawn duplicate
// goroutines.
//
// Reviews consensus actions #2 + #4: even when PRIMARY_POD_SCHEDULE_DISABLED
// is true the event subscriber must still run so gatewayctl force-up/down
// operations remain functional. Start() does NOT short-circuit on DISABLED.
func (r *Reconciler) Start(ctx context.Context) {
	log := r.deps.Log.With("subsystem", "primary.reconciler", "replica", r.deps.ReplicaID)

	// Reviews #4 — restart recovery FIRST. Failures are logged but never
	// block Start: a transient Vast 5xx or DB hiccup means the next leader
	// acquisition will retry. The schedule loop will not race because the
	// FSM starts at StateAsleep and the schedule loop's first tick fires
	// 1s after Start(); recoverOpenLifecycle completes in milliseconds when
	// no row exists.
	if err := r.recoverOpenLifecycle(ctx); err != nil {
		log.Error("primary recover-open-lifecycle failed", "err", err)
	}

	// UAT 14 follow-up (2026-05-19): when recovery completes with FSM
	// still at StateAsleep (no live lifecycle, or orphan closed), reset
	// the gw:primary:state Redis mirror so a stale snapshot from before
	// the restart (e.g. state=draining) does not linger. FSM transitions
	// during normal operation are mirrored via the onChange callback in
	// main.go; a no-transition boot needs an explicit fresh write.
	if r.deps.Redis != nil && r.deps.FSM != nil && r.deps.FSM.State() == StateAsleep {
		if err := redisx.WritePrimaryState(ctx, r.deps.Redis,
			StateAsleep.String(), "", "", "", time.Now().Unix()); err != nil {
			log.Warn("primary boot mirror reset failed", "err", err)
		}
	}

	go r.runEventSubscriber(ctx, log)
	go r.runScheduleLoop(ctx, log)
}

// runScheduleLoop is the 1Hz leader-election + evaluateTick driver.
// Mirrors emerg.runOneTick but with two-line redsync acquire+renew gate.
//
// Reviews #2: DISABLED is enforced at the tick decision point (after
// leader election succeeds). force_up_request published while DISABLED
// is still honored — the event subscriber path (runEventSubscriber)
// bypasses the schedule loop entirely.
func (r *Reconciler) runScheduleLoop(ctx context.Context, log *slog.Logger) {
	if r.deps.Redis == nil {
		// Test-only path — unit tests that exercise non-leader semantics
		// pass nil Redis. Spinning the loop without a mutex is dangerous
		// (no leader-election gate) so refuse to start.
		log.Error("primary runScheduleLoop: no Redis client wired; refusing to start")
		return
	}
	mutex := redisx.NewPrimaryRedsync(r.deps.Redis).NewMutex(
		redisx.PrimaryLockKey(),
		redsync.WithExpiry(primaryLockExpiry),
		redsync.WithTries(1),
		redsync.WithRetryDelay(0),
	)

	ticker := time.NewTicker(primaryTickInterval)
	defer ticker.Stop()

	log.Info("primary schedule loop started",
		"interval", primaryTickInterval,
		"lock_key", redisx.PrimaryLockKey(),
		"disabled", r.cfg.PrimaryPodScheduleDisabled)

	var lastExtend int64

	for {
		select {
		case <-ctx.Done():
			if r.isLeader.Load() {
				releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_, _ = mutex.UnlockContext(releaseCtx)
				cancel()
				r.isLeader.Store(false)
			}
			log.Info("primary schedule loop stopping")
			return
		case now := <-ticker.C:
			if !r.isLeader.Load() {
				if err := mutex.LockContext(ctx); err != nil {
					continue
				}
				r.isLeader.Store(true)
				lastExtend = now.Unix()
				log.Info("acquired primary leadership", "fsm_state", r.deps.FSM.State().String())
			} else if now.Unix()-lastExtend >= int64(primaryLockRenewInterval.Seconds()) {
				ok, err := mutex.ExtendContext(ctx)
				if err != nil || !ok {
					log.Warn("lost primary leadership; ceding", "err", err, "ok", ok)
					r.isLeader.Store(false)
					continue
				}
				lastExtend = now.Unix()
			}

			// Reviews #2 + UAT 14 follow-up (2026-05-19): DISABLED is now
			// gated at the per-evaluator level (evaluateAsleep + evaluateReady)
			// — NOT here. evaluateDraining + evaluateDestroying MUST keep
			// advancing under DISABLED so an operator force-down completes
			// instead of freezing the FSM in StateDraining forever. The
			// event subscriber goroutine still runs to honor manual
			// force-up/force-down events under DISABLED=true.
			r.evaluateTick(ctx, now, log)
		}
	}
}

// runEventSubscriber subscribes to gw:primary:events and dispatches
// force_up_request / force_down_request to the corresponding handlers
// (reviews #3). Non-leader replicas observe events but do NOT act.
//
// Reviews #2: ALWAYS runs even when PRIMARY_POD_SCHEDULE_DISABLED=true —
// the kill-switch only suppresses schedule ticks, not operator overrides.
// Without this property gatewayctl primary force-up would publish to a
// channel with no live consumer during the soak-gate phase.
func (r *Reconciler) runEventSubscriber(ctx context.Context, log *slog.Logger) {
	if r.deps.Redis == nil {
		log.Warn("primary runEventSubscriber: no Redis client wired; subscriber disabled")
		return
	}
	pubsub := redisx.SubscribePrimaryEvents(ctx, r.deps.Redis)
	defer func() { _ = pubsub.Close() }()

	ch := pubsub.Channel()
	log.Info("primary event subscriber started", "channel", redisx.PrimaryEventsChannel)
	for {
		select {
		case <-ctx.Done():
			log.Info("primary event subscriber stopping")
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var ev redisx.PrimaryEvent
			if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
				log.Debug("primary event subscriber: bad payload", "err", err)
				continue
			}
			// Non-leader observers: do NOT mutate FSM. Drop silently to
			// keep log volume manageable.
			if !r.isLeader.Load() {
				continue
			}
			switch ev.Type {
			case "force_up_request":
				r.handleForceUpRequest(ctx, ev, log)
			case "force_down_request":
				r.handleForceDownRequest(ctx, ev, log)
			default:
				// Self-published / informational events; nothing to do.
			}
		}
	}
}

// handleForceUpRequest is the operator-initiated provisioning entry point
// (reviews #3). Bypasses BOTH the schedule check AND the cooldown gate
// because the operator has explicit knowledge the system is healthy + the
// re-provision attempt should fire NOW.
//
// Only fires from StateAsleep: from any other state, a lifecycle is
// already in flight or active and operator intent is ambiguous.
func (r *Reconciler) handleForceUpRequest(ctx context.Context, ev redisx.PrimaryEvent, log *slog.Logger) {
	state := r.deps.FSM.State()
	if state != StateAsleep {
		log.Info("primary force-up: not in Asleep; skipping",
			"state", state.String(), "by_replica", ev.ReplicaID)
		return
	}
	log.Info("primary force-up: provisioning by operator request",
		"reason", ev.Reason, "by_replica", ev.ReplicaID)
	// Clear cooldown explicitly so subsequent evaluateAsleep ticks (and
	// the spawnProvisioning we are about to fire) don't re-fence behind
	// a stale failure timestamp.
	r.lastProvisionFailureAt.Store(nil)
	r.spawnProvisioning(ctx, "operator_force_up:"+ev.ReplicaID+":"+ev.Reason, log)
}

// handleForceDownRequest is the operator-initiated drain entry point
// (reviews #3). Three branches by current FSM state:
//
//   - StateProvisioning → cancelActiveLifecycle (triple-layer cancel +
//     Pub/Sub `cancel_in_flight` event + Vast destroy). The lifecycle
//     row is closed by the in-flight goroutine via the ctx.Done() branch
//     of waitForReadyOrDestroy (`cancelled_in_flight` shutdown_reason).
//   - StateReady / StateDraining → startDrain (ramp-down inflight then
//     transition to Destroying).
//   - Other states → noop.
//
// Without the Provisioning branch operators cannot abort a stuck
// provisioning lifecycle until the cold-start budget expires. The
// cancelActiveLifecycle helper has been in the codebase from the
// Plan 06.6-06a landing but was previously unreachable.
func (r *Reconciler) handleForceDownRequest(ctx context.Context, ev redisx.PrimaryEvent, log *slog.Logger) {
	state := r.deps.FSM.State()
	switch state {
	case StateProvisioning:
		log.Info("primary force-down: cancelling in-flight provisioning lifecycle",
			"reason", ev.Reason, "by_replica", ev.ReplicaID)
		r.cancelActiveLifecycle(ctx, "operator_force_down:"+ev.ReplicaID+":"+ev.Reason, log)
		return
	case StateReady, StateDraining:
		log.Info("primary force-down: initiating drain by operator request",
			"reason", ev.Reason, "by_replica", ev.ReplicaID)
		r.startDrain(ctx, "operator_force_down:"+ev.ReplicaID+":"+ev.Reason, log)
		return
	default:
		log.Info("primary force-down: not in Provisioning/Ready/Draining; skipping",
			"state", state.String(), "by_replica", ev.ReplicaID)
		return
	}
}

// evaluateTick is the FSM dispatcher. Reads State() once and routes to
// the 5 per-state evaluators. Mirrors emerg.evaluateTick pattern.
func (r *Reconciler) evaluateTick(ctx context.Context, now time.Time, log *slog.Logger) {
	switch r.deps.FSM.State() {
	case StateAsleep:
		r.evaluateAsleep(ctx, now, log)
	case StateProvisioning:
		// No-op: spawnProvisioning's goroutine drives the transition from
		// here. The tick observes the state and short-circuits.
	case StateReady:
		r.evaluateReady(ctx, now, log)
	case StateDraining:
		r.evaluateDraining(ctx, now, log)
	case StateDestroying:
		r.evaluateDestroying(ctx, now, log)
	}
}

// evaluateAsleep gates the provisioning trigger on:
//   - schedule says "should be provisioned" (reviews #8 ShouldBeProvisioned
//     — pre-warm offset BEFORE UpHour so the pod is Ready by UpHour given
//     the 25-30min cold-start reality), AND
//   - cooldown elapsed (T-06.6-04 mitigation — no immediate re-provision
//     after a recent failure).
//
// Reviews #8: uses ShouldBeProvisioned (NOT IsInPeak) so the pre-warm
// window kicks in PrimaryPodScheduleProvisionLeadSeconds (default 1800s
// = 30min) BEFORE the active window.
func (r *Reconciler) evaluateAsleep(ctx context.Context, now time.Time, log *slog.Logger) {
	if r.cfg.PrimaryPodScheduleDisabled {
		// Soak-gate posture; schedule-driven provisioning is off. Force
		// events bypass this gate via the event subscriber path.
		return
	}
	if !r.rule.ShouldBeProvisioned(now) {
		return
	}
	if !r.cooldownElapsed(now) {
		log.Debug("primary evaluateAsleep: cooldown not elapsed",
			"cooldown_seconds", r.cfg.PrimaryProvisionFailureCooldownSeconds)
		return
	}
	r.spawnProvisioning(ctx, "schedule_window_entered", log)
}

// cooldownElapsed returns true iff (a) no recent provisioning failure is
// recorded, OR (b) PrimaryProvisionFailureCooldownSeconds has elapsed
// since the most-recent failure.
//
// T-06.6-04 mitigation. Tests assert mechanical enforcement via
// TestEvaluateAsleep_NoopDuringCooldown +
// TestProvisioningFailure_SetsLastFailureTimestamp.
func (r *Reconciler) cooldownElapsed(now time.Time) bool {
	last := r.lastProvisionFailureAt.Load()
	if last == nil {
		return true
	}
	cooldown := time.Duration(r.cfg.PrimaryProvisionFailureCooldownSeconds) * time.Second
	return now.Sub(*last) >= cooldown
}

// evaluateReady triggers drain when the schedule window closes. Uses
// IsInPeak (NOT ShouldBeProvisioned) — drain happens at the active-window
// EXIT, not at the pre-warm-fall (pre-warm is asymmetric: only kicks in
// before UpHour; AFTER DownHour the schedule is OFF and ShouldBeProvisioned
// would also report false, but using IsInPeak makes intent clearer).
//
// UAT 14 follow-up (2026-05-19): DISABLED gates auto-drain. ScheduleRule's
// IsInPeak returns false under Disabled, which would drain a Ready pod
// brought up by operator force-up under the soak gate. Return early to
// preserve operator intent — only operator force-down should drain a
// force-up pod under DISABLED.
func (r *Reconciler) evaluateReady(ctx context.Context, now time.Time, log *slog.Logger) {
	// Pitfall #11 / D-13 re-assert (tech debt #9, UAT 18): an emerg cutback
	// (emerg/reconciler.go RestoreTier0) shares the SAME tier0Override map
	// and unilaterally clears the slot the primary wrote in markReady. The
	// primary only writes the slot once (markReady/recoverOpenLifecycle), so
	// a cutback leaves llm/tts routing to the stale static row until the
	// next pod cycle. Re-assert any cleared dynamic slot here at 1Hz — the
	// inconsistency window is <=1s. embed is EXCLUDED (D-03 — static off-pod
	// row, immune to this vector); stt is EXCLUDED (Phase 11.1 D-A4 —
	// Whisper deleted, stt is now tier-1 static OpenAI-Whisper only). This
	// runs regardless of DISABLED because an operator force-up pod is Ready
	// under DISABLED and is just as vulnerable to an emerg cutback clearing
	// its slot.
	if urls := r.activePodURLs.Load(); urls != nil && r.deps.Loader != nil {
		for _, role := range []string{"llm", "tts"} {
			if _, set := r.deps.Loader.Tier0OverrideURL(role); !set {
				r.deps.Loader.OverrideTier0(role, stripPrimaryReadinessSuffix(roleURL(*urls, role)))
				log.Warn("primary re-asserted tier-0 override (emerg cleared it)", "role", role)
			}
		}
	}

	if r.cfg.PrimaryPodScheduleDisabled {
		return
	}
	if !r.rule.IsInPeak(now) {
		r.startDrain(ctx, "schedule_window_exited", log)
		return
	}
	// Future hook: when all 3 breakers OPEN >60s, start drain for
	// "pod_unhealthy". Plan 06.6-06b owns the breaker observers; this
	// reconciler does not consume them directly.
}

// evaluateDraining ramps down the pod. Transitions Draining→Destroying
// when inflight==0 OR grace elapsed.
//
// Per Decisions Resolved #10: RestoreTier0 was already called when
// entering Draining (inside startDrain), so this method only needs to
// poll the inflight counter + the grace timer.
func (r *Reconciler) evaluateDraining(ctx context.Context, now time.Time, log *slog.Logger) {
	startedPtr := r.drainStartedAt.Load()
	if startedPtr == nil {
		// Defensive: drainStartedAt should always be set by startDrain
		// before the FSM transitions to Draining. If we observe nil,
		// stamp now and let the next tick decide grace.
		nowCopy := now
		r.drainStartedAt.Store(&nowCopy)
		return
	}
	grace := time.Duration(r.cfg.PrimaryPodScheduleGraceRampDownSeconds) * time.Second
	elapsed := now.Sub(*startedPtr)

	inflight := int64(0)
	if r.deps.Inflight != nil {
		inflight = r.deps.Inflight.Count("local-llm") +
			r.deps.Inflight.Count("local-embed")
	}

	if inflight == 0 || elapsed >= grace {
		log.Info("primary drain complete; transitioning to Destroying",
			"inflight", inflight, "elapsed_seconds", int64(elapsed.Seconds()),
			"grace_seconds", int64(grace.Seconds()))
		_ = r.deps.FSM.Transition(StateDraining, StateDestroying, now, "drain_complete")
		_ = ctx
	}
}

// evaluateDestroying calls vastutil.BestEffortDestroy + closes the
// lifecycle + transitions Destroying→Asleep.
//
// closeLifecycle handles the SQL UPDATE + RestoreTier0 (defensive
// idempotent — already cleared by startDrain) + DCGMScraper.SetURL("")
// + activePodURLs.Store(nil) + lifecycleCancel.Swap(nil).
func (r *Reconciler) evaluateDestroying(ctx context.Context, now time.Time, log *slog.Logger) {
	instanceID := r.activeInstanceID.Load()
	if instanceID != 0 {
		vastutil.BestEffortDestroy(ctx, r.deps.Vast, r.deps.Log, instanceID)
	}
	lifecycleID := r.activeLifecycleID.Load()
	if lifecycleID != 0 {
		if err := r.closeLifecycle(ctx, lifecycleID, "destroyed", 0); err != nil {
			log.Error("primary evaluateDestroying: closeLifecycle failed",
				"lifecycle_id", lifecycleID, "err", err)
		}
	}
	r.activeInstanceID.Store(0)
	r.activeLifecycleID.Store(0)
	_ = r.deps.FSM.Transition(StateDestroying, StateAsleep, now, "destroy_complete")
}

// startDrain is the entry point for Ready→Draining. Marks the DB row,
// stamps drainStartedAt, calls RestoreTier0 for 3 roles (so the
// dispatcher stops routing new requests to the about-to-die pod), and
// transitions the FSM.
//
// Idempotent — calling startDrain twice (e.g. operator force-down during
// schedule-driven drain) is a noop on the second call because the FSM
// state is already Draining.
func (r *Reconciler) startDrain(ctx context.Context, reason string, log *slog.Logger) {
	if r.deps.FSM.State() == StateDraining {
		log.Debug("primary startDrain: already draining; noop", "reason", reason)
		return
	}
	now := time.Now()
	r.drainStartedAt.Store(&now)

	// Mark the DB row's drain_started_at column (Phase 6.6 audit-trail
	// addition vs emerg). Best-effort: a DB failure is logged but does
	// not block the FSM transition.
	if q := r.queries(); q != nil {
		lifecycleID := r.activeLifecycleID.Load()
		if lifecycleID != 0 {
			eventJSON := vastutil.MustEventJSON("draining_started", map[string]any{
				"reason": reason,
				"ts":     now.Unix(),
			})
			if err := q.MarkPrimaryLifecycleDraining(ctx, gen.MarkPrimaryLifecycleDrainingParams{
				ID:        lifecycleID,
				EventJson: eventJSON,
			}); err != nil {
				log.Warn("primary startDrain: MarkPrimaryLifecycleDraining failed",
					"lifecycle_id", lifecycleID, "err", err)
			}
		}
	}

	// RestoreTier0 for the 2 dynamic roles (llm/tts — NOT embed, D-03;
	// NOT stt post-Phase 11.1 D-A4) BEFORE the FSM transition. New requests
	// land on the fallback chain immediately; in-flight ones drain over the
	// grace window.
	if r.deps.Loader != nil {
		r.deps.Loader.RestoreTier0("llm")
		r.deps.Loader.RestoreTier0("tts")
	}

	_ = r.deps.FSM.Transition(StateReady, StateDraining, now, reason)
	r.publishPrimaryEvent(ctx, redisx.PrimaryEvent{
		Type:        "draining_started",
		State:       "draining",
		LifecycleID: r.activeLifecycleID.Load(),
		Reason:      reason,
		SinceUnix:   now.Unix(),
		ReplicaID:   r.deps.ReplicaID,
	}, log)
}

// markReady is the success exit of the provisioning poll loop. Marks the
// DB row healthy, stores activePodURLs, overrides tier-0 for 2 roles
// (llm+tts post-Phase 11.1 D-A4), points DCGM scraper at :9400/metrics,
// publishes primary_ready, and transitions Provisioning→Ready.
func (r *Reconciler) markReady(ctx context.Context, lifecycleID int64, urls primaryPodURLs, acceptedDPH float64, log *slog.Logger) error {
	if q := r.queries(); q != nil {
		eventJSON := vastutil.MustEventJSON("health_pass", map[string]any{
			"lifecycle_id": lifecycleID,
			"llm_url":      urls.LLM,
			"tts_url":      urls.TTS,
			"dcgm_url":     urls.DCGM,
			"dph":          acceptedDPH,
		})
		if err := q.MarkPrimaryLifecycleHealthy(ctx, gen.MarkPrimaryLifecycleHealthyParams{
			ID:        lifecycleID,
			EventJson: eventJSON,
		}); err != nil {
			return fmt.Errorf("MarkPrimaryLifecycleHealthy: %w", err)
		}
	}
	r.activePodURLs.Store(&urls)
	if r.deps.Loader != nil {
		// Phase 11.1 (D-A4): the dynamic primary roster is llm/tts — NOT
		// embed (D-03, embed relocated to a 24/7 CPU host as a static tier-0
		// row), NOT stt (D-A4, Whisper deleted from pod; /v1/audio/
		// transcriptions routes to tier-1 OpenAI-Whisper static row only).
		// We strip the readiness suffix here so the dispatcher's
		// ReverseProxy target is the BASE URL (parity emerg markHealthy /
		// stripHealthSuffix).
		r.deps.Loader.OverrideTier0("llm", stripPrimaryReadinessSuffix(urls.LLM))
		r.deps.Loader.OverrideTier0("tts", stripPrimaryReadinessSuffix(urls.TTS))
		// Refresh is intentionally NOT called here — the OverrideTier0 path
		// is atomic and Live; Refresh would re-scan the DB which is
		// orthogonal to this transition. Refresh remains available for
		// LISTEN/NOTIFY triggers (Plan 06.6-08 wiring).
	}
	if r.deps.DCGMScraper != nil {
		r.deps.DCGMScraper.SetURL(urls.DCGM)
	}
	now := time.Now()
	_ = r.deps.FSM.Transition(StateProvisioning, StateReady, now, "all_probes_passed")
	r.publishPrimaryEvent(ctx, redisx.PrimaryEvent{
		Type:        "primary_ready",
		State:       "ready",
		LifecycleID: lifecycleID,
		Reason:      "all_probes_passed",
		SinceUnix:   now.Unix(),
		ReplicaID:   r.deps.ReplicaID,
	}, log)
	vastutil.CaptureBreadcrumb("primary.health_pass", map[string]any{
		"lifecycle_id": lifecycleID,
		"llm_url":      urls.LLM,
	})
	return nil
}

// closeLifecycle is the single point of contact for closing a primary
// lifecycle row. Mirrors emerg.closeLifecycle:
//
//   - Append `lifecycle_close` event JSONB + ended_at = NOW() (sqlc
//     ClosePrimaryLifecycle is idempotent via WHERE ended_at IS NULL).
//   - Clear activePodURLs.
//   - Defensive RestoreTier0 for 3 roles (idempotent — already cleared by
//     startDrain when entering Draining).
//   - DCGMScraper.SetURL("") (idempotent).
//   - Swap lifecycleCancel to nil + invoke previous cancel (if any).
//
// The caller is responsible for the FSM transition. closeLifecycle is a
// pure data-plane helper.
func (r *Reconciler) closeLifecycle(ctx context.Context, id int64, reason string, acceptedDPH float64) error {
	cost := r.calculatePrimaryCostBRL(ctx, id, acceptedDPH)
	if q := r.queries(); q != nil {
		eventJSON := vastutil.MustEventJSON("lifecycle_close", map[string]any{
			"reason":         reason,
			"total_cost_brl": cost,
		})
		dbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := q.ClosePrimaryLifecycle(dbCtx, gen.ClosePrimaryLifecycleParams{
			ID:             id,
			ShutdownReason: pgtype.Text{String: reason, Valid: true},
			TotalCostBrl:   vastutil.PgNumericFromFloat(cost),
			EventJson:      eventJSON,
		}); err != nil {
			r.deps.Log.Error("primary closeLifecycle: ClosePrimaryLifecycle failed",
				"id", id, "reason", reason, "err", err)
			return err
		}
	}
	r.activePodURLs.Store(nil)
	if cancelPtr := r.lifecycleCancel.Swap(nil); cancelPtr != nil {
		(*cancelPtr)()
	}
	if r.deps.Loader != nil {
		r.deps.Loader.RestoreTier0("llm")
		r.deps.Loader.RestoreTier0("tts")
	}
	if r.deps.DCGMScraper != nil {
		r.deps.DCGMScraper.SetURL("")
	}
	return nil
}

// calculatePrimaryCostBRL computes the realised cost of a primary
// lifecycle for the close-event payload. Returns 0 when accepted_dph is
// 0 (no instance was ever created) OR first_health_pass_at is NULL.
// Mirrors emerg.calculateCostBRL.
func (r *Reconciler) calculatePrimaryCostBRL(ctx context.Context, id int64, acceptedDPH float64) float64 {
	if acceptedDPH <= 0 || r.deps.DB == nil {
		return 0
	}
	var firstHealth pgtype.Timestamptz
	row := r.deps.DB.QueryRow(ctx,
		`SELECT first_health_pass_at FROM ai_gateway.primary_lifecycles WHERE id = $1`, id)
	if err := row.Scan(&firstHealth); err != nil {
		return 0
	}
	if !firstHealth.Valid {
		return 0
	}
	hours := time.Since(firstHealth.Time).Hours()
	if hours < 0 {
		hours = 0
	}
	return acceptedDPH * hours * r.deps.Cfg.USDToBRLRate
}

// cancelActiveLifecycle is the triple-layer cancel pattern (parity
// emerg.cancelActiveLifecycle):
//
//  1. Cancel the provisioning goroutine's context (lifecycleCancel).
//  2. PUBLISH cancel_in_flight on gw:primary:events so any cross-replica
//     observer can update its mirror.
//  3. vastutil.BestEffortDestroy with a fresh background ctx (Pitfall 8 —
//     the parent ctx may already be cancelled).
//
// Used by operator force-down while mid-provisioning AND by the schedule
// loop when the window closes during Provisioning.
func (r *Reconciler) cancelActiveLifecycle(ctx context.Context, reason string, log *slog.Logger) {
	if cancelPtr := r.lifecycleCancel.Swap(nil); cancelPtr != nil {
		(*cancelPtr)()
	}
	r.publishPrimaryEvent(ctx, redisx.PrimaryEvent{
		Type:        "cancel_in_flight",
		State:       r.deps.FSM.State().String(),
		LifecycleID: r.activeLifecycleID.Load(),
		Reason:      reason,
		SinceUnix:   time.Now().Unix(),
		ReplicaID:   r.deps.ReplicaID,
	}, log)
	instanceID := r.activeInstanceID.Load()
	if instanceID != 0 {
		vastutil.BestEffortDestroy(ctx, r.deps.Vast, r.deps.Log, instanceID)
	}
}

// spawnProvisioning kicks off the async provisioning lifecycle. INSERTs
// the lifecycle row FIRST (D-D5: row exists before any Vast call so
// restart-recovery can find pre-create orphans). Then spawns a goroutine
// that drives SearchOffers → CreateInstance → waitForReady → markReady.
//
// Idempotent — if the FSM is already Provisioning the call is a noop.
func (r *Reconciler) spawnProvisioning(parentCtx context.Context, reason string, log *slog.Logger) {
	q := r.queries()
	if q == nil {
		log.Error("primary spawnProvisioning: no DB pool wired; refusing to provision")
		return
	}
	if r.activeLifecycleID.Load() != 0 {
		log.Debug("primary spawnProvisioning: lifecycle already in flight; noop",
			"lifecycle_id", r.activeLifecycleID.Load())
		return
	}
	row, err := q.InsertPrimaryLifecycle(parentCtx, gen.InsertPrimaryLifecycleParams{
		TriggerReason: reason,
		LeaderReplica: pgtype.Text{String: r.deps.ReplicaID, Valid: true},
	})
	if err != nil {
		log.Error("primary spawnProvisioning: InsertPrimaryLifecycle failed", "err", err)
		now := time.Now()
		r.lastProvisionFailureAt.Store(&now)
		return
	}
	r.activeLifecycleID.Store(row.ID)
	_ = r.deps.FSM.Transition(StateAsleep, StateProvisioning, time.Now(), reason)
	r.publishPrimaryEvent(parentCtx, redisx.PrimaryEvent{
		Type:        "provisioning_started",
		State:       "provisioning",
		LifecycleID: row.ID,
		Reason:      reason,
		SinceUnix:   time.Now().Unix(),
		ReplicaID:   r.deps.ReplicaID,
	}, log)

	ctx, cancel := context.WithCancel(parentCtx)
	r.lifecycleCancel.Store(&cancel)
	go func() {
		defer cancel()
		err := r.provisionLifecycle(ctx, row.ID, log)
		if err != nil {
			now := time.Now()
			r.lastProvisionFailureAt.Store(&now)
			log.Error("primary provisionLifecycle returned error",
				"lifecycle_id", row.ID, "err", err)
			// Force the FSM back to Asleep so the next tick can re-evaluate
			// once the cooldown window elapses. SetState (unconditional
			// CAS-loop) — Transition would silently noop if the goroutine
			// already transitioned the FSM mid-flight.
			r.deps.FSM.SetState(StateAsleep, time.Now(), "provision_error:"+errReason(err))
			r.activeInstanceID.Store(0)
			r.activeLifecycleID.Store(0)
		}
	}()
}

// provisionLifecycle runs SearchOffers → CreateInstance → waitForReady.
// Mirrors emerg.provisionLifecycle without the 3-attempt bid race retry
// (primary pods schedule at known peak hours; a transient bid race can be
// retried on the next tick after cooldown).
func (r *Reconciler) provisionLifecycle(ctx context.Context, lifecycleID int64, log *slog.Logger) error {
	if r.deps.Vast == nil {
		_ = r.closeLifecycle(ctx, lifecycleID, "no_vast_client", 0)
		return errors.New("primary: no Vast.ai client wired")
	}
	// Phase 11.1 D-A6 (Wave 0 EVIDENCE-00): build a [primary, fallback]
	// SearchFilter pair and iterate — primary shape preferred (1×3090 @
	// $0.30), fallback shape only when the primary cap returns zero
	// qualified offers (2×3090 @ $0.60; same GPU model, deeper EU pool).
	filters := vast.DefaultSearchFilters(
		r.cfg.PrimaryVastPriceCapPrimary, r.cfg.PrimaryVastPriceCapFallback,
		r.cfg.PrimaryHostID,
		r.cfg.PrimaryVastGPUNamePrimary, r.cfg.PrimaryVastGPUNameFallback,
		r.cfg.PrimaryVastNumGPUsPrimary, r.cfg.PrimaryVastNumGPUsFallback,
		r.cfg.PrimaryVastMachineBlocklist...,
	)
	// shapeCaps mirrors the filter pair so vastutil.FilterBelowCap can
	// re-apply the per-shape ceiling client-side (epsilon-tolerant; UAT 17
	// 2026-05-19 Vast inventory race regression).
	shapeCaps := []float64{r.cfg.PrimaryVastPriceCapPrimary, r.cfg.PrimaryVastPriceCapFallback}

	// No geolocation restriction (operator decision 2026-05-21).
	// Machine allowlist (PRIMARY_VAST_MACHINE_ALLOWLIST): PREFERENCE pass on
	// the PRIMARY shape only — known-good 1×3090 hosts first; broaden to
	// the full qualified PRIMARY search when allowlisted hosts are busy;
	// then iterate the FALLBACK shape if PRIMARY is exhausted.
	var pickable []vast.Offer
	var pickedShape int

	if len(r.cfg.PrimaryVastMachineAllowlist) > 0 {
		allowFilter := vast.WithMachineAllowlist(filters[0], r.cfg.PrimaryVastMachineAllowlist)
		offers, err := r.deps.Vast.SearchOffers(ctx, allowFilter)
		if err != nil {
			_ = r.closeLifecycle(ctx, lifecycleID, "search_failed", 0)
			return err
		}
		pickable = vastutil.FilterBelowCap(offers, shapeCaps[0])
		if len(pickable) == 0 {
			log.Info("primary allowlist exhausted; broadening to full qualified search",
				"allowlist", r.cfg.PrimaryVastMachineAllowlist, "shape", 0)
		}
	}

	if len(pickable) == 0 {
		for i, f := range filters {
			offers, err := r.deps.Vast.SearchOffers(ctx, f)
			if err != nil {
				_ = r.closeLifecycle(ctx, lifecycleID, "search_failed", 0)
				return err
			}
			candidates := vastutil.FilterBelowCap(offers, shapeCaps[i])
			if len(candidates) > 0 {
				pickable = candidates
				pickedShape = i
				log.Info("primary offers found for shape",
					"shape", i, "offer_count", len(candidates),
					"cap", shapeCaps[i])
				break
			}
			log.Info("primary shape returned no qualified offers; trying next",
				"shape", i, "cap", shapeCaps[i])
		}
	}

	if len(pickable) == 0 {
		_ = r.closeLifecycle(ctx, lifecycleID, "no_offers_below_cap", 0)
		return errors.New("primary: no offers below cap (both shapes exhausted)")
	}
	_ = pickedShape // logged above; reserved for future per-shape metrics.
	offer := pickable[0]
	// Catalog the picked host so failures (e.g. broken-CDI multi-GPU machines)
	// can be added to PRIMARY_VAST_MACHINE_BLOCKLIST. machine_id correlates the
	// later terminal/CDI error (logged with instance_id) back to the host.
	log.Info("primary offer picked",
		"offer_id", offer.ID,
		"machine_id", offer.MachineID,
		"host_id", offer.HostID,
		"dph", offer.DphTotal,
		"geo", offer.Geolocation)
	req, err := r.buildCreateRequest(offer, lifecycleID)
	if err != nil {
		_ = r.closeLifecycle(ctx, lifecycleID, "build_create_request_failed:"+err.Error(), 0)
		return err
	}
	instance, err := r.deps.Vast.CreateInstance(ctx, offer.ID, req)
	if err != nil {
		_ = r.closeLifecycle(ctx, lifecycleID, "create_error", 0)
		return err
	}
	r.activeInstanceID.Store(instance.ID)
	q := r.queries()
	if q != nil {
		eventJSON := vastutil.MustEventJSON("offer_accepted", map[string]any{
			"offer_id":    offer.ID,
			"instance_id": instance.ID,
			"machine_id":  offer.MachineID,
			"host_id":     offer.HostID,
			"dph":         offer.DphTotal,
		})
		if err := q.UpdatePrimaryLifecycleVastIDs(ctx, gen.UpdatePrimaryLifecycleVastIDsParams{
			ID:             lifecycleID,
			VastOfferID:    vastutil.PgInt8(offer.ID),
			VastInstanceID: vastutil.PgInt8(instance.ID),
			AcceptedDph:    vastutil.PgNumericFromFloat(offer.DphTotal),
			EventJson:      eventJSON,
		}); err != nil {
			vastutil.BestEffortDestroy(ctx, r.deps.Vast, r.deps.Log, instance.ID)
			_ = r.closeLifecycle(ctx, lifecycleID, "audit_write_failed", 0)
			return err
		}
	}
	return r.waitForReadyOrDestroy(ctx, lifecycleID, instance.ID, offer.DphTotal, log)
}

// waitForReadyOrDestroy polls GetInstance every primaryInstancePollInterval
// until either ALL 4 health endpoints pass (LLM + STT + embed + DCGM) OR
// a terminal exit path fires.
//
// Reviews #11 status_msg gate: each poll iteration ALSO inspects
// inst.StatusMsg and aborts on a non-empty error substring. Carries the
// lifecycle-29 forensics fix from STATE.md.
//
// Wave 0 supervisord 4-services note: the 4 endpoints sit on 4 different
// container ports (8000/8001/8002/9400) but share the SAME container's
// network namespace. The reconciler does not need to know this — it polls
// 4 URLs via the Vast.ai-exposed host port mapping.
func (r *Reconciler) waitForReadyOrDestroy(ctx context.Context, lifecycleID, instanceID int64, acceptedDPH float64, log *slog.Logger) error {
	poll := time.NewTicker(primaryInstancePollInterval)
	defer poll.Stop()
	deadline := time.NewTimer(time.Duration(r.cfg.PrimaryProvisionColdStartBudgetSeconds) * time.Second)
	defer deadline.Stop()

	// Counter for consecutive IsTerminal observations. Vast.ai reports
	// `actual_status` from a polling agent on the host — during the boot
	// window the host may transiently report `exited` / `offline` while
	// the container is starting (image extract, supervisord launching).
	// Require 3 consecutive terminal observations (~30s) before declaring
	// the instance dead. UAT 2026-05-18 lifecycle 4 captured a false-positive
	// terminal close 12s before 4 endpoints were actually reachable.
	terminalStrikes := 0
	const terminalConfirmStrikes = 3

	// Counter for consecutive ErrInstanceNotFound observations. Same
	// transient-flap rationale as terminalStrikes but for a different
	// upstream signal: Vast can return `{"instances": null}` for an
	// instance that is STILL alive on the host (state-transition glitch /
	// eventual consistency). UAT 2026-05-27 lifecycle 2 captured this —
	// 4m24s of successful polls then a single null response silently
	// closed the DB row + left a $0.04 Vast orphan because the close
	// path also missed BestEffortDestroy. Apply the same 3-strike
	// confirmation that already gates IsTerminal(); reset on ANY
	// non-ErrInstanceNotFound result (success OR different error class)
	// so unrelated flaps do not accumulate strikes. See
	// .planning/debug/primary-reconciler-silent-hang.md.
	notFoundStrikes := 0

	for {
		select {
		case <-ctx.Done():
			vastutil.BestEffortDestroy(ctx, r.deps.Vast, r.deps.Log, instanceID)
			_ = r.closeLifecycle(context.Background(), lifecycleID, "cancelled_in_flight", 0)
			return ctx.Err()
		case <-deadline.C:
			vastutil.BestEffortDestroy(ctx, r.deps.Vast, r.deps.Log, instanceID)
			_ = r.closeLifecycle(context.Background(), lifecycleID, "health_timeout", 0)
			return errors.New("primary: cold-start budget exhausted")
		case <-poll.C:
			inst, err := r.deps.Vast.GetInstance(ctx, instanceID)
			if err != nil {
				if errors.Is(err, vast.ErrInstanceNotFound) {
					notFoundStrikes++
					log.Warn("primary provisioning: Vast GET returned no_such_instance",
						"lifecycle_id", lifecycleID,
						"vast_instance_id", instanceID,
						"strike_count", notFoundStrikes,
						"confirm_at", terminalConfirmStrikes,
						"error_class", "ErrInstanceNotFound")
					if notFoundStrikes >= terminalConfirmStrikes {
						vastutil.BestEffortDestroy(ctx, r.deps.Vast, r.deps.Log, instanceID)
						_ = r.closeLifecycle(context.Background(), lifecycleID, "instance_terminal_state_confirmed", 0)
						return errors.New("primary: instance terminal (3-strike confirm via ErrInstanceNotFound)")
					}
					continue
				}
				// Transient non-not-found GET error — reset the not-found
				// strike counter so an unrelated flap mode does not
				// accumulate strikes across error classes.
				notFoundStrikes = 0
				continue
			}
			// Healthy GET response — reset the not-found strike counter so a
			// single transient null between healthy polls does not trip the
			// 3-strike close. Mirrors the terminalStrikes reset below.
			notFoundStrikes = 0
			// Reviews #11 — Vast `status_msg` early-abort.
			if msg := strings.TrimSpace(inst.StatusMsg); msg != "" {
				if strings.Contains(strings.ToLower(msg), "error") {
					// Truncate to 200 chars to keep the forensic event bounded.
					trunc := msg
					if len(trunc) > 200 {
						trunc = trunc[:200]
					}
					forensicsReason := "vast_status_msg_error:" + trunc
					log.Error("primary provisioning: Vast reported instance error",
						"instance_id", instanceID, "status_msg", msg)
					vastutil.BestEffortDestroy(ctx, r.deps.Vast, r.deps.Log, instanceID)
					_ = r.closeLifecycle(context.Background(), lifecycleID, forensicsReason, 0)
					return errors.New(forensicsReason)
				}
			}
			if inst.IsTerminal() {
				terminalStrikes++
				log.Warn("primary provisioning: Vast reports terminal status",
					"instance_id", instanceID, "actual_status", inst.ActualStatus,
					"strike", terminalStrikes, "confirm_at", terminalConfirmStrikes)
				if terminalStrikes >= terminalConfirmStrikes {
					vastutil.BestEffortDestroy(ctx, r.deps.Vast, r.deps.Log, instanceID)
					_ = r.closeLifecycle(context.Background(), lifecycleID, "instance_terminal_state", 0)
					return errors.New("primary: instance terminal")
				}
				continue
			}
			// reset strikes on any non-terminal observation — Vast must report
			// terminal `terminalConfirmStrikes` times IN A ROW for the close.
			terminalStrikes = 0
			if inst.ActualStatus != "running" {
				continue
			}
			urls := r.buildPodURLs(inst)
			if urls.LLM == "" || urls.TTS == "" || urls.DCGM == "" {
				// Ports not fully mapped yet — keep polling. W6 fix parity.
				continue
			}
			// 3-endpoint health check inside ONE container's namespace
			// (Phase 11.1 supervisord 3-services: llm/tts/dcgm — embed
			// left the pod per D-03; stt left the pod per Phase 11.1 D-A4
			// Whisper delete). All 3 must pass.
			if r.deps.HealthCheck == nil {
				continue
			}
			if !r.deps.HealthCheck(ctx, urls.LLM) {
				continue
			}
			if !r.deps.HealthCheck(ctx, urls.TTS) {
				continue
			}
			if !r.deps.HealthCheck(ctx, urls.DCGM) {
				continue
			}
			if err := r.markReady(ctx, lifecycleID, urls, acceptedDPH, log); err != nil {
				log.Error("primary markReady failed", "lifecycle_id", lifecycleID, "err", err)
				return err
			}
			return nil
		}
	}
}

// buildPodURLs is the shared 4-URL builder consumed by waitForReadyOrDestroy
// (forward path) and recoverOpenLifecycle (restart-recovery path). Returns
// a primaryPodURLs value (not pointer) so callers can compare empty-string
// fields without nil-checking.
func (r *Reconciler) buildPodURLs(inst vast.Instance) primaryPodURLs {
	return primaryPodURLs{
		LLM:  r.podLLMURL(inst),
		TTS:  r.podTTSURL(inst),
		DCGM: r.podDCGMURL(inst),
	}
}

// recoverOpenLifecycle is the leader-recovery entry point invoked once at
// Start before the schedule loop and event subscriber begin (reviews #4).
//
// Branches per `primary_lifecycles WHERE ended_at IS NULL`:
//
//   - no row              → return nil (clean state)
//   - row, no instance ID → close('gateway_restart_orphan_no_instance')
//   - Vast says destroyed → close('gateway_restart_orphan')
//   - Vast running + 4 health probes pass → restore in-memory state +
//     OverrideTier0 3x + DCGMScraper.SetURL + FSM.SetState(Ready,
//     "restart_recovery")
//   - Vast running + ANY health probe fails → close('gateway_restart_
//     orphan_unhealthy')
func (r *Reconciler) recoverOpenLifecycle(ctx context.Context) error {
	q := r.queries()
	if q == nil {
		// No DB pool wired — test fixture; nothing to recover.
		return nil
	}
	open, err := q.GetOpenPrimaryLifecycle(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("primary recover: query open lifecycle: %w", err)
	}
	if !open.VastInstanceID.Valid {
		_ = q.ClosePrimaryLifecycle(ctx, gen.ClosePrimaryLifecycleParams{
			ID:             open.ID,
			ShutdownReason: pgtype.Text{String: "gateway_restart_orphan_no_instance", Valid: true},
			TotalCostBrl:   vastutil.PgNumericFromFloat(0),
			EventJson:      vastutil.MustEventJSON("lifecycle_close", map[string]any{"reason": "gateway_restart_orphan_no_instance"}),
		})
		return nil
	}
	if r.deps.Vast == nil {
		r.deps.Log.Warn("primary recover: no Vast.ai client; skipping recovery (next leader will retry)",
			"lifecycle_id", open.ID)
		return nil
	}
	inst, err := r.deps.Vast.GetInstance(ctx, open.VastInstanceID.Int64)
	if err != nil || inst.ActualStatus != "running" {
		r.deps.Log.Warn("primary recover: instance not running; closing as orphan",
			"lifecycle_id", open.ID,
			"instance_id", open.VastInstanceID.Int64,
			"err", err)
		_ = q.ClosePrimaryLifecycle(ctx, gen.ClosePrimaryLifecycleParams{
			ID:             open.ID,
			ShutdownReason: pgtype.Text{String: "gateway_restart_orphan", Valid: true},
			TotalCostBrl:   vastutil.PgNumericFromFloat(0),
			EventJson:      vastutil.MustEventJSON("lifecycle_close", map[string]any{"reason": "gateway_restart_orphan"}),
		})
		return nil
	}
	urls := r.buildPodURLs(inst)
	if urls.LLM == "" || urls.TTS == "" || urls.DCGM == "" {
		r.deps.Log.Warn("primary recover: pod ports not fully mapped; closing as unhealthy orphan",
			"lifecycle_id", open.ID)
		_ = q.ClosePrimaryLifecycle(ctx, gen.ClosePrimaryLifecycleParams{
			ID:             open.ID,
			ShutdownReason: pgtype.Text{String: "gateway_restart_orphan_unhealthy", Valid: true},
			TotalCostBrl:   vastutil.PgNumericFromFloat(0),
			EventJson:      vastutil.MustEventJSON("lifecycle_close", map[string]any{"reason": "gateway_restart_orphan_unhealthy"}),
		})
		return nil
	}
	if r.deps.HealthCheck == nil ||
		!r.deps.HealthCheck(ctx, urls.LLM) ||
		!r.deps.HealthCheck(ctx, urls.TTS) ||
		!r.deps.HealthCheck(ctx, urls.DCGM) {
		r.deps.Log.Warn("primary recover: 3-endpoint health check failed; closing as unhealthy orphan",
			"lifecycle_id", open.ID, "instance_id", open.VastInstanceID.Int64)
		_ = q.ClosePrimaryLifecycle(ctx, gen.ClosePrimaryLifecycleParams{
			ID:             open.ID,
			ShutdownReason: pgtype.Text{String: "gateway_restart_orphan_unhealthy", Valid: true},
			TotalCostBrl:   vastutil.PgNumericFromFloat(0),
			EventJson:      vastutil.MustEventJSON("lifecycle_close", map[string]any{"reason": "gateway_restart_orphan_unhealthy"}),
		})
		return nil
	}

	// Healthy! Rehydrate in-memory state.
	r.activeLifecycleID.Store(open.ID)
	r.activeInstanceID.Store(open.VastInstanceID.Int64)
	r.activePodURLs.Store(&urls)
	if r.deps.Loader != nil {
		r.deps.Loader.OverrideTier0("llm", stripPrimaryReadinessSuffix(urls.LLM))
		r.deps.Loader.OverrideTier0("tts", stripPrimaryReadinessSuffix(urls.TTS))
	}
	if r.deps.DCGMScraper != nil {
		r.deps.DCGMScraper.SetURL(urls.DCGM)
	}
	r.deps.FSM.SetState(StateReady, time.Now(), "restart_recovery")
	r.deps.Log.Info("primary recover: rehydrated active lifecycle",
		"lifecycle_id", open.ID,
		"instance_id", open.VastInstanceID.Int64)
	return nil
}

// queries returns the sqlc-generated query handle bound to the deps' DB
// pool. Returns nil when the pool is not wired (test fixture path) so
// callers can short-circuit without panic.
//
// Tests inject a fake DBTX via SetQueriesForTest to avoid needing a real
// *pgxpool.Pool — see reconciler_test.go fakeDBTX.
func (r *Reconciler) queries() *gen.Queries {
	if override := r.queriesOverride.Load(); override != nil {
		return override
	}
	if r.deps.DB == nil {
		return nil
	}
	return gen.New(r.deps.DB)
}

// SetQueriesForTest is the test-only injection point for the sqlc query
// handle. Production wires Deps.DB; tests build a fake DBTX → *gen.Queries
// and stash it here. Nil clears the override (production path resumes).
func (r *Reconciler) SetQueriesForTest(q *gen.Queries) {
	r.queriesOverride.Store(q)
}

// publishPrimaryEvent wraps redisx.PublishPrimaryEvent with the standard
// failure-log pattern. Best-effort: a publish failure is logged but does
// NOT block the FSM transition path — the in-process FSM is authoritative.
func (r *Reconciler) publishPrimaryEvent(ctx context.Context, ev redisx.PrimaryEvent, log *slog.Logger) {
	if r.deps.Redis == nil {
		return
	}
	if err := redisx.PublishPrimaryEvent(ctx, r.deps.Redis, ev); err != nil {
		log.Warn("primary publishPrimaryEvent failed", "type", ev.Type, "err", err)
	}
}

// IsLeader returns the cached leader-election state. Safe to call from
// any goroutine.
func (r *Reconciler) IsLeader() bool {
	return r.isLeader.Load()
}

// ReplicaID returns the per-replica identifier (deps.ReplicaID; defaults
// to os.Hostname() in NewReconcilerFull). Used by gatewayctl + Pub/Sub
// payload attribution.
func (r *Reconciler) ReplicaID() string {
	return r.deps.ReplicaID
}

// NewReconcilerFull is the production constructor that resolves
// defaults for ReplicaID + the schedule rule. Plan 06.6-08 main.go will
// call this; Plan 06.6-04's NewReconciler stays for the buildCreateRequest
// test fixture path.
func NewReconcilerFull(deps Deps) *Reconciler {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.ReplicaID == "" {
		host, _ := os.Hostname()
		if host == "" {
			host = "unknown"
		}
		deps.ReplicaID = host
	}
	r := &Reconciler{
		deps: deps,
		cfg:  deps.Cfg,
		rule: deps.Rule,
	}
	return r
}

// errReason returns a stable short token for the slog field. Used to
// classify provisioning errors at the FSM-transition reason level.
func errReason(err error) string {
	if err == nil {
		return "ok"
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled_in_flight"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, vast.ErrInstanceNotFound):
		return "instance_terminal_state"
	}
	msg := err.Error()
	if strings.HasPrefix(msg, "primary: cold-start budget") {
		return "health_timeout"
	}
	if strings.HasPrefix(msg, "vast_status_msg_error") {
		return "vast_status_msg_error"
	}
	if strings.Contains(msg, "no_offers_below_cap") || strings.Contains(msg, "no offers below cap") {
		return "no_offers_below_cap"
	}
	if strings.Contains(msg, "instance_terminal_state") || strings.Contains(msg, "instance terminal") {
		return "instance_terminal_state"
	}
	if strings.Contains(msg, "create_error") {
		return "create_error"
	}
	return "other"
}

// activeLifecycleSnapshot is a debug-only accessor for tests. Returns the
// (lifecycleID, instanceID) tuple of the currently-active lifecycle, both
// zero when none.
func (r *Reconciler) activeLifecycleSnapshot() (int64, int64) {
	return r.activeLifecycleID.Load(), r.activeInstanceID.Load()
}

// SetLastProvisionFailureAtForTest is the test-only setter for the
// cooldown gate's anchor timestamp. Production code populates this via
// the spawnProvisioning failure goroutine; the test helper lets unit
// tests drive the evaluateAsleep cooldown branch deterministically.
func (r *Reconciler) SetLastProvisionFailureAtForTest(t time.Time) {
	r.lastProvisionFailureAt.Store(&t)
}

// Static helpers used by reconciler_test.go scaffolding. _ = strconv keeps
// the import live for future Vast-port stringification — currently the
// reconciler reads HostPort as a string directly.
var _ = strconv.Atoi
var _ atomic.Int64
