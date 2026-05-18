// Package dcgm (scraper.go): HTTP scraper for the pod-side dcgm-exporter
// :9400/metrics endpoint. Runs as a single goroutine; publishes the VRAM
// used (MiB) via atomic.Int64 for the FSM ticker to consume, and updates
// the gateway_vram_used_mib Prometheus gauge for dashboards.
//
// Fail-open semantics (CONTEXT.md D-A3 / RESEARCH.md Pitfall 1):
//
//   - 3 consecutive scrape failures flip vramUnknown=true. The FSM
//     interprets vramUnknown=true as "ignore VRAM signal", so the 2-of-3
//     saturation composite reduces to 1-of-2 over inflight + P95.
//   - A single successful scrape clears consecutiveFail and vramUnknown
//     atomically (recovery is immediate — no debounce needed because the
//     FSM's own arm-recover hysteresis already absorbs single-tick noise).
//   - Boot path: if DCGM_EXPORTER_URL is empty, main.go MUST NOT call
//     dcgm.New(); ReadMiB() on a nil receiver returns (0, true) so the
//     consumer code path is uniform whether the scraper exists or not
//     (Gate C — Wave 0 operator-confirmed fail-open contract).
//
// Unit note: DCGM_FI_DEV_FB_USED is always in MiB per NVIDIA's
// dcp-metrics-included.csv — never bytes. We do NOT convert; the gauge
// gateway_vram_used_mib is published in MiB to stay consistent with the
// upstream metric and the JSONB threshold field shed_vram_used_mib
// (RESEARCH Pitfall 1).
//
// Parser choice: prometheus/common's expfmt.TextParser (zero-value-ready)
// — not regex. The text exposition format has whitespace, comment, and
// label-set edge cases that a hand-rolled regex misses (RESEARCH §Don't
// Hand-Roll). v0.62.0 of prometheus/common has no `NewTextParser`
// constructor; instances are zero-initialised and reset on each call.
//
// Threat hardening (T-05-06, T-05-07):
//
//   - http.Client.Timeout bounds a single call (default 2s). Combined
//     with http.NewRequestWithContext, the parent ctx will also cancel
//     in-flight scrapes when the gateway shuts down (Run returns in <1s,
//     covered by TestScraper_RunStopsOnContextCancel).
//   - sanity check rejects vram values outside [0, 1_000_000] MiB.
//     Legitimate GPUs in 2026 do not exceed 1 TB VRAM; tampering or a
//     parsing glitch that synthesises 9999999999 cannot drive FSM=ON
//     permanently — the value is dropped and counter
//     gateway_dcgm_scrape_failures_total{reason="sanity_check"}
//     surfaces the event for ops.
package dcgm

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/common/expfmt"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// Sanity bounds for parsed VRAM value (MiB). <0 is impossible; >1_000_000
// (~1 TB VRAM) is far above any GPU shipping in 2026 — reject as
// tampering or a parsing glitch (T-05-07).
const (
	minValidVramMiB = 0
	maxValidVramMiB = 1_000_000
	failOpenAfterN  = 3

	// dcgmMetricName is the framebuffer-used gauge exposed by nvidia's
	// dcgm-exporter (https://github.com/NVIDIA/dcgm-exporter). The plan
	// only consumes this single series — other DCGM_FI_DEV_FB_* gauges
	// are ignored.
	dcgmMetricName = "DCGM_FI_DEV_FB_USED"
)

// Scraper periodically polls the pod's dcgm-exporter and publishes the
// VRAM-used signal for the FSM ticker to consume.
//
// Lifecycle: one process-wide instance, constructed in main.go after
// config load, started via `go scraper.Run(ctx)`. Stops when ctx
// cancels.
//
// Concurrency: ReadMiB is lockless (atomic loads only); scrape is
// invoked sequentially by the ticker so internal state mutations do not
// race with each other. Tests call scrape directly on the same goroutine
// to keep assertions deterministic.
//
// Phase 6.6 — primary pod is dynamic, so the scrape URL must change
// when primary transitions Asleep → Provisioning → Ready and
// Ready → Asleep. urlMu (sync.RWMutex) protects the url field per
// reviews suggestion #13 (Codex LOW): RWMutex chosen over
// atomic.Pointer[string] because it's less invasive — existing scrape()
// reads s.url as a plain field; switching to atomic.Pointer would force
// every read to .Load() everywhere, rippling through tests + other call
// sites. RWMutex preserves the read pattern (read lock around s.url
// access, write lock around SetURL).
type Scraper struct {
	urlMu    sync.RWMutex
	url      string
	client   *http.Client
	log      *slog.Logger
	interval time.Duration

	vramUsedMiB     atomic.Int64
	vramUnknown     atomic.Bool
	consecutiveFail atomic.Int32
}

// New creates a Scraper. url must be a full URL (e.g.
// http://pod-host:9400/metrics). interval is the tick period
// (default 5s if <= 0); timeout bounds a single HTTP call
// (default 2s if <= 0).
//
// If log is nil, slog.Default() is used.
func New(url string, interval, timeout time.Duration, log *slog.Logger) *Scraper {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &Scraper{
		url: url,
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				DisableKeepAlives: false,
				MaxIdleConns:      1,
				IdleConnTimeout:   60 * time.Second,
			},
		},
		log:      log.With("module", "DCGM"),
		interval: interval,
	}
}

// Run blocks until ctx cancellation, scraping on each tick. Intended to
// be invoked via `go scraper.Run(ctx)` from main.go. An immediate
// (synchronous) scrape is performed at boot so ReadMiB has a value
// before the first FSM tick.
func (s *Scraper) Run(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	s.scrape(ctx)
	for {
		select {
		case <-ctx.Done():
			s.log.Info("dcgm scraper stopping")
			return
		case <-t.C:
			s.scrape(ctx)
		}
	}
}

// SetURL replaces the scrape target URL at runtime.
//
// Phase 6.6 — the primary pod is dynamic (Vast.ai contract URL changes
// each provision cycle). primary.Reconciler.markReady calls SetURL(pod
// URL + ":9400/metrics") on transition StateProvisioning → StateReady,
// and calls SetURL("") on Ready → Asleep so scrape() short-circuits
// fail-open instead of hammering a dead host.
//
// Reviews suggestion #13 (Codex LOW, 2026-05-17): chose sync.RWMutex
// over atomic.Pointer[string] — less invasive, matches the existing
// scraper field-access pattern (plain field reads in scrape() rather
// than .Load() at every callsite).
//
// Concurrent-safe: the ticker goroutine calls scrape() (which read-locks
// urlMu) at most once per interval; SetURL takes the write lock so a
// concurrent scrape sees either the old or the new URL, never garbage.
// Empty url is valid and signals fail-open per Phase 5 design.
func (s *Scraper) SetURL(url string) {
	if s == nil {
		return
	}
	s.urlMu.Lock()
	defer s.urlMu.Unlock()
	s.url = url
}

// scrape performs one GET + parse cycle. All failures are non-fatal —
// they increment consecutiveFail and (after 3 in a row) flip vramUnknown.
// The goroutine is never killed by a scrape failure.
//
// Phase 6.6 — the URL is captured under urlMu.RLock at the start of
// each scrape so a concurrent SetURL never tears the request build.
// Empty URL → fail-open (Phase 5 design preserved): the scrape is
// skipped but no failure is counted, so vramUnknown stays whatever it
// was. This is the contract the primary lifecycle relies on while the
// pod is Asleep.
func (s *Scraper) scrape(ctx context.Context) {
	s.urlMu.RLock()
	url := s.url
	s.urlMu.RUnlock()
	if url == "" {
		// Fail-open by absence — caller (Phase 6.6 primary lifecycle)
		// SetURL("") to signal "no pod available, skip scrape". Do not
		// count this as a failure; vramUnknown stays at its prior value.
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		s.fail("request_build", err)
		return
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.fail("http_error", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		s.fail(fmt.Sprintf("status_%d", resp.StatusCode), nil)
		return
	}

	// TextParser zero value is ready to use (prometheus/common@v0.62.0
	// docs at expfmt/text_parse.go:52). Re-instantiated per scrape because
	// the parser is not safe for concurrent use; even though scrape is
	// serialised, a fresh parser keeps state pristine.
	var parser expfmt.TextParser
	families, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		s.fail("parse_error", err)
		return
	}

	fam, ok := families[dcgmMetricName]
	if !ok || len(fam.Metric) == 0 {
		s.fail("metric_missing", nil)
		return
	}

	m := fam.Metric[0]
	var val float64
	switch {
	case m.Gauge != nil:
		val = m.Gauge.GetValue()
	case m.Counter != nil:
		// Defensive: fixtures may model the metric as counter. dcgm-exporter
		// ships it as gauge in production but accept either to keep tests
		// flexible.
		val = m.Counter.GetValue()
	default:
		s.fail("metric_not_gauge", nil)
		return
	}

	if val < float64(minValidVramMiB) || val > float64(maxValidVramMiB) {
		s.fail("sanity_check", fmt.Errorf(
			"vram value %.1f MiB out of bounds [%d, %d]",
			val, minValidVramMiB, maxValidVramMiB,
		))
		return
	}

	s.vramUsedMiB.Store(int64(val))
	s.vramUnknown.Store(false)
	s.consecutiveFail.Store(0)
	obs.GatewayVramUsedMiB.Set(val)
	s.log.Debug("dcgm scrape ok", "vram_used_mib", val)
}

// fail bumps the consecutive-failure counter, emits the metric, logs,
// and trips fail-open once the 3-strike threshold is reached. The bool
// vramUnknown is monotone-rising while failures are consecutive; the
// next successful scrape resets it.
func (s *Scraper) fail(reason string, err error) {
	n := s.consecutiveFail.Add(1)
	obs.GatewayDcgmScrapeFailures.WithLabelValues(reason).Inc()
	if err != nil {
		s.log.Warn("dcgm scrape failed",
			"reason", reason, "err", err, "consecutive", n)
	} else {
		s.log.Warn("dcgm scrape failed",
			"reason", reason, "consecutive", n)
	}
	if n >= failOpenAfterN {
		s.vramUnknown.Store(true)
	}
}

// ReadMiB returns the most recent VRAM-used reading and an "unknown"
// flag. If unknown=true, the FSM must treat the VRAM signal as not
// contributing to the 2-of-3 saturation gate.
//
// Lockless — safe for hot path. nil receiver returns (0, true) so a
// gateway booted without DCGM_EXPORTER_URL behaves identically to one
// with the scraper running but in fail-open mode (Gate C — fail-open
// by absence).
func (s *Scraper) ReadMiB() (int64, bool) {
	if s == nil {
		return 0, true
	}
	return s.vramUsedMiB.Load(), s.vramUnknown.Load()
}
