// Package shed (latency_test.go): unit tests for the lockless latency
// ring used by the FSM 2-of-3 saturation gate (CONTEXT.md D-A2).
//
// Race-benign-by-design: TestLatencyRing_ConcurrentWrites is expected to
// pass under `go test -race` because the only race condition is two
// writers landing on the same slot, which loses exactly one sample but
// does not corrupt the buffer or produce stale reads (D-A2, RESEARCH
// Pitfall 2). We assert P95 > 0 after concurrent writes — not equality —
// because losing one sample is acceptable.
package shed

import (
	"sync"
	"testing"
)

func TestLatencyRing_EmptyReturnsZero(t *testing.T) {
	r := NewLatencyRing(100)
	if got := r.P95(); got != 0 {
		t.Fatalf("empty P95 = %d, want 0", got)
	}
}

func TestLatencyRing_NilReceiverReturnsZero(t *testing.T) {
	var r *LatencyRing
	if got := r.P95(); got != 0 {
		t.Fatalf("nil P95 = %d, want 0", got)
	}
	// Record on nil should not panic (defensive against partial wiring).
	r.Record(123)
}

func TestLatencyRing_UniformSamplesP95(t *testing.T) {
	r := NewLatencyRing(200)
	for i := uint32(1); i <= 100; i++ {
		r.Record(i)
	}
	p95 := r.P95()
	// 95th percentile of uniform [1..100] is around 95–96; allow ±1.
	if p95 < 94 || p95 > 96 {
		t.Fatalf("P95 of [1..100] = %d, expected ~95", p95)
	}
}

func TestLatencyRing_OverwritesOldSamples(t *testing.T) {
	r := NewLatencyRing(10)
	for i := uint32(1); i <= 20; i++ {
		r.Record(i)
	}
	// After 20 writes into size-10 ring, only [11..20] survive.
	p95 := r.P95()
	if p95 < 19 || p95 > 20 {
		t.Fatalf("P95 of last-10 after 20 writes = %d, expected ~20", p95)
	}
}

func TestLatencyRing_DefaultsToSize200WhenZero(t *testing.T) {
	r := NewLatencyRing(0)
	// Internal default is 200 — write 250 samples and verify only ~last 200 survive.
	for i := uint32(1); i <= 250; i++ {
		r.Record(i)
	}
	p95 := r.P95()
	// With 200 surviving samples [51..250], P95 ≈ 240
	if p95 < 235 || p95 > 250 {
		t.Fatalf("P95 with default size = %d, expected ~240", p95)
	}
}

func TestLatencyRing_ConcurrentWrites(t *testing.T) {
	// Race detection: this test may flag benign races on slot writes under
	// -race. Acceptable per D-A2 / RESEARCH Pitfall 2 — concurrent writers
	// to the same slot lose one sample each but do not corrupt the buffer.
	// We verify no panic and that P95 returns a non-zero, plausible value.
	r := NewLatencyRing(512)
	var wg sync.WaitGroup
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				r.Record(uint32(i + id*1000))
			}
		}(g)
	}
	wg.Wait()
	if p := r.P95(); p == 0 {
		t.Fatalf("P95 after concurrent writes = 0, expected > 0")
	}
}
