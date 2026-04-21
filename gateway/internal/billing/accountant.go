package billing

import (
	"sync"
	"sync/atomic"
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
type RequestUsage struct {
	TokensIn         atomic.Int64
	TokensOut        atomic.Int64
	AudioSecondsMs10 atomic.Int64
	EmbedsCount      atomic.Int64
	model            atomic.Value // string
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
func (a *Accountant) Set(reqID string, u *RequestUsage) {
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
