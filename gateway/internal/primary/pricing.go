// Package primary (pricing.go): quick-261001-qdd — pure real-cost ranking of
// Vast offers for the PRIMARY pod, with interruptible (bid) candidates and the
// bid -> on-demand fallback decision. No I/O; everything here is a pure
// function of its inputs so it is exhaustively unit-testable.
//
// Ported from ops/vast-3060/unified3060.py (real_cost / bid_price_for /
// choose_mode / rank_candidates), minus the cap_steps ladder (the Go
// reconciler already walks its own [primary, fallback] shape list), PLUS a
// download-amortization term the Python 3060 ranking does NOT have yet:
//
//	FATO (L4 benchmark 2026-10-01): each primary cold start downloads
//	~19.3 GB of weights from R2 and Vast hosts bill inet_down_cost US$/GB
//	(measured US$0.0267/GB -> ~US$0.51 per start; cheap 3090 hosts range
//	US$0.025-0.48 per start).
//
// Cost model (US$/h):
//
//	hourly    = dph_base (on-demand) | bid price (interruptible)
//	storageH  = storage_cost (US$/GB/month) * diskGB / 730
//	downloadH = inet_down_cost (US$/GB) * weightsGB / expectedHoursPerStart
//	CapCost   = hourly + storageH            <- the per-shape cap applies here
//	Total     = hourly + storageH + downloadH <- the ranking key
//
// Decision (documented): the cap keeps its historical meaning (what the
// rental costs per hour while it runs — GPU + disk), so an operator's cap
// edit is not silently re-interpreted; the one-off download charge only
// influences WHICH offer under the cap wins.
package primary

import (
	"math"
	"sort"
	"strings"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/emerg/vast"
)

const (
	// primaryDiskGB is the disk size requested for the primary pod (PUT
	// /asks/{id}/ `disk`). Also the GB used for the storage share of the
	// real cost.
	primaryDiskGB = 45
	// hoursPerMonth converts Vast storage_cost (US$/GB/month) to US$/h.
	hoursPerMonth = 730.0
	// capEpsilon mirrors vastutil.FilterBelowCap's tolerance so a
	// borderline offer is judged the same way it was before real cost.
	capEpsilon = 1e-4

	// OfferModeBid / OfferModeOnDemand are the two pod_config.offer_mode values.
	OfferModeBid      = "bid"
	OfferModeOnDemand = "ondemand"
)

// CostParams carries the non-offer inputs of the real-cost model.
type CostParams struct {
	DiskGB                float64 // storage share; primaryDiskGB in prod
	WeightsDownloadGB     float64 // GB pulled per cold start (default 20)
	ExpectedHoursPerStart float64 // amortization window; <= 0 disables the term
}

// Cost is the decomposed real cost of one candidate (US$/h).
type Cost struct {
	Hourly    float64 // dph_base / bid / dph_total fallback
	StorageH  float64
	DownloadH float64
	CapCost   float64 // Hourly + StorageH (compared to the cap)
	Total     float64 // CapCost + DownloadH (ranking key)
	Src       string  // "base+storage" | "dph_total-fallback" | "bid+storage"
}

// Candidate is one rankable rental option.
type Candidate struct {
	Offer vast.Offer
	IsBid bool
	Bid   float64 // bid price US$/h (0 for on-demand)
	Cost  Cost
}

// StorageHourly returns storage_cost * diskGB / 730 (0 when unknown).
func StorageHourly(o vast.Offer, diskGB float64) float64 {
	if o.StorageCost <= 0 || diskGB <= 0 {
		return 0
	}
	return o.StorageCost * diskGB / hoursPerMonth
}

// DownloadHourly amortizes the per-start weights download over the expected
// hours a lifecycle runs. 0 when the host does not publish inet_down_cost or
// the window is disabled.
func DownloadHourly(o vast.Offer, p CostParams) float64 {
	if o.InetDownCost <= 0 || p.WeightsDownloadGB <= 0 || p.ExpectedHoursPerStart <= 0 {
		return 0
	}
	return o.InetDownCost * p.WeightsDownloadGB / p.ExpectedHoursPerStart
}

// RealCost computes the decomposed cost of renting o. isBid=true uses bid as
// the hourly price. On-demand uses dph_base + storage; when Vast omits
// dph_base (0) it falls back to dph_total with no separate storage share
// (legacy fixtures / older API rows keep their exact historical ordering —
// never a 0 cost).
func RealCost(o vast.Offer, isBid bool, bid float64, p CostParams) Cost {
	var c Cost
	switch {
	case isBid:
		c.Hourly = bid
		c.StorageH = StorageHourly(o, p.DiskGB)
		c.Src = "bid+storage"
	case o.DphBase > 0:
		c.Hourly = o.DphBase
		c.StorageH = StorageHourly(o, p.DiskGB)
		c.Src = "base+storage"
	default:
		c.Hourly = o.DphTotal
		c.Src = "dph_total-fallback"
	}
	c.DownloadH = DownloadHourly(o, p)
	c.CapCost = c.Hourly + c.StorageH
	c.Total = c.CapCost + c.DownloadH
	return c
}

// round4 rounds to 4 decimal places (Vast bid price granularity used by the
// 3060 pod's vast-cli path).
func round4(v float64) float64 { return math.Round(v*1e4) / 1e4 }

// BidPriceFor returns the bid price for o: min_bid * margin rounded to 4
// places, lowered so bid + storageH <= capTotal when needed. ok=false when the
// offer has no min_bid (<= 0) or the cap-fitting bid would fall below min_bid
// (Vast would reject / instantly outbid it).
func BidPriceFor(o vast.Offer, margin, capTotal, storageH float64) (float64, bool) {
	if o.MinBid <= 0 {
		return 0, false
	}
	if margin < 1 {
		margin = 1
	}
	bid := round4(o.MinBid * margin)
	if capTotal > 0 && bid+storageH > capTotal+1e-12 {
		bid = round4(capTotal - storageH)
		if bid+storageH > capTotal+1e-12 {
			bid = round4(bid - 0.0001)
		}
	}
	if bid < o.MinBid-1e-12 {
		return 0, false
	}
	return bid, true
}

// RankCandidates builds on-demand candidates (always) and bid candidates
// (only when mode == "bid") whose CapCost fits cap, and returns the one with
// the lowest Total. Ties (to 1e-9) go to on-demand, then to the input order
// (stable — server order dph_total asc). ok=false when nothing fits.
func RankCandidates(ondemand, bid []vast.Offer, mode string, cap, margin float64, p CostParams) (Candidate, bool) {
	cands := make([]Candidate, 0, len(ondemand)+len(bid))
	for _, o := range ondemand {
		c := RealCost(o, false, 0, p)
		if c.CapCost > cap+capEpsilon {
			continue
		}
		cands = append(cands, Candidate{Offer: o, Cost: c})
	}
	if mode == OfferModeBid {
		for _, o := range bid {
			sh := StorageHourly(o, p.DiskGB)
			b, ok := BidPriceFor(o, margin, cap, sh)
			if !ok {
				continue
			}
			c := RealCost(o, true, b, p)
			if c.CapCost > cap+capEpsilon {
				continue
			}
			cands = append(cands, Candidate{Offer: o, IsBid: true, Bid: b, Cost: c})
		}
	}
	if len(cands) == 0 {
		return Candidate{}, false
	}
	sort.SliceStable(cands, func(i, j int) bool {
		ti, tj := math.Round(cands[i].Cost.Total*1e9), math.Round(cands[j].Cost.Total*1e9)
		if ti != tj {
			return ti < tj
		}
		return !cands[i].IsBid && cands[j].IsBid
	})
	return cands[0], true
}

// NormalizeOfferMode maps a configured mode to "bid" | "ondemand". Accepts
// "on-demand"/"on_demand" spellings; anything else (incl. "") -> "bid", the
// Pedro 2026-10-01 default.
func NormalizeOfferMode(mode string) string {
	m := strings.ToLower(strings.TrimSpace(mode))
	m = strings.NewReplacer("-", "", "_", "").Replace(m)
	if m == OfferModeOnDemand {
		return OfferModeOnDemand
	}
	return OfferModeBid
}

// ChooseMode returns the effective offer mode for this provision. An explicit
// "ondemand" config is always honored. In "bid" mode, reaching
// maxPreemptions preempted lifecycles today flips to on-demand for the rest of
// the day (T-qdd-03 — a hostile bid market cannot loop cold starts forever).
// maxPreemptions <= 0 DISABLES that fallback: the configured mode is always
// honored (documented divergence from the 3060 Python, where 0 meant
// "always on-demand" — use offer_mode='ondemand' for that).
func ChooseMode(cfgMode string, preemptToday int64, maxPreemptions int) string {
	m := NormalizeOfferMode(cfgMode)
	if m == OfferModeOnDemand {
		return OfferModeOnDemand
	}
	if maxPreemptions > 0 && preemptToday >= int64(maxPreemptions) {
		return OfferModeOnDemand
	}
	return OfferModeBid
}
