package billing

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// RequestUsage is the per-request atomic counter populated by the SSE
// interceptor (proxy/interceptor_usage.go). Fields are atomic so the
// interceptor goroutine can write while the main response handler reads
// at flush time without locking.
//
// AudioSecondsMs10 is audio_seconds × 10 (decisecond precision) so the
// counter stays integer-typed. Convert to float64 at flush time via
// float64(v) / 10.0.
//
// model is the resolved model name captured from the SSE/JSON frame
// (BL-01 extension). Access via Model()/SetModel() — concurrent-safe via
// atomic.Value.
//
// createdAtUnixNano is the wall-clock time the slot was registered via
// Accountant.Set. Consumed by the reaper goroutine (ME-03) to evict
// slots older than the TTL in case the interceptor Close path never
// ran (client abort pre-header, upstream cut without teardown, etc.).
type RequestUsage struct {
	TokensIn         atomic.Int64
	TokensOut        atomic.Int64
	AudioSecondsMs10 atomic.Int64
	EmbedsCount      atomic.Int64
	// Phase 23 (23-10): prompt_tokens_details.cached_tokens /
	// cache_write_tokens do usage OpenAI-shaped (passo ① traduzido e
	// OpenRouter). Subconjuntos de TokensIn, gravados em
	// billing_events.tokens_cache_read / tokens_cache_write.
	TokensCacheRead  atomic.Int64
	TokensCacheWrite atomic.Int64
	// reportedCostUSD é o usage.cost (US$) que o OpenRouter devolve por
	// request; nil quando o upstream não reportou custo.
	reportedCostUSD   atomic.Pointer[float64]
	model             atomic.Value // string
	createdAtUnixNano atomic.Int64
}

// SetReportedCostUSD guarda o custo reportado pelo provedor (usage.cost).
func (u *RequestUsage) SetReportedCostUSD(v float64) {
	if u == nil {
		return
	}
	u.reportedCostUSD.Store(&v)
}

// ReportedCostUSD devolve o custo reportado pelo provedor e se ele existe.
func (u *RequestUsage) ReportedCostUSD() (float64, bool) {
	if u == nil {
		return 0, false
	}
	p := u.reportedCostUSD.Load()
	if p == nil {
		return 0, false
	}
	return *p, true
}

// Model returns the cached model name, or "" when none was set.
func (u *RequestUsage) Model() string {
	if u == nil {
		return ""
	}
	if v, ok := u.model.Load().(string); ok {
		return v
	}
	return ""
}

// SetModel stores the model name atomically. Idempotent — later writes
// overwrite earlier ones; most upstreams emit the model in every frame,
// so the last frame wins (they agree on the value).
func (u *RequestUsage) SetModel(name string) {
	if u == nil || name == "" {
		return
	}
	u.model.Store(name)
}

// Accountant holds the per-request usage counters keyed by request_id.
// Copy-on-write map — one writer (Set/Delete) at a time via mu, readers
// (Get) are lock-free via atomic.Pointer. Mirrors proxy/toolcall.go
// flag-map pattern.
type Accountant struct {
	mu     sync.Mutex
	usages atomic.Pointer[map[string]*RequestUsage]
}

// NewAccountant builds an Accountant with an empty map already stored. Safe
// to call Get immediately after construction.
func NewAccountant() *Accountant {
	a := &Accountant{}
	empty := make(map[string]*RequestUsage)
	a.usages.Store(&empty)
	return a
}

// Set creates a per-request usage slot. Caller (interceptor.Intercept)
// calls this at the start of streaming. Idempotent: if reqID is already
// registered the new pointer replaces the old one.
//
// ME-03 fix: Set stamps RequestUsage.createdAtUnixNano so the reaper
// (RunReaper below) can evict slots whose Close path never fired
// (client abort pre-header, upstream cut without teardown).
func (a *Accountant) Set(reqID string, u *RequestUsage) {
	if u != nil {
		u.createdAtUnixNano.Store(time.Now().UnixNano())
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	old := *a.usages.Load()
	next := make(map[string]*RequestUsage, len(old)+1)
	for k, v := range old {
		next[k] = v
	}
	next[reqID] = u
	a.usages.Store(&next)
}

// Get returns the per-request usage slot, or nil if none was Set.
// Lock-free; safe to call from response handlers.
func (a *Accountant) Get(reqID string) *RequestUsage {
	m := *a.usages.Load()
	return m[reqID]
}

// Delete removes a per-request slot at flush time. Best-effort cleanup —
// forgetting to call leaks ~32 bytes per request_id.
func (a *Accountant) Delete(reqID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	old := *a.usages.Load()
	if _, ok := old[reqID]; !ok {
		return
	}
	next := make(map[string]*RequestUsage, len(old))
	for k, v := range old {
		if k != reqID {
			next[k] = v
		}
	}
	a.usages.Store(&next)
}

// DefaultReapTTL is the maximum age a slot can sit in the map before
// the reaper considers it abandoned. 5 minutes is the balance between
// (a) long enough that a legitimately long streaming request with
// pauses between tokens won't be swept, and (b) short enough that a
// stuck slot never becomes an unbounded memory leak.
const DefaultReapTTL = 5 * time.Minute

// RunReaper is the background cleanup goroutine that periodically
// scans the accountant snapshot and deletes slots older than ttl.
// Runs for the lifetime of ctx. Designed to be called from main()
// alongside Flusher.Run.
//
// ME-03 fix: without the reaper, a request whose upstream Close never
// fires (client abort pre-header, cold connection reset, panic in the
// interceptor chain) would leak its RequestUsage slot forever. The
// copy-on-write semantics of Accountant.Set mean every subsequent
// request pays O(n) map-rebuild cost for a growing n of stuck slots.
func (a *Accountant) RunReaper(ctx context.Context, tickInterval, ttl time.Duration, log *slog.Logger) {
	if tickInterval <= 0 {
		tickInterval = 60 * time.Second
	}
	if ttl <= 0 {
		ttl = DefaultReapTTL
	}
	if log == nil {
		log = slog.Default()
	}
	log = log.With("module", "BILLING_REAPER")
	tick := time.NewTicker(tickInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("accountant reaper exited")
			return
		case <-tick.C:
			reaped := a.Reap(ttl)
			if reaped > 0 {
				log.Warn("reaped abandoned accountant slots (interceptor Close never ran)",
					"count", reaped, "ttl", ttl)
			}
		}
	}
}

// Reap atomically deletes every slot whose createdAtUnixNano is older
// than now-ttl. Returns the number of deletions. Exported for test
// harnesses that want deterministic control without running a goroutine.
func (a *Accountant) Reap(ttl time.Duration) int {
	cutoff := time.Now().Add(-ttl).UnixNano()
	a.mu.Lock()
	defer a.mu.Unlock()
	old := *a.usages.Load()
	next := make(map[string]*RequestUsage, len(old))
	reaped := 0
	for k, v := range old {
		if v == nil {
			continue
		}
		if v.createdAtUnixNano.Load() < cutoff {
			reaped++
			continue
		}
		next[k] = v
	}
	if reaped == 0 {
		return 0
	}
	a.usages.Store(&next)
	return reaped
}
