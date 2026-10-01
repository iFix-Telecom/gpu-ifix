package shed

import "testing"

// Quick 260930-uru: Reset leaves the ring equivalent to a freshly built one.
func TestLatencyRing_Reset(t *testing.T) {
	r := NewLatencyRing(10)
	for i := uint32(1); i <= 25; i++ {
		r.Record(i * 100)
	}
	if r.P95() == 0 {
		t.Fatal("precondition: P95 must be > 0 after records")
	}
	r.Reset()
	if got := r.P95(); got != 0 {
		t.Fatalf("P95 after Reset = %d, want 0", got)
	}
	r.Record(7)
	if got := r.P95(); got != 7 {
		t.Fatalf("P95 after Reset+Record(7) = %d, want 7", got)
	}
	var nilRing *LatencyRing
	nilRing.Reset() // must not panic
}
