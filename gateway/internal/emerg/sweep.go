package emerg

// Leader-only label sweep (ClickUp 86akr57nj, 2026-09-30).
//
// recoverOrphanLifecycles only sees rows with ended_at IS NULL: once a
// lifecycle row is closed after a failed destroy, nothing ever revisits
// the instance. This sweep lists every Vast instance and destroys those
// labelled EXACTLY ifix-emerg-lifecycle-<id> whose lifecycle is not live
// in the DB.
//
// Race guard (ordering matters): ListInstances runs FIRST, the DB read
// SECOND. Lifecycle rows are INSERTed before CreateInstance, so any
// instance present in the listing already has its row committed and a DB
// read done afterwards sees it as live. The in-memory active lifecycle
// and the minAge guard are extra belts.
//
// Fail-closed: no Vast client, no DB, or a DB error → destroy nothing.

import (
	"context"
	"log/slog"
	"strings"
	"time"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/vastutil"
)

const (
	// orphanSweepInterval is the cadence of the leader label sweep.
	orphanSweepInterval = 10 * time.Minute
	// orphanSweepMinAge protects instances younger than this regardless
	// of DB state.
	orphanSweepMinAge = 10 * time.Minute
	// orphanSweepListTimeout bounds the Vast listing call.
	orphanSweepListTimeout = 30 * time.Second
)

// emergSweepQuerier is the DB surface the sweep needs. *gen.Queries
// satisfies it; tests inject a stub via sweepQuerierOverride.
type emergSweepQuerier interface {
	ListLiveEmergencyLifecycles(ctx context.Context) ([]gen.ListLiveEmergencyLifecyclesRow, error)
}

func (r *Reconciler) sweepQuerier() emergSweepQuerier {
	if r.sweepQuerierOverride != nil {
		return r.sweepQuerierOverride
	}
	if r.q != nil {
		return r.q
	}
	return nil
}

// triggerOrphanSweep starts sweepOrphanInstances in a goroutine unless one
// is already running, so the 1Hz tick / lock renew is never blocked.
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
// ifix-emerg-lifecycle-<id> whose lifecycle is not live in the DB and
// that are not the in-memory active lifecycle's instance.
func (r *Reconciler) sweepOrphanInstances(ctx context.Context, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	api := r.vastAPI()
	if api == nil {
		return
	}

	// 1. List FIRST (race guard — see file comment).
	listCtx, cancel := context.WithTimeout(context.Background(), orphanSweepListTimeout)
	instances, err := api.ListInstances(listCtx)
	cancel()
	if err != nil {
		log.Warn("emerg orphan sweep: ListInstances failed; skipping", "err", err)
		return
	}

	// 2. THEN read the DB. Fail-closed on error / no DB.
	q := r.sweepQuerier()
	if q == nil {
		return
	}
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 10*time.Second)
	rows, err := q.ListLiveEmergencyLifecycles(dbCtx)
	dbCancel()
	if err != nil {
		log.Warn("emerg orphan sweep skipped: DB unavailable", "err", err)
		return
	}
	live := make(map[int64]bool, len(rows)+1)
	keep := make(map[int64]bool, len(rows)+1)
	for _, row := range rows {
		live[row.ID] = true
		if row.VastInstanceID.Valid {
			keep[row.VastInstanceID.Int64] = true
		}
	}
	if al := r.activeLifecycle.Load(); al != nil {
		if al.ID != 0 {
			live[al.ID] = true
		}
		if al.VastInstanceID != 0 {
			keep[al.VastInstanceID] = true
		}
	}

	orphans := vastutil.SelectLabelOrphans(instances, vastutil.EmergLabelPrefix,
		live, keep, time.Now(), orphanSweepMinAge)
	// Quick 260930-uru: 1 log INFO por execução com os contadores, para o
	// sweep ser observável mesmo quando não acha nada.
	matched := 0
	for _, inst := range instances {
		if strings.HasPrefix(inst.Label, vastutil.EmergLabelPrefix) {
			matched++
		}
	}
	destroyed, failed := 0, 0
	defer func() {
		log.Info("emerg orphan sweep done",
			"listed", len(instances),
			"matched_label", matched,
			"orphans", len(orphans),
			"destroyed", destroyed,
			"failed", failed)
	}()
	for _, inst := range orphans {
		lcID, _ := vastutil.ParseLifecycleLabel(inst.Label, vastutil.EmergLabelPrefix)
		log.Warn("emerg orphan Vast instance found by label sweep; destroying",
			"instance_id", inst.ID, "label", inst.Label, "lifecycle_id", lcID,
			"machine_id", inst.MachineID, "start_date", inst.StartDate,
			"actual_status", inst.ActualStatus)
		vastutil.CaptureBreadcrumb("emerg.sweep.orphan", map[string]any{
			"instance_id":  inst.ID,
			"label":        inst.Label,
			"lifecycle_id": lcID,
			"machine_id":   inst.MachineID,
		})
		if derr := vastutil.BestEffortDestroy(ctx, api, log, inst.ID); derr != nil {
			log.Error("emerg orphan sweep: destroy failed; next sweep retries",
				"instance_id", inst.ID, "lifecycle_id", lcID, "err", derr)
			failed++
			continue
		}
		destroyed++
		obs.GatewayVastOrphanSweptTotal.WithLabelValues("emerg").Inc()
	}
}
