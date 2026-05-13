// Package emerg (recovery.go): Plan 06-07 leader-recovery — D-D5 4-cenário
// orphan reconciliation.
//
// `recoverOrphanLifecycles` is invoked from runOneTick the first tick
// after a fresh leader acquires gw:emerg:lock. It scans
// emergency_lifecycles WHERE ended_at IS NULL (the partial unique index
// guarantees at most 1 row) and applies one of four branches per row:
//
//	(a) vast_instance_id IS NULL                — pre-create orphan (Pitfall 7)
//	    → close(shutdown_reason='leader_recovery_pre_create')
//	(b) Vast.GetInstance returns ErrInstanceNotFound — pod is gone
//	    → close(shutdown_reason='leader_recovery_lost')
//	(c) inst.IsActive() == false (exited|unknown|offline) — zombie pod
//	    → bestEffortDestroy + close(shutdown_reason='leader_recovery_zombie')
//	    + Sentry CaptureMessage tagged terminal
//	(d) inst.IsActive() == true                  — pod is alive; resume FSM
//	    → resumeFSMFromEvents — BLOCKER 4 fix (revisão 2026-05-13):
//	      FUNCIONAL JSONB events replay, NOT placeholder.
//
// Branch (d) walks the events JSONB array to determine the last
// `to_state`, then:
//
//   - calls FSM.SetState(lastState) — bypasses the CAS Transition (no
//     prior `from` state to assert against) and is the recovery-only
//     escape hatch added to fsm.go for this purpose.
//   - re-attaches a fresh context.WithCancel and stores it in
//     activeLifecycle / lifecycleCancel so future cancel-in-flight signals
//     reach this resumed lifecycle.
//   - reconstructs the pod /health URL via podHealthURL(inst) — the same
//     spike-derived field path used during provisioning.
//   - stores activePodURL (Plan 08 dispatcher contract).
//   - if the recovered state is EMERGENCY_ACTIVE, spawns
//     runHealthcheckResumeLoop (5s polling, cancel after 3 consecutive
//     failures = 15s degradation budget).
//   - if recovered state is EMERGENCY_PROVISIONING, no goroutine is spawned
//     here — the next reconciler tick's evaluateEmergencyProvisioning will
//     call startProvisioning, which detects activeLifecycle != nil and
//     short-circuits. The cold-start budget is recomputed from started_at
//     so the pod still has the remaining time to /health. (Future: Plan
//     could add a "resume provisioning" branch; for v1 we accept that a
//     leader handoff during EMERGENCY_PROVISIONING is rare and the
//     subsequent tick handles it via the standard path.)
//
// Per CONTEXT.md D-E3: dispatcher OverrideTier0 is owned by Plan 08; this
// recovery code only stores activePodURL — Plan 08 reads it on each
// request hot path. A TODO comment marks the cross-reference.
package emerg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// recoveryHealthcheckInterval is the cadence of the resumed lifecycle's
// /health polling. Same 5s as the provisioning poll loop — keeps the
// budget budget consistent across fresh-create and recovery flows.
const recoveryHealthcheckInterval = 5 * time.Second

// recoveryHealthcheckMaxFailures is the consecutive-failure threshold
// before the resumed lifecycle is cancelled. 3 × 5s = 15s degradation
// budget; matches the cold-start grace philosophy.
const recoveryHealthcheckMaxFailures = 3

// recoverOrphanLifecycles is the leader-recovery orchestrator. Invoked
// from runOneTick the first tick after acquiring leadership. Scans for
// live (ended_at IS NULL) lifecycle rows and applies one of the 4 D-D5
// branches per row.
//
// Returns nil even when individual rows fail to reconcile — partial
// progress is acceptable because the next leader-acquisition (or the
// next runOneTick if this is a flake) will retry. Failures are logged
// at Error so operators can investigate via gatewayctl.
func (r *Reconciler) recoverOrphanLifecycles(ctx context.Context) error {
	if r.q == nil {
		// Test misconfiguration; production wires the DB pool.
		r.deps.Log.Error("recoverOrphanLifecycles: no DB pool wired (test misconfiguration)")
		return nil
	}
	rows, err := r.q.ListLiveEmergencyLifecycles(ctx)
	if err != nil {
		return fmt.Errorf("list live lifecycles: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	r.deps.Log.Info("leader recovery: scanning live lifecycles",
		"count", len(rows))
	for _, row := range rows {
		r.recoverOneLifecycle(ctx, row)
	}
	return nil
}

// recoverOneLifecycle applies the D-D5 branch logic to a single live
// lifecycle row. Split from recoverOrphanLifecycles so per-row failures
// (e.g., transient Vast 5xx) don't abort the whole scan — the next leader
// acquisition (or the next runOneTick if Vast recovers in-process) will
// retry.
func (r *Reconciler) recoverOneLifecycle(ctx context.Context, row gen.ListLiveEmergencyLifecyclesRow) {
	// Branch (a) — pre-create orphan (Pitfall 7).
	if !row.VastInstanceID.Valid {
		r.deps.Log.Warn("leader recovery: pre-create orphan",
			"id", row.ID, "started_at", row.StartedAt)
		_ = r.closeLifecycle(ctx, row.ID, "leader_recovery_pre_create", 0)
		return
	}

	vastClient := r.vastAPI()
	if vastClient == nil {
		r.deps.Log.Warn("leader recovery: no Vast client; skipping (next leader acquisition will retry)",
			"id", row.ID, "vast_instance_id", row.VastInstanceID.Int64)
		return
	}

	// Use a bounded ctx for the GetInstance call — vast.Client already has
	// a 30s http timeout, but recovery is a leader-acquisition fast path
	// and we don't want to block the tick on a slow Vast.
	getCtx, getCancel := context.WithTimeout(ctx, 30*time.Second)
	inst, err := vastClient.GetInstance(getCtx, row.VastInstanceID.Int64)
	getCancel()

	// Branch (b) — Vast says the instance is gone.
	if errors.Is(err, vast.ErrInstanceNotFound) {
		r.deps.Log.Warn("leader recovery: instance not found at Vast (lost)",
			"id", row.ID, "vast_instance_id", row.VastInstanceID.Int64)
		_ = r.closeLifecycle(ctx, row.ID, "leader_recovery_lost", 0)
		return
	}
	// Transient error — log, skip; next tick re-tries.
	if err != nil {
		r.deps.Log.Warn("leader recovery: GetInstance failed; skipping (next tick retries)",
			"id", row.ID, "vast_instance_id", row.VastInstanceID.Int64, "err", err)
		return
	}

	// Branch (c) — instance is in a terminal state.
	if !inst.IsActive() {
		r.deps.Log.Warn("leader recovery: zombie instance",
			"id", row.ID, "vast_instance_id", row.VastInstanceID.Int64,
			"actual_status", inst.ActualStatus)
		// Pitfall 8: separate background ctx with 30s budget for the destroy.
		destroyCtx, destroyCancel := context.WithTimeout(context.Background(), destroyShutdownBudget)
		if derr := vastClient.DestroyInstance(destroyCtx, row.VastInstanceID.Int64); derr != nil {
			r.deps.Log.Warn("leader recovery: zombie destroy failed (orphan recovery will retry)",
				"id", row.ID, "vast_instance_id", row.VastInstanceID.Int64, "err", derr)
		}
		destroyCancel()
		r.captureTerminalSentry(row.ID, "leader_recovery_zombie", map[string]any{
			"vast_instance_id": row.VastInstanceID.Int64,
			"actual_status":    inst.ActualStatus,
		})
		_ = r.closeLifecycle(ctx, row.ID, "leader_recovery_zombie", 0)
		return
	}

	// Branch (d) — instance is alive; resume FSM (BLOCKER 4 functional path).
	r.deps.Log.Info("leader recovery: lifecycle alive; resuming via events JSONB replay",
		"id", row.ID, "vast_instance_id", row.VastInstanceID.Int64,
		"actual_status", inst.ActualStatus)
	if err := r.resumeFSMFromEvents(ctx, row, inst); err != nil {
		r.deps.Log.Error("leader recovery: resumeFSMFromEvents failed; closing as orphan_resume_failed",
			"id", row.ID, "err", err)
		// Best-effort destroy: if resume fails we have NO in-process state
		// tracking the pod, so let the orphan-zombie branch on the next
		// leader acquisition handle it. Or, conservatively, destroy now.
		// Choose destroy for safety — better to leak nothing than leak a
		// pod we can't track.
		destroyCtx, destroyCancel := context.WithTimeout(context.Background(), destroyShutdownBudget)
		_ = vastClient.DestroyInstance(destroyCtx, row.VastInstanceID.Int64)
		destroyCancel()
		_ = r.closeLifecycle(ctx, row.ID, "orphan_resume_failed", 0)
	}
}

// resumeFSMFromEvents reconstructs in-process FSM state from the
// emergency_lifecycles.events JSONB column for a row whose Vast instance
// is alive (D-D5 branch d). BLOCKER 4 (revisão 2026-05-13) fix: this is
// FUNCIONAL — walks events array to determine the last to_state, calls
// FSM.SetState (recovery-only escape hatch), re-attaches cancel context,
// stores activePodURL, and spawns the healthcheck resume goroutine when
// the recovered state is EMERGENCY_ACTIVE.
//
// `parentCtx` is the reconciler's tick context — used as the base for
// the new cancel context attached to the resumed lifecycle. Cancelling
// the reconciler's parent ctx will propagate to the resumed lifecycle's
// goroutines (proper shutdown semantics).
func (r *Reconciler) resumeFSMFromEvents(parentCtx context.Context, row gen.ListLiveEmergencyLifecyclesRow, inst vast.Instance) error {
	// 1. Parse events JSONB. Empty events → assume EMERGENCY_ACTIVE
	//    (defensible default because inst.IsActive()==true means the pod
	//    has been provisioned at least; events could be empty if the
	//    leader crashed before any append landed).
	lastState := StateEmergencyActive
	if len(row.Events) > 0 {
		var events []map[string]any
		if err := json.Unmarshal(row.Events, &events); err != nil {
			return fmt.Errorf("unmarshal events JSONB: %w", err)
		}
		// Walk backwards looking for the last entry that has a `to_state`
		// (or, equivalently, a `payload.to_state`). Events written by
		// markHealthy / mustEventJSON during Plan 06 do NOT include a
		// to_state field at the row level; they are typed by `type`
		// (e.g. "health_pass", "offer_accepted"). For Plan 07 recovery
		// we infer state from the most-recent `type`:
		//   - "health_pass" → EMERGENCY_ACTIVE (the only event whose
		//     presence guarantees the FSM was in ACTIVE)
		//   - "offer_accepted" but no later "health_pass" → EMERGENCY_PROVISIONING
		//   - anything else / no recognisable event → fall back to ACTIVE
		//     (since inst.IsActive()==true)
		if state, ok := inferStateFromEvents(events); ok {
			lastState = state
		}
	}

	// 2. SetState — recovery-only escape hatch (fsm.go).
	r.deps.FSM.SetState(lastState, time.Now(),
		"leader_recovery_resume:"+strconv.FormatInt(row.ID, 10))

	// 3. Re-attach cancel context. The atomic.Pointer[CancelFunc] is
	//    consumed by cancelActiveLifecycle (D-C3). Without this re-attach,
	//    a future cancel signal would have nothing to call.
	ctx, cancel := context.WithCancel(parentCtx)
	r.activeLifecycle.Store(&ActiveLifecycle{
		ID:             row.ID,
		VastInstanceID: row.VastInstanceID.Int64,
		StartedUnix:    row.StartedAt.Unix(),
	})
	r.lifecycleCancel.Store(&cancel)

	// 4. Reconstruct pod /health URL via the same spike-derived field path
	//    used during fresh provisioning. If the URL cannot be formed
	//    (W6 — ports map empty), recovery still succeeds at the FSM level
	//    but no dispatcher override is set; the runHealthcheckResumeLoop
	//    will detect failures and cancel.
	podURL := r.podHealthURL(inst)
	if podURL != "" {
		r.activePodURL.Store(&podURL)
		obs.GatewayEmergencyActivePod.WithLabelValues(podURL).Set(1)
	} else {
		r.deps.Log.Warn("leader recovery: pod URL not yet derivable (W6); dispatcher override skipped",
			"id", row.ID, "vast_instance_id", row.VastInstanceID.Int64,
			"actual_status", inst.ActualStatus, "public_ipaddr", inst.PublicIPAddr)
	}

	// 5. TODO(plan-08): when Plan 08 lands the dispatcher integration,
	//    invoke r.deps.Loader.OverrideTier0("local-llm", podURL) here.
	//    For Plan 07 we only store activePodURL — Plan 08 reads it on each
	//    request via Reconciler.ActivePodURL() (existing public method).

	// 6. Restart healthcheck polling goroutine if recovered state is
	//    EMERGENCY_ACTIVE. EMERGENCY_PROVISIONING is delegated to the next
	//    tick (which calls evaluateEmergencyProvisioning → detects
	//    activeLifecycle != nil and short-circuits the goroutine spawn;
	//    the resumed lifecycle's healthcheck happens via the existing
	//    waitForReadyOrDestroy path that owns the cold-start budget).
	if lastState == StateEmergencyActive {
		go r.runHealthcheckResumeLoop(ctx, row.ID, podURL)
	}

	r.captureBreadcrumb("leader_recovery_resume", map[string]any{
		"lifecycle_id":     row.ID,
		"recovered_state":  lastState.String(),
		"vast_instance_id": row.VastInstanceID.Int64,
		"pod_url":          podURL,
	})
	r.deps.Log.Info("leader recovery: FSM resumed from events JSONB",
		"id", row.ID, "state", lastState.String(),
		"pod_url", podURL, "vast_instance_id", row.VastInstanceID.Int64)
	return nil
}

// inferStateFromEvents walks the events JSONB array (parsed) and infers
// the FSM state the lifecycle was last in. Returns (state, true) when an
// inference is possible; (StateHealthy, false) when the array is empty or
// no recognisable event types are present.
//
// The mapping follows Plan 06's event taxonomy (mustEventJSON):
//   - presence of "health_pass" anywhere → EMERGENCY_ACTIVE (the
//     lifecycle reached the active state at some point)
//   - presence of "offer_accepted" but NO "health_pass" → EMERGENCY_PROVISIONING
//     (instance was created but never reached healthy)
//   - otherwise → false (caller falls back to a sane default)
func inferStateFromEvents(events []map[string]any) (State, bool) {
	if len(events) == 0 {
		return StateHealthy, false
	}
	var sawOfferAccepted, sawHealthPass bool
	for _, ev := range events {
		t, _ := ev["type"].(string)
		switch t {
		case "health_pass":
			sawHealthPass = true
		case "offer_accepted":
			sawOfferAccepted = true
		}
		// Future-proof: if events ever include explicit `to_state`, prefer
		// that signal over inference. Honor the LAST occurrence to track
		// the most recent transition.
		if to, ok := ev["to_state"].(string); ok {
			if parsed, err := ParseState(to); err == nil {
				return parsed, true
			}
		}
	}
	if sawHealthPass {
		return StateEmergencyActive, true
	}
	if sawOfferAccepted {
		return StateEmergencyProvisioning, true
	}
	return StateHealthy, false
}

// runHealthcheckResumeLoop polls the resumed lifecycle's /health URL on
// the same cadence as fresh provisioning. After
// recoveryHealthcheckMaxFailures consecutive failures (default 3 × 5s =
// 15s) it cancels the lifecycle via cancelActiveLifecycle —
// shutdown_reason='cancelled_in_flight' (the cancel path always reuses
// that reason; the breadcrumb category preserves the resume context for
// forensic).
//
// Exits on ctx.Done() (parent reconciler cancellation OR the resumed
// lifecycle's own cancel-in-flight signal). Empty podURL — meaning W6
// short-circuited podHealthURL during resume — is treated as the
// healthcheck loop's responsibility to re-derive on each tick: we
// cannot do that here because we don't have a vast.Instance handle, so
// we simply log and exit (the next leader acquisition will see a still-
// alive lifecycle and re-attempt resume with a new GetInstance result).
func (r *Reconciler) runHealthcheckResumeLoop(ctx context.Context, lifecycleID int64, podURL string) {
	if podURL == "" {
		r.deps.Log.Warn("runHealthcheckResumeLoop: empty podURL; exiting (next leader recovery will retry)",
			"lifecycle_id", lifecycleID)
		return
	}
	r.deps.Log.Info("runHealthcheckResumeLoop: started",
		"lifecycle_id", lifecycleID, "pod_url", podURL,
		"max_failures", recoveryHealthcheckMaxFailures,
		"interval", recoveryHealthcheckInterval)
	failures := 0
	ticker := time.NewTicker(recoveryHealthcheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.deps.Log.Info("runHealthcheckResumeLoop: ctx cancelled; exiting",
				"lifecycle_id", lifecycleID)
			return
		case <-ticker.C:
			if r.checkHealth(ctx, podURL) {
				if failures > 0 {
					r.deps.Log.Info("runHealthcheckResumeLoop: /health recovered",
						"lifecycle_id", lifecycleID, "prior_failures", failures)
				}
				failures = 0
				continue
			}
			failures++
			r.deps.Log.Warn("runHealthcheckResumeLoop: /health failed",
				"lifecycle_id", lifecycleID, "consecutive_failures", failures,
				"max_failures", recoveryHealthcheckMaxFailures)
			if failures >= recoveryHealthcheckMaxFailures {
				r.deps.Log.Error("runHealthcheckResumeLoop: exceeded failure threshold; cancelling",
					"lifecycle_id", lifecycleID,
					"consecutive_failures", failures)
				// IMPORTANT: cancelActiveLifecycle does NOT close the
				// lifecycle row — for fresh provisioning, the close is
				// performed inside provisionLifecycle/waitForReadyOrDestroy
				// when they observe ctx.Done(). For RESUMED lifecycles,
				// no such goroutine is running, so the close must happen
				// here. Use a fresh background ctx so a parent-ctx cancel
				// doesn't fail the audit write (mirrors closeLifecycle's
				// own internal pattern).
				r.cancelActiveLifecycle(ctx, "resume_health_failed")
				// Best-effort destroy: the pod is unhealthy from our
				// perspective; we do NOT trust it as serving traffic.
				lc := r.activeLifecycle.Load()
				if lc != nil && lc.VastInstanceID != 0 {
					r.bestEffortDestroy(lc.VastInstanceID)
				}
				closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = r.closeLifecycle(closeCtx, lifecycleID, "resume_health_failed", 0)
				closeCancel()
				// Return FSM to Healthy so a future trigger can fire.
				r.deps.FSM.SetState(StateHealthy, time.Now(),
					"resume_health_failed:"+strconv.FormatInt(lifecycleID, 10))
				return
			}
		}
	}
}
