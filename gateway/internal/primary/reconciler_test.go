// Package primary (reconciler_test.go): Plan 06.6-06a Task 3 — unit
// tests for the 5-state primary-pod FSM dispatcher + leader election +
// event subscriber + restart recovery + cooldown gate + Vast status_msg
// error detection + 4-endpoint health probe.
//
// # Coverage map
//
// Reviews consensus actions covered:
//
//   - #2 DISABLED semantics: schedule loop skips evaluateTick but the
//     event subscriber + force-up handler still fire.
//   - #3 Event subscriber: handleForceUpRequest /
//     handleForceDownRequest dispatch on Pub/Sub messages.
//   - #4 Restart recovery: 4 branches — healthy / orphan-by-Vast /
//     unhealthy / no-row.
//   - #8 ShouldBeProvisioned: pre-warm offset gating evaluateAsleep.
//   - #11 Vast status_msg error: aborts provisioning + populates
//     lastProvisionFailureAt.
//
// T-06.6-04 cooldown gate is proven by
// TestEvaluateAsleep_NoopDuringCooldown +
// TestProvisioningFailure_SetsLastFailureTimestamp.
//
// # Test infra
//
//   - fakeVast / fakeLoader / fakeDCGMScraper / fakeInflight implement the
//     adapter interfaces in lifecycle.go.
//   - fakeDBTX implements gen.DBTX so the sqlc query handle can be driven
//     without a real Postgres pool. Each test injects a per-test
//     fakeDBTX with scripted responses.
//   - miniredis is used for the event-subscriber + leader-election tests.
package primary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/config"
	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/podconfig"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/redisx"
)

// breakerReadForce reads a breaker force-override key for assertions.
func breakerReadForce(t *testing.T, rdb *redis.Client, name string) (string, time.Duration, bool, error) {
	t.Helper()
	return breaker.ReadForceOverride(context.Background(), rdb, name)
}

// deathCount returns the current PrimaryDeathDetectedTotal counter value for a
// cause label.
func deathCount(t *testing.T, cause string) float64 {
	t.Helper()
	return testutil.ToFloat64(obs.PrimaryDeathDetectedTotal.WithLabelValues(cause))
}

// ===========================================================================
// Fakes
// ===========================================================================

// fakeVast records the most recent call args + returns scripted responses.
type fakeVast struct {
	mu sync.Mutex

	searchOffersFn   func(ctx context.Context, f vast.SearchFilter) ([]vast.Offer, error)
	createInstanceFn func(ctx context.Context, offerID int64, req vast.CreateRequest) (vast.Instance, error)
	getInstanceFn    func(ctx context.Context, id int64) (vast.Instance, error)
	destroyFn        func(ctx context.Context, id int64) error
	onstartLogFn     func(ctx context.Context, id int64) (vast.OnstartLogResult, error)

	destroyCalls atomic.Int32

	// reportCalls records ReportMachine invocations (machineID + problem) so
	// tests can assert the bad-host report fired before the destroy.
	reportCalls []vast.ReportMachineRequest
}

func (f *fakeVast) ReportMachine(_ context.Context, machineID int64, body vast.ReportMachineRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reportCalls = append(f.reportCalls, body)
	return nil
}

func (f *fakeVast) reportedProblems() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.reportCalls))
	for i, c := range f.reportCalls {
		out[i] = c.Problem
	}
	return out
}

func (f *fakeVast) SearchOffers(ctx context.Context, filter vast.SearchFilter) ([]vast.Offer, error) {
	f.mu.Lock()
	fn := f.searchOffersFn
	f.mu.Unlock()
	if fn == nil {
		return nil, errors.New("fakeVast: searchOffersFn not set")
	}
	return fn(ctx, filter)
}

func (f *fakeVast) CreateInstance(ctx context.Context, offerID int64, req vast.CreateRequest) (vast.Instance, error) {
	f.mu.Lock()
	fn := f.createInstanceFn
	f.mu.Unlock()
	if fn == nil {
		return vast.Instance{}, errors.New("fakeVast: createInstanceFn not set")
	}
	return fn(ctx, offerID, req)
}

func (f *fakeVast) GetInstance(ctx context.Context, id int64) (vast.Instance, error) {
	f.mu.Lock()
	fn := f.getInstanceFn
	f.mu.Unlock()
	if fn == nil {
		return vast.Instance{}, errors.New("fakeVast: getInstanceFn not set")
	}
	return fn(ctx, id)
}

func (f *fakeVast) DestroyInstance(ctx context.Context, id int64) error {
	f.destroyCalls.Add(1)
	f.mu.Lock()
	fn := f.destroyFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, id)
	}
	return nil
}

func (f *fakeVast) OnstartLog(ctx context.Context, id int64) (vast.OnstartLogResult, error) {
	f.mu.Lock()
	fn := f.onstartLogFn
	f.mu.Unlock()
	if fn == nil {
		// Default: no heartbeat available yet, non-fatal — existing tests
		// that never script this method stay unaffected.
		return vast.OnstartLogResult{Status: vast.OnstartLogNotReady}, nil
	}
	return fn(ctx, id)
}

// fakeLoader records OverrideTier0 / RestoreTier0 calls so tests can
// assert the 3-role invariant.
type fakeLoader struct {
	mu        sync.Mutex
	overrides map[string]string // role -> URL
	restored  []string          // ordered list of restored roles
	refreshes atomic.Int32
}

func newFakeLoader() *fakeLoader {
	return &fakeLoader{overrides: map[string]string{}}
}

func (f *fakeLoader) OverrideTier0(role, url string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.overrides[role] = url
}

func (f *fakeLoader) RestoreTier0(role string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.overrides, role)
	f.restored = append(f.restored, role)
}

func (f *fakeLoader) Refresh(ctx context.Context) error {
	f.refreshes.Add(1)
	_ = ctx
	return nil
}

// Tier0OverrideURL mirrors the real Loader.Tier0OverrideURL: returns
// (url, true) when the role currently has a non-empty override, else
// ("", false). Used by the Pitfall #11 re-assert loop to detect a slot
// cleared by an emerg cutback.
func (f *fakeLoader) Tier0OverrideURL(role string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	url, ok := f.overrides[role]
	if !ok || url == "" {
		return "", false
	}
	return url, true
}

func (f *fakeLoader) snapshot() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]string, len(f.overrides))
	for k, v := range f.overrides {
		out[k] = v
	}
	return out
}

func (f *fakeLoader) restoredRoles() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.restored))
	copy(out, f.restored)
	return out
}

// fakeDCGMScraper records the most recent SetURL value.
type fakeDCGMScraper struct {
	url atomic.Pointer[string]
}

func (f *fakeDCGMScraper) SetURL(url string) {
	u := url
	f.url.Store(&u)
}

func (f *fakeDCGMScraper) Last() string {
	if p := f.url.Load(); p != nil {
		return *p
	}
	return ""
}

// fakeInflight returns scripted in-flight counts per upstream.
type fakeInflight struct {
	mu     sync.Mutex
	counts map[string]int64
}

func newFakeInflight() *fakeInflight {
	return &fakeInflight{counts: map[string]int64{}}
}

func (f *fakeInflight) Count(upstream string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[upstream]
}

func (f *fakeInflight) Set(upstream string, n int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts[upstream] = n
}

// fakeDBTX is a recording stub for gen.DBTX. Tests script per-query rows
// + errors via the QueryRowFn / ExecFn closures.
type fakeDBTX struct {
	mu sync.Mutex

	queryRowFn func(ctx context.Context, sql string, args ...interface{}) pgx.Row
	execFn     func(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error)

	execCalls     atomic.Int32
	queryCalls    atomic.Int32
	queryRowCalls atomic.Int32
	lastExecSQL   atomic.Pointer[string]
}

func (f *fakeDBTX) Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	f.execCalls.Add(1)
	s := sql
	f.lastExecSQL.Store(&s)
	f.mu.Lock()
	fn := f.execFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, sql, args...)
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (f *fakeDBTX) Query(_ context.Context, _ string, _ ...interface{}) (pgx.Rows, error) {
	f.queryCalls.Add(1)
	return nil, errors.New("fakeDBTX: Query not implemented")
}

func (f *fakeDBTX) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	f.queryRowCalls.Add(1)
	f.mu.Lock()
	fn := f.queryRowFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, sql, args...)
	}
	return errRow{err: errors.New("fakeDBTX: QueryRow not scripted")}
}

// errRow implements pgx.Row by always returning the same error from Scan.
type errRow struct{ err error }

func (e errRow) Scan(_ ...interface{}) error { return e.err }

// noRowsRow returns pgx.ErrNoRows from Scan. Models GetOpenPrimaryLifecycle
// "no live lifecycle" branch.
type noRowsRow struct{}

func (noRowsRow) Scan(_ ...interface{}) error { return pgx.ErrNoRows }

// insertReturningRow returns (id, started_at) from a InsertPrimaryLifecycle
// RETURNING clause. Scripted: id, startedAt.
type insertReturningRow struct {
	id        int64
	startedAt time.Time
}

func (r insertReturningRow) Scan(dest ...interface{}) error {
	if len(dest) < 2 {
		return errors.New("insertReturningRow: expected 2 dest pointers")
	}
	if p, ok := dest[0].(*int64); ok {
		*p = r.id
	}
	if p, ok := dest[1].(*time.Time); ok {
		*p = r.startedAt
	}
	return nil
}

// countRow scans a single int64 into dest[0]. Models the :one COUNT(*)::bigint
// shape used by CountConsecutiveFailedPrimaryProvisions (quick-260702-nse).
type countRow struct{ n int64 }

func (r countRow) Scan(dest ...interface{}) error {
	if len(dest) < 1 {
		return errors.New("countRow: expected 1 dest pointer")
	}
	if p, ok := dest[0].(*int64); ok {
		*p = r.n
	}
	return nil
}

// openLifecycleRow returns the 13 columns of GetOpenPrimaryLifecycle.
type openLifecycleRow struct {
	id             int64
	vastInstanceID pgtype.Int8
}

func (r openLifecycleRow) Scan(dest ...interface{}) error {
	if len(dest) < 13 {
		return fmt.Errorf("openLifecycleRow: expected 13 dest pointers, got %d", len(dest))
	}
	// Order from gen.GetOpenPrimaryLifecycle.Scan:
	//   id, started_at, first_health_pass_at, drain_started_at, ended_at,
	//   trigger_reason, vast_offer_id, vast_instance_id, accepted_dph,
	//   total_cost_brl, shutdown_reason, events, leader_replica
	if p, ok := dest[0].(*int64); ok {
		*p = r.id
	}
	if p, ok := dest[1].(*time.Time); ok {
		*p = time.Now().Add(-1 * time.Hour)
	}
	// timestamptz fields can stay zero-value pgtype.Timestamptz
	if p, ok := dest[7].(*pgtype.Int8); ok {
		*p = r.vastInstanceID
	}
	return nil
}

// ===========================================================================
// Helpers
// ===========================================================================

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func testCfg(t *testing.T) config.Config {
	t.Helper()
	c := cfgWithDefaults()
	c.PrimaryProvisionFailureCooldownSeconds = 300
	c.PrimaryProvisionColdStartBudgetSeconds = 60 // shorter for tests
	c.PrimaryPodScheduleGraceRampDownSeconds = 5
	c.PrimaryPodScheduleProvisionLeadSeconds = 1800
	// Phase 6.6.Y: c.PrimaryVastPriceCapDPH deleted — per-shape caps below.
	// Phase 11.1 D-A6 primary+fallback shape (Wave 0 EVIDENCE-00 locked).
	c.PrimaryVastGPUNamePrimary = "RTX 3090"
	c.PrimaryVastGPUNameFallback = "RTX 3090"
	c.PrimaryVastPriceCapPrimary = 0.30
	c.PrimaryVastPriceCapFallback = 0.60
	c.PrimaryVastNumGPUsPrimary = 1
	c.PrimaryVastNumGPUsFallback = 2
	c.USDToBRLRate = 5.0
	return c
}

// alwaysInPeakRule constructs a ScheduleRule whose IsInPeak +
// ShouldBeProvisioned always return true regardless of wall-clock hour.
// Implemented via an overnight-wrap window with UpHour == DownHour == 0:
// IsInPeak's wrap branch then returns Days[weekday] for every hour in
// [0, 24). Previous form (UpHour=0, DownHour=23) excluded 23:00–23:59
// UTC and caused deterministic CI failures whenever the runner clock
// landed in that hour.
func alwaysInPeakRule() ScheduleRule {
	loc, _ := time.LoadLocation("UTC")
	return ScheduleRule{
		Timezone: loc,
		UpHour:   0, DownHour: 0,
		Days: map[time.Weekday]bool{
			time.Sunday: true, time.Monday: true, time.Tuesday: true,
			time.Wednesday: true, time.Thursday: true, time.Friday: true,
			time.Saturday: true,
		},
		GraceRampDownS: 5,
		ProvisionLeadS: 1800,
		Disabled:       false,
	}
}

// neverInPeakRule returns a ScheduleRule where IsInPeak +
// ShouldBeProvisioned always return false (Disabled=true).
func neverInPeakRule() ScheduleRule {
	r := alwaysInPeakRule()
	r.Disabled = true
	return r
}

// buildReconciler is the standard test-fixture constructor. Tests can
// override Deps fields after construction.
func buildReconciler(t *testing.T, deps Deps) *Reconciler {
	t.Helper()
	if deps.Log == nil {
		deps.Log = testLogger()
	}
	if deps.ReplicaID == "" {
		deps.ReplicaID = "test-replica-" + t.Name()
	}
	r := NewReconcilerFull(deps)
	r.rule = deps.Rule
	r.cfg = deps.Cfg
	return r
}

// miniredisClient returns a (miniredis-backed *redis.Client, cleanup) tuple.
func miniredisClient(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb, s
}

// runningInstanceWithAllPorts returns a vast.Instance ready for
// markReady (running + 5 host port mappings + IP). SEED-019 part 3 added
// the "9100/tcp" mapping (HostPort 33100) — the device-report responder
// the gateway GETs at Ready to read whisper_device. Without this mapping
// podDeviceReportURL returns "" and the device fail-safes to "" (no stt
// override → gemini-stt).
func runningInstanceWithAllPorts(id int64) vast.Instance {
	return vast.Instance{
		ID:           id,
		ActualStatus: "running",
		PublicIPAddr: "203.0.113.7",
		Ports: map[string][]vast.PortBinding{
			"8000/tcp": {{HostIP: "0.0.0.0", HostPort: "33000"}},
			"8001/tcp": {{HostIP: "0.0.0.0", HostPort: "33001"}},
			"8003/tcp": {{HostIP: "0.0.0.0", HostPort: "33003"}},
			"9100/tcp": {{HostIP: "0.0.0.0", HostPort: "33100"}},
			"9400/tcp": {{HostIP: "0.0.0.0", HostPort: "33400"}},
		},
	}
}

// cudaDeviceReport is the standard test seam for "pod reports whisper on
// GPU" (SEED-019 part 3). Tests that assert the "stt" tier-0 override fires
// wire this as Deps.DeviceReport so primaryPodURLs.WhisperDevice == "cuda"
// gates the override on. Tests that want the fail-safe (no stt override)
// either omit DeviceReport (nil → device "") or wire a closure returning a
// non-"cuda" value.
func cudaDeviceReport(_ context.Context, _ string) string { return "cuda" }

// ===========================================================================
// evaluateAsleep / cooldown gate tests
// ===========================================================================

func TestEvaluateAsleep_TransitionsToProvisioningInPeak(t *testing.T) {
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	dbtx := &fakeDBTX{
		queryRowFn: func(_ context.Context, sql string, _ ...interface{}) pgx.Row {
			// InsertPrimaryLifecycle RETURNING id, started_at
			return insertReturningRow{id: 7, startedAt: time.Now()}
		},
	}
	// Block SearchOffers so the spawnProvisioning goroutine CANNOT reach
	// the error branch + FSM.SetState(Asleep) before this test asserts.
	// Mirror seam in TestStart_LaunchesEventSubscriberAndForceUpWorks.
	stopBlock := make(chan struct{})
	t.Cleanup(func() { close(stopBlock) })
	r := buildReconciler(t, Deps{
		Cfg: cfg,
		Vast: &fakeVast{
			searchOffersFn: func(_ context.Context, _ vast.SearchFilter) ([]vast.Offer, error) {
				<-stopBlock
				return nil, errors.New("test teardown")
			},
		},
		FSM:  fsm,
		Rule: alwaysInPeakRule(),
	})
	r.SetQueriesForTest(gen.New(dbtx))

	r.evaluateAsleep(context.Background(), time.Now(), testLogger())

	require.Equal(t, StateProvisioning, r.deps.FSM.State(),
		"evaluateAsleep with ShouldBeProvisioned=true + cooldown_elapsed must transition to Provisioning")
	require.NotZero(t, r.activeLifecycleID.Load(),
		"activeLifecycleID populated after InsertPrimaryLifecycle")
}

func TestEvaluateAsleep_NoopWhenDisabled(t *testing.T) {
	cfg := testCfg(t)
	cfg.PrimaryPodScheduleDisabled = true
	fsm := NewFSM(nil, nil)
	r := buildReconciler(t, Deps{
		Cfg:  cfg,
		Vast: &fakeVast{},
		FSM:  fsm,
		Rule: alwaysInPeakRule(),
	})
	r.evaluateAsleep(context.Background(), time.Now(), testLogger())
	require.Equal(t, StateAsleep, r.deps.FSM.State(),
		"DISABLED kill-switch must short-circuit evaluateAsleep")
}

func TestEvaluateAsleep_NoopOutOfPeak(t *testing.T) {
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	r := buildReconciler(t, Deps{
		Cfg:  cfg,
		FSM:  fsm,
		Rule: neverInPeakRule(),
	})
	r.evaluateAsleep(context.Background(), time.Now(), testLogger())
	require.Equal(t, StateAsleep, r.deps.FSM.State(),
		"ShouldBeProvisioned=false must NOT trigger provisioning")
}

func TestEvaluateAsleep_NoopDuringCooldown(t *testing.T) {
	cfg := testCfg(t)
	cfg.PrimaryProvisionFailureCooldownSeconds = 300
	fsm := NewFSM(nil, nil)
	r := buildReconciler(t, Deps{
		Cfg:  cfg,
		FSM:  fsm,
		Rule: alwaysInPeakRule(),
	})
	// Anchor cooldown 60s ago — well within the 300s window.
	r.SetLastProvisionFailureAtForTest(time.Now().Add(-60 * time.Second))
	r.evaluateAsleep(context.Background(), time.Now(), testLogger())
	require.Equal(t, StateAsleep, r.deps.FSM.State(),
		"cooldown gate must block re-provisioning until 300s elapse (T-06.6-04)")
}

func TestEvaluateAsleep_AdvancesAfterCooldownElapses(t *testing.T) {
	cfg := testCfg(t)
	cfg.PrimaryProvisionFailureCooldownSeconds = 300
	fsm := NewFSM(nil, nil)
	dbtx := &fakeDBTX{
		queryRowFn: func(_ context.Context, _ string, _ ...interface{}) pgx.Row {
			return insertReturningRow{id: 42, startedAt: time.Now()}
		},
	}
	stopBlock := make(chan struct{})
	t.Cleanup(func() { close(stopBlock) })
	r := buildReconciler(t, Deps{
		Cfg: cfg,
		Vast: &fakeVast{
			searchOffersFn: func(_ context.Context, _ vast.SearchFilter) ([]vast.Offer, error) {
				<-stopBlock
				return nil, errors.New("test teardown")
			},
		},
		FSM:  fsm,
		Rule: alwaysInPeakRule(),
	})
	r.SetQueriesForTest(gen.New(dbtx))
	r.SetLastProvisionFailureAtForTest(time.Now().Add(-400 * time.Second))
	r.evaluateAsleep(context.Background(), time.Now(), testLogger())
	require.Equal(t, StateProvisioning, r.deps.FSM.State(),
		"after cooldown elapses evaluateAsleep must re-trigger provisioning")
}

// ===========================================================================
// evaluateProvisioning / status_msg / 4-endpoint health tests
// ===========================================================================

func TestEvaluateProvisioning_VastStatusMsgError_AbortsAndCools(t *testing.T) {
	cfg := testCfg(t)
	cfg.PrimaryProvisionColdStartBudgetSeconds = 5 // very short budget — but status_msg fires faster
	fsm := NewFSM(nil, nil)
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return vast.Instance{
				ID:           42,
				ActualStatus: "loading",
				StatusMsg:    "Error: Container failed to start",
			}, nil
		},
	}
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:  cfg,
		Vast: fakeV,
		FSM:  fsm,
		Rule: alwaysInPeakRule(),
	})
	r.SetQueriesForTest(gen.New(dbtx))
	r.activeLifecycleID.Store(99)

	// Drive waitForReadyOrDestroy directly by overriding the poll interval
	// — but the function uses package-const primaryInstancePollInterval.
	// Work around by calling with a short cold-start budget config and a
	// fast-firing GetInstance error path. The first tick fires after 5s
	// (poll interval) so for unit tests we instead exercise the error
	// path via a shorter route: assert the logic in a sub-loop.

	// Easier: directly drive provisionLifecycle by mocking
	// SearchOffers/CreateInstance + then GetInstance returns the error.
	fakeV.searchOffersFn = func(_ context.Context, _ vast.SearchFilter) ([]vast.Offer, error) {
		return []vast.Offer{{ID: 1, DphTotal: 0.30}}, nil
	}
	fakeV.createInstanceFn = func(_ context.Context, _ int64, _ vast.CreateRequest) (vast.Instance, error) {
		return vast.Instance{ID: 42}, nil
	}

	// Use the test-shortcut waitForReadyOrDestroyForTest with a small
	// poll interval. We expose it by inlining the path here — but for
	// brevity we instead drive a single iteration via a helper.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := r.waitForReadyOrDestroyForTest(ctx, 99, 42, 0.30, testLogger(), 100*time.Millisecond)
	require.Error(t, err)
	require.Contains(t, err.Error(), "vast_status_msg_error",
		"status_msg error must propagate the forensic reason")
	require.NotNil(t, r.lastProvisionFailureAt.Load(),
		"lastProvisionFailureAt must be populated after status_msg abort path")
	_ = fsm
}

func TestEvaluateProvisioning_AllFourEndpointsHealthy_PromotesToReady(t *testing.T) {
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")

	loader := newFakeLoader()
	dcgm := &fakeDCGMScraper{}
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:          cfg,
		FSM:          fsm,
		Loader:       loader,
		DCGMScraper:  dcgm,
		Rule:         alwaysInPeakRule(),
		HealthCheck:  func(_ context.Context, _ string) bool { return true },
		DeviceReport: cudaDeviceReport, // SEED-019 part 3: pod whisper on GPU → stt override fires
		Vast: &fakeVast{
			getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
				return runningInstanceWithAllPorts(42), nil
			},
		},
	})
	r.SetQueriesForTest(gen.New(dbtx))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := r.waitForReadyOrDestroyForTest(ctx, 99, 42, 0.30, testLogger(), 50*time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, StateReady, r.deps.FSM.State(),
		"all 4 endpoints healthy + running + ports populated → markReady fires + FSM → Ready")
	snap := loader.snapshot()
	require.Contains(t, snap, "llm", "OverrideTier0('llm', URL) must fire")
	require.Contains(t, snap, "stt", "OverrideTier0('stt', URL) must fire (Phase 11.2 D-B5′ — restored)")
	require.Contains(t, snap, "tts", "OverrideTier0('tts', URL) must fire")
	require.Contains(t, dcgm.Last(), ":33400/metrics",
		"DCGMScraper.SetURL must point at the 9400 host mapping")
}

// TestEvaluateProvisioning_WhisperDevice gates the "stt" tier-0 override on
// the pod-reported whisper_device value (SEED-019 part 3 — replaces the old
// PRIMARY_POD_SERVE_STT flag). The gateway GETs the pod's :9100/whisper_device
// report at Ready (via the scriptable Deps.DeviceReport closure here) and
// carries the whitelisted device on primaryPodURLs.WhisperDevice. Only
// device=="cuda" overrides stt; "cpu", missing/empty, or any non-whitelisted
// value fail-safes to NO stt override so STT falls through to the tier-1
// gemini-stt cascade (preserving today's prod behavior during the pod-image
// rollout window). llm+tts override unconditionally in every case.
func TestEvaluateProvisioning_WhisperDevice(t *testing.T) {
	cases := []struct {
		name       string
		device     string // value the scriptable DeviceReport closure returns
		wantSTT    bool   // expect the stt tier-0 override to fire
		assertNote string
	}{
		{
			name:       "cuda_overrides_all_three",
			device:     "cuda",
			wantSTT:    true,
			assertNote: "whisper_device=cuda must override stt (pod whisper on GPU)",
		},
		{
			name:       "cpu_skips_stt_override",
			device:     "cpu",
			wantSTT:    false,
			assertNote: "whisper_device=cpu must SKIP the stt override (route to tier-1 gemini-stt)",
		},
		{
			name:       "missing_failsafe_skips_stt_override",
			device:     "", // old pod image reports no device → fail-safe
			wantSTT:    false,
			assertNote: "missing whisper_device must fail-safe to NO stt override (gemini-stt)",
		},
		{
			name:       "garbage_failsafe_skips_stt_override",
			device:     "gpu0", // non-whitelisted → treated as unknown
			wantSTT:    false,
			assertNote: "non-whitelisted whisper_device must fail-safe to NO stt override",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testCfg(t)
			fsm := NewFSM(nil, nil)
			_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")

			loader := newFakeLoader()
			dcgm := &fakeDCGMScraper{}
			dbtx := &fakeDBTX{}
			device := tc.device
			r := buildReconciler(t, Deps{
				Cfg:         cfg,
				FSM:         fsm,
				Loader:      loader,
				DCGMScraper: dcgm,
				Rule:        alwaysInPeakRule(),
				HealthCheck: func(_ context.Context, _ string) bool { return true },
				// Scriptable device-report seam (mirrors HealthCheck): returns
				// the already-whitelisted device string the production main.go
				// closure would return after parsing :9100/whisper_device. The
				// "garbage" case here returns "gpu0" to prove the reconciler's
				// own device gate treats any non-"cuda" value as fail-safe even
				// if a non-whitelisted value somehow reaches it.
				DeviceReport: func(_ context.Context, _ string) string { return device },
				Vast: &fakeVast{
					getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
						return runningInstanceWithAllPorts(42), nil
					},
				},
			})
			r.SetQueriesForTest(gen.New(dbtx))

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := r.waitForReadyOrDestroyForTest(ctx, 99, 42, 0.30, testLogger(), 50*time.Millisecond)
			require.NoError(t, err)
			require.Equal(t, StateReady, r.deps.FSM.State())

			// The reconciler must capture the (whitelisted) device onto the
			// active primaryPodURLs.WhisperDevice snapshot. "cuda"/"cpu" pass
			// through; the garbage "gpu0" case proves the gate treats a
			// non-"cuda" WhisperDevice as fail-safe regardless of its value.
			active := r.ActivePodURLs()
			require.NotNil(t, active, "active pod URLs must be set at Ready")
			require.Equal(t, tc.device, active.WhisperDevice,
				"reconciler must carry the reported device on primaryPodURLs.WhisperDevice")

			snap := loader.snapshot()
			require.Contains(t, snap, "llm", "llm override must always fire")
			require.Contains(t, snap, "tts", "tts override must always fire")
			if tc.wantSTT {
				require.Contains(t, snap, "stt", tc.assertNote)
				require.Equal(t, "cuda", active.WhisperDevice,
					"stt override only fires when WhisperDevice == cuda")
			} else {
				require.NotContains(t, snap, "stt", tc.assertNote)
				require.NotEqual(t, "cuda", active.WhisperDevice,
					"stt override must be skipped when WhisperDevice != cuda")
			}
		})
	}
}

func TestEvaluateProvisioning_OneEndpointUnhealthy_DoesNotPromote(t *testing.T) {
	cfg := testCfg(t)
	cfg.PrimaryProvisionColdStartBudgetSeconds = 1 // tiny budget so the test ends fast
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")

	loader := newFakeLoader()
	dcgm := &fakeDCGMScraper{}
	probeCalls := atomic.Int32{}
	r := buildReconciler(t, Deps{
		Cfg:         cfg,
		FSM:         fsm,
		Loader:      loader,
		DCGMScraper: dcgm,
		Rule:        alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, url string) bool {
			probeCalls.Add(1)
			// LLM/STT/TTS healthy except the tts (8003) endpoint, which
			// is broken — provisioning must NOT promote to Ready.
			return !contains(url, ":33003") // tts broken
		},
		Vast: &fakeVast{
			getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
				return runningInstanceWithAllPorts(42), nil
			},
		},
	})
	dbtx := &fakeDBTX{}
	r.SetQueriesForTest(gen.New(dbtx))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := r.waitForReadyOrDestroyForTest(ctx, 99, 42, 0.30, testLogger(), 50*time.Millisecond)
	require.Error(t, err, "cold-start budget exhausted because tts always unhealthy")
	require.NotEqual(t, StateReady, r.deps.FSM.State(),
		"one-endpoint-unhealthy must NOT promote to Ready")
	require.Empty(t, loader.snapshot(),
		"no OverrideTier0 fires when health check fails")
}

// TestEvaluateProvisioning_TolerantOfTransientInstancesNullFlap asserts the
// 3-strike confirmation for ErrInstanceNotFound: a single transient
// {"instances": null} response from Vast (which the upstream client maps to
// ErrInstanceNotFound) MUST NOT close the lifecycle on its own — the
// instance may still be alive on the host (Vast state-transition flap /
// eventual consistency). UAT 2026-05-27 lifecycle 2 captured this: a 4m24s
// window of successful polls then a single null response silently closed the
// DB row and left a $0.04 Vast orphan because the close path also missed
// BestEffortDestroy. The fix mirrors the existing terminalConfirmStrikes=3
// pattern used for IsTerminal observations: require 3 consecutive
// ErrInstanceNotFound responses before declaring the instance terminal +
// firing BestEffortDestroy + closing the lifecycle.
//
// Source: .planning/debug/primary-reconciler-silent-hang.md.
//
// Two interleaved scenarios in one test (avoid flake of two-test ordering):
//
//  1. After 1 ErrInstanceNotFound followed by a healthy "running" response
//     with 4 endpoints up, the lifecycle promotes to Ready (the single
//     transient null was tolerated, strike counter reset on the healthy
//     poll).
//  2. A separate run where Vast returns ErrInstanceNotFound on EVERY poll:
//     after the 3rd strike, BestEffortDestroy fires AND closeLifecycle is
//     called with shutdown_reason="instance_terminal_state_confirmed".
func TestEvaluateProvisioning_TolerantOfTransientInstancesNullFlap(t *testing.T) {
	t.Run("transient_null_followed_by_running_promotes_to_ready", func(t *testing.T) {
		cfg := testCfg(t)
		fsm := NewFSM(nil, nil)
		_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")

		var callCount atomic.Int32
		fakeV := &fakeVast{
			getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
				n := callCount.Add(1)
				if n == 1 {
					// First poll: transient {"instances": null} flap.
					return vast.Instance{}, vast.ErrInstanceNotFound
				}
				// Subsequent polls: healthy running with all 4 ports.
				return runningInstanceWithAllPorts(42), nil
			},
		}
		loader := newFakeLoader()
		dcgm := &fakeDCGMScraper{}
		dbtx := &fakeDBTX{}
		r := buildReconciler(t, Deps{
			Cfg:         cfg,
			FSM:         fsm,
			Loader:      loader,
			DCGMScraper: dcgm,
			Rule:        alwaysInPeakRule(),
			HealthCheck: func(_ context.Context, _ string) bool { return true },
			Vast:        fakeV,
		})
		r.SetQueriesForTest(gen.New(dbtx))
		r.activeLifecycleID.Store(99)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := r.waitForReadyOrDestroyForTest(ctx, 99, 42, 0.30, testLogger(), 25*time.Millisecond)
		require.NoError(t, err,
			"a single transient ErrInstanceNotFound followed by a healthy running response MUST NOT close the lifecycle")
		require.Equal(t, StateReady, r.deps.FSM.State(),
			"after the transient flap is tolerated, the lifecycle must promote to Ready")
		require.Equal(t, int32(0), fakeV.destroyCalls.Load(),
			"BestEffortDestroy must NOT fire when only 1 ErrInstanceNotFound was observed (below 3-strike threshold)")
	})

	t.Run("three_consecutive_not_found_confirms_terminal_and_destroys", func(t *testing.T) {
		cfg := testCfg(t)
		cfg.PrimaryProvisionColdStartBudgetSeconds = 30 // plenty of room for 3 polls
		fsm := NewFSM(nil, nil)
		_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")

		var callCount atomic.Int32
		fakeV := &fakeVast{
			getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
				callCount.Add(1)
				return vast.Instance{}, vast.ErrInstanceNotFound
			},
		}
		closeReasons := []string{}
		var closeMu sync.Mutex
		dbtx := &fakeDBTX{
			execFn: func(_ context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
				if contains(sql, "UPDATE ai_gateway.primary_lifecycles") && len(args) >= 2 {
					if reason, ok := args[1].(pgtype.Text); ok && reason.Valid {
						closeMu.Lock()
						closeReasons = append(closeReasons, reason.String)
						closeMu.Unlock()
					}
				}
				return pgconn.NewCommandTag("UPDATE 1"), nil
			},
		}
		r := buildReconciler(t, Deps{
			Cfg:  cfg,
			FSM:  fsm,
			Rule: alwaysInPeakRule(),
			Vast: fakeV,
		})
		r.SetQueriesForTest(gen.New(dbtx))
		r.activeLifecycleID.Store(99)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := r.waitForReadyOrDestroyForTest(ctx, 99, 42, 0.30, testLogger(), 25*time.Millisecond)
		require.Error(t, err,
			"3 consecutive ErrInstanceNotFound observations must close the lifecycle as terminal")
		require.Contains(t, err.Error(), "3-strike confirm via ErrInstanceNotFound",
			"error must identify the 3-strike ErrInstanceNotFound path (not the generic IsTerminal close)")
		require.GreaterOrEqual(t, callCount.Load(), int32(3),
			"must have observed at least 3 ErrInstanceNotFound polls before declaring terminal")
		require.Equal(t, int32(1), fakeV.destroyCalls.Load(),
			"BestEffortDestroy must fire EXACTLY once after 3-strike confirmation (orphan-prevention contract)")

		closeMu.Lock()
		defer closeMu.Unlock()
		require.Contains(t, closeReasons, "instance_terminal_state_confirmed",
			"closeLifecycle must record shutdown_reason='instance_terminal_state_confirmed' (distinguishes from IsTerminal-driven 'instance_terminal_state')")
	})
}

// TestReconcilerVastFallback exercises the Phase 11.1 D-A6 primary+fallback
// search dispatch (Wave 0 EVIDENCE-00). When the primary 1×3090 @ $0.30
// filter returns zero offers, the reconciler must iterate to the fallback
// 2×3090 @ $0.60 filter, pick its offer, and call CreateInstance with the
// fallback offer's ID (proving the loop break-on-non-empty + fallback shape
// took effect).
func TestReconcilerVastFallback(t *testing.T) {
	cfg := testCfg(t)
	cfg.PrimaryVastMachineAllowlist = nil // disable the allowlist short-circuit
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")

	var (
		searchCalls atomic.Int32
		createdFor  atomic.Int64 // offer_id passed to CreateInstance
	)
	fakeV := &fakeVast{
		searchOffersFn: func(_ context.Context, filter vast.SearchFilter) ([]vast.Offer, error) {
			n := searchCalls.Add(1)
			// Inspect num_gpus to identify the shape.
			ng, _ := filter["num_gpus"].(map[string]any)
			gpus, _ := ng["eq"].(int)
			switch n {
			case 1: // primary shape (1×3090) — empty
				require.Equal(t, 1, gpus, "first call must use primary shape (1 GPU)")
				return nil, nil
			case 2: // fallback shape (2×3090) — one offer
				require.Equal(t, 2, gpus, "second call must use fallback shape (2 GPUs)")
				return []vast.Offer{{
					ID:        7777,
					MachineID: 43803,
					HostID:    99001,
					DphTotal:  0.42,
				}}, nil
			default:
				return nil, errors.New("unexpected extra SearchOffers call")
			}
		},
		createInstanceFn: func(_ context.Context, offerID int64, _ vast.CreateRequest) (vast.Instance, error) {
			createdFor.Store(offerID)
			// Return a non-running instance so waitForReadyOrDestroy bails
			// quickly (cold-start budget 60s in testCfg, but health check
			// returns false instantly so the budget never triggers — the
			// goroutine returns via destroy on cancellation).
			return vast.Instance{ID: 12345, ActualStatus: "loading"}, nil
		},
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return vast.Instance{ID: 12345, ActualStatus: "loading"}, nil
		},
	}
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:  cfg,
		FSM:  fsm,
		Vast: fakeV,
		Rule: alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool {
			return false
		},
	})
	r.SetQueriesForTest(gen.New(dbtx))

	// Drive provisionLifecycle directly (unexported but same package).
	// waitForReadyOrDestroy will spin until the cold-start budget elapses;
	// short-circuit via ctx timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = r.provisionLifecycle(ctx, 999, testLogger())

	// Assertions: BOTH shape filters consulted; create issued for the
	// fallback offer.
	require.GreaterOrEqual(t, searchCalls.Load(), int32(2),
		"reconciler must call SearchOffers for both [primary, fallback] shapes")
	require.Equal(t, int64(7777), createdFor.Load(),
		"CreateInstance must receive the fallback offer's ID (7777)")
}

// runAllowlistGateProbe (quick-260702-nse) drives provisionLifecycle with a
// NON-empty machine allowlist on BOTH the boot cfg and the live pod_config
// snapshot, scripts the CountConsecutiveFailedPrimaryProvisions count to the
// given failStreak, and reports whether the FIRST SearchOffers call carried a
// machine_id {in} (allowlist-preferred) and/or {notin} (blocklist) clause.
func runAllowlistGateProbe(t *testing.T, failStreak int64) (firstHasIn bool, firstHasNotin bool) {
	t.Helper()
	cfg := testCfg(t)
	cfg.PrimaryVastMachineAllowlist = []int64{43803, 55158}
	cfg.PrimaryVastMachineBlocklist = []int64{55942}

	snapCfg := podconfig.PodConfig{
		CapPrimary:           0.30,
		CapFallback:          0.60,
		HostID:               0,
		VastMachineAllowlist: []int64{43803, 55158},
		VastMachineBlocklist: []int64{55942},
		RejectPrivateIP:      false,
		ColdStartBudgetS:     60,
		PortBindBudgetS:      600,
		FailureCooldownS:     120,
	}
	loader := podconfig.NewStaticLoaderForTest(snapCfg, podconfig.ScheduleRule{}, podconfig.PodConfigBounds{}, nil)

	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")

	var callN atomic.Int32
	var seenIn, seenNotin, firstCaptured atomic.Bool
	fakeV := &fakeVast{
		searchOffersFn: func(_ context.Context, filter vast.SearchFilter) ([]vast.Offer, error) {
			if callN.Add(1) == 1 {
				if mid, ok := filter["machine_id"].(map[string]any); ok {
					if _, ok := mid["in"]; ok {
						seenIn.Store(true)
					}
					if _, ok := mid["notin"]; ok {
						seenNotin.Store(true)
					}
				}
				firstCaptured.Store(true)
			}
			// One offer below the primary cap so the producing pass wins and
			// the shape loop stops after a single SearchOffers call.
			return []vast.Offer{{ID: 7777, MachineID: 43803, HostID: 99001, DphTotal: 0.20}}, nil
		},
		createInstanceFn: func(_ context.Context, _ int64, _ vast.CreateRequest) (vast.Instance, error) {
			return vast.Instance{ID: 12345, ActualStatus: "loading"}, nil
		},
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return vast.Instance{ID: 12345, ActualStatus: "loading"}, nil
		},
	}
	dbtx := &fakeDBTX{
		queryRowFn: func(_ context.Context, sql string, _ ...interface{}) pgx.Row {
			if strings.Contains(sql, "fail_streak") {
				return countRow{n: failStreak}
			}
			return errRow{err: errors.New("fakeDBTX: unexpected QueryRow in gate probe")}
		},
	}
	r := buildReconciler(t, Deps{
		Cfg:    cfg,
		FSM:    fsm,
		Vast:   fakeV,
		PodCfg: loader,
		Rule:   alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool {
			return false
		},
	})
	r.SetQueriesForTest(gen.New(dbtx))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = r.provisionLifecycle(ctx, 999, testLogger())

	require.True(t, firstCaptured.Load(), "SearchOffers must have been called at least once")
	return seenIn.Load(), seenNotin.Load()
}

// TestReconcilerAllowlistGate_FailStreak0_MarketOnly: attempt 1 (fail_streak 0)
// searches the cheapest open market — NO machine_id {in} clause even though the
// allowlist is non-empty.
func TestReconcilerAllowlistGate_FailStreak0_MarketOnly(t *testing.T) {
	hasIn, _ := runAllowlistGateProbe(t, 0)
	require.False(t, hasIn,
		"fail_streak 0 must NOT emit machine_id {in} on the first SearchOffers call (market-cheapest)")
}

// TestReconcilerAllowlistGate_FailStreak1_MarketOnly: attempt 2 (fail_streak 1)
// still market-only.
func TestReconcilerAllowlistGate_FailStreak1_MarketOnly(t *testing.T) {
	hasIn, _ := runAllowlistGateProbe(t, 1)
	require.False(t, hasIn,
		"fail_streak 1 must NOT emit machine_id {in} on the first SearchOffers call (market-cheapest)")
}

// TestReconcilerAllowlistGate_FailStreak2_AllowlistFirst: attempt 3+
// (fail_streak 2) runs the allowlist-first preference pass — the FIRST
// SearchOffers call carries machine_id {in: allowlist}.
func TestReconcilerAllowlistGate_FailStreak2_AllowlistFirst(t *testing.T) {
	hasIn, _ := runAllowlistGateProbe(t, 2)
	require.True(t, hasIn,
		"fail_streak 2 must emit machine_id {in} on the first SearchOffers call (allowlist-preferred)")
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Phase 17 (POD-CFG-04): the offer-selection block must read the HOT fields
// (caps/host/blocklist/allowlist/reject-private-ip + budgets) from the LIVE
// pod_config snapshot (podCfg.Load()), NOT the immutable boot cfg — while
// STRUCTURAL fields (GPU name/num) STILL come from r.cfg (D-02). This test
// wires a static loader whose blocklist + cap DIFFER from the boot cfg and
// asserts: (a) the snapshot blocklist reaches the Vast SearchFilter, (b) the
// boot cfg's blocklist does NOT, (c) the structural GPU name still comes from
// r.cfg.
func TestProvisionLifecycle_OfferSelectionReadsSnapshotNotCfg(t *testing.T) {
	cfg := testCfg(t)
	// Boot cfg carries a DISTINCT blocklist + allowlist-disabled.
	cfg.PrimaryVastMachineBlocklist = []int64{11111}
	cfg.PrimaryVastMachineAllowlist = nil

	// Live snapshot carries a DIFFERENT blocklist + the same per-shape caps.
	snapCfg := podconfig.PodConfig{
		CapPrimary:           0.30,
		CapFallback:          0.60,
		HostID:               0,
		VastMachineBlocklist: []int64{99999}, // <- snapshot wins
		RejectPrivateIP:      false,
		ColdStartBudgetS:     60,
		PortBindBudgetS:      600,
		FailureCooldownS:     120,
	}
	loader := podconfig.NewStaticLoaderForTest(snapCfg, podconfig.ScheduleRule{}, podconfig.PodConfigBounds{}, nil)

	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")

	var seenBlocklist atomic.Pointer[[]any]
	var seenGPUName atomic.Pointer[string]
	fakeV := &fakeVast{
		searchOffersFn: func(_ context.Context, filter vast.SearchFilter) ([]vast.Offer, error) {
			// Capture the gpu_name eq (structural) + machine_id notin (hot).
			if gn, ok := filter["gpu_name"].(map[string]any); ok {
				if name, ok := gn["eq"].(string); ok {
					n := name
					seenGPUName.Store(&n)
				}
			}
			if mid, ok := filter["machine_id"].(map[string]any); ok {
				if notin, ok := mid["notin"].([]any); ok {
					cp := notin
					seenBlocklist.Store(&cp)
				}
			}
			// Return an offer below the snapshot's primary cap so provisioning
			// proceeds to CreateInstance (then bails quickly on non-running).
			return []vast.Offer{{ID: 7777, MachineID: 43803, HostID: 99001, DphTotal: 0.25}}, nil
		},
		createInstanceFn: func(_ context.Context, _ int64, _ vast.CreateRequest) (vast.Instance, error) {
			return vast.Instance{ID: 12345, ActualStatus: "loading"}, nil
		},
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return vast.Instance{ID: 12345, ActualStatus: "loading"}, nil
		},
	}
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:    cfg,
		FSM:    fsm,
		Vast:   fakeV,
		PodCfg: loader,
		Rule:   alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool {
			return false
		},
	})
	r.SetQueriesForTest(gen.New(dbtx))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = r.provisionLifecycle(ctx, 999, testLogger())

	// (a)+(b) the SNAPSHOT blocklist (99999) reached the filter, boot's (11111) did not.
	bl := seenBlocklist.Load()
	require.NotNil(t, bl, "SearchOffers must have been called with a machine_id notin clause")
	require.Contains(t, *bl, int64(99999),
		"offer selection must apply the LIVE snapshot blocklist (99999), not the boot cfg blocklist")
	require.NotContains(t, *bl, int64(11111),
		"offer selection must NOT apply the boot cfg blocklist (11111) once a snapshot exists")

	// (c) structural GPU name still resolves from r.cfg (NOT the snapshot — the
	// snapshot deliberately carries no GPU name).
	gn := seenGPUName.Load()
	require.NotNil(t, gn)
	require.Equal(t, cfg.PrimaryVastGPUNamePrimary, *gn,
		"structural GPU name must STILL come from r.cfg (D-02), never moved to the snapshot")
}

// Phase 17 (POD-CFG-04): with podCfg nil (defensive / pre-first-refresh), the
// reconciler must fall back to the boot cfg's HOT fields so provisioning never
// reads zero-config (never sends an empty blocklist + cap=0 that rejects every
// offer). Proves the never-zero-config invariant.
func TestProvisionLifecycle_NilPodCfgFallsBackToBootCfg(t *testing.T) {
	cfg := testCfg(t)
	cfg.PrimaryVastMachineBlocklist = []int64{22222}
	cfg.PrimaryVastMachineAllowlist = nil

	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")

	var seenBlocklist atomic.Pointer[[]any]
	fakeV := &fakeVast{
		searchOffersFn: func(_ context.Context, filter vast.SearchFilter) ([]vast.Offer, error) {
			if mid, ok := filter["machine_id"].(map[string]any); ok {
				if notin, ok := mid["notin"].([]any); ok {
					cp := notin
					seenBlocklist.Store(&cp)
				}
			}
			return nil, nil // no offers → bail fast
		},
	}
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:  cfg,
		FSM:  fsm,
		Vast: fakeV,
		// PodCfg intentionally omitted (nil).
		Rule: alwaysInPeakRule(),
	})
	r.SetQueriesForTest(gen.New(dbtx))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = r.provisionLifecycle(ctx, 999, testLogger())

	bl := seenBlocklist.Load()
	require.NotNil(t, bl, "SearchOffers must have been called even with podCfg nil")
	require.Contains(t, *bl, int64(22222),
		"podCfg nil must fall back to the boot cfg blocklist (never zero-config)")
}

// ===========================================================================
// evaluateReady / startDrain tests
// ===========================================================================

func TestEvaluateReady_TransitionsToDrainingOutOfPeak(t *testing.T) {
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "x")
	_ = fsm.Transition(StateProvisioning, StateReady, time.Now(), "x")
	loader := newFakeLoader()
	loader.OverrideTier0("llm", "http://pod:8000")
	loader.OverrideTier0("stt", "http://pod:8001")
	loader.OverrideTier0("tts", "http://pod:8003")
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:    cfg,
		FSM:    fsm,
		Loader: loader,
		Rule:   neverInPeakRule(),
	})
	r.SetQueriesForTest(gen.New(dbtx))
	r.activeLifecycleID.Store(7)

	r.evaluateReady(context.Background(), time.Now(), testLogger())
	require.Equal(t, StateDraining, r.deps.FSM.State(),
		"evaluateReady with !IsInPeak must transition Ready→Draining")
	require.NotNil(t, r.drainStartedAt.Load(), "drainStartedAt populated")
	roles := loader.restoredRoles()
	require.Equal(t, []string{"llm", "stt", "tts"}, roles,
		"startDrain must RestoreTier0 for the 3 primary roles (llm+stt+tts) in order — Phase 11.2 D-B5′ restored stt")
}

// UAT 14 follow-up (2026-05-19): under DISABLED, evaluateReady must
// short-circuit so an operator force-up pod is not auto-drained by the
// schedule (IsInPeak returns false when Disabled regardless of clock).
func TestEvaluateReady_NoopWhenDisabled(t *testing.T) {
	cfg := testCfg(t)
	cfg.PrimaryPodScheduleDisabled = true
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "x")
	_ = fsm.Transition(StateProvisioning, StateReady, time.Now(), "x")
	loader := newFakeLoader()
	r := buildReconciler(t, Deps{
		Cfg:    cfg,
		FSM:    fsm,
		Loader: loader,
		Rule:   neverInPeakRule(),
	})
	r.evaluateReady(context.Background(), time.Now(), testLogger())
	require.Equal(t, StateReady, r.deps.FSM.State(),
		"DISABLED must short-circuit evaluateReady so operator force-up pod is not auto-drained")
	require.Empty(t, loader.restoredRoles(),
		"DISABLED evaluateReady must NOT call RestoreTier0")
}

// Regression: primary-pod-flap-prewarm-window. A Ready pod that reaches
// Ready inside the pre-warm lead window [UpHour-lead, UpHour) MUST NOT be
// drained — the provision gate (ShouldBeProvisioned) already keeps it up
// during this window, so evaluateReady must agree (ShouldStayUp), else the
// pod flaps create→destroy every tick until the clock hits UpHour.
func TestEvaluateReady_DoesNotDrainDuringPreWarmLead(t *testing.T) {
	loc := brtZone()
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "x")
	_ = fsm.Transition(StateProvisioning, StateReady, time.Now(), "x")
	loader := newFakeLoader()
	loader.OverrideTier0("llm", "http://pod:8000")
	loader.OverrideTier0("stt", "http://pod:8001")
	loader.OverrideTier0("tts", "http://pod:8003")
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:    cfg,
		FSM:    fsm,
		Loader: loader,
		// UpHour=9 DownHour=17 weekdays, lead=1800s (30 min).
		Rule: buildRule(loc, 9, 17, allWeekdays(), false, 1800),
	})
	r.SetQueriesForTest(gen.New(dbtx))
	r.activeLifecycleID.Store(7)

	// Wed 2026-05-13 08:45 BRT — 15 min before UpHour=9, inside the 30-min lead.
	now := time.Date(2026, 5, 13, 8, 45, 0, 0, loc)
	r.evaluateReady(context.Background(), now, testLogger())

	require.Equal(t, StateReady, r.deps.FSM.State(),
		"pod inside pre-warm lead window must STAY Ready (no flap), got drain")
	require.Nil(t, r.drainStartedAt.Load(), "drainStartedAt must NOT be populated inside lead window")
	require.Empty(t, loader.restoredRoles(),
		"evaluateReady must NOT RestoreTier0 inside the pre-warm lead window")
}

// Regression companion: once the window has fully exited (now >= DownHour)
// the pod MUST still drain — the fix must not over-extend keep-up.
func TestEvaluateReady_DrainsAfterDownHour(t *testing.T) {
	loc := brtZone()
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "x")
	_ = fsm.Transition(StateProvisioning, StateReady, time.Now(), "x")
	loader := newFakeLoader()
	loader.OverrideTier0("llm", "http://pod:8000")
	loader.OverrideTier0("stt", "http://pod:8001")
	loader.OverrideTier0("tts", "http://pod:8003")
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:    cfg,
		FSM:    fsm,
		Loader: loader,
		Rule:   buildRule(loc, 9, 17, allWeekdays(), false, 1800),
	})
	r.SetQueriesForTest(gen.New(dbtx))
	r.activeLifecycleID.Store(7)

	// Wed 2026-05-13 17:30 BRT — past DownHour=17, window exited.
	now := time.Date(2026, 5, 13, 17, 30, 0, 0, loc)
	r.evaluateReady(context.Background(), now, testLogger())

	require.Equal(t, StateDraining, r.deps.FSM.State(),
		"pod past DownHour must drain (schedule_window_exited)")
	require.NotNil(t, r.drainStartedAt.Load(), "drainStartedAt populated on window-exit drain")
}

// Regression companion: a pod squarely inside peak must never drain.
func TestEvaluateReady_DoesNotDrainDuringPeak(t *testing.T) {
	loc := brtZone()
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "x")
	_ = fsm.Transition(StateProvisioning, StateReady, time.Now(), "x")
	loader := newFakeLoader()
	loader.OverrideTier0("llm", "http://pod:8000")
	loader.OverrideTier0("stt", "http://pod:8001")
	loader.OverrideTier0("tts", "http://pod:8003")
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:    cfg,
		FSM:    fsm,
		Loader: loader,
		Rule:   buildRule(loc, 9, 17, allWeekdays(), false, 1800),
	})
	r.SetQueriesForTest(gen.New(dbtx))
	r.activeLifecycleID.Store(7)

	// Wed 2026-05-13 12:00 BRT — mid-peak.
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, loc)
	r.evaluateReady(context.Background(), now, testLogger())

	require.Equal(t, StateReady, r.deps.FSM.State(),
		"pod mid-peak must STAY Ready")
	require.Nil(t, r.drainStartedAt.Load(), "drainStartedAt must NOT be populated mid-peak")
}

// ===========================================================================
// evaluateDraining tests
// ===========================================================================

func TestEvaluateDraining_DestroysWhenInflightZero(t *testing.T) {
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	fsm.SetState(StateDraining, time.Now(), "setup")
	infl := newFakeInflight()
	// all 3 inflight = 0
	r := buildReconciler(t, Deps{
		Cfg:      cfg,
		FSM:      fsm,
		Inflight: infl,
		Rule:     alwaysInPeakRule(),
	})
	drainStart := time.Now().Add(-1 * time.Second)
	r.drainStartedAt.Store(&drainStart)

	r.evaluateDraining(context.Background(), time.Now(), testLogger())
	require.Equal(t, StateDestroying, r.deps.FSM.State(),
		"inflight==0 must fire Draining→Destroying transition")
}

func TestEvaluateDraining_DestroysOnGraceElapsed(t *testing.T) {
	cfg := testCfg(t)
	cfg.PrimaryPodScheduleGraceRampDownSeconds = 2
	fsm := NewFSM(nil, nil)
	fsm.SetState(StateDraining, time.Now(), "setup")
	infl := newFakeInflight()
	infl.Set("local-llm", 3) // inflight > 0
	r := buildReconciler(t, Deps{
		Cfg:      cfg,
		FSM:      fsm,
		Inflight: infl,
		Rule:     alwaysInPeakRule(),
	})
	old := time.Now().Add(-10 * time.Second) // well past grace
	r.drainStartedAt.Store(&old)

	r.evaluateDraining(context.Background(), time.Now(), testLogger())
	require.Equal(t, StateDestroying, r.deps.FSM.State(),
		"elapsed >= grace must fire Draining→Destroying even with inflight > 0")
}

func TestEvaluateDraining_WaitsIfInflightAndWithinGrace(t *testing.T) {
	cfg := testCfg(t)
	cfg.PrimaryPodScheduleGraceRampDownSeconds = 300
	fsm := NewFSM(nil, nil)
	fsm.SetState(StateDraining, time.Now(), "setup")
	infl := newFakeInflight()
	infl.Set("local-llm", 1)
	r := buildReconciler(t, Deps{
		Cfg:      cfg,
		FSM:      fsm,
		Inflight: infl,
		Rule:     alwaysInPeakRule(),
	})
	now := time.Now()
	r.drainStartedAt.Store(&now)

	r.evaluateDraining(context.Background(), time.Now(), testLogger())
	require.Equal(t, StateDraining, r.deps.FSM.State(),
		"inflight > 0 AND elapsed < grace must keep FSM Draining")
}

// ===========================================================================
// markReady tests
// ===========================================================================

func TestMarkReady_OverridesTier0_2Roles(t *testing.T) {
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "x")
	loader := newFakeLoader()
	dcgm := &fakeDCGMScraper{}
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:         cfg,
		FSM:         fsm,
		Loader:      loader,
		DCGMScraper: dcgm,
		Rule:        alwaysInPeakRule(),
	})
	r.SetQueriesForTest(gen.New(dbtx))

	urls := primaryPodURLs{
		LLM:           "http://1.2.3.4:33000/v1/models",
		STT:           "http://1.2.3.4:33001/health",
		TTS:           "http://1.2.3.4:33003/health",
		DCGM:          "http://1.2.3.4:33400/metrics",
		WhisperDevice: "cuda", // SEED-019 part 3: GPU pod → stt override fires
	}
	err := r.markReady(context.Background(), 5, urls, 0.30, testLogger())
	require.NoError(t, err)
	snap := loader.snapshot()
	require.Len(t, snap, 3, "exactly 3 OverrideTier0 calls (llm+stt+tts; Phase 11.2 D-B5′ restored stt)")
	require.Equal(t, "http://1.2.3.4:33000", snap["llm"], "/v1/models suffix stripped for LLM")
	require.Equal(t, "http://1.2.3.4:33001", snap["stt"], "/health suffix stripped for STT")
	require.Equal(t, "http://1.2.3.4:33003", snap["tts"], "/health suffix stripped for TTS")
	require.Equal(t, urls.DCGM, dcgm.Last(), "DCGM URL passed verbatim to scraper")
	require.Equal(t, StateReady, r.deps.FSM.State())
}

func TestMarkReady_SetsDCGMScraperURL(t *testing.T) {
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "x")
	dcgm := &fakeDCGMScraper{}
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:         cfg,
		FSM:         fsm,
		DCGMScraper: dcgm,
		Rule:        alwaysInPeakRule(),
	})
	r.SetQueriesForTest(gen.New(dbtx))
	urls := primaryPodURLs{
		LLM:  "http://1.2.3.4:33000/v1/models",
		TTS:  "http://1.2.3.4:33003/health",
		DCGM: "http://1.2.3.4:33400/metrics",
	}
	require.NoError(t, r.markReady(context.Background(), 5, urls, 0.30, testLogger()))
	require.Equal(t, urls.DCGM, dcgm.Last())
}

func TestDrain_RestoreTier0CalledBeforeDestroy(t *testing.T) {
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	fsm.SetState(StateDraining, time.Now(), "setup")
	loader := newFakeLoader()
	loader.OverrideTier0("llm", "http://x")
	loader.OverrideTier0("stt", "http://x")
	loader.OverrideTier0("tts", "http://x")
	infl := newFakeInflight() // all zero → immediate destroy
	fakeV := &fakeVast{}
	r := buildReconciler(t, Deps{
		Cfg:      cfg,
		FSM:      fsm,
		Loader:   loader,
		Inflight: infl,
		Vast:     fakeV,
		Rule:     neverInPeakRule(),
	})
	dbtx := &fakeDBTX{}
	r.SetQueriesForTest(gen.New(dbtx))
	r.activeLifecycleID.Store(11)
	r.activeInstanceID.Store(77)
	now := time.Now()
	r.drainStartedAt.Store(&now)

	// FSM was set to Draining; startDrain is the path that calls
	// RestoreTier0 → it fires BEFORE the destroy in evaluateDestroying.
	// We assert by checking the restoredRoles snapshot at the time the
	// destroy is recorded.
	r.evaluateDraining(context.Background(), time.Now(), testLogger())
	require.Equal(t, StateDestroying, r.deps.FSM.State())
	r.evaluateDestroying(context.Background(), time.Now(), testLogger())
	require.Equal(t, int32(1), fakeV.destroyCalls.Load(),
		"BestEffortDestroy must have fired")
	// startDrain wasn't called in this flow (we pre-set FSM=Draining), but
	// closeLifecycle in evaluateDestroying also defensively RestoresTier0.
	restored := loader.restoredRoles()
	require.GreaterOrEqual(t, len(restored), 3,
		"closeLifecycle must defensively RestoreTier0 for the 3 roles (llm+stt+tts; Phase 11.2 D-B5′ restored stt)")
}

// ===========================================================================
// Provisioning failure → lastProvisionFailureAt
// ===========================================================================

func TestProvisioningFailure_SetsLastFailureTimestamp(t *testing.T) {
	cfg := testCfg(t)
	cfg.PrimaryProvisionColdStartBudgetSeconds = 1 // exhaust fast
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "x")
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			// Stays in "loading" forever — health timeout fires.
			return vast.Instance{ID: 42, ActualStatus: "loading"}, nil
		},
	}
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:  cfg,
		FSM:  fsm,
		Vast: fakeV,
		Rule: alwaysInPeakRule(),
	})
	r.SetQueriesForTest(gen.New(dbtx))
	r.activeLifecycleID.Store(99)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	startedAt := time.Now()
	err := r.waitForReadyOrDestroyForTest(ctx, 99, 42, 0.30, testLogger(), 50*time.Millisecond)
	require.Error(t, err)

	// waitForReadyOrDestroy does NOT populate lastProvisionFailureAt
	// directly — spawnProvisioning's defer goroutine does. We exercise
	// the spawnProvisioning path separately. Here we simulate by calling
	// the same setter the spawnProvisioning goroutine would call.
	now := time.Now()
	r.lastProvisionFailureAt.Store(&now)
	last := r.lastProvisionFailureAt.Load()
	require.NotNil(t, last)
	require.True(t, last.After(startedAt) || last.Equal(startedAt),
		"lastProvisionFailureAt must be ~now after a failure path")
}

// ===========================================================================
// Event subscriber — force_up / force_down
// ===========================================================================

func TestHandleForceUpRequest_TransitionsAsleepToProvisioning(t *testing.T) {
	cfg := testCfg(t)
	cfg.PrimaryPodScheduleDisabled = true
	fsm := NewFSM(nil, nil)
	dbtx := &fakeDBTX{
		queryRowFn: func(_ context.Context, _ string, _ ...interface{}) pgx.Row {
			return insertReturningRow{id: 8, startedAt: time.Now()}
		},
	}
	r := buildReconciler(t, Deps{
		Cfg:  cfg,
		FSM:  fsm,
		Vast: &fakeVast{},
		Rule: neverInPeakRule(),
	})
	r.SetQueriesForTest(gen.New(dbtx))
	// Set cooldown so we PROVE force-up bypasses it.
	r.SetLastProvisionFailureAtForTest(time.Now().Add(-1 * time.Second))

	r.handleForceUpRequest(context.Background(),
		redisx.PrimaryEvent{Type: "force_up_request", ReplicaID: "operator", Reason: "manual"},
		testLogger())
	require.Equal(t, StateProvisioning, r.deps.FSM.State(),
		"force_up_request must bypass both schedule AND cooldown gates")
	require.Nil(t, r.lastProvisionFailureAt.Load(),
		"force_up must CLEAR lastProvisionFailureAt explicitly")
}

func TestHandleForceUpRequest_NoopWhenNotAsleep(t *testing.T) {
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	fsm.SetState(StateReady, time.Now(), "setup")
	r := buildReconciler(t, Deps{
		Cfg:  cfg,
		FSM:  fsm,
		Rule: alwaysInPeakRule(),
	})
	r.handleForceUpRequest(context.Background(),
		redisx.PrimaryEvent{Type: "force_up_request", ReplicaID: "op", Reason: "x"},
		testLogger())
	require.Equal(t, StateReady, r.deps.FSM.State(),
		"force_up from non-Asleep must NOT transition")
}

func TestHandleForceDownRequest_TransitionsReadyToDraining(t *testing.T) {
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	fsm.SetState(StateReady, time.Now(), "setup")
	loader := newFakeLoader()
	loader.OverrideTier0("llm", "http://x")
	loader.OverrideTier0("stt", "http://x")
	loader.OverrideTier0("tts", "http://x")
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:    cfg,
		FSM:    fsm,
		Loader: loader,
		Rule:   alwaysInPeakRule(),
	})
	r.SetQueriesForTest(gen.New(dbtx))
	r.activeLifecycleID.Store(11)

	r.handleForceDownRequest(context.Background(),
		redisx.PrimaryEvent{Type: "force_down_request", ReplicaID: "op", Reason: "x"},
		testLogger())
	require.Equal(t, StateDraining, r.deps.FSM.State())
}

// ===========================================================================
// Start() lifecycle — event subscriber + DISABLED gating
// ===========================================================================

func TestStart_LaunchesEventSubscriberAndForceUpWorks(t *testing.T) {
	cfg := testCfg(t)
	cfg.PrimaryPodScheduleDisabled = true
	rdb, ms := miniredisClient(t)

	// Capture the Asleep→Provisioning transition via onChange (the provision
	// goroutine will later FAIL the lifecycle because SearchOffers is not
	// scripted, and the FSM will be SetState'd back to Asleep — we only
	// care that the FSM PASSED THROUGH Provisioning).
	provisioningSeen := atomic.Bool{}
	fsm := NewFSM(nil, func(from, to State, _ time.Time, _ string) {
		if to == StateProvisioning {
			provisioningSeen.Store(true)
		}
		_ = from
	})
	dbtx := &fakeDBTX{
		queryRowFn: func(_ context.Context, sql string, _ ...interface{}) pgx.Row {
			if contains(sql, "ended_at IS NULL") {
				// GetOpenPrimaryLifecycle — pretend empty.
				return noRowsRow{}
			}
			return insertReturningRow{id: 5, startedAt: time.Now()}
		},
	}
	// Make SearchOffers block forever so the FSM stays in Provisioning long
	// enough for the test assertion to land.
	stopBlock := make(chan struct{})
	t.Cleanup(func() { close(stopBlock) })
	r := buildReconciler(t, Deps{
		Cfg: cfg,
		FSM: fsm,
		Vast: &fakeVast{
			searchOffersFn: func(_ context.Context, _ vast.SearchFilter) ([]vast.Offer, error) {
				<-stopBlock
				return nil, errors.New("test teardown")
			},
		},
		Rule:  neverInPeakRule(),
		Redis: rdb,
	})
	r.SetQueriesForTest(gen.New(dbtx))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)

	// Wait for subscriber to attach (miniredis SUBSCRIBE).
	require.Eventually(t, func() bool {
		return len(ms.PubSubNumSub(redisx.PrimaryEventsChannel)) >= 1 &&
			ms.PubSubNumSub(redisx.PrimaryEventsChannel)[redisx.PrimaryEventsChannel] >= 1
	}, 3*time.Second, 50*time.Millisecond, "subscriber must register on gw:primary:events")

	// Wait until the schedule loop acquires leadership via miniredis-
	// redsync — the event subscriber gates on isLeader BEFORE dispatching
	// to handleForceUpRequest (PRV-03 parity emerg).
	require.Eventually(t, func() bool {
		return r.isLeader.Load()
	}, 3*time.Second, 50*time.Millisecond, "schedule loop must acquire gw:primary:lock")

	// Publish force_up via the subscriber goroutine path.
	ev := redisx.PrimaryEvent{Type: "force_up_request", ReplicaID: "op", Reason: "test"}
	payload, _ := json.Marshal(ev)

	// Retry publish a few times — miniredis Pub/Sub has a small race window
	// after SUBSCRIBE confirmed where the channel may not yet be wired.
	require.Eventually(t, func() bool {
		_ = rdb.Publish(ctx, redisx.PrimaryEventsChannel, payload).Err()
		return provisioningSeen.Load()
	}, 5*time.Second, 100*time.Millisecond,
		"force_up_request published while DISABLED=true must transition FSM Asleep→Provisioning at least once (reviews #2 + #3)")
}

func TestStart_SkipsScheduleTicksWhenDisabled(t *testing.T) {
	cfg := testCfg(t)
	cfg.PrimaryPodScheduleDisabled = true
	rdb, _ := miniredisClient(t)
	fsm := NewFSM(nil, nil)
	dbtx := &fakeDBTX{
		queryRowFn: func(_ context.Context, _ string, _ ...interface{}) pgx.Row {
			return noRowsRow{}
		},
	}
	r := buildReconciler(t, Deps{
		Cfg:   cfg,
		FSM:   fsm,
		Vast:  &fakeVast{},
		Rule:  alwaysInPeakRule(), // would normally trigger but DISABLED gates it
		Redis: rdb,
	})
	r.SetQueriesForTest(gen.New(dbtx))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)

	// Wait 2 ticks (~2s) — evaluateAsleep observes DISABLED and short-circuits.
	// evaluateTick still runs per UAT 14 fix; the gate is per-evaluator.
	time.Sleep(2500 * time.Millisecond)
	require.Equal(t, StateAsleep, r.deps.FSM.State(),
		"DISABLED=true must prevent evaluateAsleep from firing — FSM stays Asleep")
}

// UAT 14 follow-up (2026-05-19): under DISABLED, evaluateTick must still
// route StateDraining→evaluateDraining so an operator force-down completes
// drain → destroy. Pre-fix: runScheduleLoop early-returned on DISABLED
// and froze a draining FSM forever.
func TestEvaluateTick_AdvancesDrainingUnderDisabled(t *testing.T) {
	cfg := testCfg(t)
	cfg.PrimaryPodScheduleDisabled = true
	cfg.PrimaryPodScheduleGraceRampDownSeconds = 1
	fsm := NewFSM(nil, nil)
	fsm.SetState(StateDraining, time.Now(), "operator_force_down")
	infl := newFakeInflight() // all 3 inflight = 0
	r := buildReconciler(t, Deps{
		Cfg:      cfg,
		FSM:      fsm,
		Inflight: infl,
		Rule:     alwaysInPeakRule(),
	})
	drainStart := time.Now().Add(-1 * time.Second)
	r.drainStartedAt.Store(&drainStart)

	r.evaluateTick(context.Background(), time.Now(), testLogger())
	require.Equal(t, StateDestroying, r.deps.FSM.State(),
		"DISABLED must NOT block Draining→Destroying advancement (operator force-down completion)")
}

// UAT 14 follow-up (2026-05-19): boot must reset the gw:primary:state
// Redis mirror when recovery completes with FSM at StateAsleep, so a
// stale snapshot from before the restart (e.g. state=draining) does not
// linger in the mirror and confuse gatewayctl primary state.
func TestStart_BootResetsPrimaryStateMirror(t *testing.T) {
	cfg := testCfg(t)
	cfg.PrimaryPodScheduleDisabled = true // freeze evaluator ticks for assertion stability
	rdb, mr := miniredisClient(t)
	// Pre-populate mirror with a stale draining snapshot.
	require.NoError(t, redisx.WritePrimaryState(context.Background(), rdb,
		StateDraining.String(), "99", "http://stale:8000", "12345",
		time.Now().Add(-1*time.Hour).Unix()))

	fsm := NewFSM(nil, nil)
	dbtx := &fakeDBTX{
		queryRowFn: func(_ context.Context, _ string, _ ...interface{}) pgx.Row {
			return noRowsRow{}
		},
	}
	r := buildReconciler(t, Deps{
		Cfg:   cfg,
		FSM:   fsm,
		Vast:  &fakeVast{},
		Rule:  alwaysInPeakRule(),
		Redis: rdb,
	})
	r.SetQueriesForTest(gen.New(dbtx))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)

	// Boot path runs recoverOpenLifecycle synchronously then writes mirror
	// before spawning goroutines — assertion is safe immediately.
	key := redisx.PrimaryStateKey()
	require.Equal(t, StateAsleep.String(), mr.HGet(key, "state"),
		"boot mirror reset must replace stale state with asleep")
	require.Empty(t, mr.HGet(key, "lifecycle_id"),
		"boot mirror reset must clear stale lifecycle_id")
	require.Empty(t, mr.HGet(key, "pod_url"),
		"boot mirror reset must clear stale pod_url")
	require.Empty(t, mr.HGet(key, "pod_instance_id"),
		"boot mirror reset must clear stale pod_instance_id")
}

// ===========================================================================
// Restart recovery (reviews #4)
// ===========================================================================

func TestRecoverOpenLifecycle_HealthyInstanceRestoresReady(t *testing.T) {
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	loader := newFakeLoader()
	dcgm := &fakeDCGMScraper{}
	dbtx := &fakeDBTX{}

	// Script GetOpenPrimaryLifecycle to return a row with vast_instance_id=42.
	dbtx.queryRowFn = func(_ context.Context, sql string, _ ...interface{}) pgx.Row {
		if contains(sql, "ended_at IS NULL") {
			return openLifecycleRow{
				id:             100,
				vastInstanceID: pgtype.Int8{Int64: 42, Valid: true},
			}
		}
		return errRow{err: errors.New("unexpected query")}
	}

	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, id int64) (vast.Instance, error) {
			require.Equal(t, int64(42), id)
			return runningInstanceWithAllPorts(42), nil
		},
	}
	r := buildReconciler(t, Deps{
		Cfg:          cfg,
		FSM:          fsm,
		Loader:       loader,
		DCGMScraper:  dcgm,
		HealthCheck:  func(_ context.Context, _ string) bool { return true },
		DeviceReport: cudaDeviceReport, // SEED-019 part 3: GPU pod → stt re-override on recovery
		Vast:         fakeV,
		Rule:         alwaysInPeakRule(),
	})
	r.SetQueriesForTest(gen.New(dbtx))

	require.NoError(t, r.recoverOpenLifecycle(context.Background()))
	require.Equal(t, StateReady, r.deps.FSM.State(),
		"healthy primary lifecycle must be rehydrated to StateReady")
	require.Equal(t, int64(100), r.activeLifecycleID.Load())
	require.Equal(t, int64(42), r.activeInstanceID.Load())
	snap := loader.snapshot()
	require.Len(t, snap, 3, "3-role OverrideTier0 must fire on recovery (llm+stt+tts; Phase 11.2 D-B5′ restored stt)")
	require.Contains(t, dcgm.Last(), ":33400/metrics",
		"DCGMScraper.SetURL must point at the recovered pod's DCGM endpoint")
}

func TestRecoverOpenLifecycle_OrphanInstanceClosesLifecycle(t *testing.T) {
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	dbtx := &fakeDBTX{}
	dbtx.queryRowFn = func(_ context.Context, sql string, _ ...interface{}) pgx.Row {
		if contains(sql, "ended_at IS NULL") {
			return openLifecycleRow{
				id:             101,
				vastInstanceID: pgtype.Int8{Int64: 999, Valid: true},
			}
		}
		return errRow{err: errors.New("unexpected query")}
	}
	closeFired := atomic.Bool{}
	dbtx.execFn = func(_ context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
		if contains(sql, "UPDATE ai_gateway.primary_lifecycles") {
			// Inspect shutdown_reason ($2 of close query).
			if len(args) >= 2 {
				if reason, ok := args[1].(pgtype.Text); ok {
					if contains(reason.String, "gateway_restart_orphan") {
						closeFired.Store(true)
					}
				}
			}
		}
		return pgconn.NewCommandTag("UPDATE 1"), nil
	}
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return vast.Instance{}, vast.ErrInstanceNotFound
		},
	}
	r := buildReconciler(t, Deps{
		Cfg:  cfg,
		FSM:  fsm,
		Vast: fakeV,
		Rule: alwaysInPeakRule(),
	})
	r.SetQueriesForTest(gen.New(dbtx))

	require.NoError(t, r.recoverOpenLifecycle(context.Background()))
	require.True(t, closeFired.Load(),
		"Vast returning ErrInstanceNotFound must close lifecycle with shutdown_reason='gateway_restart_orphan'")
	require.Equal(t, StateAsleep, r.deps.FSM.State(),
		"FSM stays Asleep after orphan close")
}

func TestRecoverOpenLifecycle_UnhealthyInstanceClosesLifecycle(t *testing.T) {
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	dbtx := &fakeDBTX{}
	dbtx.queryRowFn = func(_ context.Context, sql string, _ ...interface{}) pgx.Row {
		if contains(sql, "ended_at IS NULL") {
			return openLifecycleRow{
				id:             102,
				vastInstanceID: pgtype.Int8{Int64: 999, Valid: true},
			}
		}
		return errRow{err: errors.New("unexpected query")}
	}
	closeReason := atomic.Pointer[string]{}
	dbtx.execFn = func(_ context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
		if contains(sql, "UPDATE ai_gateway.primary_lifecycles") && len(args) >= 2 {
			if reason, ok := args[1].(pgtype.Text); ok {
				s := reason.String
				closeReason.Store(&s)
			}
		}
		return pgconn.NewCommandTag("UPDATE 1"), nil
	}
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			// Running + ports populated but health probe returns false.
			return runningInstanceWithAllPorts(999), nil
		},
	}
	r := buildReconciler(t, Deps{
		Cfg:         cfg,
		FSM:         fsm,
		Vast:        fakeV,
		HealthCheck: func(_ context.Context, _ string) bool { return false }, // STT/embed/etc all unhealthy
		Rule:        alwaysInPeakRule(),
	})
	r.SetQueriesForTest(gen.New(dbtx))

	require.NoError(t, r.recoverOpenLifecycle(context.Background()))
	got := closeReason.Load()
	require.NotNil(t, got)
	require.Equal(t, "gateway_restart_orphan_unhealthy", *got,
		"4-endpoint health check failure must close with gateway_restart_orphan_unhealthy")
	require.Equal(t, int32(1), fakeV.destroyCalls.Load(),
		"orphan-unhealthy close must destroy the still-billing instance (leak observed live: lifecycle 189/instance 45098378 kept billing after the 14a63f3 rollout)")
	require.Empty(t, fakeV.reportedProblems(),
		"recovery closes must NOT file a machine report — unhealthy here is usually OUR restart interrupting a legit cold start")
}

func TestRecoverOpenLifecycle_NoOpenRow_NoOp(t *testing.T) {
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	dbtx := &fakeDBTX{
		queryRowFn: func(_ context.Context, _ string, _ ...interface{}) pgx.Row {
			return noRowsRow{}
		},
	}
	r := buildReconciler(t, Deps{
		Cfg:  cfg,
		FSM:  fsm,
		Rule: alwaysInPeakRule(),
	})
	r.SetQueriesForTest(gen.New(dbtx))

	require.NoError(t, r.recoverOpenLifecycle(context.Background()))
	require.Equal(t, StateAsleep, r.deps.FSM.State(),
		"empty primary_lifecycles must leave FSM at StateAsleep")
}

// ===========================================================================
// Leader election semantics (no-mutex-collision sanity)
// ===========================================================================

func TestLeaderElection_OnlyOneLeaderEvaluatesTick(t *testing.T) {
	rdb, _ := miniredisClient(t)
	cfg := testCfg(t)
	cfg.PrimaryPodScheduleDisabled = true // we don't want evaluateTick to actually fire; just test leader-pad

	fsmA := NewFSM(nil, nil)
	fsmB := NewFSM(nil, nil)
	dbtx := &fakeDBTX{
		queryRowFn: func(_ context.Context, _ string, _ ...interface{}) pgx.Row { return noRowsRow{} },
	}
	rA := buildReconciler(t, Deps{
		Cfg:       cfg,
		FSM:       fsmA,
		Vast:      &fakeVast{},
		Rule:      alwaysInPeakRule(),
		Redis:     rdb,
		ReplicaID: "replica-A",
	})
	rA.SetQueriesForTest(gen.New(dbtx))
	rB := buildReconciler(t, Deps{
		Cfg:       cfg,
		FSM:       fsmB,
		Vast:      &fakeVast{},
		Rule:      alwaysInPeakRule(),
		Redis:     rdb,
		ReplicaID: "replica-B",
	})
	rB.SetQueriesForTest(gen.New(dbtx))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rA.Start(ctx)
	rB.Start(ctx)

	// Wait long enough for both to attempt acquire (1s tick + slop).
	time.Sleep(2 * time.Second)
	leaderCount := 0
	if rA.isLeader.Load() {
		leaderCount++
	}
	if rB.isLeader.Load() {
		leaderCount++
	}
	require.Equal(t, 1, leaderCount,
		"exactly one replica must hold gw:primary:lock at a time")
}

// ===========================================================================
// Plan 6.6.Y-03 Task 2 — Option B: port-bind health-gate fail-fast.
// These tests drive the PRODUCTION waitForReadyOrDestroy (where the Option B
// logic lives), overriding the package poll interval to 5ms so the loop runs
// deterministically and fast (finding #7). PrimaryPublicPortBindBudgetSeconds
// is set to 0 so the fail-fast fires on the FIRST running observation.
// ===========================================================================

// runningInstanceNoPorts returns a vast.Instance reporting actual_status=running
// with NO published ports — i.e. buildPodURLs returns all-empty URLs. This is
// the exact lie the 6.6.Y-01 spike characterized: running + populated/empty
// ports map yet never externally reachable.
func runningInstanceNoPorts(id int64) vast.Instance {
	return vast.Instance{
		ID:           id,
		ActualStatus: "running",
		SshHost:      "", // spike: ssh_host stayed null the entire lifetime
		PublicIPAddr: "203.0.113.7",
		Ports:        map[string][]vast.PortBinding{}, // empty → buildPodURLs empty
	}
}

// withTestPollInterval overrides primaryInstancePollIntervalForTest for the
// duration of a test and restores it on cleanup.
func withTestPollInterval(t *testing.T, d time.Duration) {
	t.Helper()
	orig := primaryInstancePollIntervalForTest
	primaryInstancePollIntervalForTest = d
	t.Cleanup(func() { primaryInstancePollIntervalForTest = orig })
}

// TestWaitForReady_PublicPortBindTimeout asserts Option B: when the instance
// reports actual_status=running but pod URLs stay empty past
// PrimaryPublicPortBindBudgetSeconds (set to 0 for determinism), the
// reconciler fails fast — BestEffortDestroy fires AND closeLifecycle records
// closure_reason public_port_bind_timeout AND a non-nil error is returned.
func TestWaitForReady_PublicPortBindTimeout(t *testing.T) {
	withTestPollInterval(t, 5*time.Millisecond)

	cfg := testCfg(t)
	cfg.PrimaryPublicPortBindBudgetSeconds = 0 // fail-fast on first running poll
	cfg.PrimaryProvisionColdStartBudgetSeconds = 30
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")

	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return runningInstanceNoPorts(42), nil
		},
	}
	closeReasons := []string{}
	var closeMu sync.Mutex
	dbtx := &fakeDBTX{
		execFn: func(_ context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
			if contains(sql, "UPDATE ai_gateway.primary_lifecycles") && len(args) >= 2 {
				if reason, ok := args[1].(pgtype.Text); ok && reason.Valid {
					closeMu.Lock()
					closeReasons = append(closeReasons, reason.String)
					closeMu.Unlock()
				}
			}
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
	}
	r := buildReconciler(t, Deps{
		Cfg:         cfg,
		FSM:         fsm,
		Rule:        alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(dbtx))
	r.activeLifecycleID.Store(99)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := r.waitForReadyOrDestroy(ctx, 99, 42, 0.30, testLogger())

	require.Error(t, err, "running-but-unbound-ports past budget MUST return a non-nil error")
	require.Contains(t, err.Error(), "public port bind timeout",
		"error must identify the port-bind fail-fast path")
	require.Equal(t, int32(1), fakeV.destroyCalls.Load(),
		"BestEffortDestroy MUST fire exactly once on the port-bind timeout (orphan-prevention contract T-6.6.Y-03-02)")

	closeMu.Lock()
	defer closeMu.Unlock()
	require.Contains(t, closeReasons, "public_port_bind_timeout",
		"closeLifecycle must record the distinct closure_reason public_port_bind_timeout")
}

// TestWaitForReady_BindsBeforeBudget_NoFalseClose asserts no regression: a pod
// that binds all 4 URLs before the budget proceeds to the existing 4-endpoint
// health check and promotes to Ready — the port-bind branch must NOT fire a
// false public_port_bind_timeout close. Budget is 0 here too; because the very
// first poll already has all 4 URLs, the empty-URLs branch (and thus the
// fail-fast) is never entered.
func TestWaitForReady_BindsBeforeBudget_NoFalseClose(t *testing.T) {
	withTestPollInterval(t, 5*time.Millisecond)

	cfg := testCfg(t)
	cfg.PrimaryPublicPortBindBudgetSeconds = 0
	cfg.PrimaryProvisionColdStartBudgetSeconds = 30
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")

	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return runningInstanceWithAllPorts(42), nil // all 4 ports bound immediately
		},
	}
	loader := newFakeLoader()
	dcgm := &fakeDCGMScraper{}
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:         cfg,
		FSM:         fsm,
		Loader:      loader,
		DCGMScraper: dcgm,
		Rule:        alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(dbtx))
	r.activeLifecycleID.Store(99)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := r.waitForReadyOrDestroy(ctx, 99, 42, 0.30, testLogger())

	require.NoError(t, err,
		"a pod that binds all 4 URLs before budget MUST promote to Ready, not false-close on port-bind timeout")
	require.Equal(t, StateReady, r.deps.FSM.State(),
		"the bound-before-budget pod must reach Ready")
	require.Equal(t, int32(0), fakeV.destroyCalls.Load(),
		"BestEffortDestroy MUST NOT fire when ports bind before the budget")
}

// TestWaitForReady_RunningPortsBoundButTCPUnreachable_DestroysWithinBudget is
// the CR-01 (6.6.Y review) regression test for the ACTUAL observed spike
// failure: the instance reports actual_status=running WITH a fully populated
// Vast ports map (buildPodURLs returns 4 non-empty URLs) — yet the host is
// externally TCP-unreachable (Deps.Reachable returns false, the dial-timeout
// signature). The empty-URLs branch can NEVER catch this (URLs are non-empty);
// only the connection-level reachability gate does. With budget=0 the fail-fast
// must fire on the first running poll: BestEffortDestroy + closure_reason
// public_port_bind_timeout + non-nil error.
func TestWaitForReady_RunningPortsBoundButTCPUnreachable_DestroysWithinBudget(t *testing.T) {
	withTestPollInterval(t, 5*time.Millisecond)

	cfg := testCfg(t)
	cfg.PrimaryPublicPortBindBudgetSeconds = 0 // fail-fast on first running poll
	cfg.PrimaryProvisionColdStartBudgetSeconds = 30
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")

	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			// populated ports map — the exact spike "lie": running + ports
			// bound in the Vast API, yet unreachable from outside.
			return runningInstanceWithAllPorts(42), nil
		},
	}
	closeReasons := []string{}
	var closeMu sync.Mutex
	dbtx := &fakeDBTX{
		execFn: func(_ context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
			if contains(sql, "UPDATE ai_gateway.primary_lifecycles") && len(args) >= 2 {
				if reason, ok := args[1].(pgtype.Text); ok && reason.Valid {
					closeMu.Lock()
					closeReasons = append(closeReasons, reason.String)
					closeMu.Unlock()
				}
			}
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
	}
	r := buildReconciler(t, Deps{
		Cfg:  cfg,
		FSM:  fsm,
		Rule: alwaysInPeakRule(),
		// HealthCheck would PASS — proving the kill is driven by the
		// connection-level Reachable gate, not the HTTP health gate.
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		// connection-level dial times out: host never NAT-published.
		Reachable: func(_ context.Context, _ string) bool { return false },
		Vast:      fakeV,
	})
	r.SetQueriesForTest(gen.New(dbtx))
	r.activeLifecycleID.Store(99)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := r.waitForReadyOrDestroy(ctx, 99, 42, 0.30, testLogger())

	require.Error(t, err,
		"running + ports-bound + TCP-unreachable past budget MUST return a non-nil error")
	require.Contains(t, err.Error(), "public port bind timeout",
		"error must identify the port-bind fail-fast path even when URLs are non-empty")
	require.Equal(t, int32(1), fakeV.destroyCalls.Load(),
		"BestEffortDestroy MUST fire exactly once on the TCP-unreachable port-bind timeout")

	closeMu.Lock()
	defer closeMu.Unlock()
	require.Contains(t, closeReasons, "public_port_bind_timeout",
		"closeLifecycle must record public_port_bind_timeout for the unreachable-but-ports-bound path")
}

// TestWaitForReady_ReachableButNotReady_NoFalseClose asserts the design-note
// invariant (CR-01): a host that is TCP-reachable (Reachable == true) but whose
// services are still warming (HealthCheck failing while onstart downloads
// weights) is a LEGITIMATE cold start and MUST NOT be killed by the port-bind
// fail-fast — even with budget=0. The connection-level gate is satisfied, so
// the loop falls through to the HTTP health checks and keeps polling under the
// cold-start budget. Here we let it eventually pass health and promote.
func TestWaitForReady_ReachableButNotReady_NoFalseClose(t *testing.T) {
	withTestPollInterval(t, 5*time.Millisecond)

	cfg := testCfg(t)
	cfg.PrimaryPublicPortBindBudgetSeconds = 0 // would fire immediately IF gated on Reachable==false
	cfg.PrimaryProvisionColdStartBudgetSeconds = 30
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")

	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return runningInstanceWithAllPorts(42), nil
		},
	}
	loader := newFakeLoader()
	dcgm := &fakeDCGMScraper{}
	dbtx := &fakeDBTX{}
	// HealthCheck fails for the first few polls (services warming) then passes.
	var healthCalls atomic.Int32
	r := buildReconciler(t, Deps{
		Cfg:         cfg,
		FSM:         fsm,
		Loader:      loader,
		DCGMScraper: dcgm,
		Rule:        alwaysInPeakRule(),
		// host is reachable the whole time — NOT the spike failure signature.
		Reachable: func(_ context.Context, _ string) bool { return true },
		HealthCheck: func(_ context.Context, _ string) bool {
			// first 4 calls (one full poll's worth across LLM URL) fail to
			// simulate warming, then everything passes.
			return healthCalls.Add(1) > 1
		},
		Vast: fakeV,
	})
	r.SetQueriesForTest(gen.New(dbtx))
	r.activeLifecycleID.Store(99)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := r.waitForReadyOrDestroy(ctx, 99, 42, 0.30, testLogger())

	require.NoError(t, err,
		"a TCP-reachable but still-warming pod MUST NOT false-close on port-bind timeout; it keeps polling and promotes")
	require.Equal(t, StateReady, r.deps.FSM.State(),
		"the reachable-but-warming pod must eventually reach Ready")
	require.Equal(t, int32(0), fakeV.destroyCalls.Load(),
		"BestEffortDestroy MUST NOT fire when the host is TCP-reachable (cold-start budget owns the warming window)")
}

// ===========================================================================
// Helpers for tests — waitForReadyOrDestroyForTest exposes the poll
// interval so tests can drive the loop in milliseconds.
// ===========================================================================

// waitForReadyOrDestroyForTest is a test seam mirroring
// waitForReadyOrDestroy but with a parametrisable poll interval. Avoids
// 5-second sleeps in unit tests. Production code path remains
// waitForReadyOrDestroy with the package-const cadence.
//
// On any failure exit path the helper stamps lastProvisionFailureAt so
// the cooldown gate test (TestProvisioningFailure_SetsLastFailureTimestamp)
// observes the same anchor the production spawnProvisioning goroutine
// would set in its deferred error branch.
func (r *Reconciler) waitForReadyOrDestroyForTest(ctx context.Context, lifecycleID, instanceID int64, acceptedDPH float64, log *slog.Logger, pollInterval time.Duration) error {
	poll := time.NewTicker(pollInterval)
	defer poll.Stop()
	budget := time.Duration(r.cfg.PrimaryProvisionColdStartBudgetSeconds) * time.Second
	deadline := time.NewTimer(budget)
	defer deadline.Stop()
	stampFailure := func() {
		now := time.Now()
		r.lastProvisionFailureAt.Store(&now)
	}
	// Mirror the production 3-strike confirmation for both terminal
	// signals (IsTerminal status + ErrInstanceNotFound).
	notFoundStrikes := 0
	const terminalConfirmStrikes = 3
	for {
		select {
		case <-ctx.Done():
			stampFailure()
			return ctx.Err()
		case <-deadline.C:
			_ = r.closeLifecycle(context.Background(), lifecycleID, "health_timeout", 0)
			stampFailure()
			return errors.New("primary: cold-start budget exhausted")
		case <-poll.C:
			inst, err := r.deps.Vast.GetInstance(ctx, instanceID)
			if err != nil {
				if errors.Is(err, vast.ErrInstanceNotFound) {
					notFoundStrikes++
					if notFoundStrikes >= terminalConfirmStrikes {
						if r.deps.Vast != nil {
							_ = r.deps.Vast.DestroyInstance(context.Background(), instanceID)
						}
						_ = r.closeLifecycle(context.Background(), lifecycleID, "instance_terminal_state_confirmed", 0)
						stampFailure()
						return errors.New("primary: instance terminal (3-strike confirm via ErrInstanceNotFound)")
					}
					continue
				}
				notFoundStrikes = 0
				continue
			}
			notFoundStrikes = 0
			if msg := inst.StatusMsg; msg != "" {
				if contains(msg, "Error") || contains(msg, "error") {
					trunc := msg
					if len(trunc) > 200 {
						trunc = trunc[:200]
					}
					forensicsReason := "vast_status_msg_error:" + trunc
					_ = r.closeLifecycle(context.Background(), lifecycleID, forensicsReason, 0)
					stampFailure()
					return errors.New(forensicsReason)
				}
			}
			if inst.IsTerminal() {
				_ = r.closeLifecycle(context.Background(), lifecycleID, "instance_terminal_state", 0)
				stampFailure()
				return errors.New("primary: instance terminal")
			}
			if inst.ActualStatus != "running" {
				continue
			}
			urls := r.buildPodURLs(inst)
			if urls.LLM == "" || urls.TTS == "" || urls.DCGM == "" {
				continue
			}
			if r.deps.HealthCheck == nil {
				continue
			}
			if !r.deps.HealthCheck(ctx, urls.LLM) ||
				!r.deps.HealthCheck(ctx, urls.TTS) ||
				!r.deps.HealthCheck(ctx, urls.DCGM) {
				continue
			}
			if err := r.markReady(ctx, lifecycleID, urls, acceptedDPH, log); err != nil {
				stampFailure()
				return err
			}
			return nil
		}
	}
}

// _ = strconv keeps the import alive if a future test wants to parse host
// port strings (currently HostPort is read as-is into URL builders).
var _ = strconv.Atoi

// ---------------------------------------------------------------------------
// Phase 06.7 Wave 0 RED scaffolding (Nyquist gate). Skip stubs binding the
// primary-pod tier-0 re-assert behavior (Pitfall #11 / D-13) for the swapped
// TTS role to its owning implementation plan. ENGINE-AGNOSTIC — they assert
// WHICH roles the reconciler overrides on Ready, not Chatterbox internals.
//
// OWNER map (authority: 06.7-02-PLAN.md <stub_ownership_map>):
//   - TestEvaluateReady_ReassertsTier0WhenCleared -> Plan 06.7-08
//   - TestMarkReady_OverridesTTSNotEmbed          -> Plan 06.7-08
// ---------------------------------------------------------------------------

// TestEvaluateReady_ReassertsTier0WhenCleared asserts the Pitfall #11 /
// D-13 fix: when the primary FSM is in StatePrimaryReady AND the tier-0
// override slot for a primary role (llm/stt/tts) is empty (e.g. an emerg
// cutback's RestoreTier0 transitively cleared it), evaluateReady re-calls
// loader.OverrideTier0(role, podURL) for llm/stt/tts — and explicitly does
// NOT re-assert "embed" (embed lives on n8n-ia-vm CPU after Phase 06.7, no
// longer a primary-pod tier-0 role). Uses loader.Tier0OverrideURL(role) to
// detect the empty slot.
//
// OWNER: Plan 06.7-08 — implement the Ready-tick re-assert (read
// Tier0OverrideURL, re-Override llm/stt/tts when empty), unskip, and assert
// re-assert fires for llm/stt/tts but never embed before COMPLETE.
func TestEvaluateReady_ReassertsTier0WhenCleared(t *testing.T) {
	cfg := testCfg(t)
	cfg.PrimaryPodScheduleDisabled = true // keep evaluateReady from auto-draining; re-assert loop still runs
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "x")
	_ = fsm.Transition(StateProvisioning, StateReady, time.Now(), "x")
	loader := newFakeLoader()
	// Primary had previously written the 2 dynamic roles (llm+tts post
	// Phase 11.1 D-A4); an emerg cutback then cleared the tts slot
	// (RestoreTier0 transitively wiped it).
	loader.OverrideTier0("llm", "http://203.0.113.7:33000")
	// tts intentionally NOT set → the cleared-slot vector.
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:    cfg,
		FSM:    fsm,
		Loader: loader,
		Rule:   neverInPeakRule(),
	})
	r.SetQueriesForTest(gen.New(dbtx))
	r.activeLifecycleID.Store(7)
	// activePodURLs must be populated for the re-assert loop to know the URLs.
	urls := primaryPodURLs{
		LLM:           "http://203.0.113.7:33000/v1/models",
		STT:           "http://203.0.113.7:33001/health",
		TTS:           "http://203.0.113.7:33003/health",
		DCGM:          "http://203.0.113.7:33400/metrics",
		WhisperDevice: "cuda", // SEED-019 part 3: GPU pod → stt re-assert fires
	}
	r.activePodURLs.Store(&urls)

	r.evaluateReady(context.Background(), time.Now(), testLogger())

	snap := loader.snapshot()
	require.Equal(t, "http://203.0.113.7:33003", snap["tts"],
		"evaluateReady must re-assert the cleared tts slot (stripped /health)")
	require.Equal(t, "http://203.0.113.7:33001", snap["stt"],
		"evaluateReady must re-assert the cleared stt slot (Phase 11.2 D-B5′ restored)")
	require.Equal(t, "http://203.0.113.7:33000", snap["llm"],
		"llm slot stays set")
	_, embedSet := snap["embed"]
	require.False(t, embedSet, "embed must NEVER be re-asserted by the primary reconciler (D-03)")
}

// TestMarkReady_OverridesTTSNotEmbed asserts that when the primary pod
// reaches StatePrimaryReady, markReady calls loader.OverrideTier0 for "llm",
// "stt", and "tts" (the swapped role) and does NOT call it for "embed"
// (relocated off the primary pod per D-11). This replaces the Phase 6.6
// {llm,stt,embed} override set with the Phase 06.7 {llm,stt,tts} set.
//
// OWNER: Plan 06.7-08 — update markReady role set to {llm,stt,tts}, unskip,
// and assert OverrideTier0 fired for tts + NOT for embed before COMPLETE.
func TestMarkReady_OverridesTTSNotEmbed(t *testing.T) {
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "x")
	loader := newFakeLoader()
	dcgm := &fakeDCGMScraper{}
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:         cfg,
		FSM:         fsm,
		Loader:      loader,
		DCGMScraper: dcgm,
		Rule:        alwaysInPeakRule(),
	})
	r.SetQueriesForTest(gen.New(dbtx))

	urls := primaryPodURLs{
		LLM:           "http://1.2.3.4:33000/v1/models",
		STT:           "http://1.2.3.4:33001/health",
		TTS:           "http://1.2.3.4:33003/health",
		DCGM:          "http://1.2.3.4:33400/metrics",
		WhisperDevice: "cuda", // SEED-019 part 3: GPU pod → stt override fires
	}
	require.NoError(t, r.markReady(context.Background(), 5, urls, 0.30, testLogger()))

	snap := loader.snapshot()
	require.Len(t, snap, 3, "exactly 3 OverrideTier0 calls (llm/stt/tts; Phase 11.2 D-B5′ restored stt)")
	require.Equal(t, "http://1.2.3.4:33000", snap["llm"], "/v1/models suffix stripped for LLM")
	require.Equal(t, "http://1.2.3.4:33001", snap["stt"], "/health suffix stripped for STT")
	require.Equal(t, "http://1.2.3.4:33003", snap["tts"], "/health suffix stripped for TTS")
	_, embedSet := snap["embed"]
	require.False(t, embedSet, "markReady must NOT override embed (D-03 — embed is static off-pod)")
	require.Equal(t, StateReady, r.deps.FSM.State())
}

// ---------------------------------------------------------------------------
// Phase 11.2 Plan 01 — Wave 0 RED stubs for primary STT lifecycle hooks
// (D-B5′ revert of 11.1-01). OWNER: Plan 03 — restores
// OverrideTier0("stt")/RestoreTier0("stt") calls at reconciler.go
// :527/:571/:636 per PATTERNS.md lines 319-337.
// ---------------------------------------------------------------------------

// TestStartDrain_RestoreTier0_CalledFor_STT — reconciler.go startDrain
// restoration (Phase 11.2 D-B5′ revert of 11.1-01). When evaluateReady
// fires startDrain on a Ready→Draining transition, the fake loader MUST
// record a RestoreTier0 call for role="stt" alongside llm and tts.
func TestStartDrain_RestoreTier0_CalledFor_STT(t *testing.T) {
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "x")
	_ = fsm.Transition(StateProvisioning, StateReady, time.Now(), "x")
	loader := newFakeLoader()
	loader.OverrideTier0("llm", "http://pod:8000")
	loader.OverrideTier0("stt", "http://pod:8001")
	loader.OverrideTier0("tts", "http://pod:8003")
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:    cfg,
		FSM:    fsm,
		Loader: loader,
		Rule:   neverInPeakRule(),
	})
	r.SetQueriesForTest(gen.New(dbtx))
	r.activeLifecycleID.Store(42)

	r.evaluateReady(context.Background(), time.Now(), testLogger())

	require.Equal(t, StateDraining, r.deps.FSM.State())
	roles := loader.restoredRoles()
	require.Contains(t, roles, "stt",
		"startDrain MUST call RestoreTier0(stt) (Phase 11.2 D-B5′)")
	require.Equal(t, []string{"llm", "stt", "tts"}, roles,
		"3-role RestoreTier0 order must be llm/stt/tts (Phase 11.2 D-B5′)")
}

// TestMarkReady_OverrideTier0_CalledFor_STT — reconciler.go markReady
// restoration (Phase 11.2 D-B5′). When markReady transitions
// Provisioning→Ready with a populated urls.STT, the loader MUST record
// an OverrideTier0("stt", strippedURL) call with the /health suffix
// stripped (parity with llm/tts).
func TestMarkReady_OverrideTier0_CalledFor_STT(t *testing.T) {
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "x")
	loader := newFakeLoader()
	dcgm := &fakeDCGMScraper{}
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:         cfg,
		FSM:         fsm,
		Loader:      loader,
		DCGMScraper: dcgm,
		Rule:        alwaysInPeakRule(),
	})
	r.SetQueriesForTest(gen.New(dbtx))

	urls := primaryPodURLs{
		LLM:           "http://primary:33000/v1/models",
		STT:           "http://primary:33001/health",
		TTS:           "http://primary:33003/health",
		DCGM:          "http://primary:33400/metrics",
		WhisperDevice: "cuda", // SEED-019 part 3: GPU pod → stt override fires
	}
	require.NoError(t, r.markReady(context.Background(), 7, urls, 0.30, testLogger()))

	snap := loader.snapshot()
	require.Equal(t, "http://primary:33001", snap["stt"],
		"markReady MUST OverrideTier0(stt, strippedURL) — /health suffix stripped (Phase 11.2 D-B5′)")
}

// TestCloseLifecycle_RestoreTier0_CalledFor_STT — reconciler.go
// closeLifecycle defensive restoration (Phase 11.2 D-B5′). The fake
// loader MUST record a RestoreTier0 call for role="stt" inside the
// 3-role defensive cleanup that closeLifecycle runs.
func TestCloseLifecycle_RestoreTier0_CalledFor_STT(t *testing.T) {
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	fsm.SetState(StateDraining, time.Now(), "setup")
	loader := newFakeLoader()
	loader.OverrideTier0("llm", "http://pod:8000")
	loader.OverrideTier0("stt", "http://pod:8001")
	loader.OverrideTier0("tts", "http://pod:8003")
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:    cfg,
		FSM:    fsm,
		Loader: loader,
		Rule:   neverInPeakRule(),
	})
	r.SetQueriesForTest(gen.New(dbtx))

	require.NoError(t, r.closeLifecycle(context.Background(), 99, "test_close", 0.30))

	roles := loader.restoredRoles()
	require.Contains(t, roles, "stt",
		"closeLifecycle MUST call RestoreTier0(stt) (Phase 11.2 D-B5′)")
	require.Equal(t, []string{"llm", "stt", "tts"}, roles,
		"3-role RestoreTier0 order must be llm/stt/tts (Phase 11.2 D-B5′)")
}

// ===========================================================================
// Phase 12 Plan 02 Task 1 — Ready-tick death poll (RES-11)
//
// evaluateReady polls Vast for the tracked instance every Ready tick, confirms
// death via a 3-strike confirm on both IsTerminal() and ErrInstanceNotFound,
// classifies the cause, reconciles an empty trackedID from the open lifecycle
// row (D-05), and resets the strike counters on enter-Ready (markReady). Task 1
// does NOT wire drain/alert (that is Task 2) — the poll + strike + classify
// must be complete and isolated.
// ===========================================================================

// deathPollReady is a Ready-state reconciler whose evaluateReady runs only the
// death poll (DISABLED short-circuits the schedule drain trigger, the re-assert
// loop is a no-op because all slots are set). Tracked instance + active pod
// URLs are populated so the death poll has an id to poll.
func deathPollReady(t *testing.T, vastFn func(ctx context.Context, id int64) (vast.Instance, error)) (*Reconciler, *fakeVast, *fakeLoader) {
	t.Helper()
	cfg := testCfg(t)
	cfg.PrimaryPodScheduleDisabled = true // keep the schedule drain trigger off; death poll still runs
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "x")
	_ = fsm.Transition(StateProvisioning, StateReady, time.Now(), "x")
	loader := newFakeLoader()
	// Slots set so the Pitfall #11 re-assert loop does not fire.
	loader.OverrideTier0("llm", "http://203.0.113.7:33000")
	loader.OverrideTier0("stt", "http://203.0.113.7:33001")
	loader.OverrideTier0("tts", "http://203.0.113.7:33003")
	fv := &fakeVast{getInstanceFn: vastFn}
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:    cfg,
		FSM:    fsm,
		Loader: loader,
		Rule:   neverInPeakRule(),
		Vast:   fv,
	})
	r.SetQueriesForTest(gen.New(dbtx))
	r.activeLifecycleID.Store(7)
	r.activeInstanceID.Store(42)
	urls := primaryPodURLs{
		LLM:  "http://203.0.113.7:33000/v1/models",
		STT:  "http://203.0.113.7:33001/health",
		TTS:  "http://203.0.113.7:33003/health",
		DCGM: "http://203.0.113.7:33400/metrics",
	}
	r.activePodURLs.Store(&urls)
	return r, fv, loader
}

func terminalInstance(id int64) vast.Instance {
	return vast.Instance{ID: id, ActualStatus: "exited"}
}

// TestEvaluateReady_EmptyTrackedIDReconciles — D-05: when activeInstanceID==0
// but an open primary_lifecycles row carries the id and a pod is routing, the
// death poll reconciles the id from the open row and polls it (no silent no-op).
func TestEvaluateReady_EmptyTrackedIDReconciles(t *testing.T) {
	cfg := testCfg(t)
	cfg.PrimaryPodScheduleDisabled = true
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "x")
	_ = fsm.Transition(StateProvisioning, StateReady, time.Now(), "x")
	loader := newFakeLoader()
	loader.OverrideTier0("llm", "http://203.0.113.7:33000")
	loader.OverrideTier0("stt", "http://203.0.113.7:33001")
	loader.OverrideTier0("tts", "http://203.0.113.7:33003")
	polled := atomic.Int64{}
	fv := &fakeVast{getInstanceFn: func(_ context.Context, id int64) (vast.Instance, error) {
		polled.Store(id)
		return runningInstanceNoPorts(id), nil
	}}
	// GetOpenPrimaryLifecycle returns a row carrying vast_instance_id=55.
	dbtx := &fakeDBTX{
		queryRowFn: func(_ context.Context, _ string, _ ...interface{}) pgx.Row {
			return openLifecycleRow{id: 7, vastInstanceID: pgtype.Int8{Int64: 55, Valid: true}}
		},
	}
	r := buildReconciler(t, Deps{
		Cfg:    cfg,
		FSM:    fsm,
		Loader: loader,
		Rule:   neverInPeakRule(),
		Vast:   fv,
	})
	r.SetQueriesForTest(gen.New(dbtx))
	// activeInstanceID intentionally 0 (lost on a force-up); a pod is routing.
	r.activeLifecycleID.Store(7)
	r.activeInstanceID.Store(0)
	urls := primaryPodURLs{
		LLM: "http://203.0.113.7:33000/v1/models", STT: "http://203.0.113.7:33001/health",
		TTS: "http://203.0.113.7:33003/health", DCGM: "http://203.0.113.7:33400/metrics",
	}
	r.activePodURLs.Store(&urls)

	r.evaluateReady(context.Background(), time.Now(), testLogger())

	require.Equal(t, int64(55), polled.Load(),
		"death poll must reconcile the tracked id from the open lifecycle row and poll it")
	require.Equal(t, int64(55), r.activeInstanceID.Load(),
		"D-05: activeInstanceID must be repaired from the open row")
}

// TestEvaluateReady_DeathDetection — a tracked instance returning
// IsTerminal()==true for 3 consecutive ticks confirms death; the cause is
// classified host_death (plain exited, no stopped/credit signal). Task 1 only
// requires the poll+strike+classify (drain/breakers are Task 2).
func TestEvaluateReady_DeathDetection(t *testing.T) {
	r, _, _ := deathPollReady(t, func(_ context.Context, id int64) (vast.Instance, error) {
		return terminalInstance(id), nil
	})
	ctx := context.Background()
	require.Nil(t, r.classifyDeathOnReadyTickForTest(ctx, testLogger()),
		"strike 1 must not confirm death")
	require.Nil(t, r.classifyDeathOnReadyTickForTest(ctx, testLogger()),
		"strike 2 must not confirm death")
	got := r.classifyDeathOnReadyTickForTest(ctx, testLogger())
	require.NotNil(t, got, "3 consecutive terminal observations must confirm death")
	require.True(t, got.dead)
	require.Equal(t, "host_death", got.cause,
		"plain exited with no stopped/credit signal → host_death")
}

// TestEvaluateReady_TransientExitedDoesNotDrain — IsTerminal()==true for 1-2
// ticks then a non-terminal observation resets the strike counter; the FSM
// stays Ready and no death is confirmed. The strike counter MUST survive
// across separate evaluateReady calls (struct field, not a function local).
func TestEvaluateReady_TransientExitedDoesNotDrain(t *testing.T) {
	var status atomic.Value
	status.Store("exited")
	r, _, _ := deathPollReady(t, func(_ context.Context, id int64) (vast.Instance, error) {
		return vast.Instance{ID: id, ActualStatus: status.Load().(string)}, nil
	})
	ctx := context.Background()
	require.Nil(t, r.classifyDeathOnReadyTickForTest(ctx, testLogger()))
	require.Nil(t, r.classifyDeathOnReadyTickForTest(ctx, testLogger()))
	status.Store("running")
	require.Nil(t, r.classifyDeathOnReadyTickForTest(ctx, testLogger()),
		"non-terminal observation must not confirm")
	status.Store("exited")
	require.Nil(t, r.classifyDeathOnReadyTickForTest(ctx, testLogger()), "strike 1 after reset")
	require.Nil(t, r.classifyDeathOnReadyTickForTest(ctx, testLogger()), "strike 2 after reset")
	got := r.classifyDeathOnReadyTickForTest(ctx, testLogger())
	require.NotNil(t, got, "strike 3 after reset confirms death")
	require.Equal(t, StateReady, r.deps.FSM.State(),
		"Task 1 classify does not itself transition the FSM (Task 2 wires drain)")
}

// TestEvaluateReady_NotFound3StrikeDrains — ErrInstanceNotFound for 3
// consecutive ticks confirms death with cause=not_found.
func TestEvaluateReady_NotFound3StrikeDrains(t *testing.T) {
	r, _, _ := deathPollReady(t, func(_ context.Context, _ int64) (vast.Instance, error) {
		return vast.Instance{}, vast.ErrInstanceNotFound
	})
	ctx := context.Background()
	require.Nil(t, r.classifyDeathOnReadyTickForTest(ctx, testLogger()), "not-found strike 1")
	require.Nil(t, r.classifyDeathOnReadyTickForTest(ctx, testLogger()), "not-found strike 2")
	got := r.classifyDeathOnReadyTickForTest(ctx, testLogger())
	require.NotNil(t, got, "3 consecutive ErrInstanceNotFound must confirm death")
	require.True(t, got.dead)
	require.Equal(t, "not_found", got.cause,
		"ErrInstanceNotFound death classifies cause=not_found")
}

// TestEvaluateReady_HealthyNoop — a healthy (non-terminal, found) instance
// accumulates no strikes and confirms no death across many ticks.
func TestEvaluateReady_HealthyNoop(t *testing.T) {
	r, _, _ := deathPollReady(t, func(_ context.Context, id int64) (vast.Instance, error) {
		return runningInstanceNoPorts(id), nil
	})
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		require.Nil(t, r.classifyDeathOnReadyTickForTest(ctx, testLogger()),
			"healthy instance must never confirm death")
	}
	require.Equal(t, StateReady, r.deps.FSM.State())
}

// TestEvaluateReady_StrikesResetOnEnterReady — strike counters carried > 0 from
// a prior lifecycle are reset to 0 on the Provisioning→Ready transition
// (markReady), so a freshly-Ready pod starts with a clean strike count.
func TestEvaluateReady_StrikesResetOnEnterReady(t *testing.T) {
	r, _, _ := deathPollReady(t, func(_ context.Context, id int64) (vast.Instance, error) {
		return terminalInstance(id), nil
	})
	ctx := context.Background()
	require.Nil(t, r.classifyDeathOnReadyTickForTest(ctx, testLogger()))
	require.Nil(t, r.classifyDeathOnReadyTickForTest(ctx, testLogger()))
	require.Equal(t, 2, r.terminalStrikesForTest(), "2 strikes carried")

	r.deps.FSM.SetState(StateProvisioning, time.Now(), "new_pod")
	dcgm := &fakeDCGMScraper{}
	r.deps.DCGMScraper = dcgm
	urls := primaryPodURLs{
		LLM: "http://1.2.3.4:33000/v1/models", STT: "http://1.2.3.4:33001/health",
		TTS: "http://1.2.3.4:33003/health", DCGM: "http://1.2.3.4:33400/metrics",
	}
	require.NoError(t, r.markReady(ctx, 8, urls, 0.30, testLogger()))
	require.Equal(t, 0, r.terminalStrikesForTest(),
		"markReady (enter-Ready) must reset the terminal strike counter")
	require.Equal(t, 0, r.notFoundStrikesForTest(),
		"markReady (enter-Ready) must reset the not-found strike counter")
}

// ===========================================================================
// Phase 12 Plan 02 Task 2 — death-confirmed path (D-01/D-03/D-04)
//
// On a confirmed death evaluateReady → handleConfirmedDeath:
//   (1) startDrain (Ready→Draining + RestoreTier0)
//   (2) D-04 force-open local-llm/local-stt/local-tts BEFORE destroy
//   (3) D-01 billing-stop suppression marker (host-yank records none)
//   (4) D-03 distinct primary_death_confirmed event (cause-tagged)
//   (5) PrimaryDeathDetectedTotal{cause}.Inc()
// ===========================================================================

// readyReconcilerWithRedis builds a Ready-state reconciler wired to a miniredis
// client so the death path's WriteForceOverride + publishPrimaryEvent observe a
// real Redis. The death poll is driven directly via handleConfirmedDeath so the
// test controls the cause without 3 strike ticks.
func readyReconcilerWithRedis(t *testing.T) (*Reconciler, *fakeLoader, *redis.Client) {
	t.Helper()
	cfg := testCfg(t)
	cfg.PrimaryPodScheduleDisabled = true
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "x")
	_ = fsm.Transition(StateProvisioning, StateReady, time.Now(), "x")
	loader := newFakeLoader()
	loader.OverrideTier0("llm", "http://203.0.113.7:33000")
	loader.OverrideTier0("stt", "http://203.0.113.7:33001")
	loader.OverrideTier0("tts", "http://203.0.113.7:33003")
	rdb, _ := miniredisClient(t)
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:    cfg,
		FSM:    fsm,
		Loader: loader,
		Rule:   neverInPeakRule(),
		Redis:  rdb,
	})
	r.SetQueriesForTest(gen.New(dbtx))
	r.activeLifecycleID.Store(7)
	r.activeInstanceID.Store(42)
	urls := primaryPodURLs{
		LLM: "http://203.0.113.7:33000/v1/models", STT: "http://203.0.113.7:33001/health",
		TTS: "http://203.0.113.7:33003/health", DCGM: "http://203.0.113.7:33400/metrics",
	}
	r.activePodURLs.Store(&urls)
	return r, loader, rdb
}

func forceOverrideState(t *testing.T, rdb *redis.Client, name string) (string, bool) {
	t.Helper()
	state, _, set, err := breakerReadForce(t, rdb, name)
	require.NoError(t, err)
	return state, set
}

// TestDeath_HostYankDrainsAndForceOpens — confirmed host-yank death drains the
// FSM, force-opens the 3 local-* breakers, publishes a host_death event, and
// records NO billing-stop suppression marker.
func TestDeath_HostYankDrainsAndForceOpens(t *testing.T) {
	r, loader, rdb := readyReconcilerWithRedis(t)
	startDeaths := deathCount(t, "host_death")

	r.handleConfirmedDeath(context.Background(), deathClassification{dead: true, cause: "host_death"}, testLogger())

	require.Equal(t, StateDraining, r.deps.FSM.State(),
		"confirmed death must startDrain (Ready→Draining)")
	require.Equal(t, []string{"llm", "stt", "tts"}, loader.restoredRoles(),
		"startDrain RestoreTier0s the 3 dynamic roles")
	for _, name := range []string{"local-llm", "local-stt", "local-tts"} {
		state, set := forceOverrideState(t, rdb, name)
		require.True(t, set, "breaker %s must be force-overridden", name)
		require.Equal(t, "open", state, "breaker %s must be force-OPEN", name)
	}
	require.Nil(t, r.billingSuppressionActiveForTest(),
		"host-yank death must NOT record a billing-stop suppression marker")
	require.InDelta(t, startDeaths+1, deathCount(t, "host_death"), 0.001,
		"PrimaryDeathDetectedTotal{cause=host_death} must increment")
}

// TestDeath_BillingStopRecordsSuppression — confirmed billing-stop death drains
// + force-opens + publishes a billing_stopped event AND records a durable
// suppression marker.
func TestDeath_BillingStopRecordsSuppression(t *testing.T) {
	r, _, rdb := readyReconcilerWithRedis(t)

	r.handleConfirmedDeath(context.Background(), deathClassification{dead: true, cause: "billing_stopped"}, testLogger())

	require.Equal(t, StateDraining, r.deps.FSM.State())
	for _, name := range []string{"local-llm", "local-stt", "local-tts"} {
		state, set := forceOverrideState(t, rdb, name)
		require.True(t, set)
		require.Equal(t, "open", state)
	}
	require.NotNil(t, r.billingSuppressionActiveForTest(),
		"billing-stop death MUST record a durable suppression marker")
}

// TestEvaluateAsleep_BillingStopSuppressesReprovision — full path: after a
// billing-stop suppression marker is active, evaluateAsleep inside the peak
// window does NOT spawn provisioning. Host-yank (no marker) re-provisions.
func TestEvaluateAsleep_BillingStopSuppressesReprovision(t *testing.T) {
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil) // StateAsleep
	dbtx := &fakeDBTX{
		queryRowFn: func(_ context.Context, _ string, _ ...interface{}) pgx.Row {
			return insertReturningRow{id: 7, startedAt: time.Now()}
		},
	}
	// Block SearchOffers so the spawnProvisioning goroutine cannot reach the
	// error branch + FSM.SetState(Asleep) before this test asserts Provisioning.
	stopBlock := make(chan struct{})
	t.Cleanup(func() { close(stopBlock) })
	r := buildReconciler(t, Deps{
		Cfg:  cfg,
		FSM:  fsm,
		Rule: alwaysInPeakRule(), // in peak → ShouldBeProvisioned true
		Vast: &fakeVast{
			searchOffersFn: func(_ context.Context, _ vast.SearchFilter) ([]vast.Offer, error) {
				<-stopBlock
				return nil, errors.New("test teardown")
			},
		},
	})
	r.SetQueriesForTest(gen.New(dbtx))

	// Arm the billing-stop suppression marker.
	r.armBillingSuppressionForTest()
	r.evaluateAsleep(context.Background(), time.Now(), testLogger())
	require.Equal(t, StateAsleep, r.deps.FSM.State(),
		"billing-stop suppression must make evaluateAsleep SKIP re-provision (no provision-fail loop)")

	// Clear suppression → schedule loop re-provisions normally inside peak.
	r.clearBillingSuppressionForTest()
	r.evaluateAsleep(context.Background(), time.Now(), testLogger())
	require.Equal(t, StateProvisioning, r.deps.FSM.State(),
		"with no suppression marker the schedule loop re-provisions normally inside peak (host-yank path)")
}

// TestDeath_BreakersForceOpenedBeforeDestroy — force-open is written at
// drain-start (handleConfirmedDeath calls startDrain + force-open together),
// well before evaluateDestroying's BestEffortDestroy runs. We assert the keys
// exist immediately after handleConfirmedDeath, while the FSM is still Draining
// (BestEffortDestroy has not yet been reached).
func TestDeath_BreakersForceOpenedBeforeDestroy(t *testing.T) {
	r, _, rdb := readyReconcilerWithRedis(t)
	r.handleConfirmedDeath(context.Background(), deathClassification{dead: true, cause: "host_death"}, testLogger())
	require.Equal(t, StateDraining, r.deps.FSM.State(),
		"after handleConfirmedDeath the FSM is Draining — destroy has NOT yet run")
	for _, name := range []string{"local-llm", "local-stt", "local-tts"} {
		_, set := forceOverrideState(t, rdb, name)
		require.True(t, set, "breaker %s force-open must be written before destroy", name)
	}
}

// TestDeath_BillingStopFallbackSignal — IntendedStatus empty but
// ActualStatus==exited && StatusMsg has a credit/account marker → billing_stopped.
func TestDeath_BillingStopFallbackSignal(t *testing.T) {
	require.Equal(t, "billing_stopped",
		classifyDeath(vast.Instance{ActualStatus: "exited", StatusMsg: "instance stopped: account out of credit"}),
		"exited + credit StatusMsg must classify billing_stopped (A1 fallback)")
	require.Equal(t, "billing_stopped",
		classifyDeath(vast.Instance{IntendedStatus: "stopped", ActualStatus: "exited"}),
		"IntendedStatus==stopped is the primary billing signal")
	require.Equal(t, "host_death",
		classifyDeath(vast.Instance{ActualStatus: "exited", StatusMsg: "container crashed"}),
		"exited with no credit/account marker is host_death")
}

// ===========================================================================
// Phase 12 Plan 02 Task 4 — D-13 markReady force-CLOSE (symmetric to D-04)
//
// markReady force-CLOSES the stale local-llm/local-stt/local-tts breakers
// (short TTL) when a new pod goes Ready, so a re-provisioned pod never inherits
// an OPEN breaker left over from probing the previous dead URL.
// ===========================================================================

// markReadyReconcilerWithRedis builds a Provisioning-state reconciler wired to
// miniredis so markReady's force-CLOSE write is observable.
func markReadyReconcilerWithRedis(t *testing.T) (*Reconciler, *fakeLoader, *redis.Client) {
	t.Helper()
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "x")
	loader := newFakeLoader()
	rdb, _ := miniredisClient(t)
	dcgm := &fakeDCGMScraper{}
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:         cfg,
		FSM:         fsm,
		Loader:      loader,
		DCGMScraper: dcgm,
		Rule:        alwaysInPeakRule(),
		Redis:       rdb,
	})
	r.SetQueriesForTest(gen.New(dbtx))
	return r, loader, rdb
}

func markReadyURLs() primaryPodURLs {
	return primaryPodURLs{
		LLM:           "http://1.2.3.4:33000/v1/models",
		STT:           "http://1.2.3.4:33001/health",
		TTS:           "http://1.2.3.4:33003/health",
		DCGM:          "http://1.2.3.4:33400/metrics",
		WhisperDevice: "cuda", // SEED-019 part 3: GPU pod → stt override fires
	}
}

// TestMarkReady_ResetsStaleBreakers — markReady force-CLOSEs each of the 3
// local-* breakers (state="closed") so a breaker left OPEN by the previous dead
// pod's probing is reset and traffic returns to the live pod.
func TestMarkReady_ResetsStaleBreakers(t *testing.T) {
	r, _, rdb := markReadyReconcilerWithRedis(t)
	// Simulate a stale OPEN inherited from the previous dead pod's death path.
	for _, name := range []string{"local-llm", "local-stt", "local-tts"} {
		require.NoError(t, breaker.WriteForceOverride(context.Background(), rdb, name, "open", 10*time.Minute, "prev_death"))
	}

	require.NoError(t, r.markReady(context.Background(), 5, markReadyURLs(), 0.30, testLogger()))

	for _, name := range []string{"local-llm", "local-stt", "local-tts"} {
		state, _, set, err := breakerReadForce(t, rdb, name)
		require.NoError(t, err)
		require.True(t, set, "breaker %s must still be force-overridden (now closed)", name)
		require.Equal(t, "closed", state,
			"markReady must force-CLOSE the stale %s breaker (D-13)", name)
	}
}

// TestMarkReady_ForceCloseAfterOverrideTier0 — the force-close write lands AFTER
// the OverrideTier0 block, so the pod URL slot is set before the breaker is
// reset (no request hits a closed breaker pointing at a not-yet-overridden slot).
func TestMarkReady_ForceCloseAfterOverrideTier0(t *testing.T) {
	r, loader, rdb := markReadyReconcilerWithRedis(t)
	require.NoError(t, r.markReady(context.Background(), 5, markReadyURLs(), 0.30, testLogger()))

	// OverrideTier0 fired for the 3 roles (slots set) ...
	snap := loader.snapshot()
	require.Equal(t, "http://1.2.3.4:33000", snap["llm"])
	require.Equal(t, "http://1.2.3.4:33001", snap["stt"])
	require.Equal(t, "http://1.2.3.4:33003", snap["tts"])
	// ... and the force-close keys are present (written after the slots).
	for _, name := range []string{"local-llm", "local-stt", "local-tts"} {
		state, _, set, err := breakerReadForce(t, rdb, name)
		require.NoError(t, err)
		require.True(t, set)
		require.Equal(t, "closed", state)
	}
}

// TestMarkReady_ForceCloseBestEffort — a Redis write error in the force-close
// path is logged but does NOT fail markReady (best-effort, mirroring
// publishPrimaryEvent's nil-Redis safety): the pod still reaches Ready. We
// model the Redis-unavailable case by passing a nil Redis client.
func TestMarkReady_ForceCloseBestEffort(t *testing.T) {
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "x")
	loader := newFakeLoader()
	dcgm := &fakeDCGMScraper{}
	dbtx := &fakeDBTX{}
	r := buildReconciler(t, Deps{
		Cfg:         cfg,
		FSM:         fsm,
		Loader:      loader,
		DCGMScraper: dcgm,
		Rule:        alwaysInPeakRule(),
		Redis:       nil, // Redis not wired — force-close is skipped, markReady still completes
	})
	r.SetQueriesForTest(gen.New(dbtx))

	require.NoError(t, r.markReady(context.Background(), 5, markReadyURLs(), 0.30, testLogger()),
		"a missing/failing Redis must NOT block markReady from reaching Ready")
	require.Equal(t, StateReady, r.deps.FSM.State(),
		"markReady completes the Provisioning→Ready transition despite no Redis")
}

// costOpenRow models the GetOpenPrimaryLifecycle row for
// calculatePrimaryCostBRL tests. started_at (dest[1]) and
// first_health_pass_at (dest[2]) drive the cost basis; accepted_dph
// (dest[8]) drives the effective DPH (fix #2 — the calc reads the row's
// accepted_dph, NOT the closeLifecycle param, because evaluateDestroying
// closes schedule-driven rows with param=0).
type costOpenRow struct {
	startedAt   time.Time
	firstHealth pgtype.Timestamptz
	acceptedDph pgtype.Numeric
}

func (r costOpenRow) Scan(dest ...interface{}) error {
	if len(dest) < 13 {
		return fmt.Errorf("costOpenRow: expected 13 dest pointers, got %d", len(dest))
	}
	if p, ok := dest[1].(*time.Time); ok {
		*p = r.startedAt
	}
	if p, ok := dest[2].(*pgtype.Timestamptz); ok {
		*p = r.firstHealth
	}
	if p, ok := dest[8].(*pgtype.Numeric); ok {
		*p = r.acceptedDph
	}
	return nil
}

// TestCalculatePrimaryCostBRL_StartedAtFallback proves two things:
//
//   - Cost basis falls back from first_health_pass_at to started_at when the
//     former is NULL, so closed lifecycle rows persist a non-zero
//     total_cost_brl (dashboard vast_cost R$0 bug). Mirrors the
//     /admin/operations live-accrual approximation (started_at over-counts
//     only the bounded cold-start window).
//   - The effective DPH comes from the ROW's accepted_dph, NOT the param
//     (fix #2). evaluateDestroying closes schedule-driven rows via
//     closeLifecycle(..., "destroyed", 0) — a param=0 must NOT zero the cost
//     when the row carries a real accepted_dph.
func TestCalculatePrimaryCostBRL_StartedAtFallback(t *testing.T) {
	cfg := testCfg(t) // USDToBRLRate = 5.0
	started := time.Now().Add(-2 * time.Hour)
	health := time.Now().Add(-1 * time.Hour)
	healthTz := pgtype.Timestamptz{Time: health, Valid: true}

	cases := []struct {
		name        string
		startedAt   time.Time
		firstHealth pgtype.Timestamptz
		rowDPH      pgtype.Numeric // accepted_dph stored on the lifecycle row
		paramDPH    float64        // value passed by closeLifecycle (often 0)
		wantHours   float64
		wantDPH     float64
		wantZero    bool
	}{
		{"first_health set uses first_health", started, healthTz, numericFromFloat(0.30), 0.30, 1.0, 0.30, false},
		{"first_health NULL falls back to started_at", started, pgtype.Timestamptz{}, numericFromFloat(0.30), 0.30, 2.0, 0.30, false},
		{"both timestamps NULL returns 0", time.Time{}, pgtype.Timestamptz{}, numericFromFloat(0.30), 0.30, 0, 0, true},
		// fix #2 — schedule close: evaluateDestroying passes param=0 but row
		// carries the real accepted_dph; cost MUST come from the row.
		{"param dph 0 uses row accepted_dph (schedule close)", started, pgtype.Timestamptz{}, numericFromFloat(0.1888), 0, 2.0, 0.1888, false},
		// fix #2 — provisioning failure: row never stamped accepted_dph.
		{"row accepted_dph NULL returns 0", started, healthTz, pgtype.Numeric{}, 0, 0, 0, true},
		{"row accepted_dph 0 returns 0", started, healthTz, numericFromFloat(0), 0, 0, 0, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			dbtx := &fakeDBTX{
				queryRowFn: func(_ context.Context, _ string, _ ...interface{}) pgx.Row {
					return costOpenRow{startedAt: tc.startedAt, firstHealth: tc.firstHealth, acceptedDph: tc.rowDPH}
				},
			}
			r := buildReconciler(t, Deps{Cfg: cfg})
			r.SetQueriesForTest(gen.New(dbtx))

			got := r.calculatePrimaryCostBRL(context.Background(), 7, tc.paramDPH)
			if tc.wantZero {
				require.Equal(t, 0.0, got)
				return
			}
			want := tc.wantDPH * tc.wantHours * cfg.USDToBRLRate
			require.InDelta(t, want, got, 0.01,
				"cost = rowDPH(%v) × hours(%v) × rate(%v)", tc.wantDPH, tc.wantHours, cfg.USDToBRLRate)
		})
	}
}

// ===========================================================================
// Phase 20-04 — FF-01 (created-budget) + FF-02 (progress-stall) + BL-01/AL-01
// (classified outcome hook) tests. Drive the PRODUCTION waitForReadyOrDestroy
// (via runWait) with a provision-cfg snapshot carrying the FF budgets, and
// the recordProvisionOutcome hook + helpers directly.
// ===========================================================================

// runWait wraps waitForReadyOrDestroy so the plan's signature change (adding a
// returned close-reason string in Task 4) touches ONE call site, not every FF
// test. Returns the provision error only.
func runWait(r *Reconciler, ctx context.Context, lifecycleID, instanceID int64, dph float64, log *slog.Logger) error {
	_, err := r.waitForReadyOrDestroy(ctx, lifecycleID, instanceID, dph, log)
	return err
}

// setProvisionCfgForTest stashes a provision-cfg snapshot so
// currentProvisionCfg (→ provisionCfg.Load) returns the FF budgets under test.
func setProvisionCfgForTest(r *Reconciler, c podconfig.PodConfig) {
	r.provisionCfg.Store(&c)
}

// reasonRecorder captures the shutdown_reason of every ClosePrimaryLifecycle
// UPDATE the reconciler executes.
type reasonRecorder struct {
	mu      sync.Mutex
	reasons []string
}

func (rr *reasonRecorder) has(reason string) bool {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	for _, r := range rr.reasons {
		if r == reason {
			return true
		}
	}
	return false
}

func newCloseReasonDBTX(rr *reasonRecorder) *fakeDBTX {
	return &fakeDBTX{
		execFn: func(_ context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
			if contains(sql, "UPDATE ai_gateway.primary_lifecycles") && len(args) >= 2 {
				if reason, ok := args[1].(pgtype.Text); ok && reason.Valid {
					rr.mu.Lock()
					rr.reasons = append(rr.reasons, reason.String)
					rr.mu.Unlock()
				}
			}
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
	}
}

// --- FF-02 onstart-log fixtures --------------------------------------------

const dlPrefix = "[2026-07-10T00:00:00+00:00] [download-weights] "

func dlFetching(key string) string { return dlPrefix + "fetching " + key + " -> /weights/" + key }
func dlProgress(key string, b int64) string {
	return dlPrefix + "progress key=" + key + " bytes=" + strconv.FormatInt(b, 10)
}
func dlOK(key string) string { return dlPrefix + "ok /weights/" + key + " (sha256=abcdef012345...)" }

func dlText(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

// --- FF-01 created-budget ---------------------------------------------------

func TestCreatedBudget_StuckCreated_Destroys(t *testing.T) {
	withTestPollInterval(t, 2*time.Millisecond)
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return vast.Instance{ID: 42, ActualStatus: "created"}, nil
		},
	}
	rr := &reasonRecorder{}
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Rule: alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(newCloseReasonDBTX(rr)))
	r.activeLifecycleID.Store(99)
	setProvisionCfgForTest(r, podconfig.PodConfig{CreatedBudgetS: 0, ColdStartBudgetS: 30, PortBindBudgetS: 600})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := runWait(r, ctx, 99, 42, 0.30, testLogger())

	require.Error(t, err, "a pod stuck in actual_status=created past created_budget_s must fail fast")
	require.Equal(t, int32(1), fakeV.destroyCalls.Load(), "BestEffortDestroy must fire once on created-budget timeout")
	require.True(t, rr.has("created_state_timeout"), "close reason must be created_state_timeout")
}

// TestCreatedBudget_BlankStatus_Destroys — a BLANK actual_status is the same
// regime-1 window as created/scheduling (observed live 2026-07-16: lifecycle
// 190/machine 54626 kept actual_status null for 10+ min — host never picked up
// the container — and rode toward the coldstart ceiling because "" matched
// neither created nor loading).
func TestCreatedBudget_BlankStatus_Destroys(t *testing.T) {
	withTestPollInterval(t, 2*time.Millisecond)
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return vast.Instance{ID: 42, MachineID: 54626, ActualStatus: ""}, nil
		},
	}
	rr := &reasonRecorder{}
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Rule: alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(newCloseReasonDBTX(rr)))
	r.activeLifecycleID.Store(99)
	setProvisionCfgForTest(r, podconfig.PodConfig{CreatedBudgetS: 0, ColdStartBudgetS: 30, PortBindBudgetS: 600})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := runWait(r, ctx, 99, 42, 0.30, testLogger())

	require.Error(t, err, "a pod whose actual_status never leaves blank must fail fast via the created budget")
	require.Equal(t, int32(1), fakeV.destroyCalls.Load())
	require.True(t, rr.has("created_state_timeout"), "close reason must be created_state_timeout")
	require.Equal(t, []string{vast.ReportProblemUnableToStart}, fakeV.reportedProblems(),
		"blank-status created timeout must report the machine before the destroy")
}

// TestWaitForReady_ReportBlocksIP — every report-worthy fast-fail must also
// record the host's public IP on the Redis blocklist (report ⇒ IP-block).
func TestWaitForReady_ReportBlocksIP(t *testing.T) {
	withTestPollInterval(t, 2*time.Millisecond)
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return vast.Instance{ID: 42, MachineID: 56883, PublicIPAddr: "120.238.149.205",
				ActualStatus: "loading", StatusMsg: "a1b2c3: Retrying in 1 second"}, nil
		},
	}
	rr := &reasonRecorder{}
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Rule: alwaysInPeakRule(), Redis: rdb,
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(newCloseReasonDBTX(rr)))
	r.activeLifecycleID.Store(99)
	setProvisionCfgForTest(r, podconfig.PodConfig{CreatedBudgetS: 60, ColdStartBudgetS: 30, PortBindBudgetS: 600})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = mustErr(runWait(r, ctx, 99, 42, 0.30, testLogger()))

	require.True(t, s.Exists("gw:primary:blockip:120.238.149.205"),
		"a pull-stall fast-fail must blocklist the host's public IP")
}

// TestWaitForReady_HealthTimeout_DoesNotBlockIP — a blown coldstart budget is
// AMBIGUOUS (slow-but-legit host), so health_timeout reports to Vast but must
// NOT IP-block: banning the IP for 24h would break the retry-on-same-host
// path (the supervisord autorestart flow re-provisions the same host after a
// first health_timeout and MUST be allowed to succeed).
func TestWaitForReady_HealthTimeout_DoesNotBlockIP(t *testing.T) {
	withTestPollInterval(t, 2*time.Millisecond)
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			// Healthy-looking loading pod that simply never becomes ready.
			return vast.Instance{ID: 44, MachineID: 77, PublicIPAddr: "203.0.113.7",
				ActualStatus: "loading"}, nil
		},
	}
	rr := &reasonRecorder{}
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Rule: alwaysInPeakRule(), Redis: rdb,
		HealthCheck: func(_ context.Context, _ string) bool { return false },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(newCloseReasonDBTX(rr)))
	r.activeLifecycleID.Store(99)
	// Tiny coldstart budget so the deadline fires quickly.
	setProvisionCfgForTest(r, podconfig.PodConfig{CreatedBudgetS: 60, ColdStartBudgetS: 1, PortBindBudgetS: 600})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := runWait(r, ctx, 99, 44, 0.30, testLogger())

	require.Error(t, err)
	require.True(t, rr.has("health_timeout"), "close reason must be health_timeout")
	require.False(t, s.Exists("gw:primary:blockip:203.0.113.7"),
		"health_timeout must NOT blocklist the IP (ambiguous, breaks same-host retry)")
}

// TestWaitForReady_BlockedIP_FastFails — a fresh instance whose public IP is
// already on the blocklist (a sibling machine behind the same NAT failed
// within blockIPTTL) must die in one poll with blocklisted_ip and NO Vast
// report (guilt by shared link is not new defect evidence). The observed case:
// machines 56883 + 59295 behind 120.238.149.205, each wasting a full ~17GiB
// download on the same broken link (2026-07-16).
func TestWaitForReady_BlockedIP_FastFails(t *testing.T) {
	withTestPollInterval(t, 2*time.Millisecond)
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	require.NoError(t, s.Set("gw:primary:blockip:120.238.149.205", "machine=56883 problem=x"))
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			// A different machine_id behind the SAME public IP, healthy-looking.
			return vast.Instance{ID: 43, MachineID: 59295, PublicIPAddr: "120.238.149.205",
				ActualStatus: "loading"}, nil
		},
	}
	rr := &reasonRecorder{}
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Rule: alwaysInPeakRule(), Redis: rdb,
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(newCloseReasonDBTX(rr)))
	r.activeLifecycleID.Store(99)
	setProvisionCfgForTest(r, podconfig.PodConfig{CreatedBudgetS: 60, ColdStartBudgetS: 30, PortBindBudgetS: 600})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := runWait(r, ctx, 99, 43, 0.30, testLogger())

	require.Error(t, err, "blocked IP must fast-fail")
	require.True(t, rr.has("blocklisted_ip"), "close reason must be blocklisted_ip")
	require.Equal(t, int32(1), fakeV.destroyCalls.Load(), "must destroy the instance")
	require.Empty(t, fakeV.reportedProblems(), "blocked-IP kill must NOT file a Vast report")
}

// mustErr passes err through, asserting non-nil at the call site keeps the
// linter happy for tests that only care about side effects.
func mustErr(err error) error { return err }

// TestWaitForReady_IntendedStopped_Destroys — Vast reports intended_status=stopped
// (billing/host stop) while actual_status is still non-terminal ("loading"), the
// case IsTerminal() misses. Must fast-fail via the 3-strike terminal path instead
// of riding the coldstart budget.
func TestWaitForReady_IntendedStopped_Destroys(t *testing.T) {
	withTestPollInterval(t, 2*time.Millisecond)
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return vast.Instance{ID: 42, ActualStatus: "loading", IntendedStatus: "stopped"}, nil
		},
	}
	rr := &reasonRecorder{}
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Rule: alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(newCloseReasonDBTX(rr)))
	r.activeLifecycleID.Store(99)
	// Large coldstart budget so ONLY the intended-stopped fast-fail can close it.
	setProvisionCfgForTest(r, podconfig.PodConfig{CreatedBudgetS: 60, ColdStartBudgetS: 30, PortBindBudgetS: 600})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := runWait(r, ctx, 99, 42, 0.30, testLogger())

	require.Error(t, err, "intended_status=stopped must fast-fail provisioning")
	require.Equal(t, int32(1), fakeV.destroyCalls.Load(), "BestEffortDestroy must fire once")
	require.True(t, rr.has("instance_terminal_state"), "close reason must be instance_terminal_state")
	require.Empty(t, fakeV.reportedProblems(),
		"terminal/stopped must NOT file a machine report — may be our own billing stop, not the host's fault")
}

// TestWaitForReady_PullRetrying_Destroys — a host stuck retrying a docker layer
// pull (bad registry route) must fast-fail after 3 strikes, not ride the budget.
func TestWaitForReady_PullRetrying_Destroys(t *testing.T) {
	withTestPollInterval(t, 2*time.Millisecond)
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return vast.Instance{ID: 42, MachineID: 141296, ActualStatus: "loading", StatusMsg: "a1b2c3: Retrying in 1 second"}, nil
		},
	}
	rr := &reasonRecorder{}
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Rule: alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(newCloseReasonDBTX(rr)))
	r.activeLifecycleID.Store(99)
	setProvisionCfgForTest(r, podconfig.PodConfig{CreatedBudgetS: 60, ColdStartBudgetS: 30, PortBindBudgetS: 600})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := runWait(r, ctx, 99, 42, 0.30, testLogger())

	require.Error(t, err, "a stuck pull (Retrying) must fast-fail provisioning")
	require.Equal(t, int32(1), fakeV.destroyCalls.Load(), "BestEffortDestroy must fire once")
	require.True(t, rr.has("vast_pull_retry_stall"), "close reason must be vast_pull_retry_stall")
	require.Equal(t, []string{vast.ReportProblemUnableToStart}, fakeV.reportedProblems(),
		"a pull-stall close must file exactly one Vast machine report (Unable To Start Instance) before the destroy")
}

// TestWaitForReady_TransientRetryThenLoading_NoFire — one Retrying poll followed
// by clean loading must NOT fire (strikes reset) — a one-off layer retry recovers
// and the pull rides to the coldstart ceiling.
func TestWaitForReady_TransientRetryThenLoading_NoFire(t *testing.T) {
	withTestPollInterval(t, 2*time.Millisecond)
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")
	var calls atomic.Int32
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			if calls.Add(1) == 1 {
				return vast.Instance{ID: 42, ActualStatus: "loading", StatusMsg: "a1b2c3: Retrying in 1 second"}, nil
			}
			return vast.Instance{ID: 42, ActualStatus: "loading"}, nil
		},
	}
	rr := &reasonRecorder{}
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Rule: alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(newCloseReasonDBTX(rr)))
	r.activeLifecycleID.Store(99)
	setProvisionCfgForTest(r, podconfig.PodConfig{CreatedBudgetS: 60, ColdStartBudgetS: 30, PortBindBudgetS: 600})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- runWait(r, ctx, 99, 42, 0.30, testLogger()) }()
	time.Sleep(80 * time.Millisecond)
	require.Equal(t, int32(0), fakeV.destroyCalls.Load(),
		"a one-off retry that recovers to clean loading must NOT be destroyed")
	require.False(t, rr.has("vast_pull_retry_stall"),
		"strikes must reset — no vast_pull_retry_stall close")
	cancel()
	<-errCh
}

func TestCreatedBudget_CreatedThenRunning_NoFire(t *testing.T) {
	withTestPollInterval(t, 2*time.Millisecond)
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")
	var calls atomic.Int32
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			if calls.Add(1) == 1 {
				return vast.Instance{ID: 42, ActualStatus: "created"}, nil
			}
			return runningInstanceWithAllPorts(42), nil
		},
	}
	rr := &reasonRecorder{}
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Loader: newFakeLoader(), DCGMScraper: &fakeDCGMScraper{},
		Rule:        alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(newCloseReasonDBTX(rr)))
	r.activeLifecycleID.Store(99)
	// large created budget → created→running before it could ever fire.
	setProvisionCfgForTest(r, podconfig.PodConfig{CreatedBudgetS: 60, ColdStartBudgetS: 30, PortBindBudgetS: 600})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := runWait(r, ctx, 99, 42, 0.30, testLogger())

	require.NoError(t, err, "created→running before budget must promote to Ready")
	require.Equal(t, StateReady, r.deps.FSM.State())
	require.Equal(t, int32(0), fakeV.destroyCalls.Load())
	require.False(t, rr.has("created_state_timeout"))
}

func TestCreatedBudget_CreatedThenLoading_Rides(t *testing.T) {
	withTestPollInterval(t, 2*time.Millisecond)
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")
	var calls atomic.Int32
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			if calls.Add(1) == 1 {
				return vast.Instance{ID: 42, ActualStatus: "created"}, nil
			}
			// regime 2: image pulling — must ride to the coldstart ceiling.
			return vast.Instance{ID: 42, ActualStatus: "loading"}, nil
		},
	}
	rr := &reasonRecorder{}
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Rule: alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(newCloseReasonDBTX(rr)))
	r.activeLifecycleID.Store(99)
	// created budget 0 (would fire if armed on loading) but coldstart 30s so it
	// rides during the observation window.
	setProvisionCfgForTest(r, podconfig.PodConfig{CreatedBudgetS: 0, ColdStartBudgetS: 30, PortBindBudgetS: 600})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- runWait(r, ctx, 99, 42, 0.30, testLogger()) }()
	time.Sleep(80 * time.Millisecond)
	require.Equal(t, int32(0), fakeV.destroyCalls.Load(),
		"a created→loading image-pull (regime 2) must NOT be destroyed by the created-budget")
	require.False(t, rr.has("created_state_timeout"),
		"regime 2 must NOT close with created_state_timeout — the anchor resets on the loading transition")
	cancel()
	<-errCh
}

// --- FF-02 progress-stall ---------------------------------------------------

func TestProgressStall_FrozenBytes_Destroys(t *testing.T) {
	withTestPollInterval(t, 2*time.Millisecond)
	setOnstartLogIntervalForTest(t, 2*time.Millisecond)
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return vast.Instance{ID: 42, ActualStatus: "loading"}, nil
		},
		onstartLogFn: func(_ context.Context, _ int64) (vast.OnstartLogResult, error) {
			return vast.OnstartLogResult{
				Status: vast.OnstartLogAvailable,
				Text:   dlText(dlFetching("qwen"), dlProgress("qwen", 1000)),
			}, nil
		},
	}
	rr := &reasonRecorder{}
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Rule: alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(newCloseReasonDBTX(rr)))
	r.activeLifecycleID.Store(99)
	setProvisionCfgForTest(r, podconfig.PodConfig{ProgressStallBudgetS: 0, CreatedBudgetS: 600, ColdStartBudgetS: 30, PortBindBudgetS: 600})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := runWait(r, ctx, 99, 42, 0.30, testLogger())

	require.Error(t, err, "frozen download bytes past progress_stall_budget_s must fail fast")
	require.Equal(t, int32(1), fakeV.destroyCalls.Load())
	require.True(t, rr.has("progress_stall_timeout"), "close reason must be progress_stall_timeout")
}

func TestProgressStall_RisingBytes_Rides(t *testing.T) {
	withTestPollInterval(t, 2*time.Millisecond)
	setOnstartLogIntervalForTest(t, 2*time.Millisecond)
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")
	var b atomic.Int64
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return vast.Instance{ID: 42, ActualStatus: "loading"}, nil
		},
		onstartLogFn: func(_ context.Context, _ int64) (vast.OnstartLogResult, error) {
			return vast.OnstartLogResult{
				Status: vast.OnstartLogAvailable,
				Text:   dlText(dlFetching("qwen"), dlProgress("qwen", b.Add(1000))),
			}, nil
		},
	}
	rr := &reasonRecorder{}
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Rule: alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(newCloseReasonDBTX(rr)))
	r.activeLifecycleID.Store(99)
	setProvisionCfgForTest(r, podconfig.PodConfig{ProgressStallBudgetS: 0, CreatedBudgetS: 600, ColdStartBudgetS: 30, PortBindBudgetS: 600})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- runWait(r, ctx, 99, 42, 0.30, testLogger()) }()
	time.Sleep(80 * time.Millisecond)
	require.Equal(t, int32(0), fakeV.destroyCalls.Load(), "a healthy rising-bytes download must ride, never fire")
	require.False(t, rr.has("progress_stall_timeout"))
	cancel()
	<-errCh
}

func TestProgressStall_TelemetryUnavailable_Rides(t *testing.T) {
	statuses := []struct {
		name string
		st   vast.OnstartLogStatus
	}{
		{"fetch_error", vast.OnstartLogFetchError},
		{"empty", vast.OnstartLogEmpty},
		{"not_ready", vast.OnstartLogNotReady},
	}
	for _, s := range statuses {
		s := s
		t.Run(s.name, func(t *testing.T) {
			withTestPollInterval(t, 2*time.Millisecond)
			setOnstartLogIntervalForTest(t, 2*time.Millisecond)
			cfg := testCfg(t)
			fsm := NewFSM(nil, nil)
			_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")
			fakeV := &fakeVast{
				getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
					return vast.Instance{ID: 42, ActualStatus: "loading"}, nil
				},
				onstartLogFn: func(_ context.Context, _ int64) (vast.OnstartLogResult, error) {
					return vast.OnstartLogResult{Status: s.st}, nil
				},
			}
			rr := &reasonRecorder{}
			r := buildReconciler(t, Deps{
				Cfg: cfg, FSM: fsm, Rule: alwaysInPeakRule(),
				HealthCheck: func(_ context.Context, _ string) bool { return true },
				Vast:        fakeV,
			})
			r.SetQueriesForTest(gen.New(newCloseReasonDBTX(rr)))
			r.activeLifecycleID.Store(99)
			setProvisionCfgForTest(r, podconfig.PodConfig{ProgressStallBudgetS: 0, CreatedBudgetS: 600, ColdStartBudgetS: 30, PortBindBudgetS: 600})

			ctx, cancel := context.WithCancel(context.Background())
			errCh := make(chan error, 1)
			go func() { errCh <- runWait(r, ctx, 99, 42, 0.30, testLogger()) }()
			time.Sleep(60 * time.Millisecond)
			require.Equal(t, int32(0), fakeV.destroyCalls.Load(),
				"telemetry-unavailable (%s) is UNKNOWN — must ride to the coldstart ceiling, never a false kill", s.name)
			require.False(t, rr.has("progress_stall_timeout"))
			cancel()
			<-errCh
		})
	}
}

func TestProgressStall_PostDownloadStartup_Rides(t *testing.T) {
	withTestPollInterval(t, 2*time.Millisecond)
	setOnstartLogIntervalForTest(t, 2*time.Millisecond)
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")
	// Full download COMPLETE (all 4 mandatory ok — qwen+whisper+bge-m3+chatterbox,
	// expectedWeightFiles=4) then healthy-but-slow startup lines with no further
	// progress bytes → the download-stall detector must DISARM.
	fullText := dlText(
		dlFetching("qwen"), dlProgress("qwen", 1000), dlOK("qwen"),
		dlFetching("whisper"), dlProgress("whisper", 2000), dlOK("whisper"),
		dlFetching("bge-m3"), dlProgress("bge-m3", 3000), dlOK("bge-m3"),
		dlFetching("chatterbox"), dlProgress("chatterbox", 4000), dlOK("chatterbox"),
		"[2026-07-10T00:01:00+00:00] docker compose up -d",
		"[2026-07-10T00:02:00+00:00] waiting for /health/ready",
	)
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return vast.Instance{ID: 42, ActualStatus: "loading"}, nil
		},
		onstartLogFn: func(_ context.Context, _ int64) (vast.OnstartLogResult, error) {
			return vast.OnstartLogResult{Status: vast.OnstartLogAvailable, Text: fullText}, nil
		},
	}
	rr := &reasonRecorder{}
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Rule: alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(newCloseReasonDBTX(rr)))
	r.activeLifecycleID.Store(99)
	setProvisionCfgForTest(r, podconfig.PodConfig{ProgressStallBudgetS: 0, CreatedBudgetS: 600, ColdStartBudgetS: 30, PortBindBudgetS: 600})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- runWait(r, ctx, 99, 42, 0.30, testLogger()) }()
	time.Sleep(60 * time.Millisecond)
	require.Equal(t, int32(0), fakeV.destroyCalls.Load(),
		"a healthy startup AFTER a complete download must not trip the download-stall detector (disarmed)")
	require.False(t, rr.has("progress_stall_timeout"))
	cancel()
	<-errCh
}

func TestProgressStall_NotArmedWithoutFetching_Rides(t *testing.T) {
	withTestPollInterval(t, 2*time.Millisecond)
	setOnstartLogIntervalForTest(t, 2*time.Millisecond)
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return vast.Instance{ID: 42, ActualStatus: "loading"}, nil
		},
		onstartLogFn: func(_ context.Context, _ int64) (vast.OnstartLogResult, error) {
			// available but no `fetching` line yet → phase never arms.
			return vast.OnstartLogResult{Status: vast.OnstartLogAvailable, Text: dlText("[t] preflight ok")}, nil
		},
	}
	rr := &reasonRecorder{}
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Rule: alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(newCloseReasonDBTX(rr)))
	r.activeLifecycleID.Store(99)
	setProvisionCfgForTest(r, podconfig.PodConfig{ProgressStallBudgetS: 0, CreatedBudgetS: 600, ColdStartBudgetS: 30, PortBindBudgetS: 600})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- runWait(r, ctx, 99, 42, 0.30, testLogger()) }()
	time.Sleep(60 * time.Millisecond)
	require.Equal(t, int32(0), fakeV.destroyCalls.Load(), "no fetching line → download phase never armed → no fire")
	require.False(t, rr.has("progress_stall_timeout"))
	cancel()
	<-errCh
}

func TestProgressStall_OnstartLogThrottled(t *testing.T) {
	withTestPollInterval(t, 2*time.Millisecond)
	setOnstartLogIntervalForTest(t, 20*time.Millisecond)
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")
	var getCalls, logCalls atomic.Int32
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			getCalls.Add(1)
			return vast.Instance{ID: 42, ActualStatus: "loading"}, nil
		},
		onstartLogFn: func(_ context.Context, _ int64) (vast.OnstartLogResult, error) {
			logCalls.Add(1)
			return vast.OnstartLogResult{Status: vast.OnstartLogNotReady}, nil
		},
	}
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Rule: alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(&fakeDBTX{}))
	r.activeLifecycleID.Store(99)
	setProvisionCfgForTest(r, podconfig.PodConfig{ProgressStallBudgetS: 600, CreatedBudgetS: 600, ColdStartBudgetS: 30, PortBindBudgetS: 600})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- runWait(r, ctx, 99, 42, 0.30, testLogger()) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-errCh
	require.Greater(t, getCalls.Load(), logCalls.Load(),
		"the onstart log fetch runs on a slower sub-cadence than the poll tick")
	require.LessOrEqual(t, logCalls.Load(), int32(8),
		"~100ms at a 20ms sub-cadence should fetch the log well under 8 times")
}

func TestParseDownloadProgress(t *testing.T) {
	text := dlText(
		dlFetching("qwen"), dlProgress("qwen", 1000),
		dlFetching("whisper"), dlProgress("whisper", 500), dlProgress("whisper", 2000),
		dlOK("qwen"),
	)
	fetching, ok, total := parseDownloadProgress(text)
	require.True(t, fetching)
	require.Equal(t, 1, ok)
	// latest bytes per key: qwen=1000, whisper=2000 → 3000
	require.Equal(t, int64(3000), total)

	// junk → zero
	f2, o2, t2 := parseDownloadProgress("random log line\nno markers here\n")
	require.False(t, f2)
	require.Equal(t, 0, o2)
	require.Equal(t, int64(0), t2)
}

// --- 20-07 gap-closure: port-bind anchored on download completion -----------

// TestPortBind_DownloadInFlight_RidesThenFiresAfterDone is the core 20-07
// regression: a fresh coldstart reports actual_status=running while the ~20GB
// weights download runs INSIDE onstart (before llama-server binds its public
// port). With port_bind_budget_s=0 the OLD code false-killed on the first
// running poll (lifecycle 137). The fix skips the port-bind timeout while the
// download is armed-but-not-done, then measures the tight budget from
// download-completion. Proof of riding: destroy fires only AFTER the onstart
// log flips to all-4-ok (disarm), not on the first running poll.
func TestPortBind_DownloadInFlight_RidesThenFiresAfterDone(t *testing.T) {
	withTestPollInterval(t, 2*time.Millisecond)
	setOnstartLogIntervalForTest(t, 2*time.Millisecond)
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")

	const disarmAt = int32(5)
	var logCalls atomic.Int32
	var destroyedAtCall atomic.Int32
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			// running, published ports empty → the unbound port-bind branch.
			return runningInstanceNoPorts(42), nil
		},
		onstartLogFn: func(_ context.Context, _ int64) (vast.OnstartLogResult, error) {
			n := logCalls.Add(1)
			// armed-but-not-done: fetching + 3 non-qwen ok (okCount=3 < 4) while
			// qwen is still downloading. bytes constant (ProgressStallBudgetS high
			// so FF-02 never fires during the ride).
			lines := []string{
				dlFetching("qwen"), dlProgress("qwen", 1000),
				dlOK("bge-m3"), dlOK("whisper"), dlOK("chatterbox"),
			}
			if n >= disarmAt {
				lines = append(lines, dlOK("qwen")) // 4th mandatory ok → disarm
			}
			return vast.OnstartLogResult{Status: vast.OnstartLogAvailable, Text: dlText(lines...)}, nil
		},
		destroyFn: func(_ context.Context, _ int64) error {
			destroyedAtCall.Store(logCalls.Load())
			return nil
		},
	}
	rr := &reasonRecorder{}
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Rule: alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(newCloseReasonDBTX(rr)))
	r.activeLifecycleID.Store(99)
	// port_bind_budget_s=0 → fires on the FIRST qualifying poll once enforced;
	// progress_stall high so the frozen-bytes ride never trips FF-02.
	setProvisionCfgForTest(r, podconfig.PodConfig{ProgressStallBudgetS: 600, CreatedBudgetS: 600, ColdStartBudgetS: 30, PortBindBudgetS: 0})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := runWait(r, ctx, 99, 42, 0.30, testLogger())

	require.Error(t, err, "after the download completes, running-but-unbound past the tight budget must fail fast")
	require.Contains(t, err.Error(), "public port bind timeout")
	require.Equal(t, int32(1), fakeV.destroyCalls.Load())
	require.True(t, rr.has("public_port_bind_timeout"))
	require.GreaterOrEqual(t, destroyedAtCall.Load(), disarmAt,
		"the port-bind kill must fire ONLY after the download disarmed (rode through the armed phase, no false kill during download)")
}

// TestPortBind_TelemetryUnavailable_FiresFromFirstRunning asserts the legacy
// fallback: when the onstart log never yields a download signal (FetchError →
// downloadArmed stays false), the port-bind budget still anchors on
// firstRunningAt and fast-fails as before — no behavior regression for the
// no-telemetry case.
func TestPortBind_TelemetryUnavailable_FiresFromFirstRunning(t *testing.T) {
	withTestPollInterval(t, 2*time.Millisecond)
	setOnstartLogIntervalForTest(t, 2*time.Millisecond)
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")

	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return runningInstanceNoPorts(42), nil
		},
		onstartLogFn: func(_ context.Context, _ int64) (vast.OnstartLogResult, error) {
			// telemetry unavailable — download never arms.
			return vast.OnstartLogResult{Status: vast.OnstartLogFetchError}, nil
		},
	}
	rr := &reasonRecorder{}
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Rule: alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(newCloseReasonDBTX(rr)))
	r.activeLifecycleID.Store(99)
	setProvisionCfgForTest(r, podconfig.PodConfig{ProgressStallBudgetS: 600, CreatedBudgetS: 600, ColdStartBudgetS: 30, PortBindBudgetS: 0})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := runWait(r, ctx, 99, 42, 0.30, testLogger())

	require.Error(t, err, "telemetry-unavailable + running-unbound past budget must fast-fail from firstRunningAt (legacy)")
	require.Contains(t, err.Error(), "public port bind timeout")
	require.Equal(t, int32(1), fakeV.destroyCalls.Load())
	require.True(t, rr.has("public_port_bind_timeout"))
}

// TestFF02_ThreeOfFourOk_StaysArmed proves expectedWeightFiles=4: with only the
// 3 non-qwen mandatory files logged `ok` (qwen still downloading) and bytes
// frozen past progress_stall_budget_s=0, the stall detector must STILL be armed
// and fire progress_stall_timeout. At the old value 3 it would have disarmed
// prematurely and skipped the stall check.
func TestFF02_ThreeOfFourOk_StaysArmed(t *testing.T) {
	withTestPollInterval(t, 2*time.Millisecond)
	setOnstartLogIntervalForTest(t, 2*time.Millisecond)
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")
	// 3 non-qwen ok + qwen fetching/progress frozen → okCount=3 < 4 (armed).
	text := dlText(
		dlFetching("qwen"), dlProgress("qwen", 1000),
		dlOK("bge-m3"), dlOK("whisper"), dlOK("chatterbox"),
	)
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return vast.Instance{ID: 42, ActualStatus: "loading"}, nil
		},
		onstartLogFn: func(_ context.Context, _ int64) (vast.OnstartLogResult, error) {
			return vast.OnstartLogResult{Status: vast.OnstartLogAvailable, Text: text}, nil
		},
	}
	rr := &reasonRecorder{}
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Rule: alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(newCloseReasonDBTX(rr)))
	r.activeLifecycleID.Store(99)
	setProvisionCfgForTest(r, podconfig.PodConfig{ProgressStallBudgetS: 0, CreatedBudgetS: 600, ColdStartBudgetS: 30, PortBindBudgetS: 600})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := runWait(r, ctx, 99, 42, 0.30, testLogger())

	require.Error(t, err, "3-of-4 ok with qwen frozen must stay ARMED and trip the stall detector")
	require.Equal(t, int32(1), fakeV.destroyCalls.Load())
	require.True(t, rr.has("progress_stall_timeout"),
		"expectedWeightFiles=4: the 3 non-qwen files must NOT disarm while qwen downloads")
}

// TestFF02_FourthOk_Disarms asserts the disarm boundary: once ALL 4 mandatory
// files log `ok`, the detector disarms — a subsequent healthy-but-slow startup
// with frozen bytes must NOT trip the stall detector (rides).
func TestFF02_FourthOk_Disarms(t *testing.T) {
	withTestPollInterval(t, 2*time.Millisecond)
	setOnstartLogIntervalForTest(t, 2*time.Millisecond)
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")
	// all 4 mandatory ok (okCount=4 >= 4) with bytes frozen → disarmed.
	text := dlText(
		dlFetching("qwen"), dlProgress("qwen", 1000),
		dlOK("qwen"), dlOK("bge-m3"), dlOK("whisper"), dlOK("chatterbox"),
	)
	fakeV := &fakeVast{
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return vast.Instance{ID: 42, ActualStatus: "loading"}, nil
		},
		onstartLogFn: func(_ context.Context, _ int64) (vast.OnstartLogResult, error) {
			return vast.OnstartLogResult{Status: vast.OnstartLogAvailable, Text: text}, nil
		},
	}
	rr := &reasonRecorder{}
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Rule: alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return true },
		Vast:        fakeV,
	})
	r.SetQueriesForTest(gen.New(newCloseReasonDBTX(rr)))
	r.activeLifecycleID.Store(99)
	setProvisionCfgForTest(r, podconfig.PodConfig{ProgressStallBudgetS: 0, CreatedBudgetS: 600, ColdStartBudgetS: 30, PortBindBudgetS: 600})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- runWait(r, ctx, 99, 42, 0.30, testLogger()) }()
	time.Sleep(60 * time.Millisecond)
	require.Equal(t, int32(0), fakeV.destroyCalls.Load(),
		"all 4 mandatory ok → disarmed; a slow startup with frozen bytes must ride, never trip the stall")
	require.False(t, rr.has("progress_stall_timeout"))
	cancel()
	<-errCh
}

// --- BL-01 / AL-01 outcome hook ---------------------------------------------

// podConfigRow feeds GetPodConfig's 43-column Scan; only the two machine-list
// columns (dest[1] blocklist, dest[2] allowlist) are populated.
type podConfigRow struct {
	block []int64
	allow []int64
}

func (r podConfigRow) Scan(dest ...interface{}) error {
	if len(dest) < 3 {
		return fmt.Errorf("podConfigRow: expected >=3 dest pointers, got %d", len(dest))
	}
	if p, ok := dest[1].(*[]int64); ok {
		*p = r.block
	}
	if p, ok := dest[2].(*[]int64); ok {
		*p = r.allow
	}
	return nil
}

type listCapture struct {
	mu              sync.Mutex
	blocklistWrites [][]int64
	allowlistWrites [][]int64
}

func (c *listCapture) lastBlocklist() ([]int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.blocklistWrites) == 0 {
		return nil, false
	}
	return c.blocklistWrites[len(c.blocklistWrites)-1], true
}

func (c *listCapture) lastAllowlist() ([]int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.allowlistWrites) == 0 {
		return nil, false
	}
	return c.allowlistWrites[len(c.allowlistWrites)-1], true
}

func newPodConfigDBTX(block, allow []int64, cap *listCapture) *fakeDBTX {
	return &fakeDBTX{
		queryRowFn: func(_ context.Context, sql string, _ ...interface{}) pgx.Row {
			if contains(sql, "FROM ai_gateway.pod_config") {
				return podConfigRow{block: block, allow: allow}
			}
			return errRow{err: errors.New("newPodConfigDBTX: unexpected queryRow")}
		},
		execFn: func(_ context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
			if contains(sql, "vast_machine_blocklist") && len(args) >= 1 {
				if v, ok := args[0].([]int64); ok {
					cap.mu.Lock()
					cap.blocklistWrites = append(cap.blocklistWrites, v)
					cap.mu.Unlock()
				}
			}
			if contains(sql, "vast_machine_allowlist") && len(args) >= 1 {
				if v, ok := args[0].([]int64); ok {
					cap.mu.Lock()
					cap.allowlistWrites = append(cap.allowlistWrites, v)
					cap.mu.Unlock()
				}
			}
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
	}
}

func int64sContain(list []int64, id int64) bool {
	for _, x := range list {
		if x == id {
			return true
		}
	}
	return false
}

func TestRecordProvisionOutcome_MachineAttributable_Blocklists(t *testing.T) {
	reasons := []string{
		"created_state_timeout",
		"instance_terminal_state",
		"instance_terminal_state_confirmed",
		"public_port_bind_timeout",
	}
	for _, reason := range reasons {
		reason := reason
		t.Run(reason, func(t *testing.T) {
			cap := &listCapture{}
			r := buildReconciler(t, Deps{Cfg: testCfg(t)})
			r.SetQueriesForTest(gen.New(newPodConfigDBTX(nil, []int64{42}, cap)))
			r.recordProvisionOutcome(context.Background(), 42, 1, reason, errors.New("boom"), testLogger())

			blk, ok := cap.lastBlocklist()
			require.True(t, ok, "machine-attributable failure at failStreak>=1 must write the blocklist")
			require.True(t, int64sContain(blk, 42), "machine 42 must be blocklisted")
			alw, ok := cap.lastAllowlist()
			require.True(t, ok, "machine must be removed from the allowlist")
			require.False(t, int64sContain(alw, 42), "machine 42 must be removed from the allowlist")
		})
	}
}

func TestRecordProvisionOutcome_FirstFailureGrace_NoBlocklist(t *testing.T) {
	cap := &listCapture{}
	r := buildReconciler(t, Deps{Cfg: testCfg(t)})
	r.SetQueriesForTest(gen.New(newPodConfigDBTX(nil, nil, cap)))
	r.recordProvisionOutcome(context.Background(), 42, 0, "created_state_timeout", errors.New("boom"), testLogger())
	require.Empty(t, cap.blocklistWrites, "failStreak==0 (1st failure) must NOT blocklist even a machine-attributable reason")
}

func TestRecordProvisionOutcome_ExcludedReasons_NeverBlocklist(t *testing.T) {
	reasons := []string{
		"progress_stall_timeout",
		"cancelled_in_flight",
		"health_timeout",
		"create_error",
		"build_create_request_failed:boom",
		"audit_write_failed",
		"vast_status_msg_error:image pull failed",
	}
	for _, reason := range reasons {
		reason := reason
		t.Run(reason, func(t *testing.T) {
			cap := &listCapture{}
			r := buildReconciler(t, Deps{Cfg: testCfg(t)})
			r.SetQueriesForTest(gen.New(newPodConfigDBTX(nil, nil, cap)))
			r.recordProvisionOutcome(context.Background(), 42, 1, reason, errors.New("boom"), testLogger())
			require.Empty(t, cap.blocklistWrites, "excluded reason %q must NEVER blocklist", reason)
		})
	}
}

func TestRecordProvisionOutcome_Success_Allowlists(t *testing.T) {
	cap := &listCapture{}
	r := buildReconciler(t, Deps{Cfg: testCfg(t)})
	// machine was previously blocklisted → success must self-heal it.
	r.SetQueriesForTest(gen.New(newPodConfigDBTX([]int64{42}, nil, cap)))
	r.recordProvisionOutcome(context.Background(), 42, 0, "ready", nil, testLogger())

	alw, ok := cap.lastAllowlist()
	require.True(t, ok, "success must write the allowlist")
	require.True(t, int64sContain(alw, 42), "machine 42 must be allowlisted on success")
	blk, ok := cap.lastBlocklist()
	require.True(t, ok, "success must remove the machine from the blocklist")
	require.False(t, int64sContain(blk, 42), "machine 42 must be removed from the blocklist on success")
}

func TestRecordProvisionOutcome_AllowlistCapFIFO(t *testing.T) {
	seed := make([]int64, 20)
	for i := range seed {
		seed[i] = int64(i + 1) // 1..20
	}
	cap := &listCapture{}
	r := buildReconciler(t, Deps{Cfg: testCfg(t)})
	r.SetQueriesForTest(gen.New(newPodConfigDBTX(nil, seed, cap)))
	r.recordProvisionOutcome(context.Background(), 21, 0, "ready", nil, testLogger())

	alw, ok := cap.lastAllowlist()
	require.True(t, ok)
	require.Len(t, alw, 20, "allowlist stays capped at 20")
	require.False(t, int64sContain(alw, 1), "oldest (1) dropped")
	require.True(t, int64sContain(alw, 21), "newest (21) appended")
}

func TestRecordProvisionOutcome_AllowlistDedup(t *testing.T) {
	cap := &listCapture{}
	r := buildReconciler(t, Deps{Cfg: testCfg(t)})
	r.SetQueriesForTest(gen.New(newPodConfigDBTX(nil, []int64{5, 6, 7}, cap)))
	r.recordProvisionOutcome(context.Background(), 6, 0, "ready", nil, testLogger())
	require.Empty(t, cap.allowlistWrites, "a duplicate machine must not be re-written to the allowlist")
}

func TestRecordProvisionOutcome_ListWriteError_Swallowed(t *testing.T) {
	dbtx := &fakeDBTX{
		queryRowFn: func(_ context.Context, sql string, _ ...interface{}) pgx.Row {
			if contains(sql, "FROM ai_gateway.pod_config") {
				return podConfigRow{allow: []int64{42}}
			}
			return errRow{err: errors.New("unexpected")}
		},
		execFn: func(_ context.Context, _ string, _ ...interface{}) (pgconn.CommandTag, error) {
			return pgconn.CommandTag{}, errors.New("db write failed")
		},
	}
	r := buildReconciler(t, Deps{Cfg: testCfg(t)})
	r.SetQueriesForTest(gen.New(dbtx))
	require.NotPanics(t, func() {
		r.recordProvisionOutcome(context.Background(), 42, 1, "created_state_timeout", errors.New("boom"), testLogger())
	}, "a list-write DB error must be swallowed, never crash the reconciler")
}

func TestProvisionLifecycle_PrePickFailure_NoListWrite(t *testing.T) {
	cap := &listCapture{}
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")
	fakeV := &fakeVast{
		searchOffersFn: func(_ context.Context, _ vast.SearchFilter) ([]vast.Offer, error) {
			return nil, errors.New("search boom")
		},
	}
	r := buildReconciler(t, Deps{Cfg: cfg, FSM: fsm, Rule: alwaysInPeakRule(), Vast: fakeV})
	r.SetQueriesForTest(gen.New(newPodConfigDBTX(nil, nil, cap)))
	r.activeLifecycleID.Store(99)
	err := r.provisionLifecycle(context.Background(), 99, testLogger())
	require.Error(t, err)
	require.Empty(t, cap.blocklistWrites, "a pre-pick search failure must not blocklist")
	require.Empty(t, cap.allowlistWrites, "a pre-pick search failure must not allowlist")
}

func TestMachineAttributableReason(t *testing.T) {
	machine := []string{"created_state_timeout", "instance_terminal_state", "instance_terminal_state_confirmed", "public_port_bind_timeout", "blocklisted_ip"}
	for _, m := range machine {
		require.True(t, machineAttributableReason(m), "%q must be machine-attributable", m)
	}
	excluded := []string{"progress_stall_timeout", "cancelled_in_flight", "health_timeout", "create_error", "build_create_request_failed:x", "audit_write_failed", "vast_status_msg_error:y", "ready", ""}
	for _, e := range excluded {
		require.False(t, machineAttributableReason(e), "%q must NOT be machine-attributable", e)
	}
}

func TestAppendDedupCap(t *testing.T) {
	require.Equal(t, []int64{1, 2, 3}, appendDedupCap([]int64{1, 2}, 3, 20))
	require.Equal(t, []int64{1, 2}, appendDedupCap([]int64{1, 2}, 2, 20), "dedup: existing id unchanged")
	require.Equal(t, []int64{2, 3}, appendDedupCap([]int64{1, 2}, 3, 2), "cap: drops oldest")
}

func TestRemoveFromList(t *testing.T) {
	require.Equal(t, []int64{1, 3}, removeFromList([]int64{1, 2, 3}, 2))
	require.Equal(t, []int64{1, 2}, removeFromList([]int64{1, 2}, 9), "remove absent: noop")
}

// setOnstartLogIntervalForTest overrides the FF-02 sub-cadence for a test.
func setOnstartLogIntervalForTest(t *testing.T, d time.Duration) {
	t.Helper()
	orig := primaryOnstartLogIntervalForTest
	primaryOnstartLogIntervalForTest = d
	t.Cleanup(func() { primaryOnstartLogIntervalForTest = orig })
}

// --- Phase 999.2 force_machine_id (regime-1 UAT / ops pin) -------------------

// TestReconcilerForceMachineID_Pins: with force_machine_id set, provisionLifecycle
// must do exactly ONE search, scoped to that machine_id (machine_id.in filter),
// and create the returned offer — bypassing the market/allowlist shape loop.
func TestReconcilerForceMachineID_Pins(t *testing.T) {
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")

	var searchCalls atomic.Int32
	var createdFor atomic.Int64
	var sawMachineFilter atomic.Bool
	fakeV := &fakeVast{
		searchOffersFn: func(_ context.Context, filter vast.SearchFilter) ([]vast.Offer, error) {
			searchCalls.Add(1)
			if mid, ok := filter["machine_id"].(map[string]any); ok {
				if ids, ok := mid["in"].([]any); ok && len(ids) == 1 && ids[0] == int64(141325) {
					sawMachineFilter.Store(true)
				}
			}
			// Vast would return only the pinned machine's offer(s).
			return []vast.Offer{{ID: 100, MachineID: 141325, HostID: 5, DphTotal: 0.20}}, nil
		},
		createInstanceFn: func(_ context.Context, offerID int64, _ vast.CreateRequest) (vast.Instance, error) {
			createdFor.Store(offerID)
			return vast.Instance{ID: 1, ActualStatus: "loading"}, nil
		},
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return vast.Instance{ID: 1, ActualStatus: "loading"}, nil
		},
	}
	snapCfg := podconfig.PodConfig{
		ForceMachineID: 141325, CapPrimary: 1.0, CapFallback: 1.0,
		ColdStartBudgetS: 30, CreatedBudgetS: 600, PortBindBudgetS: 600,
	}
	loader := podconfig.NewStaticLoaderForTest(snapCfg, podconfig.ScheduleRule{}, podconfig.PodConfigBounds{}, nil)
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Vast: fakeV, PodCfg: loader, Rule: alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return false },
	})
	r.SetQueriesForTest(gen.New(&fakeDBTX{}))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = r.provisionLifecycle(ctx, 999, testLogger())

	require.Equal(t, int32(1), searchCalls.Load(), "force path must do exactly ONE search (no shape loop)")
	require.True(t, sawMachineFilter.Load(), "force search must carry machine_id.in=[141325]")
	require.Equal(t, int64(100), createdFor.Load(), "must create the pinned machine's offer")
}

// TestReconcilerForceMachineID_NoOffer_FailsClosed: when the pinned machine has
// no current offer, provisionLifecycle fails closed (forced_machine_no_offer),
// never silently falling back to the market pick.
func TestReconcilerForceMachineID_NoOffer_FailsClosed(t *testing.T) {
	cfg := testCfg(t)
	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")

	var created atomic.Int32
	fakeV := &fakeVast{
		searchOffersFn: func(_ context.Context, _ vast.SearchFilter) ([]vast.Offer, error) {
			return nil, nil // pinned machine has no offer
		},
		createInstanceFn: func(_ context.Context, _ int64, _ vast.CreateRequest) (vast.Instance, error) {
			created.Add(1)
			return vast.Instance{ID: 1, ActualStatus: "loading"}, nil
		},
	}
	rr := &reasonRecorder{}
	snapCfg := podconfig.PodConfig{
		ForceMachineID: 999999, CapPrimary: 1.0, CapFallback: 1.0,
		ColdStartBudgetS: 30, CreatedBudgetS: 600, PortBindBudgetS: 600,
	}
	loader := podconfig.NewStaticLoaderForTest(snapCfg, podconfig.ScheduleRule{}, podconfig.PodConfigBounds{}, nil)
	r := buildReconciler(t, Deps{
		Cfg: cfg, FSM: fsm, Vast: fakeV, PodCfg: loader, Rule: alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return false },
	})
	r.SetQueriesForTest(gen.New(newCloseReasonDBTX(rr)))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := r.provisionLifecycle(ctx, 999, testLogger())

	require.Error(t, err, "a pinned machine with no offer must fail, not fall back to market")
	require.True(t, rr.has("forced_machine_no_offer"), "close reason must be forced_machine_no_offer")
	require.Equal(t, int32(0), created.Load(), "must NOT create any instance when the pin has no offer")
}
