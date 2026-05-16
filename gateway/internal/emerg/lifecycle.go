// Package emerg (lifecycle.go): Plan 06-06 emergency-pod provisioning
// goroutine — the SC-1 happy path "EMERGENCY_PROVISIONING →
// EMERGENCY_ACTIVE" via Vast.ai bid+create+/health-poll.
//
// # Goroutine layout
//
// `startProvisioning(ctx)` is called from `evaluateEmergencyProvisioning`
// (the StateEmergencyProvisioning branch of evaluateTick) when no
// activeLifecycle is currently in flight. It:
//
//  1. INSERTs the lifecycle row (D-D5: row exists BEFORE any Vast call so
//     leader-recovery can find orphans by `vast_instance_id IS NULL`).
//  2. Stores the activeLifecycle pointer (consumed by handleForceDestroy
//     and Plan 07 cancel-in-flight).
//  3. Spawns the long-running `provisionLifecycle` goroutine.
//  4. Records the provision duration on a Prometheus histogram.
//
// # provisionLifecycle algorithm
//
//   - SearchOffers (filter epsilon cap+0.0001 per Pitfall 5)
//   - Up to 3 attempts with 2s/4s/8s exponential backoff:
//   - CreateInstance
//   - On ErrOfferGone (404+no_such_ask), re-search and retry
//   - On any other error, abort
//   - On success, transition to waitForReadyOrDestroy
//   - On 3 race losses: Sentry CaptureMessage + close lifecycle with
//     shutdown_reason='offer_race_lost'.
//
// # waitForReadyOrDestroy algorithm
//
// Polls GetInstance every 5 seconds (configurable via
// `instancePollInterval`). Three exit paths:
//
//   - ctx.Done()                               → DestroyInstance + close('cancelled_in_flight')
//   - deadline (cfg.ProvisionColdStartBudgetSeconds) → DestroyInstance + close('health_timeout')
//   - actual_status ∈ {exited, unknown, offline} (Pitfall 9) → DestroyInstance + close('instance_terminal_state')
//   - actual_status==running AND public_ipaddr!=""
//     AND ports populated AND /health returns {status:healthy} → markHealthy + return nil
//
// # D-D3 events JSONB FIRST (W7 fix 2026-05-13)
//
// `UpdateEmergencyLifecycleVastIDs` is the FIRST DB write after
// CreateInstance succeeds — it carries the `offer_accepted` event JSONB
// in the same UPDATE as the vast_offer_id / vast_instance_id columns.
// Audit log atomicity: the events array reflects the in-flight state
// before any other in-process state mutation.
package emerg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/jackc/pgx/v5/pgtype"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

const (
	// instancePollInterval is the cadence of GetInstance polling inside
	// waitForReadyOrDestroy. 5s matches CONTEXT.md D-A4 ("polling /health
	// a cada 5s"); package-level constant rather than env-tunable because
	// re-tuning requires re-validating the Pitfall 9 terminal-detection
	// timing analysis.
	instancePollInterval = 5 * time.Second

	// destroyShutdownBudget is the timeout used for "best-effort" Destroy
	// calls during cancel/timeout/terminal exit paths. The parent ctx is
	// already cancelled (or the caller is exiting), so we mint a fresh
	// background ctx with this budget per Pitfall 8.
	destroyShutdownBudget = 30 * time.Second

	// healthCheckTimeout is the per-attempt HTTP timeout for the pod
	// /health probe. Pod is on a public IP so DNS/TCP could fan out;
	// we keep this small relative to the 5s poll cadence so a slow
	// pod does not cascade-timeout the budget.
	healthCheckTimeout = 4 * time.Second
)

// VastAPI is the subset of vast.Client methods provisionLifecycle calls.
// Stubbed in unit tests; production wires the real *vast.Client.
type VastAPI interface {
	SearchOffers(ctx context.Context, filter vast.SearchFilter) ([]vast.Offer, error)
	CreateInstance(ctx context.Context, offerID int64, req vast.CreateRequest) (vast.Instance, error)
	GetInstance(ctx context.Context, instanceID int64) (vast.Instance, error)
	DestroyInstance(ctx context.Context, instanceID int64) error
}

// HealthChecker is the pod /health probe interface. Stubbed in unit tests
// (override via Reconciler.healthCheck) so tests can simulate a healthy
// pod without spinning up a full HTTP server.
type HealthChecker func(ctx context.Context, url string) bool

// startProvisioning is the StateEmergencyProvisioning entry point. INSERTs
// the lifecycle row, stores the activeLifecycle pointer, and spawns the
// long-running provisionLifecycle goroutine. Returns immediately — the
// goroutine completes asynchronously (eventually flipping the FSM via
// markHealthy or closing it via one of the failure paths).
//
// MUST only be called by the leader (caller responsibility — typically
// from inside evaluateEmergencyProvisioning which already holds
// IsLeader()==true). Multiple concurrent calls are guarded by the
// activeLifecycle pointer + the partial unique DB index.
func (r *Reconciler) startProvisioning(parentCtx context.Context) {
	if r.q == nil {
		r.deps.Log.Error("startProvisioning: no DB pool wired (test misconfiguration)")
		return
	}
	if existing := r.activeLifecycle.Load(); existing != nil {
		// Defensive: should never happen because evaluateEmergencyProvisioning
		// gates on activeLifecycle==nil. Log and bail rather than spawn a
		// duplicate goroutine.
		r.deps.Log.Warn("startProvisioning called with active lifecycle; skipping",
			"existing_id", existing.ID)
		return
	}

	id, err := r.q.InsertEmergencyLifecycle(parentCtx, gen.InsertEmergencyLifecycleParams{
		TriggerReason: "failed_over_sustained",
		LeaderReplica: pgtype.Text{String: r.replicaID, Valid: true},
	})
	if err != nil {
		r.deps.Log.Error("startProvisioning: InsertEmergencyLifecycle failed", "err", err)
		return
	}
	r.activeLifecycle.Store(&ActiveLifecycle{
		ID:          id,
		StartedUnix: time.Now().Unix(),
	})

	r.spawnProvisionGoroutine(parentCtx, id)
}

// spawnProvisionGoroutine kicks off the long-running provisionLifecycle
// goroutine for an already-inserted lifecycle row whose activeLifecycle
// pointer has already been stored. Shared between startProvisioning (the
// auto-trigger HEALTHY → EMERGENCY_PROVISIONING path) and
// handleForceProvision (the operator-initiated path) so both code paths
// converge on identical SearchOffers → CreateInstance → markHealthy
// behaviour and identical error-routing semantics (Cooldown for
// offer_race_lost, Healthy for everything else).
//
// Pre-conditions enforced by callers, not re-checked here:
//   - r.q != nil
//   - r.activeLifecycle.Load() != nil (caller stored it with ID == id)
//   - FSM is already in StateEmergencyProvisioning
func (r *Reconciler) spawnProvisionGoroutine(parentCtx context.Context, id int64) {
	ctx, cancel := context.WithCancel(parentCtx)
	r.lifecycleCancel.Store(&cancel)

	go func() {
		defer cancel()
		start := time.Now()
		err := r.provisionLifecycle(ctx, id)
		obs.GatewayEmergencyProvisionDurationSeconds.Observe(time.Since(start).Seconds())
		if err != nil {
			reason := errReason(err)
			r.deps.Log.Error("provisionLifecycle returned error",
				"lifecycle_id", id, "err", err)
			// Re-trigger-loop fix (emerg-bid-race-lost debug session):
			// a provisioning FAILURE must NOT drop the FSM straight back
			// to Healthy. While the local-llm breaker is still OPEN,
			// evaluateHealthy would re-fire the trigger on the very next
			// tick — new lifecycle, +3 create_hits per cycle, unbounded
			// hammer loop against the Vast.ai spot market. Instead we
			// route the offer_race_lost abort through Cooldown: while the
			// FSM is IN Cooldown the reconciler dispatches to
			// evaluateCooldown (NOT evaluateHealthy), so the trigger is
			// dormant for the full ProvisionFailureCooldownSeconds window.
			// After it expires the system may re-attempt provisioning —
			// correct production behaviour for a transient bid race — but
			// now with a meaningful backoff.
			//
			// Other failure paths (cancelled_in_flight, health_timeout,
			// instance_terminal_state, no_offers_below_cap) keep the prior
			// behaviour of returning to Healthy: cancellation is a
			// deliberate recovery (local-llm came back) and the post-create
			// terminal paths have distinct semantics that the bid-race
			// backoff does not model. The lifecycle row already records
			// the precise shutdown_reason via closeLifecycle regardless of
			// the FSM target state.
			if errors.Is(err, ErrOfferRaceLost) {
				r.enterCooldown(StateEmergencyProvisioning, time.Now(),
					"provision_failure:"+reason, true)
			} else {
				r.deps.FSM.Transition(StateEmergencyProvisioning, StateHealthy,
					time.Now(), "provision_error:"+reason)
			}
		}
	}()
}

// errReason returns a stable short token suitable for logging / breadcrumb.
func errReason(err error) string {
	switch {
	case errors.Is(err, ErrOfferRaceLost):
		return "offer_race_lost"
	case errors.Is(err, ErrHealthTimeout):
		return "health_timeout"
	case errors.Is(err, ErrInstanceTerminal):
		return "instance_terminal_state"
	case errors.Is(err, ErrNoOffersBelowCap):
		return "no_offers_below_cap"
	case errors.Is(err, context.Canceled):
		return "cancelled_in_flight"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	}
	return "other"
}

// provisionLifecycle implements the SearchOffers → CreateInstance (with
// 3-attempt bid race retry) → waitForReadyOrDestroy flow. Returns nil on
// success (FSM is in StateEmergencyActive, lifecycle row has
// first_health_pass_at populated). On error, the lifecycle row has been
// closed (or will be closed) with the appropriate shutdown_reason.
func (r *Reconciler) provisionLifecycle(ctx context.Context, id int64) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	vastClient := r.vastAPI()
	if vastClient == nil {
		_ = r.closeLifecycle(ctx, id, "no_vast_client", 0)
		return errors.New("emerg: no Vast.ai client wired (VAST_AI_API_KEY missing)")
	}

	filter := vast.DefaultSearchFilter(r.deps.Cfg.VastPriceCapDPH, r.deps.Cfg.PrimaryHostID)
	offers, err := vastClient.SearchOffers(ctx, filter)
	if err != nil {
		_ = r.closeLifecycle(ctx, id, "search_failed", 0)
		return err
	}

	// Pitfall 5 — epsilon comparison `cap + 0.0001`. Defense in depth on
	// top of the server-side dph_total filter.
	pickable := filterBelowCap(offers, r.deps.Cfg.VastPriceCapDPH)
	if len(pickable) == 0 {
		r.deps.Log.Info("provisionLifecycle: no offers below cap",
			"cap", r.deps.Cfg.VastPriceCapDPH, "raw_offer_count", len(offers))
		_ = r.closeLifecycle(ctx, id, "no_offers_below_cap", 0)
		return ErrNoOffersBelowCap
	}

	// D-A3 — 3 attempts with 2s/4s/8s exponential backoff between
	// re-searches. Bid race window seconds (Pitfall 6).
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		offer := pickable[0]
		req := r.buildCreateRequest(offer, id)
		instance, createErr := vastClient.CreateInstance(ctx, offer.ID, req)
		if createErr == nil {
			// SUCCESS — record vast IDs + offer_accepted event in ONE UPDATE
			// (D-D3: events JSONB written FIRST per W7 revision 2026-05-13).
			eventJSON := mustEventJSON("offer_accepted", map[string]any{
				"offer_id":    offer.ID,
				"instance_id": instance.ID,
				"dph":         offer.DphTotal,
				"host_id":     offer.HostID,
				"machine_id":  offer.MachineID,
				"geolocation": offer.Geolocation,
				"attempt":     attempt + 1,
			})
			if err := r.q.UpdateEmergencyLifecycleVastIDs(ctx, gen.UpdateEmergencyLifecycleVastIDsParams{
				ID:             id,
				VastOfferID:    pgInt8(offer.ID),
				VastInstanceID: pgInt8(instance.ID),
				AcceptedDph:    pgNumericFromFloat(offer.DphTotal),
				EventJson:      eventJSON,
			}); err != nil {
				// Audit failure — destroy instance + close lifecycle
				// rather than leak a Vast pod whose DB row was never updated.
				r.bestEffortDestroy(instance.ID)
				_ = r.closeLifecycle(ctx, id, "audit_write_failed", 0)
				return err
			}
			// Update activeLifecycle snapshot with the instance ID.
			r.activeLifecycle.Store(&ActiveLifecycle{
				ID:             id,
				VastInstanceID: instance.ID,
				StartedUnix:    time.Now().Unix(),
			})
			r.captureBreadcrumb("offer_accepted", map[string]any{
				"lifecycle_id": id, "offer_id": offer.ID,
				"instance_id": instance.ID, "dph": offer.DphTotal,
			})
			obs.GatewayEmergencyCostDPH.WithLabelValues(strconv.FormatInt(id, 10)).Set(offer.DphTotal)
			return r.waitForReadyOrDestroy(ctx, id, instance.ID, offer.DphTotal)
		}

		if !errors.Is(createErr, vast.ErrOfferGone) {
			// Hard error — abort lifecycle.
			_ = r.closeLifecycle(ctx, id, "create_error", 0)
			return createErr
		}

		// Bid race lost — back off and re-search.
		r.captureBreadcrumb("offer_race_attempt", map[string]any{
			"lifecycle_id": id, "attempt": attempt + 1, "offer_id": offer.ID,
		})
		select {
		case <-ctx.Done():
			_ = r.closeLifecycle(ctx, id, "cancelled_in_flight", 0)
			return ctx.Err()
		case <-time.After(time.Duration(1<<attempt) * 2 * time.Second):
		}
		offers, err = vastClient.SearchOffers(ctx, filter)
		if err != nil {
			_ = r.closeLifecycle(ctx, id, "search_failed", 0)
			return err
		}
		pickable = filterBelowCap(offers, r.deps.Cfg.VastPriceCapDPH)
		if len(pickable) == 0 {
			_ = r.closeLifecycle(ctx, id, "no_offers_below_cap", 0)
			return ErrNoOffersBelowCap
		}
	}

	// 3 race losses — terminal abort.
	r.captureTerminalSentry(id, "offer_race_lost", map[string]any{"attempts": 3})
	_ = r.closeLifecycle(ctx, id, "offer_race_lost", 0)
	return ErrOfferRaceLost
}

// waitForReadyOrDestroy polls GetInstance every `instancePollInterval`
// until either the pod is healthy (return nil) OR a terminal exit path
// fires (return the appropriate sentinel error).
//
// Pitfall 9 (terminal states): exited / unknown / offline → destroy +
// close. Pitfall 1: actual_status==running ALONE is not enough — we
// also require public_ipaddr!="" + populated Ports + /health 200.
// Pitfall 6 (W6 fix): empty PublicIPAddr or empty Ports map means the
// container has not exposed its mapped port yet — keep polling, do NOT
// charge the pod cold-start budget for transient HTTP errors.
func (r *Reconciler) waitForReadyOrDestroy(ctx context.Context, lifecycleID, instanceID int64, acceptedDPH float64) error {
	poll := time.NewTicker(instancePollInterval)
	defer poll.Stop()
	deadline := time.NewTimer(time.Duration(r.deps.Cfg.ProvisionColdStartBudgetSeconds) * time.Second)
	defer deadline.Stop()
	vastClient := r.vastAPI()

	for {
		select {
		case <-ctx.Done():
			r.bestEffortDestroy(instanceID)
			_ = r.closeLifecycle(context.Background(), lifecycleID, "cancelled_in_flight", 0)
			return ctx.Err()

		case <-deadline.C:
			r.bestEffortDestroy(instanceID)
			_ = r.closeLifecycle(context.Background(), lifecycleID, "health_timeout", 0)
			r.captureTerminalSentry(lifecycleID, "health_timeout", map[string]any{
				"instance_id": instanceID,
				"budget_s":    r.deps.Cfg.ProvisionColdStartBudgetSeconds,
			})
			return ErrHealthTimeout

		case <-poll.C:
			inst, err := vastClient.GetInstance(ctx, instanceID)
			if err != nil {
				if errors.Is(err, vast.ErrInstanceNotFound) {
					// Vast destroyed the pod under us (host failure, spot
					// underbid). Surface as terminal.
					_ = r.closeLifecycle(context.Background(), lifecycleID, "instance_terminal_state", 0)
					return ErrInstanceTerminal
				}
				// Transient error — keep polling. Do NOT advance budget
				// state; the deadline timer is the source of truth.
				continue
			}
			if inst.IsTerminal() {
				r.bestEffortDestroy(instanceID)
				_ = r.closeLifecycle(context.Background(), lifecycleID, "instance_terminal_state", 0)
				r.captureTerminalSentry(lifecycleID, "instance_terminal_state", map[string]any{
					"instance_id":   instanceID,
					"actual_status": inst.ActualStatus,
				})
				return ErrInstanceTerminal
			}
			if inst.ActualStatus != "running" {
				// Still loading / scheduling — keep polling.
				continue
			}
			// W6 fix — public_ipaddr OR ports may be empty briefly even
			// after actual_status flips to running. Treat as not-yet-ready.
			healthURL := r.podHealthURL(inst)
			if healthURL == "" {
				continue
			}
			// Pitfall 1 — verify the pod actually serves /health.
			if !r.checkHealth(ctx, healthURL) {
				continue
			}
			// HEALTHY!
			if err := r.markHealthy(ctx, lifecycleID, healthURL, acceptedDPH); err != nil {
				r.deps.Log.Error("markHealthy failed; pod is healthy but DB write failed",
					"lifecycle_id", lifecycleID, "err", err)
				return err
			}
			return nil
		}
	}
}

// markHealthy is the success exit of waitForReadyOrDestroy. Updates the DB
// row (first_health_pass_at = NOW()), flips the FSM to EmergencyActive,
// stores the active pod URL, AND activates the dispatcher tier-0
// override (Plan 06-08, D-E3) so subsequent LLM requests route to the
// emergency pod instead of the (failed) primary.
//
// The dispatcher resets when evaluateEmergencyActive triggers cutback —
// see reconciler.go evaluateEmergencyActive for the RestoreTier0 call.
// Also reset r.lastEmergencyTrafficAt to NOW so the idle-grace timer
// in evaluateRecovering uses a sensible baseline (rather than 0 which
// would falsely classify a fresh-ACTIVE pod as immediately idle).
func (r *Reconciler) markHealthy(ctx context.Context, lifecycleID int64, healthURL string, acceptedDPH float64) error {
	eventJSON := mustEventJSON("health_pass", map[string]any{
		"lifecycle_id": lifecycleID,
		"health_url":   healthURL,
		"dph":          acceptedDPH,
	})
	if err := r.q.MarkEmergencyLifecycleHealthy(ctx, gen.MarkEmergencyLifecycleHealthyParams{
		ID:        lifecycleID,
		EventJson: eventJSON,
	}); err != nil {
		return err
	}
	r.activePodURL.Store(&healthURL)
	obs.GatewayEmergencyActivePod.WithLabelValues(healthURL).Set(1)
	// Plan 06-08 (D-E3): activate dispatcher tier-0 override. Use the
	// emergency pod's BASE URL (strip /health) as the upstream URL so the
	// dispatcher's ReverseProxy target matches the OpenAI-compatible
	// llama.cpp endpoint. podHealthURL produces e.g.
	// "http://1.2.3.4:40713/health"; the upstream URL is the same minus
	// "/health".
	if r.deps.Loader != nil {
		baseURL := stripHealthSuffix(healthURL)
		r.deps.Loader.OverrideTier0("llm", baseURL)
	}
	// Plan 06-08: arm the idle-grace timer with NOW so a fresh ACTIVE
	// pod is not immediately classified as idle (lastEmergencyTrafficAt
	// defaulted to 0 before any RegisterTraffic call lands).
	r.lastEmergencyTrafficAt.Store(time.Now().Unix())
	r.captureBreadcrumb("health_pass", map[string]any{
		"lifecycle_id": lifecycleID, "health_url": healthURL,
	})
	r.deps.FSM.Transition(StateEmergencyProvisioning, StateEmergencyActive, time.Now(), "health_passed")
	return nil
}

// stripHealthSuffix removes the readiness-probe suffix ("/v1/models" or
// the legacy "/health") from the given URL. Helper for markHealthy +
// leader-recovery resume so the dispatcher override receives the
// upstream BASE URL, not the probe URL. The legacy "/health" branch
// preserves compatibility with lifecycle rows that recorded the
// pre-LLM-only readiness URL during a previous gateway boot.
func stripHealthSuffix(u string) string {
	for _, suffix := range []string{"/v1/models", "/health"} {
		if len(u) > len(suffix) && u[len(u)-len(suffix):] == suffix {
			return u[:len(u)-len(suffix)]
		}
	}
	return u
}

// closeLifecycle is the single point of contact for closing a lifecycle
// row. Sets ended_at = NOW(), records shutdown_reason, calculates
// total_cost_brl per D-D4, and clears the activeLifecycle pointer.
//
// `acceptedDPH` is 0 when no instance was ever created (pre-create
// orphans, no_offers_below_cap, audit_write_failed); 0 cost is recorded.
//
// We use context.Background() for the DB write so a parent ctx
// cancellation does not also fail the audit write.
func (r *Reconciler) closeLifecycle(ctx context.Context, id int64, reason string, acceptedDPH float64) error {
	cost := r.calculateCostBRL(ctx, id, acceptedDPH)
	eventJSON := mustEventJSON("lifecycle_close", map[string]any{
		"reason":         reason,
		"total_cost_brl": cost,
	})
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dbCancel()
	if err := r.q.CloseEmergencyLifecycle(dbCtx, gen.CloseEmergencyLifecycleParams{
		ID:             id,
		ShutdownReason: pgtype.Text{String: reason, Valid: true},
		TotalCostBrl:   pgNumericFromFloat(cost),
		EventJson:      eventJSON,
	}); err != nil {
		r.deps.Log.Error("closeLifecycle: CloseEmergencyLifecycle failed",
			"id", id, "reason", reason, "err", err)
		return err
	}
	r.activeLifecycle.Store(nil)
	if cancelPtr := r.lifecycleCancel.Swap(nil); cancelPtr != nil {
		// Cancel the context for any inner goroutines tied to this lifecycle.
		(*cancelPtr)()
	}
	if podURLPtr := r.activePodURL.Swap(nil); podURLPtr != nil {
		obs.GatewayEmergencyActivePod.WithLabelValues(*podURLPtr).Set(0)
	}
	// Plan 06-08 (D-E3): defensive RestoreTier0 on every close. If the
	// override was already cleared (e.g. evaluateEmergencyActive's cutback
	// path called RestoreTier0 before destroyAndCloseLifecycle), this is
	// a cheap atomic.Pointer.Store(nil) no-op. Safety: prevents the
	// dispatcher from continuing to route to a pod whose lifecycle row
	// is closed (orphan dispatcher state).
	if r.deps.Loader != nil {
		r.deps.Loader.RestoreTier0("llm")
	}
	obs.GatewayEmergencyLifecyclesTotal.WithLabelValues("failed_over_sustained", reason).Inc()
	return nil
}

// calculateCostBRL implements D-D4: total_cost_brl = (DPH * hours_active)
// * USD_TO_BRL_RATE, where hours_active = (NOW() - first_health_pass_at).
// Returns 0 when first_health_pass_at IS NULL (cold-start failed before
// /health passed — Vast still bills, but our audit log only counts
// "useful" hours per D-D4).
func (r *Reconciler) calculateCostBRL(ctx context.Context, id int64, acceptedDPH float64) float64 {
	if acceptedDPH <= 0 {
		return 0
	}
	// Query first_health_pass_at; if NULL → 0 hours_active.
	var firstHealth pgtype.Timestamptz
	row := r.deps.DB.QueryRow(ctx, `SELECT first_health_pass_at FROM ai_gateway.emergency_lifecycles WHERE id = $1`, id)
	if err := row.Scan(&firstHealth); err != nil {
		r.deps.Log.Warn("calculateCostBRL: query first_health_pass_at failed",
			"id", id, "err", err)
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

// podHealthURL formats the /health URL from a vast.Instance. Emergency
// pods run only llama-server (no health-bridge), so the readiness probe
// targets llama-server's native /v1/models endpoint on container port
// 8000 — when this returns HTTP 200 with at least one model entry, the
// Qwen weights have been mmap'd onto the GPU and chat requests will
// succeed.
//
// Returns "" when the instance is not yet ready to serve traffic — the
// caller (waitForReadyOrDestroy) treats empty as "keep polling" rather
// than as an error (W6 fix).
func (r *Reconciler) podHealthURL(inst vast.Instance) string {
	if inst.PublicIPAddr == "" {
		return ""
	}
	bindings, ok := inst.Ports["8000/tcp"]
	if !ok || len(bindings) == 0 {
		return ""
	}
	port := bindings[0].HostPort
	if port == "" {
		return ""
	}
	return "http://" + inst.PublicIPAddr + ":" + port + "/v1/models"
}

// checkHealth issues a single GET against the pod readiness endpoint
// (podHealthURL — currently llama-server's /v1/models on port 8000).
// Returns true only when HTTP 200 + the OpenAI-compatible response body
// contains at least one model entry under data[]. llama-server only
// answers 200 after the weights are mmap'd onto the GPU and at least
// one slot is ready, which is the readiness signal the reconciler
// needs to flip the FSM to EmergencyActive.
//
// Tests can override via Reconciler.healthCheckOverride to short-circuit
// the HTTP path. Production always returns the default checker.
func (r *Reconciler) checkHealth(ctx context.Context, url string) bool {
	if r.healthCheckOverride != nil {
		return r.healthCheckOverride(ctx, url)
	}
	probeCtx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: healthCheckTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if err := json.Unmarshal(raw, &body); err != nil {
		return false
	}
	return len(body.Data) > 0
}

// vastAPI returns the configured VastAPI client. Unit tests override via
// Reconciler.vastOverride; production reads from r.deps.Vast (set by
// NewReconciler when Cfg.VastAIAPIKey is non-empty).
func (r *Reconciler) vastAPI() VastAPI {
	if r.vastOverride != nil {
		return r.vastOverride
	}
	return r.deps.Vast
}

// bestEffortDestroy issues DestroyInstance with a fresh background context
// + 30s budget. Errors are logged and swallowed — caller is already on a
// failure path and the orphan cleanup goroutine (Plan 07) will reconcile
// any leaks.
func (r *Reconciler) bestEffortDestroy(instanceID int64) {
	if instanceID == 0 {
		return
	}
	vastClient := r.vastAPI()
	if vastClient == nil {
		return
	}
	destroyCtx, cancel := context.WithTimeout(context.Background(), destroyShutdownBudget)
	defer cancel()
	if err := vastClient.DestroyInstance(destroyCtx, instanceID); err != nil {
		r.deps.Log.Warn("bestEffortDestroy failed; orphan recovery will reconcile",
			"instance_id", instanceID, "err", err)
	}
}

// buildCreateRequest assembles the CreateRequest body for PUT /asks/{id}/.
// The image tag comes from Cfg.EmergencyPodImageTag (default "v1.0").
// The `Env` map encodes the Docker -p port mappings (Vast convention).
// Onstart is a one-line bash sentinel — Plan 06 only verifies port
// mapping discovery; the production onstart pulls weights from MinIO
// (Phase 1 image already handles it).
func (r *Reconciler) buildCreateRequest(offer vast.Offer, lifecycleID int64) vast.CreateRequest {
	return vast.CreateRequest{
		ClientID: "me",
		Image:    "ghcr.io/ifixtelecom/ifix-ai-pod:" + r.deps.Cfg.EmergencyPodImageTag,
		Env: map[string]string{
			// Vast.ai Docker port forwarding convention (per spike capture):
			// keys are literal `-p HOST_PORT:CONTAINER_PORT` flag strings.
			// Emergency pods serve LLM-only (D-C2), so only 8000 is exposed;
			// Whisper/embed/health-bridge ports from the Phase 1 multi-service
			// stack are intentionally omitted.
			"-p 8000:8000": "1",
			// Only Qwen weights are needed for the LLM-only emergency pod.
			// Whisper + BGE keys/hashes still forwarded so Phase 1 host-mode
			// pod deployments (smoke.yml) keep working with the same image —
			// emerg-bootstrap.sh ignores them; Phase 1 onstart.sh consumes
			// them when present.
			"MINIO_ENDPOINT":         r.deps.Cfg.MinioEndpoint,
			"MINIO_BUCKET":           r.deps.Cfg.MinioBucket,
			"MINIO_ACCESS_KEY":       r.deps.Cfg.MinioAccessKey,
			"MINIO_SECRET_KEY":       r.deps.Cfg.MinioSecretKey,
			"WEIGHTS_QWEN_KEY":       r.deps.Cfg.WeightsQwenKey,
			"WEIGHTS_QWEN_SHA256":    r.deps.Cfg.WeightsQwenSHA256,
			"WEIGHTS_WHISPER_KEY":    r.deps.Cfg.WeightsWhisperKey,
			"WEIGHTS_WHISPER_SHA256": r.deps.Cfg.WeightsWhisperSHA256,
			"WEIGHTS_BGE_M3_KEY":     r.deps.Cfg.WeightsBGEM3Key,
			"WEIGHTS_BGE_M3_SHA256":  r.deps.Cfg.WeightsBGEM3SHA256,
		},
		// Emergency pods run the image's baked-in CMD (emerg-bootstrap.sh),
		// which downloads qwen weights from MinIO then execs llama-server.
		// No Onstart hook needed — Vast.ai's onstart runs on the VM host,
		// not inside the container, and the host has no application code.
		Onstart:     "",
		Runtype:     "ssh",
		Disk:        50,
		Label:       fmt.Sprintf("ifix-emerg-lifecycle-%d", lifecycleID),
		TargetState: "running",
	}
}

// filterBelowCap applies the Pitfall 5 epsilon comparison cap+0.0001 to
// the offer list. Defense in depth on top of the server-side dph_total
// filter (which can include hosts that priced at exactly cap+1e-6 due to
// float rounding upstream).
func filterBelowCap(offers []vast.Offer, cap float64) []vast.Offer {
	out := make([]vast.Offer, 0, len(offers))
	for _, o := range offers {
		if o.DphTotal > cap+0.0001 {
			continue
		}
		out = append(out, o)
	}
	return out
}

// excludeHost removes any offer whose HostID matches the given host. Used
// when the primary host is known to avoid bidding on the same physical
// machine (D-A2 host_id != filter). Returns a fresh slice.
func excludeHost(offers []vast.Offer, hostID int64) []vast.Offer {
	if hostID <= 0 {
		return offers
	}
	out := make([]vast.Offer, 0, len(offers))
	for _, o := range offers {
		if o.HostID == hostID {
			continue
		}
		out = append(out, o)
	}
	return out
}

// mustEventJSON marshals a single event row {ts, type, payload} for the
// emergency_lifecycles.events JSONB column. Returns a length-1 JSON array
// (the SQL `events || $::jsonb` operator requires the right side to be
// JSONB-compatible — wrapping in [...] keeps the array-of-events shape).
func mustEventJSON(eventType string, payload map[string]any) []byte {
	row := map[string]any{
		"ts":      time.Now().UTC().Format(time.RFC3339Nano),
		"type":    eventType,
		"payload": payload,
	}
	arr := []map[string]any{row}
	out, err := json.Marshal(arr)
	if err != nil {
		// json.Marshal on a map[string]any with primitive values cannot
		// realistically fail; use a fallback rather than panic to keep
		// the goroutine alive.
		return []byte(`[{"type":"event_marshal_failed"}]`)
	}
	return out
}

// pgInt8 wraps an int64 as a non-null pgtype.Int8 (sqlc's BIGINT mapping).
func pgInt8(v int64) pgtype.Int8 {
	return pgtype.Int8{Int64: v, Valid: true}
}

// pgNumericFromFloat converts a float64 to pgtype.Numeric. Used for
// accepted_dph (NUMERIC(6,4)) and total_cost_brl (NUMERIC(10,4)). Values
// are rounded to 4 decimal places — matches the column scale.
func pgNumericFromFloat(v float64) pgtype.Numeric {
	if v == 0 {
		return pgtype.Numeric{Int: big.NewInt(0), Exp: 0, Valid: true}
	}
	// Multiply by 10^4 to capture 4 decimal places, then truncate to int.
	scaled := int64(v * 10000)
	return pgtype.Numeric{Int: big.NewInt(scaled), Exp: -4, Valid: true}
}

// captureBreadcrumb adds a Sentry breadcrumb at the info level. Used for
// non-terminal events (offer_accepted, instance_created, health_pass).
// Per D-E4 — breadcrumbs ride along the next CaptureMessage so terminal
// errors land in Sentry with the full lifecycle timeline attached.
func (r *Reconciler) captureBreadcrumb(category string, data map[string]any) {
	sentry.AddBreadcrumb(&sentry.Breadcrumb{
		Category:  "emerg." + category,
		Message:   category,
		Level:     sentry.LevelInfo,
		Timestamp: time.Now(),
		Data:      data,
	})
}

// captureTerminalSentry emits a Sentry CaptureMessage with WARNING level
// + tags subsystem=emerg + lifecycle_id + shutdown_reason. Used for
// terminal failure paths (offer_race_lost, health_timeout,
// instance_terminal_state). Per D-E4.
func (r *Reconciler) captureTerminalSentry(lifecycleID int64, reason string, extras map[string]any) {
	hub := sentry.CurrentHub().Clone()
	hub.Scope().SetTag("subsystem", "emerg")
	hub.Scope().SetTag("lifecycle_id", strconv.FormatInt(lifecycleID, 10))
	hub.Scope().SetTag("shutdown_reason", reason)
	for k, v := range extras {
		hub.Scope().SetExtra(k, v)
	}
	hub.CaptureMessage(fmt.Sprintf("emergency lifecycle aborted: %s", reason))
}

// vastOverride and healthCheckOverride exist as struct fields on Reconciler
// (set via Plan 06-06 SetVastClient / SetHealthCheck test helpers below).
// They are nil in production; non-nil only inside unit tests.

// SetVastClient injects a custom VastAPI implementation. ONLY intended
// for tests — production wires the real *vast.Client via Deps.Vast in
// NewReconciler. Returns the receiver for fluent test setup.
func (r *Reconciler) SetVastClient(api VastAPI) *Reconciler {
	r.vastOverride = api
	return r
}

// SetHealthCheck injects a custom health-check function. ONLY intended
// for tests so the integration test can return true/false without spinning
// up a full HTTP server. Returns the receiver for fluent test setup.
func (r *Reconciler) SetHealthCheck(fn HealthChecker) *Reconciler {
	r.healthCheckOverride = fn
	return r
}

// ActivePodURL returns the current emergency pod /health URL when one
// is live + healthy, plus a bool indicating whether the pointer was set.
// Plan 08 dispatcher reads this every request; lockless atomic.Load makes
// it safe on the hot path.
func (r *Reconciler) ActivePodURL() (string, bool) {
	p := r.activePodURL.Load()
	if p == nil {
		return "", false
	}
	return *p, true
}

// IsActive returns true when the FSM is in EmergencyActive AND ActivePodURL
// is set. The dispatcher (Plan 08) uses this as the pre-condition for
// overriding tier-0 routing. Lockless.
func (r *Reconciler) IsActive() bool {
	if r.deps.FSM.State() != StateEmergencyActive {
		return false
	}
	_, ok := r.ActivePodURL()
	return ok
}

// cancelActiveLifecycle implements the D-C3 triple-layer cancel:
//
//  1. Layer 1 — context cancel: invokes the stored CancelFunc so the
//     in-flight provisionLifecycle goroutine sees ctx.Err() != nil at its
//     next checkpoint (post-search, post-create, during /health poll).
//     waitForReadyOrDestroy already handles the ctx.Done() branch by
//     issuing a best-effort Destroy + closing the row with
//     shutdown_reason='cancelled_in_flight' (Pitfall 8: separate
//     background ctx with 30s budget for the destroy call).
//
//  2. Layer 2 — Pub/Sub broadcast: publishes a `cancel_in_flight` event
//     on gw:emerg:events so non-leader replicas (and gatewayctl observers)
//     can update in-memory state for visibility. Non-leader applyEmergCommand
//     drops it on the floor (visibility-only).
//
//  3. Layer 3 — post-create destroy: enforced inside waitForReadyOrDestroy's
//     ctx.Done() branch (Plan 06 — already implemented). When cancel fires
//     AFTER vast_instance_id is known, the provisioning goroutine runs
//     bestEffortDestroy(instanceID) before close.
//
// MUST only be called by the leader (caller responsibility — typically
// from inside evaluateEmergencyProvisioning which already gates on the
// tracker state). Idempotent: a second call after the goroutine has
// already cleared activeLifecycle is a no-op.
//
// This method does NOT clear activeLifecycle directly — closeLifecycle
// (called from inside the goroutine on its way out) owns that write.
// Clearing here would race the goroutine and could leave the FSM in a
// state where startProvisioning thinks the slot is free but the goroutine
// has not yet finished its destroy.
func (r *Reconciler) cancelActiveLifecycle(ctx context.Context, reason string) {
	lc := r.activeLifecycle.Load()
	if lc == nil {
		return
	}
	r.deps.Log.Info("cancelling active lifecycle (D-C3)",
		"id", lc.ID, "reason", reason, "vast_instance_id", lc.VastInstanceID)

	// Layer 1: context cancel. The lifecycleCancel pointer was stored by
	// startProvisioning; Swap so a second cancelActiveLifecycle call is a
	// no-op (idempotent).
	if cancelPtr := r.lifecycleCancel.Swap(nil); cancelPtr != nil {
		(*cancelPtr)()
	}

	// Layer 2: Pub/Sub broadcast for cross-replica visibility.
	if r.deps.Redis != nil {
		ev := redisx.EmergEvent{
			Type:        "cancel_in_flight",
			State:       r.deps.FSM.State().String(),
			LifecycleID: lc.ID,
			Reason:      reason,
			SinceUnix:   time.Now().Unix(),
			ReplicaID:   r.replicaID,
		}
		if err := redisx.PublishEmergEvent(ctx, r.deps.Redis, ev); err != nil {
			r.deps.Log.Warn("cancelActiveLifecycle: PublishEmergEvent failed",
				"err", err, "lifecycle_id", lc.ID)
		}
	}

	// Layer 3 (post-create destroy) is enforced inside waitForReadyOrDestroy's
	// ctx.Done() branch — see lifecycle.go waitForReadyOrDestroy for the
	// bestEffortDestroy(instanceID) + closeLifecycle('cancelled_in_flight')
	// path. No additional work needed here.

	r.captureBreadcrumb("cancel_in_flight", map[string]any{
		"lifecycle_id":     lc.ID,
		"reason":           reason,
		"vast_instance_id": lc.VastInstanceID,
	})
}
