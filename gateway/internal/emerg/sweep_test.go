package emerg

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// sweepFakeVast implements VastAPI for the label sweep tests.
type sweepFakeVast struct {
	mu        sync.Mutex
	list      []vast.Instance
	listErr   error
	destroyed []int64
}

func (f *sweepFakeVast) SearchOffers(context.Context, vast.SearchFilter) ([]vast.Offer, error) {
	return nil, errors.New("not used")
}

func (f *sweepFakeVast) CreateInstance(context.Context, int64, vast.CreateRequest) (vast.Instance, error) {
	return vast.Instance{}, errors.New("not used")
}

func (f *sweepFakeVast) GetInstance(context.Context, int64) (vast.Instance, error) {
	return vast.Instance{}, errors.New("not used")
}

func (f *sweepFakeVast) DestroyInstance(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destroyed = append(f.destroyed, id)
	return nil
}

func (f *sweepFakeVast) ListInstances(context.Context) ([]vast.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]vast.Instance(nil), f.list...), nil
}

func (f *sweepFakeVast) destroyedIDs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.destroyed...)
}

type stubEmergSweepQuerier struct {
	rows  []gen.ListLiveEmergencyLifecyclesRow
	err   error
	calls int
}

func (s *stubEmergSweepQuerier) ListLiveEmergencyLifecycles(context.Context) ([]gen.ListLiveEmergencyLifecyclesRow, error) {
	s.calls++
	return s.rows, s.err
}

func newEmergSweepReconciler(fv *sweepFakeVast, q emergSweepQuerier) *Reconciler {
	r := &Reconciler{deps: Deps{Log: slog.New(slog.DiscardHandler)}}
	r.SetVastClient(fv)
	r.sweepQuerierOverride = q
	return r
}

func sweepOld() float64 { return float64(time.Now().Add(-3 * time.Hour).Unix()) }

func TestEmergSweep_LiveProtectedClosedDestroyed(t *testing.T) {
	fv := &sweepFakeVast{list: []vast.Instance{
		{ID: 10, Label: "ifix-emerg-lifecycle-1", StartDate: sweepOld()},    // live
		{ID: 11, Label: "ifix-emerg-lifecycle-2", StartDate: sweepOld()},    // closed → destroy
		{ID: 12, Label: "ifix-primary-lifecycle-2", StartDate: sweepOld()},  // other prefix
		{ID: 13, Label: "stt-tts-rerank-unified", StartDate: sweepOld()},    // foreign
		{ID: 14, Label: "ifix-emerg-lifecycle-", StartDate: sweepOld()},     // malformed
		{ID: 15, Label: "ifix-emerg-lifecycle-3abc", StartDate: sweepOld()}, // malformed
	}}
	q := &stubEmergSweepQuerier{rows: []gen.ListLiveEmergencyLifecyclesRow{
		{ID: 1, VastInstanceID: pgtype.Int8{Int64: 10, Valid: true}},
	}}
	r := newEmergSweepReconciler(fv, q)

	before := testutil.ToFloat64(obs.GatewayVastOrphanSweptTotal.WithLabelValues("emerg"))
	r.sweepOrphanInstances(context.Background(), nil)
	require.Equal(t, []int64{11}, fv.destroyedIDs())
	require.Equal(t, before+1, testutil.ToFloat64(obs.GatewayVastOrphanSweptTotal.WithLabelValues("emerg")))
}

func TestEmergSweep_ActiveLifecycleKept(t *testing.T) {
	fv := &sweepFakeVast{list: []vast.Instance{
		{ID: 20, Label: "ifix-emerg-lifecycle-5", StartDate: sweepOld()},
	}}
	r := newEmergSweepReconciler(fv, &stubEmergSweepQuerier{})
	r.activeLifecycle.Store(&ActiveLifecycle{ID: 5, VastInstanceID: 20})
	r.sweepOrphanInstances(context.Background(), nil)
	require.Empty(t, fv.destroyedIDs())
}

func TestEmergSweep_DBErrorFailsClosed(t *testing.T) {
	fv := &sweepFakeVast{list: []vast.Instance{
		{ID: 30, Label: "ifix-emerg-lifecycle-6", StartDate: sweepOld()},
	}}
	r := newEmergSweepReconciler(fv, &stubEmergSweepQuerier{err: errors.New("db down")})
	r.sweepOrphanInstances(context.Background(), nil)
	require.Empty(t, fv.destroyedIDs())
}

func TestEmergSweep_NoDBFailsClosed(t *testing.T) {
	fv := &sweepFakeVast{list: []vast.Instance{
		{ID: 31, Label: "ifix-emerg-lifecycle-7", StartDate: sweepOld()},
	}}
	r := newEmergSweepReconciler(fv, nil) // no override and r.q == nil
	r.sweepOrphanInstances(context.Background(), nil)
	require.Empty(t, fv.destroyedIDs())
}

func TestEmergSweep_ListErrorNoDestroy(t *testing.T) {
	fv := &sweepFakeVast{listErr: errors.New("vast 503")}
	q := &stubEmergSweepQuerier{}
	r := newEmergSweepReconciler(fv, q)
	r.sweepOrphanInstances(context.Background(), nil)
	require.Empty(t, fv.destroyedIDs())
	require.Equal(t, 0, q.calls)
}
