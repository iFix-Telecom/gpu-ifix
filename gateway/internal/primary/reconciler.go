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
// ports (8000 LLM + 8001 STT + 8003 TTS + 9400 DCGM). All 4 endpoints
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

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/podconfig"
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

	// terminalConfirmStrikes is the number of CONSECUTIVE terminal/not-found
	// observations required before declaring a Vast instance dead (D-02). Vast
	// reports actual_status from a host polling agent that can transiently
	// report exited/offline during boot or eventual-consistency glitches;
	// 3 in a row (~15s at the Ready tick / provisioning poll cadence) filters
	// the flap. Shared by waitForReadyOrDestroy (provisioning poll) and
	// pollDeathOnReadyTick (Phase 12 Plan 02 Ready-tick death poll).
	terminalConfirmStrikes = 3
)

// primaryInstancePollIntervalForTest is the poll cadence waitForReadyOrDestroy
// actually uses. It defaults to the production primaryInstancePollInterval and
// is overridden ONLY in unit tests (6.6.Y-03 finding #7) to e.g. 5ms so the
// port-bind-timeout fail-fast test completes deterministically in well under a
// second instead of sleeping a real 5s tick + the 120s budget. Never mutated
// in production code paths.
var primaryInstancePollIntervalForTest = primaryInstancePollInterval

// primaryOnstartLogIntervalForTest is the sub-cadence at which
// waitForReadyOrDestroy fetches the Vast onstart log for the FF-02 download-
// stall detector. The Vast logs API is async (~seconds); fetching it on every
// ~5s poll tick would stretch the tick, so the fetch is throttled to ~30s.
// Overridden only in unit tests. Never mutated in production code paths.
var primaryOnstartLogIntervalForTest = 30 * time.Second

const (
	// allowlistCap bounds the auto-populated machine allowlist (FIFO, dedup):
	// the newest 20 known-good hosts. Decision #5 (20-CONTEXT.md).
	allowlistCap = 20
	// expectedWeightFiles is the number of MANDATORY weights the PRIMARY onstart
	// fetches (qwen + whisper + bge-m3 — onstart.go buildPrimaryOnstart runs 3
	// download_with_verify calls, each logging one `[download-weights] ok`).
	// Phase 21 removed chatterbox TTS (was the 4th mandatory download) → 4→3; a
	// stale 4 would keep FF-02 armed forever (okCount peaks at 3 < 4, never
	// disarms) and false-stall a healthy pod once the post-download bytes froze.
	// FF-02 disarms the download-stall detector only once all 3 have logged `ok`;
	// at 2 the qwen download (the slow ~18GB one) could still be in flight while
	// the 2 smaller files disarmed early. The optional 4th (jinja, only when
	// PRIMARY_QWEN_JINJA_KEY is set) pushes okCount to 4 >= 3 — still disarms.
	expectedWeightFiles = 3
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
	// Phase 12 Plan 02 (D-01): an operator force-up means the operator has
	// explicitly decided to re-provision NOW — they have added Vast credit (or
	// accepted the cost). Clear any active billing-stop suppression marker so it
	// does not silently re-fence the next schedule tick.
	r.clearBillingSuppression()
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
	// Phase 17 (POD-CFG-04): the DISABLED soak-gate reads the live snapshot's
	// schedule_disabled (falls back to boot cfg when podCfg unwired); the
	// provision decision uses the live re-parsed rule. Both come from the same
	// snapshot in production, so they stay consistent.
	if r.liveCfg().ScheduleDisabled {
		// Soak-gate posture; schedule-driven provisioning is off. Force
		// events bypass this gate via the event subscriber path.
		return
	}
	rule := r.liveRule()
	// Phase 12 Plan 02 (D-01): billing-stop suppression gate. A zero-credit
	// pod death armed a durable suppression marker (handleConfirmedDeath). While
	// it is active, SKIP re-provision — re-bidding a Vast pod with no account
	// credit would fail every attempt and burn the schedule loop in a
	// provision-fail loop during a peak window (Codex HIGH / Pitfall 5). This is
	// a checked FLAG, not retry machinery. The marker clears when the operator
	// force-ups (credit added) or a successful provision occurs. The distinct
	// billing-stop alert (Task 3) tells the operator to add credit.
	if r.billingSuppressionActive(now) {
		log.Warn("primary evaluateAsleep: billing-stop suppression active; awaiting operator credit (skipping re-provision)")
		return
	}
	if !rule.ShouldBeProvisioned(now) {
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
	// Phase 17 (POD-CFG-04): failure-cooldown is a per-tick gate (not provision-
	// captured) — read the LIVE snapshot so an owner edit applies immediately.
	cooldown := time.Duration(r.liveCfg().FailureCooldownS) * time.Second
	return now.Sub(*last) >= cooldown
}

// evaluateReady triggers drain when the schedule window closes. Uses
// ShouldStayUp — the lead-aware keep-up gate (== ShouldBeProvisioned), NOT
// the bare IsInPeak. The earlier IsInPeak gate disagreed with the provision
// gate during the pre-warm lead window [UpHour-lead, UpHour): evaluateAsleep
// provisions there (ShouldBeProvisioned=true) but IsInPeak is still false,
// so a pod that reached Ready inside that window was drained immediately and
// re-provisioned every tick — flapping create→destroy until UpHour
// (debug: primary-pod-flap-prewarm-window). Aligning the keep-up gate with
// the provision gate keeps a pre-warmed pod up through UpHour; after DownHour
// both gates report false so drain still fires cleanly at window exit.
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
	// a cutback leaves llm/stt/tts routing to the stale static row until the
	// next pod cycle. Re-assert any cleared dynamic slot here at 1Hz — the
	// inconsistency window is <=1s. embed is EXCLUDED (D-03 — static off-pod
	// row, immune to this vector). Phase 11.2 (D-B5′) restored "stt" here
	// after Phase 11.1 D-A4 had dropped it. This runs regardless of DISABLED
	// because an operator force-up pod is Ready under DISABLED and is just
	// as vulnerable to an emerg cutback clearing its slot.
	if urls := r.activePodURLs.Load(); urls != nil && r.deps.Loader != nil {
		for _, role := range []string{"llm", "stt"} {
			// SEED-019 part 3: gate the "stt" re-assert on the pod-reported
			// whisper_device. Only a GPU pod (device=="cuda") re-asserts stt;
			// for cpu/missing/unknown the pod's whisper is too slow (or absent)
			// so STT falls to the tier-1 cloud cascade (gemini-stt). llm
			// unaffected. (Phase 21: tts removed from the roster.)
			if role == "stt" && urls.WhisperDevice != "cuda" {
				continue
			}
			if _, set := r.deps.Loader.Tier0OverrideURL(role); !set {
				r.deps.Loader.OverrideTier0(role, stripPrimaryReadinessSuffix(roleURL(*urls, role)))
				log.Warn("primary re-asserted tier-0 override (emerg cleared it)", "role", role)
			}
		}
	}

	// Phase 12 Plan 02 (RES-11): Ready-tick death poll. Runs REGARDLESS of
	// DISABLED — a Ready pod under the soak gate is just as mortal as a
	// scheduled one (mirrors the Pitfall #11 re-assert above, which also runs
	// under DISABLED). FINDING 2: evaluateReady never polled Vast before this
	// plan, so a dead pod kept the FSM Ready for 25+ minutes pointing at a
	// dead address. The poll confirms death via a 3-strike confirm on both
	// IsTerminal() and ErrInstanceNotFound, repairs an empty trackedID from
	// the open lifecycle row (D-05), and on confirmed death drives the
	// drain/breaker/alert path (Task 2). Placed BEFORE the schedule drain
	// check so a confirmed death drains immediately even inside the peak
	// window (a dead pod must not wait for the window to close).
	if death := r.pollDeathOnReadyTick(ctx, log); death != nil {
		r.handleConfirmedDeath(ctx, *death, log)
		return
	}

	// Phase 17 (POD-CFG-04): DISABLED soak-gate from the live snapshot (UAT-14:
	// don't auto-drain an operator force-up pod when schedule disabled); the
	// keep-up decision uses the live re-parsed rule so an owner window edit
	// drains (or keeps up) the pod on the next Ready tick.
	if r.liveCfg().ScheduleDisabled {
		return
	}
	if !r.liveRule().ShouldStayUp(now) {
		r.startDrain(ctx, "schedule_window_exited", log)
		return
	}
	// Future hook: when all 3 breakers OPEN >60s, start drain for
	// "pod_unhealthy". Plan 06.6-06b owns the breaker observers; this
	// reconciler does not consume them directly.
}

// deathClassification is the result of a single Ready-tick death poll. dead is
// true only on a CONFIRMED death (3-strike threshold reached on one of the two
// terminal signals); cause is one of "billing_stopped" | "host_death" |
// "not_found" (the obs.PrimaryDeathDetectedTotal label set).
type deathClassification struct {
	dead  bool
	cause string
}

// pollDeathOnReadyTick polls Vast for the tracked instance once and updates the
// persisted strike counters. It returns a non-nil *deathClassification ONLY
// when a death is CONFIRMED (a strike counter reaches terminalConfirmStrikes);
// otherwise it returns nil (the caller keeps the FSM Ready).
//
// D-05 (the load-bearing prerequisite that defeated the 11-07 reproduction —
// Pitfall 1): when r.activeInstanceID is 0 but a pod is routing (activePodURLs
// set), the tracked id was lost on a force-up that never Stored it. Reconcile
// it from the open primary_lifecycles row (exactly as recoverOpenLifecycle
// does) BEFORE polling, so the death poll never silently no-ops on a lost id.
//
// The 3-strike confirm is ported from waitForReadyOrDestroy (reconciler.go
// terminalStrikes / notFoundStrikes), but here the counters live on the
// Reconciler struct (deathStrikeMu-guarded) because each Ready tick is a
// separate call — unlike the in-loop provisioning poll. Strikes reset on ANY
// healthy/non-terminal observation here AND on the Provisioning→Ready
// transition inside markReady (a fresh pod lifecycle starts with zero strikes).
func (r *Reconciler) pollDeathOnReadyTick(ctx context.Context, log *slog.Logger) *deathClassification {
	if r.deps.Vast == nil {
		return nil
	}
	instanceID := r.activeInstanceID.Load()
	if instanceID == 0 {
		// D-05 repair: a pod is routing but the tracked id was lost. Read the
		// open lifecycle row and Store the id, mirroring recoverOpenLifecycle.
		if r.activePodURLs.Load() == nil {
			return nil
		}
		q := r.queries()
		if q == nil {
			return nil
		}
		open, err := q.GetOpenPrimaryLifecycle(ctx)
		if err != nil || !open.VastInstanceID.Valid {
			return nil
		}
		instanceID = open.VastInstanceID.Int64
		r.activeInstanceID.Store(instanceID)
		log.Warn("primary death poll: repaired lost trackedID from open lifecycle row (D-05)",
			"lifecycle_id", open.ID, "vast_instance_id", instanceID)
	}

	inst, err := r.deps.Vast.GetInstance(ctx, instanceID)

	r.deathStrikeMu.Lock()
	defer r.deathStrikeMu.Unlock()

	if err != nil {
		if errors.Is(err, vast.ErrInstanceNotFound) {
			r.notFoundStrikes++
			log.Warn("primary death poll: Vast GET returned no_such_instance",
				"vast_instance_id", instanceID,
				"strike_count", r.notFoundStrikes, "confirm_at", terminalConfirmStrikes)
			if r.notFoundStrikes >= terminalConfirmStrikes {
				return &deathClassification{dead: true, cause: "not_found"}
			}
			return nil
		}
		// Transient non-not-found GET error — reset the not-found strike
		// counter so an unrelated flap mode does not accumulate strikes across
		// error classes (parity with waitForReadyOrDestroy).
		r.notFoundStrikes = 0
		return nil
	}
	// Healthy GET — reset the not-found counter (a single transient null
	// between healthy polls must not trip the 3-strike close).
	r.notFoundStrikes = 0
	if inst.IsTerminal() {
		r.terminalStrikes++
		log.Warn("primary death poll: Vast reports terminal status",
			"vast_instance_id", instanceID, "actual_status", inst.ActualStatus,
			"intended_status", inst.IntendedStatus,
			"strike", r.terminalStrikes, "confirm_at", terminalConfirmStrikes)
		if r.terminalStrikes >= terminalConfirmStrikes {
			return &deathClassification{dead: true, cause: classifyDeath(inst)}
		}
		return nil
	}
	// Non-terminal observation — reset the terminal strike counter (Vast must
	// report terminal terminalConfirmStrikes times IN A ROW to confirm).
	r.terminalStrikes = 0
	return nil
}

const (
	// deathForceOpenTTL is the TTL for the D-04 death force-OPEN of the local-*
	// breakers. Rationale: it must outlast drain+destroy AND span the schedule
	// loop's typical re-provision latency so the breaker stays OPEN (routing to
	// tier-1) until a fresh pod's markReady force-CLOSEs it via D-13 (Task 4).
	// 10 minutes comfortably covers a drain (grace ≤5min) + BestEffortDestroy +
	// a re-provision cold start; the D-13 short-TTL force-close on the new pod's
	// markReady overrides this open key (same key, last-writer-wins) before it
	// expires, so the TTL is a safety expiry, not the primary close mechanism.
	deathForceOpenTTL = 10 * time.Minute

	// markReadyForceCloseTTL is the TTL for the D-13 markReady force-CLOSE of
	// the local-* breakers. Rationale: it is SHORT (60s) because it only needs
	// to outlast the next probe cycle — the now-live pod probes CLOSED on its
	// own, so the force-close is a brief override that hands control back to
	// observation, NOT a long mask. Short enough that a genuinely-dead pod is
	// not pinned CLOSED past the next observation window; long enough to outlast
	// one probe cycle so the freshly-Ready pod is dispatched to immediately.
	// Contrast deathForceOpenTTL (10min) which HOLDS the dead address open until
	// re-provision.
	markReadyForceCloseTTL = 60 * time.Second

	// billingSuppressionWindow bounds the D-01 billing-stop suppression marker.
	// While active, evaluateAsleep SKIPS re-provision so a zero-credit pod death
	// does not enter a provision-fail loop. It is a generous safety expiry —
	// the marker is normally consumed earlier by an operator force-up (credit
	// added) or a successful provision (markReady), both of which clear it. Set
	// to outlast a full peak window so an operator who is asleep when credit
	// runs out does not get a provision-fail storm before they wake.
	billingSuppressionWindow = 6 * time.Hour
)

// localDeathBreakers is the set of tier-0 upstream breakers force-OPENed on a
// confirmed primary death (D-04) and force-CLOSEd on a new pod's markReady
// (D-13). These are the dispatcher upstream NAMES (not the dynamic role keys) —
// the request path keys breaker state on the upstream name.
var localDeathBreakers = []string{"local-llm", "local-stt"}

// handleConfirmedDeath is the death-confirmed action path (D-01/D-03/D-04). On a
// confirmed death from pollDeathOnReadyTick it:
//
//	(1) startDrain — Ready→Draining + RestoreTier0 the 3 dynamic slots; the
//	    existing Draining→Destroying→evaluateDestroying path then reaches
//	    BestEffortDestroy for free (D-01: no new destroy machinery).
//	(2) D-04 — deterministically force-OPEN the local-* breakers (long TTL)
//	    BEFORE the destroy completes, so requests stop hitting the dead address
//	    while observation would otherwise still be accumulating.
//	(3) D-01 — for a billing-stop, record a DURABLE suppression marker so
//	    evaluateAsleep SKIPS re-provision while active (no zero-credit
//	    provision-fail loop). Host-yank records NO marker (re-provisions
//	    naturally via the schedule loop).
//	(4) D-03 — publish a distinct, cause-tagged primary_death_confirmed event.
//	(5) Increment obs.PrimaryDeathDetectedTotal{cause}.
//
// No new FSM state is added — it reuses Ready→Draining→Asleep.
func (r *Reconciler) handleConfirmedDeath(ctx context.Context, death deathClassification, log *slog.Logger) {
	reason := "primary_instance_dead_" + death.cause
	log.Error("primary death confirmed on Ready tick",
		"cause", death.cause, "vast_instance_id", r.activeInstanceID.Load(),
		"lifecycle_id", r.activeLifecycleID.Load())

	// (2) D-04 force-OPEN the local-* breakers BEFORE startDrain reaches the
	// destroy path, eliminating the dead-address window. Best-effort: a Redis
	// error is logged but never blocks the drain (the FSM is authoritative).
	if r.deps.Redis != nil {
		setBy := "primary_death:" + r.deps.ReplicaID
		for _, name := range localDeathBreakers {
			if err := breaker.WriteForceOverride(ctx, r.deps.Redis, name, "open", deathForceOpenTTL, setBy); err != nil {
				log.Warn("primary death: force-open breaker failed", "breaker", name, "err", err)
			}
		}
	}

	// (1) startDrain advances Ready→Draining + RestoreTier0s the slots.
	r.startDrain(ctx, reason, log)

	// (3) D-01 billing-stop suppression marker. A billing-stop (IntendedStatus
	// ==stopped, or the A1 fallback ActualStatus==exited + StatusMsg credit/
	// account marker — see classifyDeath) records a durable suppression flag;
	// evaluateAsleep checks it and SKIPS re-provision while active. Host-yank
	// records NO marker so the schedule loop re-provisions naturally.
	// A1: billing signal = IntendedStatus==stopped (committed 11-06 evidence:
	// intended_status=stopped/actual_status=exited/balance −$0.056); fallback =
	// ActualStatus==exited && StatusMsg credit/account marker. This is a
	// suppression FLAG checked by the existing schedule evaluator, NOT new retry
	// machinery (D-01).
	if death.cause == "billing_stopped" {
		now := time.Now()
		r.billingSuppressedAt.Store(&now)
		log.Warn("primary death: billing-stop suppression marker armed; awaiting operator credit",
			"window", billingSuppressionWindow)
	}

	// (4) D-03 distinct cause-tagged death event.
	r.publishPrimaryEvent(ctx, redisx.PrimaryEvent{
		Type:        "primary_death_confirmed",
		State:       "draining",
		LifecycleID: r.activeLifecycleID.Load(),
		Reason:      death.cause, // "billing_stopped" | "host_death" | "not_found"
		SinceUnix:   time.Now().Unix(),
		ReplicaID:   r.deps.ReplicaID,
	}, log)

	// (5) metric.
	obs.PrimaryDeathDetectedTotal.WithLabelValues(death.cause).Inc()
}

// billingSuppressionActive reports whether a billing-stop suppression marker is
// currently active (set AND within billingSuppressionWindow). evaluateAsleep
// consults it to SKIP re-provision during a zero-credit window (D-01).
func (r *Reconciler) billingSuppressionActive(now time.Time) bool {
	marker := r.billingSuppressedAt.Load()
	if marker == nil {
		return false
	}
	return now.Sub(*marker) < billingSuppressionWindow
}

// clearBillingSuppression retires the suppression marker. Called when the
// operator force-ups (credit added) or a successful provision occurs (markReady)
// — the marker is naturally consumed.
func (r *Reconciler) clearBillingSuppression() {
	r.billingSuppressedAt.Store(nil)
}

// classifyDeath maps a confirmed-terminal Vast instance to a death cause for
// the obs.PrimaryDeathDetectedTotal label set and the alert title.
//
// A1 (RESOLVED from committed 11-06 evidence — intended_status=stopped,
// actual_status=exited, balance −$0.056; no live billing-stopped instance
// required): the PRIMARY billing signal is IntendedStatus=="stopped"; the
// FALLBACK is ActualStatus=="exited" && StatusMsg contains a credit/account
// marker (case-insensitive "credit"/"account"/"saldo"). Implementing BOTH
// ensures a missed IntendedStatus does not silently re-classify a billing-stop
// as host-yank (which would re-provision and burn bid attempts — Pitfall 5).
func classifyDeath(inst vast.Instance) string {
	if strings.EqualFold(strings.TrimSpace(inst.IntendedStatus), "stopped") {
		return "billing_stopped"
	}
	if strings.EqualFold(strings.TrimSpace(inst.ActualStatus), "exited") {
		msg := strings.ToLower(inst.StatusMsg)
		if strings.Contains(msg, "credit") || strings.Contains(msg, "account") || strings.Contains(msg, "saldo") {
			return "billing_stopped"
		}
	}
	return "host_death"
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
	// Phase 17 (POD-CFG-04): live grace window so an owner edit applies to the
	// in-progress drain's completion gate.
	grace := time.Duration(r.liveCfg().GraceRampDownS) * time.Second
	elapsed := now.Sub(*startedPtr)

	inflight := int64(0)
	if r.deps.Inflight != nil {
		// Sum the upstreams that actually live on the primary pod
		// (llama/speaches). embed is off-pod (D-03) and tts was removed
		// (Phase 21) — do not count them. The drain-complete gate holds open
		// until in-flight LLM+STT requests finish.
		inflight = r.deps.Inflight.Count("local-llm") +
			r.deps.Inflight.Count("local-stt")
	}

	if inflight == 0 || elapsed >= grace {
		log.Info("primary drain complete; transitioning to Destroying",
			"inflight", inflight, "elapsed_seconds", int64(elapsed.Seconds()),
			"grace_seconds", int64(grace.Seconds()))
		_ = r.deps.FSM.Transition(StateDraining, StateDestroying, now, "drain_complete")
	}
	// ctx is unused here but retained for signature parity with the sibling
	// evaluate* dispatchers (evaluateTick/Asleep/Ready/Destroying). Any future
	// DB/Redis call added to this drain path MUST thread ctx for cancellation.
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

	// RestoreTier0 for the dynamic roles (llm/stt — NOT embed D-03, NOT tts
	// Phase 21) BEFORE the FSM transition. New requests land on the fallback
	// chain immediately; in-flight ones drain over the grace window. Phase
	// 11.2 (D-B5′) restored "stt" after Phase 11.1 D-A4 had dropped it.
	if r.deps.Loader != nil {
		r.deps.Loader.RestoreTier0("llm")
		r.deps.Loader.RestoreTier0("stt")
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
// DB row healthy, stores activePodURLs, overrides tier-0 for 3 roles
// (llm+stt+tts post-Phase 11.2 D-B5′), points DCGM scraper at :9400/metrics,
// publishes primary_ready, and transitions Provisioning→Ready.
func (r *Reconciler) markReady(ctx context.Context, lifecycleID int64, urls primaryPodURLs, acceptedDPH float64, log *slog.Logger) error {
	if q := r.queries(); q != nil {
		eventJSON := vastutil.MustEventJSON("health_pass", map[string]any{
			"lifecycle_id": lifecycleID,
			"llm_url":      urls.LLM,
			"stt_url":      urls.STT,
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
		// Phase 11.2 (D-B5′): the dynamic primary roster is llm/stt/tts —
		// NOT embed (D-03, embed relocated to a 24/7 CPU host as a static
		// tier-0 row). Phase 11.1 D-A4 had dropped "stt" but Phase 11.2
		// restored it (Speaches/Whisper is back on the pod via
		// supervisord [program:speaches] on port 8001). We strip the
		// readiness suffix here so the dispatcher's ReverseProxy target is
		// the BASE URL (parity emerg markHealthy / stripHealthSuffix).
		r.deps.Loader.OverrideTier0("llm", stripPrimaryReadinessSuffix(urls.LLM))
		// SEED-019 part 3: gate the "stt" override on the pod-reported
		// whisper_device. device=="cuda" (pod whisper on GPU) → override; any
		// other value (cpu/missing/unknown, the fail-safe) → STT routes to the
		// tier-1 gemini-stt cascade instead of the pod. Replaces the deleted
		// manual STT-serve config flag.
		if urls.WhisperDevice == "cuda" {
			r.deps.Loader.OverrideTier0("stt", stripPrimaryReadinessSuffix(urls.STT))
		} else if urls.STT != "" {
			// WR-04: the pod health-passed (STT is up on the pod, urls.STT set)
			// yet we are NOT taking the tier-0 STT override because the reported
			// whisper_device is not "cuda". On a legitimately cpu/24GB shape this
			// is correct; but if the :9100 report lost its startup race (WR-01)
			// or transiently mis-read, this is a silent cost regression — we pay
			// the tier-1 gemini/groq/openai per-minute cost while a local STT
			// service runs idle on the pod. Log it so operators can distinguish a
			// chronically mis-read device report from an expected cpu shape.
			log.Warn("primary: STT service is up on pod but tier-0 STT override skipped (whisper_device != cuda) — STT routes to tier-1 cascade",
				"lifecycle_id", lifecycleID,
				"whisper_device", urls.WhisperDevice,
				"stt_url", urls.STT)
		}
		// Phase 21: no tts override — Chatterbox removed from the pod.
		// Refresh is intentionally NOT called here — the OverrideTier0 path
		// is atomic and Live; Refresh would re-scan the DB which is
		// orthogonal to this transition. Refresh remains available for
		// LISTEN/NOTIFY triggers (Plan 06.6-08 wiring).
	}
	if r.deps.DCGMScraper != nil {
		r.deps.DCGMScraper.SetURL(urls.DCGM)
	}
	// Phase 12 Plan 02 (RES-11, Gemini suggestion): reset the Ready-tick
	// death-poll strike counters on the Provisioning→Ready transition. Entering
	// Ready from Provisioning is a FRESH pod lifecycle — it must start with a
	// clean strike count so strikes carried from a prior (dead) pod's polling
	// do not falsely confirm a death on the new pod's first few ticks. This is
	// the enter-Ready reset; pollDeathOnReadyTick also resets on any
	// healthy/non-terminal observation.
	r.deathStrikeMu.Lock()
	r.terminalStrikes = 0
	r.notFoundStrikes = 0
	r.deathStrikeMu.Unlock()
	// Phase 12 Plan 02 (D-01): a successful provision naturally consumes any
	// active billing-stop suppression marker — the operator clearly has credit
	// again (this pod was created + reached Ready), so the schedule loop must be
	// free to re-provision on the next cycle.
	r.clearBillingSuppression()
	// Phase 12 Plan 02 (D-13): force-CLOSE the stale local-* breakers. This is
	// the symmetric counterpart to the D-04 death force-OPEN (Task 2). A pod
	// that just passed every provisioning probe is live by definition, so any
	// OPEN local-* breaker is a STALE leftover from probing the PREVIOUS dead
	// URL (Pitfall 4 / SEED-012) — left untouched it would keep traffic parked
	// on tier-1 even though the live pod is back. We force-CLOSE with a SHORT
	// TTL: it only needs to outlast the next probe cycle so the now-live
	// observation-driven breaker takes over the truth from natural probes. The
	// short close hands control back to observation quickly (vs the long death
	// open-TTL that HOLDS the dead address closed off until re-provision). Runs
	// AFTER the OverrideTier0 block above so the pod URL slot is set before the
	// breaker is reset. Best-effort — a Redis hiccup logs a warning but never
	// blocks the Provisioning→Ready transition (mirror publishPrimaryEvent's
	// nil-safe pattern). Loader.Refresh is intentionally NOT called (see the
	// OverrideTier0 comment above) — the force-close is orthogonal.
	if r.deps.Redis != nil {
		setBy := "primary_markready:" + r.deps.ReplicaID
		for _, name := range localDeathBreakers {
			if err := breaker.WriteForceOverride(ctx, r.deps.Redis, name, "closed", markReadyForceCloseTTL, setBy); err != nil {
				log.Warn("primary markReady: force-close breaker failed", "breaker", name, "err", err)
			}
		}
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
		// Phase 21: defensive 2-role RestoreTier0 (idempotent; tts removed).
		r.deps.Loader.RestoreTier0("llm")
		r.deps.Loader.RestoreTier0("stt")
	}
	if r.deps.DCGMScraper != nil {
		r.deps.DCGMScraper.SetURL("")
	}
	return nil
}

// calculatePrimaryCostBRL computes the realised cost of a primary
// lifecycle for the close-event payload. Returns 0 when the row's
// accepted_dph is NULL/<=0 (no real instance) OR no open lifecycle row is
// readable.
//
// DPH source = the ROW's accepted_dph, NOT the acceptedDPH param (fix #2).
// The normal close path evaluateDestroying calls
// closeLifecycle(..., "destroyed", 0) for schedule-driven shutdowns, so the
// param is 0 there — relying on it would zero the cost of EVERY scheduled
// pod and defeat the started_at fallback. The provisioning-failure
// call-sites also pass 0, but those rows never stamped accepted_dph either,
// so reading the row yields 0 and the early-return still holds. The param
// is therefore ignored (retained only for closeLifecycle signature parity).
//
// Cost basis = first_health_pass_at when present; if it is NULL (never
// stamped — e.g. the row is being closed before markReady wrote it), it
// falls back to started_at. The started_at basis OVER-counts the cold-start
// window (~5 min, bounded), keeping the persisted total_cost_brl consistent
// with the live accrual in the /admin/operations handler (which also
// approximates with started_at). Mirrors emerg.calculateCostBRL.
//
// Reads the row via GetOpenPrimaryLifecycle (the singleton open row — which
// at close time, before ClosePrimaryLifecycle stamps ended_at, IS the row
// identified by id) so the sqlc DBTX seam is unit-testable. Deps.DB is a
// concrete *pgxpool.Pool that cannot be faked directly, so the override-
// aware r.queries() handle is used instead. The id arg is retained for
// caller/signature parity and audit clarity.
func (r *Reconciler) calculatePrimaryCostBRL(ctx context.Context, id int64, acceptedDPH float64) float64 {
	q := r.queries()
	if q == nil {
		return 0
	}
	_ = id
	_ = acceptedDPH // fix #2: DPH comes from the row, not the param.
	row, err := q.GetOpenPrimaryLifecycle(ctx)
	if err != nil {
		return 0
	}
	dph := primaryNumericToFloat(row.AcceptedDph)
	if dph <= 0 {
		return 0
	}
	var basis time.Time
	switch {
	case row.FirstHealthPassAt.Valid:
		basis = row.FirstHealthPassAt.Time
	case !row.StartedAt.IsZero():
		basis = row.StartedAt
	default:
		return 0
	}
	hours := time.Since(basis).Hours()
	if hours < 0 {
		hours = 0
	}
	return dph * hours * r.deps.Cfg.USDToBRLRate
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

// bootHotCfg maps the 16 HOT pod_config fields out of the immutable boot
// config.Config. It is the disaster-recovery fallback consumed by liveCfg
// when podCfg is nil (unit tests / DB-less boot) so the reconciler never
// reads zero-config (Phase 17 D-02; T-17-10). STRUCTURAL fields (GPU name/
// num, images, weights, llama args, timezone) are NOT part of this view —
// they stay on r.cfg.
func bootHotCfg(cfg config.Config) podconfig.PodConfig {
	return podconfig.PodConfig{
		VastMachineBlocklist: cfg.PrimaryVastMachineBlocklist,
		VastMachineAllowlist: cfg.PrimaryVastMachineAllowlist,
		CapPrimary:           cfg.PrimaryVastPriceCapPrimary,
		CapFallback:          cfg.PrimaryVastPriceCapFallback,
		HostID:               cfg.PrimaryHostID,
		RejectPrivateIP:      cfg.PrimaryVastRejectPrivateIP,
		ColdStartBudgetS:     cfg.PrimaryProvisionColdStartBudgetSeconds,
		PortBindBudgetS:      cfg.PrimaryPublicPortBindBudgetSeconds,
		FailureCooldownS:     cfg.PrimaryProvisionFailureCooldownSeconds,
		MonthlyBudgetBRL:     cfg.MonthlyPrimaryBudgetBRL,
		ScheduleUpHour:       cfg.PrimaryPodScheduleUpHour,
		ScheduleDownHour:     cfg.PrimaryPodScheduleDownHour,
		ScheduleDays:         cfg.PrimaryPodScheduleDays,
		GraceRampDownS:       cfg.PrimaryPodScheduleGraceRampDownSeconds,
		ProvisionLeadS:       cfg.PrimaryPodScheduleProvisionLeadSeconds,
		ScheduleDisabled:     cfg.PrimaryPodScheduleDisabled,
	}
}

// liveCfg returns the live HOT pod_config snapshot (Phase 17 POD-CFG-04). It
// reads podCfg.Load() once — a lock-free atomic pointer load — and falls back
// to the boot config when podCfg is nil OR no snapshot has been swapped in yet
// (pre-first-refresh), so the reconciler never reads zero-config. The loader's
// own last-good-on-error invariant guarantees a non-nil snapshot stays live
// across a transient DB hiccup (T-17-04).
func (r *Reconciler) liveCfg() podconfig.PodConfig {
	if r.podCfg != nil {
		if s := r.podCfg.Load(); s != nil {
			return r.podCfg.Cfg()
		}
	}
	return bootHotCfg(r.cfg)
}

// currentProvisionCfg returns the HOT snapshot captured at the start of the
// in-flight provision (read ONCE at provisionLifecycle top), so the budget /
// reject-private-ip helpers downstream of offer selection observe a STABLE
// view even if an operator edits the snapshot mid-provision (T-17-09). Outside
// an active provision (e.g. direct test calls of waitForReadyOrDestroy) it
// falls back to liveCfg.
func (r *Reconciler) currentProvisionCfg() podconfig.PodConfig {
	if p := r.provisionCfg.Load(); p != nil {
		return *p
	}
	return r.liveCfg()
}

// liveRule returns the live, evaluable schedule rule (Phase 17 POD-CFG-04):
// re-parsed from the pod_config snapshot's 6 HOT schedule fields using the
// STRUCTURAL boot-resolved timezone, so an owner schedule edit reaches the
// NEXT tick (provision / drain) in seconds. Falls back to the immutable boot
// rule when podCfg is nil, no snapshot exists yet, or the re-parse errors
// (last-good — never evaluate a broken rule, T-17-06). The re-parse is a
// cheap pure-Go computation at 1Hz; LoadLocation never re-runs (tz structural).
func (r *Reconciler) liveRule() ScheduleRule {
	if r.podCfg != nil {
		if s := r.podCfg.Load(); s != nil {
			if rule, err := ParseScheduleFromSnapshot(r.podCfg.Cfg(), r.rule.Timezone); err == nil {
				return rule
			}
		}
	}
	return r.rule
}

// LiveRule exposes the live, evaluable schedule rule to read-only
// observers OUTSIDE this package (admin's GET /admin/operations schedule
// section — dashboard ops audit 2026-08-21). It delegates to liveRule and
// therefore shares its exact fallback semantics: pod_config snapshot
// re-parse first, immutable boot rule when the loader is nil, no snapshot
// exists yet, or the re-parse errors (last-good, T-17-06). LiveRule never
// errors. ScheduleRule is a value type — returning it by value hands the
// caller an independent copy safe to read from any goroutine.
func (r *Reconciler) LiveRule() ScheduleRule {
	return r.liveRule()
}

// provisionLifecycle runs SearchOffers → CreateInstance → waitForReady.
// gpuShapeLabel renders a human-readable "<num>x<name>" label for the
// shape index used by DefaultSearchFilters / the primary+fallback pair.
// Phase 11.1 WR-04: surfaced on offer-pick log events so operators can
// confirm whether the cheap primary or the wider fallback path served
// the lifecycle without parsing the numeric shape index.
func gpuShapeLabel(cfg config.Config, shape int) string {
	if shape == 0 {
		return fmt.Sprintf("%dx%s", cfg.PrimaryVastNumGPUsPrimary, cfg.PrimaryVastGPUNamePrimary)
	}
	return fmt.Sprintf("%dx%s", cfg.PrimaryVastNumGPUsFallback, cfg.PrimaryVastGPUNameFallback)
}

// Mirrors emerg.provisionLifecycle without the 3-attempt bid race retry
// (primary pods schedule at known peak hours; a transient bid race can be
// retried on the next tick after cooldown).
func (r *Reconciler) provisionLifecycle(ctx context.Context, lifecycleID int64, log *slog.Logger) error {
	if r.deps.Vast == nil {
		_ = r.closeLifecycle(ctx, lifecycleID, "no_vast_client", 0)
		return errors.New("primary: no Vast.ai client wired")
	}
	// Phase 17 (POD-CFG-04): read the LIVE pod_config snapshot ONCE at the top
	// of the provision and capture it for the rest of THIS attempt. HOT fields
	// (caps/blocklist/allowlist/host/reject-private-ip/budgets) come from the
	// snapshot so an owner dashboard edit changes the NEXT provision's filters;
	// an in-flight attempt keeps these captured values (T-17-09). STRUCTURAL
	// fields (GPU name/num) STILL come from r.cfg (D-02).
	hot := r.liveCfg()
	r.provisionCfg.Store(&hot)

	// quick-260702-nse cheapest-market provisioning policy. Count the newest
	// contiguous run of FAILED primary lifecycles. Attempts 1-2 (failStreak < 2)
	// go straight to the cheapest qualified open market; only from attempt 3+
	// (failStreak >= 2, two consecutive failures prove the cheap market is flaky
	// right now) does the reconciler prefer the known-good allowlisted hosts. A
	// count failure must NEVER block the pod coming up → default to failStreak 0
	// (market_cheapest) on any error / missing DB.
	var failStreak int64
	if q := r.queries(); q != nil {
		fs, ferr := q.CountConsecutiveFailedPrimaryProvisions(ctx)
		if ferr != nil {
			log.Warn("primary failstreak count failed; defaulting to market_cheapest", "err", ferr)
		} else {
			failStreak = fs
		}
	}
	mode := "market_cheapest"
	if failStreak >= 2 {
		mode = "allowlist_preferred"
	}

	// Phase 11.1 D-A6 (Wave 0 EVIDENCE-00): build a [primary, fallback]
	// SearchFilter pair and iterate — primary shape preferred (1×3090 @
	// $0.30), fallback shape only when the primary cap returns zero
	// qualified offers (2×3090 @ $0.60; same GPU model, deeper EU pool).
	filters := vast.DefaultSearchFilters(
		hot.CapPrimary, hot.CapFallback,
		hot.HostID,
		r.cfg.PrimaryVastGPUNamePrimary, r.cfg.PrimaryVastGPUNameFallback,
		r.cfg.PrimaryVastNumGPUsPrimary, r.cfg.PrimaryVastNumGPUsFallback,
		hot.VastMachineBlocklist...,
	)
	// shapeCaps mirrors the filter pair so vastutil.FilterBelowCap can
	// re-apply the per-shape ceiling client-side (epsilon-tolerant; UAT 17
	// 2026-05-19 Vast inventory race regression).
	shapeCaps := []float64{hot.CapPrimary, hot.CapFallback}

	// No geolocation restriction (operator decision 2026-05-21).
	// Machine allowlist (PRIMARY_VAST_MACHINE_ALLOWLIST): PREFERENCE pass
	// applied to BOTH shapes (Phase 11.1 WR-05) — the catalog currently
	// holds known-good 1×3090 hosts (primary shape) AND 2×3090 hosts
	// (fallback shape), so restricting the allowlist pass to filters[0]
	// would silently skip allowlisted fallback hosts. For each shape we
	// try allowlist-first, then broaden to the full qualified search, and
	// only iterate the next shape when both passes return no qualified
	// offers below the per-shape cap.
	var pickable []vast.Offer
	var pickedShape int

	// force_machine_id (Phase 999.2 — regime-1 UAT / ops pin): when set (>0),
	// bypass market_cheapest + the allowlist-preferred pass and provision EXACTLY
	// this Vast machine_id. Reuses the machine-allowlist filter with a list of one
	// over filters[0] (primary shape). Fails CLOSED if the pinned machine has no
	// current offer — never silently falls back to the market pick.
	if hot.ForceMachineID > 0 {
		forceFilter := vast.WithMachineAllowlist(filters[0], []int64{hot.ForceMachineID})
		offers, err := r.deps.Vast.SearchOffers(ctx, forceFilter)
		if err != nil {
			_ = r.closeLifecycle(ctx, lifecycleID, "search_failed", 0)
			return err
		}
		offers = r.rejectPrivateIPOffers(offers, log, 0, "force")
		if len(offers) == 0 {
			_ = r.closeLifecycle(ctx, lifecycleID, "forced_machine_no_offer", 0)
			return fmt.Errorf("primary: force_machine_id %d has no offer", hot.ForceMachineID)
		}
		pickable = offers
		pickedShape = 0
		log.Warn("primary offer FORCED by force_machine_id (bypasses market/cap)",
			"force_machine_id", hot.ForceMachineID, "offer_count", len(offers))
	}

	for i, f := range filters {
		if len(pickable) > 0 {
			break // force_machine_id already pinned the offer above
		}
		// Allowlist preference pass for this shape — quick-260702-nse: ONLY
		// when failStreak >= 2 (allowlist_preferred mode). At failStreak < 2
		// (market_cheapest) this block is skipped entirely and each shape goes
		// straight to the full qualified broaden search below.
		if failStreak >= 2 && len(hot.VastMachineAllowlist) > 0 {
			allowFilter := vast.WithMachineAllowlist(f, hot.VastMachineAllowlist)
			offers, err := r.deps.Vast.SearchOffers(ctx, allowFilter)
			if err != nil {
				_ = r.closeLifecycle(ctx, lifecycleID, "search_failed", 0)
				return err
			}
			offers = r.rejectPrivateIPOffers(offers, log, i, "allowlist")
			candidates := vastutil.FilterBelowCap(offers, shapeCaps[i])
			if len(candidates) > 0 {
				pickable = candidates
				pickedShape = i
				log.Info("primary offers found for shape (allowlist pass)",
					"shape", i, "offer_count", len(candidates),
					"cap", shapeCaps[i], "gpu_shape", gpuShapeLabel(r.cfg, i),
					"fail_streak", failStreak, "mode", mode)
				break
			}
			log.Info("primary allowlist exhausted for shape; broadening to full qualified search",
				"allowlist", hot.VastMachineAllowlist, "shape", i,
				"gpu_shape", gpuShapeLabel(r.cfg, i))
		}

		// Broaden to the full qualified search for this shape.
		offers, err := r.deps.Vast.SearchOffers(ctx, f)
		if err != nil {
			_ = r.closeLifecycle(ctx, lifecycleID, "search_failed", 0)
			return err
		}
		offers = r.rejectPrivateIPOffers(offers, log, i, "broaden")
		candidates := vastutil.FilterBelowCap(offers, shapeCaps[i])
		if len(candidates) > 0 {
			pickable = candidates
			pickedShape = i
			log.Info("primary offers found for shape",
				"shape", i, "offer_count", len(candidates),
				"cap", shapeCaps[i], "gpu_shape", gpuShapeLabel(r.cfg, i),
				"fail_streak", failStreak, "mode", mode)
			break
		}
		log.Info("primary shape returned no qualified offers; trying next",
			"shape", i, "cap", shapeCaps[i], "gpu_shape", gpuShapeLabel(r.cfg, i))
	}

	if len(pickable) == 0 {
		_ = r.closeLifecycle(ctx, lifecycleID, "no_offers_below_cap", 0)
		return errors.New("primary: no offers below cap (both shapes exhausted)")
	}
	offer := pickable[0]
	// Catalog the picked host so failures (e.g. broken-CDI multi-GPU machines)
	// can be added to PRIMARY_VAST_MACHINE_BLOCKLIST. machine_id correlates the
	// later terminal/CDI error (logged with instance_id) back to the host.
	// Phase 11.1 WR-04: propagate pickedShape (+ gpu_shape label) on the
	// offer-picked event so operators can confirm whether the cheap primary
	// or the wider fallback path served the lifecycle.
	log.Info("primary offer picked",
		"offer_id", offer.ID,
		"machine_id", offer.MachineID,
		"host_id", offer.HostID,
		"dph", offer.DphTotal,
		"geo", offer.Geolocation,
		"shape", pickedShape,
		"gpu_shape", gpuShapeLabel(r.cfg, pickedShape),
		"fail_streak", failStreak, "mode", mode)
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
			// Phase 11.1 WR-04: per-shape attribution in audit trail
			"shape":     pickedShape,
			"gpu_shape": gpuShapeLabel(r.cfg, pickedShape),
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
	reason, werr := r.waitForReadyOrDestroy(ctx, lifecycleID, instance.ID, offer.DphTotal, log)
	// BL-01/AL-01: maintain the machine block/allow lists from the outcome.
	// Best-effort — a list-write failure never changes werr (the provision's
	// result). failStreak is pre-attempt (the open row is excluded from the
	// count), so failStreak>=1 here means THIS failure is the 2nd consecutive.
	r.recordProvisionOutcome(ctx, offer.MachineID, failStreak, reason, werr, log)
	return werr
}

// waitForReadyOrDestroy polls GetInstance every primaryInstancePollInterval
// until either ALL 4 health endpoints pass (LLM + STT + TTS + DCGM) OR
// a terminal exit path fires.
//
// Reviews #11 status_msg gate: each poll iteration ALSO inspects
// inst.StatusMsg and aborts on a non-empty error substring. Carries the
// lifecycle-29 forensics fix from STATE.md.
//
// Wave 0 supervisord 4-services note: the 4 endpoints sit on 4 different
// container ports (8000/8001/8003/9400) but share the SAME container's
// network namespace. The reconciler does not need to know this — it polls
// 4 URLs via the Vast.ai-exposed host port mapping.
// Returns the close REASON string alongside the error so the caller's
// recordProvisionOutcome hook (BL-01) can classify machine-attributable
// failures. Success returns ("ready", nil).
// blockIPTTL bounds the Redis IP-blocklist entries. Rationale for a TTL (vs
// the permanent pod_config machine blocklist): the IP block is written on
// EVERY report-worthy fast-fail — including progress_stall_timeout, which
// Codex #9 flagged as potentially GLOBAL (a broken R2 stalls every host). A
// 24h expiry caps the blast radius of a global outage to one day of blocked
// IPs, while still covering the observed failure mode: one datacenter NAT
// (one public IP) fronting several Vast machine_ids, each a "new" machine to
// the selector (2026-07-16: machines 56883 + 59295 behind 120.238.149.205,
// both wasted a full download before dying).
const blockIPTTL = 24 * time.Hour

func blockIPKey(ip string) string { return "gw:primary:blockip:" + ip }

// blockIP records a bad host's public IP in Redis (TTL blockIPTTL).
// Best-effort: nil Redis / write error logs and moves on.
func (r *Reconciler) blockIP(ctx context.Context, ip, note string, log *slog.Logger) {
	if r.deps.Redis == nil || ip == "" {
		return
	}
	if err := r.deps.Redis.Set(ctx, blockIPKey(ip), note, blockIPTTL).Err(); err != nil {
		log.Warn("primary provisioning: IP blocklist write failed", "ip", ip, "err", err)
		return
	}
	log.Info("primary provisioning: IP blocklisted", "ip", ip, "ttl_h", 24, "note", note)
}

// ipBlocked reports whether ip is on the Redis IP blocklist. Fail-open: nil
// Redis / read error returns false (never blocks provisioning on Redis).
func (r *Reconciler) ipBlocked(ctx context.Context, ip string) bool {
	if r.deps.Redis == nil || ip == "" {
		return false
	}
	n, err := r.deps.Redis.Exists(ctx, blockIPKey(ip)).Result()
	return err == nil && n > 0
}

// bestEffortReportMachine files a Vast bad-machine report for a provisioning
// fast-fail AND blocklists the host's public IP (report ⇒ IP-block: every
// reason bad enough to tell Vast about is bad enough to avoid for 24h — the
// offer feed carries no IP, so the IP can only be learned here, post-create).
// MUST run BEFORE BestEffortDestroy — Vast rejects reports (403) once the
// account has no active instance on the machine. Best-effort: failures are
// logged, never block or reorder the close path. machineID==0 (instance never
// reported one) skips the report; ip=="" skips the IP block.
func (r *Reconciler) bestEffortReportMachine(ctx context.Context, machineID, instanceID int64, ip, problem, message string, log *slog.Logger) {
	r.blockIP(ctx, ip, fmt.Sprintf("machine=%d problem=%s", machineID, problem), log)
	if r.deps.Vast == nil || machineID == 0 {
		return
	}
	err := r.deps.Vast.ReportMachine(ctx, machineID, vast.ReportMachineRequest{
		InstanceID: instanceID,
		Problem:    problem,
		Message:    message,
	})
	if err != nil {
		log.Warn("primary provisioning: Vast machine report failed",
			"machine_id", machineID, "vast_instance_id", instanceID,
			"problem", problem, "err", err)
		return
	}
	log.Info("primary provisioning: bad machine reported to Vast",
		"machine_id", machineID, "vast_instance_id", instanceID, "problem", problem)
}

func (r *Reconciler) waitForReadyOrDestroy(ctx context.Context, lifecycleID, instanceID int64, acceptedDPH float64, log *slog.Logger) (string, error) {
	poll := time.NewTicker(primaryInstancePollIntervalForTest)
	defer poll.Stop()
	// Phase 17 (POD-CFG-04): cold-start + port-bind budgets come from the
	// provision-captured snapshot (stable for THIS attempt; an owner edit
	// applies to the next provision).
	hot := r.currentProvisionCfg()
	deadline := time.NewTimer(time.Duration(hot.ColdStartBudgetS) * time.Second)
	defer deadline.Stop()

	// Counter for consecutive IsTerminal observations. Vast.ai reports
	// `actual_status` from a polling agent on the host — during the boot
	// window the host may transiently report `exited` / `offline` while
	// the container is starting (image extract, supervisord launching).
	// Require 3 consecutive terminal observations (~30s) before declaring
	// the instance dead. UAT 2026-05-18 lifecycle 4 captured a false-positive
	// terminal close 12s before 4 endpoints were actually reachable.
	terminalStrikes := 0

	// Counter for consecutive docker-pull "Retrying" observations (bad-host
	// detection, backlog primary-provisioning-bad-host-detection-todo). A host
	// whose layer pull keeps retrying (bad registry route) reports
	// status_msg="<layer>: Retrying in N second(s)" and otherwise rides the
	// full coldstart budget. Same 3-strike confirm as terminalStrikes: a
	// one-off retry can recover, so require terminalConfirmStrikes in a row.
	pullRetryStrikes := 0

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

	// Option B (plan 6.6.Y-03 + CR-01 6.6.Y review): port-bind fail-fast. The
	// 6.6.Y-01 spike (n=2) confirmed hosts that reach actual_status=running
	// with a POPULATED Vast ports map yet stay externally UNREACHABLE (TCP
	// probes from two vantage points timed out) for 40+ min while the gateway
	// silent-waited the full cold-start budget.
	//
	// firstRunningAt anchors the budget at the FIRST running observation (not
	// lifecycle start). Two distinct catches share that anchor:
	//
	//   1. empty-URLs branch — pod reports running but buildPodURLs yields an
	//      empty URL (Vast ports map empty). The narrower subclass; neither
	//      OBSERVED failure exhibited it, but it is a cheap belt-and-braces.
	//
	//   2. TCP-unreachable branch (CR-01) — pod reports running, buildPodURLs
	//      yields 4 NON-empty URLs (populated ports map), yet a connection-
	//      level dial to the LLM host:port fails (timeout / no route). This
	//      is the ACTUAL observed failure signature the empty-URLs gate could
	//      never catch, because buildPodURLs IS the Vast ports map the spike
	//      DIRECTIVE deemed an unreliable readiness signal.
	//
	// Both branches BestEffortDestroy + close with closure_reason
	// public_port_bind_timeout once contiguous running time exceeds
	// PrimaryPublicPortBindBudgetSeconds. CRITICAL distinction (spike + design
	// note): the gate keys on CONNECTION-LEVEL failure (no TCP response at
	// all), NOT on a not-ready HTTP response. A host that is TCP-reachable but
	// whose services are still warming (HealthCheck failing while onstart
	// downloads weights) is a LEGITIMATE cold start and must keep polling
	// under the cold-start budget — Reachable() returns true for connect /
	// connection-refused and false only for dial timeout / no route.
	var firstRunningAt time.Time
	portBindBudget := time.Duration(hot.PortBindBudgetS) * time.Second

	// FF-01 (regime 1 — host morto): a pod stuck in actual_status=created /
	// scheduling (the pre-loading handshake window, vast/types.go IsActive) with
	// a mute onstart never advances to loading. firstCreatedAt anchors the
	// created-budget at the FIRST created/scheduling observation; once elapsed
	// exceeds createdBudget the pod is destroyed with created_state_timeout.
	// CRITICAL (regime 1 vs 2, 20-CONTEXT.md:15-16): the anchor RESETS the moment
	// actual_status advances to loading — a created→loading transition is a
	// HEALTHY image pull (regime 2, "Pulling" 15min) that must RIDE to the
	// coldstart_budget_s ceiling, never be killed by this budget. Anchoring on
	// the first created poll then firing only on a SUBSEQUENT created poll makes
	// the reset win: a single created observation followed by loading never fires.
	var firstCreatedAt time.Time
	createdBudget := time.Duration(hot.CreatedBudgetS) * time.Second

	// FF-02 (regime 3 — download stall): the weights download runs INSIDE the
	// onstart script, BEFORE docker compose / the health-bridge exist, so it is
	// invisible to actual_status. The only signal is the onstart log (fetched via
	// the Vast logs API on a slow sub-cadence — the API is async ~seconds). The
	// stall is defined as dest-file BYTES provably frozen while telemetry is
	// Available, scoped to the download phase (arm on `fetching`, disarm once all
	// files log `ok`). Telemetry-unavailable (NotReady/FetchError/Empty) is
	// UNKNOWN → ride to the coldstart ceiling, never a false kill (Codex #3).
	var downloadArmed bool
	var downloadDone bool
	var downloadDoneAt time.Time // when downloadDone flipped true — port-bind anchor
	var maxBytes int64           // highest total dest bytes seen while armed
	var lastProgressAt time.Time // anchor of the last byte ADVANCE
	var lastLogFetchAt time.Time // sub-cadence gate
	progressStallBudget := time.Duration(hot.ProgressStallBudgetS) * time.Second

	// Machine id from the last healthy GetInstance poll — the deadline
	// (health_timeout) case fires with no inst in scope, but should still
	// report the machine to Vast before destroying. (No IP tracking: the
	// deadline deliberately does NOT IP-block — see the deadline case.)
	var lastMachineID int64

	// ipChecked gates the one-shot IP-blocklist check: runs on the FIRST poll
	// whose public_ipaddr is non-empty (Vast populates it before actual_status
	// on some hosts). A blocked IP means another machine behind the same NAT
	// already failed a report-worthy fast-fail within blockIPTTL — kill in
	// seconds instead of wasting a full download on the same broken link.
	var ipChecked bool

	for {
		select {
		case <-ctx.Done():
			// No machine report: a cancelled provision (shutdown/leader change)
			// says nothing about the host's health.
			vastutil.BestEffortDestroy(ctx, r.deps.Vast, r.deps.Log, instanceID)
			_ = r.closeLifecycle(context.Background(), lifecycleID, "cancelled_in_flight", 0)
			return "cancelled_in_flight", ctx.Err()
		case <-deadline.C:
			// ip="" — health_timeout REPORTS but does NOT IP-block: a blown
			// coldstart budget is ambiguous (slow-but-legit host, transient
			// congestion), and banning the IP for 24h would break the designed
			// retry-on-same-host path (observed: the supervisord autorestart
			// integration test loops health_timeout → blocklisted_ip forever).
			// Only unambiguous host defects (pull stall, created stuck, byte
			// freeze, port bind, status_msg error) block the IP.
			r.bestEffortReportMachine(ctx, lastMachineID, instanceID, "", vast.ReportProblemTooLongToLoad,
				fmt.Sprintf("Automated report: instance never became healthy within the %ds cold-start budget (all endpoints unreachable or unhealthy for the whole window).", hot.ColdStartBudgetS), log)
			vastutil.BestEffortDestroy(ctx, r.deps.Vast, r.deps.Log, instanceID)
			_ = r.closeLifecycle(context.Background(), lifecycleID, "health_timeout", 0)
			return "health_timeout", errors.New("primary: cold-start budget exhausted")
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
						return "instance_terminal_state_confirmed", errors.New("primary: instance terminal (3-strike confirm via ErrInstanceNotFound)")
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
			// Track the machine id for the deadline (health_timeout) report —
			// the deadline case fires outside the poll body, with no inst in
			// scope.
			if inst.MachineID != 0 {
				lastMachineID = inst.MachineID
			}
			// IP-blocklist gate (one-shot, first poll with a populated IP): a
			// sibling machine behind this NAT already burned a lifecycle on a
			// report-worthy defect within blockIPTTL. Fail in seconds. No Vast
			// report (no NEW defect evidence — guilt by shared link); the
			// blocklisted_ip reason IS machine-attributable, so
			// recordProvisionOutcome learns this machine_id into the DB
			// blocklist (the IP→machine_id association the selector cannot see).
			if !ipChecked && inst.PublicIPAddr != "" {
				ipChecked = true
				if r.ipBlocked(ctx, inst.PublicIPAddr) {
					log.Warn("primary provisioning: public IP is blocklisted; failing fast",
						"lifecycle_id", lifecycleID,
						"vast_instance_id", instanceID,
						"machine_id", inst.MachineID,
						"public_ipaddr", inst.PublicIPAddr)
					vastutil.BestEffortDestroy(ctx, r.deps.Vast, r.deps.Log, instanceID)
					_ = r.closeLifecycle(context.Background(), lifecycleID, "blocklisted_ip", 0)
					return "blocklisted_ip", errors.New("primary: host public IP is blocklisted")
				}
			}
			// Reviews #11 — Vast `status_msg` early-abort.
			msg := strings.TrimSpace(inst.StatusMsg)
			lower := strings.ToLower(msg)
			if msg != "" && strings.Contains(lower, "error") {
				// Truncate to 200 chars to keep the forensic event bounded.
				trunc := msg
				if len(trunc) > 200 {
					trunc = trunc[:200]
				}
				forensicsReason := "vast_status_msg_error:" + trunc
				log.Error("primary provisioning: Vast reported instance error",
					"instance_id", instanceID, "status_msg", msg)
				r.bestEffortReportMachine(ctx, inst.MachineID, instanceID, inst.PublicIPAddr, vast.ReportProblemUnableToStart,
					"Automated report: Vast status_msg reported an error during provisioning: "+trunc, log)
				vastutil.BestEffortDestroy(ctx, r.deps.Vast, r.deps.Log, instanceID)
				_ = r.closeLifecycle(context.Background(), lifecycleID, forensicsReason, 0)
				return forensicsReason, errors.New(forensicsReason)
			}
			// Pull-stall fast-fail: a host retrying a docker layer pull has a
			// bad registry route. A healthy pull logs Pulling/Downloading/
			// Extracting, never Retrying — so 3 consecutive Retrying polls (~15s)
			// confirm a stuck host without false-killing a one-off retry that
			// recovers. Reset on any poll that is not retrying (including empty
			// status_msg once the pull advances phase).
			if strings.Contains(lower, "retrying") {
				pullRetryStrikes++
				log.Warn("primary provisioning: Vast pull retrying (bad registry route)",
					"instance_id", instanceID, "status_msg", msg,
					"strike", pullRetryStrikes, "confirm_at", terminalConfirmStrikes)
				if pullRetryStrikes >= terminalConfirmStrikes {
					r.bestEffortReportMachine(ctx, inst.MachineID, instanceID, inst.PublicIPAddr, vast.ReportProblemUnableToStart,
						"Automated report: docker image pull stuck in a retry loop (status_msg: "+msg+"); host appears to have a broken route to the registry.", log)
					vastutil.BestEffortDestroy(ctx, r.deps.Vast, r.deps.Log, instanceID)
					_ = r.closeLifecycle(context.Background(), lifecycleID, "vast_pull_retry_stall", 0)
					return "vast_pull_retry_stall", errors.New("primary: docker pull retry stall")
				}
				continue
			}
			pullRetryStrikes = 0
			// intended_status=stopped is the reliable billing/host-stop signal
			// (classifyDeath A1). It can appear while actual_status is still
			// non-terminal or blank (Vast stopped the container but the agent
			// never reports exited) — the case IsTerminal() misses, which then
			// rides the full coldstart budget. Fold it into the same 3-strike
			// terminal path so provisioning fast-fails and the schedule loop
			// re-bids a different host.
			billingStopped := strings.EqualFold(strings.TrimSpace(inst.IntendedStatus), "stopped")
			if inst.IsTerminal() || billingStopped {
				terminalStrikes++
				log.Warn("primary provisioning: Vast reports terminal/stopped status",
					"instance_id", instanceID, "actual_status", inst.ActualStatus,
					"intended_status", inst.IntendedStatus,
					"strike", terminalStrikes, "confirm_at", terminalConfirmStrikes)
				if terminalStrikes >= terminalConfirmStrikes {
					// No machine report here: terminal/stopped is ambiguous —
					// intended=stopped may be OUR billing stop (zero credit),
					// not the host's fault. Only unambiguous host defects
					// (pull stall, created stuck, port bind, timeouts) report.
					vastutil.BestEffortDestroy(ctx, r.deps.Vast, r.deps.Log, instanceID)
					_ = r.closeLifecycle(context.Background(), lifecycleID, "instance_terminal_state", 0)
					return "instance_terminal_state", errors.New("primary: instance terminal")
				}
				continue
			}
			// reset strikes on any non-terminal observation — Vast must report
			// terminal `terminalConfirmStrikes` times IN A ROW for the close.
			terminalStrikes = 0

			// FF-02: download-phase byte-progress stall detector. Runs on the slow
			// onstart-log sub-cadence (not every poll — the Vast logs API is async).
			// ponytail: the 30s sub-cadence throttles the async fetch so it never
			// stretches the 5s poll; telemetry-unavailable rides to the ceiling —
			// the deferred SSH-tail leg (FF-03 b) is what would fast-fail regime 3
			// during a Vast-logs outage.
			if r.deps.Vast != nil && !downloadDone && time.Since(lastLogFetchAt) >= primaryOnstartLogIntervalForTest {
				lastLogFetchAt = time.Now()
				res, _ := r.deps.Vast.OnstartLog(ctx, instanceID)
				if res.Status == vast.OnstartLogAvailable {
					fetching, okCount, totalBytes := parseDownloadProgress(res.Text)
					switch {
					case fetching && !downloadArmed:
						// Arm the download phase on the first `fetching` line.
						downloadArmed = true
						maxBytes = totalBytes
						lastProgressAt = time.Now()
					case downloadArmed && okCount >= expectedWeightFiles:
						// All files logged `ok` — disarm; a healthy slow startup
						// after the download must never trip the stall detector.
						// downloadDoneAt anchors the port-bind budget (Task 20-07):
						// the tight budget counts the post-download service-bind
						// window, not the ~20GB download that precedes the bind.
						downloadDone = true
						downloadDoneAt = time.Now()
					case downloadArmed && totalBytes > maxBytes:
						// Bytes advanced — reset the stall anchor.
						maxBytes = totalBytes
						lastProgressAt = time.Now()
					case downloadArmed && time.Since(lastProgressAt) >= progressStallBudget:
						// Bytes provably frozen past budget while Available → stall.
						log.Warn("primary provisioning: download bytes frozen (regime 3 stall)",
							"lifecycle_id", lifecycleID,
							"vast_instance_id", instanceID,
							"max_bytes", maxBytes,
							"stall_s", int(time.Since(lastProgressAt).Seconds()),
							"budget_s", hot.ProgressStallBudgetS)
						r.bestEffortReportMachine(ctx, inst.MachineID, instanceID, inst.PublicIPAddr, vast.ReportProblemTooLongToLoad,
							fmt.Sprintf("Automated report: weights download bytes frozen for %ds (no progress past %d bytes) — host network stalled mid-download.", int(time.Since(lastProgressAt).Seconds()), maxBytes), log)
						vastutil.BestEffortDestroy(ctx, r.deps.Vast, r.deps.Log, instanceID)
						_ = r.closeLifecycle(context.Background(), lifecycleID, "progress_stall_timeout", 0)
						return "progress_stall_timeout", errors.New("primary: download progress stalled (regime 3)")
					}
				}
				// NotReady / FetchError / Empty → UNKNOWN: skip non-fatally.
			}

			if inst.ActualStatus != "running" {
				// Reset the port-bind budget anchor: it tracks ONLY contiguous
				// running time (consistent with how terminalStrikes resets), so
				// a flap back below running does not burn the 120s budget.
				firstRunningAt = time.Time{}
				// FF-01: created/scheduling is the regime-1 window. Anchor on the
				// first such poll, fire on a subsequent one — a created→loading
				// transition (regime 2) resets the anchor in the else branch below
				// and rides to the coldstart ceiling.
				//
				// A BLANK actual_status is the same regime: observed live 2026-07-16
				// (lifecycle 190, machine 54626) — the host never picked up the
				// container, Vast kept actual_status null for 10+ min and the pod
				// rode toward the coldstart ceiling because "" matched neither
				// created nor loading. Blank in the first seconds after create is
				// normal; the created-budget anchor absorbs that.
				if inst.ActualStatus == "created" || inst.ActualStatus == "scheduling" || inst.ActualStatus == "" {
					if firstCreatedAt.IsZero() {
						firstCreatedAt = time.Now()
						continue
					}
					elapsed := time.Since(firstCreatedAt)
					log.Warn("primary provisioning: stuck in created/scheduling",
						"lifecycle_id", lifecycleID,
						"vast_instance_id", instanceID,
						"actual_status", inst.ActualStatus,
						"elapsed_since_created_s", int(elapsed.Seconds()),
						"budget_s", hot.CreatedBudgetS)
					// `>=` (not `>`) so a budget of 0 fires on the first post-anchor
					// created poll — deterministic timeout test (mirrors port-bind).
					if elapsed >= createdBudget {
						r.bestEffortReportMachine(ctx, inst.MachineID, instanceID, inst.PublicIPAddr, vast.ReportProblemUnableToStart,
							fmt.Sprintf("Automated report: instance stuck in created/blank state (%q) for %ds without ever advancing to loading — host never picked up the container.", inst.ActualStatus, int(elapsed.Seconds())), log)
						vastutil.BestEffortDestroy(ctx, r.deps.Vast, r.deps.Log, instanceID)
						_ = r.closeLifecycle(context.Background(), lifecycleID, "created_state_timeout", 0)
						return "created_state_timeout", errors.New("primary: created-state budget exhausted (host morto)")
					}
					continue
				}
				// Any other non-running status (loading) — regime 2 image pull.
				// Reset the created anchor so the created-budget never fires for
				// this lifecycle; it rides the coldstart_budget_s ceiling.
				firstCreatedAt = time.Time{}
				continue
			}
			// First running observation: anchor the port-bind budget here, and
			// clear the created anchor (the created window is behind us).
			if firstRunningAt.IsZero() {
				firstRunningAt = time.Now()
			}
			firstCreatedAt = time.Time{}
			// ponytail: three-way port-bind anchor (Task 20-07, live evidence
			// lifecycle 137). A fresh coldstart downloads ~20GB of weights INSIDE
			// onstart — AFTER actual_status=running but BEFORE llama-server binds
			// its public port — so anchoring the tight port_bind_budget_s (seed
			// 120s) on firstRunningAt false-kills a healthy download (the download
			// alone exceeds 120s). Trade-off by regime:
			//   - download armed & not done: SKIP the timeout (enforce=false) — the
			//     download provably owns this window; FF-02 (byte-stall) + the
			//     absolute coldstart_budget_s backstop guard it. Warn still logs.
			//   - downloadDone: measure from downloadDoneAt so the tight budget
			//     counts only the post-download service-bind window (as intended).
			//   - never armed (telemetry unavailable — OnstartLog FetchError/Empty/
			//     NotReady): fall back to the legacy firstRunningAt anchor so a
			//     genuinely no-signal pod still fast-fails on port bind.
			portBindElapsed := func() (elapsed time.Duration, enforce bool) {
				switch {
				case downloadArmed && !downloadDone:
					return time.Since(firstRunningAt), false
				case downloadDone:
					return time.Since(downloadDoneAt), true
				default:
					return time.Since(firstRunningAt), true
				}
			}
			urls := r.buildPodURLs(inst)
			if urls.LLM == "" || urls.STT == "" || urls.DCGM == "" {
				// Option B (plan 6.6.Y-03): previously a SILENT continue. The
				// 6.6.Y-01 spike confirmed this exact branch swallowed a 17-min
				// (40+ min observed) wait with zero operator-visible logs. Emit
				// a per-poll Warn carrying the forensic fields, THEN fail fast
				// once contiguous running time exceeds the bind budget.
				elapsed, enforce := portBindElapsed()
				log.Warn("primary provisioning: running but public ports not bound",
					"lifecycle_id", lifecycleID,
					"vast_instance_id", instanceID,
					"actual_status", inst.ActualStatus,
					"ssh_host", inst.SshHost,
					"public_ipaddr", inst.PublicIPAddr,
					"elapsed_since_running_s", int(elapsed.Seconds()),
					"download_in_flight", downloadArmed && !downloadDone,
					"budget_s", hot.PortBindBudgetS)
				// `>=` (not `>`) so a budget of 0 fires on the first running
				// poll — required for the deterministic timeout test (finding #7).
				// enforce=false while the download is in flight — no false kill.
				if enforce && elapsed >= portBindBudget {
					r.bestEffortReportMachine(ctx, inst.MachineID, instanceID, inst.PublicIPAddr, vast.ReportProblemPortIssues,
						fmt.Sprintf("Automated report: instance running for %ds but Vast never published public port mappings.", int(elapsed.Seconds())), log)
					vastutil.BestEffortDestroy(ctx, r.deps.Vast, r.deps.Log, instanceID)
					_ = r.closeLifecycle(context.Background(), lifecycleID, "public_port_bind_timeout", 0)
					return "public_port_bind_timeout", errors.New("primary: public port bind timeout")
				}
				continue
			}
			// Option B primary catch (CR-01 6.6.Y review): URLs are now
			// non-empty (Vast ports map populated) — but the spike proved this
			// is NOT a reachability guarantee. Probe the LLM host:port at the
			// CONNECTION level. A dial timeout / no-route (Reachable == false)
			// is the observed failure signature: running + published ports yet
			// externally unreachable. Distinguished from a host that is TCP-
			// reachable but not-yet-ready (Reachable == true, HealthCheck
			// below still failing) — that is a legitimate slow cold start and
			// must keep polling under the cold-start budget, NOT be killed.
			// Skipped entirely when no Reachable probe is wired (nil) so unit
			// tests / minimal Deps never false-positive destroy.
			if r.deps.Reachable != nil && !r.deps.Reachable(ctx, urls.LLM) {
				elapsed, enforce := portBindElapsed()
				log.Warn("primary provisioning: running, ports published, host TCP-unreachable",
					"lifecycle_id", lifecycleID,
					"vast_instance_id", instanceID,
					"actual_status", inst.ActualStatus,
					"ssh_host", inst.SshHost,
					"public_ipaddr", inst.PublicIPAddr,
					"llm_url", urls.LLM,
					"elapsed_since_running_s", int(elapsed.Seconds()),
					"download_in_flight", downloadArmed && !downloadDone,
					"budget_s", hot.PortBindBudgetS)
				// `>=` (not `>`) so a budget of 0 fires on the first running
				// poll — required for the deterministic timeout test.
				// enforce=false while the download is in flight — no false kill.
				if enforce && elapsed >= portBindBudget {
					r.bestEffortReportMachine(ctx, inst.MachineID, instanceID, inst.PublicIPAddr, vast.ReportProblemPortIssues,
						fmt.Sprintf("Automated report: instance running with published ports but host is TCP-unreachable from outside for %ds (dial timeout / no route).", int(elapsed.Seconds())), log)
					vastutil.BestEffortDestroy(ctx, r.deps.Vast, r.deps.Log, instanceID)
					_ = r.closeLifecycle(context.Background(), lifecycleID, "public_port_bind_timeout", 0)
					return "public_port_bind_timeout", errors.New("primary: public port bind timeout")
				}
				continue
			}
			// 3-endpoint health check inside ONE container's namespace
			// (Phase 21 supervisord 3-services: llm/stt/dcgm — embed left the
			// pod per D-03, tts/chatterbox removed Phase 21). All 3 must pass.
			if r.deps.HealthCheck == nil {
				continue
			}
			if !r.deps.HealthCheck(ctx, urls.LLM) {
				continue
			}
			if !r.deps.HealthCheck(ctx, urls.STT) {
				continue
			}
			if !r.deps.HealthCheck(ctx, urls.DCGM) {
				continue
			}
			if err := r.markReady(ctx, lifecycleID, urls, acceptedDPH, log); err != nil {
				log.Error("primary markReady failed", "lifecycle_id", lifecycleID, "err", err)
				return "", err
			}
			return "ready", nil
		}
	}
}

// rejectPrivateIPOffers (Option A, plan 6.6.Y-03) applies the client-side
// RFC1918 reject to a freshly-searched offer slice BEFORE FilterBelowCap,
// guarded by cfg.PrimaryVastRejectPrivateIP (default true). When any offers
// are dropped it emits a log.Info so operators see the RFC1918 advertisers
// being filtered at pick stage. Mirrors the FilterBelowCap client-side
// filtering idiom exactly — there is NO Vast search-filter-map clause for
// public_ipaddr (no clean comparator; review finding #4).
//
// SPIKE-EVIDENCE (6.6.Y-01-SPIKE-EVIDENCE.md §"Answer Q2"): offers DO carry a
// non-empty public_ipaddr at offer time (55/55 in the live sample), so this
// reject is EFFECTIVE pre-provision — an RFC1918 advertiser (iter-1 root cause:
// public_ipaddr=192.168.1.8) is dropped before any pod is created. CAVEAT
// (verbatim from the spike): neither OBSERVED failing host advertised an
// RFC1918 IP — both carried public routable IPs and timed out anyway. Option A
// alone would NOT have caught either observed failure; it is a cheap guard for
// the RFC1918 subclass ONLY. The observed (timeout-on-public-IP) failure mode
// is caught by Option B's CONNECTION-LEVEL reachability gate (CR-01): once the
// pod is running with published ports, a TCP dial to the LLM host:port that
// times out (Deps.Reachable == false) trips public_port_bind_timeout — this is
// the gateway-observable reachability signal the spike DIRECTIVE mandated, NOT
// the Vast ports map (which buildPodURLs reads and which lies). Offers with an
// empty public_ipaddr are KEPT (cannot prove private) — Option B is their sole
// backstop.
func (r *Reconciler) rejectPrivateIPOffers(offers []vast.Offer, log *slog.Logger, shape int, pass string) []vast.Offer {
	// Phase 17 (POD-CFG-04): read reject-private-ip from the provision-captured
	// snapshot so an owner toggle applies to the NEXT provision (stable in-flight).
	if !r.currentProvisionCfg().RejectPrivateIP {
		return offers
	}
	before := len(offers)
	offers = vast.RejectPrivateIPOffers(offers)
	if dropped := before - len(offers); dropped > 0 {
		log.Info("primary offer reject: RFC1918 public_ipaddr",
			"dropped", dropped, "remaining", len(offers),
			"shape", shape, "pass", pass)
	}
	return offers
}

// buildPodURLs is the shared 4-URL builder consumed by waitForReadyOrDestroy
// (forward path) and recoverOpenLifecycle (restart-recovery path). Returns
// a primaryPodURLs value (not pointer) so callers can compare empty-string
// fields without nil-checking.
func (r *Reconciler) buildPodURLs(inst vast.Instance) primaryPodURLs {
	return primaryPodURLs{
		LLM:  r.podLLMURL(inst),
		STT:  r.podSTTURL(inst), // Phase 11.2 D-B5′: restored
		DCGM: r.podDCGMURL(inst),
		// SEED-019 part 3: capture the pod-reported whisper device (already
		// whitelisted to cuda/cpu; "" when unavailable). Gates the "stt"
		// tier-0 override at the 3 sites below (replaces the deleted flag).
		WhisperDevice: r.deviceReport(inst),
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
		// Leak fix (observed live 2026-07-16, lifecycle 189): closing the DB
		// row WITHOUT destroying leaves a still-billing Vast instance nobody
		// manages. A not-running instance (loading/created/blank) is still
		// billed; BestEffortDestroy is idempotent (404 → nil) so calling it
		// when Vast already destroyed the instance is harmless.
		vastutil.BestEffortDestroy(ctx, r.deps.Vast, r.deps.Log, open.VastInstanceID.Int64)
		_ = q.ClosePrimaryLifecycle(ctx, gen.ClosePrimaryLifecycleParams{
			ID:             open.ID,
			ShutdownReason: pgtype.Text{String: "gateway_restart_orphan", Valid: true},
			TotalCostBrl:   vastutil.PgNumericFromFloat(0),
			EventJson:      vastutil.MustEventJSON("lifecycle_close", map[string]any{"reason": "gateway_restart_orphan"}),
		})
		return nil
	}
	urls := r.buildPodURLs(inst)
	if urls.LLM == "" || urls.STT == "" || urls.DCGM == "" {
		r.deps.Log.Warn("primary recover: pod ports not fully mapped; closing as unhealthy orphan",
			"lifecycle_id", open.ID)
		// Leak fix: the instance is RUNNING and billing — destroy before the
		// DB close or nobody ever will. No machine report: the pod may just
		// have been mid-cold-start when OUR restart interrupted it.
		vastutil.BestEffortDestroy(ctx, r.deps.Vast, r.deps.Log, open.VastInstanceID.Int64)
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
		!r.deps.HealthCheck(ctx, urls.STT) ||
		!r.deps.HealthCheck(ctx, urls.DCGM) {
		r.deps.Log.Warn("primary recover: 3-endpoint health check failed; closing as unhealthy orphan",
			"lifecycle_id", open.ID, "instance_id", open.VastInstanceID.Int64)
		// Leak fix (THE observed case — lifecycle 189/instance 45098378 kept
		// billing after this close during the 14a63f3 rollout): destroy the
		// running-but-unhealthy instance before closing the row. No machine
		// report: unhealthy here usually means OUR restart interrupted a
		// legitimate cold start, not a host defect.
		vastutil.BestEffortDestroy(ctx, r.deps.Vast, r.deps.Log, open.VastInstanceID.Int64)
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
		// Phase 21: 2-role restart-recovery override (tts removed).
		r.deps.Loader.OverrideTier0("llm", stripPrimaryReadinessSuffix(urls.LLM))
		// SEED-019 part 3: gate "stt" override on the pod-reported
		// whisper_device (cuda → override; else fail-safe to gemini-stt).
		// Replaces the deleted manual STT-serve config flag.
		if urls.WhisperDevice == "cuda" {
			r.deps.Loader.OverrideTier0("stt", stripPrimaryReadinessSuffix(urls.STT))
		}
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

// ReconcilerSnapshot is a point-in-time, lockless view of the primary
// reconciler's observable state, consumed by the GET /admin/operations
// panel. State is the FSM state name ("asleep"|"provisioning"|"ready"|
// "draining"|"destroying"), or "unknown" when the FSM is unwired (Vast
// disabled / early boot). ActiveLifecycleID and ActiveInstanceID are 0
// when no lifecycle is active.
type ReconcilerSnapshot struct {
	State             string
	ActiveLifecycleID int64
	ActiveInstanceID  int64
	IsLeader          bool
}

// Snapshot composes the reconciler's observable state for the operations
// dashboard. No data race: every read is a lockless atomic load —
// r.deps.FSM.State() (atomic.Int32), r.activeLifecycleSnapshot() (two
// atomic.Int64 loads), and r.isLeader.Load() — and no shared state is
// written here. Safe to call from the /admin/operations request
// goroutine concurrently with the reconciler loop. nil-guards
// r.deps.FSM → State "unknown".
func (r *Reconciler) Snapshot() ReconcilerSnapshot {
	state := "unknown"
	if r.deps.FSM != nil {
		state = r.deps.FSM.State().String()
	}
	lid, iid := r.activeLifecycleSnapshot()
	return ReconcilerSnapshot{
		State:             state,
		ActiveLifecycleID: lid,
		ActiveInstanceID:  iid,
		IsLeader:          r.isLeader.Load(),
	}
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
		deps:   deps,
		cfg:    deps.Cfg,
		rule:   deps.Rule,
		podCfg: deps.PodCfg,
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

// ===========================================================================
// Phase 20-04 — FF-02 parser + BL-01/AL-01 classifier + outcome hook.
// RED scaffolding: these are stubbed in Task 1 (tests fail on behavior) and
// implemented in Tasks 3 (parseDownloadProgress) and 4 (classifier/hook/list
// helpers).
// ===========================================================================

// parseDownloadProgress scans onstart-log text for `[download-weights]` lines:
// whether any `fetching` line is present (arms the download phase), the count
// of `ok` lines (disarms once all files complete), and the SUM of the newest
// `bytes=<N>` per `key=` (3 parallel files → sum their latest partial sizes).
func parseDownloadProgress(text string) (fetching bool, okCount int, totalBytes int64) {
	const marker = "[download-weights]"
	latest := map[string]int64{}
	for _, line := range strings.Split(text, "\n") {
		i := strings.Index(line, marker)
		if i < 0 {
			continue
		}
		rest := strings.TrimSpace(line[i+len(marker):])
		switch {
		case strings.HasPrefix(rest, "fetching "):
			fetching = true
		case strings.HasPrefix(rest, "ok "):
			okCount++
		case strings.HasPrefix(rest, "progress "):
			var key string
			var b int64
			for _, tok := range strings.Fields(rest) {
				switch {
				case strings.HasPrefix(tok, "key="):
					key = tok[len("key="):]
				case strings.HasPrefix(tok, "bytes="):
					if v, err := strconv.ParseInt(tok[len("bytes="):], 10, 64); err == nil {
						b = v
					}
				}
			}
			if key != "" {
				latest[key] = b // newest bytes= per key wins
			}
		}
	}
	for _, v := range latest {
		totalBytes += v
	}
	return fetching, okCount, totalBytes
}

// machineAttributableReason reports whether a close reason indicts the HOST
// (safe to auto-blocklist) vs. a global / our-side / ambiguous cause. Only an
// ALLOWLIST of reasons is machine-attributable; everything else — including
// progress_stall_timeout (a download stall's root cause is the shared weights
// store + network, potentially GLOBAL: a broken R2 stalls EVERY host, so
// blocklisting on it would cascade-poison the market — Codex #9),
// cancelled_in_flight, health_timeout, and all create/config/status_msg/R2-auth
// errors — returns false.
//
// ponytail: promote progress_stall_timeout to machine-attributable only with
// cross-lifecycle correlation (this machine stalled while OTHERS succeeded in
// the same window ⇒ host-attributable); a single bytes-frozen signal cannot
// distinguish a host fault from a global store/network outage.
func machineAttributableReason(reason string) bool {
	switch reason {
	case "created_state_timeout",
		"instance_terminal_state",
		"instance_terminal_state_confirmed",
		"public_port_bind_timeout",
		// blocklisted_ip: a sibling machine behind the same public IP already
		// failed a report-worthy fast-fail. Adding THIS machine_id to the DB
		// blocklist is the IP→machine learning loop — the offer feed has no
		// IP, so the selector can only avoid the datacenter via machine_ids.
		"blocklisted_ip":
		return true
	}
	return false
}

// appendDedupCap appends id to list unless already present; when capacity > 0
// and the result exceeds it, the oldest entries are dropped (FIFO). Returns the
// input unchanged when id is already present (dedup — no double-add).
func appendDedupCap(list []int64, id int64, capacity int) []int64 {
	for _, x := range list {
		if x == id {
			return list
		}
	}
	out := append(append([]int64{}, list...), id)
	if capacity > 0 && len(out) > capacity {
		out = out[len(out)-capacity:]
	}
	return out
}

// removeFromList returns a copy of list with every occurrence of id removed.
func removeFromList(list []int64, id int64) []int64 {
	out := make([]int64, 0, len(list))
	for _, x := range list {
		if x != id {
			out = append(out, x)
		}
	}
	return out
}

// int64SliceEqual reports element-wise equality (order-sensitive).
func int64SliceEqual(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// recordProvisionOutcome maintains the vast_machine_{block,allow}list from a
// finished provision (BL-01/AL-01):
//   - success (provErr == nil) → allowlist (dedup, FIFO cap allowlistCap) AND
//     remove from the blocklist (a degraded-then-recovered host self-heals).
//     UNGATED by reason — success is always allowlist-worthy.
//   - failure AND failStreak>=1 AND a machine-attributable reason → blocklist
//     (dedup) AND remove from the allowlist. A 1st failure (failStreak==0) or a
//     non-machine-attributable reason mutates NEITHER list.
//
// A DB read/write error is logged and swallowed — never block/crash the
// reconciler on a list write (matches the failStreak-count "never block the
// pod" rule).
//
// ponytail: fresh read + up to 2 writes; the primary is leader-gated (single
// reconciler, PRV-03) so there is no real contention — go per-field CAS only if
// that ever changes. No TTL/expiry on the blocklist — manual prune; upgrade
// path = a 24h expiry column if the list rots.
func (r *Reconciler) recordProvisionOutcome(ctx context.Context, machineID, failStreak int64, reason string, provErr error, log *slog.Logger) {
	if machineID <= 0 {
		return
	}
	q := r.queries()
	if q == nil {
		return
	}
	// Detached ctx (same pattern as closeLifecycle's DB write): on every
	// fast-fail path closeLifecycle CANCELS the provision ctx (lifecycleCancel)
	// before waitForReadyOrDestroy returns its reason, so this function always
	// received a dead ctx on failure and every blocklist write silently died
	// with "GetPodConfig failed: context canceled" (observed prod 2026-07-16 —
	// machine 136634 was re-picked twice after a blocklisted_ip close because
	// the BL-01 write never landed).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg, err := q.GetPodConfig(ctx)
	if err != nil {
		log.Warn("primary recordProvisionOutcome: GetPodConfig failed", "err", err, "machine_id", machineID)
		return
	}
	block := cfg.VastMachineBlocklist
	allow := cfg.VastMachineAllowlist

	var newBlock, newAllow []int64
	switch {
	case provErr == nil:
		newAllow = appendDedupCap(allow, machineID, allowlistCap)
		newBlock = removeFromList(block, machineID)
	case failStreak >= 1 && machineAttributableReason(reason):
		newBlock = appendDedupCap(block, machineID, 0) // no cap on the blocklist
		newAllow = removeFromList(allow, machineID)
	default:
		// 1st-failure grace OR non-machine-attributable reason → no mutation.
		return
	}

	if !int64SliceEqual(newAllow, allow) {
		if err := q.UpdatePodConfigFieldAllowlist(ctx, newAllow); err != nil {
			log.Warn("primary recordProvisionOutcome: allowlist write failed", "err", err, "machine_id", machineID)
			return
		}
	}
	if !int64SliceEqual(newBlock, block) {
		if err := q.UpdatePodConfigFieldBlocklist(ctx, newBlock); err != nil {
			log.Warn("primary recordProvisionOutcome: blocklist write failed", "err", err, "machine_id", machineID)
			return
		}
	}
}
