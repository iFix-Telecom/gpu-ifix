package primary

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// stubSweepQuerier scripts GetOpenPrimaryLifecycle for the sweep tests.
type stubSweepQuerier struct {
	row   gen.AiGatewayPrimaryLifecycle
	err   error
	calls int
}

func (s *stubSweepQuerier) GetOpenPrimaryLifecycle(_ context.Context) (gen.AiGatewayPrimaryLifecycle, error) {
	s.calls++
	return s.row, s.err
}

func oldStart() float64   { return float64(time.Now().Add(-29 * time.Hour).Unix()) }
func youngStart() float64 { return float64(time.Now().Add(-1 * time.Minute).Unix()) }

func newSweepReconciler(fv *fakeVast, q primarySweepQuerier) *Reconciler {
	r := &Reconciler{deps: Deps{Vast: fv, Log: slog.New(slog.DiscardHandler)}}
	r.sweepQuerierOverride = q
	return r
}

// The 2026-09-30 incident shape: lifecycle 494 / instance 53345260 alive
// while lifecycle 500 is the open one. Only 53345260 must be destroyed;
// the 3060 pod and emerg instances are never touched.
func TestPrimarySweep_DestroysOnlyClosedLifecycleInstance(t *testing.T) {
	fv := &fakeVast{listInstances: []vast.Instance{
		{ID: 53345260, Label: "ifix-primary-lifecycle-494", StartDate: oldStart()},
		{ID: 600, Label: "ifix-primary-lifecycle-500", StartDate: oldStart()},
		{ID: 700, Label: "stt-tts-rerank-unified", StartDate: oldStart()},
		{ID: 701, Label: "rerank-3060-v2m3", StartDate: oldStart()},
		{ID: 702, Label: "ifix-primary-lifecycle-", StartDate: oldStart()},
		{ID: 703, Label: "ifix-primary-lifecycle-12x", StartDate: oldStart()},
		{ID: 800, Label: "ifix-emerg-lifecycle-9", StartDate: oldStart()},
	}}
	q := &stubSweepQuerier{row: gen.AiGatewayPrimaryLifecycle{
		ID: 500, VastInstanceID: pgtype.Int8{Int64: 600, Valid: true},
	}}
	r := newSweepReconciler(fv, q)

	before := testutil.ToFloat64(obs.GatewayVastOrphanSweptTotal.WithLabelValues("primary"))
	r.sweepOrphanInstances(context.Background(), nil)

	require.Equal(t, []int64{53345260}, fv.destroyed())
	require.Equal(t, 1, q.calls)
	after := testutil.ToFloat64(obs.GatewayVastOrphanSweptTotal.WithLabelValues("primary"))
	require.Equal(t, before+1, after)
}

func TestPrimarySweep_DBErrorFailsClosed(t *testing.T) {
	fv := &fakeVast{listInstances: []vast.Instance{
		{ID: 53345260, Label: "ifix-primary-lifecycle-494", StartDate: oldStart()},
	}}
	r := newSweepReconciler(fv, &stubSweepQuerier{err: errors.New("connection refused")})
	r.sweepOrphanInstances(context.Background(), nil)
	require.Empty(t, fv.destroyed())
}

func TestPrimarySweep_NoDBFailsClosed(t *testing.T) {
	fv := &fakeVast{listInstances: []vast.Instance{
		{ID: 53345260, Label: "ifix-primary-lifecycle-494", StartDate: oldStart()},
	}}
	r := newSweepReconciler(fv, nil) // no override, no Deps.DB → no authority
	r.sweepOrphanInstances(context.Background(), nil)
	require.Empty(t, fv.destroyed())
}

func TestPrimarySweep_NoOpenLifecycle_YoungKept(t *testing.T) {
	fv := &fakeVast{listInstances: []vast.Instance{
		{ID: 900, Label: "ifix-primary-lifecycle-900", StartDate: youngStart()},
	}}
	r := newSweepReconciler(fv, &stubSweepQuerier{err: pgx.ErrNoRows})
	r.sweepOrphanInstances(context.Background(), nil)
	require.Empty(t, fv.destroyed(), "instance younger than minAge must be kept")
}

func TestPrimarySweep_NoOpenLifecycle_OldDestroyed(t *testing.T) {
	fv := &fakeVast{listInstances: []vast.Instance{
		{ID: 901, Label: "ifix-primary-lifecycle-901", StartDate: oldStart()},
	}}
	r := newSweepReconciler(fv, &stubSweepQuerier{err: pgx.ErrNoRows})
	r.sweepOrphanInstances(context.Background(), nil)
	require.Equal(t, []int64{901}, fv.destroyed())
}

func TestPrimarySweep_InMemoryActiveKept(t *testing.T) {
	fv := &fakeVast{listInstances: []vast.Instance{
		{ID: 1001, Label: "ifix-primary-lifecycle-77", StartDate: oldStart()},
		{ID: 1002, Label: "ifix-primary-lifecycle-78", StartDate: oldStart()},
	}}
	r := newSweepReconciler(fv, &stubSweepQuerier{err: pgx.ErrNoRows})
	r.activeInstanceID.Store(1001) // kept by instance id
	r.activeLifecycleID.Store(78)  // kept by lifecycle id
	r.sweepOrphanInstances(context.Background(), nil)
	require.Empty(t, fv.destroyed())
}

func TestPrimarySweep_ListErrorNoDestroy(t *testing.T) {
	fv := &fakeVast{listErr: errors.New("vast 503")}
	q := &stubSweepQuerier{err: pgx.ErrNoRows}
	r := newSweepReconciler(fv, q)
	r.sweepOrphanInstances(context.Background(), nil)
	require.Empty(t, fv.destroyed())
	require.Equal(t, 0, q.calls, "DB must not be read when the listing failed")
}

func TestPrimarySweep_TriggerInFlightGuard(t *testing.T) {
	fv := &fakeVast{}
	r := newSweepReconciler(fv, &stubSweepQuerier{err: pgx.ErrNoRows})
	r.sweepRunning.Store(true) // simulate a sweep already running
	r.triggerOrphanSweep(context.Background(), slog.New(slog.DiscardHandler))
	require.True(t, r.sweepRunning.Load(), "guard must not be cleared by a skipped trigger")

	r.sweepRunning.Store(false)
	r.triggerOrphanSweep(context.Background(), slog.New(slog.DiscardHandler))
	require.Eventually(t, func() bool { return !r.sweepRunning.Load() }, time.Second, 5*time.Millisecond)
}
