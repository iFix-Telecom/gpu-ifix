package primary

// quick-261001-qdd — reconciler wiring of real-cost ranking + bid selection +
// on-demand fallback (provisionLifecycle). The fake Vast branches on
// filter["type"] ("on-demand" | "bid").

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
)

type bidProbeResult struct {
	createdOffer int64
	req          vast.CreateRequest
	searchTypes  []string
	offerModeSet bool
	isBidArg     pgtype.Bool
	bidPriceArg  pgtype.Numeric
	acceptedDph  pgtype.Numeric
	activeIsBid  bool
}

// runBidProbe drives provisionLifecycle once with scripted on-demand / bid
// offers and a scripted preempted-today count, and captures the create
// request + the audit writes.
func runBidProbe(t *testing.T, mode string, preemptToday int64, od, bd []vast.Offer, bidErr error) bidProbeResult {
	t.Helper()
	cfg := testCfg(t)
	cfg.PrimaryVastMachineAllowlist = nil
	cfg.PrimaryVastMachineBlocklist = nil
	cfg.PrimaryVastRejectPrivateIP = false
	cfg.PrimaryVastPriceCapPrimary = 0.20
	cfg.PrimaryVastPriceCapFallback = 0.20
	cfg.PrimaryVastOfferMode = mode
	cfg.PrimaryVastBidMargin = 1.15
	cfg.PrimaryVastMaxPreemptionsPerDay = 2
	cfg.PrimaryWeightsDownloadGB = 20
	cfg.PrimaryExpectedHoursPerStart = 8

	fsm := NewFSM(nil, nil)
	_ = fsm.Transition(StateAsleep, StateProvisioning, time.Now(), "test")

	var (
		mu  sync.Mutex
		res bidProbeResult
	)
	created := atomic.Bool{}
	fakeV := &fakeVast{
		searchOffersFn: func(_ context.Context, f vast.SearchFilter) ([]vast.Offer, error) {
			typ, _ := f["type"].(string)
			mu.Lock()
			res.searchTypes = append(res.searchTypes, typ)
			mu.Unlock()
			switch typ {
			case "on-demand":
				require.Contains(t, f, "dph_total", "on-demand search keeps the server-side cap pre-filter")
				return od, nil
			case "bid":
				require.NotContains(t, f, "dph_total", "bid search must not carry the dph_total ceiling")
				if bidErr != nil {
					return nil, bidErr
				}
				return bd, nil
			}
			return nil, errors.New("search without type")
		},
		createInstanceFn: func(_ context.Context, offerID int64, req vast.CreateRequest) (vast.Instance, error) {
			mu.Lock()
			res.createdOffer = offerID
			res.req = req
			mu.Unlock()
			created.Store(true)
			return vast.Instance{ID: 4242, ActualStatus: "loading"}, nil
		},
		getInstanceFn: func(_ context.Context, _ int64) (vast.Instance, error) {
			return vast.Instance{ID: 4242, ActualStatus: "loading"}, nil
		},
	}
	dbtx := &fakeDBTX{
		queryRowFn: func(_ context.Context, sql string, _ ...interface{}) pgx.Row {
			if strings.Contains(sql, "shutdown_reason = 'preempted'") {
				return countRow{n: preemptToday}
			}
			if strings.Contains(sql, "fail_streak") {
				return countRow{n: 0}
			}
			return errRow{err: errors.New("unscripted")}
		},
		execFn: func(_ context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
			mu.Lock()
			defer mu.Unlock()
			if strings.Contains(sql, "SetPrimaryLifecycleOfferMode") {
				res.offerModeSet = true
				res.isBidArg, _ = args[1].(pgtype.Bool)
				res.bidPriceArg, _ = args[2].(pgtype.Numeric)
			}
			if strings.Contains(sql, "UpdatePrimaryLifecycleVastIDs") {
				res.acceptedDph, _ = args[3].(pgtype.Numeric)
			}
			return pgconn.NewCommandTag("UPDATE 1"), nil
		},
	}
	r := buildReconciler(t, Deps{
		Cfg:         cfg,
		FSM:         fsm,
		Vast:        fakeV,
		Rule:        alwaysInPeakRule(),
		HealthCheck: func(_ context.Context, _ string) bool { return false },
	})
	r.SetQueriesForTest(gen.New(dbtx))

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_ = r.provisionLifecycle(ctx, 999, testLogger())
	require.True(t, created.Load(), "CreateInstance must have been called")
	mu.Lock()
	defer mu.Unlock()
	out := res
	out.activeIsBid = r.activeIsBid.Load()
	return out
}

func numF(t *testing.T, n pgtype.Numeric) float64 {
	t.Helper()
	f, err := n.Float64Value()
	require.NoError(t, err)
	return f.Float64
}

func TestProvision_BidCheaper_SendsPrice(t *testing.T) {
	od := []vast.Offer{{ID: 1, MachineID: 11, DphTotal: 0.19, DphBase: 0.18, StorageCost: 0.1}}
	bd := []vast.Offer{{ID: 2, MachineID: 12, DphTotal: 0.19, DphBase: 0.18, StorageCost: 0.1, MinBid: 0.08}}
	res := runBidProbe(t, OfferModeBid, 0, od, bd, nil)

	require.Equal(t, int64(2), res.createdOffer)
	require.NotNil(t, res.req.Price, "bid winner must send price")
	require.InDelta(t, 0.092, *res.req.Price, 1e-9)
	require.True(t, res.offerModeSet)
	require.True(t, res.isBidArg.Valid && res.isBidArg.Bool)
	require.InDelta(t, 0.092, numF(t, res.bidPriceArg), 1e-9)
	// accepted_dph for a bid lifecycle = bid + storage share.
	require.InDelta(t, 0.092+0.1*45/730, numF(t, res.acceptedDph), 1e-4) // numeric stored at 4dp
	require.True(t, res.activeIsBid)
	require.Equal(t, []string{"on-demand", "bid"}, res.searchTypes)
}

// TestProvision_ArgentinaLike_RealCostBeatsDphTotalOrder — the server returns
// the offers ordered by dph_total asc; the second has the lower REAL cost and
// is picked; on-demand → no price.
func TestProvision_ArgentinaLike_RealCostBeatsDphTotalOrder(t *testing.T) {
	od := []vast.Offer{
		{ID: 10, MachineID: 110, DphTotal: 0.1633, DphBase: 0.155, StorageCost: 0.5},
		{ID: 11, MachineID: 111, DphTotal: 0.1700, DphBase: 0.130, StorageCost: 0.1},
	}
	res := runBidProbe(t, OfferModeBid, 0, od, nil, nil)
	require.Equal(t, int64(11), res.createdOffer)
	require.Nil(t, res.req.Price)
	require.True(t, res.offerModeSet)
	require.True(t, res.isBidArg.Valid)
	require.False(t, res.isBidArg.Bool)
	require.False(t, res.bidPriceArg.Valid, "on-demand bid_price is NULL")
	// on-demand keeps the historical accepted_dph = dph_total.
	require.InDelta(t, 0.17, numF(t, res.acceptedDph), 1e-9)
	require.False(t, res.activeIsBid)
}

func TestProvision_NoEligibleBid_FallsBackToOnDemand(t *testing.T) {
	od := []vast.Offer{{ID: 1, MachineID: 11, DphTotal: 0.19, DphBase: 0.18, StorageCost: 0.1}}
	// min_bid too high for the cap → bid ineligible.
	bd := []vast.Offer{{ID: 2, MachineID: 12, StorageCost: 0.1, MinBid: 0.25}}
	res := runBidProbe(t, OfferModeBid, 0, od, bd, nil)
	require.Equal(t, int64(1), res.createdOffer)
	require.Nil(t, res.req.Price)
}

func TestProvision_BidSearchError_DegradesToOnDemand(t *testing.T) {
	od := []vast.Offer{{ID: 1, MachineID: 11, DphTotal: 0.19, DphBase: 0.18, StorageCost: 0.1}}
	res := runBidProbe(t, OfferModeBid, 0, od, nil, errors.New("vast 500"))
	require.Equal(t, int64(1), res.createdOffer)
	require.Nil(t, res.req.Price)
}

func TestProvision_PreemptLimitReached_NoBidSearch(t *testing.T) {
	od := []vast.Offer{{ID: 1, MachineID: 11, DphTotal: 0.19, DphBase: 0.18, StorageCost: 0.1}}
	bd := []vast.Offer{{ID: 2, MachineID: 12, StorageCost: 0.1, MinBid: 0.05}}
	res := runBidProbe(t, OfferModeBid, 2, od, bd, nil)
	require.Equal(t, int64(1), res.createdOffer)
	require.Nil(t, res.req.Price)
	require.Equal(t, []string{"on-demand"}, res.searchTypes, "no bid search once max_preemptions_per_day is reached")
}

func TestProvision_OnDemandMode_NoBidSearch(t *testing.T) {
	od := []vast.Offer{{ID: 1, MachineID: 11, DphTotal: 0.19, DphBase: 0.18, StorageCost: 0.1}}
	bd := []vast.Offer{{ID: 2, MachineID: 12, StorageCost: 0.1, MinBid: 0.05}}
	res := runBidProbe(t, OfferModeOnDemand, 0, od, bd, nil)
	require.Equal(t, int64(1), res.createdOffer)
	require.Equal(t, []string{"on-demand"}, res.searchTypes)
}

func TestPrimaryFilter_CopiesAndSpecialises(t *testing.T) {
	base := vast.DefaultSearchFilter(0.2, 0, "RTX 3090", 1)
	od := primaryFilter(base, "on-demand", 0.95)
	bd := primaryFilter(base, "bid", 0.95)
	require.Equal(t, "on-demand", od["type"])
	require.Equal(t, 64, od["limit"])
	require.Contains(t, od, "dph_total")
	require.Equal(t, "bid", bd["type"])
	require.NotContains(t, bd, "dph_total")
	require.Equal(t, map[string]any{"gte": 0.95}, od["reliability"])
	require.Equal(t, map[string]any{"gte": 0.95}, bd["reliability"])
	// Invalid / unset floor falls back to the 0.95 default.
	require.Equal(t, map[string]any{"gte": 0.95}, primaryFilter(base, "on-demand", 0)["reliability"])
	require.Equal(t, map[string]any{"gte": 0.97}, primaryFilter(base, "on-demand", 0.97)["reliability"])
	// Shared filter untouched (emerg uses DefaultSearchFilter too).
	require.Equal(t, map[string]any{"gte": 0.99}, base["reliability"], "emerg/shared filter keeps 0.99")
	require.NotContains(t, base, "type")
	require.Equal(t, 20, base["limit"])
	require.Contains(t, base, "dph_total")
}

// TestFilterMinReliability — Pedro 2026-10-01: primary floor 0.95. A 0.96
// offer passes, a 0.94 offer is rejected; Reliability 0 (absent) is kept.
func TestFilterMinReliability(t *testing.T) {
	in := []vast.Offer{{ID: 1, Reliability: 0.96}, {ID: 2, Reliability: 0.94}, {ID: 3, Reliability: 0}, {ID: 4, Reliability: 0.95}}
	got := filterMinReliability(in, 0.95)
	ids := make([]int64, 0, len(got))
	for _, o := range got {
		ids = append(ids, o.ID)
	}
	require.Equal(t, []int64{1, 3, 4}, ids)
}

// TestProvision_MinReliability_RejectsBelowFloor — end to end through
// provisionLifecycle: the server filter carries gte 0.95 and a 0.94 offer the
// (fake) server still returns is dropped client-side; the 0.96 one is rented.
func TestProvision_MinReliability_RejectsBelowFloor(t *testing.T) {
	od := []vast.Offer{
		{ID: 30, MachineID: 130, DphTotal: 0.15, DphBase: 0.12, StorageCost: 0.1, Reliability: 0.94},
		{ID: 31, MachineID: 131, DphTotal: 0.17, DphBase: 0.16, StorageCost: 0.1, Reliability: 0.96},
	}
	res := runBidProbe(t, OfferModeOnDemand, 0, od, nil, nil)
	require.Equal(t, int64(31), res.createdOffer, "0.94 must be rejected, 0.96 accepted")
}
