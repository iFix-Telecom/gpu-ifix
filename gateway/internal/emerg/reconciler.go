// Package emerg (reconciler.go): Plan 06-04 leader-elected reconciler
// loop. Drives the 7-state emergency FSM at 1Hz (configurable via
// Deps.TickInterval for tests) inside a redsync v4 distributed mutex
// (CONTEXT.md D-B2: TTL 30s, renew every 10s = 1/3 TTL).
//
// PRV-03 ("Apenas o leader avança o FSM") rests entirely on this file.
// The reconciler exposes a tiny public surface so downstream plans
// (05 trigger, 06 provisioning, 07 cancel/recovery, 08 cutback) only
// need IsLeader() / State() / ReplicaID() to gate their actions.
//
// Pitfall enforcement (RESEARCH.md Pitfall 4 + 8):
//
//   - Pitfall 4: redsync.Mutex.ExtendContext returns (bool, error). The
//     production code checks both — ANY combination other than (true,
//     nil) is treated as lost leadership. Quietly returning `(false,
//     nil)` (single-Redis quorum nuance) would otherwise cause two
//     replicas to think they hold the lock simultaneously and BOTH
//     advance the FSM → split-brain → DB unique-index violation.
//
//   - Pitfall 8: when the parent ctx is cancelled, `defer mutex.UnlockContext(ctx)`
//     is a footgun — UnlockContext fails immediately because of the
//     cancelled ctx. We use a SEPARATE context.Background() with a 2s
//     timeout for the graceful release path. Failures are ignored
//     (TTL=30s catches anything missed).
//
// evaluateTick is intentionally a no-op stub in this plan. Plans 05-08
// extend it incrementally — each downstream plan owns one transition
// branch (trigger / provision / cancel / cutback). Keeping the stub
// here is the deliberate seam.
package emerg

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/go-redsync/redsync/v4"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/upstreams"
)

// emergLockExpiry is the redsync mutex TTL (CONTEXT.md D-B2). Constant
// at 30s — drift-tolerant, survives a Pub/Sub blip + Redis pause without
// a leader losing its lease. If ops needs to retune in the field, add an
// env var; for now operators rely on the default.
const emergLockExpiry = 30 * time.Second

// emergLockRenewInterval is how often the leader extends the lease while
// it holds leadership. Set to 1/3 of emergLockExpiry so two consecutive
// Pub/Sub blips can be absorbed without losing the lease.
const emergLockRenewInterval = 10 * time.Second

// Deps is the dependency bundle injected into NewReconciler. Caller-
// owned construction so tests can pass a miniredis-backed redis.Client
// + a stub FSM, and the production wiring (Plan 09 main.go) passes
// real instances.
//
// All fields are required EXCEPT TickInterval (defaults to 1s), Log
// (defaults to slog.Default), Redsync (auto-built from Redis when
// nil) and DB (Plan 04 only — evaluateTick is a stub; downstream plans
// require a real *pgxpool.Pool).
type Deps struct {
	// DB is the Postgres pool used by Plans 05-08 for lifecycle DB
	// queries (orphan recovery, lifecycle insert/close, monthly cost
	// aggregate). Plan 04 does not exercise the DB path — leave nil
	// in tests that only verify the leader-election semantics.
	DB *pgxpool.Pool

	// Redis is the go-redis v9 client. MUST be non-nil — used for
	// redsync mutex construction and (downstream plans) the Pub/Sub
	// + Hash mirror of the FSM state.
	Redis *redis.Client

	// Redsync is the go-redsync v4 instance used to mint the leader
	// mutex. When nil, NewReconciler wraps Deps.Redis via
	// redisx.NewEmergRedsync — single point of truth for the
	// goredis/v9 pool adapter.
	Redsync *redsync.Redsync

	// FSM is the in-process 7-state emergency FSM. MUST be non-nil —
	// State() proxies through to f.State() and Plans 05-08 transition
	// it from inside evaluateTick.
	FSM *FSM

	// Cfg holds the Phase 6 env-driven knobs (D-A1..D-D4 + the 11
	// Phase 6 fields added in Plan 06-01). Plan 04 does not consume
	// any cfg field (evaluateTick stub) but we accept it here to lock
	// the constructor signature so downstream plans can reuse without
	// breaking callers.
	Cfg config.Config

	// TickInterval is the cadence of the Run loop. <=0 defaults to 1s
	// (CONTEXT.md D-B3). Tests pass a small value (50-100ms) to
	// accelerate convergence; production uses 1s.
	TickInterval time.Duration

	// Log is the structured logger. nil defaults to slog.Default(); the
	// reconciler attaches a `subsystem=emerg.reconciler` field plus the
	// per-replica replicaID at Run start.
	Log *slog.Logger

	// Vast is the Vast.ai REST client. NewReconciler auto-builds one
	// from Cfg.VastAIAPIKey when nil AND the key is non-empty; tests
	// inject a mock via the SetVastClient helper instead. Plan 06-06+
	// reads this for the provisioning lifecycle.
	Vast VastAPI

	// Loader is the upstreams.Loader used by Plan 06-08 (D-E3) cutback —
	// markHealthy calls Loader.OverrideTier0("llm", podURL) when the
	// emergency pod becomes healthy; evaluateEmergencyActive calls
	// Loader.RestoreTier0("llm") on cutback. Optional in tests that do
	// not exercise the dispatcher integration; nil-safe (the call sites
	// short-circuit when Loader is nil).
	Loader *upstreams.Loader
}

// Reconciler is the leader-elected loop owner. Construct via
// NewReconciler then start with `go r.Run(ctx)`. IsLeader is safe to
// call from any goroutine (atomic.Load).
//
// q is the sqlc-generated query handle. Plan 04 does not exercise it;
// it is constructed eagerly so Plans 05-08 do not need to re-instantiate
// inside hot paths.
type Reconciler struct {
	deps           Deps
	isLeader       atomic.Bool
	lastExtendUnix atomic.Int64 // unix-seconds of the most recent successful Extend or initial Lock
	replicaID      string
	q              *gen.Queries

	// tracker is the per-replica `local-llm` breaker mirror fed by the
	// gw:breaker:events Pub/Sub consumer (Plan 06-05 Task 1). The reader
	// is the reconciler tick (evaluateHealthy); the writer is the
	// Subscribe goroutine. Both atomic so no mutex required.
	tracker *localLlmTracker

	// activeLifecycle holds the in-flight emergency lifecycle row when
	// the FSM is in EmergencyProvisioning/Active/Recovering. Set by
	// Plan 06-06 startProvisioning; consulted by applyEmergCommand to
	// resolve force-destroy targets. nil when no live lifecycle.
	activeLifecycle atomic.Pointer[ActiveLifecycle]

	// activePodURL is the /health URL of the live emergency pod when the
	// FSM is in EmergencyActive. Plan 08 dispatcher reads this on every
	// request via ActivePodURL(). nil when no pod is healthy.
	activePodURL atomic.Pointer[string]

	// lifecycleCancel holds the cancel func for the in-flight provisioning
	// goroutine. Plan 07 will call (*lifecycleCancel)() on local-llm
	// recovery (cancel-in-flight). Stored as **func() so atomic.Swap
	// returns the previous pointer cleanly. nil when no provisioning
	// goroutine is running.
	lifecycleCancel atomic.Pointer[context.CancelFunc]

	// lastEmergencyTrafficAt is the unix-second timestamp of the most
	// recent dispatcher RegisterTraffic call. Used by IsIdle to drive the
	// D-D1 idle-grace destroy timer (PROVISION_IDLE_GRACE_SECONDS, default
	// 300s). 0 when no traffic has ever been registered. Plan 06-08.
	lastEmergencyTrafficAt atomic.Int64

	// recoveringEnteredAt is the unix-second timestamp at which the FSM
	// transitioned EmergencyActive → Recovering (Plan 06-08 cutback). The
	// idle-grace destroy timer in evaluateRecovering compares against
	// this so a fresh-Recovering tick has the FULL grace window even if
	// no traffic has been registered yet.
	recoveringEnteredAt atomic.Int64

	// cooldownEnteredAt is the unix-second timestamp at which the FSM
	// transitioned Recovering → Cooldown. evaluateCooldown re-arms the
	// trigger by transitioning Cooldown → Healthy after
	// PROVISION_HEALTHY_DURATION_SECONDS.
	cooldownEnteredAt atomic.Int64

	// vastOverride and healthCheckOverride are test-only injection slots.
	// Production leaves both nil and reads VastAPI from deps.Vast / does
	// the real HTTP /health probe inside checkHealth. SetVastClient and
	// SetHealthCheck (lifecycle.go) populate these in tests.
	vastOverride        VastAPI
	healthCheckOverride HealthChecker

	// budgetDedupe gates Sentry monthly-budget warnings to once per UTC
	// day (Plan 06-09 D-D2 / Pitfall 11). Constructed inside NewReconciler
	// so checkBudget can rely on a non-nil pointer.
	budgetDedupe *budgetAlertDedupe

	// lastBudgetCheckUnix is the unix-second timestamp of the last
	// checkBudget invocation from runOneTick. Used to rate-limit the
	// monthly cost SUM aggregate to once per 60s — the 1Hz hot path
	// stays cheap and the alert stays fresh enough to catch a runaway
	// spend within a minute. Plan 06-09.
	lastBudgetCheckUnix atomic.Int64

	// monthlyCostOverride is a test-only injection slot consumed by
	// invokeMonthlyCost (budget.go). Production leaves this nil and
	// invokeMonthlyCost falls through to r.q.GetMonthlyCostBRL — letting
	// unit tests drive checkBudget without standing up a real pgxpool.
	monthlyCostOverride monthlyCostFn
}

// ActiveLifecycle is the minimal in-memory snapshot of the live
// emergency lifecycle row. Plan 06-05 only declares the type + field
// surface; Plan 06-06 wiring (startProvisioning) is the first writer.
// The applyEmergCommand force-destroy branch (Plan 06-05 Task 3) is the
// first reader.
type ActiveLifecycle struct {
	ID             int64
	VastInstanceID int64 // 0 when bid not yet accepted
	StartedUnix    int64
}

// NewReconciler constructs a Reconciler with sensible defaults applied
// in-place to the Deps struct (TickInterval, Log, Redsync). The
// replicaID is derived from os.Hostname() — in dev it surfaces the
// container ID; in prod it surfaces the pod hostname.
//
// The function does NOT validate Deps.Redis or Deps.FSM — wiring bugs
// (passing a nil client) surface as the first method call panicking,
// which is the cheaper failure mode than a nil-check labyrinth.
func NewReconciler(deps Deps) *Reconciler {
	if deps.TickInterval <= 0 {
		deps.TickInterval = 1 * time.Second
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.Redsync == nil {
		deps.Redsync = redisx.NewEmergRedsync(deps.Redis)
	}
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	// Auto-build a real Vast.ai client when the operator provided an API
	// key but did not inject a VastAPI (tests inject mocks via the
	// SetVastClient helper instead).
	if deps.Vast == nil && deps.Cfg.VastAIAPIKey != "" {
		deps.Vast = vast.NewClient(deps.Cfg.VastAIAPIKey)
	}
	r := &Reconciler{
		deps:         deps,
		replicaID:    hostname,
		tracker:      newLocalLlmTracker(),
		budgetDedupe: &budgetAlertDedupe{},
	}
	if deps.DB != nil {
		r.q = gen.New(deps.DB)
	}
	return r
}

// IsLeader returns true iff this replica currently holds the
// gw:emerg:lock redsync mutex. Lockless atomic.Load — safe to call from
// the request hot path (e.g., dispatcher checks before routing to the
// emergency pod).
func (r *Reconciler) IsLeader() bool {
	return r.isLeader.Load()
}

// State proxies to the in-process FSM. Lockless atomic.Load.
// Convenience for callers that already hold a *Reconciler reference.
func (r *Reconciler) State() State {
	return r.deps.FSM.State()
}

// ReplicaID returns the per-replica identifier (os.Hostname() at
// boot). Used by Plan 05+ to tag Pub/Sub events and by gatewayctl to
// pretty-print "leader=<replicaID>".
func (r *Reconciler) ReplicaID() string {
	return r.replicaID
}

// defaultMutexOptions returns the canonical CONTEXT.md D-B2 mutex
// options: TTL 30s, single Try, zero retry-delay. Pulled out of Run()
// so reconciler_test.go can build the same mutex when driving
// runOneTick directly.
func defaultMutexOptions() []redsync.Option {
	return []redsync.Option{
		redsync.WithExpiry(emergLockExpiry),
		redsync.WithTries(1),
		redsync.WithRetryDelay(0),
	}
}

// Run is the reconciler main loop. Blocks until ctx cancellation.
// MUST be started inside its own goroutine: `go r.Run(rootCtx)`.
//
// On ctx.Done(), Run releases the lock via a SEPARATE
// context.Background() with a 2s timeout (Pitfall 8) so a parent
// shutdown does not swallow the UnlockContext call.
func (r *Reconciler) Run(ctx context.Context) {
	log := r.deps.Log.With("subsystem", "emerg.reconciler", "replica", r.replicaID)
	interval := r.deps.TickInterval
	if interval <= 0 {
		interval = 1 * time.Second
	}

	mutex := r.deps.Redsync.NewMutex(redisx.EmergLockKey(), defaultMutexOptions()...)

	// W11 ordering invariant (Plan 06-05): spawn Pub/Sub subscribers
	// BEFORE the ticker fires. Pub/Sub is at-most-once with no replay,
	// so a publish that arrives before the first SUBSCRIBE registers is
	// silently lost. By spawning before the ticker, the worst case is
	// the subscriber registers slightly before the leader-election tick
	// — still atomically before any state-change publish from the same
	// reconciler's FSM transitions.
	go r.Subscribe(ctx)              // gw:breaker:events → tracker
	go r.SubscribeEmergCommands(ctx) // gw:emerg:events  → applyEmergCommand

	t := time.NewTicker(interval)
	defer t.Stop()

	log.Info("emerg reconciler started", "interval", interval, "lock_key", redisx.EmergLockKey())

	for {
		select {
		case <-ctx.Done():
			// Pitfall 8: parent ctx already cancelled; UnlockContext
			// would short-circuit if we passed `ctx` here. Use a fresh
			// background ctx with a 2s budget. Ignore the error — TTL
			// expiry catches any missed unlock.
			if r.isLeader.Load() {
				releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 2*time.Second)
				_, _ = mutex.UnlockContext(releaseCtx)
				releaseCancel()
				r.isLeader.Store(false)
			}
			log.Info("emerg reconciler stopping")
			return
		case now := <-t.C:
			r.runOneTick(ctx, mutex, now, log)
		}
	}
}

// runOneTick performs ONE leader-election + evaluate pass. Held as a
// method (not inlined inside Run) so unit tests can drive single ticks
// deterministically without spinning the goroutine.
//
// Two paths:
//
//  1. Non-leader: try LockContext. On nil error, become leader and
//     record lastExtendUnix=now (so the renew gate uses the acquire
//     time as its baseline). On any error, return — someone else holds
//     the lock; observe via Pub/Sub (Plan 05 subscribe.go).
//
//  2. Leader: if elapsed since lastExtendUnix >= emergLockRenewInterval
//     (10s), call ExtendContext. Pitfall 4: ANY combination other than
//     (true, nil) means we lost the lock — flip is_leader=false and
//     return. Next tick will re-attempt Lock.
func (r *Reconciler) runOneTick(ctx context.Context, mutex *redsync.Mutex, now time.Time, log *slog.Logger) {
	if !r.isLeader.Load() {
		if err := mutex.LockContext(ctx); err != nil {
			// Someone else holds the lock — non-leader path. We do NOT
			// log at every tick; the warn would be noisy. Plan 05 will
			// observe state via Pub/Sub instead.
			return
		}
		r.isLeader.Store(true)
		r.lastExtendUnix.Store(now.Unix())
		log.Info("acquired leadership", "fsm_state", r.deps.FSM.State().String())
		// Plan 07 D-D5 — fresh leader reconciles in-flight lifecycles
		// (orphan recovery) BEFORE evaluating new transitions. 4 cenários:
		// pre-create / lost / zombie / active-resume. Failures are logged
		// but do not block the FSM tick — next leader acquisition retries.
		if err := r.recoverOrphanLifecycles(ctx); err != nil {
			log.Error("orphan recovery failed", "err", err)
		}
	} else {
		// Renew gate: only call ExtendContext when 10s have elapsed since
		// the last successful extend (or initial acquire). This keeps
		// Redis traffic low (one Extend per 10s instead of per tick) and
		// matches CONTEXT.md D-B2 1/3-TTL renew cadence.
		if now.Unix()-r.lastExtendUnix.Load() >= int64(emergLockRenewInterval.Seconds()) {
			ok, err := mutex.ExtendContext(ctx)
			if err != nil || !ok {
				// Pitfall 4: any non-(true, nil) combination = lost
				// leadership. Single-Redis usually returns either
				// (true, nil) or (false, ErrLockAlreadyExpired); the
				// (false, nil) quorum nuance is rare but possible —
				// we treat ALL non-success as identical.
				log.Warn("lost leadership; ceding", "err", err, "ok", ok)
				r.isLeader.Store(false)
				return
			}
			r.lastExtendUnix.Store(now.Unix())
		}
	}

	// Leader path: evaluate FSM transitions. STUB in Plan 04.
	r.evaluateTick(ctx, now, log)

	// Plan 06-09 (D-D2): leader-only monthly budget alert. Rate-limited
	// to once per 60s so the SUM aggregate does not run every tick. The
	// budget check is decoupled from FSM transitions because spend can
	// cross the threshold whether or not the FSM is currently active —
	// e.g. a long-running ACTIVE lifecycle steadily accumulating cost.
	if r.isLeader.Load() && now.Unix()-r.lastBudgetCheckUnix.Load() >= 60 {
		r.lastBudgetCheckUnix.Store(now.Unix())
		r.checkBudget(ctx)
	}
}

// evaluateTick is the FSM transition evaluation dispatcher. Plan 06-05
// implements the StateHealthy branch (trigger gate); Plans 06-08 extend
// the remaining cases incrementally:
//
//   - Plan 05 (trigger):     HEALTHY → FAILED_OVER → EMERGENCY_PROVISIONING
//                            when local-llm OPEN sustained ≥ threshold.
//   - Plan 06 (provisioning): EMERGENCY_PROVISIONING → EMERGENCY_ACTIVE
//                            via Vast.ai bid+create+/health poll.
//   - Plan 07 (cancel/recovery): cancel-in-flight + leader-recovery orphan
//                            reconcile.
//   - Plan 08 (cutback):     RECOVERING grace + IDLE_GRACE destroy +
//                            COOLDOWN suppression window.
func (r *Reconciler) evaluateTick(ctx context.Context, now time.Time, log *slog.Logger) {
	switch r.deps.FSM.State() {
	case StateHealthy:
		r.evaluateHealthy(ctx, now, log)
	case StateEmergencyProvisioning:
		r.evaluateEmergencyProvisioning(ctx, now, log)
	case StateEmergencyActive:
		r.evaluateEmergencyActive(ctx, now, log)
	case StateRecovering:
		r.evaluateRecovering(ctx, now, log)
	case StateCooldown:
		r.evaluateCooldown(ctx, now, log)
	default:
		// Plan 06-08 still leaves StateDegraded + StateFailedOver to Phase
		// 5 / Phase 3 ownership respectively (degraded → shed FSM in
		// internal/shed; failed_over is a transient marker observed only
		// during the trigger transition). Log at Debug to keep tests
		// exercising the leader path without spurious noise.
		log.Debug("evaluateTick: state not handled in Plan 06-08",
			"state", r.deps.FSM.State().String())
	}
}

// evaluateEmergencyActive — Plan 06-08 (D-D1 + D-C4) cutback gate.
//
//  1. D-C4 ride-out: if local-llm is OPEN/HALF_OPEN again (a second
//     failover during emergency), DO NOT spawn a new lifecycle. The
//     partial unique index `emergency_live_singleton` enforces this at
//     the DB layer; this branch is the leader-side noise gate that just
//     logs at Debug so dashboards see the multi-failover event without
//     destabilising the FSM.
//
//  2. D-D1 cutback: when the local-llm tracker has been CLOSED for
//     ≥ ProvisionHealthyDurationSeconds (default 300s; tests override to
//     1s), restore tier-0 routing via Loader.RestoreTier0("llm") and
//     transition FSM EmergencyActive → Recovering. The emergency pod
//     remains live — destroy is delayed until evaluateRecovering's idle
//     grace fires (so requests already in flight to the pod can drain).
func (r *Reconciler) evaluateEmergencyActive(_ context.Context, now time.Time, log *slog.Logger) {
	trackerState := r.tracker.State()
	if trackerState != "closed" {
		// D-C4: ride-out. local-llm still OPEN/HALF_OPEN. No action.
		log.Debug("evaluateEmergencyActive: tracker not CLOSED; ride-out",
			"tracker_state", trackerState)
		return
	}
	sustained := r.tracker.SustainedClosedSeconds()
	if sustained < int64(r.deps.Cfg.ProvisionHealthyDurationSeconds) {
		log.Debug("evaluateEmergencyActive: cutback gate not yet armed",
			"sustained_closed_seconds", sustained,
			"threshold", r.deps.Cfg.ProvisionHealthyDurationSeconds)
		return
	}
	// CUTBACK FIRES. Restore tier-0 routing FIRST (so dispatcher stops
	// sending new requests to the emergency pod) THEN transition FSM.
	// Restore is race-free + idempotent (atomic.Pointer.Store(nil)).
	if r.deps.Loader != nil {
		r.deps.Loader.RestoreTier0("llm")
	}
	log.Info("emergency cutback: primary healthy; restoring tier-0 (D-D1)",
		"sustained_closed_seconds", sustained,
		"threshold", r.deps.Cfg.ProvisionHealthyDurationSeconds)
	r.deps.FSM.Transition(StateEmergencyActive, StateRecovering, now,
		"primary_healthy_sustained")
	r.recoveringEnteredAt.Store(now.Unix())
	// Reset traffic timer — entering Recovering is the "fresh window"
	// for idle-grace counting. Without this reset, traffic registered
	// minutes ago during ACTIVE would falsely keep IsIdle false during
	// the new Recovering phase.
	r.lastEmergencyTrafficAt.Store(now.Unix())
	r.captureBreadcrumb("cutback", map[string]any{
		"sustained_closed_seconds": sustained,
		"threshold":                r.deps.Cfg.ProvisionHealthyDurationSeconds,
	})
}

// evaluateRecovering — Plan 06-08 (D-D1) idle-grace destroy gate.
//
// After cutback (Recovering entered), wait until no traffic has flowed
// to the emergency pod for PROVISION_IDLE_GRACE_SECONDS (default 300s;
// tests override to 1s). Then destroy the Vast.ai instance, close the
// lifecycle row with shutdown_reason='cutback_idle', and transition
// Recovering → Cooldown.
//
// IsIdle is computed from the lastEmergencyTrafficAt atomic — set by
// dispatcher.RegisterTraffic on each request that hit the emergency pod.
// The grace window is anchored to the LATER of (recoveringEnteredAt,
// lastEmergencyTrafficAt) so a fresh-Recovering tick has the FULL grace
// window even if RegisterTraffic was called moments ago during ACTIVE.
func (r *Reconciler) evaluateRecovering(ctx context.Context, now time.Time, log *slog.Logger) {
	graceSeconds := int64(r.deps.Cfg.ProvisionIdleGraceSeconds)
	if graceSeconds <= 0 {
		// Defense in depth — refuse to destroy if env was tampered with
		// (operator override to 0 would mean "destroy immediately on
		// cutback"). Log Error so the misconfig is visible.
		log.Error("evaluateRecovering: idle grace seconds <= 0; refusing to destroy",
			"idle_grace_seconds", r.deps.Cfg.ProvisionIdleGraceSeconds)
		return
	}
	lastTraffic := r.lastEmergencyTrafficAt.Load()
	enteredAt := r.recoveringEnteredAt.Load()
	idleAnchor := lastTraffic
	if enteredAt > idleAnchor {
		idleAnchor = enteredAt
	}
	idleSeconds := now.Unix() - idleAnchor
	if idleSeconds < graceSeconds {
		log.Debug("evaluateRecovering: idle grace not yet elapsed",
			"idle_seconds", idleSeconds, "grace_seconds", graceSeconds)
		return
	}
	lc := r.activeLifecycle.Load()
	if lc == nil {
		// Defensive: Recovering without an active lifecycle means a prior
		// close already cleared the pointer. Just transition to Cooldown.
		log.Warn("evaluateRecovering: no active lifecycle; transitioning to Cooldown",
			"idle_seconds", idleSeconds)
		r.deps.FSM.Transition(StateRecovering, StateCooldown, now, "idle_grace_no_lifecycle")
		r.cooldownEnteredAt.Store(now.Unix())
		return
	}
	log.Info("emergency idle-grace elapsed: destroying pod (D-D1)",
		"lifecycle_id", lc.ID, "vast_instance_id", lc.VastInstanceID,
		"idle_seconds", idleSeconds, "grace_seconds", graceSeconds)
	if err := r.destroyAndCloseLifecycle(ctx, lc, "cutback_idle"); err != nil {
		log.Error("evaluateRecovering: destroyAndCloseLifecycle failed; transitioning anyway",
			"lifecycle_id", lc.ID, "err", err)
	}
	r.deps.FSM.Transition(StateRecovering, StateCooldown, now, "idle_grace_elapsed")
	r.cooldownEnteredAt.Store(now.Unix())
}

// evaluateCooldown — Plan 06-08 oscillation suppression. Holds the FSM
// in Cooldown for PROVISION_HEALTHY_DURATION_SECONDS (same as the
// cutback gate; defaults to 300s). Once elapsed, transitions Cooldown
// → Healthy so a future trigger can re-arm.
func (r *Reconciler) evaluateCooldown(_ context.Context, now time.Time, log *slog.Logger) {
	holdSeconds := int64(r.deps.Cfg.ProvisionHealthyDurationSeconds)
	if holdSeconds < 0 {
		holdSeconds = 0
	}
	enteredAt := r.cooldownEnteredAt.Load()
	if enteredAt == 0 {
		// Cooldown without an entry timestamp — defensive (e.g. leader
		// recovery resumed FSM directly into Cooldown). Stamp now and
		// re-evaluate on next tick.
		r.cooldownEnteredAt.Store(now.Unix())
		return
	}
	elapsed := now.Unix() - enteredAt
	if elapsed < holdSeconds {
		log.Debug("evaluateCooldown: hold window not yet elapsed",
			"elapsed_seconds", elapsed, "hold_seconds", holdSeconds)
		return
	}
	log.Info("emergency cooldown elapsed: re-arming trigger (Healthy)",
		"elapsed_seconds", elapsed, "hold_seconds", holdSeconds)
	r.deps.FSM.Transition(StateCooldown, StateHealthy, now, "cooldown_elapsed")
	// Reset the cooldown anchor so a future Cooldown re-entry has a
	// fresh stamp.
	r.cooldownEnteredAt.Store(0)
}

// RegisterTraffic implements the EmergTrafficRegistrar interface for the
// dispatcher (Plan 06-08 / D-E3). Stores the current unix-second
// timestamp into lastEmergencyTrafficAt so IsIdle can answer "has the
// emergency pod been silent for PROVISION_IDLE_GRACE_SECONDS?". Lockless
// + safe to call on the request hot path.
func (r *Reconciler) RegisterTraffic() {
	r.lastEmergencyTrafficAt.Store(time.Now().Unix())
}

// IsIdle returns true when the emergency pod has had no registered
// traffic for at least graceSeconds. Exposed for tests + future
// gatewayctl emerg-state output. Returns false defensively when
// graceSeconds <= 0 (cannot enter "idle" state with a non-positive
// grace window).
func (r *Reconciler) IsIdle(graceSeconds int) bool {
	if graceSeconds <= 0 {
		return false
	}
	last := r.lastEmergencyTrafficAt.Load()
	enteredAt := r.recoveringEnteredAt.Load()
	anchor := last
	if enteredAt > anchor {
		anchor = enteredAt
	}
	return time.Now().Unix()-anchor >= int64(graceSeconds)
}

// evaluateEmergencyProvisioning is the StateEmergencyProvisioning branch.
// Two responsibilities:
//
//  1. Bootstrap: when no activeLifecycle is in flight (first tick after
//     FSM entered this state), spawn provisionLifecycle goroutine via
//     startProvisioning. Idempotent — subsequent ticks while the goroutine
//     is running observe activeLifecycle != nil and short-circuit.
//
//  2. Cancel detection (D-C3): when the local-llm breaker has flipped
//     back to CLOSED or HALF_OPEN while we are mid-provisioning, cancel
//     the in-flight lifecycle (triple layer: context cancel + Pub/Sub
//     broadcast + post-create destroy enforced in waitForReadyOrDestroy).
//     The FSM transitions back to Healthy with reason
//     'cancelled_local_llm_recovered' — closeLifecycle inside the
//     goroutine writes shutdown_reason='cancelled_in_flight' to the DB.
//
// The cancel branch ONLY fires while activeLifecycle != nil — calling
// cancelActiveLifecycle on a nil pointer is a no-op, but the FSM
// transition would still race startProvisioning if we tried to cancel
// before the goroutine spawned.
func (r *Reconciler) evaluateEmergencyProvisioning(ctx context.Context, now time.Time, log *slog.Logger) {
	if r.activeLifecycle.Load() == nil {
		// Fresh entry into EmergencyProvisioning — spawn provisioning.
		r.startProvisioning(ctx)
		return
	}
	// Cancel detection: tracker shows local-llm recovered (CLOSED) or is
	// re-probing (HALF_OPEN). D-C3 — cancel and return FSM to Healthy.
	trackerState := r.tracker.State()
	if trackerState == "closed" || trackerState == "half-open" {
		log.Info("local-llm recovered during provisioning; cancelling (D-C3)",
			"tracker_state", trackerState,
			"lifecycle_id", r.activeLifecycle.Load().ID)
		r.cancelActiveLifecycle(ctx, "local_llm_recovered_during_provisioning")
		// Transition FSM back to Healthy. The provisioning goroutine will
		// write shutdown_reason='cancelled_in_flight' to the DB on its way
		// out (closeLifecycle is called from waitForReadyOrDestroy's
		// ctx.Done() branch OR provisionLifecycle's bid-loop ctx-check).
		r.deps.FSM.Transition(StateEmergencyProvisioning, StateHealthy,
			now, "cancelled_local_llm_recovered")
	}
}

// evaluateHealthy is the StateHealthy branch of the reconciler tick. Plan
// 06-05 trigger gate (PRV-04 / SC-1):
//
//  1. Read tracker.SustainedFailedOverSeconds() — the local-llm breaker
//     has been OPEN this long according to the per-replica Pub/Sub
//     consumer.
//  2. If under the cfg.ProvisionTriggerFailedOverSeconds threshold (D-C1
//     default 120s; tests override to 1s via defaultTestCfg), return —
//     trigger not yet armed.
//  3. D-C5 reconciler check: query ai_gateway.emergency_lifecycles for a
//     live row (ended_at IS NULL). If one exists, the partial unique
//     index already protects against split-brain INSERT — but we abort
//     anyway and log Error so the operator notices a stale lifecycle
//     row blocking re-trigger.
//  4. Transition HEALTHY → FAILED_OVER → EMERGENCY_PROVISIONING. The
//     intermediate FAILED_OVER state is intentionally transient — Plan
//     06 evaluateEmergencyProvisioning will pick up the new state on
//     the next tick and call startProvisioning.
//
// Plan 06-05 stops here — no Vast.ai call, no lifecycle INSERT (the
// trigger fires whenever sustained ≥ threshold; the provisioning path
// lands in Plan 06-06 and is responsible for the lifecycle INSERT).
func (r *Reconciler) evaluateHealthy(ctx context.Context, now time.Time, log *slog.Logger) {
	sustained := r.tracker.SustainedFailedOverSeconds()
	if sustained < int64(r.deps.Cfg.ProvisionTriggerFailedOverSeconds) {
		return
	}
	// D-C5 reconciler check — never spawn a second lifecycle while one
	// is live. The partial unique index `emergency_live_singleton`
	// (Plan 06-02) is the authoritative gate at INSERT time; this query
	// is a defensive pre-check so we surface the conflict in logs
	// instead of as a Postgres error during Plan 06-06's INSERT.
	if r.q != nil {
		live, err := r.q.ListLiveEmergencyLifecycles(ctx)
		if err != nil {
			log.Error("query live lifecycles failed; skipping trigger",
				"err", err, "sustained_seconds", sustained)
			return
		}
		if len(live) > 0 {
			log.Error("live lifecycle exists; trigger blocked (D-C5 reconciler check)",
				"count", len(live), "live_id", live[0].ID, "sustained_seconds", sustained)
			return
		}
	}
	log.Info("emergency trigger fired",
		"sustained_seconds", sustained,
		"threshold", r.deps.Cfg.ProvisionTriggerFailedOverSeconds)
	// Two-step transition: HEALTHY → FAILED_OVER (transient marker —
	// records the trigger time on the FSM enteredAt clock) →
	// EMERGENCY_PROVISIONING (the state the next tick consumes).
	r.deps.FSM.Transition(StateHealthy, StateFailedOver, now, "local_llm_open_sustained")
	r.deps.FSM.Transition(StateFailedOver, StateEmergencyProvisioning, now, "trigger_failed_over_sustained")
}

// applyEmergCommand dispatches a typed EmergEvent received on
// gw:emerg:events to the appropriate handler. Plan 06-05 Task 3
// (BLOCKER 2 fix 2026-05-13):
//
//   - force_provision_request: leader-only INSERT lifecycle row with
//     trigger_reason='manual_force' + advance FSM HEALTHY →
//     EMERGENCY_PROVISIONING. Non-leader replicas observe the event but
//     do NOT mutate state (single-leader invariant PRV-03).
//   - force_destroy_request:   leader-only call destroyAndCloseLifecycle
//     with shutdown_reason='manual'. Plan 08 owns the helper
//     implementation; Plan 05 ships a logging-only stub so the consumer
//     wiring + leader-only filter are testable in isolation.
//   - transition / cancel_in_flight / lifecycle_close / unknown:
//     visibility-only — log at Debug and return.
//
// The leader-only filter intentionally runs BEFORE the type switch so
// every command type observes identical filtering semantics (no
// per-type bypass).
func (r *Reconciler) applyEmergCommand(ctx context.Context, ev redisx.EmergEvent, log *slog.Logger) {
	if !r.isLeader.Load() {
		log.Debug("non-leader observed emerg command; ignoring",
			"type", ev.Type, "by_replica", ev.ReplicaID)
		return
	}
	switch ev.Type {
	case "force_provision_request":
		r.handleForceProvision(ctx, ev, log)
	case "force_destroy_request":
		r.handleForceDestroy(ctx, ev, log)
	case "transition", "cancel_in_flight", "lifecycle_close":
		// Self-published or cross-replica visibility events — leader
		// already authored these; no action required.
		return
	default:
		log.Debug("unknown emerg event type; ignoring", "type", ev.Type)
	}
}

// handleForceProvision is the leader-side handler for a
// force_provision_request command. INSERTs a lifecycle row with
// trigger_reason='manual_force' BEFORE the FSM transition (D-C5: the
// partial unique index is the gate; INSERT-first surfaces conflicts via
// pg unique violation rather than a silent FSM-only transition). On
// success, transitions HEALTHY → FAILED_OVER → EMERGENCY_PROVISIONING.
//
// Plan 06-05 stops at the FSM transition — Plan 06-06
// evaluateEmergencyProvisioning will pick up the new state on the next
// tick and drive the Vast.ai provisioning path.
func (r *Reconciler) handleForceProvision(ctx context.Context, ev redisx.EmergEvent, log *slog.Logger) {
	if r.q == nil {
		log.Error("force-provision rejected: no DB pool wired (test misconfiguration)",
			"by_replica", ev.ReplicaID)
		return
	}
	live, err := r.q.ListLiveEmergencyLifecycles(ctx)
	if err != nil {
		log.Error("force-provision: query live lifecycles failed",
			"err", err, "by_replica", ev.ReplicaID)
		return
	}
	if len(live) > 0 {
		log.Warn("force-provision rejected: live lifecycle already exists",
			"live_count", len(live), "live_id", live[0].ID, "by_replica", ev.ReplicaID)
		return
	}
	id, err := r.q.InsertEmergencyLifecycle(ctx, gen.InsertEmergencyLifecycleParams{
		TriggerReason: "manual_force",
		LeaderReplica: pgtype.Text{String: r.replicaID, Valid: true},
	})
	if err != nil {
		log.Error("force-provision: InsertEmergencyLifecycle failed",
			"err", err, "by_replica", ev.ReplicaID)
		return
	}
	r.activeLifecycle.Store(&ActiveLifecycle{
		ID:          id,
		StartedUnix: time.Now().Unix(),
	})
	now := time.Now()
	r.deps.FSM.Transition(StateHealthy, StateFailedOver, now, "manual_force_provision:"+ev.Reason)
	r.deps.FSM.Transition(StateFailedOver, StateEmergencyProvisioning, now, "manual_force_provision:"+ev.Reason)
	log.Info("force-provision accepted",
		"lifecycle_id", id, "reason", ev.Reason, "by_replica", ev.ReplicaID)
}

// handleForceDestroy is the leader-side handler for a
// force_destroy_request command. When no active lifecycle exists, logs
// Warn and returns (no-op). When a live lifecycle exists, delegates to
// destroyAndCloseLifecycle — a Plan 06-08 helper — and transitions FSM
// → COOLDOWN on success.
//
// Plan 06-05 ships a logging-only stub for destroyAndCloseLifecycle so
// the subscriber wiring + leader-only filter + no-op-when-idle path are
// testable. The integration test for the active-lifecycle destroy path
// is deferred to Plan 06-08 alongside the helper itself.
func (r *Reconciler) handleForceDestroy(ctx context.Context, ev redisx.EmergEvent, log *slog.Logger) {
	lc := r.activeLifecycle.Load()
	if lc == nil {
		log.Warn("force-destroy: no active lifecycle to destroy",
			"by_replica", ev.ReplicaID)
		return
	}
	if err := r.destroyAndCloseLifecycle(ctx, lc, "manual"); err != nil {
		log.Error("force-destroy failed",
			"id", lc.ID, "err", err, "by_replica", ev.ReplicaID)
		return
	}
	now := time.Now()
	r.deps.FSM.Transition(r.deps.FSM.State(), StateCooldown, now, "manual_force_destroy")
	r.cooldownEnteredAt.Store(now.Unix())
	log.Info("force-destroy accepted",
		"lifecycle_id", lc.ID, "by_replica", ev.ReplicaID)
}

// destroyAndCloseLifecycle is the Plan 06-08 cutback + force-destroy
// helper. Single shared path so the audit row close + Vast destroy +
// dispatcher RestoreTier0 + cost calculation happen in lock-step
// regardless of trigger (operator force_destroy_request from
// gatewayctl OR D-D1 idle-grace cutback).
//
// Sequence (W7 revision 2026-05-13: events JSONB written FIRST so the
// audit trail completes even if subsequent in-process operations fail):
//
//  1. Append `lifecycle_close` event JSONB via closeLifecycle (the
//     existing Plan 06 helper which itself emits the event with
//     {reason, total_cost_brl}).
//  2. RestoreTier0("llm") if we did not already (ride-out from cutback
//     idle-grace OR force_destroy from operator while ACTIVE).
//  3. bestEffortDestroy(VastInstanceID) using a fresh background ctx
//     with destroyShutdownBudget = 30s (Pitfall 8).
//  4. Cost calculation is folded into closeLifecycle via
//     calculateCostBRL — D-D4 hours_active = NOW() - first_health_pass_at.
//
// closeLifecycle handles activeLifecycle.Store(nil) +
// activePodURL.Store(nil) + lifecycleCancel.Swap(nil) → CancelFunc(),
// so this helper does not need to repeat them. The cancel propagates
// to any goroutine still running for the lifecycle (e.g. healthcheck
// resume loop) so they exit cleanly.
//
// Returns the closeLifecycle error if any. The caller (force-destroy
// handler in reconciler.go OR evaluateRecovering in the same file) is
// responsible for FSM transitions — destroyAndCloseLifecycle is a pure
// data-plane helper.
func (r *Reconciler) destroyAndCloseLifecycle(ctx context.Context, lc *ActiveLifecycle, reason string) error {
	if lc == nil {
		return errors.New("destroyAndCloseLifecycle: nil lifecycle")
	}
	if r.q == nil {
		return errors.New("destroyAndCloseLifecycle: no DB pool wired")
	}
	r.deps.Log.Info("destroyAndCloseLifecycle (Plan 06-08)",
		"lifecycle_id", lc.ID, "vast_instance_id", lc.VastInstanceID, "reason", reason)
	// Step 2 — restore tier-0 routing. Idempotent (atomic Store(nil) of
	// already-nil is cheap). Must happen BEFORE destroy so any in-flight
	// dispatcher request that resolves AFTER this point routes back to
	// the primary, not the about-to-be-destroyed pod.
	if r.deps.Loader != nil {
		r.deps.Loader.RestoreTier0("llm")
	}
	// Step 3 — destroy Vast.ai instance. Best-effort: failure is logged
	// + swallowed by the helper; the orphan-recovery branch on the next
	// leader acquisition will reconcile any leak.
	r.bestEffortDestroy(lc.VastInstanceID)
	// Step 1 (intentionally last in code order, but the actual SQL UPDATE
	// runs INSIDE closeLifecycle which itself emits the
	// `lifecycle_close` event via mustEventJSON before the UPDATE — the
	// W7 "events first" invariant is preserved by closeLifecycle's
	// internal sequencing) — see lifecycle.go closeLifecycle for the
	// SQL ordering. We need the destroy to happen before the close so
	// the audit row reflects the actual destroy outcome (cost = hours
	// from first_health_pass_at to NOW(); the destroy doesn't affect
	// the calculation but a future enhancement might want to record
	// destroy_at separately).
	//
	// Use the calc helper to get cost for the close event payload —
	// closeLifecycle re-runs the calc internally, but we'd like the
	// breadcrumb here to carry it for forensics.
	cost := r.calculateCostBRL(ctx, lc.ID, 0)
	if err := r.closeLifecycle(ctx, lc.ID, reason, 0); err != nil {
		r.deps.Log.Error("destroyAndCloseLifecycle: closeLifecycle failed",
			"lifecycle_id", lc.ID, "reason", reason, "err", err)
		return err
	}
	r.captureBreadcrumb("destroy_and_close", map[string]any{
		"lifecycle_id":     lc.ID,
		"vast_instance_id": lc.VastInstanceID,
		"reason":           reason,
		"total_cost_brl":   cost,
	})
	return nil
}
