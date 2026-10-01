// Package shed (latency.go): lockless ring buffer for per-upstream
// request latency samples. Read pattern is once-per-tick by the FSM
// (see fsm.go, Plan 05-03 Task 3.2); write pattern is once-per-response
// by the shed middleware (see middleware.go, Plan 05-06).
//
// Race-benign-by-design: two concurrent writers to the same slot lose
// one sample each. CONTEXT.md D-A2 and RESEARCH Pitfall 2 accept this
// explicitly — the alternative (mutex per Record) would put a lock on
// every response, which is far more expensive than losing 1 sample out
// of ~200 per upstream.
//
// Size is configured via SHED_LATENCY_RING_SIZE env (default 200,
// gateway/internal/config/config.go).
package shed

import (
	"sort"
	"sync/atomic"
)

// LatencyRing holds up to `size` latest latency samples (ms) for one
// upstream. The buffer is preallocated; size is fixed for the lifetime
// of the LatencyRing (no resize after construction).
//
// Slot access uses atomic.LoadUint32 / atomic.StoreUint32 so the Go race
// detector sees synchronized memory access. The "race-benign" property
// (D-A2) is logical, not data-race: two writers may collide on the same
// slot and one sample wins arbitrarily, but neither write tears or
// corrupts the buffer. Atomic ops give us this guarantee at the cost
// of one CPU memory barrier per write — still lockless.
type LatencyRing struct {
	buf  []uint32
	size uint64
	idx  atomic.Uint64 // monotonic write index; (idx-1)%size = current slot
}

// NewLatencyRing pre-allocates the buffer. A non-positive size defaults
// to 200 (CONTEXT D-A2 / config.ShedLatencyRingSize default).
func NewLatencyRing(size int) *LatencyRing {
	if size <= 0 {
		size = 200
	}
	return &LatencyRing{buf: make([]uint32, size), size: uint64(size)}
}

// Record stores a sample. Amortized O(1), lockless via atomic.StoreUint32.
// Slot collisions are logically race-benign: the last writer wins and the
// other sample is dropped (see package doc).
func (r *LatencyRing) Record(ms uint32) {
	if r == nil {
		return
	}
	i := r.idx.Add(1) - 1
	atomic.StoreUint32(&r.buf[i%r.size], ms)
}

// Reset zera a janela (quick 260930-uru: URL efetiva do upstream mudou, as
// latências do endereço antigo não valem para o novo). Mesma disciplina do
// Record: atomic.StoreUint32 por slot + idx.Store(0). Como P95 ignora slots
// zero, o ring fica semanticamente igual a um recém-criado. Um Record
// concorrente pode sobreviver ao Reset (1 amostra antiga) — aceitável, mesma
// classe de perda race-benign do pacote.
func (r *LatencyRing) Reset() {
	if r == nil {
		return
	}
	for i := range r.buf {
		atomic.StoreUint32(&r.buf[i], 0)
	}
	r.idx.Store(0)
}

// P95 returns the 95th percentile of currently-stored samples (in ms).
// Returns 0 if no samples have been written. The computation copies the
// buffer once (atomic.LoadUint32 per slot) and sorts the non-zero
// entries; this is O(n log n) but runs at most once per tick (1Hz per
// upstream), so total cost is trivial for n=200.
//
// Zero entries are treated as "no sample" — early in the ring's life
// the un-written slots stay at 0 and must not skew the percentile.
// Callers that want to record a literal 0ms latency should record 1 instead.
func (r *LatencyRing) P95() uint32 {
	if r == nil {
		return 0
	}
	snap := make([]uint32, r.size)
	for i := range snap {
		snap[i] = atomic.LoadUint32(&r.buf[i])
	}
	out := snap[:0]
	for _, v := range snap {
		if v > 0 {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return 0
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	idx := int(float64(len(out))*0.95 + 0.5)
	if idx >= len(out) {
		idx = len(out) - 1
	}
	return out[idx]
}
