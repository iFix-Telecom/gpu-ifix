// Package upstreams (probe.go): proactive synthetic E2E probes that drive
// the per-upstream circuit breakers without waiting for client traffic.
//
// Design (CONTEXT.md D-A2 + D-A4 + Plumbing / 03-05-PLAN must_haves):
//   - One Probe instance per gateway process; Run blocks until ctx is canceled.
//   - Ticker fires every cfg.Interval (default 10s). Each tick:
//   - context.WithTimeout(parent, cfg.Budget) shares a 5s deadline across siblings.
//   - Zero-value errgroup.Group{} — NOT errgroup.WithContext (Pitfall 3:
//     WithContext cancels siblings on first error, which would abort all
//     6 probes the instant the slowest one fails).
//   - Per-upstream goroutine ALWAYS returns nil from g.Go to prevent any
//     accidental cascade cancel even though we use the zero-value group.
//   - Tier-0 upstreams are ALWAYS probed; tier-1 externals are probed
//     ONLY when the same-role tier-0 breaker is OPEN or HALF_OPEN
//     (D-A2 — saves OpenRouter / OpenAI cost during steady state).
//   - Each probe drives the breaker via breaker.Set.Execute so the D-A4
//     IsSuccessful classification (4xx/429/Canceled NOT failures, 5xx +
//     timeouts ARE) applies uniformly across real traffic and probes.
//   - Result is enqueued to a buffered channel (size 100); a flushLoop
//     goroutine drains it every 1s and writes to upstreams.last_probe_*
//     via sqlc UpdateUpstreamProbe. The hot path NEVER blocks on DB
//     writeback — overflow is dropped with a counter-incremented metric.
//
// The audit/writer.go batched-channel pattern (lines 22-27, 102-169) is
// the closest analog in the repo and was the template for the flushLoop
// + non-blocking enqueue here.
package upstreams

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/errgroup"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/breaker"
	gen "github.com/ifixtelecom/gpu-ifix/gateway/internal/db/gen"
	"github.com/ifixtelecom/gpu-ifix/gateway/internal/obs"
)

// probeWAV is the embedded synthetic WAV fixture used for STT probes.
// 1-second 16-bit mono 16 kHz silence; ~32 KB. Versioned in the repo at
// gateway/internal/upstreams/testdata/probe.wav.
//
//go:embed testdata/probe.wav
var probeWAV []byte

const (
	defaultProbeInterval     = 10 * time.Second
	defaultProbeBudget       = 5 * time.Second
	probeUpdateBufferSize    = 100
	probeUpdateFlushInterval = 1 * time.Second
	probeDBWriteTimeout      = 2 * time.Second
)

// ProbeConfig controls the probe loop cadence and per-tick budget. Zero
// values fall back to the CONTEXT.md D-A2/D-A3 defaults (10s interval, 5s
// budget). Production wiring constructs this from
// cfg.ProbeIntervalSeconds + cfg.ProbeBudgetSeconds.
type ProbeConfig struct {
	Interval time.Duration // default 10s — tick cadence
	Budget   time.Duration // default 5s  — shared deadline across one tick's probes
}

// probeQueries isolates the sqlc surface so tests can stub the writeback
// without standing up Postgres. Mirrors the loader's loaderQueries pattern.
type probeQueries interface {
	UpdateUpstreamProbe(ctx context.Context, arg gen.UpdateUpstreamProbeParams) error
}

// Probe runs synthetic E2E probes on every Interval and mirrors the
// outcome to (a) the in-process breakers via breaker.Set.Execute and
// (b) the upstreams.last_probe_* columns via batched writeback.
type Probe struct {
	loader  *Loader
	breaker *breaker.Set
	q       probeQueries
	cfg     ProbeConfig
	log     *slog.Logger
	client  *http.Client

	updates chan gen.UpdateUpstreamProbeParams
	dropped atomic.Uint64 // observable via Dropped() for tests; multiple probeOne goroutines may call enqueueUpdate concurrently
	mu      sync.Mutex

	// flushWg waits for the writeback goroutine to drain on Run exit so
	// the final tick's probe results land in Postgres before the process
	// stops. Tests that observe DB rows after Run returns rely on this.
	flushWg sync.WaitGroup
}

// NewProbe constructs the probe. Call Run(ctx) in a goroutine at boot.
// q may be nil — in that case probe results still update the breakers
// but no DB writeback happens (useful for smoke tests).
func NewProbe(loader *Loader, bs *breaker.Set, q probeQueries, cfg ProbeConfig, log *slog.Logger) *Probe {
	if cfg.Interval <= 0 {
		cfg.Interval = defaultProbeInterval
	}
	if cfg.Budget <= 0 {
		cfg.Budget = defaultProbeBudget
	}
	return &Probe{
		loader:  loader,
		breaker: bs,
		q:       q,
		cfg:     cfg,
		log:     log.With("module", "PROBE"),
		client:  &http.Client{Timeout: cfg.Budget + 500*time.Millisecond},
		updates: make(chan gen.UpdateUpstreamProbeParams, probeUpdateBufferSize),
	}
}

// Run starts the ticker + writeback goroutines. Blocks until ctx is
// canceled. Returns after the writeback goroutine drains so callers can
// observe the final probe cycle's UPDATEs in Postgres.
func (p *Probe) Run(ctx context.Context) {
	p.flushWg.Add(1)
	go func() {
		defer p.flushWg.Done()
		p.flushLoop(ctx)
	}()
	tick := time.NewTicker(p.cfg.Interval)
	defer tick.Stop()
	// Kick off an immediate first probe — don't wait a full Interval at boot.
	p.doTick(ctx)
	for {
		select {
		case <-ctx.Done():
			p.flushWg.Wait()
			return
		case <-tick.C:
			p.doTick(ctx)
		}
	}
}

// Dropped returns the running count of probe writeback enqueues dropped
// because the buffer was full. Test hook only.
func (p *Probe) Dropped() uint64 {
	return p.dropped.Load()
}

// doTick dispatches all parallel probes for this cycle. Pitfall 3: uses
// zero-value errgroup.Group{} (NOT errgroup.WithContext) so one probe's
// failure does NOT cancel siblings. Shared 5s deadline lives on tickCtx.
func (p *Probe) doTick(parent context.Context) {
	tickCtx, cancel := context.WithTimeout(parent, p.cfg.Budget)
	defer cancel()

	all := p.loader.All()
	if len(all) == 0 {
		return
	}
	breakerSnap := p.breaker.Snapshot()

	// Decide which upstreams to probe this cycle:
	//   tier-0: always probed
	//   tier-1: only when the same-role tier-0 breaker is OPEN or HALF_OPEN
	//           (D-A2 — saves OpenRouter / OpenAI cost during steady state)
	tier0Closed := make(map[string]bool, 3) // role → is the tier-0 upstream's breaker CLOSED
	for _, u := range all {
		if u.Tier == 0 {
			tier0Closed[u.Role] = breakerSnap[u.Name] == "closed"
		}
	}

	var g errgroup.Group
	for _, u := range all {
		u := u
		if u.Tier == 1 && tier0Closed[u.Role] {
			continue // skip external probe on-demand
		}
		g.Go(func() error {
			p.probeOne(tickCtx, u)
			return nil // ALWAYS nil to prevent errgroup cascade cancel
		})
	}
	_ = g.Wait()
}

// probeOne runs a single synthetic E2E request. Drives the breaker via
// breaker.Set.Execute so the D-A4 IsSuccessful classification applies
// uniformly. On success/failure, enqueues a writeback event to the
// updates channel.
func (p *Probe) probeOne(ctx context.Context, u UpstreamConfig) {
	start := time.Now()
	_, err := p.breaker.Execute(u.Name, func() (*http.Response, error) {
		return p.dispatch(ctx, u)
	})
	dur := time.Since(start)
	obs.ProbeDurationMs.WithLabelValues(u.Name).Observe(float64(dur.Milliseconds()))

	var status, errMsg string
	switch {
	case err == nil:
		status = "ok"
	case ctx.Err() == context.DeadlineExceeded:
		status = "timeout"
		errMsg = "probe budget exceeded"
		obs.ProbeFailureTotal.WithLabelValues(u.Name, "timeout").Inc()
	default:
		status = "failed"
		errMsg = err.Error()
		obs.ProbeFailureTotal.WithLabelValues(u.Name, "error").Inc()
	}
	p.enqueueUpdate(u.Name, dur, status, errMsg)
}

// dispatch builds the role-appropriate synthetic request and issues it.
// 5xx responses produce *breaker.HTTPError so IsSuccessful counts them
// as failures; 4xx are wrapped too but treated as success by the
// breaker (D-A4 — client error, not upstream health).
func (p *Probe) dispatch(ctx context.Context, u UpstreamConfig) (*http.Response, error) {
	var (
		req *http.Request
		err error
	)
	switch u.Role {
	case "llm":
		body := []byte(`{"model":"qwen","messages":[{"role":"user","content":"ping"}],"max_tokens":1,"temperature":0}`)
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, u.URL+"/v1/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
	case "embed":
		body := []byte(`{"input":"ping","model":"probe-default"}`)
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, u.URL+"/v1/embeddings", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
	case "stt":
		buf := &bytes.Buffer{}
		mw := multipart.NewWriter(buf)
		if err := mw.WriteField("model", "whisper-1"); err != nil {
			return nil, err
		}
		fw, err := mw.CreateFormFile("file", "probe.wav")
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(probeWAV); err != nil {
			return nil, err
		}
		if err := mw.Close(); err != nil {
			return nil, err
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, u.URL+"/v1/audio/transcriptions", buf)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", mw.FormDataContentType())
	default:
		return nil, fmt.Errorf("probe: unknown role %q", u.Role)
	}
	if u.AuthBearer != "" {
		req.Header.Set("Authorization", "Bearer "+u.AuthBearer)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	// Drain then close the body so the underlying conn returns to the pool.
	// Without draining, net/http cannot reuse the connection even on 2xx
	// (MED-01: ~36 conn/min leak across 6 upstreams × 10s probe cadence).
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode >= 500 {
		return nil, &breaker.HTTPError{Status: resp.StatusCode, Msg: "probe upstream 5xx: " + strconv.Itoa(resp.StatusCode)}
	}
	if resp.StatusCode >= 400 {
		// 4xx wrapped in HTTPError — IsSuccessful treats this as success
		// (probe-config issue, not upstream health). The error is still
		// surfaced so the writeback records "failed" for ops visibility.
		return nil, &breaker.HTTPError{Status: resp.StatusCode, Msg: "probe 4xx: " + strconv.Itoa(resp.StatusCode)}
	}
	return resp, nil
}

// enqueueUpdate pushes a DB writeback event onto the buffered channel.
// Non-blocking: if the buffer is full the event is dropped and the
// dropped counter is incremented (visible via Dropped() in tests; future
// gateway_probe_update_dropped_total metric in obs/metrics.go can wrap).
func (p *Probe) enqueueUpdate(name string, dur time.Duration, status, errMsg string) {
	params := gen.UpdateUpstreamProbeParams{
		Name:            name,
		LastProbeAt:     pgtype.Timestamptz{Time: time.Now(), Valid: true},
		LastProbeMs:     pgtype.Int4{Int32: int32(dur.Milliseconds()), Valid: true},
		LastProbeStatus: pgtype.Text{String: status, Valid: true},
	}
	if errMsg != "" {
		params.LastProbeError = pgtype.Text{String: errMsg, Valid: true}
	}
	select {
	case p.updates <- params:
	default:
		p.dropped.Add(1)
	}
}

// flushLoop drains pending updates every probeUpdateFlushInterval (1s)
// or whenever ctx is canceled. Each UPDATE has a 2s timeout — the loop
// MUST NOT block on a slow DB write longer than that.
func (p *Probe) flushLoop(ctx context.Context) {
	tick := time.NewTicker(probeUpdateFlushInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			// Best-effort final drain on a fresh background context so
			// the final probe cycle's results land before exit.
			p.drain(context.Background())
			return
		case <-tick.C:
			p.drain(ctx)
		}
	}
}

// drain pulls everything currently buffered and writes it to Postgres.
// Returns immediately when the channel is empty (default branch in the
// inner select). q==nil is accepted — events are simply consumed.
func (p *Probe) drain(ctx context.Context) {
	for {
		select {
		case ev := <-p.updates:
			if p.q == nil {
				continue
			}
			dctx, cancel := context.WithTimeout(ctx, probeDBWriteTimeout)
			if err := p.q.UpdateUpstreamProbe(dctx, ev); err != nil {
				p.log.Warn("probe UPDATE failed", "upstream", ev.Name, "err", err)
			}
			cancel()
		default:
			return
		}
	}
}
