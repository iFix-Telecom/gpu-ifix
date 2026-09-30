package primary

// Leader-only label sweep (ClickUp 86akr57nj, 2026-09-30).
//
// Incident: primary lifecycle 494 / Vast instance 53345260 was found
// running ~29h after its lifecycle row had been closed — the destroy path
// gave up and nothing ever looked at the Vast account again. This sweep
// is the convergence mechanism: the leader periodically lists every Vast
// instance and destroys those labelled EXACTLY ifix-primary-lifecycle-<id>
// whose lifecycle is not the open one in the DB.
//
// Race guard (ordering matters): ListInstances runs FIRST, the DB read
// SECOND. Lifecycle rows are INSERTed before CreateInstance, so any
// instance present in the listing already has its row committed and a DB
// read done afterwards sees it as open. The in-memory active ids and the
// minAge guard are extra belts, not the primary guard.
//
// Fail-closed: no Vast client, no DB, or a DB error → destroy nothing.

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/vastutil"
)

const (
	// orphanSweepInterval is the cadence of the leader label sweep.
	orphanSweepInterval = 10 * time.Minute
	// orphanSweepMinAge protects instances younger than this from the
	// sweep regardless of DB state (belt on top of the list-then-read
	// ordering guard).
	orphanSweepMinAge = 10 * time.Minute
	// orphanSweepListTimeout bounds the Vast listing call.
	orphanSweepListTimeout = 30 * time.Second
)

// primarySweepQuerier is the DB surface the sweep needs. *gen.Queries
// satisfies it; tests inject a stub via sweepQuerierOverride.
type primarySweepQuerier interface {
	GetOpenPrimaryLifecycle(ctx context.Context) (gen.AiGatewayPrimaryLifecycle, error)
}

// sweepQuerier returns the test override when set, else the production
// query handle, else nil (no DB wired → the sweep has no authority).
func (r *Reconciler) sweepQuerier() primarySweepQuerier {
	if r.sweepQuerierOverride != nil {
		return r.sweepQuerierOverride
	}
	if q := r.queries(); q != nil {
		return q
	}
	return nil
}

// triggerOrphanSweep starts sweepOrphanInstances in a goroutine unless one
// is already running. Async so a slow Vast listing / destroy never blocks
// the 1Hz tick or the 10s leader-lock renew (primaryLockExpiry = 30s).
func (r *Reconciler) triggerOrphanSweep(ctx context.Context, log *slog.Logger) {
	if !r.sweepRunning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer r.sweepRunning.Store(false)
		r.sweepOrphanInstances(ctx, log)
	}()
}

// sweepOrphanInstances destroys Vast instances labelled exactly
// ifix-primary-lifecycle-<id> whose lifecycle is not open in the DB and
// that are not the in-memory active instance. See the file comment for
// the ordering and fail-closed guarantees.
func (r *Reconciler) sweepOrphanInstances(ctx context.Context, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	if r.deps.Vast == nil {
		return
	}

	// 1. List FIRST (race guard — see file comment).
	listCtx, cancel := context.WithTimeout(context.Background(), orphanSweepListTimeout)
	instances, err := r.deps.Vast.ListInstances(listCtx)
	cancel()
	if err != nil {
		log.Warn("primary orphan sweep: ListInstances failed; skipping", "err", err)
		return
	}

	// 2. THEN read the DB. Fail-closed on anything but a clean answer.
	q := r.sweepQuerier()
	if q == nil {
		return
	}
	live := map[int64]bool{}
	keep := map[int64]bool{}
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 10*time.Second)
	open, err := q.GetOpenPrimaryLifecycle(dbCtx)
	dbCancel()
	switch {
	case err == nil:
		live[open.ID] = true
		if open.VastInstanceID.Valid {
			keep[open.VastInstanceID.Int64] = true
		}
	case errors.Is(err, pgx.ErrNoRows):
		// No open lifecycle — every exactly-labelled old instance is orphan.
	default:
		log.Warn("primary orphan sweep skipped: DB unavailable", "err", err)
		return
	}
	if id := r.activeLifecycleID.Load(); id != 0 {
		live[id] = true
	}
	if id := r.activeInstanceID.Load(); id != 0 {
		keep[id] = true
	}

	orphans := vastutil.SelectLabelOrphans(instances, vastutil.PrimaryLabelPrefix,
		live, keep, time.Now(), orphanSweepMinAge)
	for _, inst := range orphans {
		lcID, _ := vastutil.ParseLifecycleLabel(inst.Label, vastutil.PrimaryLabelPrefix)
		log.Warn("primary orphan Vast instance found by label sweep; destroying",
			"instance_id", inst.ID, "label", inst.Label, "lifecycle_id", lcID,
			"machine_id", inst.MachineID, "start_date", inst.StartDate,
			"actual_status", inst.ActualStatus)
		vastutil.CaptureBreadcrumb("primary.sweep.orphan", map[string]any{
			"instance_id":  inst.ID,
			"label":        inst.Label,
			"lifecycle_id": lcID,
			"machine_id":   inst.MachineID,
		})
		if derr := vastutil.BestEffortDestroy(ctx, r.deps.Vast, log, inst.ID); derr != nil {
			log.Error("primary orphan sweep: destroy failed; next sweep retries",
				"instance_id", inst.ID, "lifecycle_id", lcID, "err", derr)
			continue
		}
		obs.GatewayVastOrphanSweptTotal.WithLabelValues("primary").Inc()
	}
}
