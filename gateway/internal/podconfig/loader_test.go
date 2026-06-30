// Package podconfig (loader_test.go): Plan 17-02 Task 1 unit tests for the
// hot-reload snapshot loader. Covers the five behaviors:
//  1. healthy Refresh swaps the snapshot + ok metric;
//  2. query error keeps last-good + error metric (THE critical invariant);
//  3. bad schedule (out-of-range hour) keeps last-good + error metric;
//  4. a fresh Loader serves the zero snapshot until the first Refresh;
//  5. concurrent reads during Refresh never tear (atomic.Pointer).
//
// Uses a fake loaderQueries — no Postgres pool, no testcontainers.
package podconfig

import (
	"context"
	"errors"
	"log/slog"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/prometheus/client_golang/prometheus/testutil"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// fakeQueries is a scriptable loaderQueries stub. Set row to control the
// returned snapshot; set err to force the error path.
type fakeQueries struct {
	mu    sync.Mutex
	row   gen.AiGatewayPodConfig
	err   error
	calls int
}

func (f *fakeQueries) GetPodConfig(ctx context.Context) (gen.AiGatewayPodConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.row, f.err
}

func (f *fakeQueries) set(row gen.AiGatewayPodConfig, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.row = row
	f.err = err
}

// numericFromFloat builds a pgtype.Numeric scaled to 4 decimal places
// (matches the column scale). Mirrors primary/budget_test.go.
func numericFromFloat(v float64) pgtype.Numeric {
	if v == 0 {
		return pgtype.Numeric{Int: big.NewInt(0), Exp: 0, Valid: true}
	}
	return pgtype.Numeric{Int: big.NewInt(int64(v * 10000)), Exp: -4, Valid: true}
}

// validRow returns a healthy single-row pod_config with the given primary
// cap and up-hour so tests can assert the snapshot reflects the row.
func validRow(capPrimary float64, upHour int32) gen.AiGatewayPodConfig {
	return gen.AiGatewayPodConfig{
		ID:                   true,
		VastMachineBlocklist: []int64{55942, 45778},
		VastMachineAllowlist: []int64{43803, 55158},
		CapPrimary:           numericFromFloat(capPrimary),
		CapFallback:          numericFromFloat(1.50),
		HostID:               0,
		RejectPrivateIp:      true,
		ColdstartBudgetS:     3600,
		PortBindBudgetS:      300,
		FailureCooldownS:     300,
		MonthlyBudgetBrl:     numericFromFloat(800),
		ScheduleUpHour:       upHour,
		ScheduleDownHour:     17,
		ScheduleDays:         []string{"mon", "tue", "wed", "thu", "fri"},
		GraceRampDownS:       300,
		ProvisionLeadS:       2400,
		ScheduleDisabled:     false,
		CapPrimaryMin:        numericFromFloat(0.10),
		CapPrimaryMax:        numericFromFloat(2.00),
		CapFallbackMin:       numericFromFloat(0.10),
		CapFallbackMax:       numericFromFloat(3.00),
		ColdstartBudgetSMin:  600,
		ColdstartBudgetSMax:  7200,
		PortBindBudgetSMin:   60,
		PortBindBudgetSMax:   900,
		FailureCooldownSMin:  60,
		FailureCooldownSMax:  3600,
		MonthlyBudgetBrlMin:  numericFromFloat(100),
		MonthlyBudgetBrlMax:  numericFromFloat(5000),
		ScheduleUpHourMin:    0,
		ScheduleUpHourMax:    23,
		ScheduleDownHourMin:  0,
		ScheduleDownHourMax:  23,
		GraceRampDownSMin:    0,
		GraceRampDownSMax:    1800,
		ProvisionLeadSMin:    0,
		ProvisionLeadSMax:    7200,
		UpdatedAt:            time.Now(),
	}
}

// newTestLoader builds a Loader backed by the fake (no pool, no Postgres).
func newTestLoader(q loaderQueries) *Loader {
	return &Loader{
		q:   q,
		log: slog.New(slog.NewTextHandler(testWriter{}, nil)),
		tz:  time.UTC,
	}
}

// testWriter discards log output during tests.
type testWriter struct{}

func (testWriter) Write(p []byte) (int, error) { return len(p), nil }

// TestLoaderRefresh_HealthySwapsSnapshot — behavior 1.
func TestLoaderRefresh_HealthySwapsSnapshot(t *testing.T) {
	before := testutil.ToFloat64(obs.PodConfigReloadTotal.WithLabelValues("ok"))

	q := &fakeQueries{}
	q.set(validRow(0.60, 9), nil)
	l := newTestLoader(q)

	if err := l.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v, want nil", err)
	}

	cfg := l.Cfg()
	if cfg.CapPrimary != 0.60 {
		t.Errorf("Cfg().CapPrimary = %v, want 0.60", cfg.CapPrimary)
	}
	if cfg.ScheduleUpHour != 9 {
		t.Errorf("Cfg().ScheduleUpHour = %d, want 9", cfg.ScheduleUpHour)
	}
	if len(cfg.VastMachineBlocklist) != 2 || cfg.VastMachineBlocklist[0] != 55942 {
		t.Errorf("Cfg().VastMachineBlocklist = %v, want [55942 45778]", cfg.VastMachineBlocklist)
	}
	if b := l.Bounds(); b.CapPrimaryMax != 2.00 {
		t.Errorf("Bounds().CapPrimaryMax = %v, want 2.00", b.CapPrimaryMax)
	}
	if r := l.Rule(); r.UpHour != 9 || r.Timezone != time.UTC {
		t.Errorf("Rule() UpHour/Timezone = %d/%v, want 9/UTC", r.UpHour, r.Timezone)
	}

	after := testutil.ToFloat64(obs.PodConfigReloadTotal.WithLabelValues("ok"))
	if after != before+1 {
		t.Errorf("ok metric = %v, want %v", after, before+1)
	}
}

// TestLoaderRefresh_QueryErrorKeepsLastGood — behavior 2 (THE invariant).
func TestLoaderRefresh_QueryErrorKeepsLastGood(t *testing.T) {
	q := &fakeQueries{}
	q.set(validRow(0.60, 9), nil)
	l := newTestLoader(q)
	if err := l.Refresh(context.Background()); err != nil {
		t.Fatalf("initial Refresh() error = %v", err)
	}
	good := l.Load()
	if good == nil {
		t.Fatalf("snapshot nil after healthy Refresh")
	}

	errBefore := testutil.ToFloat64(obs.PodConfigReloadTotal.WithLabelValues("error"))

	// Now force a query error.
	q.set(validRow(0.99, 11), errors.New("db down"))
	err := l.Refresh(context.Background())
	if err == nil {
		t.Fatalf("Refresh() error = nil, want non-nil on query failure")
	}

	// Snapshot must be UNCHANGED — same pointer, same values.
	if l.Load() != good {
		t.Errorf("snapshot pointer changed on error — last-good NOT preserved")
	}
	if l.Cfg().CapPrimary != 0.60 {
		t.Errorf("Cfg().CapPrimary = %v after error, want 0.60 (last-good)", l.Cfg().CapPrimary)
	}

	errAfter := testutil.ToFloat64(obs.PodConfigReloadTotal.WithLabelValues("error"))
	if errAfter != errBefore+1 {
		t.Errorf("error metric = %v, want %v", errAfter, errBefore+1)
	}
}

// TestLoaderRefresh_BadScheduleKeepsLastGood — behavior 3.
func TestLoaderRefresh_BadScheduleKeepsLastGood(t *testing.T) {
	q := &fakeQueries{}
	q.set(validRow(0.60, 9), nil)
	l := newTestLoader(q)
	if err := l.Refresh(context.Background()); err != nil {
		t.Fatalf("initial Refresh() error = %v", err)
	}
	good := l.Load()

	errBefore := testutil.ToFloat64(obs.PodConfigReloadTotal.WithLabelValues("error"))

	// up_hour=99 is out of [0,23] → ParseScheduleFromSnapshot fails.
	bad := validRow(0.60, 99)
	q.set(bad, nil)
	err := l.Refresh(context.Background())
	if err == nil {
		t.Fatalf("Refresh() error = nil, want non-nil on bad schedule")
	}
	if l.Load() != good {
		t.Errorf("snapshot swapped a broken rule — last-good NOT preserved (T-17-06)")
	}
	if l.Cfg().ScheduleUpHour != 9 {
		t.Errorf("Cfg().ScheduleUpHour = %d after bad refresh, want 9 (last-good)", l.Cfg().ScheduleUpHour)
	}

	errAfter := testutil.ToFloat64(obs.PodConfigReloadTotal.WithLabelValues("error"))
	if errAfter != errBefore+1 {
		t.Errorf("error metric = %v, want %v", errAfter, errBefore+1)
	}
}

// TestLoader_ZeroSnapshotBeforeFirstRefresh — behavior 4. A fresh Loader
// (no Refresh yet) returns a nil snapshot and zero-valued accessors; the
// production NewLoader does an initial Refresh and fatals on failure so
// the gateway never serves this zero state.
func TestLoader_ZeroSnapshotBeforeFirstRefresh(t *testing.T) {
	l := newTestLoader(&fakeQueries{})
	if l.Load() != nil {
		t.Errorf("Load() = non-nil before first Refresh, want nil")
	}
	if l.Cfg().CapPrimary != 0 {
		t.Errorf("Cfg().CapPrimary = %v before Refresh, want 0", l.Cfg().CapPrimary)
	}
	if l.Rule().UpHour != 0 {
		t.Errorf("Rule().UpHour = %d before Refresh, want 0", l.Rule().UpHour)
	}
}

// TestLoader_ConcurrentReadsNeverTear — behavior 5. 50 readers call Cfg()
// while a writer alternates Refresh between two valid rows. Each read must
// observe a self-consistent snapshot (CapPrimary is always one of the two
// scripted values, never garbage). Run with -race to catch any data race
// on the atomic.Pointer.
func TestLoader_ConcurrentReadsNeverTear(t *testing.T) {
	q := &fakeQueries{}
	q.set(validRow(0.60, 9), nil)
	l := newTestLoader(q)
	if err := l.Refresh(context.Background()); err != nil {
		t.Fatalf("seed Refresh() error = %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var failures atomic.Int64

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					switch l.Cfg().CapPrimary {
					case 0.60, 0.80:
						// consistent
					default:
						failures.Add(1)
						return
					}
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			if i%2 == 0 {
				q.set(validRow(0.80, 9), nil)
			} else {
				q.set(validRow(0.60, 9), nil)
			}
			_ = l.Refresh(context.Background())
		}
		close(stop)
	}()

	wg.Wait()
	if failures.Load() > 0 {
		t.Fatalf("concurrent reads observed %d torn/garbage snapshots", failures.Load())
	}
}
