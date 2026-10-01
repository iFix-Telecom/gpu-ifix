package primary

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
)

// quick-261001-qdd — pure real-cost / bid / mode tests.

var testCostParams = CostParams{DiskGB: primaryDiskGB, WeightsDownloadGB: 20, ExpectedHoursPerStart: 8}

func TestRealCost_OnDemandBasePlusStorage(t *testing.T) {
	o := vast.Offer{DphBase: 0.1466, StorageCost: 0.27, DphTotal: 0.16}
	c := RealCost(o, false, 0, CostParams{DiskGB: 45})
	storageH := 0.27 * 45 / 730
	require.InDelta(t, 0.1466, c.Hourly, 1e-12)
	require.InDelta(t, storageH, c.StorageH, 1e-12)
	require.InDelta(t, 0.1466+storageH, c.CapCost, 1e-12)
	require.InDelta(t, 0.1466+storageH, c.Total, 1e-12)
	require.Equal(t, "base+storage", c.Src)
}

func TestRealCost_NoDphBaseFallsBackToDphTotal(t *testing.T) {
	o := vast.Offer{DphTotal: 0.21, StorageCost: 0.3}
	c := RealCost(o, false, 0, CostParams{DiskGB: 45})
	require.InDelta(t, 0.21, c.Hourly, 1e-12)
	require.Zero(t, c.StorageH)
	require.InDelta(t, 0.21, c.Total, 1e-12)
	require.Equal(t, "dph_total-fallback", c.Src)
}

func TestRealCost_BidUsesBidPlusStorage(t *testing.T) {
	o := vast.Offer{DphBase: 0.2, StorageCost: 0.146, MinBid: 0.08}
	c := RealCost(o, true, 0.092, CostParams{DiskGB: 45})
	require.InDelta(t, 0.092, c.Hourly, 1e-12)
	require.InDelta(t, 0.146*45/730, c.StorageH, 1e-12)
	require.Equal(t, "bid+storage", c.Src)
}

// TestRealCost_DownloadAmortization — FATO L4 benchmark: US$0.0267/GB × 20 GB
// / 8 h = US$0.06675/h, added to Total but NOT to CapCost.
func TestRealCost_DownloadAmortization(t *testing.T) {
	o := vast.Offer{DphBase: 0.15, StorageCost: 0.1, InetDownCost: 0.0267}
	c := RealCost(o, false, 0, testCostParams)
	wantDl := 0.0267 * 20 / 8
	require.InDelta(t, wantDl, c.DownloadH, 1e-12)
	require.InDelta(t, 0.15+0.1*45/730, c.CapCost, 1e-12)
	require.InDelta(t, c.CapCost+wantDl, c.Total, 1e-12)

	// Disabled window / missing price → 0.
	require.Zero(t, DownloadHourly(o, CostParams{WeightsDownloadGB: 20, ExpectedHoursPerStart: 0}))
	require.Zero(t, DownloadHourly(vast.Offer{}, testCostParams))
}

func TestBidPriceFor(t *testing.T) {
	sh := 0.1 * 45 / 730
	// Plain margin.
	b, ok := BidPriceFor(vast.Offer{MinBid: 0.10}, 1.15, 0.30, sh)
	require.True(t, ok)
	require.InDelta(t, 0.115, b, 1e-12)

	// Over the cap → lowered to fit cap - storage (still >= min_bid).
	b, ok = BidPriceFor(vast.Offer{MinBid: 0.10}, 1.15, 0.11, sh)
	require.True(t, ok)
	require.LessOrEqual(t, b+sh, 0.11+1e-12)
	require.GreaterOrEqual(t, b, 0.10)

	// Cap below min_bid + storage → ineligible.
	_, ok = BidPriceFor(vast.Offer{MinBid: 0.10}, 1.15, 0.10, sh)
	require.False(t, ok)

	// No min_bid → ineligible.
	_, ok = BidPriceFor(vast.Offer{MinBid: 0}, 1.15, 0.30, sh)
	require.False(t, ok)
}

func TestRankCandidates_BidCheaperWins(t *testing.T) {
	od := []vast.Offer{{ID: 1, DphBase: 0.18, StorageCost: 0.1, DphTotal: 0.19}}
	bd := []vast.Offer{{ID: 2, DphBase: 0.18, StorageCost: 0.1, MinBid: 0.08}}
	c, ok := RankCandidates(od, bd, OfferModeBid, 0.2, 1.15, testCostParams)
	require.True(t, ok)
	require.True(t, c.IsBid)
	require.Equal(t, int64(2), c.Offer.ID)
	require.InDelta(t, 0.092, c.Bid, 1e-12)
}

// TestRankCandidates_ArgentinaLike — the on-demand offer with a HIGHER
// server-sorted dph_total but LOWER real cost (dph_base + 45 GB storage)
// wins: ranking is by real cost, not by dph_total order.
func TestRankCandidates_ArgentinaLike(t *testing.T) {
	serbia := vast.Offer{ID: 10, DphTotal: 0.1633, DphBase: 0.155, StorageCost: 0.5} // 0.155 + 0.0308 = 0.1858
	argentina := vast.Offer{ID: 11, DphTotal: 0.170, DphBase: 0.130, StorageCost: 0.1} // 0.130 + 0.0062 = 0.1362
	c, ok := RankCandidates([]vast.Offer{serbia, argentina}, nil, OfferModeBid, 0.2, 1.15, testCostParams)
	require.True(t, ok)
	require.False(t, c.IsBid)
	require.Equal(t, int64(11), c.Offer.ID)
}

// TestRankCandidates_DownloadChangesWinner — two hosts with equal GPU+storage;
// the one with an expensive inet_down_cost loses on Total.
func TestRankCandidates_DownloadChangesWinner(t *testing.T) {
	pricey := vast.Offer{ID: 20, DphBase: 0.14, StorageCost: 0.1, InetDownCost: 0.024}  // +0.06/h
	cheapDL := vast.Offer{ID: 21, DphBase: 0.16, StorageCost: 0.1, InetDownCost: 0.001} // +0.0025/h
	c, ok := RankCandidates([]vast.Offer{pricey, cheapDL}, nil, OfferModeOnDemand, 0.2, 1.15, testCostParams)
	require.True(t, ok)
	require.Equal(t, int64(21), c.Offer.ID)

	// Cap is on GPU+storage only: an offer whose download share pushes Total
	// over the cap is still ELIGIBLE (cap semantics unchanged).
	onlyPricey := vast.Offer{ID: 22, DphBase: 0.19, StorageCost: 0.1, InetDownCost: 0.05} // Total ≈ 0.32
	c, ok = RankCandidates([]vast.Offer{onlyPricey}, nil, OfferModeOnDemand, 0.2, 1.15, testCostParams)
	require.True(t, ok)
	require.Equal(t, int64(22), c.Offer.ID)
	require.Greater(t, c.Cost.Total, 0.2)
}

func TestRankCandidates_TieGoesToOnDemand(t *testing.T) {
	// On-demand base 0.115 == bid 0.10*1.15; same storage.
	od := []vast.Offer{{ID: 1, DphBase: 0.115, StorageCost: 0.1}}
	bd := []vast.Offer{{ID: 2, StorageCost: 0.1, MinBid: 0.10}}
	c, ok := RankCandidates(od, bd, OfferModeBid, 0.3, 1.15, testCostParams)
	require.True(t, ok)
	require.False(t, c.IsBid)
	require.Equal(t, int64(1), c.Offer.ID)
}

func TestRankCandidates_OnDemandModeIgnoresBid(t *testing.T) {
	od := []vast.Offer{{ID: 1, DphBase: 0.18, StorageCost: 0.1}}
	bd := []vast.Offer{{ID: 2, StorageCost: 0.1, MinBid: 0.05}}
	c, ok := RankCandidates(od, bd, OfferModeOnDemand, 0.3, 1.15, testCostParams)
	require.True(t, ok)
	require.False(t, c.IsBid)
}

func TestRankCandidates_CapExcludesAndEmpty(t *testing.T) {
	od := []vast.Offer{{ID: 1, DphBase: 0.25, StorageCost: 0.1}}
	bd := []vast.Offer{{ID: 2, StorageCost: 0.1, MinBid: 0.25}}
	_, ok := RankCandidates(od, bd, OfferModeBid, 0.2, 1.15, testCostParams)
	require.False(t, ok)
	_, ok = RankCandidates(nil, nil, OfferModeBid, 0.2, 1.15, testCostParams)
	require.False(t, ok)
}

// TestRankCandidates_LegacyFixtureOrderPreserved — offers without
// dph_base/min_bid keep the historical pick (first of server order under cap).
func TestRankCandidates_LegacyFixtureOrderPreserved(t *testing.T) {
	od := []vast.Offer{{ID: 1, DphTotal: 0.25}, {ID: 2, DphTotal: 0.25}, {ID: 3, DphTotal: 0.5}}
	c, ok := RankCandidates(od, od, OfferModeBid, 0.3, 1.15, testCostParams)
	require.True(t, ok)
	require.Equal(t, int64(1), c.Offer.ID)
	require.False(t, c.IsBid)
}

func TestChooseMode(t *testing.T) {
	require.Equal(t, OfferModeOnDemand, ChooseMode("bid", 2, 2))
	require.Equal(t, OfferModeBid, ChooseMode("bid", 1, 2))
	require.Equal(t, OfferModeOnDemand, ChooseMode("ondemand", 0, 2))
	require.Equal(t, OfferModeOnDemand, ChooseMode("on-demand", 0, 2))
	require.Equal(t, OfferModeBid, ChooseMode("garbage", 0, 2))
	require.Equal(t, OfferModeBid, ChooseMode("", 0, 2))
	// max <= 0 disables the preemption fallback.
	require.Equal(t, OfferModeBid, ChooseMode("bid", 50, 0))
	require.Equal(t, OfferModeOnDemand, ChooseMode("ondemand", 0, 0))
}
